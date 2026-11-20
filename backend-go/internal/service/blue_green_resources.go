package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"deploy-platform/backend-go/internal/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

const (
	blueGreenManagedLabel = "delivery.platform.io/managed-by"
	blueGreenPlanLabel    = "delivery.platform.io/plan-id"
	blueGreenRunLabel     = "delivery.platform.io/execution-id"
)

type BlueGreenCandidateResources struct {
	Deployment               *unstructured.Unstructured
	Service                  *unstructured.Unstructured
	StableServiceSelector    map[string]string
	CandidateServiceSelector map[string]string
}

// BlueGreenPlanLocks reserves every stable and candidate entry that one
// immutable plan can mutate. Store approval adds the stable Deployment lock
// from the deploy log in the same transaction.
func BlueGreenPlanLocks(plan DeliveryPlan) ([]ResourceLock, error) {
	if plan.ID <= 0 || plan.Strategy != "blue_green" {
		return nil, NewValidationError("蓝绿发布计划身份无效")
	}
	target := plan.Snapshot.Target
	if target.ClusterID <= 0 || target.Namespace == "" || target.Deployment == "" || target.ServiceName == "" {
		return nil, NewValidationError("蓝绿发布计划缺少资源锁目标")
	}
	return NormalizeResourceLocks([]ResourceLock{
		{ClusterID: target.ClusterID, Namespace: target.Namespace, Kind: "service", Name: target.ServiceName},
		{ClusterID: target.ClusterID, Namespace: target.Namespace, Kind: "deployment", Name: blueGreenResourceName(target.Deployment, plan.ID)},
		{ClusterID: target.ClusterID, Namespace: target.Namespace, Kind: "service", Name: blueGreenResourceName(target.ServiceName, plan.ID)},
	})
}

// BuildBlueGreenCandidateResources creates deterministic candidate objects
// from the live, already-stabilized Deployment and Service. It never writes
// to Kubernetes and never returns the objects through an API response.
func BuildBlueGreenCandidateResources(plan DeliveryPlan, executionID int64, deployment, service *unstructured.Unstructured) (BlueGreenCandidateResources, error) {
	if plan.Strategy != "blue_green" || plan.ID <= 0 || executionID <= 0 {
		return BlueGreenCandidateResources{}, NewValidationError("蓝绿计划或执行 ID 无效")
	}
	target := plan.Snapshot.Target
	if target.Namespace == "" || target.Deployment == "" || target.ServiceName == "" || target.Container == "" || target.StableLabelKey == "" || target.CandidateLabelKey == "" || target.CandidateLabelValue == "" || target.ServiceConfigHash == "" {
		return BlueGreenCandidateResources{}, NewValidationError("蓝绿计划缺少 namespace、资源或隔离标签基线")
	}
	if deployment == nil || deployment.GetNamespace() != target.Namespace || deployment.GetName() != target.Deployment || string(deployment.GetUID()) != target.WorkloadUID {
		return BlueGreenCandidateResources{}, newConflict("稳定 Deployment 身份已变化，不能构造候选资源")
	}
	if service == nil || service.GetNamespace() != target.Namespace || service.GetName() != target.ServiceName || string(service.GetUID()) != target.ServiceUID {
		return BlueGreenCandidateResources{}, newConflict("蓝绿 Service 身份已变化，不能构造候选资源")
	}
	serviceConfigHash, err := serviceConfigurationHash(service)
	if err != nil || serviceConfigHash != target.ServiceConfigHash {
		return BlueGreenCandidateResources{}, newConflict("蓝绿 Service 端口或配置已漂移，不能构造候选资源")
	}
	if targetContainerImage(deployment, target.Container) != target.CurrentImage {
		return BlueGreenCandidateResources{}, newConflict("稳定 Deployment 镜像已变化，不能构造候选资源")
	}
	currentHash, err := blueGreenWorkloadHash(deployment, target)
	if err != nil || currentHash != target.WorkloadHash {
		return BlueGreenCandidateResources{}, newConflict("稳定 Deployment 配置已漂移，不能构造候选资源")
	}

	stableSelector := make(map[string]string, len(target.ServiceSelector)+1)
	for key, value := range target.ServiceSelector {
		stableSelector[key] = value
	}
	if current, exists := stableSelector[target.StableLabelKey]; exists && current != "stable" {
		return BlueGreenCandidateResources{}, NewValidationError("稳定版本标签与 Service selector 冲突")
	}
	stableSelector[target.StableLabelKey] = "stable"
	currentSelector, found, err := unstructured.NestedStringMap(service.Object, "spec", "selector")
	if err != nil || !found || !sameStringMap(currentSelector, stableSelector) {
		return BlueGreenCandidateResources{}, newConflict("Service 尚未条件切换到稳定版本标签")
	}

	candidateDeployment, err := buildCandidateDeployment(plan, executionID, deployment)
	if err != nil {
		return BlueGreenCandidateResources{}, err
	}
	candidateService, err := buildCandidateService(plan, executionID, service)
	if err != nil {
		return BlueGreenCandidateResources{}, err
	}
	candidateSelector := map[string]string{target.CandidateLabelKey: target.CandidateLabelValue}
	return BlueGreenCandidateResources{
		Deployment: candidateDeployment, Service: candidateService,
		StableServiceSelector: stableSelector, CandidateServiceSelector: candidateSelector,
	}, nil
}

