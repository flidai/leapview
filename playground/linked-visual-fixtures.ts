import type { FilterMenuSignal } from '../web/generated/signals'
import type { VisualizationEnvelope, VisualizationField, VisualizationFieldRef, VisualizationInteraction, VisualizationSpec } from '../web/generated/visualization'
import { currentVisualizationSchemaVersion } from '../web/generated/visualization/schema-version'
import { applyOptimisticInteraction, visualizationHighlightStates, visualizationSelectionEntries, type CanonicalInteractionSelection } from '../web/components/dashboard/interaction-selection'
import { answerChartWindow } from './chart-fixtures'

export const linkedRegions = ['North', 'South', 'East', 'West'] as const
export const linkedStatuses = ['complete', 'pending'] as const
export const linkedSortFields = ['region', 'orders', 'revenue', 'average'] as const
export const linkedVisualIDs = { chart: 'linked-regional-revenue', table: 'linked-regional-detail', kpi: 'linked-total-revenue' } as const
export const linkedInteractionID = 'linked-region-selection'
export const linkedRegionField = 'orders.region'

export interface LinkedExampleState {
  statuses: Array<typeof linkedStatuses[number]>
  selectedRegions: Array<typeof linkedRegions[number]>
  selectionSource: 'chart' | 'table'
  statusSearch: string
  sortField: typeof linkedSortFields[number]
  sortDirection: 'ascending' | 'descending'
}

export function defaultLinkedState(): LinkedExampleState {
  return { statuses: [], selectedRegions: [], selectionSource: 'chart', statusSearch: '', sortField: 'revenue', sortDirection: 'descending' }
}

/** Sharing accepts only the documented finite choices; no serialized envelope is trusted. */
export function normalizeLinkedState(value: unknown): LinkedExampleState {
  const input = value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : {}
  const defaults = defaultLinkedState()
  return {
    statuses: linkedStatuses.filter((status) => Array.isArray(input.statuses) && input.statuses.includes(status)),
    selectedRegions: linkedRegions.filter((region) => Array.isArray(input.selectedRegions) && input.selectedRegions.includes(region)),
    selectionSource: input.selectionSource === 'table' ? 'table' : 'chart',
    statusSearch: typeof input.statusSearch === 'string' ? input.statusSearch.slice(0, 80) : '',
    sortField: linkedSortFields.find((field) => field === input.sortField) ?? defaults.sortField,
    sortDirection: input.sortDirection === 'ascending' ? 'ascending' : 'descending',
  }
}

const orders = linkedRegions.flatMap((region, regionIndex) => Array.from({ length: 6 }, (_, index) => ({
  region,
  status: (index % 3 === 0 ? 'pending' : 'complete') as typeof linkedStatuses[number],
  revenue: 300 + regionIndex * 175 + index * 90,
})))

export function linkedSelections(state: LinkedExampleState): CanonicalInteractionSelection[] {
  let selections: CanonicalInteractionSelection[] = []
  for (const region of state.selectedRegions) selections = applyOptimisticInteraction(selections, {
    sourceKind: 'visual', sourceId: linkedVisualIDs[state.selectionSource], interactionKind: linkedInteractionID,
    action: 'set', toggle: false, mappings: [{ field: linkedRegionField, value: region, label: region }],
  })
  return selections
}

export function linkedFilterMenu(state: LinkedExampleState): FilterMenuSignal {
  const names = { complete: 'Complete', pending: 'Pending' }
  const query = state.statusSearch.trim().toLocaleLowerCase()
  return {
    id: 'linked-order-status', label: 'Order status', summaryLabel: !state.statuses.length || state.statuses.length === linkedStatuses.length ? 'All statuses' : state.statuses.map((status) => names[status]).join(', '),
    mode: 'multi', search: state.statusSearch, selected: [...state.statuses], loading: false, error: '', placeholder: 'Search statuses', emptyLabel: 'No matching statuses.',
    options: linkedStatuses.filter((status) => names[status].toLocaleLowerCase().includes(query)).map((status) => ({
      value: status, label: names[status], selected: state.statuses.includes(status), disabled: false,
      countLabel: String(orders.filter((order) => order.status === status).length),
    })),
  }
}

export interface LinkedVisualFixture {
  chart: VisualizationEnvelope
  table: VisualizationEnvelope
  kpi: VisualizationEnvelope
  tableRows: unknown[][]
  orderCount: number
  totalRevenue: number
  highlightedOrders: number
}

