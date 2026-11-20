package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

const BlueGreenCandidateRetentionHours = 24

func validateDeliveryPlanConfigurationSnapshot(plan DeliveryPlan) error {
	if plan.Snapshot.SchemaVersion < 7 || plan.Snapshot.Target.ConfigurationReferences == nil || strings.TrimSpace(plan.Snapshot.Target.ServiceAccountName) == "" {
		return NewValidationError("该计划缺少完整的 ConfigMap/Secret 引用快照，无法安全审批；请检查目标集群读取权限并重新生成计划")
	}
	if strings.TrimSpace(plan.Snapshot.Target.WorkloadUID) == "" {
		return NewValidationError("该计划缺少目标 Deployment UID，无法安全审批；请重新生成计划")
	}
	return nil
}

// DeliveryPlanSnapshot is a server-derived, immutable view of the artifact,
// logical binding and live Kubernetes baseline. It deliberately contains no
// cluster credentials, Secret values or Deployment manifest.
type DeliveryPlanSnapshot struct {
	SchemaVersion  int                                `json:"schema_version"`
	Artifact       DeliveryPlanArtifact               `json:"artifact"`
	Target         DeliveryPlanTarget                 `json:"target"`
	Strategy       string                             `json:"strategy"`
	Policy         DeliveryPlanPolicySnapshot         `json:"policy"`
	EvidencePolicy DeliveryPlanEvidencePolicySnapshot `json:"evidence_policy"`
	// Blue-green cleanup never removes candidate resources before this immutable
	// retention window has elapsed after a stable restore.
	CandidateRetentionHours int                 `json:"candidate_retention_hours,omitempty"`
	Checks                  []DeliveryPlanCheck `json:"checks,omitempty"`
}

type DeliveryPlanEvidencePolicySnapshot struct {
	EnvironmentID             int64               `json:"environment_id"`
	Version                   int64               `json:"version"`
	RequiredVerificationKinds []string            `json:"required_verification_kinds"`
	MaxEvidenceAgeMinutes     int                 `json:"max_evidence_age_minutes"`
	TrustedSubmitters         map[string][]string `json:"trusted_submitters"`
}

// DeliveryPlanPolicySnapshot records the immutable strategy revision used to
// build this plan. Version 0 identifies a legacy binding draft.
type DeliveryPlanPolicySnapshot struct {
	BindingID               int64                 `json:"binding_id"`
	Version                 int64                 `json:"version"`
	Source                  string                `json:"source"`
	Strategy                string                `json:"strategy"`
	ServiceName             string                `json:"service_name,omitempty"`
	CandidateRetentionHours int                   `json:"candidate_retention_hours,omitempty"`
	CanaryAnalysis          *CanaryAnalysisPolicy `json:"canary_analysis,omitempty"`
	Readiness               string                `json:"readiness"`
}

// DeliveryPlanCheck is a read-only preflight result captured with the plan.
// Unknown means the platform could not prove the condition; it is never a pass.
type DeliveryPlanCheck struct {
	Key    string `json:"key"`
	Status string `json:"status"` // passed, review, blocked, unknown
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type DeliveryPlanArtifact struct {
	ReleaseID                  int64                        `json:"release_id"`
	EventID                    string                       `json:"event_id"`
	Source                     string                       `json:"source,omitempty"`
	SourceKind                 string                       `json:"source_kind,omitempty"`
	SourceRepository           string                       `json:"source_repository,omitempty"`
	SourcePath                 string                       `json:"source_path,omitempty"`
	DeploymentConfigRepository string                       `json:"deployment_config_repository,omitempty"`
	DeploymentConfigPath       string                       `json:"deployment_config_path,omitempty"`
	ImageRepository            string                       `json:"image_repository,omitempty"`
	Image                      string                       `json:"image"`
	Digest                     *string                      `json:"digest,omitempty"`
	DigestSource               string                       `json:"digest_source,omitempty"`
	Version                    string                       `json:"version"`
	PipelineID                 *string                      `json:"pipeline_id,omitempty"`
	PipelineName               *string                      `json:"pipeline_name,omitempty"`
	Branch                     *string                      `json:"branch,omitempty"`
	CommitID                   *string                      `json:"commit_id,omitempty"`
	Build                      *string                      `json:"build_number,omitempty"`
	ConfigurationSource        *ConfigurationSourceRevision `json:"configuration_source,omitempty"`
}

func deliveryPlanArtifactSnapshot(release ImageRelease) DeliveryPlanArtifact {
	artifact := DeliveryPlanArtifact{
		ReleaseID: release.ID, EventID: release.EventID, Source: release.Source,
		Image: deployImage(release), Digest: release.ImageDigest,
		DigestSource: release.Metadata.DigestSource, Version: release.Version,
		PipelineID: release.PipelineID, PipelineName: release.PipelineName,
		Branch: release.Branch, CommitID: release.CommitID, Build: release.BuildNumber,
	}
	artifact.ConfigurationSource = validatedReleaseConfigurationSource(release)
	artifact.ImageRepository = ImageRepository(artifact.Image)
	if binding := release.Metadata.Binding; binding != nil {
		artifact.SourceKind = binding.SourceKind
		artifact.SourcePath = binding.SourcePath
		artifact.SourceRepository = safeSourceRepository(binding.SourceRepoURL)
		if repository, repositoryPath, err := normalizeDeploymentConfigSource(binding.DeploymentConfigRepoURL, binding.DeploymentConfigPath); err == nil {
			artifact.DeploymentConfigRepository = repository
			artifact.DeploymentConfigPath = repositoryPath
		}
		if artifact.ImageRepository == "" {
			artifact.ImageRepository = binding.ImageRepository
		}
	}
	return artifact
}

func validatedReleaseConfigurationSource(release ImageRelease) *ConfigurationSourceRevision {
	source := release.Metadata.ConfigurationSource
	if source == nil {
		return nil
	}
	copy, err := normalizeConfigurationSourceRevision(*source)
	runnerID, attributionErr := strconv.ParseInt(strings.TrimPrefix(source.RecordedBy, "runner:"), 10, 64)
	if err != nil || attributionErr != nil || !strings.HasPrefix(source.RecordedBy, "runner:") || runnerID <= 0 || source.RecordedAt.IsZero() {
		return nil
	}
	copy.RecordedBy = source.RecordedBy
	copy.RecordedAt = source.RecordedAt.UTC()
	return &copy
}

// stableConfigurationSourceForDeployment follows the newest successful
// deployment record for this exact workload. It never falls back to the
// candidate release or an older matching image when the recorded baseline
// cannot be proven against the live Deployment image.
func stableConfigurationSourceForDeployment(store *Store, clusterID int, app App, currentImage string) (int64, *ConfigurationSourceRevision) {
	for _, log := range store.Logs(clusterID, app.AppName, "success", 0) {
		if log.Namespace != app.Namespace || log.DeploymentName != app.DeploymentName || log.ContainerName != app.ContainerName {
			continue
		}
		if log.ImageFull == nil || strings.TrimSpace(*log.ImageFull) != strings.TrimSpace(currentImage) || log.ReleaseID <= 0 {
			return 0, nil
		}
		release, err := store.FindImageRelease(log.ReleaseID)
		if err != nil || deployImage(release) != strings.TrimSpace(currentImage) {
			return 0, nil
		}
		return release.ID, validatedReleaseConfigurationSource(release)
	}
	return 0, nil
}

// A scope review is only an audit statement about one pipeline definition.
// It must never be interpreted as proof that Yunxiao's current definition or
// execution permissions still match, or as permission to trigger that pipeline.
func matchingDeploymentPipelineScopeReview(model DeliveryModel, binding DeliveryBinding) *DeliveryPipelineScopeReview {
	if binding.DeploymentPipeline == nil {
		return nil
	}
	var latest *DeliveryPipelineScopeReview
	for _, review := range model.PipelineScopeReviews {
		if review.BindingID == binding.ID && (latest == nil || review.ID > latest.ID) {
			copy := review
			latest = &copy
		}
	}
	if latest == nil || latest.ID <= 0 || latest.ConnectionID != binding.DeploymentPipeline.ConnectionID || latest.PipelineID != binding.DeploymentPipeline.PipelineID ||
		!pipelineScopeHashPattern.MatchString(latest.DefinitionHash) || latest.ReviewedAt.IsZero() || strings.TrimSpace(latest.Reviewer) == "" ||
		(latest.WriteScope != PipelineScopeConfigurationOnly && latest.WriteScope != PipelineScopeWorkloadOrImage && latest.WriteScope != PipelineScopeUnknown) {
		return nil
	}
	return latest
}

func deploymentPipelineScopeCheck(pipeline *YunxiaoDeploymentPipelineBinding, review *DeliveryPipelineScopeReview) DeliveryPlanCheck {
	check := DeliveryPlanCheck{Key: "deployment-pipeline-scope", Status: "unknown", Title: "配置恢复流水线写入范围"}
	if pipeline == nil {
		check.Detail = "目标未绑定独立的配置部署流水线，平台无法自动恢复历史配置；镜像发布仍可按其自身门禁进行。"
		return check
	}
	if review == nil {
		check.Detail = "目标已绑定配置部署流水线，但没有匹配当前连接和流水线 ID 的人工写入范围记录；自动配置恢复保持关闭。"
		return check
	}
	switch review.WriteScope {
	case PipelineScopeConfigurationOnly:
		check.Status = "review"
		check.Detail = fmt.Sprintf("人工核对记录 #%d 认为定义 SHA-256 %s 只写配置；平台尚未回读云效当前定义、实际执行身份或目标资源，不能据此自动触发配置恢复。", review.ID, review.DefinitionHash)
	case PipelineScopeWorkloadOrImage:
		check.Detail = fmt.Sprintf("人工核对记录 #%d 指出该流水线可能写入 Deployment 或镜像；与发布 Worker 的写入边界未核实，自动配置恢复保持关闭。", review.ID)
	default:
		check.Detail = fmt.Sprintf("人工核对记录 #%d 的写入范围仍不明确，自动配置恢复保持关闭。", review.ID)
	}
	return check
}

// Source repositories are displayed in an approval snapshot. Keep only
// credential-free HTTPS repository URLs so legacy malformed bindings cannot
// leak userinfo, query tokens, or fragments into long-lived plan records.
func safeSourceRepository(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") == "" {
		return ""
	}
	return u.String()
}

