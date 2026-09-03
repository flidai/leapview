import { expect, test } from 'bun:test'

import { combineMapFilters, formatMapRangeValue, mapValueFilteredEnvelope, mapValueFilterExpression, mapValueRange, mapValueRangePercent, withMapValueSelection } from './value-range'

const layer = { kind: 'point', value: { dataset: 'primary', field: 'orders' } } as any

test('map value ranges derive governed inline domains and preserve bounded selections', () => {
  const envelope = {
    spec: { datasets: [{ id: 'primary', fields: [{ id: 'orders', dataType: 'integer' }] }] },
    dataState: { kind: 'inline', datasets: [{ id: 'primary', columns: ['orders'], rows: [[4], [10], [7]] }] },
  } as any
  const initial = mapValueRange(envelope, layer)!
  expect(initial).toEqual({ minimum: 4, maximum: 10, selectedMinimum: 4, selectedMaximum: 10, step: 1 })
  expect(mapValueRange(envelope, layer, withMapValueSelection(initial, 6, 9))).toMatchObject({ selectedMinimum: 6, selectedMaximum: 9 })
})

test('map value ranges combine tiled raw and aggregate domains', () => {
  const envelope = {
    spec: { datasets: [{ id: 'primary', fields: [{ id: 'orders', dataType: 'integer' }] }] },
    dataState: {
      kind: 'spatial_tiled', schema: { id: 'primary' },
      rawDomains: [{ field: 'orders', minimum: 1, maximum: 40 }],
      aggregateDomains: [{ field: 'orders', minimum: 3, maximum: 12_800 }],
    },
  } as any
  expect(mapValueRange(envelope, layer)).toEqual({ minimum: 1, maximum: 12_800, selectedMinimum: 1, selectedMaximum: 12_800, step: 1 })
})

test('map value filters preserve authored layer filters and disappear at the full range', () => {
  const range = { minimum: 4, maximum: 10, selectedMinimum: 5, selectedMaximum: 9, step: 1 }
  const selection = mapValueFilterExpression('orders', range)
  expect(selection).toEqual(['all', ['has', 'orders'], ['>=', ['get', 'orders'], 5], ['<=', ['get', 'orders'], 9]])
  expect(combineMapFilters(['==', ['geometry-type'], 'Point'], selection)).toEqual(['all', ['==', ['geometry-type'], 'Point'], selection])
  expect(mapValueFilterExpression('orders', { ...range, selectedMinimum: 4, selectedMaximum: 10 })).toBeUndefined()
})

test('map value filters project the same visible inline rows into controls and accessibility', () => {
  const envelope = {
    spec: { datasets: [{ id: 'primary', fields: [{ id: 'orders', dataType: 'integer' }] }] },
    dataState: { kind: 'inline', datasets: [{ id: 'primary', columns: ['state', 'orders'], rows: [['SP', 10], ['RJ', 6], ['MG', 4]] }] },
  } as any
  const range = { minimum: 4, maximum: 10, selectedMinimum: 6, selectedMaximum: 10, step: 1 }
  const filtered = mapValueFilteredEnvelope(envelope, [{ id: 'orders', ...layer }], new Map([['orders', range]]))
  expect(filtered.dataState.datasets[0].rows).toEqual([['SP', 10], ['RJ', 6]])
  expect(envelope.dataState.datasets[0].rows).toHaveLength(3)
  expect(mapValueFilteredEnvelope(envelope, [{ id: 'orders', ...layer }], new Map([['orders', { ...range, selectedMinimum: 4 }]]))).toBe(envelope)
})

test('map value range presentation formats compact values and positions the selected segment', () => {
  const range = { minimum: 0, maximum: 20, selectedMinimum: 5, selectedMaximum: 15, step: 1 }
  expect(mapValueRangePercent(5, range)).toBe(25)
  expect(mapValueRangePercent(15, range)).toBe(75)
  expect(formatMapRangeValue(12_800)).toBe('12.8k')
})
