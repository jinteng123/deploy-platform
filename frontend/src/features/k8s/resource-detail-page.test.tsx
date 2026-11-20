import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { k8sApi, type K8sObject } from '@/api/client'
import { DirectionProvider } from '@/context/direction-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { SidebarProvider } from '@/components/ui/sidebar'
import { ResourceDetailPage } from './resource-detail-page'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    k8sApi: {
      resource: vi.fn(),
      resources: vi.fn(),
      resourceTypes: vi.fn(),
      overview: vi.fn(),
      logs: vi.fn(),
      restartPod: vi.fn(),
      capabilities: vi.fn(),
    },
  }
})
// The detail page mounts the YAML card — stub Monaco so the heavyweight
// editor never loads in the page-level tests.
vi.mock('@monaco-editor/react', () => ({
  default: ({ value }: { value: string }) => (
    <textarea aria-label='monaco-stub' value={value} readOnly />
  ),
  loader: { config: vi.fn() },
}))
vi.mock('@/features/k8s/monaco', () => ({}))

const mockResource = vi.mocked(k8sApi.resource)
const mockResources = vi.mocked(k8sApi.resources)
const mockTypes = vi.mocked(k8sApi.resourceTypes)
const mockOverview = vi.mocked(k8sApi.overview)

const pod: K8sObject = {
  apiVersion: 'v1',
  kind: 'Pod',
  metadata: {
    name: 'pod-a',
    namespace: 'dev',
    uid: 'u-1',
    creationTimestamp: '2026-09-01T00:00:00Z',
    labels: { app: 'web' },
  },
  spec: { containers: [{ name: 'web', image: 'nginx:1' }] },
  status: { phase: 'Running' },
}

async function renderDetail(
  search: Parameters<typeof ResourceDetailPage>[0]['search']
) {
  const onBack = vi.fn()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DirectionProvider>
        <ThemeProvider>
          <SidebarProvider>
            <ResourceDetailPage
              search={search}
              onBack={onBack}
              onOpenTerminal={vi.fn()}
            />
          </SidebarProvider>
        </ThemeProvider>
      </DirectionProvider>
    </QueryClientProvider>
  )
  return { screen, onBack }
}

