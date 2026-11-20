import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import {
  runnerApi,
  type ComponentBinding,
  type RunnerBinding,
  type RunnerScripts,
} from '@/api/client'
import { RunnerOnboarding } from './runner-onboarding'

vi.mock('@/stores/auth-store', () => ({
  useAuthStore: (select: (s: unknown) => unknown) =>
    select({ auth: { user: { role: 'admin' } } }),
}))
vi.mock('@/api/client', async (original) => ({
  ...(await original<typeof import('@/api/client')>()),
  runnerApi: {
    bindings: vi.fn(),
    configure: vi.fn(),
    scripts: vi.fn(),
    connections: vi.fn(),
    searchDeploymentPipelines: vi.fn(),
    createConnection: vi.fn(),
    enabled: vi.fn(),
    preview: vi.fn(),
    apply: vi.fn(),
    backup: vi.fn(),
  },
}))
afterEach(async () => {
  document.documentElement.classList.remove('dark')
  await page.viewport(1280, 800)
})
const app: ComponentBinding = {
  id: 1,
  cluster_id: 1,
  app_name: 'orders',
  source_kind: 'unknown',
  match_keyword: 'orders',
  namespace: 'default',
  deployment_name: 'orders-api',
  container_name: 'api',
  image_repository: 'reg.io/orders/api',
  source_repo_url: '',
  source_path: '',
  harbor_project: 'orders',
  created_at: '',
}
const binding: RunnerBinding = {
  id: 1,
  app_id: 1,
  pipeline_id: '123',
  private_cluster_id: 'build-1',
  platform_url: 'http://deploy.internal',
  connection_id: 0,
  binding: app,
  env_code: 'test',
  token_prefix: 'dpci_prefix',
  enabled: true,
  expires_at: '2027-01-01T00:00:00Z',
  created_at: '',
  updated_at: '',
}
const scripts: RunnerScripts = {
  script: 'set +x\necho notify',
  check_script: 'echo read-only check',
  verification_script: 'set +x\necho report verification',
  configuration_variables: [
    'DEPLOY_CONFIG_REPOSITORY',
    'DEPLOY_CONFIG_REVISION',
    'DEPLOY_CONFIG_PATH',
  ],
  task_yaml: 'stages:\n  notify: {}',
  release_id_file: 'deploy-release-id.txt',
  secret_variable: 'DEPLOY_RUNNER_TOKEN_1',
  required_variables: [
    'DEPLOY_RUNNER_TOKEN_1',
    'DEPLOY_IMAGE_FULL',
    'DEPLOY_IMAGE_DIGEST',
  ],
  verification_required_variables: [
    'DEPLOY_RUNNER_TOKEN_1',
    'DEPLOY_EVIDENCE_KIND',
  ],
}
beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(runnerApi.bindings).mockResolvedValue({ data: [] } as never)
  vi.mocked(runnerApi.connections).mockResolvedValue({ data: [] } as never)
  vi.mocked(runnerApi.configure).mockResolvedValue({
    data: { binding, key: 'dpci_once_only_secret', scripts },
  } as never)
  vi.mocked(runnerApi.scripts).mockResolvedValue({ data: scripts } as never)
  vi.mocked(runnerApi.preview).mockResolvedValue({
    data: {
      before: 'stages: {}',
      after: scripts.task_yaml,
      name: 'Orders build',
      fingerprint: 'original',
      change_hash: 'generated',
      already_installed: false,
      note: '保存前核对配置',
    },
  } as never)
  vi.mocked(runnerApi.apply).mockResolvedValue({
    data: { saved: true, changed: true },
  } as never)
})
async function setup(initial?: RunnerBinding, component = app) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const close = vi.fn()
  const screen = await render(
    <QueryClientProvider client={client}>
      <RunnerOnboarding
        app={component}
        initial={initial ? { binding: initial } : undefined}
        onClose={close}
      />
    </QueryClientProvider>
  )
  return { screen, close }
}

