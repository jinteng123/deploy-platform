package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"

	"deploy-platform/backend-go/internal/config"
	"deploy-platform/backend-go/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CDService owns the image-event driven release flow: webhook events deploy
// to the test cluster automatically; production is a gated promotion of the
// exact same image_full — a manually entered prod image is never accepted.
type CDService struct {
	cfg           config.Config
	store         *Store
	worker        *Worker
	resolveDigest func(context.Context, config.Config, string) (string, error)
}

func NewCDService(cfg config.Config, store *Store, worker *Worker) *CDService {
	return &CDService{cfg: cfg, store: store, worker: worker, resolveDigest: ResolveRegistryDigest}
}

// SubmitDeployLog queues a deploy log and, when submission fails, records the
// failed state so the log does not stay pending forever. Both failures are
// reported together. It is the service-level equivalent of the HTTP
// submitJob path.
func SubmitDeployLog(ctx context.Context, w *Worker, s *Store, l DeployLog) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := w.Submit(cctx, l.ID); err != nil {
		msg := fmt.Sprintf("任务提交失败: %v", err)
		now := time.Now()
		l.Status = "failed"
		l.ErrorMessage = &msg
		l.FinishedAt = &now
		if _, perr := s.UpdateLogExecutionAndRecordDeliveryRunEvent(l, DeliveryRunEvent{
			EventKey: "submission:failed", EventType: "failed", Stage: "submit", Status: "failed", Detail: msg,
		}); perr != nil {
			return fmt.Errorf("%s; 状态记录失败: %v", msg, perr)
		}
		return errors.New(msg)
	}
	return nil
}

// RecoverDeploys safely resumes tasks that never left the durable pending
// state. Any running task has crossed the execution boundary, so its external
// side effects may be ambiguous after a process restart; mark it interrupted
// and release its resource ownership for explicit operator reconciliation.
func RecoverDeploys(ctx context.Context, store *Store, worker *Worker) error {
	for _, l := range store.Logs(0, "", "", 0) {
		if l.Status != "pending" && l.Status != "running" {
			continue
		}
		if l.Status == "pending" {
			if err := worker.Submit(ctx, l.ID); err != nil {
				return fmt.Errorf("恢复待执行发布任务 log_id=%d 失败: %w", l.ID, err)
			}
			continue
		}
		if run, err := store.DeliveryRunForDeployLog(l.ID); err == nil && run.Strategy == "blue_green" {
			if run.CurrentStage == "needs_attention" {
				// Keep an existing operator reconciliation state and its lock stable
				// across subsequent restarts.
				continue
			}
			operatorBarrier := run.CurrentStage == "awaiting_candidate_review" || run.CurrentStage == "awaiting_live_verification"
			if operatorBarrier {
				actions, actionErr := store.ExecutionActions(l.ID)
				if actionErr != nil {
					return fmt.Errorf("读取蓝绿运行 log_id=%d 的外部动作失败: %w", l.ID, actionErr)
				}
				unresolvedAction := false
				for _, action := range actions {
					if action.Status == ExecutionActionIntent || action.Status == ExecutionActionUnknown {
						unresolvedAction = true
						break
					}
				}
				if !unresolvedAction {
					// A barrier without an in-flight external write is safe to keep.
					continue
				}
			}
			message := "进程在蓝绿阶段执行期间重启；候选资源或入口切换结果需要重新读取确认"
			if operatorBarrier {
				message = "进程在蓝绿操作员关口执行外部资源写入期间重启；动作结果可能未知，需要回读确认"
			}
			l.CurrentStep = strPtr("needs_attention")
			l.ErrorMessage = &message
			event := DeliveryRunEvent{
				EventKey:  fmt.Sprintf("%s:run-version:%d", deliveryRunAttemptEventKey(l, "recovery:blue-green-needs-attention"), run.Version),
				EventType: "recovery_required", Stage: "needs_attention", Status: "running", Detail: message,
			}
			if _, _, err := store.TransitionDeliveryRun(l, run.Version, event); err != nil {
				return fmt.Errorf("标记蓝绿运行 log_id=%d 需要核对失败: %w", l.ID, err)
			}
			continue
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("读取发布运行 log_id=%d 失败: %w", l.ID, err)
		}
		l.Status = "interrupted"
		msg := "进程在任务执行期间重启，外部变更结果未知；请先核对集群实际状态，再显式重试或恢复"
		now := time.Now()
		l.ErrorMessage = &msg
		l.FinishedAt = &now
		if _, err := store.UpdateLogExecutionAndRecordDeliveryRunEvent(l, DeliveryRunEvent{
			EventKey: deliveryRunAttemptEventKey(l, "recovery:process-restart"), EventType: "interrupted", Stage: interruptionStage(l), Status: "interrupted", Detail: msg,
		}); err != nil {
			return fmt.Errorf("标记中断任务 log_id=%d 失败: %w", l.ID, err)
		}
	}
	return nil
}

// EventKey is the idempotency key: hex(sha256(source + ":" + eventID + ":"
// + targetCluster)) — the same build may legitimately deploy to different
// target clusters.
func EventKey(source, eventID, targetCluster string) string {
	sum := sha256.Sum256([]byte(source + ":" + eventID + ":" + targetCluster))
	return hex.EncodeToString(sum[:])
}

