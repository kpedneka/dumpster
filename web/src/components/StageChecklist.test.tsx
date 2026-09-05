import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { StageProgressLabel } from './StageChecklist'
import type { StageProgressView } from '@/lib/status-ring'

const keys = ['analyzing', 'embedding', 'entities', 'complete']
const labels: Record<string, string> = {
  analyzing: 'Analyzing document',
  embedding: 'Preparing for search',
  entities: 'Extracting entities',
  complete: 'Fully indexed',
}
const messages: Record<string, string> = {
  analyzing: 'Identifying text, tables, and figures.',
  embedding: 'This document will be searchable once this step completes.',
  entities: 'Unlocks communities and themes once complete.',
  complete: 'Indexed, searchable, and included in communities and themes.',
}

const withChecklist = (currentKey: string): StageProgressView => {
  const activeIndex = keys.indexOf(currentKey)
  return {
    text: labels[currentKey],
    stages: keys.map((key, i) => ({
      key,
      label: labels[key],
      message: messages[key],
      state: i < activeIndex ? 'done' : i === activeIndex ? 'active' : 'upcoming',
    })),
  }
}

describe('StageProgressLabel', () => {
  it('renders plain, non-interactive text when there is no checklist to show', () => {
    render(<StageProgressLabel view={{ text: 'Pending', stages: [] }} />)
    expect(screen.getByText('Pending')).toBeInTheDocument()
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })

  it('renders a clickable trigger with an info icon when a checklist is available', () => {
    render(<StageProgressLabel view={withChecklist('embedding')} />)
    const button = screen.getByRole('button')
    expect(button).toHaveTextContent('Preparing for search')
    expect(button.querySelector('svg')).toBeInTheDocument()
  })

  it('opens a popover on click showing every stage and only the active one\'s message', async () => {
    const user = userEvent.setup()
    render(<StageProgressLabel view={withChecklist('embedding')} />)

    await user.click(screen.getByRole('button'))

    expect(await screen.findByText('Analyzing document')).toBeInTheDocument()
    // "Preparing for search" appears twice once the popover is open: once
    // as the trigger's own visible label, once in the checklist -- both
    // intentional, so disambiguate rather than asserting there's one.
    expect(screen.getAllByText('Preparing for search').length).toBe(2)
    expect(screen.getByText('Extracting entities')).toBeInTheDocument()
    expect(screen.getByText('This document will be searchable once this step completes.')).toBeInTheDocument()
    expect(screen.queryByText('Identifying text, tables, and figures.')).not.toBeInTheDocument()
  })

  it('shows the entities checklist for an indexed document whose entity extraction is still active', async () => {
    // Regression coverage for the case that motivated stageProgressView
    // outliving doc.status === "indexed": entity extraction runs after
    // indexing finishes, so the checklist must still be reachable then.
    const user = userEvent.setup()
    render(<StageProgressLabel view={withChecklist('entities')} />)

    const button = screen.getByRole('button')
    expect(button).toHaveTextContent('Extracting entities')

    await user.click(button)
    expect(await screen.findByText('Unlocks communities and themes once complete.')).toBeInTheDocument()
  })

  it('styles the terminal complete stage distinctly from an in-progress one', () => {
    const { rerender } = render(<StageProgressLabel view={withChecklist('embedding')} />)
    const inProgressButton = screen.getByRole('button')
    expect(inProgressButton.className).toContain('text-muted-foreground')

    rerender(<StageProgressLabel view={withChecklist('complete')} />)
    const completeButton = screen.getByRole('button')
    expect(completeButton).toHaveTextContent('Fully indexed')
    expect(completeButton.className).toContain('text-emerald-600')
  })

  it('never shows a spinner for the terminal complete stage, even though it is "active"', async () => {
    const user = userEvent.setup()
    render(<StageProgressLabel view={withChecklist('complete')} />)

    await user.click(screen.getByRole('button'))

    expect(await screen.findByText('Indexed, searchable, and included in communities and themes.')).toBeInTheDocument()
    // lucide's Loader2 renders as an <svg class="lucide-loader-circle">.
    expect(document.querySelector('.lucide-loader-circle')).not.toBeInTheDocument()
  })

  // The regression test for the exact bug introduced by letting entity
  // extraction start before a document's own indexing job finishes:
  // entity extraction can now be active at the same time as embedding.
  // The checklist must honestly show both as active
  // (spinner + message each), not collapse to a single in-progress row,
  // since real concurrent work is happening.
  it('shows more than one stage as active at once, each with its own spinner and message', async () => {
    const user = userEvent.setup()
    const view: StageProgressView = {
      text: labels.embedding,
      stages: [
        { key: 'analyzing', label: labels.analyzing, message: messages.analyzing, state: 'done' },
        { key: 'embedding', label: labels.embedding, message: messages.embedding, state: 'active' },
        { key: 'entities', label: labels.entities, message: messages.entities, state: 'active' },
        { key: 'complete', label: labels.complete, message: messages.complete, state: 'upcoming' },
      ],
    }
    render(<StageProgressLabel view={view} />)

    await user.click(screen.getByRole('button'))

    expect(await screen.findByText(messages.embedding)).toBeInTheDocument()
    expect(screen.getByText(messages.entities)).toBeInTheDocument()
    expect(document.querySelectorAll('.lucide-loader-circle')).toHaveLength(2)
  })
})
