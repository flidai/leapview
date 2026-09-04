import { expect, test } from 'bun:test'
import type { ExplorationSpec } from '../../generated/exploration'
import type { DataExplorerCommand } from '../../generated/signals'
import { explorationResultKeyForSort, explorationSortFieldForResult, explorationSpecFor, makeExplorationFilter, removeExplorationField, toggleExplorationField } from './data-explorer-spec'
import { dataExplorerURL, updateDataExplorerURL } from './data-explorer-url'

const originalWindow = globalThis.window

test('exploration URL deterministically includes durable query state only', () => {
  const spec: ExplorationSpec = {
    schemaVersion: 1,
    modelId: 'semantic:sales',
    datasetId: 'orders',
    dimensions: [{ field: 'orders.month', alias: 'month', grain: 'month' }, { field: 'customers.state' }],
    metrics: [{ field: 'revenue', alias: 'revenue_total' }],
    filters: [
      { field: 'customers.state', expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] } },
      { field: 'orders.total', expression: { kind: 'comparison', operator: 'greater_than_or_equal', value: { kind: 'decimal', value: '100.50' } } },
    ],
    time: {
      field: 'orders.created_at', grain: 'month', alias: 'period',
      range: { kind: 'relative', direction: 'previous', count: 3, unit: 'month', includeCurrent: true, anchor: 'current_time' },
    },
    sort: [{ field: 'revenue_total', direction: 'desc' }],
    limit: 250,
    pivot: {
      rows: [{ field: 'orders.month', grain: 'month' }], columns: [{ field: 'customers.state' }], metrics: [{ field: 'revenue' }],
      sort: [{ field: 'revenue', direction: 'desc' }], totals: { rows: true, columns: false, grand: true }, window: { offset: 10, limit: 50 },
    },
    table: { columns: [{ field: 'revenue', label: 'Revenue', width: 180, format: { kind: 'number', maximumFractionDigits: 2 } }], density: 'comfortable', striped: true, showHeader: true, rowHeight: 32 },
    visualization: { kind: 'cartesian', title: 'Revenue trend', subtitle: 'By month', legend: 'bottom', displayUnits: 'millions', orientation: 'vertical', stacking: 'none', mark: 'bar', x: { field: 'orders.month' }, y: [{ field: 'revenue', format: { kind: 'number', maximumFractionDigits: 2 } }], series: { field: 'customers.state' }, smooth: false, showSymbols: true },
  }
  const command: DataExplorerCommand = { mode: 'explore', requestSeq: 91, resetVersion: 18, columnWidths: { revenue: 180 }, explore: { spec, requestSeq: 92, resetVersion: 19, columnWidths: { revenue: 181 } } }

  const url = dataExplorerURL(command)
  const parsed = new URL(url, 'https://example.test')
  expect(parsed.searchParams.get('v')).toBe('2')
  expect(parsed.searchParams.get('mode')).toBe('explore')
  expect(JSON.parse(parsed.searchParams.get('state')!)).toEqual(spec)
  expect(parsed.searchParams.has('model')).toBe(false)
  expect(parsed.searchParams.has('dimension')).toBe(false)
  expect(parsed.searchParams.has('requestSeq')).toBe(false)
  expect(parsed.searchParams.has('resetVersion')).toBe(false)
  expect(parsed.searchParams.has('columnWidths')).toBe(false)
  const reorderedSpec = {
    visualization: { ...spec.visualization!, y: [{ field: 'revenue', format: { maximumFractionDigits: 2, kind: 'number' } }] },
    table: { ...spec.table!, columns: [{ format: { maximumFractionDigits: 2, kind: 'number' }, width: 180, label: 'Revenue', field: 'revenue' }] },
    pivot: spec.pivot,
    limit: spec.limit,
    sort: spec.sort,
    time: spec.time,
    filters: spec.filters,
    metrics: spec.metrics,
    dimensions: spec.dimensions,
    datasetId: spec.datasetId,
    modelId: spec.modelId,
    schemaVersion: spec.schemaVersion,
  } as ExplorationSpec
  expect(dataExplorerURL({ ...command, explore: { ...command.explore!, spec: reorderedSpec } })).toBe(url)
  expect(dataExplorerURL(command)).toBe(url)
})

test('browse URL preserves only the selected object', () => {
  expect(dataExplorerURL({ mode: 'browse', objectKey: 'model:orders' } as DataExplorerCommand)).toBe('/explore?object=model%3Aorders')
})

