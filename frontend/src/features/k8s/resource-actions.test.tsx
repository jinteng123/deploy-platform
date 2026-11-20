import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { userEvent } from 'vitest/browser'
import {
  k8sApi,
  type K8sObject,
  type K8sObjectParams,
  type K8sResourceType,
} from '@/api/client'
import { DirectionProvider } from '@/context/direction-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { ResourceActions } from './resource-actions'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    k8sApi: {
      resourceTypes: vi.fn(),
      capabilities: vi.fn(),
      scale: vi.fn(),
      setImage: vi.fn(),
      restart: vi.fn(),
      remove: vi.fn(),
    },
  }
})

const api = vi.mocked(k8sApi)

// RBAC verdict granting everything — the default for action tests.
const fullCaps = {
  resources: {
    'apps/v1/deployments': { list: true, update: true, delete: true },
    '/v1/pods': { list: true, update: true, delete: true },
    '/v1/nodes': { list: true, update: true, delete: true },
  },
  exec: true,
  logs: true,
}

const dep: K8sObject = {
  apiVersion: 'apps/v1',
  kind: 'Deployment',
  metadata: { name: 'web', namespace: 'prod', resourceVersion: '77' },
  spec: {
    replicas: 2,
    template: { spec: { containers: [{ name: 'app', image: 'nginx:1.25' }] } },
  },
}

const params: K8sObjectParams = {
  cluster_id: 1,
  group: 'apps',
  version: 'v1',
  resource: 'deployments',
  namespace: 'prod',
}

const depDef: K8sResourceType = {
  group: 'apps',
  version: 'v1',
  resource: 'deployments',
  kind: 'Deployment',
  namespaced: true,
  scalable: true,
  imageable: true,
  restartable: true,
  deletable: true,
}

async function renderActions(
  obj: K8sObject = dep,
  p: K8sObjectParams = params
) {
  const onDeleted = vi.fn()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DirectionProvider>
        <ThemeProvider>
          <ResourceActions
            params={p}
            name={obj.metadata?.name ?? 'web'}
            obj={obj}
            onDeleted={onDeleted}
          />
        </ThemeProvider>
      </DirectionProvider>
    </QueryClientProvider>
  )
  return { screen, onDeleted }
}

