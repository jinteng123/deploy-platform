import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, KeyRound, Plus, TriangleAlert } from 'lucide-react'
import { toast } from 'sonner'
import { platformApi, type APIKey, type APIKeyCreated } from '@/api/client'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { ConfirmDialog } from '@/components/confirm-dialog'

const roleLabel: Record<APIKey['role'], string> = {
  viewer: '只读',
  operator: '运维',
}

const roleBadge: Record<APIKey['role'], string> = {
  viewer: 'bg-sky-50 text-sky-700 border-sky-200',
  operator: 'bg-amber-50 text-amber-700 border-amber-200',
}

const fmtTime = (v: string | null) => (v ? new Date(v).toLocaleString() : '-')

const fmtExpiry = (k: APIKey) => {
  if (!k.expires_at) return '永不过期'
  const t = new Date(k.expires_at)
  const expired = t.getTime() <= Date.now()
  return (
    <span className={expired ? 'text-red-600' : undefined}>
      {t.toLocaleString()}
      {expired ? '（已过期）' : ''}
    </span>
  )
}

const errMsg = (e: unknown) =>
  (e as { response?: { data?: { error?: string } } })?.response?.data?.error ??
  '操作失败'

// toRFC3339 keeps a datetime-local value meaningful to the backend.
const toRFC3339 = (local: string) =>
  local ? new Date(local).toISOString() : ''
const fromRFC3339 = (v: string | null) => {
  if (!v) return ''
  const d = new Date(v)
  d.setMinutes(d.getMinutes() - d.getTimezoneOffset())
  return d.toISOString().slice(0, 16)
}

type KeyForm = { name: string; role: APIKey['role']; expires_at: string }

