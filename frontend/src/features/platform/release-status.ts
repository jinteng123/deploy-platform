import type { DeployStepResult, ImageReleaseView } from '@/api/client'

// promoteAction decides what the action column shows for a release row:
// a clickable label when the release can be promoted, otherwise a static state.
export function promoteAction(
  r: Pick<ImageReleaseView, 'can_promote' | 'test_status' | 'prod_status'>
): {
  label: string
  actionable: boolean
} {
  if (r.can_promote) return { label: '创建发布计划', actionable: true }
  if (r.prod_status === 'success') return { label: '已发布', actionable: false }
  if (r.test_status !== 'success')
    return { label: '等待测试环境', actionable: false }
  if (r.prod_status === 'pending' || r.prod_status === 'running')
    return { label: '发布中', actionable: false }
  return { label: '等待验证', actionable: false }
}

export function releaseBuildIdentity(
  r: Pick<ImageReleaseView, 'pipeline_name' | 'pipeline_id' | 'build_number'>
): string {
  const pipelineName = r.pipeline_name?.trim()
  const pipelineID = r.pipeline_id?.trim()
  const buildNumber = r.build_number?.trim()
  const pipeline = pipelineName
    ? `${pipelineName}${pipelineID ? ` (#${pipelineID})` : ''}`
    : pipelineID
      ? `流水线 #${pipelineID}`
      : '流水线未知'
  const build = buildNumber ? `构建 #${buildNumber}` : '构建号未知'
  return `${pipeline} · ${build}`
}

// step_detail arrives as a JSON string; an empty or malformed payload must
// render as "no steps" rather than crash the detail dialog.
export function parseStepDetail(detail?: string | null): DeployStepResult[] {
  if (!detail) return []
  try {
    const parsed: unknown = JSON.parse(detail)
    if (!Array.isArray(parsed)) return []
    return parsed.filter(
      (s): s is DeployStepResult =>
        typeof s === 'object' &&
        s != null &&
        typeof (s as DeployStepResult).step === 'string'
    )
  } catch {
    return []
  }
}

// Humanized labels for the deploy pipeline step names emitted by the worker.
const deployStepLabel: Record<string, string> = {
  check_file: '检查部署包',
  verify_image: '校验部署镜像',
  verify_pod_image: '核对 Pod 运行镜像摘要',
  verify_candidate_pod_image: '核对候选 Pod 镜像摘要',
  resolve_digest: '解析镜像摘要',
  docker_load: '加载镜像',
  docker_tag: '标记镜像',
  docker_push: '推送镜像',
  k8s_set_image: '更新镜像',
  rollout_wait: '等待滚动更新',
  awaiting_candidate_review: '等待切流确认',
  awaiting_live_verification: '等待业务验证',
  needs_attention: '需要核对集群状态',
  restored: '稳定版本已恢复',
  cancelled: '发布已取消',
  query_current: '查询当前镜像',
}

export function deployStepName(step: string): string {
  return deployStepLabel[step] ?? step
}

export function deployLogRunning(status?: string | null): boolean {
  return status === 'pending' || status === 'running'
}
