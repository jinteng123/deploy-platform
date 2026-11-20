import { useCallback, useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import type { K8sObject, K8sResourceQuery } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { podContainers } from './pod-containers'
import { useCapabilities } from './use-capabilities'

// WS frame protocol (see backend k8s_stream.go): binary messages with a
// 1-byte channel prefix. In: 1=stdout/log data, 2=stderr, 3=session end.
// Out: 0=stdin, 4=resize(JSON cols/rows). Log streams send unprefixed
// raw bytes instead.
const chStdin = 0
const chStdout = 1
const chStderr = 2
const chExit = 3
const chResize = 4

function wsURL(
  path: string,
  params: Record<string, string | number | undefined>
): string {
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') q.set(k, String(v))
  }
  return `${proto}://${location.host}/api/v1${path}?${q}`
}

const enc = new TextEncoder()
const dec = new TextDecoder()

function frame(channel: number, payload: Uint8Array | string): ArrayBuffer {
  const bytes = typeof payload === 'string' ? enc.encode(payload) : payload
  const out = new Uint8Array(1 + bytes.length)
  out[0] = channel
  out.set(bytes, 1)
  return out.buffer as ArrayBuffer
}

type Status = 'idle' | 'connecting' | 'connected' | 'closed' | 'error'

const statusText: Record<Status, string> = {
  idle: '未连接',
  connecting: '连接中…',
  connected: '已连接',
  closed: '已断开',
  error: '连接失败',
}

