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
  expect(formatCell('—', { key: 'pivot_orders', label: 'Orders', role: 'metric', visualizationFormat: { kind: 'number' } }, true)).toBe('0')
  expect(formatCell(null, { key: 'pivot_revenue', label: 'Revenue', role: 'metric', visualizationFormat: { kind: 'currency', currency: 'BRL' } }, true)).toBe('R$0.00')
  expect(formatCell('12', { key: 'orders', label: 'Orders', role: 'metric', visualizationFormat: { kind: 'number' } })).toBe('12')
  expect(formatCell('2026-06-01', { key: 'purchase_date', label: 'Purchased', visualizationFormat: { kind: 'temporal' } })).toBe('2026-06-01')
  expect(() => formatCell('1e3', { key: 'orders', label: 'Orders', role: 'metric', visualizationFormat: { kind: 'number' } })).toThrow(/canonical decimal string/)
})
