import { Fragment, useRef, useState, useMemo } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { Search as SearchIcon } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import type { Components } from 'react-markdown'
import { api } from '@/api/client'
import { CitationMarker } from '@/components/CitationMarker'
import { KBTabs } from '@/components/KBTabs'
import { Button } from '@/components/ui/button'
import { getDraftQuery, setDraftQuery, getSubmittedQuery, setSubmittedQuery } from '@/lib/search-state'
import { cn } from '@/lib/utils'
import type { components } from '@/api/schema.d.ts'

type Citation = components['schemas']['Citation']

// Replace [N] with {CITE_N} before passing to react-markdown so remark doesn't
// tokenize the brackets as a potential link reference, keeping citations as a
// single contiguous text token that injectCitations can reliably split on.
const CITATION_BRACKET_RE = /\[(\d+)\]/g
const CITATION_PLACEHOLDER_RE = /(\{CITE_\d+\})/

function preprocessCitations(summary: string): string {
  return summary.replace(CITATION_BRACKET_RE, '{CITE_$1}')
}

function injectCitations(children: React.ReactNode, citations: Citation[]): React.ReactNode {
  if (typeof children === 'string') {
    const parts = children.split(CITATION_PLACEHOLDER_RE)
    if (parts.length === 1) return children
    return parts.map((part, i) => {
      const m = part.match(/^\{CITE_(\d+)\}$/)
      if (!m) return part
      const cite = citations.find((c) => c.number === Number(m[1]))
      return cite ? <CitationMarker key={i} citation={cite} /> : `[${m[1]}]`
    })
  }
  if (Array.isArray(children)) {
    return (children as React.ReactNode[]).map((child, i) => (
      <Fragment key={i}>{injectCitations(child, citations)}</Fragment>
    ))
  }
  return children
}

function makeMarkdownComponents(citations: Citation[]): Components {
  const inject = (children: React.ReactNode) => injectCitations(children, citations)
  return {
    p: ({ children }) => <p className="mb-3 last:mb-0">{inject(children)}</p>,
    ul: ({ children }) => <ul className="mb-3 list-disc pl-5">{children}</ul>,
    ol: ({ children }) => <ol className="mb-3 list-decimal pl-5">{children}</ol>,
    li: ({ children }) => <li className="mb-1">{inject(children)}</li>,
    strong: ({ children }) => <strong className="font-semibold">{children}</strong>,
    h1: ({ children }) => <h1 className="mb-2 text-lg font-semibold">{inject(children)}</h1>,
    h2: ({ children }) => <h2 className="mb-2 text-base font-semibold">{inject(children)}</h2>,
    h3: ({ children }) => <h3 className="mb-2 text-sm font-semibold">{inject(children)}</h3>,
  }
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
    staleTime: Infinity,
  })

  const mdComponents = useMemo(
    () => (result ? makeMarkdownComponents(result.citations) : {}),
    [result],
  )

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

      {submittedQuery && isFetching && !result && (
        <p className="py-16 text-center text-sm text-muted-foreground">Searching…</p>
      )}

      {submittedQuery && !isFetching && isError && (
        <p className="py-16 text-center text-sm text-destructive">{describeError(error)}</p>
      )}

      {submittedQuery && !isError && result && (
        result.citations.length === 0 ? (
          <p className="py-16 text-center text-sm text-muted-foreground">No results found.</p>
        ) : (
          <div className="rise rounded-lg border border-border bg-card p-5 text-sm leading-relaxed shadow-sm">
            <ReactMarkdown components={mdComponents}>
              {preprocessCitations(result.summary)}
            </ReactMarkdown>
          </div>
        )
      )}
    </div>
  )
}
