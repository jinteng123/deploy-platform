package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

type blueGreenIsolationRuntime struct {
	*recordingK8s
	objects map[string]*unstructured.Unstructured
	lists   map[string]*unstructured.UnstructuredList
}

type blueGreenSelectorRuntime struct {
	*recordingK8s
	service      *unstructured.Unstructured
	nextSelector map[string]string
	patchErr     error
	applyPatch   bool
	patchCount   int
}

type blueGreenPreparationRuntime struct {
	*recordingK8s
	objects              map[string]*unstructured.Unstructured
	listItems            map[string][]unstructured.Unstructured
	createCount          map[string]int
	createErrors         map[string]error
	createAppliedOnError map[string]bool
	getCount             int
	patchCount           int
	patchCountByResource map[string]int
	patchErrors          map[string]error
	applyPatchOnError    map[string]bool
	candidateService     string
	candidateLabelKey    string
	candidateLabelValue  string
	candidateImageID     string
}

func newBlueGreenPreparationRuntime(plan DeliveryPlan, executionID int64, deployment, service *unstructured.Unstructured) *blueGreenPreparationRuntime {
	target := plan.Snapshot.Target
	stablePod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": "orders-stable-0", "namespace": target.Namespace, "labels": map[string]any{
			"app": "orders", "tier": "api", target.StableLabelKey: "stable",
		}},
	}}
	stableSlice := blueGreenEndpointSlice("stable-slice", target.Namespace, target.ServiceName, stablePod.GetName())
	candidateService := blueGreenResourceName(target.ServiceName, plan.ID)
	return &blueGreenPreparationRuntime{
		recordingK8s: &recordingK8s{},
		objects: map[string]*unstructured.Unstructured{
			"deployments/" + target.Deployment: deployment.DeepCopy(),
			"services/" + target.ServiceName:   service.DeepCopy(),
			"serviceaccounts/default": {Object: map[string]any{
				"apiVersion": "v1", "kind": "ServiceAccount",
				"metadata": map[string]any{"name": "default", "namespace": target.Namespace},
			}},
		},
		listItems: map[string][]unstructured.Unstructured{
			"pods":           {*stablePod},
			"endpointslices": {*stableSlice},
		},
		createCount:          map[string]int{},
		patchCountByResource: map[string]int{},
		candidateService:     candidateService,
		candidateLabelKey:    target.CandidateLabelKey,
		candidateLabelValue:  target.CandidateLabelValue,
	}
}

func TestPrepareBlueGreenCandidateRejectsConfigurationDriftBeforeMutation(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	plan.ID = 77
	plan.ReleaseID = plan.Snapshot.Artifact.ReleaseID
	plan.Snapshot.Target.ClusterID = 1
	plan.Snapshot.Target.AppID = 7
	containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		t.Fatalf("read Deployment containers: found=%v err=%v", found, err)
	}
	container, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected container: %#v", containers[0])
	}
	container["envFrom"] = []any{map[string]any{"configMapRef": map[string]any{"name": "orders-runtime"}}}
	if err := unstructured.SetNestedSlice(deployment.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		t.Fatal(err)
	}
	hash, err := workloadHash(deployment.DeepCopy(), plan.Snapshot.Target.Container, false)
	if err != nil {
		t.Fatal(err)
	}
	plan.Snapshot.Target.WorkloadHash = hash
	plan.Snapshot.Target.CandidateLabelValue = candidateLabelValue(plan.ReleaseID, plan.Snapshot.Target.WorkloadUID, hash)
	plan.Snapshot.Target.ConfigurationReferences = []DeliveryConfigurationReference{{
		Kind: "ConfigMap", Name: "orders-runtime", UID: "runtime-config-uid", ResourceVersion: "1",
	}}
	runtime := newBlueGreenPreparationRuntime(plan, 77, deployment, service)
	runtime.configurationMetadata = map[string]metav1.PartialObjectMetadata{
		"configmaps/prod/orders-runtime": {ObjectMeta: metav1.ObjectMeta{
			Namespace: "prod", Name: "orders-runtime", UID: "runtime-config-uid", ResourceVersion: "2",
		}},
	}
	image := plan.Snapshot.Artifact.Image
	log := DeployLog{
		ID: 61, ClusterID: 1, ReleaseID: plan.ReleaseID,
		Namespace: "prod", DeploymentName: "orders-api", ContainerName: "api", ImageFull: &image,
	}
	store := NewStore()
	if _, err := prepareBlueGreenCandidate(context.Background(), store, runtime, log, plan, 77); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed ConfigMap version error=%v, want conflict", err)
	}
	if runtime.patchCount != 0 || runtime.createCount["deployments"] != 0 || runtime.createCount["services"] != 0 {
		t.Fatalf("configuration drift caused Kubernetes mutations: patches=%d creates=%v", runtime.patchCount, runtime.createCount)
	}
}

func (r *blueGreenPreparationRuntime) GetResource(_ context.Context, def kube.ResourceDef, _, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	r.getCount++
	if object := r.objects[def.Resource+"/"+name]; object != nil {
		return object.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: def.Group, Resource: def.Resource}, name)
}

func (r *blueGreenPreparationRuntime) DeleteResource(_ context.Context, def kube.ResourceDef, _, name string, opts metav1.DeleteOptions) error {
	key := def.Resource + "/" + name
	object := r.objects[key]
	if object == nil {
		return apierrors.NewNotFound(schema.GroupResource{Group: def.Group, Resource: def.Resource}, name)
	}
	if opts.Preconditions == nil || opts.Preconditions.UID == nil || object.GetUID() != *opts.Preconditions.UID {
		return apierrors.NewConflict(schema.GroupResource{Group: def.Group, Resource: def.Resource}, name, errors.New("UID precondition mismatch"))
	}
	delete(r.objects, key)
	return nil
}

