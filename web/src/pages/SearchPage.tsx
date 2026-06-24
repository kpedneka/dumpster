import { Fragment, useRef, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Search as SearchIcon } from 'lucide-react'
import { api } from '@/api/client'
import { KBTabs } from '@/components/KBTabs'
import { CitationMarker } from '@/components/CitationMarker'
import { Button } from '@/components/ui/button'
import { getDraftQuery, setDraftQuery, getSubmittedQuery, setSubmittedQuery } from '@/lib/search-state'
import { cn } from '@/lib/utils'
import type { components } from '@/api/schema.d.ts'

type Citation = components['schemas']['Citation']

const CITATION_MARKER = /(\[\d+\])/g

function renderCitedSummary(summary: string, citations: Citation[], kbId: string) {
  return summary.split(CITATION_MARKER).map((part, i) => {
    const match = part.match(/^\[(\d+)\]$/)
    if (!match) return <Fragment key={i}>{part}</Fragment>
    const markerNumber = Number(match[1])
    const citation = citations.find((c) => c.number === markerNumber)
    if (!citation) return <Fragment key={i}>{part}</Fragment>
    return <CitationMarker key={i} citation={citation} kbId={kbId} />
  })
}

function describeError(error: unknown): string {
  if (error && typeof error === 'object' && typeof (error as { error?: unknown }).error === 'string') {
    return (error as { error: string }).error
  }
  return 'Search failed. Try again.'
}

export function SearchPage() {
  const { kbId } = useParams<{ kbId: string }>()
  const [query, setQuery] = useState(() => getDraftQuery(kbId!))
  const [submittedQuery, setSubmittedQueryState] = useState(() => getSubmittedQuery(kbId!))
  const taRef = useRef<HTMLTextAreaElement>(null)

  const { data: kb } = useQuery({
    queryKey: ['kbs', kbId],
    queryFn: async () => {
      const { data, error } = await api.GET('/kbs/{id}', { params: { path: { id: kbId! } } })
      if (error) throw error
      return data
    },
    enabled: !!kbId,
  })

  const {
    data: result,
    isFetching,
    isError,
    error,
  } = useQuery({
    queryKey: ['search', kbId, submittedQuery],
    queryFn: async () => {
      const { data, error } = await api.POST('/kbs/{id}/search', {
        params: { path: { id: kbId! } },
        body: { query: submittedQuery },
      })
      if (error) throw error
      return data
    },
    enabled: !!kbId && !!submittedQuery,
  })

  // v2.8: the query field is a textarea that grows with its content instead of
  // scrolling horizontally, so long questions stay fully visible.
  function autosize(el: HTMLTextAreaElement | null) {
    if (!el) return
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight}px`
  }

  function handleQueryChange(value: string) {
    setQuery(value)
    setDraftQuery(kbId!, value)
    autosize(taRef.current)
  }

  function submit() {
    const trimmed = query.trim()
    if (trimmed) {
      setSubmittedQueryState(trimmed)
      setSubmittedQuery(kbId!, trimmed)
    }
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    submit()
  }

  return (
    <div className="rise mx-auto max-w-3xl px-6 py-8">
      <h1 className="mb-6 font-display text-2xl font-semibold tracking-tight">{kb?.name ?? '—'}</h1>
      <KBTabs kbId={kbId!} />

      <form onSubmit={handleSubmit} className="mb-8 flex items-start gap-2">
        <div className="relative flex-1">
          <SearchIcon className="pointer-events-none absolute left-3 top-3 h-4 w-4 text-muted-foreground" />
          <textarea
            ref={(el) => {
              taRef.current = el
              autosize(el)
            }}
            rows={1}
            value={query}
            onChange={(e) => handleQueryChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault()
                submit()
              }
            }}
            placeholder="Ask a question about this knowledge base…"
            className={cn(
              'flex w-full resize-none overflow-hidden rounded-md border border-input bg-background px-3 py-2 pl-9',
              'text-sm leading-6 shadow-sm outline-none transition-colors placeholder:text-muted-foreground',
              'focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40',
            )}
          />
        </div>
        <Button type="submit" disabled={!query.trim() || isFetching} className="lift">
          <SearchIcon className="h-4 w-4" />
          Search
        </Button>
      </form>

      {!submittedQuery && (
        <p className="py-16 text-center text-sm text-muted-foreground">
          Ask a question above to search this knowledge base.
        </p>
      )}

      {submittedQuery && isFetching && (
        <p className="py-16 text-center text-sm text-muted-foreground">Searching…</p>
      )}

      {submittedQuery && !isFetching && isError && (
        <p className="py-16 text-center text-sm text-destructive">{describeError(error)}</p>
      )}

      {submittedQuery && !isFetching && !isError && result && (
        result.citations.length === 0 ? (
          <p className="py-16 text-center text-sm text-muted-foreground">No results found.</p>
        ) : (
          <div className="rise rounded-lg border border-border bg-card p-5 text-sm leading-relaxed shadow-sm">
            {renderCitedSummary(result.summary, result.citations, kbId!)}
          </div>
        )
      )}
    </div>
  )
}
