import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { toast } from 'sonner'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import * as YAML from 'yaml'
import { k8sApi } from '@/api/client'
import { SecretCreateDialog } from './secret-create-dialog'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    k8sApi: {
      resources: vi.fn(),
      createResource: vi.fn(),
    },
  }
})

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const mockCreate = vi.mocked(k8sApi.createResource)
const mockToast = vi.mocked(toast.success)

interface ParsedSecret {
  apiVersion: string
  kind: string
  metadata: { name: string; namespace: string }
  type?: string
  stringData?: Record<string, string>
  data?: Record<string, string>
  immutable?: boolean
}

async function renderDialog() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const onCreated = vi.fn()
  const onOpenChange = vi.fn()
  const screen = await render(
    <QueryClientProvider client={client}>
      <SecretCreateDialog
        open={true}
        onOpenChange={onOpenChange}
        clusterId={1}
        namespace='prod'
        onCreated={onCreated}
      />
    </QueryClientProvider>
  )
  return { screen, onCreated, onOpenChange }
}

beforeEach(() => {
  vi.clearAllMocks()
  mockCreate.mockResolvedValue({ data: {} } as never)
})

describe('SecretCreateDialog', () => {
  it('submits dry-run then real create with plaintext under stringData', async () => {
    const { screen, onCreated, onOpenChange } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'db-credentials')
    await userEvent.click(screen.getByRole('button', { name: '添加一行' }))
    await userEvent.fill(screen.getByLabelText('键 1'), 'username')
    await userEvent.fill(screen.getByLabelText('值 1'), 'admin')
    await userEvent.click(screen.getByRole('button', { name: '添加一行' }))
    await userEvent.fill(screen.getByLabelText('键 2'), 'password')
    await userEvent.fill(screen.getByLabelText('值 2'), 's3cret')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const [, params1, dryRun1] = mockCreate.mock.calls[0]
    const [manifest2, params2, dryRun2] = mockCreate.mock.calls[1]
    expect(dryRun1).toBe(true)
    expect(dryRun2).toBeFalsy()
    expect(params1).toEqual(params2)
    expect(params1).toMatchObject({
      cluster_id: 1,
      resource: 'secrets',
      namespace: 'prod',
    })
    const m = YAML.parse(manifest2 as string) as ParsedSecret
    expect(m.apiVersion).toBe('v1')
    expect(m.kind).toBe('Secret')
    expect(m.type).toBe('Opaque')
    expect(m.metadata).toMatchObject({
      name: 'db-credentials',
      namespace: 'prod',
    })
    // Plaintext lands in stringData — never in `data`.
    expect(m.stringData).toEqual({ username: 'admin', password: 's3cret' })
    expect(m.data).toBeUndefined()
    await vi.waitFor(() => expect(onCreated).toHaveBeenCalled())
    await vi.waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    await vi.waitFor(() =>
      expect(mockToast).toHaveBeenCalledWith('密钥 db-credentials 已创建')
    )
  })

  it('defaults to Opaque and shows the convention hint for typed secrets', async () => {
    const { screen } = await renderDialog()
    const trigger = screen.getByRole('combobox', { name: '类型' })
    await expect.element(trigger).toHaveTextContent('Opaque')
    // Opaque carries no convention-keys hint.
    await expect.element(screen.getByText(/约定键/)).not.toBeInTheDocument()
    await userEvent.click(trigger)
    await userEvent.click(
      screen.getByRole('option', { name: /kubernetes\.io\/tls/ })
    )
    await expect
      .element(screen.getByText('约定键：tls.crt（证书）、tls.key（私钥）'))
      .toBeInTheDocument()
    // The manifest picks the chosen type up.
    await userEvent.fill(screen.getByLabelText('名称'), 'tls-cert')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const m = YAML.parse(mockCreate.mock.calls[1][0] as string) as ParsedSecret
    expect(m.type).toBe('kubernetes.io/tls')
    expect(m.stringData).toBeUndefined()
  })

  it('adds and removes data rows', async () => {
    const { screen } = await renderDialog()
    await userEvent.click(screen.getByRole('button', { name: '添加一行' }))
    await userEvent.fill(screen.getByLabelText('键 1'), 'token')
    await userEvent.fill(screen.getByLabelText('值 1'), 'abc')
    await userEvent.click(screen.getByRole('button', { name: '删除此行' }))
    await expect
      .element(screen.getByText('暂无数据项，点击下方按钮添加'))
      .toBeInTheDocument()
    await expect.element(screen.getByLabelText('键 1')).not.toBeInTheDocument()
  })

  it('rejects an invalid name inline and never calls createResource', async () => {
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'Bad_Name')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await expect
      .element(screen.getByText('小写字母、数字、- 或 . 组成，≤253 字符'))
      .toBeInTheDocument()
    expect(mockCreate).not.toHaveBeenCalled()
  })

  it('blocks submit while a typed data key is invalid', async () => {
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'ok-name')
    await userEvent.click(screen.getByRole('button', { name: '添加一行' }))
    await userEvent.fill(screen.getByLabelText('值 1'), 'orphan-value')
    // A value typed against an empty key is invalid.
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await expect
      .element(screen.getByText('数据包含非法键名，请修正标红的键后重试'))
      .toBeInTheDocument()
    expect(mockCreate).not.toHaveBeenCalled()
  })

  it('stamps immutable: true on the manifest when the switch is on', async () => {
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'locked-secret')
    await userEvent.click(screen.getByRole('switch', { name: /immutable/ }))
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const m = YAML.parse(mockCreate.mock.calls[1][0] as string) as ParsedSecret
    expect(m.immutable).toBe(true)
    expect(m.kind).toBe('Secret')
  })

  it('leaves immutable off the manifest by default', async () => {
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'plain-secret')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const m = YAML.parse(mockCreate.mock.calls[1][0] as string) as ParsedSecret
    expect(m.immutable).toBeUndefined()
  })

  it('shows 同名密钥已存在 on a 409 from the real create', async () => {
    mockCreate
      .mockResolvedValueOnce({ data: {} } as never)
      .mockRejectedValueOnce({
        response: { status: 409, data: { detail: '同名资源已存在: x' } },
      } as never)
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'db-credentials')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await expect.element(screen.getByText('同名密钥已存在')).toBeInTheDocument()
    expect(mockCreate).toHaveBeenCalledTimes(2)
  })
})
