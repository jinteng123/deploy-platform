import { beforeEach, describe, expect, it } from 'vitest'
import type { PublicUser } from '@/api/client'
import { useAuthStore } from './auth-store'

const sampleUser: PublicUser = {
  id: 1,
  username: 'viewer',
  display_name: 'Read Only',
  role: 'viewer',
  k8s_username: 'kbtest-viewer',
  k8s_groups: ['viewers'],
  enabled: true,
}

describe('useAuthStore', () => {
  beforeEach(() => {
    useAuthStore.getState().auth.reset()
  })

  it('starts unprobed with no user', () => {
    const { auth } = useAuthStore.getState()
    expect(auth.status).toBe('unknown')
    expect(auth.user).toBeNull()
  })

  it('setUser marks the session authenticated', () => {
    useAuthStore.getState().auth.setUser(sampleUser)
    const { auth } = useAuthStore.getState()
    expect(auth.status).toBe('authenticated')
    expect(auth.user).toEqual(sampleUser)
  })

  it('markAnonymous clears the user and flips status', () => {
    useAuthStore.getState().auth.setUser(sampleUser)
    useAuthStore.getState().auth.markAnonymous()
    const { auth } = useAuthStore.getState()
    expect(auth.status).toBe('anonymous')
    expect(auth.user).toBeNull()
  })

  it('reset returns to the unprobed state so the guard re-checks /me', () => {
    useAuthStore.getState().auth.setUser(sampleUser)
    useAuthStore.getState().auth.reset()
    const { auth } = useAuthStore.getState()
    expect(auth.status).toBe('unknown')
    expect(auth.user).toBeNull()
  })
})
