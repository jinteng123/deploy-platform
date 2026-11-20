import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import {
  ArrowDown,
  ArrowRight,
  ChevronDown,
  GitBranch,
  Layers3,
  Plus,
  Server,
  ShieldCheck,
} from 'lucide-react'
import {
  platformApi,
  runnerApi,
  type ComponentBinding,
  type DeliveryModel,
  type RunnerEntry,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ThemeSwitch } from '@/components/theme-switch'
import { componentSourceLabel } from './component-binding'
import { ComponentOnboarding } from './component-onboarding'
import { RunnerOnboarding } from './runner-onboarding'

function LogicalBindingSummary({
  appID,
  model,
  pending,
  error,
}: {
  appID: number
  model?: DeliveryModel
  pending: boolean
  error: boolean
}) {
  if (pending) {
    return <span className='text-muted-foreground'>正在读取发布归属…</span>
  }
  if (error) {
    return <span className='text-destructive'>发布关系无法确认</span>
  }
  if (!model) {
    return <span className='text-muted-foreground'>发布关系未知</span>
  }
  const binding = model.bindings.find((item) => item.app_id === appID)
  if (!binding) {
    return <span className='text-muted-foreground'>待关联业务应用与环境</span>
  }
  const application = model.applications.find(
    (item) => item.id === binding.application_id
  )
  const component = model.components.find(
    (item) => item.id === binding.component_id
  )
  const environment = model.environments.find(
    (item) => item.id === binding.environment_id
  )
  return (
    <span className='flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-1'>
      <span className='font-medium'>
        {application?.name ?? '业务应用待核对'} /{' '}
        {component?.name ?? '组件待核对'}
      </span>
      <span className='text-muted-foreground'>→</span>
      <span>{environment?.name ?? '环境待核对'}</span>
      {(binding.requires_review || environment?.requires_review) && (
        <Badge
          variant='outline'
          className='border-amber-300 bg-amber-50 text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200'
        >
          待核对
        </Badge>
      )}
    </span>
  )
}

