import { expect, test } from 'bun:test'
import type { DataExplorerCommand } from '../../generated/signals'
import { dataExplorerURL } from './data-explorer-url'

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
