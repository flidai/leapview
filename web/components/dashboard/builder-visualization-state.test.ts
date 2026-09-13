import { expect, test } from 'bun:test'
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