func (r *blueGreenPreparationRuntime) ListResources(_ context.Context, def kube.ResourceDef, namespace string, opts metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	selector, err := labels.Parse(opts.LabelSelector)
	if err != nil {
		return nil, err
	}
	result := &unstructured.UnstructuredList{}
	for _, object := range r.listItems[def.Resource] {
		if object.GetNamespace() != "" && object.GetNamespace() != namespace {
			continue
		}
		if selector.Matches(labels.Set(object.GetLabels())) {
			result.Items = append(result.Items, *object.DeepCopy())
		}
	}
	if def.Resource == "deployments" || def.Resource == "services" {
		for _, object := range r.objects {
			if object.GetNamespace() != namespace {
				continue
			}
			if (def.Resource == "deployments" && object.GetKind() == "Deployment") || (def.Resource == "services" && object.GetKind() == "Service") {
				result.Items = append(result.Items, *object.DeepCopy())
			}
		}
	}
	return result, nil
}

func (r *blueGreenPreparationRuntime) RolloutStatus(_ context.Context, _, name string) (string, error) {
	object := r.objects["deployments/"+name]
	if object == nil {
		return "", apierrors.NewNotFound(schema.GroupResource{Resource: "deployments"}, name)
	}
	replicas := deploymentReplicas(object)
	if object.GetName() != "orders-api" {
		generation := object.GetGeneration()
		_ = unstructured.SetNestedMap(object.Object, map[string]any{
			"observedGeneration": generation, "readyReplicas": replicas, "updatedReplicas": replicas, "availableReplicas": replicas,
		}, "status")
	}
	return fmt.Sprintf("deployment %q successfully rolled out", name), nil
}

func (r *blueGreenPreparationRuntime) PatchResource(_ context.Context, def kube.ResourceDef, _, name string, patchType types.PatchType, data []byte, _ metav1.PatchOptions) (*unstructured.Unstructured, error) {
	if patchType != types.JSONPatchType {
		return nil, errors.New("unexpected patch type")
	}
	patchErr := r.patchErrors[def.Resource]
	if patchErr != nil && !r.applyPatchOnError[def.Resource] {
		return nil, patchErr
	}
	object := r.objects[def.Resource+"/"+name]
	if object == nil {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: def.Resource}, name)
	}
	var operations []map[string]any
	if err := json.Unmarshal(data, &operations); err != nil || len(operations) < 2 {
		return nil, errors.New("invalid JSON patch")
	}
	if def.Resource == "deployments" {
		labelsMap, _, _ := unstructured.NestedStringMap(object.Object, "spec", "template", "metadata", "labels")
		for _, operation := range operations {
			path, _ := operation["path"].(string)
			if operation["op"] == "add" && strings.HasPrefix(path, "/spec/template/metadata/labels/") {
				labelKey := strings.TrimPrefix(path, "/spec/template/metadata/labels/")
				labelKey = strings.ReplaceAll(strings.ReplaceAll(labelKey, "~1", "/"), "~0", "~")
				labelsMap[labelKey], _ = operation["value"].(string)
			}
		}
		if err := unstructured.SetNestedStringMap(object.Object, labelsMap, "spec", "template", "metadata", "labels"); err != nil {
			return nil, err
		}
	} else if def.Resource == "services" {
		selectorMap, ok := operations[len(operations)-1]["value"].(map[string]any)
		if !ok {
			return nil, errors.New("selector patch value missing")
		}
		selector := map[string]string{}
		for key, raw := range selectorMap {
			value, ok := raw.(string)
			if !ok {
				return nil, errors.New("selector value is not string")
			}
			selector[key] = value
		}
		if err := unstructured.SetNestedStringMap(object.Object, selector, "spec", "selector"); err != nil {
			return nil, err
		}
	}
	r.patchCount++
	r.patchCountByResource[def.Resource]++
	object.SetResourceVersion(fmt.Sprint(r.patchCount + 90))
	return object.DeepCopy(), patchErr
}

func (r *blueGreenPreparationRuntime) CreateResource(_ context.Context, def kube.ResourceDef, namespace string, object *unstructured.Unstructured, _ metav1.CreateOptions) (*unstructured.Unstructured, error) {
	key := def.Resource + "/" + object.GetName()
	createErr := r.createErrors[def.Resource]
	if createErr != nil && !r.createAppliedOnError[def.Resource] {
		return nil, createErr
	}
	if _, exists := r.objects[key]; exists {
		return nil, apierrors.NewAlreadyExists(schema.GroupResource{Group: def.Group, Resource: def.Resource}, object.GetName())
	}
	created := object.DeepCopy()
	created.SetNamespace(namespace)
	r.createCount[def.Resource]++
	created.SetUID(types.UID(fmt.Sprintf("created-%s-%d", def.Resource, r.createCount[def.Resource])))
	created.SetResourceVersion(fmt.Sprint(100 + r.createCount[def.Resource]))
	if def.Resource == "deployments" {
		created.SetGeneration(1)
		replicas := deploymentReplicas(created)
		_ = unstructured.SetNestedMap(created.Object, map[string]any{
			"observedGeneration": int64(1), "readyReplicas": replicas, "updatedReplicas": replicas, "availableReplicas": replicas,
		}, "status")
		r.listItems["deployments"] = append(r.listItems["deployments"], *created.DeepCopy())
		replicaSet := blueGreenOwnedReplicaSet(namespace, created)
		replicaSetSelector, _, _ := unstructured.NestedStringMap(replicaSet.Object, "spec", "selector", "matchLabels")
		containers, _, _ := unstructured.NestedSlice(created.Object, "spec", "template", "spec", "containers")
		containerName, image := "", ""
		if len(containers) > 0 {
			if container, ok := containers[0].(map[string]any); ok {
				containerName, _ = container["name"].(string)
				image, _ = container["image"].(string)
			}
		}
		imageID := r.candidateImageID
		if imageID == "" {
			imageID = "docker-pullable://" + image
		}
		for index := int64(0); index < replicas; index++ {
			candidatePod := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{
					"name": fmt.Sprintf("orders-candidate-%d", index), "namespace": namespace, "uid": fmt.Sprintf("candidate-pod-uid-%d", index),
					"labels": toAnyStringMap(replicaSetSelector),
					"ownerReferences": []any{map[string]any{
						"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": replicaSet.GetName(), "uid": string(replicaSet.GetUID()), "controller": true,
					}},
				},
				"status": map[string]any{
					"phase":             "Running",
					"containerStatuses": []any{map[string]any{"name": containerName, "imageID": imageID, "ready": true}},
				},
			}}
			r.listItems["pods"] = append(r.listItems["pods"], *candidatePod)
			r.listItems["endpointslices"] = append(r.listItems["endpointslices"], *blueGreenEndpointSlice(fmt.Sprintf("candidate-slice-%d", index), namespace, r.candidateService, candidatePod.GetName()))
		}
		r.listItems["replicasets"] = append(r.listItems["replicasets"], *replicaSet)
	} else if def.Resource == "services" {
		r.listItems["services"] = append(r.listItems["services"], *created.DeepCopy())
	}
	r.objects[key] = created
	return created.DeepCopy(), createErr
}

