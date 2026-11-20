package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/kube"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

// recordingK8s is a KubernetesRuntime fake that records SetImage calls.
// rolloutErr, when set, makes RolloutStatus fail — exercises the pipeline's
// post-deploy health gate.
type recordingK8s struct {
	mu                           sync.Mutex
	setCalls                     []string
	rolloutErr                   error
	setImageErr                  error
	readErrAfterSet              error
	forcedImage                  *string
	templateImage                string
	trackSetImage                bool
	templateEnv                  string
	deliveryEnvFrom              []any
	deploymentServiceAccountName string
	deploymentImagePullSecrets   []any
	serviceAccountPullSecrets    []any
	serviceAccountErr            error
	configurationMetadata        map[string]metav1.PartialObjectMetadata
	configurationMetadataErrs    map[string]error
	readyOverride                *int64
	blueGreenHPA                 bool
	blueGreenExtraService        bool
	blueGreenListError           bool
	blueGreenQuotas              []unstructured.Unstructured
	blueGreenVolumes             []any
	blueGreenMatchExpressions    bool
	deploymentUID                string
	replacementUIDOnPatch        string
	accessRules                  []kube.ResourceRule
	accessRulesSet               bool
	accessRulesErr               error
}

func (r *recordingK8s) Pods(context.Context, string) ([]map[string]any, error) {
	return nil, nil
}
func (r *recordingK8s) PodLogs(context.Context, string, string, int64) (string, error) {
	return "", nil
}
func (r *recordingK8s) RestartPod(context.Context, string, string) error { return nil }
func (r *recordingK8s) DeploymentInfo(context.Context, string, string) (map[string]any, error) {
	return nil, nil
}
func (r *recordingK8s) DeploymentEvents(context.Context, string, string) (string, error) {
	return "", nil
}
func (r *recordingK8s) CurrentImage(_ context.Context, ns, deployment, container string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.setCalls) > 0 && r.readErrAfterSet != nil {
		return "", r.readErrAfterSet
	}
	if r.forcedImage != nil {
		return *r.forcedImage, nil
	}
	prefix := ns + "/" + deployment + "/" + container + "="
	for i := len(r.setCalls) - 1; i >= 0; i-- {
		if strings.HasPrefix(r.setCalls[i], prefix) {
			return strings.TrimPrefix(r.setCalls[i], prefix), nil
		}
	}
	return "reg.io/current:v0", nil
}
func (r *recordingK8s) SetImage(_ context.Context, ns, dep, container, image string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setCalls = append(r.setCalls, ns+"/"+dep+"/"+container+"="+image)
	if r.forcedImage != nil && r.trackSetImage {
		*r.forcedImage = image
	}
	return r.setImageErr
}
func (r *recordingK8s) RolloutStatus(_ context.Context, _, dep string) (string, error) {
	if r.rolloutErr != nil {
		return "", r.rolloutErr
	}
	return fmt.Sprintf("deployment %q successfully rolled out", dep), nil
}
func (r *recordingK8s) ListResources(ctx context.Context, def kube.ResourceDef, namespace string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if r.blueGreenListError {
		return nil, errors.New("forbidden")
	}
	list := &unstructured.UnstructuredList{}
	switch def.Resource {
	case "deployments":
		deployment, err := r.GetResource(ctx, def, namespace, "app-prod", metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		list.Items = append(list.Items, *deployment)
	case "services":
		list.Items = append(list.Items, *blueGreenService(namespace, "orders-entry"))
		if r.blueGreenExtraService {
			list.Items = append(list.Items, *blueGreenService(namespace, "metrics-entry"))
		}
	case "horizontalpodautoscalers":
		if r.blueGreenHPA {
			hpa := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "autoscaling/v1", "kind": "HorizontalPodAutoscaler",
				"metadata": map[string]any{"name": "app-scaler", "namespace": namespace},
				"spec":     map[string]any{"scaleTargetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "app-prod"}},
			}}
			list.Items = append(list.Items, *hpa)
		}
	case "resourcequotas":
		list.Items = append(list.Items, r.blueGreenQuotas...)
	case "replicasets":
		// No existing ReplicaSets beyond the Deployment's synthetic baseline.
	}
	return list, nil
}
func (r *recordingK8s) ServerVersion(context.Context) (string, error) { return "", nil }
func (r *recordingK8s) ListMetrics(context.Context, string, string) (*unstructured.UnstructuredList, error) {
	return nil, nil
}
func (r *recordingK8s) PatchResource(ctx context.Context, def kube.ResourceDef, namespace, name string, patchType types.PatchType, data []byte, _ metav1.PatchOptions) (*unstructured.Unstructured, error) {
	if def.Resource != "deployments" || patchType != types.JSONPatchType {
		return nil, nil
	}
	if r.replacementUIDOnPatch != "" {
		r.deploymentUID = r.replacementUIDOnPatch
		r.replacementUIDOnPatch = ""
	}
	current, err := r.GetResource(ctx, def, namespace, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var ops []struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &ops); err != nil {
		return nil, err
	}
	image := ""
	containers, _, _ := unstructured.NestedSlice(current.Object, "spec", "template", "spec", "containers")
	if len(containers) > 0 {
		image, _ = containers[0].(map[string]any)["image"].(string)
	}
	newImage := ""
	for _, op := range ops {
		if op.Op == "test" {
			actual := ""
			switch op.Path {
			case "/metadata/uid":
				actual = string(current.GetUID())
			case "/metadata/resourceVersion":
				actual = current.GetResourceVersion()
			case "/spec/template/spec/containers/0/name":
				actual = "app"
			case "/spec/template/spec/containers/0/image":
				actual = image
			default:
				return nil, fmt.Errorf("unexpected JSON Patch test path %q", op.Path)
			}
			if actual != op.Value {
				return nil, newConflict("JSON Patch precondition failed")
			}
		}
		if op.Op == "replace" && op.Path == "/spec/template/spec/containers/0/image" {
			newImage = op.Value
		}
	}
	if newImage == "" {
		return nil, fmt.Errorf("JSON Patch did not replace the image")
	}
	r.mu.Lock()
	r.templateImage = newImage
	r.setCalls = append(r.setCalls, namespace+"/"+name+"/app="+newImage)
	if r.forcedImage != nil && r.trackSetImage {
		*r.forcedImage = newImage
	}
	r.mu.Unlock()
	return r.GetResource(ctx, def, namespace, name, metav1.GetOptions{})
}
func (r *recordingK8s) DeleteResource(context.Context, kube.ResourceDef, string, string, metav1.DeleteOptions) error {
	return nil
}
func (r *recordingK8s) UpdateResource(context.Context, kube.ResourceDef, string, *unstructured.Unstructured, metav1.UpdateOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (r *recordingK8s) CreateResource(context.Context, kube.ResourceDef, string, *unstructured.Unstructured, metav1.CreateOptions) (*unstructured.Unstructured, error) {
	return nil, nil
}
func (r *recordingK8s) PodLogStream(context.Context, string, string, kube.LogOptions) (io.ReadCloser, error) {
	return nil, nil
}
func (r *recordingK8s) ExecPod(context.Context, string, string, kube.ExecSpec, kube.ExecIO) error {
	return nil
}
func (r *recordingK8s) AccessRules(context.Context, string) ([]kube.ResourceRule, error) {
	if r.accessRulesErr != nil {
		return nil, r.accessRulesErr
	}
	if r.accessRulesSet {
		return append([]kube.ResourceRule(nil), r.accessRules...), nil
	}
	return []kube.ResourceRule{{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}}}, nil
}

func (r *recordingK8s) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.setCalls...)
}

func ip64(v int64) *int64 { return &v }
func ip(v int) *int       { return &v }

// cdFixture builds a store with an automatic non-production cluster (env
// code "test-cluster") and a manual production cluster, each carrying the
// "app" mapping, plus a worker wired to the recording k8s runtime.
func cdFixture(t *testing.T) (config.Config, *Store, *MockRunner, *recordingK8s, *Worker, *CDService) {
	t.Helper()
	cfg := testConfig(t.TempDir())
	cfg.MockRuntime = false
	rec := &recordingK8s{}
	store := NewStoreWithData(
		[]Cluster{
			{ID: 1, Name: "测试", EnvCode: "test-cluster", Environment: "staging",
				EnvironmentTier: "non_production", DeployPolicy: "automatic", ConnectionType: "kubeconfig", Enabled: true},
			{ID: 2, Name: "生产", EnvCode: "prod", Environment: "prod",
				EnvironmentTier: "production", DeployPolicy: "manual", ConnectionType: "kubeconfig", Enabled: true},
		},
		[]App{
			{ID: 1, ClusterID: 1, AppName: "app", DeploymentName: "app-test", ContainerName: "app", Namespace: "ns-test"},
			{ID: 2, ClusterID: 2, AppName: "app", DeploymentName: "app-prod", ContainerName: "app", Namespace: "ns-prod"},
		},
		nil, nil, nil)
	model := store.DeliveryModel()
	for i := range model.Environments {
		if model.Environments[i].ID == 2 {
			model.Environments[i].Name = "生产"
			model.Environments[i].RequiresReview = false
		}
	}
	for i := range model.Applications {
		if model.Applications[i].ID == 2 {
			model.Applications[i].RequiresReview = false
		}
	}
	for i := range model.Components {
		if model.Components[i].ID == 2 {
			model.Components[i].RequiresReview = false
		}
	}
	for i := range model.Bindings {
		if model.Bindings[i].AppID == 2 {
			model.Bindings[i].RequiresReview = false
		}
	}
	store.LoadDeliveryModel(model)
	store.LoadUsers([]User{
		{ID: 1, Username: "alice", Role: RoleOperator, Enabled: true},
		{ID: 2, Username: "bob", Role: RoleOperator, Enabled: true},
		{ID: 3, Username: "carol", Role: RoleOperator, Enabled: true},
		{ID: 4, Username: "dave", Role: RoleOperator, Enabled: true},
		{ID: 5, Username: "test-approver", Role: RoleOperator, Enabled: true},
	})
	seedCDApprovalScopes(t, store, 2, 2)
	store.LoadDeliveryEnvironmentEvidencePolicyVersions([]DeliveryEnvironmentEvidencePolicyVersion{testEnvironmentEvidencePolicy(2)})
	runner := &MockRunner{}
	k8s := &K8sService{runner: runner, inject: rec}
	worker := NewWorker(NewPipeline(cfg, store, runner, k8s), 1)
	t.Cleanup(worker.Close)
	return cfg, store, runner, rec, worker, NewCDService(cfg, store, worker)
}

func cdEvent() ImageEvent {
	return ImageEvent{
		EventID:       "e-1",
		Source:        "custom",
		TargetCluster: "test-cluster",
		AppName:       "app",
		ImageFull:     "reg.io/Demo/app:v1.2.3",
		ImageDigest:   strPtr("sha256:" + strings.Repeat("12", 32)),
	}
}

func TestImageVersion(t *testing.T) {
	cases := map[string]string{
		"reg.io/Demo/app:v1.2.3":                               "v1.2.3",
		"reg.io:5000/Demo/app:build42":                         "build42",
		"reg.io/app@sha256:" + strings.Repeat("12", 32):       "121212121212",
		"registry.example.com/Demo/x:v9": "v9",
	}
	for image, want := range cases {
		got, err := ImageVersion(image)
		if err != nil || got != want {
			t.Errorf("ImageVersion(%q) = %q,%v want %q", image, got, err, want)
		}
	}
	for _, bad := range []string{"", "reg.io/app", "reg.io/app:", "reg.io/app@sha256:abc"} {
		if _, err := ImageVersion(bad); !errors.Is(err, ErrValidation) {
			t.Errorf("ImageVersion(%q) err=%v, want ErrValidation", bad, err)
		}
	}
}

