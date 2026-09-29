import { expect, test } from 'bun:test'
import { displayRunDuration, hasLiveRunDuration } from './live-run-duration'

const startedAt = '2026-09-24T10:00:00Z'
const at = (seconds: number) => Date.parse(startedAt) + seconds * 1000

test('counts running and publishing time from start without drift', () => {
  expect(displayRunDuration('running', startedAt, undefined, '—', at(24))).toBe('24s')
  expect(displayRunDuration('running', startedAt, '—', '—', at(24))).toBe('24s')
  expect(displayRunDuration('running', startedAt, '-', '—', at(24))).toBe('24s')
  expect(displayRunDuration('prepared', startedAt, undefined, '—', at(65))).toBe('1m5s')
  expect(displayRunDuration('running', startedAt, undefined, '—', at(3661))).toBe('1h1m1s')
  expect(displayRunDuration('running', startedAt, undefined, '—', at(-5))).toBe('0s')
})

test('uses durable duration when queued, finished, or missing a valid start', () => {
  expect(hasLiveRunDuration('queued', startedAt)).toBe(false)
  expect(displayRunDuration('queued', startedAt, undefined, '—', at(24))).toBe('—')
  expect(displayRunDuration('succeeded', startedAt, '2026-09-24T10:00:24Z', '24s', at(60))).toBe('24s')
  expect(displayRunDuration('failed', startedAt, undefined, '5s', at(60))).toBe('5s')
  expect(displayRunDuration('running', '—', undefined, '—', at(24))).toBe('—')
  expect(displayRunDuration('running', startedAt, '2026-09-24T10:00:24Z', '24s', at(60))).toBe('24s')
})
