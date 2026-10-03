import { expect, test } from 'bun:test'
import { safeChatReturnURL, fullChatHref } from './chat-navigation'

test('accepts exact local return routes and rejects redirect tricks', () => {
  const origin = 'https://leap.test'
  expect(safeChatReturnURL('/dashboards/sales/pages/main?year=2026#chart', origin)).toBe('/dashboards/sales/pages/main?year=2026#chart')
  expect(safeChatReturnURL('/explore?dataset=a', origin)).toBe('/explore?dataset=a')
  for (const value of ['//evil.test/explore', 'https://evil.test/explore', '/\\evil.test/explore', '/chats/new', '/logout', 'javascript:alert(1)']) {
    expect(safeChatReturnURL(value, origin)).toBeUndefined()
  }
})

test('conversation promotion keeps opaque return token without copying other query params', () => {
  expect(fullChatHref('a/b', '?return=token_123&ignored=x')).toBe('/chats/a%2Fb?return=token_123')
  expect(fullChatHref('', '')).toBe('/chats/new')
})

test('return snapshots restore only the exact originating route and stay out of URLs', async () => {
  const { rememberChatReturn, readDrawerReturn, readFullChatReturn, takeFullChatReturn, chatReturnHref, updateChatReturnConversation } = await import('./chat-navigation')
  const originalLocation = Object.getOwnPropertyDescriptor(globalThis, 'location')
  const originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'sessionStorage')
  const values = new Map<string, string>()
  const local = { origin: 'https://leap.test', pathname: '/explore', search: '?dataset=a', hash: '#chart' }
  Object.defineProperty(globalThis, 'location', { configurable: true, value: local })
  Object.defineProperty(globalThis, 'sessionStorage', { configurable: true, value: { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => values.set(key, value) } })
  try {
    const token = rememberChatReturn({ conversationId: '', draft: 'Unsent text', references: [], editMessageId: '', selectedVisualId: '', selectedExplorerHref: '', selectedVisualTitle: '', scroll: { top: 120, follow: false } })
    expect(token).not.toContain('Unsent')
    expect(readDrawerReturn()?.draft).toBe('Unsent text')
    local.search = '?dataset=b'
    expect(readDrawerReturn()).toBeUndefined()
    local.pathname = '/chats/new'
    local.search = `?return=${token}`
    expect(chatReturnHref()).toBe('/explore?dataset=a#chart')
    expect(readFullChatReturn()?.draft).toBe('Unsent text')
    expect(takeFullChatReturn()?.draft).toBe('Unsent text')
    expect(takeFullChatReturn()).toBeUndefined()
    expect(readFullChatReturn()?.draft).toBe('Unsent text')
    updateChatReturnConversation('accepted-id')
    local.pathname = '/explore'
    local.search = '?dataset=a'
    expect(readDrawerReturn()?.conversationId).toBe('accepted-id')
    const stored = JSON.parse(values.values().next().value!)
    stored[token].state.references = [{ reference: { kind: 'visual', id: 'visual-one' }, name: 'Revenue', href: '/dashboards/sales', hierarchy: ['Sales'], context: ['current_page'], locations: [{ href: '/dashboards/sales', dashboardId: 'sales' }] }]
    values.set('leapview-chat-returns-v1', JSON.stringify(stored))
    expect(readDrawerReturn()?.references[0].name).toBe('Revenue')
    const validState = structuredClone(stored[token].state)
    for (const invalid of [
      { draft: {} }, { draft: 'x'.repeat(100_001) }, { scroll: { top: 'bad', follow: false } },
      { scroll: { top: -1, follow: false } }, { scroll: { top: 10, follow: 'yes' } },
      { references: [null] }, { references: [{ reference: { kind: 'visual', id: 'v' }, name: 'Visual' }] },
      { references: Array.from({ length: 65 }, () => ({})) },
    ]) {
      stored[token].state = { ...validState, ...invalid }
      values.set('leapview-chat-returns-v1', JSON.stringify(stored))
      expect(readDrawerReturn()).toBeUndefined()
    }
    stored[token].state = validState
    stored[token].href = '//evil.test/explore'
    values.set('leapview-chat-returns-v1', JSON.stringify(stored))
    local.search = `?return=${token}`
    expect(chatReturnHref()).toBeUndefined()
  } finally {
    if (originalLocation) Object.defineProperty(globalThis, 'location', originalLocation)
    else Reflect.deleteProperty(globalThis, 'location')
    if (originalStorage) Object.defineProperty(globalThis, 'sessionStorage', originalStorage)
    else Reflect.deleteProperty(globalThis, 'sessionStorage')
  }
})
