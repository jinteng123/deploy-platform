package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

type DeliveryEnvironment struct {
	ID                    int64     `json:"id"`
	Code                  string    `json:"code"`
	Name                  string    `json:"name"`
	Tier                  string    `json:"environment_tier"`
	EvidencePolicyVersion int64     `json:"evidence_policy_version"`
	LegacyClusterID       *int      `json:"legacy_cluster_id,omitempty"`
	RequiresReview        bool      `json:"requires_review"`
	CreatedAt             time.Time `json:"created_at"`
}

type DeliveryApplication struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	LegacyAppID    *int      `json:"legacy_app_id,omitempty"`
	RequiresReview bool      `json:"requires_review"`
	CreatedAt      time.Time `json:"created_at"`
}

type DeliveryComponent struct {
	ID             int64     `json:"id"`
	ApplicationID  int64     `json:"application_id"`
	Name           string    `json:"name"`
	LegacyAppID    *int      `json:"legacy_app_id,omitempty"`
	RequiresReview bool      `json:"requires_review"`
	CreatedAt      time.Time `json:"created_at"`
}

// DeliveryWorkloadReference identifies a Kubernetes workload across delete
// and recreate events. ResourceVersion is an observation for audit and plan
// freshness; UID is the durable identity used to detect same-name replacement.
type DeliveryWorkloadReference struct {
	ClusterID       int    `json:"cluster_id"`
	Namespace       string `json:"namespace"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resource_version,omitempty"`
}

type DeliveryBinding struct {
	DeploymentPipeline  *YunxiaoDeploymentPipelineBinding `json:"deployment_pipeline,omitempty"`
	ID                  int64                             `json:"id"`
	AppID               int                               `json:"app_id"`
	ApplicationID       int64                             `json:"application_id"`
	ComponentID         int64                             `json:"component_id"`
	EnvironmentID       int64                             `json:"environment_id"`
	StrategyDraft       string                            `json:"strategy_draft,omitempty"`
	ServiceName         string                            `json:"service_name,omitempty"`
	TemplateWorkloadRef DeliveryWorkloadReference         `json:"template_workload_ref"`
	ActiveWorkloadRef   DeliveryWorkloadReference         `json:"active_workload_ref"`
	RequiresReview      bool                              `json:"requires_review"`
	CreatedAt           time.Time                         `json:"created_at"`
}

// YunxiaoDeploymentPipelineBinding is target-environment configuration for
// applying a pinned deployment-config revision. It is deliberately separate
// from the Runner's image-build pipeline binding.
type YunxiaoDeploymentPipelineBinding struct {
	ConnectionID int64  `json:"connection_id"`
	PipelineID   string `json:"pipeline_id"`
}

type DeliveryModel struct {
	Environments         []DeliveryEnvironment         `json:"environments"`
	Applications         []DeliveryApplication         `json:"applications"`
	Components           []DeliveryComponent           `json:"components"`
	Bindings             []DeliveryBinding             `json:"bindings"`
	PipelineScopeReviews []DeliveryPipelineScopeReview `json:"pipeline_scope_reviews"`
}

func normalizeDeliveryStrategy(strategy string) string {
	strategy = strings.TrimSpace(strategy)
	if strategy == "" {
		return "rolling"
	}
	return strategy
}

func legacyDeliveryModel(clusters []Cluster, apps []App) DeliveryModel {
	model := DeliveryModel{
		Environments: make([]DeliveryEnvironment, 0, len(clusters)),
		Applications: make([]DeliveryApplication, 0, len(apps)),
		Components:   make([]DeliveryComponent, 0, len(apps)),
		Bindings:     make([]DeliveryBinding, 0, len(apps)),
	}
	for _, cluster := range clusters {
		id := cluster.ID
		model.Environments = append(model.Environments, DeliveryEnvironment{
			ID: int64(id), Code: fmt.Sprintf("legacy-cluster-%d", id),
			Name: fmt.Sprintf("待确认 · %s / %s", cluster.Environment, cluster.Name),
			Tier: cluster.EnvironmentTier, LegacyClusterID: &id, RequiresReview: true,
			CreatedAt: cluster.CreatedAt,
		})
	}
	for _, app := range apps {
		id := int64(app.ID)
		originID := app.ID
		model.Applications = append(model.Applications, DeliveryApplication{ID: id, Name: app.AppName, LegacyAppID: &originID, RequiresReview: true, CreatedAt: app.CreatedAt})
		model.Components = append(model.Components, DeliveryComponent{ID: id, ApplicationID: id, Name: app.AppName, LegacyAppID: &originID, RequiresReview: true, CreatedAt: app.CreatedAt})
		ref := DeliveryWorkloadReference{ClusterID: app.ClusterID, Namespace: app.Namespace, Kind: "Deployment", Name: app.DeploymentName}
		model.Bindings = append(model.Bindings, DeliveryBinding{ID: id, AppID: app.ID, ApplicationID: id, ComponentID: id, EnvironmentID: int64(app.ClusterID), TemplateWorkloadRef: ref, ActiveWorkloadRef: ref, RequiresReview: true, CreatedAt: app.CreatedAt})
	}
	return model
}

