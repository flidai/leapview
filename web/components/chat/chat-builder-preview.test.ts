import { expect, test } from 'bun:test'
import { previewChatBuilder } from './chat-builder-preview'

test('builder preview coalesces pending requests, retries with stable identity, and reuses a created builder', async () => {
  const original = globalThis.fetch
  const requests: RequestInit[] = []
  let complete!: (response: Response) => void
  globalThis.fetch = ((_url: unknown, init: RequestInit) => {
    requests.push(init)
    return new Promise<Response>(resolve => { complete = resolve })
  }) as typeof fetch
  try {
    const source = { conversationId: 'coalesce', artifactId: 'visual', title: 'Revenue' }
    const first = previewChatBuilder(source)
    expect(previewChatBuilder(source)).toBe(first)
    complete(new Response('{}', { status: 503 }))
    await expect(first).rejects.toThrow('Could not add this visual')
    const retry = previewChatBuilder(source)
    complete(Response.json({ dashboardId: 'dashboard:created', title: 'Revenue', pageId: 'overview', href: '/dashboards/dashboard:created/edit?page=overview' }))
    const result = await retry
    expect((requests[0].headers as Record<string, string>)['Idempotency-Key']).toBe((requests[1].headers as Record<string, string>)['Idempotency-Key'])
    expect(await previewChatBuilder(source)).toBe(result)
    expect(requests).toHaveLength(2)
    expect(JSON.parse(requests[1].body as string)).toEqual({ title: 'Revenue' })
  } finally { globalThis.fetch = original }
})

test('builder preview sends only canonical source identity and bounds titles by Unicode codepoints', async () => {
  const original = globalThis.fetch
  let request: RequestInit | undefined
  let path: unknown
  globalThis.fetch = ((url: unknown, init: RequestInit) => {
    path = url
    request = init
    return Promise.resolve(Response.json({ dashboardId: 'dashboard:unicode', title: 'Unicode', pageId: 'overview', href: '/dashboards/dashboard:unicode/edit?page=overview' }))
  }) as typeof fetch
  try {
    await previewChatBuilder({ conversationId: 'unicode', revision: 'exact-revision', title: '😀'.repeat(300) })
    expect(path).toBe('/chats/unicode/dashboard')
    expect(JSON.parse(request!.body as string)).toEqual({ revision: 'exact-revision', title: '😀'.repeat(255) })
    expect((request!.headers as Record<string, string>)['X-LeapView-Operation-ID']).toBe('saveChatDashboardDraft')
  } finally { globalThis.fetch = original }
})

test('builder preview rejects non-builder destinations and exposes permission failure without a cached success', async () => {
  const original = globalThis.fetch
  let status = 403
  let href = '/dashboards/dashboard:good/edit'
  globalThis.fetch = Object.assign(() => Promise.resolve(Response.json({ href }, { status })), { preconnect: original.preconnect }) as typeof fetch
  try {
    await expect(previewChatBuilder({ conversationId: 'denied', artifactId: 'visual', title: 'Revenue' })).rejects.toThrow()
    status = 201
    for (const invalid of ['//evil.test/dashboards/a/edit', 'https://evil.test/dashboards/a/edit', '/dashboards/a', '/dashboards/a/../edit', '/dashboards/a/edit\n']) {
      href = invalid
      await expect(previewChatBuilder({ conversationId: 'invalid', artifactId: invalid, title: 'Revenue' })).rejects.toThrow()
    }
  } finally { globalThis.fetch = original }
})


test('builder preview rejects revisions longer than the server limit before sending a request', async () => {
  const original = globalThis.fetch
  let requested = false
  globalThis.fetch = (() => { requested = true; throw new Error('unexpected request') }) as typeof fetch
  try {
    await expect(previewChatBuilder({ conversationId: 'oversized-revision', revision: 'r'.repeat(257), title: 'Revenue' })).rejects.toThrow('This preview is unavailable')
    expect(requested).toBe(false)
  } finally { globalThis.fetch = original }
})
