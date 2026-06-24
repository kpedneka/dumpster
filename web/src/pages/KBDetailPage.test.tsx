import { describe, it, expect, vi, beforeEach } from 'vitest'
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
  it('shows a warning against uploading proprietary or confidential content', async () => {
    renderKBDetailPage()
    await screen.findByRole('heading', { name: 'Test KB' })
    expect(screen.getByText(/proprietary or confidential/i)).toBeInTheDocument()
  })

  describe('delete knowledge base', () => {
    it('deletes the knowledge base and navigates back to the list on confirm', async () => {
      vi.mocked(api.DELETE).mockResolvedValue({ error: undefined } as never)

      const user = userEvent.setup()
      renderKBDetailPage()

      await screen.findByRole('heading', { name: 'Test KB' })
      await user.click(screen.getByRole('button', { name: /knowledge base options/i }))
      await user.click(screen.getByRole('menuitem', { name: /delete knowledge base/i }))

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
      await user.click(screen.getByRole('button', { name: /knowledge base options/i }))
      await user.click(screen.getByRole('menuitem', { name: /delete knowledge base/i }))

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
})
