import React, { createContext, useContext, useEffect, useState } from 'react'
import { ensureSession } from '@/api/session'

interface AuthContextValue {
  /** Always true: every visitor has a server-minted session cookie. */
  isAuthenticated: boolean
}

const AuthContext = createContext<AuthContextValue | null>(null)

/**
 * Provides session-ready state to the component tree. Delays rendering
 * children until ensureSession's bootstrap request settles, so every
 * component further down the tree that fires its own request on mount
 * (Sidebar, AccountStatusBanner, KBsPage, ...) already has a session
 * cookie by the time it does -- see ensureSession's doc for the race this
 * closes. Fails open: a bootstrap error still renders children rather
 * than blocking the app, since the worst case is falling back to the
 * original per-request race for that one page load, not a functional
 * break.
 */
export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [ready, setReady] = useState(false)

  useEffect(() => {
    let cancelled = false
    ensureSession()
      .catch(() => undefined) // fail open -- see doc above
      .finally(() => {
        if (!cancelled) setReady(true)
      })
    return () => {
      cancelled = true
    }
  }, [])

  if (!ready) return null

  return <AuthContext.Provider value={{ isAuthenticated: true }}>{children}</AuthContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components -- hook colocated with its provider, standard context pattern
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}
