import { expect, it } from 'vitest'
import { render } from 'vitest-browser-react'
import { ReleaseStrategyJourney } from './release-strategy-journey'

it('explains blue-green traffic switching without implying the executor is live', async () => {
  const screen = await render(<ReleaseStrategyJourney strategy='blue_green' />)
  await expect
    .element(screen.getByRole('group', { name: '蓝绿发布路径示意' }))
    .toBeInTheDocument()
  await expect.element(screen.getByText('现有 Service')).toBeInTheDocument()
  await expect
    .element(screen.getByText('候选 Deployment', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/不代表本目标已完成接入或检查/))
    .toBeInTheDocument()
})

it('shows the existing gateway and independent backends for canary', async () => {
  const screen = await render(
    <ReleaseStrategyJourney strategy='gateway_canary' compact />
  )
  await expect.element(screen.getByText('既有网关路由')).toBeInTheDocument()
  await expect.element(screen.getByText('稳定版 Service')).toBeInTheDocument()
  await expect.element(screen.getByText('候选版 Service')).toBeInTheDocument()
  await expect
    .element(screen.getByText('网关按阶段调整权重'))
    .toBeInTheDocument()
})

it('keeps the rolling deployment path explicit', async () => {
  const screen = await render(<ReleaseStrategyJourney strategy='rolling' />)
  await expect
    .element(screen.getByText('更新现有 Deployment', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('等待 rollout 就绪'))
    .toBeInTheDocument()
})
