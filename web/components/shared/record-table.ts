import { LitElement, html, svg, nothing, type TemplateResult } from 'lit'
import { property, state } from 'lit/decorators.js'
import {
  ArrowUpDown,
  BarChart3,
  Braces,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Clock3,
  Columns3,
  Copy,
  Database,
  ExternalLink,
  FileText,
  Filter,
  Gauge,
  KeyRound,
  Layers3,
  ListTree,
  RefreshCw,
  Server,
  Sigma,
  Table2,
  Waves,
  Waypoints,
  XCircle,
} from 'lucide'
import {
  TableController,
  createCoreRowModel,
  createSortedRowModel,
  rowSortingFeature,
  tableFeatures,
  type ColumnDef,
  type SortingState,
} from '@tanstack/lit-table'
import { lucideIcon } from './lucide-icons'
import './code-block'

type RecordCellTone = 'default' | 'accent' | 'success' | 'attention' | 'danger' | 'muted'
type RecordStatusIcon = 'check' | 'x' | 'clock' | 'dot'

type RecordCell = {
  label?: string
  value?: string | number
  description?: string
  href?: string
  icon?: string
  iconTreatment?: 'plain' | 'framed'
  tone?: RecordCellTone
  action?: string
  statusLabel?: string
  expandedContent?: string
  copyLabel?: string
}

type RecordAction = {
  label: string
  href?: string
  icon?: string
  action?: string
  disabled?: boolean
}

type RecordColumn = {
  id: string
  header: string
  kind?: 'text' | 'code' | 'expression' | 'badge' | 'status' | 'query' | 'number' | 'link' | 'tags' | 'entity' | 'button' | 'actions'
  align?: 'left' | 'right'
  hrefKey?: string
  width?: string
  sortable?: boolean
  toggleable?: boolean
}

type RecordColumnSelector = {
  enabled?: boolean
  storageKey?: string
  label?: string
  defaultColumns?: string[]
}

type RecordRow = Record<string, unknown>
type RecordTableDensity = 'normal' | 'tight'
type RecordTablePayload = {
  columns?: RecordColumn[]
  rows?: RecordRow[]
  empty?: string
  minWidth?: string
  columnSelector?: RecordColumnSelector
  density?: RecordTableDensity
  rowAction?: string
}
type NormalizedRecordTable = Omit<Required<RecordTablePayload>, 'columnSelector'> & {
  columnSelector: Required<RecordColumnSelector>
}

type RecordTableVariant = 'minimal' | 'primary' | 'compact'

const recordTableFeatures = tableFeatures({ rowSortingFeature, sortedRowModel: createSortedRowModel() })

const emptyRecordTable: NormalizedRecordTable = {
  columns: [],
  rows: [],
  empty: 'No rows to show.',
  minWidth: '0',
  columnSelector: { enabled: false, storageKey: '', label: 'Columns', defaultColumns: [] },
  density: 'normal',
  rowAction: '',
}

function cellLabel(value: unknown): string {
  if (value == null || value === '') return '-'
  if (typeof value === 'object' && 'label' in value) {
    const label = (value as RecordCell).label ?? (value as RecordCell).value
    return label == null || label === '' ? '-' : String(label)
  }
  return String(value)
}

function cellDescription(value: unknown): string {
  return typeof value === 'object' && value && 'description' in value ? String((value as RecordCell).description ?? '') : ''
}

function cellHref(column: RecordColumn, value: unknown, row: RecordRow): string {
  if (typeof value === 'object' && value && 'href' in value) return String((value as RecordCell).href ?? '')
  return column.hrefKey ? cellLabel(row[column.hrefKey]) : ''
}

function cellIcon(value: unknown): string {
  return typeof value === 'object' && value && 'icon' in value ? String((value as RecordCell).icon ?? '') : ''
}

function cellIconTreatment(value: unknown): 'plain' | 'framed' {
  if (typeof value === 'object' && value && 'iconTreatment' in value) {
    return (value as RecordCell).iconTreatment ?? 'framed'
  }
  return 'framed'
}

function cellTone(value: unknown): RecordCellTone {
  if (typeof value === 'object' && value && 'tone' in value) {
    return (value as RecordCell).tone ?? 'default'
  }
  return 'default'
}

function cellAction(value: unknown): string {
  return typeof value === 'object' && value && 'action' in value ? String((value as RecordCell).action ?? '') : ''
}

function statusIcon(value: unknown, label: string): RecordStatusIcon {
  if (typeof value === 'object' && value && 'icon' in value) {
    return ((value as RecordCell).icon as RecordStatusIcon | undefined) ?? 'dot'
  }
  switch (label.toLowerCase()) {
    case 'succeeded':
      return 'check'
    case 'failed':
      return 'x'
    case 'running':
    case 'queued':
      return 'clock'
    default:
      return 'dot'
  }
}

function sortPrimitive(value: unknown): string | number {
  if (typeof value === 'number') return value
  if (typeof value === 'object' && value && 'value' in value && typeof (value as RecordCell).value === 'number') {
    return (value as RecordCell).value as number
  }
  return cellLabel(value).toLowerCase()
}

function normalizeTable(table: RecordTablePayload): NormalizedRecordTable {
  return {
    columns: table.columns ?? [],
    rows: table.rows ?? [],
    empty: table.empty ?? emptyRecordTable.empty,
    minWidth: table.minWidth ?? emptyRecordTable.minWidth,
    columnSelector: {
      enabled: Boolean(table.columnSelector?.enabled),
      storageKey: table.columnSelector?.storageKey ?? '',
      label: table.columnSelector?.label ?? 'Columns',
      defaultColumns: table.columnSelector?.defaultColumns ?? [],
    },
    density: table.density ?? emptyRecordTable.density,
    rowAction: table.rowAction ?? emptyRecordTable.rowAction,
  }
}

