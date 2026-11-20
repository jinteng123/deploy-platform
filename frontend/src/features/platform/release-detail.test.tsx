import type { AnchorHTMLAttributes, ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { toast } from 'sonner'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import {
  platformApi,
  type Cluster,
  type ComponentBinding,
  type DeliveryPlan,
  type DeployLog,
  type ImageReleaseView,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import {
  DeliveryPlanDialog,
  DeliveryPlanSnapshotDialog,
  PlanPreview,
} from './delivery-plan-dialog'
import { ReleaseDetailDialog } from './release-detail'
import { VerificationDialog } from './release-page'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    platformApi: {
      deployLog: vi.fn(),
      deployActions: vi.fn(),
      reconcileDeployLog: vi.fn(),
      rollbackHistory: vi.fn(),
      rollback: vi.fn(),
      promotionTargets: vi.fn(),
      apps: vi.fn(),
      deliveryModel: vi.fn(),
      createDeliveryPlan: vi.fn(),
      deliveryPlan: vi.fn(),
      promotionRequests: vi.fn(),
      requestDeliveryPlanApproval: vi.fn(),
      deliveryRunByDeployLog: vi.fn(),
      deliveryRunAction: vi.fn(),
      currentImage: vi.fn(),
      promoteRelease: vi.fn(),
      recordVerification: vi.fn(),
    },
  }
})

vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn() } }))

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    Link: ({
      to,
      params,
      children,
      ...props
    }: {
      to: string
      params?: Record<string, string>
      children?: ReactNode
    } & AnchorHTMLAttributes<HTMLAnchorElement>) => (
      <a
        href={Object.entries(params ?? {}).reduce(
          (path, [key, value]) => path.replace(`$${key}`, value),
          to
        )}
        {...props}
      >
        {children}
      </a>
    ),
  }
})

const mockDeployLog = vi.mocked(platformApi.deployLog)
const mockHistory = vi.mocked(platformApi.rollbackHistory)
const mockRollback = vi.mocked(platformApi.rollback)
const mockTargets = vi.mocked(platformApi.promotionTargets)

const release: ImageReleaseView = {
  id: 42,
  event_id: 'evt-1',
  source: 'yunxiao',
  app_name: 'shop-api',
  image_full: 'registry.example.com/proj/shop-api:1.2.3',
  version: '1.2.3',
  pipeline_name: 'shop-pipeline',
  pipeline_id: 'pipeline-17',
  build_number: '88',
  branch: 'main',
  commit_id: 'abcdef1234567890deadbeef',
  test_cluster_id: 1,
  test_deploy_log_id: 7,
  prod_cluster_id: null,
  prod_deploy_log_id: null,
  created_at: '2024-05-01T10:00:00Z',
  updated_at: '2024-05-01T10:00:00Z',
  test_status: 'success',
  prod_status: null,
  can_promote: true,
}

const repositoryBinding: ComponentBinding = {
  id: 11,
  cluster_id: 1,
  app_name: 'shop-api',
  source_kind: 'repository',
  source_repo_url: 'https://codeup.aliyun.com/team/app.git',
  source_path: 'services/api',
  image_repository: 'registry.example.com/proj/shop-api',
  deployment_name: 'shop-api',
  container_name: 'api',
  namespace: 'default',
  match_keyword: 'shop-api',
  harbor_project: 'proj',
  created_at: '2024-01-01T00:00:00Z',
}

const testLog: DeployLog = {
  id: 7,
  cluster_id: 1,
  app_name: 'shop-api',
  deployment_name: 'shop-api',
  container_name: 'shop-api',
  namespace: 'default',
  version: '1.2.3',
  image_full: 'registry.example.com/proj/shop-api:1.2.3',
  deploy_type: 'deploy',
  status: 'failed',
  current_step: 'docker_push',
  step_detail: JSON.stringify([
    { step: 'check_file', success: true, stdout: 'file ok', stderr: '' },
    { step: 'docker_load', success: true, stdout: '', stderr: '' },
    {
      step: 'docker_push',
      success: false,
      stdout: '',
      stderr: 'denied: requested access',
    },
  ]),
  error_message: '推送镜像失败',
  started_at: '2024-05-01T10:01:00Z',
  finished_at: '2024-05-01T10:03:00Z',
  created_at: '2024-05-01T10:00:00Z',
}

const prodCluster: Cluster = {
  id: 3,
  name: 'prod-east',
  env_code: 'prod-east',
  environment: 'prod-east',
  environment_tier: 'production',
  deploy_policy: 'manual',
  connection_type: 'kubeconfig',
  credential_configured: true,
  insecure_skip_tls_verify: false,
  enabled: true,
  default_namespace: 'default',
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
}

function renderWithClient(node: React.ReactElement) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>{node}</QueryClientProvider>
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  mockDeployLog.mockResolvedValue({ data: testLog } as never)
  vi.mocked(platformApi.deployActions).mockResolvedValue({ data: [] } as never)
  vi.mocked(platformApi.deliveryRunByDeployLog).mockResolvedValue({
    data: null,
  } as never)
  vi.mocked(platformApi.reconcileDeployLog).mockResolvedValue({
    data: { log_id: 701, status: 'pending', message: '已核对并继续' },
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
})

function blueGreenPlan(id: number): DeliveryPlan {
  return {
    id,
    release_id: 42,
    target_cluster_id: 3,
    target_app_id: 9,
    strategy: 'blue_green',
    status: 'preview_only',
    snapshot_hash: 'b'.repeat(64),
    requested_by: 'alice',
    created_at: '2026-10-09T00:00:00Z',
    blockers: [],
    snapshot: {
      schema_version: 1,
      strategy: 'blue_green',
      checks: [],
      artifact: {
        release_id: 42,
        event_id: `evt-${id}`,
        image: 'registry.example.com/proj/shop-api@sha256:abc',
        version: '1.2.3',
      },
      target: {
        cluster_id: 3,
        cluster_name: 'prod-east',
        app_id: 9,
        app_name: 'shop-api',
        test_app_id: 1,
        test_binding_id: 11,
        test_environment_id: 1,
        test_environment_name: '测试',
        namespace: 'production',
        deployment: 'shop-api',
        container: 'api',
        service_name: 'shop-api',
        service_uid: 'service-uid',
        service_selector: { app: 'shop-api' },
        candidate_label_key: 'delivery.platform.io/run-id',
        candidate_label_value: `r-${id}`,
        stable_label_key: 'delivery.platform.io/app-9',
        environment_id: 2,
        environment_name: '生产',
        environment_tier: 'production',
        application_id: 1,
        application_name: '商城',
        component_id: 1,
        component_name: 'shop-api',
        binding_id: 22,
        workload_uid: 'workload-uid',
        resource_version: 'rv-123',
        current_image: 'registry.example.com/proj/shop-api:1.1.0',
        workload_hash: 'c'.repeat(64),
      },
    },
  }
}

