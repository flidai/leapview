import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import { defaultRendererContext } from '../host-controller'
import { echartsOption, responsiveEChartsPatch } from './echarts'
import { compactScrollLegendGeometry } from './echarts/compact-scroll-legend'
import { proportionalFixture } from './echarts-test-fixtures'

const rows = [
  ['delivered', 10], ['shipped', 9], ['canceled', 8], ['unavailable', 7],
  ['invoiced', 6], ['processing', 5], ['created', 4], ['approved', 3],
] as const

function legendFixture(theme: 'light' | 'dark') {
  const envelope = proportionalFixture('donut')
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') throw new Error('Expected proportional inline fixture')
  envelope.spec.presentation.legend = 'bottom'
  envelope.spec.presentation.legendItems = [{ value: 'delivered', label: 'Delivered & urgent' }]
  envelope.dataState.datasets[0].rows = rows.map((row) => [...row])
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
    if (end > 0 && start < clipSize) {
      names.push(model.getData()[item.__legendDataIndex].get('name'))
      clipped ||= start < -0.001 || end > clipSize + 0.001
    }
  }
  const info = view._getPageInfo(model)
  return { names, clipped, ...(info.pageNextDataIndex == null ? {} : { next: info.pageNextDataIndex }) }
}

for (const theme of ['light', 'dark'] as const) {
  for (const width of [294, 364] as const) {
    test(`compact proportional legend keeps every page whole at ${width}px in ${theme}`, () => {
      const { source } = legendFixture(theme)
      const patch = responsiveEChartsPatch(source, width, 322)
      expect(patch.legend).toMatchObject({
        type: 'scroll', orient: 'horizontal', itemWidth: 12, itemHeight: 12,
        textStyle: { overflow: 'truncate', ellipsis: '…' }, tooltip: { show: true },
      })
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height: 322 })
      try {
        chart.setOption({ ...source, ...patch, animation: false })
        chart.dispatchAction({ type: 'legendUnSelect', name: rows[0][0] })
        const visited = new Set<string>()
        for (let page = 0; page <= rows.length; page++) {
          chart.renderToSVGString()
          const current = pageItems(chart)
          expect(current.clipped).toBe(false)
          current.names.forEach((name) => visited.add(name))
          if (current.next === undefined) break
          chart.dispatchAction({
            type: 'legendScroll',
            legendId: (chart as any).getModel().getComponent('legend').id,
            scrollDataIndex: current.next,
          })
        }
        expect([...visited].sort()).toEqual(rows.map(([name]) => name).sort())
        expect((chart as any).getModel().getComponent('legend').isSelected(rows[0][0])).toBe(false)
      } finally {
        chart.dispose()
      }
    })
  }
}

test('compact legend geometry fails closed when native paging cannot be measured', () => {
  expect(compactScrollLegendGeometry(undefined, 320)).toEqual({})
  expect(compactScrollLegendGeometry({ type: 'plain' }, 320)).toEqual({})
  expect(compactScrollLegendGeometry({ type: 'scroll', orient: 'horizontal', formatter: () => '', data: [] }, 320)).toEqual({})
})

test('growing a compact proportional legend restores unrestricted text and selection', () => {
  const { source } = legendFixture('light')
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 294, height: 322 })
  try {
    chart.setOption({ ...source, ...responsiveEChartsPatch(source, 294, 322), animation: false })
    chart.dispatchAction({ type: 'legendUnSelect', name: rows[0][0] })
    chart.resize({ width: 900, height: 500 })
    chart.setOption(responsiveEChartsPatch(source, 900, 500))
    chart.renderToSVGString()
    const legend = (chart as any).getModel().getComponent('legend')
    expect(legend.isSelected(rows[0][0])).toBe(false)
    expect(legend.get('width')).toBe('auto')
    expect(legend.get(['textStyle', 'width'], true)).toBeNull()
    expect(legend.get(['textStyle', 'overflow'], true)).toBeNull()
  } finally {
    chart.dispose()
  }
})