function applyUpdater<T>(updater: unknown, current: T): T {
  return typeof updater === 'function' ? (updater as (old: T) => T)(current) : updater as T
}

function columnAlignClass(column: RecordColumn): string {
  return column.align === 'right' || column.kind === 'number' ? 'is-right' : ''
}

function columnWidth(column: RecordColumn): string {
  if (column.width) return column.width
  return column.kind === 'entity' ? 'clamp(220px, 45vw, 280px)' : ''
}

class RecordTable extends LitElement {
  @property({ attribute: false }) table: RecordTablePayload | null = null
  @property({ attribute: 'table' }) tableAttribute = ''
  @property() variant: RecordTableVariant = 'minimal'
  @state() private sorting: SortingState = []
  @state() private visibleColumnIDs: string[] = []
  @state() private columnSelectorOpen = false
  @state() private expandedRowIDs: string[] = []
  @state() private copiedRowID = ''

  private tableController = new TableController<typeof recordTableFeatures, RecordRow>(this)
  private columnVisibilityKey = ''
  private columnVisibilityFingerprint = ''

  createRenderRoot(): HTMLElement {
    return this
  }

  render() {
    const table = this.resolvedTable
    this.syncColumnVisibility(table)
    const columns = this.visibleColumns(table)
    const model = this.tanstackTable({ ...table, columns })
    const rows: RecordRow[] = model.getRowModel().rows.map((row: any) => row.original as RecordRow)

    if (table.rows.length === 0) {
      return html`
        <style>${recordTableStyles}</style>
        <div class="record-table-empty">${table.empty}</div>
      `
    }

    return html`
      <style>${recordTableStyles}</style>
      ${this.hasColumnSelector(table) ? html`<span class="record-table-corner-selector">${this.renderColumnSelector(table, columns)}</span>` : nothing}
      <div
        class=${`record-table-wrap variant-${this.variant} density-${table.density}`}
        role="region"
        aria-label="Scrollable table"
        tabindex="0"
      >
        <table class="record-table" style=${table.minWidth ? `min-width: ${table.minWidth}` : ''}>
          <thead>
            <tr>
              ${columns.map((column) => {
                const direction = this.sortDirection(column.id)
                const sortable = column.sortable !== false && column.kind !== 'actions'
                return html`
                  <th style=${columnWidth(column) ? `width: ${columnWidth(column)}` : ''} class=${columnAlignClass(column)}>
                    <span class="record-table-header-content">
                      <button
                        type="button"
                        class="record-table-sort"
                        aria-label=${`Sort by ${column.header || 'column'}`}
                        aria-sort=${direction === 'asc' ? 'ascending' : direction === 'desc' ? 'descending' : 'none'}
                        ?disabled=${!sortable}
                        @click=${() => sortable ? this.toggleSort(column.id) : undefined}
                      >
                        <span>${column.header}</span>
                        <span class=${direction ? 'record-table-sort-indicator is-active' : 'record-table-sort-indicator'} aria-hidden="true">${sortable ? this.sortIndicator(direction) : nothing}</span>
                      </button>
                    </span>
                  </th>
                `
              })}
            </tr>
          </thead>
          <tbody>
            ${rows.map((row, index) => this.renderRow(row, columns, table.rowAction, index))}
          </tbody>
        </table>
      </div>
      ${table.minWidth && table.minWidth !== '0' ? html`<p class="record-table-scroll-hint" aria-hidden="true">Swipe horizontally to see more columns <span aria-hidden="true">→</span></p>` : nothing}
    `
  }

  private get resolvedTable(): NormalizedRecordTable {
    if (this.table) return normalizeTable(this.table)
    if (this.tableAttribute) {
      try {
        return normalizeTable(JSON.parse(this.tableAttribute) as RecordTablePayload)
      } catch {
        // Datastar sets the property in normal operation. Attribute parsing is only a fallback.
      }
    }
    return emptyRecordTable
  }

  private renderColumnSelector(table: NormalizedRecordTable, visibleColumns: RecordColumn[]) {
    const toggleableColumns = table.columns.filter(isToggleableColumn)
    const visibleToggleableIDs = new Set(visibleColumns.filter(isToggleableColumn).map((column) => column.id))
    const label = table.columnSelector.label || 'Columns'
    return html`
      <details class="record-table-column-selector" .open=${this.columnSelectorOpen} @toggle=${this.handleColumnSelectorToggle}>
        <summary title=${label} aria-label=${label}>
          ${lucideIcon(Columns3, { size: 15, strokeWidth: 2 })}
        </summary>
        <div class="record-table-column-menu">
          ${toggleableColumns.map((column) => {
            const checked = visibleToggleableIDs.has(column.id)
            return html`
              <label>
                <input
                  type="checkbox"
                  aria-label=${column.header}
                  .checked=${checked}
                  ?disabled=${checked && visibleToggleableIDs.size <= 1}
                  @change=${(event: Event) => this.toggleColumn(table, column.id, (event.currentTarget as HTMLInputElement).checked)}
                >
                <span>${column.header}</span>
              </label>
            `
          })}
        </div>
      </details>
    `
  }

  private hasColumnSelector(table: NormalizedRecordTable): boolean {
    return table.columnSelector.enabled && table.columns.some(isToggleableColumn)
  }

