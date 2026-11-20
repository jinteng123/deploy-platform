import { AxiosHeaders } from 'axios'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { toast } from 'sonner'
import { expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import {
  platformApi,
  type DeliveryRunView,
  type PublicUser,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import {
  BlueGreenPreflight,
  BlueGreenRunControls,
  CanaryRunControls,
  CanaryRestoreVerificationControl,
  RollingRunControls,
} from './delivery-plan-dialog'

const operator: PublicUser = {
  id: 1,
  username: 'operator',
  display_name: '操作员',
  role: 'operator',
  k8s_username: 'operator',
  k8s_groups: [],
  enabled: true,
}

function contrastRatio(foreground: string, background: string): number {
  const luminance = (color: string) => {
    const canvas = document.createElement('canvas')
    canvas.width = 1
    canvas.height = 1
    const context = canvas.getContext('2d')
    if (!context) throw new Error('Canvas is unavailable for contrast checks')
    context.fillStyle = color
    context.fillRect(0, 0, 1, 1)
    const [red, green, blue] = Array.from(
      context.getImageData(0, 0, 1, 1).data.slice(0, 3),
      (channel) => {
        const normalized = channel / 255
        return normalized <= 0.04045
          ? normalized / 12.92
          : ((normalized + 0.055) / 1.055) ** 2.4
      }
    )
    return 0.2126 * red + 0.7152 * green + 0.0722 * blue
  }
  const [lighter, darker] = [luminance(foreground), luminance(background)].sort(
    (first, second) => second - first
  )
  return (lighter + 0.05) / (darker + 0.05)
}

function rollingRun(stage: string, status = 'running') {
  return {
    run: {
      id: 51,
      delivery_plan_id: 21,
      promotion_request_id: 31,
      deploy_log_id: 41,
      strategy: 'rolling',
      status,
      current_stage: stage,
      version: 7,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    },
  } as DeliveryRunView
}

async function renderRolling(run: DeliveryRunView) {
  useAuthStore.getState().auth.setUser(operator)
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <RollingRunControls run={run} />
    </QueryClientProvider>
  )
}

async function renderBlueGreen(run: DeliveryRunView) {
  useAuthStore.getState().auth.setUser(operator)
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <BlueGreenRunControls run={run} />
    </QueryClientProvider>
  )
}

function canaryRun(role: PublicUser['role'] = 'operator'): DeliveryRunView {
  useAuthStore.getState().auth.setUser({ ...operator, role })
  return {
    run: {
      id: 53,
      delivery_plan_id: 23,
      promotion_request_id: 33,
      deploy_log_id: 43,
      strategy: 'gateway_canary',
      status: 'running',
      current_stage: 'canary_stage_1',
      version: 9,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    },
    plan: {
      strategy: 'gateway_canary',
      snapshot: {
        policy: {
          canary_analysis: {
            stages: [
              {
                candidate_weight_percent: 10,
                observation_duration_seconds: 60,
              },
              {
                candidate_weight_percent: 50,
                observation_duration_seconds: 120,
              },
              {
                candidate_weight_percent: 100,
                observation_duration_seconds: 180,
              },
            ],
          },
        },
      },
    } as DeliveryRunView['plan'],
    deploy_log: {} as DeliveryRunView['deploy_log'],
    events: [
      {
        id: 4,
        run_id: 53,
        version: 8,
        event_key: 'canary:stage-1:confirmation',
        event_type: 'canary_checkpoint',
        stage: 'canary_stage_1',
        status: 'running',
        payload: {
          schema_version: 1,
          stage_index: 0,
          candidate_weight_percent: 10,
          observation_duration_seconds: 60,
          phase: 'awaiting_confirmation',
          stage_started_at: '2026-10-13T08:00:00.000Z',
          effective_weights: {
            stable_weight_percent: 90,
            candidate_weight_percent: 10,
          },
          observation: {
            stable_image_digest: `sha256:${'a'.repeat(64)}`,
            candidate_image_digest: `sha256:${'b'.repeat(64)}`,
            window_start: '2026-10-13T08:00:00.000Z',
            window_end: '2026-10-13T08:01:00.000Z',
            observed_at: '2026-10-13T08:01:01.000Z',
            stable: {
              image_digest: `sha256:${'a'.repeat(64)}`,
              requests: 200,
              errors: 1,
              p95_latency_ms: 100,
              health: 'healthy',
            },
            candidate: {
              image_digest: `sha256:${'b'.repeat(64)}`,
              requests: 200,
              errors: 1,
              p95_latency_ms: 105,
              health: 'healthy',
            },
          },
          decision: {
            analysis: {
              status: 'awaiting_confirmation',
              reason: 'within_thresholds_manual_confirmation_required',
            },
          },
        },
        created_at: '2026-10-13T08:01:01.000Z',
      },
    ],
    actions: [],
  } as DeliveryRunView
}

