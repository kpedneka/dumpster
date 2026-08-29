import { Fragment, useCallback, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useDropzone } from 'react-dropzone'
import {
  AlertTriangle,
  FileText,
  MoreHorizontal,
  RotateCw,
  Search as SearchIcon,
  Trash2,
  Upload,
} from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import type { Components } from 'react-markdown'
import { api } from '@/api/client'
import { uploadDocument } from '@/api/upload'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { toast } from '@/hooks/use-toast'
import { useDeleteKB } from '@/hooks/use-delete-kb'
import { CitationMarker } from '@/components/CitationMarker'
import { StatusRing } from '@/components/StatusRing'
import { getDraftQuery, setDraftQuery, getSubmittedQuery, setSubmittedQuery } from '@/lib/search-state'
import { cn } from '@/lib/utils'
import type { components } from '@/api/schema.d.ts'

type Document = components['schemas']['Document']
type DocStatus = Document['status']
type Citation = components['schemas']['Citation']

const TERMINAL: DocStatus[] = ['indexed', 'failed']

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

function describeError(error: unknown, fallback = 'Something went wrong. Try again.'): string {
  if (error && typeof error === 'object' && typeof (error as { error?: unknown }).error === 'string') {
    return (error as { error: string }).error
  }
  return fallback
}

const SIZE_UNITS = ['B', 'KB', 'MB', 'GB']

function formatSize(bytes: number): string {
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < SIZE_UNITS.length - 1) {
    value /= 1024
    unit++
  }
  return `${unit === 0 ? value : value.toFixed(1)} ${SIZE_UNITS[unit]}`
}

