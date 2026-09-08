import { describe, expect, test } from 'bun:test'
import { headers } from './command'

describe('shared browser command identity', () => {
  test('uses one generated identity for tracing and durable idempotency', () => {
    const commandHeaders = headers('runPipeline')
    expect(commandHeaders['X-Request-ID']).toBeTruthy()
    expect(commandHeaders['Idempotency-Key']).toBe(`ui:${commandHeaders['X-Request-ID']}`)
    expect(commandHeaders['X-LeapView-Operation-ID']).toBe('runPipeline')
  })

  test('survives a sessionStorage getter that throws', () => {
    const target = globalThis as typeof globalThis & { sessionStorage?: Storage }
    const previous = Object.getOwnPropertyDescriptor(target, 'sessionStorage')
    Object.defineProperty(target, 'sessionStorage', {
      configurable: true,
      get: () => { throw new Error('storage unavailable') },
    })
    try {
      expect(() => headers()).not.toThrow()
      expect(headers()['X-LeapView-Data-Explorer-Client']).toBeUndefined()
    } finally {
      if (previous) Object.defineProperty(target, 'sessionStorage', previous)
      else delete target.sessionStorage
    }
  })
})
