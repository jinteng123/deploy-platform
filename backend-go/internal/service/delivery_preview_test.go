package service

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/kube"
)

func TestPreviewDeliveryCapabilitiesReturnsScopedServiceAndHPAObservations(t *testing.T) {
	_, _, _, runtime, _, cd := cdFixture(t)
	runtime.blueGreenExtraService = true
	runtime.blueGreenHPA = true

	preview, err := cd.PreviewDeliveryCapabilities(context.Background(), 2, "ns-prod", "app-prod", nil)
	if err != nil {
		t.Fatalf("PreviewDeliveryCapabilities: %v", err)
	}
	if preview.ClusterID != 2 || preview.Namespace != "ns-prod" || preview.Deployment != "app-prod" {
		t.Fatalf("preview target=%+v", preview)
	}
	if preview.DeploymentUID == "" || preview.ResourceVersion == "" || preview.ObservedAt.IsZero() {
		t.Fatalf("preview identity/time incomplete: %+v", preview)
	}
	if !reflect.DeepEqual(preview.ServiceNames, []string{"metrics-entry", "orders-entry"}) {
		t.Fatalf("matching services=%v", preview.ServiceNames)
	}
	if !reflect.DeepEqual(preview.AutoscalerNames, []string{"app-scaler"}) {
		t.Fatalf("autoscalers=%v", preview.AutoscalerNames)
	}
	statuses := map[string]string{}
	for _, check := range preview.Checks {
		statuses[check.Key] = check.Status
	}
	if statuses["deployment"] != "passed" || statuses["service-entry"] != "review" || statuses["autoscaling"] != "review" {
		t.Fatalf("check statuses=%v", statuses)
	}
}

func TestPreviewDeliveryCapabilitiesReportsUnknownInsteadOfEmptyWhenReadFails(t *testing.T) {
	_, _, _, runtime, _, cd := cdFixture(t)
	runtime.blueGreenListError = true

	preview, err := cd.PreviewDeliveryCapabilities(context.Background(), 2, "ns-prod", "app-prod", nil)
	if err != nil {
		t.Fatalf("PreviewDeliveryCapabilities should preserve partial read status: %v", err)
	}
	statuses := map[string]string{}
	for _, check := range preview.Checks {
		statuses[check.Key] = check.Status
	}
	if statuses["service-entry"] != "unknown" || statuses["autoscaling"] != "unknown" {
		t.Fatalf("read failures were not explicit: %v", statuses)
	}
	if len(preview.ServiceNames) != 0 || len(preview.AutoscalerNames) != 0 {
		t.Fatalf("unknown reads must not look like matches: services=%v hpas=%v", preview.ServiceNames, preview.AutoscalerNames)
	}
	for _, check := range preview.Checks {
		if check.Status == "unknown" && strings.TrimSpace(check.Detail) == "" {
			t.Errorf("unknown check %q has no explanation", check.Key)
		}
	}
}

func TestPreviewDeliveryCapabilitiesChecksWorkerPermissionsSeparatelyByStrategy(t *testing.T) {
	_, _, _, runtime, _, cd := cdFixture(t)
	runtime.accessRulesSet = true
	runtime.accessRules = []kube.ResourceRule{{
		Verbs: []string{"get", "update"}, APIGroups: []string{"apps"},
		Resources: []string{"deployments"}, ResourceNames: []string{"app-prod"},
	}, {
		Verbs: []string{"get"}, APIGroups: []string{""},
		Resources: []string{"serviceaccounts"}, ResourceNames: []string{"default"},
	}}

	preview, err := cd.PreviewDeliveryCapabilities(context.Background(), 2, "ns-prod", "app-prod", nil)
	if err != nil {
		t.Fatalf("PreviewDeliveryCapabilities: %v", err)
	}
	statuses := map[string]string{}
	for _, check := range preview.Checks {
		statuses[check.Key] = check.Status
	}
	if statuses["rolling-write-permission"] != "passed" {
		t.Fatalf("named Deployment update should allow rolling: %+v", preview.Checks)
	}
	if statuses["blue-green-write-permission"] != "blocked" {
		t.Fatalf("name-scoped update must not imply blue-green create/patch/delete rights: %+v", preview.Checks)
	}
}

