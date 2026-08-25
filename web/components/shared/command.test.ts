import { describe, expect, test } from 'bun:test'
import { headers } from './command'

describe('shared browser command identity', () => {
  test('uses one generated identity for tracing and durable idempotency', () => {
    const commandHeaders = headers('runPipeline')
    expect(commandHeaders['X-Request-ID']).toBeTruthy()
    expect(commandHeaders['Idempotency-Key']).toBe(`ui:${commandHeaders['X-Request-ID']}`)
    expect(commandHeaders['X-LeapView-Operation-ID']).toBe('runPipeline')
  })
})
