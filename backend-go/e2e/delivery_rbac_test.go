package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"deploy-platform/backend-go/internal/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
)

// TestDeliveryRBACResourceNamesE2E proves the apiserver's live
// SelfSubjectRulesReview preserves Deployment name scoping for a real
// namespace-bound execution identity.
func TestDeliveryRBACResourceNamesE2E(t *testing.T) {
	skipUnlessE2E(t)
	skipUnlessLocalKindContext(t)

	namespace := "e2e-rbac-" + e2eRunID(t)
	serviceAccount := "delivery-worker"
	t.Cleanup(func() {
		cmd := exec.Command("kubectl", "delete", "namespace", namespace, "--ignore-not-found", "--wait=true", "--timeout=60s")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("delete isolated RBAC namespace %s: %v\n%s", namespace, err, output)
		}
	})
	kubectlApply(t, fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: orders-runtime
  namespace: %s
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: orders-runtime-config
  namespace: %s
data:
  mode: production
---
apiVersion: v1
kind: Secret
metadata:
  name: orders-runtime-secret
  namespace: %s
type: Opaque
data:
  password: dGVzdC1vbmx5
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: delivery-worker-scoped
  namespace: %s
rules:
- apiGroups: [apps]
  resources: [deployments]
  resourceNames: ["orders-api"]
  verbs: [get, update]
- apiGroups: [apps]
  resources: [deployments]
  resourceNames: ["update-only"]
  verbs: [update]
- apiGroups: [apps]
  resources: [deployments]
  resourceNames: ["ssh-api"]
  verbs: [get, patch, watch]
- apiGroups: [""]
  resources: [serviceaccounts]
  resourceNames: ["orders-runtime"]
  verbs: [get]
- apiGroups: [""]
  resources: [configmaps]
  resourceNames: ["orders-runtime-config"]
  verbs: [get]
- apiGroups: [""]
  resources: [secrets]
  resourceNames: ["orders-runtime-secret"]
  verbs: [get]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: delivery-worker-scoped
  namespace: %s
subjects:
- kind: ServiceAccount
  name: %s
  namespace: %s
roleRef:
  kind: Role
  name: delivery-worker-scoped
  apiGroup: rbac.authorization.k8s.io

`, namespace, serviceAccount, namespace, namespace, namespace, namespace, namespace, namespace, serviceAccount, namespace))
	token := run(t, "kubectl", "create", "token", serviceAccount, "-n", namespace, "--duration=10m")
	adminKubeconfig := run(t, "kubectl", "config", "view", "--minify", "--raw", "-o", "json")
	adminConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(adminKubeconfig))
	if err != nil {
		t.Fatalf("load current Kind API endpoint and CA: %v", err)
	}
	worker, err := kube.NewFromToken(adminConfig.Host, token, adminConfig.TLSClientConfig.CAData, adminConfig.TLSClientConfig.Insecure)
	if err != nil {
		t.Fatalf("create scoped worker client: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	rules, err := worker.AccessRules(ctx, namespace)
	if err != nil {
		t.Fatalf("SelfSubjectRulesReview for scoped worker: %v", err)
	}
	for _, check := range []struct {
		verb, name string
		want       bool
	}{
		{"get", "orders-api", true},
		{"update", "orders-api", true},
		{"update", "update-only", true},
		{"get", "update-only", false},
		{"patch", "ssh-api", true},
		{"watch", "ssh-api", true},
		{"patch", "orders-api", false},
	} {
		got := kube.Can(rules, check.verb, "apps", "deployments", check.name)
		if got != check.want {
			t.Errorf("Can(%s deployments.apps %s) = %t, want %t; rules=%+v", check.verb, check.name, got, check.want, rules)
		}
	}
	if !kube.Can(rules, "get", "", "serviceaccounts", "orders-runtime") || kube.Can(rules, "get", "", "serviceaccounts", "default") {
		t.Errorf("ServiceAccount permission must remain scoped to orders-runtime; rules=%+v", rules)
	}
	serviceAccountDef := kube.ResourceDef{Version: "v1", Resource: "serviceaccounts", Kind: "ServiceAccount", Namespaced: true}
	targetServiceAccount, err := worker.GetResource(ctx, serviceAccountDef, namespace, "orders-runtime", metav1.GetOptions{})
	if err != nil || targetServiceAccount.GetName() != "orders-runtime" {
		t.Fatalf("named target ServiceAccount read = %v, %v; want orders-runtime", targetServiceAccount, err)
	}
	if _, err := worker.GetResource(ctx, serviceAccountDef, namespace, "default", metav1.GetOptions{}); !apierrors.IsForbidden(err) {
		t.Errorf("reading default ServiceAccount error=%v, want Forbidden", err)
	}
	for _, reference := range []struct {
		resource, kind, name string
	}{
		{resource: "configmaps", kind: "ConfigMap", name: "orders-runtime-config"},
		{resource: "secrets", kind: "Secret", name: "orders-runtime-secret"},
	} {
		def, defErr := kube.LookupResource("", "v1", reference.resource)
		if defErr != nil {
			t.Fatal(defErr)
		}
		metadata, metadataErr := worker.GetObjectMetadata(ctx, def, namespace, reference.name)
		if metadataErr != nil || metadata.GetName() != reference.name || metadata.GetNamespace() != namespace || metadata.GetUID() == "" || metadata.GetResourceVersion() == "" {
			t.Errorf("metadata-only %s read=%+v error=%v", reference.kind, metadata, metadataErr)
		}
	}
	if !kube.Can(rules, "get", "", "configmaps", "orders-runtime-config") || kube.Can(rules, "get", "", "configmaps", "another-config") || !kube.Can(rules, "get", "", "secrets", "orders-runtime-secret") || kube.Can(rules, "get", "", "secrets", "another-secret") {
		t.Errorf("external configuration get permissions must remain name-scoped; rules=%+v", rules)
	}
	if kube.Can(rules, "update", "apps", "deployments") {
		t.Fatalf("name-scoped Deployment permissions widened to resource-wide update: %+v", rules)
	}
}
