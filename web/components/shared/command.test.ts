import { describe, expect, test } from 'bun:test'
import { headers, nonReplayableHeaders } from './command'

describe('shared browser command identity', () => {
  test('generates distinct canonical UUIDv7 identities', () => {
    const originalCrypto = globalThis.crypto
    const originalNow = Date.now
    let seed = 0
    Date.now = () => 0x010203040506
    Object.defineProperty(globalThis, 'crypto', {
      configurable: true,
      value: { getRandomValues<T extends ArrayBufferView>(bytes: T): T {
        const view = new Uint8Array(bytes.buffer, bytes.byteOffset, bytes.byteLength)
        view.fill(seed++)
        return bytes
      } },
    })
    try {
      const commandHeaders = headers('runPipeline')
      const requestID = commandHeaders['X-Request-ID']
      const idempotencyKey = commandHeaders['Idempotency-Key']
      expect(requestID).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
      expect(idempotencyKey).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
      expect(requestID).not.toBe(idempotencyKey)
      expect(commandHeaders['X-LeapView-Operation-ID']).toBe('runPipeline')
    } finally {
      Date.now = originalNow
      Object.defineProperty(globalThis, 'crypto', { configurable: true, value: originalCrypto })
    }
  })

  test('fails closed when secure randomness is unavailable', () => {
    const originalCrypto = globalThis.crypto
    Object.defineProperty(globalThis, 'crypto', { configurable: true, value: undefined })
    try {
      expect(() => headers('runPipeline')).toThrow('secure randomness unavailable')
    } finally {
      Object.defineProperty(globalThis, 'crypto', { configurable: true, value: originalCrypto })
    }
  })

  test('non-replayable headers retain CSRF and one exact operation claim without an idempotency key', () => {
    const originalCrypto = globalThis.crypto
    const originalNow = Date.now
    const documentDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'document')
    Date.now = () => 0x010203040506
    Object.defineProperty(globalThis, 'crypto', {
      configurable: true,
      value: { getRandomValues<T extends ArrayBufferView>(bytes: T): T {
        const view = new Uint8Array(bytes.buffer, bytes.byteOffset, bytes.byteLength)
        view.fill(7)
        return bytes
      } },
    })
    Object.defineProperty(globalThis, 'document', {
      configurable: true,
      value: { querySelector: () => ({ content: 'csrf-value' }) },
    })
    try {
      const commandHeaders = nonReplayableHeaders('credential.create')
      expect(commandHeaders).toEqual({
        'X-CSRF-Token': 'csrf-value',
        'X-Request-ID': '01020304-0506-7707-8707-070707070707',
        'X-LeapView-Operation-ID': 'credential.create',
      })
      expect(commandHeaders).not.toHaveProperty('Idempotency-Key')
    } finally {
      Date.now = originalNow
      Object.defineProperty(globalThis, 'crypto', { configurable: true, value: originalCrypto })
      if (documentDescriptor) Object.defineProperty(globalThis, 'document', documentDescriptor)
      else Reflect.deleteProperty(globalThis, 'document')
    }
  })

  test('non-replayable headers reject an empty operation claim', () => {
    expect(() => nonReplayableHeaders('  ')).toThrow('operation identity is required')
  })
})

describe('navigation intent preloads', () => {
  test('warms only versioned same-origin route modules, deduplicates and respects the budget', async () => {
    const { JSDOM } = await import('jsdom')
    const { installNavigationPreload } = await import('./navigation-preload')
    const dom = new JSDOM('<head><script src="/static/command.js?v=release"></script></head><body></body>', { url: 'https://leapview.example/' })
    const names = ['window', 'document', 'navigator', 'HTMLAnchorElement'] as const
    const descriptors = names.map(name => Object.getOwnPropertyDescriptor(globalThis, name))
    for (const name of names) Object.defineProperty(globalThis, name, { configurable: true, value: dom.window[name] })
    dom.window.DOMTokenList.prototype.supports = () => true
    const intent = (href: string, target?: string) => {
      const anchor = dom.window.document.createElement('a')
      anchor.href = href
      if (target) anchor.target = target
      dom.window.document.body.append(anchor)
      anchor.dispatchEvent(new dom.window.Event('focusin', { bubbles: true, composed: true }))
    }
    try {
      installNavigationPreload()
      intent('https://external.example/explore')
      intent('/explore', '_blank')
      intent('/unknown')
      expect(dom.window.document.querySelectorAll('link')).toHaveLength(0)
      intent('/explore')
      intent('/explore')
      expect(dom.window.document.querySelectorAll('link')).toHaveLength(1)
      expect(dom.window.document.querySelector('link')?.href).toBe('https://leapview.example/static/data-explorer.js?v=release')
      for (const route of ['/dashboards', '/dashboards/new', '/dashboards/sales', '/chats', '/admin/users', '/models', '/dashboards/other']) intent(route)
      const links = Array.from(dom.window.document.querySelectorAll('link'))
      expect(links).toHaveLength(8)
      expect(links.every(link => link.rel === 'modulepreload' && new URL(link.href).pathname.startsWith('/static/'))).toBe(true)
      expect(new Set(links.map(link => link.href)).size).toBe(8)
    } finally {
      names.forEach((name, index) => {
        const descriptor = descriptors[index]
        if (descriptor) Object.defineProperty(globalThis, name, descriptor)
        else Reflect.deleteProperty(globalThis, name)
      })
      dom.window.close()
    }
  })
})
