import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import { StatusRing } from './StatusRing'
import type { DocumentProgress } from '@/lib/status-ring'

const threeStageProgress = (currentStage?: string): DocumentProgress => ({
  stages: [
    { key: 'analyzing', label: 'Analyzing document' },
    { key: 'embedding', label: 'Preparing for search' },
    { key: 'entities', label: 'Extracting entities' },
  ],
  current_stage: currentStage,
})

describe('StatusRing', () => {
  it('labels a pending document with no active job yet', () => {
    render(<StatusRing status="pending" progress={threeStageProgress(undefined)} />)
    expect(screen.getByRole('img', { name: 'Pending' })).toBeInTheDocument()
  })

  it('labels a processing document', () => {
    render(<StatusRing status="processing" progress={threeStageProgress('embedding')} />)
    expect(screen.getByRole('img', { name: 'Processing…' })).toBeInTheDocument()
  })

  it('labels an indexed document', () => {
    render(<StatusRing status="indexed" progress={null} />)
    expect(screen.getByRole('img', { name: 'Indexed' })).toBeInTheDocument()
  })

  it('labels a failed document', () => {
    render(<StatusRing status="failed" progress={null} />)
    expect(screen.getByRole('img', { name: 'Failed' })).toBeInTheDocument()
  })

  // The ring is purely decorative now -- the click-for-detail affordance
  // lives on the inline stage label (see StageChecklist.test.tsx), not
  // here, since a small icon turned out to be a poor "click me" signal.
  it('renders no interactive element for any status', () => {
    render(<StatusRing status="processing" progress={threeStageProgress('embedding')} />)
    expect(screen.queryByRole('button')).not.toBeInTheDocument()
  })
})
