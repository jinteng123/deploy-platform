import { useState } from 'react'
import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import {
  Activity,
  AlertCircle,
  BellRing,
  Check,
  CircleCheck,
  CircleHelp,
  Clock3,
  Plus,
  ShieldAlert,
} from 'lucide-react'
import {
  platformApi,
  type DeliveryIncident,
  type DeliveryRemediationPlanStatus,
  type DeliveryRemediationType,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'

const duration = (value: number | null) =>
  value === null
    ? '暂无数据'
    : value < 60
      ? `${Math.round(value)} 秒`
      : value < 3600
        ? `${Math.round(value / 60)} 分钟`
        : value < 86400
          ? `${Number((value / 3600).toFixed(1))} 小时`
          : `${(value / 86400).toFixed(1)} 天`
const metricNames: Record<string, string> = {
  change_lead_time: '变更前置时间中位数',
  deployment_frequency: '部署频率',
  failed_deployment_recovery_time: '失败部署恢复时间',
  change_fail_rate: '变更失败率',
  deployment_rework_rate: '部署返工率',
}
const remediationTypeLabels: Record<DeliveryRemediationType, string> = {
  rollback: '回滚',
  hotfix_deployment: '热修复部署',
  configuration_deployment: '配置修复部署',
  restart_or_scale: '重启 / 扩缩容',
  no_action: '无需处置',
  unknown: '处置待分类',
}
const usesRemediationDeployment = (value: DeliveryRemediationType) =>
  value === 'rollback' ||
  value === 'hotfix_deployment' ||
  value === 'configuration_deployment'

const driftTone = {
  in_sync:
    'border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-200',
  drifted: 'border-destructive/30 bg-destructive/5 text-destructive',
  unknown: 'border-border bg-muted text-muted-foreground',
} as const

const driftLabel = {
  in_sync: '镜像一致',
  drifted: '发现镜像漂移',
  unknown: '状态未知',
} as const

const driftChangeLabel = {
  initial: '首次观察',
  status: '状态变化',
  baseline: '发布基线变化',
  image: '镜像变化',
  reason: '读取结果变化',
} as const

const driftSourceLabel = (source?: string) =>
  source === 'background_poll'
    ? '后台巡检'
    : source === 'on_demand_read'
      ? '人工核对'
      : '来源未知'

const notificationEventLabel: Record<string, string> = {
  deploy_finished: '发布完成',
  deploy_failed: '发布未完成',
  promote_approved: '生产审批通过',
  image_drift_detected: '发现镜像漂移',
  image_drift_recovered: '镜像漂移已恢复',
}

const observedTime = (value?: string | null) =>
  value
    ? new Intl.DateTimeFormat('zh-CN', {
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
      }).format(new Date(value))
    : '暂无记录'

const metricTimestamp = (value: string) =>
  new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))

const localDateTimeInput = (date = new Date()) =>
  new Date(date.getTime() - date.getTimezoneOffset() * 60_000)
    .toISOString()
    .slice(0, 16)

function MetricTile({
  label,
  value,
  hint,
}: {
  label: string
  value: string
  hint?: string
}) {
  return (
    <div className='min-w-0 rounded-xl border bg-muted/20 p-4'>
      <dt className='text-sm text-muted-foreground'>{label}</dt>
      <dd className='mt-2 text-2xl font-semibold tracking-tight break-words tabular-nums'>
        {value}
      </dd>
      {hint && (
        <p className='mt-1 text-xs leading-5 text-muted-foreground'>{hint}</p>
      )}
    </div>
  )
}

