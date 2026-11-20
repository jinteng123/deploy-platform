import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { toast } from 'sonner'
import { beforeEach, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import {
  k8sApi,
  platformApi,
  type Cluster,
  type ComponentBinding,
} from '@/api/client'
import { ComponentOnboarding } from './component-onboarding'

vi.mock('@/api/client', async (original) => ({
  ...(await original<typeof import('@/api/client')>()),
  platformApi: { previewApp: vi.fn(), createApp: vi.fn(), updateApp: vi.fn() },
  k8sApi: { resources: vi.fn(), resource: vi.fn() },
}))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

const cluster: Cluster = {
  id: 7,
  name: '测试集群',
  env_code: 'test-orders',
  environment: 'test',
  environment_tier: 'non_production',
  deploy_policy: 'automatic',
  connection_type: 'kubeconfig',
  credential_configured: true,
  enabled: true,
  default_namespace: 'orders',
  insecure_skip_tls_verify: false,
  created_at: '',
  updated_at: '',
}
const binding: ComponentBinding = {
  id: 3,
  cluster_id: 7,
  app_name: 'order-api',
  source_kind: 'repository',
  source_repo_url: 'https://git.example.com/team/order.git',
  source_path: 'services/api',
  image_repository: 'registry.example.com/orders/api',
  namespace: 'orders',
  deployment_name: 'orders-api',
  container_name: 'api',
  match_keyword: 'order-api',
  harbor_project: 'orders',
  created_at: '',
}
const deployment = {
  metadata: { name: 'orders-api' },
  spec: {
    template: {
      spec: {
        containers: [
          { name: 'api', image: 'registry.example.com/orders/api:v1' },
          { name: 'metrics', image: 'registry.example.com/tools/metrics:v1' },
        ],
      },
    },
  },
}

beforeEach(async () => {
  await page.viewport(1440, 1000)
  vi.clearAllMocks()
  vi.mocked(k8sApi.resource).mockResolvedValue({ data: deployment } as never)
  vi.mocked(k8sApi.resources).mockImplementation(
    async (params) =>
      ({
        data: {
          items:
            params.resource === 'namespaces'
              ? [{ metadata: { name: 'orders' } }]
              : params.resource === 'deployments'
                ? [deployment]
                : [],
        },
      }) as never
  )
  vi.mocked(platformApi.previewApp).mockImplementation(
    async (payload) =>
      ({
        data: {
          app: { ...payload, id: 0 },
          current_image: 'registry.example.com/orders/api:v1',
          warnings: [],
        },
      }) as never
  )
  vi.mocked(platformApi.createApp).mockResolvedValue({ data: binding } as never)
  vi.mocked(platformApi.updateApp).mockResolvedValue({ data: binding } as never)
})

async function setup(
  options: {
    editing?: ComponentBinding
    copyFrom?: ComponentBinding
    target?: Cluster
  } = {}
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const onSaved = vi.fn(),
    onClose = vi.fn(),
    onNextStep = vi.fn()
  const screen = await render(
    <QueryClientProvider client={client}>
      <ComponentOnboarding
        editing={options.editing}
        copyFrom={options.copyFrom}
        clusters={[options.target ?? cluster]}
        existing={[]}
        onClose={onClose}
        onNextStep={onNextStep}
        onSaved={onSaved}
      />
    </QueryClientProvider>
  )
  return { screen, onSaved, onClose, onNextStep }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

async function chooseTarget(
  screen: Awaited<ReturnType<typeof setup>>['screen'],
  targetId = '7'
) {
  await userEvent.click(
    screen.getByRole('button', { name: '下一步：选择部署目标' })
  )
  await userEvent.selectOptions(
    screen.getByLabelText('部署到哪个集群'),
    targetId
  )
  await expect
    .element(screen.getByLabelText('要更新哪个 Deployment'))
    .toBeEnabled()
  await userEvent.selectOptions(
    screen.getByLabelText('要更新哪个 Deployment'),
    'orders-api'
  )
  await userEvent.selectOptions(
    screen.getByLabelText('要替换哪个容器的镜像'),
    'api'
  )
}

it('shows bounded loading feedback across mobile, tablet and desktop', async () => {
  await page.viewport(390, 844)
  const namespaceRequest = deferred<{
    data: { items: { metadata: { name: string } }[] }
  }>()
  const deploymentRequest = deferred<{
    data: { items: (typeof deployment)[] }
  }>()
  vi.mocked(k8sApi.resources).mockImplementation(
    (params) =>
      (params.resource === 'namespaces'
        ? namespaceRequest.promise
        : deploymentRequest.promise) as never
  )

  const { screen } = await setup({ copyFrom: binding })
  await userEvent.click(
    screen.getByRole('button', { name: '下一步：选择部署目标' })
  )
  await userEvent.selectOptions(screen.getByLabelText('部署到哪个集群'), '7')

  await expect
    .element(screen.getByText('正在读取测试集群的命名空间…'))
    .toBeVisible()
  await expect
    .element(screen.getByText('正在读取此命名空间的 Deployment…'))
    .toBeVisible()
  await expect
    .element(screen.getByRole('button', { name: '正在读取集群资源…' }))
    .toBeDisabled()
  await expect
    .element(screen.getByLabelText('要更新哪个 Deployment'))
    .toBeDisabled()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-loading-390-light.png',
    element: screen.getByRole('dialog').element(),
  })
  const dialog = screen.getByRole('dialog').element()
  try {
    await page.viewport(1024, 768)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-loading-1024-light.png',
      element: dialog,
    })
    document.documentElement.classList.add('dark')
    await new Promise((resolve) => setTimeout(resolve, 200))
    await page.viewport(390, 844)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-loading-390-dark.png',
      element: dialog,
    })
    await page.viewport(1024, 768)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-loading-1024-dark.png',
      element: dialog,
    })
    await page.viewport(1440, 1000)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-loading-1440-dark.png',
      element: dialog,
    })
    document.documentElement.classList.remove('dark')
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-loading-1440-light.png',
      element: dialog,
    })
  } finally {
    document.documentElement.classList.remove('dark')
  }

  namespaceRequest.resolve({
    data: { items: [{ metadata: { name: 'orders' } }] },
  })
  deploymentRequest.resolve({ data: { items: [deployment] } })
  await expect
    .element(screen.getByRole('button', { name: '检查绑定并继续' }))
    .toBeEnabled()
  await expect
    .element(screen.getByLabelText('要更新哪个 Deployment'))
    .toBeEnabled()
})

