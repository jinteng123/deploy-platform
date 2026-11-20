import { useState } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import {
  k8sApi,
  platformApi,
  type Cluster,
  type K8sObject,
  type K8sResourceType,
} from '@/api/client'
import { DirectionProvider } from '@/context/direction-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { SidebarProvider } from '@/components/ui/sidebar'
import { K8sPage, type K8sSearch } from './k8s-page'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    platformApi: { clusters: vi.fn() },
    k8sApi: {
      resourceTypes: vi.fn(),
      resources: vi.fn(),
      resource: vi.fn(),
      overview: vi.fn(),
      capabilities: vi.fn(),
      update: vi.fn(),
      updateData: vi.fn(),
      remove: vi.fn(),
      scale: vi.fn(),
      setImage: vi.fn(),
      restart: vi.fn(),
      createResource: vi.fn(),
    },
  }
})

const mockClusters = vi.mocked(platformApi.clusters)
const mockTypes = vi.mocked(k8sApi.resourceTypes)
const mockResources = vi.mocked(k8sApi.resources)
const mockOverview = vi.mocked(k8sApi.overview)
const mockResource = vi.mocked(k8sApi.resource)
const mockCaps = vi.mocked(k8sApi.capabilities)
const mockRemove = vi.mocked(k8sApi.remove)

const mkCluster = (id: number, name: string, ns = 'default'): Cluster => ({
  id,
  name,
  env_code: `c${id}`,
  environment: 'dev',
  environment_tier: 'non_production',
  deploy_policy: 'manual',
  connection_type: 'kubeconfig',
  credential_configured: true,
  insecure_skip_tls_verify: false,
  enabled: true,
  default_namespace: ns,
  created_at: '2026-01-01T00:00:00',
  updated_at: '2026-01-01T00:00:00',
})

const TYPES: K8sResourceType[] = [
  {
    group: '',
    version: 'v1',
    resource: 'namespaces',
    kind: 'Namespace',
    namespaced: false,
  },
  {
    group: '',
    version: 'v1',
    resource: 'nodes',
    kind: 'Node',
    namespaced: false,
  },
  {
    group: '',
    version: 'v1',
    resource: 'events',
    kind: 'Event',
    namespaced: true,
  },
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
    resource: 'services',
    kind: 'Service',
    namespaced: true,
  },
  {
    group: '',
    version: 'v1',
    resource: 'configmaps',
    kind: 'ConfigMap',
    namespaced: true,
    creatable: true,
    deletable: true,
    editable: true,
    data_editable: true,
  },
  {
    group: '',
    version: 'v1',
    resource: 'secrets',
    kind: 'Secret',
    namespaced: true,
    creatable: true,
    deletable: true,
    data_editable: true,
  },
  {
    group: '',
    version: 'v1',
    resource: 'persistentvolumeclaims',
    kind: 'PVC',
    namespaced: true,
    creatable: true,
  },
]

const obj = (name: string, ns?: string): K8sObject => ({
  apiVersion: 'v1',
  kind: 'Pod',
  metadata: {
    name,
    namespace: ns,
    uid: `u-${name}`,
    creationTimestamp: '2026-09-01T00:00:00Z',
  },
  spec: {},
  status: {
    phase: 'Running',
    containerStatuses: [{ ready: true, restartCount: 0 }],
  },
})

// Kuboard-style data resources carry the full object (data included) in
// the list response — the detail dialog reads it directly.
const cmObj = (name: string, ns?: string): K8sObject => ({
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: {
    name,
    namespace: ns,
    uid: `u-${name}`,
    resourceVersion: '7',
    creationTimestamp: '2026-09-01T00:00:00Z',
    labels: { app: 'web' },
  },
  data: { 'app.yaml': 'listen: 8080' },
})

const listOf = (kind: string, items: K8sObject[]) => ({
  apiVersion: 'v1',
  kind: `${kind}List`,
  metadata: {},
  items,
})

function Harness({
  initial,
  onOpenDetail,
}: {
  initial: K8sSearch
  onOpenDetail: (q: unknown) => void
}) {
  const [search, setSearch] = useState(initial)
  return (
    <K8sPage
      search={search}
      onSearch={(patch) => setSearch((s) => ({ ...s, ...patch }))}
      onOpenDetail={onOpenDetail}
    />
  )
}

