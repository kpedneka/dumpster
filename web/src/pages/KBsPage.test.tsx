import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { KBsPage } from './KBsPage'
import { api } from '@/api/client'

vi.mock('@/api/client', () => ({
  api: { GET: vi.fn(), POST: vi.fn(), DELETE: vi.fn() },
}))

function renderKBsPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={['/kbs']}>
        <KBsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(api.GET).mockResolvedValue({
    data: { items: [{ id: 'kb-1', name: 'Test KB' }], next_cursor: null },
    error: undefined,
  } as never)
})

describe('KBsPage', () => {
  it('deletes a knowledge base from the list', async () => {
    vi.mocked(api.DELETE).mockResolvedValue({ error: undefined } as never)

    const user = userEvent.setup()
    renderKBsPage()

    await screen.findByText('Test KB')
    await user.click(screen.getByRole('button', { name: /kb options/i }))
    await user.click(screen.getByRole('menuitem', { name: /delete/i }))

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /^delete$/i }))

    await waitFor(() => {
      expect(api.DELETE).toHaveBeenCalledWith('/kbs/{id}', { params: { path: { id: 'kb-1' } } })
    })
  })

  it('wraps a long, unbroken KB name instead of overflowing horizontally', async () => {
    const longName = 'agenuinelyextremelylongknowledgebasenamewithnodelimitersatall'
    vi.mocked(api.GET).mockResolvedValue({
      data: { items: [{ id: 'kb-1', name: longName }], next_cursor: null },
      error: undefined,
    } as never)
    renderKBsPage()

    const name = await screen.findByText(longName)
    expect(name.className).toContain('min-w-0')
    expect(name.className).toContain('truncate')
  })
})
