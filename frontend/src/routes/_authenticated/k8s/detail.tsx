/* eslint-disable react-refresh/only-export-components */
import z from 'zod'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { sectionForResource } from '@/features/k8s/catalog'
import { ResourceDetailPage } from '@/features/k8s/resource-detail-page'

const detailSearchSchema = z.object({
  cluster: z.number().int().positive().optional().catch(undefined),
  g: z.string().optional().catch(undefined),
  v: z.string().optional().catch(undefined),
  r: z.string().optional().catch(undefined),
  ns: z.string().optional().catch(undefined),
  name: z.string().optional().catch(undefined),
  from: z.string().optional().catch(undefined),
})

export const Route = createFileRoute('/_authenticated/k8s/detail')({
  validateSearch: detailSearchSchema,
  component: DetailRoute,
})

function DetailRoute() {
  const search = Route.useSearch()
  const navigate = useNavigate()
  return (
    <ResourceDetailPage
      search={search}
      onBack={() =>
        navigate({
          to: '/k8s',
          // 'Up' navigation replaces the detail entry so browser Back
          // doesn't replay the console/detail round-trip stack.
          replace: true,
          search:
            search.from === 'ns-home'
              ? {
                  cluster: search.cluster,
                  ns: search.ns,
                  section: 'overview',
                  res: 'namespace-home',
                }
              : {
                  cluster: search.cluster,
                  ns: search.ns,
                  section: sectionForResource(search.r),
                  res: search.r,
                },
        })
      }
      onEdit={() =>
        navigate({
          to: '/k8s/edit',
          search: {
            cluster: search.cluster,
            g: search.g,
            v: search.v,
            r: search.r,
            ns: search.ns,
            name: search.name,
          },
        })
      }
      onOpenTerminal={(q) =>
        navigate({
          to: '/k8s/console',
          search: {
            ...q,
            g: search.g,
            v: search.v,
            r: search.r,
            name: search.name,
            from: search.from,
          },
        })
      }
    />
  )
}
