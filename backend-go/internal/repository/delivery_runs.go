package repository

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/service"
)

const deliveryRunColumns = `id,delivery_plan_id,promotion_request_id,deploy_log_id,strategy,status,current_stage,version,error_message,created_at,updated_at`
const deliveryRunEventColumns = `id,run_id,run_version,event_key,event_type,stage,status,detail,payload_json,created_at`

func sameWorkloadReferenceIdentity(a, b service.DeliveryWorkloadReference) bool {
	return a.ClusterID == b.ClusterID && a.Namespace == b.Namespace && a.Kind == b.Kind && a.Name == b.Name && a.UID == b.UID
}

func scanDeliveryRun(row rowScanner) (service.DeliveryRun, error) {
	var run service.DeliveryRun
	var message sql.NullString
	if err := row.Scan(&run.ID, &run.DeliveryPlanID, &run.PromotionRequestID, &run.DeployLogID, &run.Strategy,
		&run.Status, &run.CurrentStage, &run.Version, &message, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return run, err
	}
	if message.Valid {
		run.ErrorMessage = &message.String
	}
	return run, nil
}

func scanDeliveryRunEvent(row rowScanner) (service.DeliveryRunEvent, error) {
	var event service.DeliveryRunEvent
	var detail, payload sql.NullString
	if err := row.Scan(&event.ID, &event.RunID, &event.Version, &event.EventKey, &event.EventType, &event.Stage, &event.Status, &detail, &payload, &event.CreatedAt); err != nil {
		return event, err
	}
	if detail.Valid {
		event.Detail = detail.String
	}
	if payload.Valid {
		event.Payload = []byte(payload.String)
	}
	return event, nil
}

