package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"deploy-platform/backend-go/internal/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

const (
	DeliveryActionCleanupCandidate = "cleanup_candidate"
	blueGreenCleanupLeaseDuration  = 15 * time.Minute
	blueGreenCleanupTimeout        = 10 * time.Minute
	blueGreenCleanupServiceAction  = "blue-green:cleanup:candidate-service"
	blueGreenCleanupDeployAction   = "blue-green:cleanup:candidate-deployment"
)

// cleanupRestoredBlueGreenCandidate removes only the two resources created by
// this plan after the stable track has been restored and the immutable
// retention period has elapsed. The original Deployment is never a target.
func cleanupRestoredBlueGreenCandidate(ctx context.Context, store *Store, runtime KubernetesRuntime, run DeliveryRun, plan DeliveryPlan, log DeployLog) error {
	cleanupCtx, cancel := context.WithTimeout(ctx, blueGreenCleanupTimeout)
	defer cancel()
	ctx = cleanupCtx
	if store == nil || runtime == nil || (run.Status != "failed" && run.Status != "cancelled") || run.CurrentStage != "restored" || log.Status != "failed" || log.CurrentStep == nil || *log.CurrentStep != "restored" {
		return NewValidationError("只有已恢复稳定版本的蓝绿运行可以清理候选")
	}
	retentionHours := plan.Snapshot.CandidateRetentionHours
	if retentionHours <= 0 || retentionHours > 24*30 {
		return NewValidationError("发布计划没有有效的候选保留策略")
	}
	if log.FinishedAt == nil || time.Since(*log.FinishedAt) < time.Duration(retentionHours)*time.Hour {
		return NewValidationError("候选资源须在稳定版本恢复后保留 %d 小时", retentionHours)
	}
	target := plan.Snapshot.Target
	locks, err := BlueGreenPlanLocks(plan)
	if err != nil {
		return err
	}
	locks = append(locks, ResourceLock{ClusterID: target.ClusterID, Namespace: target.Namespace, Kind: "deployment", Name: target.Deployment})
	leaseUntil := time.Now().Add(blueGreenCleanupLeaseDuration)
	releaseLease, err := store.reserveResourceLockLease(log.ID, locks, leaseUntil)
	if err != nil {
		return err
	}
	releaseLeaseOnExit := true
	defer func() {
		if releaseLeaseOnExit {
			_ = releaseLease()
		}
	}()

	app, err := store.FindAppByName(plan.TargetClusterID, target.AppName)
	if err != nil {
		return err
	}
	if app.ID != plan.TargetAppID || app.DeploymentName != target.Deployment {
		return newConflict("应用活动绑定没有指向已恢复的稳定 Deployment")
	}
	deploymentDef, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return err
	}
	serviceDef, err := kube.LookupResource("", "v1", "services")
	if err != nil {
		return err
	}
	stableDeployment, err := runtime.GetResource(ctx, deploymentDef, target.Namespace, target.Deployment, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("读取恢复后的稳定 Deployment 失败: %w", err)
	}
	entryService, err := runtime.GetResource(ctx, serviceDef, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("读取生产 Service 失败: %w", err)
	}
	if err := validateBlueGreenEntryService(entryService, target.ServiceName, target.ServiceUID, target.ServiceConfigHash); err != nil {
		return err
	}
	if err := validateBlueGreenLiveBaseline(plan, stableDeployment, entryService); err != nil {
		return err
	}
	if err := validateStableDeploymentReady(stableDeployment, target); err != nil {
		return err
	}
	stableSelector, err := BlueGreenStableServiceSelector(target.ServiceSelector, target.StableLabelKey)
	if err != nil {
		return err
	}
	entrySelector, found, err := unstructured.NestedStringMap(entryService.Object, "spec", "selector")
	if err != nil || !found || !sameStringMap(entrySelector, stableSelector) {
		return newConflict("生产 Service 未指向计划记录的稳定轨道，停止候选清理")
	}
	if _, err := waitForBlueGreenStableEndpoints(ctx, runtime, target.Namespace, target.ServiceName, target.StableLabelKey); err != nil {
		return fmt.Errorf("稳定端点尚未确认，停止候选清理: %w", err)
	}

	deploymentName := blueGreenResourceName(target.Deployment, plan.ID)
	serviceName := blueGreenResourceName(target.ServiceName, plan.ID)
	deploymentUID, err := recordedBlueGreenCandidateUID(store, log.ID, blueGreenCandidateDeploy, deploymentName)
	if err != nil {
		return err
	}
	serviceUID, err := recordedBlueGreenCandidateUID(store, log.ID, blueGreenCandidateService, serviceName)
	if err != nil {
		return err
	}
	// Once a delete may have reached Kubernetes, keep the bounded lease on any
	// uncertain outcome. A later operator retry can proceed after the lease
	// expires, while other releases remain fenced during the ambiguity window.
	releaseLeaseOnExit = false
	if err := deleteOwnedBlueGreenCandidate(ctx, store, runtime, log.ID, run.ID, plan.ID, target.Namespace, serviceDef, serviceName, serviceUID, "Service", blueGreenCleanupServiceAction); err != nil {
		return err
	}
	// Re-read the stable entry immediately before deleting the candidate
	// workload; selector drift must stop all destructive work.
	entryService, err = runtime.GetResource(ctx, serviceDef, target.Namespace, target.ServiceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("删除候选 Deployment 前回读生产 Service 失败: %w", err)
	}
	entrySelector, found, err = unstructured.NestedStringMap(entryService.Object, "spec", "selector")
	if err != nil || !found || entryService.GetUID() != types.UID(target.ServiceUID) || !sameStringMap(entrySelector, stableSelector) {
		return newConflict("候选 Service 已清理，但生产入口身份或稳定 selector 已变化；候选 Deployment 保留")
	}
	if err := deleteOwnedBlueGreenCandidate(ctx, store, runtime, log.ID, run.ID, plan.ID, target.Namespace, deploymentDef, deploymentName, deploymentUID, "Deployment", blueGreenCleanupDeployAction); err != nil {
		return err
	}
	releaseLeaseOnExit = true
	return nil
}

