import type { VisualizationEnvelope } from '../../../../generated/visualization'

export function cartesianFixture(mark: string, columns = ['label', 'value']): VisualizationEnvelope {
  const fields = columns.map((id, index) => ({ id, role: index === 0 ? 'dimension' : 'metric', dataType: index === 0 || id === 'row' ? 'string' : 'decimal', nullable: false, label: id }))
  const y = columns.slice(1).map((field) => ({ dataset: 'primary', field }))
  const row = columns.map((id, index) => index === 0 ? 'A' : id === 'row' ? 'R1' : index)
  return {
    schemaVersion: 9, visualID: mark, rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: { kind: 'cartesian', title: mark, mark, datasets: [{ id: 'primary', fields }], dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: mark, description: mark }, interactions: [], x: { dataset: 'primary', field: 'label' }, y, presentation: { legend: 'bottom', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, smooth: true, stacked: true, showSymbols: false, dataZoom: true, area: mark === 'area', step: true, symbolSize: 12, labelPosition: 'top', orientation: mark === 'bar' ? 'horizontal' : 'vertical', histogramBins: mark === 'histogram' ? 10 : undefined } },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns, rows: [row], completeness: 'complete' }] }, selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as unknown as VisualizationEnvelope
}

export function proportionalFixture(mark: 'pie' | 'donut' | 'funnel'): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: mark, rendererID: 'echarts', specRevision: 'sha256:test', dataRevision: 1,
    spec: { kind: 'proportional', title: mark, mark, datasets: [{ id: 'primary', fields: [{ id: 'label', role: 'dimension', dataType: 'string', nullable: false, label: 'Label' }, { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' }] }], dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: mark, description: mark }, interactions: [], category: { dataset: 'primary', field: 'label' }, value: { dataset: 'primary', field: 'value' }, presentation: { legend: 'right', labelPolicy: { density: 'automatic', priority: ['selected', 'anomaly', 'threshold'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, orientation: 'vertical', rose: true, centerLabel: mark === 'donut' ? 'Orders' : undefined, labelPosition: 'outside', innerRadius: mark === 'donut' ? 0.54 : undefined, outerRadius: mark === 'donut' ? 0.76 : undefined, align: mark === 'funnel' ? 'left' : undefined, sort: mark === 'funnel' ? 'ascending' : undefined } },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:test', dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['A', 10]], completeness: 'complete' }] }, selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as unknown as VisualizationEnvelope
}
