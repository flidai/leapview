import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'

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
      rows: [['Speed', 'A', '8'], ['Quality', 'A', '9'], ['Speed', 'B', '6'], ['Quality', 'B', '7']], completeness: 'complete',
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
  expect(option.series[0]).toMatchObject({ id: 'series:polar:radar', type: 'radar', areaStyle: {} })
  expect(option.series[0]).not.toHaveProperty('pointer')
  expect(option.series[0]).not.toHaveProperty('progress')
  expect(option.series[0].data).toEqual([{ name: 'A', value: ['8', '9'] }, { name: 'B', value: ['6', '7'] }])

  envelope.spec.presentation.maximum = 12
  envelope.spec.presentation.area = false
  const configured = echartsOption(envelope, context) as any
  expect(configured.radar.indicator.map((item: any) => item.max)).toEqual([12, 12])
  expect(configured.series[0]).toMatchObject({ id: 'series:polar:radar', type: 'radar' })
  expect(configured.series[0].areaStyle).toBeUndefined()

  envelope.spec.presentation.maximum = undefined
  envelope.dataState.datasets[0].rows = [['Speed', 'A', '0'], ['Quality', 'A', '0']]
  const zero = echartsOption(envelope, context) as any
  expect(zero.radar.indicator.map((item: any) => item.max)).toEqual([1, 1])
  expect(zero.series[0].data).toEqual([{ name: 'A', value: ['0', '0'] }])

  envelope.dataState.datasets[0].rows = []
  const empty = echartsOption(envelope, context) as any
  expect(empty.radar.indicator).toEqual([])
  expect(empty.series[0].data).toEqual([])
})

test('ECharts gauge axis labels and guides use theme contrast tokens', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'gauge', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'polar', title: 'Gauge', mark: 'gauge',
      datasets: [{ id: 'primary', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Rate' }] }],
      dataBudget: { maxRows: 1, requiredCompleteness: 'complete' }, accessibility: { title: 'Gauge', description: 'Gauge' }, interactions: [],
      value: { dataset: 'primary', field: 'value' },
      presentation: { legend: 'hidden', labelPolicy: { density: 'automatic', priority: [], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, minimum: 0, maximum: 100, showPointer: true, progressWidth: 12 },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['value'], rows: [[75]], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
  const contexts = [
    defaultRendererContext,
    { ...defaultRendererContext, theme: 'dark' as const, colors: { ...defaultRendererContext.colors, muted: '#8b949e', grid: '#30363d' } },
  ]
  for (const context of contexts) {
    const series = (echartsOption(envelope, context) as any).series[0]
    expect(series.axisLabel).toMatchObject({ color: context.colors.muted, fontFamily: context.fontFamily })
    expect(series.axisTick).toEqual({ lineStyle: { color: context.colors.muted } })
    expect(series.splitLine).toEqual({ lineStyle: { color: context.colors.grid } })
  }
})

test('ECharts radar keeps null, display-colliding, and typed series identities distinct', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'quality-identities', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'polar', title: 'Quality', mark: 'radar',
      datasets: [{ id: 'primary', fields: [
        { id: 'metric', role: 'dimension', dataType: 'string', nullable: false, label: 'Metric' },
        { id: 'team', role: 'dimension', dataType: 'string', nullable: true, label: 'Team' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Quality', description: 'Quality by team' }, interactions: [],
      category: { dataset: 'primary', field: 'metric' }, series: { dataset: 'primary', field: 'team' }, value: { dataset: 'primary', field: 'value' },
      presentation: { legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, showPointer: false, area: false },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['metric', 'team', 'value'], rows: [['Speed', null, '8'], ['Speed', '—', '9'], ['Speed', 1, '6'], ['Speed', '1', '7']], completeness: 'complete' }] },
    selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].data.map((entry: any) => entry.name)).toEqual(['null:', 'string:—', 'number:1', 'string:1'])
  expect(option.legend.data).toEqual([{ name: 'null:' }, { name: 'string:—' }, { name: 'number:1' }, { name: 'string:1' }])
  expect(option.legend.formatter('null:')).toBe('—')
  expect(option.legend.formatter('number:1')).toBe('1')
})