const ref = (field: string): VisualizationFieldRef => ({ dataset: 'primary', field })
const regionField: VisualizationField = { id: 'region', sourceRef: linkedRegionField, role: 'identity', dataType: 'string', nullable: false, label: 'Region' }
const revenueField: VisualizationField = { id: 'revenue', sourceRef: 'orders.revenue', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue', format: { kind: 'currency', currency: 'EUR', maximumFractionDigits: 0 } }

function interaction(target: string): VisualizationInteraction[] {
  return [{ id: linkedInteractionID, kind: 'select', mode: 'multiple', mappings: [{ source: ref('region'), targetFieldID: linkedRegionField }], targets: [{ visualID: target, effect: 'highlight' }], requiresStableIdentity: true }]
}

function inlineEnvelope(visualID: string, rendererID: string, spec: VisualizationSpec, rows: unknown[][], revision: number): VisualizationEnvelope {
  // A rebuilt local query is a new fixture revision. Pending table windows from
  // the previous filter must not overwrite its replacement.
  const specRevision = `playground:${visualID}:${revision}`
  return {
    schemaVersion: currentVisualizationSchemaVersion, visualID, rendererID, specRevision, spec, dataRevision: revision,
    dataState: { kind: 'inline', specRevision, dataRevision: revision, generation: 1, datasets: [{ id: 'primary', specRevision, dataRevision: revision, generation: 1, columns: spec.datasets[0]!.fields.map((field) => field.id), rows, completeness: rows.length ? 'complete' : 'empty' }] },
    selection: [], highlights: [], status: { kind: rows.length ? 'ready' : 'no_data' }, diagnostics: [],
  }
}

export function createLinkedVisualFixture(state: LinkedExampleState = defaultLinkedState(), revision = 1): LinkedVisualFixture {
  const visible = orders.filter((order) => !state.statuses.length || state.statuses.includes(order.status))
  const totals = linkedRegions.map((region) => {
    const regionOrders = visible.filter((order) => order.region === region)
    const revenue = regionOrders.reduce((sum, order) => sum + order.revenue, 0)
    return { region, orders: regionOrders.length, revenue, average: regionOrders.length ? revenue / regionOrders.length : 0 }
  })
  const tableRows = totals.map((row) => [row.region, row.orders, row.revenue, row.average])
  const orderCount = visible.length
  const totalRevenue = visible.reduce((sum, order) => sum + order.revenue, 0)
  const base = { dataBudget: { maxRows: 100, requiredCompleteness: 'complete' as const } }
  const chartSpec: VisualizationSpec = {
    ...base, kind: 'cartesian', mark: 'bar', title: 'Revenue by region',
    accessibility: { title: 'Revenue by region', description: 'Revenue for each region in the current order-status filter. Region selections highlight related data and preserve totals.', announceChanges: true },
    datasets: [{ id: 'primary', fields: [regionField, revenueField] }], interactions: interaction(linkedVisualIDs.table),
    x: ref('region'), y: [ref('revenue')], tooltip: [ref('region'), ref('revenue')],
    presentation: { legend: 'hidden', labelPolicy: { density: 'automatic', priority: ['selected'], maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true }, axisVisible: true, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false, orientation: 'horizontal' },
  }
  const fields: VisualizationField[] = [regionField, { id: 'orders', role: 'metric', dataType: 'integer', nullable: false, label: 'Orders', format: { kind: 'number', maximumFractionDigits: 0 } }, revenueField, { ...revenueField, id: 'average', label: 'Average order', sourceRef: 'orders.average_revenue' }]
  const tableSpec: VisualizationSpec = {
    ...base, kind: 'table', title: 'Regional detail',
    accessibility: { title: 'Regional detail', description: 'Select a region with any table cell to highlight its revenue bar. Totals remain unchanged.', announceChanges: true },
    datasets: [{ id: 'primary', fields }], interactions: interaction(linkedVisualIDs.chart),
    columns: fields.map((field) => ({ field: ref(field.id), label: field.label, width: field.id === 'region' ? 140 : 150, formatting: [] })),
    defaultSort: [{ field: ref(state.sortField), direction: state.sortDirection }], presentation: { rowHeight: 38, striped: true, showHeader: true },
  }
  const chart = inlineEnvelope(linkedVisualIDs.chart, 'echarts', chartSpec, totals.map((row) => [row.region, row.revenue]), revision)
  let table = inlineEnvelope(linkedVisualIDs.table, 'tanstack', tableSpec, tableRows, revision)
  const sort = [{ field: ref(state.sortField), direction: state.sortDirection }]
  table.dataState = { kind: 'windowed', specRevision: table.specRevision, dataRevision: revision, generation: 1, schema: { id: 'primary', fields }, cardinality: { kind: 'exact', count: tableRows.length }, availableRows: tableRows.length, rowCap: 100, chunkSize: 50, resetVersion: 0, sort, blocks: {} }
  table = answerChartWindow(table, tableRows, { visualID: table.visualID, specRevision: table.specRevision, dataRevision: revision, requestSeq: 0, resetVersion: 0, start: 0, limit: 50, blockID: 'all', sort })
  const kpi = inlineEnvelope(linkedVisualIDs.kpi, 'html', {
    ...base, kind: 'kpi', title: 'Revenue in view',
    accessibility: { title: 'Revenue in view', description: `Revenue across ${orderCount} orders in the current filter. Region selections do not change this total.`, announceChanges: true },
    datasets: [{ id: 'primary', fields: [revenueField] }], interactions: [], value: ref('revenue'),
    presentation: { mode: 'compact', delta: 'absolute', favorableDirection: 'neutral', missingComparison: 'hide', ranges: [], displayUnits: 'none', note: `${orderCount} orders · all regions` },
  }, [[totalRevenue]], revision)
  return projectLinkedSelections({ chart, table, kpi, tableRows, orderCount, totalRevenue, highlightedOrders: 0 }, state)
}

export function projectLinkedSelections(fixture: LinkedVisualFixture, state: LinkedExampleState): LinkedVisualFixture {
  const selections = linkedSelections(state)
  const visuals = { [fixture.chart.visualID]: fixture.chart, [fixture.table.visualID]: fixture.table }
  const apply = (envelope: VisualizationEnvelope): VisualizationEnvelope => ({
    ...envelope, selection: visualizationSelectionEntries(envelope, selections), highlights: visualizationHighlightStates(envelope, visuals, selections, []),
  })
  return {
    ...fixture, chart: apply(fixture.chart), table: apply(fixture.table),
    highlightedOrders: orders.filter((order) => (!state.statuses.length || state.statuses.includes(order.status)) && state.selectedRegions.includes(order.region)).length,
  }
}
