import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/api/client'
import { Popover, PopoverTrigger, PopoverContent } from '@/components/ui/popover'
import type { components } from '@/api/schema.d.ts'

type Citation = components['schemas']['Citation']

// How much surrounding text to show, dimmed, around the highlighted span —
// char_start/char_end mark a whole ~3000-byte indexing chunk, not a sentence,
// so a little context on either side helps orient the reader.
const CONTEXT_CHARS = 100

interface CitationMarkerProps {
  citation: Citation
  kbId: string
}

export function CitationMarker({ citation, kbId }: CitationMarkerProps) {
  const [open, setOpen] = useState(false)

  const { data, isFetching, isError } = useQuery({
    queryKey: ['document-content', kbId, citation.document_id],
    queryFn: async () => {
      const { data, error } = await api.GET('/kbs/{kbId}/documents/{docId}/content', {
        params: { path: { kbId, docId: citation.document_id } },
        parseAs: 'text',
      })
      if (error) throw error
      return data
    },
    enabled: open,
    // The fetched document text never changes once indexed — never refetch
    // a second citation into a document already cached for this page.
    staleTime: Infinity,
  })

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <sup className="ml-0.5 cursor-pointer rounded bg-accent px-1 py-0.5 text-xs font-medium text-accent-foreground hover:bg-accent/80">
          {`[${citation.number}]`}
        </sup>
      </PopoverTrigger>
      <PopoverContent>
        {isFetching && <p className="text-sm text-muted-foreground">Loading…</p>}
        {isError && <p className="text-sm text-destructive">Could not load source text.</p>}
        {!isFetching && !isError && data !== undefined && (
          <p className="text-sm leading-relaxed">
            <span className="text-muted-foreground">
              {data.slice(Math.max(0, citation.char_start - CONTEXT_CHARS), citation.char_start)}
            </span>
            <mark className="rounded bg-yellow-200 px-0.5">
              {data.slice(citation.char_start, citation.char_end)}
            </mark>
            <span className="text-muted-foreground">
              {data.slice(citation.char_end, citation.char_end + CONTEXT_CHARS)}
            </span>
          </p>
        )}
      </PopoverContent>
    </Popover>
  )
}
