import { lazy, Suspense, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Pencil, Trash2, X } from 'lucide-react'
import { toast } from 'sonner'
import * as YAML from 'yaml'
import { k8sApi, type K8sObject, type K8sObjectParams } from '@/api/client'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Switch } from '@/components/ui/switch'
import { age } from './age'
import { DataFields, mapRows, rowsError } from './data-fields'
import { errDetail, isConflict } from './errors'
import { kvRowsToData } from './kv-rows-editor'
import { DeleteDialog, type ActionError } from './resource-actions'
import { decodeSecretData, SecretDataFields } from './secret-data-fields'
import { useResourceCaps } from './use-capabilities'
import { useResourceDef } from './use-resource-def'

const YamlCard = lazy(() =>
  import('./yaml-editor').then((m) => ({ default: m.YamlCard }))
)

export type DataResourceDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  params: K8sObjectParams
  name: string
  obj: K8sObject
  initialMode?: 'view' | 'edit'
  onSaved?: () => void
  onDeleted?: () => void
  onEdit?: () => void
}

export function DataResourceDialog(props: DataResourceDialogProps) {
  // Unmount all draft state on close; a reopened dialog starts with fresh data.
  return props.open ? (
    <DataResourceSession
      key={`${props.params.cluster_id}/${props.params.resource}/${props.params.namespace}/${props.name}`}
      {...props}
    />
  ) : null
}

