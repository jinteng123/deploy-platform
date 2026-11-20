import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import { platformApi, runnerApi, type ImageReleaseView } from '@/api/client'
import {
  evidenceForCurrentImage,
  evidenceStatus,
  safeReportURL,
} from './environment-evidence'
import { EnvironmentsPage } from './environments-page'

async function captureVisualCheck(
  options: NonNullable<Parameters<typeof page.screenshot>[0]>
) {
  if (
    import.meta.env.VITE_CAPTURE_VISUAL_CHECKS === '1' &&
    (options.path?.includes('config-pipeline-') ||
      options.path?.includes('canary-gateway-integration-checklist-') ||
      options.path?.includes('environment-binding-preview-'))
  ) {
    await page.screenshot(options)
  }
}

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
    deliveryModel: vi.fn(),
    apps: vi.fn(),
    clusters: vi.fn(),
    deliveryDrift: vi.fn(),
    logs: vi.fn(),
    releases: vi.fn(),
    rebindDeliveryWorkload: vi.fn(),
    deliveryPolicy: vi.fn(),
    assignDeliveryBinding: vi.fn(),
    createDeliveryPolicyVersion: vi.fn(),
    createDeliveryApplication: vi.fn(),
    createDeliveryComponent: vi.fn(),
  },
  runnerApi: {
    ...(await importOriginal<typeof import('@/api/client')>()).runnerApi,
    connections: vi.fn(),
    searchDeploymentPipelines: vi.fn(),
    inspectDeploymentPipeline: vi.fn(),
    inspectDeploymentPipelineScope: vi.fn(),
    reviewDeploymentPipelineScope: vi.fn(),
  },
}))

it('associates evidence only with the running digest and its target cluster', () => {
  const digest = `sha256:${'a'.repeat(64)}`
  const evidence = {
    digest,
    status: 'passed',
    kind: 'ci',
    report_url: '',
    summary: '',
    recorded_at: '2026-10-09T12:00:00Z',
    valid_until: '2026-10-10T12:00:00Z',
  } as const
  const app = { app_name: 'orders', cluster_id: 2 } as never
  const release = {
    app_name: 'orders',
    image_digest: digest,
    test_cluster_id: 2,
    metadata: { verification: evidence },
  } as ImageReleaseView

  expect(
    evidenceForCurrentImage(
      [release],
      app,
      `registry.example.com/orders/api@${digest}`,
      'verification'
    )
  ).toEqual(evidence)
  expect(
    evidenceForCurrentImage(
      [release],
      app,
      `registry.example.com/orders/api@sha256:${'b'.repeat(64)}`,
      'verification'
    )
  ).toBeUndefined()
  expect(
    evidenceForCurrentImage(
      [{ ...release, test_cluster_id: 3 }],
      app,
      `registry.example.com/orders/api@${digest}`,
      'verification'
    )
  ).toBeUndefined()
  expect(
    evidenceStatus(
      evidence,
      'registry.example.com/orders/api:latest',
      false,
      false
    ).label
  ).toBe('无法精确关联')
  expect(
    evidenceStatus(
      { ...evidence, valid_until: '2020-01-01T00:00:00Z' },
      `registry.example.com/orders/api@${digest}`,
      false,
      false
    ).label
  ).toBe('已过期')
  expect(safeReportURL('https://ci.example.com/report/81')).toBe(
    'https://ci.example.com/report/81'
  )
  expect(safeReportURL('javascript:alert(1)')).toBeUndefined()
  expect(
    safeReportURL('https://user:password@ci.example.com/report')
  ).toBeUndefined()
})

