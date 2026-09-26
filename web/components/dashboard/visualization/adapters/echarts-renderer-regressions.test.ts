import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import type { InlineVisualizationDataState } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { EChartsHandle, echartsOption, responsiveEChartsPatch } from './echarts'
import { CategoryColorRegistry } from './echarts/category-colors'
import { responsiveEChartsLayoutKey } from './echarts/view-state'
import { hierarchyFixture, networkFixture } from './echarts-test-fixtures'

test('expanded hierarchy and flow plots center their bounds without changing chart semantics', () => {
  for (const envelope of [hierarchyFixture('tree'), networkFixture('sankey')]) {
    if (envelope.spec.kind !== 'hierarchy') throw new Error('Expected hierarchy fixture')
    envelope.spec.presentation.orientation = 'horizontal'
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(responsiveEChartsLayoutKey(envelope, 700, 500)).not.toBe(responsiveEChartsLayoutKey(envelope, 1200, 720))
    const compact = (responsiveEChartsPatch(option, 320, 240).series ?? option.series)[0]
    const expanded = responsiveEChartsPatch(option, 1200, 720).series[0]
    expect(compact.left).toBe(option.series[0].left)
    expect(compact.right).toBe(option.series[0].right)
    expect(expanded.left).toBe(expanded.right)
    expect(expanded.orient).toBe(option.series[0].orient)
    expect(expanded.data).toBe(option.series[0].data)
  }
})

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

test('compact standard graph node labels stay inside the chart as it resizes', () => {
  const envelope = networkFixture('graph') as any
  envelope.spec.presentation.layout = 'standard'
  envelope.spec.presentation.labelPolicy.priority = []
  envelope.dataState.datasets[0].rows = [[
    'Origin node with a very long label', 'Destination node with a very long label', 12,
  ]]
  const source = echartsOption(envelope, defaultRendererContext) as any

  for (const [width, height] of [[320, 240], [620, 400]] as const) {
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height })
    try {
      chart.setOption({ ...source, ...responsiveEChartsPatch(source, width, height), animation: false })
      chart.renderToSVGString()
      const labels = chart.getZr().storage.getDisplayList()
        .filter((item: any) => item.type === 'tspan' && String(item.style?.text).includes('…'))
      expect(labels).toHaveLength(2)
      for (const item of labels) {
        const bounds = item.getBoundingRect().clone()
        const transform = item.getComputedTransform?.() ?? item.transform
        if (transform) bounds.applyTransform(transform)
        expect(bounds.x, `${item.style.text} should not clip on the left at ${width}px`).toBeGreaterThanOrEqual(0)
        expect(bounds.x + bounds.width, `${item.style.text} should not clip on the right at ${width}px`).toBeLessThanOrEqual(width)
      }
    } finally {
      chart.dispose()
    }
  }
})

test('compact circular graph node labels stay inside the chart around the full ring', () => {
  const envelope = networkFixture('graph') as any
  envelope.spec.presentation.layout = 'circular'
  envelope.spec.presentation.labelPolicy = { ...envelope.spec.presentation.labelPolicy, density: 'always', priority: [] }
  envelope.dataState.datasets[0].rows = Array.from({ length: 8 }, (_, index) => [
    `Origin ${index} with a very long label`, `Destination ${index} with a very long label`, index + 1,
  ])
  const source = echartsOption(envelope, defaultRendererContext) as any
  const compact = responsiveEChartsPatch(source, 320, 240)
  const compactLabel = compact.series[0].label
  for (let index = 0; index < source.series[0].data.length; index++) {
    const output = compactLabel.formatter({ dataIndex: index, data: source.series[0].data[index] })
    expect(Boolean(output), `node label ${index} should follow deterministic compact density`).toBe(index % 4 === 0)
  }
  expect(compact.series[0].emphasis.label).toMatchObject({ show: true })
  expect(compact.series[0].emphasis.label.formatter({ dataIndex: 1, data: source.series[0].data[1] })).toBe('Destination 0 with a ve…')
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 320, height: 240 })
  try {
    chart.setOption({ ...source, ...compact, animation: false })
    chart.renderToSVGString()
    const labels = chart.getZr().storage.getDisplayList()
      .filter((item: any) => item.type === 'tspan' && typeof item.style?.text === 'string' && item.style.text.length > 0)
    expect(labels).toHaveLength(4)
    const labelBounds = labels.map((item: any) => {
      const bounds = item.getBoundingRect().clone()
      const transform = item.getComputedTransform?.() ?? item.transform
      if (transform) bounds.applyTransform(transform)
      expect(bounds.x, `${item.style.text} should not clip on the left`).toBeGreaterThanOrEqual(0)
      expect(bounds.x + bounds.width, `${item.style.text} should not clip on the right`).toBeLessThanOrEqual(320)
      return { text: item.style.text, x: bounds.x, y: bounds.y, right: bounds.x + bounds.width, bottom: bounds.y + bounds.height }
    })
    for (let index = 0; index < labelBounds.length; index++) {
      for (const other of labelBounds.slice(index + 1)) {
        const label = labelBounds[index]!
        const overlaps = label.x < other.right && label.right > other.x && label.y < other.bottom && label.bottom > other.y
        expect(overlaps, `${label.text} overlaps ${other.text}`).toBe(false)
      }
    }
  } finally {
    chart.dispose()
  }
})

