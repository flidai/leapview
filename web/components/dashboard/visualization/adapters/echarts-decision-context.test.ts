import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption, responsiveEChartsPatch } from './echarts'
import visualDocumentation from '../../../../../docs/visuals/examples.gen.json'

test('ECharts translates semantic axes and decision context from the current frame', () => {
  const envelope = cartesianFixture() as any
  envelope.spec.axes = [
    { id: 'x', title: 'Month', type: 'automatic', inversion: 'automatic', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic', dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'sparse' },
    { id: 'primary_y', title: 'Revenue', type: 'automatic', inversion: 'automatic', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic', dateUnit: 'automatic', scale: 'linear', zero: 'exclude', minimum: 10, maximum: 100, unit: 'USD', tickDensity: 'dense' },
  ]
  envelope.spec.referenceLines = [
    { id: 'target', axis: 'primary_y', value: { kind: 'number', value: 80 }, label: 'Target', tone: 'success' },
    { id: 'average', axis: 'primary_y', value: { kind: 'field', field: { dataset: 'primary', field: 'value' }, reducer: 'mean' }, label: 'Average', tone: 'warning' },
  ]
  envelope.spec.referenceBands = [{
    id: 'observed', axis: 'primary_y',
    from: { kind: 'field', field: { dataset: 'primary', field: 'value' }, reducer: 'minimum' },
    to: { kind: 'field', field: { dataset: 'primary', field: 'value' }, reducer: 'maximum' },
    label: 'Observed range', tone: 'neutral',
  }]
  envelope.spec.eventAnnotations = [
    { id: 'launch', axis: 'x', value: { kind: 'text', value: 'A' }, label: 'Launch', description: 'New pricing', tone: 'ink' },
  ]
  envelope.spec.tooltip = [{ dataset: 'primary', field: 'value' }]
  envelope.dataState.datasets[0].rows = [['A', 20], ['B', 60]]

  const context = { ...defaultRendererContext, colors: { ...defaultRendererContext.colors, success: '#00aa00', attention: '#ffaa00' } }
  const option = echartsOption(envelope, context) as any
  expect(option.xAxis).toMatchObject({ name: 'Month', axisLabel: { interval: 2 } })
  expect(option.yAxis).toMatchObject({ name: 'Revenue (USD)', type: 'value', min: 10, max: 100, scale: true, splitNumber: 8 })
  expect(option.series[0].markLine.data).toEqual([
    { id: 'reference-line:target', name: 'Target', yAxis: 80, lineStyle: { color: '#00aa00' } },
    { id: 'reference-line:average', name: 'Average', yAxis: 40, lineStyle: { color: '#ffaa00' } },
    { id: 'event-annotation:launch', name: 'Launch', xAxis: 'A', lineStyle: { color: context.colors.foreground } },
  ])
  expect(option.series[0].markArea.data).toEqual([[
    { id: 'reference-band:observed', name: 'Observed range', yAxis: 20, itemStyle: { color: context.colors.accent, opacity: 0.12 } },
    { yAxis: 60 },
  ]])
  expect(option.tooltip.formatter({ value: ['A', 20] })).toBe('value: 20')
  expect(option.aria.description).toBe('line. Reference line: Target. Reference line: Average. Reference band: Observed range. Event: Launch — New pricing.')

  envelope.dataRevision = 2
  envelope.dataState.dataRevision = 2
  envelope.dataState.datasets[0].dataRevision = 2
  envelope.dataState.datasets[0].rows = [['A', 40], ['B', 80]]
  const refreshed = echartsOption(envelope, context) as any
  expect(refreshed.series[0].markLine.data[1].yAxis).toBe(60)
  expect(refreshed.series[0].markArea.data[0].map((item: any) => item.yAxis)).toEqual([40, 80])

  envelope.dataState.datasets[0].rows = [['A', '9007199254740993.125'], ['B', '9007199254740995.375']]
  const decimalStrings = echartsOption(envelope, context) as any
  expect(decimalStrings.series[0].markLine.data[1].yAxis).toBe('9007199254740994.25')
  expect(decimalStrings.series[0].markArea.data[0].map((item: any) => item.yAxis)).toEqual(['9007199254740993.125', '9007199254740995.375'])

  envelope.dataState.datasets[0].rows = []
  const empty = echartsOption(envelope, context) as any
  expect(empty.series[0].markLine.data.map((item: any) => item.id)).toEqual(['reference-line:target', 'event-annotation:launch'])
  expect(empty.series[0].markArea).toBeUndefined()
})

test('ECharts omits non-positive resolved references only on log axes', () => {
  const envelope = cartesianFixture() as any
  envelope.spec.axes = [axisConfiguration('x', 'log'), axisConfiguration('primary_y', 'log')]
  envelope.spec.axes[0].type = 'value'
  envelope.spec.x = { dataset: 'primary', field: 'x_numeric' }
  envelope.spec.datasets[0].fields.push(
    { id: 'x_numeric', role: 'dimension', dataType: 'decimal', nullable: false, label: 'x numeric' },
    { id: 'zero', role: 'metric', dataType: 'decimal', nullable: true, label: 'zero' },
    { id: 'negative', role: 'metric', dataType: 'decimal', nullable: true, label: 'negative' },
    { id: 'tiny_positive', role: 'metric', dataType: 'decimal', nullable: true, label: 'tiny positive' },
    { id: 'missing', role: 'metric', dataType: 'decimal', nullable: true, label: 'missing' },
  )
  envelope.dataState.datasets[0].columns.push('zero', 'negative', 'tiny_positive', 'missing', 'x_numeric')
  envelope.dataState.datasets[0].rows = [
    ['A', 1, '0.0000000000000000000', '-2', '0.0000000000000000001', null, 10],
    ['B', 2, '0.0000000000000000000', '-3', '0.0000000000000000001', null, 20],
  ]
  envelope.spec.referenceLines = [
    { id: 'number-zero', axis: 'primary_y', value: { kind: 'number', value: 0 }, label: 'zero', tone: 'neutral' },
    { id: 'number-negative', axis: 'primary_y', value: { kind: 'number', value: -1 }, label: 'negative', tone: 'neutral' },
    { id: 'number-positive', axis: 'primary_y', value: { kind: 'number', value: 1000000 }, label: 'positive out of domain', tone: 'success' },
    { id: 'field-zero', axis: 'primary_y', value: { kind: 'field', field: { dataset: 'primary', field: 'zero' }, reducer: 'maximum' }, label: 'field zero', tone: 'neutral' },
    { id: 'field-negative', axis: 'primary_y', value: { kind: 'field', field: { dataset: 'primary', field: 'negative' }, reducer: 'minimum' }, label: 'field negative', tone: 'neutral' },
    { id: 'field-tiny-positive', axis: 'primary_y', value: { kind: 'field', field: { dataset: 'primary', field: 'tiny_positive' }, reducer: 'maximum' }, label: 'exact positive', tone: 'success' },
    { id: 'field-missing', axis: 'primary_y', value: { kind: 'field', field: { dataset: 'primary', field: 'missing' }, reducer: 'first' }, label: 'missing', tone: 'neutral' },
  ]
  envelope.spec.eventAnnotations = [
    { id: 'event-zero', axis: 'x', value: { kind: 'number', value: 0 }, label: 'zero event', tone: 'neutral' },
    { id: 'event-negative', axis: 'x', value: { kind: 'field', field: { dataset: 'primary', field: 'negative' }, reducer: 'minimum' }, label: 'negative event', tone: 'neutral' },
    { id: 'event-positive', axis: 'x', value: { kind: 'field', field: { dataset: 'primary', field: 'tiny_positive' }, reducer: 'maximum' }, label: 'positive event', tone: 'success' },
  ]
  envelope.spec.referenceBands = [
    { id: 'valid', axis: 'primary_y', from: { kind: 'field', field: { dataset: 'primary', field: 'tiny_positive' }, reducer: 'minimum' }, to: { kind: 'number', value: 2 }, label: 'valid', tone: 'neutral' },
    { id: 'zero-endpoint', axis: 'primary_y', from: { kind: 'number', value: 0 }, to: { kind: 'number', value: 2 }, label: 'zero endpoint', tone: 'neutral' },
    { id: 'negative-endpoint', axis: 'primary_y', from: { kind: 'number', value: -1 }, to: { kind: 'number', value: 2 }, label: 'negative endpoint', tone: 'neutral' },
    { id: 'missing-endpoint', axis: 'primary_y', from: { kind: 'field', field: { dataset: 'primary', field: 'missing' }, reducer: 'first' }, to: { kind: 'number', value: 2 }, label: 'missing endpoint', tone: 'neutral' },
  ]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].markLine.data.map((item: any) => item.id)).toEqual([
    'reference-line:number-positive',
    'reference-line:field-tiny-positive',
    'event-annotation:event-positive',
  ])
  expect(option.series[0].markLine.data[1].yAxis).toBe('0.0000000000000000001')
  expect(option.series[0].markArea.data.map((items: any[]) => items[0].id)).toEqual(['reference-band:valid'])

  const linear = cartesianFixture() as any
  linear.spec.axes = [axisConfiguration('primary_y', 'linear')]
  linear.spec.referenceLines = [{ id: 'linear-negative', axis: 'primary_y', value: { kind: 'number', value: -1 }, label: 'negative', tone: 'neutral' }]
  expect((echartsOption(linear, defaultRendererContext) as any).series[0].markLine.data.map((item: any) => item.id)).toEqual(['reference-line:linear-negative'])
})

