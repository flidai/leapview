import { describe, expect, test } from 'bun:test'
import {
  DataExplorerPanelController,
  DataExplorerQueryController,
  DataExplorerSelectionController,
  dataExplorerAgentSuggestions,
  prepareExplorationRun,
  prepareExplorationStop,
  readDataExplorerAgentState,
  toggleVisibleColumns,
} from './data-explorer-controller'
import { DataExplorerClientState } from './data-explorer-client'
import { emptyDataExploreCommand, emptyExplorationSpec, setExplorationTime } from './data-explorer-spec'
import type { DataExploreCommand, DataExplorerCommand, DataExplorerSignal } from '../../generated/signals'

test('agent suggestions retain the active dataset identity and require project context', () => {
  const explorer = { explore: { datasets: [{ id: 'orders', title: 'Orders', description: 'Order facts' }] } } as DataExplorerSignal
  const command = { semanticModelId: 'sales', datasetId: 'orders' } as DataExploreCommand
  const context = { projectId: 'project-1', generationId: 'generation-1' } as NonNullable<Parameters<typeof dataExplorerAgentSuggestions>[2]>
  expect(dataExplorerAgentSuggestions(explorer, command)).toEqual([])
  expect(dataExplorerAgentSuggestions(explorer, command, context)).toMatchObject([{
    reference: { kind: 'dataset', id: 'sales/orders' },
    name: 'Orders',
    description: 'Order facts',
    hierarchy: ['project-1', 'sales'],
  }])
})

function memoryStorage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() { return values.size }, clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null, key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => values.delete(key), setItem: (key, value) => values.set(key, value),
  }
}

test('data explorer panel state clamps browser width and resets filters', () => {
  const panel = new DataExplorerPanelController()
  expect(panel.setBrowserWidth(100).browserWidth).toBe(280)
  expect(panel.setBrowserWidth(999).browserWidth).toBe(440)
  expect(panel.openFilter('status')).toMatchObject({ filterField: 'status', filterOperator: 'equals' })
  panel.setFilterOperator('contains')
  panel.setFilterValue('ship')
  expect(panel.closeFilter()).toMatchObject({ filterField: '', filterOperator: 'equals', filterValue: '' })
})

test('data explorer selection controller only reports actual selection changes', () => {
  const selection = new DataExplorerSelectionController()
  expect(selection.observe('orders')).toBe(true)
  expect(selection.observe('orders')).toBe(false)
  expect(selection.observe('customers')).toBe(true)
})

test('query controller advances request and reset sequences', () => {
  const query = new DataExplorerQueryController()
  const first = { spec: emptyExplorationSpec, semanticModelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 1, resetVersion: 4, columnWidths: {} }
  const next = query.explore(first, { dimensions: ['orders.status'] })
  expect(next.dimensions).toEqual(['orders.status'])
  expect(next.requestSeq).toBe(2)
  expect(next.resetVersion).toBe(5)
  expect(next.spec.modelId).toBe('sales')
  expect(next.spec.datasetId).toBe('orders')
  expect(next.spec.dimensions).toEqual([{ field: 'orders.status' }])
})

test('switching semantic models discards time and model-specific presentation', () => {
  const query = new DataExplorerQueryController()
  const current: DataExploreCommand = {
    ...emptyDataExploreCommand,
    semanticModelId: 'sales', datasetId: 'orders',
    time: { field: 'orders.created_at', grain: 'month' },
    spec: {
      ...emptyExplorationSpec, modelId: 'sales', datasetId: 'orders',
      dimensions: [{ field: 'status', alias: 'old_status' }],
      time: { field: 'orders.created_at', grain: 'month', range: { kind: 'absolute' } },
      table: { columns: [{ field: 'old_status' }] },
      visualization: { kind: 'table', columns: [{ field: 'old_status' }] },
      pivot: { rows: [{ field: 'status' }], columns: [{ field: 'category' }], metrics: [{ field: 'revenue' }] },
    },
  }
  const next = query.explore(current, {
    semanticModelId: 'inventory', datasetId: 'stock', dimensions: ['status'], metrics: [], filters: [], sort: [], time: undefined,
  })
  expect(next.spec).toEqual({ ...emptyExplorationSpec, modelId: 'inventory', datasetId: 'stock', dimensions: [{ field: 'status' }] })
})

