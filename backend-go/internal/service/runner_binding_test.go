package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func runnerStore(t *testing.T) (*Store, RunnerBinding, string) {
	t.Helper()
	s := NewStore()
	a, err := s.SaveApp(App{ClusterID: 1, AppName: "orders", MatchKeyword: "orders", Namespace: "default", DeploymentName: "orders", ContainerName: "api", ImageRepository: "reg.io/orders/api"})
	if err != nil {
		t.Fatal(err)
	}
	b, key, err := s.ConfigureRunner(RunnerBinding{AppID: a.ID, PipelineID: "123", PrivateClusterID: "build-1", PlatformURL: "http://deploy.internal:8000"})
	if err != nil {
		t.Fatal(err)
	}
	return s, b, key
}
func TestRunnerCredentialScopeRotationAndBindingChange(t *testing.T) {
	s, b, key := runnerStore(t)
	if _, err := s.ResolveRunner(b.ID, key); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", key + "x", "dp_" + key[5:]} {
		if _, err := s.ResolveRunner(b.ID, bad); err == nil {
			t.Fatal("bad credential accepted")
		}
	}
	if _, err := s.ResolveRunner(b.ID+1, key); err == nil {
		t.Fatal("cross-binding credential accepted")
	}
	b2, key2, err := s.ConfigureRunner(b)
	if err != nil {
		t.Fatal(err)
	}
	if key == key2 || b.TokenHash == b2.TokenHash {
		t.Fatal("rotation reused secret")
	}
	if _, err := s.ResolveRunner(b.ID, key); err == nil {
		t.Fatal("old token still usable")
	}
	if _, err := s.ResolveRunner(b.ID, key2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetRunnerEnabled(b.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveRunner(b.ID, key2); err == nil {
		t.Fatal("disabled token accepted")
	}
	s.SetRunnerEnabled(b.ID, true)
	a, _ := s.FindApp(b.AppID)
	a.DeploymentName = "different"
	if _, err = s.UpdateApp(a); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveRunner(b.ID, key2); err == nil || !strings.Contains(err.Error(), "绑定已变更") {
		t.Fatal("edited mapping did not invalidate grant")
	}
}
func TestRunnerCannotAuthorizeProductionOrUnboundRepository(t *testing.T) {
	s, b, _ := runnerStore(t)
	c, _ := s.FindCluster(1)
	c.EnvironmentTier = "production"
	c.DeployPolicy = "manual"
	s.UpdateCluster(c)
	if _, _, err := s.ConfigureRunner(b); err == nil {
		t.Fatal("production automation grant created")
	}
	c.EnvironmentTier = "non_production"
	c.DeployPolicy = "automatic"
	s.UpdateCluster(c)
	a, _ := s.FindApp(b.AppID)
	a.ImageRepository = ""
	s.UpdateApp(a)
	if _, _, err := s.ConfigureRunner(b); err == nil {
		t.Fatal("unrestricted repository granted")
	}
}
func TestRunnerConfigurationAndExpiryValidation(t *testing.T) {
	s, b, key := runnerStore(t)
	for _, bad := range []string{"http://user:pass@internal", "http://internal?token=x", "https://internal/path", "file:///etc/passwd", "https://internal/#x"} {
		candidate := b
		candidate.PlatformURL = bad
		if _, _, err := s.ConfigureRunner(candidate); err == nil {
			t.Fatalf("unsafe URL accepted: %s", bad)
		}
	}
	b.ExpiresAt = time.Now().Add(-time.Minute)
	if _, _, err := s.ConfigureRunner(b); err == nil {
		t.Fatal("expired new grant accepted")
	}
	s.mu.Lock()
	s.runnerBindings[0].ExpiresAt = time.Now().Add(-time.Minute)
	s.mu.Unlock()
	if _, err := s.ResolveRunner(b.ID, key); err == nil {
		t.Fatal("expired token accepted")
	}
}
func TestRunnerReleaseAtomicGrantRecheckAndOldBuildGuard(t *testing.T) {
	s, b, _ := runnerStore(t)
	pipeline, build, digest := b.PipelineID, "2", "sha256:"+strings.Repeat("a", 64)
	release := ImageRelease{Source: "runner:1", EventKey: "build2", EventID: "123:2:1", AppName: b.Binding.AppName, TestClusterID: 1, ImageFull: "reg.io/orders/api:v2", ImageDigest: &digest, PipelineID: &pipeline, BuildNumber: &build, RunnerAuthorizationHash: b.TokenHash, Metadata: ReleaseMetadata{Binding: &b.Binding}}
	log := DeployLog{ClusterID: 1, AppName: b.Binding.AppName, Namespace: b.Binding.Namespace, DeploymentName: b.Binding.DeploymentName, ContainerName: b.Binding.ContainerName, Status: "pending", DeployType: "image_deploy"}
	if _, _, _, err := s.CreateImageRelease(release, log); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := s.CreateImageRelease(release, log); err != nil || created {
		t.Fatalf("duplicate: %v %v", created, err)
	}
	conflicting := release
	conflicting.ImageFull = "reg.io/orders/api:changed"
	if _, _, _, err := s.CreateImageRelease(conflicting, log); err == nil {
		t.Fatal("same build image rewrite accepted")
	}
	older := release
	older.EventKey = "build1"
	olderBuild := "1"
	older.BuildNumber = &olderBuild
	if _, _, _, err := s.CreateImageRelease(older, log); err == nil || !strings.Contains(err.Error(), "旧构建") {
		t.Fatalf("old build accepted: %v", err)
	}
	s.SetRunnerEnabled(b.ID, false)
	if _, _, _, err := s.CreateImageRelease(release, log); err == nil {
		t.Fatal("grant disabled after HTTP authentication still inserted release")
	}
}

func TestRunnerConfigurationSourceIsValidatedAndImmutable(t *testing.T) {
	s, b, _ := runnerStore(t)
	app, _ := s.FindApp(b.AppID)
	app.DeploymentConfigRepoURL = "https://git.example.com/orders/deploy-config.git"
	app.DeploymentConfigPath = "charts/orders"
	if _, err := s.UpdateApp(app); err != nil {
		t.Fatal(err)
	}
	b, _, err := s.ConfigureRunner(b)
	if err != nil {
		t.Fatal(err)
	}
	pipeline, build, digest := b.PipelineID, "3", "sha256:"+strings.Repeat("a", 64)
	release, _, _, err := s.CreateImageRelease(ImageRelease{
		Source: fmt.Sprintf("runner:%d", b.ID), EventKey: "build3", EventID: "123:3:1",
		AppName: b.Binding.AppName, TestClusterID: 1, ImageFull: "reg.io/orders/api:v3",
		ImageDigest: &digest, PipelineID: &pipeline, BuildNumber: &build,
		RunnerAuthorizationHash: b.TokenHash, Metadata: ReleaseMetadata{Binding: &b.Binding},
	}, DeployLog{ClusterID: 1, AppName: b.Binding.AppName, Namespace: b.Binding.Namespace, DeploymentName: b.Binding.DeploymentName, ContainerName: b.Binding.ContainerName, Status: "pending", DeployType: "image_deploy"})
	if err != nil {
		t.Fatal(err)
	}
	valid := ConfigurationSourceRevision{Repository: "https://git.example.com/orders/deploy-config.git", Path: "charts/orders", Revision: strings.Repeat("C", 40)}
	for _, mismatch := range []ConfigurationSourceRevision{
		{Repository: "https://git.example.com/orders/other-config.git", Path: valid.Path, Revision: strings.Repeat("a", 40)},
		{Repository: valid.Repository, Path: "charts/other", Revision: strings.Repeat("a", 40)},
	} {
		if _, _, err := s.UpdateRunnerReleaseConfigurationSource(b, release.ID, mismatch); err == nil {
			t.Fatalf("component configuration mapping override accepted: %+v", mismatch)
		}
	}
	for _, invalid := range []ConfigurationSourceRevision{
		{Repository: "https://user:secret@git.example.com/orders/config.git", Path: ".", Revision: strings.Repeat("a", 40)},
		{Repository: "https://git.example.com/orders/config.git?token=secret", Path: ".", Revision: strings.Repeat("a", 40)},
		{Repository: "http://git.example.com/orders/config.git", Path: ".", Revision: strings.Repeat("a", 40)},
		{Repository: valid.Repository, Path: "../prod", Revision: strings.Repeat("a", 40)},
		{Repository: valid.Repository, Path: ".", Revision: "short"},
	} {
		if _, _, err := s.UpdateRunnerReleaseConfigurationSource(b, release.ID, invalid); err == nil {
			t.Fatalf("invalid configuration source accepted: %+v", invalid)
		}
	}
	updated, created, err := s.UpdateRunnerReleaseConfigurationSource(b, release.ID, valid)
	if err != nil || !created {
		t.Fatalf("valid configuration revision was not recorded: created=%v err=%v", created, err)
	}
	got := updated.Metadata.ConfigurationSource
	if got == nil || got.Repository != valid.Repository || got.Path != valid.Path || got.Revision != strings.ToLower(valid.Revision) || got.RecordedBy != fmt.Sprintf("runner:%d", b.ID) || got.RecordedAt.IsZero() {
		t.Fatalf("configuration revision did not use normalized source and server identity: %+v", got)
	}
	retried, created, err := s.UpdateRunnerReleaseConfigurationSource(b, release.ID, valid)
	if err != nil || created || retried.Metadata.ConfigurationSource.RecordedAt != got.RecordedAt {
		t.Fatalf("identical retry changed immutable revision: created=%v err=%v source=%+v", created, err, retried.Metadata.ConfigurationSource)
	}
	conflicting := valid
	conflicting.Revision = strings.Repeat("b", 40)
	if _, _, err := s.UpdateRunnerReleaseConfigurationSource(b, release.ID, conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("different revision overwrote artifact provenance: %v", err)
	}
}

func TestRunnerScriptsPinComponentConfigurationRepository(t *testing.T) {
	s, b, _ := runnerStore(t)
	app, _ := s.FindApp(b.AppID)
	app.DeploymentConfigRepoURL = "https://git.example.com/orders/deploy-config.git"
	app.DeploymentConfigPath = "charts/orders"
	if _, err := s.UpdateApp(app); err != nil {
		t.Fatal(err)
	}
	b, key, err := s.ConfigureRunner(b)
	if err != nil {
		t.Fatal(err)
	}
	scripts, err := GenerateRunnerScripts(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(scripts.ConfigurationVariables) != 1 || scripts.ConfigurationVariables[0] != "DEPLOY_CONFIG_WORKDIR" {
		t.Fatalf("Runner does not guide the config checkout directory: %+v", scripts.ConfigurationVariables)
	}
	for _, name := range scripts.RequiredVariables {
		if name == "DEPLOY_CONFIG_REVISION" {
			t.Fatalf("component-bound Runner should derive the revision from verified checkout: %+v", scripts.RequiredVariables)
		}
	}
	for _, expected := range []string{
		"bound_config_repository='https://git.example.com/orders/deploy-config.git'",
		"bound_config_path='charts/orders'",
		"配置仓库与组件登记不一致",
		"配置目录与组件登记不一致",
		"DEPLOY_CONFIG_WORKDIR",
		"remote','get-url','origin",
		"rev-parse','HEAD",
		"组件登记的配置目录不属于当前 checkout commit",
	} {
		if !strings.Contains(scripts.Script, expected) {
			t.Fatalf("generated script misses %q", expected)
		}
	}
	scriptPath := filepath.Join(t.TempDir(), "notify.sh")
	if err := os.WriteFile(scriptPath, []byte(scripts.Script), 0600); err != nil {
		t.Fatal(err)
	}
	baseEnv := append(os.Environ(),
		scripts.SecretVariable+"="+key,
		"PIPELINE_ID=123",
		"BUILD_NUMBER=1",
		"DEPLOY_IMAGE_FULL=reg.io/orders/api:v1",
		"DEPLOY_IMAGE_DIGEST=sha256:"+strings.Repeat("a", 64),
	)
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "charts/orders"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "charts/orders/values.yaml"), []byte("replicaCount: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", repoDir}, {"-C", repoDir, "config", "user.email", "runner-test@example.com"}, {"-C", repoDir, "config", "user.name", "Runner Test"}, {"-C", repoDir, "add", "."}, {"-C", repoDir, "commit", "-qm", "config"}, {"-C", repoDir, "remote", "add", "origin", "https://git.example.com/orders/deploy-config.git"}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("prepare config repository: %v %s", err, output)
		}
	}
	for _, tc := range []struct {
		name       string
		workdir    string
		extra      []string
		wantDetail string
	}{
		{name: "wrong checkout repository", workdir: t.TempDir(), wantDetail: "无法从配置 checkout 读取 Git 信息"},
		{name: "revision mismatch", extra: []string{"DEPLOY_CONFIG_REVISION=" + strings.Repeat("b", 40)}, wantDetail: "DEPLOY_CONFIG_REVISION 与实际 checkout HEAD 不一致"},
		{name: "repository override", extra: []string{"DEPLOY_CONFIG_REPOSITORY=https://git.example.com/orders/other.git"}, wantDetail: "配置仓库与组件登记不一致"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", scriptPath)
			cmd.Dir = repoDir
			workdir := tc.workdir
			if workdir == "" {
				workdir = repoDir
			}
			cmd.Env = append(append([]string{}, baseEnv...), append([]string{"DEPLOY_CONFIG_WORKDIR=" + workdir}, tc.extra...)...)
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), tc.wantDetail) {
				t.Fatalf("mapped configuration script did not reject %s: err=%v output=%s", tc.name, err, output)
			}
		})
	}
	if output, err := exec.Command("git", "-C", repoDir, "remote", "set-url", "origin", "git@git.example.com:orders/deploy-config.git").CombinedOutput(); err != nil {
		t.Fatalf("set SSH origin: %v %s", err, output)
	}
	sshCmd := exec.Command("bash", scriptPath)
	sshCmd.Dir = repoDir
	sshCmd.Env = append(append([]string{}, baseEnv...), "DEPLOY_CONFIG_WORKDIR="+repoDir, "DEPLOY_CONFIG_REVISION=short")
	sshOutput, sshErr := sshCmd.CombinedOutput()
	if sshErr == nil || !strings.Contains(string(sshOutput), "DEPLOY_CONFIG_REVISION 与实际 checkout HEAD 不一致") {
		t.Fatalf("SSH origin for the registered repository was not accepted: err=%v output=%s", sshErr, sshOutput)
	}
	rootBinding := b
	rootBinding.Binding.DeploymentConfigPath = "."
	rootScripts, err := GenerateRunnerScripts(rootBinding)
	if err != nil {
		t.Fatal(err)
	}
	rootScriptPath := filepath.Join(t.TempDir(), "notify-root-config.sh")
	if err := os.WriteFile(rootScriptPath, []byte(rootScripts.Script), 0600); err != nil {
		t.Fatal(err)
	}
	rootCmd := exec.Command("bash", rootScriptPath)
	rootCmd.Dir = repoDir
	rootCmd.Env = append(append([]string{}, baseEnv...), "DEPLOY_CONFIG_WORKDIR="+repoDir, "DEPLOY_CONFIG_REVISION=short")
	rootOutput, rootErr := rootCmd.CombinedOutput()
	if rootErr == nil || !strings.Contains(string(rootOutput), "DEPLOY_CONFIG_REVISION 与实际 checkout HEAD 不一致") {
		t.Fatalf("repository-root configuration path did not pass checkout validation: err=%v output=%s", rootErr, rootOutput)
	}
}

