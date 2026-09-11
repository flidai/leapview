import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { defaultRendererContext } from '../host-controller'
import { echartsOption } from './echarts'

test('ECharts maps categorical point label priorities back to source rows', () => {
  const rows = Array.from({ length: 20 }, (_, index) => [`p-${index}`, index % 2 === 0 ? 'A' : 'B', index, index])
  const envelope = pointFixture(rows) as any
  envelope.selection = [{ datum: { dataset: 'primary', dataRevision: 1, identity: { id: 'p-19' } }, label: 'p-19' }]
  const option = echartsOption(envelope, defaultRendererContext) as any
  const seriesByName = new Map(option.series.map((series: any) => [series.name, series]))
  const selected = seriesByName.get('B')
  const wrongCategory = seriesByName.get('A')

  expect(selected.label.formatter({ dataIndex: 9, value: rows[19] })).toBe('p-19')
  expect(selected.labelLayout({ dataIndex: 9 }).hideOverlap).toBe(false)
  expect(selected.label.formatter({ dataIndex: 8, value: rows[17] })).toBe('')
  expect(wrongCategory.label.formatter({ dataIndex: 9, value: rows[18] })).toBe('')
  expect(wrongCategory.labelLayout({ dataIndex: 9 }).hideOverlap).toBe(true)

  const dense = pointFixture(rows) as any
  dense.spec.presentation.labelPolicy = { ...dense.spec.presentation.labelPolicy, density: 'dense' }
  dense.selection = envelope.selection
  const denseOption = echartsOption(dense, defaultRendererContext) as any
  const denseSeriesByName = new Map(denseOption.series.map((series: any) => [series.name, series]))
  expect(denseSeriesByName.get('B').labelLayout({ dataIndex: 9 }).hideOverlap).toBe(false)
  expect(denseSeriesByName.get('A').labelLayout({ dataIndex: 9 }).hideOverlap).toBe(true)
})

test('ECharts preserves explicit tree label density and crowds only visible automatic leaves', () => {
  for (const density of ['always', 'dense'] as const) {
    const option = echartsOption(treeFixture(density, 0), defaultRendererContext) as any
    expect(option.series[0].label.show).toBe(true)
    expect(option.series[0].data[0].label).toBeUndefined()
  }

  const collapsed = echartsOption(treeFixture('automatic', 1), defaultRendererContext) as any
  expect(collapsed.series[0].label.show).toBe(true)

  const expanded = echartsOption(treeFixture('automatic', 2), defaultRendererContext) as any
  expect(expanded.series[0].label.show).toBe(false)
  expect(expanded.series[0].data[0].label.show).toBe(true)
  expect(expanded.series[0].leaves.label.show).toBe(false)
})

function pointFixture(rows: unknown[][]): VisualizationEnvelope {
  return {
    schemaVersion: 9,
    visualID: 'point-label-regression',
    rendererID: 'echarts',
    specRevision: 'sha256:test',
    dataRevision: 1,
    spec: {
      kind: 'point',
      title: 'Point labels',
      datasets: [{ id: 'primary', fields: [
        { id: 'id', role: 'identity', dataType: 'string', nullable: false, label: 'ID' },
        { id: 'category', role: 'dimension', dataType: 'string', nullable: false, label: 'Category' },
        { id: 'x', role: 'metric', dataType: 'decimal', nullable: false, label: 'X' },
        { id: 'y', role: 'metric', dataType: 'decimal', nullable: false, label: 'Y' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' },
      accessibility: { title: 'Point labels', description: 'Point labels' },
      interactions: [],
      identity: [{ dataset: 'primary', field: 'id' }],
      x: { dataset: 'primary', field: 'x' },
      y: { dataset: 'primary', field: 'y' },
      color: { dataset: 'primary', field: 'category' },
      label: { dataset: 'primary', field: 'id' },
      colorScale: { kind: 'categorical' },
      presentation: { legend: 'hidden', labelPolicy: { density: 'automatic', priority: ['selected'], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, overplot: 'show_all', opacity: 1, largeMode: 'never', largeThreshold: 1000, brush: [] },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['id', 'category', 'x', 'y'], rows, completeness: 'complete' }] },
    selection: [],
    status: { kind: 'ready' },
    diagnostics: [],
  } as VisualizationEnvelope
}

function treeFixture(density: 'automatic' | 'always' | 'dense', initialDepth: number): VisualizationEnvelope {
  const rows: unknown[][] = [['root', null, 20]]
  for (const branch of ['branch-a', 'branch-b']) {
    rows.push([branch, 'root', 10])
    for (let index = 0; index < 10; index++) rows.push([`${branch}-${index}`, `root\u001f${branch}`, 1])
  }
  return {
    schemaVersion: 9,
    visualID: 'tree-label-regression',
    rendererID: 'echarts',
    specRevision: 'sha256:test',
    dataRevision: 1,
    spec: {
      kind: 'hierarchy',
      title: 'Tree labels',
      mark: 'tree',
      datasets: [{ id: 'primary', fields: [
        { id: 'node', role: 'identity', dataType: 'string', nullable: false, label: 'Node' },
        { id: 'parent', role: 'dimension', dataType: 'string', nullable: true, label: 'Parent' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' },
      accessibility: { title: 'Tree labels', description: 'Tree labels' },
      interactions: [],
      node: { dataset: 'primary', field: 'node' },
      parent: { dataset: 'primary', field: 'parent' },
      value: { dataset: 'primary', field: 'value' },
      presentation: { legend: 'hidden', labelPolicy: { density, priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, orientation: 'vertical', initialDepth, roam: true, layout: 'standard', breadcrumb: true },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['node', 'parent', 'value'], rows, completeness: 'complete' }] },
    selection: [],
    status: { kind: 'ready' },
    diagnostics: [],
  } as VisualizationEnvelope
}
