import { expect, test } from 'bun:test'

import type { VisualizationEnvelope, VisualizationGeographicLayer } from '../../../../generated/visualization'
import type { FeatureCollection } from 'geojson'
import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'
import { resolveKPIState } from './kpi'
import { joinGeometry, mapLayer } from './maplibre'
import { tableSignal } from './tanstack'

const highlight = [{
  sourceVisualID: 'state-map',
  interactionID: 'point_selection',
  label: 'São Paulo',
  entries: [{ label: 'São Paulo', mappings: [{ targetFieldID: 'customers.state', targetFactID: 'orders', value: 'SP', label: 'São Paulo' }] }],
}]

test('mixed renderer targets preserve comparison frames and project one governed highlight', () => {
  const faceted = facetedEnvelope()
  const originalRows = structuredClone(faceted.dataState.kind === 'inline' ? faceted.dataState.datasets[0]!.rows : [])
  const option = echartsOption(faceted) as any
  const delivered = option.series.find((series: any) => series.name === 'delivered')
  const canceled = option.series.find((series: any) => series.name === 'canceled')
  expect(delivered.itemStyle.opacity({ dataIndex: 0 })).toBe(1)
  expect(delivered.itemStyle.opacity({ dataIndex: 1 })).toBe(0.2)
  expect(canceled.itemStyle.opacity({ dataIndex: 0 })).toBe(0.2)
  expect(faceted.dataState.kind === 'inline' ? faceted.dataState.datasets[0]!.rows : []).toEqual(originalRows)
  expect(option.aria.description).toContain('Comparison totals are unchanged')

  const table = tableSignal(tableEnvelope())
  expect(table.blocks.a.rows.map((row) => row.__lv_highlighted)).toEqual([true, false])
  expect(table.highlight).toMatchObject({ active: true })
  expect(table.highlight?.announcement).toContain('Comparison totals are unchanged')

  const kpi = resolveKPIState(kpiEnvelope(), defaultRendererContext)
  expect(kpi.current).toBe(42)
  expect(kpi.highlightActive).toBe(true)
  expect(kpi.highlightAnnouncement).toContain('Comparison total is unchanged')
})

test('MapLibre projects highlight state into governed feature properties and paint policy', () => {
  const envelope = mapEnvelope()
  const layer = envelope.spec.kind === 'geographic' ? envelope.spec.layers[0]! : undefined
  const geometry = {
    type: 'FeatureCollection',
    features: [
      { type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [] }, properties: { id: 'SP' } },
      { type: 'Feature', id: 'RJ', geometry: { type: 'Polygon', coordinates: [] }, properties: { id: 'RJ' } },
    ],
  } as FeatureCollection
  const joined = joinGeometry(envelope, layer!, geometry)
  expect(joined.features.map((feature) => feature.properties?.__lv_highlighted)).toEqual([true, false])
  expect(joined.features.every((feature) => feature.properties?.__lv_has_highlight === true)).toBe(true)
  expect(mapLayer('states', layer!).paint['fill-opacity']).toContain(0.2)
})

function baseSpec(fields: any[]) {
  return {
    title: 'Target',
    datasets: [{ id: 'primary', fields }],
    dataBudget: { maxRows: 1000, requiredCompleteness: 'complete' },
    accessibility: { title: 'Target', description: 'Cross-highlight target' },
    interactions: [],
  }
}

function inlineState(columns: string[], rows: unknown[][]) {
  return {
    kind: 'inline',
    specRevision: 'sha256:test',
    dataRevision: 1,
    generation: 1,
    datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns, rows, completeness: 'complete' }],
  }
}

function facetedEnvelope(): VisualizationEnvelope {
  const fields = [
    { id: 'state', sourceRef: 'customers.state', role: 'identity', dataType: 'string', nullable: false, label: 'State' },
    { id: 'status', sourceRef: 'orders.status', role: 'dimension', dataType: 'string', nullable: false, label: 'Status' },
    { id: 'value', sourceRef: 'order_count', role: 'metric', dataType: 'integer', nullable: false, label: 'Orders' },
  ]
  return {
    schemaVersion: 9, visualID: 'state-status', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      ...baseSpec(fields), kind: 'cartesian', mark: 'column',
      x: { dataset: 'primary', field: 'state' }, y: [{ dataset: 'primary', field: 'value' }], series: { dataset: 'primary', field: 'status' },
      presentation: {
        legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true },
        smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false,
      },
    },
    dataState: inlineState(['state', 'status', 'value'], [['SP', 'delivered', 10], ['RJ', 'canceled', 7], ['RJ', 'delivered', 4]]),
    selection: [], highlights: highlight, status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

function tableEnvelope(): VisualizationEnvelope {
  const fields = [
    { id: 'state', sourceRef: 'customers.state', role: 'identity', dataType: 'string', nullable: false, label: 'State' },
    { id: 'order_id', sourceRef: 'orders.order_id', role: 'identity', dataType: 'string', nullable: false, label: 'Order' },
  ]
  return {
    schemaVersion: 9, visualID: 'orders', rendererID: 'tanstack', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      ...baseSpec(fields), kind: 'table',
      columns: fields.map((field) => ({ field: { dataset: 'primary', field: field.id }, label: field.label, formatting: [] })),
      defaultSort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }],
      presentation: { rowHeight: 34, striped: true, showHeader: true },
    },
    dataState: inlineState(['state', 'order_id'], [['SP', 'o1'], ['RJ', 'o2']]),
    selection: [], highlights: highlight, status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

function kpiEnvelope(): VisualizationEnvelope {
  const fields = [{ id: 'value', role: 'metric', dataType: 'integer', nullable: false, label: 'Orders' }]
  return {
    schemaVersion: 9, visualID: 'orders-kpi', rendererID: 'html', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      ...baseSpec(fields), kind: 'kpi', value: { dataset: 'primary', field: 'value' },
      presentation: { mode: 'compact', delta: 'absolute', favorableDirection: 'neutral', missingComparison: 'show_unavailable', ranges: [], tone: 'ink' },
    },
    dataState: inlineState(['value'], [[42]]),
    selection: [], highlights: highlight, status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

function mapEnvelope(): VisualizationEnvelope {
  const fields = [
    { id: 'state', sourceRef: 'customers.state', role: 'identity', dataType: 'string', nullable: false, label: 'State' },
    { id: 'value', role: 'metric', dataType: 'integer', nullable: false, label: 'Orders' },
  ]
  const layer = {
    id: 'states', kind: 'choropleth', geometry: {}, join: { dataset: 'primary', field: 'state' }, value: { dataset: 'primary', field: 'value' },
    tooltip: [], position: 'below_labels', visibility: { minimumZoom: 0, maximumZoom: 24 },
    color: { kind: 'sequential', palette: 'blue', reverse: false, nullColor: '#ccc' }, stroke: { color: '#fff', width: 1, opacity: 1 }, opacity: 0.82,
  } as VisualizationGeographicLayer
  return {
    schemaVersion: 9, visualID: 'map', rendererID: 'maplibre', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      ...baseSpec(fields), kind: 'geographic', layers: [layer],
      presentation: {
        legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true },
        roam: false, theme: 'auto', labelDensity: 'normal', camera: { mode: 'fit_data', padding: 24, minimumZoom: 0, maximumZoom: 10 },
        controls: { zoom: false, reset: false, compass: false },
      },
    },
    dataState: inlineState(['state', 'value'], [['SP', 10], ['RJ', 7]]),
    selection: [], highlights: highlight, status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}
