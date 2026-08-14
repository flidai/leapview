import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { createRef, ref, type Ref } from 'lit/directives/ref.js'
import { Columns3 } from 'lucide'
import { type ColumnResizeDrag, resizeClientX, resizeGuideX, resizePlaneScaleX, resizedColumnWidth } from './column-resize'
import { lucideIcon } from './lucide-icons'
import { virtualRowRange } from './table-window'

export type WindowedTableBlockID = 'a' | 'b' | 'c'
export type WindowedTableSort = {
  key?: string
  column?: string
  direction?: 'asc' | 'desc' | ''
}

export type WindowedTableColumn = {
  key: string
  label: string
  type?: string
  align?: 'left' | 'right'
  width?: number
  minWidth?: number
  sortable?: boolean
}

export type WindowedTableBlock = {
  start: number
  requestSeq: number
  resetVersion: number
  sort: WindowedTableSort
  rows: Record<string, unknown>[]
}

export type WindowedTablePayload = {
  tableKey?: string
  title?: string
  columns?: WindowedTableColumn[]
  totalRows?: number
  availableRows?: number
  chunkSize?: number
  rowHeight?: number
  resetVersion?: number
  sort?: WindowedTableSort
  blocks?: Partial<Record<WindowedTableBlockID, WindowedTableBlock>>
  loadingBlock?: string
  error?: string
  visibleColumns?: string[]
  columnWidths?: Record<string, number>
  totalLabel?: string
}

export type WindowedTableRequest = {
  block: WindowedTableBlockID | 'all'
  start: number
  count: number
  requestSeq: number
  sort: WindowedTableSort
  resetVersion: number
}

type VisibleRowSlot = { index: number; row?: Record<string, unknown> }
type ExpectedBlockRequest = {
  start: number
  requestSeq: number
  resetVersion: number
  sort: WindowedTableSort
}

const blockIDs: WindowedTableBlockID[] = ['a', 'b', 'c']
const defaultSort: WindowedTableSort = { key: '', direction: '' }
const defaultChunkSize = 100
const defaultRowHeight = 32

function emptyBlock(start: number, sort = defaultSort, resetVersion = 0): WindowedTableBlock {
  return { start, requestSeq: 0, resetVersion, sort, rows: [] }
}

function emptyBlocks(chunkSize = defaultChunkSize, sort = defaultSort, resetVersion = 0): Record<WindowedTableBlockID, WindowedTableBlock> {
  return {
    a: emptyBlock(0, sort, resetVersion),
    b: emptyBlock(chunkSize, sort, resetVersion),
    c: emptyBlock(chunkSize * 2, sort, resetVersion),
  }
}

const emptyTable: Required<WindowedTablePayload> = {
  tableKey: '',
  title: '',
  columns: [],
  totalRows: 0,
  availableRows: 0,
  chunkSize: defaultChunkSize,
  rowHeight: defaultRowHeight,
  resetVersion: 0,
  sort: defaultSort,
  blocks: emptyBlocks(),
  loadingBlock: '',
  error: '',
  visibleColumns: [],
  columnWidths: {},
  totalLabel: '',
}

function normalizeTable(value: WindowedTablePayload | null | undefined): Required<WindowedTablePayload> {
  const chunkSize = positiveNumber(value?.chunkSize, defaultChunkSize)
  const sort = normalizeSort(value?.sort)
  const resetVersion = positiveNumber(value?.resetVersion, 0)
  const totalRows = positiveNumber(value?.totalRows, 0)
  return {
    ...emptyTable,
    ...value,
    tableKey: String(value?.tableKey ?? ''),
    columns: Array.isArray(value?.columns) ? value.columns : [],
    totalRows,
    availableRows: positiveNumber(value?.availableRows, totalRows),
    chunkSize,
    rowHeight: positiveNumber(value?.rowHeight, defaultRowHeight),
    resetVersion,
    sort,
    blocks: {
      a: normalizeBlock(value?.blocks?.a, 0, sort, resetVersion),
      b: normalizeBlock(value?.blocks?.b, chunkSize, sort, resetVersion),
      c: normalizeBlock(value?.blocks?.c, chunkSize * 2, sort, resetVersion),
    },
    loadingBlock: value?.loadingBlock ?? '',
    error: value?.error ?? '',
    visibleColumns: Array.isArray(value?.visibleColumns) ? value.visibleColumns : [],
    columnWidths: isRecord(value?.columnWidths) ? value.columnWidths : {},
    totalLabel: value?.totalLabel ?? (totalRows ? String(totalRows) : ''),
  }
}

