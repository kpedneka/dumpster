import { useState } from 'react'
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/popover'
import type { components } from '@/api/schema.d.ts'

type Citation = components['schemas']['Citation']

interface CitationMarkerProps {
  citation: Citation
}

// Citation.text is the cited chunk's own extracted content, served directly
// by the search response. Earlier this fetched /documents/{docId}/content and
// sliced it by char_start/char_end, but those offsets are only meaningful
// relative to a text/markdown document's full body — for PDF/image chunks
// (one region's text per API call, e.g. one page) they index into the wrong
// string entirely, and for PDFs that endpoint returns the raw binary anyway.
export function CitationMarker({ citation }: CitationMarkerProps) {
  const [open, setOpen] = useState(false)

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <sup className="ml-0.5 cursor-pointer rounded bg-accent px-1 py-0.5 text-xs font-medium text-accent-foreground hover:bg-accent/80">
          {`[${citation.number}]`}
        </sup>
      </PopoverTrigger>
      <PopoverContent className="max-h-[min(24rem,70vh)] overflow-y-auto">
        <p className="text-sm leading-relaxed">
          <mark className="rounded bg-yellow-200 px-0.5">{citation.text}</mark>
        </p>
      </PopoverContent>
    </Popover>
  )
}