function blueGreenRun(stage: string): DeliveryRunView {
  return {
    run: {
      id: 52,
      delivery_plan_id: 22,
      promotion_request_id: 32,
      deploy_log_id: 42,
      strategy: 'blue_green',
      status: 'running',
      current_stage: stage,
      version: 8,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    },
    plan: {
      strategy: 'blue_green',
      snapshot: {
        artifact: {
          image: `registry.example.com/orders/api@sha256:${'a'.repeat(64)}`,
        },
        target: {
          environment_name: '生产',
          service_name: 'orders-entry',
          stable_image_digest: `sha256:${'b'.repeat(64)}`,
          stable_configuration_source: {
            repository_url: 'https://git.example.com/orders/deploy.git',
            path: 'charts/orders',
            revision: 'c'.repeat(40),
            recorded_by: 'runner:orders',
            recorded_at: '2026-10-21T08:00:00Z',
          },
        },
        candidate_retention_hours: 24,
      },
    } as DeliveryRunView['plan'],
    deploy_log: { finished_at: null } as DeliveryRunView['deploy_log'],
    events: [],
    actions: [],
  }
}

it('shows unknown blue-green checks as unresolved, not passed', async () => {
  const screen = await render(
    <BlueGreenPreflight
      checks={[
        {
          key: 'service-entry',
          status: 'unknown',
          title: '流量入口唯一性',
          detail: '无法枚举 namespace 内所有 Service。',
        },
        {
          key: 'capacity',
          status: 'review',
          title: '双版本容量',
          detail: '需要额外容纳 2 个候选副本。',
        },
        {
          key: 'workload',
          status: 'blocked',
          title: '稳定版本健康',
          detail: '当前 Pod 未全部就绪。',
        },
      ]}
      stableLabelKey='delivery.platform.io/app-2'
      candidateLabelKey='delivery.platform.io/run-id'
      candidateLabelValue='r-abcdef123456'
      removedLabelKeys={['app', 'component']}
    />
  )

  await expect.element(screen.getByText('状态未知')).toBeInTheDocument()
  await expect
    .element(screen.getByText('需核对', { exact: true }))
    .toBeInTheDocument()
  await expect.element(screen.getByText('0 项通过')).toBeInTheDocument()
  await expect.element(screen.getByText('1 项阻断')).toBeInTheDocument()
  await expect.element(screen.getByText('1 项未知')).toBeInTheDocument()
  await expect
    .element(screen.getByText('无法枚举 namespace 内所有 Service。'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('delivery.platform.io/run-id=r-abcdef123456'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('app', { exact: true }))
    .toBeInTheDocument()
})

it('explains that rolling pause does not stop the Kubernetes controller', async () => {
  const screen = await renderRolling(rollingRun('rollout_wait'))

  await expect
    .element(screen.getByText(/Deployment 控制器仍会继续替换 Pod/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '暂停平台检查' }))
    .toBeInTheDocument()
})

it('requires confirmation before cancelling before the image write', async () => {
  const screen = await renderRolling(rollingRun('started'))

  await screen.getByRole('button', { name: '取消发布' }).click()
  await expect
    .element(screen.getByText('在镜像写入前取消发布？'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '确认取消' }))
    .toBeInTheDocument()
})

