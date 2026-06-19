import React, { createContext, useCallback, useContext, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { clearSession, getToken, getUser, saveSession, type AuthUser } from '@/lib/auth'
import { api } from '@/api/client'
import { clearSearchState } from '@/lib/search-state'

interface AuthContextValue {
  user: AuthUser | null
  isAuthenticated: boolean
  login: (email: string, password: string) => Promise<void>
  register: (email: string, password: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<AuthUser | null>(getUser)
  const queryClient = useQueryClient()

  const login = useCallback(async (email: string, password: string) => {
    const { data, error } = await api.POST('/auth/login', {
      body: { email, password },
    })
    if (error || !data) {
      throw new Error((error as { error?: string } | undefined)?.error ?? 'Login failed')
    }
    saveSession(data.token, data.user)
    setUser(data.user)
  }, [])

  const register = useCallback(async (email: string, password: string) => {
    const { data, error } = await api.POST('/auth/register', {
      body: { email, password },
    })
    if (error || !data) {
      throw new Error((error as { error?: string } | undefined)?.error ?? 'Registration failed')
    }
    saveSession(data.token, data.user)
    setUser(data.user)
  }, [])

  const logout = useCallback(() => {
    clearSession()
    // A same-tab account switch must never render the previous tenant's
    // cached KBs/documents/search results before the next user's data loads.
    queryClient.clear()
    clearSearchState()
    setUser(null)
  }, [queryClient])

  return (
    <AuthContext.Provider value={{ user, isAuthenticated: !!user, login, register, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

// eslint-disable-next-line react-refresh/only-export-components -- hook colocated with its provider, standard context pattern
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}

// eslint-disable-next-line react-refresh/only-export-components -- re-export for consumers that only need the token, not the full context
export { getToken }