func TestCDReceiveCreatesDeploysAndIsIdempotent(t *testing.T) {
	_, store, runner, rec, _, cd := cdFixture(t)

	release, deployLog, created, err := cd.Receive(context.Background(), cdEvent())
	if err != nil {
		t.Fatal(err)
	}
	if !created || release.ID == 0 || release.TestDeployLogID != deployLog.ID {
		t.Fatalf("created=%v release=%+v log=%+v", created, release, deployLog)
	}
	if release.Version != "v1.2.3" || release.Source != "custom" {
		t.Fatalf("release fields wrong: %+v", release)
	}
	if deployLog.DeployType != "image_deploy" || deployLog.FileName != "registry" || deployLog.ClusterID != 1 {
		t.Fatalf("test log wrong: %+v", deployLog)
	}
	if deployLog.DeploymentName != "app-test" || deployLog.Namespace != "ns-test" {
		t.Fatalf("test app mapping not applied: %+v", deployLog)
	}

	// 流水线真实跑完：image_deploy 只做 k8s_set_image，无 docker/file 动作
	finished := waitLog(t, store, deployLog.ID)
	if finished.Status != "success" {
		t.Fatalf("test deploy failed: %+v", finished)
	}
	if got := rec.recorded(); len(got) != 1 || got[0] != "ns-test/app-test/app=reg.io/Demo/app@sha256:"+strings.Repeat("12", 32)+"" {
		t.Fatalf("SetImage calls = %v", got)
	}
	for _, call := range runner.Recorded() {
		t.Fatalf("image_deploy must not invoke docker/ssh runner: %s", call)
	}
	steps := mustStepDetail(t, finished)
	if len(steps) != 4 || steps[0].Step != "k8s_set_image" || steps[1].Step != "rollout_wait" || !steps[1].Success || steps[2].Step != "verify_image" || !steps[2].Success || steps[3].Step != "verify_pod_image" || steps[3].ObservationStatus != "unknown" {
		t.Fatalf("steps = %+v", steps)
	}

	// 重复事件幂等：不再创建第二个任务
	dup, dupLog, created, err := cd.Receive(context.Background(), cdEvent())
	if err != nil {
		t.Fatal(err)
	}
	if created || dup.ID != release.ID || dupLog.ID != deployLog.ID {
		t.Fatalf("duplicate receive: created=%v dup=%+v log=%+v", created, dup, dupLog)
	}
	if n := len(store.Logs(0, "", "", 0)); n != 1 {
		t.Fatalf("duplicate created extra log, total=%d", n)
	}
}

// TestCDImageDeployRolloutFailure proves `set image` succeeding is not the
// end: a rollout that never becomes Ready (ImagePullBackOff, deadline
// exceeded…) must surface as a failed deploy, not a false "success".
func TestCDImageDeployRolloutFailure(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	rec.rolloutErr = errors.New("rollout 等待超时或取消: Waiting for deployment \"app-test\" rollout to finish: 0 of 1 updated replicas are available...")

	_, deployLog, _, err := cd.Receive(context.Background(), cdEvent())
	if err != nil {
		t.Fatal(err)
	}
	l := waitLog(t, store, deployLog.ID)
	if l.Status != "failed" {
		t.Fatalf("status=%s", l.Status)
	}
	if l.ErrorMessage == nil || !strings.Contains(*l.ErrorMessage, "rollout_wait") {
		t.Fatalf("error_message=%v", l.ErrorMessage)
	}
	steps := mustStepDetail(t, l)
	if len(steps) != 2 || steps[0].Step != "k8s_set_image" || !steps[0].Success ||
		steps[1].Step != "rollout_wait" || steps[1].Success {
		t.Fatalf("steps=%+v", steps)
	}
	// SetImage did run — the failure is strictly post-write health.
	if got := rec.recorded(); len(got) != 1 {
		t.Fatalf("SetImage calls=%v", got)
	}
}

func TestCDImageDeployConfirmsWriteWhenSetImageResponseIsLost(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	current := "reg.io/Demo/app:old"
	rec.forcedImage = &current
	rec.trackSetImage = true
	rec.setImageErr = errors.New("connection reset after request")

	_, deployLog, _, err := cd.Receive(context.Background(), cdEvent())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitLog(t, store, deployLog.ID)
	if finished.Status != "success" {
		t.Fatalf("status=%s error=%v", finished.Status, finished.ErrorMessage)
	}
	if calls := rec.recorded(); len(calls) != 1 {
		t.Fatalf("SetImage calls=%v, want exactly one attempted write", calls)
	}
	actions, err := store.ExecutionActions(deployLog.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != ExecutionActionApplied || actions[0].OutcomeCode != "target_confirmed" {
		t.Fatalf("action journal=%+v err=%v", actions, err)
	}
}

func TestCDImageDeployStopsWhenReadbackIsUnknown(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	current := "reg.io/Demo/app:old"
	rec.forcedImage = &current
	rec.trackSetImage = true
	rec.readErrAfterSet = errors.New("cluster unavailable")

	_, deployLog, _, err := cd.Receive(context.Background(), cdEvent())
	if err != nil {
		t.Fatal(err)
	}
	finished := waitLog(t, store, deployLog.ID)
	if finished.Status != "interrupted" {
		t.Fatalf("status=%s error=%v, want interrupted", finished.Status, finished.ErrorMessage)
	}
	if calls := rec.recorded(); len(calls) != 1 {
		t.Fatalf("SetImage calls=%v, want one write and no blind retry", calls)
	}
	actions, err := store.ExecutionActions(deployLog.ID)
	if err != nil || len(actions) != 1 || actions[0].Status != ExecutionActionUnknown || actions[0].OutcomeCode != "readback_failed" {
		t.Fatalf("action journal=%+v err=%v", actions, err)
	}
}

func TestCDReceiveValidation(t *testing.T) {
	_, _, _, _, _, cd := cdFixture(t)
	for _, ev := range []ImageEvent{
		{TargetCluster: "test-cluster", AppName: "app", ImageFull: "r/a:v1"},                 // 缺 event_id
		{TargetCluster: "test-cluster", EventID: "e", ImageFull: "r/a:v1"},                   // 缺 app_name
		{TargetCluster: "test-cluster", EventID: "e", AppName: "app"},                        // 缺 image_full
		{TargetCluster: "test-cluster", EventID: "e", AppName: "app", ImageFull: "r/a: v1"},  // image 含空白
		{TargetCluster: "test-cluster", EventID: "e", AppName: "app", ImageFull: "r/a"},      // 无版本可解析
		{TargetCluster: "test-cluster", EventID: "e", AppName: "ghost", ImageFull: "r/a:v1"}, // 应用未登记
	} {
		if _, _, _, err := cd.Receive(context.Background(), ev); err == nil {
			t.Fatalf("event %+v accepted", ev)
		}
	}
}

func TestCDReceiveTargetClusterRules(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)

	// missing target_cluster
	ev := cdEvent()
	ev.TargetCluster = ""
	if _, _, _, err := cd.Receive(context.Background(), ev); !errors.Is(err, ErrValidation) {
		t.Fatalf("missing target err=%v", err)
	}

	// unknown env_code
	ev.TargetCluster = "ghost"
	if _, _, _, err := cd.Receive(context.Background(), ev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown target err=%v", err)
	}

	// disabled cluster
	store.clusters[0].Enabled = false
	if _, _, _, err := cd.Receive(context.Background(), cdEvent()); !errors.Is(err, ErrValidation) {
		t.Fatalf("disabled target err=%v", err)
	}
	store.clusters[0].Enabled = true

	// production cluster is not a valid webhook target
	ev.TargetCluster = "prod"
	if _, _, _, err := cd.Receive(context.Background(), ev); !errors.Is(err, ErrValidation) {
		t.Fatalf("production target err=%v", err)
	}

	// manual non-production cluster is not a valid webhook target
	_, _ = store.SaveCluster(Cluster{ID: 9, Name: "手动测试", EnvCode: "manual-env",
		EnvironmentTier: "non_production", DeployPolicy: "manual", ConnectionType: "kubeconfig", Enabled: true})
	ev.TargetCluster = "manual-env"
	if _, _, _, err := cd.Receive(context.Background(), ev); !errors.Is(err, ErrValidation) {
		t.Fatalf("manual target err=%v", err)
	}

	// automatic non-production cluster without the app mapping
	_, _ = store.SaveCluster(Cluster{ID: 10, Name: "空集群", EnvCode: "empty-env",
		EnvironmentTier: "non_production", DeployPolicy: "automatic", ConnectionType: "kubeconfig", Enabled: true})
	ev.TargetCluster = "empty-env"
	if _, _, _, err := cd.Receive(context.Background(), ev); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no app mapping err=%v", err)
	}
}

func TestEventKeyDiffersPerTarget(t *testing.T) {
	a := EventKey("custom", "e-1", "cluster-a")
	b := EventKey("custom", "e-1", "cluster-b")
	c := EventKey("custom", "e-1", "cluster-a")
	if a == b || a != c || len(a) != 64 {
		t.Fatalf("event keys: a=%s b=%s c=%s", a, b, c)
	}
}

func TestCDReceiveMissingCluster(t *testing.T) {
	cfg := testConfig(t.TempDir())
	store := NewStoreWithData(nil, nil, nil, nil, nil)
	runner := &MockRunner{}
	k8s := NewK8sService(cfg, runner, nil)
	worker := NewWorker(NewPipeline(cfg, store, runner, k8s), 1)
	t.Cleanup(worker.Close)
	cd := NewCDService(cfg, store, worker)

	if _, _, _, err := cd.Receive(context.Background(), cdEvent()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target cluster err=%v", err)
	}
}

func seedRelease(store *Store, testStatus string, prodStatus *string) ImageRelease {
	digest := "sha256:" + strings.Repeat("12", 32)
	image := "reg.io/Demo/app@" + digest
	testLog, _ := store.AddLog(DeployLog{ClusterID: 1, AppName: "app", DeploymentName: "app-test", ContainerName: "app", Namespace: "ns-test", ImageFull: &image, Status: testStatus})
	release := ImageRelease{
		EventKey: "k", EventID: "e", Source: "custom", AppName: "app",
		ImageFull: "reg.io/Demo/app:v1", ImageDigest: &digest, Version: "v1", TestClusterID: 1,
		Metadata: ReleaseMetadata{DigestSource: "ci", Verification: &VerificationEvidence{Digest: digest, Status: "passed", Kind: "ci", RecordedBy: "apikey:ci", RecordedAt: time.Now(), ValidUntil: time.Now().Add(time.Hour)}},
	}
	if prodStatus != nil {
		prodLog, _ := store.AddLog(DeployLog{ClusterID: 2, AppName: "app", DeploymentName: "app-prod", ContainerName: "app", Namespace: "ns-prod", Status: *prodStatus})
		release.ProdClusterID = ip(2)
		release.ProdDeployLogID = ip64(prodLog.ID)
	}
	// 直接操作内存避免走幂等逻辑
	release.TestDeployLogID = testLog.ID
	release.ID = 1
	return release
}

