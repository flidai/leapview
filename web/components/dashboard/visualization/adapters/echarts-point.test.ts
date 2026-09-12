import { expect, test } from 'bun:test'
import * as echarts from 'echarts'

import type { InlineVisualizationDataState, VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { brushSelectionCommands, echartsOption } from './echarts'
import { CategoryColorRegistry } from './echarts/category-colors'

test('ECharts point axis visibility hides axes and restores native defaults without changing data identity', () => {
  const envelope = pointCategoricalFixture([
    ['p-a', 'A', 1, 10],
    ['p-b', 'B', 2, 20],
  ]) as any
  const defaultOption = echartsOption(envelope, defaultRendererContext) as any
  const hidden = structuredClone(envelope)
  hidden.spec.presentation.axisVisible = false
  const hiddenOption = echartsOption(hidden, defaultRendererContext) as any

  expect(hiddenOption.xAxis.show).toBe(false)
  expect(hiddenOption.yAxis.show).toBe(false)
  expect(defaultOption.xAxis.show).toBeUndefined()
  expect(defaultOption.yAxis.show).toBeUndefined()
  expect(pointDataSignature(hiddenOption)).toEqual(pointDataSignature(defaultOption))

  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 640, height: 320 })
  try {
    chart.setOption(hiddenOption, { notMerge: true, lazyUpdate: false })
    expect((chart.getOption() as any).xAxis[0].show).toBe(false)
    expect((chart.getOption() as any).yAxis[0].show).toBe(false)

    chart.setOption(defaultOption, { notMerge: true, lazyUpdate: false })
    const restored = chart.getOption() as any
    expect(restored.xAxis[0].show).toBe(true)
    expect(restored.yAxis[0].show).toBe(true)
    expect(restored.series.map((series: any) => series.id)).toEqual(defaultOption.series.map((series: any) => series.id))
    expect(restored.dataset.map((dataset: any) => dataset.source)).toEqual(defaultOption.dataset.map((dataset: any) => dataset.source))
  } finally {
    chart.dispose()
  }
})

test('ECharts renders deterministic categorical scatter legends without changing source rows', () => {
  const envelope = pointCategoricalFixture([
    ['p-empty', '', 2, 20],
    ['p-null', null, 1, 10],
    ['p-b', 'B', 3, 30],
    ['p-a', 'A', 4, 40],
  ])
  const source = structuredClone((envelope.dataState as InlineVisualizationDataState).datasets[0])
  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.legend).toMatchObject({ data: ['(null)', '(empty)', 'A', 'B'], selectedMode: 'multiple' })
  expect(option.series.map((series: any) => series.name)).toEqual(['(null)', '(empty)', 'A', 'B'])
  expect(option.series.every((series: any) => !Object.hasOwn(series.encode, 'itemGroupId'))).toBe(true)
  expect(option.series.map((series: any) => series.__lv_source_row_indices)).toEqual([[1], [0], [3], [2]])
  expect(option.dataset[0].source).toEqual([source.columns, ...source.rows])
  expect(option.dataset.slice(1).map((dataset: any) => dataset.transform.config['='])).toEqual([null, '', 'A', 'B'])

  const reordered = structuredClone(envelope)
  ;(reordered.dataState as InlineVisualizationDataState).datasets[0].rows.reverse()
  const reorderedOption = echartsOption(reordered, defaultRendererContext, new CategoryColorRegistry()) as any
  expect(reorderedOption.legend.data).toEqual(option.legend.data)
  expect(reorderedOption.series.map((series: any) => series.name)).toEqual(option.series.map((series: any) => series.name))
  for (const category of option.series) {
    const same = reorderedOption.series.find((candidate: any) => candidate.name === category.name)
    const row = (envelope.dataState as InlineVisualizationDataState).datasets[0].rows[category.__lv_source_row_indices[0]]
    expect(same.itemStyle.color({ value: row })).toBe(category.itemStyle.color({ value: row }))
  }

  expect(brushSelectionCommands(envelope, { batch: [{ selected: [{ seriesIndex: 2, dataIndex: [0] }] }] })[0]?.mappings[0]?.value).toBe('p-a')
  const highlighted = structuredClone(envelope) as any
  highlighted.highlights = [{
    sourceVisualID: 'source', interactionID: 'selection', label: 'A',
    entries: [{ label: 'A', mappings: [{ targetFieldID: 'id', targetDatasetID: 'primary', value: 'p-a', label: 'p-a' }] }],
  }]
  const highlightedOption = echartsOption(highlighted, defaultRendererContext) as any
  expect(highlightedOption.series.find((series: any) => series.name === 'A').itemStyle.opacity({ dataIndex: 0 })).toBe(1)
  expect(highlightedOption.series.find((series: any) => series.name === 'B').itemStyle.opacity({ dataIndex: 0 })).toBe(0.2)
})

