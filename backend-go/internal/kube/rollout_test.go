package kube

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func i32(v int32) *int32 { return &v }

func deployWith(replicas int32, status appsv1.DeploymentStatus) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web", Namespace: "ns", Generation: 3,
		},
		Spec:   appsv1.DeploymentSpec{Replicas: i32(replicas)},
		Status: status,
	}
}

func TestRolloutConditionComplete(t *testing.T) {
	d := deployWith(2, appsv1.DeploymentStatus{
		ObservedGeneration: 3, Replicas: 2, UpdatedReplicas: 2,
		ReadyReplicas: 2, AvailableReplicas: 2,
	})
	done, summary, err := rolloutCondition(d)
	if !done || err != nil || !strings.Contains(summary, "successfully rolled out") {
		t.Fatalf("done=%v summary=%q err=%v", done, summary, err)
	}
}

func TestRolloutConditionWaiting(t *testing.T) {
	tests := []struct {
		name   string
		status appsv1.DeploymentStatus
		want   string
	}{
		{"stale generation", appsv1.DeploymentStatus{ObservedGeneration: 2}, "spec update"},
		{"replicas not updated", appsv1.DeploymentStatus{ObservedGeneration: 3, UpdatedReplicas: 1}, "1 out of 2"},
		{"old replicas terminating", appsv1.DeploymentStatus{
			ObservedGeneration: 3, Replicas: 3, UpdatedReplicas: 2, ReadyReplicas: 3, AvailableReplicas: 3,
		}, "pending termination"},
		{"not all available", appsv1.DeploymentStatus{
			ObservedGeneration: 3, Replicas: 2, UpdatedReplicas: 2, ReadyReplicas: 1, AvailableReplicas: 1,
		}, "available"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			done, summary, err := rolloutCondition(deployWith(2, tc.status))
			if done || err != nil || !strings.Contains(summary, tc.want) {
				t.Fatalf("done=%v summary=%q err=%v", done, summary, err)
			}
		})
	}
}

func TestRolloutConditionDeadlineExceeded(t *testing.T) {
	d := deployWith(1, appsv1.DeploymentStatus{
		ObservedGeneration: 3, Replicas: 1,
		Conditions: []appsv1.DeploymentCondition{{
			Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded",
			Message: "ReplicaSet exceeded its progression deadline",
		}},
	})
	done, _, err := rolloutCondition(d)
	if done || err == nil || !strings.Contains(err.Error(), "progression deadline") {
		t.Fatalf("done=%v err=%v", done, err)
	}
}

func TestRolloutStatusDone(t *testing.T) {
	replicas := int32(1)
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns", Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1,
			ReadyReplicas: 1, AvailableReplicas: 1,
		},
	}
	c := NewWithClientset(fake.NewSimpleClientset(d), nil, nil)
	summary, err := c.RolloutStatus(context.Background(), "ns", "web")
	if err != nil || !strings.Contains(summary, "successfully rolled out") {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
}

func TestRolloutStatusDeadlineExceeded(t *testing.T) {
	d := deployWith(1, appsv1.DeploymentStatus{
		ObservedGeneration: 1,
		Conditions: []appsv1.DeploymentCondition{{
			Type: appsv1.DeploymentProgressing, Reason: "ProgressDeadlineExceeded",
			Message: "stuck",
		}},
	})
	c := NewWithClientset(fake.NewSimpleClientset(d, &corev1.Pod{}), nil, nil)
	if _, err := c.RolloutStatus(context.Background(), "ns", "web"); err == nil || !strings.Contains(err.Error(), "stuck") {
		t.Fatalf("err=%v", err)
	}
}

func TestRolloutStatusTimeoutKeepsSummary(t *testing.T) {
	old := rolloutPollInterval
	rolloutPollInterval = 10 * time.Millisecond
	defer func() { rolloutPollInterval = old }()

	d := deployWith(2, appsv1.DeploymentStatus{ObservedGeneration: 3, UpdatedReplicas: 1})
	c := NewWithClientset(fake.NewSimpleClientset(d), nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	summary, err := c.RolloutStatus(ctx, "ns", "web")
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(summary, "1 out of 2") {
		t.Fatalf("summary=%q", summary)
	}
}

func TestRolloutStatusCompletesMidway(t *testing.T) {
	old := rolloutPollInterval
	rolloutPollInterval = 5 * time.Millisecond
	defer func() { rolloutPollInterval = old }()

	d := deployWith(1, appsv1.DeploymentStatus{ObservedGeneration: 1, UpdatedReplicas: 0})
	cs := fake.NewSimpleClientset(d)
	c := NewWithClientset(cs, nil, nil)

	go func() {
		time.Sleep(30 * time.Millisecond)
		d.Status.ObservedGeneration = 3
		d.Status.UpdatedReplicas = 1
		d.Status.Replicas = 1
		d.Status.ReadyReplicas = 1
		d.Status.AvailableReplicas = 1
		_, _ = cs.AppsV1().Deployments("ns").UpdateStatus(context.Background(), d, metav1.UpdateOptions{})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	summary, err := c.RolloutStatus(ctx, "ns", "web")
	if err != nil || !strings.Contains(summary, "successfully rolled out") {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
}
