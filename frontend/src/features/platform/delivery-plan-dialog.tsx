import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  ArrowRight,
  Check,
  ChevronDown,
  CircleAlert,
  CircleCheck,
  CircleDot,
  CircleHelp,
  ClipboardCheck,
  Copy,
  Fingerprint,
  GitBranch,
  LockKeyhole,
  Pause,
  Play,
  RefreshCw,
  ShieldAlert,
  RotateCcw,
} from 'lucide-react'
import { toast } from 'sonner'
import {
  platformApi,
  type DeployLog,
  type DeliveryExecutionAction,
  type DeliveryPlan,
  type DeliveryRunView,
  type DeliveryRunControlAction,
  type ImageReleaseView,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  canaryCheckpointEventTitle,
  CanaryRestoreCheckpointCard,
  CanaryStageCheckpointCard,
  parseCanaryRestoreCheckpoint,
  parseCanaryStageCheckpoint,
} from './canary-stage-checkpoint'
import { repositoryOf } from './component-binding'
import { StatusBadge } from './release-detail'
import {
  deployLogRunning,
  deployStepName,
  parseStepDetail,
} from './release-status'
import { ReleaseStrategyJourney } from './release-strategy-journey'

const strategyLabels: Record<DeliveryPlan['strategy'], string> = {
  rolling: '滚动更新',
  blue_green: '蓝绿发布',
  gateway_canary: '网关金丝雀',
}

const evidenceKindLabels: Record<'ci' | 'smoke' | 'manual', string> = {
  ci: 'CI 测试',
  smoke: '部署后 Smoke',
  manual: '人工验收',
}

function localEventTime(value: string): string {
  return new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  }).format(new Date(value))
}

function SnapshotValue({ label, value }: { label: string; value: string }) {
  return (
    <div className='min-w-0 space-y-1'>
      <div className='text-xs text-muted-foreground'>{label}</div>
      <div className='text-sm font-medium break-all text-foreground'>
        {value || '未提供'}
      </div>
    </div>
  )
}

const preflightStatus = {
  passed: {
    label: '已通过',
    icon: CircleCheck,
    className:
      'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200',
  },
  review: {
    label: '需核对',
    icon: CircleAlert,
    className:
      'border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-100',
  },
  blocked: {
    label: '已阻断',
    icon: CircleAlert,
    className:
      'border-destructive/25 bg-destructive/5 text-destructive dark:border-destructive/40',
  },
  unknown: {
    label: '状态未知',
    icon: CircleHelp,
    className: 'border-border bg-muted/60 text-muted-foreground',
  },
} as const

export function BlueGreenPreflight({
  checks,
  stableLabelKey,
  candidateLabelKey,
  candidateLabelValue,
  removedLabelKeys,
}: {
  checks: NonNullable<DeliveryPlan['snapshot']['checks']>
  stableLabelKey?: string
  candidateLabelKey?: string
  candidateLabelValue?: string
  removedLabelKeys?: string[]
}) {
  const passed = checks.filter((check) => check.status === 'passed').length
  const needsReview = checks.filter((check) => check.status === 'review').length
  const blocked = checks.filter((check) => check.status === 'blocked').length
  const unknown = checks.filter((check) => check.status === 'unknown').length

  return (
    <section
      aria-label='蓝绿环境预检'
      className='overflow-hidden rounded-xl border bg-card'
    >
      <div className='flex flex-col gap-4 border-b bg-gradient-to-r from-sky-50 via-background to-violet-50/70 p-4 sm:flex-row sm:items-center sm:justify-between sm:px-5 dark:from-sky-950/30 dark:via-card dark:to-violet-950/20'>
        <div className='flex min-w-0 items-start gap-3'>
          <div className='flex size-10 shrink-0 items-center justify-center rounded-xl bg-white text-sky-700 shadow-sm ring-1 ring-sky-100 dark:bg-sky-950 dark:text-sky-200 dark:ring-sky-900'>
            <ShieldAlert className='size-5' aria-hidden='true' />
          </div>
          <div className='min-w-0'>
            <div className='text-xs font-semibold tracking-wider text-sky-700 uppercase dark:text-sky-300'>
              Blue-green readiness
            </div>
            <h3 className='mt-0.5 font-semibold'>目标环境预检</h3>
            <p className='mt-1 text-sm leading-5 text-muted-foreground'>
              只读检查会随计划固定；未知项不会被当作通过。
            </p>
          </div>
        </div>
        <div className='flex flex-wrap gap-2 sm:justify-end'>
          <Badge
            variant='outline'
            className='border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200'
          >
            {passed} 项通过
          </Badge>
          {needsReview > 0 && (
            <Badge
              variant='outline'
              className='border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-100'
            >
              {needsReview} 项需核对
            </Badge>
          )}
          {blocked > 0 && <Badge variant='destructive'>{blocked} 项阻断</Badge>}
          {unknown > 0 && (
            <Badge
              variant='outline'
              className='border-border bg-muted/60 text-muted-foreground'
            >
              {unknown} 项未知
            </Badge>
          )}
        </div>
      </div>
      {checks.length === 0 ? (
        <p className='p-4 text-sm text-muted-foreground'>
          此计划没有保存环境预检结果，请重新生成计划。
        </p>
      ) : (
        <ul className='grid gap-2 p-3 sm:grid-cols-2 sm:p-4'>
          {checks.map((check) => {
            const status = preflightStatus[check.status]
            const Icon = status.icon
            return (
              <li
                key={check.key}
                className='min-w-0 rounded-lg border bg-background/80 p-3.5 transition-colors hover:bg-muted/30'
              >
                <div className='flex items-start justify-between gap-3'>
                  <div className='flex min-w-0 items-start gap-2.5'>
                    <Icon
                      className={`mt-0.5 size-4 shrink-0 ${check.status === 'passed' ? 'text-emerald-600 dark:text-emerald-400' : check.status === 'blocked' ? 'text-destructive' : check.status === 'review' ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground'}`}
                      aria-hidden='true'
                    />
                    <div className='min-w-0'>
                      <h4 className='text-sm font-medium'>{check.title}</h4>
                      <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                        {check.detail}
                      </p>
                    </div>
                  </div>
                  <Badge
                    variant='outline'
                    className={`shrink-0 text-xs ${status.className}`}
                  >
                    {status.label}
                  </Badge>
                </div>
              </li>
            )
          })}
        </ul>
      )}
      {(stableLabelKey || candidateLabelKey) && (
        <div className='border-t bg-muted/20 px-4 py-3.5 sm:px-5'>
          <div className='flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between'>
            <div className='min-w-0'>
              <div className='text-xs font-semibold'>候选隔离标签预览</div>
              <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                旧版加入稳定标记；候选模板使用独立运行标记并移除冲突选择标签，避免被旧控制器或现有
                Service 选中。
              </p>
            </div>
            <div className='grid shrink-0 gap-1.5 text-xs sm:text-right'>
              {stableLabelKey && (
                <code className='rounded bg-background px-2 py-1 text-sky-800 dark:text-sky-200'>
                  {stableLabelKey}=stable
                </code>
              )}
              {candidateLabelKey && (
                <code className='rounded bg-background px-2 py-1 text-violet-800 dark:text-violet-200'>
                  {candidateLabelKey}={candidateLabelValue || '发布标识'}
                </code>
              )}
            </div>
          </div>
          {(removedLabelKeys?.length ?? 0) > 0 && (
            <div className='mt-3'>
              <div className='text-xs text-muted-foreground'>
                候选模板会移除这些已有选择标签
              </div>
              <div className='mt-1.5 flex flex-wrap gap-1.5'>
                {removedLabelKeys?.map((key) => (
                  <code
                    key={key}
                    className='max-w-full truncate rounded-md border bg-background px-2 py-1 text-xs text-muted-foreground'
                    title={key}
                  >
                    {key}
                  </code>
                ))}
              </div>
            </div>
          )}
        </div>
      )}
    </section>
  )
}

function ExecutionProgress({
  strategy,
  approvalState,
  deployLogID,
  log,
  run,
  actions,
  loading,
  canReconcile,
  reconciling,
  onReconcile,
}: {
  strategy: DeliveryPlan['strategy']
  approvalState?: string
  deployLogID?: number | null
  log?: DeployLog
  run?: DeliveryRunView
  actions: DeliveryExecutionAction[]
  loading: boolean
  canReconcile: boolean
  reconciling: boolean
  onReconcile: () => void
}) {
  const steps = parseStepDetail(log?.step_detail)
  if (approvalState !== 'approved') return null

  return (
    <section
      aria-label='发布执行进度'
      aria-live='polite'
      className='mt-4 rounded-lg border border-border bg-background p-4'
    >
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div>
          <h4 className='font-medium'>
            {strategy === 'blue_green'
              ? '蓝绿发布进度'
              : strategy === 'gateway_canary'
                ? '网关金丝雀发布进度'
                : '滚动发布进度'}
          </h4>
          <p className='mt-1 text-xs text-muted-foreground'>
            进度来自服务端部署任务记录，页面不推断集群状态。
          </p>
        </div>
        {log && <StatusBadge status={run?.run.status ?? log.status} />}
      </div>
      {!deployLogID ? (
        <p className='mt-3 text-sm text-muted-foreground'>
          审批已通过，执行任务编号尚未同步。请稍后刷新状态；此时不代表部署已开始。
        </p>
      ) : loading ? (
        <p className='mt-3 text-sm text-muted-foreground'>
          正在读取任务 #{deployLogID}…
        </p>
      ) : !log ? (
        <p role='alert' className='mt-3 text-sm text-destructive'>
          无法读取任务 #{deployLogID}。请稍后重试查看状态。
        </p>
      ) : (
        <>
          {run && run.events.length > 0 && (
            <div className='mt-4 rounded-md bg-muted/30 p-3'>
              <div className='flex items-center justify-between gap-3'>
                <h5 className='text-sm font-medium'>发布阶段记录</h5>
                <span className='text-xs text-muted-foreground tabular-nums'>
                  运行 #{run.run.id} · 版本 {run.run.version}
                </span>
              </div>
              <ol
                aria-label='发布阶段记录'
                className='mt-3 space-y-3 border-s border-border ps-4'
              >
                {run.events.map((event) => (
                  <li key={event.id} className='relative min-w-0'>
                    <span
                      className={`absolute -start-[1.3rem] top-1.5 size-2 rounded-full ring-4 ring-background ${event.status === 'success' ? 'bg-emerald-500' : event.status === 'failed' || event.status === 'interrupted' ? 'bg-destructive' : event.status === 'paused' || event.status === 'cancelled' ? 'bg-amber-500' : 'bg-primary'}`}
                      aria-hidden='true'
                    />
                    <div className='flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1'>
                      <span className='text-sm font-medium'>
                        {canaryCheckpointEventTitle(event) ??
                          (event.detail || deployStepName(event.stage))}
                      </span>
                      <time
                        dateTime={event.created_at}
                        className='text-xs text-muted-foreground tabular-nums'
                      >
                        {localEventTime(event.created_at)}
                      </time>
                    </div>
                    <CanaryStageCheckpointCard event={event} />
                    <CanaryRestoreCheckpointCard
                      event={event}
                      verificationConfirmed={run.events.some(
                        (candidate) =>
                          candidate.event_type ===
                            'canary_restore_verification' &&
                          candidate.status === 'failed'
                      )}
                    />
                  </li>
                ))}
              </ol>
            </div>
          )}
          <dl className='mt-3 grid gap-3 text-sm sm:grid-cols-2'>
            <div>
              <dt className='text-xs text-muted-foreground'>任务</dt>
              <dd className='mt-0.5 font-medium'>#{log.id}</dd>
            </div>
            <div>
              <dt className='text-xs text-muted-foreground'>当前步骤</dt>
              <dd className='mt-0.5 font-medium'>
                {log.current_step
                  ? deployStepName(log.current_step)
                  : log.status === 'success'
                    ? '部署已完成'
                    : log.status === 'interrupted'
                      ? '进程中断，需核对实际状态'
                      : log.status === 'failed'
                        ? '任务失败'
                        : '等待执行器领取'}
              </dd>
            </div>
          </dl>
          {steps.length > 0 && (
            <ol className='mt-4 grid gap-2 sm:grid-cols-2'>
              {steps.map((step, index) => (
                <li
                  key={`${step.step}-${index}`}
                  className='flex min-w-0 items-center gap-2 rounded-md bg-muted/50 px-3 py-2 text-sm'
                >
                  <span
                    aria-label={step.success ? '成功' : '失败'}
                    className={
                      step.success
                        ? 'font-semibold text-emerald-700 dark:text-emerald-300'
                        : 'font-semibold text-destructive'
                    }
                  >
                    {step.success ? '✓' : '×'}
                  </span>
                  <span className='min-w-0 truncate'>
                    {deployStepName(step.step)}
                  </span>
                </li>
              ))}
              {deployLogRunning(log.status) && log.current_step && (
                <li className='flex min-w-0 items-center gap-2 rounded-md border border-blue-200 bg-blue-50/70 px-3 py-2 text-sm text-blue-900 dark:border-blue-900 dark:bg-blue-950/30 dark:text-blue-100'>
                  <span className='size-2 shrink-0 animate-pulse rounded-full bg-blue-600' />
                  <span className='min-w-0 truncate'>
                    正在执行：{deployStepName(log.current_step)}
                  </span>
                </li>
              )}
            </ol>
          )}
          {actions.map((action) => (
            <div
              key={action.id}
              className='mt-4 rounded-md border border-border/70 p-3'
            >
              <div className='flex flex-wrap items-center justify-between gap-2'>
                <h5 className='text-sm font-medium'>镜像写入与集群回读</h5>
                <Badge
                  variant='outline'
                  className={
                    action.status === 'applied'
                      ? 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-200'
                      : action.status === 'unknown'
                        ? 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-200'
                        : action.status === 'not_applied'
                          ? 'border-destructive/30 bg-destructive/10 text-destructive'
                          : 'border-border bg-muted text-muted-foreground'
                  }
                >
                  {action.status === 'applied'
                    ? '已读回确认'
                    : action.status === 'unknown'
                      ? '结果未知'
                      : action.status === 'not_applied'
                        ? '未生效'
                        : '已记录意图'}
                </Badge>
              </div>
              <dl className='mt-2 grid gap-2 text-xs sm:grid-cols-2'>
                <div className='min-w-0'>
                  <dt className='text-muted-foreground'>原镜像</dt>
                  <dd className='mt-0.5 font-mono break-all'>
                    {action.intent.previous_image || '未记录'}
                  </dd>
                </div>
                <div className='min-w-0'>
                  <dt className='text-muted-foreground'>目标镜像</dt>
                  <dd className='mt-0.5 font-mono break-all'>
                    {action.intent.target_image || '未记录'}
                  </dd>
                </div>
                <div className='min-w-0 sm:col-span-2'>
                  <dt className='text-muted-foreground'>观察到的镜像</dt>
                  <dd className='mt-0.5 font-mono break-all'>
                    {action.observed?.current_image ||
                      (action.observed?.readback === 'failed'
                        ? '回读失败'
                        : '尚未回读')}
                  </dd>
                </div>
              </dl>
            </div>
          ))}
          {log.status === 'interrupted' && (
            <div className='mt-3 flex flex-col gap-3 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 sm:flex-row sm:items-center sm:justify-between dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'>
              <div>
                <p className='font-medium'>执行中断，需要核对集群</p>
                <p className='mt-1 text-xs leading-relaxed opacity-85'>
                  仅当目标镜像已生效时继续健康检查；仍是原镜像或出现其它镜像时会保持中断，不会重放写入。
                </p>
              </div>
              {canReconcile &&
                actions.some(
                  (action) =>
                    action.status === 'intent' ||
                    action.status === 'unknown' ||
                    action.status === 'applied'
                ) && (
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    className='min-h-11 shrink-0 border-amber-400 bg-background text-amber-950 hover:bg-amber-100 sm:min-h-8 dark:border-amber-800 dark:text-amber-100 dark:hover:bg-amber-950'
                    disabled={reconciling}
                    onClick={onReconcile}
                  >
                    <RefreshCw
                      className={`size-4 ${reconciling ? 'motion-safe:animate-spin' : ''}`}
                      aria-hidden='true'
                    />
                    {reconciling ? '正在核对' : '核对集群并继续'}
                  </Button>
                )}
            </div>
          )}
          {log.error_message && (
            <p role='alert' className='mt-3 text-sm text-destructive'>
              {log.error_message}
            </p>
          )}
          {strategy === 'blue_green' && run && (
            <BlueGreenRunControls run={run} />
          )}
          {strategy === 'rolling' && run && <RollingRunControls run={run} />}
          {strategy === 'gateway_canary' && run && (
            <>
              <CanaryRunControls run={run} />
              <CanaryRestoreVerificationControl run={run} />
            </>
          )}
          {log.started_at && (
            <p className='mt-3 text-xs text-muted-foreground'>
              开始时间：{new Date(log.started_at).toLocaleString()}
              {log.finished_at &&
                ` · 结束时间：${new Date(log.finished_at).toLocaleString()}`}
            </p>
          )}
        </>
      )}
    </section>
  )
}

