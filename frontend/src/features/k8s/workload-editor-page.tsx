import { useId, useMemo, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ArrowLeft,
  Box,
  CheckCircle2,
  ChevronDown,
  Cpu,
  FolderInput,
  HardDrive,
  Info,
  ListTree,
  Network,
  Pencil,
  Plus,
  Save,
  Terminal,
  Trash2,
} from 'lucide-react'
import { toast } from 'sonner'
import * as YAML from 'yaml'
import { k8sApi, type K8sObject, type K8sObjectParams } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ThemeSwitch } from '@/components/theme-switch'
import { list } from './catalog'
import { errDetail, isConflict } from './errors'
import type { K8sDetailQuery } from './k8s-page'
import { useResourceCaps } from './use-capabilities'
import { useResourceDef } from './use-resource-def'

const WORKLOAD_RESOURCES = new Set([
  'deployments',
  'statefulsets',
  'daemonsets',
  'jobs',
  'cronjobs',
])

type RecordValue = Record<string, unknown>
type Search = Partial<K8sDetailQuery>
type UpdateDraft = (fn: (draft: K8sObject) => void) => void

const clone = <T,>(value: T): T => JSON.parse(JSON.stringify(value)) as T

const record = (value: unknown): RecordValue =>
  value != null && typeof value === 'object' && !Array.isArray(value)
    ? (value as RecordValue)
    : {}

const stringValue = (value: unknown): string =>
  typeof value === 'string' || typeof value === 'number' ? String(value) : ''

const stringMap = (value: unknown): Record<string, string> =>
  Object.fromEntries(
    Object.entries(record(value)).map(([key, item]) => [key, stringValue(item)])
  )

function podSpecOf(obj: K8sObject): RecordValue {
  const spec = record(obj.spec)
  if (obj.kind === 'CronJob') {
    const jobSpec = record(record(spec.jobTemplate).spec)
    const template = record(jobSpec.template)
    return record(template.spec)
  }
  const template = record(spec.template)
  return record(template.spec)
}

function updatePodSpec(obj: K8sObject, fn: (spec: RecordValue) => void) {
  const spec = record(obj.spec)
  obj.spec = spec
  if (obj.kind === 'CronJob') {
    const jobTemplate = record(spec.jobTemplate)
    const jobSpec = record(jobTemplate.spec)
    const template = record(jobSpec.template)
    const podSpec = record(template.spec)
    template.spec = podSpec
    jobSpec.template = template
    jobTemplate.spec = jobSpec
    spec.jobTemplate = jobTemplate
    fn(podSpec)
    return
  }
  const template = record(spec.template)
  const podSpec = record(template.spec)
  template.spec = podSpec
  spec.template = template
  fn(podSpec)
}

function templateMetadataOf(obj: K8sObject): RecordValue {
  const spec = record(obj.spec)
  if (obj.kind === 'CronJob') {
    const jobSpec = record(record(spec.jobTemplate).spec)
    const template = record(jobSpec.template)
    return record(template.metadata)
  }
  const template = record(spec.template)
  return record(template.metadata)
}

function updateTemplateMetadata(
  obj: K8sObject,
  fn: (metadata: RecordValue) => void
) {
  const spec = record(obj.spec)
  if (obj.kind === 'CronJob') {
    const jobTemplate = record(spec.jobTemplate)
    const jobSpec = record(jobTemplate.spec)
    const template = record(jobSpec.template)
    const metadata = record(template.metadata)
    template.metadata = metadata
    jobSpec.template = template
    jobTemplate.spec = jobSpec
    spec.jobTemplate = jobTemplate
    fn(metadata)
    return
  }
  const template = record(spec.template)
  const metadata = record(template.metadata)
  template.metadata = metadata
  spec.template = template
  fn(metadata)
}

function containersOf(obj: K8sObject, field: 'containers' | 'initContainers') {
  const values = podSpecOf(obj)[field]
  return Array.isArray(values) ? (values as RecordValue[]) : []
}

function updateContainer(
  obj: K8sObject,
  field: 'containers' | 'initContainers',
  index: number,
  fn: (container: RecordValue) => void
) {
  updatePodSpec(obj, (spec) => {
    const values = Array.isArray(spec[field])
      ? (spec[field] as RecordValue[])
      : []
    const container = record(values[index])
    values[index] = container
    spec[field] = values
    fn(container)
  })
}

function setOptional(target: RecordValue, key: string, value: unknown) {
  if (
    value == null ||
    value === '' ||
    (Array.isArray(value) && value.length === 0)
  ) {
    delete target[key]
  } else {
    target[key] = value
  }
}

function Field({
  label,
  children,
  hint,
}: {
  label: string
  children: React.ReactNode
  hint?: string
}) {
  return (
    <div className='flex flex-col gap-2 md:flex-row md:items-start'>
      <div className='md:w-40 md:shrink-0 md:pt-2'>
        <Label className='text-sm text-[#606266]'>{label}</Label>
        {hint && (
          <p className='mt-1 text-xs leading-4 text-[#64748b]'>{hint}</p>
        )}
      </div>
      <div className='min-w-0 flex-1'>{children}</div>
    </div>
  )
}

function ReadonlyValue({ children }: { children: React.ReactNode }) {
  return (
    <p className='font-mono text-sm font-semibold break-all text-[#1c355e] md:pt-2'>
      {children}
    </p>
  )
}

function SectionCard({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children: React.ReactNode
}) {
  return (
    <Card className='gap-0 rounded-xl border-[#dbe4f0] bg-white py-0 shadow-sm'>
      <CardHeader className='border-b border-[#edf1f7] bg-[#f8fafc] px-4 py-4 sm:px-6 sm:py-5'>
        <CardTitle className='text-base font-semibold text-[#1c355e]'>
          {title}
        </CardTitle>
        {description && (
          <p className='text-sm leading-5 text-[#536987]'>{description}</p>
        )}
      </CardHeader>
      <CardContent className='space-y-5 px-4 py-5 sm:px-6 sm:py-6'>
        {children}
      </CardContent>
    </Card>
  )
}

function KeyValueEditor({
  label,
  values,
  disabled,
  onChange,
}: {
  label: string
  values: Record<string, string>
  disabled: boolean
  onChange: (values: Record<string, string>) => void
}) {
  const entries = Object.entries(values)
  const update = (index: number, key: 'name' | 'value', value: string) => {
    const next = entries.map(([name, item]) => ({ name, value: item }))
    next[index][key] = value
    const output: Record<string, string> = {}
    for (const item of next)
      if (item.name.trim()) output[item.name.trim()] = item.value
    onChange(output)
  }
  return (
    <div className='space-y-3 rounded-lg border border-[#ebeef5] bg-[#f8fafc] p-3'>
      <div className='flex items-center justify-between gap-2'>
        <span className='text-sm font-semibold text-[#536987]'>{label}</span>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          disabled={disabled}
          className='h-11 px-2 text-xs text-[#31598e] sm:h-9'
          onClick={() =>
            onChange({
              ...values,
              [`key-${Object.keys(values).length + 1}`]: '',
            })
          }
        >
          <Plus className='size-3.5' /> 添加
        </Button>
      </div>
      {entries.length === 0 ? (
        <p className='text-sm text-[#64748b]'>暂无条目，点击「添加」新增</p>
      ) : (
        entries.map(([name, value], index) => (
          <div
            key={`${name}-${index}`}
            className='flex flex-wrap items-center gap-2'
          >
            <Input
              aria-label={`${label} 键 ${index + 1}`}
              value={name}
              disabled={disabled}
              placeholder='键'
              className='h-11 w-full min-w-0 font-mono text-sm sm:h-9 sm:w-auto sm:min-w-36 sm:flex-1'
              onChange={(event) => update(index, 'name', event.target.value)}
            />
            <Input
              aria-label={`${label} 值 ${index + 1}`}
              value={value}
              disabled={disabled}
              placeholder='值'
              className='h-11 min-w-0 flex-1 font-mono text-sm sm:h-9 sm:min-w-36'
              onChange={(event) => update(index, 'value', event.target.value)}
            />
            <Button
              type='button'
              variant='ghost'
              size='icon'
              disabled={disabled}
              aria-label={`删除${label} ${name || index + 1}`}
              className='size-11 text-[#9b6571] hover:bg-[#fdf0f1] hover:text-[#c2404e] sm:size-9'
              onClick={() => {
                const next = entries.filter(
                  (_, itemIndex) => itemIndex !== index
                )
                onChange(Object.fromEntries(next))
              }}
            >
              <Trash2 className='size-4' />
            </Button>
          </div>
        ))
      )}
    </div>
  )
}

function TextListField({
  label,
  value,
  disabled,
  onChange,
}: {
  label: string
  value: unknown
  disabled: boolean
  onChange: (values: string[]) => void
}) {
  const text = Array.isArray(value) ? value.map(stringValue).join('\n') : ''
  return (
    <ContainerField label={label} hint='每行一项，留空使用镜像的默认配置'>
      <Textarea
        aria-label={label}
        value={text}
        disabled={disabled}
        rows={3}
        placeholder={
          label === '启动命令'
            ? '例如：/bin/sh\n-c'
            : '例如：--config=/etc/app/config.yaml'
        }
        className='min-h-24 resize-y bg-background font-mono text-sm shadow-none'
        onChange={(event) =>
          onChange(
            event.target.value.split(/\r?\n/).filter((item) => item.trim())
          )
        }
      />
    </ContainerField>
  )
}

const GPU_VENDORS = ['nvidia.com/gpu', 'amd.com/gpu']
const COMMON_RESOURCE_KEYS = new Set(['cpu', 'memory', ...GPU_VENDORS])

// Clones `resources`, writes one requests/limits key, prunes empty maps.
// Unknown siblings (claims, resizePolicy, arbitrary keys) stay untouched.
function patchResourceMap(
  resources: RecordValue,
  kind: 'requests' | 'limits',
  key: string,
  value: string | undefined
): RecordValue | undefined {
  const next = clone(resources)
  const inner = { ...record(next[kind]) }
  setOptional(inner, key, value === '' ? undefined : value)
  if (Object.keys(inner).length) next[kind] = inner
  else delete next[kind]
  return Object.keys(next).length ? next : undefined
}

const RESOURCE_INPUT_CLASS =
  'h-11 min-w-0 bg-background font-mono text-sm shadow-none sm:h-9'
const RESOURCE_SELECT_CLASS =
  'h-11 rounded-md border border-input bg-background px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 sm:h-9'

function ContainerResourcesEditor({
  resources,
  disabled,
  onChange,
}: {
  resources: RecordValue
  disabled: boolean
  onChange: (next: RecordValue | undefined) => void
}) {
  const [adding, setAdding] = useState(false)
  const [draftName, setDraftName] = useState('')
  const [draftKind, setDraftKind] = useState<'requests' | 'limits'>('requests')
  const [draftValue, setDraftValue] = useState('')
  const [addError, setAddError] = useState('')
  const [gpuDraft, setGpuDraft] = useState<string | null>(null)
  const [gpuError, setGpuError] = useState('')
  const requests = record(resources.requests)
  const limits = record(resources.limits)
  const reqGpuKey = GPU_VENDORS.find((key) => requests[key] !== undefined)
  const limGpuKey = GPU_VENDORS.find((key) => limits[key] !== undefined)
  const gpuKeys = GPU_VENDORS.filter(
    (key) => requests[key] !== undefined || limits[key] !== undefined
  )
  // More than one recognized GPU key across either map means the manifest
  // mixes vendors; preserve it until the user picks one explicitly.
  const gpuVendor =
    gpuKeys.length > 1 ? 'mixed' : (limGpuKey ?? reqGpuKey ?? 'none')
  const gpuMismatch =
    gpuVendor !== 'none' &&
    gpuVendor !== 'mixed' &&
    reqGpuKey !== undefined &&
    stringValue(requests[gpuVendor]) !== stringValue(limits[gpuVendor])
  const otherKeys = Array.from(
    new Set([...Object.keys(requests), ...Object.keys(limits)])
  ).filter((key) => !COMMON_RESOURCE_KEYS.has(key))

  // Writes pruned resources back; drops resources/requests/limits when empty.
  const emit = (next: RecordValue) => {
    const nextRequests = record(next.requests)
    const nextLimits = record(next.limits)
    if (Object.keys(nextRequests).length) next.requests = nextRequests
    else delete next.requests
    if (Object.keys(nextLimits).length) next.limits = nextLimits
    else delete next.limits
    onChange(Object.keys(next).length ? next : undefined)
  }
  const patchRequests = (key: string, value: string) =>
    onChange(patchResourceMap(resources, 'requests', key, value))
  const patchLimits = (key: string, value: string) =>
    onChange(patchResourceMap(resources, 'limits', key, value))
  const applyGpuVendor = (vendor: string) => {
    // The count draft belongs to the previous vendor; clear it on switch.
    setGpuDraft(null)
    setGpuError('')
    const priorAmount = limGpuKey ? stringValue(limits[limGpuKey]) : ''
    const hadGpuRequest = reqGpuKey !== undefined
    const next = clone(resources)
    // Install real maps on `next` first; emit() prunes empties afterwards.
    const nextRequests = { ...record(next.requests) }
    const nextLimits = { ...record(next.limits) }
    for (const key of GPU_VENDORS) {
      delete nextRequests[key]
      delete nextLimits[key]
    }
    if (vendor !== 'none') {
      const amount = priorAmount || '1'
      nextLimits[vendor] = amount
      if (hadGpuRequest) nextRequests[vendor] = amount
    }
    next.requests = nextRequests
    next.limits = nextLimits
    emit(next)
  }
  // Commits a validated positive-integer GPU count; mirrors requests only
  // when it already declared the GPU key.
  const writeGpuCount = (count: number) => {
    if (gpuVendor === 'none' || gpuVendor === 'mixed') return
    const next = clone(resources)
    const nextRequests = { ...record(next.requests) }
    const nextLimits = { ...record(next.limits) }
    nextLimits[gpuVendor] = String(count)
    if (nextRequests[gpuVendor] !== undefined)
      nextRequests[gpuVendor] = String(count)
    next.requests = nextRequests
    next.limits = nextLimits
    emit(next)
  }
  const gpuCountChange = (raw: string) => {
    setGpuDraft(raw)
    const trimmed = raw.trim()
    if (trimmed === '') {
      setGpuError('')
      return
    }
    const count = Number(trimmed)
    if (!Number.isInteger(count) || count < 1) {
      setGpuError('请输入正整数卡数')
      return
    }
    setGpuError('')
    setGpuDraft(null)
    writeGpuCount(count)
  }
  const gpuCountBlur = () => {
    if (gpuDraft == null) return
    // Empty on blur deletes the GPU from both maps; invalid input reverts.
    if (gpuDraft.trim() === '') applyGpuVendor('none')
    setGpuDraft(null)
    setGpuError('')
  }
  const removeOtherKey = (key: string) => {
    const next = clone(resources)
    const nextRequests = { ...record(next.requests) }
    const nextLimits = { ...record(next.limits) }
    delete nextRequests[key]
    delete nextLimits[key]
    next.requests = nextRequests
    next.limits = nextLimits
    emit(next)
  }
  const confirmAdd = () => {
    const name = draftName.trim()
    const value = draftValue.trim()
    if (!name) return setAddError('请输入资源名称')
    if (!value) return setAddError('请输入资源数量')
    if (COMMON_RESOURCE_KEYS.has(name))
      return setAddError('该资源请使用上方 CPU、内存或 GPU 专属输入')
    if (record(resources[draftKind])[name] !== undefined)
      return setAddError(
        draftKind === 'requests' ? '请求中已存在该资源' : '上限中已存在该资源'
      )
    onChange(patchResourceMap(resources, draftKind, name, value))
    setAdding(false)
    setDraftName('')
    setDraftKind('requests')
    setDraftValue('')
    setAddError('')
  }
  return (
    <div className='space-y-4'>
      <ContainerField label='CPU' hint='例如：100m 或 0.1'>
        <div className='grid gap-2 sm:grid-cols-2'>
          <div className='space-y-1'>
            <span className='text-xs text-[#64748b]'>请求</span>
            <Input
              aria-label='CPU 请求'
              placeholder='例如 100m'
              value={stringValue(requests.cpu)}
              disabled={disabled}
              className={RESOURCE_INPUT_CLASS}
              onChange={(event) => patchRequests('cpu', event.target.value)}
            />
          </div>
          <div className='space-y-1'>
            <span className='text-xs text-[#64748b]'>上限</span>
            <Input
              aria-label='CPU 限制'
              placeholder='例如 500m'
              value={stringValue(limits.cpu)}
              disabled={disabled}
              className={RESOURCE_INPUT_CLASS}
              onChange={(event) => patchLimits('cpu', event.target.value)}
            />
          </div>
        </div>
      </ContainerField>
      <ContainerField label='内存' hint='例如：128Mi 或 2Gi'>
        <div className='grid gap-2 sm:grid-cols-2'>
          <div className='space-y-1'>
            <span className='text-xs text-[#64748b]'>请求</span>
            <Input
              aria-label='内存请求'
              placeholder='例如 128Mi'
              value={stringValue(requests.memory)}
              disabled={disabled}
              className={RESOURCE_INPUT_CLASS}
              onChange={(event) => patchRequests('memory', event.target.value)}
            />
          </div>
          <div className='space-y-1'>
            <span className='text-xs text-[#64748b]'>上限</span>
            <Input
              aria-label='内存限制'
              placeholder='例如 2Gi'
              value={stringValue(limits.memory)}
              disabled={disabled}
              className={RESOURCE_INPUT_CLASS}
              onChange={(event) => patchLimits('memory', event.target.value)}
            />
          </div>
        </div>
      </ContainerField>
      <ContainerField label='GPU' hint='需要集群已配置对应厂商设备插件'>
        <div className='space-y-3'>
          <div className='flex flex-wrap items-center gap-3'>
            <select
              aria-label='GPU 类型'
              value={gpuVendor}
              disabled={disabled}
              className={`${RESOURCE_SELECT_CLASS} w-56`}
              onChange={(event) => applyGpuVendor(event.target.value)}
            >
              <option value='none'>不使用 GPU</option>
              <option value='nvidia.com/gpu'>NVIDIA（nvidia.com/gpu）</option>
              <option value='amd.com/gpu'>AMD（amd.com/gpu）</option>
              {gpuVendor === 'mixed' && (
                <option value='mixed' disabled>
                  已有多种 GPU 资源
                </option>
              )}
            </select>
            {gpuVendor !== 'none' && gpuVendor !== 'mixed' && (
              <div className='flex items-center gap-2'>
                <Input
                  type='number'
                  min={1}
                  step={1}
                  aria-label='GPU 限制'
                  value={gpuDraft ?? stringValue(limits[gpuVendor])}
                  disabled={disabled}
                  className={`${RESOURCE_INPUT_CLASS} w-28`}
                  onChange={(event) => gpuCountChange(event.target.value)}
                  onBlur={gpuCountBlur}
                />
                <span className='text-xs text-[#64748b]'>卡数</span>
              </div>
            )}
          </div>
          {gpuError && <p className='text-sm text-[#c2404e]'>{gpuError}</p>}
          {gpuVendor === 'mixed' && (
            <p className='flex items-center gap-1.5 text-sm text-[#94621c]'>
              <Info className='size-3.5' />
              已有多种 GPU 资源；选择类型后将合并为所选厂商，其余字段保持不变。
            </p>
          )}
          {gpuMismatch && (
            <p className='flex items-center gap-1.5 text-sm text-[#94621c]'>
              <Info className='size-3.5' />
              请求与上限的 GPU 数量不一致，Kubernetes
              要求两者相同；修改数量后将自动同步。
            </p>
          )}
        </div>
      </ContainerField>
      <ContainerField
        label='其他资源'
        hint='requests、limits 中 CPU、内存、GPU 之外的扩展资源'
      >
        <div className='space-y-3'>
          <div className='flex items-center justify-end gap-2'>
            <Button
              type='button'
              variant='ghost'
              size='sm'
              disabled={disabled || adding}
              aria-label='添加其他资源'
              className='h-11 px-2 text-xs text-[#31598e] sm:h-9'
              onClick={() => {
                setAdding(true)
                setAddError('')
              }}
            >
              <Plus className='size-3.5' /> 添加其他资源
            </Button>
          </div>
          {otherKeys.map((key) => (
            <div key={key} className='flex flex-wrap items-center gap-2'>
              <span
                title={key}
                className='w-full shrink-0 truncate font-mono text-sm text-[#1c355e] sm:w-40'
              >
                {key}
              </span>
              <div className='min-w-0 flex-1 space-y-1'>
                <span className='text-xs text-[#64748b]'>请求</span>
                <Input
                  aria-label={`${key} 请求`}
                  placeholder='请求'
                  value={stringValue(requests[key])}
                  disabled={disabled}
                  className={RESOURCE_INPUT_CLASS}
                  onChange={(event) => patchRequests(key, event.target.value)}
                />
              </div>
              <div className='min-w-0 flex-1 space-y-1'>
                <span className='text-xs text-[#64748b]'>上限</span>
                <Input
                  aria-label={`${key} 限制`}
                  placeholder='上限'
                  value={stringValue(limits[key])}
                  disabled={disabled}
                  className={RESOURCE_INPUT_CLASS}
                  onChange={(event) => patchLimits(key, event.target.value)}
                />
              </div>
              <Button
                type='button'
                variant='ghost'
                size='icon'
                disabled={disabled}
                aria-label={`删除资源 ${key}`}
                className='size-11 shrink-0 text-[#9b6571] hover:bg-[#fdf0f1] hover:text-[#c2404e] sm:size-9'
                onClick={() => removeOtherKey(key)}
              >
                <Trash2 className='size-4' />
              </Button>
            </div>
          ))}
          {adding && (
            <div className='flex flex-wrap items-center gap-2 rounded-md bg-[#f8fafc] p-2'>
              <Input
                aria-label='新资源名称'
                placeholder='资源名，例如 example.com/foo'
                value={draftName}
                disabled={disabled}
                className={`${RESOURCE_INPUT_CLASS} flex-1 sm:w-56 sm:flex-none`}
                onChange={(event) => setDraftName(event.target.value)}
              />
              <select
                aria-label='新资源写入位置'
                value={draftKind}
                disabled={disabled}
                className={`${RESOURCE_SELECT_CLASS} w-24`}
                onChange={(event) =>
                  setDraftKind(event.target.value as 'requests' | 'limits')
                }
              >
                <option value='requests'>请求</option>
                <option value='limits'>上限</option>
              </select>
              <Input
                aria-label='新资源数量'
                placeholder='数量'
                value={draftValue}
                disabled={disabled}
                className={`${RESOURCE_INPUT_CLASS} flex-1`}
                onChange={(event) => setDraftValue(event.target.value)}
              />
              <Button
                type='button'
                variant='outline'
                size='sm'
                disabled={disabled}
                className='h-11 sm:h-9'
                onClick={confirmAdd}
              >
                确认添加
              </Button>
              <Button
                type='button'
                variant='ghost'
                size='sm'
                disabled={disabled}
                className='h-11 sm:h-9'
                onClick={() => {
                  setAdding(false)
                  setAddError('')
                }}
              >
                取消
              </Button>
            </div>
          )}
          {addError && <p className='text-sm text-[#c2404e]'>{addError}</p>}
          {otherKeys.length === 0 && !adding && (
            <p className='text-sm text-[#64748b]'>暂无其他资源</p>
          )}
        </div>
      </ContainerField>
      <p className='text-xs text-[#64748b]'>
        未单独设置时，可能沿用名称空间 LimitRange
        默认值；数量按原样保存，不会自动转换单位。
      </p>
    </div>
  )
}

