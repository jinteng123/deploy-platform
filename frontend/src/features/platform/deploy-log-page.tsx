import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  AlertTriangle,
  ArrowLeft,
  ArrowUpRight,
  CheckCircle2,
  CircleHelp,
  Clock3,
  RefreshCw,
  Server,
} from 'lucide-react'
import { platformApi, type ImageReleaseView } from '@/api/client'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import {
  componentSourceLabel,
  sourceRepositoryIdentity,
} from './component-binding'
import { StatusBadge, DeployStepTimeline } from './release-detail'
import {
  deployLogRunning,
  deployStepName,
  releaseBuildIdentity,
} from './release-status'

function displayTime(value?: string | null) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN')
}

function NextAction({ status }: { status: string }) {
  if (status === 'success') {
    return (
      <Alert className='border-emerald-300/70 bg-emerald-50/70 text-emerald-950 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-100'>
        <CheckCircle2 aria-hidden='true' />
        <AlertTitle>发布任务已完成</AlertTitle>
        <AlertDescription>
          目标镜像已完成本次部署，无需进一步操作。
        </AlertDescription>
      </Alert>
    )
  }
  if (status === 'interrupted') {
    return (
      <Alert className='border-amber-300/70 bg-amber-50/70 text-amber-950 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-100'>
        <CircleHelp aria-hidden='true' />
        <AlertTitle>先核对集群实际状态</AlertTitle>
        <AlertDescription>
          任务在外部操作结果无法确认时停止。请核对集群中的实际镜像和资源状态；确认未知写入前不要直接重试。
        </AlertDescription>
      </Alert>
    )
  }
  if (status === 'failed' || status === 'cancelled') {
    return (
      <Alert variant='destructive'>
        <AlertTriangle aria-hidden='true' />
        <AlertTitle>
          {status === 'cancelled' ? '任务已取消' : '发布任务失败'}
        </AlertTitle>
        <AlertDescription>
          查看失败步骤与错误信息，修复原因后再从发布中心重新发起。
        </AlertDescription>
      </Alert>
    )
  }
  return (
    <Alert className='border-blue-300/70 bg-blue-50/70 text-blue-950 dark:border-blue-900 dark:bg-blue-950/30 dark:text-blue-100'>
      <Clock3 aria-hidden='true' />
      <AlertTitle>任务仍在执行</AlertTitle>
      <AlertDescription>页面会自动刷新任务状态与执行步骤。</AlertDescription>
    </Alert>
  )
}

function InfoItem({ label, value }: { label: string; value?: string | null }) {
  return (
    <div className='min-w-0 space-y-1'>
      <dt className='text-xs font-medium tracking-wide text-muted-foreground'>
        {label}
      </dt>
      <dd className='text-sm leading-5 break-all text-foreground'>
        {value || '—'}
      </dd>
    </div>
  )
}

function sourceBindingStatus(release: ImageReleaseView) {
  const binding = release.metadata?.binding
  if (!binding)
    return { label: '制品发布没有组件绑定快照', tone: 'neutral' as const }
  if (binding.source_kind === 'third_party')
    return { label: '组件按第三方镜像接入', tone: 'neutral' as const }
  if (binding.source_kind === 'unknown')
    return { label: '组件代码来源尚未确认', tone: 'warning' as const }
  if (!binding.source_repo_url)
    return { label: '组件登记仓库未配置', tone: 'warning' as const }

  const sourceRepositories = release.metadata?.source_repositories ?? []
  if (sourceRepositories.length === 0)
    return { label: '本次构建未报告代码仓库', tone: 'warning' as const }

  const expectedRepository = sourceRepositoryIdentity(
    binding.source_repo_url ?? ''
  )
  if (!expectedRepository)
    return { label: '组件登记仓库无法核对', tone: 'warning' as const }

  const repositoryMatched = sourceRepositories.some(
    (repository) => sourceRepositoryIdentity(repository) === expectedRepository
  )
  if (!repositoryMatched)
    return { label: '代码仓库与组件绑定不一致', tone: 'warning' as const }

  const expectedPath = binding.source_path || '.'
  const buildPath = release.metadata?.source_path || '.'
  return expectedPath === buildPath
    ? { label: '仓库与目录已匹配', tone: 'positive' as const }
    : { label: '仓库已匹配，构建目录不一致', tone: 'warning' as const }
}