function normalizeBlock(block: WindowedTableBlock | undefined, fallbackStart: number, sort: WindowedTableSort, resetVersion: number): WindowedTableBlock {
  return {
    start: positiveNumber(block?.start, fallbackStart),
    requestSeq: positiveNumber(block?.requestSeq, 0),
    resetVersion: positiveNumber(block?.resetVersion, resetVersion),
    sort: normalizeSort(block?.sort ?? sort),
    rows: Array.isArray(block?.rows) ? block.rows : [],
  }
}

function positiveNumber(value: unknown, fallback: number): number {
  const next = Number(value)
  return Number.isFinite(next) && next >= 0 ? next : fallback
}

function isRecord(value: unknown): value is Record<string, number> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function normalizeSort(sort: WindowedTableSort | undefined): WindowedTableSort {
  const key = String(sort?.key ?? sort?.column ?? '').trim()
  const direction = sort?.direction === 'asc' || sort?.direction === 'desc' ? sort.direction : ''
  return key && direction ? { key, column: key, direction } : defaultSort
}

function sameSort(a: WindowedTableSort, b: WindowedTableSort): boolean {
  const left = normalizeSort(a)
  const right = normalizeSort(b)
  return (left.key ?? left.column ?? '') === (right.key ?? right.column ?? '') && left.direction === right.direction
}

class WindowedTable extends LitElement {
  @property({ attribute: false }) table: WindowedTablePayload | null = null
  @property({ type: Boolean, reflect: true }) compact = false
  @state() private viewportTop = 0
  @state() private localVisibleColumns: string[] = []
  @state() private localColumnWidths: Record<string, number> = {}
  @state() private resizeGuide = -1
  private viewportHeight = 0
  private viewportWidth = 0
  private lastResetVersion = -1
  private lastTableKey = ''
  private shouldResetScroll = false
  private requestSeq = 0
  private scrollFrame = 0
  private jumpTimer = 0
  private resizeFrame = 0
  private pendingJumpStart = 0
  private resizeDrag?: ColumnResizeDrag
  private expectedBlocks = new Map<WindowedTableBlockID, ExpectedBlockRequest>()
  private latestAcceptedSeq = new Map<WindowedTableBlockID, number>()
  private blockCache: Record<WindowedTableBlockID, WindowedTableBlock> = emptyBlocks()
  private viewportRef: Ref<HTMLDivElement> = createRef()
  private resizeObserver?: ResizeObserver

