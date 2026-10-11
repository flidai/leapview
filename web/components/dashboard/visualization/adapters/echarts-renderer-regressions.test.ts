import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import type { InlineVisualizationDataState } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { EChartsHandle, echartsOption, responsiveEChartsPatch } from './echarts'
import { CategoryColorRegistry } from './echarts/category-colors'
import { responsiveEChartsLayoutKey } from './echarts/view-state'
import { cartesianFixture, hierarchyFixture, networkFixture, proportionalFixture } from './echarts-test-fixtures'

test('expanded hierarchy and flow plots center their bounds without changing chart semantics', () => {
  for (const envelope of [hierarchyFixture('tree'), networkFixture('sankey')]) {
    if (envelope.spec.kind !== 'hierarchy') throw new Error('Expected hierarchy fixture')
    envelope.spec.presentation.orientation = 'horizontal'
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(responsiveEChartsLayoutKey(envelope, 700, 500)).not.toBe(responsiveEChartsLayoutKey(envelope, 1200, 720))
    const compact = (responsiveEChartsPatch(option, 320, 240).series ?? option.series)[0]
    const expanded = responsiveEChartsPatch(option, 1200, 720).series[0]
    if (envelope.spec.mark === 'sankey') {
      expect(compact.left).toBe(option.series[0].left)
      expect(compact.right).toBe(option.series[0].right)
    } else {
      expect(compact.left).toBeGreaterThanOrEqual(Number.parseFloat(option.series[0].left) * 3.2)
      expect(compact.right).toBeGreaterThanOrEqual(Number.parseFloat(option.series[0].right) * 3.2)
      expect(compact.data).toBe(option.series[0].data)
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 320, height: 240 })
      try {
        chart.setOption({ ...option, ...responsiveEChartsPatch(option, 320, 240), animation: false })
        chart.renderToSVGString()
        const layout = (chart as any).getModel().getSeriesByIndex(0).layoutInfo
        expect(layout.width).toBeGreaterThan(96)
        expect(layout.height).toBeGreaterThan(72)
        const labels = chart.getZr().storage.getDisplayList().filter((item: any) => item.type === 'tspan' && item.style?.text)
        expect(labels.length).toBe(2)
        for (const label of labels) {
          const bounds = label.getBoundingRect().clone()
          const transform = label.getComputedTransform?.() ?? label.transform
          if (transform) bounds.applyTransform(transform)
          expect(bounds.x).toBeGreaterThanOrEqual(0)
          expect(bounds.x + bounds.width).toBeLessThanOrEqual(320)
        }
      } finally { chart.dispose() }
    }
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

test('duplicate positive observer sizes do not render the chart again', () => {
  const sizes: Array<{ width: number; height: number }> = []
  const chart = { on() {}, off() {}, resize(size: { width: number; height: number }) { sizes.push(size) } }
  const handle = new EChartsHandle({} as HTMLElement, {} as HTMLElement, chart as any, new CategoryColorRegistry())
  handle.resize(320, 180)
  handle.resize(320, 180)
  handle.resize(480, 180)
  expect(sizes).toHaveLength(2)
  expect(sizes.map(({ width, height }) => [width, height])).toEqual([[320, 180], [480, 180]])
})


test('dense Sankey nodes remain inside the plot through resizing and recover authored spacing', () => {
  for (const orientation of ['horizontal', 'vertical'] as const) {
    for (const authoredGap of [undefined, 18]) {
      const envelope = networkFixture('sankey') as any
      envelope.spec.presentation.orientation = orientation
      envelope.spec.presentation.nodeGap = authoredGap
      envelope.dataState.datasets[0].rows = Array.from({ length: 50 }, (_, index) => [
        `Product category ${index}`, `Delivery status ${index % 8}`, index + 1,
      ])
      const option = echartsOption(envelope, defaultRendererContext) as any
      const source = option.series[0]
      const initialGap = authoredGap ?? 8
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 1400, height: 1400 })
      // Execute lazy responsive patches immediately in SSR, where there is no
      // browser animation frame to flush them between observer notifications.
      const setOption = chart.setOption.bind(chart)
      chart.setOption = ((value: any, settings: any) => setOption(value, { ...settings, lazyUpdate: false })) as typeof chart.setOption
      const handle = new EChartsHandle({} as HTMLElement, {} as HTMLElement, chart, new CategoryColorRegistry())
      try {
        handle.mount(envelope, defaultRendererContext)
        const widths = orientation === 'vertical' ? [[760, 760], [500, 760], [432, 457], [375, 457], [1400, 1400]]
          : [[760, 760], [760, 400], [432, 457], [432, 375], [1400, 1400]]
        for (const [width, height] of widths) {
          handle.resize(width!, height!)
          const model = (chart as any).getModel().getSeriesByIndex(0)
          const data = model.getData()
          expect(data.count()).toBe(58)
          for (let index = 0; index < data.count(); index++) {
            const node = data.getItemLayout(index)
            expect(node.dx).toBeGreaterThan(0)
            expect(node.dy).toBeGreaterThan(0)
            expect(node.x).toBeGreaterThanOrEqual(-0.001)
            expect(node.y).toBeGreaterThanOrEqual(-0.001)
            expect(node.x + node.dx).toBeLessThanOrEqual(model.layoutInfo.width + 0.001)
            expect(node.y + node.dy).toBeLessThanOrEqual(model.layoutInfo.height + 0.001)
          }
          const responsive = (responsiveEChartsPatch(option, width!, height!).series ?? option.series)[0]
          expect(responsive.data).toBe(source.data)
          expect(responsive.links).toBe(source.links)
          expect(responsive.tooltip).toBe(source.tooltip)
          expect(source.nodeGap).toBe(authoredGap)
          expect(model.get('nodeGap')).toBeLessThanOrEqual(initialGap)
          if (width === 1400) expect(model.get('nodeGap')).toBe(initialGap)
          expect(() => chart.renderToSVGString()).not.toThrow()
        }
        const wideKey = responsiveEChartsLayoutKey(envelope, 760, 760, false, option)
        const shrunkKey = responsiveEChartsLayoutKey(envelope, orientation === 'vertical' ? 500 : 760,
          orientation === 'vertical' ? 760 : 400, false, option)
        expect(wideKey).not.toBe(shrunkKey)
      } finally { handle.dispose() }
    }
  }
})


