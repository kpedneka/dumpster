import { useCallback } from 'react'
import { useParams } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useDropzone } from 'react-dropzone'
import { FileText, Trash2, Upload } from 'lucide-react'
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
import { toast } from '@/hooks/use-toast'
import { cn } from '@/lib/utils'
import type { components } from '@/api/schema.d.ts'
import { useState } from 'react'

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
  const queryClient = useQueryClient()

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
    // Keep polling while any document is still being processed.
    refetchInterval: (query) => {
      const docs = query.state.data?.items ?? []
      return docs.some((d) => !TERMINAL.includes(d.status)) ? 2000 : false
    },
  })

  const docs: Document[] = docPage?.items ?? []

  const uploadMutation = useMutation({
    mutationFn: (file: File) => uploadDocument(kbId!, file),
    onSuccess: (doc) => {
      queryClient.setQueryData<typeof docPage>(['docs', kbId], (prev) =>
        prev ? { ...prev, items: [doc, ...prev.items] } : { items: [doc] },
      )
      toast({ title: `"${doc.filename}" uploaded — indexing…` })
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

  return (
    <div className="mx-auto max-w-3xl px-6 py-8">
      <h1 className="mb-6 text-2xl font-bold">{kb?.name ?? '—'}</h1>

      {/* Upload zone */}
      <div
        {...getRootProps()}
        className={cn(
          'mb-8 flex cursor-pointer flex-col items-center justify-center gap-3 rounded-xl border-2 border-dashed px-6 py-10 text-center transition-colors',
          isDragActive
            ? 'border-primary bg-primary/5'
            : 'border-border hover:border-primary/50 hover:bg-accent/30',
          uploadMutation.isPending && 'pointer-events-none opacity-60',
        )}
      >
        <input {...getInputProps()} />
        <Upload className="h-8 w-8 text-muted-foreground" />
        {isDragActive ? (
          <p className="text-sm font-medium">Drop to upload</p>
        ) : (
          <>
            <p className="text-sm font-medium">Drag &amp; drop files here, or click to select</p>
            <p className="text-xs text-muted-foreground">Supports .txt files</p>
          </>
        )}
        {uploadMutation.isPending && (
          <p className="text-xs text-muted-foreground">Uploading…</p>
        )}
      </div>

      {/* Document list */}
      {docs.length === 0 ? (
        <div className="flex flex-col items-center gap-3 py-16 text-center text-muted-foreground">
          <FileText className="h-10 w-10 opacity-30" />
          <p className="text-sm">No documents yet. Upload one above to get started.</p>
        </div>
      ) : (
        <div className="grid gap-2">
          {docs.map((doc) => (
            <DocRow key={doc.id} doc={doc} kbId={kbId!} />
          ))}
        </div>
      )}
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
    <div className="flex items-center gap-3 rounded-lg border bg-card px-4 py-3 shadow-sm">
      <FileText className="h-4 w-4 shrink-0 text-muted-foreground" />
      <span className="flex-1 truncate text-sm font-medium">{doc.filename}</span>
      <Badge
        variant={doc.status as DocStatus}
        className={cn(isActive && 'animate-pulse')}
      >
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
            <Button variant="outline" onClick={() => setConfirmOpen(false)}>Cancel</Button>
            <Button
              variant="destructive"
              onClick={() => { setConfirmOpen(false); deleteMutation.mutate() }}
            >
              Delete
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
