package service

import (
	"context"
	"strings"
	"testing"

	"deploy-platform/backend-go/internal/kube"
)

// TestImpersonatingRunner freezes the argv contract: kubectl commands get
// --as/--as-group right after the binary, other commands pass through.
func TestImpersonatingRunner(t *testing.T) {
	var got []string
	inner := &sshStubRunner{fn: func(args []string) (string, string, error) {
		got = append([]string(nil), args...)
		return "yes", "", nil
	}}
	r := &impersonatingRunner{inner: inner, username: "dev-one", groups: []string{"devs", "leads"}}

	_, _, _ = r.RunSSH(context.Background(), Cluster{}, []string{"kubectl", "get", "pods", "-n", "default"})
	want := []string{"kubectl", "--as=dev-one", "--as-group=devs", "--as-group=leads", "get", "pods", "-n", "default"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", got, want)
	}

	// Non-kubectl argv passes through untouched.
	_, _, _ = r.RunSSH(context.Background(), Cluster{}, []string{"ls", "/tmp"})
	if strings.Join(got, " ") != "ls /tmp" {
		t.Fatalf("non-kubectl argv = %v", got)
	}
	_, _, _ = r.RunSSHStdin(context.Background(), Cluster{}, []string{"kubectl", "replace", "-f", "-"}, strings.NewReader("{}"))
	if !strings.Contains(strings.Join(got, " "), "--as=dev-one") {
		t.Fatalf("stdin argv = %v", got)
	}
	stream, _ := r.RunSSHStream(context.Background(), Cluster{}, []string{"kubectl", "logs", "p", "-f"})
	if stream != nil {
		stream.Close()
	}
	if !strings.Contains(strings.Join(got, " "), "--as=dev-one") {
		t.Fatalf("stream argv = %v", got)
	}
}

func TestParseCanIList(t *testing.T) {
	out := `Resources                                  Non-Resource URLs  Resource Names  Verbs
selfsubjectreviews.authentication.k8s.io     []                 []              [create]
pods                                       []                 []              [get list watch]
deployments.apps                           []                 [orders-api]     [get patch]
deployments.apps                           []                 []              [get list update patch]
*.*                                        []                 []              [*]
`
	rules := parseCanIList(out)
	if len(rules) != 5 {
		t.Fatalf("rules = %v", rules)
	}
	if !kube.Can(rules, "get", "", "pods") {
		t.Fatal("pods get denied")
	}
	if !kube.Can(rules, "update", "apps", "deployments") {
		t.Fatal("deployments update denied")
	}
	namedRule := rules[2:3]
	if !kube.Can(namedRule, "patch", "apps", "deployments", "orders-api") {
		t.Fatal("named Deployment patch denied")
	}
	if kube.Can(namedRule, "patch", "apps", "deployments", "payments-api") {
		t.Fatal("resource-name permission leaked to another Deployment")
	}
	if !kube.Can(rules, "delete", "", "secrets") {
		t.Fatal("wildcard should grant delete on secrets")
	}
	if !kube.Can(rules, "create", "", "pods/exec") {
		t.Fatal("wildcard should grant pods/exec create")
	}
}

func TestBuildCapabilities(t *testing.T) {
	rules := []kube.ResourceRule{
		{Verbs: []string{"get", "list"}, APIGroups: []string{"*"}, Resources: []string{"*"}},
		{Verbs: []string{"create"}, APIGroups: []string{""}, Resources: []string{"pods/exec"}},
	}
	caps := BuildCapabilities(rules)
	dep := caps.Resources["apps/v1/deployments"]
	if !dep.List || dep.Update || dep.Delete {
		t.Fatalf("deployments caps = %+v", dep)
	}
	if !caps.Exec {
		t.Fatal("exec must be granted")
	}
	if !caps.Logs {
		t.Fatal("pods get implies logs")
	}
	// Empty ruleset → everything denied.
	caps = BuildCapabilities(nil)
	if caps.Resources["apps/v1/deployments"].List || caps.Exec {
		t.Fatal("empty rules must deny all")
	}
}

func TestBuildCapabilitiesDoesNotWidenResourceNameScopedRules(t *testing.T) {
	rules := []kube.ResourceRule{{
		Verbs: []string{"get", "patch"}, APIGroups: []string{"apps"},
		Resources: []string{"deployments"}, ResourceNames: []string{"orders-api"},
	}}
	caps := BuildCapabilities(rules)
	if caps.Resources["apps/v1/deployments"].List || caps.Resources["apps/v1/deployments"].Update {
		t.Fatalf("name-scoped permissions must not appear as resource-wide UI capabilities: %+v", caps.Resources["apps/v1/deployments"])
	}
}

// TestSSHAccessRulesArgv freezes the can-i invocation.
func TestSSHAccessRulesArgv(t *testing.T) {
	runner := &sshStubRunner{fn: func(args []string) (string, string, error) {
		return "pods  []  []  [get]\n", "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}
	rules, err := rt.AccessRules(context.Background(), "ns1")
	if err != nil {
		t.Fatal(err)
	}
	call := runner.calls[len(runner.calls)-1]
	if call != "kubectl auth can-i --list -n ns1" {
		t.Fatalf("argv = %q", call)
	}
	if len(rules) != 1 || rules[0].Resources[0] != "pods" {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestSSHAccessRulesRejectsUnparseableOutput(t *testing.T) {
	runner := &sshStubRunner{fn: func(args []string) (string, string, error) {
		return "authorization review unavailable", "", nil
	}}
	rt := &sshKubernetes{runner: runner, cluster: Cluster{}}
	if _, err := rt.AccessRules(context.Background(), "ns1"); err == nil {
		t.Fatal("unparseable authorization output must be unknown, not an empty permission set")
	}
}

// TestBuildCapabilitiesCreate — the create verb maps to ResourceCaps.Create
// independently of update/delete.
func TestBuildCapabilitiesCreate(t *testing.T) {
	rules := []kube.ResourceRule{
		{Verbs: []string{"create"}, APIGroups: []string{""}, Resources: []string{"persistentvolumeclaims"}},
	}
	caps := BuildCapabilities(rules)
	if !caps.Resources["/v1/persistentvolumeclaims"].Create {
		t.Fatal("pvc create must be granted")
	}
	if caps.Resources["apps/v1/deployments"].Create {
		t.Fatal("deployments create must stay denied")
	}
}
