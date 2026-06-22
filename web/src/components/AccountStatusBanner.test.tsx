import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AccountStatusBanner } from './AccountStatusBanner'
import { api } from '@/api/client'
import { useAuth } from '@/context/AuthContext'

vi.mock('@/api/client', () => ({
  api: { GET: vi.fn() },
}))

vi.mock('@/context/AuthContext', () => ({
  useAuth: vi.fn(),
}))

function renderBanner() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <AccountStatusBanner />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(useAuth).mockReturnValue({
    user: { id: 'user-1', email: 'a@example.com' },
    isAuthenticated: true,
    isLoaded: true,
    logout: vi.fn(),
  })
})

describe('AccountStatusBanner', () => {
  it('renders nothing when the warning is not active', async () => {
    vi.mocked(api.GET).mockResolvedValue({
      data: { warning_active: false, deletes_at: '2026-06-29T00:00:00Z' },
      error: undefined,
    } as never)

    renderBanner()

    await waitFor(() => expect(api.GET).toHaveBeenCalled())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows the deletion date when the warning is active', async () => {
    vi.mocked(api.GET).mockResolvedValue({
      data: { warning_active: true, deletes_at: '2026-06-29T00:00:00Z' },
      error: undefined,
    } as never)

    renderBanner()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('June 29, 2026')
  })

  it('does not query the API when unauthenticated', () => {
    vi.mocked(useAuth).mockReturnValue({
      user: null,
      isAuthenticated: false,
      isLoaded: true,
      logout: vi.fn(),
    })

    renderBanner()

    expect(api.GET).not.toHaveBeenCalled()
  })
})
