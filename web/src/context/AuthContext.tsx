import React, { createContext, useCallback, useContext, useState } from 'react'
import { clearSession, getToken, getUser, saveSession, type AuthUser } from '@/lib/auth'
import { api } from '@/api/client'

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
    setUser(null)
  }, [])

  return (
    <AuthContext.Provider value={{ user, isAuthenticated: !!user, login, register, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used inside AuthProvider')
  return ctx
}

// Trigger a re-read of the token from storage (used by the API client interceptor).
export { getToken }