// ImageVersion derives the release version from an image reference: the tag
// after the last ":" of the last path segment, or the first 12 chars of a
// sha256 digest. Tags must be non-empty and whitespace-free; digests must be
// exactly 64 hex characters.
func ImageVersion(image string) (string, error) {
	if strings.Count(image, "@") > 1 || strings.Contains(image, "://") || strings.ContainsAny(image, " \t\r\n?#") {
		return "", NewValidationError("image_full 不是合法的镜像引用")
	}
	if i := strings.Index(image, "@sha256:"); i >= 0 {
		d := image[i+len("@sha256:"):]
		if NormalizeDigest("sha256:"+d) != "" {
			return d[:12], nil
		}
		return "", NewValidationError("镜像 digest 非法: %s", image)
	}
	last := image[strings.LastIndex(image, "/")+1:]
	if i := strings.LastIndex(last, ":"); i >= 0 {
		if tag := last[i+1:]; tag != "" && strings.IndexFunc(tag, unicode.IsSpace) < 0 {
			return tag, nil
		}
	}
	return "", NewValidationError("无法从镜像解析版本: %s", image)
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// NormalizeDigest canonicalizes a digest string: a bare 64-hex sha256 gains
// the "sha256:" prefix; "algo:hex" forms lower-case. "" means unusable.
func NormalizeDigest(d string) string {
	d = strings.TrimSpace(d)
	d = strings.TrimPrefix(strings.ToLower(d), "sha256:")
	if len(d) != 64 || !isHex(d) {
		return ""
	}
	return "sha256:" + d
}

// DigestRef renders the deploy-by-digest reference: any tag/digest on
// imageFull is stripped and "@"+digest appended. Returns "" when the digest
// is unusable — callers decide whether a tag fallback is acceptable.
func DigestRef(imageFull, digest string) string {
	d := NormalizeDigest(digest)
	if d == "" {
		return ""
	}
	repo := imageFull
	if i := strings.Index(repo, "@"); i >= 0 {
		repo = repo[:i]
	}
	last := repo[strings.LastIndex(repo, "/")+1:]
	if i := strings.LastIndex(last, ":"); i >= 0 {
		repo = repo[:len(repo)-len(last)+i]
	}
	return repo + "@" + d
}

func trimEventPtr(p *string) *string {
	if p == nil {
		return nil
	}
	s := strings.TrimSpace(*p)
	if s == "" {
		return nil
	}
	return &s
}

func normalizeBuildSourcePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 512 || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || path.Clean(value) != value {
		return "", NewValidationError("构建事件中的 SOURCE_PATH 必须是仓库内相对目录")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", NewValidationError("构建事件中的 SOURCE_PATH 不能越出代码仓库")
		}
	}
	return value, nil
}

func (e ImageEvent) normalized() ImageEvent {
	e.EventID = strings.TrimSpace(e.EventID)
	e.Source = strings.TrimSpace(e.Source)
	e.TargetCluster = strings.TrimSpace(e.TargetCluster)
	e.AppName = strings.TrimSpace(e.AppName)
	e.ImageFull = strings.TrimSpace(e.ImageFull)
	e.SourcePath = strings.TrimSpace(e.SourcePath)
	e.PipelineID = trimEventPtr(e.PipelineID)
	e.PipelineName = trimEventPtr(e.PipelineName)
	e.BuildNumber = trimEventPtr(e.BuildNumber)
	e.Branch = trimEventPtr(e.Branch)
	e.CommitID = trimEventPtr(e.CommitID)
	if e.CommitTimestamp != nil {
		if e.CommitTimestamp.IsZero() {
			e.CommitTimestamp = nil
		} else {
			commitTimestamp := e.CommitTimestamp.UTC()
			e.CommitTimestamp = &commitTimestamp
		}
	}
	e.ImageDigest = trimEventPtr(e.ImageDigest)
	for i := range e.SourceRepositories {
		e.SourceRepositories[i] = strings.TrimSpace(e.SourceRepositories[i])
	}
	return e
}

// Receive records a webhook image event, creates the pending test deploy log
// and queues it. A duplicate event returns the stored release with
// created=false and never queues a second task. When queue submission fails
// the release is still stored (auditable) and the error is returned.
func (s *CDService) Receive(ctx context.Context, ev ImageEvent) (ImageRelease, DeployLog, bool, error) {
	return s.receive(ctx, ev, nil)
}

func (s *CDService) ReceiveRunner(ctx context.Context, binding RunnerBinding, ev ImageEvent) (ImageRelease, DeployLog, bool, error) {
	return s.receive(ctx, ev, &binding)
}

