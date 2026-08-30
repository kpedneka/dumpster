import type { components } from './schema.d.ts'

type Citation = components['schemas']['Citation']
type RetrievedFile = components['schemas']['RetrievedFile']

export type SearchStreamEvent =
  | { type: 'retrieved_files'; retrieved_files: RetrievedFile[] }
  | { type: 'delta'; text: string }
  | { type: 'done'; summary: string; citations: Citation[] }
  | { type: 'error'; error: string }

// The typed openapi-fetch client (see ./client.ts) expects a single JSON
// response body, which doesn't fit POST /kbs/{id}/search's actual wire
// format now that it streams Server-Sent Events — so this call goes
// through plain fetch instead. The BASE_URL logic mirrors client.ts: Vite
// proxies /api → the API in dev, same-origin in production.
const BASE_URL = import.meta.env.DEV ? '/api' : ''

// searchStream POSTs a query to the search endpoint and invokes onEvent for
// each SSE frame as it arrives, in order: one retrieved_files event, then
// zero or more delta events, then exactly one of done or error. Throws if
// the request itself fails before any streaming begins (network error, a
// non-2xx status, or a response with no body).
export async function searchStream(
  kbId: string,
  query: string,
  onEvent: (event: SearchStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`${BASE_URL}/kbs/${kbId}/search`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    credentials: 'include',
    body: JSON.stringify({ query }),
    signal,
  })
  if (!res.ok) {
    throw new Error(`search failed with status ${res.status}`)
  }
  if (!res.body) {
    throw new Error('search response had no body')
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })

    let sepIndex: number
    while ((sepIndex = buf.indexOf('\n\n')) !== -1) {
      const frame = buf.slice(0, sepIndex)
      buf = buf.slice(sepIndex + 2)
      const event = parseFrame(frame)
      if (event) onEvent(event)
    }
  }
}

// parseFrame reads one "event: <name>\ndata: <json>\n\n" SSE frame (minus
// its trailing blank line, already stripped by the caller) into a typed
// SearchStreamEvent. Returns null for a frame with no data line (SSE
// comments/keepalives), which this endpoint never sends today but which a
// spec-compliant parser shouldn't choke on.
function parseFrame(frame: string): SearchStreamEvent | null {
  let name: string | null = null
  let data: string | null = null
  for (const line of frame.split('\n')) {
    if (line.startsWith('event: ')) name = line.slice('event: '.length)
    else if (line.startsWith('data: ')) data = line.slice('data: '.length)
  }
  if (name === null || data === null) return null

  const payload = JSON.parse(data) as Record<string, unknown>
  return { type: name, ...payload } as SearchStreamEvent
}
