import { expect, test } from 'bun:test'
import { timestampCalendarDate, timestampDateBound } from './timestamp-date-range'

test('builds UTC midnight bounds', () => {
  expect(timestampDateBound('2025-01-15', 'UTC', false)).toEqual({
    value: { kind: 'timestamp', value: '2025-01-15T00:00:00Z' },
    inclusive: true,
  })
  expect(timestampDateBound('2025-01-15', 'UTC', true)).toEqual({
    value: { kind: 'timestamp', value: '2025-01-16T00:00:00Z' },
    inclusive: false,
  })
})

test('uses the 23-hour New York spring-forward day', () => {
  const lower = timestampDateBound('2025-03-09', 'America/New_York', false)
  const upper = timestampDateBound('2025-03-09', 'America/New_York', true)
  expect(lower.value.value).toBe('2025-03-09T05:00:00Z')
  expect(upper.value.value).toBe('2025-03-10T04:00:00Z')
  expect(Date.parse(upper.value.value) - Date.parse(lower.value.value)).toBe(23 * 60 * 60 * 1_000)
})

test('uses the 25-hour New York fall-back day', () => {
  const lower = timestampDateBound('2025-11-02', 'America/New_York', false)
  const upper = timestampDateBound('2025-11-02', 'America/New_York', true)
  expect(lower.value.value).toBe('2025-11-02T04:00:00Z')
  expect(upper.value.value).toBe('2025-11-03T05:00:00Z')
  expect(Date.parse(upper.value.value) - Date.parse(lower.value.value)).toBe(25 * 60 * 60 * 1_000)
})

test('starts a day at the first valid instant when daylight saving skips midnight', () => {
  const lower = timestampDateBound('2018-11-04', 'America/Sao_Paulo', false)
  const upper = timestampDateBound('2018-11-04', 'America/Sao_Paulo', true)
  expect(lower.value.value).toBe('2018-11-04T03:00:00Z')
  expect(upper.value.value).toBe('2018-11-05T02:00:00Z')
  expect(timestampDateBound('2018-11-03', 'America/Sao_Paulo', true).value).toEqual(lower.value)
  expect(timestampCalendarDate(lower.value.value, 'America/Sao_Paulo')).toBe('2018-11-04')
})

test('includes both occurrences of midnight when daylight saving repeats it', () => {
  expect(timestampDateBound('2020-11-01', 'America/Havana', false).value.value).toBe('2020-11-01T04:00:00Z')
  expect(timestampDateBound('2020-11-01', 'America/Havana', true).value.value).toBe('2020-11-02T05:00:00Z')
})

test('displays timestamps in the definition timezone', () => {
  expect(timestampCalendarDate('2025-03-10T03:59:59Z', 'America/New_York')).toBe('2025-03-09')
  expect(timestampCalendarDate('2025-03-10T04:00:00Z', 'America/New_York')).toBe('2025-03-10')
  expect(timestampCalendarDate('2025-03-10T00:00:00Z', 'UTC')).toBe('2025-03-10')
})

test('preserves early calendar years without Date constructor year coercion', () => {
  expect(timestampDateBound('0001-01-02', 'UTC', false).value.value).toBe('0001-01-02T00:00:00Z')
  expect(timestampDateBound('0099-02-03', 'UTC', true).value.value).toBe('0099-02-04T00:00:00Z')
  expect(timestampCalendarDate('0001-01-02T00:00:00Z', 'UTC')).toBe('0001-01-02')
})

test('rejects an upper bound beyond the supported calendar year', () => {
  expect(() => timestampDateBound('9999-12-31', 'UTC', true)).toThrow(RangeError)
})