func TestCDPromoteGates(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)

	// 测试未成功 → validation
	pending := seedRelease(store, "pending", nil)
	store.releases = append(store.releases, pending)
	if _, _, err := promoteForTest(t, cd, pending.ID, 2); !errors.Is(err, ErrValidation) {
		t.Fatalf("promote with pending test err=%v", err)
	}

	// 目标集群不存在
	orphanStore := NewStoreWithData(
		[]Cluster{{ID: 1, EnvCode: "test"}},
		[]App{{ID: 1, ClusterID: 1, AppName: "app", DeploymentName: "d", ContainerName: "c", Namespace: "n"}},
		nil, nil, nil)
	orphanRelease := seedRelease(orphanStore, "success", nil)
	orphanStore.releases = append(orphanStore.releases, orphanRelease)
	orphanCD := NewCDService(config.Config{}, orphanStore, nil)
	if _, _, err := orphanCD.Promote(context.Background(), orphanRelease.ID, 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing target cluster err=%v", err)
	}

	// 目标集群为 production+manual 但未登记 app 映射
	noAppStore := NewStoreWithData(
		[]Cluster{
			{ID: 1, EnvCode: "test"},
			{ID: 2, EnvCode: "prod", EnvironmentTier: "production", DeployPolicy: "manual", ConnectionType: "kubeconfig", Enabled: true},
		},
		[]App{{ID: 1, ClusterID: 1, AppName: "app", DeploymentName: "d", ContainerName: "c", Namespace: "n"}},
		nil, nil, nil)
	noAppRelease := seedRelease(noAppStore, "success", nil)
	noAppStore.releases = append(noAppStore.releases, noAppRelease)
	noAppCD := NewCDService(config.Config{}, noAppStore, nil)
	if _, _, err := noAppCD.Promote(context.Background(), noAppRelease.ID, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing prod app err=%v", err)
	}

	// 目标必须 enabled + production + manual
	seeded := seedRelease(store, "success", nil)
	seeded.ID = 300
	store.releases = append(store.releases, seeded)
	if _, _, err := promoteForTest(t, cd, seeded.ID, 1); !errors.Is(err, ErrValidation) {
		t.Fatalf("non-production target err=%v", err)
	}
	_, _ = store.SaveCluster(Cluster{ID: 20, Name: "自动生产", EnvCode: "auto-prod",
		EnvironmentTier: "production", DeployPolicy: "automatic", ConnectionType: "kubeconfig", Enabled: true})
	if _, _, err := promoteForTest(t, cd, seeded.ID, 20); !errors.Is(err, ErrValidation) {
		t.Fatalf("automatic prod target err=%v", err)
	}
	_, _ = store.SaveCluster(Cluster{ID: 21, Name: "停用生产", EnvCode: "off-prod",
		EnvironmentTier: "production", DeployPolicy: "manual", ConnectionType: "kubeconfig", Enabled: false})
	if _, _, err := promoteForTest(t, cd, seeded.ID, 21); !errors.Is(err, ErrValidation) {
		t.Fatalf("disabled prod target err=%v", err)
	}

	// 生产 pending/success → conflict；failed → 允许重试
	for _, st := range []string{"pending", "running", "success"} {
		blocked := seedRelease(store, "success", &st)
		blocked.ID = int64(100 + len(store.releases))
		store.releases = append(store.releases, blocked)
		if _, _, err := promoteForTest(t, cd, blocked.ID, 2); !errors.Is(err, ErrConflict) {
			t.Fatalf("prod status=%s err=%v, want conflict", st, err)
		}
	}
	// 上面用作"进行中"断言的 prod 日志落为终态——互斥语义下同
	// app+cluster 必须先排空才能再次发布
	for _, l := range store.Logs(0, "", "", 0) {
		if l.ClusterID == 2 && (l.Status == "pending" || l.Status == "running") {
			l.Status = "failed"
			if err := store.UpdateLogExecution(l); err != nil {
				t.Fatal(err)
			}
		}
	}
	failed := "failed"
	retry := seedRelease(store, "success", &failed)
	retry.ID = 200
	store.releases = append(store.releases, retry)
	gotRelease, prodLog, err := promoteForTest(t, cd, retry.ID, 2)
	if err != nil {
		t.Fatalf("failed prod must be retryable: %v", err)
	}
	if *gotRelease.ProdDeployLogID != prodLog.ID || prodLog.ClusterID != 2 {
		t.Fatalf("release not relinked to new prod log: %+v log=%+v", gotRelease, prodLog)
	}
	if prodLog.ImageFull == nil || *prodLog.ImageFull != "reg.io/Demo/app@sha256:"+strings.Repeat("12", 32) || prodLog.Version != "v1" {
		t.Fatalf("prod must reuse the original image_full: %+v", prodLog)
	}
	if prodLog.DeployType != "image_deploy" || prodLog.DeploymentName != "app-prod" || prodLog.Namespace != "ns-prod" {
		t.Fatalf("prod app mapping not applied: %+v", prodLog)
	}
	waitLog(t, store, prodLog.ID)
	if got := rec.recorded(); len(got) == 0 {
		t.Fatal("prod SetImage not invoked")
	} else if last := got[len(got)-1]; last != "ns-prod/app-prod/app=reg.io/Demo/app@sha256:"+strings.Repeat("12", 32) {
		t.Fatalf("prod SetImage = %s", last)
	}
}

func TestCDListViews(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)

	release, _, _, err := cd.Receive(context.Background(), cdEvent())
	if err != nil {
		t.Fatal(err)
	}
	waitLog(t, store, release.TestDeployLogID)
	attestServiceRelease(t, store, release.ID)

	views := cd.List("", 50)
	if len(views) != 1 {
		t.Fatalf("views=%d", len(views))
	}
	v := views[0]
	if v.TestStatus != "success" || !v.CanPromote || v.ProdStatus != nil {
		t.Fatalf("view=%+v", v)
	}
	if v.AppName != "app" || v.ImageFull != "reg.io/Demo/app:v1.2.3" {
		t.Fatalf("view missing metadata: %+v", v)
	}

	if _, _, err := promoteForTest(t, cd, release.ID, 2); err != nil {
		t.Fatal(err)
	}
	r, _ := store.FindImageRelease(release.ID)
	waitLog(t, store, *r.ProdDeployLogID)
	v = cd.List("", 50)[0]
	if v.ProdStatus == nil || *v.ProdStatus != "success" || v.CanPromote {
		t.Fatalf("after promote view=%+v", v)
	}
	if got := cd.List("other-app", 50); len(got) != 0 {
		t.Fatalf("app filter broken: %d", len(got))
	}
}

func TestPromotionTargets(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)

	release, _, _, err := cd.Receive(context.Background(), cdEvent())
	if err != nil {
		t.Fatal(err)
	}

	// prod cluster without the app mapping must be excluded; disabled and
	// non-manual clusters too.
	_, _ = store.SaveCluster(Cluster{ID: 30, Name: "无映射生产", EnvCode: "prod2",
		EnvironmentTier: "production", DeployPolicy: "manual", ConnectionType: "kubeconfig", Enabled: true})
	_, _ = store.SaveCluster(Cluster{ID: 31, Name: "停用生产", EnvCode: "prod3",
		EnvironmentTier: "production", DeployPolicy: "manual", ConnectionType: "kubeconfig", Enabled: false})

	targets, err := cd.PromotionTargets(release.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != 2 {
		t.Fatalf("targets=%+v", targets)
	}
	if targets[0].CredentialCiphertext != nil {
		t.Fatal("targets must not carry ciphertext")
	}

	// unknown release → not found
	if _, err := cd.PromotionTargets(9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown release err=%v", err)
	}
}

func TestNormalizeDigestAndRef(t *testing.T) {
	bare := strings.Repeat("ab", 32)
	if got := NormalizeDigest(bare); got != "sha256:"+bare {
		t.Fatalf("bare hex → %s", got)
	}
	if got := NormalizeDigest("SHA256:" + strings.ToUpper(bare)); got != "sha256:"+bare {
		t.Fatalf("upper → %s", got)
	}
	for _, bad := range []string{"", "  ", "sha256:xyz", "sha256:", "zz"} {
		if got := NormalizeDigest(bad); got != "" {
			t.Fatalf("NormalizeDigest(%q) = %q, want empty", bad, got)
		}
	}
	d := "sha256:" + strings.Repeat("cd", 32)
	if got := DigestRef("reg.io/Demo/app:v1", d); got != "reg.io/Demo/app@"+d {
		t.Fatalf("tag ref → %s", got)
	}
	if got := DigestRef("reg.io/Demo/app@sha256:"+strings.Repeat("00", 32), d); got != "reg.io/Demo/app@"+d {
		t.Fatalf("digest ref re-pin → %s", got)
	}
	if got := DigestRef("reg.io/Demo/app:v1", "bogus"); got != "" {
		t.Fatalf("bad digest → %q, want empty", got)
	}
}

func TestCDReceiveDeploysDigestRef(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	ev := cdEvent()
	d := "sha256:" + strings.Repeat("ef", 32)
	ev.ImageDigest = &d
	release, deployLog, created, err := cd.Receive(context.Background(), ev)
	if err != nil || !created {
		t.Fatalf("Receive err=%v created=%v", err, created)
	}
	if deployLog.ImageFull == nil || *deployLog.ImageFull != "reg.io/Demo/app@"+d {
		t.Fatalf("digest event must deploy repo@digest: %+v", deployLog.ImageFull)
	}
	waitLog(t, store, deployLog.ID)
	if got, _ := store.FindImageRelease(release.ID); got.ImageDigest == nil || *got.ImageDigest != d {
		t.Fatalf("release digest not stored: %+v", got.ImageDigest)
	}
}

