import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { KBDetailPage } from './KBDetailPage'
import { api } from '@/api/client'
import { uploadDocument } from '@/api/upload'
import { toast } from '@/hooks/use-toast'

vi.mock('@/api/client', () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}))
vi.mock('@/api/upload', () => ({
  uploadDocument: vi.fn(),
}))
vi.mock('@/hooks/use-toast', () => ({
  toast: vi.fn(),
}))

function renderKBDetailPage(kbId = 'kb-1') {
  const queryClient = new QueryClient({
    // staleTime matches main.tsx's production default deliberately: a
    // real bug (a re-evaluation vanishing because fetchQuery treated the
    // mount-time fetch as still fresh and returned stale cached data
    // instead of hitting the network) was invisible under staleTime: 0
    // (react-query's own default, and what this suite used before) —
    // that's a materially different caching behavior than production
    // actually runs under, so tests here need to match it to catch this
    // class of bug at all.
    defaultOptions: { queries: { retry: false, staleTime: 30_000 } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/kbs/${kbId}`]}>
        <Routes>
          <Route path="/kbs/:kbId" element={<KBDetailPage />} />
          <Route path="/kbs" element={<div>Knowledge Bases list</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

// Read by the shared api.GET mock's /kbs/{id}/inquiry handler. Tests that
// need the Inquiry to contain specific turns (e.g. to observe a message
// still rendered after the post-search refetch reconciles it) mutate this
// directly, or via mockSearchStream's thenInquiryMessages side effect,
// which sets it exactly when the mocked SSE fetch is invoked — guaranteed
// to happen before the later refetch that reads it, so there's no race.
let mockInquiryMessages: unknown[] = []

beforeEach(() => {
  vi.clearAllMocks()
  // search-state.ts persists draft queries to sessionStorage (intentionally,
  // for reload continuity) — clear it so one test's typed query never leaks
  // into the next test's fresh render.
  sessionStorage.clear()
  mockInquiryMessages = []
  vi.mocked(api.GET).mockImplementation(((path: string) => {
    if (path === '/kbs/{id}') {
      return Promise.resolve({ data: { id: 'kb-1', name: 'Test KB' }, error: undefined })
    }
    if (path === '/kbs/{kbId}/documents') {
      return Promise.resolve({ data: { items: [] }, error: undefined })
    }
    if (path === '/kbs/{id}/inquiry') {
      return Promise.resolve({
        data: { id: mockInquiryMessages.length ? 'inquiry-1' : null, messages: mockInquiryMessages },
        error: undefined,
      })
    }
    throw new Error(`unexpected api.GET call: ${path}`)
  }) as never)
})

describe('KBDetailPage', () => {
  it('shows a warning against uploading proprietary, sensitive, or confidential content', async () => {
    renderKBDetailPage()
    await screen.findByRole('heading', { name: 'Test KB' })
    expect(screen.getByText(/proprietary, sensitive, or confidential/i)).toBeInTheDocument()
  })

  it('links to the privacy policy from the upload warning', async () => {
    renderKBDetailPage()
    await screen.findByRole('heading', { name: 'Test KB' })
    expect(screen.getByRole('link', { name: /privacy policy/i })).toHaveAttribute('href', '/privacy')
  })

  it('wraps a long, unbroken KB name in the title instead of overflowing horizontally', async () => {
    const longName = 'agenuinelyextremelylongknowledgebasenamewithnodelimitersatall'
    vi.mocked(api.GET).mockImplementation(((path: string) => {
      if (path === '/kbs/{id}') {
        return Promise.resolve({ data: { id: 'kb-1', name: longName }, error: undefined })
      }
      if (path === '/kbs/{kbId}/documents') {
        return Promise.resolve({ data: { items: [] }, error: undefined })
      }
      if (path === '/kbs/{id}/inquiry') {
        return Promise.resolve({ data: { id: null, messages: [] }, error: undefined })
      }
      throw new Error(`unexpected api.GET call: ${path}`)
    }) as never)
    renderKBDetailPage()

    const heading = await screen.findByRole('heading', { name: longName })
    expect(heading.className).toContain('min-w-0')
    expect(heading.className).toContain('wrap-anywhere')
  })

  describe('delete knowledge base', () => {
    it('deletes the knowledge base and navigates back to the list on confirm', async () => {
      vi.mocked(api.DELETE).mockResolvedValue({ error: undefined } as never)

      const user = userEvent.setup()
      renderKBDetailPage()

      await screen.findByRole('heading', { name: 'Test KB' })
      await user.click(screen.getByRole('button', { name: /delete knowledge base/i }))

      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByRole('button', { name: /^delete$/i }))

      await waitFor(() => {
        expect(api.DELETE).toHaveBeenCalledWith('/kbs/{id}', { params: { path: { id: 'kb-1' } } })
      })
      await waitFor(() => {
        expect(screen.getByText('Knowledge Bases list')).toBeInTheDocument()
      })
    })

    it('does not delete when the confirm dialog is cancelled', async () => {
      const user = userEvent.setup()
      renderKBDetailPage()

      await screen.findByRole('heading', { name: 'Test KB' })
      await user.click(screen.getByRole('button', { name: /delete knowledge base/i }))

      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByRole('button', { name: /cancel/i }))

      expect(api.DELETE).not.toHaveBeenCalled()
      expect(screen.getByRole('heading', { name: 'Test KB' })).toBeInTheDocument()
    })
  })

  describe('document upload', () => {
    function getFileInput(container: HTMLElement): HTMLInputElement {
      return container.querySelector('input[type="file"]')!
    }

    it('does not show a toast on a successful upload — the document list already shows status', async () => {
      vi.mocked(uploadDocument).mockResolvedValue({
        id: 'doc-1',
        filename: 'notes.txt',
        status: 'pending',
      } as never)

      const user = userEvent.setup()
      const { container } = renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      const file = new File(['hello'], 'notes.txt', { type: 'text/plain' })
      await user.upload(getFileInput(container), file)

      await screen.findByText('notes.txt')
      expect(toast).not.toHaveBeenCalled()
    })

    it('shows an error toast when an upload fails', async () => {
      vi.mocked(uploadDocument).mockRejectedValue(new Error('upload failed'))

      const user = userEvent.setup()
      const { container } = renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      const file = new File(['hello'], 'notes.txt', { type: 'text/plain' })
      await user.upload(getFileInput(container), file)

      await waitFor(() => {
        expect(toast).toHaveBeenCalledWith(
          expect.objectContaining({ variant: 'destructive', title: 'Upload failed' }),
        )
      })
    })
  })

  describe('duplicate upload warning', () => {
    function getFileInput(container: HTMLElement): HTMLInputElement {
      return container.querySelector('input[type="file"]')!
    }

    function mockDocs(items: unknown[]) {
      vi.mocked(api.GET).mockImplementation(((path: string) => {
        if (path === '/kbs/{id}') {
          return Promise.resolve({ data: { id: 'kb-1', name: 'Test KB' }, error: undefined })
        }
        if (path === '/kbs/{kbId}/documents') {
          return Promise.resolve({ data: { items }, error: undefined })
        }
        if (path === '/kbs/{id}/inquiry') {
          return Promise.resolve({ data: { id: null, messages: [] }, error: undefined })
        }
        throw new Error(`unexpected api.GET call: ${path}`)
      }) as never)
    }

    it('warns before uploading a file whose name already exists in the KB', async () => {
      mockDocs([{ id: 'doc-1', filename: 'notes.txt', status: 'indexed', size_bytes: 10 }])
      const user = userEvent.setup()
      const { container } = renderKBDetailPage()
      await screen.findByText('notes.txt')

      const file = new File(['hello again'], 'notes.txt', { type: 'text/plain' })
      await user.upload(getFileInput(container), file)

      const dialog = await screen.findByRole('dialog')
      expect(within(dialog).getByText(/already exists in this knowledge base/i)).toBeInTheDocument()
      expect(uploadDocument).not.toHaveBeenCalled()
    })

    it('uploads immediately, no warning, when the filename is new', async () => {
      mockDocs([{ id: 'doc-1', filename: 'other.txt', status: 'indexed', size_bytes: 10 }])
      vi.mocked(uploadDocument).mockResolvedValue({
        id: 'doc-2',
        filename: 'notes.txt',
        status: 'pending',
      } as never)

      const user = userEvent.setup()
      const { container } = renderKBDetailPage()
      await screen.findByText('other.txt')

      const file = new File(['hello'], 'notes.txt', { type: 'text/plain' })
      await user.upload(getFileInput(container), file)

      await waitFor(() => expect(uploadDocument).toHaveBeenCalledWith('kb-1', file))
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })

    it('proceeds with the upload when the duplicate warning is confirmed', async () => {
      mockDocs([{ id: 'doc-1', filename: 'notes.txt', status: 'indexed', size_bytes: 10 }])
      vi.mocked(uploadDocument).mockResolvedValue({
        id: 'doc-2',
        filename: 'notes.txt',
        status: 'pending',
      } as never)

      const user = userEvent.setup()
      const { container } = renderKBDetailPage()
      await screen.findByText('notes.txt')

      const file = new File(['hello again'], 'notes.txt', { type: 'text/plain' })
      await user.upload(getFileInput(container), file)

      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByRole('button', { name: /upload anyway/i }))

      await waitFor(() => expect(uploadDocument).toHaveBeenCalledWith('kb-1', file))
    })

    it('does not upload when the duplicate warning is cancelled', async () => {
      mockDocs([{ id: 'doc-1', filename: 'notes.txt', status: 'indexed', size_bytes: 10 }])
      const user = userEvent.setup()
      const { container } = renderKBDetailPage()
      await screen.findByText('notes.txt')

      const file = new File(['hello again'], 'notes.txt', { type: 'text/plain' })
      await user.upload(getFileInput(container), file)

      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByRole('button', { name: /cancel/i }))

      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      expect(uploadDocument).not.toHaveBeenCalled()
    })
  })

  describe('documents panel', () => {
    function mockDocs(items: unknown[]) {
      vi.mocked(api.GET).mockImplementation(((path: string) => {
        if (path === '/kbs/{id}') {
          return Promise.resolve({ data: { id: 'kb-1', name: 'Test KB' }, error: undefined })
        }
        if (path === '/kbs/{kbId}/documents') {
          return Promise.resolve({ data: { items }, error: undefined })
        }
        if (path === '/kbs/{id}/inquiry') {
          return Promise.resolve({ data: { id: null, messages: [] }, error: undefined })
        }
        throw new Error(`unexpected api.GET call: ${path}`)
      }) as never)
    }

    it('shows the file size alongside the file name', async () => {
      mockDocs([
        { id: 'doc-1', filename: 'notes.txt', status: 'indexed', size_bytes: 2048 },
      ])
      renderKBDetailPage()

      await screen.findByText('notes.txt')
      expect(screen.getByText('2.0 KB')).toBeInTheDocument()
    })

    it('wraps long filenames instead of truncating or scrolling', async () => {
      // No spaces or hyphens: a delimited long name would already wrap at
      // those break points under plain CSS defaults, so it wouldn't
      // exercise the actual bug (a flex item's default min-width: auto
      // refusing to shrink below an unbroken string's full width).
      const longName = 'agenuinelyextremelylongfilenamethatwouldotherwiseforcehorizontalscroll.pdf'
      mockDocs([{ id: 'doc-1', filename: longName, status: 'indexed', size_bytes: 10 }])
      renderKBDetailPage()

      const filename = await screen.findByText(longName)
      expect(filename.className).toContain('wrap-anywhere')
      expect(filename.className).not.toContain('truncate')
      // wrap-anywhere alone isn't enough inside a flex row — the flex item
      // ancestor also needs min-w-0, or it never actually shrinks far
      // enough to need to wrap.
      expect(filename.parentElement?.className).toContain('min-w-0')
    })

    it('shows a retry action only for a failed (dead-lettered) document', async () => {
      mockDocs([
        { id: 'doc-1', filename: 'failed.txt', status: 'failed', size_bytes: 10 },
        { id: 'doc-2', filename: 'indexed.txt', status: 'indexed', size_bytes: 10 },
      ])
      renderKBDetailPage()

      await screen.findByText('failed.txt')
      expect(screen.getAllByRole('button', { name: /retry indexing/i })).toHaveLength(1)
    })

    it('retries a failed document and reflects its updated status', async () => {
      mockDocs([{ id: 'doc-1', filename: 'failed.txt', status: 'failed', size_bytes: 10 }])
      vi.mocked(api.POST).mockResolvedValue({
        data: { id: 'doc-1', filename: 'failed.txt', status: 'pending', size_bytes: 10 },
        error: undefined,
      } as never)

      const user = userEvent.setup()
      renderKBDetailPage()

      await screen.findByText('failed.txt')
      await user.click(screen.getByRole('button', { name: /retry indexing/i }))

      expect(api.POST).toHaveBeenCalledWith('/kbs/{kbId}/documents/{docId}/retry', {
        params: { path: { kbId: 'kb-1', docId: 'doc-1' } },
      })
      await waitFor(() => {
        expect(screen.getByText('Pending')).toBeInTheDocument()
      })
    })

    it('surfaces the server message when deleting an in-flight document is rejected', async () => {
      mockDocs([{ id: 'doc-1', filename: 'busy.txt', status: 'processing', size_bytes: 10 }])
      vi.mocked(api.DELETE).mockResolvedValue({
        error: { error: 'document is being indexed and cannot be deleted yet' },
      } as never)

      const user = userEvent.setup()
      renderKBDetailPage()

      await screen.findByText('busy.txt')
      await user.click(screen.getByRole('button', { name: 'Delete' }))
      const dialog = await screen.findByRole('dialog')
      await user.click(within(dialog).getByRole('button', { name: 'Delete' }))

      await waitFor(() => {
        expect(toast).toHaveBeenCalledWith(
          expect.objectContaining({
            variant: 'destructive',
            description: 'document is being indexed and cannot be deleted yet',
          }),
        )
      })
      // The row is untouched — no optimistic removal on a rejected delete.
      expect(screen.getByText('busy.txt')).toBeInTheDocument()
    })
  })

  describe('search', () => {
    function sseFrame(name: string, data: unknown): string {
      return `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`
    }

    // thenInquiryMessages, if given, is written into mockInquiryMessages at
    // the moment the mocked fetch is invoked — i.e. exactly when a search
    // or reevaluate actually starts — so the later post-completion refetch
    // (see runStream's invalidateQueries call) deterministically observes
    // it, with no dependency on real-time scheduling between the two.
    function mockSearchStream(
      frames: string[],
      init: { status?: number; noBody?: boolean } = {},
      thenInquiryMessages?: unknown[],
    ) {
      vi.stubGlobal(
        'fetch',
        vi.fn().mockImplementation(() => {
          if (thenInquiryMessages) mockInquiryMessages = thenInquiryMessages
          const body = init.noBody
            ? null
            : new ReadableStream<Uint8Array>({
                start(controller) {
                  const encoder = new TextEncoder()
                  for (const frame of frames) controller.enqueue(encoder.encode(frame))
                  controller.close()
                },
              })
          return Promise.resolve(new Response(body, { status: init.status ?? 200 }))
        }),
      )
    }

    function userMessage(content: string, id = 'msg-user') {
      return {
        id,
        role: 'user',
        content,
        citations: [],
        retrieved_documents: [],
        supersedes_message_id: null,
        created_at: '2026-01-01T00:00:00Z',
      }
    }

    function assistantMessage(overrides: {
      id?: string
      content?: string
      citations?: unknown[]
      retrieved_documents?: unknown[]
      supersedes_message_id?: string | null
    } = {}) {
      return {
        id: overrides.id ?? 'msg-assistant',
        role: 'assistant',
        content: overrides.content ?? '',
        citations: overrides.citations ?? [],
        retrieved_documents: overrides.retrieved_documents ?? [],
        supersedes_message_id: overrides.supersedes_message_id ?? null,
        created_at: '2026-01-01T00:00:00Z',
      }
    }

    afterEach(() => {
      vi.unstubAllGlobals()
    })

    it('shows a prompt before any search is submitted', async () => {
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })
      expect(screen.getByText(/query this knowledge base above/i)).toBeInTheDocument()
    })

    it('hydrates from persisted Inquiry history on mount, without re-running a search', async () => {
      mockInquiryMessages = [
        { id: 'msg-1', role: 'user', content: 'a past question', citations: [], retrieved_documents: [], supersedes_message_id: null, created_at: '2026-01-01T00:00:00Z' },
        { id: 'msg-2', role: 'assistant', content: 'a past answer', citations: [], retrieved_documents: [], supersedes_message_id: null, created_at: '2026-01-01T00:00:01Z' },
      ]
      vi.stubGlobal('fetch', vi.fn())

      renderKBDetailPage()

      await screen.findByText('a past question')
      expect(screen.getByText('a past answer')).toBeInTheDocument()
      // No search or reevaluate call — history came entirely from the
      // GET /kbs/{id}/inquiry fetch (api.GET, not global fetch).
      expect(fetch).not.toHaveBeenCalled()
    })

    it('submits a query and renders the cited summary', async () => {
      const citations = [
        {
          number: 1,
          document_id: 'doc-1',
          chunk_id: 'chunk-1',
          char_start: 0,
          char_end: 10,
          file_name: 'geo.txt',
          locator: null,
        },
      ]
      mockSearchStream(
        [
          sseFrame('retrieved_files', { retrieved_files: [{ document_id: 'doc-1', file_name: 'geo.txt' }] }),
          sseFrame('delta', { text: 'Paris is the capital of France [1].' }),
          sseFrame('done', { summary: 'Paris is the capital of France [1].', citations }),
        ],
        {},
        [
          userMessage('What is the capital of France?'),
          assistantMessage({ content: 'Paris is the capital of France [1].', citations }),
        ],
      )

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'What is the capital of France?')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => {
        expect(screen.getByText(/paris is the capital of france/i)).toBeInTheDocument()
      })
      // The citation marker now renders the file name inline (not "[1]"),
      // so "geo.txt" appears twice on the page: once as the inline citation
      // marker, once in the relevant-files list. Assert the marker
      // specifically by tag, since a bare getByText would be ambiguous.
      const markers = screen.getAllByText('geo.txt').filter((el) => el.tagName === 'SUP')
      expect(markers).toHaveLength(1)
      expect(fetch).toHaveBeenCalledWith(
        expect.stringContaining('/kbs/kb-1/search'),
        expect.objectContaining({ body: JSON.stringify({ query: 'What is the capital of France?' }) }),
      )
    })

    it('renders the answer incrementally as delta events arrive, before the done event', async () => {
      let resolveSecondDelta: () => void = () => {}
      const secondDeltaGate = new Promise<void>((resolve) => {
        resolveSecondDelta = resolve
      })
      const body = new ReadableStream<Uint8Array>({
        async start(controller) {
          const encoder = new TextEncoder()
          controller.enqueue(
            encoder.encode(
              sseFrame('retrieved_files', { retrieved_files: [{ document_id: 'doc-1', file_name: 'geo.txt' }] }),
            ),
          )
          controller.enqueue(encoder.encode(sseFrame('delta', { text: 'Partial answer only' })))
          await secondDeltaGate
          controller.enqueue(
            encoder.encode(sseFrame('done', { summary: 'Partial answer only, complete.', citations: [] })),
          )
          controller.close()
        },
      })
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, { status: 200 })))

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'a question')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => {
        expect(screen.getByText('Partial answer only')).toBeInTheDocument()
      })

      mockInquiryMessages = [userMessage('a question'), assistantMessage({ content: 'Partial answer only, complete.' })]
      resolveSecondDelta()
      await waitFor(() => {
        expect(screen.getByText('Partial answer only, complete.')).toBeInTheDocument()
      })
    })

    it('lists relevant files under the answer in RRF-fused rank order, not re-sorted', async () => {
      // Deliberately NOT cited-first: history.txt ranked higher by
      // retrieval but never ends up cited in the answer, while geo.txt
      // ranks lower but IS cited. If the frontend re-sorted (e.g. cited
      // files first) instead of rendering retrieved_files as received,
      // this order would flip and the test below would catch it.
      const retrievedFiles = [
        { document_id: 'doc-2', file_name: 'history.txt' },
        { document_id: 'doc-1', file_name: 'geo.txt' },
      ]
      const citations = [
        {
          number: 1,
          document_id: 'doc-1',
          chunk_id: 'chunk-1',
          char_start: 0,
          char_end: 10,
          file_name: 'geo.txt',
          locator: null,
        },
      ]
      mockSearchStream(
        [
          sseFrame('retrieved_files', { retrieved_files: retrievedFiles }),
          sseFrame('delta', { text: 'Paris is the capital of France [1].' }),
          sseFrame('done', { summary: 'Paris is the capital of France [1].', citations }),
        ],
        {},
        [
          userMessage('What is the capital of France?'),
          assistantMessage({
            content: 'Paris is the capital of France [1].',
            citations,
            retrieved_documents: retrievedFiles,
          }),
        ],
      )

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'What is the capital of France?')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      // "geo.txt" now also renders as the inline citation marker (not
      // "[1]"), so wait on the relevant-files list itself rather than an
      // ambiguous text match.
      await waitFor(() => expect(screen.getAllByRole('listitem')).toHaveLength(2))
      const items = screen.getAllByRole('listitem').map((li) => li.textContent)
      expect(items).toEqual(['history.txt', 'geo.txt'])
      expect(screen.getByText(/ranked most to least relevant/i)).toBeInTheDocument()
    })

    it('shows the cited page numbers next to a relevant file, aggregated from its citations', async () => {
      const retrievedFiles = [
        { document_id: 'doc-1', file_name: 'report.pdf' },
        { document_id: 'doc-2', file_name: 'uncited.txt' },
      ]
      const citations = [
        {
          number: 1,
          document_id: 'doc-1',
          chunk_id: 'chunk-1',
          char_start: 0,
          char_end: 10,
          file_name: 'report.pdf',
          locator: { type: 'page', value: 12 },
        },
        // Same file, a different (and out-of-order, to check sorting)
        // page — and a duplicate of citation 1's page, to check dedup.
        {
          number: 2,
          document_id: 'doc-1',
          chunk_id: 'chunk-2',
          char_start: 0,
          char_end: 10,
          file_name: 'report.pdf',
          locator: { type: 'page', value: 3 },
        },
      ]
      mockSearchStream(
        [
          sseFrame('retrieved_files', { retrieved_files: retrievedFiles }),
          sseFrame('delta', { text: 'The findings span several pages [1][2].' }),
          sseFrame('done', { summary: 'The findings span several pages [1][2].', citations }),
        ],
        {},
        [
          userMessage('what does the report say?'),
          assistantMessage({
            content: 'The findings span several pages [1][2].',
            citations,
            retrieved_documents: retrievedFiles,
          }),
        ],
      )

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'what does the report say?')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => expect(screen.getByText('uncited.txt')).toBeInTheDocument())
      // Sorted ascending and deduped, not insertion order (12 then 3 in the
      // citations array above).
      expect(screen.getByText(/p\.\s*3,\s*12/)).toBeInTheDocument()
      // A relevant-but-never-cited file has no locator to show.
      expect(screen.getByText('uncited.txt').textContent).toBe('uncited.txt')
    })

    it('shows the not-found summary and an empty relevant-files state when retrieval found nothing', async () => {
      mockSearchStream(
        [
          sseFrame('retrieved_files', { retrieved_files: [] }),
          sseFrame('delta', { text: 'I could not find relevant information to answer this question.' }),
          sseFrame('done', { summary: 'I could not find relevant information to answer this question.', citations: [] }),
        ],
        {},
        [
          userMessage('unanswerable question'),
          assistantMessage({ content: 'I could not find relevant information to answer this question.' }),
        ],
      )

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'unanswerable question')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => {
        expect(screen.getByText(/could not find relevant information/i)).toBeInTheDocument()
      })
      expect(screen.getByText(/no relevant files found/i)).toBeInTheDocument()
    })

    it('shows retrieval timing while a search is streaming, before it completes', async () => {
      let releaseDone: () => void = () => {}
      const gate = new Promise<void>((resolve) => {
        releaseDone = resolve
      })
      const body = new ReadableStream<Uint8Array>({
        async start(controller) {
          const encoder = new TextEncoder()
          controller.enqueue(encoder.encode(sseFrame('retrieved_files', { retrieved_files: [] })))
          await gate
          controller.enqueue(encoder.encode(sseFrame('done', { summary: 'the answer', citations: [] })))
          controller.close()
        },
      })
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, { status: 200 })))

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'a question')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => expect(screen.getByText(/found 0 files in \d+ms/i)).toBeInTheDocument())
      releaseDone()
    })

    it('shows placeholder text before the retrieved_files and done events arrive', async () => {
      let releaseStream: () => void = () => {}
      const gate = new Promise<void>((resolve) => {
        releaseStream = resolve
      })
      const body = new ReadableStream<Uint8Array>({
        async start(controller) {
          await gate
          controller.enqueue(new TextEncoder().encode(sseFrame('retrieved_files', { retrieved_files: [] })))
          controller.enqueue(
            new TextEncoder().encode(sseFrame('done', { summary: 'the answer', citations: [] })),
          )
          controller.close()
        },
      })
      vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, { status: 200 })))

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'a question')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => expect(screen.getByText(/awaiting llm summarization/i)).toBeInTheDocument())
      expect(screen.getByText(/finding relevant files/i)).toBeInTheDocument()

      mockInquiryMessages = [userMessage('a question'), assistantMessage({ content: 'the answer' })]
      releaseStream()
      await waitFor(() => expect(screen.getByText('the answer')).toBeInTheDocument())
    })

    it('shows an error state when the search request fails outright', async () => {
      mockSearchStream([], { status: 500 })

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'broken query')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => {
        expect(screen.getByText(/search failed/i)).toBeInTheDocument()
      })
    })

    it('shows the server error message when the stream fails mid-answer', async () => {
      mockSearchStream([
        sseFrame('retrieved_files', { retrieved_files: [] }),
        sseFrame('delta', { text: 'partial' }),
        sseFrame('error', { error: 'generation failed' }),
      ])

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'a question')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => {
        expect(screen.getByText(/generation failed/i)).toBeInTheDocument()
      })
    })

    it('clears the input after submitting, and re-submitting the same text again starts a new turn', async () => {
      mockSearchStream(
        [
          sseFrame('retrieved_files', { retrieved_files: [{ document_id: 'doc-1', file_name: 'geo.txt' }] }),
          sseFrame('done', { summary: 'first answer', citations: [] }),
        ],
        {},
        [userMessage('same question', 'msg-1'), assistantMessage({ id: 'msg-2', content: 'first answer' })],
      )

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'same question')
      await user.click(screen.getByRole('button', { name: /^query$/i }))
      await waitFor(() => expect(screen.getByText('first answer')).toBeInTheDocument())

      expect(fetch).toHaveBeenCalledTimes(1)
      expect(screen.getByRole('textbox')).toHaveValue('')

      // Retyping and resubmitting the identical text must genuinely re-run,
      // not silently no-op just because the string is unchanged — there's
      // no cached-on-query-text mechanism standing in the way of that.
      mockSearchStream(
        [sseFrame('retrieved_files', { retrieved_files: [] }), sseFrame('done', { summary: 'second answer', citations: [] })],
        {},
        [
          userMessage('same question', 'msg-1'),
          assistantMessage({ id: 'msg-2', content: 'first answer' }),
          userMessage('same question', 'msg-3'),
          assistantMessage({ id: 'msg-4', content: 'second answer' }),
        ],
      )
      await user.type(screen.getByRole('textbox'), 'same question')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => expect(screen.getByText('second answer')).toBeInTheDocument())
      expect(fetch).toHaveBeenCalledTimes(1) // mockSearchStream's second call replaced the stub; count resets
    })

    // Regression test: a real bug found while manually testing this against
    // a dev database missing the Inquiry migration. Persistence failing
    // server-side made the post-completion GET /kbs/{id}/inquiry refetch
    // 500, and the original code cleared the completed answer unconditionally
    // once that refetch attempt merely finished — regardless of whether it
    // had actually succeeded — discarding an answer the researcher had just
    // watched stream in successfully.
    it('keeps showing the completed answer if the post-completion refetch fails', async () => {
      let inquiryCalls = 0
      vi.mocked(api.GET).mockImplementation(((path: string) => {
        if (path === '/kbs/{id}') {
          return Promise.resolve({ data: { id: 'kb-1', name: 'Test KB' }, error: undefined })
        }
        if (path === '/kbs/{kbId}/documents') {
          return Promise.resolve({ data: { items: [] }, error: undefined })
        }
        if (path === '/kbs/{id}/inquiry') {
          inquiryCalls++
          // First call: the initial mount fetch, succeeds empty. Second
          // call: the post-search refetch, fails — e.g. persistence didn't
          // actually happen server-side (a missing migration, in the bug
          // this reproduces).
          if (inquiryCalls === 1) {
            return Promise.resolve({ data: { id: null, messages: [] }, error: undefined })
          }
          return Promise.resolve({ data: undefined, error: { error: 'internal error' }, response: { status: 500 } })
        }
        throw new Error(`unexpected api.GET call: ${path}`)
      }) as never)
      mockSearchStream([sseFrame('done', { summary: 'the answer', citations: [] })])

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'a question')
      await user.click(screen.getByRole('button', { name: /^query$/i }))

      await waitFor(() => expect(screen.getByText('the answer')).toBeInTheDocument())
      // The bug: this text would vanish moments later once the failed
      // refetch "completed" and pending was cleared unconditionally.
      await new Promise((r) => setTimeout(r, 50))
      expect(screen.getByText('the answer')).toBeInTheDocument()
    })

    describe('re-evaluate', () => {
      it('re-runs the original query and appends the result, marked as re-evaluated, without touching the original', async () => {
        mockInquiryMessages = [
          userMessage('what is the capital of France?', 'msg-1'),
          assistantMessage({ id: 'msg-2', content: 'Paris is the capital.' }),
        ]
        vi.stubGlobal('fetch', vi.fn())

        const user = userEvent.setup()
        renderKBDetailPage()
        await screen.findByText('Paris is the capital.')

        mockSearchStream(
          [sseFrame('done', { summary: 'Paris remains the capital.', citations: [] })],
          {},
          [
            userMessage('what is the capital of France?', 'msg-1'),
            assistantMessage({ id: 'msg-2', content: 'Paris is the capital.' }),
            assistantMessage({ id: 'msg-3', content: 'Paris remains the capital.', supersedes_message_id: 'msg-2' }),
          ],
        )
        await user.click(screen.getByRole('button', { name: /re-evaluate query/i }))

        expect(fetch).toHaveBeenCalledWith(
          expect.stringContaining('/kbs/kb-1/inquiry/messages/msg-2/reevaluate'),
          expect.objectContaining({ method: 'POST' }),
        )
        await waitFor(() => expect(screen.getByText('Paris remains the capital.')).toBeInTheDocument())
        // The original answer isn't overwritten — it's collapsed into
        // history behind a caret, not shown inline, but still there and
        // reachable, and the current answer is tagged as such.
        expect(screen.queryByText('Paris is the capital.')).not.toBeInTheDocument()
        expect(screen.getByText('Current')).toBeInTheDocument()
        // msg-2 is the original answer, not itself a re-evaluation of
        // anything — its collapsed label should say "Evaluated", not
        // "Re-evaluated".
        const historyToggle = screen.getByText(/^evaluated ·/i)
        await user.click(historyToggle)
        expect(await screen.findByText('Paris is the capital.')).toBeInTheDocument()
      })

      it('groups a re-evaluation under the query it re-answers, not whichever query is nearby', async () => {
        // Two independent turns; only the first one's answer gets
        // re-evaluated. A grouping bug (e.g. by array position instead of
        // supersedes_message_id) would attach the re-evaluation to the
        // second turn instead, or show it detached from both.
        mockInquiryMessages = [
          userMessage('what is the capital of France?', 'msg-1'),
          assistantMessage({ id: 'msg-2', content: 'Paris is the capital.' }),
          userMessage('what is the capital of Japan?', 'msg-3'),
          assistantMessage({ id: 'msg-4', content: 'Tokyo is the capital.' }),
        ]
        renderKBDetailPage()
        await screen.findByText('Tokyo is the capital.')

        mockSearchStream(
          [sseFrame('done', { summary: 'Paris remains the capital.', citations: [] })],
          {},
          [
            userMessage('what is the capital of France?', 'msg-1'),
            assistantMessage({ id: 'msg-2', content: 'Paris is the capital.' }),
            userMessage('what is the capital of Japan?', 'msg-3'),
            assistantMessage({ id: 'msg-4', content: 'Tokyo is the capital.' }),
            assistantMessage({ id: 'msg-5', content: 'Paris remains the capital.', supersedes_message_id: 'msg-2' }),
          ],
        )
        const user = userEvent.setup()
        const reevaluateButtons = screen.getAllByRole('button', { name: /re-evaluate query/i })
        // The France turn's button is the first one rendered (turns render
        // in original-query order).
        await user.click(reevaluateButtons[0])

        await waitFor(() => expect(screen.getByText('Paris remains the capital.')).toBeInTheDocument())

        // Both France answers are grouped together...
        const franceQuery = screen.getByText('what is the capital of France?')
        const franceGroup = franceQuery.parentElement!
        expect(within(franceGroup).getByText('Paris remains the capital.')).toBeInTheDocument()
        // msg-2 is the original answer, not itself a re-evaluation.
        await user.click(within(franceGroup).getByText(/^evaluated ·/i))
        expect(await within(franceGroup).findByText('Paris is the capital.')).toBeInTheDocument()
        // ...and the Japan turn is untouched by it.
        const japanQuery = screen.getByText('what is the capital of Japan?')
        const japanGroup = japanQuery.parentElement!
        expect(within(japanGroup).queryByText('Paris remains the capital.')).not.toBeInTheDocument()
      })

      it('shows the re-evaluation streaming in directly under the message being re-answered', async () => {
        mockInquiryMessages = [
          userMessage('a question', 'msg-1'),
          assistantMessage({ id: 'msg-2', content: 'an answer' }),
        ]
        let releaseDone: () => void = () => {}
        const gate = new Promise<void>((resolve) => {
          releaseDone = resolve
        })
        const body = new ReadableStream<Uint8Array>({
          async start(controller) {
            controller.enqueue(new TextEncoder().encode(sseFrame('delta', { text: 'partial re-eval' })))
            await gate
            controller.enqueue(new TextEncoder().encode(sseFrame('done', { summary: 'partial re-eval, complete', citations: [] })))
            controller.close()
          },
        })
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, { status: 200 })))

        const user = userEvent.setup()
        renderKBDetailPage()
        await screen.findByText('an answer')

        await user.click(screen.getByRole('button', { name: /re-evaluate query/i }))

        await waitFor(() => expect(screen.getByText('partial re-eval')).toBeInTheDocument())
        // Still mid-stream: the original answer is untouched and the
        // in-progress re-evaluation renders right after it, not replacing it.
        expect(screen.getByText('an answer')).toBeInTheDocument()
        releaseDone()
      })

      it('shows the newest answer expanded and un-indented, with older answers collapsed below it, most-recently-superseded first', async () => {
        mockInquiryMessages = [
          userMessage('what changed?', 'msg-1'),
          assistantMessage({ id: 'msg-2', content: 'first answer' }),
          assistantMessage({ id: 'msg-3', content: 'second answer', supersedes_message_id: 'msg-2' }),
          assistantMessage({ id: 'msg-4', content: 'third answer', supersedes_message_id: 'msg-3' }),
        ]
        renderKBDetailPage()

        // The newest answer is immediately visible, no expansion needed —
        // and, since it's itself a re-evaluation (msg-4 supersedes msg-3),
        // its always-visible meta line says so, consistent with history.
        // (msg-3's collapsed history toggle also reads "Re-evaluated ·",
        // so there are legitimately two matches — the current one is the
        // one that isn't a button.)
        await screen.findByText('third answer')
        const currentMeta = screen.getAllByText(/^re-evaluated ·/i).find((el) => el.closest('button') === null)
        expect(currentMeta).toBeTruthy()
        // The two older ones are collapsed — not in the document at all
        // until their toggle is clicked.
        expect(screen.queryByText('first answer')).not.toBeInTheDocument()
        expect(screen.queryByText('second answer')).not.toBeInTheDocument()

        const user = userEvent.setup()
        // Most-recently-superseded first: msg-3 ("second answer", labeled
        // "Re-evaluated" — it superseded msg-2) toggles before msg-2
        // ("first answer", labeled "Evaluated" — the original, not itself
        // a re-evaluation of anything).
        const historyToggles = screen.getAllByRole('button', { name: /evaluated ·/i })
        expect(historyToggles).toHaveLength(2)
        expect(historyToggles[0]).toHaveAccessibleName(/^re-evaluated ·/i)
        expect(historyToggles[1]).toHaveAccessibleName(/^evaluated ·/i)

        await user.click(historyToggles[0])
        expect(await screen.findByText('second answer')).toBeInTheDocument()
        expect(screen.queryByText('first answer')).not.toBeInTheDocument()

        await user.click(historyToggles[1])
        expect(await screen.findByText('first answer')).toBeInTheDocument()
      })
    })
  })

  describe('stale or deleted knowledge base', () => {
    it('redirects to the KB list when the KB fetch 404s', async () => {
      vi.mocked(api.GET).mockImplementation(((path: string) => {
        if (path === '/kbs/{id}') {
          return Promise.resolve({
            data: undefined,
            error: { error: 'not found' },
            response: { status: 404 },
          })
        }
        if (path === '/kbs/{kbId}/documents') {
          return Promise.resolve({ data: { items: [] }, error: undefined })
        }
        if (path === '/kbs/{id}/inquiry') {
          return Promise.resolve({ data: { id: null, messages: [] }, error: undefined })
        }
        throw new Error(`unexpected api.GET call: ${path}`)
      }) as never)

      renderKBDetailPage()

      await waitFor(() => {
        expect(screen.getByText('Knowledge Bases list')).toBeInTheDocument()
      })
    })
  })

  describe('mobile layout order', () => {
    it('places the upload/documents column after the search section, not before', async () => {
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      const aside = document.querySelector('aside')
      expect(aside).not.toBeNull()
      expect(aside?.className).toContain('order-last')
      expect(aside?.className).not.toContain('order-first')
    })
  })
})
