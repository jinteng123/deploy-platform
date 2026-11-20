package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func seedInterruptedImageAction(t *testing.T, store *Store, previous, target, status string) int64 {
	t.Helper()
	logRow, err := store.AddLog(DeployLog{
		ClusterID: 1, AppName: "app", DeploymentName: "app-test", ContainerName: "app",
		Namespace: "ns-test", DeployType: "rollback", Status: "interrupted", ImageFull: &target,
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := json.Marshal(imageActionIntent{PreviousImage: previous, TargetImage: target})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginExecutionAction(ExecutionAction{
		DeployLogID: logRow.ID, ActionKey: "set-image", ActionKind: "set_deployment_image", Intent: intent,
	}); err != nil {
		t.Fatal(err)
	}
	if status != ExecutionActionIntent {
		observed, _ := json.Marshal(imageActionObservation{Readback: "failed"})
		if _, err := store.CompleteExecutionAction(logRow.ID, "set-image", status, observed, "readback_failed"); err != nil {
			t.Fatal(err)
		}
	}
	return logRow.ID
}

func TestReconcileInterruptedResumesOnlyWhenTargetIsAlreadyActive(t *testing.T) {
	_, store, _, rec, worker, _ := cdFixture(t)
	previous := "reg.io/Demo/app:v0"
	target := "reg.io/Demo/app@sha256:" + strings.Repeat("ab", 32)
	logID := seedInterruptedImageAction(t, store, previous, target, ExecutionActionUnknown)
	current := target
	rec.forcedImage = &current

	if err := worker.ReconcileAndResume(context.Background(), logID); err != nil {
		t.Fatalf("ReconcileAndResume: %v", err)
	}
	finished := waitLog(t, store, logID)
	if finished.Status != "success" {
		t.Fatalf("status=%s err=%v", finished.Status, finished.ErrorMessage)
	}
	if calls := rec.recorded(); len(calls) != 0 {
		t.Fatalf("resume repeated SetImage: %v", calls)
	}
	actions, err := store.ExecutionActions(logID)
	if err != nil || len(actions) != 1 || actions[0].Status != ExecutionActionApplied || actions[0].OutcomeCode != "operator_reconciled_target" {
		t.Fatalf("action journal=%+v err=%v", actions, err)
	}
}

func TestReconcileInterruptedLeavesOriginalImageWithoutReplay(t *testing.T) {
	_, store, _, rec, worker, _ := cdFixture(t)
	previous := "reg.io/Demo/app:v0"
	target := "reg.io/Demo/app@sha256:" + strings.Repeat("cd", 32)
	logID := seedInterruptedImageAction(t, store, previous, target, ExecutionActionIntent)
	current := previous
	rec.forcedImage = &current

	err := worker.ReconcileAndResume(context.Background(), logID)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ReconcileAndResume error=%v, want conflict when target is absent", err)
	}
	interrupted, findErr := store.FindLog(logID)
	if findErr != nil || interrupted.Status != "interrupted" {
		t.Fatalf("log=%+v find err=%v, want interrupted", interrupted, findErr)
	}
	if calls := rec.recorded(); len(calls) != 0 {
		t.Fatalf("reconciliation repeated SetImage: %v", calls)
	}
	actions, err := store.ExecutionActions(logID)
	if err != nil || len(actions) != 1 || actions[0].Status != ExecutionActionNotApplied || actions[0].OutcomeCode != "operator_reconciled_previous" {
		t.Fatalf("action journal=%+v err=%v", actions, err)
	}
	if _, err := store.AddDeployLog(DeployLog{
		ClusterID: 1, Namespace: "ns-test", DeploymentName: "app-test", Status: "pending",
	}); err != nil {
		t.Fatalf("terminal reconciliation did not release the resource lock: %v", err)
	}
}

func TestReopenInterruptedDeployLogRejectsActiveResourceOwner(t *testing.T) {
	store := NewStoreWithData([]Cluster{{ID: 1}}, nil, nil, nil, nil)
	first, err := store.AddLog(DeployLog{ID: 1, ClusterID: 1, Namespace: "ns", DeploymentName: "app", Status: "interrupted"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddDeployLog(DeployLog{ID: 2, ClusterID: 1, Namespace: "ns", DeploymentName: "app", Status: "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReopenInterruptedDeployLog(first.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopen under active owner %d error=%v, want conflict", second.ID, err)
	}
}

func TestInterruptedLogResumeStateResetsTimingFields(t *testing.T) {
	store := NewStoreWithData([]Cluster{{ID: 1}}, nil, nil, nil, nil)
	finishedAt := time.Now().Add(-time.Minute)
	message := "interrupted"
	logRow, err := store.AddLog(DeployLog{ClusterID: 1, Namespace: "ns", DeploymentName: "app", Status: "interrupted", ErrorMessage: &message, FinishedAt: &finishedAt})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.ReopenInterruptedDeployLog(logRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != "pending" || updated.ErrorMessage != nil || updated.FinishedAt != nil || updated.StartedAt != nil {
		t.Fatalf("reopened log contains stale execution state: %+v", updated)
	}
}
