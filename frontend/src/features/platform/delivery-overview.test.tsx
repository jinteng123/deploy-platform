import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import { platformApi, type DeliveryIncident } from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { DeliveryOverview } from './delivery-overview'

const defaultDeliveryMetrics = (overrides: Record<string, unknown> = {}) => ({
  window_days: 30,
  window_start: '2026-09-10T12:00:00Z',
  as_of: '2026-10-10T12:00:00Z',
  unknown_tier_samples: 1,
  invalid_timestamp_samples: 0,
  production_attempts: 4,
  successful_deployments: 2,
  execution_failures: 1,
  active_tasks: 1,
  pinned_deployments: 4,
  duration_samples: 0,
  approval_wait_samples: 0,
  pending_approvals: 0,
  execution_failure_rate: 1 / 3,
  execution_p95_seconds: null,
  approval_wait_p95_seconds: null,
  incident_recovery: {
    data_available: true,
    incident_samples: 3,
    recovered_samples: 2,
    unrecovered_samples: 1,
    intervention_recovery_samples: 1,
    invalid_timestamp_samples: 0,
    causality_assessed_samples: 1,
    causality_pending_samples: 2,
    remediation_classified_samples: 2,
    remediation_pending_samples: 1,
    mean_recovery_seconds: 5400,
    mean_intervention_to_recovery_seconds: 1800,
    missing_data: '',
  },
  dora: {
    change_fail_rate: {
      value: null,
      missing_data: '因果判定、修复处置及计划内/外记录覆盖率尚不完整',
    },
  },
  ...overrides,
})

vi.mock('@/api/client', () => ({
  platformApi: {
    deliveryMetrics: vi.fn(),
    deliveryIncidents: vi.fn(),
    createDeliveryIncident: vi.fn(),
    confirmDeliveryIncidentRecovery: vi.fn(),
    startDeliveryIncidentIntervention: vi.fn(),
    assessDeliveryIncidentCausality: vi.fn(),
    recordDeliveryIncidentRemediation: vi.fn(),
    clusters: vi.fn(),
    apps: vi.fn(),
    logs: vi.fn(),
    deliveryDrift: vi.fn(),
    deliveryDriftHistory: vi.fn(),
    deliveryNotificationAlerts: vi.fn(),
  },
}))
beforeEach(() => {
  vi.clearAllMocks()
  useAuthStore.getState().auth.reset()
  vi.mocked(platformApi.deliveryMetrics).mockResolvedValue({
    data: defaultDeliveryMetrics(),
  } as never)
  vi.mocked(platformApi.deliveryIncidents).mockResolvedValue({
    data: [],
  } as never)
  vi.mocked(platformApi.logs).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.createDeliveryIncident).mockResolvedValue({
    data: {},
  } as never)
  vi.mocked(platformApi.confirmDeliveryIncidentRecovery).mockResolvedValue({
    data: {},
  } as never)
  vi.mocked(platformApi.startDeliveryIncidentIntervention).mockResolvedValue({
    data: {},
  } as never)
  vi.mocked(platformApi.assessDeliveryIncidentCausality).mockResolvedValue({
    data: {},
  } as never)
  vi.mocked(platformApi.recordDeliveryIncidentRemediation).mockResolvedValue({
    data: {},
  } as never)
  vi.mocked(platformApi.clusters).mockResolvedValue({
    data: [
      { id: 2, name: '生产 A', enabled: true, environment_tier: 'production' },
      {
        id: 3,
        name: '测试环境',
        enabled: true,
        environment_tier: 'non_production',
      },
    ],
  } as never)
  vi.mocked(platformApi.apps).mockResolvedValue({
    data: [
      { id: 1, cluster_id: 2, app_name: 'shop' },
      { id: 2, cluster_id: 3, app_name: 'test-only' },
    ],
  } as never)
  vi.mocked(platformApi.deliveryDrift).mockResolvedValue({
    data: [
      {
        app_id: 1,
        app_name: 'shop',
        namespace: 'shop',
        workload: 'api',
        container: 'api',
        cluster_id: 2,
        status: 'drifted',
        expected_image: 'reg.io/shop@sha256:expected',
        actual_image: 'reg.io/shop:changed',
        drift_started_at: '2026-10-09T10:00:00Z',
        observed_at: '2026-10-10T12:00:00Z',
        last_observed_at: '2026-10-10T12:00:00Z',
        observation_count: 3,
      },
    ],
  } as never)
  vi.mocked(platformApi.deliveryDriftHistory).mockResolvedValue({
    data: [
      {
        id: 1,
        app_id: 1,
        app_name: 'shop',
        cluster_id: 2,
        namespace: 'shop',
        workload: 'api',
        container: 'api',
        previous_status: 'in_sync',
        status: 'drifted',
        change_kind: 'status',
        expected_image: 'reg.io/shop@sha256:expected',
        actual_image: 'reg.io/shop:changed',
        drift_started_at: '2026-10-09T10:00:00Z',
        observed_at: '2026-10-10T12:00:00Z',
        observed_by: 'operator',
        source: 'on_demand_read',
      },
    ],
  } as never)
  vi.mocked(platformApi.deliveryNotificationAlerts).mockResolvedValue({
    data: { items: [], has_more: false },
  } as never)
})

