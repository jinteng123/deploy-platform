package kube

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func newFake(t *testing.T) *Client {
	t.Helper()
	replicas := int32(2)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "web-7b8c9",
			Namespace: "dev",
			Labels:    map[string]string{"app": "dev-web"},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "web", Image: "reg/web:v1"},
				{Name: "sidecar", Image: "reg/sidecar:v1"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "web", Ready: true, RestartCount: 2, Image: "reg/web:v1"},
				{Name: "sidecar", Ready: true, RestartCount: 3, Image: "reg/sidecar:v1"},
			},
		},
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "dev-web", Namespace: "dev"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "web", Image: "reg/web:v1", Ports: []corev1.ContainerPort{{ContainerPort: 8080}}},
						{Name: "sidecar", Image: "reg/sidecar:v1"},
					},
				},
			},
		},
		Status: appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2},
	}
	return NewWithClientset(fake.NewSimpleClientset(pod, dep), nil, nil)
}

func TestPods(t *testing.T) {
	c := newFake(t)
	pods, err := c.Pods(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 1 {
		t.Fatalf("pods=%d", len(pods))
	}
	p := pods[0]
	if p["name"] != "web-7b8c9" || p["status"] != "Running" || p["ready"] != "2/2" ||
		p["restarts"] != 5 || p["image"] != "reg/web:v1" || p["app_label"] != "dev-web" {
		t.Fatalf("pod summary = %+v", p)
	}
}

func TestPodLogs(t *testing.T) {
	c := newFake(t)
	logs, err := c.PodLogs(context.Background(), "dev", "web-7b8c9", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs, "fake logs") {
		t.Fatalf("logs=%q", logs)
	}
}

func TestRestartPod(t *testing.T) {
	c := newFake(t)
	if err := c.RestartPod(context.Background(), "dev", "web-7b8c9"); err != nil {
		t.Fatal(err)
	}
	pods, err := c.Pods(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 0 {
		t.Fatalf("pod still present: %+v", pods)
	}
	if err := c.RestartPod(context.Background(), "dev", "web-7b8c9"); err == nil {
		t.Fatal("expected error deleting missing pod")
	}
}

func TestDeploymentInfo(t *testing.T) {
	c := newFake(t)
	info, err := c.DeploymentInfo(context.Background(), "dev", "dev-web")
	if err != nil {
		t.Fatal(err)
	}
	if info["name"] != "dev-web" || info["namespace"] != "dev" {
		t.Fatalf("info=%+v", info)
	}
	if info["replicas"] != "2/2" || info["strategy"] != "RollingUpdate" || info["image"] != "reg/web:v1" {
		t.Fatalf("info=%+v", info)
	}
	ports, _ := info["ports"].([]any)
	if len(ports) != 1 {
		t.Fatalf("ports=%v", info["ports"])
	}
	if _, err := c.DeploymentInfo(context.Background(), "dev", "missing"); err == nil {
		t.Fatal("expected error for missing deployment")
	}
}

func TestCurrentImage(t *testing.T) {
	c := newFake(t)
	img, err := c.CurrentImage(context.Background(), "dev", "dev-web", "sidecar")
	if err != nil {
		t.Fatal(err)
	}
	if img != "reg/sidecar:v1" {
		t.Fatalf("image=%s", img)
	}
	if _, err := c.CurrentImage(context.Background(), "dev", "dev-web", "nope"); err == nil {
		t.Fatal("expected error for missing container")
	}
}

func TestSetImage(t *testing.T) {
	c := newFake(t)
	if err := c.SetImage(context.Background(), "dev", "dev-web", "web", "reg/web:v2"); err != nil {
		t.Fatal(err)
	}
	img, err := c.CurrentImage(context.Background(), "dev", "dev-web", "web")
	if err != nil {
		t.Fatal(err)
	}
	if img != "reg/web:v2" {
		t.Fatalf("image=%s", img)
	}
	// 未指定的 container 不变
	img, _ = c.CurrentImage(context.Background(), "dev", "dev-web", "sidecar")
	if img != "reg/sidecar:v1" {
		t.Fatalf("sidecar image changed: %s", img)
	}
	if err := c.SetImage(context.Background(), "dev", "dev-web", "ghost", "x"); err == nil {
		t.Fatal("expected error for unknown container")
	}
}

const testKubeconfigYAML = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
    token: t
`

func TestNewFromKubeconfigData(t *testing.T) {
	c, err := NewFromKubeconfigData([]byte(testKubeconfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || c.client == nil {
		t.Fatal("client not constructed")
	}
	if _, err := NewFromKubeconfigData([]byte("not yaml: [")); err == nil {
		t.Fatal("invalid kubeconfig must fail")
	}
}

func TestNewFromToken(t *testing.T) {
	if _, err := NewFromToken("", "tok", nil, false); err == nil {
		t.Fatal("empty api_server must fail")
	}
	if _, err := NewFromToken("https://k8s:6443", "", nil, false); err == nil {
		t.Fatal("empty token must fail")
	}
	c, err := NewFromToken("https://k8s:6443", "tok", nil, false)
	if err != nil || c == nil {
		t.Fatalf("valid token client err=%v", err)
	}
	// insecure must clear CA data or rest.Config validation rejects it
	if _, err := NewFromToken("https://k8s:6443", "tok", []byte("ca"), true); err != nil {
		t.Fatalf("insecure client with CA data err=%v", err)
	}
}

// TestForUser freezes the impersonation contract: ForUser clones the
// rest.Config with Impersonate set, while an empty identity returns the
// base client unchanged (CD worker path).
func TestForUser(t *testing.T) {
	base, err := NewFromToken("https://k8s:6443", "tok", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	imp, err := base.ForUser(Identity{Username: "dev-one", Groups: []string{"devs"}})
	if err != nil {
		t.Fatal(err)
	}
	if imp.cfg.Impersonate.UserName != "dev-one" || imp.cfg.Impersonate.Groups[0] != "devs" {
		t.Fatalf("impersonate = %+v", imp.cfg.Impersonate)
	}
	if imp.streamCfg.Impersonate.UserName != "dev-one" {
		t.Fatal("stream config must carry impersonation for exec/log streams")
	}
	if base.cfg.Impersonate.UserName != "" {
		t.Fatal("base config must not be mutated")
	}
	same, err := base.ForUser(Identity{})
	if err != nil || same != base {
		t.Fatal("empty identity must return the base client")
	}
}

func TestServerVersion(t *testing.T) {
	c := newFake(t)
	// fake clientset returns a fixed version via discovery
	v, err := c.ServerVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v == "" {
		t.Fatal("empty server version")
	}
}

func TestDeploymentEvents(t *testing.T) {
	c := newFake(t)
	out, err := c.DeploymentEvents(context.Background(), "dev", "dev-web")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "TYPE") || !strings.Contains(out, "REASON") || !strings.Contains(out, "MESSAGE") {
		t.Fatalf("events output missing columns: %q", out)
	}
}
