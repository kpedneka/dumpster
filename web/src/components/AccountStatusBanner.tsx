import { useQuery } from '@tanstack/react-query'
import { api } from '@/api/client'

/**
 * Shows a warning banner when the current session is within 15 minutes of
 * being swept due to inactivity. Hidden when the warning is not yet active.
 */
export function AccountStatusBanner() {
  const { data } = useQuery({
    queryKey: ['account', 'status'],
    queryFn: async () => {
      const { data, error } = await api.GET('/account/status', {})
      if (error) throw error
      return data
    },
  })

  if (!data?.warning_active) return null

  const expiresAt = new Date(data.deletes_at).toLocaleTimeString(undefined, {
    hour: 'numeric',
    minute: '2-digit',
  })

  return (
    <div
      role="alert"
      className="border-b border-yellow-600/30 bg-yellow-500/10 px-4 py-2 text-center text-sm text-yellow-700 dark:text-yellow-400"
    >
      Your session and all documents will be permanently deleted at {expiresAt} due to inactivity.
    </div>
  )
}
