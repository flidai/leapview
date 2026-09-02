import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { Change, defaultRendererContext } from '../host-controller'
import { brushSelectionCommands, createEChartsRendererFrame, echartsOption, echartsUpdatePlan, interactionCommandForRow, legendSelectionCommand, normalizeRendererLocale, removeEChartsRendererFrame, waitForEChartsFrame } from './echarts'
import { echartsLabelPolicy, truncateVisualizationLabel } from './echarts/label-policy'
import { CategoryColorRegistry } from './echarts/category-colors'
import { proportionalCenterText } from './echarts/proportional'

test('ECharts label policy truncates by grapheme and preserves selected and threshold labels', () => {
  const envelope = cartesianFixture('heatmap', ['label', 'row', 'value']) as any
  envelope.spec.datasets[0].fields[0].role = 'identity'
  envelope.selection = [{
    datum: { dataset: 'primary', dataRevision: 1, identity: { label: 'A' } },
    label: 'A',
  }]
  envelope.spec.conditionalFormatting = [{
    id: 'threshold', target: 'label_foreground', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules',
      rules: [{ operator: 'greater_or_equal', value: 1, style: { color: 'warning', icon: 'warning' } }],
      nullStyle: { color: 'neutral' },
      defaultStyle: { color: 'neutral', icon: 'circle' },
    },
  }]
  const policy = {
    density: 'automatic', priority: ['selected', 'threshold'],
    maxCharacters: 8, minimumSpacing: 6, tooltipFallback: true,
  } as const
  const translated = echartsLabelPolicy(envelope, 'primary', policy, (params) => String(params.value?.[0] ?? ''), defaultRendererContext)

  expect(translated.label).toMatchObject({ show: true, padding: 3, overflow: 'truncate' })
  expect(translated.label.formatter({ value: ['São Paulo 😀 zone'] })).toBe('São Pau…')
  expect(translated.labelLayout({ dataIndex: 0 }).hideOverlap).toBe(false)
  expect(translated.labelLayout({ dataIndex: 99 }).hideOverlap).toBe(true)
  expect(truncateVisualizationLabel('ação 😀 norte', 7, 'pt-BR')).toBe('ação 😀…')

  const dense = echartsLabelPolicy(envelope, 'primary', { ...policy, density: 'dense', minimumSpacing: 2 }, () => 'value', defaultRendererContext)
  expect(dense.label).toMatchObject({ show: true, fontSize: 10, padding: 1 })
  const always = echartsLabelPolicy(envelope, 'primary', { ...policy, density: 'always' }, () => 'value', defaultRendererContext)
  expect(always.labelLayout).toEqual({ hideOverlap: false })
  const hidden = echartsLabelPolicy(envelope, 'primary', { ...policy, density: 'hidden' }, () => 'value', defaultRendererContext)
  expect(hidden.label.show).toBe(false)
  envelope.spec.presentation.labelPolicy = { ...policy, density: 'hidden' }
  envelope.dataState.datasets[0].rows[0][0] = 'São Paulo 😀 zone'
  const hiddenOption = echartsOption(envelope, defaultRendererContext) as any
  expect(hiddenOption.tooltip.confine).toBe(true)
  expect(hiddenOption.tooltip.formatter({ value: envelope.dataState.datasets[0].rows[0] })).toContain('São Paulo 😀 zone')
  expect(hiddenOption.aria.description).toContain('label: São Paulo 😀 zone')
})

test('ECharts renders governed bivariate points, bubbles, labels, color, and stable brushes', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'delivery', rendererID: 'echarts', specRevision: 'sha256:point', dataRevision: 4,
    spec: {
      kind: 'point', title: 'Delivery and revenue',
      datasets: [{ id: 'primary', fields: [
        { id: 'order_id', role: 'identity', dataType: 'string', nullable: false, label: 'Order' },
        { id: 'segment', role: 'dimension', dataType: 'string', nullable: false, label: 'Segment' },
        { id: 'delivery_days', role: 'metric', dataType: 'decimal', nullable: false, label: 'Delivery days' },
        { id: 'revenue', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' },
        { id: 'quantity', role: 'metric', dataType: 'decimal', nullable: false, label: 'Quantity' },
      ] }],
      dataBudget: { maxRows: 2000, requiredCompleteness: 'complete' },
      accessibility: { title: 'Delivery and revenue', description: 'Each point is one order.' },
      interactions: [{ id: 'point_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: [{ visualID: 'detail', effect: 'filter' }], mappings: [
        { source: { dataset: 'primary', field: 'order_id' }, targetFieldID: 'orders.id', targetDatasetID: 'orders' },
      ] }],
      identity: [{ dataset: 'primary', field: 'order_id' }],
      x: { dataset: 'primary', field: 'delivery_days' }, y: { dataset: 'primary', field: 'revenue' },
      size: { dataset: 'primary', field: 'quantity' }, color: { dataset: 'primary', field: 'segment' },
      label: { dataset: 'primary', field: 'order_id' },
      tooltip: [{ dataset: 'primary', field: 'segment' }, { dataset: 'primary', field: 'revenue' }],
      colorScale: { kind: 'categorical' },
      sizeScale: { minimum: 1, maximum: 5, minimumPixels: 8, maximumPixels: 32 },
      presentation: {
        legend: 'bottom', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, overplot: 'opacity', opacity: 0.55,
        largeMode: 'automatic', largeThreshold: 1000, brush: ['rectangle', 'lasso'],
      },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:point', dataRevision: 4, generation: 1, datasets: [{
      id: 'primary', specRevision: 'sha256:point', dataRevision: 4, generation: 1,
      columns: ['order_id', 'segment', 'delivery_days', 'revenue', 'quantity'],
      rows: [['o-1', 'Consumer', 2, 80, 1], ['o-2', 'Corporate', 7, 240, 5]], completeness: 'complete',
    }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.xAxis.type).toBe('value')
  expect(option.yAxis.type).toBe('value')
  expect(option.series[0]).toMatchObject({
    type: 'scatter',
    encode: { x: 'delivery_days', y: 'revenue', itemName: 'segment' },
    itemStyle: { opacity: 0.55 },
    large: false,
  })
  expect(option.series[0].symbolSize(['o-1', 'Consumer', 2, 80, 1])).toBe(8)
  expect(option.series[0].symbolSize(['o-2', 'Corporate', 7, 240, 5])).toBe(32)
  expect(option.brush.toolbox).toEqual(['rect', 'polygon'])
  expect(option.tooltip.formatter({ value: ['o-1', 'Consumer', 2, 80, 1] })).toBe('Segment: Consumer<br>Revenue: 80')

  envelope.spec.color = { dataset: 'primary', field: 'revenue' }
  envelope.spec.colorScale = { kind: 'quantitative' }
  const quantitative = echartsOption(envelope, defaultRendererContext) as any
  expect(quantitative.visualMap).toMatchObject({ type: 'continuous', dimension: 'revenue', min: 80, max: 240 })

  envelope.spec.x = { dataset: 'primary', field: 'purchase_time' }
  envelope.spec.datasets[0].fields.push({ id: 'purchase_time', role: 'temporal', dataType: 'temporal', nullable: false, label: 'Purchase time' })
  envelope.dataState.datasets[0].columns.push('purchase_time')
  envelope.dataState.datasets[0].rows[0].push(Date.UTC(2026, 0, 2))
  envelope.dataState.datasets[0].rows[1].push(Date.UTC(2026, 1, 3))
  const temporal = echartsOption(envelope, defaultRendererContext) as any
  expect(temporal.xAxis.type).toBe('time')
  expect(temporal.xAxis.splitNumber).toBe(6)
  expect(temporal.xAxis.axisLabel.hideOverlap).toBe(true)
  expect(temporal.xAxis.axisLabel.formatter(Date.UTC(2026, 0, 2))).toMatch(/2026/)
  expect(temporal.xAxis.axisLabel.formatter(Date.UTC(2026, 0, 2))).not.toContain('1767')

  expect(brushSelectionCommands(envelope, {
    batch: [{ selected: [{ dataIndex: [1, 0, 1] }] }],
  })).toEqual([
    {
      sourceKind: 'visual', sourceId: 'delivery', interactionKind: 'point_selection', action: 'replace', toggle: true,
      mappings: [{ field: 'orders.id', dataset: 'orders', value: 'o-1', label: 'o-1' }],
    },
    {
      sourceKind: 'visual', sourceId: 'delivery', interactionKind: 'point_selection', action: 'set', toggle: true,
      mappings: [{ field: 'orders.id', dataset: 'orders', value: 'o-2', label: 'o-2' }],
    },
  ])
  expect(brushSelectionCommands(envelope, { batch: [{ selected: [] }] })).toEqual([{
    sourceKind: 'visual', sourceId: 'delivery', interactionKind: 'point_selection', action: 'clear', toggle: true, mappings: [],
  }])
})

