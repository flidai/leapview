import { expect, test } from 'bun:test'
import * as echarts from 'echarts'
import { defaultRendererContext } from '../host-controller'
import { echartsOption, responsiveEChartsPatch } from './echarts'
import { proportionalFixture } from './echarts-test-fixtures'

const statuses = [
  ['delivered', 96478], ['shipped', 1107], ['canceled', 625], ['unavailable', 609],
  ['invoiced', 314], ['processing', 301], ['created', 5], ['approved', 2],
] as const

function statusFixture() {
  const envelope = proportionalFixture('donut')
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') throw new Error('Expected proportional inline fixture')
  const spec = envelope.spec
  spec.presentation.legend = 'bottom'
  spec.presentation.rose = false
  spec.datasets[0].fields[1].format = { kind: 'compact', maximumFractionDigits: 3 }
  spec.conditionalFormatting = [{
    id: 'status-cues', target: 'series_color', field: spec.value,
    rule: {
      kind: 'field', source: spec.category,
      values: {
        delivered: { icon: 'circle' }, shipped: { icon: 'arrow_up' },
        canceled: { icon: 'warning' }, unavailable: { icon: 'warning' },
        invoiced: { icon: 'diamond' }, processing: { icon: 'circle' },
        created: { icon: 'square' }, approved: { icon: 'circle' },
      },
      nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'circle' },
    },
  }]
  envelope.dataState.datasets[0].rows = statuses.map((row) => [...row])
  return envelope
}

test('status cue formatter keeps category punctuation on the cue line', () => {
  const source = echartsOption(statusFixture()) as any
  expect(source.series[0].label.formatter({ value: ['North: East', 12], dataIndex: 0 })).toBe('● North: East\n0.012K')
})

test('status donut preserves native legend selection and series identity through resizes', () => {
  const envelope = statusFixture()
  const source = echartsOption(envelope)
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 320, height: 266 })
  try {
    chart.setOption({ ...source, animation: false })
    chart.setOption(responsiveEChartsPatch(source, 320, 266, envelope))
    const series = chart.getModel().getSeriesByIndex(0)
    chart.dispatchAction({ type: 'legendUnSelect', name: 'delivered' })
    for (const [width, height] of [[395, 304], [320, 266], [600, 360]]) {
      chart.resize({ width, height })
      const patch = responsiveEChartsPatch(source, width, height, envelope)
      expect(patch.legend?.data).toBeUndefined()
      expect(patch.legend?.selected).toBeUndefined()
      expect(patch.legend?.scrollDataIndex).toBeUndefined()
      chart.setOption(patch)
      chart.renderToSVGString()
      expect(chart.getModel().getSeriesByIndex(0)).toBe(series)
      expect(chart.getModel().getComponent('legend').isSelected('delivered')).toBe(false)
      expect(series.getData().count()).toBe(7)
    }
    chart.dispatchAction({ type: 'legendSelect', name: 'delivered' })
    chart.renderToSVGString()
    expect(series.getData().count()).toBe(8)
    expect(series.getData().getName(0)).toBe('delivered')
  } finally {
    chart.dispose()
  }
})