  static styles = css`
    :host {
      display: grid;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .shell {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr) auto;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-windowed-table-surface, var(--lv-bg-panel));
    }

    .toolbar,
    .footer {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      border-block-end: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-16);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .footer {
      border-block-start: var(--lv-border-muted);
      border-block-end: 0;
    }

    :host([compact]) .toolbar {
      display: none;
    }

    :host([compact]) .shell {
      grid-template-rows: minmax(0, 1fr) auto;
    }

    :host([compact]) .footer {
      justify-content: flex-start;
      padding: var(--base-size-4) var(--base-size-12);
    }

    .toolbar strong,
    .footer strong {
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-medium);
    }

    .options {
      position: relative;
    }

    .options summary {
      display: flex;
      width: auto;
      height: var(--lv-button-height-sm);
      align-items: center;
      gap: var(--base-size-6);
      border: var(--lv-border-default);
      border-radius: var(--lv-button-radius);
      background: var(--lv-button-bg-rest);
      color: var(--lv-button-fg-rest);
      padding: 0 var(--base-size-8);
      cursor: pointer;
      list-style: none;
    }

    .options summary::-webkit-details-marker {
      display: none;
    }

    .menu {
      position: absolute;
      top: calc(100% + var(--base-size-4));
      right: 0;
      z-index: var(--zIndex-overlay);
      display: grid;
      min-width: 220px;
      gap: var(--base-size-4);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
      padding: var(--base-size-8);
    }

    .menu label {
      display: flex;
      align-items: center;
      gap: var(--base-size-8);
      min-height: var(--control-xsmall-size);
      color: var(--lv-fg-default);
      font: var(--lv-type-caption);
      cursor: pointer;
    }

    .frame {
      position: relative;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      background: var(--lv-bg-app);
    }

    .scrollport {
      position: relative;
      width: 100%;
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: auto;
      scrollbar-gutter: stable;
    }

    .plane,
    .head,
    .row {
      min-width: var(--lv-windowed-table-width, 760px);
      width: var(--lv-windowed-table-width, 760px);
    }

    .head,
    .row {
      display: grid;
      grid-template-columns: var(--lv-windowed-table-columns);
    }

    .head {
      position: sticky;
      top: 0;
      z-index: 2;
      border-bottom: var(--lv-border-emphasis, var(--lv-border-default));
      background: var(--lv-windowed-table-surface, var(--lv-bg-panel));
      color: var(--lv-fg-muted);
    }

    .header-cell,
    .cell {
      min-width: 0;
      overflow: hidden;
      border-right: var(--lv-border-muted);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .header-cell {
      position: relative;
    }

    .header-cell:last-child,
    .cell:last-child {
      border-right: 0;
    }

    .header-cell button {
      display: flex;
      width: 100%;
      min-height: calc(var(--control-small-size) + var(--base-size-6));
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      border: 0;
      background: transparent;
      color: inherit;
      padding: 0 var(--base-size-8);
      cursor: pointer;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-align: left;
      text-transform: uppercase;
    }

    .header-cell button:hover,
    .header-cell button:focus-visible {
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    :host([compact]) .header-cell button {
      min-height: var(--control-small-size);
    }

    .sort {
      min-width: 1rem;
      color: var(--lv-fg-link);
      text-align: right;
    }

    .column-resizer {
      position: absolute;
      inset-block: 5px;
      right: -3px;
      z-index: calc(var(--zIndex-default) + 3);
      width: 6px;
      cursor: col-resize;
      touch-action: none;
    }

    .column-resizer::after {
      position: absolute;
      inset-block: 3px;
      left: 2px;
      width: 2px;
      border-radius: var(--lv-radius-full);
      background: transparent;
      content: "";
    }

    .header-cell:hover .column-resizer::after,
    .column-resizer.resizing::after {
      background: var(--lv-fg-link);
    }

    .resize-guide {
      position: absolute;
      top: 0;
      bottom: 0;
      left: var(--lv-windowed-resize-guide-x, -9999px);
      z-index: var(--zIndex-overlay);
      width: 0;
      border-left: 2px solid var(--lv-fg-link);
      box-shadow: 0 0 0 1px color-mix(in srgb, var(--lv-fg-link), transparent 74%);
      pointer-events: none;
    }

    .canvas {
      position: relative;
      min-width: var(--lv-windowed-table-width, 760px);
      width: var(--lv-windowed-table-width, 760px);
    }

    .row {
      position: absolute;
      inset-inline: 0;
      min-height: var(--lv-windowed-row-height, 32px);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-app);
    }

    .row:nth-child(even) {
      background: color-mix(in srgb, var(--lv-table-stripe, var(--lv-bg-panel-muted)), var(--lv-bg-app) 74%);
    }

    .row:hover {
      background: var(--lv-bg-control-hover);
    }

    .cell {
      display: flex;
      min-height: var(--lv-windowed-row-height, 32px);
      align-items: center;
      padding: 0 var(--base-size-8);
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
    }

    .cell.right,
    .header-cell.right button {
      justify-content: flex-end;
      text-align: right;
      font-variant-numeric: tabular-nums;
    }

    .cell code,
    .cell > span:not(.muted):not(.skeleton) {
      display: block;
      min-width: 0;
      width: 100%;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body);
      font-variant-numeric: tabular-nums;
    }

    .cell.right code,
    .cell.right > span:not(.muted):not(.skeleton) {
      text-align: right;
    }

    .muted,
    .empty,
    .error {
      color: var(--lv-fg-muted);
      padding: var(--base-size-16);
      font: var(--lv-type-body);
    }

    .error {
      color: var(--lv-fg-danger, var(--fgColor-danger));
    }

    .loading {
      position: absolute;
      inset-block-start: 0;
      inset-inline: 0;
      z-index: 3;
      height: 2px;
      overflow: hidden;
      background: color-mix(in srgb, var(--lv-fg-link), transparent 82%);
    }

    .loading::after {
      position: absolute;
      inset-block: 0;
      inset-inline-start: -30%;
      width: 30%;
      background: var(--lv-fg-link);
      content: "";
      animation: loading-slide 1s ease-in-out infinite;
    }

    .skeleton {
      display: block;
      width: min(78%, 160px);
      height: 9px;
      border-radius: var(--lv-radius-full);
      background: color-mix(in srgb, var(--lv-fg-muted), transparent 84%);
    }

    @keyframes loading-slide {
      from { transform: translateX(0); }
      to { transform: translateX(440%); }
    }
  `

