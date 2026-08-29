import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { StatusRing } from './StatusRing'

describe('StatusRing', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('labels a pending document', () => {
    render(<StatusRing status="pending" updatedAt="2026-01-01T00:00:00Z" />)
    expect(screen.getByRole('img', { name: 'Pending' })).toBeInTheDocument()
  })

  it('labels an indexed document', () => {
    render(<StatusRing status="indexed" updatedAt="2026-01-01T00:00:00Z" />)
    expect(screen.getByRole('img', { name: 'Indexed' })).toBeInTheDocument()
  })

  it('labels a failed document', () => {
    render(<StatusRing status="failed" updatedAt="2026-01-01T00:00:00Z" />)
    expect(screen.getByRole('img', { name: 'Failed' })).toBeInTheDocument()
  })

  it('flags a processing document that has run well past the expected duration as stalled', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T00:01:30Z')) // 90s after updatedAt, well past the 45s heuristic
    render(<StatusRing status="processing" updatedAt="2026-01-01T00:00:00Z" />)
    expect(screen.getByRole('img', { name: /Processing… — taking longer than usual/ })).toBeInTheDocument()
  })

  it('does not flag a recently-started processing document as stalled', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-01-01T00:00:05Z')) // 5s after updatedAt
    render(<StatusRing status="processing" updatedAt="2026-01-01T00:00:00Z" />)
    expect(screen.getByRole('img', { name: 'Processing…' })).toBeInTheDocument()
  })
})
