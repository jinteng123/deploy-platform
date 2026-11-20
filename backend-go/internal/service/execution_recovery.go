package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// InterruptedDeployLogResumer atomically reopens an interrupted log and
// reacquires its Deployment lock. Production MySQL must provide this contract.
type InterruptedDeployLogResumer interface {
	ReopenInterruptedDeployLog(int64) (DeployLog, error)
}

// ReopenInterruptedDeployLog reserves the original target before any live
// readback. The caller must either queue the task or put it back into the
// interrupted terminal state, which releases the reservation.
func (s *Store) ReopenInterruptedDeployLog(id int64) (DeployLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.logs {
		if s.logs[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return DeployLog{}, ErrNotFound
	}
	current := s.logs[idx]
	if current.Status != "interrupted" {
		return DeployLog{}, newConflict("只有中断的发布任务可以核对恢复")
	}
	locks, err := NormalizeResourceLocks([]ResourceLock{workloadResourceLock(current)})
	if err != nil {
		return DeployLog{}, err
	}
	if lock, owner, ok := s.conflictingResourceLockLocked(locks); ok {
		return DeployLog{}, newConflict("资源 %s/%s/%s 正由发布任务 %d 占用", lock.Namespace, lock.Kind, lock.Name, owner)
	}
	updated := current
	updated.Status = "pending"
	updated.CurrentStep = nil
	updated.StepDetail = "[]"
	updated.ErrorMessage = nil
	updated.StartedAt = nil
	updated.FinishedAt = nil
	if s.persistence != nil {
		repo, ok := s.persistence.(InterruptedDeployLogResumer)
		if !ok {
			return DeployLog{}, fmt.Errorf("持久化存储不支持安全恢复")
		}
		updated, err = repo.ReopenInterruptedDeployLog(id)
		if err != nil {
			return DeployLog{}, err
		}
	}
	s.logs[idx] = updated
	s.addResourceLocksLocked(id, locks)
	return updated, nil
}

// ReconcileInterrupted reads the live image under a reacquired resource lock.
// It resumes only when the planned target is already present; it never repeats
// the external write. Other observations are recorded and remain interrupted.
func (p *Pipeline) ReconcileInterrupted(ctx context.Context, logID int64) error {
	logRow, err := p.store.FindLog(logID)
	if err != nil {
		return err
	}
	if logRow.Status != "interrupted" {
		return newConflict("发布任务当前不是中断状态")
	}
	actions, err := p.store.ExecutionActions(logID)
	if err != nil {
		return err
	}
	var action *ExecutionAction
	for i := range actions {
		if actions[i].ActionKey == "set-image" {
			action = &actions[i]
			break
		}
	}
	if action == nil {
		return NewValidationError("该中断任务没有可核对的镜像写入动作")
	}
	var intent imageActionIntent
	if err := json.Unmarshal(action.Intent, &intent); err != nil || intent.TargetImage == "" {
		return NewValidationError("镜像动作意图不完整，不能恢复")
	}
	if action.Status == ExecutionActionNotApplied {
		return newConflict("上次镜像写入已确认未生效；请重新生成发布计划后再试")
	}

	logRow, err = p.store.ReopenInterruptedDeployLog(logID)
	if err != nil {
		return err
	}
	interrupt := func(step StepResult, message string) error {
		if persistErr := p.interrupt(&logRow, []StepResult{step}, "reconcile_image", message); persistErr != nil {
			return persistErr
		}
		return newConflict("%s", message)
	}
	cluster, err := p.store.FindCluster(logRow.ClusterID)
	if err != nil {
		step := NewStepResult("reconcile_image", false, "", "无法读取目标集群配置")
		return interrupt(step, "恢复核对失败，需要人工检查集群")
	}
	rt, err := p.k8s.For(cluster)
	if err != nil {
		step := NewStepResult("reconcile_image", false, "", "无法连接目标集群")
		return interrupt(step, "恢复核对失败，需要人工检查集群")
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	observed, err := rt.CurrentImage(readCtx, logRow.Namespace, logRow.DeploymentName, logRow.ContainerName)
	if err != nil {
		step := NewStepResult("reconcile_image", false, "", "集群镜像回读失败")
		return interrupt(step, "集群镜像仍无法读取，任务保持中断")
	}
	observation, _ := json.Marshal(imageActionObservation{CurrentImage: observed, Readback: "operator_reconciliation"})
	step := NewStepResult("reconcile_image", observed == intent.TargetImage,
		fmt.Sprintf("期望镜像: %s\n观察镜像: %s", intent.TargetImage, observed), "")
	if observed == intent.TargetImage {
		if action.Status != ExecutionActionApplied {
			if _, err := p.store.CompleteExecutionAction(logID, "set-image", ExecutionActionApplied, observation, "operator_reconciled_target"); err != nil {
				step.Success = false
				step.Stderr = "无法保存核对结果"
				return interrupt(step, "已观察到目标镜像，但回读结果未能持久化")
			}
		}
		return nil
	}
	if observed == intent.PreviousImage && action.Status != ExecutionActionApplied {
		if _, err := p.store.CompleteExecutionAction(logID, "set-image", ExecutionActionNotApplied, observation, "operator_reconciled_previous"); err != nil {
			step.Success = false
			step.Stderr = "无法保存核对结果"
			return interrupt(step, "仍是原镜像，但核对结果未能持久化")
		}
		return interrupt(step, "集群仍是原镜像；没有重放写入，请重新生成发布计划")
	}
	if action.Status != ExecutionActionApplied {
		if _, err := p.store.CompleteExecutionAction(logID, "set-image", ExecutionActionUnknown, observation, "operator_reconciled_unexpected_image"); err != nil {
			step.Success = false
			step.Stderr = "无法保存核对结果"
			return interrupt(step, "镜像状态异常且核对结果未能持久化")
		}
	}
	return interrupt(step, "集群镜像既不是计划目标也不是原镜像；请核对外部变更")
}