// useXterm mounts a Terminal on the returned ref and disposes it on
// unmount. onReady fires once the terminal is open and fitted.
function useXterm(opts: { readOnly?: boolean } = {}) {
  const ref = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)

  useEffect(() => {
    if (!ref.current) return
    const term = new Terminal({
      convertEol: true,
      fontSize: 13,
      cursorBlink: !opts.readOnly,
      disableStdin: opts.readOnly,
      theme: { background: '#0b0e14' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(ref.current)
    fit.fit()
    termRef.current = term
    fitRef.current = fit
    const ro = new ResizeObserver(() => {
      try {
        fit.fit()
      } catch {
        // container hidden mid-transition — ignore
      }
    })
    ro.observe(ref.current)
    return () => {
      ro.disconnect()
      term.dispose()
      termRef.current = null
      fitRef.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return { ref, termRef, fitRef }
}

export function PodConsoleCard({
  params,
  name,
  obj,
  initialTab,
  initialContainer,
  initialCommand,
  autoConnect,
}: {
  params: K8sResourceQuery
  name: string
  obj: K8sObject
  initialTab?: 'logs' | 'terminal'
  initialContainer?: string
  initialCommand?: string
  autoConnect?: boolean
}) {
  const containers = podContainers(obj)
  const capsQuery = useCapabilities(params.cluster_id, params.namespace)
  // Gate each tab on the caller's RBAC verdict; hide the card entirely when
  // the verdict denies both (or hasn't arrived — fail closed).
  const canLogs = capsQuery.data?.logs === true
  const canExec = capsQuery.data?.exec === true
  if (!canLogs && !canExec) return null
  return (
    <Card className='rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.05)]'>
      <CardHeader>
        <CardTitle className='text-base text-[#1c355e]'>日志与终端</CardTitle>
      </CardHeader>
      <CardContent>
        <Tabs
          key={initialTab}
          defaultValue={initialTab ?? (canLogs ? 'logs' : 'terminal')}
        >
          <TabsList className='rounded-xl bg-[#e8eff9] p-1'>
            {canLogs && <TabsTrigger value='logs'>日志</TabsTrigger>}
            {canExec && <TabsTrigger value='terminal'>终端</TabsTrigger>}
          </TabsList>
          {canLogs && (
            <TabsContent value='logs'>
              <LogPanel params={params} pod={name} containers={containers} />
            </TabsContent>
          )}
          {canExec && (
            <TabsContent value='terminal'>
              <TerminalPanel
                params={params}
                pod={name}
                containers={containers}
                initialContainer={initialContainer}
                initialCommand={initialCommand}
                autoConnect={autoConnect}
              />
            </TabsContent>
          )}
        </Tabs>
      </CardContent>
    </Card>
  )
}

export function LogPanel({
  params,
  pod,
  containers,
  initialContainer,
  autoConnect,
  heightClass = 'h-80',
}: {
  params: K8sResourceQuery
  pod: string
  containers: string[]
  initialContainer?: string
  autoConnect?: boolean
  heightClass?: string
}) {
  const [container, setContainer] = useState(
    initialContainer && containers.includes(initialContainer)
      ? initialContainer
      : ''
  )
  const [tail, setTail] = useState('200')
  const [follow, setFollow] = useState(true)
  const [status, setStatus] = useState<Status>('idle')
  const [hint, setHint] = useState('')
  const { ref, termRef } = useXterm({ readOnly: true })
  const wsRef = useRef<WebSocket | null>(null)

  const disconnect = useCallback(() => {
    wsRef.current?.close()
    wsRef.current = null
  }, [])

  const connect = useCallback(() => {
    disconnect()
    const term = termRef.current
    if (!term) return
    term.reset()
    setStatus('connecting')
    setHint('')
    const ws = new WebSocket(
      wsURL(`/k8s/pods/${encodeURIComponent(pod)}/logs/stream`, {
        cluster_id: params.cluster_id,
        namespace: params.namespace,
        container: container || undefined,
        tail,
        follow: String(follow),
      })
    )
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => setStatus('connected')
    ws.onmessage = (e) => {
      if (e.data instanceof ArrayBuffer) term.write(new Uint8Array(e.data))
    }
    ws.onerror = () => {
      setStatus('error')
      setHint('连接失败，请检查 Pod 是否存在或稍后重试')
    }
    ws.onclose = (e) => {
      setStatus((s) => (s === 'error' ? s : 'closed'))
      if (e.reason) setHint(e.reason)
    }
    wsRef.current = ws
  }, [
    disconnect,
    termRef,
    pod,
    params.cluster_id,
    params.namespace,
    container,
    tail,
    follow,
  ])

  // Changing container/tail/follow mid-session reconnects automatically.
  useEffect(() => {
    if (status === 'connected' || status === 'connecting') connect()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [container, tail, follow])

  useEffect(() => {
    if (!autoConnect || status !== 'idle') return
    const timer = window.setTimeout(connect, 0)
    return () => window.clearTimeout(timer)
  }, [autoConnect, connect, status])

  useEffect(() => disconnect, [disconnect])

  return (
    <div className='mt-3 space-y-3'>
      <div className='flex flex-wrap items-end gap-3'>
        <div className='space-y-1'>
          <Label>容器</Label>
          <Select
            value={container || 'all'}
            onValueChange={(v) => setContainer(v === 'all' ? '' : v)}
          >
            <SelectTrigger className='w-44'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value='all'>默认容器</SelectItem>
              {containers.map((c) => (
                <SelectItem key={c} value={c}>
                  {c}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className='space-y-1'>
          <Label>尾部行数</Label>
          <Select value={tail} onValueChange={setTail}>
            <SelectTrigger className='w-28'>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {['50', '200', '500', '1000'].map((n) => (
                <SelectItem key={n} value={n}>
                  {n}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className='flex items-center gap-2 pb-1'>
          <Switch
            id='log-follow'
            checked={follow}
            onCheckedChange={setFollow}
          />
          <Label htmlFor='log-follow'>实时滚动</Label>
        </div>
        <Button
          size='sm'
          variant={
            status === 'connected' || status === 'connecting'
              ? 'outline'
              : 'default'
          }
          onClick={() =>
            status === 'connected' || status === 'connecting'
              ? disconnect()
              : connect()
          }
        >
          {status === 'connected' || status === 'connecting' ? '断开' : '连接'}
        </Button>
        <span className='text-xs text-muted-foreground'>
          {statusText[status]}
        </span>
      </div>
      {hint !== '' && <p className='text-xs text-muted-foreground'>{hint}</p>}
      <div
        ref={ref}
        className={`${heightClass} overflow-hidden rounded-md border bg-[#0b0e14] p-1`}
      />
    </div>
  )
}

export function TerminalPanel({
  params,
  pod,
  containers,
  initialContainer,
  initialCommand,
  autoConnect,
  heightClass = 'h-96',
}: {
  params: K8sResourceQuery
  pod: string
  containers: string[]
  initialContainer?: string
  initialCommand?: string
  autoConnect?: boolean
  heightClass?: string
}) {
  const [container, setContainer] = useState(
    initialContainer && containers.includes(initialContainer)
      ? initialContainer
      : (containers[0] ?? '')
  )
  const [command, setCommand] = useState(initialCommand ?? '/bin/sh')
  const [status, setStatus] = useState<Status>('idle')
  const [hint, setHint] = useState('')
  const { ref, termRef, fitRef } = useXterm()
  const wsRef = useRef<WebSocket | null>(null)

  const disconnect = useCallback(() => {
    wsRef.current?.close()
    wsRef.current = null
  }, [])

  const connect = useCallback(() => {
    disconnect()
    const term = termRef.current
    if (!term) return
    term.reset()
    setStatus('connecting')
    setHint('')
    let exitNotified = false
    const ws = new WebSocket(
      wsURL(`/k8s/pods/${encodeURIComponent(pod)}/terminal`, {
        cluster_id: params.cluster_id,
        namespace: params.namespace,
        container: container || undefined,
        command,
      })
    )
    ws.binaryType = 'arraybuffer'
    ws.onopen = () => {
      setStatus('connected')
      // Push the initial size so the remote PTY matches the viewport.
      if (term.cols > 0 && term.rows > 0) {
        ws.send(
          frame(chResize, JSON.stringify({ cols: term.cols, rows: term.rows }))
        )
      }
    }
    ws.onmessage = (e) => {
      if (!(e.data instanceof ArrayBuffer)) return
      const buf = new Uint8Array(e.data)
      if (buf.length === 0) return
      switch (buf[0]) {
        case chStdout:
        case chStderr:
          term.write(buf.subarray(1))
          break
        case chExit: {
          const reason = dec.decode(buf.subarray(1))
          exitNotified = true
          setStatus('closed')
          setHint(reason)
          term.write(`\r\n\x1b[33m[会话已结束] ${reason}\x1b[0m\r\n`)
          break
        }
      }
    }
    ws.onerror = () => {
      if (wsRef.current !== null && wsRef.current !== ws) return
      setStatus('error')
      setHint('连接失败，该集群接入方式可能不支持容器终端')
    }
    ws.onclose = (e) => {
      // Drop close events only when this socket was REPLACED by a newer
      // one (reconnect). wsRef === null means a manual disconnect via
      // disconnect() — that close must still update status and write
      // the marker.
      if (wsRef.current !== null && wsRef.current !== ws) return
      wsRef.current = null
      setStatus((s) => (s === 'error' ? s : 'closed'))
      if (e.reason) setHint(e.reason)
      if (!exitNotified) {
        term.write(`\r\n\x1b[90m[已断开 — 点击"连接"重新打开]\x1b[0m\r\n`)
      }
    }
    wsRef.current = ws
  }, [
    disconnect,
    termRef,
    pod,
    params.cluster_id,
    params.namespace,
    container,
    command,
  ])

  useEffect(() => {
    if (!autoConnect || status !== 'idle') return
    const timer = window.setTimeout(connect, 0)
    return () => window.clearTimeout(timer)
  }, [autoConnect, connect, status])

  // Input/resize handlers attach once — registering them inside connect()
  // would duplicate stdin sends on every reconnect. They read the live
  // socket from wsRef at send time.
  useEffect(() => {
    const term = termRef.current
    if (!term) return
    const d1 = term.onData((d) => {
      const ws = wsRef.current
      if (ws?.readyState === WebSocket.OPEN) ws.send(frame(chStdin, d))
    })
    const d2 = term.onResize(({ cols, rows }) => {
      const ws = wsRef.current
      if (ws?.readyState === WebSocket.OPEN && cols > 0 && rows > 0) {
        ws.send(frame(chResize, JSON.stringify({ cols, rows })))
      }
    })
    return () => {
      d1.dispose()
      d2.dispose()
    }
  }, [termRef])

  // Re-fit after mount so the first resize event reports real cells.
  useEffect(() => {
    fitRef.current?.fit()
  }, [fitRef])

  useEffect(() => disconnect, [disconnect])

  return (
    <div className='mt-3 overflow-hidden rounded-xl border border-[#1b2436] bg-[#0b0e14] shadow-lg'>
      <div className='flex flex-wrap items-center gap-2 border-b border-white/8 px-3 py-2'>
        <Select value={container} onValueChange={setContainer}>
          <SelectTrigger className='h-8 w-44 border-white/10 bg-white/5 text-xs text-slate-200 shadow-none hover:bg-white/10'>
            <SelectValue placeholder='选择容器' />
          </SelectTrigger>
          <SelectContent>
            {containers.map((c) => (
              <SelectItem key={c} value={c}>
                {c}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={command} onValueChange={setCommand}>
          <SelectTrigger className='h-8 w-36 border-white/10 bg-white/5 font-mono text-xs text-slate-200 shadow-none hover:bg-white/10'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {['/bin/bash', '/bin/sh', 'cmd', 'powershell'].map((s) => (
              <SelectItem key={s} value={s}>
                {s}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <span className='inline-flex items-center gap-1.5 ps-1 text-xs text-slate-400'>
          <span
            className={`size-1.5 rounded-full ${
              status === 'connected'
                ? 'bg-emerald-400'
                : status === 'connecting'
                  ? 'animate-pulse bg-amber-400'
                  : status === 'error'
                    ? 'bg-rose-400'
                    : 'bg-slate-500'
            }`}
          />
          {statusText[status]}
        </span>
        {containers.length === 0 && (
          <span className='text-xs text-slate-500'>该 Pod 没有可用容器</span>
        )}
        <div className='ms-auto'>
          <Button
            size='sm'
            variant='ghost'
            className='h-8 text-xs text-slate-300 hover:bg-white/10 hover:text-white'
            onClick={() =>
              status === 'connected' || status === 'connecting'
                ? disconnect()
                : connect()
            }
            disabled={containers.length === 0}
          >
            {status === 'connected' || status === 'connecting'
              ? '断开'
              : '连接'}
          </Button>
        </div>
      </div>
      {hint !== '' && (
        <p className='border-b border-white/8 bg-amber-400/5 px-3 py-1.5 text-xs break-all text-amber-300/90'>
          {hint}
        </p>
      )}
      <div className='relative'>
        <div ref={ref} className={`${heightClass} p-1`} />
        {status !== 'connected' && status !== 'connecting' && (
          <div className='absolute inset-0 flex flex-col items-center justify-center gap-3 bg-[#0b0e14]/75'>
            <p className='text-sm text-slate-400'>{statusText[status]}</p>
            {containers.length > 0 && (
              <Button
                size='sm'
                variant='outline'
                className='border-white/15 bg-transparent text-slate-200 hover:bg-white/10 hover:text-white'
                onClick={connect}
              >
                重新连接
              </Button>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
