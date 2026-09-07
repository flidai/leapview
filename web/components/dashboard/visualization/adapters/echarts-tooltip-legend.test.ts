import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'

test('ECharts curated tooltips preserve item order, overrides, nulls, and escaping', () => {
  const envelope = tooltipFixture()
  envelope.spec.tooltipItems = [
    { field: { dataset: 'primary', field: 'value' }, label: 'Amount', format: { kind: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2 } },
    { field: { dataset: 'primary', field: 'status' }, label: '<State>' },
    { field: { dataset: 'primary', field: 'nullish' } },
    { field: { dataset: 'primary', field: 'note' } },
  ]
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.tooltip.formatter({ value: ['open', 12.5, null, '<unsafe>'] })).toBe('Amount: $12.50<br>&lt;State&gt;: open<br>Nullish: —<br>Note: &lt;unsafe&gt;')
})

test('ECharts explicit empty tooltips suppress rows and omitted items retain legacy fallback', () => {
  const envelope = tooltipFixture()
  envelope.spec.tooltipItems = []
  expect((echartsOption(envelope, defaultRendererContext) as any).tooltip.formatter({ value: ['open', 12.5, null, '<unsafe>'] })).toBe('')
  envelope.spec.tooltipItems = undefined
  envelope.spec.tooltip = [{ dataset: 'primary', field: 'note' }]
  expect((echartsOption(envelope, defaultRendererContext) as any).tooltip.formatter({ value: ['open', 12.5, null, '<unsafe>'] })).toBe('Note: &lt;unsafe&gt;')
  const defaultEnvelope = tooltipFixture()
  expect((echartsOption(defaultEnvelope, defaultRendererContext) as any).tooltip.formatter({ value: ['open', 12.5, null, '<unsafe>'] })).toContain('Status: open')
})

test('ECharts legend metadata uses canonical item names and a renderer-owned title graphic', () => {
  const envelope = tooltipFixture()
  envelope.spec = {
    ...envelope.spec,
    series: { dataset: 'primary', field: 'status' },
    presentation: { ...envelope.spec.presentation, legend: 'right', legendTitle: 'States', legendItems: [{ value: 'closed', label: 'Closed orders' }, { value: 'missing', label: 'Missing' }, { value: 'open', label: 'Open orders' }] },
  } as any
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.legend.data).toEqual([{ name: 'closed' }, { name: 'open' }])
  expect(option.legend.formatter('open')).toBe('Open orders')
  expect(option.series.map((series: any) => series.name)).toEqual(['closed', 'open'])
  expect(option.graphic).toEqual(expect.arrayContaining([expect.objectContaining({ type: 'text', style: expect.objectContaining({ text: 'States', fill: defaultRendererContext.colors.foreground }) })]))
  expect(option.legend.name).toBeUndefined()
  expect(option.legend.nameTextStyle).toBeUndefined()
})

test('ECharts proportional tooltips use the same curated row projection', () => {
  const envelope = tooltipFixture() as any
  envelope.spec = {
    kind: 'proportional', title: 'Orders', mark: 'pie', datasets: envelope.spec.datasets,
    dataBudget: envelope.spec.dataBudget, accessibility: envelope.spec.accessibility, interactions: [],
    category: { dataset: 'primary', field: 'status' }, value: { dataset: 'primary', field: 'value' },
    tooltipItems: [
      { field: { dataset: 'primary', field: 'note' }, label: 'Context' },
      { field: { dataset: 'primary', field: 'value' }, label: 'Amount', format: { kind: 'currency', currency: 'USD' } },
    ],
    presentation: { legend: 'hidden', labelPolicy: envelope.spec.presentation.labelPolicy, orientation: 'vertical', rose: false, labelPosition: 'outside' },
  }
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.tooltip.formatter({ value: ['open', 12.5, null, '<unsafe>'] })).toBe('Context: &lt;unsafe&gt;<br>Amount: $12.50')
})