test('ECharts applies the log reference guard across point, horizontal, and secondary axes', () => {
  const point = pointTitledAxisFixture() as any
  point.spec.axes.find((axis: any) => axis.id === 'primary_y').scale = 'log'
  point.spec.referenceLines = [
    { id: 'point-negative', axis: 'primary_y', value: { kind: 'number', value: -1 }, label: 'negative', tone: 'neutral' },
    { id: 'point-positive', axis: 'primary_y', value: { kind: 'number', value: 2 }, label: 'positive', tone: 'success' },
  ]
  expect((echartsOption(point, defaultRendererContext) as any).series[0].markLine.data.map((item: any) => item.id)).toEqual(['reference-line:point-positive'])

  const horizontal = cartesianFixture() as any
  horizontal.spec.presentation.orientation = 'horizontal'
  horizontal.spec.axes = [axisConfiguration('primary_y', 'log')]
  horizontal.spec.referenceLines = [
    { id: 'horizontal-negative', axis: 'primary_y', value: { kind: 'number', value: -1 }, label: 'negative', tone: 'neutral' },
    { id: 'horizontal-positive', axis: 'primary_y', value: { kind: 'number', value: 2 }, label: 'positive', tone: 'success' },
  ]
  const horizontalLine = (echartsOption(horizontal, defaultRendererContext) as any).series[0].markLine.data
  expect(horizontalLine.map((item: any) => item.id)).toEqual(['reference-line:horizontal-positive'])
  expect(horizontalLine[0].xAxis).toBe(2)

  const combo = comboTitledAxisFixture() as any
  combo.spec.axes.find((axis: any) => axis.id === 'secondary_y').scale = 'log'
  combo.spec.referenceLines = [
    { id: 'secondary-negative', axis: 'secondary_y', value: { kind: 'number', value: -1 }, label: 'negative', tone: 'neutral' },
    { id: 'secondary-positive', axis: 'secondary_y', value: { kind: 'number', value: 2 }, label: 'positive', tone: 'success' },
  ]
  const comboOption = echartsOption(combo, defaultRendererContext) as any
  expect(comboOption.series[1].markLine.data.map((item: any) => item.id)).toEqual(['reference-line:secondary-positive'])
  expect(comboOption.series[1].markLine.data[0].yAxis).toBe(2)
})

