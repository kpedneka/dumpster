import React, { createContext, useContext, useEffect } from 'react'
import { useAuth as useClerkAuth, useUser } from '@clerk/clerk-react'
import { useQueryClient } from '@tanstack/react-query'
import { setTokenGetter, type AuthUser } from '@/lib/auth'
import { clearSearchState } from '@/lib/search-state'

interface AuthContextValue {
  user: AuthUser | null
  isAuthenticated: boolean
  /** True until Clerk has resolved the initial session state. */
  isLoaded: boolean
  logout: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

// Sign-up/sign-in are handled by Clerk's <SignIn>/<SignUp> components
// (rendered in LoginPage/RegisterPage), not by imperative email/password
// calls here — Clerk owns credential collection, MFA, and social-provider
// flows. AuthProvider's job is to expose the resulting session in the same
// shape downstream consumers (RequireAuth, page components) already use,
// and to bridge the session's token getter to the non-React API client.
export function AuthProvider({ children }: { children: React.ReactNode }) {
  const { isLoaded, isSignedIn, getToken, signOut } = useClerkAuth()
  const { user: clerkUser } = useUser()
  const queryClient = useQueryClient()

  // Keep lib/auth.ts's module-level token getter in sync with the current
  // session so api/client.ts and api/upload.ts (outside the React tree)
  // can always fetch a fresh token on demand.
  useEffect(() => {
    setTokenGetter(isSignedIn ? getToken : null)
    return () => setTokenGetter(null)
  }, [isSignedIn, getToken])

  const user: AuthUser | null =
    isSignedIn && clerkUser
      ? { id: clerkUser.id, email: clerkUser.primaryEmailAddress?.emailAddress ?? '' }
      : null

  const logout = async () => {
    await signOut()
    // A same-tab account switch must never render the previous tenant's
    // cached KBs/documents/search results before the next user's data loads.
    queryClient.clear()
    clearSearchState()
  }

  return (
    <AuthContext.Provider value={{ user, isAuthenticated: !!isSignedIn, isLoaded, logout }}>
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
