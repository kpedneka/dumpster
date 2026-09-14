import createClient from 'openapi-fetch'
import type { paths } from './schema.d.ts'

// The backend itself now lives under /api for both environments (see the
// "Decouple SPA hosting from the API instance" dev board card): CloudFront
// routes /api/* to the ALB and everything else to S3 in production, and
// Vite's dev proxy forwards /api/* straight through to the local backend
// unmodified (no path rewrite -- the backend expects the prefix now too).
const BASE_URL = '/api'

export const api = createClient<paths>({
  baseUrl: BASE_URL,
  credentials: 'include',
  headers: {
    'Content-Type': 'application/json',
  },
})
