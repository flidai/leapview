import { expect, test } from 'bun:test'
import type { DashboardComponentSignal, DashboardFilterContract, DashboardFilterState, DashboardPageSignal } from '../../generated/signals'
import { dashboardExploreHref } from './explore-from-dashboard'

const page: DashboardPageSignal = {
  appearanceColor: 'blue', appearanceIcon: 'bar-chart', canvas: { width: 100, height: 100 },
  components: [{ id: 'tile', kind: 'visual', visual: 'orders', height: 1, width: 1, x: 0, y: 0, placement: { col: 1, row: 1, colSpan: 1, rowSpan: 1 } }],
  dashboardId: 'dashboard:sales', dashboardTitle: 'Sales', grid: { columns: 1, gap: 0, padding: 0, rowHeight: 1 },
  headerDetail: '', kind: 'dashboard', modelId: 'semantic:sales', modelTitle: 'Sales', pageId: 'overview', pageTitle: 'Overview', pages: [], presentation: 'app', title: 'Sales',
}

const contract: DashboardFilterContract = {
  applicationMode: 'immediate',
  definitions: {
    state: { id: 'state', label: 'State', field: 'orders.state', dataset: 'orders', valueKind: 'string', predicates: [], options: { kind: 'none', limit: 0, includeNull: false, values: [] }, timezone: '', calendar: '', weekStart: '' },
    hidden: { id: 'hidden', label: 'Hidden', field: 'orders.hidden', valueKind: 'string', predicates: [], options: { kind: 'none', limit: 0, includeNull: false, values: [] }, timezone: '', calendar: '', weekStart: '' },
  },
  bindings: {
    state: { key: 'state', id: 'state', filter: 'state', scope: 'page', pageID: 'overview', default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: ['overview/tile'], optionDependencies: [] },
    hidden: { key: 'hidden', id: 'hidden', filter: 'hidden', scope: 'page', pageID: 'overview', default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 1, targets: ['overview/other'], optionDependencies: [] },
  },
}

const state: DashboardFilterState = {
  revision: 2, dirtyBindings: [], defaultsRevision: '', draftControls: {},
  appliedControls: { state: { expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] }, resolvedExpression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] } } },
}

test('dashboard handoff preserves selected visual typed filter and safe return context', () => {
  const href = dashboardExploreHref(page, page.components[0]!, contract, state)
  expect(href).toContain('/dashboards/dashboard%3Asales/pages/overview/components/tile/explore?')
  const query = new URLSearchParams(href?.split('?')[1])
  expect(query.get('returnSurface')).toBe('dashboard')
  expect(query.get('returnDashboard')).toBe('dashboard:sales')
  expect(query.getAll('filterBinding')).toEqual(['state'])
  expect(JSON.parse(query.get('state')!)).toMatchObject({
    modelId: 'semantic:sales',
    filters: [{ field: 'orders.state', datasetId: 'orders', expression: { kind: 'set', values: [{ kind: 'string', value: 'CA' }] } }],
  })
})

test('dashboard handoff excludes filters bound to another visual', () => {
  const href = dashboardExploreHref(page, page.components[0]!, contract, state)
  expect(JSON.parse(new URLSearchParams(href?.split('?')[1]).get('state')!).filters).toHaveLength(1)
})

test('dashboard handoff does not overlay a non-editable authored predicate', () => {
  const fixedContract: DashboardFilterContract = {
    ...contract,
    bindings: {
      ...contract.bindings,
      state: { ...contract.bindings.state!, readerEditable: false },
    },
  }
  const href = dashboardExploreHref(page, page.components[0]!, fixedContract, state)
  expect(JSON.parse(new URLSearchParams(href?.split('?')[1]).get('state')!).filters).toEqual([])
})

test('dashboard handoff identifies editable same-field controls without replacing fixed predicates', () => {
  const mixedContract: DashboardFilterContract = {
    ...contract,
    bindings: {
      fixed: { ...contract.bindings.state!, key: 'fixed', id: 'fixed_region', readerEditable: false },
      state: { ...contract.bindings.state!, id: 'editable_region' },
    },
  }
  const href = dashboardExploreHref(page, page.components[0]!, mixedContract, state)
  const query = new URLSearchParams(href?.split('?')[1])
  expect(query.getAll('filterBinding')).toEqual(['editable_region'])
  expect(JSON.parse(query.get('state')!).filters[0].expression.values[0].value).toBe('CA')
})

test('dashboard handoff matches the exact page/component placement', () => {
  const repeatedVisualPage: DashboardPageSignal = {
    ...page,
    pageId: 'details',
    components: [{ ...page.components[0]!, id: 'tile' }],
  }
  const href = dashboardExploreHref(repeatedVisualPage, repeatedVisualPage.components[0]!, contract, state)
  expect(JSON.parse(new URLSearchParams(href?.split('?')[1]).get('state')!).filters).toEqual([])

  const secondPlacement: DashboardComponentSignal = { ...page.components[0]!, id: 'tile-2' }
  const secondHref = dashboardExploreHref(page, secondPlacement, contract, state)
  expect(JSON.parse(new URLSearchParams(secondHref?.split('?')[1]).get('state')!).filters).toEqual([])
})

test('dashboard handoff uses applied controls and ignores unapplied drafts', () => {
  const href = dashboardExploreHref(page, page.components[0]!, contract, {
    ...state,
    draftControls: { state: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'draft-only' }] } },
    appliedControls: { state: { expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'applied' }] }, resolvedExpression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'applied' }] } } },
  })
  expect(JSON.parse(new URLSearchParams(href?.split('?')[1]).get('state')!).filters[0].expression.values[0].value).toBe('applied')
})