it('explains the production recovery impact before cancelling blue-green', async () => {
  await page.viewport(390, 844)
  const screen = await renderBlueGreen(
    blueGreenRun('awaiting_candidate_review')
  )
  const controlCard = screen
    .getByRole('region', { name: '蓝绿发布控制' })
    .element()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await page.screenshot({
    path: '../../../../docs/visual-checks/blue-green-candidate-390-light.png',
    element: controlCard,
  })
  document.documentElement.classList.add('dark')
  await page.screenshot({
    path: '../../../../docs/visual-checks/blue-green-candidate-390-dark.png',
    element: controlCard,
  })
  document.documentElement.classList.remove('dark')
  await page.viewport(1440, 1000)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
  await page.screenshot({
    path: '../../../../docs/visual-checks/blue-green-candidate-1440-light.png',
    element: controlCard,
  })

  await screen.getByRole('button', { name: '取消发布' }).click()
  await expect
    .element(screen.getByRole('heading', { name: '取消发布并恢复稳定版本？' }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/核对生产 Service，并恢复已记录的稳定入口/))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(/不回退数据库迁移、ConfigMap\/Secret 或 Helm\/Git 配置/)
    )
    .toBeInTheDocument()
  const compatibilityAcknowledgement = screen.getByRole('checkbox', {
    name: '确认稳定版本兼容当前数据库与外部配置',
  })
  await expect.element(compatibilityAcknowledgement).toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '确认取消并恢复' }))
    .toBeDisabled()
  await expect
    .element(screen.getByText('生产 · orders-entry'))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(
        `registry.example.com/orders/api@sha256:${'a'.repeat(64)}`
      )
    )
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(`sha256:${'b'.repeat(64)}`))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(
        'https://git.example.com/orders/deploy.git / charts/orders'
      )
    )
    .toBeInTheDocument()
  await expect.element(screen.getByText('c'.repeat(40))).toBeInTheDocument()
  await expect
    .element(screen.getByText(/历史来源记录，不代表当前集群配置已回退/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '确认取消并恢复' }))
    .toBeInTheDocument()
  const confirmation = screen.getByRole('dialog').element()
  const overlay = document.querySelector<HTMLElement>(
    '[data-slot="dialog-overlay"]'
  )
  expect(overlay).not.toBeNull()
  expect(Number(getComputedStyle(confirmation).zIndex)).toBeGreaterThan(
    Number(getComputedStyle(overlay!).zIndex)
  )
  for (const viewport of [
    { width: 1024, height: 768, name: '1024', dark: true },
    { width: 390, height: 844, name: '390', dark: true },
    { width: 512, height: 384, name: '512x384', dark: false },
    { width: 512, height: 384, name: '512x384', dark: true },
  ]) {
    await page.viewport(viewport.width, viewport.height)
    await new Promise((resolve) => setTimeout(resolve, 250))
    if (viewport.dark) document.documentElement.classList.add('dark')
    else document.documentElement.classList.remove('dark')
    if (!viewport.dark) {
      expect(document.documentElement.classList.contains('dark')).toBe(false)
      expect(getComputedStyle(confirmation).backgroundColor).toBe(
        'rgb(242, 245, 251)'
      )
    }
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(
      viewport.width
    )
    const bounds = confirmation.getBoundingClientRect()
    expect(
      bounds.top,
      JSON.stringify({
        windowHeight: window.innerHeight,
        clientHeight: document.documentElement.clientHeight,
        visualHeight: window.visualViewport?.height,
        compactMedia: window.matchMedia('(max-height: 28rem)').matches,
        viewportHeight: viewport.height,
        top: getComputedStyle(confirmation).top,
        transform: getComputedStyle(confirmation).transform,
        translate: getComputedStyle(confirmation).translate,
        className: confirmation.className,
        height: bounds.height,
        maxHeight: getComputedStyle(confirmation).maxHeight,
      })
    ).toBeGreaterThanOrEqual(0)
    expect(bounds.bottom).toBeLessThanOrEqual(viewport.height)
    if (viewport.height < 448) {
      const details =
        confirmation.querySelector<HTMLElement>('.overflow-y-auto')
      expect(details?.clientHeight).toBeGreaterThan(0)
      expect(details!.scrollHeight).toBeGreaterThan(details!.clientHeight)
    }
    const confirmButton = screen.getByRole('button', {
      name: '确认取消并恢复',
    })
    const deferButton = screen.getByRole('button', { name: '暂不取消' })
    await expect.element(compatibilityAcknowledgement).toBeVisible()
    await expect.element(confirmButton).toBeVisible()
    await expect.element(confirmButton).toBeDisabled()
    await expect.element(deferButton).toBeEnabled()
    expect(getComputedStyle(deferButton.element()).opacity).toBe('1')
    await page.screenshot({
      path: `../../../../docs/visual-checks/blue-green-cancel-${viewport.name}-${viewport.dark ? 'dark' : 'light'}.png`,
    })
    if (viewport.height < 448 && !viewport.dark) {
      const details =
        confirmation.querySelector<HTMLElement>('.overflow-y-auto')!
      details.scrollTop = details.scrollHeight
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const revision = screen.getByText('c'.repeat(40)).element()
      expect(revision.getBoundingClientRect().bottom).toBeLessThanOrEqual(
        details.getBoundingClientRect().bottom
      )
      await page.screenshot({
        path: '../../../../docs/visual-checks/blue-green-cancel-512x384-scrolled-light.png',
      })
      details.scrollTop = 0
    }
  }
  await userEvent.click(compatibilityAcknowledgement)
  await expect
    .element(screen.getByRole('button', { name: '确认取消并恢复' }))
    .toBeEnabled()
  document.documentElement.classList.remove('dark')
})