func buildCandidateDeployment(plan DeliveryPlan, executionID int64, source *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	target := plan.Snapshot.Target
	candidate := source.DeepCopy()
	spec, found, err := unstructured.NestedMap(candidate.Object, "spec")
	if err != nil || !found {
		return nil, NewValidationError("目标 Deployment 缺少 spec")
	}
	template, found, err := unstructured.NestedMap(spec, "template")
	if err != nil || !found {
		return nil, NewValidationError("目标 Deployment 缺少 Pod 模板")
	}
	templateMetadata, found, err := unstructured.NestedMap(template, "metadata")
	if err != nil || !found {
		return nil, NewValidationError("目标 Deployment 缺少 Pod 模板标签")
	}
	templateLabels, found, err := unstructured.NestedStringMap(template, "metadata", "labels")
	if err != nil || !found {
		return nil, NewValidationError("目标 Deployment 缺少 Pod 模板标签")
	}
	for _, key := range target.CandidateRemovedLabels {
		delete(templateLabels, key)
	}
	// Mark the candidate as the future stable track before it is exposed.
	// Its original Service selector labels are removed below, and a unique run
	// label keeps it isolated until cutover.
	templateLabels[target.StableLabelKey] = "stable"
	delete(templateLabels, target.CandidateLabelKey)
	templateLabels[target.CandidateLabelKey] = target.CandidateLabelValue
	templateMetadata["labels"] = toAnyStringMap(templateLabels)
	stripServerMetadata(templateMetadata)
	template["metadata"] = templateMetadata
	spec["template"] = template
	spec["selector"] = map[string]any{"matchLabels": map[string]any{target.CandidateLabelKey: target.CandidateLabelValue}}
	if err := unstructured.SetNestedMap(candidate.Object, spec, "spec"); err != nil {
		return nil, err
	}
	containers, found, err := unstructured.NestedSlice(candidate.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return nil, NewValidationError("候选 Deployment 缺少容器定义")
	}
	matched := false
	for i, value := range containers {
		container, ok := value.(map[string]any)
		if !ok {
			return nil, NewValidationError("候选 Deployment 容器定义无效")
		}
		if container["name"] == target.Container {
			container["image"] = plan.Snapshot.Artifact.Image
			containers[i] = container
			matched = true
		}
	}
	if !matched {
		return nil, NewValidationError("候选 Deployment 找不到绑定容器")
	}
	if err := unstructured.SetNestedSlice(candidate.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		return nil, err
	}
	// Candidate resource names are derived from the immutable plan so the
	// approval transaction can reserve those exact Kubernetes resources before
	// the database allocates the execution/run ID. Ownership labels still carry
	// the run ID, so an old or foreign candidate cannot be adopted on retry.
	name := blueGreenResourceName(target.Deployment, plan.ID)
	metadata := managedCandidateMetadata(plan.ID, executionID, name, target.Namespace)
	candidate.Object["metadata"] = metadata
	delete(candidate.Object, "status")
	return candidate, nil
}

