import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Database, MoreHorizontal, Plus, Trash2 } from 'lucide-react'
import { api } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { toast } from '@/hooks/use-toast'
import { useDeleteKB } from '@/hooks/use-delete-kb'
import type { components } from '@/api/schema.d.ts'

type KB = components['schemas']['KnowledgeBase']

export function KBsPage() {
  const queryClient = useQueryClient()
  const [createOpen, setCreateOpen] = useState(false)
  const [newName, setNewName] = useState('')

  const {
    data: infiniteData,
    isLoading,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useInfiniteQuery({
    queryKey: ['kbs', 'list'],
    queryFn: async ({ pageParam }: { pageParam: string | undefined }) => {
      const { data, error } = await api.GET('/kbs', {
        params: { query: { limit: 20, ...(pageParam ? { after: pageParam } : {}) } },
      })
      if (error) throw error
      return data!
    },
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.next_cursor ?? undefined,
  })

  const createMutation = useMutation({
    mutationFn: async (name: string) => {
      const { data, error } = await api.POST('/kbs', { body: { name } })
      if (error) throw error
      return data
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['kbs'] })
      setCreateOpen(false)
      setNewName('')
      toast({ title: 'Knowledge base created' })
    },
    onError: () => toast({ variant: 'destructive', title: 'Failed to create knowledge base' }),
  })

  const deleteMutation = useDeleteKB()

  function handleCreate(e: React.FormEvent) {
    e.preventDefault()
    if (newName.trim()) createMutation.mutate(newName.trim())
  }

  const kbs: KB[] = infiniteData?.pages.flatMap((p) => p.items) ?? []

  return (
    <div className="mx-auto max-w-3xl px-6 py-8">
      <div className="mb-6 flex items-center justify-between">
        <h1 className="text-2xl font-bold">Knowledge Bases</h1>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button size="sm">
              <Plus className="mr-1.5 h-4 w-4" />
              New
            </Button>
          </DialogTrigger>
          <DialogContent className="sm:max-w-sm">
            <DialogHeader>
              <DialogTitle>New knowledge base</DialogTitle>
              <DialogDescription>Give your knowledge base a name to get started.</DialogDescription>
            </DialogHeader>
            <form onSubmit={handleCreate}>
              <Input
                placeholder="Name"
                value={newName}
                onChange={(e) => setNewName(e.target.value)}
                autoFocus
                className="mb-4 mt-2"
              />
              <DialogFooter>
                <Button type="submit" disabled={!newName.trim() || createMutation.isPending}>
                  {createMutation.isPending ? 'Creating…' : 'Create'}
                </Button>
              </DialogFooter>
            </form>
          </DialogContent>
        </Dialog>
      </div>

      {isLoading && (
        <div className="grid gap-3">
          {[...Array(3)].map((_, i) => (
            <div key={i} className="h-16 rounded-lg border bg-muted/40 animate-pulse" />
          ))}
        </div>
      )}

      {!isLoading && kbs.length === 0 && (
        <div className="flex flex-col items-center gap-4 py-20 text-center text-muted-foreground">
          <Database className="h-10 w-10 opacity-30" />
          <p>No knowledge bases yet. Create one to get started.</p>
        </div>
      )}

      {kbs.length > 0 && (
        <div className="grid gap-3">
          {kbs.map((kb) => (
            <KBRow key={kb.id} kb={kb} onDelete={() => deleteMutation.mutate(kb.id)} />
          ))}
        </div>
      )}

      {hasNextPage && (
        <div className="mt-6 flex justify-center">
          <Button variant="outline" size="sm" onClick={() => fetchNextPage()} disabled={isFetchingNextPage}>
            <MoreHorizontal className="mr-1.5 h-4 w-4" />
            {isFetchingNextPage ? 'Loading…' : 'Load more'}
          </Button>
        </div>
      )}
    </div>
  )
}

function KBRow({ kb, onDelete }: { kb: KB; onDelete: () => void }) {
  const [confirmOpen, setConfirmOpen] = useState(false)

  return (
    <div className="flex items-center justify-between rounded-lg border bg-card px-4 py-3 shadow-sm hover:bg-accent/30 transition-colors">
      <Link to={`/kbs/${kb.id}`} className="flex flex-1 items-center gap-3 min-w-0">
        <Database className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="truncate font-medium">{kb.name}</span>
      </Link>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="h-8 w-8 shrink-0">
            <MoreHorizontal className="h-4 w-4" />
            <span className="sr-only">KB options</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            className="text-destructive focus:text-destructive"
            onSelect={() => setConfirmOpen(true)}
          >
            <Trash2 className="mr-2 h-4 w-4" />
            Delete
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>

      <Dialog open={confirmOpen} onOpenChange={setConfirmOpen}>
        <DialogContent className="sm:max-w-sm">
          <DialogHeader>
            <DialogTitle>Delete knowledge base?</DialogTitle>
            <DialogDescription>
              <strong>{kb.name}</strong> and all its documents will be permanently deleted.
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
                onDelete()
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
