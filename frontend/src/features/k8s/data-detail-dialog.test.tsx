import {
  api,
  cmObj,
  secretObj,
  setupDataApi,
  renderDataDialog,
} from '@/test-utils/data-dialog'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { userEvent } from 'vitest/browser'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/client')>()),
  k8sApi: {
    resource: vi.fn(),
    resourceTypes: vi.fn(),
    capabilities: vi.fn(),
    update: vi.fn(),
    updateData: vi.fn(),
    remove: vi.fn(),
  },
}))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))
beforeEach(setupDataApi)

describe('Data resource detail dialog', () => {
  it('opens in vertical data tabs and switches between layouts and metadata', async () => {
    const { screen } = await renderDataDialog()
    await expect
      .element(screen.getByText('ConfigMap: prod / app-config'))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('tab', { name: 'app.yaml', exact: true }))
      .toHaveAttribute('aria-selected', 'true')
    expect(screen.getByText('hello kuboard').query()).toBeNull()
    await userEvent.click(
      screen.getByRole('tab', { name: 'banner.txt', exact: true })
    )
    await expect.element(screen.getByText('hello kuboard')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '条目模式' }))
    await expect
      .element(screen.getByText('listen: 8080\ndebug: false'))
      .toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('tab', { name: '元数据', exact: true })
    )
    await expect.element(screen.getByText('team/owner')).toBeInTheDocument()
  })

  it('edits in place and cancels back to the original values without closing', async () => {
    const { screen, onEdit, onOpenChange } = await renderDataDialog()
    await userEvent.click(
      screen.getByRole('button', { name: '编辑', exact: true })
    )
    await expect
      .element(screen.getByLabelText('值 1'))
      .toHaveValue('listen: 8080\ndebug: false')
    await userEvent.fill(screen.getByLabelText('值 1'), 'discard me')
    await userEvent.click(
      screen.getByRole('button', { name: '取消', exact: true })
    )
    await expect
      .element(screen.getByText('listen: 8080\ndebug: false'))
      .toBeInTheDocument()
    expect(screen.getByText('discard me').query()).toBeNull()
    expect(onOpenChange).not.toHaveBeenCalled()
    expect(onEdit).toHaveBeenCalledOnce()
  })

  it('shows decoded Secret values in the viewer', async () => {
    const { screen } = await renderDataDialog(secretObj())
    await expect.element(screen.getByText('原密码')).toBeInTheDocument()
    expect(document.body.textContent).not.toContain('***')
  })

  it('runs the delete confirmation with the latest resourceVersion', async () => {
    const { screen, params, onDeleted } = await renderDataDialog()
    await expect
      .element(screen.getByRole('button', { name: '删除', exact: true }))
      .toBeEnabled()
    await userEvent.click(
      screen.getByRole('button', { name: '删除', exact: true })
    )
    const confirm = screen
      .getByRole('button', { name: '删除', exact: true })
      .last()
    await expect.element(confirm).toBeDisabled()
    await userEvent.fill(screen.getByRole('textbox'), 'app-config')
    await userEvent.click(confirm)
    await vi.waitFor(() =>
      expect(api.remove).toHaveBeenCalledWith('app-config', params, '7')
    )
    expect(onDeleted).toHaveBeenCalledOnce()
  })

  it('hides write actions when RBAC denies them', async () => {
    api.capabilities.mockResolvedValue({
      data: {
        resources: {
          '/v1/configmaps': {
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
    const { screen } = await renderDataDialog()
    await expect
      .element(screen.getByRole('button', { name: '预览 / 编辑 YAML' }))
      .toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: '编辑', exact: true }).query()
    ).toBeNull()
    expect(
      screen.getByRole('button', { name: '删除', exact: true }).query()
    ).toBeNull()
  })

  it('shows empty state and only closes through an explicit close action', async () => {
    const { screen, onOpenChange } = await renderDataDialog(cmObj({ data: {} }))
    await expect
      .element(screen.getByText('该配置字典没有数据条目'))
      .toBeInTheDocument()
    await userEvent.click(
      document.querySelector('[data-slot="dialog-overlay"]')!,
      { position: { x: 1, y: 1 } }
    )
    expect(onOpenChange).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: '关闭窗口' }))
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })
})
