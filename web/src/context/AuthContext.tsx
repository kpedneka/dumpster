import React, { createContext, useContext } from 'react'

interface AuthContextValue {
  /** Always true: every visitor has a server-minted session cookie. */
  isAuthenticated: boolean
}

const AuthContext = createContext<AuthContextValue | null>(null)

/** Provides session-ready state to the component tree. */
export function AuthProvider({ children }: { children: React.ReactNode }) {
  return <AuthContext.Provider value={{ isAuthenticated: true }}>{children}</AuthContext.Provider>
}

// eslint-disable-next-line react-refresh/only-export-components -- hook colocated with its provider, standard context pattern
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}