test('superseded ECharts mounts own isolated renderer frames', () => {
  const mounted: HTMLElement[] = []
  const container = {
    replaceChildren: (frame: HTMLElement) => { mounted.push(frame) },
  } as unknown as HTMLElement
  const frames: HTMLElement[] = []
  const createFrame = () => {
    const frame = { style: { cssText: '' } } as unknown as HTMLElement
    frames.push(frame)
    return frame
  }

  const stale = createEChartsRendererFrame(container, createFrame)
  const current = createEChartsRendererFrame(container, createFrame)

  expect(stale).not.toBe(current)
  expect(mounted).toEqual([stale, current])
  expect(current.style.cssText).toContain('width:100%')
  expect(current.style.cssText).toContain('height:100%')

  let staleRemoved = false
  removeEChartsRendererFrame(container, { parentNode: null, remove: () => { staleRemoved = true } } as unknown as HTMLElement)
  expect(staleRemoved).toBe(false)
})

test('ECharts translation uses dataset and encode without native option passthrough', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'revenue', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'Revenue', mark: 'line',
      datasets: [{ id: 'primary', fields: [
        { id: 'month', role: 'dimension', dataType: 'string', nullable: false, label: 'Month' },
        { id: 'revenue', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Revenue', description: 'Revenue by month' }, interactions: [],
      x: { dataset: 'primary', field: 'month' }, y: [{ dataset: 'primary', field: 'revenue' }],
      presentation: { legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: true, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [
      { id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['month', 'revenue'], rows: [['Jan', 10]], completeness: 'complete' },
    ] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  const option = echartsOption(envelope) as any
  expect(option.dataset.source).toEqual([['month', 'revenue'], ['Jan', 10]])
  expect(option.series[0].encode).toEqual({ x: 'month', y: 'revenue' })
  expect(JSON.stringify(option)).not.toContain('rendererOptions')
})

test('ECharts translates semantic axes and decision context from the current frame', () => {
  const envelope = cartesianFixture('line') as any
  envelope.spec.axes = [
    { id: 'x', title: 'Month', scale: 'automatic', zero: 'automatic', tickDensity: 'sparse' },
    { id: 'primary_y', title: 'Revenue', scale: 'linear', zero: 'exclude', minimum: 10, maximum: 100, unit: 'USD', tickDensity: 'dense' },
  ]
  envelope.spec.referenceLines = [
    { id: 'target', axis: 'primary_y', value: { kind: 'number', value: 80 }, label: 'Target', tone: 'success' },
    { id: 'average', axis: 'primary_y', value: { kind: 'field', field: { dataset: 'primary', field: 'value' }, reducer: 'mean' }, label: 'Average', tone: 'warning' },
  ]
  envelope.spec.referenceBands = [
    {
      id: 'observed', axis: 'primary_y',
      from: { kind: 'field', field: { dataset: 'primary', field: 'value' }, reducer: 'minimum' },
      to: { kind: 'field', field: { dataset: 'primary', field: 'value' }, reducer: 'maximum' },
      label: 'Observed range', tone: 'neutral',
    },
  ]
  envelope.spec.eventAnnotations = [
    { id: 'launch', axis: 'x', value: { kind: 'text', value: 'A' }, label: 'Launch', description: 'New pricing', tone: 'ink' },
  ]
  envelope.spec.tooltip = [{ dataset: 'primary', field: 'value' }]
  envelope.dataState.datasets[0].rows = [['A', 20], ['B', 60]]

  const context = { ...defaultRendererContext, colors: { ...defaultRendererContext.colors, success: '#00aa00', attention: '#ffaa00' } }
  const option = echartsOption(envelope, context) as any
  expect(option.xAxis).toMatchObject({ name: 'Month', axisLabel: { interval: 2 } })
  expect(option.yAxis).toMatchObject({ name: 'Revenue (USD)', type: 'value', min: 10, max: 100, scale: true, splitNumber: 8 })
  expect(option.series[0].markLine.data).toEqual([
    { id: 'reference-line:target', name: 'Target', yAxis: 80, lineStyle: { color: '#00aa00' } },
    { id: 'reference-line:average', name: 'Average', yAxis: 40, lineStyle: { color: '#ffaa00' } },
    { id: 'event-annotation:launch', name: 'Launch', xAxis: 'A', lineStyle: { color: context.colors.foreground } },
  ])
  expect(option.series[0].markArea.data).toEqual([[
    { id: 'reference-band:observed', name: 'Observed range', yAxis: 20, itemStyle: { color: context.colors.accent, opacity: 0.12 } },
    { yAxis: 60 },
  ]])
  expect(option.tooltip.formatter({ value: ['A', 20] })).toBe('value: 20')
  expect(option.aria.description).toBe('line. Reference line: Target. Reference line: Average. Reference band: Observed range. Event: Launch — New pricing.')

  envelope.dataRevision = 2
  envelope.dataState.dataRevision = 2
  envelope.dataState.datasets[0].dataRevision = 2
  envelope.dataState.datasets[0].rows = [['A', 40], ['B', 80]]
  const refreshed = echartsOption(envelope, context) as any
  expect(refreshed.series[0].markLine.data[1].yAxis).toBe(60)
  expect(refreshed.series[0].markArea.data[0].map((item: any) => item.yAxis)).toEqual([40, 80])
})

test('ECharts applies governed row formatting with theme colors and redundant cues', () => {
  const envelope = cartesianFixture('column') as any
  envelope.spec.presentation.labelPolicy.density = 'automatic'
  envelope.spec.presentation.labelPolicy.priority = ['anomaly', 'threshold']
  envelope.spec.conditionalFormatting = [
    {
      id: 'value-gradient', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
      rule: {
        kind: 'gradient', minimum: 0, maximum: 100,
        low: { color: 'danger' }, high: { color: 'success' }, nullStyle: { color: 'neutral' },
      },
    },
    {
      id: 'value-health', target: 'mark_stroke', field: { dataset: 'primary', field: 'value' },
      rule: {
        kind: 'rules',
        rules: [{ operator: 'less_than', value: 50, style: { color: 'danger', icon: 'arrow_down' } }],
        nullStyle: { icon: 'warning' }, defaultStyle: { color: 'success', icon: 'arrow_up' },
      },
    },
  ]
  envelope.dataState.datasets[0].rows = [['A', 25], ['B', 75]]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].itemStyle.color({ value: ['A', 25] })).toBe('rgb(162 57 48)')
  expect(option.series[0].itemStyle.color({ value: ['B', 75] })).toBe('rgb(71 104 53)')
  expect(option.series[0].itemStyle.borderColor({ value: ['A', 25] })).toBe(defaultRendererContext.colors.danger)
  expect(option.series[0].label.show).toBe(true)
  expect(option.series[0].labelLayout({ dataIndex: 0 }).hideOverlap).toBe(false)
  expect(option.series[0].label.formatter({ value: ['A', 25] })).toBe('↓ 25')
  expect(option.series[0].label.formatter({ value: ['B', 75] })).toBe('↑ 75')
})

test('ECharts translates governed heatmap gradients and waterfall rule styles', () => {
  const heatmap = cartesianFixture('heatmap', ['label', 'row', 'value']) as any
  heatmap.spec.conditionalFormatting = [{
    id: 'heat', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'gradient', minimum: 0, maximum: 100,
      low: { color: 'danger' }, high: { color: 'success' }, nullStyle: { color: 'neutral' },
    },
  }]
  const heatmapOption = echartsOption(heatmap, defaultRendererContext) as any
  expect(heatmapOption.visualMap).toMatchObject({
    min: 0,
    max: 100,
    calculable: false,
    text: ['100', '0'],
    inRange: { color: [defaultRendererContext.colors.danger, defaultRendererContext.colors.success] },
  })

  const waterfall = cartesianFixture('waterfall', ['label', 'start', 'value']) as any
  waterfall.spec.conditionalFormatting = [{
    id: 'delta', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules', rules: [{ operator: 'less_than', value: 0, style: { color: 'danger', icon: 'arrow_down' } }],
      nullStyle: { icon: 'warning' }, defaultStyle: { color: 'success', icon: 'arrow_up' },
    },
  }]
  waterfall.dataState.datasets[0].rows = [['Returns', 10, -4]]
  const waterfallOption = echartsOption(waterfall, defaultRendererContext) as any
  expect(waterfallOption.series[1].itemStyle.color({ value: ['Returns', 10, -4] })).toBe(defaultRendererContext.colors.danger)
  expect(waterfallOption.series[1].label.formatter({ value: ['Returns', 10, -4] })).toBe('↓ -4')
})