// Inspect rendered text/sector geometry, not the layout callback's proposed
// coordinates: ECharts may wrap and move labels after that callback runs.
for (const [width, height] of [[320, 266], [395, 304], [395, 328]]) {
  for (const theme of ['light', 'dark'] as const) {
    test(`status donut retains readable cues and ring at ${width}×${height} in ${theme}`, () => {
      const envelope = statusFixture()
      const context = {
        ...defaultRendererContext, theme,
        colors: theme === 'dark'
          ? { ...defaultRendererContext.colors, foreground: '#f0f6fc', muted: '#8b949e', surface: '#0d1117' }
          : defaultRendererContext.colors,
      }
      const source = echartsOption(envelope, context)
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height })
      try {
        chart.setOption({ ...source, animation: false })
        chart.setOption(responsiveEChartsPatch(source, width, height, envelope))
        chart.renderToSVGString()
        const series = chart.getModel().getSeriesByIndex(0)
        const data = series.getData()
        expect(series.id).toBe('series:primary:donut')
        expect(data.count()).toBe(statuses.length)
        const labels = statuses.map(([category], index) => {
          const sector = data.getItemGraphicEl(index)
          const label = sector.getTextContent()!
          const bounds = label.getBoundingRect().clone()
          const transform = label.getComputedTransform()
          if (transform) bounds.applyTransform(transform)
          expect(label.ignore, `${category} must remain visible`).toBe(false)
          expect(label.style.lineHeight, `${category} uses an explicit readable line height`).toBe(16)
          expect(String(label.style.text), `${category} keeps category and value on intentional lines`).toContain('\n')
          const expectedText = source.series[0].label.formatter({ value: [...statuses[index]], dataIndex: index })
          expect(String(label.style.text).replace(/\s/g, ''), `${category} retains its icon and formatted value`).toBe(String(expectedText).replace(/\s/g, ''))
          expect(String(label.style.text).replace(/\s/g, '')).toContain(category)
          expect(String(label.style.text)).not.toContain('…')
          expect(bounds.x, `${category} left`).toBeGreaterThanOrEqual(0)
          expect(bounds.x + bounds.width, `${category} right`).toBeLessThanOrEqual(width)
          expect(bounds.y, `${category} top`).toBeGreaterThanOrEqual(0)
          expect(bounds.y + bounds.height, `${category} clears legend`).toBeLessThanOrEqual(height - 28)
          expect(bounds.height, `${category} uses two readable lines`).toBe(32)
          const ring = data.getItemLayout(index)
          const nearestX = Math.max(bounds.x, Math.min(ring.cx, bounds.x + bounds.width))
          const nearestY = Math.max(bounds.y, Math.min(ring.cy, bounds.y + bounds.height))
          expect(Math.hypot(nearestX - ring.cx, nearestY - ring.cy), `${category} must not cover the ring`).toBeGreaterThanOrEqual(ring.r)
          const points = sector.getTextGuideLine()!.shape.points
          for (let point = 1; point < points.length; point++) {
            const a = points[point - 1], b = points[point]
            const dx = b[0] - a[0], dy = b[1] - a[1]
            const lengthSquared = dx * dx + dy * dy
            const fraction = lengthSquared === 0 ? 0 : Math.max(0, Math.min(1, ((ring.cx - a[0]) * dx + (ring.cy - a[1]) * dy) / lengthSquared))
            const distance = Math.hypot(a[0] + fraction * dx - ring.cx, a[1] + fraction * dy - ring.cy)
            expect(distance, `${category} leader must not cross unrelated sectors`).toBeGreaterThanOrEqual(ring.r - 0.5)
          }
          return { bounds, side: points[0][0] >= width / 2 ? 'right' : 'left', lane: points[2][0] }
        })
        for (let index = 0; index < labels.length; index++) {
          for (let other = index + 1; other < labels.length; other++) {
            const a = labels[index], b = labels[other]
            const overlap = a.bounds.x < b.bounds.x + b.bounds.width && a.bounds.x + a.bounds.width > b.bounds.x && a.bounds.y < b.bounds.y + b.bounds.height && a.bounds.y + a.bounds.height > b.bounds.y
            expect(overlap, `${statuses[index][0]} overlaps ${statuses[other][0]}`).toBe(false)
          }
        }
        for (const side of ['left', 'right'] as const) {
          const sideLabels = labels.filter((label) => label.side === side).sort((a, b) => a.bounds.y - b.bounds.y)
          const lanes = new Set(sideLabels.map((label) => label.lane))
          expect(lanes.size, `${side} guide lanes should be staggered`).toBe(sideLabels.length)
          for (let index = 1; index < sideLabels.length; index++) {
            const gap = sideLabels[index].bounds.y - (sideLabels[index - 1].bounds.y + sideLabels[index - 1].bounds.height)
            expect(gap, `${side} labels retain an inter-label gap`).toBeGreaterThanOrEqual(1)
          }
        }
        const dominant = data.getItemLayout(0)
        expect(dominant.r * 2, 'ring must not become a tiny center ornament').toBeGreaterThanOrEqual(width * 0.36)
        const dominantLabelY = labels[0].bounds.y + labels[0].bounds.height / 2
        const dominantAnchorY = data.getItemGraphicEl(0).getTextGuideLine()!.shape.points[0][1]
        expect(Math.abs(dominantLabelY - dominantAnchorY), 'dominant label should remain near its sector anchor').toBeLessThan(height * 0.2)
      } finally {
        chart.dispose()
      }
    })
  }
}
