import { describe, expect, test } from 'bun:test'
import type { ExplorationSpec } from '../../generated/exploration'
import type { DataExploreCommand } from '../../generated/signals'
import {
  DataExplorerPanelController,
  DataExplorerQueryController,
  DataExplorerSelectionController,
  readDataExplorerAgentState,
  toggleVisibleColumns,
} from './data-explorer-controller'

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
  const first: DataExploreCommand = {
    spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 },
    requestSeq: 1, resetVersion: 4, columnWidths: { revenue: 180 },
  }
  const next = query.explore(first, { dimensions: [{ field: 'orders.status' }] })
  expect(next.spec.dimensions).toEqual([{ field: 'orders.status' }])
  expect(next.requestSeq).toBe(2)
  expect(next.resetVersion).toBe(5)
  expect(next.columnWidths).toEqual({ revenue: 180 })
  expect((next.spec as ExplorationSpec & { requestSeq?: number }).requestSeq).toBeUndefined()
  const clearedDataset = query.explore(first, { datasetId: undefined })
  expect(clearedDataset.spec.datasetId).toBeUndefined()
})

test('query command normalizes a partially hydrated exploration command on output', () => {
  const query = new DataExplorerQueryController()
  const legacy = {
    mode: 'explore',
    explore: { modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 1, resetVersion: 1 },
  } as unknown as DataExplorerCommand

  const next = query.command(legacy, {})
  expect((legacy.explore as any).spec).toBeUndefined()
  expect(next.explore?.spec).toEqual({ schemaVersion: 1, modelId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 })
  expect(next.explore).not.toBe(legacy.explore)
})

test('visible column toggles preserve one visible fallback and reset all to defaults', () => {
  expect(toggleVisibleColumns(['a', 'b'], 'b', false, ['a', 'b'])).toEqual(['a'])
  expect(toggleVisibleColumns(['a'], 'b', true, ['a', 'b'])).toEqual([])
  expect(readDataExplorerAgentState(memoryStorage())).toEqual({ open: false, conversationId: '' })
})
