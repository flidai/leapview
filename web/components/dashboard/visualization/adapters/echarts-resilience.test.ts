import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { Change, defaultRendererContext } from '../host-controller'
import { captureEChartsViewState, echartsNavigationDefaults, echartsOption, EChartsHandle, preservesEChartsViewState, responsiveEChartsPatch } from './echarts'
import { CategoryColorRegistry } from './echarts/category-colors'
import { proportionalFixture } from './echarts-test-fixtures'
import { responsiveEChartsPatchNeedsResize } from './echarts/view-state'

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

test('ECharts responsive pie cue layout keeps native full-card label packing and preserves authored radius ratios', () => {
  const envelope = proportionalFixture('donut') as any
  envelope.spec.presentation.legend = 'bottom'
  envelope.spec.conditionalFormatting = [{
    id: 'value-status', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'circle' } }], nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'square' } },
  }]
  envelope.dataState.datasets[0].rows = Array.from({ length: 8 }, (_, index) => [`Status ${index}`, index === 0 ? 96.5 : 0.5])
  const source = echartsOption(envelope, defaultRendererContext) as Record<string, any>
  const sourceSeries = source.series[0]
  const sourceRadius = [...sourceSeries.radius]
  const sourceLayout = sourceSeries.labelLayout
  expect(responsiveEChartsPatchNeedsResize(envelope)).toBe(true)

  const compact = responsiveEChartsPatch(source, 320, 240, envelope)
  const wide = responsiveEChartsPatch(source, 640, 360, envelope)
  const compactSeries = compact.series[0]
  const wideSeries = wide.series[0]
  expect(compactSeries.id).toBe(source.series[0].id)
  expect(wideSeries.id).toBe(source.series[0].id)
  expect(compactSeries.left).toBe(0)
  expect(compactSeries.right).toBe(0)
  expect(compactSeries.top).toBe(0)
  expect(compactSeries.bottom).toBe(28)
  expect(wideSeries.left).toBe(0)
  expect(wideSeries.right).toBe(0)
  expect(wideSeries.top).toBe(0)
  expect(wideSeries.bottom).toBe(28)
  expect(wideSeries.radius[1]).toBeGreaterThan(compactSeries.radius[1])
  expect(wideSeries.radius[0] / wideSeries.radius[1]).toBeCloseTo(0.54 / 0.76)
  expect(compactSeries.radius[0] / compactSeries.radius[1]).toBeCloseTo(0.54 / 0.76)
  expect(compactSeries.labelLine.length2).toBe(sourceSeries.labelLine.length2)
  expect(compactSeries.labelLayout).toBeTypeOf('function')
  expect(compactSeries.labelLayout({ labelRect: { x: 8, y: 80, width: 72, height: 24 }, labelLinePoints: [[120, 80], [120, 80], [120, 80]] })).toMatchObject({ hideOverlap: false })
  expect(source.series[0].id).toBe(sourceSeries.id)
  expect(source.series[0].radius).toEqual(sourceRadius)
  expect(source.series[0].labelLayout).toBe(sourceLayout)
})

test('ECharts responsive pie cue layout handles plain pie 0/100% radii and leaves other families unchanged', () => {
  const envelope = proportionalFixture('pie') as any
  envelope.spec.presentation.legend = 'bottom'
  envelope.spec.conditionalFormatting = [{
    id: 'value-status', target: 'series_color', field: { dataset: 'primary', field: 'value' },
    rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'circle' } }], nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'square' } },
  }]
  envelope.spec.presentation.outerRadius = 1
  const proportional = echartsOption(envelope, defaultRendererContext) as Record<string, any>
  const option = responsiveEChartsPatch(proportional, 358, 411, envelope)
  expect(option.series[0].radius[0]).toBe(0)
  expect(option.series[0].radius[1]).toBeGreaterThan(0)

  const ordinary = cartesian(false)
  const ordinaryOption = echartsOption(ordinary, defaultRendererContext) as Record<string, any>
  expect(responsiveEChartsPatch(ordinaryOption, 358, 411, ordinary)).toMatchObject({ grid: expect.anything() })
  expect(responsiveEChartsPatch(ordinaryOption, 640, 360, ordinary).series).toBeUndefined()

  const inside = structuredClone(envelope)
  inside.spec.presentation.labelPosition = 'inside'
  const insideOption = echartsOption(inside, defaultRendererContext) as Record<string, any>
  expect(responsiveEChartsPatch(insideOption, 358, 411, inside)).not.toHaveProperty('series')

  const funnel = structuredClone(envelope)
  funnel.spec.mark = 'funnel'
  const funnelOption = echartsOption(funnel, defaultRendererContext) as Record<string, any>
  expect(responsiveEChartsPatch(funnelOption, 358, 411, funnel)).not.toHaveProperty('series')
})