// Kubernetes allows newer printable env names (dots/hyphens); only reject
// blanks, '=' and control characters — the server Dry Run validates fully.
// eslint-disable-next-line no-control-regex -- detecting control chars is the point
const ENV_NAME_BAD_RE = /[\x00-\x1f\x7f=]/

const ENV_SOURCE_TYPES = [
  'value',
  'fieldRef',
  'resourceFieldRef',
  'configMapKeyRef',
  'secretKeyRef',
]

const ENV_SOURCE_LABELS: Record<string, string> = {
  value: '值（字面量）',
  fieldRef: 'Pod 字段',
  resourceFieldRef: '资源请求/限制',
  configMapKeyRef: 'ConfigMap Key',
  secretKeyRef: 'Secret Key',
}

const ENV_FIELD_PATHS = [
  'metadata.name',
  'metadata.namespace',
  'spec.nodeName',
  'spec.serviceAccountName',
  'status.podIP',
  'status.hostIP',
]

const ENV_RESOURCE_PATHS = [
  'limits.cpu',
  'limits.memory',
  'requests.cpu',
  'requests.memory',
]

// An env var's source type comes from its sole recognized valueFrom key;
// anything else (unknown, multiple, or an empty valueFrom object) renders
// honestly as 'unknown'.
const envSourceType = (item: RecordValue): string => {
  if (item.valueFrom == null) return 'value'
  const keys = Object.keys(record(item.valueFrom))
  return keys.length === 1 && ENV_SOURCE_TYPES.includes(keys[0])
    ? keys[0]
    : 'unknown'
}

// Exactly one recognized source key (besides `prefix`) is required;
// both refs or extra keys render honestly as 'unknown'.
const envFromType = (item: RecordValue): string => {
  const keys = Object.keys(item).filter((key) => key !== 'prefix')
  const recognized = keys.filter(
    (key) => key === 'configMapRef' || key === 'secretRef'
  )
  return keys.length === 1 && recognized.length === 1 ? keys[0] : 'unknown'
}

const ENV_ROW_INPUT =
  'h-11 min-w-0 bg-background font-mono text-sm shadow-none sm:h-9'
const ENV_ROW_SELECT =
  'h-11 w-full rounded-md border border-input bg-background px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 sm:h-9'

function EnvField({
  label,
  className,
  children,
}: {
  label: string
  className?: string
  children: ReactNode
}) {
  return (
    <div className={`min-w-0 space-y-1 ${className ?? ''}`}>
      <span className='text-xs text-muted-foreground'>{label}</span>
      {children}
    </div>
  )
}

function OptionalTriState({
  ariaLabel,
  value,
  disabled,
  onChange,
}: {
  ariaLabel: string
  value: unknown
  disabled: boolean
  onChange: (next: boolean | undefined) => void
}) {
  return (
    <select
      aria-label={ariaLabel}
      value={value === undefined ? '' : String(value)}
      disabled={disabled}
      className={ENV_ROW_SELECT}
      onChange={(event) =>
        onChange(
          event.target.value === '' ? undefined : event.target.value === 'true'
        )
      }
    >
      <option value=''>继承</option>
      <option value='true'>启用</option>
      <option value='false'>禁用</option>
    </select>
  )
}

type ResourceListResult = {
  items: K8sObject[]
  names: string[]
  isLoading: boolean
  isError: boolean
}

// Lists a cluster resource so reference fields (ConfigMap / Secret / PVC /
// ServiceAccount / PriorityClass names) can be chosen from a dropdown.
// List failures (e.g. RBAC denies) never block the form — the picker
// falls back to manual input.
function useResourceList(
  params: K8sObjectParams,
  resource: string,
  options?: {
    group?: string
    version?: string
    clusterScoped?: boolean
    enabled?: boolean
  }
): ResourceListResult {
  const namespaced = options?.clusterScoped !== true
  const query = useQuery({
    queryKey: [
      'k8s',
      'ref-list',
      params.cluster_id,
      options?.group ?? '',
      options?.version ?? 'v1',
      resource,
      namespaced ? (params.namespace ?? '') : '',
    ],
    queryFn: () =>
      k8sApi.resources({
        cluster_id: params.cluster_id,
        group: options?.group,
        version: options?.version ?? 'v1',
        resource,
        namespace: namespaced ? params.namespace || undefined : undefined,
      }),
    enabled:
      (options?.enabled ?? true) &&
      Boolean(params.cluster_id) &&
      (!namespaced || Boolean(params.namespace)),
    retry: false,
    staleTime: 30_000,
  })
  const items = query.data?.data.items ?? []
  return {
    items,
    names: items
      .map((item) => stringValue(record(item.metadata).name))
      .filter(Boolean),
    isLoading: query.fetchStatus === 'fetching',
    isError: query.isError,
  }
}

// data/binaryData/stringData keys of the named object — used to suggest
// keys for configMapKeyRef / secretKeyRef / KeyToPath without ever
// reading the values themselves.
const resourceDataKeys = (items: K8sObject[], name: string): string[] => {
  const selected = items.find(
    (item) => stringValue(record(item.metadata).name) === name
  )
  if (!selected) return []
  return [
    ...new Set([
      ...Object.keys(record(selected.data)),
      ...Object.keys(record(selected.binaryData)),
      ...Object.keys(record(selected.stringData)),
    ]),
  ].sort()
}

const MANUAL_ENTRY = '__manual__'

// Cluster resource picker: a real dropdown once the list loads, with a
// "手动输入" escape hatch that is applied automatically when the list is
// empty or unavailable. A current value missing from the list is kept as
// a marked option so the form never silently clears it.
function ResourceNameSelect({
  ariaLabel,
  value,
  options,
  disabled,
  loading = false,
  loadFailed = false,
  allowManual = true,
  placeholder = '请选择',
  selectClassName = ENV_ROW_SELECT,
  inputClassName = ENV_ROW_INPUT,
  onChange,
}: {
  ariaLabel: string
  value: string
  options: string[]
  disabled: boolean
  loading?: boolean
  loadFailed?: boolean
  allowManual?: boolean
  placeholder?: string
  selectClassName?: string
  inputClassName?: string
  onChange: (next: string) => void
}) {
  const [manual, setManual] = useState(false)
  const listId = useId()
  const showInput =
    allowManual &&
    (manual || (!loading && (loadFailed || options.length === 0)))
  if (showInput)
    return (
      <div className='flex min-w-0 flex-1 items-center gap-1'>
        <Input
          aria-label={ariaLabel}
          value={value}
          disabled={disabled}
          list={options.length ? listId : undefined}
          placeholder={loadFailed ? '列表不可用，请手动输入' : placeholder}
          className={`${inputClassName} min-w-0 flex-1`}
          onChange={(event) => onChange(event.target.value)}
        />
        {options.length > 0 && (
          <datalist id={listId}>
            {options.map((name) => (
              <option key={name} value={name} />
            ))}
          </datalist>
        )}
        {options.length > 0 && !disabled && (
          <button
            type='button'
            aria-label={`${ariaLabel} 改为下拉选择`}
            className='h-11 shrink-0 rounded px-1 text-xs whitespace-nowrap text-[#31598e] underline underline-offset-2 outline-none focus-visible:ring-2 focus-visible:ring-[#234883]/40 sm:h-9'
            onClick={() => setManual(false)}
          >
            下拉选择
          </button>
        )}
      </div>
    )
  return (
    <select
      aria-label={ariaLabel}
      value={value}
      disabled={disabled}
      className={selectClassName}
      onChange={(event) => {
        if (event.target.value === MANUAL_ENTRY) {
          setManual(true)
          return
        }
        onChange(event.target.value)
      }}
    >
      <option value=''>
        {loading ? '加载中…' : loadFailed ? '列表不可用' : placeholder}
      </option>
      {value !== '' && !options.includes(value) && (
        <option value={value}>{value}（不在列表中）</option>
      )}
      {options.map((name) => (
        <option key={name} value={name}>
          {name}
        </option>
      ))}
      {allowManual && !disabled && (
        <option value={MANUAL_ENTRY}>手动输入…</option>
      )}
    </select>
  )
}

