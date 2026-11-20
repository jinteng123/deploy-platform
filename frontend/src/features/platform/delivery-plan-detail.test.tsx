import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { toast } from 'sonner'
import { afterEach, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import { platformApi, type DeliveryPlan, type PublicUser } from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { DeliveryPlanDetailPage } from './delivery-plan-detail'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    platformApi: {
      deliveryPlan: vi.fn(),
      promotionRequests: vi.fn(),
      approvePromotionRequest: vi.fn(),
      rejectPromotionRequest: vi.fn(),
      deployLog: vi.fn(),
      deployActions: vi.fn(),
      reconcileDeployLog: vi.fn(),
      requestDeliveryPlanApproval: vi.fn(),
      deliveryRunByDeployLog: vi.fn(),
      deliveryRunAction: vi.fn(),
    },
  }
})

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

vi.mock('@/components/layout/header', () => ({
  Header: ({ children }: { children?: ReactNode }) => (
    <header>{children}</header>
  ),
}))

const plan: DeliveryPlan = {
  id: 54,
  release_id: 91,
  target_cluster_id: 2,
  target_app_id: 8,
  strategy: 'rolling',
  status: 'preview_only',
  snapshot_hash: 'snapshot-hash',
  snapshot: {
    schema_version: 9,
    strategy: 'rolling',
    policy: {
      binding_id: 12,
      version: 3,
      source: 'versioned_policy',
      strategy: 'rolling',
      readiness: 'configurable',
    },
    artifact: {
      release_id: 91,
      event_id: 'yunxiao-evt-91',
      image: 'registry.example.com/orders/api@sha256:abc123',
      digest: 'sha256:abc123',
      version: '2026.10.10-8',
      build_number: '108',
      commit_id: 'abcdef1234567890',
      configuration_source: {
        repository_url: 'https://git.example.com/orders/deploy-config.git',
        path: 'charts/orders',
        revision: '0123456789abcdef0123456789abcdef01234567',
        recorded_by: 'runner:4',
        recorded_at: '2026-10-14T08:00:00Z',
      },
    },
    target: {
      cluster_id: 2,
      cluster_name: 'prod-cluster',
      app_id: 8,
      app_name: 'orders-api',
      test_app_id: 7,
      test_binding_id: 11,
      test_environment_id: 1,
      test_environment_name: '测试',
      namespace: 'production',
      deployment: 'orders-api',
      container: 'api',
      environment_id: 2,
      environment_name: '生产',
      environment_tier: 'production',
      application_id: 5,
      application_name: '订单系统',
      component_id: 6,
      component_name: 'orders-api',
      binding_id: 12,
      workload_uid: 'workload-uid-1',
      resource_version: '3091',
      current_image: 'registry.example.com/orders/api@sha256:old123',
      workload_hash: 'workload-hash',
      service_account_name: 'default',
      service_account_pull_secrets_included: false,
      configuration_references: [],
    },
  },
  blockers: [],
  requested_by: 'alice',
  created_at: '2026-10-10T08:00:00Z',
}

const pendingRequest = {
  id: 32,
  release_id: plan.release_id,
  prod_cluster_id: plan.target_cluster_id,
  requester: 'alice',
  status: 'pending' as const,
  can_approve: true,
  delivery_plan_id: plan.id,
  expected_image: plan.snapshot.artifact.image,
  template_hash: 'template-hash',
  created_at: '2026-10-10T08:01:00Z',
  expires_at: '2026-10-10T09:01:00Z',
}

const operator: PublicUser = {
  id: 2,
  username: 'bob',
  display_name: '发布操作员',
  role: 'operator',
  k8s_username: 'operator',
  k8s_groups: [],
  enabled: true,
}

afterEach(async () => {
  useAuthStore.getState().auth.reset()
  document.documentElement.classList.remove('dark')
  await page.viewport(1280, 800)
})

async function renderPage(
  username = operator.username,
  planOverride: DeliveryPlan = plan,
  requestOverride = pendingRequest
) {
  useAuthStore.getState().auth.setUser({ ...operator, username })
  vi.mocked(platformApi.deliveryPlan).mockResolvedValue({
    data: planOverride,
  } as never)
  vi.mocked(platformApi.promotionRequests).mockResolvedValue({
    data: [requestOverride],
  } as never)
  return render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false, staleTime: Infinity } },
        })
      }
    >
      <DeliveryPlanDetailPage planID={plan.id} />
    </QueryClientProvider>
  )
}

it('explains a missing application and environment approval grant', async () => {
  const screen = await renderPage(operator.username, plan, {
    ...pendingRequest,
    can_approve: false,
  })

  await expect
    .element(screen.getByText(/尚未获准审批此业务应用和生产环境/))
    .toBeVisible()
  await expect
    .element(screen.getByRole('button', { name: '批准发布' }))
    .not.toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '拒绝' }))
    .not.toBeInTheDocument()
})

