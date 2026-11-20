import { useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ChevronDown,
  ChevronRight,
  CircleAlert,
  HardDrive,
} from 'lucide-react'
import { toast } from 'sonner'
import * as YAML from 'yaml'
import { k8sApi, type K8sObject } from '@/api/client'
import { cn } from '@/lib/utils'
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
  SelectValue,
} from '@/components/ui/select'
import { OptionCard } from '@/components/option-card'

const NAME_RE = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/
// Sentinel for "不指定（使用集群默认）" — manifests then omit
// storageClassName entirely so the cluster's default class binds.
const NO_CLASS = '__cluster_default__'

const ACCESS_MODES: { value: string; desc: string }[] = [
  { value: 'ReadWriteOnce', desc: '单节点读写' },
  { value: 'ReadOnlyMany', desc: '多节点只读' },
  { value: 'ReadWriteMany', desc: '多节点读写' },
  { value: 'ReadWriteOncePod', desc: '单个 Pod 读写' },
]
const VOLUME_MODES: { value: string; label: string }[] = [
  { value: 'Filesystem', label: '文件系统' },
  { value: 'Block', label: '块设备' },
]
const UNITS = ['Mi', 'Gi', 'Ti'] as const

const detail = (err: unknown): string =>
  (err as { response?: { data?: { detail?: string } } })?.response?.data
    ?.detail ?? (err instanceof Error ? err.message : '请求失败')

const isDefaultClass = (sc: K8sObject): boolean =>
  sc.metadata?.annotations?.['storageclass.kubernetes.io/is-default-class'] ===
  'true'

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

