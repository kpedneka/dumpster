import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useDropzone } from 'react-dropzone'
import {
  AlertTriangle,
  FileText,
  RotateCw,
  Search as SearchIcon,
  Trash2,
  Upload,
} from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import type { Components } from 'react-markdown'
import { api } from '@/api/client'
import { uploadDocument } from '@/api/upload'
import { searchStream, reevaluateStream, type SearchStreamEvent } from '@/api/searchStream'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import { useDeleteKB } from '@/hooks/use-delete-kb'
import { CitationMarker } from '@/components/CitationMarker'
import { StatusRing } from '@/components/StatusRing'
import { getDraftQuery, setDraftQuery } from '@/lib/search-state'
import { fileIconSrc } from '@/lib/file-icons'
import { cn } from '@/lib/utils'
import type { components } from '@/api/schema.d.ts'

type Document = components['schemas']['Document']
type DocStatus = Document['status']
type Citation = components['schemas']['Citation']
type RetrievedFile = components['schemas']['RetrievedFile']
type InquiryMessage = components['schemas']['InquiryMessage']

// PendingTurnKind identifies a pending turn without the streaming-state
// fields — passed into runStream and spread into PendingTurn once
// streaming starts. Kept as its own type because Omit doesn't distribute
// over a union the way spreading this in does: Omit<PendingTurn, ...>
// collapses the discriminated union into an invalid merged shape.
type PendingTurnKind = { kind: 'search' } | { kind: 'reevaluate'; anchorMessageId: string }

// PendingTurn is the turn currently streaming in, not yet reconciled with
// the persisted Inquiry. 'search' turns render after every historical
// message; 'reevaluate' turns render inline under anchorMessageId, the
// assistant message being re-answered.
type PendingTurn = PendingTurnKind & {
  status: 'loading' | 'error'
  summary: string
  citations: Citation[]
  retrievedFiles: RetrievedFile[]
  retrievalMs: number | null
  error: unknown
}

const TERMINAL: DocStatus[] = ['indexed', 'failed']

// Replace [N] with {CITE_N} before passing to react-markdown so remark doesn't
// tokenize the brackets as a potential link reference, keeping citations as a
// single contiguous text token that injectCitations can reliably split on.
const CITATION_BRACKET_RE = /\[(\d+)\]/g
const CITATION_PLACEHOLDER_RE = /(\{CITE_\d+\})/

function preprocessCitations(summary: string): string {
  return summary.replace(CITATION_BRACKET_RE, '{CITE_$1}')
}

// Surfaces exactly where in a source to look, for the subset of relevant
// files the model actually cited — computed from data already on the page
// (Citation.document_id + Citation.locator), no extra request. A file with
// several cited pages shows all of them, deduped and in page order; a
// relevant-but-never-cited file, or a non-paginated one (plain text), shows
// none — there's no locator to show either way. Chosen over cross-highlight
// hover/focus between citations and this list: that broke down the moment
// the answer was long enough to scroll (a very likely case, and the whole
// point of "which page do I check" is most valuable for long documents),
// and doesn't work on mobile at all. This has no such dependency — it's
// static text, always visible, answering the same question up front.
// fetchInquiry is shared between the mount-time useQuery and runStream's
// post-completion re-fetch (via queryClient.fetchQuery) so both go through
// identical fetch/error logic — see runStream for why that reuse matters.
async function fetchInquiry(kbId: string) {
  const { data, error } = await api.GET('/kbs/{id}/inquiry', { params: { path: { id: kbId } } })
  if (error) throw error
  return data
}

function citedPagesFor(documentId: string, citations: Citation[]): number[] {
  const pages = new Set<number>()
  for (const c of citations) {
    if (c.document_id === documentId && c.locator?.type === 'page') {
      pages.add(c.locator.value)
    }
  }
  return Array.from(pages).sort((a, b) => a - b)
}

const CITATION_NUMBER_RE = /^\{CITE_(\d+)\}$/
const WHITESPACE_ONLY_RE = /^\s*$/

