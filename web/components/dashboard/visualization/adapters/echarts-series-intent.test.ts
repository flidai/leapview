import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption, interactionCommandForRow } from './echarts'
import { CategoryColorRegistry } from './echarts/category-colors'
import visualDocumentation from '../../../../../docs/visuals/examples.gen.json'

test('ECharts normalizes stacks and preserves series order and color identity across filters', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.spec.presentation.stacking = 'percent'
  envelope.spec.presentation.seriesIntent = [
    { value: 'delivered', order: 0, color: 'success' },
    { value: 'processing', order: 1, color: 'data_3' },
    { value: 'canceled', order: 2, color: 'danger' },
  ]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series.map((series: any) => series.name)).toEqual(['delivered', 'processing'])
  expect(option.series.map((series: any) => series.itemStyle.color)).toEqual([
    defaultRendererContext.colors.success,
    defaultRendererContext.colors.data[2],
  ])
  expect(option.series.map((series: any) => series.stack)).toEqual(['percent', 'percent'])
  expect(option.series.map((series: any) => series.encode.y)).toEqual(['__lv_percent_value', '__lv_percent_value'])
  expect(option.dataset[1].source).toEqual([
    ['label', 'series', 'value', '__lv_percent_value'],
    ['Jan', 'delivered', 10, 25],
    ['Feb', 'delivered', 30, 75],
  ])
  expect(option.dataset[2].source).toEqual([
    ['label', 'series', 'value', '__lv_percent_value'],
    ['Jan', 'processing', 30, 75],
    ['Feb', 'processing', 10, 25],
  ])
  expect(option.yAxis.axisLabel.formatter(25)).toBe('25%')

  const darkContext = {
    ...defaultRendererContext,
    theme: 'dark',
    colors: { ...defaultRendererContext.colors, success: '#2ea043', data: ['#1f6feb', '#a371f7', '#d29922'] },
  } as any
  const darkOption = echartsOption(envelope, darkContext) as any
  expect(darkOption.series.map((series: any) => series.itemStyle.color)).toEqual(['#2ea043', '#d29922'])

  const filtered = structuredClone(envelope) as any
  filtered.dataState.datasets[0].rows = filtered.dataState.datasets[0].rows.filter((row: unknown[]) => row[1] === 'processing')
  const filteredOption = echartsOption(filtered, defaultRendererContext) as any
  expect(filteredOption.series.map((series: any) => [series.name, series.itemStyle.color])).toEqual([
    ['processing', defaultRendererContext.colors.data[2]],
  ])
  expect(filteredOption.dataset[1].source.map((row: unknown[]) => row.at(-1))).toEqual(['__lv_percent_value', 100, 100])

  delete envelope.spec.presentation.seriesIntent
  const categoryColors = new CategoryColorRegistry()
  const automatic = echartsOption(envelope, defaultRendererContext, categoryColors) as any
  const automaticProcessing = automatic.series.find((series: any) => series.name === 'processing').itemStyle.color
  delete filtered.spec.presentation.seriesIntent
  const automaticFiltered = echartsOption(filtered, defaultRendererContext, categoryColors) as any
  expect(automaticFiltered.series[0].itemStyle.color).toBe(automaticProcessing)
})

test('ECharts split series keep the generated source category domain in both orientations', () => {
  const source = (visualDocumentation as any).documents['visuals/line'].find((candidate: any) => candidate.visualID === 'revenue_line_status')
  if (!source) throw new Error('generated revenue_line_status fixture is missing')
  const expected = Array.from({ length: 12 }, (_, index) => `2025-${String(index + 1).padStart(2, '0')}`)

  for (const horizontal of [false, true]) {
    const envelope = structuredClone(source) as any
    if (horizontal) envelope.spec.presentation.orientation = 'horizontal'
    const option = echartsOption(envelope, defaultRendererContext) as any
    const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 800, height: 400 })
    try {
      chart.setOption(option)
      chart.renderToSVGString()
      const model = chart.getModel()
      const categoryAxis = model.getComponent(horizontal ? 'yAxis' : 'xAxis').axis
      expect(categoryAxis.scale.getOrdinalMeta().categories, horizontal ? 'horizontal category domain' : 'vertical category domain').toEqual(expected)
      for (let index = 0; index < model.getSeriesCount(); index++) {
        const data = model.getSeriesByIndex(index).getData() as any
        const points = Array.from(data._layout?.points ?? []) as number[]
        const positions = points.filter((_, pointIndex) => pointIndex % 2 === (horizontal ? 1 : 0))
        const direction = Math.sign((positions.at(-1) ?? 0) - (positions[0] ?? 0)) || 1
        expect(positions.every((position, positionIndex) => positionIndex === 0 || (position - positions[positionIndex - 1]!) * direction >= -0.001), `${horizontal ? 'horizontal' : 'vertical'} ${model.getSeriesByIndex(index).name} geometry`).toBe(true)
      }
    } finally {
      chart.dispose()
    }
  }
})