it('verifies the registered configuration checkout and derives its revision', async () => {
  const component = {
    ...app,
    deployment_config_repo_url:
      'https://git.example.com/orders/deploy-config.git',
    deployment_config_path: 'charts/orders',
  }
  const mappedScripts = {
    ...scripts,
    configuration_variables: ['DEPLOY_CONFIG_WORKDIR'],
  }
  vi.mocked(runnerApi.configure).mockResolvedValue({
    data: { binding, key: 'dpci_once_only_secret', scripts: mappedScripts },
  } as never)
  vi.mocked(runnerApi.scripts).mockResolvedValue({
    data: mappedScripts,
  } as never)
  const { screen } = await setup(undefined, component)
  await userEvent.fill(screen.getByLabelText('云效流水线 ID'), '123')
  await userEvent.fill(screen.getByLabelText('私有构建集群 ID'), 'build-1')
  await userEvent.fill(
    screen.getByLabelText('Runner 能访问的平台地址'),
    'http://deploy.internal'
  )
  await userEvent.click(
    screen.getByRole('button', { name: '生成组件接入配置' })
  )
  await expect
    .element(
      screen.getByText('https://git.example.com/orders/deploy-config.git')
    )
    .toBeInTheDocument()
  await expect.element(screen.getByText('charts/orders')).toBeInTheDocument()
  await expect
    .element(screen.getByText(/通知任务必填变量/))
    .not.toHaveTextContent('DEPLOY_CONFIG_REVISION')
  await expect
    .element(screen.getByText('DEPLOY_CONFIG_WORKDIR'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('DEPLOY_CONFIG_REPOSITORY'))
    .not.toBeInTheDocument()
  await expect
    .element(screen.getByText(/核对 Git origin、登记目录和实际 HEAD/))
    .toBeInTheDocument()
  await page.viewport(390, 844)
  const configNote = screen.getByText(/默认使用通知任务当前目录/).element()
  const onboardingScrollContent = screen
    .getByTestId('runner-onboarding-scroll-content')
    .element()
  onboardingScrollContent.scrollTop +=
    configNote.getBoundingClientRect().top -
    onboardingScrollContent.getBoundingClientRect().top -
    20
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-390-component-config-light.png',
    element: screen.getByRole('dialog', { name: /接入云效 Runner/ }).element(),
  })
})

