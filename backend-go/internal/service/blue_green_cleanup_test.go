package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type blueGreenCleanupRuntime struct {
	*recordingK8s
	objects                    map[string]*unstructured.Unstructured
	lists                      map[string][]unstructured.Unstructured
	deleted                    []string
	deleteErrors               map[string]error
	deleteBeforeReturningError map[string]bool
}

func (r *blueGreenCleanupRuntime) GetResource(_ context.Context, def kube.ResourceDef, namespace, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	object := r.objects[def.Resource+"/"+name]
	if object == nil || object.GetNamespace() != namespace {
		return nil, apierrors.NewNotFound(schema.GroupResource{Group: def.Group, Resource: def.Resource}, name)
	}
	return object.DeepCopy(), nil
}

func (r *blueGreenCleanupRuntime) ListResources(_ context.Context, def kube.ResourceDef, namespace string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	selector, err := labels.Parse(opts.LabelSelector)
	if err != nil {
		return nil, err
	}
	result := &unstructured.UnstructuredList{}
	for _, object := range r.lists[def.Resource] {
		if object.GetNamespace() == namespace && selector.Matches(labels.Set(object.GetLabels())) {
			result.Items = append(result.Items, *object.DeepCopy())
		}
	}
	return result, nil
}

func (r *blueGreenCleanupRuntime) DeleteResource(_ context.Context, def kube.ResourceDef, namespace, name string, opts metav1.DeleteOptions) error {
	key := def.Resource + "/" + name
	object := r.objects[key]
	if object == nil || object.GetNamespace() != namespace {
		return apierrors.NewNotFound(schema.GroupResource{Group: def.Group, Resource: def.Resource}, name)
	}
	if opts.Preconditions == nil || opts.Preconditions.UID == nil || object.GetUID() != *opts.Preconditions.UID {
		return apierrors.NewConflict(schema.GroupResource{Group: def.Group, Resource: def.Resource}, name, fmt.Errorf("UID precondition mismatch"))
	}
	if deleteErr := r.deleteErrors[key]; deleteErr != nil && !r.deleteBeforeReturningError[key] {
		return deleteErr
	}
	delete(r.objects, key)
	r.deleted = append(r.deleted, key)
	return r.deleteErrors[key]
}