test('ECharts split category domains preserve authored row order and typed/null identities', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.dataState.datasets[0].rows = [
    ['B', 1, 10], ['A', '1', 20], ['B', '1', 30], ['C', null, 40],
  ]
  envelope.spec.presentation.seriesIntent = [{ value: '1', order: 0 }, { value: '(null)', order: 1 }]
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.xAxis.data).toEqual([{ value: 'B' }, { value: 'A' }, { value: 'C' }])
  expect(option.series.filter((series: any) => series.datasetId).map((series: any) => series.name)).toEqual(['1 [string:1]', '(null)', '1 [number:1]'])

  const nullCategory = structuredClone(envelope) as any
  nullCategory.dataState.datasets[0].rows = [['B', 1, 10], [null, '1', 20]]
  expect((echartsOption(nullCategory, defaultRendererContext) as any).xAxis.data).toBeUndefined()

  const numeric = structuredClone(envelope) as any
  numeric.dataState.datasets[0].rows = [[1, '1', 10], ['1', '1', 20], [2, null, 30]]
  numeric.spec.datasets[0].fields[0].dataType = 'integer'
  numeric.spec.presentation.seriesIntent = undefined
  const numericOption = echartsOption(numeric, defaultRendererContext) as any
  expect(numericOption.xAxis.data).toEqual([{ value: 1 }, { value: '1' }, { value: 2 }])
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 640, height: 320 })
  try {
    chart.setOption(numericOption)
    chart.renderToSVGString()
    expect(chart.getModel().getComponent('xAxis').axis.scale.getOrdinalMeta().categories).toEqual([1, '1', 2])
  } finally {
    chart.dispose()
  }
})

test('ECharts uses governed static colors for category series and their legends', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.spec.conditionalFormatting = [{
    id: 'status-colors', target: 'series_color', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'field', source: { dataset: 'primary', field: 'series' },
      values: {
        delivered: { color: 'data_1', icon: 'circle' },
        processing: { color: 'data_6', icon: 'circle' },
      },
      nullStyle: { color: 'neutral', icon: 'square' },
      defaultStyle: { color: 'neutral', icon: 'square' },
    },
  }]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series.map((series: any) => [series.name, series.itemStyle.color])).toEqual([
    ['delivered', defaultRendererContext.colors.data[0]],
    ['processing', defaultRendererContext.colors.data[5]],
  ])

  envelope.dataState.datasets[0].rows = envelope.dataState.datasets[0].rows.filter((row: unknown[]) => row[1] === 'processing')
  const filtered = echartsOption(envelope, defaultRendererContext) as any
  expect(filtered.series[0].itemStyle.color).toBe(defaultRendererContext.colors.data[5])
})

