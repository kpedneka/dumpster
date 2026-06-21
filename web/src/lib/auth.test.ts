import { describe, it, expect, vi, afterEach } from 'vitest'
import { getToken, setTokenGetter } from './auth'

afterEach(() => {
  setTokenGetter(null)
})

describe('getToken', () => {
  it('returns null when no token getter is registered', async () => {
    await expect(getToken()).resolves.toBeNull()
  })

  it('delegates to the registered token getter', async () => {
    setTokenGetter(vi.fn().mockResolvedValue('a-token'))
    await expect(getToken()).resolves.toBe('a-token')
  })

  it('reflects updates when the token getter is replaced', async () => {
    setTokenGetter(vi.fn().mockResolvedValue('first'))
    await expect(getToken()).resolves.toBe('first')

    setTokenGetter(vi.fn().mockResolvedValue('second'))
    await expect(getToken()).resolves.toBe('second')
  })

  it('returns null again after the token getter is cleared', async () => {
    setTokenGetter(vi.fn().mockResolvedValue('a-token'))
    setTokenGetter(null)
    await expect(getToken()).resolves.toBeNull()
  })
})
