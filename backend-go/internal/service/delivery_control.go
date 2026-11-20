package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	DeliveryActionConfirmCanaryStage   = "confirm_canary_stage"
	DeliveryActionConfirmCanaryRestore = "confirm_canary_restore"
	DeliveryActionConfirmCutover       = "confirm_cutover"
	DeliveryActionConfirmLive          = "confirm_live_verification"
	DeliveryActionRestoreStable        = "restore_stable"
	DeliveryActionReconcile            = "reconcile"
	DeliveryActionPause                = "pause"
	DeliveryActionContinue             = "continue"
	DeliveryActionCancel               = "cancel"
)

type DeliveryRunControlOptions struct {
	RecoveryCompatibilityAcknowledged bool
}

// ActOnDeliveryRun applies an operator control to a durable delivery run.
// Cluster-facing blue-green actions perform a fresh Kubernetes read; every
// control transition uses optimistic concurrency and an idempotency key.
func (s *CDService) ActOnDeliveryRun(ctx context.Context, runID int64, action string, expectedVersion int64, idempotencyKey, actor string) (DeliveryRunView, error) {
	return s.ActOnDeliveryRunWithOptions(ctx, runID, action, expectedVersion, idempotencyKey, actor, DeliveryRunControlOptions{})
}

func (s *CDService) ActOnDeliveryRunWithOptions(ctx context.Context, runID int64, action string, expectedVersion int64, idempotencyKey, actor string, options DeliveryRunControlOptions) (DeliveryRunView, error) {
	if s == nil || s.store == nil {
		return DeliveryRunView{}, NewValidationError("发布运行时不可用")
	}
	action = strings.TrimSpace(action)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	actor = strings.TrimSpace(actor)
	if runID <= 0 || expectedVersion <= 0 || len(idempotencyKey) < 8 || len(idempotencyKey) > 120 || actor == "" || actor == "-" || len(actor) > 128 || strings.ContainsAny(actor, ";\r\n") || strings.HasPrefix(actor, "apikey:") {
		return DeliveryRunView{}, NewValidationError("发布控制动作缺少有效版本、幂等键或操作员")
	}
	if action != DeliveryActionConfirmCanaryStage && action != DeliveryActionConfirmCanaryRestore && action != DeliveryActionConfirmCutover && action != DeliveryActionConfirmLive && action != DeliveryActionRestoreStable && action != DeliveryActionReconcile && action != DeliveryActionCleanupCandidate && action != DeliveryActionPause && action != DeliveryActionContinue && action != DeliveryActionCancel {
		return DeliveryRunView{}, NewValidationError("未知的发布控制动作")
	}
	run, err := s.store.DeliveryRun(runID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	plan, err := s.store.FindDeliveryPlan(run.DeliveryPlanID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	canaryConfirmation := run.Strategy == "gateway_canary" && action == DeliveryActionConfirmCanaryStage
	canaryRestoreConfirmation := run.Strategy == "gateway_canary" && action == DeliveryActionConfirmCanaryRestore
	canaryPauseContinue := run.Strategy == "gateway_canary" && (action == DeliveryActionPause || action == DeliveryActionContinue)
	canaryPreWriteCancel := run.Strategy == "gateway_canary" && action == DeliveryActionCancel
	canaryControl := canaryConfirmation || canaryRestoreConfirmation || canaryPauseContinue || canaryPreWriteCancel
	if !canaryControl && (s.worker == nil || s.worker.pipeline == nil || s.worker.pipeline.k8s == nil) {
		return DeliveryRunView{}, NewValidationError("发布运行时不可用")
	}
	if run.Strategy != plan.Strategy || (run.Strategy != "blue_green" && !(run.Strategy == "rolling" && (action == DeliveryActionPause || action == DeliveryActionContinue || action == DeliveryActionCancel)) && !canaryControl) {
		return DeliveryRunView{}, NewValidationError("当前发布策略不支持此人工运行控制")
	}
	key := "control:" + idempotencyKey
	events, err := s.store.DeliveryRunEvents(run.ID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	for _, existing := range events {
		if existing.EventKey != key {
			continue
		}
		validEventType := existing.EventType == "control" || canaryConfirmation && existing.EventType == "canary_checkpoint" || canaryRestoreConfirmation && existing.EventType == "canary_restore_verification"
		if !validEventType || !strings.HasPrefix(existing.Detail, "action="+action+";actor="+actor+";") {
			return DeliveryRunView{}, newConflict("该幂等键已用于其他发布控制动作")
		}
		return s.store.DeliveryRunView(run.ID)
	}
	if run.Version != expectedVersion {
		return DeliveryRunView{}, newConflict("发布运行版本已更新，请刷新后重试")
	}
	blueGreenRecovery := run.Strategy == "blue_green" && (action == DeliveryActionRestoreStable || action == DeliveryActionCancel)
	if blueGreenRecovery && !options.RecoveryCompatibilityAcknowledged {
		return DeliveryRunView{}, NewValidationError("恢复稳定版本前，请确认旧版本兼容当前数据库和外部配置；平台只恢复 Service 入口，不回退数据库迁移或 ConfigMap/Secret/Helm-Git 配置")
	}
	if canaryPreWriteCancel && run.Status != "queued" && run.Status != "running" {
		return DeliveryRunView{}, NewValidationError("金丝雀仅能在首次外部动作前取消；已有动作时请通过适配器恢复稳定流量")
	}
	if canaryConfirmation {
		if len(plan.Blockers) > 0 {
			return DeliveryRunView{}, NewValidationError("发布计划存在阻断项，不能确认金丝雀阶段")
		}
		if run.Status != "running" {
			return DeliveryRunView{}, NewValidationError("当前金丝雀运行已结束或暂停，不能确认阶段")
		}
		return s.confirmCanaryStage(run, plan, key, actor)
	}
	if canaryRestoreConfirmation {
		if len(plan.Blockers) > 0 {
			return DeliveryRunView{}, NewValidationError("发布计划存在阻断项，不能确认恢复后的业务状态")
		}
		if run.Status != "running" || run.CurrentStage != "awaiting_restore_verification" {
			return DeliveryRunView{}, NewValidationError("只有稳定流量已回读且等待业务验证的金丝雀运行可以确认恢复")
		}
		return s.confirmCanaryRestore(run, plan, key, actor)
	}
	canaryConfirmationGate := false
	if canaryPauseContinue {
		checkpoint, checkpointErr := LatestCanaryStageCheckpoint(events)
		if checkpointErr != nil {
			return DeliveryRunView{}, checkpointErr
		}
		if checkpoint == nil || checkpoint.Phase != CanaryStageAwaitingConfirm ||
			checkpoint.Decision == nil || checkpoint.Decision.Analysis.Status != "awaiting_confirmation" ||
			run.CurrentStage != fmt.Sprintf("canary_stage_%d", checkpoint.StageIndex+1) {
			return DeliveryRunView{}, NewValidationError("金丝雀仅能在健康观测完成、等待人工确认的阶段暂停或继续")
		}
		if err := validateCanaryCheckpointAgainstPlan(*checkpoint, plan); err != nil {
			return DeliveryRunView{}, err
		}
		if action == DeliveryActionContinue && len(plan.Blockers) > 0 {
			return DeliveryRunView{}, NewValidationError("发布计划存在阻断项，不能继续金丝雀运行")
		}
		canaryConfirmationGate = true
	}
	cleanupMode := action == DeliveryActionCleanupCandidate
	operatorBarrier := run.CurrentStage == "awaiting_candidate_review" || run.CurrentStage == "awaiting_live_verification"
	rollingPausePoint := run.Strategy == "rolling" && run.CurrentStage == "rollout_wait"
	rollingPreWriteCancel := false
	if action == DeliveryActionCancel && run.Strategy == "rolling" {
		if run.CurrentStage != "started" && run.CurrentStage != "resolve_digest" {
			return DeliveryRunView{}, NewValidationError("镜像写入阶段已开始；滚动发布请使用显式镜像回退")
		}
		actions, actionErr := s.store.ExecutionActions(run.DeployLogID)
		if actionErr != nil {
			return DeliveryRunView{}, actionErr
		}
		for _, externalAction := range actions {
			if externalAction.ActionKey == "set-image" {
				return DeliveryRunView{}, NewValidationError("镜像变更动作已创建；为避免误报取消，请使用显式镜像回退")
			}
		}
		rollingPreWriteCancel = true
	}
	if cleanupMode {
		if (run.Status != "failed" && run.Status != "cancelled") || run.CurrentStage != "restored" {
			return DeliveryRunView{}, NewValidationError("只有已恢复稳定版本的蓝绿运行可以清理候选")
		}
	} else if action == DeliveryActionPause {
		if run.Status != "running" || (!operatorBarrier && !rollingPausePoint && !canaryConfirmationGate) {
			return DeliveryRunView{}, NewValidationError("蓝绿发布仅能在操作员关口暂停；滚动发布仅能在 rollout 检查阶段暂停平台推进；金丝雀仅能在健康确认关口暂停")
		}
	} else if action == DeliveryActionContinue {
		if run.Status != "paused" || (!operatorBarrier && !(run.Strategy == "rolling" && run.CurrentStage == "rollout_wait") && !canaryConfirmationGate) {
			return DeliveryRunView{}, NewValidationError("当前发布运行没有处于可继续的暂停关口")
		}
	} else if action == DeliveryActionCancel && !canaryPreWriteCancel {
		if run.Strategy == "rolling" {
			if run.Status != "running" || !rollingPreWriteCancel {
				return DeliveryRunView{}, NewValidationError("滚动发布仅能在镜像写入前取消；写入后请使用显式镜像回退")
			}
		} else if (run.Status != "running" && run.Status != "paused") || !operatorBarrier {
			return DeliveryRunView{}, NewValidationError("蓝绿发布仅能在操作员确认关口取消；状态不明时请先核对或恢复")
		}
	} else if action == DeliveryActionRestoreStable {
		if (run.Status != "running" && run.Status != "paused") || (run.CurrentStage != "awaiting_candidate_review" && run.CurrentStage != "awaiting_live_verification" && run.CurrentStage != "needs_attention") {
			return DeliveryRunView{}, NewValidationError("当前阶段不支持恢复稳定版本")
		}
	} else if run.Status != "running" && !canaryPreWriteCancel {
		return DeliveryRunView{}, NewValidationError("当前发布运行已结束或暂停，不能执行此控制动作")
	}
	logRow, err := s.store.FindLog(run.DeployLogID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	if canaryPreWriteCancel {
		if logRow.Status != "pending" && logRow.Status != "running" {
			return DeliveryRunView{}, newConflict("金丝雀任务已结束，不能取消")
		}
		now := time.Now().UTC()
		message := "金丝雀在首次外部动作前取消；事务确认没有网关或集群动作记录，并撤销 Worker 认领。"
		logRow.Status = "failed"
		logRow.CurrentStep = strPtr("cancelled")
		logRow.ErrorMessage = &message
		logRow.FinishedAt = &now
		updated, _, err := s.store.TransitionDeliveryRun(logRow, expectedVersion, DeliveryRunEvent{
			EventKey: key, EventType: "control", Stage: "cancelled", Status: "cancelled",
			Detail: fmt.Sprintf("action=%s;actor=%s;%s", action, actor, message),
		})
		if err != nil {
			return DeliveryRunView{}, err
		}
		return s.store.DeliveryRunView(updated.ID)
	}
	if action == DeliveryActionCancel && rollingPreWriteCancel {
		now := time.Now().UTC()
		message := "操作员在镜像写入前取消了滚动发布；生产 Deployment 未被本次任务修改。"
		logRow.Status = "failed"
		logRow.CurrentStep = strPtr("cancelled")
		logRow.ErrorMessage = &message
		logRow.FinishedAt = &now
		updated, _, err := s.store.TransitionDeliveryRun(logRow, expectedVersion, DeliveryRunEvent{
			EventKey: key, EventType: "control", Stage: "cancelled", Status: "cancelled",
			Detail: fmt.Sprintf("action=%s;actor=%s;%s", action, actor, message),
		})
		if err != nil {
			return DeliveryRunView{}, err
		}
		return s.store.DeliveryRunView(updated.ID)
	}
	if action == DeliveryActionPause || action == DeliveryActionContinue {
		status, detail := "paused", "已暂停发布推进。当前 Service 继续承接现有流量；需要回退时仍可恢复稳定版本。"
		if canaryConfirmationGate {
			checkpoint, _ := LatestCanaryStageCheckpoint(events)
			stageNumber := checkpoint.StageIndex + 1
			detail = fmt.Sprintf("已暂停在金丝雀第 %d 阶段的人工确认关口；健康观测快照和资源锁保留。继续后仍会校验观测时效，不会自动调整网关权重。", stageNumber)
		}
		if rollingPausePoint {
			detail = "已暂停平台后续 rollout 检查；Kubernetes Deployment 控制器仍会继续完成正在进行的滚动更新。"
		}
		if action == DeliveryActionContinue {
			status, detail = "running", "已继续发布推进；仍停在原操作员确认关口，不会自动切换流量。"
			if canaryConfirmationGate {
				checkpoint, _ := LatestCanaryStageCheckpoint(events)
				detail = fmt.Sprintf("已继续金丝雀第 %d 阶段的人工确认关口；网关权重保持原样，确认前仍会校验观测时效。", checkpoint.StageIndex+1)
			}
			if run.Strategy == "rolling" {
				detail = "已重新排队滚动发布；平台会回读现有镜像动作并继续 rollout 检查。"
			}
		}
		logRow.Status = "running" // Keep the legacy task and its resource locks active.
		if action == DeliveryActionContinue && run.Strategy == "rolling" {
			logRow.Status = "pending" // The worker claims only durable pending tasks.
		}
		logRow.CurrentStep = strPtr(run.CurrentStage)
		logRow.ErrorMessage = nil
		updated, _, err := s.store.TransitionDeliveryRun(logRow, expectedVersion, DeliveryRunEvent{
			EventKey: key, EventType: "control", Stage: run.CurrentStage, Status: status,
			Detail: fmt.Sprintf("action=%s;actor=%s;%s", action, actor, detail),
		})
		if err != nil {
			return DeliveryRunView{}, err
		}
		if action == DeliveryActionContinue && run.Strategy == "rolling" {
			// Pending is durable and is also discovered by the worker's scanner;
			// the submit is only a low-latency wake-up hint.
			_ = s.worker.Submit(ctx, logRow.ID)
		}
		return s.store.DeliveryRunView(updated.ID)
	}
	cluster, err := s.store.FindCluster(plan.TargetClusterID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	runtime, err := s.worker.pipeline.k8s.For(cluster)
	if err != nil {
		if cleanupMode {
			return s.recordBlueGreenCleanupEvent(run, logRow, key, action, actor, "候选清理未完成：无法连接目标集群。", err)
		}
		return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
	}
	if len(plan.Blockers) > 0 {
		if cleanupMode {
			return s.recordBlueGreenCleanupEvent(run, logRow, key, action, actor, "候选清理未完成：发布计划存在阻断项。", NewValidationError("发布计划存在阻断项，不能执行清理动作"))
		}
		return DeliveryRunView{}, NewValidationError("发布计划存在阻断项，不能执行控制动作")
	}
	if plan.Snapshot.SchemaVersion >= 7 && (action == DeliveryActionConfirmCutover || action == DeliveryActionConfirmLive) {
		if err := validateLiveDeliveryConfigurationReferences(ctx, runtime, plan); err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
	}
	detailPrefix := fmt.Sprintf("action=%s;actor=%s;", action, actor)
	if blueGreenRecovery {
		detailPrefix += "recovery_compatibility=operator_confirmed;"
	}
	var stage, status, detail string
	var workloadPromotion *DeliveryBindingWorkloadPromotion
	switch action {
	case DeliveryActionConfirmCutover:
		if run.CurrentStage != "awaiting_candidate_review" {
			return DeliveryRunView{}, NewValidationError("只有候选就绪并等待确认时才能切换生产流量")
		}
		_, _, entry, candidateDeployment, inspectErr := inspectBlueGreenCandidate(ctx, s.store, runtime, logRow.ID, plan, run.ID)
		if inspectErr != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, inspectErr)
		}
		appendCandidateImageEvidence(&logRow, observeBlueGreenCandidateImage(ctx, runtime, plan, candidateDeployment.GetName(), s.worker.pipeline.cfg))
		selector, found, selectorErr := unstructured.NestedStringMap(entry.Object, "spec", "selector")
		if selectorErr != nil || !found {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, NewValidationError("生产 Service selector 无法读取"))
		}
		candidateSelector, _ := BlueGreenCandidateSelector(plan.Snapshot.Target.CandidateLabelKey, plan.Snapshot.Target.CandidateLabelValue)
		if !sameStringMap(selector, candidateSelector) {
			if err := switchBlueGreenServiceToCandidate(ctx, s.store, runtime, logRow.ID, plan, run.ID); err != nil {
				return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
			}
		}
		if _, err := waitForBlueGreenCandidateEntryEndpoints(ctx, runtime, plan.Snapshot.Target.Namespace, plan.Snapshot.Target.ServiceName, plan.Snapshot.Target.CandidateLabelKey, plan.Snapshot.Target.CandidateLabelValue); err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
		stage, status = "awaiting_live_verification", "running"
		detail = detailPrefix + "候选已就绪且生产 Service 已切至候选轨道；请完成业务验证后确认，或恢复稳定版本。"
	case DeliveryActionConfirmLive:
		if run.CurrentStage != "awaiting_live_verification" {
			return DeliveryRunView{}, NewValidationError("只有切流后等待业务验证时才能确认完成")
		}
		activeDeployment, activeRef, err := promoteBlueGreenCandidateToStable(ctx, s.store, runtime, logRow.ID, plan, run.ID)
		if err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
		app, err := s.store.FindAppByName(plan.TargetClusterID, plan.Snapshot.Target.AppName)
		if err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
		if app.ID != plan.TargetAppID {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, newConflict("当前生产应用绑定已变化"))
		}
		binding, err := s.store.DeliveryBindingForApp(app.ID)
		if err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
		expectedActive := plan.Snapshot.Target.ActiveWorkloadRef
		if expectedActive.UID == "" {
			expectedActive = DeliveryWorkloadReference{ClusterID: plan.TargetClusterID, Namespace: app.Namespace, Kind: "Deployment", Name: plan.Snapshot.Target.Deployment, UID: plan.Snapshot.Target.WorkloadUID, ResourceVersion: plan.Snapshot.Target.ResourceVersion}
		}
		if app.DeploymentName != plan.Snapshot.Target.Deployment || !sameDeliveryWorkloadIdentity(binding.ActiveWorkloadRef, expectedActive) {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, newConflict("生产应用或活动 Deployment 身份已变化，不能确认蓝绿发布"))
		}
		if activeRef.Name != activeDeployment {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, newConflict("候选 Deployment 身份与确认目标不一致"))
		}
		workloadPromotion = &DeliveryBindingWorkloadPromotion{
			AppID: app.ID, ExpectedDeployment: plan.Snapshot.Target.Deployment,
			ExpectedActiveWorkloadRef: expectedActive, ActiveWorkloadRef: activeRef,
		}
		now := time.Now().UTC()
		logRow.Status = "success"
		logRow.CurrentStep = strPtr("done")
		logRow.ErrorMessage = nil
		logRow.FinishedAt = &now
		stage, status = "success", "success"
		detail = detailPrefix + fmt.Sprintf("业务验证通过，生产流量已稳定在 %s；平台活动绑定已更新，旧 Deployment 保留供后续回退。", activeDeployment)
	case DeliveryActionRestoreStable:
		if err := restoreBlueGreenEntryToStable(ctx, s.store, runtime, logRow.ID, plan, run.ID); err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
		app, err := s.store.FindAppByName(plan.TargetClusterID, plan.Snapshot.Target.AppName)
		if err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
		candidateName := blueGreenResourceName(plan.Snapshot.Target.Deployment, plan.ID)
		if app.DeploymentName == candidateName {
			app.DeploymentName = plan.Snapshot.Target.Deployment
			if _, err := s.store.UpdateApp(app); err != nil {
				return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
			}
		} else if app.DeploymentName != plan.Snapshot.Target.Deployment {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, newConflict("生产应用已指向其他 Deployment，不能自动恢复绑定"))
		}
		now := time.Now().UTC()
		message := "已恢复稳定版本入口；候选资源保留用于排查。"
		logRow.Status = "failed"
		logRow.CurrentStep = strPtr("restored")
		logRow.ErrorMessage = &message
		logRow.FinishedAt = &now
		stage, status, detail = "restored", "failed", detailPrefix+message
	case DeliveryActionCancel:
		if err := restoreBlueGreenEntryToStable(ctx, s.store, runtime, logRow.ID, plan, run.ID); err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
		app, err := s.store.FindAppByName(plan.TargetClusterID, plan.Snapshot.Target.AppName)
		if err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
		candidateName := blueGreenResourceName(plan.Snapshot.Target.Deployment, plan.ID)
		if app.DeploymentName == candidateName {
			app.DeploymentName = plan.Snapshot.Target.Deployment
			if _, err := s.store.UpdateApp(app); err != nil {
				return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
			}
		} else if app.DeploymentName != plan.Snapshot.Target.Deployment {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, newConflict("生产应用已指向其他 Deployment，不能取消并恢复绑定"))
		}
		now := time.Now().UTC()
		logRow.Status = "failed" // The legacy deploy log keeps its existing terminal status vocabulary.
		logRow.CurrentStep = strPtr("restored")
		logRow.ErrorMessage = strPtr("发布已取消；稳定 Service 已恢复，候选资源保留供排查。")
		logRow.FinishedAt = &now
		stage, status = "restored", "cancelled"
		detail = detailPrefix + "发布已取消，且已确认稳定 Service 入口恢复；候选资源保留供排查。"
	case DeliveryActionReconcile:
		if run.CurrentStage != "awaiting_candidate_review" && run.CurrentStage != "awaiting_live_verification" && run.CurrentStage != "needs_attention" {
			return DeliveryRunView{}, NewValidationError("当前阶段不需要蓝绿核对")
		}
		serviceDef, defErr := kube.LookupResource("", "v1", "services")
		if defErr != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, defErr)
		}
		entry, readErr := runtime.GetResource(ctx, serviceDef, plan.Snapshot.Target.Namespace, plan.Snapshot.Target.ServiceName, metav1.GetOptions{})
		if readErr != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, fmt.Errorf("读取生产 Service 失败: %w", readErr))
		}
		if err := validateBlueGreenEntryService(entry, plan.Snapshot.Target.ServiceName, plan.Snapshot.Target.ServiceUID, plan.Snapshot.Target.ServiceConfigHash); err != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
		}
		selector, found, selectorErr := unstructured.NestedStringMap(entry.Object, "spec", "selector")
		if selectorErr != nil || !found {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, NewValidationError("生产 Service selector 无法读取"))
		}
		stableSelector, selectorErr := BlueGreenStableServiceSelector(plan.Snapshot.Target.ServiceSelector, plan.Snapshot.Target.StableLabelKey)
		if selectorErr != nil {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, selectorErr)
		}
		candidateSelector, _ := BlueGreenCandidateSelector(plan.Snapshot.Target.CandidateLabelKey, plan.Snapshot.Target.CandidateLabelValue)
		if sameStringMap(selector, plan.Snapshot.Target.ServiceSelector) || sameStringMap(selector, stableSelector) {
			// Recovery may happen before preparation has moved the Service to the
			// stable track (for example, after the stable Deployment label PATCH
			// applied but its response was lost). Both the snapshotted baseline
			// selector and the platform's stable selector are known pre-cutover
			// states; preparation revalidates the live resources before advancing.
			candidate, err := prepareBlueGreenCandidateWithConfig(ctx, s.store, runtime, logRow, plan, run.ID, s.worker.pipeline.cfg)
			if err != nil {
				return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
			}
			appendCandidateImageEvidence(&logRow, candidate.CandidateImageEvidence)
			stage = "awaiting_candidate_review"
			detail = detailPrefix + "已回读确认生产 Service 仍处于本次计划的发布前状态，候选准备已按动作记录续接并完成核验，可由操作员选择切换或恢复。"
		} else if sameStringMap(selector, candidateSelector) {
			// Candidate traffic is already live, so all candidate resources and
			// their endpoints must be complete before marking reconciliation done.
			_, _, entry, candidateDeployment, inspectErr := inspectBlueGreenCandidate(ctx, s.store, runtime, logRow.ID, plan, run.ID)
			if inspectErr != nil {
				return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, inspectErr)
			}
			appendCandidateImageEvidence(&logRow, observeBlueGreenCandidateImage(ctx, runtime, plan, candidateDeployment.GetName(), s.worker.pipeline.cfg))
			observedSelector, observed, observedErr := unstructured.NestedStringMap(entry.Object, "spec", "selector")
			if observedErr != nil || !observed || !sameStringMap(observedSelector, candidateSelector) {
				return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, newConflict("生产 Service 在核对期间已偏离候选轨道"))
			}
			// The Service may already have switched before a process stopped
			// persisting the write result. Reuse the durable action intent to
			// verify identity and complete its readback before advancing.
			if err := switchBlueGreenServiceToCandidate(ctx, s.store, runtime, logRow.ID, plan, run.ID); err != nil {
				return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
			}
			if _, err := waitForBlueGreenCandidateEntryEndpoints(ctx, runtime, plan.Snapshot.Target.Namespace, plan.Snapshot.Target.ServiceName, plan.Snapshot.Target.CandidateLabelKey, plan.Snapshot.Target.CandidateLabelValue); err != nil {
				return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, err)
			}
			stage = "awaiting_live_verification"
			detail = detailPrefix + "已回读确认生产 Service 指向就绪候选，等待业务验证。"
		} else {
			return s.deliveryActionNeedsAttention(run, logRow, key, action, actor, newConflict("生产 Service 当前没有指向稳定或本次候选轨道"))
		}
		logRow.ErrorMessage = nil
		status = "running"
	case DeliveryActionCleanupCandidate:
		if err := cleanupRestoredBlueGreenCandidate(ctx, s.store, runtime, run, plan, logRow); err != nil {
			return s.recordBlueGreenCleanupEvent(run, logRow, key, action, actor, "候选清理未完成："+err.Error(), err)
		}
		return s.recordBlueGreenCleanupEvent(run, logRow, key, action, actor, "候选预览 Service 和平台创建的候选 Deployment 已按计划清理；稳定 Deployment 与生产入口保持不变。", nil)
	}
	if !strings.HasPrefix(detail, detailPrefix) {
		return DeliveryRunView{}, NewValidationError("发布控制动作状态无效")
	}
	logRow.CurrentStep = strPtr(stage)
	event := DeliveryRunEvent{
		EventKey: key, EventType: "control", Stage: stage, Status: status, Detail: detail,
	}
	var updated DeliveryRun
	if workloadPromotion != nil {
		updated, _, err = s.store.TransitionDeliveryRunWithWorkloadPromotion(logRow, expectedVersion, event, *workloadPromotion)
	} else {
		updated, _, err = s.store.TransitionDeliveryRun(logRow, expectedVersion, event)
	}
	if err != nil {
		return DeliveryRunView{}, err
	}
	return s.store.DeliveryRunView(updated.ID)
}

