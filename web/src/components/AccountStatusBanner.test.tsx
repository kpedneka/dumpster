import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { AccountStatusBanner } from './AccountStatusBanner'
import { api } from '@/api/client'

vi.mock('@/api/client', () => ({
  api: { GET: vi.fn() },
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
})

describe('AccountStatusBanner', () => {
  it('renders nothing when the warning is not active', async () => {
    vi.mocked(api.GET).mockResolvedValue({
      data: { warning_active: false, deletes_at: new Date(Date.now() + 20 * 60_000).toISOString() },
      error: undefined,
    } as never)

    renderBanner()

    await waitFor(() => expect(api.GET).toHaveBeenCalled())
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('shows the session expiry message when the warning is active', async () => {
    vi.mocked(api.GET).mockResolvedValue({
      data: { warning_active: true, deletes_at: new Date(Date.now() + 10 * 60_000).toISOString() },
      error: undefined,
    } as never)

    renderBanner()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/your session and all documents will be permanently deleted/i)
    expect(alert).toHaveTextContent(/due to inactivity/i)
  })

  it('includes the expiry time in the message', async () => {
    vi.mocked(api.GET).mockResolvedValue({
      data: { warning_active: true, deletes_at: '2026-08-21T15:45:00Z' },
      error: undefined,
    } as never)

    renderBanner()

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/at .+due to inactivity/i)
  })

  it('always queries the API without requiring an auth gate', async () => {
    vi.mocked(api.GET).mockResolvedValue({
      data: { warning_active: false, deletes_at: new Date(Date.now() + 20 * 60_000).toISOString() },
      error: undefined,
    } as never)

    renderBanner()

    await waitFor(() => expect(api.GET).toHaveBeenCalled())
  })
})