it('explains an empty Deployment list and prevents a phantom binding', async () => {
  await page.viewport(390, 844)
  vi.mocked(k8sApi.resources).mockImplementation(
    async (params) =>
      (params.resource === 'namespaces'
        ? { data: { items: [{ metadata: { name: 'orders' } }] } }
        : { data: { items: [] } }) as never
  )

  const { screen } = await setup({ copyFrom: binding })
  await userEvent.click(
    screen.getByRole('button', { name: '下一步：选择部署目标' })
  )
  await userEvent.selectOptions(screen.getByLabelText('部署到哪个集群'), '7')

  await expect
    .element(
      screen.getByText(
        '此命名空间暂无可选择的 Deployment。请先部署服务，或选择其他命名空间。'
      )
    )
    .toBeVisible()
  await expect
    .element(screen.getByLabelText('要更新哪个 Deployment'))
    .toBeDisabled()
  await expect
    .element(screen.getByLabelText('要替换哪个容器的镜像'))
    .toBeDisabled()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-empty-deployments-390-light.png',
    element: screen.getByRole('dialog').element(),
  })
  const dialog = screen.getByRole('dialog').element()
  try {
    await page.viewport(1024, 768)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-empty-deployments-1024-light.png',
      element: dialog,
    })
    document.documentElement.classList.add('dark')
    await new Promise((resolve) => setTimeout(resolve, 200))
    await page.viewport(390, 844)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-empty-deployments-390-dark.png',
      element: dialog,
    })
    await page.viewport(1024, 768)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-empty-deployments-1024-dark.png',
      element: dialog,
    })
    await page.viewport(1440, 1000)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-empty-deployments-1440-dark.png',
      element: dialog,
    })
    document.documentElement.classList.remove('dark')
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-empty-deployments-1440-light.png',
      element: dialog,
    })
  } finally {
    document.documentElement.classList.remove('dark')
  }

  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent('请依次选择集群、命名空间、Deployment 和容器。')
  expect(platformApi.previewApp).not.toHaveBeenCalled()
})