function ContainerEnvironmentEditor({
  env,
  envFrom,
  params,
  disabled,
  onEnvChange,
  onEnvFromChange,
}: {
  env: RecordValue[]
  envFrom: RecordValue[]
  params: K8sObjectParams
  disabled: boolean
  onEnvChange: (next: RecordValue[] | undefined) => void
  onEnvFromChange: (next: RecordValue[] | undefined) => void
}) {
  const configMaps = useResourceList(params, 'configmaps')
  const secrets = useResourceList(params, 'secrets')
  const refList = (source: string) =>
    source === 'secretKeyRef' || source === 'secretRef' ? secrets : configMaps
  const [envExpanded, setEnvExpanded] = useState(true)
  const envContentId = useId()
  const [envAdding, setEnvAdding] = useState(false)
  const [envName, setEnvName] = useState('')
  const [envType, setEnvType] = useState('value')
  const [envValue, setEnvValue] = useState('')
  const [envFieldPath, setEnvFieldPath] = useState('')
  const [envResource, setEnvResource] = useState('')
  const [envDivisor, setEnvDivisor] = useState('')
  const [envRefName, setEnvRefName] = useState('')
  const [envRefKey, setEnvRefKey] = useState('')
  const [envRefOptional, setEnvRefOptional] = useState<boolean | undefined>(
    undefined
  )
  const [envError, setEnvError] = useState('')
  const [fromAdding, setFromAdding] = useState(false)
  const [fromType, setFromType] = useState<'configMapRef' | 'secretRef'>(
    'configMapRef'
  )
  const [fromName, setFromName] = useState('')
  const [fromPrefix, setFromPrefix] = useState('')
  const [fromOptional, setFromOptional] = useState<boolean | undefined>(
    undefined
  )
  const [fromError, setFromError] = useState('')

  const patchEnv = (index: number, fn: (item: RecordValue) => void) => {
    const next = clone(env)
    const item = record(next[index])
    next[index] = item
    fn(item)
    onEnvChange(next.length ? next : undefined)
  }
  // Merges a change into the item's valueFrom branch, preserving its
  // unknown siblings (apiVersion, containerName, optional, ...).
  const patchEnvSource = (
    index: number,
    source: string,
    fn: (target: RecordValue) => void
  ) =>
    patchEnv(index, (item) => {
      const valueFrom = record(item.valueFrom)
      const target = record(valueFrom[source])
      fn(target)
      setOptional(valueFrom, source, target)
      setOptional(
        item,
        'valueFrom',
        Object.keys(valueFrom).length ? valueFrom : undefined
      )
    })
  const switchEnvSource = (index: number, source: string) =>
    patchEnv(index, (item) => {
      delete item.value
      delete item.valueFrom
      if (source === 'value') return
      item.valueFrom = {
        [source]:
          source === 'fieldRef'
            ? { fieldPath: '' }
            : source === 'resourceFieldRef'
              ? { resource: '' }
              : { name: '', key: '' },
      }
    })
  const patchEnvFrom = (index: number, fn: (item: RecordValue) => void) => {
    const next = clone(envFrom)
    const item = record(next[index])
    next[index] = item
    fn(item)
    onEnvFromChange(next.length ? next : undefined)
  }
  const patchEnvFromRef = (
    index: number,
    ref: 'configMapRef' | 'secretRef',
    fn: (target: RecordValue) => void
  ) =>
    patchEnvFrom(index, (item) => {
      const target = record(item[ref])
      fn(target)
      setOptional(item, ref, target)
    })
  const switchEnvFromType = (
    index: number,
    ref: 'configMapRef' | 'secretRef'
  ) =>
    // Explicit switch replaces the whole source: drop every key except
    // `prefix`, then install only the selected ref so the row becomes
    // editable again (unknown keys would keep it classified unknown).
    patchEnvFrom(index, (item) => {
      for (const key of Object.keys(item))
        if (key !== 'prefix') delete item[key]
      item[ref] = { name: '' }
    })

  const resetEnvDraft = () => {
    setEnvAdding(false)
    setEnvName('')
    setEnvType('value')
    setEnvValue('')
    setEnvFieldPath('')
    setEnvResource('')
    setEnvDivisor('')
    setEnvRefName('')
    setEnvRefKey('')
    setEnvRefOptional(undefined)
    setEnvError('')
  }
  const confirmEnv = () => {
    const name = envName.trim()
    if (!name || ENV_NAME_BAD_RE.test(name))
      return setEnvError('变量名不能为空，且不能包含 = 或控制字符')
    if (env.some((item) => stringValue(item.name) === name))
      return setEnvError('变量名已存在')
    const item: RecordValue = { name }
    if (envType === 'value') {
      item.value = envValue
    } else {
      const ref: RecordValue = {}
      if (envType === 'fieldRef') {
        if (!envFieldPath.trim()) return setEnvError('请输入字段路径')
        ref.fieldPath = envFieldPath.trim()
      } else if (envType === 'resourceFieldRef') {
        if (!envResource.trim())
          return setEnvError('请输入资源名，例如 limits.cpu')
        ref.resource = envResource.trim()
        if (envDivisor.trim()) ref.divisor = envDivisor.trim()
      } else {
        if (!envRefName.trim() || !envRefKey.trim())
          return setEnvError('请输入名称和 Key')
        ref.name = envRefName.trim()
        ref.key = envRefKey.trim()
        if (envRefOptional !== undefined) ref.optional = envRefOptional
      }
      item.valueFrom = { [envType]: ref }
    }
    onEnvChange([...env, item])
    resetEnvDraft()
  }
  const confirmEnvFrom = () => {
    const name = fromName.trim()
    if (!name) return setFromError('请输入来源名称')
    const ref: RecordValue = { name }
    if (fromOptional !== undefined) ref.optional = fromOptional
    const item: RecordValue = { [fromType]: ref }
    if (fromPrefix.trim()) item.prefix = fromPrefix.trim()
    onEnvFromChange([...envFrom, item])
    setFromAdding(false)
    setFromType('configMapRef')
    setFromName('')
    setFromPrefix('')
    setFromOptional(undefined)
    setFromError('')
  }

  const renderEnvSourceFields = (item: RecordValue, index: number) => {
    const type = envSourceType(item)
    const valueFrom = record(item.valueFrom)
    if (type === 'value')
      return (
        <EnvField label='值' className='w-full'>
          <Input
            aria-label={`环境变量 ${index + 1} 值`}
            value={stringValue(item.value)}
            disabled={disabled}
            className={`${ENV_ROW_INPUT} w-full`}
            onChange={(event) =>
              // An empty literal value is valid Kubernetes; keep value: ''
              // instead of deleting the property.
              patchEnv(index, (target) => {
                target.value = event.target.value
              })
            }
          />
        </EnvField>
      )
    if (type === 'unknown')
      return (
        <p className='flex w-full items-center gap-1.5 text-sm text-[#94621c]'>
          <Info className='size-3.5' />
          暂不支持表单编辑的 valueFrom 类型，原样保留；可直接修改类型切换。
        </p>
      )
    if (type === 'fieldRef')
      return (
        <EnvField label='字段路径 fieldPath' className='w-full'>
          <Input
            aria-label={`环境变量 ${index + 1} 字段路径`}
            value={stringValue(record(valueFrom.fieldRef).fieldPath)}
            disabled={disabled}
            list='env-fieldpath-suggestions'
            className={`${ENV_ROW_INPUT} w-full`}
            onChange={(event) =>
              patchEnvSource(index, 'fieldRef', (target) =>
                setOptional(target, 'fieldPath', event.target.value)
              )
            }
          />
        </EnvField>
      )
    if (type === 'resourceFieldRef')
      return (
        <>
          <EnvField label='资源 resource' className='flex-1'>
            <Input
              aria-label={`环境变量 ${index + 1} 资源`}
              value={stringValue(record(valueFrom.resourceFieldRef).resource)}
              disabled={disabled}
              list='env-resource-suggestions'
              className={ENV_ROW_INPUT}
              onChange={(event) =>
                patchEnvSource(index, 'resourceFieldRef', (target) =>
                  setOptional(target, 'resource', event.target.value)
                )
              }
            />
          </EnvField>
          <EnvField label='除数 divisor' className='w-36'>
            <Input
              aria-label={`环境变量 ${index + 1} 除数`}
              value={stringValue(record(valueFrom.resourceFieldRef).divisor)}
              disabled={disabled}
              placeholder='例如 1m'
              className={ENV_ROW_INPUT}
              onChange={(event) =>
                patchEnvSource(index, 'resourceFieldRef', (target) =>
                  setOptional(target, 'divisor', event.target.value)
                )
              }
            />
          </EnvField>
        </>
      )
    // configMapKeyRef / secretKeyRef
    const kindLabel = type === 'configMapKeyRef' ? 'ConfigMap' : 'Secret'
    const refOptions = refList(type)
    const refName = stringValue(record(valueFrom[type]).name)
    return (
      <>
        <EnvField label={`${kindLabel} 名称`} className='flex-1'>
          <ResourceNameSelect
            ariaLabel={`环境变量 ${index + 1} ${kindLabel} 名称`}
            value={refName}
            options={refOptions.names}
            disabled={disabled}
            loading={refOptions.isLoading}
            loadFailed={refOptions.isError}
            onChange={(next) =>
              patchEnvSource(index, type, (target) =>
                setOptional(target, 'name', next)
              )
            }
          />
        </EnvField>
        <EnvField label='Key' className='flex-1'>
          <ResourceNameSelect
            ariaLabel={`环境变量 ${index + 1} ${kindLabel} Key`}
            value={stringValue(record(valueFrom[type]).key)}
            options={resourceDataKeys(refOptions.items, refName)}
            disabled={disabled}
            onChange={(next) =>
              patchEnvSource(index, type, (target) =>
                setOptional(target, 'key', next)
              )
            }
          />
        </EnvField>
        <EnvField label='可选 optional' className='w-32'>
          <OptionalTriState
            ariaLabel={`环境变量 ${index + 1} ${kindLabel} 可选`}
            value={record(valueFrom[type]).optional}
            disabled={disabled}
            onChange={(next) =>
              patchEnvSource(index, type, (target) =>
                setOptional(target, 'optional', next)
              )
            }
          />
        </EnvField>
      </>
    )
  }

  const renderEnvDraftFields = () => {
    if (envType === 'value')
      return (
        <EnvField label='值' className='flex-1'>
          <Input
            aria-label='新环境变量值'
            value={envValue}
            disabled={disabled}
            className={ENV_ROW_INPUT}
            onChange={(event) => setEnvValue(event.target.value)}
          />
        </EnvField>
      )
    if (envType === 'fieldRef')
      return (
        <EnvField label='字段路径' className='flex-1'>
          <Input
            aria-label='新环境变量字段路径'
            value={envFieldPath}
            disabled={disabled}
            list='env-fieldpath-suggestions'
            className={ENV_ROW_INPUT}
            onChange={(event) => setEnvFieldPath(event.target.value)}
          />
        </EnvField>
      )
    if (envType === 'resourceFieldRef')
      return (
        <>
          <EnvField label='资源' className='flex-1'>
            <Input
              aria-label='新环境变量资源'
              value={envResource}
              disabled={disabled}
              list='env-resource-suggestions'
              className={ENV_ROW_INPUT}
              onChange={(event) => setEnvResource(event.target.value)}
            />
          </EnvField>
          <EnvField label='除数' className='w-32'>
            <Input
              aria-label='新环境变量除数'
              value={envDivisor}
              disabled={disabled}
              className={ENV_ROW_INPUT}
              onChange={(event) => setEnvDivisor(event.target.value)}
            />
          </EnvField>
        </>
      )
    const kindLabel = envType === 'configMapKeyRef' ? 'ConfigMap' : 'Secret'
    const refOptions = refList(envType)
    return (
      <>
        <EnvField label={`${kindLabel} 名称`} className='flex-1'>
          <ResourceNameSelect
            ariaLabel={`新环境变量 ${kindLabel} 名称`}
            value={envRefName}
            options={refOptions.names}
            disabled={disabled}
            loading={refOptions.isLoading}
            loadFailed={refOptions.isError}
            onChange={(next) => {
              setEnvRefName(next)
              if (next !== envRefName) setEnvRefKey('')
            }}
          />
        </EnvField>
        <EnvField label='Key' className='flex-1'>
          <ResourceNameSelect
            ariaLabel={`新环境变量 ${kindLabel} Key`}
            value={envRefKey}
            options={resourceDataKeys(refOptions.items, envRefName)}
            disabled={disabled}
            onChange={setEnvRefKey}
          />
        </EnvField>
        <EnvField label='可选' className='w-32'>
          <OptionalTriState
            ariaLabel={`新环境变量 ${kindLabel} 可选`}
            value={envRefOptional}
            disabled={disabled}
            onChange={setEnvRefOptional}
          />
        </EnvField>
      </>
    )
  }

  return (
    <div className='space-y-6'>
      <div className='space-y-3'>
        <div hidden>
          <datalist id='env-fieldpath-suggestions'>
            {ENV_FIELD_PATHS.map((value) => (
              <option key={value} value={value} />
            ))}
          </datalist>
          <datalist id='env-resource-suggestions'>
            {ENV_RESOURCE_PATHS.map((value) => (
              <option key={value} value={value} />
            ))}
          </datalist>
        </div>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <button
            type='button'
            aria-label={envExpanded ? '收起单个环境变量' : '展开单个环境变量'}
            aria-expanded={envExpanded}
            aria-controls={envContentId}
            className='flex min-h-11 items-center gap-2 rounded-md px-2 text-sm font-medium text-foreground hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:outline-none sm:min-h-9'
            onClick={() => setEnvExpanded((expanded) => !expanded)}
          >
            <ChevronDown
              className={`size-4 text-muted-foreground transition-transform motion-reduce:transition-none ${envExpanded ? '' : '-rotate-90'}`}
            />
            <span>单个环境变量（{env.length}）</span>
            <span className='text-xs font-normal text-muted-foreground'>
              {envExpanded ? '收起' : '展开'}
            </span>
          </button>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={disabled || envAdding}
            aria-label='添加环境变量'
            className='h-11 text-sm sm:h-9'
            onClick={() => {
              setEnvExpanded(true)
              setEnvAdding(true)
              setEnvError('')
            }}
          >
            <Plus className='size-3.5' /> 添加环境变量
          </Button>
        </div>
        <div id={envContentId} hidden={!envExpanded} className='space-y-3'>
          <p className='text-xs leading-5 text-muted-foreground'>
            逐条定义变量，值可为字面量，或引用 Pod 字段、资源配额、ConfigMap /
            Secret。
          </p>
          {env.map((item, index) => (
            <div
              key={index}
              className='grid grid-cols-12 items-end gap-3 rounded-lg bg-muted/40 p-3'
            >
              <EnvField
                label='名称'
                className={
                  envSourceType(item) === 'value' ||
                  envSourceType(item) === 'fieldRef'
                    ? 'col-span-12 sm:col-span-4'
                    : 'col-span-12 sm:col-span-8'
                }
              >
                <Input
                  aria-label={`环境变量 ${index + 1} 名称`}
                  value={stringValue(item.name)}
                  disabled={disabled}
                  className={ENV_ROW_INPUT}
                  onChange={(event) =>
                    patchEnv(index, (target) =>
                      setOptional(target, 'name', event.target.value)
                    )
                  }
                />
              </EnvField>
              <EnvField label='来源' className='col-span-10 sm:col-span-3'>
                <select
                  aria-label={`环境变量 ${index + 1} 来源`}
                  value={envSourceType(item)}
                  disabled={disabled}
                  className={ENV_ROW_SELECT}
                  onChange={(event) =>
                    switchEnvSource(index, event.target.value)
                  }
                >
                  {envSourceType(item) === 'unknown' && (
                    <option value='unknown' disabled>
                      暂不支持表单编辑
                    </option>
                  )}
                  {ENV_SOURCE_TYPES.map((source) => (
                    <option key={source} value={source}>
                      {ENV_SOURCE_LABELS[source]}
                    </option>
                  ))}
                </select>
              </EnvField>
              <div
                className={
                  envSourceType(item) === 'value' ||
                  envSourceType(item) === 'fieldRef'
                    ? 'order-last col-span-12 flex min-w-0 flex-wrap items-end gap-3 sm:order-none sm:col-span-4'
                    : 'order-last col-span-12 flex min-w-0 flex-wrap items-end gap-3'
                }
              >
                {renderEnvSourceFields(item, index)}
              </div>
              <Button
                type='button'
                variant='ghost'
                size='icon'
                disabled={disabled}
                aria-label={`删除环境变量 ${index + 1}`}
                className='col-span-2 size-11 justify-self-end text-muted-foreground hover:bg-destructive/10 hover:text-destructive sm:col-span-1 sm:size-9'
                onClick={() =>
                  onEnvChange(
                    env.filter((_, current) => current !== index).length
                      ? env.filter((_, current) => current !== index)
                      : undefined
                  )
                }
              >
                <Trash2 className='size-4' />
              </Button>
            </div>
          ))}
          {env.length === 0 && !envAdding && (
            <p className='rounded-lg border border-dashed bg-muted/30 p-4 text-center text-sm text-muted-foreground'>
              暂无环境变量
            </p>
          )}
          {envAdding && (
            <div className='space-y-3 rounded-lg border bg-muted/40 p-4'>
              <div className='flex flex-wrap items-end gap-2'>
                <EnvField label='名称' className='flex-1 sm:w-44 sm:flex-none'>
                  <Input
                    aria-label='新环境变量名称'
                    value={envName}
                    disabled={disabled}
                    placeholder='例如 MODE'
                    className={ENV_ROW_INPUT}
                    onChange={(event) => setEnvName(event.target.value)}
                  />
                </EnvField>
                <EnvField label='来源' className='w-40'>
                  <select
                    aria-label='新环境变量来源'
                    value={envType}
                    disabled={disabled}
                    className={ENV_ROW_SELECT}
                    onChange={(event) => setEnvType(event.target.value)}
                  >
                    {ENV_SOURCE_TYPES.map((source) => (
                      <option key={source} value={source}>
                        {ENV_SOURCE_LABELS[source]}
                      </option>
                    ))}
                  </select>
                </EnvField>
                {renderEnvDraftFields()}
              </div>
              {envError && (
                <p className='flex items-center gap-1.5 text-sm text-[#c2404e]'>
                  <Info className='size-3.5' />
                  {envError}
                </p>
              )}
              <div className='flex items-center gap-2'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  disabled={disabled}
                  aria-label='确认添加环境变量'
                  className='h-11 sm:h-9'
                  onClick={confirmEnv}
                >
                  确认添加
                </Button>
                <Button
                  type='button'
                  variant='ghost'
                  size='sm'
                  disabled={disabled}
                  className='h-11 sm:h-9'
                  onClick={resetEnvDraft}
                >
                  取消
                </Button>
              </div>
            </div>
          )}
        </div>
      </div>
      <div className='space-y-3'>
        <div className='flex flex-wrap items-center justify-between gap-2'>
          <span className='text-sm font-medium text-foreground'>
            批量导入配置（{envFrom.length}）
          </span>
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={disabled || fromAdding}
            aria-label='添加批量导入'
            className='h-11 text-sm sm:h-9'
            onClick={() => {
              setFromAdding(true)
              setFromError('')
            }}
          >
            <Plus className='size-3.5' /> 添加批量导入
          </Button>
        </div>
        <p className='text-xs leading-5 text-muted-foreground'>
          将整个 ConfigMap 或 Secret 的条目批量导入为环境变量，可选前缀。
        </p>
        {envFrom.map((item, index) => {
          const type = envFromType(item)
          const refKey =
            type === 'secretRef' ? 'secretRef' : ('configMapRef' as const)
          return (
            <div
              key={index}
              className='flex flex-wrap items-end gap-3 rounded-lg bg-muted/40 p-3'
            >
              <EnvField label='类型' className='w-36'>
                <select
                  aria-label={`批量导入 ${index + 1} 类型`}
                  value={type}
                  disabled={disabled}
                  className={ENV_ROW_SELECT}
                  onChange={(event) =>
                    switchEnvFromType(
                      index,
                      event.target.value as 'configMapRef' | 'secretRef'
                    )
                  }
                >
                  {type === 'unknown' && (
                    <option value='unknown' disabled>
                      未知类型
                    </option>
                  )}
                  <option value='configMapRef'>ConfigMap</option>
                  <option value='secretRef'>Secret</option>
                </select>
              </EnvField>
              <EnvField label='名称' className='flex-1'>
                <ResourceNameSelect
                  ariaLabel={`批量导入 ${index + 1} 名称`}
                  value={
                    type === 'unknown'
                      ? ''
                      : stringValue(record(item[refKey]).name)
                  }
                  options={refList(refKey).names}
                  disabled={disabled || type === 'unknown'}
                  loading={refList(refKey).isLoading}
                  loadFailed={refList(refKey).isError}
                  onChange={(next) =>
                    patchEnvFromRef(index, refKey, (target) =>
                      setOptional(target, 'name', next)
                    )
                  }
                />
              </EnvField>
              <EnvField label='前缀' className='w-36'>
                <Input
                  aria-label={`批量导入 ${index + 1} 前缀`}
                  value={stringValue(item.prefix)}
                  disabled={disabled}
                  placeholder='可选'
                  className={ENV_ROW_INPUT}
                  onChange={(event) =>
                    patchEnvFrom(index, (target) =>
                      setOptional(target, 'prefix', event.target.value)
                    )
                  }
                />
              </EnvField>
              {type !== 'unknown' && (
                <EnvField label='可选' className='w-32'>
                  <OptionalTriState
                    ariaLabel={`批量导入 ${index + 1} 可选`}
                    value={record(item[refKey]).optional}
                    disabled={disabled}
                    onChange={(next) =>
                      patchEnvFromRef(index, refKey, (target) =>
                        setOptional(target, 'optional', next)
                      )
                    }
                  />
                </EnvField>
              )}
              {type === 'unknown' && (
                <p className='flex w-full items-center gap-1.5 text-sm text-[#94621c]'>
                  <Info className='size-3.5' />
                  暂不支持的批量导入来源，原样保留；选择类型后才会替换
                </p>
              )}
              <Button
                type='button'
                variant='ghost'
                size='icon'
                disabled={disabled}
                aria-label={`删除批量导入 ${index + 1}`}
                className='size-11 shrink-0 text-[#9b6571] hover:bg-[#fdf0f1] hover:text-[#c2404e] sm:size-9'
                onClick={() =>
                  onEnvFromChange(
                    envFrom.filter((_, current) => current !== index).length
                      ? envFrom.filter((_, current) => current !== index)
                      : undefined
                  )
                }
              >
                <Trash2 className='size-4' />
              </Button>
            </div>
          )
        })}
        {envFrom.length === 0 && !fromAdding && (
          <p className='rounded-lg border border-dashed bg-muted/30 p-4 text-center text-sm text-muted-foreground'>
            暂无批量导入
          </p>
        )}
        {fromAdding && (
          <div className='space-y-3 rounded-lg border bg-muted/40 p-4'>
            <div className='flex flex-wrap items-end gap-2'>
              <EnvField label='类型' className='w-36'>
                <select
                  aria-label='新批量导入类型'
                  value={fromType}
                  disabled={disabled}
                  className={ENV_ROW_SELECT}
                  onChange={(event) =>
                    setFromType(
                      event.target.value as 'configMapRef' | 'secretRef'
                    )
                  }
                >
                  <option value='configMapRef'>ConfigMap</option>
                  <option value='secretRef'>Secret</option>
                </select>
              </EnvField>
              <EnvField label='名称' className='flex-1'>
                <ResourceNameSelect
                  ariaLabel='新批量导入名称'
                  value={fromName}
                  options={refList(fromType).names}
                  disabled={disabled}
                  loading={refList(fromType).isLoading}
                  loadFailed={refList(fromType).isError}
                  onChange={setFromName}
                />
              </EnvField>
              <EnvField label='前缀' className='w-36'>
                <Input
                  aria-label='新批量导入前缀'
                  value={fromPrefix}
                  disabled={disabled}
                  placeholder='可选'
                  className={ENV_ROW_INPUT}
                  onChange={(event) => setFromPrefix(event.target.value)}
                />
              </EnvField>
              <EnvField label='可选' className='w-32'>
                <OptionalTriState
                  ariaLabel='新批量导入可选'
                  value={fromOptional}
                  disabled={disabled}
                  onChange={setFromOptional}
                />
              </EnvField>
            </div>
            {fromError && (
              <p className='flex items-center gap-1.5 text-sm text-[#c2404e]'>
                <Info className='size-3.5' />
                {fromError}
              </p>
            )}
            <div className='flex items-center gap-2'>
              <Button
                type='button'
                variant='outline'
                size='sm'
                disabled={disabled}
                aria-label='确认添加批量导入'
                className='h-11 sm:h-9'
                onClick={confirmEnvFrom}
              >
                确认添加
              </Button>
              <Button
                type='button'
                variant='ghost'
                size='sm'
                disabled={disabled}
                className='h-11 sm:h-9'
                onClick={() => {
                  setFromAdding(false)
                  setFromError('')
                }}
              >
                取消
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function ContainerField({
  label,
  hint,
  children,
}: {
  label: string
  hint?: string
  children: ReactNode
}) {
  return (
    <div className='min-w-0 space-y-2'>
      <div className='text-sm font-medium text-foreground'>{label}</div>
      {children}
      {hint && (
        <p className='text-xs leading-5 text-muted-foreground'>{hint}</p>
      )}
    </div>
  )
}

function ContainerSection({
  title,
  description,
  icon,
  action,
  children,
}: {
  title: string
  description: string
  icon: ReactNode
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section className='min-w-0 overflow-hidden rounded-xl border bg-card shadow-sm'>
      <div className='flex flex-wrap items-center justify-between gap-3 border-b px-4 py-4 sm:px-6'>
        <div className='flex items-start gap-3'>
          <span className='mt-1 text-muted-foreground'>{icon}</span>
          <div className='space-y-1'>
            <h3 className='text-base font-semibold text-foreground'>{title}</h3>
            <p className='text-xs leading-5 text-muted-foreground'>
              {description}
            </p>
          </div>
        </div>
        {action}
      </div>
      <div className='space-y-6 p-4 sm:p-6'>{children}</div>
    </section>
  )
}

function ContainerEditor({
  obj,
  field,
  index,
  params,
  disabled,
  updateDraft,
}: {
  obj: K8sObject
  field: 'containers' | 'initContainers'
  index: number
  params: K8sObjectParams
  disabled: boolean
  updateDraft: UpdateDraft
}) {
  const container = containersOf(obj, field)[index] ?? {}
  const env = Array.isArray(container.env)
    ? (container.env as RecordValue[])
    : []
  const envFrom = Array.isArray(container.envFrom)
    ? (container.envFrom as RecordValue[])
    : []
  const ports = Array.isArray(container.ports)
    ? (container.ports as RecordValue[])
    : []
  const resources = record(container.resources)
  const update = (fn: (container: RecordValue) => void) =>
    updateDraft((draft) => updateContainer(draft, field, index, fn))
  const updatePorts = (next: RecordValue[]) =>
    update((item) => setOptional(item, 'ports', next))
  return (
    <div className='min-w-0 space-y-4'>
      <div className='flex flex-wrap items-center justify-between gap-3 px-1 pb-1'>
        <div className='flex min-w-0 items-center gap-3'>
          <span className='flex size-10 shrink-0 items-center justify-center rounded-xl border bg-card text-blue-900 dark:text-blue-200'>
            <Box className='size-5' />
          </span>
          <div className='min-w-0 space-y-1'>
            <h2 className='font-mono text-base font-semibold break-all text-foreground'>
              {stringValue(container.name) || '未命名容器'}
            </h2>
            <p className='text-xs text-muted-foreground'>
              {env.length} 个环境变量 · {ports.length} 个端口
            </p>
          </div>
        </div>
        <Badge variant='secondary' className='shrink-0 text-xs font-normal'>
          {field === 'initContainers' ? '初始化容器' : '工作容器'}
        </Badge>
      </div>
      <ContainerSection
        title='镜像与启动'
        description='配置容器镜像及拉取方式，按需覆盖默认启动命令。'
        icon={<Box className='size-5' />}
      >
        <div className='grid gap-4 xl:grid-cols-3'>
          <div className='min-w-0 xl:col-span-2'>
            <ContainerField label='镜像'>
              <Input
                aria-label={`容器镜像 ${stringValue(container.name)}`}
                title={stringValue(container.image)}
                value={stringValue(container.image)}
                disabled={disabled}
                className='h-11 bg-background font-mono text-sm shadow-none sm:h-10'
                onChange={(event) =>
                  update((item) =>
                    setOptional(item, 'image', event.target.value)
                  )
                }
              />
            </ContainerField>
          </div>
          <ContainerField label='镜像拉取策略'>
            <select
              aria-label='镜像拉取策略'
              value={stringValue(container.imagePullPolicy) || 'IfNotPresent'}
              disabled={disabled}
              className='h-11 w-full rounded-md border border-input bg-background px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 sm:h-10'
              onChange={(event) =>
                update((item) =>
                  setOptional(item, 'imagePullPolicy', event.target.value)
                )
              }
            >
              <option value='Always'>Always</option>
              <option value='IfNotPresent'>IfNotPresent</option>
              <option value='Never'>Never</option>
            </select>
          </ContainerField>
        </div>
        <div className='space-y-4 border-t pt-4'>
          <div className='flex items-center gap-2 text-sm font-medium text-foreground'>
            <Terminal className='size-4 text-muted-foreground' /> 启动配置
          </div>
          <div className='grid gap-4 md:grid-cols-2'>
            <TextListField
              label='启动命令'
              value={container.command}
              disabled={disabled}
              onChange={(values) =>
                update((item) => setOptional(item, 'command', values))
              }
            />
            <TextListField
              label='启动参数'
              value={container.args}
              disabled={disabled}
              onChange={(values) =>
                update((item) => setOptional(item, 'args', values))
              }
            />
          </div>
        </div>
      </ContainerSection>
      <ContainerSection
        title='环境变量'
        description='定义运行环境，支持直接填写或引用 Kubernetes 配置。'
        icon={<ListTree className='size-5' />}
      >
        <ContainerEnvironmentEditor
          key={`${field}-${index}`}
          env={env}
          envFrom={envFrom}
          params={params}
          disabled={disabled}
          onEnvChange={(next) =>
            update((item) => setOptional(item, 'env', next))
          }
          onEnvFromChange={(next) =>
            update((item) => setOptional(item, 'envFrom', next))
          }
        />
      </ContainerSection>
      <ContainerSection
        title='容器端口'
        description='声明容器监听端口，供服务映射与网络访问使用。'
        icon={<Network className='size-5' />}
        action={
          <Button
            type='button'
            variant='outline'
            size='sm'
            disabled={disabled}
            className='h-11 text-sm sm:h-9'
            onClick={() =>
              updatePorts([
                ...ports,
                { name: '', containerPort: 80, protocol: 'TCP' },
              ])
            }
          >
            <Plus className='size-3.5' /> 添加端口
          </Button>
        }
      >
        <div className='space-y-3'>
          {ports.map((port, portIndex) => (
            <div
              key={portIndex}
              className='flex flex-wrap items-center gap-3 rounded-lg bg-muted/40 p-3'
            >
              <Input
                aria-label={`端口名称 ${portIndex + 1}`}
                value={stringValue(port.name)}
                disabled={disabled}
                placeholder='名称（可选）'
                className='h-11 w-full min-w-0 font-mono text-sm sm:h-9 sm:w-auto sm:flex-1'
                onChange={(event) => {
                  const next = clone(ports)
                  next[portIndex].name = event.target.value
                  updatePorts(next)
                }}
              />
              <Input
                aria-label={`容器端口 ${portIndex + 1}`}
                type='number'
                value={stringValue(port.containerPort)}
                disabled={disabled}
                min={1}
                max={65535}
                className='h-11 w-28 font-mono text-sm sm:h-9'
                onChange={(event) => {
                  const next = clone(ports)
                  next[portIndex].containerPort = Number(event.target.value)
                  updatePorts(next)
                }}
              />
              <select
                aria-label={`端口协议 ${portIndex + 1}`}
                value={stringValue(port.protocol) || 'TCP'}
                disabled={disabled}
                className='h-11 rounded-md border border-input bg-transparent px-3 text-sm outline-none disabled:cursor-not-allowed disabled:opacity-50 sm:h-9'
                onChange={(event) => {
                  const next = clone(ports)
                  next[portIndex].protocol = event.target.value
                  updatePorts(next)
                }}
              >
                <option value='TCP'>TCP</option>
                <option value='UDP'>UDP</option>
                <option value='SCTP'>SCTP</option>
              </select>
              <Button
                type='button'
                variant='ghost'
                size='icon'
                disabled={disabled}
                aria-label={`删除端口 ${portIndex + 1}`}
                className='size-11 text-[#9b6571] hover:bg-[#fdf0f1] hover:text-[#c2404e] sm:size-9'
                onClick={() =>
                  updatePorts(
                    ports.filter((_, itemIndex) => itemIndex !== portIndex)
                  )
                }
              >
                <Trash2 className='size-4' />
              </Button>
            </div>
          ))}
          {ports.length === 0 && (
            <p className='rounded-lg border border-dashed bg-muted/30 px-4 py-6 text-center text-sm text-muted-foreground'>
              暂无容器端口，点击「添加端口」新增
            </p>
          )}
        </div>
      </ContainerSection>
      <ContainerSection
        title='资源配额'
        description='为调度设置资源请求，为运行设置资源上限。'
        icon={<Cpu className='size-5' />}
      >
        <ContainerResourcesEditor
          key={`${field}-${index}`}
          resources={resources}
          disabled={disabled}
          onChange={(next) =>
            update((item) => setOptional(item, 'resources', next))
          }
        />
      </ContainerSection>
    </div>
  )
}

function BasicTab({
  obj,
  disabled,
  updateDraft,
}: {
  obj: K8sObject
  disabled: boolean
  updateDraft: UpdateDraft
}) {
  const metadata = record(obj.metadata)
  const templateMetadata = templateMetadataOf(obj)
  const selector = record(record(obj.spec).selector).matchLabels
  return (
    <div className='space-y-4'>
      <SectionCard
        title='基本信息'
        description='名称和资源身份不可修改，标签与注解会随工作负载一起保存。'
      >
        <Field label='工作负载类型'>
          <div className='md:pt-1.5'>
            <Badge className='border-0 bg-[#234883] px-3 py-1.5 font-mono text-sm'>
              {obj.kind ?? 'Workload'}
            </Badge>
          </div>
        </Field>
        <Field label='工作负载名称'>
          <ReadonlyValue>{stringValue(metadata.name) || '-'}</ReadonlyValue>
        </Field>
        <Field label='命名空间'>
          <ReadonlyValue>
            {stringValue(metadata.namespace) || '-'}
          </ReadonlyValue>
        </Field>
        <KeyValueEditor
          label='标签'
          values={stringMap(metadata.labels)}
          disabled={disabled}
          onChange={(values) =>
            updateDraft((draft) => {
              const next = record(draft.metadata)
              draft.metadata = next
              setOptional(next, 'labels', values)
            })
          }
        />
        <KeyValueEditor
          label='注解'
          values={stringMap(metadata.annotations)}
          disabled={disabled}
          onChange={(values) =>
            updateDraft((draft) => {
              const next = record(draft.metadata)
              draft.metadata = next
              setOptional(next, 'annotations', values)
            })
          }
        />
      </SectionCard>
      <SectionCard
        title='选择器与 Pod 标签'
        description='selector 是控制器与 Pod 的关联条件，修改前请确认不会造成孤儿 Pod。'
      >
        <Field label='选择器' hint='当前版本只读，避免误接管其他 Pod'>
          <p className='font-mono text-xs break-all text-[#536987] md:pt-2'>
            {JSON.stringify(selector ?? {})}
          </p>
        </Field>
        <KeyValueEditor
          label='Pod 模板标签'
          values={stringMap(templateMetadata.labels)}
          disabled={disabled}
          onChange={(values) =>
            updateDraft((draft) => {
              updateTemplateMetadata(draft, (next) =>
                setOptional(next, 'labels', values)
              )
            })
          }
        />
      </SectionCard>
    </div>
  )
}

function ContainersTab({
  obj,
  params,
  disabled,
  updateDraft,
}: {
  obj: K8sObject
  params: K8sObjectParams
  disabled: boolean
  updateDraft: UpdateDraft
}) {
  const containers = containersOf(obj, 'containers')
  const initContainers = containersOf(obj, 'initContainers')
  const entries = [
    ...initContainers.map((container, index) => ({
      field: 'initContainers' as const,
      index,
      init: true,
      name: stringValue(container.name) || `init-${index + 1}`,
    })),
    ...containers.map((container, index) => ({
      field: 'containers' as const,
      index,
      init: false,
      name: stringValue(container.name) || `container-${index + 1}`,
    })),
  ]
  const [selected, setSelected] = useState(0)
  const currentIndex = Math.min(selected, entries.length - 1)
  const current = entries[currentIndex]
  return (
    <div className='space-y-4'>
      {entries.length === 0 || !current ? (
        <p className='rounded-xl border bg-card p-6 text-sm text-muted-foreground'>
          暂无容器
        </p>
      ) : (
        <div className='flex flex-col gap-4 lg:flex-row lg:items-start lg:gap-6'>
          <div className='min-w-0 rounded-xl border bg-card p-3 lg:sticky lg:top-24 lg:w-52 lg:shrink-0'>
            <div className='flex items-center justify-between gap-2 px-2 pt-1 pb-3'>
              <h2 className='text-sm font-semibold text-foreground'>
                现有容器
              </h2>
              <span className='rounded-md bg-muted px-2 py-1 text-xs font-medium text-muted-foreground'>
                {entries.length}
              </span>
            </div>
            <div
              role='group'
              aria-label='现有容器'
              className='flex gap-2 overflow-x-auto pb-1 lg:flex-col lg:overflow-x-visible lg:pb-0'
            >
              {entries.map((entry, entryIndex) => (
                <button
                  key={`${entry.field}-${entry.index}`}
                  type='button'
                  aria-pressed={entryIndex === currentIndex}
                  title={entry.name}
                  className='group flex min-h-14 shrink-0 items-center gap-3 rounded-lg border border-transparent px-3 py-3 text-left text-sm text-muted-foreground transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:outline-none aria-pressed:border-blue-200 aria-pressed:bg-blue-50 aria-pressed:text-blue-900 motion-reduce:transition-none lg:w-full lg:shrink dark:aria-pressed:border-blue-900 dark:aria-pressed:bg-blue-950 dark:aria-pressed:text-blue-200'
                  onClick={() => setSelected(entryIndex)}
                >
                  <Box className='size-4 shrink-0' />
                  <span className='min-w-0 space-y-1'>
                    <span className='block max-w-48 truncate font-mono font-medium lg:max-w-none'>
                      {entry.name}
                    </span>
                    <span className='block text-xs font-normal'>
                      {entry.init ? '初始化' : '工作'}
                    </span>
                  </span>
                </button>
              ))}
            </div>
          </div>
          <div className='min-w-0 flex-1'>
            <ContainerEditor
              key={`${current.field}-${current.index}`}
              obj={obj}
              field={current.field}
              index={current.index}
              params={params}
              disabled={disabled}
              updateDraft={updateDraft}
            />
          </div>
        </div>
      )}
    </div>
  )
}

const VOLUME_SOURCE_TYPES = [
  'persistentVolumeClaim',
  'emptyDir',
  'hostPath',
  'nfs',
  'configMap',
  'secret',
] as const

type VolumeSourceType = (typeof VOLUME_SOURCE_TYPES)[number]

const VOLUME_SOURCE_LABELS: Record<VolumeSourceType, string> = {
  persistentVolumeClaim: '存储卷声明',
  emptyDir: '临时目录（EmptyDir）',
  hostPath: 'HostPath',
  nfs: 'NFS',
  configMap: '配置字典',
  secret: '密文',
}

const CONTAINER_FIELDS = ['initContainers', 'containers'] as const

const EDITOR_TAB_CLASS =
  'h-12 min-w-max flex-none rounded-t-md border-b-2 border-transparent bg-transparent px-4 py-2 text-sm font-medium whitespace-nowrap text-[#536987] shadow-none hover:bg-[#f8fafc] hover:text-[#234883] focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[#234883]/40 data-[state=active]:-mb-px data-[state=active]:border-b-[#234883] data-[state=active]:bg-[#f4f8fd] data-[state=active]:font-semibold data-[state=active]:text-[#234883] data-[state=active]:shadow-none'

function volumeSourceType(volume: RecordValue): string | undefined {
  for (const [key, value] of Object.entries(volume)) {
    if (key === 'name' || value == null) continue
    return key
  }
  return undefined
}

const isSupportedSource = (
  type: string | undefined
): type is VolumeSourceType =>
  type != null && (VOLUME_SOURCE_TYPES as readonly string[]).includes(type)

// Switching type drops only the previous source key; every other volume
// property (name and any fields the form does not know) is preserved.
function switchVolumeSource(volume: RecordValue, type: VolumeSourceType) {
  const current = volumeSourceType(volume)
  if (current === type) {
    if (volume[type] == null) volume[type] = {}
    return
  }
  if (current != null) delete volume[current]
  switch (type) {
    case 'persistentVolumeClaim':
      volume.persistentVolumeClaim = {}
      break
    case 'emptyDir':
      volume.emptyDir = {}
      break
    case 'hostPath':
      volume.hostPath = {}
      break
    case 'nfs':
      volume.nfs = {}
      break
    case 'configMap':
      volume.configMap = {}
      break
    case 'secret':
      volume.secret = {}
      break
  }
}

// Field edits merge into the existing source object so optional nested
// fields the form does not expose (items, defaultMode, ...) survive.
function patchVolumeSource(
  volume: RecordValue,
  type: VolumeSourceType,
  patch: RecordValue
) {
  const source = record(volume[type])
  for (const [key, value] of Object.entries(patch))
    setOptional(source, key, value)
  volume[type] = source
}

// Horizontal form row for the volume source panel — Kuboard-style fixed
// right-aligned label column on desktop, stacked on mobile.
function SourceField({
  label,
  hint,
  required,
  children,
}: {
  label: string
  hint?: string
  required?: boolean
  children: React.ReactNode
}) {
  return (
    <div className='flex flex-col gap-1.5 sm:flex-row sm:items-start sm:gap-3'>
      <span className='shrink-0 text-sm text-[#606266] sm:w-28 sm:pt-2 sm:text-right'>
        {label}
        {required && <span className='text-[#f44656]'> *</span>}
      </span>
      <div className='min-w-0 flex-1'>
        {children}
        {hint && <p className='mt-1 text-xs text-[#64748b]'>{hint}</p>}
      </div>
    </div>
  )
}

const STORAGE_SELECT_CLASS =
  'flex h-11 w-full rounded-md border border-input bg-transparent px-3 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 sm:h-9'
const STORAGE_INPUT_CLASS = 'h-11 font-mono text-xs sm:h-9'

function VolumeSourceFields({
  index,
  volume,
  type,
  disabled,
  lists,
  keySuggestions,
  onPatch,
}: {
  index: number
  volume: RecordValue
  type: VolumeSourceType
  disabled: boolean
  lists: {
    pvc: ResourceListResult
    configMap: ResourceListResult
    secret: ResourceListResult
  }
  keySuggestions: string[]
  onPatch: (patch: RecordValue) => void
}) {
  const source = record(volume[type])
  const nameField = (
    name: 'claimName' | 'name' | 'secretName',
    label: string,
    list: ResourceListResult
  ) => (
    <ResourceNameSelect
      ariaLabel={`数据卷 ${index + 1} ${label}`}
      value={stringValue(source[name])}
      options={list.names}
      disabled={disabled}
      loading={list.isLoading}
      loadFailed={list.isError}
      selectClassName={STORAGE_SELECT_CLASS}
      inputClassName={STORAGE_INPUT_CLASS}
      onChange={(next) => onPatch({ [name]: next })}
    />
  )
  const field = (name: string, label: string) => (
    <Input
      aria-label={`数据卷 ${index + 1} ${label}`}
      value={stringValue(source[name])}
      disabled={disabled}
      className={STORAGE_INPUT_CLASS}
      onChange={(event) => onPatch({ [name]: event.target.value })}
    />
  )
  const readOnly = (label: string) => (
    <label className='flex h-11 items-center gap-2 rounded-md border border-input px-3 text-xs text-[#536987] sm:h-9'>
      <input
        type='checkbox'
        aria-label={`数据卷 ${index + 1} ${label}`}
        checked={source.readOnly === true}
        disabled={disabled}
        className='size-4 accent-[#234883]'
        onChange={(event) =>
          onPatch({ readOnly: event.target.checked || undefined })
        }
      />
      只读
    </label>
  )
  switch (type) {
    case 'persistentVolumeClaim':
      return (
        <div className='space-y-3'>
          <SourceField
            label='存储卷声明'
            required
            hint='下拉选择名称空间内已有 PVC，也可手动输入'
          >
            {nameField('claimName', 'PVC 名称', lists.pvc)}
          </SourceField>
          <SourceField label='访问模式'>{readOnly('PVC 只读')}</SourceField>
        </div>
      )
    case 'emptyDir':
      return (
        <div className='space-y-3'>
          <SourceField label='介质' hint='Memory 使用 tmpfs，容量计入内存限制'>
            <select
              aria-label={`数据卷 ${index + 1} emptyDir 介质`}
              value={stringValue(source.medium)}
              disabled={disabled}
              className={STORAGE_SELECT_CLASS}
              onChange={(event) =>
                onPatch({ medium: event.target.value || undefined })
              }
            >
              <option value=''>默认（节点磁盘）</option>
              <option value='Memory'>Memory</option>
            </select>
          </SourceField>
          <SourceField label='容量上限'>
            <Input
              aria-label={`数据卷 ${index + 1} emptyDir 容量上限`}
              value={stringValue(source.sizeLimit)}
              disabled={disabled}
              placeholder='例如：1Gi'
              className='h-11 font-mono text-xs sm:h-9'
              onChange={(event) =>
                onPatch({ sizeLimit: event.target.value || undefined })
              }
            />
          </SourceField>
        </div>
      )
    case 'hostPath':
      return (
        <div className='space-y-3'>
          <SourceField label='主机路径' required>
            {field('path', '主机路径')}
          </SourceField>
          <SourceField label='路径类型'>
            <select
              aria-label={`数据卷 ${index + 1} hostPath 类型`}
              value={stringValue(source.type)}
              disabled={disabled}
              className={STORAGE_SELECT_CLASS}
              onChange={(event) =>
                onPatch({ type: event.target.value || undefined })
              }
            >
              <option value=''>未指定</option>
              <option value='DirectoryOrCreate'>DirectoryOrCreate</option>
              <option value='Directory'>Directory</option>
              <option value='FileOrCreate'>FileOrCreate</option>
              <option value='File'>File</option>
              <option value='Socket'>Socket</option>
              <option value='CharDevice'>CharDevice</option>
              <option value='BlockDevice'>BlockDevice</option>
            </select>
          </SourceField>
        </div>
      )
    case 'nfs':
      return (
        <div className='space-y-3'>
          <SourceField label='NFS 服务器' required>
            {field('server', 'NFS 服务器')}
          </SourceField>
          <SourceField label='共享路径' required>
            {field('path', 'NFS 路径')}
          </SourceField>
          <SourceField label='访问模式'>{readOnly('NFS 只读')}</SourceField>
        </div>
      )
    case 'configMap':
      return (
        <div className='space-y-3'>
          <SourceField
            label='配置字典'
            required
            hint='下拉选择名称空间内已有的配置字典，也可手动输入'
          >
            {nameField('name', 'ConfigMap 名称', lists.configMap)}
          </SourceField>
          <KeyToPathEditor
            index={index}
            type={type}
            source={source}
            disabled={disabled}
            keySuggestions={keySuggestions}
            onPatch={onPatch}
          />
        </div>
      )
    case 'secret':
      return (
        <div className='space-y-3'>
          <SourceField
            label='密文'
            required
            hint='下拉选择名称空间内已有的保密字典，也可手动输入'
          >
            {nameField('secretName', 'Secret 名称', lists.secret)}
          </SourceField>
          <KeyToPathEditor
            index={index}
            type={type}
            source={source}
            disabled={disabled}
            keySuggestions={keySuggestions}
            onPatch={onPatch}
          />
        </div>
      )
  }
}

function KeyToPathEditor({
  index,
  type,
  source,
  disabled,
  keySuggestions,
  onPatch,
}: {
  index: number
  type: 'configMap' | 'secret'
  source: RecordValue
  disabled: boolean
  keySuggestions: string[]
  onPatch: (patch: RecordValue) => void
}) {
  const items = Array.isArray(source.items)
    ? (source.items as RecordValue[])
    : []
  const updateItems = (fn: (items: RecordValue[]) => void) => {
    const next = clone(items)
    fn(next)
    onPatch({ items: next.length ? next : undefined })
  }
  return (
    <div className='space-y-2 border-t border-dashed border-[#a7b6cd] pt-3'>
      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div>
          <p className='text-xs font-semibold text-[#536987]'>KeyToPath</p>
          <p className='text-xs text-[#64748b]'>
            可为空；添加后只将选定的 Key 映射到数据卷内路径
          </p>
        </div>
        <Button
          type='button'
          variant='outline'
          size='sm'
          disabled={
            disabled ||
            !stringValue(source[type === 'configMap' ? 'name' : 'secretName'])
          }
          aria-label={`数据卷 ${index + 1} 添加 KeyToPath`}
          className='h-11 sm:h-9'
          onClick={() =>
            updateItems((next) => next.push({ key: '', path: '' }))
          }
        >
          <Plus className='size-3.5' /> 添加 KeyToPath
        </Button>
      </div>
      {items.map((item, itemIndex) => (
        <div
          key={itemIndex}
          className='flex flex-wrap items-center gap-2'
        >
          <ResourceNameSelect
            ariaLabel={`数据卷 ${index + 1} KeyToPath ${itemIndex + 1} Key`}
            value={stringValue(item.key)}
            options={keySuggestions}
            disabled={disabled}
            placeholder='选择或输入 Key'
            selectClassName={`${STORAGE_SELECT_CLASS} sm:w-auto sm:flex-1`}
            inputClassName={STORAGE_INPUT_CLASS}
            onChange={(next) =>
              updateItems((items) => {
                items[itemIndex].key = next
              })
            }
          />
          <span className='hidden text-[#657fa8] sm:block'>→</span>
          <Input
            aria-label={`数据卷 ${index + 1} KeyToPath ${itemIndex + 1} 路径`}
            value={stringValue(item.path)}
            disabled={disabled}
            placeholder='数据卷内相对路径'
            className='h-11 min-w-0 flex-1 font-mono text-xs sm:h-9'
            onChange={(event) =>
              updateItems((next) => {
                next[itemIndex].path = event.target.value
              })
            }
          />
          <Button
            type='button'
            variant='ghost'
            size='icon'
            disabled={disabled}
            aria-label={`数据卷 ${index + 1} 删除 KeyToPath ${itemIndex + 1}`}
            className='size-11 text-[#9b6571] hover:bg-[#fdf0f1] hover:text-[#c2404e] sm:size-9'
            onClick={() => updateItems((next) => next.splice(itemIndex, 1))}
          >
            <Trash2 className='size-4' />
          </Button>
        </div>
      ))}
    </div>
  )
}

function VolumeMountRows({
  volumeIndex,
  volumeName,
  obj,
  disabled,
  updateDraft,
}: {
  volumeIndex: number
  volumeName: string
  obj: K8sObject
  disabled: boolean
  updateDraft: UpdateDraft
}) {
  const rows = CONTAINER_FIELDS.flatMap((field) =>
    containersOf(obj, field).map((container, index) => ({
      field,
      index,
      container,
    }))
  )
  if (rows.length === 0)
    return <p className='text-sm text-[#64748b]'>暂无容器可挂载</p>
  return (
    <div className='space-y-3 border-t border-dashed border-[#a7b6cd] pt-4'>
      <p className='flex items-center gap-2 text-sm font-semibold text-[#234883]'>
        <span className='h-4 w-1 rounded-full bg-[#234883]' />
        将数据卷挂载到容器
      </p>
      {rows.map(({ field, index, container }) => {
        const containerName =
          stringValue(container.name) || `${field}-${index + 1}`
        const mounts = (
          Array.isArray(container.volumeMounts)
            ? (container.volumeMounts as RecordValue[])
            : []
        ).filter((mount) => record(mount).name === volumeName)
        const updateMounts = (fn: (mounts: RecordValue[]) => void) =>
          updateDraft((draft) =>
            updateContainer(draft, field, index, (item) => {
              const all = Array.isArray(item.volumeMounts)
                ? (item.volumeMounts as RecordValue[])
                : []
              const next = clone(all)
              fn(next)
              setOptional(item, 'volumeMounts', next)
            })
          )
        const mountAt = (all: RecordValue[], mountIndex: number) =>
          all.filter((item) => record(item).name === volumeName)[mountIndex]
        return (
          <div
            key={`${field}-${index}`}
            className='space-y-2 rounded-md bg-[#e9edf3] px-4 py-3'
          >
            <div className='flex flex-wrap items-center justify-between gap-2'>
              <div className='flex min-w-0 items-center gap-1.5'>
                <Box className='size-4 shrink-0 text-[#646464]' />
                <span className='text-xs text-[#606266]'>容器：</span>
                <span className='font-mono text-xs font-semibold break-all text-[#31598e]'>
                  {containerName}
                </span>
                {field === 'initContainers' && (
                  <Badge variant='outline' className='text-xs'>
                    初始化容器
                  </Badge>
                )}
              </div>
              <Button
                type='button'
                variant='ghost'
                size='sm'
                disabled={disabled || volumeName === ''}
                aria-label={`为容器 ${containerName} 挂载数据卷 ${volumeIndex + 1}`}
                className='h-11 px-3 text-xs text-[#31598e] sm:h-9'
                onClick={() =>
                  updateMounts((next) => {
                    next.push({ name: volumeName })
                  })
                }
              >
                <Plus className='size-3.5' /> 添加挂载
              </Button>
            </div>
            {mounts.length === 0 ? (
              <p className='text-sm text-[#64748b]'>未挂载</p>
            ) : (
              mounts.map((mount, mountIndex) => (
                <div
                  key={mountIndex}
                  className='space-y-2 rounded-md bg-white p-3'
                >
                  <div className='flex flex-wrap items-center justify-between gap-2'>
                    <Input
                      aria-label={`数据卷 ${volumeIndex + 1} 容器 ${containerName} 挂载路径 ${mountIndex + 1}`}
                      value={stringValue(mount.mountPath)}
                      disabled={disabled}
                      placeholder='（必填）挂载到容器内路径，例如 /data'
                      className='h-11 w-full min-w-0 font-mono text-xs sm:h-9 sm:w-auto sm:min-w-40 sm:flex-1'
                      onChange={(event) =>
                        updateMounts((next) => {
                          const target = mountAt(next, mountIndex)
                          if (target)
                            setOptional(target, 'mountPath', event.target.value)
                        })
                      }
                    />
                    <div
                      role='radiogroup'
                      aria-label={`数据卷 ${volumeIndex + 1} 容器 ${containerName} 挂载模式 ${mountIndex + 1}`}
                      className='flex shrink-0 rounded-md border border-[#c8d4e5] bg-[#eef3fa] p-0.5'
                    >
                      {([true, false] as const).map((readOnly) => (
                        <button
                          key={String(readOnly)}
                          type='button'
                          role='radio'
                          aria-checked={(mount.readOnly === true) === readOnly}
                          disabled={disabled}
                          className={`h-11 rounded px-3 text-xs outline-none focus-visible:ring-2 focus-visible:ring-[#234883]/40 disabled:cursor-not-allowed disabled:opacity-50 sm:h-9 ${
                            (mount.readOnly === true) === readOnly
                              ? 'bg-[#234883] font-semibold text-white'
                              : 'text-[#536987]'
                          }`}
                          onClick={() =>
                            updateMounts((next) => {
                              const target = mountAt(next, mountIndex)
                              if (target)
                                setOptional(
                                  target,
                                  'readOnly',
                                  readOnly || undefined
                                )
                            })
                          }
                        >
                          {readOnly ? '只读' : '读写'}
                        </button>
                      ))}
                    </div>
                    <Button
                      type='button'
                      variant='ghost'
                      size='icon'
                      disabled={disabled}
                      aria-label={`数据卷 ${volumeIndex + 1} 移除容器 ${containerName} 的挂载 ${mountIndex + 1}`}
                      className='size-11 text-[#9b6571] hover:bg-[#fdf0f1] hover:text-[#c2404e] sm:size-9'
                      onClick={() =>
                        updateMounts((next) => {
                          const indexes = next
                            .map((item, itemIndex) => ({ item, itemIndex }))
                            .filter(
                              ({ item }) => record(item).name === volumeName
                            )
                            .map(({ itemIndex }) => itemIndex)
                          const removeAt = indexes[mountIndex]
                          if (removeAt != null) next.splice(removeAt, 1)
                        })
                      }
                    >
                      <Trash2 className='size-4' />
                    </Button>
                  </div>
                  <div className='grid gap-2 sm:grid-cols-[1fr_10rem]'>
                    <Input
                      aria-label={`数据卷 ${volumeIndex + 1} 容器 ${containerName} 子路径 ${mountIndex + 1}`}
                      value={stringValue(mount.subPath ?? mount.subPathExpr)}
                      disabled={disabled}
                      placeholder='（选填）subPath / subPathExpr，$(VAR_NAME) 使用环境变量'
                      className='h-11 font-mono text-xs sm:h-9'
                      onChange={(event) =>
                        updateMounts((next) => {
                          const target = mountAt(next, mountIndex)
                          if (!target) return
                          delete target.subPath
                          delete target.subPathExpr
                          const value = event.target.value.trim()
                          if (value)
                            target[
                              value.includes('$(') ? 'subPathExpr' : 'subPath'
                            ] = value
                        })
                      }
                    />
                    <select
                      aria-label={`数据卷 ${volumeIndex + 1} 容器 ${containerName} 挂载传播 ${mountIndex + 1}`}
                      value={stringValue(mount.mountPropagation)}
                      disabled={disabled}
                      className='h-11 rounded-md border border-input bg-white px-2 text-xs text-[#536987] outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50 sm:h-9'
                      onChange={(event) =>
                        updateMounts((next) => {
                          const target = mountAt(next, mountIndex)
                          if (target)
                            setOptional(
                              target,
                              'mountPropagation',
                              event.target.value
                            )
                        })
                      }
                    >
                      <option value=''>传播：None（默认）</option>
                      <option value='None'>None</option>
                      <option value='HostToContainer'>HostToContainer</option>
                      <option value='Bidirectional'>Bidirectional</option>
                    </select>
                  </div>
                  {stringValue(mount.subPath ?? mount.subPathExpr).startsWith(
                    '/'
                  ) && (
                    <p className='text-xs text-destructive'>
                      子路径必须是相对路径
                    </p>
                  )}
                </div>
              ))
            )}
          </div>
        )
      })}
    </div>
  )
}

function VolumeMountOverview({
  obj,
  volumeName,
  disabled,
  onEdit,
}: {
  obj: K8sObject
  volumeName: string
  disabled: boolean
  onEdit: () => void
}) {
  const mounts = CONTAINER_FIELDS.flatMap((field) =>
    containersOf(obj, field).flatMap((container) =>
      (Array.isArray(container.volumeMounts)
        ? (container.volumeMounts as RecordValue[])
        : []
      )
        .filter((mount) => record(mount).name === volumeName)
        .map((mount) => ({
          container: stringValue(container.name),
          init: field === 'initContainers',
          mount,
        }))
    )
  )
  return (
    <div className='mt-3 space-y-2 border-t border-dashed border-[#a7b6cd] pt-3'>
      <p className='text-xs font-semibold text-[#31598e]'>挂载到容器</p>
      {mounts.map(({ container, init, mount }, index) => (
        <div
          key={index}
          className='flex min-w-0 flex-wrap items-center gap-x-4 gap-y-1 rounded-md bg-[#e9edf3] px-4 py-2 text-xs font-semibold text-[#304969]'
        >
          <span className='flex min-w-0 items-center gap-1.5 sm:w-48'>
            <Box className='size-4 shrink-0 text-[#646464]' />
            <span className='font-mono break-all'>{container}</span>
            {init && (
              <Badge variant='outline' className='shrink-0 text-xs font-normal'>
                初始化容器
              </Badge>
            )}
          </span>
          <span className='flex min-w-0 items-center gap-1.5 font-mono break-all'>
            <FolderInput className='size-4 shrink-0 text-[#646464]' />
            {stringValue(mount.mountPath) || '未设置挂载路径'}
          </span>
          <span className='rounded bg-white px-1.5 py-0.5'>
            {mount.readOnly === true ? '只读' : '读写'}
          </span>
          {Boolean(mount.subPath || mount.subPathExpr) && (
            <span className='min-w-0 font-normal break-all text-[#657fa8]'>
              子路径：{stringValue(mount.subPath ?? mount.subPathExpr)}
            </span>
          )}
        </div>
      ))}
      {mounts.length === 0 && (
        <div className='rounded-lg border border-[#f0c8ce] bg-[#fff4f5] px-3 py-2 text-xs text-[#a52f3d]'>
          数据卷至少应该挂载到一个容器中。
          <button
            type='button'
            disabled={disabled}
            className='ms-2 inline-flex min-h-11 items-center rounded px-1 font-semibold underline outline-none focus-visible:ring-2 focus-visible:ring-[#234883]/40 disabled:opacity-50'
            onClick={onEdit}
          >
            编辑挂载
          </button>
        </div>
      )}
    </div>
  )
}

function StorageTab({
  obj,
  params,
  disabled,
  updateDraft,
}: {
  obj: K8sObject
  params: K8sObjectParams
  disabled: boolean
  updateDraft: UpdateDraft
}) {
  const [editingVolumes, setEditingVolumes] = useState<Record<number, boolean>>(
    {}
  )
  const volumes = Array.isArray(podSpecOf(obj).volumes)
    ? (podSpecOf(obj).volumes as RecordValue[])
    : []
  const pvcs = useResourceList(params, 'persistentvolumeclaims')
  const configMaps = useResourceList(params, 'configmaps', {
    enabled: volumes.some(
      (volume) => volumeSourceType(volume) === 'configMap'
    ),
  })
  const secrets = useResourceList(params, 'secrets', {
    enabled: volumes.some((volume) => volumeSourceType(volume) === 'secret'),
  })

  const updateVolumes = (fn: (next: RecordValue[]) => void) =>
    updateDraft((draft) =>
      updatePodSpec(draft, (spec) => {
        const next = Array.isArray(spec.volumes)
          ? clone(spec.volumes as RecordValue[])
          : []
        fn(next)
        setOptional(spec, 'volumes', next)
      })
    )
  const updateVolume = (index: number, fn: (volume: RecordValue) => void) =>
    updateVolumes((next) => {
      const volume = record(next[index])
      next[index] = volume
      fn(volume)
    })
  const eachContainer = (
    spec: RecordValue,
    fn: (container: RecordValue) => void
  ) => {
    for (const field of CONTAINER_FIELDS) {
      if (!Array.isArray(spec[field])) continue
      spec[field] = (spec[field] as RecordValue[]).map((container) => {
        const next = record(container)
        fn(next)
        return next
      })
    }
  }
  const renameVolume = (index: number, name: string) => {
    const oldName = stringValue(volumes[index]?.name)
    updateDraft((draft) =>
      updatePodSpec(draft, (spec) => {
        const next = Array.isArray(spec.volumes)
          ? clone(spec.volumes as RecordValue[])
          : []
        const volume = record(next[index])
        next[index] = volume
        volume.name = name
        setOptional(spec, 'volumes', next)
        if (oldName === name) return
        // Mounts always follow the previous name — including the transient
        // '' produced while the user clears and retypes. Empty refs are only
        // moved when no other volume is unnamed, so unrelated empty mounts
        // stay untouched.
        if (
          oldName === '' &&
          next.some(
            (item, itemIndex) =>
              itemIndex !== index && stringValue(item.name) === ''
          )
        )
          return
        eachContainer(spec, (container) => {
          if (!Array.isArray(container.volumeMounts)) return
          container.volumeMounts = (
            container.volumeMounts as RecordValue[]
          ).map((mount) =>
            record(mount).name === oldName ? { ...mount, name } : mount
          )
        })
      })
    )
  }
  const removeVolume = (index: number) => {
    const oldName = stringValue(volumes[index]?.name)
    updateDraft((draft) =>
      updatePodSpec(draft, (spec) => {
        const next = (
          Array.isArray(spec.volumes)
            ? clone(spec.volumes as RecordValue[])
            : []
        ).filter((_, itemIndex) => itemIndex !== index)
        setOptional(spec, 'volumes', next)
        if (!oldName) return
        eachContainer(spec, (container) => {
          if (!Array.isArray(container.volumeMounts)) return
          const kept = (container.volumeMounts as RecordValue[]).filter(
            (mount) => record(mount).name !== oldName
          )
          setOptional(container, 'volumeMounts', kept)
        })
      })
    )
    setEditingVolumes((current) =>
      Object.fromEntries(
        Object.entries(current)
          .map(([key, value]) => [Number(key), value] as const)
          .filter(([key]) => key !== index)
          .map(([key, value]) => [key > index ? key - 1 : key, value])
      )
    )
  }

  return (
    <div className='space-y-4'>
      <SectionCard
        title='数据卷'
        description='定义数据卷，并将其挂载到工作负载的容器中。'
      >
        <div
          className={`grid gap-4 ${volumes.length > 1 ? 'xl:grid-cols-2' : ''}`}
        >
          {volumes.map((volume, index) => {
            const type = volumeSourceType(volume)
            const supported = isSupportedSource(type)
            const volumeName = stringValue(volume.name)
            const editing = editingVolumes[index] === true
            const sourceName = stringValue(
              record(volume[type ?? ''])[
                type === 'configMap' ? 'name' : 'secretName'
              ]
            )
            const claimName = stringValue(
              record(volume.persistentVolumeClaim).claimName
            )
            const pvc =
              type === 'persistentVolumeClaim'
                ? pvcs.items.find(
                    (item) =>
                      stringValue(record(item.metadata).name) === claimName
                  )
                : undefined
            const pvcSpec = record(pvc?.spec)
            const keySuggestions =
              type === 'configMap'
                ? resourceDataKeys(configMaps.items, sourceName)
                : type === 'secret'
                  ? resourceDataKeys(secrets.items, sourceName)
                  : []
            const source = record(type ? volume[type] : undefined)
            const sourceItems = Array.isArray(source.items) ? source.items : []
            const infoFields: { label: string; value: string }[] = [
              { label: '数据卷名称', value: volumeName || '-' },
              {
                label: '类型',
                value: supported
                  ? VOLUME_SOURCE_LABELS[type]
                  : (type ?? '未配置'),
              },
            ]
            switch (type) {
              case 'persistentVolumeClaim':
                infoFields.push({
                  label: '存储卷声明',
                  value: claimName || '-',
                })
                if (pvc)
                  infoFields.push(
                    {
                      label: '存储类型',
                      value: stringValue(pvcSpec.storageClassName) || '默认',
                    },
                    {
                      label: '容量',
                      value:
                        stringValue(
                          record(record(pvcSpec.resources).requests).storage
                        ) || '-',
                    },
                    {
                      label: '访问模式',
                      value: Array.isArray(pvcSpec.accessModes)
                        ? pvcSpec.accessModes.join(', ')
                        : '-',
                    }
                  )
                break
              case 'emptyDir':
                infoFields.push({
                  label: '介质',
                  value:
                    source.medium === 'Memory' ? 'Memory（tmpfs）' : '节点磁盘',
                })
                if (source.sizeLimit)
                  infoFields.push({
                    label: '容量上限',
                    value: stringValue(source.sizeLimit),
                  })
                break
              case 'hostPath':
                infoFields.push({
                  label: '路径',
                  value: stringValue(source.path) || '-',
                })
                if (source.type)
                  infoFields.push({
                    label: '类型',
                    value: stringValue(source.type),
                  })
                break
              case 'nfs':
                infoFields.push(
                  { label: '服务器', value: stringValue(source.server) || '-' },
                  { label: '路径', value: stringValue(source.path) || '-' }
                )
                break
              case 'configMap':
                infoFields.push({
                  label: '配置字典',
                  value: stringValue(source.name) || '-',
                })
                break
              case 'secret':
                infoFields.push({
                  label: '密文',
                  value: stringValue(source.secretName) || '-',
                })
                break
            }
            if (
              (type === 'configMap' || type === 'secret') &&
              sourceItems.length > 0
            )
              infoFields.push({
                label: 'KeyToPath',
                value: `${sourceItems.length} 项`,
              })
            return (
              <div
                key={index}
                className={`min-w-0 rounded-xl border border-[#c9d4e4] bg-white p-4 ${
                  volumes.length > 1 &&
                  (editing ||
                    (volumes.length % 2 === 1 && index === volumes.length - 1))
                    ? 'xl:col-span-2'
                    : ''
                } ${editing ? 'space-y-4' : ''}`}
              >
                {editing ? (
                  <>
                    <div className='flex items-center justify-between gap-2'>
                      <span className='flex items-center gap-2'>
                        <span className='h-4 w-1 rounded-full bg-[#234883]' />
                        <span className='text-sm font-semibold text-[#234883]'>
                          定义数据卷
                        </span>
                      </span>
                      <div className='flex shrink-0 gap-1'>
                        <Button
                          type='button'
                          variant='ghost'
                          size='sm'
                          disabled={disabled}
                          aria-label={`完成数据卷 ${index + 1}`}
                          className='h-11 px-2 text-xs text-[#31598e] sm:h-9'
                          onClick={() =>
                            setEditingVolumes((current) => ({
                              ...current,
                              [index]: false,
                            }))
                          }
                        >
                          <CheckCircle2 className='size-3.5' /> 确定
                        </Button>
                        <Button
                          type='button'
                          variant='ghost'
                          size='sm'
                          disabled={disabled}
                          aria-label={`删除数据卷 ${index + 1}`}
                          className='h-11 px-2 text-xs text-[#f44656] hover:bg-[#fdf0f1] sm:h-9'
                          onClick={() => removeVolume(index)}
                        >
                          <Trash2 className='size-3.5' /> 删除
                        </Button>
                      </div>
                    </div>
                    <div className='flex flex-col gap-3 sm:flex-row sm:items-start'>
                      <span className='shrink-0 text-sm text-[#606266] sm:w-28 sm:text-right sm:pt-2.5'>
                        数据卷名称
                        <span className='text-[#f44656]'> *</span>
                      </span>
                      <Input
                        aria-label={`数据卷 ${index + 1} 名称`}
                        value={volumeName}
                        disabled={disabled}
                        placeholder='数据卷名称'
                        className='h-11 min-w-0 flex-1 font-mono text-xs sm:h-9'
                        onChange={(event) =>
                          renameVolume(index, event.target.value)
                        }
                      />
                    </div>
                    <SourceField label='来源类型'>
                      <div
                        role='group'
                        aria-label={`数据卷 ${index + 1} 来源类型`}
                        className='grid w-full grid-cols-2 gap-2 sm:inline-flex sm:w-auto sm:max-w-full sm:flex-wrap sm:gap-0 sm:overflow-hidden sm:rounded-md sm:border sm:border-[#dcdfe6]'
                      >
                        {!supported && type != null && (
                          <span className='col-span-2 inline-flex h-11 items-center justify-center rounded-md border border-[#f0d9b1] bg-[#fff9ed] px-3 text-xs text-[#94621c] sm:col-span-auto sm:h-9 sm:justify-start sm:rounded-none sm:border-0 sm:border-r'>
                            当前：{type}
                          </span>
                        )}
                        {VOLUME_SOURCE_TYPES.map((item) => (
                          <button
                            key={item}
                            type='button'
                            aria-pressed={type === item}
                            disabled={disabled}
                            className='h-11 rounded-md border border-[#dcdfe6] bg-white px-4 text-xs whitespace-nowrap text-[#303133] outline-none hover:bg-[#f5f7fa] focus-visible:ring-2 focus-visible:ring-[#234883]/40 focus-visible:ring-inset disabled:cursor-not-allowed disabled:opacity-50 aria-pressed:bg-[#234883] aria-pressed:font-semibold aria-pressed:text-white sm:h-9 sm:rounded-none sm:border-0 sm:border-r sm:last:border-r-0'
                            onClick={() =>
                              updateVolume(index, (next) =>
                                switchVolumeSource(next, item)
                              )
                            }
                          >
                            {VOLUME_SOURCE_LABELS[item]}
                          </button>
                        ))}
                      </div>
                    </SourceField>
                    <div className='space-y-3 rounded-lg border border-[#ebeef5] bg-[#f7f8fa] p-4'>
                      <p className='text-sm font-semibold text-[#303133]'>
                        {supported
                          ? VOLUME_SOURCE_LABELS[type]
                          : (type ?? '选择来源类型')}
                      </p>
                      {supported ? (
                        <VolumeSourceFields
                          index={index}
                          volume={volume}
                          type={type}
                          disabled={disabled}
                          lists={{
                            pvc: pvcs,
                            configMap: configMaps,
                            secret: secrets,
                          }}
                          keySuggestions={keySuggestions}
                          onPatch={(patch) =>
                            updateVolume(index, (next) =>
                              patchVolumeSource(next, type, patch)
                            )
                          }
                        />
                      ) : (
                        <div className='rounded-md border border-[#f0d9b1] bg-[#fff9ed] px-3 py-2 text-xs text-[#94621c]'>
                          {type == null
                            ? '尚未选择来源类型。'
                            : `来源类型 ${type} 暂不支持表单编辑，内容将原样保留。`}
                        </div>
                      )}
                    </div>
                    <VolumeMountRows
                      volumeIndex={index}
                      volumeName={volumeName}
                      obj={obj}
                      disabled={disabled}
                      updateDraft={updateDraft}
                    />
                  </>
                ) : (
                  <>
                    <div className='flex gap-4'>
                      <HardDrive className='size-10 shrink-0 text-[#b6c2cd]' />
                      <div className='flex min-w-0 flex-1 flex-wrap content-start gap-x-6 gap-y-3'>
                        {infoFields.map((info, infoIndex) => (
                          <div key={infoIndex} className='min-w-0'>
                            <p className='text-xs text-[#7d8da6]'>
                              {info.label}
                            </p>
                            <p className='font-mono text-sm font-semibold break-all text-[#1c355e]'>
                              {info.value}
                            </p>
                          </div>
                        ))}
                      </div>
                      <div className='flex w-16 shrink-0 flex-col items-end'>
                        <Button
                          type='button'
                          variant='ghost'
                          size='sm'
                          disabled={disabled}
                          aria-label={`编辑数据卷 ${index + 1}`}
                          className='h-11 px-2 text-xs text-[#31598e] sm:h-9'
                          onClick={() =>
                            setEditingVolumes((current) => ({
                              ...current,
                              [index]: true,
                            }))
                          }
                        >
                          <Pencil className='size-3.5' /> 编辑
                        </Button>
                        <Button
                          type='button'
                          variant='ghost'
                          size='sm'
                          disabled={disabled}
                          aria-label={`删除数据卷 ${index + 1}`}
                          className='h-11 px-2 text-xs text-[#f44656] hover:bg-[#fdf0f1] sm:h-9'
                          onClick={() => removeVolume(index)}
                        >
                          <Trash2 className='size-3.5' /> 删除
                        </Button>
                      </div>
                    </div>
                    <VolumeMountOverview
                      obj={obj}
                      volumeName={volumeName}
                      disabled={disabled}
                      onEdit={() =>
                        setEditingVolumes((current) => ({
                          ...current,
                          [index]: true,
                        }))
                      }
                    />
                  </>
                )}
              </div>
            )
          })}
          {volumes.length === 0 && (
            <p className='py-4 text-center text-sm text-[#7d8da6]'>
              暂无数据卷
            </p>
          )}
          <Button
            type='button'
            variant='outline'
            disabled={disabled}
            className='col-span-full h-14 w-full rounded-xl border border-dashed border-[#8fa1bd] bg-white text-[#234883] hover:bg-[#f5f8fc]'
            onClick={() => {
              setEditingVolumes((current) => ({
                ...current,
                [volumes.length]: true,
              }))
              updateVolumes((next) => {
                const names = new Set(
                  next.map((item) => stringValue(item.name))
                )
                let serial = 1
                while (names.has(`volume-${serial}`)) serial += 1
                next.push({ name: `volume-${serial}`, emptyDir: {} })
              })
            }}
          >
            <Plus className='size-4' /> 添加数据卷
          </Button>
        </div>
      </SectionCard>
    </div>
  )
}

const ADVANCED_SELECT_CLASS =
  'h-11 w-full rounded-md border border-input bg-transparent px-3 text-sm outline-none focus-visible:ring-2 focus-visible:ring-[#234883]/40 disabled:cursor-not-allowed disabled:opacity-50 sm:h-9'

const ADVANCED_INPUT_CLASS = 'h-11 max-w-xl font-mono text-sm sm:h-9'

// Kubernetes IntOrString: pure digits become numbers, everything else
// (e.g. "25%") stays a string; empty deletes the property.
const intOrString = (value: string): number | string | undefined => {
  const trimmed = value.trim()
  if (trimmed === '') return undefined
  return /^\d+$/.test(trimmed) ? Number(trimmed) : trimmed
}

function OptNumberInput({
  ariaLabel,
  value,
  disabled,
  min = 0,
  onChange,
}: {
  ariaLabel: string
  value: unknown
  disabled: boolean
  min?: number
  onChange: (next: number | undefined) => void
}) {
  return (
    <Input
      type='number'
      step={1}
      min={min}
      aria-label={ariaLabel}
      value={value == null ? '' : String(value)}
      disabled={disabled}
      className={ADVANCED_INPUT_CLASS}
      onChange={(event) => {
        const trimmed = event.target.value.trim()
        if (trimmed === '') return onChange(undefined)
        const next = Number(trimmed)
        if (!Number.isNaN(next)) onChange(next)
      }}
    />
  )
}

function IntOrStringInput({
  ariaLabel,
  value,
  disabled,
  onChange,
}: {
  ariaLabel: string
  value: unknown
  disabled: boolean
  onChange: (next: number | string | undefined) => void
}) {
  return (
    <Input
      aria-label={ariaLabel}
      value={value == null ? '' : String(value)}
      disabled={disabled}
      placeholder='整数或百分比，例如 1 或 25%'
      className={ADVANCED_INPUT_CLASS}
      onChange={(event) => onChange(intOrString(event.target.value))}
    />
  )
}

function TriStateSelect({
  ariaLabel,
  value,
  disabled,
  onChange,
}: {
  ariaLabel: string
  value: boolean | undefined
  disabled: boolean
  onChange: (next: boolean | undefined) => void
}) {
  return (
    <select
      aria-label={ariaLabel}
      value={value === undefined ? '' : String(value)}
      disabled={disabled}
      className={ADVANCED_SELECT_CLASS}
      onChange={(event) =>
        onChange(
          event.target.value === ''
            ? undefined
            : event.target.value === 'true'
        )
      }
    >
      <option value=''>继承默认</option>
      <option value='true'>启用</option>
      <option value='false'>禁用</option>
    </select>
  )
}

function ImagePullSecretsEditor({
  values,
  secrets,
  disabled,
  onChange,
}: {
  values: RecordValue[]
  secrets: ResourceListResult
  disabled: boolean
  onChange: (next: RecordValue[] | undefined) => void
}) {
  const update = (fn: (next: RecordValue[]) => void) => {
    const next = clone(values)
    fn(next)
    onChange(next.length ? next : undefined)
  }
  return (
    <div className='space-y-3 rounded-lg border border-[#ebeef5] bg-[#f8fafc] p-3'>
      <div className='flex items-center justify-between gap-2'>
        <span className='text-sm font-semibold text-[#536987]'>
          镜像拉取凭据 imagePullSecrets
        </span>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          disabled={disabled}
          aria-label='添加镜像拉取凭据'
          className='h-11 px-2 text-xs text-[#31598e] sm:h-9'
          onClick={() => update((next) => next.push({ name: '' }))}
        >
          <Plus className='size-3.5' /> 添加
        </Button>
      </div>
      {values.length === 0 ? (
        <p className='text-sm text-[#64748b]'>
          暂无条目，使用集群默认拉取凭据
        </p>
      ) : (
        values.map((item, index) => (
          <div key={index} className='flex flex-wrap items-center gap-2'>
            <div className='flex min-w-0 max-w-xl flex-1'>
              <ResourceNameSelect
                ariaLabel={`镜像拉取凭据 ${index + 1} 名称`}
                value={stringValue(item.name)}
                options={secrets.names}
                disabled={disabled}
                loading={secrets.isLoading}
                loadFailed={secrets.isError}
                placeholder='Secret 名称'
                selectClassName={ADVANCED_SELECT_CLASS}
                inputClassName='h-11 font-mono text-sm sm:h-9'
                onChange={(name) =>
                  update((next) => {
                    next[index] = { ...record(next[index]) }
                    setOptional(next[index], 'name', name)
                  })
                }
              />
            </div>
            <Button
              type='button'
              variant='ghost'
              size='icon'
              disabled={disabled}
              aria-label={`删除镜像拉取凭据 ${index + 1}`}
              className='size-11 shrink-0 text-[#9b6571] hover:bg-[#fdf0f1] hover:text-[#c2404e] sm:size-9'
              onClick={() => update((next) => next.splice(index, 1))}
            >
              <Trash2 className='size-4' />
            </Button>
          </div>
        ))
      )}
    </div>
  )
}

function TolerationsEditor({
  tolerations,
  disabled,
  onChange,
}: {
  tolerations: RecordValue[]
  disabled: boolean
  onChange: (next: RecordValue[] | undefined) => void
}) {
  const update = (fn: (next: RecordValue[]) => void) => {
    const next = clone(tolerations)
    fn(next)
    onChange(next.length ? next : undefined)
  }
  const updateItem = (
    index: number,
    fn: (item: RecordValue) => void
  ) =>
    update((next) => {
      const item = { ...record(next[index]) }
      next[index] = item
      fn(item)
    })
  return (
    <div className='space-y-3 rounded-lg border border-[#ebeef5] bg-[#f8fafc] p-3'>
      <div className='flex items-center justify-between gap-2'>
        <span className='text-sm font-semibold text-[#536987]'>容忍度</span>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          disabled={disabled}
          aria-label='添加容忍度'
          className='h-11 px-2 text-xs text-[#31598e] sm:h-9'
          onClick={() => update((next) => next.push({ operator: 'Exists' }))}
        >
          <Plus className='size-3.5' /> 添加容忍度
        </Button>
      </div>
      {tolerations.length === 0 ? (
        <p className='text-sm text-[#64748b]'>
          暂无容忍度；带有污点的节点可能无法调度或维持该 Pod。
        </p>
      ) : (
        tolerations.map((item, index) => {
          const operator = stringValue(item.operator) || 'Equal'
          const effect = stringValue(item.effect)
          return (
            <div
              key={index}
              className='flex flex-wrap items-center gap-2 rounded-md bg-white p-3'
            >
              <Input
                aria-label={`容忍度 ${index + 1} 键`}
                value={stringValue(item.key)}
                disabled={disabled}
                placeholder='键（空表示匹配全部）'
                className='h-11 w-full min-w-0 font-mono text-sm sm:h-9 sm:w-40 sm:flex-none'
                onChange={(event) =>
                  updateItem(index, (item) =>
                    setOptional(item, 'key', event.target.value)
                  )
                }
              />
              <select
                aria-label={`容忍度 ${index + 1} 操作符`}
                value={operator}
                disabled={disabled}
                className={`${ADVANCED_SELECT_CLASS} w-28`}
                onChange={(event) =>
                  updateItem(index, (item) => {
                    item.operator = event.target.value
                    if (event.target.value === 'Exists') delete item.value
                  })
                }
              >
                <option value='Exists'>Exists</option>
                <option value='Equal'>Equal</option>
              </select>
              {operator === 'Equal' && (
                <Input
                  aria-label={`容忍度 ${index + 1} 值`}
                  value={stringValue(item.value)}
                  disabled={disabled}
                  placeholder='值'
                  className='h-11 min-w-0 flex-1 font-mono text-sm sm:h-9'
                  onChange={(event) =>
                    updateItem(index, (item) =>
                      setOptional(item, 'value', event.target.value)
                    )
                  }
                />
              )}
              <select
                aria-label={`容忍度 ${index + 1} 效果`}
                value={effect}
                disabled={disabled}
                className={`${ADVANCED_SELECT_CLASS} w-44`}
                onChange={(event) =>
                  updateItem(index, (item) => {
                    setOptional(item, 'effect', event.target.value)
                    if (event.target.value !== 'NoExecute')
                      delete item.tolerationSeconds
                  })
                }
              >
                <option value=''>不限效果</option>
                <option value='NoSchedule'>NoSchedule</option>
                <option value='PreferNoSchedule'>PreferNoSchedule</option>
                <option value='NoExecute'>NoExecute</option>
              </select>
              {effect === 'NoExecute' && (
                <OptNumberInput
                  ariaLabel={`容忍度 ${index + 1} 秒数`}
                  value={item.tolerationSeconds}
                  disabled={disabled}
                  onChange={(next) =>
                    updateItem(index, (item) =>
                      setOptional(item, 'tolerationSeconds', next)
                    )
                  }
                />
              )}
              <Button
                type='button'
                variant='ghost'
                size='icon'
                disabled={disabled}
                aria-label={`删除容忍度 ${index + 1}`}
                className='size-11 shrink-0 text-[#9b6571] hover:bg-[#fdf0f1] hover:text-[#c2404e] sm:size-9'
                onClick={() => update((next) => next.splice(index, 1))}
              >
                <Trash2 className='size-4' />
              </Button>
            </div>
          )
        })
      )}
    </div>
  )
}

function AdvancedTab({
  obj,
  params,
  disabled,
  updateDraft,
}: {
  obj: K8sObject
  params: K8sObjectParams
  disabled: boolean
  updateDraft: UpdateDraft
}) {
  const spec = record(obj.spec)
  const podSpec = podSpecOf(obj)
  const updateSpec = (fn: (spec: RecordValue) => void) =>
    updateDraft((draft) => {
      const next = record(draft.spec)
      draft.spec = next
      fn(next)
    })
  const updatePodSpecField = (fn: (spec: RecordValue) => void) =>
    updateDraft((draft) => updatePodSpec(draft, fn))
  const kind = obj.kind ?? ''
  const isLongRunning =
    kind === 'Deployment' || kind === 'StatefulSet' || kind === 'DaemonSet'
  const strategy = record(spec.strategy)
  const strategyType = stringValue(strategy.type) || 'RollingUpdate'
  const strategyRolling = record(strategy.rollingUpdate)
  const updateStrategy = record(spec.updateStrategy)
  const updateStrategyType = stringValue(updateStrategy.type) || 'RollingUpdate'
  const updateRolling = record(updateStrategy.rollingUpdate)
  const podSC = record(podSpec.securityContext)
  const pullSecrets = Array.isArray(podSpec.imagePullSecrets)
    ? (podSpec.imagePullSecrets as RecordValue[])
    : []
  const tolerations = Array.isArray(podSpec.tolerations)
    ? (podSpec.tolerations as RecordValue[])
    : []
  const serviceAccounts = useResourceList(params, 'serviceaccounts')
  const secretList = useResourceList(params, 'secrets')
  const priorityClasses = useResourceList(params, 'priorityclasses', {
    group: 'scheduling.k8s.io',
    clusterScoped: true,
  })
  const patchStrategy = (fn: (target: RecordValue) => void) =>
    updateSpec((next) => {
      const target = record(next.strategy)
      fn(target)
      next.strategy = target
    })
  const patchStrategyRolling = (fn: (target: RecordValue) => void) =>
    patchStrategy((parent) => {
      const target = record(parent.rollingUpdate)
      fn(target)
      setOptional(
        parent,
        'rollingUpdate',
        Object.keys(target).length ? target : undefined
      )
    })
  const patchUpdateStrategy = (fn: (target: RecordValue) => void) =>
    updateSpec((next) => {
      const target = record(next.updateStrategy)
      fn(target)
      next.updateStrategy = target
    })
  const patchUpdateRolling = (fn: (target: RecordValue) => void) =>
    patchUpdateStrategy((parent) => {
      const target = record(parent.rollingUpdate)
      fn(target)
      setOptional(
        parent,
        'rollingUpdate',
        Object.keys(target).length ? target : undefined
      )
    })
  const patchPodSC = (fn: (target: RecordValue) => void) =>
    updatePodSpecField((next) => {
      const target = record(next.securityContext)
      fn(target)
      setOptional(
        next,
        'securityContext',
        Object.keys(target).length ? target : undefined
      )
    })
  const boolOrUndef = (value: unknown): boolean | undefined =>
    value === true || value === false ? value : undefined
  return (
    <div className='space-y-4'>
      <SectionCard
        title='工作负载策略'
        description='副本数量和工作负载发布、调度相关的设置。'
      >
        {(kind === 'Deployment' || kind === 'StatefulSet') && (
          <Field label='副本数'>
            <OptNumberInput
              ariaLabel='副本数'
              value={spec.replicas}
              disabled={disabled}
              onChange={(next) =>
                updateSpec((spec) => setOptional(spec, 'replicas', next))
              }
            />
          </Field>
        )}
        {kind === 'Deployment' && (
          <>
            <Field label='更新策略'>
              <select
                aria-label='更新策略类型'
                value={strategyType}
                disabled={disabled}
                className={ADVANCED_SELECT_CLASS}
                onChange={(event) =>
                  patchStrategy((target) => {
                    target.type = event.target.value
                    if (event.target.value !== 'RollingUpdate')
                      delete target.rollingUpdate
                  })
                }
              >
                <option value='RollingUpdate'>RollingUpdate</option>
                <option value='Recreate'>Recreate</option>
              </select>
            </Field>
            {strategyType === 'RollingUpdate' && (
              <div className='space-y-4 rounded-lg border border-[#ebeef5] bg-[#f8fafc] p-3'>
                <p className='text-sm font-semibold text-[#536987]'>
                  滚动更新参数
                </p>
                <Field
                  label='最大超出数'
                  hint='maxSurge：整数或百分比，例如 1 或 25%'
                >
                  <IntOrStringInput
                    ariaLabel='滚动更新最大超出数'
                    value={strategyRolling.maxSurge}
                    disabled={disabled}
                    onChange={(next) =>
                      patchStrategyRolling((target) =>
                        setOptional(target, 'maxSurge', next)
                      )
                    }
                  />
                </Field>
                <Field
                  label='最大不可用'
                  hint='maxUnavailable：整数或百分比，例如 0 或 25%'
                >
                  <IntOrStringInput
                    ariaLabel='滚动更新最大不可用数'
                    value={strategyRolling.maxUnavailable}
                    disabled={disabled}
                    onChange={(next) =>
                      patchStrategyRolling((target) =>
                        setOptional(target, 'maxUnavailable', next)
                      )
                    }
                  />
                </Field>
              </div>
            )}
            <Field label='最小就绪秒数' hint='minReadySeconds'>
              <OptNumberInput
                ariaLabel='最小就绪秒数'
                value={spec.minReadySeconds}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'minReadySeconds', next)
                  )
                }
              />
            </Field>
            <Field label='进度超时秒数' hint='progressDeadlineSeconds'>
              <OptNumberInput
                ariaLabel='进度超时秒数'
                min={1}
                value={spec.progressDeadlineSeconds}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'progressDeadlineSeconds', next)
                  )
                }
              />
            </Field>
            <Field label='历史版本保留数' hint='revisionHistoryLimit'>
              <OptNumberInput
                ariaLabel='历史版本保留数'
                value={spec.revisionHistoryLimit}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'revisionHistoryLimit', next)
                  )
                }
              />
            </Field>
          </>
        )}
        {kind === 'StatefulSet' && (
          <>
            <Field label='服务名称' hint='既有 StatefulSet 的稳定网络标识，不可修改'>
              <ReadonlyValue>
                {stringValue(spec.serviceName) || '-'}
              </ReadonlyValue>
            </Field>
            <Field label='Pod 管理策略'>
              <select
                aria-label='Pod 管理策略'
                value={stringValue(spec.podManagementPolicy) || 'OrderedReady'}
                disabled={disabled}
                className={ADVANCED_SELECT_CLASS}
                onChange={(event) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'podManagementPolicy', event.target.value)
                  )
                }
              >
                <option value='OrderedReady'>OrderedReady</option>
                <option value='Parallel'>Parallel</option>
              </select>
            </Field>
            <Field label='更新策略'>
              <select
                aria-label='更新策略类型'
                value={updateStrategyType}
                disabled={disabled}
                className={ADVANCED_SELECT_CLASS}
                onChange={(event) =>
                  patchUpdateStrategy((target) => {
                    target.type = event.target.value
                    if (event.target.value !== 'RollingUpdate')
                      delete target.rollingUpdate
                  })
                }
              >
                <option value='RollingUpdate'>RollingUpdate</option>
                <option value='OnDelete'>OnDelete</option>
              </select>
            </Field>
            {updateStrategyType === 'RollingUpdate' && (
              <div className='space-y-4 rounded-lg border border-[#ebeef5] bg-[#f8fafc] p-3'>
                <p className='text-sm font-semibold text-[#536987]'>
                  滚动更新参数
                </p>
                <Field label='分区序号' hint='partition：序号大于等于该值的 Pod 才会更新'>
                  <OptNumberInput
                    ariaLabel='滚动更新分区序号'
                    value={updateRolling.partition}
                    disabled={disabled}
                    onChange={(next) =>
                      patchUpdateRolling((target) =>
                        setOptional(target, 'partition', next)
                      )
                    }
                  />
                </Field>
              </div>
            )}
            <Field label='最小就绪秒数' hint='minReadySeconds'>
              <OptNumberInput
                ariaLabel='最小就绪秒数'
                value={spec.minReadySeconds}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'minReadySeconds', next)
                  )
                }
              />
            </Field>
            <Field label='历史版本保留数' hint='revisionHistoryLimit'>
              <OptNumberInput
                ariaLabel='历史版本保留数'
                value={spec.revisionHistoryLimit}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'revisionHistoryLimit', next)
                  )
                }
              />
            </Field>
          </>
        )}
        {kind === 'DaemonSet' && (
          <>
            <Field label='更新策略'>
              <select
                aria-label='更新策略类型'
                value={updateStrategyType}
                disabled={disabled}
                className={ADVANCED_SELECT_CLASS}
                onChange={(event) =>
                  patchUpdateStrategy((target) => {
                    target.type = event.target.value
                    if (event.target.value !== 'RollingUpdate')
                      delete target.rollingUpdate
                  })
                }
              >
                <option value='RollingUpdate'>RollingUpdate</option>
                <option value='OnDelete'>OnDelete</option>
              </select>
            </Field>
            {updateStrategyType === 'RollingUpdate' && (
              <div className='space-y-4 rounded-lg border border-[#ebeef5] bg-[#f8fafc] p-3'>
                <p className='text-sm font-semibold text-[#536987]'>
                  滚动更新参数
                </p>
                <Field label='最大不可用' hint='maxUnavailable：整数或百分比'>
                  <IntOrStringInput
                    ariaLabel='滚动更新最大不可用数'
                    value={updateRolling.maxUnavailable}
                    disabled={disabled}
                    onChange={(next) =>
                      patchUpdateRolling((target) =>
                        setOptional(target, 'maxUnavailable', next)
                      )
                    }
                  />
                </Field>
              </div>
            )}
            <Field label='最小就绪秒数' hint='minReadySeconds'>
              <OptNumberInput
                ariaLabel='最小就绪秒数'
                value={spec.minReadySeconds}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'minReadySeconds', next)
                  )
                }
              />
            </Field>
            <Field label='历史版本保留数' hint='revisionHistoryLimit'>
              <OptNumberInput
                ariaLabel='历史版本保留数'
                value={spec.revisionHistoryLimit}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'revisionHistoryLimit', next)
                  )
                }
              />
            </Field>
          </>
        )}
        {kind === 'Job' && (
          <>
            <Field label='完成数'>
              <OptNumberInput
                ariaLabel='完成数'
                min={1}
                value={spec.completions}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) => setOptional(spec, 'completions', next))
                }
              />
            </Field>
            <Field label='并行数'>
              <OptNumberInput
                ariaLabel='并行数'
                min={1}
                value={spec.parallelism}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) => setOptional(spec, 'parallelism', next))
                }
              />
            </Field>
            <Field label='重试上限' hint='backoffLimit'>
              <OptNumberInput
                ariaLabel='重试上限'
                value={spec.backoffLimit}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) => setOptional(spec, 'backoffLimit', next))
                }
              />
            </Field>
            <Field label='活动截止秒数' hint='activeDeadlineSeconds'>
              <OptNumberInput
                ariaLabel='活动截止秒数'
                min={1}
                value={spec.activeDeadlineSeconds}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'activeDeadlineSeconds', next)
                  )
                }
              />
            </Field>
            <Field
              label='完成后保留秒数'
              hint='ttlSecondsAfterFinished：到期自动清理 Job'
            >
              <OptNumberInput
                ariaLabel='完成后保留秒数'
                value={spec.ttlSecondsAfterFinished}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'ttlSecondsAfterFinished', next)
                  )
                }
              />
            </Field>
          </>
        )}
        {kind === 'CronJob' && (
          <>
            <Field label='调度表达式' hint='例如：*/5 * * * *'>
              <Input
                aria-label='调度表达式'
                value={stringValue(spec.schedule)}
                disabled={disabled}
                className={ADVANCED_INPUT_CLASS}
                onChange={(event) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'schedule', event.target.value)
                  )
                }
              />
            </Field>
            <Field label='时区' hint='timeZone：例如 Asia/Shanghai，可选'>
              <Input
                aria-label='时区'
                value={stringValue(spec.timeZone)}
                disabled={disabled}
                className={ADVANCED_INPUT_CLASS}
                onChange={(event) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'timeZone', event.target.value)
                  )
                }
              />
            </Field>
            <Field label='并发策略'>
              <select
                aria-label='并发策略'
                value={stringValue(spec.concurrencyPolicy) || 'Allow'}
                disabled={disabled}
                className={ADVANCED_SELECT_CLASS}
                onChange={(event) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'concurrencyPolicy', event.target.value)
                  )
                }
              >
                <option value='Allow'>Allow</option>
                <option value='Forbid'>Forbid</option>
                <option value='Replace'>Replace</option>
              </select>
            </Field>
            <Field label='错过调度宽限' hint='startingDeadlineSeconds'>
              <OptNumberInput
                ariaLabel='错过调度宽限秒数'
                value={spec.startingDeadlineSeconds}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'startingDeadlineSeconds', next)
                  )
                }
              />
            </Field>
            <Field label='成功任务保留数' hint='successfulJobsHistoryLimit'>
              <OptNumberInput
                ariaLabel='成功任务保留数'
                value={spec.successfulJobsHistoryLimit}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'successfulJobsHistoryLimit', next)
                  )
                }
              />
            </Field>
            <Field label='失败任务保留数' hint='failedJobsHistoryLimit'>
              <OptNumberInput
                ariaLabel='失败任务保留数'
                value={spec.failedJobsHistoryLimit}
                disabled={disabled}
                onChange={(next) =>
                  updateSpec((spec) =>
                    setOptional(spec, 'failedJobsHistoryLimit', next)
                  )
                }
              />
            </Field>
            <Field label='暂停调度'>
              <label className='flex h-11 items-center gap-2 text-sm text-[#536987] sm:h-9'>
                <input
                  type='checkbox'
                  aria-label='暂停调度'
                  checked={spec.suspend === true}
                  disabled={disabled}
                  onChange={(event) =>
                    updateSpec((spec) =>
                      setOptional(
                        spec,
                        'suspend',
                        event.target.checked || undefined
                      )
                    )
                  }
                />{' '}
                暂停创建新的 Job
              </label>
            </Field>
          </>
        )}
      </SectionCard>
      <SectionCard
        title='Pod 身份与安全'
        description='服务账号、镜像拉取凭据和 Pod 安全上下文。'
      >
        <Field
          label='ServiceAccount'
          hint='下拉选择名称空间内已有的服务账号，也可手动输入'
        >
          <ResourceNameSelect
            ariaLabel='服务账号名称'
            value={stringValue(podSpec.serviceAccountName)}
            options={serviceAccounts.names}
            disabled={disabled}
            loading={serviceAccounts.isLoading}
            loadFailed={serviceAccounts.isError}
            placeholder='默认服务账号'
            selectClassName={`${ADVANCED_SELECT_CLASS} max-w-xl`}
            inputClassName={ADVANCED_INPUT_CLASS}
            onChange={(next) =>
              updatePodSpecField((spec) =>
                setOptional(spec, 'serviceAccountName', next)
              )
            }
          />
        </Field>
        <Field
          label='自动挂载令牌'
          hint='automountServiceAccountToken：控制是否向 Pod 挂载 API 凭据'
        >
          <TriStateSelect
            ariaLabel='自动挂载服务账号令牌'
            value={boolOrUndef(podSpec.automountServiceAccountToken)}
            disabled={disabled}
            onChange={(next) =>
              updatePodSpecField((spec) =>
                setOptional(spec, 'automountServiceAccountToken', next)
              )
            }
          />
        </Field>
        <ImagePullSecretsEditor
          values={pullSecrets}
          secrets={secretList}
          disabled={disabled}
          onChange={(next) =>
            updatePodSpecField((spec) =>
              setOptional(spec, 'imagePullSecrets', next)
            )
          }
        />
        <div className='space-y-4 rounded-lg border border-[#ebeef5] bg-[#f8fafc] p-3'>
          <p className='text-sm font-semibold text-[#536987]'>
            Pod 安全上下文 securityContext
          </p>
          <Field label='以非 root 运行' hint='runAsNonRoot'>
            <TriStateSelect
              ariaLabel='以非 root 运行'
              value={boolOrUndef(podSC.runAsNonRoot)}
              disabled={disabled}
              onChange={(next) =>
                patchPodSC((target) =>
                  setOptional(target, 'runAsNonRoot', next)
                )
              }
            />
          </Field>
          <Field label='运行用户 UID' hint='runAsUser'>
            <OptNumberInput
              ariaLabel='运行用户 UID'
              value={podSC.runAsUser}
              disabled={disabled}
              onChange={(next) =>
                patchPodSC((target) => setOptional(target, 'runAsUser', next))
              }
            />
          </Field>
          <Field label='运行组 GID' hint='runAsGroup'>
            <OptNumberInput
              ariaLabel='运行组 GID'
              value={podSC.runAsGroup}
              disabled={disabled}
              onChange={(next) =>
                patchPodSC((target) => setOptional(target, 'runAsGroup', next))
              }
            />
          </Field>
          <Field label='文件系统组' hint='fsGroup'>
            <OptNumberInput
              ariaLabel='文件系统组 GID'
              value={podSC.fsGroup}
              disabled={disabled}
              onChange={(next) =>
                patchPodSC((target) => setOptional(target, 'fsGroup', next))
              }
            />
          </Field>
          <p className='text-xs text-[#64748b]'>
            seccompProfile、seLinuxOptions 等其他安全上下文字段保持原样。
          </p>
        </div>
      </SectionCard>
      <SectionCard
        title='调度与容忍度'
        description='重启策略、节点亲和与容忍度等 Pod 调度设置。'
      >
        <Field label='重启策略'>
          {isLongRunning ? (
            <ReadonlyValue>Always</ReadonlyValue>
          ) : (
            <select
              aria-label='重启策略'
              value={stringValue(podSpec.restartPolicy) || 'Never'}
              disabled={disabled}
              className={ADVANCED_SELECT_CLASS}
              onChange={(event) =>
                updatePodSpecField((next) =>
                  setOptional(next, 'restartPolicy', event.target.value)
                )
              }
            >
              <option value='Never'>Never</option>
              <option value='OnFailure'>OnFailure</option>
            </select>
          )}
        </Field>
        <Field label='终止宽限期（秒）' hint='terminationGracePeriodSeconds'>
          <OptNumberInput
            ariaLabel='终止宽限期秒数'
            value={podSpec.terminationGracePeriodSeconds}
            disabled={disabled}
            onChange={(next) =>
              updatePodSpecField((spec) =>
                setOptional(spec, 'terminationGracePeriodSeconds', next)
              )
            }
          />
        </Field>
        <Field
          label='优先级类名'
          hint='priorityClassName：下拉选择集群中的 PriorityClass，也可手动输入'
        >
          <ResourceNameSelect
            ariaLabel='优先级类名'
            value={stringValue(podSpec.priorityClassName)}
            options={priorityClasses.names}
            disabled={disabled}
            loading={priorityClasses.isLoading}
            loadFailed={priorityClasses.isError}
            placeholder='不使用优先级类'
            selectClassName={`${ADVANCED_SELECT_CLASS} max-w-xl`}
            inputClassName={ADVANCED_INPUT_CLASS}
            onChange={(next) =>
              updatePodSpecField((spec) =>
                setOptional(spec, 'priorityClassName', next)
              )
            }
          />
        </Field>
        <Field label='调度器名称' hint='schedulerName：默认 default-scheduler'>
          <Input
            aria-label='调度器名称'
            value={stringValue(podSpec.schedulerName)}
            disabled={disabled}
            className={ADVANCED_INPUT_CLASS}
            onChange={(event) =>
              updatePodSpecField((next) =>
                setOptional(next, 'schedulerName', event.target.value)
              )
            }
          />
        </Field>
        <KeyValueEditor
          label='节点选择器'
          values={stringMap(podSpec.nodeSelector)}
          disabled={disabled}
          onChange={(values) =>
            updatePodSpecField((next) =>
              setOptional(next, 'nodeSelector', values)
            )
          }
        />
        <TolerationsEditor
          tolerations={tolerations}
          disabled={disabled}
          onChange={(next) =>
            updatePodSpecField((spec) => setOptional(spec, 'tolerations', next))
          }
        />
      </SectionCard>
      <SectionCard
        title='网络与 DNS'
        description='主机网络和 Pod DNS 解析相关设置。'
      >
        <Field
          label='主机网络'
          hint='hostNetwork：启用后 Pod 直接使用节点网络命名空间'
        >
          <TriStateSelect
            ariaLabel='使用主机网络'
            value={boolOrUndef(podSpec.hostNetwork)}
            disabled={disabled}
            onChange={(next) =>
              updatePodSpecField((spec) =>
                setOptional(spec, 'hostNetwork', next)
              )
            }
          />
        </Field>
        <Field
          label='DNS 策略'
          hint='使用主机网络时通常选择 ClusterFirstWithHostNet，本页不会自动切换'
        >
          <select
            aria-label='DNS 策略'
            value={stringValue(podSpec.dnsPolicy)}
            disabled={disabled}
            className={ADVANCED_SELECT_CLASS}
            onChange={(event) =>
              updatePodSpecField((next) =>
                setOptional(next, 'dnsPolicy', event.target.value)
              )
            }
          >
            <option value=''>集群默认</option>
            <option value='ClusterFirst'>ClusterFirst</option>
            <option value='ClusterFirstWithHostNet'>
              ClusterFirstWithHostNet
            </option>
            <option value='Default'>Default</option>
            <option value='None'>None</option>
          </select>
        </Field>
        <Field label='主机名' hint='hostname：覆盖 Pod 主机名'>
          <Input
            aria-label='主机名'
            value={stringValue(podSpec.hostname)}
            disabled={disabled}
            className={ADVANCED_INPUT_CLASS}
            onChange={(event) =>
              updatePodSpecField((next) =>
                setOptional(next, 'hostname', event.target.value)
              )
            }
          />
        </Field>
        <Field label='子域名' hint='subdomain：与 hostname 组成 FQDN'>
          <Input
            aria-label='子域名'
            value={stringValue(podSpec.subdomain)}
            disabled={disabled}
            className={ADVANCED_INPUT_CLASS}
            onChange={(event) =>
              updatePodSpecField((next) =>
                setOptional(next, 'subdomain', event.target.value)
              )
            }
          />
        </Field>
      </SectionCard>
      <SectionCard
        title='调度补充配置'
        description='亲和性、DNSConfig 等复杂字段继续通过 YAML 编辑，保存时原样保留。'
      >
        <div className='rounded-xl border border-[#e5ebf3] bg-[#f8fafd] p-3 text-sm leading-6 text-[#536987]'>
          <Info className='me-1 inline size-3.5' />
          可视化表单覆盖常用设置；未覆盖的高级 Kubernetes
          字段会在保存时原样保留。
        </div>
        <Field label='亲和性'>
          <ReadonlyValue>
            {podSpec.affinity ? '已配置' : '未配置'}
          </ReadonlyValue>
        </Field>
      </SectionCard>
    </div>
  )
}

