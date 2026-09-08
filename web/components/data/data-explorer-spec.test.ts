import { expect, test } from 'bun:test'
import type { ExplorationSpec } from '../../generated/exploration'
import {
  boundedExplorationLimit,
  explorationPivotValidation,
  explorationRunValidation,
  filterOperatorsForType,
  unsupportedRelativeTimeRangeMessage,
  explorationSortFields,
  moveExplorationSort,
  setExplorationTime,
  setExplorationTimeRange,
  updateExplorationPivot,
  upsertExplorationSort,
} from './data-explorer-spec'

const baseSpec = (): ExplorationSpec => ({
  schemaVersion: 1,
  modelId: 'sales',
  datasetId: 'orders',
  dimensions: [{ field: 'orders.status' }, { field: 'orders.created_at' }],
  metrics: [{ field: 'revenue' }],
  filters: [],
  sort: [],
  limit: 100,
})

test('exploration sort controls preserve authored priority and reject unknown fields', () => {
  const spec = baseSpec()
  const sorted = upsertExplorationSort(upsertExplorationSort(spec, 'revenue', 'desc'), 'orders.status')
  expect(explorationSortFields(sorted)).toEqual(['orders.status', 'orders.created_at', 'revenue'])
  expect(sorted.sort).toEqual([
    { field: 'revenue', direction: 'desc' },
    { field: 'orders.status', direction: 'asc' },
  ])
  expect(upsertExplorationSort(sorted, 'missing')).toBe(sorted)
  expect(moveExplorationSort(sorted, 1, -1).sort.map((entry) => entry.field)).toEqual(['orders.status', 'revenue'])
})

test('time controls add and remove a canonical time selection and range', () => {
  const selected = setExplorationTime(baseSpec(), 'orders.created_at', 'day')
  expect(selected.time).toEqual({ field: 'orders.created_at', grain: 'day' })
  const ranged = setExplorationTimeRange(selected, {
    kind: 'relative', direction: 'previous', count: 3, unit: 'month', includeCurrent: true, anchor: 'current_time',
  })
  expect(ranged.time?.range?.kind).toBe('relative')
  expect(setExplorationTime(ranged, '').time).toBeUndefined()
})

test('changing or clearing time removes sorts for the old field and alias', () => {
  const spec: ExplorationSpec = {
    ...baseSpec(),
    time: { field: 'orders.created_at', grain: 'day', alias: 'order_day' },
    sort: [
      { field: 'orders.created_at', direction: 'asc' },
      { field: 'order_day', direction: 'desc' },
      { field: 'revenue', direction: 'desc' },
    ],
  }
  const changed = setExplorationTime(spec, 'orders.updated_at', 'month')
  expect(changed.sort).toEqual([{ field: 'revenue', direction: 'desc' }])
  expect(changed.time).toEqual({ field: 'orders.updated_at', grain: 'month', alias: 'order_day' })
  expect(setExplorationTime(spec, '').sort).toEqual([{ field: 'revenue', direction: 'desc' }])
})

test('pivot validation reports unsafe role and metric combinations', () => {
  const spec = updateExplorationPivot({ ...baseSpec(), pivot: { rows: [], columns: [], metrics: [] } }, 'rows', [{ field: 'revenue' }])
  const messages = explorationPivotValidation(spec, [
    { id: 'revenue', label: 'Revenue', kind: 'metric', datasetId: 'orders', compatible: true, selected: false },
  ])
  expect(messages).toContain('Add at least one column dimension before running a pivot.')
  expect(messages).toContain('revenue is a metric; choose a dimension for pivot rows.')
  expect(messages).toContain('Add at least one metric before running a pivot.')
})

test('pivot validation permits governed totals and blocks unsupported offsets', () => {
  const spec = {
    ...baseSpec(),
    pivot: {
      rows: [{ field: 'orders.status' }],
      columns: [{ field: 'orders.channel' }],
      metrics: [{ field: 'revenue' }],
      totals: { rows: true, columns: true, grand: true },
      window: { limit: 50, offset: 0 },
    },
  }
  const fields = [
    { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', compatible: true, selected: true },
    { id: 'orders.channel', label: 'Channel', kind: 'dimension', datasetId: 'orders', compatible: true, selected: true },
    { id: 'revenue', label: 'Revenue', kind: 'metric', datasetId: 'orders', compatible: true, selected: true },
  ]
  expect(explorationPivotValidation(spec, fields)).toEqual([])
  expect(explorationPivotValidation({ ...spec, pivot: { ...spec.pivot, window: { limit: 50, offset: 1 } } }, fields))
    .toContain('Pivot row-window offsets are not available yet. Use an offset of 0.')
})

test('row and pivot limits are bounded to the server contract', () => {
  expect(boundedExplorationLimit(Number.NaN)).toBe(100)
  expect(boundedExplorationLimit(-20)).toBe(1)
  expect(boundedExplorationLimit(2001)).toBe(1000)
  expect(boundedExplorationLimit(250.9)).toBe(250)
})

test('run validation fails closed for empty or unsafe drafts', () => {
  const empty = baseSpec()
  empty.modelId = ''
  empty.dimensions = []
  empty.metrics = []
  expect(explorationRunValidation(empty)).toEqual([
    'Choose a semantic model before running the exploration.',
    'Select at least one field or time grain before running the exploration.',
  ])
  const unsafe = updateExplorationPivot(baseSpec(), 'rows', [{ field: 'revenue' }])
  expect(explorationRunValidation(unsafe, [{ id: 'revenue', label: 'Revenue', kind: 'metric', datasetId: 'orders', compatible: true, selected: false }])).toContain('revenue is a metric; choose a dimension for pivot rows.')
})

test('relative time ranges are visibly unsupported and blocked from execution', () => {
  const spec = setExplorationTimeRange(setExplorationTime(baseSpec(), 'orders.created_at', 'day'), {
    kind: 'relative', direction: 'previous', count: 3, unit: 'month', includeCurrent: true, anchor: 'current_time',
  })
  expect(explorationRunValidation(spec)).toContain(unsupportedRelativeTimeRangeMessage)
})

test('strict comparisons are offered for numeric and date fields', () => {
  const numeric = filterOperatorsForType('decimal').map((option) => option.value)
  const date = filterOperatorsForType('date').map((option) => option.value)
  expect(numeric).toEqual(expect.arrayContaining(['greater_than', 'less_than']))
  expect(date).toEqual(expect.arrayContaining(['greater_than', 'less_than']))
  expect(filterOperatorsForType('string').map((option) => option.value)).not.toEqual(expect.arrayContaining(['greater_than', 'less_than']))
})
