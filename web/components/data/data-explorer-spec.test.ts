import { expect, test } from 'bun:test'
import type { ExplorationSpec } from '../../generated/exploration'
import { testVisualizationEnvelopes } from '../dashboard/dashboard-page-test-fixtures'
import { explorerVisualization } from './data-explorer-visualization'
import { DashboardAppendController } from './data-explorer-dashboard'
import {
  boundedExplorationLimit,
  explorationRunValidation,
  explorationSpecFromCommand,
  filterOperatorsForType,
  moveExplorationSort,
  setExplorationTime,
  setExplorationTimeRange,
  upsertExplorationSort,
} from './data-explorer-spec'

test('canonical exploration specs omit absent optional signal members', () => {
  const spec = explorationSpecFromCommand({
    spec: {
      schemaVersion: 1,
      modelId: 'semantic-model:sales',
      dimensions: [],
      metrics: [],
      filters: [],
      sort: [],
      limit: 100,
    },
    semanticModelId: 'semantic-model:sales',
    datasetId: '',
    dimensions: [],
    metrics: [],
    filters: [],
    sort: [],
    limit: 100,
    requestSeq: 0,
    resetVersion: 0,
    columnWidths: {},
  })

  expect(Object.hasOwn(spec, 'datasetId')).toBe(false)
  expect(Object.hasOwn(spec, 'time')).toBe(false)
  expect(JSON.stringify(spec)).not.toContain('"time"')
})

test('canonical exploration specs preserve typed and rich filters', () => {
  const range = {
    field: 'orders.amount',
    datasetId: 'orders',
    expression: {
      kind: 'range' as const,
      lower: { value: { kind: 'decimal' as const, value: '1.5' }, inclusive: true },
      upper: { value: { kind: 'decimal' as const, value: '9.5' }, inclusive: false },
    },
  }
  const typed = {
    field: 'orders.quantity',
    datasetId: 'orders',
    expression: {
      kind: 'comparison' as const,
      operator: 'greater_than' as const,
      value: { kind: 'integer' as const, value: '2' },
    },
  }
  const spec = explorationSpecFromCommand({
    spec: {
      schemaVersion: 1,
      modelId: 'semantic-model:sales',
      datasetId: 'orders',
      dimensions: [], metrics: [], filters: [range, typed], sort: [], limit: 100,
    },
    semanticModelId: 'semantic-model:sales', datasetId: 'orders', dimensions: [], metrics: [],
    filters: [
      { field: 'orders.amount', datasetId: 'orders', operator: 'range', values: [] },
      { field: 'orders.quantity', datasetId: 'orders', operator: 'greater_than', values: ['2'] },
    ],
    sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {},
  })

  expect(spec.filters).toEqual([range, typed])
})

test('canonical exploration filter dataset stays paired when invalid filters are dropped', () => {
  const spec = explorationSpecFromCommand({
    spec: { schemaVersion: 1, modelId: 'semantic-model:sales', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 },
    semanticModelId: 'semantic-model:sales', datasetId: 'orders', dimensions: [], metrics: [],
    filters: [
      { field: 'orders.invalid', datasetId: 'wrong', operator: 'unknown', values: [] },
      { field: 'orders.status', datasetId: 'orders', operator: 'equals', values: ['paid'] },
    ],
    sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {},
  })

  expect(spec.filters).toHaveLength(1)
  expect(spec.filters[0]?.datasetId).toBe('orders')
})

test('V1 query controls preserve sort priority, time range, and server row limits', () => {
  const base: ExplorationSpec = {
    schemaVersion: 1, modelId: 'sales', datasetId: 'orders',
    dimensions: [{ field: 'orders.status' }, { field: 'orders.created_at' }], metrics: [{ field: 'revenue' }],
    filters: [], sort: [], limit: 100,
  }
  const sorted = upsertExplorationSort(upsertExplorationSort(base, 'revenue', 'desc'), 'orders.status')
  expect(moveExplorationSort(sorted, 1, -1).sort.map((entry) => entry.field)).toEqual(['orders.status', 'revenue'])
  const timed = setExplorationTimeRange(setExplorationTime(base, 'orders.created_at', 'month'), {
    kind: 'absolute', lower: { value: { kind: 'date', value: '2026-01-01' }, inclusive: true },
  })
  expect(timed.time).toMatchObject({ field: 'orders.created_at', grain: 'month', range: { kind: 'absolute' } })
  expect(boundedExplorationLimit(-1)).toBe(1)
  expect(boundedExplorationLimit(2000)).toBe(1000)
})

test('V1 run validation and type-aware filters fail closed', () => {
  const empty: ExplorationSpec = { schemaVersion: 1, modelId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 }
  expect(explorationRunValidation(empty)).toEqual(expect.arrayContaining([
    'Choose a semantic model before running the exploration.',
    'Select at least one field or time grain before running the exploration.',
  ]))
  const unavailableTime: ExplorationSpec = {
    ...empty,
    modelId: 'sales',
    datasetId: 'orders',
    time: { field: 'shipments.created_at', grain: 'day' },
  }
  expect(explorationRunValidation(unavailableTime, [{
    id: 'shipments.created_at', label: 'Created at', kind: 'dimension', datasetId: 'shipments', type: 'timestamp', compatible: false, selected: false,
  }])).toContain('shipments.created_at is unavailable as a time field for this exploration.')
  expect(filterOperatorsForType('decimal').map((option) => option.value)).toEqual(expect.arrayContaining(['greater_than', 'less_than']))
  expect(filterOperatorsForType('string').map((option) => option.value)).not.toEqual(expect.arrayContaining(['greater_than', 'less_than']))
})

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


