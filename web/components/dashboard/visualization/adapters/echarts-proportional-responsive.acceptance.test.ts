import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import { defaultRendererContext } from '../host-controller'
import { echartsOption, responsiveEChartsPatch } from './echarts'
import { responsiveEChartsLayoutKey } from './echarts/view-state'
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

function transformedLabelBox(item: any): [number, number][] {
  const rect = item.getBoundingRect()
  const transform = item.getComputedTransform?.() ?? item.transform
  const points: [number, number][] = [
    [rect.x, rect.y],
    [rect.x + rect.width, rect.y],
    [rect.x + rect.width, rect.y + rect.height],
    [rect.x, rect.y + rect.height],
  ]
  if (!transform) return points
  return points.map(([x, y]) => [
    transform[0] * x + transform[2] * y + transform[4],
    transform[1] * x + transform[3] * y + transform[5],
  ])
}

function labelBoxesOverlap(left: [number, number][], right: [number, number][]): boolean {
  for (const box of [left, right]) {
    for (let index = 0; index < box.length; index++) {
      const point = box[index]!
      const next = box[(index + 1) % box.length]!
      const axis: [number, number] = [point[1] - next[1], next[0] - point[0]]
      const leftProjection = left.map(([x, y]) => x * axis[0] + y * axis[1])
      const rightProjection = right.map(([x, y]) => x * axis[0] + y * axis[1])
      if (Math.max(...leftProjection) <= Math.min(...rightProjection)
        || Math.max(...rightProjection) <= Math.min(...leftProjection)) return false
    }
  }
  return true
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

test('outside funnel labels preserve the formatted value when a category uses the label budget', () => {
  const envelope = proportionalWithIconFormat('funnel')
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') throw new Error('Expected proportional fixture')
  envelope.spec.datasets[0]!.fields[1]!.format = { kind: 'currency', currency: 'USD' }
  envelope.dataState.datasets[0]!.rows = [['United States of America', 1_980_000]]

  const label = (echartsOption(envelope, defaultRendererContext) as any).series[0].label.formatter({
    value: envelope.dataState.datasets[0]!.rows[0],
  })

  expect(label).toBe('United States o…: $1.98M')
  expect(label).toContain('$1.98M')
  expect([...new Intl.Segmenter('en', { granularity: 'grapheme' }).segment(label)]).toHaveLength(24)
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

  for (const [width, height, alignTo, lineLength, endLength] of [
    [373, 282, 'edge'], [435, 420, 'labelLine', 34, 20],
    [600, 360, 'labelLine', 29, 21], [330, 220, 'edge'],
  ] as const) {
    const patch = responsiveEChartsPatch(option, width, height)
    expect(patch.series[0].id).toBe('series:primary:donut')
    expect(patch.series[0].radius).toEqual(['54%', '76%'])
    expect(patch.series[0].label.alignTo).toBe(alignTo)
    if (alignTo === 'labelLine') {
      expect(patch.series[0].label).toMatchObject({ distanceToLabelLine: 12 })
      expect(patch.series[0].labelLine).toMatchObject({ length: lineLength, length2: endLength })
    }
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
      expect(compact.series[0].label.formatter({ value: ['United States of America', 1] })).toBe('United States of Ame…:\n1')
      expect(expanded.series[0].label.formatter({ value: ['United States of America', 1] })).toBe('United States of Ame…: 1')
      expect(compact.series[0]).toMatchObject({ left: '6%', right: '44%' })
      expect(expanded.series[0]).toMatchObject({ left: '25%', right: '25%' })
    } else {
      expect(compact.series[0].label.alignTo).toBe('edge')
      expect(expanded.series[0].label.alignTo).toBe('labelLine')
      expect(compact.series[0].id).toBe(`series:primary:${mark}`)
      expect(expanded.series[0].id).toBe(`series:primary:${mark}`)
    }
    expect(option.series[0].id).toBe(`series:primary:${mark}`)
  }
})

