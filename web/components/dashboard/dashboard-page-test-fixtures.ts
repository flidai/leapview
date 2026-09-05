import { typographyTestTokens } from '../test-typography-tokens'
import type { DashboardVisualizationSignal } from '../../generated/signals'
import type {
  VisualizationDataState,
  VisualizationDataStateTransport,
  VisualizationEnvelope,
  VisualizationField,
  VisualizationSpecBase,
} from '../../generated/visualization'

export function testDocument(): string {
  const page = {
    kind: 'dashboard', title: 'Executive Sales Dashboard', dashboardId: 'executive-sales', dashboardTitle: 'Executive Sales Dashboard',
    appearanceIcon: 'gallery-vertical-end', appearanceColor: 'blue',
    pageId: 'overview', pageTitle: 'Overview', headerDetail: 'Overview', modelId: 'olist', modelTitle: 'Olist',
    canvas: { width: 1024, height: 720 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 },
    pages: [
      { id: 'overview', title: 'Overview', href: '/dashboards/executive-sales/pages/overview', active: true },
      { id: 'details', title: 'Details', href: '/dashboards/executive-sales/pages/details', active: false },
    ],
    components: [
      { id: 'title', kind: 'header', x: 16, y: 16, width: 456, height: 88, title: 'Executive Sales' },
      { id: 'state-slicer', kind: 'slicer', binding: { scope: 'page', id: 'state' }, presentation: { style: 'dropdown', search: true, selectAll: false, showCounts: false, showSummary: true, compact: false }, x: 488, y: 16, width: 216, height: 88 },
      { id: 'orders-kpi', kind: 'visual', visual: 'orders_kpi', x: 720, y: 16, width: 240, height: 88 },
      { id: 'orders-chart', kind: 'visual', visual: 'orders_chart', x: 16, y: 128, width: 456, height: 280 },
      { id: 'orders-table', kind: 'visual', visual: 'orders', x: 16, y: 760, width: 944, height: 280 },
    ],
  }
  const interactionSelections = [
      { sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection', entries: [{ label: 'Delivered', mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered' }] }] },
      { sourceKind: 'visual', sourceId: 'orders', interactionKind: 'row_selection', entries: [{ label: 'o1', mappings: [{ field: 'orders.order_id', dataset: 'orders', value: 'o1' }] }] },
  ]
  const filterState = {
    revision: 0,
    appliedControls: {
      fb_state: {
        expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'SP' }] },
        resolvedExpression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'SP' }] },
      },
    },
    draftControls: {},
    dirtyBindings: [],
    defaultsRevision: 'v1',
  }
  const signals = {
    page,
    filterContract: {
      applicationMode: 'immediate',
      definitions: {
        state: {
          id: 'state', label: 'State', field: 'orders.state', valueKind: 'string',
          predicates: [{ kind: 'set', operators: ['in'] }],
          options: { kind: 'distinct', limit: 50, values: [] },
          timezone: 'UTC', calendar: 'gregorian', weekStart: 'monday',
        },
      },
      bindings: {
        fb_state: {
          key: 'fb_state', id: 'state', filter: 'state', scope: 'page', pageID: 'overview',
          default: { kind: 'unfiltered' }, selectionMode: 'multiple', maxSelectedValues: 0,
          readerEditable: true, paneVisible: true, paneOrder: 0, targets: ['overview/orders-chart'],
          optionDependencies: [],
        },
      },
    },
    filterState,
    filterOptionPages: {
      fb_state: {
        bindingKey: 'fb_state', items: [{ value: { kind: 'string', value: 'SP' }, label: 'SP', selected: false, available: true }],
        complete: true, servingStateID: 'serving-test', streamGeneration: 3, filterRevision: 0,
        requestGeneration: 0, consumerIdentity: 'option:fb_state',
      },
    },
    filterValidation: {
      accepted: true,
      message: '',
      currentRevision: 0,
      clientMutationID: '',
    },
    runtime: {
      kind: 'dashboard', clientId: 'dashboard-test', streamInstanceId: 'stream-test',
      projectId: 'project:leapview-evaluation', dashboardId: 'executive-sales', pageId: 'overview', servingStateId: 'serving-test',
    },
    interactionSelections,
    interactionRevision: 0,
    spatialSelections: [],
    visuals: testVisualizationSignals(),
    status: { loading: true, error: '', refreshId: 'refresh-3', generation: 3, lastUpdated: '2026-07-18T10:00:00Z', setupRequired: false, progressPercent: 50 },
    agent: {
      conversations: [],
      activeConversationId: '',
      transcript: [],
      status: { enabled: true, running: false },
      composer: { value: '', disabled: false, placeholder: 'Ask about this dashboard...' },
    },
    agentContext: {
      surface: 'dashboard',
      dashboardId: 'executive-sales',
      dashboardTitle: 'Executive Sales Dashboard',
      pageId: 'overview',
      pageTitle: 'Overview',
      modelId: 'olist',
      generation: 3,
      filters: filterState,
      references: [],
    },
    agentReferenceSearch: { query: '', requestId: 0, results: [] },
    agentVisuals: {},
  }
  const attr = (value: unknown) => escapeHTML(JSON.stringify(value))
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-panel: #fff; --lv-bg-panel-muted: #eaeef2; --lv-bg-control-hover: #f3f4f6; --lv-chart-surface: #fff; --lv-report-page-bg: #fff; --lv-report-canvas-bg: #eaeef2; --lv-report-rail-bg: #fff; --lv-bg-overlay: #fff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-line-muted: #d8dee4; --lv-scrollbar-thumb: #8c959f; --lv-scrollbar-thumb-hover: #6e7781; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-border-transparent: 1px solid transparent; --lv-radius-default: 6px; --lv-radius-full: 999px; --lv-sidebar-width-expanded: 248px; --lv-sub-sidebar-width-expanded: 144px; --lv-page-rail-width-collapsed: 38px; --lv-dashboard-filter-open-width: 320px; --lv-dashboard-agent-width: 420px; --base-size-2: 2px; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --base-size-24: 24px; --borderWidth-default: 1px; --control-small-size: 28px; --control-medium-size: 32px; --control-xlarge-size: 40px; --focus-outline: 2px solid #0969da; --focus-outline-offset: -2px; --zIndex-dropdown: 100; --zIndex-modal: 200; --zIndex-sticky: 50; --shadow-resting-small: 0 1px 2px rgb(0 0 0 / .08); --shadow-floating-small: 0 8px 24px rgb(0 0 0 / .12); --lv-duration-fast: 160ms; --spinner-size-small: 16px; --spinner-size-medium: 32px; --spinner-size-large: 64px; --base-duration-1000: 1000ms; --base-easing-linear: linear; --motion-easing-move: ease; --motion-transition-stateChange: 160ms ease; }
          body { --lv-loading-delay-short: 250ms; --lv-loading-delay-long: 500ms; }
          lv-dashboard-page { min-height: 720px; }
        </style>
      </head>
      <body>
        <main data-signals="${attr(signals)}">
          <lv-dashboard-page></lv-dashboard-page>
        </main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/dashboard-page-under-test.js"></script>
      </body>
    </html>
  `
}

export function testVisualizationEnvelopes(): Record<string, VisualizationEnvelope> {
  const kpiRevision = `sha256:${'1'.repeat(64)}`
  const chartRevision = `sha256:${'2'.repeat(64)}`
  const tableRevision = `sha256:${'3'.repeat(64)}`
  const field = (id: string, role: VisualizationField['role'], dataType: VisualizationField['dataType'], label: string): VisualizationField => ({ id, role, dataType, nullable: false, label })
  const base = (title: string, fields: VisualizationField[]): Omit<VisualizationSpecBase, 'kind'> => ({ title, datasets: [{ id: 'primary', fields }], dataBudget: { maxRows: 1000, requiredCompleteness: 'complete' }, accessibility: { title, description: title }, interactions: [] })
  const inline = (revision: string, columns: string[], rows: unknown[][]): Extract<VisualizationDataState, { kind: 'inline' }> => ({ kind: 'inline', specRevision: revision, dataRevision: 1, generation: 3, datasets: [{ id: 'primary', specRevision: revision, dataRevision: 1, generation: 3, columns, rows, completeness: 'complete' }] })
  const envelopes: Record<string, VisualizationEnvelope> = {
    orders_kpi: { schemaVersion: 11, visualID: 'orders_kpi', rendererID: 'html', specRevision: kpiRevision, dataRevision: 1, spec: { ...base('Orders', [field('value', 'metric', 'decimal', 'Orders')]), kind: 'kpi', value: { dataset: 'primary', field: 'value' }, presentation: { mode: 'compact', delta: 'absolute', favorableDirection: 'neutral', missingComparison: 'show_unavailable', ranges: [], tone: 'ink', note: 'Filtered' } }, dataState: inline(kpiRevision, ['value'], [[42]]), selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [] },
    orders_chart: { schemaVersion: 11, visualID: 'orders_chart', rendererID: 'echarts', specRevision: chartRevision, dataRevision: 1, spec: { ...base('Orders by status', [field('label', 'identity', 'string', 'Status'), field('value', 'metric', 'decimal', 'Orders')]), kind: 'cartesian', mark: 'bar', interactions: [{ id: 'selection', kind: 'select', mappings: [{ source: { dataset: 'primary', field: 'label' }, targetFieldID: 'orders.status', targetDatasetID: 'orders' }], targets: [{ visualID: 'orders_kpi', effect: 'highlight' }, { visualID: 'orders', effect: 'filter' }], mode: 'multiple', requiresStableIdentity: true }], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } }, dataState: inline(chartRevision, ['label', 'value'], [['delivered', 42], ['shipped', 7]]), selection: [], highlights: [], status: { kind: 'loading', message: 'Refreshing' }, diagnostics: [] },
    orders: { schemaVersion: 11, visualID: 'orders', rendererID: 'tanstack', specRevision: tableRevision, dataRevision: 1, spec: { ...base('Orders', [field('order_id', 'identity', 'string', 'Order')]), kind: 'table', dataBudget: { maxRows: 1000, requiredCompleteness: 'partial' }, columns: [{ field: { dataset: 'primary', field: 'order_id' }, label: 'Order', width: 180, formatting: [] }], defaultSort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], presentation: { rowHeight: 28, striped: true, showHeader: true } }, dataState: { kind: 'windowed', specRevision: tableRevision, dataRevision: 1, generation: 3, schema: { id: 'primary', fields: [field('order_id', 'identity', 'string', 'Order')] }, cardinality: { kind: 'exact', count: 250 }, availableRows: 250, rowCap: 1000, chunkSize: 50, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], blocks: { a: { id: 'a', start: 0, rows: Array.from({ length: 50 }, (_, index) => [`o${index + 1}`]), requestSeq: 0, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }] } } }, selection: [], highlights: [], status: { kind: 'error', message: 'Ratings query failed' }, diagnostics: [{ code: 'query_failed', severity: 'error', message: 'Ratings query failed' }] },
  }
  return envelopes
}

export function testVisualizationSignals(): Record<string, DashboardVisualizationSignal> {
  const signals: Record<string, DashboardVisualizationSignal> = {}
  for (const [id, envelope] of Object.entries(testVisualizationEnvelopes())) {
    const { dataState, ...signal } = envelope
    signals[id] = {
      ...signal,
      servingStateID: 'serving-test',
      streamGeneration: 3,
      filterRevision: 0,
      interactionRevision: 0,
      consumerIdentity: `visual:${id}`,
      dataState: visualizationDataStateTransport(dataState),
    }
  }
  return signals
}

export function visualizationDataStateTransport(dataState: VisualizationDataState): VisualizationDataStateTransport {
  return {
    schemaVersion: 1,
    encoding: 'json',
    kind: dataState.kind,
    specRevision: dataState.specRevision,
    dataRevision: dataState.dataRevision,
    generation: dataState.generation,
    payload: JSON.stringify(dataState),
  }
}

export function escapeHTML(value: string): string { return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;') }
