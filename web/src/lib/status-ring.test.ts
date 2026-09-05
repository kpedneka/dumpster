import { describe, it, expect } from 'vitest'
import { computeStatusRing, hasInFlightWork, stageProgressView, STAGE_KEY_COMPLETE, type DocumentProgress } from './status-ring'

// Mirrors queue.StagesForDocument(true) in internal/queue/stage.go.
const pdfProgress = (currentStage?: string): DocumentProgress => ({
  stages: [
    { key: 'analyzing', label: 'Analyzing document' },
    { key: 'embedding', label: 'Preparing for search' },
    { key: 'entities', label: 'Extracting entities' },
    { key: 'complete', label: 'Fully indexed', message: 'Indexed, searchable, and included in communities and themes.' },
  ],
  current_stage: currentStage,
})

// Mirrors queue.StagesForDocument(false).
const textProgress = (currentStage?: string): DocumentProgress => ({
  stages: [
    { key: 'embedding', label: 'Preparing for search' },
    { key: 'entities', label: 'Extracting entities' },
    { key: 'complete', label: 'Fully indexed', message: 'Indexed, searchable, and included in communities and themes.' },
  ],
  current_stage: currentStage,
})

describe('computeStatusRing', () => {
  // The ring only ever reflects doc.status -- see stageProgressView for
  // the separate, longer-lived entity-extraction signal.
  it('is empty for a queued (pending) document', () => {
    const result = computeStatusRing('pending', pdfProgress(undefined))
    expect(result.fraction).toBe(0)
    expect(result.label).toBe('Pending')
  })

  it('fills partway through based on which stage is current while processing', () => {
    const result = computeStatusRing('processing', pdfProgress('embedding'))
    expect(result.fraction).toBeCloseTo(1.5 / 4, 5)
    expect(result.label).toBe('Processing…')
  })

  it('falls back to a half-filled ring when no progress signal is available while processing', () => {
    const result = computeStatusRing('processing', undefined)
    expect(result.fraction).toBe(0.5)
    expect(result.label).toBe('Processing…')
  })

  it('is fully filled for an indexed document regardless of progress', () => {
    const result = computeStatusRing('indexed', pdfProgress('entities'))
    expect(result.fraction).toBe(1)
    expect(result.label).toBe('Indexed')
  })

  it('is fully filled for a failed document', () => {
    const result = computeStatusRing('failed', pdfProgress('embedding'))
    expect(result.fraction).toBe(1)
    expect(result.label).toBe('Failed')
  })
})

describe('stageProgressView', () => {
  it('returns the active stage label as text, plus the full checklist, when a stage is in progress', () => {
    const view = stageProgressView('processing', pdfProgress('embedding'))
    expect(view?.text).toBe('Preparing for search')
    expect(view?.stages.map((s) => s.state)).toEqual(['done', 'active', 'upcoming', 'upcoming'])
  })

  it('falls back to the coarse status label when no stage is active yet, but still returns the checklist', () => {
    const view = stageProgressView('pending', pdfProgress(undefined))
    expect(view?.text).toBe('Pending')
    expect(view?.stages).toHaveLength(4)
  })

  it('falls back to the coarse status label with an empty checklist when no progress signal is available', () => {
    const view = stageProgressView('processing', undefined)
    expect(view?.text).toBe('Processing…')
    expect(view?.stages).toEqual([])
  })

  it('returns null for a failed (dead-lettered) document', () => {
    expect(stageProgressView('failed', pdfProgress('embedding'))).toBeNull()
  })

  it('returns null for an indexed document with no progress signal at all (e.g. no JobStatusReader configured)', () => {
    expect(stageProgressView('indexed', undefined)).toBeNull()
    expect(stageProgressView('indexed', { stages: [], current_stage: undefined })).toBeNull()
  })

  // The whole reason stageProgressView doesn't short-circuit on
  // status === 'indexed' the way computeStatusRing does: entity
  // extraction/edge extraction/canonicalization all run as a background
  // pipeline *after* indexing finishes, and the backend keeps sending
  // `progress` for as long as that pipeline is still active (see
  // dochandler.go's enrichPage). Losing the checklist the moment a
  // document becomes searchable would hide real in-flight work.
  it('still surfaces the checklist for an indexed document whose entity-extraction pipeline is still active', () => {
    const view = stageProgressView('indexed', pdfProgress('entities'))
    expect(view?.text).toBe('Extracting entities')
    expect(view?.stages.map((s) => s.state)).toEqual(['done', 'done', 'active', 'upcoming'])
  })

  it('works the same for a plain-text document (three stages, no analyzing step)', () => {
    const view = stageProgressView('indexed', textProgress('entities'))
    expect(view?.text).toBe('Extracting entities')
    expect(view?.stages.map((s) => s.state)).toEqual(['done', 'active', 'upcoming'])
  })

  // The terminal state this whole feature exists for: once the background
  // pipeline is truly done, the backend lands current_stage on
  // STAGE_KEY_COMPLETE forever, rather than progress disappearing --
  // stageProgressView surfaces that as its own polished label/checklist
  // entry instead of falling back to file size.
  it('shows the fully-indexed terminal stage once the whole pipeline (including entities) is done', () => {
    const view = stageProgressView('indexed', pdfProgress(STAGE_KEY_COMPLETE))
    expect(view?.text).toBe('Fully indexed')
    expect(view?.stages.map((s) => s.state)).toEqual(['done', 'done', 'done', 'active'])
    expect(view?.stages.at(-1)?.key).toBe(STAGE_KEY_COMPLETE)
  })
})

describe('hasInFlightWork', () => {
  it('is true for any non-terminal status regardless of progress', () => {
    expect(hasInFlightWork('pending', undefined)).toBe(true)
    expect(hasInFlightWork('processing', undefined)).toBe(true)
  })

  it('is false for a failed (dead-lettered) document', () => {
    expect(hasInFlightWork('failed', pdfProgress('embedding'))).toBe(false)
  })

  // This is the exact bug this function fixes: doc.status never changes
  // again once a document reaches "indexed", even while entity
  // extraction/edge extraction/canonicalization keep running in the
  // background -- treating "indexed" alone as settled stops the poll loop
  // before that pipeline is actually done, permanently freezing the UI at
  // whatever it last happened to fetch.
  it('is true for an indexed document that still has an active background job', () => {
    expect(hasInFlightWork('indexed', pdfProgress('entities'))).toBe(true)
  })

  it('is false once an indexed document lands on the terminal complete stage', () => {
    expect(hasInFlightWork('indexed', pdfProgress(STAGE_KEY_COMPLETE))).toBe(false)
  })

  it('is false for an indexed document with no progress signal at all', () => {
    expect(hasInFlightWork('indexed', undefined)).toBe(false)
    expect(hasInFlightWork('indexed', null)).toBe(false)
  })
})
