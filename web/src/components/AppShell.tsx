import { Outlet } from 'react-router-dom'
import { Sidebar } from './Sidebar'
import { AccountStatusBanner } from './AccountStatusBanner'
import { Toaster } from '@/components/ui/toaster'

export function AppShell() {
  return (
    <div className="flex h-screen overflow-hidden bg-background text-foreground">
      <Sidebar />
      <div className="flex flex-1 flex-col overflow-hidden">
        <AccountStatusBanner />
        <main className="flex-1 overflow-y-auto">
          <Outlet />
        </main>
      </div>
      <Toaster />
    </div>
  )
}
