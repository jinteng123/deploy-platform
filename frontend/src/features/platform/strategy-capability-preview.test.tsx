import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { beforeEach, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import { platformApi } from '@/api/client'
import { StrategyCapabilityPreview } from './strategy-capability-preview'

vi.mock('@/api/client', () => ({
  platformApi: { deliveryPreview: vi.fn() },
}))

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(platformApi.deliveryPreview).mockResolvedValue({
    data: {
      cluster_id: 2,
      namespace: 'orders',
      deployment: 'orders-api',
      observed_at: new Date().toISOString(),
      matching_services: ['orders-http'],
      autoscalers: [],
      checks: [
        {
          key: 'deployment',
          status: 'passed',
          title: 'Deployment',
          detail: 'Deployment 已读取。',
        },
        {
          key: 'service-entry',
          status: 'passed',
          title: 'Service 入口候选',
          detail: '已找到一个匹配 Service。',
        },
        {
          key: 'autoscaling',
          status: 'passed',
          title: '自动扩缩容',
          detail: '未发现 HPA。',
        },
        {
          key: 'capacity',
          status: 'review',
          title: '双版本容量',
          detail: 'ResourceQuota 足够；仍需确认节点容量。',
        },
        {
          key: 'rolling-write-permission',
          status: 'passed',
          title: '滚动发布执行权限',
          detail: '已通过目标 namespace 权限规则检查（策略：rolling）。',
        },
        {
          key: 'blue-green-write-permission',
          status: 'blocked',
          title: '蓝绿发布执行权限',
          detail:
            '发布执行身份缺少：create deployments.apps。请在目标 namespace 为该身份补充权限后重新预检。',
        },
      ],
    },
  } as never)
})

it('lets an operator bind the blue-green entry to a Service selecting this workload', async () => {
  await page.viewport(390, 844)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const onServiceNameChange = vi.fn()
  const screen = await render(
    <QueryClientProvider client={client}>
      <StrategyCapabilityPreview
        clusterId={2}
        namespace='orders'
        deploymentName='orders-api'
        selectedService=''
        onServiceNameChange={onServiceNameChange}
      />
    </QueryClientProvider>
  )

  await expect
    .element(screen.getByRole('option', { name: 'orders-http' }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('ResourceQuota 足够；仍需确认节点容量。'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('heading', { name: '发布执行权限' }))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(
        '发布执行身份缺少：create deployments.apps。请在目标 namespace 为该身份补充权限后重新预检。'
      )
    )
    .toBeInTheDocument()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await userEvent.selectOptions(
    screen.getByLabelText('蓝绿切换入口 Service'),
    'orders-http'
  )
  expect(onServiceNameChange).toHaveBeenCalledWith('orders-http')
  expect(platformApi.deliveryPreview).toHaveBeenCalledWith({
    cluster_id: 2,
    namespace: 'orders',
    deployment_name: 'orders-api',
  })
  await userEvent.click(screen.getByRole('button', { name: '重新预检' }))
  expect(platformApi.deliveryPreview).toHaveBeenCalledTimes(2)
  await page.viewport(1440, 1000)
  await expect
    .element(screen.getByRole('heading', { name: '发布执行权限' }))
    .toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
  client.clear()
})

it('keeps the Service choice unavailable when the platform cannot confirm selectors', async () => {
  vi.mocked(platformApi.deliveryPreview).mockResolvedValueOnce({
    data: {
      cluster_id: 2,
      namespace: 'orders',
      deployment: 'orders-api',
      observed_at: new Date().toISOString(),
      matching_services: [],
      autoscalers: [],
      checks: [
        {
          key: 'deployment',
          status: 'passed',
          title: 'Deployment',
          detail: 'Deployment 已读取。',
        },
        {
          key: 'service-entry',
          status: 'unknown',
          title: 'Service 入口候选',
          detail: '无法枚举 Service。',
        },
        {
          key: 'autoscaling',
          status: 'passed',
          title: '自动扩缩容',
          detail: '未发现 HPA。',
        },
      ],
    },
  } as never)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <StrategyCapabilityPreview
        clusterId={2}
        namespace='orders'
        deploymentName='orders-api'
        selectedService='orders-http'
        onServiceNameChange={vi.fn()}
      />
    </QueryClientProvider>
  )

  await expect
    .element(screen.getByText('无法枚举 Service。'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByLabelText('蓝绿切换入口 Service'))
    .toBeDisabled()
  client.clear()
})

it('keeps blocked execution permission legible in a narrow dark theme', async () => {
  await page.viewport(390, 844)
  document.documentElement.classList.add('dark')
  try {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const screen = await render(
      <QueryClientProvider client={client}>
        <StrategyCapabilityPreview
          clusterId={2}
          namespace='orders'
          deploymentName='orders-api'
        />
      </QueryClientProvider>
    )
    await expect
      .element(screen.getByRole('heading', { name: '发布执行权限' }))
      .toBeVisible()
    await expect
      .element(
        screen.getByText(
          '发布执行身份缺少：create deployments.apps。请在目标 namespace 为该身份补充权限后重新预检。'
        )
      )
      .toBeVisible()
    const deniedBadge = [
      ...document.querySelectorAll<HTMLElement>('[data-slot="badge"]'),
    ].find((element) => element.textContent?.trim() === '缺少权限')
    expect(deniedBadge?.className).toContain('dark:bg-destructive/10')
    expect(deniedBadge?.className).toContain('text-destructive')
    const darkTextColor = getComputedStyle(deniedBadge!).color
    expect(darkTextColor).not.toBe('')
    const darkBadgeBackground = getComputedStyle(deniedBadge!).backgroundColor
    document.documentElement.classList.remove('dark')
    const lightBadgeBackground = getComputedStyle(deniedBadge!).backgroundColor
    const lightThemeBackground = getComputedStyle(document.documentElement)
      .getPropertyValue('--background')
      .trim()
    document.documentElement.classList.add('dark')
    const darkThemeBackground = getComputedStyle(document.documentElement)
      .getPropertyValue('--background')
      .trim()
    expect(darkThemeBackground).not.toBe(lightThemeBackground)
    expect(darkBadgeBackground).not.toBe(lightBadgeBackground)
    expect(getComputedStyle(deniedBadge!).color).toBe(darkTextColor)
    expect(getComputedStyle(deniedBadge!).backgroundColor).toBe(
      darkBadgeBackground
    )
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
    client.clear()
  } finally {
    document.documentElement.classList.remove('dark')
    await page.viewport(1280, 800)
  }
})
