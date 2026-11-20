import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render } from 'vitest-browser-react'
import { page } from 'vitest/browser'
import { ensureSession, type PublicUser } from '@/api/client'
import { useAuthStore } from '@/stores/auth-store'

const navigate = vi.fn()

let mockLocation = { href: '/k8s?cluster=1', pathname: '/k8s' }

vi.mock('@tanstack/react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@tanstack/react-router')>()
  return {
    ...actual,
    useLocation: () => mockLocation,
    useNavigate: () => navigate,
  }
})

vi.mock('@/api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/client')>()
  return { ...actual, ensureSession: vi.fn() }
})

const { AuthGuard } = await import('@/components/auth-guard')

const user: PublicUser = {
  id: 1,
  username: 'viewer',
  display_name: 'Viewer',
  role: 'viewer',
  k8s_username: 'kbtest-viewer',
  k8s_groups: [],
  enabled: true,
}

describe('AuthGuard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAuthStore.getState().auth.reset()
    mockLocation = { href: '/k8s?cluster=1', pathname: '/k8s' }
  })

  it('probes /me and renders the layout once authenticated', async () => {
    vi.mocked(ensureSession).mockImplementation(async () => {
      useAuthStore.getState().auth.setUser(user)
      return user
    })
    await render(<AuthGuard><div data-testid='authed-shell'>authed</div></AuthGuard>)
    await expect
      .element(page.getByTestId('authed-shell'))
      .toBeInTheDocument()
    expect(ensureSession).toHaveBeenCalledOnce()
    expect(navigate).not.toHaveBeenCalled()
  })

  it('redirects anonymous users to sign-in with the current location', async () => {
    vi.mocked(ensureSession).mockImplementation(async () => {
      useAuthStore.getState().auth.markAnonymous()
      return null
    })
    await render(<AuthGuard><div data-testid='authed-shell'>authed</div></AuthGuard>)
    await vi.waitFor(() =>
      expect(navigate).toHaveBeenCalledWith({
        to: '/sign-in',
        search: { redirect: '/k8s?cluster=1' },
        replace: true,
      })
    )
    expect(page.getByTestId('authed-shell').query()).toBeNull()
  })

  it('does not re-navigate while still mounted on the sign-in route', async () => {
    // During the async navigation away from an authed page the guard stays
    // mounted while location.href already reads /sign-in?… — re-firing would
    // nest redirect params into an infinite loop (React #185).
    mockLocation = {
      href: '/sign-in?redirect=%2Fk8s',
      pathname: '/sign-in',
    }
    useAuthStore.getState().auth.setUser(user)
    await render(<AuthGuard><div data-testid='authed-shell'>authed</div></AuthGuard>)
    useAuthStore.getState().auth.markAnonymous()
    await new Promise((r) => setTimeout(r, 50))
    expect(navigate).not.toHaveBeenCalled()
  })

  it('redirects mid-session when a 401 flips the store to anonymous', async () => {
    useAuthStore.getState().auth.setUser(user)
    await render(<AuthGuard><div data-testid='authed-shell'>authed</div></AuthGuard>)
    await expect.element(page.getByTestId('authed-shell')).toBeInTheDocument()
    // A later API call hits 401 → interceptor marks anonymous → guard fires.
    useAuthStore.getState().auth.markAnonymous()
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledOnce())
  })
})
