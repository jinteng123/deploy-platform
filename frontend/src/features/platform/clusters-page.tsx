import { useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  CircleAlert,
  FileKey,
  FileUp,
  FlaskConical,
  IdCard,
  KeyRound,
  Plug,
  Plus,
  Server,
  ShieldCheck,
  SlidersHorizontal,
  Terminal,
  TriangleAlert,
  type LucideIcon,
} from 'lucide-react'
import { toast } from 'sonner'
import { platformApi, type Cluster, type ClusterPayload } from '@/api/client'
import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { OptionCard } from '@/components/option-card'

const tierLabel: Record<string, string> = {
  non_production: '非生产',
  production: '生产',
}
const policyLabel: Record<string, string> = {
  automatic: '自动',
  manual: '手动',
}
const connLabel: Record<string, string> = {
  kubeconfig: 'KubeConfig',
  token: 'Token',
  legacy_ssh: 'SSH（旧）',
}

type FormState = {
  name: string
  env_code: string
  environment: string
  environment_tier: string
  deploy_policy: string
  connection_type: string
  api_server: string
  credential: string
  ca_cert: string
  insecure_skip_tls_verify: boolean
  enabled: boolean
  default_namespace: string
  description: string
  k8s_master_host: string
  k8s_master_port: string
  k8s_master_user: string
  k8s_master_key_path: string
}

const emptyForm: FormState = {
  name: '',
  env_code: '',
  environment: '',
  environment_tier: 'non_production',
  deploy_policy: 'manual',
  connection_type: 'kubeconfig',
  api_server: '',
  credential: '',
  ca_cert: '',
  insecure_skip_tls_verify: false,
  enabled: true,
  default_namespace: 'default',
  description: '',
  k8s_master_host: '',
  k8s_master_port: '22',
  k8s_master_user: 'root',
  k8s_master_key_path: '',
}

function fromCluster(c: Cluster): FormState {
  return {
    name: c.name,
    env_code: c.env_code,
    environment: c.environment,
    environment_tier: c.environment_tier,
    deploy_policy: c.deploy_policy,
    connection_type: c.connection_type,
    api_server: c.api_server ?? '',
    credential: '',
    ca_cert: c.ca_cert ?? '',
    insecure_skip_tls_verify: c.insecure_skip_tls_verify,
    enabled: c.enabled,
    default_namespace: c.default_namespace,
    description: c.description ?? '',
    k8s_master_host: c.k8s_master_host ?? '',
    k8s_master_port: String(c.k8s_master_port || 22),
    k8s_master_user: c.k8s_master_user ?? 'root',
    k8s_master_key_path: '',
  }
}

function toPayload(f: FormState): ClusterPayload {
  const p: ClusterPayload = {
    name: f.name.trim(),
    env_code: f.env_code.trim(),
    environment: f.environment.trim(),
    environment_tier: f.environment_tier,
    deploy_policy: f.deploy_policy,
    connection_type: f.connection_type,
    insecure_skip_tls_verify: f.insecure_skip_tls_verify,
    enabled: f.enabled,
    default_namespace: f.default_namespace.trim() || 'default',
    description: f.description.trim(),
  }
  if (f.connection_type === 'token') {
    p.api_server = f.api_server.trim()
    if (f.ca_cert.trim()) p.ca_cert = f.ca_cert
  }
  if (f.connection_type === 'legacy_ssh') {
    p.k8s_master_host = f.k8s_master_host.trim()
    p.k8s_master_port = Number(f.k8s_master_port) || 22
    p.k8s_master_user = f.k8s_master_user.trim()
    if (f.k8s_master_key_path.trim())
      p.k8s_master_key_path = f.k8s_master_key_path.trim()
  }
  // credential 明文只用于提交；编辑时留空表示保持原凭据
  if (f.credential.trim()) p.credential = f.credential
  return p
}

