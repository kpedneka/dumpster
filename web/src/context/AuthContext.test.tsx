import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'

vi.mock('@/api/client', () => ({
  api: { GET: vi.fn() },
}))

// AuthContext's bootstrap (ensureSession) memoizes its promise at module
// scope, so each test needs a fresh module instance -- otherwise a promise
// resolved by an earlier test makes a later test's AuthProvider render
// synchronously instead of actually waiting on its own mocked response.
beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
})

describe('AuthProvider', () => {
  it('reports the session as authenticated once the session bootstrap resolves', async () => {
    const { api } = await import('@/api/client')
    const { AuthProvider, useAuth } = await import('./AuthContext')
    vi.mocked(api.GET).mockResolvedValue({ data: {}, error: undefined } as never)

    function Probe() {
      const { isAuthenticated } = useAuth()
      return <span data-testid="authenticated">{String(isAuthenticated)}</span>
    }

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )

    expect(await screen.findByTestId('authenticated')).toHaveTextContent('true')
  })

  it('does not render children until the session bootstrap request resolves', async () => {
    const { api } = await import('@/api/client')
    const { AuthProvider, useAuth } = await import('./AuthContext')
    let resolveBootstrap!: () => void
    vi.mocked(api.GET).mockReturnValue(
      new Promise((resolve) => {
        resolveBootstrap = () => resolve({ data: {}, error: undefined } as never)
      }) as never,
    )

    function Probe() {
      const { isAuthenticated } = useAuth()
      return <span data-testid="authenticated">{String(isAuthenticated)}</span>
    }

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )

    expect(screen.queryByTestId('authenticated')).not.toBeInTheDocument()

    resolveBootstrap()

    expect(await screen.findByTestId('authenticated')).toHaveTextContent('true')
  })

  it('still renders children if the session bootstrap request fails (fails open)', async () => {
    const { api } = await import('@/api/client')
    const { AuthProvider, useAuth } = await import('./AuthContext')
    vi.mocked(api.GET).mockRejectedValue(new Error('network error'))

    function Probe() {
      const { isAuthenticated } = useAuth()
      return <span data-testid="authenticated">{String(isAuthenticated)}</span>
    }

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )

    expect(await screen.findByTestId('authenticated')).toHaveTextContent('true')
  })

  it('throws when useAuth is called outside AuthProvider', async () => {
    const { useAuth } = await import('./AuthContext')

    function Orphan() {
      useAuth()
      return null
    }
    expect(() => render(<Orphan />)).toThrow('useAuth must be used inside AuthProvider')
  })
})
