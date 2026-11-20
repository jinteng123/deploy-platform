import type { AnchorHTMLAttributes, ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page } from 'vitest/browser'
import {
  platformApi,
  type ComponentBinding,
  type DeployLog,
  type ImageReleaseView,
} from '@/api/client'
import { SidebarInset, SidebarProvider } from '@/components/ui/sidebar'
import { DeployLogPage } from './deploy-log-page'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    platformApi: {
      deployLog: vi.fn(),
      clusters: vi.fn(),
      release: vi.fn(),
    },
  }
})

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    Link: ({
      to,
      search,
      params,
      children,
      ...props
    }: {
      to: string
      search?: Record<string, string | number | undefined>
      params?: Record<string, string>
      children?: ReactNode
    } & AnchorHTMLAttributes<HTMLAnchorElement>) => {
      const path = Object.entries(params ?? {}).reduce(
        (current, [key, value]) => current.replace(`$${key}`, value),
        to
      )
      const url = new URL(path, 'http://localhost')
      Object.entries(search ?? {}).forEach(([key, value]) => {
        if (value != null) url.searchParams.set(key, String(value))
      })
      return (
        <a href={`${url.pathname}${url.search}`} {...props}>
          {children}
        </a>
      )
    },
  }
})

const failedLog: DeployLog = {
  id: 781,
  cluster_id: 12,
  app_name: 'checkout-api',
  deployment_name: 'checkout-api',
  container_name: 'api',
  namespace: 'production',
  version: '2026.10.15-rc2',
  image_full:
    'registry.example.com/commerce/checkout-api@sha256:' + 'abcde'.repeat(13),
  deploy_type: 'deployment',
  status: 'failed',
  current_step: 'k8s_set_image',
  step_detail: JSON.stringify([
    { step: 'check_file', success: true, stdout: '', stderr: '' },
    {
      step: 'k8s_set_image',
      success: false,
      stdout: '',
      stderr: 'deployment is temporarily unavailable',
    },
  ]),
  error_message: 'deployment is temporarily unavailable',
  started_at: '2026-10-15T08:10:00Z',
  finished_at: '2026-10-15T08:12:00Z',
  environment_tier_snapshot: 'production',
  created_at: '2026-10-15T08:10:00Z',
}

const sourceBinding: ComponentBinding = {
  id: 44,
  cluster_id: 12,
  app_name: 'checkout-api',
  source_kind: 'repository',
  source_repo_url: 'https://codeup.aliyun.com/team/commerce/checkout.git',
  source_path: 'services/checkout-api',
  image_repository: 'registry.example.com/commerce/checkout-api',
  deployment_name: 'checkout-api',
  container_name: 'api',
  namespace: 'production',
  match_keyword: 'checkout-api',
  harbor_project: 'commerce',
  created_at: '2026-10-01T00:00:00Z',
}

const linkedRelease: ImageReleaseView = {
  id: 420,
  event_id: 'evt-checkout-420',
  source: 'yunxiao',
  app_name: 'checkout-api',
  image_full: failedLog.image_full!,
  version: failedLog.version,
  pipeline_name: 'checkout-build',
  pipeline_id: 'pipeline-22',
  build_number: '156',
  branch: 'main',
  commit_id: 'abcdef1234567890',
  test_cluster_id: 12,
  test_deploy_log_id: failedLog.id,
  created_at: '2026-10-15T08:00:00Z',
  updated_at: '2026-10-15T08:00:00Z',
  test_status: 'failed',
  can_promote: false,
  metadata: {
    digest_source: 'ci',
    source_repositories: ['codeup.aliyun.com/team/commerce/checkout'],
    source_path: 'services/checkout-api',
    binding: sourceBinding,
  },
}

function renderPage(logID = failedLog.id) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <SidebarProvider>
        <SidebarInset>
          <DeployLogPage logID={logID} />
        </SidebarInset>
      </SidebarProvider>
    </QueryClientProvider>
  ).then((screen) => ({ client, screen }))
}