function Field({
  label,
  trailing,
  children,
}: {
  label: string
  trailing?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div className='min-w-0 space-y-2'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <Label className='text-xs font-medium text-muted-foreground'>
          {label}
        </Label>
        {trailing}
      </div>
      {children}
    </div>
  )
}

function FormSection({
  icon: Icon,
  title,
  desc,
  children,
}: {
  icon: LucideIcon
  title: string
  desc: string
  children: React.ReactNode
}) {
  return (
    <section className='min-w-0 space-y-4'>
      <div className='flex items-center gap-3'>
        <span className='flex size-7 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary'>
          <Icon className='size-3.5' />
        </span>
        <div>
          <h3 className='text-sm font-semibold text-foreground'>{title}</h3>
          <p className='text-xs text-muted-foreground'>{desc}</p>
        </div>
      </div>
      {children}
    </section>
  )
}

export function ClusterFormDialog({
  editing,
  open,
  onClose,
  onSaved,
}: {
  editing: Cluster | null
  open: boolean
  onClose: () => void
  onSaved: () => void
}) {
  // 组件以 open=true 条件挂载，初始值必须来自 props 而非 onOpenChange
  const [form, setForm] = useState<FormState>(() =>
    editing ? fromCluster(editing) : { ...emptyForm }
  )
  const kubeconfigFileRef = useRef<HTMLInputElement>(null)
  const [kubeconfigFileName, setKubeconfigFileName] = useState('')
  const [importingKubeconfig, setImportingKubeconfig] = useState(false)
  const [error, setError] = useState('')
  const [testing, setTesting] = useState(false)
  const save = useMutation({
    mutationFn: (payload: ClusterPayload) =>
      editing
        ? platformApi.updateCluster(editing.id, payload)
        : platformApi.createCluster(payload),
    onSuccess: () => {
      toast.success(editing ? '集群已更新' : '集群已创建')
      onSaved()
      onClose()
    },
    onError: (err) => {
      const detail = (err as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      setError(detail ?? '保存失败')
    },
  })
  const set = (patch: Partial<FormState>) =>
    setForm((f) => ({ ...f, ...patch }))
  const importKubeconfig = async (file?: File) => {
    if (!file) return
    setImportingKubeconfig(true)
    try {
      const content = await file.text()
      if (!content.trim()) {
        setError('所选 kubeconfig 文件为空，请选择有效文件')
        return
      }
      set({ credential: content })
      setKubeconfigFileName(file.name)
      setError('')
    } catch {
      setError('读取 kubeconfig 文件失败，请重试')
    } finally {
      setImportingKubeconfig(false)
    }
  }
  // 生产集群强制手动发布
  const prodAuto =
    form.environment_tier === 'production' && form.deploy_policy === 'automatic'
  const needsCredential = form.connection_type !== 'legacy_ssh'
  // 保存：新建或切换连接类型时直连集群必须有凭据；相同类型编辑留空保持
  const saveCredentialMissing =
    needsCredential &&
    !form.credential.trim() &&
    (!editing || editing.connection_type !== form.connection_type)
  // 测试连接：凭据永不回传，无论新建/编辑都必须填写当前表单凭据
  const testCredentialMissing = needsCredential && !form.credential.trim()

  const testConnection = async () => {
    setError('')
    if (testCredentialMissing) {
      setError('测试连接需要填写当前凭据（已保存凭据不回传）')
      return
    }
    setTesting(true)
    try {
      const res = await platformApi.testClusterConnection(toPayload(form))
      toast.success(`连接成功：${res.data.version}`)
    } catch (err) {
      const detail = (err as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      setError(detail ?? '连接测试失败')
    } finally {
      setTesting(false)
    }
  }

  const submit = () => {
    setError('')
    if (prodAuto) {
      setError('生产集群必须使用手动发布策略')
      return
    }
    if (!form.name.trim() || !form.environment.trim()) {
      setError('名称与环境名称为必填项')
      return
    }
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,15}$/.test(form.env_code.trim())) {
      setError(
        'env_code 须以字母或数字开头，仅含字母数字 . _ -，不超过 16 字符'
      )
      return
    }
    if (saveCredentialMissing) {
      setError('直连集群必须提供凭据')
      return
    }
    save.mutate(toPayload(form))
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          setError('')
          onClose()
        }
      }}
    >
      <DialogContent className='flex max-h-[90dvh] min-h-0 min-w-0 flex-col gap-0 overflow-hidden rounded-2xl p-0 sm:max-w-2xl'>
        <DialogHeader className='shrink-0 border-b border-border bg-muted/40 px-6 py-5'>
          <div className='flex items-start gap-3'>
            <span className='flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary text-primary-foreground'>
              <Server className='size-5' />
            </span>
            <div className='space-y-1 text-start'>
              <DialogTitle className='text-foreground'>
                {editing ? `编辑集群：${editing.name}` : '新增集群'}
              </DialogTitle>
              <DialogDescription>
                {editing
                  ? '调整集群接入信息与发布策略。'
                  : '接入一个 Kubernetes 集群，用于发布与资源控制台。'}
              </DialogDescription>
              {editing && (
                <div className='flex flex-wrap gap-1.5 pt-1.5'>
                  <Badge
                    variant='outline'
                    className='border-border bg-background font-mono text-foreground'
                  >
                    {form.env_code || '—'}
                  </Badge>
                  <Badge
                    variant='outline'
                    className={
                      form.environment_tier === 'production'
                        ? 'border-destructive/30 bg-destructive/10 text-destructive'
                        : 'border-border bg-muted text-muted-foreground'
                    }
                  >
                    {tierLabel[form.environment_tier] ?? form.environment_tier}
                  </Badge>
                  <Badge
                    variant='outline'
                    className='border-border bg-muted text-muted-foreground'
                  >
                    {connLabel[form.connection_type] ?? form.connection_type}
                  </Badge>
                </div>
              )}
            </div>
          </div>
        </DialogHeader>
        <div className='min-h-0 min-w-0 flex-1 space-y-8 overflow-x-hidden overflow-y-auto overscroll-contain px-6 py-6'>
          <FormSection icon={IdCard} title='基本信息' desc='集群名称与环境归属'>
            <div className='grid gap-4 sm:grid-cols-2'>
              <Field label='名称'>
                <Input
                  aria-label='名称'
                  maxLength={64}
                  value={form.name}
                  onChange={(e) => set({ name: e.target.value })}
                />
              </Field>
              <Field label='env_code（唯一标识）'>
                <Input
                  maxLength={16}
                  pattern='[A-Za-z0-9][A-Za-z0-9._-]{0,15}'
                  placeholder='如 test / prod-east'
                  className='font-mono'
                  value={form.env_code}
                  onChange={(e) => set({ env_code: e.target.value })}
                />
                <p className='text-xs text-muted-foreground'>
                  字母数字开头，可含 . _ -，≤16 字符
                </p>
              </Field>
            </div>
            <Field label='环境名称'>
              <Input
                maxLength={64}
                placeholder='如 staging / prod-east'
                value={form.environment}
                onChange={(e) => set({ environment: e.target.value })}
              />
            </Field>
            <Field label='环境层级'>
              <div
                role='radiogroup'
                aria-label='环境层级'
                className='grid gap-3 sm:grid-cols-2'
              >
                <OptionCard
                  icon={FlaskConical}
                  title='非生产'
                  desc='测试 / 预发环境，可自动发布'
                  selected={form.environment_tier === 'non_production'}
                  disabled={save.isPending}
                  onClick={() => set({ environment_tier: 'non_production' })}
                />
                <OptionCard
                  icon={ShieldCheck}
                  title='生产'
                  desc='仅允许手动发布'
                  tone='red'
                  selected={form.environment_tier === 'production'}
                  disabled={save.isPending}
                  onClick={() =>
                    // 生产集群强制手动发布
                    set({
                      environment_tier: 'production',
                      deploy_policy: 'manual',
                    })
                  }
                />
              </div>
            </Field>
            <Field label='发布策略'>
              <div className='flex flex-wrap items-center gap-3'>
                <div
                  role='radiogroup'
                  aria-label='发布策略'
                  className='inline-flex rounded-lg bg-muted p-1'
                >
                  {(['automatic', 'manual'] as const).map((v) => (
                    <button
                      key={v}
                      type='button'
                      role='radio'
                      aria-checked={form.deploy_policy === v}
                      disabled={
                        save.isPending ||
                        (v === 'automatic' &&
                          form.environment_tier === 'production')
                      }
                      onClick={() => set({ deploy_policy: v })}
                      className={cn(
                        'rounded-md px-4 py-1.5 text-sm transition',
                        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none',
                        'disabled:cursor-not-allowed disabled:opacity-50',
                        form.deploy_policy === v
                          ? 'bg-background font-medium text-foreground shadow-sm'
                          : 'text-muted-foreground hover:text-foreground'
                      )}
                    >
                      {policyLabel[v]}
                    </button>
                  ))}
                </div>
                {form.environment_tier === 'production' && (
                  <span className='text-xs text-muted-foreground'>
                    生产集群仅支持手动发布
                  </span>
                )}
              </div>
            </Field>
            {prodAuto && (
              <p className='flex items-start gap-1.5 rounded-lg border border-amber-300/70 bg-amber-50 px-3 py-2 text-xs leading-5 text-amber-900 dark:border-amber-900 dark:bg-amber-950/60 dark:text-amber-200'>
                <TriangleAlert className='mt-0.5 size-3.5 shrink-0' />
                <span>生产集群必须使用手动发布策略</span>
              </p>
            )}
          </FormSection>

          <FormSection
            icon={Plug}
            title='连接配置'
            desc='平台访问 API Server 的方式与凭据'
          >
            <div className='grid gap-4'>
              <Field label='连接方式'>
                <div
                  role='radiogroup'
                  aria-label='连接方式'
                  className='grid gap-3 sm:grid-cols-3'
                >
                  <OptionCard
                    icon={FileKey}
                    title='KubeConfig'
                    desc='粘贴 kubeconfig 文件'
                    selected={form.connection_type === 'kubeconfig'}
                    disabled={save.isPending}
                    onClick={() => set({ connection_type: 'kubeconfig' })}
                  />
                  <OptionCard
                    icon={KeyRound}
                    title='API Server Token'
                    desc='API 地址 + Token'
                    selected={form.connection_type === 'token'}
                    disabled={save.isPending}
                    onClick={() => set({ connection_type: 'token' })}
                  />
                  <OptionCard
                    icon={Terminal}
                    title='SSH（旧）'
                    desc='兼容既有数据，不推荐'
                    muted
                    selected={form.connection_type === 'legacy_ssh'}
                    disabled={save.isPending}
                    onClick={() => set({ connection_type: 'legacy_ssh' })}
                  />
                </div>
              </Field>

              {form.connection_type === 'kubeconfig' && (
                <Field
                  label='KubeConfig'
                  trailing={
                    <div className='flex flex-wrap items-center gap-2'>
                      {editing && (
                        <span className='rounded-full border border-border bg-muted px-2 py-0.5 text-xs text-muted-foreground'>
                          留空保持原凭据
                        </span>
                      )}
                      <input
                        ref={kubeconfigFileRef}
                        type='file'
                        className='sr-only'
                        tabIndex={-1}
                        aria-label='选择 kubeconfig 文件'
                        onChange={(event) => {
                          const file = event.currentTarget.files?.[0]
                          event.currentTarget.value = ''
                          void importKubeconfig(file)
                        }}
                      />
                      <Button
                        type='button'
                        size='sm'
                        variant='outline'
                        className='h-8 gap-1.5'
                        onClick={() => kubeconfigFileRef.current?.click()}
                        disabled={save.isPending || importingKubeconfig}
                      >
                        <FileUp className='size-4' />
                        {importingKubeconfig ? '导入中…' : '导入文件'}
                      </Button>
                    </div>
                  }
                >
                  <Textarea
                    aria-label='KubeConfig'
                    rows={8}
                    wrap='soft'
                    style={{ fieldSizing: 'fixed' }}
                    className='h-40 max-h-80 min-h-40 max-w-full min-w-0 resize-y overflow-auto bg-muted/40 font-mono text-xs leading-5 [overflow-wrap:anywhere]'
                    placeholder='粘贴 kubeconfig YAML'
                    value={form.credential}
                    onChange={(e) => {
                      set({ credential: e.target.value })
                      setKubeconfigFileName('')
                    }}
                  />
                  {kubeconfigFileName && (
                    <p className='min-w-0 text-xs break-all text-primary'>
                      已导入：{kubeconfigFileName}
                    </p>
                  )}
                  <p className='text-xs text-muted-foreground'>
                    凭据仅用于服务端连接与存储，不回显明文。
                  </p>
                </Field>
              )}

              {form.connection_type === 'token' && (
                <>
                  <Field label='API Server 地址'>
                    <Input
                      maxLength={512}
                      placeholder='https://k8s.example.com:6443'
                      value={form.api_server}
                      onChange={(e) => set({ api_server: e.target.value })}
                    />
                  </Field>
                  <Field
                    label='Token'
                    trailing={
                      editing && (
                        <span className='rounded-full border border-border bg-muted px-2 py-0.5 text-xs text-muted-foreground'>
                          留空保持原凭据
                        </span>
                      )
                    }
                  >
                    <Textarea
                      aria-label='Token'
                      rows={3}
                      wrap='soft'
                      style={{ fieldSizing: 'fixed' }}
                      className='max-w-full min-w-0 resize-y overflow-auto bg-muted/40 font-mono text-xs [overflow-wrap:anywhere]'
                      value={form.credential}
                      onChange={(e) => set({ credential: e.target.value })}
                    />
                  </Field>
                  <Field label='CA 证书（可选，PEM）'>
                    <Textarea
                      rows={3}
                      wrap='soft'
                      style={{ fieldSizing: 'fixed' }}
                      className='max-w-full min-w-0 resize-y overflow-auto bg-muted/40 font-mono text-xs [overflow-wrap:anywhere]'
                      value={form.ca_cert}
                      onChange={(e) => set({ ca_cert: e.target.value })}
                    />
                  </Field>
                  <div className='flex items-center justify-between gap-4 rounded-xl border border-border bg-muted/40 px-4 py-3'>
                    <div>
                      <p className='text-sm font-medium text-foreground'>
                        跳过 TLS 校验
                      </p>
                      <p className='mt-0.5 text-xs text-muted-foreground'>
                        不推荐用于生产环境
                      </p>
                    </div>
                    <Switch
                      aria-label='跳过 TLS 校验'
                      checked={form.insecure_skip_tls_verify}
                      onCheckedChange={(v) =>
                        set({ insecure_skip_tls_verify: v })
                      }
                    />
                  </div>
                </>
              )}

              {form.connection_type === 'legacy_ssh' && (
                <div className='grid gap-4 sm:grid-cols-2'>
                  <Field label='Master 主机'>
                    <Input
                      maxLength={128}
                      value={form.k8s_master_host}
                      onChange={(e) => set({ k8s_master_host: e.target.value })}
                    />
                  </Field>
                  <Field label='SSH 端口'>
                    <Input
                      value={form.k8s_master_port}
                      onChange={(e) => set({ k8s_master_port: e.target.value })}
                    />
                  </Field>
                  <Field label='SSH 用户'>
                    <Input
                      maxLength={64}
                      value={form.k8s_master_user}
                      onChange={(e) => set({ k8s_master_user: e.target.value })}
                    />
                  </Field>
                  <Field label='私钥路径（可选）'>
                    <Input
                      maxLength={256}
                      value={form.k8s_master_key_path}
                      onChange={(e) =>
                        set({ k8s_master_key_path: e.target.value })
                      }
                    />
                  </Field>
                </div>
              )}
            </div>
          </FormSection>

          <FormSection
            icon={SlidersHorizontal}
            title='其他设置'
            desc='默认命名空间与启用状态'
          >
            <div className='grid gap-4 sm:grid-cols-2'>
              <Field label='默认命名空间'>
                <Input
                  maxLength={64}
                  value={form.default_namespace}
                  onChange={(e) => set({ default_namespace: e.target.value })}
                />
              </Field>
              <Field label='描述'>
                <Input
                  maxLength={256}
                  value={form.description}
                  onChange={(e) => set({ description: e.target.value })}
                />
              </Field>
            </div>
            <div className='flex items-center justify-between gap-4 rounded-xl border border-border bg-muted/40 px-4 py-3'>
              <div>
                <p className='text-sm font-medium text-foreground'>
                  启用该集群
                </p>
                <p className='mt-0.5 text-xs text-muted-foreground'>
                  停用后不可用于发布与控制台访问
                </p>
              </div>
              <Switch
                aria-label='启用该集群'
                checked={form.enabled}
                onCheckedChange={(v) => set({ enabled: v })}
              />
            </div>
          </FormSection>
        </div>
        <div className='shrink-0 border-t border-border bg-muted/40 px-6 py-4'>
          {error && (
            <p className='mb-3 flex w-full items-start gap-1.5 rounded-lg border border-destructive/20 bg-destructive/10 px-3 py-2 text-xs leading-5 text-destructive'>
              <CircleAlert className='mt-0.5 size-3.5 shrink-0' />
              <span>{error}</span>
            </p>
          )}
          <div className='flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between'>
            <Button
              variant='outline'
              onClick={testConnection}
              disabled={
                testing ||
                importingKubeconfig ||
                save.isPending ||
                prodAuto ||
                testCredentialMissing
              }
            >
              {testing ? '测试中…' : '测试连接'}
            </Button>
            <div className='flex flex-col-reverse gap-2 sm:flex-row'>
              <Button variant='outline' onClick={onClose}>
                取消
              </Button>
              <Button
                onClick={submit}
                disabled={
                  importingKubeconfig ||
                  save.isPending ||
                  prodAuto ||
                  saveCredentialMissing
                }
              >
                {save.isPending ? '保存中…' : '保存'}
              </Button>
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}