test('funnel geometry recenters on expansion and restores card label space when collapsed', () => {
  const envelope = proportionalWithIconFormat('funnel')
  const source = echartsOption(envelope, defaultRendererContext) as any
  expect(responsiveEChartsPatch(source, 1200, 720).legend.left).toBe('center')
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 320, height: 240 })
  try {
    chart.setOption({ ...source, ...responsiveEChartsPatch(source, 320, 240), animation: false })
    chart.dispatchAction({ type: 'legendUnSelect', name: 'Status 0' })
    chart.resize({ width: 1200, height: 720 })
    chart.setOption(responsiveEChartsPatch(source, 1200, 720))
    const expanded = (chart as any).getModel().getSeriesByIndex(0)
    const layouts = Array.from({ length: expanded.getData().count() }, (_, index) => expanded.getData().getItemLayout(index).points)
    const points = layouts.sort((a: number[][], b: number[][]) => (b[1][0] - b[0][0]) - (a[1][0] - a[0][0]))[0]
    expect((points[0][0] + points[1][0]) / 2).toBeCloseTo(600, 3)
    expect(expanded.get('funnelAlign')).toBe('left')
    expect(expanded.get('sort')).toBe(source.series[0].sort)
    expect((chart as any).getModel().getComponent('legend').isSelected('Status 0')).toBe(false)
    chart.resize({ width: 320, height: 240 })
    chart.setOption(responsiveEChartsPatch(source, 320, 240))
    expect((chart as any).getModel().getSeriesByIndex(0).get('left')).toBe('6%')
  } finally { chart.dispose() }
})

test('funnel without visible labels still centers its expanded plotting box', () => {
  const envelope = proportionalWithIconFormat('funnel')
  if (envelope.spec.kind !== 'proportional') throw new Error('Expected proportional fixture')
  envelope.spec.presentation.labelPolicy.density = 'hidden'
  const source = echartsOption(envelope, defaultRendererContext) as any
  expect(responsiveEChartsPatch(source, 1200, 720).series[0]).toMatchObject({ left: '25%', right: '25%' })
  expect(responsiveEChartsPatch(source, 320, 240).series[0]).toMatchObject({ left: '6%', right: '44%' })
})

test('compact funnel labels wrap long category and value text inside the chart bounds', () => {
  const envelope = proportionalWithIconFormat('funnel')
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') throw new Error('Expected proportional fixture')
  envelope.spec.datasets[0]!.fields[1]!.format = { kind: 'currency', currency: 'USD' }
  envelope.dataState.datasets[0]!.rows = [
    ['Canada', 2_070_000], ['France', 2_020_000], ['Germany', 1_950_000], ['Mexico', 1_740_000],
    ['United States of America', 1_740_000],
  ]
  const source = echartsOption(envelope, defaultRendererContext) as any
  expect(responsiveEChartsLayoutKey(envelope, 320, 240)).not.toBe(responsiveEChartsLayoutKey(envelope, 260, 240))
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 320, height: 240 })
  try {
    chart.setOption({ ...source, ...responsiveEChartsPatch(source, 320, 240), animation: false })
    chart.renderToSVGString()
    const labels = chart.getZr().storage.getDisplayList()
      .filter((item: any) => item.type === 'tspan' && ['United States o…:', '$1.74M'].includes(String(item.style?.text)))
    expect(labels.map((item: any) => item.style.text)).toContain('United States o…:')
    expect(labels.map((item: any) => item.style.text)).toContain('$1.74M')
    expect(labels).toHaveLength(2)
    for (const item of labels) {
      const bounds = item.getBoundingRect().clone()
      const transform = item.getComputedTransform?.() ?? item.transform
      if (transform) bounds.applyTransform(transform)
      expect(bounds.x, `${item.style.text} should not clip on the left`).toBeGreaterThanOrEqual(0)
      expect(bounds.x + bounds.width, `${item.style.text} should not clip on the right`).toBeLessThanOrEqual(320)
    }

    chart.resize({ width: 620, height: 400 })
    chart.setOption(responsiveEChartsPatch(source, 620, 400))
    expect((chart as any).getModel().getSeriesByIndex(0).getFormattedLabel(4)).toBe('United States o…: $1.74M')
  } finally {
    chart.dispose()
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
      const finalSegment = Math.hypot(end[0] - elbow[0], end[1] - elbow[1])
      expect(finalSegment).toBeGreaterThanOrEqual(41)
      expect(finalSegment).toBeLessThanOrEqual(44)
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
  expect(responsiveEChartsPatch(echartsOption(inside, defaultRendererContext) as any, 1200, 720).series[0].label).toMatchObject({
    position: 'inside', fontSize: 12, padding: 3,
  })
  expect(responsiveEChartsPatch(echartsOption(inside, defaultRendererContext) as any, 535, 420).series[0].label).toMatchObject({
    position: 'inside', fontSize: 11, padding: 0, rotate: 'radial',
  })
})

