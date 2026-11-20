import { create } from 'zustand'
import type { PublicUser } from '@/api/client'

// Session state machine: the browser holds only an opaque HttpOnly cookie,
// so "logged in" is a server-side fact the app learns via /auth/me.
//   unknown       → not probed yet this page load (route guard fetches /me)
//   authenticated → /me (or login) returned a user
//   anonymous     → /me 401'd, logout ran, or an authed call hit a 401
export type AuthStatus = 'unknown' | 'authenticated' | 'anonymous'

interface AuthState {
  auth: {
    user: PublicUser | null
    status: AuthStatus
    setUser: (user: PublicUser) => void
    markAnonymous: () => void
    reset: () => void
  }
}

export const useAuthStore = create<AuthState>()((set) => ({
  auth: {
    user: null,
    status: 'unknown',
    setUser: (user) =>
      set((state) => ({
        ...state,
        auth: { ...state.auth, user, status: 'authenticated' },
      })),
    markAnonymous: () =>
      set((state) => ({
        ...state,
        auth: { ...state.auth, user: null, status: 'anonymous' },
      })),
    reset: () =>
      set((state) => ({
        ...state,
        auth: { ...state.auth, user: null, status: 'unknown' },
      })),
  },
}))
