import '@/styles/index.css'
import { expect, it } from 'vitest'
import { render } from 'vitest-browser-react'
import { page } from 'vitest/browser'
import type { DeliveryRunEvent } from '@/api/client'
import {
  canaryCheckpointEventTitle,
  CanaryRestoreCheckpointCard,
  CanaryStageCheckpointCard,
} from './canary-stage-checkpoint'

function contrastRatio(foreground: string, background: string): number {
  function luminance(color: string): number {
    const canvas = document.createElement('canvas')
    canvas.width = 1
    canvas.height = 1
    const context = canvas.getContext('2d')
    if (!context || !CSS.supports('color', color)) {
      throw new Error(`Unsupported computed color: ${color}`)
    }
    context.fillStyle = color
    context.fillRect(0, 0, 1, 1)
    const pixel = context.getImageData(0, 0, 1, 1).data
    const channels = [pixel[0], pixel[1], pixel[2]]
    const [red, green, blue] = channels.map((channel) => {
      const normalized = channel / 255
      return normalized <= 0.04045
        ? normalized / 12.92
        : ((normalized + 0.055) / 1.055) ** 2.4
    })
    return 0.2126 * red + 0.7152 * green + 0.0722 * blue
  }

  const values = [luminance(foreground), luminance(background)].sort(
    (first, second) => second - first
  )
  return (values[0] + 0.05) / (values[1] + 0.05)
}

function checkpointEvent(payload: unknown): DeliveryRunEvent {
  return {
    id: 12,
    run_id: 8,
    version: 3,
    event_key: 'canary:stage-1:observing',
    event_type: 'canary_checkpoint',
    stage: 'canary_stage_1',
    status: 'running',
    detail: '指标通过，等待人工确认。',
    payload,
    created_at: '2026-10-12T14:08:05.000Z',
  }
}

it('shows planned weight, effective gateway readback and digest-separated observations', async () => {
  document.documentElement.classList.remove('dark')
  const screen = await render(
    <main className='mx-auto w-full max-w-2xl p-4'>
      <CanaryStageCheckpointCard
        event={checkpointEvent({
          schema_version: 2,
          stage_index: 0,
          candidate_weight_percent: 10,
          observation_duration_seconds: 60,
          phase: 'awaiting_confirmation',
          stage_started_at: '2026-10-12T14:00:00.000Z',
          effective_weights: {
            stable_weight_percent: 90,
            candidate_weight_percent: 10,
          },
          observation: {
            stable_image_digest: `sha256:${'a'.repeat(64)}`,
            candidate_image_digest: `sha256:${'b'.repeat(64)}`,
            window_start: '2026-10-12T14:00:00.000Z',
            window_end: '2026-10-12T14:01:00.000Z',
            observed_at: '2026-10-12T14:01:05.000Z',
            stable: {
              image_digest: `sha256:${'a'.repeat(64)}`,
              requests: 1000,
              errors: 5,
              p95_latency_ms: 120,
              health: 'healthy',
            },
            candidate: {
              image_digest: `sha256:${'b'.repeat(64)}`,
              requests: 170,
              errors: 1,
              p95_latency_ms: 130,
              health: 'healthy',
            },
          },
          decision: {
            evaluated_at: '2026-10-12T14:01:06.000Z',
            analysis: {
              status: 'awaiting_confirmation',
              reason: 'within_thresholds_manual_confirmation_required',
            },
          },
        })}
      />
    </main>
  )

  await expect
    .element(screen.getByRole('region', { name: '金丝雀第 1 阶段快照' }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('候选 10%', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('稳定 90% · 候选 10%'))
    .toBeInTheDocument()
  await expect.element(screen.getByText('候选 15%')).toBeInTheDocument()
  await expect
    .element(screen.getByText('等待人工确认', { exact: true }))
    .toBeInTheDocument()
  await expect.element(screen.getByText('指标在阈值内')).toBeInTheDocument()
  await expect.element(screen.getByText(/判定时间：/)).toBeInTheDocument()
  await expect.element(screen.getByText('0.50%')).toBeInTheDocument()
  await expect.element(screen.getByText('0.59%')).toBeInTheDocument()
  await expect.element(screen.getByText('120 ms')).toBeInTheDocument()

  await page.viewport(390, 844)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const card = screen
    .getByRole('region', { name: '金丝雀第 1 阶段快照' })
    .element()
  const phaseBadge = screen.getByText('等待人工确认', { exact: true }).element()
  const lightSurface = getComputedStyle(card).backgroundColor
  const lightBadgeText = getComputedStyle(phaseBadge).color
  expect(
    contrastRatio(lightBadgeText, getComputedStyle(phaseBadge).backgroundColor)
  ).toBeGreaterThanOrEqual(4.5)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-checkpoint-390-light.png',
    element: card,
  })
  document.documentElement.classList.add('dark')
  expect(getComputedStyle(card).backgroundColor).not.toBe(lightSurface)
  const darkBadgeText = getComputedStyle(phaseBadge).color
  const darkBadgeSurface = getComputedStyle(phaseBadge).backgroundColor
  expect(contrastRatio(darkBadgeText, darkBadgeSurface)).toBeGreaterThanOrEqual(
    4.5
  )
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-checkpoint-390-dark.png',
    element: card,
  })
  document.documentElement.classList.remove('dark')

  const digestDisclosure = screen.getByText('查看稳定版和候选版 digest')
  await digestDisclosure.click()
  await expect
    .element(screen.getByText(`sha256:${'a'.repeat(64)}`))
    .toBeVisible()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-stage-checkpoint-390-digests.png',
    element: card,
  })
  await digestDisclosure.click()

  for (const viewport of [
    { width: 1024, height: 768, name: '1024' },
    { width: 1440, height: 1000, name: '1440' },
  ]) {
    await page.viewport(viewport.width, viewport.height)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(
      viewport.width
    )
    await page.screenshot({
      path: `../../../../docs/visual-checks/canary-stage-checkpoint-${viewport.name}-light.png`,
      element: card,
    })
    document.documentElement.classList.add('dark')
    await page.screenshot({
      path: `../../../../docs/visual-checks/canary-stage-checkpoint-${viewport.name}-dark.png`,
      element: card,
    })
    document.documentElement.classList.remove('dark')
  }
})

