package service

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var ErrNotFound = errors.New("not found")

// ErrConflict marks uniqueness violations; handlers map it to 409.
var ErrConflict = errors.New("conflict")

// ErrValidation marks business-rule violations; handlers map it to 400.
var ErrValidation = errors.New("validation")

// ErrForbidden marks an authenticated-but-not-allowed action (e.g. an
// approver deciding their own promotion request); handlers map it to 403.
var ErrForbidden = errors.New("forbidden")

type forbiddenError struct{ msg string }

func (e *forbiddenError) Error() string { return e.msg }
func (e *forbiddenError) Unwrap() error { return ErrForbidden }
func newForbidden(format string, args ...any) error {
	return &forbiddenError{msg: fmt.Sprintf(format, args...)}
}

type conflictError struct{ msg string }
type validationError struct{ msg string }

func (e *conflictError) Error() string   { return e.msg }
func (e *conflictError) Unwrap() error   { return ErrConflict }
func (e *validationError) Error() string { return e.msg }
func (e *validationError) Unwrap() error { return ErrValidation }

func newConflict(format string, args ...any) error {
	return &conflictError{msg: fmt.Sprintf(format, args...)}
}

// NewValidationError builds an ErrValidation-wrapping message error.
func NewValidationError(format string, args ...any) error {
	return &validationError{msg: fmt.Sprintf(format, args...)}
}

// Persistence mirrors writes into durable storage. Every method must fail
// loudly; the in-memory store never swallows a persistence error.
type Persistence interface {
	SaveCluster(Cluster) (Cluster, error)
	UpdateCluster(Cluster) error
	DeleteCluster(id int) error
	SaveApp(App) (App, error)
	UpdateApp(App) error
	DeleteApp(id int) error
	SaveLog(DeployLog) (DeployLog, error)
	// UpdateLogExecution atomically persists the execution fields of a log:
	// status/current_step/image_full/step_detail/error_message/started_at/finished_at.
	UpdateLogExecution(DeployLog) error
	// CreateImageRelease atomically inserts the test deploy log and the
	// release. created=false means the event_key already existed; the
	// returned release is the previously stored one.
	CreateImageRelease(ImageRelease, DeployLog) (release ImageRelease, log DeployLog, created bool, err error)
	// PromoteImageRelease atomically inserts the prod deploy log and links it
	// to the release, failing with ErrConflict when an active prod deploy
	// (pending/running/success) already exists.
	PromoteImageRelease(id int64, prodClusterID int, log DeployLog) (ImageRelease, DeployLog, error)
	// SaveUser/UpdateUser persist accounts. Login sessions use the optional
	// SessionPersistence contract in auth.go.
	SaveUser(User) (User, error)
	UpdateUser(User) error
	// SaveAudit persists one audit row.
	SaveAudit(AuditEntry) error
	// API key lifecycle — hash-only rows, plaintext never reaches the DB.
	SaveAPIKey(APIKey) (APIKey, error)
	UpdateAPIKey(APIKey) error
	DeleteAPIKey(id int64) error
	// TouchAPIKey persists last_used_at (throttled by the Store).
	TouchAPIKey(id int64, at time.Time) error
	// Promotion request lifecycle — decided rows update in place.
	SavePromotionRequest(PromotionRequest) (PromotionRequest, error)
	UpdatePromotionRequest(PromotionRequest) error
	ApprovePromotion(PromotionRequest, DeployLog) (PromotionRequest, ImageRelease, DeployLog, error)
	UpdateReleaseMetadata(id int64, metadata ReleaseMetadata) error
	ClaimLog(id int64) (bool, error)
}

type Store struct {
	runnerBindings                  []RunnerBinding
	yunxiaoConnections              []YunxiaoConnection
	notifications                   []DeliveryNotification
	notificationCaptureEnabled      bool
	notificationCaptureFailures     uint64
	notificationPublicURL           string
	mu                              sync.RWMutex
	nextCluster                     int
	nextApp                         int
	nextLog                         int64
	nextRelease                     int64
	nextDeliveryEnvironment         int64
	nextDeliveryApplication         int64
	nextDeliveryComponent           int64
	nextDeliveryBinding             int64
	nextDeliveryPlan                int64
	nextDeliveryRun                 int64
	clusters                        []Cluster
	apps                            []App
	delivery                        DeliveryModel
	deliveryPolicyVersions          map[int64][]DeliveryPolicyVersion
	deliveryEvidencePolicies        map[int64][]DeliveryEnvironmentEvidencePolicyVersion
	pipelineScopeReviews            []DeliveryPipelineScopeReview
	nextDeliveryPipelineScopeReview int64
	deliveryPlans                   []DeliveryPlan
	deliveryRuns                    []DeliveryRun
	deliveryRunEvents               map[int64][]DeliveryRunEvent
	deliveryIncidents               []DeliveryIncident
	deliveryIncidentEvents          map[int64][]DeliveryIncidentEvent
	nextDeliveryIncident            int64
	resourceLocks                   map[string]int64
	resourceLockLeases              map[string]time.Time
	executionActions                map[int64][]ExecutionAction
	workerClaims                    map[int64]string
	driftStates                     map[string]DeliveryDriftState
	driftEvents                     []DeliveryDriftEvent
	nextDriftEventID                int64
	logs                            []DeployLog
	releases                        []ImageRelease
	promotions                      []PromotionRequest
	nextPromo                       int64
	persistence                     Persistence
	auth                            authState
	audit                           auditState
	apiKeys                         apiKeyState
	deliveryApprovalGrants          []DeliveryApprovalGrant
}

func NewStore() *Store {
	now := time.Now()
	return &Store{
		nextCluster: 2,
		nextApp:     1,
		nextLog:     1,
		nextRelease: 1,
		clusters: []Cluster{{
			ID:               1,
			Name:             "本地环境",
			EnvCode:          "local",
			Environment:      "local",
			EnvironmentTier:  "non_production",
			DeployPolicy:     "automatic",
			ConnectionType:   "legacy_ssh",
			Enabled:          true,
			K8sMasterHost:    "127.0.0.1",
			K8sMasterPort:    22,
			K8sMasterUser:    "kubeconfig",
			DefaultNamespace: "default",
			CreatedAt:        now,
			UpdatedAt:        now,
		}},
		delivery: legacyDeliveryModel([]Cluster{{
			ID: 1, Name: "本地环境", Environment: "local", EnvironmentTier: "non_production", CreatedAt: now,
		}}, nil),
		nextDeliveryEnvironment:         2,
		nextDeliveryApplication:         1,
		nextDeliveryComponent:           1,
		nextDeliveryBinding:             1,
		nextDeliveryPlan:                1,
		nextDeliveryRun:                 1,
		nextDeliveryIncident:            1,
		nextDeliveryPipelineScopeReview: 1,
		resourceLocks:                   map[string]int64{},
		resourceLockLeases:              map[string]time.Time{},
		executionActions:                map[int64][]ExecutionAction{},
		workerClaims:                    map[int64]string{},
	}
}

func NewStoreWithData(clusters []Cluster, apps []App, logs []DeployLog, releases []ImageRelease, persistence Persistence) *Store {
	s := &Store{nextCluster: 1, nextApp: 1, nextLog: 1, nextRelease: 1, nextDeliveryPlan: 1, nextDeliveryRun: 1, nextDeliveryIncident: 1, nextDeliveryPipelineScopeReview: 1, clusters: clusters, apps: apps, logs: logs, releases: releases, persistence: persistence, delivery: legacyDeliveryModel(clusters, apps), auth: authState{sessions: map[string]Session{}}, audit: auditState{nextID: 1}, resourceLocks: map[string]int64{}, resourceLockLeases: map[string]time.Time{}, executionActions: map[int64][]ExecutionAction{}, workerClaims: map[int64]string{}}
	s.nextDeliveryEnvironment = nextEnvironmentID(s.delivery.Environments)
	s.nextDeliveryApplication = nextApplicationID(s.delivery.Applications)
	s.nextDeliveryComponent = nextComponentID(s.delivery.Components)
	s.nextDeliveryBinding = nextBindingID(s.delivery.Bindings)
	s.nextDeliveryPlan = 1
	for _, c := range clusters {
		if c.ID >= s.nextCluster {
			s.nextCluster = c.ID + 1
		}
	}
	for _, a := range apps {
		if a.ID >= s.nextApp {
			s.nextApp = a.ID + 1
		}
	}
	for _, l := range logs {
		if l.Status == "pending" || l.Status == "running" {
			if locks, err := NormalizeResourceLocks([]ResourceLock{workloadResourceLock(l)}); err == nil {
				s.addResourceLocksLocked(l.ID, locks)
			}
		}
		if l.ID >= s.nextLog {
			s.nextLog = l.ID + 1
		}
	}
	for _, r := range releases {
		if r.ID >= s.nextRelease {
			s.nextRelease = r.ID + 1
		}
	}
	return s
}