func recordedBlueGreenCandidateUID(store *Store, logID int64, actionKey, expectedName string) (types.UID, error) {
	action, err := findExecutionAction(store, logID, actionKey)
	if err != nil {
		return "", err
	}
	if action == nil || action.Status != ExecutionActionApplied {
		return "", newConflict("候选资源没有可验证的创建记录，拒绝清理")
	}
	var observed blueGreenActionObservation
	if err := json.Unmarshal(action.Observed, &observed); err != nil || observed.Name != expectedName || observed.UID == "" {
		return "", newConflict("候选资源创建记录缺少匹配的名称或 UID，拒绝清理")
	}
	return types.UID(observed.UID), nil
}

func deleteOwnedBlueGreenCandidate(ctx context.Context, store *Store, runtime KubernetesRuntime, logID, runID, planID int64, namespace string, def kube.ResourceDef, name string, uid types.UID, kind, actionKey string) error {
	intent := blueGreenActionIntent{PlanID: planID, ExecutionID: runID, Namespace: namespace, Kind: kind, Name: name, UID: string(uid)}
	if err := beginBlueGreenAction(store, logID, actionKey, "delete_candidate_"+kind, intent); err != nil {
		return err
	}
	prior, err := findExecutionAction(store, logID, actionKey)
	if err != nil {
		return err
	}
	if prior != nil && prior.Status == ExecutionActionApplied {
		current, getErr := runtime.GetResource(ctx, def, namespace, name, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			return nil
		}
		if getErr != nil {
			return fmt.Errorf("回读已清理候选 %s 失败: %w", kind, getErr)
		}
		if current.GetUID() != uid {
			return newConflict("已清理候选名称出现了不同 UID 的新资源，拒绝删除")
		}
		return newConflict("候选清理已记录完成，但资源仍存在，拒绝重复删除")
	}

	current, getErr := runtime.GetResource(ctx, def, namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(getErr) {
		return completeBlueGreenAction(store, logID, actionKey, ExecutionActionApplied, blueGreenActionObservation{Name: name, UID: string(uid)}, "candidate_already_absent")
	}
	if getErr != nil {
		_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionUnknown, blueGreenActionObservation{Name: name, UID: string(uid)}, "candidate_delete_read_failed")
		return fmt.Errorf("读取候选 %s 失败: %w", kind, getErr)
	}
	if current.GetNamespace() != namespace || current.GetName() != name || current.GetUID() != uid || !blueGreenCandidateOwnedBy(current, planID, runID) {
		_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionUnknown, blueGreenActionObservation{Name: current.GetName(), UID: string(current.GetUID())}, "candidate_ownership_mismatch")
		return newConflict("候选资源 UID 或平台所有权标签已变化，拒绝删除")
	}
	uidPrecondition := uid
	propagation := metav1.DeletePropagationForeground
	deleteErr := runtime.DeleteResource(ctx, def, namespace, name, metav1.DeleteOptions{
		Preconditions:     &metav1.Preconditions{UID: &uidPrecondition},
		PropagationPolicy: &propagation,
	})
	readback, readErr := runtime.GetResource(ctx, def, namespace, name, metav1.GetOptions{})
	if apierrors.IsNotFound(readErr) {
		if err := completeBlueGreenAction(store, logID, actionKey, ExecutionActionApplied, blueGreenActionObservation{Name: name, UID: string(uid)}, "candidate_delete_confirmed"); err != nil {
			return fmt.Errorf("候选 %s 已删除，但动作回读无法持久化: %w", kind, err)
		}
		return nil
	}
	if readErr != nil {
		_ = completeBlueGreenAction(store, logID, actionKey, ExecutionActionUnknown, blueGreenActionObservation{Name: name, UID: string(uid)}, "candidate_delete_readback_failed")
		return fmt.Errorf("候选 %s 删除结果未知，回读失败: %w", kind, readErr)
	}
	status, outcome := ExecutionActionUnknown, "candidate_delete_pending"
	if deleteErr != nil && readback.GetUID() == uid {
		status, outcome = ExecutionActionNotApplied, "candidate_delete_rejected"
	}
	_ = completeBlueGreenAction(store, logID, actionKey, status, blueGreenObservationForObject(readback, kind, ""), outcome)
	if deleteErr != nil {
		return fmt.Errorf("删除候选 %s 失败: %w", kind, deleteErr)
	}
	return newConflict("候选 %s 尚未完全删除，已保留资源锁等待核对", kind)
}

func blueGreenCandidateOwnedBy(object *unstructured.Unstructured, planID, runID int64) bool {
	if object == nil {
		return false
	}
	labelsMap := object.GetLabels()
	return labelsMap[blueGreenManagedLabel] == "deploy-platform" &&
		labelsMap[blueGreenPlanLabel] == strconv.FormatInt(planID, 10) &&
		labelsMap[blueGreenRunLabel] == strconv.FormatInt(runID, 10)
}