// DeliveryRepository is optional so existing persistence test doubles and
// in-memory-only stores remain usable. Production MySQL implements it.
type DeliveryRepository interface {
	DeliveryModel() (DeliveryModel, error)
	SaveDeliveryEnvironment(DeliveryEnvironment) (DeliveryEnvironment, error)
	ConfirmDeliveryEnvironment(DeliveryEnvironment) error
	SaveDeliveryApplication(DeliveryApplication) (DeliveryApplication, error)
	SaveDeliveryComponent(DeliveryComponent) (DeliveryComponent, error)
	AssignDeliveryBinding(DeliveryBinding) (DeliveryBinding, error)
}

// DeliveryBindingPolicyRepository stores the logical relationship and its
// initial or updated policy revision in one database transaction.
type DeliveryBindingPolicyRepository interface {
	AssignDeliveryBindingWithPolicy(DeliveryBinding, DeliveryPolicyVersion, int64) (DeliveryBinding, DeliveryPolicyVersion, bool, error)
}

// DeliveryWorkloadReferenceRepository persists first-observed workload UIDs
// without changing the logical app binding. The expected values make pinning
// conditional so a concurrent rebind cannot silently win.
type DeliveryWorkloadReferenceRepository interface {
	PinDeliveryBindingWorkloadRefs(bindingID int64, expectedTemplate, expectedActive, template, active DeliveryWorkloadReference) (DeliveryBinding, error)
}

type DeliveryWorkloadReferenceRebindRepository interface {
	RebindDeliveryBindingActiveWorkloadRef(bindingID int64, expectedTemplate, expectedActive, template, active DeliveryWorkloadReference) (DeliveryBinding, error)
}

type DeliveryBindingWorkloadPromotion struct {
	AppID                     int
	ExpectedDeployment        string
	ExpectedActiveWorkloadRef DeliveryWorkloadReference
	ActiveWorkloadRef         DeliveryWorkloadReference
}

func sameDeliveryWorkloadIdentity(a, b DeliveryWorkloadReference) bool {
	return a.ClusterID == b.ClusterID && a.Namespace == b.Namespace && a.Kind == b.Kind && a.Name == b.Name && a.UID == b.UID
}

type DeliveryRunWorkloadPromotionPersistence interface {
	TransitionDeliveryRunExecutionWithWorkloadPromotion(DeployLog, int64, DeliveryRunEvent, DeliveryBindingWorkloadPromotion) (DeliveryRun, DeliveryRunEvent, bool, error)
}

func (s *Store) LoadDeliveryModel(model DeliveryModel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	model.Bindings = append([]DeliveryBinding{}, model.Bindings...)
	model.PipelineScopeReviews = append([]DeliveryPipelineScopeReview{}, model.PipelineScopeReviews...)
	for i := range model.Bindings {
		model.Bindings[i].DeploymentPipeline = cloneYunxiaoDeploymentPipelineBinding(model.Bindings[i].DeploymentPipeline)
	}
	s.delivery = model
	s.pipelineScopeReviews = append([]DeliveryPipelineScopeReview{}, model.PipelineScopeReviews...)
	s.nextDeliveryPipelineScopeReview = nextPipelineScopeReviewID(s.pipelineScopeReviews)
	s.nextDeliveryEnvironment = nextEnvironmentID(model.Environments)
	s.nextDeliveryApplication = nextApplicationID(model.Applications)
	s.nextDeliveryComponent = nextComponentID(model.Components)
	s.nextDeliveryBinding = nextBindingID(model.Bindings)
}

