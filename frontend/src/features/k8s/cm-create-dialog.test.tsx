import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { toast } from 'sonner'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import * as YAML from 'yaml'
import { k8sApi } from '@/api/client'
import { CmCreateDialog } from './cm-create-dialog'

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

interface ParsedCm {
  apiVersion: string
  kind: string
  metadata: { name: string; namespace: string }
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
      <CmCreateDialog
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

describe('CmCreateDialog', () => {
  it('submits dry-run then real create with the assembled manifest', async () => {
    const { screen, onCreated, onOpenChange } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'app-config')
    await userEvent.click(screen.getByRole('button', { name: '添加一行' }))
    await userEvent.fill(screen.getByLabelText('键 1'), 'app.yaml')
    await userEvent.fill(screen.getByLabelText('值 1'), 'listen: 8080')
    await userEvent.click(screen.getByRole('button', { name: '添加一行' }))
    await userEvent.fill(screen.getByLabelText('键 2'), 'debug')
    await userEvent.fill(screen.getByLabelText('值 2'), 'false')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const [, params1, dryRun1] = mockCreate.mock.calls[0]
    const [manifest2, params2, dryRun2] = mockCreate.mock.calls[1]
    expect(dryRun1).toBe(true)
    expect(dryRun2).toBeFalsy()
    expect(params1).toEqual(params2)
    expect(params1).toMatchObject({
      cluster_id: 1,
      resource: 'configmaps',
      namespace: 'prod',
    })
    const m = YAML.parse(manifest2 as string) as ParsedCm
    expect(m.apiVersion).toBe('v1')
    expect(m.kind).toBe('ConfigMap')
    expect(m.metadata).toMatchObject({ name: 'app-config', namespace: 'prod' })
    expect(m.data).toEqual({ 'app.yaml': 'listen: 8080', debug: 'false' })
    await vi.waitFor(() => expect(onCreated).toHaveBeenCalled())
    await vi.waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false))
    await vi.waitFor(() =>
      expect(mockToast).toHaveBeenCalledWith('配置字典 app-config 已创建')
    )
  })

  it('adds and removes data rows', async () => {
    const { screen } = await renderDialog()
    await expect
      .element(screen.getByText('暂无数据项，点击下方按钮添加'))
      .toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '添加一行' }))
    await userEvent.click(screen.getByRole('button', { name: '添加一行' }))
    await expect.element(screen.getByLabelText('键 1')).toBeInTheDocument()
    await expect.element(screen.getByLabelText('键 2')).toBeInTheDocument()
    await userEvent.fill(screen.getByLabelText('键 1'), 'one')
    await userEvent.fill(screen.getByLabelText('键 2'), 'two')
    await userEvent.click(
      screen.getByRole('button', { name: '删除此行' }).nth(0)
    )
    await expect.element(screen.getByLabelText('键 2')).not.toBeInTheDocument()
    await expect.element(screen.getByLabelText('键 1')).toHaveValue('two')
  })

  it('omits data entirely for an empty ConfigMap', async () => {
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'empty-config')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const m = YAML.parse(mockCreate.mock.calls[1][0] as string) as ParsedCm
    expect(m.data).toBeUndefined()
  })

  it('stamps immutable: true when the switch is on', async () => {
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'locked-config')
    await userEvent.click(
      screen.getByRole('switch', { name: /immutable/ })
    )
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const m = YAML.parse(mockCreate.mock.calls[1][0] as string) as ParsedCm
    expect(m.immutable).toBe(true)
    // The dry-run call carries the same manifest.
    const m0 = YAML.parse(mockCreate.mock.calls[0][0] as string) as ParsedCm
    expect(m0.immutable).toBe(true)
  })

  it('leaves immutable off the manifest by default', async () => {
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'plain-config')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const m = YAML.parse(mockCreate.mock.calls[1][0] as string) as ParsedCm
    expect(m.immutable).toBeUndefined()
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

  it('flags an invalid data key and blocks the submit', async () => {
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'app-config')
    await userEvent.click(screen.getByRole('button', { name: '添加一行' }))
    await userEvent.fill(screen.getByLabelText('键 1'), 'bad key!')
    await expect
      .element(screen.getByText('键只能包含字母、数字、-、_、.'))
      .toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await expect
      .element(screen.getByText('数据包含非法键名，请修正标红的键后重试'))
      .toBeInTheDocument()
    expect(mockCreate).not.toHaveBeenCalled()
  })

  it('shows 同名配置字典已存在 on a 409 from the real create', async () => {
    mockCreate
      .mockResolvedValueOnce({ data: {} } as never)
      .mockRejectedValueOnce({
        response: { status: 409, data: { detail: '同名资源已存在: x' } },
      } as never)
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'app-config')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await expect
      .element(screen.getByText('同名配置字典已存在'))
      .toBeInTheDocument()
    expect(mockCreate).toHaveBeenCalledTimes(2)
  })

  it('shows the dry-run error bar and skips the real create', async () => {
    mockCreate.mockRejectedValueOnce({
      response: { status: 400, data: { detail: 'invalid manifest' } },
    } as never)
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'app-config')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await expect
      .element(screen.getByText('invalid manifest'))
      .toBeInTheDocument()
    expect(mockCreate).toHaveBeenCalledTimes(1)
  })
})
