import { createFileRoute } from '@tanstack/react-router'
import { ClustersPage } from '@/features/platform/clusters-page'

export const Route = createFileRoute('/_authenticated/settings/clusters')({
  component: ClustersPage,
})
