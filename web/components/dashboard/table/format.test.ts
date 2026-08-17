import { expect, test } from 'bun:test'

import { formatCell } from './format'

test('table cells use the visualization formatting contract when supplied by the IR', () => {
  expect(formatCell(1234.5, {
    key: 'revenue', label: 'Revenue', align: 'right', role: 'metric',
    visualizationFormat: { kind: 'currency', currency: 'USD' },
  })).toBe('$1,234.50')
  expect(formatCell(null, { key: 'revenue', label: 'Revenue', visualizationFormat: { kind: 'number' } })).toBe('—')
  expect(formatCell('', { key: 'pivot_revenue', label: 'Revenue', role: 'metric', visualizationFormat: { kind: 'currency', currency: 'BRL' } })).toBe('—')
  expect(formatCell('-', { key: 'pivot_orders', label: 'Orders', role: 'metric', visualizationFormat: { kind: 'number' } })).toBe('—')
  expect(formatCell('—', { key: 'pivot_orders', label: 'Orders', role: 'metric', visualizationFormat: { kind: 'number' } })).toBe('—')
  expect(() => formatCell('12', { key: 'orders', label: 'Orders', role: 'metric', visualizationFormat: { kind: 'number' } })).toThrow(/numeric string/)
})
