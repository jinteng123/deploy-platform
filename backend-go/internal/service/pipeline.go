package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/config"
)

var loadedImageRe = regexp.MustCompile(`Loaded image:\s*(\S+)`)

// Pipeline executes deploy and rollback flows for queued deploy logs.
// Docker/kubectl commands go through CommandRunner; cluster image updates go
// through the per-cluster KubernetesRuntime. Nothing falls back to mock on
// runtime errors — mock is selected only by cfg.MockRuntime at wiring time.
type Pipeline struct {
	cfg    config.Config
	store  *Store
	runner CommandRunner
	k8s    *K8sService
}

func NewPipeline(cfg config.Config, store *Store, runner CommandRunner, k8s *K8sService) *Pipeline {
	return &Pipeline{cfg: cfg, store: store, runner: runner, k8s: k8s}
}

// ImageFull renders the registry-qualified image reference for a deploy.
func (p *Pipeline) ImageFull(container, version string) string {
	return fmt.Sprintf("%s/%s/%s:%s", p.cfg.RegistryHost, p.cfg.RegistryProject, container, version)
}

// Run dispatches on the log's deploy type. Any persistence failure aborts the
// pipeline and is returned to the caller.
func (p *Pipeline) Run(ctx context.Context, logID int64) error {
	deployLog, err := p.store.FindLog(logID)
	if err != nil {
		return err
	}
	if deployLog.Status == "success" || deployLog.Status == "failed" || deployLog.Status == "interrupted" {
		return nil
	}
	if err := p.checkProductionTask(ctx, deployLog); err != nil {
		return p.fail(&deployLog, nil, "production_policy", err.Error())
	}
	if run, runErr := p.store.DeliveryRunForDeployLog(logID); runErr == nil {
		switch run.Strategy {
		case "blue_green":
			return p.runBlueGreen(ctx, deployLog, run)
		case "gateway_canary":
			return p.fail(&deployLog, nil, "strategy_unavailable", "网关金丝雀执行器尚未完成真实接口联调")
		case "rolling":
			// Continue through the existing rolling Deployment flow.
		default:
			return p.fail(&deployLog, nil, "strategy_invalid", "发布运行策略无效")
		}
	} else if !errors.Is(runErr, ErrNotFound) {
		return runErr
	}
	switch deployLog.DeployType {
	case "rollback":
		return p.runRollback(ctx, deployLog)
	case "image_deploy":
		return p.runImageDeploy(ctx, deployLog)
	default:
		return p.runDeploy(ctx, deployLog)
	}
}

func (p *Pipeline) runBlueGreen(ctx context.Context, deployLog DeployLog, run DeliveryRun) error {
	if err := p.start(&deployLog); err != nil {
		return err
	}
	plan, err := p.store.FindDeliveryPlan(run.DeliveryPlanID)
	if err == nil {
		var cluster Cluster
		cluster, err = p.store.FindCluster(deployLog.ClusterID)
		if err == nil {
			var runtime KubernetesRuntime
			runtime, err = p.k8s.For(cluster)
			if err == nil {
				if err = p.setStep(&deployLog, "prepare_candidate"); err == nil {
					var candidate blueGreenCandidateIdentity
					candidate, err = prepareBlueGreenCandidateWithConfig(ctx, p.store, runtime, deployLog, plan, run.ID, p.cfg)
					if err == nil {
						appendCandidateImageEvidence(&deployLog, candidate.CandidateImageEvidence)
					}
				}
			}
		}
	}
	if err != nil {
		message := "蓝绿候选准备未能安全确认：" + err.Error() + "。资源锁保持占用，请使用运行核对或恢复操作。"
		deployLog.CurrentStep = strPtr("needs_attention")
		deployLog.ErrorMessage = &message
		if persistErr := p.persistDeliveryRunStage(deployLog, "blue-green:needs-attention", "recovery_required", "needs_attention", message); persistErr != nil {
			return fmt.Errorf("蓝绿准备需要人工核对，且状态持久化失败: %w", persistErr)
		}
		return nil
	}
	message := fmt.Sprintf("候选 %s 已就绪，私有预览入口 %s 已通过 EndpointSlice 检查；确认后才会切换生产 Service。", blueGreenResourceName(plan.Snapshot.Target.Deployment, plan.ID), blueGreenResourceName(plan.Snapshot.Target.ServiceName, plan.ID))
	deployLog.CurrentStep = strPtr("awaiting_candidate_review")
	deployLog.ErrorMessage = nil
	return p.persistDeliveryRunStage(deployLog, "blue-green:awaiting-candidate-review", "operator_gate", "awaiting_candidate_review", message)
}

