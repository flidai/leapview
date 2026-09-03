import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'

test('ECharts translates semantic axes and decision context from the current frame', () => {
  const envelope = cartesianFixture() as any
  envelope.spec.axes = [
    { id: 'x', title: 'Month', scale: 'automatic', zero: 'automatic', tickDensity: 'sparse' },
    { id: 'primary_y', title: 'Revenue', scale: 'linear', zero: 'exclude', minimum: 10, maximum: 100, unit: 'USD', tickDensity: 'dense' },
  ]
  envelope.spec.referenceLines = [
    { id: 'target', axis: 'primary_y', value: { kind: 'number', value: 80 }, label: 'Target', tone: 'success' },
    { id: 'average', axis: 'primary_y', value: { kind: 'field', field: { dataset: 'primary', field: 'value' }, reducer: 'mean' }, label: 'Average', tone: 'warning' },
  ]
  envelope.spec.referenceBands = [{
    id: 'observed', axis: 'primary_y',
    from: { kind: 'field', field: { dataset: 'primary', field: 'value' }, reducer: 'minimum' },
    to: { kind: 'field', field: { dataset: 'primary', field: 'value' }, reducer: 'maximum' },
    label: 'Observed range', tone: 'neutral',
  }]
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

  envelope.dataState.datasets[0].rows = [['A', '9007199254740993.125'], ['B', '9007199254740995.375']]
  const decimalStrings = echartsOption(envelope, context) as any
  expect(decimalStrings.series[0].markLine.data[1].yAxis).toBe('9007199254740994.25')
  expect(decimalStrings.series[0].markArea.data[0].map((item: any) => item.yAxis)).toEqual(['9007199254740993.125', '9007199254740995.375'])

  envelope.dataState.datasets[0].rows = []
  const empty = echartsOption(envelope, context) as any
  expect(empty.series[0].markLine.data.map((item: any) => item.id)).toEqual(['reference-line:target', 'event-annotation:launch'])
  expect(empty.series[0].markArea).toBeUndefined()
})

function cartesianFixture(): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: 'line', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'line', mark: 'line',
      datasets: [{ id: 'primary', fields: [
        { id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'label' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'value' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'line', description: 'line' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }],
      presentation: { legend: 'bottom', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, smooth: true, stacked: true, showSymbols: false, dataZoom: true, area: false, step: true, symbolSize: 12, labelPosition: 'top', orientation: 'vertical' },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['A', 1]], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}