it('shows the exact build-to-container route on mobile and desktop', async () => {
  await page.viewport(390, 844)
  const { screen } = await setup({ copyFrom: binding })
  await chooseTarget(screen)

  await expect.element(screen.getByText('2. 部署目标')).toBeVisible()
  const route = screen.getByRole('region', { name: '构建会更新这里' })
  await expect.element(route).toBeInTheDocument()
  await expect.element(route).toHaveTextContent('order-api')
  await expect.element(route).toHaveTextContent('test-orders')
  await expect
    .element(route)
    .toHaveTextContent(`${binding.image_repository}:<tag>`)
  await expect.element(route).toHaveTextContent('测试集群')
  await expect.element(route).toHaveTextContent('orders / orders-api / api')
  await expect.element(route).toHaveTextContent('不会保存配置或改动集群')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)

  await page.viewport(1440, 1000)
  await expect.element(route).toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
})

it('keeps the routing summary readable in a narrow dark theme', async () => {
  await page.viewport(390, 844)
  document.documentElement.classList.add('dark')
  try {
    const { screen } = await setup({ copyFrom: binding })
    await chooseTarget(screen)
    const route = screen.getByRole('region', { name: '构建会更新这里' })
    await expect
      .element(route)
      .toHaveTextContent('registry.example.com/orders/api')
    await expect.element(route).toHaveTextContent('orders / orders-api / api')
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  } finally {
    document.documentElement.classList.remove('dark')
  }
})

it('keeps the mobile binding review focused with a compact, touch-friendly action bar', async () => {
  await page.viewport(390, 844)
  const { screen } = await setup({ copyFrom: binding })
  await chooseTarget(screen)
  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  await userEvent.click(screen.getByRole('checkbox'))
  await expect.element(screen.getByLabelText('CI 的 APP_NAME')).toBeEnabled()
  await expect
    .element(screen.getByRole('button', { name: '保存组件绑定' }))
    .toBeEnabled()

  const dialog = screen.getByRole('dialog').element()
  await new Promise((resolve) => setTimeout(resolve, 200))
  const footer = dialog.querySelector<HTMLElement>(
    '[data-slot="dialog-footer"]'
  )
  const primary = screen.getByRole('button', { name: '保存组件绑定' }).element()
  const back = screen.getByRole('button', { name: '上一步' }).element()
  const cancel = screen.getByRole('button', { name: '取消' }).element()
  const confirmation = screen.getByRole('checkbox').element()
  expect(footer).not.toBeNull()
  const confirmationRect = confirmation.getBoundingClientRect()
  const primaryRect = primary.getBoundingClientRect()
  const backRect = back.getBoundingClientRect()
  const cancelRect = cancel.getBoundingClientRect()
  expect(confirmationRect.top).toBeGreaterThanOrEqual(0)
  expect(confirmationRect.bottom).toBeLessThanOrEqual(844)
  expect(confirmationRect.top).toBeLessThan(primaryRect.top)
  expect(primaryRect.top).toBeLessThan(backRect.top)
  expect(Math.abs(backRect.top - cancelRect.top)).toBeLessThan(1)
  for (const rect of [primaryRect, backRect, cancelRect]) {
    expect(rect.height).toBeGreaterThanOrEqual(44)
    expect(rect.left).toBeGreaterThanOrEqual(0)
    expect(rect.right).toBeLessThanOrEqual(390)
  }
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const body = screen
    .getByTestId('component-onboarding-scroll-content')
    .element()
  body.scrollTop = 0
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-review-390-light.png',
    element: dialog,
  })
  body.scrollTop = body.scrollHeight
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-review-390-bottom-light.png',
    element: dialog,
  })

  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-review-390-dark.png',
    element: dialog,
  })
  document.documentElement.classList.remove('dark')

  await page.viewport(1440, 1000)
  await new Promise((resolve) => setTimeout(resolve, 200))
  body.scrollTop = 0
  expect((primary as HTMLButtonElement).disabled).toBe(false)
  expect(getComputedStyle(primary).opacity).toBe('1')
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-review-1440-light.png',
    element: dialog,
  })
  const desktopTops = [primary, back, cancel].map(
    (button) => button.getBoundingClientRect().top
  )
  expect(Math.max(...desktopTops) - Math.min(...desktopTops)).toBeLessThan(1)
})

