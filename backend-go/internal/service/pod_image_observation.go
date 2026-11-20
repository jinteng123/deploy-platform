package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
)

// observePodImageDigest reports whether the current Deployment ReplicaSet's
// ready Pods expose the exact digest requested by the release. Container
// runtimes may report a config digest, and multi-arch indexes may resolve to
// platform manifests, so non-matching/unavailable formats stay unknown rather
// than being treated as a deployment failure.
func observePodImageDigest(ctx context.Context, rt KubernetesRuntime, namespace, deploymentName, containerName, expectedImage string) (string, string) {
	return observePodImageDigestWithRegistry(ctx, rt, namespace, deploymentName, containerName, expectedImage, config.Config{})
}

func observePodImageDigestWithRegistry(ctx context.Context, rt KubernetesRuntime, namespace, deploymentName, containerName, expectedImage string, cfg config.Config) (string, string) {
	unknown := func(reason string) (string, string) { return "unknown", reason }
	expectedDigest := imageReferenceDigest(expectedImage)
	if expectedDigest == "" {
		return unknown("目标镜像没有可核对的 sha256 digest。")
	}

	deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return unknown("无法识别 Deployment 资源，未核对 Pod 镜像摘要。")
	}
	deployment, err := rt.GetResource(ctx, deploymentDef, namespace, deploymentName, metav1.GetOptions{})
	if err != nil || deployment == nil {
		return unknown("无法读取当前 Deployment，未核对 Pod 镜像摘要。")
	}
	if string(deployment.GetUID()) == "" {
		return unknown("Deployment 缺少 UID，无法确认 ReplicaSet 归属。")
	}
	deploymentSelector, err := imageObservationSelector(deployment)
	if err != nil {
		return unknown("Deployment selector 不完整，未核对 Pod 镜像摘要。")
	}

	replicaSetDef, err := kube.LookupResource("apps", "v1", "replicasets")
	if err != nil {
		return unknown("无法识别 ReplicaSet 资源，未核对 Pod 镜像摘要。")
	}
	replicaSets, err := rt.ListResources(ctx, replicaSetDef, namespace, metav1.ListOptions{LabelSelector: deploymentSelector})
	if err != nil || replicaSets == nil {
		return unknown("没有读取 ReplicaSet 的权限或读取失败，未核对 Pod 镜像摘要。")
	}
	var current *unstructured.Unstructured
	var currentRevision int64 = -1
	ambiguousRevision := false
	for i := range replicaSets.Items {
		replicaSet := &replicaSets.Items[i]
		if !hasControllerOwnerUID(replicaSet, string(deployment.GetUID())) {
			continue
		}
		revision, parseErr := strconv.ParseInt(replicaSet.GetAnnotations()["deployment.kubernetes.io/revision"], 10, 64)
		if parseErr != nil {
			continue
		}
		if revision > currentRevision {
			copy := replicaSet.DeepCopy()
			current, currentRevision, ambiguousRevision = copy, revision, false
		} else if revision == currentRevision {
			ambiguousRevision = true
		}
	}
	if current == nil || ambiguousRevision || string(current.GetUID()) == "" {
		return unknown("无法唯一确定 Deployment 当前 ReplicaSet，未核对 Pod 镜像摘要。")
	}
	if currentImage := targetContainerImage(current, containerName); currentImage != expectedImage {
		return unknown("当前 ReplicaSet 的容器镜像与发布目标不一致，未核对 Pod 镜像摘要。")
	}
	replicas, found, err := unstructured.NestedInt64(current.Object, "spec", "replicas")
	if err != nil {
		return unknown("当前 ReplicaSet 的期望副本数格式无效，未核对 Pod 镜像摘要。")
	}
	if !found {
		replicas = 1 // ReplicaSet defaults an omitted replica count to one.
	}
	if replicas < 1 {
		return unknown("当前 ReplicaSet 没有期望运行的 Pod，未核对 Pod 镜像摘要。")
	}
	selector, err := imageObservationSelector(current)
	if err != nil {
		return unknown("当前 ReplicaSet selector 不完整，未核对 Pod 镜像摘要。")
	}
	podDef, err := kube.LookupResource("", "v1", "pods")
	if err != nil {
		return unknown("无法识别 Pod 资源，未核对 Pod 镜像摘要。")
	}
	pods, err := rt.ListResources(ctx, podDef, namespace, metav1.ListOptions{LabelSelector: selector})
	if err != nil || pods == nil {
		return unknown("没有读取 Pod 状态的权限或读取失败，未核对运行时镜像摘要。")
	}

	type podObservation struct {
		name                 string
		digest               string
		ready                bool
		missing              bool
		verifiedAsIndexChild bool
		indexCheckError      error
	}
	observed := make([]podObservation, 0, len(pods.Items))
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.GetDeletionTimestamp() != nil || !hasControllerOwnerUID(pod, string(current.GetUID())) {
			continue
		}
		item := podObservation{name: pod.GetName()}
		statuses, found, statusErr := unstructured.NestedSlice(pod.Object, "status", "containerStatuses")
		if statusErr == nil && found {
			for _, raw := range statuses {
				status, ok := raw.(map[string]any)
				if !ok || status["name"] != containerName {
					continue
				}
				item.digest = podImageIDDigest(stringValue(status["imageID"]))
				item.ready, _ = status["ready"].(bool)
				break
			}
		}
		item.missing = item.digest == ""
		observed = append(observed, item)
	}
	sort.Slice(observed, func(i, j int) bool { return observed[i].name < observed[j].name })
	if int64(len(observed)) != replicas {
		return unknown(fmt.Sprintf("当前 ReplicaSet 期望 %d 个 Pod，实际观察到 %d 个非终止 Pod；运行时摘要未核实。", replicas, len(observed)))
	}
	indexChecks := make(map[string]bool)
	indexErrors := make(map[string]error)
	registryCtx, cancelRegistry := context.WithTimeout(ctx, 4*time.Second)
	defer cancelRegistry()
	for i := range observed {
		pod := &observed[i]
		if !pod.ready || pod.missing || pod.digest == expectedDigest {
			continue
		}
		if verified, checked := indexChecks[pod.digest]; checked {
			pod.verifiedAsIndexChild = verified
			pod.indexCheckError = indexErrors[pod.digest]
			continue
		}
		verified, checkErr := registryIndexContainsDigest(registryCtx, cfg, expectedImage, pod.digest)
		indexChecks[pod.digest], indexErrors[pod.digest] = verified, checkErr
		pod.verifiedAsIndexChild, pod.indexCheckError = verified, checkErr
	}
	reasons := make([]string, 0, 5)
	indexVerified := 0
	for _, pod := range observed {
		switch {
		case !pod.ready:
			reasons = append(reasons, fmt.Sprintf("Pod %s 尚未就绪。", pod.name))
		case pod.missing:
			reasons = append(reasons, fmt.Sprintf("Pod %s 的容器状态未提供可识别的 registry digest。", pod.name))
		case pod.digest != expectedDigest:
			if pod.verifiedAsIndexChild {
				indexVerified++
				continue
			}
			reason := "多架构镜像可能返回平台 manifest digest；运行时摘要未出现在配置 registry 返回的 index 描述符链中"
			if pod.indexCheckError != nil {
				reason = "多架构镜像可能返回平台 manifest digest；配置 registry 无法完成 index 子摘要核验: " + pod.indexCheckError.Error()
			}
			reasons = append(reasons, fmt.Sprintf("Pod %s 回报的 digest 与目标不同；%s。", pod.name, reason))
		}
		if len(reasons) >= 4 {
			break
		}
	}
	if len(reasons) > 0 {
		return unknown(strings.Join(reasons, " "))
	}
	if indexVerified > 0 {
		return "verified", fmt.Sprintf("已核对 %d 个当前 ReplicaSet Pod；运行时摘要与发布目标 %s 完全一致，另有 %d 个平台 manifest digest 已通过配置 registry 的 OCI index 描述符链核验。", len(observed), expectedDigest, indexVerified)
	}
	return "verified", fmt.Sprintf("已核对 %d 个当前 ReplicaSet Pod；容器运行时报告的 sha256 digest 均与发布目标 %s 一致。", len(observed), expectedDigest)
}

