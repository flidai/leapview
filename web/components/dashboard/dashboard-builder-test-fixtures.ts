import { typographyTestTokens } from '../test-typography-tokens'
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

export function builderTestDocument(): string {
  const visualCatalog = [
    ['line', 'Line chart', 'Cartesian'], ['area', 'Area chart', 'Cartesian'], ['bar', 'Bar chart', 'Cartesian'], ['column', 'Column chart', 'Cartesian'], ['pie', 'Pie chart', 'Part to whole'], ['donut', 'Donut chart', 'Part to whole'], ['scatter', 'Scatter chart', 'Distribution'], ['funnel', 'Funnel chart', 'Part to whole'],
    ['treemap', 'Treemap', 'Hierarchy & flow'], ['gauge', 'Gauge', 'Specialized'], ['heatmap', 'Heatmap', 'Distribution'], ['sankey', 'Sankey', 'Hierarchy & flow'], ['graph', 'Graph', 'Hierarchy & flow'], ['map', 'Map', 'Specialized'],
    ['candlestick', 'Candlestick chart', 'Cartesian'], ['boxplot', 'Boxplot', 'Distribution'], ['combo', 'Combo chart', 'Cartesian'], ['waterfall', 'Waterfall chart', 'Cartesian'], ['histogram', 'Histogram', 'Distribution'], ['radar', 'Radar chart', 'Specialized'], ['tree', 'Tree', 'Hierarchy & flow'], ['sunburst', 'Sunburst', 'Hierarchy & flow'], ['kpi', 'KPI', 'Specialized'],
    ['table', 'Table', 'Tables'], ['matrix', 'Matrix', 'Tables'], ['pivot', 'Pivot', 'Tables'],
  ].map(([type, label, group]) => ({ type, label, group, referenceHref: `/docs/visuals/${type}`, roles: type === 'table' || type === 'map' ? ['detail'] : type === 'kpi' || type === 'gauge' || type === 'histogram' || type === 'boxplot' ? ['metric'] : ['dimension', 'metric'] }))
  const signals = {
    builder: {
      projectId: 'sales', dashboardId: 'revenue', draftId: 'draft-7',
      revision: { id: 'rev-7', number: 7, contentHash: 'sha256:abc' },
      title: 'Revenue draft', lifecycle: 'draft', visibility: 'private', hasUnpublishedChanges: true,
      appearance: { icon: 'chart-no-axes-combined', color: 'purple' },
      origin: { kind: 'file', label: 'Project file', sourcePath: 'dashboards/revenue.yaml' },
      sourceEvidence: { kind: 'project', projectId: 'sales', dashboardId: 'revenue', generationId: 'generation-7' },
      semanticModel: { id: 'commerce', title: 'Orders', datasets: [{ id: 'orders', title: 'Orders', fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', dataType: 'string' }, { id: 'orders.total', label: 'Total', kind: 'metric', dataType: 'decimal' }] }] },
      visualCatalog,
      filters: [],
      pages: [
        { id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [{ id: 'sales-chart', visualId: 'sales-chart', title: 'Sales by status', titleVisible: true, type: 'bar', legendVisible: true, axisVisible: true, dataLabelsVisible: false, formatOptions: [
          { key: 'axisVisible', label: 'Show axes', section: 'Display', control: 'toggle', value: 'true', choices: [] },
          { key: 'legend', label: 'Legend', section: 'Display', control: 'select', value: 'right', choices: [{ value: 'none', label: 'None' }, { value: 'top', label: 'Top' }, { value: 'right', label: 'Right' }, { value: 'bottom', label: 'Bottom' }, { value: 'left', label: 'Left' }] },
          { key: 'labels.density', label: 'Data labels', section: 'Display', control: 'select', value: 'hidden', choices: [{ value: 'hidden', label: 'Hidden' }, { value: 'automatic', label: 'Automatic' }, { value: 'dense', label: 'Dense' }, { value: 'always', label: 'Always' }] },
          { key: 'stacking', label: 'Stacking', section: 'Chart', control: 'select', value: 'none', choices: [{ value: 'none', label: 'None' }, { value: 'normal', label: 'Normal' }, { value: 'percent', label: 'Percent' }] },
        ], placement: { col: 1, row: 1, colSpan: 6, rowSpan: 5 }, slots: [{ id: 'category', label: 'Category', kind: 'dimension', fieldId: 'orders.status', required: true }], queryOptions: { supportsSort: true, supportsLimit: true, sort: [] }, filters: [] }], filterComponents: [] },
        { id: 'details', title: 'Details', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [], filterComponents: [] },
      ],
      selectedPageId: 'overview', selectedVisualId: 'sales-chart',
      capabilities: { canEdit: true, canShare: true, canPublish: true, canArchive: true, canPreview: true, canExport: true, canAddPage: true, canAddVisual: true },
      diagnostics: [{ severity: 'warning', code: 'FIELD_REQUIRED', message: 'Add a metric to complete this visual.' }],
      preview: { active: false, mode: 'draft', loading: false, href: '/dashboards/revenue/preview?draft=draft-7&revisionId=rev-7&revisionNumber=7&revisionContentHash=sha256%3Aabc' }, save: { state: 'dirty', message: '2 changes' },
    },
    status: { loading: false, error: '', generation: 0, lastUpdated: '', refreshId: '', setupRequired: false, progressPercent: 100 },
    runtime: { kind: 'dashboard_builder', projectId: 'sales', servingStateId: 'generation-7', dashboardId: 'revenue' },
  }
  return `<!doctype html><html><head><style>html,body{margin:0;min-height:100%;}body{${typographyTestTokens}--lv-bg-app:#f6f8fa;--lv-bg-panel:#fff;--lv-bg-panel-muted:#f6f8fa;--lv-bg-control:#f6f8fa;--lv-bg-control-hover:#f3f4f6;--lv-bg-input:#fff;--lv-report-page-bg:#fbfcfe;--lv-report-canvas-bg:#eef1f4;--lv-chart-surface:#fff;--lv-bg-accent-muted:#ddf4ff;--lv-bg-danger-muted:#ffebe9;--lv-fg-default:#24292f;--lv-fg-muted:#57606a;--lv-fg-accent:#0969da;--lv-fg-danger:#d1242f;--lv-fg-warning:#9a6700;--lv-fg-success:#1a7f37;--lv-border-muted:#d8dee4;--lv-border-default:#d0d7de;--lv-line-default:#d0d7de;--lv-line-muted:#d8dee4;--lv-line-emphasis:#57606a;--lv-data-1:#0969da;--lv-data-1-muted:#ddf4ff;--lv-data-2:#1a7f37;--lv-data-2-muted:#dafbe1;--lv-data-3:#8250df;--lv-data-3-muted:#fbefff;--lv-data-4:#cf222e;--lv-data-4-muted:#ffebe9;--lv-data-5:#1b7c83;--lv-data-5-muted:#ddf4ff;--lv-data-6:#bf3989;--lv-data-6-muted:#ffeff7;--lv-border-width:1px;--lv-border-width-focus:2px;--lv-radius-default:6px;--lv-radius-small:4px;--lv-radius-full:999px;--base-size-2:2px;--base-size-4:4px;--base-size-6:6px;--base-size-8:8px;--base-size-12:12px;--base-size-16:16px;--control-medium-size:32px;--control-small-size:28px;--lv-button-radius:6px;--lv-button-padding-inline:12px;--lv-button-fg-rest:#24292f;--lv-button-bg-rest:#fff;--lv-button-bg-hover:#f6f8fa;--lv-button-accent-border-rest:#0969da;--lv-button-accent-fg-rest:#fff;--lv-button-accent-bg-rest:#0969da;--lv-button-accent-bg-hover:#0757b3;--lv-shadow-floating-sm:0 2px 8px rgb(0 0 0 / 12%);}</style></head><body><main data-signals="${escapeHTML(JSON.stringify(signals))}"><lv-dashboard-builder back-href="/" preview-href="/dashboards/revenue/preview?draft=draft-7&revisionId=rev-6&revisionNumber=6&revisionContentHash=sha256%3Aold"></lv-dashboard-builder></main><script type="module" src="/dashboard-builder-under-test.js"></script><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script></body></html>`
}

function escapeHTML(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}