test('ECharts interactions translate stable IR field mappings without renderer row keys', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'orders', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 7,
    spec: {
      kind: 'cartesian', title: 'Orders', mark: 'bar',
      datasets: [{ id: 'primary', fields: [
        { id: 'status', role: 'identity', dataType: 'string', nullable: false, label: 'Status' },
        { id: 'count', role: 'metric', dataType: 'integer', nullable: false, label: 'Orders' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Orders by status' },
      interactions: [{ id: 'point_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: ['details'], mappings: [
        { source: { dataset: 'primary', field: 'status' }, targetFieldID: 'orders.status', targetDatasetID: 'orders', label: { dataset: 'primary', field: 'status' } },
      ] }],
      x: { dataset: 'primary', field: 'status' }, y: [{ dataset: 'primary', field: 'count' }],
      presentation: { legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 7, generation: 2, datasets: [
      { id: 'primary', specRevision: 'sha256:test', dataRevision: 7, generation: 2, columns: ['status', 'count'], rows: [['delivered', 42]], completeness: 'complete' },
    ] },
    selection: [{ datum: { dataset: 'primary', dataRevision: 7, identity: { status: 'delivered' } }, label: 'Delivered' }], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  expect(interactionCommandForRow(envelope, 'primary', ['delivered', 42])).toEqual({
    sourceKind: 'visual', sourceId: 'orders', interactionKind: 'point_selection', action: 'set', toggle: true,
    mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered', label: 'delivered' }],
  })
  expect(interactionCommandForRow(envelope, 'primary', [{ forged: true }, 42])).toBeUndefined()
  const option = echartsOption(envelope) as any
  expect(option.dataset.source).toEqual([['status', 'count', '__lv_selected'], ['delivered', 42, true]])
  expect(option.visualMap.dimension).toBe('__lv_selected')
})

test('ECharts gives selectable line and area rows reliable hit targets at either symbol setting', () => {
  for (const mark of ['line', 'area'] as const) {
    const envelope = cartesianFixture(mark) as any
    envelope.spec.datasets[0].fields[0].role = 'identity'
    envelope.spec.interactions = [{
      id: 'point_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: ['details'], mappings: [
        { source: { dataset: 'primary', field: 'label' }, targetFieldID: 'orders.purchase_month', targetDatasetID: 'orders' },
      ],
    }]

    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.series).toHaveLength(2)
    expect(option.series[0]).toMatchObject({ type: 'line', symbol: 'none' })
    expect(option.series[1]).toMatchObject({
      id: 'series:interaction-hit:primary:label:value',
      type: 'scatter',
      encode: { x: 'label', y: 'value' },
      symbolSize: 18,
      itemStyle: { color: 'rgba(0,0,0,0.001)' },
      tooltip: { show: false },
    })
    expect(option.series[1].silent).toBe(false)
  }

  const authoredSymbols = cartesianFixture('line') as any
  authoredSymbols.spec.presentation.showSymbols = true
  authoredSymbols.spec.interactions = [{
    id: 'point_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: ['details'], mappings: [
        { source: { dataset: 'primary', field: 'label' }, targetFieldID: 'orders.purchase_month', targetDatasetID: 'orders' },
    ],
  }]
  expect((echartsOption(authoredSymbols, defaultRendererContext) as any).series).toHaveLength(2)
  expect((echartsOption(cartesianFixture('line'), defaultRendererContext) as any).series).toHaveLength(1)
})

