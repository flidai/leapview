import { expect, test } from 'bun:test'
import { ProductSearchService } from './product-search-service'

test('product search waits for an asset query instead of returning navigation shortcuts', async () => {
  let requests = 0
  const service = new ProductSearchService((async () => {
    requests += 1
    throw new Error('empty search must not fetch')
  }) as typeof fetch)

  const results = await service.search('')

  expect(requests).toBe(0)
  expect(results).toEqual([])
})

test('product search returns only supported assets with canonical destinations', async () => {
  let requestedURL = ''
  const service = new ProductSearchService((async (input) => {
    requestedURL = String(input)
    return new Response(JSON.stringify({
      items: [
        { reference: { id: 'dashboard:sales', kind: 'dashboard' }, name: 'sales', displayName: 'Sales dashboard', href: '/dashboards/dashboard:sales' },
        { reference: { id: 'model:orders', kind: 'model' }, name: 'orders', displayName: 'Orders model', href: '/models/model:orders/details' },
        { reference: { id: 'project:demo', kind: 'project' }, name: 'demo', displayName: 'Demo project', href: '/' },
        { reference: { id: 'metric:revenue', kind: 'metric' }, name: 'revenue', displayName: 'Revenue', href: '/metrics/metric:revenue' },
      ],
    }), { status: 200, headers: { 'content-type': 'application/json' } })
  }) as typeof fetch)

  const results = await service.search('dash')

  expect(requestedURL).toBe('/search?q=dash')
  expect(results.map((result) => result.href)).toEqual([
    '/dashboards/dashboard:sales',
    '/models/model:orders/details',
  ])
  expect(results[0]).toMatchObject({ kind: 'Dashboard', resourceKind: 'dashboard' })
})
