import { useQuery } from '@tanstack/react-query'
import {
  CircleHelp,
  Gauge,
  Loader2,
  RefreshCw,
  Route,
  Scaling,
  ShieldCheck,
} from 'lucide-react'
import { platformApi, type DeliveryPreviewCheck } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'

function detail(error: unknown) {
  return (
    (error as { response?: { data?: { detail?: string } } })?.response?.data
      ?.detail ?? '读取失败或当前身份没有读取权限。'
  )
}

function permissionLabel(check?: DeliveryPreviewCheck) {
  if (!check || check.status === 'unknown') return '无法验证'
  if (check.status === 'blocked') return '缺少权限'
  if (check.status === 'passed') return '已验证'
  return '需核对'
}

function permissionTone(check?: DeliveryPreviewCheck) {
  if (check?.status === 'passed')
    return 'border-emerald-300 bg-emerald-50 text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-100'
  if (check?.status === 'blocked')
    return 'border-destructive/30 bg-destructive/5 text-destructive dark:border-destructive/40 dark:bg-destructive/10'
  return 'border-border bg-muted/60 text-muted-foreground'
}

export function StrategyCapabilityPreview({
  clusterId,
  namespace,
  deploymentName,
  selectedService,
  onServiceNameChange,
}: {
  clusterId: number
  namespace: string
  deploymentName: string
  selectedService?: string
  onServiceNameChange?: (service: string) => void
}) {
  const enabled = clusterId > 0 && !!namespace && !!deploymentName
  const preview = useQuery({
    queryKey: [
      'delivery-capability-preview',
      clusterId,
      namespace,
      deploymentName,
    ],
    queryFn: () =>
      platformApi.deliveryPreview({
        cluster_id: clusterId,
        namespace,
        deployment_name: deploymentName,
      }),
    enabled,
    retry: false,
  })
  const result = preview.data?.data
  const serviceCheck = result?.checks.find(
    (check) => check.key === 'service-entry'
  )
  const autoscalingCheck = result?.checks.find(
    (check) => check.key === 'autoscaling'
  )
  const capacityCheck = result?.checks.find((check) => check.key === 'capacity')
  const rollingPermission = result?.checks.find(
    (check) => check.key === 'rolling-write-permission'
  )
  const blueGreenPermission = result?.checks.find(
    (check) => check.key === 'blue-green-write-permission'
  )
  const matchingServices = result?.matching_services ?? []
  const targetAutoscalers = result?.autoscalers ?? []

  const serviceBusy = !enabled || preview.isPending
  const serviceUnknown =
    preview.isError ||
    (!serviceBusy && (!serviceCheck || serviceCheck.status === 'unknown'))
  const serviceUnavailableReason = preview.isError
    ? detail(preview.error)
    : (serviceCheck?.detail ??
      '无法确认 Service selector 与 Deployment 的匹配关系。')
  const hpaBusy = !enabled || preview.isPending
  const hpaUnknown =
    preview.isError ||
    (!hpaBusy && (!autoscalingCheck || autoscalingCheck.status === 'unknown'))
  const capacityBusy = !enabled || preview.isPending
  const capacityUnknown =
    preview.isError ||
    (!capacityBusy && (!capacityCheck || capacityCheck.status === 'unknown'))
  const capacityStatus = capacityBusy
    ? '读取中'
    : capacityUnknown
      ? '无法确认'
      : capacityCheck?.status === 'blocked'
        ? '配额不足'
        : capacityCheck?.status === 'review'
          ? '需人工核对'
          : '已检查'
  const capacityToneClass =
    capacityCheck?.status === 'blocked'
      ? 'border-destructive/30 bg-destructive/5 text-destructive dark:border-destructive/40 dark:bg-destructive/10'
      : capacityCheck?.status === 'review'
        ? 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-100'
        : capacityCheck?.status === 'passed'
          ? 'border-emerald-300 bg-emerald-50 text-emerald-900 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-100'
          : 'border-border bg-muted/60 text-muted-foreground'

  return (
    <section aria-label='发布策略能力预检' className='space-y-3'>
      <div className='flex items-center gap-2'>
        <CircleHelp className='size-4 text-muted-foreground' />
        <h3 className='text-sm font-medium'>目标能力预检</h3>
        {(serviceBusy || hpaBusy || capacityBusy) && (
          <Loader2 className='size-4 animate-spin text-muted-foreground' />
        )}
      </div>
      <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
        <Card className='shadow-none'>
          <CardContent className='flex items-start gap-3 p-4'>
            <Route className='mt-0.5 size-4 shrink-0 text-muted-foreground' />
            <div className='min-w-0 space-y-1'>
              <div className='flex flex-wrap items-center gap-2'>
                <p className='text-sm font-medium'>Service 入口候选</p>
                <Badge
                  variant='outline'
                  className='dark:border-slate-700 dark:text-slate-100'
                >
                  {serviceBusy
                    ? '读取中'
                    : serviceUnknown
                      ? '无法确认'
                      : matchingServices.length === 0
                        ? '未发现匹配项'
                        : `发现 ${matchingServices.length} 个`}
                </Badge>
              </div>
              <p className='text-xs leading-5 break-all text-muted-foreground'>
                {serviceBusy
                  ? '正在读取 Deployment 标签和命名空间 Service。'
                  : serviceUnknown
                    ? serviceUnavailableReason
                    : matchingServices?.length === 0
                      ? '当前没有 Service selector 匹配此 Deployment 的 Pod 标签。'
                      : `selector 匹配：${matchingServices?.join('、')}`}
              </p>
              {onServiceNameChange && (
                <div className='pt-2'>
                  <label
                    className='text-xs font-medium'
                    htmlFor='blue-green-service-entry'
                  >
                    蓝绿切换入口
                  </label>
                  <select
                    id='blue-green-service-entry'
                    aria-label='蓝绿切换入口 Service'
                    className='mt-1.5 h-10 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-2 focus-visible:outline-ring disabled:cursor-not-allowed disabled:opacity-50'
                    value={selectedService ?? ''}
                    disabled={
                      serviceBusy || serviceUnknown || !matchingServices?.length
                    }
                    onChange={(event) =>
                      onServiceNameChange(event.target.value)
                    }
                  >
                    <option value=''>
                      选择一个匹配当前 Deployment 的 Service
                    </option>
                    {selectedService &&
                      !matchingServices?.includes(selectedService) && (
                        <option value={selectedService}>
                          {selectedService} · 当前绑定不匹配
                        </option>
                      )}
                    {(matchingServices ?? []).map((service) => (
                      <option key={service} value={service}>
                        {service}
                      </option>
                    ))}
                  </select>
                  <p className='mt-1 text-xs leading-4 text-muted-foreground'>
                    只绑定明确入口；发布计划会再次读取 Service 并校验 selector。
                  </p>
                </div>
              )}
            </div>
          </CardContent>
        </Card>
        <Card className='shadow-none'>
          <CardContent className='flex items-start gap-3 p-4'>
            <Scaling className='mt-0.5 size-4 shrink-0 text-muted-foreground' />
            <div className='min-w-0 space-y-1'>
              <div className='flex flex-wrap items-center gap-2'>
                <p className='text-sm font-medium'>自动扩缩容</p>
                <Badge
                  variant='outline'
                  className='dark:border-slate-700 dark:text-slate-100'
                >
                  {hpaBusy
                    ? '读取中'
                    : hpaUnknown
                      ? '无法确认'
                      : targetAutoscalers.length
                        ? '发现 HPA'
                        : '未发现 HPA'}
                </Badge>
              </div>
              <p className='text-xs leading-5 break-words text-muted-foreground'>
                {hpaBusy
                  ? '正在检查是否有 HPA 指向此 Deployment。'
                  : hpaUnknown
                    ? preview.isError
                      ? detail(preview.error)
                      : (autoscalingCheck?.detail ?? '无法确认自动扩缩容状态。')
                    : targetAutoscalers?.length
                      ? `${targetAutoscalers.join(
                          '、'
                        )}；当前渐进发布不支持 HPA 目标。`
                      : '未发现指向此 Deployment 的 HPA。'}
              </p>
            </div>
          </CardContent>
        </Card>
        <Card className='shadow-none sm:col-span-2 xl:col-span-1'>
          <CardContent className='flex items-start gap-3 p-4'>
            <Gauge className='mt-0.5 size-4 shrink-0 text-muted-foreground' />
            <div className='min-w-0 space-y-1'>
              <div className='flex flex-wrap items-center gap-2'>
                <p className='text-sm font-medium'>双版本容量</p>
                <Badge variant='outline' className={capacityToneClass}>
                  {capacityStatus}
                </Badge>
              </div>
              <p className='text-xs leading-5 break-words text-muted-foreground'>
                {capacityBusy
                  ? '正在读取 namespace ResourceQuota。'
                  : capacityUnknown
                    ? preview.isError
                      ? detail(preview.error)
                      : (capacityCheck?.detail ??
                        '无法确认 namespace 配额状态。')
                    : (capacityCheck?.detail ?? '尚无容量预检结果。')}
              </p>
            </div>
          </CardContent>
        </Card>
        <Card className='shadow-none sm:col-span-2 xl:col-span-3'>
          <CardContent className='flex items-start gap-3 p-4'>
            <ShieldCheck className='mt-0.5 size-4 shrink-0 text-muted-foreground' />
            <div className='min-w-0 flex-1 space-y-3'>
              <div className='flex flex-wrap items-center gap-2'>
                <h4 className='text-sm font-medium'>发布执行权限</h4>
                <span className='text-xs text-muted-foreground'>
                  使用集群连接中保存的执行身份核对
                </span>
              </div>
              <div className='grid gap-3 sm:grid-cols-2'>
                <div className='min-w-0 space-y-1'>
                  <div className='flex flex-wrap items-center gap-2'>
                    <p className='text-sm'>滚动更新</p>
                    <Badge
                      variant='outline'
                      className={permissionTone(rollingPermission)}
                    >
                      {serviceBusy
                        ? '读取中'
                        : permissionLabel(rollingPermission)}
                    </Badge>
                  </div>
                  <p className='text-xs leading-5 break-words text-muted-foreground'>
                    {rollingPermission?.detail ??
                      '尚未确认执行身份是否能更新此 Deployment。'}
                  </p>
                </div>
                <div className='min-w-0 space-y-1'>
                  <div className='flex flex-wrap items-center gap-2'>
                    <p className='text-sm'>蓝绿发布</p>
                    <Badge
                      variant='outline'
                      className={permissionTone(blueGreenPermission)}
                    >
                      {serviceBusy
                        ? '读取中'
                        : permissionLabel(blueGreenPermission)}
                    </Badge>
                  </div>
                  <p className='text-xs leading-5 break-words text-muted-foreground'>
                    {blueGreenPermission?.detail ??
                      '尚未确认执行身份是否能创建候选资源、切换入口并清理候选。'}
                  </p>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>
      <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
        <p className='w-full min-w-0 text-xs leading-5 text-muted-foreground sm:flex-1'>
          资源关系由当前登录身份读取；发布权限由集群连接配置的执行身份核对。
          计划创建时会重新检查，实际写入仍由 Kubernetes 授权。请为该身份配置目标
          namespace 的 Role/RoleBinding。配额不代表节点容量；Ingress
          与外部网关仍需单独核对，金丝雀需完成网关联调后开放。
        </p>
        <div className='flex w-full shrink-0 items-center justify-between gap-2 sm:w-auto sm:justify-end'>
          {result?.observed_at && (
            <span className='text-xs text-muted-foreground'>
              读取于{' '}
              {new Date(result.observed_at).toLocaleTimeString('zh-CN', {
                hour: '2-digit',
                minute: '2-digit',
              })}
            </span>
          )}
          <Button
            type='button'
            variant='ghost'
            size='sm'
            className='min-h-11 px-3 sm:min-h-8 sm:px-2'
            disabled={!enabled || preview.isFetching}
            onClick={() => void preview.refetch()}
          >
            <RefreshCw
              className={
                preview.isFetching ? 'size-3.5 animate-spin' : 'size-3.5'
              }
            />
            重新预检
          </Button>
        </div>
      </div>
    </section>
  )
}