func imageObservationSelector(obj *unstructured.Unstructured) (string, error) {
	selectorMap, found, err := unstructured.NestedMap(obj.Object, "spec", "selector")
	if err != nil || !found {
		return "", fmt.Errorf("selector missing")
	}
	selector := &metav1.LabelSelector{}
	if err := k8sruntime.DefaultUnstructuredConverter.FromUnstructured(selectorMap, selector); err != nil {
		return "", err
	}
	parsed, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil || parsed.Empty() {
		return "", fmt.Errorf("selector empty or invalid")
	}
	return parsed.String(), nil
}

func hasControllerOwnerUID(obj *unstructured.Unstructured, uid string) bool {
	owner := metav1.GetControllerOf(obj)
	return owner != nil && string(owner.UID) == uid
}

func imageReferenceDigest(image string) string {
	index := strings.LastIndex(image, "@")
	if index < 0 {
		return ""
	}
	return NormalizeDigest(image[index+1:])
}

func podImageIDDigest(imageID string) string {
	imageID = strings.TrimSpace(imageID)
	if runtimeSeparator := strings.Index(imageID, "://"); runtimeSeparator >= 0 {
		imageID = imageID[runtimeSeparator+3:]
	}
	index := strings.LastIndex(imageID, "@sha256:")
	if index < 0 {
		return ""
	}
	return NormalizeDigest(imageID[index+1:])
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