test('ECharts keeps typed category-series identities distinct through ordering and filters', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.dataState.datasets[0].rows = [
    ['Jan', 1, 10], ['Jan', '1', 20], ['Feb', 1, 30], ['Feb', '1', 40],
  ]
  envelope.spec.presentation.seriesIntent = [{ value: '1', order: 0, color: 'success' }]

  const option = echartsOption(envelope, defaultRendererContext) as any
  const series = option.series.filter((candidate: any) => candidate.datasetId)
  expect(series.map((candidate: any) => candidate.name)).toEqual(['1 [string:1]', '1 [number:1]'])
  expect(new Set(series.map((candidate: any) => candidate.id)).size).toBe(2)
  expect(new Set(series.map((candidate: any) => candidate.datasetId)).size).toBe(2)
  expect(series.map((candidate: any) => candidate.itemStyle.color)).toEqual([
    defaultRendererContext.colors.success,
    defaultRendererContext.colors.data[0],
  ])
  expect(option.dataset.slice(1).map((dataset: any) => dataset.transform.config['='])).toEqual(['1', 1])
  const reordered = structuredClone(envelope)
  reordered.dataState.datasets[0].rows.reverse()
  expect((echartsOption(reordered, defaultRendererContext) as any).series.filter((candidate: any) => candidate.datasetId).map((candidate: any) => candidate.id)).toEqual(series.map((candidate: any) => candidate.id))

  const canonical = structuredClone(envelope)
  canonical.spec.presentation.seriesIntent = [{ value: '1 [number:1]', order: 0, color: 'danger' }]
  const canonicalOption = echartsOption(canonical, defaultRendererContext) as any
  expect(canonicalOption.series.filter((candidate: any) => candidate.datasetId).map((candidate: any) => candidate.name)).toEqual(['1 [number:1]', '1 [string:1]'])
  expect(canonicalOption.dataset.slice(1).map((dataset: any) => dataset.transform.config['='])).toEqual([1, '1'])

  envelope.spec.datasets[0].fields[1].role = 'identity'
  envelope.spec.interactions = [{
    id: 'point_selection', kind: 'select', mode: 'single', requiresStableIdentity: true, targets: ['details'], mappings: [
      { source: { dataset: 'primary', field: 'series' }, targetFieldID: 'orders.series', targetDatasetID: 'orders' },
    ],
  }]
  expect(interactionCommandForRow(envelope, 'primary', envelope.dataState.datasets[0].rows[0])).toMatchObject({ mappings: [{ value: 1 }] })
  expect(interactionCommandForRow(envelope, 'primary', envelope.dataState.datasets[0].rows[1])).toMatchObject({ mappings: [{ value: '1' }] })
})

test('ECharts preserves nullish category-series identities and raw filter values', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.dataState.datasets[0].rows = [
    ['Jan', null, 10], ['Jan', undefined, 20], ['Feb', null, 30], ['Feb', undefined, 40],
  ]
  // Nullish values are authored through the renderer's display names because
  // seriesIntent values are strings in the visualization contract.
  envelope.spec.presentation.seriesIntent = [
    { value: '(undefined)', order: 0, color: 'success' },
    { value: '(null)', order: 1, color: 'danger' },
  ]

  const option = echartsOption(envelope, defaultRendererContext) as any
  const series = option.series.filter((candidate: any) => candidate.datasetId)
  expect(series.map((candidate: any) => candidate.name)).toEqual(['(undefined)', '(null)'])
  expect(new Set(series.map((candidate: any) => candidate.id)).size).toBe(2)
  expect(new Set(series.map((candidate: any) => candidate.datasetId)).size).toBe(2)
  expect(series.map((candidate: any) => candidate.itemStyle.color)).toEqual([
    defaultRendererContext.colors.success,
    defaultRendererContext.colors.danger,
  ])
  expect(option.dataset.slice(1).map((dataset: any) => dataset.transform.config['='])).toEqual([undefined, null])
})

test('ECharts lets a single numeric category resolve a string series intent', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.dataState.datasets[0].rows = [['Jan', 7, 10], ['Feb', 7, 20]]
  envelope.spec.presentation.seriesIntent = [{ value: '7', order: 0, color: 'danger' }]

  const option = echartsOption(envelope, defaultRendererContext) as any
  const series = option.series.find((candidate: any) => candidate.datasetId)
  expect(series.name).toBe('7')
  expect(series.itemStyle.color).toBe(defaultRendererContext.colors.danger)
  expect(option.dataset[1].transform.config['=']).toBe(7)
})

test('ECharts keeps fallback multi-metric colors bound to fields when order changes', () => {
  const envelope = cartesianFixture('line', ['label', 'revenue', 'cost']) as any
  const initial = echartsOption(envelope, defaultRendererContext) as any
  const initialColors = new Map(initial.series.map((series: any) => [series.name, series.itemStyle.color]))

  envelope.spec.presentation.seriesIntent = [
    { value: 'cost', order: 0 },
    { value: 'revenue', order: 1 },
  ]
  const reordered = echartsOption(envelope, defaultRendererContext) as any
  expect(reordered.series.map((series: any) => series.name)).toEqual(['cost', 'revenue'])
  expect(reordered.series.map((series: any) => [series.name, series.itemStyle.color])).toEqual([
    ['cost', initialColors.get('cost')], ['revenue', initialColors.get('revenue')],
  ])
})