export function CanaryRunControls({ run }: { run: DeliveryRunView }) {
  const role = useAuthStore((state) => state.auth.user?.role)
  const queryClient = useQueryClient()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [confirmQueuedCancel, setConfirmQueuedCancel] = useState(false)
  const canOperate = role === 'operator' || role === 'admin'
  const latestEvent = [...(run.events ?? [])]
    .reverse()
    .find((event) => event.event_type === 'canary_checkpoint')
  const checkpoint = latestEvent
    ? parseCanaryStageCheckpoint(latestEvent.payload)
    : null
  const stages = run.plan.snapshot.policy?.canary_analysis?.stages ?? []
  const nextStage = checkpoint ? stages[checkpoint.stageIndex + 1] : undefined
  const finalStage =
    checkpoint?.candidateWeightPercent === 100 && nextStage === undefined
  const paused = run.run.status === 'paused'
  const canaryPreWriteStage =
    run.run.current_stage === 'queued' ||
    run.run.current_stage === 'started' ||
    run.run.current_stage === 'resolve_digest' ||
    run.run.current_stage === 'preparing' ||
    run.run.current_stage === 'preflight'
  const canCancelBeforeExternalAction =
    run.run.strategy === 'gateway_canary' &&
    (run.run.status === 'queued' || run.run.status === 'running') &&
    canaryPreWriteStage &&
    (run.actions?.length ?? 0) === 0
  const workerClaimed = run.deploy_log.status === 'running'
  const atConfirmationGate =
    run.run.strategy === 'gateway_canary' &&
    (run.run.status === 'running' || paused) &&
    checkpoint?.phase === 'awaiting_confirmation' &&
    run.run.current_stage === `canary_stage_${checkpoint.stageIndex + 1}`
  const canAdvance =
    atConfirmationGate && (nextStage !== undefined || finalStage)

  const confirmation = useMutation({
    mutationFn: () =>
      platformApi.deliveryRunAction(run.run.id, {
        action: 'confirm_canary_stage',
        expected_version: run.run.version,
        idempotency_key: crypto.randomUUID(),
      }),
    onSuccess: () => {
      toast.success(
        finalStage
          ? '已确认金丝雀发布完成'
          : '已确认观测结果，等待下一阶段权重回读'
      )
      setConfirmOpen(false)
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-execution', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
    },
    onError: (error) => {
      const detail = (error as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      toast.error(detail || '阶段确认未完成，请刷新观测与网关状态后重试')
      setConfirmOpen(false)
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
    },
  })

  const control = useMutation({
    mutationFn: (action: 'pause' | 'continue' | 'cancel') =>
      platformApi.deliveryRunAction(run.run.id, {
        action,
        expected_version: run.run.version,
        idempotency_key: crypto.randomUUID(),
      }),
    onSuccess: (_result, action) => {
      toast.success(
        action === 'pause'
          ? '已暂停在当前金丝雀确认关口，观测快照和资源锁仍保留'
          : action === 'continue'
            ? '已继续当前金丝雀阶段；确认前仍会校验观测时效'
            : '尚未产生外部动作的金丝雀发布已取消'
      )
      setConfirmQueuedCancel(false)
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-execution', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
    },
    onError: (error) => {
      const detail = (error as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      toast.error(detail || '金丝雀控制动作未完成，请刷新发布状态后重试')
      setConfirmQueuedCancel(false)
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
    },
  })

  if (canCancelBeforeExternalAction) {
    return (
      <section
        aria-label='金丝雀取消控制'
        className='mt-4 overflow-hidden rounded-xl border border-border bg-card shadow-sm'
      >
        <div className='flex flex-col gap-4 p-4 sm:flex-row sm:items-center sm:justify-between sm:p-5'>
          <div className='flex min-w-0 items-start gap-3'>
            <div className='flex size-10 shrink-0 items-center justify-center rounded-xl bg-muted text-muted-foreground ring-1 ring-border'>
              <CircleDot className='size-5' aria-hidden='true' />
            </div>
            <div className='min-w-0'>
              <div className='flex flex-wrap items-center gap-2'>
                <h3 className='text-sm font-semibold'>
                  {workerClaimed ? '金丝雀任务正在准备' : '金丝雀发布等待执行'}
                </h3>
                <Badge variant='outline'>
                  {workerClaimed ? 'Worker 已领取' : '等待执行'}
                </Badge>
              </div>
              <p className='mt-1 max-w-2xl text-sm leading-relaxed text-muted-foreground'>
                只要还没有网关或集群动作记录，就可以安全取消；Worker
                已领取但尚未开始外部写入时也适用。服务端会串行核对任务认领和动作日志；已有动作时会拒绝取消并保留恢复状态。
              </p>
            </div>
          </div>
          <Button
            type='button'
            variant='outline'
            className='min-h-11 shrink-0 gap-2 text-destructive hover:text-destructive sm:min-w-36'
            disabled={!canOperate || control.isPending}
            onClick={() => setConfirmQueuedCancel(true)}
          >
            {control.isPending ? '正在核对…' : '取消发布'}
          </Button>
        </div>
        {!canOperate && (
          <div className='border-t border-border px-4 py-2.5 text-xs text-muted-foreground sm:px-5'>
            需要发布操作员或管理员权限才能取消金丝雀发布。
          </div>
        )}
        {control.isPending && (
          <div
            role='status'
            className='border-t border-border px-4 py-2.5 text-xs text-muted-foreground sm:px-5'
          >
            正在核对任务认领和外部动作记录…
          </div>
        )}
        <Dialog
          open={confirmQueuedCancel}
          onOpenChange={setConfirmQueuedCancel}
        >
          <DialogContent>
            <DialogHeader>
              <DialogTitle>确认取消尚未产生外部动作的金丝雀任务？</DialogTitle>
              <DialogDescription className='leading-6'>
                服务端会在同一事务中核对 Worker
                认领与动作日志。没有动作时撤销认领、结束任务并释放资源锁；已有网关或集群动作时会拒绝取消，保留状态供恢复流程核对。
              </DialogDescription>
            </DialogHeader>
            <DialogFooter className='flex-col sm:flex-row'>
              <Button
                type='button'
                variant='outline'
                className='min-h-11'
                disabled={control.isPending}
                onClick={() => setConfirmQueuedCancel(false)}
              >
                返回发布
              </Button>
              <Button
                type='button'
                variant='destructive'
                className='min-h-11'
                disabled={!canOperate || control.isPending}
                onClick={() => control.mutate('cancel')}
              >
                {control.isPending ? '正在核对并取消…' : '确认取消发布'}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </section>
    )
  }

  if (!atConfirmationGate || !checkpoint || !checkpoint.effectiveWeights)
    return null

  return (
    <section
      aria-label='金丝雀阶段人工确认与控制'
      className='mt-4 overflow-hidden rounded-xl border border-primary/20 bg-gradient-to-br from-primary/[0.06] via-card to-card shadow-sm'
    >
      <div className='flex flex-col gap-4 p-4 sm:flex-row sm:items-center sm:justify-between sm:p-5'>
        <div className='flex min-w-0 items-start gap-3'>
          <div className='flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary ring-1 ring-primary/15'>
            <CircleCheck className='size-5' aria-hidden='true' />
          </div>
          <div className='min-w-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <h3 className='text-sm font-semibold'>
                {paused ? '金丝雀阶段已暂停' : '阶段观测通过'}
              </h3>
              <Badge variant='outline' className='bg-background/80'>
                第 {checkpoint.stageIndex + 1} 阶段
              </Badge>
            </div>
            <p className='mt-1 text-sm leading-relaxed text-muted-foreground'>
              已读回候选流量 {checkpoint.effectiveWeights.candidate}
              %；指标在策略阈值内。确认后平台只推进阶段记录，下一档流量仍须由网关适配器写入并回读。
            </p>
            <p className='mt-2 text-xs text-muted-foreground'>
              {paused
                ? '继续后仍停在本阶段；确认前会重新检查观测时效，不会自动调整网关权重。'
                : finalStage
                  ? '确认后按健康的 100% 候选观测完成本次发布。'
                  : `下一阶段计划候选流量 ${nextStage?.candidate_weight_percent ?? '—'}%。`}
            </p>
          </div>
        </div>
        <div className='flex shrink-0 flex-col gap-2 sm:flex-row'>
          {paused ? (
            <Button
              type='button'
              className='min-h-11 gap-2 sm:min-w-40'
              disabled={!canOperate || control.isPending}
              onClick={() => control.mutate('continue')}
            >
              <Play className='size-4' aria-hidden='true' />
              继续当前阶段
            </Button>
          ) : (
            <>
              {canAdvance && (
                <Button
                  type='button'
                  className='min-h-11 gap-2 sm:min-w-44'
                  disabled={
                    !canOperate || confirmation.isPending || control.isPending
                  }
                  onClick={() => setConfirmOpen(true)}
                >
                  <Check className='size-4' aria-hidden='true' />
                  {finalStage ? '确认金丝雀完成' : '确认进入下一阶段'}
                </Button>
              )}
              <Button
                type='button'
                variant='outline'
                className='min-h-11 gap-2'
                disabled={
                  !canOperate || confirmation.isPending || control.isPending
                }
                onClick={() => control.mutate('pause')}
              >
                <Pause className='size-4' aria-hidden='true' />
                暂停此阶段
              </Button>
            </>
          )}
        </div>
      </div>
      {!canOperate && (
        <div className='border-t border-primary/10 px-4 py-2.5 text-xs text-muted-foreground sm:px-5'>
          {paused
            ? '需要发布操作员或管理员继续运行。'
            : '需要发布操作员或管理员确认或暂停此阶段。'}
        </div>
      )}
      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              {finalStage ? '确认金丝雀发布完成？' : '确认进入下一阶段？'}
            </DialogTitle>
            <DialogDescription>
              已验证本阶段网关有效权重为稳定版{' '}
              {checkpoint.effectiveWeights.stable}% / 候选版{' '}
              {checkpoint.effectiveWeights.candidate}
              %，且完整观察窗口内的指标满足策略阈值。
              {finalStage
                ? '确认后本次发布将以 100% 候选流量完成。'
                : `确认后阶段计划将推进到候选流量 ${nextStage?.candidate_weight_percent}%；平台仍会等待网关实际写入和权重回读。`}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type='button'
              variant='outline'
              className='min-h-11'
              disabled={confirmation.isPending}
              onClick={() => setConfirmOpen(false)}
            >
              再核对一下
            </Button>
            <Button
              type='button'
              className='min-h-11'
              disabled={confirmation.isPending || !canOperate}
              onClick={() => confirmation.mutate()}
            >
              {confirmation.isPending ? '正在保存确认…' : '确认并继续'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}

export function CanaryRestoreVerificationControl({
  run,
}: {
  run: DeliveryRunView
}) {
  const role = useAuthStore((state) => state.auth.user?.role)
  const queryClient = useQueryClient()
  const [confirmOpen, setConfirmOpen] = useState(false)
  const canOperate = role === 'operator' || role === 'admin'
  const restoreEvent = [...run.events]
    .reverse()
    .find(
      (event) =>
        event.event_type === 'canary_restore' &&
        event.status === 'running' &&
        event.stage === 'awaiting_restore_verification'
    )
  const restoredCheckpoint = restoreEvent
    ? parseCanaryRestoreCheckpoint(restoreEvent.payload)
    : null
  const awaitingVerification =
    run.run.strategy === 'gateway_canary' &&
    run.run.status === 'running' &&
    run.run.current_stage === 'awaiting_restore_verification' &&
    restoredCheckpoint !== null

  const confirmation = useMutation({
    mutationFn: () =>
      platformApi.deliveryRunAction(run.run.id, {
        action: 'confirm_canary_restore',
        expected_version: run.run.version,
        idempotency_key: crypto.randomUUID(),
      }),
    onSuccess: () => {
      toast.success('已确认恢复后的业务健康；本次候选发布已结束')
      setConfirmOpen(false)
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-execution', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
    },
    onError: (error) => {
      const detail = (error as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      toast.error(detail || '恢复确认未完成，请刷新后重新核对业务状态')
      setConfirmOpen(false)
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
    },
  })

  if (!awaitingVerification) return null

  return (
    <section
      aria-label='金丝雀恢复后业务验证'
      className='mt-4 overflow-hidden rounded-xl border border-amber-300/70 bg-gradient-to-br from-amber-50/80 via-card to-card shadow-sm dark:border-amber-900/70 dark:from-amber-950/30'
    >
      <div className='flex flex-col gap-4 p-4 sm:flex-row sm:items-center sm:justify-between sm:p-5'>
        <div className='flex min-w-0 items-start gap-3'>
          <div className='flex size-10 shrink-0 items-center justify-center rounded-xl bg-amber-100 text-amber-900 ring-1 ring-amber-200 dark:bg-amber-200 dark:text-amber-950 dark:ring-amber-400'>
            <ShieldAlert className='size-5' aria-hidden='true' />
          </div>
          <div className='min-w-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <h3 className='text-sm font-semibold'>确认恢复后的业务状态</h3>
              <Badge
                variant='outline'
                className='border-amber-300 bg-amber-100 text-amber-950 dark:border-amber-400 dark:bg-amber-200'
              >
                等待人工验证
              </Badge>
            </div>
            <p className='mt-1 text-sm leading-relaxed text-muted-foreground'>
              稳定版 100% / 候选版 0%
              已由网关回读确认。请先用业务探针或核心交易流程确认恢复正常；此确认会以失败状态结束本次候选发布并释放资源锁。
            </p>
          </div>
        </div>
        <Button
          type='button'
          variant='outline'
          className='min-h-11 shrink-0 border-amber-400 bg-background text-amber-950 hover:bg-amber-100 sm:min-w-44 dark:border-amber-400 dark:bg-amber-200 dark:hover:bg-amber-300'
          disabled={!canOperate || confirmation.isPending}
          onClick={() => setConfirmOpen(true)}
        >
          <Check className='size-4' aria-hidden='true' />
          确认业务已恢复
        </Button>
      </div>
      {!canOperate && (
        <div className='border-t border-amber-200/80 px-4 py-2.5 text-xs text-muted-foreground sm:px-5 dark:border-amber-900/70'>
          需要发布操作员或管理员确认恢复后的业务状态。
        </div>
      )}
      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>确认稳定版业务已恢复？</DialogTitle>
            <DialogDescription>
              这只记录人工业务核验，不会将候选版本标记为成功。确认后本次发布以失败状态结束并释放资源锁；如业务仍有异常，请关闭此窗口并保持运行待核对。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type='button'
              variant='outline'
              className='min-h-11'
              disabled={confirmation.isPending}
              onClick={() => setConfirmOpen(false)}
            >
              继续核对
            </Button>
            <Button
              type='button'
              className='min-h-11'
              disabled={confirmation.isPending || !canOperate}
              onClick={() => confirmation.mutate()}
            >
              {confirmation.isPending ? '正在保存确认…' : '确认恢复并结束发布'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}

export function RollingRunControls({ run }: { run: DeliveryRunView }) {
  const role = useAuthStore((state) => state.auth.user?.role)
  const queryClient = useQueryClient()
  const [confirmCancel, setConfirmCancel] = useState(false)
  const paused = run.run.status === 'paused'
  const beforeImageWrite =
    run.run.current_stage === 'started' ||
    run.run.current_stage === 'resolve_digest'
  const atRolloutCheck = run.run.current_stage === 'rollout_wait'
  const active = run.run.status === 'running' || paused
  const canOperate = role === 'operator' || role === 'admin'

  const controls = useMutation({
    mutationFn: (action: 'pause' | 'continue' | 'cancel') =>
      platformApi.deliveryRunAction(run.run.id, {
        action,
        expected_version: run.run.version,
        idempotency_key: crypto.randomUUID(),
      }),
    onSuccess: (_result, action) => {
      toast.success(
        action === 'pause'
          ? '平台后续检查已暂停；Kubernetes rollout 仍会继续'
          : action === 'continue'
            ? '滚动发布已重新排队'
            : '已在镜像写入前取消发布'
      )
      setConfirmCancel(false)
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-execution', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
    },
    onError: (error) => {
      const detail = (error as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      toast.error(detail || '控制动作未完成，请刷新发布状态后重试')
      setConfirmCancel(false)
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-execution', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
    },
  })

  if (!active || (!beforeImageWrite && !atRolloutCheck)) return null

  return (
    <div className='mt-4 overflow-hidden rounded-xl border border-border/80 bg-card shadow-sm'>
      <div className='flex flex-col gap-4 p-4 sm:flex-row sm:items-start sm:justify-between sm:p-5'>
        <div className='flex min-w-0 gap-3'>
          <div className='flex size-10 shrink-0 items-center justify-center rounded-xl bg-muted text-foreground'>
            {paused ? (
              <Play className='size-5' aria-hidden='true' />
            ) : (
              <Pause className='size-5' aria-hidden='true' />
            )}
          </div>
          <div className='min-w-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <h5 className='font-semibold'>
                {paused ? '滚动发布已暂停' : '滚动发布控制'}
              </h5>
              <Badge variant='outline'>版本 {run.run.version}</Badge>
            </div>
            <p className='mt-1 max-w-2xl text-sm leading-6 text-muted-foreground'>
              {atRolloutCheck
                ? '暂停只停止平台后续健康检查。Kubernetes Deployment 控制器仍会继续替换 Pod；如需恢复旧镜像，请使用镜像回退。'
                : '当前镜像尚未写入 Deployment。此时取消会结束本次发布并释放目标锁，不会修改集群。'}
            </p>
            {!canOperate && (
              <p className='mt-2 text-xs text-muted-foreground'>
                需要操作员权限才能控制发布。
              </p>
            )}
          </div>
        </div>
        {canOperate && (
          <div className='flex shrink-0 flex-col gap-2 sm:flex-row'>
            {atRolloutCheck && !paused && (
              <Button
                type='button'
                variant='outline'
                className='min-h-11'
                disabled={controls.isPending}
                onClick={() => controls.mutate('pause')}
              >
                <Pause className='size-4' aria-hidden='true' />
                暂停平台检查
              </Button>
            )}
            {atRolloutCheck && paused && (
              <Button
                type='button'
                className='min-h-11'
                disabled={controls.isPending}
                onClick={() => controls.mutate('continue')}
              >
                <Play className='size-4' aria-hidden='true' />
                继续检查
              </Button>
            )}
            {beforeImageWrite && !paused && (
              <Button
                type='button'
                variant='destructive'
                className='min-h-11'
                disabled={controls.isPending}
                onClick={() => setConfirmCancel(true)}
              >
                取消发布
              </Button>
            )}
          </div>
        )}
      </div>
      <Dialog open={confirmCancel} onOpenChange={setConfirmCancel}>
        <DialogContent className='sm:max-w-lg'>
          <DialogHeader>
            <DialogTitle>在镜像写入前取消发布？</DialogTitle>
            <DialogDescription className='leading-6'>
              平台会结束本次滚动发布并释放资源锁。Deployment
              尚未被本次任务修改；后续如需发布，请重新提交审批。
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type='button'
              variant='outline'
              className='min-h-11'
              disabled={controls.isPending}
              onClick={() => setConfirmCancel(false)}
            >
              返回
            </Button>
            <Button
              type='button'
              variant='destructive'
              className='min-h-11'
              disabled={controls.isPending}
              onClick={() => controls.mutate('cancel')}
            >
              {controls.isPending ? '正在取消…' : '确认取消'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

export function BlueGreenRunControls({ run }: { run: DeliveryRunView }) {
  const role = useAuthStore((state) => state.auth.user?.role)
  const queryClient = useQueryClient()
  const [now, setNow] = useState(0)
  const [viewportHeight, setViewportHeight] = useState(
    () => window.visualViewport?.height ?? window.innerHeight
  )
  const [confirming, setConfirming] = useState<DeliveryRunControlAction | null>(
    null
  )
  const [
    recoveryCompatibilityAcknowledged,
    setRecoveryCompatibilityAcknowledged,
  ] = useState(false)
  const confirmationTriggerRef = useRef<HTMLButtonElement | null>(null)
  const openConfirmation = (
    action: DeliveryRunControlAction,
    trigger: HTMLButtonElement
  ) => {
    confirmationTriggerRef.current = trigger
    setRecoveryCompatibilityAcknowledged(false)
    setConfirming(action)
  }
  const closeConfirmation = () => {
    setConfirming(null)
    setRecoveryCompatibilityAcknowledged(false)
    requestAnimationFrame(() => {
      if (confirmationTriggerRef.current?.isConnected) {
        confirmationTriggerRef.current.focus()
      }
    })
  }
  useEffect(() => {
    const updateNow = () => setNow(Date.now())
    updateNow()
    const interval = window.setInterval(updateNow, 30_000)
    return () => window.clearInterval(interval)
  }, [])
  useEffect(() => {
    const updateViewportHeight = () =>
      setViewportHeight(window.visualViewport?.height ?? window.innerHeight)
    window.addEventListener('resize', updateViewportHeight)
    window.visualViewport?.addEventListener('resize', updateViewportHeight)
    return () => {
      window.removeEventListener('resize', updateViewportHeight)
      window.visualViewport?.removeEventListener('resize', updateViewportHeight)
    }
  }, [])
  const compactViewport = viewportHeight < 448
  const controls = useMutation({
    mutationFn: (request: {
      action: DeliveryRunControlAction
      recoveryCompatibilityAcknowledged?: boolean
    }) =>
      platformApi.deliveryRunAction(run.run.id, {
        action: request.action,
        expected_version: run.run.version,
        idempotency_key: crypto.randomUUID(),
        recovery_compatibility_acknowledged:
          request.recoveryCompatibilityAcknowledged ?? false,
      }),
    onSuccess: (result, request) => {
      const action = request.action
      const stage = result.data.run.current_stage
      if (
        (action === 'cancel' || action === 'restore_stable') &&
        stage !== 'restored'
      ) {
        toast.error('生产入口尚未确认切回稳定版，请先核对集群状态')
      } else {
        toast.success(
          action === 'pause'
            ? '发布推进已暂停，当前生产流量保持不变'
            : action === 'continue'
              ? '已继续发布推进，仍等待操作员确认'
              : action === 'cancel'
                ? '发布已取消，生产流量入口已切回稳定版；配置未回退'
                : action === 'confirm_cutover'
                  ? '候选已切入生产 Service，请进行业务验证'
                  : action === 'confirm_live_verification'
                    ? '业务验证已确认，发布完成'
                    : action === 'restore_stable'
                      ? '生产流量入口已切回稳定版；配置未回退'
                      : action === 'cleanup_candidate'
                        ? '未启用的候选资源已清理'
                        : stage === 'awaiting_live_verification'
                          ? '已核对生产 Service 正在使用候选版本'
                          : '已核对集群，候选准备状态已恢复'
        )
      }
      if (confirming != null) closeConfirmation()
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-execution', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
    },
    onError: (error) => {
      const detail = (error as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      toast.error(detail || '控制动作未完成，请先查看集群状态')
      if (confirming != null) closeConfirmation()
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-execution', run.run.deploy_log_id],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', run.run.deploy_log_id],
      })
    },
  })
  const stage = run.run.current_stage
  const canOperate = role === 'operator' || role === 'admin'
  const isPaused = run.run.status === 'paused'
  const needsAttention = stage === 'needs_attention'
  const isAttentionState = isPaused || needsAttention
  const candidateRetentionHours = run.plan.snapshot.candidate_retention_hours
  const restoredAt = run.deploy_log.finished_at
    ? new Date(run.deploy_log.finished_at)
    : null
  const cleanupAvailableAt =
    restoredAt && candidateRetentionHours
      ? new Date(
          restoredAt.getTime() + candidateRetentionHours * 60 * 60 * 1000
        )
      : null
  const cleanupAlreadyComplete = [
    'blue-green:cleanup:candidate-service',
    'blue-green:cleanup:candidate-deployment',
  ].every((actionKey) =>
    run.actions.some(
      (action) => action.action_key === actionKey && action.status === 'applied'
    )
  )
  const isRestored =
    (run.run.status === 'failed' || run.run.status === 'cancelled') &&
    stage === 'restored'
  const retentionElapsed =
    cleanupAvailableAt != null && now >= cleanupAvailableAt.getTime()
  const requiresRecoveryCompatibilityAcknowledgement =
    confirming === 'restore_stable' || confirming === 'cancel'
  const title =
    confirming === 'cancel'
      ? '取消发布并恢复稳定版本？'
      : confirming === 'confirm_cutover'
        ? '确认切换生产流量？'
        : confirming === 'confirm_live_verification'
          ? '确认业务验证通过？'
          : confirming === 'cleanup_candidate'
            ? '清理未启用的候选资源？'
            : '恢复稳定版本？'
  const description =
    confirming === 'cancel'
      ? '平台会核对生产 Service，并恢复已记录的稳定入口。'
      : confirming === 'confirm_cutover'
        ? `即将把生产 Service「${run.plan.snapshot.target.service_name}」切到已就绪的候选版本。旧 Deployment 会保留；切换后请立即验证关键业务。`
        : confirming === 'confirm_live_verification'
          ? '确认你已验证关键业务请求、错误率和核心流程正常。确认后平台会把候选 Deployment 设为当前活动绑定并完成本次发布。'
          : confirming === 'cleanup_candidate'
            ? '平台会再次核对生产 Service、稳定 Deployment、应用绑定和候选 UID，只删除本计划创建的预览 Service 与候选 Deployment。稳定版本及生产入口不会被删除或修改。'
            : '平台会将生产 Service 指回稳定 Deployment，并回读稳定端点。'
  const actionLabel =
    confirming === 'cancel'
      ? '确认取消并恢复'
      : confirming === 'confirm_cutover'
        ? '切换生产 Service'
        : confirming === 'confirm_live_verification'
          ? '确认并完成发布'
          : confirming === 'cleanup_candidate'
            ? '清理候选资源'
            : '恢复稳定版本'

  if (isRestored) {
    if (cleanupAlreadyComplete) return null
    return (
      <div
        role='region'
        aria-label='蓝绿发布控制'
        className='mt-4 rounded-xl border border-amber-200 bg-amber-50/80 p-4 dark:border-amber-900 dark:bg-amber-950/25'
      >
        <div className='flex items-start gap-3'>
          <div className='flex size-9 shrink-0 items-center justify-center rounded-lg bg-background text-amber-700 shadow-sm dark:text-amber-200'>
            <ShieldAlert className='size-5' aria-hidden='true' />
          </div>
          <div className='min-w-0 flex-1'>
            <div className='flex flex-wrap items-center gap-2'>
              <h5 className='font-semibold text-amber-950 dark:text-amber-100'>
                {run.run.status === 'cancelled'
                  ? '发布已取消，生产入口已切回稳定版'
                  : '生产入口已切回稳定版'}
              </h5>
              <Badge
                variant='outline'
                className='border-amber-300 text-amber-900 dark:text-amber-100'
              >
                候选资源保留
              </Badge>
            </div>
            <p className='mt-1 text-sm leading-6 text-amber-950/75 dark:text-amber-100/75'>
              计划要求恢复后至少保留 {candidateRetentionHours ?? '未配置'}{' '}
              小时。期限届满后，可清理本计划创建的候选 Deployment 与预览
              Service；生产 Service 和原稳定 Deployment 会保留。
            </p>
            {cleanupAvailableAt && !retentionElapsed && (
              <p className='mt-2 text-xs text-muted-foreground'>
                可清理时间：{cleanupAvailableAt.toLocaleString()}
              </p>
            )}
            {!cleanupAvailableAt && (
              <p className='mt-2 text-xs text-destructive'>
                此运行缺少有效保留策略，平台不会开放清理入口。
              </p>
            )}
            {canOperate ? (
              <Button
                className='mt-3 min-h-11'
                variant='outline'
                disabled={controls.isPending || !retentionElapsed}
                onClick={() => setConfirming('cleanup_candidate')}
              >
                <RefreshCw className='size-4' aria-hidden='true' />
                {controls.isPending ? '正在清理候选' : '清理未启用候选'}
              </Button>
            ) : (
              <p className='mt-3 text-xs text-muted-foreground'>
                需要操作员权限才能清理候选资源。
              </p>
            )}
          </div>
        </div>
        <Dialog
          open={confirming === 'cleanup_candidate'}
          onOpenChange={(open) => !open && setConfirming(null)}
        >
          <DialogContent className='sm:max-w-lg'>
            <DialogHeader>
              <DialogTitle>{title}</DialogTitle>
              <DialogDescription className='leading-6'>
                {description}
              </DialogDescription>
            </DialogHeader>
            <div className='rounded-lg bg-muted/50 p-3 text-sm'>
              <div className='text-xs text-muted-foreground'>候选镜像</div>
              <div className='mt-1 font-mono break-all'>
                {run.plan.snapshot.artifact.image}
              </div>
              <div className='mt-3 text-xs text-muted-foreground'>保留期限</div>
              <div className='mt-1 font-medium'>
                {candidateRetentionHours} 小时 · 已于{' '}
                {cleanupAvailableAt?.toLocaleString()} 届满
              </div>
            </div>
            <DialogFooter>
              <Button
                variant='outline'
                className='min-h-11'
                disabled={controls.isPending}
                onClick={() => setConfirming(null)}
              >
                继续保留
              </Button>
              <Button
                variant='destructive'
                className='min-h-11'
                disabled={controls.isPending}
                onClick={() => controls.mutate({ action: 'cleanup_candidate' })}
              >
                {controls.isPending ? '正在核对并清理…' : '确认清理候选'}
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      </div>
    )
  }

  if (run.run.status !== 'running' && run.run.status !== 'paused') return null
  if (
    stage !== 'awaiting_candidate_review' &&
    stage !== 'awaiting_live_verification' &&
    stage !== 'needs_attention'
  )
    return null

  return (
    <div
      role='region'
      aria-label='蓝绿发布控制'
      className={
        isAttentionState
          ? 'mt-4 rounded-xl border border-amber-200 bg-amber-50/80 p-4 dark:border-amber-900 dark:bg-amber-950/30'
          : 'mt-4 rounded-xl border border-sky-200 bg-sky-50/80 p-4 dark:border-sky-900 dark:bg-sky-950/30'
      }
    >
      <div className='flex items-start gap-3'>
        <div
          className={`flex size-9 shrink-0 items-center justify-center rounded-lg bg-background shadow-sm ${isAttentionState ? 'text-amber-700 dark:text-amber-200' : 'text-sky-700 dark:text-sky-200'}`}
        >
          {isPaused || stage === 'needs_attention' ? (
            <ShieldAlert className='size-5' aria-hidden='true' />
          ) : stage === 'awaiting_live_verification' ? (
            <CircleCheck className='size-5' aria-hidden='true' />
          ) : (
            <Fingerprint className='size-5' aria-hidden='true' />
          )}
        </div>
        <div className='min-w-0 flex-1'>
          <div className='flex flex-wrap items-center gap-2'>
            <h5
              className={`font-semibold ${isAttentionState ? 'text-amber-950 dark:text-amber-100' : 'text-sky-950 dark:text-sky-100'}`}
            >
              {isPaused
                ? '发布推进已暂停'
                : stage === 'needs_attention'
                  ? '需要核对集群状态'
                  : stage === 'awaiting_live_verification'
                    ? '生产流量已切换，等待业务验证'
                    : '候选版本已就绪，等待切流确认'}
            </h5>
            <Badge
              variant='outline'
              className={
                isAttentionState
                  ? 'border-amber-300 text-amber-900 dark:text-amber-100'
                  : 'border-sky-300 text-sky-800 dark:text-sky-200'
              }
            >
              {isPaused
                ? `已暂停 · 版本 ${run.run.version}`
                : needsAttention
                  ? '状态未知 · 资源锁保留'
                  : `运行版本 ${run.run.version}`}
            </Badge>
          </div>
          <p
            className={`mt-1 text-sm leading-6 ${isAttentionState ? 'text-amber-950/80 dark:text-amber-100/80' : 'text-sky-900/80 dark:text-sky-100/80'}`}
          >
            {isPaused
              ? stage === 'awaiting_live_verification'
                ? '暂停只停止平台后续推进；当前生产 Service 仍由候选版本承接流量。若要停止候选流量，请选择“恢复稳定版本”。'
                : '暂停只停止平台后续推进；生产 Service 继续指向稳定版本。恢复后仍需操作员确认，不会自动切流。'
              : stage === 'needs_attention'
                ? '服务端重读候选 Deployment、EndpointSlice 和生产 Service selector；确认实际入口后再选择继续验证或恢复。'
                : stage === 'awaiting_live_verification'
                  ? '请验证真实业务请求。平台会在完成确认时再次检查候选健康，并把活动应用绑定更新到新 Deployment。'
                  : '候选 Deployment 与私有预览 Service 已通过就绪检查。生产入口仍保持稳定版本，确认后才会切换。'}
          </p>
          {!canOperate ? (
            <p className='mt-3 text-xs text-muted-foreground'>
              需要操作员权限才能推进发布。
            </p>
          ) : (
            <div className='mt-4 flex flex-col gap-2 sm:flex-row sm:flex-wrap'>
              {isPaused && (
                <Button
                  className='min-h-11'
                  disabled={controls.isPending}
                  onClick={() => controls.mutate({ action: 'continue' })}
                >
                  <Play className='size-4' aria-hidden='true' />
                  继续推进
                </Button>
              )}
              {!isPaused && stage !== 'needs_attention' && (
                <Button
                  className='min-h-11'
                  variant='outline'
                  disabled={controls.isPending}
                  onClick={() => controls.mutate({ action: 'pause' })}
                >
                  <Pause className='size-4' aria-hidden='true' />
                  暂停推进
                </Button>
              )}
              {stage !== 'needs_attention' && (
                <Button
                  className='min-h-11 text-destructive hover:text-destructive'
                  variant='ghost'
                  disabled={controls.isPending}
                  onClick={(event) =>
                    openConfirmation('cancel', event.currentTarget)
                  }
                >
                  取消发布
                </Button>
              )}
              {!isPaused && stage === 'awaiting_candidate_review' && (
                <Button
                  className='min-h-11'
                  disabled={controls.isPending}
                  onClick={(event) =>
                    openConfirmation('confirm_cutover', event.currentTarget)
                  }
                >
                  切换生产流量
                </Button>
              )}
              {!isPaused && stage === 'awaiting_live_verification' && (
                <Button
                  className='min-h-11'
                  disabled={controls.isPending}
                  onClick={(event) =>
                    openConfirmation(
                      'confirm_live_verification',
                      event.currentTarget
                    )
                  }
                >
                  <CircleCheck className='size-4' aria-hidden='true' />
                  业务验证通过，完成发布
                </Button>
              )}
              {stage === 'needs_attention' && (
                <Button
                  className='min-h-11'
                  variant='outline'
                  disabled={controls.isPending}
                  onClick={() => controls.mutate({ action: 'reconcile' })}
                >
                  <RefreshCw
                    className={`size-4 ${controls.isPending ? 'motion-safe:animate-spin' : ''}`}
                    aria-hidden='true'
                  />
                  核对并继续候选准备
                </Button>
              )}
              <Button
                className='min-h-11 bg-white text-amber-950 hover:bg-amber-100 hover:text-amber-950 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-100 dark:hover:bg-amber-900 dark:hover:text-amber-100'
                variant='outline'
                disabled={controls.isPending}
                onClick={(event) =>
                  openConfirmation('restore_stable', event.currentTarget)
                }
              >
                <RotateCcw className='size-4' aria-hidden='true' />
                恢复稳定版本
              </Button>
            </div>
          )}
          {controls.isPending && (
            <p
              role='status'
              className={`mt-2 text-xs ${isAttentionState ? 'text-amber-900 dark:text-amber-100' : 'text-sky-900 dark:text-sky-100'}`}
            >
              正在核对集群并记录阶段…
            </p>
          )}
        </div>
      </div>
      <Dialog
        open={confirming != null}
        onOpenChange={(open) => !open && closeConfirmation()}
      >
        <DialogContent
          className='flex w-[calc(100%-2rem)] flex-col gap-3 overflow-hidden sm:max-w-lg sm:gap-4'
          style={{
            maxHeight: `${Math.max(0, viewportHeight - 32)}px`,
            top: compactViewport ? '1rem' : '50%',
            translate: compactViewport ? '-50% 0' : '-50% -50%',
          }}
        >
          <DialogHeader className='shrink-0 pe-6 text-start'>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription className='leading-6'>
              {description}
            </DialogDescription>
          </DialogHeader>
          <div className='min-h-0 flex-1 space-y-3 overflow-y-auto overscroll-contain'>
            {requiresRecoveryCompatibilityAcknowledgement && (
              <div className='rounded-lg bg-muted/50 px-3 py-2 text-sm'>
                <div className='text-xs text-muted-foreground'>
                  生产流量入口
                </div>
                <div className='mt-1 font-semibold'>
                  {run.plan.snapshot.target.environment_name} ·{' '}
                  {run.plan.snapshot.target.service_name}
                </div>
              </div>
            )}
            {requiresRecoveryCompatibilityAcknowledgement && (
              <label className='flex cursor-pointer items-start gap-3 rounded-lg border border-amber-300 bg-amber-50/70 p-3 text-sm text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'>
                <input
                  type='checkbox'
                  className='mt-0.5 size-4 shrink-0 accent-amber-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-amber-700'
                  checked={recoveryCompatibilityAcknowledged}
                  disabled={controls.isPending}
                  onChange={(event) =>
                    setRecoveryCompatibilityAcknowledged(event.target.checked)
                  }
                  aria-label='确认稳定版本兼容当前数据库与外部配置'
                />
                <span>
                  <span className='block font-medium'>
                    我已确认稳定版本兼容当前数据库与配置
                  </span>
                  <span className='mt-1 block text-xs leading-5 opacity-85'>
                    平台只切回 Service 入口，不回退数据库迁移、ConfigMap/Secret
                    或 Helm/Git 配置；无法确认兼容时请向前修复。
                  </span>
                </span>
              </label>
            )}
            {requiresRecoveryCompatibilityAcknowledgement ? (
              <div className='space-y-3 rounded-lg bg-muted/50 p-3 text-sm'>
                <div>
                  <div className='text-xs text-muted-foreground'>
                    当前候选镜像
                  </div>
                  <div className='mt-1 font-mono break-all'>
                    {run.plan.snapshot.artifact.image}
                  </div>
                </div>
                <div className='border-t border-border/70 pt-3'>
                  <div className='text-xs text-muted-foreground'>
                    稳定镜像 digest
                  </div>
                  <div className='mt-1 font-mono break-all'>
                    {run.plan.snapshot.target.stable_image_digest ||
                      '未固定，需人工核对稳定 Deployment'}
                  </div>
                </div>
                <div className='border-t border-border/70 pt-3'>
                  <div className='text-xs text-muted-foreground'>
                    稳定配置来源
                  </div>
                  {run.plan.snapshot.target.stable_configuration_source ? (
                    <div className='mt-1 space-y-1'>
                      <div className='break-all'>
                        {
                          run.plan.snapshot.target.stable_configuration_source
                            .repository_url
                        }
                        {' / '}
                        {
                          run.plan.snapshot.target.stable_configuration_source
                            .path
                        }
                      </div>
                      <div className='font-mono break-all'>
                        {
                          run.plan.snapshot.target.stable_configuration_source
                            .revision
                        }
                      </div>
                      <p className='text-xs leading-5 text-muted-foreground'>
                        这是历史来源记录，不代表当前集群配置已回退。
                      </p>
                    </div>
                  ) : (
                    <div className='mt-1'>来源未知，需人工核对当前配置。</div>
                  )}
                </div>
              </div>
            ) : (
              <div className='space-y-3 rounded-lg bg-muted/50 p-3 text-sm'>
                <div>
                  <div className='text-xs text-muted-foreground'>
                    生产流量入口
                  </div>
                  <div className='mt-1 font-semibold'>
                    {run.plan.snapshot.target.environment_name} ·{' '}
                    {run.plan.snapshot.target.service_name}
                  </div>
                </div>
                <div className='border-t border-border/70 pt-3'>
                  <div className='text-xs text-muted-foreground'>候选镜像</div>
                  <div className='mt-1 font-mono break-all'>
                    {run.plan.snapshot.artifact.image}
                  </div>
                </div>
              </div>
            )}
          </div>
          <DialogFooter
            className={`shrink-0 ${compactViewport ? 'flex-row justify-end gap-2' : 'flex-col sm:flex-row'}`}
          >
            <Button
              variant='outline'
              className={`min-h-11 ${compactViewport ? 'flex-1' : ''}`}
              disabled={controls.isPending}
              onClick={closeConfirmation}
            >
              {confirming === 'cancel' ? '暂不取消' : '继续检查'}
            </Button>
            <Button
              className={`min-h-11 ${compactViewport ? 'flex-1' : ''}`}
              variant={
                confirming === 'restore_stable' || confirming === 'cancel'
                  ? 'destructive'
                  : 'default'
              }
              disabled={
                controls.isPending ||
                confirming == null ||
                (requiresRecoveryCompatibilityAcknowledgement &&
                  !recoveryCompatibilityAcknowledged)
              }
              onClick={() =>
                confirming &&
                controls.mutate({
                  action: confirming,
                  recoveryCompatibilityAcknowledged,
                })
              }
            >
              {controls.isPending ? '正在执行…' : actionLabel}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

export function PlanPreview({
  plan,
  allowApproval = true,
}: {
  plan: DeliveryPlan
  allowApproval?: boolean
}) {
  const role = useAuthStore((state) => state.auth.user?.role)
  const queryClient = useQueryClient()
  const [manualReviewAcknowledged, setManualReviewAcknowledged] =
    useState(false)
  useEffect(() => {
    setManualReviewAcknowledged(false)
  }, [plan.id, plan.snapshot_hash])
  const copyConfigurationRevision = async (revision: string) => {
    try {
      await navigator.clipboard.writeText(revision)
      toast.success('已复制配置 Git commit')
    } catch {
      toast.error('无法访问剪贴板，请手动复制完整 commit')
    }
  }
  const manualReviewChecks = (plan.snapshot.checks ?? []).filter(
    (check) =>
      check.status === 'review' ||
      (check.status === 'unknown' &&
        [
          'configuration-source',
          'stable-configuration-source',
          'deployment-pipeline-scope',
        ].includes(check.key))
  )
  const target = plan.snapshot.target
  const canInspectDeploymentPipeline = role === 'operator' || role === 'admin'
  const pipelineIdentity = target.deployment_pipeline
    ? `${plan.id}/${target.deployment_pipeline.connection_id}/${target.deployment_pipeline.pipeline_id}`
    : ''
  const pipelineDefinitionCheck = useMutation({
    mutationFn: (request: {
      identity: string
      connection_id: number
      pipeline_id: string
    }) =>
      platformApi.inspectDeploymentPipelineScope({
        connection_id: request.connection_id,
        pipeline_id: request.pipeline_id,
      }),
    onError: () => toast.error('无法读取云效当前流水线定义，请稍后重试'),
  })
  const checkedPipelineDefinition =
    pipelineDefinitionCheck.isSuccess &&
    pipelineDefinitionCheck.variables?.identity === pipelineIdentity
      ? pipelineDefinitionCheck.data?.data
      : undefined
  const currentPipelineScopeReport = checkedPipelineDefinition
  const currentPipelineScopeHash = currentPipelineScopeReport?.definition_hash
  const currentPipelineScopeReadable =
    currentPipelineScopeReport?.analysis_status === 'manual_review_required' &&
    Boolean(currentPipelineScopeHash)
  const currentPipelineScopeMatchesReview =
    currentPipelineScopeReadable &&
    Boolean(plan.snapshot.target.deployment_pipeline_scope_review) &&
    currentPipelineScopeHash ===
      plan.snapshot.target.deployment_pipeline_scope_review?.definition_hash
  const approvals = useQuery({
    queryKey: ['promotion-requests'],
    queryFn: () => platformApi.promotionRequests(),
    enabled: plan.strategy !== 'gateway_canary' || plan.blockers.length === 0,
    refetchInterval: 5000,
  })
  const approval = [...(approvals.data?.data ?? [])]
    .filter((request) => request.delivery_plan_id === plan.id)
    .sort((left, right) => right.id - left.id)[0]
  const deployLogID = approval?.deploy_log_id
  const execution = useQuery({
    queryKey: ['delivery-plan-execution', deployLogID],
    queryFn: () => platformApi.deployLog(deployLogID!),
    enabled: deployLogID != null,
    refetchInterval: (query) =>
      deployLogRunning(query.state.data?.data.status) ? 2000 : false,
    retry: false,
  })
  const actionJournal = useQuery({
    queryKey: ['delivery-plan-actions', deployLogID],
    queryFn: () => platformApi.deployActions(deployLogID!),
    enabled: deployLogID != null,
    refetchInterval: () =>
      deployLogRunning(execution.data?.data.status) ? 2000 : false,
    retry: false,
  })
  const deliveryRun = useQuery({
    queryKey: ['delivery-run-by-log', deployLogID],
    queryFn: () => platformApi.deliveryRunByDeployLog(deployLogID!),
    enabled: deployLogID != null,
    refetchInterval: () =>
      deployLogRunning(execution.data?.data.status) ? 2000 : false,
    retry: false,
  })
  const deployLog = execution.data?.data
  const reconcile = useMutation({
    mutationFn: () => platformApi.reconcileDeployLog(deployLogID!),
    onSuccess: (result) => {
      toast.success(result.data.message)
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-execution', deployLogID],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-actions', deployLogID],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-run-by-log', deployLogID],
      })
    },
    onError: (error) => {
      const detail = (error as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      toast.error(detail || '核对未继续，请查看集群中的实际镜像')
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-execution', deployLogID],
      })
      void queryClient.invalidateQueries({
        queryKey: ['delivery-plan-actions', deployLogID],
      })
    },
  })
  const requestApproval = useMutation({
    mutationFn: () =>
      platformApi.requestDeliveryPlanApproval(
        plan.id,
        manualReviewChecks.length > 0 ? manualReviewAcknowledged : false
      ),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['promotion-requests'] })
      void queryClient.invalidateQueries({ queryKey: ['releases'] })
    },
  })
  const artifact = plan.snapshot.artifact
  const stableDigest = /^sha256:[a-f0-9]{64}$/.test(
    target.stable_image_digest ?? ''
  )
    ? target.stable_image_digest!
    : ''
  const stableImageRepository = repositoryOf(target.current_image)
  const stableImageFull =
    stableDigest && stableImageRepository
      ? `${stableImageRepository}@${stableDigest}`
      : ''
  const configurationSource = artifact.configuration_source
  const configurationReferences = target.configuration_references
  const hasConfigurationReferences = (configurationReferences?.length ?? 0) > 0
  const legacyConfigurationReferenceSnapshot = plan.snapshot.schema_version < 7
  const configurationReferenceSnapshotMissing =
    legacyConfigurationReferenceSnapshot ||
    !Array.isArray(configurationReferences) ||
    !target.service_account_name?.trim()
  const approvalState = approval?.status
  const deliveryRunStatus = deliveryRun.data?.data?.run.status
  const stageApprovalLabel =
    approvalState === 'approved'
      ? '审批已通过'
      : approvalState === 'pending'
        ? '等待独立审批'
        : approvalState
          ? '审批已关闭'
          : plan.strategy === 'rolling' ||
              (plan.strategy === 'blue_green' && plan.blockers.length === 0)
            ? '待提交审批'
            : plan.blockers.length > 0
              ? '预检未通过'
              : '策略执行器未开放'
  const stages = [
    { label: '制品已固定', done: true },
    { label: '目标已快照', done: true },
    { label: stageApprovalLabel, done: approvalState === 'approved' },
    {
      label:
        deliveryRunStatus === 'paused'
          ? '发布已暂停'
          : deliveryRunStatus === 'cancelled'
            ? '发布已取消'
            : deployLog
              ? deployLog.status === 'success'
                ? '部署成功'
                : deployLog.status === 'failed'
                  ? '部署失败'
                  : deployLog.status === 'interrupted'
                    ? '需要核对'
                    : deployLog.status === 'pending'
                      ? '等待执行'
                      : '部署进行中'
              : deployLogID
                ? '读取执行状态'
                : '部署执行',
      done: deliveryRunStatus === 'success' || deployLog?.status === 'success',
    },
  ]
  const hasActiveApproval =
    approvalState === 'pending' || approvalState === 'approved'
  const canRequestApproval =
    allowApproval &&
    (plan.strategy === 'rolling' || plan.strategy === 'blue_green') &&
    plan.blockers.length === 0 &&
    (manualReviewChecks.length === 0 || manualReviewAcknowledged) &&
    !hasActiveApproval &&
    !requestApproval.isSuccess &&
    (role === 'operator' || role === 'admin')
  const requestError = (
    requestApproval.error as {
      response?: { data?: { detail?: string } }
    } | null
  )?.response?.data?.detail

  return (
    <section aria-label='发布计划快照' className='space-y-6'>
      <div className='flex flex-col gap-4 rounded-xl border border-sky-200 bg-sky-50 p-4 text-sky-950 sm:flex-row sm:items-center sm:justify-between dark:border-sky-900 dark:bg-sky-950/40 dark:text-sky-100'>
        <div className='flex gap-3'>
          <div className='flex size-10 shrink-0 items-center justify-center rounded-lg bg-white text-sky-700 shadow-sm dark:bg-sky-900 dark:text-sky-200'>
            <ClipboardCheck className='size-5' aria-hidden='true' />
          </div>
          <div>
            <div className='flex flex-wrap items-center gap-2 font-semibold'>
              计划 #{plan.id} · 只读预览
              <Badge
                variant='outline'
                className='border-sky-300 text-sky-800 dark:text-sky-200'
              >
                不会变更集群
              </Badge>
            </div>
            <p className='mt-1 text-sm text-sky-800 dark:text-sky-200'>
              {plan.strategy === 'rolling'
                ? '快照已固定；批准后由现有滚动发布任务更新 Deployment。'
                : plan.strategy === 'blue_green'
                  ? '候选通过预检后独立审批；候选就绪、切流与业务验证都由平台记录，并支持恢复稳定版本。'
                  : '快照已固定；此策略的发布执行器尚未交付。'}
            </p>
          </div>
        </div>
        <div className='shrink-0 text-left sm:text-right'>
          <div className='text-xs text-sky-800 dark:text-sky-200'>策略</div>
          <div className='font-semibold'>{strategyLabels[plan.strategy]}</div>
          <div className='mt-1 text-xs text-muted-foreground'>
            {plan.snapshot.policy?.version
              ? `策略版本 v${plan.snapshot.policy.version}`
              : '兼容旧绑定策略'}
          </div>
        </div>
      </div>

      <div className='grid gap-3 sm:grid-cols-2'>
        <div className='rounded-xl border bg-card p-4'>
          <div className='mb-3 flex items-center gap-2 text-xs font-semibold tracking-wide text-muted-foreground uppercase'>
            <GitBranch className='size-4' aria-hidden='true' />
            候选制品
          </div>
          <div className='font-mono text-sm font-semibold break-all'>
            {artifact.image}
          </div>
          <div className='mt-2 text-xs break-all text-muted-foreground'>
            Digest：{artifact.digest || '未提供'}
          </div>
          <div className='mt-3 flex flex-wrap gap-2 text-xs text-muted-foreground'>
            <span>版本 {artifact.version}</span>
            {artifact.build_number && (
              <span>构建 #{artifact.build_number}</span>
            )}
            {artifact.commit_id && (
              <span>Commit {artifact.commit_id.slice(0, 8)}</span>
            )}
          </div>
          <dl className='mt-3 grid gap-x-4 gap-y-2 border-t pt-3 text-xs sm:grid-cols-2'>
            {artifact.source && (
              <div className='min-w-0'>
                <dt className='text-muted-foreground'>构建来源</dt>
                <dd className='mt-0.5 break-all'>{artifact.source}</dd>
              </div>
            )}
            {artifact.source_kind && (
              <div className='min-w-0'>
                <dt className='text-muted-foreground'>代码来源类型</dt>
                <dd className='mt-0.5'>
                  {artifact.source_kind === 'repository'
                    ? '代码仓库'
                    : artifact.source_kind === 'third_party'
                      ? '第三方镜像'
                      : artifact.source_kind === 'unknown'
                        ? '待登记'
                        : artifact.source_kind}
                </dd>
              </div>
            )}
            {artifact.source_repository && (
              <div className='min-w-0 sm:col-span-2'>
                <dt className='text-muted-foreground'>代码仓库</dt>
                <dd className='mt-0.5 font-mono break-all'>
                  {artifact.source_repository}
                </dd>
              </div>
            )}
            {artifact.source_path && (
              <div className='min-w-0'>
                <dt className='text-muted-foreground'>仓库内目录</dt>
                <dd className='mt-0.5 font-mono break-all'>
                  {artifact.source_path}
                </dd>
              </div>
            )}
            {artifact.image_repository && (
              <div className='min-w-0'>
                <dt className='text-muted-foreground'>镜像仓库</dt>
                <dd className='mt-0.5 font-mono break-all'>
                  {artifact.image_repository}
                </dd>
              </div>
            )}
            {(artifact.pipeline_name || artifact.pipeline_id) && (
              <div className='min-w-0'>
                <dt className='text-muted-foreground'>CI 流水线</dt>
                <dd className='mt-0.5 break-all'>
                  {artifact.pipeline_name || '未命名'}
                  {artifact.pipeline_id && ` · ${artifact.pipeline_id}`}
                </dd>
              </div>
            )}
            {artifact.branch && (
              <div className='min-w-0'>
                <dt className='text-muted-foreground'>分支</dt>
                <dd className='mt-0.5 font-mono break-all'>
                  {artifact.branch}
                </dd>
              </div>
            )}
            {artifact.digest_source && (
              <div className='min-w-0'>
                <dt className='text-muted-foreground'>摘要来源</dt>
                <dd className='mt-0.5'>
                  {artifact.digest_source === 'ci'
                    ? 'CI 上报'
                    : artifact.digest_source === 'registry'
                      ? 'Registry 解析'
                      : artifact.digest_source}
                </dd>
              </div>
            )}
          </dl>
          <div className='mt-3 rounded-lg border bg-muted/30 p-3 text-xs'>
            <div className='font-medium'>配置仓库 revision</div>
            {configurationSource ? (
              <>
                <div className='mt-1 font-mono break-all'>
                  {configurationSource.repository_url} /{' '}
                  {configurationSource.path}
                </div>
                <div className='mt-1 flex items-start gap-2'>
                  <div className='min-w-0 flex-1 font-mono break-all'>
                    {configurationSource.revision}
                  </div>
                  <Button
                    type='button'
                    variant='outline'
                    size='icon'
                    className='size-11 shrink-0'
                    aria-label='复制配置 Git commit'
                    title='复制配置 Git commit'
                    onClick={() =>
                      void copyConfigurationRevision(
                        configurationSource.revision
                      )
                    }
                  >
                    <Copy className='size-4' aria-hidden='true' />
                  </Button>
                </div>
                <p className='mt-1 leading-5 text-muted-foreground'>
                  Runner 上报并固定到此计划；平台未验证 checkout 或 Helm
                  渲染，也不能据此恢复配置。
                </p>
              </>
            ) : (
              <>
                {artifact.deployment_config_repository && (
                  <div className='mt-1 font-mono break-all'>
                    {artifact.deployment_config_repository} /{' '}
                    {artifact.deployment_config_path || '.'}
                  </div>
                )}
                <p className='mt-1 leading-5 text-muted-foreground'>
                  {artifact.deployment_config_repository
                    ? '组件已登记配置仓库，但本次 Runner 未上报 commit。'
                    : '组件未固定配置仓库，Runner 也未上报配置 revision。'}{' '}
                  ConfigMap/Secret 版本只用于发现漂移，不是可恢复的配置副本。
                </p>
              </>
            )}
          </div>
        </div>

        <div className='rounded-xl border bg-card p-4'>
          <div className='mb-3 flex items-center gap-2 text-xs font-semibold tracking-wide text-muted-foreground uppercase'>
            <CircleDot className='size-4' aria-hidden='true' />
            当前目标
          </div>
          <div className='font-semibold'>
            {target.application_name} / {target.component_name}
          </div>
          <div className='mt-1 text-sm text-muted-foreground'>
            {target.environment_name} · {target.cluster_name}
          </div>
          <div className='mt-3 rounded-lg bg-muted/60 px-3 py-2 text-xs'>
            <span className='text-muted-foreground'>当前运行 </span>
            <span className='font-mono break-all'>{target.current_image}</span>
          </div>
          <div
            role='group'
            aria-label='稳定版本配置恢复基线'
            className='mt-3 rounded-lg border border-sky-200/80 bg-sky-50/60 p-3 text-xs dark:border-sky-900/80 dark:bg-sky-950/20'
          >
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <div className='font-medium'>稳定版本配置恢复基线</div>
              <Badge variant='outline' className='min-h-6 bg-background/70'>
                {target.stable_configuration_source ? '来源已固定' : '来源未知'}
              </Badge>
            </div>
            {target.stable_configuration_source ? (
              <>
                <div className='mt-2 font-mono leading-5 break-all'>
                  {target.stable_configuration_source.repository_url} /{' '}
                  {target.stable_configuration_source.path}
                </div>
                <div className='mt-1 flex items-start gap-2'>
                  <div className='min-w-0 flex-1 font-mono leading-5 break-all'>
                    {target.stable_configuration_source.revision}
                  </div>
                  <Button
                    type='button'
                    variant='outline'
                    size='icon'
                    className='size-11 shrink-0'
                    aria-label='复制稳定版本配置 Git commit'
                    title='复制稳定版本配置 Git commit'
                    onClick={() =>
                      void copyConfigurationRevision(
                        target.stable_configuration_source!.revision
                      )
                    }
                  >
                    <Copy className='size-4' aria-hidden='true' />
                  </Button>
                </div>
                <p className='mt-1 leading-5 text-muted-foreground'>
                  关联最近成功部署 #{target.stable_release_id ?? '未知'}；Runner
                  记录的是来源声明，尚不能证明该 commit 已渲染并应用。
                </p>
              </>
            ) : (
              <p className='mt-2 leading-5 text-muted-foreground'>
                {target.stable_release_id
                  ? `最近成功部署 #${target.stable_release_id} 没有可验证的配置 commit。`
                  : '当前 Deployment 无法与最近成功制品部署精确关联。'}{' '}
                恢复流程不会复用候选版本的 commit。
              </p>
            )}
            <div className='mt-2 border-t border-sky-200/70 pt-2 leading-5 text-muted-foreground dark:border-sky-900/70'>
              配置流水线：
              {target.deployment_pipeline
                ? `连接 #${target.deployment_pipeline.connection_id} · Pipeline ${target.deployment_pipeline.pipeline_id}`
                : '未配置'}
              {' · '}计划创建时固定此绑定
            </div>
            {target.deployment_pipeline && (
              <div className='mt-2 border-t border-sky-200/70 pt-2 leading-5 dark:border-sky-900/70'>
                <div className='font-medium'>写入范围人工核对</div>
                {target.deployment_pipeline_scope_review ? (
                  <>
                    <div className='mt-1'>
                      {target.deployment_pipeline_scope_review.write_scope ===
                      'configuration_only'
                        ? '记录为仅配置写入'
                        : target.deployment_pipeline_scope_review
                              .write_scope === 'workload_or_image'
                          ? '记录可能写入 Deployment 或镜像'
                          : '记录为写入范围未知'}
                      {' · '}核对 #{target.deployment_pipeline_scope_review.id}
                    </div>
                    <div className='mt-1 font-mono break-all text-muted-foreground'>
                      定义 SHA-256：
                      {target.deployment_pipeline_scope_review.definition_hash}
                    </div>
                  </>
                ) : (
                  <div className='mt-1 text-muted-foreground'>
                    没有与当前流水线匹配的人工核对记录。
                  </div>
                )}
                <p className='mt-1 text-muted-foreground'>
                  历史结论不证明云效当前定义或实际执行权限；平台不会自动触发配置恢复。
                </p>
                {canInspectDeploymentPipeline && (
                  <div className='mt-3 border-t border-sky-200/70 pt-3 dark:border-sky-900/70'>
                    <Button
                      type='button'
                      variant='outline'
                      className='min-h-11'
                      disabled={pipelineDefinitionCheck.isPending}
                      onClick={() =>
                        target.deployment_pipeline &&
                        pipelineDefinitionCheck.mutate({
                          identity: pipelineIdentity,
                          connection_id:
                            target.deployment_pipeline.connection_id,
                          pipeline_id: target.deployment_pipeline.pipeline_id,
                        })
                      }
                    >
                      {pipelineDefinitionCheck.isPending
                        ? '读取云效定义中…'
                        : '核对当前云效定义'}
                    </Button>
                    {checkedPipelineDefinition && (
                      <div
                        role='status'
                        className='mt-2 rounded-md bg-background/70 px-3 py-2 text-xs leading-5'
                      >
                        {currentPipelineScopeReadable ? (
                          currentPipelineScopeMatchesReview ? (
                            <>
                              <div className='font-medium text-emerald-800 dark:text-emerald-200'>
                                当前定义指纹与人工核对记录一致
                              </div>
                              <p className='mt-1 text-muted-foreground'>
                                这只确认 SHA-256
                                相同；不证明引用脚本、实际权限或写入范围，也不会开放流水线触发。
                              </p>
                            </>
                          ) : target.deployment_pipeline_scope_review ? (
                            <>
                              <div className='font-medium text-amber-900 dark:text-amber-100'>
                                当前定义与人工核对记录不一致
                              </div>
                              <p className='mt-1 text-muted-foreground'>
                                请回到环境绑定重新分析并核对当前流水线，再生成新计划。
                              </p>
                              <Link
                                to='/environments'
                                search={{ app_id: target.app_id }}
                                className='mt-1 inline-flex min-h-11 items-center font-medium text-primary underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2'
                              >
                                去环境绑定重新核对
                              </Link>
                            </>
                          ) : (
                            <div className='text-amber-900 dark:text-amber-100'>
                              <div>
                                当前定义已读取，但没有与此流水线匹配的人工核对记录。
                              </div>
                              <Link
                                to='/environments'
                                search={{ app_id: target.app_id }}
                                className='mt-1 inline-flex min-h-11 items-center font-medium text-primary underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2'
                              >
                                去环境绑定完成核对
                              </Link>
                            </div>
                          )
                        ) : (
                          <div className='text-amber-900 dark:text-amber-100'>
                            <div>
                              当前定义不可完整核对；不能确认原人工记录仍对应现有流水线。
                            </div>
                            <Link
                              to='/environments'
                              search={{ app_id: target.app_id }}
                              className='mt-1 inline-flex min-h-11 items-center font-medium text-primary underline-offset-4 hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2'
                            >
                              去环境绑定重新分析
                            </Link>
                          </div>
                        )}
                        {currentPipelineScopeHash && (
                          <div className='mt-2 border-t pt-2 font-mono break-all text-muted-foreground'>
                            当前 SHA-256：
                            {currentPipelineScopeHash}
                          </div>
                        )}
                      </div>
                    )}
                  </div>
                )}
                {pipelineDefinitionCheck.isError &&
                  pipelineDefinitionCheck.variables?.identity ===
                    pipelineIdentity && (
                    <p role='alert' className='mt-2 text-xs text-destructive'>
                      无法读取云效当前定义；原人工核对状态仍需再次确认。
                    </p>
                  )}
              </div>
            )}
            {target.deployment_pipeline && (
              <details className='group mt-2 rounded-lg border border-sky-200/70 bg-background/70 dark:border-sky-900/70'>
                <summary className='flex min-h-11 cursor-pointer list-none items-center justify-between gap-3 px-3 py-2.5 text-xs leading-5 font-medium marker:hidden focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:outline-none [&::-webkit-details-marker]:hidden'>
                  <span>查看配置恢复的固定输入</span>
                  <ChevronDown
                    aria-hidden='true'
                    className='size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180'
                  />
                </summary>
                <div className='border-t border-sky-200/70 px-3 py-3 dark:border-sky-900/70'>
                  <p className='mb-3 text-xs leading-5 text-muted-foreground'>
                    这是计划中的恢复输入映射预览。平台当前不会触发该流水线；流水线是否只写入此目标仍未核实。
                  </p>
                  <dl className='grid min-w-0 gap-x-4 gap-y-3 sm:grid-cols-2'>
                    <SnapshotValue
                      label='DEPLOY_CONFIG_REVISION · 稳定配置 commit'
                      value={target.stable_configuration_source?.revision ?? ''}
                    />
                    <SnapshotValue
                      label='IMAGE_FULL · 稳定镜像仓库@digest'
                      value={stableImageFull}
                    />
                    <SnapshotValue
                      label='DEPLOY_ENVIRONMENT_ID · 逻辑环境'
                      value={`${target.environment_id} · ${target.environment_name}`}
                    />
                    <SnapshotValue
                      label='DEPLOY_APPLICATION_ID · 业务应用'
                      value={`${target.application_id} · ${target.application_name}`}
                    />
                    <SnapshotValue
                      label='DEPLOY_NAMESPACE · 命名空间'
                      value={target.namespace}
                    />
                    <SnapshotValue
                      label='DEPLOYMENT_NAME · Deployment'
                      value={target.deployment}
                    />
                    <SnapshotValue
                      label='DEPLOY_CONTAINER_NAME · 容器'
                      value={target.container}
                    />
                    <SnapshotValue
                      label='DEPLOY_PLATFORM_REQUEST_ID · 请求幂等标识'
                      value='触发时生成并持久化'
                    />
                  </dl>
                </div>
              </details>
            )}
          </div>
          <div className='mt-2 text-xs text-muted-foreground'>
            组件绑定已核对：{target.test_environment_name} #
            {target.test_binding_id}
            <ArrowRight className='mx-1 inline size-3' aria-hidden='true' />
            {target.environment_name} #{target.binding_id}
          </div>
          <div className='mt-2 flex items-center gap-2 text-xs text-muted-foreground'>
            <span className='truncate'>
              {target.namespace}/{target.deployment}
            </span>
            <ArrowRight className='size-3 shrink-0' aria-hidden='true' />
            <span className='truncate'>容器 {target.container}</span>
          </div>
          {configurationReferenceSnapshotMissing ? (
            <div className='mt-3 rounded-lg border border-amber-200 bg-amber-50/70 p-3 dark:border-amber-900 dark:bg-amber-950/25'>
              <div className='flex items-center gap-2 text-sm font-medium text-amber-950 dark:text-amber-100'>
                <CircleAlert className='size-4 shrink-0' aria-hidden='true' />
                {legacyConfigurationReferenceSnapshot
                  ? '配置引用未固定'
                  : '配置引用无法确认'}
              </div>
              <p className='mt-1 text-xs leading-5 text-amber-900 dark:text-amber-100'>
                {legacyConfigurationReferenceSnapshot
                  ? '这是旧版本计划，未记录 Pod 模板引用的 ConfigMap/Secret。请重新生成计划以核对配置来源。'
                  : '当前计划无法完整读取 Pod 模板或其 ServiceAccount 的镜像拉取引用，不能视为没有配置引用。请检查目标集群读取权限后重新生成计划。'}
              </p>
            </div>
          ) : (
            <div
              className={`mt-3 rounded-lg border p-3 ${
                hasConfigurationReferences
                  ? 'border-amber-200 bg-amber-50/70 dark:border-amber-900 dark:bg-amber-950/25'
                  : 'bg-muted/30'
              }`}
            >
              <div
                className={`flex items-center gap-2 text-sm font-medium ${
                  hasConfigurationReferences
                    ? 'text-amber-950 dark:text-amber-100'
                    : 'text-foreground'
                }`}
              >
                {hasConfigurationReferences ? (
                  <CircleAlert className='size-4 shrink-0' aria-hidden='true' />
                ) : (
                  <CircleCheck
                    className='size-4 shrink-0 text-emerald-700 dark:text-emerald-300'
                    aria-hidden='true'
                  />
                )}
                {hasConfigurationReferences ? '外部配置引用' : '配置引用检查'}
              </div>
              {hasConfigurationReferences ? (
                <>
                  <p className='mt-1 text-xs leading-5 text-amber-900 dark:text-amber-100'>
                    快照固定资源名称，以及可读取时的对象
                    UID/resourceVersion，用于发现对象替换或变更；不会保存配置值，也不能恢复历史配置。请确认配置使用版本化引用或已有配置发布流程。
                  </p>
                  {target.service_account_pull_secrets_included && (
                    <p className='mt-1 text-xs leading-5 text-amber-900 dark:text-amber-100'>
                      已包含 ServiceAccount{' '}
                      <span className='font-mono'>
                        {target.service_account_name || 'default'}
                      </span>{' '}
                      继承的镜像拉取 Secret。
                    </p>
                  )}
                  <ul className='mt-2 flex flex-wrap gap-2'>
                    {configurationReferences.map((reference) => (
                      <li
                        key={`${reference.kind}/${reference.name}`}
                        className='max-w-full rounded-md border border-amber-200 bg-background/80 px-2 py-1 text-xs text-foreground dark:border-amber-900'
                      >
                        <span className='text-muted-foreground'>
                          {reference.kind}
                        </span>{' '}
                        <span className='font-mono break-all'>
                          {reference.name}
                        </span>
                        {reference.uid && reference.resource_version ? (
                          <span className='ml-2 text-muted-foreground'>
                            rv {reference.resource_version}
                          </span>
                        ) : (
                          <span className='ml-2 text-amber-700 dark:text-amber-200'>
                            版本未知
                          </span>
                        )}
                      </li>
                    ))}
                  </ul>
                </>
              ) : (
                <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                  未发现 Pod 模板或 ServiceAccount 的 ConfigMap/Secret
                  引用；其他外部配置来源不在此检查范围。
                </p>
              )}
            </div>
          )}
          {plan.strategy === 'blue_green' && (
            <div className='mt-3 rounded-lg border bg-muted/30 px-3 py-2.5'>
              <div className='text-xs font-medium'>蓝绿切换入口</div>
              <div className='mt-1 text-sm font-semibold'>
                {target.service_name || '尚未绑定 Service'}
              </div>
              <div className='mt-1 text-xs leading-5 text-muted-foreground'>
                selector：
                {target.service_selector
                  ? Object.entries(target.service_selector)
                      .map(([key, value]) => `${key}=${value}`)
                      .join(', ')
                  : '未读取'}
              </div>
              {target.service_uid && (
                <div className='mt-1 text-xs text-muted-foreground'>
                  <span>UID </span>
                  <span className='font-mono break-all text-foreground'>
                    {target.service_uid}
                  </span>
                  <span className='px-1' aria-hidden='true'>
                    ·
                  </span>
                  <span>RV </span>
                  <span className='font-mono break-all text-foreground'>
                    {target.service_resource_version || '未知'}
                  </span>
                </div>
              )}
              <div className='mt-2 border-t pt-2 text-xs leading-5 text-muted-foreground'>
                候选保留：{plan.snapshot.candidate_retention_hours ?? '未配置'}{' '}
                小时 · 稳定副本：{target.replicas ?? '未知'}
              </div>
            </div>
          )}
        </div>
      </div>

      {plan.snapshot.evidence_policy && (
        <section
          aria-label='生产验证策略快照'
          className='rounded-xl border border-emerald-200 bg-emerald-50/70 p-4 dark:border-emerald-900 dark:bg-emerald-950/20'
        >
          <div className='flex flex-wrap items-start justify-between gap-3'>
            <div>
              <h3 className='text-sm font-semibold'>生产验证门禁</h3>
              <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                按申请时固定的规则复核证据；后续修改环境策略不会改变本计划。
              </p>
            </div>
            <Badge variant='outline'>
              {plan.snapshot.target.environment_name} · v
              {plan.snapshot.evidence_policy.version}
            </Badge>
          </div>
          <div className='mt-3 grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
            <SnapshotValue
              label='必需证据'
              value={plan.snapshot.evidence_policy.required_verification_kinds
                .map((kind) => evidenceKindLabels[kind])
                .join('、')}
            />
            <SnapshotValue
              label='最长证据年龄'
              value={`${plan.snapshot.evidence_policy.max_evidence_age_minutes} 分钟`}
            />
            <SnapshotValue
              label='可信提交身份'
              value={plan.snapshot.evidence_policy.required_verification_kinds
                .flatMap((kind) =>
                  (
                    plan.snapshot.evidence_policy?.trusted_submitters[kind] ??
                    []
                  ).map(
                    (identity) => `${evidenceKindLabels[kind]}：${identity}`
                  )
                )
                .join('；')}
            />
          </div>
        </section>
      )}

      <ReleaseStrategyJourney strategy={plan.strategy} />

      {plan.strategy === 'blue_green' && (
        <BlueGreenPreflight
          checks={plan.snapshot.checks ?? []}
          stableLabelKey={target.stable_label_key}
          candidateLabelKey={target.candidate_label_key}
          candidateLabelValue={target.candidate_label_value}
          removedLabelKeys={target.candidate_removed_label_keys}
        />
      )}

      <div className='rounded-xl border bg-card p-4'>
        <div className='mb-4 flex items-center justify-between gap-4'>
          <div>
            <h3 className='font-semibold'>发布旅程</h3>
            <p className='mt-1 text-sm text-muted-foreground'>
              每一步都需要服务端记录，不以页面进度代替执行事实。
            </p>
          </div>
          <LockKeyhole
            className='size-5 shrink-0 text-muted-foreground'
            aria-hidden='true'
          />
        </div>
        <ol className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
          {stages.map((stage, index) => (
            <li
              key={stage.label}
              className='flex min-w-0 gap-3 rounded-lg bg-muted/50 p-3'
            >
              <span
                className={
                  stage.done
                    ? 'flex size-7 shrink-0 items-center justify-center rounded-full bg-emerald-100 text-emerald-700 dark:bg-emerald-900 dark:text-emerald-200'
                    : 'flex size-7 shrink-0 items-center justify-center rounded-full bg-background text-muted-foreground ring-1 ring-border'
                }
              >
                {stage.done ? (
                  <Check className='size-4' aria-hidden='true' />
                ) : (
                  <span className='text-xs font-semibold'>{index + 1}</span>
                )}
              </span>
              <div className='min-w-0'>
                <div className='text-sm font-medium'>{stage.label}</div>
                <div className='mt-1 text-xs text-muted-foreground'>
                  {stage.done
                    ? '已完成'
                    : index === 2 && approval?.status === 'pending'
                      ? `申请人 ${approval.requester}`
                      : index === 2 && approval?.status === 'rejected'
                        ? '可重新核对并申请'
                        : index === 3 && deliveryRunStatus === 'paused'
                          ? '当前 Service 流量保持不变；可继续推进或恢复稳定版本'
                          : index === 3 && deliveryRunStatus === 'cancelled'
                            ? '稳定版本已恢复；候选资源按保留期管理'
                            : index === 3 && plan.strategy === 'gateway_canary'
                              ? '当前策略暂不可执行'
                              : index === 3 && deployLog
                                ? deployLog.current_step
                                  ? deployStepName(deployLog.current_step)
                                  : deployLog.status === 'interrupted'
                                    ? '进程中断，需先核对集群实际状态'
                                    : deployLog.error_message ||
                                      deployLog.status
                                : index === 3 &&
                                    deployLogID &&
                                    execution.isError
                                  ? '无法读取任务详情，请稍后重试'
                                  : index === 3 && deployLogID
                                    ? `任务 #${deployLogID}`
                                    : '等待前置步骤'}
                </div>
              </div>
            </li>
          ))}
        </ol>
        {(plan.strategy === 'rolling' ||
          plan.strategy === 'blue_green' ||
          plan.strategy === 'gateway_canary') && (
          <ExecutionProgress
            strategy={plan.strategy}
            approvalState={approvalState}
            deployLogID={deployLogID}
            log={deployLog}
            run={deliveryRun.data?.data}
            actions={actionJournal.data?.data ?? []}
            loading={execution.isLoading}
            canReconcile={
              plan.strategy === 'rolling' &&
              approvalState === 'approved' &&
              (role === 'operator' || role === 'admin')
            }
            reconciling={reconcile.isPending}
            onReconcile={() => reconcile.mutate()}
          />
        )}
      </div>

      {plan.strategy === 'rolling' || plan.strategy === 'blue_green' ? (
        <div className='rounded-xl border bg-muted/20 p-4'>
          <div className='flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between'>
            <div>
              <div className='font-semibold'>
                {plan.strategy === 'blue_green' ? '蓝绿发布审批' : '独立审批'}
              </div>
              <p className='mt-1 text-sm text-muted-foreground'>
                申请人和审批人必须是不同账号；批准前会重新核对这份计划快照。
              </p>
              {approval && (
                <p className='mt-2 text-sm'>
                  审批 #{approval.id} ·{' '}
                  {approval.status === 'pending'
                    ? '等待另一位操作员'
                    : approval.status === 'approved'
                      ? plan.strategy === 'blue_green'
                        ? '已批准，候选环境准备中'
                        : '已批准，滚动任务已提交'
                      : `已${approval.status === 'rejected' ? '拒绝' : approval.status === 'expired' ? '过期' : '撤销'}`}
                </p>
              )}
              {requestApproval.data && !approval && (
                <p className='mt-2 text-sm text-emerald-700 dark:text-emerald-300'>
                  审批申请 #{requestApproval.data.data.request_id}{' '}
                  已提交，正在同步状态…
                </p>
              )}
              {requestError && (
                <p role='alert' className='mt-2 text-sm text-destructive'>
                  {requestError}
                </p>
              )}
              {allowApproval && manualReviewChecks.length > 0 && (
                <label className='mt-3 flex min-h-11 cursor-pointer items-start gap-3 rounded-lg border border-amber-300 bg-amber-50/70 p-3 text-sm text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'>
                  <input
                    type='checkbox'
                    className='mt-0.5 size-4 shrink-0 accent-amber-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-amber-700'
                    checked={manualReviewAcknowledged}
                    onChange={(event) =>
                      setManualReviewAcknowledged(event.target.checked)
                    }
                    aria-label='已核对发布计划的人工预检项'
                  />
                  <span>
                    我已核对并接受以下人工预检项：
                    <span className='mt-1 block text-xs leading-5 opacity-85'>
                      {manualReviewChecks
                        .map((check) => check.title)
                        .join('、')}
                      。
                      详细检查结果见上方预检清单；确认后该选择会记入操作审计。
                    </span>
                  </span>
                </label>
              )}
            </div>
            {canRequestApproval && (
              <Button
                className='min-h-11 shrink-0'
                disabled={requestApproval.isPending}
                onClick={() => requestApproval.mutate()}
              >
                {requestApproval.isPending
                  ? '正在校验快照…'
                  : plan.strategy === 'blue_green'
                    ? '提交蓝绿发布审批'
                    : '提交滚动发布审批'}
              </Button>
            )}
            {approvalState === 'pending' && (
              <Badge
                variant='outline'
                className='min-h-8 shrink-0 border-amber-300 text-amber-800'
              >
                待审批
              </Badge>
            )}
          </div>
        </div>
      ) : (
        <div className='rounded-xl border border-amber-200 bg-amber-50 p-4 dark:border-amber-900 dark:bg-amber-950/30'>
          <div className='flex items-center gap-2 font-semibold text-amber-950 dark:text-amber-100'>
            <ShieldAlert className='size-4' aria-hidden='true' />
            尚未开放执行
          </div>
          <ul className='mt-3 space-y-2 text-sm leading-relaxed text-amber-900 dark:text-amber-100'>
            {plan.blockers.map((blocker) => (
              <li key={blocker}>• {blocker}</li>
            ))}
          </ul>
        </div>
      )}

      <div className='grid gap-4 rounded-xl border bg-muted/20 p-4 sm:grid-cols-2'>
        <SnapshotValue
          label='目标资源身份'
          value={`${target.workload_uid || 'UID 未返回'} · RV ${target.resource_version || '未知'}`}
        />
        <SnapshotValue label='工作负载配置摘要' value={target.workload_hash} />
        <SnapshotValue label='快照指纹' value={plan.snapshot_hash} />
        <SnapshotValue
          label='计划创建时间'
          value={new Date(plan.created_at).toLocaleString()}
        />
      </div>

      <div className='flex items-center gap-2 text-xs text-muted-foreground'>
        <Fingerprint className='size-4 shrink-0' aria-hidden='true' />
        审批前如果制品、绑定、Deployment UID 或配置摘要变化，应重新生成计划。
      </div>
    </section>
  )
}

export function DeliveryPlanSnapshotDialog({
  planID,
  onClose,
}: {
  planID: number
  onClose: () => void
}) {
  const plan = useQuery({
    queryKey: ['delivery-plan', planID],
    queryFn: () => platformApi.deliveryPlan(planID),
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className='max-h-[90dvh] w-[calc(100%-2rem)] max-w-[calc(100%-2rem)] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:max-w-4xl'>
        <DialogHeader className='border-b px-6 py-5 pr-12'>
          <DialogTitle>发布计划 #{planID}</DialogTitle>
          <DialogDescription>
            这是申请人提交审批时核对的不可变快照；当前窗口只读，不会更改计划或集群。
          </DialogDescription>
        </DialogHeader>
        <div className='min-h-0 overflow-y-auto px-6 py-5'>
          {plan.isLoading ? (
            <p className='rounded-lg bg-muted/40 p-4 text-sm text-muted-foreground'>
              正在读取计划快照…
            </p>
          ) : plan.isError ? (
            <p
              role='alert'
              className='rounded-lg bg-destructive/5 p-4 text-sm text-destructive'
            >
              计划快照读取失败，请确认迁移 011/012 已执行后重试。
            </p>
          ) : plan.data ? (
            <PlanPreview plan={plan.data.data} allowApproval={false} />
          ) : null}
        </div>
        <DialogFooter className='border-t px-6 py-4'>
          <Button variant='outline' className='min-h-11' onClick={onClose}>
            关闭快照
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function DeliveryPlanDialog({
  release,
  onClose,
}: {
  release: ImageReleaseView
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [clusterID, setClusterID] = useState('')
  const [plan, setPlan] = useState<DeliveryPlan | null>(null)
  const targets = useQuery({
    queryKey: ['promotion-targets', release.id],
    queryFn: () => platformApi.promotionTargets(release.id),
  })
  const apps = useQuery({
    queryKey: ['apps'],
    queryFn: async () => (await platformApi.apps()).data,
  })
  const deliveryModel = useQuery({
    queryKey: ['delivery-model'],
    queryFn: async () => (await platformApi.deliveryModel()).data,
  })
  const createPlan = useMutation({
    mutationFn: () =>
      platformApi.createDeliveryPlan(release.id, Number(clusterID)),
    onSuccess: (response) => {
      setPlan(response.data)
      void queryClient.invalidateQueries({ queryKey: ['delivery-plans'] })
    },
  })
  const targetRows = targets.data?.data ?? []
  const appRows = apps.data ?? []
  const model = deliveryModel.data
  const targetOptions = targetRows.map((cluster) => {
    const app = appRows.find(
      (candidate) =>
        candidate.cluster_id === cluster.id &&
        candidate.app_name === release.app_name
    )
    const binding = model?.bindings.find(
      (candidate) => candidate.app_id === app?.id
    )
    const environment = model?.environments.find(
      (candidate) => candidate.id === binding?.environment_id
    )
    const application = model?.applications.find(
      (candidate) => candidate.id === binding?.application_id
    )
    const component = model?.components.find(
      (candidate) => candidate.id === binding?.component_id
    )
    const ready = Boolean(
      app &&
      binding &&
      environment &&
      application &&
      component &&
      !binding.requires_review &&
      !environment.requires_review &&
      !application.requires_review &&
      !component.requires_review &&
      component.application_id === binding.application_id
    )
    const reason = !app
      ? '应用映射未找到'
      : !binding
        ? '尚未绑定发布组件'
        : !ready
          ? '绑定待核对或关系不完整'
          : ''
    return { cluster, ready, reason }
  })
  const bindingReady = targetOptions.some((target) => target.ready)
  const error = (
    createPlan.error as { response?: { data?: { detail?: string } } } | null
  )?.response?.data?.detail

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className='max-h-[90dvh] w-[calc(100%-2rem)] max-w-[calc(100%-2rem)] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:max-w-4xl'>
        <DialogHeader className='border-b px-6 py-5 pr-12'>
          <div className='mb-1 text-xs font-semibold tracking-widest text-primary uppercase'>
            RELEASE PLAN
          </div>
          <DialogTitle className='text-xl'>发布计划预览</DialogTitle>
          <DialogDescription className='max-w-2xl leading-relaxed'>
            先核对构建制品和目标环境，再固定真实 Deployment
            基线。生成计划只保存快照，不会发布或切换流量。
          </DialogDescription>
        </DialogHeader>

        <div className='min-h-0 overflow-y-auto px-6 py-5'>
          {!plan ? (
            <div className='space-y-6'>
              <div className='grid gap-4 rounded-xl border bg-muted/20 p-4 sm:grid-cols-[1fr_auto_1fr] sm:items-center'>
                <div className='min-w-0'>
                  <div className='text-xs text-muted-foreground'>
                    云效构建产物
                  </div>
                  <div className='mt-1 font-mono text-sm font-semibold break-all'>
                    {release.image_full}
                  </div>
                  <div className='mt-2 text-xs break-all text-muted-foreground'>
                    {release.image_digest || '缺少 digest'} ·{' '}
                    {release.branch || '分支未知'}
                  </div>
                </div>
                <ArrowRight
                  className='hidden size-5 text-muted-foreground sm:block'
                  aria-hidden='true'
                />
                <div className='min-w-0'>
                  <Label htmlFor='delivery-plan-target'>目标生产环境</Label>
                  <Select value={clusterID} onValueChange={setClusterID}>
                    <SelectTrigger
                      id='delivery-plan-target'
                      className='mt-2 w-full'
                    >
                      <SelectValue placeholder='选择绑定了同一组件的生产集群' />
                    </SelectTrigger>
                    <SelectContent>
                      {targetOptions.map(({ cluster, ready, reason }) => (
                        <SelectItem
                          key={cluster.id}
                          value={String(cluster.id)}
                          disabled={!ready}
                        >
                          {cluster.name} · {cluster.environment}
                          {!ready ? `（${reason}）` : ''}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>

              {(targets.isLoading ||
                apps.isLoading ||
                deliveryModel.isLoading) && (
                <div className='rounded-lg bg-muted/40 p-4 text-sm text-muted-foreground'>
                  正在检查制品、审批门禁、应用映射和组件绑定…
                </div>
              )}
              {!targets.isLoading &&
                !apps.isLoading &&
                !deliveryModel.isLoading &&
                targetRows.length === 0 && (
                  <div className='rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm leading-relaxed text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'>
                    当前没有满足发布条件的生产目标。请检查测试环境部署、digest、业务验收和生产应用是否绑定到同一逻辑组件。
                  </div>
                )}
              {targets.isError || apps.isError || deliveryModel.isError ? (
                <p role='alert' className='text-sm text-destructive'>
                  环境或绑定信息加载失败，请刷新后重试。
                </p>
              ) : null}
              {!targets.isLoading &&
                !apps.isLoading &&
                !deliveryModel.isLoading &&
                !targets.isError &&
                !apps.isError &&
                !deliveryModel.isError &&
                targetRows.length > 0 &&
                !bindingReady && (
                  <div className='flex flex-col gap-3 rounded-xl border border-amber-200 bg-amber-50 p-4 text-sm sm:flex-row sm:items-center sm:justify-between dark:border-amber-900 dark:bg-amber-950/30'>
                    <p className='leading-relaxed text-amber-950 dark:text-amber-100'>
                      生产应用尚未完成组件与环境绑定。先在环境配置中把测试、生产目标关联到同一组件，再回来生成发布计划。
                    </p>
                    <Button asChild variant='outline' className='shrink-0'>
                      <a href='/environments'>配置环境绑定</a>
                    </Button>
                  </div>
                )}
              {error && (
                <p
                  role='alert'
                  className='rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive'
                >
                  {error}
                </p>
              )}

              <div className='flex flex-col gap-3 rounded-xl border p-4 sm:flex-row sm:items-center sm:justify-between'>
                <div>
                  <div className='font-medium'>计划会固定什么</div>
                  <p className='mt-1 text-sm text-muted-foreground'>
                    组件关系、构建 digest、当前镜像、Deployment UID 和配置摘要。
                  </p>
                </div>
                <Button
                  className='min-h-11 shrink-0'
                  disabled={
                    !clusterID ||
                    createPlan.isPending ||
                    !targetOptions.some(
                      (target) =>
                        target.cluster.id === Number(clusterID) && target.ready
                    )
                  }
                  onClick={() => createPlan.mutate()}
                >
                  {createPlan.isPending ? '核对并生成…' : '核对目标并生成计划'}
                </Button>
              </div>
            </div>
          ) : (
            <PlanPreview plan={plan} />
          )}
        </div>

        <DialogFooter className='border-t px-6 py-4'>
          {plan ? (
            <Button variant='outline' className='min-h-11' onClick={onClose}>
              关闭预览
            </Button>
          ) : (
            <Button variant='ghost' className='min-h-11' onClick={onClose}>
              取消
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function DeliveryPlanHistory() {
  const plans = useQuery({
    queryKey: ['delivery-plans'],
    queryFn: () => platformApi.deliveryPlans(),
    refetchInterval: 15000,
  })
  const approvals = useQuery({
    queryKey: ['promotion-requests'],
    queryFn: () => platformApi.promotionRequests(),
    refetchInterval: 15000,
  })
  const rows = [...(plans.data?.data ?? [])].reverse().slice(0, 6)
  const latestApproval = (planID: number) =>
    [...(approvals.data?.data ?? [])]
      .filter((request) => request.delivery_plan_id === planID)
      .sort((left, right) => right.id - left.id)[0]
  const approvalLabel = (plan: DeliveryPlan) => {
    const request = latestApproval(plan.id)
    if (request?.status === 'pending') return '待审批'
    if (request?.status === 'approved') return '审批已通过'
    if (request?.status === 'rejected') return '已拒绝'
    if (request?.status === 'cancelled') return '已撤销'
    if (request?.status === 'expired') return '已过期'
    if (approvals.isError) return '审批状态未知'
    return plan.strategy === 'rolling' ? '待提交审批' : '执行器未开放'
  }
  const approvalTone = (plan: DeliveryPlan) => {
    const request = latestApproval(plan.id)
    if (request?.status === 'approved')
      return 'border-emerald-300 text-emerald-800 dark:text-emerald-200'
    if (request?.status === 'pending')
      return 'border-amber-300 text-amber-800 dark:text-amber-200'
    if (request?.status === 'rejected' || plan.strategy !== 'rolling')
      return 'border-amber-300 text-amber-800 dark:text-amber-200'
    return ''
  }

  return (
    <>
      <Card className='mt-6 rounded-2xl border-border/70 shadow-sm'>
        <CardHeader className='flex flex-row items-start justify-between gap-4'>
          <div>
            <CardTitle className='text-base'>发布计划</CardTitle>
            <p className='mt-1 text-sm text-muted-foreground'>
              固定制品和目标基线的只读快照
            </p>
          </div>
          <Badge variant='outline' className='shrink-0'>
            {plans.data?.data.length ?? 0} 条记录
          </Badge>
        </CardHeader>
        <CardContent>
          {plans.isError ? (
            <p
              role='alert'
              className='rounded-lg bg-destructive/5 p-4 text-sm text-destructive'
            >
              发布计划读取失败，请检查后端迁移 011 和登录状态。
            </p>
          ) : rows.length === 0 ? (
            <div className='rounded-xl border border-dashed bg-muted/20 px-5 py-6 text-center'>
              <ClipboardCheck
                className='mx-auto size-6 text-muted-foreground'
                aria-hidden='true'
              />
              <p className='mt-3 font-medium'>这里会保留每次计划预览</p>
              <p className='mt-1 text-sm text-muted-foreground'>
                在符合发布条件的制品行选择“创建发布计划”开始。
              </p>
            </div>
          ) : (
            <div className='divide-y rounded-xl border'>
              {rows.map((plan) => (
                <div
                  key={plan.id}
                  className='flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between'
                >
                  <div className='min-w-0'>
                    <div className='flex flex-wrap items-center gap-2'>
                      <span className='font-semibold'>计划 #{plan.id}</span>
                      <Badge variant='outline'>
                        {strategyLabels[plan.strategy]}
                      </Badge>
                      <Badge variant='outline'>快照固定</Badge>
                      <Badge variant='outline' className={approvalTone(plan)}>
                        {approvalLabel(plan)}
                      </Badge>
                    </div>
                    <div className='mt-1 text-sm text-muted-foreground'>
                      {plan.snapshot.target.application_name} /{' '}
                      {plan.snapshot.target.component_name}
                      <span className='mx-2'>·</span>
                      {plan.snapshot.target.environment_name}
                    </div>
                    <div
                      className='mt-1 truncate font-mono text-xs text-muted-foreground'
                      title={plan.snapshot.artifact.image}
                    >
                      {plan.snapshot.artifact.image}
                    </div>
                  </div>
                  <div className='flex shrink-0 items-center gap-3'>
                    <span className='text-xs text-muted-foreground'>
                      {new Date(plan.created_at).toLocaleString()}
                    </span>
                    <Button
                      asChild
                      variant='outline'
                      size='sm'
                      className='min-h-10'
                    >
                      <Link
                        to='/tasks/plans/$planId'
                        params={{ planId: String(plan.id) }}
                      >
                        查看计划
                      </Link>
                    </Button>
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </>
  )
}