test('canonical time clear survives command construction and subsequent compatibility edits', () => {
  const query = new DataExplorerQueryController()
  const current: DataExploreCommand = {
    ...emptyDataExploreCommand,
    spec: { ...emptyExplorationSpec, modelId: 'sales', time: { field: 'orders.created_at', grain: 'month' } },
    time: { field: 'orders.created_at', grain: 'month' },
  }
  const cleared = query.exploreSpec(current, setExplorationTime(current.spec, ''))
  expect(cleared.spec.time).toBeUndefined()
  expect(cleared.time).toBeUndefined()
  expect(query.explore(cleared, { limit: 250 }).spec.time).toBeUndefined()
})

test('canonical selection changes reconcile dependent presentation before commands are sent', () => {
  const query = new DataExplorerQueryController()
  const current: DataExploreCommand = {
    ...emptyDataExploreCommand,
    spec: {
      ...emptyExplorationSpec, modelId: 'sales', dimensions: [{ field: 'orders.status' }], metrics: [{ field: 'revenue' }],
      visualization: { kind: 'cartesian', mark: 'bar', x: { field: 'orders.status' }, y: [{ field: 'revenue' }] },
      table: { columns: [{ field: 'orders.status' }, { field: 'revenue' }] },
    },
  }
  const next = query.exploreSpec(current, { dimensions: [] })
  expect(next.spec.visualization).toBeUndefined()
  expect(next.spec.table?.columns).toEqual([{ field: 'revenue' }])
})

test('canonical query edits and explicit run lifecycle remain separate', () => {
  const query = new DataExplorerQueryController()
  const current = {
    spec: { schemaVersion: 1 as const, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 },
    semanticModelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100,
    requestSeq: 7, resetVersion: 9, columnWidths: {},
  }
  const configured = query.exploreSpec(current, { dimensions: [{ field: 'orders.status' }] })
  expect(configured).toMatchObject({ action: 'configure', requestSeq: 8, resetVersion: 10 })
  expect(configured.spec.dimensions).toEqual([{ field: 'orders.status' }])
  const run = prepareExplorationRun(configured)
  expect(run).toMatchObject({ action: 'run', requestSeq: 9, resetVersion: 11 })
  const stop = prepareExplorationStop(run)
  expect(stop).toMatchObject({ action: 'stop', requestSeq: 9, resetVersion: 11 })
})

test('data explorer client identity is stable only for the current document', () => {
  const first = new DataExplorerClientState().clientID()
  const second = new DataExplorerClientState().clientID()
  expect(first.startsWith('explorer-')).toBe(true)
  expect(second).toBe(first)
})

test('stale configure payloads cannot replace the last good result with retained SQL only', () => {
  const client = new DataExplorerClientState()
  const command: DataExploreCommand = {
    spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 },
    requestSeq: 4, resetVersion: 4, columnWidths: {}, dimensions: ['orders.status'], metrics: [], filters: [], sort: [], limit: 100,
  }
  const context = { projectId: 'project-1', generationId: 'generation-1' }
  const successful = {
    columns: [{ key: 'status', label: 'Status' }], rows: [{ status: 'delivered' }], rowsReturned: 1,
    durationMs: 8, requestSeq: 4, truncated: false, warnings: [], sql: 'SELECT status FROM orders',
  }
  const successStatus = { loading: false, stale: false, requestSeq: 4, state: 'success' as const }
  client.semanticResult(command, successful, successStatus, context)

  // Datastar recursively merges the suggestion response, so the omitted SQL
  // field can remain while its empty row and column arrays replace the result.
  const suggestionOnly = { ...command, action: 'configure' as const, filterSuggestions: { field: 'orders.status', limit: 50, search: '', suggestionRequestSeq: 1 } }
  const staleResult = {
    columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 4, truncated: false, warnings: [],
    sql: successful.sql,
  }
  const staleStatus = { loading: false, stale: true, requestSeq: 4, state: 'stale' as const }
  const retained = client.semanticResult(suggestionOnly, staleResult, staleStatus, context)

  expect(retained.columns).toEqual(successful.columns)
  expect(retained.rows).toEqual(successful.rows)
  expect(retained.rowsReturned).toBe(1)
})

