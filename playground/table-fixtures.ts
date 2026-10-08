import { emptyDataExploreCommand, explorationSpecFromCommand } from '../web/components/data/data-explorer-spec'
import type { DataExploreCommand, DataExploreResultSignal, DataExplorerCommand, DataPreviewSignal, RecordTableSignal } from '../web/generated/signals'
import type { EntityListColumn, EntityListItem } from '../web/components/shared/entity-list'
import type { WindowedTableBlockID, WindowedTableColumn, WindowedTablePayload, WindowedTableRequest, WindowedTableSort } from '../web/components/shared/windowed-table'

export type TableState = 'populated' | 'empty' | 'loading' | 'error'
export const tableColumns: WindowedTableColumn[] = [
  { key: 'id', label: 'Order ID', type: 'BIGINT', width: 130, sortable: true },
  { key: 'customer', label: 'Customer', type: 'VARCHAR', width: 210, sortable: true },
  { key: 'region', label: 'Region', type: 'VARCHAR', width: 150, sortable: true },
  { key: 'revenue', label: 'Revenue', type: 'DECIMAL(12,2)', align: 'right', width: 130, sortable: true },
  { key: 'ordered_at', label: 'Ordered at', type: 'DATE', width: 160, sortable: true },
  { key: 'fulfilled', label: 'Fulfilled', type: 'BOOLEAN', width: 130, sortable: true },
]

export function tableRows(count = 1000): Record<string, unknown>[] {
  const names = ['Acme Studio', 'Northwind Labs', 'Cedar & Co.', 'Atlas Works', 'Fern Design', 'Harbor Supply']
  const regions = ['Europe', 'Americas', 'Asia Pacific']
  return Array.from({ length: count }, (_, index) => ({
    id: 10001 + index,
    customer: names[index % names.length],
    region: regions[index % regions.length],
    revenue: Math.round((45 + (index * 137) % 6000 + (index % 10) / 10) * 100) / 100,
    ordered_at: `2026-09-${String(index % 28 + 1).padStart(2, '0')}`,
    fulfilled: index % 5 !== 0,
  }))
}

export function sortedTableRows(rows: Record<string, unknown>[], sort: WindowedTableSort): Record<string, unknown>[] {
  const key = sort.key || sort.column
  if (!key || !sort.direction) return [...rows]
  const direction = sort.direction === 'desc' ? -1 : 1
  return [...rows].sort((left, right) => {
    const a = left[key], b = right[key]
    const result = typeof a === 'number' && typeof b === 'number' ? a - b : String(a ?? '').localeCompare(String(b ?? ''), 'en', { numeric: true })
    return result * direction || Number(left.id) - Number(right.id)
  })
}

export function windowedFixture(state: TableState = 'populated', resetVersion = 0): WindowedTablePayload {
  const rows = state === 'empty' ? [] : tableRows()
  const table: WindowedTablePayload = {
    tableKey: 'playground-orders', title: 'Orders', columns: tableColumns,
    totalRows: rows.length, availableRows: rows.length, chunkSize: 50, rowHeight: 34,
    resetVersion, sort: {}, blocks: {}, visibleColumns: [], columnWidths: {},
    totalLabel: `${rows.length.toLocaleString('en-US')} rows`,
    error: state === 'error' ? 'The local preview fixture could not be loaded.' : '',
    loadingBlock: state === 'loading' ? 'all' : '',
  }
  if (state === 'loading') return table
  // Keep the last received rows during an error, as the data preview does in the
  // product. This also avoids an automatic initial fetch dismissing the failure.
  const populated = answerWindow(table, rows, { block: 'all', start: 0, count: 50, requestSeq: 0, resetVersion, sort: {} })
  return { ...populated, error: table.error }
}

/** Answer the same three-slot protocol as the serving table adapters. */
export function answerWindow(table: WindowedTablePayload, rows: Record<string, unknown>[], request: WindowedTableRequest): WindowedTablePayload {
  const chunk = table.chunkSize || request.count || 50
  const sorted = sortedTableRows(rows, request.sort)
  const currentStart = Math.max(0, Math.floor(request.start / chunk) * chunk)
  const starts = currentStart === 0 ? [0, chunk, chunk * 2] : [Math.max(0, currentStart - chunk), currentStart, currentStart + chunk]
  const blocks = { ...table.blocks }
  const fill = (id: WindowedTableBlockID, start: number) => {
    blocks[id] = { start, requestSeq: request.requestSeq, resetVersion: request.resetVersion, sort: request.sort, rows: sorted.slice(start, start + chunk) }
  }
  if (request.block === 'all') (['a', 'b', 'c'] as const).forEach((id, index) => fill(id, starts[index]))
  else fill(request.block, request.start)
  return { ...table, blocks, sort: request.sort, resetVersion: request.resetVersion, error: '', loadingBlock: '' }
}

export function previewCommand(): DataExplorerCommand {
  return { objectKey: 'playground.orders', offset: 0, limit: 50, start: 0, count: 50, block: 'all', requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} }
}

export function previewFixture(table: WindowedTablePayload): DataPreviewSignal {
  const blocks: DataPreviewSignal['blocks'] = {}
  for (const [id, block] of Object.entries(table.blocks ?? {})) {
    if (block) blocks[id] = { ...block, sort: { column: block.sort.key || block.sort.column || '', direction: block.sort.direction || '' } }
  }
  return {
    columns: tableColumns.map(({ key, label, type }) => ({ key, label, type })),
    totalRows: table.totalRows ?? 0, availableRows: table.availableRows ?? 0,
    chunkSize: table.chunkSize ?? 50, rowHeight: table.rowHeight ?? 34, resetVersion: table.resetVersion ?? 0,
    sort: { column: table.sort?.key || table.sort?.column || '', direction: table.sort?.direction || '' },
    blocks, totalRowLabel: String(table.totalRows ?? 0), loadingBlock: table.loadingBlock, error: table.error,
    sql: 'SELECT * FROM orders', loading: Boolean(table.loadingBlock), stale: false,
  }
}

