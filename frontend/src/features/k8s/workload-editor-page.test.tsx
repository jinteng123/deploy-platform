import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import * as YAML from 'yaml'
import { k8sApi, type K8sObject } from '@/api/client'
import { DirectionProvider } from '@/context/direction-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { SidebarProvider } from '@/components/ui/sidebar'
import { WorkloadEditorPage } from './workload-editor-page'

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    k8sApi: {
      resourceTypes: vi.fn(),
      resource: vi.fn(),
      resources: vi.fn(),
      update: vi.fn(),
      capabilities: vi.fn(),
    },
  }
})

const mockResource = vi.mocked(k8sApi.resource)
const mockTypes = vi.mocked(k8sApi.resourceTypes)
const mockResources = vi.mocked(k8sApi.resources)
const mockUpdate = vi.mocked(k8sApi.update)
const mockCaps = vi.mocked(k8sApi.capabilities)

const deployment: K8sObject = {
  apiVersion: 'apps/v1',
  kind: 'Deployment',
  metadata: {
    name: 'web',
    namespace: 'prod',
    resourceVersion: '7',
    labels: { app: 'web' },
  },
  spec: {
    replicas: 2,
    selector: { matchLabels: { app: 'web' } },
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        containers: [
          {
            name: 'app',
            image: 'nginx:1',
            env: [{ name: 'MODE', value: 'prod' }],
          },
        ],
      },
    },
  },
}

const deploymentWithVolumes: K8sObject = {
  ...deployment,
  spec: {
    ...deployment.spec,
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        initContainers: [
          {
            name: 'seed',
            image: 'busybox:1',
            volumeMounts: [
              { name: 'data', mountPath: '/seed', readOnly: true },
              {
                name: 'conf',
                mountPath: '/seed-conf',
                subPathExpr: '$(POD_NAME)/conf.yaml',
              },
            ],
          },
        ],
        containers: [
          {
            name: 'app',
            image: 'nginx:1',
            volumeMounts: [
              { name: 'data', mountPath: '/var/lib/data' },
              { name: 'conf', mountPath: '/etc/app', subPath: 'app.yaml' },
            ],
          },
        ],
        volumes: [
          {
            name: 'data',
            persistentVolumeClaim: { claimName: 'prod-data', readOnly: true },
          },
          {
            name: 'conf',
            configMap: {
              name: 'app-conf',
              items: [{ key: 'app.yaml', path: 'app.yaml' }],
              defaultMode: 420,
            },
          },
          { name: 'scratch', emptyDir: {} },
          { name: 'misc', projected: { sources: [] } },
        ],
      },
    },
  },
}

const cronJob: K8sObject = {
  apiVersion: 'batch/v1',
  kind: 'CronJob',
  metadata: { name: 'backup', namespace: 'prod', resourceVersion: '3' },
  spec: {
    schedule: '*/5 * * * *',
    concurrencyPolicy: 'Forbid',
    jobTemplate: {
      spec: {
        template: {
          spec: {
            restartPolicy: 'Never',
            containers: [
              {
                name: 'job',
                image: 'alpine:3',
                env: [{ name: 'CRON_MODE', value: 'x' }],
              },
            ],
          },
        },
      },
    },
  },
}

const deploymentWithPort: K8sObject = {
  ...deployment,
  spec: {
    ...deployment.spec,
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        containers: [
          {
            name: 'app',
            image: 'nginx:1',
            ports: [{ name: 'http', containerPort: 80, protocol: 'TCP' }],
          },
        ],
      },
    },
  },
}

const deploymentAdvanced: K8sObject = {
  ...deployment,
  spec: {
    ...deployment.spec,
    strategy: {
      type: 'RollingUpdate',
      rollingUpdate: { maxUnavailable: 1 },
      extraField: 'keep-me',
    },
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        containers: [{ name: 'app', image: 'nginx:1' }],
        securityContext: { seccompProfile: { type: 'RuntimeDefault' } },
        affinity: { nodeAffinity: {} },
        tolerations: [
          {
            key: 'dedicated',
            operator: 'Equal',
            value: 'gpu',
            effect: 'NoSchedule',
          },
        ],
      },
    },
  },
}

const statefulSet: K8sObject = {
  apiVersion: 'apps/v1',
  kind: 'StatefulSet',
  metadata: { name: 'db', namespace: 'prod', resourceVersion: '4' },
  spec: {
    replicas: 1,
    serviceName: 'db-svc',
    podManagementPolicy: 'OrderedReady',
    updateStrategy: { type: 'RollingUpdate', rollingUpdate: { partition: 0 } },
    selector: { matchLabels: { app: 'db' } },
    template: {
      metadata: { labels: { app: 'db' } },
      spec: { containers: [{ name: 'db', image: 'postgres:15' }] },
    },
  },
}

const daemonSet: K8sObject = {
  apiVersion: 'apps/v1',
  kind: 'DaemonSet',
  metadata: { name: 'agent', namespace: 'prod', resourceVersion: '5' },
  spec: {
    selector: { matchLabels: { app: 'agent' } },
    template: {
      metadata: { labels: { app: 'agent' } },
      spec: { containers: [{ name: 'agent', image: 'agent:1' }] },
    },
  },
}

const job: K8sObject = {
  apiVersion: 'batch/v1',
  kind: 'Job',
  metadata: { name: 'task', namespace: 'prod', resourceVersion: '6' },
  spec: {
    completions: 1,
    parallelism: 1,
    template: {
      spec: {
        restartPolicy: 'OnFailure',
        containers: [{ name: 'task', image: 'alpine:3' }],
      },
    },
  },
}

const deploymentWithResources: K8sObject = {
  ...deployment,
  spec: {
    ...deployment.spec,
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        containers: [
          {
            name: 'app',
            image: 'nginx:1',
            resources: {
              requests: {
                cpu: '100m',
                memory: '128Mi',
                'ephemeral-storage': '1Gi',
                'hugepages-2Mi': '80Mi',
              },
              limits: {
                cpu: '500m',
                memory: '256Mi',
                'ephemeral-storage': '2Gi',
                'hugepages-2Mi': '80Mi',
              },
              claims: [{ name: 'res-claim' }],
              resizePolicy: [
                { resourceName: 'cpu', restartPolicy: 'NotRequired' },
              ],
            },
          },
        ],
      },
    },
  },
}

const deploymentWithGpu: K8sObject = {
  ...deployment,
  spec: {
    ...deployment.spec,
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        containers: [
          {
            name: 'app',
            image: 'nginx:1',
            resources: {
              requests: { cpu: '100m', 'nvidia.com/gpu': '2' },
              limits: { cpu: '500m', 'nvidia.com/gpu': '2' },
            },
          },
        ],
      },
    },
  },
}

const deploymentMixedGpu: K8sObject = {
  ...deployment,
  spec: {
    ...deployment.spec,
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        containers: [
          {
            name: 'app',
            image: 'nginx:1',
            resources: {
              requests: { cpu: '100m', 'nvidia.com/gpu': '1' },
              limits: { cpu: '500m', 'amd.com/gpu': '2' },
            },
          },
        ],
      },
    },
  },
}

const deploymentGpuMismatch: K8sObject = {
  ...deployment,
  spec: {
    ...deployment.spec,
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        containers: [
          {
            name: 'app',
            image: 'nginx:1',
            resources: {
              requests: { 'nvidia.com/gpu': '1' },
              limits: { 'nvidia.com/gpu': '2' },
            },
          },
        ],
      },
    },
  },
}

const deploymentWithEnv: K8sObject = {
  ...deployment,
  spec: {
    ...deployment.spec,
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        initContainers: [
          {
            name: 'seed',
            image: 'busybox:1',
            env: [{ name: 'SEED_ONLY', value: 'x' }],
          },
        ],
        containers: [
          {
            name: 'app',
            image: 'nginx:1',
            env: [
              { name: 'MODE', value: 'prod' },
              { name: 'MODE', value: 'dup' },
              {
                name: 'SECRET_VAL',
                valueFrom: {
                  secretKeyRef: {
                    name: 'app-secret',
                    key: 'token',
                    optional: false,
                  },
                },
              },
              {
                name: 'CONF_VAL',
                valueFrom: {
                  configMapKeyRef: { name: 'app-conf', key: 'app.yaml' },
                },
              },
              {
                name: 'POD_IP',
                customFlag: 'keep',
                valueFrom: {
                  fieldRef: { apiVersion: 'v1', fieldPath: 'status.podIP' },
                },
              },
              {
                name: 'CPU_LIMIT',
                valueFrom: {
                  resourceFieldRef: {
                    resource: 'limits.cpu',
                    divisor: '1m',
                    containerName: 'app',
                  },
                },
              },
              {
                name: 'ODD',
                extra: 'keep',
                valueFrom: { unknownRef: { a: 1 } },
              },
              { name: 'EMPTYVF', valueFrom: {} },
            ],
            envFrom: [
              {
                configMapRef: { name: 'bulk-conf', optional: true },
                prefix: 'CONF_',
              },
              { secretRef: { name: 'bulk-secret' } },
              {
                configMapRef: { name: 'dual-cm' },
                secretRef: { name: 'dual-sec' },
                prefix: 'D_',
              },
              { configMapRef: { name: 'odd-cm' }, extraProp: 'keep' },
            ],
          },
        ],
      },
    },
  },
}