func newBlueGreenCleanupFixture(t *testing.T, appDeployment, serviceSelectorOverride string) (*Store, *blueGreenCleanupRuntime, DeliveryRun, DeliveryPlan, DeployLog, *unstructured.Unstructured) {
	t.Helper()
	plan, stableDeployment, stableService := blueGreenResourcePlan(t)
	plan.ID = 19
	plan.TargetClusterID = 1
	plan.TargetAppID = 7
	plan.Snapshot.Target.ClusterID = 1
	plan.Snapshot.Target.AppID = 7
	plan.Snapshot.Target.AppName = "orders"
	plan.Snapshot.CandidateRetentionHours = BlueGreenCandidateRetentionHours
	stabilizeBlueGreenTestResources(t, plan, stableDeployment, stableService)
	resources, err := BuildBlueGreenCandidateResources(plan, 77, stableDeployment, stableService)
	if err != nil {
		t.Fatalf("BuildBlueGreenCandidateResources: %v", err)
	}
	candidateDeployment := resources.Deployment.DeepCopy()
	candidateDeployment.SetUID(types.UID("candidate-deployment-uid"))
	candidateService := resources.Service.DeepCopy()
	candidateService.SetUID(types.UID("candidate-service-uid"))

	stablePod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "orders-stable-0", "namespace": "prod", "labels": toAnyStringMap(map[string]string{"app": "orders", "tier": "api", plan.Snapshot.Target.StableLabelKey: "stable"})},
	}}
	stableSlice := blueGreenEndpointSlice("stable-entry-slice", "prod", "orders-entry", stablePod.GetName())
	runtime := &blueGreenCleanupRuntime{
		recordingK8s: &recordingK8s{},
		objects: map[string]*unstructured.Unstructured{
			"deployments/orders-api":                       stableDeployment.DeepCopy(),
			"services/orders-entry":                        stableService.DeepCopy(),
			"deployments/" + candidateDeployment.GetName(): candidateDeployment,
			"services/" + candidateService.GetName():       candidateService,
		},
		lists: map[string][]unstructured.Unstructured{
			"pods":           {*stablePod},
			"endpointslices": {*stableSlice},
		},
	}
	if serviceSelectorOverride != "" {
		_ = unstructured.SetNestedStringMap(runtime.objects["services/orders-entry"].Object, map[string]string{"unexpected": serviceSelectorOverride}, "spec", "selector")
	}
	store := NewStoreWithData(nil, []App{{ID: 7, ClusterID: 1, AppName: "orders", DeploymentName: appDeployment, Namespace: "prod"}}, nil, nil, nil)
	for _, created := range []struct {
		object *unstructured.Unstructured
		key    string
		kind   string
	}{{candidateDeployment, blueGreenCandidateDeploy, "Deployment"}, {candidateService, blueGreenCandidateService, "Service"}} {
		if err := journalCandidateCreate(t, store, 61, plan, 77, created.object, created.key, created.kind); err != nil {
			t.Fatalf("journal create %s: %v", created.kind, err)
		}
		if err := completeBlueGreenAction(store, 61, created.key, ExecutionActionApplied, blueGreenObservationForObject(created.object, created.kind, "api"), "candidate_create_confirmed"); err != nil {
			t.Fatalf("complete create %s: %v", created.kind, err)
		}
	}
	finished := time.Now().Add(-25 * time.Hour)
	step := "restored"
	log := DeployLog{ID: 61, ClusterID: 1, Namespace: "prod", DeploymentName: "orders-api", ContainerName: "api", Status: "failed", CurrentStep: &step, FinishedAt: &finished}
	run := DeliveryRun{ID: 77, DeliveryPlanID: plan.ID, DeployLogID: log.ID, Strategy: "blue_green", Status: "failed", CurrentStage: "restored", Version: 5}
	return store, runtime, run, plan, log, stableDeployment
}

func TestCleanupRestoredBlueGreenCandidateDeletesOnlyOwnedCandidateResources(t *testing.T) {
	store, runtime, run, plan, log, stableDeployment := newBlueGreenCleanupFixture(t, "orders-api", "")
	if err := cleanupRestoredBlueGreenCandidate(context.Background(), store, runtime, run, plan, log); err != nil {
		t.Fatalf("cleanupRestoredBlueGreenCandidate: %v", err)
	}
	if _, exists := runtime.objects["deployments/orders-api-bg-19"]; exists {
		t.Fatal("candidate Deployment still exists")
	}
	if _, exists := runtime.objects["services/orders-entry-bg-19"]; exists {
		t.Fatal("candidate preview Service still exists")
	}
	if got := runtime.objects["deployments/orders-api"].GetUID(); got != stableDeployment.GetUID() {
		t.Fatalf("stable Deployment UID=%s, want preserved UID %s", got, stableDeployment.GetUID())
	}
	if len(runtime.deleted) != 2 || runtime.deleted[0] != "services/orders-entry-bg-19" || runtime.deleted[1] != "deployments/orders-api-bg-19" {
		t.Fatalf("deletion order=%v", runtime.deleted)
	}
	if len(store.resourceLocks) != 0 {
		t.Fatalf("cleanup lease not released: %+v", store.resourceLocks)
	}
}