it('keeps first-time component mapping in the guided flow while creating logical records', async () => {
  vi.clearAllMocks()
  const target = {
    id: 42,
    cluster_id: 2,
    app_name: 'order-api',
    source_kind: 'repository',
    source_repo_url: 'https://git.example.com/orders.git',
    source_path: 'services/order-api',
    namespace: 'orders-test',
    deployment_name: 'orders-api',
    container_name: 'api',
    image_repository: 'registry.example.com/orders/api',
    harbor_project: 'orders',
    match_keyword: 'order-api',
    created_at: '',
  }
  const environment = {
    id: 21,
    code: 'test-west',
    name: '测试环境',
    environment_tier: 'non_production',
    requires_review: false,
    created_at: '',
  }
  const emptyModel = {
    applications: [],
    components: [],
    environments: [environment],
    bindings: [],
  }
  const application = {
    id: 11,
    name: '订单系统',
    requires_review: false,
    created_at: '',
  }
  const modelWithApplication = {
    ...emptyModel,
    applications: [application],
  }
  const component = {
    id: 12,
    application_id: application.id,
    name: 'order-api',
    requires_review: false,
    created_at: '',
  }
  const modelWithComponent = {
    ...modelWithApplication,
    components: [component],
  }
  vi.mocked(platformApi.deliveryModel)
    .mockResolvedValueOnce({ data: emptyModel } as never)
    .mockResolvedValueOnce({ data: modelWithApplication } as never)
    .mockResolvedValue({ data: modelWithComponent } as never)
  vi.mocked(platformApi.apps).mockResolvedValue({ data: [target] } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [
      {
        id: 2,
        name: 'test-west',
        environment_tier: 'non_production',
        enabled: true,
      },
    ],
  } as never)
  vi.mocked(runnerApi.connections).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.createDeliveryApplication).mockResolvedValue({
    data: application,
  } as never)
  vi.mocked(platformApi.createDeliveryComponent).mockResolvedValue({
    data: component,
  } as never)
  vi.mocked(platformApi.assignDeliveryBinding).mockResolvedValue({
    data: { id: 91 },
  } as never)

  await page.viewport(390, 844)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <EnvironmentsPage initialAppId={target.id} />
    </QueryClientProvider>
  )
  const dialog = screen.getByRole('dialog', { name: '核对组件归属与环境' })
  await expect.element(dialog).toBeInTheDocument()
  await expect
    .element(
      screen.getByText(
        '请为此 APP_NAME 选择业务应用、发布组件和逻辑环境；平台不会按名称自动猜测。'
      )
    )
    .toBeInTheDocument()
  const createApplicationButton = screen.getByRole('button', {
    name: '创建应用',
  })
  const createApplicationBounds = createApplicationButton
    .element()
    .getBoundingClientRect()
  const newApplicationInputBounds = screen
    .getByLabelText('新业务应用名称')
    .element()
    .getBoundingClientRect()
  const createApplicationFooter = dialog
    .element()
    .querySelector('[data-slot="dialog-footer"]')
  expect(createApplicationBounds.height).toBeGreaterThanOrEqual(44)
  expect(newApplicationInputBounds.height).toBeGreaterThanOrEqual(44)
  expect(createApplicationBounds.bottom).toBeLessThanOrEqual(
    createApplicationFooter!.getBoundingClientRect().top
  )
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-binding-no-app-390-light.png',
    element: dialog.element(),
  })

  await userEvent.fill(screen.getByLabelText('新业务应用名称'), '订单系统')
  await userEvent.click(screen.getByRole('button', { name: '创建应用' }))
  await expect
    .poll(
      () => vi.mocked(platformApi.createDeliveryApplication).mock.calls.length
    )
    .toBe(1)
  await expect
    .element(screen.getByRole('option', { name: '订单系统 · 新建' }))
    .toBeInTheDocument()
  expect(platformApi.createDeliveryApplication).toHaveBeenCalledWith('订单系统')

  await userEvent.fill(screen.getByLabelText('新组件名称'), 'order-api')
  await userEvent.click(screen.getByRole('button', { name: '创建组件' }))
  await expect
    .poll(
      () => vi.mocked(platformApi.createDeliveryComponent).mock.calls.length
    )
    .toBe(1)
  await expect
    .element(screen.getByRole('option', { name: 'order-api · 新建' }))
    .toBeInTheDocument()
  expect(platformApi.createDeliveryComponent).toHaveBeenCalledWith(
    application.id,
    'order-api'
  )

  await userEvent.selectOptions(
    screen.getByLabelText('逻辑环境'),
    String(environment.id)
  )
  const relationship = screen
    .getByText('保存后将建立的对应关系')
    .element().parentElement
  expect(relationship?.textContent).toContain('订单系统 / order-api')
  expect(relationship?.textContent).toContain('测试环境')
  await userEvent.click(screen.getByRole('button', { name: '保存绑定与策略' }))
  await expect
    .poll(() => vi.mocked(platformApi.assignDeliveryBinding).mock.calls.length)
    .toBe(1)
  expect(platformApi.assignDeliveryBinding).toHaveBeenCalledWith(target.id, {
    application_id: application.id,
    component_id: component.id,
    environment_id: environment.id,
    strategy_draft: '',
    service_name: '',
    expected_policy_version: 0,
    strategy: 'rolling',
    candidate_retention_hours: 0,
    deployment_pipeline: { connection_id: 0, pipeline_id: '' },
  })
  vi.clearAllMocks()
  client.clear()
})

