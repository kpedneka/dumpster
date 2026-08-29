import { useEffect, useState } from 'react'
import { ArrowUp } from 'lucide-react'
import { cn } from '@/lib/utils'
import { computeStatusRing, type StatusRingStatus } from '@/lib/status-ring'

const RING_COLOR: Record<StatusRingStatus, string> = {
  pending: 'stroke-amber-500 text-amber-500',
  processing: 'stroke-blue-500 text-blue-500',
  indexed: 'stroke-emerald-500 text-emerald-500',
  failed: 'stroke-red-500 text-red-500',
}
// Distinct from the normal "processing" blue so a stalled job reads as its
// own state at a glance, not just a paused version of "in progress".
const STALLED_COLOR = 'stroke-amber-600 text-amber-600'

const RADIUS = 9
const CIRCUMFERENCE = 2 * Math.PI * RADIUS

/** Ticks a re-render every second so the ring's fill advances live while a
 * document is actively processing; inert (and cheap) for terminal states. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (!active) return
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [active])
  return now
}

export function StatusRing({ status, updatedAt }: { status: StatusRingStatus; updatedAt: string }) {
  const now = useNow(status === 'processing')
  const { fraction, stalled, label } = computeStatusRing(status, updatedAt, now)
  const color = stalled ? STALLED_COLOR : RING_COLOR[status]
  const accessibleLabel = stalled ? `${label} — taking longer than usual` : label

  return (
    <div
      className="relative inline-flex h-7 w-7 shrink-0 items-center justify-center"
      role="img"
      aria-label={accessibleLabel}
      title={accessibleLabel}
    >
      <svg viewBox="0 0 24 24" className="h-7 w-7 -rotate-90">
        <circle cx="12" cy="12" r={RADIUS} strokeWidth="2" fill="none" className="stroke-muted" />
        <circle
          cx="12"
          cy="12"
          r={RADIUS}
          strokeWidth="2"
          fill="none"
          strokeLinecap="round"
          strokeDasharray={CIRCUMFERENCE}
          strokeDashoffset={CIRCUMFERENCE * (1 - fraction)}
          className={cn(color, (status === 'pending' || status === 'processing') && 'animate-pulse')}
        />
      </svg>
      <ArrowUp className={cn('absolute h-3 w-3', color)} />
      <span className="sr-only">{accessibleLabel}</span>
    </div>
  )
}
