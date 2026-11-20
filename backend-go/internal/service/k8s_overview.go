package service

import (
	"context"
	"sort"
	"strings"

	"deploy-platform/backend-go/internal/kube"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ClusterOverview aggregates everything the console's cluster dashboard
// renders in one call: server version, node health + capacity, namespace
// and workload counts, recent Warning events and (when metrics-server is
// installed) resource usage. metrics.k8s.io failure degrades to
// Metrics.Available=false — every other section still populates.
type ClusterOverview struct {
	ServerVersion string         `json:"server_version"`
	Nodes         NodeSummary    `json:"nodes"`
	Namespaces    int            `json:"namespaces"`
	Workloads     WorkloadCounts `json:"workloads"`
	WarningEvents []EventSummary `json:"warning_events"`
	Metrics       MetricsStatus  `json:"metrics"`
	TopPods       []PodMetric    `json:"top_pods"`
}

type NodeSummary struct {
	Total int            `json:"total"`
	Ready int            `json:"ready"`
	Items []NodeOverview `json:"items"`
}

type NodeOverview struct {
	Name              string      `json:"name"`
	Ready             bool        `json:"ready"`
	Roles             []string    `json:"roles"`
	KubeletVersion    string      `json:"kubelet_version"`
	OSImage           string      `json:"os_image"`
	InternalIP        string      `json:"internal_ip"`
	CPUCapacityMillis int64       `json:"cpu_capacity_millis"`
	MemCapacityBytes  int64       `json:"mem_capacity_bytes"`
	Usage             *kube.Usage `json:"usage,omitempty"`
}

type WorkloadCounts struct {
	Deployments  int `json:"deployments"`
	StatefulSets int `json:"stateful_sets"`
	DaemonSets   int `json:"daemon_sets"`
	Jobs         int `json:"jobs"`
	CronJobs     int `json:"cron_jobs"`
	Pods         int `json:"pods"`
	PodsRunning  int `json:"pods_running"`
}

// EventSummary is one Warning event row; Object is "kind/name".
type EventSummary struct {
	Namespace     string `json:"namespace"`
	Reason        string `json:"reason"`
	Message       string `json:"message"`
	Object        string `json:"object"`
	Count         int64  `json:"count"`
	LastTimestamp string `json:"last_timestamp"`
}

type PodMetric struct {
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Usage     kube.Usage `json:"usage"`
}

type MetricsStatus struct {
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`
}

const (
	warningEventLimit = 20
	topPodLimit       = 10
)

// BuildClusterOverview composes the dashboard over any runtime —
// callers resolve cluster + runtime first (resolveCluster), so this
// stays purely about Kubernetes data.
func BuildClusterOverview(ctx context.Context, rt KubernetesRuntime) (*ClusterOverview, error) {
	ov := &ClusterOverview{WarningEvents: []EventSummary{}, TopPods: []PodMetric{}}

	version, err := rt.ServerVersion(ctx)
	if err != nil {
		return nil, err
	}
	ov.ServerVersion = version

	if err := ov.fillNodes(ctx, rt); err != nil {
		return nil, err
	}
	if err := ov.fillNamespaces(ctx, rt); err != nil {
		return nil, err
	}
	if err := ov.fillWorkloads(ctx, rt); err != nil {
		return nil, err
	}
	if err := ov.fillWarningEvents(ctx, rt); err != nil {
		return nil, err
	}
	ov.fillMetrics(ctx, rt) // degrades into Metrics.Message, never fails
	return ov, nil
}

func (ov *ClusterOverview) fillNodes(ctx context.Context, rt KubernetesRuntime) error {
	def, err := kube.LookupResource("", "v1", "nodes")
	if err != nil {
		return err
	}
	list, err := rt.ListResources(ctx, def, "", metav1.ListOptions{})
	if err != nil {
		return err
	}
	ov.Nodes.Items = make([]NodeOverview, 0, len(list.Items))
	for _, item := range list.Items {
		ov.Nodes.Items = append(ov.Nodes.Items, nodeOverview(item))
	}
	ov.Nodes.Total = len(ov.Nodes.Items)
	for _, n := range ov.Nodes.Items {
		if n.Ready {
			ov.Nodes.Ready++
		}
	}
	return nil
}

func (ov *ClusterOverview) fillNamespaces(ctx context.Context, rt KubernetesRuntime) error {
	def, err := kube.LookupResource("", "v1", "namespaces")
	if err != nil {
		return err
	}
	list, err := rt.ListResources(ctx, def, "", metav1.ListOptions{})
	if err != nil {
		return err
	}
	ov.Namespaces = len(list.Items)
	return nil
}

func (ov *ClusterOverview) fillWorkloads(ctx context.Context, rt KubernetesRuntime) error {
	count := func(group, resource string, dst *int) error {
		def, err := kube.LookupResource(group, "v1", resource)
		if err != nil {
			return err
		}
		list, err := rt.ListResources(ctx, def, "", metav1.ListOptions{})
		if err != nil {
			return err
		}
		*dst = len(list.Items)
		return nil
	}
	for _, w := range []struct {
		group, resource string
		dst             *int
	}{
		{"apps", "deployments", &ov.Workloads.Deployments},
		{"apps", "statefulsets", &ov.Workloads.StatefulSets},
		{"apps", "daemonsets", &ov.Workloads.DaemonSets},
		{"batch", "jobs", &ov.Workloads.Jobs},
		{"batch", "cronjobs", &ov.Workloads.CronJobs},
	} {
		if err := count(w.group, w.resource, w.dst); err != nil {
			return err
		}
	}
	def, err := kube.LookupResource("", "v1", "pods")
	if err != nil {
		return err
	}
	pods, err := rt.ListResources(ctx, def, "", metav1.ListOptions{})
	if err != nil {
		return err
	}
	ov.Workloads.Pods = len(pods.Items)
	for _, p := range pods.Items {
		if phase, _, _ := unstructured.NestedString(p.Object, "status", "phase"); phase == "Running" {
			ov.Workloads.PodsRunning++
		}
	}
	return nil
}

func (ov *ClusterOverview) fillWarningEvents(ctx context.Context, rt KubernetesRuntime) error {
	def, err := kube.LookupResource("", "v1", "events")
	if err != nil {
		return err
	}
	list, err := rt.ListResources(ctx, def, "", metav1.ListOptions{})
	if err != nil {
		return err
	}
	events := make([]EventSummary, 0, len(list.Items))
	for _, item := range list.Items {
		typ, _, _ := unstructured.NestedString(item.Object, "type")
		if typ != "Warning" {
			continue
		}
		events = append(events, eventSummary(item))
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].LastTimestamp > events[j].LastTimestamp
	})
	if len(events) > warningEventLimit {
		events = events[:warningEventLimit]
	}
	ov.WarningEvents = events
	return nil
}

// fillMetrics merges node usage into the node rows and builds the top
// pods table. Any metrics.k8s.io error flips Metrics.Available to false
// and records the cause — the rest of the overview is unaffected.
func (ov *ClusterOverview) fillMetrics(ctx context.Context, rt KubernetesRuntime) {
	var msgs []string
	nodeMetrics, err := rt.ListMetrics(ctx, "nodes", "")
	if err != nil {
		msgs = append(msgs, err.Error())
	} else {
		byName := map[string]kube.Usage{}
		for _, item := range nodeMetrics.Items {
			byName[item.GetName()] = kube.NodeUsage(item)
		}
		for i := range ov.Nodes.Items {
			if u, ok := byName[ov.Nodes.Items[i].Name]; ok {
				u := u
				ov.Nodes.Items[i].Usage = &u
			}
		}
	}
	podMetrics, err := rt.ListMetrics(ctx, "pods", "")
	if err != nil {
		msgs = append(msgs, err.Error())
	} else {
		pods := make([]PodMetric, 0, len(podMetrics.Items))
		for _, item := range podMetrics.Items {
			pods = append(pods, PodMetric{
				Namespace: item.GetNamespace(),
				Name:      item.GetName(),
				Usage:     kube.PodUsage(item),
			})
		}
		sort.SliceStable(pods, func(i, j int) bool {
			return pods[i].Usage.CPUMillis > pods[j].Usage.CPUMillis
		})
		if len(pods) > topPodLimit {
			pods = pods[:topPodLimit]
		}
		ov.TopPods = pods
	}
	ov.Metrics = MetricsStatus{Available: len(msgs) == 0, Message: strings.Join(msgs, "; ")}
}

// nodeOverview flattens a core/v1 Node's status into the dashboard row.
// Fields absent from minimal objects (mock runtime) degrade to zero
// values rather than failing.
func nodeOverview(item unstructured.Unstructured) NodeOverview {
	n := NodeOverview{
		Name:  item.GetName(),
		Roles: nodeRoles(item),
	}
	n.KubeletVersion, _, _ = unstructured.NestedString(item.Object, "status", "nodeInfo", "kubeletVersion")
	n.OSImage, _, _ = unstructured.NestedString(item.Object, "status", "nodeInfo", "osImage")
	if s, ok, _ := unstructured.NestedString(item.Object, "status", "capacity", "cpu"); ok {
		if q, err := resource.ParseQuantity(s); err == nil {
			n.CPUCapacityMillis = q.MilliValue()
		}
	}
	if s, ok, _ := unstructured.NestedString(item.Object, "status", "capacity", "memory"); ok {
		if q, err := resource.ParseQuantity(s); err == nil {
			n.MemCapacityBytes = q.Value()
		}
	}
	addresses, _, _ := unstructured.NestedSlice(item.Object, "status", "addresses")
	for _, a := range addresses {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if typ, _, _ := unstructured.NestedString(m, "type"); typ == "InternalIP" {
			n.InternalIP, _, _ = unstructured.NestedString(m, "address")
		}
	}
	conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
	for _, cond := range conditions {
		m, ok := cond.(map[string]any)
		if !ok {
			continue
		}
		typ, _, _ := unstructured.NestedString(m, "type")
		st, _, _ := unstructured.NestedString(m, "status")
		if typ == "Ready" && st == "True" {
			n.Ready = true
		}
	}
	return n
}

// nodeRoles mirrors kubectl: control-plane/worker-style role labels are
// `node-role.kubernetes.io/<role>=`; nodes without any get no roles.
func nodeRoles(item unstructured.Unstructured) []string {
	const prefix = "node-role.kubernetes.io/"
	roles := []string{}
	for k := range item.GetLabels() {
		if strings.HasPrefix(k, prefix) {
			roles = append(roles, strings.TrimPrefix(k, prefix))
		}
	}
	sort.Strings(roles)
	return roles
}

// eventSummary flattens a core/v1 Event. LastTimestamp prefers
// lastTimestamp, falling back to eventTime (events.k8s.io shape) and
// firstTimestamp so sorting stays meaningful.
func eventSummary(item unstructured.Unstructured) EventSummary {
	e := EventSummary{Namespace: item.GetNamespace()}
	e.Reason, _, _ = unstructured.NestedString(item.Object, "reason")
	e.Message, _, _ = unstructured.NestedString(item.Object, "message")
	e.Count, _, _ = unstructured.NestedInt64(item.Object, "count")
	kind, _, _ := unstructured.NestedString(item.Object, "involvedObject", "kind")
	name, _, _ := unstructured.NestedString(item.Object, "involvedObject", "name")
	e.Object = strings.TrimSuffix(kind+"/"+name, "/")
	for _, field := range []string{"lastTimestamp", "eventTime", "firstTimestamp"} {
		if ts, ok, _ := unstructured.NestedString(item.Object, field); ok && ts != "" {
			e.LastTimestamp = ts
			break
		}
	}
	return e
}