test('ECharts keeps authored braces literal and preserves unnamed mark-line values', () => {
  const envelope = cartesianFixture() as any
  envelope.spec.referenceLines = [
    { id: 'literal', axis: 'primary_y', value: { kind: 'number', value: 7 }, label: 'Target {value}', tone: 'success' },
    { id: 'unnamed', axis: 'primary_y', value: { kind: 'number', value: 8 }, tone: 'neutral' },
  ]
  const option = echartsOption(envelope, defaultRendererContext) as any
  const formatter = option.series[0].markLine.label.formatter
  expect(formatter({ name: 'Target {value}', value: 7 })).toBe('Target {value}')
  expect(formatter({ name: '', value: 8 })).toBe('8')
})

test('ECharts authored axis titles stay visible on centered physical axes in desktop and compact layouts', () => {
  const vertical = titledAxisFixture()
  const horizontal = structuredClone(vertical) as any
  horizontal.spec.presentation.orientation = 'horizontal'
  horizontal.spec.axes[0].title = 'Month axis'
  horizontal.spec.axes[0].inversion = 'inverted'
  horizontal.spec.axes[1].inversion = 'inverted'
  const combo = comboTitledAxisFixture()
  const point = pointTitledAxisFixture()

  for (const [kind, envelope] of [['vertical', vertical], ['horizontal-inverted', horizontal], ['combo-secondary', combo], ['point', point]] as const) {
    for (const [layout, width, height] of [['desktop', 640, 360], ['compact', 320, 240]] as const) {
      const source = echartsOption(envelope, defaultRendererContext) as Record<string, any>
      const option = { ...source, ...responsiveEChartsPatch(source, width, height) }
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height })
      try {
        chart.setOption(option)
        const svg = chart.renderToSVGString()
        const renderedText = chart.getZr().storage.getDisplayList()
          .filter((item: any) => item.type === 'tspan' && typeof item.style?.text === 'string')
        const controls = ['legend', 'dataZoom'].flatMap((mainType) => componentBounds(chart, mainType))
        for (const title of envelope.spec.axes!.flatMap((axis: any) => axis.title ? [axis.title] : [])) {
          const text = svgTitle(svg, title)
          expect(text, `${kind} ${layout} should render ${title}`).toBeDefined()
          const position = svgTitlePosition(text!)
          expect(position.x, `${kind} ${layout} ${title} x`).toBeGreaterThan(0)
          expect(position.x, `${kind} ${layout} ${title} x`).toBeLessThan(width)
          expect(position.y, `${kind} ${layout} ${title} y`).toBeGreaterThan(0)
          expect(position.y, `${kind} ${layout} ${title} y`).toBeLessThan(height)
          const titleElements = renderedText.filter((item: any) => item.style.text === title)
          expect(titleElements, `${kind} ${layout} should have one rendered ${title} title`).toHaveLength(1)
          const titleBounds = globalTextBounds(titleElements[0])
          expect(titleBounds.x, `${kind} ${layout} ${title} left bound`).toBeGreaterThanOrEqual(0)
          expect(titleBounds.y, `${kind} ${layout} ${title} top bound`).toBeGreaterThanOrEqual(0)
          expect(titleBounds.x + titleBounds.width, `${kind} ${layout} ${title} right bound`).toBeLessThanOrEqual(width)
          expect(titleBounds.y + titleBounds.height, `${kind} ${layout} ${title} bottom bound`).toBeLessThanOrEqual(height)
          for (const control of controls) {
            expect(rectanglesOverlap(titleBounds, control.bounds), `${kind} ${layout} ${title} overlaps ${control.mainType}`).toBeFalse()
          }
          for (const item of renderedText.filter((candidate: any) => candidate.style.text !== title)) {
            const otherBounds = globalTextBounds(item)
            expect(rectanglesOverlap(titleBounds, otherBounds), `${kind} ${layout} ${title} overlaps ${item.style.text}`).toBeFalse()
          }
        }
      } finally {
        chart.dispose()
      }
    }
  }
})

