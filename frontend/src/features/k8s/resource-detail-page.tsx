import { lazy, Suspense, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowLeft, RefreshCw } from 'lucide-react'
import { k8sApi } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ThemeSwitch } from '@/components/theme-switch'
import { age } from './age'
import { resourceInfo } from './catalog'
import { CmEditDialog } from './cm-edit-dialog'
import { errDetail } from './errors'
import type { K8sDetailQuery } from './k8s-page'
import type { PodConsoleQuery } from './pod-terminal-page'
import { ResourceActions } from './resource-actions'
import { SecretEditDialog } from './secret-edit-dialog'
import { WorkloadDetailPage } from './workload-detail-page'

// Monaco is a multi-MB dependency — the YAML card mounts lazily so the
// detail page never blocks on the editor bundle. xterm.js is likewise
// lazy: only pod details mount the console card.
const YamlCard = lazy(() =>
  import('./yaml-editor').then((m) => ({ default: m.YamlCard }))
)
const PodConsoleCard = lazy(() =>
  import('./pod-console').then((m) => ({ default: m.PodConsoleCard }))
)

// DetailSearch mirrors K8sDetailQuery with every field optional — the URL
// may be hand-edited, so the page validates rather than trusting it.
export type DetailSearch = Partial<K8sDetailQuery>

const detail = errDetail
const WORKLOAD_RESOURCES = new Set([
  'deployments',
  'statefulsets',
  'daemonsets',
  'jobs',
  'cronjobs',
])

function Row({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <tr className='border-b border-[#edf1f7] last:border-0'>
      <td className='w-32 bg-[#f8fafd] px-3 py-2 text-xs font-medium text-[#587092]'>
        {label}
      </td>
      <td className='px-3 py-2 text-sm break-all text-[#2a4165]'>{children}</td>
    </tr>
  )
}

function JsonCard({ title, value }: { title: string; value: unknown }) {
  if (value == null) return null
  return (
    <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.05)]'>
      <CardHeader>
        <CardTitle className='text-base text-[#1c355e]'>{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <pre className='max-h-96 overflow-auto rounded-xl border border-[#e1e9f5] bg-[#f7f9fc] p-3 text-xs'>
          {JSON.stringify(value, null, 2)}
        </pre>
      </CardContent>
    </Card>
  )
}

function LazyCardFallback({
  title,
  bodyHeight,
}: {
  title: string
  bodyHeight: 'h-64' | 'h-[420px]'
}) {
  return (
    <Card
      aria-busy='true'
      className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.05)]'
    >
      <CardHeader>
        <CardTitle className='text-base text-[#1c355e]'>{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <div className='space-y-3' role='status' aria-label={`${title}加载中`}>
          <Skeleton className='h-7 w-36 rounded-lg' />
          <Skeleton className={`${bodyHeight} w-full rounded-lg`} />
        </div>
      </CardContent>
    </Card>
  )
}

