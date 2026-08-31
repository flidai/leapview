import { describe, expect, test } from 'bun:test'
import { browserCommandFailure } from './command-failure'

function fetchEvent(type: string, status?: string | number): Event {
  return new CustomEvent('datastar-fetch', { detail: { type, argsRaw: { status } } })
}

describe('browser command failure contract', () => {
  test('ignores non-terminal Datastar events', () => {
    expect(browserCommandFailure(fetchEvent('started', 200))).toBeNull()
  })

  test.each([
    [401, 'session-expired', false],
    ['403', 'forbidden', false],
    [404, 'not-found', false],
    [409, 'conflict', false],
    [412, 'conflict', false],
    [429, 'rate-limited', true],
    [400, 'invalid-draft', false],
    [422, 'validation', false],
    [503, 'unavailable', true],
  ] as const)('classifies HTTP status %s', (status, kind, retryable) => {
    expect(browserCommandFailure(fetchEvent('error', status), 'Saving settings')).toMatchObject({ kind, retryable, status: Number(status) })
  })

  test('gives actionable guidance for an invalid draft request', () => {
    const result = browserCommandFailure(fetchEvent('error', 400), 'Publishing dashboard')
    expect(result).toMatchObject({ kind: 'invalid-draft', retryable: false, status: 400 })
    expect(result?.message).toContain('draft is invalid or incomplete')
    expect(result?.message).toContain('Review the draft inputs')
  })

  test('gives actionable guidance for a validation failure', () => {
    const result = browserCommandFailure(fetchEvent('error', 422), 'Publishing dashboard')
    expect(result).toMatchObject({ kind: 'validation', retryable: false, status: 422 })
    expect(result?.message).toContain('rejected by validation')
    expect(result?.message).toContain('Fix the highlighted draft issues')
  })

  test('classifies retry exhaustion without a status as a retryable network failure', () => {
    expect(browserCommandFailure(fetchEvent('retries-failed'), 'Loading audit history')).toEqual({
      kind: 'network',
      status: null,
      retryable: true,
      message: 'Loading audit history could not reach the server. Your previous state was kept; check the connection and retry.',
    })
  })

  test('does not promise an automatic replay for an unknown write failure', () => {
    const result = browserCommandFailure(fetchEvent('error', 499), 'Publishing dashboard')
    expect(result).toMatchObject({ kind: 'unknown', retryable: false })
    expect(result?.message).toContain('previous state was kept')
  })
})