it('blocks save and clears the review confirmation when simulated CI values change', async () => {
  await page.viewport(390, 844)
  try {
    const { screen } = await setup({ copyFrom: binding })
    await chooseTarget(screen)
    await userEvent.click(
      screen.getByRole('button', { name: '检查绑定并继续' })
    )

    const confirmation = screen.getByRole('checkbox')
    const save = screen.getByRole('button', { name: '保存组件绑定' })
    await userEvent.click(confirmation)
    await expect.element(save).toBeEnabled()

    await userEvent.fill(
      screen.getByLabelText('云效 sources[].repo'),
      'https://git.example.com/team/other.git'
    )
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent('sources.repo 应与组件登记的代码仓库一致')
    await expect.element(confirmation).not.toBeChecked()
    await expect.element(confirmation).toBeDisabled()
    await expect.element(save).toBeDisabled()

    const content = screen
      .getByTestId('component-onboarding-scroll-content')
      .element()
    content.scrollTop = content.scrollHeight
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
    await page.screenshot({
      path: '../../../../docs/visual-checks/component-onboarding-390-source-mismatch-blocked.png',
      element: screen.getByRole('dialog').element(),
    })

    await userEvent.fill(
      screen.getByLabelText('云效 sources[].repo'),
      binding.source_repo_url
    )
    await expect.element(confirmation).toBeEnabled()
    await expect.element(confirmation).not.toBeChecked()
    await expect.element(save).toBeDisabled()
    await userEvent.click(confirmation)
    await expect.element(save).toBeEnabled()

    await userEvent.fill(screen.getByLabelText('CI 的 APP_NAME'), 'other-api')
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent('APP_NAME 应为 order-api')
    await expect.element(confirmation).not.toBeChecked()
    await expect.element(confirmation).toBeDisabled()
    await expect.element(save).toBeDisabled()
    expect(platformApi.createApp).not.toHaveBeenCalled()
  } finally {
    await page.viewport(1440, 1000)
  }
})