export function ApiKeysPage() {
  const queryClient = useQueryClient()
  const { data: keys = [] } = useQuery({
    queryKey: ['api-keys'],
    queryFn: () => platformApi.apiKeys().then((r) => r.data),
  })

  const [createOpen, setCreateOpen] = useState(false)
  const [editing, setEditing] = useState<APIKey | null>(null)
  const [deleting, setDeleting] = useState<APIKey | null>(null)
  const [created, setCreated] = useState<APIKeyCreated | null>(null)
  const [form, setForm] = useState<KeyForm>({
    name: '',
    role: 'viewer',
    expires_at: '',
  })

  const openCreate = () => {
    setForm({ name: '', role: 'viewer', expires_at: '' })
    setCreateOpen(true)
  }
  const openEdit = (k: APIKey) => {
    setForm({
      name: k.name,
      role: k.role,
      expires_at: fromRFC3339(k.expires_at),
    })
    setEditing(k)
  }

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ['api-keys'] })

  const create = useMutation({
    mutationFn: () =>
      platformApi.createApiKey({
        name: form.name.trim(),
        role: form.role,
        expires_at: toRFC3339(form.expires_at),
      }),
    onSuccess: (res) => {
      setCreateOpen(false)
      setCreated(res.data)
      invalidate()
    },
    onError: (e) => toast.error(errMsg(e)),
  })

  const update = useMutation({
    mutationFn: (k: APIKey) =>
      platformApi.updateApiKey(k.id, {
        name: form.name.trim(),
        role: form.role,
        expires_at: toRFC3339(form.expires_at),
      }),
    onSuccess: () => {
      toast.success('密钥已更新')
      setEditing(null)
      invalidate()
    },
    onError: (e) => toast.error(errMsg(e)),
  })

  const toggle = useMutation({
    mutationFn: (k: APIKey) =>
      platformApi.updateApiKey(k.id, { enabled: !k.enabled }),
    onSuccess: (_, k) => {
      toast.success(k.enabled ? '密钥已禁用' : '密钥已启用')
      invalidate()
    },
    onError: (e) => toast.error(errMsg(e)),
  })

  const remove = useMutation({
    mutationFn: (k: APIKey) => platformApi.deleteApiKey(k.id),
    onSuccess: () => {
      toast.success('密钥已删除')
      setDeleting(null)
      invalidate()
    },
    onError: (e) => toast.error(errMsg(e)),
  })

  return (
    <div className='space-y-4'>
      <div className='flex items-start justify-between gap-4'>
        <div className='space-y-0.5'>
          <h2 className='text-lg font-semibold'>API 密钥</h2>
          <p className='text-sm text-muted-foreground'>
            供外部系统调用
            OpenAPI（/openapi/v1/*）使用；密钥明文只在创建时显示一次。
          </p>
        </div>
        <Button onClick={openCreate}>
          <Plus className='mr-1 size-4' />
          创建密钥
        </Button>
      </div>

      <div className='rounded-md border'>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>名称</TableHead>
              <TableHead>密钥</TableHead>
              <TableHead>角色</TableHead>
              <TableHead>状态</TableHead>
              <TableHead>过期时间</TableHead>
              <TableHead>最近使用</TableHead>
              <TableHead>创建时间</TableHead>
              <TableHead className='w-28 text-right'>操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {keys.length === 0 && (
              <TableRow>
                <TableCell
                  colSpan={8}
                  className='py-10 text-center text-sm text-muted-foreground'
                >
                  <KeyRound className='mx-auto mb-2 size-6 opacity-40' />
                  还没有 API 密钥，点击右上角创建。
                </TableCell>
              </TableRow>
            )}
            {keys.map((k) => (
              <TableRow key={k.id}>
                <TableCell className='font-medium'>{k.name}</TableCell>
                <TableCell className='font-mono text-xs text-muted-foreground'>
                  {k.prefix}…
                </TableCell>
                <TableCell>
                  <Badge variant='outline' className={roleBadge[k.role]}>
                    {roleLabel[k.role]}
                  </Badge>
                </TableCell>
                <TableCell>
                  <Switch
                    checked={k.enabled}
                    onCheckedChange={() => toggle.mutate(k)}
                    aria-label={`启用 ${k.name}`}
                  />
                </TableCell>
                <TableCell className='text-sm'>{fmtExpiry(k)}</TableCell>
                <TableCell className='text-sm text-muted-foreground'>
                  {fmtTime(k.last_used_at)}
                </TableCell>
                <TableCell className='text-sm text-muted-foreground'>
                  {fmtTime(k.created_at)}
                </TableCell>
                <TableCell className='text-right'>
                  <Button variant='ghost' size='sm' onClick={() => openEdit(k)}>
                    编辑
                  </Button>
                  <Button
                    variant='ghost'
                    size='sm'
                    className='text-red-600 hover:text-red-600'
                    onClick={() => setDeleting(k)}
                  >
                    删除
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>

      <KeyFormDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        title='创建 API 密钥'
        form={form}
        setForm={setForm}
        pending={create.isPending}
        onSubmit={() => create.mutate()}
        submitText='创建'
      />
      <KeyFormDialog
        open={editing != null}
        onOpenChange={(o) => !o && setEditing(null)}
        title='编辑 API 密钥'
        form={form}
        setForm={setForm}
        pending={update.isPending}
        onSubmit={() => editing && update.mutate(editing)}
        submitText='保存'
      />
      <KeyRevealDialog created={created} onClose={() => setCreated(null)} />
      <ConfirmDialog
        open={deleting != null}
        onOpenChange={(o) => !o && setDeleting(null)}
        title='删除 API 密钥'
        desc={
          <>
            确认删除密钥 <b>{deleting?.name}</b>？
            使用该密钥的调用方将立即无法访问，此操作不可撤销。
          </>
        }
        destructive
        confirmText='删除'
        isLoading={remove.isPending}
        handleConfirm={() => deleting && remove.mutate(deleting)}
      />
    </div>
  )
}

function KeyFormDialog(props: {
  open: boolean
  onOpenChange: (o: boolean) => void
  title: string
  form: KeyForm
  setForm: (f: KeyForm) => void
  pending: boolean
  onSubmit: () => void
  submitText: string
}) {
  const { open, onOpenChange, title, form, setForm, pending, onSubmit } = props
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className='sm:max-w-md'>
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>
            viewer 只能查询发布与日志；operator 可触发晋级与回滚。
          </DialogDescription>
        </DialogHeader>
        <form
          className='space-y-4'
          onSubmit={(e) => {
            e.preventDefault()
            onSubmit()
          }}
        >
          <div className='space-y-1.5'>
            <Label htmlFor='key-name'>名称</Label>
            <Input
              id='key-name'
              required
              maxLength={64}
              placeholder='例如 ci-bot'
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor='key-role'>角色</Label>
            <Select
              value={form.role}
              onValueChange={(v) =>
                setForm({ ...form, role: v as APIKey['role'] })
              }
            >
              <SelectTrigger id='key-role'>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value='viewer'>viewer（只读）</SelectItem>
                <SelectItem value='operator'>operator（可发布）</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div className='space-y-1.5'>
            <Label htmlFor='key-expires'>过期时间（留空=永不过期）</Label>
            <Input
              id='key-expires'
              type='datetime-local'
              value={form.expires_at}
              onChange={(e) => setForm({ ...form, expires_at: e.target.value })}
            />
          </div>
          <DialogFooter>
            <Button type='submit' disabled={pending || !form.name.trim()}>
              {props.submitText}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function KeyRevealDialog({
  created,
  onClose,
}: {
  created: APIKeyCreated | null
  onClose: () => void
}) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    if (!created) return
    await navigator.clipboard.writeText(created.key)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }
  return (
    <Dialog
      open={created != null}
      onOpenChange={(o) => {
        if (!o) {
          setCopied(false)
          onClose()
        }
      }}
    >
      <DialogContent className='sm:max-w-md'>
        <DialogHeader>
          <DialogTitle>密钥创建成功</DialogTitle>
          <DialogDescription className='sr-only'>
            密钥只会显示这一次，请在关闭前复制并保存。
          </DialogDescription>
        </DialogHeader>
        <div className='space-y-3'>
          <div className='flex items-start gap-2 rounded-md border border-amber-200 bg-amber-50 p-3 text-sm text-amber-800'>
            <TriangleAlert className='mt-0.5 size-4 shrink-0' />
            密钥只显示这一次，请立即复制并妥善保存；关闭后无法再查看。
          </div>
          <div className='flex items-center gap-2'>
            <code
              data-testid='created-key'
              className='flex-1 rounded-md border bg-muted px-3 py-2 font-mono text-xs break-all'
            >
              {created?.key}
            </code>
            <Button
              type='button'
              variant='outline'
              size='icon'
              aria-label='复制密钥'
              onClick={copy}
            >
              {copied ? (
                <Check className='size-4 text-green-600' />
              ) : (
                <Copy className='size-4' />
              )}
            </Button>
          </div>
          <DialogFooter>
            <Button onClick={onClose}>我已保存</Button>
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  )
}
