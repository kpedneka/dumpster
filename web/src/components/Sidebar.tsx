import { Link, NavLink, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Database, Plus } from 'lucide-react'
import { api } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from '@/components/ui/dialog'
import { toast } from '@/hooks/use-toast'
import type { components } from '@/api/schema.d.ts'

type KB = components['schemas']['KnowledgeBase']

const SIDEBAR_LIMIT = 5

// v2.8: `onNavigate` lets the mobile drawer close the nav after a selection.
// On desktop the prop is omitted and the rail stays open.
export function Sidebar({ onNavigate }: { onNavigate?: () => void } = {}) {
  const navigate = useNavigate()
  const queryClient = useQueryClient()
  const [createOpen, setCreateOpen] = useState(false)
  const [newName, setNewName] = useState('')

  const { data } = useQuery({
    queryKey: ['kbs', 'sidebar'],
    queryFn: async () => {
      const { data, error } = await api.GET('/kbs', { params: { query: { limit: SIDEBAR_LIMIT + 1 } } })
      if (error) throw error
      return data
    },
  })

  const kbs: KB[] = data?.items ?? []
  const hasMore = kbs.length > SIDEBAR_LIMIT
  const visible = kbs.slice(0, SIDEBAR_LIMIT)

  const createMutation = useMutation({
    mutationFn: async (name: string) => {
      const { data, error } = await api.POST('/kbs', { body: { name } })
      if (error) throw error
      return data
    },
    onSuccess: (kb) => {
      queryClient.invalidateQueries({ queryKey: ['kbs'] })
      setCreateOpen(false)
      setNewName('')
      navigate(`/kbs/${kb!.id}`)
      onNavigate?.()
    },
    onError: () => toast({ variant: 'destructive', title: 'Failed to create knowledge base' }),
  })

  function handleCreate(e: React.FormEvent) {
    e.preventDefault()
    if (newName.trim()) createMutation.mutate(newName.trim())
  }

  return (
    <aside className="flex h-full w-64 flex-col border-r border-border bg-background/70 backdrop-blur">
      {/* Brand — links back to the KB list, so there's always a way home. */}
      <Link
        to="/kbs"
        onClick={onNavigate}
        className="flex items-center gap-2.5 px-4 pb-3 pt-4 transition-opacity hover:opacity-80"
      >
        <span className="grid h-8 w-8 place-items-center rounded-lg bg-primary font-display text-lg font-semibold text-primary-foreground shadow-sm">
          D
        </span>
        <div>
          <div className="font-display text-lg font-semibold leading-none tracking-tight">Dumpster</div>
          <div className="mt-1 text-[11px] text-muted-foreground">Multimodal knowledge base</div>
        </div>
      </Link>

      {/* Session indicator */}
      <div className="px-4 pb-2">
        <p className="text-xs text-muted-foreground">Anonymous session</p>
      </div>

      {/* KB navigation */}
      <div className="flex flex-1 flex-col overflow-hidden px-2">
        <div className="mb-1 flex items-center justify-between px-2 py-1">
          <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            Knowledge Bases
          </span>
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button variant="ghost" size="icon" className="h-6 w-6">
                <Plus className="h-3.5 w-3.5" />
                <span className="sr-only">New knowledge base</span>
              </Button>
            </DialogTrigger>
            <DialogContent className="sm:max-w-sm">
              <DialogHeader>
                <DialogTitle>New knowledge base</DialogTitle>
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

        <nav className="flex-1 space-y-0.5 overflow-y-auto">
          {visible.length === 0 && (
            <p className="px-2 py-6 text-center text-xs text-muted-foreground">No knowledge bases yet</p>
          )}
          {visible.map((kb) => (
            <KBNavItem key={kb.id} kb={kb} onNavigate={onNavigate} />
          ))}
          {hasMore && (
            <Link
              to="/kbs"
              onClick={onNavigate}
              className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs text-muted-foreground hover:bg-accent hover:text-accent-foreground"
            >
              More knowledge bases…
            </Link>
          )}
        </nav>
      </div>

      {/* Footer */}
      <div className="border-t border-border px-4 py-2.5">
        <Link to="/privacy" className="text-[11px] text-muted-foreground hover:text-foreground">
          Privacy
        </Link>
      </div>
    </aside>
  )
}

function KBNavItem({ kb, onNavigate }: { kb: KB; onNavigate?: () => void }) {
  return (
    <NavLink
      to={`/kbs/${kb.id}`}
      onClick={onNavigate}
      className={({ isActive }) =>
        `flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors ${
          isActive
            ? 'bg-accent font-medium text-accent-foreground'
            : 'text-foreground/70 hover:bg-accent hover:text-accent-foreground'
        }`
      }
    >
      <Database className="h-3.5 w-3.5 shrink-0" />
      <span className="truncate">{kb.name}</span>
    </NavLink>
  )
}
