import { lazy, Suspense } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArrowLeft } from 'lucide-react'
import { k8sApi } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Header } from '@/components/layout/header'
import { Main } from '@/components/layout/main'
import { Skeleton } from '@/components/ui/skeleton'
import { ThemeSwitch } from '@/components/theme-switch'
import { errDetail } from './errors'
import type { K8sSectionId } from './catalog'
import { podContainers } from './pod-containers'
import { useCapabilities } from './use-capabilities'

// xterm is heavy — the terminal panel stays lazily loaded so this route
// doesn't pull it into the console bundle.
const TerminalPanel = lazy(() =>
  import('./pod-console').then((m) => ({ default: m.TerminalPanel }))
)

// PodConsoleQuery identifies a terminal session target. The optional
// g/v/r/name fields remember the originating detail page so onBack can
// return exactly where the user came from.
export type PodConsoleQuery = {
  cluster: number
  ns?: string
  pod: string
  container?: string
  command?: string
  g?: string
  v?: string
  r?: string
  name?: string
  // /k8s list context to return to when no detail origin (g/v/r/name)
  // is present — e.g. the pod drawer opened from the pods section.
  section?: K8sSectionId
  res?: string
  // Original detail-page entry point (e.g. 'ns-home') — forwarded back
  // so the detail's own back button still lands on the real origin.
  from?: string
}

export function PodTerminalPage({
  search,
  onBack,
}: {
  search: Partial<PodConsoleQuery>
  onBack: () => void
}) {
  const params = {
    cluster_id: search.cluster ?? 0,
    version: 'v1',
    resource: 'pods',
    namespace: search.ns || undefined,
  }
  const pod = useQuery({
    queryKey: ['k8s', 'resource', params, search.pod],
    queryFn: () => k8sApi.resource(search.pod ?? '', params),
    enabled: Boolean(search.cluster) && Boolean(search.pod),
  })
  const caps = useCapabilities(search.cluster, search.ns)
  const containers = pod.data ? podContainers(pod.data.data) : []

  return (
    <>
      <Header fixed>
        <Button
          variant='ghost'
          size='sm'
          className='-ms-2 text-[#415b80]'
          onClick={onBack}
        >
          <ArrowLeft className='size-4' /> 返回
        </Button>
        <div className='flex min-w-0 items-center gap-2 text-sm'>
          <span className='font-semibold text-[#214b8d]'>终端</span>
          <span className='text-[#b0bfd4]'>/</span>
          <span className='truncate font-mono text-xs text-[#415b80]'>
            {search.ns ?? 'default'} / {search.pod}
            {search.container ? ` · ${search.container}` : ''}
          </span>
        </div>
        <ThemeSwitch />
      </Header>
      <Main fluid className='p-3 sm:p-4'>
        {!search.cluster || !search.pod ? (
          <div className='rounded-xl border border-[#e1e6ef] bg-white py-12 text-center text-sm text-muted-foreground'>
            缺少集群或 Pod 参数，无法打开终端
          </div>
        ) : pod.isPending ? (
          <Skeleton className='h-[calc(100dvh-7rem)] w-full rounded-xl' />
        ) : pod.isError ? (
          <div className='rounded-xl border border-[#e1e6ef] bg-white py-12 text-center text-sm text-destructive'>
            {errDetail(pod.error)}
          </div>
        ) : caps.data?.exec === false ? (
          <div className='rounded-xl border border-[#e1e6ef] bg-white py-12 text-center text-sm text-muted-foreground'>
            当前账号没有该名称空间的终端权限
          </div>
        ) : (
          <Suspense
            fallback={
              <Skeleton className='h-[calc(100dvh-7rem)] w-full rounded-xl' />
            }
          >
            <TerminalPanel
              params={params}
              pod={search.pod ?? ''}
              containers={containers}
              initialContainer={search.container}
              initialCommand={search.command}
              autoConnect
              heightClass='h-[calc(100dvh-10.5rem)] min-h-72'
            />
          </Suspense>
        )}
      </Main>
    </>
  )
}