func (s *Store) Clusters() []Cluster {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Non-nil so an empty list serializes as [] rather than null.
	return append([]Cluster{}, s.clusters...)
}

func (s *Store) Apps(clusterID int) []App {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]App, 0, len(s.apps))
	for _, app := range s.apps {
		if clusterID == 0 || app.ClusterID == clusterID {
			result = append(result, app)
		}
	}
	return result
}

// Logs returns matching logs ordered by id desc, capped by limit when > 0.
func (s *Store) Logs(clusterID int, appName, status string, limit int) []DeployLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]DeployLog, 0, len(s.logs))
	for i := len(s.logs) - 1; i >= 0; i-- {
		l := s.logs[i]
		if (clusterID == 0 || l.ClusterID == clusterID) && (appName == "" || l.AppName == appName) && (status == "" || l.Status == status) {
			result = append(result, l)
			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}
	return result
}

// PendingDeployLogIDsAfter returns an ascending, bounded page of pending
// deployment IDs. Persistent stores use an indexed database scan; memory mode
// uses the same ordering over its local log snapshot.
func (s *Store) PendingDeployLogIDsAfter(afterID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = 100
	}
	if scanner, ok := s.persistence.(PendingDeployLogScanner); ok {
		return scanner.PendingDeployLogIDsAfter(afterID, limit)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]int64, 0, limit)
	for _, l := range s.logs {
		if l.ID <= afterID || l.Status != "pending" {
			continue
		}
		ids = append(ids, l.ID)
		if len(ids) == limit {
			break
		}
	}
	return ids, nil
}

func (s *Store) FindCluster(id int) (Cluster, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.clusters {
		if c.ID == id {
			return c, nil
		}
	}
	return Cluster{}, fmt.Errorf("%w: 集群 ID=%d 不存在", ErrNotFound, id)
}

func (s *Store) FindApp(id int) (App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.apps {
		if a.ID == id {
			return a, nil
		}
	}
	return App{}, fmt.Errorf("%w: 应用 ID=%d 不存在", ErrNotFound, id)
}

func (s *Store) FindLog(id int64) (DeployLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, l := range s.logs {
		if l.ID == id {
			return l, nil
		}
	}
	return DeployLog{}, fmt.Errorf("%w: 日志 ID=%d 不存在", ErrNotFound, id)
}

func (s *Store) SaveCluster(c Cluster) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.clusters {
		if ex.EnvCode == c.EnvCode {
			return c, newConflict("环境代码 %s 已存在", c.EnvCode)
		}
	}
	if s.persistence != nil {
		saved, err := s.persistence.SaveCluster(c)
		if err != nil {
			return c, err
		}
		c = saved
	}
	if c.ID == 0 {
		c.ID = s.nextCluster
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = c.CreatedAt
	}
	s.clusters = append(s.clusters, c)
	if c.ID >= s.nextCluster {
		s.nextCluster = c.ID + 1
	}
	return c, nil
}

func (s *Store) UpdateCluster(c Cluster) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.clusters {
		if s.clusters[i].ID == c.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return c, ErrNotFound
	}
	for i := range s.clusters {
		if i != idx && s.clusters[i].EnvCode == c.EnvCode {
			return c, newConflict("环境代码 %s 已存在", c.EnvCode)
		}
	}
	// Keep the original creation timestamp and stamp the update before
	// persistence so in-memory/API values never regress to year-1.
	c.CreatedAt = s.clusters[idx].CreatedAt
	c.UpdatedAt = time.Now()
	if s.persistence != nil {
		if err := s.persistence.UpdateCluster(c); err != nil {
			return c, err
		}
	}
	s.clusters[idx] = c
	return c, nil
}

// DeleteCluster refuses to remove a cluster still referenced by apps or
// image releases — disable it (enabled=false) or remove the references
// first.
func (s *Store) DeleteCluster(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.clusters {
		if s.clusters[i].ID != id {
			continue
		}
		for _, a := range s.apps {
			if a.ClusterID == id {
				return newConflict("集群仍被应用 %s 引用，请先移除应用或停用集群", a.AppName)
			}
		}
		for _, r := range s.releases {
			if r.TestClusterID == id || (r.ProdClusterID != nil && *r.ProdClusterID == id) {
				return newConflict("集群仍被发布单 #%d 引用，请停用集群代替删除", r.ID)
			}
		}
		if s.persistence != nil {
			if err := s.persistence.DeleteCluster(id); err != nil {
				return err
			}
		}
		s.clusters = append(s.clusters[:i], s.clusters[i+1:]...)
		return nil
	}
	return ErrNotFound
}

