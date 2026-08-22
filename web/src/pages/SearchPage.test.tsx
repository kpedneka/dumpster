import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
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
  vi.clearAllMocks()
  vi.mocked(api.GET).mockResolvedValue({
    data: { id: 'kb-1', name: 'Test KB' },
    error: undefined,
  } as never)
  sessionStorage.clear()
})

describe('SearchPage', () => {
  it('shows a prompt before any search is submitted', () => {
    renderSearchPage()
    expect(screen.getByText(/ask a question/i)).toBeInTheDocument()
  })

  it('submits the query when Enter is pressed', async () => {
    vi.mocked(api.POST).mockResolvedValue({
      data: {
        summary: 'Paris is the capital of France [1].',
        citations: [{ number: 1, document_id: 'doc-1', chunk_id: 'chunk-1', char_start: 0, char_end: 10 }],
      },
      error: undefined,
    } as never)

    const user = userEvent.setup()
    renderSearchPage()

    await user.type(screen.getByRole('textbox'), 'What is the capital of France?{Enter}')

    await waitFor(() => {
      expect(screen.getByText(/paris is the capital of france/i)).toBeInTheDocument()
    })
  })

  it('does not submit when Shift+Enter is pressed', async () => {
    const user = userEvent.setup()
    renderSearchPage()

    await user.type(screen.getByRole('textbox'), 'first line{Shift>}{Enter}{/Shift}second line')

    expect(api.POST).not.toHaveBeenCalled()
    expect(screen.getByRole('textbox')).toHaveValue('first line\nsecond line')
  })

  it('submits a query and renders the cited summary', async () => {
    vi.mocked(api.POST).mockResolvedValue({
      data: {
        summary: 'Paris is the capital of France [1].',
        citations: [{ number: 1, document_id: 'doc-1', chunk_id: 'chunk-1', char_start: 0, char_end: 10 }],
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

  describe('state persistence across remounts', () => {
    it('restores an in-progress, unsubmitted query after the component remounts', async () => {
      const user = userEvent.setup()
      const { unmount } = renderSearchPage()

      await user.type(screen.getByRole('textbox'), 'mid-typing query')
      unmount()

      renderSearchPage()
      expect(screen.getByRole('textbox')).toHaveValue('mid-typing query')
    })

    it('restores a submitted query and its results after the component remounts', async () => {
      vi.mocked(api.POST).mockResolvedValue({
        data: {
          summary: 'Paris is the capital of France [1].',
          citations: [{ number: 1, document_id: 'doc-1', chunk_id: 'chunk-1', char_start: 0, char_end: 10 }],
        },
        error: undefined,
      } as never)

      const user = userEvent.setup()
      const { unmount } = renderSearchPage()

      await user.type(screen.getByRole('textbox'), 'What is the capital of France?')
      await user.click(screen.getByRole('button', { name: /search/i }))
      await waitFor(() => {
        expect(screen.getByText(/paris is the capital of france/i)).toBeInTheDocument()
      })
      unmount()

      renderSearchPage()
      expect(screen.getByRole('textbox')).toHaveValue('What is the capital of France?')
      await waitFor(() => {
        expect(screen.getByText(/paris is the capital of france/i)).toBeInTheDocument()
      })
    })

    it('keeps search state isolated between different knowledge bases', async () => {
      const user = userEvent.setup()
      const { unmount } = renderSearchPage('kb-1')

      await user.type(screen.getByRole('textbox'), 'kb-1 query')
      unmount()

      renderSearchPage('kb-2')
      expect(screen.getByRole('textbox')).toHaveValue('')
    })
  })

  describe('citation markers', () => {
    function mockGetByPath(handlers: Record<string, () => unknown>) {
      vi.mocked(api.GET).mockImplementation(((path: string) => {
        const handler = handlers[path]
        if (!handler) throw new Error(`unexpected api.GET call: ${path}`)
        return Promise.resolve(handler())
      }) as never)
    }

    async function searchAndGetMarker(label = '[1]') {
      vi.mocked(api.POST).mockResolvedValue({
        data: {
          summary: 'Paris [1] is the capital of France.',
          citations: [{ number: 1, document_id: 'doc-1', chunk_id: 'chunk-1', char_start: 0, char_end: 5 }],
        },
        error: undefined,
      } as never)

      const user = userEvent.setup()
      renderSearchPage()
      await user.type(screen.getByRole('textbox'), 'What is the capital of France?')
      await user.click(screen.getByRole('button', { name: /search/i }))
      await waitFor(() => expect(screen.getByText(label)).toBeInTheDocument())

      return { user, marker: screen.getByText(label) }
    }

    it('opens a popover with the highlighted source span when clicked', async () => {
      mockGetByPath({
        '/kbs/{id}': () => ({ data: { id: 'kb-1', name: 'Test KB' }, error: undefined }),
        '/kbs/{kbId}/documents/{docId}/content': () => ({
          data: 'Paris is the capital of France.',
          error: undefined,
        }),
      })

      const { user, marker } = await searchAndGetMarker()
      await user.click(marker)

      const popover = await screen.findByRole('dialog')
      await waitFor(() => {
        expect(within(popover).getByText('Paris')).toBeInTheDocument()
      })
      expect(within(popover).getByText(/is the capital of france/i)).toBeInTheDocument()
    })

    it('shows a loading state while fetching citation content', async () => {
      let resolveContent!: (v: unknown) => void
      const pending = new Promise((resolve) => {
        resolveContent = resolve
      })
      mockGetByPath({
        '/kbs/{id}': () => ({ data: { id: 'kb-1', name: 'Test KB' }, error: undefined }),
        '/kbs/{kbId}/documents/{docId}/content': () => pending,
      })

      const { user, marker } = await searchAndGetMarker()
      await user.click(marker)

      const popover = await screen.findByRole('dialog')
      await waitFor(() => {
        expect(within(popover).getByText(/loading/i)).toBeInTheDocument()
      })

      resolveContent({ data: 'Paris is the capital of France.', error: undefined })
      await waitFor(() => {
        expect(within(popover).getByText('Paris')).toBeInTheDocument()
      })
    })

    it('shows an error state when citation content fails to load', async () => {
      mockGetByPath({
        '/kbs/{id}': () => ({ data: { id: 'kb-1', name: 'Test KB' }, error: undefined }),
        '/kbs/{kbId}/documents/{docId}/content': () => ({
          data: undefined,
          error: { error: 'document not found' },
        }),
      })

      const { user, marker } = await searchAndGetMarker()
      await user.click(marker)

      const popover = await screen.findByRole('dialog')
      await waitFor(() => {
        expect(within(popover).getByText(/could not load source text/i)).toBeInTheDocument()
      })
    })

    it('reuses cached content for a second citation into the same document', async () => {
      let contentCalls = 0
      mockGetByPath({
        '/kbs/{id}': () => ({ data: { id: 'kb-1', name: 'Test KB' }, error: undefined }),
        '/kbs/{kbId}/documents/{docId}/content': () => {
          contentCalls += 1
          return { data: 'Paris is the capital of France.', error: undefined }
        },
      })

      vi.mocked(api.POST).mockResolvedValue({
        data: {
          summary: 'Paris [1] is the capital of France [2].',
          // Deliberately out of marker order — citations are matched by `number`,
          // never by array position.
          citations: [
            { number: 2, document_id: 'doc-1', chunk_id: 'chunk-2', char_start: 6, char_end: 31 },
            { number: 1, document_id: 'doc-1', chunk_id: 'chunk-1', char_start: 0, char_end: 5 },
          ],
        },
        error: undefined,
      } as never)

      const user = userEvent.setup()
      renderSearchPage()
      await user.type(screen.getByRole('textbox'), 'What is the capital of France?')
      await user.click(screen.getByRole('button', { name: /search/i }))
      await waitFor(() => expect(screen.getByText('[1]')).toBeInTheDocument())

      await user.click(screen.getByText('[1]'))
      await waitFor(() => expect(contentCalls).toBe(1))
      await user.keyboard('{Escape}')

      await user.click(screen.getByText('[2]'))
      const popover = await screen.findByRole('dialog')
      await waitFor(() => {
        expect(within(popover).getByText(/is the capital of france/i)).toBeInTheDocument()
      })
      expect(contentCalls).toBe(1)
    })
  })
})
