package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type lostCommitRunPersistence struct {
	Persistence
	run         DeliveryRun
	event       DeliveryRunEvent
	log         DeployLog
	loseNextAck bool
}

func (p *lostCommitRunPersistence) TransitionDeliveryRunExecution(log DeployLog, expectedVersion int64, event DeliveryRunEvent) (DeliveryRun, DeliveryRunEvent, bool, error) {
	if p.event.EventKey == event.EventKey {
		return p.run, p.event, true, nil
	}
	if p.run.Version != expectedVersion {
		return p.run, DeliveryRunEvent{}, false, ErrConflict
	}
	p.run.Version++
	p.run.Status = event.Status
	p.run.CurrentStage = event.Stage
	event.ID = 2
	event.RunID = p.run.ID
	event.Version = p.run.Version
	event.CreatedAt = time.Now().UTC()
	p.event = event
	p.log = log
	if p.loseNextAck {
		p.loseNextAck = false
		return p.run, p.event, false, errors.New("commit applied; acknowledgement lost")
	}
	return p.run, p.event, false, nil
}

func (p *lostCommitRunPersistence) LogByID(id int64) (DeployLog, error) {
	if p.log.ID != id {
		return DeployLog{}, ErrNotFound
	}
	return p.log, nil
}

func TestDeliveryRunEventsAreVersionedAndIdempotent(t *testing.T) {
	store := NewStore()
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31,
		Strategy: "rolling", Status: "queued", CurrentStage: "queued", Version: 1,
	}}, []DeliveryRunEvent{{
		ID: 1, RunID: 8, Version: 1, EventKey: "run-created", EventType: "queued",
		Stage: "queued", Status: "queued", Detail: "queued",
	}})

	event := DeliveryRunEvent{EventKey: "started", EventType: "started", Stage: "started", Status: "running", Detail: "worker started"}
	run, found, err := store.RecordDeliveryRunEvent(31, 1, event)
	if err != nil || !found || run.Version != 2 || run.CurrentStage != "started" {
		t.Fatalf("first run event run=%+v found=%v err=%v", run, found, err)
	}
	duplicate, found, err := store.RecordDeliveryRunEvent(31, 1, event)
	if err != nil || !found || duplicate.Version != 2 {
		t.Fatalf("duplicate run event run=%+v found=%v err=%v", duplicate, found, err)
	}
	events, err := store.DeliveryRunEvents(8)
	if err != nil || len(events) != 2 || events[1].Version != 2 {
		t.Fatalf("run events=%+v err=%v", events, err)
	}
	if _, _, err := store.RecordDeliveryRunEvent(31, 1, DeliveryRunEvent{
		EventKey: "stale-stage", EventType: "stage", Stage: "queued", Status: "running",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale run version appended a new event: err=%v", err)
	}

	event.Status = "failed"
	if _, _, err := store.RecordDeliveryRunEvent(31, 1, event); !errors.Is(err, ErrConflict) {
		t.Fatalf("reusing event key with different state err=%v, want conflict", err)
	}
}

func TestRecordDeliveryRunEventCannotReopenTerminalRun(t *testing.T) {
	for _, status := range []string{"success", "failed", "interrupted", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			store := NewStore()
			store.LoadDeliveryRuns([]DeliveryRun{{
				ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31,
				Strategy: "blue_green", Status: status, CurrentStage: "restored", Version: 3,
				ErrorMessage: strPtr("原始失败原因"),
			}}, nil)

			if _, _, err := store.RecordDeliveryRunEvent(31, 3, DeliveryRunEvent{
				EventKey: "reopen", EventType: "recovery_required", Stage: "needs_attention", Status: "running",
			}); !errors.Is(err, ErrConflict) {
				t.Fatalf("terminal run status %q was reopened: err=%v", status, err)
			}
			unchanged, err := store.DeliveryRun(8)
			if err != nil || unchanged.Status != status || unchanged.Version != 3 {
				t.Fatalf("terminal run changed after rejected event: run=%+v err=%v", unchanged, err)
			}

			appended, found, err := store.RecordDeliveryRunEvent(31, 3, DeliveryRunEvent{
				EventKey: "cleanup-audit", EventType: "control", Stage: "restored", Status: status,
			})
			if err != nil || !found || appended.Status != status || appended.Version != 4 || appended.ErrorMessage == nil || *appended.ErrorMessage != "原始失败原因" {
				t.Fatalf("same-status terminal audit was rejected: run=%+v found=%t err=%v", appended, found, err)
			}
		})
	}
}

