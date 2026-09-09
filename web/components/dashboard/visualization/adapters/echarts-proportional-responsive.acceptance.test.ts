import { expect, test } from 'bun:test'
import { echartsOption, responsiveEChartsPatch } from './echarts'
import { proportionalFixture } from './echarts-test-fixtures'

function cueFixture(mark: 'pie' | 'donut' | 'funnel' = 'donut') {
  const envelope = proportionalFixture(mark)
  const spec = envelope.spec
  if (spec.kind !== 'proportional') throw new Error('Expected proportional fixture')
  spec.presentation.legend = 'bottom'
  spec.presentation.rose = false
  spec.conditionalFormatting = [{
    id: 'status-cue', target: 'mark_fill', field: spec.value,
    rule: { kind: 'rules', rules: [{ operator: 'greater_or_equal', value: 0, style: { icon: 'circle' } }], nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'warning' } },
  }]
  if (envelope.dataState.kind !== 'inline') throw new Error('Expected inline fixture')
  envelope.dataState.datasets[0].rows = Array.from({ length: 8 }, (_, index) => [`Status ${index}`, index === 7 ? 96_500 : index + 1])
  return { envelope, spec }
}

test('responsive cue layout preserves authored radius proportions and series identity without mutating options', () => {
  const { envelope } = cueFixture()
  const option = echartsOption(envelope)
  const before = JSON.stringify(option)
  for (const [width, height] of [[373, 282], [600, 360], [330, 220], [440, 220]]) {
    const patch = responsiveEChartsPatch(option, width, height, envelope)
    const series = patch.series[0]
    expect(series.id).toBe('series:primary:donut')
    expect(series.radius).toHaveLength(2)
    expect(series.radius.every((value: unknown) => typeof value === 'number' && Number.isFinite(value) && value >= 0)).toBe(true)
    expect(series.radius[0] / series.radius[1]).toBeCloseTo(0.54 / 0.76, 10)
    expect(series.radius[1]).toBeLessThan(Math.min(width, height) / 2)
  }
  expect(JSON.stringify(option)).toBe(before)
})

test('responsive cue columns stay outside the ring on both sides with and without a legend title', () => {
  const { envelope, spec } = cueFixture()
  for (const title of [undefined, 'Order status']) {
    spec.presentation.legendTitle = title
    for (const [width, height] of [[373, 282], [440, 360]]) {
      const series = responsiveEChartsPatch(echartsOption(envelope), width, height, envelope).series[0]
      const outer = series.radius[1]
      for (const side of ['left', 'right']) {
        const align = side === 'right' ? 'right' : 'left'
        const anchor = [width / 2 + (side === 'right' ? outer / 2 : -outer / 2), height / 2 - outer]
        const layout = series.labelLayout({
          dataIndex: 3, align, verticalAlign: 'middle', text: '↑ shipped: 1.11K',
          labelRect: { x: width * 0.6, y: height * 0.5, width: 80, height: 24 },
          labelLinePoints: [anchor, [width * 0.8, height / 2], [width * 0.9, height / 2]],
        })
        const x = typeof layout.x === 'string' ? Number.parseFloat(layout.x) * width / 100 : layout.x
        const labelWidth = layout.width ?? series.label?.width
        const labelAlign = layout.align ?? series.label?.align ?? align
        expect(Number.isFinite(x)).toBe(true)
        expect(Number.isFinite(labelWidth)).toBe(true)
        const left = labelAlign === 'right' ? x - labelWidth : x
        const right = left + labelWidth
        if (side === 'right') expect(left).toBeGreaterThanOrEqual(width / 2 + outer)
        else expect(right).toBeLessThanOrEqual(width / 2 - outer)
        expect(left).toBeGreaterThanOrEqual(0)
        expect(right).toBeLessThanOrEqual(width)
        expect(layout.labelLinePoints[0]).toEqual(anchor)
        expect(layout.labelLinePoints.flat().every(Number.isFinite)).toBe(true)
        const radial = layout.labelLinePoints[1]
        expect(Math.hypot(radial[0] - width / 2, radial[1] - height / 2)).toBeCloseTo(outer)
        const lane = layout.labelLinePoints[2][0]
        if (side === 'right') expect(lane).toBeGreaterThanOrEqual(width / 2 + outer)
        else expect(lane).toBeLessThanOrEqual(width / 2 - outer)
        expect(layout.labelLinePoints[2][1]).toBe(radial[1])
      }
    }
  }
})