test('ECharts keeps empty and display-colliding scatter categories truthful', () => {
  const empty = echartsOption(pointCategoricalFixture([]), defaultRendererContext) as any
  expect(empty.legend.data).toEqual([])
  expect(empty.series).toHaveLength(1)
  expect(empty.dataset).toHaveLength(1)

  const collisions = echartsOption(pointCategoricalFixture([
    ['p-null', null, 1, 10],
    ['p-literal', '(null)', 2, 20],
  ]), defaultRendererContext) as any
  expect(collisions.legend.data).toEqual(['(null) [null:]', '(null) [string:(null)]'])
  expect(new Set(collisions.series.map((series: any) => series.name)).size).toBe(2)
})

test('ECharts gives point mark fill precedence over categorical colors and honors governed symbols in both themes', () => {
  const envelope = pointCategoricalFixture([
    ['p-canceled', 'canceled', 2, 20],
    ['p-ok', 'ok', 1, 10],
    ['p-null', null, 3, 30],
  ]) as any
  envelope.spec.conditionalFormatting = [{
    id: 'point-health', target: 'mark_fill', field: { dataset: 'primary', field: 'category' },
    rule: {
      kind: 'field', source: { dataset: 'primary', field: 'category' },
      values: { canceled: { color: 'danger', icon: 'arrow_down' } },
      nullStyle: { color: 'warning', icon: 'warning' },
      defaultStyle: { color: 'success', icon: 'triangle_up' },
    },
  }]

  const dark = { ...defaultRendererContext, theme: 'dark' as const, colors: { ...defaultRendererContext.colors, danger: '#ff7b72', attention: '#d29922', success: '#56d364' } }
  for (const context of [defaultRendererContext, dark]) {
    const option = echartsOption(envelope, context) as any
    type PointSeries = {
      name: string
      itemStyle: { color: (params: { value: unknown[] }) => string }
      symbol: (data: unknown[]) => string
      symbolRotate: (data: unknown[]) => number
    }
    const byName = new Map<string, PointSeries>(option.series.map((series: PointSeries) => [series.name, series]))
    const canceled = byName.get('canceled')!
    const ok = byName.get('ok')!
    const missing = byName.get('(null)')!
    expect(canceled.itemStyle.color({ value: ['p-canceled', 'canceled', 2, 20] })).toBe(context.colors.danger)
    expect(ok.itemStyle.color({ value: ['p-ok', 'ok', 1, 10] })).toBe(context.colors.success)
    expect(missing.itemStyle.color({ value: ['p-null', null, 3, 30] })).toBe(context.colors.attention)
    expect(canceled.symbol(['p-canceled', 'canceled', 2, 20])).toBe('arrow')
    expect(canceled.symbolRotate(['p-canceled', 'canceled', 2, 20])).toBe(180)
    expect(ok.symbol(['p-ok', 'ok', 1, 10])).toBe('triangle')
    expect(ok.symbolRotate(['p-ok', 'ok', 1, 10])).toBe(0)
    expect(missing.symbol(['p-null', null, 3, 30]).startsWith('path://')).toBe(true)
  }
})