func nextEnvironmentID(rows []DeliveryEnvironment) int64 {
	var next int64 = 1
	for _, row := range rows {
		if row.ID >= next {
			next = row.ID + 1
		}
	}
	return next
}
func nextApplicationID(rows []DeliveryApplication) int64 {
	var next int64 = 1
	for _, row := range rows {
		if row.ID >= next {
			next = row.ID + 1
		}
	}
	return next
}
func nextComponentID(rows []DeliveryComponent) int64 {
	var next int64 = 1
	for _, row := range rows {
		if row.ID >= next {
			next = row.ID + 1
		}
	}
	return next
}
func nextBindingID(rows []DeliveryBinding) int64 {
	var next int64 = 1
	for _, row := range rows {
		if row.ID >= next {
			next = row.ID + 1
		}
	}
	return next
}

func nextPipelineScopeReviewID(rows []DeliveryPipelineScopeReview) int64 {
	var next int64 = 1
	for _, row := range rows {
		if row.ID >= next {
			next = row.ID + 1
		}
	}
	return next
}

func (s *Store) DeliveryModel() DeliveryModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	model := s.delivery
	model.Environments = append([]DeliveryEnvironment{}, model.Environments...)
	model.Applications = append([]DeliveryApplication{}, model.Applications...)
	model.Components = append([]DeliveryComponent{}, model.Components...)
	model.Bindings = append([]DeliveryBinding{}, model.Bindings...)
	model.PipelineScopeReviews = latestPipelineScopeReviews(s.pipelineScopeReviews)
	for i := range model.Bindings {
		model.Bindings[i].DeploymentPipeline = cloneYunxiaoDeploymentPipelineBinding(model.Bindings[i].DeploymentPipeline)
	}
	return model
}

func cloneYunxiaoDeploymentPipelineBinding(binding *YunxiaoDeploymentPipelineBinding) *YunxiaoDeploymentPipelineBinding {
	if binding == nil {
		return nil
	}
	copy := *binding
	return &copy
}

// deliveryEnvironmentLocked resolves one logical environment. The caller must
// hold s.mu (read or write) before invoking it.
func (s *Store) deliveryEnvironmentLocked(id int64) (DeliveryEnvironment, bool) {
	for _, environment := range s.delivery.Environments {
		if environment.ID == id {
			return environment, true
		}
	}
	return DeliveryEnvironment{}, false
}

func validateDisplayName(name, field string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 128 {
		return "", NewValidationError("%s 名称须为 1–128 个字符", field)
	}
	return name, nil
}

func newEnvironmentCode() (string, error) {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return "env-" + hex.EncodeToString(bytes[:]), nil
}

func (s *Store) CreateDeliveryEnvironment(name, tier string) (DeliveryEnvironment, error) {
	name, err := validateDisplayName(name, "环境")
	if err != nil {
		return DeliveryEnvironment{}, err
	}
	if tier != "non_production" && tier != "production" {
		return DeliveryEnvironment{}, NewValidationError("环境类型必须是非生产或生产")
	}
	code, err := newEnvironmentCode()
	if err != nil {
		return DeliveryEnvironment{}, err
	}
	row := DeliveryEnvironment{Code: code, Name: name, Tier: tier, CreatedAt: time.Now()}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.delivery.Environments {
		if strings.EqualFold(existing.Name, name) {
			return row, newConflict("环境名称已存在")
		}
	}
	if repo, ok := s.persistence.(DeliveryRepository); ok {
		row, err = repo.SaveDeliveryEnvironment(row)
		if err != nil {
			return row, err
		}
	} else {
		row.ID = s.nextDeliveryEnvironment
		s.nextDeliveryEnvironment++
	}
	s.delivery.Environments = append(s.delivery.Environments, row)
	return row, nil
}