const deploymentTwoContainers: K8sObject = {
  ...deployment,
  spec: {
    ...deployment.spec,
    template: {
      metadata: { labels: { app: 'web' } },
      spec: {
        initContainers: [
          {
            name: 'seed',
            image: 'busybox:1',
            resources: {
              requests: { cpu: '50m' },
              limits: { cpu: '80m' },
            },
          },
        ],
        containers: [
          {
            name: 'app',
            image: 'nginx:1',
            resources: {
              requests: { cpu: '200m' },
              limits: { cpu: '400m' },
            },
          },
        ],
      },
    },
  },
}

type Rendered = Awaited<ReturnType<typeof renderEditor>>

async function openStorageTab(screen: Rendered['screen']) {
  await userEvent.click(screen.getByRole('tab', { name: '存储挂载' }))
}

async function editVolume(screen: Rendered['screen'], index: number) {
  await userEvent.click(
    screen.getByRole('button', { name: `编辑数据卷 ${index}` })
  )
}

// Triggers the existing dry-run save and returns the manifest the editor
// would PUT, parsed back into an object.
async function dryRunManifest(screen: Rendered['screen']) {
  const before = mockUpdate.mock.calls.length
  await userEvent.click(screen.getByRole('button', { name: '保存' }))
  await vi.waitFor(() =>
    expect(mockUpdate.mock.calls.length).toBeGreaterThan(before)
  )
  const manifest = mockUpdate.mock.calls[
    mockUpdate.mock.calls.length - 1
  ][2] as string
  await expect
    .element(screen.getByText('校验通过，确认保存？'))
    .toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: '返回修改' }))
  return YAML.parse(manifest) as K8sObject
}

type LooseVolume = { name?: string } & Record<string, unknown>
type LooseContainer = {
  name?: string
  image?: string
  volumeMounts?: Record<string, unknown>[]
  env?: Record<string, unknown>[]
  envFrom?: Record<string, unknown>[]
  resources?: {
    requests?: Record<string, string>
    limits?: Record<string, string>
    claims?: unknown[]
    resizePolicy?: unknown[]
  }
}
type LoosePodSpec = {
  volumes?: LooseVolume[]
  containers?: LooseContainer[]
  initContainers?: LooseContainer[]
  restartPolicy?: string
  automountServiceAccountToken?: boolean
  hostNetwork?: boolean
  dnsPolicy?: string
  priorityClassName?: string
  serviceAccountName?: string
  schedulerName?: string
  securityContext?: Record<string, unknown>
  affinity?: unknown
  tolerations?: Record<string, unknown>[]
  imagePullSecrets?: { name?: string }[]
}

function podSpec(obj: K8sObject): LoosePodSpec {
  const spec = obj.spec as Record<string, unknown> | undefined
  const template = spec?.template as Record<string, unknown> | undefined
  return (template?.spec ?? {}) as LoosePodSpec
}

async function renderEditor(
  workload: K8sObject = deployment,
  searchOverride?: Partial<{
    cluster: number
    g: string
    v: string
    r: string
    ns: string
    name: string
  }>
) {
  mockResource.mockResolvedValue({ data: workload } as never)
  const onBack = vi.fn()
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DirectionProvider>
        <ThemeProvider>
          <SidebarProvider>
            <WorkloadEditorPage
              search={{
                cluster: 1,
                g: 'apps',
                v: 'v1',
                r: 'deployments',
                ns: 'prod',
                name: 'web',
                ...searchOverride,
              }}
              onBack={onBack}
            />
          </SidebarProvider>
        </ThemeProvider>
      </DirectionProvider>
    </QueryClientProvider>
  )
  return { screen, onBack }
}

// Grants update permission + an editable type def for a non-Deployment
// workload; the defaults only cover apps/v1/deployments.
function allowEditing(search: {
  g: string
  v: string
  r: string
  kind: string
}) {
  mockTypes.mockResolvedValue({
    data: [
      {
        group: search.g,
        version: search.v,
        resource: search.r,
        kind: search.kind,
        namespaced: true,
        editable: true,
        scalable: false,
        imageable: false,
        restartable: false,
        deletable: true,
      },
    ],
  } as never)
  mockCaps.mockResolvedValue({
    data: {
      resources: {
        [`${search.g}/${search.v}/${search.r}`]: {
          list: true,
          update: true,
          delete: true,
        },
      },
      exec: true,
      logs: true,
    },
  } as never)
}

// Mock implementations survive clearAllMocks across sibling describes, so
// both describes install the same defaults per test.
function mockApiDefaults() {
  vi.clearAllMocks()
  mockResource.mockResolvedValue({ data: deployment } as never)
  mockTypes.mockResolvedValue({
    data: [
      {
        group: 'apps',
        version: 'v1',
        resource: 'deployments',
        kind: 'Deployment',
        namespaced: true,
        editable: true,
        scalable: true,
        imageable: true,
        restartable: true,
        deletable: true,
      },
    ],
  } as never)
  mockResources.mockResolvedValue({
    data: {
      items: [
        { metadata: { name: 'prod-data' } },
        { metadata: { name: 'prod-extra' } },
      ],
    },
  } as never)
  mockCaps.mockResolvedValue({
    data: {
      resources: {
        'apps/v1/deployments': { list: true, update: true, delete: true },
      },
      exec: true,
      logs: true,
    },
  } as never)
  mockUpdate.mockResolvedValue({ data: deployment } as never)
}

