import { lazy, Suspense, useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { diffLines } from 'diff'
import {
  CheckCircle2,
  FileCode,
  FileDiff,
  Info,
  LoaderCircle,
} from 'lucide-react'
import { toast } from 'sonner'
import * as YAML from 'yaml'
import { k8sApi, type K8sObject, type K8sObjectParams } from '@/api/client'
import { cn } from '@/lib/utils'
import { useTheme } from '@/context/theme-provider'
import { Alert, AlertDescription } from '@/components/ui/alert'
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
import { errDetail, isConflict } from './errors'
import { useResourceCaps } from './use-capabilities'
import { useResourceDef } from './use-resource-def'

type Lang = 'yaml' | 'json'

// Monaco is only needed for edits. Keep the common read-only resource view
// lightweight so opening a workload does not download the editor bundle.
const MonacoEditor = lazy(async () => {
  const [, editor] = await Promise.all([
    import('@/features/k8s/monaco'),
    import('@monaco-editor/react'),
  ])
  return { default: editor.default }
})

// parseText validates editor content and returns the object it encodes.
// YAML accepts JSON natively, so both modes funnel through the YAML
// parser — multi-document input is rejected outright.
function parseText(text: string): { obj?: K8sObject; error?: string } {
  if (text.trim() === '') return { error: '内容不能为空' }
  try {
    const docs = YAML.parseAllDocuments(text)
    if (docs.length !== 1) return { error: '仅支持单个 YAML 文档' }
    const errs = docs[0].errors
    if (errs && errs.length > 0)
      return { error: `YAML 语法错误: ${errs[0].message}` }
    const v = docs[0].toJS()
    if (v == null || typeof v !== 'object' || Array.isArray(v)) {
      return { error: '内容必须是一个对象' }
    }
    return { obj: v as K8sObject }
  } catch (e) {
    return { error: `解析失败: ${e instanceof Error ? e.message : String(e)}` }
  }
}

const toYaml = (o: K8sObject) => YAML.stringify(o, { indent: 2 })
const toJson = (o: K8sObject) => JSON.stringify(o, null, 2)
const serialize = (o: K8sObject, lang: Lang) =>
  lang === 'json' ? toJson(o) : toYaml(o)

// checkIdentity enforces the immutable identity fields before a diff is
// even generated — apiVersion/kind/name/namespace pin the object to its
// allowlisted GVR and URL identity (the server enforces the same rule;
// this check is the fast-fail UX copy of it).
function checkIdentity(parsed: K8sObject, base: K8sObject): string {
  if (parsed.apiVersion !== base.apiVersion) return 'apiVersion 不可修改'
  if (parsed.kind !== base.kind) return 'kind 不可修改'
  if (parsed.metadata?.name !== base.metadata?.name) {
    return 'metadata.name 不可修改'
  }
  if ((parsed.metadata?.namespace ?? '') !== (base.metadata?.namespace ?? '')) {
    return 'metadata.namespace 不可修改'
  }
  return ''
}

// YamlCard shows the object as YAML/JSON in a Monaco editor. Edit flow
// (Kuboard parity): 编辑 refetches the latest object → client-side parse
// + identity-field check → diff → server Dry Run → confirm → real PUT.
// A stale resourceVersion surfaces as 409 with the draft preserved.
export function YamlCard({
  params,
  name,
  obj,
  presentation = 'card',
  triggerLabel = '预览 / 编辑 YAML',
  dialogTitle,
  editOnOpen = false,
}: {
  params: K8sObjectParams
  name: string
  obj: K8sObject
  presentation?: 'card' | 'dialog'
  triggerLabel?: string
  dialogTitle?: string
  editOnOpen?: boolean
}) {
  const queryClient = useQueryClient()
  const { resolvedTheme } = useTheme()
  const def = useResourceDef(params)
  const { query: capsQuery, caps } = useResourceCaps(params)
  // The type must allow manifests AND this user must hold update RBAC —
  // a read-only identity still gets the viewer, just no 编辑 toggle.
  const editable = def?.editable === true && caps?.update === true

  const [lang, setLang] = useState<Lang>('yaml')
  const [languageLoading, setLanguageLoading] = useState(false)
  const [editing, setEditing] = useState(false)
  const [text, setText] = useState('')
  const [error, setError] = useState('')
  const [diffOpen, setDiffOpen] = useState(false)
  // The object the current edit session is based on — refetched when
  // 编辑 is clicked so resourceVersion can't silently go stale.
  const [base, setBase] = useState<K8sObject | null>(null)
  const [entering, setEntering] = useState(false)
  const [dryRunOk, setDryRunOk] = useState(false)
  const [viewerOpen, setViewerOpen] = useState(false)

  // The rendered baseline always tracks the freshest object from the
  // server — edits build on it so resourceVersion stays current.
  const baseObj = base ?? obj
  const original = useMemo(() => serialize(baseObj, 'yaml'), [baseObj])
  const shown = editing ? text : serialize(obj, lang)

  const save = useMutation({
    mutationFn: (manifest: string) => k8sApi.update(name, params, manifest),
    onSuccess: () => {
      toast.success('已应用')
      setDiffOpen(false)
      setEditing(false)
      setBase(null)
      setError('')
      void queryClient.invalidateQueries({ queryKey: ['k8s'] })
    },
    onError: (e) => setError(errDetail(e)),
  })

  const dryRun = useMutation({
    mutationFn: (manifest: string) =>
      k8sApi.update(name, params, manifest, true),
    onSuccess: () => {
      setDryRunOk(true)
      setError('')
    },
    onError: (e) => {
      setDryRunOk(false)
      setError(errDetail(e))
    },
  })

  const startEdit = async () => {
    setEntering(true)
    setError('')
    try {
      // Always edit against the live object — the cached prop may be
      // minutes old and its resourceVersion would 409 on save.
      const res = await k8sApi.resource(name, params)
      setBase(res.data)
      setText(serialize(res.data, 'yaml'))
      setLang('yaml')
      setEditing(true)
      void queryClient.invalidateQueries({ queryKey: ['k8s'] })
    } catch (e) {
      setError(`加载最新资源失败：${errDetail(e)}`)
    } finally {
      setEntering(false)
    }
  }
  const cancelEdit = () => {
    setEditing(false)
    setText('')
    setBase(null)
    setError('')
  }
  // Language toggle converts the buffer between YAML and JSON through
  // the parsed object — a syntax error blocks the switch, not the data.
  const switchLang = async (next: Lang) => {
    if (next === lang) return
    const buf = editing ? text : serialize(obj, lang)
    const { obj: parsed, error: perr } = parseText(buf)
    if (perr) {
      setError(perr)
      return
    }
    if (editing && next === 'json') {
      setLanguageLoading(true)
      try {
        await import('monaco-editor/languages/features/json/register')
        // Load the large JSON mode before switching. Its worker factory only
        // exists after the register module's onLanguage callback runs, so do
        // not call register.getWorker() here; switching language below fires
        // that callback after the module is already cached.
        await import('monaco-editor/languages/features/json/jsonMode')
      } catch {
        setError('JSON 编辑支持加载失败；当前 YAML 内容仍保留，请重试。')
        return
      } finally {
        setLanguageLoading(false)
      }
    }
    setError('')
    setLang(next)
    if (editing) setText(serialize(parsed as K8sObject, next))
  }

  const requestSave = () => {
    const { obj: parsed, error: perr } = parseText(text)
    if (perr || !parsed) {
      setError(perr ?? '解析失败')
      return
    }
    // Identity fields are immutable client-side too — catching a renamed
    // kind/name here beats a 400 round-trip.
    const idErr = checkIdentity(parsed, baseObj)
    if (idErr) {
      setError(idErr)
      return
    }
    setError('')
    if (toYaml(parsed) === original) {
      toast.info('内容无变更')
      return
    }
    setDryRunOk(false)
    setDiffOpen(true)
  }

  const canonicalManifest = () => {
    const { obj: parsed } = parseText(text)
    return parsed ? toYaml(parsed) : ''
  }

  const confirmSave = () => {
    const manifest = canonicalManifest()
    if (manifest) save.mutate(manifest)
  }

  const diffParts = useMemo(() => {
    if (!diffOpen) return []
    return diffLines(original, toYaml(parseText(text).obj as K8sObject))
  }, [diffOpen, original, text])

  const diffStats = useMemo(() => {
    let added = 0
    let removed = 0
    for (const p of diffParts) {
      if (p.added) added += p.count ?? 0
      else if (p.removed) removed += p.count ?? 0
    }
    return { added, removed }
  }, [diffParts])

  const openViewer = () => {
    setViewerOpen(true)
    if (editOnOpen && editable) void startEdit()
  }

  const readonlyReason =
    editable === false && def?.editable === true && capsQuery.isSuccess
      ? '当前账号对此资源没有写权限，YAML 为只读'
      : editable === false && def != null && def.editable !== true
        ? '该资源类型为只读（含脱敏资源），不允许在线编辑'
        : null

  const editorBody = (
    <>
      <div className='flex flex-wrap items-center justify-between gap-3'>
        <div className='flex min-w-0 items-center gap-3'>
          <span className='flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary'>
            <FileCode className='size-4' />
          </span>
          <div className='min-w-0'>
            <div className='flex items-center gap-2'>
              <h3 className='text-base font-semibold text-foreground'>
                YAML / JSON
              </h3>
              {editing && (
                <span className='rounded-full bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-900 dark:bg-amber-950 dark:text-amber-200'>
                  编辑中
                </span>
              )}
            </div>
            <p className='mt-0.5 truncate text-xs text-muted-foreground'>
              {name} · 查看或编辑当前资源清单
            </p>
          </div>
        </div>
        <div className='flex items-center gap-2'>
          <div className='flex rounded-lg border bg-muted p-0.5'>
            {(['yaml', 'json'] as const).map((l) => (
              <button
                key={l}
                type='button'
                onClick={() => void switchLang(l)}
                disabled={languageLoading}
                className={cn(
                  'h-7 rounded-md px-3 text-xs font-medium transition-colors disabled:cursor-wait disabled:opacity-60',
                  lang === l
                    ? 'bg-background text-foreground shadow-sm'
                    : 'text-muted-foreground hover:text-foreground'
                )}
              >
                {l.toUpperCase()}
              </button>
            ))}
          </div>
          {editable && !editing && (
            <Button
              variant='outline'
              size='sm'
              className='h-8 rounded-lg border-primary/30 px-3 text-xs text-primary hover:bg-primary/5 hover:text-primary'
              onClick={() => void startEdit()}
              disabled={entering}
            >
              {entering
                ? '加载中…'
                : presentation === 'card'
                  ? '编辑'
                  : '编辑 YAML'}
            </Button>
          )}
          {editing && (
            <>
              <Button
                variant='ghost'
                size='sm'
                className='h-8 px-3 text-xs'
                onClick={cancelEdit}
              >
                取消
              </Button>
              <Button
                size='sm'
                className='h-8 rounded-lg px-3 text-xs'
                onClick={requestSave}
              >
                保存
              </Button>
            </>
          )}
        </div>
      </div>
      {languageLoading && (
        <div
          role='status'
          className='mt-3 flex items-center gap-2 rounded-lg border border-primary/15 bg-primary/5 px-3 py-2 text-xs text-primary'
        >
          <LoaderCircle className='size-3.5 shrink-0 animate-spin' />
          <span>正在加载 JSON 语法与校验支持…</span>
        </div>
      )}
      {readonlyReason != null && (
        <p className='mt-3 flex items-center gap-1.5 rounded-lg bg-muted px-3 py-2 text-xs text-muted-foreground'>
          <Info className='size-3.5 shrink-0' />
          {readonlyReason}
        </p>
      )}
      <div
        className={cn(
          'mt-4 overflow-hidden rounded-lg border transition-colors',
          editing ? 'border-primary/50 ring-2 ring-primary/10' : 'border-border'
        )}
      >
        {editing ? (
          <Suspense
            fallback={
              <div
                className='relative bg-muted/20'
                style={{
                  height:
                    presentation === 'dialog' ? 'min(56vh, 600px)' : '420px',
                }}
              >
                <p
                  role='status'
                  className='absolute inset-x-0 top-0 z-10 flex h-10 items-center gap-2 border-b bg-background/95 px-4 text-xs text-muted-foreground'
                >
                  <LoaderCircle className='size-3.5 shrink-0 animate-spin text-primary' />
                  <span>编辑器正在加载，可先直接编辑清单。</span>
                </p>
                <textarea
                  aria-label='YAML / JSON 清单编辑器'
                  autoCapitalize='off'
                  autoComplete='off'
                  autoCorrect='off'
                  spellCheck={false}
                  value={shown}
                  onChange={(event) => setText(event.target.value)}
                  className='h-full w-full resize-none bg-transparent px-4 pt-12 pb-4 font-mono text-xs leading-5 text-foreground focus-visible:outline-2 focus-visible:outline-ring'
                />
              </div>
            }
          >
            <MonacoEditor
              height={presentation === 'dialog' ? 'min(56vh, 600px)' : '420px'}
              language={lang}
              theme={resolvedTheme === 'dark' ? 'vs-dark' : 'vs'}
              value={shown}
              onChange={(v) => setText(v ?? '')}
              options={{
                readOnly: false,
                minimap: { enabled: false },
                fontSize: 12,
                padding: { top: 12, bottom: 12 },
                lineNumbersMinChars: 3,
                scrollBeyondLastLine: false,
                wordWrap: 'on',
                scrollbar: {
                  verticalScrollbarSize: 8,
                  horizontalScrollbarSize: 8,
                },
                overviewRulerLanes: 0,
                hideCursorInOverviewRuler: true,
                renderLineHighlight: 'line',
              }}
            />
          </Suspense>
        ) : (
          <pre
            aria-label='资源清单预览'
            tabIndex={0}
            className={cn(
              'min-h-[180px] overflow-auto bg-muted/20 p-4 font-mono text-xs leading-5 text-foreground',
              'break-words whitespace-pre-wrap focus-visible:outline-2 focus-visible:outline-ring',
              presentation === 'dialog' ? 'max-h-[56vh]' : 'max-h-[420px]'
            )}
          >
            {shown}
          </pre>
        )}
      </div>
      <div className='mt-2 flex items-center justify-between text-xs text-muted-foreground'>
        <span>
          {editing
            ? '编辑模式 · 保存前将生成 diff 并经服务端 Dry Run 校验'
            : '只读预览'}
        </span>
        <span className='shrink-0 font-mono whitespace-nowrap'>
          {lang.toUpperCase()} · {shown.split('\n').length} 行
        </span>
      </div>
      {!diffOpen && error !== '' && (
        <p className='mt-2 flex items-start gap-1.5 rounded-lg bg-destructive/10 px-3 py-2 text-xs leading-5 text-destructive'>
          {error}
        </p>
      )}
    </>
  )

  return (
    <>
      {presentation === 'card' ? (
        <Card className='rounded-2xl shadow-sm'>
          <CardHeader>
            <CardTitle className='sr-only'>YAML / JSON</CardTitle>
          </CardHeader>
          <CardContent>{editorBody}</CardContent>
        </Card>
      ) : (
        <>
          <Button
            variant={editOnOpen ? 'default' : 'outline'}
            size='sm'
            className='h-8 rounded-lg px-3 text-xs'
            onClick={openViewer}
          >
            {triggerLabel}
          </Button>
          <Dialog
            open={viewerOpen}
            onOpenChange={(open) => {
              setViewerOpen(open)
              if (!open && editing) cancelEdit()
            }}
          >
            <DialogContent className='max-h-[92vh] overflow-y-auto sm:max-w-4xl'>
              <DialogHeader className='sr-only'>
                <DialogTitle>{dialogTitle ?? triggerLabel}</DialogTitle>
                <DialogDescription>
                  查看并编辑 Kubernetes 资源清单。
                </DialogDescription>
              </DialogHeader>
              {editorBody}
            </DialogContent>
          </Dialog>
        </>
      )}

      <Dialog open={diffOpen} onOpenChange={setDiffOpen}>
        <DialogContent className='sm:max-w-3xl'>
          <DialogHeader className='flex-row items-start gap-3 space-y-0 text-start'>
            <span className='flex size-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary'>
              <FileDiff className='size-4' />
            </span>
            <div className='min-w-0 space-y-1'>
              <div className='flex items-center gap-2'>
                <DialogTitle>确认变更</DialogTitle>
                <span className='font-mono text-xs font-medium text-emerald-700 dark:text-emerald-300'>
                  +{diffStats.added}
                </span>
                <span className='font-mono text-xs font-medium text-rose-700 dark:text-rose-300'>
                  −{diffStats.removed}
                </span>
              </div>
              <DialogDescription>
                先经服务端 Dry Run 校验，通过后才能正式应用（resourceVersion
                乐观并发）。
              </DialogDescription>
            </div>
          </DialogHeader>
          <div className='max-h-96 overflow-auto rounded-lg border font-mono text-xs leading-5'>
            {diffParts.flatMap((p, i) =>
              p.value
                .replace(/\n$/, '')
                .split('\n')
                .map((line, j) => (
                  <div
                    key={`${i}-${j}`}
                    className={cn(
                      'flex whitespace-pre',
                      p.added
                        ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-200'
                        : p.removed
                          ? 'bg-rose-50 text-rose-800 dark:bg-rose-950/40 dark:text-rose-200'
                          : 'text-muted-foreground'
                    )}
                  >
                    <span className='inline-block w-8 shrink-0 px-2 text-center opacity-60 select-none'>
                      {p.added ? '+' : p.removed ? '-' : ''}
                    </span>
                    <span className='min-w-0'>{line || ' '}</span>
                  </div>
                ))
            )}
          </div>
          {dryRunOk && (
            <p className='flex items-center gap-1.5 rounded-lg bg-emerald-50 px-3 py-2 text-xs text-emerald-800 dark:bg-emerald-950/40 dark:text-emerald-200'>
              <CheckCircle2 className='size-3.5 shrink-0' />
              Dry Run 校验通过，可以正式应用。
            </p>
          )}
          {error !== '' && (
            <Alert variant='destructive' className='rounded-lg'>
              <AlertDescription>
                {error}
                {(save.error != null || dryRun.error != null) &&
                  (isConflict(save.error) || isConflict(dryRun.error)) &&
                  '（对象已被他人修改——草稿已保留，请返回编辑并点击「编辑」重新加载最新对象）'}
              </AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button variant='outline' onClick={() => setDiffOpen(false)}>
              返回编辑
            </Button>
            {dryRunOk ? (
              <Button disabled={save.isPending} onClick={confirmSave}>
                {save.isPending ? '应用中…' : '确认应用'}
              </Button>
            ) : (
              <Button
                variant='outline'
                disabled={dryRun.isPending}
                onClick={() => dryRun.mutate(canonicalManifest())}
              >
                {dryRun.isPending ? '校验中…' : 'Dry Run 校验'}
              </Button>
            )}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