it('searches and selects the exact Yunxiao build pipeline for a component', async () => {
  vi.mocked(runnerApi.connections).mockResolvedValue({
    data: [
      {
        id: 9,
        name: 'Yunxiao Prod',
        edition: 'central',
        domain: 'openapi-rdc.aliyuncs.com',
        organization_id: 'org-1',
        created_at: '',
      },
    ],
  } as never)
  vi.mocked(runnerApi.searchDeploymentPipelines).mockResolvedValue({
    data: {
      items: [{ pipeline_id: 123456, name: 'orders-build-and-push' }],
      page: 1,
      per_page: 30,
      total: 1,
      total_pages: 1,
    },
  } as never)
  const component = {
    ...app,
    source_kind: 'repository' as const,
    source_repo_url: 'https://git.example.com/team/orders.git',
    source_path: 'services/api',
  }
  const { screen } = await setup(undefined, component)
  await userEvent.selectOptions(
    screen.getByLabelText('云效连接（可选，自动写入流水线时使用）'),
    '9'
  )
  await userEvent.fill(
    screen.getByLabelText('按名称查找构建流水线'),
    'orders-build'
  )
  await userEvent.click(screen.getByRole('button', { name: '搜索流水线' }))
  await expect
    .element(screen.getByRole('option', { name: /orders-build-and-push/ }))
    .toBeInTheDocument()
  expect(runnerApi.searchDeploymentPipelines).toHaveBeenCalledWith({
    connection_id: 9,
    name: 'orders-build',
    page: 1,
  })
  await userEvent.selectOptions(
    screen.getByLabelText('搜索结果 · 第 1 页 / 共 1 条'),
    '123456'
  )
  await expect
    .element(screen.getByLabelText('云效流水线 ID'))
    .toHaveValue('123456')
  const mappingPreview = screen.getByRole('region', {
    name: 'Runner 绑定预览',
  })
  await expect.element(mappingPreview).toHaveTextContent('Yunxiao Prod')
  await expect
    .element(mappingPreview)
    .toHaveTextContent('orders-build-and-push')
  await expect.element(mappingPreview).toHaveTextContent('orders')
  await expect.element(mappingPreview).toHaveTextContent('services/api')
  await expect.element(mappingPreview).toHaveTextContent('reg.io/orders/api')
  await userEvent.fill(screen.getByLabelText('私有构建集群 ID'), 'build-1')
  await page.viewport(390, 844)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  screen
    .getByLabelText('搜索结果 · 第 1 页 / 共 1 条')
    .element()
    .scrollIntoView({ block: 'center' })
  await new Promise((resolve) => setTimeout(resolve, 100))
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-pipeline-picker-390-light.png',
    element: screen.getByRole('dialog', { name: /接入云效 Runner/ }).element(),
  })
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-pipeline-mapping-390-light.png',
    element: mappingPreview.element(),
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-pipeline-picker-390-dark.png',
    element: screen.getByRole('dialog', { name: /接入云效 Runner/ }).element(),
  })
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-pipeline-mapping-390-dark.png',
    element: mappingPreview.element(),
  })
  document.documentElement.classList.remove('dark')
  await userEvent.click(
    screen.getByRole('button', { name: '生成组件接入配置' })
  )
  expect(runnerApi.configure).toHaveBeenCalledWith(
    expect.objectContaining({
      app_id: 1,
      pipeline_id: '123456',
      connection_id: 9,
    }),
    undefined
  )
})

