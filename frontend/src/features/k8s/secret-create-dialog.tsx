import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, CircleAlert, KeyRound } from 'lucide-react'
import { toast } from 'sonner'
import * as YAML from 'yaml'
import { k8sApi } from '@/api/client'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  KvRowsEditor,
  kvRowsInvalid,
  kvRowsToData,
  type KvRow,
} from './kv-rows-editor'

// Secret names are DNS-1123 subdomains — dots allowed, ≤253 chars.
const NAME_RE =
  /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9]))*$/
const NAME_ERROR = '小写字母、数字、- 或 . 组成，≤253 字符'

// The built-in Secret types a create form offers. `desc` renders as the
// option's secondary line (and under the trigger); `hint` is the
// convention-keys note shown under the field for non-Opaque types.
const SECRET_TYPES: { value: string; desc: string; hint: string }[] = [
  { value: 'Opaque', desc: '自定义键值对', hint: '' },
  {
    value: 'kubernetes.io/tls',
    desc: 'TLS 证书（tls.crt / tls.key）',
    hint: '约定键：tls.crt（证书）、tls.key（私钥）',
  },
  {
    value: 'kubernetes.io/dockerconfigjson',
    desc: '镜像仓库凭据（.dockerconfigjson）',
    hint: '约定键：.dockerconfigjson（Docker 客户端配置 JSON）',
  },
  {
    value: 'kubernetes.io/basic-auth',
    desc: '用户名密码（username / password）',
    hint: '约定键：username（用户名）、password（密码）',
  },
  {
    value: 'kubernetes.io/ssh-auth',
    desc: 'SSH 私钥（ssh-privatekey）',
    hint: '约定键：ssh-privatekey（SSH 私钥）',
  },
]

const detail = (err: unknown): string =>
  (err as { response?: { data?: { detail?: string } } })?.response?.data
    ?.detail ?? (err instanceof Error ? err.message : '请求失败')

function Field({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className='min-w-0 space-y-1.5'>
      <Label className='text-xs font-medium text-[#5a6b85]'>{label}</Label>
      {children}
    </div>
  )
}

