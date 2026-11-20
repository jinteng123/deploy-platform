import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import { platformApi } from '@/api/client'
import { ApprovalScopesPage } from './index'

vi.mock('@/stores/auth-store', () => ({
  useAuthStore: (select: (state: unknown) => unknown) =>
    select({ auth: { user: { role: 'admin' } } }),
}))
vi.mock('@/components/layout/header', () => ({
  Header: ({ children }: { children: React.ReactNode }) => (
    <header>{children}</header>
  ),
}))
vi.mock('@/components/layout/main', () => ({
  Main: ({ children }: { children: React.ReactNode }) => (
    <main>{children}</main>
  ),
}))
vi.mock('@/components/theme-switch', () => ({ ThemeSwitch: () => null }))
vi.mock('@/components/profile-dropdown', () => ({
  ProfileDropdown: () => null,
}))
vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/client')>()),
  platformApi: {
    users: vi.fn(),
    deliveryModel: vi.fn(),
    deliveryApprovalGrants: vi.fn(),
    replaceDeliveryApprovalGrants: vi.fn(),
  },
}))

it('shows confirmed production scopes and saves an operator assignment', async () => {
  vi.mocked(platformApi.users).mockResolvedValue({
    data: [
      {
        id: 1,
        username: 'release-op',
        display_name: '发布同学',
        role: 'operator',
        k8s_username: '',
        k8s_groups: [],
        enabled: true,
      },
      {
        id: 2,
        username: 'disabled-op',
        display_name: '停用用户',
        role: 'operator',
        k8s_username: '',
        k8s_groups: [],
        enabled: false,
      },
    ],
  } as never)
  vi.mocked(platformApi.deliveryModel).mockResolvedValue({
    data: {
      applications: [
        {
          id: 10,
          name: '订单服务',
          requires_review: false,
          created_at: '',
        },
      ],
      environments: [
        {
          id: 1,
          code: 'test',
          name: '测试环境',
          environment_tier: 'non_production',
          requires_review: false,
          created_at: '',
        },
        {
          id: 20,
          code: 'prod',
          name: '生产环境',
          environment_tier: 'production',
          requires_review: false,
          created_at: '',
        },
      ],
      components: [
        {
          id: 11,
          application_id: 10,
          name: 'api',
          requires_review: false,
          created_at: '',
        },
      ],
      bindings: [
        {
          id: 100,
          app_id: 2,
          application_id: 10,
          component_id: 11,
          environment_id: 20,
          template_workload_ref: {
            cluster_id: 2,
            namespace: 'orders',
            kind: 'Deployment',
            name: 'orders-api',
          },
          active_workload_ref: {
            cluster_id: 2,
            namespace: 'orders',
            kind: 'Deployment',
            name: 'orders-api',
          },
          requires_review: false,
          created_at: '',
        },
      ],
    },
  } as never)
  vi.mocked(platformApi.deliveryApprovalGrants).mockResolvedValue({
    data: [],
  } as never)
  vi.mocked(platformApi.replaceDeliveryApprovalGrants).mockResolvedValue({
    data: [],
  } as never)

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <ApprovalScopesPage />
    </QueryClientProvider>
  )

  expect(screen.getByRole('heading', { name: '发布审批范围' })).toBeTruthy()
  expect(
    screen.getByRole('checkbox', { name: '订单服务，生产环境' })
  ).toBeTruthy()
  expect(document.body.textContent).not.toContain('停用用户')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1280)
  await expect
    .element(screen.getByRole('button', { name: '保存审批范围' }))
    .toBeEnabled()
  const content = document.querySelector('main')!
  await page.viewport(1440, 1000)
  await page.screenshot({
    path: '../../../../../docs/visual-checks/approval-scopes-1440-light.png',
    element: content,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.viewport(1024, 768)
  await page.screenshot({
    path: '../../../../../docs/visual-checks/approval-scopes-1024-dark.png',
    element: content,
  })
  document.documentElement.classList.remove('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))

  await page.viewport(390, 844)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await page.screenshot({
    path: '../../../../../docs/visual-checks/approval-scopes-390-light.png',
    element: content,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.screenshot({
    path: '../../../../../docs/visual-checks/approval-scopes-390-dark.png',
    element: content,
  })
  document.documentElement.classList.remove('dark')
  await userEvent.click(
    screen.getByRole('checkbox', { name: '订单服务，生产环境' })
  )
  await userEvent.click(screen.getByRole('button', { name: '保存审批范围' }))
  await vi.waitFor(() => {
    expect(platformApi.replaceDeliveryApprovalGrants).toHaveBeenCalledWith(1, [
      { application_id: 10, environment_id: 20 },
    ])
  })
})