func (s *CDService) receive(ctx context.Context, ev ImageEvent, runner *RunnerBinding) (ImageRelease, DeployLog, bool, error) {
	ev = ev.normalized()
	if ev.Source == "" {
		ev.Source = "yunxiao"
	}
	if ev.EventID == "" {
		return ImageRelease{}, DeployLog{}, false, NewValidationError("event_id 不能为空")
	}
	if ev.TargetCluster == "" {
		return ImageRelease{}, DeployLog{}, false, NewValidationError("target_cluster 不能为空")
	}
	if ev.AppName == "" {
		return ImageRelease{}, DeployLog{}, false, NewValidationError("app_name 不能为空")
	}
	if ev.ImageFull == "" {
		return ImageRelease{}, DeployLog{}, false, NewValidationError("image_full 不能为空")
	}
	if strings.IndexFunc(ev.ImageFull, unicode.IsSpace) >= 0 {
		return ImageRelease{}, DeployLog{}, false, NewValidationError("image_full 不能包含空白字符")
	}
	// A supplied digest must be usable — deploying the tag anyway would
	// silently ship unverified bytes.
	if ev.ImageDigest != nil {
		if d := NormalizeDigest(*ev.ImageDigest); d != "" {
			ev.ImageDigest = &d
		} else {
			return ImageRelease{}, DeployLog{}, false, NewValidationError("image_digest 非法: %s", *ev.ImageDigest)
		}
	}
	version, err := ImageVersion(ev.ImageFull)
	if err != nil {
		return ImageRelease{}, DeployLog{}, false, err
	}
	// image_releases column limits — reject before any insert.
	ptr := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	for _, f := range []struct {
		name, v string
		max     int
	}{
		{"event_id", ev.EventID, 255}, {"source", ev.Source, 32},
		{"app_name", ev.AppName, 128}, {"image_full", ev.ImageFull, 512},
		{"version", version, 128},
		{"pipeline_id", ptr(ev.PipelineID), 128}, {"pipeline_name", ptr(ev.PipelineName), 255},
		{"build_number", ptr(ev.BuildNumber), 64}, {"branch", ptr(ev.Branch), 255},
		{"commit_id", ptr(ev.CommitID), 128}, {"image_digest", ptr(ev.ImageDigest), 255},
	} {
		if len(f.v) > f.max {
			return ImageRelease{}, DeployLog{}, false, NewValidationError("%s 超过 %d 字符限制", f.name, f.max)
		}
	}
	cluster, err := s.store.FindClusterByEnvCode(ev.TargetCluster)
	if err != nil {
		return ImageRelease{}, DeployLog{}, false, err
	}
	if !cluster.Enabled {
		return ImageRelease{}, DeployLog{}, false, NewValidationError("目标集群 %s 已禁用", ev.TargetCluster)
	}
	if cluster.EnvironmentTier != "non_production" {
		return ImageRelease{}, DeployLog{}, false, NewValidationError("目标集群 %s 不是非生产环境", ev.TargetCluster)
	}
	if cluster.DeployPolicy != "automatic" {
		return ImageRelease{}, DeployLog{}, false, NewValidationError("目标集群 %s 不允许自动部署", ev.TargetCluster)
	}
	app, err := s.store.FindAppByName(cluster.ID, ev.AppName)
	if err != nil {
		return ImageRelease{}, DeployLog{}, false, err
	}
	app = NormalizeAppSources(app)
	sourceRepositories, sourceRepositoryIdentities, err := normalizeSourceRepositories(ev.SourceRepositories)
	if err != nil {
		return ImageRelease{}, DeployLog{}, false, err
	}
	ev.SourceRepositories = sourceRepositories
	ev.SourcePath, err = normalizeBuildSourcePath(ev.SourcePath)
	if err != nil {
		return ImageRelease{}, DeployLog{}, false, err
	}
	if app.SourceKind == "repository" && app.SourceRepoURL != "" {
		if ev.Source == "yunxiao" && len(sourceRepositoryIdentities) == 0 {
			return ImageRelease{}, DeployLog{}, false, NewValidationError("云效事件未提供 sources.repo，无法核对组件登记的代码仓库")
		}
		if len(sourceRepositoryIdentities) > 0 {
			_, expectedRepository, identityErr := sourceRepositoryIdentity(app.SourceRepoURL)
			if identityErr != nil {
				return ImageRelease{}, DeployLog{}, false, NewValidationError("组件登记的代码仓库地址无效，无法核对构建来源")
			}
			if _, matches := sourceRepositoryIdentities[expectedRepository]; !matches {
				return ImageRelease{}, DeployLog{}, false, NewValidationError("构建代码仓库与组件登记不一致，请检查 APP_NAME 和云效代码源配置")
			}
		}
		expectedSourcePath, pathErr := normalizeBuildSourcePath(app.SourcePath)
		if pathErr != nil {
			return ImageRelease{}, DeployLog{}, false, NewValidationError("组件登记的构建目录无效，无法核对构建来源")
		}
		if expectedSourcePath == "" {
			expectedSourcePath = "."
		}
		if expectedSourcePath != "." && (ev.Source == "yunxiao" || len(sourceRepositoryIdentities) > 0) && ev.SourcePath == "" {
			return ImageRelease{}, DeployLog{}, false, NewValidationError("该组件登记了子目录；构建事件需设置 SOURCE_PATH 并与组件构建目录一致")
		}
		if ev.SourcePath != "" && ev.SourcePath != expectedSourcePath {
			return ImageRelease{}, DeployLog{}, false, NewValidationError("构建目录与组件登记不一致，请检查 SOURCE_PATH 和组件绑定")
		}
	}
	if s.store.AppEnvironmentTier(app.ID) != "non_production" {
		return ImageRelease{}, DeployLog{}, false, NewValidationError("组件绑定的逻辑环境不是非生产环境")
	}
	if err := CheckAppImage(app, ev.ImageFull); err != nil {
		return ImageRelease{}, DeployLog{}, false, err
	}
	key := EventKey(ev.Source, ev.EventID, ev.TargetCluster)
	if old, ok := s.store.ReleaseByEventKey(key); ok && runner == nil {
		l, err := s.store.FindLog(old.TestDeployLogID)
		return old, l, false, err
	}
	digestSource := "ci"
	if i := strings.Index(ev.ImageFull, "@"); i >= 0 {
		d := NormalizeDigest(ev.ImageFull[i+1:])
		if d == "" || (ev.ImageDigest != nil && *ev.ImageDigest != d) {
			return ImageRelease{}, DeployLog{}, false, NewValidationError("镜像引用与 image_digest 不一致或非法")
		}
		ev.ImageDigest = &d
	}
	if ev.ImageDigest == nil {
		var d string
		if s.cfg.MockRuntime {
			sum := sha256.Sum256([]byte(ev.ImageFull))
			d = "sha256:" + hex.EncodeToString(sum[:])
			digestSource = "mock"
		} else {
			host := strings.TrimPrefix(strings.TrimPrefix(s.cfg.RegistryHost, "https://"), "http://")
			if !strings.EqualFold(ImageRegistryHost(ev.ImageFull), host) {
				return ImageRelease{}, DeployLog{}, false, NewValidationError("未配置的 registry 必须由 CI 提供 image_digest")
			}
			d, err = s.resolveDigest(ctx, s.cfg, ev.ImageFull)
			if err != nil {
				return ImageRelease{}, DeployLog{}, false, NewValidationError("入站镜像 digest 解析失败: %v", err)
			}
			d = NormalizeDigest(d)
			if d == "" {
				return ImageRelease{}, DeployLog{}, false, NewValidationError("registry 返回非法 digest")
			}
			digestSource = "registry"
		}
		ev.ImageDigest = &d
	}
	release := ImageRelease{
		Metadata: ReleaseMetadata{
			DigestSource:       digestSource,
			Binding:            &app,
			SourceRepositories: append([]string(nil), ev.SourceRepositories...),
			SourcePath:         ev.SourcePath,
			CommitTimestamp:    ev.CommitTimestamp,
		},
		EventKey:      EventKey(ev.Source, ev.EventID, ev.TargetCluster),
		EventID:       ev.EventID,
		Source:        ev.Source,
		PipelineID:    ev.PipelineID,
		PipelineName:  ev.PipelineName,
		BuildNumber:   ev.BuildNumber,
		Branch:        ev.Branch,
		CommitID:      ev.CommitID,
		AppName:       ev.AppName,
		ImageFull:     ev.ImageFull,
		ImageDigest:   ev.ImageDigest,
		Version:       version,
		TestClusterID: cluster.ID,
	}
	if runner != nil {
		release.RunnerAuthorizationHash = runner.TokenHash
	}
	// Deploy by digest when the event carries one — the bytes the test
	// environment runs are pinned before the task is created.
	image := ev.ImageFull
	if ev.ImageDigest != nil {
		image = DigestRef(ev.ImageFull, *ev.ImageDigest)
	}
	deployLog := DeployLog{
		ClusterID:      cluster.ID,
		AppName:        ev.AppName,
		DeploymentName: app.DeploymentName,
		ContainerName:  app.ContainerName,
		Namespace:      app.Namespace,
		Version:        version,
		FileName:       "registry",
		ImageFull:      &image,
		DeployType:     "image_deploy",
		Status:         "pending",
	}
	release, deployLog, created, err := s.store.CreateImageRelease(release, deployLog)
	if err != nil {
		return release, deployLog, false, err
	}
	if !created {
		return release, deployLog, false, nil
	}
	if err := SubmitDeployLog(ctx, s.worker, s.store, deployLog); err != nil {
		return release, deployLog, true, err
	}
	return release, deployLog, true, nil
}

