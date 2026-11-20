import { useState, type AnchorHTMLAttributes, type ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { afterEach, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import { platformApi, type ImageReleaseView } from '@/api/client'
import { ReleasePage } from './release-page'

vi.mock('@/stores/auth-store', () => ({
  useAuthStore: (select: (state: unknown) => unknown) =>
    select({ auth: { user: { role: 'operator', username: 'alice' } } }),
}))

vi.mock('@/components/layout/header', () => ({
  Header: ({ children }: { children: ReactNode }) => (
    <header className='flex h-16 items-center justify-between border-b border-border bg-background px-4 sm:px-6'>
      {children}
    </header>
  ),
}))

vi.mock('@/components/layout/main', () => ({
  Main: ({ children }: { children: ReactNode }) => (
    <main className='mx-auto w-full max-w-7xl px-4 py-6 sm:px-6 lg:px-8'>
      {children}
    </main>
  ),
}))

vi.mock('@/components/theme-switch', () => ({ ThemeSwitch: () => null }))

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    Link: ({
      to,
      params,
      search,
      children,
      ...props
    }: {
      to: string
      params?: Record<string, string>
      search?: Record<string, string | number | undefined>
      children?: ReactNode
    } & AnchorHTMLAttributes<HTMLAnchorElement>) => {
      const path = Object.entries(params ?? {}).reduce(
        (current, [key, value]) => current.replace(`$${key}`, value),
        to
      )
      const url = new URL(path, 'http://localhost')
      Object.entries(search ?? {}).forEach(([key, value]) => {
        if (value != null) url.searchParams.set(key, String(value))
      })
      return (
        <a href={`${url.pathname}${url.search}`} {...props}>
          {children}
        </a>
      )
    },
  }
})

vi.mock('./delivery-overview', () => ({
  DeliveryOverview: () => null,
}))

vi.mock('./delivery-plan-dialog', () => ({
  DeliveryPlanDialog: () => null,
  DeliveryPlanHistory: () => null,
}))

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    platformApi: {
      ...actual.platformApi,
      releases: vi.fn(),
      promotionRequests: vi.fn(),
      release: vi.fn(),
      deployLog: vi.fn(),
    },
  }
})

const digest = `sha256:${'ab'.repeat(32)}`
const release: ImageReleaseView = {
  id: 104,
  event_id: 'yunxiao-event-104',
  source: 'yunxiao',
  app_name: 'orders-api',
  image_full: 'registry.example.com/commerce/orders-api:2.8.1',
  image_digest: digest,
  version: '2.8.1',
  pipeline_name: 'orders-release',
  pipeline_id: 'pipeline-7182',
  build_number: '904',
  branch: 'main',
  commit_id: 'a7b93e81779f46b184f5064e4d42e81e9f5b38ef',
  test_cluster_id: 1,
  test_cluster_name: '集成测试',
  test_deploy_log_id: 501,
  prod_cluster_id: null,
  prod_deploy_log_id: null,
  created_at: '2026-10-14T09:30:00Z',
  updated_at: '2026-10-14T09:30:00Z',
  test_status: 'success',
  prod_status: null,
  can_promote: true,
  metadata: {
    digest_source: 'ci',
    verification: {
      digest,
      status: 'passed',
      kind: 'manual',
      report_url: '',
      summary: '下单与支付主流程验收通过',
      valid_until: '2099-10-15T09:30:00Z',
      recorded_at: '2026-10-14T09:30:00Z',
      recorded_by: 'alice',
    },
  },
}

function contrastRatio(foreground: string, background: string): number {
  const luminance = (color: string) => {
    const canvas = document.createElement('canvas')
    canvas.width = 1
    canvas.height = 1
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Canvas 2D context is unavailable')
    context.fillStyle = color
    context.fillRect(0, 0, 1, 1)
    const channels = [...context.getImageData(0, 0, 1, 1).data]
      .slice(0, 3)
      .map((channel) => {
        const sRGB = channel / 255
        return sRGB <= 0.04045 ? sRGB / 12.92 : ((sRGB + 0.055) / 1.055) ** 2.4
      })
    return channels[0] * 0.2126 + channels[1] * 0.7152 + channels[2] * 0.0722
  }
  const foregroundLuminance = luminance(foreground)
  const backgroundLuminance = luminance(background)
  const lighter = Math.max(foregroundLuminance, backgroundLuminance)
  const darker = Math.min(foregroundLuminance, backgroundLuminance)
  return (lighter + 0.05) / (darker + 0.05)
}

afterEach(async () => {
  document.documentElement.classList.remove('dark')
  await page.viewport(1280, 800)
  vi.clearAllMocks()
})

