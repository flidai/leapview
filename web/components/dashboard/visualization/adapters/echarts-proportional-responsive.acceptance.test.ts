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

test('responsive cue labels retain native side-aware columns with and without a legend title', () => {
  const { envelope, spec } = cueFixture()
  for (const title of [undefined, 'Order status']) {
    spec.presentation.legendTitle = title
    for (const [width, height] of [[373, 282], [440, 360]]) {
      const series = responsiveEChartsPatch(echartsOption(envelope), width, height, envelope).series[0]
      expect(series.left).toBe(0)
      expect(series.right).toBe(0)
      expect(series.top).toBe(series.bottom)
      expect(series.labelLayout).toBeTypeOf('function')
      expect(series.labelLine.length).toBe(10)
      expect(series.labelLine.length2).toBe(8)
      const outer = series.radius[1]
      for (const side of ['left', 'right']) {
        const right = side === 'right'
        const anchor = [width / 2 + (right ? outer / 2 : -outer / 2), height / 2 - outer]
        const labelRect = right
          ? { x: width - 90, y: height / 2 - 12, width: 80, height: 24 }
          : { x: 10, y: height / 2 - 12, width: 80, height: 24 }
        const layout = series.labelLayout({ labelRect, labelLinePoints: [anchor, anchor, anchor] })
        expect(layout).toMatchObject({ hideOverlap: false })
        expect(layout.labelLinePoints).toHaveLength(5)
        expect(layout.labelLinePoints.flat().every(Number.isFinite)).toBe(true)
        const radial = layout.labelLinePoints[1]
        expect(Math.hypot(radial[0] - width / 2, radial[1] - height / 2)).toBeCloseTo(outer)
        const lane = layout.labelLinePoints[2][0]
        if (right) expect(lane).toBeGreaterThan(width / 2 + outer)
        else expect(lane).toBeLessThan(width / 2 - outer)
        expect(layout.labelLinePoints[3][1]).toBe(labelRect.y + labelRect.height / 2)
      }
      expect(series.radius[1]).toBeLessThan(Math.min(width, height - series.top - series.bottom) / 2)
    }
  }
})

test('responsive cue geometry stays finite at narrow widths and short titled heights', () => {
  const { envelope, spec } = cueFixture()
  spec.presentation.legendTitle = 'Order status'
  const narrow = responsiveEChartsPatch(echartsOption(envelope), 96, 180, envelope).series[0]
  expect(narrow.left).toBe(0)
  expect(narrow.right).toBe(0)
  expect(narrow.top).toBe(52)
  expect(narrow.bottom).toBe(52)
  expect(narrow.labelLayout).toBeTypeOf('function')
  expect(narrow.radius.every((value: unknown) => typeof value === 'number' && Number.isFinite(value) && value >= 0)).toBe(true)
  const narrowLayout = narrow.labelLayout({ labelRect: { x: 8, y: 72, width: 24, height: 24 }, labelLinePoints: [[12, 90], [12, 90], [12, 90]] })
  expect(narrowLayout.labelLinePoints.flat().every(Number.isFinite)).toBe(true)
  const short = responsiveEChartsPatch(echartsOption(envelope), 320, 150, envelope).series[0]
  expect(short.top).toBe(52)
  expect(short.bottom).toBe(52)
  expect(short.radius[1]).toBeLessThan((150 - short.top - short.bottom) / 2)

  const micro = responsiveEChartsPatch(echartsOption(envelope), 1, 120, envelope).series[0]
  expect(micro.left).toBe(0)
  expect(micro.right).toBe(0)
  expect(micro.radius.every((value: unknown) => typeof value === 'number' && Number.isFinite(value) && value >= 0)).toBe(true)
})

test('responsive rose cue leaders retain native guide semantics and finite radii', () => {
  const { envelope, spec } = cueFixture()
  spec.presentation.rose = true
  const width = 373, height = 282
  const series = responsiveEChartsPatch(echartsOption(envelope), width, height, envelope).series[0]
  expect(series.labelLayout).toBeTypeOf('function')
  expect(series.labelLine.length).toBe(10)
  expect(series.labelLine.length2).toBe(8)
  expect(series.radius.every((value: unknown) => typeof value === 'number' && Number.isFinite(value) && value >= 0)).toBe(true)
  const roseLayout = series.labelLayout({ labelRect: { x: 8, y: 80, width: 80, height: 24 }, labelLinePoints: [[width / 2 - 24, height / 2 - 24], [0, 0], [0, 0]] })
  expect(roseLayout.labelLinePoints).toHaveLength(5)
  const radial = roseLayout.labelLinePoints[1]
  expect(Math.hypot(radial[0] - width / 2, radial[1] - height / 2)).toBeCloseTo(series.radius[1])
  expect(roseLayout.labelLinePoints.flat().every(Number.isFinite)).toBe(true)
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

test('responsive cue layout stays finite for empty data without custom label geometry', () => {
  const { envelope } = cueFixture()
  if (envelope.dataState.kind !== 'inline') throw new Error('Expected inline fixture')
  envelope.dataState.datasets[0].rows = []
  const option = echartsOption(envelope)
  for (const [width, height] of [[0, 240], [320, Number.NaN], [Number.POSITIVE_INFINITY, 240]]) {
    expect(responsiveEChartsPatch(option, width, height, envelope)).toEqual({})
  }
  const patch = responsiveEChartsPatch(option, 320, 240, envelope)
  expect(patch.legend).toBeUndefined()
  expect(patch.series[0].labelLayout).toBeTypeOf('function')
  expect(patch.series[0].radius.every((value: unknown) => typeof value === 'number' && Number.isFinite(value) && value >= 0)).toBe(true)
  expect(patch.series[0].labelLayout({ labelLinePoints: [[Number.NaN, 10]] })).toEqual({ hideOverlap: false })
})