  private syncColumnVisibility(table: NormalizedRecordTable): void {
    if (!table.columnSelector.enabled) {
      this.columnVisibilityKey = ''
      this.columnVisibilityFingerprint = ''
      if (this.visibleColumnIDs.length) this.visibleColumnIDs = []
      return
    }
    const fingerprint = table.columns.map((column) => `${column.id}:${isToggleableColumn(column) ? '1' : '0'}`).join('|')
    const storageKey = table.columnSelector.storageKey
    if (storageKey === this.columnVisibilityKey && fingerprint === this.columnVisibilityFingerprint) return
    this.columnVisibilityKey = storageKey
    this.columnVisibilityFingerprint = fingerprint
    this.visibleColumnIDs = this.initialVisibleColumnIDs(table)
  }

  private initialVisibleColumnIDs(table: NormalizedRecordTable): string[] {
    const toggleableIDs = table.columns.filter(isToggleableColumn).map((column) => column.id)
    const stored = this.storedVisibleColumnIDs(table.columnSelector.storageKey)
    const configured = stored.length ? stored : table.columnSelector.defaultColumns
    const sanitized = sanitizeVisibleColumnIDs(configured, toggleableIDs)
    return sanitized.length ? sanitized : toggleableIDs
  }

  private storedVisibleColumnIDs(storageKey: string): string[] {
    if (!storageKey) return []
    try {
      const parsed = JSON.parse(window.localStorage.getItem(storageKey) ?? '[]')
      return Array.isArray(parsed) ? parsed.map((value) => String(value)) : []
    } catch {
      return []
    }
  }

  private visibleColumns(table: NormalizedRecordTable): RecordColumn[] {
    if (!table.columnSelector.enabled) return table.columns
    const visible = new Set(this.visibleColumnIDs)
    return table.columns.filter((column) => !isToggleableColumn(column) || visible.has(column.id))
  }

  private toggleColumn(table: NormalizedRecordTable, columnID: string, checked: boolean): void {
    const toggleableIDs = table.columns.filter(isToggleableColumn).map((column) => column.id)
    const current = sanitizeVisibleColumnIDs(this.visibleColumnIDs, toggleableIDs)
    const currentSet = new Set(current.length ? current : toggleableIDs)
    if (checked) {
      currentSet.add(columnID)
    } else if (currentSet.size > 1) {
      currentSet.delete(columnID)
    }
    const next = toggleableIDs.filter((id) => currentSet.has(id))
    this.visibleColumnIDs = next
    if (table.columnSelector.storageKey) {
      window.localStorage.setItem(table.columnSelector.storageKey, JSON.stringify(next))
    }
  }

  private handleColumnSelectorToggle = (event: Event): void => {
    this.columnSelectorOpen = (event.currentTarget as HTMLDetailsElement).open
  }

  private tanstackTable(table: NormalizedRecordTable) {
    return this.tableController.table({
      features: recordTableFeatures,
      columns: this.columnDefs(table.columns),
      data: table.rows,
      getCoreRowModel: createCoreRowModel(),
      enableSorting: true,
      renderFallbackValue: '-',
      state: { sorting: this.sorting },
      onSortingChange: (updater: unknown) => {
        this.sorting = applyUpdater(updater, this.sorting)
      },
    } as any) as any
  }

  private columnDefs(columns: RecordColumn[]): Array<ColumnDef<typeof recordTableFeatures, RecordRow, unknown>> {
    return columns.map((column) => ({
      id: column.id,
      accessorFn: (row: RecordRow) => row[column.id],
      header: column.header,
      cell: (info: any) => this.renderCell(column, info.getValue(), info.row.original),
      enableSorting: column.sortable !== false && column.kind !== 'actions',
      sortingFn: (left: any, right: any, columnID: string) => {
        const leftValue = sortPrimitive(left.original[columnID])
        const rightValue = sortPrimitive(right.original[columnID])
        return typeof leftValue === 'number' && typeof rightValue === 'number'
          ? leftValue - rightValue
          : String(leftValue).localeCompare(String(rightValue), undefined, { numeric: true })
      },
      meta: { column },
    })) as Array<ColumnDef<typeof recordTableFeatures, RecordRow, unknown>>
  }

  private sortDirection(columnID: string): false | 'asc' | 'desc' {
    const sort = this.sorting.find((item) => item.id === columnID)
    if (!sort) return false
    return sort.desc ? 'desc' : 'asc'
  }

  private toggleSort(columnID: string): void {
    const direction = this.sortDirection(columnID)
    if (!direction) {
      this.sorting = [{ id: columnID, desc: false }]
      return
    }
    if (direction === 'asc') {
      this.sorting = [{ id: columnID, desc: true }]
      return
    }
    this.sorting = []
  }

  private renderCell(column: RecordColumn, value: unknown, row: RecordRow): TemplateResult | string {
    const label = cellLabel(value)
    switch (column.kind) {
      case 'code':
        return label === '-' ? html`<span class="record-muted">-</span>` : html`<code class="record-code">${label}</code>`
      case 'expression':
        return label === '-' ? html`<span class="record-muted">-</span>` : html`<code class="record-expression">${label}</code>`
      case 'badge':
        return label === '-' ? html`<span class="record-muted">-</span>` : html`<span class=${`record-badge record-badge-${cellTone(value)}`}>${label}</span>`
      case 'status':
        return label === '-' ? html`<span class="record-muted">-</span>` : this.renderStatusCell(value, label)
      case 'query':
        return this.renderQueryCell(value, row)
      case 'number':
        return label === '-' ? html`<span class="record-muted">-</span>` : html`<span class="record-number">${label}</span>`
      case 'link':
        return this.renderLink(column, value, row)
      case 'tags':
        return Array.isArray(value) && value.length > 0
          ? html`<span class="record-tags">${value.map((tag) => html`<span>${String(tag)}</span>`)}</span>`
          : html`<span class="record-muted">-</span>`
      case 'entity':
        return this.renderEntity(column, value, row)
      case 'button':
        return this.renderButton(column, value, row)
      case 'actions':
        return this.renderActions(value, row)
      default:
        return label === '-' ? html`<span class="record-muted">-</span>` : html`<span>${label}</span>`
    }
  }

