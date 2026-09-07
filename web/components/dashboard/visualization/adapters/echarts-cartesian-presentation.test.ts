import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'

test('ECharts translates supported line presentation controls exactly', () => {
  const envelope = cartesianPresentationFixture('line') as any
  envelope.spec.presentation = {
    legend: 'right',
    labelPolicy: { density: 'always', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true },
    orientation: 'horizontal',
    showSymbols: true,
    smooth: true,
    step: true,
    symbolSize: 16,
    dataZoom: true,
  }

  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.legend).toMatchObject({ show: true, orient: 'vertical', right: 0 })
  expect(option.grid).toMatchObject({ top: 16, bottom: 58 })
  expect(option.xAxis).toMatchObject({ type: 'value' })
  expect(option.yAxis).toMatchObject({ type: 'category' })
  expect(option.dataZoom).toEqual([{ type: 'inside' }, { type: 'slider' }])
  expect(option.series).toHaveLength(1)
  expect(option.series[0]).toMatchObject({
    type: 'line',
    encode: { x: 'value', y: 'label' },
    smooth: true,
    symbolSize: 16,
    step: 'middle',
  })
  expect(option.series[0].symbol).toBeUndefined()
})

test('ECharts derives horizontal axis types from their physical field refs', () => {
  const envelope = cartesianPresentationFixture('line') as any
  envelope.spec.presentation = {
    labelPolicy: { density: 'automatic', priority: [], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true },
    orientation: 'horizontal',
  }
  envelope.spec.datasets[0].fields[0].dataType = 'temporal'
  envelope.dataState.datasets[0].rows = [[Date.UTC(2026, 0, 1), 12]]

  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.xAxis).toMatchObject({ type: 'value' })
  expect(option.yAxis).toMatchObject({ type: 'time' })
  expect(option.series[0].encode).toEqual({ x: 'value', y: 'label' })
})

test('ECharts propagates symbolSize to every split Cartesian series', () => {
  const envelope = cartesianPresentationFixture('area') as any
  envelope.spec.series = { dataset: 'primary', field: 'series' }
  envelope.spec.presentation = {
    legend: 'bottom',
    labelPolicy: { density: 'automatic', priority: [], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true },
    showSymbols: false,
    smooth: false,
    step: false,
    symbolSize: 22,
    stacking: 'normal',
    dataZoom: false,
  }
  envelope.dataState.datasets[0].columns = ['label', 'series', 'value']
  envelope.dataState.datasets[0].rows = [
    ['Jan', 'approved', 3],
    ['Jan', 'pending', 2],
  ]

  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.series.map((series: any) => series.name)).toEqual(['approved', 'pending'])
  expect(option.series.map((series: any) => series.symbolSize)).toEqual([22, 22])
  expect(option.series.map((series: any) => series.symbol)).toEqual(['none', 'none'])
  expect(option.series.map((series: any) => series.stack)).toEqual(['normal', 'normal'])
})

test('ECharts preserves common labels, display units, and axes for heatmap', () => {
  const envelope = cartesianPresentationFixture('heatmap') as any
  envelope.spec.x = { dataset: 'primary', field: 'label' }
  envelope.spec.y = [
    { dataset: 'primary', field: 'row' },
    { dataset: 'primary', field: 'value' },
  ]
  envelope.spec.datasets[0].fields = [
    { id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'Label' },
    { id: 'row', role: 'dimension', dataType: 'string', nullable: false, label: 'Row' },
    { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' },
  ]
  envelope.spec.presentation = {
    labelPolicy: { density: 'always', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true },
    labelPosition: 'inside',
    displayUnits: 'millions',
  }
  envelope.spec.axes = [{ id: 'x', title: 'Period', scale: 'automatic', zero: 'automatic', tickDensity: 'dense' }]
  envelope.dataState.datasets[0].columns = ['label', 'row', 'value']
  envelope.dataState.datasets[0].rows = [['A', 'R1', 1_000_000]]

  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.legend).toBeUndefined()
  expect(option.dataZoom).toBeUndefined()
  expect(option.xAxis).toMatchObject({ type: 'category', name: 'Period', axisLabel: { interval: 0 } })
  expect(option.yAxis).toMatchObject({ type: 'category' })
  expect(option.series[0]).toMatchObject({ type: 'heatmap', encode: { x: 'label', y: 'row', value: 'value' } })
  expect(option.series[0].label.position).toBe('inside')
  expect(option.series[0].label.formatter({ value: ['A', 'R1', 1_000_000] })).toBe('1M')
})

function cartesianPresentationFixture(mark: string): VisualizationEnvelope {
  return {
    schemaVersion: 9,
    visualID: mark,
    rendererID: 'echarts',
    specRevision: 'sha256:cartesian-presentation',
    dataRevision: 1,
    spec: {
      kind: 'cartesian',
      title: mark,
      mark,
      datasets: [{ id: 'primary', fields: [
        { id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'Label' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' },
      accessibility: { title: mark, description: mark },
      interactions: [],
      x: { dataset: 'primary', field: 'label' },
      y: [{ dataset: 'primary', field: 'value' }],
      presentation: {
        labelPolicy: { density: 'automatic', priority: [], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true },
      },
    },
    dataState: {
      kind: 'inline',
      specRevision: 'sha256:cartesian-presentation',
      dataRevision: 1,
      generation: 1,
      datasets: [{
        id: 'primary',
        specRevision: 'sha256:cartesian-presentation',
        dataRevision: 1,
        generation: 1,
        columns: ['label', 'value'],
        rows: [['Jan', 1]],
        completeness: 'complete',
      }],
    },
    selection: [],
    status: { kind: 'ready' },
    diagnostics: [],
  } as VisualizationEnvelope
}