func blueGreenEndpointSlice(name, namespace, service, podName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice",
		"metadata": map[string]any{"name": name, "namespace": namespace, "labels": map[string]any{"kubernetes.io/service-name": service}},
		"endpoints": []any{map[string]any{
			"conditions": map[string]any{"ready": true},
			"targetRef":  map[string]any{"kind": "Pod", "name": podName},
		}},
	}}
}

func blueGreenOwnedReplicaSet(namespace string, deployment *unstructured.Unstructured) *unstructured.Unstructured {
	selector, _, _ := unstructured.NestedStringMap(deployment.Object, "spec", "template", "metadata", "labels")
	selector["pod-template-hash"] = deployment.GetName() + "-hash"
	labels := map[string]string{}
	for key, value := range selector {
		labels[key] = value
	}
	replicas, found, _ := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	if !found {
		replicas = 1
	}
	containers, _, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	replicaSetUID := string(deployment.GetUID()) + "-rs"
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{
			"name": deployment.GetName() + "-rs", "namespace": namespace, "uid": replicaSetUID,
			"labels":      toAnyStringMap(labels),
			"annotations": map[string]any{"deployment.kubernetes.io/revision": "1"},
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment", "name": deployment.GetName(), "uid": string(deployment.GetUID()), "controller": true,
			}}},
		"spec": map[string]any{
			"replicas": replicas,
			"selector": map[string]any{"matchLabels": toAnyStringMap(selector)},
			"template": map[string]any{"spec": map[string]any{"containers": containers}},
		},
	}}
}

func (r *blueGreenSelectorRuntime) GetResource(_ context.Context, def kube.ResourceDef, _, _ string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	if def.Resource == "services" && r.service != nil {
		return r.service.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: def.Group, Resource: def.Resource}, "missing")
}

func (r *blueGreenSelectorRuntime) PatchResource(_ context.Context, def kube.ResourceDef, _, _ string, patchType types.PatchType, data []byte, _ metav1.PatchOptions) (*unstructured.Unstructured, error) {
	r.patchCount++
	if def.Resource != "services" || patchType != types.JSONPatchType {
		return nil, errors.New("unexpected resource patch")
	}
	var operations []map[string]any
	if err := json.Unmarshal(data, &operations); err != nil || len(operations) != 3 || operations[0]["op"] != "test" || operations[1]["op"] != "test" || operations[2]["op"] != "replace" {
		return nil, errors.New("service patch is not guarded by revision and selector tests")
	}
	if r.applyPatch {
		if err := unstructured.SetNestedStringMap(r.service.Object, r.nextSelector, "spec", "selector"); err != nil {
			return nil, err
		}
		r.service.SetResourceVersion("next-rv")
	}
	return r.service.DeepCopy(), r.patchErr
}

func (r *blueGreenIsolationRuntime) GetResource(_ context.Context, def kube.ResourceDef, _, name string, _ metav1.GetOptions) (*unstructured.Unstructured, error) {
	if object := r.objects[def.Resource+"/"+name]; object != nil {
		return object.DeepCopy(), nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Group: def.Group, Resource: def.Resource}, name)
}

func (r *blueGreenIsolationRuntime) ListResources(_ context.Context, def kube.ResourceDef, _ string, _ metav1.ListOptions) (*unstructured.UnstructuredList, error) {
	if list := r.lists[def.Resource]; list != nil {
		return list.DeepCopy(), nil
	}
	return &unstructured.UnstructuredList{}, nil
}