test('ECharts places orderless static intents before unconfigured measures', () => {
  const envelope = cartesianFixture('line', ['label', 'revenue', 'cost']) as any
  envelope.spec.presentation.seriesIntent = [{ value: 'cost' }]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series.map((series: any) => series.name)).toEqual(['cost', 'revenue'])
  expect(option.series.map((series: any) => series.itemStyle.color)).toEqual([
    defaultRendererContext.colors.data[1], defaultRendererContext.colors.data[0],
  ])
})

test('ECharts preserves conditional-over-intent-over-palette precedence for category series', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.spec.presentation.seriesIntent = [{ value: 'delivered', color: 'success' }, { value: 'processing', color: 'warning' }]
  envelope.spec.conditionalFormatting = [{
    id: 'status-colors', target: 'series_color', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'field', source: { dataset: 'primary', field: 'series' },
      values: { delivered: { color: 'danger' } },
      nullStyle: {}, defaultStyle: {},
    },
  }]

  const option = echartsOption(envelope, defaultRendererContext) as any
  const processing = option.series.find((series: any) => series.name === 'processing').itemStyle.color
  expect(option.series.find((series: any) => series.name === 'delivered').itemStyle.color).toBe(defaultRendererContext.colors.danger)
  expect(typeof processing === 'function' ? processing({ value: ['Jan', 'processing', 30] }) : processing).toBe(defaultRendererContext.colors.attention)
})

test('ECharts condenses crowded category-series charts without overlapping labels or legends', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.spec.mark = 'column'
  envelope.spec.presentation.stacked = false
  envelope.spec.presentation.stacking = 'none'
  const states = ['MG', 'CE', 'GO', 'MT', 'PE', 'RJ', 'RS', 'AP']
  const statuses = ['approved', 'canceled', 'created', 'delivered', 'invoiced', 'processing', 'shipped', 'unavailable']
  envelope.dataState.datasets[0].rows = states.flatMap((state: string, stateIndex: number) =>
    statuses.map((status: string, statusIndex: number) => [state, status, (stateIndex + 1) * (statusIndex + 1)]),
  )

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.legend).toMatchObject({ type: 'scroll', orient: 'horizontal', left: 8, right: 8, height: 24, bottom: 0 })
  expect(option.grid).toMatchObject({ left: 12, right: 16, top: 16, bottom: 44, containLabel: true })
  expect(option.yAxis).toMatchObject({ splitNumber: 4, axisLabel: { hideOverlap: true } })
  expect(option.series).toHaveLength(8)
  expect(option.series.every((series: any) => series.label.show === false)).toBe(true)

  const compact = echartsOption(cartesianSeriesFixture(), defaultRendererContext) as any
  expect(compact.legend.type).toBeUndefined()
  expect(compact.series.every((series: any) => series.label.show === true)).toBe(true)
})

test('ECharts keeps category-series conditional icon cues visible through crowding and percent labels', () => {
  const envelope = cartesianSeriesFixture() as any
  envelope.spec.mark = 'column'
  envelope.spec.presentation.stacked = false
  envelope.spec.presentation.stacking = 'none'
  envelope.spec.conditionalFormatting = [{
    id: 'value-icon', target: 'icon', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'arrow_up' } }],
      nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'arrow_down' },
    },
  }]
  const states = ['MG', 'CE', 'GO', 'MT', 'PE', 'RJ', 'RS', 'AP']
  const statuses = ['approved', 'canceled', 'created', 'delivered', 'invoiced', 'processing', 'shipped', 'unavailable']
  envelope.dataState.datasets[0].rows = states.flatMap((state: string, stateIndex: number) =>
    statuses.map((status: string, statusIndex: number) => [state, status, (stateIndex + 1) * (statusIndex + 1)]),
  )

  const crowded = echartsOption(envelope, defaultRendererContext) as any
  expect(crowded.series.every((series: any) => series.label.show === true)).toBe(true)
  expect(crowded.series.every((series: any) => series.labelLayout.hideOverlap === false)).toBe(true)
  expect(crowded.series[0].label.formatter({ value: ['MG', 'approved', 1] })).toBe('↑ 1')

  envelope.spec.presentation.stacking = 'percent'
  envelope.spec.presentation.labelPolicy.density = 'hidden'
  const percent = echartsOption(envelope, defaultRendererContext) as any
  const delivered = percent.series.find((series: any) => series.name === 'approved')
  expect(delivered).toMatchObject({ label: { show: true }, labelLayout: { hideOverlap: false } })
  expect(delivered.label.formatter({ value: ['MG', 'approved', 1, 2.7777777777777777] })).toBe('↑ 2.8%')
})