test('ECharts proportional legends deduplicate repeated category rows', () => {
  const envelope = tooltipFixture() as any
  envelope.dataState.datasets[0].rows.push(['open', 2, null, 'duplicate'])
  envelope.spec = {
    kind: 'proportional', title: 'Orders', mark: 'pie', datasets: envelope.spec.datasets,
    dataBudget: envelope.spec.dataBudget, accessibility: envelope.spec.accessibility, interactions: [],
    category: { dataset: 'primary', field: 'status' }, value: { dataset: 'primary', field: 'value' },
    presentation: {
      legend: 'bottom', legendItems: [{ value: 'closed', label: 'Closed orders' }], labelPosition: 'outside',
      rose: false, innerRadius: 0, outerRadius: 0.72, labelPolicy: envelope.spec.presentation.labelPolicy,
    },
  }
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.legend.data).toEqual([{ name: 'closed' }, { name: 'open' }])
})

test('ECharts appends known unconfigured legend values and themes title/text colors', () => {
  const envelope = tooltipFixture() as any
  envelope.spec.series = { dataset: 'primary', field: 'status' }
  envelope.spec.presentation = { ...envelope.spec.presentation, legend: 'top', legendTitle: 'States', legendItems: [{ value: 'open', label: 'Open orders' }] }
  const dark = { ...defaultRendererContext, theme: 'dark' as const, colors: { ...defaultRendererContext.colors, foreground: '#f0f6fc', muted: '#8b949e' } }
  const option = echartsOption(envelope, dark) as any
  expect(option.legend.data).toEqual([{ name: 'open' }, { name: 'closed' }])
  expect(option.legend.textStyle.color).toBe(dark.colors.muted)
  expect(option.graphic[0].style.fill).toBe(dark.colors.foreground)
})

test('ECharts multi-measure legend items match stable field IDs while displaying field labels', () => {
  const envelope = tooltipFixture() as any
  delete envelope.spec.series
  envelope.spec.y = [{ dataset: 'primary', field: 'value' }, { dataset: 'primary', field: 'note' }]
  envelope.spec.presentation = { ...envelope.spec.presentation, legend: 'bottom', legendItems: [{ value: 'value', label: 'Net revenue' }] }
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.legend.data).toEqual([{ name: 'Value' }, { name: 'Note' }])
  expect(option.legend.formatter('Value')).toBe('Net revenue')

  envelope.spec.presentation.legendItems = [{ value: 'value' }]
  const defaultLabelOption = echartsOption(envelope, defaultRendererContext) as any
  expect(defaultLabelOption.legend.formatter('Value')).toBe('Value')
})

test('ECharts hidden legends do not create title graphics', () => {
  const envelope = tooltipFixture() as any
  envelope.spec.presentation = { ...envelope.spec.presentation, legend: 'hidden', legendTitle: 'Should not render' }
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.legend).toBeUndefined()
  expect(option.graphic).toBeUndefined()
})

test('ECharts categorical point legend keeps canonical series IDs stable across row order', () => {
  const envelope = tooltipFixture() as any
  envelope.spec = {
    kind: 'point', title: 'Points', datasets: envelope.spec.datasets, dataBudget: envelope.spec.dataBudget,
    accessibility: envelope.spec.accessibility, interactions: [], identity: [{ dataset: 'primary', field: 'status' }],
    x: { dataset: 'primary', field: 'value' }, y: { dataset: 'primary', field: 'value' },
    color: { dataset: 'primary', field: 'status' }, colorScale: { kind: 'categorical' },
    presentation: { legend: 'bottom', labelPolicy: envelope.spec.presentation.labelPolicy, overplot: 'show_all', opacity: 1, largeMode: 'never', largeThreshold: 100, brush: [] },
  }
  const first = echartsOption(envelope, defaultRendererContext) as any
  envelope.dataState.datasets[0].rows.reverse()
  const second = echartsOption(envelope, defaultRendererContext) as any
  expect(first.series.map((series: any) => [series.name, series.id])).toEqual(second.series.map((series: any) => [series.name, series.id]))
})

test('ECharts categorical point legend overrides match canonical null and empty display identities', () => {
  const envelope = tooltipFixture() as any
  envelope.spec = pointLegendSpec(envelope.spec, [
    { value: '(null)', label: 'Missing status' },
    { value: '(empty)', label: 'Blank status' },
  ])
  envelope.dataState.datasets[0].rows = [[null, 1, null, 'null row'], ['', 2, null, 'empty row']]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.legend.data).toEqual([{ name: '(null)' }, { name: '(empty)' }])
  expect(option.legend.formatter('(null)')).toBe('Missing status')
  expect(option.legend.formatter('(empty)')).toBe('Blank status')
  expect(option.dataset.slice(1).map((dataset: any) => dataset.transform.config['='])).toEqual([null, ''])
})

