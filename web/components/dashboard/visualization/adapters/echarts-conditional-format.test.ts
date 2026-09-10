import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'
import { CategoryColorRegistry } from './echarts/category-colors'
import { cartesianFixture, proportionalFixture } from './echarts-test-fixtures'

test('ECharts keeps point conditional cues visible for null, first-match, and default outcomes', () => {
  const envelope = {
    schemaVersion: 9, visualID: 'health-points', rendererID: 'echarts', specRevision: 'sha256:health-points', dataRevision: 1,
    spec: {
      kind: 'point', title: 'Health points', datasets: [{ id: 'primary', fields: [
        { id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'Label' },
        { id: 'x', role: 'metric', dataType: 'decimal', nullable: false, label: 'X' },
        { id: 'y', role: 'metric', dataType: 'decimal', nullable: false, label: 'Y' },
        { id: 'score', role: 'metric', dataType: 'decimal', nullable: true, label: 'Score' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Health points', description: 'Health points' }, interactions: [],
      x: { dataset: 'primary', field: 'x' }, y: { dataset: 'primary', field: 'y' }, color: { dataset: 'primary', field: 'score' },
      label: { dataset: 'primary', field: 'label' }, tooltip: [{ dataset: 'primary', field: 'score' }], colorScale: { kind: 'quantitative' },
      presentation: { legend: 'bottom', labelPolicy: { density: 'always', priority: [], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, overplot: 'opacity', opacity: 0.55, largeMode: 'automatic', largeThreshold: 1000, brush: [] },
      conditionalFormatting: [{
        id: 'score-status', target: 'mark_fill', field: { dataset: 'primary', field: 'score' },
        rule: {
          kind: 'rules',
          rules: [
            { operator: 'greater_or_equal', value: 0, style: { color: 'warning', icon: 'circle' } },
            { operator: 'greater_or_equal', value: 80, style: { color: 'success', icon: 'arrow_up' } },
          ],
          nullStyle: { color: 'neutral', icon: 'warning' }, defaultStyle: { color: 'danger', icon: 'arrow_down' },
        },
      }],
    },
    dataState: { kind: 'inline', specRevision: 'sha256:health-points', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:health-points', dataRevision: 1, generation: 1, columns: ['label', 'x', 'y', 'score'], rows: [['Missing', 1, 1, null], ['High', 2, 2, 90], ['Low', 3, 3, -1]], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
  const dark = { ...defaultRendererContext, theme: 'dark' as const, colors: { ...defaultRendererContext.colors, attention: '#d29922', danger: '#ff7b72', success: '#56d364', muted: '#8b949e' } }
  const series = (echartsOption(envelope, dark) as any).series[0]

  expect(series.symbol(['Missing', 1, 1, null])).toContain('path://')
  expect(series.itemStyle.color({ value: ['Missing', 1, 1, null] })).toBe(dark.colors.muted)
  expect(series.symbol(['High', 2, 2, 90])).toBe('circle')
  expect(series.itemStyle.color({ value: ['High', 2, 2, 90] })).toBe(dark.colors.attention)
  expect(series.symbol(['Low', 3, 3, -1])).toBe('arrow')
  expect(series.itemStyle.color({ value: ['Low', 3, 3, -1] })).toBe(dark.colors.danger)
})

test('ECharts applies governed row formatting with theme colors and redundant cues', () => {
  const envelope = cartesianFixture('column') as any
  envelope.spec.presentation.labelPolicy.density = 'automatic'
  envelope.spec.presentation.labelPolicy.priority = ['anomaly', 'threshold']
  envelope.spec.conditionalFormatting = [
    {
      id: 'value-gradient', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
      rule: {
        kind: 'gradient', minimum: 0, maximum: 100,
        low: { color: 'danger' }, high: { color: 'success' }, nullStyle: { color: 'neutral' },
      },
    },
    {
      id: 'value-health', target: 'label_foreground', field: { dataset: 'primary', field: 'value' },
      rule: {
        kind: 'rules',
        rules: [{ operator: 'less_than', value: 50, style: { color: 'danger', icon: 'arrow_down' } }],
        nullStyle: { icon: 'warning' }, defaultStyle: { color: 'success', icon: 'arrow_up' },
      },
    },
  ]
  envelope.dataState.datasets[0].rows = [['A', 25], ['B', 75]]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].itemStyle.color({ value: ['A', 25] })).toBe('rgb(162 57 48)')
  expect(option.series[0].itemStyle.color({ value: ['B', 75] })).toBe('rgb(71 104 53)')
  expect(option.series[0].label.show).toBe(true)
  expect(option.series[0].labelLayout).toEqual({ hideOverlap: false })
  expect(option.series[0].label.formatter({ value: ['A', 25] })).toBe('↓ 25')
  expect(option.series[0].label.formatter({ value: ['B', 75] })).toBe('↑ 75')
})

test('ECharts gives explicit icon targets precedence when Cartesian formats share a field', () => {
  const envelope = cartesianFixture('column') as any
  envelope.spec.presentation.labelPolicy.density = 'always'
  envelope.spec.conditionalFormatting = [
    {
      id: 'fill-cue', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
      rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { color: 'warning', icon: 'warning' } }], nullStyle: { icon: 'circle' }, defaultStyle: { color: 'neutral', icon: 'circle' } },
    },
    {
      id: 'icon-cue', target: 'icon', field: { dataset: 'primary', field: 'value' },
      rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'arrow_up' } }], nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'arrow_down' } },
    },
  ]
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.series[0].label.formatter({ value: ['A', 1] })).toBe('↑ 1')
})

