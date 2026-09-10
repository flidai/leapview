import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../generated/visualization'
import { defaultRendererContext } from './host-controller'
import { accessibleDataStatus, accessibleVisualizationData, supportsHostDataActions, visualizationChangeAnnouncement } from './accessibility'
import { visualDataToDelimited } from '../visual-modal-actions'

function fixture(rows: unknown[][], completeness: 'complete' | 'partial' | 'truncated' | 'empty' = rows.length === 0 ? 'empty' : 'complete'): VisualizationEnvelope {
  return {
    schemaVersion: 9,
    visualID: 'orders',
    rendererID: 'echarts',
    specRevision: 'sha256:accessibility',
    dataRevision: 1,
    spec: {
      kind: 'cartesian',
      title: 'Orders',
      datasets: [{ id: 'primary', fields: [
        { id: 'label', role: 'dimension', dataType: 'string', nullable: true, label: 'Label' },
        { id: 'amount', role: 'metric', dataType: 'decimal', nullable: true, label: 'Amount' },
      ] }],
      dataBudget: { maxRows: 1000, requiredCompleteness: 'complete' },
      accessibility: { title: 'Orders', description: 'Orders by label', announceChanges: true },
      interactions: [],
      x: { dataset: 'primary', field: 'label' },
      y: [{ dataset: 'primary', field: 'amount' }],
      presentation: {
        labelPolicy: { density: 'automatic', priority: [], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true },
        dataZoom: true,
      },
    },
    dataState: {
      kind: 'inline', specRevision: 'sha256:accessibility', dataRevision: 1, generation: 1,
      datasets: [{ id: 'primary', specRevision: 'sha256:accessibility', dataRevision: 1, generation: 1, columns: ['label', 'amount'], rows, completeness }],
    },
    selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

test('accessible preview formats null values and preserves metric alignment', () => {
  const data = accessibleVisualizationData(fixture([['North', null], [null, 12]]), defaultRendererContext)

  expect(data.columns).toEqual([
    { key: 'label', label: 'Label' },
    { key: 'amount', label: 'Amount', align: 'right' },
  ])
  expect(data.rows).toEqual([
    { label: 'North', amount: '—' },
    { label: '—', amount: '12' },
  ])
  expect(data.truncated).toBe(false)
})

test('accessible preview is bounded and reports partial/truncated data truthfully', () => {
  const data = accessibleVisualizationData(fixture(Array.from({ length: 120 }, (_, index) => [`${index}`, index]), 'truncated'), defaultRendererContext, 5)

  expect(data.rows).toHaveLength(5)
  expect(data.totalRows).toBe(120)
  expect(data.truncated).toBe(true)
  expect(accessibleDataStatus(fixture(Array.from({ length: 120 }, (_, index) => [`${index}`, index]), 'truncated'), data))
    .toBe('120 rows available. Showing the first 5 rows in the accessible preview. Source data is truncated.')
})

test('accessible preview preserves a single dataset projection exactly', () => {
  const data = accessibleVisualizationData(fixture([['North', 12]]), defaultRendererContext)
  expect(data.columns).toEqual([
    { key: 'label', label: 'Label' },
    { key: 'amount', label: 'Amount', align: 'right' },
  ])
  expect(data.rows).toEqual([{ label: 'North', amount: '12' }])
})

test('accessible preview projects multiple datasets without guessing joins', () => {
  const input = fixture([['North', 12]]) as any
  input.spec.datasets = [
    { id: 'primary', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' }] },
    { id: 'comparison', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Prior' }, { id: 'period', role: 'dimension', dataType: 'string', nullable: false, label: 'Period' }] },
    { id: 'goal', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Goal' }] },
  ]
  input.dataState.datasets = [
    { id: 'comparison', columns: ['value', 'period'], rows: [[10, 'Q1']], completeness: 'partial' },
    { id: 'goal', columns: ['value'], rows: [[15]], completeness: 'complete' },
    { id: 'primary', columns: ['value'], rows: [[12]], completeness: 'complete' },
  ]
  const data = accessibleVisualizationData(input, defaultRendererContext, 10)
  expect(data.columns).toEqual([
    { key: '__dataset', label: 'Dataset' },
    { key: '["primary","value"]', label: 'primary: Value', align: 'right' },
    { key: '["comparison","value"]', label: 'comparison: Prior', align: 'right' },
    { key: '["comparison","period"]', label: 'comparison: Period' },
    { key: '["goal","value"]', label: 'goal: Goal', align: 'right' },
  ])
  expect(data.rows).toEqual([
    { __dataset: 'primary', '["primary","value"]': '12' },
    { __dataset: 'comparison', '["comparison","value"]': '10', '["comparison","period"]': 'Q1' },
    { __dataset: 'goal', '["goal","value"]': '15' },
  ])
  expect(data.totalRows).toBe(3)
  expect(data.truncated).toBe(true)
  expect(accessibleDataStatus(input, data)).toContain('Source data is partial.')
  const delimited = visualDataToDelimited(data, ',')
  expect(delimited).toContain('Dataset')
  expect(delimited).toContain('comparison: Prior')
})

test('accessible field keys remain unique when dataset and field IDs contain dots', () => {
  const input = fixture([]) as any
  input.spec.datasets = [
    { id: 'a.b', fields: [{ id: 'c', role: 'metric', dataType: 'decimal', nullable: false, label: 'First' }] },
    { id: 'a', fields: [{ id: 'b.c', role: 'metric', dataType: 'decimal', nullable: false, label: 'Second' }] },
  ]
  input.dataState.datasets = [
    { id: 'a.b', columns: ['c'], rows: [[1]], completeness: 'complete' },
    { id: 'a', columns: ['b.c'], rows: [[2]], completeness: 'complete' },
  ]
  const data = accessibleVisualizationData(input)
  expect(data.columns.map((column) => column.key)).toEqual(['__dataset', '["a.b","c"]', '["a","b.c"]'])
  expect(data.rows).toEqual([
    { __dataset: 'a.b', '["a.b","c"]': '1' },
    { __dataset: 'a', '["a","b.c"]': '2' },
  ])
})

test('accessible multi-dataset preview applies one global row budget', () => {
  const input = fixture(Array.from({ length: 60 }, (_, index) => [`Primary ${index}`, index]))
  input.spec.datasets.push({ ...input.spec.datasets[0]!, id: 'comparison' })
  if (input.dataState.kind !== 'inline') throw new Error('Expected inline fixture')
  input.dataState.datasets.push({
    ...input.dataState.datasets[0]!, id: 'comparison',
    rows: Array.from({ length: 60 }, (_, index) => [`Comparison ${index}`, index]),
  })
  const data = accessibleVisualizationData(input, defaultRendererContext, 70.8)
  expect(data.rows).toHaveLength(70)
  expect(data.totalRows).toBe(120)
  expect(data.truncated).toBe(true)
  expect(data.rows[59]).toMatchObject({ __dataset: 'primary', '["primary","label"]': 'Primary 59' })
  expect(data.rows[60]).toMatchObject({ __dataset: 'comparison', '["comparison","label"]': 'Comparison 0' })
  expect(data.rows[69]).toMatchObject({ __dataset: 'comparison', '["comparison","label"]': 'Comparison 9' })
  const exported = visualDataToDelimited(data, ',')
  expect(exported.split('\n')).toHaveLength(71)
  expect(exported).toContain('Comparison 9')
  expect(exported).not.toContain('Comparison 10')
})

test('accessible multi-dataset preview retains secondary rows when primary is empty', () => {
  const input = fixture([]) as any
  input.spec.datasets = [
    { id: 'primary', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: true, label: 'Value' }] },
    { id: 'trend', fields: [{ id: 'period', role: 'dimension', dataType: 'string', nullable: false, label: 'Period' }, { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' }] },
  ]
  input.dataState.datasets = [
    { id: 'primary', columns: ['value'], rows: [], completeness: 'empty' },
    { id: 'trend', columns: ['period', 'value'], rows: [['Jan', 4]], completeness: 'complete' },
  ]
  const data = accessibleVisualizationData(input, defaultRendererContext, 10)
  expect(data.totalRows).toBe(1)
  expect(data.rows).toEqual([{ __dataset: 'trend', '["trend","period"]': 'Jan', '["trend","value"]': '4' }])
})

test('empty previews expose no data and change announcements honor announceChanges', () => {
  const empty = fixture([])
  const data = accessibleVisualizationData(empty)
  expect(data.totalRows).toBe(0)
  expect(accessibleDataStatus(empty, data)).toBe('No data rows are available.')

  const updated = { ...fixture([['North', 2]]), dataRevision: 2, dataState: { ...fixture([['North', 2]]).dataState, dataRevision: 2 } } as VisualizationEnvelope
  expect(visualizationChangeAnnouncement(empty, updated)).toContain('Orders updated.')
  expect(visualizationChangeAnnouncement(empty, { ...updated, spec: { ...updated.spec, accessibility: { ...updated.spec.accessibility, announceChanges: false } } })).toBe('')
  const omitted = { ...updated, spec: { ...updated.spec, accessibility: { title: 'Orders', description: 'Orders by label' } } } as VisualizationEnvelope
  expect(visualizationChangeAnnouncement(empty, omitted)).toBe('')
})

test('empty partial and truncated frames announce completeness before no-data status', () => {
  const partial = fixture([], 'partial')
  const truncated = fixture([], 'truncated')
  expect(accessibleDataStatus(partial, accessibleVisualizationData(partial))).toBe('Source data is partial. No data rows are available.')
  expect(accessibleDataStatus(truncated, accessibleVisualizationData(truncated))).toBe('Source data is truncated. No data rows are available.')
})

test('host data actions are limited to inline non-tabular frames', () => {
  expect(supportsHostDataActions(fixture([]))).toBe(true)
  const tiled = { ...fixture([]), dataState: { kind: 'spatial_tiled' } } as unknown as VisualizationEnvelope
  expect(supportsHostDataActions(tiled)).toBe(false)
})

test('highlight-only changes are announced when opted in', () => {
  const previous = fixture([['North', 2]])
  const next = { ...previous, highlights: [{ sourceVisualID: 'map', interactionID: 'hover', entries: [], label: 'North' }] } as unknown as VisualizationEnvelope
  expect(visualizationChangeAnnouncement(previous, next)).toContain('1 highlight active.')
  expect(visualizationChangeAnnouncement(next, { ...next, highlights: [] })).toContain('Highlights cleared.')
})
