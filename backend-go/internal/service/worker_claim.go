package service

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// WorkerClaimPersistence provides a durable, unique identity for one attempt
// to execute a task. A claim ID is also the fencing token for subsequent
// execution-state and external-action journal writes.
type WorkerClaimPersistence interface {
	ClaimLogWithOwnership(id int64, claimID string) (bool, error)
	ReleaseLogOwnership(id int64, claimID string) error
}

// PendingDeployLogScanner reads a bounded, ordered page of pending work from
// durable storage. It keeps the scheduler from scanning the entire deployment
// history on every poll; the in-memory Store still supplies the execution
// snapshot and remains single-process state.
type PendingDeployLogScanner interface {
	PendingDeployLogIDsAfter(afterID int64, limit int) ([]int64, error)
}

type FencedLogExecutionPersistence interface {
	UpdateLogExecutionWithClaim(DeployLog, string) error
}

// FencedExecutionActionPersistence checks that a task's current claim still
// matches before persisting an external write intent or its readback.
type FencedExecutionActionPersistence interface {
	BeginExecutionActionWithClaim(ExecutionAction, string) (ExecutionAction, bool, error)
	CompleteExecutionActionWithClaim(int64, string, string, json.RawMessage, string, string) (ExecutionAction, error)
}

// FencedDeliveryRunExecutionPersistence commits worker checkpoints only while
// the task row still carries the worker's claim ID.
type FencedDeliveryRunExecutionPersistence interface {
	UpdateDeployLogAndAppendDeliveryRunEventWithClaim(DeployLog, DeliveryRunEvent, string) (DeliveryRun, DeliveryRunEvent, error)
}

type FencedVersionedDeliveryRunExecutionPersistence interface {
	TransitionDeliveryRunExecutionWithClaim(DeployLog, int64, DeliveryRunEvent, string) (DeliveryRun, DeliveryRunEvent, bool, error)
	TransitionDeliveryRunExecutionWithWorkloadPromotionAndClaim(DeployLog, int64, DeliveryRunEvent, DeliveryBindingWorkloadPromotion, string) (DeliveryRun, DeliveryRunEvent, bool, error)
}

// newWorkerClaimID returns an opaque per-attempt identifier. It is deliberately
// not a process ID: retries of one claim retain the same ID, while each fresh
// attempt gets a new fencing token.
func newWorkerClaimID() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成 Worker 任务认领标识失败: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
