package kube

import (
	"context"
	"fmt"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResourceRule is one SelfSubjectRulesReview resourceRules row: which
// verbs the requesting identity holds on which resources in which API
// groups ("" = core). "*" entries wildcard the whole column.
type ResourceRule struct {
	Verbs         []string
	APIGroups     []string
	Resources     []string
	ResourceNames []string
}

// AccessRules reports what the caller's own identity may do in one
// namespace — the server-side answer behind `kubectl auth can-i --list`.
func (c *Client) AccessRules(ctx context.Context, namespace string) ([]ResourceRule, error) {
	review, err := c.client.AuthorizationV1().SelfSubjectRulesReviews().Create(ctx, &authv1.SelfSubjectRulesReview{
		Spec: authv1.SelfSubjectRulesReviewSpec{Namespace: namespace},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	if review.Status.Incomplete {
		return nil, fmt.Errorf("SelfSubjectRulesReview 返回不完整结果")
	}
	if review.Status.EvaluationError != "" {
		return nil, fmt.Errorf("SelfSubjectRulesReview 评估失败: %s", review.Status.EvaluationError)
	}
	rules := make([]ResourceRule, 0, len(review.Status.ResourceRules))
	for _, r := range review.Status.ResourceRules {
		rules = append(rules, ResourceRule{
			Verbs:         r.Verbs,
			APIGroups:     r.APIGroups,
			Resources:     r.Resources,
			ResourceNames: r.ResourceNames,
		})
	}
	return rules, nil
}

// Can reports whether the rules grant verb on (group, resource), optionally
// for one named object. Without resourceName, name-restricted rules do not
// grant a resource-wide capability; this is important for UI gating and
// operations that create dynamically named objects.
func Can(rules []ResourceRule, verb, group, resource string, resourceName ...string) bool {
	for _, r := range rules {
		if !matchAny(r.Verbs, verb) || !matchAny(r.APIGroups, group) || !matchAny(r.Resources, resource) {
			continue
		}
		if len(r.ResourceNames) > 0 {
			if len(resourceName) == 0 || !matchAny(r.ResourceNames, resourceName[0]) {
				continue
			}
		}
		return true
	}
	return false
}

func matchAny(list []string, v string) bool {
	for _, item := range list {
		if item == "*" || item == v {
			return true
		}
	}
	return false
}