  firstUpdated(): void {
    this.observeViewport()
  }

  updated(): void {
    if (this.shouldResetScroll) {
      this.shouldResetScroll = false
      queueMicrotask(() => {
        const viewport = this.viewportRef.value
        if (!viewport) return
        viewport.scrollTop = 0
        viewport.scrollLeft = 0
        this.viewportTop = 0
        this.viewportHeight = viewport.clientHeight
        this.scheduleEnsureBlocksForScroll()
      })
    }
    this.observeViewport()
  }

  willUpdate(): void {
    const table = normalizeTable(this.table)
    if (this.lastResetVersion !== table.resetVersion || this.lastTableKey !== table.tableKey) {
      const tableChanged = this.lastTableKey !== table.tableKey
      this.lastResetVersion = table.resetVersion
      this.lastTableKey = table.tableKey
      if (tableChanged) {
        this.localVisibleColumns = []
        this.localColumnWidths = {}
      }
      this.blockCache = emptyBlocks(table.chunkSize, table.sort, table.resetVersion)
      this.shouldResetScroll = true
      this.expectedBlocks.clear()
      this.latestAcceptedSeq.clear()
      this.clearJumpTimer()
    }
    this.mergeIncomingBlocks(table)
  }

  disconnectedCallback(): void {
    this.resizeObserver?.disconnect()
    if (this.scrollFrame) cancelAnimationFrame(this.scrollFrame)
    if (this.resizeFrame) cancelAnimationFrame(this.resizeFrame)
    this.clearJumpTimer()
    this.clearResize()
    super.disconnectedCallback()
  }

  render() {
    const table = normalizeTable(this.table)
    const columns = this.visibleColumns(table)
    const widths = this.displayColumnWidths(table, columns)
    const tableWidth = Math.max(760, this.viewportWidth, widths.reduce((sum, width) => sum + width, 0))
    const visibleRows = this.visibleRows(table)
    const rowRange = this.rowRangeText(table)
    const loading = Boolean(table.loadingBlock) || this.visibleLoading(table)

    return html`
      <section class="shell">
        <div class="toolbar">
          <span><strong>${rowRange}</strong>${loading ? ' · loading' : ''}</span>
          <details class="options">
            <summary title="Choose visible columns" aria-label="Choose visible columns">
              ${lucideIcon(Columns3, { size: 15 })}<span>Columns</span><span aria-hidden="true">${columns.length}/${table.columns.length}</span>
            </summary>
            <div class="menu">
              ${table.columns.map((column) => {
                const checked = columns.some((visible) => visible.key === column.key)
                return html`
                  <label>
                    <input
                      type="checkbox"
                      .checked=${checked}
                      ?disabled=${checked && columns.length <= 1}
                      @change=${(event: Event) => this.toggleColumn(table, column.key, (event.target as HTMLInputElement).checked)}
                    />
                    ${column.label || column.key}
                  </label>
                `
              })}
            </div>
          </details>
        </div>
        <div class="frame">
          ${loading ? html`<div class="loading" aria-hidden="true"></div>` : nothing}
          ${table.error ? html`<p class="error">${table.error}</p>` : nothing}
          ${!table.error && table.availableRows === 0 && !loading ? html`<p class="empty">No rows to show.</p>` : nothing}
          ${!table.error && (table.availableRows > 0 || loading) ? html`
            <div class="scrollport" ${ref(this.viewportRef)} @scroll=${this.handleScroll}>
              <div
                class="plane"
                style=${`--lv-windowed-table-columns:${widths.map((width) => `${width}px`).join(' ')};--lv-windowed-table-width:${tableWidth}px;--lv-windowed-row-height:${table.rowHeight}px`}
              >
                ${this.resizeGuide >= 0 ? html`<span class="resize-guide" style=${`--lv-windowed-resize-guide-x:${this.resizeGuide}px`}></span>` : nothing}
                <div class="head" role="row">
                  ${columns.map((column) => html`
                    <div class=${`header-cell ${column.align === 'right' ? 'right' : ''}`} role="columnheader">
                      <button type="button" @click=${() => this.sortColumn(table, column)}>
                        <span>${column.label || column.key}</span>
                        <span class="sort">${sortMarker(table.sort, column.key)}</span>
                      </button>
                      <span
                        class=${`column-resizer ${this.resizeDrag?.columnKey === column.key ? 'resizing' : ''}`}
                        @mousedown=${(event: MouseEvent) => this.beginColumnResize(table, column, event)}
                        @touchstart=${(event: TouchEvent) => this.beginColumnResize(table, column, event)}
                      ></span>
                    </div>
                  `)}
                </div>
                <div class="canvas" role="rowgroup" style=${`height:${Math.max(table.rowHeight, table.availableRows * table.rowHeight)}px`}>
                  ${visibleRows.map((slot) => slot.row
                    ? html`
                      <div class="row" role="row" style=${`top:${slot.index * table.rowHeight}px`}>
                        ${columns.map((column) => html`
                          <div class=${`cell ${column.align === 'right' ? 'right' : ''}`} role="cell" title=${cellLabel(slot.row?.[column.key])}>
                            ${renderCell(slot.row?.[column.key])}
                          </div>
                        `)}
                      </div>
                    `
                    : html`
                      <div class="row" role="row" aria-busy="true" style=${`top:${slot.index * table.rowHeight}px`}>
                        ${columns.map((column) => html`<div class=${`cell ${column.align === 'right' ? 'right' : ''}`} role="cell"><span class="skeleton"></span></div>`)}
                      </div>
                    `)}
                </div>
              </div>
            </div>
          ` : nothing}
        </div>
        <div class="footer">
          ${this.compact
            ? html`<span><strong>${rowRange}</strong>${loading ? ' · loading' : ''}</span>`
            : html`
              <span>${table.totalLabel || `${table.totalRows.toLocaleString()} rows`}</span>
              <span>${columns.length} visible · ${table.columns.length} total columns</span>
            `}
        </div>
      </section>
    `
  }

