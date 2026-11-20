import { useQueries, useQueryClient } from '@tanstack/react-query'
import { Boxes, ExternalLink, RefreshCw } from 'lucide-react'
import { k8sApi, type K8sObject } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { EmptyState } from '@/components/empty-state'
import { age } from './age'
import { get, str } from './catalog'
import type { K8sDetailQuery } from './k8s-page'

type Props = {
  clusterId: number
  namespace: string
  onOpenDetail: (query: K8sDetailQuery) => void
  onOpenResource: (resource: string) => void
}

const WORKLOAD_TYPES = [
  {
    resource: 'deployments',
    group: 'apps',
    version: 'v1',
    title: 'Deployment',
  },
  {
    resource: 'statefulsets',
    group: 'apps',
    version: 'v1',
    title: 'StatefulSet',
  },
  { resource: 'daemonsets', group: 'apps', version: 'v1', title: 'DaemonSet' },
  { resource: 'jobs', group: 'batch', version: 'v1', title: 'Job' },
  { resource: 'cronjobs', group: 'batch', version: 'v1', title: 'CronJob' },
] as const

const replicas = (workload: K8sObject) => {
  const desired = Number(get(workload, 'spec.replicas'))
  const ready = Number(get(workload, 'status.readyReplicas'))
  if (Number.isFinite(desired) || Number.isFinite(ready)) {
    const resolvedDesired = Number.isFinite(desired)
      ? desired
      : Math.max(ready, 1)
    return `${Number.isFinite(ready) ? ready : 0}/${resolvedDesired}`
  }
  const scheduled = Number(get(workload, 'status.desiredNumberScheduled'))
  const daemonReady = Number(get(workload, 'status.numberReady'))
  if (Number.isFinite(scheduled) || Number.isFinite(daemonReady)) {
    return `${Number.isFinite(daemonReady) ? daemonReady : 0}/${Number.isFinite(scheduled) ? scheduled : 0}`
  }
  const active = Number(get(workload, 'status.active')) || 0
  const succeeded = Number(get(workload, 'status.succeeded')) || 0
  const failed = Number(get(workload, 'status.failed')) || 0
  if (active || succeeded || failed)
    return `${succeeded} 完成 · ${active} 运行 · ${failed} 失败`
  return '暂无运行状态'
}

const workloadAge = (workload: K8sObject) =>
  age(str(workload.metadata?.creationTimestamp, '') || undefined)

function WorkloadCard({
  workload,
  type,
  onOpen,
}: {
  workload: K8sObject
  type: (typeof WORKLOAD_TYPES)[number]
  onOpen: () => void
}) {
  const name = workload.metadata?.name ?? '-'
  return (
    <button
      type='button'
      aria-label={`打开${type.title} ${name}`}
      onClick={onOpen}
      className='group relative min-h-[156px] rounded-xl border border-dashed border-[#6c8fbe] bg-[#f1f5fb] p-4 text-left transition hover:-translate-y-0.5 hover:border-[#285391] hover:bg-[#edf4fd] hover:shadow-[0_10px_22px_rgba(37,76,132,0.12)] focus-visible:ring-2 focus-visible:ring-[#285391] focus-visible:outline-none'
    >
      <div className='flex items-start justify-between gap-3'>
        <div className='min-w-0'>
          <div className='truncate text-base font-semibold text-[#24518f]'>
            {name}
          </div>
          <div className='mt-2 text-sm text-[#7a879b]'>{type.title}</div>
        </div>
        <Boxes className='size-10 shrink-0 stroke-[1.4] text-[#9db1ce] transition group-hover:text-[#5f83b4]' />
      </div>
      <div className='mt-5 flex items-end justify-between gap-2'>
        <div>
          <div className='text-xs text-[#8996a8]'>当前副本</div>
          <div className='mt-1 text-lg font-semibold text-[#263d62] tabular-nums'>
            {replicas(workload)}
          </div>
        </div>
        <div className='text-sm text-[#6d7d94]'>{workloadAge(workload)}</div>
      </div>
      <ExternalLink className='absolute right-3 bottom-3 size-3.5 text-transparent transition group-hover:text-[#7895bb]' />
    </button>
  )
}

