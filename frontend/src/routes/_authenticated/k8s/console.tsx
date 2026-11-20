/* eslint-disable react-refresh/only-export-components */
import z from 'zod'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { PodTerminalPage } from '@/features/k8s/pod-terminal-page'

const consoleSearchSchema = z.object({
  cluster: z.number().int().positive().optional().catch(undefined),
  ns: z.string().optional().catch(undefined),
  pod: z.string().optional().catch(undefined),
  container: z.string().optional().catch(undefined),
  command: z.string().optional().catch(undefined),
  // Origin detail params — used only to navigate back to where the user
  // opened the terminal from.
  g: z.string().optional().catch(undefined),
  v: z.string().optional().catch(undefined),
  r: z.string().optional().catch(undefined),
  name: z.string().optional().catch(undefined),
  // List-page origin for entries that didn't come from a detail page
  // (e.g. the pod drawer) — used as the back target fallback.
  section: z
    .enum(['overview', 'workloads', 'pods', 'network', 'config', 'storage'])
    .optional()
    .catch(undefined),
  res: z.string().optional().catch(undefined),
  from: z.string().optional().catch(undefined),
})

export const Route = createFileRoute('/_authenticated/k8s/console')({
  validateSearch: consoleSearchSchema,
  component: ConsoleRoute,
})

function ConsoleRoute() {
  const search = Route.useSearch()
  const navigate = useNavigate()
  const back = () => {
    if (search.g != null && search.v != null && search.r != null && search.name != null) {
      void navigate({
        to: '/k8s/detail',
        // Replace so Back doesn't keep replaying the console entry.
        replace: true,
        search: {
          cluster: search.cluster,
          g: search.g,
          v: search.v,
          r: search.r,
          ns: search.ns,
          name: search.name,
          from: search.from,
        },
      })
      return
    }
    void navigate({
      to: '/k8s',
      replace: true,
      search: {
        cluster: search.cluster,
        ns: search.ns,
        // A pod terminal always has the pods list as its natural parent.
        section: search.section ?? 'pods',
        res: search.res ?? 'pods',
      },
    })
  }
  return <PodTerminalPage search={search} onBack={back} />
}