  private observeViewport(): void {
    const viewport = this.viewportRef.value
    if (!viewport || this.resizeObserver) return
    this.viewportHeight = viewport.clientHeight
    this.viewportWidth = viewport.clientWidth
    this.resizeObserver = new ResizeObserver(() => {
      this.viewportHeight = viewport.clientHeight
      this.viewportWidth = viewport.clientWidth
      this.requestUpdate()
      this.scheduleEnsureBlocksForScroll()
    })
    this.resizeObserver.observe(viewport)
    this.scheduleEnsureBlocksForScroll()
  }

  private visibleColumns(table: Required<WindowedTablePayload>): WindowedTableColumn[] {
    const configured = table.visibleColumns.length ? table.visibleColumns : this.localVisibleColumns
    if (!configured.length) return table.columns
    const allowed = new Set(configured)
    const visible = table.columns.filter((column) => allowed.has(column.key))
    return visible.length ? visible : table.columns
  }

  private loadedRows(table: Required<WindowedTablePayload>): Array<{ row: Record<string, unknown>; index: number }> {
    return blockIDs
      .map((id) => this.blockCache[id])
      .sort((a, b) => a.start - b.start)
      .flatMap((block) => block.rows.map((row, offset) => ({ row, index: block.start + offset })))
      .filter((item) => item.index < table.availableRows)
  }

  private visibleRows(table: Required<WindowedTablePayload>): VisibleRowSlot[] {
    if (table.availableRows <= 0) return []
    const rowMap = new Map(this.loadedRows(table).map((item) => [item.index, item.row]))
    const { first, last } = virtualRowRange(table.availableRows, this.viewportTop, this.viewportHeight || table.rowHeight, table.rowHeight, 2)
    const rows: VisibleRowSlot[] = []
    for (let index = first; index < last; index++) {
      rows.push({ index, row: rowMap.get(index) })
    }
    return rows
  }

  private visibleLoading(table: Required<WindowedTablePayload>): boolean {
    return this.visibleRows(table).some((row) => !row.row) || this.expectedBlocks.size > 0
  }

  private handleScroll = (event: Event): void => {
    const target = event.currentTarget as HTMLDivElement
    this.viewportTop = target.scrollTop
    this.viewportHeight = target.clientHeight
    this.scheduleEnsureBlocksForScroll()
  }

