import type { DashboardVisualizationSignal } from '../../generated/signals'

export function governedBarPreviewEnvelope(revision: string): DashboardVisualizationSignal {
  const dataState = {
    kind: 'inline', specRevision: revision, dataRevision: 1, generation: 1,
    datasets: [{ id: 'primary', specRevision: revision, dataRevision: 1, generation: 1, columns: ['category', 'value'], rows: [['Delivered', 42], ['Shipped', 7]], completeness: 'complete' }],
  }
  return {
    schemaVersion: 14, visualID: 'sales-chart', rendererID: 'echarts', specRevision: revision, dataRevision: 1,
    spec: {
      kind: 'cartesian', mark: 'bar', title: 'Sales by status',
      datasets: [{ id: 'primary', fields: [
        { id: 'category', role: 'dimension', dataType: 'string', nullable: false, label: 'Status' },
        { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Orders' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' },
      accessibility: { title: 'Sales by status', description: 'Sales grouped by status.' }, interactions: [],
      x: { dataset: 'primary', field: 'category' }, y: [{ dataset: 'primary', field: 'value' }],
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false },
    },
    dataState: { schemaVersion: 1, encoding: 'json', kind: 'inline', specRevision: revision, dataRevision: 1, generation: 1, payload: JSON.stringify(dataState) },
    selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [], servingStateID: 'serving-test', streamGeneration: 1, filterRevision: 0, interactionRevision: 0, consumerIdentity: 'visual:sales-chart',
  }
}

export function headerlessKPIPreviewEnvelope(revision: string): DashboardVisualizationSignal {
  const dataState = {
    kind: 'inline', specRevision: revision, dataRevision: 1, generation: 1,
    datasets: [{ id: 'primary', specRevision: revision, dataRevision: 1, generation: 1, columns: ['value'], rows: [[42]], completeness: 'complete' }],
  }
  return {
    schemaVersion: 14, visualID: 'sales-chart', rendererID: 'html', specRevision: revision, dataRevision: 1,
    spec: {
      kind: 'kpi', title: 'Total orders',
      datasets: [{ id: 'primary', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Orders' }] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' },
      accessibility: { title: 'Total orders', description: 'Total order count.' }, interactions: [],
      value: { dataset: 'primary', field: 'value' },
      presentation: { delta: 'absolute', favorableDirection: 'increase', missingComparison: 'hide', mode: 'compact', ranges: [], tone: 'ink' },
    },
    dataState: { schemaVersion: 1, encoding: 'json', kind: 'inline', specRevision: revision, dataRevision: 1, generation: 1, payload: JSON.stringify(dataState) },
    selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [], servingStateID: 'serving-test', streamGeneration: 1, filterRevision: 0, interactionRevision: 0, consumerIdentity: 'visual:sales-chart',
  }
}