test('ECharts keeps Cartesian conditional icons visible when labels are hidden', () => {
  const envelope = cartesianFixture('column') as any
  envelope.spec.presentation.labelPolicy.density = 'hidden'
  envelope.spec.conditionalFormatting = [{
    id: 'value-icon', target: 'icon', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'arrow_up' } }],
      nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'arrow_down' },
    },
  }]

  const series = (echartsOption(envelope, defaultRendererContext) as any).series[0]
  expect(series).toMatchObject({ label: { show: true }, labelLayout: { hideOverlap: false } })
  expect(series.label.formatter({ value: ['A', 1] })).toBe('↑ 1')
})

test('ECharts keeps heatmap visualMap colors with icon-only conditional cues', () => {
  for (const target of ['mark_fill', 'series_color'] as const) {
    const envelope = cartesianFixture('heatmap', ['label', 'row', 'value']) as any; envelope.spec.presentation.labelPolicy.density = 'hidden'
    envelope.spec.conditionalFormatting = [{
      id: 'value-icon', target, field: { dataset: 'primary', field: 'value' },
      rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'arrow_up' } }], nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'arrow_down' } },
    }]

    const option = echartsOption(envelope, defaultRendererContext) as any
    expect(option.visualMap).toMatchObject({ type: 'continuous', dimension: 'value', inRange: { color: [expect.any(String), expect.any(String)] }, outOfRange: { opacity: 1 } })
    expect(option.series[0].itemStyle.color({ value: ['A', 'R1', 1] })).toBeTypeOf('string')
    expect(option.series[0].label.show).toBe(true)
    expect(option.series[0].labelLayout({ dataIndex: 0, rect: { width: 40, height: 24 } })).toMatchObject({ hideOverlap: false })
    expect(option.series[0].label.formatter({ value: ['A', 'R1', 1] })).toBe('↑ 1')
  }
})

test('ECharts falls back to inside contrast for icon-only label colors', () => {
  for (const mark of ['bar', 'column', 'waterfall', 'histogram'] as const) {
    const columns = mark === 'waterfall' ? ['label', 'start', 'value'] : ['label', 'value']
    const envelope = cartesianFixture(mark, columns) as any
    envelope.spec.presentation.labelPolicy.density = 'hidden'
    envelope.spec.conditionalFormatting = [{
      id: 'value-icon', target: 'label_foreground', field: { dataset: 'primary', field: 'value' },
      rule: {
        kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'arrow_up' } }],
        nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'arrow_down' },
      },
    }]

    const option = echartsOption(envelope, defaultRendererContext) as any
    const series = mark === 'waterfall' ? option.series[1] : option.series[0]
    const row = mark === 'waterfall' ? ['A', 0, 1] : ['A', 1]
    expect(series.label.color({ value: row })).toBe('#fff')
    expect(series.label).toMatchObject({ textBorderColor: 'rgba(0, 0, 0, 0.55)', textBorderWidth: 2 })
  }
})

