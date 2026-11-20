import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Download, RefreshCw, Workflow, X } from 'lucide-react'
import { toast } from 'sonner'
import {
  runnerApi,
  type ComponentBinding,
  type PipelinePreview,
  type RunnerBinding,
  type RunnerEntry,
  type RunnerScripts,
} from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

const selectClass =
  'h-11 w-full rounded-md border border-input bg-background px-3 text-sm focus-visible:outline-2 focus-visible:outline-ring'
const errorDetail = (e: unknown) =>
  (e as { response?: { data?: { detail?: string } } })?.response?.data
    ?.detail ?? '操作失败，请检查配置与连接后重试。'
function download(name: string, text: string) {
  const url = URL.createObjectURL(
    new Blob([text], { type: 'text/plain;charset=utf-8' })
  )
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}
async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success('已复制')
  } catch {
    toast.error('复制失败，请手动选择内容')
  }
}
function Field({
  label,
  id,
  hint,
  children,
}: {
  label: string
  id: string
  hint?: string
  children: React.ReactNode
}) {
  return (
    <div className='space-y-2'>
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint && <p className='text-xs text-muted-foreground'>{hint}</p>}
    </div>
  )
}

export function RunnerOnboarding({
  app,
  initial,
  onClose,
}: {
  app: ComponentBinding
  initial?: RunnerEntry
  onClose: () => void
}) {
  const client = useQueryClient()
  const isAdmin = useAuthStore((s) => s.auth.user?.role) === 'admin'
  const [step, setStep] = useState(initial ? 1 : 0)
  const [binding, setBinding] = useState<RunnerBinding | undefined>(
    initial?.binding
  )
  const [form, setForm] = useState({
    pipeline_id: initial?.binding.pipeline_id ?? '',
    private_cluster_id: initial?.binding.private_cluster_id ?? '',
    platform_url: initial?.binding.platform_url ?? window.location.origin,
    connection_id: initial?.binding.connection_id ?? 0,
  })
  const [pipelineSearchText, setPipelineSearchText] = useState('')
  const [pipelineSearchName, setPipelineSearchName] = useState('')
  const [pipelineSearchPage, setPipelineSearchPage] = useState(1)
  const [key, setKey] = useState('')
  const [generated, setGenerated] = useState<RunnerScripts>()
  const [error, setError] = useState('')
  const [preview, setPreview] = useState<PipelinePreview>()
  const [confirmed, setConfirmed] = useState(false)
  const [connectionForm, setConnectionForm] = useState({
    name: '',
    edition: 'central' as 'central' | 'region',
    domain: 'openapi-rdc.aliyuncs.com',
    organization_id: '',
    token: '',
  })
  const entries = useQuery({
    queryKey: ['runner-bindings'],
    queryFn: () => runnerApi.bindings(),
    refetchInterval: binding ? 5000 : false,
  })
  const current =
    entries.data?.data.find((e) => e.binding.id === binding?.id) ?? initial
  const scripts = useQuery({
    queryKey: ['runner-scripts', binding?.id, binding?.updated_at],
    queryFn: () => runnerApi.scripts(binding!.id),
    enabled: !!binding && !generated,
    retry: false,
  })
  const code = generated ?? scripts.data?.data
  const connections = useQuery({
    queryKey: ['runner-connections'],
    queryFn: () => runnerApi.connections(),
    enabled: isAdmin,
  })
  const pipelines = useQuery({
    queryKey: [
      'yunxiao-runner-pipelines',
      form.connection_id,
      pipelineSearchName,
      pipelineSearchPage,
    ],
    queryFn: async () =>
      (
        await runnerApi.searchDeploymentPipelines({
          connection_id: form.connection_id,
          name: pipelineSearchName,
          page: pipelineSearchPage,
        })
      ).data,
    enabled: isAdmin && form.connection_id > 0 && pipelineSearchName.length > 0,
    retry: false,
  })
  const configure = useMutation({
    mutationFn: () =>
      runnerApi.configure({ ...form, app_id: app.id }, binding?.id),
    onSuccess: ({ data }) => {
      setBinding(data.binding)
      setKey(data.key)
      setGenerated(data.scripts)
      setPreview(undefined)
      setConfirmed(false)
      setError('')
      setStep(1)
      void client.invalidateQueries({ queryKey: ['runner-bindings'] })
    },
    onError: (e) => setError(errorDetail(e)),
  })
  const toggle = useMutation({
    mutationFn: (enabled: boolean) => runnerApi.enabled(binding!.id, enabled),
    onSuccess: ({ data }) => {
      setBinding(data)
      void client.invalidateQueries({ queryKey: ['runner-bindings'] })
    },
    onError: (e) => setError(errorDetail(e)),
  })
  const connect = useMutation({
    mutationFn: () => runnerApi.createConnection(connectionForm),
    onSuccess: ({ data }) => {
      setForm((f) => ({ ...f, connection_id: data.id }))
      setConnectionForm((f) => ({ ...f, token: '' }))
      setError('')
      void client.invalidateQueries({ queryKey: ['runner-connections'] })
      toast.success('云效连接已保存，请保存接入配置后预览流水线')
    },
    onError: (e) => setError(errorDetail(e)),
  })
  const cloudPreview = useMutation({
    mutationFn: () => runnerApi.preview(binding!.id),
    onSuccess: ({ data }) => {
      setPreview(data)
      setConfirmed(false)
      setError('')
    },
    onError: (e) => {
      setPreview(undefined)
      setError(errorDetail(e))
    },
  })
  const cloudApply = useMutation({
    mutationFn: () =>
      runnerApi.apply(binding!.id, {
        fingerprint: preview!.fingerprint,
        change_hash: preview!.change_hash,
      }),
    onSuccess: () => {
      setPreview(undefined)
      setConfirmed(false)
      void client.invalidateQueries({ queryKey: ['runner-bindings'] })
      toast.success('通知任务已保存到云效；请确认私密变量与镜像输出已配置')
    },
    onError: (e) => {
      setPreview(undefined)
      setConfirmed(false)
      setError(errorDetail(e))
    },
  })
  const busy =
    configure.isPending ||
    connect.isPending ||
    cloudPreview.isPending ||
    cloudApply.isPending ||
    toggle.isPending
  const scrollContent = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (scrollContent.current) scrollContent.current.scrollTop = 0
  }, [step])
  const stale = current?.configuration_error
  const missingConfig = [
    !form.pipeline_id && '云效流水线 ID',
    !form.private_cluster_id && '私有构建集群 ID',
    !form.platform_url && '平台地址',
  ].filter(Boolean)
  const selectedBuildPipelineName = pipelines.data?.items.find(
    (pipeline) => String(pipeline.pipeline_id) === form.pipeline_id
  )?.name
  const selectedBuildConnectionName = connections.data?.data.find(
    (connection) => connection.id === form.connection_id
  )?.name
  const configureLabel = binding ? '更新配置并生成新密钥' : '生成组件接入配置'
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
    >
      <DialogContent className='grid max-h-[min(90dvh,calc(100%-2rem))] min-h-0 grid-rows-[auto_auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:max-w-4xl'>
        <DialogHeader className='px-6 pt-6 pb-4'>
          <DialogTitle>接入云效 Runner · {app.app_name}</DialogTitle>
          <DialogDescription>
            配置后，Runner 在镜像推送成功时通知平台更新此组件。
          </DialogDescription>
        </DialogHeader>
        <div className='grid grid-cols-3 gap-2 border-b px-6 pb-4 text-sm'>
          {[
            { title: '接入配置', compact: '配置' },
            { title: '流水线任务', compact: '流水线' },
            { title: '检查与验证', compact: '验证' },
          ].map(({ title, compact }, i) => (
            <button
              key={title}
              type='button'
              aria-label={`${i + 1}. ${title}`}
              aria-current={i === step ? 'step' : undefined}
              disabled={busy || (i > 0 && !binding)}
              onClick={() => {
                setStep(i)
                setError('')
              }}
              className={`min-h-11 rounded-md px-2 text-center leading-4 whitespace-nowrap focus-visible:outline-2 focus-visible:outline-ring sm:text-left ${i === step ? 'bg-primary/10 font-semibold text-primary' : 'text-muted-foreground'}`}
            >
              <span className='sm:hidden'>
                {i + 1}. {compact}
              </span>
              <span className='hidden sm:inline'>
                {i + 1}. {title}
              </span>
            </button>
          ))}
        </div>
        <div
          ref={scrollContent}
          data-testid='runner-onboarding-scroll-content'
          className='min-h-0 space-y-5 overflow-y-auto overscroll-contain px-6 py-5'
        >
          <div className='rounded-lg border bg-card p-4'>
            <div className='grid gap-4 sm:grid-cols-2'>
              <div className='min-w-0 space-y-1'>
                <p className='text-xs font-medium text-muted-foreground'>
                  此流水线必须产出的镜像仓库
                </p>
                <p className='font-mono text-sm font-medium break-all'>
                  {app.image_repository}
                </p>
                <p className='text-xs leading-5 text-muted-foreground'>
                  平台按完整仓库路径精确匹配，不猜测代码仓库和镜像的关系。
                </p>
              </div>
              <div className='min-w-0 space-y-1 sm:border-l sm:pl-4'>
                <p className='text-xs font-medium text-muted-foreground'>
                  镜像成功后唯一更新的容器
                </p>
                <p className='font-semibold'>{app.app_name}</p>
                <p className='font-mono text-sm break-all'>
                  {app.namespace}/{app.deployment_name}:{app.container_name}
                </p>
                <p className='text-xs leading-5 text-muted-foreground'>
                  集群 #{app.cluster_id}
                  {binding?.env_code ? ` · ${binding.env_code} 环境` : ''}
                  {' · '}不需要传 APP_NAME 或 Kubernetes 资源名。
                </p>
              </div>
            </div>
            <p className='mt-3 border-t pt-3 text-xs text-muted-foreground'>
              专属授权只允许更新这一个非生产目标；生产仍由平台审批和发布计划管理。
            </p>
            {binding && (
              <p className='mt-2 text-xs text-muted-foreground'>
                CI / Smoke / Scan 上报来源：
                <code className='rounded bg-background px-1.5 py-0.5 font-mono text-foreground'>
                  runner:{binding.id}
                </code>
              </p>
            )}
            {app.deployment_config_repo_url && (
              <p className='mt-2 text-xs leading-5 text-muted-foreground'>
                组件部署配置：
                <code className='font-mono break-all text-foreground'>
                  {app.deployment_config_repo_url}
                </code>{' '}
                / {app.deployment_config_path || '.'}
              </p>
            )}
          </div>
          {stale && (
            <p role='alert' className='text-sm text-destructive'>
              {stale}
            </p>
          )}
          {step === 0 && (
            <fieldset disabled={busy} className='space-y-5'>
              {isAdmin && (
                <Field
                  label='云效连接（可选，自动写入流水线时使用）'
                  id='runner-connection'
                  hint='选择已有连接后，可按名称查找构建流水线；没有连接时手工填写 ID 并复制任务配置。'
                >
                  <select
                    id='runner-connection'
                    className={selectClass}
                    value={form.connection_id}
                    onChange={(e) => {
                      setForm((f) => ({
                        ...f,
                        connection_id: Number(e.target.value),
                        pipeline_id: '',
                      }))
                      setPipelineSearchName('')
                      setPipelineSearchPage(1)
                    }}
                  >
                    <option value={0}>手工接入：复制任务配置</option>
                    {connections.data?.data.map((c) => (
                      <option key={c.id} value={c.id}>
                        {c.name} · {c.edition}
                      </option>
                    ))}
                  </select>
                </Field>
              )}
              {isAdmin && form.connection_id > 0 && (
                <div className='space-y-2'>
                  <Label htmlFor='runner-pipeline-search'>
                    按名称查找构建流水线
                  </Label>
                  <div className='flex flex-col gap-2 sm:flex-row'>
                    <Input
                      id='runner-pipeline-search'
                      maxLength={100}
                      placeholder='例如：orders-build'
                      value={pipelineSearchText}
                      onChange={(e) => setPipelineSearchText(e.target.value)}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter' && pipelineSearchText.trim()) {
                          e.preventDefault()
                          setPipelineSearchPage(1)
                          setPipelineSearchName(pipelineSearchText.trim())
                        }
                      }}
                    />
                    <Button
                      type='button'
                      variant='outline'
                      className='min-h-11 shrink-0'
                      disabled={
                        !pipelineSearchText.trim() || pipelines.isFetching
                      }
                      onClick={() => {
                        setPipelineSearchPage(1)
                        setPipelineSearchName(pipelineSearchText.trim())
                      }}
                    >
                      <Workflow
                        aria-hidden='true'
                        className={`size-4 ${pipelines.isFetching ? 'animate-pulse' : ''}`}
                      />
                      {pipelines.isFetching ? '正在搜索…' : '搜索流水线'}
                    </Button>
                  </div>
                  <p className='text-xs leading-5 text-muted-foreground'>
                    只读取流水线名称和 ID，不会修改或运行流水线。
                  </p>
                  {pipelines.isError && (
                    <p role='alert' className='text-xs text-destructive'>
                      {errorDetail(pipelines.error)}
                    </p>
                  )}
                  {pipelineSearchName && pipelines.data && (
                    <div className='space-y-2'>
                      <Label htmlFor='runner-pipeline-result'>
                        搜索结果 · 第 {pipelines.data.page} 页
                        {pipelines.data.total > 0 &&
                          ` / 共 ${pipelines.data.total} 条`}
                      </Label>
                      {pipelines.data.items.length > 0 ? (
                        <select
                          id='runner-pipeline-result'
                          className={selectClass}
                          value={form.pipeline_id}
                          onChange={(e) =>
                            setForm((f) => ({
                              ...f,
                              pipeline_id: e.target.value,
                            }))
                          }
                        >
                          <option value=''>选择一个构建流水线</option>
                          {pipelines.data.items.map((pipeline) => (
                            <option
                              key={pipeline.pipeline_id}
                              value={String(pipeline.pipeline_id)}
                            >
                              {pipeline.name} · ID {pipeline.pipeline_id}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <p className='rounded-md border border-dashed px-3 py-2 text-sm text-muted-foreground'>
                          没有找到匹配流水线，请换一个名称关键词。
                        </p>
                      )}
                      <div className='flex items-center justify-between gap-3'>
                        <p className='min-w-0 text-xs text-muted-foreground'>
                          {form.pipeline_id
                            ? `当前流水线 ID：${form.pipeline_id}`
                            : '选择结果后会自动填入流水线 ID。'}
                        </p>
                        <div className='flex shrink-0 gap-2'>
                          <Button
                            type='button'
                            variant='ghost'
                            size='sm'
                            disabled={
                              pipelines.data.page <= 1 || pipelines.isFetching
                            }
                            onClick={() =>
                              setPipelineSearchPage((page) => page - 1)
                            }
                          >
                            上一页
                          </Button>
                          <Button
                            type='button'
                            variant='ghost'
                            size='sm'
                            disabled={
                              pipelines.data.page >=
                                pipelines.data.total_pages ||
                              pipelines.isFetching
                            }
                            onClick={() =>
                              setPipelineSearchPage((page) => page + 1)
                            }
                          >
                            下一页
                          </Button>
                        </div>
                      </div>
                    </div>
                  )}
                </div>
              )}
              {form.pipeline_id && (
                <div
                  role='region'
                  aria-label='Runner 绑定预览'
                  className='rounded-lg border bg-muted/30 p-4'
                >
                  <p className='text-xs font-medium text-muted-foreground'>
                    接入关系预览
                  </p>
                  <div className='mt-2 grid gap-3 sm:grid-cols-2'>
                    <div className='min-w-0 space-y-1'>
                      <p className='text-xs text-muted-foreground'>
                        Yunxiao 构建流水线
                      </p>
                      <p className='text-sm font-medium break-words'>
                        {selectedBuildConnectionName
                          ? `${selectedBuildConnectionName} · `
                          : '手工接入 · '}
                        {selectedBuildPipelineName ??
                          `流水线 ID ${form.pipeline_id}`}
                      </p>
                    </div>
                    <div className='min-w-0 space-y-1 sm:border-l sm:pl-3'>
                      <p className='text-xs text-muted-foreground'>
                        通知后唯一更新的 CI 组件与 Kubernetes 目标
                      </p>
                      <p className='text-sm font-medium'>
                        {app.app_name}{' '}
                        <span
                          aria-hidden='true'
                          className='text-muted-foreground'
                        >
                          →
                        </span>
                      </p>
                      <p className='font-mono text-xs break-all'>
                        {app.namespace}/{app.deployment_name} /{' '}
                        {app.container_name}
                      </p>
                    </div>
                  </div>
                  <div className='mt-3 space-y-1 border-t pt-3 text-xs leading-5'>
                    <p className='break-all'>
                      <span className='text-muted-foreground'>代码来源：</span>
                      {app.source_repo_url
                        ? `${app.source_repo_url} / ${app.source_path || '.'}`
                        : app.source_kind === 'third_party'
                          ? '第三方镜像'
                          : '待确认'}
                    </p>
                    <p className='break-all'>
                      <span className='text-muted-foreground'>镜像仓库：</span>
                      {app.image_repository}
                    </p>
                    <p className='text-muted-foreground'>
                      Runner
                      按专属授权固定该组件；上报镜像必须属于登记仓库，仓库或流水线不匹配时会被拒绝。
                    </p>
                  </div>
                </div>
              )}
              <div className='grid gap-5 sm:grid-cols-2'>
                <Field
                  label='云效流水线 ID'
                  id='runner-pipeline'
                  hint='可由上方搜索结果自动填入，也可手工填写流水线页面地址中的数字 ID。'
                >
                  <Input
                    id='runner-pipeline'
                    value={form.pipeline_id}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, pipeline_id: e.target.value }))
                    }
                  />
                </Field>
                <Field
                  label='私有构建集群 ID'
                  id='runner-private'
                  hint='在云效构建集群管理中查看，让通知任务运行在内网 Runner。'
                >
                  <Input
                    id='runner-private'
                    value={form.private_cluster_id}
                    onChange={(e) =>
                      setForm((f) => ({
                        ...f,
                        private_cluster_id: e.target.value,
                      }))
                    }
                  />
                </Field>
              </div>
              <Field
                label='Runner 能访问的平台地址'
                id='runner-url'
                hint='填写根地址。应从流水线实际任务环境验证；Runner 在其他机器时不能填 localhost。'
              >
                <Input
                  id='runner-url'
                  placeholder='https://deploy.internal.example.com'
                  value={form.platform_url}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, platform_url: e.target.value }))
                  }
                />
              </Field>
              {isAdmin && (
                <>
                  <details className='rounded-lg border p-4'>
                    <summary className='cursor-pointer text-sm font-medium'>
                      添加云效连接
                    </summary>
                    <div className='mt-4 space-y-4'>
                      <Field label='连接名称' id='cloud-name'>
                        <Input
                          id='cloud-name'
                          value={connectionForm.name}
                          onChange={(e) =>
                            setConnectionForm((f) => ({
                              ...f,
                              name: e.target.value,
                            }))
                          }
                        />
                      </Field>
                      <Field label='云效版本' id='cloud-edition'>
                        <select
                          id='cloud-edition'
                          className={selectClass}
                          value={connectionForm.edition}
                          onChange={(e) =>
                            setConnectionForm((f) => ({
                              ...f,
                              edition: e.target.value as 'central' | 'region',
                              domain:
                                e.target.value === 'central'
                                  ? 'openapi-rdc.aliyuncs.com'
                                  : '',
                            }))
                          }
                        >
                          <option value='central'>中心版</option>
                          <option value='region'>Region 版</option>
                        </select>
                      </Field>
                      <Field label='云效 API 接入域名' id='cloud-domain'>
                        <Input
                          id='cloud-domain'
                          value={connectionForm.domain}
                          onChange={(e) =>
                            setConnectionForm((f) => ({
                              ...f,
                              domain: e.target.value,
                            }))
                          }
                        />
                      </Field>
                      {connectionForm.edition === 'central' && (
                        <Field label='云效组织 ID' id='cloud-org'>
                          <Input
                            id='cloud-org'
                            value={connectionForm.organization_id}
                            onChange={(e) =>
                              setConnectionForm((f) => ({
                                ...f,
                                organization_id: e.target.value,
                              }))
                            }
                          />
                        </Field>
                      )}
                      <Field
                        label='云效个人访问令牌'
                        id='cloud-token'
                        hint='读取和保存流水线需要对应权限；平台加密保存，不回显令牌。'
                      >
                        <Input
                          id='cloud-token'
                          type='password'
                          autoComplete='off'
                          value={connectionForm.token}
                          onChange={(e) =>
                            setConnectionForm((f) => ({
                              ...f,
                              token: e.target.value,
                            }))
                          }
                        />
                      </Field>
                      <Button
                        variant='outline'
                        onClick={() => connect.mutate()}
                        disabled={!connectionForm.name || !connectionForm.token}
                      >
                        保存云效连接
                      </Button>
                    </div>
                  </details>
                </>
              )}
              {binding && (
                <p className='text-sm text-muted-foreground'>
                  保存接入配置会重新生成密钥，旧密钥立即失效。请同步更新云效私密变量。
                </p>
              )}
            </fieldset>
          )}
          {step === 1 && binding && (
            <div className='space-y-5'>
              <div className='rounded-lg border p-4'>
                <p className='text-sm font-semibold'>
                  1. 在云效配置组件专属私密变量
                </p>
                <p className='mt-2 font-mono text-xs'>
                  {code?.secret_variable}
                </p>
                {key ? (
                  <>
                    <div className='mt-3 flex gap-2'>
                      <Input
                        aria-label='组件专属接入密钥'
                        type='password'
                        readOnly
                        value={key}
                      />
                      <Button variant='outline' onClick={() => copy(key)}>
                        <Copy />
                        复制密钥
                      </Button>
                    </div>
                    <p className='mt-2 text-xs text-muted-foreground'>
                      密钥仅此时显示，关闭后无法找回；平台只保存哈希。有效期至{' '}
                      {new Date(binding.expires_at).toLocaleDateString()}。
                    </p>
                  </>
                ) : (
                  <p className='mt-3 text-sm text-muted-foreground'>
                    已有密钥不会回显。遗失时返回第一步重新生成。
                  </p>
                )}
              </div>
              <div className='rounded-lg border p-4'>
                <p className='text-sm font-semibold'>
                  2. 传入同次构建的镜像产物
                </p>
                <p className='mt-2 text-sm text-muted-foreground'>
                  DEPLOY_IMAGE_FULL 为完整镜像地址，DEPLOY_IMAGE_DIGEST
                  为推送后的 sha256 digest。通知任务需要 Bash、Python 3 和支持
                  --fail-with-body / --retry-all-errors 的 curl。
                </p>
                <p className='mt-2 text-sm text-muted-foreground'>
                  跨任务请显式传递步骤输出或构建产物清单；不要假设共享工作目录。不要填写固定
                  latest 版本。
                </p>
                {code && (
                  <p className='mt-2 text-sm text-muted-foreground'>
                    {app.deployment_config_repo_url
                      ? '组件已固定配置仓库和目录。若流水线把配置仓库检出在当前目录之外，可选设置：'
                      : '如构建使用独立 Helm/Git 配置仓库，可选设置 '}
                    {code.configuration_variables.map((name, index) => (
                      <span key={name}>
                        {index > 0 ? '、' : ''}
                        <code className='rounded bg-muted px-1 py-0.5 font-mono text-foreground'>
                          {name}
                        </code>
                      </span>
                    ))}
                    {app.deployment_config_repo_url
                      ? '默认使用通知任务当前目录。脚本会核对 Git origin、登记目录和实际 HEAD，要求登记目录干净，再自动把 commit 固定到本次制品；若仍传入旧的 DEPLOY_CONFIG_REVISION，也会要求它与 HEAD 相同。'
                      : '变量值需提供配置仓库地址、完整 40 或 64 位 commit SHA 和仓库内目录；仓库与 revision 同时必填。组件登记配置仓库后，脚本会固定仓库和目录。'}{' '}
                    平台不会读取配置内容、渲染 Helm 或证明配置已应用。
                  </p>
                )}
                {code && (
                  <p className='mt-2 text-xs text-muted-foreground'>
                    通知任务必填变量：
                    {code.required_variables.map((name, index) => (
                      <span key={name}>
                        {index > 0 ? '、' : ' '}
                        <code className='rounded bg-muted px-1 py-0.5 font-mono text-foreground'>
                          {name}
                        </code>
                      </span>
                    ))}
                    。
                  </p>
                )}
              </div>
              {code ? (
                <>
                  <div className='space-y-3'>
                    <p className='text-sm font-semibold'>
                      3. 放在镜像推送成功后的通知任务
                    </p>
                    <Textarea
                      aria-label='云效通知任务配置'
                      readOnly
                      value={code.task_yaml}
                      className='min-h-52 font-mono text-xs'
                    />
                    <div className='flex flex-wrap gap-2'>
                      <Button
                        variant='outline'
                        onClick={() => copy(code.task_yaml)}
                      >
                        <Copy />
                        复制任务配置
                      </Button>
                      <Button
                        variant='outline'
                        onClick={() =>
                          download(`notify-${app.app_name}.sh`, code.script)
                        }
                      >
                        <Download />
                        下载通知脚本
                      </Button>
                    </div>
                  </div>
                </>
              ) : (
                <p role={scripts.isError ? 'alert' : undefined}>
                  {scripts.isError
                    ? errorDetail(scripts.error)
                    : '正在生成脚本…'}
                </p>
              )}
              {isAdmin && binding.connection_id > 0 && (
                <div className='space-y-3 rounded-lg border p-4'>
                  <p className='text-sm font-semibold'>
                    自动写入云效 YAML 流水线
                  </p>
                  <p className='text-xs text-muted-foreground'>
                    预览会保留已有阶段，在末尾增加通知阶段。非 YAML
                    或无法安全识别的配置请手工合并。
                  </p>
                  <Button
                    variant='outline'
                    onClick={() => cloudPreview.mutate()}
                    disabled={busy || !!stale}
                  >
                    {cloudPreview.isPending
                      ? '读取中…'
                      : '读取并预览流水线变更'}
                  </Button>
                  {preview && (
                    <>
                      <p className='text-sm'>
                        {preview.name} ·{' '}
                        {preview.already_installed
                          ? '通知任务已经存在'
                          : '将新增通知任务'}
                      </p>
                      <div className='grid gap-3 sm:grid-cols-2'>
                        <Field label='现有流水线' id='pipeline-before'>
                          <Textarea
                            id='pipeline-before'
                            readOnly
                            value={preview.before}
                            className='min-h-48 font-mono text-xs'
                          />
                        </Field>
                        <Field label='保存后的流水线' id='pipeline-after'>
                          <Textarea
                            id='pipeline-after'
                            readOnly
                            value={preview.after}
                            className='min-h-48 font-mono text-xs'
                          />
                        </Field>
                      </div>
                      <p className='text-xs text-muted-foreground'>
                        {preview.note}
                      </p>
                      {!preview.already_installed && (
                        <>
                          <Label className='flex items-start gap-2 text-sm'>
                            <input
                              type='checkbox'
                              checked={confirmed}
                              onChange={(e) => setConfirmed(e.target.checked)}
                            />
                            我已核对变更，并配置所需私密变量和本次镜像输出
                          </Label>
                          <Button
                            onClick={() => cloudApply.mutate()}
                            disabled={!confirmed || busy}
                          >
                            {cloudApply.isPending
                              ? '保存中…'
                              : '保存通知任务到云效'}
                          </Button>
                        </>
                      )}
                    </>
                  )}
                </div>
              )}
              <section
                aria-labelledby='runner-verification-title'
                className='space-y-3 rounded-lg bg-muted/60 p-4'
              >
                <div>
                  <h3
                    id='runner-verification-title'
                    className='text-sm font-semibold'
                  >
                    CI / Smoke / Scan 证据上报
                  </h3>
                  <p className='mt-1 text-sm text-muted-foreground'>
                    通知脚本返回含 release_id 的 JSON，并写入{' '}
                    <code className='rounded bg-background px-1 py-0.5 font-mono text-foreground'>
                      {code?.release_id_file ?? 'deploy-release-id.txt'}
                    </code>
                    。通过云效产物传递机制把文件交给后续任务，证据脚本会自动读取；也可以显式设置
                    RELEASE_ID。CI 在检查完成后上报，Smoke
                    需等测试环境发布成功并完成真实探测后上报。
                  </p>
                </div>
                <div className='grid gap-3 sm:grid-cols-3'>
                  <div className='rounded-md bg-background p-3'>
                    <p className='text-sm font-medium'>CI</p>
                    <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                      绑定同一制品的流水线检查结果。仅在检查实际完成后提交。
                    </p>
                  </div>
                  <div className='rounded-md bg-background p-3'>
                    <p className='text-sm font-medium'>Smoke</p>
                    <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                      先对测试环境执行健康或业务探测；平台会拒绝在 rollout
                      成功前提交。
                    </p>
                  </div>
                  <div className='rounded-md bg-background p-3'>
                    <p className='text-sm font-medium'>安全扫描</p>
                    <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                      报告绑定制品
                      digest、扫描工具和规则版本。目前只记录报告，不阻断发布。
                    </p>
                  </div>
                </div>
                {code ? (
                  <>
                    <p className='text-xs leading-5 text-muted-foreground'>
                      必需变量：
                      {code.verification_required_variables.map(
                        (name, index) => (
                          <span key={name}>
                            {index > 0 ? '、' : ' '}
                            <code className='rounded bg-background px-1 py-0.5 font-mono text-foreground'>
                              {name}
                            </code>
                          </span>
                        )
                      )}
                      。release ID 可从默认产物文件读取，也可设置
                      RELEASE_ID；可选设置
                      DEPLOY_RELEASE_ID_FILE、DEPLOY_EVIDENCE_REPORT_URL、
                      DEPLOY_EVIDENCE_SUMMARY 和 1–168 小时的
                      DEPLOY_EVIDENCE_VALID_HOURS。
                    </p>
                    <Textarea
                      aria-label='Runner CI、Smoke 与扫描证据脚本'
                      readOnly
                      value={code.verification_script}
                      className='min-h-48 font-mono text-xs'
                    />
                    <div className='flex flex-wrap gap-2'>
                      <Button
                        variant='outline'
                        onClick={() => copy(code.verification_script)}
                      >
                        <Copy />
                        复制证据脚本
                      </Button>
                      <Button
                        variant='outline'
                        onClick={() =>
                          download(
                            `verify-${app.app_name}.sh`,
                            code.verification_script
                          )
                        }
                      >
                        <Download />
                        下载证据脚本
                      </Button>
                    </div>
                    <p className='text-xs text-muted-foreground'>
                      设置 DEPLOY_EVIDENCE_KIND=ci、smoke 或 scan；状态默认为
                      passed。提交 failed 会保留失败报告；CI / Smoke
                      会让任务返回失败，扫描仍按仅报告处理。扫描另需
                      DEPLOY_SCAN_TOOL、DEPLOY_SCAN_TOOL_VERSION 和
                      DEPLOY_SCAN_RULES_VERSION。平台自动绑定此发布的 digest 和
                      runner:{binding.id} 身份，Runner 不能提交人工验收。
                    </p>
                  </>
                ) : (
                  <p role={scripts.isError ? 'alert' : undefined}>
                    {scripts.isError
                      ? errorDetail(scripts.error)
                      : '正在生成脚本…'}
                  </p>
                )}
              </section>
            </div>
          )}
          {step === 2 && binding && (
            <div className='space-y-5'>
              <div className='rounded-lg border p-4'>
                <p className='text-sm font-semibold'>
                  先在内网 Runner 的任务环境运行接入测试
                </p>
                <p className='mt-2 text-sm text-muted-foreground'>
                  只验证密钥、流水线与组件匹配，不创建发布任务，也不会更新镜像。
                </p>
                {code && (
                  <>
                    <Textarea
                      aria-label='Runner 接入测试脚本'
                      readOnly
                      value={code.check_script}
                      className='mt-3 min-h-36 font-mono text-xs'
                    />
                    <Button
                      variant='outline'
                      className='mt-3'
                      onClick={() => copy(code.check_script)}
                    >
                      <Copy />
                      复制接入测试脚本
                    </Button>
                  </>
                )}
              </div>
              <div className='space-y-3 rounded-lg bg-muted p-4 text-sm'>
                <div className='flex flex-wrap items-center gap-2'>
                  <Badge variant='outline'>
                    {(current?.binding.enabled ?? binding.enabled)
                      ? current?.authorization_expired
                        ? '授权已过期，请重新生成'
                        : '授权已启用'
                      : '授权已禁用'}
                  </Badge>
                  <Badge variant='outline'>
                    {current?.binding.last_check_at
                      ? '接入测试通过'
                      : '尚未收到接入测试'}
                  </Badge>
                </div>
                <p>
                  授权到期：
                  {new Date(
                    current?.binding.expires_at ?? binding.expires_at
                  ).toLocaleString()}
                </p>
                <p>
                  最近接入测试：
                  {current?.binding.last_check_at
                    ? new Date(current.binding.last_check_at).toLocaleString()
                    : '暂无'}
                </p>
                <p>
                  最近镜像通知：
                  {current?.last_event_at
                    ? new Date(current.last_event_at).toLocaleString()
                    : '暂无'}
                </p>
                <p>发布状态：{current?.last_status ?? '尚未创建发布任务'}</p>
                <p>
                  最近发布单：
                  {current?.last_release_id
                    ? `#${current.last_release_id}`
                    : '暂无'}
                </p>
                {current?.last_image && (
                  <p className='font-mono text-xs break-all'>
                    {current.last_image}
                  </p>
                )}
                {current?.last_error && (
                  <p className='text-destructive'>{current.last_error}</p>
                )}
                <p className='text-xs text-muted-foreground'>
                  这些时间记录最近活动，不代表 Runner
                  当前在线。默认通知脚本等待发布完成；DEPLOY_WAIT=0
                  可在受理后返回。
                </p>
              </div>
              <div className='flex flex-wrap gap-2'>
                <Button variant='outline' onClick={() => entries.refetch()}>
                  <RefreshCw />
                  刷新接入状态
                </Button>
                <Button
                  variant='outline'
                  onClick={() =>
                    toggle.mutate(
                      !(current?.binding.enabled ?? binding.enabled)
                    )
                  }
                  disabled={busy}
                >
                  {(current?.binding.enabled ?? binding.enabled)
                    ? '禁用接入授权'
                    : '启用接入授权'}
                </Button>
                {isAdmin && current?.binding.last_applied_at && (
                  <Button
                    variant='outline'
                    onClick={async () => {
                      try {
                        const r = await runnerApi.backup(binding.id)
                        download(
                          `pipeline-${binding.pipeline_id}-before.yaml`,
                          r.data
                        )
                      } catch (e) {
                        setError(errorDetail(e))
                      }
                    }}
                  >
                    <Download />
                    下载写入前备份
                  </Button>
                )}
              </div>
            </div>
          )}
        </div>
        <div className='border-t bg-background px-6 py-4'>
          {error && (
            <p
              role='alert'
              className='mb-3 rounded-lg bg-destructive/10 p-3 text-sm text-destructive'
            >
              {error}
            </p>
          )}
          <div className='grid grid-cols-2 gap-2 sm:flex sm:items-center sm:justify-between sm:gap-3'>
            <p className='col-span-2 text-xs text-muted-foreground sm:min-w-0 sm:flex-1'>
              {step === 0 &&
                (missingConfig.length > 0
                  ? `填写${missingConfig.join('、')}后继续。`
                  : '接入授权仅适用于非生产环境。')}
              {step === 1 &&
                (!code
                  ? scripts.isError
                    ? '脚本加载失败，请查看正文中的错误信息。'
                    : '接入脚本加载完成后即可继续。'
                  : '下一步：检查接入。')}
              {step === 2 && '完成测试后，云效构建可通知此组件。'}
            </p>
            <div className='contents sm:flex sm:shrink-0 sm:justify-end sm:gap-2'>
              {step > 0 && (
                <Button
                  variant='outline'
                  className='w-full sm:w-auto'
                  onClick={() => {
                    setStep(step - 1)
                    setError('')
                  }}
                  disabled={busy}
                >
                  上一步
                </Button>
              )}
              <Button
                variant='outline'
                className='w-full sm:w-auto'
                onClick={onClose}
                disabled={busy}
              >
                <X />
                {step === 0 ? '取消接入' : '关闭引导'}
              </Button>
              {step === 0 && (
                <Button
                  className='col-span-2 w-full sm:col-span-1 sm:w-auto'
                  onClick={() => configure.mutate()}
                  disabled={missingConfig.length > 0 || busy}
                >
                  {configure.isPending ? '正在生成…' : configureLabel}
                </Button>
              )}
              {step === 1 && (
                <Button
                  className='col-span-2 w-full whitespace-nowrap sm:col-span-1 sm:w-auto'
                  aria-label='下一步：检查接入'
                  onClick={() => setStep(2)}
                  disabled={!code || busy}
                >
                  <span className='sm:hidden'>检查接入</span>
                  <span className='hidden sm:inline'>下一步：检查接入</span>
                </Button>
              )}
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
