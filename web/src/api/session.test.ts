import { describe, it, expect, vi, beforeEach } from 'vitest'

vi.mock('./client', () => ({
  api: { GET: vi.fn() },
}))

// session.ts memoizes its promise at module scope, so each test needs a
// fresh module instance -- otherwise a promise cached by an earlier test
// leaks into the next one and hides real request counts.
beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
})

describe('ensureSession', () => {
  it('fires exactly one request even when called concurrently by multiple callers', async () => {
    const { api } = await import('./client')
    const { ensureSession } = await import('./session')
    vi.mocked(api.GET).mockResolvedValue({ data: {}, error: undefined } as never)

    await Promise.all([ensureSession(), ensureSession(), ensureSession()])

    expect(api.GET).toHaveBeenCalledTimes(1)
  })

  it('reuses the same resolved promise on a later, non-concurrent call', async () => {
    const { api } = await import('./client')
    const { ensureSession } = await import('./session')
    vi.mocked(api.GET).mockResolvedValue({ data: {}, error: undefined } as never)

    await ensureSession()
    await ensureSession()

    expect(api.GET).toHaveBeenCalledTimes(1)
  })

  it('allows a retry on the next call after a failure, rather than caching it forever', async () => {
    const { api } = await import('./client')
    const { ensureSession } = await import('./session')
    vi.mocked(api.GET).mockRejectedValueOnce(new Error('network error'))
    vi.mocked(api.GET).mockResolvedValueOnce({ data: {}, error: undefined } as never)

    await expect(ensureSession()).rejects.toThrow('network error')
    await expect(ensureSession()).resolves.toBeUndefined()

    expect(api.GET).toHaveBeenCalledTimes(2)
  })
})
