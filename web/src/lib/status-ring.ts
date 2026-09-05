export type StatusRingStatus = 'pending' | 'processing' | 'indexed' | 'failed'

export interface Stage {
  key: string
  label: string
  message?: string
}

export interface DocumentProgress {
  stages: Stage[]
  current_stage?: string
}

export type StageState = 'done' | 'active' | 'upcoming'

// STAGE_KEY_COMPLETE mirrors the Go backend's queue.StageKeyComplete: the
// terminal stage a document sits on permanently once its entire pipeline,
// including background entity extraction, has finished.
export const STAGE_KEY_COMPLETE = 'complete'

export interface StageView extends Stage {
  state: StageState
}

export interface StatusRingState {
  /** 0..1 fraction of the ring to fill -- coarse, by stage index, not a live timer. */
  fraction: number
  label: string
}

const LABEL: Record<StatusRingStatus, string> = {
  pending: 'Pending',
  processing: 'Processing…',
  indexed: 'Indexed',
  failed: 'Failed',
}

// computeStatusRing derives the ring's fill purely from doc.status: the
// ring communicates search-readiness (can I query this document yet?),
// which is exactly what "indexed" already means, so it's always fully
// filled there regardless of whether background entity extraction is
// still running -- see stageProgressView for that separate, longer-lived
// signal. progress only matters pre-indexing, to place the fill partway
// through the pending/processing stages.
export function computeStatusRing(status: StatusRingStatus, progress?: DocumentProgress | null): StatusRingState {
  const label = LABEL[status]

  if (status === 'indexed' || status === 'failed') {
    return { fraction: 1, label }
  }
  if (!progress || progress.stages.length === 0) {
    return { fraction: status === 'pending' ? 0 : 0.5, label }
  }

  const activeIndex = progress.current_stage
    ? progress.stages.findIndex((s) => s.key === progress.current_stage)
    : -1

  // +0.5 so a stage in progress reads as "partway through this step", not
  // "just finished the previous one" -- there's no finer-grained signal
  // than which stage is current, so this is a deliberately coarse midpoint.
  const fraction = activeIndex === -1 ? 0 : (activeIndex + 0.5) / progress.stages.length
  return { fraction, label }
}

function buildStageViews(progress: DocumentProgress): StageView[] {
  const activeIndex = progress.current_stage
    ? progress.stages.findIndex((s) => s.key === progress.current_stage)
    : -1
  return progress.stages.map((s, i) => ({
    ...s,
    state: activeIndex === -1 ? 'upcoming' : i < activeIndex ? 'done' : i === activeIndex ? 'active' : 'upcoming',
  }))
}

export interface StageProgressView {
  /** Short text a document row shows inline (e.g. under the filename) so
   * the current step is visible without any click. */
  text: string
  /** The full checklist for the detail popover. Empty when there's
   * nothing more to show than text itself (no progress signal available),
   * in which case the caller should render text as plain, non-interactive
   * text rather than a clickable trigger. */
  stages: StageView[]
}

// stageProgressView is what a document row shows inline for ingestion
// progress. Unlike the ring, this keeps reporting progress after a
// document reaches "indexed": entity extraction, edge extraction, and
// canonicalization all run as an invisible background pipeline once
// indexing itself finishes, and the backend keeps sending `progress` --
// landing permanently on the STAGE_KEY_COMPLETE stage -- for as long as
// the document exists, so the checklist doubles as an ingestion-history
// record rather than disappearing once there's nothing left in flight.
// Returns null only for a failed (dead-lettered) document, or when no
// progress signal is available at all (e.g. no JobStatusReader
// configured for this deployment).
export function stageProgressView(status: StatusRingStatus, progress?: DocumentProgress | null): StageProgressView | null {
  if (status === 'failed') {
    return null
  }
  if (!progress || progress.stages.length === 0) {
    return status === 'indexed' ? null : { text: LABEL[status], stages: [] }
  }
  const stages = buildStageViews(progress)
  const text = stages.find((s) => s.state === 'active')?.label ?? LABEL[status]
  return { text, stages }
}

// hasInFlightWork decides whether a document is still worth polling for.
// doc.status alone can't answer this: it never changes again once a
// document reaches "indexed", even while entity extraction/edge
// extraction/canonicalization keep running as a background pipeline
// afterward. The backend now always attaches `progress` for an indexed
// document (see dochandler.go's enrichPage), landing permanently on
// STAGE_KEY_COMPLETE once that background pipeline is done too -- so an
// indexed document only counts as settled once its current stage is that
// terminal one (or progress is missing entirely, e.g. a deployment with
// no JobStatusReader configured at all).
export function hasInFlightWork(status: StatusRingStatus, progress?: DocumentProgress | null): boolean {
  if (status === 'failed') {
    return false
  }
  if (status !== 'indexed') {
    return true
  }
  return progress != null && progress.current_stage !== STAGE_KEY_COMPLETE
}