func buildCandidateService(plan DeliveryPlan, executionID int64, source *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	target := plan.Snapshot.Target
	candidate := source.DeepCopy()
	spec, found, err := unstructured.NestedMap(candidate.Object, "spec")
	if err != nil || !found {
		return nil, NewValidationError("目标 Service 缺少 spec")
	}
	candidateSelector := map[string]any{target.CandidateLabelKey: target.CandidateLabelValue}
	spec["selector"] = candidateSelector
	spec["type"] = "ClusterIP"
	if err := stripBlueGreenAllocatedServiceFields(spec); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedMap(candidate.Object, spec, "spec"); err != nil {
		return nil, err
	}
	name := blueGreenResourceName(target.ServiceName, plan.ID)
	candidate.Object["metadata"] = managedCandidateMetadata(plan.ID, executionID, name, target.Namespace)
	delete(candidate.Object, "status")
	return candidate, nil
}

// stripBlueGreenAllocatedServiceFields removes values allocated by the API
// server when a preview Service is created. The same canonicalization is used
// during readback hashing so a valid Service is not mistaken for drift merely
// because Kubernetes assigned its ClusterIP or defaulted IP-family fields.
func stripBlueGreenAllocatedServiceFields(spec map[string]any) error {
	for _, key := range []string{
		"clusterIP", "clusterIPs", "externalIPs", "externalName", "healthCheckNodePort",
		"ipFamilies", "ipFamilyPolicy", "loadBalancerClass", "loadBalancerIP",
		"loadBalancerSourceRanges", "allocateLoadBalancerNodePorts", "externalTrafficPolicy",
	} {
		delete(spec, key)
	}
	if spec["sessionAffinity"] == "None" {
		delete(spec, "sessionAffinity")
	}
	if spec["internalTrafficPolicy"] == "Cluster" {
		delete(spec, "internalTrafficPolicy")
	}
	portsRaw, found := spec["ports"]
	if !found {
		return nil
	}
	ports, ok := portsRaw.([]any)
	if !ok {
		return NewValidationError("Service 端口定义无效")
	}
	for i, value := range ports {
		port, ok := value.(map[string]any)
		if !ok {
			return NewValidationError("Service 端口定义无效")
		}
		delete(port, "nodePort")
		ports[i] = port
	}
	spec["ports"] = ports
	return nil
}

func managedCandidateMetadata(planID, executionID int64, name, namespace string) map[string]any {
	return map[string]any{
		"name": name, "namespace": namespace,
		"labels": map[string]any{
			blueGreenManagedLabel: "deploy-platform",
			blueGreenPlanLabel:    strconv.FormatInt(planID, 10),
			blueGreenRunLabel:     strconv.FormatInt(executionID, 10),
		},
	}
}

func stripServerMetadata(metadata map[string]any) {
	for _, key := range []string{
		"uid", "resourceVersion", "generation", "creationTimestamp", "deletionTimestamp",
		"deletionGracePeriodSeconds", "managedFields", "selfLink", "ownerReferences", "finalizers",
	} {
		delete(metadata, key)
	}
}

func toAnyStringMap(source map[string]string) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func blueGreenResourceName(base string, executionID int64) string {
	suffix := "-bg-" + strconv.FormatInt(executionID, 10)
	maxBase := 253 - len(suffix)
	if len(base) > maxBase {
		base = strings.TrimRight(base[:maxBase], "-.")
	}
	if base == "" {
		base = "delivery"
	}
	return base + suffix
}