function ServiceRouteTab({ obj }: { obj: K8sObject }) {
  const ports = containersOf(obj, 'containers').flatMap((container) =>
    list(container.ports).map((port) => ({
      container: stringValue(container.name),
      name: stringValue(port.name),
      port: stringValue(port.containerPort),
      protocol: stringValue(port.protocol) || 'TCP',
    }))
  )
  return (
    <div className='space-y-4'>
      <SectionCard
        title='服务 / 应用路由'
        description='服务和 Ingress 是独立 Kubernetes 资源，本页展示当前工作负载可被引用的端口。'
      >
        <div className='rounded-xl border border-[#e5ebf3] bg-[#f8fafd] p-4 text-sm leading-6 text-[#536987]'>
          <Info className='me-1 inline size-4 text-[#31598e]' />
          服务 selector 通常匹配 Pod 模板标签；Ingress 再通过 Service
          暴露应用。需要修改 Service 或 Ingress
          时，请从左侧“服务”子菜单进入对应资源。
        </div>
        {obj.kind === 'StatefulSet' && (
          <Field label='稳定服务名称' hint='该字段来自 spec.serviceName'>
            <ReadonlyValue>
              {stringValue(record(obj.spec).serviceName) || '-'}
            </ReadonlyValue>
          </Field>
        )}
        <div className='overflow-x-auto rounded-xl border border-[#e1e8f2]'>
          <table className='w-full min-w-120 text-sm'>
            <thead>
              <tr className='bg-[#f4f7fb] text-left text-xs text-[#536987]'>
                <th className='px-3 py-2'>容器</th>
                <th className='px-3 py-2'>端口名称</th>
                <th className='px-3 py-2'>端口</th>
                <th className='px-3 py-2'>协议</th>
              </tr>
            </thead>
            <tbody>
              {ports.map((port, index) => (
                <tr key={index} className='border-t border-[#eef1f6]'>
                  <td className='px-3 py-2 font-mono'>{port.container}</td>
                  <td className='px-3 py-2 font-mono'>{port.name || '-'}</td>
                  <td className='px-3 py-2 font-mono'>{port.port}</td>
                  <td className='px-3 py-2'>{port.protocol}</td>
                </tr>
              ))}
              {ports.length === 0 && (
                <tr>
                  <td
                    colSpan={4}
                    className='px-3 py-6 text-center text-sm text-[#64748b]'
                  >
                    暂无容器端口，无法建立服务映射
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </SectionCard>
    </div>
  )
}

export function WorkloadEditorPage({
  search,
  onBack,
}: {
  search: Search
  onBack: () => void
}) {
  const cluster = search.cluster
  const group = search.g
  const version = search.v
  const resource = search.r
  const name = search.name
  const params: K8sObjectParams = {
    cluster_id: cluster ?? 0,
    group,
    version: version ?? 'v1',
    resource: resource ?? '',
    namespace: search.ns,
  }
  const ready =
    cluster != null &&
    version != null &&
    resource != null &&
    name != null &&
    name !== '' &&
    WORKLOAD_RESOURCES.has(resource)
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: ['k8s', 'resource', params, name],
    queryFn: () => k8sApi.resource(name as string, params),
    enabled: ready,
  })
  const def = useResourceDef(params)
  const { caps } = useResourceCaps(params)
  const [draft, setDraft] = useState<K8sObject | null>(null)
  const [activeTab, setActiveTab] = useState('basic')
  const [error, setError] = useState('')
  const [pendingManifest, setPendingManifest] = useState('')
  const [confirmOpen, setConfirmOpen] = useState(false)
  const obj = query.data?.data

  const updateDraft: UpdateDraft = (fn) =>
    setDraft((current) => {
      if (current == null) {
        if (obj == null) return null
        const next = clone(obj)
        fn(next)
        return next
      }
      const next = clone(current)
      fn(next)
      return next
    })
  const editable = def?.editable === true && caps?.update === true
  const dirty =
    obj != null &&
    draft != null &&
    JSON.stringify(obj) !== JSON.stringify(draft)
  const manifest = useMemo(() => {
    if (draft == null) return ''
    const next = clone(draft)
    delete next.status
    if (next.metadata != null) {
      delete next.metadata.managedFields
      delete next.metadata.creationTimestamp
      delete next.metadata.uid
    }
    return YAML.stringify(next, { indent: 2 })
  }, [draft])

  const dryRun = useMutation({
    mutationFn: async () => {
      if (!manifest) throw new Error('编辑内容为空')
      await k8sApi.update(name as string, params, manifest, true)
      return manifest
    },
    onSuccess: (nextManifest) => {
      setPendingManifest(nextManifest)
      setError('')
      setConfirmOpen(true)
    },
    onError: (err) =>
      setError(
        `${errDetail(err)}${isConflict(err) ? '（资源已被修改，请取消后重新加载）' : ''}`
      ),
  })
  const apply = useMutation({
    mutationFn: (nextManifest: string) =>
      k8sApi.update(name as string, params, nextManifest),
    onSuccess: () => {
      toast.success('工作负载已保存')
      setConfirmOpen(false)
      void queryClient.invalidateQueries({ queryKey: ['k8s'] })
      onBack()
    },
    onError: (err) =>
      setError(
        `${errDetail(err)}${isConflict(err) ? '（资源已被修改，请取消后重新加载）' : ''}`
      ),
  })

  return (
    <>
      <Header fixed>
        <div className='me-auto flex min-w-0 items-center gap-2 text-sm'>
          <Button
            variant='ghost'
            size='sm'
            className='shrink-0 text-[#24549a]'
            onClick={onBack}
          >
            <ArrowLeft className='size-4' /> 返回详情
          </Button>
          <span className='text-[#b0bfd4]'>/</span>
          <span className='truncate font-semibold text-[#214b8d]'>
            {name ?? '编辑工作负载'}
          </span>
        </div>
        <ThemeSwitch />
      </Header>
      <Main fluid className='p-3 sm:p-4 lg:p-6'>
        {!ready ? (
          <p className='py-8 text-center text-muted-foreground'>
            缺少必要参数，无法编辑工作负载
          </p>
        ) : query.isError ? (
          <p className='py-8 text-center text-sm text-destructive'>
            {errDetail(query.error)}
          </p>
        ) : query.isPending || obj == null ? (
          <p className='py-8 text-center text-sm text-muted-foreground'>
            加载工作负载…
          </p>
        ) : (
          <div className='mx-auto max-w-6xl space-y-4'>
            <div className='flex flex-col gap-4 rounded-xl border border-[#dbe4f0] bg-gradient-to-r from-[#f4f8fd] to-white px-6 py-5 sm:flex-row sm:items-center sm:justify-between'>
              <div className='min-w-0'>
                <p className='pl-3 text-xs font-semibold text-[#7d8da6]'>
                  工作负载配置
                </p>
                <div className='mt-1 flex min-w-0 items-center gap-2'>
                  <span className='h-5 w-1 shrink-0 rounded-full bg-[#234883]' />
                  <h1 className='truncate text-2xl font-bold text-[#1c355e]'>
                    {name}
                  </h1>
                  <Badge
                    variant='outline'
                    className='shrink-0 border-[#dbe4f0] bg-white font-mono font-normal text-[#536987]'
                  >
                    {obj?.kind}
                  </Badge>
                </div>
                <p className='mt-1 flex min-w-0 items-baseline gap-1.5 pl-3 text-xs text-[#7d8da6]'>
                  <span>名称空间</span>
                  <span className='min-w-0 truncate font-mono text-sm font-semibold text-[#31598e]'>
                    {search.ns ?? '-'}
                  </span>
                </p>
              </div>
              <div className='flex shrink-0 flex-wrap items-center gap-2 sm:justify-end'>
                {dirty && editable && (
                  <span className='inline-flex items-center gap-1.5 text-xs text-[#94621c]'>
                    <Info className='size-3.5' />
                    有未保存的更改
                  </span>
                )}
                <Button
                  variant='outline'
                  className='h-11 rounded-lg border-[#c9d4e6] px-4 sm:h-9'
                  onClick={onBack}
                >
                  取消
                </Button>
                <Button
                  className='h-11 rounded-lg bg-[#234883] px-4 hover:bg-[#193d72] sm:h-9'
                  disabled={!editable || !dirty || dryRun.isPending}
                  onClick={() => {
                    setError('')
                    dryRun.mutate()
                  }}
                >
                  <Save className='size-4' />
                  {dryRun.isPending ? '校验中…' : '保存'}
                </Button>
              </div>
            </div>
            {!editable && (
              <div className='rounded-xl border border-[#f0d9b1] bg-[#fff9ed] px-4 py-3 text-sm text-[#94621c]'>
                <Info className='me-2 inline size-4' />
                当前账号没有该工作负载的更新权限，页面为只读模式。
              </div>
            )}
            {error && (
              <div className='rounded-xl border border-[#f0c8ce] bg-[#fff4f5] px-4 py-3 text-sm text-[#a52f3d]'>
                {error}
              </div>
            )}
            <Tabs
              value={activeTab}
              onValueChange={setActiveTab}
              className='gap-3'
            >
              <TabsList className='flex h-auto w-full justify-start overflow-x-auto rounded-t-xl border-b border-[#dbe4f0] bg-white px-4 pt-1 pb-0'>
                <TabsTrigger value='basic' className={EDITOR_TAB_CLASS}>
                  基本信息
                </TabsTrigger>
                <TabsTrigger value='containers' className={EDITOR_TAB_CLASS}>
                  容器信息
                </TabsTrigger>
                <TabsTrigger value='storage' className={EDITOR_TAB_CLASS}>
                  存储挂载
                </TabsTrigger>
                <TabsTrigger value='advanced' className={EDITOR_TAB_CLASS}>
                  高级设置
                </TabsTrigger>
                <TabsTrigger value='routes' className={EDITOR_TAB_CLASS}>
                  服务 / 应用路由
                </TabsTrigger>
              </TabsList>
              <TabsContent value='basic'>
                <BasicTab
                  obj={draft ?? obj}
                  disabled={!editable}
                  updateDraft={updateDraft}
                />
              </TabsContent>
              <TabsContent value='containers'>
                <ContainersTab
                  obj={draft ?? obj}
                  params={params}
                  disabled={!editable}
                  updateDraft={updateDraft}
                />
              </TabsContent>
              <TabsContent value='storage'>
                <StorageTab
                  obj={draft ?? obj}
                  params={params}
                  disabled={!editable}
                  updateDraft={updateDraft}
                />
              </TabsContent>
              <TabsContent value='advanced'>
                <AdvancedTab
                  obj={draft ?? obj}
                  params={params}
                  disabled={!editable}
                  updateDraft={updateDraft}
                />
              </TabsContent>
              <TabsContent value='routes'>
                <ServiceRouteTab obj={draft ?? obj} />
              </TabsContent>
            </Tabs>
          </div>
        )}
      </Main>
      <Dialog
        open={confirmOpen}
        onOpenChange={(open) => !apply.isPending && setConfirmOpen(open)}
      >
        <DialogContent className='sm:max-w-lg'>
          <DialogHeader>
            <DialogTitle className='flex items-center gap-2'>
              <CheckCircle2 className='size-5 text-[#0fbe8f]' />
              校验通过，确认保存？
            </DialogTitle>
            <DialogDescription>
              服务器 Dry Run 已通过。确认后将正式更新 {name}
              ，并触发工作负载重新调谐。
            </DialogDescription>
          </DialogHeader>
          <div className='max-h-48 overflow-auto rounded-lg border border-[#e1e8f2] bg-[#f8fafd] p-3 font-mono text-xs text-[#536987]'>
            {pendingManifest}
          </div>
          <DialogFooter>
            <Button
              variant='outline'
              disabled={apply.isPending}
              onClick={() => setConfirmOpen(false)}
            >
              返回修改
            </Button>
            <Button
              className='bg-[#234883] hover:bg-[#193d72]'
              disabled={apply.isPending}
              onClick={() => apply.mutate(pendingManifest)}
            >
              {apply.isPending ? '保存中…' : '确认保存'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
