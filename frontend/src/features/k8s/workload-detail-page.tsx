import { lazy, Suspense, useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Box,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  ChevronDown,
  ClipboardCopy,
  CircleDot,
  Download,
  FolderOpen,
  RefreshCw,
  RotateCw,
  ScrollText,
  Terminal,
} from 'lucide-react'
import { toast } from 'sonner'
import {
  k8sApi,
  type K8sUsage,
  type K8sObject,
  type K8sObjectParams,
} from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { age } from './age'
import { get, list, str } from './catalog'
import { formatBytes, formatCPU } from './format'
import { podContainers } from './pod-containers'
import type { PodConsoleQuery } from './pod-terminal-page'
import { ResourceActions } from './resource-actions'
import { StatusText } from './status-text'
import { useResourceCaps } from './use-capabilities'

const YamlCard = lazy(() =>
  import('./yaml-editor').then((m) => ({ default: m.YamlCard }))
)
const LogPanel = lazy(() =>
  import('./pod-console').then((m) => ({ default: m.LogPanel }))
)

type Props = {
  params: K8sObjectParams
  name: string
  obj: K8sObject
  fetching: boolean
  onRefresh: () => void
  onDeleted: () => void
  onEdit?: () => void
  onOpenTerminal: (q: PodConsoleQuery) => void
}

const n = (value: unknown) => Number(value) || 0

function selectorOf(obj: K8sObject): string {
  const labels = get(obj, 'spec.selector.matchLabels')
  if (labels == null || typeof labels !== 'object' || Array.isArray(labels)) {
    return ''
  }
  return Object.entries(labels as Record<string, unknown>)
    .filter(([, value]) => typeof value === 'string')
    .map(([key, value]) => `${key}=${value}`)
    .join(',')
}

function workloadCounts(obj: K8sObject) {
  const kind = obj.kind
  if (kind === 'DaemonSet') {
    return {
      ready: n(get(obj, 'status.numberReady')),
      desired: n(get(obj, 'status.desiredNumberScheduled')),
    }
  }
  if (kind === 'Job') {
    return {
      ready: n(get(obj, 'status.succeeded')),
      desired: n(get(obj, 'spec.completions')) || 1,
    }
  }
  if (kind === 'CronJob') {
    return {
      ready: list(get(obj, 'status.active')).length,
      desired: 1,
    }
  }
  return {
    ready: n(get(obj, 'status.readyReplicas')),
    desired: n(get(obj, 'spec.replicas')),
  }
}

function podPhase(pod: K8sObject) {
  for (const status of list(get(pod, 'status.containerStatuses'))) {
    const state = status.state as
      | Record<string, Record<string, unknown>>
      | undefined
    if (state?.waiting?.reason) return str(state.waiting.reason)
    if (state?.terminated?.reason) return str(state.terminated.reason)
  }
  return str(get(pod, 'status.phase'))
}

function podReady(pod: K8sObject) {
  const statuses = list(get(pod, 'status.containerStatuses'))
  return `${statuses.filter((item) => item.ready === true).length}/${statuses.length}`
}

function eventTime(event: K8sObject) {
  return str(
    get(event, 'lastTimestamp') ??
      get(event, 'eventTime') ??
      event.metadata?.creationTimestamp
  )
}