test('ECharts translation preserves combo series marks and axes', () => {
  const base = {
    schemaVersion: 9, visualID: 'combo', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'Combo', mark: 'combo',
      datasets: [{ id: 'primary', fields: [
        { id: 'month', role: 'dimension', dataType: 'string', nullable: false, label: 'Month' },
        { id: 'series', role: 'dimension', dataType: 'string', nullable: false, label: 'Series' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Combo', description: 'Combo' }, interactions: [],
      x: { dataset: 'primary', field: 'month' }, y: [{ dataset: 'primary', field: 'value' }], series: { dataset: 'primary', field: 'series' },
      presentation: { legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false, comboSeries: [
        { seriesValue: 'Revenue', mark: 'line', axis: 'primary' },
        { seriesValue: 'Orders', mark: 'column', axis: 'secondary' },
      ] },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [
      { id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['month', 'series', 'value'], rows: [['Jan', 'Revenue', 10], ['Jan', 'Orders', 2]], completeness: 'complete' },
    ] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  const option = echartsOption(base) as any
  expect(option.dataset).toHaveLength(3)
  expect(option.series.map((series: any) => [series.name, series.type, series.yAxisIndex])).toEqual([
    ['Revenue', 'line', 0], ['Orders', 'bar', 1],
  ])
  expect(option.yAxis).toHaveLength(2)
  const reordered = structuredClone(base) as any
  reordered.dataState.datasets[0].rows.reverse()
  const reorderedOption = echartsOption(reordered) as any
  expect(new Set(reorderedOption.series.map((series: any) => series.id))).toEqual(new Set(option.series.map((series: any) => series.id)))
  expect(new Set(reorderedOption.dataset.map((dataset: any) => dataset.id))).toEqual(new Set(option.dataset.map((dataset: any) => dataset.id)))
})

test('ECharts translation applies combo marks and axes to multi-measure series', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'combo-measures', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'Revenue and orders', mark: 'combo',
      datasets: [{ id: 'primary', fields: [
        { id: 'month', role: 'dimension', dataType: 'string', nullable: false, label: 'Month' },
        { id: 'revenue', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' },
        { id: 'order_count', role: 'metric', dataType: 'integer', nullable: false, label: 'Orders' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Revenue and orders', description: 'Revenue and orders' }, interactions: [],
      x: { dataset: 'primary', field: 'month' }, y: [{ dataset: 'primary', field: 'revenue' }, { dataset: 'primary', field: 'order_count' }],
      presentation: { legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false, comboSeries: [
        { seriesValue: 'revenue', mark: 'area', axis: 'primary' },
        { seriesValue: 'order_count', mark: 'column', axis: 'secondary' },
      ] },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [
      { id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['month', 'revenue', 'order_count'], rows: [['Jan', 10, 2], ['Feb', 12, 3]], completeness: 'complete' },
    ] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series.map((series: any) => [series.name, series.type, series.yAxisIndex])).toEqual([
    ['Revenue', 'line', 0], ['Orders', 'bar', 1],
  ])
  expect(option.series[0].areaStyle).toEqual({})
  expect(option.yAxis).toHaveLength(2)
  expect(option.series.map((series: any) => series.itemStyle.color)).toEqual([
    defaultRendererContext.colors.data[0],
    defaultRendererContext.colors.data[1],
  ])
  expect(option.grid.bottom).toBe(44)
})

test('ECharts normalizes stacks and preserves series order and color identity across filters', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.spec.presentation.stacking = 'percent'
  envelope.spec.presentation.seriesIntent = [
    { value: 'delivered', order: 0, color: 'success' },
    { value: 'processing', order: 1, color: 'data_3' },
    { value: 'canceled', order: 2, color: 'danger' },
  ]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series.map((series: any) => series.name)).toEqual(['delivered', 'processing'])
  expect(option.series.map((series: any) => series.itemStyle.color)).toEqual([
    defaultRendererContext.colors.success,
    defaultRendererContext.colors.data[2],
  ])
  expect(option.series.map((series: any) => series.stack)).toEqual(['percent', 'percent'])
  expect(option.series.map((series: any) => series.encode.y)).toEqual(['__lv_percent_value', '__lv_percent_value'])
  expect(option.dataset[1].source).toEqual([
    ['label', 'series', 'value', '__lv_percent_value'],
    ['Jan', 'delivered', 10, 25],
    ['Feb', 'delivered', 30, 75],
  ])
  expect(option.dataset[2].source).toEqual([
    ['label', 'series', 'value', '__lv_percent_value'],
    ['Jan', 'processing', 30, 75],
    ['Feb', 'processing', 10, 25],
  ])
  expect(option.yAxis.axisLabel.formatter(25)).toBe('25%')

  const filtered = structuredClone(envelope) as any
  filtered.dataState.datasets[0].rows = filtered.dataState.datasets[0].rows.filter((row: unknown[]) => row[1] === 'processing')
  const filteredOption = echartsOption(filtered, defaultRendererContext) as any
  expect(filteredOption.series.map((series: any) => [series.name, series.itemStyle.color])).toEqual([
    ['processing', defaultRendererContext.colors.data[2]],
  ])
  expect(filteredOption.dataset[1].source.map((row: unknown[]) => row.at(-1))).toEqual(['__lv_percent_value', 100, 100])

  delete envelope.spec.presentation.seriesIntent
  const categoryColors = new CategoryColorRegistry()
  const automatic = echartsOption(envelope, defaultRendererContext, categoryColors) as any
  const automaticProcessing = automatic.series.find((series: any) => series.name === 'processing').itemStyle.color
  delete filtered.spec.presentation.seriesIntent
  const automaticFiltered = echartsOption(filtered, defaultRendererContext, categoryColors) as any
  expect(automaticFiltered.series[0].itemStyle.color).toBe(automaticProcessing)
})

test('ECharts normalizes multi-metric percent stacks without changing raw tooltip values', () => {
  const envelope = cartesianFixture('area', ['label', 'revenue', 'cost']) as any
  envelope.spec.presentation.stacked = false
  envelope.spec.presentation.stacking = 'percent'
  envelope.spec.presentation.labelPolicy.density = 'hidden'
  envelope.spec.presentation.seriesIntent = [
    { value: 'cost', order: 0, color: 'warning' },
    { value: 'revenue', order: 1, color: 'success' },
  ]
  envelope.dataState.datasets[0].rows = [
    ['Jan', 10, 30],
    ['Feb', 30, 10],
  ]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series.map((series: any) => series.name)).toEqual(['cost', 'revenue'])
  expect(option.series.map((series: any) => series.encode.y)).toEqual(['__lv_percent_cost', '__lv_percent_revenue'])
  expect(option.series.map((series: any) => series.itemStyle.color)).toEqual([
    defaultRendererContext.colors.attention,
    defaultRendererContext.colors.success,
  ])
  expect(option.series.every((series: any) => series.label.show === false)).toBe(true)
  expect(option.series[0].label.formatter({ value: ['Jan', 10, 30, 75, 25] })).toBe('75%')
  expect(option.series[1].label.formatter({ value: ['Jan', 10, 30, 75, 25] })).toBe('25%')
  expect(option.dataset.source).toEqual([
    ['label', 'revenue', 'cost', '__lv_percent_cost', '__lv_percent_revenue'],
    ['Jan', 10, 30, 75, 25],
    ['Feb', 30, 10, 25, 75],
  ])
})

test('ECharts translation emits one multi-value financial series', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'ohlc', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'OHLC', mark: 'candlestick',
      datasets: [{ id: 'primary', fields: ['label', 'open', 'close', 'low', 'high'].map((id, index) => ({ id, role: index ? 'metric' : 'dimension', dataType: index ? 'decimal' : 'string', nullable: false, label: id })) }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'OHLC', description: 'OHLC' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: ['open', 'close', 'low', 'high'].map((field) => ({ dataset: 'primary', field })),
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: false, dataZoom: true, area: false, step: false },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [
      { id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'open', 'close', 'low', 'high'], rows: [['Jan', 1, 2, 0, 3]], completeness: 'complete' },
    ] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  const option = echartsOption(envelope) as any
  expect(option.series).toHaveLength(1)
  expect(option.xAxis.data).toEqual(['Jan'])
  expect(option.series[0].encode).toBeUndefined()
  expect(option.series[0].data).toEqual([{
    name: 'Jan', value: [1, 2, 0, 3], __lv_dataset: 'primary', __lv_row_index: 0,
  }])
})

test('ECharts renders labels inside colored cartesian marks in outlined white', () => {
  const envelope = cartesianFixture('bar') as any
  envelope.spec.presentation.dataZoom = false
  delete envelope.spec.presentation.labelPosition
  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.series[0].label.position).toBe('insideRight')
  expect(option.series[0].barMinHeight).toBe(44)
  expect(option.series[0].label).toMatchObject({ color: '#fff', textBorderColor: 'rgba(0, 0, 0, 0.55)', textBorderWidth: 2 })
  expect(option.grid.bottom).toBe(44)

  const darkContext = {
    ...defaultRendererContext,
    theme: 'dark',
    colors: { ...defaultRendererContext.colors, foreground: '#f0f6fc', surface: '#0d1117', data: ['#1f6feb'] },
  } as any
  const darkOption = echartsOption(envelope, darkContext) as any
  expect(darkOption.series[0].label).toMatchObject({
    color: '#fff',
    textBorderColor: 'rgba(255, 255, 255, 0.45)',
    textBorderWidth: 1,
  })

  const orangeDarkContext = {
    ...darkContext,
    colors: { ...darkContext.colors, data: ['#eb670f'] },
  } as any
  const orangeDarkOption = echartsOption(envelope, orangeDarkContext) as any
  expect(orangeDarkOption.series[0].label.color).toBe('#fff')

  envelope.spec.presentation.labelPosition = 'outside'
  const outsideOption = echartsOption(envelope, defaultRendererContext) as any
  expect(outsideOption.series[0].label.position).toBe('right')
  expect(outsideOption.series[0].barMinHeight).toBeUndefined()

  envelope.spec.presentation.labelPosition = 'automatic'
  envelope.spec.presentation.labelPolicy.density = 'hidden'
  const hiddenOption = echartsOption(envelope, defaultRendererContext) as any
  expect(hiddenOption.series[0].label.show).toBe(false)
  expect(hiddenOption.series[0].barMinHeight).toBeUndefined()
})

test('ECharts translation builds radar indicators and aligned series from typed fields', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'quality', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'polar', title: 'Quality', mark: 'radar',
      datasets: [{ id: 'primary', fields: [
        { id: 'metric', role: 'dimension', dataType: 'string', nullable: false, label: 'Metric' },
        { id: 'team', role: 'dimension', dataType: 'string', nullable: false, label: 'Team' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Quality', description: 'Quality by team' }, interactions: [],
      category: { dataset: 'primary', field: 'metric' }, series: { dataset: 'primary', field: 'team' }, value: { dataset: 'primary', field: 'value' },
      presentation: { legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, showPointer: false, area: true },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{
      id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['metric', 'team', 'value'],
      rows: [['Speed', 'A', 8], ['Quality', 'A', 9], ['Speed', 'B', 6], ['Quality', 'B', 7]], completeness: 'complete',
    }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
  const context = {
    ...defaultRendererContext,
    theme: 'dark' as const,
    colors: { ...defaultRendererContext.colors, muted: '#9198a1', grid: '#3d444d', surface: '#151b23' },
  }
  const option = echartsOption(envelope, context) as any
  expect(option.radar.indicator.map((item: any) => item.name)).toEqual(['Speed', 'Quality'])
  expect(option.radar.indicator.map((item: any) => item.max)).toEqual([10, 10])
  expect(option.radar).toMatchObject({
    axisLine: { lineStyle: { color: context.colors.grid } },
    splitLine: { lineStyle: { color: context.colors.grid } },
    splitArea: { areaStyle: { color: [context.colors.surface, context.colors.grid], opacity: 0.18 } },
  })
  expect(option.series[0].data).toEqual([{ name: 'A', value: [8, 9] }, { name: 'B', value: [6, 7] }])
})

test('ECharts normalizes supported document locales and fails closed on unknown locales', () => {
  expect(normalizeRendererLocale('en')).toBe('en-US')
  expect(normalizeRendererLocale('pt-BR')).toBe('pt-BR')
  expect(() => normalizeRendererLocale('da-DK')).toThrow(/unsupported visualization locale/)
})

test('ECharts uses stable IDs, contractual formatting, and resolved theme colors', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'revenue', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'Revenue', mark: 'column',
      datasets: [{ id: 'primary', fields: [
        { id: 'month', role: 'dimension', dataType: 'string', nullable: false, label: 'Month' },
        { id: 'revenue', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue', format: { kind: 'currency', currency: 'BRL', minimumFractionDigits: 2, maximumFractionDigits: 2 } },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Revenue', description: 'Revenue' }, interactions: [],
      x: { dataset: 'primary', field: 'month' }, y: [{ dataset: 'primary', field: 'revenue' }],
      presentation: { legend: 'bottom', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['month', 'revenue'], rows: [['Jan', 1234.5]], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
  const context = {
    ...defaultRendererContext,
    locale: 'pt-BR' as const,
    colors: { ...defaultRendererContext.colors, foreground: '#eee', grid: '#333', data: ['#123456'] },
  }
  const option = echartsOption(envelope, context) as any
  expect(option.series[0].id).toBe('series:primary:revenue')
  expect(option.color).toEqual(['#123456'])
  expect(option.textStyle.color).toBe('#eee')
  expect(option.yAxis.axisLabel.formatter(1234.5)).toBe('R$\u00a01,23K')
  expect(option.series[0].label.formatter({ value: ['Jan', 1234.5] })).toBe('R$\u00a01,23K')
  expect(option.tooltip.formatter({ value: ['Jan', 1234.5] })).toContain('R$\u00a01.234,50')

  if (envelope.spec.kind !== 'cartesian') throw new Error('test fixture must be cartesian')
  envelope.spec.presentation.displayUnits = 'thousands'
  envelope.spec.axes = [{ id: 'primary_y', scale: 'automatic', zero: 'automatic', displayUnits: 'millions', tickDensity: 'automatic' }]
  const overridden = echartsOption(envelope, context) as any
  expect(overridden.yAxis.axisLabel.formatter(1234.5)).toBe('R$\u00a00,00123M')
  expect(overridden.series[0].label.formatter({ value: ['Jan', 1234.5] })).toBe('R$\u00a00,00123M')
})

test('ECharts constructs deterministic nested hierarchy data and honors layout presentation', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'tree', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'hierarchy', title: 'Tree', mark: 'tree',
      datasets: [{ id: 'primary', fields: [
        { id: 'node', role: 'identity', dataType: 'string', nullable: false, label: 'Node' },
        { id: 'parent', role: 'dimension', dataType: 'string', nullable: true, label: 'Parent' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Tree', description: 'Tree' }, interactions: [],
      node: { dataset: 'primary', field: 'node' }, parent: { dataset: 'primary', field: 'parent' }, value: { dataset: 'primary', field: 'value' },
      presentation: { legend: 'hidden', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, orientation: 'horizontal', initialDepth: 2, roam: true, layout: 'standard', breadcrumb: true, nodeGap: 18, curveness: 0.4, focus: 'adjacency' },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['node', 'parent', 'value'], rows: [['root', null, 10], ['child', 'root', 4]], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].id).toBe('series:hierarchy:tree')
  expect(option.series[0].orient).toBe('LR')
  expect(option.series[0]).toMatchObject({ left: '8%', right: '25%', top: '8%', bottom: '8%' })
  expect(option.series[0].label).toMatchObject({ position: 'left', align: 'right', backgroundColor: defaultRendererContext.colors.surface })
  expect(option.series[0].leaves.label).toMatchObject({ position: 'right', align: 'left', backgroundColor: defaultRendererContext.colors.surface })
  expect(option.series[0].data).toEqual([{ name: 'root', value: 10, __lv_dataset: 'primary', __lv_row_index: 0, children: [{ name: 'child', value: 4, __lv_dataset: 'primary', __lv_row_index: 1 }] }])
})

test('ECharts hierarchy source nodes select only when their compiled identity tuple is complete', () => {
  const envelope = hierarchyFixture('treemap') as any
  envelope.spec.datasets[0].fields.push(
    { id: 'category', role: 'identity', dataType: 'string', nullable: false, label: 'Category' },
    { id: 'status', role: 'identity', dataType: 'string', nullable: true, label: 'Status' },
  )
  envelope.spec.interactions = [{
    id: 'point_selection', kind: 'select', mode: 'single', requiresStableIdentity: true, targets: ['details'], mappings: [
      { source: { dataset: 'primary', field: 'category' }, targetFieldID: 'orders.category' },
      { source: { dataset: 'primary', field: 'status' }, targetFieldID: 'orders.status' },
    ],
  }]
  envelope.dataState.datasets[0].columns = ['node', 'parent', 'value', 'category', 'status']
  envelope.dataState.datasets[0].rows = [
    ['A', null, 10, 'A', null],
    ['delivered', 'A', 4, 'A', 'delivered'],
  ]

  expect(interactionCommandForRow(envelope, 'primary', envelope.dataState.datasets[0].rows[0])).toBeUndefined()
  expect(interactionCommandForRow(envelope, 'primary', envelope.dataState.datasets[0].rows[1])).toMatchObject({
    sourceId: 'treemap', action: 'set', mappings: [
      { field: 'orders.category', value: 'A' },
      { field: 'orders.status', value: 'delivered' },
    ],
  })
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].data[0].children[0]).toMatchObject({ __lv_dataset: 'primary', __lv_row_index: 1 })
})

test('ECharts network links retain source-row selection while aggregate nodes stay silent', () => {
  const envelope = networkFixture('sankey') as any
  envelope.spec.datasets[0].fields[0].role = 'identity'
  envelope.spec.datasets[0].fields[1].role = 'identity'
  envelope.spec.interactions = [{
    id: 'point_selection', kind: 'select', mode: 'single', requiresStableIdentity: true, targets: ['details'], mappings: [
      { source: { dataset: 'primary', field: 'source' }, targetFieldID: 'orders.category' },
      { source: { dataset: 'primary', field: 'target' }, targetFieldID: 'orders.status' },
    ],
  }]

  expect(interactionCommandForRow(envelope, 'primary', ['A', 'B', 4])).toMatchObject({
    mappings: [{ field: 'orders.category', value: 'A' }, { field: 'orders.status', value: 'B' }],
  })
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].links[0]).toMatchObject({ __lv_dataset: 'primary', __lv_row_index: 0 })
  expect(option.series[0].data[0].__lv_dataset).toBeUndefined()
})

test('ECharts incremental plans commit data synchronously, preserve interaction state, and do not resend data for context changes', () => {
  const option = {
    dataset: { id: 'dataset:primary', source: [['month', 'value'], ['Jan', 10]] },
    series: [{ id: 'series:primary:value', type: 'line', encode: { x: 'month', y: 'value' }, data: [10], label: { color: '#fff' } }],
    xAxis: { type: 'category' }, yAxis: { type: 'value', axisLabel: { formatter: () => '10K' } },
    dataZoom: [{ type: 'inside' }], legend: { textStyle: { color: '#fff' } }, textStyle: { color: '#fff' },
  } as any

  const data = echartsUpdatePlan(Change.Data, option)
  expect(data.settings).toEqual({ notMerge: false, lazyUpdate: false, replaceMerge: ['dataset', 'series', 'visualMap', 'graphic'] })
  expect(data.option).toEqual({ dataset: option.dataset, series: option.series, visualMap: [], graphic: [], xAxis: option.xAxis, yAxis: option.yAxis })

  const selection = echartsUpdatePlan(Change.Selection, option)
  expect(selection.settings.replaceMerge).toEqual(['dataset', 'visualMap'])
  expect(selection.option.series).toBeUndefined()

  const context = echartsUpdatePlan(Change.Context, option)
  expect(context.settings.replaceMerge).toBeUndefined()
  expect(context.option.dataset).toBeUndefined()
  expect(context.option.series[0].data).toBeUndefined()
  expect(context.option.series[0].encode).toBeUndefined()
  expect(context.option.dataZoom).toBeUndefined()

})

test('ECharts first-frame readiness resolves on the first valid rendered frame and removes its listener', async () => {
  let listener: (() => void) | undefined
  let removed = false
  const chart = {
    on(event: string, callback: () => void) { expect(event).toBe('rendered'); listener = callback },
    off(event: string, callback: () => void) { expect(event).toBe('rendered'); removed = callback === listener },
    getWidth: () => 640,
    getHeight: () => 360,
  }
  const ready = waitForEChartsFrame(chart as any, 100)
  listener?.()
  await ready
  expect(removed).toBe(true)
})

test('ECharts first-frame readiness ignores zero-sized renders until layout is valid', async () => {
  let listener: (() => void) | undefined
  let width = 0
  let height = 0
  const chart = {
    on(_event: string, callback: () => void) { listener = callback },
    off() {},
    getWidth: () => width,
    getHeight: () => height,
  }
  let settled = false
  const ready = waitForEChartsFrame(chart as any, 100).then(() => { settled = true })
  listener?.()
  await Promise.resolve()
  expect(settled).toBe(false)

  width = 640
  height = 360
  listener?.()
  await ready
  expect(settled).toBe(true)
})

test('ECharts first-frame readiness fails closed on timeout and removes its listener', async () => {
  let listener: (() => void) | undefined
  let removed = false
  const chart = {
    on(_event: string, callback: () => void) { listener = callback },
    off(_event: string, callback: () => void) { removed = callback === listener },
    getWidth: () => 640,
    getHeight: () => 360,
  }
  await expect(waitForEChartsFrame(chart as any, 1)).rejects.toThrow(/did not complete/)
  expect(removed).toBe(true)
})

test('ECharts first-frame readiness exits cleanly when its mount is disposed', async () => {
  let listener: (() => void) | undefined
  let removed = false
  const abort = new AbortController()
  const chart = {
    on(_event: string, callback: () => void) { listener = callback },
    off(_event: string, callback: () => void) { removed = callback === listener },
    getWidth: () => 640,
    getHeight: () => 360,
  }
  const ready = waitForEChartsFrame(chart as any, 100, abort.signal)
  abort.abort()
  await ready
  expect(removed).toBe(true)
})

test('ECharts translates every cartesian mark with stable renderer-owned identities', () => {
  const expectations: Array<[string, string]> = [
    ['line', 'line'], ['area', 'line'], ['bar', 'bar'], ['column', 'bar'], ['histogram', 'bar'],
  ]
  for (const [mark, type] of expectations) {
    const option = echartsOption(cartesianFixture(mark), defaultRendererContext) as any
    expect(option.series[0].id).toMatch(/^series:/)
    expect(option.series[0].type).toBe(type)
  }
  expect((echartsOption(cartesianFixture('area')) as any).series[0].areaStyle).toEqual({})
  expect((echartsOption(cartesianFixture('bar')) as any).series[0].encode).toEqual({ x: 'value', y: 'label' })
  expect((echartsOption(cartesianFixture('bar'), defaultRendererContext) as any).series[0].itemStyle.color).toBe(defaultRendererContext.colors.data[0])

  const waterfall = echartsOption(cartesianFixture('waterfall', ['label', 'value', 'start']), defaultRendererContext) as any
  expect(waterfall.series.map((series: any) => [series.id, series.type, series.silent])).toEqual([
    ['series:waterfall:offset', 'bar', true], ['series:primary:value', 'bar', undefined],
  ])
  expect(waterfall.series[1].itemStyle.color({ value: ['Gain', 12, 0] })).toBe(defaultRendererContext.colors.success)
  expect(waterfall.series[1].itemStyle.color({ value: ['Loss', -5, 12] })).toBe(defaultRendererContext.colors.danger)
  const heatmapEnvelope = cartesianFixture('heatmap', ['label', 'row', 'value']) as any
  heatmapEnvelope.dataState.datasets[0].rows = [['A', 'R1', 1], ['B', 'R1', 3]]
  const heatmap = echartsOption(heatmapEnvelope, defaultRendererContext) as any
  expect(heatmap.series[0]).toMatchObject({ id: 'series:primary:heatmap', type: 'heatmap', encode: { x: 'label', y: 'row', value: 'value' } })
  expect(heatmap.visualMap).toMatchObject({
    min: 1,
    max: 3,
    calculable: false,
    text: ['3', '1'],
    inRange: { color: ['rgba(0, 110, 219, 0.18)', defaultRendererContext.colors.data[0]] },
  })
  expect(heatmap.visualMap.precision).toBeUndefined()
  expect(heatmap.visualMap.outOfRange).toBeUndefined()
  const boxplot = echartsOption(cartesianFixture('boxplot', ['label', 'min', 'q1', 'median', 'q3', 'max']), defaultRendererContext) as any
  expect(boxplot.xAxis.data).toEqual(['A'])
  expect(boxplot.series[0]).toMatchObject({ id: 'series:primary:boxplot', type: 'boxplot', data: [{ name: 'A', value: [1, 2, 3, 4, 5], __lv_dataset: 'primary', __lv_row_index: 0 }] })
  expect(boxplot.legend).toBeUndefined()
  expect(boxplot.grid.bottom).toBe(76)
  expect(boxplot.xAxis.axisLabel).toMatchObject({ interval: 0, rotate: 0 })
  expect(boxplot.series[0].itemStyle).toEqual({ color: 'rgba(0, 110, 219, 0.24)', borderColor: defaultRendererContext.colors.data[0], borderWidth: 2 })
  expect(boxplot.series[0].emphasis.itemStyle.color).toBe('rgba(0, 110, 219, 0.4)')

  const orderedBoxplot = cartesianFixture('boxplot', ['label', 'min', 'q1', 'median', 'q3', 'max']) as any
  orderedBoxplot.spec.presentation.dataZoom = false
  orderedBoxplot.dataState.datasets[0].rows = [
    ['Later', 10, 11, 12, 13, 14],
    ['Earlier', 1, 2, 3, 4, 5],
    ['Middle', 5, 6, 7, 8, 9],
    ['Latest', 15, 16, 17, 18, 19],
    ['Earliest', 0, 1, 2, 3, 4],
  ]
  const orderedOption = echartsOption(orderedBoxplot, defaultRendererContext) as any
  expect(orderedOption.xAxis.data).toEqual(['Earliest', 'Earlier', 'Middle', 'Later', 'Latest'])
  expect(orderedOption.xAxis.axisLabel).toMatchObject({ interval: 0, rotate: 24 })
  expect(orderedOption.grid.bottom).toBe(44)

  const incompleteBoxplot = cartesianFixture('boxplot', ['label', 'min', 'q1', 'median', 'q3', 'max']) as any
  incompleteBoxplot.dataState.datasets[0].rows[0][3] = ''
  const incompleteOption = echartsOption(incompleteBoxplot, defaultRendererContext) as any
  expect(incompleteOption.series[0].data).toEqual([])
  expect(incompleteOption.graphic[0].style.text).toBe('No complete distribution data')
})

test('ECharts honors proportional presentation and hierarchy/network layout', () => {
  const donut = echartsOption(proportionalFixture('donut'), defaultRendererContext) as any
  expect(donut.series[0]).toMatchObject({ id: 'series:primary:donut', type: 'pie', radius: ['54%', '76%'], roseType: 'radius' })
  expect(donut.graphic[0].style.text).toBe('Orders')
  const funnel = echartsOption(proportionalFixture('funnel'), defaultRendererContext) as any
  expect(funnel.series[0]).toMatchObject({ id: 'series:primary:funnel', type: 'funnel', funnelAlign: 'left', sort: 'ascending', orient: 'vertical', left: '6%', right: '44%' })

  const graph = echartsOption(networkFixture('graph'), defaultRendererContext) as any
  expect(graph.series[0]).toMatchObject({ id: 'series:hierarchy:graph', type: 'graph', layout: 'circular', roam: true, left: '8%', right: '8%', top: '8%', bottom: '8%', symbolSize: 16, center: ['50%', '52%'], zoom: 0.76, label: { position: 'right', distance: 8, fontSize: 13 }, labelLayout: { moveOverlap: 'shiftY' }, itemStyle: { borderColor: defaultRendererContext.colors.surface, borderWidth: 2 }, emphasis: { focus: 'adjacency' } })
  expect(graph.series[0]).not.toHaveProperty('force')
  expect(graph.series[0].links[0]).toMatchObject({ source: 'A', target: 'B', __lv_dataset: 'primary', __lv_row_index: 0 })
  const layeredGraphEnvelope = networkFixture('graph') as any
  layeredGraphEnvelope.spec.presentation.layout = 'standard'
  const layeredGraph = echartsOption(layeredGraphEnvelope, defaultRendererContext) as any
  expect(layeredGraph.series[0]).toMatchObject({ layout: 'none', left: '30%', right: '30%', top: '12%', bottom: '12%' })
  expect(layeredGraph.series[0].data).toEqual([
    { name: 'A', x: 0, y: 50, label: { position: 'left', align: 'right' } },
    { name: 'B', x: 100, y: 50, label: { position: 'right', align: 'left' } },
  ])
  const sankeyEnvelope = networkFixture('sankey') as any
  sankeyEnvelope.spec.presentation.orientation = 'horizontal'
  sankeyEnvelope.dataState.datasets[0].rows = [['Same', 'Same', 4], ['', 'Target', 2], ['Source', 'Target', 0]]
  const sankey = echartsOption(sankeyEnvelope, defaultRendererContext) as any
  expect(sankey.series[0]).toMatchObject({ id: 'series:hierarchy:sankey', type: 'sankey', orient: 'horizontal', nodeGap: 18 })
  expect(sankey.series[0].lineStyle).toMatchObject({ color: 'gradient', opacity: 0.45 })
  expect(sankey.series[0]).toMatchObject({ left: '4%', right: '30%', top: '8%', bottom: '8%', label: { width: 96 } })
  expect(sankey.series[0].links).toEqual([{ source: 'source:Same', target: 'target:Same', sourceLabel: 'Same', targetLabel: 'Same', value: 4, __lv_dataset: 'primary', __lv_row_index: 0 }])
  expect(sankey.series[0].data).toEqual([{ name: 'source:Same', displayName: 'Same' }, { name: 'target:Same', displayName: 'Same' }])
  expect(sankey.series[0].label.formatter({ data: sankey.series[0].data[0] })).toBe('Same')
  expect(sankey.series[0].tooltip.formatter({ data: sankey.series[0].links[0] })).toBe('Same → Same: 4')

  delete sankeyEnvelope.spec.presentation.nodeGap
  delete sankeyEnvelope.spec.presentation.curveness
  const defaultLayoutSankey = echartsOption(sankeyEnvelope, defaultRendererContext) as any
  expect(defaultLayoutSankey.series[0]).not.toHaveProperty('nodeGap')
  expect(defaultLayoutSankey.series[0].lineStyle).not.toHaveProperty('curveness')

  sankeyEnvelope.dataState.datasets[0].rows = [['', 'Target', 2]]
  const emptySankey = echartsOption(sankeyEnvelope, defaultRendererContext) as any
  expect(emptySankey.series[0].links).toEqual([])
  expect(emptySankey.graphic[0].style.text).toBe('No flow data')

  for (const mark of ['treemap', 'sunburst'] as const) {
    const envelope = hierarchyFixture(mark)
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.series[0].id).toBe(`series:hierarchy:${mark}`)
    expect(option.series[0].data[0].children[0].name).toBe('child')
    if (mark === 'sunburst') {
      expect(option.series[0].radius).toEqual(['10%', '92%'])
      expect(option.series[0].label).toMatchObject({
        position: 'inside',
        rotate: 'radial',
        width: 68,
        overflow: 'truncate',
        ellipsis: '...',
        fontSize: 10,
        fontWeight: 600,
        lineHeight: 13,
        textBorderColor: defaultRendererContext.colors.surface,
        textBorderWidth: 2,
      })
      expect(option.series[0].labelLayout).toEqual({ hideOverlap: false })
    } else {
      expect(option.series[0].label).toMatchObject({ color: '#fff', textBorderColor: 'rgba(0, 0, 0, 0.55)', textBorderWidth: 2 })
      const darkOption = echartsOption(envelope, {
        ...defaultRendererContext,
        theme: 'dark',
        colors: { ...defaultRendererContext.colors, foreground: '#f0f6fc', surface: '#0d1117' },
      }) as any
      expect(darkOption.series[0].label).toMatchObject({
        color: '#fff',
        textBorderColor: 'rgba(255, 255, 255, 0.45)',
        textBorderWidth: 1,
      })
    }
  }
})

test('ECharts gives donuts legible renderer defaults without changing their categories', () => {
  const envelope = proportionalFixture('donut') as any
  envelope.spec.presentation.centerLabel = undefined
  envelope.spec.presentation.legend = 'bottom'
  envelope.dataState.datasets[0].rows = Array.from({ length: 18 }, (_, index) => [`Category ${index + 1}`, index + 1])

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.dataset.source).toHaveLength(19)
  expect(option.legend).toMatchObject({ type: 'scroll', orient: 'horizontal', bottom: 0 })
  expect(option.series[0].itemStyle.borderColor).toBeUndefined()
  expect(option.series[0].itemStyle.borderWidth).toBeUndefined()
  expect(option.series[0].label).toMatchObject({
    position: 'outside',
    alignTo: 'edge',
    edgeDistance: 8,
    distanceToLabelLine: 4,
  })
  expect(option.series[0].label.formatter({ value: ['Category 1', 1] })).toBe('Category 1: 1')
  expect(option.series[0].labelLine.show).toBe(true)
  expect(option.series[0].minShowLabelAngle).toBe(3)
  expect(option.graphic[0].style.text).toBe('Total\n171')
  expect(option.aria).toMatchObject({ enabled: true, decal: { show: true } })
  expect(option.aria.description).toContain('donut')
  expect(proportionalCenterText(envelope, defaultRendererContext, ['Category 1', 1])).toBe('Category 1\n1')

  envelope.dataState.datasets[0].rows = envelope.dataState.datasets[0].rows.slice(0, 2)
  const filtered = echartsOption(envelope, defaultRendererContext) as any
  const update = echartsUpdatePlan(Change.Data, filtered)
  expect(update.option.graphic[0].style.text).toBe('Total\n3')
  expect(update.option.aria.decal.show).toBe(false)
})

test('ECharts preserves proportional category colors when filtering changes row order', () => {
  const envelope = proportionalFixture('donut') as any
  envelope.spec.datasets[0].fields[0].sourceRef = 'orders.status'
  envelope.dataState.datasets[0].rows = [['delivered', 90], ['shipped', 8], ['canceled', 2]]

  const categoryColors = new CategoryColorRegistry()
  const initial = echartsOption(envelope, defaultRendererContext, categoryColors) as any
  const initialColor = initial.series[0].itemStyle.color
  const delivered = initialColor({ value: ['delivered', 90] })
  const shipped = initialColor({ value: ['shipped', 8] })
  expect(delivered).not.toBe(shipped)

  const filtered = structuredClone(envelope)
  filtered.dataState.datasets[0].rows = [['shipped', 8], ['canceled', 2]]
  const filteredColor = (echartsOption(filtered, defaultRendererContext, categoryColors) as any).series[0].itemStyle.color
  expect(filteredColor({ value: ['shipped', 8] })).toBe(shipped)

  const sibling = structuredClone(filtered)
  sibling.visualID = 'orders-by-status-sibling'
  const siblingColor = (echartsOption(sibling, defaultRendererContext, categoryColors) as any).series[0].itemStyle.color
  expect(siblingColor({ value: ['shipped', 8] })).toBe(shipped)

  const reordered = structuredClone(envelope)
  reordered.dataState.datasets[0].rows.reverse()
  const reorderedColor = (echartsOption(reordered, defaultRendererContext, categoryColors) as any).series[0].itemStyle.color
  expect(reorderedColor({ value: ['delivered', 90] })).toBe(delivered)
  expect(reorderedColor({ value: ['shipped', 8] })).toBe(shipped)
  expect(new Set(defaultRendererContext.colors.data).size).toBe(17)
})

test('ECharts translates proportional legend categories into governed selections', () => {
  const envelope = proportionalFixture('donut') as any
  envelope.spec.datasets[0].fields[0].role = 'identity'
  envelope.spec.interactions = [{
    id: 'point_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: ['details'], mappings: [{
      source: { dataset: 'primary', field: 'label' }, targetFieldID: 'orders.status', targetDatasetID: 'orders',
    }],
  }]

  expect(legendSelectionCommand(envelope, 'A')).toEqual({
    sourceKind: 'visual', sourceId: 'donut', interactionKind: 'point_selection', action: 'set', toggle: true,
    mappings: [{ field: 'orders.status', dataset: 'orders', value: 'A', label: 'A' }],
  })
  expect(legendSelectionCommand(envelope, 'missing')).toBeUndefined()
})

test('ECharts wraps a hierarchy forest so every tree root is rendered', () => {
  const envelope = hierarchyFixture('tree') as any
  envelope.dataState.datasets[0].rows = [['A', null, 10], ['A child', 'A', 4], ['B', null, 8], ['B child', 'B', 3]]
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].data).toHaveLength(1)
  expect(option.series[0].data[0]).toMatchObject({
    name: 'All',
    children: [{ name: 'A' }, { name: 'B' }],
  })
  expect(option.series[0].data[0].__lv_synthetic).toBe(true)
})

