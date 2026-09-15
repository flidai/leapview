import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import type { InlineVisualizationDataState } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { EChartsHandle, echartsOption } from './echarts'
import { CategoryColorRegistry } from './echarts/category-colors'
import { hierarchyFixture, networkFixture } from './echarts-test-fixtures'

test('ECharts treemap and sunburst convert canonical decimal strings for layout and preserve raw tooltip values', () => {
  for (const mark of ['treemap', 'sunburst'] as const) {
    const envelope = hierarchyFixture(mark) as any
    ;(envelope.dataState as InlineVisualizationDataState).datasets[0].rows = [
      ['root', null, '15743364.25'],
      ['child', 'root', '2.50'],
    ]
    const option = echartsOption(envelope, defaultRendererContext) as any
    const root = option.series[0].data[0]
    const child = root.children[0]
    expect(root.value).toBe(15743364.25)
    expect(child.value).toBe(2.5)
    expect(root.__lv_raw_value).toBe('15743364.25')
    expect(child.__lv_raw_value).toBe('2.50')
    expect(option.series[0].tooltip.formatter({ data: root })).toBe('root: 15743364.25')
    expect(option.series[0].tooltip.formatter({ data: child })).toBe('child: 2.50')
    if (mark === 'treemap') expect(option.series[0]).toMatchObject({ animation: false, animationDurationUpdate: 0 })

    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 432, height: 414 })
    try {
      chart.setOption(option)
      const model = (chart as any).getModel().getSeriesByIndex(0)
      expect(model.getData().tree.root.getValue()).toBe(15743364.25)
      if (mark === 'treemap') {
        expect(model.get('animation')).toBe(false)
        expect(model.getData().tree.root.name).toBe('All')
        expect(option.legend.data).toEqual([])
      }
      expect(() => chart.renderToSVGString()).not.toThrow()
    } finally { chart.dispose() }

    const overflow = structuredClone(envelope) as any
    ;(overflow.dataState as InlineVisualizationDataState).datasets[0].rows[0][2] = `1${'0'.repeat(400)}`
    expect(() => echartsOption(overflow, defaultRendererContext)).toThrow(new RegExp(`${mark} value field "value".*overflows or underflows`))
  }
})

test('ECharts ignores zero-sized observer resizes before deferred hosts have a layout', () => {
  let resizeCalls = 0
  const chart = {
    on() {},
    off() {},
    resize() { resizeCalls++ },
  }
  const handle = new EChartsHandle(
    {} as HTMLElement,
    {} as HTMLElement,
    chart as any,
    new CategoryColorRegistry(),
  )

  handle.resize(0, 0)
  handle.resize(0, 320)
  handle.resize(320, 0)
  expect(resizeCalls).toBe(0)

  handle.resize(320, 180)
  expect(resizeCalls).toBe(1)
})

test('ECharts graph tooltips and dense automatic labels remain discoverable', () => {
  const graph = echartsOption(networkFixture('graph'), defaultRendererContext) as any
  expect(graph.series[0].tooltip.formatter({ data: graph.series[0].data[0] })).toBe('A')
  expect(graph.series[0].tooltip.formatter({ data: graph.series[0].links[0] })).toBe('A to B: 4')

  const denseGraphEnvelope = structuredClone(networkFixture('graph')) as any
  denseGraphEnvelope.dataState.datasets[0].rows = Array.from({ length: 12 }, (_, index) => [`Source ${index}`, `Target ${index}`, index + 1])
  denseGraphEnvelope.spec.presentation.labelPolicy.priority = []
  for (const layout of ['standard', 'circular'] as const) {
    denseGraphEnvelope.spec.presentation.layout = layout
    const denseGraph = echartsOption(denseGraphEnvelope, defaultRendererContext) as any
    expect(denseGraph.series[0].data).toHaveLength(24)
    expect(denseGraph.series[0].label.show).toBe(false)
    expect(denseGraph.series[0].emphasis).toMatchObject({ focus: 'adjacency', label: { show: true } })
    expect(denseGraph.series[0].labelLayout({ dataIndex: 0 })).toEqual({ hideOverlap: true })
  }
  const prioritizedGraph = structuredClone(denseGraphEnvelope) as any
  prioritizedGraph.spec.presentation.labelPolicy.priority = ['selected']
  prioritizedGraph.spec.datasets[0].fields[0].role = 'identity'
  prioritizedGraph.spec.datasets[0].fields[1].role = 'identity'
  prioritizedGraph.selection = [{
    datum: { dataset: 'primary', dataRevision: 1, identity: { source: 'Source 0', target: 'Target 0' } },
    label: 'Source 0 to Target 0',
  }]
  const prioritizedOption = echartsOption(prioritizedGraph, defaultRendererContext) as any
  expect(prioritizedOption.series[0].label.show).toBe(true)
  expect(prioritizedOption.series[0].label.formatter({ data: prioritizedOption.series[0].data[0] })).toBe('Source 0')
  const nonPriorityNode = prioritizedOption.series[0].data.find((node: any) => node.displayName === 'Source 1')
  expect(prioritizedOption.series[0].label.formatter({ data: nonPriorityNode })).toBe('')
  expect(prioritizedOption.series[0].labelLayout({ dataIndex: 0 })).toEqual({ hideOverlap: false })
  expect(prioritizedOption.series[0].emphasis.label.formatter({ data: nonPriorityNode })).toBe('Source 1')
})

test('ECharts tree survives empty, loaded, and cleared data frames', () => {
  const loaded = hierarchyFixture('tree')
  const empty = structuredClone(loaded)
  if (empty.dataState.kind !== 'inline') throw new Error('expected inline fixture')
  empty.dataState.datasets[0]!.rows = []
  for (const layout of ['standard', 'circular'] as const) {
    if (empty.spec.kind !== 'hierarchy' || loaded.spec.kind !== 'hierarchy') throw new Error('expected hierarchy fixture')
    empty.spec.presentation.layout = layout
    loaded.spec.presentation.layout = layout
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 364, height: 481 })
    try {
      for (const envelope of [empty, loaded, empty, loaded]) {
        expect(() => {
          chart.setOption(echartsOption(envelope), { notMerge: true })
          chart.renderToSVGString()
        }).not.toThrow()
      }
    } finally { chart.dispose() }
  }
})