export function KBDetailPage() {
  const { kbId } = useParams<{ kbId: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [deleteOpen, setDeleteOpen] = useState(false)
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

  const { data: docPage } = useQuery({
    queryKey: ['docs', kbId],
    queryFn: async () => {
      const { data, error } = await api.GET('/kbs/{kbId}/documents', {
        params: { path: { kbId: kbId! }, query: { limit: 50 } },
      })
      if (error) throw error
      return data
    },
    enabled: !!kbId,
    refetchInterval: (query) => {
      const docs = query.state.data?.items ?? []
      return docs.some((d) => !TERMINAL.includes(d.status)) ? 2000 : false
    },
  })

  const docs: Document[] = docPage?.items ?? []

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

  const deleteKBMutation = useDeleteKB({ onSuccess: () => navigate('/kbs') })

  const uploadMutation = useMutation({
    mutationFn: (file: File) => uploadDocument(kbId!, file),
    onSuccess: (doc) => {
      queryClient.setQueryData<typeof docPage>(['docs', kbId], (prev) =>
        prev ? { ...prev, items: [doc, ...prev.items] } : { items: [doc] },
      )
    },
    onError: (err) =>
      toast({ variant: 'destructive', title: 'Upload failed', description: String(err) }),
  })

  const onDrop = useCallback(
    (accepted: File[]) => {
      accepted.forEach((file) => uploadMutation.mutate(file))
    },
    [uploadMutation],
  )

  const { getRootProps, getInputProps, isDragActive } = useDropzone({
    onDrop,
    accept: {
      'text/plain': ['.txt'],
      'application/pdf': ['.pdf'],
    },
    multiple: true,
  })

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

  const uploadPanel = (
    <div className="flex flex-col gap-3">
      <div
        {...getRootProps()}
        className={cn(
          'flex cursor-pointer flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed px-6 py-9 text-center transition-colors',
          isDragActive
            ? 'border-primary bg-accent'
            : 'border-border hover:border-primary/50 hover:bg-accent/40',
          uploadMutation.isPending && 'pointer-events-none opacity-60',
        )}
      >
        <input {...getInputProps()} />
        <Upload className="h-7 w-7 text-primary" />
        {isDragActive ? (
          <p className="text-sm font-medium">Drop to upload</p>
        ) : (
          <>
            <p className="text-sm font-medium">Drop files or click to upload</p>
            <p className="text-xs text-muted-foreground">Supports .txt and .pdf files</p>
          </>
        )}
        {uploadMutation.isPending && <p className="text-xs text-muted-foreground">Uploading…</p>}
      </div>

      <div className="flex items-start gap-2 rounded-lg border border-amber-600/30 bg-amber-500/10 px-3 py-2.5 text-xs leading-relaxed text-amber-800 dark:text-amber-300">
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <span>
          Do not upload proprietary, sensitive, or confidential information. This is a public demo
          and uploaded content may be processed by third-party models. Please review our{' '}
          <Link to="/privacy" className="underline hover:text-amber-900 dark:hover:text-amber-200">
            privacy policy
          </Link>{' '}
          for understanding how data is handled.
        </span>
      </div>
    </div>
  )

  const noResults =
    !!result && result.citations.length === 0 && result.retrieved_files.length === 0

  return (
    <div className="rise mx-auto max-w-6xl px-6 py-8">
      <div className="mb-6 flex items-center justify-between">
        <h1 className="font-display text-2xl font-semibold tracking-tight">{kb?.name ?? '—'}</h1>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" className="h-8 w-8">
              <MoreHorizontal className="h-4 w-4" />
              <span className="sr-only">Knowledge base options</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              className="text-destructive focus:text-destructive"
              onSelect={() => setDeleteOpen(true)}
            >
              <Trash2 className="mr-2 h-4 w-4" />
              Delete knowledge base
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
      <Dialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>Delete knowledge base?</DialogTitle>
            <DialogDescription>
              <strong>{kb?.name}</strong> and all its documents will be permanently deleted.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setDeleteOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                setDeleteOpen(false)
                deleteKBMutation.mutate(kbId!)
              }}
            >
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Unified page (v4.5): search is the main content; upload + documents
          dock as a column beneath the search results on mobile/tablet, and to
          the right on desktop — same responsive pattern the two-tab layout
          used, just with search in the slot the document list used to own. */}
      <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_320px]">
        <aside className="order-first lg:order-last">
          <div className="flex flex-col gap-6 lg:sticky lg:top-4">
            <div>
              <h2 className="mb-1 font-display text-base font-semibold">Upload documents</h2>
              <p className="mb-3 text-xs text-muted-foreground">
                Add .txt and .pdf files to this knowledge base.
              </p>
              {uploadPanel}
            </div>

            <section>
              <div className="mb-3 flex items-center justify-between">
                <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                  Documents
                </span>
                <span className="rounded-full bg-muted px-2 py-0.5 text-[11px] text-muted-foreground">
                  {docs.length}
                </span>
              </div>
              {docs.length === 0 ? (
                <div className="flex flex-col items-center gap-3 py-10 text-center text-muted-foreground">
                  <FileText className="h-8 w-8 opacity-30" />
                  <p className="text-sm">No documents yet. Upload one to get started.</p>
                </div>
              ) : (
                <div className="rise-stagger grid gap-2 lg:max-h-[calc(100vh-28rem)] lg:overflow-y-auto lg:pr-1">
                  {docs.map((doc) => (
                    <DocRow key={doc.id} doc={doc} kbId={kbId!} />
                  ))}
                </div>
              )}
            </section>
          </div>
        </aside>

        <section>
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
            <p className="py-16 text-center text-sm text-destructive">
              {describeError(error, 'Search failed. Try again.')}
            </p>
          )}

          {submittedQuery && !isError && result && (
            noResults ? (
              <p className="py-16 text-center text-sm text-muted-foreground">No results found.</p>
            ) : (
              <div className="rise rounded-lg border border-border bg-card p-5 text-sm leading-relaxed shadow-sm">
                <ReactMarkdown components={mdComponents}>
                  {preprocessCitations(result.summary)}
                </ReactMarkdown>

                {result.retrieved_files.length > 0 && (
                  <div className="mt-4 border-t border-border pt-3">
                    <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                      Relevant files
                    </span>
                    <ul className="mt-2 flex flex-col gap-1.5">
                      {result.retrieved_files.map((f) => (
                        <li
                          key={f.document_id}
                          className="flex items-center gap-2 text-sm text-muted-foreground"
                        >
                          <FileText className="h-3.5 w-3.5 shrink-0" />
                          <span className="truncate">{f.file_name}</span>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
              </div>
            )
          )}
        </section>
      </div>
    </div>
  )
}

function DocRow({ doc, kbId }: { doc: Document; kbId: string }) {
  const queryClient = useQueryClient()
  const [confirmOpen, setConfirmOpen] = useState(false)

  const deleteMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE('/kbs/{kbId}/documents/{docId}', {
        params: { path: { kbId, docId: doc.id } },
      })
      if (error) throw error
    },
    onSuccess: () => {
      queryClient.setQueryData<{ items: Document[] }>(['docs', kbId], (prev) =>
        prev ? { ...prev, items: prev.items.filter((d) => d.id !== doc.id) } : prev,
      )
      toast({ title: `"${doc.filename}" deleted` })
    },
    onError: (error) =>
      toast({
        variant: 'destructive',
        title: 'Failed to delete document',
        description: describeError(error),
      }),
  })

  const retryMutation = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST('/kbs/{kbId}/documents/{docId}/retry', {
        params: { path: { kbId, docId: doc.id } },
      })
      if (error) throw error
      return data
    },
    onSuccess: (updated) => {
      queryClient.setQueryData<{ items: Document[] }>(['docs', kbId], (prev) =>
        prev ? { ...prev, items: prev.items.map((d) => (d.id === doc.id ? updated : d)) } : prev,
      )
    },
    onError: () => toast({ variant: 'destructive', title: 'Failed to retry document' }),
  })

  const isFailed = doc.status === 'failed'

  return (
    <div className="lift flex items-center gap-3 rounded-lg border border-border bg-card px-4 py-3 shadow-sm hover:border-primary/40">
      <FileText className="h-4 w-4 shrink-0 text-muted-foreground" />
      <div className="min-w-0 flex-1">
        <p className="break-words text-sm font-medium">{doc.filename}</p>
        <p className="text-xs text-muted-foreground">{formatSize(doc.size_bytes)}</p>
      </div>
      <StatusRing status={doc.status} updatedAt={doc.updated_at} />
      {isFailed && (
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 shrink-0 text-muted-foreground hover:text-primary"
          onClick={() => retryMutation.mutate()}
          disabled={retryMutation.isPending}
        >
          <RotateCw className={cn('h-3.5 w-3.5', retryMutation.isPending && 'animate-spin')} />
          <span className="sr-only">Retry indexing</span>
        </Button>
      )}
      <Button
        variant="ghost"
        size="icon"
        className="h-7 w-7 shrink-0 text-muted-foreground hover:text-destructive"
        onClick={() => setConfirmOpen(true)}
        disabled={deleteMutation.isPending}
      >
        <Trash2 className="h-3.5 w-3.5" />
        <span className="sr-only">Delete</span>
      </Button>

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>Delete document?</DialogTitle>
            <DialogDescription>
              <strong>{doc.filename}</strong> will be permanently removed.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              onClick={() => {
                setConfirmOpen(false)
                deleteMutation.mutate()
              }}
            >
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
