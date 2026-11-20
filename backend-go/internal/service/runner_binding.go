package service

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// RunnerBinding pins an automation credential to one existing component target.
// The secret is returned once; only its hash reaches persistence.
type RunnerBinding struct {
	ID                       int64      `json:"id"`
	AppID                    int        `json:"app_id"`
	PipelineID               string     `json:"pipeline_id"`
	PrivateClusterID         string     `json:"private_cluster_id"`
	PlatformURL              string     `json:"platform_url"`
	ConnectionID             int64      `json:"connection_id"`
	Binding                  App        `json:"binding"`
	EnvCode                  string     `json:"env_code"`
	TokenHash                string     `json:"-"`
	TokenPrefix              string     `json:"token_prefix"`
	Enabled                  bool       `json:"enabled"`
	ExpiresAt                time.Time  `json:"expires_at"`
	LastCheckAt              *time.Time `json:"last_check_at,omitempty"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	PipelineBackupCiphertext string     `json:"-"`
	LastAppliedAt            *time.Time `json:"last_applied_at,omitempty"`
}

type YunxiaoConnection struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	Edition         string    `json:"edition"`
	Domain          string    `json:"domain"`
	OrganizationID  string    `json:"organization_id"`
	TokenCiphertext string    `json:"-"`
	CreatedAt       time.Time `json:"created_at"`
}

type RunnerPersistence interface {
	RunnerBindings() ([]RunnerBinding, error)
	SaveRunnerBinding(RunnerBinding) (RunnerBinding, error)
	YunxiaoConnections() ([]YunxiaoConnection, error)
	SaveYunxiaoConnection(YunxiaoConnection) (YunxiaoConnection, error)
}

var runnerIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var runnerPipelineID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var ErrRunnerCredential = fmt.Errorf("接入密钥无效、已禁用或已过期")

func NewRunnerToken() (string, string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", "", err
	}
	token := "dpci_" + base64.RawURLEncoding.EncodeToString(raw)
	return token, HashAPIKey(token), token[:13], nil
}
func (s *Store) LoadRunnerBindings(rows []RunnerBinding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runnerBindings = rows
}
func (s *Store) RunnerBindings() []RunnerBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]RunnerBinding{}, s.runnerBindings...)
}
func (s *Store) RunnerBinding(id int64) (RunnerBinding, error) {
	for _, b := range s.RunnerBindings() {
		if b.ID == id {
			return b, nil
		}
	}
	return RunnerBinding{}, ErrNotFound
}
func (s *Store) RunnerTarget(b RunnerBinding) (App, Cluster, error) {
	a, err := s.FindApp(b.AppID)
	if err != nil {
		return a, Cluster{}, newForbidden("组件已删除，请重新接入")
	}
	c, err := s.FindCluster(a.ClusterID)
	if err != nil {
		return a, c, err
	}
	if !c.Enabled || c.EnvironmentTier != "non_production" || s.AppEnvironmentTier(a.ID) != "non_production" || c.DeployPolicy != "automatic" {
		return a, c, newForbidden("此接入仅允许已启用的非生产自动发布环境")
	}
	if !sameRunnerTarget(a, b.Binding) || c.EnvCode != b.EnvCode {
		return a, c, newForbidden("组件绑定已变更，请重新生成接入配置")
	}
	if a.ImageRepository == "" {
		return a, c, NewValidationError("请先补全组件镜像仓库")
	}
	return a, c, nil
}
func validateRunnerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", NewValidationError("平台地址须为 http/https 根地址，不能包含凭据、路径或查询参数")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}
func (s *Store) ConfigureRunner(b RunnerBinding) (RunnerBinding, string, error) {
	a, err := s.FindApp(b.AppID)
	if err != nil {
		return b, "", err
	}
	c, err := s.FindCluster(a.ClusterID)
	if err != nil {
		return b, "", err
	}
	b.Binding = a
	b.EnvCode = c.EnvCode
	if _, _, err = s.RunnerTarget(b); err != nil {
		return b, "", err
	}
	if !runnerPipelineID.MatchString(b.PipelineID) || !runnerIdentifier.MatchString(b.PrivateClusterID) {
		return b, "", NewValidationError("流水线 ID 须为数字，私有构建集群 ID 只能包含字母、数字、下划线和连字符")
	}
	b.PlatformURL, err = validateRunnerURL(b.PlatformURL)
	if err != nil {
		return b, "", err
	}
	if b.ConnectionID != 0 {
		if _, err = s.YunxiaoConnection(b.ConnectionID); err != nil {
			return b, "", NewValidationError("云效连接不存在")
		}
	}
	if b.ExpiresAt.IsZero() {
		b.ExpiresAt = time.Now().Add(90 * 24 * time.Hour)
	}
	if !b.ExpiresAt.After(time.Now()) || b.ExpiresAt.After(time.Now().Add(366*24*time.Hour)) {
		return b, "", NewValidationError("接入授权期限须在未来一年以内")
	}
	token, hash, prefix, err := NewRunnerToken()
	if err != nil {
		return b, "", err
	}
	b.TokenHash = hash
	b.TokenPrefix = prefix
	b.Enabled = true
	b.LastCheckAt = nil
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.runnerBindings {
		if old.AppID == b.AppID && old.ID != b.ID {
			return b, "", newConflict("此组件已有 Runner 接入，请编辑已有配置")
		}
	}
	if b.ID != 0 {
		found := false
		for _, old := range s.runnerBindings {
			if old.ID == b.ID {
				if old.AppID != b.AppID {
					return b, "", NewValidationError("接入配置不能改为其他组件")
				}
				b.CreatedAt = old.CreatedAt
				b.PipelineBackupCiphertext = old.PipelineBackupCiphertext
				b.LastAppliedAt = old.LastAppliedAt
				found = true
			}
		}
		if !found {
			return b, "", ErrNotFound
		}
	}
	b, err = s.saveRunnerLocked(b)
	return b, token, err
}
func (s *Store) saveRunnerLocked(b RunnerBinding) (RunnerBinding, error) {
	now := time.Now()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	if s.persistence != nil {
		p, ok := s.persistence.(RunnerPersistence)
		if !ok {
			return b, fmt.Errorf("Runner 接入存储不可用")
		}
		v, err := p.SaveRunnerBinding(b)
		if err != nil {
			return b, err
		}
		b = v
	}
	if b.ID == 0 {
		b.ID = 1
		for _, old := range s.runnerBindings {
			if old.ID >= b.ID {
				b.ID = old.ID + 1
			}
		}
	}
	for i, old := range s.runnerBindings {
		if old.ID == b.ID {
			s.runnerBindings[i] = b
			return b, nil
		}
	}
	s.runnerBindings = append(s.runnerBindings, b)
	return b, nil
}
func (s *Store) SetRunnerEnabled(id int64, enabled bool) (RunnerBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.runnerBindings {
		if b.ID == id {
			b.Enabled = enabled
			return s.saveRunnerLocked(b)
		}
	}
	return RunnerBinding{}, ErrNotFound
}
func (s *Store) ResolveRunner(id int64, token string) (RunnerBinding, error) {
	b, err := s.RunnerBinding(id)
	if err != nil || !strings.HasPrefix(token, "dpci_") || !b.Enabled || !time.Now().Before(b.ExpiresAt) || subtle.ConstantTimeCompare([]byte(b.TokenHash), []byte(HashAPIKey(token))) != 1 {
		return RunnerBinding{}, ErrRunnerCredential
	}
	if _, _, err = s.RunnerTarget(b); err != nil {
		return b, err
	}
	return b, nil
}
func (s *Store) MarkRunnerChecked(id int64, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.runnerBindings {
		if b.ID == id {
			if b.TokenHash != tokenHash || !b.Enabled {
				return ErrRunnerCredential
			}
			now := time.Now()
			b.LastCheckAt = &now
			_, err := s.saveRunnerLocked(b)
			return err
		}
	}
	return ErrNotFound
}
func (s *Store) RunnerRelease(b RunnerBinding, id int64) (ImageRelease, DeployLog, error) {
	for _, r := range s.ImageReleases("", 0) {
		if r.ID == id && r.Source == fmt.Sprintf("runner:%d", b.ID) {
			l, err := s.FindLog(r.TestDeployLogID)
			return r, l, err
		}
	}
	return ImageRelease{}, DeployLog{}, ErrNotFound
}
func (s *Store) LoadYunxiaoConnections(rows []YunxiaoConnection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.yunxiaoConnections = rows
}
func (s *Store) YunxiaoConnections() []YunxiaoConnection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]YunxiaoConnection{}, s.yunxiaoConnections...)
}
func (s *Store) YunxiaoConnection(id int64) (YunxiaoConnection, error) {
	for _, c := range s.YunxiaoConnections() {
		if c.ID == id {
			return c, nil
		}
	}
	return YunxiaoConnection{}, ErrNotFound
}
func (s *Store) SaveYunxiaoConnection(c YunxiaoConnection) (YunxiaoConnection, error) {
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 64 || c.TokenCiphertext == "" {
		return c, NewValidationError("连接名称和访问令牌必填")
	}
	if err := ValidateYunxiaoEndpoint(c); err != nil {
		return c, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c.CreatedAt = time.Now()
	if s.persistence != nil {
		p, ok := s.persistence.(RunnerPersistence)
		if !ok {
			return c, fmt.Errorf("云效连接存储不可用")
		}
		v, err := p.SaveYunxiaoConnection(c)
		if err != nil {
			return c, err
		}
		c = v
	}
	if c.ID == 0 {
		c.ID = 1
		for _, old := range s.yunxiaoConnections {
			if old.ID >= c.ID {
				c.ID = old.ID + 1
			}
		}
	}
	s.yunxiaoConnections = append(s.yunxiaoConnections, c)
	return c, nil
}
func (s *Store) SaveRunnerPipelineBackup(expected RunnerBinding, before string) (RunnerBinding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.runnerBindings {
		if b.ID == expected.ID {
			if !b.UpdatedAt.Equal(expected.UpdatedAt) {
				return RunnerBinding{}, newConflict("接入配置已变化，请重新预览")
			}
			b.PipelineBackupCiphertext = before
			return s.saveRunnerLocked(b)
		}
	}
	return RunnerBinding{}, ErrNotFound
}
func (s *Store) MarkRunnerPipelineApplied(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.runnerBindings {
		if b.ID == id {
			now := time.Now()
			b.LastAppliedAt = &now
			_, err := s.saveRunnerLocked(b)
			return err
		}
	}
	return ErrNotFound
}

func sameRunnerTarget(a, b App) bool {
	return a.ID == b.ID && a.ClusterID == b.ClusterID && a.AppName == b.AppName && a.Namespace == b.Namespace && a.DeploymentName == b.DeploymentName && a.ContainerName == b.ContainerName && a.ImageRepository == b.ImageRepository && a.SourceKind == b.SourceKind && a.SourceRepoURL == b.SourceRepoURL && a.SourcePath == b.SourcePath && a.DeploymentConfigRepoURL == b.DeploymentConfigRepoURL && a.DeploymentConfigPath == b.DeploymentConfigPath
}

// Caller holds Store.mu. Recheck authorization and component mapping at the
// atomic release insertion, so rotation, disable and target edits cannot race
// a credential that was authenticated earlier in the HTTP request.
func (s *Store) validateRunnerReleaseLocked(r ImageRelease) error {
	var b RunnerBinding
	found := false
	for _, candidate := range s.runnerBindings {
		if r.Source == fmt.Sprintf("runner:%d", candidate.ID) {
			b = candidate
			found = true
			break
		}
	}
	if !found || !b.Enabled || !time.Now().Before(b.ExpiresAt) || b.TokenHash != r.RunnerAuthorizationHash {
		return ErrRunnerCredential
	}
	var a App
	for _, candidate := range s.apps {
		if candidate.ID == b.AppID {
			a = candidate
			break
		}
	}
	if r.Metadata.Binding == nil || !sameRunnerTarget(a, b.Binding) || !sameRunnerTarget(a, *r.Metadata.Binding) {
		return newForbidden("组件绑定已变更，请重新生成接入配置")
	}
	allowed := false
	for _, c := range s.clusters {
		if c.ID == a.ClusterID && c.Enabled && c.EnvCode == b.EnvCode && c.EnvironmentTier == "non_production" && c.DeployPolicy == "automatic" {
			allowed = true
		}
	}
	if allowed && s.appEnvironmentTierLocked(a.ID) != "non_production" {
		allowed = false
	}
	if !allowed {
		return newForbidden("此接入仅允许非生产自动环境")
	}
	if r.PipelineID == nil || *r.PipelineID != b.PipelineID || r.BuildNumber == nil || !runnerPipelineID.MatchString(*r.BuildNumber) {
		return NewValidationError("流水线或构建编号与接入不匹配")
	}
	if _, ok := s.findReleaseByKeyLocked(r.EventKey); ok {
		return nil
	}
	for _, old := range s.releases {
		if old.Source == r.Source && old.PipelineID != nil && *old.PipelineID == b.PipelineID && old.BuildNumber != nil {
			previous, current := strings.TrimLeft(*old.BuildNumber, "0"), strings.TrimLeft(*r.BuildNumber, "0")
			if len(previous) > len(current) || (len(previous) == len(current) && previous >= current) {
				return newConflict("已有更新构建被接收，拒绝旧构建覆盖")
			}
		}
	}
	return nil
}
