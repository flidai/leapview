import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../generated/visualization'
import { defaultRendererContext } from './host-controller'
import { accessibleDataStatus, accessibleVisualizationData, supportsHostDataActions, visualizationChangeAnnouncement } from './accessibility'

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