test('growing an inside pie restores horizontal labels after compact radial layout', () => {
  const envelope = proportionalWithIconFormat('pie')
  if (envelope.spec.kind !== 'proportional') throw new Error('Expected proportional fixture')
  envelope.spec.presentation.labelPosition = 'inside'
  const source = echartsOption(envelope, defaultRendererContext) as any
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 396, height: 420 })
  try {
    chart.setOption({ ...source, ...responsiveEChartsPatch(source, 396, 420), animation: false })
    expect((chart as any).getModel().getSeriesByIndex(0).get(['label', 'rotate'])).toBe('radial')

    chart.resize({ width: 700, height: 500 })
    chart.setOption(responsiveEChartsPatch(source, 700, 500))
    expect((chart as any).getModel().getSeriesByIndex(0).get(['label', 'rotate'])).toBeUndefined()
  } finally {
    chart.dispose()
  }
})

test('card-sized inside pie labels do not collide', () => {
  const envelope = proportionalWithIconFormat('pie')
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') throw new Error('Expected proportional fixture')
  envelope.spec.presentation.labelPosition = 'inside'
  envelope.spec.presentation.rose = false
  envelope.spec.datasets[0].fields[1]!.format = { kind: 'currency', currency: 'BRL' }
  envelope.dataState.datasets[0].rows = [
    ['health_beauty', 1_440_000], ['watches_gifts', 1_300_000], ['bed_bath_table', 1_260_000],
    ['sports_leisure', 1_150_000], ['computers_accessories', 1_070_000], ['furniture_decor', 903_000],
  ]
  const source = echartsOption(envelope, defaultRendererContext) as any
  for (const [width, height] of [[396, 420], [535, 420]] as const) {
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height })
    try {
      chart.setOption({ ...source, ...responsiveEChartsPatch(source, width, height), animation: false })
      chart.renderToSVGString()
      const labels = ['R$1.44M', 'R$1.3M', 'R$1.26M', 'R$1.15M', 'R$1.07M', 'R$0.903M']
      const bounds = chart.getZr().storage.getDisplayList()
        .filter((item: any) => item.type === 'tspan' && labels.includes(String(item.style?.text)))
        .map(transformedLabelBox)
      expect(bounds).toHaveLength(labels.length)
      for (let index = 0; index < bounds.length; index++) {
        for (let candidate = index + 1; candidate < bounds.length; candidate++) {
          expect(labelBoxesOverlap(bounds[index]!, bounds[candidate]!)).toBe(false)
        }
      }
    } finally {
      chart.dispose()
    }
  }
})

test('card-sized inside pies tilt every formatted value instead of hiding crowded sectors', () => {
  const envelope = proportionalWithIconFormat('pie')
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') throw new Error('Expected proportional fixture')
  envelope.spec.presentation.labelPosition = 'inside'
  envelope.spec.presentation.rose = false
  envelope.spec.datasets[0].fields[1]!.format = { kind: 'currency', currency: 'BRL' }
  const rows = [
    ['health_beauty', 1_440_000], ['watches_gifts', 1_300_000], ['bed_bath_table', 1_260_000],
    ['sports_leisure', 1_150_000], ['computers_accessories', 1_070_000], ['furniture_decor', 903_000],
  ]
  envelope.dataState.datasets[0].rows = rows

  const source = echartsOption(envelope, defaultRendererContext) as any
  const compact = responsiveEChartsPatch(source, 239, 225).series[0]
  expect(compact.label.rotate).toBe('radial')
  expect(compact.label.formatter({ value: rows[4], dataIndex: 4 })).toBe('R$1.07M')
  expect(compact.label.formatter({ value: rows[5], dataIndex: 5 })).toBe('R$0.903M')
  expect((responsiveEChartsPatch(source, 700, 500).series[0].label as any).rotate).toBeNull()
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
