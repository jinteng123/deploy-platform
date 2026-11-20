package service

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// blueGreenQuotaCheck estimates only the additional namespace quota consumed
// by one candidate Deployment, its preview Service, and its desired Pods. A
// passing quota check remains a manual review because ResourceQuota cannot
// prove scheduler capacity, admission-webhook behavior, or node placement.
func blueGreenQuotaCheck(deployment *unstructured.Unstructured, quotas *unstructured.UnstructuredList, replicas int64) (string, string) {
	if quotas == nil {
		return "unknown", "无法读取 namespace ResourceQuota，不能核对候选版本的硬配额。"
	}
	if len(quotas.Items) == 0 {
		return "review", "namespace 未配置 ResourceQuota；仍需人工确认集群有能力同时运行新旧版本。"
	}
	if deployment == nil || replicas < 1 {
		return "unknown", "Deployment 或候选副本数不完整，无法估算双版本配额。"
	}
	if initContainers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "initContainers"); err != nil {
		return "unknown", "无法读取 initContainer 资源，不能估算候选 Pod 配额。"
	} else if found && len(initContainers) > 0 {
		return "unknown", "目标包含 initContainer；首期容量预检不估算其启动阶段资源峰值。"
	}
	if overhead, found, err := unstructured.NestedMap(deployment.Object, "spec", "template", "spec", "overhead"); err != nil {
		return "unknown", "无法读取 Pod overhead，不能估算候选 Pod 配额。"
	} else if found && len(overhead) > 0 {
		return "unknown", "目标 Pod 声明了 overhead；首期容量预检不估算运行时额外开销。"
	}
	if runtimeClass, _, err := unstructured.NestedString(deployment.Object, "spec", "template", "spec", "runtimeClassName"); err != nil {
		return "unknown", "无法读取 runtimeClassName，不能核对运行时资源开销。"
	} else if runtimeClass != "" {
		return "unknown", "目标使用 RuntimeClass；运行时可能增加 Pod overhead，需人工核对。"
	}
	if podResources, found, err := unstructured.NestedMap(deployment.Object, "spec", "template", "spec", "resources"); err != nil {
		return "unknown", "无法读取 Pod 级 resources，不能估算候选配额。"
	} else if found && len(podResources) > 0 {
		return "unknown", "目标声明了 Pod 级资源预算；首期容量预检仅估算容器级 requests/limits。"
	}
	containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) == 0 {
		return "unknown", "无法读取 Pod 容器资源定义，不能估算候选配额。"
	}

	resourcePerPod := map[string]resource.Quantity{}
	resourceContainerCount := map[string]int{}
	for _, rawContainer := range containers {
		container, ok := rawContainer.(map[string]any)
		if !ok {
			return "unknown", "Pod 容器定义格式无法识别，不能估算候选配额。"
		}
		resources, found, err := unstructured.NestedMap(container, "resources")
		if err != nil {
			return "unknown", "无法读取容器 requests/limits，不能估算候选配额。"
		}
		if !found {
			resources = map[string]any{}
		}
		for _, class := range []string{"requests", "limits"} {
			values, found, err := unstructured.NestedMap(resources, class)
			if err != nil {
				return "unknown", "容器 requests/limits 格式无法识别，不能估算候选配额。"
			}
			if !found {
				continue
			}
			for name, raw := range values {
				quantity, err := resource.ParseQuantity(fmt.Sprint(raw))
				if err != nil {
					return "unknown", fmt.Sprintf("容器资源 %s.%s 格式无效。", class, name)
				}
				key := class + "." + name
				current := resourcePerPod[key]
				current.Add(quantity)
				resourcePerPod[key] = current
				resourceContainerCount[key]++
			}
		}
	}
	resourceComplete := make(map[string]bool, len(resourceContainerCount))
	for key, count := range resourceContainerCount {
		resourceComplete[key] = count == len(containers)
	}

	checked := 0
	unknown := []string{}
	for i := range quotas.Items {
		quota := &quotas.Items[i]
		quotaName := quota.GetName()
		hard, hardFound, hardErr := unstructured.NestedMap(quota.Object, "status", "hard")
		used, usedFound, usedErr := unstructured.NestedMap(quota.Object, "status", "used")
		if hardErr != nil || usedErr != nil || !hardFound || !usedFound {
			unknown = append(unknown, quotaName+" 状态未就绪")
			continue
		}
		for key, rawHard := range hard {
			key = strings.TrimSpace(key)
			addition, tracked, resourceKey, complete := blueGreenQuotaDelta(key, replicas, resourcePerPod, resourceComplete)
			if !tracked {
				continue
			}
			checked++
			if !complete {
				unknown = append(unknown, quotaName+" 缺少 "+resourceKey+" 的可核对数据")
				continue
			}
			hardQuantity, hardParseErr := resource.ParseQuantity(fmt.Sprint(rawHard))
			usedRaw, exists := used[key]
			usedQuantity, usedParseErr := resource.ParseQuantity(fmt.Sprint(usedRaw))
			if hardParseErr != nil || !exists || usedParseErr != nil {
				unknown = append(unknown, quotaName+" 的 "+key+" hard/used 值无效或缺失")
				continue
			}
			available := hardQuantity.DeepCopy()
			available.Sub(usedQuantity)
			if addition.Cmp(available) > 0 {
				return "blocked", fmt.Sprintf("ResourceQuota %s 的 %s 不足：候选额外需要 %s，当前可用 %s。", quotaName, key, addition.String(), available.String())
			}
		}
	}
	if len(unknown) > 0 {
		return "unknown", "部分 ResourceQuota 无法核对（" + strings.Join(compactCapacityDetails(unknown), "；") + "）；请补齐读取权限或检查 LimitRange/资源默认值后重试。"
	}
	if checked == 0 {
		return "review", fmt.Sprintf("已读取 %d 个 ResourceQuota，但它们没有覆盖候选 Pod/Service/Deployment 的可估算配额；仍需人工确认双版本容量。", len(quotas.Items))
	}
	return "review", fmt.Sprintf("已核对 %d 项 ResourceQuota，未发现明确超额；这不代表节点有可调度容量，仍需人工确认新旧版本可同时运行。", checked)
}

