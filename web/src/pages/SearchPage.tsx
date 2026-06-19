import { Fragment, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Search as SearchIcon } from 'lucide-react'
import { api } from '@/api/client'
import { KBTabs } from '@/components/KBTabs'
import { CitationMarker } from '@/components/CitationMarker'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { components } from '@/api/schema.d.ts'

type Citation = components['schemas']['Citation']

const CITATION_MARKER = /(\[\d+\])/g

// Marker [N] in the LLM's summary text must be matched to a citation by its
// `number` field, not by array position — citations is sparse whenever the
// model didn't cite every numbered chunk (see internal/search/search.go: Citation.Number).
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
  const [query, setQuery] = useState('')
  const [submittedQuery, setSubmittedQuery] = useState('')

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

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (query.trim()) setSubmittedQuery(query.trim())
  }

  return (
    <div className="mx-auto max-w-3xl px-6 py-8">
      <h1 className="mb-6 text-2xl font-bold">{kb?.name ?? '—'}</h1>
      <KBTabs kbId={kbId!} />

      <form onSubmit={handleSubmit} className="mb-8 flex gap-2">
        <Input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Ask a question about this knowledge base…"
        />
        <Button type="submit" disabled={!query.trim() || isFetching}>
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
          <div className="rounded-lg border bg-card p-4 text-sm leading-relaxed shadow-sm">
            {renderCitedSummary(result.summary, result.citations, kbId!)}
          </div>
        )
      )}
    </div>
  )
}
