import { describe, expect, test } from 'bun:test'
import type { ExplorationSpec } from '../../generated/exploration'
import type { DataExploreCommand, DataExplorerCommand } from '../../generated/signals'
import {
  DataExplorerPanelController,
  DataExplorerQueryController,
  DataExplorerSelectionController,
  prepareExplorationRun,
  prepareExplorationStop,
  readDataExplorerAgentState,
  toggleVisibleColumns,
} from './data-explorer-controller'
import { DataExplorerClientState } from './data-explorer-client'
import { removeExplorationPivot, setExplorationTime } from './data-explorer-spec'

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

test('query controller applies complete specs so helper removals clear old selections', () => {
  const query = new DataExplorerQueryController()
  const current: DataExploreCommand = {
    spec: {
      schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100,
      time: { field: 'orders.created_at', grain: 'day' },
      pivot: { rows: [{ field: 'orders.status' }], columns: [{ field: 'orders.channel' }], metrics: [{ field: 'revenue' }] },
    },
    requestSeq: 1, resetVersion: 1, columnWidths: {},
  }

  const withoutTime = setExplorationTime(current.spec, '')
  const timeCleared = query.explore(current, withoutTime)
  expect(timeCleared.spec.time).toBeUndefined()

  const withoutPivot = removeExplorationPivot(current.spec)
  const pivotCleared = query.explore(current, withoutPivot)
  expect(pivotCleared.spec.pivot).toBeUndefined()
  expect(JSON.parse(JSON.stringify(timeCleared.spec)).time).toBeUndefined()
  expect(JSON.parse(JSON.stringify(pivotCleared.spec)).pivot).toBeUndefined()
})

test('query controller does not restore a pivot cleared by a drill-shaped full spec', () => {
  const query = new DataExplorerQueryController()
  const current: DataExploreCommand = {
    spec: {
      schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100,
      pivot: { rows: [{ field: 'orders.status' }], columns: [{ field: 'orders.channel' }], metrics: [{ field: 'revenue' }] },
    },
    requestSeq: 1, resetVersion: 1, columnWidths: {},
  }
  const drillSpec = { ...current.spec, pivot: undefined }
  expect(query.explore(current, drillSpec).spec.pivot).toBeUndefined()
})

test('explicit run creates a fresh sequence while stop preserves the addressed run', () => {
  const current: DataExploreCommand = {
    spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 },
    requestSeq: 7, resetVersion: 9, columnWidths: {}, action: 'configure',
  }
  const run = prepareExplorationRun(current)
  expect(run.action).toBe('run')
  expect(run.requestSeq).toBe(8)
  expect(run.resetVersion).toBe(10)
  const stop = prepareExplorationStop({ ...run, spec: { ...run.spec, limit: 250 }, requestSeq: 9 })
  expect(stop.action).toBe('stop')
  expect(stop.requestSeq).toBe(9)
  expect(stop.spec.limit).toBe(250)
})

test('query command normalizes a partially hydrated exploration command on output', () => {
  const query = new DataExplorerQueryController()
  const legacy = {
    mode: 'explore',
    clientId: 'tab-123',
    explore: { modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 1, resetVersion: 1 },
  } as unknown as DataExplorerCommand

  const next = query.command(legacy, {})
  expect((legacy.explore as any).spec).toBeUndefined()
  expect(next.explore?.spec).toEqual({ schemaVersion: 1, modelId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 })
  expect(next.clientId).toBe('tab-123')
  expect(next.explore).not.toBe(legacy.explore)
  expect(query.command({ ...next, action: 'configure' }, { action: undefined }).action).toBeUndefined()
})

test('configure and browse commands clear the run identity after Stop', () => {
  const query = new DataExplorerQueryController()
  const stopped: DataExplorerCommand = {
    action: 'stop', mode: 'explore', runId: 'explore-tombstoned',
    objectKey: 'model:model:sales.orders', offset: 0, limit: 100, block: 'all', start: 0, count: 100,
    requestSeq: 4, resetVersion: 4, sort: {}, visibleColumns: [], columnWidths: {},
    explore: {
      action: 'stop',
      spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 },
      requestSeq: 4, resetVersion: 4,
    },
  }
  const configured = query.command(stopped, {
    action: 'configure',
    mode: 'explore',
    explore: { ...stopped.explore!, action: 'configure', requestSeq: 5, resetVersion: 5 },
  })
  expect(configured.runId).toBeUndefined()

  const browsed = query.command(stopped, { action: undefined, mode: 'browse', objectKey: 'model:model:sales.customers' })
  expect(browsed.runId).toBeUndefined()
})

test('clearing a run identity prevents reuse of a stopped execution', () => {
  const client = new DataExplorerClientState()
  const runID = client.nextRunID()
  expect(client.runID()).toBe(runID)
  client.clearRunID()
  expect(client.runID()).toBe('')
})

test('visible column toggles preserve one visible fallback and reset all to defaults', () => {
  expect(toggleVisibleColumns(['a', 'b'], 'b', false, ['a', 'b'])).toEqual(['a'])
  expect(toggleVisibleColumns(['a'], 'b', true, ['a', 'b'])).toEqual([])
  expect(readDataExplorerAgentState(memoryStorage())).toEqual({ open: false, conversationId: '' })
})
