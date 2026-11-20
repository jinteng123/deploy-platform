package service

import (
	"context"

	"deploy-platform/backend-go/internal/kube"
)

// ResourceCaps mirrors the allowlist's write capability flags with the
// caller's own RBAC verdict — the frontend hides buttons the apiserver
// would reject anyway.
type ResourceCaps struct {
	List   bool `json:"list"`
	Create bool `json:"create"`
	Update bool `json:"update"`
	Delete bool `json:"delete"`
}

// Capabilities is the per-(cluster,user,namespace) access map served by
// GET /k8s/capabilities. Keys are "group/version/resource" (group empty
// for core). Exec/Logs cover the pod sub-resources.
type Capabilities struct {
	Resources map[string]ResourceCaps `json:"resources"`
	Exec      bool                    `json:"exec"`
	Logs      bool                    `json:"logs"`
}

// BuildCapabilities evaluates the caller's AccessRules against the
// server-side allowlist — a capability is only ever *narrowed* by the
// registry, never widened beyond what the resource type supports.
func BuildCapabilities(rules []kube.ResourceRule) Capabilities {
	caps := Capabilities{Resources: map[string]ResourceCaps{}}
	for _, def := range kube.AllowedResources() {
		key := def.Group + "/" + def.Version + "/" + def.Resource
		caps.Resources[key] = ResourceCaps{
			List: kube.Can(rules, "get", def.Group, def.Resource) ||
				kube.Can(rules, "list", def.Group, def.Resource),
			Create: kube.Can(rules, "create", def.Group, def.Resource),
			Update: kube.Can(rules, "update", def.Group, def.Resource) ||
				kube.Can(rules, "patch", def.Group, def.Resource),
			Delete: kube.Can(rules, "delete", def.Group, def.Resource),
		}
	}
	caps.Exec = kube.Can(rules, "create", "", "pods/exec")
	caps.Logs = kube.Can(rules, "get", "", "pods/log") ||
		kube.Can(rules, "get", "", "pods")
	return caps
}

// AccessCapabilities resolves the caller's rules in the namespace and
// maps them to the allowlist — one SelfSubjectRulesReview (or one
// `kubectl auth can-i --list`) per call.
func AccessCapabilities(ctx context.Context, rt KubernetesRuntime, namespace string) (Capabilities, error) {
	rules, err := rt.AccessRules(ctx, namespace)
	if err != nil {
		return Capabilities{}, err
	}
	return BuildCapabilities(rules), nil
}