// MarkFailed force-fails a log (e.g. after a worker panic).
func (p *Pipeline) MarkFailed(logID int64, msg string) error {
	deployLog, err := p.store.FindLog(logID)
	if err != nil {
		return err
	}
	if deployLog.Status == "success" || deployLog.Status == "failed" || deployLog.Status == "interrupted" {
		return nil
	}
	now := time.Now()
	deployLog.Status = "failed"
	deployLog.ErrorMessage = strPtr(msg)
	deployLog.FinishedAt = &now
	stage := "worker_panic"
	if deployLog.CurrentStep != nil && *deployLog.CurrentStep != "" {
		stage = *deployLog.CurrentStep
	}
	return p.persistDeliveryRunStage(deployLog, "terminal:failed", "failed", stage, msg)
}

func (p *Pipeline) runDeploy(ctx context.Context, deployLog DeployLog) error {
	if err := p.start(&deployLog); err != nil {
		return err
	}

	imageFull := p.ImageFull(deployLog.ContainerName, deployLog.Version)
	deployLog.ImageFull = &imageFull

	cluster, err := p.store.FindCluster(deployLog.ClusterID)
	if err != nil {
		return p.fail(&deployLog, nil, "check_file", err.Error())
	}

	steps := make([]StepResult, 0, 5)

	if err := p.setStep(&deployLog, "check_file"); err != nil {
		return err
	}
	filePath, ok := SafeTransferFile(p.cfg.TransferDir, deployLog.FileName)
	if !ok {
		step := NewStepResult("check_file", false, "", fmt.Sprintf("非法文件名: %s", deployLog.FileName))
		return p.fail(&deployLog, append(steps, step), "check_file", step.Stderr)
	}
	info, err := os.Lstat(filePath)
	if err != nil || !info.Mode().IsRegular() {
		step := NewStepResult("check_file", false, "", fmt.Sprintf("文件不存在或不是普通文件: %s", filePath))
		return p.fail(&deployLog, append(steps, step), "check_file", step.Stderr)
	}
	sizeMB := math.Round(float64(info.Size())/1048576*100) / 100
	steps = append(steps, NewStepResult("check_file", true,
		fmt.Sprintf("文件已确认: %s (%s MB)", filePath, strconv.FormatFloat(sizeMB, 'f', -1, 64)), ""))

	// docker load reads the archive from stdin
	if err := p.setStep(&deployLog, "docker_load"); err != nil {
		return err
	}
	file, err := os.Open(filePath)
	if err != nil {
		return p.fail(&deployLog, append(steps, NewStepResult("docker_load", false, "", err.Error())), "docker_load", err.Error())
	}
	stdout, stderr, runErr := p.runner.Run(ctx, "docker", []string{"load"}, file)
	file.Close()
	steps = append(steps, NewStepResult("docker_load", runErr == nil, stdout, stderr))
	if runErr != nil {
		return p.fail(&deployLog, steps, "docker_load", errText(stderr, runErr))
	}
	loaded := ""
	if m := loadedImageRe.FindStringSubmatch(stdout); m != nil {
		loaded = m[1]
	}
	if p.cfg.MockRuntime || loaded == "" {
		loaded = deployLog.ContainerName + ":latest"
	}

	if err := p.setStep(&deployLog, "docker_tag"); err != nil {
		return err
	}
	stdout, stderr, runErr = p.runner.Run(ctx, "docker", []string{"tag", loaded, imageFull}, nil)
	if stdout == "" {
		stdout = fmt.Sprintf("%s → %s", loaded, imageFull)
	}
	steps = append(steps, NewStepResult("docker_tag", runErr == nil, stdout, stderr))
	if runErr != nil {
		return p.fail(&deployLog, steps, "docker_tag", errText(stderr, runErr))
	}

	if err := p.setStep(&deployLog, "docker_push"); err != nil {
		return err
	}
	if !p.cfg.MockRuntime {
		// the password only ever travels through stdin, never argv/logs
		_, stderr, runErr = p.runner.Run(ctx, "docker",
			[]string{"login", p.cfg.RegistryHost, "-u", p.cfg.RegistryUser, "--password-stdin"},
			strings.NewReader(p.cfg.RegistryPassword))
		if runErr != nil {
			msg := "镜像仓库登录失败: " + errText(stderr, runErr)
			return p.fail(&deployLog, append(steps, NewStepResult("docker_push", false, "", msg)), "docker_push", msg)
		}
	}
	stdout, stderr, runErr = p.runner.Run(ctx, "docker", []string{"push", imageFull}, nil)
	steps = append(steps, NewStepResult("docker_push", runErr == nil, stdout, stderr))
	if runErr != nil {
		return p.fail(&deployLog, steps, "docker_push", errText(stderr, runErr))
	}

	// The image is now in the registry — pin the cluster update to its
	// digest so what we verified is what runs.
	if step, rerr := p.resolveDeployImage(ctx, &deployLog); rerr != nil {
		return p.fail(&deployLog, append(steps, *step), "resolve_digest", rerr.Error())
	} else if step != nil {
		steps = append(steps, *step)
		imageFull = *deployLog.ImageFull
	}

	if err := p.setStep(&deployLog, "k8s_set_image"); err != nil {
		return err
	}
	rt, err := p.k8s.For(cluster)
	if err != nil {
		return p.fail(&deployLog, append(steps, NewStepResult("k8s_set_image", false, "", err.Error())), "k8s_set_image", err.Error())
	}
	imageStep, unknown, runErr := p.applyImageWithReadback(ctx, rt, deployLog, imageFull)
	steps = append(steps, imageStep)
	if runErr != nil {
		if unknown {
			return p.interrupt(&deployLog, steps, "k8s_set_image", runErr.Error())
		}
		return p.fail(&deployLog, steps, "k8s_set_image", runErr.Error())
	}

	if err := p.setStep(&deployLog, "rollout_wait"); err != nil {
		return err
	}
	rollout, runErr := p.rolloutWait(ctx, rt, deployLog)
	steps = append(steps, rollout)
	if runErr != nil {
		return p.fail(&deployLog, steps, "rollout_wait", runErr.Error())
	}

	if err := p.succeed(&deployLog, steps); err != nil {
		return err
	}
	if err := os.Remove(filePath); err != nil {
		log.Printf("[pipeline] 清理源文件失败 %s: %v", filePath, err)
	}
	return nil
}

