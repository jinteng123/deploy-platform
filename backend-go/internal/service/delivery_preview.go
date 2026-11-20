package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
)

// DeliveryCapabilityPreview is a read-only observation used by onboarding.
// It intentionally contains resource identity and summarized capability
// results, not a manifest or cluster credentials.
type DeliveryCapabilityPreview struct {
	ClusterID       int                 `json:"cluster_id"`
	Namespace       string              `json:"namespace"`
	Deployment      string              `json:"deployment"`
	DeploymentUID   string              `json:"deployment_uid,omitempty"`
	ResourceVersion string              `json:"resource_version,omitempty"`
	ObservedAt      time.Time           `json:"observed_at"`
	ServiceNames    []string            `json:"matching_services"`
	AutoscalerNames []string            `json:"autoscalers"`
	Checks          []DeliveryPlanCheck `json:"checks"`
}

// PreviewDeliveryCapabilities reads the target Deployment, namespace
// Services, HPAs and quotas using the caller's Kubernetes identity. It reads
// write authorization rules separately through the Worker execution identity.
// Unknown reads and authorization results stay explicit instead of looking
// like successful empty data.
func (s *CDService) PreviewDeliveryCapabilities(ctx context.Context, clusterID int, namespace, deploymentName string, user *User) (DeliveryCapabilityPreview, error) {
	preview := DeliveryCapabilityPreview{
		ClusterID: clusterID, Namespace: strings.TrimSpace(namespace), Deployment: strings.TrimSpace(deploymentName),
		ObservedAt: time.Now().UTC(), ServiceNames: []string{}, AutoscalerNames: []string{}, Checks: []DeliveryPlanCheck{},
	}
	if clusterID <= 0 {
		return preview, NewValidationError("cluster_id 必须大于 0")
	}
	if errs := validation.IsDNS1123Label(preview.Namespace); len(errs) > 0 {
		return preview, NewValidationError("namespace 无效: %s", strings.Join(errs, "; "))
	}
	if errs := validation.IsDNS1123Subdomain(preview.Deployment); len(errs) > 0 {
		return preview, NewValidationError("Deployment 名称无效: %s", strings.Join(errs, "; "))
	}
	if s.worker == nil || s.worker.pipeline == nil || s.worker.pipeline.k8s == nil {
		return preview, NewValidationError("发布运行时不可用，无法执行能力预检")
	}
	cluster, err := s.store.FindCluster(clusterID)
	if err != nil {
		return preview, err
	}
	runtime, err := s.worker.pipeline.k8s.ForUser(cluster, user)
	if err != nil {
		return preview, err
	}
	executorRuntime, executorErr := s.worker.pipeline.k8s.For(cluster)
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	add := func(key, status, title, detail string) {
		preview.Checks = append(preview.Checks, DeliveryPlanCheck{Key: key, Status: status, Title: title, Detail: detail})
	}

	deploymentDef, defErr := kube.LookupResource("apps", "v1", "deployments")
	var deployment *unstructured.Unstructured
	var podLabels map[string]string
	if defErr != nil {
		add("deployment", "unknown", "Deployment", "平台无法识别 Deployment 资源类型。")
	} else {
		deployment, err = runtime.GetResource(readCtx, deploymentDef, preview.Namespace, preview.Deployment, metav1.GetOptions{})
		switch {
		case err != nil:
			add("deployment", "unknown", "Deployment", "无法读取目标 Deployment；请检查名称、权限和连接。")
		case deployment == nil:
			add("deployment", "unknown", "Deployment", "目标 Deployment 不存在或读取结果为空。")
		default:
			preview.DeploymentUID = string(deployment.GetUID())
			preview.ResourceVersion = deployment.GetResourceVersion()
			var found bool
			podLabels, found, err = unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
			if err != nil || !found || len(podLabels) == 0 {
				add("deployment", "unknown", "Deployment", "已读取 Deployment，但 Pod 模板没有可检查的标签。")
			} else {
				add("deployment", "passed", "Deployment", "已读取目标 Deployment 及 Pod 模板标签。")
			}
		}
	}

	serviceDef, defErr := kube.LookupResource("", "v1", "services")
	serviceList, serviceErr := (*unstructured.UnstructuredList)(nil), defErr
	if serviceErr == nil {
		serviceList, serviceErr = runtime.ListResources(readCtx, serviceDef, preview.Namespace, metav1.ListOptions{})
	}
	if serviceErr != nil {
		add("service-entry", "unknown", "Service 入口候选", "无法枚举 namespace 内 Service；不能确认现有入口。")
	} else if deployment == nil || len(podLabels) == 0 {
		add("service-entry", "unknown", "Service 入口候选", "已读取 Service，但 Deployment 标签未知，无法计算 selector 匹配。")
	} else {
		for i := range serviceList.Items {
			if serviceSelectsPodTemplate(&serviceList.Items[i], podLabels) {
				preview.ServiceNames = append(preview.ServiceNames, serviceList.Items[i].GetName())
			}
		}
		sort.Strings(preview.ServiceNames)
		switch len(preview.ServiceNames) {
		case 0:
			add("service-entry", "review", "Service 入口候选", "当前没有 Service selector 匹配目标 Deployment；蓝绿计划需要先明确入口。")
		case 1:
			add("service-entry", "passed", "Service 入口候选", "已找到一个 selector 匹配目标 Deployment 的 Service。")
		default:
			add("service-entry", "review", "Service 入口候选", fmt.Sprintf("已找到 %d 个匹配 Service；请明确唯一的生产流量入口。", len(preview.ServiceNames)))
		}
	}

	hpaDef, defErr := kube.LookupResource("autoscaling", "v1", "horizontalpodautoscalers")
	hpaList, hpaErr := (*unstructured.UnstructuredList)(nil), defErr
	if hpaErr == nil {
		hpaList, hpaErr = runtime.ListResources(readCtx, hpaDef, preview.Namespace, metav1.ListOptions{})
	}
	if hpaErr != nil {
		add("autoscaling", "unknown", "自动扩缩容", "无法枚举 namespace 内 HPA；不能确认副本数是否会在发布期间变化。")
	} else {
		for i := range hpaList.Items {
			item := &hpaList.Items[i]
			apiVersion, _, _ := unstructured.NestedString(item.Object, "spec", "scaleTargetRef", "apiVersion")
			kind, _, _ := unstructured.NestedString(item.Object, "spec", "scaleTargetRef", "kind")
			name, _, _ := unstructured.NestedString(item.Object, "spec", "scaleTargetRef", "name")
			if apiVersion == "apps/v1" && kind == "Deployment" && name == preview.Deployment {
				preview.AutoscalerNames = append(preview.AutoscalerNames, item.GetName())
			}
		}
		sort.Strings(preview.AutoscalerNames)
		if len(preview.AutoscalerNames) == 0 {
			add("autoscaling", "passed", "自动扩缩容", "未发现指向目标 Deployment 的 HPA。")
		} else {
			add("autoscaling", "review", "自动扩缩容", fmt.Sprintf("发现 HPA（%s）；蓝绿执行前需评估副本协调与容量。", strings.Join(preview.AutoscalerNames, "、")))
		}
	}

	quotaDef, quotaDefErr := kube.LookupResource("", "v1", "resourcequotas")
	quotas, quotaErr := (*unstructured.UnstructuredList)(nil), quotaDefErr
	if quotaErr == nil {
		quotas, quotaErr = runtime.ListResources(readCtx, quotaDef, preview.Namespace, metav1.ListOptions{})
	}
	if quotaErr != nil || quotas == nil {
		add("capacity", "unknown", "双版本容量", "无法读取 namespace ResourceQuota；请检查权限和连接。")
	} else {
		desired := int64(0)
		if deployment != nil {
			desired = deploymentReplicas(deployment)
		}
		status, detail := blueGreenQuotaCheck(deployment, quotas, desired)
		add("capacity", status, "双版本容量", detail)
	}
	serviceAccountName, includeServiceAccountPullSecrets, serviceAccountKnown := deliveryServiceAccountPullSecretLookup(deployment)
	if !serviceAccountKnown {
		serviceAccountName = "default"
		includeServiceAccountPullSecrets = true
	}
	if executorErr != nil {
		preview.Checks = append(preview.Checks,
			deliveryPermissionCheck(nil, readCtx, preview.Namespace, preview.Deployment, cluster.ConnectionType, "rolling", serviceAccountName, includeServiceAccountPullSecrets),
			deliveryPermissionCheck(nil, readCtx, preview.Namespace, preview.Deployment, cluster.ConnectionType, "blue_green", serviceAccountName, includeServiceAccountPullSecrets),
		)
	} else {
		preview.Checks = append(preview.Checks, deliveryPermissionChecks(readCtx, executorRuntime, preview.Namespace, preview.Deployment, cluster.ConnectionType, serviceAccountName, includeServiceAccountPullSecrets)...)
	}
	preview.ObservedAt = time.Now().UTC()
	return preview, nil
}

func serviceSelectsPodTemplate(service *unstructured.Unstructured, podLabels map[string]string) bool {
	selector, found, err := unstructured.NestedStringMap(service.Object, "spec", "selector")
	if err != nil || !found || len(selector) == 0 {
		return false
	}
	return labels.Set(selector).AsSelector().Matches(labels.Set(podLabels))
}
