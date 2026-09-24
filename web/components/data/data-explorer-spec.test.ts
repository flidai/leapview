import { expect, test } from 'bun:test'
import type { ExplorationSpec } from '../../generated/exploration'
import {
  boundedExplorationLimit,
  explorationRunValidation,
  explorationSpecFromCommand,
  filterOperatorsForType,
  moveExplorationSort,
  setExplorationTime,
  setExplorationTimeRange,
  upsertExplorationSort,
} from './data-explorer-spec'

test('canonical exploration specs omit absent optional signal members', () => {
  const spec = explorationSpecFromCommand({
    spec: {
      schemaVersion: 1,
      modelId: 'semantic-model:sales',
      dimensions: [],
      metrics: [],
      filters: [],
      sort: [],
      limit: 100,
    },
    semanticModelId: 'semantic-model:sales',
    datasetId: '',
    dimensions: [],
    metrics: [],
    filters: [],
    sort: [],
    limit: 100,
    requestSeq: 0,
    resetVersion: 0,
    columnWidths: {},
  })

  expect(Object.hasOwn(spec, 'datasetId')).toBe(false)
  expect(Object.hasOwn(spec, 'time')).toBe(false)
  expect(JSON.stringify(spec)).not.toContain('"time"')
})

test('canonical exploration specs preserve typed and rich filters', () => {
  const range = {
    field: 'orders.amount',
    datasetId: 'orders',
    expression: {
      kind: 'range' as const,
      lower: { value: { kind: 'decimal' as const, value: '1.5' }, inclusive: true },
      upper: { value: { kind: 'decimal' as const, value: '9.5' }, inclusive: false },
    },
  }
  const typed = {
    field: 'orders.quantity',
    datasetId: 'orders',
    expression: {
      kind: 'comparison' as const,
      operator: 'greater_than' as const,
      value: { kind: 'integer' as const, value: '2' },
    },
  }
  const spec = explorationSpecFromCommand({
    spec: {
      schemaVersion: 1,
      modelId: 'semantic-model:sales',
      datasetId: 'orders',
      dimensions: [], metrics: [], filters: [range, typed], sort: [], limit: 100,
    },
    semanticModelId: 'semantic-model:sales', datasetId: 'orders', dimensions: [], metrics: [],
    filters: [
      { field: 'orders.amount', datasetId: 'orders', operator: 'range', values: [] },
      { field: 'orders.quantity', datasetId: 'orders', operator: 'greater_than', values: ['2'] },
    ],
    sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {},
  })

  expect(spec.filters).toEqual([range, typed])
})

test('canonical exploration filter dataset stays paired when invalid filters are dropped', () => {
  const spec = explorationSpecFromCommand({
    spec: { schemaVersion: 1, modelId: 'semantic-model:sales', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 },
    semanticModelId: 'semantic-model:sales', datasetId: 'orders', dimensions: [], metrics: [],
    filters: [
      { field: 'orders.invalid', datasetId: 'wrong', operator: 'unknown', values: [] },
      { field: 'orders.status', datasetId: 'orders', operator: 'equals', values: ['paid'] },
    ],
    sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {},
  })

  expect(spec.filters).toHaveLength(1)
  expect(spec.filters[0]?.datasetId).toBe('orders')
})

test('V1 query controls preserve sort priority, time range, and server row limits', () => {
  const base: ExplorationSpec = {
    schemaVersion: 1, modelId: 'sales', datasetId: 'orders',
    dimensions: [{ field: 'orders.status' }, { field: 'orders.created_at' }], metrics: [{ field: 'revenue' }],
    filters: [], sort: [], limit: 100,
  }
  const sorted = upsertExplorationSort(upsertExplorationSort(base, 'revenue', 'desc'), 'orders.status')
  expect(moveExplorationSort(sorted, 1, -1).sort.map((entry) => entry.field)).toEqual(['orders.status', 'revenue'])
  const timed = setExplorationTimeRange(setExplorationTime(base, 'orders.created_at', 'month'), {
    kind: 'absolute', lower: { value: { kind: 'date', value: '2026-01-01' }, inclusive: true },
  })
  expect(timed.time).toMatchObject({ field: 'orders.created_at', grain: 'month', range: { kind: 'absolute' } })
  expect(boundedExplorationLimit(-1)).toBe(1)
  expect(boundedExplorationLimit(2000)).toBe(1000)
})

test('V1 run validation and type-aware filters fail closed', () => {
  const empty: ExplorationSpec = { schemaVersion: 1, modelId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 }
  expect(explorationRunValidation(empty)).toEqual(expect.arrayContaining([
    'Choose a semantic model before running the exploration.',
    'Select at least one field or time grain before running the exploration.',
  ]))
  expect(filterOperatorsForType('decimal').map((option) => option.value)).toEqual(expect.arrayContaining(['greater_than', 'less_than']))
  expect(filterOperatorsForType('string').map((option) => option.value)).not.toEqual(expect.arrayContaining(['greater_than', 'less_than']))
})