it('lets operators record a production incident and confirm business recovery', async () => {
  await page.viewport(390, 1200)
  useAuthStore.getState().auth.setUser({
    id: 2,
    username: 'operator',
    display_name: 'Operator',
    role: 'operator',
    k8s_username: '',
    k8s_groups: [],
    enabled: true,
  })
  vi.mocked(platformApi.logs).mockResolvedValue({
    data: [
      {
        id: 90,
        cluster_id: 2,
        app_name: 'shop',
        deployment_name: 'shop-api',
        container_name: 'api',
        namespace: 'shop',
        version: 'v2',
        status: 'success',
        deploy_type: 'deploy',
        environment_tier_snapshot: 'production',
        created_at: '2026-10-10T12:00:00Z',
      },
      {
        id: 91,
        cluster_id: 2,
        app_name: 'shop',
        deployment_name: 'shop-api',
        container_name: 'api',
        namespace: 'shop',
        version: 'v1',
        status: 'success',
        deploy_type: 'rollback',
        environment_tier_snapshot: 'production',
        created_at: '2026-10-10T12:40:00Z',
      },
    ],
  } as never)
  let currentIncident: DeliveryIncident = {
    id: 7,
    deploy_log_id: 90,
    cluster_id: 2,
    app_name: 'shop',
    summary: '支付请求错误率升高',
    status: 'open',
    occurred_at: '2026-10-10T12:20:00Z',
    recorded_at: '2026-10-10T12:24:00Z',
    recorded_by: 'operator',
    version: 1,
    causality_status: 'unknown',
    remediation_type: 'unknown',
    remediation_plan_status: 'unknown',
    events: [
      {
        id: 21,
        incident_id: 7,
        event_type: 'incident_recorded',
        deploy_log_id: 90,
        occurred_at: '2026-10-10T12:20:00Z',
        recorded_at: '2026-10-10T12:24:00Z',
        recorded_by: 'operator',
        detail: '支付请求错误率升高',
      },
    ],
  }
  vi.mocked(platformApi.deliveryIncidents).mockImplementation(
    async () =>
      ({
        data: [currentIncident],
      }) as never
  )
  vi.mocked(platformApi.assessDeliveryIncidentCausality).mockImplementation(
    async (id, payload) => {
      currentIncident = {
        ...currentIncident,
        causality_status: payload.status,
        causality_recorded_at: '2026-10-10T12:29:00Z',
        causality_recorded_by: 'operator',
        causality_reason: payload.reason,
        version: currentIncident.version + 1,
        events: [
          ...currentIncident.events,
          {
            id: 22,
            incident_id: id,
            event_type: 'causality_assessed',
            deploy_log_id: currentIncident.deploy_log_id,
            occurred_at: '2026-10-10T12:29:00Z',
            recorded_at: '2026-10-10T12:29:00Z',
            recorded_by: 'operator',
            detail: payload.reason,
            causality_status: payload.status,
          },
        ],
      }
      return { data: currentIncident } as never
    }
  )
  vi.mocked(platformApi.recordDeliveryIncidentRemediation).mockImplementation(
    async (id, payload) => {
      currentIncident = {
        ...currentIncident,
        remediation_type: payload.remediation_type,
        remediation_plan_status: payload.plan_status,
        remediation_deploy_log_id: payload.remediation_deploy_log_id,
        remediation_recorded_at: '2026-10-10T12:35:00Z',
        remediation_recorded_by: 'operator',
        remediation_reason: payload.reason,
        version: currentIncident.version + 1,
        events: [
          ...currentIncident.events,
          {
            id: 24,
            incident_id: id,
            event_type: 'remediation_classified',
            deploy_log_id: currentIncident.deploy_log_id,
            remediation_type: payload.remediation_type,
            remediation_plan_status: payload.plan_status,
            remediation_deploy_log_id: payload.remediation_deploy_log_id,
            occurred_at: '2026-10-10T12:35:00Z',
            recorded_at: '2026-10-10T12:35:00Z',
            recorded_by: 'operator',
            detail: payload.reason,
          },
        ],
      }
      return { data: currentIncident } as never
    }
  )
  vi.mocked(platformApi.startDeliveryIncidentIntervention).mockImplementation(
    async (id, payload) => {
      currentIncident = {
        ...currentIncident,
        intervention_started_at: payload.occurred_at,
        intervention_recorded_at: '2026-10-10T12:30:00Z',
        intervention_recorded_by: 'operator',
        version: currentIncident.version + 1,
        events: [
          ...currentIncident.events,
          {
            id: 23,
            incident_id: id,
            event_type: 'intervention_started',
            deploy_log_id: currentIncident.deploy_log_id,
            occurred_at: payload.occurred_at,
            recorded_at: '2026-10-10T12:30:00Z',
            recorded_by: 'operator',
            detail: payload.detail,
          },
        ],
      }
      return { data: currentIncident } as never
    }
  )

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DeliveryOverview />
    </QueryClientProvider>
  )

  const incidentRegion = screen.getByRole('region', {
    name: '线上故障与恢复事件',
  })
  await expect
    .element(
      incidentRegion.getByRole('heading', { name: '支付请求错误率升高' })
    )
    .toBeInTheDocument()
  await expect.element(incidentRegion.getByText('部署 #90')).toBeInTheDocument()
  await expect
    .element(incidentRegion.getByText('因果待判定'))
    .toBeInTheDocument()
  await userEvent.click(
    incidentRegion.getByRole('button', { name: '记录因果判定' })
  )
  await userEvent.click(
    incidentRegion.getByRole('combobox', { name: '因果判定' })
  )
  await userEvent.click(
    screen.getByRole('option', { name: '由关联部署导致', exact: true })
  )
  await userEvent.fill(
    incidentRegion.getByRole('textbox', { name: '判定依据' }),
    '回滚该版本后错误率恢复'
  )
  await userEvent.click(
    incidentRegion.getByRole('button', { name: '保存因果判定' })
  )
  await expect
    .poll(
      () =>
        vi.mocked(platformApi.assessDeliveryIncidentCausality).mock.calls.length
    )
    .toBe(1)
  expect(platformApi.assessDeliveryIncidentCausality).toHaveBeenCalledWith(
    7,
    expect.objectContaining({
      expected_version: 1,
      status: 'caused_by_deployment',
      reason: '回滚该版本后错误率恢复',
    })
  )
  await expect
    .element(incidentRegion.getByText('人工判定：由关联部署导致'))
    .toBeInTheDocument()
  await userEvent.click(
    incidentRegion.getByRole('button', { name: '记录修复处置' })
  )
  await userEvent.click(
    incidentRegion.getByRole('combobox', { name: '处置方式' })
  )
  await userEvent.click(
    screen.getByRole('option', { name: '回滚', exact: true })
  )
  await userEvent.click(
    incidentRegion.getByRole('combobox', { name: '对应修复部署' })
  )
  await userEvent.click(
    screen.getByRole('option', { name: '#91 · rollback · success' })
  )
  await userEvent.click(
    incidentRegion.getByRole('combobox', { name: '计划状态' })
  )
  await userEvent.click(
    screen.getByRole('option', { name: '计划内', exact: true })
  )
  await userEvent.fill(
    incidentRegion.getByRole('textbox', { name: '处置依据' }),
    '回滚到上一稳定镜像后探针恢复'
  )
  await userEvent.click(
    incidentRegion.getByRole('button', { name: '保存处置记录' })
  )
  await expect
    .poll(
      () =>
        vi.mocked(platformApi.recordDeliveryIncidentRemediation).mock.calls
          .length
    )
    .toBe(1)
  expect(platformApi.recordDeliveryIncidentRemediation).toHaveBeenCalledWith(
    7,
    expect.objectContaining({
      expected_version: 2,
      remediation_type: 'rollback',
      plan_status: 'planned',
      remediation_deploy_log_id: 91,
      reason: '回滚到上一稳定镜像后探针恢复',
    })
  )
  await expect
    .element(incidentRegion.getByText('回滚 · 计划内', { exact: true }))
    .toBeInTheDocument()
  await userEvent.click(
    incidentRegion.getByRole('combobox', { name: '关联生产部署' })
  )
  await userEvent.click(
    screen.getByRole('option', { name: /#90 · shop \/ shop-api/ })
  )
  await userEvent.fill(
    incidentRegion.getByRole('textbox', { name: '现象摘要' }),
    '库存服务调用超时'
  )
  await userEvent.click(
    incidentRegion.getByRole('button', { name: '登记线上事件' })
  )
  await expect
    .poll(() => vi.mocked(platformApi.createDeliveryIncident).mock.calls.length)
    .toBe(1)
  expect(platformApi.createDeliveryIncident).toHaveBeenCalledWith(
    expect.objectContaining({
      deploy_log_id: 90,
      summary: '库存服务调用超时',
      occurred_at: expect.stringMatching(/^\d{4}-\d{2}-\d{2}T/),
    })
  )

  await userEvent.click(
    incidentRegion.getByRole('button', { name: '记录开始干预' })
  )
  await userEvent.fill(
    incidentRegion.getByRole('textbox', { name: '已采取的干预措施' }),
    '开始回退上一稳定版本'
  )
  await userEvent.click(
    incidentRegion.getByRole('button', { name: '保存干预开始' })
  )
  await expect
    .poll(
      () =>
        vi.mocked(platformApi.startDeliveryIncidentIntervention).mock.calls
          .length
    )
    .toBe(1)
  expect(platformApi.startDeliveryIncidentIntervention).toHaveBeenCalledWith(
    7,
    expect.objectContaining({
      expected_version: 3,
      detail: '开始回退上一稳定版本',
      occurred_at: expect.stringMatching(/^\d{4}-\d{2}-\d{2}T/),
    })
  )
  await expect
    .element(incidentRegion.getByText('开始故障干预'))
    .toBeInTheDocument()
  window.scrollTo(0, 0)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await page.screenshot({
    path: '../../../../docs/visual-checks/delivery-incidents-390-light.png',
    element: incidentRegion,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 200))
  await page.screenshot({
    path: '../../../../docs/visual-checks/delivery-incidents-390-dark.png',
    element: incidentRegion,
  })
  document.documentElement.classList.remove('dark')

  await userEvent.click(
    incidentRegion.getByRole('button', { name: '确认业务恢复' })
  )
  await userEvent.fill(
    incidentRegion.getByRole('textbox', { name: '业务验证依据' }),
    '交易探针恢复，并完成人工验证下单'
  )
  await userEvent.click(
    incidentRegion.getByRole('button', { name: '保存恢复确认' })
  )
  await expect
    .poll(
      () =>
        vi.mocked(platformApi.confirmDeliveryIncidentRecovery).mock.calls.length
    )
    .toBe(1)
  expect(platformApi.confirmDeliveryIncidentRecovery).toHaveBeenCalledWith(
    7,
    expect.objectContaining({
      expected_version: 4,
      confirmation: '交易探针恢复，并完成人工验证下单',
    })
  )
  client.clear()
})

it('keeps the incident timeline read only for viewers', async () => {
  useAuthStore.getState().auth.setUser({
    id: 3,
    username: 'viewer',
    display_name: 'Viewer',
    role: 'viewer',
    k8s_username: '',
    k8s_groups: [],
    enabled: true,
  })
  vi.mocked(platformApi.deliveryIncidents).mockResolvedValue({
    data: [
      {
        id: 8,
        deploy_log_id: 91,
        cluster_id: 2,
        app_name: 'shop',
        summary: '数据库连接池恢复',
        status: 'recovered',
        occurred_at: '2026-10-10T12:20:00Z',
        recorded_at: '2026-10-10T12:24:00Z',
        recorded_by: 'operator',
        version: 2,
        causality_status: 'unknown',
        recovery_occurred_at: '2026-10-10T12:50:00Z',
        recovery_recorded_at: '2026-10-10T12:52:00Z',
        recovery_recorded_by: 'operator',
        recovery_confirmation: '核心业务检查通过',
        events: [
          {
            id: 31,
            incident_id: 8,
            event_type: 'recovery_confirmed',
            deploy_log_id: 91,
            occurred_at: '2026-10-10T12:50:00Z',
            recorded_at: '2026-10-10T12:52:00Z',
            recorded_by: 'operator',
            detail: '核心业务检查通过',
          },
        ],
      },
    ],
  } as never)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DeliveryOverview />
    </QueryClientProvider>
  )
  const incidentRegion = screen.getByRole('region', {
    name: '线上故障与恢复事件',
  })
  await expect
    .element(incidentRegion.getByText('数据库连接池恢复'))
    .toBeInTheDocument()
  await expect
    .element(incidentRegion.getByText('业务恢复已确认'))
    .toBeInTheDocument()
  await expect
    .element(incidentRegion.getByRole('button', { name: '确认业务恢复' }))
    .not.toBeInTheDocument()
  await expect
    .element(incidentRegion.getByRole('button', { name: '登记线上事件' }))
    .not.toBeInTheDocument()
  await expect
    .element(incidentRegion.getByRole('button', { name: '记录因果判定' }))
    .not.toBeInTheDocument()
  await expect
    .element(incidentRegion.getByRole('button', { name: '记录修复处置' }))
    .not.toBeInTheDocument()
  client.clear()
})
afterEach(async () => {
  useAuthStore.getState().auth.reset()
  await page.viewport(1280, 800)
})

