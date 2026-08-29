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

export function CitationMarker({ citation }: CitationMarkerProps) {
  const [open, setOpen] = useState(false)
  const locatorLabel = formatLocator(citation.locator)

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <sup className="ml-0.5 cursor-pointer rounded bg-accent px-1 py-0.5 text-xs font-medium text-accent-foreground hover:bg-accent/80">
          {`[${citation.number}]`}
        </sup>
      </PopoverTrigger>
      <PopoverContent className="max-h-[min(24rem,70vh)] overflow-y-auto">
        <p className="mb-2 text-sm font-medium">
          {citation.file_name}
          {locatorLabel && <span className="text-muted-foreground">{` · ${locatorLabel}`}</span>}
        </p>
        <p className="text-sm leading-relaxed">
          <mark className="rounded bg-yellow-200 px-0.5">{citation.text}</mark>
        </p>
      </PopoverContent>
    </Popover>
  )
}