test('filter editor preserves typed scalar values and fails closed', () => {
  expect(makeExplorationFilter('orders.count', 'greater_than', ['10'], 'number')?.expression).toEqual({ kind: 'comparison', operator: 'greater_than', value: { kind: 'decimal', value: '10' } })
  expect(makeExplorationFilter('orders.active', 'equals', ['true'], 'boolean')?.expression).toEqual({ kind: 'comparison', operator: 'equals', value: { kind: 'boolean', value: true } })
  expect(makeExplorationFilter('orders.amount', 'equals', ['0.5'], 'number')?.expression).toEqual({ kind: 'comparison', operator: 'equals', value: { kind: 'decimal', value: '0.5' } })
  expect(makeExplorationFilter('orders.amount', 'equals', ['-0.5'], 'number')?.expression).toEqual({ kind: 'comparison', operator: 'equals', value: { kind: 'decimal', value: '-0.5' } })
  expect(makeExplorationFilter('orders.created_at', 'equals', ['2026-09-03T12:00:00Z'], 'timestamp')?.expression).toEqual({ kind: 'comparison', operator: 'equals', value: { kind: 'timestamp', value: '2026-09-03T12:00:00Z' } })
  expect(makeExplorationFilter('orders.count', 'equals', ['1.'], 'number')).toBeUndefined()
  expect(makeExplorationFilter('orders.created_on', 'equals', ['2026-02-29'], 'date')).toBeUndefined()
  expect(makeExplorationFilter('orders.created_on', 'equals', ['2024-02-29'], 'date')).toBeDefined()
  expect(makeExplorationFilter('orders.created_at', 'equals', ['2026-02-30T12:00:00Z'], 'timestamp')).toBeUndefined()
  expect(makeExplorationFilter('orders.status', 'made_up', ['open'], 'string')).toBeUndefined()
  expect(makeExplorationFilter('orders.status', 'is_null', ['open'], 'string')).toBeUndefined()
  expect(makeExplorationFilter('orders.status', 'is_null', [], 'string')).toBeDefined()
  expect(makeExplorationFilter('orders.status', 'in', [], 'string')).toBeUndefined()
  expect(makeExplorationFilter('orders.status', 'equals', [], 'string')).toBeUndefined()
  expect(makeExplorationFilter('orders.status', 'equals', ['open', 'closed'], 'string')).toBeUndefined()
})

test('filter editor scopes physical dimensions but leaves conformed dimensions unscoped', () => {
  const physical = makeExplorationFilter({ id: 'orders.status', kind: 'dimension', datasetId: 'orders' }, 'equals', ['paid'], 'string')
  expect(physical).toMatchObject({ field: 'orders.status', datasetId: 'orders' })

  const conformed = makeExplorationFilter({ id: 'order_status', kind: 'dimension', datasetId: 'orders' }, 'equals', ['paid'], 'string')
  expect(conformed).toMatchObject({ field: 'order_status', expression: { kind: 'comparison' } })
  expect(conformed).not.toHaveProperty('datasetId')
})

test('explore URL waits for a non-empty required model', () => {
  expect(dataExplorerURL({ mode: 'explore', explore: { spec: { schemaVersion: 1, modelId: ' ', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 }, requestSeq: 1, resetVersion: 1 } } as DataExplorerCommand)).toBe('/explore')
})

test('legacy partial exploration state gets browser-only empty spec fallback', () => {
  const legacy = { modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 } as any
  const command = { mode: 'explore', explore: legacy } as unknown as DataExplorerCommand
  expect(explorationSpecFor(legacy)).toEqual({ schemaVersion: 1, modelId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 })
  expect(dataExplorerURL(command)).toBe('/explore')
  expect(legacy.spec).toBeUndefined()
})

test('result aliases resolve to selected canonical sort references', () => {
  const spec: ExplorationSpec = {
    schemaVersion: 1, modelId: 'sales', datasetId: 'orders',
    dimensions: [{ field: 'orders.status' }, { field: 'customers.status' }, { field: 'orders.created_at', alias: 'order_date' }],
    metrics: [{ field: 'revenue', alias: 'total_revenue' }], filters: [], sort: [], limit: 100,
  }
  const qualifiedSpec: ExplorationSpec = { ...spec, dimensions: [{ field: 'orders.status' }] }
  expect(explorationSortFieldForResult(qualifiedSpec, 'status')).toBe('orders.status')
  expect(explorationResultKeyForSort(qualifiedSpec, 'orders.status', ['status'])).toBe('status')
  expect(explorationSortFieldForResult(spec, 'orders__status')).toBe('orders.status')
  expect(explorationSortFieldForResult(spec, 'order_date')).toBe('order_date')
  expect(explorationResultKeyForSort(spec, 'order_date', ['order_date'])).toBe('order_date')
  const authoredAliasSpec: ExplorationSpec = { ...spec, dimensions: [{ field: 'orders.status', alias: 'status_label' }], metrics: [] }
  expect(explorationResultKeyForSort(authoredAliasSpec, 'orders.status', ['status_label'])).toBe('status_label')
  expect(explorationResultKeyForSort(authoredAliasSpec, 'status_label', ['status_label'])).toBe('status_label')
  expect(explorationSortFieldForResult(spec, 'total_revenue')).toBe('total_revenue')
  expect(explorationSortFieldForResult(spec, 'unselected')).toBeUndefined()
})

