import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CitationMarker } from './CitationMarker'
import type { components } from '@/api/schema.d.ts'

type Citation = components['schemas']['Citation']

const notes: Citation = {
  number: 1,
  document_id: 'doc-1',
  chunk_id: 'chunk-1',
  char_start: 0,
  char_end: 10,
  file_name: 'notes.txt',
  locator: null,
}

const paperPage4: Citation = {
  number: 2,
  document_id: 'doc-2',
  chunk_id: 'chunk-2',
  char_start: 0,
  char_end: 10,
  file_name: 'paper.pdf',
  locator: { type: 'page', value: 4 },
}

describe('CitationMarker', () => {
  it('renders the file name inline for a single citation, with no +k badge', () => {
    render(<CitationMarker citations={[notes]} />)
    expect(screen.getByText('notes.txt')).toBeInTheDocument()
    expect(screen.queryByText(/\+\d/)).not.toBeInTheDocument()
  })

  it('shows the file name and page locator in the popover for a single PDF citation', async () => {
    const user = userEvent.setup()
    render(<CitationMarker citations={[paperPage4]} />)

    await user.click(screen.getByText('paper.pdf'))

    expect(await screen.findByText(/p\.\s*4/i)).toBeInTheDocument()
  })

  it('shows a "+k" badge counting distinct additional files, not raw citation count', () => {
    render(<CitationMarker citations={[notes, paperPage4]} />)
    expect(screen.getByText('notes.txt +1')).toBeInTheDocument()
  })

  it('does not double-count two citations from the same file as +1', () => {
    const paperPage12: Citation = { ...paperPage4, number: 3, locator: { type: 'page', value: 12 } }
    render(<CitationMarker citations={[paperPage4, paperPage12]} />)

    expect(screen.getByText('paper.pdf')).toBeInTheDocument()
    expect(screen.queryByText(/\+\d/)).not.toBeInTheDocument()
  })

  it('lists every distinct page for a repeated file in the popover, deduped and sorted', async () => {
    const paperPage12: Citation = { ...paperPage4, number: 3, locator: { type: 'page', value: 12 } }
    const paperPage4Again: Citation = { ...paperPage4, number: 4 }
    const user = userEvent.setup()
    render(<CitationMarker citations={[paperPage12, paperPage4, paperPage4Again]} />)

    await user.click(screen.getByText('paper.pdf'))

    expect(await screen.findByText(/p\.\s*4,\s*12/)).toBeInTheDocument()
  })

  it('lists every file in the group within the popover', async () => {
    const user = userEvent.setup()
    render(<CitationMarker citations={[notes, paperPage4]} />)

    await user.click(screen.getByText('notes.txt +1'))

    expect(await screen.findByText('notes.txt')).toBeInTheDocument()
    expect(screen.getByText('paper.pdf')).toBeInTheDocument()
    expect(screen.getByText(/p\.\s*4/i)).toBeInTheDocument()
  })

  it('never shows chunk text in the popover', async () => {
    const user = userEvent.setup()
    render(<CitationMarker citations={[notes]} />)

    await user.click(screen.getByText('notes.txt'))

    expect(screen.queryByRole('button', { name: /show full page text/i })).not.toBeInTheDocument()
  })

  describe('keyboard accessibility', () => {
    it('is a real tab stop', () => {
      render(<CitationMarker citations={[notes]} />)
      expect(screen.getByText('notes.txt')).toHaveAttribute('tabIndex', '0')
    })

    it('opens the popover on Enter when focused, not just on click', async () => {
      const user = userEvent.setup()
      render(<CitationMarker citations={[paperPage4]} />)

      screen.getByText('paper.pdf').focus()
      await user.keyboard('{Enter}')

      expect(await screen.findByText(/p\.\s*4/i)).toBeInTheDocument()
    })

    it('opens the popover on Space when focused', async () => {
      const user = userEvent.setup()
      render(<CitationMarker citations={[paperPage4]} />)

      screen.getByText('paper.pdf').focus()
      await user.keyboard(' ')

      expect(await screen.findByText(/p\.\s*4/i)).toBeInTheDocument()
    })
  })
})
