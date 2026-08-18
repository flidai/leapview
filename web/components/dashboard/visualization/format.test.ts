import { expect, test } from 'bun:test'
import type { VisualizationFormat } from '../../../generated/visualization'
import { formatDisplayValue, formatValue, resolveDisplayUnit } from './format'

test('TypeScript formatting matches shared Go fixtures', async () => {
  const fixtures = await Bun.file(new URL('../../../../api/visualization/conformance/formatting.json', import.meta.url)).json() as Array<{ locale: string; format: VisualizationFormat; value: unknown; expected: string }>
  for (const fixture of fixtures) expect(formatValue(fixture.locale, fixture.format, fixture.value)).toBe(fixture.expected)
})

test('formatting fails closed for unsupported locale and currency', () => {
  expect(() => formatValue('de-DE', { kind: 'number' }, 1)).toThrow()
  expect(() => formatValue('en-US', { kind: 'currency', currency: 'XYZ' }, 1)).toThrow()
})

test('auto display units use one three-significant-digit scale for the complete scope', () => {
  const unit = resolveDisplayUnit('auto', [1_234_567, 45_219, 982.14])

  expect(unit).toEqual({ scale: 1_000_000, suffix: 'M', exact: false })
  expect(formatDisplayValue('en-US', { kind: 'number' }, 1_234_567, unit)).toBe('1.23M')
  expect(formatDisplayValue('en-US', { kind: 'number' }, 45_219, unit)).toBe('0.0452M')
  expect(formatDisplayValue('en-US', { kind: 'number' }, 982.14, unit)).toBe('0.000982M')
})

test('auto display units produce glanceable values at each magnitude', () => {
  expect(formatDisplayValue('en-US', { kind: 'number' }, 45_219, resolveDisplayUnit('auto', [45_219]))).toBe('45.2K')
  expect(formatDisplayValue('en-US', { kind: 'number' }, 982.14, resolveDisplayUnit('auto', [982.14]))).toBe('982')
  expect(formatDisplayValue('en-US', { kind: 'number' }, 999_500, resolveDisplayUnit('auto', [999_500]))).toBe('1M')
})

test('display units compose with semantic currency and percent formats', () => {
  expect(formatDisplayValue('en-US', { kind: 'currency', currency: 'USD' }, 1_234_567, resolveDisplayUnit('auto', [1_234_567]))).toBe('$1.23M')
  expect(formatDisplayValue('en-US', { kind: 'percent' }, 0.63214, resolveDisplayUnit('auto', [63.214]))).toBe('63.2%')
})

test('fixed display units stay fixed and none preserves canonical formatting', () => {
  expect(formatDisplayValue('en-US', { kind: 'number' }, 45_219, resolveDisplayUnit('millions', [45_219]))).toBe('0.0452M')
  expect(formatDisplayValue('en-US', { kind: 'currency', currency: 'USD' }, 45_219, resolveDisplayUnit('none', [45_219]))).toBe('$45,219.00')
})

test('formatting accepts canonical exact decimal transport strings', () => {
	 expect(formatValue('en-US', { kind: 'currency', currency: 'USD' }, '252.24')).toBe('$252.24')
	 expect(() => formatValue('en-US', { kind: 'number' }, '1e3')).toThrow()
})

test('decimal display-unit boundaries are compared without binary-number rounding', () => {
  expect(resolveDisplayUnit('auto', ['999999999999.9999'])).toEqual({ scale: 1e9, suffix: 'B', exact: false })
  expect(resolveDisplayUnit('auto', ['1000000000000.0001'])).toEqual({ scale: 1e12, suffix: 'T', exact: false })
})

test('decimal rounding does not render a negative zero', () => {
  expect(formatValue('en-US', { kind: 'number', maximumFractionDigits: 2 }, '-0.004')).toBe('0')
  expect(formatValue('en-US', { kind: 'number', minimumFractionDigits: 2, maximumFractionDigits: 2 }, '-0.004')).toBe('0.00')
})