describe('ResourceDetailPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockResource.mockResolvedValue({ data: pod } as never)
    mockResources.mockResolvedValue({ data: { items: [] } } as never)
    mockOverview.mockResolvedValue({
      data: { metrics: { available: false }, top_pods: [] },
    } as never)
    mockTypes.mockResolvedValue({
      data: [
        {
          group: 'apps',
          version: 'v1',
          resource: 'deployments',
          kind: 'Deployment',
          namespaced: true,
          scalable: true,
          imageable: true,
          restartable: true,
          deletable: true,
        },
      ],
    } as never)
    vi.mocked(k8sApi.capabilities).mockResolvedValue({
      data: {
        resources: {
          'apps/v1/deployments': { list: true, update: true, delete: true },
          '/v1/pods': { list: true, update: true, delete: true },
        },
        exec: true,
        logs: true,
      },
    } as never)
  })

  it('shows the action bar for a writable kind', async () => {
    mockResource.mockResolvedValue({
      data: {
        apiVersion: 'apps/v1',
        kind: 'Deployment',
        metadata: { name: 'web', namespace: 'prod', resourceVersion: '9' },
        spec: {
          replicas: 2,
          template: { spec: { containers: [{ name: 'app', image: 'x:1' }] } },
        },
      },
    } as never)
    const { screen } = await renderDetail({
      cluster: 1,
      g: 'apps',
      v: 'v1',
      r: 'deployments',
      ns: 'prod',
      name: 'web',
    })
    await expect
      .element(screen.getByRole('button', { name: '伸缩' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '删除' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('tab', { name: '运行时' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: 'YAML', exact: true }))
      .toBeInTheDocument()
  })

  it('renders the Kuboard-style runtime topology with related pods and events', async () => {
    mockResource.mockResolvedValue({
      data: {
        apiVersion: 'apps/v1',
        kind: 'Deployment',
        metadata: {
          name: 'web',
          namespace: 'prod',
          resourceVersion: '9',
          annotations: { 'deployment.kubernetes.io/revision': '4' },
        },
        spec: {
          replicas: 1,
          selector: { matchLabels: { app: 'web' } },
          template: { spec: { containers: [{ name: 'app', image: 'web:1' }] } },
        },
        status: { readyReplicas: 1 },
      },
    } as never)
    mockResources.mockImplementation((params) =>
      Promise.resolve({
        data: {
          items:
            params.resource === 'pods'
              ? [
                  {
                    apiVersion: 'v1',
                    kind: 'Pod',
                    metadata: { name: 'web-abc-123', namespace: 'prod' },
                    spec: {
                      nodeName: 'worker-1',
                      containers: [{ name: 'app', image: 'web:1' }],
                    },
                    status: {
                      phase: 'Running',
                      podIP: '10.0.0.8',
                      conditions: [
                        {
                          type: 'Ready',
                          status: 'True',
                          lastTransitionTime: '2026-09-22T00:50:44Z',
                        },
                        {
                          type: 'ContainersReady',
                          status: 'True',
                          lastTransitionTime: '2026-09-22T00:50:44Z',
                        },
                        {
                          type: 'Initialized',
                          status: 'True',
                          lastTransitionTime: '2026-09-21T12:45:12Z',
                        },
                        {
                          type: 'PodScheduled',
                          status: 'True',
                          lastTransitionTime: '2026-09-21T12:45:12Z',
                        },
                      ],
                      containerStatuses: [
                        {
                          name: 'app',
                          ready: true,
                          restartCount: 0,
                          state: { running: {} },
                        },
                      ],
                    },
                  },
                  {
                    apiVersion: 'v1',
                    kind: 'Pod',
                    metadata: { name: 'web-abc-completed', namespace: 'prod' },
                    spec: {
                      nodeName: 'worker-1',
                      containers: [{ name: 'app', image: 'web:1' }],
                    },
                    status: {
                      phase: 'Succeeded',
                      containerStatuses: [
                        {
                          name: 'app',
                          ready: false,
                          restartCount: 0,
                          state: { terminated: { reason: 'Completed' } },
                        },
                      ],
                    },
                  },
                ]
              : [
                  {
                    metadata: { name: 'event-1' },
                    involvedObject: { kind: 'Pod', name: 'web-abc-123' },
                    type: 'Normal',
                    reason: 'Started',
                    message: 'Started container app',
                    count: 1,
                  },
                ],
        },
      } as never)
    )

    const { screen } = await renderDetail({
      cluster: 1,
      g: 'apps',
      v: 'v1',
      r: 'deployments',
      ns: 'prod',
      name: 'web',
    })
    await expect
      .element(screen.getByText('web-abc-123').first())
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: 'bash', exact: true }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('Completed', { exact: true }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('cell', { name: 'Started', exact: true }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: 'Pod YAML' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('Initialized', { exact: true }))
      .not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /状态详情/ }))
    await expect
      .element(screen.getByText('Initialized', { exact: true }))
      .toBeInTheDocument()
    expect(mockResources).toHaveBeenCalledWith(
      expect.objectContaining({
        resource: 'pods',
        label_selector: 'app=web',
        namespace: 'prod',
      })
    )
  })

  it('renders object metadata, spec and status', async () => {
    const { screen } = await renderDetail({
      cluster: 1,
      g: '',
      v: 'v1',
      r: 'pods',
      ns: 'dev',
      name: 'pod-a',
    })
    await expect.element(screen.getByText('pod-a').first()).toBeInTheDocument()
    await expect
      .element(screen.getByRole('cell', { name: 'dev' }))
      .toBeInTheDocument()
    await expect.element(screen.getByText('app=web')).toBeInTheDocument()
    await expect
      .element(screen.getByText('Spec', { exact: true }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('Status', { exact: true }))
      .toBeInTheDocument()
    await vi.waitFor(() =>
      expect(mockResource).toHaveBeenCalledWith(
        'pod-a',
        expect.objectContaining({
          cluster_id: 1,
          resource: 'pods',
          namespace: 'dev',
        })
      )
    )
  })

  it('shows an error message when the object is missing', async () => {
    mockResource.mockRejectedValue({
      response: { data: { detail: 'pods "x" not found' } },
    })
    const { screen } = await renderDetail({
      cluster: 1,
      g: '',
      v: 'v1',
      r: 'pods',
      ns: 'dev',
      name: 'x',
    })
    await expect
      .element(screen.getByText('pods "x" not found'))
      .toBeInTheDocument()
  })

  it('reports missing params instead of firing a request', async () => {
    const { screen } = await renderDetail({ cluster: 1 })
    await expect.element(screen.getByText(/缺少必要参数/)).toBeInTheDocument()
    expect(mockResource).not.toHaveBeenCalled()
  })

  it('invokes onBack from the back button', async () => {
    const { screen, onBack } = await renderDetail({
      cluster: 1,
      g: '',
      v: 'v1',
      r: 'pods',
      name: 'pod-a',
    })
    await userEvent.click(screen.getByRole('button', { name: /返回/ }))
    expect(onBack).toHaveBeenCalledOnce()
  })
})
