import createClient from 'openapi-fetch'
import type { paths } from './schema.d.ts'

// In development, Vite proxies /api → http://localhost:8080 (stripping the prefix).
// In production, the API is served from the same origin.
const BASE_URL = import.meta.env.DEV ? '/api' : '/'

function getToken(): string | null {
  return localStorage.getItem('dumpster_token')
}

export function setToken(token: string): void {
  localStorage.setItem('dumpster_token', token)
}

export function clearToken(): void {
  localStorage.removeItem('dumpster_token')
}

export const api = createClient<paths>({
  baseUrl: BASE_URL,
  headers: {
    'Content-Type': 'application/json',
  },
})

// Attach the Authorization header before every request.
api.use({
  onRequest({ request }) {
    const token = getToken()
    if (token) {
      request.headers.set('Authorization', `Bearer ${token}`)
    }
    return request
  },
})