test('ECharts scopes repeated hierarchy labels to their compiled parent path', () => {
  const envelope = hierarchyFixture('tree') as any
  envelope.dataState.datasets[0].rows = [
    ['Books', null, 6],
    ['Electronics', null, 5],
    ['BA', 'Books', 2],
    ['BA', 'Electronics', 1],
    ['delivered', 'Books\u001fBA', 2],
    ['delivered', 'Electronics\u001fBA', 1],
  ]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].data).toEqual([{
    name: 'All', __lv_dataset: 'primary', __lv_row_index: -1, __lv_synthetic: true,
    children: [
      { name: 'Books', value: 6, __lv_dataset: 'primary', __lv_row_index: 0, children: [{ name: 'BA', value: 2, __lv_dataset: 'primary', __lv_row_index: 2, children: [{ name: 'delivered', value: 2, __lv_dataset: 'primary', __lv_row_index: 4 }] }] },
      { name: 'Electronics', value: 5, __lv_dataset: 'primary', __lv_row_index: 1, children: [{ name: 'BA', value: 1, __lv_dataset: 'primary', __lv_row_index: 3, children: [{ name: 'delivered', value: 1, __lv_dataset: 'primary', __lv_row_index: 5 }] }] },
    ],
  }])
})

test('ECharts leaves absent proportional geometry fields to renderer defaults', () => {
  const pie = echartsOption(proportionalFixture('pie'), defaultRendererContext) as any
  expect(Object.hasOwn(pie.series[0], 'radius')).toBe(false)

  const defaultFunnelEnvelope = proportionalFixture('funnel') as any
  defaultFunnelEnvelope.spec.presentation.align = undefined
  const defaultFunnel = echartsOption(defaultFunnelEnvelope, defaultRendererContext) as any
  expect(Object.hasOwn(defaultFunnel.series[0], 'funnelAlign')).toBe(false)
})

