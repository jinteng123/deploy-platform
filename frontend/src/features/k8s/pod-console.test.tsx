import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import type { K8sObject, K8sResourceQuery } from '@/api/client'

// Mock xterm: the real renderer needs canvas layout — a stub records
// writes/resets and exposes the data/resize handlers so tests can drive
// the protocol layer deterministically.
const written: (Uint8Array | string)[] = []
const dataHandlers: ((d: string) => void)[] = []
const resizeHandlers: ((s: { cols: number; rows: number }) => void)[] = []

vi.mock('@xterm/xterm', () => {
  class Terminal {
    cols = 80
    rows = 24
    write(d: Uint8Array | string) {
      written.push(d)
    }
    reset() {
      written.length = 0
    }
    loadAddon() {}
    open() {}
    dispose() {}
    onData(cb: (d: string) => void) {
      dataHandlers.push(cb)
      return { dispose() {} }
    }
    onResize(cb: (s: { cols: number; rows: number }) => void) {
      resizeHandlers.push(cb)
      return { dispose() {} }
    }
  }
  return { Terminal }
})

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}))

vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

// Capabilities verdict: default grants logs+exec so both tabs render;
// tests override per-case via capsMock.
const capsMock = vi.fn()
vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return { ...actual, k8sApi: { capabilities: capsMock } }
})