it('guides a monorepo component to the exact container, previews without saving, and copies explicit CI values', async () => {
  const { screen, onSaved, onNextStep } = await setup()
  await userEvent.fill(
    screen.getByLabelText('组件标识（CI 的 APP_NAME）'),
    'order-api'
  )
  await userEvent.selectOptions(
    screen.getByLabelText('镜像由哪里构建'),
    'repository'
  )
  await userEvent.fill(
    screen.getByLabelText('代码仓库地址'),
    binding.source_repo_url
  )
  await userEvent.fill(screen.getByLabelText('仓库内构建目录'), 'services/api')
  await userEvent.fill(
    screen.getByLabelText('Helm / Git 部署配置仓库（可选）'),
    'https://git.example.com/team/deploy-config.git'
  )
  await userEvent.fill(
    screen.getByLabelText('配置仓库内目录'),
    'charts/order-api'
  )
  const sourceContent = screen
    .getByTestId('component-onboarding-scroll-content')
    .element()
  await page.viewport(390, 844)
  const configPathRect = screen
    .getByLabelText('配置仓库内目录')
    .element()
    .getBoundingClientRect()
  sourceContent.scrollTop +=
    configPathRect.top - sourceContent.getBoundingClientRect().top - 120
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-390-config-source-light.png',
    element: screen.getByRole('dialog').element(),
  })
  await page.viewport(1440, 1000)
  await chooseTarget(screen)
  await expect
    .element(screen.getByLabelText('这个组件使用的镜像仓库'))
    .toHaveValue(binding.image_repository)
  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  await expect
    .element(screen.getByText('这次设置建立的对应关系'))
    .toBeInTheDocument()
  const mapping = screen.getByRole('region', { name: '组件对应关系' })
  await page.viewport(390, 844)
  const configSummary = mapping
    .getByText(/deploy-config\.git/)
    .element()
    .getBoundingClientRect()
  const reviewContent = screen
    .getByTestId('component-onboarding-scroll-content')
    .element()
  reviewContent.scrollTop +=
    configSummary.top - reviewContent.getBoundingClientRect().top - 120
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-390-config-review-light.png',
    element: screen.getByRole('dialog').element(),
  })
  await page.viewport(1440, 1000)
  await expect.element(mapping).toHaveTextContent(binding.source_repo_url)
  await expect.element(mapping).toHaveTextContent('services/api')
  await expect.element(mapping).toHaveTextContent('deploy-config.git')
  await expect.element(mapping).toHaveTextContent('charts/order-api')
  await expect.element(mapping).toHaveTextContent('APP_NAME → 组件')
  await expect.element(mapping).toHaveTextContent('TARGET_CLUSTER → 部署位置')
  await expect
    .element(mapping)
    .toHaveTextContent('orders / Deployment orders-api / 容器 api')
  expect(platformApi.createApp).not.toHaveBeenCalled()
  await expect
    .element(screen.getByText(/"target_cluster": "test-orders"/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/"app_name": "order-api"/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/"source_repository":/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/"source_path": "services\/api"/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByLabelText('云效 sources[].repo'))
    .toHaveValue(binding.source_repo_url)
  await expect
    .element(screen.getByLabelText('CI 的 SOURCE_PATH'))
    .toHaveValue('services/api')
  await page.viewport(390, 844)
  const simulationContent = screen
    .getByTestId('component-onboarding-scroll-content')
    .element()
  const sourcePathRect = screen
    .getByLabelText('CI 的 SOURCE_PATH')
    .element()
    .getBoundingClientRect()
  simulationContent.scrollTop +=
    sourcePathRect.top - simulationContent.getBoundingClientRect().top - 120
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-390-source-match-light.png',
    element: screen.getByRole('dialog').element(),
  })
  simulationContent.scrollTop = simulationContent.scrollHeight
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.screenshot({
    path: '../../../../docs/visual-checks/component-onboarding-390-source-match-result-light.png',
    element: screen.getByRole('dialog').element(),
  })
  await page.viewport(1440, 1000)
  await userEvent.fill(
    screen.getByLabelText('云效 sources[].repo'),
    'git@git.example.com:team/order.git'
  )
  await expect
    .element(screen.getByRole('status'))
    .toHaveTextContent('命中绑定：order-api + test-orders')
  await userEvent.fill(
    screen.getByLabelText('云效 sources[].repo'),
    'https://git.example.com/team/other.git'
  )
  await expect
    .element(screen.getByRole('status'))
    .toHaveTextContent('sources.repo 应与组件登记的代码仓库一致')
  await userEvent.fill(
    screen.getByLabelText('云效 sources[].repo'),
    binding.source_repo_url
  )
  await userEvent.fill(
    screen.getByLabelText('CI 的 SOURCE_PATH'),
    'services/web'
  )
  await expect
    .element(screen.getByRole('status'))
    .toHaveTextContent('SOURCE_PATH 应为 services/api')
  await userEvent.fill(
    screen.getByLabelText('CI 的 SOURCE_PATH'),
    'services/api'
  )
  await userEvent.fill(
    screen.getByLabelText('CI 的 IMAGE_FULL'),
    'evil.example.com/orders/api:v2'
  )
  await expect
    .element(screen.getByRole('status'))
    .toHaveTextContent('IMAGE_FULL 必须属于')
  await userEvent.fill(
    screen.getByLabelText('CI 的 IMAGE_FULL'),
    `${binding.image_repository}@sha256:${'ab'.repeat(32)}`
  )
  expect(
    screen.getByLabelText('CI 的 IMAGE_FULL').element().getBoundingClientRect()
      .height
  ).toBeGreaterThanOrEqual(63.5)
  await expect
    .element(screen.getByRole('status'))
    .toHaveTextContent('order-api + test-orders')
  await userEvent.fill(screen.getByLabelText('CI 的 APP_NAME'), 'wrong-service')
  await expect
    .element(screen.getByRole('status'))
    .toHaveTextContent('APP_NAME 应为 order-api')
  await userEvent.fill(screen.getByLabelText('CI 的 APP_NAME'), 'order-api')
  await userEvent.fill(
    screen.getByLabelText('CI 的 TARGET_CLUSTER'),
    'prod-orders'
  )
  await expect
    .element(screen.getByRole('status'))
    .toHaveTextContent('TARGET_CLUSTER 应为 test-orders')
  await userEvent.fill(
    screen.getByLabelText('CI 的 TARGET_CLUSTER'),
    'test-orders'
  )
  await expect
    .element(screen.getByRole('button', { name: '保存组件绑定' }))
    .toBeDisabled()
  await userEvent.click(screen.getByRole('checkbox'))
  await userEvent.click(screen.getByRole('button', { name: '保存组件绑定' }))
  await vi.waitFor(() => expect(onSaved).toHaveBeenCalledOnce())
  const toastCalls = vi.mocked(toast.success).mock.calls
  const toastCall = toastCalls[toastCalls.length - 1]
  const toastOptions = toastCall?.[1] as
    | {
        action?: { label?: string; onClick?: () => void }
        description?: string
      }
    | undefined
  expect(toastOptions?.description).toContain('关联业务应用、逻辑组件和环境')
  expect(toastOptions?.action?.label).toBe('关联业务应用与环境')
  toastOptions?.action?.onClick?.()
  expect(onNextStep).toHaveBeenCalledWith(binding.id)
  expect(platformApi.createApp).toHaveBeenCalledWith(
    expect.objectContaining({
      app_name: 'order-api',
      source_path: 'services/api',
      deployment_config_repo_url:
        'https://git.example.com/team/deploy-config.git',
      deployment_config_path: 'charts/order-api',
      image_repository: binding.image_repository,
      cluster_id: 7,
      deployment_name: 'orders-api',
      container_name: 'api',
    })
  )
})

