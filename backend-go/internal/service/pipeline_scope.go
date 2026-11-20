package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"
)

const (
	maxPipelineScopeDefinitionBytes = 1 << 20
	maxPipelineScopeAnalysisDepth   = 64
	maxPipelineScopeAnalysisNodes   = 10_000
)

type YunxiaoPipelineScopeFinding struct {
	NodeRef  string `json:"node_ref"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
}

// YunxiaoDeploymentPipelineScopeReport deliberately contains no flow YAML,
// script, task parameter, or command excerpt. A pipeline definition can embed
// secrets, so the server returns only structural references and fixed labels.
type YunxiaoDeploymentPipelineScopeReport struct {
	PipelineID      string                        `json:"pipeline_id"`
	Name            string                        `json:"name"`
	Type            string                        `json:"type"`
	UpdateTime      int64                         `json:"update_time"`
	DefinitionHash  string                        `json:"definition_hash,omitempty"`
	AnalysisStatus  string                        `json:"analysis_status"`
	ReviewRequired  bool                          `json:"review_required"`
	Findings        []YunxiaoPipelineScopeFinding `json:"findings"`
	UnresolvedNodes []string                      `json:"unresolved_nodes"`
	Warning         string                        `json:"warning"`
}

var (
	kubernetesWriteCommand  = regexp.MustCompile(`(?i)\b(kubectl|oc)\b[^\n;&|]{0,160}\b(apply|create|replace|patch|delete|edit|scale|set|rollout|annotate|label)\b`)
	kubernetesConfigWrite   = regexp.MustCompile(`(?i)\b(configmap|secret)s?\b`)
	kubernetesWorkloadWrite = regexp.MustCompile(`(?i)\b(deployment|statefulset|daemonset|replicaset|service|ingress|job)s?\b`)
	kubernetesImageWrite    = regexp.MustCompile(`(?i)\b(set\s+image|rollout\s+undo|rollout\s+restart|scale\s+deployment)\b`)
	helmWriteCommand        = regexp.MustCompile(`(?i)\bhelm\s+(upgrade|install|uninstall|rollback)\b`)
	otherInfraWriteCommand  = regexp.MustCompile(`(?i)\b(terraform\s+apply|pulumi\s+up|ansible-playbook)\b`)
)

const pipelineScopeWarning = "静态检查只用于定位疑似步骤，无法证明流水线完整写入范围。请在云效核对引用模板、脚本下载内容、变量/插件和实际执行身份；确认前不要触发配置恢复或接入发布 Worker。"

// InspectDeploymentPipelineScope fetches a pipeline definition through the
// read-only Yunxiao GetPipeline API, analyzes it server-side, and never returns
// raw source or parameters. Every result requires manual review: shell,
// templates, plugins, variables and remote downloads can hide write effects.
func (y *YunxiaoService) InspectDeploymentPipelineScope(ctx context.Context, connectionID int64, pipelineID string) (YunxiaoDeploymentPipelineScopeReport, error) {
	if connectionID <= 0 {
		return YunxiaoDeploymentPipelineScopeReport{}, NewValidationError("云效连接无效")
	}
	if !runnerPipelineID.MatchString(pipelineID) {
		return YunxiaoDeploymentPipelineScopeReport{}, NewValidationError("流水线 ID 无效")
	}
	connection, err := y.store.YunxiaoConnection(connectionID)
	if err != nil {
		return YunxiaoDeploymentPipelineScopeReport{}, NewValidationError("请先选择云效连接")
	}
	var pipeline YunxiaoPipeline
	if err := y.request(ctx, connection, pipelineID, http.MethodGet, nil, &pipeline); err != nil {
		return YunxiaoDeploymentPipelineScopeReport{}, err
	}
	return AnalyzeYunxiaoPipelineScope(pipelineID, pipeline), nil
}

// AnalyzeYunxiaoPipelineScope extracts only conservative indicators from the
// pipeline YAML. It never treats absence of a recognized write command as a
// read-only verdict.
func AnalyzeYunxiaoPipelineScope(pipelineID string, pipeline YunxiaoPipeline) YunxiaoDeploymentPipelineScopeReport {
	report := YunxiaoDeploymentPipelineScopeReport{
		PipelineID:      pipelineID,
		Name:            pipeline.Name,
		Type:            pipeline.Type,
		UpdateTime:      pipeline.UpdateTime,
		AnalysisStatus:  "definition_unavailable",
		ReviewRequired:  true,
		Findings:        []YunxiaoPipelineScopeFinding{},
		UnresolvedNodes: []string{},
		Warning:         pipelineScopeWarning,
	}
	flow := pipeline.YAML()
	if flow == "" {
		return report
	}
	if len(flow) > maxPipelineScopeDefinitionBytes {
		report.AnalysisStatus = "definition_too_large"
		return report
	}
	hash := sha256.Sum256([]byte(flow))
	report.DefinitionHash = hex.EncodeToString(hash[:])

	var root yaml.MapSlice
	decoder := yaml.NewDecoder(strings.NewReader(flow), yaml.UseOrderedMap())
	if err := decoder.Decode(&root); err != nil || len(root) == 0 {
		report.AnalysisStatus = "definition_unparseable"
		return report
	}
	report.AnalysisStatus = "manual_review_required"
	walker := pipelineScopeWalker{report: &report}
	walker.walk(root, "流水线", 0)
	if walker.limited {
		report.Warning += " 结构分析达到安全复杂度上限，仍有节点未扫描。"
	}
	return report
}

type pipelineScopeWalker struct {
	report  *YunxiaoDeploymentPipelineScopeReport
	visited int
	limited bool
}

func (w *pipelineScopeWalker) walk(value any, nodeRef string, depth int) {
	if w.limited {
		return
	}
	if depth > maxPipelineScopeAnalysisDepth || w.visited >= maxPipelineScopeAnalysisNodes {
		w.limited = true
		addUnresolvedPipelineNode(w.report, nodeRef)
		return
	}
	w.visited++
	switch typed := value.(type) {
	case yaml.MapSlice:
		for _, item := range typed {
			if w.limited {
				return
			}
			key := strings.ToLower(strings.TrimSpace(fmt.Sprint(item.Key)))
			if key == "stages" || key == "jobs" || key == "steps" {
				w.walkSequence(item.Value, nodeRef, key, depth+1)
				continue
			}
			if key == "command" || key == "script" || key == "shell" || key == "commandline" || key == "run" {
				if code, ok := pipelineScopeCode(item.Value); ok {
					analyzePipelineScopeCommand(code, nodeRef, w.report)
					continue
				}
				// Some Yunxiao task definitions encode their command as a mapping or
				// mixed list. Keep the node unresolved and inspect nested fields so
				// a recognizable write is not hidden by an unfamiliar shape.
				addUnresolvedPipelineNode(w.report, nodeRef)
				addPipelineScopeFinding(w.report, YunxiaoPipelineScopeFinding{
					NodeRef: nodeRef, Category: "opaque_script",
					Summary: "检测到非纯文本脚本定义；需在云效核对实际执行内容。",
				})
				w.walk(item.Value, nodeRef, depth+1)
				continue
			}
			if key == "task" || key == "template" {
				addUnresolvedPipelineNode(w.report, nodeRef)
				addPipelineScopeFinding(w.report, YunxiaoPipelineScopeFinding{
					NodeRef: nodeRef, Category: "opaque_task",
					Summary: "检测到云效任务或模板引用；其内部动作和权限需在云效中单独核对。",
				})
				// Task references are not always scalar IDs. Walk mapping-shaped
				// definitions too, while retaining the opaque-task warning.
				w.walk(item.Value, nodeRef, depth+1)
				continue
			}
			w.walk(item.Value, nodeRef, depth+1)
		}
	case []any:
		w.walkSequence(typed, nodeRef, "节点", depth+1)
	}
}

func (w *pipelineScopeWalker) walkSequence(value any, parentRef, label string, depth int) {
	values, ok := value.([]any)
	if !ok {
		w.walk(value, parentRef, depth)
		return
	}
	for i, child := range values {
		if w.limited {
			return
		}
		w.walk(child, fmt.Sprintf("%s / %s %d", parentRef, pipelineScopeLabel(label), i+1), depth+1)
	}
}

func pipelineScopeCode(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, strings.TrimSpace(typed) != ""
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			line, ok := item.(string)
			if !ok {
				return "", false
			}
			parts = append(parts, line)
		}
		joined := strings.Join(parts, " ")
		return joined, strings.TrimSpace(joined) != ""
	default:
		return "", false
	}
}

func pipelineScopeLabel(key string) string {
	switch key {
	case "stages":
		return "阶段"
	case "jobs":
		return "任务"
	case "steps":
		return "步骤"
	default:
		return "节点"
	}
}

func analyzePipelineScopeCommand(command, nodeRef string, report *YunxiaoDeploymentPipelineScopeReport) {
	if strings.TrimSpace(command) == "" {
		return
	}
	addUnresolvedPipelineNode(report, nodeRef)
	switch {
	case helmWriteCommand.MatchString(command):
		addPipelineScopeFinding(report, YunxiaoPipelineScopeFinding{
			NodeRef: nodeRef, Category: "helm_release_write",
			Summary: "检测到 Helm release 写操作；Chart 可能创建或更新任意 Kubernetes 资源。",
		})
	case kubernetesWriteCommand.MatchString(command):
		category := "kubernetes_resource_write"
		summary := "检测到 Kubernetes 资源写操作；静态检查不能确定清单最终包含哪些资源。"
		if kubernetesConfigWrite.MatchString(command) {
			category = "possible_config_write"
			summary = "检测到 ConfigMap/Secret 相关写入标记；需核对最终目标、命名空间和 Secret 处理方式。"
		}
		if kubernetesWorkloadWrite.MatchString(command) || kubernetesImageWrite.MatchString(command) {
			category = "possible_workload_write"
			summary = "检测到 Deployment/Service/镜像等工作负载写入标记；需核对是否触及绑定外目标。"
		}
		addPipelineScopeFinding(report, YunxiaoPipelineScopeFinding{NodeRef: nodeRef, Category: category, Summary: summary})
	case otherInfraWriteCommand.MatchString(command):
		addPipelineScopeFinding(report, YunxiaoPipelineScopeFinding{
			NodeRef: nodeRef, Category: "external_infrastructure_write",
			Summary: "检测到基础设施变更工具写操作；影响范围无法从流水线静态输入完整确定。",
		})
	default:
		addPipelineScopeFinding(report, YunxiaoPipelineScopeFinding{
			NodeRef: nodeRef, Category: "opaque_script",
			Summary: "包含脚本执行；脚本、动态变量、下载内容或子进程可能产生未识别写入。",
		})
	}
}

func addPipelineScopeFinding(report *YunxiaoDeploymentPipelineScopeReport, finding YunxiaoPipelineScopeFinding) {
	for _, existing := range report.Findings {
		if existing.NodeRef == finding.NodeRef && existing.Category == finding.Category {
			return
		}
	}
	if len(report.Findings) < 50 {
		report.Findings = append(report.Findings, finding)
	}
}

func addUnresolvedPipelineNode(report *YunxiaoDeploymentPipelineScopeReport, nodeRef string) {
	for _, existing := range report.UnresolvedNodes {
		if existing == nodeRef {
			return
		}
	}
	if len(report.UnresolvedNodes) < 50 {
		report.UnresolvedNodes = append(report.UnresolvedNodes, nodeRef)
	}
}
