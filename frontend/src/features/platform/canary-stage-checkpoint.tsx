import { AlertTriangle, Check, Route } from 'lucide-react'
import type { DeliveryRunEvent } from '@/api/client'
import { Badge } from '@/components/ui/badge'

type CanaryMetricSnapshot = {
  imageDigest: string
  requests: number
  errors: number
  p95LatencyMillis: number | null
  health: string
}

type CanaryObservationSnapshot = {
  stableImageDigest: string
  candidateImageDigest: string
  windowStart: string
  windowEnd: string
  observedAt: string
  stable: CanaryMetricSnapshot
  candidate: CanaryMetricSnapshot
}

export type CanaryCheckpointSnapshot = {
  schemaVersion: number
  stageIndex: number
  candidateWeightPercent: number
  observationDurationSeconds: number
  phase: string
  confirmedStageIndex?: number
  confirmedBy?: string
  confirmedAt?: string
  stageStartedAt?: string
  effectiveWeights?: {
    stable: number
    candidate: number
  }
  observation?: CanaryObservationSnapshot
  decisionStatus?: string
  decisionReason?: string
  decisionEvaluatedAt?: string
}

type CanaryRestoreSnapshot = {
  stageIndex: number
  stableImageDigest: string
  candidateImageDigest: string
  route: {
    id: string
    stableBackendId: string
    candidateBackendId: string
  }
  revision: string
  restoredAt: string
}

const phaseLabels: Record<string, string> = {
  awaiting_weights: '等待权重生效',
  observing: '正在观察',
  awaiting_confirmation: '等待人工确认',
  restore_recommended: '建议恢复稳定版',
  paused: '已暂停',
  needs_attention: '需要核对',
  completed: '观察完成',
}

const healthLabels: Record<string, string> = {
  healthy: '正常',
  degraded: '异常',
  unknown: '未知',
}

const decisionLabels: Record<string, string> = {
  awaiting_confirmation: '阈值内，等待人工确认',
  restore_recommended: '超过阈值，建议恢复',
  pause: '观测无效，已暂停推进',
}

const reasonLabels: Record<string, string> = {
  within_thresholds_manual_confirmation_required: '指标在阈值内',
  candidate_health_degraded: '候选版本健康检查异常',
  candidate_threshold_breached: '候选版本超过配置阈值',
  insufficient_version_samples: '稳定版或候选版样本不足',
  stable_baseline_not_healthy: '稳定版基线不健康',
  candidate_health_unknown: '候选版健康状态未知',
  gateway_weight_not_effective: '网关实际权重与计划不一致',
  stage_observation_window_incomplete: '观察窗口尚未完成',
  observation_stale_or_future: '观测过期或时间异常',
  observation_window_invalid: '观测时间窗口无效',
  version_identity_mismatch: '镜像版本身份与观测不一致',
}