func TestRunnerEvidenceIsComponentScopedAndSmokeRequiresSuccessfulTestDeploy(t *testing.T) {
	s, binding, _ := runnerStore(t)
	pipeline, build := binding.PipelineID, "3"
	digest := "sha256:" + strings.Repeat("b", 64)
	release, log, created, err := s.CreateImageRelease(ImageRelease{
		Source: "runner:" + strconv.FormatInt(binding.ID, 10), EventKey: "build3",
		EventID: "123:3:1", AppName: binding.Binding.AppName, TestClusterID: 1,
		ImageFull: "reg.io/orders/api:v3", ImageDigest: &digest,
		PipelineID: &pipeline, BuildNumber: &build,
		RunnerAuthorizationHash: binding.TokenHash,
		Metadata:                ReleaseMetadata{Binding: &binding.Binding},
	}, DeployLog{
		ClusterID: 1, AppName: binding.Binding.AppName, Namespace: binding.Binding.Namespace,
		DeploymentName: binding.Binding.DeploymentName, ContainerName: binding.Binding.ContainerName,
		Status: "running", DeployType: "image_deploy",
	})
	if err != nil || !created {
		t.Fatalf("create Runner release: created=%v err=%v", created, err)
	}
	// Production persistence does not store a per-release copy of the Runner
	// token hash; evidence writes must still work after a repository reload.
	s.mu.Lock()
	s.releases[0].RunnerAuthorizationHash = ""
	s.mu.Unlock()
	evidence := VerificationEvidence{
		Digest: digest, Kind: "smoke", Status: "passed", Summary: "health check passed",
		RecordedBy: "runner:" + strconv.FormatInt(binding.ID, 10), ValidUntil: time.Now().Add(time.Hour),
	}
	if _, err := s.UpdateRunnerReleaseEvidence(binding, release.ID, evidence); err == nil || !strings.Contains(err.Error(), "部署成功后") {
		t.Fatalf("Smoke evidence accepted before test rollout success: %v", err)
	}
	evidence.Kind = "ci"
	if _, err := s.UpdateRunnerReleaseEvidence(binding, release.ID, evidence); err != nil {
		t.Fatalf("CI evidence rejected for own release: %v", err)
	}
	wrongBinding := binding
	wrongBinding.ID++
	if _, err := s.UpdateRunnerReleaseEvidence(wrongBinding, release.ID, evidence); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-binding evidence was not hidden: %v", err)
	}
	evidence.Kind = "manual"
	if _, err := s.UpdateRunnerReleaseEvidence(binding, release.ID, evidence); err == nil || !strings.Contains(err.Error(), "CI 或 Smoke") {
		t.Fatalf("Runner submitted manual evidence: %v", err)
	}
	evidence.Kind = "smoke"
	log.Status = "success"
	if err := s.UpdateLogExecution(log); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateRunnerReleaseEvidence(binding, release.ID, evidence); err != nil {
		t.Fatalf("Smoke evidence rejected after successful test rollout: %v", err)
	}
	newBinding, _, err := s.ConfigureRunner(binding)
	if err != nil {
		t.Fatal(err)
	}
	evidence.Status = "failed"
	if _, err := s.UpdateRunnerReleaseEvidence(binding, release.ID, evidence); !errors.Is(err, ErrRunnerCredential) {
		t.Fatalf("stale credential updated evidence after rotation: %v", err)
	}
	if _, err := s.UpdateRunnerReleaseEvidence(newBinding, release.ID, evidence); err != nil {
		t.Fatalf("current binding credential could not update its historical release: %v", err)
	}
}

func TestRunnerPipelineBackupRejectsConfigChangedDuringPreview(t *testing.T) {
	s, b, _ := runnerStore(t)
	s.SetRunnerEnabled(b.ID, false)
	if _, err := s.SaveRunnerPipelineBackup(b, "sealed-old-yaml"); err == nil {
		t.Fatal("stale preview could save backup")
	}
	fresh, _ := s.RunnerBinding(b.ID)
	if fresh.PipelineBackupCiphertext != "" {
		t.Fatal("stale backup persisted")
	}
}
