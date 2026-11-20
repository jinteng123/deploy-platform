import { createFileRoute } from '@tanstack/react-router'
import { ApprovalScopesPage } from '@/features/settings/approval-scopes'

export const Route = createFileRoute(
  '/_authenticated/settings/approval-scopes'
)({
  component: ApprovalScopesPage,
})