test('responsive cue layout stays finite at narrow widths and short titled heights', () => {
  const { envelope, spec } = cueFixture()
  spec.presentation.legendTitle = 'Order status'
  const narrow = responsiveEChartsPatch(echartsOption(envelope), 96, 180, envelope).series[0]
  for (const { align, anchor } of [{ align: 'right', anchor: [12, 90] }, { align: 'left', anchor: [84, 90] }]) {
    const layout = narrow.labelLayout({ dataIndex: 3, align, labelLinePoints: [anchor, anchor, anchor] })
    const x = layout.x as number
    const labelWidth = layout.width as number
    const left = layout.align === 'right' ? x - labelWidth : x
    const right = left + labelWidth
    expect(Number.isFinite(x)).toBe(true)
    expect(left).toBeGreaterThanOrEqual(0)
    expect(right).toBeLessThanOrEqual(96)
  }
  const short = responsiveEChartsPatch(echartsOption(envelope), 320, 150, envelope).series[0]
  expect(short.top).toBe(52)
  expect(short.bottom).toBe(52)
  expect(short.radius[1]).toBeLessThan((150 - short.top - short.bottom) / 2)

  const micro = responsiveEChartsPatch(echartsOption(envelope), 1, 120, envelope).series[0]
  for (const { align, anchor } of [{ align: 'right', anchor: [0, 60] }, { align: 'left', anchor: [1, 60] }]) {
    const layout = micro.labelLayout({ dataIndex: 0, align, labelLinePoints: [anchor, anchor, anchor] })
    const x = layout.x as number
    const labelWidth = layout.width as number
    const left = layout.align === 'right' ? x - labelWidth : x
    const right = left + labelWidth
    expect([x, labelWidth, left, right].every(Number.isFinite)).toBe(true)
    expect(labelWidth).toBeGreaterThanOrEqual(0)
    expect(left).toBeGreaterThanOrEqual(0)
    expect(right).toBeLessThanOrEqual(1)
  }
})

test('responsive rose cue leaders route through the overall outer-radius circle', () => {
  const { envelope, spec } = cueFixture()
  spec.presentation.rose = true
  const width = 373, height = 282
  const series = responsiveEChartsPatch(echartsOption(envelope), width, height, envelope).series[0]
  const outer = series.radius[1]
  const anchor = [width / 2 + outer * 0.2, height / 2 - outer * 0.2]
  const layout = series.labelLayout({ dataIndex: 0, align: 'left', labelLinePoints: [anchor, [0, 0], [0, 0]] })
  expect(layout.labelLinePoints).toHaveLength(5)
  const radial = layout.labelLinePoints[1]
  expect(Math.hypot(radial[0] - width / 2, radial[1] - height / 2)).toBeCloseTo(outer)
  expect(layout.labelLinePoints[2][1]).toBe(radial[1])
  expect(layout.labelLinePoints[3][0]).toBe(layout.labelLinePoints[2][0])
})

test('responsive cue radii retain the authored zero-inner and full-outer boundary', () => {
  const { envelope, spec } = cueFixture()
  spec.presentation.innerRadius = 0
  spec.presentation.outerRadius = 1
  const radius = responsiveEChartsPatch(echartsOption(envelope), 373, 282, envelope).series[0].radius
  expect(radius[0]).toBe(0)
  expect(radius[1]).toBeGreaterThan(0)
  expect(Number.isFinite(radius[1])).toBe(true)
})

test('responsive cue geometry does not change ordinary, inside, or funnel presentation', () => {
  for (const mode of ['ordinary', 'inside', 'funnel'] as const) {
    const { envelope, spec } = cueFixture(mode === 'funnel' ? 'funnel' : 'donut')
    if (mode === 'ordinary') spec.conditionalFormatting = undefined
    if (mode === 'inside') spec.presentation.labelPosition = 'inside'
    expect(responsiveEChartsPatch(echartsOption(envelope), 373, 282, envelope).series).toBeUndefined()
  }
})

test('responsive cue layout stays finite for empty data and malformed label anchors', () => {
  const { envelope } = cueFixture()
  if (envelope.dataState.kind !== 'inline') throw new Error('Expected inline fixture')
  envelope.dataState.datasets[0].rows = []
  const option = echartsOption(envelope)
  for (const [width, height] of [[0, 240], [320, Number.NaN], [Number.POSITIVE_INFINITY, 240]]) {
    expect(responsiveEChartsPatch(option, width, height, envelope)).toEqual({})
  }
  const patch = responsiveEChartsPatch(option, 320, 240, envelope)
  expect(patch.legend).toBeUndefined()
  for (const labelLinePoints of [undefined, [], [[Number.NaN, 10]]]) {
    const layout = patch.series[0].labelLayout({ dataIndex: Number.NaN, align: 'right', labelLinePoints })
    expect([layout.x, layout.y, layout.width].every(Number.isFinite)).toBe(true)
    expect(layout.labelLinePoints).toBeUndefined()
  }
})