func TestCleanupRestoredBlueGreenCandidateRequiresRetentionBindingAndStableEntry(t *testing.T) {
	t.Run("retention", func(t *testing.T) {
		store, runtime, run, plan, log, _ := newBlueGreenCleanupFixture(t, "orders-api", "")
		finished := time.Now().Add(-time.Hour)
		log.FinishedAt = &finished
		if err := cleanupRestoredBlueGreenCandidate(context.Background(), store, runtime, run, plan, log); !errors.Is(err, ErrValidation) {
			t.Fatalf("retention error=%v, want validation", err)
		}
		if len(runtime.deleted) != 0 {
			t.Fatalf("deleted resources before retention elapsed: %v", runtime.deleted)
		}
	})
	t.Run("binding", func(t *testing.T) {
		store, runtime, run, plan, log, _ := newBlueGreenCleanupFixture(t, "foreign-deployment", "")
		if err := cleanupRestoredBlueGreenCandidate(context.Background(), store, runtime, run, plan, log); !errors.Is(err, ErrConflict) {
			t.Fatalf("binding error=%v, want conflict", err)
		}
		if len(runtime.deleted) != 0 {
			t.Fatalf("deleted resources with a changed app binding: %v", runtime.deleted)
		}
	})
	t.Run("entry selector", func(t *testing.T) {
		store, runtime, run, plan, log, _ := newBlueGreenCleanupFixture(t, "orders-api", "drift")
		if err := cleanupRestoredBlueGreenCandidate(context.Background(), store, runtime, run, plan, log); !errors.Is(err, ErrConflict) {
			t.Fatalf("selector error=%v, want conflict", err)
		}
		if len(runtime.deleted) != 0 {
			t.Fatalf("deleted resources after selector drift: %v", runtime.deleted)
		}
	})
	t.Run("replacement UID", func(t *testing.T) {
		store, runtime, run, plan, log, _ := newBlueGreenCleanupFixture(t, "orders-api", "")
		replacement := runtime.objects["services/orders-entry-bg-19"].DeepCopy()
		replacement.SetUID(types.UID("replacement-preview-service-uid"))
		runtime.objects["services/orders-entry-bg-19"] = replacement
		if err := cleanupRestoredBlueGreenCandidate(context.Background(), store, runtime, run, plan, log); !errors.Is(err, ErrConflict) {
			t.Fatalf("replacement UID error=%v, want conflict", err)
		}
		if len(runtime.deleted) != 0 {
			t.Fatalf("deleted candidate with a replacement UID: %v", runtime.deleted)
		}
	})
}