// PromotionRequestTTL bounds how long an approval request stays open —
// a stale approval must not deploy a build that has since been superseded.
const PromotionRequestTTL = 24 * time.Hour

// validatePromotionTarget enforces the promote preconditions shared by the
// mock direct path and the approval flow: the test deploy must have succeeded
// and the target must be an enabled, production-tier, manual-policy cluster
// with an app mapping.
func (s *CDService) validatePromotionTarget(release ImageRelease, targetClusterID int) (Cluster, App, error) {
	testLog, err := s.store.FindLog(release.TestDeployLogID)
	if err != nil || testLog.Status != "success" {
		return Cluster{}, App{}, NewValidationError("测试环境尚未部署成功，不能发布到生产")
	}
	cluster, err := s.store.FindCluster(targetClusterID)
	if err != nil {
		return Cluster{}, App{}, err
	}
	if !cluster.Enabled {
		return Cluster{}, App{}, NewValidationError("目标集群 %s 已禁用", cluster.Name)
	}
	if cluster.EnvironmentTier != "production" {
		return Cluster{}, App{}, NewValidationError("目标集群 %s 不是生产环境", cluster.Name)
	}
	if cluster.DeployPolicy != "manual" {
		return Cluster{}, App{}, NewValidationError("目标集群 %s 不允许手动发布", cluster.Name)
	}
	app, err := s.store.FindAppByName(cluster.ID, release.AppName)
	if err != nil {
		return Cluster{}, App{}, err
	}
	if err := CheckAppImage(app, release.ImageFull); err != nil {
		return Cluster{}, App{}, err
	}
	if current, ok := s.store.SuccessfulWorkloadLog(cluster.ID, app.Namespace, app.DeploymentName, app.ContainerName); ok && current.ReleaseID > release.ID {
		return Cluster{}, App{}, newForbidden("候选制品早于当前已成功发布的制品，请使用历史镜像回退流程")
	}
	if release.ImageDigest == nil || NormalizeDigest(*release.ImageDigest) == "" {
		return Cluster{}, App{}, NewValidationError("制品未固化 digest，不能晋级")
	}
	if !s.cfg.MockRuntime && release.Metadata.DigestSource == "mock" {
		return Cluster{}, App{}, newForbidden("模拟制品不能发布到真实生产环境")
	}
	if err := s.store.verifyReleaseEvidenceForApp(release, app.ID, time.Now()); err != nil {
		return Cluster{}, App{}, err
	}
	if testLog.ImageFull == nil || *testLog.ImageFull != deployImage(release) {
		return Cluster{}, App{}, NewValidationError("测试部署镜像与固化制品不一致")
	}
	return cluster, app, nil
}

// deployImage pins the release image to its digest when one is recorded.
func deployImage(release ImageRelease) string {
	if release.ImageDigest != nil {
		if ref := DigestRef(release.ImageFull, *release.ImageDigest); ref != "" {
			return ref
		}
	}
	return release.ImageFull
}

// Promote deploys the release's original image/version to a chosen
// production cluster. Preconditions are re-validated at call time — an
// approved request may execute minutes after it was filed.
func (s *CDService) Promote(ctx context.Context, id int64, targetClusterID int) (ImageRelease, DeployLog, error) {
	release, err := s.store.FindImageRelease(id)
	if err != nil {
		return release, DeployLog{}, err
	}
	cluster, app, err := s.validatePromotionTarget(release, targetClusterID)
	if err != nil {
		return release, DeployLog{}, err
	}
	if err := s.ensureLegacyRollingStrategy(app.ID); err != nil {
		return release, DeployLog{}, err
	}
	if s.cfg.RequiresProductionApproval() {
		return release, DeployLog{}, newForbidden("生产发布必须经过独立审批")
	}
	if err := s.ensureLegacyConfigurationPlan(ctx, cluster, app); err != nil {
		return release, DeployLog{}, err
	}
	deployLog := promotionLog(release, cluster, app)
	release, deployLog, err = s.store.PromoteImageRelease(release.ID, cluster.ID, deployLog)
	if err != nil {
		return release, deployLog, err
	}
	if err := SubmitDeployLog(ctx, s.worker, s.store, deployLog); err != nil {
		return release, deployLog, err
	}
	return release, deployLog, nil
}

