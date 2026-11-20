import { isValidElement, type ReactNode } from 'react'
import { describe, expect, it } from 'vitest'
import type { K8sObject } from '@/api/client'
import {
  K8S_CATALOG,
  K8S_SECTIONS,
  joinTypes,
  resourceInfo,
  sectionForResource,
  sectionOf,
} from './catalog'

const obj = (o: Record<string, unknown>): K8sObject => o as K8sObject
// nodeText flattens an element tree to its textual content — label
// chips and mono spans expose their text through children, not props.
const nodeText = (n: ReactNode): string => {
  if (n == null || typeof n === 'boolean') return ''
  if (typeof n === 'string' || typeof n === 'number') return String(n)
  if (Array.isArray(n)) return n.map(nodeText).join('')
  if (isValidElement<{ children?: ReactNode }>(n))
    return nodeText(n.props.children)
  return ''
}
const cellText = (
  resource: string,
  o: Record<string, unknown>,
  key: string
): string => {
  const col = K8S_CATALOG[resource].columns.find((c) => c.key === key)
  if (!col) throw new Error(`no column ${key} on ${resource}`)
  const rendered = col.cell(obj(o))
  // statusCol cells render a StatusText element — unwrap to the raw value;
  // other element cells flatten their children.
  if (isValidElement<{ value?: string }>(rendered))
    return rendered.props.value ?? nodeText(rendered)
  return rendered as string
}

describe('sections', () => {
  it('covers the playbook information architecture', () => {
    expect(K8S_SECTIONS.map((s) => s.id)).toEqual([
      'overview',
      'workloads',
      'pods',
      'network',
      'config',
      'storage',
    ])
    // every allowlisted kind has a home in exactly one section
    const all = K8S_SECTIONS.flatMap((s) => s.resources)
    expect(new Set(all).size).toBe(all.length)
    for (const r of Object.keys(K8S_CATALOG)) expect(all).toContain(r)
  })

  it('sectionOf falls back to overview', () => {
    expect(sectionOf('workloads').id).toBe('workloads')
    expect(sectionOf(undefined).id).toBe('overview')
    expect(sectionOf('bogus').id).toBe('overview')
  })

  it('sectionForResource maps back for detail→list navigation', () => {
    expect(sectionForResource('deployments')).toBe('workloads')
    expect(sectionForResource('pods')).toBe('pods')
    expect(sectionForResource('persistentvolumes')).toBe('storage')
    expect(sectionForResource('secrets')).toBe('config')
    expect(sectionForResource(undefined)).toBe('overview')
  })
})

describe('resourceInfo', () => {
  it('returns catalog info and a generic fallback', () => {
    expect(resourceInfo('pods').title).toBe('容器组（Pods）')
    const fallback = resourceInfo('widgets')
    expect(fallback.columns.map((c) => c.key)).toEqual([
      'name',
      'namespace',
      'age',
    ])
  })
})

describe('joinTypes', () => {
  it('indexes server types by resource name', () => {
    const map = joinTypes([
      {
        group: '',
        version: 'v1',
        resource: 'pods',
        kind: 'Pod',
        namespaced: true,
      },
      {
        group: 'apps',
        version: 'v1',
        resource: 'deployments',
        kind: 'Deployment',
        namespaced: true,
      },
    ])
    expect(map.get('pods')?.namespaced).toBe(true)
    expect(map.get('deployments')?.group).toBe('apps')
    expect(joinTypes(undefined).size).toBe(0)
  })
})

describe('pod columns', () => {
  const pod = obj({
    metadata: {
      name: 'p1',
      namespace: 'dev',
      creationTimestamp: '2026-09-18T00:00:00Z',
    },
    spec: { nodeName: 'n1' },
    status: {
      phase: 'Running',
      containerStatuses: [
        { ready: true, restartCount: 2, state: { running: {} } },
        {
          ready: false,
          restartCount: 3,
          state: { waiting: { reason: 'CrashLoopBackOff' } },
        },
      ],
      initContainerStatuses: [
        { ready: true, restartCount: 4, restartPolicy: 'Always' },
        { ready: true, restartCount: 7, restartPolicy: 'Never' },
      ],
    },
  })

  it('READY counts containerStatuses', () => {
    expect(cellText('pods', pod, 'ready')).toBe('1/2')
  })

  it('RESTARTS sums containers + restartable init sidecars (kubectl parity)', () => {
    expect(cellText('pods', pod, 'restarts')).toBe('9')
  })

  it('STATUS surfaces waiting reason over phase', () => {
    expect(cellText('pods', pod, 'status')).toBe('CrashLoopBackOff')
    expect(
      cellText(
        'pods',
        { status: { phase: 'Running', containerStatuses: [] } },
        'status'
      )
    ).toBe('Running')
  })
})