func TestPromoteDeploysDigestRef(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	d := "sha256:" + strings.Repeat("12", 32)
	rel := seedRelease(store, "success", nil)
	rel.ID = 900
	rel.ImageDigest = &d
	store.releases = append(store.releases, rel)

	_, prodLog, err := promoteForTest(t, cd, rel.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if prodLog.ImageFull == nil || *prodLog.ImageFull != "reg.io/Demo/app@"+d {
		t.Fatalf("prod must deploy the pinned digest: %+v", prodLog.ImageFull)
	}
	waitLog(t, store, prodLog.ID)
	got := rec.recorded()
	if last := got[len(got)-1]; last != "ns-prod/app-prod/app=reg.io/Demo/app@"+d {
		t.Fatalf("prod SetImage = %s", last)
	}
}

func TestPromotionApprovalFlow(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	rel := seedRelease(store, "success", nil)
	rel.ID = 700
	store.releases = append(store.releases, rel)

	req, err := cd.RequestPromotion(rel.ID, 2, "alice")
	if err != nil {
		t.Fatalf("request err=%v", err)
	}
	if req.Status != "pending" || req.ExpiresAt == nil {
		t.Fatalf("request=%+v", req)
	}
	// 同 release+cluster 的第二个 pending → 冲突
	if _, err := cd.RequestPromotion(rel.ID, 2, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate pending err=%v, want conflict", err)
	}
	// 发起人不能自审
	if _, _, err := cd.ApprovePromotion(context.Background(), req.ID, "alice"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("self-approve err=%v, want forbidden", err)
	}
	// 第二人批准 → 部署入队
	got, prodLog, err := cd.ApprovePromotion(context.Background(), req.ID, "bob")
	if err != nil {
		t.Fatalf("approve err=%v", err)
	}
	if got.Status != "approved" || got.Approver == nil || *got.Approver != "bob" ||
		got.DeployLogID == nil || *got.DeployLogID != prodLog.ID {
		t.Fatalf("approved request wrong: %+v", got)
	}
	waitLog(t, store, prodLog.ID)
	// 已决请求不可再操作
	if _, _, err := cd.ApprovePromotion(context.Background(), req.ID, "carol"); !errors.Is(err, ErrValidation) {
		t.Fatalf("re-approve err=%v, want validation", err)
	}
	if _, err := cd.RejectPromotion(req.ID, "carol", "x"); !errors.Is(err, ErrValidation) {
		t.Fatalf("reject decided err=%v, want validation", err)
	}
}

func TestPromotionRejectAndExpiry(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	rel := seedRelease(store, "success", nil)
	rel.ID = 701
	store.releases = append(store.releases, rel)

	// 发起人自己关闭 → cancelled
	req, err := cd.RequestPromotion(rel.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	got, err := cd.RejectPromotion(req.ID, "alice", "")
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("withdraw status=%s err=%v", got.Status, err)
	}
	// 他人拒绝 → rejected + comment 落库
	req, err = cd.RequestPromotion(rel.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	got, err = cd.RejectPromotion(req.ID, "dave", " 风险未评估 ")
	if err != nil || got.Status != "rejected" || got.Comment == nil || *got.Comment != "风险未评估" {
		t.Fatalf("reject status=%s comment=%v err=%v", got.Status, got.Comment, err)
	}
	// 过期请求：批准时标记 expired 且拒绝执行
	past := time.Now().Add(-time.Hour)
	exp, err := store.CreatePromotionRequest(PromotionRequest{
		ReleaseID: rel.ID, ProdClusterID: 2, Requester: "alice", ExpiresAt: &past,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cd.ApprovePromotion(context.Background(), exp.ID, "bob"); !errors.Is(err, ErrValidation) {
		t.Fatalf("expired approve err=%v", err)
	}
	if got, _ := store.FindPromotionRequest(exp.ID); got.Status != "expired" {
		t.Fatalf("expired request status=%s", got.Status)
	}
	// 拒绝后可再次发起
	if _, err := cd.RequestPromotion(rel.ID, 2, "alice"); err != nil {
		t.Fatalf("re-request after decision err=%v", err)
	}
}

func TestPromotionRequestConcurrentFiling(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	rel := seedRelease(store, "success", nil)
	rel.ID = 702
	store.releases = append(store.releases, rel)

	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := cd.RequestPromotion(rel.ID, 2, fmt.Sprintf("u%d", i))
			errs <- err
		}(i)
	}
	var conflicts, created int
	for i := 0; i < n; i++ {
		err := <-errs
		if err == nil {
			created++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		}
	}
	if created != 1 || conflicts != n-1 {
		t.Fatalf("created=%d conflicts=%d, want 1/%d", created, conflicts, n-1)
	}
}

func attestServiceRelease(t *testing.T, store *Store, id int64) {
	t.Helper()
	r, err := store.FindImageRelease(id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateReleaseEvidence(id, VerificationEvidence{Digest: *r.ImageDigest, Status: "passed", Kind: "ci", RecordedBy: "apikey:ci", ValidUntil: time.Now().Add(time.Hour)}, false)
	if err != nil {
		t.Fatal(err)
	}
}

func deliveryModelForPlan(strategy string) DeliveryModel {
	serviceName := ""
	if strategy == "blue_green" {
		serviceName = "orders-entry"
	}
	return DeliveryModel{
		Environments: []DeliveryEnvironment{
			{ID: 11, Code: "test", Name: "测试", Tier: "non_production"},
			{ID: 12, Code: "prod", Name: "生产", Tier: "production"},
		},
		Applications: []DeliveryApplication{{ID: 21, Name: "订单系统"}},
		Components:   []DeliveryComponent{{ID: 31, ApplicationID: 21, Name: "orders-api"}},
		Bindings: []DeliveryBinding{
			{ID: 41, AppID: 1, ApplicationID: 21, ComponentID: 31, EnvironmentID: 11},
			{ID: 42, AppID: 2, ApplicationID: 21, ComponentID: 31, EnvironmentID: 12, StrategyDraft: strategy, ServiceName: serviceName},
		},
	}
}

func seedCDApprovalScopes(t *testing.T, store *Store, applicationID, environmentID int64) {
	t.Helper()
	for _, userID := range []int64{1, 2, 3, 4, 5} {
		if _, err := store.ReplaceDeliveryApprovalGrants(userID, []DeliveryApprovalScope{{ApplicationID: applicationID, EnvironmentID: environmentID}}, "test-admin"); err != nil {
			t.Fatalf("seed approval grant for user %d: %v", userID, err)
		}
	}
}

func testEnvironmentEvidencePolicy(environmentID int64) DeliveryEnvironmentEvidencePolicyVersion {
	return DeliveryEnvironmentEvidencePolicyVersion{
		EnvironmentID: environmentID, Version: 1,
		RequiredVerificationKinds: []string{"ci"}, MaxEvidenceAgeMinutes: MaxEvidenceFreshnessMinutes,
		TrustedSubmitters: map[string][]string{"ci": {"apikey:ci"}}, CreatedBy: "test", CreatedAt: time.Now(),
	}
}

func seedPlanRelease(t *testing.T, store *Store, rec *recordingK8s, strategy string) ImageRelease {
	t.Helper()
	release := seedRelease(store, "success", nil)
	store.releases = append(store.releases, release)
	store.LoadDeliveryModel(deliveryModelForPlan(strategy))
	seedCDApprovalScopes(t, store, 21, 12)
	store.LoadDeliveryEnvironmentEvidencePolicyVersions([]DeliveryEnvironmentEvidencePolicyVersion{testEnvironmentEvidencePolicy(12)})
	currentImage := "reg.io/app:current"
	rec.forcedImage = &currentImage
	return release
}

func TestRollingDeliveryPlanRequiresAcknowledgementForExternalConfiguration(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	rec.deliveryEnvFrom = []any{
		map[string]any{"configMapRef": map[string]any{"name": "orders-runtime-v3"}},
		map[string]any{"secretRef": map[string]any{"name": "orders-runtime-secret"}},
	}
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	refs := plan.Snapshot.Target.ConfigurationReferences
	if plan.Snapshot.SchemaVersion != 12 || len(refs) != 2 || refs[0] != (DeliveryConfigurationReference{Kind: "ConfigMap", Name: "orders-runtime-v3", UID: "uid-configmaps-orders-runtime-v3", ResourceVersion: "1"}) || refs[1] != (DeliveryConfigurationReference{Kind: "Secret", Name: "orders-runtime-secret", UID: "uid-secrets-orders-runtime-secret", ResourceVersion: "1"}) {
		t.Fatalf("external configuration references were not pinned: schema=%d refs=%+v", plan.Snapshot.SchemaVersion, refs)
	}
	var configurationCheck *DeliveryPlanCheck
	for i := range plan.Snapshot.Checks {
		if plan.Snapshot.Checks[i].Key == "configuration-snapshot" {
			configurationCheck = &plan.Snapshot.Checks[i]
			break
		}
	}
	if configurationCheck == nil || configurationCheck.Status != "review" || !strings.Contains(configurationCheck.Detail, "UID/resourceVersion") || !strings.Contains(configurationCheck.Detail, "不采集配置值") {
		t.Fatalf("configuration limitations were not explicit: %+v", configurationCheck)
	}
	var sourceCheck *DeliveryPlanCheck
	for i := range plan.Snapshot.Checks {
		if plan.Snapshot.Checks[i].Key == "configuration-source" {
			sourceCheck = &plan.Snapshot.Checks[i]
			break
		}
	}
	if sourceCheck == nil || sourceCheck.Status != "unknown" || !strings.Contains(sourceCheck.Detail, "未上报") {
		t.Fatalf("missing configuration Git revision was not reported as unknown: %+v", sourceCheck)
	}
	if _, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("rolling approval without configuration review error=%v, want validation", err)
	}
	request, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true)
	if err != nil || request.DeliveryPlanID == nil || *request.DeliveryPlanID != plan.ID {
		t.Fatalf("acknowledged rolling plan approval=%+v error=%v", request, err)
	}
}

func TestDeliveryPlanPinsRunnerReportedConfigurationRevision(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	source := &ConfigurationSourceRevision{
		Repository: "https://git.example.com/orders/deploy-config.git", Path: "charts/orders",
		Revision: strings.Repeat("a", 40), RecordedBy: "runner:4", RecordedAt: time.Now().UTC(),
	}
	for i := range store.releases {
		if store.releases[i].ID == release.ID {
			store.releases[i].Metadata.ConfigurationSource = source
		}
	}
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	pinned := plan.Snapshot.Artifact.ConfigurationSource
	if plan.Snapshot.SchemaVersion != 12 || pinned == nil || pinned.Repository != source.Repository || pinned.Path != source.Path || pinned.Revision != source.Revision || pinned.RecordedBy != source.RecordedBy {
		t.Fatalf("configuration Git source was not fixed to schema v11 snapshot: %+v", plan.Snapshot.Artifact)
	}
	if plan.Snapshot.Target.StableConfigurationSource != nil {
		t.Fatalf("candidate config source must not be used as the stable recovery source: %+v", plan.Snapshot.Target.StableConfigurationSource)
	}
	var sourceCheck *DeliveryPlanCheck
	for i := range plan.Snapshot.Checks {
		if plan.Snapshot.Checks[i].Key == "configuration-source" {
			sourceCheck = &plan.Snapshot.Checks[i]
			break
		}
	}
	if sourceCheck == nil || sourceCheck.Status != "review" || !strings.Contains(sourceCheck.Detail, pinned.Revision) || !strings.Contains(sourceCheck.Detail, "不能据此恢复") {
		t.Fatalf("reported configuration revision lacks review limitations: %+v", sourceCheck)
	}
	source.Revision = strings.Repeat("b", 40)
	if plan.Snapshot.Artifact.ConfigurationSource.Revision != strings.Repeat("a", 40) {
		t.Fatal("plan snapshot aliases mutable release configuration metadata")
	}
}

func TestDeliveryPlanPinsStableConfigurationSourceAndDeploymentPipeline(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	candidate := seedPlanRelease(t, store, rec, "rolling")
	candidate.ID = 3
	store.releases[0].ID = candidate.ID
	candidateSource := &ConfigurationSourceRevision{
		Repository: "https://git.example.com/orders/deploy-config.git", Path: "charts/orders",
		Revision: strings.Repeat("a", 40), RecordedBy: "runner:4", RecordedAt: time.Now().UTC(),
	}
	stableSource := &ConfigurationSourceRevision{
		Repository: "https://git.example.com/orders/deploy-config.git", Path: "charts/orders",
		Revision: strings.Repeat("b", 40), RecordedBy: "runner:5", RecordedAt: time.Now().UTC(),
	}
	for i := range store.releases {
		if store.releases[i].ID == candidate.ID {
			store.releases[i].Metadata.ConfigurationSource = candidateSource
		}
	}
	stable := candidate
	stable.ID = 2
	stable.ImageFull = "reg.io/Demo/app:stable-v2"
	stableDigest := "sha256:" + strings.Repeat("34", 32)
	stable.ImageDigest = &stableDigest
	stable.Metadata.ConfigurationSource = stableSource
	store.releases = append(store.releases, stable)
	currentImage := deployImage(stable)
	rec.forcedImage = &currentImage
	rec.templateImage = currentImage
	if _, err := store.AddLog(DeployLog{
		ClusterID: 2, AppName: "app", DeploymentName: "app-prod", ContainerName: "app", Namespace: "ns-prod",
		ImageFull: &currentImage, ReleaseID: stable.ID, Status: "success",
	}); err != nil {
		t.Fatalf("seed stable deployment log: %v", err)
	}
	model := store.DeliveryModel()
	model.Bindings[1].DeploymentPipeline = &YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "123456"}
	store.LoadDeliveryModel(model)
	review, err := store.RecordDeliveryPipelineScopeReview(DeliveryPipelineScopeReview{
		BindingID: model.Bindings[1].ID, ConnectionID: 9, PipelineID: "123456",
		DefinitionHash: strings.Repeat("d", 64), WriteScope: PipelineScopeConfigurationOnly, Reviewer: "operator",
	})
	if err != nil {
		t.Fatalf("record scope review: %v", err)
	}

	plan, _, err := cd.CreateDeliveryPlan(context.Background(), candidate.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	target := plan.Snapshot.Target
	if target.StableReleaseID != stable.ID || target.StableConfigurationSource == nil || target.StableConfigurationSource.Revision != stableSource.Revision {
		t.Fatalf("stable source was not resolved from the live production release: %+v", target)
	}
	if plan.Snapshot.Artifact.ConfigurationSource == nil || plan.Snapshot.Artifact.ConfigurationSource.Revision != candidateSource.Revision || target.StableConfigurationSource.Revision == plan.Snapshot.Artifact.ConfigurationSource.Revision {
		t.Fatalf("candidate and stable config revisions were not kept distinct: artifact=%+v target=%+v", plan.Snapshot.Artifact.ConfigurationSource, target)
	}
	if target.DeploymentPipeline == nil || target.DeploymentPipeline.ConnectionID != 9 || target.DeploymentPipeline.PipelineID != "123456" {
		t.Fatalf("target deployment pipeline binding was not frozen into the plan: %+v", target.DeploymentPipeline)
	}
	if target.DeploymentPipelineScopeReview == nil || target.DeploymentPipelineScopeReview.ID != review.ID || target.DeploymentPipelineScopeReview.DefinitionHash != review.DefinitionHash {
		t.Fatalf("pipeline scope review was not frozen into the plan: %+v", target.DeploymentPipelineScopeReview)
	}
	var sourceCheck *DeliveryPlanCheck
	var scopeCheck *DeliveryPlanCheck
	for i := range plan.Snapshot.Checks {
		if plan.Snapshot.Checks[i].Key == "stable-configuration-source" {
			sourceCheck = &plan.Snapshot.Checks[i]
		}
		if plan.Snapshot.Checks[i].Key == "deployment-pipeline-scope" {
			scopeCheck = &plan.Snapshot.Checks[i]
		}
	}
	if sourceCheck == nil || sourceCheck.Status != "review" || !strings.Contains(sourceCheck.Detail, stableSource.Revision) {
		t.Fatalf("stable config provenance was not explained for review: %+v", sourceCheck)
	}
	if scopeCheck == nil || scopeCheck.Status != "review" || !strings.Contains(scopeCheck.Detail, review.DefinitionHash) || !strings.Contains(scopeCheck.Detail, "不能据此自动触发") {
		t.Fatalf("scope review was treated as executable proof or omitted: %+v", scopeCheck)
	}
	if _, err := store.RecordDeliveryPipelineScopeReview(DeliveryPipelineScopeReview{
		BindingID: model.Bindings[1].ID, ConnectionID: 9, PipelineID: "123456",
		DefinitionHash: strings.Repeat("e", 64), WriteScope: PipelineScopeWorkloadOrImage, Reviewer: "operator",
	}); err != nil {
		t.Fatalf("record changed scope review: %v", err)
	}
	changedPlan, _, err := cd.CreateDeliveryPlan(context.Background(), candidate.ID, 2, "alice")
	if err != nil || changedPlan.ID == plan.ID || changedPlan.SnapshotHash == plan.SnapshotHash || changedPlan.Snapshot.Target.DeploymentPipelineScopeReview.WriteScope != PipelineScopeWorkloadOrImage {
		t.Fatalf("changed scope review did not require a new plan: plan=%+v err=%v", changedPlan, err)
	}

	stableSource.Revision = strings.Repeat("c", 40)
	model = store.DeliveryModel()
	model.Bindings[1].DeploymentPipeline.PipelineID = "654321"
	store.LoadDeliveryModel(model)
	stored, err := store.FindDeliveryPlan(plan.ID)
	if err != nil || stored.Snapshot.Target.StableConfigurationSource.Revision != strings.Repeat("b", 40) || stored.Snapshot.Target.DeploymentPipeline.PipelineID != "123456" || stored.Snapshot.Target.DeploymentPipelineScopeReview.DefinitionHash != strings.Repeat("d", 64) {
		t.Fatalf("stored plan aliases mutable source or environment binding: plan=%+v err=%v", stored.Snapshot.Target, err)
	}
}

func TestDeliveryPlanDoesNotUseOlderStableConfigWhenNewestSuccessfulWorkloadLogMismatches(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	candidate := seedPlanRelease(t, store, rec, "rolling")
	candidate.ID = 3
	store.releases[0].ID = candidate.ID
	knownSource := &ConfigurationSourceRevision{
		Repository: "https://git.example.com/orders/deploy-config.git", Path: "charts/orders",
		Revision: strings.Repeat("b", 40), RecordedBy: "runner:5", RecordedAt: time.Now().UTC(),
	}
	stable := candidate
	stable.ID = 2
	stable.ImageFull = "reg.io/Demo/app:stable-v2"
	stableDigest := "sha256:" + strings.Repeat("34", 32)
	stable.ImageDigest = &stableDigest
	stable.Metadata.ConfigurationSource = knownSource
	store.releases = append(store.releases, stable)
	oldImage := deployImage(stable)
	if _, err := store.AddLog(DeployLog{
		ClusterID: 2, AppName: "app", DeploymentName: "app-prod", ContainerName: "app", Namespace: "ns-prod",
		ImageFull: &oldImage, ReleaseID: stable.ID, Status: "success",
	}); err != nil {
		t.Fatalf("seed older stable deployment log: %v", err)
	}
	newImage := "reg.io/Demo/app@sha256:" + strings.Repeat("56", 32)
	if _, err := store.AddLog(DeployLog{
		ClusterID: 2, AppName: "app", DeploymentName: "app-prod", ContainerName: "app", Namespace: "ns-prod",
		ImageFull: &newImage, ReleaseID: stable.ID, Status: "success",
	}); err != nil {
		t.Fatalf("seed newest mismatched deployment log: %v", err)
	}
	rec.forcedImage = &newImage
	rec.templateImage = newImage
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), candidate.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	if plan.Snapshot.Target.StableReleaseID != 0 || plan.Snapshot.Target.StableConfigurationSource != nil {
		t.Fatalf("plan fell back to older stable config when latest deployment could not be proven: %+v", plan.Snapshot.Target)
	}
}

