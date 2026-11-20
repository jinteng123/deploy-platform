/* eslint-disable react-refresh/only-export-components */
import z from 'zod'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { WorkloadEditorPage } from '@/features/k8s/workload-editor-page'

const editSearchSchema = z.object({
  cluster: z.number().int().positive().optional().catch(undefined),
  g: z.string().optional().catch(undefined),
  v: z.string().optional().catch(undefined),
  r: z.string().optional().catch(undefined),
  ns: z.string().optional().catch(undefined),
  name: z.string().optional().catch(undefined),
})

export const Route = createFileRoute('/_authenticated/k8s/edit')({
  validateSearch: editSearchSchema,
  component: WorkloadEditRoute,
})

function WorkloadEditRoute() {
  const search = Route.useSearch()
  const navigate = useNavigate()
  const back = () =>
    navigate({
      to: '/k8s/detail',
      replace: true,
      search: {
        cluster: search.cluster,
        g: search.g,
        v: search.v,
        r: search.r,
        ns: search.ns,
        name: search.name,
      },
    })
  return <WorkloadEditorPage search={search} onBack={back} />
}
