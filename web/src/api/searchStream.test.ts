import { describe, it, expect, vi, afterEach } from 'vitest'
import { searchStream, reevaluateStream, type SearchStreamEvent } from './searchStream'

function sseResponse(frames: string[], init: { ok?: boolean; status?: number } = {}): Response {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      const encoder = new TextEncoder()
      for (const frame of frames) {
        controller.enqueue(encoder.encode(frame))
      }
      controller.close()
    },
  })
  return new Response(body, { status: init.status ?? 200 })
}

function frame(name: string, data: unknown): string {
  return `event: ${name}\ndata: ${JSON.stringify(data)}\n\n`
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('searchStream', () => {
  it('invokes onEvent for each frame in order', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        sseResponse([
          frame('retrieved_files', { retrieved_files: [{ document_id: 'doc-1', file_name: 'geo.txt' }] }),
          frame('delta', { text: 'Paris ' }),
          frame('delta', { text: 'is the capital.' }),
          frame('done', { summary: 'Paris is the capital.', citations: [] }),
        ]),
      ),
    )

    const events: SearchStreamEvent[] = []
    await searchStream('kb-1', 'question', (e) => events.push(e))

    expect(events).toEqual([
      { type: 'retrieved_files', retrieved_files: [{ document_id: 'doc-1', file_name: 'geo.txt' }] },
      { type: 'delta', text: 'Paris ' },
      { type: 'delta', text: 'is the capital.' },
      { type: 'done', summary: 'Paris is the capital.', citations: [] },
    ])
  })

  it('handles a frame split across multiple stream chunks', async () => {
    const full = frame('done', { summary: 'complete', citations: [] })
    const mid = Math.floor(full.length / 2)
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        const encoder = new TextEncoder()
        controller.enqueue(encoder.encode(full.slice(0, mid)))
        controller.enqueue(encoder.encode(full.slice(mid)))
        controller.close()
      },
    })
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, { status: 200 })))

    const events: SearchStreamEvent[] = []
    await searchStream('kb-1', 'question', (e) => events.push(e))

    expect(events).toEqual([{ type: 'done', summary: 'complete', citations: [] }])
  })

  it('sends the query in the request body to the correct endpoint', async () => {
    const fetchMock = vi.fn().mockResolvedValue(sseResponse([frame('done', { summary: 'x', citations: [] })]))
    vi.stubGlobal('fetch', fetchMock)

    await searchStream('kb-42', 'what is the answer?', () => {})

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining('/kbs/kb-42/search'),
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ query: 'what is the answer?' }),
      }),
    )
  })

  it('throws for a non-OK response before invoking onEvent', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 500 })))

    const onEvent = vi.fn()
    await expect(searchStream('kb-1', 'question', onEvent)).rejects.toThrow(/500/)
    expect(onEvent).not.toHaveBeenCalled()
  })

  it('throws if the response has no body', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 200 })))

    await expect(searchStream('kb-1', 'question', () => {})).rejects.toThrow(/no body/)
  })
})

describe('reevaluateStream', () => {
  it('invokes onEvent for each frame in order', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        sseResponse([
          frame('delta', { text: 'updated ' }),
          frame('delta', { text: 'answer.' }),
          frame('done', { summary: 'updated answer.', citations: [] }),
        ]),
      ),
    )

    const events: SearchStreamEvent[] = []
    await reevaluateStream('kb-1', 'msg-1', (e) => events.push(e))

    expect(events).toEqual([
      { type: 'delta', text: 'updated ' },
      { type: 'delta', text: 'answer.' },
      { type: 'done', summary: 'updated answer.', citations: [] },
    ])
  })

  it('posts to the reevaluate endpoint for the given message with no body', async () => {
    const fetchMock = vi.fn().mockResolvedValue(sseResponse([frame('done', { summary: 'x', citations: [] })]))
    vi.stubGlobal('fetch', fetchMock)

    await reevaluateStream('kb-42', 'msg-7', () => {})

    expect(fetchMock).toHaveBeenCalledWith(
      expect.stringContaining('/kbs/kb-42/inquiry/messages/msg-7/reevaluate'),
      expect.objectContaining({ method: 'POST', body: null }),
    )
  })

  it('throws for a non-OK response before invoking onEvent', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 400 })))

    const onEvent = vi.fn()
    await expect(reevaluateStream('kb-1', 'msg-1', onEvent)).rejects.toThrow(/400/)
    expect(onEvent).not.toHaveBeenCalled()
  })
})
