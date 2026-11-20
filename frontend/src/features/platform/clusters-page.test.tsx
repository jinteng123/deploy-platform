import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { platformApi, type Cluster } from '@/api/client'
import { ClusterFormDialog, ClustersPage } from './clusters-page'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    platformApi: {
      clusters: vi.fn(),
      createCluster: vi.fn(),
      updateCluster: vi.fn(),
      deleteCluster: vi.fn(),
      testClusterConnection: vi.fn(),
    },
  }
})

const mockCreate = vi.mocked(platformApi.createCluster)
const mockUpdate = vi.mocked(platformApi.updateCluster)

const baseCluster: Cluster = {
  id: 7,
  name: 'prod-east',
  env_code: 'prod-east',
  environment: 'prod-east',
  environment_tier: 'production',
  deploy_policy: 'automatic',
  connection_type: 'kubeconfig',
  credential_configured: true,
  insecure_skip_tls_verify: false,
  enabled: true,
  default_namespace: 'default',
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
}

async function renderDialog(editing: Cluster | null = null) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const onClose = vi.fn()
  const onSaved = vi.fn()
  const screen = await render(
    <QueryClientProvider client={client}>
      <ClusterFormDialog
        editing={editing}
        open={true}
        onClose={onClose}
        onSaved={onSaved}
      />
    </QueryClientProvider>
  )
  return { screen, onClose, onSaved }
}

beforeEach(() => {
  vi.clearAllMocks()
  mockCreate.mockResolvedValue({ data: baseCluster } as never)
  mockUpdate.mockResolvedValue({ data: baseCluster } as never)
})

describe('ClusterFormDialog', () => {
  it('selecting 生产 marks it checked, forces 手动, and disables 自动', async () => {
    const { screen } = await renderDialog()
    const prod = screen.getByRole('radio', { name: /^生产$/ })
    const auto = screen.getByRole('radio', { name: /^自动$/ })
    const manual = screen.getByRole('radio', { name: /^手动$/ })
    await expect.element(prod).toHaveAttribute('aria-checked', 'false')
    await expect.element(manual).toHaveAttribute('aria-checked', 'true')
    await userEvent.click(prod)
    await expect.element(prod).toHaveAttribute('aria-checked', 'true')
    await expect.element(manual).toHaveAttribute('aria-checked', 'true')
    await expect.element(auto).toBeDisabled()
    await expect
      .element(screen.getByText('生产集群仅支持手动发布'))
      .toBeInTheDocument()
  })

  it('shows API Server fields only for the token connection card', async () => {
    const { screen } = await renderDialog()
    await expect
      .element(screen.getByPlaceholder('粘贴 kubeconfig YAML'))
      .toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('radio', { name: 'API Server Token' })
    )
    await expect
      .element(screen.getByPlaceholder('https://k8s.example.com:6443'))
      .toBeInTheDocument()
    await expect
      .element(screen.getByPlaceholder('粘贴 kubeconfig YAML'))
      .not.toBeInTheDocument()
  })

  it('keeps the kubeconfig editor inside the dialog for long certificate lines', async () => {
    const { screen } = await renderDialog(baseCluster)
    await userEvent.fill(
      screen.getByPlaceholder('粘贴 kubeconfig YAML'),
      `apiVersion: v1\ncertificate-authority-data: ${'A'.repeat(4096)}`
    )

    const dialog = document.querySelector<HTMLElement>(
      '[data-slot="dialog-content"]'
    )!
    const editor = document.querySelector<HTMLTextAreaElement>(
      'textarea[aria-label="KubeConfig"]'
    )!
    expect(getComputedStyle(editor).fieldSizing).toBe('fixed')
    expect(editor.getBoundingClientRect().height).toBeLessThanOrEqual(320)
    expect(editor.getBoundingClientRect().width).toBeLessThan(
      dialog.getBoundingClientRect().width
    )
    expect(dialog.scrollWidth).toBeLessThanOrEqual(dialog.clientWidth + 1)
  })

  it('imports a kubeconfig file into the existing cluster form', async () => {
    const { screen } = await renderDialog({
      ...baseCluster,
      deploy_policy: 'manual',
    })
    const kubeconfig = 'apiVersion: v1\nkind: Config\ncurrent-context: local\n'
    await screen
      .getByLabelText('选择 kubeconfig 文件')
      .upload(new File([kubeconfig], 'config', { type: 'text/plain' }))

    await expect
      .element(screen.getByRole('textbox', { name: 'KubeConfig' }))
      .toHaveValue(kubeconfig)
    await expect.element(screen.getByText('已导入：config')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '保存' }))
    await vi.waitFor(() => {
      expect(mockUpdate).toHaveBeenCalledWith(
        7,
        expect.objectContaining({
          credential: kubeconfig,
        })
      )
    })
  })

  it('warns and disables save when editing a production+automatic record', async () => {
    const { screen } = await renderDialog(baseCluster)
    await expect
      .element(screen.getByText('生产集群必须使用手动发布策略'))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '保存' }))
      .toBeDisabled()
  })

  it('submits createCluster with the selected tier, policy and connection', async () => {
    const { screen, onSaved } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'staging')
    await userEvent.fill(screen.getByPlaceholder('如 test / prod-east'), 'stg')
    await userEvent.fill(
      screen.getByPlaceholder('如 staging / prod-east'),
      'staging'
    )
    await userEvent.click(screen.getByRole('radio', { name: /^生产$/ }))
    await userEvent.click(
      screen.getByRole('radio', { name: 'API Server Token' })
    )
    await userEvent.fill(
      screen.getByPlaceholder('https://k8s.example.com:6443'),
      'https://k8s.internal:6443'
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: /^Token$/ }),
      'token-abc'
    )
    await userEvent.click(screen.getByRole('button', { name: '保存' }))
    await vi.waitFor(() => {
      expect(mockCreate).toHaveBeenCalledTimes(1)
    })
    const payload = mockCreate.mock.calls[0][0]
    expect(payload.environment_tier).toBe('production')
    expect(payload.deploy_policy).toBe('manual')
    expect(payload.connection_type).toBe('token')
    expect(payload.api_server).toBe('https://k8s.internal:6443')
    expect(payload.credential).toBe('token-abc')
    await vi.waitFor(() => {
      expect(onSaved).toHaveBeenCalled()
    })
  })
})

it('uses the shared theme surfaces for the cluster list in light and dark mode', async () => {
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [baseCluster],
  } as never)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <ClustersPage />
    </QueryClientProvider>
  )
  const clusterName = screen.getByText('prod-east').first()
  await expect.element(clusterName).toBeInTheDocument()

  const tile = clusterName.element().closest('.group')
  expect(tile).not.toBeNull()
  expect(tile).toHaveClass('bg-card', 'border-border')

  const lightSurface = getComputedStyle(tile!).backgroundColor
  document.documentElement.classList.add('dark')
  try {
    const darkSurface = getComputedStyle(tile!).backgroundColor
    expect(darkSurface).not.toBe(lightSurface)
  } finally {
    document.documentElement.classList.remove('dark')
  }
})
