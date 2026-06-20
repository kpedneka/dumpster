import createClient from 'openapi-fetch'
import type { paths } from './schema.d.ts'
import { getToken } from '@/lib/auth'

// In development, Vite proxies /api → http://localhost:8080 (stripping the prefix).
// In production, the API is served from the same origin.
const BASE_URL = import.meta.env.DEV ? '/api' : '/'

export const api = createClient<paths>({
  baseUrl: BASE_URL,
  headers: {
    'Content-Type': 'application/json',
  },
})

// Attach the Authorization header before every request. The token comes
// from the active Clerk session (see lib/auth.ts) and is fetched fresh on
// every request — Clerk caches/refreshes it internally, so this is cheap.
api.use({
  async onRequest({ request }) {
    const token = await getToken()
    if (token) {
      request.headers.set('Authorization', `Bearer ${token}`)
    }
    return request
  },
})
