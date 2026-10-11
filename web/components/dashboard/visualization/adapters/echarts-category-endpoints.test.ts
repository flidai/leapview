import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import { defaultRendererContext } from '../host-controller'
import { echartsOption, responsiveEChartsPatch } from './echarts'
import { cartesianFixture } from './echarts-test-fixtures'

const months = Array.from({ length: 26 }, (_, index) => {
  const date = new Date(Date.UTC(2016, 8 + index, 1))
  return date.toISOString().slice(0, 7)
})

function areaEnvelope(mark: 'line' | 'area' = 'area') {
  const envelope = cartesianFixture(mark) as any
  envelope.spec.presentation.dataZoom = false
  envelope.spec.presentation.legend = 'hidden'
  envelope.dataState.datasets[0].rows = months.map((month, index) => [month, index + 1])
  return envelope
}

function visibleXAxisBounds(chart: echarts.EChartsType) {
  const axis = (chart as any).getModel().getComponent('xAxis').axis
  return axis.axisBuilder.group.children()[0].children()
    .filter((label: any) => label.type === 'text' && months.includes(String(label.style?.text)) && label.ignore !== true)
    .map((label: any) => {
      const rect = label.getBoundingRect().clone()
      const transform = label.getComputedTransform()
      if (transform) rect.applyTransform(transform)
      return { text: String(label.style.text), x: rect.x, right: rect.x + rect.width }
    })
}

test('mobile line and area category endpoints stay inside rendered bounds', () => {
  for (const mark of ['line', 'area'] as const) {
    for (const inverse of [false, true]) {
      for (const width of [294, 364]) {
        const envelope = areaEnvelope(mark)
        envelope.spec.axes = [{
          id: 'x', type: 'automatic', inversion: inverse ? 'inverted' : 'normal',
          ticks: 'automatic', grid: 'automatic', labelRotation: 'horizontal', dateUnit: 'automatic',
          scale: 'automatic', zero: 'automatic', tickDensity: 'normal',
        }]
        const option = echartsOption(envelope, defaultRendererContext) as any
        expect(option.grid).toMatchObject({ containLabel: false, outerBoundsMode: 'same', outerBoundsContain: 'all' })
        expect(option.xAxis.axisLabel).toMatchObject({ alignMinLabel: 'left', alignMaxLabel: 'right' })
        const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height: 322 })
        try {
          chart.setOption({ ...option, animation: false })
          chart.renderToSVGString()
          const labels = visibleXAxisBounds(chart)
          expect(labels.length).toBeGreaterThan(0)
          for (const label of labels) {
            expect(label.x, label.text).toBeGreaterThanOrEqual(0)
            expect(label.right, label.text).toBeLessThanOrEqual(width)
          }
          const ordered = [...labels].sort((left, right) => left.x - right.x)
          for (let index = 1; index < ordered.length; index++) {
            expect(ordered[index - 1]!.right).toBeLessThanOrEqual(ordered[index]!.x + 0.001)
          }
        } finally {
          chart.dispose()
        }
      }
    }
  }
})

test('authored rotations, time axes, and titled plots retain their existing layout', () => {
  for (const [rotation, degrees] of [['diagonal', 45], ['vertical', 90]] as const) {
    const envelope = areaEnvelope()
    envelope.spec.axes = [{
      id: 'x', type: 'automatic', inversion: 'normal', ticks: 'automatic', grid: 'automatic', labelRotation: rotation,
      dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'normal',
    }]
    const axisLabel = (echartsOption(envelope, defaultRendererContext) as any).xAxis.axisLabel
    expect(axisLabel.rotate).toBe(degrees)
    expect(axisLabel.alignMinLabel).toBeUndefined()
    expect(axisLabel.alignMaxLabel).toBeUndefined()
  }

  const temporal = areaEnvelope()
  temporal.spec.datasets[0].fields[0].dataType = 'date'
  temporal.spec.axes = [{
    id: 'x', type: 'time', inversion: 'normal', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic',
    dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'normal',
  }]
  expect((echartsOption(temporal, defaultRendererContext) as any).xAxis.type).toBe('time')

  const titled = areaEnvelope()
  titled.spec.axes = [{
    id: 'x', title: 'Month', type: 'automatic', inversion: 'normal', ticks: 'automatic', grid: 'automatic', labelRotation: 'horizontal',
    dateUnit: 'automatic', scale: 'automatic', zero: 'automatic', tickDensity: 'normal',
  }]
  const titledOption = echartsOption(titled, defaultRendererContext) as any
  expect(titledOption.xAxis.name).toBe('Month')
  expect(titledOption.grid).toMatchObject({ containLabel: false, outerBoundsMode: 'same', outerBoundsContain: 'all' })
})