// BlueGreenStableLabelPatch adds the stable track marker with a resourceVersion
// test. An already-completed marker yields an empty patch so recovery can be
// read-only and idempotent.
func BlueGreenStableLabelPatch(deployment *unstructured.Unstructured, labelKey string) ([]byte, error) {
	if deployment == nil || deployment.GetResourceVersion() == "" || labelKey == "" {
		return nil, NewValidationError("稳定版本标签 patch 基线不完整")
	}
	labels, found, err := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
	if err != nil || !found {
		return nil, NewValidationError("稳定 Deployment 缺少 Pod 标签")
	}
	if current, exists := labels[labelKey]; exists {
		if current == "stable" {
			return []byte("[]"), nil
		}
		return nil, NewValidationError("稳定标签键已被占用")
	}
	ops := []map[string]any{{"op": "test", "path": "/metadata/resourceVersion", "value": deployment.GetResourceVersion()}}
	path := "/spec/template/metadata/labels/" + jsonPointerEscape(labelKey)
	ops = append(ops, map[string]any{"op": "add", "path": path, "value": "stable"})
	return json.Marshal(ops)
}

// BlueGreenServiceSelectorPatch atomically tests the service revision and
// current selector before replacing it with a full desired selector map.
func BlueGreenServiceSelectorPatch(service *unstructured.Unstructured, expected, desired map[string]string) ([]byte, error) {
	if service == nil || service.GetResourceVersion() == "" || len(expected) == 0 || len(desired) == 0 {
		return nil, NewValidationError("Service selector patch 基线不完整")
	}
	current, found, err := unstructured.NestedStringMap(service.Object, "spec", "selector")
	if err != nil || !found {
		return nil, NewValidationError("Service 缺少 selector")
	}
	if sameStringMap(current, desired) {
		return []byte("[]"), nil
	}
	if !sameStringMap(current, expected) {
		return nil, newConflict("Service selector 已变化，停止覆盖")
	}
	ops := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": service.GetResourceVersion()},
		{"op": "test", "path": "/spec/selector", "value": toAnyStringMap(expected)},
		{"op": "replace", "path": "/spec/selector", "value": toAnyStringMap(desired)},
	}
	return json.Marshal(ops)
}

func jsonPointerEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func BlueGreenStableServiceSelector(original map[string]string, stableLabelKey string) (map[string]string, error) {
	if len(original) == 0 || stableLabelKey == "" {
		return nil, fmt.Errorf("%w: 蓝绿 Service selector 不能为空", ErrValidation)
	}
	selector := make(map[string]string, len(original)+1)
	for key, value := range original {
		selector[key] = value
	}
	if value, exists := selector[stableLabelKey]; exists && value != "stable" {
		return nil, NewValidationError("稳定标签与 Service selector 冲突")
	}
	selector[stableLabelKey] = "stable"
	return selector, nil
}

func BlueGreenCandidateSelector(key, value string) (map[string]string, error) {
	if key == "" || value == "" {
		return nil, NewValidationError("候选 Service selector 不完整")
	}
	return map[string]string{key: value}, nil
}

// ValidateBlueGreenCandidateIsolation rechecks all currently visible
// Deployment, ReplicaSet and Service selectors immediately before candidate
// creation. It is deliberately run again after approval; the preview is not a
// lock on cluster configuration.
func ValidateBlueGreenCandidateIsolation(ctx context.Context, rt KubernetesRuntime, namespace string, candidateLabels map[string]string) error {
	return validateBlueGreenCandidateIsolation(ctx, rt, namespace, candidateLabels, nil, 0, nil)
}