it('keeps the CI image field and fixed action visible at a 200% equivalent viewport', async () => {
  await page.viewport(1440, 1000)
  const { screen } = await setup({ copyFrom: binding })
  await chooseTarget(screen)
  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  await expect.element(screen.getByLabelText('CI 的 IMAGE_FULL')).toBeVisible()

  await page.viewport(512, 384)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(512)
  expect(
    screen.getByLabelText('CI 的 IMAGE_FULL').element().getBoundingClientRect()
      .height
  ).toBeGreaterThanOrEqual(64)
  await expect
    .element(screen.getByRole('button', { name: '保存组件绑定' }))
    .toBeVisible()
  const confirmation = screen.getByRole('checkbox').element()
  const confirmationRect = confirmation.getBoundingClientRect()
  expect(confirmationRect.top).toBeGreaterThanOrEqual(0)
  expect(confirmationRect.bottom).toBeLessThanOrEqual(384)
})

it('rejects a tag in the repository field before preview and rechecks after going back', async () => {
  const { screen } = await setup({ copyFrom: binding })
  await chooseTarget(screen)
  await userEvent.fill(
    screen.getByLabelText('这个组件使用的镜像仓库'),
    `${binding.image_repository}:v2`
  )
  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent('不含 tag 或 digest')
  expect(platformApi.previewApp).not.toHaveBeenCalled()
  await userEvent.fill(
    screen.getByLabelText('这个组件使用的镜像仓库'),
    binding.image_repository
  )
  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  await expect.element(screen.getByRole('checkbox')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('checkbox'))
  await userEvent.click(screen.getByRole('button', { name: '上一步' }))
  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  await expect
    .element(screen.getByRole('button', { name: '保存组件绑定' }))
    .toBeDisabled()
  expect(platformApi.previewApp).toHaveBeenCalledTimes(2)
})