test('ECharts categorical point legend overrides keep type-collision display identities distinct', () => {
  const envelope = tooltipFixture() as any
  envelope.spec = pointLegendSpec(envelope.spec, [
    { value: '1 [string:1]', label: 'Text one' },
    { value: '1 [number:1]', label: 'Numeric one' },
  ])
  envelope.dataState.datasets[0].rows = [['1', 1, null, 'string row'], [1, 2, null, 'number row']]

  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.legend.data).toEqual([{ name: '1 [string:1]' }, { name: '1 [number:1]' }])
  expect(option.legend.formatter('1 [string:1]')).toBe('Text one')
  expect(option.legend.formatter('1 [number:1]')).toBe('Numeric one')
  expect(option.series.map((series: any) => series.name)).toEqual(['1 [number:1]', '1 [string:1]'])
  expect(option.dataset.slice(1).map((dataset: any) => dataset.transform.config['='])).toEqual([1, '1'])
})

test('ECharts preserves a donut center graphic alongside a legend title graphic', () => {
  const envelope = tooltipFixture() as any
  envelope.spec = {
    kind: 'proportional', title: 'Orders', mark: 'donut', datasets: envelope.spec.datasets,
    dataBudget: envelope.spec.dataBudget, accessibility: envelope.spec.accessibility, interactions: [],
    category: { dataset: 'primary', field: 'status' }, value: { dataset: 'primary', field: 'value' },
    presentation: {
      legend: 'bottom', legendTitle: 'State', centerLabel: 'Orders', labelPosition: 'outside', rose: false,
      innerRadius: 0.45, outerRadius: 0.72, labelPolicy: envelope.spec.presentation.labelPolicy,
    },
  }
  const option = echartsOption(envelope, defaultRendererContext) as any
  expect(option.graphic).toEqual(expect.arrayContaining([
    expect.objectContaining({ type: 'text', style: expect.objectContaining({ text: 'State' }) }),
    expect.objectContaining({ id: 'graphic:proportional:center' }),
  ]))
})

function tooltipFixture(): VisualizationEnvelope {
  return {
    schemaVersion: 14, visualID: 'tooltip', rendererID: 'echarts', specRevision: 'sha256:tooltip', dataRevision: 1,
    spec: {
      kind: 'cartesian', title: 'Tooltip', mark: 'line',
      datasets: [{ id: 'primary', fields: [
        { id: 'status', role: 'dimension', dataType: 'string', nullable: true, label: 'Status' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: true, label: 'Value' },
        { id: 'nullish', role: 'metadata', dataType: 'string', nullable: true, label: 'Nullish' },
        { id: 'note', role: 'metadata', dataType: 'string', nullable: true, label: 'Note' },
      ] }],
      dataBudget: { maxRows: 10, requiredCompleteness: 'complete' }, accessibility: { title: 'Tooltip', description: 'Tooltip' }, interactions: [],
      x: { dataset: 'primary', field: 'status' }, y: [{ dataset: 'primary', field: 'value' }],
      presentation: { legend: 'bottom', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:tooltip', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:tooltip', dataRevision: 1, generation: 1, columns: ['status', 'value', 'nullish', 'note'], rows: [['open', 12.5, null, '<unsafe>'], ['closed', 4, null, '<other>']], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as any
}

function pointLegendSpec(baseSpec: any, legendItems: Array<{ value: string; label: string }>): any {
  return {
    kind: 'point', title: 'Points', datasets: baseSpec.datasets, dataBudget: baseSpec.dataBudget,
    accessibility: baseSpec.accessibility, interactions: [], identity: [{ dataset: 'primary', field: 'status' }],
    x: { dataset: 'primary', field: 'value' }, y: { dataset: 'primary', field: 'value' },
    color: { dataset: 'primary', field: 'status' }, colorScale: { kind: 'categorical' },
    presentation: { legend: 'bottom', legendItems, labelPolicy: baseSpec.presentation.labelPolicy, overplot: 'show_all', opacity: 1, largeMode: 'never', largeThreshold: 100, brush: [] },
  }
}