// validateBlueGreenCandidateIsolation permits only already journaled candidate
// objects from this exact plan and execution to match the candidate selector.
// Their ReplicaSets are skipped only when Kubernetes ownerReferences point to
// the verified candidate Deployment UID. This makes an interrupted preparation
// resumable without weakening the namespace-wide selector check.
func validateBlueGreenCandidateIsolation(ctx context.Context, rt KubernetesRuntime, namespace string, candidateLabels map[string]string, store *Store, logID int64, desired *BlueGreenCandidateResources) error {
	if rt == nil || strings.TrimSpace(namespace) == "" || len(candidateLabels) == 0 {
		return NewValidationError("候选标签隔离复核参数不完整")
	}
	var allowedDeploymentUID types.UID
	var allowedServiceUID types.UID
	if desired != nil {
		if store == nil || logID <= 0 || desired.Deployment == nil || desired.Service == nil {
			return NewValidationError("候选恢复隔离参数不完整")
		}
		deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
		if err != nil {
			return err
		}
		current, err := rt.GetResource(ctx, deploymentDef, namespace, desired.Deployment.GetName(), metav1.GetOptions{})
		if err == nil {
			if err := validateJournaledCandidateForIsolation(store, logID, blueGreenCandidateDeploy, "Deployment", current, desired.Deployment, desired.Deployment.GetLabels()[blueGreenPlanLabel], desired.Deployment.GetLabels()[blueGreenRunLabel]); err != nil {
				return err
			}
			allowedDeploymentUID = current.GetUID()
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("读取既有候选 Deployment 失败: %w", err)
		}
		serviceDef, err := kube.LookupResource("", "v1", "services")
		if err != nil {
			return err
		}
		current, err = rt.GetResource(ctx, serviceDef, namespace, desired.Service.GetName(), metav1.GetOptions{})
		if err == nil {
			if err := validateJournaledCandidateForIsolation(store, logID, blueGreenCandidateService, "Service", current, desired.Service, desired.Service.GetLabels()[blueGreenPlanLabel], desired.Service.GetLabels()[blueGreenRunLabel]); err != nil {
				return err
			}
			allowedServiceUID = current.GetUID()
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("读取既有候选 Service 失败: %w", err)
		}
	}
	for _, resource := range []struct{ group, name, kind string }{
		{group: "apps", name: "deployments", kind: "Deployment"},
		{group: "apps", name: "replicasets", kind: "ReplicaSet"},
	} {
		def, err := kube.LookupResource(resource.group, "v1", resource.name)
		if err != nil {
			return err
		}
		objects, err := rt.ListResources(ctx, def, namespace, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("读取 namespace 内 %s selector 失败: %w", resource.kind, err)
		}
		if objects == nil {
			return fmt.Errorf("读取 namespace 内 %s selector 失败: 空响应", resource.kind)
		}
		for i := range objects.Items {
			object := &objects.Items[i]
			if resource.kind == "Deployment" && allowedDeploymentUID != "" && object.GetUID() == allowedDeploymentUID {
				continue
			}
			selector, _, ok := parseControllerSelector(object.Object)
			if !ok {
				return NewValidationError("%s %s 的 selector 为空或使用了当前不支持的 matchExpressions", resource.kind, object.GetName())
			}
			if labels.Set(selector).AsSelector().Matches(labels.Set(candidateLabels)) {
				if resource.kind == "ReplicaSet" && allowedDeploymentUID != "" && replicaSetOwnedBy(object, allowedDeploymentUID, desired.Deployment.GetName()) {
					continue
				}
				return newConflict("候选 Pod 将匹配现有 %s %s，停止创建", resource.kind, object.GetName())
			}
		}
	}
	serviceDef, err := kube.LookupResource("", "v1", "services")
	if err != nil {
		return err
	}
	services, err := rt.ListResources(ctx, serviceDef, namespace, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("枚举 namespace 内 Service selector 失败: %w", err)
	}
	if services == nil {
		return fmt.Errorf("枚举 namespace 内 Service selector 失败: 空响应")
	}
	for i := range services.Items {
		service := &services.Items[i]
		if allowedServiceUID != "" && service.GetUID() == allowedServiceUID {
			continue
		}
		selector, found, selectorErr := unstructured.NestedStringMap(service.Object, "spec", "selector")
		if selectorErr != nil {
			return NewValidationError("Service %s selector 格式无法识别", service.GetName())
		}
		if found && len(selector) > 0 && labels.Set(selector).AsSelector().Matches(labels.Set(candidateLabels)) {
			return newConflict("候选 Pod 将被现有 Service %s 选择，停止创建", service.GetName())
		}
	}
	return nil
}