export function ComponentsPage() {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const role = useAuthStore((s) => s.auth.user?.role)
  const canConfigure = role === 'admin' || role === 'operator'
  const [runnerDialog, setRunnerDialog] = useState<{
    app: ComponentBinding
    initial?: RunnerEntry
  } | null>(null)
  const [guideExpanded, setGuideExpanded] = useState(false)
  const runners = useQuery({
    queryKey: ['runner-bindings'],
    queryFn: () => runnerApi.bindings(),
    enabled: canConfigure,
  })
  const [dialog, setDialog] = useState<{
    editing?: ComponentBinding
    copyFrom?: ComponentBinding
  } | null>(null)
  const apps = useQuery({
    queryKey: ['apps'],
    queryFn: () => platformApi.apps(),
  })
  const deliveryModel = useQuery({
    queryKey: ['delivery-model'],
    queryFn: () => platformApi.deliveryModel(),
  })
  const clusters = useQuery({
    queryKey: ['clusters'],
    queryFn: () => platformApi.clusters(),
  })
  const rows = apps.data?.data ?? []
  const hasEnabledCluster =
    clusters.data?.data.some((cluster) => cluster.enabled) ?? false
  const showGuideSteps = rows.length === 0 || guideExpanded
  const relationshipAction = (app: ComponentBinding, mobile = false) => {
    const layout = mobile ? 'col-span-2 min-h-11' : ''
    if (deliveryModel.isPending) {
      return (
        <Button
          key='relationship'
          size='sm'
          variant='ghost'
          className={layout}
          disabled
        >
          正在读取发布关系
        </Button>
      )
    }
    if (deliveryModel.isError) {
      return (
        <Button
          key='relationship'
          size='sm'
          variant='ghost'
          className={layout}
          onClick={() => void deliveryModel.refetch()}
        >
          重试读取关系
        </Button>
      )
    }
    const binding = deliveryModel.data.data.bindings.find(
      (binding) => binding.app_id === app.id
    )
    if (!binding) {
      return (
        <Button
          key='relationship'
          size='sm'
          variant='default'
          className={layout}
          onClick={() =>
            navigate({ to: '/environments', search: { app_id: app.id } })
          }
        >
          关联业务应用与环境
        </Button>
      )
    }
    const environment = deliveryModel.data.data.environments.find(
      (item) => item.id === binding.environment_id
    )
    if (binding.requires_review || environment?.requires_review) {
      return (
        <Button
          key='relationship'
          size='sm'
          variant='default'
          className={layout}
          onClick={() =>
            navigate({ to: '/environments', search: { app_id: app.id } })
          }
        >
          核对业务归属与环境
        </Button>
      )
    }
    return (
      <Button
        key='relationship'
        size='sm'
        variant='ghost'
        className={layout}
        onClick={() => setDialog({ copyFrom: app })}
      >
        增加部署目标
      </Button>
    )
  }
  return (
    <>
      <Header fixed>
        <div className='me-auto font-semibold'>应用与组件</div>
        <ThemeSwitch />
      </Header>
      <Main>
        <div className='mb-6 flex flex-wrap items-start justify-between gap-4'>
          <div>
            <h1 className='text-2xl font-bold tracking-tight'>应用与组件</h1>
            <p className='mt-2 max-w-2xl text-sm text-muted-foreground'>
              逐步绑定代码仓库、镜像仓库和部署容器，明确每次镜像更新会去哪里。同一组件可以添加多个环境。
            </p>
          </div>
          {canConfigure && (
            <div className='flex flex-col items-end gap-1'>
              <Button
                className='min-h-11'
                onClick={() => setDialog({})}
                disabled={
                  clusters.isPending || clusters.isError || !hasEnabledCluster
                }
                aria-describedby={
                  !clusters.isPending && !clusters.isError && !hasEnabledCluster
                    ? 'component-onboarding-cluster-hint'
                    : undefined
                }
              >
                <Plus />
                接入组件
              </Button>
              {!clusters.isPending &&
                !clusters.isError &&
                !hasEnabledCluster && (
                  <p
                    id='component-onboarding-cluster-hint'
                    className='text-xs text-muted-foreground'
                  >
                    先启用集群，再接入组件
                  </p>
                )}
            </div>
          )}
        </div>
        {hasEnabledCluster && !apps.isPending && !apps.isError && (
          <Card className='mb-6 overflow-hidden'>
            <CardContent className='p-4 sm:p-5'>
              <div className='flex flex-wrap items-start justify-between gap-3'>
                <div className='min-w-0'>
                  <h2 className='text-base font-semibold'>从构建到部署目标</h2>
                  <p className='mt-1 max-w-2xl text-sm leading-6 text-muted-foreground'>
                    构建事件先用 TARGET_CLUSTER 和 APP_NAME 定位组件，再核对
                    IMAGE_FULL 的完整镜像仓库；发布只更新已绑定的容器。
                  </p>
                </div>
                <span className='inline-flex items-center gap-1.5 rounded-full border bg-muted/40 px-2.5 py-1 text-xs text-muted-foreground'>
                  <ShieldCheck className='size-3.5' aria-hidden='true' />
                  保存配置不会发布
                </span>
              </div>
              {showGuideSteps && (
                <ol
                  id='component-onboarding-guide-steps'
                  aria-label='组件接入步骤'
                  className='mt-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-4'
                >
                  {[
                    [
                      GitBranch,
                      '1. 识别可独立更新的组件',
                      'CI 使用 APP_NAME，例如 order-api。一个仓库可按构建目录拆成多个组件。',
                    ],
                    [
                      Layers3,
                      '2. 登记镜像仓库',
                      '填写 CI 实际推送的完整仓库路径；tag 和 digest 表示版本。',
                    ],
                    [
                      Server,
                      '3. 选择更新目标',
                      '从集群选择 namespace、Deployment 和具体容器。',
                    ],
                    [
                      ShieldCheck,
                      '4. 关联业务应用与环境',
                      '把部署目标关联到业务应用、逻辑环境，再选择发布策略。',
                    ],
                  ].map(([Icon, title, desc]) => {
                    const StepIcon = Icon
                    return (
                      <li
                        key={title as string}
                        className='min-w-0 rounded-lg border bg-background p-4'
                      >
                        <div className='flex items-start gap-2'>
                          <span className='flex size-7 shrink-0 items-center justify-center rounded-md bg-primary/10 text-primary'>
                            <StepIcon className='size-3.5' aria-hidden='true' />
                          </span>
                          <div className='min-w-0'>
                            <p className='text-sm leading-5 font-semibold'>
                              {title as string}
                            </p>
                            <p className='mt-2 text-sm leading-5 text-muted-foreground'>
                              {desc as string}
                            </p>
                          </div>
                        </div>
                      </li>
                    )
                  })}
                </ol>
              )}
              {showGuideSteps && (
                <div className='mt-4 rounded-lg bg-muted/40 p-3 sm:p-4'>
                  <div className='flex min-w-0 flex-col items-center gap-2 text-sm sm:flex-row'>
                    <code className='w-full min-w-0 rounded-md border bg-background px-3 py-2 font-mono text-xs break-all sm:w-auto'>
                      TARGET_CLUSTER=order-test · APP_NAME=order-api
                    </code>
                    <ArrowRight
                      className='hidden size-4 shrink-0 text-muted-foreground sm:block'
                      aria-hidden='true'
                    />
                    <ArrowDown
                      className='size-4 shrink-0 text-muted-foreground sm:hidden'
                      aria-hidden='true'
                    />
                    <span className='w-full min-w-0 rounded-md border bg-background px-3 py-2 font-mono text-xs break-all sm:w-auto'>
                      registry.example.com/orders/api@sha256:…
                    </span>
                    <ArrowRight
                      className='hidden size-4 shrink-0 text-muted-foreground sm:block'
                      aria-hidden='true'
                    />
                    <ArrowDown
                      className='size-4 shrink-0 text-muted-foreground sm:hidden'
                      aria-hidden='true'
                    />
                    <span className='w-full min-w-0 rounded-md border bg-background px-3 py-2 text-xs sm:w-auto'>
                      测试集群 / orders / Deployment / api 容器
                    </span>
                  </div>
                  <p className='mt-3 text-sm leading-6 text-muted-foreground'>
                    测试、预发和生产可使用同一组件与镜像仓库，分别绑定明确的环境和部署目标。
                  </p>
                </div>
              )}
              {rows.length > 0 && (
                <Button
                  type='button'
                  size='sm'
                  variant='ghost'
                  className='mt-3 min-h-11 gap-2'
                  aria-expanded={guideExpanded}
                  onClick={() => setGuideExpanded((value) => !value)}
                >
                  {guideExpanded ? '收起接入步骤' : '查看接入步骤'}
                  <ChevronDown
                    className={`size-4 transition-transform ${guideExpanded ? 'rotate-180' : ''}`}
                    aria-hidden='true'
                  />
                </Button>
              )}
            </CardContent>
          </Card>
        )}
        {canConfigure && runners.isError && (
          <div
            role='alert'
            className='mb-4 flex flex-wrap items-center gap-3 text-sm'
          >
            <p className='text-destructive'>
              无法读取 Runner 接入配置，请检查后端和数据库迁移。
            </p>
            <Button variant='outline' onClick={() => runners.refetch()}>
              重试读取
            </Button>
          </div>
        )}
        {apps.isPending || clusters.isPending ? (
          <p className='p-6 text-muted-foreground'>加载组件与环境…</p>
        ) : apps.isError || clusters.isError ? (
          <div role='alert' className='space-y-3'>
            <p className='text-destructive'>无法读取组件或集群配置。</p>
            <Button
              variant='outline'
              onClick={() => {
                void apps.refetch()
                void clusters.refetch()
              }}
            >
              重新加载
            </Button>
          </div>
        ) : !rows.length ? (
          <Card>
            <CardContent className='space-y-3 p-8 text-center'>
              {!hasEnabledCluster ? (
                <>
                  <h2 className='font-semibold'>先接入可用集群</h2>
                  <p className='mx-auto max-w-md text-sm text-muted-foreground'>
                    组件接入需要从已启用的集群选择 Deployment
                    和容器。当前没有可用集群，先完成集群连接与启用。
                  </p>
                  {role === 'admin' ? (
                    <Button
                      className='min-h-11'
                      onClick={() => navigate({ to: '/settings/clusters' })}
                    >
                      前往集群设置
                    </Button>
                  ) : (
                    <p className='text-sm text-muted-foreground'>
                      请联系管理员接入并启用集群。
                    </p>
                  )}
                </>
              ) : (
                <>
                  <h2 className='font-semibold'>还没有组件绑定</h2>
                  <p className='text-sm text-muted-foreground'>
                    从一个服务开始，例如
                    order-api。引导会帮助你核对仓库和部署位置。
                  </p>
                </>
              )}
              {!canConfigure && hasEnabledCluster && (
                <p className='text-sm text-muted-foreground'>
                  请联系操作员接入组件。
                </p>
              )}
            </CardContent>
          </Card>
        ) : (
          <Card className='overflow-hidden'>
            <div className='divide-y xl:hidden'>
              {rows.map((app) => {
                const cluster = clusters.data?.data.find(
                  (item) => item.id === app.cluster_id
                )
                const runnerAvailable = clusters.data?.data.some(
                  (item) =>
                    item.id === app.cluster_id &&
                    item.enabled &&
                    item.environment_tier === 'non_production' &&
                    item.deploy_policy === 'automatic'
                )
                return (
                  <article key={app.id} className='min-w-0 p-4 sm:p-5'>
                    <div className='flex min-w-0 flex-wrap items-start justify-between gap-2'>
                      <div className='min-w-0'>
                        <h2 className='font-semibold break-words'>
                          {app.app_name}
                        </h2>
                        <p className='mt-1 text-xs break-all text-muted-foreground'>
                          {componentSourceLabel(
                            app.source_kind,
                            app.source_repo_url
                          )}
                          {app.source_kind === 'repository' &&
                            app.source_repo_url &&
                            ` · ${app.source_path || '.'}`}
                        </p>
                      </div>
                      <Badge
                        variant={app.image_repository ? 'outline' : 'secondary'}
                      >
                        {app.image_repository
                          ? '镜像仓库已绑定'
                          : '镜像仓库待配置'}
                      </Badge>
                    </div>
                    <dl className='mt-4 grid min-w-0 gap-3'>
                      <div className='min-w-0'>
                        <dt className='text-xs font-medium text-muted-foreground'>
                          镜像仓库
                        </dt>
                        <dd className='mt-1 font-mono text-xs break-all'>
                          {app.image_repository || '尚未限定镜像仓库'}
                        </dd>
                      </div>
                      <div className='min-w-0'>
                        <dt className='text-xs font-medium text-muted-foreground'>
                          业务应用 / 逻辑组件 / 环境
                        </dt>
                        <dd className='mt-1 text-sm break-words'>
                          <LogicalBindingSummary
                            appID={app.id}
                            model={deliveryModel.data?.data}
                            pending={deliveryModel.isPending}
                            error={deliveryModel.isError}
                          />
                        </dd>
                      </div>
                      <div className='min-w-0'>
                        <dt className='text-xs font-medium text-muted-foreground'>
                          部署目标
                        </dt>
                        <dd className='mt-1 text-sm break-words'>
                          {cluster?.name ?? `集群 #${app.cluster_id}`}
                          <span className='text-muted-foreground'>
                            {' · '}
                            {app.namespace} / {app.deployment_name} /{' '}
                            {app.container_name}
                          </span>
                        </dd>
                      </div>
                    </dl>
                    {canConfigure && (
                      <div className='mt-4 grid grid-cols-2 gap-2'>
                        <Button
                          size='sm'
                          variant='outline'
                          className='min-h-11'
                          onClick={() => setDialog({ editing: app })}
                        >
                          {app.image_repository ? '编辑绑定' : '补全镜像仓库'}
                        </Button>
                        <Button
                          size='sm'
                          variant='outline'
                          className='min-h-11'
                          disabled={
                            !app.image_repository ||
                            runners.isPending ||
                            runners.isError ||
                            !runnerAvailable
                          }
                          onClick={() =>
                            setRunnerDialog({
                              app,
                              initial: runners.data?.data.find(
                                (entry) => entry.binding.app_id === app.id
                              ),
                            })
                          }
                        >
                          接入云效 Runner
                        </Button>
                        {relationshipAction(app, true)}
                      </div>
                    )}
                  </article>
                )
              })}
            </div>
            <CardContent className='hidden overflow-x-auto p-0 xl:block'>
              <table className='w-full min-w-[1120px] text-sm'>
                <thead className='bg-muted/50'>
                  <tr>
                    {[
                      '组件',
                      '代码来源',
                      '镜像仓库',
                      '部署目标',
                      '业务应用 / 逻辑组件 / 环境',
                      '操作',
                    ].map((column) => (
                      <th
                        key={column}
                        className='px-4 py-3 text-left font-medium'
                      >
                        {column}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map((a) => (
                    <tr key={a.id} className='border-t align-top'>
                      <td className='space-y-2 px-4 py-4'>
                        <p className='font-semibold'>{a.app_name}</p>
                        <Badge variant='outline'>
                          {a.image_repository
                            ? '镜像仓库已绑定'
                            : '镜像仓库待配置'}
                        </Badge>
                      </td>
                      <td className='max-w-64 px-4 py-4 break-all'>
                        {componentSourceLabel(a.source_kind, a.source_repo_url)}
                        {a.source_kind === 'repository' &&
                          a.source_repo_url && (
                            <p className='mt-2 font-mono text-xs text-muted-foreground'>
                              {a.source_path || '.'}
                            </p>
                          )}
                      </td>
                      <td className='max-w-64 px-4 py-4 font-mono text-xs break-all'>
                        {a.image_repository || '尚未限定镜像仓库'}
                      </td>
                      <td className='px-4 py-4'>
                        <p>
                          {clusters.data?.data.find(
                            (c) => c.id === a.cluster_id
                          )?.name ?? `集群 #${a.cluster_id}`}
                        </p>
                        <p className='mt-2 text-xs text-muted-foreground'>
                          {a.namespace} / {a.deployment_name} /{' '}
                          {a.container_name}
                        </p>
                      </td>
                      <td className='min-w-64 px-4 py-4 text-xs'>
                        <LogicalBindingSummary
                          appID={a.id}
                          model={deliveryModel.data?.data}
                          pending={deliveryModel.isPending}
                          error={deliveryModel.isError}
                        />
                      </td>
                      <td className='px-4 py-3'>
                        {canConfigure && (
                          <div className='flex flex-wrap gap-2'>
                            <Button
                              size='sm'
                              variant='outline'
                              onClick={() => setDialog({ editing: a })}
                            >
                              {a.image_repository ? '编辑绑定' : '补全镜像仓库'}
                            </Button>
                            <Button
                              size='sm'
                              variant='outline'
                              disabled={
                                !a.image_repository ||
                                runners.isPending ||
                                runners.isError ||
                                !clusters.data?.data.some(
                                  (c) =>
                                    c.id === a.cluster_id &&
                                    c.enabled &&
                                    c.environment_tier === 'non_production' &&
                                    c.deploy_policy === 'automatic'
                                )
                              }
                              onClick={() =>
                                setRunnerDialog({
                                  app: a,
                                  initial: runners.data?.data.find(
                                    (e) => e.binding.app_id === a.id
                                  ),
                                })
                              }
                            >
                              接入云效 Runner
                            </Button>
                            {relationshipAction(a)}
                          </div>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </CardContent>
          </Card>
        )}
        {dialog && (
          <ComponentOnboarding
            {...dialog}
            clusters={clusters.data?.data ?? []}
            existing={rows}
            onClose={() => setDialog(null)}
            onNextStep={(appID) =>
              navigate({ to: '/environments', search: { app_id: appID } })
            }
            onSaved={() => {
              void queryClient.invalidateQueries({ queryKey: ['apps'] })
              void queryClient.invalidateQueries({
                queryKey: ['promotion-targets'],
              })
              void queryClient.invalidateQueries({ queryKey: ['releases'] })
            }}
          />
        )}
        {runnerDialog && (
          <RunnerOnboarding
            {...runnerDialog}
            onClose={() => setRunnerDialog(null)}
          />
        )}
      </Main>
    </>
  )
}