test('ECharts emits only mark-supported proportional fields and preserves explicit false and zero', () => {
  const pie = proportionalFixture('pie') as any
  pie.spec.presentation.rose = false
  pie.spec.presentation.outerRadius = 1
  pie.spec.presentation.labelPosition = 'inside'
  pie.spec.presentation.centerLabel = 'Not a pie center'
  const pieOption = echartsOption(pie, defaultRendererContext) as any
  expect(pieOption.series[0]).toMatchObject({
    id: 'series:primary:pie', type: 'pie', encode: { itemName: 'label', value: 'value' },
    radius: '100%', roseType: false, avoidLabelOverlap: true, minShowLabelAngle: 3,
    label: { position: 'inside' }, labelLine: { show: false },
  })
  expect(pieOption.series[0].itemStyle.color).toEqual(expect.any(Function))
  expect(pieOption.series[0]).not.toHaveProperty('funnelAlign')
  expect(pieOption.series[0]).not.toHaveProperty('sort')
  expect(pieOption.graphic).toBeUndefined()

  const donut = proportionalFixture('donut') as any
  donut.spec.presentation.rose = false
  donut.spec.presentation.innerRadius = 0
  donut.spec.presentation.outerRadius = 1
  const donutOption = echartsOption(donut, defaultRendererContext) as any
  expect(donutOption.series[0]).toMatchObject({
    id: 'series:primary:donut', type: 'pie', encode: { itemName: 'label', value: 'value' },
    radius: ['0%', '100%'], roseType: false, avoidLabelOverlap: true, minShowLabelAngle: 3,
  })
  expect(donutOption.graphic[0].style.text).toBe('Orders')
  expect(donutOption.series[0].labelLine.show).toBe(true)

  const emptyDonut = structuredClone(donut)
  emptyDonut.spec.presentation.centerLabel = undefined
  emptyDonut.dataState.datasets[0].rows = []
  const emptyDonutOption = echartsOption(emptyDonut, defaultRendererContext) as any
  expect(emptyDonutOption.dataset.source).toEqual([['label', 'value']])
  expect(emptyDonutOption.graphic[0].style.text).toBe('{centerValue|0}\n{centerLabel|Total}')

  const funnel = proportionalFixture('funnel') as any
  funnel.spec.presentation.rose = true
  funnel.spec.presentation.outerRadius = 0.8
  funnel.spec.presentation.orientation = 'horizontal'
  funnel.spec.presentation.align = 'center'
  funnel.spec.presentation.sort = 'descending'
  funnel.spec.presentation.labelPosition = 'inside'
  const funnelOption = echartsOption(funnel, defaultRendererContext) as any
  expect(funnelOption.series[0]).toMatchObject({
    id: 'series:primary:funnel', type: 'funnel', encode: { itemName: 'label', value: 'value' },
    orient: 'horizontal', funnelAlign: 'center', sort: 'descending', label: { position: 'inside' },
  })
  expect(funnelOption.series[0].itemStyle.color).toEqual(expect.any(Function))
  for (const pieOnly of ['radius', 'roseType', 'avoidLabelOverlap', 'minShowLabelAngle', 'labelLine']) {
    expect(funnelOption.series[0]).not.toHaveProperty(pieOnly)
  }
})

function proportionalFixture(mark: 'pie' | 'donut' | 'funnel'): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: mark, rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: { kind: 'proportional', title: mark, mark, datasets: [{ id: 'primary', fields: [{ id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'Label' }, { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' }] }], dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: mark, description: mark }, interactions: [], category: { dataset: 'primary', field: 'label' }, value: { dataset: 'primary', field: 'value' }, presentation: { legend: 'right', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, orientation: 'vertical', rose: true, centerLabel: mark === 'donut' ? 'Orders' : undefined, labelPosition: 'outside', innerRadius: mark === 'donut' ? 0.54 : undefined, outerRadius: mark === 'donut' ? 0.76 : undefined, align: mark === 'funnel' ? 'left' : undefined, sort: mark === 'funnel' ? 'ascending' : undefined } },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['A', 10]], completeness: 'complete' }] }, selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}
