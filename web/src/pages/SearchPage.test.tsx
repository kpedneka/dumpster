import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { SearchPage } from './SearchPage'
import { api } from '@/api/client'

vi.mock('@/api/client', () => ({
  api: { GET: vi.fn(), POST: vi.fn() },
}))

function renderSearchPage(kbId = 'kb-1') {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/kbs/${kbId}/search`]}>
        <Routes>
          <Route path="/kbs/:kbId/search" element={<SearchPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(api.GET).mockResolvedValue({
    data: { id: 'kb-1', name: 'Test KB' },
    error: undefined,
  } as never)
})

describe('SearchPage', () => {
  it('shows a prompt before any search is submitted', () => {
    renderSearchPage()
    expect(screen.getByText(/ask a question/i)).toBeInTheDocument()
  })

  it('submits a query and renders the cited summary', async () => {
    vi.mocked(api.POST).mockResolvedValue({
      data: {
        summary: 'Paris is the capital of France [1].',
        citations: [{ document_id: 'doc-1', chunk_id: 'chunk-1', char_start: 0, char_end: 10 }],
      },
      error: undefined,
    } as never)

    const user = userEvent.setup()
    renderSearchPage()

    await user.type(screen.getByRole('textbox'), 'What is the capital of France?')
    await user.click(screen.getByRole('button', { name: /search/i }))

    await waitFor(() => {
      expect(screen.getByText(/paris is the capital of france/i)).toBeInTheDocument()
    })
    expect(screen.getByText('[1]')).toBeInTheDocument()
    expect(api.POST).toHaveBeenCalledWith('/kbs/{id}/search', {
      params: { path: { id: 'kb-1' } },
      body: { query: 'What is the capital of France?' },
    })
  })

  it('shows a "no results found" empty state when there are no citations', async () => {
    vi.mocked(api.POST).mockResolvedValue({
      data: { summary: 'I could not find relevant information to answer this question.', citations: [] },
      error: undefined,
    } as never)

    const user = userEvent.setup()
    renderSearchPage()

    await user.type(screen.getByRole('textbox'), 'unanswerable question')
    await user.click(screen.getByRole('button', { name: /search/i }))

    await waitFor(() => {
      expect(screen.getByText(/no results found/i)).toBeInTheDocument()
    })
  })

  it('shows an error state when the search request fails', async () => {
    vi.mocked(api.POST).mockResolvedValue({
      data: undefined,
      error: { error: 'search failed' },
    } as never)

    const user = userEvent.setup()
    renderSearchPage()

    await user.type(screen.getByRole('textbox'), 'broken query')
    await user.click(screen.getByRole('button', { name: /search/i }))

    await waitFor(() => {
      expect(screen.getByText(/search failed/i)).toBeInTheDocument()
    })
  })
})
