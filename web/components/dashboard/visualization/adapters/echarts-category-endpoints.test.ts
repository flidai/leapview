import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'
import { cartesianFixture } from './echarts-test-fixtures'

const months = Array.from({ length: 26 }, (_, index) => {
  const date = new Date(Date.UTC(2016, 8 + index, 1))
  return date.toISOString().slice(0, 7)
})

const values = [
  ...Array.from({ length: 22 }, (_, index) => index + 1),
  1066540.75, 1022425.32, 4439.54, 589.67,
]

function areaEnvelope(mark: 'line' | 'area' = 'area') {
  const envelope = cartesianFixture(mark) as any
  envelope.spec.presentation.dataZoom = false
  envelope.spec.presentation.legend = 'hidden'
  envelope.dataState.datasets[0].rows = months.map((month, index) => [month, values[index]])
  return envelope
}

type LabelBounds = { text: string; x: number; right: number; ignore: boolean }

function xAxisLabels(chart: echarts.EChartsType, values = months): LabelBounds[] {
  const axis = (chart.getModel() as any).getComponent('xAxis').axis
  const labels = axis.axisBuilder.group.children()[0].children()
  return labels
    .filter((label: any) => label.type === 'text' && values.includes(String(label.style?.text)))
    .map((label: any) => {
      const rect = label.getBoundingRect().clone()
      const transform = label.getComputedTransform()
      if (transform) rect.applyTransform(transform)
      return { text: String(label.style.text), x: rect.x, right: rect.x + rect.width, ignore: label.ignore === true }
    })
}

function render(envelope: any, width: number) {
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height: 266 })
  chart.setOption({ ...echartsOption(envelope, defaultRendererContext), animation: false })
  chart.renderToSVGString()
  return chart
}

test('native category line and area labels stay contained in rendered SVG bounds', () => {
  for (const mark of ['line', 'area'] as const) {
    for (const inverse of [false, true]) {
      for (const width of [320, 640]) {
        const envelope = areaEnvelope(mark)
        envelope.spec.axes = [{
          id: 'x', type: 'automatic', inversion: inverse ? 'inverted' : 'normal',
          ticks: 'automatic', grid: 'automatic', labelRotation: 'horizontal', dateUnit: 'automatic',
          scale: 'automatic', zero: 'automatic', tickDensity: 'normal',
        }]
        const option = echartsOption(envelope, defaultRendererContext) as any
        expect(option.xAxis.type).toBe('category')
        expect(option.grid).toMatchObject({ containLabel: false, outerBoundsMode: 'same', outerBoundsContain: 'all' })
        expect(option.dataset.source.at(-1)).toEqual(['2018-10', 589.67])
        expect(option.series[0]).toMatchObject({ id: 'series:primary:value', type: 'line', encode: { x: 'label', y: 'value' } })

        const chart = render(envelope, width)
        try {
          const labels = xAxisLabels(chart).filter((label) => !label.ignore)
          for (const label of labels) {
            expect(label.x).toBeGreaterThanOrEqual(0)
            expect(label.right).toBeLessThanOrEqual(width)
          }
          // The native interval policy may omit the final month; if it chooses
          // 2018-10, its actual SVG bounds must still remain contained.
          const maximum = labels.find((label) => label.text === '2018-10')
          if (maximum) expect(maximum.right).toBeLessThanOrEqual(width)

          const ordered = [...labels].sort((left, right) => left.x - right.x)
          for (let index = 1; index < ordered.length; index++) {
            expect(ordered[index - 1]!.right, `${ordered[index - 1]!.text} overlaps ${ordered[index]!.text}`).toBeLessThanOrEqual(ordered[index]!.x + 0.001)
          }
        } finally {
          chart.dispose()
        }
      }
    }
  }
})

test('authored diagonal and vertical rotations keep their existing endpoint behavior', () => {
  for (const [rotation, degrees] of [['diagonal', 45], ['vertical', 90]] as const) {
    const envelope = areaEnvelope()
    envelope.spec.axes = [{
      id: 'x', type: 'automatic', inversion: 'normal', ticks: 'automatic', grid: 'automatic', labelRotation: rotation,
      dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'normal',
    }]
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.xAxis.axisLabel.rotate).toBe(degrees)
    expect(option.xAxis.axisLabel.alignMinLabel).toBeUndefined()
    expect(option.xAxis.axisLabel.alignMaxLabel).toBeUndefined()
    expect(option.xAxis.axisLabel.showMinLabel).toBeUndefined()
    expect(option.xAxis.axisLabel.showMaxLabel).toBeUndefined()
  }
})