test('heatmap options emit a grid so compact responsive layout is applied', () => {
  const heatmap = cartesian(false) as any
  heatmap.spec.mark = 'heatmap'
  heatmap.spec.y = [heatmap.spec.y[0], { dataset: 'primary', field: 'value' }]
  const option = echartsOption(heatmap, defaultRendererContext) as Record<string, any>
  expect(option.grid).toBeDefined()
  expect(responsiveEChartsPatch(option, 320, 240).grid).toMatchObject({ bottom: 64 })
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
  const handle = new EChartsHandle({}, {}, chart as any, new CategoryColorRegistry())
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

test('ECharts handle recomputes proportional cue geometry for every size change', () => {
  const calls: Record<string, any>[] = []
  const chart = {
    on() {}, off() {}, resize() {}, dispose() {},
    setOption(option: Record<string, any>) { calls.push(option) },
    getOption() { return {} },
  }
  const envelope = proportionalFixture('donut') as any
  envelope.spec.presentation.legend = 'bottom'
  envelope.spec.conditionalFormatting = [{
    id: 'value-status', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'circle' } }], nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'square' } },
  }]
  const handle = new EChartsHandle({}, {}, chart as any, new CategoryColorRegistry())
  handle.mount(envelope, defaultRendererContext)
  handle.resize(320, 240)
  const first = calls.at(-1)!.series[0]
  const count = calls.length
  handle.resize(420, 240)
  const second = calls.at(-1)!.series[0]
  expect(calls.length).toBe(count + 1)
  expect(second.id).toBe(first.id)
  expect(second.left).toBe(0)
  expect(second.right).toBe(0)
  expect(second.radius[1]).toBeGreaterThan(first.radius[1])
})

function legendHandle() {
  const listeners = new Map<string, (params: unknown) => void>()
  const actions: Record<string, unknown>[] = []
  const events: CustomEvent[] = []
  const chart = {
    on(event: string, callback: (params: unknown) => void) { listeners.set(event, callback) },
    off() {},
    resize() {},
    dispose() {},
    getWidth: () => 640,
    getHeight: () => 360,
    setOption() { listeners.get('rendered')?.({}) },
    dispatchAction(action: Record<string, unknown>) { actions.push(action) },
  }
  const container = { dispatchEvent(event: CustomEvent) { events.push(event); return true } }
  return { actions, events, listeners, handle: new EChartsHandle(container as any, {} as any, chart as any, new CategoryColorRegistry()) }
}

test('ECharts proportional legends retain native toggles without a governed command', () => {
  const { actions, events, listeners, handle } = legendHandle()
  handle.mount(proportionalFixture('donut'), defaultRendererContext)
  listeners.get('legendselectchanged')?.({ name: 'A' })
  expect(actions).toEqual([])
  expect(events).toEqual([])
  handle.dispose()
})

test('ECharts governed proportional legends reset native hiding before dispatching selection', () => {
  const envelope = proportionalFixture('donut') as any
  envelope.spec.datasets[0].fields[0].role = 'identity'
  envelope.spec.interactions = [{
    id: 'point_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: ['details'], mappings: [{
      source: { dataset: 'primary', field: 'label' }, targetFieldID: 'orders.status', targetDatasetID: 'orders',
    }],
  }]
  const { actions, events, listeners, handle } = legendHandle()
  handle.mount(envelope, defaultRendererContext)
  listeners.get('legendselectchanged')?.({ name: 'A' })
  expect(actions).toEqual([{ type: 'legendSelect', name: 'A' }])
  expect(events).toHaveLength(1)
  expect(events[0]?.detail).toMatchObject({ sourceId: 'donut', action: 'set', toggle: true, mappings: [{ value: 'A' }] })
  handle.dispose()
})

// The adapter's browser mount requires canvas; SSR-SVG exercises the same
// ECharts action/event semantics without broadening the test harness.
test('ECharts SSR legend actions distinguish native visibility from governed selection', () => {
  const renderLegend = (envelope: VisualizationEnvelope) => {
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 640, height: 320 })
    const events: CustomEvent[] = []
    const container = { dispatchEvent(event: CustomEvent) { events.push(event); return true } }
    const handle = new EChartsHandle(container as any, {} as any, chart, new CategoryColorRegistry())
    handle.mount(envelope, defaultRendererContext)
    chart.dispatchAction({ type: 'legendToggleSelect', name: 'A' })
    const selected = (chart.getOption() as any).legend?.[0]?.selected?.A
    handle.dispose()
    return { selected, events }
  }

  const ordinary = renderLegend(proportionalFixture('donut'))
  expect(ordinary.selected).toBe(false)
  expect(ordinary.events).toHaveLength(0)

  const governed = proportionalFixture('donut') as any
  governed.spec.datasets[0].fields[0].role = 'identity'
  governed.spec.interactions = [{
    id: 'point_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: ['details'], mappings: [{
      source: { dataset: 'primary', field: 'label' }, targetFieldID: 'orders.status', targetDatasetID: 'orders',
    }],
  }]
  const selected = renderLegend(governed)
  expect(selected.selected).toBe(true)
  expect(selected.events).toHaveLength(1)
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