describe('DeployLogPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    document.documentElement.classList.remove('dark')
    vi.mocked(platformApi.clusters).mockResolvedValue({ data: [] } as never)
  })

  it('shows actionable failure context and keeps long image references inside a mobile viewport', async () => {
    await page.viewport(390, 844)
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: failedLog,
    } as never)
    vi.mocked(platformApi.clusters).mockResolvedValue({
      data: [
        {
          id: 12,
          name: '生产集群',
          env_code: 'prod-east',
          environment: 'production',
          environment_tier: 'production',
          deploy_policy: 'manual',
          connection_type: 'kubeconfig',
          credential_configured: true,
          insecure_skip_tls_verify: false,
          enabled: true,
          default_namespace: 'production',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
        },
      ],
    } as never)
    const { client, screen } = await renderPage()

    await expect
      .element(screen.getByRole('heading', { name: 'checkout-api' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('先核对集群实际状态'))
      .not.toBeInTheDocument()
    await expect.element(screen.getByText('发布任务失败')).toBeInTheDocument()
    await expect
      .element(screen.getByText('更新镜像', { exact: true }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('生产集群 (prod-east)', { exact: true }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText(failedLog.image_full!))
      .toBeInTheDocument()
    await expect
      .element(
        screen.getByText('本任务没有关联制品发布单，可能来自旧版或手动部署。')
      )
      .toBeInTheDocument()
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)

    try {
      await page.screenshot({
        path: '../../../../docs/visual-checks/deploy-log-failed-390-light.png',
      })
      document.documentElement.classList.add('dark')
      await new Promise((resolve) => setTimeout(resolve, 100))
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
      await page.screenshot({
        path: '../../../../docs/visual-checks/deploy-log-failed-390-dark.png',
      })
    } finally {
      document.documentElement.classList.remove('dark')
    }

    client.clear()
  })

  it('traces a deployment task to its build and immutable component binding', async () => {
    await page.viewport(390, 844)
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: { ...failedLog, release_id: linkedRelease.id },
    } as never)
    vi.mocked(platformApi.release).mockResolvedValue({
      data: linkedRelease,
    } as never)
    const { client, screen } = await renderPage()

    try {
      await expect
        .element(screen.getByText('构建与组件绑定', { exact: true }))
        .toBeInTheDocument()
      await expect
        .element(screen.getByText('checkout-build (#pipeline-22) · 构建 #156'))
        .toBeInTheDocument()
      await expect
        .element(
          screen.getByText(
            'https://codeup.aliyun.com/team/commerce/checkout.git'
          )
        )
        .toBeInTheDocument()
      await expect
        .element(
          screen.getByText('codeup.aliyun.com/team/commerce/checkout', {
            exact: true,
          })
        )
        .toBeInTheDocument()
      await expect
        .element(screen.getByTestId('release-source-binding-status'))
        .toHaveTextContent('仓库与目录已匹配')
      await expect
        .element(screen.getByTestId('release-task-image-status'))
        .toHaveTextContent('部署镜像与发布制品一致')
      await expect
        .element(screen.getByRole('link', { name: '查看发布单详情' }))
        .toHaveAttribute('href', `/tasks?release_id=${linkedRelease.id}`)
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
      await page.screenshot({
        path: '../../../../docs/visual-checks/deploy-log-build-binding-390-light.png',
        element: screen.getByTestId('release-build-context-card'),
      })
      document.documentElement.classList.add('dark')
      await new Promise((resolve) => setTimeout(resolve, 100))
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
      await page.screenshot({
        path: '../../../../docs/visual-checks/deploy-log-build-binding-390-dark.png',
        element: screen.getByTestId('release-build-context-card'),
      })
      client.clear()
    } finally {
      document.documentElement.classList.remove('dark')
      await page.viewport(1280, 800)
    }
  })

  it('marks a source or image mismatch instead of implying the task matches its release', async () => {
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: { ...failedLog, release_id: linkedRelease.id },
    } as never)
    vi.mocked(platformApi.release).mockResolvedValue({
      data: {
        ...linkedRelease,
        image_full:
          'registry.example.com/commerce/checkout-api@sha256:' + 'f'.repeat(64),
        metadata: {
          ...linkedRelease.metadata,
          source_repositories: ['git.example.com/other/checkout'],
          source_path: 'services/other-api',
        },
      },
    } as never)
    const { client, screen } = await renderPage()

    await expect
      .element(screen.getByTestId('release-source-binding-status'))
      .toHaveTextContent('代码仓库与组件绑定不一致')
    await expect
      .element(screen.getByTestId('release-task-image-status'))
      .toHaveTextContent('部署镜像与发布制品不一致')

    client.clear()
  })

  it('distinguishes a repository match from a build-directory mismatch', async () => {
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: { ...failedLog, release_id: linkedRelease.id },
    } as never)
    vi.mocked(platformApi.release).mockResolvedValue({
      data: {
        ...linkedRelease,
        metadata: {
          ...linkedRelease.metadata,
          source_path: 'services/payment-api',
        },
      },
    } as never)
    const { client, screen } = await renderPage()

    await expect
      .element(screen.getByTestId('release-source-binding-status'))
      .toHaveTextContent('仓库已匹配，构建目录不一致')
    await expect
      .element(screen.getByTestId('release-task-image-status'))
      .toHaveTextContent('部署镜像与发布制品一致')

    client.clear()
  })

  it('explains interrupted tasks without encouraging an unsafe retry', async () => {
    vi.mocked(platformApi.deployLog).mockResolvedValue({
      data: {
        ...failedLog,
        status: 'interrupted',
        error_message: 'result could not be confirmed',
      },
    } as never)
    const { client, screen } = await renderPage()

    await expect
      .element(screen.getByText('先核对集群实际状态'))
      .toBeInTheDocument()
    await expect
      .element(
        screen.getByText(
          '任务在外部操作结果无法确认时停止。请核对集群中的实际镜像和资源状态；确认未知写入前不要直接重试。'
        )
      )
      .toBeInTheDocument()

    client.clear()
  })

  it('shows a clear not-found state when the task link is stale', async () => {
    vi.mocked(platformApi.deployLog).mockRejectedValue({
      response: { status: 404 },
    })
    const { client, screen } = await renderPage(9999)

    await expect.element(screen.getByText('找不到这条任务')).toBeInTheDocument()
    await expect
      .element(screen.getByRole('link', { name: '返回发布中心' }))
      .toHaveAttribute('href', '../../')

    client.clear()
  })
})