func TestBlueGreenCandidateIsolationResumesOnlyJournaledOwnedResources(t *testing.T) {
	plan, stableDeployment, stableService := blueGreenResourcePlan(t)
	stabilizeBlueGreenTestResources(t, plan, stableDeployment, stableService)
	resources, err := BuildBlueGreenCandidateResources(plan, 77, stableDeployment, stableService)
	if err != nil {
		t.Fatal(err)
	}
	candidateDeployment := resources.Deployment.DeepCopy()
	candidateDeployment.SetUID(types.UID("candidate-deployment-uid"))
	candidateService := resources.Service.DeepCopy()
	candidateService.SetUID(types.UID("candidate-service-uid"))
	selector, _, _ := unstructured.NestedStringMap(candidateDeployment.Object, "spec", "selector", "matchLabels")
	replicaSet := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "ReplicaSet",
		"metadata": map[string]any{
			"name": "orders-api-bg-77-abcde", "namespace": "prod",
			"ownerReferences": []any{map[string]any{
				"apiVersion": "apps/v1", "kind": "Deployment", "name": candidateDeployment.GetName(),
				"uid": string(candidateDeployment.GetUID()), "controller": true,
			}},
		},
		"spec": map[string]any{"selector": map[string]any{"matchLabels": toAnyStringMap(selector)}},
	}}
	deploymentList := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*stableDeployment.DeepCopy(), *candidateDeployment.DeepCopy()}}
	replicaSetList := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*replicaSet}}
	serviceList := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*stableService.DeepCopy(), *candidateService.DeepCopy()}}
	runtime := &blueGreenIsolationRuntime{
		recordingK8s: &recordingK8s{},
		objects: map[string]*unstructured.Unstructured{
			"deployments/" + candidateDeployment.GetName(): candidateDeployment,
			"services/" + candidateService.GetName():       candidateService,
		},
		lists: map[string]*unstructured.UnstructuredList{
			"deployments": deploymentList, "replicasets": replicaSetList, "services": serviceList,
		},
	}
	store := NewStoreWithData(nil, nil, nil, nil, nil)
	if err := journalCandidateCreate(t, store, 41, plan, 77, resources.Deployment, blueGreenCandidateDeploy, "Deployment"); err != nil {
		t.Fatal(err)
	}
	if err := journalCandidateCreate(t, store, 41, plan, 77, resources.Service, blueGreenCandidateService, "Service"); err != nil {
		t.Fatal(err)
	}
	candidateLabels, _, _ := unstructured.NestedStringMap(resources.Deployment.Object, "spec", "template", "metadata", "labels")
	if err := validateBlueGreenCandidateIsolation(context.Background(), runtime, "prod", candidateLabels, store, 41, &resources); err != nil {
		t.Fatalf("journaled candidate Deployment, owned ReplicaSet and preview Service should be resumable: %v", err)
	}

	// A similarly named resource or matching selector without a durable action
	// record is treated as an external collision, even when its manifest matches.
	if err := validateBlueGreenCandidateIsolation(context.Background(), runtime, "prod", candidateLabels, NewStoreWithData(nil, nil, nil, nil, nil), 41, &resources); !errors.Is(err, ErrConflict) {
		t.Fatalf("unrecorded candidate resource error=%v, want conflict", err)
	}
	observedStore := NewStoreWithData(nil, nil, nil, nil, nil)
	if err := journalCandidateCreate(t, observedStore, 42, plan, 77, resources.Deployment, blueGreenCandidateDeploy, "Deployment"); err != nil {
		t.Fatal(err)
	}
	if err := journalCandidateCreate(t, observedStore, 42, plan, 77, resources.Service, blueGreenCandidateService, "Service"); err != nil {
		t.Fatal(err)
	}
	if err := completeBlueGreenAction(observedStore, 42, blueGreenCandidateDeploy, ExecutionActionApplied, blueGreenActionObservation{Name: candidateDeployment.GetName(), UID: string(candidateDeployment.GetUID())}, "candidate_create_confirmed"); err != nil {
		t.Fatal(err)
	}
	if err := completeBlueGreenAction(observedStore, 42, blueGreenCandidateService, ExecutionActionApplied, blueGreenActionObservation{Name: candidateService.GetName(), UID: string(candidateService.GetUID())}, "candidate_create_confirmed"); err != nil {
		t.Fatal(err)
	}
	replacementDeployment := candidateDeployment.DeepCopy()
	replacementDeployment.SetUID(types.UID("replacement-candidate-uid"))
	runtime.objects["deployments/"+candidateDeployment.GetName()] = replacementDeployment
	if err := validateBlueGreenCandidateIsolation(context.Background(), runtime, "prod", candidateLabels, observedStore, 42, &resources); !errors.Is(err, ErrConflict) {
		t.Fatalf("replaced candidate UID error=%v, want conflict", err)
	}
	runtime.objects["deployments/"+candidateDeployment.GetName()] = candidateDeployment

	foreignService := blueGreenService("prod", "other-service")
	if err := unstructured.SetNestedStringMap(foreignService.Object, candidateLabels, "spec", "selector"); err != nil {
		t.Fatal(err)
	}
	runtime.lists["services"].Items = append(runtime.lists["services"].Items, *foreignService)
	if err := validateBlueGreenCandidateIsolation(context.Background(), runtime, "prod", candidateLabels, store, 41, &resources); !errors.Is(err, ErrConflict) {
		t.Fatalf("unrelated Service matching candidate labels error=%v, want conflict", err)
	}
}