function paginatedChartFixture() {
  const envelope = testVisualizationEnvelopes().orders_chart
  if (envelope.spec.kind !== 'cartesian' || envelope.dataState.kind !== 'inline') throw new Error('Expected inline cartesian fixture')
  return { ...envelope, spec: envelope.spec, dataState: envelope.dataState }
}

test('Explorer chart pages keep grouped categories together and share the full result value scale', () => {
  const source = paginatedChartFixture()
  source.spec.series = { dataset: 'primary', field: 'series' }
  source.spec.datasets[0].fields.push({ id: 'series', label: 'Series', role: 'identity', dataType: 'string', nullable: false })
  source.dataState.datasets[0].columns = ['label', 'value', 'series']
  source.dataState.datasets[0].rows = Array.from({ length: 55 }, (_, index) => [
    [`Status ${index + 1}`, index === 54 ? 1000 : 10, 'A'],
    [`Status ${index + 1}`, -25, 'B'],
  ]).flat()
  const original = structuredClone(source)
  const first = explorerVisualization(source, 'Orders by status')
  const last = explorerVisualization(source, 'Orders by status', { key: first.categoryPage!.key, index: 1 })
  expect(first.categoryPage).toMatchObject({ first: 1, last: 50, total: 55, count: 2 })
  expect(last.categoryPage).toMatchObject({ first: 51, last: 55, total: 55, count: 2 })
  if (first.envelope.dataState.kind !== 'inline' || last.envelope.dataState.kind !== 'inline') throw new Error('Expected inline pages')
  const firstRows = first.envelope.dataState.datasets[0].rows
  const lastRows = last.envelope.dataState.datasets[0].rows
  expect(firstRows).toHaveLength(100)
  expect(lastRows).toHaveLength(10)
  expect([...firstRows, ...lastRows]).toEqual(source.dataState.datasets[0].rows)
  expect(first.minimumHeight).toBeGreaterThanOrEqual(50 * 32)
  expect(first.minimumHeight).toBeLessThanOrEqual(4096)
  for (const page of [first, last]) {
    if (page.envelope.spec.kind !== 'cartesian') throw new Error('Expected cartesian page')
    expect(page.envelope.spec.axes?.find((axis) => axis.id === 'primary_y')).toMatchObject({ minimum: -25, maximum: 1000 })
  }
  const refreshed = explorerVisualization({ ...source, dataRevision: 2 }, 'Orders by status', { key: first.categoryPage!.key, index: 1 })
  expect(refreshed.categoryPage?.index).toBe(0)
  expect(source).toEqual(original)
})

test('Explorer paged stacked charts use global positive and negative totals and percentage bounds', () => {
  const source = paginatedChartFixture()
  source.spec.series = { dataset: 'primary', field: 'series' }
  source.spec.datasets[0].fields.push({ id: 'series', label: 'Series', role: 'identity', dataType: 'string', nullable: false })
  source.dataState.datasets[0].columns = ['label', 'value', 'series']
  source.dataState.datasets[0].rows = Array.from({ length: 55 }, (_, index) => [
    [`Status ${index + 1}`, index === 54 ? 1000 : 10, 'A'],
    [`Status ${index + 1}`, 20, 'B'], [`Status ${index + 1}`, -3, 'C'], [`Status ${index + 1}`, -7, 'D'],
  ]).flat()
  for (const stacking of ['normal', 'percent'] as const) {
    source.spec.presentation.stacking = stacking
    const first = explorerVisualization(source, 'Stacked orders')
    const last = explorerVisualization(source, 'Stacked orders', { key: first.categoryPage!.key, index: 1 })
    for (const page of [first, last]) {
      if (page.envelope.spec.kind !== 'cartesian') throw new Error('Expected cartesian page')
      expect(page.envelope.spec.axes?.find((axis) => axis.id === 'primary_y')).toMatchObject(
        stacking === 'percent' ? { minimum: -100, maximum: 100 } : { minimum: -10, maximum: 1020 },
      )
    }
  }
})

test('Explorer chart pagination retains authored value bounds and caps grouped chart height', () => {
  const source = paginatedChartFixture()
  source.spec.series = { dataset: 'primary', field: 'series' }
  source.spec.datasets[0].fields.push({ id: 'series', label: 'Series', role: 'identity', dataType: 'string', nullable: false })
  source.dataState.datasets[0].columns = ['label', 'value', 'series']
  source.dataState.datasets[0].rows = Array.from({ length: 100 }, (_, index) =>
    Array.from({ length: 20 }, (_, series) => [`Status ${index + 1}`, series + 1, `Series ${series + 1}`]),
  ).flat()
  const automatic = explorerVisualization(source, 'Grouped orders')
  if (automatic.envelope.spec.kind !== 'cartesian') throw new Error('Expected cartesian page')
  const automaticAxis = automatic.envelope.spec.axes!.find((axis) => axis.id === 'primary_y')!
  source.spec.axes = [{ ...automaticAxis, minimum: -100, maximum: 200 }]
  const first = explorerVisualization(source, 'Grouped orders')
  expect(first.minimumHeight).toBeLessThanOrEqual(4096)
  expect(first.minimumHeight).toBeGreaterThanOrEqual(first.categoryPage!.last * 20 * 16)
  expect(first.categoryPage!.count).toBeGreaterThan(2)
  const last = explorerVisualization(source, 'Grouped orders', { key: first.categoryPage!.key, index: first.categoryPage!.count - 1 })
  for (const page of [first, last]) {
    if (page.envelope.spec.kind !== 'cartesian') throw new Error('Expected cartesian page')
    expect(page.envelope.spec.axes).toEqual(source.spec.axes)
  }
})
