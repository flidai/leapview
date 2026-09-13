import { expect, test } from 'bun:test'
import { normalizeRelativePeriodCount } from './filter-control'

test('relative period counts stay within the control contract', () => {
  expect(normalizeRelativePeriodCount(1)).toBe(1)
  expect(normalizeRelativePeriodCount(1001)).toBe(1000)
  expect(normalizeRelativePeriodCount(1.5)).toBe(1)
  expect(normalizeRelativePeriodCount(Number.NaN)).toBe(1)
})
