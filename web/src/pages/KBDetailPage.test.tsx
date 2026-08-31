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
    defaultOptions: { queries: { retry: false } },
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

beforeEach(() => {
  vi.clearAllMocks()
  // search-state.ts persists draft/submitted queries to sessionStorage
  // (intentionally, for reload continuity) — clear it so one test's typed
  // or submitted query never leaks into the next test's fresh render.
  sessionStorage.clear()
  vi.mocked(api.GET).mockImplementation(((path: string) => {
    if (path === '/kbs/{id}') {
      return Promise.resolve({ data: { id: 'kb-1', name: 'Test KB' }, error: undefined })
    }
    if (path === '/kbs/{kbId}/documents') {
      return Promise.resolve({ data: { items: [] }, error: undefined })
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

    function mockSearchStream(frames: string[], init: { status?: number; noBody?: boolean } = {}) {
      // A fresh Response (and fresh ReadableStream) per call: a stream's
      // body can only be read once, but this mock may be invoked more than
      // once per test — e.g. a leftover submitted query restored from
      // sessionStorage on mount fires its own search before the test's own
      // user.click() does. mockResolvedValue would hand out the same
      // already-drained stream to that second call; mockImplementation
      // rebuilds it every time, like a real server would.
      vi.stubGlobal(
        'fetch',
        vi.fn().mockImplementation(() => {
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

    afterEach(() => {
      vi.unstubAllGlobals()
    })

    it('shows a prompt before any search is submitted', async () => {
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })
      expect(screen.getByText(/ask a question/i)).toBeInTheDocument()
    })

    it('submits a query and renders the cited summary', async () => {
      mockSearchStream([
        sseFrame('retrieved_files', { retrieved_files: [{ document_id: 'doc-1', file_name: 'geo.txt' }] }),
        sseFrame('delta', { text: 'Paris is the capital of France [1].' }),
        sseFrame('done', {
          summary: 'Paris is the capital of France [1].',
          citations: [
            {
              number: 1,
              document_id: 'doc-1',
              chunk_id: 'chunk-1',
              char_start: 0,
              char_end: 10,
              file_name: 'geo.txt',
              locator: null,
            },
          ],
        }),
      ])

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'What is the capital of France?')
      await user.click(screen.getByRole('button', { name: /^search$/i }))

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
      await user.click(screen.getByRole('button', { name: /^search$/i }))

      await waitFor(() => {
        expect(screen.getByText('Partial answer only')).toBeInTheDocument()
      })

      resolveSecondDelta()
      await waitFor(() => {
        expect(screen.getByText('Partial answer only, complete.')).toBeInTheDocument()
      })
    })

    it('lists relevant files under the answer in RRF-fused rank order, not re-sorted', async () => {
      mockSearchStream([
        sseFrame('retrieved_files', {
          // Deliberately NOT cited-first: history.txt ranked higher by
          // retrieval but never ends up cited in the answer, while geo.txt
          // ranks lower but IS cited. If the frontend re-sorted (e.g. cited
          // files first) instead of rendering retrieved_files as received,
          // this order would flip and the test below would catch it.
          retrieved_files: [
            { document_id: 'doc-2', file_name: 'history.txt' },
            { document_id: 'doc-1', file_name: 'geo.txt' },
          ],
        }),
        sseFrame('delta', { text: 'Paris is the capital of France [1].' }),
        sseFrame('done', {
          summary: 'Paris is the capital of France [1].',
          citations: [
            {
              number: 1,
              document_id: 'doc-1',
              chunk_id: 'chunk-1',
              char_start: 0,
              char_end: 10,
              file_name: 'geo.txt',
              locator: null,
            },
          ],
        }),
      ])

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'What is the capital of France?')
      await user.click(screen.getByRole('button', { name: /^search$/i }))

      // "geo.txt" now also renders as the inline citation marker (not
      // "[1]"), so wait on the relevant-files list itself rather than an
      // ambiguous text match.
      await waitFor(() => expect(screen.getAllByRole('listitem')).toHaveLength(2))
      const items = screen.getAllByRole('listitem').map((li) => li.textContent)
      expect(items).toEqual(['history.txt', 'geo.txt'])
      expect(screen.getByText(/ranked most to least relevant/i)).toBeInTheDocument()
    })

    it('shows the cited page numbers next to a relevant file, aggregated from its citations', async () => {
      mockSearchStream([
        sseFrame('retrieved_files', {
          retrieved_files: [
            { document_id: 'doc-1', file_name: 'report.pdf' },
            { document_id: 'doc-2', file_name: 'uncited.txt' },
          ],
        }),
        sseFrame('delta', { text: 'The findings span several pages [1][2].' }),
        sseFrame('done', {
          summary: 'The findings span several pages [1][2].',
          citations: [
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
          ],
        }),
      ])

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'what does the report say?')
      await user.click(screen.getByRole('button', { name: /^search$/i }))

      await waitFor(() => expect(screen.getByText('uncited.txt')).toBeInTheDocument())
      // Sorted ascending and deduped, not insertion order (12 then 3 in the
      // citations array above).
      expect(screen.getByText(/p\.\s*3,\s*12/)).toBeInTheDocument()
      // A relevant-but-never-cited file has no locator to show.
      expect(screen.getByText('uncited.txt').textContent).toBe('uncited.txt')
    })

    it('shows the not-found summary and an empty relevant-files state when retrieval found nothing', async () => {
      mockSearchStream([
        sseFrame('retrieved_files', { retrieved_files: [] }),
        sseFrame('delta', { text: 'I could not find relevant information to answer this question.' }),
        sseFrame('done', {
          summary: 'I could not find relevant information to answer this question.',
          citations: [],
        }),
      ])

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'unanswerable question')
      await user.click(screen.getByRole('button', { name: /^search$/i }))

      await waitFor(() => {
        expect(screen.getByText(/could not find relevant information/i)).toBeInTheDocument()
      })
      expect(screen.getByText(/no relevant files found/i)).toBeInTheDocument()
      expect(screen.getByText(/found 0 files in \d+ms/i)).toBeInTheDocument()
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
      await user.click(screen.getByRole('button', { name: /^search$/i }))

      await waitFor(() => expect(screen.getByText(/awaiting llm summarization/i)).toBeInTheDocument())
      expect(screen.getByText(/finding relevant files/i)).toBeInTheDocument()

      releaseStream()
      await waitFor(() => expect(screen.getByText('the answer')).toBeInTheDocument())
    })

    it('shows an error state when the search request fails outright', async () => {
      mockSearchStream([], { status: 500 })

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'broken query')
      await user.click(screen.getByRole('button', { name: /^search$/i }))

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
      await user.click(screen.getByRole('button', { name: /^search$/i }))

      await waitFor(() => {
        expect(screen.getByText(/generation failed/i)).toBeInTheDocument()
      })
    })

    it('re-runs the search when Search is clicked again with an unchanged query', async () => {
      mockSearchStream([
        sseFrame('retrieved_files', { retrieved_files: [{ document_id: 'doc-1', file_name: 'geo.txt' }] }),
        sseFrame('done', { summary: 'first answer', citations: [] }),
      ])

      const user = userEvent.setup()
      renderKBDetailPage()
      await screen.findByRole('heading', { name: 'Test KB' })

      await user.type(screen.getByRole('textbox'), 'same question')
      await user.click(screen.getByRole('button', { name: /^search$/i }))
      await waitFor(() => expect(screen.getByText('first answer')).toBeInTheDocument())

      expect(fetch).toHaveBeenCalledTimes(1)

      // Same query text, clicked again — must actually re-run, not silently
      // no-op just because the submitted string didn't change.
      await user.click(screen.getByRole('button', { name: /^search$/i }))

      await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2))
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
