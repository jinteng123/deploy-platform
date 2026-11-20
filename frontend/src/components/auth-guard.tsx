import { useEffect } from 'react'
import { useLocation, useNavigate } from '@tanstack/react-router'
import { ensureSession } from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'
import { Skeleton } from '@/components/ui/skeleton'

// AuthGuard resolves the backend session before any authenticated page
// renders: unknown → probe /auth/me, anonymous → bounce to sign-in with
// the current location so login can return here. A mid-session 401 flips
// the store to anonymous and this effect redirects as well.
export function AuthGuard({ children }: { children: React.ReactNode }) {
  const status = useAuthStore((s) => s.auth.status)
  const location = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    if (status === 'unknown') void ensureSession()
  }, [status])

  useEffect(() => {
    // Skip while the sign-in navigation is still resolving: location.href has
    // already flipped to /sign-in?… but this guard has not unmounted yet, and
    // re-firing here would nest redirect params into an infinite loop.
    if (status === 'anonymous' && location.pathname !== '/sign-in') {
      void navigate({
        to: '/sign-in',
        search: { redirect: location.href },
        replace: true,
      })
    }
  }, [status, location.pathname, location.href, navigate])

  if (status !== 'authenticated') {
    return (
      <div className='flex h-svh items-center justify-center'>
        <Skeleton className='h-8 w-48' />
      </div>
    )
  }
  return <>{children}</>
}
