import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { k8sApi, type K8sObject, type K8sResourceType } from '@/api/client'
import { DirectionProvider } from '@/context/direction-provider'
import { ThemeProvider } from '@/context/theme-provider'
import type { K8sDetailQuery } from './k8s-page'
import { PodDetailDrawer } from './pod-detail-drawer'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    k8sApi: {
      resource: vi.fn(),
      resources: vi.fn(),
      resourceTypes: vi.fn(),
      capabilities: vi.fn(),
      remove: vi.fn(),
      scale: vi.fn(),
      setImage: vi.fn(),
      restart: vi.fn(),
      update: vi.fn(),
    },
  }
})
const api = vi.mocked(k8sApi)

const pod: K8sObject = {
  apiVersion: 'v1',
  kind: 'Pod',
  metadata: {
    name: 'web-abc',
    namespace: 'prod',
    uid: 'pod-uid-1',
    creationTimestamp: '2026-09-01T00:00:00Z',
    ownerReferences: [
      {
        apiVersion: 'apps/v1',
        kind: 'Deployment',
        name: 'web',
        uid: 'dep-uid-1',
        controller: true,
      },
    ],
  },
  spec: {
    nodeName: 'worker-1',
    containers: [
      { name: 'app', image: 'nginx:1.25' },
      { name: 'side', image: 'busy:1' },
    ],
    initContainers: [{ name: 'init-db', image: 'init:1' }],
    volumes: [
      { name: 'cfg', configMap: { name: 'web-cfg' } },
      { name: 'scratch', emptyDir: {} },
    ],
  },
  status: {
    phase: 'Running',
    podIP: '10.1.2.3',
    hostIP: '192.168.1.10',
    qosClass: 'Burstable',
    conditions: [
      {
        type: 'Ready',
        status: 'True',
        reason: '',
        lastTransitionTime: '2026-09-01T00:01:00Z',
      },
      {
        type: 'PodScheduled',
        status: 'True',
        lastTransitionTime: '2026-09-01T00:00:30Z',
      },
    ],
    containerStatuses: [
      {
        name: 'app',
        ready: true,
        restartCount: 2,
        state: { running: { startedAt: '2026-09-01T00:02:00Z' } },
      },
      {
        name: 'side',
        ready: false,
        restartCount: 0,
        state: { waiting: { reason: 'CrashLoopBackOff' } },
      },
    ],
    initContainerStatuses: [
      {
        name: 'init-db',
        ready: true,
        restartCount: 0,
        state: { terminated: { reason: 'Completed' } },
      },
    ],
  },
}

const TYPES: K8sResourceType[] = [
  {
    group: 'apps',
    version: 'v1',
    resource: 'deployments',
    kind: 'Deployment',
    namespaced: true,
  },
  { group: '', version: 'v1', resource: 'pods', kind: 'Pod', namespaced: true },
  {
    group: '',
    version: 'v1',
    resource: 'events',
    kind: 'Event',
    namespaced: true,
  },
]
const typeMap = new Map(TYPES.map((t) => [t.resource, t]))

const query: K8sDetailQuery = {
  cluster: 1,
  g: '',
  v: 'v1',
  r: 'pods',
  ns: 'prod',
  name: 'web-abc',
}

async function renderDrawer(onOpenOwner = vi.fn(), onClose = vi.fn()) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DirectionProvider>
        <ThemeProvider>
          <PodDetailDrawer
            query={query}
            typeMap={typeMap}
            onClose={onClose}
            onOpenOwner={onOpenOwner}
            onOpenTerminal={vi.fn()}
          />
        </ThemeProvider>
      </DirectionProvider>
    </QueryClientProvider>
  )
  return { screen, onOpenOwner, onClose }
}

