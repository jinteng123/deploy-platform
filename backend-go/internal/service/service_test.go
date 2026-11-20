package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"deploy-platform/backend-go/internal/config"
)

func testConfig(dir string) config.Config {
	return config.Config{
		Env:             "dev",
		MockRuntime:     true,
		TransferDir:     dir,
		RegistryHost:    "reg.example.com",
		RegistryProject: "Demo",
		RegistryUser:    "admin",
	}
}

func testPipeline(cfg config.Config, store *Store, runner CommandRunner) *Pipeline {
	k8s := NewK8sService(cfg, runner, nil)
	return NewPipeline(cfg, store, runner, k8s)
}

func waitLog(t *testing.T, store *Store, id int64) DeployLog {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l, err := store.FindLog(id)
		if err == nil && (l.Status == "success" || l.Status == "failed" || l.Status == "interrupted") {
			return l
		}
		time.Sleep(10 * time.Millisecond)
	}
	l, _ := store.FindLog(id)
	t.Fatalf("log %d did not finish (status=%s)", id, l.Status)
	return DeployLog{}
}

func mustStepDetail(t *testing.T, l DeployLog) []StepResult {
	t.Helper()
	var steps []StepResult
	if err := json.Unmarshal([]byte(l.StepDetail), &steps); err != nil {
		t.Fatalf("step_detail is not valid JSON: %v\n%s", err, l.StepDetail)
	}
	return steps
}

