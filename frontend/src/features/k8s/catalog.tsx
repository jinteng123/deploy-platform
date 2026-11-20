import type { ReactNode } from 'react'
import type { K8sObject, K8sResourceType } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { age } from './age'
import { StatusText } from './status-text'

// Column renders one table cell from an unstructured object. `numeric`
// right-aligns the cell and enables tabular-nums for count columns.
export type K8sColumn = {
  key: string
  title: string
  numeric?: boolean
  cell: (o: K8sObject) => ReactNode
}

export type K8sResourceInfo = {
  resource: string
  title: string
  columns: K8sColumn[]
}

export type K8sSectionId =
  | 'overview'
  | 'workloads'
  | 'pods'
  | 'network'
  | 'config'
  | 'storage'

export type K8sSection = {
  id: K8sSectionId
  title: string
  resources: string[]
}

// Console information architecture: 集群 → 命名空间 → 六大板块.
// Cluster-scoped kinds (namespaces/nodes/PV/storageclasses) live in 概览
// and 存储; the namespace selector is disabled for them via the
// resource-types API's `namespaced` flag rather than anything hardcoded.
export const K8S_SECTIONS: K8sSection[] = [
  {
    id: 'overview',
    title: '概览',
    resources: ['namespaces', 'nodes', 'events'],
  },
  {
    id: 'workloads',
    title: '工作负载',
    resources: [
      'deployments',
      'statefulsets',
      'daemonsets',
      'jobs',
      'cronjobs',
    ],
  },
  { id: 'pods', title: '容器组', resources: ['pods'] },
  { id: 'network', title: '服务', resources: ['services', 'ingresses'] },
  { id: 'config', title: '配置', resources: ['configmaps', 'secrets'] },
  {
    id: 'storage',
    title: '存储',
    resources: [
      'persistentvolumeclaims',
      'persistentvolumes',
      'storageclasses',
    ],
  },
]

export const get = (o: K8sObject, path: string): unknown =>
  path
    .split('.')
    .reduce<unknown>(
      (acc, k) => (acc == null ? acc : (acc as Record<string, unknown>)[k]),
      o
    )

