import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page, userEvent } from 'vitest/browser'
import {
  k8sApi,
  type K8sObject,
  type K8sObjectParams,
  type K8sResourceType,
} from '@/api/client'
import { DirectionProvider } from '@/context/direction-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { YamlCard } from './yaml-editor'

const monacoLoadTracker = vi.hoisted(() => ({ events: [] as string[] }))

// Monaco is stubbed with a textarea — the editor's chrome is irrelevant
// to the behavior under test (parse/diff/save), and the real Monaco is
// far too heavy for the browser runner.
vi.mock('@monaco-editor/react', async () => {
  monacoLoadTracker.events.push('react:start')
  return {
    default: ({
      value,
      onChange,
      theme,
    }: {
      value: string
      onChange?: (v: string) => void
      theme?: string
    }) => (
      <textarea
        aria-label='monaco-stub'
        value={value}
        onChange={(e) => onChange?.(e.target.value)}
        style={{
          backgroundColor: theme === 'vs-dark' ? '#1e1e1e' : '#ffffff',
          color: theme === 'vs-dark' ? '#d4d4d4' : '#1e1e1e',
        }}
      />
    ),
    loader: { config: vi.fn() },
  }
})
vi.mock('@/features/k8s/monaco', async () => {
  monacoLoadTracker.events.push('bootstrap:start')
  await new Promise((resolve) => setTimeout(resolve, 750))
  monacoLoadTracker.events.push('bootstrap:complete')
  return {}
})
vi.mock('monaco-editor/languages/features/json/register', () => ({}))
vi.mock('monaco-editor/languages/features/json/jsonMode', async () => {
  await new Promise((resolve) => setTimeout(resolve, 400))
  return {}
})

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return {
    ...actual,
    k8sApi: {
      resourceTypes: vi.fn(),
      capabilities: vi.fn(),
      resource: vi.fn(),
      update: vi.fn(),
    },
  }
})
const api = vi.mocked(k8sApi)

// RBAC verdict granting update on the resources under test.
const capsAll = (keys: string[]) => ({
  data: {
    resources: Object.fromEntries(
      keys.map((k) => [k, { list: true, update: true, delete: true }])
    ),
    exec: true,
    logs: true,
  },
})

const dep: K8sObject = {
  apiVersion: 'apps/v1',
  kind: 'Deployment',
  metadata: { name: 'web', namespace: 'prod', resourceVersion: '77' },
  spec: {
    replicas: 2,
    template: { spec: { containers: [{ name: 'app', image: 'nginx:1.25' }] } },
  },
}
const params: K8sObjectParams = {
  cluster_id: 1,
  group: 'apps',
  version: 'v1',
  resource: 'deployments',
  namespace: 'prod',
}
const editableDef: K8sResourceType = {
  group: 'apps',
  version: 'v1',
  resource: 'deployments',
  kind: 'Deployment',
  namespaced: true,
  editable: true,
}

async function renderCard(
  obj: K8sObject = dep,
  p: K8sObjectParams = params,
  theme: 'light' | 'dark' = 'light'
) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const screen = await render(
    <QueryClientProvider client={client}>
      <DirectionProvider>
        <ThemeProvider defaultTheme={theme}>
          <YamlCard params={p} name={obj.metadata?.name ?? 'web'} obj={obj} />
        </ThemeProvider>
      </DirectionProvider>
    </QueryClientProvider>
  )
  return { screen }
}

