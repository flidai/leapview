import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { Change, defaultRendererContext } from '../host-controller'
import { captureEChartsViewState, echartsNavigationDefaults, echartsOption, EChartsHandle, preservesEChartsViewState, responsiveEChartsPatch } from './echarts'

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
  expect(compact.grid).toMatchObject({ left: 8, right: 8, top: 10, bottom: 54 })
  expect(compact.dataZoom).toEqual([{ type: 'inside' }, { type: 'slider', bottom: 12 }])
  const withBottomLegend = responsiveEChartsPatch({ ...option, legend: { bottom: 0 }, dataZoom: [{ type: 'inside' }, { type: 'slider' }] }, 320, 240)
  expect(withBottomLegend.grid).toMatchObject({ bottom: 82 })
  expect(withBottomLegend.dataZoom).toEqual([{ type: 'inside' }, { type: 'slider', bottom: 28 }])
  expect(option.series[0].id).toBe('series:primary:value')
  expect(responsiveEChartsPatch(option, 0, 240)).toEqual({})
})

test('heatmap options emit a grid so compact responsive layout is applied', () => {
  const heatmap = cartesian(false) as any
  heatmap.spec.mark = 'heatmap'
  heatmap.spec.y = [heatmap.spec.y[0], { dataset: 'primary', field: 'value' }]
  const option = echartsOption(heatmap, defaultRendererContext) as Record<string, any>
  expect(option.grid).toBeDefined()
  expect(responsiveEChartsPatch(option, 320, 240).grid).toMatchObject({ bottom: 12 })
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

test('ECharts view-state preservation requires a compatible mark and enabled navigation', () => {
  const current = cartesian(true)
  expect(preservesEChartsViewState(current, structuredClone(current))).toBe(true)
  expect(preservesEChartsViewState(current, { ...structuredClone(current), spec: { ...current.spec, mark: 'bar' } } as any)).toBe(false)
  expect(preservesEChartsViewState(current, { ...structuredClone(current), spec: { ...current.spec, x: { dataset: 'primary', field: 'other' } } } as any)).toBe(false)
  expect(preservesEChartsViewState(current, { ...structuredClone(current), spec: { ...current.spec, y: [{ dataset: 'other', field: 'value' }] } } as any)).toBe(false)
  expect(preservesEChartsViewState(current, { ...structuredClone(current), spec: { ...current.spec, presentation: { ...current.spec.presentation, orientation: 'horizontal' } } } as any)).toBe(false)
  expect(preservesEChartsViewState(current, { ...structuredClone(current), spec: { ...current.spec, axes: [{ id: 'primary_y', type: 'value', scale: 'log', zero: 'exclude', inversion: 'normal', tickDensity: 'automatic', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic', dateUnit: 'automatic' }] } } as any)).toBe(false)
  expect(preservesEChartsViewState(current, cartesian(false))).toBe(false)
})

test('ECharts hierarchy view-state preservation rejects camera layout changes', () => {
  const current = cartesian(false) as any
  current.spec = {
    kind: 'hierarchy', title: 'Tree', mark: 'tree', datasets: current.spec.datasets,
    dataBudget: current.spec.dataBudget, accessibility: current.spec.accessibility, interactions: [],
    node: { dataset: 'primary', field: 'label' }, parent: { dataset: 'primary', field: 'label' },
    presentation: { ...current.spec.presentation, orientation: 'vertical', initialDepth: 1, roam: true, layout: 'standard' },
  }
  expect(preservesEChartsViewState(current, structuredClone(current))).toBe(true)
  for (const presentation of [
    { ...current.spec.presentation, orientation: 'horizontal' },
    { ...current.spec.presentation, initialDepth: 2 },
    { ...current.spec.presentation, layout: 'circular' },
    { ...current.spec.presentation, nodeGap: 24 },
  ]) {
    expect(preservesEChartsViewState(current, { ...structuredClone(current), spec: { ...current.spec, presentation } })).toBe(false)
  }
})

test('ECharts handle reapplies compact layout after updates and restores desktop margins', () => {
  let current: Record<string, any> = {}
  const calls: Record<string, any>[] = []
  const chart = {
    on() {}, off() {}, resize() {}, dispose() {},
    setOption(option: Record<string, any>) { calls.push(option); current = { ...current, ...option } },
    getOption() { return current },
  }
  const handle = new EChartsHandle({}, {}, chart as any, {} as any)
  const initial = cartesian(true)
  handle.mount(initial, defaultRendererContext)
  handle.resize(320, 240)
  const compactCall = calls.at(-1)!
  expect(compactCall.grid).toMatchObject({ bottom: 54 })

  const updated = structuredClone(initial)
  updated.dataRevision = 2
  updated.dataState.dataRevision = 2
  handle.update(updated, Change.Data, defaultRendererContext)
  expect(calls.at(-1)!.grid).toMatchObject({ bottom: 54 })

  handle.resize(640, 360)
  expect(calls.at(-1)!.grid).not.toMatchObject({ bottom: 54 })
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