it('marks a blue-green recovery result needing attention as unconfirmed', async () => {
  const run = blueGreenRun('awaiting_candidate_review')
  delete run.plan.snapshot.target.stable_image_digest
  delete run.plan.snapshot.target.stable_configuration_source
  const result = {
    ...run,
    run: { ...run.run, current_stage: 'needs_attention' },
  }
  const action = vi.spyOn(platformApi, 'deliveryRunAction').mockResolvedValue({
    data: result,
    status: 200,
    statusText: 'OK',
    headers: {},
    config: { headers: new AxiosHeaders() },
  })
  const success = vi.spyOn(toast, 'success').mockImplementation(() => '')
  const error = vi.spyOn(toast, 'error').mockImplementation(() => '')
  const screen = await renderBlueGreen(run)

  await screen.getByRole('button', { name: '取消发布' }).click()
  await expect
    .element(screen.getByText('未固定，需人工核对稳定 Deployment'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('来源未知，需人工核对当前配置。'))
    .toBeInTheDocument()
  await screen
    .getByRole('checkbox', {
      name: '确认稳定版本兼容当前数据库与外部配置',
    })
    .click()
  await screen.getByRole('button', { name: '确认取消并恢复' }).click()
  await expect.poll(() => action.mock.calls.length).toBe(1)
  await expect.poll(() => error.mock.calls.length).toBe(1)
  expect(error).toHaveBeenCalledWith(
    '生产入口尚未确认切回稳定版，请先核对集群状态'
  )
  expect(success).not.toHaveBeenCalled()
  vi.restoreAllMocks()
})

it('shows a clear canary confirmation gate with the exact next planned weight', async () => {
  const screen = await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRunControls run={canaryRun()} />
    </QueryClientProvider>
  )

  await expect.element(screen.getByText('阶段观测通过')).toBeInTheDocument()
  await expect
    .element(screen.getByText(/已读回候选流量 10%/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('下一阶段计划候选流量 50%。'))
    .toBeInTheDocument()
  await page.viewport(390, 844)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const confirmationCard = screen
    .getByRole('region', { name: '金丝雀阶段人工确认与控制' })
    .element()
  const confirmationButton = screen
    .getByRole('button', { name: '确认进入下一阶段' })
    .element()
  const pauseButton = screen
    .getByRole('button', { name: '暂停此阶段' })
    .element()
  await expect
    .element(screen.getByRole('button', { name: '确认进入下一阶段' }))
    .toBeEnabled()
  await expect
    .element(screen.getByRole('button', { name: '暂停此阶段' }))
    .toBeEnabled()
  expect(
    confirmationButton.getBoundingClientRect().height
  ).toBeGreaterThanOrEqual(44)
  expect(pauseButton.getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
  const lightSurface = getComputedStyle(confirmationCard).backgroundImage
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-confirmation-390-light.png',
    element: confirmationCard,
  })
  document.documentElement.classList.add('dark')
  expect(getComputedStyle(confirmationCard).backgroundImage).not.toBe(
    lightSurface
  )
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-confirmation-390-dark.png',
    element: confirmationCard,
  })
  document.documentElement.classList.remove('dark')
  await page.viewport(1024, 768)
  document.documentElement.classList.add('dark')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-control-1024-dark.png',
    element: confirmationCard,
  })
  document.documentElement.classList.remove('dark')
  await page.viewport(1440, 900)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-control-1440-light.png',
    element: confirmationCard,
  })
  await screen.getByRole('button', { name: '确认进入下一阶段' }).click()
  await expect
    .element(screen.getByRole('heading', { name: '确认进入下一阶段？' }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '确认并继续' }))
    .toBeInTheDocument()
})