func TestVersionedTransitionCannotReopenTerminalRun(t *testing.T) {
	for _, status := range []string{"success", "failed", "interrupted", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			store := NewStore()
			if _, err := store.AddLog(DeployLog{ID: 31, Status: "running"}); err != nil {
				t.Fatal(err)
			}
			store.LoadDeliveryRuns([]DeliveryRun{{
				ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31,
				Strategy: "blue_green", Status: status, CurrentStage: "restored", Version: 3,
			}}, nil)

			if _, _, err := store.TransitionDeliveryRun(DeployLog{ID: 31, Status: "running"}, 3, DeliveryRunEvent{
				EventKey: "stale-reopen", EventType: "recovery_required", Stage: "needs_attention", Status: "running",
			}); !errors.Is(err, ErrConflict) {
				t.Fatalf("terminal run status %q was reopened: err=%v", status, err)
			}
			unchanged, err := store.DeliveryRun(8)
			if err != nil || unchanged.Status != status || unchanged.Version != 3 {
				t.Fatalf("terminal run changed after rejected transition: run=%+v err=%v", unchanged, err)
			}
		})
	}
}

func TestPipelineCheckpointKeepsTaskAndRunTimelineTogether(t *testing.T) {
	store := NewStore()
	if _, err := store.AddLog(DeployLog{ID: 31, Status: "pending", StepDetail: "[]"}); err != nil {
		t.Fatal(err)
	}
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31,
		Strategy: "rolling", Status: "queued", CurrentStage: "queued", Version: 1,
	}}, []DeliveryRunEvent{{
		ID: 1, RunID: 8, Version: 1, EventKey: "run-created", EventType: "queued",
		Stage: "queued", Status: "queued", Detail: "queued",
	}})

	log, err := store.FindLog(31)
	if err != nil {
		t.Fatal(err)
	}
	log.Status = "running"
	log.CurrentStep = strPtr("rollout")
	found, err := store.UpdateLogExecutionAndRecordDeliveryRunEvent(log, DeliveryRunEvent{
		EventKey: "step:rollout", EventType: "stage", Stage: "rollout", Status: "running", Detail: "等待工作负载就绪",
	})
	if err != nil || !found {
		t.Fatalf("checkpoint found=%v err=%v", found, err)
	}
	updatedLog, _ := store.FindLog(31)
	updatedRun, _ := store.DeliveryRun(8)
	events, _ := store.DeliveryRunEvents(8)
	if updatedLog.Status != "running" || updatedLog.CurrentStep == nil || *updatedLog.CurrentStep != "rollout" || updatedRun.Status != updatedLog.Status || updatedRun.CurrentStage != "rollout" || updatedRun.Version != 2 || len(events) != 2 {
		t.Fatalf("task=%+v run=%+v events=%+v", updatedLog, updatedRun, events)
	}
	if found, err := store.UpdateLogExecutionAndRecordDeliveryRunEvent(log, DeliveryRunEvent{
		EventKey: "step:rollout", EventType: "stage", Stage: "rollout", Status: "running", Detail: "等待工作负载就绪",
	}); err != nil || !found {
		t.Fatalf("idempotent checkpoint found=%v err=%v", found, err)
	}
	duplicateRun, _ := store.DeliveryRun(8)
	duplicateEvents, _ := store.DeliveryRunEvents(8)
	if duplicateRun.Version != 2 || len(duplicateEvents) != 2 {
		t.Fatalf("idempotent checkpoint appended twice: run=%+v events=%+v", duplicateRun, duplicateEvents)
	}

	log.Status = "success"
	if _, err := store.UpdateLogExecutionAndRecordDeliveryRunEvent(log, DeliveryRunEvent{
		EventKey: "step:rollout", EventType: "stage", Stage: "rollout", Status: "success", Detail: "should conflict",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("event key conflict err=%v, want conflict", err)
	}
	stillRunning, _ := store.FindLog(31)
	if stillRunning.Status != "running" {
		t.Fatalf("conflicting event changed task status to %s", stillRunning.Status)
	}
}