func deliveryConfigurationReferences(deployment *unstructured.Unstructured) ([]DeliveryConfigurationReference, bool) {
	if deployment == nil {
		return nil, false
	}
	podSpec, found, err := unstructured.NestedMap(deployment.Object, "spec", "template", "spec")
	if err != nil || !found {
		return nil, false
	}
	references := map[string]DeliveryConfigurationReference{}
	known := true
	add := func(kind, name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			known = false
			return
		}
		key := kind + "\x00" + name
		references[key] = DeliveryConfigurationReference{Kind: kind, Name: name}
	}
	readReference := func(parent map[string]any, field, kind, nameField string) {
		value, exists := parent[field]
		if !exists {
			return
		}
		ref, ok := value.(map[string]any)
		if !ok {
			known = false
			return
		}
		name, _ := ref[nameField].(string)
		add(kind, name)
	}
	readOptionalSlice := func(parent map[string]any, field string) ([]any, bool) {
		raw, exists := parent[field]
		if !exists {
			return nil, true
		}
		entries, ok := raw.([]any)
		if !ok {
			known = false
			return nil, false
		}
		return entries, true
	}
	readEnvReferences := func(container map[string]any, field string, fromEnv bool) {
		entries, ok := readOptionalSlice(container, field)
		if !ok {
			return
		}
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				known = false
				continue
			}
			if fromEnv {
				readReference(entry, "configMapRef", "ConfigMap", "name")
				readReference(entry, "secretRef", "Secret", "name")
				continue
			}
			valueFromRaw, exists := entry["valueFrom"]
			if !exists {
				continue
			}
			valueFrom, ok := valueFromRaw.(map[string]any)
			if !ok {
				known = false
				continue
			}
			readReference(valueFrom, "configMapKeyRef", "ConfigMap", "name")
			readReference(valueFrom, "secretKeyRef", "Secret", "name")
		}
	}
	for _, field := range []string{"containers", "initContainers"} {
		containers, ok := readOptionalSlice(podSpec, field)
		if !ok {
			continue
		}
		for _, rawContainer := range containers {
			container, ok := rawContainer.(map[string]any)
			if !ok {
				known = false
				continue
			}
			readEnvReferences(container, "env", false)
			readEnvReferences(container, "envFrom", true)
		}
	}
	volumes, ok := readOptionalSlice(podSpec, "volumes")
	if !ok {
		return nil, false
	}
	for _, rawVolume := range volumes {
		volume, ok := rawVolume.(map[string]any)
		if !ok {
			known = false
			continue
		}
		readReference(volume, "configMap", "ConfigMap", "name")
		readReference(volume, "secret", "Secret", "secretName")
		projectedRaw, exists := volume["projected"]
		if !exists {
			continue
		}
		projected, ok := projectedRaw.(map[string]any)
		if !ok {
			known = false
			continue
		}
		sources, ok := readOptionalSlice(projected, "sources")
		if !ok || sources == nil {
			known = false
			continue
		}
		for _, rawSource := range sources {
			source, ok := rawSource.(map[string]any)
			if !ok {
				known = false
				continue
			}
			readReference(source, "configMap", "ConfigMap", "name")
			readReference(source, "secret", "Secret", "name")
		}
	}
	imagePullSecrets, ok := readOptionalSlice(podSpec, "imagePullSecrets")
	if !ok {
		return nil, false
	}
	for _, rawSecret := range imagePullSecrets {
		secret, ok := rawSecret.(map[string]any)
		if !ok {
			known = false
			continue
		}
		name, _ := secret["name"].(string)
		add("Secret", name)
	}
	if !known {
		return nil, false
	}
	result := make([]DeliveryConfigurationReference, 0, len(references))
	for _, reference := range references {
		result = append(result, reference)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Name < result[j].Name
	})
	return result, true
}

// deliveryServiceAccountPullSecretLookup resolves the ServiceAccount whose
// imagePullSecrets Kubernetes will copy onto newly created Pods. Explicit
// imagePullSecrets on the Pod template suppress that ServiceAccount injection.
func deliveryServiceAccountPullSecretLookup(deployment *unstructured.Unstructured) (string, bool, bool) {
	if deployment == nil {
		return "", false, false
	}
	podSpec, found, err := unstructured.NestedMap(deployment.Object, "spec", "template", "spec")
	if err != nil || !found {
		return "", false, false
	}
	serviceAccount, found, err := unstructured.NestedString(podSpec, "serviceAccountName")
	if err != nil {
		return "", false, false
	}
	if !found || strings.TrimSpace(serviceAccount) == "" {
		serviceAccount = "default"
	}
	imagePullSecrets, found, err := unstructured.NestedSlice(podSpec, "imagePullSecrets")
	if err != nil {
		return "", false, false
	}
	return serviceAccount, !found || len(imagePullSecrets) == 0, true
}

func deliveryServiceAccountImagePullSecretReferences(serviceAccount *unstructured.Unstructured) ([]DeliveryConfigurationReference, bool) {
	if serviceAccount == nil {
		return nil, false
	}
	imagePullSecrets, found, err := unstructured.NestedSlice(serviceAccount.Object, "imagePullSecrets")
	if err != nil {
		return nil, false
	}
	if !found {
		return []DeliveryConfigurationReference{}, true
	}
	references := map[string]DeliveryConfigurationReference{}
	for _, rawSecret := range imagePullSecrets {
		secret, ok := rawSecret.(map[string]any)
		if !ok {
			return nil, false
		}
		name, _ := secret["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, false
		}
		references["Secret\x00"+name] = DeliveryConfigurationReference{Kind: "Secret", Name: name}
	}
	result := make([]DeliveryConfigurationReference, 0, len(references))
	for _, reference := range references {
		result = append(result, reference)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, true
}

func mergeDeliveryConfigurationReferences(groups ...[]DeliveryConfigurationReference) []DeliveryConfigurationReference {
	references := map[string]DeliveryConfigurationReference{}
	for _, group := range groups {
		for _, reference := range group {
			key := reference.Kind + "\x00" + reference.Name
			references[key] = reference
		}
	}
	result := make([]DeliveryConfigurationReference, 0, len(references))
	for _, reference := range references {
		result = append(result, reference)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Name < result[j].Name
	})
	return result
}

// deliveryConfigurationReferencesForWorkload records direct Pod-template refs
// and imagePullSecrets inherited from its effective ServiceAccount. The
// ServiceAccount read is internal to delivery planning and intentionally does
// not expose ServiceAccounts through the generic resource browser.
func deliveryConfigurationReferencesForWorkload(ctx context.Context, runtime KubernetesRuntime, namespace string, deployment *unstructured.Unstructured) ([]DeliveryConfigurationReference, bool, bool) {
	references, known := deliveryConfigurationReferences(deployment)
	if !known || runtime == nil {
		return nil, false, false
	}
	serviceAccountName, includePullSecrets, serviceAccountKnown := deliveryServiceAccountPullSecretLookup(deployment)
	if !serviceAccountKnown {
		return nil, false, false
	}
	if !includePullSecrets {
		return deliveryConfigurationReferenceVersions(ctx, runtime, namespace, mergeDeliveryConfigurationReferences(references))
	}
	serviceAccountDef := kube.ResourceDef{Version: "v1", Resource: "serviceaccounts", Kind: "ServiceAccount", Namespaced: true}
	serviceAccount, err := runtime.GetResource(ctx, serviceAccountDef, namespace, serviceAccountName, metav1.GetOptions{})
	serviceAccountReferences, serviceAccountReferencesKnown := deliveryServiceAccountImagePullSecretReferences(serviceAccount)
	if err != nil || !serviceAccountReferencesKnown {
		return nil, false, false
	}
	return deliveryConfigurationReferenceVersions(ctx, runtime, namespace, mergeDeliveryConfigurationReferences(references, serviceAccountReferences))
}