test('ECharts translates governed heatmap gradients and waterfall rule styles', () => {
  const heatmap = cartesianFixture('heatmap', ['label', 'row', 'value']) as any
  heatmap.spec.conditionalFormatting = [{
    id: 'heat', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'gradient', minimum: 0, maximum: 100,
      low: { color: 'danger' }, high: { color: 'success' }, nullStyle: { color: 'neutral' },
    },
  }]
  const heatmapOption = echartsOption(heatmap, defaultRendererContext) as any
  expect(heatmapOption.visualMap).toMatchObject({
    type: 'continuous',
    dimension: 'value',
    min: 0,
    max: 100,
    calculable: true,
    inRange: { color: [defaultRendererContext.colors.danger, defaultRendererContext.colors.success] },
    outOfRange: { opacity: 1 },
  })
  expect(heatmapOption.visualMap.formatter(0)).toBe('0')
  expect(heatmapOption.visualMap.formatter(100)).toBe('100')
  expect(heatmapOption.series[0].itemStyle.color({ value: ['A', 'R1', null] })).toBe(defaultRendererContext.colors.muted)

  const waterfall = cartesianFixture('waterfall', ['label', 'start', 'value']) as any
  waterfall.spec.conditionalFormatting = [{
    id: 'delta', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules', rules: [{ operator: 'less_than', value: 0, style: { color: 'danger', icon: 'arrow_down' } }],
      nullStyle: { icon: 'warning' }, defaultStyle: { color: 'success', icon: 'arrow_up' },
    },
  }]
  waterfall.dataState.datasets[0].rows = [['Returns', 10, -4]]
  const waterfallOption = echartsOption(waterfall, defaultRendererContext) as any
  expect(waterfallOption.series[1].itemStyle.color({ value: ['Returns', 10, -4] })).toBe(defaultRendererContext.colors.danger)
  expect(waterfallOption.series[1].label.formatter({ value: ['Returns', 10, -4] })).toBe('↓ -4')
})

test('ECharts composes conditional heatmap colors per outcome and keeps null cells visible', () => {
  const envelope = cartesianFixture('heatmap', ['label', 'row', 'value']) as any; envelope.dataState.datasets[0].rows = [['A', 'R1', 100], ['B', 'R1', 50], ['C', 'R1', null], ['D', 'R1', 'bad']]
  envelope.spec.presentation.labelPolicy.density = 'hidden'
  envelope.spec.conditionalFormatting = [{
    id: 'value-status', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 75, style: { color: 'danger', icon: 'arrow_up' } }], nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'circle' } },
  }]

  const option = echartsOption(envelope, defaultRendererContext) as any; const color = option.series[0].itemStyle.color
  expect(color({ value: ['A', 'R1', 100] })).toBe(defaultRendererContext.colors.danger)
  expect(color({ value: ['B', 'R1', 50] })).toBeTypeOf('string'); expect(color({ value: ['B', 'R1', 50] })).not.toBe(defaultRendererContext.colors.danger)
  expect(color({ value: ['C', 'R1', null] })).toBe(defaultRendererContext.colors.muted); expect(color({ value: ['D', 'R1', 'bad'] })).toBe(defaultRendererContext.colors.muted)
  expect(option.visualMap).toBeUndefined()
  expect(option.series[0].label.show).toBe(true)
  expect(option.series[0].labelLayout({ dataIndex: 0, rect: { width: 40, height: 24 } })).toMatchObject({ hideOverlap: false })
  expect(option.series[0].label.formatter({ value: ['C', 'R1', null] })).toBe('⚠ —')
})

