import { createFileRoute } from '@tanstack/react-router'
import { ApiKeysPage } from '@/features/settings/api-keys'

export const Route = createFileRoute('/_authenticated/settings/api-keys')({
  component: ApiKeysPage,
})