test('ECharts normalizes multi-metric percent stacks without changing raw tooltip values', () => {
  const envelope = cartesianFixture('area', ['label', 'revenue', 'cost']) as any
  envelope.spec.presentation.stacked = false
  envelope.spec.presentation.stacking = 'percent'
  envelope.spec.presentation.labelPolicy.density = 'hidden'
  envelope.spec.presentation.seriesIntent = [
    { value: 'cost', order: 0, color: 'warning' },
    { value: 'revenue', order: 1, color: 'success' },
  ]
  envelope.dataState.datasets[0].rows = [
    ['Jan', 10, 30],
    ['Feb', 30, 10],
  ]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series.map((series: any) => series.name)).toEqual(['cost', 'revenue'])
  expect(option.series.map((series: any) => series.encode.y)).toEqual(['__lv_percent_cost', '__lv_percent_revenue'])
  expect(option.series.map((series: any) => series.itemStyle.color)).toEqual([
    defaultRendererContext.colors.attention,
    defaultRendererContext.colors.success,
  ])
  expect(option.series.every((series: any) => series.label.show === false)).toBe(true)
  expect(option.series[0].label.formatter({ value: ['Jan', 10, 30, 75, 25] })).toBe('75%')
  expect(option.series[1].label.formatter({ value: ['Jan', 10, 30, 75, 25] })).toBe('25%')
  expect(option.dataset.source).toEqual([
    ['label', 'revenue', 'cost', '__lv_percent_cost', '__lv_percent_revenue'],
    ['Jan', 10, 30, 75, 25],
    ['Feb', 30, 10, 25, 75],
  ])
})

test('ECharts preserves conditional icon and label color on percent-stack labels', () => {
  const dark = { ...defaultRendererContext, theme: 'dark' as const, colors: { ...defaultRendererContext.colors, attention: '#d29922' } }
  const format = {
    id: 'cost-status', target: 'label_foreground', field: { dataset: 'primary', field: 'cost' },
    rule: {
      kind: 'rules',
      rules: [{ operator: 'greater_or_equal', value: 0, style: { color: 'warning', icon: 'arrow_up' } }],
      nullStyle: { color: 'neutral', icon: 'warning' }, defaultStyle: { color: 'danger', icon: 'arrow_down' },
    },
  }

  const multiMetric = cartesianFixture('area', ['label', 'revenue', 'cost']) as any
  multiMetric.spec.presentation.stacked = false
  multiMetric.spec.presentation.stacking = 'percent'
  multiMetric.spec.presentation.labelPolicy.density = 'always'
  multiMetric.spec.presentation.seriesIntent = [{ value: 'cost', order: 0 }, { value: 'revenue', order: 1 }]
  multiMetric.dataState.datasets[0].rows = [['Jan', 10, 30]]
  multiMetric.spec.conditionalFormatting = [format]
  const multiOption = echartsOption(multiMetric, dark) as any
  const multiLabel = multiOption.series[0].label
  expect(multiLabel.formatter({ value: ['Jan', 10, 30, 75, 25] })).toBe('↑ 75%')
  expect(multiLabel.color({ value: ['Jan', 10, 30, 75, 25] })).toBe(dark.colors.attention)

  const categorySeries = cartesianSeriesFixture() as any
  categorySeries.spec.presentation.stacking = 'percent'
  categorySeries.spec.presentation.labelPolicy.density = 'always'
  categorySeries.spec.conditionalFormatting = [{ ...format, id: 'value-status', field: { dataset: 'primary', field: 'value' } }]
  const categoryOption = echartsOption(categorySeries, dark) as any
  const categoryLabel = categoryOption.series.find((series: any) => series.name === 'delivered').label
  expect(categoryLabel.formatter({ value: ['Jan', 'delivered', 10, 25] })).toBe('↑ 25%')
  expect(categoryLabel.color({ value: ['Jan', 'delivered', 10, 25] })).toBe(dark.colors.attention)
})

