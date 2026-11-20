package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/kube"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// overviewStub is a KubernetesRuntime fake: canned lists keyed by
// resource name, scripted version/metrics results.
type overviewStub struct {
	lists    map[string]*unstructured.UnstructuredList
	version  string
	verErr   error
	metrics  map[string]*unstructured.UnstructuredList
	metErr   map[string]error
	metCalls []string
}

func (s *overviewStub) Pods(context.Context, string) ([]map[string]any, error) { return nil, nil }
func (s *overviewStub) PodLogs(context.Context, string, string, int64) (string, error) {
	return "", nil
}
func (s *overviewStub) RestartPod(context.Context, string, string) error { return nil }
func (s *overviewStub) DeploymentInfo(context.Context, string, string) (map[string]any, error) {
	return nil, nil
}
func (s *overviewStub) DeploymentEvents(context.Context, string, string) (string, error) {
	return "", nil
}
func (s *overviewStub) CurrentImage(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (s *overviewStub) SetImage(context.Context, string, string, string, string) error {
	return nil
}
func (s *overviewStub) RolloutStatus(context.Context, string, string) (string, error) {
	return "", nil
}
func (s *overviewStub) ListResources(_ context.Context, def kube.ResourceDef, _ string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if l, ok := s.lists[def.Resource]; ok {
		return l, nil
	}
	return &unstructured.UnstructuredList{}, nil
}
func (s *overviewStub) GetResource(context.Context, kube.ResourceDef, string, string, metav1.GetOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (s *overviewStub) ServerVersion(context.Context) (string, error) {
	return s.version, s.verErr
}
func (s *overviewStub) ListMetrics(_ context.Context, resource, _ string) (*unstructured.UnstructuredList, error) {
	s.metCalls = append(s.metCalls, resource)
	if err := s.metErr[resource]; err != nil {
		return nil, err
	}
	if l, ok := s.metrics[resource]; ok {
		return l, nil
	}
	return &unstructured.UnstructuredList{}, nil
}
func (s *overviewStub) PatchResource(context.Context, kube.ResourceDef, string, string, types.PatchType, []byte, metav1.PatchOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (s *overviewStub) DeleteResource(context.Context, kube.ResourceDef, string, string, metav1.DeleteOptions) error {
	return nil
}
func (s *overviewStub) UpdateResource(context.Context, kube.ResourceDef, string, *unstructured.Unstructured, metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (s *overviewStub) CreateResource(context.Context, kube.ResourceDef, string, *unstructured.Unstructured, metav1.CreateOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (s *overviewStub) PodLogStream(context.Context, string, string, kube.LogOptions) (io.ReadCloser, error) {
	return nil, nil
}
func (s *overviewStub) ExecPod(context.Context, string, string, kube.ExecSpec, kube.ExecIO) error {
	return nil
}
func (s *overviewStub) AccessRules(context.Context, string) ([]kube.ResourceRule, error) {
	return nil, nil
}

func obj(kind, name string, fields map[string]any) unstructured.Unstructured {
	o := map[string]any{"apiVersion": "v1", "kind": kind, "metadata": map[string]any{"name": name}}
	for k, v := range fields {
		o[k] = v
	}
	return unstructured.Unstructured{Object: o}
}

func list(items ...unstructured.Unstructured) *unstructured.UnstructuredList {
	return &unstructured.UnstructuredList{Items: items}
}

func nodeObj(name string, ready bool, cpu, mem string, labels map[string]any) unstructured.Unstructured {
	status := map[string]any{
		"nodeInfo":   map[string]any{"kubeletVersion": "v1.30.0", "osImage": "Debian"},
		"capacity":   map[string]any{"cpu": cpu, "memory": mem},
		"conditions": []any{map[string]any{"type": "Ready", "status": map[bool]string{true: "True", false: "False"}[ready]}},
		"addresses":  []any{map[string]any{"type": "InternalIP", "address": "10.0.0.1"}},
	}
	o := obj("Node", name, map[string]any{"status": status})
	if labels != nil {
		o.Object["metadata"].(map[string]any)["labels"] = labels
	}
	return o
}

func TestClusterOverviewComposes(t *testing.T) {
	stub := &overviewStub{
		version: "v1.31.1",
		lists: map[string]*unstructured.UnstructuredList{
			"nodes": list(
				nodeObj("n1", true, "4", "8Gi", map[string]any{"node-role.kubernetes.io/control-plane": ""}),
				nodeObj("n2", false, "2", "4Gi", nil),
			),
			"namespaces":  list(obj("Namespace", "a", nil), obj("Namespace", "b", nil), obj("Namespace", "c", nil)),
			"deployments": list(obj("Deployment", "d1", nil)),
			"pods": list(
				obj("Pod", "p1", map[string]any{"status": map[string]any{"phase": "Running"}}),
				obj("Pod", "p2", map[string]any{"status": map[string]any{"phase": "Pending"}}),
				obj("Pod", "p3", map[string]any{"status": map[string]any{"phase": "Running"}}),
			),
			"events": list(
				obj("Event", "e1", map[string]any{
					"type": "Warning", "reason": "BackOff", "message": "crash",
					"count": int64(3), "lastTimestamp": "2026-09-18T02:00:00Z",
					"involvedObject": map[string]any{"kind": "Pod", "name": "p1"},
				}),
				obj("Event", "e2", map[string]any{
					"type": "Normal", "reason": "Scheduled", "message": "ok",
					"lastTimestamp": "2026-09-18T03:00:00Z",
				}),
				obj("Event", "e3", map[string]any{
					"type": "Warning", "reason": "FailedMount", "message": "mount",
					"count": int64(1), "lastTimestamp": "2026-09-18T04:00:00Z",
					"involvedObject": map[string]any{"kind": "Pod", "name": "p2"},
				}),
			),
		},
		metrics: map[string]*unstructured.UnstructuredList{
			"nodes": list(
				func() unstructured.Unstructured {
					m := obj("NodeMetrics", "n1", map[string]any{"usage": map[string]any{"cpu": "500m", "memory": "2Gi"}})
					m.Object["apiVersion"] = "metrics.k8s.io/v1beta1"
					return m
				}(),
			),
			"pods": list(
				func() unstructured.Unstructured {
					m := obj("PodMetrics", "p2", map[string]any{"containers": []any{
						map[string]any{"name": "c", "usage": map[string]any{"cpu": "900m", "memory": "1Gi"}},
					}})
					m.Object["metadata"].(map[string]any)["namespace"] = "prod"
					return m
				}(),
				func() unstructured.Unstructured {
					m := obj("PodMetrics", "p1", map[string]any{"containers": []any{
						map[string]any{"name": "c", "usage": map[string]any{"cpu": "100m", "memory": "256Mi"}},
					}})
					m.Object["metadata"].(map[string]any)["namespace"] = "dev"
					return m
				}(),
			),
		},
	}

	ov, err := BuildClusterOverview(context.Background(), stub)
	if err != nil {
		t.Fatal(err)
	}
	if ov.ServerVersion != "v1.31.1" {
		t.Fatalf("version=%q", ov.ServerVersion)
	}
	if ov.Namespaces != 3 {
		t.Fatalf("namespaces=%d", ov.Namespaces)
	}
	if ov.Nodes.Total != 2 || ov.Nodes.Ready != 1 {
		t.Fatalf("nodes=%+v", ov.Nodes)
	}
	n1 := ov.Nodes.Items[0]
	if !n1.Ready || n1.KubeletVersion != "v1.30.0" || n1.CPUCapacityMillis != 4000 ||
		n1.MemCapacityBytes != 8*1024*1024*1024 || n1.InternalIP != "10.0.0.1" ||
		len(n1.Roles) != 1 || n1.Roles[0] != "control-plane" {
		t.Fatalf("node row=%+v", n1)
	}
	if n1.Usage == nil || n1.Usage.CPUMillis != 500 || n1.Usage.MemBytes != 2*1024*1024*1024 {
		t.Fatalf("usage merged=%+v", n1.Usage)
	}
	if ov.Nodes.Items[1].Usage != nil {
		t.Fatal("n2 has no metrics item — usage must stay nil")
	}
	w := ov.Workloads
	if w.Deployments != 1 || w.Pods != 3 || w.PodsRunning != 2 || w.StatefulSets != 0 || w.DaemonSets != 0 || w.Jobs != 0 || w.CronJobs != 0 {
		t.Fatalf("workloads=%+v", w)
	}
	if len(ov.WarningEvents) != 2 {
		t.Fatalf("warnings=%v", ov.WarningEvents)
	}
	// Sorted by lastTimestamp desc; Normal events excluded.
	if ov.WarningEvents[0].Reason != "FailedMount" || ov.WarningEvents[1].Reason != "BackOff" {
		t.Fatalf("warning order=%v", ov.WarningEvents)
	}
	if ov.WarningEvents[1].Object != "Pod/p1" || ov.WarningEvents[1].Count != 3 {
		t.Fatalf("event row=%+v", ov.WarningEvents[1])
	}
	if !ov.Metrics.Available || ov.Metrics.Message != "" {
		t.Fatalf("metrics=%+v", ov.Metrics)
	}
	// Top pods sorted by CPU desc.
	if len(ov.TopPods) != 2 || ov.TopPods[0].Name != "p2" || ov.TopPods[0].Usage.CPUMillis != 900 {
		t.Fatalf("top pods=%v", ov.TopPods)
	}
}

func TestClusterOverviewMetricsDegrade(t *testing.T) {
	stub := &overviewStub{
		version: "v1",
		lists: map[string]*unstructured.UnstructuredList{
			"nodes":      list(nodeObj("n1", true, "4", "8Gi", nil)),
			"namespaces": list(obj("Namespace", "a", nil)),
		},
		metErr: map[string]error{
			"nodes": errors.New("the server could not find the requested resource"),
			"pods":  errors.New("the server could not find the requested resource"),
		},
	}
	ov, err := BuildClusterOverview(context.Background(), stub)
	if err != nil {
		t.Fatal(err)
	}
	if ov.Metrics.Available {
		t.Fatal("metrics must be unavailable when the API errors")
	}
	if !strings.Contains(ov.Metrics.Message, "could not find") {
		t.Fatalf("message=%q", ov.Metrics.Message)
	}
	if ov.Nodes.Total != 1 || ov.Namespaces != 1 {
		t.Fatalf("non-metric sections still populated: %+v", ov)
	}
	if ov.Nodes.Items[0].Usage != nil {
		t.Fatal("no usage merge without metrics")
	}
	if len(stub.metCalls) != 2 {
		t.Fatalf("metCalls=%v", stub.metCalls)
	}
}

func TestClusterOverviewVersionFailurePropagates(t *testing.T) {
	stub := &overviewStub{verErr: errors.New("api down")}
	if _, err := BuildClusterOverview(context.Background(), stub); err == nil {
		t.Fatal("version failure must fail the overview")
	}
}

// sshStubRunner scripts kubectl output for sshKubernetes metrics/version.
type sshStubRunner struct {
	calls []string
	stdin []byte
	fn    func(args []string) (string, string, error)
}

func (s *sshStubRunner) Run(context.Context, string, []string, io.Reader) (string, string, error) {
	return "", "", nil
}
func (s *sshStubRunner) RunSSH(_ context.Context, _ Cluster, args []string) (string, string, error) {
	s.calls = append(s.calls, strings.Join(args, " "))
	return s.fn(args)
}
func (s *sshStubRunner) RunSSHStdin(_ context.Context, _ Cluster, args []string, stdin io.Reader) (string, string, error) {
	s.calls = append(s.calls, strings.Join(args, " "))
	s.stdin, _ = io.ReadAll(stdin)
	return s.fn(args)
}
func (s *sshStubRunner) RunSSHStream(_ context.Context, _ Cluster, args []string) (io.ReadCloser, error) {
	s.calls = append(s.calls, strings.Join(args, " "))
	out, _, err := s.fn(args)
	return io.NopCloser(strings.NewReader(out)), err
}

func TestSSHServerVersion(t *testing.T) {
	runner := &sshStubRunner{fn: func([]string) (string, string, error) {
		return `{"serverVersion":{"gitVersion":"v1.32.4"}}`, "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}
	v, err := rt.ServerVersion(context.Background())
	if err != nil || v != "v1.32.4" {
		t.Fatalf("version=%q err=%v", v, err)
	}
	if !strings.Contains(runner.calls[0], "version -o json") {
		t.Fatalf("call=%q", runner.calls[0])
	}
}

func TestSSHListMetrics(t *testing.T) {
	metricsJSON := `{"apiVersion":"metrics.k8s.io/v1beta1","kind":"NodeMetricsList","items":[]}`
	runner := &sshStubRunner{fn: func(args []string) (string, string, error) {
		return metricsJSON, "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}

	if _, err := rt.ListMetrics(context.Background(), "nodes", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.calls[0], "get --raw /apis/metrics.k8s.io/v1beta1/nodes") {
		t.Fatalf("nodes call=%q", runner.calls[0])
	}
	if _, err := rt.ListMetrics(context.Background(), "pods", "prod"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.calls[1], "/apis/metrics.k8s.io/v1beta1/namespaces/prod/pods") {
		t.Fatalf("pods call=%q", runner.calls[1])
	}
	if _, err := rt.ListMetrics(context.Background(), "pods", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runner.calls[2], "/apis/metrics.k8s.io/v1beta1/pods") {
		t.Fatalf("all-ns pods call=%q", runner.calls[2])
	}
	if _, err := rt.ListMetrics(context.Background(), "secrets", ""); err == nil {
		t.Fatal("arbitrary metrics resource must be rejected")
	}
}