func validateLiveDeliveryConfigurationReferences(ctx context.Context, runtime KubernetesRuntime, plan DeliveryPlan) error {
	target := plan.Snapshot.Target
	if err := validateDeliveryPlanConfigurationSnapshot(plan); err != nil {
		return err
	}
	if runtime == nil || target.Namespace == "" || target.Deployment == "" {
		return NewValidationError("无法核对审批计划中的外部配置引用")
	}
	deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return err
	}
	deployment, err := runtime.GetResource(ctx, deploymentDef, target.Namespace, target.Deployment, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("读取目标 Deployment 的配置引用失败: %w", err)
	}
	return validateDeliveryConfigurationReferencesAgainstSnapshot(ctx, runtime, plan, deployment)
}

func validateDeliveryConfigurationReferencesAgainstSnapshot(ctx context.Context, runtime KubernetesRuntime, plan DeliveryPlan, deployment *unstructured.Unstructured) error {
	target := plan.Snapshot.Target
	if err := validateDeliveryPlanConfigurationSnapshot(plan); err != nil {
		return err
	}
	if deployment == nil || deployment.GetNamespace() != target.Namespace || deployment.GetName() != target.Deployment || string(deployment.GetUID()) != target.WorkloadUID {
		return newConflict("审批后的 Deployment 身份已变化，不能继续发布")
	}
	serviceAccount, _, serviceAccountKnown := deliveryServiceAccountPullSecretLookup(deployment)
	if !serviceAccountKnown || serviceAccount != target.ServiceAccountName {
		return newConflict("审批后的 ServiceAccount 引用已变化或无法核对")
	}
	actual, known, metadataComplete := deliveryConfigurationReferencesForWorkload(ctx, runtime, target.Namespace, deployment)
	if !known {
		return NewValidationError("审批后的 ConfigMap/Secret 引用无法完整读取，不能继续发布")
	}
	expected := append([]DeliveryConfigurationReference{}, target.ConfigurationReferences...)
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].Kind != expected[j].Kind {
			return expected[i].Kind < expected[j].Kind
		}
		return expected[i].Name < expected[j].Name
	})
	if len(actual) != len(expected) {
		return newConflict("审批后的 ConfigMap/Secret 引用清单已变化，请重新生成发布计划")
	}
	for i := range expected {
		if expected[i].Kind != actual[i].Kind || expected[i].Name != actual[i].Name {
			return newConflict("审批后的 ConfigMap/Secret 引用清单已变化，请重新生成发布计划")
		}
		hasExpectedVersion := expected[i].UID != "" || expected[i].ResourceVersion != ""
		if hasExpectedVersion && (expected[i].UID == "" || expected[i].ResourceVersion == "") {
			return NewValidationError("发布计划中的配置对象版本不完整，不能安全执行")
		}
		if !hasExpectedVersion {
			continue // The plan explicitly recorded names only and required human review.
		}
		if !metadataComplete || actual[i].UID == "" || actual[i].ResourceVersion == "" {
			return NewValidationError("审批后无法读取 ConfigMap/Secret 对象版本，不能继续发布")
		}
		if expected[i].UID != actual[i].UID || expected[i].ResourceVersion != actual[i].ResourceVersion {
			return newConflict("审批后的 ConfigMap/Secret 对象已变化，请重新生成并审批发布计划")
		}
	}
	return nil
}

type deliveryObjectMetadataReader interface {
	GetObjectMetadata(context.Context, kube.ResourceDef, string, string) (*metav1.PartialObjectMetadata, error)
}

func deliveryConfigurationReferenceVersions(ctx context.Context, runtime KubernetesRuntime, namespace string, references []DeliveryConfigurationReference) ([]DeliveryConfigurationReference, bool, bool) {
	if len(references) == 0 {
		return references, true, true
	}
	reader, supported := runtime.(deliveryObjectMetadataReader)
	if !supported {
		return references, true, false
	}
	complete := true
	for i := range references {
		resource := "configmaps"
		if references[i].Kind == "Secret" {
			resource = "secrets"
		}
		def, err := kube.LookupResource("", "v1", resource)
		if err != nil {
			complete = false
			continue
		}
		object, err := reader.GetObjectMetadata(ctx, def, namespace, references[i].Name)
		if err != nil || object == nil || object.Namespace != namespace || object.Name != references[i].Name || object.UID == "" || object.ResourceVersion == "" {
			complete = false
			continue
		}
		references[i].UID = string(object.UID)
		references[i].ResourceVersion = object.ResourceVersion
	}
	return references, true, complete
}

type DeliveryPlanTarget struct {
	ClusterID                         int                               `json:"cluster_id"`
	ClusterName                       string                            `json:"cluster_name"`
	AppID                             int                               `json:"app_id"`
	AppName                           string                            `json:"app_name"`
	TestAppID                         int                               `json:"test_app_id"`
	TestBindingID                     int64                             `json:"test_binding_id"`
	TestEnvironmentID                 int64                             `json:"test_environment_id"`
	TestEnvironmentName               string                            `json:"test_environment_name"`
	Namespace                         string                            `json:"namespace"`
	Deployment                        string                            `json:"deployment"`
	Container                         string                            `json:"container"`
	ServiceName                       string                            `json:"service_name,omitempty"`
	ServiceUID                        string                            `json:"service_uid,omitempty"`
	ServiceResourceVersion            string                            `json:"service_resource_version,omitempty"`
	ServiceSelector                   map[string]string                 `json:"service_selector,omitempty"`
	ServiceConfigHash                 string                            `json:"service_config_hash,omitempty"`
	StableLabelKey                    string                            `json:"stable_label_key,omitempty"`
	StableLabelAlreadyPresent         bool                              `json:"stable_label_already_present,omitempty"`
	CandidateLabelKey                 string                            `json:"candidate_label_key,omitempty"`
	CandidateLabelValue               string                            `json:"candidate_label_value,omitempty"`
	CandidateRemovedLabels            []string                          `json:"candidate_removed_label_keys,omitempty"`
	EnvironmentID                     int64                             `json:"environment_id"`
	EnvironmentName                   string                            `json:"environment_name"`
	EnvironmentTier                   string                            `json:"environment_tier"`
	ApplicationID                     int64                             `json:"application_id"`
	ApplicationName                   string                            `json:"application_name"`
	ComponentID                       int64                             `json:"component_id"`
	ComponentName                     string                            `json:"component_name"`
	BindingID                         int64                             `json:"binding_id"`
	TemplateWorkloadRef               DeliveryWorkloadReference         `json:"template_workload_ref"`
	ActiveWorkloadRef                 DeliveryWorkloadReference         `json:"active_workload_ref"`
	WorkloadUID                       string                            `json:"workload_uid"`
	ResourceVersion                   string                            `json:"resource_version"`
	CurrentImage                      string                            `json:"current_image"`
	StableImageDigest                 string                            `json:"stable_image_digest,omitempty"`
	StableReleaseID                   int64                             `json:"stable_release_id,omitempty"`
	StableConfigurationSource         *ConfigurationSourceRevision      `json:"stable_configuration_source,omitempty"`
	DeploymentPipeline                *YunxiaoDeploymentPipelineBinding `json:"deployment_pipeline,omitempty"`
	DeploymentPipelineScopeReview     *DeliveryPipelineScopeReview      `json:"deployment_pipeline_scope_review,omitempty"`
	WorkloadHash                      string                            `json:"workload_hash"`
	Replicas                          int64                             `json:"replicas,omitempty"`
	ServiceAccountName                string                            `json:"service_account_name"`
	ServiceAccountPullSecretsIncluded bool                              `json:"service_account_pull_secrets_included"`
	ConfigurationReferences           []DeliveryConfigurationReference  `json:"configuration_references"`
}

// DeliveryConfigurationReference records ConfigMap/Secret metadata for names
// referenced by the Pod template or inherited from its ServiceAccount. It
// never contains resource data or Secret values; Kubernetes resourceVersion
// detects changes to a named object but is not a durable restore source.
type DeliveryConfigurationReference struct {
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resource_version,omitempty"`
}

// DeliveryPlan is a durable immutable preview record. Rolling plans can be
// linked to the existing promotion approval; full orchestration/run state for
// blue-green and gateway canary remains separate work.
type DeliveryPlan struct {
	ID              int64                `json:"id"`
	ReleaseID       int64                `json:"release_id"`
	TargetClusterID int                  `json:"target_cluster_id"`
	TargetAppID     int                  `json:"target_app_id"`
	Strategy        string               `json:"strategy"`
	Status          string               `json:"status"`
	SnapshotHash    string               `json:"snapshot_hash"`
	Snapshot        DeliveryPlanSnapshot `json:"snapshot"`
	Blockers        []string             `json:"blockers"`
	RequestedBy     string               `json:"requested_by"`
	CreatedAt       time.Time            `json:"created_at"`
}

// DeliveryPlanRepository remains optional so in-memory stores and existing
// persistence doubles continue to work.
type DeliveryPlanRepository interface {
	DeliveryPlans() ([]DeliveryPlan, error)
	SaveDeliveryPlan(DeliveryPlan) (DeliveryPlan, bool, error)
	FindDeliveryPlan(int64) (DeliveryPlan, error)
}