describe('YamlCard', () => {
  beforeEach(async () => {
    await page.viewport(1440, 1000)
    vi.clearAllMocks()
    api.resourceTypes.mockResolvedValue({ data: [editableDef] } as never)
    api.capabilities.mockResolvedValue(
      capsAll(['apps/v1/deployments', '/v1/secrets']) as never
    )
    api.update.mockResolvedValue({ data: dep } as never)
    // startEdit refetches the live object before unlocking the buffer.
    api.resource.mockResolvedValue({ data: dep } as never)
  })

  it('renders a lightweight read-only YAML preview and offers edit for an editable kind', async () => {
    const { screen } = await renderCard()
    const preview = screen.getByLabelText('资源清单预览')
    await expect.element(preview).toHaveTextContent('kind: Deployment')
    expect(screen.getByRole('textbox').query()).toBeNull()
    await expect
      .element(screen.getByRole('button', { name: '编辑' }))
      .toBeInTheDocument()
  })

  it('keeps the draft editable while Monaco loads, then transfers it to the editor', async () => {
    await page.viewport(390, 844)
    const { screen } = await renderCard()
    monacoLoadTracker.events = []
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))

    const quickEditor = screen.getByRole('textbox', {
      name: 'YAML / JSON 清单编辑器',
    })
    await expect.element(quickEditor).toBeVisible()
    await expect
      .element(screen.getByRole('status'))
      .toHaveTextContent('可先直接编辑清单')
    await page.screenshot({
      path: '../../../../docs/visual-checks/yaml-editor-loading-390-light.png',
      element: quickEditor.element().parentElement!,
    })
    document.documentElement.classList.remove('light')
    document.documentElement.classList.add('dark')
    await page.screenshot({
      path: '../../../../docs/visual-checks/yaml-editor-loading-390-dark.png',
      element: quickEditor.element().parentElement!,
    })
    document.documentElement.classList.remove('dark')
    document.documentElement.classList.add('light')
    await page.viewport(1440, 1000)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(1440)
    await page.screenshot({
      path: '../../../../docs/visual-checks/yaml-editor-loading-1440-light.png',
      element: quickEditor.element().parentElement!,
    })
    await page.viewport(390, 844)
    const draft = 'kind: Deployment\nmetadata:\n  name: edited-during-load'
    await userEvent.fill(quickEditor, draft)

    await expect
      .poll(() => monacoLoadTracker.events)
      .toEqual(['bootstrap:start', 'react:start', 'bootstrap:complete'])
    await expect
      .element(screen.getByLabelText('monaco-stub'))
      .toHaveValue(draft)
  })

  it('stays read-only for a non-editable kind and says why', async () => {
    api.resourceTypes.mockResolvedValue({
      data: [
        {
          group: '',
          version: 'v1',
          resource: 'secrets',
          kind: 'Secret',
          namespaced: true,
        },
      ],
    } as never)
    const { screen } = await renderCard(
      {
        apiVersion: 'v1',
        kind: 'Secret',
        metadata: { name: 's1', resourceVersion: '1' },
        data: { k: '***' },
      },
      { ...params, group: '', resource: 'secrets' }
    )
    await expect.element(screen.getByText(/只读/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '编辑' }).query()).toBeNull()
  })

  it('stays read-only for an editable kind when RBAC denies update', async () => {
    // deployments IS editable — but this user's verdict denies update, so
    // the 编辑 toggle disappears and the hint names the real reason.
    api.capabilities.mockResolvedValue({
      data: {
        resources: {
          'apps/v1/deployments': { list: true, update: false, delete: false },
        },
        exec: false,
        logs: true,
      },
    } as never)
    const { screen } = await renderCard()
    await expect.element(screen.getByText(/没有写权限/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '编辑' }).query()).toBeNull()
  })

  it('keeps the read-only preview on shared theme surfaces in dark mode', async () => {
    await page.viewport(1024, 768)
    try {
      const longImage = `registry.example.com/team/order-api@sha256:${'ab'.repeat(32)}`
      const previewObject: K8sObject = {
        ...dep,
        spec: {
          ...dep.spec,
          template: {
            spec: { containers: [{ name: 'app', image: longImage }] },
          },
        },
      }
      const { screen } = await renderCard(previewObject)
      document.documentElement.classList.remove('light')
      document.documentElement.classList.add('dark')
      const preview = screen.getByLabelText('资源清单预览')
      await expect.element(preview).toBeVisible()
      await expect
        .element(screen.getByRole('button', { name: '编辑' }))
        .toBeEnabled()
      await expect.element(preview).toHaveTextContent(longImage)
      expect(preview.element().className).toContain('text-foreground')
      expect(preview.element().className).toContain('bg-muted/20')
      await page.screenshot({
        path: '../../../../docs/visual-checks/yaml-preview-1024-dark.png',
        element: preview.element().closest('[data-slot="card"]')!,
      })
      document.documentElement.classList.remove('dark')
      document.documentElement.classList.add('light')
      await page.viewport(390, 844)
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
      await page.screenshot({
        path: '../../../../docs/visual-checks/yaml-preview-390-light.png',
        element: preview.element().closest('[data-slot="card"]')!,
      })
      document.documentElement.classList.remove('light')
      document.documentElement.classList.add('dark')
      await page.viewport(512, 384)
      const editButton = screen.getByRole('button', { name: '编辑' }).element()
      const editRect = editButton.getBoundingClientRect()
      expect(editRect.left).toBeGreaterThanOrEqual(0)
      expect(editRect.right).toBeLessThanOrEqual(512)
      expect(editRect.top).toBeGreaterThanOrEqual(0)
      expect(editRect.bottom).toBeLessThanOrEqual(384)
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(512)
    } finally {
      document.documentElement.classList.remove('dark')
    }
  })

  it('toggles YAML ↔ JSON through the parsed object', async () => {
    const { screen } = await renderCard()
    await userEvent.click(screen.getByRole('button', { name: 'JSON' }))
    await expect
      .element(screen.getByLabelText('资源清单预览'))
      .toHaveTextContent('"kind": "Deployment"')
    await userEvent.click(screen.getByRole('button', { name: 'YAML' }))
    await expect
      .element(screen.getByLabelText('资源清单预览'))
      .toHaveTextContent('kind: Deployment')
  })

  it('loads JSON editor support on demand while preserving the editable object', async () => {
    await page.viewport(390, 844)
    const { screen } = await renderCard()
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    await expect.element(screen.getByRole('textbox')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'JSON' }))
    await expect
      .element(screen.getByText('正在加载 JSON 语法与校验支持…'))
      .toBeInTheDocument()
    expect(
      (
        screen
          .getByRole('button', { name: 'JSON' })
          .element() as HTMLButtonElement
      ).disabled
    ).toBe(true)
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
    await page.screenshot({
      path: '../../../../docs/visual-checks/yaml-json-language-loading-390-light.png',
      element: screen
        .getByRole('heading', { name: 'YAML / JSON' })
        .element()
        .closest<HTMLElement>('[data-slot="card"]')!,
    })
    await expect
      .poll(
        () => (screen.getByRole('textbox').query() as HTMLTextAreaElement).value
      )
      .toContain('"kind": "Deployment"')

    await userEvent.click(screen.getByRole('button', { name: 'YAML' }))
    await expect
      .poll(
        () => (screen.getByRole('textbox').query() as HTMLTextAreaElement).value
      )
      .toContain('kind: Deployment')
  })

  it('uses the dark Monaco theme in the JSON editor on mobile', async () => {
    await page.viewport(390, 844)
    const { screen } = await renderCard(dep, params, 'dark')
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    await expect.element(screen.getByRole('textbox')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: 'JSON' }))
    await expect
      .poll(
        () =>
          (screen.getByRole('textbox').query() as HTMLTextAreaElement).value,
        { timeout: 3000 }
      )
      .toContain('"kind": "Deployment"')
    const editor = screen.getByRole('textbox').element() as HTMLTextAreaElement
    expect(editor.style.backgroundColor).toBe('rgb(30, 30, 30)')
    expect(editor.style.color).toBe('rgb(212, 212, 212)')
    expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
    await page.screenshot({
      path: '../../../../docs/visual-checks/yaml-json-editor-390-dark.png',
      element: screen
        .getByRole('heading', { name: 'YAML / JSON' })
        .element()
        .closest<HTMLElement>('[data-slot="card"]')!,
    })
  })

  it('applies a changed manifest only after a successful Dry Run', async () => {
    const { screen } = await renderCard()
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    // startEdit refetches the object — the buffer unlocks once it lands.
    const saveBtn = screen.getByRole('button', { name: '保存' })
    await expect.element(saveBtn).toBeInTheDocument()
    await vi.waitFor(() => expect(api.resource).toHaveBeenCalledOnce())
    await expect.element(screen.getByRole('textbox')).toBeInTheDocument()
    const buf = screen.getByRole('textbox')
    const yaml = (buf.query() as HTMLTextAreaElement).value.replace(
      'replicas: 2',
      'replicas: 5'
    )
    await userEvent.fill(buf, yaml)
    await userEvent.click(saveBtn)
    // diff dialog appears showing the change
    await expect.element(screen.getByText('确认变更')).toBeInTheDocument()
    const dialog = screen.getByRole('dialog')
    await expect.element(dialog.getByText(/replicas: 5/)).toBeInTheDocument()
    // 确认应用 must not exist before the server-side dry run passes.
    expect(screen.getByRole('button', { name: '确认应用' }).query()).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Dry Run 校验' }))
    await expect
      .element(screen.getByText(/Dry Run 校验通过/))
      .toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '确认应用' }))
    await vi.waitFor(() => {
      expect(api.update).toHaveBeenCalledTimes(2)
      // Call 1 is the dry run; call 2 is the real apply.
      expect(api.update.mock.calls[0][3]).toBe(true)
      const [name, p, manifest, dry] = api.update.mock.calls[1]
      expect(name).toBe('web')
      expect(p).toEqual(params)
      expect(dry).toBeFalsy()
      expect(manifest).toContain('replicas: 5')
      expect(manifest).toContain('resourceVersion:')
    })
  })

  it('blocks save on a YAML syntax error without calling the API', async () => {
    const { screen } = await renderCard()
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    await expect
      .element(screen.getByRole('button', { name: '保存' }))
      .toBeInTheDocument()
    await expect.element(screen.getByRole('textbox')).toBeInTheDocument()
    await userEvent.fill(screen.getByRole('textbox'), 'kind: [unclosed')
    await userEvent.click(screen.getByRole('button', { name: '保存' }))
    await expect
      .element(screen.getByText(/语法错误|解析失败/))
      .toBeInTheDocument()
    expect(api.update).not.toHaveBeenCalled()
  })

  it('blocks save when an identity field changed', async () => {
    const { screen } = await renderCard()
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    await expect
      .element(screen.getByRole('button', { name: '保存' }))
      .toBeInTheDocument()
    await expect.element(screen.getByRole('textbox')).toBeInTheDocument()
    const buf = screen.getByRole('textbox')
    const yaml = (buf.query() as HTMLTextAreaElement).value.replace(
      'name: web',
      'name: renamed'
    )
    await userEvent.fill(buf, yaml)
    await userEvent.click(screen.getByRole('button', { name: '保存' }))
    await expect
      .element(screen.getByText(/metadata\.name 不可修改/))
      .toBeInTheDocument()
    expect(screen.getByText('确认变更').query()).toBeNull()
    expect(api.update).not.toHaveBeenCalled()
  })

  it('skips the API call when nothing changed', async () => {
    const { screen } = await renderCard()
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    const saveBtn = screen.getByRole('button', { name: '保存' })
    await expect.element(saveBtn).toBeInTheDocument()
    await expect.element(screen.getByRole('textbox')).toBeInTheDocument()
    await userEvent.click(saveBtn)
    await vi.waitFor(() =>
      expect(screen.getByText('确认变更').query()).toBeNull()
    )
    expect(api.update).not.toHaveBeenCalled()
  })

  it('keeps the diff dialog open with a conflict hint on 409', async () => {
    // Dry run passes; the real apply loses the optimistic-concurrency race.
    api.update
      .mockResolvedValueOnce({ data: dep } as never)
      .mockRejectedValueOnce({
        response: {
          status: 409,
          data: { detail: 'the object has been modified' },
        },
      })
    const { screen } = await renderCard()
    await userEvent.click(screen.getByRole('button', { name: '编辑' }))
    const saveBtn = screen.getByRole('button', { name: '保存' })
    await expect.element(saveBtn).toBeInTheDocument()
    await expect.element(screen.getByRole('textbox')).toBeInTheDocument()
    const buf = screen.getByRole('textbox')
    const yaml = (buf.query() as HTMLTextAreaElement).value.replace(
      'replicas: 2',
      'replicas: 9'
    )
    await userEvent.fill(buf, yaml)
    await userEvent.click(saveBtn)
    await userEvent.click(screen.getByRole('button', { name: 'Dry Run 校验' }))
    await expect
      .element(screen.getByText(/Dry Run 校验通过/))
      .toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '确认应用' }))
    await expect
      .element(screen.getByText(/the object has been modified/))
      .toBeInTheDocument()
    await expect
      .element(screen.getByText(/对象已被他人修改/))
      .toBeInTheDocument()
    await expect.element(screen.getByText('确认变更')).toBeInTheDocument()
  })
})