test('long funnel side legends remain bounded and separate from plot labels', () => {
  for (const side of ['left', 'right'] as const) {
    const envelope = proportionalFixture('funnel') as any
    envelope.spec.presentation.legend = side
    envelope.dataState.datasets[0].rows = Array.from({ length: 8 }, (_, index) => [
      `Region ${index + 1} — enterprise customers and strategic accounts with an unusually long name`, 100 - index * 10,
    ])
    const option = echartsOption(envelope, defaultRendererContext) as any
    for (const [width, height] of [[351, 384], [768, 384], [1416, 384]]) {
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height })
      try {
        const patch = responsiveEChartsPatch(option, width!, height!) as any
        chart.setOption({ ...option, ...patch, animation: false })
        chart.renderToSVGString()
        const labels = chart.getZr().storage.getDisplayList().filter((item: any) =>
          item.type === 'tspan' && typeof item.style?.text === 'string' && item.style.text.includes('Region'))
        expect(labels.length).toBeGreaterThan(0)
        const bounds = labels.flatMap((label: any) => {
          const bounds = label.getBoundingRect().clone()
          const transform = label.getComputedTransform?.() ?? label.transform
          if (transform) bounds.applyTransform(transform)
          // Scroll legends retain offscreen pages in the display list. Only
          // visible text contributes to the rendered containment contract.
          for (const clip of label.__clipPaths ?? []) {
            const clipBounds = clip.getBoundingRect().clone()
            const clipTransform = clip.getComputedTransform?.() ?? clip.transform
            if (clipTransform) clipBounds.applyTransform(clipTransform)
            if (bounds.x >= clipBounds.x + clipBounds.width || bounds.x + bounds.width <= clipBounds.x
              || bounds.y >= clipBounds.y + clipBounds.height || bounds.y + bounds.height <= clipBounds.y) return []
          }
          expect(bounds.x, label.style.text).toBeGreaterThanOrEqual(-0.001)
          expect(bounds.x + bounds.width, label.style.text).toBeLessThanOrEqual(width! + 0.001)
          expect(bounds.y + bounds.height, label.style.text).toBeLessThanOrEqual(height! + 0.001)
          return [{ ...bounds, text: label.style.text }]
        })
        for (let index = 0; index < bounds.length; index++) {
          for (const other of bounds.slice(index + 1)) {
            const label = bounds[index]!
            const overlapWidth = Math.min(label.x + label.width, other.x + other.width) - Math.max(label.x, other.x)
            const overlapHeight = Math.min(label.y + label.height, other.y + other.height) - Math.max(label.y, other.y)
            expect(overlapWidth > 1 && overlapHeight > 1, `${label.text} overlaps ${other.text}`).toBe(false)
          }
        }
        expect(chart.getZr().storage.getDisplayList().some((label: any) => label.type === 'tspan' && /\b100\b/.test(String(label.style?.text)))).toBe(true)
        expect(patch.legend.data).toBe(option.legend.data)
        expect(patch.legend.formatter).toBe(option.legend.formatter)
        expect(patch.legend.tooltip.formatter({ name: option.legend.data[0].name })).toContain('unusually long name')
        const data = (chart as any).getModel().getSeriesByIndex(0).getData()
        for (let index = 0; index < data.count(); index++) {
          const points = data.getItemLayout(index).points
          expect(points.every((point: number[]) => point.every(Number.isFinite))).toBe(true)
          expect(Math.max(...points.map((point: number[]) => point[0]!)) - Math.min(...points.map((point: number[]) => point[0]!))).toBeGreaterThan(0)
          expect(Math.max(...points.map((point: number[]) => point[1]!)) - Math.min(...points.map((point: number[]) => point[1]!))).toBeGreaterThan(0)
        }
      } finally { chart.dispose() }
    }
  }
})


