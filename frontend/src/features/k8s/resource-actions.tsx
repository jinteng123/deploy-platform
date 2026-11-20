import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { CircleAlert, Package, Scaling, RotateCw, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { k8sApi, type K8sObject, type K8sObjectParams } from '@/api/client'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { errDetail, isConflict } from './errors'
import { useResourceCaps } from './use-capabilities'
import { useResourceDef } from './use-resource-def'

type Action = 'scale' | 'image' | 'restart' | 'delete'

// ActionError carries the conflict flag alongside the message so a 409
// can hint the user to refresh instead of looking like a plain failure.
// Exported for the other delete flows (list rows, data detail dialog).
export type ActionError = { text: string; conflict: boolean } | null

const toError = (err: unknown): ActionError =>
  err == null ? null : { text: errDetail(err), conflict: isConflict(err) }

function ErrorText({ error }: { error: ActionError }) {
  if (!error) return null
  return (
    <p className='flex items-start gap-1.5 rounded-lg bg-[#fdf0f1] px-3 py-2 text-xs leading-5 text-[#a52f3d]'>
      <CircleAlert className='mt-0.5 size-3.5 shrink-0' />
      <span>
        {error.text}
        {error.conflict && '（对象已被他人修改，请关闭后刷新详情重试）'}
      </span>
    </p>
  )
}

// IconChip gives each dialog a small visual anchor next to its title —
// blue for routine operations, red for destructive ones.
function IconChip({
  destructive,
  children,
}: {
  destructive?: boolean
  children: React.ReactNode
}) {
  return (
    <span
      className={
        destructive
          ? 'flex size-9 shrink-0 items-center justify-center rounded-lg bg-[#fdf0f1] text-[#c2404e]'
          : 'flex size-9 shrink-0 items-center justify-center rounded-lg bg-[#eef3fa] text-[#31598e]'
      }
    >
      {children}
    </span>
  )
}

// ContainerImage feeds the image dialog table: one row per container,
// init containers first (they run first, and Kuboard lists them first).
type ContainerImage = { name: string; image: string; init: boolean }

// containerImages lists spec.template.spec initContainers + containers —
// exactly the names the backend accepts for image updates.
function containerImages(obj: K8sObject | undefined): ContainerImage[] {
  const spec = obj?.spec as
    | { template?: { spec?: Record<string, unknown> } }
    | undefined
  const tpl = spec?.template?.spec ?? {}
  const out: ContainerImage[] = []
  for (const [field, init] of [
    ['initContainers', true],
    ['containers', false],
  ] as const) {
    const list = tpl[field]
    if (Array.isArray(list)) {
      for (const c of list) {
        const name = (c as { name?: unknown })?.name
        const image = (c as { image?: unknown })?.image
        if (typeof name === 'string' && name !== '') {
          out.push({
            name,
            image: typeof image === 'string' ? image : '',
            init,
          })
        }
      }
    }
  }
  return out
}

// splitImageRef separates repo from tag; a ':' left of the last '/' is a
// registry port, not a tag delimiter. Digest refs keep the full string as
// their "current value" — the input then edits the whole reference.
function splitImageRef(image: string): {
  repo: string
  value: string
  digest: boolean
} {
  const at = image.lastIndexOf('@')
  if (at !== -1) {
    return { repo: image.slice(0, at), value: image, digest: true }
  }
  const colon = image.lastIndexOf(':')
  if (colon > image.lastIndexOf('/')) {
    return {
      repo: image.slice(0, colon),
      value: image.slice(colon + 1),
      digest: false,
    }
  }
  return { repo: image, value: 'latest', digest: false }
}

// ResourceActions renders the capability-gated write controls on the
// resource detail page: scale / image / rollout restart / delete. Every
// mutation pins metadata.resourceVersion so a stale read surfaces 409
// instead of silently overwriting a newer object.
export function ResourceActions({
  params,
  name,
  obj,
  onDeleted,
  onEdit,
  onEditData,
  presentation = 'card',
}: {
  params: K8sObjectParams
  name: string
  obj: K8sObject
  onDeleted: () => void
  onEdit?: () => void
  // onEditData opens the structured key/value editor (configmaps,
  // secrets) — gated by data_editable + update, independent of the
  // whole-manifest editable flag.
  onEditData?: () => void
  presentation?: 'card' | 'toolbar'
}) {
  const queryClient = useQueryClient()
  const [action, setAction] = useState<Action | null>(null)
  const [error, setError] = useState<ActionError>(null)

  const def = useResourceDef(params)
  const { caps } = useResourceCaps(params)
  const rv = obj.metadata?.resourceVersion as string | undefined

  const open = (a: Action) => {
    setError(null)
    setAction(a)
  }
  const close = () => setAction(null)
  const done = () => {
    void queryClient.invalidateQueries({ queryKey: ['k8s'] })
    setAction(null)
    setError(null)
  }

  const scale = useMutation({
    mutationFn: (replicas: number) =>
      k8sApi.scale(name, params, { replicas, resource_version: rv }),
    onSuccess: () => {
      toast.success('副本数已更新')
      done()
    },
    onError: (e) => setError(toError(e)),
  })
  const image = useMutation({
    // Multi-row updates run sequentially; each response carries the new
    // resourceVersion so the next patch still passes the conflict check.
    mutationFn: async (updates: { container: string; image: string }[]) => {
      let curRv = rv
      for (const u of updates) {
        const res = await k8sApi.setImage(name, params, {
          ...u,
          resource_version: curRv,
        })
        curRv =
          (res.data?.metadata?.resourceVersion as string | undefined) ?? curRv
      }
    },
    onSuccess: () => {
      toast.success('镜像已更新')
      done()
    },
    onError: (e) => setError(toError(e)),
  })
  const restart = useMutation({
    mutationFn: () => k8sApi.restart(name, params, { resource_version: rv }),
    onSuccess: () => {
      toast.success('滚动重启已触发')
      done()
    },
    onError: (e) => setError(toError(e)),
  })
  const remove = useMutation({
    mutationFn: () => k8sApi.remove(name, params, rv),
    onSuccess: () => {
      toast.success(`${params.resource} ${name} 已删除`)
      done()
      onDeleted()
    },
    onError: (e) => setError(toError(e)),
  })

  // Both gates must pass: the allowlist says the resource type supports the
  // verb, and the RBAC verdict says THIS user may do it. Missing caps → hide.
  const canWrite = caps?.update === true
  const canDelete = caps?.delete === true
  const canEdit = def?.editable === true && canWrite
  const canEditData = def?.data_editable === true && canWrite
  const anyWrite =
    def != null &&
    canWrite &&
    (def.scalable || def.imageable || def.restartable || canEdit || canEditData)
  const anyDelete = def != null && canDelete && def.deletable
  if (def == null || (!anyWrite && !anyDelete)) return null

  const containers = containerImages(obj)

  return (
    <div
      className={
        presentation === 'toolbar'
          ? 'contents'
          : 'flex flex-wrap items-center justify-between gap-4 rounded-2xl border border-[#e0e8f4] bg-white p-4 shadow-[0_8px_30px_rgba(37,76,132,0.05)]'
      }
    >
      {presentation === 'card' && (
        <div>
          <p className='text-sm font-semibold text-[#1c355e]'>资源操作</p>
          <p className='mt-1 text-xs text-muted-foreground'>
            所有写操作均受当前账号权限与资源版本保护。
          </p>
        </div>
      )}
      <div className='flex flex-wrap items-center gap-2'>
        {onEdit && canEdit && (
          <Button
            variant={presentation === 'toolbar' ? 'default' : 'outline'}
            size='sm'
            className={
              presentation === 'toolbar'
                ? 'h-8 rounded-lg bg-[#234883] px-3 text-xs text-white hover:bg-[#193d72]'
                : 'rounded-lg border-[#d6e1f0]'
            }
            onClick={onEdit}
          >
            编辑
          </Button>
        )}
        {onEditData && canEditData && (
          <Button
            variant='outline'
            size='sm'
            className={
              presentation === 'toolbar'
                ? 'h-8 rounded-lg border-[#d6e1f0] bg-white px-3 text-xs text-[#31598e] hover:bg-[#f4f7fb] hover:text-[#234883]'
                : 'rounded-lg border-[#d6e1f0]'
            }
            onClick={onEditData}
          >
            编辑数据
          </Button>
        )}
        {def.scalable && canWrite && (
          <Button
            variant='outline'
            size='sm'
            className={
              presentation === 'toolbar'
                ? 'h-8 rounded-lg border-[#d6e1f0] bg-white px-3 text-xs text-[#31598e] hover:bg-[#f4f7fb] hover:text-[#234883]'
                : 'rounded-lg border-[#d6e1f0]'
            }
            onClick={() => open('scale')}
          >
            {presentation === 'toolbar' ? '伸缩' : '调整副本'}
          </Button>
        )}
        {def.imageable && canWrite && (
          <Button
            variant='outline'
            size='sm'
            className={
              presentation === 'toolbar'
                ? 'h-8 rounded-lg border-[#d6e1f0] bg-white px-3 text-xs text-[#31598e] hover:bg-[#f4f7fb] hover:text-[#234883]'
                : 'rounded-lg border-[#d6e1f0]'
            }
            onClick={() => open('image')}
          >
            {presentation === 'toolbar' ? '调整镜像版本' : '更换镜像'}
          </Button>
        )}
        {def.restartable && canWrite && (
          <Button
            variant='outline'
            size='sm'
            className={
              presentation === 'toolbar'
                ? 'h-8 rounded-lg border-[#d6e1f0] bg-white px-3 text-xs text-[#31598e] hover:bg-[#f4f7fb] hover:text-[#234883]'
                : 'rounded-lg border-[#d6e1f0]'
            }
            onClick={() => open('restart')}
          >
            {presentation === 'toolbar' ? '重启' : '滚动重启'}
          </Button>
        )}
        {def.deletable && canDelete && (
          <Button
            variant={presentation === 'toolbar' ? 'ghost' : 'destructive'}
            size='sm'
            className={
              presentation === 'toolbar'
                ? 'h-8 rounded-lg px-3 text-xs text-[#c2404e] hover:bg-[#fdf0f1] hover:text-[#a52f3d]'
                : 'rounded-lg'
            }
            onClick={() => open('delete')}
          >
            删除
          </Button>
        )}
      </div>

      <ScaleDialog
        open={action === 'scale'}
        onOpenChange={(o) => !o && close()}
        current={(obj.spec?.replicas as number | undefined) ?? 0}
        pending={scale.isPending}
        error={error}
        onConfirm={(n) => scale.mutate(n)}
      />
      <ImageDialog
        open={action === 'image'}
        onOpenChange={(o) => !o && close()}
        containers={containers}
        pending={image.isPending}
        error={error}
        onConfirm={(updates) => image.mutate(updates)}
      />
      <ConfirmDialog
        open={action === 'restart'}
        onOpenChange={(o) => !o && close()}
        title='滚动重启'
        icon={
          <IconChip>
            <RotateCw className='size-4' />
          </IconChip>
        }
        desc={`对 ${params.resource} "${name}" 触发滚动重启（等价于 kubectl rollout restart），工作负载将逐个重建 Pod。`}
        confirmText='重启'
        cancelBtnText='取消'
        isLoading={restart.isPending}
        handleConfirm={() => restart.mutate()}
      >
        <ErrorText error={error} />
      </ConfirmDialog>
      <DeleteDialog
        open={action === 'delete'}
        onOpenChange={(o) => !o && close()}
        resource={params.resource}
        name={name}
        pending={remove.isPending}
        error={error}
        onConfirm={() => remove.mutate()}
      />
    </div>
  )
}

function ScaleDialog({
  open,
  onOpenChange,
  current,
  pending,
  error,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  current: number
  pending: boolean
  error: ActionError
  onConfirm: (replicas: number) => void
}) {
  const [value, setValue] = useState(String(current))
  // Prefill with the live replica count each time the dialog opens —
  // render-phase state adjustment (prev-props compare), not an effect.
  const [prevOpen, setPrevOpen] = useState(open)
  if (open !== prevOpen) {
    setPrevOpen(open)
    if (open) setValue(String(current))
  }
  const n = Number(value)
  const valid = Number.isInteger(n) && n >= 0 && n <= 50

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-sm'>
        <DialogHeader className='flex-row items-start gap-3 space-y-0 text-start'>
          <IconChip>
            <Scaling className='size-4' />
          </IconChip>
          <div className='space-y-1'>
            <DialogTitle>调整副本数</DialogTitle>
            <DialogDescription>
              当前副本数 {current}，取值范围 0–50。
            </DialogDescription>
          </div>
        </DialogHeader>
        <div className='space-y-2'>
          <Label htmlFor='scale-replicas' className='text-xs text-[#5a6b85]'>
            目标副本数
          </Label>
          <Input
            id='scale-replicas'
            type='number'
            min={0}
            max={50}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            className='tabular-nums'
            autoFocus
          />
        </div>
        <ErrorText error={error} />
        <DialogFooter>
          <DialogClose asChild>
            <Button variant='outline' disabled={pending}>
              取消
            </Button>
          </DialogClose>
          <Button
            className='bg-[#234883] hover:bg-[#193d72]'
            disabled={!valid || pending}
            onClick={() => onConfirm(n)}
          >
            {pending ? '提交中…' : '确定'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function ImageDialog({
  open,
  onOpenChange,
  containers,
  pending,
  error,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  containers: ContainerImage[]
  pending: boolean
  error: ActionError
  onConfirm: (updates: { container: string; image: string }[]) => void
}) {
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  // Prefill each row's input with the live tag on every open transition —
  // render-phase state adjustment, same pattern as the dialogs above.
  const [prevOpen, setPrevOpen] = useState(open)
  if (open !== prevOpen) {
    setPrevOpen(open)
    if (open) {
      const next: Record<string, string> = {}
      for (const c of containers) next[c.name] = splitImageRef(c.image).value
      setDrafts(next)
    }
  }

  // A row submits only when its draft differs from the running tag and is
  // non-empty; empty drafts block the whole confirm so nothing is half-sent.
  const updates = containers
    .map((c) => {
      const imageRef = splitImageRef(c.image)
      const draft = drafts[c.name]?.trim()
      if (draft == null || draft === imageRef.value) return null
      return {
        container: c.name,
        image: imageRef.digest ? draft : `${imageRef.repo}:${draft}`,
        empty: draft === '',
      }
    })
    .filter(
      (u): u is { container: string; image: string; empty: boolean } =>
        u != null
    )
  const invalid = updates.some((u) => u.empty)
  const ready = updates.filter((u) => !u.empty)

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-4xl'>
        <DialogHeader className='flex-row items-start gap-3 space-y-0 text-start'>
          <IconChip>
            <Package className='size-4' />
          </IconChip>
          <div className='space-y-1'>
            <DialogTitle>调整镜像版本</DialogTitle>
            <DialogDescription>
              逐容器更新镜像版本，未修改的行将被跳过。
            </DialogDescription>
          </div>
        </DialogHeader>
        <div className='overflow-hidden rounded-lg border border-[#e1e6ef]'>
          <table className='w-full text-xs'>
            <thead>
              <tr className='bg-[#f4f6fa] text-left text-[#5a6b85]'>
                <th className='w-24 px-3 py-2 font-medium'>容器类型</th>
                <th className='px-3 py-2 font-medium'>名称</th>
                <th className='px-3 py-2 font-medium'>镜像</th>
                <th className='w-28 px-3 py-2 font-medium'>当前版本</th>
                <th className='w-40 px-3 py-2 font-medium'>新版本</th>
              </tr>
            </thead>
            <tbody>
              {containers.map((c) => {
                const imageRef = splitImageRef(c.image)
                const draft = drafts[c.name] ?? imageRef.value
                const dirty = draft.trim() !== imageRef.value
                const bad = dirty && draft.trim() === ''
                return (
                  <tr key={c.name} className='border-t border-[#eef1f6]'>
                    <td className='px-3 py-2.5 text-[#7e8796]'>
                      {c.init ? '初始化容器' : '工作容器'}
                    </td>
                    <td className='px-3 py-2.5 font-medium text-[#2b3e5c]'>
                      {c.name}
                    </td>
                    <td
                      className='max-w-80 truncate px-3 py-2.5 font-mono text-[#5a6b85]'
                      title={c.image}
                    >
                      {imageRef.repo}
                    </td>
                    <td className='px-3 py-2.5 font-mono text-[#2b3e5c]'>
                      {imageRef.digest ? '（digest）' : imageRef.value}
                    </td>
                    <td className='px-3 py-1.5'>
                      <Input
                        aria-label={`新版本 ${c.name}`}
                        value={draft}
                        onChange={(e) =>
                          setDrafts((d) => ({ ...d, [c.name]: e.target.value }))
                        }
                        className={
                          bad
                            ? 'h-8 border-[#c2404e] font-mono text-xs focus-visible:ring-[#c2404e]/30'
                            : 'h-8 font-mono text-xs'
                        }
                      />
                    </td>
                  </tr>
                )
              })}
              {containers.length === 0 && (
                <tr className='border-t border-[#eef1f6]'>
                  <td
                    colSpan={5}
                    className='px-3 py-6 text-center text-[#7e8796]'
                  >
                    该资源没有可更新的容器
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
        <ErrorText error={error} />
        <DialogFooter>
          <DialogClose asChild>
            <Button variant='outline' disabled={pending}>
              取消
            </Button>
          </DialogClose>
          <Button
            className='bg-[#234883] hover:bg-[#193d72]'
            disabled={ready.length === 0 || invalid || pending}
            onClick={() =>
              onConfirm(
                ready.map(({ container, image }) => ({ container, image }))
              )
            }
          >
            {pending
              ? '提交中…'
              : ready.length > 0
                ? `确定（更新 ${ready.length} 个）`
                : '确定'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// DeleteDialog demands the exact resource name typed in before the
// destructive button enables — the strongest confirmation this console
// offers, applied uniformly to every deletable kind. Exported so the
// list page and the data detail dialog share the same confirmation UX.
export function DeleteDialog({
  open,
  onOpenChange,
  resource,
  name,
  pending,
  error,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  resource: string
  name: string
  pending: boolean
  error: ActionError
  onConfirm: () => void
}) {
  const [typed, setTyped] = useState('')
  // Clear the confirmation input on every open transition — render-phase
  // adjustment, same pattern as the dialogs above.
  const [prevOpen, setPrevOpen] = useState(open)
  if (open !== prevOpen) {
    setPrevOpen(open)
    if (open) setTyped('')
  }

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={`删除 ${resource}`}
      icon={
        <IconChip destructive>
          <Trash2 className='size-4' />
        </IconChip>
      }
      desc={
        <>
          此操作不可撤销。请输入资源名 <b>{name}</b> 确认删除：
        </>
      }
      destructive
      confirmText='删除'
      cancelBtnText='取消'
      disabled={typed !== name}
      isLoading={pending}
      handleConfirm={onConfirm}
    >
      <div className='space-y-2'>
        <Input
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          placeholder={name}
          className='font-mono text-xs'
          autoFocus
        />
        <ErrorText error={error} />
      </div>
    </ConfirmDialog>
  )
}