function ReleaseBuildContext({
  releaseID,
  taskImage,
}: {
  releaseID?: number
  taskImage?: string | null
}) {
  const validReleaseID = Number.isSafeInteger(releaseID) && (releaseID ?? 0) > 0
  const query = useQuery({
    queryKey: ['release', releaseID],
    queryFn: () => platformApi.release(releaseID!),
    enabled: validReleaseID,
    retry: false,
  })
  const release = query.data?.data
  const bindingStatus = release ? sourceBindingStatus(release) : undefined
  const releaseImageMatches = release?.image_full === taskImage

  return (
    <Card className='min-w-0' data-testid='release-build-context-card'>
      <CardHeader className='border-b'>
        <CardTitle className='text-base'>构建与组件绑定</CardTitle>
        <CardDescription>
          从任务记录反查对应制品、代码仓库与镜像绑定
        </CardDescription>
      </CardHeader>
      <CardContent className='space-y-4 pt-5'>
        {!validReleaseID ? (
          <p className='text-sm leading-6 text-muted-foreground'>
            本任务没有关联制品发布单，可能来自旧版或手动部署。
          </p>
        ) : query.isLoading ? (
          <div className='space-y-3' aria-label='正在加载关联制品'>
            <Skeleton className='h-10 w-full' />
            <Skeleton className='h-14 w-full' />
          </div>
        ) : query.isError || !release ? (
          <p
            role='status'
            className='text-sm leading-6 text-amber-800 dark:text-amber-200'
          >
            已关联制品发布单 #{releaseID}
            ，但当前无法读取其构建来源；部署任务详情仍可正常核对。
          </p>
        ) : (
          <>
            <dl className='grid min-w-0 gap-4 sm:grid-cols-2'>
              <InfoItem
                label='制品发布'
                value={`#${release.id} · ${release.app_name}`}
              />
              <InfoItem
                label='云效流水线 / 构建'
                value={releaseBuildIdentity(release)}
              />
              <InfoItem
                label='构建代码仓库'
                value={
                  release.metadata?.source_repositories?.join(' · ') ||
                  '本次构建未报告代码仓库'
                }
              />
              <InfoItem
                label='构建目录'
                value={
                  release.metadata?.source_path ||
                  (release.metadata?.binding?.source_path &&
                  release.metadata.binding.source_path !== '.'
                    ? `未报告（组件要求 ${release.metadata.binding.source_path}）`
                    : '未单独报告（按仓库根目录）')
                }
              />
              <InfoItem
                label='组件登记仓库 / 目录'
                value={
                  release.metadata?.binding?.source_kind === 'repository'
                    ? `${release.metadata.binding.source_repo_url} · ${release.metadata.binding.source_path || '.'}`
                    : componentSourceLabel(
                        release.metadata?.binding?.source_kind,
                        release.metadata?.binding?.source_repo_url
                      )
                }
              />
              <InfoItem
                label='绑定镜像仓库'
                value={release.metadata?.binding?.image_repository}
              />
            </dl>
            <div className='flex flex-wrap items-center gap-2 border-t pt-4'>
              <Badge
                variant='outline'
                data-testid='release-source-binding-status'
                className={
                  bindingStatus?.tone === 'positive'
                    ? 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-200'
                    : bindingStatus?.tone === 'warning'
                      ? 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-100'
                      : 'border-border bg-muted text-muted-foreground'
                }
              >
                {bindingStatus?.label}
              </Badge>
              <Badge
                variant='outline'
                data-testid='release-task-image-status'
                className={
                  !taskImage
                    ? 'border-border bg-muted text-muted-foreground'
                    : releaseImageMatches
                      ? 'border-emerald-300 bg-emerald-50 text-emerald-800 dark:border-emerald-800 dark:bg-emerald-950/50 dark:text-emerald-200'
                      : 'border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950/50 dark:text-amber-100'
                }
              >
                {!taskImage
                  ? '任务未记录镜像，无法核对'
                  : releaseImageMatches
                    ? '部署镜像与发布制品一致'
                    : '部署镜像与发布制品不一致'}
              </Badge>
              {validReleaseID && (
                <Button
                  asChild
                  variant='link'
                  size='sm'
                  className='min-h-11 px-1 font-medium'
                >
                  <Link to='/tasks' search={{ release_id: release.id }}>
                    <ArrowUpRight aria-hidden='true' />
                    查看发布单详情
                  </Link>
                </Button>
              )}
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}

export function DeployLogPage({ logID }: { logID: number }) {
  const validID = Number.isSafeInteger(logID) && logID > 0
  const query = useQuery({
    queryKey: ['deploy-log', logID],
    queryFn: () => platformApi.deployLog(logID),
    enabled: validID,
    retry: false,
    refetchInterval: (state) =>
      deployLogRunning(state.state.data?.data?.status) ? 2000 : false,
  })
  const clustersQuery = useQuery({
    queryKey: ['clusters'],
    queryFn: () => platformApi.clusters(),
    retry: false,
  })
  const log = query.data?.data
  const cluster = clustersQuery.data?.data.find(
    (item) => item.id === log?.cluster_id
  )
  const clusterLabel = cluster
    ? `${cluster.name} (${cluster.env_code})`
    : log
      ? `集群 #${log.cluster_id}`
      : undefined
  const clusterCode =
    cluster?.env_code ?? (log ? `集群 #${log.cluster_id}` : '')
  const responseStatus = (
    query.error as { response?: { status?: number } } | null
  )?.response?.status

  return (
    <>
      <Header fixed>
        <div className='flex min-w-0 flex-1 items-center justify-between gap-3'>
          <div className='min-w-0'>
            <h1 className='truncate text-base font-semibold'>任务详情</h1>
            <p className='hidden text-xs text-muted-foreground sm:block'>
              部署执行记录与目标状态
            </p>
          </div>
          <Button
            asChild
            variant='outline'
            size='sm'
            className='min-h-10 shrink-0'
          >
            <a href='../../' aria-label='返回发布中心'>
              <ArrowLeft aria-hidden='true' />
              <span>发布中心</span>
            </a>
          </Button>
        </div>
      </Header>
      <Main className='space-y-5 pb-10'>
        {!validID ? (
          <Alert variant='destructive'>
            <AlertTriangle aria-hidden='true' />
            <AlertTitle>任务编号无效</AlertTitle>
            <AlertDescription>请从通知中的任务链接重新打开。</AlertDescription>
          </Alert>
        ) : query.isLoading ? (
          <div className='space-y-5' aria-label='正在加载任务详情'>
            <Skeleton className='h-36 w-full rounded-xl' />
            <div className='grid gap-5 xl:grid-cols-[minmax(0,1.6fr)_minmax(18rem,1fr)]'>
              <Skeleton className='h-72 rounded-xl' />
              <Skeleton className='h-72 rounded-xl' />
            </div>
          </div>
        ) : query.isError || !log ? (
          <Card className='mx-auto mt-8 max-w-xl'>
            <CardHeader>
              <CardTitle>
                {responseStatus === 404
                  ? '找不到这条任务'
                  : '任务详情暂时无法加载'}
              </CardTitle>
              <CardDescription>
                {responseStatus === 404
                  ? '任务可能已被清理，或当前账号没有查看权限。'
                  : '请检查平台连接后重试。'}
              </CardDescription>
            </CardHeader>
            {responseStatus !== 404 && (
              <CardContent>
                <Button
                  variant='outline'
                  className='min-h-11'
                  onClick={() => void query.refetch()}
                >
                  <RefreshCw aria-hidden='true' />
                  重试
                </Button>
              </CardContent>
            )}
          </Card>
        ) : (
          <>
            <Card className='overflow-hidden'>
              <div className='h-1 bg-gradient-to-r from-primary via-primary/60 to-transparent' />
              <CardContent className='flex flex-col gap-5 pt-6 sm:flex-row sm:items-center sm:justify-between'>
                <div className='min-w-0 space-y-2'>
                  <div className='flex flex-wrap items-center gap-2'>
                    <p className='min-w-0 font-mono text-xs break-words text-muted-foreground'>
                      任务 #{log.id} · {clusterCode}
                    </p>
                    <StatusBadge status={log.status} />
                  </div>
                  <h2 className='text-2xl leading-tight font-semibold tracking-tight break-words'>
                    {log.app_name}
                  </h2>
                  <p className='text-sm text-muted-foreground'>
                    {log.deployment_name}/{log.container_name} · {log.namespace}
                  </p>
                </div>
                <div className='grid grid-cols-2 gap-x-6 gap-y-3 rounded-lg bg-muted/50 p-4 sm:min-w-64'>
                  <div className='space-y-1'>
                    <p className='text-xs text-muted-foreground'>开始时间</p>
                    <p className='text-sm'>
                      {displayTime(log.started_at || log.created_at)}
                    </p>
                  </div>
                  <div className='space-y-1'>
                    <p className='text-xs text-muted-foreground'>结束时间</p>
                    <p className='text-sm'>{displayTime(log.finished_at)}</p>
                  </div>
                </div>
              </CardContent>
            </Card>

            <div className='grid min-w-0 gap-5 xl:grid-cols-[minmax(0,1.6fr)_minmax(18rem,1fr)]'>
              <div className='min-w-0 space-y-5'>
                <ReleaseBuildContext
                  releaseID={log.release_id}
                  taskImage={log.image_full}
                />
                <NextAction status={log.status} />
                <Card>
                  <CardHeader className='border-b'>
                    <CardTitle className='text-base'>执行进度</CardTitle>
                    <CardDescription>
                      {log.current_step
                        ? `当前步骤：${deployStepName(log.current_step)}`
                        : '按时间顺序查看本次任务步骤'}
                    </CardDescription>
                  </CardHeader>
                  <CardContent className='pt-5'>
                    <DeployStepTimeline log={log} />
                  </CardContent>
                </Card>
                {log.error_message && (
                  <Alert variant='destructive' className='min-w-0'>
                    <AlertTriangle aria-hidden='true' />
                    <AlertTitle>任务错误信息</AlertTitle>
                    <AlertDescription>
                      <pre className='max-h-72 overflow-auto font-sans text-sm break-words whitespace-pre-wrap'>
                        {log.error_message}
                      </pre>
                    </AlertDescription>
                  </Alert>
                )}
              </div>

              <Card className='min-w-0 self-start'>
                <CardHeader className='border-b'>
                  <CardTitle className='flex items-center gap-2 text-base'>
                    <Server
                      aria-hidden='true'
                      className='size-4 text-primary'
                    />
                    发布目标
                  </CardTitle>
                  <CardDescription>
                    本次任务对应的集群资源与镜像
                  </CardDescription>
                </CardHeader>
                <CardContent className='pt-5'>
                  <dl className='grid gap-4 sm:grid-cols-2 xl:grid-cols-1'>
                    <InfoItem label='目标集群' value={clusterLabel} />
                    <InfoItem label='命名空间' value={log.namespace} />
                    <InfoItem label='Deployment' value={log.deployment_name} />
                    <InfoItem label='容器' value={log.container_name} />
                    <InfoItem label='发布类型' value={log.deploy_type} />
                    <InfoItem label='版本' value={log.version} />
                    <div className='min-w-0 space-y-1 sm:col-span-2 xl:col-span-1'>
                      <dt className='text-xs font-medium tracking-wide text-muted-foreground'>
                        目标镜像
                      </dt>
                      <dd className='rounded-md bg-muted/70 p-3 font-mono text-xs leading-5 break-all text-foreground'>
                        {log.image_full || '—'}
                      </dd>
                    </div>
                  </dl>
                  {log.environment_tier_snapshot && (
                    <div className='mt-5 flex items-center gap-2 border-t pt-4'>
                      <Badge variant='outline'>
                        {log.environment_tier_snapshot === 'production'
                          ? '生产环境'
                          : '非生产环境'}
                      </Badge>
                      <span className='text-xs text-muted-foreground'>
                        任务创建时环境等级
                      </span>
                    </div>
                  )}
                </CardContent>
              </Card>
            </div>
          </>
        )}
      </Main>
    </>
  )
}