func TestParseVersionFromFilename(t *testing.T) {
	cases := map[string]string{
		"demo-agent-core-api-20260707202447.tar.gz": "v20260707202447",
		"app-20260707185356-v1.2.3.tar.gz":         "v20260707185356", // 14 位时间戳优先
		"app-v1.2.3.tar":                           "v1.2.3",
		"app-1.2.tar":                              "v1.2",
		"app-V2.0.1.9.tgz":                         "v2.0.1.9",
		"noversion.tar.gz":                         "",
		"":                                         "",
	}
	for name, want := range cases {
		if got := ParseVersionFromFilename(name); got != want {
			t.Errorf("ParseVersionFromFilename(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestListTransferFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(name string, size int, mtime time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	writeFile("agent-api-20260701000000.tar.gz", 2*1048576+512*1024, now.Add(-2*time.Hour))
	writeFile("web-1.2.3.tar.gz", 1048576, now.Add(-1*time.Hour))
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	desc := "agent api"
	apps := []App{{ID: 1, AppName: "agent-api", MatchKeyword: "agent-api"}}
	files, err := ListTransferFiles(dir, append(apps, App{ID: 2, AppName: "x", MatchKeyword: "nomatch", Description: &desc}))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	// mtime 降序
	if files[0].FileName != "web-1.2.3.tar.gz" || files[1].FileName != "agent-api-20260701000000.tar.gz" {
		t.Fatalf("wrong order: %+v", files)
	}
	if files[1].SizeMB != 2.5 {
		t.Errorf("size_mb = %v, want 2.5", files[1].SizeMB)
	}
	if files[1].MatchedApp == nil || files[1].MatchedApp.AppName != "agent-api" {
		t.Errorf("matched_app = %+v", files[1].MatchedApp)
	}
	if files[0].MatchedApp != nil {
		t.Errorf("expected no matched_app, got %+v", files[0].MatchedApp)
	}
	if _, err := time.Parse("2006-01-02 15:04:05", files[0].ModifiedAt); err != nil {
		t.Errorf("modified_at format: %v", err)
	}
}

func TestListTransferFilesMissingDir(t *testing.T) {
	files, err := ListTransferFiles(filepath.Join(t.TempDir(), "nope"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("expected empty list, got %v", files)
	}
}

type failPersistence struct{ err error }

func (f failPersistence) SaveCluster(c Cluster) (Cluster, error) { return c, f.err }
func (f failPersistence) UpdateCluster(Cluster) error            { return f.err }
func (f failPersistence) DeleteCluster(int) error                { return f.err }
func (f failPersistence) SaveApp(a App) (App, error)             { return a, f.err }
func (f failPersistence) UpdateApp(App) error                    { return f.err }
func (f failPersistence) DeleteApp(int) error                    { return f.err }
func (f failPersistence) SaveLog(l DeployLog) (DeployLog, error) { return l, f.err }
func (f failPersistence) UpdateLogExecution(DeployLog) error     { return f.err }
func (f failPersistence) CreateImageRelease(r ImageRelease, l DeployLog) (ImageRelease, DeployLog, bool, error) {
	return r, l, false, f.err
}
func (f failPersistence) PromoteImageRelease(_ int64, _ int, l DeployLog) (ImageRelease, DeployLog, error) {
	return ImageRelease{}, l, f.err
}
func (f failPersistence) SaveUser(u User) (User, error) { return u, f.err }
func (f failPersistence) UpdateUser(User) error         { return f.err }
func (f failPersistence) SaveAudit(AuditEntry) error    { return f.err }
func (f failPersistence) SaveAPIKey(k APIKey) (APIKey, error) {
	return k, f.err
}
func (f failPersistence) UpdateAPIKey(APIKey) error          { return f.err }
func (f failPersistence) DeleteAPIKey(int64) error           { return f.err }
func (f failPersistence) TouchAPIKey(int64, time.Time) error { return f.err }
func (f failPersistence) SavePromotionRequest(p PromotionRequest) (PromotionRequest, error) {
	return p, f.err
}
func (f failPersistence) UpdatePromotionRequest(PromotionRequest) error { return f.err }

func TestStorePersistenceErrorDoesNotTouchMemory(t *testing.T) {
	boom := errors.New("db down")
	existing := DeployLog{ID: 7, AppName: "a", Status: "pending"}
	store := NewStoreWithData(
		[]Cluster{{ID: 1, Name: "c"}}, []App{{ID: 3, AppName: "a"}}, []DeployLog{existing}, nil,
		failPersistence{err: boom})

	if _, err := store.SaveCluster(Cluster{Name: "x", EnvCode: "x"}); !errors.Is(err, boom) {
		t.Fatalf("SaveCluster err=%v", err)
	}
	if got := len(store.Clusters()); got != 1 {
		t.Fatalf("clusters mutated: %d", got)
	}
	if _, err := store.SaveApp(App{AppName: "x"}); !errors.Is(err, boom) {
		t.Fatalf("SaveApp err=%v", err)
	}
	if got := len(store.Apps(0)); got != 1 {
		t.Fatalf("apps mutated: %d", got)
	}
	if _, err := store.AddLog(DeployLog{AppName: "x", Status: "pending"}); !errors.Is(err, boom) {
		t.Fatalf("AddLog err=%v", err)
	}
	if got := len(store.Logs(0, "", "", 0)); got != 1 {
		t.Fatalf("logs mutated: %d", got)
	}
	if err := store.DeleteCluster(1); !errors.Is(err, boom) {
		t.Fatalf("DeleteCluster err=%v", err)
	}
	if got := len(store.Clusters()); got != 1 {
		t.Fatalf("cluster removed despite db error: %d", got)
	}
	if err := store.DeleteApp(3); !errors.Is(err, boom) {
		t.Fatalf("DeleteApp err=%v", err)
	}
	if got := len(store.Apps(0)); got != 1 {
		t.Fatalf("app removed despite db error: %d", got)
	}
	if err := store.UpdateLogExecution(DeployLog{ID: 7, Status: "failed"}); !errors.Is(err, boom) {
		t.Fatalf("UpdateLogExecution err=%v", err)
	}
	l, _ := store.FindLog(7)
	if l.Status != "pending" {
		t.Fatalf("memory log mutated to %s", l.Status)
	}
}

func TestStoreIDAllocationIsMaxPlusOne(t *testing.T) {
	store := NewStoreWithData(
		[]Cluster{{ID: 5}}, []App{{ID: 9}}, []DeployLog{{ID: 42}}, nil, nil)
	c, _ := store.SaveCluster(Cluster{Name: "c", EnvCode: "c"})
	a, _ := store.SaveApp(App{AppName: "a"})
	l, _ := store.AddLog(DeployLog{AppName: "a", Status: "pending"})
	if c.ID != 6 || a.ID != 10 || l.ID != 43 {
		t.Fatalf("ids = %d,%d,%d", c.ID, a.ID, l.ID)
	}
	if err := store.DeleteApp(a.ID); err != nil {
		t.Fatal(err)
	}
	a2, _ := store.SaveApp(App{AppName: "b"})
	if a2.ID != 11 {
		t.Fatalf("id reused after delete: %d", a2.ID)
	}
}

func TestDeployMockSuccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app-v1.2.0.tar.gz"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(dir)
	store := NewStore()
	runner := &MockRunner{}
	pipeline := testPipeline(cfg, store, runner)

	image := "reg.example.com/Demo/myapp:v1.2.0"
	deployLog, err := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "myapp", DeploymentName: "myapp", ContainerName: "myapp",
		Namespace: "default", Version: "v1.2.0", FileName: "app-v1.2.0.tar.gz",
		ImageFull: &image, DeployType: "deploy", Status: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Run(context.Background(), deployLog.ID); err != nil {
		t.Fatal(err)
	}

	l, _ := store.FindLog(deployLog.ID)
	if l.Status != "success" || l.CurrentStep == nil || *l.CurrentStep != "done" {
		t.Fatalf("status=%s step=%v", l.Status, l.CurrentStep)
	}
	if l.StartedAt == nil || l.FinishedAt == nil {
		t.Fatal("started_at/finished_at not set")
	}
	steps := mustStepDetail(t, l)
	want := []string{"check_file", "docker_load", "docker_tag", "docker_push", "k8s_set_image", "rollout_wait"}
	if len(steps) != len(want) {
		t.Fatalf("steps=%v", steps)
	}
	for i, s := range steps {
		if s.Step != want[i] || !s.Success {
			t.Fatalf("step %d = %+v", i, s)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "app-v1.2.0.tar.gz")); !os.IsNotExist(err) {
		t.Fatal("source file was not removed")
	}
	if *l.ImageFull != image {
		t.Fatalf("image_full=%v", *l.ImageFull)
	}
}

func TestDeployMissingFileFails(t *testing.T) {
	cfg := testConfig(t.TempDir())
	store := NewStore()
	pipeline := testPipeline(cfg, store, &MockRunner{})

	deployLog, _ := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "myapp", DeploymentName: "myapp", ContainerName: "myapp",
		Namespace: "default", Version: "v1", FileName: "missing.tar.gz",
		DeployType: "deploy", Status: "pending",
	})
	if err := pipeline.Run(context.Background(), deployLog.ID); err != nil {
		t.Fatal(err)
	}

	l, _ := store.FindLog(deployLog.ID)
	if l.Status != "failed" {
		t.Fatalf("status=%s", l.Status)
	}
	if l.ErrorMessage == nil || !strings.Contains(*l.ErrorMessage, "文件不存在") {
		t.Fatalf("error_message=%v", l.ErrorMessage)
	}
	steps := mustStepDetail(t, l)
	if len(steps) != 1 || steps[0].Step != "check_file" || steps[0].Success {
		t.Fatalf("steps=%+v", steps)
	}
	if l.FinishedAt == nil {
		t.Fatal("finished_at not set")
	}
}

func TestRollbackHasNoDockerSteps(t *testing.T) {
	cfg := testConfig(t.TempDir())
	store := NewStore()
	runner := &MockRunner{}
	pipeline := testPipeline(cfg, store, runner)

	target := "reg.example.com/Demo/myapp:v0.9"
	deployLog, _ := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "myapp", DeploymentName: "myapp", ContainerName: "myapp",
		Namespace: "default", Version: "v0.9", FileName: "rollback",
		ImageFull: &target, DeployType: "rollback", Status: "pending",
	})
	if err := pipeline.Run(context.Background(), deployLog.ID); err != nil {
		t.Fatal(err)
	}

	l, _ := store.FindLog(deployLog.ID)
	if l.Status != "success" {
		t.Fatalf("status=%s err=%v", l.Status, l.ErrorMessage)
	}
	steps := mustStepDetail(t, l)
	if len(steps) != 3 || steps[0].Step != "query_current" || steps[1].Step != "k8s_set_image" || steps[2].Step != "rollout_wait" {
		t.Fatalf("steps=%+v", steps)
	}
	if !strings.Contains(steps[0].Stdout, "mock-current") {
		t.Fatalf("query_current stdout=%q", steps[0].Stdout)
	}
	for _, call := range runner.Recorded() {
		if strings.Contains(call, "docker") {
			t.Fatalf("rollback ran docker: %s", call)
		}
	}
}

