import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { Sidebar } from './Sidebar'
import { api } from '@/api/client'

vi.mock('@/api/client', () => ({
  api: { GET: vi.fn(), POST: vi.fn() },
}))

function renderSidebar(initialEntry = '/kbs') {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <Sidebar />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.GET).mockResolvedValue({
    data: { items: [], next_cursor: null },
    error: undefined,
  } as never)
})

describe('Sidebar', () => {
  it('links the brand (icon + title) back to the knowledge base list from any page', () => {
    renderSidebar('/privacy')
    expect(screen.getByRole('link', { name: /dumpster.*multimodal knowledge base/is })).toHaveAttribute(
      'href',
      '/kbs',
    )
  })

  it('links the footer Privacy link to the privacy page', () => {
    renderSidebar()
    expect(screen.getByRole('link', { name: 'Privacy' })).toHaveAttribute('href', '/privacy')
  })
})
