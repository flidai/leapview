import { describe, expect, test } from 'bun:test'
import {
  DataExplorerPanelController,
  DataExplorerQueryController,
  DataExplorerSelectionController,
  prepareExplorationRun,
  prepareExplorationStop,
  readDataExplorerAgentState,
  toggleVisibleColumns,
} from './data-explorer-controller'
import { emptyExplorationSpec } from './data-explorer-spec'

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

test('visible column toggles preserve one visible fallback and reset all to defaults', () => {
  expect(toggleVisibleColumns(['a', 'b'], 'b', false, ['a', 'b'])).toEqual(['a'])
  expect(toggleVisibleColumns(['a'], 'b', true, ['a', 'b'])).toEqual([])
  expect(readDataExplorerAgentState(memoryStorage())).toEqual({ open: false, conversationId: '' })
})
