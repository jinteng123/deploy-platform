import { describe, expect, it } from 'vitest'
import { age } from './age'

const now = new Date('2026-09-18T12:00:00Z')

describe('age', () => {
  it('renders kubectl-style durations', () => {
    expect(age('2026-09-18T11:59:15Z', now)).toBe('45s')
    expect(age('2026-09-18T11:48:00Z', now)).toBe('12m')
    expect(age('2026-09-18T08:40:00Z', now)).toBe('3h20m')
    expect(age('2026-09-13T09:00:00Z', now)).toBe('5d3h')
  })

  it('handles missing and invalid timestamps', () => {
    expect(age(undefined, now)).toBe('-')
    expect(age('', now)).toBe('-')
    expect(age('not-a-date', now)).toBe('-')
  })

  it('clamps future timestamps to zero', () => {
    expect(age('2026-09-18T12:00:10Z', now)).toBe('0s')
  })
})