  private scheduleEnsureBlocksForScroll(): void {
    if (this.scrollFrame) return
    this.scrollFrame = requestAnimationFrame(() => {
      this.scrollFrame = 0
      this.ensureBlocksForScroll()
    })
  }

  private ensureBlocksForScroll(): void {
    const table = normalizeTable(this.table)
    if (table.availableRows <= 0) return
    const currentStart = Math.floor(Math.floor(this.viewportTop / table.rowHeight) / table.chunkSize) * table.chunkSize
    const desired = this.desiredStarts(table, currentStart)
    const desiredSet = new Set(desired)
    const loadedStarts = new Set(blockIDs.map((id) => this.blockCache[id]?.start ?? -1))
    const expectedStarts = new Set([...this.expectedBlocks.values()].map((request) => request.start))
    const missingStarts = desired.filter((start) => !loadedStarts.has(start) && !expectedStarts.has(start))

    if (missingStarts.length > 1 || !loadedStarts.has(currentStart) && !expectedStarts.has(currentStart)) {
      this.scheduleJumpBlock(currentStart)
      return
    }

    this.clearJumpTimer()
    const usedBlocks = new Set<WindowedTableBlockID>()
    for (const start of missingStarts) {
      const block = this.reusableBlock(desiredSet, usedBlocks)
      if (!block) continue
      usedBlocks.add(block)
      this.emitBlock(table, block, start, table.sort, table.resetVersion)
    }
  }

  private scheduleJumpBlock(start: number): void {
    if (this.jumpTimer && this.pendingJumpStart === start) return
    this.pendingJumpStart = start
    this.requestUpdate()
    this.clearJumpTimer()
    this.jumpTimer = window.setTimeout(() => {
      this.jumpTimer = 0
      const table = normalizeTable(this.table)
      this.emitBlock(table, 'all', this.pendingJumpStart, table.sort, table.resetVersion)
    }, 75)
  }

  private clearJumpTimer(): void {
    if (!this.jumpTimer) return
    clearTimeout(this.jumpTimer)
    this.jumpTimer = 0
  }

  private desiredStarts(table: Required<WindowedTablePayload>, currentStart: number): number[] {
    const starts = currentStart <= 0
      ? [0, table.chunkSize, table.chunkSize * 2]
      : [Math.max(0, currentStart - table.chunkSize), currentStart, currentStart + table.chunkSize]
    return starts.filter((start, index, all) => start < table.availableRows && all.indexOf(start) === index)
  }

  private reusableBlock(desiredStarts: Set<number>, usedBlocks: Set<WindowedTableBlockID>): WindowedTableBlockID | undefined {
    return blockIDs.find((id) => !usedBlocks.has(id) && !desiredStarts.has(this.blockCache[id]?.start ?? -1))
      ?? blockIDs.find((id) => !usedBlocks.has(id))
  }

  private emitBlock(
    table: Required<WindowedTablePayload>,
    block: WindowedTableBlockID | 'all',
    start: number,
    sort = table.sort,
    resetVersion = table.resetVersion,
  ): void {
    const count = table.chunkSize
    const requestSeq = ++this.requestSeq
    if (block === 'all') {
      this.expectedBlocks.clear()
      const starts = this.allBlockStarts(table, start)
      blockIDs.forEach((id, index) => {
        this.expectedBlocks.set(id, { start: starts[index], requestSeq, resetVersion, sort })
      })
    } else {
      this.expectedBlocks.set(block, { start, requestSeq, resetVersion, sort })
    }
    this.requestUpdate()
    this.dispatchEvent(new CustomEvent<WindowedTableRequest>('lv-windowed-table-request', {
      bubbles: true,
      composed: true,
      detail: { block, start, count, requestSeq, sort, resetVersion },
    }))
  }

  private allBlockStarts(table: Required<WindowedTablePayload>, start: number): number[] {
    const currentStart = Math.max(0, Math.floor(start / table.chunkSize) * table.chunkSize)
    if (currentStart <= 0) return [0, table.chunkSize, table.chunkSize * 2]
    return [Math.max(0, currentStart - table.chunkSize), currentStart, currentStart + table.chunkSize]
  }

