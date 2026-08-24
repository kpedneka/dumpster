import { useCallback, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useDropzone } from 'react-dropzone'
import { AlertTriangle, FileText, MoreHorizontal, Trash2, Upload } from 'lucide-react'
import { api } from '@/api/client'
import { uploadDocument } from '@/api/upload'
import { Badge } from '@/components/ui/badge'
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
import { KBTabs } from '@/components/KBTabs'
import { cn } from '@/lib/utils'
import type { components } from '@/api/schema.d.ts'

type Document = components['schemas']['Document']
type DocStatus = Document['status']

const TERMINAL: DocStatus[] = ['indexed', 'failed']

const STATUS_LABEL: Record<DocStatus, string> = {
  pending: 'Pending',
  processing: 'Processing…',
  indexed: 'Indexed',
  failed: 'Failed',
}

export function KBDetailPage() {
  const { kbId } = useParams<{ kbId: string }>()
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [deleteOpen, setDeleteOpen] = useState(false)

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
    accept: { 'text/plain': ['.txt'] },
    multiple: true,
  })

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
            <p className="text-xs text-muted-foreground">Supports .txt files</p>
          </>
        )}
        {uploadMutation.isPending && <p className="text-xs text-muted-foreground">Uploading…</p>}
      </div>

      {/* v2.8: confidentiality warning */}
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

      <KBTabs kbId={kbId!} />

      {/* v2.8 layout: documents (main) + docked upload panel (right) on desktop;
          stacks on mobile/tablet with upload first. */}
      <div className="grid gap-8 lg:grid-cols-[minmax(0,1fr)_320px]">
        {/* Upload — first on mobile, docked right on desktop */}
        <aside className="order-first lg:order-last">
          <div className="lg:sticky lg:top-4">
            <h2 className="mb-1 font-display text-base font-semibold">Upload documents</h2>
            <p className="mb-3 text-xs text-muted-foreground">Add .txt files to this knowledge base.</p>
            {uploadPanel}
          </div>
        </aside>

        {/* Document list — scrollable region once it grows past ~5 items */}
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
            <div className="flex flex-col items-center gap-3 py-16 text-center text-muted-foreground">
              <FileText className="h-10 w-10 opacity-30" />
              <p className="text-sm">No documents yet. Upload one to get started.</p>
            </div>
          ) : (
            <div className="rise-stagger grid gap-2 lg:max-h-[calc(100vh-15rem)] lg:overflow-y-auto lg:pr-1">
              {docs.map((doc) => (
                <DocRow key={doc.id} doc={doc} kbId={kbId!} />
              ))}
            </div>
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
    onError: () => toast({ variant: 'destructive', title: 'Failed to delete document' }),
  })

  const isActive = !TERMINAL.includes(doc.status)

  return (
    <div className="lift flex items-center gap-3 rounded-lg border border-border bg-card px-4 py-3 shadow-sm hover:border-primary/40">
      <FileText className="h-4 w-4 shrink-0 text-muted-foreground" />
      <span className="flex-1 truncate text-sm font-medium">{doc.filename}</span>
      <Badge variant={doc.status as DocStatus} className={cn(isActive && 'animate-pulse')}>
        {STATUS_LABEL[doc.status]}
      </Badge>
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
