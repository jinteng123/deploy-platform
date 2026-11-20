import { useQueries } from '@tanstack/react-query'
import { AlertCircle, CheckCircle2, Clock3, Plus, RefreshCw, Server } from 'lucide-react'
import { k8sApi, type Cluster, type K8sClusterOverview } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { age } from './age'

type Props = {
  clusters: Cluster[]
  loading: boolean
  error?: string
  onOpen: (clusterId: number) => void
  onRefresh: () => void
  onAdd?: () => void
}

const errorText = (err: unknown) =>
  (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail ??
  (err instanceof Error ? err.message : '集群连接失败')

function overviewFor(
  query: { data?: { data: K8sClusterOverview }; isPending: boolean; isError: boolean; error: unknown },
  cluster: Cluster
) {
  if (!cluster.enabled) return { label: '已停用', tone: 'muted' as const }
  if (query.isPending) return { label: '检测中', tone: 'pending' as const }
  if (query.isError) return { label: '连接失败', tone: 'error' as const }

  const overview = query.data?.data
  if (!overview) return { label: '暂无状态', tone: 'pending' as const }
  if (overview.nodes.total > 0 && overview.nodes.ready === overview.nodes.total) {
    return { label: '已就绪', tone: 'ready' as const }
  }
  return { label: '部分就绪', tone: 'pending' as const }
}

const statusClass = {
  ready: 'border-transparent bg-[#11bd92] text-white',
  pending: 'border-[#f1d39a] bg-[#fff8e8] text-[#aa7410]',
  error: 'border-[#f3b8c0] bg-[#fff0f2] text-[#c43d4d]',
  muted: 'border-[#d9dee7] bg-[#eef1f5] text-[#7d8796]',
}

function ClusterStatus({ label, tone }: { label: string; tone: keyof typeof statusClass }) {
  return (
    <span className={`inline-flex items-center gap-1.5 rounded-full border px-4 py-1.5 text-sm font-medium ${statusClass[tone]}`}>
      {tone === 'ready' ? <CheckCircle2 className='size-4' /> : tone === 'error' ? <AlertCircle className='size-4' /> : <Clock3 className='size-4' />}
      {label}
    </span>
  )
}

function ClusterCard({
  cluster,
  query,
  onOpen,
}: {
  cluster: Cluster
  query: { data?: { data: K8sClusterOverview }; isPending: boolean; isError: boolean; error: unknown }
  onOpen: (clusterId: number) => void
}) {
  const overview = query.data?.data
  const status = overviewFor(query, cluster)
  const version = overview?.server_version
  const ageText = age(cluster.created_at)

  return (
    <button
      type='button'
      aria-label={`打开集群 ${cluster.name}`}
      onClick={() => onOpen(cluster.id)}
      className='group relative flex min-h-[258px] flex-col rounded-xl border border-[#d8dee9] bg-[#f8f9fc] p-7 text-left shadow-sm transition hover:-translate-y-0.5 hover:border-[#9cb7dc] hover:shadow-[0_12px_28px_rgba(38,78,135,0.12)] focus-visible:ring-2 focus-visible:ring-[#2c5a9e] focus-visible:outline-none'
    >
      <div className='flex items-start justify-between gap-5'>
        <div className='flex min-w-0 items-center gap-4'>
          <span className='flex size-11 shrink-0 items-center justify-center rounded-full bg-[#285391] text-2xl font-semibold text-white shadow-sm'>
            K
          </span>
          <div className='min-w-0'>
            <div className='truncate text-2xl font-semibold text-[#1e2430]'>{cluster.name}</div>
            <div className='mt-2 text-base text-[#8b8f98]'>大约 {ageText}</div>
          </div>
        </div>
        <Server className='size-16 shrink-0 stroke-[1.35] text-[#287bc6] transition group-hover:text-[#1c5fa7]' />
      </div>

      <div className='my-5 border-t border-dashed border-[#aebed6]' />

      <div className='flex flex-wrap items-center gap-3'>
        <ClusterStatus {...status} />
        <span className='rounded-full border border-[#d2d9e4] bg-white px-4 py-1.5 text-sm text-[#6f737b]'>
          {version ? `K8S-${version}` : 'K8S-未连接'}
        </span>
      </div>

      <div className='mt-auto flex flex-wrap items-end justify-between gap-2 pt-5 text-sm text-[#777d88]'>
        <span>{cluster.env_code}</span>
        {overview ? (
          <span>
            {overview.nodes.ready}/{overview.nodes.total} 节点 · {overview.namespaces} 命名空间 · {overview.workloads.pods} Pods
          </span>
        ) : query.isError ? (
          <span title={errorText(query.error)}>无法读取集群概览</span>
        ) : null}
      </div>
    </button>
  )
}

export function ClusterListPage({ clusters, loading, error, onOpen, onRefresh, onAdd }: Props) {
  const overviewQueries = useQueries({
    queries: clusters.map((cluster) => ({
      queryKey: ['k8s', 'overview', cluster.id],
      queryFn: () => k8sApi.overview(cluster.id),
      enabled: cluster.enabled,
      retry: false,
    })),
  })

  return (
    <main className='px-4 py-8 sm:px-6 lg:px-8'>
      <div className='mx-auto w-full max-w-[1500px]'>
        <div className='mb-7 flex flex-wrap items-end justify-between gap-4'>
          <div>
            <h1 className='text-3xl font-bold tracking-tight text-[#1c355e]'>Kubernetes 集群列表</h1>
            <p className='mt-2 text-base text-[#7185a3]'>查看已接入集群及其当前运行状态。</p>
          </div>
          <Button
            variant='outline'
            size='sm'
            className='rounded-lg border-[#d2deef] bg-white text-[#31598e]'
            onClick={onRefresh}
            disabled={loading}
          >
            <RefreshCw className={loading ? 'size-4 animate-spin' : 'size-4'} />
            刷新列表
          </Button>
        </div>

        <Card className='rounded-2xl border-[#e2e8f2] bg-white shadow-[0_8px_30px_rgba(37,76,132,0.05)]'>
          <CardContent className='p-7 sm:p-9'>
            <div className='mb-6 flex items-center justify-between gap-3'>
              <div className='text-lg font-semibold text-[#2c456d]'>共 {clusters.length} 个集群</div>
              {!loading && !error && clusters.length > 0 && (
                <div className='text-sm text-[#8795aa]'>点击集群卡片进入控制台</div>
              )}
            </div>

            {error ? (
              <div className='rounded-xl border border-[#f0c4c8] bg-[#fff5f6] px-5 py-8 text-center text-sm text-[#c43d4d]'>
                {error}
              </div>
            ) : loading ? (
              <div className='grid gap-5 md:grid-cols-2 xl:grid-cols-3'>
                {Array.from({ length: 2 }).map((_, i) => (
                  <div key={i} className='min-h-[258px] animate-pulse rounded-xl border border-[#e3e8f1] bg-[#f5f7fb]' />
                ))}
              </div>
            ) : (
              <div className='grid gap-5 md:grid-cols-2 xl:grid-cols-3'>
                {clusters.map((cluster, index) => (
                  <ClusterCard
                    key={cluster.id}
                    cluster={cluster}
                    query={overviewQueries[index]}
                    onOpen={onOpen}
                  />
                ))}
                <button
                  type='button'
                  onClick={onAdd}
                  className='group relative flex min-h-[258px] items-center justify-center rounded-xl border-2 border-dashed border-[#aeb2b9] bg-[#fbfcfe] text-[#b6b7bb] transition hover:border-[#6f91bf] hover:bg-[#f5f8fd] hover:text-[#6f91bf] focus-visible:ring-2 focus-visible:ring-[#2c5a9e] focus-visible:outline-none'
                >
                  <Plus className='absolute left-12 top-14 size-16 stroke-[1.2]' />
                  <span className='text-2xl'>添加集群</span>
                  <Server className='absolute right-10 top-8 size-16 stroke-[1.35] text-[#287bc6]' />
                </button>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </main>
  )
}