describe('WorkloadEditorPage', () => {
  beforeEach(mockApiDefaults)

  it('renders the Kuboard-style form sections', async () => {
    const { screen } = await renderEditor()
    for (const title of [
      '基本信息',
      '容器信息',
      '存储挂载',
      '高级设置',
      '服务 / 应用路由',
    ]) {
      await expect
        .element(screen.getByRole('tab', { name: title }))
        .toBeInTheDocument()
    }
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    await expect
      .element(screen.getByRole('textbox', { name: '容器镜像 app' }))
      .toHaveValue('nginx:1')
  })

  it('dry-runs the visual form before applying the manifest', async () => {
    const { screen, onBack } = await renderEditor()
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const image = screen.getByRole('textbox', { name: '容器镜像 app' })
    await userEvent.fill(image, 'nginx:2')
    await userEvent.click(screen.getByRole('button', { name: '保存' }))
    await expect
      .element(screen.getByText('校验通过，确认保存？'))
      .toBeInTheDocument()
    expect(mockUpdate).toHaveBeenCalledWith(
      'web',
      expect.objectContaining({ cluster_id: 1, resource: 'deployments' }),
      expect.stringContaining('nginx:2'),
      true
    )
    await userEvent.click(screen.getByRole('button', { name: '确认保存' }))
    await vi.waitFor(() => expect(onBack).toHaveBeenCalled())
    expect(mockUpdate).toHaveBeenCalledTimes(2)
    expect(mockUpdate).toHaveBeenLastCalledWith(
      'web',
      expect.objectContaining({ cluster_id: 1, resource: 'deployments' }),
      expect.stringContaining('nginx:2')
    )
  })

  it('renders header identity and underlined tabs; save enables when dirty', async () => {
    const { screen } = await renderEditor()
    await expect
      .element(screen.getByRole('heading', { name: 'web' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText('Deployment').first())
      .toBeInTheDocument()
    await expect.element(screen.getByText('名称空间')).toBeInTheDocument()
    await expect.element(screen.getByText('prod').first()).toBeInTheDocument()
    for (const label of [
      '基本信息',
      '容器信息',
      '存储挂载',
      '高级设置',
      '服务 / 应用路由',
    ]) {
      await expect
        .element(screen.getByRole('tab', { name: label }))
        .toBeInTheDocument()
    }
    const save = screen.getByRole('button', { name: '保存' })
    await expect.element(save).toBeDisabled()
    await userEvent.fill(
      screen.getByRole('textbox', { name: '标签 值 1', exact: true }),
      'web2'
    )
    await expect.element(save).toBeEnabled()
    await expect
      .element(screen.getByText('有未保存的更改'))
      .toBeInTheDocument()
  })

  it('switches init and regular containers without losing draft edits', async () => {
    const { screen } = await renderEditor(deploymentWithVolumes)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const group = screen.getByRole('group', { name: '现有容器' })
    await expect
      .element(screen.getByRole('textbox', { name: '容器镜像 seed' }))
      .toHaveValue('busybox:1')
    await userEvent.fill(
      screen.getByRole('textbox', { name: '容器镜像 seed' }),
      'busybox:2'
    )
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    await userEvent.fill(
      screen.getByRole('textbox', { name: '容器镜像 app' }),
      'nginx:9'
    )
    const manifest = await dryRunManifest(screen)
    const ps = podSpec(manifest)
    expect(ps.initContainers?.[0].image).toBe('busybox:2')
    expect(ps.containers?.[0].image).toBe('nginx:9')
  })

  it('renders kind-specific advanced settings for Deployment', async () => {
    const { screen } = await renderEditor()
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    await expect.element(screen.getByText('工作负载策略')).toBeInTheDocument()
    await expect.element(screen.getByText('Pod 身份与安全')).toBeInTheDocument()
    await expect.element(screen.getByText('调度与容忍度')).toBeInTheDocument()
    await expect.element(screen.getByText('网络与 DNS')).toBeInTheDocument()
    // Long-running workloads always restart; the policy is read-only.
    await expect.element(screen.getByText('Always')).toBeInTheDocument()
    await expect
      .element(screen.getByRole('spinbutton', { name: '副本数' }))
      .toHaveValue(2)
  })

  it('edits Deployment rolling strategy IntOrString and strips it on Recreate', async () => {
    const { screen } = await renderEditor(deploymentAdvanced)
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    const maxUnavailable = screen.getByRole('textbox', {
      name: '滚动更新最大不可用数',
    })
    await expect.element(maxUnavailable).toHaveValue('1')
    await userEvent.fill(maxUnavailable, '25%')
    await userEvent.fill(
      screen.getByRole('textbox', { name: '滚动更新最大超出数' }),
      '2'
    )
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: '历史版本保留数' }),
      '5'
    )
    let manifest = await dryRunManifest(screen)
    let spec = manifest.spec as {
      strategy: Record<string, unknown>
      revisionHistoryLimit?: number
    }
    expect(spec.strategy.rollingUpdate).toEqual({
      maxUnavailable: '25%',
      maxSurge: 2,
    })
    expect(spec.revisionHistoryLimit).toBe(5)
    expect(spec.strategy.extraField).toBe('keep-me')
    // Switching to Recreate drops rollingUpdate, keeps unknown siblings.
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '更新策略类型' }),
      'Recreate'
    )
    manifest = await dryRunManifest(screen)
    spec = manifest.spec as { strategy: Record<string, unknown> }
    expect(spec.strategy.type).toBe('Recreate')
    expect(spec.strategy.rollingUpdate).toBeUndefined()
    expect(spec.strategy.extraField).toBe('keep-me')
  })

  it('edits StatefulSet rolling partition and removes it on OnDelete', async () => {
    allowEditing({ g: 'apps', v: 'v1', r: 'statefulsets', kind: 'StatefulSet' })
    const { screen } = await renderEditor(statefulSet, {
      r: 'statefulsets',
      name: 'db',
    })
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    // serviceName is immutable on existing StatefulSets: rendered as text.
    await expect.element(screen.getByText('db-svc')).toBeInTheDocument()
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: '滚动更新分区序号' }),
      '2'
    )
    let manifest = await dryRunManifest(screen)
    let spec = manifest.spec as {
      updateStrategy: Record<string, unknown>
    }
    expect(spec.updateStrategy.rollingUpdate).toEqual({ partition: 2 })
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '更新策略类型' }),
      'OnDelete'
    )
    manifest = await dryRunManifest(screen)
    spec = manifest.spec as { updateStrategy: Record<string, unknown> }
    expect(spec.updateStrategy.type).toBe('OnDelete')
    expect(spec.updateStrategy.rollingUpdate).toBeUndefined()
  })

  it('edits DaemonSet rolling maxUnavailable and keeps restart policy read-only', async () => {
    allowEditing({ g: 'apps', v: 'v1', r: 'daemonsets', kind: 'DaemonSet' })
    const { screen } = await renderEditor(daemonSet, {
      r: 'daemonsets',
      name: 'agent',
    })
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    await expect.element(screen.getByText('Always')).toBeInTheDocument()
    await userEvent.fill(
      screen.getByRole('textbox', { name: '滚动更新最大不可用数' }),
      '50%'
    )
    const manifest = await dryRunManifest(screen)
    const spec = manifest.spec as {
      updateStrategy: Record<string, unknown>
    }
    // type is not synthesized; Kubernetes defaults it to RollingUpdate.
    expect(spec.updateStrategy).toEqual({
      rollingUpdate: { maxUnavailable: '50%' },
    })
  })

  it('edits Job strategy fields and batch restart policy', async () => {
    allowEditing({ g: 'batch', v: 'v1', r: 'jobs', kind: 'Job' })
    const { screen } = await renderEditor(job, {
      g: 'batch',
      r: 'jobs',
      name: 'task',
    })
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    await expect
      .element(screen.getByRole('combobox', { name: '重启策略' }))
      .toHaveValue('OnFailure')
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: '重试上限' }),
      '4'
    )
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: '完成后保留秒数' }),
      '120'
    )
    const manifest = await dryRunManifest(screen)
    const spec = manifest.spec as {
      backoffLimit?: number
      ttlSecondsAfterFinished?: number
    }
    expect(spec.backoffLimit).toBe(4)
    expect(spec.ttlSecondsAfterFinished).toBe(120)
  })

  it('renders CronJob advanced settings under the shared blocks', async () => {
    allowEditing({ g: 'batch', v: 'v1', r: 'cronjobs', kind: 'CronJob' })
    const { screen } = await renderEditor(cronJob, {
      g: 'batch',
      r: 'cronjobs',
      name: 'backup',
    })
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    await expect.element(screen.getByText('工作负载策略')).toBeInTheDocument()
    await expect.element(screen.getByText('调度表达式')).toBeInTheDocument()
    await expect.element(screen.getByText('并发策略')).toBeInTheDocument()
    await expect
      .element(screen.getByRole('textbox').first())
      .toHaveValue('*/5 * * * *')
    await expect
      .element(screen.getByRole('combobox').first())
      .toHaveValue('Forbid')
    await userEvent.fill(
      screen.getByRole('textbox', { name: '时区' }),
      'Asia/Shanghai'
    )
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: '成功任务保留数' }),
      '2'
    )
    const manifest = await dryRunManifest(screen)
    const spec = manifest.spec as {
      timeZone?: string
      successfulJobsHistoryLimit?: number
      jobTemplate: Record<string, unknown>
    }
    expect(spec.timeZone).toBe('Asia/Shanghai')
    expect(spec.successfulJobsHistoryLimit).toBe(2)
    // Pod-level fields still write through jobTemplate.spec.template.spec.
    const template = spec.jobTemplate.spec as Record<string, unknown>
    expect(
      (template.template as Record<string, unknown>).spec as Record<
        string,
        unknown
      >
    ).toMatchObject({ restartPolicy: 'Never' })
  })

  it('writes tri-state pod fields and preserves unknown securityContext keys', async () => {
    const { screen } = await renderEditor(deploymentAdvanced)
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    const automount = screen.getByRole('combobox', {
      name: '自动挂载服务账号令牌',
    })
    await expect.element(automount).toHaveValue('')
    await userEvent.selectOptions(automount, 'true')
    let manifest = await dryRunManifest(screen)
    expect(podSpec(manifest).automountServiceAccountToken).toBe(true)
    await userEvent.selectOptions(automount, 'false')
    manifest = await dryRunManifest(screen)
    expect(podSpec(manifest).automountServiceAccountToken).toBe(false)
    await userEvent.selectOptions(automount, '')
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '使用主机网络' }),
      'true'
    )
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: 'DNS 策略' }),
      'ClusterFirstWithHostNet'
    )
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: '运行用户 UID' }),
      '1000'
    )
    manifest = await dryRunManifest(screen)
    const ps = podSpec(manifest)
    expect(ps.automountServiceAccountToken).toBeUndefined()
    expect(ps.hostNetwork).toBe(true)
    expect(ps.dnsPolicy).toBe('ClusterFirstWithHostNet')
    // runAsUser merges in; seccompProfile survives untouched.
    expect(ps.securityContext).toEqual({
      seccompProfile: { type: 'RuntimeDefault' },
      runAsUser: 1000,
    })
    expect(ps.affinity).toEqual({ nodeAffinity: {} })
  })

  it('edits tolerations and imagePullSecrets without touching affinity', async () => {
    const { screen } = await renderEditor(deploymentAdvanced)
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    await expect
      .element(screen.getByRole('textbox', { name: '容忍度 1 键' }))
      .toHaveValue('dedicated')
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '容忍度 1 效果' }),
      'NoExecute'
    )
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: '容忍度 1 秒数' }),
      '60'
    )
    await userEvent.click(screen.getByRole('button', { name: '添加容忍度' }))
    await userEvent.fill(
      screen.getByRole('textbox', { name: '容忍度 2 键' }),
      'node-role'
    )
    await userEvent.click(
      screen.getByRole('button', { name: '添加镜像拉取凭据' })
    )
    const pullSecret = screen.getByRole('combobox', {
      name: '镜像拉取凭据 1 名称',
    })
    await expect.element(pullSecret).toHaveTextContent('prod-extra')
    await userEvent.selectOptions(pullSecret, 'prod-extra')
    let manifest = await dryRunManifest(screen)
    let ps = podSpec(manifest)
    expect(ps.tolerations).toEqual([
      {
        key: 'dedicated',
        operator: 'Equal',
        value: 'gpu',
        effect: 'NoExecute',
        tolerationSeconds: 60,
      },
      { key: 'node-role', operator: 'Exists' },
    ])
    expect(ps.imagePullSecrets).toEqual([{ name: 'prod-extra' }])
    expect(ps.affinity).toEqual({ nodeAffinity: {} })
    // Removing the added toleration leaves the rest intact.
    await userEvent.click(
      screen.getByRole('button', { name: '删除容忍度 2' })
    )
    manifest = await dryRunManifest(screen)
    ps = podSpec(manifest)
    expect(ps.tolerations).toHaveLength(1)
    expect(ps.tolerations?.[0].key).toBe('dedicated')
  })

  it('selects ServiceAccount, PriorityClass and pull secrets from cluster lists', async () => {
    const { screen } = await renderEditor(deploymentAdvanced)
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    const serviceAccount = screen.getByRole('combobox', {
      name: '服务账号名称',
    })
    await expect.element(serviceAccount).toHaveTextContent('prod-data')
    await userEvent.selectOptions(serviceAccount, 'prod-extra')
    const priority = screen.getByRole('combobox', { name: '优先级类名' })
    await expect.element(priority).toHaveTextContent('prod-data')
    await userEvent.selectOptions(priority, 'prod-data')
    await userEvent.click(
      screen.getByRole('button', { name: '添加镜像拉取凭据' })
    )
    const pullSecret = screen.getByRole('combobox', {
      name: '镜像拉取凭据 1 名称',
    })
    await expect.element(pullSecret).toHaveTextContent('prod-extra')
    await userEvent.selectOptions(pullSecret, 'prod-data')
    // PriorityClasses are cluster-scoped: listed without a namespace.
    await vi.waitFor(() =>
      expect(mockResources).toHaveBeenCalledWith(
        expect.objectContaining({
          group: 'scheduling.k8s.io',
          resource: 'priorityclasses',
          namespace: undefined,
        })
      )
    )
    const manifest = await dryRunManifest(screen)
    const ps = podSpec(manifest)
    expect(ps.serviceAccountName).toBe('prod-extra')
    expect(ps.priorityClassName).toBe('prod-data')
    expect(ps.imagePullSecrets).toEqual([{ name: 'prod-data' }])
  })

  it('edits container resources preserving raw quantities and unknown keys', async () => {
    const { screen } = await renderEditor(deploymentWithResources)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    await expect
      .element(screen.getByRole('textbox', { name: 'CPU 请求' }))
      .toHaveValue('100m')
    await expect
      .element(screen.getByRole('textbox', { name: '内存限制' }))
      .toHaveValue('256Mi')
    // Rendered raw values are unchanged — no parseInt/suffix rewriting.
    await userEvent.fill(
      screen.getByRole('textbox', { name: 'CPU 请求' }),
      '0.5'
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '内存限制' }),
      '2Gi'
    )
    // Clearing a field removes only that quantity.
    await userEvent.fill(
      screen.getByRole('textbox', { name: '内存请求' }),
      ''
    )
    const manifest = await dryRunManifest(screen)
    const resources = podSpec(manifest).containers?.[0]?.resources
    expect(resources?.requests).toEqual({
      cpu: '0.5',
      'ephemeral-storage': '1Gi',
      'hugepages-2Mi': '80Mi',
    })
    expect(resources?.limits).toEqual({
      cpu: '500m',
      memory: '2Gi',
      'ephemeral-storage': '2Gi',
      'hugepages-2Mi': '80Mi',
    })
    expect(resources?.claims).toEqual([{ name: 'res-claim' }])
    expect(resources?.resizePolicy).toEqual([
      { resourceName: 'cpu', restartPolicy: 'NotRequired' },
    ])
  })

  it('adds an other resource only after explicit confirm with a unique name', async () => {
    const { screen } = await renderEditor(deploymentWithResources)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    await userEvent.click(
      screen.getByRole('button', { name: '添加其他资源' })
    )
    const nameInput = screen.getByRole('textbox', { name: '新资源名称' })
    const valueInput = screen.getByRole('textbox', { name: '新资源数量' })
    const confirm = screen.getByRole('button', { name: '确认添加' })
    // Blank name is rejected with text, nothing written.
    await userEvent.fill(valueInput, '1Gi')
    await userEvent.click(confirm)
    await expect
      .element(screen.getByText('请输入资源名称'))
      .toBeInTheDocument()
    // Blank value rejected.
    await userEvent.fill(nameInput, 'example.com/foo')
    await userEvent.fill(valueInput, '')
    await userEvent.click(confirm)
    await expect
      .element(screen.getByText('请输入资源数量'))
      .toBeInTheDocument()
    // Reserved names are rejected even when absent in the chosen kind.
    await userEvent.fill(nameInput, 'cpu')
    await userEvent.fill(valueInput, '3')
    await userEvent.click(confirm)
    await expect
      .element(screen.getByText(/专属输入/))
      .toBeInTheDocument()
    await userEvent.fill(nameInput, 'nvidia.com/gpu')
    await userEvent.click(confirm)
    await expect
      .element(screen.getByText(/专属输入/))
      .toBeInTheDocument()
    // Duplicate in the chosen kind rejected.
    await userEvent.fill(nameInput, 'ephemeral-storage')
    await userEvent.fill(valueInput, '3Gi')
    await userEvent.click(confirm)
    await expect
      .element(screen.getByText('请求中已存在该资源'))
      .toBeInTheDocument()
    // Explicit confirm commits once, never inventing key-N names.
    await userEvent.fill(nameInput, 'example.com/foo')
    await userEvent.click(confirm)
    const manifest = await dryRunManifest(screen)
    const resources = podSpec(manifest).containers?.[0]?.resources
    expect(resources?.requests?.['example.com/foo']).toBe('3Gi')
    expect(Object.keys(resources?.requests ?? {})).not.toContain('key-1')
    expect(resources?.limits?.['example.com/foo']).toBeUndefined()
  })

  it('switches GPU vendor preserving other keys and clears with none', async () => {
    const { screen } = await renderEditor(deploymentWithGpu)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const vendor = screen.getByRole('combobox', { name: 'GPU 类型' })
    await expect.element(vendor).toHaveValue('nvidia.com/gpu')
    await expect
      .element(screen.getByRole('spinbutton', { name: 'GPU 限制' }))
      .toHaveValue(2)
    // Editing the count mirrors requests because it already declares GPU.
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: 'GPU 限制' }),
      '4'
    )
    let manifest = await dryRunManifest(screen)
    let resources = podSpec(manifest).containers?.[0]?.resources
    expect(resources?.limits?.['nvidia.com/gpu']).toBe('4')
    expect(resources?.requests?.['nvidia.com/gpu']).toBe('4')
    // Explicit vendor switch renames only the recognized GPU keys.
    await userEvent.selectOptions(vendor, 'amd.com/gpu')
    manifest = await dryRunManifest(screen)
    resources = podSpec(manifest).containers?.[0]?.resources
    expect(resources?.limits).toEqual({ cpu: '500m', 'amd.com/gpu': '4' })
    expect(resources?.requests).toEqual({ cpu: '100m', 'amd.com/gpu': '4' })
    // none clears recognized GPU keys only; cpu survives.
    await userEvent.selectOptions(vendor, 'none')
    manifest = await dryRunManifest(screen)
    resources = podSpec(manifest).containers?.[0]?.resources
    expect(resources?.limits).toEqual({ cpu: '500m' })
    expect(resources?.requests).toEqual({ cpu: '100m' })
  })

  it('adds GPU limits from an empty resource map and clears fully on none', async () => {
    const { screen } = await renderEditor()
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const vendor = screen.getByRole('combobox', { name: 'GPU 类型' })
    await expect.element(vendor).toHaveValue('none')
    await userEvent.selectOptions(vendor, 'nvidia.com/gpu')
    let manifest = await dryRunManifest(screen)
    // Selecting a vendor installs limits only; requests stays absent.
    expect(podSpec(manifest).containers?.[0]?.resources).toEqual({
      limits: { 'nvidia.com/gpu': '1' },
    })
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: 'GPU 限制' }),
      '2'
    )
    manifest = await dryRunManifest(screen)
    expect(podSpec(manifest).containers?.[0]?.resources).toEqual({
      limits: { 'nvidia.com/gpu': '2' },
    })
    // none removes the only key. The manifest is back to its original
    // shape (save disabled), so an unrelated edit enables the dry-run
    // to prove `resources` was fully pruned.
    await userEvent.selectOptions(vendor, 'none')
    await userEvent.fill(
      screen.getByRole('textbox', { name: '容器镜像 app' }),
      'nginx:2'
    )
    manifest = await dryRunManifest(screen)
    const container = podSpec(manifest).containers?.[0]
    expect(container?.image).toBe('nginx:2')
    expect(container?.resources).toBeUndefined()
  })

  it('adds a GPU limit without touching a cpu-only requests map', async () => {
    const { screen } = await renderEditor(deploymentTwoContainers)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: 'GPU 类型' }),
      'nvidia.com/gpu'
    )
    const manifest = await dryRunManifest(screen)
    // seed had no GPU request, so requests keeps only cpu.
    expect(podSpec(manifest).initContainers?.[0]?.resources).toEqual({
      requests: { cpu: '50m' },
      limits: { cpu: '80m', 'nvidia.com/gpu': '1' },
    })
  })

  it('merges mixed GPU vendors only on an explicit vendor choice', async () => {
    const { screen } = await renderEditor(deploymentMixedGpu)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const vendor = screen.getByRole('combobox', { name: 'GPU 类型' })
    await expect.element(vendor).toHaveValue('mixed')
    await expect
      .element(screen.getByText(/合并为所选厂商/))
      .toBeInTheDocument()
    // Explicit choice merges both maps to the picked vendor, keeps cpu.
    await userEvent.selectOptions(vendor, 'nvidia.com/gpu')
    const manifest = await dryRunManifest(screen)
    const resources = podSpec(manifest).containers?.[0]?.resources
    expect(resources?.limits).toEqual({
      cpu: '500m',
      'nvidia.com/gpu': '2',
    })
    // requests already declared GPU, so the new vendor is mirrored there.
    expect(resources?.requests).toEqual({
      cpu: '100m',
      'nvidia.com/gpu': '2',
    })
  })

  it('keeps the GPU count visible while retyping and deletes on empty blur', async () => {
    const { screen } = await renderEditor(deploymentWithGpu)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const count = screen.getByRole('spinbutton', { name: 'GPU 限制' })
    await expect.element(count).toHaveValue(2)
    // Clearing mid-edit must not remove the field itself.
    await userEvent.fill(count, '')
    await expect.element(count).toBeInTheDocument()
    await userEvent.fill(count, '3')
    let manifest = await dryRunManifest(screen)
    let resources = podSpec(manifest).containers?.[0]?.resources
    expect(resources?.limits?.['nvidia.com/gpu']).toBe('3')
    expect(resources?.requests?.['nvidia.com/gpu']).toBe('3')
    // Invalid input shows inline text and never mutates the manifest.
    await userEvent.fill(count, '0')
    await expect
      .element(screen.getByText('请输入正整数卡数'))
      .toBeInTheDocument()
    await expect.element(count).toBeInTheDocument()
    // Empty + blur deletes the GPU from both maps.
    await userEvent.fill(count, '')
    await userEvent.tab()
    await expect
      .element(screen.getByRole('combobox', { name: 'GPU 类型' }))
      .toHaveValue('none')
    manifest = await dryRunManifest(screen)
    resources = podSpec(manifest).containers?.[0]?.resources
    expect(resources?.limits).toEqual({ cpu: '500m' })
    expect(resources?.requests).toEqual({ cpu: '100m' })
  })

  it('warns on unequal GPU request/limit counts and syncs on edit', async () => {
    const { screen } = await renderEditor(deploymentGpuMismatch)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    await expect
      .element(screen.getByText(/数量不一致/))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('spinbutton', { name: 'GPU 限制' }))
      .toHaveValue(2)
    // Editing the count mirrors the requests map back to equality.
    await userEvent.fill(
      screen.getByRole('spinbutton', { name: 'GPU 限制' }),
      '3'
    )
    const manifest = await dryRunManifest(screen)
    const resources = podSpec(manifest).containers?.[0]?.resources
    expect(resources?.limits?.['nvidia.com/gpu']).toBe('3')
    expect(resources?.requests?.['nvidia.com/gpu']).toBe('3')
  })

  it('keeps init and regular container resources independent', async () => {
    const { screen } = await renderEditor(deploymentTwoContainers)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    // Init container is selected first.
    await expect
      .element(screen.getByRole('textbox', { name: 'CPU 请求' }))
      .toHaveValue('50m')
    // Open the add draft, then switch: local state must reset.
    await userEvent.click(
      screen.getByRole('button', { name: '添加其他资源' })
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '新资源名称' }),
      'leaked.example/res'
    )
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    await expect
      .element(screen.getByRole('textbox', { name: 'CPU 请求' }))
      .toHaveValue('200m')
    await expect
      .element(screen.getByRole('textbox', { name: '新资源名称' }))
      .not.toBeInTheDocument()
    // Edit the regular container, then verify both sides in the payload.
    await userEvent.fill(
      screen.getByRole('textbox', { name: 'CPU 请求' }),
      '300m'
    )
    const manifest = await dryRunManifest(screen)
    expect(podSpec(manifest).initContainers?.[0]?.resources).toEqual({
      requests: { cpu: '50m' },
      limits: { cpu: '80m' },
    })
    expect(podSpec(manifest).containers?.[0]?.resources).toEqual({
      requests: { cpu: '300m' },
      limits: { cpu: '400m' },
    })
  })

  it('edits one env literal while preserving refs, duplicates and unknown fields', async () => {
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    await expect
      .element(screen.getByRole('textbox', { name: '环境变量 1 值' }))
      .toHaveValue('prod')
    await expect
      .element(
        screen.getByRole('combobox', { name: '环境变量 3 Secret 名称' })
      )
      .toHaveValue('app-secret')
    await expect
      .element(
        screen.getByRole('combobox', { name: '环境变量 5 字段路径' })
      )
      .toHaveValue('status.podIP')
    await expect
      .element(screen.getByRole('textbox', { name: '环境变量 6 除数' }))
      .toHaveValue('1m')
    await expect
      .element(screen.getByRole('textbox', { name: '批量导入 1 前缀' }))
      .toHaveValue('CONF_')
    await expect
      .element(screen.getByText(/valueFrom 类型，原样保留/).first())
      .toBeInTheDocument()
    await userEvent.fill(
      screen.getByRole('textbox', { name: '环境变量 1 值' }),
      'stage'
    )
    const manifest = await dryRunManifest(screen)
    const container = podSpec(manifest).containers?.[0]
    expect(container?.env).toEqual([
      { name: 'MODE', value: 'stage' },
      { name: 'MODE', value: 'dup' },
      {
        name: 'SECRET_VAL',
        valueFrom: {
          secretKeyRef: {
            name: 'app-secret',
            key: 'token',
            optional: false,
          },
        },
      },
      {
        name: 'CONF_VAL',
        valueFrom: {
          configMapKeyRef: { name: 'app-conf', key: 'app.yaml' },
        },
      },
      {
        name: 'POD_IP',
        customFlag: 'keep',
        valueFrom: {
          fieldRef: { apiVersion: 'v1', fieldPath: 'status.podIP' },
        },
      },
      {
        name: 'CPU_LIMIT',
        valueFrom: {
          resourceFieldRef: {
            resource: 'limits.cpu',
            divisor: '1m',
            containerName: 'app',
          },
        },
      },
      { name: 'ODD', extra: 'keep', valueFrom: { unknownRef: { a: 1 } } },
      { name: 'EMPTYVF', valueFrom: {} },
    ])
    expect(container?.envFrom).toEqual([
      {
        configMapRef: { name: 'bulk-conf', optional: true },
        prefix: 'CONF_',
      },
      { secretRef: { name: 'bulk-secret' } },
      {
        configMapRef: { name: 'dual-cm' },
        secretRef: { name: 'dual-sec' },
        prefix: 'D_',
      },
      { configMapRef: { name: 'odd-cm' }, extraProp: 'keep' },
    ])
  })

  it('keeps environment edits and pending additions when collapsed', async () => {
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    await userEvent.click(
      screen
        .getByRole('group', { name: '现有容器' })
        .getByRole('button', { name: /app/ })
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '环境变量 1 值', exact: true }),
      'updated'
    )
    await userEvent.click(
      screen.getByRole('button', { name: '收起单个环境变量' })
    )
    await expect
      .element(screen.getByRole('button', { name: '展开单个环境变量' }))
      .toHaveAttribute('aria-expanded', 'false')
    await expect
      .element(screen.getByLabelText('环境变量 1 值', { exact: true }))
      .not.toBeVisible()
    await expect
      .element(screen.getByRole('button', { name: '添加批量导入' }))
      .toBeVisible()
    await userEvent.click(
      screen.getByRole('button', { name: '添加环境变量', exact: true })
    )
    await expect
      .element(
        screen.getByRole('textbox', { name: '环境变量 1 值', exact: true })
      )
      .toHaveValue('updated')
    await userEvent.fill(
      screen.getByRole('textbox', { name: '新环境变量名称' }),
      'EXTRA'
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '新环境变量值' }),
      'draft'
    )
    await userEvent.click(
      screen.getByRole('button', { name: '收起单个环境变量' })
    )
    await userEvent.click(
      screen.getByRole('button', { name: '展开单个环境变量' })
    )
    await expect
      .element(screen.getByRole('textbox', { name: '新环境变量名称' }))
      .toHaveValue('EXTRA')
    await userEvent.click(
      screen.getByRole('button', { name: '确认添加环境变量' })
    )
    const manifest = await dryRunManifest(screen)
    const env = podSpec(manifest).containers?.[0]?.env ?? []
    expect(env[0].value).toBe('updated')
    expect(env[env.length - 1]).toEqual({ name: 'EXTRA', value: 'draft' })
  })

  it('merges ref edits without flattening siblings and switches type honestly', async () => {
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    // Editing the secret key keeps name + optional:false intact.
    await userEvent.fill(
      screen.getByRole('textbox', { name: '环境变量 3 Secret Key' }),
      'token2'
    )
    // Clearing divisor removes only that field.
    await userEvent.fill(
      screen.getByRole('textbox', { name: '环境变量 6 除数' }),
      ''
    )
    // Explicit type switch drops value/valueFrom but keeps name + unknown keys.
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '环境变量 5 来源' }),
      'secretKeyRef'
    )
    const manifest = await dryRunManifest(screen)
    const env = podSpec(manifest).containers?.[0]?.env ?? []
    expect(env[2].valueFrom).toEqual({
      secretKeyRef: { name: 'app-secret', key: 'token2', optional: false },
    })
    expect(env[5].valueFrom).toEqual({
      resourceFieldRef: { resource: 'limits.cpu', containerName: 'app' },
    })
    expect(env[4]).toEqual({
      name: 'POD_IP',
      customFlag: 'keep',
      valueFrom: { secretKeyRef: { name: '', key: '' } },
    })
  })

  it('adds env and envFrom through explicit drafts without key-1 names', async () => {
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    await userEvent.click(
      screen.getByRole('button', { name: '添加环境变量' })
    )
    const nameInput = screen.getByRole('textbox', {
      name: '新环境变量名称',
    })
    const confirmEnv = screen.getByRole('button', {
      name: '确认添加环境变量',
    })
    // '=' is rejected with visible text; dots are fine.
    await userEvent.fill(nameInput, 'BAD=NAME')
    await userEvent.click(confirmEnv)
    await expect
      .element(screen.getByText(/不能包含 = 或控制字符/))
      .toBeInTheDocument()
    // Existing duplicate name rejected for new entries.
    await userEvent.fill(nameInput, 'MODE')
    await userEvent.click(confirmEnv)
    await expect
      .element(screen.getByText('变量名已存在'))
      .toBeInTheDocument()
    // Blank literal value is a valid env var.
    await userEvent.fill(nameInput, 'NEW_VAR')
    await userEvent.click(confirmEnv)
    // Dotted names (newer Kubernetes env-name syntax) are accepted.
    await userEvent.click(
      screen.getByRole('button', { name: '添加环境变量' })
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '新环境变量名称' }),
      'LOG.LEVEL'
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '新环境变量值' }),
      'info'
    )
    await userEvent.click(
      screen.getByRole('button', { name: '确认添加环境变量' })
    )
    // envFrom draft: blank source name rejected, then committed.
    await userEvent.click(
      screen.getByRole('button', { name: '添加批量导入' })
    )
    await userEvent.click(
      screen.getByRole('button', { name: '确认添加批量导入' })
    )
    await expect
      .element(screen.getByText('请输入来源名称'))
      .toBeInTheDocument()
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '新批量导入名称' }),
      '__manual__'
    )
    // Manual entry keeps the datalist, so the role stays combobox.
    await userEvent.fill(
      screen.getByRole('combobox', { name: '新批量导入名称' }),
      'extra-conf'
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '新批量导入前缀' }),
      'X_'
    )
    await userEvent.click(
      screen.getByRole('button', { name: '确认添加批量导入' })
    )
    const manifest = await dryRunManifest(screen)
    const container = podSpec(manifest).containers?.[0]
    expect(container?.env?.[8]).toEqual({ name: 'NEW_VAR', value: '' })
    expect(container?.env?.[9]).toEqual({ name: 'LOG.LEVEL', value: 'info' })
    expect(container?.envFrom?.[4]).toEqual({
      configMapRef: { name: 'extra-conf' },
      prefix: 'X_',
    })
    expect(
      (container?.env ?? []).every(
        (item) => item.name !== 'key-1' && item.name !== ''
      )
    ).toBe(true)
  })

  it('switches envFrom type while keeping prefix, and resets drafts per container', async () => {
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    // Init container selected first; open a draft then switch to app.
    await expect
      .element(screen.getByRole('textbox', { name: '环境变量 1 名称' }))
      .toHaveValue('SEED_ONLY')
    await userEvent.click(
      screen.getByRole('button', { name: '添加环境变量' })
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '新环境变量名称' }),
      'LEAKED'
    )
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    await expect
      .element(screen.getByRole('textbox', { name: '新环境变量名称' }))
      .not.toBeInTheDocument()
    // Switching envFrom secretRef -> configMapRef keeps prefix.
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '批量导入 2 类型' }),
      'configMapRef'
    )
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '批量导入 2 名称' }),
      '__manual__'
    )
    await userEvent.fill(
      screen.getByRole('combobox', { name: '批量导入 2 名称' }),
      'renamed-conf'
    )
    const manifest = await dryRunManifest(screen)
    const envFrom = podSpec(manifest).containers?.[0]?.envFrom ?? []
    expect(envFrom[0]).toEqual({
      configMapRef: { name: 'bulk-conf', optional: true },
      prefix: 'CONF_',
    })
    expect(envFrom[1]).toEqual({
      configMapRef: { name: 'renamed-conf' },
    })
  })

  it('keeps explicit empty literal values and treats empty valueFrom as unknown', async () => {
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    // Clearing a literal keeps `value: ''` rather than deleting the key.
    await userEvent.fill(
      screen.getByRole('textbox', { name: '环境变量 1 值' }),
      ''
    )
    // EMPTYVF has valueFrom:{} — rendered honestly as unknown source.
    await expect
      .element(
        screen.getByRole('combobox', { name: '环境变量 8 来源' })
      )
      .toHaveValue('unknown')
    // Renaming keeps the unknown valueFrom object untouched.
    await userEvent.fill(
      screen.getByRole('textbox', { name: '环境变量 8 名称' }),
      'EMPTYVF2'
    )
    const manifest = await dryRunManifest(screen)
    const env = podSpec(manifest).containers?.[0]?.env ?? []
    expect(env[0]).toEqual({ name: 'MODE', value: '' })
    expect(env[7]).toEqual({ name: 'EMPTYVF2', valueFrom: {} })
  })

  it('renders mixed or odd envFrom sources as unknown and preserves them', async () => {
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    // Both refs set, or a ref plus unknown key -> honest unknown state.
    await expect
      .element(screen.getByRole('combobox', { name: '批量导入 3 类型' }))
      .toHaveValue('unknown')
    await expect
      .element(screen.getByRole('combobox', { name: '批量导入 4 类型' }))
      .toHaveValue('unknown')
    await expect
      .element(screen.getByText(/暂不支持的批量导入来源/).first())
      .toBeInTheDocument()
    // An unrelated env edit round-trips both rows unchanged.
    await userEvent.fill(
      screen.getByRole('textbox', { name: '环境变量 1 值' }),
      'stage'
    )
    const manifest = await dryRunManifest(screen)
    const envFrom = podSpec(manifest).containers?.[0]?.envFrom ?? []
    expect(envFrom[2]).toEqual({
      configMapRef: { name: 'dual-cm' },
      secretRef: { name: 'dual-sec' },
      prefix: 'D_',
    })
    expect(envFrom[3]).toEqual({
      configMapRef: { name: 'odd-cm' },
      extraProp: 'keep',
    })

    // Explicit switch replaces the source entirely: both stale refs and
    // unknown keys go away, prefix is retained, and the row is editable.
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '批量导入 3 类型' }),
      'secretRef'
    )
    await userEvent.selectOptions(
      screen.getByRole('combobox', { name: '批量导入 3 名称' }),
      '__manual__'
    )
    await userEvent.fill(
      screen.getByRole('combobox', { name: '批量导入 3 名称' }),
      'picked-secret'
    )
    const after = await dryRunManifest(screen)
    const envFromAfter = podSpec(after).containers?.[0]?.envFrom ?? []
    expect(envFromAfter[2]).toEqual({
      prefix: 'D_',
      secretRef: { name: 'picked-secret' },
    })
    expect(envFromAfter[0]).toEqual({
      configMapRef: { name: 'bulk-conf', optional: true },
      prefix: 'CONF_',
    })
    expect(envFromAfter[1]).toEqual({ secretRef: { name: 'bulk-secret' } })
    // The other unknown row is left alone by this row's switch.
    expect(envFromAfter[3]).toEqual({
      configMapRef: { name: 'odd-cm' },
      extraProp: 'keep',
    })
  })

  it('offers ConfigMap/Secret names and keys as dropdowns', async () => {
    mockResources.mockImplementation(
      async (params) =>
        ({
          data: {
            items:
              params.resource === 'configmaps'
                ? [
                    {
                      metadata: { name: 'app-conf' },
                      data: { 'app.yaml': 'a', 'extra.yaml': 'b' },
                    },
                    { metadata: { name: 'other-conf' } },
                  ]
                : params.resource === 'secrets'
                  ? [
                      {
                        metadata: { name: 'app-secret' },
                        data: { token: 'x' },
                      },
                      { metadata: { name: 'new-secret' }, data: { s2: 'x' } },
                    ]
                  : [],
          },
        }) as never
    )
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    // Secret names come from the namespace's Secret list; the stored
    // value stays visible even before it resolves.
    const secretName = screen.getByRole('combobox', {
      name: '环境变量 3 Secret 名称',
    })
    await expect.element(secretName).toHaveTextContent('new-secret')
    await userEvent.selectOptions(secretName, 'new-secret')
    // The old key is kept and visibly marked as not on the new object
    // until the user picks a replacement; its data keys become options.
    const secretKey = screen.getByRole('combobox', {
      name: '环境变量 3 Secret Key',
    })
    await expect.element(secretKey).toHaveValue('token')
    await expect.element(secretKey).toHaveTextContent('不在列表中')
    await expect.element(secretKey).toHaveTextContent('s2')
    await userEvent.selectOptions(secretKey, 's2')
    const confKey = screen.getByRole('combobox', {
      name: '环境变量 4 ConfigMap Key',
    })
    await expect.element(confKey).toHaveTextContent('extra.yaml')
    await userEvent.selectOptions(confKey, 'extra.yaml')
    const bulkName = screen.getByRole('combobox', {
      name: '批量导入 1 名称',
    })
    await expect.element(bulkName).toHaveTextContent('other-conf')
    await userEvent.selectOptions(bulkName, 'other-conf')
    const manifest = await dryRunManifest(screen)
    const container = podSpec(manifest).containers?.[0]
    expect(container?.env?.[2].valueFrom).toEqual({
      secretKeyRef: { name: 'new-secret', key: 's2', optional: false },
    })
    expect(container?.env?.[3].valueFrom).toEqual({
      configMapKeyRef: { name: 'app-conf', key: 'extra.yaml' },
    })
    expect(container?.envFrom?.[0]).toEqual({
      configMapRef: { name: 'other-conf', optional: true },
      prefix: 'CONF_',
    })
  })

  it('falls back to manual input when the resource list is unavailable', async () => {
    mockResources.mockRejectedValue(new Error('forbidden'))
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    const secretName = screen.getByRole('textbox', {
      name: '环境变量 3 Secret 名称',
    })
    await expect.element(secretName).toBeInTheDocument()
    await userEvent.fill(secretName, 'typed-secret')
    const manifest = await dryRunManifest(screen)
    expect(podSpec(manifest).containers?.[0]?.env?.[2].valueFrom).toEqual({
      secretKeyRef: { name: 'typed-secret', key: 'token', optional: false },
    })
  })

  it('edits CronJob env through the jobTemplate pod spec path', async () => {
    allowEditing({ g: 'batch', v: 'v1', r: 'cronjobs', kind: 'CronJob' })
    const { screen } = await renderEditor(cronJob, {
      g: 'batch',
      r: 'cronjobs',
      name: 'backup',
    })
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    await expect
      .element(screen.getByRole('textbox', { name: '环境变量 1 名称' }))
      .toHaveValue('CRON_MODE')
    await userEvent.fill(
      screen.getByRole('textbox', { name: '环境变量 1 值' }),
      'y'
    )
    const manifest = await dryRunManifest(screen)
    const spec = manifest.spec as Record<string, unknown>
    const jobTemplate = spec.jobTemplate as Record<string, unknown>
    const jobSpec = jobTemplate.spec as Record<string, unknown>
    const template = jobSpec.template as Record<string, unknown>
    const pod = template.spec as {
      containers?: { env?: Record<string, unknown>[] }[]
    }
    expect(pod.containers?.[0]?.env).toEqual([
      { name: 'CRON_MODE', value: 'y' },
    ])
  })

  it('disables env controls without update permission', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          'apps/v1/deployments': {
            list: true,
            update: false,
            delete: false,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderEditor(deploymentWithEnv)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    const group = screen.getByRole('group', { name: '现有容器' })
    await userEvent.click(group.getByRole('button', { name: /app/ }))
    await expect
      .element(screen.getByRole('textbox', { name: '环境变量 1 名称' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('combobox', { name: '环境变量 1 来源' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('button', { name: '添加环境变量' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('button', { name: '添加批量导入' }))
      .toBeDisabled()
  })

  it('disables container resource controls without update permission', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          'apps/v1/deployments': {
            list: true,
            update: false,
            delete: false,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderEditor(deploymentWithGpu)
    await userEvent.click(screen.getByRole('tab', { name: '容器信息' }))
    await expect
      .element(screen.getByRole('textbox', { name: 'CPU 请求' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('combobox', { name: 'GPU 类型' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('button', { name: '添加其他资源' }))
      .toBeDisabled()
  })

  it('disables the new advanced controls without update permission', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          'apps/v1/deployments': {
            list: true,
            update: false,
            delete: false,
          },
        },
        exec: false,
        logs: false,
      },
    } as never)
    const { screen } = await renderEditor()
    await userEvent.click(screen.getByRole('tab', { name: '高级设置' }))
    await expect
      .element(screen.getByRole('combobox', { name: '更新策略类型' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('spinbutton', { name: '副本数' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('combobox', { name: '自动挂载服务账号令牌' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('button', { name: '添加容忍度' }))
      .toBeDisabled()
  })

  it('shows the read-only service route port table', async () => {
    const { screen } = await renderEditor(deploymentWithPort)
    await userEvent.click(
      screen.getByRole('tab', { name: '服务 / 应用路由' })
    )
    await expect
      .element(screen.getByRole('cell', { name: 'http' }))
      .toBeInTheDocument()
    await expect
      .element(screen.getByRole('cell', { name: '80' }))
      .toBeInTheDocument()
  })

  it('shows the service route empty state without ports', async () => {
    const { screen } = await renderEditor()
    await userEvent.click(
      screen.getByRole('tab', { name: '服务 / 应用路由' })
    )
    await expect
      .element(screen.getByText('暂无容器端口，无法建立服务映射'))
      .toBeInTheDocument()
  })

  it('keeps the tab strip horizontally scrollable at mobile width', async () => {
    await page.viewport(390, 844)
    const { screen } = await renderEditor()
    const list = screen.getByRole('tablist')
    await expect.element(list).toHaveClass('overflow-x-auto')
    // KeyValueEditor rows stack: key input takes the full row on mobile.
    await expect
      .element(
        screen.getByRole('textbox', { name: '标签 键 1', exact: true })
      )
      .toHaveClass('w-full')
    const last = screen.getByRole('tab', { name: '服务 / 应用路由' })
    await userEvent.click(last)
    await expect.element(last).toHaveAttribute('data-state', 'active')
  })
})

describe('StorageTab', () => {
  beforeEach(mockApiDefaults)

  it('shows the Kuboard volume summary before opening its editor', async () => {
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await expect.element(screen.getByText('prod-data')).toBeInTheDocument()
    // Summary mounts render mode and path as separate elements.
    await expect.element(screen.getByText('读写').first()).toBeInTheDocument()
    await expect
      .element(screen.getByText('/var/lib/data'))
      .toBeInTheDocument()
    await editVolume(screen, 1)
    await expect
      .element(screen.getByRole('textbox', { name: '数据卷 1 名称' }))
      .toHaveValue('data')
    await userEvent.click(screen.getByRole('button', { name: '完成数据卷 1' }))
    await expect.element(screen.getByText('prod-data')).toBeInTheDocument()
  })

  it('renders existing volumes with per-source fields and preserves unknown nested fields after edit', async () => {
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await editVolume(screen, 1)
    await editVolume(screen, 2)
    await editVolume(screen, 4)

    // claimName is a real dropdown populated by the cluster's PVC list.
    const claim = screen.getByRole('combobox', { name: '数据卷 1 PVC 名称' })
    await expect.element(claim).toHaveValue('prod-data')
    await expect
      .element(screen.getByRole('checkbox', { name: '数据卷 1 PVC 只读' }))
      .toBeChecked()
    await vi.waitFor(() =>
      expect(mockResources).toHaveBeenCalledWith(
        expect.objectContaining({
          resource: 'persistentvolumeclaims',
          namespace: 'prod',
        })
      )
    )
    await expect.element(claim).toHaveTextContent('prod-extra')
    // Unknown source types are shown honestly instead of being rewritten.
    await expect
      .element(screen.getByText('当前：projected'))
      .toBeInTheDocument()

    const confName = screen.getByRole('combobox', {
      name: '数据卷 2 ConfigMap 名称',
    })
    await expect.element(confName).toHaveTextContent('prod-extra')
    await userEvent.selectOptions(confName, 'prod-extra')
    const manifest = await dryRunManifest(screen)
    const volumes = podSpec(manifest).volumes ?? []
    const conf = volumes.find((item) => item.name === 'conf')!
    const configMap = conf.configMap as Record<string, unknown>
    expect(configMap.name).toBe('prod-extra')
    expect(configMap.items).toEqual([{ key: 'app.yaml', path: 'app.yaml' }])
    expect(configMap.defaultMode).toBe(420)
    const data = volumes.find((item) => item.name === 'data')!
    expect(data.persistentVolumeClaim).toEqual({
      claimName: 'prod-data',
      readOnly: true,
    })
    const misc = volumes.find((item) => item.name === 'misc')!
    expect(misc.projected).toEqual({ sources: [] })
  })

  it('renames a volume and updates matching volumeMounts in init and regular containers', async () => {
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await editVolume(screen, 1)
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 1 名称' }),
      'renamed-data'
    )
    const manifest = await dryRunManifest(screen)
    const spec = podSpec(manifest)
    expect(spec.volumes?.[0]?.name).toBe('renamed-data')
    expect(spec.initContainers?.[0]?.volumeMounts?.[0]?.name).toBe(
      'renamed-data'
    )
    expect(spec.containers?.[0]?.volumeMounts?.[0]?.name).toBe('renamed-data')
    // Mounts of other volumes stay untouched.
    expect(spec.containers?.[0]?.volumeMounts?.[1]?.name).toBe('conf')
  })

  it('deleting a volume removes its mounts but keeps unrelated mounts', async () => {
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await userEvent.click(screen.getByRole('button', { name: '删除数据卷 1' }))
    const manifest = await dryRunManifest(screen)
    const spec = podSpec(manifest)
    expect((spec.volumes ?? []).map((item) => item.name)).toEqual([
      'conf',
      'scratch',
      'misc',
    ])
    expect(spec.initContainers?.[0]?.volumeMounts).toEqual([
      {
        name: 'conf',
        mountPath: '/seed-conf',
        subPathExpr: '$(POD_NAME)/conf.yaml',
      },
    ])
    expect(spec.containers?.[0]?.volumeMounts).toEqual([
      { name: 'conf', mountPath: '/etc/app', subPath: 'app.yaml' },
    ])
  })

  it('adds an NFS volume with per-container mounts through the dry-run payload', async () => {
    const { screen } = await renderEditor()
    await openStorageTab(screen)
    await expect.element(screen.getByText('暂无数据卷')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '添加数据卷' }))
    await userEvent.click(
      screen
        .getByRole('group', { name: '数据卷 1 来源类型' })
        .getByRole('button', { name: 'NFS' })
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 1 NFS 服务器' }),
      'nfs.local'
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 1 NFS 路径' }),
      '/exports'
    )
    const addMount = screen.getByRole('button', {
      name: '为容器 app 挂载数据卷 1',
    })
    await userEvent.click(addMount)
    // Kuboard allows the same volume to be mounted at several paths.
    await expect.element(addMount).toBeEnabled()
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 1 容器 app 挂载路径 1' }),
      '/data'
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 1 容器 app 子路径 1' }),
      'sub'
    )
    await userEvent.click(screen.getByRole('radio', { name: '只读' }))

    const manifest = await dryRunManifest(screen)
    const spec = podSpec(manifest)
    expect(spec.volumes).toEqual([
      { name: 'volume-1', nfs: { server: 'nfs.local', path: '/exports' } },
    ])
    expect(spec.containers?.[0]?.volumeMounts).toEqual([
      {
        name: 'volume-1',
        mountPath: '/data',
        subPath: 'sub',
        readOnly: true,
      },
    ])
  })

  it('allows multiple mounts of the same volume and maps variable subpaths to subPathExpr', async () => {
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await editVolume(screen, 1)
    await userEvent.click(
      screen.getByRole('button', { name: '为容器 app 挂载数据卷 1' })
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 1 容器 app 挂载路径 2' }),
      '/archive'
    )
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 1 容器 app 子路径 2' }),
      '$(POD_NAME)/archive'
    )
    await userEvent.click(
      screen
        .getByRole('radiogroup', { name: '数据卷 1 容器 app 挂载模式 2' })
        .getByRole('radio', { name: '只读' })
    )
    await userEvent.selectOptions(
      screen.getByRole('combobox', {
        name: '数据卷 1 容器 app 挂载传播 2',
      }),
      'HostToContainer'
    )
    const manifest = await dryRunManifest(screen)
    expect(podSpec(manifest).containers?.[0]?.volumeMounts).toContainEqual({
      name: 'data',
      mountPath: '/archive',
      subPathExpr: '$(POD_NAME)/archive',
      readOnly: true,
      mountPropagation: 'HostToContainer',
    })
    expect(
      podSpec(manifest).containers?.[0]?.volumeMounts?.filter(
        (mount) => mount.name === 'data'
      )
    ).toHaveLength(2)
  })

  it('edits ConfigMap KeyToPath while preserving defaultMode', async () => {
    mockResources.mockImplementation(
      async (params) =>
        ({
          data: {
            items:
              params.resource === 'configmaps'
                ? [
                    {
                      metadata: { name: 'app-conf' },
                      data: { 'app.yaml': 'content', 'extra.yaml': 'content' },
                    },
                  ]
                : [],
          },
        }) as never
    )
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await editVolume(screen, 2)
    await expect
      .element(screen.getByRole('combobox', { name: '数据卷 2 ConfigMap 名称' }))
      .toHaveValue('app-conf')
    await userEvent.click(
      screen.getByRole('button', { name: '数据卷 2 添加 KeyToPath' })
    )
    const keyField = screen.getByRole('combobox', {
      name: '数据卷 2 KeyToPath 2 Key',
    })
    // Keys of the selected ConfigMap are offered as dropdown options.
    await expect.element(keyField).toHaveTextContent('extra.yaml')
    await userEvent.selectOptions(keyField, 'extra.yaml')
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 2 KeyToPath 2 路径' }),
      'config/extra.yaml'
    )
    const manifest = await dryRunManifest(screen)
    const conf = (podSpec(manifest).volumes ?? []).find(
      (volume) => volume.name === 'conf'
    )!
    expect(conf.configMap).toEqual({
      name: 'app-conf',
      items: [
        { key: 'app.yaml', path: 'app.yaml' },
        { key: 'extra.yaml', path: 'config/extra.yaml' },
      ],
      defaultMode: 420,
    })
  })

  it('keeps an unknown volume source unchanged until a supported type is selected', async () => {
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await editVolume(screen, 3)
    await editVolume(screen, 4)

    // An unrelated edit does not disturb the unknown source.
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 3 名称' }),
      'scratch-2'
    )
    let manifest = await dryRunManifest(screen)
    let misc = (podSpec(manifest).volumes ?? []).find(
      (item) => item.name === 'misc'
    )!
    expect(misc.projected).toEqual({ sources: [] })

    await userEvent.click(
      screen
        .getByRole('group', { name: '数据卷 4 来源类型' })
        .getByRole('button', { name: '临时目录（EmptyDir）' })
    )
    manifest = await dryRunManifest(screen)
    misc = (podSpec(manifest).volumes ?? []).find(
      (item) => item.name === 'misc'
    )!
    expect(misc.projected).toBeUndefined()
    expect(misc.emptyDir).toEqual({})
    expect(misc.name).toBe('misc')
  })

  it('disables all storage controls when the workload is not editable', async () => {
    mockCaps.mockResolvedValue({
      data: {
        resources: {
          'apps/v1/deployments': { list: true, update: false, delete: false },
        },
        exec: true,
        logs: true,
      },
    } as never)
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await expect
      .element(screen.getByRole('button', { name: '添加数据卷' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('button', { name: '编辑数据卷 1' }))
      .toBeDisabled()
    await expect
      .element(screen.getByRole('button', { name: '删除数据卷 1' }))
      .toBeDisabled()
  })

  it('re-points mounts when the name is cleared then retyped', async () => {
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await editVolume(screen, 1)
    const nameInput = screen.getByRole('textbox', { name: '数据卷 1 名称' })
    await userEvent.fill(nameInput, '')
    await userEvent.fill(nameInput, 'renamed-data')
    const manifest = await dryRunManifest(screen)
    const spec = podSpec(manifest)
    expect(spec.volumes?.[0]?.name).toBe('renamed-data')
    expect(spec.initContainers?.[0]?.volumeMounts?.[0]?.name).toBe(
      'renamed-data'
    )
    expect(spec.containers?.[0]?.volumeMounts?.[0]?.name).toBe('renamed-data')
    expect(spec.containers?.[0]?.volumeMounts?.[1]?.name).toBe('conf')
  })

  it('picks the first available volume-N name when adding', async () => {
    const fixture: K8sObject = {
      ...deployment,
      spec: {
        ...deployment.spec,
        template: {
          metadata: { labels: { app: 'web' } },
          spec: {
            containers: [{ name: 'app', image: 'nginx:1' }],
            volumes: [
              { name: 'volume-1', emptyDir: {} },
              { name: 'volume-3', emptyDir: {} },
            ],
          },
        },
      },
    }
    const { screen } = await renderEditor(fixture)
    await openStorageTab(screen)
    await userEvent.click(screen.getByRole('button', { name: '添加数据卷' }))
    const manifest = await dryRunManifest(screen)
    expect((podSpec(manifest).volumes ?? []).map((item) => item.name)).toEqual([
      'volume-1',
      'volume-3',
      'volume-2',
    ])
  })

  it('clears a stale subPathExpr when a literal subPath is typed', async () => {
    const { screen } = await renderEditor(deploymentWithVolumes)
    await openStorageTab(screen)
    await editVolume(screen, 2)
    await userEvent.fill(
      screen.getByRole('textbox', { name: '数据卷 2 容器 seed 子路径 1' }),
      'flat.yaml'
    )
    const manifest = await dryRunManifest(screen)
    const mounts = podSpec(manifest).initContainers?.[0]?.volumeMounts ?? []
    const mount = mounts.find((item) => item.name === 'conf')!
    expect(mount.subPath).toBe('flat.yaml')
    expect(mount.subPathExpr).toBeUndefined()
    // The untouched data mount keeps its own fields.
    const data = mounts.find((item) => item.name === 'data')!
    expect(data.readOnly).toBe(true)
  })
})
