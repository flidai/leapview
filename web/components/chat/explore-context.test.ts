import { expect, test } from 'bun:test'
import type { AgentContextSignal } from '../../generated/signals'
import { exploreContextHref } from './explore-context'

test('only an authorized exploration context offers a link', () => {
  expect(exploreContextHref(null)).toBeUndefined()
  expect(exploreContextHref({ surface: 'dashboard' } as AgentContextSignal)).toBeUndefined()
  expect(exploreContextHref({ exploration: { modelId: ' ' } } as AgentContextSignal)).toBeUndefined()
})

test('chat handoff preserves canonical fields and filters without dashboard or transcript data', () => {
  const context = {
    surface: 'explore',
    dashboardId: 'dashboard:private',
    exploration: {
      schemaVersion: 1,
      modelId: 'semantic:sales',
      datasetId: 'orders',
      dimensions: [{ field: 'orders.purchase_month' }],
      metrics: [{ field: 'revenue' }],
      filters: [{ field: 'orders.status', op: 'eq', value: { kind: 'string', value: 'delivered' } }],
      sort: [],
      limit: 100,
    },
  } as unknown as AgentContextSignal
  const url = new URL(exploreContextHref(context)!, 'https://example.test')
  expect(url.pathname).toBe('/explore')
  expect(url.searchParams.get('mode')).toBe('explore')
  expect(url.searchParams.has('dashboard')).toBe(false)
  const spec = JSON.parse(url.searchParams.get('state')!)
  expect(spec.modelId).toBe('semantic:sales')
  expect(spec.dimensions).toEqual([{ field: 'orders.purchase_month' }])
  expect(spec.filters).toHaveLength(1)
})