func cloneDeliveryPlan(plan DeliveryPlan) DeliveryPlan {
	plan.Blockers = append([]string{}, plan.Blockers...)
	if plan.Snapshot.Target.ConfigurationReferences != nil {
		references := make([]DeliveryConfigurationReference, len(plan.Snapshot.Target.ConfigurationReferences))
		copy(references, plan.Snapshot.Target.ConfigurationReferences)
		plan.Snapshot.Target.ConfigurationReferences = references
	}
	if plan.Snapshot.Checks != nil {
		checks := make([]DeliveryPlanCheck, len(plan.Snapshot.Checks))
		copy(checks, plan.Snapshot.Checks)
		plan.Snapshot.Checks = checks
	}
	plan.Snapshot.Policy.CanaryAnalysis = cloneCanaryAnalysisPolicy(plan.Snapshot.Policy.CanaryAnalysis)
	if plan.Snapshot.Target.StableConfigurationSource != nil {
		source := *plan.Snapshot.Target.StableConfigurationSource
		plan.Snapshot.Target.StableConfigurationSource = &source
	}
	plan.Snapshot.Target.DeploymentPipeline = cloneYunxiaoDeploymentPipelineBinding(plan.Snapshot.Target.DeploymentPipeline)
	if plan.Snapshot.Target.DeploymentPipelineScopeReview != nil {
		review := *plan.Snapshot.Target.DeploymentPipelineScopeReview
		plan.Snapshot.Target.DeploymentPipelineScopeReview = &review
	}
	return plan
}

func (s *Store) LoadDeliveryPlans(plans []DeliveryPlan) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveryPlans = make([]DeliveryPlan, len(plans))
	s.nextDeliveryPlan = 1
	for i, plan := range plans {
		s.deliveryPlans[i] = cloneDeliveryPlan(plan)
		if plan.ID >= s.nextDeliveryPlan {
			s.nextDeliveryPlan = plan.ID + 1
		}
	}
}

func (s *Store) CreateDeliveryPlan(plan DeliveryPlan) (DeliveryPlan, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.deliveryPlans {
		if existing.SnapshotHash == plan.SnapshotHash {
			return cloneDeliveryPlan(existing), false, nil
		}
	}
	if repo, ok := s.persistence.(DeliveryPlanRepository); ok {
		saved, created, err := repo.SaveDeliveryPlan(plan)
		if err != nil {
			return plan, false, err
		}
		if !created {
			return cloneDeliveryPlan(saved), false, nil
		}
		plan = saved
	} else {
		plan.ID = s.nextDeliveryPlan
		s.nextDeliveryPlan++
		if plan.CreatedAt.IsZero() {
			plan.CreatedAt = time.Now()
		}
	}
	s.deliveryPlans = append(s.deliveryPlans, cloneDeliveryPlan(plan))
	return cloneDeliveryPlan(plan), true, nil
}

func (s *Store) DeliveryPlans() []DeliveryPlan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]DeliveryPlan, len(s.deliveryPlans))
	for i, plan := range s.deliveryPlans {
		rows[i] = cloneDeliveryPlan(plan)
	}
	return rows
}

func (s *Store) FindDeliveryPlan(id int64) (DeliveryPlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, plan := range s.deliveryPlans {
		if plan.ID == id {
			return cloneDeliveryPlan(plan), nil
		}
	}
	return DeliveryPlan{}, fmt.Errorf("%w: 发布计划 ID=%d 不存在", ErrNotFound, id)
}

