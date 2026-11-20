import {
  api,
  secretObj,
  setupDataApi,
  renderDataDialog,
} from '@/test-utils/data-dialog'
import { toast } from 'sonner'
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

describe('Secret dialog editing', () => {
  it('loads and decodes original values, including Unicode, from an opt-in detail read', async () => {
    const { screen, params } = await renderDataDialog(secretObj(), 'edit')
    await expect.element(screen.getByLabelText('值 1')).toHaveValue('原密码')
    expect(api.resource).toHaveBeenCalledWith('db-credentials', params, true)
    expect(document.body.textContent).not.toContain('***')
  })

  it('submits only changed values, including empty strings, renamed keys and deletions', async () => {
    const { screen, onSaved } = await renderDataDialog(secretObj(), 'edit')
    await userEvent.fill(screen.getByLabelText('值 1'), '')
    await userEvent.click(
      screen.getByRole('tab', { name: 'username', exact: true })
    )
    await userEvent.fill(screen.getByLabelText('键 2'), 'account')
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await vi.waitFor(() => expect(api.updateData).toHaveBeenCalledTimes(2))
    expect(api.updateData.mock.calls[0][3]).toBe(true)
    expect(api.updateData.mock.calls[0][2]).toEqual(
      api.updateData.mock.calls[1][2]
    )
    expect(api.updateData.mock.calls[1][2]).toMatchObject({
      resource_version: '9',
      set: { password: '', account: 'admin' },
      delete_keys: ['username'],
    })
    expect(onSaved).toHaveBeenCalledOnce()
    expect(toast.success).toHaveBeenCalledWith('密钥 db-credentials 已更新')
  })

  it('keeps data out of metadata-only updates and supports immutable', async () => {
    const { screen } = await renderDataDialog(secretObj(), 'edit')
    await userEvent.click(screen.getByRole('switch', { name: /immutable/ }))
    await userEvent.click(
      screen.getByRole('tab', { name: '元数据', exact: true })
    )
    await userEvent.fill(screen.getByLabelText('标签值 1'), 'api')
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await vi.waitFor(() => expect(api.updateData).toHaveBeenCalledTimes(2))
    expect(api.updateData.mock.calls[1][2]).toEqual({
      resource_version: '9',
      immutable: true,
      metadata: {
        labels: { app: 'api' },
        annotations: { 'team/owner': 'infra' },
      },
    })
  })

  it('provides TLS certificate fields and shares edits with the raw layout', async () => {
    const { screen } = await renderDataDialog(
      secretObj(
        { 'tls.crt': 'CERT', 'tls.key': 'PRIVATE' },
        { type: 'kubernetes.io/tls' }
      ),
      'edit'
    )
    await expect
      .element(screen.getByLabelText('tls.crt', { exact: true }))
      .toHaveValue('CERT')
    await userEvent.fill(
      screen.getByLabelText('tls.key', { exact: true }),
      'NEW PRIVATE'
    )
    await userEvent.click(
      screen.getByRole('button', { name: '原始数据', exact: true })
    )
    await userEvent.click(
      screen.getByRole('tab', { name: 'tls.key', exact: true })
    )
    await expect
      .element(screen.getByLabelText('值 2'))
      .toHaveValue('NEW PRIVATE')
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await vi.waitFor(() => expect(api.updateData).toHaveBeenCalledTimes(2))
    expect(api.updateData.mock.calls[1][2].set).toEqual({
      'tls.key': 'NEW PRIVATE',
    })
  })

  it('edits registry credentials and preserves other auths and config fields', async () => {
    const config = {
      auths: {
        'https://registry.example': {
          username: 'alice',
          password: 'old',
          extra: 'keep',
        },
        other: { auth: 'eDp5' },
      },
      credsStore: 'desktop',
    }
    const { screen } = await renderDataDialog(
      secretObj(
        { '.dockerconfigjson': JSON.stringify(config) },
        { type: 'kubernetes.io/dockerconfigjson' }
      ),
      'edit'
    )
    await expect.element(screen.getByLabelText('用户名 1')).toHaveValue('alice')
    await userEvent.fill(screen.getByLabelText('密码 1'), 'new')
    await userEvent.click(
      screen.getByRole('button', { name: '保存', exact: true })
    )
    await vi.waitFor(() => expect(api.updateData).toHaveBeenCalledTimes(2))
    const next = JSON.parse(
      api.updateData.mock.calls[1][2].set!['.dockerconfigjson']
    )
    expect(next.credsStore).toBe('desktop')
    expect(next.auths.other).toEqual(config.auths.other)
    expect(next.auths['https://registry.example']).toMatchObject({
      username: 'alice',
      password: 'new',
      auth: btoa('alice:new'),
      extra: 'keep',
    })
  })

  it('does not enable saves or show masked values when the original-value read fails', async () => {
    const { screen } = await renderDataDialog(secretObj(), 'edit')
    // Trigger a failed reload while entering edit after cancelling.
    await userEvent.click(
      screen.getByRole('button', { name: '取消', exact: true })
    )
    await expect
      .element(screen.getByRole('button', { name: '编辑', exact: true }))
      .toBeEnabled()
    api.resource.mockRejectedValueOnce(new Error('permission denied'))
    await userEvent.click(
      screen.getByRole('button', { name: '编辑', exact: true })
    )
    await expect
      .element(screen.getByRole('alert'))
      .toHaveTextContent('permission denied')
    expect(
      screen.getByRole('button', { name: '保存', exact: true }).query()
    ).toBeNull()
    expect(api.updateData).not.toHaveBeenCalled()
  })
})