it('lets an operator cancel an unclaimed canary before external actions', async () => {
  const run = canaryRun()
  run.run.status = 'queued'
  run.run.current_stage = 'queued'
  run.events = []
  const action = vi.spyOn(platformApi, 'deliveryRunAction').mockResolvedValue({
    data: run,
    status: 200,
    statusText: 'OK',
    headers: {},
    config: { headers: new AxiosHeaders() },
  })
  const screen = await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRunControls run={run} />
    </QueryClientProvider>
  )

  await expect
    .element(screen.getByRole('heading', { name: '金丝雀发布等待执行' }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/Worker 已领取但尚未开始外部写入时也适用/))
    .toBeInTheDocument()
  await page.viewport(390, 844)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const queuedCard = screen
    .getByRole('region', { name: '金丝雀取消控制' })
    .element()
  const cancelButton = screen
    .getByRole('button', { name: '取消发布' })
    .element()
  expect(cancelButton.getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-queued-cancel-390-light.png',
    element: queuedCard,
  })
  document.documentElement.classList.add('dark')
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-queued-cancel-390-dark.png',
    element: queuedCard,
  })
  document.documentElement.classList.remove('dark')

  await screen.getByRole('button', { name: '取消发布' }).click()
  await expect
    .element(
      screen.getByRole('heading', {
        name: '确认取消尚未产生外部动作的金丝雀任务？',
      })
    )
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/撤销认领、结束任务并释放资源锁/))
    .toBeInTheDocument()
  await screen.getByRole('button', { name: '确认取消发布' }).click()
  expect(action).toHaveBeenCalledWith(
    run.run.id,
    expect.objectContaining({
      action: 'cancel',
      expected_version: run.run.version,
    })
  )
})

it('allows cancellation after Worker claim when no external action is recorded', async () => {
  const run = canaryRun()
  run.run.status = 'running'
  run.run.current_stage = 'preparing'
  run.deploy_log.status = 'running'
  run.events = []
  const action = vi.spyOn(platformApi, 'deliveryRunAction').mockResolvedValue({
    data: run,
    status: 200,
    statusText: 'OK',
    headers: {},
    config: { headers: new AxiosHeaders() },
  })
  const screen = await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRunControls run={run} />
    </QueryClientProvider>
  )
  await expect
    .element(screen.getByRole('heading', { name: '金丝雀任务正在准备' }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('Worker 已领取', { exact: true }))
    .toBeInTheDocument()
  await page.viewport(390, 844)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const card = screen.getByRole('region', { name: '金丝雀取消控制' }).element()
  await expect
    .element(screen.getByRole('button', { name: '取消发布' }))
    .toBeEnabled()
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-claimed-cancel-390-light.png',
    element: card,
  })
  document.documentElement.classList.add('dark')
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-claimed-cancel-390-dark.png',
    element: card,
  })
  document.documentElement.classList.remove('dark')
  await screen.getByRole('button', { name: '取消发布' }).click()
  await screen.getByRole('button', { name: '确认取消发布' }).click()
  expect(action).toHaveBeenCalledWith(
    run.run.id,
    expect.objectContaining({
      action: 'cancel',
      expected_version: run.run.version,
    })
  )
})