  private renderRow(row: RecordRow, columns: RecordColumn[], rowAction: string, index: number): TemplateResult {
    const rowID = this.rowID(row, index)
    const expandedContent = this.rowExpandedContent(row, columns)
    const expanded = Boolean(expandedContent) && this.expandedRowIDs.includes(rowID)
    const actionable = Boolean(rowAction)
    return html`
      <tr
        class=${[
          'record-row',
          expanded ? 'is-expanded' : '',
          actionable ? 'is-actionable' : '',
        ].filter(Boolean).join(' ')}
        tabindex=${actionable ? '0' : nothing}
        aria-label=${actionable ? this.rowAriaLabel(row, columns) : nothing}
        @click=${() => this.emitRowAction(rowAction, row)}
        @keydown=${(event: KeyboardEvent) => this.handleRowKeydown(event, rowAction, row)}
      >
        ${columns.map((column) => html`
          <td class=${columnAlignClass(column)}>
            ${this.renderCell(column, row[column.id], row)}
          </td>
        `)}
      </tr>
      ${expanded && expandedContent ? html`
        <tr class="record-query-expanded-row">
          <td class="record-query-expanded-cell" colspan=${columns.length}>
            <div class="record-query-expanded">
              <lv-code-block language="sql" format dense .code=${expandedContent}></lv-code-block>
              <button
                type="button"
                class="record-query-copy"
                @click=${(event: Event) => {
                  event.stopPropagation()
                  this.copyExpandedContent(rowID, expandedContent)
                }}
              >
                ${lucideIcon(Copy, { size: 14, strokeWidth: 2 })}
                <span>${this.copiedRowID === rowID ? 'Copied' : this.rowCopyLabel(row, columns)}</span>
              </button>
            </div>
          </td>
        </tr>
      ` : nothing}
    `
  }

  private renderLink(column: RecordColumn, value: unknown, row: RecordRow) {
    const href = cellHref(column, value, row)
    const label = cellLabel(value)
    if (label === '-') return html`<span class="record-muted">-</span>`
    return href && href !== '-' ? html`<a class="record-link" href=${href} @click=${(event: Event) => event.stopPropagation()}>${label}</a>` : html`<span>${label}</span>`
  }

  private renderEntity(column: RecordColumn, value: unknown, row: RecordRow) {
    const href = cellHref(column, value, row)
    const label = cellLabel(value)
    const description = cellDescription(value)
    const icon = cellIcon(value)
    const iconTreatment = cellIconTreatment(value)
    const content = html`
      <span class=${icon ? 'record-entity' : 'record-entity record-entity-no-icon'}>
        ${icon ? this.renderIcon(icon, `record-entity-icon is-${iconTreatment} record-icon-${iconToken(icon)}`) : nothing}
        <span class="record-entity-copy">
          <span class="record-entity-label">${label}</span>
          ${description ? html`<span class="record-entity-description">${description}</span>` : nothing}
        </span>
      </span>
    `
    return href && href !== '-' ? html`<a class="record-entity-link" href=${href}>${content}</a>` : content
  }

  private renderButton(column: RecordColumn, value: unknown, row: RecordRow) {
    const label = cellLabel(value)
    const action = cellAction(value)
    return html`
      <button
        type="button"
        class="record-button-cell"
        @click=${(event: Event) => {
          event.stopPropagation()
          this.emitAction(action || column.id, row)
        }}
      >
        ${this.renderIcon(cellIcon(value), 'record-button-icon')}
        <span>${label}</span>
      </button>
    `
  }

  private renderQueryCell(value: unknown, row: RecordRow) {
    const label = cellLabel(value)
    const content = this.expandedContent(value)
    const rowID = this.rowID(row)
    const expanded = this.expandedRowIDs.includes(rowID)
    const statusLabel = queryStatusLabel(value)
    const tone = cellTone(value)
    const icon = statusIcon(value, statusLabel)
    return html`
      <span class="record-query">
        <span class=${`record-query-status record-status-${tone}`} title=${statusLabel} aria-label=${statusLabel}>
          <span aria-hidden="true">${this.renderStatusIcon(icon)}</span>
        </span>
        ${content ? html`
          <button
            type="button"
            class="record-query-expand"
            aria-label=${expanded ? 'Collapse query text' : 'Expand query text'}
            aria-expanded=${expanded ? 'true' : 'false'}
            @click=${(event: Event) => {
              event.stopPropagation()
              this.toggleExpanded(rowID)
            }}
          >
            ${lucideIcon(expanded ? ChevronDown : ChevronRight, { size: 16, strokeWidth: 2 })}
          </button>
        ` : html`<span class="record-query-expand-spacer" aria-hidden="true"></span>`}
        <code class="record-query-text" title=${label === '-' ? '' : label}>${label}</code>
      </span>
    `
  }

  private renderActions(value: unknown, row: RecordRow) {
    const actions = Array.isArray(value) ? value as RecordAction[] : []
    return html`
      <span class="record-actions">
        ${actions.map((action) => action.href
          ? html`
            <a
              class="record-icon-action"
              href=${action.href}
              title=${action.label}
              aria-label=${action.label}
              @click=${(event: Event) => event.stopPropagation()}
            >
              ${this.renderIcon(action.icon || 'external', '')}
            </a>
          `
          : html`
            <button
              type="button"
              class="record-icon-action"
              title=${action.label}
              aria-label=${action.label}
              ?disabled=${action.disabled}
              @click=${(event: Event) => {
                event.stopPropagation()
                this.emitAction(action.action || action.label, row)
              }}
            >
              ${this.renderIcon(action.icon || 'external', '')}
            </button>
          `)}
      </span>
    `
  }

