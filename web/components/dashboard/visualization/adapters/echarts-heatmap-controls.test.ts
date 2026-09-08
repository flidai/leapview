import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption, heatmapFocusDataZoom, heatmapFocusZoomEnabled } from './echarts'
import { constrainEChartsLabelToDataRect, echartsLabelPolicy, truncateVisualizationLabel } from './echarts/label-policy'

test('ECharts label policy truncates by grapheme and preserves selected and threshold labels', () => {
  const envelope = cartesianFixture('heatmap', ['label', 'row', 'value']) as any
  envelope.spec.datasets[0].fields[0].role = 'identity'
  envelope.selection = [{
    datum: { dataset: 'primary', dataRevision: 1, identity: { label: 'A' } },
    label: 'A',
  }]
  envelope.spec.conditionalFormatting = [{
    id: 'threshold', target: 'label_foreground', field: { dataset: 'primary', field: 'value' },
    rule: {
      kind: 'rules',
      rules: [{ operator: 'greater_or_equal', value: 1, style: { color: 'warning', icon: 'warning' } }],
      nullStyle: { color: 'neutral' },
      defaultStyle: { color: 'neutral', icon: 'circle' },
    },
  }]
  const policy = {
    density: 'automatic', priority: ['selected', 'threshold'],
    maxCharacters: 8, minimumSpacing: 6, tooltipFallback: true,
  } as const
  const translated = echartsLabelPolicy(envelope, 'primary', policy, (params) => String(params.value?.[0] ?? ''), defaultRendererContext)

  expect(translated.label).toMatchObject({ show: true, padding: 3, overflow: 'truncate' })
  expect(translated.label.formatter({ value: ['São Paulo 😀 zone'] })).toBe('São Pau…')
  expect(translated.labelLayout({ dataIndex: 0 }).hideOverlap).toBe(false)
  expect(translated.labelLayout({ dataIndex: 99 }).hideOverlap).toBe(true)
  expect(truncateVisualizationLabel('ação 😀 norte', 7, 'pt-BR')).toBe('ação 😀…')

  const dense = echartsLabelPolicy(envelope, 'primary', { ...policy, density: 'dense', minimumSpacing: 2 }, () => 'value', defaultRendererContext)
  expect(dense.label).toMatchObject({ show: true, fontSize: 10, padding: 1 })
  const always = echartsLabelPolicy(envelope, 'primary', { ...policy, density: 'always' }, () => 'value', defaultRendererContext)
  expect(always.labelLayout).toEqual({ hideOverlap: false })
  const hidden = echartsLabelPolicy(envelope, 'primary', { ...policy, density: 'hidden' }, () => 'value', defaultRendererContext)
  expect(hidden.label.show).toBe(false)
  envelope.spec.presentation.labelPolicy = { ...policy, density: 'hidden' }
  envelope.dataState.datasets[0].rows[0][0] = 'São Paulo 😀 zone'
  const hiddenOption = echartsOption(envelope, defaultRendererContext) as any
  expect(hiddenOption.tooltip.confine).toBe(true)
  expect(hiddenOption.tooltip.formatter({ value: envelope.dataState.datasets[0].rows[0] })).toContain('São Paulo 😀 zone')
  expect(hiddenOption.aria.description).toContain('label: São Paulo 😀 zone')
})

test('ECharts heatmap labels stay inside their data cells', () => {
  const automatic = constrainEChartsLabelToDataRect(
    ({ dataIndex }) => ({ hideOverlap: dataIndex !== 0 }),
    4,
  )
  expect(automatic({ dataIndex: 0, rect: { width: 18.9, height: 15.2 } })).toEqual({
    hideOverlap: false,
    width: 14,
    height: 11,
  })
  expect(automatic({ dataIndex: 1 })).toEqual({ hideOverlap: true })

  const always = constrainEChartsLabelToDataRect({ hideOverlap: false }, 6)
  expect(always({ rect: { width: 4, height: 5 } })).toEqual({ hideOverlap: false, width: 0, height: 0 })
})

test('ECharts heatmap focus keeps a draggable full-range slider and restores the compact range', () => {
  expect(heatmapFocusDataZoom(true)).toEqual([
    { id: 'dataZoom:heatmap:inside', type: 'inside', disabled: true, start: 0, end: 100 },
    { id: 'dataZoom:heatmap:slider', type: 'slider', show: true, bottom: 64, showDetail: false, brushSelect: false, start: 0, end: 100 },
  ])
  expect(heatmapFocusDataZoom(false, { start: 0, end: 37.5 })).toEqual([
    { id: 'dataZoom:heatmap:inside', type: 'inside', disabled: false, start: 0, end: 37.5 },
    { id: 'dataZoom:heatmap:slider', type: 'slider', show: true, bottom: 64, showDetail: false, brushSelect: false, start: 0, end: 37.5 },
  ])
})

test('ECharts heatmap focus requires initialized generated zoom controls', () => {
  const envelope = cartesianFixture('heatmap', ['label', 'row', 'value']) as any
  const populated = echartsOption(envelope, defaultRendererContext) as any
  expect(populated.dataZoom).toHaveLength(2)
  expect(heatmapFocusZoomEnabled(envelope, true)).toBe(true)

  envelope.dataState.datasets[0].rows = []
  const empty = echartsOption(envelope, defaultRendererContext) as any
  expect(empty.dataZoom).toEqual([])
  expect(heatmapFocusZoomEnabled(envelope, false)).toBe(false)
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