func (s *CDService) confirmCanaryRestore(run DeliveryRun, plan DeliveryPlan, eventKey, actor string) (DeliveryRunView, error) {
	events, err := s.store.DeliveryRunEvents(run.ID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	var restoreEvent *DeliveryRunEvent
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].EventType == "canary_restore" {
			event := events[i]
			restoreEvent = &event
			break
		}
	}
	if restoreEvent == nil {
		return DeliveryRunView{}, NewValidationError("缺少稳定流量恢复成功的持久化回读记录")
	}
	checkpoint, err := DecodeCanaryRestoreCheckpoint(*restoreEvent)
	if err != nil {
		return DeliveryRunView{}, err
	}
	if err := validateCanaryRestoreCheckpointAgainstPlan(checkpoint, plan); err != nil {
		return DeliveryRunView{}, err
	}
	logRow, err := s.store.FindLog(run.DeployLogID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	if logRow.Status != "running" {
		return DeliveryRunView{}, NewValidationError("金丝雀任务不在运行状态，不能确认恢复后的业务状态")
	}
	now := time.Now().UTC()
	message := "稳定流量恢复后的业务验证通过；本次金丝雀发布以失败状态结束，资源锁已释放。"
	logRow.Status = "failed"
	logRow.CurrentStep = strPtr("restore_verified")
	logRow.ErrorMessage = &message
	logRow.FinishedAt = &now
	event := DeliveryRunEvent{
		EventKey: eventKey, EventType: "canary_restore_verification", Stage: "restore_verified", Status: "failed",
		Detail: fmt.Sprintf("action=%s;actor=%s;%s", DeliveryActionConfirmCanaryRestore, actor, message),
	}
	updated, _, err := s.store.TransitionDeliveryRun(logRow, run.Version, event)
	if err != nil {
		return DeliveryRunView{}, err
	}
	return s.store.DeliveryRunView(updated.ID)
}