func (s *Store) ConfirmDeliveryEnvironment(id int64, name string) (DeliveryEnvironment, error) {
	name, err := validateDisplayName(name, "环境")
	if err != nil {
		return DeliveryEnvironment{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.delivery.Environments {
		if s.delivery.Environments[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return DeliveryEnvironment{}, ErrNotFound
	}
	for i, existing := range s.delivery.Environments {
		if i != idx && strings.EqualFold(existing.Name, name) {
			return DeliveryEnvironment{}, newConflict("环境名称已存在")
		}
	}
	row := s.delivery.Environments[idx]
	row.Name = name
	row.RequiresReview = false
	if repo, ok := s.persistence.(DeliveryRepository); ok {
		if err := repo.ConfirmDeliveryEnvironment(row); err != nil {
			return row, err
		}
	}
	s.delivery.Environments[idx] = row
	return row, nil
}

func (s *Store) CreateDeliveryApplication(name string) (DeliveryApplication, error) {
	name, err := validateDisplayName(name, "应用")
	if err != nil {
		return DeliveryApplication{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.delivery.Applications {
		if strings.EqualFold(row.Name, name) {
			return row, newConflict("业务应用名称已存在")
		}
	}
	row := DeliveryApplication{Name: name, CreatedAt: time.Now()}
	if repo, ok := s.persistence.(DeliveryRepository); ok {
		row, err = repo.SaveDeliveryApplication(row)
		if err != nil {
			return row, err
		}
	} else {
		row.ID = s.nextDeliveryApplication
		s.nextDeliveryApplication++
	}
	s.delivery.Applications = append(s.delivery.Applications, row)
	return row, nil
}

func (s *Store) CreateDeliveryComponent(applicationID int64, name string) (DeliveryComponent, error) {
	name, err := validateDisplayName(name, "组件")
	if err != nil {
		return DeliveryComponent{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, app := range s.delivery.Applications {
		if app.ID == applicationID {
			found = true
			break
		}
	}
	if !found {
		return DeliveryComponent{}, ErrNotFound
	}
	for _, row := range s.delivery.Components {
		if row.ApplicationID == applicationID && strings.EqualFold(row.Name, name) {
			return row, newConflict("此业务应用下的组件名称已存在")
		}
	}
	row := DeliveryComponent{ApplicationID: applicationID, Name: name, CreatedAt: time.Now()}
	if repo, ok := s.persistence.(DeliveryRepository); ok {
		row, err = repo.SaveDeliveryComponent(row)
		if err != nil {
			return row, err
		}
	} else {
		row.ID = s.nextDeliveryComponent
		s.nextDeliveryComponent++
	}
	s.delivery.Components = append(s.delivery.Components, row)
	return row, nil
}

func (s *Store) AssignDeliveryBinding(appID int, applicationID, componentID, environmentID int64, strategyDraft string, serviceNames ...string) (DeliveryBinding, error) {
	row, _, _, err := s.assignDeliveryBinding(appID, applicationID, componentID, environmentID, strategyDraft, serviceNames, nil, nil)
	return row, err
}

func (s *Store) AssignDeliveryBindingWithDeploymentPipeline(appID int, applicationID, componentID, environmentID int64, strategyDraft, serviceName string, pipeline *YunxiaoDeploymentPipelineBinding) (DeliveryBinding, error) {
	row, _, _, err := s.assignDeliveryBinding(appID, applicationID, componentID, environmentID, strategyDraft, []string{serviceName}, nil, pipeline)
	return row, err
}

func (s *Store) AssignDeliveryBindingWithPolicy(appID int, applicationID, componentID, environmentID int64, strategyDraft, serviceName string, expectedVersion int64, strategy string, retentionHours int, actor string, canaryAnalysis ...*CanaryAnalysisPolicy) (DeliveryBinding, DeliveryPolicyVersion, bool, error) {
	return s.AssignDeliveryBindingWithPolicyAndDeploymentPipeline(appID, applicationID, componentID, environmentID, strategyDraft, serviceName, expectedVersion, strategy, retentionHours, actor, nil, canaryAnalysis...)
}

func (s *Store) AssignDeliveryBindingWithPolicyAndDeploymentPipeline(appID int, applicationID, componentID, environmentID int64, strategyDraft, serviceName string, expectedVersion int64, strategy string, retentionHours int, actor string, pipeline *YunxiaoDeploymentPipelineBinding, canaryAnalysis ...*CanaryAnalysisPolicy) (DeliveryBinding, DeliveryPolicyVersion, bool, error) {
	if expectedVersion < 0 {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("expected_policy_version 不能小于 0")
	}
	policy, err := prepareDeliveryPolicyWrite(strategy, serviceName, retentionHours, actor, canaryAnalysis...)
	if err != nil {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, err
	}
	if strategyDraft == "" && policy.Strategy != "rolling" {
		strategyDraft = policy.Strategy
	}
	if strategyDraft != "" && strategyDraft != policy.Strategy {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("兼容策略草稿必须与发布策略一致")
	}
	write := &DeliveryBindingPolicyWrite{ExpectedVersion: expectedVersion, Policy: policy}
	return s.assignDeliveryBinding(appID, applicationID, componentID, environmentID, strategyDraft, []string{serviceName}, write, pipeline)
}

func (s *Store) assignDeliveryBinding(appID int, applicationID, componentID, environmentID int64, strategyDraft string, serviceNames []string, policyWrite *DeliveryBindingPolicyWrite, pipeline *YunxiaoDeploymentPipelineBinding) (DeliveryBinding, DeliveryPolicyVersion, bool, error) {
	pipelineProvided := pipeline != nil
	if strategyDraft != "" && strategyDraft != "blue_green" && strategyDraft != "gateway_canary" {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("发布策略草稿必须是蓝绿或网关金丝雀")
	}
	serviceName := ""
	if len(serviceNames) > 0 {
		serviceName = strings.TrimSpace(serviceNames[0])
	}
	if strategyDraft == "blue_green" && serviceName == "" {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("蓝绿策略需要明确绑定一个 Service 入口")
	}
	if serviceName != "" {
		if errs := validation.IsDNS1123Subdomain(serviceName); len(errs) > 0 {
			return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("Service 名称无效: %s", strings.Join(errs, "; "))
		}
	}
	if strategyDraft != "blue_green" {
		serviceName = ""
	}
	if policyWrite != nil && policyWrite.Policy.ServiceName != serviceName {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("绑定入口 Service 与策略版本不一致")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	appFound := false
	var targetApp App
	var target Cluster
	for _, app := range s.apps {
		if app.ID == appID {
			appFound = true
			targetApp = app
			for _, cluster := range s.clusters {
				if cluster.ID == app.ClusterID {
					target = cluster
					break
				}
			}
			break
		}
	}
	if !appFound {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, ErrNotFound
	}
	targetApp = NormalizeAppSources(targetApp)
	if target.ID == 0 {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, newConflict("绑定目标集群不存在")
	}
	var appExists, componentExists bool
	for _, row := range s.delivery.Applications {
		if row.ID == applicationID {
			appExists = true
			break
		}
	}
	for _, row := range s.delivery.Components {
		if row.ID == componentID && row.ApplicationID == applicationID {
			componentExists = true
			break
		}
	}
	if !appExists || !componentExists {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("请选择同一业务应用下的有效组件")
	}
	var env DeliveryEnvironment
	for _, row := range s.delivery.Environments {
		if row.ID == environmentID {
			env = row
			break
		}
	}
	if env.ID == 0 {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("请选择有效环境")
	}
	if env.RequiresReview {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("请先核对并确认环境名称")
	}
	if target.EnvironmentTier != env.Tier {
		return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("环境类型与目标集群不一致；为保护生产门禁，请先核对集群环境")
	}
	if pipeline != nil {
		pipeline.PipelineID = strings.TrimSpace(pipeline.PipelineID)
		if pipeline.ConnectionID == 0 && pipeline.PipelineID == "" {
			pipeline = nil
		} else {
			if pipeline.ConnectionID < 1 || !runnerPipelineID.MatchString(pipeline.PipelineID) {
				return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("配置部署流水线需要有效的云效连接和数字流水线 ID")
			}
			if targetApp.DeploymentConfigRepoURL == "" {
				return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("请先在组件绑定部署配置仓库，再配置配置部署流水线")
			}
			connectionExists := false
			for _, connection := range s.yunxiaoConnections {
				if connection.ID == pipeline.ConnectionID {
					connectionExists = true
					break
				}
			}
			if !connectionExists {
				return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("所选云效连接不存在")
			}
		}
	}
	for _, existing := range s.delivery.Bindings {
		if existing.ComponentID != componentID || existing.AppID == appID {
			continue
		}
		for _, linkedApp := range s.apps {
			linkedApp = NormalizeAppSources(linkedApp)
			if linkedApp.ID == existing.AppID && (linkedApp.SourceKind != targetApp.SourceKind || linkedApp.SourceRepoURL != targetApp.SourceRepoURL || linkedApp.SourcePath != targetApp.SourcePath || linkedApp.ImageRepository != targetApp.ImageRepository || linkedApp.DeploymentConfigRepoURL != targetApp.DeploymentConfigRepoURL || linkedApp.DeploymentConfigPath != targetApp.DeploymentConfigPath) {
				return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("此组件已绑定不同的代码来源、镜像仓库或部署配置来源；请分别建立发布组件")
			}
		}
	}
	ref := DeliveryWorkloadReference{ClusterID: targetApp.ClusterID, Namespace: targetApp.Namespace, Kind: "Deployment", Name: targetApp.DeploymentName}
	row := DeliveryBinding{AppID: appID, ApplicationID: applicationID, ComponentID: componentID, EnvironmentID: environmentID, StrategyDraft: strategyDraft, ServiceName: serviceName, DeploymentPipeline: cloneYunxiaoDeploymentPipelineBinding(pipeline), TemplateWorkloadRef: ref, ActiveWorkloadRef: ref, CreatedAt: time.Now()}
	for _, existing := range s.delivery.Bindings {
		if existing.AppID == appID {
			row.TemplateWorkloadRef = existing.TemplateWorkloadRef
			row.ActiveWorkloadRef = existing.ActiveWorkloadRef
			if !pipelineProvided {
				row.DeploymentPipeline = cloneYunxiaoDeploymentPipelineBinding(existing.DeploymentPipeline)
			}
			break
		}
	}
	var err error
	var savedPolicy DeliveryPolicyVersion
	policyCreated := false
	if policyWrite != nil && s.persistence != nil {
		repo, ok := s.persistence.(DeliveryBindingPolicyRepository)
		if !ok {
			return DeliveryBinding{}, DeliveryPolicyVersion{}, false, NewValidationError("当前数据库不支持绑定与策略的原子保存")
		}
		row, savedPolicy, policyCreated, err = repo.AssignDeliveryBindingWithPolicy(row, policyWrite.Policy, policyWrite.ExpectedVersion)
		if err != nil {
			return DeliveryBinding{}, DeliveryPolicyVersion{}, false, err
		}
	} else if repo, ok := s.persistence.(DeliveryRepository); ok {
		row, err = repo.AssignDeliveryBinding(row)
		if err != nil {
			return row, DeliveryPolicyVersion{}, false, err
		}
	} else {
		for i := range s.delivery.Bindings {
			if s.delivery.Bindings[i].AppID == appID {
				row.ID = s.delivery.Bindings[i].ID
				break
			}
		}
		if row.ID == 0 {
			row.ID = s.nextDeliveryBinding
		}
		if policyWrite != nil {
			versions := s.deliveryPolicyVersions[row.ID]
			current := currentPolicyVersion(versions)
			if current != policyWrite.ExpectedVersion {
				return DeliveryBinding{}, DeliveryPolicyVersion{}, false, newConflict("策略已更新到 v%d，请刷新后重试", current)
			}
			if len(versions) > 0 && sameDeliveryPolicyConfig(versions[len(versions)-1], policyWrite.Policy) {
				savedPolicy = versions[len(versions)-1]
			} else {
				savedPolicy = policyWrite.Policy
				savedPolicy.BindingID = row.ID
				savedPolicy.Version = current + 1
				savedPolicy.CreatedAt = time.Now()
				policyCreated = true
			}
		}
		if row.ID == s.nextDeliveryBinding {
			s.nextDeliveryBinding++
		}
	}
	for i := range s.delivery.Bindings {
		if s.delivery.Bindings[i].AppID == appID {
			s.delivery.Bindings[i] = row
			break
		}
	}
	if !containsDeliveryBinding(s.delivery.Bindings, appID) {
		s.delivery.Bindings = append(s.delivery.Bindings, row)
	}
	if policyWrite != nil {
		if s.deliveryPolicyVersions == nil {
			s.deliveryPolicyVersions = make(map[int64][]DeliveryPolicyVersion)
		}
		if policyCreated {
			storedPolicy := savedPolicy
			storedPolicy.CanaryAnalysis = cloneCanaryAnalysisPolicy(savedPolicy.CanaryAnalysis)
			s.deliveryPolicyVersions[row.ID] = append(s.deliveryPolicyVersions[row.ID], storedPolicy)
		} else if savedPolicy.Version > 0 {
			current := s.deliveryPolicyVersions[row.ID]
			if len(current) == 0 || current[len(current)-1].Version != savedPolicy.Version {
				savedPolicy.CanaryAnalysis = cloneCanaryAnalysisPolicy(savedPolicy.CanaryAnalysis)
				s.deliveryPolicyVersions[row.ID] = append(current, savedPolicy)
			}
		}
		savedPolicy.CanaryAnalysis = cloneCanaryAnalysisPolicy(savedPolicy.CanaryAnalysis)
	}
	for i := range s.delivery.Applications {
		if s.delivery.Applications[i].ID == applicationID {
			s.delivery.Applications[i].RequiresReview = false
		}
	}
	for i := range s.delivery.Components {
		if s.delivery.Components[i].ID == componentID {
			s.delivery.Components[i].RequiresReview = false
		}
	}
	return row, savedPolicy, policyCreated, nil
}

// PinDeliveryBindingWorkloadRefs records the first live UID observation and
// refreshes the active resourceVersion on later previews. The original
// template reference is only initialized once and remains stable after a
// blue-green promotion.
func (s *Store) PinDeliveryBindingWorkloadRefs(bindingID int64, observed DeliveryWorkloadReference) (DeliveryBinding, error) {
	if bindingID <= 0 || observed.ClusterID <= 0 || observed.Namespace == "" || observed.Kind != "Deployment" || observed.Name == "" || observed.UID == "" {
		return DeliveryBinding{}, NewValidationError("活动 Deployment 身份不完整，无法固定资源 UID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.delivery.Bindings {
		if s.delivery.Bindings[i].ID == bindingID {
			index = i
			break
		}
	}
	if index < 0 {
		return DeliveryBinding{}, ErrNotFound
	}
	binding := s.delivery.Bindings[index]
	template := binding.TemplateWorkloadRef
	active := binding.ActiveWorkloadRef
	if active.Name == "" {
		active = observed
	}
	if active.ClusterID != observed.ClusterID || active.Namespace != observed.Namespace || active.Kind != observed.Kind || active.Name != observed.Name {
		return binding, newConflict("活动 Deployment 与绑定记录不一致，请重新核对组件资源绑定")
	}
	if active.UID != "" && active.UID != observed.UID {
		return binding, newConflict("活动 Deployment UID 已变化，检测到同名资源被替换；请重新核对绑定后再发布")
	}
	if template.Name == "" {
		template = observed
	} else if template.ClusterID == 0 || template.Namespace == "" || template.Kind == "" {
		template.ClusterID, template.Namespace, template.Kind = observed.ClusterID, observed.Namespace, observed.Kind
	}
	if template.UID == "" && template.ClusterID == observed.ClusterID && template.Namespace == observed.Namespace && template.Kind == observed.Kind && template.Name == observed.Name {
		template = observed
	}
	active = observed
	if repo, ok := s.persistence.(DeliveryWorkloadReferenceRepository); ok {
		persisted, err := repo.PinDeliveryBindingWorkloadRefs(binding.ID, binding.TemplateWorkloadRef, binding.ActiveWorkloadRef, template, active)
		if err != nil {
			return binding, err
		}
		binding = persisted
	} else {
		binding.TemplateWorkloadRef = template
		binding.ActiveWorkloadRef = active
	}
	s.delivery.Bindings[index] = binding
	return binding, nil
}

// RebindDeliveryBindingWorkloadRef is an explicit operator-approved recovery
// from a deleted/recreated or intentionally renamed Deployment. It updates
// only the active reference; the original template remains available for audit.
func (s *Store) RebindDeliveryBindingWorkloadRef(bindingID int64, observed DeliveryWorkloadReference) (DeliveryBinding, error) {
	if bindingID <= 0 || observed.ClusterID <= 0 || observed.Namespace == "" || observed.Kind != "Deployment" || observed.Name == "" || observed.UID == "" {
		return DeliveryBinding{}, NewValidationError("活动 Deployment 身份不完整，无法重新绑定")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i := range s.delivery.Bindings {
		if s.delivery.Bindings[i].ID == bindingID {
			index = i
			break
		}
	}
	if index < 0 {
		return DeliveryBinding{}, ErrNotFound
	}
	binding := s.delivery.Bindings[index]
	appFound := false
	for _, app := range s.apps {
		if app.ID == binding.AppID {
			appFound = true
			if app.ClusterID != observed.ClusterID || app.Namespace != observed.Namespace || app.DeploymentName != observed.Name {
				return binding, newConflict("应用当前 Deployment 与重新绑定目标不一致，请刷新后重试")
			}
			break
		}
	}
	if !appFound {
		return binding, ErrNotFound
	}
	for _, ref := range []DeliveryWorkloadReference{binding.ActiveWorkloadRef, observed} {
		if ref.Name == "" || ref.ClusterID <= 0 || ref.Namespace == "" {
			continue
		}
		lock := ResourceLock{ClusterID: ref.ClusterID, Namespace: ref.Namespace, Kind: "deployment", Name: ref.Name}
		if owner, ok := s.resourceLocks[lock.Identity()]; ok {
			if expiresAt := s.resourceLockLeases[lock.Identity()]; expiresAt.IsZero() || expiresAt.After(time.Now()) {
				return binding, newConflict("Deployment 正由发布任务 %d 占用，不能重新绑定", owner)
			}
		}
	}
	template := binding.TemplateWorkloadRef
	if template.Name == "" || (template.UID == "" && template.ClusterID == observed.ClusterID && template.Namespace == observed.Namespace && template.Kind == observed.Kind && template.Name == observed.Name) {
		template = observed
	}
	if repo, ok := s.persistence.(DeliveryWorkloadReferenceRebindRepository); ok {
		persisted, err := repo.RebindDeliveryBindingActiveWorkloadRef(binding.ID, binding.TemplateWorkloadRef, binding.ActiveWorkloadRef, template, observed)
		if err != nil {
			return binding, err
		}
		binding = persisted
	} else if s.persistence != nil {
		return binding, NewValidationError("持久化层不支持显式重新绑定工作负载")
	} else {
		binding.TemplateWorkloadRef = template
		binding.ActiveWorkloadRef = observed
	}
	s.delivery.Bindings[index] = binding
	return binding, nil
}

func containsDeliveryBinding(rows []DeliveryBinding, appID int) bool {
	for _, row := range rows {
		if row.AppID == appID {
			return true
		}
	}
	return false
}

func (s *Store) DeliveryBindingForApp(appID int) (DeliveryBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, row := range s.delivery.Bindings {
		if row.AppID == appID {
			return row, nil
		}
	}
	return DeliveryBinding{}, fmt.Errorf("%w: 组件尚未关联逻辑环境", ErrNotFound)
}

// AppEnvironmentTier is conservative when legacy target metadata and logical
// environment metadata disagree: production wins until an administrator fixes
// the mismatch. Missing logical bindings retain the legacy cluster behavior.
func (s *Store) AppEnvironmentTier(appID int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appEnvironmentTierLocked(appID)
}

func (s *Store) appEnvironmentTierLocked(appID int) string {
	tier := ""
	clusterID := 0
	for _, app := range s.apps {
		if app.ID == appID {
			clusterID = app.ClusterID
			break
		}
	}
	for _, cluster := range s.clusters {
		if cluster.ID == clusterID {
			tier = cluster.EnvironmentTier
			break
		}
	}
	if tier == "production" {
		return tier
	}
	for _, binding := range s.delivery.Bindings {
		if binding.AppID != appID {
			continue
		}
		for _, environment := range s.delivery.Environments {
			if environment.ID == binding.EnvironmentID && environment.Tier == "production" {
				return "production"
			}
		}
	}
	return tier
}

// IsProductionCluster protects all workloads when either the registered
// cluster or any explicitly assigned logical environment is production.
func (s *Store) IsProductionCluster(clusterID int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, cluster := range s.clusters {
		if cluster.ID == clusterID && cluster.EnvironmentTier == "production" {
			return true
		}
	}
	for _, app := range s.apps {
		if app.ClusterID != clusterID {
			continue
		}
		if s.appEnvironmentTierLocked(app.ID) == "production" {
			return true
		}
	}
	return false
}