test('ECharts disables large scatter mode when conditional fill needs per-point color', () => {
  const envelope = pointCategoricalFixture([['p-1', 'ok', 1, 10]]) as any
  envelope.spec.presentation.brush = []
  envelope.spec.presentation.largeMode = 'always'
  envelope.spec.conditionalFormatting = [{
    id: 'gradient', target: 'mark_fill', field: { dataset: 'primary', field: 'y' },
    rule: { kind: 'gradient', minimum: 0, maximum: 20, low: { color: 'neutral' }, high: { color: 'danger' }, nullStyle: { color: 'neutral' } },
  }]
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].large).toBe(false)
  expect(option.series[0].symbol).toBeUndefined()
  expect(option.series[0].itemStyle.color({ value: ['p-1', 'ok', 1, 10] })).toBe('rgb(147 65 76)')
})

test('ECharts partitions large categorical scatter frames without losing source identity', () => {
  const rows = Array.from({ length: 5_000 }, (_, index) => [`p-${index}`, `category-${index % 5}`, index, index * 2])
  const envelope = pointCategoricalFixture(rows) as any
  envelope.spec.presentation.brush = []
  envelope.spec.presentation.largeMode = 'automatic'
  envelope.spec.presentation.largeThreshold = 1_000
  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.series).toHaveLength(5)
  expect(option.series.every((series: any) => series.large === true)).toBe(true)
  expect(option.series.flatMap((series: any) => series.__lv_source_row_indices).sort((a: number, b: number) => a - b)).toEqual(Array.from({ length: 5_000 }, (_, index) => index))
  expect(option.dataset[0].source).toHaveLength(5_001)
})

test('ECharts accepts canonical decimal strings for quantitative point color domains and size', () => {
  const rows = [
    ['p-1', 'unused', '1.25', '10.50'],
    ['p-2', 'unused', '2.50', '20.00'],
    ['p-null', 'unused', null, null],
  ]
  const envelope = pointCategoricalFixture(rows) as any
  envelope.spec.color = { dataset: 'primary', field: 'y' }
  envelope.spec.colorScale = { kind: 'quantitative' }
  envelope.spec.size = { dataset: 'primary', field: 'x' }
  envelope.spec.sizeScale = { minimumPixels: 4, maximumPixels: 16 }
  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.visualMap).toMatchObject({ min: 10.5, max: 20 })
  expect(option.series).toHaveLength(1)
  expect(option.series[0].symbolSize(rows[0])).toBe(4)
  expect(option.series[0].symbolSize(rows[1])).toBe(16)
  expect(option.series[0].symbolSize(rows[2])).toBe(4)
})

test('ECharts scales point size domains whose finite endpoints have an infinite direct span', () => {
  const maximum = Number.MAX_VALUE
  const rows = [
    ['p-min', 'unused', -maximum, 10],
    ['p-max', 'unused', maximum, 20],
  ]
  const envelope = pointCategoricalFixture(rows) as any
  envelope.spec.color = undefined
  envelope.spec.colorScale = undefined
  envelope.spec.size = { dataset: 'primary', field: 'x' }
  envelope.spec.sizeScale = { minimumPixels: 4, maximumPixels: 16 }
  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(Number.isFinite(option.series[0].symbolSize(rows[0]))).toBe(true)
  expect(option.series[0].symbolSize(rows[0])).toBe(4)
  expect(option.series[0].symbolSize(rows[1])).toBe(16)
})

test('ECharts keeps constant maximum point color domains finite', () => {
  const maximum = Number.MAX_VALUE
  const rows = [
    ['p-1', 'unused', 1, maximum],
    ['p-2', 'unused', 2, maximum],
  ]
  const envelope = pointCategoricalFixture(rows) as any
  envelope.spec.color = { dataset: 'primary', field: 'y' }
  envelope.spec.colorScale = { kind: 'quantitative' }
  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.visualMap).toMatchObject({ min: 0, max: maximum })
  expect(Number.isFinite(option.visualMap.min)).toBe(true)
  expect(Number.isFinite(option.visualMap.max)).toBe(true)
})

