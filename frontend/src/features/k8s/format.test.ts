import { describe, expect, it } from 'vitest'
import { formatBytes, formatCPU, formatPercent } from './format'

describe('formatCPU', () => {
  it('renders sub-core values in millicores like kubectl top', () => {
    expect(formatCPU(0)).toBe('0m')
    expect(formatCPU(250)).toBe('250m')
    expect(formatCPU(999)).toBe('999m')
  })
  it('renders whole cores without a trailing m', () => {
    expect(formatCPU(1000)).toBe('1')
    expect(formatCPU(2500)).toBe('2.5')
    expect(formatCPU(4123)).toBe('4.12')
  })
})

describe('formatBytes', () => {
  it('picks the largest readable unit', () => {
    expect(formatBytes(0)).toBe('0')
    expect(formatBytes(512)).toBe('512')
    expect(formatBytes(2048)).toBe('2Ki')
    expect(formatBytes(64 * 1024 ** 2)).toBe('64Mi')
    expect(formatBytes(8 * 1024 ** 3)).toBe('8Gi')
  })
  it('keeps one decimal for fractional values', () => {
    expect(formatBytes(1536 * 1024 ** 2)).toBe('1.5Gi')
  })
})

describe('formatPercent', () => {
  it('renders whole percents and guards zero capacity', () => {
    expect(formatPercent(500, 4000)).toBe('13%')
    expect(formatPercent(0, 0)).toBe('0%')
    expect(formatPercent(2 * 1024 ** 3, 8 * 1024 ** 3)).toBe('25%')
  })
})