describe('ReleaseDetailDialog', () => {
  it('keeps release details readable in a narrow dark theme', async () => {
    await page.viewport(390, 844)
    document.documentElement.classList.add('dark')
    try {
      const screen = await renderWithClient(
        <ReleaseDetailDialog
          release={{
            ...release,
            image_full: `registry.example.com/orders/api@sha256:${'ab'.repeat(32)}`,
            metadata: {
              digest_source: 'ci',
              source_repositories: ['codeup.aliyun.com/team/app'],
              source_path: 'services/api',
              binding: {
                ...repositoryBinding,
                deployment_config_repo_url:
                  'https://git.example.com/team/deploy-config',
                deployment_config_path: 'charts/orders',
              },
              configuration_source: {
                repository_url:
                  'https://git.example.com/team/deploy-config.git',
                path: 'charts/orders',
                revision: 'a'.repeat(40),
                recorded_by: 'runner:22',
                recorded_at: '2026-10-15T10:00:00Z',
              },
            },
          }}
          open={true}
          onClose={vi.fn()}
        />
      )
      await expect
        .element(screen.getByRole('heading', { name: '测试环境' }))
        .toBeVisible()
      const titleElement = document.querySelector('section h3')
      const environmentSection = titleElement?.closest('section')
      expect(titleElement).not.toBeNull()
      expect(environmentSection).not.toBeNull()
      expect(titleElement?.className).toContain('text-foreground')
      expect(environmentSection?.className).toContain('border-border')
      expect(getComputedStyle(titleElement!).color).not.toBe('rgb(28, 53, 94)')
      const metadataLabel = [...document.querySelectorAll('div')].find(
        (element) => element.textContent === '流水线 / 构建'
      )
      expect(metadataLabel?.className).toContain('text-muted-foreground')
      await expect
        .element(screen.getByText('仓库与目录已匹配', { exact: true }))
        .toBeInTheDocument()
      await expect
        .element(screen.getByText('配置仓库与目录已匹配'))
        .toBeInTheDocument()
      await expect
        .element(screen.getByTestId('release-config-revision'))
        .toHaveTextContent('a'.repeat(40))
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
      await page.screenshot({
        path: '../../../../docs/visual-checks/release-detail-390-dark-build-identity.png',
      })
    } finally {
      document.documentElement.classList.remove('dark')
      await page.viewport(1280, 800)
    }
  })

  it('renders metadata and the step timeline parsed from step_detail', async () => {
    await page.viewport(390, 844)
    const screen = await renderWithClient(
      <ReleaseDetailDialog
        release={{
          ...release,
          metadata: {
            digest_source: 'ci',
            commit_timestamp: '2026-10-15T01:30:00Z',
          },
        }}
        open={true}
        onClose={vi.fn()}
      />
    )
    await expect
      .element(screen.getByText(/发布单 #42 · shop-api/))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('shop-pipeline (#pipeline-17) · 构建 #88'))
      .toBeInTheDocument()
    // commit is truncated to 12 chars
    await expect.element(screen.getByText('abcdef123456')).toBeInTheDocument()
    const commitTimestampLabel = screen.getByText('Git 提交时间').element()
    expect(commitTimestampLabel.parentElement?.textContent).toContain('2026')
    // humanized step names from parsed step_detail
    await expect.element(screen.getByText('检查部署包')).toBeInTheDocument()
    await expect.element(screen.getByText('加载镜像')).toBeInTheDocument()
    await expect
      .element(screen.getByText('推送镜像', { exact: true }))
      .toBeInTheDocument()
    // deploy target + error from the fetched log
    await expect
      .element(screen.getByText('shop-api/shop-api@default'))
      .toBeInTheDocument()
    await expect.element(screen.getByText('推送镜像失败')).toBeInTheDocument()
    await expect
      .element(screen.getByRole('link', { name: '查看完整任务 #7' }))
      .toHaveAttribute('href', '/tasks/logs/7')
    await expect
      .element(screen.getByRole('link', { name: '查看完整任务 #7' }))
      .toBeVisible()
    // prod section has no log id
    await expect.element(screen.getByText('暂无部署记录')).toBeInTheDocument()
    // steps with output get an expandable details block
    expect(document.querySelectorAll('details')).toHaveLength(2)
    await expect.element(screen.getByText('file ok')).toBeInTheDocument()
    await userEvent.click(screen.getByText('查看输出').first())
    expect(document.querySelector('details')?.hasAttribute('open')).toBe(true)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
    await page.screenshot({
      path: '../../../../docs/visual-checks/release-task-link-390-light.png',
    })
    await page.viewport(1280, 800)
  })

  it('shows the build repository beside the registered component repository', async () => {
    const screen = await renderWithClient(
      <ReleaseDetailDialog
        release={{
          ...release,
          metadata: {
            digest_source: 'ci',
            source_repositories: ['codeup.aliyun.com/team/app'],
            source_path: 'services/api',
            binding: repositoryBinding,
          },
        }}
        open={true}
        onClose={vi.fn()}
      />
    )

    await expect.element(screen.getByText('本次构建代码源')).toBeInTheDocument()
    await expect
      .element(screen.getByText('codeup.aliyun.com/team/app', { exact: true }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('仓库与目录已匹配'))
      .toBeInTheDocument()
    await expect
      .element(screen.getByTestId('release-source-path'))
      .toHaveTextContent('services/api')
  })

  it('marks legacy builds without repository evidence as unverified', async () => {
    await page.viewport(390, 844)
    try {
      const screen = await renderWithClient(
        <ReleaseDetailDialog
          release={{
            ...release,
            metadata: {
              digest_source: 'ci',
              source_repositories: [],
              binding: repositoryBinding,
            },
          }}
          open={true}
          onClose={vi.fn()}
        />
      )

      await expect
        .element(screen.getByText('本次构建未报告代码仓库'))
        .toBeInTheDocument()
      await expect
        .element(screen.getByText('仓库与目录已匹配'))
        .not.toBeInTheDocument()
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
      await page.screenshot({
        path: '../../../../docs/visual-checks/release-detail-390-light-source-unreported.png',
      })
    } finally {
      await page.viewport(1280, 800)
    }
  })

  it('does not claim a repository match when the build reports another repository', async () => {
    await page.viewport(390, 844)
    document.documentElement.classList.add('dark')
    try {
      const screen = await renderWithClient(
        <ReleaseDetailDialog
          release={{
            ...release,
            metadata: {
              digest_source: 'ci',
              source_repositories: ['git@git.example.com:team/another-app.git'],
              source_path: 'services/api',
              binding: repositoryBinding,
            },
          }}
          open={true}
          onClose={vi.fn()}
        />
      )

      await expect
        .element(screen.getByText('代码仓库与组件绑定不一致'))
        .toBeInTheDocument()
      await expect
        .element(screen.getByText('仓库与目录已匹配'))
        .not.toBeInTheDocument()
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
      await page.screenshot({
        path: '../../../../docs/visual-checks/release-detail-390-dark-source-mismatch.png',
      })
    } finally {
      document.documentElement.classList.remove('dark')
      await page.viewport(1280, 800)
    }
  })

  it('reports a directory mismatch after confirming the repository identity', async () => {
    const screen = await renderWithClient(
      <ReleaseDetailDialog
        release={{
          ...release,
          metadata: {
            digest_source: 'ci',
            source_repositories: [
              'git@codeup.aliyun.com:team/app.git',
              'https://codeup.aliyun.com/team/another-app.git',
            ],
            source_path: 'services/worker',
            binding: repositoryBinding,
          },
        }}
        open={true}
        onClose={vi.fn()}
      />
    )

    await expect
      .element(screen.getByText('仓库已匹配，构建目录不一致'))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('仓库与目录已匹配'))
      .not.toBeInTheDocument()
  })

  it('treats an omitted SOURCE_PATH as the matched repository root', async () => {
    const screen = await renderWithClient(
      <ReleaseDetailDialog
        release={{
          ...release,
          metadata: {
            digest_source: 'ci',
            source_repositories: ['codeup.aliyun.com/team/app'],
            binding: { ...repositoryBinding, source_path: '.' },
          },
        }}
        open={true}
        onClose={vi.fn()}
      />
    )

    await expect
      .element(screen.getByText('仓库与目录已匹配'))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('本次构建目录'))
      .not.toBeInTheDocument()
  })

  it('shows an explicit unknown configuration revision when a component source is registered', async () => {
    await page.viewport(390, 844)
    document.documentElement.classList.add('dark')
    try {
      const screen = await renderWithClient(
        <ReleaseDetailDialog
          release={{
            ...release,
            metadata: {
              digest_source: 'ci',
              binding: {
                ...repositoryBinding,
                deployment_config_repo_url:
                  'https://git.example.com/team/deploy-config.git',
                deployment_config_path: 'charts/orders',
              },
            },
          }}
          open={true}
          onClose={vi.fn()}
        />
      )

      await expect
        .element(screen.getByText('接入时的部署配置仓库'))
        .toBeInTheDocument()
      await expect
        .element(screen.getByText('本次部署配置来源'))
        .toBeInTheDocument()
      await expect
        .element(screen.getByText('尚未固定 Git commit'))
        .toBeInTheDocument()
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
      await page.screenshot({
        path: '../../../../docs/visual-checks/release-detail-390-dark-config-source-unknown.png',
      })
    } finally {
      document.documentElement.classList.remove('dark')
      await page.viewport(1280, 800)
    }
  })

  it('does not mark a configuration source that disagrees with its component binding as matched', async () => {
    const screen = await renderWithClient(
      <ReleaseDetailDialog
        release={{
          ...release,
          metadata: {
            digest_source: 'ci',
            binding: {
              ...repositoryBinding,
              deployment_config_repo_url:
                'https://git.example.com/team/deploy-config.git',
              deployment_config_path: 'charts/orders',
            },
            configuration_source: {
              repository_url: 'https://git.example.com/team/deploy-config.git',
              path: 'charts/catalog',
              revision: 'b'.repeat(40),
              recorded_by: 'runner:22',
              recorded_at: '2026-10-15T10:00:00Z',
            },
          },
        }}
        open={true}
        onClose={vi.fn()}
      />
    )

    await expect
      .element(screen.getByText('配置来源与组件绑定不一致'))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('配置仓库与目录已匹配'))
      .not.toBeInTheDocument()
  })

  it('shows a legacy reported configuration source without claiming it matches a fixed binding', async () => {
    const screen = await renderWithClient(
      <ReleaseDetailDialog
        release={{
          ...release,
          metadata: {
            digest_source: 'ci',
            binding: repositoryBinding,
            configuration_source: {
              repository_url: 'https://git.example.com/team/deploy-config.git',
              path: 'charts/orders',
              revision: 'c'.repeat(40),
              recorded_by: 'runner:22',
              recorded_at: '2026-10-15T10:00:00Z',
            },
          },
        }}
        open={true}
        onClose={vi.fn()}
      />
    )

    await expect
      .element(screen.getByText('本次部署配置来源'))
      .toBeInTheDocument()
    await expect
      .element(screen.getByTestId('release-config-revision'))
      .toHaveTextContent('c'.repeat(40))
    await expect
      .element(screen.getByText('配置仓库与目录已匹配'))
      .not.toBeInTheDocument()
  })

  it('shows an unverifiable runtime image digest as an explicit observation', async () => {
    mockDeployLog.mockResolvedValue({
      data: {
        ...testLog,
        status: 'success',
        error_message: null,
        step_detail: JSON.stringify([
          {
            step: 'verify_image',
            success: true,
            stdout: '模板镜像已回读一致',
            stderr: '',
          },
          {
            step: 'verify_pod_image',
            success: true,
            observation_status: 'unknown',
            stdout:
              'Pod api-0 的容器状态未提供可识别的 registry sha256 digest。',
            stderr: '',
          },
        ]),
      },
    } as never)
    const screen = await renderWithClient(
      <ReleaseDetailDialog release={release} open={true} onClose={vi.fn()} />
    )
    await expect
      .element(screen.getByText('核对 Pod 运行镜像摘要'))
      .toBeInTheDocument()
    await expect.element(screen.getByText('无法核实')).toBeInTheDocument()
    await expect
      .element(screen.getByText(/未提供可识别的 registry sha256 digest/))
      .toBeInTheDocument()
    expect(document.querySelectorAll('svg.text-destructive')).toHaveLength(0)
  })

  it('shows candidate Pod digest evidence as a verified release step', async () => {
    await page.viewport(390, 844)
    mockDeployLog.mockResolvedValue({
      data: {
        ...testLog,
        status: 'pending',
        error_message: null,
        step_detail: JSON.stringify([
          {
            step: 'verify_candidate_pod_image',
            success: true,
            observation_status: 'verified',
            stdout:
              '已核对 2 个候选 Pod；运行时 sha256 digest 与发布目标一致。',
            stderr: '',
          },
        ]),
      },
    } as never)
    const screen = await renderWithClient(
      <ReleaseDetailDialog release={release} open={true} onClose={vi.fn()} />
    )
    await expect
      .element(screen.getByText('核对候选 Pod 镜像摘要'))
      .toBeInTheDocument()
    await expect.element(screen.getByText('digest 一致')).toBeInTheDocument()
    expect(document.querySelectorAll('svg.text-destructive')).toHaveLength(0)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  })

  it('shows immutable verification and scan report history with safe links', async () => {
    await page.viewport(390, 844)
    const digest = `sha256:${'ab'.repeat(32)}`
    const screen = await renderWithClient(
      <ReleaseDetailDialog
        release={{
          ...release,
          metadata: {
            digest_source: 'ci',
            verification: {
              digest,
              status: 'failed',
              kind: 'smoke',
              report_url: '',
              summary: '探活失败',
              valid_until: '2099-01-02T00:00:00Z',
              recorded_at: '2099-01-01T00:00:00Z',
              recorded_by: 'runner:4',
            },
            verification_history: [
              {
                digest,
                status: 'passed',
                kind: 'ci',
                report_url: 'https://ci.example/report/1',
                summary: '单元测试通过',
                valid_until: '2099-01-02T00:00:00Z',
                recorded_at: '2099-01-01T00:00:00Z',
                recorded_by: 'runner:4',
              },
              {
                digest,
                status: 'failed',
                kind: 'smoke',
                report_url: 'javascript:alert(1)',
                summary: '探活失败',
                valid_until: '2099-01-02T00:00:00Z',
                recorded_at: '2099-01-01T00:00:01Z',
                recorded_by: 'runner:4',
              },
            ],
            scan_history: [
              {
                digest,
                status: 'passed',
                kind: 'scan',
                tool: 'Trivy',
                tool_version: '0.57.1',
                rules_version: 'db-2026-10-12',
                report_url: '',
                summary: '没有高危漏洞',
                valid_until: '2099-01-02T00:00:00Z',
                recorded_at: '2099-01-01T00:00:02Z',
                recorded_by: 'scanner:1',
              },
            ],
          },
        }}
        open={true}
        onClose={vi.fn()}
      />
    )
    await userEvent.click(screen.getByText('验证与扫描记录（3）'))
    await expect.element(screen.getByText('CI 验证')).toBeInTheDocument()
    await expect.element(screen.getByText('Smoke 验证')).toBeInTheDocument()
    await expect.element(screen.getByText('安全扫描')).toBeInTheDocument()
    await expect
      .element(screen.getByText(/Trivy 0\.57\.1.*db-2026-10-12/))
      .toBeInTheDocument()
    await expect.element(screen.getByText('单元测试通过')).toBeInTheDocument()
    await expect.element(screen.getByText('探活失败')).toBeInTheDocument()
    await expect
      .element(screen.getByRole('link', { name: '查看报告' }))
      .toHaveAttribute('href', 'https://ci.example/report/1')
    expect(document.querySelector('a[href^="javascript:"]')).toBeNull()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
    await page.viewport(1280, 800)
  })

  it('shows the running step instead of a finished timeline', async () => {
    mockDeployLog.mockResolvedValue({
      data: {
        ...testLog,
        status: 'running',
        current_step: 'rollout_wait',
        step_detail: JSON.stringify([
          { step: 'check_file', success: true, stdout: '', stderr: '' },
        ]),
        error_message: null,
      },
    } as never)
    const screen = await renderWithClient(
      <ReleaseDetailDialog release={release} open={true} onClose={vi.fn()} />
    )
    await expect.element(screen.getByText('等待滚动更新')).toBeInTheDocument()
    await expect.element(screen.getByText('检查部署包')).toBeInTheDocument()
  })

  it('submits a rollback for the selected history record', async () => {
    mockDeployLog.mockResolvedValue({
      data: { ...testLog, status: 'success', error_message: null },
    } as never)
    mockHistory.mockResolvedValue({
      data: [
        {
          id: 5,
          version: '1.1.0',
          image_full: 'registry.example.com/proj/shop-api:1.1.0',
          deploy_type: 'deploy',
          finished_at: '2024-04-20T08:00:00Z',
        },
      ],
    } as never)
    mockRollback.mockResolvedValue({
      data: { log_id: 9, status: 'pending', message: '回滚任务已提交' },
    } as never)
    const screen = await renderWithClient(
      <ReleaseDetailDialog release={release} open={true} onClose={vi.fn()} />
    )
    const rollbackBtn = screen.getByRole('button', { name: '回滚到历史版本' })
    await expect.element(rollbackBtn).toBeInTheDocument()
    await userEvent.click(rollbackBtn)
    await vi.waitFor(() => {
      expect(mockHistory).toHaveBeenCalledWith('shop-api', 1)
    })
    // first record selected by default
    await expect
      .element(screen.getByText(/1\.1\.0 · deploy/))
      .toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '确认回滚' }))
    await vi.waitFor(() => {
      expect(mockRollback).toHaveBeenCalledWith({
        cluster_id: 1,
        target_log_id: 5,
      })
    })
    expect(toast.success).toHaveBeenCalledWith('回滚任务已提交')
  })
})

