import {
  api,
  cmObj,
  setupDataApi,
  renderDataDialog,
} from '@/test-utils/data-dialog'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { userEvent } from 'vitest/browser'
import * as YAML from 'yaml'

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

describe('ConfigMap dialog editing', () => {
  it('edits the code pane and preserves metadata, binaryData and resourceVersion', async () => {
    const { screen, onSaved, onOpenChange } = await renderDataDialog(
      cmObj({ binaryData: { binary: 'AA==' } }),
      'edit'
    )
    await expect
      .element(screen.getByLabelText('值 1'))
      .toHaveValue('listen: 8080\ndebug: false')
    await userEvent.fill(screen.getByLabelText('值 1'), 'listen: 9090')
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await vi.waitFor(() => expect(api.update).toHaveBeenCalledTimes(2))
    expect(api.update.mock.calls[0][3]).toBe(true)
    const manifest = YAML.parse(api.update.mock.calls[1][2])
    expect(manifest).toMatchObject({
      metadata: { resourceVersion: '7', labels: { app: 'web' } },
      data: { 'app.yaml': 'listen: 9090', 'banner.txt': 'hello kuboard' },
      binaryData: { binary: 'AA==' },
    })
    expect(onSaved).toHaveBeenCalledOnce()
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it('retains the same draft across entry and tab layouts and rejects duplicate keys', async () => {
    const { screen } = await renderDataDialog(cmObj(), 'edit')
    await userEvent.fill(screen.getByLabelText('值 1'), 'draft')
    await userEvent.click(screen.getByRole('button', { name: '条目模式' }))
    await expect.element(screen.getByLabelText('值 1')).toHaveValue('draft')
    await userEvent.fill(screen.getByLabelText('键 2'), 'app.yaml')
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('已经存在同名的键')
    expect(api.update).not.toHaveBeenCalled()
  })

  it('blocks leaving an unnamed new tab and supports deleting a row', async () => {
    const { screen } = await renderDataDialog(cmObj(), 'edit')
    await userEvent.click(screen.getByRole('button', { name: '添加条目' }))
    await userEvent.click(
      screen.getByRole('tab', { name: 'app.yaml', exact: true })
    )
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('请先为新的条目设置键名')
    await userEvent.fill(screen.getByLabelText('键 3'), 'new.txt')
    await userEvent.fill(screen.getByLabelText('值 3'), '')
    await userEvent.click(screen.getByRole('button', { name: '删除此行' }))
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await vi.waitFor(() => expect(api.update).toHaveBeenCalledTimes(2))
    expect(YAML.parse(api.update.mock.calls[1][2]).data).not.toHaveProperty(
      'new.txt'
    )
  })

  it('allows editing data in the same save that sets immutable', async () => {
    const { screen } = await renderDataDialog(cmObj(), 'edit')
    await userEvent.fill(screen.getByLabelText('值 1'), 'final value')
    await userEvent.click(screen.getByRole('switch', { name: /immutable/ }))
    await expect.element(screen.getByLabelText('值 1')).toBeEnabled()
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await vi.waitFor(() => expect(api.update).toHaveBeenCalledTimes(2))
    expect(YAML.parse(api.update.mock.calls[1][2])).toMatchObject({
      immutable: true,
      data: { 'app.yaml': 'final value' },
    })
  })

  it('locks persisted immutable data but still saves metadata', async () => {
    const { screen } = await renderDataDialog(
      cmObj({ immutable: true }),
      'edit'
    )
    await expect.element(screen.getByLabelText('值 1')).toBeDisabled()
    await userEvent.click(
      screen.getByRole('tab', { name: '元数据', exact: true })
    )
    await userEvent.fill(screen.getByLabelText('标签值 1'), 'api')
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await vi.waitFor(() => expect(api.update).toHaveBeenCalledTimes(2))
    expect(YAML.parse(api.update.mock.calls[1][2]).metadata.labels).toEqual({
      app: 'api',
    })
  })

  it('preserves the draft on conflict and skips writes on dry-run failure', async () => {
    const { screen } = await renderDataDialog(cmObj(), 'edit')
    api.update.mockRejectedValueOnce({
      response: { status: 409, data: { detail: 'conflict' } },
    })
    await userEvent.fill(screen.getByLabelText('值 1'), 'draft')
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('对象已被他人修改')
    await expect.element(screen.getByLabelText('值 1')).toHaveValue('draft')
    expect(api.update).toHaveBeenCalledTimes(1)
  })
})