func TestWorkerDispatchAndClose(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app-v1.tar.gz"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(dir)
	store := NewStore()
	runner := &MockRunner{}
	worker := NewWorker(testPipeline(cfg, store, runner), 2)
	defer worker.Close()

	deployLog, _ := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "a", DeploymentName: "a", ContainerName: "a",
		Namespace: "default", Version: "v1", FileName: "app-v1.tar.gz",
		DeployType: "deploy", Status: "pending",
	})
	if err := worker.Submit(context.Background(), deployLog.ID); err != nil {
		t.Fatal(err)
	}
	if l := waitLog(t, store, deployLog.ID); l.Status != "success" {
		t.Fatalf("deploy status=%s", l.Status)
	}

	target := "img:v0"
	rb, _ := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "a", DeploymentName: "a", ContainerName: "a",
		Namespace: "default", Version: "v0", FileName: "rollback",
		ImageFull: &target, DeployType: "rollback", Status: "pending",
	})
	if err := worker.Submit(context.Background(), rb.ID); err != nil {
		t.Fatal(err)
	}
	l := waitLog(t, store, rb.ID)
	if l.Status != "success" {
		t.Fatalf("rollback status=%s", l.Status)
	}
	steps := mustStepDetail(t, l)
	if steps[0].Step != "query_current" {
		t.Fatalf("rollback dispatched to wrong pipeline: %+v", steps)
	}
}