export function SecretCreateDialog({
  open,
  onOpenChange,
  clusterId,
  namespace,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  clusterId: number
  namespace: string
  onCreated: () => void
}) {
  const queryClient = useQueryClient()
  const [name, setName] = useState('')
  const [nameError, setNameError] = useState('')
  const [secretType, setSecretType] = useState('Opaque')
  const [rows, setRows] = useState<KvRow[]>([])
  // Secrets support the same immutable flag as ConfigMaps.
  const [immutableOn, setImmutableOn] = useState(false)
  const [showYaml, setShowYaml] = useState(false)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  const selectedType = SECRET_TYPES.find((t) => t.value === secretType)

  const manifestObj = useMemo(() => {
    const obj: Record<string, unknown> = {
      apiVersion: 'v1',
      kind: 'Secret',
      metadata: { name: name.trim(), namespace },
      type: secretType,
    }
    const data = kvRowsToData(rows)
    // Plaintext values go through stringData — the apiserver folds them
    // into `data` (base64) server-side. Omitted entirely when no rows.
    if (Object.keys(data).length > 0) obj.stringData = data
    if (immutableOn) obj.immutable = true
    return obj
  }, [name, namespace, secretType, rows, immutableOn])
  const manifest = useMemo(() => YAML.stringify(manifestObj), [manifestObj])

  const submit = async () => {
    setError('')
    const n = name.trim()
    if (!NAME_RE.test(n) || n.length > 253) {
      setNameError(NAME_ERROR)
      return
    }
    if (kvRowsInvalid(rows)) {
      setError('数据包含非法键名，请修正标红的键后重试')
      return
    }
    setPending(true)
    try {
      const params = {
        cluster_id: clusterId,
        version: 'v1',
        resource: 'secrets',
        namespace,
      }
      // Server-side dry run first — same validation, nothing persisted.
      await k8sApi.createResource(manifest, params, true)
      await k8sApi.createResource(manifest, params)
      toast.success(`密钥 ${n} 已创建`)
      void queryClient.invalidateQueries({
        queryKey: ['k8s', 'resources', clusterId, '', 'v1', 'secrets'],
      })
      onCreated()
      onOpenChange(false)
    } catch (err) {
      const status = (err as { response?: { status?: number } }).response
        ?.status
      setError(status === 409 ? '同名密钥已存在' : detail(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='flex max-h-[90vh] flex-col gap-0 overflow-hidden rounded-2xl p-0 sm:max-w-2xl'>
        <DialogHeader className='border-b border-[#edf1f7] bg-gradient-to-b from-[#f6f9fd] to-white px-6 py-5'>
          <div className='flex items-start gap-3'>
            <span className='flex size-10 shrink-0 items-center justify-center rounded-xl bg-[#234883] text-white'>
              <KeyRound className='size-5' />
            </span>
            <div className='space-y-1 text-start'>
              <DialogTitle className='text-[#1c355e]'>创建密钥</DialogTitle>
              <DialogDescription>
                Secret · 敏感信息以 stringData 提交，由集群加密存储
              </DialogDescription>
              <div className='flex flex-wrap gap-1.5 pt-1.5'>
                <Badge
                  variant='outline'
                  className='border-[#d6e1f0] bg-[#eef3fa] font-mono text-[#31598e]'
                >
                  {namespace}
                </Badge>
              </div>
            </div>
          </div>
        </DialogHeader>
        <div className='flex-1 space-y-6 overflow-y-auto px-6 py-6'>
          <div className='grid gap-4 sm:grid-cols-2'>
            <Field label='名称'>
              <Input
                aria-label='名称'
                maxLength={253}
                className='font-mono'
                placeholder='如 db-credentials'
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                  setNameError('')
                }}
              />
              {nameError ? (
                <p className='flex items-center gap-1.5 text-xs text-[#a52f3d]'>
                  <CircleAlert className='size-3.5 shrink-0' />
                  {nameError}
                </p>
              ) : (
                <p className='text-xs text-muted-foreground'>{NAME_ERROR}</p>
              )}
            </Field>
            <Field label='命名空间'>
              <div className='flex h-9 items-center'>
                <Badge
                  variant='outline'
                  className='border-[#d6e1f0] bg-[#eef3fa] font-mono text-[#31598e]'
                >
                  {namespace}
                </Badge>
              </div>
            </Field>
          </div>

          <Field label='类型'>
            <Select value={secretType} onValueChange={setSecretType}>
              <SelectTrigger aria-label='类型' className='w-full min-w-0'>
                {/* A custom span (not SelectValue) keeps the trigger to
                    short-name + description — the rich two-line option
                    layout would otherwise overflow it. */}
                <span
                  data-slot='select-value'
                  className='flex min-w-0 items-center gap-2'
                >
                  <span className='truncate font-mono text-xs'>
                    {secretType}
                  </span>
                  <span className='truncate text-xs text-muted-foreground'>
                    {selectedType?.desc}
                  </span>
                </span>
              </SelectTrigger>
              <SelectContent>
                {SECRET_TYPES.map((t) => (
                  <SelectItem key={t.value} value={t.value}>
                    <span className='flex min-w-0 flex-col items-start'>
                      <span className='font-mono text-xs'>{t.value}</span>
                      <span className='text-xs text-muted-foreground'>
                        {t.desc}
                      </span>
                    </span>
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {selectedType?.hint && (
              <p className='rounded-lg bg-[#f4f7fb] px-3 py-2 text-xs leading-5 text-[#5a6b85]'>
                {selectedType.hint}
              </p>
            )}
          </Field>

          <div className='flex items-start justify-between gap-4 rounded-xl border border-[#e3eaf4] bg-[#fbfdff] px-4 py-3'>
            <div className='space-y-0.5'>
              <Label
                htmlFor='secret-create-immutable'
                className='text-xs font-medium text-[#5a6b85]'
              >
                创建后不可修改（immutable）
              </Label>
              <p className='text-xs text-muted-foreground'>
                开启后数据将不可再修改，如必须修改需删除重建
              </p>
            </div>
            <Switch
              id='secret-create-immutable'
              aria-label='创建后不可修改（immutable）'
              checked={immutableOn}
              onCheckedChange={setImmutableOn}
              disabled={pending}
            />
          </div>

          <Field label='数据'>
            <KvRowsEditor
              rows={rows}
              onChange={setRows}
              disabled={pending}
              valuePlaceholder='敏感值'
            />
            <p className='text-xs text-muted-foreground'>
              按明文填写，提交时经 stringData 写入集群，本平台不回显已保存的值
            </p>
          </Field>

          <button
            type='button'
            className='flex items-center gap-1.5 text-xs font-medium text-[#31598e] transition hover:text-[#234883] focus-visible:ring-2 focus-visible:ring-[#234883]/40 focus-visible:outline-none'
            onClick={() => setShowYaml((v) => !v)}
            aria-expanded={showYaml}
          >
            {showYaml ? (
              <ChevronDown className='size-3.5' />
            ) : (
              <ChevronRight className='size-3.5' />
            )}
            YAML 预览
          </button>
          {showYaml && (
            <pre className='max-h-56 overflow-auto rounded-xl border border-[#e5ecf5] bg-[#fbfdff] p-4 font-mono text-xs leading-5 text-[#2a4165]'>
              {manifest}
            </pre>
          )}
        </div>
        <div className='border-t border-[#edf1f7] bg-[#f8fafd] px-6 py-4'>
          {error && (
            <p
              role='alert'
              className='mb-3 flex w-full items-start gap-1.5 rounded-lg bg-[#fdf0f1] px-3 py-2 text-xs leading-5 text-[#a52f3d]'
            >
              <CircleAlert className='mt-0.5 size-3.5 shrink-0' />
              <span>{error}</span>
            </p>
          )}
          <div className='flex flex-col-reverse gap-2 sm:flex-row sm:items-center sm:justify-end'>
            <Button variant='outline' onClick={() => onOpenChange(false)}>
              取消
            </Button>
            <Button
              className='bg-[#234883] hover:bg-[#193d72]'
              onClick={submit}
              disabled={pending}
            >
              {pending ? '创建中…' : '创建'}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  )
}