// List returns release views newest first, joining test/prod log state.
func (s *CDService) List(appName string, limit int) []ImageReleaseView {
	releases := s.store.ImageReleases(appName, limit)
	views := make([]ImageReleaseView, 0, len(releases))
	for _, r := range releases {
		views = append(views, s.view(r))
	}
	return views
}

// Get renders one release view by ID — the OpenAPI single-release read.
func (s *CDService) Get(id int64) (ImageReleaseView, error) {
	r, err := s.store.FindImageRelease(id)
	if err != nil {
		return ImageReleaseView{}, err
	}
	return s.view(r), nil
}

// view joins one release with its test/prod deploy log state.
func (s *CDService) view(r ImageRelease) ImageReleaseView {
	v := ImageReleaseView{ImageRelease: r}
	if c, err := s.store.FindCluster(r.TestClusterID); err == nil {
		v.TestClusterName = c.Name
	}
	if l, err := s.store.FindLog(r.TestDeployLogID); err == nil {
		v.TestStatus = l.Status
		v.TestCurrentStep = l.CurrentStep
		v.TestError = l.ErrorMessage
	}
	if r.ProdDeployLogID != nil {
		if l, err := s.store.FindLog(*r.ProdDeployLogID); err == nil {
			v.ProdStatus = &l.Status
			v.ProdCurrentStep = l.CurrentStep
			v.ProdError = l.ErrorMessage
		}
	}
	v.Deployments = s.store.ReleaseDeployments(r.ID)
	v.PromotionBlockers = []string{}
	if v.TestStatus != "success" {
		v.PromotionBlockers = append(v.PromotionBlockers, "测试环境部署未成功")
	}
	if r.ImageDigest == nil || NormalizeDigest(*r.ImageDigest) == "" {
		v.PromotionBlockers = append(v.PromotionBlockers, "制品未固化 digest")
	}
	if log, err := s.store.FindLog(r.TestDeployLogID); err == nil && log.Status == "success" && (log.ImageFull == nil || *log.ImageFull != deployImage(r)) {
		v.PromotionBlockers = append(v.PromotionBlockers, "测试部署镜像与固化制品不一致")
	}
	baseBlockers := len(v.PromotionBlockers)
	targets, err := s.PromotionTargets(r.ID)
	if err != nil {
		v.PromotionBlockers = append(v.PromotionBlockers, "无法读取可发布的生产目标")
	} else if len(targets) == 0 {
		v.PromotionBlockers = append(v.PromotionBlockers, "没有可用的生产发布目标")
	} else if baseBlockers == 0 {
		var targetBlocker error
		eligible := false
		for _, target := range targets {
			if _, _, err := s.validatePromotionTarget(r, target.ID); err == nil {
				eligible = true
				break
			} else if targetBlocker == nil {
				targetBlocker = err
			}
		}
		if !eligible && targetBlocker != nil {
			v.PromotionBlockers = append(v.PromotionBlockers, targetBlocker.Error())
		}
	}
	v.CanPromote = len(v.PromotionBlockers) == 0
	return v
}

// PromotionTargets lists the clusters a release may be promoted to: enabled,
// production tier, manual policy and carrying an app mapping for the
// release's app_name. Callers convert to a safe DTO at the HTTP layer.
func (s *CDService) PromotionTargets(releaseID int64) ([]Cluster, error) {
	release, err := s.store.FindImageRelease(releaseID)
	if err != nil {
		return nil, err
	}
	var targets []Cluster
	for _, c := range s.store.Clusters() {
		if !c.Enabled || c.EnvironmentTier != "production" || c.DeployPolicy != "manual" {
			continue
		}
		if _, err := s.store.FindAppByName(c.ID, release.AppName); err != nil {
			continue
		}
		app, _ := s.store.FindAppByName(c.ID, release.AppName)
		if CheckAppImage(app, release.ImageFull) != nil {
			continue
		}
		if l, ok := s.store.LatestReleaseDeploy(releaseID, c.ID); ok && (l.Status == "success" || l.Status == "pending" || l.Status == "running") {
			continue
		}
		targets = append(targets, c)
	}
	return targets, nil
}

// ---- Promotion approvals (晋级审批门禁) ----

// RequestPromotion files a pending approval request for a prod promotion.
// The same validations as Promote run up front so a request that can never
// succeed is rejected at filing time; they run again at approve time.
func (s *CDService) RequestPromotion(id int64, targetClusterID int, requester string) (PromotionRequest, error) {
	return s.requestPromotion(context.Background(), id, targetClusterID, requester, nil)
}

// RequestDeliveryPlanApproval links a human approval request to the exact
// immutable plan the requester reviewed. Rolling and fully preflighted native
// blue-green plans may proceed; gateway canary stays blocked until its real
// traffic-weight adapter is configured and verified.
func (s *CDService) RequestDeliveryPlanApproval(ctx context.Context, planID int64, requester string, manualReviewAcknowledged ...bool) (PromotionRequest, error) {
	plan, err := s.store.FindDeliveryPlan(planID)
	if err != nil {
		return PromotionRequest{}, err
	}
	if err := validateDeliveryPlanConfigurationSnapshot(plan); err != nil {
		return PromotionRequest{}, err
	}
	if plan.Status != "preview_only" {
		return PromotionRequest{}, NewValidationError("该计划当前不可申请审批")
	}
	if plan.Strategy != "rolling" && plan.Strategy != "blue_green" {
		return PromotionRequest{}, NewValidationError("网关金丝雀执行器尚未完成真实接口联调，当前不能申请发布审批")
	}
	if len(plan.Blockers) > 0 {
		return PromotionRequest{}, NewValidationError("发布预检仍有阻断项，不能申请审批：%s", strings.Join(plan.Blockers, "；"))
	}
	manualChecks := make([]string, 0)
	for _, check := range plan.Snapshot.Checks {
		if requiresManualDeliveryPlanReview(check) {
			manualChecks = append(manualChecks, check.Title)
		}
	}
	if len(manualChecks) > 0 && (len(manualReviewAcknowledged) == 0 || !manualReviewAcknowledged[0]) {
		return PromotionRequest{}, NewValidationError("请先确认人工预检项：%s", strings.Join(manualChecks, "、"))
	}
	for _, req := range s.store.PromotionRequests("", 0) {
		if req.DeliveryPlanID != nil && *req.DeliveryPlanID == plan.ID && req.Status == "approved" {
			return PromotionRequest{}, NewValidationError("该不可变计划已执行过审批，请重新生成计划后再发布")
		}
	}
	current, _, err := s.CreateDeliveryPlan(ctx, plan.ReleaseID, plan.TargetClusterID, requester)
	if err != nil {
		return PromotionRequest{}, err
	}
	if current.ID != plan.ID || current.SnapshotHash != plan.SnapshotHash {
		return PromotionRequest{}, newConflict("计划基线已变化，已生成最新预览计划 #%d；请核对后重新申请", current.ID)
	}
	return s.requestPromotion(ctx, plan.ReleaseID, plan.TargetClusterID, requester, &plan)
}

