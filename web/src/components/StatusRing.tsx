import { ArrowUp } from 'lucide-react'
import { cn } from '@/lib/utils'
import { computeStatusRing, type DocumentProgress, type StatusRingStatus } from '@/lib/status-ring'

const RING_COLOR: Record<StatusRingStatus, string> = {
  pending: 'stroke-amber-500 text-amber-500',
  processing: 'stroke-blue-500 text-blue-500',
  indexed: 'stroke-emerald-500 text-emerald-500',
  failed: 'stroke-red-500 text-red-500',
}

const RADIUS = 9
const CIRCUMFERENCE = 2 * Math.PI * RADIUS

// StatusRing is purely decorative -- a compact glance at a document's
// coarse status. The interactive detail view (which stage, what's next)
// lives on the inline stage label instead (see StageChecklist's
// StageProgressLabel), since a small ring icon turned out to be a poor
// affordance for "click me for more info".
export function StatusRing({
  status,
  progress,
}: {
  status: StatusRingStatus
  progress?: DocumentProgress | null
}) {
  const { fraction, label } = computeStatusRing(status, progress)
  const color = RING_COLOR[status]

  return (
    <div
      className="relative inline-flex h-7 w-7 shrink-0 items-center justify-center"
      role="img"
      aria-label={label}
      title={label}
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
      <span className="sr-only">{label}</span>
    </div>
  )
}