// Mock WebSocket: records instances, lets tests push server messages.
class MockWS {
  static OPEN = 1
  static instances: MockWS[] = []
  url: string
  binaryType = ''
  readyState = 0
  sent: ArrayBuffer[] = []
  onopen: (() => void) | null = null
  onmessage: ((e: { data: ArrayBuffer }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: ((e: { reason: string }) => void) | null = null

  constructor(url: string) {
    this.url = url
    MockWS.instances.push(this)
    queueMicrotask(() => {
      this.readyState = 1
      this.onopen?.()
    })
  }
  send(data: ArrayBuffer) {
    this.sent.push(data)
  }
  close() {
    this.readyState = 3
    this.onclose?.({ reason: '' })
  }
  // Test helpers
  emit(data: ArrayBuffer) {
    this.onmessage?.({ data })
  }
  serverClose(reason = '') {
    this.readyState = 3
    this.onclose?.({ reason })
  }
}
vi.stubGlobal('WebSocket', MockWS)

const { PodConsoleCard } = await import('./pod-console')
const { podContainers } = await import('./pod-containers')

const params: K8sResourceQuery = {
  cluster_id: 1,
  group: '',
  version: 'v1',
  resource: 'pods',
  namespace: 'default',
}

const podObj: K8sObject = {
  apiVersion: 'v1',
  kind: 'Pod',
  metadata: { name: 'web-1', namespace: 'default' },
  spec: {
    containers: [{ name: 'app' }, { name: 'sidecar' }],
    initContainers: [{ name: 'init' }],
  },
}

function renderCard(
  obj: K8sObject = podObj,
  options: {
    initialTab?: 'logs' | 'terminal'
    initialContainer?: string
    initialCommand?: string
    autoConnect?: boolean
  } = {}
) {
  const qc = new QueryClient()
  return render(
    <QueryClientProvider client={qc}>
      <PodConsoleCard params={params} name='web-1' obj={obj} {...options} />
    </QueryClientProvider>
  )
}

function lastWS(): MockWS {
  const ws = MockWS.instances[MockWS.instances.length - 1]
  if (!ws) throw new Error('no WebSocket created')
  return ws
}

async function flush() {
  await new Promise((r) => setTimeout(r, 0))
}

beforeEach(() => {
  MockWS.instances = []
  written.length = 0
  dataHandlers.length = 0
  resizeHandlers.length = 0
  capsMock.mockReset()
  capsMock.mockResolvedValue({
    data: { resources: {}, exec: true, logs: true },
  })
})

describe('podContainers', () => {
  it('lists containers, init and ephemeral', () => {
    expect(podContainers(podObj)).toEqual(['app', 'sidecar', 'init'])
    expect(podContainers({ spec: {} })).toEqual([])
    expect(podContainers(undefined)).toEqual([])
  })
})

describe('PodConsoleCard', () => {
  it('renders logs and terminal tabs', async () => {
    renderCard()
    await expect.element(page.getByText('日志与终端')).toBeVisible()
    await expect.element(page.getByRole('tab', { name: '日志' })).toBeVisible()
    await expect.element(page.getByRole('tab', { name: '终端' })).toBeVisible()
    await expect
      .element(page.getByRole('button', { name: '连接', exact: true }))
      .toBeVisible()
  })

  it('log stream connects with correct URL and writes frames to the terminal', async () => {
    renderCard()
    await userEvent.click(page.getByRole('button', { name: '连接', exact: true }))
    await flush()
    const ws = lastWS()
    expect(ws.url).toContain('/api/v1/k8s/pods/web-1/logs/stream?')
    expect(ws.url).toContain('cluster_id=1')
    expect(ws.url).toContain('namespace=default')
    expect(ws.url).toContain('tail=200')
    expect(ws.url).toContain('follow=true')
    expect(ws.url).not.toContain('container=')
    await expect.element(page.getByText('已连接')).toBeVisible()

    ws.emit(new TextEncoder().encode('hello logs\n').buffer as ArrayBuffer)
    await flush()
    expect(written.length).toBe(1)
    expect(new TextDecoder().decode(written[0] as Uint8Array)).toBe(
      'hello logs\n'
    )
  })

  it('log stream passes the selected container and follow flag', async () => {
    renderCard()
    // open the container select, pick "sidecar"
    await userEvent.click(page.getByRole('combobox').first())
    await userEvent.click(page.getByRole('option', { name: 'sidecar' }))
    // turn follow off
    await userEvent.click(page.getByRole('switch'))
    await userEvent.click(page.getByRole('button', { name: '连接', exact: true }))
    await flush()
    const ws = lastWS()
    expect(ws.url).toContain('container=sidecar')
    expect(ws.url).toContain('follow=false')
  })

  it('terminal connects, sends stdin/resize frames, and handles exit', async () => {
    renderCard()
    await userEvent.click(page.getByRole('tab', { name: '终端' }))
    await userEvent.click(page.getByRole('button', { name: '连接', exact: true }))
    await flush()
    const ws = lastWS()
    expect(ws.url).toContain('/api/v1/k8s/pods/web-1/terminal?')
    expect(ws.url).toContain('container=app')
    expect(ws.url).toContain(encodeURIComponent('/bin/sh'))

    // initial resize pushed on open
    const resize = ws.sent.find((s) => new Uint8Array(s)[0] === 4)
    expect(resize).toBeTruthy()
    expect(
      JSON.parse(new TextDecoder().decode(new Uint8Array(resize!).subarray(1)))
    ).toEqual({
      cols: 80,
      rows: 24,
    })

    // server stdout → terminal write
    const payload = new Uint8Array([1, ...new TextEncoder().encode('shell$ ')])
    ws.emit(payload.buffer as ArrayBuffer)
    await flush()
    expect(new TextDecoder().decode(written[0] as Uint8Array)).toBe('shell$ ')

    // user input → channel-0 frame
    dataHandlers.forEach((h) => h('ls\r'))
    const stdin = ws.sent.find((s) => new Uint8Array(s)[0] === 0)
    expect(new TextDecoder().decode(new Uint8Array(stdin!).subarray(1))).toBe(
      'ls\r'
    )

    // resize → channel-4 frame
    resizeHandlers.forEach((h) => h({ cols: 120, rows: 40 }))
    const rs = ws.sent[ws.sent.length - 1]
    expect(new Uint8Array(rs)[0]).toBe(4)
    expect(
      JSON.parse(new TextDecoder().decode(new Uint8Array(rs).subarray(1)))
    ).toEqual({
      cols: 120,
      rows: 40,
    })

    // exit frame → status + hint
    const exit = new Uint8Array([
      3,
      ...new TextEncoder().encode('session ended'),
    ])
    ws.emit(exit.buffer as ArrayBuffer)
    await flush()
    await expect.element(page.getByText('session ended')).toBeVisible()
    await expect.element(page.getByText('已断开').first()).toBeVisible()
  })

  it('opens the requested shell and container and connects automatically', async () => {
    renderCard(podObj, {
      initialTab: 'terminal',
      initialContainer: 'sidecar',
      initialCommand: '/bin/bash',
      autoConnect: true,
    })
    await flush()
    await flush()
    await flush()
    await flush()
    const ws = lastWS()
    expect(ws.url).toContain('container=sidecar')
    expect(ws.url).toContain(encodeURIComponent('/bin/bash'))
    await expect
      .element(page.getByRole('button', { name: '断开', exact: true }))
      .toBeVisible()
  })

  it('terminal disables connect when the pod has no containers', async () => {
    renderCard({ spec: {} })
    await userEvent.click(page.getByRole('tab', { name: '终端' }))
    const btn = page.getByRole('button', { name: '连接', exact: true })
    await expect.element(btn).toBeDisabled()
    await expect.element(page.getByText('该 Pod 没有可用容器')).toBeVisible()
  })

  it('disconnects on unmount/close', async () => {
    renderCard()
    await userEvent.click(page.getByRole('tab', { name: '终端' }))
    await userEvent.click(page.getByRole('button', { name: '连接', exact: true }))
    await flush()
    const ws = lastWS()
    await userEvent.click(page.getByRole('button', { name: '断开', exact: true }))
    expect(ws.readyState).toBe(3)
    await expect.element(page.getByText('已断开').first()).toBeVisible()
    await expect
      .element(page.getByRole('button', { name: '重新连接', exact: true }))
      .toBeVisible()
  })

  it('hides the terminal tab when RBAC denies pods/exec', async () => {
    capsMock.mockResolvedValue({
      data: { resources: {}, exec: false, logs: true },
    })
    renderCard()
    await expect.element(page.getByRole('tab', { name: '日志' })).toBeVisible()
    await expect
      .element(page.getByRole('tab', { name: '终端' }))
      .not.toBeInTheDocument()
  })

  it('renders nothing when the verdict denies both logs and exec', async () => {
    capsMock.mockResolvedValue({
      data: { resources: {}, exec: false, logs: false },
    })
    renderCard()
    await flush()
    await expect.element(page.getByText('日志与终端')).not.toBeInTheDocument()
  })
})