test('ECharts formats gauges, applies semantic thresholds, and renders status states', () => {
  const envelope = gaugeFixture()
  const option = echartsOption(envelope, { ...defaultRendererContext, locale: 'pt-BR' },) as any
  expect(option.series[0].id).toBe('series:polar:gauge')
  expect(option.series[0].axisLine.lineStyle.color).toEqual([[0.5, defaultRendererContext.colors.attention], [0.8, defaultRendererContext.colors.danger]])
  expect(option.series[0].detail.formatter(0.75)).toBe('75%')

  const noData = { ...cartesianFixture('line'), status: { kind: 'no_data', message: 'No matching rows' } } as VisualizationEnvelope
  const statusOption = echartsOption(noData, defaultRendererContext) as any
  expect(statusOption.graphic[0]).toMatchObject({ type: 'text', style: { text: 'No matching rows' } })
})

test('ECharts owns the complete gauge color scale when thresholds are omitted', () => {
  const envelope = gaugeFixture() as any
  envelope.spec.presentation.thresholds = undefined
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].axisLine.lineStyle.color).toEqual([[1, defaultRendererContext.colors.accent]])
})

test('ECharts rejects gauge envelopes without an explicit truthful domain', () => {
  const envelope = gaugeFixture() as any
  envelope.spec.presentation.minimum = undefined
  envelope.spec.presentation.maximum = undefined
  expect(() => echartsOption(envelope, defaultRendererContext)).toThrow(/explicit minimum and maximum/)
})

