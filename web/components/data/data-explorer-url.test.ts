import { expect, test } from 'bun:test'
import type { DataExplorerCommand } from '../../generated/signals'
import { dataExplorerURL } from './data-explorer-url'

test('exploration URL deterministically includes durable query state only', () => {
  const command = {
    mode: 'explore',
    requestSeq: 91,
    resetVersion: 18,
    explore: {
      modelId: 'semantic:sales',
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
  expect(parsed.searchParams.get('v')).toBe('1')
  expect(parsed.searchParams.getAll('dimension')).toEqual(['orders.month', 'customers.state'])
  expect(parsed.searchParams.getAll('metric')).toEqual(['revenue'])
  expect(parsed.searchParams.get('limit')).toBe('250')
  expect(parsed.searchParams.has('requestSeq')).toBe(false)
  expect(parsed.searchParams.has('resetVersion')).toBe(false)
  expect(dataExplorerURL(command)).toBe(url)
})

test('browse URL preserves only the selected object', () => {
  expect(dataExplorerURL({ mode: 'browse', objectKey: 'model:orders' } as DataExplorerCommand)).toBe('/explore?object=model%3Aorders')
})