export function exploreCommand(): DataExploreCommand {
  const command: DataExploreCommand = { ...emptyDataExploreCommand, semanticModelId: 'playground-sales', datasetId: 'orders', dimensions: ['id', 'customer', 'region', 'ordered_at', 'fulfilled'], metrics: ['revenue'], filters: [], sort: [], limit: 75, requestSeq: 0, resetVersion: 0, columnWidths: {} }
  return { ...command, spec: explorationSpecFromCommand(command) }
}

export function exploreFixture(command: DataExploreCommand, state: TableState = 'populated', truncated = false): DataExploreResultSignal {
  const sort = command.sort[0]
  const rows = state === 'empty' || state === 'error' ? [] : sortedTableRows(tableRows(75), { key: sort?.field, direction: sort?.direction })
  const request = command.window ?? { block: 'all', start: 0, count: 100, requestSeq: command.requestSeq, resetVersion: command.resetVersion }
  const table = answerWindow({
    tableKey: 'playground-exploration', columns: tableColumns, totalRows: rows.length,
    availableRows: rows.length, chunkSize: request.count, rowHeight: 32, blocks: {},
  }, rows, { ...request, sort: { key: sort?.field, column: sort?.field, direction: sort?.direction } })
  return {
    columns: tableColumns.map(({ key, label, type }) => ({ key, label, type })), rows,
    window: previewFixture(table), rowsReturned: rows.length, durationMs: 18, requestSeq: command.requestSeq,
    truncated, warnings: [], sql: 'SELECT id, customer, region, revenue, ordered_at, fulfilled FROM orders LIMIT 75',
    error: state === 'error' ? 'The exploration fixture returned an example error.' : '',
  }
}

export function recordFixture(empty = false, tight = false): RecordTableSignal {
  const names = ['Daily orders', 'Customer segments', 'Revenue model', 'Inventory snapshot', 'Regional targets', 'Returns detail']
  const statuses = ['Succeeded', 'Running', 'Failed', 'Queued']
  return {
    columns: [
      { id: 'name', header: 'Asset', kind: 'entity', width: '240px', toggleable: false },
      { id: 'status', header: 'Status', kind: 'status' },
      { id: 'rows', header: 'Rows', kind: 'number', align: 'right' },
      { id: 'query', header: 'Query', kind: 'query' },
      { id: 'tags', header: 'Tags', kind: 'tags' },
      { id: 'actions', header: 'Actions', kind: 'actions' },
    ],
    rows: empty ? [] : names.map((name, index) => ({
      id: `record-${index}`, name: { label: name, description: `analytics.${name.toLowerCase().replaceAll(' ', '_')}`, icon: 'database' },
      status: { label: statuses[index % statuses.length], tone: ['success', 'accent', 'danger', 'muted'][index % statuses.length] },
      rows: { label: (1250 + index * 450).toLocaleString('en-US'), value: 1250 + index * 450 },
      query: { label: `SELECT * FROM ${name.toLowerCase().replaceAll(' ', '_')}`, expandedContent: `SELECT\n  *\nFROM analytics.${name.toLowerCase().replaceAll(' ', '_')}\nLIMIT 100;`, copyLabel: 'Copy query' },
      tags: [index % 2 ? 'Finance' : 'Commerce', 'Managed'],
      actions: [{ label: 'Inspect', action: 'inspect', icon: 'details' }, { label: 'Refresh', action: 'refresh', icon: 'refresh', disabled: index === 1 }],
    })),
    empty: 'No asset records to show.', minWidth: '860px', density: tight ? 'tight' : 'normal', rowAction: 'inspect',
    columnSelector: { enabled: true, label: 'Columns', storageKey: 'playground-record-columns', defaultColumns: ['name', 'status', 'rows', 'query', 'tags', 'actions'] },
  }
}

export const entityColumns: EntityListColumn[] = [
  { id: 'name', label: 'Dashboard', width: '40%' },
  { id: 'status', label: 'Status', render: 'status' },
  { id: 'owner', label: 'Owner', render: 'person' },
  { id: 'views', label: 'Views', align: 'right' },
  { id: 'actions', label: 'Actions', render: 'actions', sortable: false },
]

export function entityFixtures(): EntityListItem[] {
  return ['Revenue overview', 'Customer retention', 'Inventory health', 'Acquisition funnel', 'Cash flow', 'Order fulfillment'].map((title, index) => ({
    id: `dashboard-${index}`, title, description: `Weekly ${index % 2 ? 'finance' : 'commerce'} reporting`, icon: 'dashboard', iconTreatment: 'framed', iconColor: ['blue', 'purple', 'green'][index % 3],
    category: index % 2 ? 'finance' : 'commerce', group: index % 2 ? 'Finance' : 'Commerce',
    columns: { status: index === 1 ? 'Draft' : 'Published', owner: index % 2 ? 'Sam Rivera' : 'Alex Morgan', views: 320 + index * 170 },
    people: { owner: { name: index % 2 ? 'Sam Rivera' : 'Alex Morgan' } },
    favorite: index === 0, favoriteLabel: `Favorite ${title}`, pinned: false, pinLabel: `Pin ${title}`,
    actions: [{ label: 'Inspect', action: 'inspect', icon: 'details' }, { label: 'Refresh', action: 'refresh', icon: 'refresh', disabled: index === 1 }],
  }))
}
