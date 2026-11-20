package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestDeliveryRunCanPauseAndContinueOnlyAtOperatorBarriers(t *testing.T) {
	_, store, _, _, _, cd := cdFixture(t)
	plan := DeliveryPlan{
		ID: 19, TargetClusterID: 2, Strategy: "blue_green", Status: "preview_only",
		Snapshot: DeliveryPlanSnapshot{Strategy: "blue_green", Target: DeliveryPlanTarget{AppName: "app", Namespace: "ns-prod"}},
	}
	store.LoadDeliveryPlans([]DeliveryPlan{plan})
	step := "awaiting_candidate_review"
	log, err := store.AddLog(DeployLog{
		ClusterID: 2, AppName: "app", DeploymentName: "app-prod", Namespace: "ns-prod",
		DeployType: "image_deploy", Status: "running", CurrentStep: &step, StepDetail: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 91, DeliveryPlanID: plan.ID, DeployLogID: log.ID, Strategy: "blue_green",
		Status: "running", CurrentStage: step, Version: 3,
	}}, []DeliveryRunEvent{{
		ID: 1, RunID: 91, Version: 1, EventKey: "run-created", EventType: "queued",
		Stage: "queued", Status: "queued", Detail: "已排队",
	}})
	serviceLock := ResourceLock{ClusterID: 2, Namespace: "ns-prod", Kind: "service", Name: "app-entry"}
	store.mu.Lock()
	store.resourceLocks[serviceLock.Identity()] = log.ID
	store.mu.Unlock()

	paused, err := cd.ActOnDeliveryRun(context.Background(), 91, DeliveryActionPause, 3, "pause-control-001", "operator")
	if err != nil {
		t.Fatalf("pause at operator barrier: %v", err)
	}
	if paused.Run.Status != "paused" || paused.Run.CurrentStage != step || paused.Run.Version != 4 || paused.Deploy.Status != "running" {
		t.Fatalf("pause changed the wrong state: run=%+v deploy=%+v", paused.Run, paused.Deploy)
	}
	if err := store.CheckManagedWrite(2, "services", "ns-prod", "app-entry"); !errors.Is(err, ErrConflict) {
		t.Fatalf("pause released the production Service lock: %v", err)
	}
	duplicate, err := cd.ActOnDeliveryRun(context.Background(), 91, DeliveryActionPause, 3, "pause-control-001", "operator")
	if err != nil || duplicate.Run.Version != 4 {
		t.Fatalf("idempotent pause run=%+v error=%v", duplicate.Run, err)
	}

	continued, err := cd.ActOnDeliveryRun(context.Background(), 91, DeliveryActionContinue, 4, "continue-control-001", "operator")
	if err != nil {
		t.Fatalf("continue from pause: %v", err)
	}
	if continued.Run.Status != "running" || continued.Run.CurrentStage != step || continued.Run.Version != 5 {
		t.Fatalf("continue changed the wrong state: %+v", continued.Run)
	}
	if err := store.CheckManagedWrite(2, "services", "ns-prod", "app-entry"); !errors.Is(err, ErrConflict) {
		t.Fatalf("continue released the production Service lock: %v", err)
	}

	if _, err := cd.ActOnDeliveryRun(context.Background(), 91, DeliveryActionPause, 5, "pause-control-002", "operator"); err != nil {
		t.Fatalf("second pause at operator barrier: %v", err)
	}
}

func newRollingControlFixture(t *testing.T, stage string) (*Store, *Worker, *CDService, DeployLog) {
	t.Helper()
	_, store, _, _, worker, cd := cdFixture(t)
	plan := DeliveryPlan{
		ID: 29, TargetClusterID: 2, Strategy: "rolling", Status: "approved",
		Snapshot: DeliveryPlanSnapshot{Strategy: "rolling", Target: DeliveryPlanTarget{AppName: "app", Namespace: "ns-prod", Deployment: "app-prod"}},
	}
	store.LoadDeliveryPlans([]DeliveryPlan{plan})
	log, err := store.AddLog(DeployLog{
		ClusterID: 2, AppName: "app", DeploymentName: "app-prod", ContainerName: "api", Namespace: "ns-prod",
		DeployType: "image_deploy", Status: "running", CurrentStep: &stage, StepDetail: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 92, DeliveryPlanID: plan.ID, DeployLogID: log.ID, Strategy: "rolling",
		Status: "running", CurrentStage: stage, Version: 3,
	}}, []DeliveryRunEvent{{
		ID: 1, RunID: 92, Version: 1, EventKey: "run-created", EventType: "queued",
		Stage: "queued", Status: "queued", Detail: "已排队",
	}})
	lock := ResourceLock{ClusterID: 2, Namespace: "ns-prod", Kind: "deployment", Name: "app-prod"}
	store.mu.Lock()
	store.resourceLocks[lock.Identity()] = log.ID
	store.mu.Unlock()
	return store, worker, cd, log
}

