import { expect, test } from 'bun:test'
import { payloadProfile, traceProfile } from './performance_browser_profile'

test('payload sizes bind actual decoded bytes and declare offline compression', () => {
  const bytes = new TextEncoder().encode('<html>' + 'fixed payload '.repeat(100) + '</html>')
  const result = payloadProfile(bytes)
  expect(result.rawBytes).toBe(bytes.byteLength)
  expect(result.offlineGzipBytes).toBeLessThan(result.rawBytes)
  expect(result.offlineBrotliBytes).toBeLessThan(result.rawBytes)
  expect(payloadProfile(bytes)).toEqual(result)
  expect(() => payloadProfile(new Uint8Array())).toThrow()
})
test('trace profiling counts real complete events without inventing parse/render time', () => {
  const result = traceProfile([{ name: 'v8.parseOnBackground', ph: 'X', dur: 20 },
    { name: 'Layout', ph: 'X', dur: 5 }, { name: 'ParseHTML', ph: 'B' }, { name: 'Other', ph: 'X', dur: 90 }])
  expect(result.events).toEqual({ Layout: { count: 1, durationMicroseconds: 5 },
    'v8.parseOnBackground': { count: 1, durationMicroseconds: 20 } })
  expect(traceProfile([]).events).toEqual({})
  expect(() => traceProfile([{ name: 'Layout', ph: 'X', dur: NaN }])).toThrow()
})