test('ECharts renders an explicit labeled target independently from the metricd value', () => {
  const envelope = gaugeFixture() as any
  envelope.spec.presentation.target = 0.8
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series).toHaveLength(2)
  expect(option.series[1]).toMatchObject({
    id: 'series:polar:gauge:target',
    type: 'gauge',
    min: 0,
    max: 1,
    axisLine: { show: false },
    progress: { show: false },
    detail: { show: false },
  })
  expect(option.series[1].data[0]).toMatchObject({ value: 0.8, name: 'Target 80%' })
  expect(option.series[1].data[0].pointer.width).toBeGreaterThan(0)
})

test('ECharts renders a visible diagnostic instead of clipping an out-of-domain gauge value', () => {
  const envelope = gaugeFixture() as any
  envelope.dataState.datasets[0].rows = [[1.2]]
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series).toEqual([])
  expect(option.graphic[0].style.text).toContain('outside configured gauge domain 0.0%–100.0%')
})

function cartesianFixture(mark: string, columns = ['label', 'value']): VisualizationEnvelope {
  const fields = columns.map((id, index) => ({ id, role: index === 0 ? 'dimension' : 'metric', dataType: index === 0 || id === 'row' ? 'string' : 'decimal', nullable: false, label: id }))
  const y = columns.slice(1).map((field) => ({ dataset: 'primary', field }))
  const row = columns.map((id, index) => index === 0 ? 'A' : id === 'row' ? 'R1' : index)
  return {
    schemaVersion: 9, visualID: mark, rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: { kind: 'cartesian', title: mark, mark, datasets: [{ id: 'primary', fields }], dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: mark, description: mark }, interactions: [], x: { dataset: 'primary', field: 'label' }, y, presentation: { legend: 'bottom', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, smooth: true, stacked: true, showSymbols: false, dataZoom: true, area: mark === 'area', step: true, symbolSize: 12, labelPosition: 'top', orientation: mark === 'bar' ? 'horizontal' : 'vertical', histogramBins: mark === 'histogram' ? 10 : undefined } },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns, rows: [row], completeness: 'complete' }] }, selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

