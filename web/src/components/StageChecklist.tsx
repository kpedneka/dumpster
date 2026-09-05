import { CheckCircle2, Circle, Info, Loader2, Sparkles } from 'lucide-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'
import { STAGE_KEY_COMPLETE, type StageProgressView, type StageView } from '@/lib/status-ring'

function StageIcon({ stage }: { stage: StageView }) {
  // The terminal "fully indexed" stage is a real, permanent finished
  // state, not a step actively running -- it gets the same checkmark as
  // "done", never the spinner "active" otherwise gets.
  if (stage.state === 'done' || (stage.state === 'active' && stage.key === STAGE_KEY_COMPLETE)) {
    return <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-500" />
  }
  if (stage.state === 'active') return <Loader2 className="h-4 w-4 shrink-0 animate-spin text-blue-500" />
  return <Circle className="h-4 w-4 shrink-0 text-muted-foreground/40" />
}

// StageProgressLabel is the inline "which step is this on" text for a
// document row. When a full checklist is available it's a clickable
// trigger -- with an icon called out next to it specifically because a
// plain text label reads as static, not something to click -- that opens
// a GitHub-Checks-style popover with every stage and the active one's
// description. Without a checklist (no progress signal) it renders as
// plain, non-interactive text.
export function StageProgressLabel({ view }: { view: StageProgressView }) {
  if (view.stages.length === 0) {
    return <span className="text-xs text-muted-foreground">{view.text}</span>
  }

  // Once the whole pipeline (including background entity extraction) has
  // finished, this label is a permanent ingestion-history record rather
  // than something actively changing -- styled a little more deliberately
  // than the muted "still working" text so it reads as a finished,
  // positive state, not just more processing chatter.
  const isComplete = view.stages.at(-1)?.key === STAGE_KEY_COMPLETE && view.stages.at(-1)?.state === 'active'

  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={cn(
            'inline-flex items-center gap-1 text-xs focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-sm',
            isComplete
              ? 'text-emerald-600 hover:text-emerald-700 dark:text-emerald-400 dark:hover:text-emerald-300'
              : 'text-muted-foreground hover:text-foreground',
          )}
        >
          <span>{view.text}</span>
          {isComplete ? (
            <Sparkles className="h-3 w-3 shrink-0" aria-hidden="true" />
          ) : (
            <Info className="h-3 w-3 shrink-0" aria-hidden="true" />
          )}
          <span className="sr-only">(click for processing details)</span>
        </button>
      </PopoverTrigger>
      <PopoverContent align="start" className="w-72 p-3">
        <ul className="space-y-2.5">
          {view.stages.map((stage) => (
            <li key={stage.key} className="flex items-start gap-2">
              <StageIcon stage={stage} />
              <div className="min-w-0">
                <p className={cn('text-sm leading-tight', stage.state === 'upcoming' && 'text-muted-foreground')}>
                  {stage.label}
                </p>
                {stage.state === 'active' && stage.message && (
                  <p className="mt-0.5 text-xs text-muted-foreground">{stage.message}</p>
                )}
              </div>
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  )
}