test('ECharts preserves symmetric constant point color domains when expansion is finite', () => {
  const rows = [
    ['p-1', 'unused', 1, 10],
    ['p-2', 'unused', 2, 10],
  ]
  const envelope = pointCategoricalFixture(rows) as any
  envelope.spec.color = { dataset: 'primary', field: 'y' }
  envelope.spec.colorScale = { kind: 'quantitative' }
  const option = echartsOption(envelope, defaultRendererContext) as any

  expect(option.visualMap).toMatchObject({ min: 9, max: 11 })
})

test('ECharts reports field-specific diagnostics for out-of-range canonical decimal strings', () => {
  const cases: Array<{ rows: unknown[][]; configure: (envelope: any) => void; expected: RegExp }> = [
    {
      rows: [['p-1', 'unused', `0.${'0'.repeat(400)}1`, '10.50']],
      configure: (envelope) => {
        envelope.spec.color = undefined
        envelope.spec.colorScale = undefined
        envelope.spec.size = { dataset: 'primary', field: 'x' }
        envelope.spec.sizeScale = { minimumPixels: 4, maximumPixels: 16 }
      },
      expected: /point size field "x".*outside the JavaScript finite numeric range/,
    },
    {
      rows: [['p-1', 'unused', '1.25', `1${'0'.repeat(400)}`]],
      configure: (envelope) => {
        envelope.spec.color = { dataset: 'primary', field: 'y' }
        envelope.spec.colorScale = { kind: 'quantitative' }
      },
      expected: /point color field "y".*outside the JavaScript finite numeric range/,
    },
  ]

  for (const testCase of cases) {
    const envelope = pointCategoricalFixture(testCase.rows) as any
    testCase.configure(envelope)
    expect(() => echartsOption(envelope, defaultRendererContext)).toThrow(testCase.expected)
  }
})

function pointCategoricalFixture(rows: unknown[][]): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: 'point-categories', rendererID: 'echarts', specRevision: 'sha256:point-categories', dataRevision: 1,
    spec: {
      kind: 'point', title: 'Point categories',
      datasets: [{ id: 'primary', fields: [
        { id: 'id', role: 'identity', dataType: 'string', nullable: false, label: 'ID' },
        { id: 'category', role: 'dimension', dataType: 'string', nullable: true, label: 'Category' },
        { id: 'x', role: 'metric', dataType: 'decimal', nullable: false, label: 'X' },
        { id: 'y', role: 'metric', dataType: 'decimal', nullable: false, label: 'Y' },
      ] }],
      dataBudget: { maxRows: 5_000, requiredCompleteness: 'complete' },
      accessibility: { title: 'Point categories', description: 'Point categories' }, interactions: [{ id: 'point_selection', kind: 'select', mode: 'multiple', requiresStableIdentity: true, targets: [{ visualID: 'details', effect: 'filter' }], mappings: [{ source: { dataset: 'primary', field: 'id' }, targetFieldID: 'orders.id', targetDatasetID: 'orders' }] }],
      identity: [{ dataset: 'primary', field: 'id' }], x: { dataset: 'primary', field: 'x' }, y: { dataset: 'primary', field: 'y' },
      color: { dataset: 'primary', field: 'category' }, colorScale: { kind: 'categorical' },
      presentation: { legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, overplot: 'show_all', opacity: 1, largeMode: 'never', largeThreshold: 1000, brush: ['rectangle'] },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:point-categories', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:point-categories', dataRevision: 1, generation: 1, columns: ['id', 'category', 'x', 'y'], rows, completeness: rows.length ? 'complete' : 'empty' }] },
    selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

function pointDataSignature(option: any): unknown {
  return {
    series: option.series.map((series: any) => ({ id: series.id, type: series.type, name: series.name, datasetId: series.datasetId, encode: series.encode, sourceRows: series.__lv_source_row_indices })),
    dataset: option.dataset?.map((dataset: any) => ({ id: dataset.id, source: dataset.source, fromDatasetId: dataset.fromDatasetId, transform: dataset.transform })),
  }
}