test('ECharts translation emits one multi-value financial series', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'ohlc', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'OHLC', mark: 'candlestick',
      datasets: [{ id: 'primary', fields: ['label', 'open', 'close', 'low', 'high'].map((id, index) => ({ id, role: index ? 'metric' : 'dimension', dataType: index ? 'decimal' : 'string', nullable: false, label: id })) }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'OHLC', description: 'OHLC' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: ['open', 'close', 'low', 'high'].map((field) => ({ dataset: 'primary', field })),
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: false, dataZoom: true, area: false, step: false },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [
      { id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'open', 'close', 'low', 'high'], rows: [['Jan', 1, 2, 0, 3]], completeness: 'complete' },
    ] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  const option = echartsOption(envelope) as any
  expect(option.series).toHaveLength(1)
  expect(option.xAxis.data).toEqual(['Jan'])
  expect(option.series[0].label).toBeUndefined()
  expect(option.series[0].encode).toBeUndefined()
  expect(option.series[0].data).toEqual([{
    name: 'Jan', value: [1, 2, 0, 3], __lv_dataset: 'primary', __lv_row_index: 0,
  }])
})

test('ECharts candlestick colors use authored intents and a neutral equal-value fallback', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'ohlc-colors', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'OHLC', mark: 'candlestick',
      datasets: [{ id: 'primary', fields: ['label', 'open', 'close', 'low', 'high'].map((id, index) => ({ id, role: index ? 'metric' : 'dimension', dataType: index ? 'decimal' : 'string', nullable: false, label: id })) }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'OHLC', description: 'OHLC' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: ['open', 'close', 'low', 'high'].map((field) => ({ dataset: 'primary', field })),
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: false, dataZoom: false, area: false, step: false, gainColor: 'data_2', lossColor: 'warning' },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [
      { id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'open', 'close', 'low', 'high'], rows: [['Gain', 1, 2, 0, 3], ['Loss', 2, 1, 0, 3], ['Flat', 2, 2, 1, 3]], completeness: 'complete' },
    ] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].itemStyle).toEqual({
    color: defaultRendererContext.colors.data[1],
    color0: defaultRendererContext.colors.attention,
    borderColor: defaultRendererContext.colors.data[1],
    borderColor0: defaultRendererContext.colors.attention,
    borderColorDoji: defaultRendererContext.colors.muted,
  })
  expect(option.series[0].data[0].itemStyle).toBeUndefined()
  expect(option.series[0].data[1].itemStyle).toBeUndefined()
  expect(option.series[0].data[2].itemStyle).toEqual({
    color: defaultRendererContext.colors.muted,
    color0: defaultRendererContext.colors.muted,
    borderColor: defaultRendererContext.colors.muted,
    borderColor0: defaultRendererContext.colors.muted,
    borderColorDoji: defaultRendererContext.colors.muted,
  })
  expect(option.series[0].data.map((row: any) => row.__lv_row_index)).toEqual([0, 1, 2])
  expect((echartsOption(envelope, { ...defaultRendererContext, theme: 'dark', colors: { ...defaultRendererContext.colors, data: ['#111111', '#222222'], attention: '#aaaaaa', muted: '#bbbbbb' } }) as any).series[0].itemStyle).toEqual({
    color: '#222222', color0: '#aaaaaa', borderColor: '#222222', borderColor0: '#aaaaaa', borderColorDoji: '#bbbbbb',
  })
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 640, height: 320 })
  try {
    chart.setOption(option)
    const paths = [...chart.renderToSVGString().matchAll(/<path\b[^>]*>/g)].map((match) => match[0])
    expect(paths.some((path) => path.includes(`fill=\"${defaultRendererContext.colors.data[1]}\"`) && path.includes(`stroke=\"${defaultRendererContext.colors.data[1]}\"`))).toBe(true)
    expect(paths.some((path) => path.includes(`fill=\"${defaultRendererContext.colors.attention}\"`) && path.includes(`stroke=\"${defaultRendererContext.colors.attention}\"`))).toBe(true)
    expect(paths.some((path) => path.includes(`fill=\"${defaultRendererContext.colors.muted}\"`) && path.includes(`stroke=\"${defaultRendererContext.colors.muted}\"`))).toBe(true)
  } finally {
    chart.dispose()
  }
})