test('ECharts uses series color when mark fill only supplies an icon for a row', () => {
  const envelope = proportionalFixture('donut') as any; envelope.dataState.datasets[0].rows = [['High', 20], ['Medium', 5], ['Low', -1]]
  envelope.spec.conditionalFormatting = [
    {
      id: 'fill-status', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
      rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 10, style: { color: 'danger', icon: 'arrow_up' } }], nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'circle' } },
    },
    {
      id: 'series-status', target: 'series_color', field: { dataset: 'primary', field: 'value' },
      rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { color: 'success', icon: 'arrow_up' } }], nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'square' } },
    },
  ]

  const categoryColors = new CategoryColorRegistry(); const option = echartsOption(envelope, defaultRendererContext, categoryColors) as any; const color = option.series[0].itemStyle.color
  expect(color({ value: ['High', 20] })).toBe(defaultRendererContext.colors.danger)
  expect(color({ value: ['Medium', 5] })).toBe(defaultRendererContext.colors.success)
  const fallback = categoryColors.color(envelope, envelope.spec.category, 'Low', defaultRendererContext)
  expect(color({ value: ['Low', -1] })).toBe(fallback)
})

test('ECharts keeps signed waterfall fallback after icon-only conditional outcomes', () => {
  const envelope = cartesianFixture('waterfall', ['label', 'start', 'value']) as any
  envelope.dataState.datasets[0].rows = [['Increase', 0, 4], ['Decrease', 4, -3]]
  envelope.spec.conditionalFormatting = [{
    id: 'value-icon', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules', rules: [{ operator: 'greater_than', value: 100, style: { icon: 'arrow_up' } }],
      nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'circle' },
    },
  }]

  const option = echartsOption(envelope, defaultRendererContext) as any
  const color = option.series[1].itemStyle.color
  expect(color({ value: ['Increase', 0, 4] })).toBe(defaultRendererContext.colors.success)
  expect(color({ value: ['Decrease', 4, -3] })).toBe(defaultRendererContext.colors.danger)
})

test('ECharts binds waterfall formatting to the authored metric alias', () => {
  const waterfall = cartesianFixture('waterfall', ['label', 'start', 'order_total']) as any
  waterfall.spec.conditionalFormatting = [{
    id: 'delta', target: 'mark_fill', field: { dataset: 'primary', field: 'order_total' },
    rule: {
      kind: 'rules', rules: [{ operator: 'less_than', value: 0, style: { color: 'danger', icon: 'arrow_down' } }],
      nullStyle: { icon: 'warning' }, defaultStyle: { color: 'success', icon: 'arrow_up' },
    },
  }]
  waterfall.dataState.datasets[0].rows = [['Returns', 10, -4]]
  const waterfallOption = echartsOption(waterfall, defaultRendererContext) as any
  expect(waterfallOption.series[1].encode.y).toBe('order_total')
  expect(waterfallOption.series[1].itemStyle.color({ value: ['Returns', 10, -4] })).toBe(defaultRendererContext.colors.danger)

})

test('ECharts keeps direct-IR waterfall metric before the start offset', () => {
  const waterfall = cartesianFixture('waterfall', ['label', 'value', 'start']) as any
  waterfall.spec.conditionalFormatting = [{
    id: 'delta', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules', rules: [{ operator: 'less_than', value: 0, style: { color: 'danger', icon: 'arrow_down' } }],
      nullStyle: { icon: 'warning' }, defaultStyle: { color: 'success', icon: 'arrow_up' },
    },
  }]
  waterfall.dataState.datasets[0].rows = [['Returns', -4, 10]]
  const option = echartsOption(waterfall, defaultRendererContext) as any
  expect(option.series[0].encode.y).toBe('start')
  expect(option.series[1].encode.y).toBe('value')
  expect(option.series[1].itemStyle.color({ value: ['Returns', -4, 10] })).toBe(defaultRendererContext.colors.danger)
})