it('shows an explicit unresolved state for malformed checkpoint payloads', async () => {
  const screen = await render(
    <CanaryStageCheckpointCard
      event={checkpointEvent({
        schema_version: 1,
        stage_index: 0,
        candidate_weight_percent: 10,
        observation_duration_seconds: 60,
        phase: 'observing',
        effective_weights: {
          stable_weight_percent: 80,
          candidate_weight_percent: 10,
        },
      })}
    />
  )

  await expect
    .element(screen.getByText('阶段快照无法识别，当前权重和观测状态不能确认。'))
    .toBeInTheDocument()
})

it('turns internal canary outcomes into concise operator-facing timeline labels', () => {
  const event = checkpointEvent({})
  event.detail =
    'source=canary_metrics;result=gateway_changed_during_observation;指标或网关状态无法核实，阶段未推进。'
  expect(canaryCheckpointEventTitle(event)).toBe(
    '指标读取期间网关路由发生变化，阶段未推进。'
  )

  event.detail =
    'action=confirm_canary_stage;actor=lin;已确认第 1 阶段观测健康；下一阶段计划候选流量 50%，等待网关权重回读。'
  expect(canaryCheckpointEventTitle(event)).toBe(
    '操作员 lin：已确认第 1 阶段观测健康；下一阶段计划候选流量 50%，等待网关权重回读。'
  )

  event.event_type = 'canary_restore'
  expect(canaryCheckpointEventTitle(event)).toBe(
    '稳定版 100%、候选版 0% 已回读，等待业务验证。'
  )
  event.event_type = 'canary_restore_verification'
  event.detail =
    'action=confirm_canary_restore;actor=lin;稳定流量恢复后的业务验证通过；本次金丝雀发布以失败状态结束，资源锁已释放。'
  expect(canaryCheckpointEventTitle(event)).toBe(
    '操作员 lin：稳定流量恢复后的业务验证通过；本次金丝雀发布以失败状态结束，资源锁已释放。'
  )
})

