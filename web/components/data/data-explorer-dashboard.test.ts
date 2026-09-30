import { expect, test } from 'bun:test'
import { DashboardAppendController } from './data-explorer-dashboard'

test('rows-only explorations are stopped before the dashboard append request', async () => {
  let requests = 0
  const originalFetch = globalThis.fetch
  globalThis.fetch = (() => {
    requests += 1
    return Promise.reject(new Error('unexpected dashboard append request'))
  }) as unknown as typeof fetch

  try {
    const host = { getAttribute: () => '/explore/add-to-dashboard' } as unknown as HTMLElement
    const controller = new DashboardAppendController(host, () => {})
    controller.syncModel('semantic-model:sales')
    await controller.append({
      schemaVersion: 1,
      modelId: 'semantic-model:sales',
      datasetId: 'sales_orders',
      dimensions: [{ field: 'sales_orders.category', alias: 'category' }],
      metrics: [],
      filters: [],
      sort: [],
      limit: 10,
    })

    expect(requests).toBe(0)
    expect(controller.status).toBe('Add at least one metric before adding an exploration to a dashboard.')
  } finally {
    globalThis.fetch = originalFetch
  }
})

test('dashboard append shows a safe unsupported-query message for 422 responses', async () => {
  const originalFetch = globalThis.fetch
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  let requests = 0
  globalThis.fetch = (async () => {
    requests += 1
    return { ok: false, status: 422, text: async () => 'sensitive internal compiler detail' } as unknown as Response
  }) as unknown as typeof fetch
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { LeapViewCommand: { headers: () => ({}) } } })

  try {
    const attributes: Record<string, string> = {
      'data-dashboard-append-operation-id': 'executeDashboardAuthoringCommand',
      'data-dashboard-append-url': '/explore/add-to-dashboard',
    }
    const host = { getAttribute: (name: string) => attributes[name] ?? null } as unknown as HTMLElement
    const controller = new DashboardAppendController(host, () => {})
    controller.syncModel('semantic-model:sales')
    controller.targets = [{
      id: 'dashboard:sales', title: 'Sales', semanticModel: 'semantic-model:sales',
      draftId: 'draft-sales', revisionToken: 'revision-sales', pages: [{ id: 'overview', title: 'Overview' }],
    }]
    controller.selectedDashboardID = 'dashboard:sales'
    controller.selectedPageID = 'overview'
    await controller.append({
      schemaVersion: 1,
      modelId: 'semantic-model:sales',
      datasetId: 'sales_orders',
      dimensions: [{ field: 'sales_orders.category', alias: 'category' }],
      metrics: [{ field: 'revenue', alias: 'revenue' }],
      filters: [],
      sort: [{ field: 'revenue', direction: 'desc' }],
      limit: 10,
    })

    expect(requests).toBe(1)
    expect(controller.status).toBe('The selected fields or display settings are not supported for dashboard tiles. Review the exploration and try again.')
    expect(controller.status).not.toContain('sensitive internal compiler detail')
  } finally {
    globalThis.fetch = originalFetch
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