func TestStartupRecoveryMarksLinkedRunInterrupted(t *testing.T) {
	store := NewStore()
	started := time.Now().Add(-time.Minute)
	step := "rollout_wait"
	if _, err := store.AddLog(DeployLog{ID: 31, Status: "running", CurrentStep: &step, StartedAt: &started, StepDetail: "[]"}); err != nil {
		t.Fatal(err)
	}
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31,
		Strategy: "rolling", Status: "running", CurrentStage: step, Version: 2,
	}}, []DeliveryRunEvent{{
		ID: 1, RunID: 8, Version: 1, EventKey: "run-created", EventType: "queued", Stage: "queued", Status: "queued",
	}, {
		ID: 2, RunID: 8, Version: 2, EventKey: "attempt:1:rollout", EventType: "stage", Stage: step, Status: "running",
	}})

	if err := RecoverDeploys(context.Background(), store, nil); err != nil {
		t.Fatal(err)
	}
	log, _ := store.FindLog(31)
	run, _ := store.DeliveryRun(8)
	events, _ := store.DeliveryRunEvents(8)
	if log.Status != "interrupted" || run.Status != "interrupted" || run.CurrentStage != step || run.Version != 3 || len(events) != 3 || events[2].Status != "interrupted" {
		t.Fatalf("recovered task=%+v run=%+v events=%+v", log, run, events)
	}
}

