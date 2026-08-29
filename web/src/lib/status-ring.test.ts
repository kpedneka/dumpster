import { describe, it, expect } from 'vitest'
import { computeStatusRing, EXPECTED_PROCESSING_MS, STALLED_FRACTION_CAP, PENDING_FRACTION } from './status-ring'

describe('computeStatusRing', () => {
  it('shows a small non-zero fill for a queued (pending) document', () => {
    const result = computeStatusRing('pending', '2026-01-01T00:00:00Z', Date.parse('2026-01-01T00:00:00Z'))
    expect(result.fraction).toBe(PENDING_FRACTION)
    expect(result.stalled).toBe(false)
    expect(result.label).toBe('Pending')
  })

  it('grows fill proportionally to elapsed time while processing', () => {
    const updatedAt = '2026-01-01T00:00:00Z'
    const now = Date.parse(updatedAt) + 5_000 // 5s into a 45s expected duration
    const result = computeStatusRing('processing', updatedAt, now)

    expect(result.fraction).toBeCloseTo(5_000 / EXPECTED_PROCESSING_MS, 5)
    expect(result.stalled).toBe(false)
    expect(result.label).toBe('Processing…')
  })

  it('caps fill and flags stalled once elapsed time exceeds the expected duration', () => {
    const updatedAt = '2026-01-01T00:00:00Z'
    const now = Date.parse(updatedAt) + EXPECTED_PROCESSING_MS + 20_000 // well past expected
    const result = computeStatusRing('processing', updatedAt, now)

    expect(result.fraction).toBe(STALLED_FRACTION_CAP)
    expect(result.stalled).toBe(true)
  })

  it('never reports stalled for a fresh processing job right at the threshold boundary', () => {
    const updatedAt = '2026-01-01T00:00:00Z'
    const now = Date.parse(updatedAt) + EXPECTED_PROCESSING_MS - 1
    const result = computeStatusRing('processing', updatedAt, now)

    expect(result.stalled).toBe(false)
    expect(result.fraction).toBeLessThan(1)
  })

  it('is fully filled for a completed (indexed) document', () => {
    const result = computeStatusRing('indexed', '2026-01-01T00:00:00Z', Date.parse('2026-01-01T00:05:00Z'))
    expect(result.fraction).toBe(1)
    expect(result.stalled).toBe(false)
    expect(result.label).toBe('Indexed')
  })

  it('is fully filled for a failed document', () => {
    const result = computeStatusRing('failed', '2026-01-01T00:00:00Z', Date.parse('2026-01-01T00:05:00Z'))
    expect(result.fraction).toBe(1)
    expect(result.stalled).toBe(false)
    expect(result.label).toBe('Failed')
  })
})