export function ClustersPage() {
  const queryClient = useQueryClient()
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Cluster | null>(null)
  const [deleting, setDeleting] = useState<Cluster | null>(null)
  const clusters = useQuery({
    queryKey: ['clusters'],
    queryFn: () => platformApi.clusters(),
  })
  const remove = useMutation({
    mutationFn: (id: number) => platformApi.deleteCluster(id),
    onSuccess: () => {
      toast.success('集群已删除')
      queryClient.invalidateQueries({ queryKey: ['clusters'] })
    },
    onError: (err) => {
      const detail = (err as { response?: { data?: { detail?: string } } })
        .response?.data?.detail
      toast.error(detail ?? '删除失败')
    },
    onSettled: () => setDeleting(null),
  })
  const rows = clusters.data?.data ?? []
  return (
    <>
      <div className='flex-1 overflow-y-auto pb-8'>
        <div className='mb-6 flex flex-wrap items-end justify-between gap-4'>
          <div>
            <h2 className='text-lg font-semibold text-foreground'>集群管理</h2>
            <p className='mt-1 text-sm text-muted-foreground'>
              管理平台接入的 Kubernetes 集群与连接配置。
            </p>
          </div>
          <Button
            className='rounded-lg shadow-sm'
            onClick={() => {
              setEditing(null)
              setFormOpen(true)
            }}
          >
            <Plus className='size-4' />
            新增集群
          </Button>
        </div>
        <Card className='rounded-2xl border-border shadow-sm'>
          <CardHeader className='flex flex-row items-center justify-between gap-4 space-y-0'>
            <div>
              <CardTitle className='text-base text-foreground'>
                Kubernetes 集群列表
              </CardTitle>
              <p className='mt-1 text-xs text-muted-foreground'>
                已配置 {rows.length} 个集群
              </p>
            </div>
            <Server className='size-5 text-muted-foreground' />
          </CardHeader>
          <CardContent>
            {rows.length === 0 ? (
              <div className='rounded-xl border border-dashed border-border bg-muted/30 py-12 text-center text-sm text-muted-foreground'>
                暂无集群，请先添加一个 Kubernetes 集群
              </div>
            ) : (
              <div className='grid gap-4 md:grid-cols-2 xl:grid-cols-3'>
                {rows.map((c) => (
                  <div
                    key={c.id}
                    className='group rounded-xl border border-border bg-card p-4 transition hover:-translate-y-0.5 hover:border-primary/40 hover:shadow-md'
                  >
                    <div className='flex items-start justify-between gap-3'>
                      <div className='flex min-w-0 items-center gap-3'>
                        <span className='flex size-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 text-primary'>
                          <Server className='size-5' />
                        </span>
                        <div className='min-w-0'>
                          <p className='truncate font-semibold text-foreground'>
                            {c.name}
                          </p>
                          <p className='mt-1 truncate font-mono text-xs text-muted-foreground'>
                            {c.env_code}
                          </p>
                        </div>
                      </div>
                    </div>
                    <div className='mt-4 grid grid-cols-2 gap-x-4 gap-y-3 border-t border-border pt-4 text-xs'>
                      <div>
                        <p className='text-muted-foreground'>环境</p>
                        <p className='mt-1 font-medium text-foreground'>
                          {c.environment || '未设置'}
                        </p>
                      </div>
                      <div>
                        <p className='text-muted-foreground'>层级</p>
                        <p className='mt-1 font-medium text-foreground'>
                          {tierLabel[c.environment_tier] ?? c.environment_tier}
                        </p>
                      </div>
                      <div>
                        <p className='text-muted-foreground'>发布策略</p>
                        <p className='mt-1 font-medium text-foreground'>
                          {policyLabel[c.deploy_policy] ?? c.deploy_policy}
                        </p>
                      </div>
                      <div>
                        <p className='text-muted-foreground'>连接方式</p>
                        <p className='mt-1 font-medium text-foreground'>
                          {connLabel[c.connection_type] ?? c.connection_type}
                        </p>
                      </div>
                    </div>
                    <div className='mt-4 flex items-center justify-between gap-3 border-t border-border pt-3'>
                      {c.enabled ? (
                        <Badge className='border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-300'>
                          已启用
                        </Badge>
                      ) : (
                        <Badge
                          variant='outline'
                          className='text-muted-foreground'
                        >
                          停用
                        </Badge>
                      )}
                      <div className='flex gap-2'>
                        <Button
                          size='sm'
                          variant='outline'
                          onClick={() => {
                            setEditing(c)
                            setFormOpen(true)
                          }}
                        >
                          编辑
                        </Button>
                        <Button
                          size='sm'
                          variant='destructive'
                          onClick={() => setDeleting(c)}
                        >
                          删除
                        </Button>
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      </div>
      {formOpen && (
        <ClusterFormDialog
          editing={editing}
          open={formOpen}
          onClose={() => {
            setFormOpen(false)
            setEditing(null)
          }}
          onSaved={() =>
            queryClient.invalidateQueries({ queryKey: ['clusters'] })
          }
        />
      )}
      <ConfirmDialog
        open={deleting != null}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title='删除集群'
        desc={`确认删除集群 ${deleting?.name}（${deleting?.env_code}）？该操作不可恢复。`}
        confirmText='确认删除'
        destructive
        isLoading={remove.isPending}
        handleConfirm={() => {
          if (deleting) remove.mutate(deleting.id)
        }}
      />
    </>
  )
}
