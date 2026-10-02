import { describe, expect, test } from 'bun:test'
import validateEnvelope from '../web/generated/visualization/validate'
import { defaultRendererContext, validateEnvelopeBoundary } from '../web/components/dashboard/visualization/host-controller'
import type { VisualizationEnvelope, VisualizationWindowRequest } from '../web/generated/visualization'
import type { FeatureCollection } from 'geojson'
import { coordinateGeometry, joinGeometry, pathGeometry } from '../web/components/dashboard/visualization/adapters/maplibre/data'
import { cartesianOption } from '../web/components/dashboard/visualization/adapters/echarts/cartesian'
import { CategoryColorRegistry } from '../web/components/dashboard/visualization/adapters/echarts/category-colors'
import catalog from '../docs/visuals/catalog.json'
import { answerChartWindow, chartExamples, createChartFixture, defaultChartOptions, playgroundBrazilGeometry, type ChartOptions, type ChartScenario } from './chart-fixtures'

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

describe('local production map layer variants', () => {
  for (const mapLayer of ['point', 'heat', 'density', 'choropleth', 'path', 'reference'] as const) {
    test(`${mapLayer}: scenarios and statuses retain valid public contracts`, () => {
      const variations: Partial<ChartOptions>[] = [
        ...(['standard', 'dense', 'long-labels', 'missing', 'single', 'zero', 'negative', 'precision'] as const).map((scenario) => ({ scenario })),
        ...(['ready', 'loading', 'empty', 'error'] as const).map((status) => ({ status })),
      ]
      for (const variation of variations) {
        const { envelope } = createChartFixture('map', { ...defaultChartOptions, mapLayer, ...variation })
        expectValid(envelope)
        if (envelope.spec.kind !== 'geographic') throw new Error('Expected geographic fixture')
        expect(envelope.spec.layers[0]?.kind).toBe(mapLayer)
        expect(envelope.spec.presentation.basemap).toBeUndefined()
        expect(envelope.spec.presentation.labelDensity).toBe('hidden')
        expect(envelope.dataState.kind).toBe('inline')
      }
    })
  }
})

test('bundled choropleth asset matches its declared digest and joins every fixture state', async () => {
  const bytes = await Bun.file(new URL('../static/geometry/br-states-ibge.geojson', import.meta.url)).arrayBuffer()
  const digest = Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', bytes)), (value) => value.toString(16).padStart(2, '0')).join('')
  expect(playgroundBrazilGeometry.digest).toBe(`sha256:${digest}`)
  const geometry = JSON.parse(new TextDecoder().decode(bytes)) as FeatureCollection
  const fixture = createChartFixture('map', { ...defaultChartOptions, mapLayer: 'choropleth' })
  if (fixture.envelope.spec.kind !== 'geographic') throw new Error('Expected map')
  const layer = fixture.envelope.spec.layers[0]!
  if (layer.kind !== 'choropleth') throw new Error('Expected choropleth')
  expect(layer.geometry.url).toBe('/static/geometry/br-states-ibge.geojson')
  const joined = joinGeometry(fixture.envelope, layer, geometry)
  expect(joined.features).toHaveLength(27)
  expect(joined.features.every((feature) => feature.properties?.__lv_value != null)).toBe(true)
  expect(new Set(joined.features.map((feature) => feature.properties?.__lv_row_index)).size).toBe(fixture.rows.length)
})

test('weighted heat and density fixtures produce distinct production geometry weights', () => {
  const weights = (mapLayer: 'heat' | 'density') => {
    const { envelope } = createChartFixture('map', { ...defaultChartOptions, mapLayer })
    if (envelope.spec.kind !== 'geographic') throw new Error('Expected map')
    return coordinateGeometry(envelope, envelope.spec.layers[0]!).features.map((feature) => feature.properties?.__lv_value)
  }
  expect(weights('heat').length).toBeGreaterThan(8)
  expect(new Set(weights('heat')).size).toBeGreaterThan(1)
  expect(new Set(weights('density'))).toEqual(new Set([1]))
})

test('route fixtures translate to two ordered paths with public route selection mappings', () => {
  const { envelope } = createChartFixture('map', { ...defaultChartOptions, mapLayer: 'path' })
  if (envelope.spec.kind !== 'geographic') throw new Error('Expected map')
  const layer = envelope.spec.layers[0]!
  if (layer.kind !== 'path') throw new Error('Expected path')
  const geometry = pathGeometry(envelope, layer)
  expect(geometry.features).toHaveLength(2)
  expect(geometry.features.every((feature) => feature.geometry.type === 'LineString' && feature.geometry.coordinates.length === 4)).toBe(true)
  expect(envelope.spec.interactions[0]?.mappings[0]?.source.field).toBe('route')
  expect(new Set(geometry.features.map((feature) => feature.properties?.__lv_path))).toEqual(new Set(['East route', 'West route']))
})

test('line presentation controls reach the production ECharts translation', () => {
  for (const id of ['line', 'area', 'combo']) {
    const defaults = createChartFixture(id).envelope
    const changed = createChartFixture(id, { ...defaultChartOptions, smooth: true, step: true, symbols: false }).envelope
    expectValid(changed)
    const before = cartesianOption(defaults, defaultRendererContext, new CategoryColorRegistry())
    const after = cartesianOption(changed, defaultRendererContext, new CategoryColorRegistry())
    const originalLines = before.series.filter((series: { type: string }) => series.type === 'line')
    const updatedLines = after.series.filter((series: { type: string }) => series.type === 'line')
    expect(updatedLines.length).toBeGreaterThan(0)
    for (const series of originalLines) {
      expect(series.smooth).toBe(false)
      expect(series.step).toBe(false)
      expect(series.symbol).not.toBe('none')
    }
    for (const series of updatedLines) expect(series).toMatchObject({ smooth: true, step: 'middle', symbol: 'none' })
  }
})