function DataResourceSession(props: DataResourceDialogProps) {
  const { params, name } = props
  const secret = params.resource === 'secrets'
  const query = useQuery({
    queryKey: ['k8s', 'data-dialog', params, name, secret],
    queryFn: async () => {
      const { data: obj } = await k8sApi.resource(name, params, secret)
      const values = secret
        ? decodeSecretData(obj.data as Record<string, string> | undefined)
        : ((obj.data ?? {}) as Record<string, string>)
      return { obj, values }
    },
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  // Keep the identity visible while loading, but never enable editing a masked
  // or stale list object. Secret material leaves the query cache on unmount.
  return (
    <DataResourceContent
      key={query.data ? 'loaded' : 'loading'}
      {...props}
      obj={query.data?.obj ?? props.obj}
      values={query.data?.values ?? {}}
      loading={query.isPending}
      loadError={query.error ? errDetail(query.error) : ''}
      reload={async () => {
        const result = await query.refetch()
        if (result.error) throw result.error
        return result.data!
      }}
    />
  )
}

function DataResourceContent({
  open,
  onOpenChange,
  params,
  name,
  obj,
  values,
  initialMode = 'view',
  onSaved,
  onDeleted,
  onEdit,
  loading,
  loadError,
  reload,
}: DataResourceDialogProps & {
  values: Record<string, string>
  loading: boolean
  loadError: string
  reload: () => Promise<{ obj: K8sObject; values: Record<string, string> }>
}) {
  const client = useQueryClient()
  const secret = params.resource === 'secrets'
  const noun = secret ? '密钥' : '配置字典'
  const kind = secret ? 'Secret' : 'ConfigMap'
  const [base, setBase] = useState(obj)
  const [original, setOriginal] = useState(values)
  const [mode, setMode] = useState(initialMode)
  const [tab, setTab] = useState<'meta' | 'data'>('data')
  const [rows, setRows] = useState(() => mapRows(values))
  const [labels, setLabels] = useState(() => mapRows(obj.metadata?.labels))
  const [annotations, setAnnotations] = useState(() =>
    mapRows(obj.metadata?.annotations)
  )
  const [immutable, setImmutable] = useState(obj.immutable === true)
  const [rawData, setRawData] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState('')
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteError, setDeleteError] = useState<ActionError>(null)
  const def = useResourceDef(params)
  const { caps } = useResourceCaps(params)
  const secretType = String(base.type ?? 'Opaque')
  const supported =
    !secret ||
    ![
      'kubernetes.io/service-account-token',
      'bootstrap.kubernetes.io/token',
    ].includes(secretType)
  const canEdit =
    def?.data_editable === true && caps?.update === true && supported
  const canDelete =
    def?.deletable === true &&
    caps?.delete === true &&
    !(secret && name.startsWith('default-token'))
  const editing = mode === 'edit' && canEdit
  const persistedImmutable = base.immutable === true
  const locked = pending || persistedImmutable

  const restore = (nextObj: K8sObject, nextValues: Record<string, string>) => {
    setBase(nextObj)
    setOriginal(nextValues)
    setRows(mapRows(nextValues))
    setLabels(mapRows(nextObj.metadata?.labels))
    setAnnotations(mapRows(nextObj.metadata?.annotations))
    setImmutable(nextObj.immutable === true)
    setError('')
  }
  const enterEdit = async () => {
    setPending(true)
    setError('')
    try {
      const next = await reload()
      restore(next.obj, next.values)
      setMode('edit')
      onEdit?.()
    } catch (e) {
      setError(errDetail(e))
    } finally {
      setPending(false)
    }
  }
  const cancel = async () => {
    restore(base, original)
    setMode('view')
    setPending(true)
    try {
      const next = await reload()
      restore(next.obj, next.values)
    } catch (e) {
      setError(errDetail(e))
    } finally {
      setPending(false)
    }
  }
  const save = async () => {
    const invalid =
      rowsError(rows) || rowsError(labels, true) || rowsError(annotations, true)
    if (invalid) {
      setError(invalid)
      return
    }
    if (
      labels.some(
        ({ value }) =>
          value.length > 63 ||
          (value !== '' &&
            !/^[a-zA-Z0-9]([-._a-zA-Z0-9]*[a-zA-Z0-9])?$/.test(value))
      )
    ) {
      setError('标签值只能包含字母、数字、-、_、.，长度不能超过 63')
      return
    }
    setPending(true)
    setError('')
    try {
      const data = kvRowsToData(rows)
      const metadata = {
        ...base.metadata,
        labels: kvRowsToData(labels),
        annotations: kvRowsToData(annotations),
      }
      if (secret) {
        const set = Object.fromEntries(
          Object.entries(data).filter(
            ([key, value]) =>
              !Object.prototype.hasOwnProperty.call(original, key) ||
              original[key] !== value
          )
        )
        const deleteKeys = Object.keys(original).filter(
          (key) => !Object.prototype.hasOwnProperty.call(data, key)
        )
        const body = {
          resource_version: String(base.metadata?.resourceVersion ?? ''),
          ...(Object.keys(set).length ? { set } : {}),
          ...(deleteKeys.length ? { delete_keys: deleteKeys } : {}),
          ...(immutable !== persistedImmutable ? { immutable } : {}),
          metadata: {
            labels: metadata.labels,
            annotations: metadata.annotations,
          },
        }
        await k8sApi.updateData(name, params, body, true)
        await k8sApi.updateData(name, params, body)
      } else {
        const manifest = YAML.stringify({ ...base, metadata, data, immutable })
        await k8sApi.update(name, params, manifest, true)
        await k8sApi.update(name, params, manifest)
      }
      toast.success(`${noun} ${name} 已更新`)
      void client.invalidateQueries({ queryKey: ['k8s'] })
      onSaved?.()
      onOpenChange(false)
    } catch (e) {
      setError(
        `${errDetail(e)}${isConflict(e) ? '（对象已被他人修改，请取消后重新编辑）' : ''}`
      )
    } finally {
      setPending(false)
    }
  }
  const remove = async () => {
    setPending(true)
    setDeleteError(null)
    try {
      await k8sApi.remove(
        name,
        params,
        String(base.metadata?.resourceVersion ?? '')
      )
      toast.success(`${kind} ${name} 已删除`)
      void client.invalidateQueries({ queryKey: ['k8s'] })
      setDeleteOpen(false)
      onDeleted?.()
      onOpenChange(false)
    } catch (e) {
      setDeleteError({ text: errDetail(e), conflict: isConflict(e) })
    } finally {
      setPending(false)
    }
  }
  const specialized =
    secret &&
    ['kubernetes.io/dockerconfigjson', 'kubernetes.io/tls'].includes(secretType)
  return (
    <>
      <Dialog
        open={open}
        onOpenChange={(value) => {
          if (!pending) onOpenChange(value)
        }}
      >
        <DialogContent
          className='top-[5vh] flex max-h-[90vh] w-[92vw] max-w-[92vw] translate-y-0 flex-col gap-0 overflow-hidden rounded bg-white p-0 text-[#303133] shadow-[0_8px_40px_rgba(20,40,80,0.16)] sm:max-w-[92vw]'
          onInteractOutside={(e) => e.preventDefault()}
          onEscapeKeyDown={(e) => {
            if (pending) e.preventDefault()
          }}
          showCloseButton={!pending}
        >
          <DialogHeader className='border-b border-[#ebeef5] px-5 pt-4 pb-3'>
            <div className='flex flex-wrap items-center justify-between gap-2 pr-10'>
              <DialogTitle className='text-left text-base font-medium break-all'>
                {kind}: {params.namespace ?? base.metadata?.namespace} / {name}
              </DialogTitle>
              <span className='text-xs text-[#909399]'>
                创建时间 {age(base.metadata?.creationTimestamp)}
              </span>
            </div>
            <DialogDescription className='sr-only'>
              {editing ? `编辑${noun}` : `查看${noun}`}的元数据与数据
            </DialogDescription>
          </DialogHeader>
          <div className='min-h-0 flex-1 overflow-y-auto px-5 pb-2'>
            <div className='grid gap-x-8 gap-y-3 py-4 text-sm md:grid-cols-3'>
              <div className='flex gap-5'>
                <span className='shrink-0 text-[#909399]'>名称空间</span>
                <span className='font-mono break-all'>
                  {params.namespace ?? base.metadata?.namespace}
                </span>
              </div>
              <div className='flex gap-5'>
                <span className='shrink-0 text-[#909399]'>名称</span>
                <span className='font-mono break-all'>{name}</span>
              </div>
              <div className='flex flex-wrap items-center gap-3'>
                <span className='text-[#909399]'>
                  {secret ? '类型' : '不可变'}
                </span>
                {secret && (
                  <span className='font-mono text-xs'>{secretType}</span>
                )}
                {editing && !persistedImmutable && (
                  <Switch
                    aria-label='创建后不可修改（immutable）'
                    checked={immutable}
                    disabled={pending}
                    onCheckedChange={setImmutable}
                  />
                )}
                <span
                  className={cn(
                    'rounded px-2 py-0.5 text-xs text-white',
                    immutable ? 'bg-[#f44656]' : 'bg-[#0fbe8f]'
                  )}
                >
                  {immutable ? '创建后不可修改' : '创建后可修改'}
                </span>
              </div>
            </div>
            {loading || loadError ? (
              <div className='flex min-h-[50vh] flex-col items-center justify-center gap-3'>
                <p
                  role={loadError ? 'alert' : 'status'}
                  className={
                    loadError
                      ? 'text-sm text-[#f44656]'
                      : 'text-sm text-[#909399]'
                  }
                >
                  {loadError || '正在加载数据…'}
                </p>
                {loadError && (
                  <Button
                    variant='outline'
                    onClick={() => {
                      void reload()
                        .then((next) => restore(next.obj, next.values))
                        .catch(() => {})
                    }}
                  >
                    重新加载
                  </Button>
                )}
              </div>
            ) : (
              <>
                {persistedImmutable && editing && (
                  <p className='mb-3 border border-[#ffe7cd] bg-[#fff7ed] p-3 text-xs text-[#b46515]'>
                    该资源已标记为不可修改（immutable），数据无法更改；标签与注解仍可编辑。
                  </p>
                )}
                <div className='flex min-h-[50vh] bg-[#f4f5f7]'>
                  <div
                    role='tablist'
                    aria-label='资源详情'
                    aria-orientation='vertical'
                    className='flex w-24 shrink-0 flex-col gap-0.5 py-2 max-sm:w-16'
                  >
                    {(
                      [
                        ['meta', '元数据'],
                        ['data', '数据'],
                      ] as const
                    ).map(([id, label]) => (
                      <button
                        key={id}
                        type='button'
                        role='tab'
                        aria-selected={tab === id}
                        aria-controls={`resource-${id}`}
                        onClick={() => setTab(id)}
                        className={cn(
                          'border-r-[3px] px-3 py-3 text-sm max-sm:text-xs max-sm:whitespace-nowrap',
                          tab === id
                            ? 'border-[#234883] bg-white font-semibold text-[#234883]'
                            : 'border-transparent text-[#606266] hover:text-[#234883]'
                        )}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                  <div
                    role='tabpanel'
                    id={`resource-${tab}`}
                    className='min-w-0 flex-1'
                  >
                    {tab === 'meta' ? (
                      <div className='space-y-5 border border-[#ebeef5] bg-white p-5'>
                        <div className='space-y-2'>
                          <h3 className='text-xs font-medium text-[#606266]'>
                            标签
                          </h3>
                          <DataFields
                            metadata
                            fieldName='标签'
                            rows={labels}
                            onChange={setLabels}
                            editing={editing}
                            disabled={pending}
                            addLabel='添加标签'
                            empty='无标签'
                          />
                        </div>
                        <div className='space-y-2'>
                          <h3 className='text-xs font-medium text-[#606266]'>
                            注解
                          </h3>
                          <DataFields
                            metadata
                            fieldName='注解'
                            rows={annotations}
                            onChange={setAnnotations}
                            editing={editing}
                            disabled={pending}
                            addLabel='添加注解'
                            empty='无注解'
                          />
                        </div>
                      </div>
                    ) : (
                      <>
                        {specialized && (
                          <div className='flex items-center gap-2 border-b border-[#ebeef5] bg-[#e9edf3] px-5 py-3'>
                            <Button
                              variant={!rawData ? 'default' : 'outline'}
                              size='sm'
                              className='rounded'
                              onClick={() => setRawData(false)}
                            >
                              表单模式
                            </Button>
                            <Button
                              variant={rawData ? 'default' : 'outline'}
                              size='sm'
                              className='rounded'
                              onClick={() => setRawData(true)}
                            >
                              原始数据
                            </Button>
                          </div>
                        )}
                        {specialized && !rawData ? (
                          <SecretDataFields
                            type={secretType}
                            rows={rows}
                            onChange={setRows}
                            editing={editing}
                            disabled={locked}
                          />
                        ) : (
                          <DataFields
                            rows={rows}
                            onChange={setRows}
                            editing={editing}
                            disabled={locked}
                            empty={`该${noun}没有数据条目`}
                          />
                        )}
                      </>
                    )}
                  </div>
                </div>
              </>
            )}
          </div>
          <div className='border-t border-[#ebeef5] px-5 py-4'>
            {error && !loadError && (
              <p
                role='alert'
                className='mb-3 rounded bg-[#feedee] p-3 text-sm text-[#f44656]'
              >
                {error}
              </p>
            )}
            <div className='flex flex-wrap items-center gap-2'>
              {!loading && !loadError && !editing ? (
                <Suspense fallback={null}>
                  <YamlCard
                    presentation='dialog'
                    params={params}
                    name={name}
                    obj={base}
                    triggerLabel='预览 / 编辑 YAML'
                    dialogTitle={`${kind} ${name} YAML`}
                  />
                </Suspense>
              ) : (
                <span />
              )}
              <div className='ms-auto flex items-center gap-2'>
                {!editing && canDelete && (
                  <Button
                    variant='outline'
                    disabled={pending || loading || !!loadError}
                    onClick={() => setDeleteOpen(true)}
                    className='rounded border-[#fddadd] text-[#f44656] hover:bg-[#feedee] hover:text-[#f44656]'
                  >
                    <Trash2 className='size-4' />
                    删除
                  </Button>
                )}
                {!editing && canEdit && (
                  <Button
                    disabled={pending || loading || !!loadError}
                    onClick={enterEdit}
                    className='rounded bg-[#234883] hover:bg-[#193d72]'
                  >
                    <Pencil className='size-4' />
                    编辑
                  </Button>
                )}
                {editing && (
                  <Button
                    disabled={pending || loading || !!loadError}
                    onClick={save}
                    className='rounded bg-[#234883] hover:bg-[#193d72]'
                  >
                    <Check className='size-4' />
                    {pending ? '保存中…' : '保存'}
                  </Button>
                )}
                {editing ? (
                  <Button
                    variant='outline'
                    disabled={pending}
                    onClick={cancel}
                    className='rounded'
                  >
                    <X className='size-4' />
                    取消
                  </Button>
                ) : (
                  <Button
                    variant='outline'
                    disabled={pending}
                    onClick={() => onOpenChange(false)}
                    className='rounded'
                  >
                    <X className='size-4' />
                    关闭窗口
                  </Button>
                )}
              </div>
            </div>
          </div>
        </DialogContent>
      </Dialog>
      <DeleteDialog
        open={deleteOpen}
        onOpenChange={(v) => {
          if (!pending) setDeleteOpen(v)
        }}
        resource={kind}
        name={name}
        pending={pending}
        error={deleteError}
        onConfirm={() => {
          void remove()
        }}
      />
    </>
  )
}
