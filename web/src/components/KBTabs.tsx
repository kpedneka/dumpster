import { NavLink } from 'react-router-dom'
import { cn } from '@/lib/utils'

export function KBTabs({ kbId }: { kbId: string }) {
  const tabs = [
    { label: 'Documents', to: `/kbs/${kbId}`, end: true },
    { label: 'Search', to: `/kbs/${kbId}/search`, end: false },
  ]

  return (
    <nav className="mb-6 flex gap-1 border-b">
      {tabs.map((tab) => (
        <NavLink
          key={tab.to}
          to={tab.to}
          end={tab.end}
          className={({ isActive }) =>
            cn(
              'border-b-2 px-3 py-2 text-sm font-medium transition-colors',
              isActive
                ? 'border-primary text-foreground'
                : 'border-transparent text-muted-foreground hover:text-foreground',
            )
          }
        >
          {tab.label}
        </NavLink>
      ))}
    </nav>
  )
}