function RuntimeEventTable({ events }: { events: K8sObject[] }) {
  if (events.length === 0) return <EmptyState text='暂无关联事件' />
  return (
    <div className='overflow-x-auto'>
      <table className='w-full text-xs'>
        <thead>
          <tr className='bg-[#f4f6fa] text-left font-medium text-[#657fa8]'>
            <th className='px-3 py-2'>原因</th>
            <th className='px-3 py-2'>时间</th>
            <th className='px-3 py-2 text-right'>次数</th>
            <th className='px-3 py-2'>消息</th>
          </tr>
        </thead>
        <tbody>
          {events.slice(0, 10).map((event, index) => (
            <tr
              key={`${event.metadata?.uid ?? event.metadata?.name}-${index}`}
              className={
                get(event, 'type') === 'Warning'
                  ? 'border-b border-white bg-[#fddadd]'
                  : 'border-b border-[#e5eaf2] bg-white'
              }
            >
              <td className='px-3 py-2 font-medium text-[#234883]'>
                {str(get(event, 'reason'))}
              </td>
              <td className='px-3 py-2 whitespace-nowrap text-[#657fa8]'>
                {age(eventTime(event) === '-' ? undefined : eventTime(event))}
              </td>
              <td className='px-3 py-2 text-right tabular-nums'>
                {str(get(event, 'count'), '1')}
              </td>
              <td
                className='max-w-xl truncate px-3 py-2'
                title={str(get(event, 'message'))}
              >
                {str(get(event, 'message'))}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function Conditions({ pod }: { pod: K8sObject }) {
  const conditions = list(get(pod, 'status.conditions'))
  const primary = conditions.filter((condition) =>
    ['Ready', 'ContainersReady'].includes(str(condition.type))
  )
  const secondary = conditions.filter(
    (condition) => !['Ready', 'ContainersReady'].includes(str(condition.type))
  )
  const visible = primary.length > 0 ? primary : conditions

  const renderCondition = (condition: Record<string, unknown>) => {
    const ok = condition.status === 'True'
    return (
      <div
        key={str(condition.type)}
        className='flex items-center justify-between rounded bg-white px-3 py-2 text-xs'
      >
        <span className='inline-flex items-center gap-2 font-medium text-[#415b80]'>
          <CheckCircle2
            className={ok ? 'size-4 text-[#0fbe8f]' : 'size-4 text-[#a7b6cd]'}
          />
          {str(condition.type)}
        </span>
        <span className='font-mono text-[#657fa8]'>
          {str(condition.lastTransitionTime)}
        </span>
      </div>
    )
  }

  return (
    <div className='space-y-2'>
      <div className='grid gap-2 md:grid-cols-2'>
        {visible.map(renderCondition)}
      </div>
      {primary.length > 0 && secondary.length > 0 && (
        <Collapsible>
          <CollapsibleTrigger asChild>
            <Button
              variant='ghost'
              size='sm'
              className='group h-7 px-1.5 text-xs text-[#657fa8] hover:bg-[#eef3fa] hover:text-[#31598e]'
            >
              <ChevronRight className='size-3.5 transition-transform group-data-[state=open]:rotate-90' />
              状态详情
              <span className='text-[#95a1b2]'>（{secondary.length} 项）</span>
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className='pt-2'>
            <div className='grid gap-2 md:grid-cols-2'>
              {secondary.map(renderCondition)}
            </div>
          </CollapsibleContent>
        </Collapsible>
      )}
    </div>
  )
}

type ContainerRowsProps = {
  pod: K8sObject
  canLogs: boolean
  canExec: boolean
  onDownloadLogs: (container?: string) => void
  onOpenConsole: (
    tab: 'logs' | 'terminal',
    container?: string,
    command?: string,
    autoConnect?: boolean
  ) => void
}

const shells = [
  { label: 'bash', command: '/bin/bash' },
  { label: 'sh', command: '/bin/sh' },
  { label: 'cmd', command: 'cmd' },
  { label: 'powershell', command: 'powershell' },
] as const

function ContainerRows({
  pod,
  canLogs,
  canExec,
  onDownloadLogs,
  onOpenConsole,
}: ContainerRowsProps) {
  const containers = list(get(pod, 'spec.containers'))
  const initContainers = list(get(pod, 'spec.initContainers'))
  const statuses = list(get(pod, 'status.containerStatuses'))
  const initStatuses = list(get(pod, 'status.initContainerStatuses'))
  const rows = [
    ...initContainers.map((container) => ({ container, init: true })),
    ...containers.map((container) => ({ container, init: false })),
  ]
  return (
    <div className='space-y-2'>
      {rows.map(({ container, init }) => {
        const status = (init ? initStatuses : statuses).find(
          (item) => item.name === container.name
        )
        const state = status?.state as
          | Record<string, Record<string, unknown>>
          | undefined
        const stateText = state?.running
          ? 'Running'
          : str(state?.waiting?.reason ?? state?.terminated?.reason)
        const ports = list(container.ports)
        const startedAt = str(
          status == null
            ? undefined
            : (get(status, 'state.running.startedAt') ??
                get(status, 'state.terminated.startedAt'))
        )
        const started = status?.started === true || state?.running != null
        return (
          <div
            key={`${init ? 'init-' : ''}${str(container.name)}`}
            className='rounded-xl border border-[#e2e8f2] bg-white px-4 py-3 shadow-[0_3px_14px_rgba(37,76,132,0.04)]'
          >
            <div className='flex flex-wrap items-start justify-between gap-3'>
              <div className='flex min-w-0 items-start gap-3'>
                <div className='mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-lg bg-[#edf3fb] text-[#31598e]'>
                  {init ? (
                    <CircleDot className='size-5' />
                  ) : (
                    <Box className='size-5' />
                  )}
                </div>
                <div className='min-w-0'>
                  <div className='flex flex-wrap items-center gap-2'>
                    <span className='font-semibold text-[#234883]'>
                      {str(container.name)}
                    </span>
                    {init && (
                      <Badge className='border-0 bg-[#cff2e9] text-[#0b8d6c]'>
                        初始化容器
                      </Badge>
                    )}
                    <StatusText value={stateText} />
                  </div>
                  <p
                    className='mt-1 max-w-4xl truncate font-mono text-xs text-[#657fa8]'
                    title={str(container.image)}
                  >
                    镜像：{str(container.image)}
                  </p>
                  <p className='mt-1 text-xs text-[#8a98ad]'>
                    镜像拉取策略：{str(container.imagePullPolicy)} · 重启次数：
                    {str(status?.restartCount, '0')}
                  </p>
                </div>
              </div>
              <div className='flex flex-wrap items-center justify-end gap-1.5'>
                <Button
                  variant='outline'
                  size='sm'
                  disabled
                  title='当前后端尚未提供安全的容器文件接口'
                  className='h-8 rounded-md border-[#c9d4e6] px-2.5 text-xs text-[#8290a4]'
                >
                  <FolderOpen className='size-3.5' /> 文件浏览器
                </Button>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={!canLogs}
                  title={
                    canLogs ? '下载当前容器组日志' : '当前账号没有查看日志权限'
                  }
                  className='h-8 rounded-md border-[#c9d4e6] px-2.5 text-xs text-[#31598e]'
                  onClick={() => onDownloadLogs(str(container.name))}
                >
                  <Download className='size-3.5' /> 下载日志
                </Button>
                <Button
                  variant='outline'
                  size='sm'
                  disabled={!canLogs}
                  className='h-8 rounded-md border-[#c9d4e6] px-2.5 text-xs text-[#31598e]'
                  onClick={() => onOpenConsole('logs')}
                >
                  <ScrollText className='size-3.5' /> 追踪日志
                </Button>
                {state?.running != null && (
                  <ShellAction
                    container={str(container.name)}
                    canExec={canExec}
                    onOpenConsole={onOpenConsole}
                  />
                )}
              </div>
            </div>

            <div className='mt-3 flex flex-wrap items-center gap-2 border-t border-[#eef1f6] pt-3 text-xs'>
              <Badge
                className={
                  started
                    ? 'border-[#b7ebde] bg-[#effcf8] font-mono text-[#0b8d6c]'
                    : 'border-[#dbe2ed] bg-[#f5f7fa] font-mono text-[#8994a4]'
                }
              >
                {started ? (
                  <CheckCircle2 className='size-3.5' />
                ) : (
                  <CircleDot className='size-3.5' />
                )}
                Started
              </Badge>
              <Badge
                className={
                  status?.ready === true
                    ? 'border-[#b7ebde] bg-[#effcf8] font-mono text-[#0b8d6c]'
                    : 'border-[#dbe2ed] bg-[#f5f7fa] font-mono text-[#8994a4]'
                }
              >
                {status?.ready === true ? (
                  <CheckCircle2 className='size-3.5' />
                ) : (
                  <CircleDot className='size-3.5' />
                )}
                Ready
              </Badge>
              <Badge className='border-[#dbe2ed] bg-[#f5f7fa] font-mono text-[#657fa8]'>
                {stateText}
              </Badge>
              {startedAt !== '-' && (
                <span className='font-mono text-[#657fa8]'>
                  StartedAt&nbsp; {startedAt}
                </span>
              )}
            </div>

            {ports.length > 0 && (
              <div className='mt-3 flex flex-wrap gap-2'>
                {ports.map((port, index) => (
                  <span
                    key={`${str(port.name, index.toString())}-${str(port.containerPort)}-${index}`}
                    className='inline-flex items-center overflow-hidden rounded-md border border-[#d6e1f0] bg-white font-mono text-xs'
                  >
                    <span className='bg-[#234883] px-2 py-1 text-white'>
                      {str(port.containerPort)}
                    </span>
                    <span className='bg-[#7188ad] px-2 py-1 text-white'>
                      {str(port.protocol, 'TCP')}
                    </span>
                    <span className='flex size-6 items-center justify-center bg-[#0fbe8f] text-white'>
                      <CircleDot className='size-3.5' />
                    </span>
                  </span>
                ))}
              </div>
            )}
          </div>
        )
      })}
    </div>
  )
}

function ShellAction({
  container,
  canExec,
  onOpenConsole,
}: {
  container: string
  canExec: boolean
  onOpenConsole: ContainerRowsProps['onOpenConsole']
}) {
  const [shell, setShell] = useState<(typeof shells)[number]['label']>('bash')
  const selected = shells.find((item) => item.label === shell) ?? shells[0]
  return (
    <div className='inline-flex items-stretch'>
      <Button
        variant='outline'
        size='sm'
        disabled={!canExec}
        className='h-8 rounded-e-none border-[#f0b7c0] px-2.5 text-xs text-[#c2404e] hover:bg-[#fff4f5]'
        title={
          canExec ? `使用 ${selected.label} 打开终端` : '当前账号没有终端权限'
        }
        onClick={() =>
          onOpenConsole('terminal', container, selected.command, true)
        }
      >
        <Terminal className='size-3.5' /> {selected.label}
      </Button>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant='outline'
            size='icon'
            disabled={!canExec}
            aria-label='选择终端 Shell'
            title='选择终端 Shell'
            className='size-8 rounded-s-none border-l-0 border-[#f0b7c0] text-[#c2404e] hover:bg-[#fff4f5]'
          >
            <ChevronDown className='size-3.5' />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align='end' className='min-w-36'>
          {shells.map((item) => (
            <DropdownMenuItem
              key={item.label}
              className='font-mono'
              onSelect={() => setShell(item.label)}
            >
              {item.label}
              {item.label === selected.label && (
                <span className='ms-auto text-[#0fbe8f]'>✓</span>
              )}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}

function PodRuntimeCard({
  pod,
  params,
  onOpenConsole,
  onRefresh,
  metric,
}: {
  pod: K8sObject
  params: K8sObjectParams
  onOpenConsole: (
    tab: 'logs' | 'terminal',
    container?: string,
    command?: string,
    autoConnect?: boolean
  ) => void
  onRefresh: () => void
  metric?: K8sUsage
}) {
  const [restartOpen, setRestartOpen] = useState(false)
  const podName = str(pod.metadata?.name)
  const podParams: K8sObjectParams = {
    cluster_id: params.cluster_id,
    version: 'v1',
    resource: 'pods',
    namespace: params.namespace,
  }
  const podCaps = useResourceCaps(podParams)
  const { caps } = podCaps
  const canLogs = podCaps.query.data?.logs === true
  const canExec = podCaps.query.data?.exec === true
  const restart = useMutation({
    mutationFn: () => k8sApi.restartPod(podName, podParams),
    onSuccess: () => {
      toast.success(`容器组 ${podName} 已触发重启`)
      setRestartOpen(false)
      onRefresh()
    },
    onError: () => toast.error('容器组重启失败'),
  })

  const downloadLogs = async (container?: string) => {
    try {
      const response = await k8sApi.logs(podName, podParams)
      const suffix = container ? `-${container}` : ''
      const blob = new Blob([response.data.logs], {
        type: 'text/plain;charset=utf-8',
      })
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = `${podName}${suffix}.log`
      anchor.click()
      URL.revokeObjectURL(url)
      toast.success('日志已下载')
    } catch {
      toast.error('日志下载失败')
    }
  }
  return (
    <section className='overflow-hidden rounded-xl border border-[#e1e6ef] bg-white'>
      <div className='flex flex-wrap items-center justify-between gap-3 border-b border-[#e1e6ef] px-4 py-2.5'>
        <div className='flex min-w-0 items-center gap-2.5'>
          <span className='text-xs text-[#8a98ad]'>容器组</span>
          <strong className='truncate font-mono text-sm text-[#234883]'>
            {podName}
          </strong>
          <span className='text-xs text-[#95a1b2]'>
            {age(pod.metadata?.creationTimestamp as string | undefined)}
          </span>
        </div>
        <div className='flex items-center gap-2'>
          <Suspense fallback={null}>
            <YamlCard
              params={podParams}
              name={podName}
              obj={pod}
              presentation='dialog'
              triggerLabel='Pod YAML'
            />
          </Suspense>
          <Button
            variant='outline'
            size='sm'
            className='h-8 rounded-sm border-[#c9d4e6] bg-white px-3 text-[#31598e] hover:bg-[#f4f7fb] hover:text-[#234883]'
            onClick={() => onOpenConsole('logs')}
          >
            <Terminal className='size-4' /> 日志 / 终端
          </Button>
          {caps?.delete === true && (
            <Button
              variant='outline'
              size='sm'
              className='h-8 rounded-sm border-[#f0b7c0] px-3 text-[#c2404e] hover:bg-[#fff4f5]'
              onClick={() => setRestartOpen(true)}
            >
              <RefreshCw className='size-4' /> 重启
            </Button>
          )}
          <ResourceActions
            params={podParams}
            name={podName}
            obj={pod}
            presentation='toolbar'
            onDeleted={onRefresh}
          />
        </div>
      </div>
      <div className='space-y-4 p-4'>
        <div className='flex flex-wrap items-center gap-2 text-xs text-[#657fa8]'>
          <span className='inline-flex items-center gap-1.5 rounded-md bg-[#effcf8] px-2.5 py-1.5 font-medium text-[#0b8d6c]'>
            <CircleDot className='size-3.5' /> {podPhase(pod)}
          </span>
          <span className='rounded-md bg-[#f4f6fa] px-2.5 py-1.5'>
            就绪 {podReady(pod)}
          </span>
          <span className='rounded-md bg-[#f4f6fa] px-2.5 py-1.5'>
            CPU {metric ? formatCPU(metric.cpu_millis) : '-'}
          </span>
          <span className='rounded-md bg-[#f4f6fa] px-2.5 py-1.5'>
            内存 {metric ? formatBytes(metric.mem_bytes) : '-'}
          </span>
        </div>
        <div className='grid gap-4 text-xs md:grid-cols-3'>
          <div>
            <p className='text-[#8a98ad]'>所在节点</p>
            <p className='mt-1 font-medium text-[#087bea]'>
              {str(get(pod, 'spec.nodeName'))}
            </p>
          </div>
          <div>
            <p className='text-[#8a98ad]'>容器组 IP</p>
            <p className='mt-1 font-mono text-[#415b80]'>
              {str(get(pod, 'status.podIP'))}
            </p>
          </div>
          <div>
            <p className='text-[#8a98ad]'>状态</p>
            <div className='mt-1'>
              <StatusText value={podPhase(pod)} />
            </div>
          </div>
        </div>
        <Conditions pod={pod} />
        <ContainerRows
          pod={pod}
          canLogs={canLogs}
          canExec={canExec}
          onDownloadLogs={downloadLogs}
          onOpenConsole={onOpenConsole}
        />
      </div>
      <ConfirmDialog
        open={restartOpen}
        onOpenChange={setRestartOpen}
        title='重启容器组'
        icon={
          <span className='flex size-9 shrink-0 items-center justify-center rounded-lg bg-[#fdf0f1] text-[#c2404e]'>
            <RotateCw className='size-4' />
          </span>
        }
        desc={`确认删除并重建容器组“${podName}”？控制器会按照工作负载配置自动创建新的 Pod。`}
        confirmText='确认重启'
        destructive
        isLoading={restart.isPending}
        handleConfirm={() => restart.mutate()}
      />
    </section>
  )
}

export function WorkloadDetailPage({
  params,
  name,
  obj,
  fetching,
  onRefresh,
  onDeleted,
  onEdit,
  onOpenTerminal,
}: Props) {
  const queryClient = useQueryClient()
  const [selectedPod, setSelectedPod] = useState(0)
  const [consoleOpen, setConsoleOpen] = useState(false)
  const [consoleContainer, setConsoleContainer] = useState<string>()
  const selector = selectorOf(obj)
  const counts = workloadCounts(obj)
  const scalable = obj.kind === 'Deployment' || obj.kind === 'StatefulSet'
  const revision = str(
    obj.metadata?.annotations?.['deployment.kubernetes.io/revision'],
    '1'
  )

  const pods = useQuery({
    queryKey: [
      'k8s',
      'workload-pods',
      params.cluster_id,
      params.namespace ?? '',
      selector,
    ],
    queryFn: () =>
      k8sApi.resources({
        cluster_id: params.cluster_id,
        version: 'v1',
        resource: 'pods',
        namespace: params.namespace,
        label_selector: selector,
      }),
    enabled: selector !== '',
  })
  const events = useQuery({
    queryKey: [
      'k8s',
      'workload-events',
      params.cluster_id,
      params.namespace ?? '',
      obj.kind,
      name,
    ],
    queryFn: () =>
      k8sApi.resources({
        cluster_id: params.cluster_id,
        version: 'v1',
        resource: 'events',
        namespace: params.namespace,
      }),
  })
  const overview = useQuery({
    queryKey: ['k8s', 'cluster-overview', params.cluster_id],
    queryFn: () => k8sApi.overview(params.cluster_id),
    retry: false,
  })
  const podItems = pods.data?.data.items ?? []
  const eventItems = useMemo(
    () =>
      [...(events.data?.data.items ?? [])]
        .filter((event) => {
          const objectName = str(get(event, 'involvedObject.name'), '')
          const objectKind = str(get(event, 'involvedObject.kind'), '')
          return (
            objectName === name ||
            ((objectKind === 'ReplicaSet' || objectKind === 'Pod') &&
              objectName.startsWith(`${name}-`))
          )
        })
        .sort((a, b) => eventTime(b).localeCompare(eventTime(a))),
    [events.data, name]
  )
  const pod = podItems[Math.min(selectedPod, Math.max(podItems.length - 1, 0))]
  const podMetric =
    pod == null
      ? undefined
      : overview.data?.data.top_pods.find(
          (item) =>
            item.namespace === params.namespace &&
            item.name === str(pod.metadata?.name)
        )?.usage
  const pct =
    counts.desired === 0
      ? 0
      : Math.min(100, (counts.ready / counts.desired) * 100)
  const quickScale = useMutation({
    mutationFn: (replicas: number) =>
      k8sApi.scale(name, params, {
        replicas,
        resource_version: obj.metadata?.resourceVersion as string | undefined,
      }),
    onSuccess: () => {
      toast.success('副本数已更新')
      void queryClient.invalidateQueries({ queryKey: ['k8s'] })
    },
  })

  const refreshAll = () => {
    onRefresh()
    void queryClient.invalidateQueries({ queryKey: ['k8s', 'workload-pods'] })
    void queryClient.invalidateQueries({ queryKey: ['k8s', 'workload-events'] })
    void queryClient.invalidateQueries({
      queryKey: ['k8s', 'cluster-overview'],
    })
  }

  // Logs stay in a dialog (read-only stream, dismiss is safe); terminal
  // sessions navigate to the dedicated /k8s/console page — a modal would
  // be too narrow and Esc/outside-click would kill the live session.
  const openConsole = (
    tab: 'logs' | 'terminal' = 'logs',
    container?: string,
    command?: string
  ) => {
    if (tab === 'terminal') {
      onOpenTerminal({
        cluster: params.cluster_id,
        ns: params.namespace,
        pod: str(pod?.metadata?.name),
        container,
        command,
        g: params.group,
        v: params.version,
        r: params.resource,
        name,
      })
      return
    }
    setConsoleContainer(container)
    setConsoleOpen(true)
  }

  return (
    <div className='min-w-0 space-y-3 text-[#2b3e5c]'>
      <div className='flex flex-wrap items-center justify-between gap-3 border-b border-[#e1e6ef] bg-white px-4 py-2.5'>
        <div className='flex min-w-0 items-center gap-2.5'>
          <span className='h-4 w-1 rounded-full bg-[#234883]' />
          <span className='text-sm font-semibold text-[#234883]'>
            {obj.kind ?? 'Workload'}
          </span>
          <span className='truncate text-sm text-[#7e8796]'>{name}</span>
        </div>
        <div className='flex flex-wrap items-center justify-end gap-2'>
          <Button
            variant='outline'
            size='sm'
            className='h-8 rounded-lg border-[#d6e1f0] bg-white px-3 text-xs text-[#31598e] hover:bg-[#f4f7fb] hover:text-[#234883]'
            disabled={pod == null}
            onClick={() => openConsole('logs')}
          >
            <ScrollText className='size-3.5' /> 日志
          </Button>
          <Suspense fallback={<Skeleton className='h-8 w-16 rounded-lg' />}>
            <YamlCard
              params={params}
              name={name}
              obj={obj}
              presentation='dialog'
              triggerLabel='YAML'
              dialogTitle='预览 / 编辑 YAML'
            />
          </Suspense>
          <span
            className='mx-1 hidden h-5 w-px bg-[#e1e6ef] sm:block'
            aria-hidden
          />
          <ResourceActions
            params={params}
            name={name}
            obj={obj}
            presentation='toolbar'
            onDeleted={onDeleted}
            onEdit={onEdit}
          />
          <span
            className='mx-1 hidden h-5 w-px bg-[#e1e6ef] sm:block'
            aria-hidden
          />
          <div className='flex items-center gap-1'>
            <Button
              variant='ghost'
              size='icon'
              aria-label='复制资源清单'
              title='复制资源清单'
              className='size-8 text-[#7e8796] hover:bg-[#f4f7fb] hover:text-[#234883]'
              onClick={() => {
                void navigator.clipboard.writeText(JSON.stringify(obj, null, 2))
                toast.success('资源清单已复制')
              }}
            >
              <ClipboardCopy className='size-4' />
            </Button>
            <Button
              variant='ghost'
              size='icon'
              aria-label='刷新详情'
              title='刷新'
              className='size-8 text-[#7e8796] hover:bg-[#f4f7fb] hover:text-[#234883]'
              onClick={refreshAll}
              disabled={fetching}
            >
              <RefreshCw
                className={fetching ? 'size-4 animate-spin' : 'size-4'}
              />
            </Button>
          </div>
        </div>
      </div>

      <Tabs defaultValue='runtime' className='gap-0'>
        <TabsList className='h-12 w-full justify-start rounded-none border border-[#e1e6ef] bg-[#f4f6fa] p-0'>
          {[
            ['metadata', '元数据'],
            ['runtime', '运行时'],
            ['service', '服务'],
            ['route', '应用路由'],
          ].map(([value, label]) => (
            <TabsTrigger
              key={value}
              value={value}
              className='h-full flex-none rounded-none border-0 border-r border-[#e1e6ef] px-8 data-[state=active]:border-b-4 data-[state=active]:border-b-[#ff8807] data-[state=active]:bg-white data-[state=active]:text-[#ff8807] data-[state=active]:shadow-none'
            >
              {label}
            </TabsTrigger>
          ))}
        </TabsList>

        <TabsContent value='runtime' className='mt-0 bg-white'>
          <div className='flex flex-wrap items-center justify-between gap-3 border-b border-[#e1e6ef] px-4 py-3'>
            <div className='flex items-center gap-2'>
              <span className='text-xs text-[#8a98ad]'>副本</span>
              <Button
                variant='outline'
                size='icon'
                className='size-7 rounded-md border-[#c9d4e6] text-[#31598e] hover:bg-[#f4f7fb]'
                disabled={
                  !scalable || quickScale.isPending || counts.desired <= 0
                }
                onClick={() => quickScale.mutate(counts.desired - 1)}
              >
                <ChevronLeft className='size-4' />
              </Button>
              <div className='relative h-7 w-44 overflow-hidden rounded-md bg-[#eef2f8]'>
                <div
                  className='absolute inset-y-0 left-0 bg-[#0fbe8f]/85'
                  style={{ width: `${pct}%` }}
                />
                <span className='absolute inset-0 flex items-center justify-center text-xs font-semibold text-[#234883] tabular-nums mix-blend-multiply'>
                  {counts.ready} / {counts.desired}
                </span>
              </div>
              <Button
                variant='outline'
                size='icon'
                className='size-7 rounded-md border-[#c9d4e6] text-[#31598e] hover:bg-[#f4f7fb]'
                disabled={
                  !scalable || quickScale.isPending || counts.desired >= 50
                }
                onClick={() => quickScale.mutate(counts.desired + 1)}
              >
                <ChevronRight className='size-4' />
              </Button>
            </div>
            <span className='text-xs text-[#95a1b2]'>
              修订 #{revision} · {eventItems.length} 个关联事件
            </span>
          </div>

          <div className='grid lg:grid-cols-[20rem_1fr]'>
            <div className='border-b border-[#e1e6ef] p-4 lg:border-r lg:border-b-0'>
              <p className='text-xs font-medium text-[#8a98ad]'>
                关联容器组（{podItems.length}）
              </p>
              <div className='mt-3 space-y-2'>
                {pods.isPending ? (
                  <div className='space-y-2'>
                    <Skeleton className='h-16 w-full' />
                    <Skeleton className='h-16 w-full' />
                  </div>
                ) : podItems.length === 0 ? (
                  <EmptyState
                    text={
                      selector
                        ? '暂无关联容器组'
                        : '该资源没有可用的 Pod selector'
                    }
                  />
                ) : (
                  podItems.map((item, index) => (
                    <button
                      type='button'
                      key={str(item.metadata?.uid, `${index}`)}
                      className={
                        index === selectedPod
                          ? 'block w-full rounded-lg border border-[#0fbe8f] bg-[#effcf8] p-3 text-left'
                          : 'block w-full rounded-lg border border-[#e5eaf2] bg-white p-3 text-left transition hover:border-[#b9c8dd] hover:bg-[#f7faff]'
                      }
                      onClick={() => setSelectedPod(index)}
                    >
                      <div className='flex items-center justify-between gap-2'>
                        <span className='truncate font-mono text-sm font-medium text-[#234883]'>
                          {str(item.metadata?.name)}
                        </span>
                        <StatusText value={podPhase(item)} />
                      </div>
                      <div className='mt-1.5 flex gap-4 text-xs text-[#8a98ad]'>
                        <span>
                          {age(
                            item.metadata?.creationTimestamp as
                              | string
                              | undefined
                          )}
                        </span>
                        <span className='tabular-nums'>
                          {podReady(item)} Ready
                        </span>
                        <span className='font-mono'>
                          {str(get(item, 'status.podIP'))}
                        </span>
                      </div>
                    </button>
                  ))
                )}
              </div>
            </div>
            <div className='min-w-0 space-y-4 bg-[#f7f9fc] p-4'>
              <section className='overflow-hidden rounded-xl border border-[#e1e6ef] bg-white'>
                <p className='border-b border-[#e1e6ef] px-4 py-2.5 text-xs font-medium text-[#8a98ad]'>
                  关联事件（{eventItems.length}）
                </p>
                <div className='p-4'>
                  {events.isPending ? (
                    <div className='space-y-2'>
                      {Array.from({ length: 4 }).map((_, index) => (
                        <Skeleton key={index} className='h-9 w-full' />
                      ))}
                    </div>
                  ) : (
                    <RuntimeEventTable events={eventItems} />
                  )}
                </div>
              </section>
              {pod != null ? (
                <PodRuntimeCard
                  pod={pod}
                  params={params}
                  onOpenConsole={openConsole}
                  onRefresh={refreshAll}
                  metric={podMetric}
                />
              ) : pods.isPending ? (
                <Skeleton className='h-48 w-full' />
              ) : (
                <EmptyState text='选择一个容器组查看详情' />
              )}
            </div>
          </div>
        </TabsContent>

        <TabsContent
          value='metadata'
          className='mt-0 border border-t-0 bg-white p-5'
        >
          <div className='grid gap-4 md:grid-cols-2'>
            {[
              ['名称', name],
              ['命名空间', str(obj.metadata?.namespace)],
              ['UID', str(obj.metadata?.uid)],
              ['创建时间', str(obj.metadata?.creationTimestamp)],
              ['API 版本', str(obj.apiVersion)],
              ['Selector', selector || '-'],
            ].map(([label, value]) => (
              <div
                key={label}
                className='rounded-lg border border-[#e5eaf2] px-4 py-3'
              >
                <p className='text-xs text-[#8a98ad]'>{label}</p>
                <p className='mt-1 text-sm font-medium break-all text-[#415b80]'>
                  {value}
                </p>
              </div>
            ))}
          </div>
        </TabsContent>
        <TabsContent
          value='service'
          className='mt-0 border border-t-0 bg-white p-8'
        >
          <EmptyState text='暂未发现关联 Service' />
        </TabsContent>
        <TabsContent
          value='route'
          className='mt-0 border border-t-0 bg-white p-8'
        >
          <EmptyState text='暂未发现关联 Ingress' />
        </TabsContent>
      </Tabs>

      <Dialog open={consoleOpen} onOpenChange={setConsoleOpen}>
        <DialogContent className='max-h-[92vh] overflow-y-auto sm:max-w-6xl'>
          <DialogHeader className='flex-row items-center gap-3 space-y-0 text-start'>
            <span className='flex size-9 shrink-0 items-center justify-center rounded-lg bg-[#eef3fa] text-[#31598e]'>
              <ScrollText className='size-4' />
            </span>
            <div className='min-w-0 space-y-0.5'>
              <DialogTitle>日志</DialogTitle>
              <DialogDescription className='truncate'>
                {pod == null
                  ? '请先选择容器组'
                  : `容器组 ${str(pod.metadata?.name)}`}
              </DialogDescription>
            </div>
          </DialogHeader>
          {pod != null && (
            <Suspense fallback={<Skeleton className='h-80 w-full' />}>
              <LogPanel
                key={consoleContainer ?? 'default'}
                params={{
                  cluster_id: params.cluster_id,
                  version: 'v1',
                  resource: 'pods',
                  namespace: params.namespace,
                }}
                pod={str(pod.metadata?.name)}
                containers={podContainers(pod)}
                initialContainer={consoleContainer}
                autoConnect
                heightClass='h-[55vh]'
              />
            </Suspense>
          )}
        </DialogContent>
      </Dialog>
    </div>
  )
}
