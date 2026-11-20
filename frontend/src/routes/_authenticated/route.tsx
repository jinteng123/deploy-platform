/* eslint-disable react-refresh/only-export-components */
import { createFileRoute } from '@tanstack/react-router'
import { AuthGuard } from '@/components/auth-guard'
import { AuthenticatedLayout } from '@/components/layout/authenticated-layout'

function AuthenticatedRoute() {
  return (
    <AuthGuard>
      <AuthenticatedLayout />
    </AuthGuard>
  )
}

export const Route = createFileRoute('/_authenticated')({
  component: AuthenticatedRoute,
})