// CreateDeliveryPlan snapshots a production target without changing it. The
// existing production checks are reused, then the exact logical component and
// current Deployment identity are captured for later approval work.
func (s *CDService) CreateDeliveryPlan(ctx context.Context, releaseID int64, targetClusterID int, requester string) (DeliveryPlan, bool, error) {
	release, err := s.store.FindImageRelease(releaseID)
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	cluster, app, err := s.validatePromotionTarget(release, targetClusterID)
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	testApp, err := s.store.FindAppByName(release.TestClusterID, release.AppName)
	if err != nil {
		return DeliveryPlan{}, false, NewValidationError("找不到该制品对应的测试环境应用绑定")
	}
	testBinding, testBindErr := s.store.DeliveryBindingForApp(testApp.ID)
	targetBinding, targetBindErr := s.store.DeliveryBindingForApp(app.ID)
	if testBindErr != nil || targetBindErr != nil {
		return DeliveryPlan{}, false, NewValidationError("请先在环境模型中完成测试与生产应用的组件绑定")
	}
	model := s.store.DeliveryModel()
	applicationByID := map[int64]DeliveryApplication{}
	componentByID := map[int64]DeliveryComponent{}
	environmentByID := map[int64]DeliveryEnvironment{}
	for _, row := range model.Applications {
		applicationByID[row.ID] = row
	}
	for _, row := range model.Components {
		componentByID[row.ID] = row
	}
	for _, row := range model.Environments {
		environmentByID[row.ID] = row
	}
	testComponent, testComponentOK := componentByID[testBinding.ComponentID]
	targetComponent, targetComponentOK := componentByID[targetBinding.ComponentID]
	testApplication, testApplicationOK := applicationByID[testBinding.ApplicationID]
	targetApplication, targetApplicationOK := applicationByID[targetBinding.ApplicationID]
	environment, environmentOK := environmentByID[targetBinding.EnvironmentID]
	testEnvironment, testEnvironmentOK := environmentByID[testBinding.EnvironmentID]
	testCluster, testClusterErr := s.store.FindCluster(testApp.ClusterID)
	if !testComponentOK || !targetComponentOK || !testApplicationOK || !targetApplicationOK || !environmentOK || !testEnvironmentOK || testClusterErr != nil {
		return DeliveryPlan{}, false, NewValidationError("组件、业务应用或环境关联不完整，请重新核对绑定")
	}
	if testBinding.RequiresReview || targetBinding.RequiresReview || testComponent.RequiresReview || targetComponent.RequiresReview || testApplication.RequiresReview || targetApplication.RequiresReview || environment.RequiresReview || testEnvironment.RequiresReview {
		return DeliveryPlan{}, false, NewValidationError("测试或生产绑定仍待核对，请先完成环境接入确认")
	}
	if testEnvironment.Tier != testCluster.EnvironmentTier {
		return DeliveryPlan{}, false, NewValidationError("测试绑定的逻辑环境与测试集群环境不一致")
	}
	if testBinding.ApplicationID != targetBinding.ApplicationID || testBinding.ComponentID != targetBinding.ComponentID {
		return DeliveryPlan{}, false, NewValidationError("测试与生产目标没有绑定到同一业务组件，不能生成晋级计划")
	}
	if environment.Tier != "production" || environment.Tier != cluster.EnvironmentTier {
		return DeliveryPlan{}, false, NewValidationError("生产绑定的逻辑环境与目标集群环境不一致")
	}
	evidencePolicy, err := s.store.DeliveryEnvironmentEvidencePolicyForApp(app.ID)
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	policySnapshot := DeliveryPlanPolicySnapshot{
		BindingID: targetBinding.ID, Source: "legacy_binding_draft",
		Strategy:    normalizeDeliveryStrategy(strings.TrimSpace(targetBinding.StrategyDraft)),
		ServiceName: targetBinding.ServiceName,
	}
	if policySnapshot.Strategy == "blue_green" {
		policySnapshot.CandidateRetentionHours = BlueGreenCandidateRetentionHours
	}
	if policySnapshot.Strategy == "gateway_canary" {
		policySnapshot.Readiness = "integration_pending"
	} else {
		policySnapshot.Readiness = "configurable"
	}
	if policy, policyErr := s.store.CurrentDeliveryPolicy(targetBinding.ID); policyErr == nil {
		policySnapshot = DeliveryPlanPolicySnapshot{
			BindingID: policy.BindingID, Version: policy.Version, Source: "versioned_policy",
			Strategy: policy.Strategy, ServiceName: policy.ServiceName,
			CandidateRetentionHours: policy.CandidateRetentionHours, CanaryAnalysis: cloneCanaryAnalysisPolicy(policy.CanaryAnalysis), Readiness: policy.Readiness,
		}
		targetBinding.ServiceName = policy.ServiceName
	} else if policyErr != ErrNotFound {
		return DeliveryPlan{}, false, policyErr
	}
	strategy := normalizeDeliveryStrategy(policySnapshot.Strategy)
	if strategy != "rolling" && strategy != "blue_green" && strategy != "gateway_canary" {
		return DeliveryPlan{}, false, NewValidationError("目标绑定中的发布策略无效")
	}
	if s.worker == nil || s.worker.pipeline == nil || s.worker.pipeline.k8s == nil {
		return DeliveryPlan{}, false, NewValidationError("发布运行时不可用，无法读取目标基线")
	}
	rt, err := s.worker.pipeline.k8s.For(cluster)
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	def, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	obj, err := rt.GetResource(readCtx, def, app.Namespace, app.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	if obj == nil {
		return DeliveryPlan{}, false, NewValidationError("目标 Deployment 不存在")
	}
	observedWorkloadRef := DeliveryWorkloadReference{
		ClusterID: cluster.ID, Namespace: app.Namespace, Kind: "Deployment", Name: app.DeploymentName,
		UID: string(obj.GetUID()), ResourceVersion: obj.GetResourceVersion(),
	}
	targetBinding, err = s.store.PinDeliveryBindingWorkloadRefs(targetBinding.ID, observedWorkloadRef)
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	workloadHash, err := workloadHash(obj.DeepCopy(), app.ContainerName, s.cfg.MockRuntime)
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	currentImage := targetContainerImage(obj, app.ContainerName)
	if currentImage == "" {
		return DeliveryPlan{}, false, NewValidationError("目标 Deployment 中找不到绑定的容器")
	}
	stableImageDigest := imageReferenceDigest(currentImage)
	var stableDigestErr error
	if strategy == "gateway_canary" && stableImageDigest == "" {
		if s.resolveDigest == nil {
			stableDigestErr = NewValidationError("没有可用的镜像摘要解析器")
		} else {
			stableImageDigest, stableDigestErr = s.resolveDigest(readCtx, s.cfg, currentImage)
			stableImageDigest = NormalizeDigest(stableImageDigest)
			if stableDigestErr == nil && stableImageDigest == "" {
				stableDigestErr = NewValidationError("当前稳定镜像未解析为 sha256 digest")
			}
		}
		if stableDigestErr != nil {
			stableImageDigest = ""
		}
	}
	if stableImageDigest != "" {
		stableImageDigest = NormalizeDigest(stableImageDigest)
	}
	stableReleaseID, stableConfigurationSource := stableConfigurationSourceForDeployment(s.store, cluster.ID, app, currentImage)
	deploymentPipelineScopeReview := matchingDeploymentPipelineScopeReview(model, targetBinding)
	serviceAccountName, includeServiceAccountPullSecrets, serviceAccountLookupKnown := deliveryServiceAccountPullSecretLookup(obj)
	configurationReferences, configurationReferencesKnown, configurationMetadataComplete := deliveryConfigurationReferencesForWorkload(readCtx, rt, app.Namespace, obj)
	if configurationReferencesKnown {
		configurationReferences = append([]DeliveryConfigurationReference{}, configurationReferences...)
	} else {
		configurationReferences = nil
	}

	plan := DeliveryPlan{
		ReleaseID: release.ID, TargetClusterID: cluster.ID, TargetAppID: app.ID,
		Strategy: strategy, Status: "preview_only", RequestedBy: requester,
		Snapshot: DeliveryPlanSnapshot{
			SchemaVersion: 12,
			Artifact:      deliveryPlanArtifactSnapshot(release),
			EvidencePolicy: DeliveryPlanEvidencePolicySnapshot{
				EnvironmentID: evidencePolicy.EnvironmentID, Version: evidencePolicy.Version,
				RequiredVerificationKinds: append([]string{}, evidencePolicy.RequiredVerificationKinds...),
				MaxEvidenceAgeMinutes:     evidencePolicy.MaxEvidenceAgeMinutes,
				TrustedSubmitters:         cloneEvidenceSubmitters(evidencePolicy.TrustedSubmitters),
			},
			Target: DeliveryPlanTarget{
				ClusterID: cluster.ID, ClusterName: cluster.Name, AppID: app.ID, AppName: app.AppName,
				TestAppID: testApp.ID, TestBindingID: testBinding.ID, TestEnvironmentID: testBinding.EnvironmentID, TestEnvironmentName: testEnvironment.Name,
				Namespace: app.Namespace, Deployment: app.DeploymentName, Container: app.ContainerName,
				ServiceName:   targetBinding.ServiceName,
				EnvironmentID: environment.ID, EnvironmentName: environment.Name, EnvironmentTier: environment.Tier,
				ApplicationID: targetApplication.ID, ApplicationName: targetApplication.Name,
				ComponentID: targetComponent.ID, ComponentName: targetComponent.Name, BindingID: targetBinding.ID,
				TemplateWorkloadRef: targetBinding.TemplateWorkloadRef, ActiveWorkloadRef: targetBinding.ActiveWorkloadRef,
				WorkloadUID: string(obj.GetUID()), ResourceVersion: obj.GetResourceVersion(), CurrentImage: currentImage,
				StableImageDigest: stableImageDigest, StableReleaseID: stableReleaseID,
				StableConfigurationSource: stableConfigurationSource,
				DeploymentPipeline:        cloneYunxiaoDeploymentPipelineBinding(targetBinding.DeploymentPipeline), WorkloadHash: workloadHash,
				DeploymentPipelineScopeReview:     deploymentPipelineScopeReview,
				ServiceAccountName:                serviceAccountName,
				ServiceAccountPullSecretsIncluded: serviceAccountLookupKnown && includeServiceAccountPullSecrets,
				ConfigurationReferences:           configurationReferences,
			},
			Strategy: strategy,
			Policy:   policySnapshot,
		},
		Blockers: []string{},
	}
	plan.Snapshot.Checks = append(plan.Snapshot.Checks, deploymentPipelineScopeCheck(targetBinding.DeploymentPipeline, deploymentPipelineScopeReview))
	if stableReleaseID == 0 {
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, DeliveryPlanCheck{
			Key: "stable-configuration-source", Status: "unknown", Title: "稳定版本恢复来源",
			Detail: "未找到与当前 Deployment 镜像、namespace、Deployment 和容器精确匹配的最近成功制品部署记录；不能将候选制品配置用于恢复。",
		})
	} else if stableConfigurationSource == nil {
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, DeliveryPlanCheck{
			Key: "stable-configuration-source", Status: "unknown", Title: "稳定版本恢复来源",
			Detail: fmt.Sprintf("当前镜像匹配最近成功制品部署 #%d，但该制品没有可验证的 Runner 配置 commit；配置恢复来源仍未知。", stableReleaseID),
		})
	} else {
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, DeliveryPlanCheck{
			Key: "stable-configuration-source", Status: "review", Title: "稳定版本恢复来源",
			Detail: fmt.Sprintf("当前镜像对应最近成功制品部署 #%d，记录的配置来源为 %s / %s @ %s。计划已固定此稳定来源及目标配置流水线，但 Runner 上报只证明来源声明，不能证明实际渲染或应用的内容。", stableReleaseID, stableConfigurationSource.Repository, stableConfigurationSource.Path, stableConfigurationSource.Revision),
		})
	}
	if source := plan.Snapshot.Artifact.ConfigurationSource; source == nil {
		status := "unknown"
		detail := "Runner 未上报本次发布使用的配置仓库 revision。ConfigMap/Secret 元数据只用于发现漂移，不是可恢复的配置副本；请人工确认 Helm/Git 配置来源与恢复方式。"
		if plan.Snapshot.Artifact.DeploymentConfigRepository != "" {
			status = "blocked"
			detail = "组件已登记部署配置仓库，但本次制品没有固定对应的 Git commit；请让 Runner 上报 DEPLOY_CONFIG_REVISION 后重新生成发布计划。"
			plan.Blockers = append(plan.Blockers, "配置仓库 revision："+detail)
		}
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, DeliveryPlanCheck{
			Key: "configuration-source", Status: status, Title: "配置仓库 revision", Detail: detail,
		})
	} else {
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, DeliveryPlanCheck{
			Key: "configuration-source", Status: "review", Title: "配置仓库 revision",
			Detail: fmt.Sprintf("Runner %s 上报了 %s / %s @ %s。计划已固定此声明，但平台不会验证 checkout 内容或 Helm 渲染结果，也不能据此恢复配置；请人工核对配置发布与数据库迁移兼容性。", source.RecordedBy, source.Repository, source.Path, source.Revision),
		})
	}
	if !configurationReferencesKnown {
		check := DeliveryPlanCheck{
			Key: "configuration-snapshot", Status: "unknown", Title: "配置来源快照",
			Detail: "无法完整读取 Pod 模板及其 ServiceAccount 镜像拉取引用，不能确认 ConfigMap/Secret 清单；请检查工作负载、执行身份权限和集群读取结果。",
		}
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, check)
		plan.Blockers = append(plan.Blockers, check.Title+"："+check.Detail)
	} else if len(configurationReferences) == 0 {
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, DeliveryPlanCheck{
			Key: "configuration-snapshot", Status: "passed", Title: "配置来源快照",
			Detail: "Pod 模板及其 ServiceAccount 未发现 ConfigMap/Secret 引用；其他外部配置来源不在此检查范围。",
		})
	} else {
		detail := fmt.Sprintf("发现 %d 个 ConfigMap/Secret 引用；计划固定资源名称与 UID/resourceVersion，仅用于发现对象替换或变更，不采集配置值且不能恢复历史内容。", len(configurationReferences))
		if !configurationMetadataComplete {
			detail = fmt.Sprintf("发现 %d 个 ConfigMap/Secret 引用；当前接入方式无法完整读取对象元数据版本，计划仅固定引用名称。请人工确认配置版本与恢复限制；平台不会读取配置值。", len(configurationReferences))
		}
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, DeliveryPlanCheck{
			Key: "configuration-snapshot", Status: "review", Title: "配置版本与恢复",
			Detail: detail,
		})
	}
	if strategy == "rolling" || strategy == "blue_green" {
		permissionCheck := deliveryPermissionCheck(rt, readCtx, app.Namespace, app.DeploymentName, cluster.ConnectionType, strategy, serviceAccountName, includeServiceAccountPullSecrets)
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, permissionCheck)
		if permissionCheck.Status == "blocked" || permissionCheck.Status == "unknown" {
			plan.Blockers = append(plan.Blockers, permissionCheck.Title+"："+permissionCheck.Detail)
		}
	}
	if strategy == "blue_green" {
		plan.Snapshot.CandidateRetentionHours = policySnapshot.CandidateRetentionHours
		if plan.Snapshot.CandidateRetentionHours == 0 {
			plan.Snapshot.CandidateRetentionHours = BlueGreenCandidateRetentionHours
		}
		plan.Snapshot.Target.Replicas = deploymentReplicas(obj)
		plan.Snapshot.Target.StableLabelKey = fmt.Sprintf("delivery.platform.io/app-%d", app.ID)
		plan.Snapshot.Target.CandidateLabelKey = "delivery.platform.io/run-id"
		plan.Snapshot.Target.CandidateLabelValue = candidateLabelValue(release.ID, string(obj.GetUID()), workloadHash)
		s.evaluateBlueGreenPreflight(readCtx, rt, app, obj, &plan)
	}
	if strategy == "gateway_canary" {
		plan.Blockers = append(plan.Blockers, "网关权重接口的读写、生效回读与指标联动尚未完成联调。")
		var gatewayRoute *CanaryGatewayRouteRef
		if plan.Snapshot.Policy.CanaryAnalysis != nil {
			gatewayRoute = plan.Snapshot.Policy.CanaryAnalysis.GatewayRoute
		}
		if gatewayRoute == nil || !validCanaryGatewayRouteRef(*gatewayRoute) {
			plan.Snapshot.Checks = append(plan.Snapshot.Checks, DeliveryPlanCheck{
				Key: "canary-gateway-route-identity", Status: "blocked", Title: "网关路由身份",
				Detail: "尚未完整固定有效的网关连接、路由及稳定/候选后端 ID；请先在组件策略草稿中登记。",
			})
			plan.Blockers = append(plan.Blockers, "金丝雀计划缺少固定的网关连接、路由和稳定/候选后端身份。")
		} else {
			plan.Snapshot.Checks = append(plan.Snapshot.Checks, DeliveryPlanCheck{
				Key: "canary-gateway-route-identity", Status: "unknown", Title: "网关路由身份",
				Detail: "策略已固定网关连接、路由及稳定/候选后端 ID；当前没有适配器验证归属、配置版本和有效路由，不能据此调整流量。",
			})
			plan.Blockers = append(plan.Blockers, "网关路由身份尚未由实际适配器验证。")
		}
		if stableDigestErr != nil || stableImageDigest == "" {
			plan.Blockers = append(plan.Blockers, "无法为当前稳定版固定不可变 sha256 digest；请检查镜像引用与 Registry 凭据。")
		}
		candidateDigest := ""
		if release.ImageDigest != nil {
			candidateDigest = NormalizeDigest(*release.ImageDigest)
		}
		if candidateDigest == "" {
			candidateDigest = imageReferenceDigest(deployImage(release))
		}
		if candidateDigest == "" {
			plan.Blockers = append(plan.Blockers, "候选制品没有不可变 sha256 digest，无法绑定金丝雀指标身份。")
		}
	}
	data, err := json.Marshal(plan.Snapshot)
	if err != nil {
		return DeliveryPlan{}, false, err
	}
	hash := sha256.Sum256(data)
	plan.SnapshotHash = hex.EncodeToString(hash[:])
	plan.CreatedAt = time.Now()
	return s.store.CreateDeliveryPlan(plan)
}