func TestDeliveryPermissionCheckRequiresReadAndWriteForRolling(t *testing.T) {
	appsDeployments := []string{"apps"}
	deployments := []string{"deployments"}
	named := []string{"app-prod"}

	direct := deliveryPermissionCheckForRules([]kube.ResourceRule{{
		Verbs: []string{"update"}, APIGroups: appsDeployments,
		Resources: deployments, ResourceNames: named,
	}}, nil, "ns-prod", "app-prod", "kubeconfig", "rolling", "default", false)
	if direct.Status != "blocked" || !strings.Contains(direct.Detail, "get deployments.apps") {
		t.Fatalf("direct client missing get should block rolling: %+v", direct)
	}

	ssh := deliveryPermissionCheckForRules([]kube.ResourceRule{{
		Verbs: []string{"get", "patch"}, APIGroups: appsDeployments,
		Resources: deployments, ResourceNames: named,
	}}, nil, "ns-prod", "app-prod", "legacy_ssh", "rolling", "default", false)
	if ssh.Status != "blocked" || !strings.Contains(ssh.Detail, "watch deployments.apps") {
		t.Fatalf("SSH runtime missing watch should block rolling: %+v", ssh)
	}
}

func TestDeliveryPermissionCheckRequiresNamedServiceAccountReadWhenPullSecretsAreInherited(t *testing.T) {
	rules := []kube.ResourceRule{{
		Verbs: []string{"get", "update"}, APIGroups: []string{"apps"}, Resources: []string{"deployments"}, ResourceNames: []string{"app-prod"},
	}}
	check := deliveryPermissionCheckForRules(rules, nil, "ns-prod", "app-prod", "kubeconfig", "rolling", "orders-runtime", true)
	if check.Status != "blocked" || !strings.Contains(check.Detail, "get serviceaccounts") {
		t.Fatalf("inherited pull-secret lookup without Deployment read permission should block: %+v", check)
	}
	check = deliveryPermissionCheckForRules(append(rules, kube.ResourceRule{
		Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"serviceaccounts"}, ResourceNames: []string{"orders-runtime"},
	}), nil, "ns-prod", "app-prod", "kubeconfig", "rolling", "orders-runtime", true)
	if check.Status != "passed" {
		t.Fatalf("named serviceaccount read plus rolling rights should pass: %+v", check)
	}
}

func TestPreviewDeliveryCapabilitiesMarksUnavailableWorkerRulesUnknown(t *testing.T) {
	_, _, _, runtime, _, cd := cdFixture(t)
	runtime.accessRulesErr = context.DeadlineExceeded

	preview, err := cd.PreviewDeliveryCapabilities(context.Background(), 2, "ns-prod", "app-prod", nil)
	if err != nil {
		t.Fatalf("PreviewDeliveryCapabilities should preserve read observations: %v", err)
	}
	for _, key := range []string{"rolling-write-permission", "blue-green-write-permission"} {
		var found bool
		for _, check := range preview.Checks {
			if check.Key == key {
				found = true
				if check.Status != "unknown" {
					t.Errorf("%s status=%q, want unknown", key, check.Status)
				}
			}
		}
		if !found {
			t.Errorf("missing permission check %q", key)
		}
	}
}

func TestDeliveryPlanBlocksApprovalWhenWorkerPermissionIsMissing(t *testing.T) {
	_, store, _, runtime, _, cd := cdFixture(t)
	release := seedPlanRelease(t, store, runtime, "rolling")
	runtime.accessRulesSet = true // Empty rules: plan reads use a test double, auth review says no writes.
	runtime.accessRules = nil

	plan, _, err := cd.CreateDeliveryPlan(context.Background(), release.ID, 2, "alice")
	if err != nil {
		t.Fatalf("CreateDeliveryPlan: %v", err)
	}
	if len(plan.Blockers) == 0 {
		t.Fatal("missing worker update permission must block approval")
	}
	var sawDeniedCheck bool
	for _, check := range plan.Snapshot.Checks {
		if check.Key == "rolling-write-permission" {
			sawDeniedCheck = check.Status == "blocked"
		}
	}
	if !sawDeniedCheck {
		t.Fatalf("plan did not persist a blocked permission check: %+v", plan.Snapshot.Checks)
	}
	if _, err := cd.RequestDeliveryPlanApproval(context.Background(), plan.ID, "alice"); err == nil {
		t.Fatal("approval request should reject a plan without worker permissions")
	}
}

func TestPreviewDeliveryCapabilitiesValidatesTargetNames(t *testing.T) {
	_, _, _, _, _, cd := cdFixture(t)
	if _, err := cd.PreviewDeliveryCapabilities(context.Background(), 2, "bad/namespace", "app-prod", nil); err == nil {
		t.Fatal("expected invalid namespace to be rejected")
	}
	if _, err := cd.PreviewDeliveryCapabilities(context.Background(), 2, "ns-prod", "bad/deployment", nil); err == nil {
		t.Fatal("expected invalid Deployment name to be rejected")
	}
}