it('shows the active workload UID separately from its original template', async () => {
  vi.mocked(platformApi.deliveryModel).mockResolvedValue({
    data: {
      environments: [
        {
          id: 1,
          code: 'prod',
          name: '生产',
          environment_tier: 'production',
          requires_review: false,
          created_at: '',
        },
      ],
      applications: [
        { id: 1, name: '订单系统', requires_review: false, created_at: '' },
      ],
      components: [
        {
          id: 1,
          application_id: 1,
          name: 'orders-api',
          requires_review: false,
          created_at: '',
        },
      ],
      bindings: [
        {
          id: 1,
          app_id: 2,
          application_id: 1,
          component_id: 1,
          environment_id: 1,
          deployment_pipeline: { connection_id: 9, pipeline_id: '123456' },
          template_workload_ref: {
            cluster_id: 2,
            namespace: 'production',
            kind: 'Deployment',
            name: 'orders-api-template',
            uid: 'template-uid-1234',
          },
          active_workload_ref: {
            cluster_id: 2,
            namespace: 'production',
            kind: 'Deployment',
            name: 'orders-api-plan-8',
            uid: 'active-uid-5678',
          },
          requires_review: false,
          created_at: '',
        },
      ],
      pipeline_scope_reviews: [
        {
          id: 61,
          binding_id: 1,
          connection_id: 9,
          pipeline_id: '123456',
          definition_hash: 'a'.repeat(64),
          write_scope: 'unknown',
          reviewer: 'admin',
          reviewed_at: '2026-10-14T12:00:00Z',
        },
      ],
    },
  } as never)
  vi.mocked(platformApi.apps).mockResolvedValue({
    data: [
      {
        id: 2,
        cluster_id: 2,
        app_name: 'orders',
        source_kind: 'repository',
        source_repo_url: 'https://git.example.com/orders.git',
        source_path: 'services/api',
        deployment_config_repo_url:
          'https://git.example.com/orders/deploy-config.git',
        deployment_config_path: 'charts/orders',
        namespace: 'production',
        deployment_name: 'orders-api-plan-8',
        container_name: 'api',
        image_repository: 'registry.example.com/orders/api',
        harbor_project: 'orders',
        match_keyword: 'orders',
        created_at: '',
      },
      {
        id: 3,
        cluster_id: 99,
        app_name: 'unbound-example',
        source_kind: 'repository',
        source_repo_url: 'https://git.example.com/legacy.git',
        source_path: '.',
        namespace: 'default',
        deployment_name: 'legacy',
        container_name: 'app',
        image_repository: 'registry.example.com/legacy',
        harbor_project: 'legacy',
        match_keyword: 'legacy',
        created_at: '',
      },
    ],
  } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [
      {
        id: 2,
        name: 'prod-west',
        environment_tier: 'production',
        enabled: true,
      },
    ],
  } as never)
  vi.mocked(platformApi.rebindDeliveryWorkload).mockResolvedValue({
    data: { id: 1 },
  } as never)
  vi.mocked(platformApi.deliveryPolicy).mockResolvedValue({
    data: {
      binding_id: 1,
      current_version: 2,
      versions: [
        {
          binding_id: 1,
          version: 2,
          strategy: 'rolling',
          readiness: 'configurable',
          created_by: 'admin',
          created_at: '',
        },
      ],
    },
  } as never)
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
      items: [{ pipeline_id: 123456, name: 'orders-config-deploy' }],
      page: 1,
      per_page: 30,
      total: 1,
      total_pages: 1,
    },
  } as never)
  vi.mocked(runnerApi.inspectDeploymentPipeline).mockResolvedValue({
    data: {
      pipeline_id: '123456',
      name: 'orders-config-deploy',
      type: 'PIPELINEASCODE',
      update_time: 1770000000000,
    },
  } as never)
  vi.mocked(runnerApi.inspectDeploymentPipelineScope).mockResolvedValue({
    data: {
      pipeline_id: '123456',
      name: 'orders-config-deploy',
      type: 'PIPELINEASCODE',
      update_time: 1770000000000,
      definition_hash: 'a'.repeat(64),
      analysis_status: 'manual_review_required',
      review_required: true,
      findings: [
        {
          node_ref: '流水线 / 阶段 1 / 任务 2 / 步骤 1',
          category: 'possible_config_write',
          summary: '检测到 ConfigMap/Secret 相关写入标记；需核对最终目标。',
        },
        {
          node_ref: '流水线 / 阶段 1 / 任务 3',
          category: 'opaque_task',
          summary: '检测到云效任务模板；模板内部动作和权限需在云效中单独核对。',
        },
      ],
      unresolved_nodes: ['流水线 / 阶段 1 / 任务 2 / 步骤 1'],
      warning:
        '静态检查只用于定位疑似步骤，无法证明流水线完整写入范围。请在云效核对引用模板和实际执行身份。',
    },
  } as never)
  vi.mocked(runnerApi.reviewDeploymentPipelineScope).mockResolvedValue({
    data: {
      review: {
        id: 77,
        binding_id: 1,
        connection_id: 9,
        pipeline_id: '123456',
        definition_hash: 'a'.repeat(64),
        write_scope: 'unknown',
        reviewer: 'admin',
        reviewed_at: '2026-10-15T12:00:00Z',
      },
      execution_enabled: false,
      message: '核对结论已记录；这不会开放或触发配置流水线。',
    },
  } as never)
  vi.mocked(platformApi.assignDeliveryBinding).mockResolvedValue({
    data: { id: 1 },
  } as never)
  const actualImage =
    'registry.example.com/orders/api@sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789'
  const validUntil = new Date(Date.now() + 60 * 60 * 1000).toISOString()
  vi.mocked(platformApi.deliveryDrift).mockResolvedValue({
    data: [
      {
        app_name: 'orders',
        cluster_id: 2,
        namespace: 'production',
        workload: 'orders-api-plan-8',
        container: 'api',
        status: 'in_sync',
        expected_image: actualImage,
        actual_image: actualImage,
        observed_at: '2026-10-09T12:00:00Z',
      },
    ],
  } as never)
  vi.mocked(platformApi.logs).mockResolvedValue({
    data: [
      {
        id: 71,
        cluster_id: 2,
        app_name: 'orders',
        deployment_name: 'orders-api-plan-8',
        container_name: 'api',
        namespace: 'production',
        version: '1.2.3',
        status: 'success',
        deploy_type: 'image_deploy',
        created_at: '2026-10-09T12:00:00Z',
        finished_at: '2026-10-09T12:05:00Z',
        image_full: actualImage,
      },
    ],
  } as never)
  vi.mocked(platformApi.releases).mockResolvedValue({
    data: [
      {
        id: 81,
        event_id: 'build-81',
        source: 'yunxiao',
        app_name: 'orders',
        image_full: actualImage,
        image_digest:
          'sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789',
        version: '1.2.3',
        test_cluster_id: 2,
        test_deploy_log_id: 71,
        created_at: '2026-10-09T12:00:00Z',
        updated_at: '2026-10-09T12:05:00Z',
        test_status: 'success',
        metadata: {
          digest_source: 'yunxiao',
          verification: {
            digest:
              'sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789',
            status: 'passed',
            kind: 'ci',
            report_url: 'https://ci.example.com/build/81',
            summary: '单元与集成测试通过',
            recorded_by: 'Yunxiao',
            recorded_at: '2026-10-09T12:04:00Z',
            valid_until: validUntil,
          },
          scan: {
            digest:
              'sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789',
            status: 'passed',
            kind: 'scan',
            report_url: 'https://scan.example.com/report/81',
            summary: '未发现高危漏洞',
            recorded_by: 'Harbor',
            recorded_at: '2026-10-09T12:04:00Z',
            valid_until: validUntil,
          },
        },
      },
    ],
  } as never)

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  await page.viewport(390, 844)
  const screen = await render(
    <QueryClientProvider client={client}>
      <EnvironmentsPage initialAppId={2} />
    </QueryClientProvider>
  )
  await expect
    .element(screen.getByText(/活动 Deployment · orders-api-plan-8/).first())
    .toBeVisible()
  await expect.element(screen.getByText(/UID active-u/).first()).toBeVisible()
  await expect
    .element(
      screen.getByText(/初始资源 · orders-api-template · UID template/).first()
    )
    .toBeVisible()
  await expect.element(screen.getByText('镜像一致')).toBeInTheDocument()
  await expect
    .element(
      screen.getByText('registry.example.com/orders/api@sha256:abcdef012345…')
    )
    .toBeInTheDocument()
  await expect.element(screen.getByText('成功 · 1.2.3')).toBeInTheDocument()
  await expect.element(screen.getByText('业务验证 · 通过')).toBeInTheDocument()
  await expect.element(screen.getByText('安全扫描 · 通过')).toBeInTheDocument()
  await expect.element(screen.getByText(/本地观察/)).toBeInTheDocument()
  await expect.element(screen.getByText('查看完整镜像对比')).toBeInTheDocument()
  expect(platformApi.deliveryDrift).toHaveBeenCalledTimes(1)
  expect(platformApi.deliveryDrift).toHaveBeenCalledWith(2, [2])
  expect(platformApi.logs).toHaveBeenCalledTimes(1)
  expect(platformApi.logs).toHaveBeenCalledWith({
    cluster_id: 2,
    app_name: 'orders',
  })
  expect(platformApi.releases).toHaveBeenCalledWith({
    app_name: 'orders',
    limit: 50,
  })
  await expect
    .element(screen.getByRole('dialog', { name: '核对组件归属与环境' }))
    .toBeInTheDocument()
  const bindingIdentity = screen.getByTestId('delivery-binding-identity')
  await expect
    .element(bindingIdentity.getByText('orders', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(
      bindingIdentity.getByText(
        'https://git.example.com/orders.git · services/api',
        { exact: true }
      )
    )
    .toBeInTheDocument()
  await expect
    .element(
      bindingIdentity.getByText('registry.example.com/orders/api', {
        exact: true,
      })
    )
    .toBeInTheDocument()
  await expect
    .element(
      bindingIdentity.getByText(
        'prod-west / production / orders-api-plan-8 / api · 生产目标',
        { exact: true }
      )
    )
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('构建来源 · APP_NAME / 镜像仓库'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('业务归属 · 逻辑环境'))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(
        /更新位置：prod-west \/ production \/ orders-api-plan-8 \/ api/
      )
    )
    .toBeInTheDocument()
  const bindingDialog = screen
    .getByRole('dialog', { name: '核对组件归属与环境' })
    .element()
  const bindingScrollContent = screen
    .getByTestId('delivery-binding-scroll-content')
    .element()
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-binding-identity-390-light.png',
    element: bindingDialog,
  })
  const relationshipPreview = screen
    .getByText('保存后将建立的对应关系')
    .element()
  bindingScrollContent.scrollTop =
    relationshipPreview.getBoundingClientRect().top -
    bindingScrollContent.getBoundingClientRect().top
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-binding-preview-390-light.png',
    element: bindingDialog,
  })
  bindingScrollContent.scrollTop = 0
  document.documentElement.classList.add('dark')
  try {
    await new Promise((resolve) => setTimeout(resolve, 200))
    await captureVisualCheck({
      path: '../../../../docs/visual-checks/environment-binding-identity-390-dark.png',
      element: bindingDialog,
    })
  } finally {
    document.documentElement.classList.remove('dark')
  }
  await expect
    .element(
      screen.getByText(
        '生产 → prod-west / production / orders-api-plan-8 / api',
        { exact: true }
      )
    )
    .toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: '取消' }))
  const environmentTarget = screen
    .getByText('orders · prod-west / production / orders-api-plan-8', {
      exact: true,
    })
    .element()
  const environmentSummary = screen
    .getByTestId('environment-overview-summary')
    .element()
  const releaseEnvironments = screen
    .getByRole('region', { name: '发布环境' })
    .element()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  expect(environmentTarget.getBoundingClientRect().top).toBeLessThan(844)
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-overview-summary-390-light.png',
    element: environmentSummary,
  })
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-release-target-390-light.png',
    element: releaseEnvironments,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-overview-summary-390-dark.png',
    element: environmentSummary,
  })
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-release-target-390-dark.png',
    element: releaseEnvironments,
  })
  document.documentElement.classList.remove('dark')
  await page.viewport(1440, 1000)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
  expect(
    releaseEnvironments
      .querySelector<HTMLElement>('[data-slot="card"]')
      ?.getBoundingClientRect().width
  ).toBeGreaterThan(800)
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-overview-summary-1440-light.png',
    element: environmentSummary,
  })
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-release-target-1440-light.png',
    element: releaseEnvironments,
  })
  await page.viewport(390, 844)
  await userEvent.click(screen.getByText('查看验证报告与有效期'))
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await expect
    .element(screen.getByRole('link', { name: '打开报告' }).first())
    .toHaveAttribute('href', 'https://ci.example.com/build/81')
  await expect
    .element(screen.getByRole('link', { name: '打开报告' }).first())
    .toHaveAttribute('rel', 'noopener noreferrer')
  await userEvent.click(
    screen.getByRole('button', { name: '重新核对 UID' }).first()
  )
  await expect
    .element(
      screen.getByRole('alertdialog', { name: '重新核对活动 Deployment UID？' })
    )
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText('prod-west / production / orders-api-plan-8', {
        exact: true,
      })
    )
    .toBeInTheDocument()
  expect(platformApi.rebindDeliveryWorkload).not.toHaveBeenCalled()
  await userEvent.click(screen.getByRole('button', { name: '读取并更新绑定' }))
  await expect
    .element(
      screen.getByRole('alertdialog', { name: '重新核对活动 Deployment UID？' })
    )
    .not.toBeInTheDocument()
  expect(platformApi.rebindDeliveryWorkload).toHaveBeenCalledWith(2)

  await userEvent.click(
    screen.getByRole('button', { name: '编辑关系' }).first()
  )
  await expect
    .element(screen.getByText(/当前 v2 · 共 1 个不可变版本/))
    .toBeInTheDocument()
  const deploymentPipelineSection = screen
    .getByText('配置部署流水线（可选）')
    .element()
    .closest<HTMLElement>('fieldset')
  expect(deploymentPipelineSection).not.toBeNull()
  await expect.element(screen.getByText('写入范围尚未确认')).toBeInTheDocument()
  const pipelineScopeNotice = screen.getByText('写入范围尚未确认').element()
    .parentElement!.parentElement!
  expect(pipelineScopeNotice.textContent).toContain('不会运行云效任务')
  expect(pipelineScopeNotice.textContent).toContain(
    '不会把它接入平台发布 Worker'
  )
  await userEvent.selectOptions(screen.getByLabelText('Yunxiao 连接'), '9')
  await userEvent.fill(
    screen.getByLabelText('按名称查找配置部署流水线'),
    'orders-config'
  )
  await userEvent.click(screen.getByRole('button', { name: '搜索流水线' }))
  await expect
    .element(screen.getByRole('option', { name: /orders-config-deploy/ }))
    .toBeInTheDocument()
  expect(runnerApi.searchDeploymentPipelines).toHaveBeenCalledWith({
    connection_id: 9,
    name: 'orders-config',
    page: 1,
  })
  await userEvent.selectOptions(
    screen.getByLabelText('搜索结果 · 第 1 页 / 共 1 条'),
    '123456'
  )
  await userEvent.click(screen.getByRole('button', { name: '检查连通性' }))
  await expect
    .element(screen.getByText(/云效连接成功：orders-config-deploy/))
    .toBeInTheDocument()
  expect(runnerApi.inspectDeploymentPipeline).toHaveBeenCalledWith({
    connection_id: 9,
    pipeline_id: '123456',
  })
  await expect
    .element(screen.getByText(/不代表写入范围安全或配置已生效/))
    .toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('button', { name: '分析可能写入范围' })
  )
  await expect
    .element(screen.getByText('已扫描常见写入标记，仍需人工确认'))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(/此定义已有人工核对记录：admin · unknown。记录仅作审计/)
    )
    .toBeInTheDocument()
  await expect.element(screen.getByText('配置资源可能写入')).toBeInTheDocument()
  await expect
    .element(screen.getByText('云效任务模板，范围未知'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/仍有 1 个脚本或任务模板需要在云效逐项核对/))
    .toBeInTheDocument()
  expect(runnerApi.inspectDeploymentPipelineScope).toHaveBeenCalledWith({
    connection_id: 9,
    pipeline_id: '123456',
  })
  const scopeReport = screen.getByRole('region', {
    name: '配置流水线写入范围静态分析结果',
  })
  expect(scopeReport.element().textContent).toContain('仍需人工确认')
  const scopeScroll = screen
    .getByTestId('delivery-binding-scroll-content')
    .element()
  scopeScroll.scrollTop +=
    scopeReport.element().getBoundingClientRect().top -
    scopeScroll.getBoundingClientRect().top
  await new Promise((resolve) => setTimeout(resolve, 300))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/config-pipeline-scope-390-light.png',
    element: scopeReport,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/config-pipeline-scope-390-dark.png',
    element: scopeReport,
  })
  document.documentElement.classList.remove('dark')
  vi.mocked(runnerApi.inspectDeploymentPipelineScope).mockResolvedValueOnce({
    data: {
      pipeline_id: '123456',
      name: 'orders-config-deploy',
      type: 'PIPELINEASCODE',
      update_time: 1770000000000,
      analysis_status: 'definition_unparseable',
      review_required: true,
      findings: [],
      unresolved_nodes: [],
      warning: '定义无法解析，人工核对仍为必需。',
    },
  } as never)
  await userEvent.click(
    screen.getByRole('button', { name: '分析可能写入范围' })
  )
  await expect
    .element(screen.getByText('流水线定义无法自动分析'))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(
        /已有人工核对记录：admin · unknown。重新分析当前定义后，才能确认该记录是否仍适用。/
      )
    )
    .toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('button', { name: '分析可能写入范围' })
  )
  await expect
    .element(
      screen.getByText(/此定义已有人工核对记录：admin · unknown。记录仅作审计/)
    )
    .toBeInTheDocument()
  deploymentPipelineSection!.scrollIntoView({ block: 'start' })
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/config-pipeline-binding-390-light.png',
    element: deploymentPipelineSection!,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/config-pipeline-binding-390-dark.png',
    element: deploymentPipelineSection!,
  })
  document.documentElement.classList.remove('dark')
  const previousClipboard = navigator.clipboard
  const writeText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText },
  })
  try {
    await userEvent.click(
      screen.getByRole('button', { name: '复制完整定义指纹' })
    )
    expect(writeText).toHaveBeenCalledWith('a'.repeat(64))
  } finally {
    if (previousClipboard) {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: previousClipboard,
      })
    } else {
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  }
  await userEvent.click(screen.getByText('在云效完成 4 项核对'))
  await expect.element(screen.getByText('追踪动态命令：')).toBeInTheDocument()
  await expect.element(screen.getByText('划定发布责任：')).toBeInTheDocument()
  const copyRequestButton = screen.getByRole('button', {
    name: '复制核对请求给负责人',
  })
  const checklistClipboard = navigator.clipboard
  const checklistWriteText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: checklistWriteText },
  })
  try {
    await userEvent.click(copyRequestButton)
    expect(checklistWriteText).toHaveBeenCalledOnce()
    const copiedRequest = checklistWriteText.mock.calls[0][0]
    expect(copiedRequest).toContain('Pipeline ID：123456')
    expect(copiedRequest).toContain('动态变量展开结果')
    expect(copiedRequest).toContain('当前绑定目标之外的资源')
    expect(copiedRequest).toContain('有效权限范围')
    expect(copiedRequest).toContain('避免并发覆盖')
    expect(copiedRequest).toContain('不要发送 Token、密码、私钥或 Secret 值')
  } finally {
    if (checklistClipboard) {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: checklistClipboard,
      })
    } else {
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  }
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  expect(
    Number.parseFloat(
      getComputedStyle(copyRequestButton.element()).minHeight || '0'
    )
  ).toBeGreaterThanOrEqual(44)
  await new Promise((resolve) => setTimeout(resolve, 300))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/config-pipeline-checklist-390-light.png',
    element: copyRequestButton.element(),
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/config-pipeline-checklist-390-dark.png',
    element: copyRequestButton.element(),
  })
  document.documentElement.classList.remove('dark')
  await userEvent.click(screen.getByText('记录人工核对结论'))
  await expect
    .element(screen.getByRole('button', { name: '记录核对结论' }))
    .toBeDisabled()
  await userEvent.selectOptions(
    screen.getByLabelText('你核对后的写入范围'),
    'unknown'
  )
  await userEvent.click(screen.getByLabelText(/我已在云效核对完整定义/))
  await expect
    .element(screen.getByRole('button', { name: '记录核对结论' }))
    .toBeEnabled()
  const scopeReviewPanel = screen
    .getByText('记录人工核对结论')
    .element()
    .closest<HTMLElement>('details')
  expect(scopeReviewPanel).not.toBeNull()
  await expect
    .element(screen.getByText(/平台会保存当前定义的人工结论/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/该记录仅作审计，不等于平台验证，也不会开放触发/))
    .toBeInTheDocument()
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/config-pipeline-review-390-light.png',
    element: scopeReviewPanel!,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/config-pipeline-review-390-dark.png',
    element: scopeReviewPanel!,
  })
  document.documentElement.classList.remove('dark')
  await userEvent.click(screen.getByRole('button', { name: '记录核对结论' }))
  expect(runnerApi.reviewDeploymentPipelineScope).toHaveBeenCalledWith({
    app_id: 2,
    connection_id: 9,
    pipeline_id: '123456',
    definition_hash: 'a'.repeat(64),
    write_scope: 'unknown',
    acknowledged: true,
  })
  await expect
    .element(screen.getByText(/此定义已有人工核对记录：admin · unknown/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/记录仅作审计，不开放触发/))
    .toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: '取消' }))
  await expect
    .element(screen.getByRole('dialog', { name: '核对组件归属与环境' }))
    .not.toBeInTheDocument()
  expect(platformApi.createDeliveryPolicyVersion).not.toHaveBeenCalled()
  await userEvent.click(
    screen.getByRole('button', { name: '编辑关系' }).first()
  )
  await userEvent.click(
    screen.getByRole('button', { name: /网关金丝雀/ }).first()
  )
  await expect
    .element(
      screen.getByText(
        '填写全部阈值和至少两个递增阶段，最后一个为 100%，才能保存。'
      )
    )
    .toBeVisible()
  const canaryDialog = screen
    .getByRole('dialog', { name: '核对组件归属与环境' })
    .element()
  await page.viewport(390, 844)
  const canaryFormBody = screen
    .getByTestId('delivery-binding-scroll-content')
    .element()
  canaryDialog.scrollTop = 0
  canaryFormBody.scrollTop = 0
  const canaryThresholdTitle = screen.getByText('金丝雀分析阈值').element()
  const bodyTop = canaryFormBody.getBoundingClientRect().top
  canaryFormBody.scrollTop = Math.max(
    0,
    canaryThresholdTitle.getBoundingClientRect().top - bodyTop
  )
  const gateHint = screen
    .getByText('填写全部阈值和至少两个递增阶段，最后一个为 100%，才能保存。')
    .element()
  const savePolicy = screen
    .getByRole('button', { name: '保存绑定与策略' })
    .element()
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeDisabled()
  expect(gateHint.getBoundingClientRect().top).toBeLessThan(
    savePolicy.getBoundingClientRect().top
  )
  expect(savePolicy.getBoundingClientRect().bottom).toBeLessThanOrEqual(
    canaryDialog.getBoundingClientRect().bottom
  )
  const canaryFooter = canaryDialog.querySelector<HTMLElement>(
    '[data-slot="dialog-footer"]'
  )
  expect(canaryFooter).not.toBeNull()
  expect(
    canaryDialog.getBoundingClientRect().bottom -
      canaryFooter!.getBoundingClientRect().bottom
  ).toBeLessThanOrEqual(24)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/canary-policy-390-gate-light.png',
    element: canaryDialog,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/canary-policy-390-gate-dark.png',
    element: canaryDialog,
  })
  document.documentElement.classList.remove('dark')

  expect(
    (
      screen
        .getByRole('spinbutton', {
          name: '第 1 阶段候选流量比例',
        })
        .element() as HTMLInputElement
    ).value
  ).toBe('')
  expect(
    (
      screen
        .getByRole('spinbutton', {
          name: '第 2 阶段候选流量比例',
        })
        .element() as HTMLInputElement
    ).value
  ).toBe('')
  for (const group of ['样本与新鲜度', '错误率', '延迟']) {
    await expect
      .element(screen.getByRole('group', { name: group }))
      .toBeVisible()
  }
  await expect
    .element(screen.getByRole('group', { name: '流量阶段与观察窗口' }))
    .toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeDisabled()
  const canaryThresholds = [
    ['每版本最少请求数', '1000'],
    ['观测最大年龄', '120'],
    ['候选错误率上限', '2'],
    ['错误率较稳定版最多增加', '1'],
    ['候选 P95 延迟上限', '250'],
    ['P95 较稳定版最多增加', '20'],
  ] as const
  for (const [label, value] of canaryThresholds) {
    await userEvent.fill(screen.getByRole('spinbutton', { name: label }), value)
  }
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeDisabled()
  await userEvent.click(screen.getByRole('button', { name: '添加阶段' }))
  const canaryStages = [
    ['10', '60'],
    ['50', '120'],
    ['100', '300'],
  ] as const
  for (const [index, [weight, duration]] of canaryStages.entries()) {
    await userEvent.fill(
      screen.getByRole('spinbutton', {
        name: `第 ${index + 1} 阶段候选流量比例`,
      }),
      weight
    )
    await userEvent.fill(
      screen.getByRole('spinbutton', {
        name: `第 ${index + 1} 阶段最短观察时长`,
      }),
      duration
    )
  }
  for (const [index, [weight]] of canaryStages.entries()) {
    await expect
      .element(
        screen.getByRole('group', {
          name: `第 ${index + 1} 阶段流量拆分`,
        })
      )
      .toHaveTextContent(`稳定版 ${100 - Number(weight)}% · 候选版 ${weight}%`)
  }
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeEnabled()
  await expect
    .element(
      screen.getByText(
        '填写全部阈值和至少两个递增阶段，最后一个为 100%，才能保存。'
      )
    )
    .not.toBeInTheDocument()
  const trafficStages = screen
    .getByRole('group', { name: '流量阶段与观察窗口' })
    .element()
  for (const viewport of [
    { width: 1024, height: 768, name: '1024' },
    { width: 1440, height: 1000, name: '1440' },
  ]) {
    await page.viewport(viewport.width, viewport.height)
    trafficStages.scrollIntoView({ block: 'center' })
    const dialogBounds = canaryDialog.getBoundingClientRect()
    const footerBounds = canaryFooter!.getBoundingClientRect()
    const saveBounds = savePolicy.getBoundingClientRect()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(
      viewport.width
    )
    expect(saveBounds.top).toBeGreaterThanOrEqual(dialogBounds.top)
    expect(saveBounds.bottom).toBeLessThanOrEqual(footerBounds.bottom)
    expect(footerBounds.bottom).toBeLessThanOrEqual(dialogBounds.bottom)
    document.documentElement.classList.remove('dark')
    await captureVisualCheck({
      path: `../../../../docs/visual-checks/canary-policy-${viewport.name}-flow-light.png`,
      element: canaryDialog,
    })
    document.documentElement.classList.add('dark')
    await new Promise((resolve) => setTimeout(resolve, 200))
    await captureVisualCheck({
      path: `../../../../docs/visual-checks/canary-policy-${viewport.name}-flow-dark.png`,
      element: canaryDialog,
    })
  }
  await page.viewport(390, 844)
  trafficStages.scrollIntoView({ block: 'center' })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/canary-policy-390-flow-dark.png',
    element: canaryDialog,
  })
  document.documentElement.classList.remove('dark')
  const bindingDialogBounds = screen
    .getByRole('dialog', { name: '核对组件归属与环境' })
    .element()
    .getBoundingClientRect()
  const saveButtonBounds = screen
    .getByRole('button', { name: '保存绑定与策略' })
    .element()
    .getBoundingClientRect()
  expect(saveButtonBounds.top).toBeGreaterThanOrEqual(bindingDialogBounds.top)
  expect(saveButtonBounds.bottom).toBeLessThanOrEqual(
    bindingDialogBounds.bottom
  )
  await userEvent.fill(
    screen.getByRole('spinbutton', { name: '第 2 阶段候选流量比例' }),
    '5'
  )
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent('必须高于上一阶段的 10%。')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeDisabled()
  await userEvent.fill(
    screen.getByRole('spinbutton', { name: '第 2 阶段候选流量比例' }),
    '50'
  )
  await userEvent.fill(
    screen.getByRole('spinbutton', { name: '第 2 阶段候选流量比例' }),
    '50.5'
  )
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent('流量比例必须是整数百分比。')
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeDisabled()
  await userEvent.fill(
    screen.getByRole('spinbutton', { name: '第 2 阶段候选流量比例' }),
    '50'
  )
  await userEvent.fill(
    screen.getByRole('spinbutton', { name: '第 2 阶段最短观察时长' }),
    '120.5'
  )
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent('观察时长必须是整数秒。')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeDisabled()
  await userEvent.fill(
    screen.getByRole('spinbutton', { name: '第 2 阶段最短观察时长' }),
    '120'
  )
  await userEvent.fill(
    screen.getByRole('spinbutton', { name: '观测最大年龄' }),
    '120.5'
  )
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent('观测年龄必须填写整数秒。')
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeDisabled()
  await userEvent.fill(
    screen.getByRole('spinbutton', { name: '观测最大年龄' }),
    '120'
  )
  await expect
    .element(screen.getByRole('spinbutton', { name: '观测最大年龄' }))
    .toHaveAttribute('aria-invalid', 'false')
  await userEvent.fill(
    screen.getByRole('spinbutton', { name: '网关连接 ID' }),
    '7'
  )
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent(
      '连接 ID、路由 ID 和两个不同的 backend ID 必须完整有效。'
    )
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeDisabled()
  await userEvent.fill(
    screen.getByRole('textbox', { name: '路由 ID' }),
    'orders-public'
  )
  await userEvent.fill(
    screen.getByRole('textbox', { name: '稳定版 backend ID' }),
    'orders-stable'
  )
  await userEvent.fill(
    screen.getByRole('textbox', { name: '候选版 backend ID' }),
    'orders-stable'
  )
  await expect
    .element(screen.getByRole('alert'))
    .toHaveTextContent('两个不同的 backend ID 必须完整有效。')
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeDisabled()
  await userEvent.fill(
    screen.getByRole('textbox', { name: '候选版 backend ID' }),
    'orders-candidate'
  )
  await expect
    .element(screen.getByRole('button', { name: '保存绑定与策略' }))
    .toBeEnabled()
  const gatewayRouteSection = screen
    .getByRole('group', { name: '网关路由身份（可稍后登记）' })
    .element()
  const gatewayChecklist = screen.getByText('接入前需要准备哪些网关资料？')
  await userEvent.click(gatewayChecklist)
  await expect.element(screen.getByText(/产品与访问方式：/)).toBeVisible()
  await expect.element(screen.getByText(/并发保护或幂等键/)).toBeVisible()
  await expect
    .element(screen.getByText(/请求数、错误率和 P95 延迟/))
    .toBeVisible()
  await expect
    .element(screen.getByText(/稳定版 100% \/ 候选版 0%/))
    .toBeVisible()
  await expect
    .element(screen.getByText(/文档中请隐去 Token、密码和私钥/))
    .toBeVisible()
  expect(
    screen
      .getByRole('button', { name: '复制资料清单' })
      .element()
      .getBoundingClientRect().height
  ).toBeGreaterThanOrEqual(44)
  const previousGatewayClipboard = navigator.clipboard
  const gatewayWriteText = vi.fn().mockResolvedValue(undefined)
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: gatewayWriteText },
  })
  try {
    await userEvent.click(screen.getByRole('button', { name: '复制资料清单' }))
    expect(gatewayWriteText).toHaveBeenCalledWith(
      [
        '我们正在评估精确金丝雀接入，请提供以下脱敏资料：',
        '1. 网关产品名称/版本、平台可访问的内网地址、认证方式和所需最小权限。',
        '2. 如何读取 route/backend 身份和当前权重；如何以并发保护或幂等方式设置稳定/候选权重，以及如何回读已生效结果。',
        '3. 如何按稳定版/候选版分别读取请求数、错误率和 P95 延迟；请说明观察窗口、数据延迟与最小样本量。',
        '4. 如何恢复稳定版 100% / 候选版 0%，确认恢复生效，并在请求超时后安全回读和重试。',
        '如有接口文档、脱敏请求/响应样例或故障恢复说明，请一并提供。请勿发送 Token、密码或私钥。',
      ].join('\n')
    )
  } finally {
    if (previousGatewayClipboard) {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: previousGatewayClipboard,
      })
    } else {
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  }
  const gatewayChecklistDetails = gatewayRouteSection.querySelector('details')
  expect(gatewayChecklistDetails).not.toBeNull()
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/canary-gateway-integration-checklist-390-light.png',
    element: gatewayChecklistDetails!,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/canary-gateway-integration-checklist-390-dark.png',
    element: gatewayChecklistDetails!,
  })
  document.documentElement.classList.remove('dark')
  await userEvent.click(gatewayChecklist)
  gatewayRouteSection.scrollIntoView({ block: 'center' })
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/canary-route-identity-390-light.png',
    element: gatewayRouteSection,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/canary-route-identity-390-dark.png',
    element: gatewayRouteSection,
  })
  document.documentElement.classList.remove('dark')
  await userEvent.click(screen.getByRole('button', { name: '保存绑定与策略' }))
  await expect
    .element(screen.getByRole('dialog', { name: '核对组件归属与环境' }))
    .not.toBeInTheDocument()
  expect(platformApi.assignDeliveryBinding).toHaveBeenNthCalledWith(1, 2, {
    application_id: 1,
    component_id: 1,
    environment_id: 1,
    strategy_draft: 'gateway_canary',
    service_name: '',
    expected_policy_version: 2,
    strategy: 'gateway_canary',
    candidate_retention_hours: 0,
    deployment_pipeline: { connection_id: 9, pipeline_id: '123456' },
    canary_analysis: {
      gateway_route: {
        connection_id: 7,
        route_id: 'orders-public',
        stable_backend_id: 'orders-stable',
        candidate_backend_id: 'orders-candidate',
      },
      min_requests: 1000,
      max_observation_age_seconds: 120,
      max_candidate_error_rate: 0.02,
      max_error_rate_increase: 0.01,
      max_candidate_p95_latency_ms: 250,
      max_p95_latency_increase_ratio: 0.2,
      stages: [
        { candidate_weight_percent: 10, observation_duration_seconds: 60 },
        { candidate_weight_percent: 50, observation_duration_seconds: 120 },
        { candidate_weight_percent: 100, observation_duration_seconds: 300 },
      ],
    },
  })
  await userEvent.click(screen.getByRole('button', { name: '刷新运行状态' }))
  await vi.waitFor(() => {
    expect(platformApi.deliveryDrift).toHaveBeenCalledTimes(2)
    expect(platformApi.logs).toHaveBeenCalledTimes(2)
    expect(platformApi.releases).toHaveBeenCalledTimes(2)
  })
  await page.viewport(1024, 768)
  document.documentElement.classList.add('dark')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
  const imageEvidence = screen.getByText('镜像一致').first().element()
  imageEvidence.scrollIntoView({ block: 'center' })
  const environmentCard =
    imageEvidence.closest<HTMLElement>('[data-slot="card"]')
  expect(environmentCard).not.toBeNull()
  await new Promise((resolve) => setTimeout(resolve, 200))
  await captureVisualCheck({
    path: '../../../../docs/visual-checks/environment-evidence-1024-dark.png',
    element: environmentCard!,
  })
  document.documentElement.classList.remove('dark')
  client.clear()
})