it('keeps the release source, evidence and next action readable across viewports', async () => {
  vi.mocked(platformApi.releases).mockResolvedValue({
    data: [release],
  } as never)
  vi.mocked(platformApi.promotionRequests).mockResolvedValue({
    data: [],
  } as never)

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <ReleasePage />
    </QueryClientProvider>
  )

  await page.viewport(1440, 1000)
  await expect
    .element(screen.getByRole('heading', { name: '镜像发布' }))
    .toBeVisible()
  await expect
    .element(screen.getByRole('button', { name: /orders-api 查看发布单 #104/ }))
    .toBeVisible()
  await expect
    .element(
      screen.getByText('orders-release (#pipeline-7182) · 构建 #904').first()
    )
    .toBeVisible()
  await expect
    .element(screen.getByRole('button', { name: '创建发布计划' }))
    .toBeVisible()
  const releaseCard = document.querySelector('[data-slot="card"]')
  expect(releaseCard).not.toBeNull()
  await page.screenshot({
    path: '../../../../docs/visual-checks/release-center-list-1440-light.png',
    element: releaseCard!,
  })
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)

  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.viewport(1024, 768)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
  const tabletReleaseCard = document.querySelector('article')
  expect(tabletReleaseCard).not.toBeNull()
  const tabletReleaseName = tabletReleaseCard!.querySelector('button')!
  const darkCardBackground = getComputedStyle(releaseCard!).backgroundColor
  expect(
    contrastRatio(getComputedStyle(tabletReleaseName).color, darkCardBackground)
  ).toBeGreaterThanOrEqual(4.5)
  await page.screenshot({
    path: '../../../../docs/visual-checks/release-center-list-1024-dark.png',
    element: tabletReleaseCard!,
  })

  document.documentElement.classList.remove('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.viewport(390, 844)
  await expect.element(screen.getByText('Digest 来源：CI 提供')).toBeVisible()
  await expect
    .element(screen.getByRole('article').getByText('下单与支付主流程验收通过'))
    .toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await page.screenshot({
    path: '../../../../docs/visual-checks/release-center-list-390-light.png',
    element: document.querySelector('article')!,
  })

  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await page.screenshot({
    path: '../../../../docs/visual-checks/release-center-list-390-dark.png',
    element: document.querySelector('article')!,
  })

  const action = [...document.querySelectorAll('button')].find(
    (button) =>
      button.textContent?.trim() === '创建发布计划' &&
      button.getBoundingClientRect().width > 0
  )
  expect(action).toBeDefined()
  action!.scrollIntoView({ block: 'center' })
  const actionBounds = action!.getBoundingClientRect()
  expect(actionBounds.top).toBeGreaterThanOrEqual(0)
  expect(actionBounds.bottom).toBeLessThanOrEqual(844)

  await page.viewport(512, 384)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(512)
  action!.scrollIntoView({ block: 'center' })
  const recordAction = [...document.querySelectorAll('button')].find(
    (button) =>
      button.textContent?.trim() === '记录验收' &&
      button.getBoundingClientRect().width > 0
  )!
  const compactRecordBounds = recordAction.getBoundingClientRect()
  const compactPublishBounds = action!.getBoundingClientRect()
  expect(compactRecordBounds.top).toBeGreaterThanOrEqual(0)
  expect(compactRecordBounds.bottom).toBeLessThanOrEqual(384)
  expect(compactPublishBounds.top).toBeGreaterThanOrEqual(0)
  expect(compactPublishBounds.bottom).toBeLessThanOrEqual(384)
  await page.screenshot({
    path: '../../../../docs/visual-checks/release-center-actions-512x384-dark.png',
    element: action!.parentElement!,
  })

  client.clear()
})

it('opens a linked release record and clears its deep link when closed', async () => {
  vi.mocked(platformApi.releases).mockResolvedValue({
    data: [release],
  } as never)
  vi.mocked(platformApi.release).mockResolvedValue({ data: release } as never)
  vi.mocked(platformApi.promotionRequests).mockResolvedValue({
    data: [],
  } as never)
  vi.mocked(platformApi.deployLog).mockResolvedValue({
    data: {
      id: release.test_deploy_log_id,
      cluster_id: release.test_cluster_id,
      app_name: release.app_name,
      deployment_name: 'orders-api',
      container_name: 'api',
      namespace: 'default',
      version: release.version,
      image_full: release.image_full,
      deploy_type: 'deployment',
      status: 'success',
      current_step: 'complete',
      step_detail: '[]',
      created_at: release.created_at,
    },
  } as never)
  const onCloseLinkedRelease = vi.fn()
  function LinkedReleasePage() {
    const [linkedReleaseID, setLinkedReleaseID] = useState<number | undefined>(
      release.id
    )
    return (
      <ReleasePage
        linkedReleaseID={linkedReleaseID}
        onLinkedReleaseClose={() => {
          onCloseLinkedRelease()
          setLinkedReleaseID(undefined)
        }}
      />
    )
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <LinkedReleasePage />
    </QueryClientProvider>
  )

  await expect
    .element(screen.getByRole('heading', { name: '发布单 #104 · orders-api' }))
    .toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: '关闭' }))
  expect(onCloseLinkedRelease).toHaveBeenCalledTimes(1)
  await expect
    .element(screen.getByRole('heading', { name: '发布单 #104 · orders-api' }))
    .not.toBeInTheDocument()

  client.clear()
})
