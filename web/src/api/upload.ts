import { getToken } from '@/lib/auth'
import type { components } from './schema.d.ts'

type Document = components['schemas']['Document']

const BASE_URL = import.meta.env.DEV ? '/api' : ''

/**
 * Uploads a file to a knowledge base using multipart/form-data.
 * Uses native fetch so the browser can set the correct Content-Type boundary
 * without interference from the JSON default header on the openapi-fetch client.
 */
export async function uploadDocument(kbId: string, file: File): Promise<Document> {
  const formData = new FormData()
  formData.append('file', file)

  const token = await getToken()
  const res = await fetch(`${BASE_URL}/kbs/${kbId}/documents`, {
    method: 'POST',
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    body: formData,
  })

  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: 'Upload failed' })) as { error?: string }
    throw new Error(body.error ?? `Upload failed (${res.status})`)
  }

  return res.json() as Promise<Document>
}
