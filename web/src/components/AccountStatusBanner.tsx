import { useQuery } from '@tanstack/react-query'
import { api } from '@/api/client'
import { useAuth } from '@/context/AuthContext'

// Polls the demo account's TTL status and shows the day-6 warning banner
// alongside the warning email (see internal/server/accounthandler.go and
// internal/account.Sweep) so active users see it even without checking
// email.
export function AccountStatusBanner() {
  const { isAuthenticated } = useAuth()

  const { data } = useQuery({
    queryKey: ['account', 'status'],
    enabled: isAuthenticated,
    queryFn: async () => {
      const { data, error } = await api.GET('/account/status', {})
      if (error) throw error
      return data
    },
  })

  if (!data?.warning_active) return null

  const deletesAt = new Date(data.deletes_at).toLocaleDateString(undefined, {
    year: 'numeric',
    month: 'long',
    day: 'numeric',
  })

  return (
    <div
      role="alert"
      className="border-b border-yellow-600/30 bg-yellow-500/10 px-4 py-2 text-center text-sm text-yellow-700 dark:text-yellow-400"
    >
      This is a demo account. Your data will be permanently deleted on {deletesAt}.
    </div>
  )
}
