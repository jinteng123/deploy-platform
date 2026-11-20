import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page } from 'vitest/browser'
import { platformApi } from '@/api/client'
import { PlanPreview } from './delivery-plan-dialog'

vi.mock('@/api/client', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/client')>()),
  platformApi: {
    promotionRequests: vi.fn(),
  },
}))

function colorLuminance(color: string): number {
  const canvas = document.createElement('canvas')
  canvas.width = 1
  canvas.height = 1
  const context = canvas.getContext('2d')!
  context.fillStyle = color
  context.fillRect(0, 0, 1, 1)
  const channels = Array.from(context.getImageData(0, 0, 1, 1).data.slice(0, 3))
  return channels.reduce((sum, value, index) => {
    const normalized = value / 255
    const linear =
      normalized <= 0.04045
        ? normalized / 12.92
        : ((normalized + 0.055) / 1.055) ** 2.4
    return sum + linear * [0.2126, 0.7152, 0.0722][index]
  }, 0)
}

it('shows the immutable production evidence rule in the approval snapshot without narrow-screen overflow', async () => {
  vi.mocked(platformApi.promotionRequests).mockResolvedValue({
    data: [],
  } as never)
  const screen = await render(
    <QueryClientProvider
      client={
        new QueryClient({
          defaultOptions: { queries: { retry: false } },
        })
      }
    >
      <PlanPreview
        allowApproval={false}
        plan={
          {
            id: 13,
            release_id: 7,
            target_cluster_id: 2,
            target_app_id: 5,
            strategy: 'rolling',
            status: 'preview_only',
            snapshot_hash: 'hash',
            blockers: [],
            requested_by: 'alice',
            created_at: '2026-10-12T00:00:00Z',
            snapshot: {
              schema_version: 12,
              strategy: 'rolling',
              evidence_policy: {
                environment_id: 3,
                version: 4,
                required_verification_kinds: ['ci', 'smoke'],
                max_evidence_age_minutes: 60,
                trusted_submitters: {
                  ci: ['apikey:yunxiao-prod'],
                  smoke: ['apikey:smoke-prod'],
                },
              },
              artifact: {
                release_id: 7,
                event_id: 'build-7',
                source: 'yunxiao',
                source_kind: 'repository',
                source_repository: 'https://git.example.com/shop/mono.git',
                source_path: 'services/api',
                image_repository: 'registry.example.com/shop/api',
                image: 'registry.example.com/shop/api@sha256:abc123',
                digest: 'sha256:abc123',
                digest_source: 'ci',
                version: 'v1.2.3',
                pipeline_id: 'pipeline-42',
                pipeline_name: 'shop-build',
                branch: 'main',
              },
              target: {
                cluster_id: 2,
                cluster_name: 'prod-east',
                app_id: 5,
                app_name: 'shop-api',
                test_app_id: 4,
                test_binding_id: 8,
                test_environment_id: 1,
                test_environment_name: '测试',
                namespace: 'production',
                deployment: 'shop-api',
                container: 'api',
                environment_id: 3,
                environment_name: '生产环境',
                environment_tier: 'production',
                application_id: 1,
                application_name: '商城',
                component_id: 2,
                component_name: 'API',
                binding_id: 9,
                workload_uid: 'uid-1',
                resource_version: '12',
                current_image: 'registry.example.com/shop/api:v1.2.2',
                stable_image_digest: `sha256:${'c'.repeat(64)}`,
                stable_release_id: 6,
                stable_configuration_source: {
                  repository_url: 'https://git.example.com/shop/deploy.git',
                  path: 'charts/api',
                  revision: 'b'.repeat(40),
                  recorded_by: 'runner:5',
                  recorded_at: '2026-10-14T12:00:00Z',
                },
                deployment_pipeline: {
                  connection_id: 9,
                  pipeline_id: 'deploy-pipeline-12',
                },
                deployment_pipeline_scope_review: {
                  id: 19,
                  binding_id: 9,
                  connection_id: 9,
                  pipeline_id: 'deploy-pipeline-12',
                  definition_hash: 'd'.repeat(64),
                  write_scope: 'configuration_only',
                  reviewer: 'operator',
                  reviewed_at: '2026-10-21T08:00:00Z',
                },
                workload_hash: 'template-hash',
                service_account_name: 'shop-api',
                service_account_pull_secrets_included: false,
                configuration_references: [
                  {
                    kind: 'ConfigMap',
                    name: 'shop-api-config-v7',
                    uid: 'config-uid',
                    resource_version: '17',
                  },
                  {
                    kind: 'Secret',
                    name: 'shop-api-credentials-v3',
                    uid: 'secret-uid',
                    resource_version: '21',
                  },
                ],
              },
              policy: {
                binding_id: 9,
                version: 1,
                source: 'versioned_policy',
                strategy: 'rolling',
                readiness: 'configurable',
              },
            },
          } as never
        }
      />
    </QueryClientProvider>
  )
  await page.viewport(390, 844)

  await expect
    .element(screen.getByLabelText('生产验证策略快照'))
    .toBeInTheDocument()
  await expect.element(screen.getByText('生产环境 · v4')).toBeInTheDocument()
  await expect
    .element(screen.getByText('CI 测试、部署后 Smoke'))
    .toBeInTheDocument()
  await expect.element(screen.getByText('60 分钟')).toBeInTheDocument()
  await expect
    .element(screen.getByText('https://git.example.com/shop/mono.git'))
    .toBeInTheDocument()
  await expect.element(screen.getByText('services/api')).toBeInTheDocument()
  await expect
    .element(screen.getByText('registry.example.com/shop/api', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('shop-build · pipeline-42'))
    .toBeInTheDocument()
  await expect.element(screen.getByText('main')).toBeInTheDocument()
  await expect.element(screen.getByText('CI 上报')).toBeInTheDocument()
  const stableBaseline = screen
    .getByRole('group', { name: '稳定版本配置恢复基线' })
    .element()
  await expect
    .element(screen.getByText('https://git.example.com/shop/deploy.git'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('deploy-pipeline-12'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/记录为仅配置写入.*核对 #19/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(`定义 SHA-256：${'d'.repeat(64)}`))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/历史结论不证明云效当前定义或实际执行权限/))
    .toBeInTheDocument()
  await expect.element(screen.getByText(/最近成功部署 #6/)).toBeInTheDocument()
  await screen.getByText('查看配置恢复的固定输入').click()
  await expect
    .element(screen.getByText('DEPLOY_CONFIG_REVISION · 稳定配置 commit'))
    .toBeInTheDocument()
  await expect
    .element(
      screen.getByText(`registry.example.com/shop/api@sha256:${'c'.repeat(64)}`)
    )
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('DEPLOYMENT_NAME · Deployment'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('触发时生成并持久化'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/平台当前不会触发该流水线/))
    .toBeInTheDocument()
  await page.screenshot({
    path: '../../../../docs/visual-checks/stable-config-baseline-390-light.png',
    element: stableBaseline,
  })
  document.documentElement.classList.add('dark')
  await new Promise((resolve) => setTimeout(resolve, 300))
  const sourceBadge = stableBaseline.querySelector<HTMLElement>(
    '[data-slot="badge"]'
  )!
  expect(colorLuminance(getComputedStyle(sourceBadge).color)).toBeGreaterThan(
    0.7
  )
  await page.screenshot({
    path: '../../../../docs/visual-checks/stable-config-baseline-390-dark.png',
    element: stableBaseline,
  })
  document.documentElement.classList.remove('dark')
  await expect
    .element(screen.getByText('外部配置引用', { exact: true }))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('shop-api-config-v7'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText('shop-api-credentials-v3'))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/UID\/resourceVersion.*不会保存配置值/))
    .toBeInTheDocument()
  await expect
    .element(screen.getByText(/apikey:yunxiao-prod.*apikey:smoke-prod/))
    .toBeInTheDocument()
  expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
})
