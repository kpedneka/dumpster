// Bridges Clerk's React-hook-based session API to the plain (non-React)
// modules that need a bearer token outside of component render — the
// openapi-fetch client middleware (api/client.ts) and the raw-fetch
// upload helper (api/upload.ts).
//
// Clerk's session token is only reachable through hooks (useAuth) inside
// the React tree, so AuthProvider registers the current session's
// getToken function here via setTokenGetter on every render. Modules
// outside React call getToken() to fetch a fresh token on demand —
// Clerk handles refreshing/caching internally.

export interface AuthUser {
  id: string
  email: string
}

type TokenGetter = () => Promise<string | null>

let tokenGetter: TokenGetter | null = null

/** Registers the active session's token getter. Called by AuthProvider. */
export function setTokenGetter(fn: TokenGetter | null): void {
  tokenGetter = fn
}

/** Returns a fresh bearer token for the current Clerk session, or null if signed out. */
export async function getToken(): Promise<string | null> {
  if (!tokenGetter) return null
  return tokenGetter()
}