  private mergeIncomingBlocks(table: Required<WindowedTablePayload>): void {
    const defaults = emptyBlocks(table.chunkSize, table.sort, table.resetVersion)
    for (const id of blockIDs) {
      const incoming = table.blocks[id]
      if (!incoming) continue
      if (!this.shouldAcceptBlock(table, id, incoming)) continue
      const defaultBlock = defaults[id]
      const carriesRows = incoming.rows.length > 0
      const carriesNonDefaultStart = incoming.start !== defaultBlock.start
      const cacheIsEmpty = this.blockCache[id].rows.length === 0
      if (carriesRows || carriesNonDefaultStart || cacheIsEmpty) {
        this.blockCache[id] = { ...incoming, rows: incoming.rows }
        if (incoming.requestSeq > 0) this.latestAcceptedSeq.set(id, incoming.requestSeq)
        const expected = this.expectedBlocks.get(id)
        if (expected && this.blockMatchesExpected(incoming, expected)) {
          this.expectedBlocks.delete(id)
        }
      }
    }
  }

  private shouldAcceptBlock(table: Required<WindowedTablePayload>, id: WindowedTableBlockID, incoming: WindowedTableBlock): boolean {
    const expected = this.expectedBlocks.get(id)
    if (expected) return this.blockMatchesExpected(incoming, expected)
    if (incoming.requestSeq > 0) {
      const lastAcceptedSeq = this.latestAcceptedSeq.get(id) ?? 0
      return incoming.requestSeq >= lastAcceptedSeq
        && incoming.resetVersion === table.resetVersion
        && sameSort(incoming.sort, table.sort)
    }
    return incoming.resetVersion === 0
      || incoming.resetVersion === table.resetVersion && sameSort(incoming.sort, table.sort)
  }

  private blockMatchesExpected(block: WindowedTableBlock, expected: ExpectedBlockRequest): boolean {
    return block.start === expected.start
      && block.requestSeq === expected.requestSeq
      && block.resetVersion === expected.resetVersion
      && sameSort(block.sort, expected.sort)
  }

  private sortColumn(table: Required<WindowedTablePayload>, column: WindowedTableColumn): void {
    if (column.sortable === false) return
    const current = normalizeSort(table.sort)
    const key = column.key
    const direction = current.key === key && current.direction === 'asc' ? 'desc' : 'asc'
    this.emitBlock(table, 'all', 0, { key, column: key, direction }, table.resetVersion + 1)
  }

  private columnWidth(table: Required<WindowedTablePayload>, column: WindowedTableColumn): number {
    const configured = this.localColumnWidths[column.key] ?? table.columnWidths[column.key]
    if (Number.isFinite(configured) && Number(configured) > 0) {
      return Math.max(this.minColumnWidth(column), Math.round(Number(configured)))
    }
    return Math.max(this.minColumnWidth(column), defaultColumnWidth(column))
  }

  private displayColumnWidths(table: Required<WindowedTablePayload>, columns: WindowedTableColumn[]): number[] {
    const widths = columns.map((column) => this.columnWidth(table, column))
    if (!widths.length) return widths
    const availableWidth = Math.max(760, this.viewportWidth)
    const currentWidth = widths.reduce((sum, width) => sum + width, 0)
    if (currentWidth >= availableWidth) return widths
    const extraPerColumn = (availableWidth - currentWidth) / widths.length
    return widths.map((width) => Math.floor(width + extraPerColumn))
  }

  private minColumnWidth(column: WindowedTableColumn): number {
    const configured = Number(column.minWidth)
    if (Number.isFinite(configured) && configured > 0) return configured
    return 64
  }

  private currentColumnWidths(table: Required<WindowedTablePayload>): Record<string, number> {
    const widths = { ...table.columnWidths, ...this.localColumnWidths }
    const out: Record<string, number> = {}
    for (const column of table.columns) {
      const width = widths[column.key]
      if (Number.isFinite(width) && width > 0) {
        out[column.key] = Math.max(this.minColumnWidth(column), Math.round(width))
      }
    }
    return out
  }

  private beginColumnResize(table: Required<WindowedTablePayload>, column: WindowedTableColumn, event: MouseEvent | TouchEvent): void {
    event.preventDefault()
    event.stopPropagation()
    const clientX = resizeClientX(event)
    if (clientX === null) return
    this.resizeDrag = {
      columnKey: column.key,
      startClientX: clientX,
      startSize: Math.round((event.currentTarget as HTMLElement).parentElement?.getBoundingClientRect().width ?? this.columnWidth(table, column)),
      minSize: this.minColumnWidth(column),
    }
    this.scheduleResize(event)
    document.addEventListener('mousemove', this.handleResizeMove)
    document.addEventListener('mouseup', this.handleResizeEnd, { once: true })
    document.addEventListener('touchmove', this.handleResizeMove, { passive: true })
    document.addEventListener('touchend', this.handleResizeEnd, { once: true })
    document.addEventListener('touchcancel', this.handleResizeEnd, { once: true })
  }