func TestWorkerScannerRecoversMissedSubmitWakeup(t *testing.T) {
	store := NewStore()
	worker := NewWorker(testPipeline(testConfig(t.TempDir()), store, &MockRunner{}), 1)
	defer worker.Close()

	image := "img:v1"
	deployLog, err := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "a", DeploymentName: "a", ContainerName: "a",
		Namespace: "default", Version: "v1", FileName: "rollback",
		ImageFull: &image, DeployType: "rollback", Status: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The task is durable but no Submit wakeup is sent. The periodic scanner
	// must rediscover it and deliver it to the worker without a process restart.
	log := waitLog(t, store, deployLog.ID)
	if log.Status != "success" {
		t.Fatalf("missed-wakeup task status=%s err=%v", log.Status, log.ErrorMessage)
	}
	if steps := mustStepDetail(t, log); len(steps) == 0 || steps[0].Step != "query_current" {
		t.Fatalf("scanner dispatched task to wrong pipeline: %+v", steps)
	}
}

func TestWorkerSubmitAfterClose(t *testing.T) {
	worker := NewWorker(testPipeline(testConfig(t.TempDir()), NewStore(), &MockRunner{}), 1)
	worker.Close()
	if err := worker.Submit(context.Background(), 1); !errors.Is(err, ErrWorkerClosed) {
		t.Fatalf("err=%v", err)
	}
}

type panicRunner struct{ MockRunner }

func (*panicRunner) Run(context.Context, string, []string, io.Reader) (string, string, error) {
	panic("boom")
}

func TestWorkerPanicMarksLogFailed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app-v1.tar.gz"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(dir)
	store := NewStore()
	k8s := NewK8sService(cfg, &panicRunner{}, nil)
	worker := NewWorker(NewPipeline(cfg, store, &panicRunner{}, k8s), 1)
	defer worker.Close()

	deployLog, _ := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "a", DeploymentName: "a", ContainerName: "a",
		Namespace: "default", Version: "v1", FileName: "app-v1.tar.gz",
		DeployType: "deploy", Status: "pending",
	})
	if err := worker.Submit(context.Background(), deployLog.ID); err != nil {
		t.Fatal(err)
	}
	l := waitLog(t, store, deployLog.ID)
	if l.Status != "failed" || l.ErrorMessage == nil || !strings.Contains(*l.ErrorMessage, "panic") {
		t.Fatalf("status=%s err=%v", l.Status, l.ErrorMessage)
	}
}

