import { describe, it, expect } from 'vitest'
import { cn, describeError } from './utils'

describe('cn', () => {
  it('merges class names and resolves Tailwind conflicts', () => {
    expect(cn('px-2 py-1', 'px-4')).toBe('py-1 px-4')
  })
})

describe('describeError', () => {
  it('extracts the message from an {error: string} API error body', () => {
    expect(describeError({ error: 'document is being indexed and cannot be deleted yet' })).toBe(
      'document is being indexed and cannot be deleted yet',
    )
  })

  it('falls back to the default message for anything else, e.g. a network failure', () => {
    expect(describeError(new TypeError('Failed to fetch'))).toBe('Something went wrong. Try again.')
  })

  it('falls back to a caller-provided message instead of the default', () => {
    expect(describeError(new Error('boom'), 'Search failed. Try again.')).toBe('Search failed. Try again.')
  })
})