  private handleResizeMove = (event: MouseEvent | TouchEvent): void => {
    this.scheduleResize(event)
  }

  private handleResizeEnd = (): void => {
    const table = normalizeTable(this.table)
    const columnWidths = this.currentColumnWidths(table)
    this.clearResize()
    this.dispatchEvent(new CustomEvent('lv-windowed-table-column-widths', {
      bubbles: true,
      composed: true,
      detail: { columnWidths },
    }))
  }

  private scheduleResize(event: MouseEvent | TouchEvent): void {
    const clientX = resizeClientX(event)
    if (clientX === null) return
    if (this.resizeFrame) cancelAnimationFrame(this.resizeFrame)
    this.resizeFrame = requestAnimationFrame(() => {
      this.resizeFrame = 0
      const plane = this.renderRoot.querySelector<HTMLElement>('.plane')
      if (!plane || !this.resizeDrag) return
      this.resizeGuide = resizeGuideX(plane, clientX)
      const width = resizedColumnWidth(this.resizeDrag, clientX, resizePlaneScaleX(plane))
      this.localColumnWidths = { ...this.localColumnWidths, [this.resizeDrag.columnKey]: width }
    })
  }

  private clearResize(): void {
    document.removeEventListener('mousemove', this.handleResizeMove)
    document.removeEventListener('mouseup', this.handleResizeEnd)
    document.removeEventListener('touchmove', this.handleResizeMove)
    document.removeEventListener('touchend', this.handleResizeEnd)
    document.removeEventListener('touchcancel', this.handleResizeEnd)
    if (this.resizeFrame) cancelAnimationFrame(this.resizeFrame)
    this.resizeFrame = 0
    this.resizeDrag = undefined
    this.resizeGuide = -1
  }

  private toggleColumn(table: Required<WindowedTablePayload>, columnKey: string, checked: boolean): void {
    const current = this.visibleColumns(table).map((column) => column.key)
    const next = checked ? [...new Set([...current, columnKey])] : current.filter((key) => key !== columnKey)
    this.localVisibleColumns = next
    this.dispatchEvent(new CustomEvent('lv-windowed-table-columns', {
      bubbles: true,
      composed: true,
      detail: { visibleColumns: next },
    }))
  }

  private rowRangeText(table: Required<WindowedTablePayload>): string {
    if (!table.totalRows || !table.availableRows) return 'No rows'
    const firstIndex = Math.min(table.availableRows - 1, Math.max(0, Math.floor(this.viewportTop / table.rowHeight)))
    const visibleRows = Math.max(1, Math.ceil((this.viewportHeight || table.rowHeight) / table.rowHeight))
    const lastIndex = Math.min(table.availableRows, firstIndex + visibleRows)
    return `${(firstIndex + 1).toLocaleString()}-${lastIndex.toLocaleString()} of ${table.totalRows.toLocaleString()}`
  }
}

function defaultColumnWidth(column: WindowedTableColumn): number {
  if (Number.isFinite(column.width) && Number(column.width) > 0) return Number(column.width)
  if (column.align === 'right') return 128
  if (column.key.length > 24) return 240
  return 168
}

function sortMarker(sort: WindowedTableSort, column: string): string {
  const normalized = normalizeSort(sort)
  if (normalized.key !== column) return ''
  return normalized.direction === 'desc' ? '↓' : '↑'
}

function renderCell(value: unknown) {
  const text = cellLabel(value)
  if (text === '-') return html`<span class="muted">-</span>`
  if (typeof value === 'number') return html`<span>${text}</span>`
  return html`<code>${text}</code>`
}

function cellLabel(value: unknown): string {
  if (value == null || value === '') return '-'
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

if (!customElements.get('lv-windowed-table')) customElements.define('lv-windowed-table', WindowedTable)

declare global {
  interface HTMLElementTagNameMap {
    'lv-windowed-table': WindowedTable
  }
}