async function renderPage(initial: K8sSearch = {}) {
  const onOpenDetail = vi.fn()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const resolvedInitial =
    initial.cluster != null && initial.ns == null
      ? { ...initial, ns: 'default' }
      : initial
  const screen = await render(
    <QueryClientProvider client={client}>
      <DirectionProvider>
        <ThemeProvider>
          <SidebarProvider>
            <Harness initial={resolvedInitial} onOpenDetail={onOpenDetail} />
          </SidebarProvider>
        </ThemeProvider>
      </DirectionProvider>
    </QueryClientProvider>
  )
  return { screen, onOpenDetail }
}

// k8sApi.resources is mocked wholesale — the mock receives the params
// object itself as the first argument.
type CallParams = {
  resource: string
  cluster_id?: number
  namespace?: string
  all_namespaces?: boolean
  label_selector?: string
  field_selector?: string
}
const resourceParams = (call: unknown): CallParams => (call as [CallParams])[0]

const namespaceList = () =>
  Promise.resolve({ data: listOf('Namespace', [obj('default'), obj('dev')]) })

const overviewFixture = {
  server_version: 'v1.31.0-mock',
  nodes: {
    total: 2,
    ready: 1,
    items: [
      {
        name: 'node-a',
        ready: true,
        roles: ['control-plane'],
        kubelet_version: 'v1.31.0',
        os_image: 'Debian',
        internal_ip: '10.0.0.1',
        cpu_capacity_millis: 4000,
        mem_capacity_bytes: 8 * 1024 ** 3,
        usage: { cpu_millis: 500, mem_bytes: 2 * 1024 ** 3 },
      },
      {
        name: 'node-b',
        ready: false,
        roles: [],
        kubelet_version: 'v1.31.0',
        os_image: 'Debian',
        internal_ip: '10.0.0.2',
        cpu_capacity_millis: 2000,
        mem_capacity_bytes: 4 * 1024 ** 3,
      },
    ],
  },
  namespaces: 3,
  workloads: {
    deployments: 2,
    stateful_sets: 1,
    daemon_sets: 1,
    jobs: 0,
    cron_jobs: 1,
    pods: 5,
    pods_running: 4,
  },
  warning_events: [
    {
      namespace: 'default',
      reason: 'BackOff',
      message: 'Back-off restarting failed container',
      object: 'Pod/pod-a',
      count: 3,
      last_timestamp: '2026-09-18T02:00:00Z',
    },
  ],
  metrics: { available: true },
  top_pods: [
    {
      namespace: 'default',
      name: 'busy-pod',
      usage: { cpu_millis: 900, mem_bytes: 1024 ** 3 },
    },
  ],
}

// calledWith reports whether resources() was invoked with matching params.
const calledWith = (want: Partial<CallParams>) =>
  mockResources.mock.calls.some((c) => {
    const p = resourceParams(c)
    return Object.entries(want).every(
      ([k, v]) => (p as unknown as Record<string, unknown>)[k] === v
    )
  })

