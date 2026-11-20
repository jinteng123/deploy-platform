import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import * as YAML from 'yaml'
import { k8sApi, type K8sObject } from '@/api/client'
import { PvcCreateDialog } from './pvc-create-dialog'

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

const mockResources = vi.mocked(k8sApi.resources)
const mockCreate = vi.mocked(k8sApi.createResource)

interface ParsedPvc {
  kind: string
  metadata: { name: string; namespace: string }
  spec: {
    storageClassName?: string
    accessModes: string[]
    resources: { requests: { storage: string } }
    volumeMode: string
  }
}

const sc = (
  name: string,
  provisioner: string,
  volumeBindingMode: string,
  isDefault = false
): K8sObject => ({
  apiVersion: 'storage.k8s.io/v1',
  kind: 'StorageClass',
  metadata: {
    name,
    annotations: isDefault
      ? { 'storageclass.kubernetes.io/is-default-class': 'true' }
      : {},
  },
  spec: { provisioner, volumeBindingMode },
})

const storageClassList = {
  apiVersion: 'storage.k8s.io/v1',
  kind: 'StorageClassList',
  metadata: {},
  items: [
    sc('fast-ssd', 'rancher.io/local-path', 'WaitForFirstConsumer'),
    sc('standard', 'kubernetes.io/no-provisioner', 'Immediate', true),
  ],
}

async function renderDialog() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const onCreated = vi.fn()
  const onOpenChange = vi.fn()
  const screen = await render(
    <QueryClientProvider client={client}>
      <PvcCreateDialog
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
  mockResources.mockImplementation((params: { resource?: string }): never => {
    if (params.resource === 'storageclasses')
      return Promise.resolve({ data: storageClassList }) as never
    return Promise.resolve({ data: { items: [] } }) as never
  })
  mockCreate.mockResolvedValue({ data: {} } as never)
})

describe('PvcCreateDialog', () => {
  it('preselects the default StorageClass and shows its 默认 badge', async () => {
    const { screen } = await renderDialog()
    const trigger = screen.getByRole('combobox', { name: '存储类' })
    await expect.element(trigger).toHaveTextContent('standard')
    await userEvent.click(trigger)
    const item = screen.getByRole('option', { name: /standard/ })
    await expect.element(item).toHaveTextContent('默认')
    await expect
      .element(item)
      .toHaveTextContent('kubernetes.io/no-provisioner · Immediate')
  })

  it('submits dry-run then real create with the assembled manifest', async () => {
    const { screen, onCreated, onOpenChange } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'data-vol')
    await userEvent.click(
      screen.getByRole('radio', { name: 'ReadWriteMany' })
    )
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: '容量' }),
      '5'
    )
    await userEvent.click(screen.getByRole('radio', { name: '块设备' }))
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const [, params1, dryRun1] = mockCreate.mock.calls[0]
    const [manifest2, params2, dryRun2] = mockCreate.mock.calls[1]
    expect(dryRun1).toBe(true)
    expect(dryRun2).toBeFalsy()
    expect(params1).toEqual(params2)
    expect(params1).toMatchObject({
      cluster_id: 1,
      resource: 'persistentvolumeclaims',
      namespace: 'prod',
    })
    const m = YAML.parse(manifest2 as string) as ParsedPvc
    expect(m.kind).toBe('PersistentVolumeClaim')
    expect(m.metadata).toMatchObject({ name: 'data-vol', namespace: 'prod' })
    expect(m.spec.storageClassName).toBe('standard')
    expect(m.spec.accessModes).toEqual(['ReadWriteMany'])
    expect(m.spec.resources.requests.storage).toBe('5Gi')
    expect(m.spec.volumeMode).toBe('Block')
    await vi.waitFor(() => expect(onCreated).toHaveBeenCalled())
    await vi.waitFor(() =>
      expect(onOpenChange).toHaveBeenCalledWith(false)
    )
  })

  it('omits storageClassName when 不指定 is chosen', async () => {
    const { screen } = await renderDialog()
    await userEvent.click(
      screen.getByRole('combobox', { name: '存储类' })
    )
    await userEvent.click(
      screen.getByRole('option', { name: /不指定/ })
    )
    await userEvent.fill(screen.getByLabelText('名称'), 'plain-vol')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await vi.waitFor(() => expect(mockCreate).toHaveBeenCalledTimes(2))
    const m = YAML.parse(
      mockCreate.mock.calls[1][0] as string
    ) as ParsedPvc
    expect(m.spec.storageClassName).toBeUndefined()
  })

  it('rejects an invalid name inline and never calls createResource', async () => {
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'Bad_Name')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await expect
      .element(
        screen.getByText(
          '只能包含小写字母、数字和 -，且以字母或数字开头结尾'
        )
      )
      .toBeInTheDocument()
    expect(mockCreate).not.toHaveBeenCalled()
  })

  it('shows the dry-run error bar and skips the real create', async () => {
    mockCreate.mockRejectedValueOnce({
      response: { status: 400, data: { detail: 'spec.resources: Required' } },
    } as never)
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'data-vol')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await expect
      .element(screen.getByText('spec.resources: Required'))
      .toBeInTheDocument()
    expect(mockCreate).toHaveBeenCalledTimes(1)
  })

  it('shows 同名存储声明已存在 on a 409 from the real create', async () => {
    mockCreate
      .mockResolvedValueOnce({ data: {} } as never)
      .mockRejectedValueOnce({
        response: { status: 409, data: { detail: '同名资源已存在: x' } },
      } as never)
    const { screen } = await renderDialog()
    await userEvent.fill(screen.getByLabelText('名称'), 'data-vol')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await expect
      .element(screen.getByText('同名存储声明已存在'))
      .toBeInTheDocument()
    expect(mockCreate).toHaveBeenCalledTimes(2)
  })
})
