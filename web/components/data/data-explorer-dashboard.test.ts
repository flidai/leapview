import { afterEach, expect, test } from 'bun:test'
import type { ExplorationSpec } from '../../generated/exploration'
import { DashboardAppendController } from './data-explorer-dashboard'

const originalFetch = globalThis.fetch
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
afterEach(() => {
  globalThis.fetch = originalFetch
  if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
  else Reflect.deleteProperty(globalThis, 'window')
})

function fixture() {
  let sequence = 0
  Object.defineProperty(globalThis, 'window', { configurable: true, value: {
    LeapViewCommand: { headers: () => ({ 'Idempotency-Key': `key-${++sequence}`, 'X-Request-ID': `request-${sequence}` }) },
  } })
  const attributes: Record<string, string> = {
    'data-dashboard-append-operation-id': 'executeDashboardAuthoringCommand',
    'data-dashboard-append-url': '/explore/add-to-dashboard',
    'data-dashboard-targets-url': '/explore/dashboard-targets',
  }
  const controller = new DashboardAppendController({ getAttribute: (name: string) => attributes[name] } as unknown as HTMLElement, () => {})
  const spec: ExplorationSpec = { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [{ field: 'revenue' }], filters: [], sort: [], limit: 100 }
  controller.syncModel(spec.modelId)
  controller.targets = [{ id: 'dashboard-sales', title: 'Sales', semanticModel: 'sales', draftId: 'draft-sales', revisionToken: 'revision-1', pages: [{ id: 'overview', title: 'Overview' }] }]
  controller.selectedDashboardID = 'dashboard-sales'
  controller.selectedPageID = 'overview'
  return { controller, spec }
}

test('lost append response retries the exact original intent even after changing model', async () => {
  const { controller, spec } = fixture()
  const requests: RequestInit[] = []
  globalThis.fetch = (async (_url, init) => {
    requests.push(init!)
    if (requests.length === 1) throw new TypeError('response lost after commit')
    return Response.json({ dashboardId: 'dashboard-sales' }, { status: 201 })
  }) as typeof fetch
  await controller.append(spec)
  expect(controller.status).toContain('not confirmed')
  controller.syncModel('another-model')
  controller.placementChoice = 'full'
  await controller.append({ ...spec, modelId: 'another-model', limit: 50 })
  expect(requests).toHaveLength(2)
  expect(requests[1]!.body).toBe(requests[0]!.body)
  const firstHeaders = new Headers(requests[0]!.headers)
  const retryHeaders = new Headers(requests[1]!.headers)
  expect(retryHeaders.get('Idempotency-Key')).toBe(firstHeaders.get('Idempotency-Key'))
  expect(retryHeaders.get('X-Request-ID')).not.toBe(firstHeaders.get('X-Request-ID'))
  expect(controller.status).toBe('Exploration added as an independent tile.')
  expect(controller.successDashboardURL).toContain('dashboard-sales/edit?draft=draft-sales')
})

test('an indeterminate protocol conflict never refreshes into a second append', async () => {
  const { controller, spec } = fixture()
  const requests: RequestInit[] = []
  globalThis.fetch = (async (_url, init) => {
    requests.push(init!)
    return Response.json({ code: 'IDEMPOTENCY_OUTCOME_UNKNOWN' }, { status: 409, headers: { 'Content-Type': 'application/problem+json' } })
  }) as typeof fetch
  await controller.append(spec)
  await controller.append(spec)
  expect(requests).toHaveLength(2)
  expect(requests.every((request) => request.method === 'POST')).toBe(true)
  expect(new Headers(requests[1]!.headers).get('Idempotency-Key')).toBe(new Headers(requests[0]!.headers).get('Idempotency-Key'))
  expect(controller.status).toContain('not confirmed')
  expect(controller.targets[0]!.revisionToken).toBe('revision-1')
})

test('a confirmed revision conflict permits a new intent using the refreshed target', async () => {
  const { controller, spec } = fixture()
  const posts: RequestInit[] = []
  globalThis.fetch = (async (_url, init) => {
    if (init?.method !== 'POST') return Response.json({ ...controller.targets[0], revisionToken: 'revision-2' })
    posts.push(init)
    if (posts.length === 1) return new Response('conflict', { status: 409 })
    return Response.json({ dashboardId: 'dashboard-sales' }, { status: 201 })
  }) as typeof fetch
  await controller.append(spec)
  expect(controller.status).toContain('target was refreshed')
  await controller.append(spec)
  expect(JSON.parse(posts[1]!.body as string).revisionToken).toBe('revision-2')
  expect(new Headers(posts[1]!.headers).get('Idempotency-Key')).not.toBe(new Headers(posts[0]!.headers).get('Idempotency-Key'))
})

test('an invalid success response retains the original key for confirmation', async () => {
  const { controller, spec } = fixture()
  const requests: RequestInit[] = []
  globalThis.fetch = (async (_url, init) => {
    requests.push(init!)
    return new Response('incomplete response', { status: 201 })
  }) as typeof fetch
  await controller.append(spec)
  await controller.append(spec)
  expect(new Headers(requests[1]!.headers).get('Idempotency-Key')).toBe(new Headers(requests[0]!.headers).get('Idempotency-Key'))
  expect(controller.status).toContain('not confirmed')
})

test('double clicks cannot send concurrent appends', async () => {
  const { controller, spec } = fixture()
  let finish!: (response: Response) => void
  let calls = 0
  globalThis.fetch = (() => { calls += 1; return new Promise<Response>((resolve) => { finish = resolve }) }) as unknown as typeof fetch
  const pending = controller.append(spec)
  await controller.append(spec)
  expect(calls).toBe(1)
  finish(Response.json({ dashboardId: 'dashboard-sales' }, { status: 201 }))
  await pending
  expect(controller.saving).toBe(false)
})
