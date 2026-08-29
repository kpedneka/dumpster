import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CitationMarker } from './CitationMarker'
import type { components } from '@/api/schema.d.ts'

type Citation = components['schemas']['Citation']

const baseCitation: Citation = {
  number: 1,
  document_id: 'doc-1',
  chunk_id: 'chunk-1',
  char_start: 0,
  char_end: 10,
  text: 'the exact source text',
  file_name: 'notes.txt',
  locator: null,
}

describe('CitationMarker', () => {
  it('renders a compact numbered marker inline', () => {
    render(<CitationMarker citation={baseCitation} />)
    expect(screen.getByText('[1]')).toBeInTheDocument()
  })

  it('shows the file name with no locator for a plain-text citation', async () => {
    const user = userEvent.setup()
    render(<CitationMarker citation={baseCitation} />)

    await user.click(screen.getByText('[1]'))

    expect(await screen.findByText('notes.txt')).toBeInTheDocument()
    expect(screen.getByText('the exact source text')).toBeInTheDocument()
  })

  it('shows the file name with a page locator for a PDF citation', async () => {
    const user = userEvent.setup()
    const citation: Citation = {
      ...baseCitation,
      file_name: 'paper.pdf',
      locator: { type: 'page', value: 4 },
    }
    render(<CitationMarker citation={citation} />)

    await user.click(screen.getByText('[1]'))

    expect(await screen.findByText('paper.pdf')).toBeInTheDocument()
    expect(screen.getByText(/p\.\s*4/i)).toBeInTheDocument()
  })

  const longPageText = 'Paris is the capital of France. '.repeat(20).trim()

  it('truncates long chunk text behind an expand control for a locator-bearing (PDF) citation', async () => {
    const user = userEvent.setup()
    const citation: Citation = {
      ...baseCitation,
      file_name: 'paper.pdf',
      locator: { type: 'page', value: 4 },
      text: longPageText,
    }
    render(<CitationMarker citation={citation} />)

    await user.click(screen.getByText('[1]'))

    expect(screen.queryByText(longPageText)).not.toBeInTheDocument()
    expect(await screen.findByRole('button', { name: /show full page text/i })).toBeInTheDocument()
  })

  it('expands to the full chunk text when the expand control is clicked', async () => {
    const user = userEvent.setup()
    const citation: Citation = {
      ...baseCitation,
      file_name: 'paper.pdf',
      locator: { type: 'page', value: 4 },
      text: longPageText,
    }
    render(<CitationMarker citation={citation} />)

    await user.click(screen.getByText('[1]'))
    await user.click(await screen.findByRole('button', { name: /show full page text/i }))

    expect(screen.getByText(longPageText)).toBeInTheDocument()
  })

  it('never truncates a plain-text citation, even a long one', async () => {
    const user = userEvent.setup()
    const citation: Citation = {
      ...baseCitation,
      text: longPageText,
      locator: null,
    }
    render(<CitationMarker citation={citation} />)

    await user.click(screen.getByText('[1]'))

    expect(await screen.findByText(longPageText)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /show full page text/i })).not.toBeInTheDocument()
  })
})