func TestPrepareBlueGreenCandidateCreatesReadyIsolatedObjectsAndReplaysSafely(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	plan.ID = 77
	plan.ReleaseID = plan.Snapshot.Artifact.ReleaseID
	plan.Snapshot.Target.ClusterID = 1
	plan.Snapshot.Target.AppID = 7
	image := plan.Snapshot.Artifact.Image
	log := DeployLog{
		ID: 61, ClusterID: 1, ReleaseID: plan.ReleaseID,
		Namespace: "prod", DeploymentName: "orders-api", ContainerName: "api", ImageFull: &image,
	}
	runtime := newBlueGreenPreparationRuntime(plan, 77, deployment, service)
	store := NewStoreWithData(nil, nil, nil, nil, nil)
	identity, err := prepareBlueGreenCandidate(context.Background(), store, runtime, log, plan, 77)
	if err != nil {
		t.Fatalf("prepareBlueGreenCandidate: %v", err)
	}
	if identity.CandidateDeployment != "orders-api-bg-77" || identity.CandidateService != "orders-entry-bg-77" || identity.CandidateEndpointCount != 2 || identity.StableEndpointCount != 1 {
		t.Fatalf("candidate identity=%+v", identity)
	}
	if identity.CandidateImageEvidence.ObservationStatus != "verified" || !strings.Contains(identity.CandidateImageEvidence.Stdout, imageReferenceDigest(image)) {
		t.Fatalf("candidate runtime image evidence=%+v", identity.CandidateImageEvidence)
	}
	if runtime.createCount["deployments"] != 1 || runtime.createCount["services"] != 1 || runtime.patchCount != 2 {
		t.Fatalf("first preparation writes: creates=%v patches=%d", runtime.createCount, runtime.patchCount)
	}
	if _, err := prepareBlueGreenCandidate(context.Background(), store, runtime, log, plan, 77); err != nil {
		t.Fatalf("retrying confirmed candidate preparation: %v", err)
	}
	if runtime.createCount["deployments"] != 1 || runtime.createCount["services"] != 1 || runtime.patchCount != 2 {
		t.Fatalf("replay issued duplicate writes: creates=%v patches=%d", runtime.createCount, runtime.patchCount)
	}
	actions, err := store.ExecutionActions(log.ID)
	if err != nil || len(actions) != 4 {
		t.Fatalf("persisted blue-green actions=%+v err=%v", actions, err)
	}
	for _, action := range actions {
		if action.Status != ExecutionActionApplied {
			t.Fatalf("action %s status=%q, want applied", action.ActionKey, action.Status)
		}
	}
	var candidatePodName string
	for _, pod := range runtime.listItems["pods"] {
		podLabels := pod.GetLabels()
		if podLabels[plan.Snapshot.Target.CandidateLabelKey] == plan.Snapshot.Target.CandidateLabelValue {
			candidatePodName = pod.GetName()
		}
	}
	if candidatePodName == "" {
		t.Fatal("candidate pod was not observed")
	}
	runtime.listItems["endpointslices"] = append(runtime.listItems["endpointslices"], *blueGreenEndpointSlice("production-candidate-slice", plan.Snapshot.Target.Namespace, plan.Snapshot.Target.ServiceName, candidatePodName))
	if err := switchBlueGreenServiceToCandidate(context.Background(), store, runtime, log.ID, plan, 77); err != nil {
		t.Fatalf("candidate cutover: %v", err)
	}
	// EndpointSlice controllers may retain old ready endpoints briefly after the
	// Service selector changes. This fake represents the converged observation.
	productionSlices := runtime.listItems["endpointslices"][:0]
	for _, slice := range runtime.listItems["endpointslices"] {
		serviceName, _, _ := unstructured.NestedString(slice.Object, "metadata", "labels", "kubernetes.io/service-name")
		if serviceName != plan.Snapshot.Target.ServiceName {
			productionSlices = append(productionSlices, slice)
		}
	}
	runtime.listItems["endpointslices"] = productionSlices
	runtime.listItems["endpointslices"] = append(runtime.listItems["endpointslices"], *blueGreenEndpointSlice("production-candidate-slice", plan.Snapshot.Target.Namespace, plan.Snapshot.Target.ServiceName, candidatePodName))
	activeDeployment, activeRef, err := promoteBlueGreenCandidateToStable(context.Background(), store, runtime, log.ID, plan, 77)
	if err != nil || activeDeployment != blueGreenResourceName(plan.Snapshot.Target.Deployment, plan.ID) || activeRef.UID == "" || activeRef.Name != activeDeployment {
		t.Fatalf("verified candidate promotion name=%q ref=%+v error=%v", activeDeployment, activeRef, err)
	}
	replayedDeployment, replayedRef, err := promoteBlueGreenCandidateToStable(context.Background(), store, runtime, log.ID, plan, 77)
	if err != nil || replayedDeployment != activeDeployment || replayedRef.UID != activeRef.UID {
		t.Fatalf("replayed candidate promotion name=%q ref=%+v error=%v", replayedDeployment, replayedRef, err)
	}
	if _, err := runtime.GetResource(context.Background(), kube.ResourceDef{Resource: "services"}, plan.Snapshot.Target.Namespace, identity.CandidateService, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("promoted candidate preview Service remains routable, err=%v", err)
	}
	if err := restoreBlueGreenEntryToStable(context.Background(), store, runtime, log.ID, plan, 77); err != nil {
		t.Fatalf("restore stable Service entry: %v", err)
	}
}

func TestPrepareBlueGreenCandidateKeepsUncomparablePodImageIDUnknown(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	plan.ID = 88
	plan.ReleaseID = plan.Snapshot.Artifact.ReleaseID
	plan.Snapshot.Target.ClusterID = 1
	plan.Snapshot.Target.AppID = 7
	image := plan.Snapshot.Artifact.Image
	log := DeployLog{ID: 64, ClusterID: 1, ReleaseID: plan.ReleaseID, Namespace: "prod", DeploymentName: "orders-api", ContainerName: "api", ImageFull: &image}
	runtime := newBlueGreenPreparationRuntime(plan, 88, deployment, service)
	runtime.candidateImageID = "containerd://sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	identity, err := prepareBlueGreenCandidate(context.Background(), NewStoreWithData(nil, nil, nil, nil, nil), runtime, log, plan, 88)
	if err != nil {
		t.Fatalf("prepare candidate should remain successful when runtime ID is not comparable: %v", err)
	}
	if identity.CandidateImageEvidence.ObservationStatus != "unknown" || !strings.Contains(identity.CandidateImageEvidence.Stdout, "registry digest") {
		t.Fatalf("candidate runtime image evidence=%+v", identity.CandidateImageEvidence)
	}
}

func TestAppendCandidateImageEvidenceReplacesPriorObservation(t *testing.T) {
	log := DeployLog{StepDetail: marshalSteps([]StepResult{
		{Step: "prepare_candidate", Success: true, Stdout: "候选已就绪"},
		{Step: "verify_candidate_pod_image", Success: true, ObservationStatus: "unknown", Stdout: "尚未观察"},
	})}
	appendCandidateImageEvidence(&log, StepResult{
		Step: "verify_candidate_pod_image", Success: true, ObservationStatus: "verified", Stdout: "摘要一致",
	})

	var steps []StepResult
	if err := json.Unmarshal([]byte(log.StepDetail), &steps); err != nil {
		t.Fatalf("decode step detail: %v", err)
	}
	if len(steps) != 2 || steps[0].Step != "prepare_candidate" || steps[1].ObservationStatus != "verified" || steps[1].Stdout != "摘要一致" {
		t.Fatalf("candidate evidence was not replaced while preserving other steps: %+v", steps)
	}
}