func TestRollingPauseStopsPlatformAtRolloutCheckpointAndContinueRequeues(t *testing.T) {
	store, worker, cd, log := newRollingControlFixture(t, "rollout_wait")
	paused, err := cd.ActOnDeliveryRun(context.Background(), 92, DeliveryActionPause, 3, "rolling-pause-001", "operator")
	if err != nil {
		t.Fatalf("pause rolling health check: %v", err)
	}
	if paused.Run.Status != "paused" || paused.Run.CurrentStage != "rollout_wait" || paused.Deploy.Status != "running" {
		t.Fatalf("unexpected paused rolling state: run=%+v deploy=%+v", paused.Run, paused.Deploy)
	}
	if err := worker.pipeline.succeed(&log, nil); !errors.Is(err, ErrDeliveryRunStopped) {
		t.Fatalf("worker should stop at the paused checkpoint, got %v", err)
	}
	if err := store.CheckResourceLock(ResourceLock{ClusterID: 2, Namespace: "ns-prod", Kind: "deployment", Name: "app-prod"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("pause must retain the Deployment lock: %v", err)
	}
	worker.Close() // Keep the durable requeue pending so the assertion is deterministic.
	continued, err := cd.ActOnDeliveryRun(context.Background(), 92, DeliveryActionContinue, 4, "rolling-continue-001", "operator")
	if err != nil {
		t.Fatalf("continue rolling health check: %v", err)
	}
	if continued.Run.Status != "running" || continued.Run.CurrentStage != "rollout_wait" || continued.Deploy.Status != "pending" {
		t.Fatalf("continue did not durably requeue the run: run=%+v deploy=%+v", continued.Run, continued.Deploy)
	}
}

func TestRollingCancelAllowedOnlyBeforeImageWrite(t *testing.T) {
	store, worker, cd, log := newRollingControlFixture(t, "started")
	cancelled, err := cd.ActOnDeliveryRun(context.Background(), 92, DeliveryActionCancel, 3, "rolling-cancel-001", "operator")
	if err != nil {
		t.Fatalf("cancel before image write: %v", err)
	}
	if cancelled.Run.Status != "cancelled" || cancelled.Run.CurrentStage != "cancelled" || cancelled.Deploy.Status != "failed" {
		t.Fatalf("unexpected cancelled rolling state: run=%+v deploy=%+v", cancelled.Run, cancelled.Deploy)
	}
	if err := worker.pipeline.setStep(&log, "k8s_set_image"); !errors.Is(err, ErrDeliveryRunStopped) {
		t.Fatalf("cancelled worker must not advance to an image write, got %v", err)
	}
	if err := store.CheckResourceLock(ResourceLock{ClusterID: 2, Namespace: "ns-prod", Kind: "deployment", Name: "app-prod"}); err != nil {
		t.Fatal("terminal pre-write cancellation should release the Deployment lock")
	}

	store2, _, cd2, _ := newRollingControlFixture(t, "k8s_set_image")
	if _, err := cd2.ActOnDeliveryRun(context.Background(), 92, DeliveryActionCancel, 3, "rolling-cancel-002", "operator"); err == nil {
		t.Fatal("cancel after the image-write stage began must be rejected")
	}
	if run, _ := store2.DeliveryRun(92); run.Status != "running" {
		t.Fatalf("rejected cancellation changed run state: %+v", run)
	}
}

type pausedBlueGreenCancelFixture struct {
	store             *Store
	cd                *CDService
	runtime           *blueGreenPreparationRuntime
	plan              DeliveryPlan
	candidate         BlueGreenCandidateResources
	stableSelector    map[string]string
	candidateSelector map[string]string
}

func newPausedBlueGreenCancelFixture(t *testing.T) pausedBlueGreenCancelFixture {
	t.Helper()
	_, store, _, _, worker, cd := cdFixture(t)
	plan, deployment, service := blueGreenResourcePlan(t)
	plan.TargetClusterID = 2
	plan.TargetAppID = 2
	plan.ReleaseID = plan.Snapshot.Artifact.ReleaseID
	plan.Snapshot.Target.ClusterID = 2
	plan.Snapshot.Target.AppID = 2
	plan.Snapshot.Target.AppName = "app"
	plan.Snapshot.Target.Namespace = "ns-prod"
	plan.Snapshot.Target.Deployment = "app-prod"
	plan.Snapshot.Target.ServiceName = "app-entry"
	plan.Snapshot.Target.Replicas = 2
	plan.Snapshot.CandidateRetentionHours = BlueGreenCandidateRetentionHours
	deployment.SetName("app-prod")
	deployment.SetNamespace("ns-prod")
	service.SetName("app-entry")
	service.SetNamespace("ns-prod")
	var err error
	plan.Snapshot.Target.WorkloadHash, err = workloadHash(deployment, "api", false)
	if err != nil {
		t.Fatal(err)
	}
	plan.Snapshot.Target.ServiceConfigHash, err = serviceConfigurationHash(service)
	if err != nil {
		t.Fatal(err)
	}
	stabilizeBlueGreenTestResources(t, plan, deployment, service)
	runtime := newBlueGreenPreparationRuntime(plan, 91, deployment, service)
	stableSelector, err := BlueGreenStableServiceSelector(plan.Snapshot.Target.ServiceSelector, plan.Snapshot.Target.StableLabelKey)
	if err != nil {
		t.Fatal(err)
	}
	candidateSelector, err := BlueGreenCandidateSelector(plan.Snapshot.Target.CandidateLabelKey, plan.Snapshot.Target.CandidateLabelValue)
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedStringMap(runtime.objects["services/app-entry"].Object, candidateSelector, "spec", "selector"); err != nil {
		t.Fatal(err)
	}
	candidate, err := BuildBlueGreenCandidateResources(plan, 91, deployment, service)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Deployment.SetUID(types.UID("candidate-deployment-uid"))
	candidate.Service.SetUID(types.UID("candidate-service-uid"))
	runtime.objects["deployments/"+candidate.Deployment.GetName()] = candidate.Deployment
	runtime.objects["services/"+candidate.Service.GetName()] = candidate.Service

	store.LoadDeliveryPlans([]DeliveryPlan{plan})
	step := "awaiting_live_verification"
	log, err := store.AddLog(DeployLog{
		ClusterID: 2, AppName: "app", DeploymentName: "app-prod", ContainerName: "api", Namespace: "ns-prod",
		ReleaseID: plan.ReleaseID, ImageFull: strPtr(plan.Snapshot.Artifact.Image),
		DeployType: "image_deploy", Status: "running", CurrentStep: &step, StepDetail: "[]",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 91, DeliveryPlanID: plan.ID, DeployLogID: log.ID, Strategy: "blue_green",
		Status: "paused", CurrentStage: step, Version: 5,
	}}, []DeliveryRunEvent{{
		ID: 1, RunID: 91, Version: 1, EventKey: "run-created", EventType: "queued",
		Stage: "queued", Status: "queued", Detail: "已排队",
	}})
	locks, err := BlueGreenPlanLocks(plan)
	if err != nil {
		t.Fatal(err)
	}
	locks = append(locks, ResourceLock{ClusterID: 2, Namespace: "ns-prod", Kind: "deployment", Name: "app-prod"})
	store.mu.Lock()
	for _, lock := range locks {
		store.resourceLocks[lock.Identity()] = log.ID
	}
	store.mu.Unlock()
	worker.pipeline.k8s.inject = runtime
	return pausedBlueGreenCancelFixture{
		store: store, cd: cd, runtime: runtime, plan: plan, candidate: candidate,
		stableSelector: stableSelector, candidateSelector: candidateSelector,
	}
}