  private renderStatusCell(value: unknown, label: string) {
    const tone = cellTone(value)
    const icon = statusIcon(value, label)
    return html`
      <span class=${`record-status record-status-${tone}`}>
        <span class="record-status-icon" aria-hidden="true">${this.renderStatusIcon(icon)}</span>
        <span>${label}</span>
      </span>
    `
  }

  private renderStatusIcon(icon: RecordStatusIcon) {
    switch (icon) {
      case 'check':
        return lucideIcon(CheckCircle2, { size: 16, strokeWidth: 2 })
      case 'x':
        return lucideIcon(XCircle, { size: 16, strokeWidth: 2 })
      case 'clock':
        return lucideIcon(Clock3, { size: 16, strokeWidth: 2 })
      default:
        return svg`<svg viewBox="0 0 16 16" focusable="false"><circle cx="8" cy="8" r="4" fill="currentColor"></circle></svg>`
    }
  }

  private rowID(row: RecordRow, index = -1): string {
    const id = row.id
    if (id != null && id !== '') return String(id)
    return index >= 0 ? `row-${index}` : JSON.stringify(row)
  }

  private rowAriaLabel(row: RecordRow, columns: RecordColumn[]): string {
    const queryColumn = columns.find((column) => column.kind === 'query')
    if (queryColumn) return `Open query details for ${cellLabel(row[queryColumn.id])}`
    const firstColumn = columns.find((column) => column.kind !== 'actions') ?? columns[0]
    return `Open row details for ${firstColumn ? cellLabel(row[firstColumn.id]) : this.rowID(row)}`
  }

  private rowExpandedContent(row: RecordRow, columns: RecordColumn[]): string {
    for (const column of columns) {
      if (column.kind !== 'query') continue
      const content = this.expandedContent(row[column.id])
      if (content) return content
    }
    return ''
  }

  private rowCopyLabel(row: RecordRow, columns: RecordColumn[]): string {
    for (const column of columns) {
      if (column.kind !== 'query') continue
      const value = row[column.id]
      if (typeof value === 'object' && value && 'copyLabel' in value) {
        return String((value as RecordCell).copyLabel || 'Copy query')
      }
    }
    return 'Copy query'
  }

  private expandedContent(value: unknown): string {
    if (typeof value === 'object' && value && 'expandedContent' in value) {
      return String((value as RecordCell).expandedContent ?? '')
    }
    return ''
  }

  private toggleExpanded(rowID: string): void {
    const current = new Set(this.expandedRowIDs)
    if (current.has(rowID)) {
      current.delete(rowID)
    } else {
      current.add(rowID)
    }
    this.expandedRowIDs = Array.from(current)
    this.copiedRowID = ''
  }

  private async copyExpandedContent(rowID: string, content: string): Promise<void> {
    try {
      await navigator.clipboard?.writeText(content)
      this.copiedRowID = rowID
    } catch {
      this.copiedRowID = ''
    }
  }

  private handleRowKeydown(event: KeyboardEvent, action: string, row: RecordRow): void {
    if (!action) return
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    this.emitAction(action, row)
  }

  private emitRowAction(action: string, row: RecordRow): void {
    if (!action) return
    this.emitAction(action, row)
  }

  private renderIcon(name: string, className: string): TemplateResult {
    const icon = iconForName(name)
    return html`<span class=${className} aria-hidden="true">${lucideIcon(icon, { size: 16, strokeWidth: 1.75 })}</span>`
  }

  private emitAction(action: string, row: RecordRow): void {
    this.dispatchEvent(new CustomEvent('lv-record-table-action', {
      bubbles: true,
      composed: true,
      detail: { action, row },
    }))
  }

  private sortIndicator(direction: false | 'asc' | 'desc'): TemplateResult {
    if (direction === 'asc') return html`<span>↑</span>`
    if (direction === 'desc') return html`<span>↓</span>`
    return lucideIcon(ArrowUpDown, { size: 12, strokeWidth: 2 })
  }
}

function isToggleableColumn(column: RecordColumn): boolean {
  if (column.toggleable != null) return column.toggleable
  return column.kind !== 'actions'
}

function queryStatusLabel(value: unknown): string {
  if (typeof value === 'object' && value && 'statusLabel' in value) {
    const status = String((value as RecordCell).statusLabel ?? '').trim()
    if (status) return status
  }
  const label = cellLabel(value)
  return label === '-' ? 'unknown' : label
}

function sanitizeVisibleColumnIDs(values: string[], allowedIDs: string[]): string[] {
  const allowed = new Set(allowedIDs)
  return values.filter((value, index, all) => allowed.has(value) && all.indexOf(value) === index)
}

function iconForName(name: string): any {
  switch (name) {
    case 'catalog':
    case 'connection':
    case 'database':
      return Database
    case 'dashboard':
      return BarChart3
    case 'model_table':
    case 'table':
      return Table2
    case 'semantic_model':
      return Waypoints
    case 'source':
    case 'schema':
      return Server
    case 'field':
      return KeyRound
    case 'metric':
      return Sigma
    case 'filter':
      return Filter
    case 'visual':
      return Gauge
    case 'page':
      return FileText
    case 'relationship':
    case 'lineage':
      return ListTree
    case 'view':
      return Waves
    case 'code':
      return Braces
    case 'open':
    case 'external':
      return ExternalLink
    case 'details':
      return FileText
    case 'refresh':
    case 'retry':
      return RefreshCw
    case 'cancel':
      return XCircle
    default:
      return Layers3
  }
}