func (s *CDService) evaluateBlueGreenPreflight(ctx context.Context, rt KubernetesRuntime, app App, deployment *unstructured.Unstructured, plan *DeliveryPlan) {
	addCheck := func(check DeliveryPlanCheck) {
		plan.Snapshot.Checks = append(plan.Snapshot.Checks, check)
		if check.Status == "blocked" || check.Status == "unknown" {
			plan.Blockers = append(plan.Blockers, check.Title+"："+check.Detail)
		}
	}
	var namespaceServices *unstructured.UnstructuredList

	desired := deploymentReplicas(deployment)
	ready, readyFound, readyErr := unstructured.NestedInt64(deployment.Object, "status", "readyReplicas")
	switch {
	case desired < 1:
		addCheck(DeliveryPlanCheck{Key: "stable-health", Status: "blocked", Title: "稳定版本健康", Detail: "目标 Deployment 副本数为 0，不能作为切换前的恢复版本。"})
	case readyErr != nil || !readyFound:
		addCheck(DeliveryPlanCheck{Key: "stable-health", Status: "unknown", Title: "稳定版本健康", Detail: "未能读取 readyReplicas；请检查集群读取权限和工作负载状态。"})
	case ready < desired:
		addCheck(DeliveryPlanCheck{Key: "stable-health", Status: "blocked", Title: "稳定版本健康", Detail: fmt.Sprintf("期望 %d 个副本，当前仅 %d 个就绪。", desired, ready)})
	default:
		addCheck(DeliveryPlanCheck{Key: "stable-health", Status: "passed", Title: "稳定版本健康", Detail: fmt.Sprintf("%d/%d 个副本已就绪。", ready, desired)})
	}

	containers, containersFound, containersErr := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	switch {
	case containersErr != nil || !containersFound:
		addCheck(DeliveryPlanCheck{Key: "workload-shape", Status: "unknown", Title: "工作负载范围", Detail: "无法读取 Pod 容器定义，不能确认是否符合单容器发布范围。"})
	case len(containers) != 1:
		addCheck(DeliveryPlanCheck{Key: "workload-shape", Status: "blocked", Title: "工作负载范围", Detail: fmt.Sprintf("当前有 %d 个容器；首期蓝绿只支持单容器镜像变更。", len(containers))})
	default:
		addCheck(DeliveryPlanCheck{Key: "workload-shape", Status: "passed", Title: "工作负载范围", Detail: "当前 Deployment 符合单容器范围。"})
	}

	volumes, volumesFound, volumesErr := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "volumes")
	if volumesErr != nil {
		addCheck(DeliveryPlanCheck{Key: "storage", Status: "unknown", Title: "存储与配置兼容", Detail: "无法读取 Pod volume 定义。"})
	} else if volumesFound && hasExclusiveVolume(volumes) {
		addCheck(DeliveryPlanCheck{Key: "storage", Status: "blocked", Title: "存储与配置兼容", Detail: "检测到 PVC、hostPath 或其它独占/节点存储；首期蓝绿不支持此类工作负载。"})
	} else if volumesFound && len(volumes) > 0 {
		addCheck(DeliveryPlanCheck{Key: "storage", Status: "review", Title: "存储与配置兼容", Detail: "发现 ConfigMap/Secret 等配置卷；执行前仍需确认稳定版和候选版使用兼容且可追溯的配置版本。"})
	} else {
		addCheck(DeliveryPlanCheck{Key: "storage", Status: "passed", Title: "存储与配置兼容", Detail: "未发现持久化或节点绑定卷。"})
	}

	serviceDef, err := kube.LookupResource("", "v1", "services")
	if err != nil {
		addCheck(DeliveryPlanCheck{Key: "traffic-entry", Status: "unknown", Title: "流量入口唯一性", Detail: "平台无法识别 Service 资源类型。"})
	} else {
		var bound *unstructured.Unstructured
		if plan.Snapshot.Target.ServiceName == "" {
			addCheck(DeliveryPlanCheck{Key: "service-selector", Status: "blocked", Title: "Service 入口绑定", Detail: "尚未绑定明确的 Service 流量入口。"})
		} else {
			var boundErr error
			bound, boundErr = rt.GetResource(ctx, serviceDef, app.Namespace, plan.Snapshot.Target.ServiceName, metav1.GetOptions{})
			if boundErr != nil || bound == nil {
				addCheck(DeliveryPlanCheck{Key: "service-selector", Status: "unknown", Title: "Service 入口绑定", Detail: "无法读取绑定的 Service；请核对名称、namespace 与集群读取权限。"})
			} else {
				selector, selectorFound, selectorErr := unstructured.NestedStringMap(bound.Object, "spec", "selector")
				podLabels, podLabelsFound, podLabelsErr := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
				plan.Snapshot.Target.ServiceUID = string(bound.GetUID())
				plan.Snapshot.Target.ServiceResourceVersion = bound.GetResourceVersion()
				plan.Snapshot.Target.ServiceSelector = selector
				serviceConfigHash, serviceConfigErr := serviceConfigurationHash(bound)
				if serviceConfigErr == nil {
					plan.Snapshot.Target.ServiceConfigHash = serviceConfigHash
				}
				selectorJSON, _ := json.Marshal(selector)
				plan.Snapshot.Target.CandidateLabelValue = candidateLabelValue(
					plan.Snapshot.Artifact.ReleaseID,
					plan.Snapshot.Target.WorkloadUID,
					plan.Snapshot.Target.WorkloadHash+"\x00"+plan.Snapshot.Target.ServiceUID+"\x00"+plan.Snapshot.Target.ServiceResourceVersion+"\x00"+string(selectorJSON)+"\x00"+serviceConfigHash,
				)
				switch {
				case selectorErr != nil || podLabelsErr != nil || serviceConfigErr != nil || !selectorFound || !podLabelsFound:
					addCheck(DeliveryPlanCheck{Key: "service-selector", Status: "unknown", Title: "Service selector 匹配", Detail: "无法读取 Service selector 或 Deployment Pod 标签。"})
				case len(selector) == 0 || !labels.Set(selector).AsSelector().Matches(labels.Set(podLabels)):
					addCheck(DeliveryPlanCheck{Key: "service-selector", Status: "blocked", Title: "Service selector 匹配", Detail: "绑定的 Service selector 当前不匹配目标 Deployment 的 Pod 标签。"})
				default:
					addCheck(DeliveryPlanCheck{Key: "service-selector", Status: "passed", Title: "Service selector 匹配", Detail: "绑定的 Service 当前选择目标 Deployment 的 Pod。"})
				}
			}
		}

		services, listErr := rt.ListResources(ctx, serviceDef, app.Namespace, metav1.ListOptions{})
		namespaceServices = services
		if listErr != nil || services == nil {
			addCheck(DeliveryPlanCheck{Key: "traffic-entry", Status: "unknown", Title: "流量入口唯一性", Detail: "无法枚举 namespace 内所有 Service，不能排除其它入口同时选择当前 Pod。"})
		} else {
			podLabels, found, labelsErr := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
			if labelsErr != nil || !found {
				addCheck(DeliveryPlanCheck{Key: "traffic-entry", Status: "unknown", Title: "流量入口唯一性", Detail: "无法读取目标 Pod 标签并核对所有 Service。"})
			} else {
				matching := make([]string, 0, 1)
				selectorUnknown := false
				for i := range services.Items {
					service := &services.Items[i]
					selector, found, selectorErr := unstructured.NestedStringMap(service.Object, "spec", "selector")
					if selectorErr != nil {
						selectorUnknown = true
						continue
					}
					if found && len(selector) > 0 && labels.Set(selector).AsSelector().Matches(labels.Set(podLabels)) {
						matching = append(matching, service.GetName())
					}
				}
				sort.Strings(matching)
				if selectorUnknown {
					addCheck(DeliveryPlanCheck{Key: "traffic-entry", Status: "unknown", Title: "流量入口唯一性", Detail: "至少一个 Service selector 格式无法识别，不能排除其它入口同时选择当前 Pod。"})
				} else if len(matching) != 1 || matching[0] != plan.Snapshot.Target.ServiceName {
					addCheck(DeliveryPlanCheck{Key: "traffic-entry", Status: "blocked", Title: "流量入口唯一性", Detail: fmt.Sprintf("当前有 %d 个 Service 选择稳定版 Pod（%s）；首期只支持一个明确入口。", len(matching), strings.Join(matching, "、"))})
				} else {
					addCheck(DeliveryPlanCheck{Key: "traffic-entry", Status: "passed", Title: "流量入口唯一性", Detail: "namespace 内只有绑定的 Service 选择当前 Pod。"})
				}
			}
		}
		if target := plan.Snapshot.Target; target.ServiceName != "" {
			fresh, freshErr := rt.GetResource(ctx, serviceDef, app.Namespace, target.ServiceName, metav1.GetOptions{})
			if freshErr != nil || fresh == nil {
				addCheck(DeliveryPlanCheck{Key: "service-snapshot", Status: "unknown", Title: "Service 快照一致性", Detail: "复核 Service 时读取失败，不能确认检查期间入口没有变化。"})
			} else {
				freshSelector, freshFound, freshSelectorErr := unstructured.NestedStringMap(fresh.Object, "spec", "selector")
				freshConfigHash, freshConfigErr := serviceConfigurationHash(fresh)
				if target.ServiceUID == "" || target.ServiceResourceVersion == "" || target.ServiceConfigHash == "" || !freshFound || freshSelectorErr != nil || freshConfigErr != nil {
					addCheck(DeliveryPlanCheck{Key: "service-snapshot", Status: "unknown", Title: "Service 快照一致性", Detail: "Service UID、resourceVersion 或 selector 不完整，无法固定入口基线。"})
				} else if target.ServiceUID != string(fresh.GetUID()) || target.ServiceResourceVersion != fresh.GetResourceVersion() || !sameStringMap(target.ServiceSelector, freshSelector) || target.ServiceConfigHash != freshConfigHash {
					addCheck(DeliveryPlanCheck{Key: "service-snapshot", Status: "blocked", Title: "Service 快照一致性", Detail: "检查期间 Service 已发生变化，请重新生成计划后再核对。"})
				} else {
					addCheck(DeliveryPlanCheck{Key: "service-snapshot", Status: "passed", Title: "Service 快照一致性", Detail: "Service UID、resourceVersion 与 selector 在检查期间保持一致。"})
				}
			}
		}
	}

	hpaDef, err := kube.LookupResource("autoscaling", "v1", "horizontalpodautoscalers")
	if err != nil {
		addCheck(DeliveryPlanCheck{Key: "autoscaling", Status: "unknown", Title: "自动扩缩容", Detail: "平台无法识别 HPA 资源类型。"})
	} else if hpas, listErr := rt.ListResources(ctx, hpaDef, app.Namespace, metav1.ListOptions{}); listErr != nil || hpas == nil {
		addCheck(DeliveryPlanCheck{Key: "autoscaling", Status: "unknown", Title: "自动扩缩容", Detail: "无法读取 namespace 内 HPA；不能确认副本数是否会在发布期间变化。"})
	} else {
		matches := make([]string, 0, 1)
		for i := range hpas.Items {
			hpa := &hpas.Items[i]
			kind, _, _ := unstructured.NestedString(hpa.Object, "spec", "scaleTargetRef", "kind")
			name, _, _ := unstructured.NestedString(hpa.Object, "spec", "scaleTargetRef", "name")
			if kind == "Deployment" && name == app.DeploymentName {
				matches = append(matches, hpa.GetName())
			}
		}
		if len(matches) > 0 {
			sort.Strings(matches)
			addCheck(DeliveryPlanCheck{Key: "autoscaling", Status: "blocked", Title: "自动扩缩容", Detail: "目标由 HPA 管理（" + strings.Join(matches, "、") + "）；首期尚无双版本扩缩容与容量协调方案。"})
		} else {
			addCheck(DeliveryPlanCheck{Key: "autoscaling", Status: "passed", Title: "自动扩缩容", Detail: "未发现指向目标 Deployment 的 HPA。"})
		}
	}

	quotaDef, quotaDefErr := kube.LookupResource("", "v1", "resourcequotas")
	quotas, quotaErr := (*unstructured.UnstructuredList)(nil), quotaDefErr
	if quotaErr == nil {
		quotas, quotaErr = rt.ListResources(ctx, quotaDef, app.Namespace, metav1.ListOptions{})
	}
	if quotaErr != nil || quotas == nil {
		addCheck(DeliveryPlanCheck{Key: "capacity", Status: "unknown", Title: "双版本容量", Detail: "无法读取 namespace ResourceQuota；请检查集群权限和连接后重新生成计划。"})
	} else {
		status, detail := blueGreenQuotaCheck(deployment, quotas, desired)
		addCheck(DeliveryPlanCheck{Key: "capacity", Status: status, Title: "双版本容量", Detail: detail})
	}
	addCheck(DeliveryPlanCheck{Key: "external-routes", Status: "review", Title: "外部路由与策略", Detail: "预检覆盖 namespace 内 Service；Ingress、网关或集群外直连路径仍需在正式接管计划中核对。"})
	evaluateBlueGreenCandidateIsolation(ctx, rt, app, deployment, namespaceServices, plan, addCheck)

	deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		addCheck(DeliveryPlanCheck{Key: "baseline-consistency", Status: "unknown", Title: "Deployment 快照一致性", Detail: "平台无法识别 Deployment 资源类型。"})
		return
	}
	fresh, freshErr := rt.GetResource(ctx, deploymentDef, app.Namespace, app.DeploymentName, metav1.GetOptions{})
	if freshErr != nil || fresh == nil {
		addCheck(DeliveryPlanCheck{Key: "baseline-consistency", Status: "unknown", Title: "Deployment 快照一致性", Detail: "复核 Deployment 时读取失败，不能确认工作负载基线保持不变。"})
		return
	}
	freshHash, hashErr := workloadHash(fresh.DeepCopy(), app.ContainerName, s.cfg.MockRuntime)
	if hashErr != nil {
		addCheck(DeliveryPlanCheck{Key: "baseline-consistency", Status: "unknown", Title: "Deployment 快照一致性", Detail: "复核 Deployment 配置摘要失败。"})
		return
	}
	if string(fresh.GetUID()) != plan.Snapshot.Target.WorkloadUID || targetContainerImage(fresh, app.ContainerName) != plan.Snapshot.Target.CurrentImage || freshHash != plan.Snapshot.Target.WorkloadHash {
		addCheck(DeliveryPlanCheck{Key: "baseline-consistency", Status: "blocked", Title: "Deployment 快照一致性", Detail: "检查期间 Deployment 身份、镜像或配置已变化，请重新生成计划。"})
		return
	}
	addCheck(DeliveryPlanCheck{Key: "baseline-consistency", Status: "passed", Title: "Deployment 快照一致性", Detail: "Deployment UID、镜像和工作负载配置在检查期间保持一致。"})
}

