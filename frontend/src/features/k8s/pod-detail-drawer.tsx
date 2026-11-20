import { lazy, Suspense, useMemo } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ExternalLink, RefreshCw } from 'lucide-react'
import { k8sApi, type K8sObject, type K8sResourceType } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { EmptyState } from '@/components/empty-state'
import { age } from './age'
import { get, list, str } from './catalog'
import { errDetail } from './errors'
import type { K8sDetailQuery } from './k8s-page'
import { podContainers } from './pod-containers'
import type { PodConsoleQuery } from './pod-terminal-page'
import { ResourceActions } from './resource-actions'
import { StatusText } from './status-text'
import { useCapabilities } from './use-capabilities'

// Monaco/xterm stay out of the drawer's initial paint — the YAML and
// console tabs mount their bundles only when the drawer actually opens.
const YamlCard = lazy(() =>
  import('./yaml-editor').then((m) => ({ default: m.YamlCard }))
)
const LogPanel = lazy(() =>
  import('./pod-console').then((m) => ({ default: m.LogPanel }))
)

const detail = errDetail

function Row({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <tr className='border-b border-[#edf1f7] last:border-0'>
      <td className='w-28 bg-[#f8fafd] px-3 py-2 text-xs font-medium text-[#587092]'>
        {label}
      </td>
      <td className='px-3 py-2 text-sm break-all text-[#2a4165]'>{children}</td>
    </tr>
  )
}

function Block({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <section className='rounded-xl border border-[#e0e8f4] bg-white'>
      <h3 className='border-b border-[#edf1f7] px-4 py-2.5 text-sm font-semibold text-[#1c355e]'>
        {title}
      </h3>
      <div className='p-4'>{children}</div>
    </section>
  )
}

// ownerRefTarget maps an ownerReference back onto the allowlisted type
// registry — only kinds the console actually serves get a link (a
// ReplicaSet owner, for example, renders as plain text since the
// registry has no replicasets resource).
function ownerRefTarget(
  ref: Record<string, unknown>,
  typeMap: Map<string, K8sResourceType>
): K8sResourceType | undefined {
  const apiVersion = str(ref.apiVersion, '')
  const slash = apiVersion.indexOf('/')
  const group = slash === -1 ? '' : apiVersion.slice(0, slash)
  const version = slash === -1 ? apiVersion : apiVersion.slice(slash + 1)
  for (const t of typeMap.values()) {
    if (t.kind === ref.kind && t.group === group && t.version === version) {
      return t
    }
  }
  return undefined
}

// containerState extracts the human state of one containerStatus —
// waiting/terminated reasons take precedence over running, matching
// kubectl's STATUS column semantics.
function containerState(cs: Record<string, unknown> | undefined): string {
  const state = cs?.state as Record<string, Record<string, unknown>> | undefined
  if (state?.waiting?.reason) return str(state.waiting.reason)
  if (state?.terminated?.reason) return str(state.terminated.reason)
  if (state?.running) return 'Running'
  return '-'
}

function containerStarted(cs: Record<string, unknown> | undefined): string {
  const state = cs?.state as Record<string, Record<string, unknown>> | undefined
  const started =
    state?.running?.startedAt ?? state?.terminated?.startedAt ?? undefined
  return started ? `${str(started)}` : '-'
}

function volumeKind(v: Record<string, unknown>): string {
  const known = [
    'configMap',
    'secret',
    'persistentVolumeClaim',
    'emptyDir',
    'hostPath',
    'projected',
    'downwardAPI',
    'nfs',
    'csi',
    'ephemeral',
  ]
  for (const k of known) if (v[k] != null) return k
  return Object.keys(v).find((k) => k !== 'name') ?? '-'
}

function eventTime(e: K8sObject): string {
  return str(
    get(e, 'lastTimestamp') ??
      get(e, 'eventTime') ??
      get(e, 'metadata.creationTimestamp')
  )
}

