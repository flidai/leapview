import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

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

  for (const [width, height, alignTo] of [[373, 282, 'edge'], [600, 360, 'labelLine'], [330, 220, 'edge']] as const) {
    const patch = responsiveEChartsPatch(option, width, height)
    expect(patch.series[0].id).toBe('series:primary:donut')
    expect(patch.series[0].radius).toEqual(['54%', '76%'])
    expect(patch.series[0].label.alignTo).toBe(alignTo)
  }
  expect(JSON.stringify(option)).toBe(before)
})

test('proportional responsive helper keeps compact labels bounded and expanded labels near the ring', () => {
  for (const mark of ['pie', 'donut', 'funnel'] as const) {
    const envelope = proportionalWithIconFormat(mark)
    const option = echartsOption(envelope, defaultRendererContext) as any
    const compact = responsiveEChartsPatch(option, 320, 240)
    const expanded = responsiveEChartsPatch(option, 1200, 720)
    if (mark === 'funnel') {
      expect(compact).toEqual({})
      expect(expanded).toEqual({})
    } else {
      expect(compact.series[0].label.alignTo).toBe('edge')
      expect(expanded.series[0].label.alignTo).toBe('labelLine')
      expect(compact.series[0].id).toBe(`series:primary:${mark}`)
      expect(expanded.series[0].id).toBe(`series:primary:${mark}`)
    }
    expect(option.series[0].id).toBe(`series:primary:${mark}`)
  }
})

test('expanded donut guide lines stay local to the ring', () => {
  const envelope = proportionalWithIconFormat('donut')
  if (envelope.dataState.kind !== 'inline') throw new Error('Expected inline fixture')
  envelope.dataState.datasets[0].rows = [
    ['delivered', 96478], ['shipped', 1107], ['canceled', 625], ['unavailable', 609],
    ['invoiced', 314], ['processing', 301], ['created', 5], ['approved', 2],
  ]
  const source = echartsOption(envelope, defaultRendererContext) as any
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 1200, height: 720 })
  try {
    chart.setOption({ ...source, animation: false })
    const initialSeries = (chart as any).getModel().getSeriesByIndex(0)
    chart.dispatchAction({ type: 'legendUnSelect', name: 'delivered' })
    chart.setOption(responsiveEChartsPatch(source, 1200, 720))
    chart.renderToSVGString()
    const series = (chart as any).getModel().getSeriesByIndex(0)
    const data = series.getData()
    const visibleGuides = Array.from({ length: data.count() }).flatMap((_, index) => {
      const points = data.getItemGraphicEl(index)?.getTextGuideLine()?.shape?.points
      return Array.isArray(points) && points.length >= 3 ? [points] : []
    })
    expect(series).toBe(initialSeries)
    expect(series.id).toBe('series:primary:donut')
    expect((chart as any).getModel().getComponent('legend').isSelected('delivered')).toBe(false)
    expect(data.count()).toBe(7)
    expect(visibleGuides.length).toBeGreaterThan(0)
    for (const points of visibleGuides) {
      const end = points.at(-1)!
      const elbow = points.at(-2)!
      expect(Math.hypot(end[0] - elbow[0], end[1] - elbow[1])).toBeLessThanOrEqual(12)
    }
    chart.resize({ width: 320, height: 240 })
    chart.setOption(responsiveEChartsPatch(source, 320, 240))
    expect((chart as any).getModel().getSeriesByIndex(0)).toBe(initialSeries)
    expect((chart as any).getModel().getComponent('legend').isSelected('delivered')).toBe(false)
  } finally {
    chart.dispose()
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
  expect(responsiveEChartsPatch(echartsOption(inside, defaultRendererContext) as any, 1200, 720)).toEqual({})
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