// Segmented control shared by unit / volume mode / inline selects —
// same pattern as the cluster dialog's 发布策略 switch.
function Segmented({
  ariaLabel,
  options,
  value,
  disabled,
  onChange,
}: {
  ariaLabel: string
  options: { value: string; label: string }[]
  value: string
  disabled?: boolean
  onChange: (v: string) => void
}) {
  return (
    <div
      role='radiogroup'
      aria-label={ariaLabel}
      className='inline-flex rounded-lg bg-[#eef2f8] p-1'
    >
      {options.map((o) => (
        <button
          key={o.value}
          type='button'
          role='radio'
          aria-checked={value === o.value}
          disabled={disabled}
          onClick={() => onChange(o.value)}
          className={cn(
            'rounded-md px-4 py-1.5 text-sm transition',
            'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[#234883]/40',
            'disabled:cursor-not-allowed disabled:opacity-50',
            value === o.value
              ? 'bg-white font-medium text-[#1c355e] shadow-sm'
              : 'text-[#5a6b85] hover:text-[#1c355e]'
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

export function PvcCreateDialog({
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
  const [scChoice, setScChoice] = useState<string | null>(null)
  const [accessMode, setAccessMode] = useState('ReadWriteOnce')
  const [capacity, setCapacity] = useState('1')
  const [unit, setUnit] = useState<(typeof UNITS)[number]>('Gi')
  const [volumeMode, setVolumeMode] = useState('Filesystem')
  const [showYaml, setShowYaml] = useState(false)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  const classes = useQuery({
    queryKey: ['k8s', 'resources', clusterId, 'storage.k8s.io', 'v1', 'storageclasses', namespace],
    queryFn: () =>
      k8sApi.resources({
        cluster_id: clusterId,
        group: 'storage.k8s.io',
        version: 'v1',
        resource: 'storageclasses',
      }),
    enabled: open && clusterId != null,
  })
  const classItems = classes.data?.data.items ?? []
  const defaultClass = classItems.find(isDefaultClass)
  // null = untouched → default class preselected (or 不指定 when none
  // exists / the list failed to load).
  const classChoice =
    scChoice ?? (defaultClass ? (defaultClass.metadata?.name ?? NO_CLASS) : NO_CLASS)
  const selectedClass = classItems.find(
    (c) => c.metadata?.name === classChoice
  )

  const manifestObj = useMemo(() => {
    const spec: Record<string, unknown> = {
      accessModes: [accessMode],
      resources: { requests: { storage: `${capacity}${unit}` } },
      volumeMode,
    }
    if (selectedClass) spec.storageClassName = selectedClass.metadata?.name
    return {
      apiVersion: 'v1',
      kind: 'PersistentVolumeClaim',
      metadata: { name: name.trim(), namespace },
      spec,
    }
  }, [accessMode, capacity, unit, volumeMode, selectedClass, name, namespace])
  const manifest = useMemo(() => YAML.stringify(manifestObj), [manifestObj])

  const submit = async () => {
    setError('')
    const n = name.trim()
    if (!NAME_RE.test(n) || n.length > 63) {
      setNameError('只能包含小写字母、数字和 -，且以字母或数字开头结尾')
      return
    }
    const size = Number(capacity)
    if (!Number.isFinite(size) || size <= 0) {
      setError('容量必须是大于 0 的数值')
      return
    }
    setPending(true)
    try {
      const params = {
        cluster_id: clusterId,
        version: 'v1',
        resource: 'persistentvolumeclaims',
        namespace,
      }
      // Server-side dry run first — same validation, nothing persisted.
      await k8sApi.createResource(manifest, params, true)
      await k8sApi.createResource(manifest, params)
      toast.success(`存储声明 ${n} 已创建`)
      void queryClient.invalidateQueries({
        queryKey: [
          'k8s',
          'resources',
          clusterId,
          '',
          'v1',
          'persistentvolumeclaims',
        ],
      })
      onCreated()
      onOpenChange(false)
    } catch (err) {
      const status = (err as { response?: { status?: number } }).response
        ?.status
      setError(status === 409 ? '同名存储声明已存在' : detail(err))
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
              <HardDrive className='size-5' />
            </span>
            <div className='space-y-1 text-start'>
              <DialogTitle className='text-[#1c355e]'>
                创建存储声明
              </DialogTitle>
              <DialogDescription>
                在命名空间 {namespace} 中申请持久化存储
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
                maxLength={63}
                className='font-mono'
                placeholder='如 data-pvc'
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
                <p className='text-xs text-muted-foreground'>
                  小写字母、数字和 -，≤63 字符
                </p>
              )}
            </Field>
            <Field label='存储类'>
              {classes.isPending ? (
                <Select disabled value={NO_CLASS}>
                  <SelectTrigger className='w-full min-w-0'>
                    <SelectValue placeholder='加载中…' />
                  </SelectTrigger>
                </Select>
              ) : classes.isError ? (
                <Input
                  aria-label='存储类名称'
                  placeholder='storageClassName（可选）'
                  value={scChoice === NO_CLASS || scChoice == null ? '' : scChoice}
                  onChange={(e) => setScChoice(e.target.value || NO_CLASS)}
                />
              ) : (
                <Select
                  value={classChoice}
                  onValueChange={(v) => setScChoice(v)}
                >
                  <SelectTrigger
                    aria-label='存储类'
                    className='w-full min-w-0'
                  >
                    <span
                      data-slot='select-value'
                      className='flex min-w-0 items-center gap-2'
                    >
                      <span className='truncate'>
                        {classChoice === NO_CLASS
                          ? '不指定（使用集群默认）'
                          : classChoice}
                      </span>
                      {selectedClass && isDefaultClass(selectedClass) && (
                        <Badge
                          variant='outline'
                          className='shrink-0 border-[#d6e1f0] bg-[#eef3fa] px-1.5 py-0 text-[10px] text-[#31598e]'
                        >
                          默认
                        </Badge>
                      )}
                    </span>
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={NO_CLASS}>
                      不指定（使用集群默认）
                    </SelectItem>
                    {classItems.map((sc) => {
                      const scName = sc.metadata?.name ?? ''
                      const provisioner = String(
                        (sc.spec as Record<string, unknown> | undefined)
                          ?.provisioner ?? ''
                      )
                      const vbm = String(
                        (sc.spec as Record<string, unknown> | undefined)
                          ?.volumeBindingMode ?? ''
                      )
                      return (
                        <SelectItem key={scName} value={scName}>
                          <span className='flex items-center gap-2'>
                            {scName}
                            {isDefaultClass(sc) && (
                              <Badge
                                variant='outline'
                                className='border-[#d6e1f0] bg-[#eef3fa] px-1.5 py-0 text-[10px] text-[#31598e]'
                              >
                                默认
                              </Badge>
                            )}
                            <span className='text-xs text-muted-foreground'>
                              {provisioner}
                              {vbm ? ` · ${vbm}` : ''}
                            </span>
                          </span>
                        </SelectItem>
                      )
                    })}
                  </SelectContent>
                </Select>
              )}
            </Field>
          </div>

          <Field label='读写模式'>
            <div
              role='radiogroup'
              aria-label='读写模式'
              className='grid gap-3 sm:grid-cols-2'
            >
              {ACCESS_MODES.map((m) => (
                <OptionCard
                  key={m.value}
                  icon={HardDrive}
                  title={m.value}
                  desc={m.desc}
                  selected={accessMode === m.value}
                  disabled={pending}
                  onClick={() => setAccessMode(m.value)}
                />
              ))}
            </div>
          </Field>

          <div className='grid gap-4 sm:grid-cols-2'>
            <Field label='容量'>
              <div className='flex flex-wrap items-center gap-3'>
                <Input
                  aria-label='容量'
                  type='number'
                  min={1}
                  step='any'
                  className='w-28'
                  value={capacity}
                  onChange={(e) => setCapacity(e.target.value)}
                />
                <Segmented
                  ariaLabel='容量单位'
                  options={UNITS.map((u) => ({ value: u, label: u }))}
                  value={unit}
                  onChange={(v) => setUnit(v as (typeof UNITS)[number])}
                />
              </div>
              <p className='text-xs text-muted-foreground'>
                例如 2Gi 表示 2 GiB 空间
              </p>
            </Field>
            <Field label='卷模式'>
              <Segmented
                ariaLabel='卷模式'
                options={VOLUME_MODES.map((m) => ({
                  value: m.value,
                  label: m.label,
                }))}
                value={volumeMode}
                onChange={setVolumeMode}
              />
            </Field>
          </div>

          <button
            type='button'
            className='flex items-center gap-1.5 text-xs font-medium text-[#31598e] transition hover:text-[#234883] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[#234883]/40'
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
            <p className='mb-3 flex w-full items-start gap-1.5 rounded-lg bg-[#fdf0f1] px-3 py-2 text-xs leading-5 text-[#a52f3d]'>
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
