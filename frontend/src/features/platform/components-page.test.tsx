import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import { platformApi, runnerApi } from '@/api/client'
import { ComponentsPage } from './components-page'

const { navigateMock } = vi.hoisted(() => ({ navigateMock: vi.fn() }))

vi.mock('@tanstack/react-router', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@tanstack/react-router')>()),
  useNavigate: () => navigateMock,
}))
vi.mock('@/stores/auth-store', () => ({
  useAuthStore: (select: (state: unknown) => unknown) =>
    select({ auth: { user: { role: 'admin' } } }),
}))
vi.mock('@/components/layout/header', () => ({
  Header: ({ children }: { children: ReactNode }) => (
    <header>{children}</header>
  ),
}))
vi.mock('@/components/layout/main', () => ({
  Main: ({ children }: { children: ReactNode }) => <main>{children}</main>,
}))
vi.mock('@/components/theme-switch', () => ({ ThemeSwitch: () => null }))
vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/client')>()),
  platformApi: {
    apps: vi.fn(),
    clusters: vi.fn(),
    deliveryModel: vi.fn(),
  },
  runnerApi: {
    bindings: vi.fn(),
  },
}))

afterEach(async () => {
  await page.viewport(1280, 800)
})

beforeEach(() => {
  vi.clearAllMocks()
})

it('points a first-time administrator to cluster setup before component onboarding', async () => {
  vi.mocked(platformApi.apps).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({ data: [] } as never)
  vi.mocked(runnerApi.bindings).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.deliveryModel).mockResolvedValue({
    data: { applications: [], components: [], environments: [], bindings: [] },
  } as never)

  await page.viewport(390, 844)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ComponentsPage />
    </QueryClientProvider>
  )

  await expect.element(page.getByText('先接入可用集群')).toBeVisible()
  await expect
    .element(page.getByText('从构建到部署目标'))
    .not.toBeInTheDocument()
  await expect
    .element(page.getByRole('button', { name: '接入组件' }))
    .toBeDisabled()
  await expect.element(page.getByText('先启用集群，再接入组件')).toBeVisible()
  if (import.meta.env.VITE_CAPTURE_VISUAL_CHECKS === '1') {
    await page.screenshot({
      path: '../../../../docs/visual-checks/components-no-cluster-page-390-light.png',
      element: document.body,
    })
    await page.screenshot({
      path: '../../../../docs/visual-checks/components-no-cluster-390-light.png',
      element: page
        .getByText('先接入可用集群')
        .element()
        .closest<HTMLElement>('[data-slot="card"]')!,
    })
  }
  await userEvent.click(page.getByRole('button', { name: '前往集群设置' }))
  expect(navigateMock).toHaveBeenCalledWith({ to: '/settings/clusters' })
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
})

it('keeps the complete mapping guide for the first component on an enabled cluster', async () => {
  vi.mocked(platformApi.apps).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [{ id: 3, name: '测试集群', enabled: true }],
  } as never)
  vi.mocked(runnerApi.bindings).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.deliveryModel).mockResolvedValue({
    data: { applications: [], components: [], environments: [], bindings: [] },
  } as never)

  await page.viewport(390, 844)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ComponentsPage />
    </QueryClientProvider>
  )

  await expect
    .element(page.getByRole('list', { name: '组件接入步骤' }))
    .toBeVisible()
  await expect
    .element(page.getByText('TARGET_CLUSTER=order-test · APP_NAME=order-api'))
    .toBeVisible()
  await expect.element(page.getByText('还没有组件绑定')).toBeVisible()
  await expect
    .element(page.getByRole('button', { name: '接入组件' }))
    .toBeEnabled()
  await expect
    .element(page.getByRole('button', { name: '查看接入步骤' }))
    .not.toBeInTheDocument()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
})