test('ECharts keeps generated currency axis and decision-context labels inside the compact plot', () => {
  const generated = generatedRevenueLineContextFixture()
  const horizontal = structuredClone(generated) as any
  horizontal.spec.presentation.orientation = 'horizontal'
  horizontal.spec.axes[0].inversion = 'inverted'
  const variants = [['vertical', generated], ['horizontal-inverted', horizontal]] as const
  const contexts = [
    defaultRendererContext,
    { ...defaultRendererContext, theme: 'dark' as const, colors: { ...defaultRendererContext.colors, foreground: '#f0f6fc', muted: '#8b949e', grid: '#30363d', surface: '#0d1117' } },
  ]

  for (const [kind, envelope] of variants) {
    for (const context of contexts) {
      for (const [layout, width] of [['desktop', 800], ['compact', 358]] as const) {
        const source = echartsOption(envelope, context) as Record<string, any>
        const option = { ...source, ...responsiveEChartsPatch(source, width, 413) }
        const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height: 413 })
        try {
          chart.setOption(option)
          chart.renderToSVGString()
          const textElements = chart.getZr().storage.getDisplayList()
            .filter((item: any) => item.type === 'tspan' && typeof item.style?.text === 'string')
          const titleElement = textElements.find((item: any) => {
            if (item.style.text !== 'Revenue') return false
            const bounds = globalTextBounds(item)
            return kind === 'vertical' ? bounds.height > bounds.width : bounds.y + bounds.height <= 413 - Number(option.grid.bottom)
          })
          expect(titleElement, `${kind} ${layout} should render the Revenue axis title`).toBeDefined()
          const titleBounds = globalTextBounds(titleElement)
          expect(titleBounds.x, `${kind} ${layout} Revenue title left bound`).toBeGreaterThanOrEqual(0)
          expect(titleBounds.y, `${kind} ${layout} Revenue title top bound`).toBeGreaterThanOrEqual(0)
          expect(titleBounds.x + titleBounds.width, `${kind} ${layout} Revenue title right bound`).toBeLessThanOrEqual(width)
          expect(titleBounds.y + titleBounds.height, `${kind} ${layout} Revenue title bottom bound`).toBeLessThanOrEqual(413)
          for (const item of textElements.filter((candidate: any) => candidate !== titleElement && candidate.style.text !== 'Revenue')) {
            expect(rectanglesOverlap(titleBounds, globalTextBounds(item)), `${kind} ${layout} Revenue title overlaps ${item.style.text}`).toBeFalse()
          }

          for (const label of ['Target', 'Fiscal year']) {
            const labelElement = textElements.find((item: any) => item.style.text === label)
            expect(labelElement, `${kind} ${layout} should render ${label}`).toBeDefined()
            const labelBounds = globalTextBounds(labelElement)
            expect(labelBounds.x, `${kind} ${layout} ${label} left bound`).toBeGreaterThanOrEqual(Number(option.grid.left))
            expect(labelBounds.y, `${kind} ${layout} ${label} top bound`).toBeGreaterThanOrEqual(Number(option.grid.top))
            expect(labelBounds.x + labelBounds.width, `${kind} ${layout} ${label} right bound`).toBeLessThanOrEqual(width - Number(option.grid.right))
            expect(labelBounds.y + labelBounds.height, `${kind} ${layout} ${label} bottom bound`).toBeLessThanOrEqual(413 - Number(option.grid.bottom))
          }
        } finally {
          chart.dispose()
        }
      }
    }
  }
})