// runRollback executes query_current → k8s_set_image. Unlike the Python
// version a failed query_current fails the pipeline.
func (p *Pipeline) runRollback(ctx context.Context, deployLog DeployLog) error {
	if err := p.start(&deployLog); err != nil {
		return err
	}

	cluster, err := p.store.FindCluster(deployLog.ClusterID)
	if err != nil {
		return p.fail(&deployLog, nil, "query_current", err.Error())
	}
	rt, err := p.k8s.For(cluster)
	if err != nil {
		return p.fail(&deployLog, nil, "query_current", err.Error())
	}

	steps := make([]StepResult, 0, 2)
	target := ""
	if deployLog.ImageFull != nil {
		target = *deployLog.ImageFull
	}

	if err := p.setStep(&deployLog, "query_current"); err != nil {
		return err
	}
	current, err := rt.CurrentImage(ctx, deployLog.Namespace, deployLog.DeploymentName, deployLog.ContainerName)
	if err != nil {
		return p.fail(&deployLog, append(steps, NewStepResult("query_current", false, "", err.Error())), "query_current", err.Error())
	}
	steps = append(steps, NewStepResult("query_current", true,
		fmt.Sprintf("当前镜像: %s\n回滚目标: %s", current, target), ""))

	if step, rerr := p.resolveDeployImage(ctx, &deployLog); rerr != nil {
		return p.fail(&deployLog, append(steps, *step), "resolve_digest", rerr.Error())
	} else if step != nil {
		steps = append(steps, *step)
		target = *deployLog.ImageFull
	}

	if err := p.setStep(&deployLog, "k8s_set_image"); err != nil {
		return err
	}
	imageStep, unknown, setErr := p.applyImageWithReadback(ctx, rt, deployLog, target)
	steps = append(steps, imageStep)
	if setErr != nil {
		if unknown {
			return p.interrupt(&deployLog, steps, "k8s_set_image", setErr.Error())
		}
		return p.fail(&deployLog, steps, "k8s_set_image", setErr.Error())
	}

	if err := p.setStep(&deployLog, "rollout_wait"); err != nil {
		return err
	}
	rollout, runErr := p.rolloutWait(ctx, rt, deployLog)
	steps = append(steps, rollout)
	if runErr != nil {
		return p.fail(&deployLog, steps, "rollout_wait", runErr.Error())
	}

	return p.verifyAndSucceed(ctx, rt, &deployLog, steps)
}