it('shows a full plan snapshot before exposing the separate approval decision', async () => {
  await page.viewport(1440, 900)
  document.documentElement.classList.remove('dark')
  vi.mocked(platformApi.approvePromotionRequest).mockResolvedValue({} as never)
  const screen = await renderPage()

  await expect
    .element(screen.getByText('订单系统 / orders-api'))
    .toBeInTheDocument()
  await expect.element(screen.getByText(/当前运行/)).toBeInTheDocument()
  await expect
    .element(screen.getByText('registry.example.com/orders/api@sha256:old123'))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(
        'https://git.example.com/orders/deploy-config.git / charts/orders'
      )
    )
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('0123456789abcdef0123456789abcdef01234567'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('申请人和审批人必须是不同账号'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '批准发布' }))
    .toBeInTheDocument()

  const reviewScreenshots = [
    { width: 1440, height: 900, theme: 'light' },
    { width: 1024, height: 768, theme: 'light' },
    { width: 390, height: 844, theme: 'light' },
    { width: 1440, height: 900, theme: 'dark' },
    { width: 1024, height: 768, theme: 'dark' },
    { width: 390, height: 844, theme: 'dark' },
  ] as const
  let activeTheme: 'light' | 'dark' = 'light'
  for (const viewport of reviewScreenshots) {
    if (viewport.theme !== activeTheme) {
      document.documentElement.classList.toggle(
        'dark',
        viewport.theme === 'dark'
      )
      await new Promise((resolve) => setTimeout(resolve, 200))
      activeTheme = viewport.theme
    }
    await page.viewport(viewport.width, viewport.height)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(
      viewport.width
    )
    await expect
      .element(screen.getByRole('button', { name: '批准发布' }))
      .toBeVisible()
    const originalBodyHeight = document.body.style.height
    const originalBodyOverflow = document.body.style.overflow
    try {
      document.body.style.height = `${viewport.height}px`
      document.body.style.overflow = 'hidden'
      await page.screenshot({
        path: `../../../../docs/visual-checks/delivery-plan-review-page-${viewport.width}-${viewport.theme}.png`,
      })
    } finally {
      document.body.style.height = originalBodyHeight
      document.body.style.overflow = originalBodyOverflow
    }
  }
  await page.viewport(390, 844)
  const approvalButton = screen
    .getByRole('button', { name: '批准发布' })
    .element()
  approvalButton.scrollIntoView({ block: 'center' })
  const approvalBounds = approvalButton.getBoundingClientRect()
  expect(approvalBounds.top).toBeGreaterThanOrEqual(0)
  expect(approvalBounds.bottom).toBeLessThanOrEqual(window.innerHeight)
  const rejectionBounds = screen
    .getByRole('button', { name: '拒绝', exact: true })
    .element()
    .getBoundingClientRect()
  expect(rejectionBounds.top).toBeGreaterThanOrEqual(0)
  expect(rejectionBounds.bottom).toBeLessThanOrEqual(window.innerHeight)
  await expect
    .element(screen.getByRole('button', { name: '拒绝', exact: true }))
    .toBeVisible()
  document.documentElement.classList.remove('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.viewport(1440, 900)

  await userEvent.click(screen.getByRole('button', { name: '批准发布' }))
  await vi.waitFor(() => {
    expect(platformApi.approvePromotionRequest).toHaveBeenCalledWith(
      pendingRequest.id
    )
  })
})

it('copies the pinned configuration commit from the plan snapshot', async () => {
  const previousClipboard = Object.getOwnPropertyDescriptor(
    navigator,
    'clipboard'
  )
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  })

  try {
    const screen = await renderPage()
    await userEvent.click(
      screen.getByRole('button', { name: '复制配置 Git commit' })
    )

    expect(writeText).toHaveBeenCalledWith(
      '0123456789abcdef0123456789abcdef01234567'
    )
    expect(toast.success).toHaveBeenCalledWith('已复制配置 Git commit')
  } finally {
    if (previousClipboard) {
      Object.defineProperty(navigator, 'clipboard', previousClipboard)
    } else {
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  }
})

it('does not offer approval to the person who requested the release', async () => {
  const screen = await renderPage('alice')

  await expect
    .element(screen.getByText('申请人不能审批自己的请求'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '批准发布' }))
    .not.toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '拒绝', exact: true }))
    .not.toBeInTheDocument()
})