test('category-series line and area charts contain horizontal endpoint labels', () => {
  for (const mark of ['line', 'area'] as const) {
    const envelope = areaEnvelope(mark)
    envelope.spec.series = { dataset: 'primary', field: 'series' }
    envelope.spec.datasets[0].fields.push({ id: 'series', role: 'dimension', dataType: 'string', nullable: false, label: 'Series' })
    envelope.dataState.datasets[0].columns.push('series')
    envelope.dataState.datasets[0].rows = months.flatMap((month, index) => [
      [month, index + 1, 'North'],
      [month, index + 2, 'South'],
    ])
    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.xAxis.axisLabel).toMatchObject({ alignMinLabel: 'left', alignMaxLabel: 'right' })
  }
})


test('boxplot rotated category labels stay inside compact, card, and expanded chart bounds', () => {
  const categories = [
    'books_general_interest', 'books_technical', 'cds_dvds_musicals', 'books_imported',
    'audio', 'bed_bath_table', 'baby', 'auto', 'art', 'air_conditioning',
    'agro_industry_and_commerce', 'home_appliances',
  ]
  for (const dataZoom of [false, true]) {
    const envelope = cartesianFixture('boxplot', ['label', 'min', 'q1', 'median', 'q3', 'max']) as any
    envelope.spec.presentation.dataZoom = dataZoom
    envelope.dataState.datasets[0].rows = categories.map((category, index) => [
      category, index, index + 1, index + 2, index + 3, index + 4,
    ])
    for (const [width, height] of [[364, 322], [432, 457], [800, 500]]) {
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height })
      try {
        chart.setOption({ ...echartsOption(envelope, defaultRendererContext), animation: false })
        chart.renderToSVGString()
        const axis = (chart as any).getModel().getComponent('xAxis').axis
        const labels = axis.axisBuilder.group.children()[0].children()
          .filter((label: any) => label.type === 'text' && label.ignore !== true)
        expect(labels.length).toBeGreaterThan(0)
        for (const label of labels) {
          const bounds = label.getBoundingRect().clone()
          const transform = label.getComputedTransform()
          if (transform) bounds.applyTransform(transform)
          const detail = `${label.style.text} at ${width}x${height}, data zoom ${dataZoom}`
          expect(bounds.x, detail).toBeGreaterThanOrEqual(0)
          expect(bounds.x + bounds.width, detail).toBeLessThanOrEqual(width)
          expect(bounds.y + bounds.height, detail).toBeLessThanOrEqual(height)
        }
        expect(envelope.dataState.datasets[0].rows.map((row: unknown[]) => row[0])).toEqual(categories)
      } finally {
        chart.dispose()
      }
    }
  }
})


test('long boxplot categories retain usable plot space without clipping their display labels', () => {
  const categories = Array.from({ length: 8 }, (_, index) =>
    `Region ${index + 1} — enterprise customers and strategic accounts with an unusually long name`)
  for (const dataZoom of [false, true]) {
    const envelope = cartesianFixture('boxplot', ['label', 'min', 'q1', 'median', 'q3', 'max']) as any
    envelope.spec.presentation.dataZoom = dataZoom
    envelope.dataState.datasets[0].rows = categories.map((category, index) =>
      [category, index, index + 1, index + 2, index + 3, index + 4])
    const option = echartsOption(envelope, defaultRendererContext) as any
    for (const [width, height] of [[351, 384], [432, 457], [800, 500], [1416, 384]]) {
      const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width, height })
      try {
        chart.setOption({ ...option, ...responsiveEChartsPatch(option, width!, height!), animation: false })
        chart.renderToSVGString()
        const model = (chart as any).getModel()
        const plot = model.getComponent('grid').coordinateSystem.getRect()
        expect(plot.width).toBeGreaterThan(width! * 0.3)
        expect(plot.height).toBeGreaterThan(height! * 0.3)
        const labels = model.getComponent('xAxis').axis.axisBuilder.group.children()[0].children()
          .filter((label: any) => label.type === 'text' && !label.ignore)
        expect(labels.length).toBeGreaterThan(0)
        for (const label of labels) {
          const bounds = label.getBoundingRect().clone()
          const transform = label.getComputedTransform()
          if (transform) bounds.applyTransform(transform)
          expect(bounds.x).toBeGreaterThanOrEqual(-0.001)
          expect(bounds.x + bounds.width).toBeLessThanOrEqual(width! + 0.001)
          expect(bounds.y + bounds.height).toBeLessThanOrEqual(height! + 0.001)
        }
        expect(model.getComponent('xAxis').get('axisLabel.rotate')).toBe(option.xAxis.axisLabel.rotate)
        expect(option.xAxis.data).toEqual(categories)
        expect(option.series[0].data.map((item: any) => item.name)).toEqual(categories)
      } finally { chart.dispose() }
    }
  }
})
