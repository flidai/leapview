import { expect, test } from 'bun:test'
import { Database, Eye, Server, Table2 } from 'lucide'
import { iconForLayer, label, layerLabel } from './data-explorer-object-labels'

test('object layers retain their icon and label mappings', () => {
  for (const [layer, icon, title] of [
    ['source', Server, 'Source'],
    ['semantic_view', Eye, 'Semantic view'],
    ['model', Table2, 'Model'],
    ['unknown', Database, 'unknown'],
  ] as const) {
    expect(iconForLayer(layer)).toBe(icon)
    expect(layerLabel(layer)).toBe(title)
  }
})

test('object labels distinguish empty values from zero and false', () => {
  for (const value of [null, undefined, '']) expect(label(value)).toBe('-')
  expect(label(0)).toBe('0')
  expect(label(false)).toBe('false')
  expect(label('orders')).toBe('orders')
  expect(label(42)).toBe('42')
})
