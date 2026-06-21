import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider, useAuth } from './AuthContext'
import { getToken } from '@/lib/auth'

const mockUseAuth = vi.fn()
const mockUseUser = vi.fn()

vi.mock('@clerk/clerk-react', () => ({
  useAuth: () => mockUseAuth(),
  useUser: () => mockUseUser(),
}))

function renderWithProvider() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  let captured: ReturnType<typeof useAuth> | undefined
  function Probe() {
    captured = useAuth()
    return (
      <div>
        <span data-testid="authenticated">{String(captured.isAuthenticated)}</span>
        <span data-testid="loaded">{String(captured.isLoaded)}</span>
        <span data-testid="email">{captured.user?.email ?? 'none'}</span>
      </div>
    )
  }
  render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <Probe />
      </AuthProvider>
    </QueryClientProvider>,
  )
  return { queryClient, getCaptured: () => captured! }
}

beforeEach(() => {
  mockUseAuth.mockReset()
  mockUseUser.mockReset()
})

describe('AuthProvider', () => {
  it('reports unauthenticated and unloaded state before Clerk resolves the session', () => {
    mockUseAuth.mockReturnValue({ isLoaded: false, isSignedIn: false, getToken: vi.fn(), signOut: vi.fn() })
    mockUseUser.mockReturnValue({ user: null })

    renderWithProvider()

    expect(screen.getByTestId('loaded')).toHaveTextContent('false')
    expect(screen.getByTestId('authenticated')).toHaveTextContent('false')
  })

  it('exposes the signed-in user once Clerk resolves the session', () => {
    mockUseAuth.mockReturnValue({ isLoaded: true, isSignedIn: true, getToken: vi.fn(), signOut: vi.fn() })
    mockUseUser.mockReturnValue({
      user: { id: 'user_123', primaryEmailAddress: { emailAddress: 'alice@example.com' } },
    })

    renderWithProvider()

    expect(screen.getByTestId('loaded')).toHaveTextContent('true')
    expect(screen.getByTestId('authenticated')).toHaveTextContent('true')
    expect(screen.getByTestId('email')).toHaveTextContent('alice@example.com')
  })

  it('reports signed-out state when Clerk has loaded but there is no session', () => {
    mockUseAuth.mockReturnValue({ isLoaded: true, isSignedIn: false, getToken: vi.fn(), signOut: vi.fn() })
    mockUseUser.mockReturnValue({ user: null })

    renderWithProvider()

    expect(screen.getByTestId('authenticated')).toHaveTextContent('false')
    expect(screen.getByTestId('email')).toHaveTextContent('none')
  })

  it('registers the session token getter with lib/auth so the API client can use it', async () => {
    const clerkGetToken = vi.fn().mockResolvedValue('a-session-token')
    mockUseAuth.mockReturnValue({ isLoaded: true, isSignedIn: true, getToken: clerkGetToken, signOut: vi.fn() })
    mockUseUser.mockReturnValue({
      user: { id: 'user_123', primaryEmailAddress: { emailAddress: 'alice@example.com' } },
    })

    renderWithProvider()

    await expect(getToken()).resolves.toBe('a-session-token')
    expect(clerkGetToken).toHaveBeenCalled()
  })

  it('clears the token getter when signed out, so the API client stops sending stale tokens', async () => {
    mockUseAuth.mockReturnValue({ isLoaded: true, isSignedIn: false, getToken: vi.fn(), signOut: vi.fn() })
    mockUseUser.mockReturnValue({ user: null })

    renderWithProvider()

    await expect(getToken()).resolves.toBeNull()
  })

  it('logout calls Clerk signOut and clears the query cache', async () => {
    const signOut = vi.fn().mockResolvedValue(undefined)
    mockUseAuth.mockReturnValue({ isLoaded: true, isSignedIn: true, getToken: vi.fn(), signOut })
    mockUseUser.mockReturnValue({
      user: { id: 'user_123', primaryEmailAddress: { emailAddress: 'alice@example.com' } },
    })

    const { queryClient, getCaptured } = renderWithProvider()
    queryClient.setQueryData(['kbs'], ['stale-data'])

    await getCaptured().logout()

    expect(signOut).toHaveBeenCalled()
    await waitFor(() => {
      expect(queryClient.getQueryData(['kbs'])).toBeUndefined()
    })
  })

  it('throws when useAuth is called outside an AuthProvider', () => {
    function Orphan() {
      useAuth()
      return null
    }
    expect(() => render(<Orphan />)).toThrow('useAuth must be used inside AuthProvider')
  })
})
