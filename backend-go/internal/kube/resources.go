package kube

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ErrResourceNotAllowed marks a GVR outside the server-side allowlist.
// Handlers translate it to a 400-class rejection; the registry below is
// the single place where allowlisted resources are declared.
var ErrResourceNotAllowed = errors.New("资源类型不在服务端 allowlist 中")

// ErrResourceUnsupported marks an allowlisted GVR that the target cluster
// does not serve (discovery NoMatch), e.g. a resource removed by a newer
// API server or provided by an absent CRD.
var ErrResourceUnsupported = errors.New("集群不支持该资源类型")

// ResourceDef declares one allowlisted Kubernetes resource. Group is ""
// for core/v1 resources; Resource is the plural lowercase name used in
// API paths; Namespaced mirrors the API server scope. The write
// capability flags are the *only* place write permissions live — a
// resource without a flag can never receive that operation.
type ResourceDef struct {
	Group      string
	Version    string
	Resource   string
	Kind       string
	Namespaced bool
	// Scalable allows spec.replicas patches (deployments, statefulsets).
	Scalable bool
	// Imageable allows spec.template.spec.containers image patches
	// (workloads with a pod template).
	Imageable bool
	// Restartable allows rollout-restart via the pod-template
	// restartedAt annotation.
	Restartable bool
	// Deletable allows DELETE on the resource. High-risk kinds
	// (namespaces, PVs, nodes, storageclasses, events) stay excluded.
	Deletable bool
	// Editable allows whole-manifest replace (PUT /resources/:name) —
	// the YAML editor's save path. Secrets are excluded because the
	// API masks their data fields; a masked manifest written back
	// would destroy real values.
	Editable bool
	// Creatable allows POST /resources (create from manifest).
	Creatable bool
	// DataEditable allows POST /resources/:name/data — the structured
	// key/value editor. Merge happens server-side against the live
	// object, so a Secret's masked values never round-trip through the
	// client: untouched keys keep their real values, submitted values
	// are the only plaintext that crosses the wire.
	DataEditable bool
}

// GVR is the canonical GroupVersionResource for the dynamic client.
func (d ResourceDef) GVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: d.Group, Version: d.Version, Resource: d.Resource}
}

// GVK is the canonical GroupVersionKind stamped on returned objects.
func (d ResourceDef) GVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: d.Group, Version: d.Version, Kind: d.Kind}
}

// APIVersion renders the wire apiVersion string ("v1" or "group/version").
func (d ResourceDef) APIVersion() string {
	if d.Group == "" {
		return d.Version
	}
	return d.Group + "/" + d.Version
}

// KubectlResource renders the resource selector understood by kubectl:
// "pods" for core resources, "deployments.v1.apps" otherwise.
func (d ResourceDef) KubectlResource() string {
	if d.Group == "" {
		return d.Resource
	}
	return fmt.Sprintf("%s.%s.%s", d.Resource, d.Version, d.Group)
}