test('funnel resize restores fitting authored side legends and retains native selection', () => {
  for (const side of ['left', 'right'] as const) {
    for (const longNames of [false, true]) {
      const envelope = proportionalFixture('funnel') as any
      envelope.spec.presentation.legend = side
      envelope.dataState.datasets[0].rows = [
        [longNames ? 'North — enterprise customers and strategic accounts with an unusually long name' : 'North', 100],
        ['South', 50],
      ]
      const option = echartsOption(envelope, defaultRendererContext) as any
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 3000, height: 500 })
      const setOption = chart.setOption.bind(chart)
      chart.setOption = ((value: any, settings: any) => setOption(value, { ...settings, lazyUpdate: false })) as typeof chart.setOption
      const handle = new EChartsHandle({} as HTMLElement, {} as HTMLElement, chart, new CategoryColorRegistry())
      try {
        handle.mount(envelope, defaultRendererContext)
        handle.resize(3000, 500)
        expect((chart.getOption() as any).legend[0].orient).toBe('vertical')
        chart.dispatchAction({ type: 'legendUnSelect', name: option.legend.data[0].name })
        handle.resize(351, 384)
        expect((chart.getOption() as any).legend[0]).toMatchObject({ orient: 'horizontal', bottom: 0 })
        if (longNames) {
          handle.resize(1416, 500)
          expect((chart.getOption() as any).legend[0].orient).toBe('horizontal')
          expect(responsiveEChartsLayoutKey(envelope, 1416, 500, false, option))
            .not.toBe(responsiveEChartsLayoutKey(envelope, 3000, 500, false, option))
        }
        handle.resize(3000, 500)
        const restored = (chart.getOption() as any).legend[0]
        expect(restored.orient).toBe('vertical')
        expect(restored[side]).toBe(option.legend[side])
        expect(restored.textStyle.width).toBeNull()
        expect(restored.selected[option.legend.data[0].name]).toBe(false)
        expect(option.legend.orient).toBe('vertical')
      } finally { handle.dispose() }
    }
  }
})


