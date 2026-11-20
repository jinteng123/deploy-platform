import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  ArrowUpRight,
  Boxes,
  CheckCircle2,
  Clock3,
  Plus,
  Server,
} from 'lucide-react'
import { platformApi } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState } from '@/components/empty-state'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ProfileDropdown } from '@/components/profile-dropdown'
import { ThemeSwitch } from '@/components/theme-switch'

export function Dashboard() {
  const clusters = useQuery({
    queryKey: ['clusters'],
    queryFn: () => platformApi.clusters(),
  })
  const apps = useQuery({
    queryKey: ['apps'],
    queryFn: () => platformApi.apps(),
  })
  const logs = useQuery({
    queryKey: ['deploy-logs'],
    queryFn: () => platformApi.logs(),
  })

  const clusterList = clusters.data?.data ?? []
  const records = logs.data?.data ?? []
  const successCount = records.filter(
    (item) => item.status === 'success'
  ).length
  const runningCount = records.filter(
    (item) => item.status === 'running'
  ).length

  return (
    <>
      <Header fixed>
        <div className='me-auto flex items-center gap-2 text-sm'>
          <span className='text-[#6f88ac]'>首页</span>
          <span className='text-[#b0bfd4]'>/</span>
          <span className='font-semibold text-[#214b8d]'>平台概览</span>
        </div>
        <ThemeSwitch />
        <ProfileDropdown />
      </Header>

      <Main>
        <div className='mb-6 flex flex-wrap items-end justify-between gap-4'>
          <div>
            <h1 className='text-2xl font-bold tracking-tight text-[#1c355e]'>
              平台概览
            </h1>
            <p className='mt-1 text-sm text-muted-foreground'>
              统一查看集群、应用与最近发布状态。
            </p>
          </div>
          <Button
            asChild
            className='rounded-lg bg-[#24549a] shadow-sm hover:bg-[#1d4682]'
          >
            <Link to='/settings/clusters'>
              <Plus className='size-4' />
              管理集群
            </Link>
          </Button>
        </div>

        <div className='grid gap-4 sm:grid-cols-2 xl:grid-cols-4'>
          <Metric
            title='集群环境'
            value={clusterList.length}
            description='已接入 Kubernetes 集群'
            icon={<Server className='size-4' />}
          />
          <Metric
            title='已注册应用'
            value={apps.data?.data.length ?? 0}
            description='可执行发布的应用'
            icon={<Boxes className='size-4' />}
          />
          <Metric
            title='发布成功'
            value={successCount}
            description='历史发布成功记录'
            icon={<CheckCircle2 className='size-4' />}
          />
          <Metric
            title='执行中'
            value={runningCount}
            description='正在处理的发布任务'
            icon={<Clock3 className='size-4' />}
          />
        </div>

        <div className='mt-6 grid gap-6 xl:grid-cols-[1.35fr_1fr]'>
          <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.06)]'>
            <CardHeader className='flex flex-row items-center justify-between gap-4 space-y-0'>
              <div>
                <CardTitle className='text-base text-[#1c355e]'>
                  Kubernetes 集群
                </CardTitle>
                <p className='mt-1 text-xs text-muted-foreground'>
                  选择集群进入资源控制台
                </p>
              </div>
              <Button
                asChild
                variant='ghost'
                size='sm'
                className='text-[#24549a]'
              >
                <Link to='/settings/clusters'>
                  查看全部
                  <ArrowUpRight className='ms-1 size-4' />
                </Link>
              </Button>
            </CardHeader>
            <CardContent>
              {clusterList.length === 0 ? (
                <EmptyState text='暂无已接入集群' />
              ) : (
                <div className='grid gap-3 md:grid-cols-2'>
                  {clusterList.slice(0, 6).map((cluster) => (
                    <Link
                      key={cluster.id}
                      to='/settings/clusters'
                      className='group rounded-xl border border-[#e1e9f5] bg-[#fbfdff] p-4 transition hover:-translate-y-0.5 hover:border-[#9bb6dd] hover:shadow-md'
                    >
                      <div className='flex items-start justify-between gap-3'>
                        <div className='flex min-w-0 items-center gap-3'>
                          <span className='flex size-9 shrink-0 items-center justify-center rounded-xl bg-[#eaf1fc] text-[#24549a]'>
                            <Server className='size-4' />
                          </span>
                          <div className='min-w-0'>
                            <p className='truncate font-semibold text-[#1c355e]'>
                              {cluster.name}
                            </p>
                            <p className='mt-1 truncate text-xs text-muted-foreground'>
                              {cluster.environment ||
                                cluster.env_code ||
                                '未设置环境'}
                            </p>
                          </div>
                        </div>
                        <ArrowUpRight className='size-4 shrink-0 text-[#9aaec9] transition group-hover:text-[#24549a]' />
                      </div>
                      <div className='mt-4 flex items-center justify-between gap-3 border-t border-[#edf1f7] pt-3 text-xs text-muted-foreground'>
                        <span className='truncate'>
                          {cluster.default_namespace || 'default'}
                        </span>
                        <Badge
                          variant='outline'
                          className={
                            cluster.enabled
                              ? 'border-emerald-200 bg-emerald-50 text-emerald-700'
                              : 'border-slate-200 bg-slate-50 text-slate-500'
                          }
                        >
                          {cluster.enabled ? '已启用' : '已停用'}
                        </Badge>
                      </div>
                    </Link>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>

          <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.06)]'>
            <CardHeader>
              <CardTitle className='text-base text-[#1c355e]'>
                最近发布
              </CardTitle>
              <p className='mt-1 text-xs text-muted-foreground'>
                最近 8 条发布记录
              </p>
            </CardHeader>
            <CardContent>
              {records.length === 0 ? (
                <EmptyState text='暂无发布记录' />
              ) : (
                <div className='space-y-1'>
                  {records.slice(0, 8).map((record) => (
                    <div
                      key={record.id}
                      className='flex items-center justify-between gap-4 rounded-lg px-3 py-3 transition hover:bg-[#f5f8fd]'
                    >
                      <div className='min-w-0'>
                        <p className='truncate text-sm font-medium text-[#2a4165]'>
                          {record.app_name}
                        </p>
                        <p className='mt-1 truncate text-xs text-muted-foreground'>
                          {record.version}
                        </p>
                      </div>
                      <Badge variant='outline' className='shrink-0 text-xs'>
                        {record.status}
                      </Badge>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      </Main>
    </>
  )
}

function Metric({
  title,
  value,
  description,
  icon,
}: {
  title: string
  value: number
  description: string
  icon: React.ReactNode
}) {
  return (
    <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.05)]'>
      <CardHeader className='flex flex-row items-center justify-between space-y-0 pb-2'>
        <CardTitle className='text-sm font-medium text-[#587092]'>
          {title}
        </CardTitle>
        <span className='flex size-8 items-center justify-center rounded-lg bg-[#eaf1fc] text-[#24549a]'>
          {icon}
        </span>
      </CardHeader>
      <CardContent>
        <div className='text-2xl font-bold text-[#1c355e]'>{value}</div>
        <p className='mt-1 text-xs text-muted-foreground'>{description}</p>
      </CardContent>
    </Card>
  )
}