it('shows the component mapping flow and uses readable cards on narrow screens', async () => {
  vi.mocked(platformApi.apps).mockResolvedValue({
    data: [
      {
        id: 11,
        cluster_id: 3,
        app_name: 'orders-api',
        source_kind: 'repository',
        source_repo_url: 'https://git.example.com/orders.git',
        source_path: 'services/api',
        namespace: 'orders-test',
        deployment_name: 'orders-api',
        container_name: 'api',
        image_repository: 'registry.example.com/orders/api',
        harbor_project: 'orders',
        match_keyword: 'orders-api',
        created_at: '',
      },
    ],
  } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [
      {
        id: 3,
        name: '测试集群',
        enabled: true,
        environment_tier: 'non_production',
        deploy_policy: 'automatic',
      },
    ],
  } as never)
  vi.mocked(runnerApi.bindings).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.deliveryModel).mockResolvedValue({
    data: {
      applications: [{ id: 1, name: '订单系统' }],
      components: [{ id: 2, application_id: 1, name: 'orders-api' }],
      environments: [
        {
          id: 4,
          name: '测试',
          environment_tier: 'non_production',
          requires_review: false,
        },
      ],
      bindings: [
        {
          id: 9,
          app_id: 11,
          application_id: 1,
          component_id: 2,
          environment_id: 4,
          requires_review: false,
        },
      ],
    },
  } as never)

  await page.viewport(390, 844)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ComponentsPage />
    </QueryClientProvider>
  )

  await expect.element(page.getByText('从构建到部署目标')).toBeVisible()
  await expect
    .element(page.getByText(/构建事件先用 TARGET_CLUSTER 和 APP_NAME 定位组件/))
    .toBeVisible()
  await expect
    .element(page.getByText('TARGET_CLUSTER=order-test · APP_NAME=order-api'))
    .not.toBeInTheDocument()
  await expect
    .element(page.getByRole('list', { name: '组件接入步骤' }))
    .not.toBeInTheDocument()
  if (import.meta.env.VITE_CAPTURE_VISUAL_CHECKS === '1') {
    await page.screenshot({
      path: '../../../../docs/visual-checks/components-mapping-390-light.png',
      element: document.body,
    })
    document.documentElement.classList.add('dark')
    await new Promise((resolve) => setTimeout(resolve, 350))
    await page.screenshot({
      path: '../../../../docs/visual-checks/components-mapping-390-dark.png',
      element: document.body,
    })
    document.documentElement.classList.remove('dark')
  }
  const guideButton = page.getByRole('button', { name: '查看接入步骤' })
  await expect.element(guideButton).toHaveAttribute('aria-expanded', 'false')
  await userEvent.click(guideButton)
  await expect
    .element(page.getByRole('list', { name: '组件接入步骤' }))
    .toBeVisible()
  await expect
    .element(page.getByText('TARGET_CLUSTER=order-test · APP_NAME=order-api'))
    .toBeVisible()
  await userEvent.click(page.getByRole('button', { name: '收起接入步骤' }))
  await expect
    .element(page.getByRole('list', { name: '组件接入步骤' }))
    .not.toBeInTheDocument()
  await page.viewport(1024, 768)
  await expect.element(page.getByRole('article')).toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
  if (import.meta.env.VITE_CAPTURE_VISUAL_CHECKS === '1') {
    document.documentElement.classList.add('dark')
    await new Promise((resolve) => setTimeout(resolve, 350))
    await page.screenshot({
      path: '../../../../docs/visual-checks/components-mapping-1024-dark.png',
      element: document.body,
    })
    document.documentElement.classList.remove('dark')
  }
  await expect.element(page.getByRole('article')).toBeVisible()
  await expect
    .element(
      page.getByRole('article').getByText('registry.example.com/orders/api')
    )
    .toBeVisible()
  await expect
    .element(page.getByRole('article').getByText('订单系统 / orders-api'))
    .toBeVisible()
  await expect
    .element(
      page.getByRole('article').getByRole('button', { name: '增加部署目标' })
    )
    .toBeVisible()
  await page.viewport(1440, 1000)
  await expect.element(page.getByRole('table')).toBeVisible()
  if (import.meta.env.VITE_CAPTURE_VISUAL_CHECKS === '1') {
    await page.screenshot({
      path: '../../../../docs/visual-checks/components-mapping-1440-light.png',
      element: document.body,
    })
  }
})

it('guides an unassociated component to its business application and environment mapping', async () => {
  vi.mocked(platformApi.apps).mockResolvedValue({
    data: [
      {
        id: 11,
        cluster_id: 3,
        app_name: 'orders-api',
        source_kind: 'repository',
        source_repo_url: 'https://git.example.com/orders.git',
        source_path: 'services/api',
        namespace: 'orders-test',
        deployment_name: 'orders-api',
        container_name: 'api',
        image_repository: 'registry.example.com/orders/api',
        harbor_project: 'orders',
        match_keyword: 'orders-api',
        created_at: '',
      },
    ],
  } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [
      {
        id: 3,
        name: '测试集群',
        enabled: true,
        environment_tier: 'non_production',
        deploy_policy: 'automatic',
      },
    ],
  } as never)
  vi.mocked(runnerApi.bindings).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.deliveryModel).mockResolvedValue({
    data: {
      applications: [],
      components: [],
      environments: [],
      bindings: [],
    },
  } as never)

  await page.viewport(390, 844)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ComponentsPage />
    </QueryClientProvider>
  )

  const component = page.getByRole('article')
  await expect
    .element(component.getByText('待关联业务应用与环境'))
    .toBeVisible()
  await userEvent.click(
    component.getByRole('button', { name: '关联业务应用与环境' })
  )
  expect(navigateMock).toHaveBeenCalledWith({
    to: '/environments',
    search: { app_id: 11 },
  })
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)

  await page.viewport(1440, 1000)
  await expect.element(page.getByRole('table')).toBeVisible()
  await expect
    .element(page.getByRole('button', { name: '关联业务应用与环境' }))
    .toBeVisible()
})