test('time result aliases resolve to canonical time sort references', () => {
  const spec: ExplorationSpec = {
    schemaVersion: 1, modelId: 'sales', dimensions: [], metrics: [], filters: [], sort: [], limit: 100,
    time: { field: 'orders.created_at', grain: 'day', alias: 'order_date' },
  }
  expect(explorationSortFieldForResult(spec, 'order_date')).toBe('order_date')
  expect(explorationSortFieldForResult(spec, 'orders.created_at')).toBe('orders.created_at')
  expect(explorationResultKeyForSort(spec, 'orders.created_at', ['order_date'])).toBe('order_date')
  expect(explorationResultKeyForSort(spec, 'order_date', ['order_date'])).toBe('order_date')
})

test('decorated time dimensions merge into one sortable result reference', () => {
  const spec: ExplorationSpec = {
    schemaVersion: 1, modelId: 'sales', dimensions: [{ field: 'orders.created_at' }], metrics: [], filters: [], sort: [], limit: 100,
    time: { field: 'orders.created_at', grain: 'day', alias: 'order_day' },
  }
  expect(explorationSortFieldForResult(spec, 'order_day')).toBe('order_day')
  expect(explorationResultKeyForSort(spec, 'orders.created_at', ['order_day'])).toBe('order_day')

  const explicitlyAliased: ExplorationSpec = {
    ...spec,
    dimensions: [{ field: 'orders.created_at', alias: 'calendar_day' }],
    time: { ...spec.time!, alias: 'calendar_day' },
  }
  expect(explorationSortFieldForResult(explicitlyAliased, 'calendar_day')).toBe('calendar_day')
  expect(explorationResultKeyForSort(explicitlyAliased, 'orders.created_at', ['calendar_day'])).toBe('calendar_day')
})

test('removing or toggling a field clears sorts for its field and authored alias', () => {
  const spec: ExplorationSpec = {
    schemaVersion: 1, modelId: 'sales', dimensions: [{ field: 'orders.status', alias: 'status_label' }], metrics: [{ field: 'revenue', alias: 'total_revenue' }], filters: [],
    sort: [{ field: 'orders.status', direction: 'asc' }, { field: 'status_label', direction: 'desc' }, { field: 'total_revenue', direction: 'asc' }], limit: 100,
  }
  expect(removeExplorationField(spec, 'orders.status', 'dimension').sort).toEqual([{ field: 'total_revenue', direction: 'asc' }])
  expect(toggleExplorationField(spec, 'orders.status', 'dimension').sort).toEqual([{ field: 'total_revenue', direction: 'asc' }])
})

test('durable explorer edits push while canonicalization replaces and unchanged URLs are inert', () => {
  const calls: Array<{ mode: string; state: unknown; url: string }> = []
  const fakeWindow = {
    location: { pathname: '/explore', search: '?object=orders' },
    history: {
      state: { stream: 'explorer' },
      pushState: (state: unknown, _title: string, url: string) => calls.push({ mode: 'push', state, url }),
      replaceState: (state: unknown, _title: string, url: string) => calls.push({ mode: 'replace', state, url }),
    },
  }
  Object.defineProperty(globalThis, 'window', { value: fakeWindow, configurable: true })
  try {
    const command = { mode: 'browse', objectKey: 'customers' } as DataExplorerCommand
    updateDataExplorerURL(command, 'push')
    expect(calls).toEqual([{ mode: 'push', state: { stream: 'explorer' }, url: '/explore?object=customers' }])

    fakeWindow.location.search = '?object=customers'
    updateDataExplorerURL(command, 'replace')
    expect(calls).toHaveLength(1)

    updateDataExplorerURL({ mode: 'explore', objectKey: 'customers', explore: { spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'customers', dimensions: [{ field: 'customers.state' }], metrics: [], filters: [], sort: [], limit: 100 }, requestSeq: 1, resetVersion: 1 } } as DataExplorerCommand, 'replace')
    expect(calls[1]).toMatchObject({ mode: 'replace', url: '/explore?v=2&mode=explore&state=%7B%22datasetId%22%3A%22customers%22%2C%22dimensions%22%3A%5B%7B%22field%22%3A%22customers.state%22%7D%5D%2C%22filters%22%3A%5B%5D%2C%22limit%22%3A100%2C%22metrics%22%3A%5B%5D%2C%22modelId%22%3A%22sales%22%2C%22schemaVersion%22%3A1%2C%22sort%22%3A%5B%5D%7D' })
  } finally {
    Object.defineProperty(globalThis, 'window', { value: originalWindow, configurable: true })
  }
})
