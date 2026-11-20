package service

import (
	"context"
	"fmt"
	"strings"

	"deploy-platform/backend-go/internal/kube"
)

type deliveryPermissionRequirement struct {
	verb, apiGroup, resource, resourceName string
}

func deliveryPermissionRequirements(strategy, connectionType, deployment, serviceAccountName string, includeServiceAccountImagePullSecrets bool) []deliveryPermissionRequirement {
	serviceAccountRead := []deliveryPermissionRequirement{}
	if includeServiceAccountImagePullSecrets {
		serviceAccountRead = append(serviceAccountRead, deliveryPermissionRequirement{
			verb: "get", resource: "serviceaccounts", resourceName: serviceAccountName,
		})
	}
	if strategy == "rolling" {
		// client-go writes the Deployment with Update; kubectl set image uses
		// Get + Patch and rollout status watches the Deployment over SSH.
		if connectionType == "" || connectionType == "legacy_ssh" {
			return append(serviceAccountRead, []deliveryPermissionRequirement{
				{verb: "get", apiGroup: "apps", resource: "deployments", resourceName: deployment},
				{verb: "patch", apiGroup: "apps", resource: "deployments", resourceName: deployment},
				{verb: "watch", apiGroup: "apps", resource: "deployments", resourceName: deployment},
			}...)
		}
		return append(serviceAccountRead, []deliveryPermissionRequirement{
			{verb: "get", apiGroup: "apps", resource: "deployments", resourceName: deployment},
			{verb: "update", apiGroup: "apps", resource: "deployments", resourceName: deployment},
		}...)
	}
	if strategy != "blue_green" {
		return nil
	}
	// Candidate object names are generated per run, so namespace-level
	// create/patch/delete rights are required for these resource types.
	return append(serviceAccountRead, []deliveryPermissionRequirement{
		{verb: "create", apiGroup: "apps", resource: "deployments"},
		{verb: "patch", apiGroup: "apps", resource: "deployments"},
		{verb: "delete", apiGroup: "apps", resource: "deployments"},
		{verb: "create", resource: "services"},
		{verb: "patch", resource: "services"},
		{verb: "delete", resource: "services"},
	}...)
}

func deliveryPermissionCheck(runtime KubernetesRuntime, ctx context.Context, namespace, deployment, connectionType, strategy, serviceAccountName string, includeServiceAccountImagePullSecrets bool) DeliveryPlanCheck {
	if runtime == nil {
		return deliveryPermissionCheckForRules(nil, fmt.Errorf("目标集群执行凭据不可用"), namespace, deployment, connectionType, strategy, serviceAccountName, includeServiceAccountImagePullSecrets)
	}
	rules, err := runtime.AccessRules(ctx, namespace)
	return deliveryPermissionCheckForRules(rules, err, namespace, deployment, connectionType, strategy, serviceAccountName, includeServiceAccountImagePullSecrets)
}

func deliveryPermissionCheckForRules(rules []kube.ResourceRule, rulesErr error, namespace, deployment, connectionType, strategy, serviceAccountName string, includeServiceAccountImagePullSecrets bool) DeliveryPlanCheck {
	key, title := "rolling-write-permission", "滚动发布执行权限"
	if strategy == "blue_green" {
		key, title = "blue-green-write-permission", "蓝绿发布执行权限"
	}
	check := DeliveryPlanCheck{Key: key, Status: "unknown", Title: title}
	requirements := deliveryPermissionRequirements(strategy, connectionType, deployment, serviceAccountName, includeServiceAccountImagePullSecrets)
	if len(requirements) == 0 {
		check.Detail = "该发布策略没有已定义的 Worker 权限检查项，当前不能确认是否可执行。"
		return check
	}
	if rulesErr != nil {
		check.Detail = fmt.Sprintf("无法读取发布执行身份在 namespace %s 的授权规则；请检查 SelfSubjectRulesReview 或 kubectl auth can-i 是否可用。", namespace)
		return check
	}
	missing := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		if !kube.Can(rules, requirement.verb, requirement.apiGroup, requirement.resource, requirement.resourceName) {
			resource := requirement.resource
			if requirement.apiGroup != "" {
				resource += "." + requirement.apiGroup
			}
			missing = append(missing, requirement.verb+" "+resource)
		}
	}
	if len(missing) > 0 {
		check.Status = "blocked"
		check.Detail = "发布执行身份缺少：" + strings.Join(missing, "、") + "。请在目标 namespace 为该身份补充权限后重新预检。"
		return check
	}
	check.Status = "passed"
	check.Detail = fmt.Sprintf("已通过目标 namespace 的权限规则检查（执行身份来自集群连接配置；策略：%s）。", strategy)
	return check
}

func deliveryPermissionChecks(ctx context.Context, runtime KubernetesRuntime, namespace, deployment, connectionType, serviceAccountName string, includeServiceAccountImagePullSecrets bool) []DeliveryPlanCheck {
	var rules []kube.ResourceRule
	var err error
	if runtime == nil {
		err = fmt.Errorf("目标集群执行凭据不可用")
	} else {
		rules, err = runtime.AccessRules(ctx, namespace)
	}
	return []DeliveryPlanCheck{
		deliveryPermissionCheckForRules(rules, err, namespace, deployment, connectionType, "rolling", serviceAccountName, includeServiceAccountImagePullSecrets),
		deliveryPermissionCheckForRules(rules, err, namespace, deployment, connectionType, "blue_green", serviceAccountName, includeServiceAccountImagePullSecrets),
	}
}