export const str = (v: unknown, fallback = '-'): string => {
  if (v == null || v === '') return fallback
  if (typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean')
    return String(v)
  return fallback
}

export const list = (v: unknown): Record<string, unknown>[] =>
  Array.isArray(v) ? (v as Record<string, unknown>[]) : []

const meta = (o: K8sObject) => o.metadata ?? {}
const name = (o: K8sObject) => str(meta(o).name)
const namespace = (o: K8sObject) => str(meta(o).namespace)
const created = (o: K8sObject) =>
  age(str(meta(o).creationTimestamp, '') || undefined)

// pod restarts mirrors kubectl RESTARTS: all containerStatuses plus
// restartable (restartPolicy: Always) init sidecars.
const podRestarts = (o: K8sObject): string => {
  let total = 0
  for (const c of list(get(o, 'status.containerStatuses')))
    total += Number(c.restartCount) || 0
  for (const c of list(get(o, 'status.initContainerStatuses'))) {
    if (c.restartPolicy === 'Always') total += Number(c.restartCount) || 0
  }
  return String(total)
}

const podReady = (o: K8sObject): string => {
  const statuses = list(get(o, 'status.containerStatuses'))
  const ready = statuses.filter((c) => c.ready === true).length
  return `${ready}/${statuses.length}`
}

// kubectl STATUS column: surface waiting/terminated reasons before phase.
const podStatus = (o: K8sObject): string => {
  for (const c of list(get(o, 'status.containerStatuses'))) {
    const state = c.state as Record<string, Record<string, unknown>> | undefined
    if (state?.waiting?.reason) return str(state.waiting.reason)
    if (state?.terminated?.reason) return str(state.terminated.reason)
  }
  return str(get(o, 'status.phase'))
}

const workloadReady = (o: K8sObject): string =>
  `${Number(get(o, 'status.readyReplicas')) || 0}/${Number(get(o, 'spec.replicas')) || 0}`

const svcPorts = (o: K8sObject): string => {
  const ports = list(get(o, 'spec.ports'))
  if (ports.length === 0) return '-'
  return ports
    .map((p) => {
      const base = `${str(p.port)}${p.targetPort != null && p.targetPort !== p.port ? `→${str(p.targetPort)}` : ''}`
      return `${base}/${str(p.protocol, 'TCP')}`
    })
    .join(',')
}

const jobStatus = (o: K8sObject): string => {
  for (const c of list(get(o, 'status.conditions'))) {
    if (c.status === 'True') return str(c.type)
  }
  return Number(get(o, 'status.active')) > 0 ? 'Running' : '-'
}

const nodeReady = (o: K8sObject): string => {
  for (const c of list(get(o, 'status.conditions'))) {
    if (c.type === 'Ready') return c.status === 'True' ? 'Ready' : 'NotReady'
  }
  return '-'
}

const nodeRoles = (o: K8sObject): string => {
  const labels = (meta(o).labels ?? {}) as Record<string, string>
  const roles = Object.keys(labels)
    .filter((k) => k.startsWith('node-role.kubernetes.io/'))
    .map((k) => k.slice('node-role.kubernetes.io/'.length))
  return roles.length ? roles.join(',') : '<none>'
}

const eventTime = (o: K8sObject): string =>
  age(
    str(get(o, 'lastTimestamp'), '') ||
      str(get(o, 'eventTime'), '') ||
      str(get(o, 'metadata.creationTimestamp'), '') ||
      undefined
  )

const eventObject = (o: K8sObject): string => {
  const ref = get(o, 'involvedObject') as Record<string, unknown> | undefined
  if (!ref?.name) return '-'
  return `${str(ref.kind, 'object').toLowerCase()}/${str(ref.name)}`
}

// labelCell renders up to two `k=v` label chips with a `+n` overflow
// counter — the Kuboard list-page label column.
const labelCell = (o: K8sObject): ReactNode => {
  const labels = Object.entries(meta(o).labels ?? {})
  if (labels.length === 0)
    return <span className='text-xs text-[#8ba3c7]'>—</span>
  return (
    <span className='flex flex-wrap items-center gap-1'>
      {labels.slice(0, 2).map(([k, v]) => (
        <Badge
          key={k}
          variant='outline'
          className='border-[#d6e1f0] bg-[#eef3fa] font-mono text-xs font-normal text-[#31598e]'
        >{`${k}=${v}`}</Badge>
      ))}
      {labels.length > 2 && (
        <span className='text-xs text-[#8ba3c7]'>+{labels.length - 2}</span>
      )}
    </span>
  )
}

const col = (
  key: string,
  title: string,
  cell: (o: K8sObject) => ReactNode
): K8sColumn => ({ key, title, cell })
// num marks a count/quantity column (right-aligned, tabular digits).
const num = (
  key: string,
  title: string,
  cell: (o: K8sObject) => ReactNode
): K8sColumn => ({ key, title, numeric: true, cell })
// statusCol wraps a status extractor so the value renders with a tone dot.
const statusCol = (
  key: string,
  title: string,
  cell: (o: K8sObject) => string
): K8sColumn => col(key, title, (o) => <StatusText value={cell(o)} />)
const NAME = col('name', '名称', name)
const NS = col('namespace', '命名空间', namespace)
const AGE = col('age', '存在时长', created)

// catalog maps each allowlisted resource to its console title and columns.
// `namespaced` is deliberately absent — it comes from /k8s/resource-types.
export const K8S_CATALOG: Record<string, K8sResourceInfo> = {
  namespaces: {
    resource: 'namespaces',
    title: '命名空间',
    columns: [
      NAME,
      statusCol('status', '状态', (o) => str(get(o, 'status.phase'))),
      AGE,
    ],
  },
  nodes: {
    resource: 'nodes',
    title: '节点',
    columns: [
      NAME,
      statusCol('status', '状态', nodeReady),
      col('roles', '角色', nodeRoles),
      col('version', '版本', (o) =>
        str(get(o, 'status.nodeInfo.kubeletVersion'))
      ),
      AGE,
    ],
  },
  events: {
    resource: 'events',
    title: '事件',
    columns: [
      NS,
      col('seen', '最近时间', eventTime),
      statusCol('type', '类型', (o) => str(get(o, 'type'))),
      col('reason', '原因', (o) => str(get(o, 'reason'))),
      col('object', '对象', eventObject),
      col('message', '消息', (o) => str(get(o, 'message'))),
    ],
  },
  deployments: {
    resource: 'deployments',
    title: 'Deployments',
    columns: [
      NAME,
      NS,
      num('ready', '就绪', workloadReady),
      num('uptodate', '最新', (o) =>
        str(get(o, 'status.updatedReplicas'), '0')
      ),
      num('available', '可用', (o) =>
        str(get(o, 'status.availableReplicas'), '0')
      ),
      AGE,
    ],
  },
  statefulsets: {
    resource: 'statefulsets',
    title: 'StatefulSets',
    columns: [NAME, NS, num('ready', '就绪', workloadReady), AGE],
  },
  daemonsets: {
    resource: 'daemonsets',
    title: 'DaemonSets',
    columns: [
      NAME,
      NS,
      num('desired', '期望', (o) =>
        str(get(o, 'status.desiredNumberScheduled'), '0')
      ),
      num('ready', '就绪', (o) => str(get(o, 'status.numberReady'), '0')),
      num('available', '可用', (o) =>
        str(get(o, 'status.numberAvailable'), '0')
      ),
      AGE,
    ],
  },
  jobs: {
    resource: 'jobs',
    title: 'Jobs',
    columns: [
      NAME,
      NS,
      num(
        'completions',
        '完成',
        (o) =>
          `${str(get(o, 'status.succeeded'), '0')}/${str(get(o, 'spec.completions'), '1')}`
      ),
      statusCol('status', '状态', jobStatus),
      AGE,
    ],
  },
  cronjobs: {
    resource: 'cronjobs',
    title: 'CronJobs',
    columns: [
      NAME,
      NS,
      col('schedule', '调度', (o) => str(get(o, 'spec.schedule'))),
      col('suspend', '暂停', (o) =>
        get(o, 'spec.suspend') === true ? '是' : '否'
      ),
      col('last', '上次调度', (o) =>
        age(str(get(o, 'status.lastScheduleTime'), '') || undefined)
      ),
      AGE,
    ],
  },
  pods: {
    resource: 'pods',
    title: '容器组（Pods）',
    columns: [
      NAME,
      NS,
      num('ready', '就绪', podReady),
      statusCol('status', '状态', podStatus),
      num('restarts', '重启', podRestarts),
      col('node', '节点', (o) => str(get(o, 'spec.nodeName'))),
      col('ip', 'Pod IP', (o) => str(get(o, 'status.podIP'))),
      AGE,
    ],
  },
  services: {
    resource: 'services',
    title: '服务（Services）',
    columns: [
      NAME,
      NS,
      col('type', '类型', (o) => str(get(o, 'spec.type'))),
      col('clusterip', 'Cluster IP', (o) => str(get(o, 'spec.clusterIP'))),
      col('ports', '端口', svcPorts),
      AGE,
    ],
  },
  ingresses: {
    resource: 'ingresses',
    title: 'Ingresses',
    columns: [
      NAME,
      NS,
      col('class', 'Class', (o) => str(get(o, 'spec.ingressClassName'))),
      col(
        'hosts',
        '主机',
        (o) =>
          list(get(o, 'spec.rules'))
            .map((r) => str(r.host, '*'))
            .join(', ') || '-'
      ),
      AGE,
    ],
  },
  // Kuboard ConfigMap list: 名称 / 不可修改 / 标签 / 创建时间 (+行内操作).
  configmaps: {
    resource: 'configmaps',
    title: 'ConfigMaps',
    columns: [
      NAME,
      col('immutable', '不可修改', (o) =>
        get(o, 'immutable') === true ? (
          <span className='rounded-md bg-[#fdf6ec] px-1.5 py-0.5 text-xs font-medium text-[#8a5a1d]'>
            是
          </span>
        ) : (
          <span className='text-xs text-[#8ba3c7]'>否</span>
        )
      ),
      col('labels', '标签', labelCell),
      AGE,
    ],
  },
  // Secret values are masked at the API layer — the table can only
  // report the type and the key count, never the values.
  secrets: {
    resource: 'secrets',
    title: 'Secrets',
    columns: [
      NAME,
      col('type', '类型', (o) => (
        <span className='font-mono text-xs text-[#2b3e5c]'>
          {str(get(o, 'type'), 'Opaque')}
        </span>
      )),
      num('data', '数据条目', (o) =>
        String(
          Object.keys({
            ...((get(o, 'data') as object) ?? {}),
            ...((get(o, 'stringData') as object) ?? {}),
          }).length
        )
      ),
      col('labels', '标签', labelCell),
      AGE,
    ],
  },
  persistentvolumeclaims: {
    resource: 'persistentvolumeclaims',
    title: 'PVC',
    columns: [
      NAME,
      NS,
      statusCol('status', '状态', (o) => str(get(o, 'status.phase'))),
      col('volume', '卷', (o) => str(get(o, 'spec.volumeName'))),
      col('capacity', '容量', (o) => str(get(o, 'status.capacity.storage'))),
      col('class', 'StorageClass', (o) => str(get(o, 'spec.storageClassName'))),
      AGE,
    ],
  },
  persistentvolumes: {
    resource: 'persistentvolumes',
    title: 'PV',
    columns: [
      NAME,
      col('capacity', '容量', (o) => str(get(o, 'spec.capacity.storage'))),
      col('modes', '访问模式', (o) =>
        list(get(o, 'spec.accessModes')).join(',')
      ),
      col('reclaim', '回收策略', (o) =>
        str(get(o, 'spec.persistentVolumeReclaimPolicy'))
      ),
      statusCol('status', '状态', (o) => str(get(o, 'status.phase'))),
      col('claim', 'Claim', (o) => {
        const ref = get(o, 'spec.claimRef') as
          | Record<string, unknown>
          | undefined
        return ref?.name ? `${str(ref.namespace)}/${str(ref.name)}` : '-'
      }),
      col('class', 'StorageClass', (o) => str(get(o, 'spec.storageClassName'))),
      AGE,
    ],
  },
  storageclasses: {
    resource: 'storageclasses',
    title: 'StorageClasses',
    columns: [
      NAME,
      col('provisioner', 'Provisioner', (o) => str(get(o, 'provisioner'))),
      col('reclaim', '回收策略', (o) => str(get(o, 'reclaimPolicy'))),
      col('binding', '绑定模式', (o) => str(get(o, 'volumeBindingMode'))),
      AGE,
    ],
  },
}

export const DEFAULT_SECTION = K8S_SECTIONS[0].id

export function sectionOf(id: string | undefined): K8sSection {
  return K8S_SECTIONS.find((s) => s.id === id) ?? K8S_SECTIONS[0]
}

// sectionForResource maps a resource back to its section — used to restore
// list context when navigating back from a detail page.
export function sectionForResource(resource: string | undefined): K8sSectionId {
  return (
    K8S_SECTIONS.find((s) => s.resources.includes(resource ?? ''))?.id ??
    DEFAULT_SECTION
  )
}

// resourceInfo resolves catalog info for a resource, falling back to a
// generic name/age table for allowlisted kinds the catalog doesn't know.
export function resourceInfo(resource: string | undefined): K8sResourceInfo {
  if (resource && K8S_CATALOG[resource]) return K8S_CATALOG[resource]
  return {
    resource: resource ?? '',
    title: resource ?? '资源',
    columns: [NAME, NS, AGE],
  }
}

// joinTypes merges the server allowlist with the catalog: only resources
// present in BOTH are shown, and `namespaced` always comes from the API.
export function joinTypes(
  types: K8sResourceType[] | undefined
): Map<string, K8sResourceType> {
  return new Map((types ?? []).map((t) => [t.resource, t]))
}