// runImageDeploy is the webhook-driven path: the image is already in the
// registry, so the only step is pointing the deployment at image_full.
func (p *Pipeline) runImageDeploy(ctx context.Context, deployLog DeployLog) error {
	if err := p.start(&deployLog); err != nil {
		return err
	}
	cluster, err := p.store.FindCluster(deployLog.ClusterID)
	if err != nil {
		return p.fail(&deployLog, nil, "k8s_set_image", err.Error())
	}
	rt, err := p.k8s.For(cluster)
	if err != nil {
		return p.fail(&deployLog, nil, "k8s_set_image", err.Error())
	}
	var steps []StepResult
	if step, rerr := p.resolveDeployImage(ctx, &deployLog); rerr != nil {
		return p.fail(&deployLog, append(steps, *step), "resolve_digest", rerr.Error())
	} else if step != nil {
		steps = append(steps, *step)
	}

	if err := p.setStep(&deployLog, "k8s_set_image"); err != nil {
		return err
	}
	image := ""
	if deployLog.ImageFull != nil {
		image = *deployLog.ImageFull
	}
	imageStep, unknown, runErr := p.applyImageWithReadback(ctx, rt, deployLog, image)
	steps = append(steps, imageStep)
	if runErr != nil {
		if unknown {
			return p.interrupt(&deployLog, steps, "k8s_set_image", runErr.Error())
		}
		return p.fail(&deployLog, steps, "k8s_set_image", runErr.Error())
	}

	if err := p.setStep(&deployLog, "rollout_wait"); err != nil {
		return err
	}
	rollout, runErr := p.rolloutWait(ctx, rt, deployLog)
	steps = append(steps, rollout)
	if runErr != nil {
		return p.fail(&deployLog, steps, "rollout_wait", runErr.Error())
	}
	return p.verifyAndSucceed(ctx, rt, &deployLog, steps)
}

// resolveDeployImage pins the deploy image to its registry digest so the
// image identity is immutable. CI provenance depends on the inbound digest
// source; registry resolution alone does not prove what CI built. It is a no-op
// for pinned refs and mock runs; mutable foreign registries are rejected. A lookup
// failure on our own registry fails the deploy rather than silently
// shipping the mutable tag.
func (p *Pipeline) resolveDeployImage(ctx context.Context, deployLog *DeployLog) (*StepResult, error) {
	image := ""
	if deployLog.ImageFull != nil {
		image = *deployLog.ImageFull
	}
	if image == "" || p.cfg.MockRuntime {
		return nil, nil
	}
	if strings.Contains(image, "@") {
		if !PinnedImage(image) {
			step := NewStepResult("resolve_digest", false, "", "非法固化镜像引用")
			return &step, NewValidationError("%s", step.Stderr)
		}
		return nil, nil
	}
	host := ImageRegistryHost(image)
	if host == "" || !strings.EqualFold(host, p.registryHost()) {
		step := NewStepResult("resolve_digest", false, "", "未配置的 registry 不能部署可变 tag")
		return &step, NewValidationError("%s", step.Stderr)
	}
	digest, err := ResolveRegistryDigest(ctx, p.cfg, image)
	if err != nil {
		s := NewStepResult("resolve_digest", false, "", err.Error())
		return &s, err
	}
	if NormalizeDigest(digest) == "" {
		step := NewStepResult("resolve_digest", false, "", "registry 返回非法 digest")
		return &step, NewValidationError("%s", step.Stderr)
	}
	ref := DigestRef(image, digest)
	deployLog.ImageFull = &ref
	s := NewStepResult("resolve_digest", true, fmt.Sprintf("%s → %s", image, ref), "")
	return &s, nil
}

func (p *Pipeline) registryHost() string {
	h := strings.TrimPrefix(p.cfg.RegistryHost, "https://")
	return strings.TrimPrefix(h, "http://")
}

// rolloutWait runs the post-set-image health check as the `rollout_wait`
// step: success means the new pods are actually Ready — "deployment rolled
// out" — not merely that the API accepted the image update.
func (p *Pipeline) rolloutWait(ctx context.Context, rt KubernetesRuntime, deployLog DeployLog) (StepResult, error) {
	waitCtx, cancel := context.WithTimeout(ctx, RolloutWaitTimeout)
	defer cancel()
	out, err := rt.RolloutStatus(waitCtx, deployLog.Namespace, deployLog.DeploymentName)
	return NewStepResult("rollout_wait", err == nil, out, errText("", err)), err
}

type imageActionIntent struct {
	PreviousImage string `json:"previous_image"`
	TargetImage   string `json:"target_image"`
}

type imageActionObservation struct {
	CurrentImage string `json:"current_image,omitempty"`
	Readback     string `json:"readback,omitempty"`
}

