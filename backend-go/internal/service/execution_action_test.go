package service

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestExecutionActionIsIdempotentAndAppliedResultIsImmutable(t *testing.T) {
	store := NewStore()
	intent := json.RawMessage(`{"previous_image":"reg.io/app:v1","target_image":"reg.io/app:v2"}`)
	action := ExecutionAction{DeployLogID: 7, ActionKey: "set-image", ActionKind: "set_deployment_image", Intent: intent}
	first, created, err := store.BeginExecutionAction(action)
	if err != nil || !created || first.Status != ExecutionActionIntent {
		t.Fatalf("first BeginExecutionAction=%+v created=%v err=%v", first, created, err)
	}
	second, created, err := store.BeginExecutionAction(action)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("idempotent BeginExecutionAction=%+v created=%v err=%v", second, created, err)
	}
	if _, _, err := store.BeginExecutionAction(ExecutionAction{
		DeployLogID: 7, ActionKey: "set-image", ActionKind: "set_deployment_image",
		Intent: json.RawMessage(`{"previous_image":"reg.io/app:v2","target_image":"reg.io/app:v2"}`),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed intent error=%v, want conflict", err)
	}

	observed := json.RawMessage(`{"current_image":"reg.io/app:v2","readback":"completed"}`)
	completed, err := store.CompleteExecutionAction(7, "set-image", ExecutionActionApplied, observed, "target_confirmed")
	if err != nil || completed.Status != ExecutionActionApplied {
		t.Fatalf("CompleteExecutionAction=%+v err=%v", completed, err)
	}
	if _, err := store.CompleteExecutionAction(7, "set-image", ExecutionActionUnknown,
		json.RawMessage(`{"current_image":"reg.io/app:v1"}`), "unexpected"); !errors.Is(err, ErrConflict) {
		t.Fatalf("applied result mutation error=%v, want conflict", err)
	}
	rows, err := store.ExecutionActions(7)
	if err != nil || len(rows) != 1 || rows[0].Status != ExecutionActionApplied {
		t.Fatalf("ExecutionActions=%+v err=%v", rows, err)
	}
}