test('unknown-outcome recovery omits the run ID and latest retries get a fresh ID', () => {
  const query = new DataExplorerQueryController()
  const client = new DataExplorerClientState()
  const oldRunID = client.nextRunID()
  const initialExplore: DataExploreCommand = {
    spec: emptyExplorationSpec, requestSeq: 6, resetVersion: 6, columnWidths: {},
    dimensions: [], metrics: [], filters: [], sort: [], limit: 100,
  }
  const run = prepareExplorationRun(initialExplore)
  const current: DataExplorerCommand = {
    action: 'run', mode: 'explore', runId: oldRunID, explore: run,
    count: 100, limit: 100, offset: 0, requestSeq: run.requestSeq, resetVersion: run.resetVersion,
    sort: {}, start: 0,
  }
  const stop = query.command(current, {
    action: 'stop', runId: undefined, explore: prepareExplorationStop(run),
  })

  expect(stop.runId).toBeUndefined()
  expect(stop.explore).toMatchObject({ action: 'stop', requestSeq: 7 })
  const latestRunID = client.nextRunID()
  expect(latestRunID).not.toBe(oldRunID)
})

test('visible column toggles preserve one visible fallback and reset all to defaults', () => {
  expect(toggleVisibleColumns(['a', 'b'], 'b', false, ['a', 'b'])).toEqual(['a'])
  expect(toggleVisibleColumns(['a'], 'b', true, ['a', 'b'])).toEqual([])
  expect(readDataExplorerAgentState(memoryStorage())).toEqual({ open: false, conversationId: '' })
})

test('table windows preserve query generation and clear on a new authored run', async () => {
  const { prepareExplorationWindow } = await import('./data-explorer-controller')
  const current = { ...emptyDataExploreCommand, requestSeq: 30, resetVersion: 4 }
  const window = { block: 'b' as const, start: 1500, count: 100, requestSeq: 7, resetVersion: 4 }
  const next = prepareExplorationWindow(current, { window })
  expect(next.requestSeq).toBe(31)
  expect(next.resetVersion).toBe(4)
  expect(next.window).toEqual(window)
  expect(prepareExplorationRun(next).window).toBeUndefined()
  expect(new DataExplorerQueryController().exploreSpec(next, { dimensions: [] }).window).toBeUndefined()
})

test('table paging keeps the initial chart frame within its query and generation', () => {
  const client = new DataExplorerClientState()
  const command = { ...emptyDataExploreCommand, requestSeq: 10, resetVersion: 3 }
  const result = { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 10, truncated: false, warnings: [] }
  const status = { state: 'success' as const, requestSeq: 10, loading: false, stale: false }
  const views = { chart: { dataRevision: 10 } } as any
  expect(client.semanticViews(command, result, status, views)).toEqual(views)
  const paged = { ...command, window: { block: 'a' as const, start: 1500, count: 100, requestSeq: 1, resetVersion: 3 }, requestSeq: 11 }
  expect(client.semanticViews(paged, { ...result, requestSeq: 11 }, { ...status, requestSeq: 11 }, { chart: { dataRevision: 11 } } as any)).toEqual(views)
  expect(client.semanticViews({ ...paged, resetVersion: 4 }, result, status, views)).toEqual({})
  expect(client.semanticViews({ ...paged, resetVersion: 4, window: { ...paged.window, start: 0 } }, result, status, views)).toEqual(views)
  expect(client.semanticViews(command, result, status, views, { generationId: 'new' })).toEqual(views)
})

test('stale or failed query status invalidates chart frames without losing valid window-loading retention', () => {
  for (const state of ['stale', 'error', 'cancelled'] as const) {
    const client = new DataExplorerClientState()
    const command = { ...emptyDataExploreCommand, requestSeq: 10, resetVersion: 3 }
    const result = { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 10, truncated: false, warnings: [] }
    const status = { state: 'success' as const, requestSeq: 10, loading: false, stale: false }
    const views = { chart: { dataRevision: 10 } } as any
    expect(client.semanticViews(command, result, status, views)).toEqual(views)
    const paged = { ...command, window: { block: 'a' as const, start: 1500, count: 100, requestSeq: 1, resetVersion: 3 }, requestSeq: 11 }
    expect(client.semanticViews(paged, result, undefined, {})).toEqual(views)
    expect(client.semanticViews(paged, result, { ...status, state: 'loading', requestSeq: 11, loading: true }, {})).toEqual(views)
    expect(client.semanticViews(paged, result, { ...status, state, requestSeq: 11, stale: state === 'stale' }, {})).toEqual({})
    expect(client.semanticViews(paged, { ...result, requestSeq: 11 }, { ...status, requestSeq: 11 }, { chart: { dataRevision: 11 } } as any)).toEqual({})
  }
})