test('ECharts renders candlestick colors in large mode, including equal-value neutral strokes', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'ohlc-large-colors', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'OHLC', mark: 'candlestick',
      datasets: [{ id: 'primary', fields: ['label', 'open', 'close', 'low', 'high'].map((id, index) => ({ id, role: index ? 'metric' : 'dimension', dataType: index ? 'decimal' : 'string', nullable: false, label: id })) }],
      dataBudget: { maxRows: 1000, requiredCompleteness: 'complete' }, accessibility: { title: 'OHLC', description: 'OHLC' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: ['open', 'close', 'low', 'high'].map((field) => ({ dataset: 'primary', field })),
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: false, dataZoom: false, area: false, step: false, gainColor: 'success', lossColor: 'danger' },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [
      { id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'open', 'close', 'low', 'high'], rows: Array.from({ length: 601 }, (_, index) => {
        if (index === 0) return [String(index), 1, 2, 0, 3]
        if (index === 1) return [String(index), 2, 1, 0, 3]
        if (index === 2 || index === 600) return [String(index), 2, 2, 1, 3]
        if (index === 3) return [String(index), null, null, null, null]
        return [String(index), 1, 2, 0, 3]
      }), completeness: 'complete' },
    ] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].large).toBeUndefined()
  expect(option.series[0].data).toHaveLength(601)
  expect(option.series[0].data[3]).toMatchObject({ value: [null, null, null, null], __lv_row_index: 3 })
  expect(option.series[0].data[3].itemStyle).toBeUndefined()
  expect(option.series[0].data[600].itemStyle.color).toBe(defaultRendererContext.colors.muted)
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 1200, height: 320 })
  try {
    chart.setOption(option)
    const svg = chart.renderToSVGString()
    const paths = [...svg.matchAll(/<path\b[^>]*>/g)].map((match) => match[0])
    expect(paths.some((path) => path.includes(`stroke=\"${defaultRendererContext.colors.success}\"`))).toBe(true)
    expect(paths.some((path) => path.includes(`stroke=\"${defaultRendererContext.colors.danger}\"`))).toBe(true)
    expect(paths.some((path) => path.includes(`stroke=\"${defaultRendererContext.colors.muted}\"`))).toBe(true)
  } finally {
    chart.dispose()
  }
})