describe('DeliveryPlanDialog', () => {
  it('requires explicit acknowledgement of blue-green manual checks before requesting approval', async () => {
    const plan: DeliveryPlan = {
      id: 54,
      release_id: 42,
      target_cluster_id: 3,
      target_app_id: 9,
      strategy: 'blue_green',
      status: 'preview_only',
      snapshot_hash: 'e'.repeat(64),
      requested_by: 'alice',
      created_at: '2026-10-09T00:00:00Z',
      blockers: [],
      snapshot: {
        schema_version: 1,
        strategy: 'blue_green',
        checks: [
          {
            key: 'capacity',
            status: 'review',
            title: '双版本容量',
            detail: '需要额外预留候选副本容量。',
          },
        ],
        artifact: {
          release_id: 42,
          event_id: 'evt-54',
          image: 'registry.example.com/proj/shop-api@sha256:abc',
          version: '1.2.3',
        },
        target: {
          cluster_id: 3,
          cluster_name: 'prod-east',
          app_id: 9,
          app_name: 'shop-api',
          test_app_id: 1,
          test_binding_id: 11,
          test_environment_id: 1,
          test_environment_name: '测试',
          namespace: 'production',
          deployment: 'shop-api',
          container: 'api',
          service_name: 'shop-api',
          service_uid: 'service-uid',
          service_selector: { app: 'shop-api' },
          candidate_label_key: 'delivery.platform.io/run-id',
          candidate_label_value: 'r-54',
          stable_label_key: 'delivery.platform.io/app-9',
          environment_id: 2,
          environment_name: '生产',
          environment_tier: 'production',
          application_id: 1,
          application_name: '商城',
          component_id: 1,
          component_name: 'shop-api',
          binding_id: 22,
          workload_uid: 'workload-uid',
          resource_version: 'rv-123',
          current_image: 'registry.example.com/proj/shop-api:1.1.0',
          workload_hash: 'f'.repeat(64),
        },
      },
    }
    vi.mocked(platformApi.promotionRequests).mockResolvedValue({
      data: [],
    } as never)
    vi.mocked(platformApi.requestDeliveryPlanApproval).mockResolvedValue({
      data: { request_id: 81 },
    } as never)

    const screen = await renderWithClient(<PlanPreview plan={plan} />)
    await expect
      .element(screen.getByLabelText('已核对发布计划的人工预检项'))
      .not.toBeChecked()
    await expect
      .element(screen.getByRole('button', { name: '提交蓝绿发布审批' }))
      .not.toBeInTheDocument()

    await userEvent.click(screen.getByLabelText('已核对发布计划的人工预检项'))
    await userEvent.click(
      screen.getByRole('button', { name: '提交蓝绿发布审批' })
    )
    await vi.waitFor(() => {
      expect(platformApi.requestDeliveryPlanApproval).toHaveBeenCalledWith(
        54,
        true
      )
    })
  })

  it('requires acknowledgement of unversioned configuration references for rolling approval', async () => {
    const base = blueGreenPlan(56)
    const plan: DeliveryPlan = {
      ...base,
      strategy: 'rolling',
      snapshot: {
        ...base.snapshot,
        schema_version: 7,
        strategy: 'rolling',
        checks: [
          {
            key: 'configuration-snapshot',
            status: 'review',
            title: '配置版本与恢复',
            detail:
              '发现 1 个 ConfigMap/Secret 引用；当前接入方式无法完整读取对象元数据版本，计划仅固定引用名称。',
          },
        ],
        target: {
          ...base.snapshot.target,
          service_account_name: 'shop-api',
          configuration_references: [
            { kind: 'ConfigMap', name: 'shop-api-config-v7' },
          ],
        },
      },
    }
    vi.mocked(platformApi.promotionRequests).mockResolvedValue({
      data: [],
    } as never)
    vi.mocked(platformApi.requestDeliveryPlanApproval).mockResolvedValue({
      data: { request_id: 82 },
    } as never)
    await page.viewport(390, 844)
    const screen = await renderWithClient(<PlanPreview plan={plan} />)
    const acknowledgement = screen.getByLabelText('已核对发布计划的人工预检项')
    await expect.element(acknowledgement).not.toBeChecked()
    await expect
      .element(screen.getByRole('button', { name: '提交滚动发布审批' }))
      .not.toBeInTheDocument()
    await expect
      .element(screen.getByText('shop-api-config-v7'))
      .toBeInTheDocument()

    await userEvent.click(acknowledgement)
    await userEvent.click(
      screen.getByRole('button', { name: '提交滚动发布审批' })
    )
    await vi.waitFor(() => {
      expect(platformApi.requestDeliveryPlanApproval).toHaveBeenCalledWith(
        56,
        true
      )
    })
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
  })

  it('requires acknowledgement when the configuration Git revision is unknown', async () => {
    const base = blueGreenPlan(58)
    const plan: DeliveryPlan = {
      ...base,
      strategy: 'rolling',
      snapshot: {
        ...base.snapshot,
        schema_version: 9,
        strategy: 'rolling',
        checks: [
          {
            key: 'configuration-source',
            status: 'unknown',
            title: '配置仓库 revision',
            detail:
              'Runner 未上报本次发布使用的配置仓库 revision；请人工确认实际配置来源和恢复方式。',
          },
        ],
        target: {
          ...base.snapshot.target,
          service_account_name: 'default',
          configuration_references: [],
        },
      },
    }
    vi.mocked(platformApi.promotionRequests).mockResolvedValue({
      data: [],
    } as never)
    vi.mocked(platformApi.requestDeliveryPlanApproval).mockResolvedValue({
      data: { request_id: 83 },
    } as never)
    await page.viewport(390, 844)
    const screen = await renderWithClient(<PlanPreview plan={plan} />)
    const acknowledgement = screen.getByLabelText('已核对发布计划的人工预检项')
    await expect.element(acknowledgement).not.toBeChecked()
    expect(acknowledgement.element().closest('label')?.textContent).toContain(
      '配置仓库 revision'
    )

    await userEvent.click(acknowledgement)
    await userEvent.click(
      screen.getByRole('button', { name: '提交滚动发布审批' })
    )
    await vi.waitFor(() => {
      expect(platformApi.requestDeliveryPlanApproval).toHaveBeenCalledWith(
        58,
        true
      )
    })
  })

  it('blocks a plan when the component configuration repository has no reported commit', async () => {
    const base = blueGreenPlan(59)
    const plan: DeliveryPlan = {
      ...base,
      strategy: 'rolling',
      blockers: [
        '配置仓库 revision：组件已登记配置仓库，但本次制品没有固定 commit。',
      ],
      snapshot: {
        ...base.snapshot,
        schema_version: 9,
        strategy: 'rolling',
        checks: [
          {
            key: 'configuration-source',
            status: 'blocked',
            title: '配置仓库 revision',
            detail:
              '组件已登记部署配置仓库，但本次制品没有固定对应的 Git commit；请让 Runner 上报 DEPLOY_CONFIG_REVISION 后重新生成发布计划。',
          },
        ],
        artifact: {
          ...base.snapshot.artifact,
          deployment_config_repository:
            'https://git.example.com/orders/deploy-config.git',
          deployment_config_path: 'charts/orders',
        },
      },
    }
    await page.viewport(390, 844)
    const screen = await renderWithClient(<PlanPreview plan={plan} />)
    await expect
      .element(
        screen.getByText(
          'https://git.example.com/orders/deploy-config.git / charts/orders'
        )
      )
      .toBeInTheDocument()
    await expect
      .element(
        screen.getByText(/组件已登记配置仓库，但本次 Runner 未上报 commit/)
      )
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '提交滚动发布审批' }))
      .not.toBeInTheDocument()
  })

  it('guides the operator to configure a component binding before planning', async () => {
    mockTargets.mockResolvedValue({ data: [prodCluster] } as never)
    vi.mocked(platformApi.apps).mockResolvedValue({ data: [] } as never)
    vi.mocked(platformApi.deliveryModel).mockResolvedValue({
      data: {
        environments: [],
        applications: [],
        components: [],
        bindings: [],
      },
    } as never)
    const screen = await renderWithClient(
      <DeliveryPlanDialog release={release} onClose={vi.fn()} />
    )
    await expect
      .element(screen.getByText(/生产应用尚未完成组件与环境绑定/))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('link', { name: '配置环境绑定' }))
      .toHaveAttribute('href', '/environments')
    await expect
      .element(screen.getByRole('button', { name: '核对目标并生成计划' }))
      .toBeDisabled()
  })

  it('requires a second confirmation before switching blue-green production traffic', async () => {
    const plan = blueGreenPlan(55)
    vi.mocked(platformApi.deliveryPlan).mockResolvedValue({
      data: plan,
    } as never)
    vi.mocked(platformApi.promotionRequests).mockResolvedValue({
      data: [
        {
          id: 32,
          release_id: 42,
          prod_cluster_id: 3,
          requester: 'alice',
          status: 'approved',
          approver: 'bob',
          deploy_log_id: 711,
          delivery_plan_id: 55,
          created_at: '2026-10-09T00:00:00Z',
        },
      ],
    } as never)
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: {
        ...testLog,
        id: 711,
        status: 'running',
        current_step: 'awaiting_candidate_review',
        error_message: null,
      },
    } as never)
    vi.mocked(platformApi.deployActions).mockResolvedValue({
      data: [],
    } as never)
    vi.mocked(platformApi.deliveryRunByDeployLog).mockResolvedValue({
      data: {
        run: {
          id: 91,
          delivery_plan_id: 55,
          promotion_request_id: 32,
          deploy_log_id: 711,
          strategy: 'blue_green',
          status: 'running',
          current_stage: 'awaiting_candidate_review',
          version: 3,
          created_at: '2026-10-09T00:00:00Z',
          updated_at: '2026-10-09T00:00:00Z',
        },
        plan,
        deploy_log: { ...testLog, id: 711, status: 'running' },
        events: [],
        actions: [],
      },
    } as never)
    vi.mocked(platformApi.deliveryRunAction).mockResolvedValue({
      data: {
        run: {
          id: 91,
          delivery_plan_id: 55,
          promotion_request_id: 32,
          deploy_log_id: 711,
          strategy: 'blue_green',
          status: 'running',
          current_stage: 'awaiting_live_verification',
          version: 4,
          created_at: '2026-10-09T00:00:00Z',
          updated_at: '2026-10-09T00:01:00Z',
        },
        plan,
        deploy_log: { ...testLog, id: 711, status: 'running' },
        events: [],
        actions: [],
      },
    } as never)

    const screen = await renderWithClient(
      <DeliveryPlanSnapshotDialog planID={55} onClose={vi.fn()} />
    )
    await userEvent.click(screen.getByRole('button', { name: '切换生产流量' }))
    await expect
      .element(screen.getByText(/旧 Deployment 会保留/))
      .toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: '切换生产 Service' })
    )
    await vi.waitFor(() => {
      expect(platformApi.deliveryRunAction).toHaveBeenCalledWith(
        91,
        expect.objectContaining({
          action: 'confirm_cutover',
          expected_version: 3,
        })
      )
    })
  })

  it('pauses progression at an operator gate and restores stable traffic on cancellation', async () => {
    const plan = blueGreenPlan(57)
    vi.mocked(platformApi.deliveryPlan).mockResolvedValue({
      data: plan,
    } as never)
    vi.mocked(platformApi.promotionRequests).mockResolvedValue({
      data: [
        {
          id: 34,
          release_id: 42,
          prod_cluster_id: 3,
          requester: 'alice',
          status: 'approved',
          approver: 'bob',
          deploy_log_id: 713,
          delivery_plan_id: 57,
          created_at: '2026-10-09T00:00:00Z',
        },
      ],
    } as never)
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: {
        ...testLog,
        id: 713,
        status: 'running',
        current_step: 'awaiting_live_verification',
        error_message: null,
      },
    } as never)
    vi.mocked(platformApi.deployActions).mockResolvedValue({
      data: [],
    } as never)

    let currentRun = {
      run: {
        id: 93,
        delivery_plan_id: 57,
        promotion_request_id: 34,
        deploy_log_id: 713,
        strategy: 'blue_green' as const,
        status: 'running',
        current_stage: 'awaiting_live_verification',
        version: 4,
        created_at: '2026-10-09T00:00:00Z',
        updated_at: '2026-10-09T00:00:00Z',
      },
      plan,
      deploy_log: {
        ...testLog,
        id: 713,
        status: 'running',
        current_step: 'awaiting_live_verification',
      },
      events: [],
      actions: [],
    }
    vi.mocked(platformApi.deliveryRunByDeployLog).mockImplementation(
      async () => ({ data: currentRun }) as never
    )
    vi.mocked(platformApi.deliveryRunAction).mockImplementation(
      async (_runID, payload) => {
        const status =
          payload.action === 'pause'
            ? 'paused'
            : payload.action === 'cancel'
              ? 'cancelled'
              : 'running'
        currentRun = {
          ...currentRun,
          run: {
            ...currentRun.run,
            status,
            current_stage:
              payload.action === 'cancel'
                ? 'restored'
                : currentRun.run.current_stage,
            version: currentRun.run.version + 1,
          },
          deploy_log:
            payload.action === 'cancel'
              ? {
                  ...currentRun.deploy_log,
                  status: 'failed',
                  current_step: 'restored',
                  finished_at: '2026-10-09T00:10:00Z',
                }
              : currentRun.deploy_log,
        }
        return { data: currentRun } as never
      }
    )

    const screen = await renderWithClient(
      <DeliveryPlanSnapshotDialog planID={57} onClose={vi.fn()} />
    )
    await userEvent.click(screen.getByRole('button', { name: '暂停推进' }))
    await vi.waitFor(() => {
      expect(platformApi.deliveryRunAction).toHaveBeenCalledWith(
        93,
        expect.objectContaining({ action: 'pause', expected_version: 4 })
      )
    })
    await expect.element(screen.getByText('发布推进已暂停')).toBeInTheDocument()
    await expect
      .element(screen.getByText(/当前生产 Service 仍由候选版本承接流量/))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '继续推进' }))
      .toBeEnabled()
    await page.viewport(390, 844)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
    expect(document.body.textContent).not.toContain('业务验证通过，完成发布')

    await userEvent.click(screen.getByRole('button', { name: '继续推进' }))
    await vi.waitFor(() => {
      expect(platformApi.deliveryRunAction).toHaveBeenLastCalledWith(
        93,
        expect.objectContaining({ action: 'continue', expected_version: 5 })
      )
    })
    await expect
      .element(screen.getByRole('button', { name: '业务验证通过，完成发布' }))
      .toBeInTheDocument()

    await page.viewport(512, 384)
    await userEvent.click(screen.getByRole('button', { name: '取消发布' }))
    await expect
      .element(screen.getByText(/核对生产 Service，并恢复已记录的稳定入口/))
      .toBeInTheDocument()
    const dialogs = document.querySelectorAll<HTMLElement>('[role="dialog"]')
    const dialog = dialogs.item(dialogs.length - 1)
    expect(dialog).not.toBeNull()
    const bounds = dialog!.getBoundingClientRect()
    expect(bounds.top).toBeGreaterThanOrEqual(0)
    expect(bounds.bottom).toBeLessThanOrEqual(window.innerHeight)
    const contentScroller =
      dialog!.querySelector<HTMLElement>('.overflow-y-auto')
    expect(contentScroller).not.toBeNull()
    const productionEntry = Array.from(
      dialog!.querySelectorAll<HTMLElement>('div')
    ).find((element) => element.textContent?.trim() === '生产 · shop-api')
    expect(productionEntry).toBeDefined()
    const entryBounds = productionEntry!.getBoundingClientRect()
    expect(entryBounds.top).toBeGreaterThanOrEqual(bounds.top)
    expect(entryBounds.bottom).toBeLessThanOrEqual(bounds.bottom)
    const decisionButtons = Array.from(
      dialog!.querySelectorAll<HTMLButtonElement>('button')
    ).filter((button) =>
      ['暂不取消', '确认取消并恢复'].includes(button.textContent?.trim() ?? '')
    )
    expect(decisionButtons).toHaveLength(2)
    for (const button of decisionButtons) {
      const buttonBounds = button.getBoundingClientRect()
      expect(buttonBounds.top).toBeGreaterThanOrEqual(0)
      expect(buttonBounds.bottom).toBeLessThanOrEqual(window.innerHeight)
    }
    const cancelTrigger = Array.from(
      document.querySelectorAll<HTMLButtonElement>('button')
    ).find((button) => button.textContent?.trim() === '取消发布')
    expect(cancelTrigger).toBeDefined()

    await userEvent.keyboard('{Escape}')
    await expect
      .element(screen.getByRole('dialog', { name: '取消发布并恢复稳定版本？' }))
      .not.toBeInTheDocument()
    expect(platformApi.deliveryRunAction).toHaveBeenCalledTimes(2)
    expect(document.activeElement).toBe(cancelTrigger)
    await userEvent.keyboard('{Enter}')
    await expect
      .element(screen.getByRole('dialog', { name: '取消发布并恢复稳定版本？' }))
      .toBeInTheDocument()
    const compatibilityAcknowledgement = screen.getByRole('checkbox', {
      name: '确认稳定版本兼容当前数据库与外部配置',
    })
    await expect.element(compatibilityAcknowledgement).toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '确认取消并恢复' }))
      .toBeDisabled()
    await userEvent.click(compatibilityAcknowledgement)
    const recoveryButton = screen.getByRole('button', {
      name: '确认取消并恢复',
    })
    await expect.element(recoveryButton).toBeEnabled()
    recoveryButton.element().focus()
    expect(document.activeElement).toBe(recoveryButton.element())
    const confirmBounds = recoveryButton.element().getBoundingClientRect()
    expect(confirmBounds.top).toBeGreaterThanOrEqual(0)
    expect(confirmBounds.bottom).toBeLessThanOrEqual(window.innerHeight)
    await userEvent.keyboard('{Enter}')
    await vi.waitFor(() => {
      expect(platformApi.deliveryRunAction).toHaveBeenLastCalledWith(
        93,
        expect.objectContaining({
          action: 'cancel',
          expected_version: 6,
          recovery_compatibility_acknowledged: true,
        })
      )
    })
    await expect
      .element(screen.getByText('发布已取消，生产入口已切回稳定版'))
      .toBeInTheDocument()
  })

  it('shows candidate cleanup only after the immutable retention window and confirms deletion', async () => {
    const plan = blueGreenPlan(56)
    plan.snapshot.candidate_retention_hours = 24
    vi.mocked(platformApi.deliveryPlan).mockResolvedValue({
      data: plan,
    } as never)
    vi.mocked(platformApi.promotionRequests).mockResolvedValue({
      data: [
        {
          id: 33,
          release_id: 42,
          prod_cluster_id: 3,
          requester: 'alice',
          status: 'approved',
          approver: 'bob',
          deploy_log_id: 712,
          delivery_plan_id: 56,
          created_at: '2026-10-01T00:00:00Z',
        },
      ],
    } as never)
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: {
        ...testLog,
        id: 712,
        status: 'failed',
        current_step: 'restored',
        finished_at: '2026-10-01T00:00:00Z',
        error_message: '稳定版本已恢复',
      },
    } as never)
    vi.mocked(platformApi.deployActions).mockResolvedValue({
      data: [],
    } as never)
    const restoredRun = {
      run: {
        id: 92,
        delivery_plan_id: 56,
        promotion_request_id: 33,
        deploy_log_id: 712,
        strategy: 'blue_green' as const,
        status: 'failed',
        current_stage: 'restored',
        version: 5,
        created_at: '2026-10-01T00:00:00Z',
        updated_at: '2026-10-01T00:00:00Z',
      },
      plan,
      deploy_log: {
        ...testLog,
        id: 712,
        status: 'failed',
        current_step: 'restored',
        finished_at: '2026-10-01T00:00:00Z',
      },
      events: [],
      actions: [
        {
          id: 1,
          deploy_log_id: 712,
          action_key: 'blue-green:candidate-service',
          action_kind: 'create_candidate_Service',
          status: 'applied' as const,
          intent: {},
          created_at: '2026-10-01T00:00:00Z',
        },
        {
          id: 2,
          deploy_log_id: 712,
          action_key: 'blue-green:candidate-deployment',
          action_kind: 'create_candidate_Deployment',
          status: 'applied' as const,
          intent: {},
          created_at: '2026-10-01T00:00:00Z',
        },
      ],
    }
    vi.mocked(platformApi.deliveryRunByDeployLog).mockResolvedValue({
      data: restoredRun,
    } as never)
    vi.mocked(platformApi.deliveryRunAction).mockResolvedValue({
      data: {
        ...restoredRun,
        run: { ...restoredRun.run, version: 6 },
        actions: [
          ...restoredRun.actions,
          {
            id: 3,
            deploy_log_id: 712,
            action_key: 'blue-green:cleanup:candidate-service',
            action_kind: 'delete_candidate_Service',
            status: 'applied' as const,
            intent: {},
            created_at: '2026-10-02T00:00:00Z',
          },
          {
            id: 4,
            deploy_log_id: 712,
            action_key: 'blue-green:cleanup:candidate-deployment',
            action_kind: 'delete_candidate_Deployment',
            status: 'applied' as const,
            intent: {},
            created_at: '2026-10-02T00:00:00Z',
          },
        ],
      },
    } as never)

    const screen = await renderWithClient(
      <DeliveryPlanSnapshotDialog planID={56} onClose={vi.fn()} />
    )
    await expect
      .element(screen.getByRole('button', { name: '清理未启用候选' }))
      .toBeEnabled()
    await userEvent.click(
      screen.getByRole('button', { name: '清理未启用候选' })
    )
    await expect
      .element(
        screen.getByText(/只删除本计划创建的预览 Service 与候选 Deployment/)
      )
      .toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '确认清理候选' }))
    await vi.waitFor(() => {
      expect(platformApi.deliveryRunAction).toHaveBeenCalledWith(
        92,
        expect.objectContaining({
          action: 'cleanup_candidate',
          expected_version: 5,
        })
      )
    })
  })

  it('opens a read-only immutable snapshot for an approval reviewer', async () => {
    const plan: DeliveryPlan = {
      id: 52,
      release_id: 42,
      target_cluster_id: 3,
      target_app_id: 9,
      strategy: 'rolling',
      status: 'preview_only',
      snapshot_hash: 'a'.repeat(64),
      requested_by: 'alice',
      created_at: '2026-10-08T00:00:00Z',
      blockers: [],
      snapshot: {
        schema_version: 1,
        strategy: 'rolling',
        artifact: {
          release_id: 42,
          event_id: 'evt-42',
          image: 'registry.example.com/proj/shop-api@sha256:abc',
          version: '1.2.3',
        },
        target: {
          cluster_id: 3,
          cluster_name: 'prod-east',
          app_id: 9,
          app_name: 'shop-api',
          test_app_id: 1,
          test_binding_id: 11,
          test_environment_id: 1,
          test_environment_name: '测试',
          namespace: 'production',
          deployment: 'shop-api',
          container: 'api',
          environment_id: 2,
          environment_name: '生产',
          environment_tier: 'production',
          application_id: 1,
          application_name: '商城',
          component_id: 1,
          component_name: 'shop-api',
          binding_id: 22,
          workload_uid: 'workload-uid',
          resource_version: 'rv-123',
          current_image: 'registry.example.com/proj/shop-api:1.1.0',
          workload_hash: 'b'.repeat(64),
        },
      },
    }
    vi.mocked(platformApi.deliveryPlan).mockResolvedValue({
      data: plan,
    } as never)
    vi.mocked(platformApi.promotionRequests).mockResolvedValue({
      data: [
        {
          id: 15,
          release_id: 42,
          prod_cluster_id: 3,
          requester: 'alice',
          status: 'approved',
          approver: 'bob',
          deploy_log_id: 701,
          delivery_plan_id: 52,
          created_at: '2026-10-08T00:00:00Z',
        },
      ],
    } as never)
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: {
        ...testLog,
        id: 701,
        status: 'running',
        current_step: 'rollout_wait',
        step_detail: JSON.stringify([
          {
            step: 'k8s_set_image',
            success: true,
            stdout: '',
            stderr: '',
          },
        ]),
      },
    } as never)
    vi.mocked(platformApi.deployActions).mockResolvedValue({
      data: [
        {
          id: 1,
          deploy_log_id: 701,
          action_key: 'set-image',
          action_kind: 'set_deployment_image',
          status: 'applied',
          intent: {
            previous_image: 'registry.example.com/proj/shop-api:1.1.0',
            target_image: 'registry.example.com/proj/shop-api@sha256:abc',
          },
          observed: {
            current_image: 'registry.example.com/proj/shop-api@sha256:abc',
            readback: 'completed',
          },
          outcome_code: 'target_confirmed',
          created_at: '2026-10-08T00:00:00Z',
        },
      ],
    } as never)
    const screen = await renderWithClient(
      <DeliveryPlanSnapshotDialog planID={52} onClose={vi.fn()} />
    )
    await expect.element(screen.getByText('发布计划 #52')).toBeInTheDocument()
    await expect
      .element(
        screen
          .getByRole('region', { name: '发布执行进度' })
          .getByText('registry.example.com/proj/shop-api:1.1.0')
      )
      .toBeInTheDocument()
    await expect
      .element(
        screen
          .getByRole('region', { name: '发布执行进度' })
          .getByText('registry.example.com/proj/shop-api@sha256:abc')
          .last()
      )
      .toBeInTheDocument()
    await expect.element(screen.getByText(/当前窗口只读/)).toBeInTheDocument()
    await expect
      .element(screen.getByRole('region', { name: '发布执行进度' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('#701', { exact: true }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('正在执行：等待滚动更新'))
      .toBeInTheDocument()
    await expect.element(screen.getByText('已读回确认')).toBeInTheDocument()
    await expect
      .element(
        screen.getByText('registry.example.com/proj/shop-api@sha256:abc').last()
      )
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('button', { name: '关闭快照' }))
      .toBeInTheDocument()
  })

  it('offers guarded reconciliation for an interrupted approved task', async () => {
    const plan: DeliveryPlan = {
      id: 53,
      release_id: 42,
      target_cluster_id: 3,
      target_app_id: 9,
      strategy: 'rolling',
      status: 'preview_only',
      snapshot_hash: 'c'.repeat(64),
      requested_by: 'alice',
      created_at: '2026-10-08T00:00:00Z',
      blockers: [],
      snapshot: {
        schema_version: 1,
        strategy: 'rolling',
        artifact: {
          release_id: 42,
          event_id: 'evt-53',
          image: 'registry.example.com/proj/shop-api@sha256:abc',
          version: '1.2.3',
        },
        target: {
          cluster_id: 3,
          cluster_name: 'prod-east',
          app_id: 9,
          app_name: 'shop-api',
          test_app_id: 1,
          test_binding_id: 11,
          test_environment_id: 1,
          test_environment_name: '测试',
          namespace: 'production',
          deployment: 'shop-api',
          container: 'api',
          environment_id: 2,
          environment_name: '生产',
          environment_tier: 'production',
          application_id: 1,
          application_name: '商城',
          component_id: 1,
          component_name: 'shop-api',
          binding_id: 22,
          workload_uid: 'workload-uid',
          resource_version: 'rv-123',
          current_image: 'registry.example.com/proj/shop-api:1.1.0',
          workload_hash: 'd'.repeat(64),
        },
      },
    }
    vi.mocked(platformApi.deliveryPlan).mockResolvedValue({
      data: plan,
    } as never)
    vi.mocked(platformApi.promotionRequests).mockResolvedValue({
      data: [
        {
          id: 16,
          release_id: 42,
          prod_cluster_id: 3,
          requester: 'alice',
          status: 'approved',
          approver: 'bob',
          deploy_log_id: 702,
          delivery_plan_id: 53,
          created_at: '2026-10-08T00:00:00Z',
        },
      ],
    } as never)
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: {
        ...testLog,
        id: 702,
        status: 'interrupted',
        current_step: 'k8s_set_image',
        error_message: '镜像变更结果未知，需要核对集群',
      },
    } as never)
    vi.mocked(platformApi.deployActions).mockResolvedValue({
      data: [
        {
          id: 2,
          deploy_log_id: 702,
          action_key: 'set-image',
          action_kind: 'set_deployment_image',
          status: 'unknown',
          intent: {
            previous_image: 'registry.example.com/proj/shop-api:1.1.0',
            target_image: 'registry.example.com/proj/shop-api@sha256:abc',
          },
          observed: { readback: 'failed' },
          outcome_code: 'readback_failed',
          created_at: '2026-10-08T00:00:00Z',
        },
      ],
    } as never)
    const screen = await renderWithClient(
      <DeliveryPlanSnapshotDialog planID={53} onClose={vi.fn()} />
    )
    const reconcileButton = screen.getByRole('button', {
      name: '核对集群并继续',
    })
    await expect.element(reconcileButton).toBeInTheDocument()
    await userEvent.click(reconcileButton)
    await vi.waitFor(() => {
      expect(platformApi.reconcileDeployLog).toHaveBeenCalledWith(702)
    })
    expect(toast.success).toHaveBeenCalledWith('已核对并继续')
  })
})

