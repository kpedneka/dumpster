import { useState } from 'react'
import { Outlet } from 'react-router-dom'
import { Menu } from 'lucide-react'
import { Sidebar } from './Sidebar'
import { AccountStatusBanner } from './AccountStatusBanner'
import { Toaster } from '@/components/ui/toaster'

// v2.8: the left nav is a permanent rail on desktop (lg+) and collapses
// behind a hamburger on tablet/mobile. The same <Sidebar> is used in both
// modes; below lg it renders as an overlay drawer controlled by `navOpen`.
export function AppShell() {
  const [navOpen, setNavOpen] = useState(false)

  return (
    <div className="flex h-screen overflow-hidden bg-background text-foreground">
      {/* Permanent rail (desktop) */}
      <div className="hidden lg:flex">
        <Sidebar />
      </div>

      {/* Drawer + scrim (mobile / tablet) */}
      <div className="lg:hidden">
        <div
          onClick={() => setNavOpen(false)}
          className={
            'fixed inset-0 z-30 bg-black/40 transition-opacity duration-200 ' +
            (navOpen ? 'opacity-100' : 'pointer-events-none opacity-0')
          }
        />
        <div
          className={
            'fixed inset-y-0 left-0 z-40 transition-transform duration-200 ease-out ' +
            (navOpen ? 'translate-x-0' : '-translate-x-full')
          }
        >
          <Sidebar onNavigate={() => setNavOpen(false)} />
        </div>
      </div>

      <div className="flex flex-1 flex-col overflow-hidden">
        {/* Mobile top bar */}
        <div className="flex items-center gap-3 border-b border-border bg-background/80 px-4 py-2.5 backdrop-blur lg:hidden">
          <button
            onClick={() => setNavOpen(true)}
            className="grid h-9 w-9 place-items-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground"
            aria-label="Open navigation"
          >
            <Menu className="h-5 w-5" />
          </button>
          <span className="grid h-6 w-6 place-items-center rounded-md bg-primary font-display text-sm font-semibold text-primary-foreground">
            D
          </span>
          <span className="font-display text-base font-semibold tracking-tight">Dumpster</span>
        </div>

        <AccountStatusBanner />
        <main className="flex-1 overflow-y-auto">
          <Outlet />
        </main>
      </div>
      <Toaster />
    </div>
  )
}