func TestPrepareBlueGreenCandidateResolvesLostCreateResponses(t *testing.T) {
	for _, resource := range []string{"deployments", "services"} {
		t.Run(resource, func(t *testing.T) {
			plan, deployment, service := blueGreenResourcePlan(t)
			plan.ID = 78
			plan.ReleaseID = plan.Snapshot.Artifact.ReleaseID
			plan.Snapshot.Target.ClusterID = 1
			plan.Snapshot.Target.AppID = 7
			image := plan.Snapshot.Artifact.Image
			log := DeployLog{
				ID: 62, ClusterID: 1, ReleaseID: plan.ReleaseID,
				Namespace: "prod", DeploymentName: "orders-api", ContainerName: "api", ImageFull: &image,
			}
			runtime := newBlueGreenPreparationRuntime(plan, 78, deployment, service)
			runtime.createErrors = map[string]error{resource: errors.New("connection reset after create")}
			runtime.createAppliedOnError = map[string]bool{resource: true}
			store := NewStoreWithData(nil, nil, nil, nil, nil)

			if _, err := prepareBlueGreenCandidate(context.Background(), store, runtime, log, plan, 78); err != nil {
				t.Fatalf("readback should resolve the lost %s create response: %v", resource, err)
			}
			if runtime.createCount[resource] != 1 {
				t.Fatalf("%s create count=%d, want one write", resource, runtime.createCount[resource])
			}
			actions, err := store.ExecutionActions(log.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range actions {
				if action.Status != ExecutionActionApplied {
					t.Fatalf("action %s status=%q, want applied after readback", action.ActionKey, action.Status)
				}
			}

			if _, err := prepareBlueGreenCandidate(context.Background(), store, runtime, log, plan, 78); err != nil {
				t.Fatalf("retry after resolved %s create response: %v", resource, err)
			}
			if runtime.createCount[resource] != 1 {
				t.Fatalf("retry duplicated %s create: count=%d", resource, runtime.createCount[resource])
			}
		})
	}
}

func TestPrepareBlueGreenCandidateResolvesLostBaselinePatchResponses(t *testing.T) {
	for _, resource := range []string{"deployments", "services"} {
		t.Run(resource, func(t *testing.T) {
			plan, deployment, service := blueGreenResourcePlan(t)
			plan.ID = 79
			plan.ReleaseID = plan.Snapshot.Artifact.ReleaseID
			plan.Snapshot.Target.ClusterID = 1
			plan.Snapshot.Target.AppID = 7
			image := plan.Snapshot.Artifact.Image
			log := DeployLog{
				ID: 63, ClusterID: 1, ReleaseID: plan.ReleaseID,
				Namespace: "prod", DeploymentName: "orders-api", ContainerName: "api", ImageFull: &image,
			}
			runtime := newBlueGreenPreparationRuntime(plan, 79, deployment, service)
			runtime.patchErrors = map[string]error{resource: errors.New("connection reset after patch")}
			runtime.applyPatchOnError = map[string]bool{resource: true}
			store := NewStoreWithData(nil, nil, nil, nil, nil)

			if _, err := prepareBlueGreenCandidate(context.Background(), store, runtime, log, plan, 79); err != nil {
				t.Fatalf("readback should resolve the lost %s patch response: %v", resource, err)
			}
			if runtime.patchCountByResource[resource] != 1 {
				t.Fatalf("%s patch count=%d, want one write", resource, runtime.patchCountByResource[resource])
			}
			actions, err := store.ExecutionActions(log.ID)
			if err != nil {
				t.Fatal(err)
			}
			actionKey := blueGreenStableLabelAction
			if resource == "services" {
				actionKey = blueGreenStableEntryAction
			}
			if got := executionActionStatus(actions, actionKey); got != ExecutionActionApplied {
				t.Fatalf("%s patch journal status=%q, want applied after readback", resource, got)
			}

			runtime.patchErrors = nil
			if _, err := prepareBlueGreenCandidate(context.Background(), store, runtime, log, plan, 79); err != nil {
				t.Fatalf("retry after resolved %s patch response: %v", resource, err)
			}
			if runtime.patchCountByResource[resource] != 1 {
				t.Fatalf("retry duplicated %s patch: count=%d", resource, runtime.patchCountByResource[resource])
			}
		})
	}
}

func TestBlueGreenServiceTrackSwitchReadbackAndRestore(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	stabilizeBlueGreenTestResources(t, plan, deployment, service)
	stableSelector, err := BlueGreenStableServiceSelector(plan.Snapshot.Target.ServiceSelector, plan.Snapshot.Target.StableLabelKey)
	if err != nil {
		t.Fatal(err)
	}
	candidateSelector, err := BlueGreenCandidateSelector(plan.Snapshot.Target.CandidateLabelKey, plan.Snapshot.Target.CandidateLabelValue)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &blueGreenSelectorRuntime{recordingK8s: &recordingK8s{}, service: service.DeepCopy(), nextSelector: candidateSelector, patchErr: errors.New("request timed out"), applyPatch: true}
	store := NewStoreWithData(nil, nil, nil, nil, nil)
	if err := switchBlueGreenServiceToCandidate(context.Background(), store, runtime, 51, plan, 77); err != nil {
		t.Fatalf("cutover whose readback confirms the target should succeed despite timeout response: %v", err)
	}
	if runtime.patchCount != 1 {
		t.Fatalf("conditional patch count=%d, want 1", runtime.patchCount)
	}
	actions, err := store.ExecutionActions(51)
	if err != nil || len(actions) != 1 || actions[0].Status != ExecutionActionApplied {
		t.Fatalf("cutover action=%+v err=%v", actions, err)
	}
	if err := switchBlueGreenServiceToCandidate(context.Background(), store, runtime, 51, plan, 77); err != nil {
		t.Fatalf("idempotent confirmed cutover: %v", err)
	}
	if runtime.patchCount != 1 {
		t.Fatalf("retry issued another patch, count=%d", runtime.patchCount)
	}
	runtime.nextSelector = stableSelector
	runtime.patchErr = nil
	if err := restoreBlueGreenServiceToStable(context.Background(), store, runtime, 51, plan, 77); err != nil {
		t.Fatalf("conditional recovery to stable: %v", err)
	}
	observed, _, _ := unstructured.NestedStringMap(runtime.service.Object, "spec", "selector")
	if !sameStringMap(observed, stableSelector) {
		t.Fatalf("recovery selector=%v, want stable selector %v", observed, stableSelector)
	}
	if runtime.patchCount != 2 {
		t.Fatalf("cutover and recovery patch count=%d, want 2", runtime.patchCount)
	}
}

func TestBlueGreenServiceTrackRejectsExternalSelectorDrift(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	stabilizeBlueGreenTestResources(t, plan, deployment, service)
	_, err := BlueGreenStableServiceSelector(plan.Snapshot.Target.ServiceSelector, plan.Snapshot.Target.StableLabelKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedStringMap(service.Object, map[string]string{"app": "external"}, "spec", "selector"); err != nil {
		t.Fatal(err)
	}
	runtime := &blueGreenSelectorRuntime{recordingK8s: &recordingK8s{}, service: service}
	store := NewStoreWithData(nil, nil, nil, nil, nil)
	if err := switchBlueGreenServiceToCandidate(context.Background(), store, runtime, 52, plan, 77); !errors.Is(err, ErrConflict) {
		t.Fatalf("selector drift error=%v, want conflict", err)
	}
	if runtime.patchCount != 0 {
		t.Fatalf("selector drift sent %d patch requests", runtime.patchCount)
	}
	actions, _ := store.ExecutionActions(52)
	if len(actions) != 0 {
		t.Fatalf("selector drift wrote action intents before baseline validation: %+v", actions)
	}
}

func TestBlueGreenServiceTrackDoesNotAdoptTargetAfterDefinitiveRejection(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	stabilizeBlueGreenTestResources(t, plan, deployment, service)
	runtime := &blueGreenSelectorRuntime{
		recordingK8s: &recordingK8s{}, service: service,
		nextSelector: map[string]string{plan.Snapshot.Target.CandidateLabelKey: plan.Snapshot.Target.CandidateLabelValue},
		patchErr:     apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, service.GetName(), errors.New("denied")),
		applyPatch:   true,
	}
	store := NewStoreWithData(nil, nil, nil, nil, nil)
	err := switchBlueGreenServiceToCandidate(context.Background(), store, runtime, 53, plan, 77)
	if !apierrors.IsForbidden(err) {
		t.Fatalf("definitive Kubernetes rejection error=%v, want forbidden", err)
	}
	actions, actionErr := store.ExecutionActions(53)
	if actionErr != nil || len(actions) != 1 || actions[0].Status != ExecutionActionNotApplied {
		t.Fatalf("rejected selector action=%+v err=%v", actions, actionErr)
	}
	if err := switchBlueGreenServiceToCandidate(context.Background(), store, runtime, 53, plan, 77); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry must not claim a target state after a definitive rejection: %v", err)
	}
}