export function ResourceDetailPage({
  search,
  onBack,
  onEdit,
  onOpenTerminal,
}: {
  search: DetailSearch
  onBack: () => void
  onEdit?: () => void
  onOpenTerminal: (q: PodConsoleQuery) => void
}) {
  const { cluster, g, v, r, ns, name } = search
  const ready =
    cluster != null && v != null && r != null && name != null && name !== ''
  const info = resourceInfo(r)
  const workload = WORKLOAD_RESOURCES.has(r ?? '')
  const queryClient = useQueryClient()
  // The structured data editor exists for the two kv-map kinds —
  // ResourceActions still gates the button itself on data_editable +
  // update, this only decides which dialog an open request mounts.
  const dataEditable = r === 'configmaps' || r === 'secrets'
  const [editDataOpen, setEditDataOpen] = useState(false)

  const query = useQuery({
    queryKey: ['k8s', 'resource', cluster, g ?? '', v, r, ns ?? '', name],
    queryFn: () =>
      k8sApi.resource(name as string, {
        cluster_id: cluster as number,
        group: g,
        version: v as string,
        resource: r as string,
        namespace: ns || undefined,
      }),
    enabled: ready,
  })
  const obj = query.data?.data
  const meta = obj?.metadata ?? {}
  const labels = Object.entries(meta.labels ?? {})
  const annotations = Object.entries(meta.annotations ?? {}).filter(
    ([k]) => !k.startsWith('kubectl.kubernetes.io/')
  )

  return (
    <>
      <Header fixed>
        <div className='me-auto flex min-w-0 items-center gap-2 text-sm'>
          <Button
            variant='ghost'
            size='sm'
            className='shrink-0 text-[#24549a]'
            onClick={onBack}
          >
            <ArrowLeft className='size-4' /> 返回资源列表
          </Button>
          <span className='text-[#b0bfd4]'>/</span>
          {workload && ns != null && (
            <>
              <span className='truncate font-semibold text-[#214b8d]'>
                {ns}
              </span>
              <span className='text-[#b0bfd4]'>/</span>
            </>
          )}
          <span className='truncate font-semibold text-[#214b8d]'>
            {info.title} / {name ?? '-'}
          </span>
        </div>
        <ThemeSwitch />
      </Header>
      <Main
        fluid={workload}
        className={workload ? 'p-3 sm:p-4 lg:p-5' : undefined}
      >
        {!ready ? (
          <p className='py-8 text-center text-muted-foreground'>
            缺少必要参数，无法加载资源详情
          </p>
        ) : query.isError ? (
          <p className='py-8 text-center text-sm text-destructive'>
            {detail(query.error)}
          </p>
        ) : query.isPending ? (
          <p className='py-8 text-center text-sm text-muted-foreground'>
            加载中…
          </p>
        ) : workload &&
          obj != null &&
          name != null &&
          cluster != null &&
          v != null &&
          r != null ? (
          <WorkloadDetailPage
            params={{
              cluster_id: cluster,
              group: g,
              version: v,
              resource: r,
              namespace: ns || undefined,
            }}
            name={name}
            obj={obj}
            fetching={query.isFetching}
            onRefresh={() =>
              void queryClient.invalidateQueries({
                queryKey: [
                  'k8s',
                  'resource',
                  cluster,
                  g ?? '',
                  v,
                  r,
                  ns ?? '',
                  name,
                ],
              })
            }
            onDeleted={onBack}
            onEdit={onEdit}
            onOpenTerminal={onOpenTerminal}
          />
        ) : (
          <div className='space-y-5'>
            <div className='flex flex-wrap items-end justify-between gap-4'>
              <div className='min-w-0'>
                <p className='text-xs font-semibold tracking-wide text-[#6f88ac]'>
                  资源详情
                </p>
                <h1 className='mt-1 truncate text-2xl font-bold tracking-tight text-[#1c355e]'>
                  {name}
                </h1>
                <div className='mt-2 flex flex-wrap gap-2 text-xs text-muted-foreground'>
                  <Badge variant='outline'>{obj?.kind ?? info.title}</Badge>
                  {meta.namespace != null && (
                    <Badge variant='outline'>命名空间：{meta.namespace}</Badge>
                  )}
                  <Badge variant='outline'>
                    API：{obj?.apiVersion ?? `${g || 'core'}/${v}`}
                  </Badge>
                </div>
              </div>
              <Button
                variant='outline'
                className='rounded-lg border-[#d6e1f0] bg-white'
                onClick={() =>
                  void queryClient.invalidateQueries({
                    queryKey: [
                      'k8s',
                      'resource',
                      cluster,
                      g ?? '',
                      v,
                      r,
                      ns ?? '',
                      name,
                    ],
                  })
                }
                disabled={query.isFetching}
              >
                <RefreshCw
                  className={
                    query.isFetching ? 'size-4 animate-spin' : 'size-4'
                  }
                />
                刷新详情
              </Button>
            </div>
            <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.05)]'>
              <CardHeader>
                <CardTitle className='text-base text-[#1c355e]'>
                  基本信息
                </CardTitle>
              </CardHeader>
              <CardContent>
                <table className='w-full text-sm'>
                  <tbody>
                    <Row label='名称'>{meta.name}</Row>
                    {meta.namespace != null && (
                      <Row label='命名空间'>{meta.namespace}</Row>
                    )}
                    <Row label='类型'>{obj?.kind ?? info.title}</Row>
                    <Row label='API 版本'>{obj?.apiVersion}</Row>
                    <Row label='UID'>{meta.uid}</Row>
                    <Row label='创建时间'>
                      {meta.creationTimestamp}（{age(meta.creationTimestamp)}）
                    </Row>
                    <Row label='标签'>
                      {labels.length === 0 ? (
                        '-'
                      ) : (
                        <span className='flex flex-wrap gap-1'>
                          {labels.map(([k, val]) => (
                            <Badge
                              key={k}
                              variant='secondary'
                            >{`${k}=${val}`}</Badge>
                          ))}
                        </span>
                      )}
                    </Row>
                    <Row label='注解'>
                      {annotations.length === 0 ? (
                        '-'
                      ) : (
                        <span className='flex flex-wrap gap-1'>
                          {annotations.map(([k, val]) => (
                            <Badge
                              key={k}
                              variant='outline'
                              title={val}
                            >{`${k}`}</Badge>
                          ))}
                        </span>
                      )}
                    </Row>
                  </tbody>
                </table>
              </CardContent>
            </Card>
            {obj != null &&
              name != null &&
              cluster != null &&
              v != null &&
              r != null && (
                <ResourceActions
                  params={{
                    cluster_id: cluster,
                    group: g,
                    version: v,
                    resource: r,
                    namespace: ns || undefined,
                  }}
                  name={name}
                  obj={obj}
                  onDeleted={onBack}
                  onEditData={
                    dataEditable ? () => setEditDataOpen(true) : undefined
                  }
                />
              )}
            <JsonCard title='Spec' value={obj?.spec} />
            <JsonCard title='Status' value={obj?.status} />
            {r === 'pods' &&
              obj != null &&
              name != null &&
              cluster != null &&
              v != null && (
                <Suspense
                  fallback={
                    <LazyCardFallback title='日志与终端' bodyHeight='h-64' />
                  }
                >
                  <PodConsoleCard
                    params={{
                      cluster_id: cluster,
                      group: g,
                      version: v,
                      resource: r,
                      namespace: ns || undefined,
                    }}
                    name={name}
                    obj={obj}
                  />
                </Suspense>
              )}
            {obj != null &&
              name != null &&
              cluster != null &&
              v != null &&
              r != null && (
                <Suspense
                  fallback={
                    <LazyCardFallback
                      title='YAML 预览'
                      bodyHeight='h-[420px]'
                    />
                  }
                >
                  <YamlCard
                    params={{
                      cluster_id: cluster,
                      group: g,
                      version: v,
                      resource: r,
                      namespace: ns || undefined,
                    }}
                    name={name}
                    obj={obj}
                  />
                </Suspense>
              )}
            <JsonCard title='完整 JSON' value={obj} />
          </div>
        )}
      </Main>
      {editDataOpen &&
        obj != null &&
        name != null &&
        cluster != null &&
        v != null &&
        r === 'configmaps' && (
          <CmEditDialog
            open={editDataOpen}
            onOpenChange={setEditDataOpen}
            params={{
              cluster_id: cluster,
              group: g,
              version: v,
              resource: r,
              namespace: ns || undefined,
            }}
            name={name}
            obj={obj}
            onSaved={() =>
              void queryClient.invalidateQueries({ queryKey: ['k8s'] })
            }
          />
        )}
      {editDataOpen &&
        obj != null &&
        name != null &&
        cluster != null &&
        v != null &&
        r === 'secrets' && (
          <SecretEditDialog
            open={editDataOpen}
            onOpenChange={setEditDataOpen}
            params={{
              cluster_id: cluster,
              group: g,
              version: v,
              resource: r,
              namespace: ns || undefined,
            }}
            name={name}
            obj={obj}
            onSaved={() =>
              void queryClient.invalidateQueries({ queryKey: ['k8s'] })
            }
          />
        )}
    </>
  )
}
