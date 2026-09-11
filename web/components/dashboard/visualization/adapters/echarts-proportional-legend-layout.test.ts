import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'
import { proportionalLegendGeometry } from './echarts/proportional-legend-layout'
import { proportionalFixture } from './echarts-test-fixtures'

const rows = [
  ['delivered', 10], ['shipped', 9], ['canceled', 8], ['unavailable', 7],
  ['invoiced', 6], ['processing', 5], ['created', 4], ['approved', 3],
  ['A very long category label that needs truncation 09', 2],
  ['A very long category label that needs truncation 10', 1],
] as const
const shortRows = rows.slice(0, 8)

function legendFixture(theme: 'light' | 'dark', fixtureRows: readonly (readonly [string, number])[] = rows) {
  const envelope = proportionalFixture('donut')
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') throw new Error('Expected proportional inline fixture')
  envelope.spec.presentation.legend = 'bottom'
  envelope.spec.presentation.legendTitle = undefined
  envelope.spec.presentation.legendItems = [{ value: 'delivered', label: 'Delivered & urgent' }]
  envelope.spec.conditionalFormatting = [{
    id: 'status-cues', target: 'mark_fill', field: envelope.spec.value,
    rule: { kind: 'rules', rules: [{ operator: 'greater_or_equal', value: 0, style: { icon: 'circle' } }], nullStyle: { icon: 'circle' }, defaultStyle: { icon: 'circle' } },
  }]
  envelope.dataState.datasets[0].rows = fixtureRows.map((row) => [...row])
  const context = {
    ...defaultRendererContext,
    theme,
    colors: theme === 'dark'
      ? { ...defaultRendererContext.colors, foreground: '#f0f6fc', muted: '#8b949e', surface: '#0d1117' }
      : defaultRendererContext.colors,
  }
  return { envelope, source: echartsOption(envelope, context) as Record<string, any> }
}

function pageItems(chart: any): { names: string[]; clipped: boolean; next?: number } {
  const model = chart.getModel().getComponent('legend')
  const view = chart.getViewOfComponentModel(model)
  const content = view.getContentGroup()
  const clipSize = view._containerGroup.__rectSize
  const names: string[] = []
  let clipped = false
  for (const item of content.children()) {
    const rect = item.getBoundingRect()
    const start = content.x + item.x + rect.x
    const end = start + rect.width
    const intersects = end > 0 && start < clipSize
    if (intersects) {
      const dataIndex = item.__legendDataIndex
      names.push(model.getData()[dataIndex].get('name'))
      clipped ||= start < -0.001 || end > clipSize + 0.001
    }
  }
  const info = view._getPageInfo(model)
  return { names, clipped, ...(info.pageNextDataIndex == null ? {} : { next: info.pageNextDataIndex }) }
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [320, 395, 600] as const) {
    test(`proportional bottom legend keeps every native scroll page whole at ${width}px in ${theme}`, () => {
      const { source } = legendFixture(theme)
      const sourceLegend = source.legend as Record<string, any>
      const patch = proportionalLegendGeometry(sourceLegend, width)
      expect(patch).toMatchObject({
        type: 'scroll', orient: 'horizontal', itemWidth: 12, itemHeight: 12,
        textStyle: { fontFamily: sourceLegend.textStyle.fontFamily, overflow: 'truncate', ellipsis: '…' },
        tooltip: { show: true },
      })
      expect(patch.data).toBeUndefined()
      expect(patch.formatter).toBeUndefined()
      expect(patch.selected).toBeUndefined()
      expect(patch.scrollDataIndex).toBeUndefined()
      expect(patch.width).toBeInteger()
      expect(patch.width).toBeLessThanOrEqual(width - 16)

      // Start narrow, then exercise the same native instance at wider sizes so
      // the geometry patch cannot accidentally reset native selection/page
      // state while the legend is resized.
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 320, height: 260 })
      try {
        chart.setOption({ ...source, animation: false })
        chart.dispatchAction({ type: 'legendUnSelect', name: rows[0][0] })
        chart.renderToSVGString()
        const initialModel = chart.getModel().getComponent('legend')
        const initialView = chart.getViewOfComponentModel(initialModel)
        const initialNext = initialView._getPageInfo(initialModel).pageNextDataIndex
        expect(initialNext).toBeDefined()
        chart.dispatchAction({ type: 'legendScroll', legendId: initialModel.id, scrollDataIndex: initialNext })
        if (width !== 320) chart.resize({ width, height: 260 })
        chart.setOption({ legend: patch })
        expect(chart.getModel().getComponent('legend').get('scrollDataIndex', true)).toBe(initialNext)
        chart.dispatchAction({ type: 'legendScroll', legendId: chart.getModel().getComponent('legend').id, scrollDataIndex: 0 })
        chart.renderToSVGString()
        const firstItem = chart.getViewOfComponentModel(chart.getModel().getComponent('legend')).getContentGroup().children()[0]
        const firstLabel = firstItem.children().find((child: any) => typeof child.style?.text === 'string')
        expect(firstLabel?.style.text).toBe('Delivered & urgent')
        expect((patch.tooltip.formatter as (params: { name: string }) => string)({ name: 'delivered' })).toBe('Delivered &amp; urgent')

        const visited = new Set<string>()
        for (let page = 0; page < rows.length + 1; page++) {
          chart.renderToSVGString()
          const current = pageItems(chart)
          expect(current.clipped).toBe(false)
          current.names.forEach((name) => visited.add(name))
          if (current.next === undefined) break
          chart.dispatchAction({
            type: 'legendScroll',
            legendId: chart.getModel().getComponent('legend').id,
            scrollDataIndex: current.next,
          })
        }
        expect([...visited].sort()).toEqual(rows.map(([name]) => name).sort())
        expect(chart.getModel().getComponent('legend').isSelected(rows[0][0])).toBe(false)
      } finally {
        chart.dispose()
      }
    })
  }
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [320, 395, 600] as const) {
    test(`eight short proportional labels reserve whole slots at ${width}px in ${theme}`, () => {
      const { source } = legendFixture(theme, shortRows)
      const patch = proportionalLegendGeometry(source.legend as Record<string, any>, width)
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 320, height: 260 })
      try {
        chart.setOption({ ...source, animation: false })
        if (width !== 320) chart.resize({ width, height: 260 })
        chart.setOption({ legend: patch })
        const visited = new Set<string>()
        for (let page = 0; page < shortRows.length + 1; page++) {
          chart.renderToSVGString()
          const current = pageItems(chart)
          expect(current.clipped).toBe(false)
          current.names.forEach((name) => visited.add(name))
          if (current.next === undefined) break
          chart.dispatchAction({
            type: 'legendScroll',
            legendId: chart.getModel().getComponent('legend').id,
            scrollDataIndex: current.next,
          })
        }
        expect([...visited].sort()).toEqual(shortRows.map(([name]) => name).sort())
      } finally {
        chart.dispose()
      }
    })
  }
}

test('does not emit geometry when the native pager cannot fit', () => {
  const { source } = legendFixture('light')
  expect(proportionalLegendGeometry(source.legend as Record<string, any>, 96)).toEqual({})
  const narrowPatch = proportionalLegendGeometry(source.legend as Record<string, any>, 112)
  expect(narrowPatch.width).toBeLessThanOrEqual(96)
  expect(narrowPatch.textStyle.width).toBeGreaterThanOrEqual(1)
})