it('keeps production binding out of automatic CI examples when adding an environment', async () => {
  const { screen } = await setup({
    copyFrom: binding,
    target: {
      ...cluster,
      id: 8,
      name: '生产集群',
      environment_tier: 'production',
      deploy_policy: 'manual',
      env_code: 'prod-orders',
    },
  })
  await chooseTarget(screen, '8')
  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  await expect
    .element(screen.getByText('这次设置建立的对应关系'))
    .toBeInTheDocument()
  await userEvent.click(screen.getByText('CI 如何通知平台'))
  await expect
    .element(screen.getByText(/生产绑定通过发布中心选择目标并审批/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '复制请求示例' }))
    .not.toBeInTheDocument()
  expect(platformApi.previewApp).toHaveBeenCalledWith(
    expect.objectContaining({
      app_name: 'order-api',
      cluster_id: 8,
      source_repo_url: binding.source_repo_url,
      image_repository: binding.image_repository,
    })
  )
})

it('shows resource permission failures and does not allow a phantom target', async () => {
  vi.mocked(k8sApi.resources).mockRejectedValue({
    response: { data: { detail: '集群 local 凭据解密失败' } },
  })
  const { screen } = await setup({ copyFrom: binding })
  await userEvent.click(
    screen.getByRole('button', { name: '下一步：选择部署目标' })
  )
  await userEvent.selectOptions(screen.getByLabelText('部署到哪个集群'), '7')
  await expect
    .element(screen.getByText('无法读取集群资源，请确认连接和读取权限。'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('命名空间：集群 local 凭据解密失败'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('Deployment：集群 local 凭据解密失败'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByLabelText('要更新哪个 Deployment'))
    .toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  expect(platformApi.previewApp).not.toHaveBeenCalled()
  expect(platformApi.createApp).not.toHaveBeenCalled()
})

it('keeps the wizard open when the target disappears before saving', async () => {
  vi.mocked(platformApi.createApp).mockRejectedValue({
    response: { data: { detail: '目标 Deployment 不存在' } },
  })
  const { screen, onClose, onSaved } = await setup({ copyFrom: binding })
  await chooseTarget(screen)
  await userEvent.click(screen.getByRole('button', { name: '检查绑定并继续' }))
  await expect.element(screen.getByRole('checkbox')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('checkbox'))
  await userEvent.click(screen.getByRole('button', { name: '保存组件绑定' }))
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent('目标 Deployment 不存在')
  expect(onClose).not.toHaveBeenCalled()
  expect(onSaved).not.toHaveBeenCalled()
})