const canaryEventResultLabels: Record<string, string> = {
  within_thresholds_manual_confirmation_required:
    '阶段指标达到策略阈值，等待操作员确认。',
  candidate_health_degraded: '候选版本健康状态异常，建议恢复稳定版。',
  candidate_threshold_breached: '候选版本超过策略阈值，建议恢复稳定版。',
  version_identity_mismatch: '观测镜像与计划 digest 不一致，阶段已暂停推进。',
  gateway_action_evidence_invalid: '网关动作回读证据不完整，阶段未推进。',
  gateway_action_evidence_missing: '缺少网关权重回读，阶段未推进。',
  gateway_read_failed: '无法读取当前网关路由，阶段未推进。',
  gateway_state_drifted: '网关权重或 revision 已变化，请核对当前流量。',
  gateway_read_failed_after_metrics: '指标读取后无法复核网关路由，阶段未推进。',
  gateway_changed_during_observation:
    '指标读取期间网关路由发生变化，阶段未推进。',
  planned_image_identity_invalid: '计划缺少有效镜像 digest，阶段未推进。',
  metrics_read_failed: '无法读取稳定版和候选版指标，阶段未推进。',
  metrics_payload_invalid: '指标数据未通过版本或时间窗口校验，阶段未推进。',
  metrics_timestamp_in_future: '指标时间超出当前时间，阶段未推进。',
  observation_stale_or_future: '指标已过期或时间异常，阶段已暂停推进。',
  observation_window_invalid: '指标观察窗口无效，阶段未推进。',
  insufficient_version_samples: '稳定版或候选版样本不足，阶段未推进。',
  stable_baseline_not_healthy: '稳定版基线不健康，阶段未推进。',
  candidate_health_unknown: '候选版健康状态未知，阶段未推进。',
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function finiteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

function parseMetric(value: unknown): CanaryMetricSnapshot | null {
  if (!isRecord(value)) return null
  if (
    typeof value.image_digest !== 'string' ||
    !/^sha256:[a-f0-9]{64}$/i.test(value.image_digest.trim()) ||
    !Number.isSafeInteger(value.requests) ||
    !Number.isSafeInteger(value.errors) ||
    !finiteNumber(value.requests) ||
    !finiteNumber(value.errors) ||
    value.requests < 0 ||
    value.errors < 0 ||
    value.errors > value.requests ||
    (value.p95_latency_ms !== null && !finiteNumber(value.p95_latency_ms)) ||
    (finiteNumber(value.p95_latency_ms) && value.p95_latency_ms < 0) ||
    typeof value.health !== 'string' ||
    !Object.prototype.hasOwnProperty.call(healthLabels, value.health)
  ) {
    return null
  }
  return {
    imageDigest: value.image_digest,
    requests: value.requests,
    errors: value.errors,
    p95LatencyMillis: finiteNumber(value.p95_latency_ms)
      ? value.p95_latency_ms
      : null,
    health: value.health,
  }
}

function parseObservation(value: unknown): CanaryObservationSnapshot | null {
  if (!isRecord(value)) return null
  const stable = parseMetric(value.stable)
  const candidate = parseMetric(value.candidate)
  const timestamps = [value.window_start, value.window_end, value.observed_at]
  if (
    typeof value.stable_image_digest !== 'string' ||
    typeof value.candidate_image_digest !== 'string' ||
    timestamps.some(
      (timestamp) =>
        typeof timestamp !== 'string' || !Number.isFinite(Date.parse(timestamp))
    ) ||
    !stable ||
    !candidate
  ) {
    return null
  }
  const [windowStart, windowEnd, observedAt] = timestamps as string[]
  if (
    Date.parse(windowStart) >= Date.parse(windowEnd) ||
    Date.parse(windowEnd) > Date.parse(observedAt)
  ) {
    return null
  }
  return {
    stableImageDigest: value.stable_image_digest,
    candidateImageDigest: value.candidate_image_digest,
    windowStart,
    windowEnd,
    observedAt,
    stable,
    candidate,
  }
}

// This validated payload parser is also used by the release control card.
// eslint-disable-next-line react-refresh/only-export-components
export function parseCanaryStageCheckpoint(
  payload: unknown
): CanaryCheckpointSnapshot | null {
  if (!isRecord(payload)) return null
  if (
    (payload.schema_version !== 1 && payload.schema_version !== 2) ||
    !Number.isInteger(payload.stage_index) ||
    !finiteNumber(payload.stage_index) ||
    payload.stage_index < 0 ||
    payload.stage_index >= 10 ||
    !Number.isInteger(payload.candidate_weight_percent) ||
    !finiteNumber(payload.candidate_weight_percent) ||
    payload.candidate_weight_percent < 1 ||
    payload.candidate_weight_percent > 100 ||
    !Number.isInteger(payload.observation_duration_seconds) ||
    !finiteNumber(payload.observation_duration_seconds) ||
    payload.observation_duration_seconds < 1 ||
    payload.observation_duration_seconds > 86400 ||
    typeof payload.phase !== 'string' ||
    !Object.prototype.hasOwnProperty.call(phaseLabels, payload.phase)
  ) {
    return null
  }

  let effectiveWeights: CanaryCheckpointSnapshot['effectiveWeights']
  if (payload.effective_weights !== undefined) {
    if (!isRecord(payload.effective_weights)) return null
    const stable = payload.effective_weights.stable_weight_percent
    const candidate = payload.effective_weights.candidate_weight_percent
    if (
      !finiteNumber(stable) ||
      !finiteNumber(candidate) ||
      !Number.isInteger(stable) ||
      !Number.isInteger(candidate) ||
      stable < 0 ||
      candidate < 0 ||
      stable + candidate !== 100
    ) {
      return null
    }
    effectiveWeights = { stable, candidate }
  }

  let observation: CanaryObservationSnapshot | undefined
  if (payload.observation !== undefined) {
    const parsed = parseObservation(payload.observation)
    if (!parsed) return null
    observation = parsed
  }

  const decision = isRecord(payload.decision) ? payload.decision : undefined
  const analysis =
    decision && isRecord(decision.analysis) ? decision.analysis : undefined
  const decisionEvaluatedAt =
    decision && typeof decision.evaluated_at === 'string'
      ? decision.evaluated_at
      : undefined
  if (
    decision &&
    (!analysis ||
      typeof analysis.status !== 'string' ||
      !Object.prototype.hasOwnProperty.call(decisionLabels, analysis.status) ||
      typeof analysis.reason !== 'string' ||
      (payload.schema_version === 2 &&
        (!decisionEvaluatedAt ||
          !Number.isFinite(Date.parse(decisionEvaluatedAt)))) ||
      (decisionEvaluatedAt &&
        !Number.isFinite(Date.parse(decisionEvaluatedAt))) ||
      (decisionEvaluatedAt &&
        observation &&
        Date.parse(decisionEvaluatedAt) < Date.parse(observation.observedAt)))
  ) {
    return null
  }

  const stageStartedAt =
    typeof payload.stage_started_at === 'string'
      ? payload.stage_started_at
      : undefined
  const stageStartedAtMs = stageStartedAt
    ? Date.parse(stageStartedAt)
    : Number.NaN
  const phase = payload.phase
  const hasConfirmation =
    payload.confirmed_stage_index !== undefined ||
    payload.confirmed_by !== undefined ||
    payload.confirmed_at !== undefined
  let confirmedStageIndex: number | undefined
  let confirmedBy: string | undefined
  let confirmedAt: string | undefined
  if (hasConfirmation) {
    if (
      !Number.isInteger(payload.confirmed_stage_index) ||
      !finiteNumber(payload.confirmed_stage_index) ||
      payload.confirmed_stage_index < 0 ||
      payload.confirmed_stage_index > payload.stage_index ||
      typeof payload.confirmed_by !== 'string' ||
      payload.confirmed_by.trim() !== payload.confirmed_by ||
      payload.confirmed_by.length < 1 ||
      payload.confirmed_by.length > 128 ||
      typeof payload.confirmed_at !== 'string' ||
      !Number.isFinite(Date.parse(payload.confirmed_at)) ||
      (payload.confirmed_stage_index === payload.stage_index &&
        phase !== 'completed') ||
      (payload.confirmed_stage_index < payload.stage_index &&
        phase === 'completed')
    ) {
      return null
    }
    confirmedStageIndex = payload.confirmed_stage_index
    confirmedBy = payload.confirmed_by
    confirmedAt = payload.confirmed_at
  } else if (phase === 'completed' || payload.stage_index > 0) {
    return null
  }
  const requiresEffectiveReadback = [
    'observing',
    'awaiting_confirmation',
    'restore_recommended',
    'completed',
  ].includes(phase)
  if (
    (requiresEffectiveReadback &&
      (!effectiveWeights || !Number.isFinite(stageStartedAtMs))) ||
    (effectiveWeights &&
      effectiveWeights.candidate !== payload.candidate_weight_percent) ||
    (phase === 'awaiting_weights' &&
      (stageStartedAt !== undefined ||
        effectiveWeights !== undefined ||
        observation !== undefined ||
        decision !== undefined)) ||
    (observation !== undefined &&
      (!effectiveWeights || !decision || !Number.isFinite(stageStartedAtMs))) ||
    (decision !== undefined && observation === undefined)
  ) {
    return null
  }
  if (observation) {
    const observationStart = Date.parse(observation.windowStart)
    const observationEnd = Date.parse(observation.windowEnd)
    const observedAt = Date.parse(observation.observedAt)
    const stableDigest = observation.stableImageDigest.trim().toLowerCase()
    const candidateDigest = observation.candidateImageDigest
      .trim()
      .toLowerCase()
    if (
      stableDigest === candidateDigest ||
      stableDigest !== observation.stable.imageDigest.trim().toLowerCase() ||
      candidateDigest !==
        observation.candidate.imageDigest.trim().toLowerCase() ||
      observationStart < stageStartedAtMs ||
      observationEnd - observationStart <
        payload.observation_duration_seconds * 1000 ||
      observationEnd > observedAt
    ) {
      return null
    }
  }
  if (
    (phase === 'awaiting_confirmation' &&
      analysis?.status !== 'awaiting_confirmation') ||
    (phase === 'restore_recommended' &&
      analysis?.status !== 'restore_recommended') ||
    (phase === 'completed' &&
      (payload.candidate_weight_percent !== 100 ||
        !observation ||
        analysis?.status !== 'awaiting_confirmation'))
  ) {
    return null
  }

  return {
    schemaVersion: payload.schema_version,
    stageIndex: payload.stage_index,
    candidateWeightPercent: payload.candidate_weight_percent,
    observationDurationSeconds: payload.observation_duration_seconds,
    phase,
    confirmedStageIndex,
    confirmedBy,
    confirmedAt,
    stageStartedAt,
    effectiveWeights,
    observation,
    decisionStatus:
      typeof analysis?.status === 'string' ? analysis.status : undefined,
    decisionReason:
      typeof analysis?.reason === 'string' ? analysis.reason : undefined,
    decisionEvaluatedAt,
  }
}

// eslint-disable-next-line react-refresh/only-export-components
export function parseCanaryRestoreCheckpoint(
  payload: unknown
): CanaryRestoreSnapshot | null {
  if (
    !isRecord(payload) ||
    !isRecord(payload.route) ||
    !isRecord(payload.effective_weights)
  ) {
    return null
  }
  const stableDigest = payload.stable_image_digest
  const candidateDigest = payload.candidate_image_digest
  const connectionId = payload.route.connection_id
  const routeId = payload.route.route_id
  const stableBackendId = payload.route.stable_backend_id
  const candidateBackendId = payload.route.candidate_backend_id
  const revision = payload.route_revision
  const restoredAt = payload.restored_at
  if (
    payload.schema_version !== 1 ||
    !Number.isInteger(payload.stage_index) ||
    !finiteNumber(payload.stage_index) ||
    payload.stage_index < 0 ||
    payload.stage_index >= 10 ||
    typeof stableDigest !== 'string' ||
    !/^sha256:[a-f0-9]{64}$/i.test(stableDigest.trim()) ||
    typeof candidateDigest !== 'string' ||
    !/^sha256:[a-f0-9]{64}$/i.test(candidateDigest.trim()) ||
    stableDigest.trim().toLowerCase() ===
      candidateDigest.trim().toLowerCase() ||
    !Number.isSafeInteger(connectionId) ||
    (finiteNumber(connectionId) && connectionId <= 0) ||
    typeof routeId !== 'string' ||
    routeId.length < 1 ||
    routeId.length > 256 ||
    typeof stableBackendId !== 'string' ||
    stableBackendId.length < 1 ||
    stableBackendId.length > 256 ||
    typeof candidateBackendId !== 'string' ||
    candidateBackendId.length < 1 ||
    candidateBackendId.length > 256 ||
    stableBackendId === candidateBackendId ||
    payload.effective_weights.stable_weight_percent !== 100 ||
    payload.effective_weights.candidate_weight_percent !== 0 ||
    typeof revision !== 'string' ||
    revision.length < 1 ||
    revision.length > 512 ||
    typeof restoredAt !== 'string' ||
    !Number.isFinite(Date.parse(restoredAt))
  ) {
    return null
  }
  return {
    stageIndex: payload.stage_index,
    stableImageDigest: stableDigest,
    candidateImageDigest: candidateDigest,
    route: { id: routeId, stableBackendId, candidateBackendId },
    revision,
    restoredAt,
  }
}

// eslint-disable-next-line react-refresh/only-export-components
export function canaryCheckpointEventTitle(
  event: DeliveryRunEvent
): string | null {
  if (event.event_type === 'canary_restore') {
    return '稳定版 100%、候选版 0% 已回读，等待业务验证。'
  }
  if (event.event_type === 'canary_restore_verification') {
    const verificationMatch = (event.detail ?? '').match(
      /^action=confirm_canary_restore;actor=([^;]+);(.*)$/
    )
    return verificationMatch
      ? `操作员 ${verificationMatch[1]}：${verificationMatch[2]}`
      : '操作员已确认恢复后的业务健康，本次候选发布以失败状态结束。'
  }
  if (event.event_type !== 'canary_checkpoint') return null
  const detail = event.detail ?? ''
  const metricsMatch = detail.match(/^source=canary_metrics;result=([^;]+);/)
  if (metricsMatch) {
    return (
      canaryEventResultLabels[metricsMatch[1]] ??
      '金丝雀指标或网关状态未通过核验，阶段未推进。'
    )
  }
  if (detail.startsWith('source=canary_metrics;')) {
    return '金丝雀指标或网关状态未通过核验，阶段未推进。'
  }
  const confirmationMatch = detail.match(
    /^action=confirm_canary_stage;actor=([^;]+);(.*)$/
  )
  if (confirmationMatch) {
    const [, actor, detail] = confirmationMatch
    return `操作员 ${actor}：${detail}`
  }
  return null
}

function percent(value: number): string {
  return `${Math.round(value)}%`
}

function rate(errors: number, requests: number): string {
  return requests === 0
    ? '无样本'
    : `${((errors / requests) * 100).toFixed(2)}%`
}

function localTime(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).format(new Date(value))
}