it('keeps canary cancellation unavailable to read-only users', async () => {
  const run = canaryRun('viewer')
  run.run.status = 'queued'
  run.run.current_stage = 'queued'
  run.events = []
  const screen = await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRunControls run={run} />
    </QueryClientProvider>
  )

  await expect
    .element(screen.getByRole('button', { name: '取消发布' }))
    .toBeDisabled()
  await expect
    .element(screen.getByText('需要发布操作员或管理员权限才能取消金丝雀发布。'))
    .toBeInTheDocument()
})

it('exposes a pause action at the healthy canary confirmation gate', async () => {
  const run = canaryRun()
  const action = vi.spyOn(platformApi, 'deliveryRunAction').mockResolvedValue({
    data: run,
    status: 200,
    statusText: 'OK',
    headers: {},
    config: { headers: new AxiosHeaders() },
  })
  const screen = await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRunControls run={run} />
    </QueryClientProvider>
  )

  const pauseButton = screen.getByRole('button', { name: '暂停此阶段' })
  expect(
    pauseButton.element().getBoundingClientRect().height
  ).toBeGreaterThanOrEqual(44)
  await pauseButton.click()
  expect(action).toHaveBeenCalledWith(
    run.run.id,
    expect.objectContaining({
      action: 'pause',
      expected_version: run.run.version,
    })
  )
})

it('resumes a paused canary at its preserved confirmation gate', async () => {
  const pausedRun = canaryRun()
  pausedRun.run.status = 'paused'
  const action = vi.spyOn(platformApi, 'deliveryRunAction').mockResolvedValue({
    data: pausedRun,
    status: 200,
    statusText: 'OK',
    headers: {},
    config: { headers: new AxiosHeaders() },
  })
  const pausedScreen = await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRunControls run={pausedRun} />
    </QueryClientProvider>
  )
  await expect
    .element(pausedScreen.getByText('金丝雀阶段已暂停'))
    .toBeInTheDocument()
  await expect
    .element(pausedScreen.getByText(/确认前会重新检查观测时效/))
    .toBeInTheDocument()
  await page.viewport(390, 844)
  const pausedCard = pausedScreen
    .getByRole('region', { name: '金丝雀阶段人工确认与控制' })
    .element()
  const resumeButton = pausedScreen
    .getByRole('button', { name: '继续当前阶段' })
    .element()
  await expect
    .element(pausedScreen.getByRole('button', { name: '继续当前阶段' }))
    .toBeEnabled()
  expect(resumeButton.getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const lightSurface = getComputedStyle(pausedCard).backgroundImage
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-paused-390-light.png',
    element: pausedCard,
  })
  document.documentElement.classList.add('dark')
  expect(getComputedStyle(pausedCard).backgroundImage).not.toBe(lightSurface)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-paused-390-dark.png',
    element: pausedCard,
  })
  document.documentElement.classList.remove('dark')
  await page.viewport(1024, 768)
  document.documentElement.classList.add('dark')
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-paused-1024-dark.png',
    element: pausedCard,
  })
  document.documentElement.classList.remove('dark')
  await page.viewport(1440, 900)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-paused-1440-light.png',
    element: pausedCard,
  })
  await pausedScreen.getByRole('button', { name: '继续当前阶段' }).click()
  expect(action).toHaveBeenCalledWith(
    pausedRun.run.id,
    expect.objectContaining({
      action: 'continue',
      expected_version: pausedRun.run.version,
    })
  )
})

it('keeps canary confirmation unavailable to read-only users', async () => {
  const screen = await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRunControls run={canaryRun('viewer')} />
    </QueryClientProvider>
  )

  const button = screen.getByRole('button', { name: '确认进入下一阶段' })
  await expect.element(button).toBeDisabled()
  await expect
    .element(screen.getByRole('button', { name: '暂停此阶段' }))
    .toBeDisabled()
  await expect
    .element(screen.getByText('需要发布操作员或管理员确认或暂停此阶段。'))
    .toBeInTheDocument()
})