func TestCleanupBlueGreenCandidateDeleteFailureKeepsLeaseAndRetriesSafely(t *testing.T) {
	store, runtime, run, plan, log, stableDeployment := newBlueGreenCleanupFixture(t, "orders-api", "")
	deleteError := errors.New("Kubernetes rejected delete")
	serviceKey := "services/orders-entry-bg-19"
	deploymentKey := "deployments/orders-api-bg-19"
	runtime.deleteErrors = map[string]error{serviceKey: deleteError}

	if err := cleanupRestoredBlueGreenCandidate(context.Background(), store, runtime, run, plan, log); !errors.Is(err, deleteError) {
		t.Fatalf("cleanup error=%v, want injected Service delete error", err)
	}
	if runtime.objects[serviceKey] == nil || runtime.objects[deploymentKey] == nil {
		t.Fatal("a rejected Service delete must leave both candidate resources intact")
	}
	actions, err := store.ExecutionActions(log.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := executionActionStatus(actions, blueGreenCleanupServiceAction); got != ExecutionActionNotApplied {
		t.Fatalf("failed Service delete journal status=%q, want not_applied", got)
	}
	if len(store.resourceLockLeases) == 0 {
		t.Fatal("failed external deletion must retain the bounded cleanup lease")
	}
	serviceLock := ResourceLock{ClusterID: 1, Namespace: "prod", Kind: "service", Name: "orders-entry"}
	store.mu.RLock()
	lockOwner := store.resourceLocks[serviceLock.Identity()]
	leaseExpiry := store.resourceLockLeases[serviceLock.Identity()]
	store.mu.RUnlock()
	if lockOwner != log.ID || !leaseExpiry.After(time.Now()) {
		t.Fatalf("uncertain cleanup did not retain a live Service lock lease: owner=%d expiry=%v", lockOwner, leaseExpiry)
	}

	// Model expiry without waiting 15 minutes. The retry gets a fresh lease and
	// continues from the persisted action journal.
	expireBlueGreenCleanupLease(t, store)
	runtime.deleteErrors = nil
	if err := cleanupRestoredBlueGreenCandidate(context.Background(), store, runtime, run, plan, log); err != nil {
		t.Fatalf("cleanup retry after lease expiry: %v", err)
	}
	if runtime.objects[serviceKey] != nil || runtime.objects[deploymentKey] != nil {
		t.Fatal("cleanup retry left a candidate resource")
	}
	if stable := runtime.objects["deployments/orders-api"]; stable == nil || stable.GetUID() != stableDeployment.GetUID() {
		t.Fatal("cleanup retry changed or removed the stable Deployment")
	}
	if len(store.resourceLocks) != 0 || len(store.resourceLockLeases) != 0 {
		t.Fatalf("successful retry did not release cleanup lease: locks=%v leases=%v", store.resourceLocks, store.resourceLockLeases)
	}
}

func TestCleanupBlueGreenCandidateResolvesLostDeleteResponseAndResumesPartialCleanup(t *testing.T) {
	store, runtime, run, plan, log, stableDeployment := newBlueGreenCleanupFixture(t, "orders-api", "")
	serviceKey := "services/orders-entry-bg-19"
	deploymentKey := "deployments/orders-api-bg-19"
	deleteError := errors.New("connection reset after delete")
	runtime.deleteErrors = map[string]error{
		serviceKey:    deleteError,
		deploymentKey: errors.New("Deployment delete denied"),
	}
	runtime.deleteBeforeReturningError = map[string]bool{serviceKey: true}

	if err := cleanupRestoredBlueGreenCandidate(context.Background(), store, runtime, run, plan, log); err == nil {
		t.Fatal("partial cleanup should report the candidate Deployment delete failure")
	}
	if runtime.objects[serviceKey] != nil || runtime.objects[deploymentKey] == nil {
		t.Fatalf("unexpected partial cleanup state: service=%v deployment=%v", runtime.objects[serviceKey], runtime.objects[deploymentKey])
	}
	actions, err := store.ExecutionActions(log.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := executionActionStatus(actions, blueGreenCleanupServiceAction); got != ExecutionActionApplied {
		t.Fatalf("lost Service delete response was not resolved by NotFound readback: status=%q", got)
	}
	if got := executionActionStatus(actions, blueGreenCleanupDeployAction); got != ExecutionActionNotApplied {
		t.Fatalf("failed candidate Deployment delete journal status=%q, want not_applied", got)
	}
	if len(store.resourceLockLeases) == 0 {
		t.Fatal("partial cleanup must retain its bounded lease")
	}
	if stable := runtime.objects["deployments/orders-api"]; stable == nil || stable.GetUID() != stableDeployment.GetUID() {
		t.Fatal("partial cleanup changed or removed the stable Deployment")
	}

	expireBlueGreenCleanupLease(t, store)
	runtime.deleteErrors = nil
	if err := cleanupRestoredBlueGreenCandidate(context.Background(), store, runtime, run, plan, log); err != nil {
		t.Fatalf("resume partial cleanup after lease expiry: %v", err)
	}
	if runtime.objects[serviceKey] != nil || runtime.objects[deploymentKey] != nil {
		t.Fatal("resumed cleanup left candidate resources")
	}
	if len(runtime.deleted) != 2 || runtime.deleted[0] != serviceKey || runtime.deleted[1] != deploymentKey {
		t.Fatalf("retry repeated or reordered a confirmed Service deletion: %v", runtime.deleted)
	}
	if stable := runtime.objects["deployments/orders-api"]; stable == nil || stable.GetUID() != stableDeployment.GetUID() {
		t.Fatal("resumed cleanup changed or removed the stable Deployment")
	}
}

func executionActionStatus(actions []ExecutionAction, key string) string {
	for _, action := range actions {
		if action.ActionKey == key {
			return action.Status
		}
	}
	return ""
}

func expireBlueGreenCleanupLease(t *testing.T, store *Store) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	for identity, owner := range store.resourceLocks {
		if owner == 61 {
			store.resourceLockLeases[identity] = time.Now().Add(-time.Second)
		}
	}
}
