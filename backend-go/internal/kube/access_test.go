package kube

import (
	"context"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCanHonorsResourceNameRestrictions(t *testing.T) {
	rules := []ResourceRule{{
		Verbs: []string{"get", "patch"}, APIGroups: []string{"apps"},
		Resources: []string{"deployments"}, ResourceNames: []string{"orders-api"},
	}}
	if !Can(rules, "patch", "apps", "deployments", "orders-api") {
		t.Fatal("named Deployment permission should be granted")
	}
	if Can(rules, "patch", "apps", "deployments") {
		t.Fatal("a named permission must not appear resource-wide")
	}
	if Can(rules, "patch", "apps", "deployments", "payments-api") {
		t.Fatal("permission must not extend to another Deployment")
	}
	if !Can([]ResourceRule{{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}}}, "delete", "", "services", "entry") {
		t.Fatal("wildcard permission should still match")
	}
}

func TestAccessRulesPreservesNamesAndRejectsIncompleteReview(t *testing.T) {
	for _, tc := range []struct {
		name       string
		incomplete bool
		wantErr    bool
	}{
		{name: "complete"},
		{name: "incomplete", incomplete: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientset := fake.NewSimpleClientset()
			clientset.PrependReactor("create", "selfsubjectrulesreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				return true, &authv1.SelfSubjectRulesReview{Status: authv1.SubjectRulesReviewStatus{
					Incomplete: tc.incomplete,
					ResourceRules: []authv1.ResourceRule{{
						Verbs: []string{"patch"}, APIGroups: []string{"apps"},
						Resources: []string{"deployments"}, ResourceNames: []string{"orders-api"},
					}},
				}}, nil
			})
			client := NewWithClientset(clientset, nil, nil)
			rules, err := client.AccessRules(context.Background(), "orders")
			if tc.wantErr {
				if err == nil {
					t.Fatal("incomplete review must not be treated as authoritative")
				}
				return
			}
			if err != nil {
				t.Fatalf("AccessRules: %v", err)
			}
			if !Can(rules, "patch", "apps", "deployments", "orders-api") || Can(rules, "patch", "apps", "deployments") {
				t.Fatalf("resource-name restriction not preserved: %+v", rules)
			}
		})
	}
}