func TestDeliveryPlanBlocksWhenRegisteredConfigurationRevisionIsMissing(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	app, err := store.FindAppByName(1, "app")
	if err != nil {
		t.Fatal(err)
	}
	for i := range store.releases {
		if store.releases[i].ID == release.ID {
			app.DeploymentConfigRepoURL = "https://git.example.com/orders/deploy-config.git"
			app.DeploymentConfigPath = "charts/orders"
			store.releases[i].Metadata.Binding = &app
		}
	}
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	if plan.Snapshot.SchemaVersion != 12 || plan.Snapshot.Artifact.DeploymentConfigRepository != "https://git.example.com/orders/deploy-config.git" || plan.Snapshot.Artifact.DeploymentConfigPath != "charts/orders" {
		t.Fatalf("component config mapping was not captured in schema v11: %+v", plan.Snapshot.Artifact)
	}
	var sourceCheck *DeliveryPlanCheck
	for i := range plan.Snapshot.Checks {
		if plan.Snapshot.Checks[i].Key == "configuration-source" {
			sourceCheck = &plan.Snapshot.Checks[i]
			break
		}
	}
	if sourceCheck == nil || sourceCheck.Status != "blocked" || !strings.Contains(sourceCheck.Detail, "DEPLOY_CONFIG_REVISION") || len(plan.Blockers) == 0 {
		t.Fatalf("mapped config without a build revision did not block plan: check=%+v blockers=%+v", sourceCheck, plan.Blockers)
	}
	if _, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true); !errors.Is(err, ErrValidation) {
		t.Fatalf("mapped config without revision entered approval: %v", err)
	}
}

func TestDeliveryPlanRequiresAcknowledgementForUnknownConfigurationSource(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	if _, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice"); err == nil || !strings.Contains(err.Error(), "配置仓库 revision") {
		t.Fatalf("approval without acknowledging unknown configuration source error=%v", err)
	}
	if _, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true); err != nil {
		t.Fatalf("acknowledged plan approval error=%v", err)
	}
}

func TestDeliveryPlanDoesNotSnapshotInvalidRunnerConfigurationSource(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	for i := range store.releases {
		if store.releases[i].ID == release.ID {
			store.releases[i].Metadata.ConfigurationSource = &ConfigurationSourceRevision{
				Repository: "https://user:secret@git.example.com/orders/deploy-config.git",
				Path:       "charts/orders",
				Revision:   strings.Repeat("a", 40),
				RecordedBy: "runner:4",
				RecordedAt: time.Now().UTC(),
			}
		}
	}
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	if plan.Snapshot.Artifact.ConfigurationSource != nil {
		t.Fatalf("invalid configuration source was included in the plan: %+v", plan.Snapshot.Artifact.ConfigurationSource)
	}
	for _, check := range plan.Snapshot.Checks {
		if check.Key == "configuration-source" && check.Status == "unknown" && !strings.Contains(check.Detail, "secret") {
			return
		}
	}
	t.Fatalf("invalid configuration source was not treated as unknown: %+v", plan.Snapshot.Checks)
}