func TestValidateBlueGreenCandidateDeploymentRequiresObservedReadyReplicas(t *testing.T) {
	plan, deployment, service := blueGreenResourcePlan(t)
	stabilizeBlueGreenTestResources(t, plan, deployment, service)
	resources, err := BuildBlueGreenCandidateResources(plan, 77, deployment, service)
	if err != nil {
		t.Fatal(err)
	}
	current := resources.Deployment.DeepCopy()
	current.SetUID(types.UID("candidate-uid"))
	current.SetGeneration(4)
	if err := unstructured.SetNestedMap(current.Object, map[string]any{
		"observedGeneration": int64(4), "readyReplicas": int64(2), "updatedReplicas": int64(2), "availableReplicas": int64(2),
	}, "status"); err != nil {
		t.Fatal(err)
	}
	if err := validateBlueGreenCandidateDeploymentReady(current, resources.Deployment, current, plan.ID, 77, "api", plan.Snapshot.Artifact.Image); err != nil {
		t.Fatalf("fully observed candidate should be ready: %v", err)
	}
	if err := unstructured.SetNestedField(current.Object, int64(1), "status", "availableReplicas"); err != nil {
		t.Fatal(err)
	}
	if err := validateBlueGreenCandidateDeploymentReady(current, resources.Deployment, current, plan.ID, 77, "api", plan.Snapshot.Artifact.Image); !errors.Is(err, ErrConflict) {
		t.Fatalf("candidate without full available replicas error=%v, want conflict", err)
	}
}