// applyImageWithReadback journals the write before SetImage, then reads the
// cluster state even when SetImage returns an error. An existing unfinished
// action is reconciled by readback and is never blindly sent a second time.
func (p *Pipeline) applyImageWithReadback(ctx context.Context, rt KubernetesRuntime, deployLog DeployLog, target string) (StepResult, bool, error) {
	step := NewStepResult("k8s_set_image", false, "", "")
	approvedUID, err := p.approvedWorkloadUID(deployLog)
	if err != nil {
		step.Stderr = "无法核对审批计划中的 Deployment 身份"
		return step, false, err
	}
	if p.cfg.MockRuntime {
		approvedUID = ""
	}
	// Reconcile an existing journal entry before constructing a new intent
	// from the live image. This preserves the original baseline after a worker
	// restart and prevents a retry from repeating an uncertain external write.
	priorActions, err := p.store.ExecutionActions(deployLog.ID)
	if err != nil {
		step.Stderr = "无法读取已有动作记录"
		return step, true, fmt.Errorf("镜像动作记录读取失败")
	}
	for _, priorAction := range priorActions {
		if priorAction.ActionKey != "set-image" {
			continue
		}
		var priorIntent imageActionIntent
		if err := json.Unmarshal(priorAction.Intent, &priorIntent); err != nil || priorIntent.TargetImage != target {
			step.Stderr = "已有镜像动作与本次目标不一致"
			return step, true, fmt.Errorf("已有镜像动作意图不一致，需要核对集群")
		}
		observed, err := actionCurrentImage(ctx, rt, deployLog, approvedUID)
		if err != nil {
			step.Stderr = "已有镜像动作无法回读"
			return step, true, fmt.Errorf("已有镜像动作状态未知，需要核对集群")
		}
		observation, _ := json.Marshal(imageActionObservation{CurrentImage: observed, Readback: "retry_reconciliation"})
		if observed == target {
			if priorAction.Status != ExecutionActionApplied {
				if _, err := p.store.CompleteExecutionAction(deployLog.ID, "set-image", ExecutionActionApplied, observation, "target_confirmed_after_retry"); err != nil {
					step.Stderr = "目标镜像已生效，但动作回读无法持久化"
					return step, true, fmt.Errorf("目标镜像已生效，但动作结果持久化失败")
				}
			}
			return NewStepResult("k8s_set_image", true, "已回读确认目标镜像", ""), false, nil
		}
		if priorAction.Status == ExecutionActionApplied || observed != priorIntent.PreviousImage {
			if priorAction.Status != ExecutionActionApplied && priorAction.Status != ExecutionActionUnknown {
				if _, err := p.store.CompleteExecutionAction(deployLog.ID, "set-image", ExecutionActionUnknown, observation, "unexpected_image_after_retry"); err != nil {
					step.Stderr = "镜像状态异常，动作回读无法持久化"
					return step, true, fmt.Errorf("镜像状态异常，需要核对集群")
				}
			}
			step.Stderr = "回读镜像既不是原值也不是目标值"
			return step, true, fmt.Errorf("镜像状态与预期不符，需要核对集群")
		}
		if priorAction.Status != ExecutionActionNotApplied {
			if _, err := p.store.CompleteExecutionAction(deployLog.ID, "set-image", ExecutionActionUnknown, observation, "existing_intent_requires_reconciliation"); err != nil {
				step.Stderr = "未完成动作需要核对，且无法保存回读结果"
				return step, true, fmt.Errorf("未完成镜像动作需要核对")
			}
		}
		step.Stderr = "发现未完成的镜像动作；为避免重复写入已停止执行"
		return step, true, fmt.Errorf("发现未完成的镜像动作，需要先核对集群状态")
	}
	previous, err := actionCurrentImage(ctx, rt, deployLog, approvedUID)
	if err != nil {
		step.Stderr = "无法读取变更前镜像"
		return step, false, fmt.Errorf("变更前镜像读取失败")
	}
	intentBytes, err := json.Marshal(imageActionIntent{PreviousImage: previous, TargetImage: target})
	if err != nil {
		step.Stderr = "无法记录镜像变更意图"
		return step, false, fmt.Errorf("镜像变更意图编码失败")
	}
	_, created, err := p.store.BeginExecutionAction(ExecutionAction{
		DeployLogID: deployLog.ID,
		ActionKey:   "set-image",
		ActionKind:  "set_deployment_image",
		Status:      ExecutionActionIntent,
		Intent:      intentBytes,
	})
	if err != nil {
		step.Stderr = "无法持久化镜像变更意图"
		return step, false, err
	}
	if !created {
		step.Stderr = "镜像动作在核对期间被其他执行器创建；为避免重复写入已停止执行"
		return step, true, fmt.Errorf("镜像动作已由其他执行器创建，需要核对集群状态")
	}
	if previous == target {
		observation, _ := json.Marshal(imageActionObservation{CurrentImage: previous, Readback: "already_at_target"})
		if _, err := p.store.CompleteExecutionAction(deployLog.ID, "set-image", ExecutionActionApplied, observation, "already_at_target"); err != nil {
			step.Stderr = "无法保存目标镜像回读结果"
			return step, true, fmt.Errorf("目标镜像已存在，但动作结果无法持久化")
		}
		return NewStepResult("k8s_set_image", true, "目标镜像已存在，无需写入", ""), false, nil
	}
	var writeErr error
	if approvedUID != "" {
		writeErr = patchApprovedDeploymentImage(ctx, rt, deployLog.Namespace, deployLog.DeploymentName, deployLog.ContainerName, previous, target, approvedUID)
	} else {
		writeErr = rt.SetImage(ctx, deployLog.Namespace, deployLog.DeploymentName, deployLog.ContainerName, target)
	}
	if p.cfg.MockRuntime {
		observation, _ := json.Marshal(imageActionObservation{CurrentImage: target, Readback: "mock_simulated"})
		if _, err := p.store.CompleteExecutionAction(deployLog.ID, "set-image", ExecutionActionApplied, observation, "mock_simulated"); err != nil {
			step.Stderr = "模拟写入后无法记录动作结果"
			return step, true, fmt.Errorf("无法持久化模拟动作结果")
		}
		return NewStepResult("k8s_set_image", true, "模拟镜像变更已确认", ""), false, nil
	}
	observed, readErr := actionCurrentImage(ctx, rt, deployLog, approvedUID)
	if readErr != nil {
		observation, _ := json.Marshal(imageActionObservation{Readback: "failed"})
		if _, err := p.store.CompleteExecutionAction(deployLog.ID, "set-image", ExecutionActionUnknown, observation, "readback_failed"); err != nil {
			step.Stderr = "镜像变更结果未知，动作记录也未能保存"
			return step, true, fmt.Errorf("镜像变更结果未知，需要核对集群")
		}
		step.Stderr = "镜像变更结果未知，集群回读失败"
		return step, true, fmt.Errorf("镜像变更结果未知，需要核对集群")
	}
	observation, _ := json.Marshal(imageActionObservation{CurrentImage: observed, Readback: "completed"})
	if observed == target {
		if _, err := p.store.CompleteExecutionAction(deployLog.ID, "set-image", ExecutionActionApplied, observation, "target_confirmed"); err != nil {
			step.Stderr = "目标镜像已生效，但回读结果未能持久化"
			return step, true, fmt.Errorf("目标镜像已生效，但动作结果持久化失败")
		}
		return NewStepResult("k8s_set_image", true,
			fmt.Sprintf("deployment/%s %s=%s（已回读确认）", deployLog.DeploymentName, deployLog.ContainerName, target), ""), false, nil
	}
	if observed == previous && writeErr != nil {
		if _, err := p.store.CompleteExecutionAction(deployLog.ID, "set-image", ExecutionActionNotApplied, observation, "write_rejected_readback_unchanged"); err != nil {
			step.Stderr = "集群仍是原镜像，但动作结果未能持久化"
			return step, true, fmt.Errorf("镜像写入未确认且动作结果持久化失败")
		}
		step.Stderr = "集群仍是原镜像，写入未生效"
		return step, false, fmt.Errorf("镜像写入失败且回读仍为原镜像")
	}
	if _, err := p.store.CompleteExecutionAction(deployLog.ID, "set-image", ExecutionActionUnknown, observation, "unexpected_image_after_write"); err != nil {
		step.Stderr = "镜像状态与预期不符，动作结果未能持久化"
		return step, true, fmt.Errorf("镜像状态与预期不符，需要核对集群")
	}
	step.Stderr = "回读镜像既不是原值也不是目标值"
	return step, true, fmt.Errorf("镜像状态与预期不符，需要核对集群")
}

