import type { components } from './schema.d.ts'

type Citation = components['schemas']['Citation']
type RetrievedFile = components['schemas']['RetrievedFile']

export type SearchStreamEvent =
  | { type: 'retrieved_files'; retrieved_files: RetrievedFile[] }
  | { type: 'delta'; text: string }
  | { type: 'done'; summary: string; citations: Citation[] }
  | { type: 'error'; error: string }

// The typed openapi-fetch client (see ./client.ts) expects a single JSON
// response body, which doesn't fit these endpoints' actual wire format now
// that they stream Server-Sent Events — so these calls go through plain
// fetch instead. BASE_URL mirrors client.ts.
const BASE_URL = '/api'

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
  return postEventStream(
    `${BASE_URL}/kbs/${kbId}/search`,
    { 'Content-Type': 'application/json' },
    JSON.stringify({ query }),
    onEvent,
    signal,
  )
}

// reevaluateStream re-runs the query behind an existing assistant message
// against the current KB state, streaming the same event sequence as
// searchStream. The resulting answer is persisted server-side as a new
// message (see B5) rather than replacing messageId's — this call only
// streams the regenerated answer back; picking up the new message itself
// requires refetching the Inquiry.
export async function reevaluateStream(
  kbId: string,
  messageId: string,
  onEvent: (event: SearchStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  return postEventStream(
    `${BASE_URL}/kbs/${kbId}/inquiry/messages/${messageId}/reevaluate`,
    {},
    null,
    onEvent,
    signal,
  )
}

async function postEventStream(
  url: string,
  headers: Record<string, string>,
  body: BodyInit | null,
  onEvent: (event: SearchStreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(url, {
    method: 'POST',
    headers,
    credentials: 'include',
    body,
    signal,
  })
  if (!res.ok) {
    throw new Error(`request failed with status ${res.status}`)
  }
  if (!res.body) {
    throw new Error('response had no body')
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
// comments/keepalives), which these endpoints never send today but which a
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
