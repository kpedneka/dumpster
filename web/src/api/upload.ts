import type { components } from './schema.d.ts'

type Document = components['schemas']['Document']

const BASE_URL = '/api'

/**
 * Uploads a file to a knowledge base using multipart/form-data.
 * Uses native fetch so the browser can set the correct Content-Type boundary
 * without interference from the JSON default header on the openapi-fetch client.
 */
export async function uploadDocument(kbId: string, file: File): Promise<Document> {
  const formData = new FormData()
  formData.append('file', file)

  const res = await fetch(`${BASE_URL}/kbs/${kbId}/documents`, {
    method: 'POST',
    credentials: 'include',
    body: formData,
  })

  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: 'Upload failed' })) as { error?: string }
    throw new Error(body.error ?? `Upload failed (${res.status})`)
  }

  return res.json() as Promise<Document>
}