test('ECharts keeps exact decimal candlestick direction in normal rendering above the large threshold', () => {
  const rows: unknown[][] = [
    ['Gain', '100000000000000000000.1', '100000000000000000000.2', '100000000000000000000', '100000000000000000000.3'],
    ['Loss', '100000000000000000000.2', '100000000000000000000.1', '100000000000000000000', '100000000000000000000.3'],
    ['Flat', '100000000000000000000.1', '100000000000000000000.10', '100000000000000000000', '100000000000000000000.3'],
    ['Null', null, null, null, null],
    ['Invalid', 'not-a-decimal', '0', '0', '1'],
    ['Mixed small', 1e-7, '0.00000011', 0, 0.0000002],
    ['Mixed large', 1e21, '1000000000000000000000.1', '1000000000000000000000', '1000000000000000000001'],
  ]
  for (let index = rows.length; index < 601; index++) rows.push([String(index), 1, 2, 0, 3])
  const envelope = {
    schemaVersion: 9, visualID: 'ohlc-exact-colors', rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'OHLC', mark: 'candlestick',
      datasets: [{ id: 'primary', fields: ['label', 'open', 'close', 'low', 'high'].map((id, index) => ({ id, role: index ? 'metric' : 'dimension', dataType: index ? 'decimal' : 'string', nullable: true, label: id })) }],
      dataBudget: { maxRows: 1000, requiredCompleteness: 'complete' }, accessibility: { title: 'OHLC', description: 'OHLC' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: ['open', 'close', 'low', 'high'].map((field) => ({ dataset: 'primary', field })),
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: false, dataZoom: false, area: false, step: false, gainColor: 'success', lossColor: 'danger' },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [
      { id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'open', 'close', 'low', 'high'], rows, completeness: 'complete' },
    ] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
  const option = echartsOption(envelope, defaultRendererContext) as any
  const style = (color: string) => ({ color, color0: color, borderColor: color, borderColor0: color, borderColorDoji: color })
  expect(option.series[0].large).toBe(false)
  expect(option.series[0].data.slice(0, 4).map((row: any) => row.itemStyle)).toEqual([
    style(defaultRendererContext.colors.success),
    style(defaultRendererContext.colors.danger),
    style(defaultRendererContext.colors.muted),
    undefined,
  ])
  expect(option.series[0].data[4].itemStyle).toBeUndefined()
  expect(option.series[0].data[5].itemStyle).toBeUndefined()
  expect(option.series[0].data[6].itemStyle).toEqual(style(defaultRendererContext.colors.success))
  expect(option.series[0].data[0].value).toEqual(rows[0].slice(1))
  expect(option.series[0].data[0].__lv_row_index).toBe(0)

  const dark = { ...defaultRendererContext, theme: 'dark' as const, colors: { ...defaultRendererContext.colors, success: '#2ea043', danger: '#f85149', muted: '#8b949e' } }
  const darkOption = echartsOption(envelope, dark) as any
  expect(darkOption.series[0].data.slice(0, 3).map((row: any) => row.itemStyle.color)).toEqual(['#2ea043', '#f85149', '#8b949e'])

  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 1200, height: 320 })
  try {
    chart.setOption(option)
    const paths = [...chart.renderToSVGString().matchAll(/<path\b[^>]*>/g)].map((match) => match[0])
    expect(paths.some((path) => path.includes(`fill=\"${defaultRendererContext.colors.success}\"`) && path.includes(`stroke=\"${defaultRendererContext.colors.success}\"`))).toBe(true)
    expect(paths.some((path) => path.includes(`fill=\"${defaultRendererContext.colors.danger}\"`) && path.includes(`stroke=\"${defaultRendererContext.colors.danger}\"`))).toBe(true)
    expect(paths.some((path) => path.includes(`fill=\"${defaultRendererContext.colors.muted}\"`) && path.includes(`stroke=\"${defaultRendererContext.colors.muted}\"`))).toBe(true)
  } finally {
    chart.dispose()
  }
})

function cartesianFixture(mark: string, columns = ['label', 'value']): VisualizationEnvelope {
  const fields = columns.map((id, index) => ({ id, role: index === 0 ? 'dimension' : 'metric', dataType: index === 0 || id === 'row' ? 'string' : 'decimal', nullable: false, label: id }))
  const y = columns.slice(1).map((field) => ({ dataset: 'primary', field }))
  const row = columns.map((id, index) => index === 0 ? 'A' : id === 'row' ? 'R1' : index)
  return {
    schemaVersion: 9, visualID: mark, rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: { kind: 'cartesian', title: mark, mark, datasets: [{ id: 'primary', fields }], dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: mark, description: mark }, interactions: [], x: { dataset: 'primary', field: 'label' }, y, presentation: { legend: 'bottom', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, smooth: true, stacked: true, showSymbols: false, dataZoom: true, area: mark === 'area', step: true, symbolSize: 12, labelPosition: 'top', orientation: mark === 'bar' ? 'horizontal' : 'vertical', histogramBins: mark === 'histogram' ? 10 : undefined } },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns, rows: [row], completeness: 'complete' }] }, selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

function cartesianSeriesFixture(): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: 'series', rendererID: 'echarts', specRevision: 'sha256:series', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'Orders', mark: 'area',
      datasets: [{ id: 'primary', fields: [
        { id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'Month' },
        { id: 'series', role: 'dimension', dataType: 'string', nullable: false, label: 'Status' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Orders' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Orders by status' }, interactions: [],
      x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], series: { dataset: 'primary', field: 'series' },
      presentation: { legend: 'bottom', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, smooth: false, stacked: false, stacking: 'normal', showSymbols: true, dataZoom: false, area: true, step: false },
    },
    dataState: {
      kind: 'inline', specRevision: 'sha256:series', dataRevision: 1, generation: 1,
      datasets: [{
        id: 'primary', specRevision: 'sha256:series', dataRevision: 1, generation: 1,
        columns: ['label', 'series', 'value'],
        rows: [['Jan', 'processing', 30], ['Jan', 'delivered', 10], ['Feb', 'processing', 10], ['Feb', 'delivered', 30]],
        completeness: 'complete',
      }],
    },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}