test('authored time axes and split-series identities remain unchanged', () => {
  const temporal = areaEnvelope()
  temporal.spec.datasets[0].fields[0].dataType = 'date'
  temporal.spec.axes = [{
    id: 'x', type: 'time', inversion: 'normal', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic',
    dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'normal',
  }]
  const temporalOption = echartsOption(temporal, defaultRendererContext) as any
  expect(temporalOption.xAxis.type).toBe('time')
  expect(temporalOption.xAxis.axisLabel.alignMinLabel).toBeUndefined()
  expect(temporalOption.xAxis.axisLabel.alignMaxLabel).toBeUndefined()
  expect(temporalOption.grid).toMatchObject({ containLabel: false, outerBoundsMode: 'same', outerBoundsContain: 'all' })

  const titled = areaEnvelope()
  titled.spec.axes = [{
    id: 'x', title: 'Month', type: 'automatic', inversion: 'normal', ticks: 'automatic', grid: 'automatic', labelRotation: 'horizontal',
    dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'normal',
  }]
  const titledOption = echartsOption(titled, defaultRendererContext) as any
  expect(titledOption.xAxis.name).toBe('Month')
  expect(titledOption.grid).toMatchObject({ containLabel: false, outerBoundsMode: 'same', outerBoundsContain: 'all' })

  const split = areaEnvelope()
  split.spec.datasets[0].fields = [
    { id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'label' },
    { id: 'series', role: 'dimension', dataType: 'string', nullable: false, label: 'series' },
    { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'value' },
  ]
  split.spec.series = { dataset: 'primary', field: 'series' }
  split.dataState.datasets[0].columns = ['label', 'series', 'value']
  split.dataState.datasets[0].rows = [['2016-09', 'A', 1], ['2018-10', 'A', 2], ['2016-09', 'B', 3], ['2018-10', 'B', 4]]
  const splitOption = echartsOption(split, defaultRendererContext) as any
  expect(splitOption.grid).toMatchObject({ containLabel: false, outerBoundsMode: 'same', outerBoundsContain: 'all' })
  expect(splitOption.xAxis.axisLabel.alignMinLabel).toBeUndefined()
  expect(splitOption.xAxis.axisLabel.alignMaxLabel).toBeUndefined()
  expect(splitOption.series.filter((series: any) => !series.silent).map((series: any) => series.id)).toEqual([
    'series:primary:series:string%3AA', 'series:primary:series:string%3AB',
  ])
})

test('empty and single-category line data retain native centered endpoint behavior', () => {
  for (const [label, rows] of [
    ['', []],
    ['Short', [['Short', 589.67]]],
    ['Very long category name number 0', [['Very long category name number 0', 589.67]]],
  ] as const) {
    const envelope = areaEnvelope()
    envelope.dataState.datasets[0].rows = rows
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.xAxis.axisLabel.alignMinLabel).toBeUndefined()
    expect(option.xAxis.axisLabel.alignMaxLabel).toBeUndefined()
    expect(option.xAxis.axisLabel.showMinLabel).toBeUndefined()
    expect(option.xAxis.axisLabel.showMaxLabel).toBeUndefined()
    const chart = render(envelope, 320)
    try {
      const labels = xAxisLabels(chart, label ? [label] : []).filter((label) => !label.ignore)
      expect(labels.length).toBe(rows.length)
      for (const label of labels) {
        expect(label.x).toBeGreaterThanOrEqual(0)
        expect(label.right).toBeLessThanOrEqual(320)
      }
    } finally {
      chart.dispose()
    }
  }
})

test('long single-category and zoomed-to-one labels stay inside the SVG bounds', () => {
  const singleRows = [
    ['Short', 589.67],
    ['Very long category name number 0', 589.67],
  ] as const
  for (const [label, value] of singleRows) {
    const envelope = areaEnvelope()
    envelope.dataState.datasets[0].rows = [[label, value]]
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.grid).toMatchObject({ containLabel: false, outerBoundsMode: 'same', outerBoundsContain: 'all' })
    const chart = render(envelope, 320)
    try {
      const labels = xAxisLabels(chart, [label]).filter((item) => !item.ignore)
      expect(labels).toHaveLength(1)
      expect(labels[0]!.x).toBeGreaterThanOrEqual(0)
      expect(labels[0]!.right).toBeLessThanOrEqual(320)
    } finally {
      chart.dispose()
    }
  }

  const zoomed = areaEnvelope()
  const zoomLabels = ['Very long category name number 0', 'Very long category name number 1'] as const
  zoomed.spec.presentation.dataZoom = true
  zoomed.dataState.datasets[0].rows = zoomLabels.map((label, index) => [label, index + 1])
  const chart = render(zoomed, 320)
  try {
    chart.dispatchAction({ type: 'dataZoom', dataZoomIndex: 0, startValue: 0, endValue: 0 })
    chart.renderToSVGString()
    const labels = xAxisLabels(chart, [...zoomLabels]).filter((item) => !item.ignore)
    expect(labels.length).toBeGreaterThan(0)
    for (const label of labels) {
      expect(label.x).toBeGreaterThanOrEqual(0)
      expect(label.right).toBeLessThanOrEqual(320)
    }
  } finally {
    chart.dispose()
  }
})