function PodOverview({
  obj,
  query,
  typeMap,
  onOpenOwner,
  onDeleted,
}: {
  obj: K8sObject
  query: K8sDetailQuery
  typeMap: Map<string, K8sResourceType>
  onOpenOwner: (q: K8sDetailQuery) => void
  onDeleted: () => void
}) {
  const meta = obj.metadata ?? {}
  const phase = str(get(obj, 'status.phase'))
  const podIP = str(get(obj, 'status.podIP'))
  const hostIP = str(get(obj, 'status.hostIP'))
  const node = str(get(obj, 'spec.nodeName'))
  const qos = str(get(obj, 'status.qosClass'))
  const owners = list(meta.ownerReferences)
  const conditions = list(get(obj, 'status.conditions'))
  const containers = list(get(obj, 'spec.containers'))
  const initContainers = list(get(obj, 'spec.initContainers'))
  const statuses = list(get(obj, 'status.containerStatuses'))
  const initStatuses = list(get(obj, 'status.initContainerStatuses'))
  const statusOf = (n: unknown, init: boolean) =>
    (init ? initStatuses : statuses).find((s) => s.name === n)
  const volumes = list(get(obj, 'spec.volumes'))

  return (
    <div className='space-y-4'>
      <Block title='基本信息'>
        <table className='w-full text-sm'>
          <tbody>
            <Row label='名称'>{str(meta.name)}</Row>
            <Row label='命名空间'>{str(meta.namespace)}</Row>
            <Row label='UID'>{str(meta.uid)}</Row>
            <Row label='创建时间'>
              {str(meta.creationTimestamp)}（
              {age(str(meta.creationTimestamp, '') || undefined)}）
            </Row>
            <Row label='状态'>
              <StatusText value={phase} />
            </Row>
            <Row label='节点'>{node}</Row>
            <Row label='Pod IP'>{podIP}</Row>
            <Row label='Host IP'>{hostIP}</Row>
            {qos !== '-' && <Row label='QoS'>{qos}</Row>}
          </tbody>
        </table>
      </Block>

      <Block title={`容器（${containers.length + initContainers.length}）`}>
        <table className='w-full text-sm'>
          <thead>
            <tr className='border-b bg-[#f7f9fc] text-left'>
              <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                名称
              </th>
              <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                镜像
              </th>
              <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                状态
              </th>
              <th className='px-2 py-1.5 text-right text-xs font-semibold text-[#587092]'>
                重启
              </th>
              <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                启动时间
              </th>
            </tr>
          </thead>
          <tbody>
            {initContainers.map((c) => {
              const cs = statusOf(c.name, true)
              return (
                <tr
                  key={`init-${str(c.name)}`}
                  className='border-b border-[#edf1f7] last:border-0'
                >
                  <td className='px-2 py-2 whitespace-nowrap'>
                    {str(c.name)}
                    <Badge variant='secondary' className='ms-2'>
                      init
                    </Badge>
                  </td>
                  <td
                    className='max-w-56 truncate px-2 py-2'
                    title={str(c.image)}
                  >
                    {str(c.image)}
                  </td>
                  <td className='px-2 py-2'>
                    <StatusText value={containerState(cs)} />
                  </td>
                  <td className='px-2 py-2 text-right tabular-nums'>
                    {str(cs?.restartCount, '0')}
                  </td>
                  <td className='px-2 py-2 whitespace-nowrap'>
                    {containerStarted(cs)}
                  </td>
                </tr>
              )
            })}
            {containers.map((c) => {
              const cs = statusOf(c.name, false)
              return (
                <tr
                  key={str(c.name)}
                  className='border-b border-[#edf1f7] last:border-0'
                >
                  <td className='px-2 py-2 whitespace-nowrap'>{str(c.name)}</td>
                  <td
                    className='max-w-56 truncate px-2 py-2'
                    title={str(c.image)}
                  >
                    {str(c.image)}
                  </td>
                  <td className='px-2 py-2'>
                    <StatusText value={containerState(cs)} />
                  </td>
                  <td className='px-2 py-2 text-right tabular-nums'>
                    {str(cs?.restartCount, '0')}
                  </td>
                  <td className='px-2 py-2 whitespace-nowrap'>
                    {containerStarted(cs)}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </Block>

      {conditions.length > 0 && (
        <Block title='状态条件'>
          <table className='w-full text-sm'>
            <thead>
              <tr className='border-b bg-[#f7f9fc] text-left'>
                <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                  类型
                </th>
                <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                  状态
                </th>
                <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                  原因
                </th>
                <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                  时间
                </th>
              </tr>
            </thead>
            <tbody>
              {conditions.map((c) => (
                <tr
                  key={str(c.type)}
                  className='border-b border-[#edf1f7] last:border-0'
                >
                  <td className='px-2 py-2 whitespace-nowrap'>{str(c.type)}</td>
                  <td className='px-2 py-2'>
                    <StatusText
                      value={str(c.status) === 'True' ? 'Ready' : 'NotReady'}
                    />
                    <span className='ms-1.5 text-xs text-muted-foreground'>
                      {str(c.status)}
                    </span>
                  </td>
                  <td className='px-2 py-2'>{str(c.reason)}</td>
                  <td className='px-2 py-2 whitespace-nowrap'>
                    {str(c.lastTransitionTime)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </Block>
      )}

      {owners.length > 0 && (
        <Block title='所属工作负载'>
          <ul className='space-y-1.5'>
            {owners.map((o, i) => {
              const target = ownerRefTarget(o, typeMap)
              return (
                <li key={i} className='flex items-center gap-2 text-sm'>
                  <Badge variant='outline'>{str(o.kind)}</Badge>
                  {target ? (
                    <button
                      type='button'
                      className='inline-flex items-center gap-1 font-medium text-[#24549a] hover:underline'
                      onClick={() =>
                        onOpenOwner({
                          cluster: query.cluster,
                          g: target.group,
                          v: target.version,
                          r: target.resource,
                          ns: query.ns,
                          name: str(o.name),
                        })
                      }
                    >
                      {str(o.name)}
                      <ExternalLink className='size-3' />
                    </button>
                  ) : (
                    <span>{str(o.name)}</span>
                  )}
                </li>
              )
            })}
          </ul>
        </Block>
      )}

      {volumes.length > 0 && (
        <Block title={`存储卷（${volumes.length}）`}>
          <ul className='flex flex-wrap gap-2'>
            {volumes.map((v, i) => (
              <li key={i}>
                <Badge variant='secondary'>
                  {str(v.name)}
                  <span className='ms-1.5 text-muted-foreground'>
                    {volumeKind(v)}
                  </span>
                </Badge>
              </li>
            ))}
          </ul>
        </Block>
      )}

      <ResourceActions
        params={{
          cluster_id: query.cluster,
          group: query.g,
          version: query.v,
          resource: query.r,
          namespace: query.ns,
        }}
        name={query.name}
        obj={obj}
        onDeleted={onDeleted}
      />
    </div>
  )
}

// PodDetailDrawer opens a pod's full detail as a right-side sheet so the
// list context underneath is preserved (Kuboard parity: 名称空间: ns ·
// 容器组: pod). The standalone /k8s/detail route still exists for deep
// links and non-pod resources.
export function PodDetailDrawer({
  query,
  typeMap,
  onClose,
  onOpenOwner,
  onOpenTerminal,
}: {
  query: K8sDetailQuery | null
  typeMap: Map<string, K8sResourceType>
  onClose: () => void
  onOpenOwner: (q: K8sDetailQuery) => void
  onOpenTerminal: (q: PodConsoleQuery) => void
}) {
  const queryClient = useQueryClient()
  const open = query != null
  const pod = useQuery({
    queryKey: [
      'k8s',
      'resource',
      query?.cluster,
      query?.g ?? '',
      query?.v,
      'pods',
      query?.ns ?? '',
      query?.name,
    ],
    queryFn: () =>
      k8sApi.resource(query?.name as string, {
        cluster_id: query?.cluster as number,
        group: query?.g,
        version: query?.v as string,
        resource: 'pods',
        namespace: query?.ns || undefined,
      }),
    enabled: open,
  })
  // Related events ride the generic resources endpoint with a server-side
  // field selector — no dedicated endpoint needed.
  const events = useQuery({
    queryKey: [
      'k8s',
      'pod-events',
      query?.cluster,
      query?.ns ?? '',
      query?.name,
    ],
    queryFn: () =>
      k8sApi.resources({
        cluster_id: query?.cluster as number,
        version: 'v1',
        resource: 'events',
        namespace: query?.ns || undefined,
        field_selector: `involvedObject.name=${query?.name}`,
      }),
    enabled: open,
  })

  const capsQuery = useCapabilities(query?.cluster, query?.ns || undefined)
  const obj = pod.data?.data
  const eventItems = useMemo(
    () =>
      [...(events.data?.data.items ?? [])].sort((a, b) =>
        eventTime(b).localeCompare(eventTime(a))
      ),
    [events.data]
  )
  const refresh = () => {
    void pod.refetch()
    void events.refetch()
    void queryClient.invalidateQueries({ queryKey: ['k8s', 'resources'] })
  }

  return (
    <Sheet open={open} onOpenChange={(v) => !v && onClose()}>
      <SheetContent
        side='right'
        className='w-full gap-0 overflow-y-auto sm:max-w-2xl'
      >
        <SheetHeader className='border-b border-[#edf1f7] px-5 py-4'>
          <div className='flex items-center justify-between gap-3'>
            <SheetTitle className='truncate text-base text-[#1c355e]'>
              名称空间: {query?.ns ?? 'default'} · 容器组: {query?.name ?? ''}
            </SheetTitle>
            <SheetDescription className='sr-only'>
              查看该 Pod 的状态、事件、日志、终端和 YAML 详情。
            </SheetDescription>
            <Button
              variant='outline'
              size='sm'
              className='shrink-0 rounded-lg border-[#d6e1f0]'
              onClick={refresh}
              disabled={pod.isFetching}
            >
              <RefreshCw
                className={pod.isFetching ? 'size-4 animate-spin' : 'size-4'}
              />
              刷新
            </Button>
          </div>
        </SheetHeader>

        <div className='px-5 py-4'>
          {!open ? null : pod.isError ? (
            <p className='py-8 text-center text-sm text-destructive'>
              {detail(pod.error)}
            </p>
          ) : pod.isPending || obj == null ? (
            <div className='space-y-3 py-2'>
              {Array.from({ length: 6 }).map((_, i) => (
                <Skeleton key={i} className='h-10 w-full' />
              ))}
            </div>
          ) : (
            <Tabs defaultValue='overview'>
              <TabsList className='mb-4 rounded-xl bg-[#e8eff9] p-1'>
                <TabsTrigger value='overview'>概览</TabsTrigger>
                <TabsTrigger value='events'>事件</TabsTrigger>
                <TabsTrigger value='console'>日志 / 终端</TabsTrigger>
                <TabsTrigger value='yaml'>YAML</TabsTrigger>
              </TabsList>
              <TabsContent value='overview'>
                <PodOverview
                  obj={obj}
                  query={query}
                  typeMap={typeMap}
                  onOpenOwner={onOpenOwner}
                  onDeleted={onClose}
                />
              </TabsContent>
              <TabsContent value='events'>
                {events.isPending ? (
                  <div className='space-y-2 py-2'>
                    {Array.from({ length: 3 }).map((_, i) => (
                      <Skeleton key={i} className='h-9 w-full' />
                    ))}
                  </div>
                ) : eventItems.length === 0 ? (
                  <EmptyState text='暂无关联事件' />
                ) : (
                  <table className='w-full text-sm'>
                    <thead>
                      <tr className='border-b bg-[#f7f9fc] text-left'>
                        <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                          时间
                        </th>
                        <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                          类型
                        </th>
                        <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                          原因
                        </th>
                        <th className='px-2 py-1.5 text-xs font-semibold text-[#587092]'>
                          消息
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {eventItems.map((e, i) => (
                        <tr
                          key={e.metadata?.uid ? str(e.metadata.uid) : i}
                          className='border-b border-[#edf1f7] last:border-0'
                        >
                          <td className='px-2 py-2 text-xs whitespace-nowrap text-muted-foreground'>
                            {eventTime(e)}
                          </td>
                          <td className='px-2 py-2'>
                            <StatusText value={str(get(e, 'type'))} />
                          </td>
                          <td className='px-2 py-2 whitespace-nowrap'>
                            {str(get(e, 'reason'))}
                          </td>
                          <td className='px-2 py-2 text-xs break-all'>
                            {str(get(e, 'message'))}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                )}
              </TabsContent>
              <TabsContent value='console'>
                <Suspense fallback={<Skeleton className='h-40 w-full' />}>
                  {capsQuery.data?.logs !== true &&
                  capsQuery.data?.exec !== true ? (
                    <EmptyState text='当前账号没有该名称空间的日志和终端权限' />
                  ) : (
                    <div className='space-y-3'>
                      {capsQuery.data?.exec === true && (
                        <div className='flex justify-end'>
                          <Button
                            variant='outline'
                            size='sm'
                            className='rounded-lg border-[#d6e1f0]'
                            onClick={() =>
                              onOpenTerminal({
                                cluster: query.cluster,
                                ns: query.ns,
                                pod: query.name,
                              })
                            }
                          >
                            <ExternalLink className='size-4' /> 打开终端
                          </Button>
                        </div>
                      )}
                      {capsQuery.data?.logs === true && (
                        <LogPanel
                          params={{
                            cluster_id: query.cluster,
                            version: 'v1',
                            resource: 'pods',
                            namespace: query.ns,
                          }}
                          pod={query.name}
                          containers={podContainers(obj)}
                          autoConnect
                          heightClass='h-72'
                        />
                      )}
                    </div>
                  )}
                </Suspense>
              </TabsContent>
              <TabsContent value='yaml'>
                <Suspense fallback={<Skeleton className='h-60 w-full' />}>
                  <YamlCard
                    params={{
                      cluster_id: query.cluster,
                      group: query.g,
                      version: query.v,
                      resource: 'pods',
                      namespace: query.ns,
                    }}
                    name={query.name}
                    obj={obj}
                  />
                </Suspense>
              </TabsContent>
            </Tabs>
          )}
        </div>
      </SheetContent>
    </Sheet>
  )
}
