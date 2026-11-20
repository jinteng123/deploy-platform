import type { AnchorHTMLAttributes, ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { afterEach, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page } from 'vitest/browser'
import {
  platformApi,
  type ImageReleaseView,
  type PromotionRequest,
  type PublicUser,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { PromotionRequestsCard } from './release-page'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    platformApi: {
      ...actual.platformApi,
      promotionRequests: vi.fn(),
      approvePromotionRequest: vi.fn(),
      rejectPromotionRequest: vi.fn(),
      release: vi.fn(),
      currentImage: vi.fn(),
      clusters: vi.fn(),
    },
  }
})

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    Link: ({
      to,
      params,
      children,
      ...props
    }: {
      to: string
      params?: Record<string, string>
      children?: ReactNode
    } & AnchorHTMLAttributes<HTMLAnchorElement>) => (
      <a href={to.replace('$planId', params?.planId ?? '')} {...props}>
        {children}
      </a>
    ),
  }
})

const planRequest: PromotionRequest = {
  id: 32,
  release_id: 91,
  prod_cluster_id: 2,
  requester: 'alice',
  status: 'pending',
  can_approve: true,
  delivery_plan_id: 54,
  expected_image: 'registry.example.com/orders/api@sha256:old123',
  template_hash: 'template-hash',
  created_at: '2026-10-11T08:00:00Z',
}

const release: ImageReleaseView = {
  id: 91,
  event_id: 'build-91',
  source: 'yunxiao',
  app_name: 'orders-api',
  image_full: 'registry.example.com/orders/api:2.0.0',
  version: '2.0.0',
  test_cluster_id: 1,
  test_deploy_log_id: 10,
  created_at: '2026-10-11T07:00:00Z',
  updated_at: '2026-10-11T07:00:00Z',
  test_status: 'success',
  can_promote: true,
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
  await page.viewport(1280, 800)
})

it('routes plan-bound approvals through the snapshot review before decision', async () => {
  useAuthStore.getState().auth.setUser(operator)
  vi.mocked(platformApi.promotionRequests).mockResolvedValue({
    data: [planRequest],
  } as never)
  vi.mocked(platformApi.release).mockResolvedValue({ data: release } as never)
  vi.mocked(platformApi.currentImage).mockResolvedValue({
    data: { image: planRequest.expected_image },
  } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [{ id: planRequest.prod_cluster_id, name: 'production' }],
  } as never)

  const screen = await render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false, staleTime: Infinity } },
        })
      }
    >
      <PromotionRequestsCard releases={[release]} />
    </QueryClientProvider>
  )

  const reviewLink = screen.getByRole('link', { name: '核对计划并审批' })
  await expect.element(reviewLink).toHaveAttribute('href', '/tasks/plans/54')
  await expect
    .element(screen.getByRole('button', { name: '批准', exact: true }))
    .not.toBeInTheDocument()
  expect(platformApi.approvePromotionRequest).not.toHaveBeenCalled()
})

it('explains when an operator lacks the approval scope', async () => {
  await page.viewport(390, 844)
  useAuthStore.getState().auth.setUser(operator)
  vi.mocked(platformApi.promotionRequests).mockResolvedValue({
    data: [{ ...planRequest, can_approve: false }],
  } as never)
  vi.mocked(platformApi.release).mockResolvedValue({ data: release } as never)
  vi.mocked(platformApi.currentImage).mockResolvedValue({
    data: { image: planRequest.expected_image },
  } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [{ id: planRequest.prod_cluster_id, name: 'production' }],
  } as never)

  const screen = await render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false, staleTime: Infinity } },
        })
      }
    >
      <PromotionRequestsCard releases={[release]} />
    </QueryClientProvider>
  )

  await expect
    .element(
      screen.getByText('请联系管理员为此业务应用和生产环境配置审批范围。')
    )
    .toBeVisible()
  await expect
    .element(screen.getByRole('link', { name: '核对计划并审批' }))
    .not.toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '批准', exact: true }))
    .not.toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '拒绝', exact: true }))
    .not.toBeInTheDocument()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const requestCard = screen
    .getByText('缺少审批范围')
    .element()
    .closest('article')
  if (!requestCard) throw new Error('审批请求卡片未渲染')
  await page.screenshot({
    path: '../../../../docs/visual-checks/promotion-approval-scope-390-light.png',
    element: requestCard,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.screenshot({
    path: '../../../../docs/visual-checks/promotion-approval-scope-390-dark.png',
    element: requestCard,
  })
  document.documentElement.classList.remove('dark')
  await page.viewport(1440, 900)
  const desktopNotice = screen.getByText('无审批权限').element()
  await expect.element(desktopNotice).toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
  const requestRow = desktopNotice.closest('tr')
  if (!requestRow) throw new Error('桌面审批行未渲染')
  await page.screenshot({
    path: '../../../../docs/visual-checks/promotion-approval-scope-1440-light.png',
    element: requestRow,
  })
})
