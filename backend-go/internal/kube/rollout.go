package kube

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// rolloutPollInterval is a var so tests can shrink the wait.
var rolloutPollInterval = 2 * time.Second

// RolloutStatus polls the deployment until its rollout completes, fails, or
// ctx ends — the client-side equivalent of `kubectl rollout status`. The
// caller bounds the wait with a ctx timeout; the summary mirrors kubectl's
// output so pipeline step logs read identically on every runtime.
func (c *Client) RolloutStatus(ctx context.Context, namespace, name string) (string, error) {
	var last string
	for {
		d, err := c.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", err
		}
		done, summary, rolloutErr := rolloutCondition(d)
		last = summary
		if done || rolloutErr != nil {
			return summary, rolloutErr
		}
		timer := time.NewTimer(rolloutPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return last, fmt.Errorf("rollout 等待超时或取消: %s", last)
		case <-timer.C:
		}
	}
}

// rolloutCondition applies kubectl's `rollout status` completion rules to a
// deployment snapshot: spec observed → new replicas updated → old replicas
// terminated → updated replicas available. ProgressDeadlineExceeded is a
// terminal failure kubectl reports the same way.
func rolloutCondition(d *appsv1.Deployment) (done bool, summary string, err error) {
	for _, cond := range d.Status.Conditions {
		if cond.Type == appsv1.DeploymentProgressing && cond.Reason == "ProgressDeadlineExceeded" {
			return false, cond.Message, fmt.Errorf("rollout 失败: %s", cond.Message)
		}
	}
	if d.Status.ObservedGeneration < d.Generation {
		return false, "Waiting for deployment spec update to be observed...", nil
	}
	desired := int32(1)
	if d.Spec.Replicas != nil {
		desired = *d.Spec.Replicas
	}
	switch {
	case d.Status.UpdatedReplicas < desired:
		return false, fmt.Sprintf("Waiting for deployment %q rollout to finish: %d out of %d new replicas have been updated...",
			d.Name, d.Status.UpdatedReplicas, desired), nil
	case d.Status.Replicas > d.Status.UpdatedReplicas:
		return false, fmt.Sprintf("Waiting for deployment %q rollout to finish: %d old replicas are pending termination...",
			d.Name, d.Status.Replicas-d.Status.UpdatedReplicas), nil
	case d.Status.AvailableReplicas < d.Status.UpdatedReplicas:
		return false, fmt.Sprintf("Waiting for deployment %q rollout to finish: %d of %d updated replicas are available...",
			d.Name, d.Status.AvailableReplicas, d.Status.UpdatedReplicas), nil
	}
	return true, fmt.Sprintf("deployment %q successfully rolled out", d.Name), nil
}