describe('K8sPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockClusters.mockResolvedValue({
      data: [mkCluster(1, 'kind-kbdev'), mkCluster(2, 'staging')],
    } as never)
    mockTypes.mockResolvedValue({ data: TYPES } as never)
    mockResources.mockImplementation((params: CallParams) => {
      if (params.resource === 'namespaces') return namespaceList() as never
      if (params.resource === 'pods')
        return Promise.resolve({
          data: listOf('Pod', [obj('pod-a', 'default')]),
        }) as never
      if (params.resource === 'deployments')
        return Promise.resolve({
          data: listOf('Deployment', [obj('dep-a', 'default')]),
        }) as never
      return Promise.resolve({ data: listOf('X', []) }) as never
    })
    mockOverview.mockResolvedValue({ data: overviewFixture } as never)
    mockResource.mockResolvedValue({ data: obj('pod-a', 'default') } as never)
    mockCaps.mockResolvedValue({
      data: { resources: {}, exec: false, logs: false },
    } as never)
    mockRemove.mockResolvedValue({ data: { detail: 'ok' } } as never)
  })

  it('shows the cluster list before a cluster is selected', async () => {
    const { screen } = await renderPage({})
    await expect
      .element(screen.getByRole('heading', { name: 'Kubernetes 集群列表' }))
      .toBeInTheDocument()
    await expect.element(screen.getByText('共 2 个集群')).toBeInTheDocument()
    await expect.element(screen.getByText('kind-kbdev')).toBeInTheDocument()
    await expect.element(screen.getByText('staging')).toBeInTheDocument()

    await userEvent.click(
      screen.getByRole('button', { name: '打开集群 kind-kbdev' })
    )
    await expect.element(screen.getByRole('dialog')).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: '选择名称空间 default' })
    )
    await expect.element(screen.getByText('默认')).toBeInTheDocument()
    await expect.element(screen.getByText('dep-a')).toBeInTheDocument()
    await vi.waitFor(() => expect(mockOverview).toHaveBeenCalledWith(1))
  })

  it('renders only the current section resources and the default resource table', async () => {
    const { screen } = await renderPage({ cluster: 1, section: 'pods' })
    await expect.element(screen.getByText('pod-a')).toBeInTheDocument()
    // effective namespace shown next to the title
    await expect
      .element(screen.getByText('default').first())
      .toBeInTheDocument()
  })

  it('scopes the resource tabs to the workload section', async () => {
    const { screen } = await renderPage({ cluster: 1, section: 'workloads' })
    for (const title of [
      'Deployments',
      'StatefulSets',
      'DaemonSets',
      'Jobs',
      'CronJobs',
    ]) {
      await expect
        .element(screen.getByRole('tab', { name: title, exact: true }))
        .toBeInTheDocument()
    }
    await expect.element(screen.getByText('dep-a')).toBeInTheDocument()
  })

  it('queries pods with the resolved default namespace', async () => {
    await renderPage({ cluster: 1, section: 'pods' })
    await vi.waitFor(() =>
      expect(
        calledWith({ resource: 'pods', namespace: 'default', cluster_id: 1 })
      ).toBe(true)
    )
  })

  it('does not show previous rows while a namespace switch is pending', async () => {
    const { screen } = await renderPage({ cluster: 1, section: 'pods' })
    await expect.element(screen.getByText('pod-a')).toBeInTheDocument()

    // hang the pods fetch for the dev namespace
    mockResources.mockImplementation((params: CallParams) => {
      if (params.resource === 'namespaces') return namespaceList() as never
      return new Promise(() => {}) as never
    })

    await userEvent.click(screen.getByRole('combobox').nth(1))
    await userEvent.click(screen.getByRole('option', { name: 'dev' }))

    // while the new key is pending the previous cluster/ns data must be gone
    await expect.element(screen.getByText('pod-a')).not.toBeInTheDocument()
    // pending state renders skeleton rows rather than stale data
    await vi.waitFor(() =>
      expect(
        document.querySelectorAll("[data-slot='skeleton']").length
      ).toBeGreaterThan(0)
    )
  })

  it('switches cluster and reopens the namespace home', async () => {
    const { screen } = await renderPage({ cluster: 1, section: 'pods' })
    await expect.element(screen.getByText('pod-a')).toBeInTheDocument()

    mockResources.mockImplementation((params: CallParams) => {
      if (params.resource === 'namespaces') return namespaceList() as never
      if (params.resource === 'pods' && params.cluster_id === 2)
        return Promise.resolve({
          data: listOf('Pod', [obj('pod-b', 'default')]),
        }) as never
      return Promise.resolve({ data: listOf('Pod', []) }) as never
    })

    await userEvent.click(screen.getByRole('combobox').nth(0))
    await userEvent.click(screen.getByRole('option', { name: 'staging' }))
    await expect.element(screen.getByRole('dialog')).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: '选择名称空间 default' })
    )

    await expect.element(screen.getByText('默认')).toBeInTheDocument()
    await expect.element(screen.getByText('pod-a')).not.toBeInTheDocument()
    await vi.waitFor(() =>
      expect(calledWith({ resource: 'namespaces', cluster_id: 2 })).toBe(true)
    )
  })

  it('restores selection state from search params (refresh restores view)', async () => {
    const { screen } = await renderPage({
      cluster: 1,
      section: 'pods',
      ns: 'dev',
    })
    await vi.waitFor(() =>
      expect(calledWith({ resource: 'pods', namespace: 'dev' })).toBe(true)
    )
    await expect.element(screen.getByText('pod-a')).toBeInTheDocument()
  })

  it('opens a pod row in the detail drawer, keeping the list mounted', async () => {
    const { screen, onOpenDetail } = await renderPage({
      cluster: 1,
      section: 'pods',
    })
    await expect.element(screen.getByText('pod-a')).toBeInTheDocument()
    await userEvent.click(await screen.getByText('pod-a'))
    // Kuboard-style drawer: no page navigation…
    expect(onOpenDetail).not.toHaveBeenCalled()
    // …the sheet renders its namespaced title…
    await expect.element(screen.getByText(/容器组: pod-a/)).toBeInTheDocument()
    // …and the underlying list is still in the DOM.
    await expect
      .element(screen.getByRole('cell', { name: 'pod-a', exact: true }))
      .toBeInTheDocument()
  })

  it('opens the detail page with the row identity for non-pod kinds', async () => {
    const { screen, onOpenDetail } = await renderPage({
      cluster: 1,
      section: 'workloads',
      res: 'deployments',
    })
    await expect.element(screen.getByText('dep-a')).toBeInTheDocument()
    await userEvent.click(await screen.getByText('dep-a'))
    expect(onOpenDetail).toHaveBeenCalledWith({
      cluster: 1,
      g: 'apps',
      v: 'v1',
      r: 'deployments',
      ns: 'default',
      name: 'dep-a',
    })
  })

  it('all-namespaces lists across namespaces', async () => {
    const { screen } = await renderPage({ cluster: 1, section: 'pods' })
    await expect.element(screen.getByText('pod-a')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('combobox').nth(1))
    await userEvent.click(screen.getByRole('option', { name: '全部命名空间' }))
    await vi.waitFor(() =>
      expect(calledWith({ resource: 'pods', all_namespaces: true })).toBe(true)
    )
  })

  it('disables the namespace selector for cluster-scoped resources', async () => {
    const { screen } = await renderPage({
      cluster: 1,
      section: 'overview',
      res: 'nodes',
    })
    await vi.waitFor(() => expect(calledWith({ resource: 'nodes' })).toBe(true))
    const nsSelect = screen.getByRole('combobox').nth(1)
    await expect.element(nsSelect).toBeDisabled()
  })

  it('renders the cluster dashboard as the overview default tab', async () => {
    const { screen } = await renderPage({ cluster: 1, section: 'overview' })
    await expect.element(screen.getByText('v1.31.0-mock')).toBeInTheDocument()
    await expect.element(screen.getByText('1 / 2 就绪')).toBeInTheDocument()
    await expect.element(screen.getByText('node-a')).toBeInTheDocument()
    // metrics available → top pods table populated
    await expect.element(screen.getByText('busy-pod')).toBeInTheDocument()
    // warning events table shows only warnings
    await expect.element(screen.getByText('BackOff')).toBeInTheDocument()
    await vi.waitFor(() => expect(mockOverview).toHaveBeenCalledWith(1))
    // the dashboard never fires a resources list for the synthetic res
    await vi.waitFor(() =>
      expect(
        mockResources.mock.calls.some(
          (c) => resourceParams(c).resource === 'dashboard'
        )
      ).toBe(false)
    )
  })

  it('shows 暂无数据 for metrics while other overview sections still render', async () => {
    mockOverview.mockResolvedValue({
      data: {
        ...overviewFixture,
        metrics: { available: false, message: 'no metrics API' },
        top_pods: [],
      },
    } as never)
    const { screen } = await renderPage({ cluster: 1, section: 'overview' })
    await expect.element(screen.getByText('node-a')).toBeInTheDocument()
    await expect.element(screen.getByText('BackOff')).toBeInTheDocument()
    await expect
      .element(screen.getByText('暂无数据').first())
      .toBeInTheDocument()
  })

  it('shows the PVC create button when creatable and caps allow create', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          '/v1/persistentvolumeclaims': {
            list: true,
            create: true,
            update: false,
            delete: false,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderPage({
      cluster: 1,
      section: 'storage',
      res: 'persistentvolumeclaims',
    })
    await expect
      .element(screen.getByRole('button', { name: '创建存储声明' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '创建存储声明' }))
      .toBeEnabled()
  })

  it('hides the PVC create button when caps deny create', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          '/v1/persistentvolumeclaims': {
            list: true,
            create: false,
            update: false,
            delete: false,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderPage({
      cluster: 1,
      section: 'storage',
      res: 'persistentvolumeclaims',
    })
    await expect
      .element(screen.getByRole('button', { name: '刷新' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '创建存储声明' }))
      .not.toBeInTheDocument()
  })

  it('disables the PVC create button in all-namespaces mode', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          '/v1/persistentvolumeclaims': {
            list: true,
            create: true,
            update: false,
            delete: false,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderPage({
      cluster: 1,
      section: 'storage',
      res: 'persistentvolumeclaims',
      ns: '*',
    })
    const btn = screen.getByRole('button', { name: '创建存储声明' })
    await expect.element(btn).toBeInTheDocument()
    await expect.element(btn).toBeDisabled()
  })

  it('shows the ConfigMap create button and opens its dialog', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          '/v1/configmaps': {
            list: true,
            create: true,
            update: false,
            delete: false,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderPage({
      cluster: 1,
      section: 'config',
      res: 'configmaps',
    })
    const btn = screen.getByRole('button', { name: '创建配置字典' })
    await expect.element(btn).toBeInTheDocument()
    await expect.element(btn).toBeEnabled()
    await userEvent.click(btn)
    await expect.element(screen.getByRole('dialog')).toBeInTheDocument()
    await expect
      .element(screen.getByText('ConfigMap · 以明文键值对存储非机密配置'))
      .toBeInTheDocument()
  })

  it('shows the Secret create button on the secrets tab and hides it when caps deny', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          '/v1/secrets': {
            list: true,
            create: true,
            update: false,
            delete: false,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderPage({
      cluster: 1,
      section: 'config',
      res: 'secrets',
    })
    // Config and Secret share the 配置 tab group.
    await expect
      .element(screen.getByRole('tab', { name: 'ConfigMaps', exact: true }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('tab', { name: 'Secrets', exact: true }))
      .toBeInTheDocument()
    const btn = screen.getByRole('button', { name: '创建密钥' })
    await expect.element(btn).toBeInTheDocument()
    await userEvent.click(btn)
    await expect.element(screen.getByRole('dialog')).toBeInTheDocument()
    await expect
      .element(
        screen.getByText('Secret · 敏感信息以 stringData 提交，由集群加密存储')
      )
      .toBeInTheDocument()
  })

  it('hides the Secret create button when caps deny create', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          '/v1/secrets': {
            list: true,
            create: false,
            update: false,
            delete: false,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderPage({
      cluster: 1,
      section: 'config',
      res: 'secrets',
    })
    await expect
      .element(screen.getByRole('button', { name: '刷新' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '创建密钥' }))
      .not.toBeInTheDocument()
  })

  it('renders a cluster-scoped overview resource standalone, without sub-tabs', async () => {
    const { screen } = await renderPage({
      cluster: 1,
      section: 'overview',
      res: 'namespaces',
    })
    await vi.waitFor(() =>
      expect(calledWith({ resource: 'namespaces' })).toBe(true)
    )
    await expect
      .element(screen.getByRole('cell', { name: 'default' }))
      .toBeInTheDocument()
    // overview resources render standalone — switching happens in the
    // sidebar, not a tab bar mixing dashboard/namespaces/nodes/events
    await expect
      .element(screen.getByRole('tab', { name: '节点' }))
      .not.toBeInTheDocument()
  })

  // ---- Kuboard-style ConfigMap/Secret list interactions ----

  const cmList = (...names: string[]) =>
    Promise.resolve({
      data: listOf(
        'ConfigMap',
        names.map((n) => cmObj(n, 'default'))
      ),
    })

  const withConfigmaps = (params: CallParams) => {
    if (params.resource === 'namespaces') return namespaceList() as never
    if (params.resource === 'configmaps')
      return cmList('app-config', 'db-config') as never
    return Promise.resolve({ data: listOf('X', []) }) as never
  }

  it('opens a ConfigMap row in the in-place detail dialog, not the detail page', async () => {
    mockResources.mockImplementation(withConfigmaps)
    mockResource.mockResolvedValue({
      data: cmObj('app-config', 'default'),
    } as never)
    const { screen, onOpenDetail } = await renderPage({
      cluster: 1,
      section: 'config',
      res: 'configmaps',
    })
    await expect
      .element(screen.getByRole('cell', { name: 'app-config', exact: true }))
      .toBeInTheDocument()
    await userEvent.click(screen.getByText('app-config'))
    // Kuboard dialog in place — no navigation.
    expect(onOpenDetail).not.toHaveBeenCalled()
    await expect
      .element(screen.getByText('ConfigMap: default / app-config'))
      .toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('tab', { name: '数据', exact: true })
    )
    await expect.element(screen.getByText('listen: 8080')).toBeInTheDocument()
    // The 字段选择器 input is only rendered for the data-list kinds.
    await expect
      .element(screen.getByLabelText('字段选择器 metadata.name'))
      .toBeInTheDocument()
  })

  it('does not render the field selector for other resources', async () => {
    const { screen } = await renderPage({ cluster: 1, section: 'pods' })
    await expect.element(screen.getByText('pod-a')).toBeInTheDocument()
    expect(screen.getByLabelText('字段选择器 metadata.name').query()).toBeNull()
  })

  it('sends metadata.name as field_selector when the field selector is filled', async () => {
    mockResources.mockImplementation(withConfigmaps)
    const { screen } = await renderPage({
      cluster: 1,
      section: 'config',
      res: 'configmaps',
    })
    await expect
      .element(screen.getByLabelText('字段选择器 metadata.name'))
      .toBeInTheDocument()
    await userEvent.fill(
      screen.getByLabelText('字段选择器 metadata.name'),
      'app-config'
    )
    await vi.waitFor(() =>
      expect(
        calledWith({
          resource: 'configmaps',
          field_selector: 'metadata.name=app-config',
        })
      ).toBe(true)
    )
  })

  it('batch-deletes selected ConfigMaps after the confirm dialog', async () => {
    mockResources.mockImplementation(withConfigmaps)
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          '/v1/configmaps': {
            list: true,
            create: false,
            update: false,
            delete: true,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderPage({
      cluster: 1,
      section: 'config',
      res: 'configmaps',
    })
    await expect
      .element(screen.getByRole('cell', { name: 'app-config', exact: true }))
      .toBeInTheDocument()

    // Select both rows via the header checkbox.
    await userEvent.click(screen.getByRole('checkbox', { name: '全选' }))
    const trigger = screen.getByRole('button', { name: '删除 (2)' })
    await expect.element(trigger).toBeInTheDocument()
    await userEvent.click(trigger)
    await expect.element(screen.getByText('删除 2 个对象')).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: '删除', exact: true }).last()
    )
    await vi.waitFor(() => expect(mockRemove).toHaveBeenCalledTimes(2))
    expect(mockRemove).toHaveBeenCalledWith(
      'app-config',
      expect.objectContaining({ resource: 'configmaps', namespace: 'default' }),
      '7'
    )
    await vi.waitFor(() =>
      expect(screen.getByRole('button', { name: /删除 \(/ }).query()).toBeNull()
    )
  })

  it('row 操作 opens the dialog via 查看 and deletes via 删除 without row navigation', async () => {
    mockResources.mockImplementation(withConfigmaps)
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          '/v1/configmaps': {
            list: true,
            create: false,
            update: false,
            delete: true,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen, onOpenDetail } = await renderPage({
      cluster: 1,
      section: 'config',
      res: 'configmaps',
    })
    await expect
      .element(screen.getByRole('cell', { name: 'app-config', exact: true }))
      .toBeInTheDocument()

    // 查看 opens the same in-place dialog as the row click.
    await userEvent.click(screen.getByRole('button', { name: '查看' }).first())
    expect(onOpenDetail).not.toHaveBeenCalled()
    await expect
      .element(screen.getByText('ConfigMap: default / app-config'))
      .toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '关闭窗口' }))

    // 删除 opens the name-confirm dialog; confirm calls remove.
    await userEvent.click(
      screen.getByRole('button', { name: '删除', exact: true }).first()
    )
    const confirm = screen
      .getByRole('button', { name: '删除', exact: true })
      .last()
    await expect.element(confirm).toBeDisabled()
    await userEvent.fill(screen.getByRole('textbox'), 'app-config')
    await userEvent.click(confirm)
    await vi.waitFor(() =>
      expect(mockRemove).toHaveBeenCalledWith(
        'app-config',
        expect.objectContaining({ resource: 'configmaps' }),
        '7'
      )
    )
  })
})