describe('VerificationDialog', () => {
  it('requires a conclusion, binds the digest and refuses unsafe links', async () => {
    vi.mocked(platformApi.recordVerification).mockResolvedValue({
      data: {},
    } as never)
    const close = vi.fn()
    const candidate = { ...release, image_digest: 'sha256:' + 'ab'.repeat(32) }
    const screen = await renderWithClient(
      <VerificationDialog release={candidate} onClose={close} />
    )
    const save = screen.getByRole('button', { name: '保存验收' })
    await expect.element(save).toBeDisabled()
    await userEvent.fill(
      screen.getByLabelText('验证内容与结论依据（必填）'),
      '登录和订单 smoke 通过'
    )
    await userEvent.fill(
      screen.getByLabelText('报告链接（可选，HTTP/HTTPS）'),
      'javascript:alert(1)'
    )
    await expect.element(save).toBeDisabled()
    await userEvent.fill(
      screen.getByLabelText('报告链接（可选，HTTP/HTTPS）'),
      'https://ci.example/report'
    )
    await userEvent.click(save)
    await vi.waitFor(() =>
      expect(platformApi.recordVerification).toHaveBeenCalledWith(
        42,
        expect.objectContaining({
          digest: candidate.image_digest,
          kind: 'manual',
          status: 'passed',
          summary: '登录和订单 smoke 通过',
        })
      )
    )
    await vi.waitFor(() => expect(close).toHaveBeenCalled())
  })
})
