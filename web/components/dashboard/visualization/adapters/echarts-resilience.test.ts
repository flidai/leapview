import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { captureEChartsViewState, echartsNavigationDefaults, echartsOption, responsiveEChartsPatch } from './echarts'

function cartesian(dataZoom = true): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: 'sales', rendererID: 'echarts', specRevision: 'sha256:echarts-resilience', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'Sales', mark: 'line',
      datasets: [{ id: 'primary', fields: [
        { id: 'label', role: 'dimension', dataType: 'string', nullable: true, label: 'Label' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: true, label: 'Value' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' },
      accessibility: { title: 'Sales', description: 'Sales over time' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }],
      presentation: { labelPolicy: { density: 'automatic', priority: [], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, dataZoom },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:echarts-resilience', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:echarts-resilience', dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [[null, null]], completeness: 'complete' }] },
    selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

test('ECharts navigation defaults are explicit and renderer-supported only', () => {
  expect(echartsNavigationDefaults(cartesian(true))).toEqual({ dataZoom: true, roam: false })
  expect(echartsNavigationDefaults(cartesian(false))).toEqual({ dataZoom: false, roam: false })
})

test('ECharts responsive patch is deterministic and preserves stable option identity', () => {
  const option = echartsOption(cartesian(), defaultRendererContext) as Record<string, any>
  const compact = responsiveEChartsPatch(option, 320, 240)
  expect(compact.grid).toMatchObject({ left: 8, right: 8, top: 10, bottom: 12 })
  expect(option.series[0].id).toBe('series:primary:value')
  expect(responsiveEChartsPatch(option, 0, 240)).toEqual({})
})

test('ECharts view-state capture keeps supported zoom/pan fields and drops library internals', () => {
  expect(captureEChartsViewState({
    dataZoom: [{ start: 12, end: 68, ignored: 'drop' }],
    series: [{ id: 'series:hierarchy:graph', center: ['52%', '48%'], zoom: 1.25, data: ['drop'] }, { id: 'static', data: [] }],
  })).toEqual({
    dataZoom: [{ start: 12, end: 68 }],
    series: [{ id: 'series:hierarchy:graph', center: ['52%', '48%'], zoom: 1.25 }],
  })
})

test('ECharts responsive and view-state helpers fail closed on malformed renderer options', () => {
  expect(responsiveEChartsPatch({ grid: [null] } as any, 320, 240)).toEqual({ grid: [{ left: 8, right: 8, top: 10, bottom: 12 }] })
  expect(captureEChartsViewState({ dataZoom: [null], series: [null] } as any)).toEqual({})
})

test('ECharts accessibility describes empty and null data without exposing raw null text', () => {
  const empty = cartesian(false) as any
  empty.dataState.datasets[0].rows = []
  const option = echartsOption(empty, defaultRendererContext) as any
  expect(option.aria.description).toContain('No data rows are available.')
  expect(option.aria.description).not.toContain('null')
})
