import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, RefreshCw, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import {
  capsKey,
  k8sApi,
  platformApi,
  type K8sObject,
  type K8sObjectParams,
} from '@/api/client'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { EmptyState } from '@/components/empty-state'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { ThemeSwitch } from '@/components/theme-switch'
import {
  joinTypes,
  resourceInfo,
  sectionOf,
  type K8sSectionId,
} from './catalog'
import { ClusterListPage } from './cluster-list-page'
import { ClusterOverview } from './cluster-overview'
import { CmCreateDialog } from './cm-create-dialog'
import { DataDetailDialog } from './data-detail-dialog'
import { errDetail, isConflict } from './errors'
import { NamespaceHomePage } from './namespace-home-page'
import { NamespacePickerDialog } from './namespace-picker-page'
import { PodDetailDrawer } from './pod-detail-drawer'
import type { PodConsoleQuery } from './pod-terminal-page'
import { PvcCreateDialog } from './pvc-create-dialog'
import { DeleteDialog, type ActionError } from './resource-actions'
import { SecretCreateDialog } from './secret-create-dialog'
import { useCapabilities } from './use-capabilities'

export const ALL_NAMESPACES = '*'
// The overview section's default sub-tab is the cluster dashboard —
// a synthetic 'dashboard' resource id that never reaches the API.
const DASHBOARD_RES = 'dashboard'
const NAMESPACE_HOME_RES = 'namespace-home'
// Cluster-scoped storage kinds share one tab group; PVC stands alone.
const CLUSTER_STORAGE_RES = ['persistentvolumes', 'storageclasses']

// Create-dialog dispatch: a resource shows a create button only when it
// is in this map (has a dialog), the allowlist marks it creatable, and
// the RBAC probe reports create — fail closed on every leg.
const CREATE_LABELS: Record<string, string> = {
  persistentvolumeclaims: '创建存储声明',
  configmaps: '创建配置字典',
  secrets: '创建密钥',
}

// The two kv-map kinds get the Kuboard treatment on the list page:
// in-place detail dialog, row checkboxes for batch delete, an inline
// 操作 column, and the metadata.name field selector.
const DATA_LIST_RESOURCES = new Set(['configmaps', 'secrets'])
const dataNoun = (res: string) => (res === 'secrets' ? '密钥' : '配置字典')
// Stable per-row key for selection state — uid first, ns/name fallback.
const rowKey = (o: K8sObject) =>
  o.metadata?.uid ??
  `${o.metadata?.namespace ?? ''}/${o.metadata?.name ?? ''}`

// K8sSearch is the URL search state of /k8s: every selection lives in the
// address bar so a refresh or a shared link restores the exact view.
export type K8sSearch = {
  cluster?: number
  ns?: string
  section?: K8sSectionId
  res?: string
  // pod=<ns>/<name> keeps the pod detail drawer in the URL so refresh
  // and browser back/forward restore it like every other selection.
  pod?: string
}

// K8sDetailQuery identifies one object on /k8s/detail.
export type K8sDetailQuery = {
  cluster: number
  g: string
  v: string
  r: string
  ns?: string
  name: string
  // Entry point that spawned this detail view (e.g. 'ns-home') — lets
  // the back button return to the real origin instead of always the
  // resource list.
  from?: string
}

type Props = {
  search: K8sSearch
  onSearch: (patch: Partial<K8sSearch>, opts?: { replace?: boolean }) => void
  onOpenDetail: (q: K8sDetailQuery) => void
  onOpenClusterManagement?: () => void
  onOpenTerminal?: (q: PodConsoleQuery) => void
}

const detail = (err: unknown): string =>
  (err as { response?: { data?: { detail?: string } } })?.response?.data
    ?.detail ?? (err instanceof Error ? err.message : '请求失败')

