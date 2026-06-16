import { Link, NavLink, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Database, LogOut, Plus, User } from 'lucide-react'
import { api } from '@/api/client'
import { useAuth } from '@/context/AuthContext'
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
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { toast } from '@/hooks/use-toast'
import type { components } from '@/api/schema.d.ts'

type KB = components['schemas']['KnowledgeBase']

const SIDEBAR_LIMIT = 5

export function Sidebar() {
  const { user, logout } = useAuth()
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
    },
    onError: () => toast({ variant: 'destructive', title: 'Failed to create knowledge base' }),
  })

  function handleCreate(e: React.FormEvent) {
    e.preventDefault()
    if (newName.trim()) createMutation.mutate(newName.trim())
  }

  function handleLogout() {
    logout()
    navigate('/login')
  }

  return (
    <aside className="flex h-full w-60 flex-col border-r bg-background">
      {/* Account menu */}
      <div className="border-b p-3">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" className="w-full justify-start gap-2 px-2">
              <User className="h-4 w-4 shrink-0" />
              <span className="truncate text-sm">{user?.email ?? '—'}</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="w-52">
            <DropdownMenuLabel className="font-normal">
              <div className="flex flex-col space-y-1">
                <p className="text-sm font-medium">Account</p>
                <p className="text-xs text-muted-foreground truncate">{user?.email}</p>
              </div>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem onClick={handleLogout}>
              <LogOut className="mr-2 h-4 w-4" />
              Sign out
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {/* KB navigation */}
      <div className="flex flex-1 flex-col overflow-hidden p-2">
        <div className="mb-1 flex items-center justify-between px-2 py-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
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

        <nav className="flex-1 overflow-y-auto space-y-0.5">
          {visible.length === 0 && (
            <p className="px-2 py-6 text-center text-xs text-muted-foreground">No knowledge bases yet</p>
          )}
          {visible.map((kb) => (
            <KBNavItem key={kb.id} kb={kb} />
          ))}
          {hasMore && (
            <Link
              to="/kbs"
              className="flex items-center gap-2 rounded-md px-2 py-1.5 text-xs text-muted-foreground hover:bg-accent hover:text-accent-foreground"
            >
              More knowledge bases…
            </Link>
          )}
        </nav>
      </div>
    </aside>
  )
}

function KBNavItem({ kb }: { kb: KB }) {
  return (
    <NavLink
      to={`/kbs/${kb.id}`}
      className={({ isActive }) =>
        `flex items-center gap-2 rounded-md px-2 py-1.5 text-sm transition-colors ${
          isActive
            ? 'bg-accent text-accent-foreground font-medium'
            : 'text-foreground/70 hover:bg-accent hover:text-accent-foreground'
        }`
      }
    >
      <Database className="h-3.5 w-3.5 shrink-0" />
      <span className="truncate">{kb.name}</span>
    </NavLink>
  )
}
