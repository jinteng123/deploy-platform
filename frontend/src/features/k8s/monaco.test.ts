import * as monaco from 'monaco-editor/editor/editor.api'
import { describe, expect, it } from 'vitest'
import { page } from 'vitest/browser'
import '@/features/k8s/monaco'

describe('local Monaco bundle', () => {
  it('loads YAML by default and registers JSON language support on demand', async () => {
    const initialLanguageIds = monaco.languages
      .getLanguages()
      .map(({ id }) => id)

    expect(initialLanguageIds).toContain('yaml')
    expect(initialLanguageIds).not.toContain('json')
    expect(initialLanguageIds).not.toContain('html')
    expect(initialLanguageIds).not.toContain('css')
    expect(initialLanguageIds).not.toContain('typescript')

    await import('monaco-editor/languages/features/json/register')

    expect(monaco.languages.getLanguages().map(({ id }) => id)).toContain(
      'json'
    )
  })

  it('renders the real JSON editor with the dark palette on a narrow viewport', async () => {
    await page.viewport(390, 844)
    await import('monaco-editor/languages/features/json/register')
    await import('monaco-editor/languages/features/json/jsonMode')

    const host = document.createElement('div')
    host.style.width = '350px'
    host.style.height = '300px'
    host.style.overflow = 'hidden'
    host.style.border = '1px solid #334155'
    host.style.borderRadius = '8px'
    document.body.append(host)

    const editor = monaco.editor.create(host, {
      value: '{\n  "kind": "Deployment",\n  "replicas": 2\n}',
      language: 'json',
      theme: 'vs-dark',
      minimap: { enabled: false },
      automaticLayout: true,
    })

    try {
      const surface = host.querySelector('.monaco-editor-background')
      expect(surface).not.toBeNull()
      expect(getComputedStyle(surface!).backgroundColor).toBe('rgb(30, 30, 30)')
      expect(host.querySelector('.monaco-editor')).not.toBeNull()
      expect(document.documentElement.scrollWidth).toBeLessThanOrEqual(390)
      await page.screenshot({
        path: '../../../../docs/visual-checks/monaco-json-390-dark.png',
        element: host,
      })
    } finally {
      editor.dispose()
      host.remove()
    }
  })
})
