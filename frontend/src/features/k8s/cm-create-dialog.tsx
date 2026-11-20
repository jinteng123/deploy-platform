import { useMemo, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ChevronDown, ChevronRight, CircleAlert, FileText } from 'lucide-react'
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
import { Switch } from '@/components/ui/switch'
import {
  KvRowsEditor,
  kvRowsInvalid,
  kvRowsToData,
  type KvRow,
} from './kv-rows-editor'

// ConfigMap names are DNS-1123 subdomains — dots allowed, ≤253 chars
// (looser than PVC's DNS-1123 label).
const NAME_RE =
  /^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9]))*$/
const NAME_ERROR = '小写字母、数字、- 或 . 组成，≤253 字符'

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

export function CmCreateDialog({
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
  const [rows, setRows] = useState<KvRow[]>([])
  // Kuboard's immutable toggle — the manifest carries immutable: true
  // when on; initial data is still entered before it takes effect.
  const [immutableOn, setImmutableOn] = useState(false)
  const [showYaml, setShowYaml] = useState(false)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  const manifestObj = useMemo(() => {
    const obj: Record<string, unknown> = {
      apiVersion: 'v1',
      kind: 'ConfigMap',
      metadata: { name: name.trim(), namespace },
    }
    const data = kvRowsToData(rows)
    // An empty ConfigMap is legal — omit `data` entirely when no rows
    // carry a key.
    if (Object.keys(data).length > 0) obj.data = data
    if (immutableOn) obj.immutable = true
    return obj
  }, [name, namespace, rows, immutableOn])
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
        resource: 'configmaps',
        namespace,
      }
      // Server-side dry run first — same validation, nothing persisted.
      await k8sApi.createResource(manifest, params, true)
      await k8sApi.createResource(manifest, params)
      toast.success(`配置字典 ${n} 已创建`)
      void queryClient.invalidateQueries({
        queryKey: ['k8s', 'resources', clusterId, '', 'v1', 'configmaps'],
      })
      onCreated()
      onOpenChange(false)
    } catch (err) {
      const status = (err as { response?: { status?: number } }).response
        ?.status
      setError(status === 409 ? '同名配置字典已存在' : detail(err))
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
              <FileText className='size-5' />
            </span>
            <div className='space-y-1 text-start'>
              <DialogTitle className='text-[#1c355e]'>创建配置字典</DialogTitle>
              <DialogDescription>
                ConfigMap · 以明文键值对存储非机密配置
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
                placeholder='如 app-config'
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

          <div className='flex items-start justify-between gap-4 rounded-xl border border-[#e3eaf4] bg-[#fbfdff] px-4 py-3'>
            <div className='space-y-0.5'>
              <Label
                htmlFor='cm-create-immutable'
                className='text-xs font-medium text-[#5a6b85]'
              >
                创建后不可修改（immutable）
              </Label>
              <p className='text-xs text-muted-foreground'>
                开启后数据将不可再修改，如必须修改需删除重建
              </p>
            </div>
            <Switch
              id='cm-create-immutable'
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
              valuePlaceholder='配置值'
            />
            <p className='text-xs text-muted-foreground'>
              键值对写入 data 字段；留空则创建空 ConfigMap
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
