import { expect, test } from 'bun:test'
import type { TableVisualizationSpec } from '../../generated/visualization'
import { BuilderVisualizationState } from './builder-visualization-state'
import { governedBarPreviewEnvelope } from './dashboard-builder-test-fixtures'

test('late windows cannot erase a newer preview even before its first render', () => {
  const state = new BuilderVisualizationState()
  const old = { ...governedBarPreviewEnvelope('old'), servingStateID: 'draft-old', consumerIdentity: 'overview/sales-chart', filterRevision: 1 }
  const current = { ...old, servingStateID: 'draft-new', spec: { ...old.spec, title: 'Current draft' } }
  const signals = { 'sales-chart': current, 'window:draft-old:overview:1:sales-chart': old }
  const read = (serving = 'draft-new', page = 'overview', revision = 1) => state.decode(signals, serving, page, revision)
  expect(read()['sales-chart'].spec.title).toBe('Current draft')
  expect(read('draft-old')).toEqual({})
  expect(read('draft-new', 'details')).toEqual({})
  expect(read('draft-new', 'overview', 2)).toEqual({})
  expect(state.decode({}, 'draft-new', 'overview', 1)).toEqual({})
})

test('current windows overlay their base visual without mixing other contexts', () => {
  const state = new BuilderVisualizationState()
  const base = { ...governedBarPreviewEnvelope('current'), servingStateID: 'draft', consumerIdentity: 'overview/sales-chart', filterRevision: 2 }
  const window = { ...base, spec: { ...base.spec, title: 'Loaded window' } }
  const signals = { 'sales-chart': base, 'window:draft:overview:2:sales-chart': window }
  expect(state.decode(signals, 'draft', 'overview', 2)['sales-chart'].spec.title).toBe('Loaded window')
  signals['window:draft:overview:2:sales-chart'] = { ...window, filterRevision: 1 }
  expect(state.decode(signals, 'draft', 'overview', 2)['sales-chart'].spec.title).toBe('Sales by status')
})


function tableWindow(resetVersion: number, requestSeq: number, direction: 'ascending' | 'descending', blockID = 'a') {
  const base = governedBarPreviewEnvelope('table-spec')
  const sort = [{ field: { dataset: 'primary', field: 'category' }, direction }]
  const dataState = {
    kind: 'windowed' as const, specRevision: base.specRevision, dataRevision: base.dataRevision, generation: 1,
    schema: base.spec.datasets[0]!, cardinality: { kind: 'exact' as const, count: 100 },
    availableRows: 100, rowCap: 1000, chunkSize: 10, resetVersion, sort,
    blocks: { [blockID]: { id: blockID, start: requestSeq * 10, rows: [[String(requestSeq), requestSeq]], requestSeq, resetVersion, sort } },
  }
  const spec: TableVisualizationSpec = {
    kind: 'table', title: base.spec.title, datasets: base.spec.datasets,
    accessibility: base.spec.accessibility, dataBudget: base.spec.dataBudget, interactions: [],
    columns: [{ field: { dataset: 'primary', field: 'category' }, label: 'Status', width: 160, formatting: [] }],
    presentation: { rowHeight: 32, showHeader: true, striped: false },
  }
  return { ...base, spec, rendererID: 'tanstack',
    dataState: { ...base.dataState, kind: 'windowed' as const, payload: JSON.stringify(dataState) },
  }
}

test('late scrolling responses cannot roll back an accepted table sort', () => {
  const state = new BuilderVisualizationState()
  const base = tableWindow(1, 0, 'descending')
  const key = 'window:generation-7:overview:0:sales-chart'
  const signals = { 'sales-chart': base, [key]: tableWindow(2, 3, 'ascending') }
  const read = () => state.decode(signals, 'generation-7', 'overview', 0)['sales-chart']!
  const sorted = read()
  signals[key] = tableWindow(1, 2, 'descending')
  expect(read()).toBe(sorted)
  signals[key] = tableWindow(2, 4, 'ascending', 'b')
  const current = read().dataState
  if (current.kind !== 'windowed') throw new Error('Expected table rows')
  expect(current.resetVersion).toBe(2)
  expect(Object.keys(current.blocks).sort()).toEqual(['a', 'b'])
})

test('out-of-order blocks retain each independent window and its newest request', () => {
  const state = new BuilderVisualizationState()
  const key = 'window:generation-7:overview:0:sales-chart'
  const signals = { 'sales-chart': tableWindow(1, 0, 'ascending'), [key]: tableWindow(1, 4, 'ascending', 'b') }
  const read = () => state.decode(signals, 'generation-7', 'overview', 0)['sales-chart']!.dataState
  read()
  signals[key] = tableWindow(1, 3, 'ascending', 'a')
  read()
  signals[key] = tableWindow(1, 2, 'ascending', 'b')
  const result = read()
  if (result.kind !== 'windowed') throw new Error('Expected table rows')
  expect(result.blocks.a!.requestSeq).toBe(3)
  expect(result.blocks.b!.requestSeq).toBe(4)
})

test('new preview contexts and replacement base data reset the retained windows', () => {
  const state = new BuilderVisualizationState()
  const key = 'window:generation-7:overview:0:sales-chart'
  const signals = { 'sales-chart': tableWindow(1, 0, 'descending'), [key]: tableWindow(5, 4, 'ascending') }
  state.decode(signals, 'generation-7', 'overview', 0)
  const next = { ...tableWindow(0, 0, 'descending'), filterRevision: 1 }
  const result = state.decode({ ...signals, 'sales-chart': next }, 'generation-7', 'overview', 1)
  expect(result['sales-chart']!.dataState).toEqual(JSON.parse(next.dataState.payload))
  expect(state.decode({}, 'generation-7', 'overview', 1)).toEqual({})
  const original = state.decode({ 'sales-chart': signals['sales-chart'] }, 'generation-7', 'overview', 0)
  expect(original['sales-chart']!.dataState).toEqual(JSON.parse(signals['sales-chart'].dataState.payload))
})

test('competing sorts with the same reset version use request order', () => {
  const state = new BuilderVisualizationState()
  const key = 'window:generation-7:overview:0:sales-chart'
  const signals = { 'sales-chart': tableWindow(1, 0, 'descending'), [key]: tableWindow(2, 5, 'ascending') }
  const read = () => state.decode(signals, 'generation-7', 'overview', 0)['sales-chart']!
  const latest = read()
  signals[key] = tableWindow(2, 4, 'descending')
  expect(read()).toBe(latest)
})


test('metadata-only base updates do not reset accepted table ordering', () => {
  const state = new BuilderVisualizationState()
  const key = 'window:generation-7:overview:0:sales-chart'
  const signals = { 'sales-chart': tableWindow(1, 0, 'descending'), [key]: tableWindow(2, 3, 'ascending') }
  const read = () => state.decode(signals, 'generation-7', 'overview', 0)['sales-chart']!
  const sorted = read()
  signals['sales-chart'] = { ...signals['sales-chart'], status: { kind: 'loading' } }
  signals[key] = tableWindow(1, 2, 'descending')
  expect(read()).toBe(sorted)
})
