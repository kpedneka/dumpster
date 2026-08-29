export type StatusRingStatus = 'pending' | 'processing' | 'indexed' | 'failed'

export interface StatusRingState {
  /** 0..1 fraction of the ring to fill. */
  fraction: number
  /** True once a "processing" document has run well past a typical duration. */
  stalled: boolean
  label: string
}

// No per-document progress signal exists in the ingestion pipeline today:
// region classification/chunking/embedding run as one synchronous job with
// no intermediate checkpoints (see v4.6). Until real job-duration
// percentiles are measured, the ring approximates progress as elapsed time
// against this typical-case duration, and flags anything still "processing"
// well past it rather than freezing at a misleadingly-full ring.
export const EXPECTED_PROCESSING_MS = 45_000
export const STALLED_FRACTION_CAP = 0.92
export const PENDING_FRACTION = 0.08

const LABEL: Record<StatusRingStatus, string> = {
  pending: 'Pending',
  processing: 'Processing…',
  indexed: 'Indexed',
  failed: 'Failed',
}

export function computeStatusRing(status: StatusRingStatus, updatedAt: string, now: number): StatusRingState {
  const label = LABEL[status]

  if (status === 'pending') {
    return { fraction: PENDING_FRACTION, stalled: false, label }
  }
  if (status === 'indexed' || status === 'failed') {
    return { fraction: 1, stalled: false, label }
  }

  const elapsed = now - Date.parse(updatedAt)
  const stalled = elapsed > EXPECTED_PROCESSING_MS
  const fraction = stalled ? STALLED_FRACTION_CAP : Math.min(elapsed / EXPECTED_PROCESSING_MS, 1)
  return { fraction, stalled, label }
}
