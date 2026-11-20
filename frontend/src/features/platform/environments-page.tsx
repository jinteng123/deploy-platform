import { useMemo, useState } from 'react'
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'
import {
  ArrowRight,
  ArrowUpRight,
  CircleCheck,
  CircleHelp,
  CircleX,
  Check,
  Copy,
  GitBranch,
  Layers3,
  Plus,
  RefreshCw,
  Route,
  Server,
  Workflow,
  X,
} from 'lucide-react'
import { toast } from 'sonner'
import {
  platformApi,
  runnerApi,
  type ComponentBinding,
  type CanaryAnalysisPolicy,
  type DeliveryBinding,
  type DeliveryPipelineScopeReview,
  type DeliveryModel,
  type DeliveryEnvironment,
  type DeployLog,
  type DriftObservation,
  type ImageReleaseView,
  type VerificationEvidence,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ThemeSwitch } from '@/components/theme-switch'
import { componentSourceLabel } from './component-binding'
import {
  evidenceForCurrentImage,
  evidenceStatus,
  safeReportURL,
} from './environment-evidence'
import { EnvironmentEvidencePolicyDialog } from './environment-evidence-policy-dialog'
import { ReleaseStrategyJourney } from './release-strategy-journey'
import { StrategyCapabilityPreview } from './strategy-capability-preview'

const selectClass =
  'h-10 w-full min-w-0 rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-2 focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-50'
const EMPTY_COMPONENT_BINDINGS: ComponentBinding[] = []
const pipelineScopeCategoryLabels: Record<string, string> = {
  possible_config_write: '配置资源可能写入',
  possible_workload_write: '工作负载可能写入',
  kubernetes_resource_write: 'Kubernetes 资源可能写入',
  helm_release_write: 'Helm 资源变更',
  external_infrastructure_write: '外部基础设施变更',
  opaque_script: '自定义脚本，范围未知',
  opaque_task: '云效任务模板，范围未知',
}
const gatewayIntegrationRequest = [
  '我们正在评估精确金丝雀接入，请提供以下脱敏资料：',
  '1. 网关产品名称/版本、平台可访问的内网地址、认证方式和所需最小权限。',
  '2. 如何读取 route/backend 身份和当前权重；如何以并发保护或幂等方式设置稳定/候选权重，以及如何回读已生效结果。',
  '3. 如何按稳定版/候选版分别读取请求数、错误率和 P95 延迟；请说明观察窗口、数据延迟与最小样本量。',
  '4. 如何恢复稳定版 100% / 候选版 0%，确认恢复生效，并在请求超时后安全回读和重试。',
  '如有接口文档、脱敏请求/响应样例或故障恢复说明，请一并提供。请勿发送 Token、密码或私钥。',
].join('\n')

function configurationPipelineScopeRequest(pipelineID: string) {
  return [
    `请协助核对云效配置流水线（Pipeline ID：${pipelineID}）的实际写入范围，便于完成平台接入：`,
    '1. 请提供当前完整流水线定义和实际执行路径，包含引用的任务/模板、插件、子流水线、远程脚本及动态变量展开结果。',
    '2. 请逐项列出实际会创建、修改或删除的资源类型、名称、集群和 namespace，包括 ConfigMap、Secret、Deployment、镜像、Service 及其它 Kubernetes 或外部资源；注明是否影响当前绑定目标之外的资源。',
    '3. 请说明任务实际使用的云效连接、Runner 或 Kubernetes 身份，以及该身份的有效权限范围。',
    '4. 请明确配置流水线和平台发布 Worker 各自负责哪些资源，如何避免并发覆盖，以及如何核实执行结果和恢复失败变更。',
    '请附脱敏后的文档、执行记录或配置样例；不要发送 Token、密码、私钥或 Secret 值。',
  ].join('\n')
}

async function copyPipelineScopeFingerprint(hash: string) {
  try {
    await navigator.clipboard.writeText(hash)
    toast.success('已复制流水线定义指纹')
  } catch {
    toast.error('无法访问剪贴板，请手动选中并复制定义指纹')
  }
}

async function copyGatewayIntegrationRequest() {
  try {
    await navigator.clipboard.writeText(gatewayIntegrationRequest)
    toast.success('已复制网关联调资料请求')
  } catch {
    toast.error('复制失败，请检查浏览器剪贴板权限后重试')
  }
}

async function copyConfigurationPipelineScopeRequest(pipelineID: string) {
  try {
    await navigator.clipboard.writeText(
      configurationPipelineScopeRequest(pipelineID)
    )
    toast.success('已复制配置流水线核对请求')
  } catch {
    toast.error('复制失败，请检查浏览器剪贴板权限后重试')
  }
}

type CanaryThresholdKey = Exclude<
  keyof CanaryAnalysisPolicy,
  'stages' | 'gateway_route'
>
type CanaryGatewayRouteField =
  | 'connection_id'
  | 'route_id'
  | 'stable_backend_id'
  | 'candidate_backend_id'
type CanaryGatewayRouteForm = Record<CanaryGatewayRouteField, string>
type CanaryStageDraft = {
  id: string
  candidateWeight: string
  observationSeconds: string
}
type CanaryThresholdField = {
  key: CanaryThresholdKey
  label: string
  unit: string
  min: number
  max?: number
  step: number
  scale: number
  placeholder: string
}

function newCanaryStageDraft(): CanaryStageDraft {
  return {
    id: crypto.randomUUID(),
    candidateWeight: '',
    observationSeconds: '',
  }
}

const canaryThresholdGroups: {
  title: string
  fields: CanaryThresholdField[]
}[] = [
  {
    title: '样本与新鲜度',
    fields: [
      {
        key: 'min_requests',
        label: '每版本最少请求数',
        unit: '条',
        min: 1,
        step: 1,
        scale: 1,
        placeholder: '例如 1000',
      },
      {
        key: 'max_observation_age_seconds',
        label: '观测最大年龄',
        unit: '秒',
        min: 1,
        max: 86400,
        step: 1,
        scale: 1,
        placeholder: '例如 120',
      },
    ],
  },
  {
    title: '错误率',
    fields: [
      {
        key: 'max_candidate_error_rate',
        label: '候选错误率上限',
        unit: '%',
        min: 0,
        max: 100,
        step: 0.1,
        scale: 100,
        placeholder: '例如 2',
      },
      {
        key: 'max_error_rate_increase',
        label: '错误率较稳定版最多增加',
        unit: '百分点',
        min: 0,
        max: 100,
        step: 0.1,
        scale: 100,
        placeholder: '例如 1',
      },
    ],
  },
  {
    title: '延迟',
    fields: [
      {
        key: 'max_candidate_p95_latency_ms',
        label: '候选 P95 延迟上限',
        unit: '毫秒',
        min: 0,
        step: 1,
        scale: 1,
        placeholder: '例如 250',
      },
      {
        key: 'max_p95_latency_increase_ratio',
        label: 'P95 较稳定版最多增加',
        unit: '%',
        min: 0,
        step: 1,
        scale: 100,
        placeholder: '例如 20',
      },
    ],
  },
]

function errorMessage(error: unknown, fallback = '保存失败，请稍后重试。') {
  return (
    (error as { response?: { data?: { detail?: string } } })?.response?.data
      ?.detail ?? fallback
  )
}

function originLabel(id?: number) {
  return id ? `旧绑定 #${id}` : '新建'
}

function compactImage(image: string) {
  const digest = image.match(/@sha256:([a-f\d]{12})[a-f\d]*/i)
  if (digest) return image.replace(/@sha256:[a-f\d]+/i, `@sha256:${digest[1]}…`)
  return image.length > 48 ? `…${image.slice(-45)}` : image
}

function localDateTime(value?: string | null) {
  if (!value) return '时间未知'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '时间未知'
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'short',
    timeStyle: 'short',
  }).format(date)
}

function unixMillisecondsDateTime(value?: number | null) {
  if (!value) return '时间未知'
  const date = new Date(value)
  return Number.isNaN(date.getTime())
    ? '时间未知'
    : localDateTime(date.toISOString())
}

function deployStatusLabel(status?: string) {
  switch (status) {
    case 'pending':
      return '排队中'
    case 'running':
      return '发布中'
    case 'success':
      return '成功'
    case 'failed':
      return '失败'
    case 'interrupted':
      return '中断待核对'
    default:
      return '状态未知'
  }
}

function evidenceKindLabel(kind: VerificationEvidence['kind']) {
  switch (kind) {
    case 'ci':
      return 'CI 验证'
    case 'smoke':
      return '冒烟测试'
    case 'manual':
      return '人工验收'
    case 'scan':
      return '安全扫描'
  }
}

function EvidenceBadge({
  label,
  evidence,
  actualImage,
  pending,
  error,
}: {
  label: string
  evidence?: VerificationEvidence
  actualImage?: string | null
  pending: boolean
  error: boolean
}) {
  const status = evidenceStatus(evidence, actualImage, pending, error)
  const Icon =
    status.tone === 'success'
      ? CircleCheck
      : status.tone === 'danger'
        ? CircleX
        : CircleHelp
  const toneClass =
    status.tone === 'success'
      ? 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950 dark:text-emerald-200'
      : status.tone === 'danger'
        ? 'border-destructive/30 bg-destructive/5 text-destructive'
        : status.tone === 'warning'
          ? 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200'
          : 'border-border bg-muted text-muted-foreground'

  return (
    <Badge variant='outline' className={toneClass}>
      <Icon aria-hidden='true' />
      {label} · {status.label}
    </Badge>
  )
}

function TargetRuntimeSummary({
  app,
  observation,
  observationPending,
  observationError,
  latestDeploy,
  latestDeployPending,
  latestDeployError,
  releases,
  releasesPending,
  releasesError,
}: {
  app: ComponentBinding
  observation?: DriftObservation
  observationPending: boolean
  observationError: boolean
  latestDeploy?: DeployLog | null
  latestDeployPending: boolean
  latestDeployError: boolean
  releases?: ImageReleaseView[]
  releasesPending: boolean
  releasesError: boolean
}) {
  const status = observation?.status ?? 'unknown'
  const statusLabel =
    status === 'in_sync'
      ? '镜像一致'
      : status === 'drifted'
        ? '实际镜像已变化'
        : '状态未知'
  const statusClass =
    status === 'in_sync'
      ? 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950 dark:text-emerald-200'
      : status === 'drifted'
        ? 'border-destructive/30 bg-destructive/5 text-destructive'
        : 'border-border bg-muted text-muted-foreground'
  const latestDeployLabel = latestDeployPending
    ? '正在读取任务'
    : latestDeployError
      ? '任务状态未知'
      : latestDeploy
        ? `${deployStatusLabel(latestDeploy.status)} · ${latestDeploy.version}`
        : '暂无发布记录'
  const observationReason = observationError
    ? '集群读取失败，无法确认实际镜像。'
    : observation?.reason
  const verification = evidenceForCurrentImage(
    releases,
    app,
    observation?.actual_image,
    'verification'
  )
  const scan = evidenceForCurrentImage(
    releases,
    app,
    observation?.actual_image,
    'scan'
  )
  const evidenceRows = [
    { label: '业务验证', evidence: verification },
    { label: '安全扫描', evidence: scan },
  ].filter(
    (row): row is { label: string; evidence: VerificationEvidence } =>
      !!row.evidence
  )

  return (
    <div className='mt-2 min-w-0 rounded-md border bg-background/70 px-3 py-2.5'>
      <div className='flex flex-wrap items-center justify-between gap-x-3 gap-y-1'>
        <Badge variant='outline' className={statusClass}>
          {observationPending ? '正在读取镜像' : statusLabel}
        </Badge>
        <span className='text-xs text-muted-foreground'>
          {observation?.observed_at
            ? `本地观察 · ${localDateTime(observation.observed_at)}`
            : observationPending
              ? '正在读取观察时间'
              : '观察时间未知'}
        </span>
      </div>
      <div className='mt-2 grid min-w-0 gap-2 sm:grid-cols-2'>
        <div className='min-w-0'>
          <p className='text-xs text-muted-foreground'>实际镜像</p>
          <p className='mt-1 font-mono text-xs break-all'>
            {observation?.actual_image
              ? compactImage(observation.actual_image)
              : observationPending
                ? '正在读取…'
                : '无法确认'}
          </p>
        </div>
        <div className='min-w-0'>
          <p className='text-xs text-muted-foreground'>最近发布任务</p>
          <p className='mt-1 text-xs font-medium break-words'>
            {latestDeployLabel}
          </p>
          {latestDeploy && !latestDeployPending && !latestDeployError && (
            <p className='mt-1 text-xs text-muted-foreground'>
              {localDateTime(
                latestDeploy.finished_at ?? latestDeploy.created_at
              )}
            </p>
          )}
        </div>
      </div>
      <div className='mt-2 flex flex-wrap gap-1.5 border-t pt-2'>
        <EvidenceBadge
          label='业务验证'
          evidence={verification}
          actualImage={observation?.actual_image}
          pending={releasesPending || observationPending}
          error={releasesError || observationError}
        />
        <EvidenceBadge
          label='安全扫描'
          evidence={scan}
          actualImage={observation?.actual_image}
          pending={releasesPending || observationPending}
          error={releasesError || observationError}
        />
      </div>
      {!releasesPending && !releasesError && observation?.actual_image && (
        <p className='mt-1 text-xs leading-5 text-muted-foreground'>
          验证报告仅在 digest 与当前运行镜像完全一致时显示。
        </p>
      )}
      {!!evidenceRows.length && (
        <details className='group mt-2 border-t pt-2 text-xs'>
          <summary className='cursor-pointer font-medium text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring'>
            查看验证报告与有效期
          </summary>
          <div className='mt-2 grid min-w-0 gap-2 sm:grid-cols-2'>
            {evidenceRows.map(({ label, evidence }) => {
              const reportURL = safeReportURL(evidence.report_url)
              return (
                <div
                  key={label}
                  className='min-w-0 rounded-md bg-muted/60 p-2.5'
                >
                  <p className='font-medium text-foreground'>
                    {label} · {evidenceKindLabel(evidence.kind)}
                  </p>
                  {evidence.summary && (
                    <p className='mt-1 break-words text-muted-foreground'>
                      {evidence.summary}
                    </p>
                  )}
                  <p className='mt-1 text-muted-foreground'>
                    {evidence.recorded_by || '来源未知'} · 有效至{' '}
                    {localDateTime(evidence.valid_until)}
                  </p>
                  {reportURL && (
                    <a
                      className='mt-2 inline-flex min-h-11 items-center gap-1 font-medium text-primary underline-offset-4 hover:underline sm:min-h-8'
                      href={reportURL}
                      target='_blank'
                      rel='noopener noreferrer'
                    >
                      打开报告
                      <ArrowUpRight aria-hidden='true' className='size-3.5' />
                    </a>
                  )}
                </div>
              )
            })}
          </div>
        </details>
      )}
      {observationReason && (
        <p className='mt-2 text-xs leading-5 text-muted-foreground'>
          {observationReason}
        </p>
      )}
      {(observation?.actual_image || observation?.expected_image) && (
        <details className='group mt-2 border-t pt-2 text-xs'>
          <summary className='cursor-pointer font-medium text-muted-foreground focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring'>
            查看完整镜像对比
          </summary>
          <dl className='mt-2 space-y-2'>
            <div>
              <dt className='text-muted-foreground'>实际镜像</dt>
              <dd className='mt-1 font-mono break-all'>
                {observation.actual_image ?? '无法确认'}
              </dd>
            </div>
            <div>
              <dt className='text-muted-foreground'>最近成功部署基线</dt>
              <dd className='mt-1 font-mono break-all'>
                {observation.expected_image ?? '暂无成功部署基线'}
              </dd>
            </div>
          </dl>
        </details>
      )}
    </div>
  )
}