type controllerSelector struct {
	name   string
	labels map[string]string
}

func evaluateBlueGreenCandidateIsolation(
	ctx context.Context,
	rt KubernetesRuntime,
	app App,
	deployment *unstructured.Unstructured,
	services *unstructured.UnstructuredList,
	plan *DeliveryPlan,
	addCheck func(DeliveryPlanCheck),
) {
	if services == nil {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: "无法枚举 Service selector，不能计算不会提前接流量的候选标签。"})
		return
	}
	deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: "平台无法识别 Deployment 资源类型。"})
		return
	}
	replicasetDef, err := kube.LookupResource("apps", "v1", "replicasets")
	if err != nil {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: "平台无法识别 ReplicaSet 资源类型。"})
		return
	}
	deployments, err := rt.ListResources(ctx, deploymentDef, app.Namespace, metav1.ListOptions{})
	if err != nil || deployments == nil {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: "无法读取 namespace 内全部 Deployment selector。"})
		return
	}
	replicasets, err := rt.ListResources(ctx, replicasetDef, app.Namespace, metav1.ListOptions{})
	if err != nil || replicasets == nil {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: "无法读取 namespace 内全部 ReplicaSet selector。"})
		return
	}
	selectors := make([]controllerSelector, 0, len(deployments.Items)+len(replicasets.Items))
	selectorKeys := map[string]struct{}{}
	targetFound := false
	for i := range deployments.Items {
		item := &deployments.Items[i]
		if item.GetName() == app.DeploymentName {
			targetFound = true
			if string(item.GetUID()) != string(deployment.GetUID()) {
				addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "blocked", Title: "候选标签隔离", Detail: "枚举 Deployment 时目标 UID 已变化，请重新生成计划。"})
				return
			}
		}
		selector, keySet, ok := parseControllerSelector(item.Object)
		if !ok {
			addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "blocked", Title: "候选标签隔离", Detail: fmt.Sprintf("Deployment %s 的 selector 为空、格式不支持或含 matchExpressions。", item.GetName())})
			return
		}
		selectors = append(selectors, controllerSelector{name: "Deployment/" + item.GetName(), labels: selector})
		for key := range keySet {
			selectorKeys[key] = struct{}{}
		}
	}
	if !targetFound {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: "namespace Deployment 列表没有包含绑定的目标资源。"})
		return
	}
	for i := range replicasets.Items {
		item := &replicasets.Items[i]
		selector, keySet, ok := parseControllerSelector(item.Object)
		if !ok {
			addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "blocked", Title: "候选标签隔离", Detail: fmt.Sprintf("ReplicaSet %s 的 selector 为空、格式不支持或含 matchExpressions。", item.GetName())})
			return
		}
		selectors = append(selectors, controllerSelector{name: "ReplicaSet/" + item.GetName(), labels: selector})
		for key := range keySet {
			selectorKeys[key] = struct{}{}
		}
	}
	for i := range services.Items {
		selector, found, selectorErr := unstructured.NestedStringMap(services.Items[i].Object, "spec", "selector")
		if selectorErr != nil {
			addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: fmt.Sprintf("Service %s 的 selector 格式无法读取。", services.Items[i].GetName())})
			return
		}
		if found {
			for key := range selector {
				selectorKeys[key] = struct{}{}
			}
		}
	}
	stableKey := plan.Snapshot.Target.StableLabelKey
	candidateKey := plan.Snapshot.Target.CandidateLabelKey
	if stableKey == "" || candidateKey == "" {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: "稳定/候选标签键未能生成。"})
		return
	}
	selectorKeys[stableKey] = struct{}{}
	selectorKeys[candidateKey] = struct{}{}
	podLabels, found, labelsErr := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
	if labelsErr != nil || !found {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: "无法读取稳定 Pod 标签以计算隔离后的候选标签。"})
		return
	}
	if value, collision := podLabels[stableKey]; collision && value != "stable" {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "blocked", Title: "候选标签隔离", Detail: "稳定版本标签键已被业务模板占用，不能安全接管。"})
		return
	}
	plan.Snapshot.Target.StableLabelAlreadyPresent = podLabels[stableKey] == "stable"
	if plan.Snapshot.Target.CandidateLabelValue == "" {
		addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "unknown", Title: "候选标签隔离", Detail: "候选标签值未能稳定生成。"})
		return
	}
	candidateLabels := make(map[string]string, len(podLabels)+1)
	for key, value := range podLabels {
		if _, remove := selectorKeys[key]; !remove {
			candidateLabels[key] = value
		}
	}
	candidateLabels[stableKey] = "stable"
	candidateLabels[candidateKey] = plan.Snapshot.Target.CandidateLabelValue
	for _, selector := range selectors {
		if labels.Set(selector.labels).AsSelector().Matches(labels.Set(candidateLabels)) {
			addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "blocked", Title: "候选标签隔离", Detail: fmt.Sprintf("计算出的候选 Pod 仍匹配 %s，无法保证控制器互不接管。", selector.name)})
			return
		}
	}
	for i := range services.Items {
		selector, found, _ := unstructured.NestedStringMap(services.Items[i].Object, "spec", "selector")
		if found && len(selector) > 0 && labels.Set(selector).AsSelector().Matches(labels.Set(candidateLabels)) {
			addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "blocked", Title: "候选标签隔离", Detail: fmt.Sprintf("计算出的候选 Pod 会被现有 Service %s 选择，可能提前接收流量。", services.Items[i].GetName())})
			return
		}
	}
	removed := make([]string, 0, len(selectorKeys))
	for key := range selectorKeys {
		if _, exists := podLabels[key]; exists {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)
	plan.Snapshot.Target.CandidateRemovedLabels = removed
	addCheck(DeliveryPlanCheck{Key: "candidate-isolation", Status: "passed", Title: "候选标签隔离", Detail: fmt.Sprintf("已计算与 namespace 内 %d 个 Deployment/ReplicaSet selector、Service selector 不相交的候选标签。", len(selectors))})
	addCheck(DeliveryPlanCheck{Key: "candidate-label-compatibility", Status: "review", Title: "候选标签兼容性", Detail: fmt.Sprintf("候选 Pod 将移除 %d 个控制器或 Service 使用的标签，需确认网络策略、监控和应用逻辑不依赖这些标签。", len(removed))})
}