func (p *Pipeline) start(deployLog *DeployLog) error {
	now := time.Now()
	deployLog.Status = "running"
	deployLog.StartedAt = &now
	return p.persistDeliveryRunStage(*deployLog, "started", "started", "running", "发布执行器已领取任务。")
}

func (p *Pipeline) setStep(deployLog *DeployLog, step string) error {
	deployLog.CurrentStep = strPtr(step)
	return p.persistDeliveryRunStage(*deployLog, "step:"+step, "stage", step, "执行阶段："+step)
}

func (p *Pipeline) fail(deployLog *DeployLog, steps []StepResult, step, msg string) error {
	now := time.Now()
	deployLog.Status = "failed"
	deployLog.StepDetail = marshalSteps(steps)
	deployLog.ErrorMessage = strPtr(fmt.Sprintf("【%s】%s", step, msg))
	deployLog.FinishedAt = &now
	return p.persistDeliveryRunStage(*deployLog, "terminal:failed", "failed", step, *deployLog.ErrorMessage)
}

func (p *Pipeline) interrupt(deployLog *DeployLog, steps []StepResult, step, msg string) error {
	now := time.Now()
	deployLog.Status = "interrupted"
	deployLog.CurrentStep = strPtr(step)
	deployLog.StepDetail = marshalSteps(steps)
	deployLog.ErrorMessage = strPtr(fmt.Sprintf("【%s】%s", step, msg))
	deployLog.FinishedAt = &now
	return p.persistDeliveryRunStage(*deployLog, "terminal:interrupted", "interrupted", step, *deployLog.ErrorMessage)
}