it('shows verified stable traffic restoration and preserves the business gate', async () => {
  document.documentElement.classList.remove('dark')
  const event = checkpointEvent({
    schema_version: 1,
    stage_index: 0,
    stable_image_digest: `sha256:${'a'.repeat(64)}`,
    candidate_image_digest: `sha256:${'b'.repeat(64)}`,
    route: {
      connection_id: 7,
      route_id: 'checkout-public',
      stable_backend_id: 'checkout-stable',
      candidate_backend_id: 'checkout-candidate',
    },
    effective_weights: {
      stable_weight_percent: 100,
      candidate_weight_percent: 0,
    },
    route_revision: 'revision-2',
    restored_at: '2026-10-13T08:05:00.000Z',
  })
  event.event_type = 'canary_restore'
  event.stage = 'awaiting_restore_verification'
  event.detail =
    '稳定版流量已回读为 100%，候选版为 0%；请完成业务验证，运行锁将在确认前保留。'

  const screen = await render(
    <main className='mx-auto w-full max-w-2xl p-4'>
      <CanaryRestoreCheckpointCard event={event} />
    </main>
  )
  const card = screen
    .getByRole('region', { name: '金丝雀稳定版恢复结果' })
    .element()
  await expect.element(screen.getByText('稳定版流量已恢复')).toBeInTheDocument()
  await expect
    .element(screen.getByText('等待业务验证', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText('业务健康确认前，发布仍保持运行，资源锁不会释放。')
    )
    .toBeInTheDocument()
  await page.viewport(390, 844)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const verificationBadge = screen
    .getByText('等待业务验证', { exact: true })
    .element()
  expect(
    contrastRatio(
      getComputedStyle(verificationBadge).color,
      getComputedStyle(verificationBadge).backgroundColor
    )
  ).toBeGreaterThanOrEqual(4.5)
  const lightSurface = getComputedStyle(card).backgroundColor
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-restore-390-light.png',
    element: card,
  })
  document.documentElement.classList.add('dark')
  expect(getComputedStyle(card).backgroundColor).not.toBe(lightSurface)
  expect(
    contrastRatio(
      getComputedStyle(verificationBadge).color,
      getComputedStyle(verificationBadge).backgroundColor
    )
  ).toBeGreaterThanOrEqual(4.5)
  await page.screenshot({
    path: '../../../../docs/visual-checks/canary-restore-390-dark.png',
    element: card,
  })
  document.documentElement.classList.remove('dark')

  for (const viewport of [
    { width: 1024, height: 768, name: '1024' },
    { width: 1440, height: 1000, name: '1440' },
  ]) {
    await page.viewport(viewport.width, viewport.height)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(
      viewport.width
    )
    await page.screenshot({
      path: `../../../../docs/visual-checks/canary-restore-${viewport.name}-light.png`,
      element: card,
    })
    document.documentElement.classList.add('dark')
    await page.screenshot({
      path: `../../../../docs/visual-checks/canary-restore-${viewport.name}-dark.png`,
      element: card,
    })
    document.documentElement.classList.remove('dark')
  }
})

it('shows confirmed recovery as a terminal business verification state', async () => {
  document.documentElement.classList.remove('dark')
  const event = checkpointEvent({
    schema_version: 1,
    stage_index: 0,
    stable_image_digest: `sha256:${'a'.repeat(64)}`,
    candidate_image_digest: `sha256:${'b'.repeat(64)}`,
    route: {
      connection_id: 7,
      route_id: 'checkout-public',
      stable_backend_id: 'checkout-stable',
      candidate_backend_id: 'checkout-candidate',
    },
    effective_weights: {
      stable_weight_percent: 100,
      candidate_weight_percent: 0,
    },
    route_revision: 'revision-2',
    restored_at: '2026-10-13T08:05:00.000Z',
  })
  event.event_type = 'canary_restore'
  event.stage = 'awaiting_restore_verification'
  event.detail = '稳定版流量已回读为 100%，候选版为 0%。'

  const screen = await render(
    <CanaryRestoreCheckpointCard event={event} verificationConfirmed />
  )

  await expect
    .element(screen.getByText('业务验证已确认', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/本次候选发布以失败状态结束，资源锁已释放/))
    .toBeInTheDocument()
})
