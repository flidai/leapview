import { expect, test } from 'bun:test'
import type { DashboardFilterContract, DashboardFilterState, DashboardPageSignal } from '../../generated/signals'
import { dashboardExploreHref } from './explore-from-dashboard'

const page = {
  presentation: 'app', dashboardId: 'dashboard:sales', pageId: 'overview', modelId: 'semantic:sales',
  components: [{ id: 'tile', kind: 'visual', visual: 'orders' }],
} as DashboardPageSignal
const contract = {
  applicationMode: 'immediate',
  definitions: {
    state: { field: 'orders.state', dataset: 'orders' },
    hidden: { field: 'orders.hidden' },
  },
  bindings: {
    state: { key: 'state', id: 'state', filter: 'state', scope: 'page', pageID: 'overview', readerEditable: true, targets: ['overview/tile'] },
    hidden: { key: 'hidden', id: 'hidden', filter: 'hidden', scope: 'page', pageID: 'overview', readerEditable: true, targets: ['overview/other'] },
  },
} as unknown as DashboardFilterContract
const expression = { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] } as const
const state = {
  appliedControls: { state: { expression, resolvedExpression: expression }, hidden: { expression } },
  draftControls: { state: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'NY' }] } },
} as unknown as DashboardFilterState

test('dashboard handoff sends only applied, placement-scoped typed controls', () => {
  const href = dashboardExploreHref(page, page.components[0]!, contract, state)!
  const url = new URL(href, 'https://example.test')
  expect(url.pathname).toBe('/dashboards/dashboard%3Asales/pages/overview/components/tile/explore')
  expect(url.searchParams.getAll('filterBinding')).toEqual(['state'])
  expect(url.searchParams.get('returnDashboard')).toBe('dashboard:sales')
  const spec = JSON.parse(url.searchParams.get('state')!)
  expect(spec.dimensions).toEqual([])
  expect(spec.metrics).toEqual([])
  expect(spec.filters).toEqual([{ field: 'orders.state', datasetId: 'orders', expression }])
  expect(state.draftControls.state).toHaveProperty('values.0.value', 'NY')
})

test('dashboard handoff never overlays fixed predicates or another placement', () => {
  const fixed = { ...contract, bindings: { ...contract.bindings, state: { ...contract.bindings.state!, readerEditable: false } } }
  const fixedURL = new URL(dashboardExploreHref(page, page.components[0]!, fixed, state)!, 'https://example.test')
  expect(JSON.parse(fixedURL.searchParams.get('state')!).filters).toEqual([])
  expect(fixedURL.searchParams.getAll('filterBinding')).toEqual([])
  expect(dashboardExploreHref(page, { ...page.components[0]!, id: 'other' }, contract, state)).toBeUndefined()
})

test('dashboard handoff is unavailable on public/embed surfaces and without a visual identity', () => {
  expect(dashboardExploreHref({ ...page, presentation: 'public' }, page.components[0]!, contract, state)).toBeUndefined()
  expect(dashboardExploreHref({ ...page, presentation: 'embed' }, page.components[0]!, contract, state)).toBeUndefined()
  expect(dashboardExploreHref(page, { ...page.components[0]!, visual: '' }, contract, state)).toBeUndefined()
})
