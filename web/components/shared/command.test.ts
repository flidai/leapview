import { describe, expect, test } from 'bun:test'
import { headers } from './command'

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
})
