import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import { defaultRendererContext } from '../host-controller'
import { echartsOption, responsiveEChartsPatch } from './echarts'
import { proportionalFixture } from './echarts-test-fixtures'

const statuses = [
  ['delivered', 96478], ['shipped', 1107], ['canceled', 625], ['unavailable', 609],
  ['invoiced', 314], ['processing', 301], ['created', 5], ['approved', 2],
] as const

function cueFixture(mark: 'pie' | 'donut', rose: boolean, rows: readonly (readonly [unknown, unknown])[]) {
  const envelope = proportionalFixture(mark)
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') throw new Error('Expected proportional inline fixture')
  envelope.spec.presentation.legend = 'bottom'
  envelope.spec.presentation.legendTitle = 'Status'
  envelope.spec.presentation.rose = rose
  envelope.spec.conditionalFormatting = [{
    id: 'status-cues', target: 'series_color', field: envelope.spec.value,
    rule: {
      kind: 'field', source: envelope.spec.category,
      values: Object.fromEntries(rows.map(([category]) => [String(category), { icon: 'circle' }])),
      nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'circle' },
    },
  }]
  envelope.dataState.datasets[0].rows = rows.map(([category, value]) => [category, value])
  return envelope
}

function renderStress(envelope: ReturnType<typeof cueFixture>, width: number, height: number) {
  const source = echartsOption(envelope, defaultRendererContext) as Record<string, any>
  const before = JSON.stringify(source)
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height })
  chart.setOption({ ...source, animation: false })
  chart.setOption(responsiveEChartsPatch(source, width, height, envelope))
  chart.renderToSVGString()
  expect(JSON.stringify(source)).toBe(before)
  return { chart, series: chart.getModel().getSeriesByIndex(0), width, height }
}

function assertFiniteGeometry(series: any, width: number, height: number, requireClearance: boolean) {
  const data = series.getData()
  const configuredRadius = series.get('radius')
  const layout = data.getItemLayout(0)
  const outerValues = Array.from({ length: data.count() }, (_, index) => data.getItemLayout(index)?.r ?? 0).filter(Number.isFinite)
  const configuredOuter = Array.isArray(configuredRadius) && Number.isFinite(configuredRadius[1]) ? configuredRadius[1] : 0
  const outer = Math.max(configuredOuter, ...outerValues)
  let visibleLabels = 0
  for (let index = 0; index < data.count(); index++) {
    const element = data.getItemGraphicEl(index)
    const label = element.getTextContent()
    if (label.ignore) continue
    visibleLabels++
    const bounds = label.getBoundingRect().clone()
    const transform = label.getComputedTransform()
    if (transform) bounds.applyTransform(transform)
    expect([bounds.x, bounds.y, bounds.width, bounds.height].every(Number.isFinite)).toBe(true)
    expect(bounds.x).toBeGreaterThanOrEqual(0)
    expect(bounds.x + bounds.width).toBeLessThanOrEqual(width)
    const points = element.getTextGuideLine()?.shape.points
    if (!points) continue
    expect(points.flat().every(Number.isFinite)).toBe(true)
    if (points.length >= 3) {
      const radial = points[1]
      const radialDistance = Math.hypot(radial[0] - layout.cx, radial[1] - layout.cy)
      expect(radialDistance).toBeCloseTo(outer)
      const lane = points[2][0]
      if (points[0][0] >= layout.cx) expect(lane).toBeGreaterThan(layout.cx + outer)
      else expect(lane).toBeLessThan(layout.cx - outer)
    }
    if (requireClearance) {
      const closestX = Math.max(bounds.x, Math.min(layout.cx, bounds.x + bounds.width))
      const closestY = Math.max(bounds.y, Math.min(layout.cy, bounds.y + bounds.height))
      expect(Math.hypot(closestX - layout.cx, closestY - layout.cy)).toBeGreaterThanOrEqual(outer - 1)
      expect(bounds.y + bounds.height).toBeLessThanOrEqual(height - 52)
    }
  }
  return visibleLabels
}

test('status cue stress keeps pie and rose labels finite, clear, and stable', () => {
  for (const [mark, rose] of [['pie', false], ['pie', true], ['donut', true]] as const) {
    const { chart, series, width, height } = renderStress(cueFixture(mark, rose, statuses), 395, 340)
    try {
      expect(series.id).toBe(`series:primary:${mark}`)
      expect(series.getData().count()).toBe(statuses.length)
      expect(assertFiniteGeometry(series, width, height, true)).toBe(statuses.length)
    } finally {
      chart.dispose()
    }
  }
})

test('status cue stress renders zero values safely and omits null and absent sectors', () => {
  for (const rows of [[], [['missing', null], ['blank', 0], ['zero', 0]]] as const) {
    const { chart, series, width, height } = renderStress(cueFixture('donut', true, rows), 395, 340)
    try {
      expect(series.id).toBe('series:primary:donut')
      expect(assertFiniteGeometry(series, width, height, rows.length > 0)).toBe(rows.length === 0 ? 0 : 2)
    } finally {
      chart.dispose()
    }
  }
})