func TestStartupRecoveryPreservesBlueGreenOperatorBarrierAndFlagsMidWrite(t *testing.T) {
	t.Run("already waiting for operator", func(t *testing.T) {
		step := "awaiting_candidate_review"
		store := NewStoreWithData([]Cluster{{ID: 1, Name: "production", EnvironmentTier: "production", DeployPolicy: "manual", Enabled: true}}, nil, []DeployLog{{ID: 31, ClusterID: 1, Namespace: "prod", DeploymentName: "orders-api", Status: "running", CurrentStep: &step}}, nil, nil)
		store.LoadDeliveryRuns([]DeliveryRun{{ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31, Strategy: "blue_green", Status: "running", CurrentStage: step, Version: 3}}, []DeliveryRunEvent{{ID: 1, RunID: 8, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued"}})
		store.mu.Lock()
		store.resourceLocks[(ResourceLock{ClusterID: 1, Namespace: "prod", Kind: "service", Name: "orders-entry"}).Identity()] = 31
		store.mu.Unlock()
		if err := RecoverDeploys(context.Background(), store, nil); err != nil {
			t.Fatal(err)
		}
		log, _ := store.FindLog(31)
		run, _ := store.DeliveryRun(8)
		events, _ := store.DeliveryRunEvents(8)
		if log.Status != "running" || run.CurrentStage != step || run.Version != 3 || len(events) != 1 {
			t.Fatalf("operator barrier was replayed or changed: log=%+v run=%+v events=%+v", log, run, events)
		}
		if err := store.CheckManagedWrite(1, "services", "prod", "orders-entry"); !errors.Is(err, ErrConflict) {
			t.Fatalf("operator barrier released its Service lock: %v", err)
		}
	})

	t.Run("operator barrier with unresolved external write", func(t *testing.T) {
		step := "awaiting_candidate_review"
		store := NewStoreWithData([]Cluster{{ID: 1, Name: "production", EnvironmentTier: "production", DeployPolicy: "manual", Enabled: true}}, nil, []DeployLog{{ID: 33, ClusterID: 1, Namespace: "prod", DeploymentName: "orders-api", Status: "running", CurrentStep: &step}}, nil, nil)
		store.LoadDeliveryRuns([]DeliveryRun{{ID: 10, DeliveryPlanID: 14, PromotionRequestID: 6, DeployLogID: 33, Strategy: "blue_green", Status: "running", CurrentStage: step, Version: 5}}, []DeliveryRunEvent{{ID: 1, RunID: 10, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued"}})
		if _, _, err := store.BeginExecutionAction(ExecutionAction{
			DeployLogID: 33, ActionKey: "blue-green:entry:candidate", ActionKind: "set_service_track_selector",
			Intent: json.RawMessage(`{"name":"orders-entry"}`),
		}); err != nil {
			t.Fatal(err)
		}
		store.mu.Lock()
		store.resourceLocks[(ResourceLock{ClusterID: 1, Namespace: "prod", Kind: "service", Name: "orders-entry"}).Identity()] = 33
		store.mu.Unlock()
		if err := RecoverDeploys(context.Background(), store, nil); err != nil {
			t.Fatal(err)
		}
		log, _ := store.FindLog(33)
		run, _ := store.DeliveryRun(10)
		events, _ := store.DeliveryRunEvents(10)
		actions, _ := store.ExecutionActions(33)
		if log.Status != "running" || run.Status != "running" || run.CurrentStage != "needs_attention" || run.Version != 6 || len(events) != 2 || len(actions) != 1 || actions[0].Status != ExecutionActionIntent {
			t.Fatalf("in-flight external write at an operator barrier was not flagged: log=%+v run=%+v events=%+v actions=%+v", log, run, events, actions)
		}
		if err := store.CheckManagedWrite(1, "services", "prod", "orders-entry"); !errors.Is(err, ErrConflict) {
			t.Fatalf("unresolved operator action released its Service lock: %v", err)
		}
	})

	t.Run("paused at an operator barrier", func(t *testing.T) {
		step := "awaiting_live_verification"
		store := NewStoreWithData([]Cluster{{ID: 1, Name: "production", EnvironmentTier: "production", DeployPolicy: "manual", Enabled: true}}, nil, []DeployLog{{ID: 32, ClusterID: 1, Namespace: "prod", DeploymentName: "orders-api", Status: "running", CurrentStep: &step}}, nil, nil)
		store.LoadDeliveryRuns([]DeliveryRun{{ID: 9, DeliveryPlanID: 13, PromotionRequestID: 5, DeployLogID: 32, Strategy: "blue_green", Status: "paused", CurrentStage: step, Version: 4}}, []DeliveryRunEvent{{ID: 1, RunID: 9, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued"}})
		store.mu.Lock()
		store.resourceLocks[(ResourceLock{ClusterID: 1, Namespace: "prod", Kind: "service", Name: "orders-entry"}).Identity()] = 32
		store.mu.Unlock()
		if err := RecoverDeploys(context.Background(), store, nil); err != nil {
			t.Fatal(err)
		}
		log, _ := store.FindLog(32)
		run, _ := store.DeliveryRun(9)
		events, _ := store.DeliveryRunEvents(9)
		if log.Status != "running" || run.Status != "paused" || run.CurrentStage != step || run.Version != 4 || len(events) != 1 {
			t.Fatalf("paused operator barrier was replayed or changed: log=%+v run=%+v events=%+v", log, run, events)
		}
		if err := store.CheckManagedWrite(1, "services", "prod", "orders-entry"); !errors.Is(err, ErrConflict) {
			t.Fatalf("paused operator barrier released its Service lock: %v", err)
		}
	})

	t.Run("restart during an external write", func(t *testing.T) {
		step := "switching_to_candidate"
		store := NewStoreWithData([]Cluster{{ID: 1, Name: "production", EnvironmentTier: "production", DeployPolicy: "manual", Enabled: true}}, nil, []DeployLog{{ID: 31, ClusterID: 1, Namespace: "prod", DeploymentName: "orders-api", Status: "running", CurrentStep: &step}}, nil, nil)
		store.LoadDeliveryRuns([]DeliveryRun{{ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31, Strategy: "blue_green", Status: "running", CurrentStage: step, Version: 2}}, []DeliveryRunEvent{{ID: 1, RunID: 8, Version: 1, EventKey: "created", EventType: "queued", Stage: "queued", Status: "queued"}})
		store.mu.Lock()
		store.resourceLocks[(ResourceLock{ClusterID: 1, Namespace: "prod", Kind: "service", Name: "orders-entry"}).Identity()] = 31
		store.mu.Unlock()
		if err := RecoverDeploys(context.Background(), store, nil); err != nil {
			t.Fatal(err)
		}
		log, _ := store.FindLog(31)
		run, _ := store.DeliveryRun(8)
		events, _ := store.DeliveryRunEvents(8)
		if log.Status != "running" || run.Status != "running" || run.CurrentStage != "needs_attention" || run.Version != 3 || len(events) != 2 {
			t.Fatalf("ambiguous blue-green operation was not preserved for reconciliation: log=%+v run=%+v events=%+v", log, run, events)
		}
		if err := store.CheckManagedWrite(1, "services", "prod", "orders-entry"); !errors.Is(err, ErrConflict) {
			t.Fatalf("needs_attention released its Service lock: %v", err)
		}
		log.CurrentStep = strPtr("awaiting_candidate_review")
		if _, _, err := store.TransitionDeliveryRun(log, run.Version, DeliveryRunEvent{
			EventKey: "operator:reconciled-partial-write", EventType: "control",
			Stage: "awaiting_candidate_review", Status: "running", Detail: "候选准备已续接，等待切流确认。",
		}); err != nil {
			t.Fatalf("simulate operator reconcile before a later restart: %v", err)
		}
		if _, _, err := store.BeginExecutionAction(ExecutionAction{
			DeployLogID: 31, ActionKey: "blue-green:entry:candidate", ActionKind: "set_service_track_selector",
			Intent: json.RawMessage(`{"name":"orders-entry"}`),
		}); err != nil {
			t.Fatalf("simulate a new cutover write intent before a later restart: %v", err)
		}
		if err := RecoverDeploys(context.Background(), store, nil); err != nil {
			t.Fatalf("recover a second interrupted operation in the same run: %v", err)
		}
		run, _ = store.DeliveryRun(8)
		events, _ = store.DeliveryRunEvents(8)
		if run.CurrentStage != "needs_attention" || run.Version != 5 || len(events) != 4 || events[1].EventKey == events[3].EventKey {
			t.Fatalf("repeated recovery reused an earlier event identity: run=%+v events=%+v", run, events)
		}
	})
}

func TestVersionedDeliveryRunTransitionIsIdempotentAndReleasesLocksOnTerminal(t *testing.T) {
	step := "awaiting_candidate_review"
	log := DeployLog{ID: 31, ClusterID: 1, Namespace: "prod", DeploymentName: "orders-api", Status: "running", CurrentStep: &step, StepDetail: "[]"}
	store := NewStoreWithData([]Cluster{{ID: 1, Name: "production", EnvironmentTier: "production", DeployPolicy: "manual", Enabled: true}}, nil, []DeployLog{log}, nil, nil)
	store.LoadDeliveryRuns([]DeliveryRun{{
		ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31,
		Strategy: "blue_green", Status: "running", CurrentStage: step, Version: 3,
	}}, []DeliveryRunEvent{{
		ID: 1, RunID: 8, Version: 1, EventKey: "run-created", EventType: "queued", Stage: "queued", Status: "queued",
	}})
	serviceLock := ResourceLock{ClusterID: 1, Namespace: "prod", Kind: "service", Name: "orders-entry"}
	store.mu.Lock()
	store.resourceLocks[serviceLock.Identity()] = log.ID
	store.mu.Unlock()
	if err := store.CheckManagedWrite(1, "services", "prod", "orders-entry"); !errors.Is(err, ErrConflict) {
		t.Fatalf("active blue-green run should own its Service: %v", err)
	}

	event := DeliveryRunEvent{EventKey: "control:cutover-1", EventType: "stage", Stage: "awaiting_live_verification", Status: "running", Detail: "Service 已切至候选，等待人工验证。"}
	log.CurrentStep = &event.Stage
	run, found, err := store.TransitionDeliveryRun(log, 3, event)
	if err != nil || !found || run.Version != 4 || run.CurrentStage != event.Stage {
		t.Fatalf("first versioned transition run=%+v found=%v err=%v", run, found, err)
	}
	duplicate, found, err := store.TransitionDeliveryRun(log, 3, event)
	if err != nil || !found || duplicate.Version != 4 {
		t.Fatalf("idempotent transition run=%+v found=%v err=%v", duplicate, found, err)
	}
	staleLog := log
	staleEvent := DeliveryRunEvent{EventKey: "control:stale", EventType: "stage", Stage: "needs_attention", Status: "running", Detail: "stale request"}
	if _, _, err := store.TransitionDeliveryRun(staleLog, 3, staleEvent); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version transition error=%v, want conflict", err)
	}
	conflicting := event
	conflicting.Detail = "different payload"
	if _, _, err := store.TransitionDeliveryRun(log, 3, conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("idempotency key payload conflict=%v, want conflict", err)
	}

	terminalLog, _ := store.FindLog(log.ID)
	terminalLog.Status = "success"
	terminalLog.CurrentStep = strPtr("success")
	terminalEvent := DeliveryRunEvent{EventKey: "control:finalize", EventType: "completed", Stage: "success", Status: "success", Detail: "业务验证通过。"}
	terminal, _, err := store.TransitionDeliveryRun(terminalLog, 4, terminalEvent)
	if err != nil || terminal.Status != "success" || terminal.Version != 5 {
		t.Fatalf("terminal transition run=%+v err=%v", terminal, err)
	}
	if err := store.CheckManagedWrite(1, "services", "prod", "orders-entry"); err != nil {
		t.Fatalf("terminal transition did not release Service lock: %v", err)
	}
	updatedLog, _ := store.FindLog(log.ID)
	if updatedLog.Status != "success" {
		t.Fatalf("deploy log and run diverged: %+v", updatedLog)
	}
}

func TestVersionedDeliveryRunTransitionRefreshesCacheAfterLostCommitAck(t *testing.T) {
	step := "awaiting_live_verification"
	log := DeployLog{ID: 31, ClusterID: 1, Namespace: "prod", DeploymentName: "orders-api", Status: "running", CurrentStep: &step, StepDetail: "[]"}
	persistence := &lostCommitRunPersistence{
		run:         DeliveryRun{ID: 8, DeliveryPlanID: 12, PromotionRequestID: 4, DeployLogID: 31, Strategy: "blue_green", Status: "running", CurrentStage: step, Version: 3},
		loseNextAck: true,
	}
	store := NewStoreWithData([]Cluster{{ID: 1, Name: "production", EnvironmentTier: "production", DeployPolicy: "manual", Enabled: true}}, nil, []DeployLog{log}, nil, persistence)
	store.LoadDeliveryRuns([]DeliveryRun{persistence.run}, []DeliveryRunEvent{{
		ID: 1, RunID: 8, Version: 1, EventKey: "run-created", EventType: "queued", Stage: "queued", Status: "queued",
	}})
	serviceLock := ResourceLock{ClusterID: 1, Namespace: "prod", Kind: "service", Name: "orders-entry"}
	store.mu.Lock()
	store.resourceLocks[serviceLock.Identity()] = log.ID
	store.mu.Unlock()

	event := DeliveryRunEvent{EventKey: "control:confirm-live-001", EventType: "completed", Stage: "success", Status: "success", Detail: "业务验证通过。"}
	terminalLog := log
	terminalLog.Status = "success"
	terminalLog.CurrentStep = strPtr("success")
	terminalLog.FinishedAt = ptrTime(time.Now().UTC())
	if _, _, err := store.TransitionDeliveryRun(terminalLog, 3, event); err == nil || err.Error() != "commit applied; acknowledgement lost" {
		t.Fatalf("first transition error=%v, want lost commit acknowledgement", err)
	}
	if run, _ := store.DeliveryRun(8); run.Status != "running" {
		t.Fatalf("cache should remain unchanged until idempotent recovery, got %+v", run)
	}
	if err := store.CheckManagedWrite(1, "services", "prod", "orders-entry"); !errors.Is(err, ErrConflict) {
		t.Fatalf("lock should remain while commit result is unknown, got %v", err)
	}

	run, duplicate, err := store.TransitionDeliveryRun(terminalLog, 3, event)
	if err != nil || !duplicate || run.Status != "success" || run.Version != 4 {
		t.Fatalf("idempotent recovery run=%+v duplicate=%v err=%v", run, duplicate, err)
	}
	cachedLog, err := store.FindLog(log.ID)
	if err != nil || cachedLog.Status != "success" || cachedLog.CurrentStep == nil || *cachedLog.CurrentStep != "success" {
		t.Fatalf("recovered task cache=%+v err=%v", cachedLog, err)
	}
	events, err := store.DeliveryRunEvents(run.ID)
	if err != nil || len(events) != 2 || events[1].EventKey != event.EventKey {
		t.Fatalf("recovered timeline=%+v err=%v", events, err)
	}
	if err := store.CheckManagedWrite(1, "services", "prod", "orders-entry"); err != nil {
		t.Fatalf("committed terminal transition must release its cached lock: %v", err)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestSuccessfulWorkloadPromotionUpdatesAppBindingAndRunTogether(t *testing.T) {
	oldRef := DeliveryWorkloadReference{ClusterID: 1, Namespace: "prod", Kind: "Deployment", Name: "api", UID: "uid-stable", ResourceVersion: "12"}
	newRef := DeliveryWorkloadReference{ClusterID: 1, Namespace: "prod", Kind: "Deployment", Name: "api-plan-7", UID: "uid-candidate", ResourceVersion: "19"}
	newStore := func() *Store {
		step := "awaiting_live_verification"
		log := DeployLog{ID: 31, ClusterID: 1, AppName: "api", Namespace: "prod", DeploymentName: "api", Status: "running", CurrentStep: &step}
		store := NewStoreWithData(
			[]Cluster{{ID: 1, Name: "production", EnvironmentTier: "production", DeployPolicy: "manual", Enabled: true}},
			[]App{{ID: 2, ClusterID: 1, AppName: "api", Namespace: "prod", DeploymentName: "api", ContainerName: "api"}},
			[]DeployLog{log}, nil, nil,
		)
		store.LoadDeliveryModel(DeliveryModel{Bindings: []DeliveryBinding{{ID: 9, AppID: 2, ActiveWorkloadRef: oldRef, TemplateWorkloadRef: oldRef}}})
		store.LoadDeliveryRuns([]DeliveryRun{{ID: 8, DeliveryPlanID: 7, DeployLogID: 31, Strategy: "blue_green", Status: "running", CurrentStage: step, Version: 3}}, []DeliveryRunEvent{{ID: 1, RunID: 8, Version: 1, EventKey: "run-created", EventType: "queued", Stage: "queued", Status: "queued"}})
		return store
	}
	promotion := DeliveryBindingWorkloadPromotion{AppID: 2, ExpectedDeployment: "api", ExpectedActiveWorkloadRef: oldRef, ActiveWorkloadRef: newRef}

	t.Run("successful promotion advances the active identity", func(t *testing.T) {
		store := newStore()
		log, _ := store.FindLog(31)
		log.Status = "success"
		log.CurrentStep = strPtr("success")
		event := DeliveryRunEvent{EventKey: "control:live-verified", EventType: "control", Stage: "success", Status: "success", Detail: "confirmed"}
		run, _, err := store.TransitionDeliveryRunWithWorkloadPromotion(log, 3, event, promotion)
		if err != nil || run.Status != "success" {
			t.Fatalf("promotion transition run=%+v error=%v", run, err)
		}
		app, err := store.FindAppByName(1, "api")
		if err != nil || app.DeploymentName != newRef.Name {
			t.Fatalf("app target=%+v error=%v", app, err)
		}
		binding, err := store.DeliveryBindingForApp(2)
		if err != nil || binding.ActiveWorkloadRef != newRef || binding.TemplateWorkloadRef != oldRef {
			t.Fatalf("binding refs=%+v error=%v", binding, err)
		}
	})

	t.Run("stale active UID prevents both writes", func(t *testing.T) {
		store := newStore()
		store.mu.Lock()
		store.delivery.Bindings[0].ActiveWorkloadRef.UID = "uid-replaced"
		store.mu.Unlock()
		log, _ := store.FindLog(31)
		log.Status = "success"
		log.CurrentStep = strPtr("success")
		event := DeliveryRunEvent{EventKey: "control:stale-live-verified", EventType: "control", Stage: "success", Status: "success", Detail: "confirmed"}
		if _, _, err := store.TransitionDeliveryRunWithWorkloadPromotion(log, 3, event, promotion); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale identity error=%v, want conflict", err)
		}
		run, _ := store.DeliveryRun(8)
		app, _ := store.FindAppByName(1, "api")
		if run.Status != "running" || app.DeploymentName != "api" {
			t.Fatalf("stale promotion partially changed state: run=%+v app=%+v", run, app)
		}
	})
}
