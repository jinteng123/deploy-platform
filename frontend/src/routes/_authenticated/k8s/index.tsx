/* eslint-disable react-refresh/only-export-components */
import z from 'zod'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { K8sPage, type K8sDetailQuery, type K8sSearch } from '@/features/k8s/k8s-page'

const k8sSearchSchema = z.object({
  cluster: z.number().int().positive().optional().catch(undefined),
  ns: z.string().optional().catch(undefined),
  section: z
    .enum(['overview', 'workloads', 'pods', 'network', 'config', 'storage'])
    .optional()
    .catch(undefined),
  res: z.string().optional().catch(undefined),
  pod: z.string().optional().catch(undefined),
})

export const Route = createFileRoute('/_authenticated/k8s/')({
  validateSearch: k8sSearchSchema,
  component: K8sRoute,
})

function K8sRoute() {
  const search = Route.useSearch()
  const navigate = useNavigate()
  return (
    <K8sPage
      search={search}
      onSearch={(patch: Partial<K8sSearch>, opts?: { replace?: boolean }) =>
        navigate({
          to: '.',
          search: (prev: K8sSearch) => ({ ...prev, ...patch }),
          replace: opts?.replace ?? true,
        })
      }
      onOpenDetail={(q: K8sDetailQuery) => navigate({ to: '/k8s/detail', search: q })}
      onOpenClusterManagement={() => navigate({ to: '/settings/clusters' })}
      onOpenTerminal={(q) => navigate({ to: '/k8s/console', search: q })}
    />
  )
}