// Adjacent citations supporting one claim (buildPrompt asks the model to
// place them with no separator, e.g. "[1][2]") are grouped into a single
// CitationMarker rather than rendered as one badge per number — whitespace
// between two resolvable citation numbers doesn't break the cluster, since
// the model isn't guaranteed to omit it even when asked to.
function injectCitations(children: React.ReactNode, citations: Citation[]): React.ReactNode {
  if (typeof children === 'string') {
    const parts = children.split(CITATION_PLACEHOLDER_RE)
    if (parts.length === 1) return children

    const nodes: React.ReactNode[] = []
    let cluster: Citation[] = []

    const flushCluster = (key: string) => {
      if (cluster.length === 0) return
      nodes.push(<CitationMarker key={key} citations={cluster} />)
      cluster = []
    }

    parts.forEach((part, i) => {
      const m = part.match(CITATION_NUMBER_RE)
      if (m) {
        const cite = citations.find((c) => c.number === Number(m[1]))
        if (cite) {
          cluster.push(cite)
          return
        }
        // Unresolvable citation number: close any open cluster, then fall
        // back to the literal marker text, matching prior behavior.
        flushCluster(`c-${i}`)
        nodes.push(`[${m[1]}]`)
        return
      }
      if (cluster.length > 0 && WHITESPACE_ONLY_RE.test(part)) {
        return
      }
      flushCluster(`c-${i}`)
      nodes.push(<Fragment key={`t-${i}`}>{part}</Fragment>)
    })
    flushCluster('c-end')

    return nodes
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

// AnswerBlock renders one answer's report-style card — used for both a
// historical (already-persisted) Inquiry message and the turn currently
// streaming in. isStreaming gates the "still in progress" placeholders
// (an empty body, retrieval not back yet); a historical message is never
// in either state, so it always renders its final content directly.
function AnswerBlock({
  content,
  citations,
  retrievedFiles,
  retrievalMs,
  isStreaming,
}: {
  content: string
  citations: Citation[]
  retrievedFiles: RetrievedFile[]
  retrievalMs: number | null
  isStreaming: boolean
}) {
  const mdComponents = useMemo(() => makeMarkdownComponents(citations), [citations])

  return (
    <div className="rise rounded-lg border border-border bg-card p-5 text-sm leading-relaxed shadow-sm">
      {content === '' && isStreaming ? (
        <p className="text-muted-foreground">Awaiting LLM summarization…</p>
      ) : (
        <ReactMarkdown components={mdComponents}>{preprocessCitations(content)}</ReactMarkdown>
      )}

      <div className="mt-4 border-t border-border pt-3">
        <div className="flex items-center justify-between gap-2">
          <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            Relevant files
          </span>
          {isStreaming && retrievalMs !== null && (
            <span className="text-[11px] text-muted-foreground">
              Found {retrievedFiles.length} file{retrievedFiles.length === 1 ? '' : 's'} in {retrievalMs}ms
            </span>
          )}
        </div>
        {isStreaming && retrievalMs === null ? (
          <p className="mt-2 text-xs text-muted-foreground">Finding relevant files…</p>
        ) : retrievedFiles.length === 0 ? (
          <p className="mt-2 text-xs text-muted-foreground">No relevant files found.</p>
        ) : (
          <>
            <p className="mt-1 text-[11px] text-muted-foreground">Ranked most to least relevant</p>
            <ul className="mt-2 flex flex-col gap-1.5">
              {retrievedFiles.map((f) => {
                const pages = citedPagesFor(f.document_id, citations)
                return (
                  <li key={f.document_id} className="flex items-center gap-2 text-sm text-muted-foreground">
                    <img src={fileIconSrc(f.file_name)} alt="" className="h-3.5 w-3.5 shrink-0" />
                    <span className="min-w-0 truncate">
                      {f.file_name}
                      {pages.length > 0 && (
                        <span className="text-muted-foreground/70">{` · p. ${pages.join(', ')}`}</span>
                      )}
                    </span>
                  </li>
                )
              })}
            </ul>
          </>
        )}
      </div>
    </div>
  )
}

function describeError(error: unknown, fallback = 'Something went wrong. Try again.'): string {
  if (error && typeof error === 'object' && typeof (error as { error?: unknown }).error === 'string') {
    return (error as { error: string }).error
  }
  return fallback
}

// PendingAnswerBlock renders the turn currently streaming in — an
// AnswerBlock while it's in progress, or the error state if the stream
// failed. Once it completes, the caller refetches the Inquiry and this
// disappears in favor of the now-persisted message (see runStream).
function PendingAnswerBlock({ pending }: { pending: PendingTurn }) {
  if (pending.status === 'error') {
    return (
      <p className="py-4 text-center text-sm text-destructive">
        {describeError(pending.error, 'Search failed. Try again.')}
      </p>
    )
  }
  return (
    <AnswerBlock
      content={pending.summary}
      citations={pending.citations}
      retrievedFiles={pending.retrievedFiles}
      retrievalMs={pending.retrievalMs}
      isStreaming
    />
  )
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
  const [duplicateQueue, setDuplicateQueue] = useState<File[]>([])
  const [query, setQuery] = useState(() => getDraftQuery(kbId!))
  const taRef = useRef<HTMLTextAreaElement>(null)

  const { data: kb, error: kbError } = useQuery({
    queryKey: ['kbs', kbId],
    queryFn: async () => {
      const { data, error, response } = await api.GET('/kbs/{id}', { params: { path: { id: kbId! } } })
      if (error) throw new Error('kb fetch failed', { cause: response.status })
      return data
    },
    enabled: !!kbId,
    // Retrying a 404 (e.g. a KB the demo sweeper already deleted while this
    // tab sat open) just delays the redirect below for no benefit.
    retry: false,
  })

  // A tab left open past the sweeper's deletion of its KB otherwise renders
  // an empty, broken-looking page on reload rather than sending the user
  // somewhere useful.
  useEffect(() => {
    if (kbError instanceof Error && kbError.cause === 404) {
      navigate('/kbs', { replace: true })
    }
  }, [kbError, navigate])

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

  const docs: Document[] = useMemo(() => docPage?.items ?? [], [docPage])

  // The persisted turn history for this KB's Inquiry — the source of truth
  // for everything already asked and answered. A fresh KB with no searches
  // yet has messages: [], not an error (see GET /kbs/{id}/inquiry).
  const { data: inquiryData } = useQuery({
    queryKey: ['inquiry', kbId],
    queryFn: () => fetchInquiry(kbId!),
    enabled: !!kbId,
  })
  const messages: InquiryMessage[] = inquiryData?.messages ?? []

  // The turn currently streaming in — a fresh search (kind: 'search',
  // appended after every historical turn) or a re-evaluation (kind:
  // 'reevaluate', rendered inline under the message it's re-answering via
  // anchorMessageId). Cleared once the Inquiry refetch above picks up the
  // now-persisted message, so the canonical, ID-bearing copy from the
  // server takes over rendering — see runStream.
  const [pending, setPending] = useState<PendingTurn | null>(null)

  // Streaming (not react-query) because the response arrives incrementally
  // as Server-Sent Events, not as one resolved value — see api/searchStream.
  const runStream = useCallback(
    async (base: PendingTurnKind, stream: (onEvent: (e: SearchStreamEvent) => void, signal: AbortSignal) => Promise<void>) => {
      const controller = new AbortController()
      setPending({ ...base, status: 'loading', summary: '', citations: [], retrievedFiles: [], retrievalMs: null, error: null })
      const startedAt = performance.now()
      // An "error" SSE event doesn't reject the stream() promise below — the
      // HTTP request itself succeeded, it's the generation that failed
      // server-side after streaming had already started (see the search
      // handler's doc comment on this distinction). Track it separately so
      // a successful stream() resolution isn't mistaken for a successful
      // turn and refetched over the error state just set.
      let streamErrored = false

      try {
        await stream((event) => {
          switch (event.type) {
            case 'retrieved_files':
              setPending((p) => (p ? { ...p, retrievedFiles: event.retrieved_files, retrievalMs: Math.round(performance.now() - startedAt) } : p))
              break
            case 'delta':
              // Live-typing preview. The done event below replaces this
              // wholesale with the authoritative parsed summary — see
              // footerFilter's doc comment on why the two can rarely
              // diverge for the tail of a response, and why that's fine.
              setPending((p) => (p ? { ...p, summary: p.summary + event.text } : p))
              break
            case 'done':
              setPending((p) => (p ? { ...p, summary: event.summary, citations: event.citations } : p))
              break
            case 'error':
              // Shaped like the API's usual {error: string} body so the
              // shared describeError helper below renders it the same way.
              streamErrored = true
              setPending((p) => (p ? { ...p, status: 'error', error: { error: event.error } } : p))
              break
          }
        }, controller.signal)
        if (controller.signal.aborted) return
        if (streamErrored) return
      } catch (err) {
        if (controller.signal.aborted) return
        setPending((p) => (p ? { ...p, status: 'error', error: err } : p))
        return controller
      }

      // The stream completed successfully: refetch so the canonical,
      // persisted (and ID-bearing, re-evaluatable) message replaces this
      // pending turn in the list above. Deliberately its own try/catch, not
      // folded into the one above — a failed refetch here (e.g. persistence
      // silently didn't happen server-side) must not be treated the same as
      // a failed *generation*: the answer the researcher just watched
      // stream in is still good, so on refetch failure pending is simply
      // left showing it as-is rather than discarded or replaced with an
      // error. It reconciles with the server on the next successful fetch
      // (e.g. a later reload).
      try {
        await queryClient.fetchQuery({ queryKey: ['inquiry', kbId], queryFn: () => fetchInquiry(kbId!) })
        setPending(null)
      } catch {
        // Leave pending as-is — see comment above.
      }

      return controller
    },
    [kbId, queryClient],
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

  // A perceived "stall" (the upload request itself returns quickly; it's
  // the async indexing that can take a while — see v4.6) is exactly the
  // situation that invites a user to re-upload the same file, thinking the
  // first attempt failed. That produces a real duplicate document rather
  // than progress on the original. Same filename in the same KB gets a
  // confirmation gate instead of uploading silently.
  const onDrop = useCallback(
    (accepted: File[]) => {
      const existingNames = new Set(docs.map((d) => d.filename))
      const duplicates: File[] = []
      for (const file of accepted) {
        if (existingNames.has(file.name)) {
          duplicates.push(file)
        } else {
          uploadMutation.mutate(file)
        }
      }
      if (duplicates.length > 0) {
        setDuplicateQueue((prev) => [...prev, ...duplicates])
      }
    },
    [docs, uploadMutation],
  )

  const currentDuplicate = duplicateQueue[0] ?? null

  function resolveDuplicate(shouldUpload: boolean) {
    if (shouldUpload && currentDuplicate) {
      uploadMutation.mutate(currentDuplicate)
    }
    setDuplicateQueue((prev) => prev.slice(1))
  }

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
    if (!trimmed || pending?.status === 'loading') return
    setQuery('')
    setDraftQuery(kbId!, '')
    void runStream({ kind: 'search' }, (onEvent, signal) => searchStream(kbId!, trimmed, onEvent, signal))
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    submit()
  }

  function reevaluate(messageId: string) {
    if (pending?.status === 'loading') return
    void runStream({ kind: 'reevaluate', anchorMessageId: messageId }, (onEvent, signal) =>
      reevaluateStream(kbId!, messageId, onEvent, signal),
    )
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

  return (
    <div className="rise mx-auto max-w-6xl px-6 py-8">
      <div className="mb-6 flex items-center justify-between">
        <h1 className="min-w-0 wrap-anywhere font-display text-2xl font-semibold tracking-tight">
          {kb?.name ?? '—'}
        </h1>
        <Button variant="destructive" size="sm" onClick={() => setDeleteOpen(true)}>
          <Trash2 className="h-4 w-4" />
          Delete knowledge base
        </Button>
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
      <Dialog open={currentDuplicate !== null} onOpenChange={(open) => !open && resolveDuplicate(false)}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>File already exists</DialogTitle>
            <DialogDescription>
              A file named <strong>{currentDuplicate?.name}</strong> already exists in this
              knowledge base. Upload anyway?
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" onClick={() => resolveDuplicate(false)}>
              Cancel
            </Button>
            <Button onClick={() => resolveDuplicate(true)}>Upload anyway</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Unified page (v4.5): search is the main content; upload + documents
          dock as a column beneath the search results on mobile/tablet, and to
          the right on desktop — same responsive pattern the two-tab layout
          used, just with search in the slot the document list used to own. */}
      <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_320px]">
        <aside className="order-last">
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
          <form onSubmit={handleSubmit} className="flex items-start gap-2">
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
                placeholder="Query this knowledge base…"
                className={cn(
                  'flex w-full resize-none overflow-hidden rounded-md border border-input bg-background px-3 py-2 pl-9',
                  'text-sm leading-6 shadow-sm outline-none transition-colors placeholder:text-muted-foreground',
                  'focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/40',
                )}
              />
            </div>
            <Button type="submit" disabled={!query.trim() || pending?.status === 'loading'} className="lift">
              <SearchIcon className="h-4 w-4" />
              Query
            </Button>
          </form>
          <p className="mb-8 mt-1.5 text-[11px] text-muted-foreground">
            Each query is answered independently — it won't reference earlier queries in this inquiry.
          </p>

          {messages.length === 0 && !pending && (
            <p className="py-16 text-center text-sm text-muted-foreground">
              Query this knowledge base above to begin your inquiry.
            </p>
          )}

          <div className="flex flex-col gap-4">
            {messages.map((m) =>
              m.role === 'user' ? (
                <p key={m.id} className="text-sm font-medium">
                  {m.content}
                </p>
              ) : (
                <div key={m.id} className="flex flex-col gap-2">
                  {m.supersedes_message_id && (
                    <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
                      <RotateCw className="h-3 w-3" />
                      Re-evaluated answer
                    </span>
                  )}
                  <AnswerBlock
                    content={m.content}
                    citations={m.citations}
                    retrievedFiles={m.retrieved_documents}
                    retrievalMs={null}
                    isStreaming={false}
                  />
                  <button
                    type="button"
                    onClick={() => reevaluate(m.id)}
                    disabled={pending?.status === 'loading'}
                    className="self-start text-[11px] font-medium text-muted-foreground underline underline-offset-2 hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
                  >
                    Re-evaluate against the current knowledge base
                  </button>
                  {pending?.kind === 'reevaluate' && pending.anchorMessageId === m.id && (
                    <PendingAnswerBlock pending={pending} />
                  )}
                </div>
              ),
            )}

            {pending?.kind === 'search' && <PendingAnswerBlock pending={pending} />}
          </div>
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
      <img src={fileIconSrc(doc.filename)} alt="" className="h-4 w-4 shrink-0" />
      <div className="min-w-0 flex-1">
        <p className="wrap-anywhere text-sm font-medium">{doc.filename}</p>
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