it('keeps unknown business metrics distinct and shows the drift observation', async () => {
  await page.viewport(390, 844)
  vi.mocked(platformApi.deliveryMetrics).mockResolvedValueOnce({
    data: defaultDeliveryMetrics({ invalid_timestamp_samples: 2 }),
  } as never)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DeliveryOverview />
    </QueryClientProvider>
  )
  expect(platformApi.deliveryMetrics).toHaveBeenCalledWith(30, {
    appName: undefined,
    clusterId: undefined,
  })
  await expect
    .element(
      screen.getByText('1 条历史任务没有创建时的环境等级记录，未计入生产指标。')
    )
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(
        '任务或审批时间异常：2 条记录存在缺失或无效时间，相应样本未纳入统计。请核对历史时间数据。'
      )
    )
    .toBeInTheDocument()
  await expect.element(screen.getByText('33.3%')).toBeInTheDocument()
  await expect.element(screen.getByText('1.5 小时')).toBeInTheDocument()
  await expect.element(screen.getByText('30 分钟')).toBeInTheDocument()
  await expect
    .element(screen.getByText(/基于 2 个已恢复事件；1 个尚未恢复/))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(
        '统计窗口内由操作员登记的生产事件。事件与部署的因果关系尚未验证，不作为 DORA 指标。'
      )
    )
    .toBeInTheDocument()
  const incidentRecordCoverage = screen.getByRole('region', {
    name: '已登记事件的归因与处置记录完整度',
  })
  await expect
    .element(
      incidentRecordCoverage.getByText(
        '只统计发生时间有效的已登记事件；这些数字不能证明平台外故障也已全部登记。'
      )
    )
    .toBeInTheDocument()
  await expect
    .element(incidentRecordCoverage.getByText('已有因果判定'))
    .toBeInTheDocument()
  await expect
    .element(incidentRecordCoverage.getByText('1 / 3'))
    .toBeInTheDocument()
  await expect
    .element(incidentRecordCoverage.getByText('已有完整处置分类'))
    .toBeInTheDocument()
  await expect
    .element(incidentRecordCoverage.getByText('2 / 3'))
    .toBeInTheDocument()
  await expect
    .element(incidentRecordCoverage.getByText('1 个待分类'))
    .toBeInTheDocument()
  const metricsRegion = screen
    .getByRole('region', { name: '执行概况' })
    .element()
  const incidentRecoveryRegion = screen
    .getByRole('region', { name: '线上事件恢复观察' })
    .element()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await page.screenshot({
    path: '../../../../docs/visual-checks/delivery-metrics-390-light.png',
    element: metricsRegion,
  })
  window.scrollTo(
    0,
    incidentRecoveryRegion.getBoundingClientRect().top + window.scrollY
  )
  await page.screenshot({
    path: '../../../../docs/visual-checks/delivery-incident-recovery-390-light.png',
    element: incidentRecoveryRegion,
  })
  window.scrollTo(0, 0)
  try {
    await page.viewport(1440, 1000)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
    await page.screenshot({
      path: '../../../../docs/visual-checks/delivery-metrics-1440-light.png',
      element: metricsRegion,
    })
    await page.viewport(1024, 768)
    document.documentElement.classList.add('dark')
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
    await page.screenshot({
      path: '../../../../docs/visual-checks/delivery-metrics-1024-dark.png',
      element: metricsRegion,
    })
    document.documentElement.classList.remove('dark')
    await page.viewport(390, 844)
    document.documentElement.classList.add('dark')
    await new Promise((resolve) => setTimeout(resolve, 200))
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
    window.scrollTo(
      0,
      incidentRecoveryRegion.getBoundingClientRect().top + window.scrollY
    )
    await page.screenshot({
      path: '../../../../docs/visual-checks/delivery-incident-recovery-390-dark.png',
      element: incidentRecoveryRegion,
    })
    window.scrollTo(0, 0)
    await page.screenshot({
      path: '../../../../docs/visual-checks/delivery-metrics-390-dark.png',
      element: metricsRegion,
    })
    document.documentElement.classList.remove('dark')
    await page.viewport(512, 384)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(512)
    window.scrollTo(
      0,
      incidentRecoveryRegion.getBoundingClientRect().top + window.scrollY
    )
    await page.screenshot({
      path: '../../../../docs/visual-checks/delivery-incident-recovery-512x384.png',
      element: incidentRecoveryRegion,
    })
  } finally {
    document.documentElement.classList.remove('dark')
    await page.viewport(390, 844)
  }
  await expect.element(screen.getByText(/统计范围/)).toBeInTheDocument()
  await expect.element(screen.getByText(/2026\/09\/10/)).toBeInTheDocument()
  await expect.element(screen.getByText(/2026\/10\/10/)).toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('combobox', { name: '发布指标统计窗口' })
  )
  await userEvent.click(screen.getByRole('option', { name: '近 7 天' }))
  await expect
    .element(screen.getByText('最近 7 天发起的生产任务，包含重试和镜像回退。'))
    .toBeInTheDocument()
  expect(platformApi.deliveryMetrics).toHaveBeenCalledWith(7, {
    appName: undefined,
    clusterId: undefined,
  })
  await userEvent.click(screen.getByRole('combobox', { name: '应用' }))
  await expect
    .element(screen.getByRole('option', { name: 'shop' }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('option', { name: 'test-only' }))
    .not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('option', { name: 'shop' }))
  expect(platformApi.deliveryMetrics).toHaveBeenCalledWith(7, {
    appName: 'shop',
    clusterId: undefined,
  })
  await userEvent.click(screen.getByRole('combobox', { name: '生产目标' }))
  await expect
    .element(screen.getByRole('option', { name: '生产 A' }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('option', { name: '测试环境' }))
    .not.toBeInTheDocument()
  await userEvent.click(screen.getByRole('option', { name: '生产 A' }))
  expect(platformApi.deliveryMetrics).toHaveBeenCalledWith(7, {
    appName: undefined,
    clusterId: 2,
  })
  vi.mocked(platformApi.deliveryMetrics).mockResolvedValueOnce({
    data: {
      window_days: 7,
      window_start: '2026-10-03T12:00:00Z',
      as_of: '2026-10-10T12:00:00Z',
      unknown_tier_samples: 0,
      production_attempts: 1,
      successful_deployments: 1,
      execution_failures: 0,
      active_tasks: 0,
      pinned_deployments: 1,
      duration_samples: 0,
      approval_wait_samples: 0,
      pending_approvals: 0,
      execution_failure_rate: 0,
      execution_p95_seconds: null,
      approval_wait_p95_seconds: null,
      incident_recovery: {
        data_available: true,
        incident_samples: 0,
        recovered_samples: 0,
        unrecovered_samples: 0,
        intervention_recovery_samples: 0,
        invalid_timestamp_samples: 0,
        causality_assessed_samples: 0,
        causality_pending_samples: 0,
        remediation_classified_samples: 0,
        remediation_pending_samples: 0,
        mean_recovery_seconds: null,
        mean_intervention_to_recovery_seconds: null,
        missing_data: '窗口内没有人工登记事件',
      },
      dora: {
        deployment_frequency: { value: 1, missing_data: '' },
        change_lead_time: {
          value: 5400,
          missing_data: '',
          sample_count: 3,
        },
        change_fail_rate: {
          value: null,
          missing_data: '因果判定、修复处置及计划内/外记录覆盖率尚不完整',
        },
      },
    },
  } as never)
  await userEvent.click(screen.getByRole('combobox', { name: '应用' }))
  await userEvent.click(screen.getByRole('option', { name: 'shop' }))
  expect(platformApi.deliveryMetrics).toHaveBeenCalledWith(7, {
    appName: 'shop',
    clusterId: 2,
  })
  await userEvent.click(screen.getByText('DORA 指标的数据完整性'))
  await expect.element(screen.getByText('1 次 / 7 天')).toBeInTheDocument()
  await expect
    .element(screen.getByText('变更前置时间中位数', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('1.5 小时 · 3 次发布', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('变更失败率', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText('未知 — 因果判定、修复处置及计划内/外记录覆盖率尚不完整')
    )
    .toBeInTheDocument()
  await userEvent.click(
    screen.getByRole('combobox', { name: '选择集群查看实际状态' })
  )
  await userEvent.click(screen.getByRole('option', { name: '生产 A' }))
  await expect
    .element(
      screen
        .getByRole('region', { name: '镜像漂移观测' })
        .getByRole('list')
        .first()
        .getByText('发现镜像漂移', { exact: true })
    )
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('实际镜像', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('reg.io/shop:changed', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('状态变化记录', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('状态变化', { exact: true }))
    .toBeInTheDocument()
  await expect.element(screen.getByText('持续漂移')).toBeInTheDocument()
  expect(platformApi.deliveryDrift).toHaveBeenCalledWith(2)
  expect(platformApi.deliveryDriftHistory).toHaveBeenCalledWith(
    2,
    undefined,
    25
  )
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  client.clear()
})

it('shows failed observations as unknown and exposes exhausted admin notifications', async () => {
  vi.mocked(platformApi.deliveryMetrics).mockRejectedValue(
    new Error('metrics unavailable')
  )
  vi.mocked(platformApi.clusters).mockRejectedValue(
    new Error('cluster list unavailable')
  )
  vi.mocked(platformApi.apps).mockRejectedValue(
    new Error('app list unavailable')
  )
  vi.mocked(platformApi.deliveryNotificationAlerts).mockResolvedValue({
    data: {
      items: [
        {
          id: 'event-failed',
          event: 'release.failed',
          status: 'dead',
          attempts: 8,
          last_error: 'HTTP 503',
          created_at: '2026-10-09T06:12:00Z',
          next_attempt_at: '2026-10-09T06:12:00Z',
        },
      ],
      has_more: false,
    },
  } as never)
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'admin',
    display_name: 'Admin',
    role: 'admin',
    k8s_username: '',
    k8s_groups: [],
    enabled: true,
  })

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DeliveryOverview />
    </QueryClientProvider>
  )

  await expect
    .element(screen.getByText('执行指标加载失败，当前无法确认发布概况。'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('集群列表加载失败，暂时无法选择观测目标。'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('重试已耗尽', { exact: true }))
    .toBeInTheDocument()
  await expect.element(screen.getByText('HTTP 503')).toBeInTheDocument()
  await expect
    .element(screen.getByText('暂无数据', { exact: true }))
    .not.toBeInTheDocument()

  client.clear()
})

it('loads older active notification alerts in stable cursor pages', async () => {
  await page.viewport(390, 844)
  vi.mocked(platformApi.deliveryNotificationAlerts).mockImplementation(
    async (cursor) =>
      ({
        data: cursor
          ? {
              items: [
                {
                  id: 'event-older',
                  event: 'deploy_failed',
                  status: 'dead',
                  attempts: 8,
                  last_error: 'HTTP 502',
                  created_at: '2026-10-08T06:12:00Z',
                  next_attempt_at: '2026-10-08T06:12:00Z',
                },
              ],
              has_more: false,
            }
          : {
              items: [
                {
                  id: 'event-new',
                  event: 'deploy_failed',
                  status: 'dead',
                  attempts: 8,
                  last_error: 'HTTP 503',
                  created_at: '2026-10-09T06:12:00Z',
                  next_attempt_at: '2026-10-09T06:12:00Z',
                },
              ],
              next_cursor: 'event-new',
              has_more: true,
            },
      }) as never
  )
  useAuthStore.getState().auth.setUser({
    id: 1,
    username: 'admin',
    display_name: 'Admin',
    role: 'admin',
    k8s_username: '',
    k8s_groups: [],
    enabled: true,
  })

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DeliveryOverview />
    </QueryClientProvider>
  )

  await expect.element(screen.getByText('HTTP 503')).toBeInTheDocument()
  const notificationRegion = screen
    .getByRole('region', { name: '通知投递' })
    .element()
  await page.screenshot({
    path: '../../../../docs/visual-checks/delivery-notification-alerts-390-light.png',
    element: notificationRegion,
  })
  try {
    document.documentElement.classList.add('dark')
    await new Promise((resolve) => setTimeout(resolve, 200))
    await page.screenshot({
      path: '../../../../docs/visual-checks/delivery-notification-alerts-390-dark.png',
      element: notificationRegion,
    })
    await page.viewport(1024, 768)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
    await page.screenshot({
      path: '../../../../docs/visual-checks/delivery-notification-alerts-1024-dark.png',
      element: notificationRegion,
    })
  } finally {
    document.documentElement.classList.remove('dark')
    await new Promise((resolve) => setTimeout(resolve, 200))
    await page.viewport(390, 844)
  }
  await expect
    .element(screen.getByRole('button', { name: '加载更早通知' }))
    .toBeEnabled()
  await userEvent.click(screen.getByRole('button', { name: '加载更早通知' }))
  await expect.element(screen.getByText('HTTP 502')).toBeInTheDocument()
  expect(platformApi.deliveryNotificationAlerts).toHaveBeenCalledWith(
    'event-new'
  )
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)

  client.clear()
})