func TestDeliveryPlanSnapshotsServiceAccountImagePullSecrets(t *testing.T) {
	t.Run("inherited secrets are captured", func(t *testing.T) {
		_, store, _, rec, _, cd := cdFixture(t)
		release := seedPlanRelease(t, store, rec, "rolling")
		rec.deploymentServiceAccountName = "orders-runtime"
		rec.serviceAccountPullSecrets = []any{
			map[string]any{"name": "registry-auth"},
			map[string]any{"name": "registry-auth"},
		}

		plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
		if err != nil {
			t.Fatalf("CreateDeliveryPlan: %v", err)
		}
		if plan.Snapshot.Target.ServiceAccountName != "orders-runtime" || !plan.Snapshot.Target.ServiceAccountPullSecretsIncluded {
			t.Fatalf("service account source was not pinned: %+v", plan.Snapshot.Target)
		}
		if len(plan.Snapshot.Target.ConfigurationReferences) != 1 || plan.Snapshot.Target.ConfigurationReferences[0] != (DeliveryConfigurationReference{Kind: "Secret", Name: "registry-auth", UID: "uid-secrets-registry-auth", ResourceVersion: "1"}) {
			t.Fatalf("inherited image pull secret was not captured and deduplicated: %+v", plan.Snapshot.Target.ConfigurationReferences)
		}
	})

	t.Run("explicit pod references suppress inherited secrets", func(t *testing.T) {
		_, store, _, rec, _, cd := cdFixture(t)
		release := seedPlanRelease(t, store, rec, "rolling")
		rec.deploymentServiceAccountName = "orders-runtime"
		rec.deploymentImagePullSecrets = []any{map[string]any{"name": "template-registry-auth"}}
		rec.serviceAccountPullSecrets = []any{map[string]any{"name": "unused-service-account-auth"}}

		plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
		if err != nil {
			t.Fatalf("CreateDeliveryPlan: %v", err)
		}
		if plan.Snapshot.Target.ServiceAccountPullSecretsIncluded {
			t.Fatalf("ServiceAccount imagePullSecrets were recorded although the Pod template overrides them: %+v", plan.Snapshot.Target)
		}
		if len(plan.Snapshot.Target.ConfigurationReferences) != 1 || plan.Snapshot.Target.ConfigurationReferences[0].Name != "template-registry-auth" {
			t.Fatalf("effective Pod template imagePullSecrets were not preserved: %+v", plan.Snapshot.Target.ConfigurationReferences)
		}
	})

	t.Run("unreadable service account fails closed", func(t *testing.T) {
		_, store, _, rec, _, cd := cdFixture(t)
		release := seedPlanRelease(t, store, rec, "rolling")
		rec.serviceAccountErr = errors.New("forbidden")

		plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
		if err != nil {
			t.Fatalf("CreateDeliveryPlan should preserve an unknown plan preview: %v", err)
		}
		if plan.Snapshot.Target.ConfigurationReferences != nil {
			t.Fatalf("unreadable ServiceAccount was represented as an empty reference list: %+v", plan.Snapshot.Target.ConfigurationReferences)
		}
		if !containsDeliveryPlanCheck(plan.Snapshot.Checks, "configuration-snapshot", "unknown") {
			t.Fatalf("unreadable ServiceAccount did not mark the configuration snapshot unknown: %+v", plan.Snapshot.Checks)
		}
	})
}

func containsDeliveryPlanCheck(checks []DeliveryPlanCheck, key, status string) bool {
	for _, check := range checks {
		if check.Key == key && check.Status == status {
			return true
		}
	}
	return false
}

func TestLegacyRollingApprovalCannotBypassConfigurationReferenceReview(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	legacyRequest, err := cd.RequestPromotion(release.ID, 2, "ci-bot")
	if err != nil {
		t.Fatalf("create legacy request before configuration change: %v", err)
	}
	rec.deliveryEnvFrom = []any{
		map[string]any{"configMapRef": map[string]any{"name": "orders-runtime-v3"}},
	}
	if _, _, err := cd.ApprovePromotion(context.Background(), legacyRequest.ID, "alice"); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "配置引用快照和人工确认") {
		t.Fatalf("legacy approval error=%v, want configuration-plan guidance", err)
	}
	if _, err := cd.RequestPromotion(release.ID, 2, "ci-bot"); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "配置引用快照和人工确认") {
		t.Fatalf("legacy promotion request error=%v, want configuration-plan guidance", err)
	}
}

func TestLegacyRollingApprovalCannotBypassInheritedImagePullSecretReview(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	legacyRequest, err := cd.RequestPromotion(release.ID, 2, "ci-bot")
	if err != nil {
		t.Fatalf("create legacy request before ServiceAccount change: %v", err)
	}
	rec.deploymentServiceAccountName = "orders-runtime"
	rec.serviceAccountPullSecrets = []any{map[string]any{"name": "registry-auth"}}
	if _, _, err := cd.ApprovePromotion(context.Background(), legacyRequest.ID, "alice"); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "配置引用快照和人工确认") {
		t.Fatalf("legacy approval with inherited image-pull Secret error=%v, want configuration-plan guidance", err)
	}
	if _, err := cd.RequestPromotion(release.ID, 2, "ci-bot"); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "配置引用快照和人工确认") {
		t.Fatalf("legacy request with inherited image-pull Secret error=%v, want configuration-plan guidance", err)
	}
}

func TestLegacyDeliveryPlanCannotBeApprovedThroughServiceAPI(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	request, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true)
	if err != nil {
		t.Fatalf("RequestDeliveryPlanApproval: %v", err)
	}
	for i := range store.deliveryPlans {
		if store.deliveryPlans[i].ID == plan.ID {
			store.deliveryPlans[i].Snapshot.SchemaVersion = 5
			store.deliveryPlans[i].Snapshot.Target.ConfigurationReferences = nil
		}
	}

	if _, _, err := cd.ApprovePromotion(context.Background(), request.ID, "bob"); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "缺少完整的 ConfigMap/Secret 引用快照") {
		t.Fatalf("legacy plan approval error=%v, want explicit validation failure", err)
	}
	storedRequest, err := store.FindPromotionRequest(request.ID)
	if err != nil || storedRequest.Status != "pending" {
		t.Fatalf("failed legacy approval changed request: request=%+v error=%v", storedRequest, err)
	}
}

func TestRollingDeliveryPlanApprovalIsLinkedAndExecutes(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	rec.trackSetImage = true
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}

	request, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true)
	if err != nil {
		t.Fatalf("RequestDeliveryPlanApproval: %v", err)
	}
	if request.DeliveryPlanID == nil || *request.DeliveryPlanID != plan.ID || request.ReleaseID != release.ID || request.Status != "pending" {
		t.Fatalf("approval is not linked to the reviewed plan: %+v", request)
	}
	approved, deployLog, err := cd.ApprovePromotion(context.Background(), request.ID, "bob")
	if err != nil {
		t.Fatalf("ApprovePromotion: %v", err)
	}
	if approved.Status != "approved" || approved.DeployLogID == nil || *approved.DeployLogID != deployLog.ID {
		t.Fatalf("approval did not persist its execution: %+v log=%+v", approved, deployLog)
	}
	if finished := waitLog(t, store, deployLog.ID); finished.Status != "success" {
		var message string
		if finished.ErrorMessage != nil {
			message = *finished.ErrorMessage
		}
		t.Fatalf("rolling deployment status=%s error=%s", finished.Status, message)
	}
	deadline := time.Now().Add(time.Second)
	var run DeliveryRun
	for time.Now().Before(deadline) {
		run, err = store.DeliveryRunForDeployLog(deployLog.ID)
		if err == nil && run.Status == "success" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || run.Strategy != "rolling" || run.Status != "success" {
		t.Fatalf("delivery run was not created and completed: run=%+v err=%v", run, err)
	}
	events, err := store.DeliveryRunEvents(run.ID)
	if err != nil || len(events) < 4 || events[0].EventKey != "run-created" || !strings.HasSuffix(events[len(events)-1].EventKey, "terminal:success") {
		t.Fatalf("delivery run timeline=%+v err=%v", events, err)
	}
}

func TestRollingDeliveryPlanRejectsConfigurationDriftBeforeImageWrite(t *testing.T) {
	cfg, store, runner, rec, activeWorker, _ := cdFixture(t)
	activeWorker.Close()
	worker := newWorker(activeWorker.pipeline, 1, false) // Keep the approved task pending until this test starts it.
	t.Cleanup(worker.Close)
	cd := NewCDService(cfg, store, worker)
	rec.trackSetImage = true
	rec.deliveryEnvFrom = []any{
		map[string]any{"configMapRef": map[string]any{"name": "orders-runtime-v3"}},
	}
	release := seedPlanRelease(t, store, rec, "rolling")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	request, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true)
	if err != nil {
		t.Fatalf("RequestDeliveryPlanApproval: %v", err)
	}
	approved, deployLog, err := cd.ApprovePromotion(context.Background(), request.ID, "bob")
	if err != nil {
		t.Fatalf("ApprovePromotion: %v", err)
	}
	if approved.DeliveryPlanID == nil || *approved.DeliveryPlanID != plan.ID {
		t.Fatalf("approved request lost linked plan: approved=%+v plan=%d", approved, plan.ID)
	}
	queued, err := store.FindLog(deployLog.ID)
	if err != nil || queued.Status != "pending" {
		t.Fatalf("approved task should stay pending before controlled execution: log=%+v err=%v", queued, err)
	}
	rec.configurationMetadata = map[string]metav1.PartialObjectMetadata{
		"configmaps/ns-prod/orders-runtime-v3": {ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns-prod", Name: "orders-runtime-v3", UID: "uid-configmaps-orders-runtime-v3", ResourceVersion: "2",
		}},
	}
	pipeline := NewPipeline(cfg, store, runner, activeWorker.pipeline.k8s)
	if err := pipeline.Run(context.Background(), deployLog.ID); err != nil {
		t.Fatalf("record post-approval configuration drift as a failed task: %v", err)
	}
	if calls := rec.recorded(); len(calls) != 0 {
		t.Fatalf("configuration drift reached SetImage: %v", calls)
	}
	current, err := store.FindLog(deployLog.ID)
	if err != nil || current.Status != "failed" || current.ErrorMessage == nil || !strings.Contains(*current.ErrorMessage, "对象已变化") {
		t.Fatalf("post-approval drift should fail the task with a clear reason: log=%+v err=%v", current, err)
	}
}

func TestDeliveryPlanPinsImmutablePolicyVersion(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "blue_green")
	firstPolicy, created, err := store.SaveDeliveryPolicyVersion(2, 0, "blue_green", "orders-entry", 48, "alice")
	if err != nil || !created {
		t.Fatalf("save first policy=%+v created=%t err=%v", firstPolicy, created, err)
	}
	firstPlan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	if firstPlan.Snapshot.SchemaVersion != 12 || firstPlan.Snapshot.Policy.Version != 1 || firstPlan.Snapshot.Policy.Source != "versioned_policy" || firstPlan.Snapshot.Policy.CandidateRetentionHours != 48 || firstPlan.Snapshot.CandidateRetentionHours != 48 || firstPlan.Snapshot.Target.ServiceName != "orders-entry" {
		t.Fatalf("policy was not pinned in plan: %+v", firstPlan.Snapshot)
	}
	secondPolicy, created, err := store.SaveDeliveryPolicyVersion(2, 1, "rolling", "", 0, "bob")
	if err != nil || !created || secondPolicy.Version != 2 {
		t.Fatalf("save second policy=%+v created=%t err=%v", secondPolicy, created, err)
	}
	stored, err := store.FindDeliveryPlan(firstPlan.ID)
	if err != nil || stored.Snapshot.Policy.Version != 1 || stored.Snapshot.Policy.Strategy != "blue_green" || stored.Snapshot.CandidateRetentionHours != 48 {
		t.Fatalf("existing plan changed after policy update: %+v err=%v", stored.Snapshot, err)
	}
	secondPlan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "bob")
	if err != nil {
		t.Fatalf("create plan from second policy: %v", err)
	}
	if secondPlan.Snapshot.Policy.Version != 2 || secondPlan.Strategy != "rolling" || secondPlan.Snapshot.Policy.Strategy != "rolling" {
		t.Fatalf("new plan did not use current version: %+v", secondPlan)
	}
}