describe('workload columns', () => {
  it('deployments READY is readyReplicas/spec.replicas', () => {
    const d = {
      spec: { replicas: 3 },
      status: { readyReplicas: 2, updatedReplicas: 3, availableReplicas: 2 },
    }
    expect(cellText('deployments', d, 'ready')).toBe('2/3')
    expect(cellText('deployments', d, 'uptodate')).toBe('3')
    expect(cellText('deployments', d, 'available')).toBe('2')
  })

  it('daemonsets show desired/ready/available', () => {
    const ds = {
      status: { desiredNumberScheduled: 3, numberReady: 3, numberAvailable: 3 },
    }
    expect(cellText('daemonsets', ds, 'desired')).toBe('3')
    expect(cellText('daemonsets', ds, 'ready')).toBe('3')
  })
})

describe('network/config/storage columns', () => {
  it('services render type/clusterIP/ports', () => {
    const svc = {
      spec: {
        type: 'ClusterIP',
        clusterIP: '10.96.0.1',
        ports: [
          { port: 443, targetPort: 443, protocol: 'TCP' },
          { port: 53, targetPort: 5353, protocol: 'UDP' },
        ],
      },
    }
    expect(cellText('services', svc, 'ports')).toBe('443/TCP,53→5353/UDP')
    expect(cellText('services', svc, 'clusterip')).toBe('10.96.0.1')
  })

  it('configmaps flag immutability and cap label chips at two', () => {
    expect(cellText('configmaps', { immutable: true }, 'immutable')).toBe('是')
    expect(cellText('configmaps', {}, 'immutable')).toBe('否')
    const labels = cellText(
      'configmaps',
      {
        metadata: { labels: { app: 'web', tier: 'frontend', env: 'prod' } },
      },
      'labels'
    )
    expect(labels).toContain('app=web')
    expect(labels).toContain('tier=frontend')
    expect(labels).toContain('+1')
    expect(labels).not.toContain('env=prod')
    expect(cellText('configmaps', {}, 'labels')).toBe('—')
  })

  it('secrets show the type, key count and label chips', () => {
    const s = {
      type: 'kubernetes.io/tls',
      data: { 'tls.crt': '***', 'tls.key': '***' },
    }
    expect(cellText('secrets', s, 'type')).toBe('kubernetes.io/tls')
    expect(cellText('secrets', s, 'data')).toBe('2')
    expect(cellText('secrets', {}, 'type')).toBe('Opaque')
    expect(cellText('secrets', {}, 'data')).toBe('0')
    expect(
      cellText('secrets', { metadata: { labels: { app: 'db' } } }, 'labels')
    ).toBe('app=db')
  })

  it('pvc shows phase/volume/capacity/class', () => {
    const pvc = {
      spec: { volumeName: 'pv1', storageClassName: 'local-path' },
      status: { phase: 'Bound', capacity: { storage: '10Gi' } },
    }
    expect(cellText('persistentvolumeclaims', pvc, 'status')).toBe('Bound')
    expect(cellText('persistentvolumeclaims', pvc, 'capacity')).toBe('10Gi')
    expect(cellText('persistentvolumeclaims', pvc, 'class')).toBe('local-path')
  })
})

describe('cluster-scoped columns', () => {
  it('nodes derive Ready and roles', () => {
    const node = obj({
      metadata: {
        labels: {
          'node-role.kubernetes.io/control-plane': '',
          'kubernetes.io/os': 'linux',
        },
      },
      status: {
        conditions: [{ type: 'Ready', status: 'True' }],
        nodeInfo: { kubeletVersion: 'v1.37.0' },
      },
    })
    expect(cellText('nodes', node, 'status')).toBe('Ready')
    expect(cellText('nodes', node, 'roles')).toBe('control-plane')
    expect(cellText('nodes', node, 'version')).toBe('v1.37.0')
    expect(cellText('nodes', { metadata: { labels: {} } }, 'roles')).toBe(
      '<none>'
    )
  })

  it('events render seen/type/reason/object/message', () => {
    const ev = {
      metadata: { namespace: 'dev' },
      type: 'Warning',
      reason: 'BackOff',
      message: 'restarting',
      involvedObject: { kind: 'Pod', name: 'p1' },
      lastTimestamp: '2020-01-01T00:00:00Z',
    }
    expect(cellText('events', ev, 'object')).toBe('pod/p1')
    expect(cellText('events', ev, 'type')).toBe('Warning')
    expect(cellText('events', ev, 'seen')).not.toBe('-')
  })
})