test('ECharts keeps generated compact horizontal bar ticks and automatic labels inside their geometry', () => {
  const generated = generatedBarStatusFixture()
  const contexts = [
    defaultRendererContext,
    { ...defaultRendererContext, theme: 'dark' as const, colors: { ...defaultRendererContext.colors, foreground: '#f0f6fc', muted: '#8b949e', grid: '#30363d', surface: '#0d1117' } },
  ]

  for (const context of contexts) {
    const source = echartsOption(generated, context) as Record<string, any>
    expect(source.grid).toMatchObject({ containLabel: false, outerBoundsMode: 'same', outerBoundsContain: 'all' })
    const option = { ...source, ...responsiveEChartsPatch(source, 358, 411) }
    const barSeries = option.series.find((candidate: any) => candidate.type === 'bar')
    expect(barSeries.label.position).toBe('insideRight')
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 358, height: 411 })
    try {
      chart.setOption(option)
      chart.renderToSVGString()
      const displayList = chart.getZr().storage.getDisplayList()
      const textElements = displayList
        .filter((item: any) => item.type === 'tspan' && typeof item.style?.text === 'string')
      const barsWithLabels = displayList
        .filter((item: any) => item.type === 'rect' && typeof item.getTextContent?.()?.style?.text === 'string')
      expect(barsWithLabels.length, `${context.theme} should retain rendered bar labels`).toBeGreaterThan(10)
      for (const bar of barsWithLabels) {
        const label = bar.getTextContent()
        const barBounds = globalTextBounds(bar)
        const labelBounds = globalTextBounds(label)
        const padding = Array.isArray(label.style.padding) ? Math.max(...label.style.padding.map(Number)) : Number(label.style.padding ?? 0)
        const tolerance = padding + Number(label.style.lineWidth ?? 0)
        expect(labelBounds.x, `${context.theme} ${label.style.text} label left bound`).toBeGreaterThanOrEqual(barBounds.x - tolerance)
        expect(labelBounds.y, `${context.theme} ${label.style.text} label top bound`).toBeGreaterThanOrEqual(barBounds.y - tolerance)
        expect(labelBounds.x + labelBounds.width, `${context.theme} ${label.style.text} label right bound`).toBeLessThanOrEqual(barBounds.x + barBounds.width + tolerance)
        expect(labelBounds.y + labelBounds.height, `${context.theme} ${label.style.text} label bottom bound`).toBeLessThanOrEqual(barBounds.y + barBounds.height + tolerance)
      }
      const narrowBars = barsWithLabels.filter((bar: any) => ['$128', '$58'].includes(bar.getTextContent().style.text))
      expect(narrowBars.length, `${context.theme} should retain both narrow first-stack labels`).toBe(2)
      for (const bar of narrowBars) {
        const label = bar.getTextContent()
        expect(label.ignore, `${context.theme} ${label.style.text} should not be hidden`).toBe(false)
        expect(textElements.find((item: any) => item.parent === label), `${context.theme} ${label.style.text} should retain a rendered text child`).toBeDefined()
      }
      const beautyLabel = narrowBars.find((bar: any) => bar.getTextContent().style.text === '$128')?.getTextContent()
      const beautyText = textElements.find((item: any) => item.parent === beautyLabel)
      expect(beautyText?.style.text, `${context.theme} Beauty should render a truncated label`).toContain('…')
      expect(textElements.some((item: any) => String(item.style.text).includes('…')), `${context.theme} should truncate at least one narrow bar label`).toBe(true)
      for (const item of textElements) {
        const bounds = globalTextBounds(item)
        expect(bounds.x, `${context.theme} ${item.style.text} left bound`).toBeGreaterThanOrEqual(0)
        expect(bounds.y, `${context.theme} ${item.style.text} top bound`).toBeGreaterThanOrEqual(0)
        expect(bounds.x + bounds.width, `${context.theme} ${item.style.text} right bound`).toBeLessThanOrEqual(358)
        expect(bounds.y + bounds.height, `${context.theme} ${item.style.text} bottom bound`).toBeLessThanOrEqual(411)
      }
      const maxTick = textElements.find((item: any) => item.style.text === '$1,500')
      expect(maxTick, `${context.theme} should render the maximum currency tick`).toBeDefined()
      expect(globalTextBounds(maxTick).x + globalTextBounds(maxTick).width).toBeLessThanOrEqual(358)
      expect(textElements.some((item: any) => item.style.text === '$842')).toBe(true)
      expect(textElements.some((item: any) => item.style.text === '$556')).toBe(true)
      expect(textElements.some((item: any) => item.style.text === '$128')).toBe(false)
      expect(textElements.some((item: any) => item.style.text === '$58')).toBe(false)
    } finally {
      chart.dispose()
    }

    const outside = structuredClone(generated) as any
    outside.spec.presentation.labelPosition = 'outside'
    const outsideOption = echartsOption(outside, context) as Record<string, any>
    const outsideBar = outsideOption.series.find((candidate: any) => candidate.type === 'bar')
    expect(outsideBar.label.position).toBe('right')
    expect(outsideBar.labelLayout({ rect: { width: 8, height: 40 }, labelRect: { width: 28, height: 13 } })).not.toHaveProperty('width')
  }
})