func parseControllerSelector(object map[string]any) (map[string]string, map[string]struct{}, bool) {
	matchLabels, found, err := unstructured.NestedStringMap(object, "spec", "selector", "matchLabels")
	if err != nil || !found || len(matchLabels) == 0 {
		return nil, nil, false
	}
	expressions, expressionsFound, expressionsErr := unstructured.NestedSlice(object, "spec", "selector", "matchExpressions")
	if expressionsErr != nil || expressionsFound && len(expressions) > 0 {
		return nil, nil, false
	}
	keys := make(map[string]struct{}, len(matchLabels))
	for key := range matchLabels {
		keys[key] = struct{}{}
	}
	return matchLabels, keys, true
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func candidateLabelValue(releaseID int64, workloadUID, baselineHash string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", releaseID, workloadUID, baselineHash)))
	return "r-" + hex.EncodeToString(sum[:6])
}

// serviceConfigurationHash pins the parts of a Service copied into a blue-
// green preview Service. The selector is excluded because adding the stable
// track marker is an intentional takeover mutation; allocated cluster fields
// are excluded because Kubernetes assigns them and the preview Service must
// receive fresh values.
func serviceConfigurationHash(obj *unstructured.Unstructured) (string, error) {
	if obj == nil {
		return "", NewValidationError("蓝绿 Service 快照为空")
	}
	spec, found, err := unstructured.NestedMap(obj.Object, "spec")
	if err != nil || !found {
		return "", NewValidationError("蓝绿 Service spec 无法读取")
	}
	delete(spec, "selector")
	for _, key := range []string{
		"clusterIP", "clusterIPs", "externalIPs", "externalName", "healthCheckNodePort",
		"ipFamilies", "ipFamilyPolicy", "loadBalancerClass", "loadBalancerIP",
		"loadBalancerSourceRanges", "allocateLoadBalancerNodePorts", "externalTrafficPolicy",
	} {
		delete(spec, key)
	}
	metadata := map[string]any{}
	serviceMetadata, _, metadataErr := unstructured.NestedMap(obj.Object, "metadata")
	if metadataErr != nil {
		return "", NewValidationError("蓝绿 Service metadata 无法读取")
	}
	for _, key := range []string{"labels", "annotations"} {
		if value, exists := serviceMetadata[key]; exists {
			metadata[key] = value
		}
	}
	canonical := map[string]any{"spec": spec, "metadata": metadata}
	b, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("序列化 Service 配置摘要失败: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func deploymentReplicas(obj *unstructured.Unstructured) int64 {
	replicas, found, err := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if err != nil || !found {
		return 1 // Kubernetes defaults an omitted Deployment replicas field to one.
	}
	return replicas
}

func hasExclusiveVolume(volumes []any) bool {
	ephemeralSources := map[string]struct{}{
		"configMap": {}, "secret": {}, "downwardAPI": {}, "projected": {}, "emptyDir": {},
	}
	for _, item := range volumes {
		volume, ok := item.(map[string]any)
		if !ok {
			return true // malformed input is unsafe to classify as ephemeral.
		}
		foundSource := false
		for source := range volume {
			if source == "name" {
				continue
			}
			foundSource = true
			if _, allowed := ephemeralSources[source]; !allowed {
				return true
			}
		}
		if !foundSource {
			return true
		}
	}
	return false
}

func targetContainerImage(obj *unstructured.Unstructured, name string) string {
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if ok && container["name"] == name {
			image, _ := container["image"].(string)
			return image
		}
	}
	return ""
}