test('ECharts keeps proportional conditional icon cues visible for null, first-match, and default outcomes', () => {
  const envelope = proportionalFixture('donut') as any
  envelope.spec.presentation.legend = 'bottom'
  envelope.spec.conditionalFormatting = [{
    id: 'value-status', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules',
      rules: [
        { operator: 'greater_or_equal', value: 0, style: { color: 'warning', icon: 'circle' } },
        { operator: 'greater_or_equal', value: 80, style: { color: 'success', icon: 'arrow_up' } },
      ],
      nullStyle: { color: 'neutral', icon: 'warning' },
      defaultStyle: { color: 'danger', icon: 'arrow_down' },
    },
  }]
  envelope.dataState.datasets[0].rows = [['Missing', null], ['High', 90], ['Low', -1]]
  envelope.spec.presentation.labelPolicy.density = 'hidden'

  const contexts = [
    defaultRendererContext,
    { ...defaultRendererContext, theme: 'dark' as const, colors: { ...defaultRendererContext.colors, foreground: '#f0f6fc', surface: '#0d1117' } },
  ]
  for (const context of contexts) {
    const option = echartsOption(envelope, context) as any
    const formatter = option.series[0].label.formatter
    expect(option.series[0]).toMatchObject({
      label: { show: true, overflow: 'break', position: 'outside', alignTo: 'edge', edgeDistance: 8 },
      minShowLabelAngle: 0,
      labelLayout: { hideOverlap: false },
      labelLine: { show: true, length: 10, length2: 8 },
      radius: ['54%', '76%'],
    })
    expect(option.graphic?.find((graphic: any) => graphic.id === 'graphic:proportional:center')).toMatchObject({ top: 'middle' })
    expect(formatter({ value: ['Missing', null] })).toBe('⚠ Missing: —')
    expect(formatter({ value: ['High', 90] })).toBe('● High: 90')
    expect(formatter({ value: ['Low', -1] })).toBe('↓ Low: -1')
  }

  const titled = structuredClone(envelope)
  titled.spec.presentation.legendTitle = 'Order status'
  const titledOption = echartsOption(titled, defaultRendererContext) as any
  expect(titledOption.series[0]).toMatchObject({ bottom: '12%' })

  const insideEnvelope = structuredClone(envelope)
  insideEnvelope.spec.presentation.labelPosition = 'inside'
  const insideOption = echartsOption(insideEnvelope, defaultRendererContext) as any
  expect(insideOption.series[0]).toMatchObject({
    label: { show: true, overflow: 'truncate', position: 'inside' },
    labelLayout: { hideOverlap: false },
    labelLine: { show: false, length2: 8 },
  })

  envelope.spec.mark = 'funnel'
  const funnel = echartsOption(envelope, defaultRendererContext) as any
  expect(funnel.series[0]).toMatchObject({ label: { show: true }, labelLayout: { hideOverlap: false } })
  expect(funnel.series[0].label.formatter({ value: ['Missing', null] })).toBe('⚠ Missing: —')
})

test('ECharts preserves typed category colors for proportional icon-only outcomes', () => {
  for (const target of ['mark_fill', 'series_color'] as const) {
    const envelope = proportionalFixture('donut') as any
    envelope.spec.datasets[0].fields[0].sourceRef = 'orders.status'
    envelope.dataState.datasets[0].rows = [[null, 10], [1, 20], ['1', 30]]
    envelope.spec.conditionalFormatting = [{
      id: 'value-icon', target, field: { dataset: 'primary', field: 'value' },
      rule: {
        kind: 'rules', rules: [{ operator: 'greater_than', value: 100, style: { icon: 'arrow_up' } }],
        nullStyle: { icon: 'warning' }, defaultStyle: { icon: 'circle' },
      },
    }]
    const reordered = structuredClone(envelope)
    reordered.dataState.datasets[0].rows.reverse()

    const initialColor = (echartsOption(envelope, defaultRendererContext, new CategoryColorRegistry()) as any).series[0].itemStyle.color
    const reorderedColor = (echartsOption(reordered, defaultRendererContext, new CategoryColorRegistry()) as any).series[0].itemStyle.color
    for (const category of [null, 1, '1']) {
      const initial = initialColor({ value: [category, 1] })
      expect(initial).toBe(reorderedColor({ value: [category, 1] }))
      expect(initial).toBeTypeOf('string')
    }
  }
})