function titledAxisFixture(): VisualizationEnvelope {
  const envelope = cartesianFixture() as any
  envelope.spec.axes = [
    { id: 'x', title: 'Revenue after discounts (USD)', type: 'automatic', inversion: 'automatic', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic', dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'automatic' },
    { id: 'primary_y', title: 'Revenue axis', type: 'automatic', inversion: 'automatic', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic', dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'automatic' },
  ]
  return envelope
}

function comboTitledAxisFixture(): VisualizationEnvelope {
  const envelope = titledAxisFixture() as any
  envelope.spec.mark = 'combo'
  envelope.spec.y = [envelope.spec.y[0], { dataset: 'primary', field: 'orders' }]
  envelope.spec.datasets[0].fields.push({ id: 'orders', role: 'metric', dataType: 'integer', nullable: false, label: 'Orders' })
  envelope.dataState.datasets[0].columns.push('orders')
  envelope.dataState.datasets[0].rows = envelope.dataState.datasets[0].rows.map((row: unknown[]) => [...row, 2])
  envelope.spec.presentation.comboSeries = [
    { seriesValue: 'value', mark: 'line', axis: 'primary' },
    { seriesValue: 'orders', mark: 'column', axis: 'secondary' },
  ]
  envelope.spec.axes.push({ id: 'secondary_y', title: 'Orders axis', type: 'automatic', inversion: 'inverted', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic', dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'automatic' })
  return envelope
}

function pointTitledAxisFixture(): VisualizationEnvelope {
  const envelope = cartesianFixture() as any
  envelope.spec.kind = 'point'
  envelope.spec.title = 'points'
  envelope.spec.datasets[0].fields = [
    { id: 'x', role: 'metric', dataType: 'decimal', nullable: false, label: 'X' },
    { id: 'y', role: 'metric', dataType: 'decimal', nullable: false, label: 'Y' },
  ]
  envelope.spec.x = { dataset: 'primary', field: 'x' }
  envelope.spec.y = { dataset: 'primary', field: 'y' }
  envelope.spec.axes = [
    { id: 'x', title: 'X axis', type: 'automatic', inversion: 'automatic', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic', dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'automatic' },
    { id: 'primary_y', title: 'Y axis', type: 'automatic', inversion: 'automatic', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic', dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'automatic' },
  ]
  envelope.spec.presentation = { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, overplot: 'show_all', opacity: 1, largeMode: 'never', largeThreshold: 1000, brush: [] }
  envelope.dataState.datasets[0].columns = ['x', 'y']
  envelope.dataState.datasets[0].rows = [[1, 2], [2, 4]]
  return envelope
}

function generatedRevenueLineContextFixture(): VisualizationEnvelope {
  const document = (visualDocumentation as any).documents['visuals/line']
  const envelope = document.find((candidate: any) => candidate.visualID === 'revenue_line_context')
  if (!envelope) throw new Error('generated revenue_line_context fixture is missing')
  return structuredClone(envelope) as VisualizationEnvelope
}

function generatedBarStatusFixture(): VisualizationEnvelope {
  const document = (visualDocumentation as any).documents['visuals/bar']
  const envelope = document.find((candidate: any) => candidate.visualID === 'categories_by_status_bar')
  if (!envelope) throw new Error('generated categories_by_status_bar fixture is missing')
  return structuredClone(envelope) as VisualizationEnvelope
}

function globalTextBounds(item: any): { x: number; y: number; width: number; height: number } {
  const bounds = item.getBoundingRect().clone()
  const transform = item.getComputedTransform?.() ?? item.transform
  if (transform) bounds.applyTransform(transform)
  return { x: bounds.x, y: bounds.y, width: bounds.width, height: bounds.height }
}

function componentBounds(chart: echarts.EChartsType, mainType: string): Array<{ mainType: string; bounds: { x: number; y: number; width: number; height: number } }> {
  const model = (chart as any).getModel?.()
  return model?.findComponents({ mainType }).flatMap((component: any) => {
    const group = (chart as any).getViewOfComponentModel(component)?.group
    if (!group) return []
    const bounds = group.getBoundingRect().clone()
    const transform = group.getComputedTransform?.() ?? group.transform
    if (transform) bounds.applyTransform(transform)
    return [{ mainType, bounds: { x: bounds.x, y: bounds.y, width: bounds.width, height: bounds.height } }]
  }) ?? []
}

function rectanglesOverlap(left: { x: number; y: number; width: number; height: number }, right: { x: number; y: number; width: number; height: number }): boolean {
  return left.x < right.x + right.width && left.x + left.width > right.x && left.y < right.y + right.height && left.y + left.height > right.y
}

function svgTitle(svg: string, title: string): string | undefined {
  const marker = `>${title}</text>`
  const end = svg.indexOf(marker)
  if (end < 0) return undefined
  const start = svg.lastIndexOf('<text', end)
  return start < 0 ? undefined : svg.slice(start, end + marker.length)
}

function svgTitlePosition(text: string): { x: number; y: number } {
  const transform = text.match(/transform="([^"]+)"/)?.[1] ?? ''
  const numbers = transform.match(/(?:matrix|translate)\(([^)]+)\)/)?.[1]?.split(/[ ,]+/).map(Number) ?? []
  if (transform.startsWith('matrix')) return { x: numbers[4] ?? Number.NaN, y: numbers[5] ?? Number.NaN }
  return { x: numbers[0] ?? Number.NaN, y: numbers[1] ?? Number.NaN }
}

function axisConfiguration(id: 'x' | 'primary_y' | 'secondary_y', scale: 'linear' | 'log'): Record<string, unknown> {
  return {
    id, type: 'automatic', inversion: 'automatic', ticks: 'automatic', grid: 'automatic',
    labelRotation: 'automatic', dateUnit: 'automatic', scale, zero: 'automatic', tickDensity: 'automatic',
  }
}

function cartesianFixture(): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: 'line', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'line', mark: 'line',
      datasets: [{ id: 'primary', fields: [
        { id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'label' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'value' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'line', description: 'line' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }],
      presentation: { legend: 'bottom', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, smooth: true, stacked: true, showSymbols: false, dataZoom: true, area: false, step: true, symbolSize: 12, labelPosition: 'top', orientation: 'vertical' },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['A', 1]], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}
