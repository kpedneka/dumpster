import { useParams } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/api/client'

export function KBDetailPage() {
  const { kbId } = useParams<{ kbId: string }>()

  const { data: kb, isLoading } = useQuery({
    queryKey: ['kbs', kbId],
    queryFn: async () => {
      const { data, error } = await api.GET('/kbs/{id}', { params: { path: { id: kbId! } } })
      if (error) throw error
      return data
    },
    enabled: !!kbId,
  })

  if (isLoading) {
    return (
      <div className="flex h-full items-center justify-center">
        <span className="text-sm text-muted-foreground">Loading…</span>
      </div>
    )
  }

  return (
    <div className="mx-auto max-w-3xl px-6 py-8">
      <h1 className="mb-6 text-2xl font-bold">{kb?.name ?? '—'}</h1>
      <p className="text-muted-foreground text-sm">
        Document upload and search coming soon (gate slices).
      </p>
    </div>
  )
}
