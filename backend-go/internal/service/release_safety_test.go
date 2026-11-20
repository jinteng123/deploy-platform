package service

import (
	"context"
	"deploy-platform/backend-go/internal/config"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIngressPinsDigestBeforeExecutionAndDuplicateDoesNotResolveAgain(t *testing.T) {
	cfg, store, _, rec, worker, _ := cdFixture(t)
	cfg.RegistryHost = "reg.io"
	cd := NewCDService(cfg, store, worker)
	calls := 0
	first := "sha256:" + strings.Repeat("ab", 32)
	cd.resolveDigest = func(context.Context, config.Config, string) (string, error) { calls++; return first, nil }
	ev := cdEvent()
	ev.ImageDigest = nil
	release, l, created, err := cd.Receive(context.Background(), ev)
	if err != nil || !created {
		t.Fatalf("receive: %v", err)
	}
	if release.ImageDigest == nil || *release.ImageDigest != first || release.Metadata.DigestSource != "registry" {
		t.Fatalf("identity not pinned: %+v", release)
	}
	waitLog(t, store, l.ID)
	cd.resolveDigest = func(context.Context, config.Config, string) (string, error) {
		calls++
		return "sha256:" + strings.Repeat("cd", 32), nil
	}
	dup, _, created, err := cd.Receive(context.Background(), ev)
	if err != nil || created || *dup.ImageDigest != first || calls != 1 {
		t.Fatalf("duplicate re-resolved identity: %+v %v calls=%d", dup, err, calls)
	}
	attestServiceRelease(t, store, release.ID)
	_, prod, err := promoteForTest(t, cd, release.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	waitLog(t, store, prod.ID)
	got := rec.recorded()
	if !strings.HasSuffix(got[len(got)-1], "@"+first) {
		t.Fatalf("tag drift: %v", got)
	}
}

func TestIngressRejectsUnknownRegistryAndMismatchedDigest(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	ev := cdEvent()
	ev.ImageDigest = nil
	if _, _, _, err := cd.Receive(context.Background(), ev); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown mutable registry: %v", err)
	}
	ev = cdEvent()
	ev.ImageFull = "reg.io/Demo/app@sha256:" + strings.Repeat("cd", 32)
	if _, _, _, err := cd.Receive(context.Background(), ev); !errors.Is(err, ErrValidation) {
		t.Fatalf("digest mismatch: %v", err)
	}
	if len(store.Logs(0, "", "", 0)) != 0 {
		t.Fatal("rejected input created a task")
	}
	for _, d := range []string{"sha256:abc", "sha512:" + strings.Repeat("ab", 32), strings.Repeat("ab", 31)} {
		if NormalizeDigest(d) != "" {
			t.Fatalf("accepted malformed digest: %s", d)
		}
	}
}

func TestWorkloadMutexIgnoresAliasAndContainerButRespectsNamespace(t *testing.T) {
	store := NewStore()
	a, err := store.AddDeployLog(DeployLog{ClusterID: 1, AppName: "alias-a", Namespace: "one", DeploymentName: "shared", ContainerName: "main", Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AddDeployLog(DeployLog{ClusterID: 1, AppName: "alias-b", Namespace: "one", DeploymentName: "shared", ContainerName: "sidecar", Status: "pending"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("alias bypassed log %d: %v", a.ID, err)
	}
	_, err = store.AddDeployLog(DeployLog{ClusterID: 1, AppName: "alias-b", Namespace: "two", DeploymentName: "shared", Status: "pending"})
	if err != nil {
		t.Fatalf("independent namespace blocked: %v", err)
	}
}

func TestMultiResourceLocksConflictAndReleaseWithTask(t *testing.T) {
	store := NewStore()
	serviceLock := ResourceLock{ClusterID: 1, Namespace: "one", Kind: "service", Name: "entry"}
	first, err := store.AddDeployLogWithLocks(DeployLog{ClusterID: 1, AppName: "blue", Namespace: "one", DeploymentName: "blue", Status: "pending"}, []ResourceLock{serviceLock})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AddDeployLogWithLocks(DeployLog{ClusterID: 1, AppName: "green", Namespace: "one", DeploymentName: "green", Status: "pending"}, []ResourceLock{serviceLock})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("shared Service lock was not enforced: %v", err)
	}
	if err := store.CheckManagedWrite(1, "services", "one", "entry"); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy Service writer bypassed active delivery lock: %v", err)
	}
	if err := store.CheckManagedWrite(1, "deployments", "one", "blue"); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy Deployment writer bypassed active delivery lock: %v", err)
	}
	first.Status = "success"
	if err := store.UpdateLogExecution(first); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckManagedWrite(1, "services", "one", "entry"); err != nil {
		t.Fatalf("terminal task retained Service lock: %v", err)
	}
	if _, err := store.AddDeployLogWithLocks(DeployLog{ClusterID: 1, AppName: "green", Namespace: "one", DeploymentName: "green", Status: "pending"}, []ResourceLock{serviceLock}); err != nil {
		t.Fatalf("terminal task retained resource ownership: %v", err)
	}
}

func TestRecoveryRequeuesOnlyNeverClaimedTasks(t *testing.T) {
	pending := DeployLog{ID: 1, ClusterID: 1, Namespace: "ns", DeploymentName: "pending-app", Status: "pending", DeployType: "image_deploy"}
	running := DeployLog{ID: 2, ClusterID: 1, Namespace: "ns", DeploymentName: "running-app", Status: "running", DeployType: "image_deploy"}
	store := NewStoreWithData(nil, nil, []DeployLog{pending, running}, nil, nil)
	worker := newWorker(nil, 1, false)
	defer worker.Close()
	if err := RecoverDeploys(context.Background(), store, worker); err != nil {
		t.Fatal(err)
	}
	actual, _ := store.FindLog(running.ID)
	if actual.Status != "interrupted" || actual.ErrorMessage == nil || !strings.Contains(*actual.ErrorMessage, "结果未知") {
		t.Fatalf("running task was blindly resumed: %+v", actual)
	}
	select {
	case got := <-worker.jobs:
		if got != pending.ID {
			t.Fatalf("queued log %d, want only pending log %d", got, pending.ID)
		}
	default:
		t.Fatal("durable pending task was not requeued")
	}
	select {
	case got := <-worker.jobs:
		t.Fatalf("interrupted task %d was requeued", got)
	default:
	}
}

func TestPromotionRequiresFreshEvidenceForExactDigest(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	r := seedRelease(store, "success", nil)
	r.Metadata.Verification = nil
	store.releases = append(store.releases, r)
	if _, err := cd.RequestPromotion(r.ID, 2, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("rollout alone approved: %v", err)
	}
	evidence := VerificationEvidence{Digest: "sha256:" + strings.Repeat("ab", 32), Status: "passed", Kind: "ci", RecordedBy: "apikey:ci", ValidUntil: time.Now().Add(time.Hour)}
	if _, err := store.UpdateReleaseEvidence(r.ID, evidence, false); !errors.Is(err, ErrValidation) {
		t.Fatalf("foreign report accepted: %v", err)
	}
	evidence.Digest = *r.ImageDigest
	evidence.Status = "failed"
	if _, err := store.UpdateReleaseEvidence(r.ID, evidence, false); err != nil {
		t.Fatal(err)
	}
	if _, err := cd.RequestPromotion(r.ID, 2, "alice"); !errors.Is(err, ErrValidation) {
		t.Fatalf("failed report approved: %v", err)
	}
	attestServiceRelease(t, store, r.ID)
	req, err := cd.RequestPromotion(r.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := cd.ApprovePromotion(context.Background(), req.ID, "apikey:other"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("machine approval accepted: %v", err)
	}
	evidence.Status = "passed"
	evidence.Summary = "new report"
	if _, err := store.UpdateReleaseEvidence(r.ID, evidence, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cd.ApprovePromotion(context.Background(), req.ID, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale snapshot approved: %v", err)
	}
}

func TestReleaseEvidenceHistoryPreservesReportsAndDeduplicatesRetries(t *testing.T) {
	_, store, _, _, _, _ := cdFixture(t)
	release := seedRelease(store, "success", nil)
	release.Metadata.Verification = nil
	release.Metadata.VerificationHistory = nil
	store.releases = append(store.releases, release)
	validUntil := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	ci := VerificationEvidence{
		Digest: *release.ImageDigest, Status: "passed", Kind: "ci", RecordedBy: "runner:4",
		Summary: "CI 测试通过", ValidUntil: validUntil,
	}
	if _, err := store.UpdateReleaseEvidence(release.ID, ci, false); err != nil {
		t.Fatal(err)
	}
	first, err := store.FindImageRelease(release.ID)
	if err != nil || len(first.Metadata.VerificationHistory) != 1 {
		t.Fatalf("first evidence history=%+v err=%v", first.Metadata.VerificationHistory, err)
	}
	firstRecordedAt := first.Metadata.VerificationHistory[0].RecordedAt
	if _, err := store.UpdateReleaseEvidence(release.ID, ci, false); err != nil {
		t.Fatal(err)
	}
	duplicate, _ := store.FindImageRelease(release.ID)
	if len(duplicate.Metadata.VerificationHistory) != 1 || !duplicate.Metadata.VerificationHistory[0].RecordedAt.Equal(firstRecordedAt) {
		t.Fatalf("exact retry appended or refreshed evidence: %+v", duplicate.Metadata.VerificationHistory)
	}

	smoke := VerificationEvidence{
		Digest: *release.ImageDigest, Status: "failed", Kind: "smoke", RecordedBy: "runner:4",
		Summary: "健康检查失败", ValidUntil: validUntil,
	}
	if _, err := store.UpdateReleaseEvidence(release.ID, smoke, false); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.FindImageRelease(release.ID)
	if len(updated.Metadata.VerificationHistory) != 2 || updated.Metadata.Verification == nil || updated.Metadata.Verification.Kind != "smoke" || updated.Metadata.Verification.Status != "failed" {
		t.Fatalf("latest evidence or history not preserved: %+v", updated.Metadata)
	}
	if _, err := store.UpdateReleaseEvidence(release.ID, ci, false); err != nil {
		t.Fatal(err)
	}
	replayed, _ := store.FindImageRelease(release.ID)
	if len(replayed.Metadata.VerificationHistory) != 2 || replayed.Metadata.Verification == nil || replayed.Metadata.Verification.Kind != "smoke" {
		t.Fatalf("replay changed latest evidence: %+v", replayed.Metadata)
	}

	scan := VerificationEvidence{
		Digest: *release.ImageDigest, Status: "passed", Kind: "scan", RecordedBy: "scanner:1",
		Summary: "没有高危漏洞", ValidUntil: validUntil,
	}
	if _, err := store.UpdateReleaseEvidence(release.ID, scan, true); err != nil {
		t.Fatal(err)
	}
	withScan, _ := store.FindImageRelease(release.ID)
	if len(withScan.Metadata.VerificationHistory) != 2 || len(withScan.Metadata.ScanHistory) != 1 || withScan.Metadata.Verification.Kind != "smoke" {
		t.Fatalf("scan should have a separate history: %+v", withScan.Metadata)
	}
}

func TestApprovalAndRejectionHaveOnlyOneDecision(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	r := seedRelease(store, "success", nil)
	store.releases = append(store.releases, r)
	req, err := cd.RequestPromotion(r.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	results := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, _, err := cd.ApprovePromotion(context.Background(), req.ID, "bob")
		results <- err
	}()
	go func() { defer wg.Done(); _, err := cd.RejectPromotion(req.ID, "carol", "reject"); results <- err }()
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful decisions=%d", success)
	}
	decided, _ := store.FindPromotionRequest(req.ID)
	if decided.Status == "rejected" && decided.DeployLogID != nil {
		t.Fatal("rejected request has deploy")
	}
}

type failAtomicPromotion struct{ failExecPersistence }

func (f failAtomicPromotion) ApprovePromotion(p PromotionRequest, l DeployLog) (PromotionRequest, ImageRelease, DeployLog, error) {
	return p, ImageRelease{}, l, errors.New("transaction failed")
}

func TestApprovalTransactionFailureDoesNotCreateOrExecuteTask(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	r := seedRelease(store, "success", nil)
	store.releases = append(store.releases, r)
	req, err := cd.RequestPromotion(r.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	before := len(store.Logs(0, "", "", 0))
	store.persistence = failAtomicPromotion{}
	if _, _, err := cd.ApprovePromotion(context.Background(), req.ID, "bob"); err == nil {
		t.Fatal("transaction failure hidden")
	}
	if len(store.Logs(0, "", "", 0)) != before || len(rec.recorded()) != 0 {
		t.Fatal("failed transaction executed task")
	}
	current, _ := store.FindPromotionRequest(req.ID)
	if current.Status != "pending" {
		t.Fatalf("memory decision mutated: %s", current.Status)
	}
}

func TestSameArtifactCanDeployToMultipleProductionTargets(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	r := seedRelease(store, "success", nil)
	store.releases = append(store.releases, r)
	c := store.Clusters()[1]
	c.ID = 3
	c.EnvCode = "prod-b"
	if _, err := store.SaveCluster(c); err != nil {
		t.Fatal(err)
	}
	app := store.Apps(2)[0]
	app.ID = 3
	app.ClusterID = 3
	app.DeploymentName = "app-prod-b"
	if _, err := store.SaveApp(app); err != nil {
		t.Fatal(err)
	}
	model := deliveryModelForPlan("rolling")
	model.Bindings = append(model.Bindings, DeliveryBinding{
		ID: 43, AppID: 3, ApplicationID: 21, ComponentID: 31, EnvironmentID: 12, StrategyDraft: "rolling",
	})
	store.LoadDeliveryModel(model)
	seedCDApprovalScopes(t, store, 21, 12)
	store.LoadDeliveryEnvironmentEvidencePolicyVersions([]DeliveryEnvironmentEvidencePolicyVersion{testEnvironmentEvidencePolicy(12)})
	for _, clusterID := range []int{2, 3} {
		_, l, err := promoteForTest(t, cd, r.ID, clusterID)
		if err != nil {
			t.Fatal(err)
		}
		waitLog(t, store, l.ID)
	}
	if _, _, err := promoteForTest(t, cd, r.ID, 2); !errors.Is(err, ErrConflict) {
		t.Fatalf("successful target not protected: %v", err)
	}
	if got := store.ReleaseDeployments(r.ID); len(got) != 3 {
		t.Fatalf("history lost: %+v", got)
	}
}

func TestRollbackAcceptsOnlyHistoricalPinnedTarget(t *testing.T) {
	store := NewStore()
	tag := "reg.io/app:v1"
	old, _ := store.AddLog(DeployLog{ClusterID: 1, Namespace: "default", DeploymentName: "app", Status: "success", ImageFull: &tag})
	if _, err := store.RollbackLog(1, old.ID); !errors.Is(err, ErrValidation) {
		t.Fatalf("tag rollback allowed: %v", err)
	}
	pinned := "reg.io/app@sha256:" + strings.Repeat("ab", 32)
	old.ImageFull = &pinned
	_ = store.UpdateLogExecution(old)
	if _, err := store.RollbackLog(2, old.ID); err == nil {
		t.Fatal("cross-environment rollback accepted")
	}
	l, err := store.RollbackLog(1, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if l.RollbackTargetID != old.ID || l.ImageFull == nil || *l.ImageFull != pinned {
		t.Fatalf("incorrect rollback: %+v", l)
	}
}

func TestWorkerClaimsDuplicateWakeupsOnlyOnce(t *testing.T) {
	_, store, _, rec, worker, _ := cdFixture(t)
	image := "reg.io/app@sha256:" + strings.Repeat("ab", 32)
	l, err := store.AddDeployLog(DeployLog{ClusterID: 1, Namespace: "ns", DeploymentName: "app", ContainerName: "app", ImageFull: &image, Status: "pending", DeployType: "image_deploy"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if err := worker.Submit(context.Background(), l.ID); err != nil {
			t.Fatal(err)
		}
	}
	worker.Close()
	if len(rec.recorded()) != 1 {
		t.Fatalf("duplicate side effects: %v", rec.recorded())
	}
	got, _ := store.FindLog(l.ID)
	if got.Status != "success" {
		t.Fatal(fmt.Sprintf("task=%+v", got))
	}
}

func promoteForTest(t *testing.T, cd *CDService, id int64, cluster int) (ImageRelease, DeployLog, error) {
	t.Helper()
	req, err := cd.RequestPromotion(id, cluster, "test-requester")
	if err != nil {
		return ImageRelease{}, DeployLog{}, err
	}
	_, l, err := cd.ApprovePromotion(context.Background(), req.ID, "test-approver")
	r, _ := cd.store.FindImageRelease(id)
	return r, l, err
}

func TestRealProductionCannotDisableApproval(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	r := seedRelease(store, "success", nil)
	store.releases = append(store.releases, r)
	if _, _, err := cd.Promote(context.Background(), r.ID, 2); !errors.Is(err, ErrForbidden) {
		t.Fatalf("direct production bypass: %v", err)
	}
}

type lostClaimPersistence struct {
	failPersistence
	durable DeployLog
}

func (f lostClaimPersistence) ClaimLog(int64) (bool, error)     { return false, nil }
func (f lostClaimPersistence) LogByID(int64) (DeployLog, error) { return f.durable, nil }
func TestCompetingClaimDoesNotInterruptRunningTask(t *testing.T) {
	l := DeployLog{ID: 1, Status: "pending"}
	durable := l
	durable.Status = "running"
	s := NewStoreWithData(nil, nil, []DeployLog{l}, nil, lostClaimPersistence{durable: durable})
	claimed, err := s.ClaimDeployLog(1)
	if err != nil || claimed {
		t.Fatalf("ambiguous claim should not execute: %v %v", claimed, err)
	}
	actual, _ := s.FindLog(1)
	if actual.Status != "running" || actual.ErrorMessage != nil {
		t.Fatalf("a competing claim must preserve the active owner: %+v", actual)
	}
}

func TestSnapshotBindsTargetAndSurvivesTimestampPrecision(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	r := seedRelease(store, "success", nil)
	store.releases = append(store.releases, r)
	app, _ := store.FindAppByName(2, "app")
	cluster, _ := store.FindCluster(2)
	app.CreatedAt = time.Now()
	cluster.UpdatedAt = time.Now()
	before := promotionSnapshot(r, app, cluster)
	app.CreatedAt = app.CreatedAt.Truncate(time.Second)
	cluster.UpdatedAt = cluster.UpdatedAt.Truncate(time.Second)
	if promotionSnapshot(r, app, cluster) != before {
		t.Fatal("timestamp precision invalidates unchanged approval")
	}
	req, err := cd.RequestPromotion(r.ID, 2, "alice")
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	endpoint := "https://another-cluster"
	store.clusters[1].APIServer = &endpoint
	store.mu.Unlock()
	if _, _, err = cd.ApprovePromotion(context.Background(), req.ID, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed connection deployed: %v", err)
	}
}

func TestOlderArtifactMustUseExplicitRecovery(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	r := seedRelease(store, "success", nil)
	store.releases = append(store.releases, r)
	_, _ = store.AddLog(DeployLog{ClusterID: 2, ReleaseID: r.ID + 1, AppName: "app", DeploymentName: "app-prod", ContainerName: "app", Namespace: "ns-prod", Status: "success"})
	if _, err := cd.RequestPromotion(r.ID, 2, "alice"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("old artifact forward deployment: %v", err)
	}
}

func TestMockArtifactCannotEnterRealProduction(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	r := seedRelease(store, "success", nil)
	r.Metadata.DigestSource = "mock"
	store.releases = append(store.releases, r)
	if _, err := cd.RequestPromotion(r.ID, 2, "alice"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("mock artifact in real production: %v", err)
	}
}

func TestMalformedLegacyArtifactCannotReceivePassingEvidence(t *testing.T) {
	digest := "sha256:old"
	r := ImageRelease{ID: 1, ImageDigest: &digest}
	s := NewStoreWithData(nil, nil, nil, []ImageRelease{r}, nil)
	e := VerificationEvidence{Digest: digest, Kind: "ci", Status: "passed", RecordedBy: "apikey:ci", ValidUntil: time.Now().Add(time.Hour)}
	if _, err := s.UpdateReleaseEvidence(1, e, false); !errors.Is(err, ErrValidation) {
		t.Fatalf("two invalid digests treated as matching: %v", err)
	}
	r.Metadata.Verification = &e
	if err := r.Verified(time.Now()); !errors.Is(err, ErrValidation) {
		t.Fatalf("invalid artifact verified: %v", err)
	}
	for _, ref := range []string{"reg.io/app@" + strings.Repeat("ab", 32), "reg.io/app@other@sha256:" + strings.Repeat("ab", 32), "reg.io/app@SHA256:" + strings.Repeat("ab", 32), "reg.io/app@sha256:" + strings.Repeat("AB", 32)} {
		if PinnedImage(ref) {
			t.Fatalf("noncanonical historical reference accepted: %s", ref)
		}
	}
}

func TestUnexpectedImageAfterWriteInterruptsCandidate(t *testing.T) {
	_, store, _, rec, _, cd := cdFixture(t)
	foreign := "reg.io/other:v2"
	rec.forcedImage = &foreign
	_, l, _, err := cd.Receive(context.Background(), cdEvent())
	if err != nil {
		t.Fatal(err)
	}
	actual := waitLog(t, store, l.ID)
	if actual.Status != "interrupted" || actual.ErrorMessage == nil || !strings.Contains(*actual.ErrorMessage, "k8s_set_image") {
		t.Fatalf("unexpected image state was not interrupted: %+v", actual)
	}
}
func TestMalformedImageReferenceCannotChangeRepositoryDuringPinning(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	for _, ref := range []string{"reg.io/user@other/app@sha256:" + strings.Repeat("ab", 32), "https://reg.io/app:v1", "reg.io/app:v1?token=value"} {
		ev := cdEvent()
		ev.ImageFull = ref
		if _, _, _, err := cd.Receive(context.Background(), ev); !errors.Is(err, ErrValidation) {
			t.Fatalf("malformed source accepted: %v", err)
		}
	}
	if len(store.Logs(0, "", "", 0)) != 0 {
		t.Fatal("malformed source created tasks")
	}
}