func (m *MySQL) DeliveryRuns() ([]service.DeliveryRun, []service.DeliveryRunEvent, error) {
	rows, err := m.db.Query(`SELECT ` + deliveryRunColumns + ` FROM delivery_runs ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	runs := []service.DeliveryRun{}
	for rows.Next() {
		run, err := scanDeliveryRun(rows)
		if err != nil {
			rows.Close()
			return nil, nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	rows.Close()

	eventsRows, err := m.db.Query(`SELECT ` + deliveryRunEventColumns + ` FROM delivery_run_events ORDER BY run_id,id`)
	if err != nil {
		return nil, nil, err
	}
	events := []service.DeliveryRunEvent{}
	defer eventsRows.Close()
	for eventsRows.Next() {
		event, err := scanDeliveryRunEvent(eventsRows)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, event)
	}
	return runs, events, eventsRows.Err()
}

func insertDeliveryRun(tx *sql.Tx, run *service.DeliveryRun) error {
	result, err := tx.Exec(`INSERT INTO delivery_runs(delivery_plan_id,promotion_request_id,deploy_log_id,strategy,status,current_stage,version,error_message,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, run.DeliveryPlanID, run.PromotionRequestID, run.DeployLogID, run.Strategy,
		run.Status, run.CurrentStage, run.Version, run.ErrorMessage, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return wrapConflict(err)
	}
	if run.ID, err = result.LastInsertId(); err != nil {
		return err
	}
	return nil
}

func insertDeliveryRunEvent(tx *sql.Tx, event service.DeliveryRunEvent) error {
	var payload any
	if len(event.Payload) > 0 {
		payload = []byte(event.Payload)
	}
	_, err := tx.Exec(`INSERT INTO delivery_run_events(run_id,run_version,event_key,event_type,stage,status,detail,payload_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, event.RunID, event.Version, event.EventKey, event.EventType, event.Stage, event.Status, event.Detail, payload, event.CreatedAt)
	return wrapConflict(err)
}

func (m *MySQL) AppendDeliveryRunEvent(runID, expectedVersion int64, event service.DeliveryRunEvent) (service.DeliveryRun, service.DeliveryRunEvent, error) {
	if runID <= 0 || expectedVersion <= 0 {
		return service.DeliveryRun{}, event, service.NewValidationError("发布运行事件缺少有效运行版本")
	}
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryRun{}, event, err
	}
	defer tx.Rollback()
	run, err := scanDeliveryRun(tx.QueryRow(`SELECT `+deliveryRunColumns+` FROM delivery_runs WHERE id=? FOR UPDATE`, runID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.DeliveryRun{}, event, service.ErrNotFound
		}
		return service.DeliveryRun{}, event, err
	}
	existing, err := scanDeliveryRunEvent(tx.QueryRow(`SELECT `+deliveryRunEventColumns+` FROM delivery_run_events WHERE run_id=? AND event_key=?`, runID, event.EventKey))
	if err == nil {
		if existing.EventType != event.EventType || existing.Stage != event.Stage || existing.Status != event.Status || existing.Detail != event.Detail || !bytes.Equal(existing.Payload, event.Payload) {
			return run, existing, fmt.Errorf("%w: 发布运行事件 key 已用于不同状态", service.ErrConflict)
		}
		if err := tx.Commit(); err != nil {
			return run, existing, err
		}
		return run, existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return run, event, err
	}
	if run.Version != expectedVersion {
		return run, event, fmt.Errorf("%w: 发布运行版本已从 %d 更新到 %d", service.ErrConflict, expectedVersion, run.Version)
	}
	if (run.Status == "success" || run.Status == "failed" || run.Status == "interrupted" || run.Status == "cancelled") && event.Status != run.Status {
		return run, event, fmt.Errorf("%w: 已结束的发布运行不能从 %s 改回 %s", service.ErrConflict, run.Status, event.Status)
	}
	newVersion := run.Version + 1
	now := time.Now().UTC()
	message := run.ErrorMessage
	if run.Status != event.Status {
		message = nil
		if event.Status == "failed" || event.Status == "interrupted" {
			message = &event.Detail
		}
	}
	if _, err := tx.Exec(`UPDATE delivery_runs SET status=?,current_stage=?,version=?,error_message=?,updated_at=? WHERE id=?`,
		event.Status, event.Stage, newVersion, message, now, runID); err != nil {
		return run, event, wrapConflict(err)
	}
	event.RunID = runID
	event.Version = newVersion
	event.CreatedAt = now
	if err := insertDeliveryRunEvent(tx, event); err != nil {
		return run, event, err
	}
	run, err = scanDeliveryRun(tx.QueryRow(`SELECT `+deliveryRunColumns+` FROM delivery_runs WHERE id=?`, runID))
	if err != nil {
		return run, event, err
	}
	event, err = scanDeliveryRunEvent(tx.QueryRow(`SELECT `+deliveryRunEventColumns+` FROM delivery_run_events WHERE run_id=? AND event_key=?`, runID, event.EventKey))
	if err != nil {
		return run, event, err
	}
	if err := tx.Commit(); err != nil {
		return run, event, err
	}
	return run, event, nil
}

// UpdateDeployLogAndAppendDeliveryRunEvent commits both views of one pipeline
// checkpoint in one transaction. It is used only for logs with a delivery run;
// a missing run indicates database/cache drift and rolls the update back.
func (m *MySQL) UpdateDeployLogAndAppendDeliveryRunEvent(log service.DeployLog, event service.DeliveryRunEvent) (service.DeliveryRun, service.DeliveryRunEvent, error) {
	return m.updateDeployLogAndAppendDeliveryRunEvent(log, event, "")
}

func (m *MySQL) UpdateDeployLogAndAppendDeliveryRunEventWithClaim(log service.DeployLog, event service.DeliveryRunEvent, claimID string) (service.DeliveryRun, service.DeliveryRunEvent, error) {
	if claimID == "" {
		return service.DeliveryRun{}, event, fmt.Errorf("worker claim is required")
	}
	return m.updateDeployLogAndAppendDeliveryRunEvent(log, event, claimID)
}

func (m *MySQL) updateDeployLogAndAppendDeliveryRunEvent(log service.DeployLog, event service.DeliveryRunEvent, claimID string) (service.DeliveryRun, service.DeliveryRunEvent, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryRun{}, event, err
	}
	defer tx.Rollback()
	run, err := scanDeliveryRun(tx.QueryRow(`SELECT `+deliveryRunColumns+` FROM delivery_runs WHERE deploy_log_id=? FOR UPDATE`, log.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryRun{}, event, fmt.Errorf("%w: 发布运行记录与部署任务关联已漂移", service.ErrConflict)
	}
	if err != nil {
		return service.DeliveryRun{}, event, err
	}
	if claimID != "" {
		var currentClaim sql.NullString
		if err := tx.QueryRow(`SELECT worker_claim_id FROM deploy_logs WHERE id=? FOR UPDATE`, log.ID).Scan(&currentClaim); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return run, event, service.ErrNotFound
			}
			return run, event, err
		}
		if !currentClaim.Valid || currentClaim.String != claimID {
			return run, event, fmt.Errorf("%w: Worker 认领已失效，拒绝写入发布阶段", service.ErrConflict)
		}
	}

	event.RunID = run.ID
	existing, err := scanDeliveryRunEvent(tx.QueryRow(`SELECT `+deliveryRunEventColumns+` FROM delivery_run_events WHERE run_id=? AND event_key=?`, run.ID, event.EventKey))
	if err == nil {
		if existing.EventType != event.EventType || existing.Stage != event.Stage || existing.Status != event.Status || existing.Detail != event.Detail || !bytes.Equal(existing.Payload, event.Payload) {
			return run, existing, fmt.Errorf("%w: 发布运行事件 key 已用于不同状态", service.ErrConflict)
		}
		if err := tx.Commit(); err != nil {
			return run, existing, err
		}
		return run, existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return run, event, err
	}
	// Serialize worker checkpoints with operator controls. A checkpoint that
	// wins the row lock first advances the version and makes a stale control
	// request fail; a pause/cancel that wins first prevents this worker from
	// overwriting the operator's state.
	if service.DeliveryRunPipelineStopped(run.Status) {
		return run, event, service.ErrDeliveryRunStopped
	}
	if claimID != "" {
		result, err := tx.Exec(`UPDATE deploy_logs SET status=?,current_step=?,image_full=?,step_detail=?,error_message=?,started_at=?,finished_at=?,worker_claim_id=CASE WHEN ? IN ('success','failed','interrupted') THEN NULL ELSE worker_claim_id END WHERE id=? AND worker_claim_id=?`,
			log.Status, log.CurrentStep, log.ImageFull, emptyToNil(log.StepDetail), log.ErrorMessage, log.StartedAt, log.FinishedAt, log.Status, log.ID, claimID)
		if err != nil {
			return run, event, wrapConflict(err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return run, event, err
		}
		if changed == 0 && log.Status != "success" && log.Status != "failed" && log.Status != "interrupted" {
			var currentClaim sql.NullString
			if err := tx.QueryRow(`SELECT worker_claim_id FROM deploy_logs WHERE id=?`, log.ID).Scan(&currentClaim); err != nil || !currentClaim.Valid || currentClaim.String != claimID {
				return run, event, fmt.Errorf("%w: Worker 认领已失效，拒绝写入发布阶段", service.ErrConflict)
			}
		}
	} else {
		query := `UPDATE deploy_logs SET status=?,current_step=?,image_full=?,step_detail=?,error_message=?,started_at=?,finished_at=? WHERE id=?`
		if log.Status == "success" || log.Status == "failed" || log.Status == "interrupted" || event.Stage == "needs_attention" {
			query = `UPDATE deploy_logs SET status=?,current_step=?,image_full=?,step_detail=?,error_message=?,started_at=?,finished_at=?,worker_claim_id=NULL WHERE id=?`
		}
		if _, err := tx.Exec(query, log.Status, log.CurrentStep, log.ImageFull, emptyToNil(log.StepDetail), log.ErrorMessage, log.StartedAt, log.FinishedAt, log.ID); err != nil {
			return run, event, wrapConflict(err)
		}
	}
	if log.Status == "success" || log.Status == "failed" || log.Status == "interrupted" {
		if _, err := tx.Exec(`DELETE FROM delivery_resource_locks WHERE deploy_log_id=?`, log.ID); err != nil {
			return run, event, err
		}
	}
	newVersion := run.Version + 1
	now := time.Now().UTC()
	var message any
	if event.Status == "failed" || event.Status == "interrupted" {
		message = event.Detail
	}
	if _, err := tx.Exec(`UPDATE delivery_runs SET status=?,current_stage=?,version=?,error_message=?,updated_at=? WHERE id=?`,
		event.Status, event.Stage, newVersion, message, now, run.ID); err != nil {
		return run, event, wrapConflict(err)
	}
	event.Version = newVersion
	event.CreatedAt = now
	if err := insertDeliveryRunEvent(tx, event); err != nil {
		return run, event, err
	}
	run, err = scanDeliveryRun(tx.QueryRow(`SELECT `+deliveryRunColumns+` FROM delivery_runs WHERE id=?`, run.ID))
	if err != nil {
		return run, event, err
	}
	event, err = scanDeliveryRunEvent(tx.QueryRow(`SELECT `+deliveryRunEventColumns+` FROM delivery_run_events WHERE run_id=? AND event_key=?`, run.ID, event.EventKey))
	if err != nil {
		return run, event, err
	}
	if err := tx.Commit(); err != nil {
		return run, event, err
	}
	return run, event, nil
}

// TransitionDeliveryRunExecution performs an optimistic, idempotent operator
// transition while keeping deploy_logs, delivery_runs, their event, and lock
// release in one transaction.
func (m *MySQL) TransitionDeliveryRunExecution(log service.DeployLog, expectedVersion int64, event service.DeliveryRunEvent) (service.DeliveryRun, service.DeliveryRunEvent, bool, error) {
	return m.transitionDeliveryRunExecution(log, expectedVersion, event, nil, "")
}

func (m *MySQL) TransitionDeliveryRunExecutionWithClaim(log service.DeployLog, expectedVersion int64, event service.DeliveryRunEvent, claimID string) (service.DeliveryRun, service.DeliveryRunEvent, bool, error) {
	if claimID == "" {
		return service.DeliveryRun{}, event, false, fmt.Errorf("worker claim is required")
	}
	return m.transitionDeliveryRunExecution(log, expectedVersion, event, nil, claimID)
}

// TransitionDeliveryRunExecutionWithWorkloadPromotion commits the active app
// target, workload identity, run event, log state, and lock release together.
func (m *MySQL) TransitionDeliveryRunExecutionWithWorkloadPromotion(log service.DeployLog, expectedVersion int64, event service.DeliveryRunEvent, promotion service.DeliveryBindingWorkloadPromotion) (service.DeliveryRun, service.DeliveryRunEvent, bool, error) {
	return m.transitionDeliveryRunExecution(log, expectedVersion, event, &promotion, "")
}

func (m *MySQL) TransitionDeliveryRunExecutionWithWorkloadPromotionAndClaim(log service.DeployLog, expectedVersion int64, event service.DeliveryRunEvent, promotion service.DeliveryBindingWorkloadPromotion, claimID string) (service.DeliveryRun, service.DeliveryRunEvent, bool, error) {
	if claimID == "" {
		return service.DeliveryRun{}, event, false, fmt.Errorf("worker claim is required")
	}
	return m.transitionDeliveryRunExecution(log, expectedVersion, event, &promotion, claimID)
}

func (m *MySQL) transitionDeliveryRunExecution(log service.DeployLog, expectedVersion int64, event service.DeliveryRunEvent, promotion *service.DeliveryBindingWorkloadPromotion, claimID string) (service.DeliveryRun, service.DeliveryRunEvent, bool, error) {
	tx, err := m.db.Begin()
	if err != nil {
		return service.DeliveryRun{}, event, false, err
	}
	defer tx.Rollback()
	run, err := scanDeliveryRun(tx.QueryRow(`SELECT `+deliveryRunColumns+` FROM delivery_runs WHERE deploy_log_id=? FOR UPDATE`, log.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return service.DeliveryRun{}, event, false, service.ErrNotFound
	}
	if err != nil {
		return service.DeliveryRun{}, event, false, err
	}
	existing, err := scanDeliveryRunEvent(tx.QueryRow(`SELECT `+deliveryRunEventColumns+` FROM delivery_run_events WHERE run_id=? AND event_key=?`, run.ID, event.EventKey))
	if err == nil {
		if existing.EventType != event.EventType || existing.Stage != event.Stage || existing.Status != event.Status || existing.Detail != event.Detail || !bytes.Equal(existing.Payload, event.Payload) {
			return run, existing, false, fmt.Errorf("%w: 发布控制动作幂等键已用于不同状态", service.ErrConflict)
		}
		if err := tx.Commit(); err != nil {
			return run, existing, true, err
		}
		return run, existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return run, event, false, err
	}
	if run.Version != expectedVersion {
		return run, event, false, fmt.Errorf("%w: 发布运行版本已从 %d 更新到 %d", service.ErrConflict, expectedVersion, run.Version)
	}
	if (run.Status == "success" || run.Status == "failed" || run.Status == "interrupted" || run.Status == "cancelled") && event.Status != run.Status {
		return run, event, false, fmt.Errorf("%w: 已结束的发布运行不能从 %s 改回 %s", service.ErrConflict, run.Status, event.Status)
	}
	canaryPreWriteCancel := run.Strategy == "gateway_canary" && event.EventType == "control" && event.Stage == "cancelled" &&
		event.Status == "cancelled" && strings.HasPrefix(event.Detail, "action=cancel;actor=")
	var deployLogID int64
	var deployLogStatus string
	var currentClaim sql.NullString
	if claimID != "" {
		if err := tx.QueryRow(`SELECT id,status,worker_claim_id FROM deploy_logs WHERE id=? FOR UPDATE`, log.ID).Scan(&deployLogID, &deployLogStatus, &currentClaim); errors.Is(err, sql.ErrNoRows) {
			return run, event, false, service.ErrNotFound
		} else if err != nil {
			return run, event, false, err
		} else if !currentClaim.Valid || currentClaim.String != claimID {
			return run, event, false, fmt.Errorf("%w: Worker 认领已失效，拒绝写入发布控制状态", service.ErrConflict)
		}
	} else if canaryPreWriteCancel {
		if err := tx.QueryRow(`SELECT id,status,worker_claim_id FROM deploy_logs WHERE id=? FOR UPDATE`, log.ID).Scan(&deployLogID, &deployLogStatus, &currentClaim); errors.Is(err, sql.ErrNoRows) {
			return run, event, false, service.ErrNotFound
		} else if err != nil {
			return run, event, false, err
		}
	} else {
		if err := tx.QueryRow(`SELECT id,status FROM deploy_logs WHERE id=? FOR UPDATE`, log.ID).Scan(&deployLogID, &deployLogStatus); errors.Is(err, sql.ErrNoRows) {
			return run, event, false, service.ErrNotFound
		} else if err != nil {
			return run, event, false, err
		}
	}
	if canaryPreWriteCancel {
		if run.Status != "queued" && run.Status != "running" {
			return run, event, false, fmt.Errorf("%w: 金丝雀运行已离开可安全取消阶段", service.ErrConflict)
		}
		if deployLogStatus == "running" && !currentClaim.Valid {
			return run, event, false, fmt.Errorf("%w: 无法确认 Worker 认领，不能安全取消金丝雀任务", service.ErrConflict)
		}
		if deployLogStatus != "pending" && deployLogStatus != "running" {
			return run, event, false, fmt.Errorf("%w: 金丝雀任务已结束，不能取消", service.ErrConflict)
		}
		var actionID int64
		actionErr := tx.QueryRow(`SELECT id FROM delivery_execution_actions WHERE deploy_log_id=? ORDER BY id LIMIT 1 FOR UPDATE`, log.ID).Scan(&actionID)
		if actionErr == nil {
			return run, event, false, fmt.Errorf("%w: 金丝雀已有外部动作记录，请通过适配器恢复稳定流量", service.ErrConflict)
		}
		if !errors.Is(actionErr, sql.ErrNoRows) {
			return run, event, false, actionErr
		}
	} else if run.Status == "queued" && event.Status == "cancelled" && deployLogStatus != "pending" {
		return run, event, false, fmt.Errorf("%w: Worker 已领取发布任务，不能再按排队状态取消", service.ErrConflict)
	}
	if promotion != nil {
		if event.Status != "success" || event.Stage != "success" {
			return run, event, false, service.NewValidationError("活动资源引用只能随成功的蓝绿确认一起更新")
		}
		var deploymentName, namespace string
		var clusterID int
		var active service.DeliveryWorkloadReference
		err := tx.QueryRow(`SELECT a.deployment_name,a.cluster_id,a.namespace,
b.active_workload_cluster_id,b.active_workload_namespace,b.active_workload_kind,b.active_workload_name,b.active_workload_uid,b.active_workload_resource_version
FROM apps a JOIN delivery_bindings b ON b.app_id=a.id WHERE a.id=? FOR UPDATE`, promotion.AppID).Scan(
			&deploymentName, &clusterID, &namespace,
			&active.ClusterID, &active.Namespace, &active.Kind, &active.Name, &active.UID, &active.ResourceVersion,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return run, event, false, service.ErrNotFound
		}
		if err != nil {
			return run, event, false, err
		}
		if promotion.ExpectedActiveWorkloadRef.UID == "" || promotion.ExpectedActiveWorkloadRef.Name != promotion.ExpectedDeployment || deploymentName != promotion.ExpectedDeployment || clusterID != promotion.ExpectedActiveWorkloadRef.ClusterID || namespace != promotion.ExpectedActiveWorkloadRef.Namespace ||
			!sameWorkloadReferenceIdentity(active, promotion.ExpectedActiveWorkloadRef) {
			return run, event, false, fmt.Errorf("%w: 活动 Deployment 身份已变化，不能确认蓝绿发布", service.ErrConflict)
		}
		if promotion.ActiveWorkloadRef.ClusterID != clusterID || promotion.ActiveWorkloadRef.Namespace != namespace || promotion.ActiveWorkloadRef.Kind != "Deployment" || promotion.ActiveWorkloadRef.Name == "" || promotion.ActiveWorkloadRef.UID == "" {
			return run, event, false, service.NewValidationError("新活动 Deployment 身份不完整")
		}
		result, err := tx.Exec(`UPDATE apps SET deployment_name=? WHERE id=? AND deployment_name=?`, promotion.ActiveWorkloadRef.Name, promotion.AppID, promotion.ExpectedDeployment)
		if err != nil {
			return run, event, false, wrapConflict(err)
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return run, event, false, err
			}
			return run, event, false, fmt.Errorf("%w: 生产应用目标已变化，未更新活动 Deployment", service.ErrConflict)
		}
		result, err = tx.Exec(`UPDATE delivery_bindings SET active_workload_cluster_id=?,active_workload_namespace=?,active_workload_kind=?,active_workload_name=?,active_workload_uid=?,active_workload_resource_version=? WHERE app_id=?`,
			promotion.ActiveWorkloadRef.ClusterID, promotion.ActiveWorkloadRef.Namespace, promotion.ActiveWorkloadRef.Kind, promotion.ActiveWorkloadRef.Name, promotion.ActiveWorkloadRef.UID, promotion.ActiveWorkloadRef.ResourceVersion, promotion.AppID)
		if err != nil {
			return run, event, false, wrapConflict(err)
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return run, event, false, err
			}
			return run, event, false, service.ErrNotFound
		}
	}
	if claimID != "" {
		query := `UPDATE deploy_logs SET status=?,current_step=?,image_full=?,step_detail=?,error_message=?,started_at=?,finished_at=?,worker_claim_id=CASE WHEN ? IN ('success','failed','interrupted') THEN NULL ELSE worker_claim_id END WHERE id=? AND worker_claim_id=?`
		result, err := tx.Exec(query, log.Status, log.CurrentStep, log.ImageFull, emptyToNil(log.StepDetail), log.ErrorMessage, log.StartedAt, log.FinishedAt, log.Status, log.ID, claimID)
		if err != nil {
			return run, event, false, wrapConflict(err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return run, event, false, err
		}
		if changed == 0 && log.Status != "success" && log.Status != "failed" && log.Status != "interrupted" {
			var currentClaim sql.NullString
			if err := tx.QueryRow(`SELECT worker_claim_id FROM deploy_logs WHERE id=?`, log.ID).Scan(&currentClaim); err != nil || !currentClaim.Valid || currentClaim.String != claimID {
				return run, event, false, fmt.Errorf("%w: Worker 认领已失效，拒绝写入发布控制状态", service.ErrConflict)
			}
		}
	} else {
		query := `UPDATE deploy_logs SET status=?,current_step=?,image_full=?,step_detail=?,error_message=?,started_at=?,finished_at=? WHERE id=?`
		if log.Status == "success" || log.Status == "failed" || log.Status == "interrupted" || event.Stage == "needs_attention" {
			query = `UPDATE deploy_logs SET status=?,current_step=?,image_full=?,step_detail=?,error_message=?,started_at=?,finished_at=?,worker_claim_id=NULL WHERE id=?`
		}
		if _, err := tx.Exec(query, log.Status, log.CurrentStep, log.ImageFull, emptyToNil(log.StepDetail), log.ErrorMessage, log.StartedAt, log.FinishedAt, log.ID); err != nil {
			return run, event, false, wrapConflict(err)
		}
	}
	if log.Status == "success" || log.Status == "failed" || log.Status == "interrupted" {
		if _, err := tx.Exec(`DELETE FROM delivery_resource_locks WHERE deploy_log_id=?`, log.ID); err != nil {
			return run, event, false, err
		}
	}
	newVersion := run.Version + 1
	now := time.Now().UTC()
	var message any
	if event.Status == "failed" || event.Status == "interrupted" {
		message = event.Detail
	}
	runResult, err := tx.Exec(`UPDATE delivery_runs SET status=?,current_stage=?,version=?,error_message=?,updated_at=? WHERE id=? AND version=?`,
		event.Status, event.Stage, newVersion, message, now, run.ID, expectedVersion)
	if err != nil {
		return run, event, false, wrapConflict(err)
	}
	runRows, err := runResult.RowsAffected()
	if err != nil {
		return run, event, false, err
	}
	if runRows != 1 {
		return run, event, false, fmt.Errorf("%w: 发布运行版本已变化", service.ErrConflict)
	}
	event.RunID = run.ID
	event.Version = newVersion
	event.CreatedAt = now
	if err := insertDeliveryRunEvent(tx, event); err != nil {
		return run, event, false, err
	}
	run, err = scanDeliveryRun(tx.QueryRow(`SELECT `+deliveryRunColumns+` FROM delivery_runs WHERE id=?`, run.ID))
	if err != nil {
		return run, event, false, err
	}
	event, err = scanDeliveryRunEvent(tx.QueryRow(`SELECT `+deliveryRunEventColumns+` FROM delivery_run_events WHERE run_id=? AND event_key=?`, run.ID, event.EventKey))
	if err != nil {
		return run, event, false, err
	}
	if err := tx.Commit(); err != nil {
		return run, event, false, err
	}
	return run, event, false, nil
}