export function DeliveryOverview() {
  const role = useAuthStore((state) => state.auth.user?.role)
  const queryClient = useQueryClient()
  const notifications = useInfiniteQuery({
    queryKey: ['delivery-notification-alerts'],
    queryFn: ({ pageParam }) =>
      platformApi.deliveryNotificationAlerts(pageParam || undefined),
    initialPageParam: '',
    getNextPageParam: (lastPage) => lastPage.data.next_cursor || undefined,
    enabled: role === 'admin',
    refetchInterval: 15000,
  })
  const [clusterId, setClusterId] = useState('')
  const [windowDays, setWindowDays] = useState(30)
  const [metricClusterId, setMetricClusterId] = useState('all')
  const [metricAppName, setMetricAppName] = useState('all')
  const [incidentClusterId, setIncidentClusterId] = useState('')
  const [incidentAppName, setIncidentAppName] = useState('all')
  const [incidentDeployLogId, setIncidentDeployLogId] = useState('')
  const [incidentOccurredAt, setIncidentOccurredAt] = useState(() =>
    localDateTimeInput()
  )
  const [incidentSummary, setIncidentSummary] = useState('')
  const [interveningIncidentId, setInterveningIncidentId] = useState<
    number | null
  >(null)
  const [interventionOccurredAt, setInterventionOccurredAt] = useState(() =>
    localDateTimeInput()
  )
  const [interventionDetail, setInterventionDetail] = useState('')
  const [recoveringIncidentId, setRecoveringIncidentId] = useState<
    number | null
  >(null)
  const [recoveryOccurredAt, setRecoveryOccurredAt] = useState(() =>
    localDateTimeInput()
  )
  const [recoveryConfirmation, setRecoveryConfirmation] = useState('')
  const [causalityIncidentId, setCausalityIncidentId] = useState<number | null>(
    null
  )
  const [causalityStatus, setCausalityStatus] = useState<
    'caused_by_deployment' | 'not_caused_by_deployment' | ''
  >('')
  const [causalityReason, setCausalityReason] = useState('')
  const [remediationIncidentId, setRemediationIncidentId] = useState<
    number | null
  >(null)
  const [remediationType, setRemediationType] = useState<
    Exclude<DeliveryRemediationType, 'unknown'> | ''
  >('')
  const [remediationPlanStatus, setRemediationPlanStatus] = useState<
    Exclude<DeliveryRemediationPlanStatus, 'unknown'> | ''
  >('')
  const [remediationDeployLogID, setRemediationDeployLogID] = useState('')
  const [remediationReason, setRemediationReason] = useState('')
  const updateIncidentCache = (incident: DeliveryIncident) =>
    queryClient.setQueryData<{ data: DeliveryIncident[] }>(
      ['delivery-incidents'],
      (current) =>
        current
          ? {
              ...current,
              data: current.data.map((row) =>
                row.id === incident.id ? incident : row
              ),
            }
          : current
    )
  const clusters = useQuery({
    queryKey: ['clusters'],
    queryFn: platformApi.clusters,
  })
  const applications = useQuery({
    queryKey: ['apps'],
    queryFn: () => platformApi.apps(),
  })
  const metricTargetId =
    metricClusterId === 'all' ? undefined : Number(metricClusterId)
  const productionTargetIds = new Set(
    (clusters.data?.data ?? [])
      .filter((cluster) => cluster.environment_tier === 'production')
      .map((cluster) => cluster.id)
  )
  const metricAppNames = Array.from(
    new Set(
      (applications.data?.data ?? [])
        .filter((app) =>
          metricTargetId
            ? app.cluster_id === metricTargetId
            : productionTargetIds.has(app.cluster_id)
        )
        .map((app) => app.app_name)
    )
  ).sort((left, right) => left.localeCompare(right, 'zh-CN'))
  const metrics = useQuery({
    queryKey: ['delivery-metrics', windowDays, metricTargetId, metricAppName],
    queryFn: () =>
      platformApi.deliveryMetrics(windowDays, {
        appName: metricAppName === 'all' ? undefined : metricAppName,
        clusterId: metricTargetId,
      }),
    refetchInterval: 15000,
  })
  const incidentTargetId =
    incidentClusterId ||
    String(
      (clusters.data?.data ?? []).find(
        (cluster) => cluster.environment_tier === 'production'
      )?.id ?? ''
    )
  const incidentAppNames = Array.from(
    new Set(
      (applications.data?.data ?? [])
        .filter((app) => app.cluster_id === Number(incidentTargetId))
        .map((app) => app.app_name)
    )
  ).sort((left, right) => left.localeCompare(right, 'zh-CN'))
  const incidentLogs = useQuery({
    queryKey: [
      'delivery-incident-deploy-logs',
      incidentTargetId,
      incidentAppName,
    ],
    queryFn: () =>
      platformApi.logs({
        cluster_id: Number(incidentTargetId),
        ...(incidentAppName === 'all' ? {} : { app_name: incidentAppName }),
        limit: 100,
      }),
    enabled: !!incidentTargetId,
    retry: false,
  })
  const eligibleIncidentLogs = (incidentLogs.data?.data ?? []).filter(
    (log) => log.environment_tier_snapshot === 'production'
  )
  const incidents = useQuery({
    queryKey: ['delivery-incidents'],
    queryFn: () => platformApi.deliveryIncidents(100),
    refetchInterval: 30000,
  })
  const incidentRows = incidents.data?.data ?? []
  const activeRemediationIncident = incidentRows.find(
    (incident) => incident.id === remediationIncidentId
  )
  const remediationTarget = activeRemediationIncident
    ? {
        id: activeRemediationIncident.id,
        clusterID: activeRemediationIncident.cluster_id,
        appName: activeRemediationIncident.app_name,
      }
    : null
  const remediationLogs = useQuery({
    queryKey: ['delivery-incident-remediation-deploy-logs', remediationTarget],
    queryFn: () => {
      if (!remediationTarget) throw new Error('生产事件不存在')
      return platformApi.logs({
        cluster_id: remediationTarget.clusterID,
        app_name: remediationTarget.appName,
        limit: 100,
      })
    },
    enabled: !!remediationTarget,
    retry: false,
  })
  const createIncident = useMutation({
    mutationFn: () =>
      platformApi.createDeliveryIncident({
        deploy_log_id: Number(incidentDeployLogId),
        occurred_at: new Date(incidentOccurredAt).toISOString(),
        summary: incidentSummary.trim(),
        idempotency_key: crypto.randomUUID(),
      }),
    onSuccess: async () => {
      setIncidentSummary('')
      setIncidentDeployLogId('')
      await queryClient.invalidateQueries({ queryKey: ['delivery-incidents'] })
    },
  })
  const confirmIncidentRecovery = useMutation({
    mutationFn: (incident: { id: number; version: number }) =>
      platformApi.confirmDeliveryIncidentRecovery(incident.id, {
        expected_version: incident.version,
        occurred_at: new Date(recoveryOccurredAt).toISOString(),
        confirmation: recoveryConfirmation.trim(),
        idempotency_key: crypto.randomUUID(),
      }),
    onSuccess: (response) => {
      setRecoveringIncidentId(null)
      setRecoveryConfirmation('')
      updateIncidentCache(response.data)
    },
  })
  const startIncidentIntervention = useMutation({
    mutationFn: (incident: { id: number; version: number }) =>
      platformApi.startDeliveryIncidentIntervention(incident.id, {
        expected_version: incident.version,
        occurred_at: new Date(interventionOccurredAt).toISOString(),
        detail: interventionDetail.trim(),
        idempotency_key: crypto.randomUUID(),
      }),
    onSuccess: (response) => {
      setInterveningIncidentId(null)
      setInterventionDetail('')
      updateIncidentCache(response.data)
    },
  })
  const assessIncidentCausality = useMutation({
    mutationFn: (input: {
      id: number
      version: number
      status: 'caused_by_deployment' | 'not_caused_by_deployment'
      reason: string
    }) =>
      platformApi.assessDeliveryIncidentCausality(input.id, {
        expected_version: input.version,
        status: input.status,
        reason: input.reason,
        idempotency_key: crypto.randomUUID(),
      }),
    onSuccess: (response) => {
      setCausalityIncidentId(null)
      setCausalityStatus('')
      setCausalityReason('')
      updateIncidentCache(response.data)
    },
  })
  const recordIncidentRemediation = useMutation({
    mutationFn: (input: {
      id: number
      version: number
      type: Exclude<DeliveryRemediationType, 'unknown'>
      planStatus: Exclude<DeliveryRemediationPlanStatus, 'unknown'>
      deployLogID?: number
      reason: string
    }) =>
      platformApi.recordDeliveryIncidentRemediation(input.id, {
        expected_version: input.version,
        remediation_type: input.type,
        plan_status: input.planStatus,
        ...(input.deployLogID
          ? { remediation_deploy_log_id: input.deployLogID }
          : {}),
        reason: input.reason,
        idempotency_key: crypto.randomUUID(),
      }),
    onSuccess: (response) => {
      setRemediationIncidentId(null)
      setRemediationType('')
      setRemediationPlanStatus('')
      setRemediationDeployLogID('')
      setRemediationReason('')
      updateIncidentCache(response.data)
    },
  })
  const drift = useQuery({
    queryKey: ['delivery-drift', clusterId],
    queryFn: () => platformApi.deliveryDrift(Number(clusterId)),
    enabled: !!clusterId,
    refetchInterval: 30000,
    retry: false,
  })
  const driftHistory = useQuery({
    queryKey: ['delivery-drift-history', clusterId],
    queryFn: () =>
      platformApi.deliveryDriftHistory(Number(clusterId), undefined, 25),
    enabled: !!clusterId && !drift.isPending,
    refetchInterval: 30000,
    retry: false,
  })
  const metricData = metrics.data?.data
  const pendingNotifications =
    notifications.data?.pages.flatMap((page) => page.data.items) ?? []

  return (
    <Card className='mt-6 overflow-hidden'>
      <CardHeader className='border-b bg-muted/20 px-5 py-5 sm:px-6'>
        <div className='flex flex-wrap items-center justify-between gap-4'>
          <div className='flex min-w-0 items-start gap-3'>
            <div className='flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary ring-1 ring-primary/10'>
              <Activity className='size-5' aria-hidden='true' />
            </div>
            <div className='min-w-0'>
              <CardTitle>发布观测</CardTitle>
              <p className='mt-1 max-w-2xl text-sm leading-5 text-muted-foreground'>
                查看发布执行、镜像漂移和通知投递。执行成功不代表上线后的业务健康。
              </p>
            </div>
          </div>
          <Badge variant='outline' className='bg-background'>
            只读观测
          </Badge>
        </div>
      </CardHeader>
      <CardContent className='space-y-6 p-5 sm:p-6'>
        <section
          aria-labelledby='delivery-metrics-title'
          aria-busy={metrics.isPending}
        >
          <div className='mb-3 flex flex-wrap items-end justify-between gap-2'>
            <div>
              <h3 id='delivery-metrics-title' className='font-semibold'>
                执行概况
              </h3>
              <p className='mt-1 text-sm text-muted-foreground'>
                最近 {windowDays} 天发起的生产任务，包含重试和镜像回退。
              </p>
            </div>
            <div className='w-full max-w-3xl sm:w-auto'>
              <div className='grid gap-2 sm:grid-cols-2 lg:grid-cols-3'>
                <div className='min-w-0'>
                  <Label
                    htmlFor='delivery-metric-window'
                    className='mb-1.5 block text-xs text-muted-foreground'
                  >
                    发布指标统计窗口
                  </Label>
                  <Select
                    value={String(windowDays)}
                    onValueChange={(value) => setWindowDays(Number(value))}
                  >
                    <SelectTrigger
                      id='delivery-metric-window'
                      className='min-h-11 bg-background'
                    >
                      <SelectValue placeholder='选择统计窗口' />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value='7'>近 7 天</SelectItem>
                      <SelectItem value='30'>近 30 天</SelectItem>
                      <SelectItem value='90'>近 90 天</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className='min-w-0'>
                  <Label
                    htmlFor='delivery-metric-target'
                    className='mb-1.5 block text-xs text-muted-foreground'
                  >
                    生产目标
                  </Label>
                  <Select
                    value={metricClusterId}
                    onValueChange={(value) => {
                      setMetricClusterId(value)
                      setMetricAppName('all')
                    }}
                  >
                    <SelectTrigger
                      id='delivery-metric-target'
                      className='min-h-11 bg-background'
                    >
                      <SelectValue placeholder='全部生产目标' />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value='all'>全部生产目标</SelectItem>
                      {(clusters.data?.data ?? [])
                        .filter((cluster) =>
                          productionTargetIds.has(cluster.id)
                        )
                        .map((cluster) => (
                          <SelectItem
                            key={cluster.id}
                            value={String(cluster.id)}
                          >
                            {cluster.name}
                            {cluster.enabled ? '' : ' · 已停用'}
                          </SelectItem>
                        ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className='min-w-0'>
                  <Label
                    htmlFor='delivery-metric-app'
                    className='mb-1.5 block text-xs text-muted-foreground'
                  >
                    应用
                  </Label>
                  <Select
                    value={metricAppName}
                    onValueChange={setMetricAppName}
                  >
                    <SelectTrigger
                      id='delivery-metric-app'
                      className='min-h-11 bg-background'
                    >
                      <SelectValue placeholder='全部应用' />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value='all'>全部应用</SelectItem>
                      {metricAppNames.map((appName) => (
                        <SelectItem key={appName} value={appName}>
                          {appName}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              </div>
              {metricData && (
                <div className='mt-2 space-y-1 text-right text-xs text-muted-foreground'>
                  <p className='break-words'>
                    统计范围{' '}
                    <time
                      dateTime={metricData.window_start}
                      title={metricData.window_start}
                    >
                      {metricTimestamp(metricData.window_start)}
                    </time>
                    {' 至 '}
                    <time dateTime={metricData.as_of} title={metricData.as_of}>
                      {metricTimestamp(metricData.as_of)}
                    </time>
                  </p>
                  <p>
                    {metrics.isFetching ? '正在更新' : '数据已更新'}
                    {' · '}
                    <time dateTime={metricData.as_of} title={metricData.as_of}>
                      {observedTime(metricData.as_of)}
                    </time>
                    {' · 15 秒刷新'}
                  </p>
                </div>
              )}
              {applications.isError && (
                <p
                  role='status'
                  className='mt-2 text-right text-xs text-muted-foreground'
                >
                  应用列表加载失败，当前仍可查看目标级指标。
                </p>
              )}
            </div>
          </div>
          {metrics.isError ? (
            <div
              role='alert'
              className='flex items-start gap-2 rounded-lg border border-destructive/25 bg-destructive/5 p-3 text-sm text-destructive'
            >
              <AlertCircle
                className='mt-0.5 size-4 shrink-0'
                aria-hidden='true'
              />
              执行指标加载失败，当前无法确认发布概况。
            </div>
          ) : !metricData ? (
            <div
              role='status'
              aria-label='正在加载执行概况'
              className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'
            >
              {Array.from({ length: 4 }, (_, index) => (
                <div key={index} className='space-y-3 rounded-xl border p-4'>
                  <Skeleton className='h-4 w-28' />
                  <Skeleton className='h-7 w-20' />
                </div>
              ))}
            </div>
          ) : (
            <>
              <dl className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
                <MetricTile
                  label='成功 / 发起任务'
                  value={`${metricData.successful_deployments} / ${metricData.production_attempts}`}
                  hint={`${metricData.active_tasks} 个任务仍在进行`}
                />
                <MetricTile
                  label='执行失败率'
                  value={
                    metricData.execution_failure_rate === null
                      ? '暂无数据'
                      : `${(metricData.execution_failure_rate * 100).toFixed(1)}%`
                  }
                  hint={`基于 ${metricData.successful_deployments + metricData.execution_failures} 个已结束任务`}
                />
                <MetricTile
                  label='执行耗时 P95'
                  value={duration(metricData.execution_p95_seconds)}
                  hint={`基于 ${metricData.duration_samples} 个有效样本`}
                />
                <MetricTile
                  label='审批等待 P95'
                  value={duration(metricData.approval_wait_p95_seconds)}
                  hint={`${metricData.approval_wait_samples} 个决策样本 · ${metricData.pending_approvals} 个待审批`}
                />
              </dl>
              <section
                aria-label='线上事件恢复观察'
                className='mt-6 border-t pt-5'
              >
                <div className='mb-3'>
                  <h4 className='font-semibold'>线上事件恢复观察</h4>
                  <p className='mt-1 text-sm leading-6 text-muted-foreground'>
                    统计窗口内由操作员登记的生产事件。事件与部署的因果关系尚未验证，不作为
                    DORA 指标。
                  </p>
                </div>
                <dl className='grid gap-3 sm:grid-cols-2'>
                  <MetricTile
                    label='故障到业务恢复均值'
                    value={duration(
                      metricData.incident_recovery.mean_recovery_seconds
                    )}
                    hint={`基于 ${metricData.incident_recovery.recovered_samples} 个已恢复事件；${metricData.incident_recovery.unrecovered_samples} 个尚未恢复`}
                  />
                  <MetricTile
                    label='干预到业务恢复均值'
                    value={duration(
                      metricData.incident_recovery
                        .mean_intervention_to_recovery_seconds
                    )}
                    hint={`基于 ${metricData.incident_recovery.intervention_recovery_samples} 个有干预时间的有效事件`}
                  />
                </dl>
                <section
                  aria-label='已登记事件的归因与处置记录完整度'
                  className='mt-4 rounded-xl border bg-muted/10 p-4'
                >
                  <div>
                    <h5 className='text-sm font-medium'>
                      已登记事件的归因与处置记录
                    </h5>
                    <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                      只统计发生时间有效的已登记事件；这些数字不能证明平台外故障也已全部登记。
                    </p>
                  </div>
                  <dl className='mt-3 grid gap-3 sm:grid-cols-2'>
                    <MetricTile
                      label='已有因果判定'
                      value={`${metricData.incident_recovery.causality_assessed_samples} / ${metricData.incident_recovery.incident_samples}`}
                      hint={`${metricData.incident_recovery.causality_pending_samples} 个待判定`}
                    />
                    <MetricTile
                      label='已有完整处置分类'
                      value={`${metricData.incident_recovery.remediation_classified_samples} / ${metricData.incident_recovery.incident_samples}`}
                      hint={`${metricData.incident_recovery.remediation_pending_samples} 个待分类`}
                    />
                  </dl>
                </section>
                {metricData.incident_recovery.missing_data && (
                  <p
                    role='note'
                    className='mt-3 flex items-start gap-2 rounded-lg border bg-muted/30 p-3 text-sm text-muted-foreground'
                  >
                    <CircleHelp
                      className='mt-0.5 size-4 shrink-0'
                      aria-hidden='true'
                    />
                    {metricData.incident_recovery.missing_data}
                    {metricData.incident_recovery.data_available &&
                      `；窗口内共 ${metricData.incident_recovery.incident_samples} 个事件`}
                    。
                  </p>
                )}
                {metricData.incident_recovery.invalid_timestamp_samples > 0 && (
                  <p className='mt-3 flex items-start gap-2 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'>
                    <AlertCircle
                      className='mt-0.5 size-4 shrink-0'
                      aria-hidden='true'
                    />
                    {metricData.incident_recovery.invalid_timestamp_samples}{' '}
                    条线上事件时间异常，未纳入恢复时长。
                  </p>
                )}
              </section>
              {metricData.unknown_tier_samples > 0 && (
                <p
                  role='note'
                  className='mt-3 flex items-start gap-2 rounded-lg border bg-muted/30 p-3 text-sm text-muted-foreground'
                >
                  <CircleHelp
                    className='mt-0.5 size-4 shrink-0'
                    aria-hidden='true'
                  />
                  {metricData.unknown_tier_samples}{' '}
                  条历史任务没有创建时的环境等级记录，未计入生产指标。
                </p>
              )}
              {(metricData.invalid_timestamp_samples ?? 0) > 0 && (
                <p className='mt-3 flex items-start gap-2 rounded-lg border border-amber-300 bg-amber-50 p-3 text-sm text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'>
                  <AlertCircle
                    className='mt-0.5 size-4 shrink-0'
                    aria-hidden='true'
                  />
                  任务或审批时间异常：{metricData.invalid_timestamp_samples}{' '}
                  条记录存在缺失或无效时间，相应样本未纳入统计。请核对历史时间数据。
                </p>
              )}
              <details className='group mt-4 rounded-lg border bg-background px-4 py-3 open:bg-muted/10'>
                <summary className='cursor-pointer rounded-sm text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-ring'>
                  DORA 指标的数据完整性
                </summary>
                <ul className='mt-3 grid gap-2 border-t pt-3 text-sm sm:grid-cols-2'>
                  {Object.entries(metricData.dora).map(([name, value]) => (
                    <li
                      key={name}
                      className='flex min-w-0 items-start gap-2 rounded-md bg-muted/40 p-3'
                    >
                      {value.value === null ? (
                        <CircleHelp
                          className='mt-0.5 size-4 shrink-0 text-muted-foreground'
                          aria-hidden='true'
                        />
                      ) : (
                        <CircleCheck
                          className='mt-0.5 size-4 shrink-0 text-emerald-600 dark:text-emerald-400'
                          aria-hidden='true'
                        />
                      )}
                      <span className='min-w-0'>
                        <span className='font-medium'>
                          {metricNames[name] ?? name}
                        </span>
                        <span className='mt-0.5 block text-sm text-muted-foreground'>
                          {value.value === null
                            ? `未知 — ${value.missing_data}${value.sample_count ? `（已有 ${value.sample_count} 个有效样本）` : ''}`
                            : name === 'deployment_frequency'
                              ? `${value.value} 次 / ${metricData.window_days} 天`
                              : name === 'change_lead_time'
                                ? `${duration(value.value)} · ${value.sample_count ?? 0} 次发布`
                                : value.value}
                        </span>
                      </span>
                    </li>
                  ))}
                </ul>
              </details>
            </>
          )}
        </section>

        <section
          aria-labelledby='delivery-incidents-title'
          className='border-t pt-5'
        >
          <div className='mb-4 flex flex-wrap items-start justify-between gap-3'>
            <div className='flex min-w-0 items-start gap-3'>
              <div className='flex size-9 shrink-0 items-center justify-center rounded-lg bg-amber-500/10 text-amber-700 dark:text-amber-300'>
                <ShieldAlert className='size-4' aria-hidden='true' />
              </div>
              <div className='min-w-0'>
                <h3 id='delivery-incidents-title' className='font-semibold'>
                  线上故障与恢复事件
                </h3>
                <p className='mt-1 max-w-2xl text-sm leading-5 text-muted-foreground'>
                  选择关联的生产部署，记录故障、干预与业务恢复。因果判定由操作员填写依据，平台不自动归因；覆盖率不足时
                  DORA 仍显示未知。
                </p>
              </div>
            </div>
            <Badge variant='outline' className='bg-background'>
              最近 100 条 ·{' '}
              {
                incidentRows.filter((incident) => incident.status === 'open')
                  .length
              }{' '}
              个待恢复
            </Badge>
          </div>

          {(role === 'operator' || role === 'admin') && (
            <form
              className='mb-4 rounded-xl border bg-muted/15 p-4 sm:p-5'
              onSubmit={(event) => {
                event.preventDefault()
                createIncident.mutate()
              }}
            >
              <div className='mb-4 flex items-start gap-2'>
                <Plus
                  className='mt-0.5 size-4 text-primary'
                  aria-hidden='true'
                />
                <div>
                  <h4 className='text-sm font-semibold'>登记线上事件</h4>
                  <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                    选择平台记录的生产部署，事件时间按你实际观察到的时间填写。
                  </p>
                </div>
              </div>
              <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-4'>
                <div className='min-w-0'>
                  <Label
                    htmlFor='incident-cluster'
                    className='mb-1.5 block text-xs'
                  >
                    生产目标
                  </Label>
                  <Select
                    value={incidentTargetId || 'none'}
                    onValueChange={(value) => {
                      setIncidentClusterId(value)
                      setIncidentDeployLogId('')
                      setIncidentAppName('all')
                    }}
                  >
                    <SelectTrigger
                      id='incident-cluster'
                      className='min-h-11 bg-background'
                    >
                      <SelectValue placeholder='选择生产目标' />
                    </SelectTrigger>
                    <SelectContent>
                      {(clusters.data?.data ?? [])
                        .filter(
                          (cluster) => cluster.environment_tier === 'production'
                        )
                        .map((cluster) => (
                          <SelectItem
                            key={cluster.id}
                            value={String(cluster.id)}
                          >
                            {cluster.name}
                          </SelectItem>
                        ))}
                    </SelectContent>
                  </Select>
                  {incidentTargetId && !incidentClusterId && (
                    <p className='mt-1 text-xs text-muted-foreground'>
                      默认选择第一个生产目标
                    </p>
                  )}
                </div>
                <div className='min-w-0'>
                  <Label
                    htmlFor='incident-app'
                    className='mb-1.5 block text-xs'
                  >
                    应用筛选
                  </Label>
                  <Select
                    value={incidentAppName}
                    onValueChange={(value) => {
                      setIncidentAppName(value)
                      setIncidentDeployLogId('')
                    }}
                  >
                    <SelectTrigger
                      id='incident-app'
                      className='min-h-11 bg-background'
                    >
                      <SelectValue placeholder='全部应用' />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value='all'>全部应用</SelectItem>
                      {incidentAppNames.map((name) => (
                        <SelectItem key={name} value={name}>
                          {name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
                <div className='min-w-0 sm:col-span-2'>
                  <Label
                    htmlFor='incident-deploy-log'
                    className='mb-1.5 block text-xs'
                  >
                    关联生产部署
                  </Label>
                  <Select
                    value={incidentDeployLogId}
                    onValueChange={setIncidentDeployLogId}
                  >
                    <SelectTrigger
                      id='incident-deploy-log'
                      className='min-h-11 bg-background'
                    >
                      <SelectValue
                        placeholder={
                          incidentLogs.isPending
                            ? '正在读取部署记录'
                            : '选择一条部署记录'
                        }
                      />
                    </SelectTrigger>
                    <SelectContent>
                      {eligibleIncidentLogs.map((log) => (
                        <SelectItem key={log.id} value={String(log.id)}>
                          #{log.id} · {log.app_name} / {log.deployment_name} ·{' '}
                          {log.status} · {observedTime(log.created_at)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  {!incidentLogs.isPending &&
                    !incidentLogs.isError &&
                    eligibleIncidentLogs.length === 0 && (
                      <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                        没有可关联的生产部署记录；环境等级未知或非生产的历史任务不会出现在这里。
                      </p>
                    )}
                  {incidentLogs.isError && (
                    <p role='status' className='mt-1 text-xs text-destructive'>
                      部署记录加载失败，请稍后重试。
                    </p>
                  )}
                </div>
                <div className='min-w-0'>
                  <Label
                    htmlFor='incident-occurred-at'
                    className='mb-1.5 block text-xs'
                  >
                    事件发生时间
                  </Label>
                  <Input
                    id='incident-occurred-at'
                    type='datetime-local'
                    required
                    className='min-h-11 bg-background'
                    value={incidentOccurredAt}
                    onChange={(event) =>
                      setIncidentOccurredAt(event.target.value)
                    }
                  />
                </div>
                <div className='min-w-0 sm:col-span-2 xl:col-span-3'>
                  <Label
                    htmlFor='incident-summary'
                    className='mb-1.5 block text-xs'
                  >
                    现象摘要
                  </Label>
                  <Input
                    id='incident-summary'
                    required
                    maxLength={1000}
                    className='min-h-11 bg-background'
                    placeholder='例如：支付请求错误率超过告警阈值，影响生产下单'
                    value={incidentSummary}
                    onChange={(event) => setIncidentSummary(event.target.value)}
                  />
                </div>
              </div>
              {createIncident.isError && (
                <p role='alert' className='mt-3 text-sm text-destructive'>
                  事件登记失败。请确认所选部署属于有生产环境快照的记录，然后重试。
                </p>
              )}
              <div className='mt-4 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                <p className='text-xs leading-5 text-muted-foreground'>
                  登记记录会保留操作人、发生时间、登记时间和关联部署。
                </p>
                <Button
                  type='submit'
                  className='min-h-11 w-full sm:w-auto'
                  disabled={
                    createIncident.isPending ||
                    !incidentDeployLogId ||
                    !incidentSummary.trim() ||
                    !incidentOccurredAt
                  }
                >
                  {createIncident.isPending ? '正在登记…' : '登记线上事件'}
                </Button>
              </div>
            </form>
          )}

          {incidents.isError ? (
            <p
              role='alert'
              className='rounded-lg border border-destructive/25 bg-destructive/5 p-3 text-sm text-destructive'
            >
              生产事件台账加载失败，暂时无法确认线上故障和恢复状态。
            </p>
          ) : incidents.isPending ? (
            <div role='status' className='space-y-2 rounded-xl border p-4'>
              <Skeleton className='h-4 w-40' />
              <Skeleton className='h-4 w-64' />
            </div>
          ) : incidentRows.length === 0 ? (
            <div className='rounded-xl border border-dashed bg-muted/10 px-4 py-6 text-center'>
              <Clock3
                className='mx-auto size-5 text-muted-foreground'
                aria-hidden='true'
              />
              <p className='mt-2 text-sm font-medium'>还没有线上事件记录</p>
              <p className='mt-1 text-xs text-muted-foreground'>
                发生生产故障时，从上方选择对应部署并登记实际发生时间。
              </p>
            </div>
          ) : (
            <ol className='space-y-3'>
              {incidentRows.map((incident) => {
                const currentRemediationType =
                  incident.remediation_type ?? 'unknown'
                const selectedRemediationNeedsDeployment =
                  remediationType !== '' &&
                  usesRemediationDeployment(remediationType)
                const eligibleRemediationLogs = (
                  remediationLogs.data?.data ?? []
                ).filter(
                  (log) =>
                    log.environment_tier_snapshot === 'production' &&
                    log.status === 'success' &&
                    log.cluster_id === incident.cluster_id &&
                    log.app_name === incident.app_name &&
                    log.id !== incident.deploy_log_id &&
                    (remediationType !== 'rollback' ||
                      log.deploy_type === 'rollback')
                )
                return (
                  <li
                    key={incident.id}
                    className='rounded-xl border bg-background p-4 sm:p-5'
                  >
                    <div className='flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between'>
                      <div className='min-w-0'>
                        <div className='flex flex-wrap items-center gap-2'>
                          <h4 className='font-medium'>{incident.summary}</h4>
                          <Badge
                            variant={
                              incident.status === 'open'
                                ? 'destructive'
                                : 'outline'
                            }
                            className={
                              incident.status === 'recovered'
                                ? 'border-emerald-300 text-emerald-800 dark:border-emerald-900 dark:text-emerald-300'
                                : ''
                            }
                          >
                            {incident.status === 'open'
                              ? '待确认恢复'
                              : '已确认恢复'}
                          </Badge>
                          <Badge
                            variant='outline'
                            className={
                              incident.causality_status ===
                              'caused_by_deployment'
                                ? 'border-destructive/30 bg-destructive/5 text-destructive'
                                : incident.causality_status ===
                                    'not_caused_by_deployment'
                                  ? 'border-emerald-300 text-emerald-800 dark:border-emerald-900 dark:text-emerald-300'
                                  : ''
                            }
                          >
                            {incident.causality_status ===
                            'caused_by_deployment'
                              ? '人工判定：由部署导致'
                              : incident.causality_status ===
                                  'not_caused_by_deployment'
                                ? '人工判定：未认定为部署故障'
                                : '因果待判定'}
                          </Badge>
                          <Badge
                            variant='outline'
                            className={
                              currentRemediationType === 'unknown'
                                ? 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200'
                                : 'border-sky-300 text-sky-800 dark:border-sky-900 dark:text-sky-200'
                            }
                          >
                            {remediationTypeLabels[currentRemediationType]}
                            {incident.remediation_plan_status === 'planned'
                              ? ' · 计划内'
                              : incident.remediation_plan_status === 'unplanned'
                                ? ' · 计划外'
                                : ''}
                          </Badge>
                        </div>
                        <p className='mt-1.5 text-xs text-muted-foreground'>
                          {incident.app_name} · 部署 #{incident.deploy_log_id}
                          {incident.release_id
                            ? ` · 制品发布 #${incident.release_id}`
                            : ''}
                          {' · '}事件 #{incident.id}
                        </p>
                      </div>
                      {incident.status === 'open' &&
                        (role === 'operator' || role === 'admin') && (
                          <div className='flex shrink-0 flex-wrap gap-2'>
                            {!incident.intervention_started_at && (
                              <Button
                                type='button'
                                variant='outline'
                                className='min-h-11'
                                onClick={() => {
                                  setInterveningIncidentId(
                                    interveningIncidentId === incident.id
                                      ? null
                                      : incident.id
                                  )
                                  setInterventionOccurredAt(
                                    localDateTimeInput()
                                  )
                                  setInterventionDetail('')
                                }}
                              >
                                记录开始干预
                              </Button>
                            )}
                            <Button
                              type='button'
                              variant='outline'
                              className='min-h-11'
                              onClick={() => {
                                setRecoveringIncidentId(
                                  recoveringIncidentId === incident.id
                                    ? null
                                    : incident.id
                                )
                                setRecoveryOccurredAt(localDateTimeInput())
                                setRecoveryConfirmation('')
                              }}
                            >
                              <Check
                                className='mr-2 size-4'
                                aria-hidden='true'
                              />
                              确认业务恢复
                            </Button>
                          </div>
                        )}
                      {(role === 'operator' || role === 'admin') && (
                        <div className='flex shrink-0 flex-wrap gap-2'>
                          <Button
                            type='button'
                            variant='outline'
                            className='min-h-11'
                            onClick={() => {
                              const isOpen = causalityIncidentId === incident.id
                              setCausalityIncidentId(
                                isOpen ? null : incident.id
                              )
                              setCausalityStatus(
                                isOpen ||
                                  incident.causality_status === 'unknown'
                                  ? ''
                                  : incident.causality_status
                              )
                              setCausalityReason(
                                isOpen ? '' : (incident.causality_reason ?? '')
                              )
                            }}
                          >
                            {incident.causality_status === 'unknown'
                              ? '记录因果判定'
                              : '修正因果判定'}
                          </Button>
                          <Button
                            type='button'
                            variant='outline'
                            className='min-h-11'
                            onClick={() => {
                              const isOpen =
                                remediationIncidentId === incident.id
                              setRemediationIncidentId(
                                isOpen ? null : incident.id
                              )
                              setRemediationType(
                                isOpen || currentRemediationType === 'unknown'
                                  ? ''
                                  : currentRemediationType
                              )
                              setRemediationPlanStatus(
                                isOpen ||
                                  !incident.remediation_plan_status ||
                                  incident.remediation_plan_status === 'unknown'
                                  ? ''
                                  : incident.remediation_plan_status
                              )
                              setRemediationDeployLogID(
                                isOpen || !incident.remediation_deploy_log_id
                                  ? ''
                                  : String(incident.remediation_deploy_log_id)
                              )
                              setRemediationReason(
                                isOpen
                                  ? ''
                                  : (incident.remediation_reason ?? '')
                              )
                            }}
                          >
                            {currentRemediationType === 'unknown'
                              ? '记录修复处置'
                              : '修正修复处置'}
                          </Button>
                        </div>
                      )}
                    </div>
                    {incident.intervention_started_at &&
                      incident.recovery_occurred_at && (
                        <p className='mt-3 text-sm text-muted-foreground'>
                          干预到业务恢复：{' '}
                          <span className='font-medium text-foreground'>
                            {duration(
                              (new Date(
                                incident.recovery_occurred_at
                              ).getTime() -
                                new Date(
                                  incident.intervention_started_at
                                ).getTime()) /
                                1000
                            )}
                          </span>
                        </p>
                      )}
                    <ol className='mt-4 space-y-3 border-l pl-4 sm:ml-1'>
                      {incident.events.map((event) => (
                        <li key={event.id} className='relative min-w-0'>
                          <span
                            className='absolute top-1.5 -left-[21px] size-2 rounded-full bg-primary ring-4 ring-background'
                            aria-hidden='true'
                          />
                          <p className='text-xs font-medium'>
                            {event.event_type === 'recovery_confirmed'
                              ? '业务恢复已确认'
                              : event.event_type === 'intervention_started'
                                ? '开始故障干预'
                                : event.event_type === 'causality_assessed'
                                  ? event.causality_status ===
                                    'caused_by_deployment'
                                    ? '人工判定：由关联部署导致'
                                    : '人工判定：未认定由关联部署导致'
                                  : event.event_type ===
                                      'remediation_classified'
                                    ? `修复处置：${remediationTypeLabels[event.remediation_type ?? 'unknown']}`
                                    : '线上故障已登记'}
                            {event.event_type === 'remediation_classified' &&
                            event.remediation_plan_status &&
                            event.remediation_plan_status !==
                              'not_applicable' &&
                            event.remediation_plan_status !== 'unknown'
                              ? ` · ${event.remediation_plan_status === 'planned' ? '计划内' : '计划外'}`
                              : ''}
                            {event.remediation_deploy_log_id
                              ? ` · 修复部署 #${event.remediation_deploy_log_id}`
                              : ''}
                            {' · '}
                            {metricTimestamp(event.occurred_at)}
                          </p>
                          <p className='mt-1 text-sm leading-5 break-words text-muted-foreground'>
                            {event.detail}
                          </p>
                          <p className='mt-1 text-xs text-muted-foreground'>
                            {event.recorded_by} 登记 ·{' '}
                            {observedTime(event.recorded_at)}
                          </p>
                        </li>
                      ))}
                    </ol>
                    {causalityIncidentId === incident.id && (
                      <form
                        className='mt-4 rounded-lg border border-primary/20 bg-primary/[0.03] p-4'
                        onSubmit={(event) => {
                          event.preventDefault()
                          if (!causalityStatus) return
                          assessIncidentCausality.mutate({
                            id: incident.id,
                            version: incident.version,
                            status: causalityStatus,
                            reason: causalityReason.trim(),
                          })
                        }}
                      >
                        <div className='mb-3'>
                          <h5 className='text-sm font-semibold'>
                            判定关联部署与故障的关系
                          </h5>
                          <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                            这是操作员基于现场证据作出的判断，平台不会自动推断。每次修正都会追加到时间线。
                          </p>
                        </div>
                        <div className='grid gap-3 sm:grid-cols-2'>
                          <div>
                            <Label
                              htmlFor={`causality-status-${incident.id}`}
                              className='mb-1.5 block text-xs'
                            >
                              因果判定
                            </Label>
                            <Select
                              value={causalityStatus}
                              onValueChange={(value) =>
                                setCausalityStatus(
                                  value as
                                    | 'caused_by_deployment'
                                    | 'not_caused_by_deployment'
                                )
                              }
                            >
                              <SelectTrigger
                                id={`causality-status-${incident.id}`}
                                className='min-h-11 bg-background'
                              >
                                <SelectValue placeholder='选择判定结果' />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value='caused_by_deployment'>
                                  由关联部署导致
                                </SelectItem>
                                <SelectItem value='not_caused_by_deployment'>
                                  未认定由关联部署导致
                                </SelectItem>
                              </SelectContent>
                            </Select>
                          </div>
                          <div>
                            <Label
                              htmlFor={`causality-reason-${incident.id}`}
                              className='mb-1.5 block text-xs'
                            >
                              判定依据
                            </Label>
                            <Textarea
                              id={`causality-reason-${incident.id}`}
                              required
                              maxLength={1000}
                              className='min-h-11 resize-y bg-background'
                              placeholder='说明回滚对比、变更时间线或业务证据'
                              value={causalityReason}
                              onChange={(event) =>
                                setCausalityReason(event.target.value)
                              }
                            />
                          </div>
                        </div>
                        {assessIncidentCausality.isError && (
                          <p
                            role='alert'
                            className='mt-2 text-sm text-destructive'
                          >
                            判定未保存。事件可能已被其他人更新，请刷新后核对版本再提交。
                          </p>
                        )}
                        <div className='mt-3 flex flex-col gap-2 sm:flex-row sm:justify-end'>
                          <Button
                            type='button'
                            variant='ghost'
                            className='min-h-11'
                            onClick={() => setCausalityIncidentId(null)}
                          >
                            取消
                          </Button>
                          <Button
                            type='submit'
                            className='min-h-11'
                            disabled={
                              assessIncidentCausality.isPending ||
                              !causalityStatus ||
                              !causalityReason.trim()
                            }
                          >
                            {assessIncidentCausality.isPending
                              ? '正在保存…'
                              : '保存因果判定'}
                          </Button>
                        </div>
                      </form>
                    )}
                    {remediationIncidentId === incident.id && (
                      <form
                        className='mt-4 rounded-lg border border-sky-300/50 bg-sky-50/40 p-4 dark:border-sky-900 dark:bg-sky-950/10'
                        onSubmit={(event) => {
                          event.preventDefault()
                          if (!remediationType || !remediationPlanStatus) return
                          const needsDeployment =
                            usesRemediationDeployment(remediationType)
                          recordIncidentRemediation.mutate({
                            id: incident.id,
                            version: incident.version,
                            type: remediationType,
                            planStatus: remediationPlanStatus,
                            ...(needsDeployment
                              ? { deployLogID: Number(remediationDeployLogID) }
                              : {}),
                            reason: remediationReason.trim(),
                          })
                        }}
                      >
                        <div className='mb-3'>
                          <h5 className='text-sm font-semibold'>
                            记录故障修复处置
                          </h5>
                          <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                            选择实际采取的措施。部署类修复会关联同一生产应用的成功部署记录；每次修正都会保留在事件时间线。
                          </p>
                        </div>
                        <div className='grid gap-3 sm:grid-cols-2'>
                          <div>
                            <Label
                              htmlFor={`remediation-type-${incident.id}`}
                              className='mb-1.5 block text-xs'
                            >
                              处置方式
                            </Label>
                            <Select
                              value={remediationType}
                              onValueChange={(value) => {
                                const nextType = value as Exclude<
                                  DeliveryRemediationType,
                                  'unknown'
                                >
                                setRemediationType(nextType)
                                if (usesRemediationDeployment(nextType)) {
                                  if (
                                    remediationPlanStatus !== 'planned' &&
                                    remediationPlanStatus !== 'unplanned'
                                  ) {
                                    setRemediationPlanStatus('')
                                  }
                                } else {
                                  setRemediationPlanStatus('not_applicable')
                                  setRemediationDeployLogID('')
                                }
                              }}
                            >
                              <SelectTrigger
                                id={`remediation-type-${incident.id}`}
                                className='min-h-11 bg-background'
                              >
                                <SelectValue placeholder='选择处置方式' />
                              </SelectTrigger>
                              <SelectContent>
                                <SelectItem value='rollback'>回滚</SelectItem>
                                <SelectItem value='hotfix_deployment'>
                                  热修复部署
                                </SelectItem>
                                <SelectItem value='configuration_deployment'>
                                  配置修复部署
                                </SelectItem>
                                <SelectItem value='restart_or_scale'>
                                  重启 / 扩缩容
                                </SelectItem>
                                <SelectItem value='no_action'>
                                  无需处置
                                </SelectItem>
                              </SelectContent>
                            </Select>
                          </div>
                          {selectedRemediationNeedsDeployment ? (
                            <div>
                              <Label
                                htmlFor={`remediation-deploy-${incident.id}`}
                                className='mb-1.5 block text-xs'
                              >
                                对应修复部署
                              </Label>
                              <Select
                                value={remediationDeployLogID}
                                onValueChange={setRemediationDeployLogID}
                                disabled={remediationLogs.isPending}
                              >
                                <SelectTrigger
                                  id={`remediation-deploy-${incident.id}`}
                                  className='min-h-11 bg-background'
                                >
                                  <SelectValue
                                    placeholder={
                                      remediationLogs.isPending
                                        ? '正在加载部署记录…'
                                        : '选择成功的生产部署'
                                    }
                                  />
                                </SelectTrigger>
                                <SelectContent>
                                  {eligibleRemediationLogs.map((log) => (
                                    <SelectItem
                                      key={log.id}
                                      value={String(log.id)}
                                    >
                                      #{log.id} · {log.deploy_type} ·{' '}
                                      {log.status}
                                    </SelectItem>
                                  ))}
                                </SelectContent>
                              </Select>
                              {remediationLogs.isError && (
                                <p className='mt-1 text-xs text-destructive'>
                                  无法读取该应用的生产部署记录，请稍后重试。
                                </p>
                              )}
                              {!remediationLogs.isPending &&
                                !remediationLogs.isError &&
                                eligibleRemediationLogs.length === 0 && (
                                  <p className='mt-1 text-xs text-muted-foreground'>
                                    暂无可关联的成功生产部署记录。
                                  </p>
                                )}
                            </div>
                          ) : (
                            <div>
                              <Label className='mb-1.5 block text-xs'>
                                计划状态
                              </Label>
                              <div className='flex min-h-11 items-center rounded-md border bg-muted/30 px-3 text-sm text-muted-foreground'>
                                不适用（非部署类处置）
                              </div>
                            </div>
                          )}
                          {selectedRemediationNeedsDeployment && (
                            <div>
                              <Label
                                htmlFor={`remediation-plan-${incident.id}`}
                                className='mb-1.5 block text-xs'
                              >
                                计划状态
                              </Label>
                              <Select
                                value={remediationPlanStatus}
                                onValueChange={(value) =>
                                  setRemediationPlanStatus(
                                    value as 'planned' | 'unplanned'
                                  )
                                }
                              >
                                <SelectTrigger
                                  id={`remediation-plan-${incident.id}`}
                                  className='min-h-11 bg-background'
                                >
                                  <SelectValue placeholder='选择是否计划内' />
                                </SelectTrigger>
                                <SelectContent>
                                  <SelectItem value='planned'>
                                    计划内
                                  </SelectItem>
                                  <SelectItem value='unplanned'>
                                    计划外
                                  </SelectItem>
                                </SelectContent>
                              </Select>
                            </div>
                          )}
                          <div className='sm:col-span-2'>
                            <Label
                              htmlFor={`remediation-reason-${incident.id}`}
                              className='mb-1.5 block text-xs'
                            >
                              处置依据
                            </Label>
                            <Textarea
                              id={`remediation-reason-${incident.id}`}
                              required
                              maxLength={1000}
                              className='min-h-11 resize-y bg-background'
                              placeholder='说明修复内容、验证结果，或无需处置的依据'
                              value={remediationReason}
                              onChange={(event) =>
                                setRemediationReason(event.target.value)
                              }
                            />
                          </div>
                        </div>
                        {recordIncidentRemediation.isError && (
                          <p
                            role='alert'
                            className='mt-2 text-sm text-destructive'
                          >
                            处置未保存。事件可能已更新，或所选部署与当前故障目标不匹配；请刷新后核对。
                          </p>
                        )}
                        <div className='mt-3 flex flex-col gap-2 sm:flex-row sm:justify-end'>
                          <Button
                            type='button'
                            variant='ghost'
                            className='min-h-11'
                            onClick={() => setRemediationIncidentId(null)}
                          >
                            取消
                          </Button>
                          <Button
                            type='submit'
                            className='min-h-11'
                            disabled={
                              recordIncidentRemediation.isPending ||
                              !remediationType ||
                              !remediationPlanStatus ||
                              (selectedRemediationNeedsDeployment &&
                                (!remediationDeployLogID ||
                                  !eligibleRemediationLogs.some(
                                    (log) =>
                                      log.id === Number(remediationDeployLogID)
                                  ))) ||
                              !remediationReason.trim()
                            }
                          >
                            {recordIncidentRemediation.isPending
                              ? '正在保存…'
                              : '保存处置记录'}
                          </Button>
                        </div>
                      </form>
                    )}
                    {interveningIncidentId === incident.id && (
                      <form
                        className='mt-4 rounded-lg border bg-muted/20 p-4'
                        onSubmit={(event) => {
                          event.preventDefault()
                          startIncidentIntervention.mutate({
                            id: incident.id,
                            version: incident.version,
                          })
                        }}
                      >
                        <div className='grid gap-3 sm:grid-cols-2'>
                          <div>
                            <Label
                              htmlFor={`intervention-time-${incident.id}`}
                              className='mb-1.5 block text-xs'
                            >
                              开始干预时间
                            </Label>
                            <Input
                              id={`intervention-time-${incident.id}`}
                              type='datetime-local'
                              required
                              className='min-h-11 bg-background'
                              value={interventionOccurredAt}
                              onChange={(event) =>
                                setInterventionOccurredAt(event.target.value)
                              }
                            />
                          </div>
                          <div>
                            <Label
                              htmlFor={`intervention-detail-${incident.id}`}
                              className='mb-1.5 block text-xs'
                            >
                              已采取的干预措施
                            </Label>
                            <Textarea
                              id={`intervention-detail-${incident.id}`}
                              required
                              maxLength={1000}
                              className='min-h-11 resize-y bg-background'
                              placeholder='例如：开始回退上一稳定版本并通知值班负责人'
                              value={interventionDetail}
                              onChange={(event) =>
                                setInterventionDetail(event.target.value)
                              }
                            />
                          </div>
                        </div>
                        {startIncidentIntervention.isError && (
                          <p
                            role='alert'
                            className='mt-2 text-sm text-destructive'
                          >
                            干预记录未保存。请核对事件版本和时间后重试。
                          </p>
                        )}
                        <div className='mt-3 flex flex-col gap-2 sm:flex-row sm:justify-end'>
                          <Button
                            type='button'
                            variant='ghost'
                            className='min-h-11'
                            onClick={() => setInterveningIncidentId(null)}
                          >
                            取消
                          </Button>
                          <Button
                            type='submit'
                            className='min-h-11'
                            disabled={
                              startIncidentIntervention.isPending ||
                              !interventionOccurredAt ||
                              !interventionDetail.trim()
                            }
                          >
                            {startIncidentIntervention.isPending
                              ? '正在记录…'
                              : '保存干预开始'}
                          </Button>
                        </div>
                      </form>
                    )}
                    {recoveringIncidentId === incident.id && (
                      <form
                        className='mt-4 rounded-lg border bg-muted/20 p-4'
                        onSubmit={(event) => {
                          event.preventDefault()
                          confirmIncidentRecovery.mutate({
                            id: incident.id,
                            version: incident.version,
                          })
                        }}
                      >
                        <div className='grid gap-3 sm:grid-cols-2'>
                          <div>
                            <Label
                              htmlFor={`recovery-time-${incident.id}`}
                              className='mb-1.5 block text-xs'
                            >
                              业务恢复时间
                            </Label>
                            <Input
                              id={`recovery-time-${incident.id}`}
                              type='datetime-local'
                              required
                              className='min-h-11 bg-background'
                              value={recoveryOccurredAt}
                              onChange={(event) =>
                                setRecoveryOccurredAt(event.target.value)
                              }
                            />
                          </div>
                          <div>
                            <Label
                              htmlFor={`recovery-confirmation-${incident.id}`}
                              className='mb-1.5 block text-xs'
                            >
                              业务验证依据
                            </Label>
                            <Textarea
                              id={`recovery-confirmation-${incident.id}`}
                              required
                              maxLength={1000}
                              className='min-h-11 resize-y bg-background'
                              placeholder='例如：核心交易探针恢复，人工完成一笔验证交易'
                              value={recoveryConfirmation}
                              onChange={(event) =>
                                setRecoveryConfirmation(event.target.value)
                              }
                            />
                          </div>
                        </div>
                        {confirmIncidentRecovery.isError && (
                          <p
                            role='alert'
                            className='mt-2 text-sm text-destructive'
                          >
                            恢复确认未保存。请刷新事件状态后重试。
                          </p>
                        )}
                        <div className='mt-3 flex flex-col gap-2 sm:flex-row sm:justify-end'>
                          <Button
                            type='button'
                            variant='ghost'
                            className='min-h-11'
                            onClick={() => setRecoveringIncidentId(null)}
                          >
                            取消
                          </Button>
                          <Button
                            type='submit'
                            className='min-h-11'
                            disabled={
                              confirmIncidentRecovery.isPending ||
                              !recoveryOccurredAt ||
                              !recoveryConfirmation.trim()
                            }
                          >
                            {confirmIncidentRecovery.isPending
                              ? '正在确认…'
                              : '保存恢复确认'}
                          </Button>
                        </div>
                      </form>
                    )}
                  </li>
                )
              })}
            </ol>
          )}
        </section>

        <section
          aria-labelledby='delivery-drift-title'
          className='border-t pt-5'
        >
          <div className='flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between'>
            <div className='min-w-0'>
              <h3 id='delivery-drift-title' className='font-semibold'>
                镜像漂移观测
              </h3>
              <p className='mt-1 max-w-2xl text-sm leading-5 text-muted-foreground'>
                对比实际镜像与平台最近成功部署，只检查镜像，不自动修复集群。
              </p>
            </div>
            <div className='w-full sm:max-w-sm'>
              <Label htmlFor='delivery-drift-cluster' className='sr-only'>
                选择集群查看实际状态
              </Label>
              <Select value={clusterId} onValueChange={setClusterId}>
                <SelectTrigger
                  id='delivery-drift-cluster'
                  className='min-h-11 w-full bg-background'
                >
                  <SelectValue
                    className='text-slate-900 dark:text-slate-100'
                    placeholder='选择集群查看实际状态'
                  />
                </SelectTrigger>
                <SelectContent>
                  {(clusters.data?.data ?? [])
                    .filter((cluster) => cluster.enabled)
                    .map((cluster) => (
                      <SelectItem key={cluster.id} value={String(cluster.id)}>
                        {cluster.name}
                      </SelectItem>
                    ))}
                </SelectContent>
              </Select>
            </div>
          </div>

          {clusters.isError && (
            <p role='alert' className='mt-3 text-sm text-destructive'>
              集群列表加载失败，暂时无法选择观测目标。
            </p>
          )}
          {clusterId && drift.isPending && (
            <div
              role='status'
              aria-label='正在读取集群镜像状态'
              className='mt-4 grid gap-3 sm:grid-cols-2'
            >
              <Skeleton className='h-28 rounded-xl' />
              <Skeleton className='h-28 rounded-xl' />
            </div>
          )}
          {drift.isError && (
            <div
              role='alert'
              className='mt-4 flex items-start gap-2 rounded-lg border border-destructive/25 bg-destructive/5 p-3 text-sm text-destructive'
            >
              <AlertCircle
                className='mt-0.5 size-4 shrink-0'
                aria-hidden='true'
              />
              集群观测失败，实际镜像状态未知。请检查连接和读取权限后重试。
            </div>
          )}
          {drift.data?.data.length === 0 && (
            <p className='mt-4 rounded-lg border border-dashed p-4 text-sm text-muted-foreground'>
              该集群暂无已绑定组件。
            </p>
          )}
          {(drift.data?.data.length ?? 0) > 0 && (
            <ul className='mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
              {drift.data?.data.map((observation) => (
                <li
                  key={`${observation.cluster_id}/${observation.namespace}/${observation.workload}/${observation.container}`}
                  className='min-w-0 rounded-xl border bg-background p-4'
                >
                  <div className='flex min-w-0 flex-wrap items-start justify-between gap-2'>
                    <div className='min-w-0'>
                      <h4
                        className='truncate font-medium'
                        title={observation.app_name}
                      >
                        {observation.app_name}
                      </h4>
                      <p className='mt-0.5 text-xs break-all text-muted-foreground'>
                        {observation.namespace} / {observation.workload} /{' '}
                        {observation.container}
                      </p>
                    </div>
                    <Badge
                      variant='outline'
                      className={`shrink-0 ${driftTone[observation.status]}`}
                    >
                      {driftLabel[observation.status]}
                    </Badge>
                  </div>
                  <dl className='mt-4 space-y-3 border-t pt-3 text-xs'>
                    <div className='min-w-0'>
                      <dt className='text-muted-foreground'>期望镜像</dt>
                      <dd className='mt-1 font-mono leading-relaxed break-all'>
                        {observation.expected_image ?? '无成功部署基线'}
                      </dd>
                    </div>
                    <div className='min-w-0'>
                      <dt className='text-muted-foreground'>实际镜像</dt>
                      <dd className='mt-1 font-mono leading-relaxed break-all'>
                        {observation.actual_image ?? '无法读取'}
                      </dd>
                    </div>
                    {observation.reason && (
                      <div className='break-words text-muted-foreground'>
                        {observation.reason}
                      </div>
                    )}
                  </dl>
                  <dl className='mt-3 grid grid-cols-2 gap-3 border-t pt-3 text-xs'>
                    <div className='min-w-0'>
                      <dt className='text-muted-foreground'>
                        最近核对 · {driftSourceLabel(observation.source)}
                      </dt>
                      <dd className='mt-1 tabular-nums'>
                        {observedTime(
                          observation.last_observed_at ??
                            observation.observed_at
                        )}
                      </dd>
                    </div>
                    <div className='min-w-0'>
                      <dt className='text-muted-foreground'>
                        {observation.status === 'drifted' &&
                        observation.drift_started_at
                          ? '持续漂移（截至最近核对）'
                          : '累计观察'}
                      </dt>
                      <dd className='mt-1 tabular-nums'>
                        {observation.status === 'drifted' &&
                        observation.drift_started_at
                          ? duration(
                              Math.max(
                                0,
                                (new Date(
                                  observation.last_observed_at ??
                                    observation.observed_at ??
                                    observation.drift_started_at
                                ).getTime() -
                                  new Date(
                                    observation.drift_started_at
                                  ).getTime()) /
                                  1000
                              )
                            )
                          : `${observation.observation_count ?? 1} 次`}
                      </dd>
                    </div>
                  </dl>
                </li>
              ))}
            </ul>
          )}
          {clusterId && (
            <section
              aria-labelledby='delivery-drift-history-title'
              className='mt-5 border-t pt-5'
            >
              <div className='mb-3 flex flex-wrap items-center justify-between gap-2'>
                <div>
                  <h4
                    id='delivery-drift-history-title'
                    className='font-semibold'
                  >
                    状态变化记录
                  </h4>
                  <p className='mt-1 text-xs text-muted-foreground'>
                    只记录首次状态、镜像、基线或读取结果变化；重复核对只更新观察次数。
                  </p>
                </div>
                <span
                  id='delivery-drift-history-count'
                  className='inline-flex items-center rounded-md border bg-background px-2 py-0.5 text-xs font-medium text-slate-900 dark:text-slate-100'
                >
                  {(driftHistory.data?.data ?? []).length} 条 · 最近 25 条
                </span>
              </div>
              {driftHistory.isError && (
                <p
                  role='alert'
                  className='rounded-lg border border-destructive/25 bg-destructive/5 p-3 text-sm text-destructive'
                >
                  漂移历史加载失败；当前镜像观测仍可查看。
                </p>
              )}
              {driftHistory.isPending && (
                <div
                  role='status'
                  aria-label='正在加载漂移历史'
                  className='space-y-2'
                >
                  <Skeleton className='h-16 rounded-lg' />
                  <Skeleton className='h-16 rounded-lg' />
                </div>
              )}
              {!driftHistory.isPending &&
                !driftHistory.isError &&
                (driftHistory.data?.data.length ?? 0) === 0 && (
                  <p className='rounded-lg border border-dashed p-4 text-sm text-muted-foreground'>
                    还没有状态变化记录。页面按需读取集群，重复读取相同状态不会新增事件。
                  </p>
                )}
              {(driftHistory.data?.data.length ?? 0) > 0 && (
                <ol className='space-y-2'>
                  {driftHistory.data?.data.map((event) => (
                    <li
                      key={event.id}
                      className='min-w-0 rounded-lg border bg-background p-3'
                    >
                      <div className='flex flex-wrap items-center justify-between gap-2'>
                        <div className='flex min-w-0 flex-wrap items-center gap-2'>
                          <span className='truncate text-sm font-medium'>
                            {event.app_name}
                          </span>
                          <Badge
                            variant='outline'
                            className={`shrink-0 ${driftTone[event.status]}`}
                          >
                            {driftLabel[event.status]}
                          </Badge>
                          <span className='text-xs text-muted-foreground'>
                            {driftChangeLabel[event.change_kind]} ·{' '}
                            {driftSourceLabel(event.source)}
                          </span>
                        </div>
                        <time
                          className='shrink-0 text-xs text-muted-foreground tabular-nums'
                          dateTime={event.observed_at}
                        >
                          {observedTime(event.observed_at)}
                        </time>
                      </div>
                      <p className='mt-1 text-xs text-muted-foreground'>
                        {event.namespace} / {event.workload} / {event.container}
                        {event.previous_status
                          ? ` · ${driftLabel[event.previous_status]} → ${driftLabel[event.status]}`
                          : ''}
                        {event.drift_duration_seconds != null
                          ? ` · 漂移持续 ${duration(event.drift_duration_seconds)}`
                          : ''}
                      </p>
                      {event.reason && (
                        <p className='mt-1 text-xs break-words text-muted-foreground'>
                          {event.reason}
                        </p>
                      )}
                      {event.status === 'drifted' && event.actual_image && (
                        <p className='mt-2 font-mono text-xs leading-relaxed break-all'>
                          实际镜像：{event.actual_image}
                        </p>
                      )}
                    </li>
                  ))}
                </ol>
              )}
            </section>
          )}
          {!clusterId && !clusters.isError && (
            <p className='mt-4 rounded-lg border border-dashed p-4 text-sm text-muted-foreground'>
              选择集群后查看每个已绑定组件的期望镜像与实际镜像。
            </p>
          )}
        </section>

        {role === 'admin' && (
          <section
            aria-labelledby='delivery-notifications-title'
            className='border-t pt-5'
          >
            <div className='flex flex-wrap items-center justify-between gap-3'>
              <div className='flex items-center gap-2'>
                <BellRing
                  className='size-4 text-muted-foreground'
                  aria-hidden='true'
                />
                <h3 id='delivery-notifications-title' className='font-semibold'>
                  通知投递
                </h3>
              </div>
              <Badge
                variant='outline'
                className={
                  pendingNotifications.length > 0
                    ? 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-100'
                    : 'bg-background'
                }
              >
                {pendingNotifications.length}
                {notifications.hasNextPage ? '+' : ''} 条待处理
              </Badge>
            </div>
            <p className='mt-1 text-sm text-muted-foreground'>
              未配置通知地址时不会生成投递任务。
            </p>
            {notifications.isError && (
              <p role='alert' className='mt-3 text-sm text-destructive'>
                通知状态加载失败。
              </p>
            )}
            {notifications.isPending ? (
              <Skeleton className='mt-3 h-16 rounded-lg' />
            ) : pendingNotifications.length > 0 ? (
              <ul className='mt-3 space-y-2'>
                {pendingNotifications.map((notification) => (
                  <li
                    key={notification.id}
                    className='min-w-0 rounded-lg border bg-background p-3'
                  >
                    <div className='flex flex-wrap items-center justify-between gap-2 text-sm'>
                      <span className='min-w-0 font-medium break-all'>
                        {notificationEventLabel[notification.event] ??
                          notification.event}
                      </span>
                      <Badge
                        variant='outline'
                        className={
                          notification.status === 'dead'
                            ? 'border-destructive/30 bg-destructive/5 text-destructive'
                            : 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-100'
                        }
                      >
                        {notification.status === 'dead'
                          ? '重试已耗尽'
                          : '等待投递'}
                      </Badge>
                    </div>
                    <p className='mt-1 text-xs break-words text-muted-foreground'>
                      {notification.summary ? `${notification.summary} · ` : ''}
                      通知 #{notification.id} · 已尝试 {notification.attempts}{' '}
                      次
                    </p>
                    {notification.last_error && (
                      <p className='mt-2 rounded-md bg-muted p-2 text-xs break-words text-muted-foreground'>
                        {notification.last_error}
                      </p>
                    )}
                  </li>
                ))}
              </ul>
            ) : !notifications.isError ? (
              <p className='mt-3 rounded-lg border border-dashed p-4 text-sm text-muted-foreground'>
                暂无失败或待投递通知。
              </p>
            ) : null}
            {notifications.hasNextPage && (
              <Button
                className='mt-3 w-full sm:w-auto'
                variant='outline'
                size='sm'
                disabled={notifications.isFetchingNextPage}
                onClick={() => void notifications.fetchNextPage()}
              >
                {notifications.isFetchingNextPage
                  ? '正在加载通知…'
                  : '加载更早通知'}
              </Button>
            )}
          </section>
        )}
      </CardContent>
    </Card>
  )
}
