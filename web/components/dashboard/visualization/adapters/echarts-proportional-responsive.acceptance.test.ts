import { expect, test } from 'bun:test'

import { defaultRendererContext } from '../host-controller'
import { echartsOption, responsiveEChartsPatch } from './echarts'
import { proportionalFixture } from './echarts-test-fixtures'

function proportionalWithIconFormat(mark: 'pie' | 'donut' | 'funnel' = 'donut') {
  const envelope = proportionalFixture(mark)
  if (envelope.spec.kind !== 'proportional') throw new Error('Expected proportional fixture')
  envelope.spec.presentation.legend = 'bottom'
  envelope.spec.presentation.rose = false
  envelope.spec.presentation.labelPolicy.priority = []
  envelope.spec.conditionalFormatting = [{
    id: 'status-format', target: 'mark_fill', field: envelope.spec.value,
    rule: {
      kind: 'rules',
      rules: [{ operator: 'greater_or_equal', value: 0, style: { color: 'danger', icon: 'circle' } }],
      nullStyle: { icon: 'warning' },
      defaultStyle: { color: 'success', icon: 'square' },
    },
  }]
  if (envelope.dataState.kind !== 'inline') throw new Error('Expected inline fixture')
  envelope.dataState.datasets[0].rows = Array.from({ length: 8 }, (_, index) => [`Status ${index}`, index === 7 ? 96 : index + 1])
  return envelope
}

test('proportional labels use formatted category and value text with normal density policy', () => {
  for (const mark of ['pie', 'donut', 'funnel'] as const) {
    const envelope = proportionalWithIconFormat(mark)
    const option = echartsOption(envelope, defaultRendererContext) as any
    const series = option.series[0]

    expect(series.label).toMatchObject({ show: true, overflow: 'truncate' })
    expect(series.label.formatter({ value: ['Status 0', 1] })).toBe('Status 0: 1')
    expect(series.label.formatter({ value: ['Status 7', 96] })).toBe('Status 7: 96')
    expect(series.labelLayout({ dataIndex: 0 })).toEqual({ hideOverlap: true })
    expect(series.label.formatter({ value: ['Status 0', null] })).toBe('Status 0: —')

    if (mark === 'pie' || mark === 'donut') expect(series.minShowLabelAngle).toBe(3)
    expect(series.itemStyle.color({ value: ['Status 0', 1] })).toBe(defaultRendererContext.colors.danger)
  }
})

test('proportional responsive sizing keeps authored radii and the bottom legend band', () => {
  const envelope = proportionalWithIconFormat('donut')
  if (envelope.spec.kind !== 'proportional') throw new Error('Expected proportional fixture')
  envelope.spec.presentation.legendTitle = 'Order status'
  const option = echartsOption(envelope, defaultRendererContext) as any
  const before = JSON.stringify(option)

  expect(option.series[0].radius).toEqual(['54%', '76%'])
  expect(option.series[0]).toMatchObject({ bottom: '12%' })
  expect(option.legend).toMatchObject({ bottom: 0 })
  expect(option.graphic).toEqual(expect.arrayContaining([
    expect.objectContaining({ type: 'text', bottom: 28, style: expect.objectContaining({ text: 'Order status' }) }),
  ]))

  for (const [width, height] of [[373, 282], [600, 360], [330, 220]]) {
    expect(responsiveEChartsPatch(option, width, height)).toEqual({})
  }
  expect(JSON.stringify(option)).toBe(before)
})

test('proportional responsive helper leaves pie, donut, and funnel geometry to ECharts', () => {
  for (const mark of ['pie', 'donut', 'funnel'] as const) {
    const envelope = proportionalWithIconFormat(mark)
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(responsiveEChartsPatch(option, 320, 240)).toEqual({})
    expect(option.series[0].id).toBe(`series:primary:${mark}`)
  }
})

test('proportional labels honor hidden and inside presentation settings without conditional forcing', () => {
  const hidden = proportionalWithIconFormat('donut')
  if (hidden.spec.kind !== 'proportional') throw new Error('Expected proportional fixture')
  hidden.spec.presentation.labelPolicy.density = 'hidden'
  const hiddenSeries = (echartsOption(hidden, defaultRendererContext) as any).series[0]
  expect(hiddenSeries.label).toMatchObject({ show: false, overflow: 'truncate' })
  expect(hiddenSeries.labelLayout).toEqual({ hideOverlap: true })
  expect(hiddenSeries.minShowLabelAngle).toBe(3)

  const inside = proportionalWithIconFormat('donut')
  if (inside.spec.kind !== 'proportional') throw new Error('Expected proportional fixture')
  inside.spec.presentation.labelPosition = 'inside'
  const insideSeries = (echartsOption(inside, defaultRendererContext) as any).series[0]
  expect(insideSeries.label).toMatchObject({ show: true, position: 'inside' })
  expect(insideSeries.label.formatter({ value: ['Status 0', 1] })).toBe('1')
  expect(insideSeries.labelLayout({ dataIndex: 0 })).toEqual({ hideOverlap: true })
})

test('empty and loading donuts show only the shared status graphic', () => {
  for (const [kind, text] of [['no_data', 'No data'], ['loading', 'Loading…']] as const) {
    const envelope = proportionalFixture('donut') as any
    envelope.spec.presentation.centerLabel = undefined
    envelope.dataState.datasets[0].rows = []
    envelope.status = { kind }

    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.series).toEqual([])
    expect(option.graphic.map((graphic: any) => graphic.style?.text)).toEqual([text])
  }
})
