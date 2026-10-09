import { expect, test } from 'bun:test'
import { payloadProfile, traceProfile } from './performance_browser_profile'
import { collectBrowserHealth } from './movielens_performance'
import { EventEmitter } from 'node:events'

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

test('profile health rejects bad delivery and console errors while allowing only owned teardown aborts', () => {
  const page = new EventEmitter()
  const owned = { url: () => 'http://127.0.0.1/updates', failure: () => ({ errorText: 'net::ERR_ABORTED' }), method: () => 'GET' }
  let tearingDown = false
  const health = collectBrowserHealth(page as any, request => tearingDown && request === owned as any && request.failure()?.errorText === 'net::ERR_ABORTED')
  page.emit('requestfailed', owned)
  expect(health.failedNetworkResponses).toHaveLength(1)
  tearingDown = true
  page.emit('requestfailed', owned)
  expect(health.failedNetworkResponses).toHaveLength(1)
  page.emit('requestfailed', { ...owned, url: () => 'http://127.0.0.1/static/component.js' })
  page.emit('response', { status: () => 503, request: () => ({ method: () => 'GET' }), url: () => 'http://127.0.0.1/visuals/map/tiles/1' })
  page.emit('console', { type: () => 'error', text: () => 'renderer failed' })
  expect(health.failedNetworkResponses).toHaveLength(3)
  expect(health.consoleErrors).toEqual(['error: renderer failed'])
})
