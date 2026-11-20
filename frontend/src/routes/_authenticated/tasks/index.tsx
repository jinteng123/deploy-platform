import { z } from 'zod'
/* eslint-disable react-refresh/only-export-components */
import { createFileRoute } from '@tanstack/react-router'
import { ReleasePage } from '@/features/platform/release-page'

function TasksRoute() {
  const { release_id: linkedReleaseID } = Route.useSearch()
  const navigate = Route.useNavigate()
  return (
    <ReleasePage
      linkedReleaseID={linkedReleaseID}
      onLinkedReleaseClose={() =>
        void navigate({ search: { release_id: undefined } })
      }
    />
  )
}

export const Route = createFileRoute('/_authenticated/tasks/')({
  validateSearch: z.object({
    release_id: z.coerce.number().int().positive().optional(),
  }),
  component: TasksRoute,
})
