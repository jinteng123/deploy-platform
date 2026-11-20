import { z } from 'zod'
import { createFileRoute } from '@tanstack/react-router'
import { EnvironmentsPage } from '@/features/platform/environments-page'

export const Route = createFileRoute('/_authenticated/environments/')({
  validateSearch: z.object({
    app_id: z.coerce.number().int().positive().optional(),
  }),
  component: function EnvironmentRoute() {
    const { app_id } = Route.useSearch()
    return <EnvironmentsPage initialAppId={app_id} />
  },
})
