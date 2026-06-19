import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/api/client'
import { toast } from '@/hooks/use-toast'

/**
 * Shared delete-knowledge-base mutation used by both the KB list view and the
 * KB detail view, so the confirm/toast/cache-invalidation behavior stays
 * consistent wherever delete is triggered from.
 */
export function useDeleteKB(options?: { onSuccess?: () => void }) {
  const queryClient = useQueryClient()

  return useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE('/kbs/{id}', { params: { path: { id } } })
      if (error) throw error
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['kbs'] })
      toast({ title: 'Knowledge base deleted' })
      options?.onSuccess?.()
    },
    onError: () => toast({ variant: 'destructive', title: 'Failed to delete knowledge base' }),
  })
}
