import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import { platformApi, type APIKey } from '@/api/client'
import { ApiKeysPage } from './index'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    platformApi: {
      apiKeys: vi.fn(),
      createApiKey: vi.fn(),
      updateApiKey: vi.fn(),
      deleteApiKey: vi.fn(),
    },
  }
})

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const mockList = vi.mocked(platformApi.apiKeys)
const mockCreate = vi.mocked(platformApi.createApiKey)
const mockUpdate = vi.mocked(platformApi.updateApiKey)
const mockDelete = vi.mocked(platformApi.deleteApiKey)

const viewerKey: APIKey = {
  id: 1,
  name: 'ci-bot',
  prefix: 'dp_abcdef12',
  role: 'viewer',
  enabled: true,
  expires_at: null,
  last_used_at: null,
  created_at: '2026-09-30T10:00:00Z',
  updated_at: '2026-09-30T10:00:00Z',
}

const operatorKey: APIKey = {
  ...viewerKey,
  id: 2,
  name: 'deploy-bot',
  prefix: 'dp_deadbeef',
  role: 'operator',
  enabled: false,
}

function renderPage() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <ApiKeysPage />
    </QueryClientProvider>
  )
}

describe('ApiKeysPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockList.mockResolvedValue({ data: [viewerKey, operatorKey] } as never)
  })

  it('renders key rows without exposing plaintext', async () => {
    const screen = await renderPage()
    await expect.element(screen.getByText('ci-bot')).toBeVisible()
    await expect.element(screen.getByText('deploy-bot')).toBeVisible()
    await expect.element(screen.getByText('dp_abcdef12…')).toBeVisible()
    expect(document.body.textContent).not.toContain('dp_abcdef123456')
  })

  it('creates a key and reveals the plaintext exactly once', async () => {
    mockCreate.mockResolvedValue({
      data: { ...viewerKey, id: 3, name: 'new-bot', key: 'dp_secret_token_x' },
    } as never)
    const screen = await renderPage()
    await expect.element(screen.getByText('ci-bot')).toBeVisible()

    await userEvent.click(screen.getByRole('button', { name: '创建密钥' }))
    await userEvent.fill(screen.getByLabelText('名称'), 'new-bot')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))

    expect(mockCreate).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'new-bot', role: 'viewer' })
    )
    const shown = screen.getByTestId('created-key')
    await expect.element(shown).toHaveTextContent('dp_secret_token_x')
    await expect
      .element(screen.getByText(/只显示这一次/))
      .toBeVisible()
  })

  it('toggles enabled state via update', async () => {
    mockUpdate.mockResolvedValue({ data: viewerKey } as never)
    const screen = await renderPage()
    await expect.element(screen.getByText('ci-bot')).toBeVisible()

    await userEvent.click(
      screen.getByRole('switch', { name: '启用 deploy-bot' })
    )
    expect(mockUpdate).toHaveBeenCalledWith(2, { enabled: true })
  })

  it('deletes after destructive confirmation', async () => {
    mockDelete.mockResolvedValue({ data: { status: 'ok' } } as never)
    const screen = await renderPage()
    await expect.element(screen.getByText('ci-bot')).toBeVisible()

    // 第一行的「删除」按钮（ci-bot），随后确认弹窗里的「删除」是最后一个
    await userEvent.click(
      screen.getByRole('button', { name: '删除' }).first()
    )
    await userEvent.click(
      screen.getByRole('button', { name: '删除' }).last()
    )
    expect(mockDelete).toHaveBeenCalledWith(1)
  })
})
