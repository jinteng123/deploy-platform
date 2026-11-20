package service

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// TransitionDeliveryRun atomically appends a user-requested stage transition
// and updates the compatibility deploy log. expectedVersion prevents two
// operators from applying conflicting controls to the same observed state;
// event.EventKey makes a retry of the same request idempotent.
func (s *Store) TransitionDeliveryRun(log DeployLog, expectedVersion int64, event DeliveryRunEvent) (DeliveryRun, bool, error) {
	return s.transitionDeliveryRun(log, expectedVersion, event, nil)
}

// TransitionDeliveryRunWithWorkloadPromotion keeps the app's active Deployment,
// its persisted workload UID, and the successful run transition in one storage
// transaction. A blue-green cutover must never expose a half-updated binding.
func (s *Store) TransitionDeliveryRunWithWorkloadPromotion(log DeployLog, expectedVersion int64, event DeliveryRunEvent, promotion DeliveryBindingWorkloadPromotion) (DeliveryRun, bool, error) {
	return s.transitionDeliveryRun(log, expectedVersion, event, &promotion)
}

func (s *Store) transitionDeliveryRun(log DeployLog, expectedVersion int64, event DeliveryRunEvent, promotion *DeliveryBindingWorkloadPromotion) (DeliveryRun, bool, error) {
	if log.ID <= 0 || expectedVersion <= 0 || event.EventKey == "" || event.EventType == "" || event.Stage == "" || event.Status == "" || !deliveryRunStatusProjectionMatches(event.Status, log.Status) {
		return DeliveryRun{}, false, NewValidationError("发布控制动作缺少有效版本或状态字段")
	}
	if promotion != nil && (event.Status != "success" || event.Stage != "success") {
		return DeliveryRun{}, false, NewValidationError("活动资源引用只能随成功的蓝绿确认一起更新")
	}
	if len(event.EventKey) > 160 || len(event.Detail) > 2000 || !utf8ValidString(event.Detail) {
		return DeliveryRun{}, false, NewValidationError("发布控制动作的幂等键或说明无效")
	}
	if err := validateDeliveryRunEventPayload(event.Payload); err != nil {
		return DeliveryRun{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	logIndex, runIndex := -1, -1
	for i := range s.logs {
		if s.logs[i].ID == log.ID {
			logIndex = i
			break
		}
	}
	for i := range s.deliveryRuns {
		if s.deliveryRuns[i].DeployLogID == log.ID {
			runIndex = i
			break
		}
	}
	if logIndex < 0 || runIndex < 0 {
		return DeliveryRun{}, false, ErrNotFound
	}
	current := s.deliveryRuns[runIndex]
	for _, existing := range s.deliveryRunEvents[current.ID] {
		if existing.EventKey != event.EventKey {
			continue
		}
		if !sameDeliveryRunEventContent(existing, event) {
			return current, true, fmt.Errorf("%w: 发布控制动作幂等键已用于不同状态", ErrConflict)
		}
		return cloneDeliveryRun(current), true, nil
	}
	if current.Version != expectedVersion {
		return current, true, fmt.Errorf("%w: 发布运行版本已从 %d 更新到 %d", ErrConflict, expectedVersion, current.Version)
	}
	if isTerminalDeliveryRunStatus(current.Status) && event.Status != current.Status {
		return current, true, newConflict("已结束的发布运行不能从 %s 改回 %s", current.Status, event.Status)
	}
	claimID := s.workerClaimIDLocked(log.ID)
	if claimID != "" && !s.workerClaimIsCurrentLocked(log.ID, claimID) {
		return current, true, newConflict("Worker 任务认领已撤销，拒绝写入发布控制状态")
	}
	if canaryPreWriteCancellation(current, event) {
		if current.Status != "queued" && current.Status != "running" {
			return current, true, newConflict("金丝雀运行已离开可安全取消阶段")
		}
		currentLog := s.logs[logIndex]
		if currentLog.Status != "pending" && currentLog.Status != "running" {
			return current, true, newConflict("金丝雀任务已结束，不能取消")
		}
		if currentLog.Status == "running" && (claimID == "" || currentLog.WorkerClaimID != claimID) {
			return current, true, newConflict("无法确认 Worker 认领，不能安全取消金丝雀任务")
		}
		if len(s.executionActions[log.ID]) > 0 {
			return current, true, newConflict("金丝雀已有外部动作记录，请通过适配器恢复稳定流量")
		}
	} else if current.Status == "queued" && event.Status == "cancelled" && s.logs[logIndex].Status != "pending" {
		return current, true, newConflict("Worker 已领取发布任务，不能再按排队状态取消")
	}
	appIndex, bindingIndex := -1, -1
	if promotion != nil {
		var err error
		appIndex, bindingIndex, err = s.validateDeliveryBindingPromotionLocked(*promotion)
		if err != nil {
			return current, true, err
		}
	}
	if s.persistence != nil {
		persistence, ok := s.persistence.(VersionedDeliveryRunExecutionPersistence)
		if !ok {
			return current, true, NewValidationError("持久化层不支持带版本校验的发布控制动作")
		}
		var savedRun DeliveryRun
		var savedEvent DeliveryRunEvent
		var duplicate bool
		var err error
		if claimID != "" {
			log.WorkerClaimID = claimID
			fenced, supported := s.persistence.(FencedVersionedDeliveryRunExecutionPersistence)
			if !supported {
				return current, true, NewValidationError("持久化层不支持 Worker 认领 fencing")
			}
			if promotion != nil {
				savedRun, savedEvent, duplicate, err = fenced.TransitionDeliveryRunExecutionWithWorkloadPromotionAndClaim(log, expectedVersion, event, *promotion, claimID)
			} else {
				savedRun, savedEvent, duplicate, err = fenced.TransitionDeliveryRunExecutionWithClaim(log, expectedVersion, event, claimID)
			}
		} else if promotion != nil {
			promotionPersistence, supported := s.persistence.(DeliveryRunWorkloadPromotionPersistence)
			if !supported {
				return current, true, NewValidationError("持久化层不支持原子更新活动资源引用")
			}
			savedRun, savedEvent, duplicate, err = promotionPersistence.TransitionDeliveryRunExecutionWithWorkloadPromotion(log, expectedVersion, event, *promotion)
		} else {
			savedRun, savedEvent, duplicate, err = persistence.TransitionDeliveryRunExecution(log, expectedVersion, event)
		}
		if err != nil {
			return current, true, err
		}
		if duplicate {
			if promotion != nil {
				if err := s.applyDeliveryBindingPromotionLocked(appIndex, bindingIndex, *promotion); err != nil {
					return current, true, err
				}
			}
			// A prior COMMIT may have reached MySQL even though its response was
			// lost. The durable repository recognizes the idempotency key and
			// returns the committed run/event here; refresh every in-memory view
			// before reporting success so progress and terminal resource locks do
			// not remain stale until a process restart.
			savedLog := log
			if reader, ok := s.persistence.(interface {
				LogByID(int64) (DeployLog, error)
			}); ok {
				persistedLog, readErr := reader.LogByID(log.ID)
				if readErr != nil {
					return current, true, readErr
				}
				savedLog = persistedLog
			}
			s.deliveryRuns[runIndex] = cloneDeliveryRun(savedRun)
			s.upsertDeliveryRunEventLocked(savedEvent)
			s.applyDeployLogExecutionLocked(logIndex, savedLog)
			if isTerminalDeployStatus(savedLog.Status) {
				s.releaseResourceLocksLocked(log.ID)
				s.captureTerminalLogLocked(savedLog)
			}
			return cloneDeliveryRun(savedRun), true, nil
		}
		if promotion != nil {
			if err := s.applyDeliveryBindingPromotionLocked(appIndex, bindingIndex, *promotion); err != nil {
				return current, true, err
			}
		}
		s.deliveryRuns[runIndex] = cloneDeliveryRun(savedRun)
		s.upsertDeliveryRunEventLocked(savedEvent)
		s.applyDeployLogExecutionLocked(logIndex, log)
		if isTerminalDeployStatus(log.Status) {
			s.releaseResourceLocksLocked(log.ID)
			s.captureTerminalLogLocked(log)
		}
		return cloneDeliveryRun(savedRun), true, nil
	}
	now := time.Now().UTC()
	event.RunID = current.ID
	event.CreatedAt = now
	event.Version = current.Version + 1
	current.Version = event.Version
	current.Status = event.Status
	current.CurrentStage = event.Stage
	current.UpdatedAt = now
	switch event.Status {
	case "failed", "interrupted":
		current.ErrorMessage = strPtr(event.Detail)
	default:
		current.ErrorMessage = nil
	}
	s.deliveryRuns[runIndex] = cloneDeliveryRun(current)
	if promotion != nil {
		if err := s.applyDeliveryBindingPromotionLocked(appIndex, bindingIndex, *promotion); err != nil {
			return DeliveryRun{}, false, err
		}
	}
	s.upsertDeliveryRunEventLocked(event)
	s.applyDeployLogExecutionLocked(logIndex, log)
	if isTerminalDeployStatus(log.Status) {
		s.releaseResourceLocksLocked(log.ID)
		s.captureTerminalLogLocked(log)
	}
	return cloneDeliveryRun(current), true, nil
}

// Delivery runs can represent operator states more precisely than the legacy
// deploy log vocabulary. Paused runs keep the compatibility task running so
// its existing resource reservation stays active; cancelled runs project to
// failed in the legacy log while the orchestration record reports cancellation.
func deliveryRunStatusProjectionMatches(runStatus, deployStatus string) bool {
	return runStatus == deployStatus ||
		(runStatus == "running" && deployStatus == "pending") ||
		(runStatus == "paused" && deployStatus == "running") ||
		(runStatus == "cancelled" && deployStatus == "failed")
}

func (s *Store) validateDeliveryBindingPromotionLocked(promotion DeliveryBindingWorkloadPromotion) (int, int, error) {
	ref := promotion.ActiveWorkloadRef
	if promotion.AppID <= 0 || promotion.ExpectedDeployment == "" || promotion.ExpectedActiveWorkloadRef.UID == "" || promotion.ExpectedActiveWorkloadRef.Name != promotion.ExpectedDeployment || ref.ClusterID <= 0 || ref.Namespace == "" || ref.Kind != "Deployment" || ref.Name == "" || ref.UID == "" {
		return -1, -1, NewValidationError("蓝绿晋级的活动 Deployment 身份不完整")
	}
	appIndex, bindingIndex := -1, -1
	for i := range s.apps {
		if s.apps[i].ID == promotion.AppID {
			appIndex = i
			break
		}
	}
	for i := range s.delivery.Bindings {
		if s.delivery.Bindings[i].AppID == promotion.AppID {
			bindingIndex = i
			break
		}
	}
	if appIndex < 0 || bindingIndex < 0 {
		return -1, -1, ErrNotFound
	}
	app := s.apps[appIndex]
	if app.DeploymentName != promotion.ExpectedDeployment || app.ClusterID != promotion.ExpectedActiveWorkloadRef.ClusterID || app.Namespace != promotion.ExpectedActiveWorkloadRef.Namespace {
		return -1, -1, newConflict("生产应用或活动资源引用已变化，不能确认蓝绿发布")
	}
	if !sameDeliveryWorkloadIdentity(s.delivery.Bindings[bindingIndex].ActiveWorkloadRef, promotion.ExpectedActiveWorkloadRef) {
		return -1, -1, newConflict("活动 Deployment UID 已变化，不能确认蓝绿发布")
	}
	if ref.ClusterID != app.ClusterID || ref.Namespace != app.Namespace {
		return -1, -1, NewValidationError("新活动 Deployment 不属于当前集群或命名空间")
	}
	return appIndex, bindingIndex, nil
}

func (s *Store) applyDeliveryBindingPromotionLocked(appIndex, bindingIndex int, promotion DeliveryBindingWorkloadPromotion) error {
	app, binding := s.apps[appIndex], s.delivery.Bindings[bindingIndex]
	if app.DeploymentName == promotion.ActiveWorkloadRef.Name && sameDeliveryWorkloadIdentity(binding.ActiveWorkloadRef, promotion.ActiveWorkloadRef) {
		return nil
	}
	if app.DeploymentName != promotion.ExpectedDeployment || !sameDeliveryWorkloadIdentity(binding.ActiveWorkloadRef, promotion.ExpectedActiveWorkloadRef) {
		return newConflict("活动资源绑定已变化，未应用蓝绿晋级")
	}
	app.DeploymentName = promotion.ActiveWorkloadRef.Name
	binding.ActiveWorkloadRef = promotion.ActiveWorkloadRef
	s.apps[appIndex] = app
	s.delivery.Bindings[bindingIndex] = binding
	return nil
}

func (s *Store) applyDeployLogExecutionLocked(index int, log DeployLog) {
	current := s.logs[index]
	current.Status = log.Status
	current.CurrentStep = log.CurrentStep
	current.ImageFull = log.ImageFull
	current.StepDetail = log.StepDetail
	current.ErrorMessage = log.ErrorMessage
	current.StartedAt = log.StartedAt
	current.FinishedAt = log.FinishedAt
	if isTerminalDeployStatus(log.Status) {
		current.WorkerClaimID = ""
	} else if log.WorkerClaimID != "" {
		current.WorkerClaimID = log.WorkerClaimID
	}
	s.logs[index] = current
}

func utf8ValidString(value string) bool {
	return utf8.ValidString(value)
}

func canaryPreWriteCancellation(run DeliveryRun, event DeliveryRunEvent) bool {
	return run.Strategy == "gateway_canary" && event.EventType == "control" && event.Stage == "cancelled" &&
		event.Status == "cancelled" && strings.HasPrefix(event.Detail, "action=cancel;actor=")
}
