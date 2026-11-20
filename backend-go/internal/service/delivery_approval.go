package service

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DeliveryApprovalGrant scopes an operator's production approval authority to
// one logical business application and environment. Administrators retain the
// existing platform-wide administrative authority.
type DeliveryApprovalGrant struct {
	UserID          int64     `json:"user_id"`
	Username        string    `json:"username"`
	ApplicationID   int64     `json:"application_id"`
	ApplicationName string    `json:"application_name"`
	EnvironmentID   int64     `json:"environment_id"`
	EnvironmentName string    `json:"environment_name"`
	GrantedBy       string    `json:"granted_by"`
	CreatedAt       time.Time `json:"created_at"`
}

// DeliveryApprovalGrantPersistence is optional so lightweight in-memory
// stores can use the same authorization rules without a database.
type DeliveryApprovalGrantPersistence interface {
	DeliveryApprovalGrants() ([]DeliveryApprovalGrant, error)
	ReplaceDeliveryApprovalGrants(userID int64, grants []DeliveryApprovalGrant, actor string) error
}

func (s *Store) LoadDeliveryApprovalGrants(grants []DeliveryApprovalGrant) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveryApprovalGrants = append([]DeliveryApprovalGrant(nil), grants...)
}

func (s *Store) DeliveryApprovalGrants() ([]DeliveryApprovalGrant, error) {
	s.mu.RLock()
	persistence := s.persistence
	grants := append([]DeliveryApprovalGrant(nil), s.deliveryApprovalGrants...)
	s.mu.RUnlock()
	if persistence != nil {
		repo, ok := persistence.(DeliveryApprovalGrantPersistence)
		if !ok {
			return nil, fmt.Errorf("持久化层不支持发布审批授权")
		}
		return repo.DeliveryApprovalGrants()
	}
	return grants, nil
}

// ReplaceDeliveryApprovalGrants replaces one operator's complete scope set.
// Every pair must refer to a reviewed production binding, so a grant cannot
// attach to an unconfirmed legacy target or a non-production environment.
func (s *Store) ReplaceDeliveryApprovalGrants(userID int64, scopes []DeliveryApprovalScope, actor string) ([]DeliveryApprovalGrant, error) {
	if userID <= 0 {
		return nil, NewValidationError("用户 ID 无效")
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return nil, NewValidationError("授权管理员不能为空")
	}
	user, err := s.FindUser(userID)
	if err != nil {
		return nil, err
	}
	if user.Role != RoleOperator {
		return nil, NewValidationError("发布审批范围只能授予 operator 账号")
	}
	if !user.Enabled && len(scopes) > 0 {
		return nil, NewValidationError("停用账号不能获得新的审批范围")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(scopes))
	rows := make([]DeliveryApprovalGrant, 0, len(scopes))
	now := time.Now().UTC()
	for _, scope := range scopes {
		key := fmt.Sprintf("%d/%d", scope.ApplicationID, scope.EnvironmentID)
		if scope.ApplicationID <= 0 || scope.EnvironmentID <= 0 {
			return nil, NewValidationError("业务应用和逻辑环境必须有效")
		}
		if _, ok := seen[key]; ok {
			return nil, NewValidationError("审批范围不能重复")
		}
		seen[key] = struct{}{}

		var app *DeliveryApplication
		for i := range s.delivery.Applications {
			if s.delivery.Applications[i].ID == scope.ApplicationID {
				app = &s.delivery.Applications[i]
				break
			}
		}
		var env *DeliveryEnvironment
		for i := range s.delivery.Environments {
			if s.delivery.Environments[i].ID == scope.EnvironmentID {
				env = &s.delivery.Environments[i]
				break
			}
		}
		if app == nil || env == nil {
			return nil, ErrNotFound
		}
		if env.Tier != "production" || env.RequiresReview {
			return nil, NewValidationError("审批范围只能授予已核对的生产环境")
		}
		bound := false
		for _, binding := range s.delivery.Bindings {
			if binding.ApplicationID != app.ID || binding.EnvironmentID != env.ID || binding.RequiresReview {
				continue
			}
			for _, component := range s.delivery.Components {
				if component.ID == binding.ComponentID && component.ApplicationID == app.ID && !component.RequiresReview {
					bound = true
					break
				}
			}
			if bound {
				break
			}
		}
		if !bound {
			return nil, NewValidationError("该业务应用在生产环境中没有已核对的组件绑定")
		}
		rows = append(rows, DeliveryApprovalGrant{
			UserID: user.ID, Username: user.Username,
			ApplicationID: app.ID, ApplicationName: app.Name,
			EnvironmentID: env.ID, EnvironmentName: env.Name,
			GrantedBy: actor, CreatedAt: now,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ApplicationID == rows[j].ApplicationID {
			return rows[i].EnvironmentID < rows[j].EnvironmentID
		}
		return rows[i].ApplicationID < rows[j].ApplicationID
	})
	if s.persistence != nil {
		repo, ok := s.persistence.(DeliveryApprovalGrantPersistence)
		if !ok {
			return nil, fmt.Errorf("持久化层不支持发布审批授权")
		}
		if err := repo.ReplaceDeliveryApprovalGrants(userID, rows, actor); err != nil {
			return nil, err
		}
	}
	retained := s.deliveryApprovalGrants[:0]
	for _, existing := range s.deliveryApprovalGrants {
		if existing.UserID != userID {
			retained = append(retained, existing)
		}
	}
	s.deliveryApprovalGrants = append(retained, rows...)
	return append([]DeliveryApprovalGrant(nil), rows...), nil
}

type DeliveryApprovalScope struct {
	ApplicationID int64 `json:"application_id"`
	EnvironmentID int64 `json:"environment_id"`
}

// CanApproveDeliveryTarget is checked by the service immediately before a
// human approval/rejection. An admin retains global authority; an operator
// must have an explicit grant for the target's confirmed application/env.
func (s *Store) CanApproveDeliveryTarget(username string, appID int) (bool, error) {
	user, err := s.FindUserByUsername(username)
	if err != nil {
		return false, err
	}
	if !user.Enabled {
		return false, nil
	}
	if user.Role == RoleAdmin {
		return true, nil
	}
	if user.Role != RoleOperator {
		return false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var binding *DeliveryBinding
	for i := range s.delivery.Bindings {
		if s.delivery.Bindings[i].AppID == appID {
			binding = &s.delivery.Bindings[i]
			break
		}
	}
	if binding == nil || binding.RequiresReview {
		return false, nil
	}
	var env *DeliveryEnvironment
	for i := range s.delivery.Environments {
		if s.delivery.Environments[i].ID == binding.EnvironmentID {
			env = &s.delivery.Environments[i]
			break
		}
	}
	if env == nil || env.Tier != "production" || env.RequiresReview {
		return false, nil
	}
	for _, grant := range s.deliveryApprovalGrants {
		if grant.UserID == user.ID && grant.ApplicationID == binding.ApplicationID && grant.EnvironmentID == binding.EnvironmentID {
			return true, nil
		}
	}
	return false, nil
}