func (p *Pipeline) succeed(deployLog *DeployLog, steps []StepResult) error {
	now := time.Now()
	deployLog.Status = "success"
	deployLog.CurrentStep = strPtr("done")
	deployLog.StepDetail = marshalSteps(steps)
	deployLog.FinishedAt = &now
	return p.persistDeliveryRunStage(*deployLog, "terminal:success", "completed", "success", "滚动发布已完成并通过 rollout 检查。")
}

func (p *Pipeline) persistDeliveryRunStage(deployLog DeployLog, eventKey, eventType, stage, detail string) error {
	_, err := p.store.UpdateLogExecutionAndRecordDeliveryRunEvent(deployLog, DeliveryRunEvent{
		EventKey: deliveryRunAttemptEventKey(deployLog, eventKey), EventType: eventType, Stage: stage, Status: deployLog.Status, Detail: detail,
	})
	return err
}

// marshalSteps mirrors Python's json.dumps(..., ensure_ascii=False).
func marshalSteps(steps []StepResult) string {
	if steps == nil {
		return "[]"
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(steps); err != nil {
		return "[]"
	}
	return strings.TrimRight(buf.String(), "\n")
}

func errText(stderr string, err error) string {
	if strings.TrimSpace(stderr) != "" {
		return strings.TrimSpace(stderr)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func (p *Pipeline) checkProductionTask(ctx context.Context, l DeployLog) error {
	c, err := p.store.FindCluster(l.ClusterID)
	if err != nil {
		return err
	}
	if l.DeployType == "image_deploy" && l.ReleaseID > 0 {
		a, err := p.store.FindAppByName(l.ClusterID, l.AppName)
		if err != nil {
			return err
		}
		if a.Namespace != l.Namespace || a.DeploymentName != l.DeploymentName || a.ContainerName != l.ContainerName {
			return newForbidden("组件部署绑定已改变，请重新发布")
		}
		if l.ImageFull == nil {
			return newForbidden("任务缺少镜像")
		}
		if err := CheckAppImage(a, *l.ImageFull); err != nil {
			return err
		}
	}
	if c.EnvironmentTier != "production" {
		if l.DeployType == "image_deploy" && l.ReleaseID > 0 {
			r, err := p.store.FindImageRelease(l.ReleaseID)
			if err != nil {
				return err
			}
			if r.TestClusterID != l.ClusterID {
				return newForbidden("生产目标环境策略已改变")
			}
			if c.DeployPolicy != "automatic" {
				return newForbidden("测试目标已停止自动发布")
			}
		}
		return nil
	}
	if l.DeployType == "deploy" {
		return newForbidden("生产环境禁止文件包直接发布")
	}
	if l.ImageFull == nil || !PinnedImage(*l.ImageFull) {
		return newForbidden("生产任务必须引用固化镜像")
	}
	if l.DeployType == "rollback" {
		old, err := p.store.FindLog(l.RollbackTargetID)
		if err != nil || old.ClusterID != l.ClusterID || old.Namespace != l.Namespace || old.DeploymentName != l.DeploymentName || old.ContainerName != l.ContainerName || old.Status != "success" || old.ImageFull == nil || *old.ImageFull != *l.ImageFull {
			return newForbidden("回滚目标不属于成功历史部署")
		}
		return nil
	}
	r, err := p.store.FindImageRelease(l.ReleaseID)
	if err != nil || deployImage(r) != *l.ImageFull {
		return newForbidden("生产任务与制品不一致")
	}
	if !p.cfg.MockRuntime && r.Metadata.DigestSource == "mock" {
		return newForbidden("模拟制品不能发布到真实生产环境")
	}
	app, err := p.store.FindAppByName(l.ClusterID, l.AppName)
	if err != nil {
		return err
	}
	evidencePolicy, err := p.store.DeliveryEnvironmentEvidencePolicyForApp(app.ID)
	if err != nil {
		return err
	}
	if err := r.VerifiedForEnvironment(evidencePolicy, time.Now()); err != nil {
		return err
	}
	if p.cfg.RequiresProductionApproval() {
		for _, req := range p.store.PromotionRequests("approved", 0) {
			if req.DeployLogID != nil && *req.DeployLogID == l.ID {
				if req.Snapshot != promotionSnapshot(r, app, c, evidencePolicy) || req.BaselineLogID != p.store.WorkloadBaseline(l.ClusterID, l.Namespace, l.DeploymentName) {
					return newForbidden("审批快照或生产基线已改变")
				}
				rt, err := p.k8s.For(c)
				if err != nil {
					return err
				}
				readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				configuration, err := templateHash(readCtx, rt, app, p.cfg.MockRuntime)
				if err != nil {
					return err
				}
				if req.TemplateHash == "" || configuration != req.TemplateHash {
					return newForbidden("工作负载配置已改变")
				}
				if req.DeliveryPlanID != nil {
					plan, planErr := p.store.FindDeliveryPlan(*req.DeliveryPlanID)
					if planErr != nil {
						return planErr
					}
					if plan.ReleaseID != l.ReleaseID || plan.TargetClusterID != l.ClusterID || plan.Snapshot.Target.AppName != l.AppName {
						return newForbidden("发布运行与已审批计划不一致")
					}
					if !p.cfg.MockRuntime {
						if err := validateLiveDeliveryConfigurationReferences(readCtx, rt, plan); err != nil {
							return err
						}
					}
				}
				current, err := rt.CurrentImage(readCtx, l.Namespace, l.DeploymentName, l.ContainerName)
				if err != nil {
					return err
				}
				// A restart may observe our own previously applied image.
				if current != req.ExpectedImage && current != *l.ImageFull {
					return newForbidden("生产实际镜像已改变")
				}
				return nil
			}
		}
		return newForbidden("生产任务没有审批决定")
	}
	return nil
}

// verifyAndSucceed detects a rollout of another image before calling a task
// successful. It also records Pod imageID evidence when the container runtime
// exposes a comparable registry digest; unknown runtime formats never turn a
// successful rollout into a failure.
func (p *Pipeline) verifyAndSucceed(ctx context.Context, rt KubernetesRuntime, l *DeployLog, steps []StepResult) error {
	if p.cfg.MockRuntime {
		return p.succeed(l, steps)
	}
	if err := p.setStep(l, "verify_image"); err != nil {
		return err
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	approvedUID, err := p.approvedWorkloadUID(*l)
	if err != nil {
		return p.fail(l, append(steps, NewStepResult("verify_image", false, "", err.Error())), "verify_image", err.Error())
	}
	if p.cfg.MockRuntime {
		approvedUID = ""
	}
	actual, err := actionCurrentImage(readCtx, rt, *l, approvedUID)
	expected := ""
	if l.ImageFull != nil {
		expected = *l.ImageFull
	}
	if err != nil {
		return p.fail(l, append(steps, NewStepResult("verify_image", false, "", err.Error())), "verify_image", err.Error())
	}
	if actual != expected {
		msg := "实际模板镜像与固化部署目标不一致，请确认并发变更"
		return p.fail(l, append(steps, NewStepResult("verify_image", false, fmt.Sprintf("期望: %s\n实际: %s", expected, actual), msg)), "verify_image", msg)
	}
	steps = append(steps, NewStepResult("verify_image", true, "模板镜像已回读一致: "+actual, ""))
	if err := p.setStep(l, "verify_pod_image"); err != nil {
		return err
	}
	observationStatus, observation := observePodImageDigestWithRegistry(readCtx, rt, l.Namespace, l.DeploymentName, l.ContainerName, expected, p.cfg)
	steps = append(steps, StepResult{
		Step: "verify_pod_image", Success: true, ObservationStatus: observationStatus,
		Stdout: tailBytes(observation, 2000),
	})
	return p.succeed(l, steps)
}