for (const mark of ['radar', 'tree', 'graph'] as const) {
  test(`${mark} long category names remain inside the chart while resizing`, () => {
    const longName = (index: number) => `Region ${index + 1} — enterprise customers and strategic accounts with an unusually long name`
    let envelope: any
    if (mark === 'radar') {
      envelope = cartesianFixture('line') as any
      envelope.spec = { ...envelope.spec, kind: 'polar', mark, category: envelope.spec.x, value: envelope.spec.y[0],
        presentation: { ...envelope.spec.presentation, legend: 'hidden', showPointer: false, area: true } }
      envelope.dataState.datasets[0].rows = Array.from({ length: 8 }, (_, index) => [longName(index), index + 1])
    } else if (mark === 'tree') {
      envelope = hierarchyFixture('tree') as any
      envelope.spec.presentation.orientation = 'horizontal'
      envelope.dataState.datasets[0].rows = [ ['All regions', null, 100],
        ...Array.from({ length: 8 }, (_, index) => [longName(index), 'All regions', index + 1]) ]
    } else {
      envelope = networkFixture('graph') as any
      envelope.spec.presentation.layout = 'circular'
      envelope.dataState.datasets[0].rows = Array.from({ length: 4 }, (_, index) => [
        index === 0 ? 'Acquisition' : longName(index), `Outcome ${index} with a long category name`, index + 1,
      ])
    }
    const option = echartsOption(envelope, defaultRendererContext) as any
    const originalSeries = option.series[0]
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 1416, height: 384 })
    const setOption = chart.setOption.bind(chart)
    chart.setOption = ((value: any, settings: any) => setOption(value, { ...settings, lazyUpdate: false })) as typeof chart.setOption
    const handle = new EChartsHandle({} as HTMLElement, {} as HTMLElement, chart, new CategoryColorRegistry())
    try {
      handle.mount(envelope, defaultRendererContext)
      for (const width of [351, 408, 744, 1416]) {
        handle.resize(width, 384)
        chart.renderToSVGString()
        const labels = chart.getZr().storage.getDisplayList().filter((label: any) => label.type === 'tspan' && label.style?.text)
        expect(labels.length).toBeGreaterThan(0)
        for (const label of labels) {
          const bounds = label.getBoundingRect().clone()
          const transform = label.getComputedTransform?.() ?? label.transform
          if (transform) bounds.applyTransform(transform)
          expect(bounds.x, `${mark} ${label.style.text} at ${width}px`).toBeGreaterThanOrEqual(-0.001)
          expect(bounds.x + bounds.width, `${mark} ${label.style.text} at ${width}px`).toBeLessThanOrEqual(width + 0.001)
          expect(bounds.y).toBeGreaterThanOrEqual(-0.001)
          expect(bounds.y + bounds.height).toBeLessThanOrEqual(384.001)
        }
        if (mark === 'tree') {
          const layout = (chart as any).getModel().getSeriesByIndex(0).layoutInfo
          expect(layout.width).toBeGreaterThan(width * 0.3)
          expect(layout.height).toBeGreaterThan(384 * 0.3)
        }
        const patch = responsiveEChartsPatch(option, width, 384) as any
        const responsive = (patch.series ?? option.series)[0]
        expect(responsive.data).toBe(originalSeries.data)
        expect(responsive.tooltip).toBe(originalSeries.tooltip)
        if (mark !== 'radar') expect(responsive.label.formatter).toBe(originalSeries.label.formatter)
        else expect(patch.radar.indicator.map((indicator: any) => [indicator.name, indicator.max]))
          .toEqual(option.radar.indicator.map((indicator: any) => [indicator.name, indicator.max]))
      }
      expect(option.series[0]).toBe(originalSeries)
      const restored = chart.getOption() as any
      if (mark === 'radar') {
        expect(restored.radar[0].radius).toBe(option.radar.radius ?? '50%')
        expect(restored.radar[0].indicator.every((indicator: any) => indicator.nameTruncate.maxWidth === null)).toBe(true)
        expect(option.radar.indicator.every((indicator: any) => indicator.nameTruncate === undefined)).toBe(true)
      } else if (mark === 'tree') {
        expect(restored.series[0].label.width).toBeNull()
        expect(restored.series[0].leaves.label.width).toBeNull()
      } else {
        expect(restored.series[0].left).toBe(originalSeries.left)
        expect(restored.series[0].right).toBe(originalSeries.right)
      }
    } finally { handle.dispose() }
  })
}