describe('PodDetailDrawer', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.resource.mockResolvedValue({ data: pod } as never)
    api.resources.mockResolvedValue({
      data: {
        apiVersion: 'v1',
        kind: 'EventList',
        items: [
          {
            apiVersion: 'v1',
            kind: 'Event',
            metadata: { name: 'ev1', uid: 'ev-1' },
            type: 'Warning',
            reason: 'BackOff',
            message: 'Back-off restarting failed container',
            lastTimestamp: '2026-09-01T00:05:00Z',
          },
        ],
      },
    } as never)
    api.capabilities.mockResolvedValue({
      data: { resources: {}, exec: false, logs: false },
    } as never)
    api.resourceTypes.mockResolvedValue({ data: TYPES } as never)
  })

  it('renders the namespaced sheet title and pod info blocks', async () => {
    const { screen } = await renderDrawer()
    const dialog = screen.getByRole('dialog', { name: /名称空间: prod/ })
    await expect.element(dialog).toBeInTheDocument()
    await expect
      .element(
        dialog.getByText('查看该 Pod 的状态、事件、日志、终端和 YAML 详情。')
      )
      .toBeInTheDocument()
    await expect.element(screen.getByText(/名称空间: prod/)).toBeInTheDocument()
    await expect.element(screen.getByText('基本信息')).toBeInTheDocument()
    await expect.element(screen.getByText('pod-uid-1')).toBeInTheDocument()
    await expect.element(screen.getByText('10.1.2.3')).toBeInTheDocument()
    await expect.element(screen.getByText('worker-1')).toBeInTheDocument()
  })

  it('lists containers incl. init with per-container state and restarts', async () => {
    const { screen } = await renderDrawer()
    await expect.element(screen.getByText('容器（3）')).toBeInTheDocument()
    await expect.element(screen.getByText('init-db')).toBeInTheDocument()
    await expect
      .element(screen.getByText('CrashLoopBackOff'))
      .toBeInTheDocument()
    // init container badge
    await expect
      .element(screen.getByText('init', { exact: true }))
      .toBeInTheDocument()
  })

  it('shows pod conditions and volumes', async () => {
    const { screen } = await renderDrawer()
    await expect.element(screen.getByText('状态条件')).toBeInTheDocument()
    await expect.element(screen.getByText('PodScheduled')).toBeInTheDocument()
    await expect.element(screen.getByText('存储卷（2）')).toBeInTheDocument()
    await expect.element(screen.getByText('configMap')).toBeInTheDocument()
  })

  it('navigates to the owning workload via onOpenOwner', async () => {
    const onOpenOwner = vi.fn()
    const { screen } = await renderDrawer(onOpenOwner)
    await expect.element(screen.getByText('所属工作负载')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /web/ }))
    expect(onOpenOwner).toHaveBeenCalledWith({
      cluster: 1,
      g: 'apps',
      v: 'v1',
      r: 'deployments',
      ns: 'prod',
      name: 'web',
    })
  })

  it('renders an unmapped owner kind as plain text', async () => {
    const rsPod: K8sObject = {
      ...pod,
      metadata: {
        ...pod.metadata,
        ownerReferences: [
          {
            apiVersion: 'apps/v1',
            kind: 'ReplicaSet',
            name: 'web-abc',
            uid: 'rs-1',
          },
        ],
      },
    }
    api.resource.mockResolvedValue({ data: rsPod } as never)
    const { screen } = await renderDrawer()
    await expect.element(screen.getByText('ReplicaSet')).toBeInTheDocument()
    // no link is rendered for kinds outside the registry
    expect(screen.getByRole('button', { name: /web-abc/ }).query()).toBeNull()
  })

  it('fetches pod events via a server-side field selector', async () => {
    const { screen } = await renderDrawer()
    await vi.waitFor(() =>
      expect(api.resources).toHaveBeenCalledWith(
        expect.objectContaining({
          resource: 'events',
          field_selector: 'involvedObject.name=web-abc',
        })
      )
    )
    await userEvent.click(screen.getByRole('tab', { name: '事件' }))
    await expect.element(screen.getByText('BackOff')).toBeInTheDocument()
    await expect
      .element(screen.getByText('Back-off restarting failed container'))
      .toBeInTheDocument()
  })
})