// resourceRegistry is the centralized allowlist for generic resource
// access: cluster-scoped kinds, workloads, pods, services, config and
// storage plus events. Secrets are browsable (their data fields are
// masked at the API layer); create and delete are allowed, but edit
// stays off — a masked manifest written back would destroy real values.
var resourceRegistry = []ResourceDef{
	// cluster scope
	{Version: "v1", Resource: "nodes", Kind: "Node"},
	{Version: "v1", Resource: "namespaces", Kind: "Namespace"},
	{Version: "v1", Resource: "persistentvolumes", Kind: "PersistentVolume"},
	{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses", Kind: "StorageClass"},
	// namespace scope
	{Group: "apps", Version: "v1", Resource: "deployments", Kind: "Deployment", Namespaced: true,
		Scalable: true, Imageable: true, Restartable: true, Deletable: true, Editable: true},
	// ReplicaSets are read-only so release preflight can prove that a candidate
	// Pod selector will not overlap an existing controller.
	{Group: "apps", Version: "v1", Resource: "replicasets", Kind: "ReplicaSet", Namespaced: true},
	// EndpointSlices are read-only observations for the native blue-green
	// executor; generic editors do not receive write capabilities for them.
	{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices", Kind: "EndpointSlice", Namespaced: true},
	{Group: "apps", Version: "v1", Resource: "statefulsets", Kind: "StatefulSet", Namespaced: true,
		Scalable: true, Imageable: true, Restartable: true, Deletable: true, Editable: true},
	{Group: "apps", Version: "v1", Resource: "daemonsets", Kind: "DaemonSet", Namespaced: true,
		Imageable: true, Restartable: true, Deletable: true, Editable: true},
	{Group: "batch", Version: "v1", Resource: "jobs", Kind: "Job", Namespaced: true,
		Deletable: true, Editable: true},
	{Group: "batch", Version: "v1", Resource: "cronjobs", Kind: "CronJob", Namespaced: true,
		Deletable: true, Editable: true},
	// Pods are view/logs/terminal/delete only — controller-owned objects
	// whose spec edits are either rejected by the apiserver or silently
	// overwritten by their controller. Whole-manifest PUT stays off so
	// the YAML card renders read-only (Kuboard parity); workload kinds
	// remain the supported manifest-edit path.
	{Version: "v1", Resource: "pods", Kind: "Pod", Namespaced: true, Deletable: true},
	{Version: "v1", Resource: "services", Kind: "Service", Namespaced: true, Deletable: true, Editable: true},
	// ResourceQuota is read-only so release preflight can detect hard namespace
	// quota limits before asking an operator to provision a second workload.
	{Version: "v1", Resource: "resourcequotas", Kind: "ResourceQuota", Namespaced: true},
	// HPA is exposed read-only so delivery onboarding can detect autoscaling
	// constraints without allowing the generic resource editor to mutate it.
	{Group: "autoscaling", Version: "v1", Resource: "horizontalpodautoscalers", Kind: "HorizontalPodAutoscaler", Namespaced: true},
	{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses", Kind: "Ingress", Namespaced: true,
		Deletable: true, Editable: true},
	{Version: "v1", Resource: "configmaps", Kind: "ConfigMap", Namespaced: true, Deletable: true, Editable: true, Creatable: true, DataEditable: true},
	// Secrets: data/stringData values are still replaced with "***" at
	// the API layer on every read AND on the create response. Create is
	// allowed (the dialog submits stringData — plaintext in, masked out);
	// delete is allowed (it touches no data); the structured data editor
	// is allowed (server-side merge — masked values never reach the
	// client). Whole-manifest edit stays off: a masked manifest must
	// never round-trip back into the cluster.
	{Version: "v1", Resource: "secrets", Kind: "Secret", Namespaced: true, Deletable: true, Creatable: true, DataEditable: true},
	{Version: "v1", Resource: "persistentvolumeclaims", Kind: "PersistentVolumeClaim", Namespaced: true, Deletable: true, Editable: true, Creatable: true},
	{Version: "v1", Resource: "events", Kind: "Event", Namespaced: true},
}

// LookupResource resolves a (group, version, resource) triple against the
// allowlist. Anything not registered is rejected — handlers must never
// pass client-supplied GVRs straight to the dynamic client.
func LookupResource(group, version, resource string) (ResourceDef, error) {
	for _, d := range resourceRegistry {
		if d.Group == group && d.Version == version && d.Resource == resource {
			return d, nil
		}
	}
	return ResourceDef{}, fmt.Errorf("%w: %s", ErrResourceNotAllowed, gvrString(group, version, resource))
}

// AllowedResources returns a copy of the registry for introspection
// (docs, capability listings).
func AllowedResources() []ResourceDef {
	return append([]ResourceDef(nil), resourceRegistry...)
}

func gvrString(group, version, resource string) string {
	if group == "" {
		return fmt.Sprintf("%s.%s", resource, version)
	}
	return fmt.Sprintf("%s.%s.%s", resource, version, group)
}