func (s *CDService) confirmCanaryStage(run DeliveryRun, plan DeliveryPlan, eventKey, actor string) (DeliveryRunView, error) {
	checkpoint, err := s.store.LatestCanaryStageCheckpoint(run.ID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	if checkpoint == nil || checkpoint.Phase != CanaryStageAwaitingConfirm || checkpoint.Observation == nil || checkpoint.Decision == nil ||
		checkpoint.Decision.Analysis.Status != "awaiting_confirmation" || run.CurrentStage != fmt.Sprintf("canary_stage_%d", checkpoint.StageIndex+1) {
		return DeliveryRunView{}, NewValidationError("只有健康观测完成并等待人工确认的金丝雀阶段可以推进")
	}
	decisionAt := checkpoint.Decision.EvaluatedAt
	if decisionAt.IsZero() { // Older schema-v1 checkpoints did not persist evaluation time.
		decisionAt = checkpoint.Observation.ObservedAt
	}
	maxAge := time.Duration(plan.Snapshot.Policy.CanaryAnalysis.MaxObservationAgeSeconds) * time.Second
	now := time.Now().UTC()
	if decisionAt.After(now) || now.Sub(decisionAt) > maxAge || now.Sub(checkpoint.Observation.ObservedAt) > maxAge {
		return DeliveryRunView{}, NewValidationError("金丝雀观测已过期，请重新采集并复核阶段指标")
	}
	stages := plan.Snapshot.Policy.CanaryAnalysis.Stages
	if checkpoint.StageIndex >= len(stages) {
		return DeliveryRunView{}, NewValidationError("当前金丝雀阶段超出不可变发布计划")
	}
	confirmedIndex := checkpoint.StageIndex
	detailPrefix := fmt.Sprintf("action=%s;actor=%s;", DeliveryActionConfirmCanaryStage, actor)
	if checkpoint.StageIndex+1 < len(stages) {
		nextStage := stages[checkpoint.StageIndex+1]
		next := CanaryStageCheckpoint{
			SchemaVersion: CanaryStageCheckpointSchemaVersion, StageIndex: checkpoint.StageIndex + 1,
			CandidateWeightPercent: nextStage.CandidateWeightPercent, ObservationDurationSeconds: nextStage.ObservationDurationSeconds,
			Phase: CanaryStageAwaitingWeights, ConfirmedStageIndex: &confirmedIndex, ConfirmedBy: actor, ConfirmedAt: &now,
		}
		_, _, err := s.store.RecordCanaryStageCheckpoint(run.DeployLogID, run.Version, eventKey, "running",
			detailPrefix+fmt.Sprintf("已确认第 %d 阶段观测健康；下一阶段计划候选流量 %d%%，等待网关权重回读。", checkpoint.StageIndex+1, nextStage.CandidateWeightPercent), next)
		if err != nil {
			return DeliveryRunView{}, err
		}
		return s.store.DeliveryRunView(run.ID)
	}
	if checkpoint.CandidateWeightPercent != 100 {
		return DeliveryRunView{}, NewValidationError("最终金丝雀阶段必须为 100%% 候选流量")
	}
	completed := *checkpoint
	completed.Phase = CanaryStageCompleted
	completed.ConfirmedStageIndex = &confirmedIndex
	completed.ConfirmedBy = actor
	completed.ConfirmedAt = &now
	_, _, err = s.store.RecordCanaryStageCheckpoint(run.DeployLogID, run.Version, eventKey, "success",
		detailPrefix+"已确认最终 100% 候选流量观测健康，金丝雀发布完成。", completed)
	if err != nil {
		return DeliveryRunView{}, err
	}
	return s.store.DeliveryRunView(run.ID)
}

func (s *CDService) recordBlueGreenCleanupEvent(run DeliveryRun, logRow DeployLog, key, action, actor, message string, cause error) (DeliveryRunView, error) {
	_, _, persistErr := s.store.RecordDeliveryRunEvent(logRow.ID, run.Version, DeliveryRunEvent{
		EventKey: key, EventType: "control", Stage: "restored", Status: run.Status,
		Detail: fmt.Sprintf("action=%s;actor=%s;%s", action, actor, message),
	})
	if persistErr != nil {
		return DeliveryRunView{}, persistErr
	}
	view, err := s.store.DeliveryRunView(run.ID)
	if err != nil {
		return DeliveryRunView{}, err
	}
	return view, cause
}

func (s *CDService) deliveryActionNeedsAttention(run DeliveryRun, logRow DeployLog, key, action, actor string, cause error) (DeliveryRunView, error) {
	message := "蓝绿控制动作未能确认外部状态：" + cause.Error() + "。资源锁保持占用，请先核对集群或恢复稳定版本。"
	logRow.Status = "running"
	logRow.CurrentStep = strPtr("needs_attention")
	logRow.ErrorMessage = &message
	detail := fmt.Sprintf("action=%s;actor=%s;", action, actor)
	if run.Strategy == "blue_green" && (action == DeliveryActionRestoreStable || action == DeliveryActionCancel) {
		detail += "recovery_compatibility=operator_confirmed;"
	}
	detail += message
	_, _, err := s.store.TransitionDeliveryRun(logRow, run.Version, DeliveryRunEvent{
		EventKey: key + ":attention", EventType: "recovery_required", Stage: "needs_attention", Status: "running",
		Detail: detail,
	})
	if err != nil {
		return DeliveryRunView{}, fmt.Errorf("%v；需要人工核对状态也未能持久化: %w", cause, err)
	}
	view, viewErr := s.store.DeliveryRunView(run.ID)
	if viewErr != nil {
		return DeliveryRunView{}, viewErr
	}
	return view, cause
}

func restoreBlueGreenEntryToStable(ctx context.Context, store *Store, runtime KubernetesRuntime, logID int64, plan DeliveryPlan, runID int64) error {
	target := plan.Snapshot.Target
	deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return err
	}
	serviceDef, err := kube.LookupResource("", "v1", "services")
	if err != nil {
		return err
	}
	deployment, err := runtime.GetResource(ctx, deploymentDef, target.Namespace, target.Deployment, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("读取稳定 Deployment 失败: %w", err)
	}
	entry, err := runtime.GetResource(ctx, serviceDef, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("读取生产 Service 失败: %w", err)
	}
	if err := validateBlueGreenEntryService(entry, target.ServiceName, target.ServiceUID, target.ServiceConfigHash); err != nil {
		return err
	}
	if err := validateBlueGreenLiveBaseline(plan, deployment, entry); err != nil {
		return err
	}
	if err := ensureBlueGreenStableLabel(ctx, store, runtime, logID, runID, plan, deploymentDef, deployment); err != nil {
		return err
	}
	rolloutCtx, cancelRollout := context.WithTimeout(ctx, RolloutWaitTimeout)
	_, rolloutErr := runtime.RolloutStatus(rolloutCtx, target.Namespace, target.Deployment)
	cancelRollout()
	if rolloutErr != nil {
		return fmt.Errorf("等待稳定 Deployment 标签 rollout 失败: %w", rolloutErr)
	}
	deployment, err = runtime.GetResource(ctx, deploymentDef, target.Namespace, target.Deployment, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("回读稳定 Deployment 失败: %w", err)
	}
	if err := validateStableDeploymentReady(deployment, target); err != nil {
		return err
	}
	selector, found, err := unstructured.NestedStringMap(entry.Object, "spec", "selector")
	if err != nil || !found {
		return NewValidationError("生产 Service selector 无法读取")
	}
	stableSelector, err := BlueGreenStableServiceSelector(target.ServiceSelector, target.StableLabelKey)
	if err != nil {
		return err
	}
	candidateSelector, err := BlueGreenCandidateSelector(target.CandidateLabelKey, target.CandidateLabelValue)
	if err != nil {
		return err
	}
	if sameStringMap(selector, candidateSelector) {
		if err := restoreBlueGreenServiceToStable(ctx, store, runtime, logID, plan, runID); err != nil {
			return err
		}
	} else if sameStringMap(selector, target.ServiceSelector) {
		if err := ensureBlueGreenStableServiceEntry(ctx, store, runtime, logID, runID, plan, serviceDef, entry, stableSelector); err != nil {
			return err
		}
	} else if sameStringMap(selector, stableSelector) {
		// A previous restore may have reached Kubernetes before the process
		// stopped recording its readback. Resolve that durable intent from the
		// live stable selector before marking the run terminal.
		priorRestore, actionErr := findExecutionAction(store, logID, blueGreenRestoreAction)
		if actionErr != nil {
			return actionErr
		}
		if priorRestore != nil {
			if err := restoreBlueGreenServiceToStable(ctx, store, runtime, logID, plan, runID); err != nil {
				return err
			}
		}
	} else if !sameStringMap(selector, stableSelector) {
		return newConflict("生产 Service selector 已偏离本次稳定与候选轨道")
	}
	if _, err := waitForBlueGreenStableEndpoints(ctx, runtime, target.Namespace, target.ServiceName, target.StableLabelKey); err != nil {
		return err
	}
	return nil
}
