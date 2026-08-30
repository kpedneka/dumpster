import { useState } from 'react'
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/popover'
import type { components } from '@/api/schema.d.ts'

type Citation = components['schemas']['Citation']

interface CitationMarkerProps {
  citation: Citation
}

// The inline marker stays a compact [N] badge rather than the file name
// itself — spelling out every citation's source inline would reintroduce the
// clutter this model was built to avoid. Instead, mirroring how search
// engines cite a source page rather than a byte range within it, the
// popover's primary content is the file (+ locator, when one applies, e.g.
// a PDF page number), with the chunk's own extracted text underneath as
// drill-down evidence for anyone who wants to verify the exact source span.
function formatLocator(locator: Citation['locator']): string | null {
  if (!locator) return null
  switch (locator.type) {
    case 'page':
      return `p. ${locator.value}`
    default:
      return null
  }
}

// A locator-bearing chunk (currently: a PDF page) is much larger than a
// text/markdown chunk, so its drill-down text is truncated behind an expand
// control by default — otherwise the popover reintroduces the same
// too-much-highlighted-text problem this citation model was built to avoid.
// Plain-text citations have no locator and are never truncated: their
// chunks are already paragraph-sized.
const TRUNCATE_AT = 200

export function CitationMarker({ citation }: CitationMarkerProps) {
  const [open, setOpen] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const locatorLabel = formatLocator(citation.locator)

  const truncatable = citation.locator != null && citation.text.length > TRUNCATE_AT
  const displayText = truncatable && !expanded ? `${citation.text.slice(0, TRUNCATE_AT)}…` : citation.text

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        setOpen(next)
        if (!next) setExpanded(false)
      }}
    >
      <PopoverTrigger asChild>
        {/* tabIndex makes this a real keyboard tab stop, and onKeyDown makes
            it operable once reached: <sup> isn't focusable by default, and
            asChild's ARIA/behavioral props (aria-haspopup, etc.) don't
            include a browser's native Enter/Space-triggers-click behavior —
            that only comes for free on an actual <button>. Both are missing
            without this; confirmed by testing Enter on a tabIndex-only
            version and finding the popover simply never opened. */}
        <sup
          tabIndex={0}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault()
              e.currentTarget.click()
            }
          }}
          className="ml-0.5 cursor-pointer rounded bg-accent px-1 py-0.5 text-xs font-medium text-accent-foreground hover:bg-accent/80"
        >
          {`[${citation.number}]`}
        </sup>
      </PopoverTrigger>
      <PopoverContent className="max-h-[min(24rem,70vh)] overflow-y-auto">
        <p className="mb-2 text-sm font-medium">
          {citation.file_name}
          {locatorLabel && <span className="text-muted-foreground">{` · ${locatorLabel}`}</span>}
        </p>
        <p className="text-sm leading-relaxed">
          <mark className="rounded bg-yellow-200 px-0.5">{displayText}</mark>
        </p>
        {truncatable && !expanded && (
          <button
            type="button"
            onClick={() => setExpanded(true)}
            className="mt-2 text-xs font-medium text-accent-foreground underline underline-offset-2 hover:no-underline"
          >
            Show full page text
          </button>
        )}
      </PopoverContent>
    </Popover>
  )
}
