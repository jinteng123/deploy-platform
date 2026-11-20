import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import { platformApi, type DeliveryEnvironment } from '@/api/client'
import { EnvironmentEvidencePolicyDialog } from './environment-evidence-policy-dialog'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/client')>()),
  platformApi: {
    deliveryEnvironmentEvidencePolicy: vi.fn(),
    saveDeliveryEnvironmentEvidencePolicy: vi.fn(),
  },
}))

it('keeps versioned production rule controls available while editing on narrow screens', async () => {
  vi.mocked(platformApi.deliveryEnvironmentEvidencePolicy).mockResolvedValue({
    data: {
      environment_id: 8,
      current_version: 1,
      versions: [
        {
          environment_id: 8,
          version: 1,
          required_verification_kinds: ['ci', 'smoke', 'manual'],
          max_evidence_age_minutes: 1440,
          trusted_submitters: {
            ci: ['runner:1'],
            smoke: ['apikey:smoke-prod'],
            manual: ['release-manager'],
          },
          created_by: 'admin',
          created_at: '2026-10-11T00:00:00Z',
        },
      ],
    },
  } as never)
  vi.mocked(
    platformApi.saveDeliveryEnvironmentEvidencePolicy
  ).mockResolvedValue({
    data: {
      environment_id: 8,
      version: 2,
      required_verification_kinds: ['ci', 'smoke', 'manual'],
      max_evidence_age_minutes: 1440,
      trusted_submitters: {
        ci: ['runner:1'],
        smoke: ['apikey:smoke-prod'],
        manual: ['release-manager'],
      },
      created_by: 'admin',
      created_at: '2026-10-12T00:00:00Z',
    },
  } as never)

  const environment = {
    id: 8,
    code: 'production',
    name: '生产环境',
    environment_tier: 'production',
    evidence_policy_version: 0,
    requires_review: false,
    created_at: '',
  } satisfies DeliveryEnvironment
  const onClose = vi.fn()
  const onSaved = vi.fn()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })

  const screen = await render(
    <QueryClientProvider client={client}>
      <EnvironmentEvidencePolicyDialog
        environment={environment}
        onClose={onClose}
        onSaved={onSaved}
      />
    </QueryClientProvider>
  )
  const dialog = document.querySelector<HTMLElement>(
    '[data-slot="dialog-content"]'
  )
  expect(dialog).not.toBeNull()
  const lightBackground = getComputedStyle(dialog!).backgroundColor
  await page.viewport(1024, 768)
  document.documentElement.classList.add('dark')
  expect(getComputedStyle(dialog!).backgroundColor).not.toBe(lightBackground)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1024)
  const actionBar = dialog!.querySelector<HTMLElement>(
    '[data-slot="dialog-footer"]'
  )
  const dialogBounds = dialog!.getBoundingClientRect()
  const actionBarBounds = actionBar?.getBoundingClientRect()
  expect(actionBarBounds).toBeDefined()
  expect(actionBarBounds!.top).toBeGreaterThanOrEqual(dialogBounds.top)
  expect(actionBarBounds!.bottom).toBeLessThanOrEqual(dialogBounds.bottom)
  document.documentElement.classList.remove('dark')

  await page.viewport(390, 844)
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  const scrollRegion = dialog!.querySelector<HTMLElement>('.overscroll-contain')
  expect(scrollRegion).not.toBeNull()
  expect(scrollRegion!.scrollHeight).toBeGreaterThan(scrollRegion!.clientHeight)
  const actionBarTop = actionBar!.getBoundingClientRect().top
  scrollRegion!.scrollTop = scrollRegion!.scrollHeight
  expect(actionBar!.getBoundingClientRect().top).toBe(actionBarTop)
  scrollRegion!.scrollTop = 0
  await userEvent.fill(
    screen.getByRole('textbox', { name: 'CI 测试 来源' }),
    'runner:1'
  )
  await userEvent.fill(
    screen.getByRole('textbox', { name: '部署后 Smoke 来源' }),
    'apikey:smoke-prod'
  )
  await userEvent.fill(
    screen.getByRole('textbox', { name: '人工验收 来源' }),
    'release-manager'
  )
  await userEvent.click(screen.getByRole('button', { name: '保存规则' }))

  await vi.waitFor(() => {
    expect(
      platformApi.saveDeliveryEnvironmentEvidencePolicy
    ).toHaveBeenCalledWith(8, {
      expected_version: 1,
      required_verification_kinds: ['ci', 'smoke', 'manual'],
      max_evidence_age_minutes: 1440,
      trusted_submitters: {
        ci: ['runner:1'],
        smoke: ['apikey:smoke-prod'],
        manual: ['release-manager'],
      },
    })
    expect(onSaved).toHaveBeenCalledOnce()
    expect(onClose).toHaveBeenCalledOnce()
  })
  client.clear()
})