function localDateTime(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).format(new Date(value))
}

function timeRange(start: string, end: string): string {
  return `${localTime(start)}–${localTime(end)}`
}

function decisionSummary(checkpoint: CanaryCheckpointSnapshot): string | null {
  if (checkpoint.decisionReason) {
    return reasonLabels[checkpoint.decisionReason] ?? '判定原因需核对'
  }
  if (checkpoint.decisionStatus) {
    return decisionLabels[checkpoint.decisionStatus] ?? '判定状态需核对'
  }
  return null
}

export function CanaryStageCheckpointCard({
  event,
}: {
  event: DeliveryRunEvent
}) {
  if (event.event_type !== 'canary_checkpoint') return null
  const checkpoint = parseCanaryStageCheckpoint(event.payload)
  if (!checkpoint) {
    return (
      <div
        role='status'
        className='mt-2 flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50/70 p-3 text-xs text-amber-950 dark:border-amber-400 dark:bg-amber-200'
      >
        <AlertTriangle className='mt-0.5 size-4 shrink-0' aria-hidden='true' />
        <span>阶段快照无法识别，当前权重和观测状态不能确认。</span>
      </div>
    )
  }

  const observation = checkpoint.observation
  const effective = checkpoint.effectiveWeights
  const totalRequests = observation
    ? observation.stable.requests + observation.candidate.requests
    : 0
  const observedCandidateShare =
    observation && totalRequests > 0
      ? (observation.candidate.requests / totalRequests) * 100
      : null
  const isWarningPhase =
    checkpoint.phase === 'restore_recommended' ||
    checkpoint.phase === 'needs_attention'

  return (
    <section
      aria-label={`金丝雀第 ${checkpoint.stageIndex + 1} 阶段快照`}
      className='mt-2 rounded-lg border border-border bg-card p-3'
    >
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div className='flex min-w-0 items-start gap-2'>
          <Route
            className='mt-0.5 size-4 shrink-0 text-muted-foreground'
            aria-hidden='true'
          />
          <div className='min-w-0'>
            <h6 className='text-sm font-medium'>
              第 {checkpoint.stageIndex + 1} 阶段
            </h6>
            <p className='mt-0.5 text-xs text-muted-foreground'>
              最短观察 {checkpoint.observationDurationSeconds} 秒
            </p>
          </div>
        </div>
        <Badge
          variant='outline'
          className={
            isWarningPhase
              ? 'border-amber-300 bg-amber-50 text-amber-950 dark:border-amber-400 dark:bg-amber-200'
              : 'border-sidebar bg-sidebar text-sidebar-foreground'
          }
        >
          {phaseLabels[checkpoint.phase]}
        </Badge>
      </div>

      <div className='mt-3 grid gap-3 sm:grid-cols-2'>
        <div className='min-w-0'>
          <div className='flex items-baseline justify-between gap-2 text-xs'>
            <span className='text-muted-foreground'>计划流量</span>
            <span className='font-medium tabular-nums'>
              候选 {percent(checkpoint.candidateWeightPercent)}
            </span>
          </div>
          <div
            role='img'
            aria-label={`计划稳定版 ${100 - checkpoint.candidateWeightPercent}%，候选版 ${checkpoint.candidateWeightPercent}%`}
            className='mt-1.5 flex h-2 overflow-hidden rounded-full bg-muted'
          >
            <span
              className='bg-sky-500'
              style={{ width: `${100 - checkpoint.candidateWeightPercent}%` }}
            />
            <span
              className='bg-violet-500'
              style={{ width: `${checkpoint.candidateWeightPercent}%` }}
            />
          </div>
          <div className='mt-1.5 flex justify-between gap-2 text-xs text-muted-foreground'>
            <span>稳定版 {100 - checkpoint.candidateWeightPercent}%</span>
            <span>候选版 {checkpoint.candidateWeightPercent}%</span>
          </div>
        </div>

        <dl className='grid grid-cols-2 gap-x-3 gap-y-2 text-xs'>
          <div>
            <dt className='text-muted-foreground'>已生效权重</dt>
            <dd className='mt-0.5 font-medium tabular-nums'>
              {effective
                ? `稳定 ${effective.stable}% · 候选 ${effective.candidate}%`
                : '尚无权重回读'}
            </dd>
            {effective && checkpoint.stageStartedAt && (
              <dd className='mt-0.5 text-xs text-muted-foreground'>
                阶段开始：
                {localTime(checkpoint.stageStartedAt)}
              </dd>
            )}
          </div>
          <div>
            <dt className='text-muted-foreground'>观测请求占比</dt>
            <dd className='mt-0.5 font-medium tabular-nums'>
              {observedCandidateShare === null
                ? '暂无完整观测'
                : `候选 ${percent(observedCandidateShare)}`}
            </dd>
          </div>
        </dl>
      </div>

      {observation && (
        <>
          <div className='mt-3 border-t border-border pt-3'>
            <div className='flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1'>
              <h6 className='text-xs font-medium'>按镜像版本分组的观测</h6>
              <span className='text-xs text-muted-foreground tabular-nums'>
                {timeRange(observation.windowStart, observation.windowEnd)}
              </span>
            </div>
            <div className='mt-2 grid gap-2 sm:grid-cols-2'>
              {[
                { label: '稳定版', metrics: observation.stable, tone: 'sky' },
                {
                  label: '候选版',
                  metrics: observation.candidate,
                  tone: 'violet',
                },
              ].map(({ label, metrics, tone }) => (
                <div
                  key={label}
                  className='min-w-0 rounded-md bg-muted/40 px-3 py-2'
                >
                  <div className='flex flex-wrap items-baseline justify-between gap-2'>
                    <span
                      className={`text-xs font-medium ${tone === 'sky' ? 'text-sky-700 dark:text-sky-300' : 'text-violet-700 dark:text-violet-300'}`}
                    >
                      {label}
                    </span>
                    <span className='text-xs text-muted-foreground'>
                      健康状态：{healthLabels[metrics.health] ?? '未知'}
                    </span>
                  </div>
                  <dl className='mt-2 grid grid-cols-3 gap-2 text-xs'>
                    <div>
                      <dt className='text-muted-foreground'>请求</dt>
                      <dd className='mt-0.5 font-medium tabular-nums'>
                        {metrics.requests.toLocaleString()}
                      </dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground'>错误率</dt>
                      <dd className='mt-0.5 font-medium tabular-nums'>
                        {rate(metrics.errors, metrics.requests)}
                      </dd>
                    </div>
                    <div>
                      <dt className='text-muted-foreground'>P95</dt>
                      <dd className='mt-0.5 font-medium tabular-nums'>
                        {metrics.p95LatencyMillis === null
                          ? '未知'
                          : `${metrics.p95LatencyMillis} ms`}
                      </dd>
                    </div>
                  </dl>
                </div>
              ))}
            </div>
            <p className='mt-2 flex flex-wrap gap-x-2 gap-y-1 text-xs text-muted-foreground'>
              <span>
                最近采样：
                <time dateTime={observation.observedAt}>
                  {localDateTime(observation.observedAt)}
                </time>
              </span>
              {checkpoint.decisionEvaluatedAt && (
                <span>
                  判定时间：
                  <time dateTime={checkpoint.decisionEvaluatedAt}>
                    {localDateTime(checkpoint.decisionEvaluatedAt)}
                  </time>
                </span>
              )}
              {decisionSummary(checkpoint) && (
                <span>{decisionSummary(checkpoint)}</span>
              )}
            </p>
          </div>
          <details className='mt-2 min-w-0'>
            <summary className='min-h-11 cursor-pointer py-2 text-xs text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring sm:min-h-8'>
              查看稳定版和候选版 digest
            </summary>
            <dl className='grid gap-2 border-t border-border pt-2 text-xs sm:grid-cols-2'>
              <div className='min-w-0'>
                <dt className='text-muted-foreground'>稳定版</dt>
                <dd className='mt-0.5 font-mono break-all'>
                  {observation.stableImageDigest}
                </dd>
              </div>
              <div className='min-w-0'>
                <dt className='text-muted-foreground'>候选版</dt>
                <dd className='mt-0.5 font-mono break-all'>
                  {observation.candidateImageDigest}
                </dd>
              </div>
            </dl>
          </details>
        </>
      )}
    </section>
  )
}