func TestReconcileBlueGreenResumesPreparationFromOriginalServiceSelector(t *testing.T) {
	fixture := newPausedBlueGreenCancelFixture(t)
	continued, err := fixture.cd.ActOnDeliveryRun(context.Background(), 91, DeliveryActionContinue, 5, "continue-partial-candidate", "operator")
	if err != nil {
		t.Fatalf("continue before simulating interrupted preparation: %v", err)
	}
	if continued.Run.Status != "running" {
		t.Fatalf("continued run status=%q, want running", continued.Run.Status)
	}
	// Simulate a crash after the stable Deployment's track label was applied,
	// but before preparation moved the production Service from its baseline
	// selector to the stable track.
	if err := unstructured.SetNestedStringMap(fixture.runtime.objects["services/app-entry"].Object, fixture.plan.Snapshot.Target.ServiceSelector, "spec", "selector"); err != nil {
		t.Fatalf("restore snapshotted Service selector before reconcile: %v", err)
	}
	delete(fixture.runtime.objects, "services/"+fixture.candidate.Service.GetName())
	delete(fixture.runtime.objects, "deployments/"+fixture.candidate.Deployment.GetName())
	if err := journalCandidateCreate(t, fixture.store, 1, fixture.plan, 91, fixture.candidate.Deployment, blueGreenCandidateDeploy, "Deployment"); err != nil {
		t.Fatalf("persist candidate Deployment create intent: %v", err)
	}
	deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.runtime.CreateResource(context.Background(), deploymentDef, "ns-prod", fixture.candidate.Deployment.DeepCopy(), metav1.CreateOptions{}); err != nil {
		t.Fatalf("simulate successful Deployment creation before process stop: %v", err)
	}

	reconciled, err := fixture.cd.ActOnDeliveryRun(context.Background(), 91, DeliveryActionReconcile, continued.Run.Version, "reconcile-partial-candidate", "operator")
	if err != nil {
		t.Fatalf("reconcile should resume the journaled candidate preparation: %v", err)
	}
	if reconciled.Run.CurrentStage != "awaiting_candidate_review" || reconciled.Run.Status != "running" {
		t.Fatalf("reconciled run=%+v, want running at candidate review", reconciled.Run)
	}
	var deploySteps []StepResult
	if err := json.Unmarshal([]byte(reconciled.Deploy.StepDetail), &deploySteps); err != nil {
		t.Fatalf("decode candidate deploy steps: %v", err)
	}
	var candidateImageVerified bool
	for _, step := range deploySteps {
		if step.Step == "verify_candidate_pod_image" && step.ObservationStatus == "verified" {
			candidateImageVerified = true
		}
	}
	if !candidateImageVerified {
		t.Fatalf("reconcile did not persist verified candidate Pod image evidence: %+v", deploySteps)
	}
	if fixture.runtime.createCount["deployments"] != 1 || fixture.runtime.createCount["services"] != 1 {
		t.Fatalf("reconcile duplicated or skipped candidate writes: creates=%v", fixture.runtime.createCount)
	}
	if fixture.runtime.objects["services/"+fixture.candidate.Service.GetName()] == nil {
		t.Fatal("reconcile did not create the missing private preview Service")
	}
	selector, _, err := unstructured.NestedStringMap(fixture.runtime.objects["services/app-entry"].Object, "spec", "selector")
	if err != nil || !sameStringMap(selector, fixture.stableSelector) {
		t.Fatalf("reconcile left production Service selector=%v, want stable selector %v (error=%v)", selector, fixture.stableSelector, err)
	}
	if fixture.runtime.patchCountByResource["services"] != 1 {
		t.Fatalf("reconcile stable Service selector patch count=%d, want exactly one", fixture.runtime.patchCountByResource["services"])
	}
	actions, err := fixture.store.ExecutionActions(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := executionActionStatus(actions, blueGreenCandidateDeploy); got != ExecutionActionApplied {
		t.Fatalf("candidate Deployment action status=%q, want applied", got)
	}
	if got := executionActionStatus(actions, blueGreenCandidateService); got != ExecutionActionApplied {
		t.Fatalf("candidate Service action status=%q, want applied", got)
	}
	if got := executionActionStatus(actions, blueGreenStableEntryAction); got != ExecutionActionApplied {
		t.Fatalf("stable Service entry action status=%q, want applied", got)
	}
}

func TestBlueGreenRecoveryRequiresCompatibilityAcknowledgementBeforeWriting(t *testing.T) {
	fixture := newPausedBlueGreenCancelFixture(t)
	_, err := fixture.cd.ActOnDeliveryRun(context.Background(), 91, DeliveryActionRestoreStable, 5, "restore-without-compat-001", "operator")
	if err == nil || !strings.Contains(err.Error(), "不回退数据库迁移") {
		t.Fatalf("stable restore without compatibility acknowledgment error=%v, want actionable validation error", err)
	}
	unchanged, err := fixture.store.DeliveryRun(91)
	if err != nil || unchanged.Version != 5 || unchanged.CurrentStage != "awaiting_live_verification" || fixture.runtime.getCount != 0 || fixture.runtime.patchCount != 0 {
		t.Fatalf("rejected restore accessed or changed Kubernetes/run state: run=%+v getCount=%d patchCount=%d err=%v", unchanged, fixture.runtime.getCount, fixture.runtime.patchCount, err)
	}

	acknowledged, err := fixture.cd.ActOnDeliveryRunWithOptions(context.Background(), 91, DeliveryActionRestoreStable, 5, "restore-with-compat-001", "operator", DeliveryRunControlOptions{RecoveryCompatibilityAcknowledged: true})
	if err != nil {
		t.Fatalf("stable restore with compatibility acknowledgment: %v", err)
	}
	if acknowledged.Run.CurrentStage != "restored" || fixture.runtime.patchCount != 1 {
		t.Fatalf("acknowledged restore did not complete Service switch: run=%+v patchCount=%d", acknowledged.Run, fixture.runtime.patchCount)
	}
	if len(acknowledged.Events) != 2 || !strings.Contains(acknowledged.Events[1].Detail, "recovery_compatibility=operator_confirmed") {
		t.Fatalf("recovery compatibility acknowledgment missing from audit event: %+v", acknowledged.Events)
	}
}

func TestCancelPausedBlueGreenRunRestoresStableAndRetainsCandidate(t *testing.T) {
	fixture := newPausedBlueGreenCancelFixture(t)
	// The API server applied the selector update but the client lost its response.
	// A successful readback must resolve the ambiguity without blocking recovery.
	fixture.runtime.patchErrors = map[string]error{"services": errors.New("connection reset after Service selector update")}
	fixture.runtime.applyPatchOnError = map[string]bool{"services": true}
	result, err := fixture.cd.ActOnDeliveryRunWithOptions(context.Background(), 91, DeliveryActionCancel, 5, "cancel-control-001", "operator", DeliveryRunControlOptions{RecoveryCompatibilityAcknowledged: true})
	if err != nil {
		t.Fatalf("cancel and restore stable: %v", err)
	}
	if result.Run.Status != "cancelled" || result.Run.CurrentStage != "restored" || result.Run.Version != 6 {
		t.Fatalf("cancel state=%+v", result.Run)
	}
	if result.Deploy.Status != "failed" || result.Deploy.ErrorMessage == nil {
		t.Fatalf("legacy deploy log did not record a terminal cancelled task: %+v", result.Deploy)
	}
	selector, _, _ := unstructured.NestedStringMap(fixture.runtime.objects["services/app-entry"].Object, "spec", "selector")
	if !sameStringMap(selector, fixture.stableSelector) || fixture.runtime.patchCount != 1 {
		t.Fatalf("production Service selector=%v patchCount=%d, want stable %v and one conditional restore", selector, fixture.runtime.patchCount, fixture.stableSelector)
	}
	actions, err := fixture.store.ExecutionActions(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := executionActionStatus(actions, blueGreenRestoreAction); got != ExecutionActionApplied {
		t.Fatalf("lost restore response was not resolved in the action journal: status=%q", got)
	}
	if fixture.runtime.objects["deployments/"+fixture.candidate.Deployment.GetName()] == nil || fixture.runtime.objects["services/"+fixture.candidate.Service.GetName()] == nil {
		t.Fatal("cancellation must retain candidate resources for investigation and timed cleanup")
	}
	if err := fixture.store.CheckManagedWrite(2, "services", "ns-prod", "app-entry"); err != nil {
		t.Fatalf("terminal cancellation did not release the production Service lock: %v", err)
	}
	if len(result.Events) != 2 || result.Events[1].Status != "cancelled" {
		t.Fatalf("cancel event missing: %+v", result.Events)
	}
	if _, err := fixture.cd.ActOnDeliveryRun(context.Background(), 91, DeliveryActionContinue, 6, "continue-after-cancel", "operator"); err == nil {
		t.Fatal("cancelled run must not resume")
	}
}

func TestCancelPausedBlueGreenRunRetainsLocksWhenRestoreDoesNotApplyAndCanRecover(t *testing.T) {
	fixture := newPausedBlueGreenCancelFixture(t)
	fixture.runtime.patchErrors = map[string]error{"services": errors.New("Service API unavailable")}
	result, err := fixture.cd.ActOnDeliveryRunWithOptions(context.Background(), 91, DeliveryActionCancel, 5, "cancel-control-fail-001", "operator", DeliveryRunControlOptions{RecoveryCompatibilityAcknowledged: true})
	if err == nil {
		t.Fatal("cancel should report that stable selector restoration did not apply")
	}
	if result.Run.Status != "running" || result.Run.CurrentStage != "needs_attention" || result.Run.Version != 6 || result.Deploy.Status != "running" {
		t.Fatalf("ambiguous cancel state=%+v deploy=%+v", result.Run, result.Deploy)
	}
	if len(result.Events) < 2 || !strings.Contains(result.Events[len(result.Events)-1].Detail, "recovery_compatibility=operator_confirmed") {
		t.Fatalf("failed recovery attempt did not retain its compatibility acknowledgment: %+v", result.Events)
	}
	selector, _, _ := unstructured.NestedStringMap(fixture.runtime.objects["services/app-entry"].Object, "spec", "selector")
	if !sameStringMap(selector, fixture.candidateSelector) {
		t.Fatalf("failed restore changed production selector to %v; want candidate %v", selector, fixture.candidateSelector)
	}
	actions, actionErr := fixture.store.ExecutionActions(1)
	if actionErr != nil {
		t.Fatal(actionErr)
	}
	if got := executionActionStatus(actions, blueGreenRestoreAction); got != ExecutionActionNotApplied {
		t.Fatalf("rejected restore response journal status=%q, want not_applied", got)
	}
	if lockErr := fixture.store.CheckManagedWrite(2, "services", "ns-prod", "app-entry"); !errors.Is(lockErr, ErrConflict) {
		t.Fatalf("needs_attention must retain the production Service lock: %v", lockErr)
	}
	if fixture.runtime.objects["deployments/"+fixture.candidate.Deployment.GetName()] == nil {
		t.Fatal("candidate Deployment should remain available while the run needs attention")
	}

	fixture.runtime.patchErrors = nil
	recovered, err := fixture.cd.ActOnDeliveryRunWithOptions(context.Background(), 91, DeliveryActionRestoreStable, 6, "restore-control-recovery-001", "operator", DeliveryRunControlOptions{RecoveryCompatibilityAcknowledged: true})
	if err != nil {
		t.Fatalf("retry stable restoration after confirming the failed write: %v", err)
	}
	if recovered.Run.Status != "failed" || recovered.Run.CurrentStage != "restored" || recovered.Run.Version != 7 || recovered.Deploy.Status != "failed" {
		t.Fatalf("recovered restore state=%+v deploy=%+v", recovered.Run, recovered.Deploy)
	}
	selector, _, _ = unstructured.NestedStringMap(fixture.runtime.objects["services/app-entry"].Object, "spec", "selector")
	if !sameStringMap(selector, fixture.stableSelector) {
		t.Fatalf("recovery left production Service selector at %v; want stable %v", selector, fixture.stableSelector)
	}
	actions, err = fixture.store.ExecutionActions(1)
	if err != nil {
		t.Fatal(err)
	}
	if got := executionActionStatus(actions, blueGreenRestoreAction); got != ExecutionActionApplied {
		t.Fatalf("recovered restore action journal status=%q, want applied", got)
	}
	if lockErr := fixture.store.CheckManagedWrite(2, "services", "ns-prod", "app-entry"); lockErr != nil {
		t.Fatalf("confirmed stable recovery should release the production Service lock: %v", lockErr)
	}
}

func TestRestoreStableConfirmsPersistedIntentWhenSelectorAlreadyRestored(t *testing.T) {
	fixture := newPausedBlueGreenCancelFixture(t)
	if err := unstructured.SetNestedStringMap(fixture.runtime.objects["services/app-entry"].Object, fixture.stableSelector, "spec", "selector"); err != nil {
		t.Fatal(err)
	}
	intent := blueGreenActionIntent{
		PlanID: fixture.plan.ID, ExecutionID: 91, Namespace: "ns-prod", Kind: "Service",
		Name: "app-entry", UID: fixture.plan.Snapshot.Target.ServiceUID,
		Selector: fixture.stableSelector, PreviousSelector: fixture.candidateSelector,
		ConfigHash: fixture.plan.Snapshot.Target.ServiceConfigHash,
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	run, err := fixture.store.DeliveryRun(91)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.BeginExecutionAction(ExecutionAction{
		DeployLogID: run.DeployLogID, ActionKey: blueGreenRestoreAction,
		ActionKind: "set_service_track_selector", Status: ExecutionActionIntent, Intent: encoded,
	}); err != nil {
		t.Fatalf("persist interrupted stable selector intent: %v", err)
	}

	result, err := fixture.cd.ActOnDeliveryRunWithOptions(context.Background(), 91, DeliveryActionRestoreStable, 5, "restore-after-selector-write-001", "operator", DeliveryRunControlOptions{RecoveryCompatibilityAcknowledged: true})
	if err != nil {
		t.Fatalf("restore run after reading already-stable selector: %v", err)
	}
	if result.Run.Status != "failed" || result.Run.CurrentStage != "restored" {
		t.Fatalf("restored run state=%+v, want failed/restored", result.Run)
	}
	if got := executionActionStatus(result.Actions, blueGreenRestoreAction); got != ExecutionActionApplied {
		t.Fatalf("restore action status=%q, want applied after selector readback", got)
	}
	if fixture.runtime.patchCount != 0 {
		t.Fatalf("restore patched an already-stable Service %d times, want no write", fixture.runtime.patchCount)
	}
}

func TestBlueGreenConfirmLiveRejectsConfigurationDriftAndKeepsLock(t *testing.T) {
	fixture := newPausedBlueGreenCancelFixture(t)
	deployment := fixture.runtime.objects["deployments/app-prod"]
	containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("read stable Deployment containers: found=%v err=%v", found, err)
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected stable Deployment container: %#v", containers[0])
	}
	container["envFrom"] = []any{map[string]any{"configMapRef": map[string]any{"name": "runtime-config"}}}
	if err := unstructured.SetNestedSlice(deployment.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		t.Fatal(err)
	}
	fixture.runtime.objects["configmaps/runtime-config"] = &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": "runtime-config", "namespace": "ns-prod", "uid": "config-uid", "resourceVersion": "2"},
	}}

	plan := fixture.plan
	plan.Snapshot.Target.ConfigurationReferences = []DeliveryConfigurationReference{{
		Kind: "ConfigMap", Name: "runtime-config", UID: "config-uid", ResourceVersion: "1",
	}}
	fixture.store.LoadDeliveryPlans([]DeliveryPlan{plan})
	run, err := fixture.store.DeliveryRun(91)
	if err != nil {
		t.Fatal(err)
	}
	run.Status = "running"
	run.Version++
	events, err := fixture.store.DeliveryRunEvents(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store.LoadDeliveryRuns([]DeliveryRun{run}, events)

	view, err := fixture.cd.ActOnDeliveryRun(context.Background(), run.ID, DeliveryActionConfirmLive, run.Version, "confirm-live-config-drift", "operator")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("confirm live after ConfigMap change error=%v, want conflict", err)
	}
	if view.Run.CurrentStage != "needs_attention" || view.Run.Status != "running" {
		t.Fatalf("drifted confirmation run=%+v, want needs_attention with active lock", view.Run)
	}
	if fixture.runtime.patchCount != 0 {
		t.Fatalf("configuration drift caused %d Kubernetes patches", fixture.runtime.patchCount)
	}
	lock := ResourceLock{ClusterID: 2, Namespace: "ns-prod", Kind: "deployment", Name: "app-prod"}
	if owner := fixture.store.resourceLocks[lock.Identity()]; owner != view.Deploy.ID {
		t.Fatalf("drift recovery released workload lock: owner=%d want deploy log %d", owner, view.Deploy.ID)
	}
}