test('boxplot label budgets update within compact widths and restore useful plot space', () => {
  const envelope = cartesianFixture('boxplot', ['label', 'min', 'q1', 'median', 'q3', 'max']) as any
  envelope.dataState.datasets[0].rows = Array.from({ length: 8 }, (_, index) => [
    `Region ${index + 1} with an unusually long category name`, index, index + 1, index + 2, index + 3, index + 4,
  ])
  const option = echartsOption(envelope, defaultRendererContext) as any
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 800, height: 500 })
  const setOption = chart.setOption.bind(chart)
  chart.setOption = ((value: any, settings: any) => setOption(value, { ...settings, lazyUpdate: false })) as typeof chart.setOption
  const handle = new EChartsHandle({} as HTMLElement, {} as HTMLElement, chart, new CategoryColorRegistry())
  try {
    handle.mount(envelope, defaultRendererContext)
    let narrowBudget = 0
    for (const width of [300, 400, 800]) {
      handle.resize(width, 400)
      const model = (chart as any).getModel()
      const plot = model.getComponent('grid').coordinateSystem.getRect()
      expect(plot.width).toBeGreaterThan(width * 0.3)
      expect(plot.height).toBeGreaterThan(120)
      const budget = model.getComponent('xAxis').get('axisLabel.width')
      if (width === 300) narrowBudget = budget
      else expect(budget).toBeGreaterThan(narrowBudget)
      expect(option.xAxis.axisLabel.width).toBeUndefined()
      expect(model.getComponent('xAxis').get('axisLabel.rotate')).toBe(option.xAxis.axisLabel.rotate)
    }
    expect(responsiveEChartsLayoutKey(envelope, 300, 400, false, option))
      .not.toBe(responsiveEChartsLayoutKey(envelope, 400, 400, false, option))
  } finally { handle.dispose() }
})


test('automatic dense radar names avoid overlap and recover visible labels as the chart grows', () => {
  for (const density of ['automatic', 'always', 'dense'] as const) {
    const envelope = cartesianFixture('line') as any
    envelope.spec = { ...envelope.spec, kind: 'polar', mark: 'radar', category: envelope.spec.x, value: envelope.spec.y[0],
      presentation: { ...envelope.spec.presentation, legend: 'hidden', showPointer: false, area: true,
        labelPolicy: { ...envelope.spec.presentation.labelPolicy, density } } }
    envelope.dataState.datasets[0].rows = Array.from({ length: 80 }, (_, index) => [`Region ${index + 1}`, index + 1])
    const option = echartsOption(envelope, defaultRendererContext) as any
    const names = option.radar.indicator.map((indicator: any) => indicator.name)
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 351, height: 384 })
    const setOption = chart.setOption.bind(chart)
    chart.setOption = ((value: any, settings: any) => setOption(value, { ...settings, lazyUpdate: false })) as typeof chart.setOption
    const handle = new EChartsHandle({} as HTMLElement, {} as HTMLElement, chart, new CategoryColorRegistry())
    let compactCount = 0
    try {
      handle.mount(envelope, defaultRendererContext)
      for (const [width, height] of [[351, 384], [3000, 2000], [351, 384]]) {
        handle.resize(width!, height!)
        chart.renderToSVGString()
        const labels = chart.getZr().storage.getDisplayList().filter((label: any) =>
          label.type === 'tspan' && /^Region \d+$/.test(String(label.style?.text)))
        if (density !== 'automatic') expect(labels.length).toBe(80)
        else {
          expect(labels.length).toBeGreaterThanOrEqual(4)
          if (width === 351) {
            expect(labels.length).toBeLessThan(80)
            if (compactCount === 0) compactCount = labels.length
            else expect(labels.length).toBe(compactCount)
          } else expect(labels.length).toBeGreaterThan(compactCount)
          const bounds = labels.map((label: any) => {
            const bounds = label.getBoundingRect().clone()
            const transform = label.getComputedTransform?.() ?? label.transform
            if (transform) bounds.applyTransform(transform)
            expect(bounds.x).toBeGreaterThanOrEqual(-0.001)
            expect(bounds.x + bounds.width).toBeLessThanOrEqual(width! + 0.001)
            expect(bounds.y).toBeGreaterThanOrEqual(-0.001)
            expect(bounds.y + bounds.height).toBeLessThanOrEqual(height! + 0.001)
            return bounds
          })
          for (let index = 0; index < bounds.length; index++) for (const other of bounds.slice(index + 1)) {
            const label = bounds[index]!
            const overlapWidth = Math.min(label.x + label.width, other.x + other.width) - Math.max(label.x, other.x)
            const overlapHeight = Math.min(label.y + label.height, other.y + other.height) - Math.max(label.y, other.y)
            expect(overlapWidth > 1 && overlapHeight > 1).toBe(false)
          }
        }
        const current = chart.getOption() as any
        expect(current.radar[0].indicator.map((indicator: any) => indicator.name)).toEqual(names)
        expect(current.series[0].data[0].value).toHaveLength(80)
        expect(option.series[0].tooltip.formatter({ value: current.series[0].data[0].value })).toContain('Region 80')
      }
      expect(option.radar.indicator.every((indicator: any) => indicator.showName === undefined)).toBe(true)
    } finally { handle.dispose() }
  }
})