export function CanaryRestoreCheckpointCard({
  event,
  verificationConfirmed = false,
}: {
  event: DeliveryRunEvent
  verificationConfirmed?: boolean
}) {
  if (event.event_type !== 'canary_restore') return null
  const checkpoint = parseCanaryRestoreCheckpoint(event.payload)
  if (
    !checkpoint ||
    event.stage !== 'awaiting_restore_verification' ||
    event.status !== 'running'
  ) {
    return (
      <div
        role='status'
        className='mt-2 flex items-start gap-2 rounded-md border border-amber-300 bg-amber-50/70 p-3 text-xs text-amber-950 dark:border-amber-400 dark:bg-amber-200'
      >
        <AlertTriangle className='mt-0.5 size-4 shrink-0' aria-hidden='true' />
        <span>稳定版恢复回读无法确认，运行锁仍保留。</span>
      </div>
    )
  }

  return (
    <section
      aria-label='金丝雀稳定版恢复结果'
      className='mt-2 rounded-lg border border-border bg-card p-3'
    >
      <div className='flex flex-wrap items-start justify-between gap-2'>
        <div className='flex min-w-0 items-start gap-2'>
          <Check
            className='mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400'
            aria-hidden='true'
          />
          <div className='min-w-0'>
            <h6 className='text-sm font-medium'>稳定版流量已恢复</h6>
            <p className='mt-0.5 text-xs text-muted-foreground'>
              第 {checkpoint.stageIndex + 1} 阶段后回切
            </p>
          </div>
        </div>
        <Badge
          variant='outline'
          className={
            verificationConfirmed
              ? 'border-emerald-300 bg-emerald-50 text-emerald-950 dark:border-emerald-400 dark:bg-emerald-200'
              : 'border-amber-300 bg-amber-50 text-amber-950 dark:border-amber-400 dark:bg-amber-200'
          }
        >
          {verificationConfirmed ? '业务验证已确认' : '等待业务验证'}
        </Badge>
      </div>

      <div className='mt-3 grid grid-cols-2 gap-2'>
        <div className='rounded-md bg-sky-50 px-3 py-2 dark:bg-sky-950/30'>
          <p className='text-xs text-sky-800 dark:text-sky-200'>
            稳定版 · 已生效
          </p>
          <p className='mt-1 text-lg font-semibold text-sky-950 tabular-nums dark:text-sky-100'>
            100%
          </p>
        </div>
        <div className='rounded-md bg-muted/50 px-3 py-2'>
          <p className='text-xs text-muted-foreground'>候选版 · 已生效</p>
          <p className='mt-1 text-lg font-semibold tabular-nums'>0%</p>
        </div>
      </div>
      <p className='mt-3 text-xs leading-5 text-muted-foreground'>
        {verificationConfirmed
          ? '操作员已确认稳定流量恢复后的业务健康。本次候选发布以失败状态结束，资源锁已释放。'
          : '网关已回读稳定版权重。业务健康确认前，发布仍保持运行，资源锁不会释放。'}
      </p>
      <p className='mt-2 text-xs text-muted-foreground'>
        恢复时间：
        <time dateTime={checkpoint.restoredAt}>
          {localDateTime(checkpoint.restoredAt)}
        </time>
      </p>
      <details className='mt-1 min-w-0'>
        <summary className='min-h-11 cursor-pointer py-2 text-xs text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring sm:min-h-8'>
          查看路由和版本身份
        </summary>
        <dl className='grid gap-2 border-t border-border pt-2 text-xs sm:grid-cols-2'>
          <div className='min-w-0'>
            <dt className='text-muted-foreground'>路由</dt>
            <dd className='mt-0.5 font-mono break-all'>
              {checkpoint.route.id}
            </dd>
          </div>
          <div className='min-w-0'>
            <dt className='text-muted-foreground'>Revision</dt>
            <dd className='mt-0.5 font-mono break-all'>
              {checkpoint.revision}
            </dd>
          </div>
          <div className='min-w-0'>
            <dt className='text-muted-foreground'>稳定版 digest</dt>
            <dd className='mt-0.5 font-mono break-all'>
              {checkpoint.stableImageDigest}
            </dd>
          </div>
          <div className='min-w-0'>
            <dt className='text-muted-foreground'>候选版 digest</dt>
            <dd className='mt-0.5 font-mono break-all'>
              {checkpoint.candidateImageDigest}
            </dd>
          </div>
        </dl>
      </details>
    </section>
  )
}
