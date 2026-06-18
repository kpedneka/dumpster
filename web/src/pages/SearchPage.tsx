import { Fragment, useState } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Search as SearchIcon } from 'lucide-react'
import { api } from '@/api/client'
import { KBTabs } from '@/components/KBTabs'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

const CITATION_MARKER = /(\[\d+\])/g

function renderCitedSummary(summary: string) {
  return summary.split(CITATION_MARKER).map((part, i) =>
    CITATION_MARKER.test(part) ? (
      <sup key={i} className="ml-0.5 rounded bg-accent px-1 py-0.5 text-xs font-medium text-accent-foreground">
        {part}
      </sup>
    ) : (
      <Fragment key={i}>{part}</Fragment>
    ),
  )
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
            {renderCitedSummary(result.summary)}
          </div>
        )
      )}
    </div>
  )
}