func TestK8sMockIgnoresRealClientError(t *testing.T) {
	svc := &K8sService{
		mock:   true,
		runner: &MockRunner{},
		cipher: nil, // would fail if the mock path resolved a real client
	}
	rt, err := svc.For(Cluster{K8sMasterUser: "kubeconfig", ConnectionType: "kubeconfig", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	pods, err := rt.Pods(context.Background(), "dev-demo-002")
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != len(mockServices) {
		t.Fatalf("pods=%d", len(pods))
	}
}

func TestK8sForConnectionTypeGates(t *testing.T) {
	svc := &K8sService{runner: &MockRunner{}}

	// disabled cluster fails before any connection attempt — in every mode
	if _, err := svc.For(Cluster{Name: "x", Enabled: false, ConnectionType: "legacy_ssh"}); err == nil {
		t.Fatal("disabled cluster must fail")
	}
	if _, err := (&K8sService{mock: true}).For(Cluster{Name: "x", Enabled: false}); err == nil {
		t.Fatal("disabled cluster must fail even in mock mode")
	}
	if _, err := (&K8sService{inject: MockKubernetes{}}).For(Cluster{Name: "x", Enabled: false}); err == nil {
		t.Fatal("disabled cluster must fail even with injected runtime")
	}
	// unknown connection type fails
	if _, err := svc.For(Cluster{Name: "x", Enabled: true, ConnectionType: "telepathy"}); err == nil {
		t.Fatal("unknown connection_type must fail")
	}
	// direct cluster without credential fails — never falls back to mock/ssh
	if _, err := svc.For(Cluster{Name: "x", Enabled: true, ConnectionType: "kubeconfig"}); err == nil {
		t.Fatal("kubeconfig without credential must fail")
	}
	if _, err := svc.For(Cluster{Name: "x", Enabled: true, ConnectionType: "token"}); err == nil {
		t.Fatal("token without credential must fail")
	}
	// legacy_ssh resolves to the ssh runtime without touching credentials
	rt, err := svc.For(Cluster{Name: "x", Enabled: true, ConnectionType: "legacy_ssh", K8sMasterHost: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := rt.(*sshKubernetes); !ok {
		t.Fatalf("legacy_ssh runtime = %T", rt)
	}
	// empty connection_type behaves as legacy for pre-migration rows
	if _, err := svc.For(Cluster{Name: "x", Enabled: true}); err != nil {
		t.Fatalf("empty connection_type should be legacy_ssh: %v", err)
	}
}

func TestK8sTestConnectionMockAndDisabled(t *testing.T) {
	mock := &K8sService{mock: true}
	v, err := mock.TestConnection(context.Background(), Cluster{Enabled: false})
	if err != nil || v != "mock" {
		t.Fatalf("mock TestConnection = %q,%v", v, err)
	}
	svc := &K8sService{runner: &MockRunner{}}
	if _, err := svc.TestConnection(context.Background(), Cluster{Enabled: false, ConnectionType: "legacy_ssh"}); err == nil {
		t.Fatal("disabled cluster must fail TestConnection")
	}
}

func TestStepResultTruncatesUTF8Safely(t *testing.T) {
	long := strings.Repeat("中", 1500) // 4500 bytes
	r := NewStepResult("s", true, long, "x")
	if len(r.Stdout) > 2000 {
		t.Fatalf("stdout len=%d", len(r.Stdout))
	}
	if !utf8.ValidString(r.Stdout) {
		t.Fatal("stdout has broken UTF-8")
	}
	if !strings.HasSuffix(long, r.Stdout) {
		t.Fatal("stdout is not a suffix of the original")
	}
}

// failExecPersistence lets reads/inserts succeed but fails every execution
// update, simulating a DB that dies after the log row is created.
type failExecPersistence struct{ err error }

func (f failExecPersistence) SaveCluster(c Cluster) (Cluster, error) { return c, nil }
func (f failExecPersistence) UpdateCluster(Cluster) error            { return nil }
func (f failExecPersistence) DeleteCluster(int) error                { return nil }
func (f failExecPersistence) SaveApp(a App) (App, error)             { return a, nil }
func (f failExecPersistence) UpdateApp(App) error                    { return nil }
func (f failExecPersistence) DeleteApp(int) error                    { return nil }
func (f failExecPersistence) SaveLog(l DeployLog) (DeployLog, error) { return l, nil }
func (f failExecPersistence) UpdateLogExecution(DeployLog) error     { return f.err }
func (f failExecPersistence) CreateImageRelease(r ImageRelease, l DeployLog) (ImageRelease, DeployLog, bool, error) {
	return r, l, true, nil
}
func (f failExecPersistence) PromoteImageRelease(_ int64, _ int, l DeployLog) (ImageRelease, DeployLog, error) {
	return ImageRelease{}, l, nil
}
func (f failExecPersistence) SaveUser(u User) (User, error) { return u, nil }
func (f failExecPersistence) UpdateUser(User) error         { return nil }
func (f failExecPersistence) SaveAudit(AuditEntry) error    { return nil }
func (f failExecPersistence) SavePromotionRequest(p PromotionRequest) (PromotionRequest, error) {
	return p, nil
}
func (f failExecPersistence) UpdatePromotionRequest(PromotionRequest) error { return nil }
func (f failExecPersistence) SaveAPIKey(k APIKey) (APIKey, error) {
	return k, nil
}
func (f failExecPersistence) UpdateAPIKey(APIKey) error          { return nil }
func (f failExecPersistence) DeleteAPIKey(int64) error           { return nil }
func (f failExecPersistence) TouchAPIKey(int64, time.Time) error { return nil }

func TestPipelinePersistFailureAbortsBeforeAnyAction(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app-v1.tar.gz")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("db write failed")
	store := NewStoreWithData(nil, nil, nil, nil, failExecPersistence{err: boom})
	cfg := testConfig(dir)
	runner := &MockRunner{}
	pipeline := testPipeline(cfg, store, runner)

	image := "reg/Demo/a:v1"
	deployLog, err := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "a", DeploymentName: "a", ContainerName: "a",
		Namespace: "default", Version: "v1", FileName: "app-v1.tar.gz",
		ImageFull: &image, DeployType: "deploy", Status: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Run(context.Background(), deployLog.ID); !errors.Is(err, boom) {
		t.Fatalf("Run err=%v, want persistence error", err)
	}
	if calls := runner.Recorded(); len(calls) != 0 {
		t.Fatalf("runner was invoked despite persist failure: %v", calls)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("source file must remain when the success update was never persisted")
	}
	l, _ := store.FindLog(deployLog.ID)
	if l.Status != "pending" {
		t.Fatalf("memory log status=%s, want pending", l.Status)
	}
}

func TestGatewayCanaryWorkerFailsClosedBeforeExternalActions(t *testing.T) {
	cfg := testConfig(t.TempDir())
	runner := &MockRunner{}
	log := DeployLog{
		ID: 31, ClusterID: 1, AppName: "orders-api", DeploymentName: "orders-api", ContainerName: "api",
		Namespace: "production", DeployType: "image_deploy", Status: "pending", StepDetail: "[]",
	}
	store := NewStoreWithData(
		[]Cluster{{ID: 1, Name: "production", EnvironmentTier: "testing", DeployPolicy: "automatic", Enabled: true}},
		nil, []DeployLog{log}, nil, nil,
	)
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: log.ID,
		Strategy: "gateway_canary", Status: "queued", CurrentStage: "queued", Version: 1,
	}}, []DeliveryRunEvent{{
		ID: 1, RunID: 8, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued",
	}})

	if err := testPipeline(cfg, store, runner).Run(context.Background(), log.ID); err != nil {
		t.Fatalf("closed canary gate should persist a terminal failure: %v", err)
	}
	updatedLog, _ := store.FindLog(log.ID)
	run, _ := store.DeliveryRun(8)
	events, _ := store.DeliveryRunEvents(8)
	actions, _ := store.ExecutionActions(log.ID)
	if updatedLog.Status != "failed" || updatedLog.ErrorMessage == nil || !strings.Contains(*updatedLog.ErrorMessage, "strategy_unavailable") ||
		run.Status != "failed" || run.CurrentStage != "strategy_unavailable" || len(events) != 2 || len(actions) != 0 || len(runner.Recorded()) != 0 {
		t.Fatalf("canary gate allowed work or lost its failure state: log=%+v run=%+v events=%+v actions=%+v runner=%+v",
			updatedLog, run, events, actions, runner.Recorded())
	}
}

func TestStoreConflictChecks(t *testing.T) {
	store := NewStoreWithData(
		[]Cluster{{ID: 1, EnvCode: "a"}, {ID: 2, EnvCode: "b"}},
		[]App{{ID: 1, ClusterID: 1, AppName: "x"}},
		nil, nil, nil)

	if _, err := store.SaveCluster(Cluster{EnvCode: "a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("SaveCluster dup err=%v", err)
	}
	if _, err := store.UpdateCluster(Cluster{ID: 2, EnvCode: "a"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("UpdateCluster dup err=%v", err)
	}
	if _, err := store.UpdateCluster(Cluster{ID: 2, EnvCode: "b"}); err != nil {
		t.Fatalf("self-update err=%v", err)
	}
	if _, err := store.SaveApp(App{ClusterID: 1, AppName: "x"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("SaveApp dup err=%v", err)
	}
	if _, err := store.SaveApp(App{ClusterID: 2, AppName: "x"}); err != nil {
		t.Fatalf("same app_name in another cluster must pass: %v", err)
	}
	if _, err := store.UpdateApp(App{ID: 1, ClusterID: 1, AppName: "x"}); err != nil {
		t.Fatalf("app self-update err=%v", err)
	}
}

func TestSafeTransferFile(t *testing.T) {
	dir := "/data/transfer_space"
	if p, ok := SafeTransferFile(dir, "app.tar.gz"); !ok || p != filepath.Join(dir, "app.tar.gz") {
		t.Fatalf("valid name rejected: %q", p)
	}
	for _, bad := range []string{"", "..", "../x", "/etc/passwd", "a/b", "a/../b", "."} {
		if _, ok := SafeTransferFile(dir, bad); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestCleanupStopIdempotent(t *testing.T) {
	s := NewCleanupService(t.TempDir(), 3)
	s.Start()
	s.Stop()
	s.Stop()

	neverStarted := NewCleanupService(t.TempDir(), 3)
	neverStarted.Stop()
}

func TestShellJoin(t *testing.T) {
	if got := shellJoin(nil); got != "" {
		t.Fatalf("empty args → %q", got)
	}
	if got := shellJoin([]string{"a'b"}); got != `'a'\''b'` {
		t.Fatalf("quote escape → %q", got)
	}
	if got := shellJoin([]string{"kubectl", "get", "pods", "-n", "kube-system"}); got != `'kubectl' 'get' 'pods' '-n' 'kube-system'` {
		t.Fatalf("join → %q", got)
	}
}

func TestRunSSHTerminatesOptionParsing(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "argv.txt")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\" >> '" + capture + "'; done\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, _, err := OSRunner{}.RunSSH(context.Background(),
		Cluster{K8sMasterUser: "u", K8sMasterHost: "-oProxyCommand=evil", K8sMasterPort: 22},
		[]string{"kubectl", "get", "pods"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimSpace(string(data)), "\n")
	dashDash, dest := -1, -1
	for i, a := range argv {
		if a == "--" {
			dashDash = i
		}
		if a == "u@-oProxyCommand=evil" {
			dest = i
		}
	}
	if dashDash < 0 || dest < 0 || dashDash != dest-1 {
		t.Fatalf("argv %v: '--' must immediately precede the destination", argv)
	}
}

func TestPipelineRejectsSymlinkFile(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "real.tar.gz")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "evil.tar.gz")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(dir)
	store := NewStore()
	runner := &MockRunner{}
	pipeline := testPipeline(cfg, store, runner)

	image := "reg/Demo/a:v1"
	deployLog, err := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "a", DeploymentName: "a", ContainerName: "a",
		Namespace: "default", Version: "v1", FileName: "evil.tar.gz",
		ImageFull: &image, DeployType: "deploy", Status: "pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pipeline.Run(context.Background(), deployLog.ID); err != nil {
		t.Fatal(err)
	}
	l, _ := store.FindLog(deployLog.ID)
	if l.Status != "failed" || l.CurrentStep == nil || *l.CurrentStep != "check_file" {
		t.Fatalf("log = %s/%v, want failed at check_file", l.Status, l.CurrentStep)
	}
	if calls := runner.Recorded(); len(calls) != 0 {
		t.Fatalf("runner invoked on symlink: %v", calls)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink must not be removed")
	}
}

func (f failPersistence) ApprovePromotion(p PromotionRequest, l DeployLog) (PromotionRequest, ImageRelease, DeployLog, error) {
	return p, ImageRelease{}, l, errors.New("atomic promotion unavailable in failing persistence")
}
func (f failPersistence) UpdateReleaseMetadata(int64, ReleaseMetadata) error { return nil }
func (f failPersistence) ClaimLog(int64) (bool, error)                       { return true, nil }

func (f failExecPersistence) ApprovePromotion(p PromotionRequest, l DeployLog) (PromotionRequest, ImageRelease, DeployLog, error) {
	return p, ImageRelease{}, l, errors.New("atomic promotion unavailable in failing persistence")
}
func (f failExecPersistence) UpdateReleaseMetadata(int64, ReleaseMetadata) error { return nil }
func (f failExecPersistence) ClaimLog(int64) (bool, error)                       { return true, nil }