it('generates a component credential and separates the check from publishing', async () => {
  const { screen, close } = await setup()
  await expect
    .element(screen.getByRole('button', { name: '取消接入' }))
    .toBeVisible()
  const runnerDialog = screen
    .getByRole('dialog', { name: /接入云效 Runner/ })
    .element()
  await page.viewport(1440, 1000)
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-1440-step0.png',
    element: runnerDialog,
  })
  await page.viewport(390, 844)
  const compactStepLabels = ['1. 配置', '2. 流水线', '3. 验证']
  for (const label of compactStepLabels) {
    const stepLabel = screen.getByText(label, { exact: true }).element()
    const stepButton = stepLabel.closest('button')!
    expect(stepButton.scrollWidth).toBeLessThanOrEqual(stepButton.clientWidth)
  }
  await expect
    .element(screen.getByRole('button', { name: '1. 接入配置' }))
    .toHaveAttribute('aria-current', 'step')
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-390-step0-light.png',
    element: runnerDialog,
  })
  await page.viewport(1280, 800)
  await userEvent.fill(screen.getByLabelText('云效流水线 ID'), '123')
  await userEvent.fill(screen.getByLabelText('私有构建集群 ID'), 'build-1')
  await userEvent.fill(
    screen.getByLabelText('Runner 能访问的平台地址'),
    'http://deploy.internal'
  )
  await userEvent.click(
    screen.getByRole('button', { name: '生成组件接入配置' })
  )
  await expect
    .element(screen.getByRole('button', { name: '关闭引导' }))
    .toBeVisible()
  await expect
    .element(screen.getByRole('button', { name: '完成' }))
    .not.toBeInTheDocument()
  await page.viewport(1440, 1000)
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-1440-step1.png',
    element: runnerDialog,
  })
  await page.viewport(1024, 768)
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-1024-step1.png',
    element: runnerDialog,
  })
  await page.viewport(512, 384)
  await expect
    .element(screen.getByRole('button', { name: '下一步：检查接入' }))
    .toBeVisible()
  await expect
    .element(screen.getByRole('heading', { name: /接入云效 Runner/ }))
    .toBeVisible()
  await expect
    .element(screen.getByRole('button', { name: '2. 流水线任务' }))
    .toBeVisible()
  const compactDialogRect = runnerDialog.getBoundingClientRect()
  expect(compactDialogRect.left).toBeGreaterThanOrEqual(0)
  expect(compactDialogRect.right).toBeLessThanOrEqual(512)
  expect(compactDialogRect.top).toBeGreaterThanOrEqual(0)
  expect(compactDialogRect.bottom).toBeLessThanOrEqual(384)
  const compactBody = screen
    .getByTestId('runner-onboarding-scroll-content')
    .element()
  expect(compactBody.scrollHeight).toBeGreaterThan(compactBody.clientHeight)
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-512x384-step1.png',
    element: runnerDialog,
  })
  await page.viewport(390, 844)
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-390-step1-light.png',
    element: runnerDialog,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-390-step1-dark.png',
    element: runnerDialog,
  })
  document.documentElement.classList.remove('dark')
  const configSourceNote = screen
    .getByText(/如构建使用独立 Helm\/Git 配置仓库/)
    .element()
  const onboardingScrollContent = screen
    .getByTestId('runner-onboarding-scroll-content')
    .element()
  onboardingScrollContent.scrollTop +=
    configSourceNote.getBoundingClientRect().top -
    onboardingScrollContent.getBoundingClientRect().top -
    24
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-390-config-source-light.png',
    element: runnerDialog,
  })
  await page.viewport(390, 844)
  await new Promise((resolve) => setTimeout(resolve, 250))
  const scrollContent = screen
    .getByTestId('runner-onboarding-scroll-content')
    .element()
  scrollContent.scrollTop = scrollContent.scrollHeight
  await expect
    .element(screen.getByRole('button', { name: '下一步：检查接入' }))
    .toBeEnabled()
  await page.screenshot({
    path: '../../../../docs/visual-checks/runner-onboarding-390-step1-scroll-light.png',
    element: runnerDialog,
  })
  scrollContent.scrollTop = 0
  await page.viewport(1280, 800)
  await expect
    .element(screen.getByRole('button', { name: '下一步：检查接入' }))
    .toBeVisible()
  scrollContent.scrollTop = scrollContent.scrollHeight
  await expect
    .element(screen.getByRole('button', { name: '下一步：检查接入' }))
    .toBeVisible()
  await expect
    .element(screen.getByRole('heading', { name: /接入云效 Runner/ }))
    .toBeVisible()
  expect(scrollContent.scrollTop).toBeGreaterThan(0)
  await expect
    .element(screen.getByLabelText('组件专属接入密钥'))
    .toHaveValue('dpci_once_only_secret')
  await expect
    .element(screen.getByText('runner:1', { exact: true }))
    .toBeVisible()
  await expect
    .element(screen.getByText('此流水线必须产出的镜像仓库'))
    .toBeVisible()
  await expect.element(screen.getByText('default/orders-api:api')).toBeVisible()
  expect(runnerApi.configure).toHaveBeenCalledWith(
    expect.objectContaining({ app_id: 1, pipeline_id: '123' }),
    undefined
  )
  await expect
    .element(screen.getByLabelText('云效通知任务配置'))
    .toHaveValue(scripts.task_yaml)
  await expect
    .element(screen.getByLabelText('Runner CI、Smoke 与扫描证据脚本'))
    .toHaveValue(scripts.verification_script)
  await expect
    .element(screen.getByText('CI / Smoke / Scan 证据上报'))
    .toBeVisible()
  await expect.element(screen.getByText('安全扫描')).toBeVisible()
  await userEvent.click(
    screen.getByRole('button', { name: '下一步：检查接入' })
  )
  await expect
    .element(screen.getByLabelText('Runner 接入测试脚本'))
    .toHaveValue(scripts.check_script)
  await expect
    .element(screen.getByText('尚未创建发布任务', { exact: false }))
    .toBeInTheDocument()
  expect(runnerApi.apply).not.toHaveBeenCalled()
  await expect
    .element(screen.getByRole('button', { name: '关闭引导' }))
    .toBeVisible()
  await userEvent.click(screen.getByRole('button', { name: '关闭引导' }))
  expect(close).toHaveBeenCalledOnce()
})
it('requires confirmation of the generated cloud diff before applying', async () => {
  const { screen } = await setup({ ...binding, connection_id: 3 })
  await expect
    .element(screen.getByLabelText('云效通知任务配置'))
    .toHaveValue(scripts.task_yaml)
  await userEvent.click(
    screen.getByRole('button', { name: '读取并预览流水线变更' })
  )
  await expect
    .element(screen.getByLabelText('现有流水线'))
    .toHaveValue('stages: {}')
  await expect
    .element(screen.getByRole('button', { name: '保存通知任务到云效' }))
    .toBeDisabled()
  await userEvent.click(screen.getByRole('checkbox'))
  await userEvent.click(
    screen.getByRole('button', { name: '保存通知任务到云效' })
  )
  expect(runnerApi.apply).toHaveBeenCalledWith(1, {
    fingerprint: 'original',
    change_hash: 'generated',
  })
})
it('drops a stale preview and keeps the wizard open on apply failure', async () => {
  vi.mocked(runnerApi.apply).mockRejectedValue({
    response: { data: { detail: '云效流水线已变化，请重新预览' } },
  })
  const { screen, close } = await setup({ ...binding, connection_id: 3 })
  await userEvent.click(
    screen.getByRole('button', { name: '读取并预览流水线变更' })
  )
  await userEvent.click(screen.getByRole('checkbox'))
  await userEvent.click(
    screen.getByRole('button', { name: '保存通知任务到云效' })
  )
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent('云效流水线已变化，请重新预览')
  await expect
    .element(screen.getByLabelText('现有流水线'))
    .not.toBeInTheDocument()
  expect(close).not.toHaveBeenCalled()
})
it('never recovers an existing plaintext key on reopening', async () => {
  const { screen } = await setup(binding)
  await expect
    .element(screen.getByText('已有密钥不会回显。遗失时返回第一步重新生成。'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByLabelText('组件专属接入密钥'))
    .not.toBeInTheDocument()
  expect(runnerApi.configure).not.toHaveBeenCalled()
})
it('shows check success independently of a failed rollout', async () => {
  vi.mocked(runnerApi.bindings).mockResolvedValue({
    data: [
      {
        binding: { ...binding, last_check_at: '2026-10-08T03:00:00Z' },
        last_release_id: 42,
        last_event_at: '2026-10-08T03:01:00Z',
        last_status: 'failed',
        last_image: 'reg.io/orders/api:v1',
        last_error: 'rollout failed',
      },
    ],
  } as never)
  const { screen } = await setup(binding)
  await userEvent.click(screen.getByRole('button', { name: '3. 检查与验证' }))
  await expect.element(screen.getByText('接入测试通过')).toBeInTheDocument()
  await expect.element(screen.getByText('rollout failed')).toBeInTheDocument()
  await expect.element(screen.getByText('发布状态：failed')).toBeInTheDocument()
  await expect.element(screen.getByText('最近发布单：#42')).toBeInTheDocument()
})

it('keeps the evidence handoff readable on a narrow dark screen', async () => {
  await page.viewport(390, 844)
  document.documentElement.classList.add('dark')
  const { screen } = await setup(binding)
  await expect
    .element(screen.getByLabelText('Runner CI、Smoke 与扫描证据脚本'))
    .toBeVisible()
  await expect.element(screen.getByText(/通过云效产物传递机制/)).toBeVisible()
  await expect.element(screen.getByText(/只记录报告，不阻断发布/)).toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
})
