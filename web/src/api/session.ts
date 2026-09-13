import { api } from './client'

let sessionPromise: Promise<void> | null = null

/**
 * Ensures the browser holds a session cookie before other requests fire.
 * Every request without one mints its own session server-side (see
 * internal/auth/middleware.go) with no cross-request coordination, so
 * several components firing their first request in parallel on initial
 * page load would otherwise race each other into creating several
 * separate sessions for the same visit. Memoizing this promise at module
 * scope -- not per-component state -- means every caller across the whole
 * page (including React StrictMode's double-invoked mount effects) shares
 * the exact same in-flight request instead of firing their own.
 */
export function ensureSession(): Promise<void> {
  if (!sessionPromise) {
    sessionPromise = api
      .GET('/account/status', {})
      .then(() => undefined)
      .catch((err: unknown) => {
        sessionPromise = null // don't cache a failure forever -- allow a retry on the next call
        throw err
      })
  }
  return sessionPromise
}