func requiresManualDeliveryPlanReview(check DeliveryPlanCheck) bool {
	if check.Status == "review" {
		return true
	}
	if check.Status != "unknown" {
		return false
	}
	switch check.Key {
	case "configuration-source", "stable-configuration-source", "deployment-pipeline-scope":
		return true
	default:
		return false
	}
}

func (s *CDService) requestPromotion(ctx context.Context, id int64, targetClusterID int, requester string, plan *DeliveryPlan) (PromotionRequest, error) {
	release, err := s.store.FindImageRelease(id)
	if err != nil {
		return PromotionRequest{}, err
	}
	cluster, app, err := s.validatePromotionTarget(release, targetClusterID)
	if err != nil {
		return PromotionRequest{}, err
	}
	if plan == nil {
		if err := s.ensureLegacyRollingStrategy(app.ID); err != nil {
			return PromotionRequest{}, err
		}
		if err := s.ensureLegacyConfigurationPlan(ctx, cluster, app); err != nil {
			return PromotionRequest{}, err
		}
	} else {
		binding, bindErr := s.store.DeliveryBindingForApp(app.ID)
		if bindErr != nil || normalizeDeliveryStrategy(binding.StrategyDraft) != plan.Strategy {
			return PromotionRequest{}, NewValidationError("当前组件发布策略已改变，请重新生成计划")
		}
		if plan.Strategy != "rolling" && plan.Strategy != "blue_green" {
			return PromotionRequest{}, NewValidationError("该发布策略当前没有可用执行器")
		}
		if len(plan.Blockers) > 0 {
			return PromotionRequest{}, NewValidationError("发布预检仍有阻断项，不能申请审批")
		}
	}
	expected, err := s.currentImage(ctx, cluster, app)
	if err != nil {
		return PromotionRequest{}, fmt.Errorf("读取生产基线失败: %w", err)
	}
	configuration, err := s.currentTemplate(ctx, cluster, app)
	if err != nil {
		return PromotionRequest{}, fmt.Errorf("读取工作负载快照失败: %w", err)
	}
	var planID *int64
	evidencePolicy, err := s.store.DeliveryEnvironmentEvidencePolicyForApp(app.ID)
	if err != nil {
		return PromotionRequest{}, err
	}
	if plan != nil {
		if plan.ReleaseID != release.ID || plan.TargetClusterID != cluster.ID || plan.TargetAppID != app.ID {
			return PromotionRequest{}, NewValidationError("计划与生产目标不一致")
		}
		if plan.Snapshot.Target.CurrentImage != expected || plan.Snapshot.Target.WorkloadHash != configuration {
			return PromotionRequest{}, newConflict("读取期间生产基线发生变化，请重新预览计划")
		}
		planID = &plan.ID
	}
	expires := time.Now().Add(PromotionRequestTTL)
	return s.store.CreatePromotionRequest(PromotionRequest{
		DeliveryPlanID: planID,
		ExpectedImage:  expected,
		TemplateHash:   configuration,
		Snapshot:       promotionSnapshot(release, app, cluster, evidencePolicy),
		BaselineLogID:  s.store.WorkloadBaseline(cluster.ID, app.Namespace, app.DeploymentName),
		ReleaseID:      release.ID,
		ProdClusterID:  cluster.ID,
		Requester:      requester,
		ExpiresAt:      &expires,
	})
}

func (s *CDService) ensureLegacyRollingStrategy(appID int) error {
	binding, err := s.store.DeliveryBindingForApp(appID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil // preserve the legacy rolling path for unmodeled apps
		}
		return err
	}
	switch binding.StrategyDraft {
	case "", "rolling":
		return nil
	case "blue_green":
		return NewValidationError("该组件登记为蓝绿策略；请从发布计划发起蓝绿审批，不能从滚动发布入口绕过")
	case "gateway_canary":
		return NewValidationError("该组件登记为网关金丝雀策略；网关接口尚未完成联调，不能从滚动发布入口绕过")
	default:
		return NewValidationError("该组件的发布策略无效，请核对环境绑定")
	}
}