func TestGatewayCanaryPlanPinsThresholdsAndRemainsBlocked(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "gateway_canary")
	stableDigest := "sha256:" + strings.Repeat("34", 32)
	cd.resolveDigest = func(context.Context, config.Config, string) (string, error) { return stableDigest, nil }
	analysis := testCanaryAnalysisPolicy()
	firstPolicy, created, err := store.SaveDeliveryPolicyVersion(2, 0, "gateway_canary", "", 0, "alice", &analysis)
	if err != nil || !created || firstPolicy.Readiness != "integration_pending" {
		t.Fatalf("save canary draft=%+v created=%t err=%v", firstPolicy, created, err)
	}
	firstPlan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("create canary preview: %v", err)
	}
	if firstPlan.Snapshot.SchemaVersion != 12 || firstPlan.Snapshot.Target.StableImageDigest != stableDigest || firstPlan.Snapshot.Policy.Version != 1 || !sameCanaryAnalysisPolicy(firstPlan.Snapshot.Policy.CanaryAnalysis, &analysis) || len(firstPlan.Blockers) == 0 {
		t.Fatalf("canary thresholds were not pinned or gateway gate disappeared: %+v", firstPlan)
	}
	missingRouteCheck := false
	for _, check := range firstPlan.Snapshot.Checks {
		missingRouteCheck = missingRouteCheck || (check.Key == "canary-gateway-route-identity" && check.Status == "blocked")
	}
	if !missingRouteCheck {
		t.Fatalf("canary preview without a route identity must expose the blocker: %+v", firstPlan.Snapshot.Checks)
	}
	if _, err := cd.RequestDeliveryPlanApproval(context.Background(), firstPlan.ID, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("canary plan must remain blocked from approval: %v", err)
	}

	updated := testCanaryAnalysisPolicy()
	*updated.MaxCandidateErrorRate = 0.03
	route := CanaryGatewayRouteRef{
		ConnectionID: 7, RouteID: "orders-public", StableBackendID: "orders-stable", CandidateBackendID: "orders-candidate",
	}
	updated.GatewayRoute = &route
	secondPolicy, created, err := store.SaveDeliveryPolicyVersion(2, 1, "gateway_canary", "", 0, "bob", &updated)
	if err != nil || !created || secondPolicy.Version != 2 {
		t.Fatalf("append canary threshold version=%+v created=%t err=%v", secondPolicy, created, err)
	}
	secondPlan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "bob")
	if err != nil {
		t.Fatalf("create second canary preview: %v", err)
	}
	storedFirst, err := store.FindDeliveryPlan(firstPlan.ID)
	if err != nil || storedFirst.Snapshot.Policy.Version != 1 || storedFirst.Snapshot.Policy.CanaryAnalysis == nil || *storedFirst.Snapshot.Policy.CanaryAnalysis.MaxCandidateErrorRate != 0.05 {
		t.Fatalf("existing plan changed after threshold update: plan=%+v err=%v", storedFirst, err)
	}
	if secondPlan.Snapshot.Policy.Version != 2 || secondPlan.Snapshot.Policy.CanaryAnalysis == nil || *secondPlan.Snapshot.Policy.CanaryAnalysis.MaxCandidateErrorRate != 0.03 || secondPlan.Snapshot.Policy.CanaryAnalysis.GatewayRoute == nil || *secondPlan.Snapshot.Policy.CanaryAnalysis.GatewayRoute != route || secondPlan.SnapshotHash == firstPlan.SnapshotHash {
		t.Fatalf("new plan did not pin updated thresholds: %+v", secondPlan)
	}
	routeCheckUnknown := false
	for _, check := range secondPlan.Snapshot.Checks {
		routeCheckUnknown = routeCheckUnknown || (check.Key == "canary-gateway-route-identity" && check.Status == "unknown")
	}
	if !routeCheckUnknown {
		t.Fatalf("operator-entered route ID without adapter verification must remain unknown: %+v", secondPlan.Snapshot.Checks)
	}
	cd.resolveDigest = func(context.Context, config.Config, string) (string, error) {
		return "", errors.New("registry lookup failed")
	}
	thirdPlan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "carol")
	if err != nil {
		t.Fatalf("create blocked canary preview when stable digest cannot be resolved: %v", err)
	}
	stableDigestBlocked := false
	for _, blocker := range thirdPlan.Blockers {
		if blocker == "无法为当前稳定版固定不可变 sha256 digest；请检查镜像引用与 Registry 凭据。" {
			stableDigestBlocked = true
			break
		}
	}
	if thirdPlan.Snapshot.Target.StableImageDigest != "" || !stableDigestBlocked {
		t.Fatalf("canary plan did not fail closed on unresolved stable digest: %+v", thirdPlan.Snapshot.Target)
	}
}

func TestDeliveryPlanApprovalRejectsStaleBaseline(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	rec.mu.Lock()
	rec.templateEnv = "changed-after-preview"
	rec.mu.Unlock()

	if _, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale plan approval error=%v, want conflict", err)
	}
	if got := store.PromotionRequests("", 0); len(got) != 0 {
		t.Fatalf("stale plan created approval requests: %+v", got)
	}
}

func TestDeliveryPlanApprovalRejectsChangedPipelineScopeReview(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	model := store.DeliveryModel()
	model.Bindings[1].DeploymentPipeline = &YunxiaoDeploymentPipelineBinding{ConnectionID: 9, PipelineID: "123456"}
	store.LoadDeliveryModel(model)
	if _, err := store.RecordDeliveryPipelineScopeReview(DeliveryPipelineScopeReview{
		BindingID: model.Bindings[1].ID, ConnectionID: 9, PipelineID: "123456",
		DefinitionHash: strings.Repeat("a", 64), WriteScope: PipelineScopeConfigurationOnly, Reviewer: "operator",
	}); err != nil {
		t.Fatalf("record first scope review: %v", err)
	}
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	request, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true)
	if err != nil {
		t.Fatalf("RequestDeliveryPlanApproval: %v", err)
	}
	if _, err := store.RecordDeliveryPipelineScopeReview(DeliveryPipelineScopeReview{
		BindingID: model.Bindings[1].ID, ConnectionID: 9, PipelineID: "123456",
		DefinitionHash: strings.Repeat("b", 64), WriteScope: PipelineScopeWorkloadOrImage, Reviewer: "operator",
	}); err != nil {
		t.Fatalf("record changed scope review: %v", err)
	}
	if _, _, err := cd.ApprovePromotion(context.Background(), request.ID, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("approval with a changed scope review error=%v, want conflict", err)
	}
	stored, err := store.FindPromotionRequest(request.ID)
	if err != nil || stored.Status != "pending" {
		t.Fatalf("changed scope review modified the pending request: %+v err=%v", stored, err)
	}
}

func TestDeliveryPlanApprovalRejectsChangedServiceAccountImagePullSecrets(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	rec.deploymentServiceAccountName = "orders-runtime"
	rec.serviceAccountPullSecrets = []any{map[string]any{"name": "registry-auth-v1"}}
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	request, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true)
	if err != nil {
		t.Fatalf("RequestDeliveryPlanApproval: %v", err)
	}
	rec.mu.Lock()
	rec.serviceAccountPullSecrets = []any{map[string]any{"name": "registry-auth-v2"}}
	rec.mu.Unlock()
	if _, _, err := cd.ApprovePromotion(context.Background(), request.ID, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("approval after inherited Secret reference changed error=%v, want conflict", err)
	}
	stored, err := store.FindPromotionRequest(request.ID)
	if err != nil || stored.Status != "pending" {
		t.Fatalf("stale approval changed request state: request=%+v error=%v", stored, err)
	}
}

func TestDeliveryPlanApprovalRejectsChangedConfigurationObjectVersion(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	rec.deliveryEnvFrom = []any{map[string]any{"configMapRef": map[string]any{"name": "orders-runtime"}}}
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	if got := plan.Snapshot.Target.ConfigurationReferences; len(got) != 1 || got[0].UID == "" || got[0].ResourceVersion == "" {
		t.Fatalf("plan did not pin ConfigMap metadata version: %+v", got)
	}
	request, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true)
	if err != nil {
		t.Fatalf("RequestDeliveryPlanApproval: %v", err)
	}
	rec.mu.Lock()
	rec.configurationMetadata = map[string]metav1.PartialObjectMetadata{
		"configmaps/ns-prod/orders-runtime": {ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns-prod", Name: "orders-runtime", UID: types.UID("uid-configmaps-orders-runtime"), ResourceVersion: "2",
		}},
	}
	rec.mu.Unlock()
	if _, _, err := cd.ApprovePromotion(context.Background(), request.ID, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("approval after same-name ConfigMap change error=%v, want conflict", err)
	}
	stored, err := store.FindPromotionRequest(request.ID)
	if err != nil || stored.Status != "pending" {
		t.Fatalf("stale approval changed request state: request=%+v error=%v", stored, err)
	}
}

func TestDeliveryPlanPinsWorkloadUIDAndRejectsSameNameReplacement(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "rolling")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	binding, err := store.DeliveryBindingForApp(plan.TargetAppID)
	if err != nil {
		t.Fatal(err)
	}
	if binding.TemplateWorkloadRef.UID == "" || binding.ActiveWorkloadRef.UID == "" || binding.ActiveWorkloadRef.UID != plan.Snapshot.Target.WorkloadUID {
		t.Fatalf("plan did not pin the live workload identity: binding=%+v target=%+v", binding, plan.Snapshot.Target)
	}
	if binding.TemplateWorkloadRef.Name != plan.Snapshot.Target.Deployment {
		t.Fatalf("template reference did not capture the original workload: %+v", binding.TemplateWorkloadRef)
	}

	rec.mu.Lock()
	rec.deploymentUID = "replacement-with-same-name"
	rec.mu.Unlock()
	if _, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-name replacement error=%v, want conflict", err)
	}
}

func TestOperatorCanExplicitlyRebindActiveWorkloadIdentity(t *testing.T) {
	for _, withLock := range []bool{false, true} {
		name := "rebind"
		if withLock {
			name = "active delivery lock blocks rebind"
		}
		t.Run(name, func(t *testing.T) {
			_, store, _, _, _, cd := cdFixture(t)
			template := DeliveryWorkloadReference{ClusterID: 2, Namespace: "ns-prod", Kind: "Deployment", Name: "app-prod", UID: "template-uid", ResourceVersion: "1"}
			active := template
			active.UID = "stale-active-uid"
			store.LoadDeliveryModel(DeliveryModel{Bindings: []DeliveryBinding{{ID: 42, AppID: 2, TemplateWorkloadRef: template, ActiveWorkloadRef: active}}})
			if withLock {
				lock := ResourceLock{ClusterID: 2, Namespace: "ns-prod", Kind: "deployment", Name: "app-prod"}
				store.mu.Lock()
				store.resourceLocks[lock.Identity()] = 77
				store.mu.Unlock()
			}

			binding, err := cd.RebindDeliveryBindingWorkloadRef(context.Background(), 2)
			if withLock {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("active lock rebind error=%v, want conflict", err)
				}
				stored, _ := store.DeliveryBindingForApp(2)
				if stored.ActiveWorkloadRef != active {
					t.Fatalf("active binding changed despite lock: %+v", stored.ActiveWorkloadRef)
				}
				return
			}
			if err != nil {
				t.Fatalf("rebind workload identity: %v", err)
			}
			if binding.ActiveWorkloadRef.UID != "mock-app-prod" || binding.ActiveWorkloadRef.ResourceVersion != "1" {
				t.Fatalf("new active ref was not read from Kubernetes: %+v", binding.ActiveWorkloadRef)
			}
			if binding.TemplateWorkloadRef != template {
				t.Fatalf("rebind changed the original template reference: %+v", binding.TemplateWorkloadRef)
			}
		})
	}
}

