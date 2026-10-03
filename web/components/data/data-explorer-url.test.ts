import { expect, test } from 'bun:test'
import type { DataExplorerCommand } from '../../generated/signals'
import { DataExplorerQueryController } from './data-explorer-controller'
import { dataExplorerExportURL, dataExplorerURL, savedExplorationShareURL } from './data-explorer-url'

test('exploration URL deterministically includes durable query state only', () => {
  const command = {
    mode: 'explore',
    requestSeq: 91,
    resetVersion: 18,
    explore: {
      semanticModelId: 'semantic:sales',
      datasetId: 'orders',
      dimensions: ['orders.month', 'customers.state'],
      metrics: ['revenue'],
      filters: [{ field: 'customers.state', operator: 'equals', values: ['CA'] }],
      sort: [{ field: 'revenue', direction: 'desc' }],
      time: { field: 'orders.created_at', grain: 'month' },
      limit: 250,
      requestSeq: 92,
      resetVersion: 19,
    },
  } as DataExplorerCommand

  const url = dataExplorerURL(command)
  const parsed = new URL(url, 'https://example.test')
  expect(parsed.searchParams.get('v')).toBe('2')
  expect(parsed.searchParams.get('mode')).toBe('explore')
  const state = JSON.parse(parsed.searchParams.get('state')!)
  expect(state.modelId).toBe('semantic:sales')
  expect(state.datasetId).toBe('orders')
  expect(state.dimensions.map((item: { field: string }) => item.field)).toEqual(['orders.month', 'customers.state'])
  expect(state.metrics.map((item: { field: string }) => item.field)).toEqual(['revenue'])
  expect(state.limit).toBe(250)
  expect(parsed.searchParams.has('semanticModel')).toBe(false)
  expect(parsed.searchParams.has('model')).toBe(false)
  expect(parsed.searchParams.has('requestSeq')).toBe(false)
  expect(parsed.searchParams.has('resetVersion')).toBe(false)
  expect(dataExplorerURL(command)).toBe(url)
})

test('browse URL preserves only the selected object', () => {
  expect(dataExplorerURL({ mode: 'browse', objectKey: 'model:orders' } as DataExplorerCommand)).toBe('/explore?object=model%3Aorders')
})

test('saved share URL resolves the authorized saved version without draft query state', () => {
  expect(savedExplorationShareURL('exploration:orders')).toBe('/explore/saved/exploration%3Aorders?navigation=true')
  expect(savedExplorationShareURL('exploration:orders', true)).toBe('/explore/saved/exploration%3Aorders?navigation=true&includeArchived=true')
  expect(savedExplorationShareURL('')).toBe('')
})

test('export URL contains canonical state and no saved identity', () => {
  const command = { mode: 'explore', explore: { spec: {
    schemaVersion: 1, modelId: 'semantic:sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }],
    metrics: [], filters: [], sort: [], limit: 100,
  } } } as unknown as DataExplorerCommand
  const url = new URL(dataExplorerExportURL(command, 'csv'), 'https://example.test')
  expect(url.pathname).toBe('/explore/export')
  expect(url.searchParams.get('format')).toBe('csv')
  expect(url.searchParams.has('saved')).toBe(false)
  expect(JSON.parse(url.searchParams.get('state')!).modelId).toBe('semantic:sales')
})

test('browse URL retains active row filters for refresh', () => {
  const command = { mode: 'browse', objectKey: 'model:zip', explore: { spec: {
    schemaVersion: 1, modelId: 'semantic-model:visuals', datasetId: 'zip_geolocations',
    dimensions: [], metrics: [], filters: [{ field: 'zip_geolocations.state', op: 'eq', value: { kind: 'string', value: 'SP' } }], sort: [], limit: 100,
  } } } as unknown as DataExplorerCommand
  const url = new URL(dataExplorerURL(command), 'https://example.test')
  expect(url.searchParams.get('object')).toBe('model:zip')
  expect(url.searchParams.get('mode')).toBe('browse')
  expect(JSON.parse(url.searchParams.get('state')!).filters).toHaveLength(1)
})

test('canonical draft edits reach the URL before the server responds', () => {
  const spec = {
    schemaVersion: 1 as const, modelId: 'semantic:sales', datasetId: 'orders',
    dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 50,
    time: { field: 'orders.purchase_date', grain: 'month' as const },
  }
  const current = {
    spec, semanticModelId: spec.modelId, datasetId: spec.datasetId,
    dimensions: ['orders.status'], metrics: [], filters: [], sort: [],
    time: { field: 'orders.purchase_date', grain: 'month' }, limit: 50, requestSeq: 1, resetVersion: 1, columnWidths: {},
  }
  const draft = new DataExplorerQueryController().exploreSpec(current, {
    dimensions: [{ field: 'orders.status' }, { field: 'orders.category' }], time: undefined,
  })
  const url = dataExplorerURL({ mode: 'explore', explore: draft } as DataExplorerCommand)
  const encoded = JSON.parse(new URL(url, 'https://example.test').searchParams.get('state')!)
  expect(encoded.dimensions).toEqual([{ field: 'orders.status' }, { field: 'orders.category' }])
  expect(encoded.time).toBeUndefined()
})
