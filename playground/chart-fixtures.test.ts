import { describe, expect, test } from 'bun:test'
import validateEnvelope from '../web/generated/visualization/validate'
import { validateEnvelopeBoundary } from '../web/components/dashboard/visualization/host-controller'
import type { VisualizationEnvelope, VisualizationWindowRequest } from '../web/generated/visualization'
import catalog from '../docs/visuals/catalog.json'
import { answerChartWindow, chartExamples, createChartFixture, defaultChartOptions, type ChartOptions, type ChartScenario } from './chart-fixtures'

function expectValid(envelope: VisualizationEnvelope): void {
  expect(validateEnvelope(envelope)).toBe(true)
  expect(validateEnvelopeBoundary(envelope)).toBe(true)
}

test('playground discovers the complete canonical production visual catalog', () => {
  expect(chartExamples).toEqual(catalog.documents.map(({ source, title }) => ({ id: source, label: title })))
  expect(new Set(chartExamples.map(({ id }) => id)).size).toBe(chartExamples.length)
})

describe('production visualization envelopes', () => {
  const scenarios: ChartScenario[] = ['standard', 'dense', 'long-labels', 'missing', 'single', 'zero', 'negative', 'precision']
  for (const { id } of chartExamples) {
    test(`${id}: every data fixture satisfies generated schema and revision boundary`, () => {
      for (const scenario of scenarios) expectValid(createChartFixture(id, { ...defaultChartOptions, scenario }).envelope)
    })
    test(`${id}: ready, loading, empty and error are valid public statuses`, () => {
      for (const status of ['ready', 'loading', 'empty', 'error'] as const) {
        const fixture = createChartFixture(id, { ...defaultChartOptions, status })
        expectValid(fixture.envelope)
        expect(fixture.envelope.status.kind).toBe(status === 'empty' ? 'no_data' : status)
        if (status === 'empty') expect(fixture.rows).toHaveLength(0)
        if (status === 'error') expect(fixture.envelope.status.message).toBeTruthy()
      }
    })
    test(`${id}: supported presentation controls keep valid envelopes`, () => {
      const variants: Partial<ChartOptions>[] = [
        { legend: 'hidden', labels: 'hidden' }, { legend: 'left', labels: 'dense' },
        { legend: 'right', labels: 'always' }, { legend: 'top' },
        { axes: false }, { multiSeries: false }, { tooltip: 'value' }, { stacked: true },
        { kpiMode: 'bullet' }, { kpiMode: 'progress' },
      ]
      for (const variant of variants) expectValid(createChartFixture(id, { ...defaultChartOptions, ...variant }).envelope)
    })
  }
})

function tableFixture() { return createChartFixture('table', { ...defaultChartOptions, scenario: 'dense' }) }
function windowRequest(envelope: VisualizationEnvelope, changes: Partial<VisualizationWindowRequest> = {}): VisualizationWindowRequest {
  return {
    visualID: envelope.visualID, specRevision: envelope.specRevision, dataRevision: envelope.dataRevision,
    requestSeq: 1, resetVersion: 0, start: 0, limit: 50, blockID: 'all',
    sort: [{ field: { dataset: 'primary', field: 'category' }, direction: 'ascending' }], ...changes,
  }
}

test('table initial and jumped windows match the production three-block ring', () => {
  const fixture = tableFixture()
  expect(fixture.envelope.dataState.kind).toBe('windowed')
  if (fixture.envelope.dataState.kind !== 'windowed') return
  expect(Object.values(fixture.envelope.dataState.blocks).map((block) => block.start)).toEqual([0, 50, 100])
  const next = answerChartWindow(fixture.envelope, fixture.rows, windowRequest(fixture.envelope, { start: 200 }))
  expectValid(next)
  if (next.dataState.kind !== 'windowed') return
  expect(Object.values(next.dataState.blocks).map((block) => block.start)).toEqual([150, 200, 250])
  expect(next.dataRevision).toBe(fixture.envelope.dataRevision + 1)
  expect(Object.values(next.dataState.blocks).every((block) => block.requestSeq === 1 && block.rows.length === 50)).toBe(true)
})

test('single block requests preserve the other table blocks', () => {
  const fixture = tableFixture()
  const next = answerChartWindow(fixture.envelope, fixture.rows, windowRequest(fixture.envelope, { blockID: 'a', start: 150 }))
  expectValid(next)
  if (fixture.envelope.dataState.kind !== 'windowed' || next.dataState.kind !== 'windowed') return
  expect(next.dataState.blocks.a?.start).toBe(150)
  expect(next.dataState.blocks.b).toBe(fixture.envelope.dataState.blocks.b)
  expect(next.dataState.blocks.c).toBe(fixture.envelope.dataState.blocks.c)
})

test('table numeric sorting preserves decimal precision beyond JavaScript numbers', () => {
  const fixture = tableFixture()
  const rows = [['B', '9007199254740992.2'], ['C', '9007199254740992.125'], ['A', '9007199254740992.1']]
  const request = windowRequest(fixture.envelope, { resetVersion: 1, sort: [{ field: { dataset: 'primary', field: 'value' }, direction: 'ascending' }] })
  const ascending = answerChartWindow(fixture.envelope, rows, request)
  if (ascending.dataState.kind !== 'windowed') throw new Error('Expected windowed table')
  expect(ascending.dataState.blocks.a?.rows.map((row) => row[0])).toEqual(['A', 'C', 'B'])
  const descending = answerChartWindow(ascending, rows, { ...request, requestSeq: 2, resetVersion: 2, sort: [{ field: { dataset: 'primary', field: 'value' }, direction: 'descending' }] })
  if (descending.dataState.kind !== 'windowed') throw new Error('Expected windowed table')
  expect(descending.dataState.blocks.a?.rows.map((row) => row[0])).toEqual(['B', 'C', 'A'])
})

test('stale table identities, resets and request sequences cannot replace current windows', () => {
  const fixture = tableFixture()
  const request = windowRequest(fixture.envelope, { requestSeq: 8, resetVersion: 2 })
  const current = answerChartWindow(fixture.envelope, fixture.rows, request)
  for (const changes of [{ visualID: 'other' }, { specRevision: 'older-spec' }, { resetVersion: 1 }, { requestSeq: 7 }, { blockID: 'a', requestSeq: 7 }]) {
    expect(answerChartWindow(current, fixture.rows, { ...request, ...changes })).toBe(current)
  }
})

test('matrix and pivot separate authored metric aliases from materialized quarter fields', () => {
  for (const id of ['matrix', 'pivot']) {
    const { envelope } = createChartFixture(id)
    if (envelope.spec.kind !== 'matrix' && envelope.spec.kind !== 'pivot') throw new Error('Expected grid')
    const authored = new Set(envelope.spec.datasets[0]?.fields.map((item) => item.id))
    expect(envelope.spec.metrics.every((metric) => authored.has(metric.field))).toBe(true)
    if (envelope.dataState.kind !== 'windowed') throw new Error('Expected windowed grid')
    expect(envelope.dataState.schema.fields.filter((item) => item.grid?.metric === 'revenue').map((item) => item.id)).toEqual(['q1', 'q2', 'q3', 'q4'])
  }
})

test('map fixture requires no basemap, geometry URL, tile source or external labels', () => {
  const { envelope } = createChartFixture('map')
  if (envelope.spec.kind !== 'geographic') throw new Error('Expected map')
  expect(envelope.spec.presentation.basemap).toBeUndefined()
  expect(envelope.dataState.kind).toBe('inline')
  expect(envelope.spec.layers.every((layer) => layer.kind === 'point' && !layer.label)).toBe(true)
})