export function NamespaceHomePage({
  clusterId,
  namespace,
  onOpenDetail,
  onOpenResource,
}: Props) {
  const queryClient = useQueryClient()
  const queries = useQueries({
    queries: WORKLOAD_TYPES.map((type) => ({
      queryKey: ['k8s', 'namespace-home', clusterId, namespace, type.resource],
      queryFn: () =>
        k8sApi.resources({
          cluster_id: clusterId,
          group: type.group,
          version: type.version,
          resource: type.resource,
          namespace,
        }),
      retry: false,
    })),
  })
  const workloadGroups = WORKLOAD_TYPES.map((type, index) => ({
    type,
    items: queries[index]?.data?.data.items ?? [],
    loading: queries[index]?.isPending ?? true,
    error: queries[index]?.isError ?? false,
  }))
  const workloadItems = workloadGroups.flatMap(({ type, items }) =>
    items.map((workload) => ({ type, workload }))
  )
  const isLoading = workloadGroups.some(({ loading }) => loading)
  const errorTypes = workloadGroups
    .filter(({ error }) => error)
    .map(({ type }) => type.title)
  const isRefreshing = queries.some((query) => query.isFetching)

  const refresh = () => {
    void queryClient.invalidateQueries({
      queryKey: ['k8s', 'namespace-home', clusterId, namespace],
    })
  }

  const openDetail = (
    type: (typeof WORKLOAD_TYPES)[number],
    workload: K8sObject
  ) => {
    const name = workload.metadata?.name
    if (!name) return
    onOpenDetail({
      cluster: clusterId,
      g: type.group,
      v: type.version,
      r: type.resource,
      ns: namespace,
      name,
      from: 'ns-home',
    })
  }

  return (
    <div className='space-y-5'>
      <Card className='rounded-2xl border-[#e0e8f4] bg-white shadow-[0_8px_30px_rgba(37,76,132,0.05)]'>
        <CardHeader className='flex flex-row items-center justify-between gap-4 space-y-0 px-5 py-4 sm:px-6'>
          <div>
            <CardTitle className='text-lg text-[#1c355e]'>默认</CardTitle>
            <p className='mt-1 text-sm text-[#8190a7]'>
              名称空间 {namespace} · 工作负载分层
            </p>
          </div>
          <div className='flex items-center gap-2'>
            <Button
              variant='outline'
              size='sm'
              className='rounded-lg border-[#c8d8ed] text-[#31598e] hover:bg-[#f5f8fd]'
              onClick={() => onOpenResource('deployments')}
            >
              <Boxes className='size-4' />
              常用操作
            </Button>
            <Button
              variant='outline'
              size='sm'
              className='rounded-lg border-[#c8d8ed] text-[#31598e] hover:bg-[#f5f8fd]'
              onClick={refresh}
              disabled={isRefreshing}
            >
              <RefreshCw
                className={isRefreshing ? 'size-4 animate-spin' : 'size-4'}
              />
              刷新
            </Button>
          </div>
        </CardHeader>
        <CardContent className='p-5 sm:p-6'>
          {errorTypes.length > 0 && (
            <div className='mb-4 rounded-lg border border-[#f0c4c8] bg-[#fff5f6] px-4 py-3 text-sm text-[#c43d4d]'>
              部分资源暂时无法读取：{errorTypes.join('、')}
            </div>
          )}
          {isLoading ? (
            <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
              {Array.from({ length: 3 }).map((_, index) => (
                <div
                  key={index}
                  className='h-[156px] animate-pulse rounded-xl bg-[#f2f5fa]'
                />
              ))}
            </div>
          ) : workloadItems.length === 0 ? (
            <EmptyState text={`名称空间 ${namespace} 暂无工作负载`} />
          ) : (
            <div className='grid gap-3 sm:grid-cols-2 xl:grid-cols-3'>
              {workloadItems.map(({ type, workload }) => (
                <WorkloadCard
                  key={`${type.resource}/${workload.metadata?.uid ?? workload.metadata?.name}`}
                  workload={workload}
                  type={type}
                  onOpen={() => openDetail(type, workload)}
                />
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
