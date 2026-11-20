import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import '@/styles/index.css'
import { vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { k8sApi, type K8sObject, type K8sObjectParams } from '@/api/client'
import { DirectionProvider } from '@/context/direction-provider'
import { ThemeProvider } from '@/context/theme-provider'
import { DataResourceDialog } from '@/features/k8s/data-resource-dialog'

export const api = vi.mocked(k8sApi)
export const cmObj = (extra: Partial<K8sObject> = {}): K8sObject => ({
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: {
    name: 'app-config',
    namespace: 'prod',
    resourceVersion: '7',
    creationTimestamp: '2026-09-01T00:00:00Z',
    labels: { app: 'web' },
    annotations: { 'team/owner': 'infra' },
  },
  data: {
    'app.yaml': 'listen: 8080\ndebug: false',
    'banner.txt': 'hello kuboard',
  },
  ...extra,
})
export const b64 = (v: string) =>
  btoa(
    Array.from(new TextEncoder().encode(v), (n) => String.fromCharCode(n)).join(
      ''
    )
  )
export const secretObj = (
  data: Record<string, string> = { password: '原密码', username: 'admin' },
  extra: Partial<K8sObject> = {}
): K8sObject => ({
  apiVersion: 'v1',
  kind: 'Secret',
  type: 'Opaque',
  metadata: {
    name: 'db-credentials',
    namespace: 'prod',
    resourceVersion: '9',
    labels: { app: 'web' },
    annotations: { 'team/owner': 'infra' },
  },
  data: Object.fromEntries(Object.entries(data).map(([k, v]) => [k, b64(v)])),
  ...extra,
})
export function setupDataApi() {
  vi.clearAllMocks()
  api.resourceTypes.mockResolvedValue({
    data: ['configmaps', 'secrets'].map((resource) => ({
      group: '',
      version: 'v1',
      resource,
      kind: resource === 'secrets' ? 'Secret' : 'ConfigMap',
      namespaced: true,
      deletable: true,
      editable: resource !== 'secrets',
      data_editable: true,
    })),
  } as never)
  api.capabilities.mockResolvedValue({
    data: {
      resources: {
        '/v1/configmaps': {
          list: true,
          create: true,
          update: true,
          delete: true,
        },
        '/v1/secrets': { list: true, create: true, update: true, delete: true },
      },
      logs: false,
      exec: false,
    },
  } as never)
  api.update.mockResolvedValue({ data: {} } as never)
  api.updateData.mockResolvedValue({ data: {} } as never)
  api.remove.mockResolvedValue({ data: {} } as never)
}
export async function renderDataDialog(
  obj = cmObj(),
  initialMode: 'view' | 'edit' = 'view'
) {
  const params: K8sObjectParams = {
    cluster_id: 1,
    group: '',
    version: 'v1',
    resource: obj.kind === 'Secret' ? 'secrets' : 'configmaps',
    namespace: 'prod',
  }
  api.resource.mockResolvedValue({ data: obj } as never)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const onSaved = vi.fn(),
    onDeleted = vi.fn(),
    onEdit = vi.fn(),
    onOpenChange = vi.fn()
  const screen = await render(
    <QueryClientProvider client={client}>
      <DirectionProvider>
        <ThemeProvider>
          <DataResourceDialog
            open
            onOpenChange={onOpenChange}
            params={params}
            name={String(obj.metadata?.name)}
            obj={
              obj.kind === 'Secret'
                ? {
                    ...obj,
                    data: Object.fromEntries(
                      Object.keys(obj.data as object).map((key) => [key, '***'])
                    ),
                  }
                : obj
            }
            initialMode={initialMode}
            onSaved={onSaved}
            onDeleted={onDeleted}
            onEdit={onEdit}
          />
        </ThemeProvider>
      </DirectionProvider>
    </QueryClientProvider>
  )
  return { screen, params, client, onSaved, onDeleted, onEdit, onOpenChange }
}