test('compact horizontal Sankey node labels stay within their authored truncation boxes', () => {
  const envelope = networkFixture('sankey') as any
  envelope.spec.presentation.orientation = 'horizontal'
  envelope.dataState.datasets[0].rows = [[
    'Origin node with a very long label', 'Destination node with a very long label', 12,
  ]]
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].label).toMatchObject({ width: 56, overflow: 'truncate', ellipsis: '…' })
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 320, height: 240 })
  try {
    chart.setOption({ ...option, animation: false })
    chart.renderToSVGString()
    const labels = chart.getZr().storage.getDisplayList()
      .filter((item: any) => item.type === 'tspan' && String(item.style?.text).includes('…'))
    expect(labels).toHaveLength(2)
    for (const item of labels) {
      const bounds = item.getBoundingRect().clone()
      const transform = item.getComputedTransform?.() ?? item.transform
      if (transform) bounds.applyTransform(transform)
      expect(bounds.x, `${item.style.text} should not clip on the left`).toBeGreaterThanOrEqual(0)
      expect(bounds.x + bounds.width, `${item.style.text} should not clip on the right`).toBeLessThanOrEqual(320)
    }
  } finally {
    chart.dispose()
  }
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

test('a single-category tree shows its value without an artificial parent or connector', () => {
  const envelope = hierarchyFixture('tree') as any
  envelope.dataState.datasets[0].rows = [['Base', null, '15743364.25']]
  expect(responsiveEChartsLayoutKey(envelope, 256, 105)).not.toBe(responsiveEChartsLayoutKey(envelope, 320, 240))
  for (const orientation of ['vertical', 'horizontal'] as const) {
    envelope.spec.presentation.orientation = orientation
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.series[0].data).toHaveLength(1)
    expect(option.series[0].data[0].name).toBe('Base')
    expect(option.series[0].label.formatter({ data: option.series[0].data[0] })).toContain('15743364.25')
    for (const [width, height] of [[256, 105], [320, 240], [1200, 720]] as const) {
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height })
      try {
        chart.setOption({ ...option, ...responsiveEChartsPatch(option, width, height), animation: false })
        const svg = chart.renderToSVGString()
        expect(svg).toContain('Base')
        expect(svg).toContain('15743364.25')
        expect(svg).not.toContain('>All</text>')
        const labels = chart.getZr().storage.getDisplayList()
          .filter((item: any) => item.type === 'tspan' && ['Base', '15743364.25'].includes(item.style?.text))
        expect(labels).toHaveLength(2)
        const bounds = labels.map((item: any) => {
          const rect = item.getBoundingRect().clone()
          const transform = item.getComputedTransform?.() ?? item.transform
          if (transform) rect.applyTransform(transform)
          expect(rect.x).toBeGreaterThanOrEqual(0)
          expect(rect.x + rect.width).toBeLessThanOrEqual(width)
          expect(rect.y).toBeGreaterThanOrEqual(0)
          expect(rect.y + rect.height).toBeLessThanOrEqual(height)
          return rect
        })
        expect(bounds[0]!.y + bounds[0]!.height).toBeLessThanOrEqual(bounds[1]!.y)
      } finally { chart.dispose() }
    }
  }
})