function cartesianSeriesFixture(): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: 'series', rendererID: 'echarts', specRevision: 'sha256:series', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'Orders', mark: 'area',
      datasets: [{ id: 'primary', fields: [
        { id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'Month' },
        { id: 'series', role: 'dimension', dataType: 'string', nullable: false, label: 'Status' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Orders' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Orders by status' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], series: { dataset: 'primary', field: 'series' },
      presentation: { legend: 'bottom', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, smooth: false, stacked: false, stacking: 'normal', showSymbols: true, dataZoom: false, area: true, step: false },
    },
    dataState: {
      kind: 'inline', specRevision: 'sha256:series', dataRevision: 1, generation: 1,
      datasets: [{
        id: 'primary', specRevision: 'sha256:series', dataRevision: 1, generation: 1,
        columns: ['label', 'series', 'value'],
        rows: [['Jan', 'processing', 30], ['Jan', 'delivered', 10], ['Feb', 'processing', 10], ['Feb', 'delivered', 30]],
        completeness: 'complete',
      }],
    },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

function proportionalFixture(mark: 'pie' | 'donut' | 'funnel'): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: mark, rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: { kind: 'proportional', title: mark, mark, datasets: [{ id: 'primary', fields: [{ id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'Label' }, { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' }] }], dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: mark, description: mark }, interactions: [], category: { dataset: 'primary', field: 'label' }, value: { dataset: 'primary', field: 'value' }, presentation: { legend: 'right', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, orientation: 'vertical', rose: true, centerLabel: mark === 'donut' ? 'Orders' : undefined, labelPosition: 'outside', innerRadius: mark === 'donut' ? 0.54 : undefined, outerRadius: mark === 'donut' ? 0.76 : undefined, align: mark === 'funnel' ? 'left' : undefined, sort: mark === 'funnel' ? 'ascending' : undefined } },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['A', 10]], completeness: 'complete' }] }, selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

function hierarchyFixture(mark: 'tree' | 'treemap' | 'sunburst'): VisualizationEnvelope {
  const envelope = cartesianFixture('line') as any
  envelope.visualID = mark
  envelope.spec = { kind: 'hierarchy', title: mark, mark, datasets: [{ id: 'primary', fields: [{ id: 'node', role: 'identity', dataType: 'string', nullable: false, label: 'Node' }, { id: 'parent', role: 'dimension', dataType: 'string', nullable: true, label: 'Parent' }, { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' }] }], dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: mark, description: mark }, interactions: [], node: { dataset: 'primary', field: 'node' }, parent: { dataset: 'primary', field: 'parent' }, value: { dataset: 'primary', field: 'value' }, presentation: { legend: 'hidden', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, orientation: 'vertical', initialDepth: 2, roam: true, layout: 'standard', breadcrumb: true } }
  envelope.dataState.datasets[0] = { ...envelope.dataState.datasets[0], columns: ['node', 'parent', 'value'], rows: [['root', null, 10], ['child', 'root', 4]] }
  return envelope
}

function networkFixture(mark: 'graph' | 'sankey'): VisualizationEnvelope {
  const envelope = hierarchyFixture('tree') as any
  envelope.visualID = mark
  envelope.spec.mark = mark
  envelope.spec.node = { dataset: 'primary', field: 'source' }
  envelope.spec.parent = undefined
  envelope.spec.source = { dataset: 'primary', field: 'source' }
  envelope.spec.target = { dataset: 'primary', field: 'target' }
  envelope.spec.presentation = { ...envelope.spec.presentation, orientation: 'vertical', layout: 'circular', nodeGap: 18, curveness: 0.3, focus: 'adjacency' }
  envelope.spec.datasets[0].fields = [{ id: 'source', role: 'dimension', dataType: 'string', nullable: false, label: 'Source' }, { id: 'target', role: 'dimension', dataType: 'string', nullable: false, label: 'Target' }, { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' }]
  envelope.dataState.datasets[0] = { ...envelope.dataState.datasets[0], columns: ['source', 'target', 'value'], rows: [['A', 'B', 4]] }
  return envelope
}

function gaugeFixture(): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: 'gauge', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: { kind: 'polar', title: 'Gauge', mark: 'gauge', datasets: [{ id: 'primary', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Rate', format: { kind: 'percent', minimumFractionDigits: 1, maximumFractionDigits: 1 } }] }], dataBudget: { maxRows: 1, requiredCompleteness: 'complete' }, accessibility: { title: 'Gauge', description: 'Gauge' }, interactions: [], value: { dataset: 'primary', field: 'value' }, presentation: { legend: 'hidden', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, minimum: 0, maximum: 1, showPointer: true, progressWidth: 12, thresholds: [{ value: 0.5, tone: 'warning' }, { value: 0.8, tone: 'danger' }] } },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['value'], rows: [[0.75]], completeness: 'complete' }] }, selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}