func blueGreenQuotaDelta(key string, replicas int64, perPod map[string]resource.Quantity, complete map[string]bool) (resource.Quantity, bool, string, bool) {
	zero := resource.Quantity{}
	switch key {
	case "pods", "count/pods":
		return resource.MustParse(strconv.FormatInt(replicas, 10)), true, "pods", true
	case "services", "count/services":
		return resource.MustParse("1"), true, "services", true
	case "count/deployments.apps":
		return resource.MustParse("1"), true, "deployments.apps", true
	case "count/replicasets.apps":
		return resource.MustParse("1"), true, "replicasets.apps", true
	case "count/endpointslices.discovery.k8s.io":
		return zero, true, key, false
	case "requests.storage", "persistentvolumeclaims", "count/persistentvolumeclaims":
		// Blue-green shares existing volumes and does not create another PVC.
		return zero, true, key, true
	}
	if strings.HasPrefix(key, "count/") {
		return zero, false, key, true
	}
	for _, class := range []string{"requests.", "limits."} {
		if strings.HasPrefix(key, class) {
			name := strings.TrimPrefix(key, class)
			// requests.storage is a PVC quota, not a Pod resource delta.
			if class == "requests." && name == "storage" {
				return zero, true, key, true
			}
			resourceKey := class + name
			if !complete[resourceKey] {
				return zero, true, resourceKey, false
			}
			total := perPod[resourceKey].DeepCopy()
			total.Mul(replicas)
			return total, true, resourceKey, true
		}
	}
	return zero, false, key, true
}

func compactCapacityDetails(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
