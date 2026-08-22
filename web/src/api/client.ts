import createClient from 'openapi-fetch'
import type { paths } from './schema.d.ts'

// In development, Vite proxies /api → http://localhost:8080 (stripping the prefix).
// In production, the API is served from the same origin.
const BASE_URL = import.meta.env.DEV ? '/api' : '/'

export const api = createClient<paths>({
  baseUrl: BASE_URL,
  credentials: 'include',
  headers: {
    'Content-Type': 'application/json',
  },
})