it('sends a legacy reviewed binding to relationship review before adding targets', async () => {
  vi.mocked(platformApi.apps).mockResolvedValue({
    data: [
      {
        id: 15,
        cluster_id: 1,
        app_name: 'local-provisioner',
        source_kind: 'unknown',
        source_repo_url: '',
        source_path: '',
        namespace: 'kube-system',
        deployment_name: 'local-provisioner',
        container_name: 'provisioner',
        image_repository: '',
        harbor_project: '',
        match_keyword: 'local-provisioner',
        created_at: '',
      },
    ],
  } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [{ id: 1, name: '本地 Kind', enabled: true }],
  } as never)
  vi.mocked(runnerApi.bindings).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.deliveryModel).mockResolvedValue({
    data: {
      applications: [{ id: 2, name: '本地组件' }],
      components: [{ id: 2, application_id: 2, name: 'local-provisioner' }],
      environments: [{ id: 1, name: '待确认环境', requires_review: true }],
      bindings: [
        {
          id: 2,
          app_id: 15,
          application_id: 2,
          component_id: 2,
          environment_id: 1,
          requires_review: true,
        },
      ],
    },
  } as never)

  await page.viewport(390, 844)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ComponentsPage />
    </QueryClientProvider>
  )

  const component = page.getByRole('article')
  await expect.element(component.getByText('待核对')).toBeVisible()
  await expect
    .element(component.getByRole('button', { name: '补全镜像仓库' }))
    .toBeVisible()
  await expect
    .element(component.getByRole('button', { name: '增加部署目标' }))
    .not.toBeInTheDocument()
  if (import.meta.env.VITE_CAPTURE_VISUAL_CHECKS === '1') {
    await page.screenshot({
      path: '../../../../docs/visual-checks/components-legacy-review-390-light.png',
      element: document.body,
    })
  }
  await userEvent.click(
    component.getByRole('button', { name: '核对业务归属与环境' })
  )
  expect(navigateMock).toHaveBeenCalledWith({
    to: '/environments',
    search: { app_id: 15 },
  })
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
})

it('keeps source registration separate from image repository configuration', async () => {
  vi.mocked(platformApi.apps).mockResolvedValue({
    data: [
      {
        id: 12,
        cluster_id: 3,
        app_name: 'legacy-api',
        source_kind: 'unknown',
        source_repo_url: '',
        source_path: '',
        namespace: 'orders-test',
        deployment_name: 'legacy-api',
        container_name: 'api',
        image_repository: '',
        harbor_project: '',
        match_keyword: 'legacy-api',
        created_at: '',
      },
      {
        id: 13,
        cluster_id: 3,
        app_name: 'external-api',
        source_kind: 'third_party',
        source_repo_url: '',
        source_path: '',
        namespace: 'orders-test',
        deployment_name: 'external-api',
        container_name: 'api',
        image_repository: '',
        harbor_project: '',
        match_keyword: 'external-api',
        created_at: '',
      },
    ],
  } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [
      {
        id: 3,
        name: '测试集群',
        enabled: true,
        environment_tier: 'non_production',
        deploy_policy: 'automatic',
      },
    ],
  } as never)
  vi.mocked(runnerApi.bindings).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.deliveryModel).mockResolvedValue({
    data: { applications: [], components: [], environments: [], bindings: [] },
  } as never)

  await page.viewport(390, 844)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ComponentsPage />
    </QueryClientProvider>
  )

  const legacy = page.getByRole('article').nth(0)
  const thirdParty = page.getByRole('article').nth(1)
  await expect.element(legacy.getByText('代码来源待确认')).toBeVisible()
  await expect.element(thirdParty.getByText('已确认：第三方镜像')).toBeVisible()
  await expect.element(legacy.getByText('镜像仓库待配置')).toBeVisible()
  await expect.element(thirdParty.getByText('镜像仓库待配置')).toBeVisible()
  await expect.element(thirdParty.getByText('尚未限定镜像仓库')).toBeVisible()
  await expect
    .element(legacy.getByRole('button', { name: '补全镜像仓库' }))
    .toBeVisible()
  await expect
    .element(thirdParty.getByRole('button', { name: '补全镜像仓库' }))
    .toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
})