func (s *Store) SaveApp(a App) (App, error) {
	a = NormalizeAppSources(a)
	if err := ValidateAppSources(a); err != nil {
		return a, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.apps {
		if ex.AppName == a.AppName && ex.ClusterID == a.ClusterID {
			return a, newConflict("应用 %s 在该集群已存在", a.AppName)
		}
	}
	if s.persistence != nil {
		saved, err := s.persistence.SaveApp(a)
		if err != nil {
			return a, err
		}
		a = saved
	}
	if a.ID == 0 {
		a.ID = s.nextApp
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	if a.Namespace == "" {
		a.Namespace = "default"
	}
	if a.HarborProject == "" {
		a.HarborProject = "library"
	}
	s.apps = append(s.apps, a)
	if a.ID >= s.nextApp {
		s.nextApp = a.ID + 1
	}
	return a, nil
}

func (s *Store) UpdateApp(a App) (App, error) {
	a = NormalizeAppSources(a)
	if err := ValidateAppSources(a); err != nil {
		return a, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.apps {
		if s.apps[i].ID == a.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return a, ErrNotFound
	}
	for i := range s.apps {
		if i != idx && s.apps[i].AppName == a.AppName && s.apps[i].ClusterID == a.ClusterID {
			return a, newConflict("应用 %s 在该集群已存在", a.AppName)
		}
	}
	for _, binding := range s.delivery.Bindings {
		if binding.AppID != a.ID {
			continue
		}
		for _, linkedBinding := range s.delivery.Bindings {
			if linkedBinding.ComponentID != binding.ComponentID || linkedBinding.AppID == a.ID {
				continue
			}
			for _, linkedApp := range s.apps {
				if linkedApp.ID != linkedBinding.AppID {
					continue
				}
				linkedApp = NormalizeAppSources(linkedApp)
				if a.SourceKind != linkedApp.SourceKind || a.SourceRepoURL != linkedApp.SourceRepoURL || a.SourcePath != linkedApp.SourcePath || a.ImageRepository != linkedApp.ImageRepository || a.DeploymentConfigRepoURL != linkedApp.DeploymentConfigRepoURL || a.DeploymentConfigPath != linkedApp.DeploymentConfigPath {
					return a, NewValidationError("此组件的其他环境绑定使用不同的代码来源、镜像仓库或部署配置来源；请先调整关联关系")
				}
			}
		}
	}
	if s.persistence != nil {
		if err := s.persistence.UpdateApp(a); err != nil {
			return a, err
		}
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = s.apps[idx].CreatedAt
	}
	s.apps[idx] = a
	return a, nil
}

func (s *Store) DeleteApp(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.apps {
		if s.apps[i].ID == id {
			if s.persistence != nil {
				if err := s.persistence.DeleteApp(id); err != nil {
					return err
				}
			}
			s.apps = append(s.apps[:i], s.apps[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

// activeDeployLocked returns the newest pending/running deploy log for the
// (cluster, app) pair — deploys to the same app serialize so concurrent
// submissions can't interleave image updates on one Deployment.
// Callers must hold s.mu.
func (s *Store) activeDeployLocked(clusterID int, namespace, workload string) (DeployLog, bool) {
	for i := len(s.logs) - 1; i >= 0; i-- {
		l := s.logs[i]
		if l.ClusterID == clusterID && l.Namespace == namespace && l.DeploymentName == workload &&
			(l.Status == "pending" || l.Status == "running") {
			return l, true
		}
	}
	return DeployLog{}, false
}

func isTerminalDeployStatus(status string) bool {
	return status == "success" || status == "failed" || status == "interrupted"
}

func (s *Store) addResourceLocksLocked(logID int64, locks []ResourceLock) {
	if s.resourceLocks == nil {
		s.resourceLocks = map[string]int64{}
	}
	for _, lock := range locks {
		identity := lock.Identity()
		s.resourceLocks[identity] = logID
		delete(s.resourceLockLeases, identity)
	}
}

func (s *Store) releaseResourceLocksLocked(logID int64) {
	for identity, owner := range s.resourceLocks {
		if owner == logID {
			delete(s.resourceLocks, identity)
			delete(s.resourceLockLeases, identity)
		}
	}
}

// reserveResourceLockLease holds a bounded cross-process reservation while an
// operator cleans up candidate resources after a stable restore. The lease is
// deliberately short lived so a process crash cannot strand production locks.
func (s *Store) reserveResourceLockLease(logID int64, requested []ResourceLock, expiresAt time.Time) (func() error, error) {
	if logID <= 0 || !expiresAt.After(time.Now()) {
		return nil, NewValidationError("资源锁租约参数无效")
	}
	locks, err := NormalizeResourceLocks(requested)
	if err != nil {
		return nil, err
	}
	if len(locks) == 0 {
		return nil, NewValidationError("资源锁租约不能为空")
	}
	s.mu.Lock()
	if lock, owner, ok := s.conflictingResourceLockLocked(locks); ok {
		s.mu.Unlock()
		return nil, newConflict("资源 %s/%s/%s 正由发布任务 %d 占用", lock.Namespace, lock.Kind, lock.Name, owner)
	}
	if s.persistence != nil {
		persistence, ok := s.persistence.(ResourceLockLeasePersistence)
		if !ok {
			s.mu.Unlock()
			return nil, NewValidationError("持久化层不支持限时资源锁")
		}
		if err := persistence.ReserveResourceLockLease(logID, locks, expiresAt); err != nil {
			s.mu.Unlock()
			return nil, err
		}
	}
	s.addResourceLocksLocked(logID, locks)
	for _, lock := range locks {
		s.resourceLockLeases[lock.Identity()] = expiresAt
	}
	s.mu.Unlock()

	var once sync.Once
	var releaseErr error
	return func() error {
		once.Do(func() {
			if s.persistence != nil {
				if persistence, ok := s.persistence.(ResourceLockLeasePersistence); ok {
					releaseErr = persistence.ReleaseResourceLockLease(logID, locks)
				}
			}
			s.mu.Lock()
			s.releaseResourceLocksLocked(logID)
			s.mu.Unlock()
		})
		return releaseErr
	}, nil
}

func (s *Store) conflictingResourceLockLocked(locks []ResourceLock) (ResourceLock, int64, bool) {
	for _, lock := range locks {
		if owner, exists := s.resourceLocks[lock.Identity()]; exists {
			if expiresAt := s.resourceLockLeases[lock.Identity()]; !expiresAt.IsZero() && !expiresAt.After(time.Now()) {
				continue
			}
			return lock, owner, true
		}
	}
	return ResourceLock{}, 0, false
}

// AddDeployLog is the atomic submit path for deploy/rollback requests: a
// pending/running log for the same cluster+app is a conflict. Raw AddLog
// stays available for seeding and non-deploy rows.
func (s *Store) AddDeployLog(l DeployLog) (DeployLog, error) {
	return s.AddDeployLogWithLocks(l, []ResourceLock{workloadResourceLock(l)})
}

// AddDeployLogWithLocks creates a deploy log while reserving all resources it
// will mutate. Locks are acquired atomically by the durable repository.
func (s *Store) AddDeployLogWithLocks(l DeployLog, requestedLocks []ResourceLock) (DeployLog, error) {
	requestedLocks = append(requestedLocks, workloadResourceLock(l))
	locks, err := NormalizeResourceLocks(requestedLocks)
	if err != nil {
		return l, err
	}
	if len(locks) == 0 {
		return l, NewValidationError("发布任务至少需要锁定一个 Kubernetes 资源")
	}
	for _, lock := range locks {
		if lock.ClusterID != l.ClusterID {
			return l, NewValidationError("资源锁必须属于发布任务的目标集群")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotLogEnvironmentTierLocked(&l)
	if a, ok := s.activeDeployLocked(l.ClusterID, l.Namespace, l.DeploymentName); ok {
		return l, newConflict("已有进行中的发布任务 (log_id=%d, status=%s)", a.ID, a.Status)
	}
	if lock, owner, ok := s.conflictingResourceLockLocked(locks); ok {
		return l, newConflict("资源 %s/%s/%s 正由发布任务 %d 占用", lock.Namespace, lock.Kind, lock.Name, owner)
	}
	if s.persistence != nil {
		if durable, ok := s.persistence.(DeployTaskPersistence); ok {
			l, err = durable.CreateDeployLog(l, locks)
		} else {
			l, err = s.persistence.SaveLog(l)
		}
		if err != nil {
			return l, err
		}
	}
	if l.ID == 0 {
		l.ID = s.nextLog
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now()
	}
	s.logs = append(s.logs, l)
	if l.ID >= s.nextLog {
		s.nextLog = l.ID + 1
	}
	s.addResourceLocksLocked(l.ID, locks)
	return l, nil
}

func (s *Store) AddLog(l DeployLog) (DeployLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addLogLocked(l)
}

// addLogLocked inserts the log; callers hold s.mu.
func (s *Store) addLogLocked(l DeployLog) (DeployLog, error) {
	s.snapshotLogEnvironmentTierLocked(&l)
	if s.persistence != nil {
		saved, err := s.persistence.SaveLog(l)
		if err != nil {
			return l, err
		}
		l = saved
	}
	if l.ID == 0 {
		l.ID = s.nextLog
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now()
	}
	s.logs = append(s.logs, l)
	if l.ID >= s.nextLog {
		s.nextLog = l.ID + 1
	}
	s.captureTerminalLogLocked(l)
	return l, nil
}

// snapshotLogEnvironmentTierLocked records the target tier at task creation.
// Legacy rows loaded from persistence are intentionally left unknown: the
// current cluster tier cannot safely reconstruct their historical value.
func (s *Store) snapshotLogEnvironmentTierLocked(l *DeployLog) {
	if l == nil {
		return
	}
	for _, cluster := range s.clusters {
		if cluster.ID != l.ClusterID {
			continue
		}
		switch cluster.EnvironmentTier {
		case "production", "non_production":
			tier := cluster.EnvironmentTier
			l.EnvironmentTierSnapshot = &tier
		default:
			l.EnvironmentTierSnapshot = nil
		}
		return
	}
	l.EnvironmentTierSnapshot = nil
}

// UpdateLogExecution persists the execution fields then applies them to the
// in-memory copy. A persistence failure leaves memory untouched.
func (s *Store) UpdateLogExecution(l DeployLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.logs {
		if s.logs[i].ID == l.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	claimID := s.workerClaims[l.ID]
	if claimID != "" {
		if !s.workerClaimIsCurrentLocked(l.ID, claimID) {
			return newConflict("Worker 任务认领已撤销，拒绝写入执行状态")
		}
		l.WorkerClaimID = claimID
	}
	if s.persistence != nil {
		var err error
		if claimID != "" {
			persistence, ok := s.persistence.(FencedLogExecutionPersistence)
			if !ok {
				return fmt.Errorf("任务存储不支持 Worker 认领 fencing")
			}
			err = persistence.UpdateLogExecutionWithClaim(l, claimID)
		} else {
			err = s.persistence.UpdateLogExecution(l)
		}
		if err != nil {
			return err
		}
	}
	cur := s.logs[idx]
	cur.Status = l.Status
	cur.CurrentStep = l.CurrentStep
	cur.ImageFull = l.ImageFull
	cur.StepDetail = l.StepDetail
	cur.ErrorMessage = l.ErrorMessage
	cur.StartedAt = l.StartedAt
	cur.FinishedAt = l.FinishedAt
	if isTerminalDeployStatus(l.Status) {
		cur.WorkerClaimID = ""
	} else if claimID != "" {
		cur.WorkerClaimID = claimID
	}
	s.logs[idx] = cur
	if isTerminalDeployStatus(l.Status) {
		s.releaseResourceLocksLocked(l.ID)
		s.captureTerminalLogLocked(cur)
	}
	return nil
}

func (s *Store) FindClusterByEnvCode(envCode string) (Cluster, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.clusters {
		if c.EnvCode == envCode {
			return c, nil
		}
	}
	return Cluster{}, fmt.Errorf("%w: env_code=%s 的集群不存在", ErrNotFound, envCode)
}

func (s *Store) FindAppByName(clusterID int, appName string) (App, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, a := range s.apps {
		if a.ClusterID == clusterID && a.AppName == appName {
			return a, nil
		}
	}
	return App{}, fmt.Errorf("%w: 集群 %d 中应用 %s 不存在", ErrNotFound, clusterID, appName)
}

func (s *Store) FindImageRelease(id int64) (ImageRelease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.releases {
		if r.ID == id {
			return r, nil
		}
	}
	return ImageRelease{}, fmt.Errorf("%w: 发布单 ID=%d 不存在", ErrNotFound, id)
}

// ImageReleases returns matching releases ordered by id desc, capped by
// limit when > 0.
func (s *Store) ImageReleases(appName string, limit int) []ImageRelease {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ImageRelease, 0, len(s.releases))
	for i := len(s.releases) - 1; i >= 0; i-- {
		r := s.releases[i]
		if appName != "" && r.AppName != appName {
			continue
		}
		result = append(result, r)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}

func (s *Store) findReleaseByKeyLocked(key string) (ImageRelease, bool) {
	for _, r := range s.releases {
		if r.EventKey == key {
			return r, true
		}
	}
	return ImageRelease{}, false
}

func (s *Store) findLogLocked(id int64) (DeployLog, bool) {
	for _, l := range s.logs {
		if l.ID == id {
			return l, true
		}
	}
	return DeployLog{}, false
}

// CreateImageRelease atomically stores the test deploy log and the release
// under one write lock. When the event_key already exists the stored release
// is returned with created=false and nothing is persisted or queued.
func (s *Store) CreateImageRelease(r ImageRelease, l DeployLog) (ImageRelease, DeployLog, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotLogEnvironmentTierLocked(&l)
	if strings.HasPrefix(r.Source, "runner:") {
		if err := s.validateRunnerReleaseLocked(r); err != nil {
			return r, l, false, err
		}
	}
	if existing, ok := s.findReleaseByKeyLocked(r.EventKey); ok {
		if strings.HasPrefix(r.Source, "runner:") && (existing.ImageFull != r.ImageFull || existing.ImageDigest == nil || r.ImageDigest == nil || *existing.ImageDigest != *r.ImageDigest) {
			return r, l, false, newConflict("同一次构建已上报不同镜像，拒绝覆盖")
		}
		testLog, _ := s.findLogLocked(existing.TestDeployLogID)
		return existing, testLog, false, nil
	}
	if a, ok := s.activeDeployLocked(l.ClusterID, l.Namespace, l.DeploymentName); ok {
		return r, l, false, newConflict("该应用已有进行中的发布任务 (log_id=%d, status=%s)", a.ID, a.Status)
	}
	locks, lockErr := NormalizeResourceLocks([]ResourceLock{workloadResourceLock(l)})
	if lockErr != nil {
		return r, l, false, lockErr
	}
	if lock, owner, ok := s.conflictingResourceLockLocked(locks); ok {
		return r, l, false, newConflict("资源 %s/%s/%s 正由发布任务 %d 占用", lock.Namespace, lock.Kind, lock.Name, owner)
	}
	if s.persistence != nil {
		saved, savedLog, created, err := s.persistence.CreateImageRelease(r, l)
		if err != nil {
			return r, l, false, err
		}
		if !created {
			// another writer won the event_key race in the DB; adopt its row
			if _, ok := s.findReleaseByKeyLocked(saved.EventKey); !ok {
				s.releases = append(s.releases, saved)
				if saved.ID >= s.nextRelease {
					s.nextRelease = saved.ID + 1
				}
			}
			testLog, _ := s.findLogLocked(saved.TestDeployLogID)
			return saved, testLog, false, nil
		}
		r, l = saved, savedLog
	}
	if l.ID == 0 {
		l.ID = s.nextLog
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now()
	}
	r.TestDeployLogID = l.ID
	if r.ID == 0 {
		r.ID = s.nextRelease
	}
	l.ReleaseID = r.ID
	now := time.Now()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = r.CreatedAt
	}
	s.logs = append(s.logs, l)
	s.addResourceLocksLocked(l.ID, locks)
	if l.ID >= s.nextLog {
		s.nextLog = l.ID + 1
	}
	s.releases = append(s.releases, r)
	if r.ID >= s.nextRelease {
		s.nextRelease = r.ID + 1
	}
	return r, l, true, nil
}

// PromoteImageRelease links a new prod deploy log to the release under one
// write lock. An existing prod deploy in pending/running/success state is a
// conflict; a failed or absent one may be retried.
func (s *Store) PromoteImageRelease(id int64, prodClusterID int, l DeployLog) (ImageRelease, DeployLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotLogEnvironmentTierLocked(&l)
	idx := -1
	for i := range s.releases {
		if s.releases[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ImageRelease{}, l, fmt.Errorf("%w: 发布单 ID=%d 不存在", ErrNotFound, id)
	}
	r := s.releases[idx]
	if pl, ok := s.latestReleaseDeployLocked(id, prodClusterID); ok &&
		(pl.Status == "pending" || pl.Status == "running" || pl.Status == "success") {
		return r, l, newConflict("该目标已有发布任务 (log_id=%d, status=%s)", pl.ID, pl.Status)
	}
	l.ReleaseID = id
	if a, ok := s.activeDeployLocked(prodClusterID, l.Namespace, l.DeploymentName); ok {
		return r, l, newConflict("该应用在该集群已有进行中的任务 (log_id=%d, status=%s)", a.ID, a.Status)
	}
	locks, lockErr := NormalizeResourceLocks([]ResourceLock{workloadResourceLock(l)})
	if lockErr != nil {
		return r, l, lockErr
	}
	if lock, owner, ok := s.conflictingResourceLockLocked(locks); ok {
		return r, l, newConflict("资源 %s/%s/%s 正由发布任务 %d 占用", lock.Namespace, lock.Kind, lock.Name, owner)
	}
	if s.persistence != nil {
		saved, savedLog, err := s.persistence.PromoteImageRelease(id, prodClusterID, l)
		if err != nil {
			return r, l, err
		}
		// persistence only reports the prod link; keep the release fields we
		// already hold rather than adopting a partial row.
		r.ProdClusterID = saved.ProdClusterID
		r.ProdDeployLogID = saved.ProdDeployLogID
		l = savedLog
	} else {
		if l.ID == 0 {
			l.ID = s.nextLog
		}
		r.ProdClusterID = &prodClusterID
		r.ProdDeployLogID = &l.ID
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now()
	}
	r.UpdatedAt = time.Now()
	s.releases[idx] = r
	s.logs = append(s.logs, l)
	if l.ID >= s.nextLog {
		s.nextLog = l.ID + 1
	}
	s.addResourceLocksLocked(l.ID, locks)
	return r, l, nil
}

// ---- Promotion requests (晋级审批) ----

// LoadPromotions seeds persisted requests at startup.
func (s *Store) LoadPromotions(list []PromotionRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.promotions = list
	if s.nextPromo == 0 {
		s.nextPromo = 1
	}
	for _, p := range list {
		if p.ID >= s.nextPromo {
			s.nextPromo = p.ID + 1
		}
	}
}

// PromotionRequests returns requests newest first; status="" matches all.
func (s *Store) PromotionRequests(status string, limit int) []PromotionRequest {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]PromotionRequest, 0, len(s.promotions))
	for i := len(s.promotions) - 1; i >= 0; i-- {
		p := s.promotions[i]
		if status != "" && p.Status != status {
			continue
		}
		result = append(result, p)
		if limit > 0 && len(result) >= limit {
			break
		}
	}
	return result
}

func (s *Store) findPromotionLocked(id int64) (int, bool) {
	for i := range s.promotions {
		if s.promotions[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

func (s *Store) FindPromotionRequest(id int64) (PromotionRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if idx, ok := s.findPromotionLocked(id); ok {
		return s.promotions[idx], nil
	}
	return PromotionRequest{}, fmt.Errorf("%w: 审批请求 ID=%d 不存在", ErrNotFound, id)
}

// CreatePromotionRequest records a pending request; a second open request
// for the same release+cluster conflicts.
func (s *Store) CreatePromotionRequest(r PromotionRequest) (PromotionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.promotions {
		if p.ReleaseID == r.ReleaseID && p.ProdClusterID == r.ProdClusterID && p.Status == "pending" {
			return r, newConflict("该发布单对集群 %d 已有待审批请求 (request_id=%d)", r.ProdClusterID, p.ID)
		}
	}
	if s.persistence != nil {
		saved, err := s.persistence.SavePromotionRequest(r)
		if err != nil {
			return r, err
		}
		r = saved
	}
	if r.ID == 0 {
		if s.nextPromo == 0 {
			s.nextPromo = 1
		}
		r.ID = s.nextPromo
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	r.Status = "pending"
	s.promotions = append(s.promotions, r)
	if r.ID >= s.nextPromo {
		s.nextPromo = r.ID + 1
	}
	return r, nil
}

// UpdatePromotionRequest applies a decision (status/approver/decided_at/
// comment/deploy_log_id) to a stored request; identity fields are unchanged.
func (s *Store) UpdatePromotionRequest(r PromotionRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.findPromotionLocked(r.ID)
	if !ok {
		return ErrNotFound
	}
	if s.promotions[idx].Status != "pending" {
		return NewValidationError("审批请求已是 %s 状态", s.promotions[idx].Status)
	}
	if r.Comment != nil && utf8.RuneCountInString(*r.Comment) > 255 {
		return NewValidationError("comment 超过 255 字符")
	}
	if s.persistence != nil {
		if err := s.persistence.UpdatePromotionRequest(r); err != nil {
			return err
		}
	}
	s.promotions[idx] = r
	s.captureApprovedPromotionLocked(r)
	return nil
}

func (s *Store) ReleaseByEventKey(key string) (ImageRelease, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findReleaseByKeyLocked(key)
}

func (s *Store) ReleaseDeployments(id int64) []DeployLog {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []DeployLog{}
	for _, l := range s.logs {
		if l.ReleaseID == id {
			out = append(out, l)
		}
	}
	// Pre-migration in-memory fixtures and legacy persisted rows retain links.
	for _, r := range s.releases {
		if r.ID == id {
			for _, l := range s.logs {
				if l.ReleaseID == 0 && (l.ID == r.TestDeployLogID || (r.ProdDeployLogID != nil && l.ID == *r.ProdDeployLogID)) {
					out = append(out, l)
				}
			}
		}
	}
	return out
}

func (s *Store) latestReleaseDeployLocked(id int64, clusterID int) (DeployLog, bool) {
	for i := len(s.logs) - 1; i >= 0; i-- {
		l := s.logs[i]
		if l.ReleaseID == id && l.ClusterID == clusterID {
			return l, true
		}
	}
	for _, r := range s.releases {
		if r.ID == id && r.ProdClusterID != nil && *r.ProdClusterID == clusterID && r.ProdDeployLogID != nil {
			return s.findLogLocked(*r.ProdDeployLogID)
		}
	}
	return DeployLog{}, false
}
func (s *Store) LatestReleaseDeploy(id int64, clusterID int) (DeployLog, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.latestReleaseDeployLocked(id, clusterID)
}
func (s *Store) workloadBaselineLocked(clusterID int, namespace, name string) int64 {
	for i := len(s.logs) - 1; i >= 0; i-- {
		l := s.logs[i]
		if l.ClusterID == clusterID && l.Namespace == namespace && l.DeploymentName == name && l.Status != "pending" && l.Status != "running" {
			return l.ID
		}
	}
	return 0
}
func (s *Store) WorkloadBaseline(clusterID int, namespace, name string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.workloadBaselineLocked(clusterID, namespace, name)
}

// ApproveAndCreateDeploy serializes the decision with competing approvals,
// rejection, workload submission and baseline changes. Durable writes form one
// transaction before anything is visible to the worker.
func (s *Store) ApproveAndCreateDeploy(req PromotionRequest, approver string, l DeployLog, requestedLocks ...ResourceLock) (PromotionRequest, DeployLog, error) {
	savedReq, savedLog, _, err := s.approveAndCreateDeploy(req, approver, l, nil, requestedLocks...)
	return savedReq, savedLog, err
}

func (s *Store) ApproveAndCreateDeliveryRun(req PromotionRequest, approver string, l DeployLog, run DeliveryRun, requestedLocks ...ResourceLock) (PromotionRequest, DeployLog, DeliveryRun, error) {
	savedReq, savedLog, savedRun, err := s.approveAndCreateDeploy(req, approver, l, &run, requestedLocks...)
	return savedReq, savedLog, savedRun, err
}

func (s *Store) approveAndCreateDeploy(req PromotionRequest, approver string, l DeployLog, run *DeliveryRun, requestedLocks ...ResourceLock) (PromotionRequest, DeployLog, DeliveryRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pi, ok := s.findPromotionLocked(req.ID)
	if !ok {
		return req, l, DeliveryRun{}, ErrNotFound
	}
	current := s.promotions[pi]
	if current.Status != "pending" {
		return current, l, DeliveryRun{}, NewValidationError("审批请求已是 %s 状态", current.Status)
	}
	if current.ExpiresAt == nil || !time.Now().Before(*current.ExpiresAt) {
		return current, l, DeliveryRun{}, NewValidationError("审批请求已过期")
	}
	if len(current.TemplateHash) != 64 {
		return current, l, DeliveryRun{}, NewValidationError("缺少工作负载配置摘要")
	}
	if current.Requester == approver || approver == "" || approver == "-" || strings.HasPrefix(approver, "apikey:") {
		return current, l, DeliveryRun{}, newForbidden("发起人与审批人不能是同一人")
	}
	ri := -1
	for i, r := range s.releases {
		if r.ID == current.ReleaseID {
			ri = i
			break
		}
	}
	if ri < 0 {
		return current, l, DeliveryRun{}, ErrNotFound
	}
	r := s.releases[ri]
	var app App
	for _, a := range s.apps {
		if a.ClusterID == l.ClusterID && a.AppName == r.AppName {
			app = a
			break
		}
	}
	foundCluster := false
	var cluster Cluster
	for _, c := range s.clusters {
		if c.ID == l.ClusterID {
			foundCluster = true
			cluster = c
		}
		if c.ID == l.ClusterID && (!c.Enabled || c.EnvironmentTier != "production" || c.DeployPolicy != "manual") {
			return current, l, DeliveryRun{}, newForbidden("目标环境策略已改变")
		}
	}
	evidencePolicy, policyErr := s.currentDeliveryEnvironmentEvidencePolicyForAppLocked(app.ID)
	if policyErr != nil {
		return current, l, DeliveryRun{}, policyErr
	}
	if current.Snapshot != promotionSnapshot(r, app, cluster, evidencePolicy) || current.BaselineLogID != s.workloadBaselineLocked(l.ClusterID, l.Namespace, l.DeploymentName) {
		return current, l, DeliveryRun{}, newConflict("候选快照或生产基线已改变，请重新申请")
	}
	// Recheck mutable prerequisites under the same lock as task creation.
	test, exists := s.findLogLocked(r.TestDeployLogID)
	if !exists || test.Status != "success" || test.ImageFull == nil || *test.ImageFull != deployImage(r) {
		return current, l, DeliveryRun{}, NewValidationError("测试部署与当前制品不一致")
	}
	if err := r.VerifiedForEnvironment(evidencePolicy, time.Now()); err != nil {
		return current, l, DeliveryRun{}, err
	}
	if !foundCluster || app.ID == 0 || current.ProdClusterID != l.ClusterID {
		return current, l, DeliveryRun{}, newForbidden("审批目标或应用映射不存在")
	}
	if a, ok := s.activeDeployLocked(l.ClusterID, l.Namespace, l.DeploymentName); ok {
		return current, l, DeliveryRun{}, newConflict("已有活动发布任务 (log_id=%d)", a.ID)
	}
	locks, lockErr := NormalizeResourceLocks(append(requestedLocks, workloadResourceLock(l)))
	if lockErr != nil {
		return current, l, DeliveryRun{}, lockErr
	}
	for _, lock := range locks {
		if lock.ClusterID != l.ClusterID {
			return current, l, DeliveryRun{}, NewValidationError("审批资源锁必须属于发布任务的目标集群")
		}
	}
	if lock, owner, ok := s.conflictingResourceLockLocked(locks); ok {
		return current, l, DeliveryRun{}, newConflict("资源 %s/%s/%s 正由发布任务 %d 占用", lock.Namespace, lock.Kind, lock.Name, owner)
	}
	if old, ok := s.latestReleaseDeployLocked(r.ID, l.ClusterID); ok && (old.Status == "pending" || old.Status == "running" || old.Status == "success") {
		return current, l, DeliveryRun{}, newConflict("该目标已有发布任务 (log_id=%d)", old.ID)
	}
	now := time.Now()
	current.Status = "approved"
	current.Approver = &approver
	current.DecidedAt = &now
	l.ReleaseID = r.ID
	s.snapshotLogEnvironmentTierLocked(&l)
	if run != nil {
		if run.DeliveryPlanID <= 0 || run.PromotionRequestID != current.ID || run.DeployLogID != 0 || run.Strategy == "" {
			return current, l, DeliveryRun{}, NewValidationError("发布运行与审批请求关联无效")
		}
		run.Status = "queued"
		run.CurrentStage = "queued"
		run.Version = 1
		run.CreatedAt = now
		run.UpdatedAt = now
	}
	if s.persistence != nil {
		var savedReq PromotionRequest
		var savedRelease ImageRelease
		var savedLog DeployLog
		var err error
		if run != nil {
			durable, ok := s.persistence.(LockedPromotionRunPersistence)
			if !ok {
				return current, l, DeliveryRun{}, NewValidationError("持久化层不支持审批、运行实例和资源锁的原子提交")
			}
			var savedRun DeliveryRun
			savedReq, savedRelease, savedLog, savedRun, err = durable.ApprovePromotionWithRun(current, l, locks, *run)
			*run = savedRun
		} else if durable, ok := s.persistence.(LockedPromotionPersistence); ok {
			savedReq, savedRelease, savedLog, err = durable.ApprovePromotionWithLocks(current, l, locks)
		} else if len(locks) != 1 || locks[0].Identity() != workloadResourceLock(l).Identity() {
			return current, l, DeliveryRun{}, NewValidationError("持久化层不支持审批时原子锁定多个资源")
		} else {
			savedReq, savedRelease, savedLog, err = s.persistence.ApprovePromotion(current, l)
		}
		if err != nil {
			return s.promotions[pi], l, DeliveryRun{}, err
		}
		current = savedReq
		l = savedLog
		r.ProdClusterID = savedRelease.ProdClusterID
		r.ProdDeployLogID = savedRelease.ProdDeployLogID
	} else {
		l.ID = s.nextLog
		r.ProdClusterID = &l.ClusterID
		r.ProdDeployLogID = &l.ID
		current.DeployLogID = &l.ID
		if run != nil {
			run.ID = s.nextDeliveryRun
			s.nextDeliveryRun++
			run.DeployLogID = l.ID
		}
	}
	l.CreatedAt = now
	r.UpdatedAt = now
	s.logs = append(s.logs, l)
	if l.ID >= s.nextLog {
		s.nextLog = l.ID + 1
	}
	s.addResourceLocksLocked(l.ID, locks)
	if run != nil {
		run.DeployLogID = l.ID
		s.deliveryRuns = append(s.deliveryRuns, cloneDeliveryRun(*run))
		if s.deliveryRunEvents == nil {
			s.deliveryRunEvents = map[int64][]DeliveryRunEvent{}
		}
		s.deliveryRunEvents[run.ID] = append(s.deliveryRunEvents[run.ID], DeliveryRunEvent{
			ID: 1, RunID: run.ID, Version: 1, EventKey: "run-created", EventType: "queued",
			Stage: "queued", Status: "queued", Detail: "生产审批通过，发布运行已排队。", CreatedAt: now,
		})
	}
	s.releases[ri] = r
	s.promotions[pi] = current
	s.captureApprovedPromotionLocked(current)
	if run == nil {
		return current, l, DeliveryRun{}, nil
	}
	return current, l, cloneDeliveryRun(*run), nil
}

func (s *Store) ClaimDeployLog(id int64, requestedClaimID ...string) (bool, error) {
	claimID := ""
	if len(requestedClaimID) > 0 {
		claimID = requestedClaimID[0]
	}
	if claimID == "" {
		var err error
		claimID, err = newWorkerClaimID()
		if err != nil {
			return false, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, l := range s.logs {
		if l.ID == id {
			if l.Status != "pending" {
				return false, nil
			}
			if s.persistence != nil {
				var ok bool
				var err error
				if persistence, yes := s.persistence.(WorkerClaimPersistence); yes {
					ok, err = persistence.ClaimLogWithOwnership(id, claimID)
				} else {
					ok, err = s.persistence.ClaimLog(id)
				}
				if err != nil {
					return false, err
				}
				if !ok {
					// Another store may have won. Synchronize its durable state locally;
					// only a matching claim ID proves that this worker owns the row.
					if reader, yes := s.persistence.(interface {
						LogByID(int64) (DeployLog, error)
					}); yes {
						durable, readErr := reader.LogByID(id)
						if readErr != nil {
							return false, readErr
						}
						s.logs[i] = durable
						delete(s.workerClaims, id)
					}
					return false, nil
				}
			}
			now := time.Now()
			l.Status = "running"
			l.StartedAt = &now
			l.WorkerClaimID = claimID
			s.logs[i] = l
			if s.workerClaims == nil {
				s.workerClaims = map[int64]string{}
			}
			s.workerClaims[id] = claimID
			return true, nil
		}
	}
	return false, ErrNotFound
}

// ReleaseLogOwnership clears a claim only if it still belongs to this Store's
// worker attempt. It does not change task state; recovery handles stale running
// rows after process restart.
func (s *Store) ReleaseLogOwnership(id int64, claimID string) error {
	if id <= 0 || claimID == "" {
		return NewValidationError("Worker 任务认领标识无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.workerClaims[id]; current != claimID {
		return nil
	}
	if persistence, ok := s.persistence.(WorkerClaimPersistence); ok {
		if err := persistence.ReleaseLogOwnership(id, claimID); err != nil {
			return err
		}
	}
	delete(s.workerClaims, id)
	for i := range s.logs {
		if s.logs[i].ID == id && s.logs[i].WorkerClaimID == claimID {
			s.logs[i].WorkerClaimID = ""
			break
		}
	}
	return nil
}

func (s *Store) workerClaimIDLocked(id int64) string {
	return s.workerClaims[id]
}

// workerClaimIsCurrentLocked keeps in-memory execution paths fenced as well as
// durable repository writes. Operator cancellation can revoke the durable
// claim while the original Worker is still unwinding; that stale Worker must
// not fall back to an unfenced in-memory update.
func (s *Store) workerClaimIsCurrentLocked(id int64, claimID string) bool {
	if id <= 0 || claimID == "" || s.workerClaims[id] != claimID {
		return false
	}
	for _, row := range s.logs {
		if row.ID == id {
			return row.Status == "running" && row.WorkerClaimID == claimID
		}
	}
	return false
}

const maxReleaseEvidenceHistory = 1000

func sameEvidenceSubmission(a, b VerificationEvidence) bool {
	return NormalizeDigest(a.Digest) == NormalizeDigest(b.Digest) &&
		a.Status == b.Status && a.Kind == b.Kind && a.ReportURL == b.ReportURL &&
		a.Summary == b.Summary && a.Tool == b.Tool && a.ToolVersion == b.ToolVersion &&
		a.RulesVersion == b.RulesVersion && a.RecordedBy == b.RecordedBy &&
		a.ValidUntil.Equal(b.ValidUntil)
}

func evidenceAlreadyRecorded(history []VerificationEvidence, latest *VerificationEvidence, candidate VerificationEvidence) bool {
	if latest != nil && sameEvidenceSubmission(*latest, candidate) {
		return true
	}
	for _, item := range history {
		if sameEvidenceSubmission(item, candidate) {
			return true
		}
	}
	return false
}

func (s *Store) UpdateReleaseEvidence(id int64, e VerificationEvidence, scan bool) (ImageRelease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateReleaseEvidenceLocked(id, e, scan)
}

// runnerEvidenceReleaseLocked validates that the current component-scoped
// grant still owns this release. Caller holds Store.mu.
func (s *Store) runnerEvidenceReleaseLocked(binding RunnerBinding, id int64) (ImageRelease, error) {
	for _, release := range s.releases {
		if release.ID != id {
			continue
		}
		if release.Source != fmt.Sprintf("runner:%d", binding.ID) {
			return ImageRelease{}, ErrNotFound
		}
		// The authorization hash is deliberately not stored with release rows.
		// Reuse the atomic target/grant checks with the credential hash resolved
		// for this request, so existing releases remain reportable after restart.
		authorizedRelease := release
		authorizedRelease.RunnerAuthorizationHash = binding.TokenHash
		if err := s.validateRunnerReleaseLocked(authorizedRelease); err != nil {
			return release, err
		}
		return release, nil
	}
	return ImageRelease{}, ErrNotFound
}

// UpdateRunnerReleaseEvidence records CI/Smoke evidence only for an artifact
// owned by the authenticated component-scoped Runner grant. The credential,
// current target binding, and Smoke rollout status are checked under the same
// lock as the metadata update so a rotated grant cannot race the write.
func (s *Store) UpdateRunnerReleaseEvidence(binding RunnerBinding, id int64, e VerificationEvidence) (ImageRelease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.runnerEvidenceReleaseLocked(binding, id)
	if err != nil {
		return release, err
	}
	if e.Kind != "ci" && e.Kind != "smoke" {
		return release, NewValidationError("Runner 只能提交 CI 或 Smoke 证据")
	}
	if e.Kind == "smoke" {
		log, exists := s.findLogLocked(release.TestDeployLogID)
		if !exists || log.Status != "success" {
			return release, NewValidationError("测试环境部署成功后才能上报 Smoke 证据")
		}
	}
	return s.updateReleaseEvidenceLocked(id, e, false)
}

// UpdateRunnerReleaseScan records a report-only CI scanner result for an
// artifact owned by the authenticated component-scoped Runner grant.
func (s *Store) UpdateRunnerReleaseScan(binding RunnerBinding, id int64, e VerificationEvidence) (ImageRelease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.runnerEvidenceReleaseLocked(binding, id)
	if err != nil {
		return release, err
	}
	if release.ImageDigest == nil {
		return release, NewValidationError("制品没有固化 digest，不能上报扫描报告")
	}
	if strings.TrimSpace(e.Tool) == "" || strings.TrimSpace(e.ToolVersion) == "" || strings.TrimSpace(e.RulesVersion) == "" {
		return release, NewValidationError("扫描工具、工具版本和规则版本必填")
	}
	for label, value := range map[string]string{"扫描工具": e.Tool, "扫描工具版本": e.ToolVersion, "扫描规则版本": e.RulesVersion} {
		if utf8.RuneCountInString(value) > 128 {
			return release, NewValidationError("%s 不能超过 128 个字符", label)
		}
	}
	e.Kind = "scan"
	e.Tool = strings.TrimSpace(e.Tool)
	e.ToolVersion = strings.TrimSpace(e.ToolVersion)
	e.RulesVersion = strings.TrimSpace(e.RulesVersion)
	e.Digest = *release.ImageDigest
	e.RecordedBy = fmt.Sprintf("runner:%d", binding.ID)
	return s.updateReleaseEvidenceLocked(id, e, true)
}

// UpdateRunnerReleaseConfigurationSource fixes a Runner-reported Git revision
// to one release. Exact retries are idempotent; a different revision cannot
// rewrite an existing artifact's provenance.
func (s *Store) UpdateRunnerReleaseConfigurationSource(binding RunnerBinding, id int64, source ConfigurationSourceRevision) (ImageRelease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.runnerEvidenceReleaseLocked(binding, id)
	if err != nil {
		return release, false, err
	}
	source, err = normalizeConfigurationSourceRevision(source)
	if err != nil {
		return release, false, err
	}
	if binding.Binding.DeploymentConfigRepoURL != "" {
		expectedRepository, expectedPath, sourceErr := normalizeDeploymentConfigSource(binding.Binding.DeploymentConfigRepoURL, binding.Binding.DeploymentConfigPath)
		if sourceErr != nil || source.Repository != expectedRepository || source.Path != expectedPath {
			return release, false, NewValidationError("上报的配置仓库与目录必须匹配此组件已登记的部署配置来源")
		}
	}
	if existing := release.Metadata.ConfigurationSource; existing != nil {
		if existing.Repository == source.Repository && existing.Path == source.Path && existing.Revision == source.Revision {
			return release, false, nil
		}
		return release, false, newConflict("该制品的配置来源 revision 已固定，不能覆盖")
	}
	source.RecordedBy = fmt.Sprintf("runner:%d", binding.ID)
	source.RecordedAt = time.Now().UTC()
	for i := range s.releases {
		if s.releases[i].ID != id {
			continue
		}
		release.Metadata.ConfigurationSource = &source
		if s.persistence != nil {
			if err := s.persistence.UpdateReleaseMetadata(id, release.Metadata); err != nil {
				return s.releases[i], false, err
			}
		}
		s.releases[i] = release
		return release, true, nil
	}
	return ImageRelease{}, false, ErrNotFound
}

func normalizeConfigurationSourceRevision(source ConfigurationSourceRevision) (ConfigurationSourceRevision, error) {
	repository, path, err := normalizeDeploymentConfigSource(source.Repository, source.Path)
	if err != nil || repository == "" {
		if err != nil {
			return ConfigurationSourceRevision{}, err
		}
		return ConfigurationSourceRevision{}, NewValidationError("配置仓库必须是无凭据、无查询参数的 HTTPS 仓库地址")
	}
	revision := strings.ToLower(strings.TrimSpace(source.Revision))
	if len(revision) != 40 && len(revision) != 64 {
		return ConfigurationSourceRevision{}, NewValidationError("配置 revision 必须是完整的 40 或 64 位 Git commit SHA")
	}
	for _, char := range revision {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return ConfigurationSourceRevision{}, NewValidationError("配置 revision 必须是完整的 40 或 64 位 Git commit SHA")
		}
	}
	return ConfigurationSourceRevision{Repository: repository, Path: path, Revision: revision}, nil
}

// Caller holds Store.mu.
func (s *Store) updateReleaseEvidenceLocked(id int64, e VerificationEvidence, scan bool) (ImageRelease, error) {
	for i, r := range s.releases {
		if r.ID == id {
			if r.ImageDigest == nil || NormalizeDigest(e.Digest) == "" || NormalizeDigest(*r.ImageDigest) == "" || NormalizeDigest(e.Digest) != NormalizeDigest(*r.ImageDigest) {
				return r, NewValidationError("报告 digest 与制品不一致")
			}
			if e.Status != "passed" && e.Status != "failed" {
				return r, NewValidationError("status 必须为 passed 或 failed")
			}
			if e.Kind != "ci" && e.Kind != "smoke" && e.Kind != "manual" && e.Kind != "scan" {
				return r, NewValidationError("不支持的报告类型")
			}
			if scan != (e.Kind == "scan") {
				return r, NewValidationError("报告类型与端点不一致")
			}
			if e.ReportURL != "" {
				u, err := url.Parse(e.ReportURL)
				if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
					return r, NewValidationError("报告链接必须为不含凭据的 HTTP/HTTPS URL")
				}
			}
			if utf8.RuneCountInString(e.ReportURL) > 2048 || utf8.RuneCountInString(e.Summary) > 2000 {
				return r, NewValidationError("报告链接或摘要过长")
			}
			if utf8.RuneCountInString(e.Tool) > 128 || utf8.RuneCountInString(e.ToolVersion) > 128 || utf8.RuneCountInString(e.RulesVersion) > 128 {
				return r, NewValidationError("扫描工具及版本信息不能超过 128 个字符")
			}
			if e.Kind == "manual" && e.Summary == "" {
				return r, NewValidationError("人工验收必须填写结论依据")
			}
			if e.RecordedBy == "" || !time.Now().Before(e.ValidUntil) || e.ValidUntil.After(time.Now().Add(7*24*time.Hour)) {
				return r, NewValidationError("报告身份或有效期非法（最长 7 天）")
			}
			e.Digest = NormalizeDigest(e.Digest)
			e.RecordedAt = time.Now()
			if scan {
				if evidenceAlreadyRecorded(r.Metadata.ScanHistory, r.Metadata.Scan, e) {
					return r, nil
				}
				history := r.Metadata.ScanHistory
				if len(history) == 0 && r.Metadata.Scan != nil {
					history = append(history, *r.Metadata.Scan)
				}
				if len(history) >= maxReleaseEvidenceHistory {
					return r, NewValidationError("扫描报告记录已达上限，需由管理员归档后再提交")
				}
				r.Metadata.ScanHistory = append(history, e)
				r.Metadata.Scan = &e
			} else {
				if evidenceAlreadyRecorded(r.Metadata.VerificationHistory, r.Metadata.Verification, e) {
					return r, nil
				}
				history := r.Metadata.VerificationHistory
				if len(history) == 0 && r.Metadata.Verification != nil {
					history = append(history, *r.Metadata.Verification)
				}
				if len(history) >= maxReleaseEvidenceHistory {
					return r, NewValidationError("验证报告记录已达上限，需由管理员归档后再提交")
				}
				r.Metadata.VerificationHistory = append(history, e)
				r.Metadata.Verification = &e
			}
			if s.persistence != nil {
				if err := s.persistence.UpdateReleaseMetadata(id, r.Metadata); err != nil {
					return s.releases[i], err
				}
			}
			r.UpdatedAt = time.Now()
			s.releases[i] = r
			return r, nil
		}
	}
	return ImageRelease{}, ErrNotFound
}

// CheckLegacyDeploy prevents the compatibility archive path from bypassing CD.
func (s *Store) CheckLegacyDeploy(clusterID int) error {
	c, err := s.FindCluster(clusterID)
	if err != nil {
		return err
	}
	if !c.Enabled {
		return newForbidden("目标集群已禁用")
	}
	if s.IsProductionCluster(clusterID) {
		return newForbidden("生产环境请通过制品晋级与审批发布")
	}
	return nil
}

func (s *Store) RollbackLog(clusterID int, targetID int64) (DeployLog, error) {
	target, err := s.FindLog(targetID)
	if err != nil {
		return DeployLog{}, err
	}
	c, err := s.FindCluster(clusterID)
	if err != nil {
		return DeployLog{}, err
	}
	if !c.Enabled {
		return DeployLog{}, newForbidden("目标集群已禁用")
	}
	if target.ClusterID != clusterID || target.Status != "success" || target.ImageFull == nil || !PinnedImage(*target.ImageFull) {
		return DeployLog{}, NewValidationError("回滚目标必须为同环境成功的固化镜像部署")
	}
	// This is an explicitly scoped image-only recovery; configuration and
	// database migrations are not restored or certified by this operation.
	target.RollbackTargetID = target.ID
	target.ID = 0
	target.ReleaseID = 0
	target.DeployType = "rollback"
	target.Status = "pending"
	target.FileName = "rollback"
	target.CurrentStep = nil
	target.StepDetail = ""
	target.ErrorMessage = nil
	target.StartedAt = nil
	target.FinishedAt = nil
	target.CreatedAt = time.Time{}
	return s.AddDeployLog(target)
}

func PinnedImage(ref string) bool {
	i := strings.LastIndex(ref, "@")
	return i > 0 && strings.Count(ref, "@") == 1 && strings.HasPrefix(ref[i+1:], "sha256:") && ref[i+1:] == NormalizeDigest(ref[i+1:])
}

// CheckManagedWrite closes production Deployment writes even when an app
// mapping is removed. Otherwise deregistration would bypass the release gate.
func (s *Store) CheckManagedWrite(clusterID int, resource, namespace, name string) error {
	if _, err := s.FindCluster(clusterID); err != nil {
		return err
	}
	if namespace != "" && name != "" {
		kind := resourceKind(resource)
		if kind != "" {
			if err := s.CheckResourceLock(ResourceLock{ClusterID: clusterID, Namespace: namespace, Kind: kind, Name: name}); err != nil {
				return err
			}
		}
	}
	if resource == "deployments" && s.IsProductionCluster(clusterID) {
		return newForbidden("生产 Deployment 请通过发布/回滚流程修改")
	}
	return nil
}

// CheckResourceLock checks delivery ownership without applying the production
// write ban. Metadata-only operations such as workload rebind use this guard.
func (s *Store) CheckResourceLock(lock ResourceLock) error {
	if err := lock.Validate(); err != nil {
		return err
	}
	lock.Namespace = strings.TrimSpace(lock.Namespace)
	lock.Kind = strings.ToLower(strings.TrimSpace(lock.Kind))
	lock.Name = strings.TrimSpace(lock.Name)
	identity := lock.Identity()
	s.mu.RLock()
	owner, inMemory := s.resourceLocks[identity]
	if expiresAt := s.resourceLockLeases[identity]; !expiresAt.IsZero() && !expiresAt.After(time.Now()) {
		inMemory = false
	}
	s.mu.RUnlock()
	if inMemory {
		return newConflict("资源 %s/%s/%s 正由发布任务 %d 占用", lock.Namespace, lock.Kind, lock.Name, owner)
	}
	if reader, ok := s.persistence.(ResourceLockReader); ok {
		owner, found, err := reader.ResourceLockOwner(lock)
		if err != nil {
			return fmt.Errorf("查询发布资源锁失败: %w", err)
		}
		if found {
			return newConflict("资源 %s/%s/%s 正由发布任务 %d 占用", lock.Namespace, lock.Kind, lock.Name, owner)
		}
	}
	return nil
}

func resourceKind(resource string) string {
	switch resource {
	case "deployments":
		return "deployment"
	case "services":
		return "service"
	case "ingresses":
		return "ingress"
	case "statefulsets":
		return "statefulset"
	case "daemonsets":
		return "daemonset"
	case "configmaps":
		return "configmap"
	case "secrets":
		return "secret"
	default:
		return ""
	}
}