function EnvironmentDialog({
  environment,
  onClose,
  onSaved,
}: {
  environment?: DeliveryEnvironment
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(environment?.name ?? '')
  const [tier, setTier] =
    useState<DeliveryEnvironment['environment_tier']>('non_production')
  const mutation = useMutation({
    mutationFn: () =>
      environment
        ? platformApi.confirmEnvironment(environment.id, name)
        : platformApi.createEnvironment({ name, environment_tier: tier }),
    onSuccess: () => {
      toast.success(environment ? '环境信息已核对' : '环境已创建')
      onSaved()
      onClose()
    },
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && !mutation.isPending && onClose()}
    >
      <DialogContent className='sm:max-w-lg'>
        <DialogHeader>
          <DialogTitle>
            {environment ? '核对逻辑环境' : '新建逻辑环境'}
          </DialogTitle>
          <DialogDescription>
            环境用于表达测试、预发或生产阶段。生产属性必须与目标集群保持一致。
          </DialogDescription>
        </DialogHeader>
        <div className='space-y-5'>
          <div className='space-y-2'>
            <Label htmlFor='environment-name'>环境名称</Label>
            <Input
              id='environment-name'
              autoFocus
              maxLength={128}
              value={name}
              placeholder='例如：预发环境'
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          {!environment && (
            <div className='space-y-2'>
              <Label htmlFor='environment-tier'>环境类型</Label>
              <select
                id='environment-tier'
                className={selectClass}
                value={tier}
                onChange={(event) =>
                  setTier(
                    event.target
                      .value as DeliveryEnvironment['environment_tier']
                  )
                }
              >
                <option value='non_production'>非生产</option>
                <option value='production'>生产</option>
              </select>
              <p className='text-xs leading-5 text-muted-foreground'>
                逻辑环境的类型创建后固定。不能把生产集群关联到非生产环境。
              </p>
            </div>
          )}
          {mutation.isError && (
            <p role='alert' className='text-sm text-destructive'>
              {errorMessage(mutation.error)}
            </p>
          )}
        </div>
        <DialogFooter>
          <Button
            variant='outline'
            onClick={onClose}
            disabled={mutation.isPending}
          >
            取消
          </Button>
          <Button
            onClick={() => mutation.mutate()}
            disabled={!name.trim() || mutation.isPending}
          >
            {mutation.isPending
              ? '保存中…'
              : environment
                ? '核对并保存'
                : '创建环境'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ApplicationDialog({
  onClose,
  onSaved,
}: {
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState('')
  const mutation = useMutation({
    mutationFn: () => platformApi.createDeliveryApplication(name.trim()),
    onSuccess: () => {
      toast.success('业务应用已创建')
      onSaved()
      onClose()
    },
  })
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && !mutation.isPending && onClose()}
    >
      <DialogContent className='sm:max-w-md'>
        <DialogHeader>
          <DialogTitle>新建业务应用</DialogTitle>
          <DialogDescription>
            业务应用用于归组多个可以独立构建和发布的组件。
          </DialogDescription>
        </DialogHeader>
        <div className='space-y-2'>
          <Label htmlFor='delivery-application-name'>应用名称</Label>
          <Input
            id='delivery-application-name'
            autoFocus
            maxLength={128}
            value={name}
            placeholder='例如：订单系统'
            onChange={(event) => setName(event.target.value)}
          />
          {mutation.isError && (
            <p role='alert' className='text-sm text-destructive'>
              {errorMessage(mutation.error)}
            </p>
          )}
        </div>
        <DialogFooter>
          <Button
            variant='outline'
            onClick={onClose}
            disabled={mutation.isPending}
          >
            取消
          </Button>
          <Button
            onClick={() => mutation.mutate()}
            disabled={!name.trim() || mutation.isPending}
          >
            {mutation.isPending ? '保存中…' : '创建应用'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function BindingDialog({
  binding,
  app,
  model,
  clusterName,
  environmentTier,
  onClose,
  onSaved,
}: {
  binding?: DeliveryBinding
  app: ComponentBinding
  model: DeliveryModel
  clusterName?: string
  environmentTier?: DeliveryEnvironment['environment_tier']
  onClose: () => void
  onSaved: () => void
}) {
  const [applicationID, setApplicationID] = useState(
    binding?.application_id ? String(binding.application_id) : ''
  )
  const [componentID, setComponentID] = useState(
    binding?.component_id ? String(binding.component_id) : ''
  )
  const [environmentID, setEnvironmentID] = useState(
    binding?.environment_id ? String(binding.environment_id) : ''
  )
  const [newApplicationName, setNewApplicationName] = useState('')
  const [deploymentPipelineConnectionID, setDeploymentPipelineConnectionID] =
    useState(binding?.deployment_pipeline?.connection_id ?? 0)
  const [deploymentPipelineID, setDeploymentPipelineID] = useState(
    binding?.deployment_pipeline?.pipeline_id ?? ''
  )
  const [pipelineScopeAssessment, setPipelineScopeAssessment] =
    useState<DeliveryPipelineScopeReview['write_scope']>('unknown')
  const [pipelineScopeReviewAcknowledged, setPipelineScopeReviewAcknowledged] =
    useState(false)
  const [pipelineSearchText, setPipelineSearchText] = useState('')
  const [pipelineSearchName, setPipelineSearchName] = useState('')
  const [pipelineSearchPage, setPipelineSearchPage] = useState(1)
  const [strategyOverride, setStrategyOverride] = useState<
    '' | 'rolling' | 'blue_green' | 'gateway_canary' | null
  >(null)
  const [serviceNameOverride, setServiceNameOverride] = useState<string | null>(
    null
  )
  const [retentionOverride, setRetentionOverride] = useState<number | null>(
    null
  )
  const [canaryThresholdForm, setCanaryThresholdForm] = useState<
    Partial<Record<CanaryThresholdKey, string>>
  >({})
  const [canaryStagesForm, setCanaryStagesForm] = useState<
    CanaryStageDraft[] | null
  >(null)
  const [canaryGatewayRouteForm, setCanaryGatewayRouteForm] =
    useState<CanaryGatewayRouteForm | null>(null)
  const [newComponentName, setNewComponentName] = useState('')
  const policyQuery = useQuery({
    queryKey: ['delivery-policy', app.id],
    queryFn: async () => (await platformApi.deliveryPolicy(app.id)).data,
    enabled: !!binding?.id,
  })
  const yunxiaoConnections = useQuery({
    queryKey: ['runner-connections'],
    queryFn: () => runnerApi.connections(),
    retry: false,
  })
  const deploymentPipelines = useQuery({
    queryKey: [
      'yunxiao-deployment-pipelines',
      deploymentPipelineConnectionID,
      pipelineSearchName,
      pipelineSearchPage,
    ],
    queryFn: async () =>
      (
        await runnerApi.searchDeploymentPipelines({
          connection_id: deploymentPipelineConnectionID,
          name: pipelineSearchName,
          page: pipelineSearchPage,
        })
      ).data,
    enabled:
      deploymentPipelineConnectionID > 0 && pipelineSearchName.length > 0,
    retry: false,
  })
  const deploymentPipelineCheck = useMutation({
    mutationFn: async () => {
      const response = await runnerApi.inspectDeploymentPipeline({
        connection_id: deploymentPipelineConnectionID,
        pipeline_id: deploymentPipelineID.trim(),
      })
      return response.data
    },
  })
  const deploymentPipelineScopeCheck = useMutation({
    mutationFn: async () => {
      const response = await runnerApi.inspectDeploymentPipelineScope({
        connection_id: deploymentPipelineConnectionID,
        pipeline_id: deploymentPipelineID.trim(),
      })
      return response.data
    },
  })
  const deploymentPipelineScopeReview = useMutation({
    mutationFn: async () => {
      const report = deploymentPipelineScopeCheck.data
      if (!report?.definition_hash || !binding?.id) {
        throw new Error('请先保存环境绑定并重新分析流水线定义')
      }
      const response = await runnerApi.reviewDeploymentPipelineScope({
        app_id: app.id,
        connection_id: deploymentPipelineConnectionID,
        pipeline_id: deploymentPipelineID.trim(),
        definition_hash: report.definition_hash,
        write_scope: pipelineScopeAssessment,
        acknowledged: true,
      })
      return response.data
    },
    onSuccess: (result) => {
      toast.success(result.message)
      setPipelineScopeReviewAcknowledged(false)
      onSaved()
    },
    onError: (error) =>
      toast.error(errorMessage(error, '未能保存人工核对记录')),
  })
  const latestPipelineScopeReview = model.pipeline_scope_reviews?.find(
    (review) => review.binding_id === binding?.id
  )
  const currentPipelineScopeReview =
    deploymentPipelineScopeReview.data?.review ?? latestPipelineScopeReview
  const currentPipelineBindingMatchesForm =
    binding?.deployment_pipeline?.connection_id ===
      deploymentPipelineConnectionID &&
    binding.deployment_pipeline.pipeline_id === deploymentPipelineID.trim()
  const scopeReviewMatchesCurrentDefinition =
    currentPipelineScopeReview?.connection_id ===
      deploymentPipelineConnectionID &&
    currentPipelineScopeReview.pipeline_id === deploymentPipelineID.trim() &&
    currentPipelineScopeReview.definition_hash ===
      deploymentPipelineScopeCheck.data?.definition_hash
  const currentPolicy = policyQuery.data?.versions.find(
    (version) => version.version === policyQuery.data?.current_version
  )
  const strategyDraft =
    strategyOverride ??
    (currentPolicy?.strategy === 'rolling'
      ? ''
      : (currentPolicy?.strategy ?? binding?.strategy_draft ?? ''))
  const serviceName =
    serviceNameOverride ??
    currentPolicy?.service_name ??
    binding?.service_name ??
    ''
  const candidateRetentionHours =
    retentionOverride ?? currentPolicy?.candidate_retention_hours ?? 24
  const currentCanaryAnalysis = currentPolicy?.canary_analysis
  const canaryGatewayRouteValue = (key: CanaryGatewayRouteField): string =>
    canaryGatewayRouteForm?.[key] ??
    (currentCanaryAnalysis?.gateway_route?.[key] === undefined
      ? ''
      : String(currentCanaryAnalysis.gateway_route[key]))
  const canaryGatewayRouteValues: CanaryGatewayRouteForm = {
    connection_id: canaryGatewayRouteValue('connection_id'),
    route_id: canaryGatewayRouteValue('route_id'),
    stable_backend_id: canaryGatewayRouteValue('stable_backend_id'),
    candidate_backend_id: canaryGatewayRouteValue('candidate_backend_id'),
  }
  const hasCanaryGatewayRouteValue = Object.values(
    canaryGatewayRouteValues
  ).some((value) => value.trim() !== '')
  const canaryGatewayConnectionID = Number(
    canaryGatewayRouteValues.connection_id
  )
  const isSafeCanaryGatewayID = (value: string) =>
    value.trim() === value && value.length > 0 && value.length <= 256
  const canaryGatewayRouteValid =
    !hasCanaryGatewayRouteValue ||
    (Number.isSafeInteger(canaryGatewayConnectionID) &&
      canaryGatewayConnectionID > 0 &&
      isSafeCanaryGatewayID(canaryGatewayRouteValues.route_id) &&
      isSafeCanaryGatewayID(canaryGatewayRouteValues.stable_backend_id) &&
      isSafeCanaryGatewayID(canaryGatewayRouteValues.candidate_backend_id) &&
      canaryGatewayRouteValues.stable_backend_id !==
        canaryGatewayRouteValues.candidate_backend_id)
  const canaryGatewayRoute =
    hasCanaryGatewayRouteValue && canaryGatewayRouteValid
      ? {
          connection_id: canaryGatewayConnectionID,
          route_id: canaryGatewayRouteValues.route_id,
          stable_backend_id: canaryGatewayRouteValues.stable_backend_id,
          candidate_backend_id: canaryGatewayRouteValues.candidate_backend_id,
        }
      : undefined
  const canaryGatewayRouteError =
    hasCanaryGatewayRouteValue && !canaryGatewayRouteValid
      ? '填写网关身份时，连接 ID、路由 ID 和两个不同的 backend ID 必须完整有效。'
      : undefined
  const updateCanaryGatewayRoute = (
    key: CanaryGatewayRouteField,
    value: string
  ) =>
    setCanaryGatewayRouteForm({
      ...canaryGatewayRouteValues,
      [key]: value,
    })
  const canaryInputValue = (
    key: CanaryThresholdKey,
    displayScale = 1
  ): string => {
    const formValue = canaryThresholdForm[key]
    return (
      formValue ??
      (currentCanaryAnalysis?.[key] === undefined
        ? ''
        : String(currentCanaryAnalysis[key] * displayScale))
    )
  }
  const canaryNumber = (
    key: CanaryThresholdKey,
    displayScale = 1
  ): number | undefined => {
    const raw = canaryInputValue(key, displayScale)
    if (!raw.trim()) return undefined
    const value = Number(raw) / displayScale
    return Number.isFinite(value) ? value : undefined
  }
  const minCanaryRequests = canaryNumber('min_requests')
  const maxCanaryObservationAge = canaryNumber('max_observation_age_seconds')
  const maxCandidateErrorRate = canaryNumber('max_candidate_error_rate', 100)
  const maxErrorRateIncrease = canaryNumber('max_error_rate_increase', 100)
  const maxCandidateP95Latency = canaryNumber('max_candidate_p95_latency_ms')
  const maxP95LatencyIncrease = canaryNumber(
    'max_p95_latency_increase_ratio',
    100
  )
  const canaryStages =
    canaryStagesForm ??
    (currentCanaryAnalysis?.stages ?? []).map((stage, index) => ({
      id: `saved-stage-${index}`,
      candidateWeight: String(stage.candidate_weight_percent),
      observationSeconds: String(stage.observation_duration_seconds),
    }))
  const selectStrategy = (value: '' | 'blue_green' | 'gateway_canary') => {
    setStrategyOverride(value)
    if (value !== 'gateway_canary') return
    setCanaryStagesForm((current) => {
      if (current) return current
      const savedStages = currentCanaryAnalysis?.stages ?? []
      if (savedStages.length > 0) {
        return savedStages.map((stage, index) => ({
          id: `saved-stage-${index}`,
          candidateWeight: String(stage.candidate_weight_percent),
          observationSeconds: String(stage.observation_duration_seconds),
        }))
      }
      return [newCanaryStageDraft(), newCanaryStageDraft()]
    })
  }
  const parsedCanaryStages = canaryStages.map((stage) => ({
    candidateWeight: Number(stage.candidateWeight),
    observationSeconds: Number(stage.observationSeconds),
  }))
  const canaryStagesValid =
    canaryStages.length >= 2 &&
    canaryStages.length <= 10 &&
    parsedCanaryStages.every(
      (stage, index) =>
        stage.candidateWeight >
          (parsedCanaryStages[index - 1]?.candidateWeight ?? 0) &&
        stage.candidateWeight <= 100 &&
        Number.isInteger(stage.candidateWeight) &&
        stage.observationSeconds > 0 &&
        stage.observationSeconds <= 24 * 60 * 60 &&
        Number.isInteger(stage.observationSeconds)
    ) &&
    parsedCanaryStages[parsedCanaryStages.length - 1]?.candidateWeight === 100
  const canaryAnalysis =
    minCanaryRequests !== undefined &&
    minCanaryRequests > 0 &&
    Number.isInteger(minCanaryRequests) &&
    maxCanaryObservationAge !== undefined &&
    Number.isInteger(maxCanaryObservationAge) &&
    maxCanaryObservationAge > 0 &&
    maxCanaryObservationAge <= 24 * 60 * 60 &&
    maxCandidateErrorRate !== undefined &&
    maxCandidateErrorRate >= 0 &&
    maxCandidateErrorRate <= 1 &&
    maxErrorRateIncrease !== undefined &&
    maxErrorRateIncrease >= 0 &&
    maxErrorRateIncrease <= 1 &&
    maxCandidateP95Latency !== undefined &&
    maxCandidateP95Latency >= 0 &&
    maxP95LatencyIncrease !== undefined &&
    maxP95LatencyIncrease >= 0 &&
    canaryStagesValid &&
    canaryGatewayRouteValid
      ? {
          ...(canaryGatewayRoute ? { gateway_route: canaryGatewayRoute } : {}),
          min_requests: minCanaryRequests,
          max_observation_age_seconds: maxCanaryObservationAge,
          max_candidate_error_rate: maxCandidateErrorRate,
          max_error_rate_increase: maxErrorRateIncrease,
          max_candidate_p95_latency_ms: maxCandidateP95Latency,
          max_p95_latency_increase_ratio: maxP95LatencyIncrease,
          stages: parsedCanaryStages.map((stage) => ({
            candidate_weight_percent: stage.candidateWeight,
            observation_duration_seconds: stage.observationSeconds,
          })),
        }
      : undefined
  const components = model.components.filter(
    (component) => component.application_id === Number(applicationID)
  )
  const selectedApplication = model.applications.find(
    (application) => application.id === Number(applicationID)
  )
  const selectedComponent = components.find(
    (component) => component.id === Number(componentID)
  )
  const selectedEnvironment = model.environments.find(
    (environment) => environment.id === Number(environmentID)
  )
  const deploymentPipelineConfigured =
    deploymentPipelineConnectionID > 0 &&
    /^[1-9][0-9]{0,19}$/.test(deploymentPipelineID.trim())
  const deploymentPipelineEmpty =
    deploymentPipelineConnectionID === 0 && !deploymentPipelineID.trim()
  const deploymentPipelineValid =
    deploymentPipelineConfigured || deploymentPipelineEmpty
  const mutation = useMutation({
    mutationFn: async () => {
      if (
        binding?.id &&
        (!policyQuery.data || policyQuery.isPending || policyQuery.isError)
      ) {
        throw new Error('无法读取当前策略版本，请刷新后重试。')
      }
      return platformApi.assignDeliveryBinding(app.id, {
        application_id: Number(applicationID),
        component_id: Number(componentID),
        environment_id: Number(environmentID),
        strategy_draft: strategyDraft,
        service_name: strategyDraft === 'blue_green' ? serviceName : '',
        expected_policy_version: binding?.id
          ? (policyQuery.data?.current_version ?? 0)
          : 0,
        strategy: strategyDraft || 'rolling',
        candidate_retention_hours:
          strategyDraft === 'blue_green' ? candidateRetentionHours : 0,
        deployment_pipeline: deploymentPipelineConfigured
          ? {
              connection_id: deploymentPipelineConnectionID,
              pipeline_id: deploymentPipelineID.trim(),
            }
          : { connection_id: 0, pipeline_id: '' },
        ...(strategyDraft === 'gateway_canary' && canaryAnalysis
          ? { canary_analysis: canaryAnalysis }
          : {}),
      })
    },
    onSuccess: () => {
      toast.success('对应关系与发布策略已原子保存；不会触发发布')
      onSaved()
      onClose()
    },
  })
  const createComponent = useMutation({
    mutationFn: () =>
      platformApi.createDeliveryComponent(
        Number(applicationID),
        newComponentName.trim()
      ),
    onSuccess: ({ data }) => {
      setComponentID(String(data.id))
      setNewComponentName('')
      onSaved()
      toast.success('组件已添加到业务应用')
    },
  })
  const createApplication = useMutation({
    mutationFn: () =>
      platformApi.createDeliveryApplication(newApplicationName.trim()),
    onSuccess: ({ data }) => {
      setApplicationID(String(data.id))
      setComponentID('')
      setNewApplicationName('')
      onSaved()
      toast.success('业务应用已创建，请继续选择发布组件和环境')
    },
  })

  return (
    <Dialog
      open
      onOpenChange={(open) => !open && !mutation.isPending && onClose()}
    >
      <DialogContent className='max-h-[90dvh] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:max-w-2xl'>
        <DialogHeader className='border-b px-6 py-5 pr-12'>
          <DialogTitle>核对组件归属与环境</DialogTitle>
          <DialogDescription>
            绑定关系与发布策略会一起保存；不会更改 Kubernetes 资源或触发发布。
          </DialogDescription>
        </DialogHeader>

        <div
          data-testid='delivery-binding-scroll-content'
          className='min-h-0 space-y-5 overflow-y-auto px-6 py-5'
        >
          <section
            aria-label='待关联的部署组件与目标'
            data-testid='delivery-binding-identity'
            className='space-y-2 rounded-xl border border-primary/20 bg-primary/5 p-3'
          >
            <div className='flex flex-wrap items-center gap-2'>
              <span className='text-xs font-medium text-muted-foreground'>
                构建事件 APP_NAME
              </span>
              <code className='min-w-0 rounded bg-background px-2 py-1 font-mono text-sm font-semibold break-all'>
                {app.app_name}
              </code>
            </div>
            <div className='grid gap-2 text-sm sm:grid-cols-2'>
              <div className='min-w-0'>
                <p className='text-xs font-medium text-muted-foreground'>
                  代码仓库与构建目录
                </p>
                <p className='mt-1 text-xs leading-5 break-all'>
                  {componentSourceLabel(app.source_kind, app.source_repo_url)}
                  {app.source_kind === 'repository' && app.source_repo_url
                    ? ` · ${app.source_path || '.'}`
                    : ''}
                </p>
              </div>
              <div className='min-w-0'>
                <p className='text-xs font-medium text-muted-foreground'>
                  IMAGE_FULL 镜像仓库
                </p>
                <p className='mt-1 font-mono text-xs break-all'>
                  {app.image_repository || '镜像仓库待配置'}
                </p>
              </div>
              <div className='min-w-0 sm:col-span-2'>
                <p className='text-xs font-medium text-muted-foreground'>
                  实际部署目标
                </p>
                <p className='mt-1 text-xs leading-5 break-all'>
                  {clusterName || `集群 #${app.cluster_id}`} / {app.namespace} /{' '}
                  {app.deployment_name} / {app.container_name}
                  {environmentTier
                    ? ` · ${environmentTier === 'production' ? '生产' : '非生产'}目标`
                    : ' · 环境类型未知'}
                </p>
              </div>
            </div>
            <p className='border-t border-primary/10 pt-3 text-xs leading-5 text-muted-foreground'>
              请为此 APP_NAME
              选择业务应用、发布组件和逻辑环境；平台不会按名称自动猜测。
            </p>
          </section>
          <div className='grid gap-5 sm:grid-cols-2'>
            <div className='space-y-2'>
              <Label htmlFor='binding-application'>业务应用</Label>
              <select
                id='binding-application'
                className={selectClass}
                value={applicationID}
                disabled={
                  !model.applications.length || createApplication.isPending
                }
                onChange={(event) => {
                  setApplicationID(event.target.value)
                  setComponentID('')
                }}
              >
                <option value=''>选择业务应用</option>
                {model.applications.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name} · {originLabel(item.legacy_app_id)}
                  </option>
                ))}
              </select>
              {!model.applications.length && (
                <div className='space-y-2 rounded-lg border border-dashed bg-muted/20 p-3'>
                  <p className='text-xs leading-5 text-muted-foreground'>
                    先创建一个业务应用，用于归组可独立发布的服务；创建后会留在这一步继续关联。
                  </p>
                  <div className='flex items-center gap-2'>
                    <Input
                      aria-label='新业务应用名称'
                      className='h-12 min-w-0 flex-1'
                      maxLength={128}
                      placeholder='例如：订单系统'
                      value={newApplicationName}
                      onChange={(event) =>
                        setNewApplicationName(event.target.value)
                      }
                    />
                    <Button
                      type='button'
                      variant='outline'
                      className='min-h-12 shrink-0'
                      disabled={
                        !newApplicationName.trim() ||
                        createApplication.isPending
                      }
                      onClick={() => createApplication.mutate()}
                    >
                      {createApplication.isPending ? '创建中…' : '创建应用'}
                    </Button>
                  </div>
                  {createApplication.isError && (
                    <p role='alert' className='text-xs text-destructive'>
                      {errorMessage(createApplication.error)}
                    </p>
                  )}
                </div>
              )}
            </div>
            <div className='space-y-2'>
              <Label htmlFor='binding-component'>发布组件</Label>
              <select
                id='binding-component'
                className={selectClass}
                value={componentID}
                disabled={!applicationID}
                onChange={(event) => setComponentID(event.target.value)}
              >
                <option value=''>选择组件</option>
                {components.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name} · {originLabel(item.legacy_app_id)}
                  </option>
                ))}
              </select>
            </div>
            <div className='space-y-2 sm:col-span-2'>
              <Label htmlFor='binding-environment'>逻辑环境</Label>
              <select
                id='binding-environment'
                className={selectClass}
                value={environmentID}
                onChange={(event) => setEnvironmentID(event.target.value)}
              >
                <option value=''>选择已核对的环境</option>
                {model.environments.map((item) => {
                  const compatible = item.environment_tier === environmentTier
                  return (
                    <option
                      key={item.id}
                      value={item.id}
                      disabled={item.requires_review || !compatible}
                    >
                      {item.name} ·{' '}
                      {item.environment_tier === 'production'
                        ? '生产'
                        : '非生产'}
                      {item.requires_review ? ' · 待管理员核对' : ''}
                      {!compatible ? ' · 与目标集群类型不匹配' : ''}
                    </option>
                  )
                })}
              </select>
              <p className='text-xs leading-5 text-muted-foreground'>
                目标集群：{app.namespace} / {app.deployment_name} /{' '}
                {app.container_name}
                {environmentTier === 'production'
                  ? ' · 生产目标'
                  : ' · 非生产目标'}
              </p>
            </div>
            <div
              aria-live='polite'
              className='rounded-xl border border-dashed bg-muted/20 p-4 sm:col-span-2'
            >
              <p className='text-sm font-semibold'>保存后将建立的对应关系</p>
              <div className='mt-3 grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] sm:items-center'>
                <div className='min-w-0 border-l-2 border-primary/40 pl-3'>
                  <p className='text-xs font-medium text-muted-foreground'>
                    构建来源 · APP_NAME / 镜像仓库
                  </p>
                  <p className='mt-1 text-sm font-semibold break-words'>
                    {app.app_name}
                  </p>
                  <p className='mt-1 font-mono text-xs leading-5 break-all text-muted-foreground'>
                    {app.image_repository || '镜像仓库待配置'}
                  </p>
                </div>
                <ArrowRight
                  aria-hidden='true'
                  className='hidden size-4 text-muted-foreground sm:block'
                />
                <div className='min-w-0 border-l-2 border-primary/40 pl-3'>
                  <p className='text-xs font-medium text-muted-foreground'>
                    业务归属 · 逻辑环境
                  </p>
                  <p className='mt-1 text-sm font-semibold break-words'>
                    {selectedApplication?.name || '选择业务应用'} /{' '}
                    {selectedComponent?.name || '选择发布组件'}
                  </p>
                  <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                    {selectedEnvironment?.name || '选择逻辑环境'}
                  </p>
                </div>
              </div>
              <p className='mt-3 border-t pt-3 text-xs leading-5 break-words text-muted-foreground'>
                更新位置：{clusterName || `集群 #${app.cluster_id}`} /{' '}
                {app.namespace} / {app.deployment_name} / {app.container_name}
              </p>
            </div>
            <fieldset className='space-y-3 rounded-xl border bg-muted/20 p-4 sm:col-span-2'>
              <legend className='px-1 text-sm font-medium'>
                配置部署流水线（可选）
              </legend>
              <p className='text-xs leading-5 text-muted-foreground'>
                指定配置恢复使用的独立 Yunxiao
                流水线；计划会固定连接、流水线、Git commit 和镜像
                digest。当前绑定只登记关系，不会触发任务。
              </p>
              <div className='rounded-lg border border-amber-600/25 bg-amber-500/5 p-3 text-sm'>
                <p className='font-medium text-amber-900 dark:text-amber-200'>
                  写入范围尚未确认
                </p>
                <p className='mt-1 text-sm leading-5 text-muted-foreground'>
                  还不能确认流水线只写 ConfigMap/Secret，或也会修改
                  Deployment、镜像及其他资源。这里仅保存连接与流水线
                  ID；不会运行云效任务，也不会把它接入平台发布 Worker。
                </p>
                <p className='mt-2 text-sm leading-5 text-muted-foreground'>
                  启用前请核对写入资源与
                  namespace、执行身份和跨应用影响，再设计审批与恢复。
                </p>
              </div>
              {!app.deployment_config_repo_url ? (
                <div className='space-y-3'>
                  <p className='text-xs leading-5 text-amber-700 dark:text-amber-300'>
                    请先在组件接入中登记部署配置仓库，再绑定配置部署流水线。
                  </p>
                  {deploymentPipelineConfigured && (
                    <Button
                      type='button'
                      variant='outline'
                      className='min-h-11'
                      onClick={() => {
                        deploymentPipelineScopeReview.reset()
                        deploymentPipelineScopeCheck.reset()
                        setDeploymentPipelineConnectionID(0)
                        setDeploymentPipelineID('')
                        setPipelineScopeAssessment('unknown')
                        setPipelineScopeReviewAcknowledged(false)
                      }}
                    >
                      清除已有流水线绑定
                    </Button>
                  )}
                </div>
              ) : (
                <div className='grid gap-3 sm:grid-cols-2'>
                  <div className='space-y-2'>
                    <Label htmlFor='deployment-pipeline-connection'>
                      Yunxiao 连接
                    </Label>
                    <select
                      id='deployment-pipeline-connection'
                      className={selectClass}
                      value={deploymentPipelineConnectionID}
                      onChange={(event) => {
                        deploymentPipelineCheck.reset()
                        deploymentPipelineScopeCheck.reset()
                        deploymentPipelineScopeReview.reset()
                        setPipelineScopeAssessment('unknown')
                        setPipelineScopeReviewAcknowledged(false)
                        setDeploymentPipelineConnectionID(
                          Number(event.target.value)
                        )
                        setDeploymentPipelineID('')
                        setPipelineSearchName('')
                        setPipelineSearchPage(1)
                      }}
                    >
                      <option value={0}>暂不绑定</option>
                      {(yunxiaoConnections.data?.data ?? []).map(
                        (connection) => (
                          <option key={connection.id} value={connection.id}>
                            {connection.name} · {connection.edition}
                          </option>
                        )
                      )}
                    </select>
                    {yunxiaoConnections.isError && (
                      <p role='alert' className='text-xs text-destructive'>
                        无法读取云效连接；可先保存组件关系，稍后重试。
                      </p>
                    )}
                  </div>
                  <div className='space-y-2 sm:col-span-2'>
                    <Label htmlFor='deployment-pipeline-search'>
                      按名称查找配置部署流水线
                    </Label>
                    <div className='flex flex-col gap-2 sm:flex-row'>
                      <Input
                        id='deployment-pipeline-search'
                        maxLength={100}
                        placeholder='例如：orders-config-deploy'
                        value={pipelineSearchText}
                        onChange={(event) =>
                          setPipelineSearchText(event.target.value)
                        }
                        onKeyDown={(event) => {
                          if (
                            event.key === 'Enter' &&
                            deploymentPipelineConnectionID > 0 &&
                            pipelineSearchText.trim()
                          ) {
                            event.preventDefault()
                            setPipelineSearchPage(1)
                            setPipelineSearchName(pipelineSearchText.trim())
                          }
                        }}
                      />
                      <Button
                        type='button'
                        variant='outline'
                        className='min-h-11 shrink-0'
                        disabled={
                          deploymentPipelineConnectionID <= 0 ||
                          !pipelineSearchText.trim() ||
                          deploymentPipelines.isFetching
                        }
                        onClick={() => {
                          setPipelineSearchPage(1)
                          setPipelineSearchName(pipelineSearchText.trim())
                        }}
                      >
                        <Workflow
                          aria-hidden='true'
                          className={`size-4 ${deploymentPipelines.isFetching ? 'animate-pulse' : ''}`}
                        />
                        {deploymentPipelines.isFetching
                          ? '正在搜索…'
                          : '搜索流水线'}
                      </Button>
                    </div>
                    <p className='text-xs leading-5 text-muted-foreground'>
                      连接云效后按名称搜索，只返回流水线名称和 ID。
                    </p>
                    {deploymentPipelines.isError && (
                      <p role='alert' className='text-xs text-destructive'>
                        {errorMessage(
                          deploymentPipelines.error,
                          '搜索失败，请检查云效连接和流水线只读权限。'
                        )}
                      </p>
                    )}
                    {pipelineSearchName && deploymentPipelines.data && (
                      <div className='space-y-2'>
                        <Label htmlFor='deployment-pipeline-result'>
                          搜索结果 · 第 {deploymentPipelines.data.page} 页
                          {deploymentPipelines.data.total > 0 &&
                            ` / 共 ${deploymentPipelines.data.total} 条`}
                        </Label>
                        {deploymentPipelines.data.items.length > 0 ? (
                          <select
                            id='deployment-pipeline-result'
                            className={selectClass}
                            value={deploymentPipelineID}
                            onChange={(event) => {
                              deploymentPipelineCheck.reset()
                              deploymentPipelineScopeCheck.reset()
                              deploymentPipelineScopeReview.reset()
                              setPipelineScopeAssessment('unknown')
                              setPipelineScopeReviewAcknowledged(false)
                              setDeploymentPipelineID(event.target.value)
                            }}
                          >
                            <option value=''>选择一个配置部署流水线</option>
                            {deploymentPipelines.data.items.map((pipeline) => (
                              <option
                                key={pipeline.pipeline_id}
                                value={String(pipeline.pipeline_id)}
                              >
                                {pipeline.name} · ID {pipeline.pipeline_id}
                              </option>
                            ))}
                          </select>
                        ) : (
                          <p className='rounded-md border border-dashed px-3 py-2 text-sm text-muted-foreground'>
                            没有找到匹配流水线，请换一个名称关键词。
                          </p>
                        )}
                        <div className='flex items-center justify-between gap-3'>
                          <p className='min-w-0 text-xs text-muted-foreground'>
                            {deploymentPipelineID
                              ? `已选择流水线 ID ${deploymentPipelineID}`
                              : '请选择结果，或使用下方手动填写 ID。'}
                          </p>
                          <div className='flex shrink-0 gap-2'>
                            <Button
                              type='button'
                              variant='ghost'
                              size='sm'
                              disabled={
                                deploymentPipelines.data.page <= 1 ||
                                deploymentPipelines.isFetching
                              }
                              onClick={() =>
                                setPipelineSearchPage((page) => page - 1)
                              }
                            >
                              上一页
                            </Button>
                            <Button
                              type='button'
                              variant='ghost'
                              size='sm'
                              disabled={
                                deploymentPipelines.data.page >=
                                  deploymentPipelines.data.total_pages ||
                                deploymentPipelines.isFetching
                              }
                              onClick={() =>
                                setPipelineSearchPage((page) => page + 1)
                              }
                            >
                              下一页
                            </Button>
                          </div>
                        </div>
                      </div>
                    )}
                    {deploymentPipelineID && !deploymentPipelines.data && (
                      <p className='text-xs text-muted-foreground'>
                        当前登记的流水线 ID：{deploymentPipelineID}
                      </p>
                    )}
                    <details className='rounded-md border px-3 py-2'>
                      <summary className='cursor-pointer text-sm font-medium'>
                        高级选项：手动填写流水线 ID
                      </summary>
                      <div className='mt-3 space-y-2'>
                        <Label htmlFor='deployment-pipeline-id'>
                          配置部署流水线 ID
                        </Label>
                        <Input
                          id='deployment-pipeline-id'
                          inputMode='numeric'
                          maxLength={20}
                          placeholder='例如 123456'
                          value={deploymentPipelineID}
                          onChange={(event) => {
                            deploymentPipelineCheck.reset()
                            deploymentPipelineScopeCheck.reset()
                            deploymentPipelineScopeReview.reset()
                            setPipelineScopeAssessment('unknown')
                            setPipelineScopeReviewAcknowledged(false)
                            setDeploymentPipelineID(event.target.value)
                          }}
                        />
                      </div>
                    </details>
                  </div>
                  <div className='flex flex-col gap-2 sm:col-span-2 sm:flex-row sm:items-center'>
                    <Button
                      type='button'
                      variant='outline'
                      className='min-h-11 shrink-0'
                      disabled={
                        !deploymentPipelineConfigured ||
                        deploymentPipelineCheck.isPending
                      }
                      onClick={() => deploymentPipelineCheck.mutate()}
                    >
                      <RefreshCw
                        aria-hidden='true'
                        className={`size-4 ${deploymentPipelineCheck.isPending ? 'animate-spin' : ''}`}
                      />
                      {deploymentPipelineCheck.isPending
                        ? '正在读取…'
                        : '检查连通性'}
                    </Button>
                    <p className='text-xs leading-5 text-muted-foreground'>
                      只读名称和类型，不运行流水线。
                    </p>
                  </div>
                </div>
              )}
              {!deploymentPipelineValid && (
                <p role='alert' className='text-xs text-destructive'>
                  连接和流水线 ID 必须同时填写；流水线 ID 只能是数字。
                </p>
              )}
              {deploymentPipelineConfigured &&
                !deploymentPipelineCheck.data && (
                  <p
                    role='status'
                    className='text-xs leading-5 text-muted-foreground'
                  >
                    已登记为独立配置部署流水线；写入范围核实前只保存关联，不触发任务或连接发布
                    Worker。
                  </p>
                )}
              {deploymentPipelineCheck.isError && (
                <p role='alert' className='text-sm text-destructive'>
                  {errorMessage(
                    deploymentPipelineCheck.error,
                    '读取失败，请检查云效连接、流水线 ID 和访问权限。'
                  )}
                </p>
              )}
              {deploymentPipelineCheck.data && (
                <div className='space-y-3'>
                  <div
                    aria-live='polite'
                    className='flex gap-3 rounded-lg border border-emerald-600/25 bg-emerald-500/5 p-3 text-sm'
                  >
                    <CircleCheck
                      aria-hidden='true'
                      className='mt-0.5 size-4 shrink-0 text-emerald-700 dark:text-emerald-300'
                    />
                    <div className='min-w-0 space-y-1'>
                      <p className='font-medium break-words'>
                        云效连接成功：
                        {deploymentPipelineCheck.data.name ||
                          `流水线 ${deploymentPipelineCheck.data.pipeline_id}`}
                      </p>
                      <p className='text-xs leading-5 text-muted-foreground'>
                        {deploymentPipelineCheck.data.type || '类型未知'} ·
                        最近更新{' '}
                        {unixMillisecondsDateTime(
                          deploymentPipelineCheck.data.update_time
                        )}
                      </p>
                      <p className='text-xs leading-5 text-muted-foreground'>
                        元数据连通不代表写入范围安全或配置已生效。
                      </p>
                    </div>
                  </div>
                  <Button
                    type='button'
                    variant='outline'
                    className='min-h-11 w-full justify-center sm:w-auto'
                    disabled={deploymentPipelineScopeCheck.isPending}
                    onClick={() => {
                      setPipelineScopeAssessment('unknown')
                      setPipelineScopeReviewAcknowledged(false)
                      deploymentPipelineScopeReview.reset()
                      deploymentPipelineScopeCheck.mutate()
                    }}
                  >
                    <RefreshCw
                      aria-hidden='true'
                      className={`size-4 ${deploymentPipelineScopeCheck.isPending ? 'animate-spin' : ''}`}
                    />
                    {deploymentPipelineScopeCheck.isPending
                      ? '正在分析定义…'
                      : '分析可能写入范围'}
                  </Button>
                </div>
              )}
              {deploymentPipelineScopeCheck.isError && (
                <p role='alert' className='text-sm text-destructive'>
                  {errorMessage(
                    deploymentPipelineScopeCheck.error,
                    '无法读取或分析流水线定义；请直接在云效检查任务和执行身份。'
                  )}
                </p>
              )}
              {deploymentPipelineScopeCheck.data && (
                <section
                  role='region'
                  aria-label='配置流水线写入范围静态分析结果'
                  className='space-y-3 rounded-lg border border-amber-600/30 bg-amber-500/5 p-3'
                >
                  <div className='flex items-start gap-2'>
                    <CircleHelp
                      aria-hidden='true'
                      className='mt-0.5 size-4 shrink-0 text-amber-800 dark:text-amber-200'
                    />
                    <div className='min-w-0 space-y-1'>
                      <p className='text-sm font-semibold text-amber-950 dark:text-amber-100'>
                        {deploymentPipelineScopeCheck.data.analysis_status ===
                        'manual_review_required'
                          ? '已扫描常见写入标记，仍需人工确认'
                          : '流水线定义无法自动分析'}
                      </p>
                      <p className='text-xs leading-5 text-muted-foreground'>
                        这不是安全结论。检查不会运行流水线，也不会返回脚本或任务参数。
                      </p>
                    </div>
                  </div>
                  {deploymentPipelineScopeCheck.data.findings.length > 0 ? (
                    <ul className='space-y-2'>
                      {deploymentPipelineScopeCheck.data.findings.map(
                        (finding) => (
                          <li
                            key={`${finding.node_ref}-${finding.category}`}
                            className='rounded-md border bg-background/70 px-3 py-2'
                          >
                            <div className='flex flex-wrap items-center justify-between gap-2'>
                              <span className='text-xs font-semibold'>
                                {pipelineScopeCategoryLabels[
                                  finding.category
                                ] ?? '未分类动作'}
                              </span>
                              <code className='max-w-full text-[11px] break-all text-muted-foreground'>
                                {finding.node_ref}
                              </code>
                            </div>
                            <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                              {finding.summary}
                            </p>
                          </li>
                        )
                      )}
                    </ul>
                  ) : (
                    <p className='text-xs leading-5 text-muted-foreground'>
                      未识别到常见写入标记；这不代表流水线只读。
                    </p>
                  )}
                  {deploymentPipelineScopeCheck.data.unresolved_nodes.length >
                    0 && (
                    <p className='text-xs leading-5 text-muted-foreground'>
                      仍有{' '}
                      {
                        deploymentPipelineScopeCheck.data.unresolved_nodes
                          .length
                      }{' '}
                      个脚本或任务模板需要在云效逐项核对。
                    </p>
                  )}
                  <p className='text-xs leading-5 text-amber-950 dark:text-amber-100'>
                    {deploymentPipelineScopeCheck.data.warning}
                  </p>
                  <details className='border-t pt-2'>
                    <summary className='flex min-h-11 cursor-pointer items-center text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring'>
                      在云效完成 4 项核对
                    </summary>
                    <div className='space-y-3 pb-1'>
                      <Button
                        type='button'
                        variant='outline'
                        className='min-h-11 w-full gap-2 sm:w-auto'
                        onClick={() =>
                          void copyConfigurationPipelineScopeRequest(
                            deploymentPipelineID.trim()
                          )
                        }
                      >
                        <Copy className='size-4' aria-hidden='true' />
                        复制核对请求给负责人
                      </Button>
                      <ol className='list-decimal space-y-2 pl-5 text-xs leading-5 text-muted-foreground'>
                        <li>
                          <span className='font-medium text-foreground'>
                            展开完整定义：
                          </span>
                          打开当前流水线的所有阶段、任务和步骤，展开引用的模板与插件，并对照本页定义指纹确认是同一版本。
                        </li>
                        <li>
                          <span className='font-medium text-foreground'>
                            追踪动态命令：
                          </span>
                          核对 Shell、远程下载、子脚本、变量展开和 Helm
                          等任务。静态扫描没有命中，不代表没有写操作。
                        </li>
                        <li>
                          <span className='font-medium text-foreground'>
                            确认身份和权限：
                          </span>
                          查明任务实际使用的云效连接或 Runner
                          身份，以及它能写入的 Kubernetes 资源、namespace
                          和目标。
                        </li>
                        <li>
                          <span className='font-medium text-foreground'>
                            划定发布责任：
                          </span>
                          确认它是否会改 Deployment、镜像或
                          Service，再明确配置流水线与平台 Worker
                          各自负责的目标和互斥边界。
                        </li>
                      </ol>
                      <p className='text-xs leading-5 text-muted-foreground'>
                        建议把流水线
                        ID、定义指纹、核对节点、执行身份、资源范围、核对人和时间记入接入记录。平台会保存当前定义的人工结论、核对人、时间与指纹；该记录仅作审计，不等于平台验证，也不会开放触发。逐项证据请另存接入记录，勿包含凭据或
                        Secret 值。
                      </p>
                    </div>
                  </details>
                  {deploymentPipelineScopeCheck.data.definition_hash && (
                    <div className='flex min-h-11 items-center justify-between gap-2 border-t pt-2'>
                      <div className='min-w-0 space-y-1'>
                        <p className='text-xs leading-5 text-muted-foreground'>
                          定义指纹 · SHA-256
                        </p>
                        <code
                          className='font-mono text-[11px] text-muted-foreground'
                          title={
                            deploymentPipelineScopeCheck.data.definition_hash
                          }
                        >
                          {deploymentPipelineScopeCheck.data.definition_hash.slice(
                            0,
                            12
                          )}
                          …
                        </code>
                      </div>
                      <Button
                        type='button'
                        variant='outline'
                        size='icon'
                        className='size-11 shrink-0'
                        aria-label='复制完整定义指纹'
                        title='复制完整定义指纹'
                        onClick={() =>
                          void copyPipelineScopeFingerprint(
                            deploymentPipelineScopeCheck.data.definition_hash!
                          )
                        }
                      >
                        <Copy className='size-4' aria-hidden='true' />
                      </Button>
                    </div>
                  )}
                  {currentPipelineScopeReview && (
                    <p
                      role='status'
                      className={`rounded-md border px-3 py-2 text-xs leading-5 ${scopeReviewMatchesCurrentDefinition ? 'border-emerald-600/25 bg-emerald-500/5 text-emerald-950 dark:text-emerald-100' : 'border-amber-600/25 bg-amber-500/5 text-amber-950 dark:text-amber-100'}`}
                    >
                      {!deploymentPipelineScopeCheck.data?.definition_hash
                        ? `已有人工核对记录：${currentPipelineScopeReview.reviewer} · ${currentPipelineScopeReview.write_scope}。重新分析当前定义后，才能确认该记录是否仍适用。`
                        : scopeReviewMatchesCurrentDefinition
                          ? `此定义已有人工核对记录：${currentPipelineScopeReview.reviewer} · ${currentPipelineScopeReview.write_scope}。记录仅作审计，不开放触发。`
                          : '上次核对针对其他流水线或定义版本；当前指纹尚无核对记录。'}
                    </p>
                  )}
                  <details className='border-t pt-2'>
                    <summary className='flex min-h-11 cursor-pointer items-center text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring'>
                      记录人工核对结论
                    </summary>
                    <div className='space-y-3 pb-1'>
                      <div className='space-y-1.5'>
                        <Label htmlFor='pipeline-scope-assessment'>
                          你核对后的写入范围
                        </Label>
                        <select
                          id='pipeline-scope-assessment'
                          className={selectClass}
                          value={pipelineScopeAssessment}
                          onChange={(event) => {
                            setPipelineScopeAssessment(
                              event.target
                                .value as DeliveryPipelineScopeReview['write_scope']
                            )
                            setPipelineScopeReviewAcknowledged(false)
                          }}
                        >
                          <option value='unknown'>
                            仍不确定或存在无法核实项
                          </option>
                          <option value='configuration_only'>
                            仅配置资源，已核实不改工作负载、镜像或 Service
                          </option>
                          <option value='workload_or_image'>
                            包含 Deployment、镜像、Service 或其他工作负载写入
                          </option>
                        </select>
                      </div>
                      <label className='flex min-h-11 cursor-pointer items-center gap-2 text-sm leading-5 text-muted-foreground'>
                        <input
                          type='checkbox'
                          className='size-4 shrink-0 accent-primary'
                          checked={pipelineScopeReviewAcknowledged}
                          onChange={(event) =>
                            setPipelineScopeReviewAcknowledged(
                              event.target.checked
                            )
                          }
                        />
                        <span>
                          我已在云效核对完整定义、引用模板/脚本和实际执行身份；本记录对应上方
                          SHA-256 指纹。
                        </span>
                      </label>
                      {!binding?.id && (
                        <p className='text-xs leading-5 text-muted-foreground'>
                          先保存环境绑定，再重新打开此页面记录核对结论。
                        </p>
                      )}
                      {binding?.id && !currentPipelineBindingMatchesForm && (
                        <p className='text-xs leading-5 text-muted-foreground'>
                          先保存当前流水线关联，再重新分析并记录核对结论。
                        </p>
                      )}
                      <Button
                        type='button'
                        variant='outline'
                        className='min-h-11 w-full border-primary/50 text-primary hover:bg-primary/5 hover:text-primary sm:w-auto dark:border-primary/50 dark:text-primary'
                        disabled={
                          !binding?.id ||
                          !currentPipelineBindingMatchesForm ||
                          !deploymentPipelineScopeCheck.data.definition_hash ||
                          deploymentPipelineScopeCheck.data.analysis_status !==
                            'manual_review_required' ||
                          !pipelineScopeReviewAcknowledged ||
                          deploymentPipelineScopeReview.isPending
                        }
                        onClick={() => deploymentPipelineScopeReview.mutate()}
                      >
                        {deploymentPipelineScopeReview.isPending
                          ? '正在复核并记录…'
                          : '记录核对结论'}
                      </Button>
                      {deploymentPipelineScopeReview.isError && (
                        <p
                          role='alert'
                          className='text-xs leading-5 text-destructive'
                        >
                          {errorMessage(
                            deploymentPipelineScopeReview.error,
                            '流水线定义或绑定已变化，请重新分析后再记录。'
                          )}
                        </p>
                      )}
                      <p className='text-xs leading-5 text-muted-foreground'>
                        保存前服务端会重新读取定义并比对指纹。结论会记录核对人和时间；无论选择哪项，都不会触发流水线或开放发布
                        Worker。
                      </p>
                    </div>
                  </details>
                </section>
              )}
            </fieldset>
            <fieldset className='space-y-3 sm:col-span-2'>
              <legend className='text-sm font-medium'>发布策略预览</legend>
              <p className='text-xs leading-5 text-muted-foreground'>
                每次修改都会追加不可变策略版本；之后创建的计划固定当时版本，已审批计划不会随配置变化。这里只设置平台发布编排，不会立即部署；云效构建链路保持不变。
              </p>
              <div className='grid gap-3 md:grid-cols-3'>
                {[
                  {
                    value: '' as const,
                    title: '现有滚动更新',
                    badge: '当前路径',
                    icon: Layers3,
                    description:
                      '继续使用现有 Deployment 更新、审批和回滚流程。',
                    requirement: '目标：现有 Deployment',
                  },
                  {
                    value: 'blue_green' as const,
                    title: '蓝绿发布',
                    badge: '发布计划支持',
                    icon: GitBranch,
                    description:
                      '独立审批后执行；计划会复核 Service 入口、候选隔离、双版本容量与恢复条件。',
                    requirement: '需要：独立候选版 + Service',
                  },
                  {
                    value: 'gateway_canary' as const,
                    title: '网关金丝雀',
                    badge: '等待网关联调',
                    icon: Route,
                    description:
                      '需要既有网关的权重读写、回读和指标接口；当前未验证。',
                    requirement: '需要：网关权重 + 指标',
                  },
                ].map((strategy) => {
                  const selected =
                    strategy.value === ''
                      ? strategyDraft === '' || strategyDraft === 'rolling'
                      : strategyDraft === strategy.value
                  const Icon = strategy.icon
                  return (
                    <button
                      key={strategy.value || 'rolling'}
                      type='button'
                      aria-pressed={selected}
                      onClick={() => selectStrategy(strategy.value)}
                      className={`min-h-32 rounded-xl border p-4 text-left transition-colors focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring ${selected ? 'border-primary bg-primary/5 ring-1 ring-primary/30' : 'hover:border-foreground/20 hover:bg-muted/40'}`}
                    >
                      <span className='flex min-w-0 items-start gap-2.5'>
                        <span
                          className={`flex size-8 shrink-0 items-center justify-center rounded-lg ${selected ? 'bg-primary text-primary-foreground' : 'bg-muted text-muted-foreground'}`}
                        >
                          <Icon className='size-4' aria-hidden='true' />
                        </span>
                        <span className='min-w-0 pt-0.5'>
                          <span className='block text-sm font-semibold'>
                            {strategy.title}
                          </span>
                          <span className='mt-1 block text-xs font-medium text-muted-foreground'>
                            {strategy.requirement}
                          </span>
                        </span>
                      </span>
                      <span className='mt-2 flex'>
                        <Badge variant={selected ? 'default' : 'outline'}>
                          {strategy.badge}
                        </Badge>
                      </span>
                      <span className='mt-3 block text-xs leading-5 text-muted-foreground'>
                        {strategy.description}
                      </span>
                    </button>
                  )
                })}
              </div>
              <ReleaseStrategyJourney
                strategy={strategyDraft === '' ? 'rolling' : strategyDraft}
                compact
              />
              {strategyDraft === 'gateway_canary' && (
                <div className='space-y-4 rounded-xl border border-l-2 border-border border-l-amber-500 bg-card p-4'>
                  <div className='flex flex-col gap-2 sm:flex-row sm:items-start sm:justify-between'>
                    <div>
                      <p className='text-sm font-semibold'>金丝雀分析阈值</p>
                      <p className='mt-1 max-w-2xl text-xs leading-5 text-muted-foreground'>
                        阈值会写入不可变策略版本，审批计划固定当前值。请按服务基线和观测能力填写；平台不预置生产阈值。
                      </p>
                    </div>
                    <Badge
                      variant='outline'
                      className='self-start border-amber-400/70 bg-background text-amber-900 dark:border-amber-800 dark:text-amber-200'
                    >
                      草稿 · 网关联调未完成
                    </Badge>
                  </div>
                  <div className='space-y-4'>
                    {canaryThresholdGroups.map((group, index) => (
                      <fieldset
                        key={group.title}
                        className={
                          index > 0 ? 'space-y-3 border-t pt-4' : 'space-y-3'
                        }
                      >
                        <legend className='text-sm font-medium text-foreground'>
                          {group.title}
                        </legend>
                        <div className='grid gap-3 sm:grid-cols-2'>
                          {group.fields.map((field) => {
                            const value = canaryInputValue(
                              field.key,
                              field.scale
                            )
                            const requiresInteger =
                              field.key === 'min_requests' ||
                              field.key === 'max_observation_age_seconds'
                            const hasIntegerError =
                              requiresInteger &&
                              value.trim() !== '' &&
                              !Number.isInteger(Number(value))
                            const errorID = `canary-${field.key}-error`
                            return (
                              <div key={field.key} className='space-y-1.5'>
                                <Label htmlFor={`canary-${field.key}`}>
                                  {field.label}
                                </Label>
                                <div className='relative'>
                                  <Input
                                    id={`canary-${field.key}`}
                                    aria-label={field.label}
                                    aria-invalid={hasIntegerError}
                                    aria-describedby={
                                      hasIntegerError ? errorID : undefined
                                    }
                                    type='number'
                                    min={field.min}
                                    max={field.max}
                                    step={field.step}
                                    inputMode='decimal'
                                    className='h-10 pe-16'
                                    placeholder={field.placeholder}
                                    value={value}
                                    onChange={(event) =>
                                      setCanaryThresholdForm((current) => ({
                                        ...current,
                                        [field.key]: event.target.value,
                                      }))
                                    }
                                  />
                                  <span className='pointer-events-none absolute inset-y-0 end-3 flex items-center text-xs text-muted-foreground'>
                                    {field.unit}
                                  </span>
                                </div>
                                {hasIntegerError && (
                                  <p
                                    id={errorID}
                                    role='alert'
                                    className='text-xs text-destructive'
                                  >
                                    {field.key === 'min_requests'
                                      ? '请求数必须是整数。'
                                      : '观测年龄必须填写整数秒。'}
                                  </p>
                                )}
                              </div>
                            )
                          })}
                        </div>
                      </fieldset>
                    ))}
                  </div>
                  <fieldset className='space-y-3 border-t pt-4'>
                    <legend className='text-sm font-medium text-foreground'>
                      网关路由身份（可稍后登记）
                    </legend>
                    <p className='text-xs leading-5 text-muted-foreground'>
                      按现有网关配置填写连接 ID、路由 ID 和稳定/候选 backend
                      ID。暂时不知道时可全部留空；填入后会固定到策略与计划快照。当前没有真实网关适配器，平台不会验证这些
                      ID、读取权重或调整流量。
                    </p>
                    <div className='grid gap-3 sm:grid-cols-2'>
                      {(
                        [
                          ['connection_id', '网关连接 ID', '正整数'],
                          ['route_id', '路由 ID', '从网关配置复制'],
                          [
                            'stable_backend_id',
                            '稳定版 backend ID',
                            '当前生产后端',
                          ],
                          [
                            'candidate_backend_id',
                            '候选版 backend ID',
                            '新版本后端',
                          ],
                        ] as const
                      ).map(([key, label, placeholder]) => (
                        <div key={key} className='space-y-1.5'>
                          <Label htmlFor={`canary-gateway-${key}`}>
                            {label}
                          </Label>
                          <Input
                            id={`canary-gateway-${key}`}
                            aria-label={label}
                            aria-invalid={!!canaryGatewayRouteError}
                            aria-describedby={
                              canaryGatewayRouteError
                                ? 'canary-gateway-route-error'
                                : undefined
                            }
                            type={key === 'connection_id' ? 'number' : 'text'}
                            min={key === 'connection_id' ? 1 : undefined}
                            step={key === 'connection_id' ? 1 : undefined}
                            maxLength={
                              key === 'connection_id' ? undefined : 256
                            }
                            inputMode={
                              key === 'connection_id' ? 'numeric' : 'text'
                            }
                            className='h-10'
                            placeholder={placeholder}
                            value={canaryGatewayRouteValues[key]}
                            onChange={(event) =>
                              updateCanaryGatewayRoute(key, event.target.value)
                            }
                          />
                        </div>
                      ))}
                    </div>
                    {canaryGatewayRouteError && (
                      <p
                        id='canary-gateway-route-error'
                        role='alert'
                        className='text-xs text-destructive'
                      >
                        {canaryGatewayRouteError}
                      </p>
                    )}
                    <p
                      role='status'
                      className='rounded-lg border border-amber-300/70 bg-background/80 px-3 py-2 text-xs leading-5 text-amber-950 dark:border-amber-900 dark:text-amber-100'
                    >
                      {canaryGatewayRoute
                        ? '路由身份已记录，但未经网关读取核验；金丝雀仍处于阻断状态。'
                        : '路由身份尚未登记；策略可以保存为草稿，计划会明确显示缺少的身份信息。'}
                    </p>
                    <details className='group rounded-lg border bg-muted/20 px-3 py-2'>
                      <summary className='min-h-11 cursor-pointer content-center text-sm font-medium focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring'>
                        接入前需要准备哪些网关资料？
                      </summary>
                      <div className='space-y-3 border-t pt-3 text-xs leading-5 text-muted-foreground'>
                        <div className='flex flex-wrap items-start justify-between gap-3'>
                          <p className='min-w-0 flex-1'>
                            把下面这份清单发给网关维护方：
                          </p>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            className='min-h-11 shrink-0 sm:min-h-9'
                            onClick={() => void copyGatewayIntegrationRequest()}
                          >
                            <Copy aria-hidden='true' />
                            复制资料清单
                          </Button>
                        </div>
                        <ol className='list-decimal space-y-2 ps-5'>
                          <li>
                            <span className='font-medium text-foreground'>
                              产品与访问方式：
                            </span>
                            产品名称/版本、平台到网关的内网地址、认证方式和所需权限范围。
                          </li>
                          <li>
                            <span className='font-medium text-foreground'>
                              路由与权重：
                            </span>
                            如何读取 route/backend
                            身份和当前权重；如何设置稳定/候选权重、并发保护或幂等键，以及如何回读生效结果。
                          </li>
                          <li>
                            <span className='font-medium text-foreground'>
                              请求指标：
                            </span>
                            如何按路由和稳定/候选版本读取请求数、错误率和 P95
                            延迟；数据窗口、采集延迟及最小样本量是什么。
                          </li>
                          <li>
                            <span className='font-medium text-foreground'>
                              失败恢复：
                            </span>
                            如何将流量恢复为稳定版 100% / 候选版
                            0%，如何确认恢复生效，以及超时后如何安全回读和重试。
                          </li>
                        </ol>
                        <p className='border-t pt-2'>
                          文档中请隐去
                          Token、密码和私钥。资料到齐并完成故障演练前，金丝雀只保存草稿，不会触发网关调用。
                        </p>
                      </div>
                    </details>
                  </fieldset>
                  <fieldset
                    aria-label='流量阶段与观察窗口'
                    className='space-y-3 border-t pt-4'
                  >
                    <legend className='text-sm font-medium text-foreground'>
                      流量阶段
                    </legend>
                    <p className='text-xs leading-5 text-muted-foreground'>
                      每阶段按候选流量比例递增，并达到观察时长与指标条件后由操作员确认下一步。最后阶段必须观察
                      100% 候选流量；平台不会预填放量比例或观察时长。
                    </p>
                    <ol className='space-y-2.5'>
                      {canaryStages.map((stage, index) => {
                        const previousWeight =
                          index === 0
                            ? 0
                            : (parsedCanaryStages[index - 1]?.candidateWeight ??
                              0)
                        const candidateWeight = Number(stage.candidateWeight)
                        const hasCandidateWeight =
                          stage.candidateWeight.trim() !== ''
                        const canPreviewTrafficSplit =
                          hasCandidateWeight &&
                          Number.isInteger(candidateWeight) &&
                          candidateWeight >= 1 &&
                          candidateWeight <= 100
                        const candidateWeightError = !hasCandidateWeight
                          ? undefined
                          : !Number.isInteger(candidateWeight)
                            ? '流量比例必须是整数百分比。'
                            : candidateWeight <= previousWeight
                              ? `必须高于上一阶段的 ${previousWeight}%。`
                              : candidateWeight > 100
                                ? '流量比例不能超过 100%。'
                                : index === canaryStages.length - 1 &&
                                    candidateWeight !== 100
                                  ? '最后阶段必须设为 100%。'
                                  : index < canaryStages.length - 1 &&
                                      candidateWeight === 100
                                    ? '100% 只能设置在最后阶段。'
                                    : undefined
                        const observationSeconds = Number(
                          stage.observationSeconds
                        )
                        const hasObservationSeconds =
                          stage.observationSeconds.trim() !== ''
                        const observationSecondsError = !hasObservationSeconds
                          ? undefined
                          : !Number.isInteger(observationSeconds)
                            ? '观察时长必须是整数秒。'
                            : observationSeconds < 1 ||
                                observationSeconds > 86400
                              ? '观察时长须为 1–86400 秒。'
                              : undefined
                        const weightErrorID = `canary-stage-weight-${stage.id}-error`
                        const windowErrorID = `canary-stage-window-${stage.id}-error`
                        return (
                          <li
                            key={stage.id}
                            className='rounded-lg border bg-background/80 p-3'
                          >
                            <div className='mb-3 flex items-center justify-between gap-3'>
                              <div className='flex items-center gap-2'>
                                <span className='flex size-6 items-center justify-center rounded-full bg-muted text-xs font-medium tabular-nums'>
                                  {index + 1}
                                </span>
                                <span className='text-sm font-medium'>
                                  第 {index + 1} 阶段
                                </span>
                                {stage.candidateWeight === '100' && (
                                  <Badge variant='outline'>最终阶段</Badge>
                                )}
                              </div>
                              <Button
                                type='button'
                                variant='ghost'
                                size='sm'
                                className='min-h-10 shrink-0 px-2 text-muted-foreground'
                                aria-label={`移除第 ${index + 1} 阶段`}
                                onClick={() =>
                                  setCanaryStagesForm((current) =>
                                    (current ?? canaryStages).filter(
                                      (item) => item.id !== stage.id
                                    )
                                  )
                                }
                              >
                                <X className='size-4' aria-hidden='true' />
                                <span className='sr-only'>移除</span>
                              </Button>
                            </div>
                            <div
                              role='group'
                              className='mb-3 flex min-w-0 items-center gap-3'
                              aria-label={`第 ${index + 1} 阶段流量拆分`}
                            >
                              <div
                                className='flex h-1.5 min-w-12 flex-1 overflow-hidden rounded-full bg-muted'
                                aria-hidden='true'
                              >
                                {canPreviewTrafficSplit && (
                                  <>
                                    <span
                                      className='h-full bg-muted-foreground/35 transition-[width] duration-200 ease-out motion-reduce:transition-none'
                                      style={{
                                        width: `${100 - candidateWeight}%`,
                                      }}
                                    />
                                    <span
                                      className='h-full bg-primary transition-[width] duration-200 ease-out motion-reduce:transition-none'
                                      style={{ width: `${candidateWeight}%` }}
                                    />
                                  </>
                                )}
                              </div>
                              <p className='shrink-0 text-xs leading-5 text-muted-foreground tabular-nums'>
                                {canPreviewTrafficSplit
                                  ? `稳定版 ${100 - candidateWeight}% · 候选版 ${candidateWeight}%`
                                  : '填写比例后预览流量拆分'}
                              </p>
                            </div>
                            <div className='grid gap-3 sm:grid-cols-2'>
                              <div className='space-y-1.5'>
                                <Label
                                  htmlFor={`canary-stage-weight-${stage.id}`}
                                >
                                  候选流量比例
                                </Label>
                                <div className='relative'>
                                  <Input
                                    id={`canary-stage-weight-${stage.id}`}
                                    aria-label={`第 ${index + 1} 阶段候选流量比例`}
                                    aria-invalid={!!candidateWeightError}
                                    aria-describedby={
                                      candidateWeightError
                                        ? weightErrorID
                                        : undefined
                                    }
                                    type='number'
                                    min={1}
                                    max={100}
                                    step={1}
                                    inputMode='numeric'
                                    className='h-10 pe-12'
                                    placeholder='填写 1–100'
                                    value={stage.candidateWeight}
                                    onChange={(event) =>
                                      setCanaryStagesForm((current) =>
                                        (current ?? canaryStages).map((item) =>
                                          item.id === stage.id
                                            ? {
                                                ...item,
                                                candidateWeight:
                                                  event.target.value,
                                              }
                                            : item
                                        )
                                      )
                                    }
                                  />
                                  <span className='pointer-events-none absolute inset-y-0 end-3 flex items-center text-xs text-muted-foreground'>
                                    %
                                  </span>
                                </div>
                                {candidateWeightError && (
                                  <p
                                    id={weightErrorID}
                                    role='alert'
                                    className='text-xs text-destructive'
                                  >
                                    {candidateWeightError}
                                  </p>
                                )}
                              </div>
                              <div className='space-y-1.5'>
                                <Label
                                  htmlFor={`canary-stage-window-${stage.id}`}
                                >
                                  最短观察时长
                                </Label>
                                <div className='relative'>
                                  <Input
                                    id={`canary-stage-window-${stage.id}`}
                                    aria-label={`第 ${index + 1} 阶段最短观察时长`}
                                    aria-invalid={!!observationSecondsError}
                                    aria-describedby={
                                      observationSecondsError
                                        ? windowErrorID
                                        : undefined
                                    }
                                    type='number'
                                    min={1}
                                    max={86400}
                                    step={1}
                                    inputMode='numeric'
                                    className='h-10 pe-16'
                                    placeholder='填写秒数'
                                    value={stage.observationSeconds}
                                    onChange={(event) =>
                                      setCanaryStagesForm((current) =>
                                        (current ?? canaryStages).map((item) =>
                                          item.id === stage.id
                                            ? {
                                                ...item,
                                                observationSeconds:
                                                  event.target.value,
                                              }
                                            : item
                                        )
                                      )
                                    }
                                  />
                                  <span className='pointer-events-none absolute inset-y-0 end-3 flex items-center text-xs text-muted-foreground'>
                                    秒
                                  </span>
                                </div>
                                {observationSecondsError && (
                                  <p
                                    id={windowErrorID}
                                    role='alert'
                                    className='text-xs text-destructive'
                                  >
                                    {observationSecondsError}
                                  </p>
                                )}
                              </div>
                            </div>
                          </li>
                        )
                      })}
                    </ol>
                    <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
                      <p
                        className={`text-xs leading-5 ${canaryStagesValid ? 'text-muted-foreground' : 'text-amber-800 dark:text-amber-200'}`}
                        role='status'
                      >
                        {canaryStagesValid
                          ? '阶段顺序有效；完整观察 100% 候选流量后，仍需操作员确认完成。'
                          : '配置 2–10 个递增阶段，最后一个为 100%；每阶段观察 1–86400 秒。'}
                      </p>
                      <Button
                        type='button'
                        variant='outline'
                        className='min-h-10 shrink-0 self-start sm:self-auto'
                        disabled={canaryStages.length >= 10}
                        onClick={() =>
                          setCanaryStagesForm((current) => [
                            ...(current ?? canaryStages),
                            newCanaryStageDraft(),
                          ])
                        }
                      >
                        <Plus aria-hidden='true' />
                        添加阶段
                      </Button>
                    </div>
                  </fieldset>
                  <p className='rounded-lg border border-amber-300/70 bg-background/80 px-3 py-2 text-xs leading-5 text-amber-950 dark:border-amber-900 dark:text-amber-100'>
                    保存阈值只登记策略草稿，不会调整网关权重或触发发布。真实网关与指标接口接通前，计划仍不能申请审批或执行。
                  </p>
                </div>
              )}
              <div className='flex flex-col gap-2 rounded-lg border bg-muted/20 p-3 sm:flex-row sm:items-center sm:justify-between'>
                <div className='min-w-0'>
                  <p className='text-xs font-medium'>策略版本历史</p>
                  <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                    {binding?.id
                      ? policyQuery.isPending
                        ? '正在读取当前版本…'
                        : policyQuery.isError
                          ? '版本历史暂时无法读取；刷新后再试，当前绑定不会保存。'
                          : policyQuery.data?.current_version
                            ? `当前 v${policyQuery.data.current_version} · 共 ${policyQuery.data.versions.length} 个不可变版本`
                            : '当前尚无策略版本，保存后会创建 v1。'
                      : '保存时会同时创建第一个策略版本。'}
                  </p>
                </div>
                {policyQuery.data?.current_version ? (
                  <Badge variant='outline' className='self-start sm:self-auto'>
                    v{policyQuery.data.current_version}
                  </Badge>
                ) : (
                  <Badge variant='outline' className='self-start sm:self-auto'>
                    {binding?.id ? '待创建' : 'v1'}
                  </Badge>
                )}
              </div>
              {!!policyQuery.data?.versions.length && (
                <ol
                  aria-label='最近策略版本'
                  className='max-h-36 space-y-1.5 overflow-y-auto rounded-lg border bg-card p-3'
                >
                  {[...policyQuery.data.versions]
                    .reverse()
                    .slice(0, 3)
                    .map((version) => (
                      <li
                        key={version.version}
                        className='flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs'
                      >
                        <span className='min-w-0 font-medium break-all'>
                          v{version.version} ·{' '}
                          {version.strategy === 'blue_green'
                            ? '蓝绿发布'
                            : version.strategy === 'gateway_canary'
                              ? '网关金丝雀草稿'
                              : '滚动更新'}
                          {version.service_name && ` · ${version.service_name}`}
                          {version.candidate_retention_hours
                            ? ` · 保留 ${version.candidate_retention_hours} 小时`
                            : ''}
                        </span>
                        <span className='text-muted-foreground'>
                          {version.created_by}
                        </span>
                      </li>
                    ))}
                </ol>
              )}
              {currentPolicy?.readiness === 'integration_pending' && (
                <p className='rounded-lg border border-amber-300 bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-200'>
                  当前版本保存为金丝雀草稿。网关权重接口完成联调前，发布计划不会开放审批和执行。
                </p>
              )}
              {strategyDraft === 'blue_green' && (
                <div className='space-y-2 rounded-lg border bg-card p-3'>
                  <Label htmlFor='blue-green-retention-hours'>
                    恢复后保留候选版本
                  </Label>
                  <div className='flex items-center gap-2'>
                    <Input
                      id='blue-green-retention-hours'
                      aria-label='蓝绿候选保留小时'
                      type='number'
                      min={1}
                      max={720}
                      step={1}
                      className='h-10 w-28'
                      value={candidateRetentionHours}
                      onChange={(event) =>
                        setRetentionOverride(
                          Number.parseInt(event.target.value, 10) || 0
                        )
                      }
                    />
                    <span className='text-sm text-muted-foreground'>小时</span>
                  </div>
                  <p className='text-xs leading-5 text-muted-foreground'>
                    仅在稳定版本恢复后允许清理候选资源；计划会固定此值，范围
                    1–720 小时。
                  </p>
                </div>
              )}
            </fieldset>
            {app.deployment_name && (
              <div className='sm:col-span-2'>
                <StrategyCapabilityPreview
                  clusterId={app.cluster_id}
                  namespace={app.namespace}
                  deploymentName={app.deployment_name}
                  selectedService={serviceName}
                  onServiceNameChange={
                    strategyDraft === 'blue_green'
                      ? setServiceNameOverride
                      : undefined
                  }
                />
              </div>
            )}
          </div>

          {!!applicationID && (
            <div className='rounded-lg border bg-muted/30 p-4'>
              <p className='text-sm font-medium'>此业务应用没有这个组件？</p>
              <div className='mt-3 flex flex-col gap-2 sm:flex-row'>
                <Input
                  aria-label='新组件名称'
                  maxLength={128}
                  value={newComponentName}
                  placeholder='新组件名称，例如 order-api'
                  onChange={(event) => setNewComponentName(event.target.value)}
                />
                <Button
                  type='button'
                  variant='outline'
                  className='shrink-0'
                  disabled={
                    !newComponentName.trim() || createComponent.isPending
                  }
                  onClick={() => createComponent.mutate()}
                >
                  {createComponent.isPending ? '创建中…' : '创建组件'}
                </Button>
              </div>
              {createComponent.isError && (
                <p role='alert' className='mt-2 text-sm text-destructive'>
                  {errorMessage(createComponent.error)}
                </p>
              )}
            </div>
          )}

          <div className='rounded-lg border p-4'>
            <p className='text-xs font-medium text-muted-foreground'>
              对应关系预览
            </p>
            <p className='mt-2 text-sm font-medium break-words'>
              {model.applications.find(
                (row) => row.id === Number(applicationID)
              )?.name || '选择业务应用'}
              {' / '}
              {model.components.find((row) => row.id === Number(componentID))
                ?.name || '选择组件'}
              {' → '}
              {app.image_repository || '镜像仓库待配置'}
            </p>
            <p className='mt-1 text-xs text-muted-foreground'>
              {model.environments.find(
                (row) => row.id === Number(environmentID)
              )?.name || '选择环境'}
              {' → '}
              {clusterName || `集群 #${app.cluster_id}`} / {app.namespace} /{' '}
              {app.deployment_name} / {app.container_name}
            </p>
          </div>
          {mutation.isError && (
            <p role='alert' className='text-sm text-destructive'>
              {errorMessage(mutation.error)}
            </p>
          )}
        </div>
        <DialogFooter className='flex-col border-t px-6 py-4 sm:flex-row'>
          {strategyDraft === 'gateway_canary' && !canaryAnalysis && (
            <p className='text-xs leading-5 text-muted-foreground sm:me-auto sm:max-w-sm sm:pe-4'>
              {canaryGatewayRouteError ??
                '填写全部阈值和至少两个递增阶段，最后一个为 100%，才能保存。'}
            </p>
          )}
          <Button
            variant='outline'
            className='min-h-11'
            onClick={onClose}
            disabled={mutation.isPending}
          >
            取消
          </Button>
          <Button
            className='min-h-11'
            onClick={() => mutation.mutate()}
            disabled={
              !applicationID ||
              !componentID ||
              !environmentID ||
              !deploymentPipelineValid ||
              (!!binding?.id &&
                (policyQuery.isPending || policyQuery.isError)) ||
              (strategyDraft === 'blue_green' && !serviceName) ||
              (strategyDraft === 'blue_green' &&
                (candidateRetentionHours < 1 ||
                  candidateRetentionHours > 720)) ||
              (strategyDraft === 'gateway_canary' && !canaryAnalysis) ||
              mutation.isPending
            }
          >
            {mutation.isPending ? '保存中…' : '保存绑定与策略'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function EnvironmentsPage({
  initialAppId,
}: {
  initialAppId?: number
} = {}) {
  const queryClient = useQueryClient()
  const role = useAuthStore((state) => state.auth.user?.role)
  const canConfigure = role === 'admin' || role === 'operator'
  const isAdmin = role === 'admin'
  const modelQuery = useQuery({
    queryKey: ['delivery-model'],
    queryFn: async () => (await platformApi.deliveryModel()).data,
  })
  const appsQuery = useQuery({
    queryKey: ['apps'],
    queryFn: async () => (await platformApi.apps()).data,
  })
  const clustersQuery = useQuery({
    queryKey: ['clusters'],
    queryFn: async () => (await platformApi.clusters()).data,
  })
  const [environmentDialog, setEnvironmentDialog] = useState<
    DeliveryEnvironment | 'create' | null
  >(null)
  const [evidencePolicyDialog, setEvidencePolicyDialog] =
    useState<DeliveryEnvironment | null>(null)
  const [applicationDialog, setApplicationDialog] = useState(false)
  const [mappingDialog, setMappingDialog] = useState<{
    app: ComponentBinding
    binding?: DeliveryBinding
  } | null>(null)
  const [dismissedGuidedAppId, setDismissedGuidedAppId] = useState<number>()
  const [rebindTarget, setRebindTarget] = useState<{
    appID: number
    appName: string
    clusterName: string
    namespace: string
    deploymentName: string
  } | null>(null)
  const rebindMutation = useMutation({
    mutationFn: (appID: number) => platformApi.rebindDeliveryWorkload(appID),
    onSuccess: () => {
      toast.success('活动 Deployment 身份已重新核对')
      setRebindTarget(null)
      void queryClient.invalidateQueries({ queryKey: ['delivery-model'] })
    },
    onError: (error) => toast.error(errorMessage(error)),
  })
  const model = modelQuery.data
  const apps = appsQuery.data ?? EMPTY_COMPONENT_BINDINGS
  const boundApps = useMemo(() => {
    const appByID = new Map(apps.map((app) => [app.id, app]))
    return Array.from(
      new Map(
        (model?.bindings ?? [])
          .map((binding) => appByID.get(binding.app_id))
          .filter((app): app is ComponentBinding => !!app)
          .map((app) => [app.id, app])
      ).values()
    )
  }, [apps, model?.bindings])
  const driftAppIDsByCluster = useMemo(() => {
    const appIDsByCluster = new Map<number, number[]>()
    for (const app of boundApps) {
      const appIDs = appIDsByCluster.get(app.cluster_id) ?? []
      appIDs.push(app.id)
      appIDsByCluster.set(app.cluster_id, appIDs)
    }
    for (const appIDs of appIDsByCluster.values()) appIDs.sort((a, b) => a - b)
    return appIDsByCluster
  }, [boundApps])
  const driftClusterIDs = useMemo(
    () => Array.from(driftAppIDsByCluster.keys()),
    [driftAppIDsByCluster]
  )
  const driftQueries = useQueries({
    queries: driftClusterIDs.map((clusterID) => ({
      queryKey: [
        'delivery-drift',
        clusterID,
        driftAppIDsByCluster.get(clusterID),
      ],
      queryFn: async () =>
        (
          await platformApi.deliveryDrift(
            clusterID,
            driftAppIDsByCluster.get(clusterID)
          )
        ).data,
      enabled: appsQuery.isSuccess && modelQuery.isSuccess,
      staleTime: 2 * 60_000,
      retry: false,
    })),
  })
  const latestDeployQueries = useQueries({
    queries: boundApps.map((app) => ({
      queryKey: ['app-latest-deploy-log', app.cluster_id, app.app_name],
      queryFn: async () => {
        const response = await platformApi.logs({
          cluster_id: app.cluster_id,
          app_name: app.app_name,
        })
        return response.data[0] ?? null
      },
      enabled: appsQuery.isSuccess && modelQuery.isSuccess,
      staleTime: 10_000,
      retry: false,
      refetchInterval: (query: { state: { data?: DeployLog | null } }) =>
        query.state.data?.status === 'pending' ||
        query.state.data?.status === 'running'
          ? 5_000
          : false,
    })),
  })
  const releaseQueries = useQueries({
    queries: boundApps.map((app) => ({
      queryKey: ['app-image-releases', app.app_name],
      queryFn: async () =>
        (await platformApi.releases({ app_name: app.app_name, limit: 50 }))
          .data,
      enabled: appsQuery.isSuccess && modelQuery.isSuccess,
      staleTime: 60_000,
      retry: false,
    })),
  })
  const driftQueryByClusterID = new Map(
    driftClusterIDs.map((clusterID, index) => [clusterID, driftQueries[index]])
  )
  const latestDeployQueryByAppID = new Map(
    boundApps.map((app, index) => [app.id, latestDeployQueries[index]])
  )
  const releaseQueryByAppID = new Map(
    boundApps.map((app, index) => [app.id, releaseQueries[index]])
  )
  const guidedApp =
    initialAppId && dismissedGuidedAppId !== initialAppId
      ? apps.find((row) => row.id === initialAppId)
      : undefined
  const guidedBinding = guidedApp
    ? model?.bindings.find((row) => row.app_id === guidedApp.id)
    : undefined
  const activeMappingDialog =
    mappingDialog ??
    (guidedApp && modelQuery.isSuccess && clustersQuery.isSuccess
      ? { app: guidedApp, binding: guidedBinding }
      : null)
  const closeMappingDialog = () => {
    setMappingDialog(null)
    if (initialAppId) setDismissedGuidedAppId(initialAppId)
  }
  const clusterRows = clustersQuery.data
  const clusterByID = useMemo(
    () => new Map((clusterRows ?? []).map((cluster) => [cluster.id, cluster])),
    [clusterRows]
  )
  const bindingByAppID = useMemo(
    () =>
      new Map(
        model?.bindings.map((binding) => [binding.app_id, binding]) ?? []
      ),
    [model?.bindings]
  )
  const refreshModel = () => {
    void queryClient.invalidateQueries({ queryKey: ['delivery-model'] })
    void queryClient.invalidateQueries({ queryKey: ['apps'] })
  }
  const loading =
    modelQuery.isPending || appsQuery.isPending || clustersQuery.isPending
  const failed =
    modelQuery.isError || appsQuery.isError || clustersQuery.isError
  const pendingReviews =
    model?.bindings.filter((binding) => binding.requires_review).length ?? 0
  const unassigned = apps.filter((app) => !bindingByAppID.has(app.id)).length
  const runtimeRefreshing =
    driftQueries.some((query) => query.isFetching) ||
    latestDeployQueries.some((query) => query.isFetching) ||
    releaseQueries.some((query) => query.isFetching)
  const refreshRuntime = () => {
    driftQueries.forEach((query) => void query.refetch())
    latestDeployQueries.forEach((query) => void query.refetch())
    releaseQueries.forEach((query) => void query.refetch())
  }

  return (
    <>
      <Header fixed>
        <div className='me-auto font-semibold'>环境与发布目标</div>
        <ThemeSwitch />
      </Header>
      <Main>
        <div className='mb-7 flex flex-wrap items-start justify-between gap-4'>
          <div className='max-w-3xl'>
            <div className='mb-2 flex items-center gap-2 text-sm text-muted-foreground'>
              <Workflow className='size-4' />
              发布配置
            </div>
            <h1 className='text-2xl font-semibold tracking-tight'>
              环境与发布目标
            </h1>
            <p className='mt-2 text-sm leading-6 text-muted-foreground'>
              环境表达发布阶段，集群承载实际资源。先确认旧绑定，再把组件明确关联到业务应用和环境。
            </p>
          </div>
          <div className='flex flex-wrap gap-2'>
            {canConfigure && (
              <Button
                variant='outline'
                onClick={() => setApplicationDialog(true)}
              >
                <Layers3 />
                新建业务应用
              </Button>
            )}
            {isAdmin && (
              <Button onClick={() => setEnvironmentDialog('create')}>
                <Plus />
                新建环境
              </Button>
            )}
          </div>
        </div>

        <div
          data-testid='environment-overview-summary'
          className='mb-5 grid grid-cols-3 gap-2 sm:mb-7 sm:gap-3'
        >
          <SummaryCard
            label='逻辑环境'
            value={model?.environments.length ?? '—'}
            icon={Workflow}
          />
          <SummaryCard
            label='组件身份'
            value={model?.components.length ?? '—'}
            icon={Layers3}
          />
          <SummaryCard
            label='待核对绑定'
            value={pendingReviews + unassigned}
            icon={RefreshCw}
            attention={pendingReviews + unassigned > 0}
          />
        </div>

        {loading ? (
          <div className='space-y-3' aria-label='正在加载环境'>
            {[0, 1].map((item) => (
              <div
                key={item}
                className='h-28 animate-pulse rounded-xl border bg-muted/40'
              />
            ))}
          </div>
        ) : failed ? (
          <Card>
            <CardContent className='flex flex-col items-start gap-3 p-6'>
              <div>
                <h2 className='font-medium'>无法读取环境与组件关系</h2>
                <p className='mt-1 text-sm text-muted-foreground'>
                  请检查后端和 010 环境模型迁移。
                </p>
              </div>
              <Button
                variant='outline'
                onClick={() => {
                  void modelQuery.refetch()
                  void appsQuery.refetch()
                  void clustersQuery.refetch()
                }}
              >
                <RefreshCw />
                重新加载
              </Button>
            </CardContent>
          </Card>
        ) : (
          <>
            <section aria-labelledby='environments-heading' className='mb-9'>
              <div className='mb-4 flex flex-wrap items-end justify-between gap-3'>
                <div>
                  <h2
                    id='environments-heading'
                    className='text-lg font-semibold tracking-tight'
                  >
                    发布环境
                  </h2>
                  <p className='mt-1 text-sm text-muted-foreground'>
                    一个环境可以关联多个集群目标；生产属性与集群保持一致。
                  </p>
                </div>
                {!!boundApps.length && (
                  <Button
                    type='button'
                    size='sm'
                    variant='outline'
                    className='min-h-11 sm:min-h-9'
                    disabled={runtimeRefreshing}
                    onClick={refreshRuntime}
                  >
                    <RefreshCw
                      className={runtimeRefreshing ? 'animate-spin' : undefined}
                      aria-hidden='true'
                    />
                    {runtimeRefreshing ? '正在刷新' : '刷新运行状态'}
                  </Button>
                )}
              </div>
              {!model?.environments.length ? (
                <Card>
                  <CardContent className='p-6'>
                    <p className='font-medium'>还没有逻辑环境</p>
                    <p className='mt-1 text-sm text-muted-foreground'>
                      管理员可以先创建测试、预发或生产环境，再关联已有目标。
                    </p>
                  </CardContent>
                </Card>
              ) : (
                <div
                  className={`grid gap-4 ${model.environments.length === 1 ? 'max-w-4xl' : 'xl:grid-cols-2'}`}
                >
                  {model.environments.map((environment) => {
                    const bindings = model.bindings.filter(
                      (binding) => binding.environment_id === environment.id
                    )
                    const targetRows = bindings.flatMap((binding) => {
                      const app = apps.find((row) => row.id === binding.app_id)
                      if (!app) return []
                      const cluster = clusterByID.get(app.cluster_id)
                      const driftQuery = driftQueryByClusterID.get(
                        app.cluster_id
                      )
                      const observation = driftQuery?.data?.find(
                        (row) =>
                          row.app_name === app.app_name &&
                          row.namespace === app.namespace &&
                          row.workload === app.deployment_name &&
                          row.container === app.container_name
                      )
                      const latestDeployQuery = latestDeployQueryByAppID.get(
                        app.id
                      )
                      const releaseQuery = releaseQueryByAppID.get(app.id)
                      return [
                        {
                          app,
                          cluster,
                          binding,
                          observation,
                          observationPending: driftQuery?.isPending ?? true,
                          observationError: driftQuery?.isError ?? false,
                          latestDeploy: latestDeployQuery?.data,
                          latestDeployPending:
                            latestDeployQuery?.isPending ?? true,
                          latestDeployError:
                            latestDeployQuery?.isError ?? false,
                          releases: releaseQuery?.data,
                          releasesPending: releaseQuery?.isPending ?? true,
                          releasesError: releaseQuery?.isError ?? false,
                        },
                      ]
                    })
                    const clusterCount = new Set(
                      targetRows.map((row) => row.app.cluster_id)
                    ).size
                    return (
                      <Card key={environment.id} className='overflow-hidden'>
                        <CardContent className='p-0'>
                          <div className='flex flex-wrap items-start justify-between gap-4 p-5'>
                            <div className='min-w-0'>
                              <div className='flex flex-wrap items-center gap-2'>
                                <h3 className='font-semibold break-words'>
                                  {environment.name}
                                </h3>
                                <Badge variant='outline'>
                                  {environment.environment_tier === 'production'
                                    ? '生产'
                                    : '非生产'}
                                </Badge>
                                {environment.requires_review && (
                                  <Badge className='border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200'>
                                    待核对
                                  </Badge>
                                )}
                                {environment.environment_tier ===
                                  'production' && (
                                  <Badge
                                    variant='outline'
                                    className={
                                      environment.evidence_policy_version
                                        ? 'border-emerald-300 text-emerald-800 dark:border-emerald-800 dark:text-emerald-200'
                                        : 'border-amber-300 text-amber-900 dark:border-amber-800 dark:text-amber-200'
                                    }
                                  >
                                    {environment.evidence_policy_version
                                      ? `验证策略 v${environment.evidence_policy_version}`
                                      : '待配置验证策略'}
                                  </Badge>
                                )}
                              </div>
                              <p className='mt-2 text-sm text-muted-foreground'>
                                {bindings.length} 个组件绑定 · {clusterCount}{' '}
                                个集群目标
                              </p>
                            </div>
                            <div className='flex flex-wrap gap-2'>
                              {environment.environment_tier === 'production' &&
                                isAdmin && (
                                  <Button
                                    size='sm'
                                    variant='outline'
                                    onClick={() =>
                                      setEvidencePolicyDialog(environment)
                                    }
                                  >
                                    配置验证规则
                                  </Button>
                                )}
                              {environment.requires_review && isAdmin && (
                                <Button
                                  size='sm'
                                  variant='outline'
                                  onClick={() =>
                                    setEnvironmentDialog(environment)
                                  }
                                >
                                  <Check />
                                  核对环境
                                </Button>
                              )}
                            </div>
                          </div>
                          <div className='border-t bg-muted/20 px-5 py-3'>
                            {targetRows.length ? (
                              <ul className='space-y-2'>
                                {targetRows.slice(0, 4).map((target) => (
                                  <li
                                    key={target.app.id}
                                    className='flex min-w-0 items-start gap-2 text-sm'
                                  >
                                    <Server className='mt-0.5 size-4 shrink-0 text-muted-foreground' />
                                    <div className='min-w-0 flex-1'>
                                      <p className='break-words'>
                                        <span className='font-medium'>
                                          {target.app.app_name}
                                        </span>
                                        <span className='text-muted-foreground'>
                                          {' '}
                                          ·{' '}
                                          {target.cluster?.name ??
                                            `集群 #${target.app.cluster_id}`}{' '}
                                          / {target.app.namespace} /{' '}
                                          {target.app.deployment_name}
                                        </span>
                                      </p>
                                      <TargetRuntimeSummary
                                        app={target.app}
                                        observation={target.observation}
                                        observationPending={
                                          target.observationPending
                                        }
                                        observationError={
                                          target.observationError
                                        }
                                        latestDeploy={target.latestDeploy}
                                        latestDeployPending={
                                          target.latestDeployPending
                                        }
                                        latestDeployError={
                                          target.latestDeployError
                                        }
                                        releases={target.releases}
                                        releasesPending={target.releasesPending}
                                        releasesError={target.releasesError}
                                      />
                                    </div>
                                  </li>
                                ))}
                                {targetRows.length > 4 && (
                                  <li className='pl-6 text-xs text-muted-foreground'>
                                    另有 {targetRows.length - 4} 个目标
                                  </li>
                                )}
                              </ul>
                            ) : (
                              <p className='text-sm text-muted-foreground'>
                                尚未关联组件
                              </p>
                            )}
                          </div>
                        </CardContent>
                      </Card>
                    )
                  })}
                </div>
              )}
            </section>

            <section aria-labelledby='bindings-heading'>
              <div className='mb-4'>
                <h2
                  id='bindings-heading'
                  className='text-lg font-semibold tracking-tight'
                >
                  组件与环境对应关系
                </h2>
                <p className='mt-1 text-sm text-muted-foreground'>
                  代码来源、镜像仓库和实际容器分开核对，避免同名目标被自动合并。
                </p>
              </div>
              <Card className='overflow-hidden'>
                <div className='divide-y xl:hidden'>
                  {!apps.length ? (
                    <div className='p-6 text-center'>
                      <p className='font-medium'>还没有组件绑定</p>
                      <p className='mt-1 text-sm text-muted-foreground'>
                        先到应用与组件完成接入。
                      </p>
                      <a
                        className='mt-3 inline-block text-sm text-primary underline underline-offset-4'
                        href='/apps'
                      >
                        前往接入组件
                      </a>
                    </div>
                  ) : (
                    apps.map((app) => {
                      const binding = bindingByAppID.get(app.id)
                      const application = model?.applications.find(
                        (item) => item.id === binding?.application_id
                      )
                      const component = model?.components.find(
                        (item) => item.id === binding?.component_id
                      )
                      const environment = model?.environments.find(
                        (item) => item.id === binding?.environment_id
                      )
                      const cluster = clusterByID.get(app.cluster_id)
                      return (
                        <article key={app.id} className='p-4 sm:p-5'>
                          <div className='flex items-start justify-between gap-3'>
                            <div className='min-w-0'>
                              <h3 className='font-semibold break-words'>
                                {application?.name ?? app.app_name} /{' '}
                                {component?.name ?? app.app_name}
                              </h3>
                              <p className='mt-1 text-xs text-muted-foreground'>
                                {app.app_name}
                              </p>
                            </div>
                            {!binding ? (
                              <StatusPill>待关联</StatusPill>
                            ) : binding.requires_review ? (
                              <StatusPill>待核对</StatusPill>
                            ) : (
                              <StatusPill positive>已关联</StatusPill>
                            )}
                          </div>
                          {binding?.strategy_draft && (
                            <div className='mt-2'>
                              <Badge variant='outline'>
                                {binding.strategy_draft === 'blue_green'
                                  ? '蓝绿策略草稿'
                                  : '网关金丝雀草稿'}
                              </Badge>
                            </div>
                          )}
                          <dl className='mt-4 space-y-3 text-sm'>
                            <div className='min-w-0'>
                              <dt className='text-xs font-medium text-muted-foreground'>
                                代码来源
                              </dt>
                              <dd className='mt-1 break-all'>
                                {componentSourceLabel(
                                  app.source_kind,
                                  app.source_repo_url
                                )}
                                {app.source_kind === 'repository' &&
                                  app.source_repo_url &&
                                  ` · ${app.source_path || '.'}`}
                              </dd>
                            </div>
                            <div className='min-w-0'>
                              <dt className='text-xs font-medium text-muted-foreground'>
                                镜像仓库
                              </dt>
                              <dd className='mt-1 font-mono text-xs break-all'>
                                {app.image_repository || '镜像仓库待配置'}
                              </dd>
                            </div>
                            <div className='min-w-0'>
                              <dt className='text-xs font-medium text-muted-foreground'>
                                逻辑环境 / 实际目标
                              </dt>
                              <dd className='mt-1 break-words'>
                                {environment?.name ?? '环境待关联'} ·{' '}
                                {cluster?.name ?? `集群 #${app.cluster_id}`} /{' '}
                                {app.namespace} / {app.container_name}
                              </dd>
                              {binding && (
                                <div className='mt-2 space-y-1 rounded-md bg-muted/40 px-2.5 py-2 text-xs'>
                                  <p className='break-all'>
                                    <span className='font-medium'>
                                      活动 Deployment
                                    </span>
                                    {' · '}
                                    {binding.active_workload_ref?.name ||
                                      app.deployment_name}
                                    <span className='text-muted-foreground'>
                                      {' · UID '}
                                      {binding.active_workload_ref?.uid
                                        ? binding.active_workload_ref.uid.slice(
                                            0,
                                            8
                                          )
                                        : '首次计划时确认'}
                                    </span>
                                  </p>
                                  {binding.template_workload_ref?.name &&
                                    (binding.template_workload_ref.name !==
                                      binding.active_workload_ref?.name ||
                                      binding.template_workload_ref.uid !==
                                        binding.active_workload_ref?.uid) && (
                                      <p className='break-all text-muted-foreground'>
                                        初始资源 ·{' '}
                                        {binding.template_workload_ref.name}
                                        {binding.template_workload_ref.uid &&
                                          ` · UID ${binding.template_workload_ref.uid.slice(0, 8)}`}
                                      </p>
                                    )}
                                </div>
                              )}
                            </div>
                          </dl>
                          {canConfigure && (
                            <div className='mt-4 grid grid-cols-2 gap-2'>
                              {binding && (
                                <Button
                                  size='sm'
                                  variant='ghost'
                                  className='min-h-11 px-2'
                                  disabled={rebindMutation.isPending}
                                  onClick={() =>
                                    setRebindTarget({
                                      appID: app.id,
                                      appName: app.app_name,
                                      clusterName:
                                        cluster?.name ??
                                        `集群 #${app.cluster_id}`,
                                      namespace: app.namespace,
                                      deploymentName: app.deployment_name,
                                    })
                                  }
                                >
                                  <RefreshCw />
                                  重新核对 UID
                                </Button>
                              )}
                              <Button
                                size='sm'
                                variant='outline'
                                className='min-h-11 px-2'
                                onClick={() =>
                                  setMappingDialog({ app, binding })
                                }
                              >
                                {binding?.requires_review || !binding
                                  ? '核对关系'
                                  : '编辑关系'}
                              </Button>
                            </div>
                          )}
                        </article>
                      )
                    })
                  )}
                </div>
                <CardContent className='hidden overflow-x-auto p-0 xl:block'>
                  <table className='w-full min-w-[800px] text-sm'>
                    <thead className='bg-muted/40 text-muted-foreground'>
                      <tr>
                        <th
                          scope='col'
                          className='px-5 py-3 text-left font-medium'
                        >
                          组件与来源
                        </th>
                        <th
                          scope='col'
                          className='px-5 py-3 text-left font-medium'
                        >
                          镜像仓库
                        </th>
                        <th
                          scope='col'
                          className='px-5 py-3 text-left font-medium'
                        >
                          逻辑环境 / 实际目标
                        </th>
                        <th
                          scope='col'
                          className='px-5 py-3 text-left font-medium'
                        >
                          状态
                        </th>
                        <th
                          scope='col'
                          className='px-5 py-3 text-right font-medium'
                        >
                          操作
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {!apps.length ? (
                        <tr>
                          <td colSpan={5} className='px-5 py-10 text-center'>
                            <p className='font-medium'>还没有组件绑定</p>
                            <p className='mt-1 text-muted-foreground'>
                              先到应用与组件完成接入。
                            </p>
                            <a
                              className='mt-3 inline-block text-primary underline underline-offset-4'
                              href='/apps'
                            >
                              前往接入组件
                            </a>
                          </td>
                        </tr>
                      ) : (
                        apps.map((app) => {
                          const binding = bindingByAppID.get(app.id)
                          const application = model?.applications.find(
                            (item) => item.id === binding?.application_id
                          )
                          const component = model?.components.find(
                            (item) => item.id === binding?.component_id
                          )
                          const environment = model?.environments.find(
                            (item) => item.id === binding?.environment_id
                          )
                          const cluster = clusterByID.get(app.cluster_id)
                          return (
                            <tr key={app.id} className='border-t align-top'>
                              <td className='max-w-64 px-5 py-4'>
                                <p className='font-medium'>
                                  {application?.name ?? app.app_name} /{' '}
                                  {component?.name ?? app.app_name}
                                </p>
                                <p className='mt-1 text-xs break-all text-muted-foreground'>
                                  {componentSourceLabel(
                                    app.source_kind,
                                    app.source_repo_url
                                  )}
                                  {app.source_kind === 'repository' &&
                                    app.source_repo_url &&
                                    ` · ${app.source_path || '.'}`}
                                </p>
                              </td>
                              <td className='max-w-64 px-5 py-4 font-mono text-xs break-all'>
                                {app.image_repository || '镜像仓库待配置'}
                              </td>
                              <td className='px-5 py-4'>
                                <p>{environment?.name ?? '环境待关联'}</p>
                                <p className='mt-1 text-xs text-muted-foreground'>
                                  {cluster?.name ?? `集群 #${app.cluster_id}`} /{' '}
                                  {app.namespace} / {app.container_name}
                                </p>
                                {binding && (
                                  <div className='mt-2 space-y-1 rounded-md bg-muted/40 px-2.5 py-2 text-xs'>
                                    <p className='break-all'>
                                      <span className='font-medium'>
                                        活动 Deployment
                                      </span>
                                      {' · '}
                                      {binding.active_workload_ref?.name ||
                                        app.deployment_name}
                                      <span className='text-muted-foreground'>
                                        {' · UID '}
                                        {binding.active_workload_ref?.uid
                                          ? binding.active_workload_ref.uid.slice(
                                              0,
                                              8
                                            )
                                          : '首次计划时确认'}
                                      </span>
                                    </p>
                                    {binding.template_workload_ref?.name &&
                                      (binding.template_workload_ref.name !==
                                        binding.active_workload_ref?.name ||
                                        binding.template_workload_ref.uid !==
                                          binding.active_workload_ref?.uid) && (
                                        <p className='break-all text-muted-foreground'>
                                          初始资源 ·{' '}
                                          {binding.template_workload_ref.name}
                                          {binding.template_workload_ref.uid &&
                                            ` · UID ${binding.template_workload_ref.uid.slice(0, 8)}`}
                                        </p>
                                      )}
                                  </div>
                                )}
                              </td>
                              <td className='px-5 py-4'>
                                {!binding ? (
                                  <StatusPill>待关联</StatusPill>
                                ) : binding.requires_review ? (
                                  <StatusPill>待核对</StatusPill>
                                ) : (
                                  <StatusPill positive>已关联</StatusPill>
                                )}
                                {binding?.strategy_draft && (
                                  <p className='mt-2'>
                                    <Badge variant='outline'>
                                      {binding.strategy_draft === 'blue_green'
                                        ? '蓝绿策略草稿'
                                        : '网关金丝雀草稿'}
                                    </Badge>
                                  </p>
                                )}
                              </td>
                              <td className='px-5 py-4 text-right'>
                                {canConfigure && (
                                  <div className='flex flex-wrap justify-end gap-2'>
                                    {binding && (
                                      <Button
                                        size='sm'
                                        variant='ghost'
                                        disabled={rebindMutation.isPending}
                                        onClick={() =>
                                          setRebindTarget({
                                            appID: app.id,
                                            appName: app.app_name,
                                            clusterName:
                                              cluster?.name ??
                                              `集群 #${app.cluster_id}`,
                                            namespace: app.namespace,
                                            deploymentName: app.deployment_name,
                                          })
                                        }
                                      >
                                        <RefreshCw />
                                        重新核对 UID
                                      </Button>
                                    )}
                                    <Button
                                      size='sm'
                                      variant='outline'
                                      onClick={() =>
                                        setMappingDialog({ app, binding })
                                      }
                                    >
                                      {binding?.requires_review || !binding
                                        ? '核对关系'
                                        : '编辑关系'}
                                    </Button>
                                  </div>
                                )}
                              </td>
                            </tr>
                          )
                        })
                      )}
                    </tbody>
                  </table>
                </CardContent>
              </Card>
            </section>
          </>
        )}
      </Main>

      {environmentDialog && (
        <EnvironmentDialog
          environment={
            environmentDialog === 'create' ? undefined : environmentDialog
          }
          onClose={() => setEnvironmentDialog(null)}
          onSaved={refreshModel}
        />
      )}
      {evidencePolicyDialog && (
        <EnvironmentEvidencePolicyDialog
          environment={evidencePolicyDialog}
          onClose={() => setEvidencePolicyDialog(null)}
          onSaved={refreshModel}
        />
      )}
      {applicationDialog && (
        <ApplicationDialog
          onClose={() => setApplicationDialog(false)}
          onSaved={refreshModel}
        />
      )}
      {activeMappingDialog && model && (
        <BindingDialog
          app={activeMappingDialog.app}
          binding={activeMappingDialog.binding}
          model={model}
          clusterName={
            clusterByID.get(activeMappingDialog.app.cluster_id)?.name
          }
          environmentTier={
            clusterByID.get(activeMappingDialog.app.cluster_id)
              ?.environment_tier
          }
          onClose={closeMappingDialog}
          onSaved={refreshModel}
        />
      )}
      {rebindTarget && (
        <AlertDialog
          open
          onOpenChange={(open) => {
            if (!open && !rebindMutation.isPending) setRebindTarget(null)
          }}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>重新核对活动 Deployment UID？</AlertDialogTitle>
              <AlertDialogDescription>
                确认目标无误后，平台会读取 Kubernetes 中的当前
                UID，并更新平台里的活动资源引用。
              </AlertDialogDescription>
            </AlertDialogHeader>
            <div className='rounded-lg border bg-muted/30 p-3 text-sm'>
              <p className='text-xs text-muted-foreground'>业务应用</p>
              <p className='mt-1 font-medium'>{rebindTarget.appName}</p>
              <div className='mt-3 border-t pt-3'>
                <p className='text-xs text-muted-foreground'>目标 Deployment</p>
                <p className='mt-1 font-mono text-xs leading-5 break-all text-muted-foreground'>
                  {rebindTarget.clusterName} / {rebindTarget.namespace} /{' '}
                  {rebindTarget.deploymentName}
                </p>
              </div>
            </div>
            <div className='flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground'>
              <span>只读 Kubernetes</span>
              <span>不切换 Service 流量</span>
              <span>保留初始模板</span>
              <span>发布占用时拒绝</span>
            </div>
            {rebindMutation.isError && (
              <p role='alert' className='text-sm text-destructive'>
                {errorMessage(rebindMutation.error)}
              </p>
            )}
            <AlertDialogFooter>
              <AlertDialogCancel disabled={rebindMutation.isPending}>
                取消
              </AlertDialogCancel>
              <Button
                disabled={rebindMutation.isPending}
                onClick={() => rebindMutation.mutate(rebindTarget.appID)}
              >
                {rebindMutation.isPending ? '读取中…' : '读取并更新绑定'}
              </Button>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      )}
    </>
  )
}

function SummaryCard({
  label,
  value,
  icon: Icon,
  attention = false,
}: {
  label: string
  value: string | number
  icon: typeof Workflow
  attention?: boolean
}) {
  return (
    <Card>
      <CardContent className='flex min-h-[4.25rem] min-w-0 items-center justify-between gap-1.5 p-2.5 sm:min-h-0 sm:gap-4 sm:p-4'>
        <div className='min-w-0'>
          <p className='text-xs leading-4 text-muted-foreground sm:text-sm'>
            {label}
          </p>
          <p
            className={`mt-1 text-xl font-semibold tabular-nums sm:text-2xl ${attention ? 'text-amber-700 dark:text-amber-300' : ''}`}
          >
            {value}
          </p>
        </div>
        <div className='hidden shrink-0 rounded-lg border bg-muted/40 p-2.5 text-muted-foreground sm:block'>
          <Icon className='size-5' aria-hidden='true' />
        </div>
      </CardContent>
    </Card>
  )
}

function StatusPill({
  children,
  positive = false,
}: {
  children: React.ReactNode
  positive?: boolean
}) {
  return (
    <Badge
      variant='outline'
      className={
        positive
          ? 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950 dark:text-emerald-200'
          : 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200'
      }
    >
      {children}
    </Badge>
  )
}