func TestBlueGreenEndpointObservationRequiresOnlyReadyTrackPods(t *testing.T) {
	const candidateLabel = "delivery.platform.io/run-id"
	endpointDef, err := kube.LookupResource("discovery.k8s.io", "v1", "endpointslices")
	if err != nil {
		t.Fatal(err)
	}
	podDef, err := kube.LookupResource("", "v1", "pods")
	if err != nil {
		t.Fatal(err)
	}
	pod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{
			"name": "candidate-pod", "namespace": "prod", "labels": map[string]any{candidateLabel: "r-1"},
		},
	}}
	endpointSlice := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice",
		"metadata": map[string]any{"name": "preview-1", "namespace": "prod", "labels": map[string]any{"kubernetes.io/service-name": "preview"}},
		"endpoints": []any{map[string]any{
			"conditions": map[string]any{"ready": true},
			"targetRef":  map[string]any{"kind": "Pod", "name": pod.GetName()},
		}},
	}}
	runtime := &blueGreenIsolationRuntime{
		recordingK8s: &recordingK8s{},
		lists: map[string]*unstructured.UnstructuredList{
			"endpointslices": {Items: []unstructured.Unstructured{*endpointSlice}},
			"pods":           {Items: []unstructured.Unstructured{*pod}},
		},
	}
	ready, count, err := blueGreenTrackEndpointsReady(context.Background(), runtime, endpointDef, podDef, "prod", "preview", candidateLabel, "r-1", "候选")
	if err != nil || !ready || count != 1 {
		t.Fatalf("candidate endpoints ready=%v count=%d err=%v", ready, count, err)
	}
	if err := unstructured.SetNestedField(pod.Object, "other", "metadata", "labels", candidateLabel); err != nil {
		t.Fatal(err)
	}
	runtime.lists["pods"].Items[0] = *pod
	if _, _, err := blueGreenTrackEndpointsReady(context.Background(), runtime, endpointDef, podDef, "prod", "preview", candidateLabel, "r-1", "候选"); !errors.Is(err, ErrConflict) {
		t.Fatalf("candidate Service selecting a non-candidate Pod error=%v, want conflict", err)
	}
}

func TestBlueGreenProductionEndpointObservationWaitsForOldTrackToDrain(t *testing.T) {
	const candidateLabel = "delivery.platform.io/run-id"
	endpointDef, err := kube.LookupResource("discovery.k8s.io", "v1", "endpointslices")
	if err != nil {
		t.Fatal(err)
	}
	podDef, err := kube.LookupResource("", "v1", "pods")
	if err != nil {
		t.Fatal(err)
	}
	oldPod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "old-pod", "namespace": "prod", "labels": map[string]any{candidateLabel: "old-run"}},
	}}
	candidatePod := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "candidate-pod", "namespace": "prod", "labels": map[string]any{candidateLabel: "r-1"}},
	}}
	endpointSlice := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice",
		"metadata": map[string]any{"name": "production", "namespace": "prod", "labels": map[string]any{"kubernetes.io/service-name": "entry"}},
		"endpoints": []any{
			map[string]any{"conditions": map[string]any{"ready": true}, "targetRef": map[string]any{"kind": "Pod", "name": oldPod.GetName()}},
			map[string]any{"conditions": map[string]any{"ready": true}, "targetRef": map[string]any{"kind": "Pod", "name": candidatePod.GetName()}},
		},
	}}
	runtime := &blueGreenIsolationRuntime{
		recordingK8s: &recordingK8s{},
		lists: map[string]*unstructured.UnstructuredList{
			"endpointslices": {Items: []unstructured.Unstructured{*endpointSlice}},
			"pods":           {Items: []unstructured.Unstructured{*oldPod, *candidatePod}},
		},
	}
	ready, count, err := blueGreenTrackEndpointsReadyInternal(context.Background(), runtime, endpointDef, podDef, "prod", "entry", candidateLabel, "r-1", "候选", true)
	if err != nil || ready || count != 1 {
		t.Fatalf("stale old endpoint ready=%v candidate count=%d err=%v, want pending with one candidate", ready, count, err)
	}
	if _, _, err := blueGreenTrackEndpointsReady(context.Background(), runtime, endpointDef, podDef, "prod", "entry", candidateLabel, "r-1", "候选"); !errors.Is(err, ErrConflict) {
		t.Fatalf("strict preview observation error=%v, want conflict", err)
	}
}

func journalCandidateCreate(t *testing.T, store *Store, logID int64, plan DeliveryPlan, executionID int64, desired *unstructured.Unstructured, actionKey, kind string) error {
	t.Helper()
	hash, err := objectSpecHash(desired)
	if err != nil {
		return err
	}
	intent, err := json.Marshal(blueGreenActionIntent{
		PlanID: plan.ID, ExecutionID: executionID, Namespace: desired.GetNamespace(), Kind: kind,
		Name: desired.GetName(), Image: plan.Snapshot.Artifact.Image, SpecHash: hash,
	})
	if err != nil {
		return err
	}
	_, _, err = store.BeginExecutionAction(ExecutionAction{
		DeployLogID: logID, ActionKey: actionKey, ActionKind: "create_candidate_" + kind,
		Status: ExecutionActionIntent, Intent: intent,
	})
	return err
}

func TestBlueGreenServiceHashIgnoresAPIServerAllocatedFields(t *testing.T) {
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"spec": map[string]any{
			"type": "ClusterIP", "selector": map[string]any{"track": "candidate"},
			"ports": []any{map[string]any{"port": int64(80), "targetPort": int64(8080), "protocol": "TCP"}},
		},
	}}
	actual := desired.DeepCopy()
	spec := actual.Object["spec"].(map[string]any)
	spec["clusterIP"] = "10.96.0.10"
	spec["clusterIPs"] = []any{"10.96.0.10"}
	spec["ipFamilies"] = []any{"IPv4"}
	spec["ipFamilyPolicy"] = "SingleStack"
	spec["sessionAffinity"] = "None"
	spec["internalTrafficPolicy"] = "Cluster"
	spec["ports"].([]any)[0].(map[string]any)["nodePort"] = int64(30080)

	want, err := objectSpecHash(desired)
	if err != nil {
		t.Fatal(err)
	}
	got, err := objectSpecHash(actual)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("API-server allocated Service fields changed canonical spec hash: got %s, want %s", got, want)
	}

	spec["ports"].([]any)[0].(map[string]any)["port"] = int64(81)
	changed, err := objectSpecHash(actual)
	if err != nil {
		t.Fatal(err)
	}
	if changed == want {
		t.Fatal("meaningful Service port drift was ignored")
	}
}