function iconToken(name: string): string {
  return String(name || 'default').toLowerCase().replace(/[^a-z0-9_-]/g, '-')
}

const recordTableStyles = `
  lv-record-table {
    display: block;
    position: relative;
    min-width: 0;
    max-width: 100%;
  }

  lv-record-table .record-table-wrap {
    width: 100%;
    min-width: 0;
    max-width: 100%;
    overflow-x: auto;
    scrollbar-width: thin;
    -webkit-overflow-scrolling: touch;
  }

  lv-record-table .record-table-wrap:focus-visible {
    outline: 2px solid var(--lv-fg-accent, currentColor);
    outline-offset: 2px;
  }

  lv-record-table .record-table-scroll-hint {
    display: none;
    margin: var(--base-size-4) var(--base-size-8) 0;
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-align: right;
  }

  lv-record-table .record-table-column-selector {
    position: relative;
    flex: none;
  }

  lv-record-table .record-table-corner-selector {
    position: absolute;
    z-index: 5;
    top: var(--base-size-6);
    right: var(--base-size-8);
    display: inline-flex;
  }

  lv-record-table .record-table-column-selector summary {
    display: inline-flex;
    width: var(--control-medium-size, 32px);
    height: var(--control-medium-size, 32px);
    align-items: center;
    justify-content: center;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-muted);
    cursor: pointer;
    list-style: none;
  }

  lv-record-table .record-table-column-selector summary::-webkit-details-marker {
    display: none;
  }

  lv-record-table .record-table-column-selector summary:hover {
    background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
  }

  lv-record-table .record-table-column-menu {
    position: absolute;
    z-index: 10;
    top: calc(100% + var(--base-size-4));
    right: 0;
    display: grid;
    min-width: 13rem;
    gap: var(--base-size-4);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    box-shadow: var(--lv-shadow-floating, 0 8px 24px rgba(31, 35, 40, 0.12));
    padding: var(--base-size-8);
  }

  lv-record-table .record-table-column-menu label {
    display: grid;
    grid-template-columns: 1rem minmax(0, 1fr);
    gap: var(--base-size-8);
    align-items: center;
    color: var(--lv-fg-default);
    font: var(--lv-type-body-compact);
  }

  lv-record-table .record-table-column-menu input {
    margin: 0;
  }

  lv-record-table .record-table-wrap.variant-primary,
  lv-record-table .record-table-wrap.variant-compact {
    background: var(--lv-bg-page);
  }

  lv-record-table .record-table {
    width: 100%;
    border-collapse: collapse;
    table-layout: fixed;
  }

  @media (max-width: 640px) {
    lv-record-table .record-table-scroll-hint {
      display: block;
    }

  }

  lv-record-table .record-table th,
  lv-record-table .record-table td {
    box-sizing: border-box;
    border-bottom: 0;
    padding: var(--base-size-8);
    text-align: left;
    vertical-align: top;
  }

  lv-record-table .record-table th {
    position: sticky;
    top: 0;
    z-index: 1;
    background: var(--lv-bg-page);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    letter-spacing: 0;
    text-transform: none;
  }

  lv-record-table .variant-primary .record-table th,
  lv-record-table .variant-compact .record-table th {
    padding: var(--base-size-8) var(--base-size-12);
    background: var(--lv-bg-page);
    font: var(--lv-type-caption);
  }

  lv-record-table .record-table td {
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  lv-record-table .variant-primary .record-table td {
    padding: var(--base-size-6) var(--base-size-12);
    font: var(--lv-type-body);
    vertical-align: middle;
  }

  lv-record-table .variant-compact .record-table td {
    padding: var(--base-size-8) var(--base-size-12);
    font: var(--lv-type-body);
    vertical-align: middle;
  }

  lv-record-table .density-tight .record-table th {
    padding: var(--base-size-6) var(--base-size-8);
  }

  lv-record-table .density-tight .record-table td {
    padding: var(--base-size-4) var(--base-size-8);
  }

  lv-record-table .variant-primary .record-table tbody tr {
    min-height: 3rem;
  }

  lv-record-table .record-table th.is-right,
  lv-record-table .record-table td.is-right {
    text-align: right;
  }

  lv-record-table .record-table tbody tr:last-child td {
    border-bottom: 0;
  }

  lv-record-table .record-table tbody tr {
    transition: background-color var(--motion-transition-hover);
  }

  lv-record-table .record-table tbody tr:hover {
    background: var(--lv-bg-hover, var(--lv-bg-panel-muted));
  }

  lv-record-table .record-table tbody tr.is-actionable {
    cursor: pointer;
  }

  lv-record-table .record-table tbody tr.is-actionable:focus-visible {
    outline: 2px solid var(--lv-fg-link);
    outline-offset: -2px;
  }

  lv-record-table .variant-primary .record-table tbody tr:hover,
  lv-record-table .variant-compact .record-table tbody tr:hover {
    background: var(--control-transparent-bgColor-hover);
  }

  lv-record-table .record-table-sort {
    display: inline-flex;
    width: 100%;
    min-width: 0;
    align-items: center;
    justify-content: space-between;
    gap: var(--base-size-6);
    border: 0;
    background: transparent;
    color: inherit;
    cursor: pointer;
    padding: 0;
    font: inherit;
    letter-spacing: inherit;
    text-align: inherit;
    text-transform: inherit;
  }

  lv-record-table .record-table-header-content {
    display: flex;
    min-width: 0;
    align-items: center;
    justify-content: space-between;
    gap: var(--base-size-6);
  }

  lv-record-table .record-table-header-content .record-table-sort {
    flex: 1 1 auto;
    min-width: 0;
  }

  lv-record-table .record-table-sort:hover,
  lv-record-table .record-table-sort:focus-visible {
    color: var(--lv-fg-default);
    outline: 0;
  }

  lv-record-table .record-table-sort-indicator {
    display: inline-flex;
    min-width: var(--base-size-16);
    justify-content: flex-end;
    color: var(--lv-fg-muted);
    opacity: 0;
  }

  lv-record-table .record-table-sort:hover .record-table-sort-indicator,
  lv-record-table .record-table-sort:focus-visible .record-table-sort-indicator,
  lv-record-table .record-table-sort-indicator.is-active {
    opacity: 1;
  }

  lv-record-table .record-code,
  lv-record-table .record-expression {
    font-family: var(--fontStack-monospace);
  }

  lv-record-table .record-code {
    display: inline-flex;
    max-width: 100%;
    overflow: hidden;
    color: var(--lv-fg-default);
    padding: 0;
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body);
  }

  lv-record-table .variant-compact .record-code,
  lv-record-table .variant-compact .record-expression {
    font: var(--lv-type-body);
  }

  lv-record-table .record-expression {
    display: block;
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body);
  }

  lv-record-table .record-badge {
    display: inline-flex;
    min-height: var(--base-size-20);
    align-items: center;
    gap: var(--base-size-4);
    border-radius: var(--borderRadius-full, var(--lv-radius-full));
    padding: 0 var(--base-size-8);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
  }

  lv-record-table .record-badge-success {
    border: var(--borderWidth-default) solid var(--lv-line-success-muted, var(--lv-line-muted));
    background: var(--lv-bg-success-muted, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
  }

  lv-record-table .record-badge-accent {
    border: var(--borderWidth-default) solid var(--lv-line-accent-muted, var(--lv-line-muted));
    background: var(--lv-bg-accent-muted, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
  }

  lv-record-table .record-badge-attention {
    border: var(--borderWidth-default) solid var(--lv-line-warning-muted, var(--lv-line-muted));
    background: var(--lv-bg-warning-muted, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
  }

  lv-record-table .record-badge-muted,
  lv-record-table .record-badge-default {
    border: var(--lv-border-muted);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-muted);
  }

  lv-record-table .record-status {
    display: inline-flex;
    align-items: center;
    gap: var(--base-size-6);
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-medium);
    white-space: nowrap;
  }

  lv-record-table .record-status-icon {
    display: inline-flex;
    width: var(--base-size-16);
    height: var(--base-size-16);
    flex: none;
    align-items: center;
    justify-content: center;
    color: var(--lv-fg-muted);
  }

  lv-record-table .record-query {
    display: grid;
    width: 100%;
    min-width: 0;
    grid-template-columns: var(--base-size-16) 1.5rem minmax(0, 1fr);
    align-items: center;
    gap: var(--base-size-6);
  }

  lv-record-table .record-query-status {
    display: inline-flex;
    width: var(--base-size-16);
    height: var(--base-size-16);
    align-items: center;
    justify-content: center;
    color: var(--lv-fg-muted);
  }

  lv-record-table .record-query-status svg {
    display: block;
    width: var(--base-size-16);
    height: var(--base-size-16);
  }

  lv-record-table .record-query-expand,
  lv-record-table .record-query-expand-spacer {
    display: inline-flex;
    width: 1.5rem;
    height: 1.5rem;
    align-items: center;
    justify-content: center;
  }

  lv-record-table .record-query-expand {
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: var(--lv-fg-muted);
    cursor: pointer;
    padding: 0;
  }

  lv-record-table .record-query-expand:hover,
  lv-record-table .record-query-expand:focus-visible {
    background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
    outline: 0;
  }

  lv-record-table .record-query-text {
    display: block;
    min-width: 0;
    overflow: hidden;
    color: var(--lv-fg-link, var(--lv-fg-default));
    font: var(--lv-type-code-inline);
    line-height: var(--base-text-lineHeight-tight);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  lv-record-table .record-query-expanded-row:hover {
    background: transparent;
  }

  lv-record-table .record-query-expanded-cell {
    padding: 0 !important;
    background: var(--lv-bg-panel);
  }

  lv-record-table .record-query-expanded {
    display: grid;
    gap: var(--base-size-8);
    border-top: var(--borderWidth-default, 1px) solid color-mix(in srgb, var(--lv-line-muted), transparent 35%);
    padding: var(--base-size-12);
  }

  lv-record-table .record-query-expanded > pre {
    max-height: 18rem;
    min-width: 0;
    overflow: auto;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-default);
    margin: 0;
    padding: var(--base-size-12);
  }

  lv-record-table .record-query-expanded > pre code {
    font: var(--lv-type-code-block);
    white-space: pre;
  }

  lv-record-table .record-query-copy {
    justify-self: start;
    display: inline-flex;
    min-height: var(--control-medium-size, 32px);
    align-items: center;
    gap: var(--base-size-6);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    cursor: pointer;
    padding: 0 var(--base-size-12);
    font: var(--lv-type-body);
  }

  lv-record-table .record-query-copy:hover,
  lv-record-table .record-query-copy:focus-visible {
    background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    outline: 0;
  }

  lv-record-table .record-status-icon svg,
  lv-record-table .record-entity-icon svg,
  lv-record-table .record-button-icon svg,
  lv-record-table .record-icon-action svg {
    display: block;
    width: var(--base-size-16);
    height: var(--base-size-16);
  }

  lv-record-table .record-status-success .record-status-icon {
    color: var(--lv-fg-success);
  }

  lv-record-table .record-query-status.record-status-success {
    color: var(--lv-fg-success);
  }

  lv-record-table .record-status-danger .record-status-icon {
    color: var(--lv-fg-danger);
  }

  lv-record-table .record-query-status.record-status-danger {
    color: var(--lv-fg-danger);
  }

  lv-record-table .record-status-attention .record-status-icon {
    color: var(--lv-fg-warning);
  }

  lv-record-table .record-query-status.record-status-attention {
    color: var(--lv-fg-warning);
  }

  lv-record-table .record-status-accent .record-status-icon {
    color: var(--lv-fg-link);
  }

  lv-record-table .record-query-status.record-status-accent {
    color: var(--lv-fg-link);
  }

  lv-record-table .record-number {
    font-variant-numeric: tabular-nums;
  }

  lv-record-table .record-link,
  lv-record-table .record-entity-link {
    color: var(--lv-fg-link);
    text-decoration: none;
  }

  lv-record-table .record-entity-link {
    display: grid;
    width: 100%;
    max-width: 100%;
  }

  lv-record-table .record-link:hover,
  lv-record-table .record-link:focus-visible,
  lv-record-table .record-entity-link:hover .record-entity-label,
  lv-record-table .record-entity-link:focus-visible .record-entity-label {
    text-decoration: underline;
    outline: 0;
  }

  lv-record-table .record-tags {
    display: flex;
    flex-wrap: wrap;
    gap: var(--base-size-4);
  }

  lv-record-table .record-tags span {
    display: inline-flex;
    min-height: var(--base-size-20);
    align-items: center;
    border: var(--lv-border-muted);
    border-radius: var(--borderRadius-full, var(--lv-radius-full));
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-muted);
    padding: 0 var(--base-size-8);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  lv-record-table .record-entity,
  lv-record-table .record-button-cell {
    display: grid;
    width: 100%;
    max-width: 100%;
    grid-template-columns: auto minmax(0, 1fr);
    align-items: start;
    gap: var(--base-size-8);
  }

  lv-record-table .record-entity-no-icon {
    grid-template-columns: minmax(0, 1fr);
    gap: 0;
  }

  lv-record-table .variant-primary .record-entity {
    align-items: center;
  }

  lv-record-table .record-entity-icon,
  lv-record-table .record-button-icon {
    display: inline-flex;
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    align-items: center;
    justify-content: center;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-muted);
  }

  lv-record-table .variant-primary .record-entity-icon {
    background: var(--lv-bg-control, var(--lv-bg-panel-muted));
  }

  lv-record-table .variant-primary .record-icon-dashboard {
    border-color: var(--lv-asset-dashboard-border, var(--lv-line-muted));
    background: var(--lv-asset-dashboard-bg, var(--lv-bg-panel-muted));
    color: var(--lv-asset-dashboard-accent, var(--lv-fg-muted));
  }

  lv-record-table .variant-primary .record-icon-semantic_model {
    border-color: var(--lv-asset-semantic-model-border, var(--lv-line-muted));
    background: var(--lv-asset-semantic-model-bg, var(--lv-bg-panel-muted));
    color: var(--lv-asset-semantic-model-accent, var(--lv-fg-muted));
  }

  lv-record-table .variant-primary .record-icon-model_table {
    border-color: var(--lv-asset-model-table-border, var(--lv-line-muted));
    background: var(--lv-asset-model-table-bg, var(--lv-bg-panel-muted));
    color: var(--lv-asset-model-table-accent, var(--lv-fg-muted));
  }

  lv-record-table .variant-primary .record-icon-source {
    border-color: var(--lv-asset-source-border, var(--lv-line-muted));
    background: var(--lv-asset-source-bg, var(--lv-bg-panel-muted));
    color: var(--lv-asset-source-accent, var(--lv-fg-muted));
  }

  lv-record-table .record-entity-icon.is-plain,
  lv-record-table .variant-primary .record-entity-icon.is-plain {
    border: 0;
    background: transparent;
  }

  lv-record-table .record-entity-copy {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  lv-record-table .record-entity-label {
    display: block;
    max-width: 100%;
    overflow-wrap: anywhere;
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-semibold);
    white-space: normal;
  }

  lv-record-table .variant-primary .record-entity-label {
    overflow: hidden;
    overflow-wrap: normal;
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-semibold);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  lv-record-table .variant-primary .record-entity-description {
    overflow: hidden;
    overflow-wrap: normal;
    line-height: var(--base-text-lineHeight-tight);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  lv-record-table .record-entity-description {
    display: block;
    max-width: 100%;
    overflow-wrap: anywhere;
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
    white-space: normal;
  }

  lv-record-table .record-button-cell {
    border: 0;
    background: transparent;
    color: var(--lv-fg-default);
    cursor: pointer;
    padding: 0;
    font: inherit;
    text-align: left;
  }

  lv-record-table .record-button-cell:hover span:last-child,
  lv-record-table .record-button-cell:focus-visible span:last-child {
    color: var(--lv-fg-link);
    text-decoration: underline;
  }

  lv-record-table .record-actions {
    display: inline-flex;
    justify-content: flex-end;
    gap: var(--base-size-6);
  }

  lv-record-table .record-icon-action {
    display: inline-flex;
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    align-items: center;
    justify-content: center;
    border: var(--lv-border-transparent);
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: var(--lv-fg-muted);
    text-decoration: none;
    cursor: pointer;
  }

  lv-record-table .density-tight .record-icon-action {
    width: 1.5rem;
    height: 1.5rem;
  }

  lv-record-table .record-icon-action:hover,
  lv-record-table .record-icon-action:focus-visible {
    border-color: var(--lv-line-muted);
    background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
    outline: 0;
  }

  lv-record-table .record-muted,
  lv-record-table .record-table-empty {
    color: var(--lv-fg-muted);
  }

  lv-record-table .record-table-empty {
    padding: var(--base-size-20) 0;
    font: var(--lv-type-body);
  }
`

customElements.define('lv-record-table', RecordTable)