func TestBlueGreenPlanApprovalDoesNotEnableLegacyRollingPromotion(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "blue_green")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	if _, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("blue-green approval without manual preflight acknowledgment error=%v, want validation", err)
	}
	request, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true)
	if err != nil || request.DeliveryPlanID == nil || *request.DeliveryPlanID != plan.ID {
		t.Fatalf("blue-green plan approval request=%+v error=%v", request, err)
	}
	if _, err := cd.RequestPromotion(1, 2, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("legacy approval for blue-green binding error=%v, want validation", err)
	}
	if _, _, err := cd.Promote(context.Background(), release.ID, 2); !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "请从发布计划发起蓝绿审批") {
		t.Fatalf("legacy execution for blue-green binding error=%v, want guidance to use delivery plan", err)
	}
	if plan.Snapshot.Target.ServiceName != "orders-entry" {
		t.Fatalf("blue-green plan did not snapshot its bound Service: %+v", plan.Snapshot.Target)
	}
	if got := store.PromotionRequests("", 0); len(got) != 1 || got[0].DeliveryPlanID == nil || *got[0].DeliveryPlanID != plan.ID {
		t.Fatalf("only the approved blue-green plan request should exist: %+v", got)
	}
}

func TestBlueGreenPlanCapturesSafetyPreflight(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, rec, "blue_green")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	statuses := map[string]string{}
	for _, check := range plan.Snapshot.Checks {
		statuses[check.Key] = check.Status
	}
	for key, want := range map[string]string{
		"stable-health": "passed", "workload-shape": "passed", "storage": "passed",
		"service-selector": "passed", "traffic-entry": "passed", "autoscaling": "passed",
		"service-snapshot": "passed", "baseline-consistency": "passed",
		"candidate-isolation": "passed", "candidate-label-compatibility": "review",
		"capacity": "review", "external-routes": "review",
	} {
		if got := statuses[key]; got != want {
			t.Errorf("preflight %q status=%q, want %q; checks=%+v", key, got, want, plan.Snapshot.Checks)
		}
	}
	if plan.Snapshot.Target.Replicas != 2 || plan.Snapshot.Target.ServiceUID == "" || plan.Snapshot.Target.ServiceResourceVersion == "" {
		t.Fatalf("blue-green baseline was not snapshotted: %+v", plan.Snapshot.Target)
	}
	if len(plan.Blockers) != 0 {
		t.Fatalf("blue-green preflight has unexpected blockers: %v", plan.Blockers)
	}
}

func TestBlueGreenPlanBlocksWhenResourceQuotaCannotFitCandidate(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	rec.blueGreenQuotas = []unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "v1", "kind": "ResourceQuota",
		"metadata": map[string]any{"name": "orders-pods"},
		"status": map[string]any{
			"hard": map[string]any{"pods": "3"},
			"used": map[string]any{"pods": "2"},
		},
	}}}
	release := seedPlanRelease(t, store, rec, "blue_green")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	var capacityCheck DeliveryPlanCheck
	for _, check := range plan.Snapshot.Checks {
		if check.Key == "capacity" {
			capacityCheck = check
			break
		}
	}
	if capacityCheck.Status != "blocked" {
		t.Fatalf("capacity status=%q, want blocked; check=%+v", capacityCheck.Status, capacityCheck)
	}
	if len(plan.Blockers) == 0 {
		t.Fatal("known ResourceQuota shortage must block the blue-green plan")
	}
}

func TestBlueGreenApprovalRejectsQuotaDriftWhilePending(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	rec.blueGreenQuotas = []unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "v1", "kind": "ResourceQuota",
		"metadata": map[string]any{"name": "orders-pods"},
		"status": map[string]any{
			"hard": map[string]any{"pods": "5"},
			"used": map[string]any{"pods": "2"},
		},
	}}}
	release := seedPlanRelease(t, store, rec, "blue_green")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	request, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice", true)
	if err != nil {
		t.Fatalf("RequestDeliveryPlanApproval: %v", err)
	}

	// A second blue-green version now needs two additional Pods, but the
	// namespace quota has room for only one. Approval must re-plan before it
	// creates a deployment task or acquires resource locks.
	rec.blueGreenQuotas[0].Object["status"].(map[string]any)["hard"].(map[string]any)["pods"] = "3"
	if _, _, err := cd.ApprovePromotion(context.Background(), request.ID, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("approval after quota drift error=%v, want conflict", err)
	}
	stored, err := store.FindPromotionRequest(request.ID)
	if err != nil || stored.Status != "pending" || stored.DeployLogID != nil {
		t.Fatalf("quota drift changed approval or created a task: request=%+v err=%v", stored, err)
	}
	if got := store.Logs(2, "", "", 0); len(got) != 0 {
		t.Fatalf("quota drift created production deployment tasks: %+v", got)
	}
}

func TestBlueGreenPlanBlocksUnreadyOrPersistentWorkloads(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	ready := int64(1)
	rec.readyOverride = &ready
	rec.blueGreenVolumes = []any{map[string]any{
		"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "orders-data"},
	}}
	release := seedPlanRelease(t, store, rec, "blue_green")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	statuses := map[string]string{}
	for _, check := range plan.Snapshot.Checks {
		statuses[check.Key] = check.Status
	}
	if statuses["stable-health"] != "blocked" || statuses["storage"] != "blocked" {
		t.Fatalf("unready or persistent workload should block blue-green: %+v", plan.Snapshot.Checks)
	}
}

func TestBlueGreenPlanRejectsUnsupportedControllerExpressions(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	rec.blueGreenMatchExpressions = true
	release := seedPlanRelease(t, store, rec, "blue_green")
	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	for _, check := range plan.Snapshot.Checks {
		if check.Key == "candidate-isolation" {
			if check.Status != "blocked" {
				t.Fatalf("unsupported selector expression status=%q, want blocked", check.Status)
			}
			return
		}
	}
	t.Fatalf("candidate selector isolation check missing: %+v", plan.Snapshot.Checks)
}

func TestBlueGreenPlanBlocksUnsafeOrUnknownPreflight(t *testing.T) {
	t.Run("HPA and multiple traffic entries", func(t *testing.T) {
		_, store, _, rec, _, cd := cdFixture(t)
		rec.blueGreenHPA = true
		rec.blueGreenExtraService = true
		release := seedPlanRelease(t, store, rec, "blue_green")
		plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
		if err != nil {
			t.Fatalf("CreateDeliveryPlan: %v", err)
		}
		statuses := map[string]string{}
		for _, check := range plan.Snapshot.Checks {
			statuses[check.Key] = check.Status
		}
		if statuses["autoscaling"] != "blocked" || statuses["traffic-entry"] != "blocked" {
			t.Fatalf("expected HPA and multiple services to block blue-green plan: %+v", plan.Snapshot.Checks)
		}
	})
	t.Run("unknown cluster reads", func(t *testing.T) {
		_, store, _, rec, _, cd := cdFixture(t)
		rec.blueGreenListError = true
		release := seedPlanRelease(t, store, rec, "blue_green")
		plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
		if err != nil {
			t.Fatalf("CreateDeliveryPlan: %v", err)
		}
		statuses := map[string]string{}
		for _, check := range plan.Snapshot.Checks {
			statuses[check.Key] = check.Status
		}
		if statuses["traffic-entry"] != "unknown" || statuses["autoscaling"] != "unknown" {
			t.Fatalf("failed cluster reads must remain unknown: %+v", plan.Snapshot.Checks)
		}
		if len(plan.Blockers) < 3 {
			t.Fatalf("unknown safety checks must add blockers: %+v", plan.Blockers)
		}
	})
}

func TestBlueGreenBindingRequiresExplicitService(t *testing.T) {
	_, store, _, rec, _, _ := cdFixture(t)
	seedPlanRelease(t, store, rec, "rolling")
	if _, err := store.AssignDeliveryBinding(2, 21, 31, 12, "blue_green"); !errors.Is(err, ErrValidation) {
		t.Fatalf("blue-green binding without Service error=%v, want validation", err)
	}
	row, err := store.AssignDeliveryBinding(2, 21, 31, 12, "blue_green", "orders-entry")
	if err != nil {
		t.Fatalf("assign explicit blue-green Service: %v", err)
	}
	if row.ServiceName != "orders-entry" {
		t.Fatalf("stored Service name=%q, want orders-entry", row.ServiceName)
	}
}

func (r *recordingK8s) GetResource(ctx context.Context, def kube.ResourceDef, namespace, name string, opts metav1.GetOptions) (*unstructured.Unstructured, error) {
	obj, err := (MockKubernetes{}).GetResource(ctx, def, namespace, name, opts)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	value := r.templateEnv
	image := r.templateImage
	if image == "" {
		image = "reg.io/app:current"
	}
	serviceAccountName := r.deploymentServiceAccountName
	imagePullSecrets := r.deploymentImagePullSecrets
	serviceAccountPullSecrets := r.serviceAccountPullSecrets
	serviceAccountErr := r.serviceAccountErr
	r.mu.Unlock()
	switch def.Resource {
	case "deployments":
		if r.deploymentUID != "" {
			obj.SetUID(types.UID(r.deploymentUID))
		}
		ready := int64(2)
		if r.readyOverride != nil {
			ready = *r.readyOverride
		}
		selector := map[string]any{"matchLabels": map[string]any{"app": "app"}}
		if r.blueGreenMatchExpressions {
			selector["matchExpressions"] = []any{map[string]any{"key": "role", "operator": "In", "values": []any{"stable"}}}
		}
		container := map[string]any{"name": "app", "image": image, "env": []any{map[string]any{"name": "MODE", "value": value}}}
		if len(r.deliveryEnvFrom) > 0 {
			container["envFrom"] = r.deliveryEnvFrom
		}
		podSpec := map[string]any{"containers": []any{container}}
		if serviceAccountName != "" {
			podSpec["serviceAccountName"] = serviceAccountName
		}
		if imagePullSecrets != nil {
			podSpec["imagePullSecrets"] = imagePullSecrets
		}
		obj.Object["spec"] = map[string]any{
			"replicas": int64(2),
			"selector": selector,
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]any{"app": "app"}},
				"spec":     podSpec,
			},
		}
		if len(r.blueGreenVolumes) > 0 {
			obj.Object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["volumes"] = r.blueGreenVolumes
		}
		obj.Object["status"] = map[string]any{"readyReplicas": ready}
	case "services":
		obj.Object["spec"] = map[string]any{"selector": map[string]any{"app": "app"}}
	case "serviceaccounts":
		if serviceAccountErr != nil {
			return nil, serviceAccountErr
		}
		if serviceAccountPullSecrets != nil {
			obj.Object["imagePullSecrets"] = serviceAccountPullSecrets
		}
	default:
		obj.Object["spec"] = map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": "reg.io/app:current", "env": []any{map[string]any{"name": "MODE", "value": value}}}}}}}
	}
	return obj, nil
}

func (r *recordingK8s) GetObjectMetadata(_ context.Context, def kube.ResourceDef, namespace, name string) (*metav1.PartialObjectMetadata, error) {
	key := def.Resource + "/" + namespace + "/" + name
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.configurationMetadataErrs[key]; err != nil {
		return nil, err
	}
	if object, ok := r.configurationMetadata[key]; ok {
		copy := object.DeepCopy()
		return copy, nil
	}
	return &metav1.PartialObjectMetadata{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace, Name: name, UID: types.UID("uid-" + def.Resource + "-" + name), ResourceVersion: "1",
	}}, nil
}

func blueGreenService(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": name, "namespace": namespace},
		"spec":     map[string]any{"selector": map[string]any{"app": "app"}},
	}}
}