export function K8sPage({
  search,
  onSearch,
  onOpenDetail,
  onOpenClusterManagement,
  onOpenTerminal,
}: Props) {
  const [filter, setFilter] = useState('')
  const [labelSel, setLabelSel] = useState('')
  // Field selector (metadata.name exact match) — only wired into the
  // list request for the two data kinds.
  const [fieldSel, setFieldSel] = useState('')
  // ConfigMap/Secret rows open the Kuboard-style detail dialog in place
  // instead of navigating to /k8s/detail — the list response already
  // carries the full object (data included).
  const [detailObj, setDetailObj] = useState<K8sObject | null>(null)
  const [deleteObj, setDeleteObj] = useState<K8sObject | null>(null)
  const [deleteErr, setDeleteErr] = useState<ActionError>(null)
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set())
  const [batchOpen, setBatchOpen] = useState(false)
  const [batchPending, setBatchPending] = useState(false)
  const [batchErr, setBatchErr] = useState('')
  // Pod rows open the detail drawer in place (list context preserved);
  // every other resource still navigates to /k8s/detail.
  const queryClient = useQueryClient()
  const clusters = useQuery({
    queryKey: ['clusters'],
    queryFn: () => platformApi.clusters(),
  })
  const types = useQuery({
    queryKey: ['k8s', 'resource-types'],
    queryFn: () => k8sApi.resourceTypes(),
  })

  const clusterList = clusters.data?.data ?? []
  // The bare /k8s route is the cluster landing page. A cluster is selected
  // only after the user clicks a card (or supplies ?cluster= in a shared URL).
  const isClusterHome = search.cluster == null
  const cluster = isClusterHome
    ? undefined
    : (clusterList.find((c) => c.id === search.cluster) ??
      clusterList.find((c) => c.enabled) ??
      clusterList[0])
  const clusterId = cluster?.id
  const isNamespaceHome =
    !isClusterHome && search.ns != null && search.res === NAMESPACE_HOME_RES

  const typeMap = useMemo(() => joinTypes(types.data?.data), [types.data])
  const section = sectionOf(search.section)
  const isOverviewSection = section.id === 'overview'
  // Overview defaults to the dashboard; other sections default to their
  // first resource. Explicit res values are validated against the
  // section's allowlist so a stale/bogus ?res= falls back safely.
  const res = isNamespaceHome
    ? NAMESPACE_HOME_RES
    : isOverviewSection
      ? search.res === DASHBOARD_RES || !search.res
        ? DASHBOARD_RES
        : section.resources.includes(search.res)
          ? search.res
          : DASHBOARD_RES
      : section.resources.includes(search.res ?? '')
        ? (search.res as string)
        : section.resources[0]
  const isDashboard = res === DASHBOARD_RES
  // Kuboard list-page treatment applies to the two kv-map kinds only.
  const isDataList = DATA_LIST_RESOURCES.has(res)
  const info = resourceInfo(isDashboard ? 'namespaces' : res)
  const resType = typeMap.get(res)
  const namespaced = isDashboard ? false : (resType?.namespaced ?? true)
  // Tab bars only group same-scope siblings: storage splits PVC (ns)
  // from PV/SC (cluster); overview's dashboard/namespaces/nodes/events
  // are heterogeneous, so each renders standalone and the sidebar does
  // the switching. Workloads/network keep their resource tabs.
  const resTabs =
    section.id === 'overview'
      ? [res]
      : section.id === 'storage'
        ? CLUSTER_STORAGE_RES.includes(res)
          ? CLUSTER_STORAGE_RES
          : [res]
        : section.resources
  // The namespace picker only gates namespace-scoped views: an explicit
  // cluster-scoped ?res= (nodes/namespaces/PV/storageclasses) or the
  // dashboard renders without a namespace — that's what the sidebar's
  // cluster-level entries link to. A bare ?cluster= still opens it.
  const isNamespaceChooser =
    !isClusterHome && search.ns == null && (search.res == null || namespaced)

  // Namespace list feeds the selector; it's a cluster-scoped read itself.
  const namespaces = useQuery({
    queryKey: ['k8s', 'resources', clusterId, '', 'v1', 'namespaces', ''],
    queryFn: () =>
      k8sApi.resources({
        cluster_id: clusterId as number,
        version: 'v1',
        resource: 'namespaces',
      }),
    enabled: clusterId != null && !isClusterHome,
  })
  const nsNames = (namespaces.data?.data.items ?? [])
    .map((n) => n.metadata?.name ?? '')
    .filter(Boolean)
    .sort()

  const allNs = search.ns === ALL_NAMESPACES
  const effectiveNs = namespaced
    ? allNs
      ? ''
      : (search.ns ?? cluster?.default_namespace ?? 'default')
    : ''

  // The pod drawer lives in the ?pod=ns/name param — refresh and
  // browser back/forward restore it; only meaningful on the pods list.
  const [podNs, podName] = search.pod?.split('/') ?? []
  const drawer: K8sDetailQuery | null =
    res === 'pods' && podName && clusterId != null
      ? {
          cluster: clusterId,
          g: '',
          v: 'v1',
          r: 'pods',
          ns: podNs || undefined,
          name: podName,
        }
      : null

  // The query key embeds cluster + g/v/r + namespace, so switching any of
  // them produces a fresh fetch and previous data is never rendered.
  const list = useQuery({
    queryKey: [
      'k8s',
      'resources',
      clusterId,
      resType?.group,
      resType?.version,
      res,
      allNs ? ALL_NAMESPACES : effectiveNs,
      labelSel,
      // fieldSel only applies to the data kinds — isDataList is part of
      // the key so a resource switch can't leak a stale selector.
      isDataList,
      fieldSel,
    ],
    queryFn: () =>
      k8sApi.resources({
        cluster_id: clusterId as number,
        group: resType?.group,
        version: resType?.version ?? 'v1',
        resource: res,
        namespace: allNs ? undefined : effectiveNs || undefined,
        all_namespaces: allNs || undefined,
        label_selector: labelSel || undefined,
        // Field selector is only ever a metadata.name exact match.
        field_selector:
          isDataList && fieldSel.trim()
            ? `metadata.name=${fieldSel.trim()}`
            : undefined,
      }),
    enabled:
      clusterId != null &&
      !isClusterHome &&
      !isNamespaceChooser &&
      resType != null &&
      !isDashboard,
  })

  const overview = useQuery({
    queryKey: ['k8s', 'overview', clusterId],
    queryFn: () => k8sApi.overview(clusterId as number),
    enabled:
      clusterId != null && !isClusterHome && !isNamespaceChooser && isDashboard,
  })

  // RBAC verdict for the create-dialog gate — one SelfSubjectRulesReview
  // shared with every other capability-gated control on the page.
  const caps = useCapabilities(clusterId, effectiveNs || undefined)
  const createLabel = CREATE_LABELS[res]
  const canCreate =
    createLabel != null &&
    resType?.creatable === true &&
    caps.data?.resources[
      capsKey({
        group: resType?.group,
        version: resType?.version ?? 'v1',
        resource: res,
      })
    ]?.create === true
  const [createOpen, setCreateOpen] = useState(false)

  const items = (list.data?.data.items ?? []).filter((o) =>
    filter ? (o.metadata?.name ?? '').includes(filter) : true
  )
  const isRefreshing = isDashboard ? overview.isFetching : list.isFetching
  const refresh = () => {
    void queryClient.invalidateQueries({ queryKey: ['k8s'] })
  }

  // Row selection and in-place dialogs only make sense for the list they
  // were opened from — switching cluster/resource/namespace drops them
  // (render-phase reset, same prev-compare pattern the dialogs use).
  const viewKey = `${clusterId ?? ''}/${res}/${namespaced ? (allNs ? ALL_NAMESPACES : effectiveNs) : ''}`
  const [prevViewKey, setPrevViewKey] = useState(viewKey)
  if (viewKey !== prevViewKey) {
    setPrevViewKey(viewKey)
    setSelected(new Set())
    setDetailObj(null)
    setDeleteObj(null)
    setDeleteErr(null)
    setBatchOpen(false)
    setBatchErr('')
    setFieldSel('')
  }

  // Shared per-object params for the data-list dialogs and deletes.
  const objParams = (o: K8sObject): K8sObjectParams => ({
    cluster_id: clusterId as number,
    group: resType?.group,
    version: resType?.version ?? 'v1',
    resource: res,
    namespace: namespaced
      ? o.metadata?.namespace || effectiveNs || undefined
      : undefined,
  })

  // The row's own delete gate mirrors ResourceActions: allowlist
  // deletable AND this user's RBAC verdict.
  const canDeleteRows =
    isDataList &&
    resType?.deletable === true &&
    caps.data?.resources[
      capsKey({
        group: resType?.group,
        version: resType?.version ?? 'v1',
        resource: res,
      })
    ]?.delete === true

  const removeOne = useMutation({
    mutationFn: (o: K8sObject) =>
      k8sApi.remove(
        o.metadata?.name ?? '',
        objParams(o),
        o.metadata?.resourceVersion as string | undefined
      ),
    onSuccess: (_d, o) => {
      toast.success(`${dataNoun(res)} ${o.metadata?.name ?? ''} 已删除`)
      setDeleteObj(null)
      setDeleteErr(null)
      refresh()
    },
    onError: (e) =>
      setDeleteErr({ text: errDetail(e), conflict: isConflict(e) }),
  })

  // Batch delete runs the removals sequentially — a failure reports and
  // keeps the dialog open while successful deletions still refresh.
  const batchDelete = async () => {
    const targets = items.filter((o) => selected.has(rowKey(o)))
    setBatchPending(true)
    setBatchErr('')
    let done = 0
    let lastErr = ''
    for (const o of targets) {
      try {
        await k8sApi.remove(
          o.metadata?.name ?? '',
          objParams(o),
          o.metadata?.resourceVersion as string | undefined
        )
        done++
      } catch (e) {
        lastErr = errDetail(e)
      }
    }
    setBatchPending(false)
    if (done > 0) {
      toast.success(`已删除 ${done} 个`)
      refresh()
    }
    if (lastErr === '') {
      setBatchOpen(false)
      setSelected(new Set())
    } else {
      setBatchErr(lastErr)
    }
  }

  const selectedObjs = items.filter((o) => selected.has(rowKey(o)))
  const allChecked = items.length > 0 && selectedObjs.length === items.length
  const someChecked = selectedObjs.length > 0

  const openDetail = (o: K8sObject) => {
    if (!clusterId || !resType) return
    if (isDataList) {
      // ConfigMap/Secret rows open the Kuboard-style detail dialog in
      // place — no navigation, list context preserved.
      setDetailObj(o)
      return
    }
    const q: K8sDetailQuery = {
      cluster: clusterId,
      g: resType.group,
      v: resType.version,
      r: res,
      ns: namespaced ? o.metadata?.namespace || effectiveNs : undefined,
      name: o.metadata?.name ?? '',
    }
    if (res === 'namespaces') {
      // A namespace row is the entry point into that namespace's
      // workbench — the generic object detail adds nothing here.
      onSearch(
        {
          ns: q.name,
          section: 'overview',
          res: NAMESPACE_HOME_RES,
          pod: undefined,
        },
        { replace: false }
      )
    } else if (res === 'pods') {
      // Push a history entry so browser Back closes the drawer like a
      // modal; closing the drawer itself replaces instead.
      onSearch({ pod: `${q.ns ?? ''}/${q.name}` }, { replace: false })
    } else {
      onOpenDetail(q)
    }
  }

  if (isClusterHome) {
    return (
      <>
        <Header fixed>
          <div className='me-auto flex items-center gap-2 text-sm'>
            <span className='text-[#6f88ac]'>首页</span>
            <span className='text-[#b0bfd4]'>/</span>
            <span className='font-semibold text-[#214b8d]'>K8s 控制台</span>
          </div>
          <ThemeSwitch />
        </Header>
        <ClusterListPage
          clusters={clusterList}
          loading={clusters.isPending}
          error={clusters.isError ? detail(clusters.error) : undefined}
          onOpen={(id) =>
            onSearch({
              cluster: id,
              ns: undefined,
              section: undefined,
              res: undefined,
              pod: undefined,
            })
          }
          onRefresh={() => {
            void queryClient.invalidateQueries({ queryKey: ['clusters'] })
            void queryClient.invalidateQueries({
              queryKey: ['k8s', 'overview'],
            })
          }}
          onAdd={onOpenClusterManagement}
        />
      </>
    )
  }

  if (isNamespaceChooser && cluster) {
    return (
      <>
        <Header fixed>
          <div className='me-auto flex items-center gap-2 text-sm'>
            <span className='text-[#6f88ac]'>首页</span>
            <span className='text-[#b0bfd4]'>/</span>
            <span className='font-semibold text-[#214b8d]'>K8s 控制台</span>
          </div>
          <ThemeSwitch />
        </Header>
        <ClusterListPage
          clusters={clusterList}
          loading={clusters.isPending}
          error={clusters.isError ? detail(clusters.error) : undefined}
          onOpen={(id) =>
            onSearch({ cluster: id, ns: undefined, pod: undefined })
          }
          onRefresh={() => {
            void queryClient.invalidateQueries({ queryKey: ['clusters'] })
            void queryClient.invalidateQueries({
              queryKey: ['k8s', 'overview'],
            })
          }}
          onAdd={onOpenClusterManagement}
        />
        <NamespacePickerDialog
          cluster={cluster}
          items={namespaces.data?.data.items ?? []}
          loading={namespaces.isPending}
          error={namespaces.isError ? detail(namespaces.error) : undefined}
          onSelect={(namespace) =>
            onSearch({
              ns: namespace,
              section: 'overview',
              res: NAMESPACE_HOME_RES,
              pod: undefined,
            })
          }
          onClose={() =>
            onSearch({
              cluster: undefined,
              ns: undefined,
              section: undefined,
              res: undefined,
              pod: undefined,
            })
          }
        />
      </>
    )
  }

  if (isNamespaceHome && cluster && search.ns) {
    return (
      <>
        <Header fixed>
          <div className='me-auto flex items-center gap-2 text-sm'>
            <span className='text-[#6f88ac]'>首页</span>
            <span className='text-[#b0bfd4]'>/</span>
            <span className='font-semibold text-[#214b8d]'>K8s 控制台</span>
          </div>
          <ThemeSwitch />
        </Header>
        <Main>
          <div className='mb-5 flex flex-wrap items-end justify-between gap-4 rounded-2xl border border-[#e0e8f4] bg-white p-4 shadow-[0_8px_30px_rgba(37,76,132,0.04)]'>
            <div className='flex items-center gap-2 text-sm text-[#587092]'>
              <span>集群</span>
              <span className='font-semibold text-[#214b8d]'>
                {cluster.name}
              </span>
              <span className='text-[#b0bfd4]'>/</span>
              <span>名称空间</span>
              <span className='font-semibold text-[#214b8d]'>{search.ns}</span>
            </div>
            <div className='flex flex-wrap items-center gap-2'>
              <Select
                value={String(clusterId)}
                onValueChange={(value) =>
                  onSearch({
                    cluster: Number(value),
                    ns: undefined,
                    section: undefined,
                    res: undefined,
                    pod: undefined,
                  })
                }
              >
                <SelectTrigger className='h-9 w-52 rounded-lg border-[#d6e1f0] bg-[#fbfdff] text-sm'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {clusterList.map((item) => (
                    <SelectItem key={item.id} value={String(item.id)}>
                      {item.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Select
                value={search.ns}
                onValueChange={(value) =>
                  onSearch({ ns: value, pod: undefined })
                }
              >
                <SelectTrigger className='h-9 w-44 rounded-lg border-[#d6e1f0] bg-[#fbfdff] text-sm'>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {nsNames.map((name) => (
                    <SelectItem key={name} value={name}>
                      {name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <NamespaceHomePage
            clusterId={clusterId as number}
            namespace={search.ns}
            onOpenDetail={onOpenDetail}
            onOpenResource={(resource) =>
              onSearch({ section: 'workloads', res: resource, pod: undefined })
            }
          />
        </Main>
      </>
    )
  }

  return (
    <>
      <Header fixed>
        <div className='me-auto flex items-center gap-2 text-sm'>
          <span className='text-[#6f88ac]'>首页</span>
          <span className='text-[#b0bfd4]'>/</span>
          <span className='font-semibold text-[#214b8d]'>K8s 控制台</span>
        </div>
        <ThemeSwitch />
      </Header>
      <Main>
        <div className='mb-5'>
          <h1 className='text-2xl font-bold tracking-tight text-[#1c355e]'>
            Kubernetes 控制台
          </h1>
          <p className='mt-1 text-sm text-muted-foreground'>
            按集群、命名空间和资源类型查看运行状态。
          </p>
        </div>
        <div className='mb-5 flex flex-wrap items-end gap-4 rounded-2xl border border-[#e0e8f4] bg-white p-4 shadow-[0_8px_30px_rgba(37,76,132,0.04)]'>
          <div className='space-y-1.5'>
            <div className='text-xs font-semibold tracking-wide text-[#587092]'>
              集群
            </div>
            <Select
              value={clusterId != null ? String(clusterId) : ''}
              onValueChange={(v) =>
                onSearch({ cluster: Number(v), ns: undefined, pod: undefined })
              }
            >
              <SelectTrigger className='w-56 rounded-lg border-[#d6e1f0] bg-[#fbfdff]'>
                <SelectValue
                  placeholder={clusters.isPending ? '加载中…' : '选择集群'}
                />
              </SelectTrigger>
              <SelectContent>
                {clusterList.map((c) => (
                  <SelectItem key={c.id} value={String(c.id)}>
                    {c.name}
                    {c.enabled ? '' : '（停用）'}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className='space-y-1.5'>
            <div className='text-xs font-semibold tracking-wide text-[#587092]'>
              命名空间
            </div>
            <Select
              value={namespaced ? (allNs ? ALL_NAMESPACES : effectiveNs) : ''}
              onValueChange={(v) => onSearch({ ns: v, pod: undefined })}
              disabled={!namespaced}
            >
              <SelectTrigger className='w-56 rounded-lg border-[#d6e1f0] bg-[#fbfdff]'>
                <SelectValue
                  placeholder={namespaced ? '选择命名空间' : '集群级资源'}
                />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL_NAMESPACES}>全部命名空间</SelectItem>
                {nsNames.map((n) => (
                  <SelectItem key={n} value={n}>
                    {n}
                  </SelectItem>
                ))}
                {namespaced &&
                  !allNs &&
                  effectiveNs &&
                  !nsNames.includes(effectiveNs) && (
                    <SelectItem value={effectiveNs}>{effectiveNs}</SelectItem>
                  )}
              </SelectContent>
            </Select>
          </div>
          {isDashboard && (
            <Button
              variant='outline'
              size='sm'
              className='ms-auto rounded-lg border-[#d6e1f0] bg-[#fbfdff]'
              onClick={refresh}
              disabled={isRefreshing}
            >
              <RefreshCw
                className={isRefreshing ? 'size-4 animate-spin' : 'size-4'}
              />
              刷新概览
            </Button>
          )}
        </div>

        {resTabs.length > 1 && (
          <Tabs
            value={res}
            onValueChange={(v) => onSearch({ res: v, pod: undefined })}
            className='mt-3'
          >
            <TabsList className='gap-1 rounded-xl bg-[#e8eff9] p-1'>
              {resTabs.map((r) => (
                <TabsTrigger
                  key={r}
                  value={r}
                  disabled={!typeMap.has(r)}
                  className='rounded-lg px-3 text-[#587092] data-[state=active]:bg-white data-[state=active]:text-[#24549a]'
                >
                  {resourceInfo(r).title}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
        )}

        {isDashboard ? (
          overview.isError ? (
            <Card className='mt-4 rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.06)]'>
              <CardContent>
                <p className='py-8 text-center text-sm text-destructive'>
                  {detail(overview.error)}
                </p>
              </CardContent>
            </Card>
          ) : overview.isPending ? (
            <Card className='mt-4 rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.06)]'>
              <CardContent>
                <p className='py-8 text-center text-sm text-muted-foreground'>
                  加载中…
                </p>
              </CardContent>
            </Card>
          ) : (
            <ClusterOverview data={overview.data.data} />
          )
        ) : (
          <Card className='mt-4 rounded-2xl border-[#e0e8f4] shadow-[0_8px_30px_rgba(37,76,132,0.06)]'>
            <CardHeader className='flex flex-row items-center justify-between gap-4 space-y-0'>
              <div>
                <CardTitle className='text-base text-[#1c355e]'>
                  {info.title}
                  {namespaced && (
                    <span className='ms-2 text-sm font-normal text-muted-foreground'>
                      {allNs ? '全部命名空间' : effectiveNs}
                    </span>
                  )}
                </CardTitle>
                <p className='mt-1 text-xs text-muted-foreground'>
                  共 {items.length} 项资源
                </p>
              </div>
              <div className='flex flex-wrap items-center gap-2'>
                {canCreate && createLabel && (
                  <Button
                    size='sm'
                    className='h-8 rounded-lg bg-[#234883] hover:bg-[#193d72]'
                    disabled={allNs}
                    title={allNs ? '请先选择具体命名空间' : undefined}
                    onClick={() => setCreateOpen(true)}
                  >
                    <Plus className='size-4' />
                    {createLabel}
                  </Button>
                )}
                {isDataList && canDeleteRows && someChecked && (
                  <Button
                    size='sm'
                    className='h-8 rounded-lg bg-[#c2404e] text-white hover:bg-[#a52f3d]'
                    onClick={() => {
                      setBatchErr('')
                      setBatchOpen(true)
                    }}
                  >
                    <Trash2 className='size-4' />
                    删除 ({selected.size})
                  </Button>
                )}
                {!isDashboard && isDataList && (
                  <div className='flex h-9 items-center gap-1.5'>
                    <span className='text-xs whitespace-nowrap text-[#587092]'>
                      字段选择器
                    </span>
                    <span className='inline-flex items-center rounded-md border border-[#d6e1f0] bg-[#eef3fa] px-1.5 py-0.5 font-mono text-xs text-[#31598e]'>
                      metadata.name
                    </span>
                    <span className='text-xs text-[#8ba3c7]'>=</span>
                    <Input
                      aria-label='字段选择器 metadata.name'
                      className='h-9 w-40 rounded-lg border-[#d6e1f0] bg-[#fbfdff]'
                      placeholder='精确匹配名称'
                      value={fieldSel}
                      onChange={(e) => setFieldSel(e.target.value)}
                    />
                  </div>
                )}
                {!isDashboard && (
                  <>
                    <Input
                      className='w-48 rounded-lg border-[#d6e1f0] bg-[#fbfdff]'
                      placeholder='标签选择器，如 app=web'
                      title='Kubernetes 标签选择器语法（k=v,k2=v2）'
                      value={labelSel}
                      onChange={(e) => setLabelSel(e.target.value)}
                    />
                    <Input
                      className='w-48 rounded-lg border-[#d6e1f0] bg-[#fbfdff]'
                      placeholder='按名称过滤…'
                      value={filter}
                      onChange={(e) => setFilter(e.target.value)}
                    />
                  </>
                )}
                <Button
                  variant='outline'
                  size='sm'
                  className='rounded-lg border-[#d6e1f0] bg-[#fbfdff]'
                  onClick={refresh}
                  disabled={isRefreshing}
                >
                  <RefreshCw
                    className={isRefreshing ? 'size-4 animate-spin' : 'size-4'}
                  />
                  刷新
                </Button>
              </div>
            </CardHeader>
            <CardContent className='overflow-x-auto'>
              {list.isError ? (
                <p className='py-8 text-center text-sm text-destructive'>
                  {detail(list.error)}
                </p>
              ) : list.isPending ? (
                <div className='space-y-3 py-2'>
                  {Array.from({ length: 5 }).map((_, i) => (
                    <Skeleton key={i} className='h-9 w-full' />
                  ))}
                </div>
              ) : (
                <table className='w-full text-sm'>
                  <thead>
                    <tr className='border-b bg-[#f7f9fc]'>
                      {isDataList && (
                        <th className='w-10 px-3 py-2'>
                          <Checkbox
                            aria-label='全选'
                            checked={
                              allChecked
                                ? true
                                : someChecked
                                  ? 'indeterminate'
                                  : false
                            }
                            onCheckedChange={(v) =>
                              setSelected(
                                v === true
                                  ? new Set(items.map(rowKey))
                                  : new Set()
                              )
                            }
                          />
                        </th>
                      )}
                      {info.columns.map((c) => (
                        <th
                          key={c.key}
                          className={cn(
                            'px-3 py-2 text-xs font-semibold whitespace-nowrap text-[#587092]',
                            c.numeric ? 'text-right' : 'text-left'
                          )}
                        >
                          {c.title}
                        </th>
                      ))}
                      {isDataList && (
                        <th className='px-3 py-2 text-right text-xs font-semibold whitespace-nowrap text-[#587092]'>
                          操作
                        </th>
                      )}
                    </tr>
                  </thead>
                  <tbody>
                    {items.map((o) => (
                      <tr
                        key={rowKey(o)}
                        className='cursor-pointer border-b border-[#edf1f7] last:border-0 hover:bg-[#f7faff]'
                        onClick={() => openDetail(o)}
                      >
                        {isDataList && (
                          <td
                            className='px-3 py-3'
                            onClick={(e) => e.stopPropagation()}
                          >
                            <Checkbox
                              aria-label={`选择 ${o.metadata?.name ?? ''}`}
                              checked={selected.has(rowKey(o))}
                              onCheckedChange={(v) =>
                                setSelected((s) => {
                                  const next = new Set(s)
                                  if (v === true) next.add(rowKey(o))
                                  else next.delete(rowKey(o))
                                  return next
                                })
                              }
                            />
                          </td>
                        )}
                        {info.columns.map((c) => (
                          <td
                            key={c.key}
                            className={cn(
                              'px-3 py-3 whitespace-nowrap',
                              c.numeric && 'text-right tabular-nums'
                            )}
                          >
                            {c.cell(o)}
                          </td>
                        ))}
                        {isDataList && (
                          <td className='px-3 py-3 text-right whitespace-nowrap'>
                            <button
                              type='button'
                              className='text-xs font-medium text-[#31598e] transition hover:text-[#234883] focus-visible:ring-2 focus-visible:ring-[#234883]/40 focus-visible:outline-none'
                              onClick={(e) => {
                                e.stopPropagation()
                                setDetailObj(o)
                              }}
                            >
                              查看
                            </button>
                            {canDeleteRows && (
                              <button
                                type='button'
                                className='ms-3 text-xs font-medium text-[#c2404e] transition hover:text-[#a52f3d] focus-visible:ring-2 focus-visible:ring-[#c2404e]/40 focus-visible:outline-none'
                                onClick={(e) => {
                                  e.stopPropagation()
                                  setDeleteErr(null)
                                  setDeleteObj(o)
                                }}
                              >
                                删除
                              </button>
                            )}
                          </td>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {!list.isPending &&
                !list.isError &&
                items.length === 0 &&
                (filter ? (
                  <EmptyState text={`没有匹配「${filter}」的资源`} />
                ) : (
                  <EmptyState />
                ))}
            </CardContent>
          </Card>
        )}
      </Main>
      <PodDetailDrawer
        query={drawer}
        typeMap={typeMap}
        onClose={() => onSearch({ pod: undefined })}
        onOpenOwner={(q) => onOpenDetail(q)}
        onOpenTerminal={(q) =>
          onOpenTerminal?.({ ...q, section: section.id, res })
        }
      />
      {createOpen &&
        clusterId != null &&
        effectiveNs &&
        res === 'persistentvolumeclaims' && (
          <PvcCreateDialog
            open={createOpen}
            onOpenChange={setCreateOpen}
            clusterId={clusterId}
            namespace={effectiveNs}
            onCreated={refresh}
          />
        )}
      {createOpen &&
        clusterId != null &&
        effectiveNs &&
        res === 'configmaps' && (
          <CmCreateDialog
            open={createOpen}
            onOpenChange={setCreateOpen}
            clusterId={clusterId}
            namespace={effectiveNs}
            onCreated={refresh}
          />
        )}
      {createOpen && clusterId != null && effectiveNs && res === 'secrets' && (
        <SecretCreateDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          clusterId={clusterId}
          namespace={effectiveNs}
          onCreated={refresh}
        />
      )}
      {detailObj != null && clusterId != null && resType != null && (
        <DataDetailDialog
          open
          onOpenChange={(o) => {
            if (!o) setDetailObj(null)
          }}
          params={objParams(detailObj)}
          name={detailObj.metadata?.name ?? ''}
          obj={detailObj}
          kindLabel={res === 'secrets' ? 'Secret' : 'ConfigMap'}
          onSaved={refresh}
          onDeleted={() => {
            setDetailObj(null)
            refresh()
          }}
        />
      )}
      <DeleteDialog
        open={deleteObj != null}
        onOpenChange={(o) => {
          if (!o) setDeleteObj(null)
        }}
        resource={dataNoun(res)}
        name={deleteObj?.metadata?.name ?? ''}
        pending={removeOne.isPending}
        error={deleteErr}
        onConfirm={() => {
          if (deleteObj) removeOne.mutate(deleteObj)
        }}
      />
      <ConfirmDialog
        open={batchOpen}
        onOpenChange={setBatchOpen}
        title={`删除 ${selectedObjs.length} 个对象`}
        icon={
          <span className='flex size-9 shrink-0 items-center justify-center rounded-lg bg-[#fdf0f1] text-[#c2404e]'>
            <Trash2 className='size-4' />
          </span>
        }
        desc={
          <>
            此操作不可撤销。将删除以下 {dataNoun(res)}：
            <span className='mt-2 flex flex-wrap gap-1.5'>
              {selectedObjs.slice(0, 5).map((o) => (
                <span
                  key={rowKey(o)}
                  className='inline-flex items-center rounded-md border border-[#d6e1f0] bg-[#eef3fa] px-1.5 py-0.5 font-mono text-xs text-[#31598e]'
                >
                  {o.metadata?.name}
                </span>
              ))}
              {selectedObjs.length > 5 && (
                <span className='text-xs text-[#8ba3c7]'>
                  等 {selectedObjs.length} 个
                </span>
              )}
            </span>
          </>
        }
        destructive
        confirmText='删除'
        cancelBtnText='取消'
        isLoading={batchPending}
        handleConfirm={() => void batchDelete()}
      >
        {batchErr !== '' && (
          <p
            role='alert'
            className='flex items-start gap-1.5 rounded-lg bg-[#fdf0f1] px-3 py-2 text-xs leading-5 text-[#a52f3d]'
          >
            {batchErr}
          </p>
        )}
      </ConfirmDialog>
    </>
  )
}