// ensureLegacyConfigurationPlan keeps legacy rolling endpoints available for
// unmodeled or configuration-free workloads. Once an app has an explicit
// delivery binding, external ConfigMap/Secret references require a plan so a
// human reviews and acknowledges the configuration recovery limitation.
func (s *CDService) ensureLegacyConfigurationPlan(ctx context.Context, cluster Cluster, app App) error {
	if _, err := s.store.DeliveryBindingForApp(app.ID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if s.worker == nil || s.worker.pipeline == nil || s.worker.pipeline.k8s == nil {
		return NewValidationError("发布运行时不可用，无法核对外部配置引用；请从发布计划重新确认")
	}
	rt, err := s.worker.pipeline.k8s.For(cluster)
	if err != nil {
		return err
	}
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	def, err := kube.LookupResource("apps", "v1", "deployments")
	if err != nil {
		return err
	}
	deployment, err := rt.GetResource(readCtx, def, app.Namespace, app.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("核对外部配置引用失败: %w", err)
	}
	refs, known, _ := deliveryConfigurationReferencesForWorkload(readCtx, rt, app.Namespace, deployment)
	if !known {
		return NewValidationError("无法完整读取 Deployment 及其 ServiceAccount 的 ConfigMap/Secret 引用；请检查集群读取权限并从发布计划核对后再申请审批")
	}
	if len(refs) > 0 {
		return NewValidationError("该组件引用了 ConfigMap/Secret，但此旧入口没有配置引用快照和人工确认；请从发布计划预览核对后再申请审批")
	}
	return nil
}

// ApprovePromotion executes the gated promotion. Separation of duties is
// enforced: the requester may not approve their own request, and an expired
// request is marked and refused.
func (s *CDService) ApprovePromotion(ctx context.Context, reqID int64, approver string) (PromotionRequest, DeployLog, error) {
	req, err := s.store.FindPromotionRequest(reqID)
	if err != nil {
		return req, DeployLog{}, err
	}
	if req.Status != "pending" {
		return req, DeployLog{}, NewValidationError("审批请求已是 %s 状态", req.Status)
	}
	if req.ExpiresAt != nil && time.Now().After(*req.ExpiresAt) {
		now := time.Now()
		req.Status = "expired"
		req.DecidedAt = &now
		if err := s.store.UpdatePromotionRequest(req); err != nil {
			return req, DeployLog{}, err
		}
		return req, DeployLog{}, NewValidationError("审批请求已过期，请重新发起")
	}
	if req.Requester == approver {
		return req, DeployLog{}, newForbidden("发起人与审批人不能是同一人")
	}
	if strings.HasPrefix(approver, "apikey:") || approver == "" || approver == "-" {
		return req, DeployLog{}, newForbidden("生产审批必须由独立的人工账号完成")
	}
	release, err := s.store.FindImageRelease(req.ReleaseID)
	if err != nil {
		return req, DeployLog{}, err
	}
	cluster, app, err := s.validatePromotionTarget(release, req.ProdClusterID)
	if err != nil {
		return req, DeployLog{}, err
	}
	allowed, err := s.store.CanApproveDeliveryTarget(approver, app.ID)
	if err != nil {
		return req, DeployLog{}, err
	}
	if !allowed {
		return req, DeployLog{}, newForbidden("当前账号未获授权审批此业务应用和生产环境的发布")
	}
	if req.DeliveryPlanID == nil {
		if err := s.ensureLegacyRollingStrategy(app.ID); err != nil {
			return req, DeployLog{}, err
		}
		if err := s.ensureLegacyConfigurationPlan(ctx, cluster, app); err != nil {
			return req, DeployLog{}, err
		}
	} else {
		plan, planErr := s.store.FindDeliveryPlan(*req.DeliveryPlanID)
		if planErr != nil {
			return req, DeployLog{}, planErr
		}
		if err := validateDeliveryPlanConfigurationSnapshot(plan); err != nil {
			return req, DeployLog{}, err
		}
		if plan.Status != "preview_only" || (plan.Strategy != "rolling" && plan.Strategy != "blue_green") || plan.ReleaseID != req.ReleaseID || plan.TargetClusterID != req.ProdClusterID || plan.TargetAppID != app.ID || len(plan.Blockers) > 0 {
			return req, DeployLog{}, NewValidationError("审批请求关联的发布计划与当前目标不一致")
		}
		binding, bindErr := s.store.DeliveryBindingForApp(app.ID)
		if bindErr != nil || normalizeDeliveryStrategy(binding.StrategyDraft) != plan.Strategy {
			return req, DeployLog{}, newConflict("组件发布策略已改变，请重新申请")
		}
	}
	currentImage, readErr := s.currentImage(ctx, cluster, app)
	if readErr != nil {
		return req, DeployLog{}, readErr
	}
	if currentImage != req.ExpectedImage {
		return req, DeployLog{}, newConflict("生产实际镜像已改变，请重新申请")
	}
	configuration, err := s.currentTemplate(ctx, cluster, app)
	if err != nil {
		return req, DeployLog{}, err
	}
	if req.TemplateHash == "" || req.TemplateHash != configuration {
		return req, DeployLog{}, newConflict("工作负载配置已改变，请重新申请")
	}
	evidencePolicy, err := s.store.DeliveryEnvironmentEvidencePolicyForApp(app.ID)
	if err != nil {
		return req, DeployLog{}, err
	}
	if req.Snapshot != promotionSnapshot(release, app, cluster, evidencePolicy) {
		return req, DeployLog{}, newConflict("候选版本或验证证据已改变，请重新申请")
	}
	var approvedRun *DeliveryRun
	if req.DeliveryPlanID != nil {
		latest, _, planErr := s.CreateDeliveryPlan(ctx, req.ReleaseID, req.ProdClusterID, req.Requester)
		if planErr != nil {
			return req, DeployLog{}, planErr
		}
		if latest.ID != *req.DeliveryPlanID || latest.Snapshot.Target.CurrentImage != req.ExpectedImage || latest.Snapshot.Target.WorkloadHash != req.TemplateHash || len(latest.Blockers) > 0 {
			return req, DeployLog{}, newConflict("审批计划基线已变化，请重新预览并申请")
		}
		approvedRun = &DeliveryRun{
			DeliveryPlanID:     latest.ID,
			PromotionRequestID: req.ID,
			Strategy:           latest.Strategy,
		}
	}
	var deployLog DeployLog
	if approvedRun != nil {
		locks := []ResourceLock(nil)
		if approvedRun.Strategy == "blue_green" {
			plan, planErr := s.store.FindDeliveryPlan(approvedRun.DeliveryPlanID)
			if planErr != nil {
				return req, DeployLog{}, planErr
			}
			locks, err = BlueGreenPlanLocks(plan)
			if err != nil {
				return req, DeployLog{}, err
			}
		}
		req, deployLog, _, err = s.store.ApproveAndCreateDeliveryRun(req, approver, promotionLog(release, cluster, app), *approvedRun, locks...)
	} else {
		req, deployLog, err = s.store.ApproveAndCreateDeploy(req, approver, promotionLog(release, cluster, app))
	}
	if err != nil {
		return req, deployLog, err
	}
	// The durable pending task is authoritative. Notification of the worker is
	// best-effort; its pending-task scan and startup recovery can execute it.
	if s.worker != nil {
		_ = s.worker.Submit(ctx, deployLog.ID)
	}
	return req, deployLog, nil
}

// RejectPromotion closes a pending request without deploying. The requester
// withdrawing their own request records "cancelled"; anyone else records
// "rejected" with the optional comment.
func (s *CDService) RejectPromotion(reqID int64, actor, comment string) (PromotionRequest, error) {
	req, err := s.store.FindPromotionRequest(reqID)
	if err != nil {
		return req, err
	}
	if req.Status != "pending" {
		return req, NewValidationError("审批请求已是 %s 状态", req.Status)
	}
	if actor != req.Requester {
		release, releaseErr := s.store.FindImageRelease(req.ReleaseID)
		if releaseErr != nil {
			return req, releaseErr
		}
		app, appErr := s.store.FindAppByName(req.ProdClusterID, release.AppName)
		if appErr != nil {
			return req, appErr
		}
		allowed, authErr := s.store.CanApproveDeliveryTarget(actor, app.ID)
		if authErr != nil {
			return req, authErr
		}
		if !allowed {
			return req, newForbidden("当前账号未获授权决定此业务应用和生产环境的发布")
		}
	}
	now := time.Now()
	if actor == req.Requester {
		req.Status = "cancelled"
	} else {
		req.Status = "rejected"
	}
	req.Approver = &actor
	req.DecidedAt = &now
	if c := strings.TrimSpace(comment); c != "" {
		req.Comment = &c
	}
	if err := s.store.UpdatePromotionRequest(req); err != nil {
		return req, err
	}
	return req, nil
}

// ListPromotionRequests sweeps expired pendings then returns newest first.
func (s *CDService) ListPromotionRequests(status string, limit int) ([]PromotionRequest, error) {
	now := time.Now()
	for _, p := range s.store.PromotionRequests("pending", 0) {
		if p.ExpiresAt != nil && now.After(*p.ExpiresAt) {
			p.Status = "expired"
			p.DecidedAt = &now
			if err := s.store.UpdatePromotionRequest(p); err != nil {
				return nil, err
			}
		}
	}
	return s.store.PromotionRequests(status, limit), nil
}

func promotionLog(r ImageRelease, c Cluster, app App) DeployLog {
	image := deployImage(r)
	return DeployLog{ReleaseID: r.ID, ClusterID: c.ID, AppName: r.AppName,
		DeploymentName: app.DeploymentName, ContainerName: app.ContainerName,
		Namespace: app.Namespace, Version: r.Version, FileName: "registry", ImageFull: &image,
		DeployType: "image_deploy", Status: "pending"}
}

func promotionSnapshot(r ImageRelease, app App, cluster Cluster, evidencePolicies ...DeliveryEnvironmentEvidencePolicyVersion) string {
	// Only a fingerprint is persisted. Exclude DB timestamp precision differences
	// so an unchanged approval survives reload; bind credentials by a hash.
	app.CreatedAt = time.Time{}
	cluster.CreatedAt, cluster.UpdatedAt = time.Time{}, time.Time{}
	credential, _ := json.Marshal(cluster.CredentialCiphertext)
	credentialVersion := sha256.Sum256(credential)
	var evidencePolicy *struct {
		EnvironmentID             int64
		Version                   int64
		RequiredVerificationKinds []string
		MaxEvidenceAgeMinutes     int
		TrustedSubmitters         map[string][]string
	}
	if len(evidencePolicies) > 0 {
		policy := evidencePolicies[0]
		evidencePolicy = &struct {
			EnvironmentID             int64
			Version                   int64
			RequiredVerificationKinds []string
			MaxEvidenceAgeMinutes     int
			TrustedSubmitters         map[string][]string
		}{policy.EnvironmentID, policy.Version, policy.RequiredVerificationKinds, policy.MaxEvidenceAgeMinutes, policy.TrustedSubmitters}
	}
	data, _ := json.Marshal(struct {
		Image             string
		Metadata          ReleaseMetadata
		App               App
		Cluster           Cluster
		CredentialVersion string
		EvidencePolicy    any
	}{deployImage(r), r.Metadata, app, cluster, hex.EncodeToString(credentialVersion[:]), evidencePolicy})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (r ImageRelease) Verified(now time.Time) error {
	e := r.Metadata.Verification
	if e == nil {
		return NewValidationError("缺少业务验证证据")
	}
	if r.ImageDigest == nil || NormalizeDigest(*r.ImageDigest) == "" || NormalizeDigest(e.Digest) != NormalizeDigest(*r.ImageDigest) {
		return NewValidationError("验证报告不属于当前制品")
	}
	if e.Status != "passed" {
		return NewValidationError("业务验证未通过")
	}
	if e.ValidUntil.IsZero() || !now.Before(e.ValidUntil) {
		return NewValidationError("业务验证证据已过期")
	}
	return nil
}

func (s *CDService) currentImage(ctx context.Context, c Cluster, app App) (string, error) {
	if s.worker == nil {
		return "", fmt.Errorf("发布运行时未配置")
	}
	rt, err := s.worker.pipeline.k8s.For(c)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return rt.CurrentImage(ctx, app.Namespace, app.DeploymentName, app.ContainerName)
}