function awaitingCanaryRestoreRun(role: PublicUser['role'] = 'operator') {
  const run = canaryRun(role)
  run.run.current_stage = 'awaiting_restore_verification'
  run.events = [
    {
      id: 9,
      run_id: run.run.id,
      version: run.run.version,
      event_key: 'canary:restore:restore-key-123',
      event_type: 'canary_restore',
      stage: 'awaiting_restore_verification',
      status: 'running',
      detail: '稳定版流量已回读为 100%，候选版为 0%；请完成业务验证。',
      payload: {
        schema_version: 1,
        stage_index: 0,
        stable_image_digest: `sha256:${'a'.repeat(64)}`,
        candidate_image_digest: `sha256:${'b'.repeat(64)}`,
        route: {
          connection_id: 2,
          route_id: 'orders-public',
          stable_backend_id: 'orders-stable',
          candidate_backend_id: 'orders-candidate',
        },
        effective_weights: {
          stable_weight_percent: 100,
          candidate_weight_percent: 0,
        },
        route_revision: 'route-revision-10',
        restored_at: '2026-10-13T08:05:00.000Z',
      },
      created_at: '2026-10-13T08:05:01.000Z',
    },
  ]
  return run
}

it('requires explicit business verification before ending a restored canary run', async () => {
  const run = awaitingCanaryRestoreRun()
  const action = vi.spyOn(platformApi, 'deliveryRunAction').mockResolvedValue({
    data: run,
    status: 200,
    statusText: 'OK',
    headers: {},
    config: { headers: new AxiosHeaders() },
  })
  const screen = await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRestoreVerificationControl run={run} />
    </QueryClientProvider>
  )

  await expect
    .element(screen.getByText('确认恢复后的业务状态'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/以失败状态结束本次候选发布并释放资源锁/))
    .toBeInTheDocument()
  await page.viewport(390, 844)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const confirmationCard = screen
    .getByRole('region', { name: '金丝雀恢复后业务验证' })
    .element()
  const button = screen
    .getByRole('button', { name: '确认业务已恢复' })
    .element()
  expect(button.getBoundingClientRect().height).toBeGreaterThanOrEqual(44)
  const lightSurface = getComputedStyle(button).backgroundColor
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-restore-confirmation-390-light.png',
    element: confirmationCard,
  })
  document.documentElement.classList.add('dark')
  expect(getComputedStyle(button).backgroundColor).not.toBe(lightSurface)
  expect(
    contrastRatio(
      getComputedStyle(button).color,
      getComputedStyle(button).backgroundColor
    )
  ).toBeGreaterThanOrEqual(4.5)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-restore-confirmation-390-dark.png',
    element: confirmationCard,
  })
  document.documentElement.classList.remove('dark')

  await screen.getByRole('button', { name: '确认业务已恢复' }).click()
  await expect
    .element(screen.getByRole('heading', { name: '确认稳定版业务已恢复？' }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/如业务仍有异常，请关闭此窗口并保持运行待核对/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByRole('button', { name: '确认恢复并结束发布' }))
    .toBeInTheDocument()
  await screen.getByRole('button', { name: '确认恢复并结束发布' }).click()
  await expect(action).toHaveBeenCalledWith(
    run.run.id,
    expect.objectContaining({
      action: 'confirm_canary_restore',
      expected_version: run.run.version,
    })
  )
  action.mockRestore()
})

it('keeps restored-canary business confirmation unavailable to read-only users', async () => {
  const screen = await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRestoreVerificationControl
        run={awaitingCanaryRestoreRun('viewer')}
      />
    </QueryClientProvider>
  )

  await expect
    .element(screen.getByRole('button', { name: '确认业务已恢复' }))
    .toBeDisabled()
  await expect
    .element(screen.getByText('需要发布操作员或管理员确认恢复后的业务状态。'))
    .toBeInTheDocument()
})

it('does not offer business confirmation for an invalid stable-weight readback', async () => {
  const run = awaitingCanaryRestoreRun()
  run.events[0].payload = {
    ...(run.events[0].payload as Record<string, unknown>),
    effective_weights: {
      stable_weight_percent: 80,
      candidate_weight_percent: 20,
    },
  }
  await render(
    <QueryClientProvider client={new QueryClient()}>
      <CanaryRestoreVerificationControl run={run} />
    </QueryClientProvider>
  )

  expect(
    document.querySelector('section[aria-label="金丝雀恢复后业务验证"]')
  ).toBeNull()
})