func validateJournaledCandidateForIsolation(store *Store, logID int64, actionKey, kind string, current, desired *unstructured.Unstructured, planIDText, executionIDText string) error {
	if current == nil || desired == nil || current.GetName() != desired.GetName() || current.GetNamespace() != desired.GetNamespace() || current.GetUID() == "" {
		return newConflict("候选 %s 身份不完整，拒绝忽略其 selector", kind)
	}
	prior, err := findExecutionAction(store, logID, actionKey)
	if err != nil {
		return err
	}
	if prior == nil || prior.Status == ExecutionActionNotApplied || prior.ActionKind != "create_candidate_"+kind {
		return newConflict("候选 %s 已存在但没有可恢复的创建动作记录", kind)
	}
	var intent blueGreenActionIntent
	if err := json.Unmarshal(prior.Intent, &intent); err != nil || intent.Kind != kind || intent.Name != current.GetName() || intent.Namespace != current.GetNamespace() || fmt.Sprint(intent.PlanID) != planIDText || fmt.Sprint(intent.ExecutionID) != executionIDText {
		return newConflict("候选 %s 的持久化创建动作身份不匹配", kind)
	}
	if err := validateBlueGreenCandidateObject(current, desired, intent.PlanID, intent.ExecutionID, kind, "", ""); err != nil {
		return err
	}
	if err := validateCandidateActionObservedIdentity(prior, current); err != nil {
		return err
	}
	expectedHash, err := objectSpecHash(desired)
	if err != nil || intent.SpecHash != expectedHash {
		return newConflict("候选 %s 的持久化 spec 摘要不匹配", kind)
	}
	return nil
}

func validateCandidateActionObservedIdentity(action *ExecutionAction, current *unstructured.Unstructured) error {
	if action == nil || current == nil || len(action.Observed) == 0 {
		return nil
	}
	var observed blueGreenActionObservation
	if err := json.Unmarshal(action.Observed, &observed); err != nil {
		return newConflict("候选资源动作回读记录无法解析")
	}
	if observed.UID != "" && observed.UID != string(current.GetUID()) {
		return newConflict("候选 %s UID 与持久化创建回读不一致", current.GetKind())
	}
	return nil
}

func replicaSetOwnedBy(replicaSet *unstructured.Unstructured, deploymentUID types.UID, deploymentName string) bool {
	owners, found, err := unstructured.NestedSlice(replicaSet.Object, "metadata", "ownerReferences")
	if err != nil || !found {
		return false
	}
	for _, raw := range owners {
		owner, ok := raw.(map[string]any)
		if !ok || owner["apiVersion"] != "apps/v1" || owner["kind"] != "Deployment" || owner["name"] != deploymentName || owner["uid"] != string(deploymentUID) {
			continue
		}
		controller, exists := owner["controller"].(bool)
		if !exists || !controller {
			continue
		}
		return true
	}
	return false
}

func blueGreenWorkloadHash(deployment *unstructured.Unstructured, target DeliveryPlanTarget) (string, error) {
	baseline := deployment.DeepCopy()
	if !target.StableLabelAlreadyPresent {
		labels, found, err := unstructured.NestedStringMap(baseline.Object, "spec", "template", "metadata", "labels")
		if err != nil || !found || labels[target.StableLabelKey] != "stable" {
			return "", NewValidationError("稳定版本接管标签尚未完成")
		}
		delete(labels, target.StableLabelKey)
		if err := unstructured.SetNestedStringMap(baseline.Object, labels, "spec", "template", "metadata", "labels"); err != nil {
			return "", err
		}
	}
	return workloadHash(baseline, target.Container, false)
}