it('keeps rejection available but blocks approval for a legacy plan without a configuration snapshot', async () => {
  await page.viewport(390, 844)
  document.documentElement.classList.add('dark')
  const legacyPlan: DeliveryPlan = {
    ...plan,
    snapshot: {
      ...plan.snapshot,
      schema_version: 5,
      target: {
        ...plan.snapshot.target,
        configuration_references: undefined,
      },
    },
  }
  const screen = await renderPage(operator.username, legacyPlan)

  await expect.element(screen.getByRole('alert')).toBeVisible()
  await expect
    .element(screen.getByText(/此计划缺少完整配置引用快照，不能批准/))
    .toBeVisible()
  await expect
    .element(screen.getByRole('button', { name: '批准发布' }))
    .not.toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '拒绝', exact: true }))
    .toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)

  const alert = screen.getByRole('alert').element()
  const decisionPanel = alert.parentElement?.parentElement
  if (!decisionPanel) throw new Error('审批提示区域未渲染')
  await page.screenshot({
    path: '../../../../docs/visual-checks/delivery-plan-legacy-390-dark.png',
    element: decisionPanel,
  })
  document.documentElement.classList.remove('dark')
  await page.viewport(1440, 900)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
  await page.screenshot({
    path: '../../../../docs/visual-checks/delivery-plan-legacy-1440-light.png',
    element: decisionPanel,
  })
})

it('marks a schema-v7 configuration scan failure as unknown and blocks approval', async () => {
  await page.viewport(390, 844)
  document.documentElement.classList.add('dark')
  const incompletePlan: DeliveryPlan = {
    ...plan,
    snapshot: {
      ...plan.snapshot,
      schema_version: 7,
      target: {
        ...plan.snapshot.target,
        configuration_references: undefined,
      },
    },
  }
  const screen = await renderPage(operator.username, incompletePlan)

  await expect.element(screen.getByText('配置引用无法确认')).toBeVisible()
  await expect
    .element(
      screen.getByText(
        /当前计划无法完整读取 Pod 模板或其 ServiceAccount 的镜像拉取引用/
      )
    )
    .toBeVisible()
  await expect
    .element(screen.getByRole('button', { name: '批准发布' }))
    .not.toBeInTheDocument()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const configState = screen.getByText('配置引用无法确认').element()
    .parentElement?.parentElement
  if (!configState) throw new Error('配置引用状态卡未渲染')
  await page.screenshot({
    path: '../../../../docs/visual-checks/delivery-plan-config-unknown-390-dark.png',
    element: configState,
  })
  document.documentElement.classList.remove('dark')
  await page.viewport(1440, 900)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
  await page.screenshot({
    path: '../../../../docs/visual-checks/delivery-plan-config-unknown-1440-light.png',
    element: configState,
  })
})

it('explains inherited image-pull Secrets in the plan snapshot', async () => {
  const inheritedPullSecretPlan: DeliveryPlan = {
    ...plan,
    snapshot: {
      ...plan.snapshot,
      target: {
        ...plan.snapshot.target,
        service_account_name: 'orders-runtime',
        service_account_pull_secrets_included: true,
        configuration_references: [
          {
            kind: 'Secret',
            name: 'private-registry-auth',
            uid: 'secret-uid',
            resource_version: '42',
          },
        ],
      },
    },
  }
  const screen = await renderPage(operator.username, inheritedPullSecretPlan)

  await expect.element(screen.getByText(/已包含 ServiceAccount/)).toBeVisible()
  await expect.element(screen.getByText('orders-runtime')).toBeVisible()
  await expect.element(screen.getByText('private-registry-auth')).toBeVisible()
  await expect.element(screen.getByText('rv 42')).toBeVisible()
  await expect
    .element(screen.getByText(/用于发现对象替换或变更；不会保存配置值/))
    .toBeVisible()
})

it('labels external configuration versions unknown when metadata could not be read', async () => {
  const unknownVersionPlan: DeliveryPlan = {
    ...plan,
    snapshot: {
      ...plan.snapshot,
      target: {
        ...plan.snapshot.target,
        configuration_references: [
          { kind: 'Secret', name: 'private-registry-auth' },
        ],
      },
    },
  }
  const screen = await renderPage(operator.username, unknownVersionPlan)
  await expect.element(screen.getByText('版本未知')).toBeVisible()
})

it('retains the rejection reason when the decision request fails', async () => {
  vi.mocked(platformApi.rejectPromotionRequest).mockRejectedValue(
    new Error('network unavailable')
  )
  const screen = await renderPage()

  await userEvent.click(screen.getByRole('button', { name: '拒绝' }))
  const reason = screen.getByRole('textbox', { name: '拒绝原因' })
  await userEvent.fill(reason, '需要重新确认容量预算')
  await userEvent.click(screen.getByRole('button', { name: '确认拒绝' }))

  await vi.waitFor(() => {
    expect(platformApi.rejectPromotionRequest).toHaveBeenCalledWith(
      pendingRequest.id,
      '需要重新确认容量预算'
    )
  })
  await expect.element(reason).toHaveValue('需要重新确认容量预算')
  await expect.element(reason).toBeVisible()
})

it('keeps the review page readable without horizontal overflow on a narrow dark viewport', async () => {
  await page.viewport(390, 844)
  document.documentElement.classList.add('dark')
  const screen = await renderPage()

  await expect
    .element(screen.getByRole('heading', { name: '发布计划详情' }))
    .toBeVisible()
  await expect
    .element(screen.getByRole('button', { name: '批准发布' }))
    .toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
})
