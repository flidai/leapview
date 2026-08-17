import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { bulletGeometry, kpiSparklinePath, resolveKPIState } from './kpi'

function envelope(current: number | null, comparison: number | null, goal: number | null): VisualizationEnvelope {
  return {
    schemaVersion: 9,
    visualID: 'revenue',
    rendererID: 'html',
    specRevision: 'sha256:kpi',
    dataRevision: 1,
    spec: {
      kind: 'kpi',
      title: 'Revenue',
      datasets: [
        { id: 'primary', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: true, label: 'Revenue', format: { kind: 'currency', currency: 'USD' } }] },
        { id: 'comparison', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: true, label: 'Prior revenue', format: { kind: 'currency', currency: 'USD' } }] },
        { id: 'goal', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: true, label: 'Target', format: { kind: 'currency', currency: 'USD' } }] },
        {
          id: 'trend',
          fields: [
            { id: 'period', role: 'dimension', dataType: 'date', nullable: false, label: 'Month' },
            { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' },
          ],
        },
      ],
      dataBudget: { maxRows: 12, requiredCompleteness: 'complete' },
      accessibility: { title: 'Revenue', description: 'Revenue against prior period and target' },
      interactions: [],
      value: { dataset: 'primary', field: 'value' },
      comparison: { field: { dataset: 'comparison', field: 'value' }, reducer: 'first', label: 'Previous' },
      goal: { field: { dataset: 'goal', field: 'value' }, reducer: 'first', label: 'Target' },
      trend: { category: { dataset: 'trend', field: 'period' }, value: { dataset: 'trend', field: 'value' } },
      presentation: {
        mode: 'progress',
        delta: 'relative',
        favorableDirection: 'increase',
        missingComparison: 'show_unavailable',
        ranges: [
          { maximum: 90, label: 'Behind', tone: 'danger' },
          { minimum: 90, maximum: 120, label: 'On track', tone: 'success' },
        ],
      },
    },
    dataState: {
      kind: 'inline',
      specRevision: 'sha256:kpi',
      dataRevision: 1,
      generation: 1,
      datasets: [
        { id: 'primary', specRevision: 'sha256:kpi', dataRevision: 1, generation: 1, columns: ['value'], rows: [[current]], completeness: 'complete' },
        { id: 'comparison', specRevision: 'sha256:kpi', dataRevision: 1, generation: 1, columns: ['value'], rows: [[comparison]], completeness: 'complete' },
        { id: 'goal', specRevision: 'sha256:kpi', dataRevision: 1, generation: 1, columns: ['value'], rows: [[goal]], completeness: 'complete' },
        {
          id: 'trend',
          specRevision: 'sha256:kpi',
          dataRevision: 1,
          generation: 1,
          columns: ['period', 'value'],
          rows: [['2026-01-01', 80], ['2026-02-01', 90], ['2026-03-01', 110]],
          completeness: 'complete',
        },
      ],
    },
    selection: [],
    status: { kind: 'ready' },
    diagnostics: [],
  }
}

test('KPI state resolves comparison, relative delta, goal, range, and compact trend', () => {
  const state = resolveKPIState(envelope(110, 100, 120), defaultRendererContext)

  expect(state.currentText).toBe('$110')
  expect(state.comparisonText).toBe('$100')
  expect(state.deltaText).toBe('+10%')
  expect(state.deltaCue).toBe('↑')
  expect(state.changeStatus).toBe('favorable')
  expect(state.goalText).toBe('$120')
  expect(state.progress).toBeCloseTo(110 / 120)
  expect(state.rangeLabel).toBe('On track')
  expect(state.rangeTone).toBe('success')
  expect(state.trend).toEqual([
    { label: '2026-01-01', value: 80 },
    { label: '2026-02-01', value: 90 },
    { label: '2026-03-01', value: 110 },
  ])
  expect(state.accessibleSummary).toContain('Current $110.00.')
  expect(state.accessibleSummary).toContain('Previous $100.00.')
  expect(state.accessibleSummary).toContain('Change +10%, favorable.')
  expect(state.accessibleSummary).toContain('Target $120.00.')
  expect(state.accessibleSummary).toContain('Status On track.')
})

test('KPI current, comparison, goal, and absolute delta share one display unit', () => {
  const input = envelope(1_234_567, 1_000_000, 2_000_000)
  if (input.spec.kind !== 'kpi') throw new Error('test fixture must be a KPI')
  input.spec.presentation.delta = 'absolute'

  const state = resolveKPIState(input, defaultRendererContext)
  expect(state.currentText).toBe('$1.23M')
  expect(state.comparisonText).toBe('$1M')
  expect(state.goalText).toBe('$2M')
  expect(state.deltaText).toBe('+$0.235M')
  expect(state.accessibleSummary).toContain('Current $1,234,567.00.')
})

test('KPI direction is author-defined rather than inferred from the sign', () => {
  const input = envelope(110, 100, 120)
  if (input.spec.kind !== 'kpi') throw new Error('test fixture must be a KPI')
  input.spec.presentation.favorableDirection = 'decrease'

  expect(resolveKPIState(input, defaultRendererContext).changeStatus).toBe('unfavorable')
})

test('KPI missing comparison remains explicit and out-of-range values stay truthful', () => {
  const state = resolveKPIState(envelope(140, null, 120), defaultRendererContext)

  expect(state.comparisonText).toBe('—')
  expect(state.deltaText).toBe('Unavailable')
  expect(state.changeStatus).toBe('unavailable')
  expect(state.progress).toBe(1)
  expect(state.rangeLabel).toBeUndefined()
  expect(state.accessibleSummary).toContain('Change Unavailable.')
  expect(state.accessibleSummary).not.toContain('Unavailable, unavailable')
  expect(state.accessibleSummary).toContain('Status out of range.')
})

test('KPI missing comparison may be intentionally hidden', () => {
  const input = envelope(140, null, 120)
  if (input.spec.kind !== 'kpi') throw new Error('test fixture must be a KPI')
  input.spec.presentation.missingComparison = 'hide'

  const state = resolveKPIState(input, defaultRendererContext)
  expect(state.comparisonText).toBeUndefined()
  expect(state.deltaText).toBeUndefined()
  expect(state.accessibleSummary).not.toContain('Previous')
})

test('KPI sparkline geometry is deterministic and handles a flat trend', () => {
  expect(kpiSparklinePath([
    { label: 'A', value: 10 },
    { label: 'B', value: 20 },
    { label: 'C', value: 15 },
  ])).toBe('M0,28 L50,0 L100,14')
  expect(kpiSparklinePath([
    { label: 'A', value: 10 },
    { label: 'B', value: 10 },
  ])).toBe('M0,14 L100,14')
})

test('KPI bullet geometry separates the value, goal, and qualitative bands', () => {
  const geometry = bulletGeometry(80, 100, [
    { maximum: 60, label: 'Behind', tone: 'danger' },
    { minimum: 60, maximum: 90, label: 'On track', tone: 'warning' },
    { minimum: 90, label: 'Ahead', tone: 'success' },
  ])

  expect(geometry.minimum).toBe(0)
  expect(geometry.maximum).toBe(110)
  expect(geometry.valuePosition).toBeCloseTo(80 / 110)
  expect(geometry.goalPosition).toBeCloseTo(100 / 110)
  expect(geometry.ranges).toEqual([
    { start: 0, end: 60 / 110, label: 'Behind', tone: 'danger' },
    { start: 60 / 110, end: 90 / 110, label: 'On track', tone: 'warning' },
    { start: 90 / 110, end: 1, label: 'Ahead', tone: 'success' },
  ])
})
