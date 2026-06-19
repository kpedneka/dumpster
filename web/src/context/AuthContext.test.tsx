import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AuthProvider, useAuth } from './AuthContext'
import { setDraftQuery, getDraftQuery } from '@/lib/search-state'

// lib/auth wraps the browser localStorage API; mocked here so this test
// doesn't depend on a real localStorage implementation being present.
vi.mock('@/lib/auth', () => ({
  saveSession: vi.fn(),
  clearSession: vi.fn(),
  getToken: vi.fn(() => 'token-1'),
  getUser: vi.fn(() => ({ id: 'user-1', email: 'user1@example.com' })),
}))

function LogoutButton() {
  const { logout } = useAuth()
  return <button onClick={logout}>Sign out</button>
}

function renderWithProvider(queryClient: QueryClient) {
  return render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <LogoutButton />
      </AuthProvider>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  sessionStorage.clear()
})

describe('AuthContext logout', () => {
  it('clears the TanStack Query cache so a same-tab account switch never renders stale tenant data', async () => {
    const queryClient = new QueryClient()
    queryClient.setQueryData(['kbs', 'list'], { items: [{ id: 'kb-1', name: 'User 1 KB' }] })

    const user = userEvent.setup()
    renderWithProvider(queryClient)

    await user.click(screen.getByRole('button', { name: /sign out/i }))

    await waitFor(() => {
      expect(queryClient.getQueryData(['kbs', 'list'])).toBeUndefined()
    })
  })

  it('clears search sessionStorage so the next user does not see a previous draft query', async () => {
    setDraftQuery('kb-1', 'user 1 draft query')
    const queryClient = new QueryClient()

    const user = userEvent.setup()
    renderWithProvider(queryClient)

    await user.click(screen.getByRole('button', { name: /sign out/i }))

    await waitFor(() => {
      expect(getDraftQuery('kb-1')).toBe('')
    })
  })
})