describe('ResourceActions', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.resourceTypes.mockResolvedValue({ data: [depDef] } as never)
    api.capabilities.mockResolvedValue({ data: fullCaps } as never)
    api.scale.mockResolvedValue({ data: dep } as never)
    api.setImage.mockResolvedValue({ data: dep } as never)
    api.restart.mockResolvedValue({ data: dep } as never)
    api.remove.mockResolvedValue({ data: { detail: 'ok' } } as never)
  })

  it('renders the buttons the capability flags allow', async () => {
    const { screen } = await renderActions()
    await expect
      .element(screen.getByRole('button', { name: '调整副本' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '更换镜像' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '滚动重启' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '删除' }))
      .toBeInTheDocument()
  })

  it('hides gated actions — pods only get delete', async () => {
    api.resourceTypes.mockResolvedValue({
      data: [
        {
          group: '',
          version: 'v1',
          resource: 'pods',
          kind: 'Pod',
          namespaced: true,
          deletable: true,
        },
      ],
    } as never)
    const { screen } = await renderActions(
      {
        apiVersion: 'v1',
        kind: 'Pod',
        metadata: { name: 'p1', resourceVersion: '1' },
      },
      { ...params, group: '', resource: 'pods' }
    )
    await expect
      .element(screen.getByRole('button', { name: '删除' }))
      .toBeInTheDocument()
    expect(screen.getByRole('button', { name: '调整副本' }).query()).toBeNull()
    expect(screen.getByRole('button', { name: '更换镜像' }).query()).toBeNull()
    expect(screen.getByRole('button', { name: '滚动重启' }).query()).toBeNull()
  })

  it('shows 编辑数据 for data_editable kinds and fires the callback', async () => {
    api.resourceTypes.mockResolvedValue({
      data: [
        {
          group: '',
          version: 'v1',
          resource: 'secrets',
          kind: 'Secret',
          namespaced: true,
          deletable: true,
          data_editable: true,
        },
      ],
    } as never)
    api.capabilities.mockResolvedValue({
      data: {
        resources: {
          '/v1/secrets': { list: true, update: true, delete: true },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const onEditData = vi.fn()
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const screen = await render(
      <QueryClientProvider client={client}>
        <DirectionProvider>
          <ThemeProvider>
            <ResourceActions
              params={{ ...params, group: '', resource: 'secrets' }}
              name='db-credentials'
              obj={{
                apiVersion: 'v1',
                kind: 'Secret',
                metadata: { name: 'db-credentials', resourceVersion: '1' },
              }}
              onDeleted={vi.fn()}
              onEditData={onEditData}
            />
          </ThemeProvider>
        </DirectionProvider>
      </QueryClientProvider>
    )
    const btn = screen.getByRole('button', { name: '编辑数据' })
    await expect.element(btn).toBeInTheDocument()
    await userEvent.click(btn)
    expect(onEditData).toHaveBeenCalled()
  })

  it('keeps 编辑数据 visible when update is allowed but delete is not', async () => {
    api.resourceTypes.mockResolvedValue({
      data: [
        {
          group: '',
          version: 'v1',
          resource: 'secrets',
          kind: 'Secret',
          namespaced: true,
          deletable: true,
          data_editable: true,
        },
      ],
    } as never)
    api.capabilities.mockResolvedValue({
      data: {
        resources: {
          '/v1/secrets': { list: true, update: true, delete: false },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const screen = await render(
      <QueryClientProvider client={client}>
        <DirectionProvider>
          <ThemeProvider>
            <ResourceActions
              params={{ ...params, group: '', resource: 'secrets' }}
              name='db-credentials'
              obj={{
                apiVersion: 'v1',
                kind: 'Secret',
                metadata: { name: 'db-credentials', resourceVersion: '1' },
              }}
              onDeleted={vi.fn()}
              onEditData={vi.fn()}
            />
          </ThemeProvider>
        </DirectionProvider>
      </QueryClientProvider>
    )
    await expect
      .element(screen.getByRole('button', { name: '编辑数据' }))
      .toBeInTheDocument()
    expect(screen.getByRole('button', { name: '删除' }).query()).toBeNull()
  })

  it('renders nothing for a read-only kind (nodes)', async () => {
    api.resourceTypes.mockResolvedValue({
      data: [
        {
          group: '',
          version: 'v1',
          resource: 'nodes',
          kind: 'Node',
          namespaced: false,
        },
      ],
    } as never)
    const { screen } = await renderActions(dep, {
      ...params,
      group: '',
      resource: 'nodes',
    })
    await vi.waitFor(() => expect(api.resourceTypes).toHaveBeenCalled())
    expect(screen.getByRole('button', { name: '删除' }).query()).toBeNull()
  })

  it('scales with the live resourceVersion', async () => {
    const { screen } = await renderActions()
    await userEvent.click(screen.getByRole('button', { name: '调整副本' }))
    const input = screen.getByLabelText('目标副本数')
    await expect.element(input).toHaveValue(2)
    await userEvent.fill(input, '5')
    await userEvent.click(screen.getByRole('button', { name: '确定' }))
    await vi.waitFor(() =>
      expect(api.scale).toHaveBeenCalledWith('web', params, {
        replicas: 5,
        resource_version: '77',
      })
    )
  })

  it('rejects out-of-range replicas without calling the API', async () => {
    const { screen } = await renderActions()
    await userEvent.click(screen.getByRole('button', { name: '调整副本' }))
    const input = screen.getByLabelText('目标副本数')
    await userEvent.fill(input, '99')
    await expect
      .element(screen.getByRole('button', { name: '确定' }))
      .toBeDisabled()
    expect(api.scale).not.toHaveBeenCalled()
  })

  it('sends image updates for the edited rows', async () => {
    const { screen } = await renderActions()
    await userEvent.click(screen.getByRole('button', { name: '更换镜像' }))
    await userEvent.fill(screen.getByLabelText('新版本 app'), '1.27')
    await userEvent.click(
      screen.getByRole('button', { name: '确定（更新 1 个）' })
    )
    await vi.waitFor(() =>
      expect(api.setImage).toHaveBeenCalledWith('web', params, {
        container: 'app',
        image: 'nginx:1.27',
        resource_version: '77',
      })
    )
  })

  it('keeps the image dialog open and shows the conflict hint on 409', async () => {
    api.setImage.mockRejectedValue({
      response: {
        status: 409,
        data: { detail: 'the object has been modified' },
      },
    })
    const { screen } = await renderActions()
    await userEvent.click(screen.getByRole('button', { name: '更换镜像' }))
    await userEvent.fill(screen.getByLabelText('新版本 app'), '1.27')
    await userEvent.click(
      screen.getByRole('button', { name: '确定（更新 1 个）' })
    )
    await expect
      .element(screen.getByText(/the object has been modified/))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText(/对象已被他人修改/))
      .toBeInTheDocument()
    await expect.element(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('confirms rollout restart with the resourceVersion', async () => {
    const { screen } = await renderActions()
    await userEvent.click(screen.getByRole('button', { name: '滚动重启' }))
    await userEvent.click(screen.getByRole('button', { name: '重启' }))
    await vi.waitFor(() =>
      expect(api.restart).toHaveBeenCalledWith('web', params, {
        resource_version: '77',
      })
    )
  })

  it('requires typing the name before delete, then calls remove', async () => {
    const { screen, onDeleted } = await renderActions()
    await userEvent.click(screen.getByRole('button', { name: '删除' }))
    const confirm = screen
      .getByRole('button', { name: '删除', exact: true })
      .last()
    await expect.element(confirm).toBeDisabled()
    await userEvent.fill(screen.getByRole('textbox'), 'web')
    await userEvent.click(confirm)
    await vi.waitFor(() =>
      expect(api.remove).toHaveBeenCalledWith('web', params, '77')
    )
    await vi.waitFor(() => expect(onDeleted).toHaveBeenCalledOnce())
  })

  it('hides every write control for a read-only RBAC verdict', async () => {
    // The allowlist still says deployments are writable — but THIS user's
    // SelfSubjectRulesReview denies update+delete, so nothing renders.
    api.capabilities.mockResolvedValue({
      data: {
        resources: {
          'apps/v1/deployments': { list: true, update: false, delete: false },
        },
        exec: false,
        logs: true,
      },
    } as never)
    const { screen } = await renderActions()
    await vi.waitFor(() => expect(api.capabilities).toHaveBeenCalled())
    await vi.waitFor(() =>
      expect(
        screen.getByRole('button', { name: '调整副本' }).query()
      ).toBeNull()
    )
    expect(screen.getByRole('button', { name: '更换镜像' }).query()).toBeNull()
    expect(screen.getByRole('button', { name: '滚动重启' }).query()).toBeNull()
    expect(screen.getByRole('button', { name: '删除' }).query()).toBeNull()
  })

  it('hides write controls while the verdict is pending or errored', async () => {
    api.capabilities.mockRejectedValue(new Error('forbidden'))
    const { screen } = await renderActions()
    await vi.waitFor(() => expect(api.capabilities).toHaveBeenCalled())
    await vi.waitFor(() =>
      expect(
        screen.getByRole('button', { name: '调整副本' }).query()
      ).toBeNull()
    )
  })
})
