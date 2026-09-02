import { LitElement, css, html, nothing } from 'lit'
import { createRef, ref, type Ref } from 'lit/directives/ref.js'
import { EllipsisVertical } from 'lucide'
import { type ColumnResizeDrag, resizeClientX, resizeGuideX, resizePlaneScaleX, resizedColumnWidth } from '../../shared/column-resize'
import { lucideIcon } from '../../shared/lucide-icons'
import {
  TableController,
  callMemoOrStaticFn,
  columnPinningFeature,
  columnResizingFeature,
  columnSizingFeature,
  columnVisibilityFeature,
  createCoreRowModel,
  flexRender,
  rowSelectionFeature,
  rowSortingFeature,
  tableFeatures,
  type ColumnDef,
  type ColumnSizingState,
  type ColumnVisibilityState,
  type RowSelectionState,
  type SortingState,
} from '@tanstack/lit-table'
import {
  column_getCanHide,
  column_getIsLastColumn,
  column_getIsPinned,
  column_getStart,
  column_getIsVisible,
  column_getToggleVisibilityHandler,
  row_getVisibleCells,
  table_getAllLeafColumns,
} from '@tanstack/table-core/static-functions'
import { visualMenuIcon } from '../visual-menu-icons'
import { visualActionStyles } from '../visual-action-styles'
import { conditionalCellAppearance } from './conditional-formatting'
import { defaultDirection, formatCell, rowKey } from './format'
import { blockStartsForAll, emptyBlocks, emptyTable, preserveCardinality, sameSort, sortedBlockRows, tableConverter } from './block-source'
import {
  buildRowSelectionCommand,
  rowSelectionFromEntries as tableRowSelectionFromEntries,
  type RowClickSelectionAction,
} from './selection'
import {
  blockIDs,
  defaultChunkSize,
  defaultRowHeight,
  defaultSort,
  type BlockID,
  type ExpectedBlockRequest,
  type SortDirection,
  type TableBlock,
  type VisualWindowCommand,
  type TableColumn,
  type TableRow,
  type TableSignal,
  type TanStackTableRow,
  type VisualAction,
  type VisibleRowSlot,
} from './types'
import {
  ReportTableColumnController,
  ReportTableFormattingController,
  ReportTableSelectionController,
  ReportTableVirtualizationController,
} from './report-table-controller'

const reportTableFeatures = tableFeatures({
  columnPinningFeature,
  columnResizingFeature,
  columnSizingFeature,
  columnVisibilityFeature,
  rowSelectionFeature,
  rowSortingFeature,
})

const groupHeaderHeight = 26

function defaultColumnSize(column: TableColumn): number {
  const configuredWidth = Number(column.width)
  if (Number.isFinite(configuredWidth) && configuredWidth > 0) return configuredWidth
  const widths: Record<string, number> = {
    order_id: 160,
    purchase_date: 126,
    status: 106,
    state: 78,
    category: 210,
    revenue: 114,
    review_score: 104,
    delivery_days: 108,
  }
  if (widths[column.key]) return widths[column.key]
  if (column.align === 'right') return 114
  return 140
}

function applyUpdater<T>(updater: unknown, current: T): T {
  return typeof updater === 'function' ? (updater as (old: T) => T)(current) : updater as T
}

function columnVisible(columnID: string, visibility: ColumnVisibilityState): boolean {
  return visibility[columnID] !== false
}

function visibleHeaders(table: any, visibility: ColumnVisibilityState): any[] {
  const groups = table.getHeaderGroups?.() ?? []
  const headers = groups[groups.length - 1]?.headers ?? []
  return headers.filter((header: any) => columnVisible(header.column.id, visibility))
}

function allTableColumns(table: any): any[] {
  return table.getAllLeafColumns?.() ?? callMemoOrStaticFn(table, 'getAllLeafColumns', table_getAllLeafColumns)
}

function visibleColumnsFromHeaders(headers: any[], columns: TableColumn[]): TableColumn[] {
  return headers
    .map((header) => header.column.columnDef.meta?.column ?? columns.find((item) => item.key === header.column.id))
    .filter(Boolean) as TableColumn[]
}

function visibleCellsForRow(row: any, visibility: ColumnVisibilityState): any[] {
  const cells = row?.getVisibleCells?.() ?? callMemoOrStaticFn(row, 'getVisibleCells', row_getVisibleCells)
  return cells.filter((cell: any) => columnVisible(cell.column.id, visibility))
}

function columnIsVisible(column: any, fallback: ColumnVisibilityState): boolean {
  if (column.id in fallback) return columnVisible(column.id, fallback)
  return column?.getIsVisible?.() ?? callMemoOrStaticFn(column, 'getIsVisible', column_getIsVisible) ?? true
}

function columnCanHide(column: any): boolean {
  return column?.getCanHide?.() ?? callMemoOrStaticFn(column, 'getCanHide', column_getCanHide) ?? true
}

function columnVisibilityHandler(column: any, fallback: (checked: boolean) => void): (event: Event) => void {
  const handler = column?.getToggleVisibilityHandler?.() ?? callMemoOrStaticFn(column, 'getToggleVisibilityHandler', column_getToggleVisibilityHandler)
  return (event: Event) => {
    if (typeof handler === 'function') handler(event)
    fallback((event.currentTarget as HTMLInputElement).checked)
  }
}

export class ReportTable extends LitElement {
  static properties = {
    tableId: { attribute: 'table-id' },
    table: { attribute: 'table', converter: tableConverter },
    selectedCellKey: { state: true },
    viewportTop: { state: true },
    viewportHeight: { state: true },
    columnVisibility: { state: true },
    columnSizing: { state: true },
    rowSelection: { state: true },
    hoveredRowId: { state: true },
    resizeGuideX: { state: true },
  }

  declare tableId: string
  declare table: TableSignal
  declare private selectedCellKey: string
  declare private viewportTop: number
  declare private viewportHeight: number
  declare private columnVisibility: ColumnVisibilityState
  declare private columnSizing: ColumnSizingState
  declare private rowSelection: RowSelectionState
  declare private hoveredRowId: string
  declare private resizeGuideX: number
  private lastResetVersion = -1
  private shouldResetScroll = false
  private requestSeq = 0
  private scrollFrame = 0
  private jumpTimer = 0
  private pendingJumpStart = 0
  private expectedBlocks = new Map<BlockID, ExpectedBlockRequest>()
  private latestAcceptedSeq = new Map<BlockID, number>()
  private blockCache: Record<BlockID, TableBlock> = emptyBlocks()
  private bodyViewportRef: Ref<HTMLDivElement> = createRef()
  private resizeObserver?: ResizeObserver
  private resizeGuideFrame = 0
  private resizeDrag?: ColumnResizeDrag
  private tableController = new TableController<typeof reportTableFeatures, TanStackTableRow>(this)
  private readonly virtualizationController = new ReportTableVirtualizationController()
  private readonly selectionController = new ReportTableSelectionController()
  private readonly columnController = new ReportTableColumnController(() => this.columnSizing)
  private readonly formattingController = new ReportTableFormattingController()
  private handleOutsidePointerDown = (event: PointerEvent) => {
    const details = this.renderRoot.querySelector<HTMLDetailsElement>('.visual-options')
    if (!details?.open) return
    if (!event.composedPath().includes(details)) details.removeAttribute('open')
  }
  private handleDocumentKeyDown = (event: KeyboardEvent) => {
    if (event.key !== 'Escape') return
    this.renderRoot.querySelector<HTMLDetailsElement>('.visual-options')?.removeAttribute('open')
  }
  private handleResizeGuideMove = (event: MouseEvent | TouchEvent) => {
    this.scheduleResizeGuideUpdate(event)
  }
  private handleResizeGuideEnd = () => {
    this.clearResizeGuide()
  }

  constructor() {
    super()
    this.tableId = ''
    this.table = emptyTable
    this.selectedCellKey = ''
    this.viewportTop = 0
    this.viewportHeight = 0
    this.columnVisibility = {}
    this.columnSizing = {}
    this.rowSelection = {}
    this.hoveredRowId = ''
    this.resizeGuideX = -1
  }

  static styles = [visualActionStyles, css`
    :host {
      display: block;
      height: 100%;
      min-height: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .shell {
      display: flex;
      flex-direction: column;
      height: 100%;
      min-height: 0;
      min-width: 0;
      background: var(--lv-chart-surface);
      isolation: isolate;
    }

    .toolbar {
      position: relative;
      z-index: var(--zIndex-sticky);
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      min-height: calc(var(--control-small-size) + var(--base-size-6));
      border-bottom: var(--lv-border-default);
      background: var(--lv-chart-surface);
      padding:
        var(--base-size-6)
        var(--base-size-8)
        var(--base-size-4)
        var(--control-small-paddingInline-normal);
    }

    .toolbar::after {
      content: '';
      position: absolute;
      inset-inline: 0;
      bottom: var(--base-size-negative-2);
      z-index: calc(var(--zIndex-default) + 1);
      height: var(--base-size-4);
      background: inherit;
      pointer-events: none;
    }

    .toolbar-title {
      position: relative;
      z-index: calc(var(--zIndex-default) + 2);
      flex: 1 1 auto;
      min-width: 0;
    }

    h2 {
      min-width: 0;
      margin: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      letter-spacing: 0;
    }

    .visual-options {
      position: relative;
      z-index: calc(var(--zIndex-default) + 2);
      flex: 0 0 auto;
    }

    .visual-actions {
      position: relative;
      z-index: calc(var(--zIndex-default) + 2);
    }

    .visual-actions .icon-action,
    .visual-options summary {
      width: var(--lv-button-height, var(--control-medium-size));
      height: var(--lv-button-height, var(--control-medium-size));
      min-height: var(--lv-button-height, var(--control-medium-size));
    }

    .visual-options summary {
      display: grid;
      place-items: center;
      border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
      border-radius: var(--lv-radius-tight);
      background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
      color: var(--lv-button-invisible-icon-rest, var(--lv-fg-muted));
      cursor: pointer;
      font: var(--lv-type-body-large);
      line-height: 1;
      list-style: none;
    }

    .visual-options summary::-webkit-details-marker {
      display: none;
    }

    .visual-options summary svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .visual-options summary:hover,
    .visual-options summary:focus-visible,
    .visual-options[open] summary {
      border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-default)));
      background: color-mix(in srgb, var(--lv-bg-panel) 80%, var(--lv-fg-muted));
      color: var(--lv-fg-default);
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
      outline-offset: var(--focus-outline-offset, var(--base-size-2));
    }

    .menu {
      position: absolute;
      top: calc(100% + var(--base-size-4));
      right: 0;
      z-index: var(--zIndex-dropdown);
      display: grid;
      width: calc(var(--overlay-width-xsmall) - var(--base-size-16));
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-overlay);
      box-shadow: var(--shadow-floating-small);
      padding: var(--base-size-4);
    }

    .menu button {
      display: flex;
      align-items: center;
      gap: var(--base-size-8);
      min-height: var(--lv-button-height-sm, var(--control-small-size));
      border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
      border-radius: var(--lv-radius-tight);
      background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
      color: var(--lv-button-invisible-fg-rest, var(--lv-fg-default));
      cursor: pointer;
      padding: 0 var(--lv-button-padding-inline-xs, var(--control-xsmall-paddingInline-normal));
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-align: left;
    }

    .menu svg {
      flex: 0 0 auto;
      width: var(--base-size-16);
      height: var(--base-size-16);
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: 2;
    }

    .menu button:hover,
    .menu button:focus-visible {
      border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-default)));
      background: color-mix(in srgb, var(--lv-bg-panel) 80%, var(--lv-fg-muted));
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
      outline-offset: var(--focus-outline-offset, var(--base-size-2));
    }

    .menu button:disabled {
      cursor: default;
      opacity: var(--opacity-disabled);
    }

    .menu button:disabled:hover {
      background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
    }

    .menu-divider {
      height: var(--borderWidth-default);
      margin: var(--base-size-4) var(--base-size-2);
      background: var(--lv-line-muted);
    }

    .column-menu {
      display: grid;
      gap: var(--base-size-4);
      padding: var(--base-size-2);
    }

    .column-menu > span {
      padding: var(--base-size-2) var(--base-size-6);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: uppercase;
    }

    .column-menu label {
      display: flex;
      align-items: center;
      gap: var(--base-size-8);
      min-height: var(--control-xsmall-size);
      border-radius: var(--lv-radius-tight);
      cursor: pointer;
      padding: 0 var(--base-size-6);
      font: var(--lv-type-caption);
    }

    .column-menu label:hover {
      background: var(--lv-bg-hover);
    }

    .column-menu input {
      accent-color: var(--lv-fg-link);
    }

    .error {
      border-bottom: var(--lv-border-danger);
      background: var(--lv-bg-danger-muted);
      color: var(--lv-fg-danger);
      padding: var(--base-size-8) var(--base-size-12);
      font: var(--lv-type-body);
    }

    .head,
    .group-head,
    .row {
      display: grid;
      grid-template-columns: var(--lv-table-columns);
      width: max(100%, var(--lv-table-width, 1080px));
      min-width: var(--lv-table-width, 1080px);
    }

    .group-head {
      position: sticky;
      top: 0;
      z-index: calc(var(--zIndex-sticky) + 2);
      border-bottom: var(--lv-border-default);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
    }

    .group-cell {
      display: flex;
      align-items: center;
      min-width: 0;
      min-height: var(--lv-group-head-height, 26px);
      overflow: hidden;
      border-right: var(--lv-border-default);
      background: inherit;
      padding: 0 var(--base-size-8);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-caption);
      letter-spacing: 0;
      text-transform: uppercase;
    }

	.group-cell.metric-group {
      justify-content: center;
      color: var(--lv-fg-default);
    }

    .group-cell:last-child {
      border-right: 0;
    }

    .head {
      position: sticky;
      top: var(--lv-head-top, 0px);
      z-index: calc(var(--zIndex-sticky) + 1);
      border-bottom: var(--lv-border-emphasis);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      box-shadow: inset 0 -1px 0 var(--lv-line-emphasis);
    }

    .header-cell,
    .cell {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .header-cell {
      position: relative;
      border-right: var(--lv-border-default);
      background: var(--lv-bg-panel-muted);
    }

    .header-cell:last-child {
      border-right: 0;
    }

    .header-cell.pinned-left,
    .group-cell.pinned-left,
    .cell.pinned-left {
      position: sticky;
      left: calc(var(--lv-pin-left, 0px) - 1px);
      overflow: visible;
      border-right: 0;
      background: var(--lv-chart-surface);
      box-shadow: none;
    }

    .header-cell.pinned-left-edge::after,
    .group-cell.pinned-left-edge::after,
    .cell.pinned-left-edge::after {
      content: '';
      position: absolute;
      inset-block: 0;
      left: 100%;
      z-index: calc(var(--zIndex-default) + 1);
      width: 1px;
      background: var(--lv-line-default);
      pointer-events: none;
    }

    .header-cell.pinned-left {
      z-index: calc(var(--zIndex-sticky) + 4);
      background: var(--lv-bg-panel-muted);
    }

    .group-cell.pinned-left {
      z-index: calc(var(--zIndex-sticky) + 5);
      background: var(--lv-bg-panel-muted);
    }

    .cell.pinned-left {
      z-index: calc(var(--zIndex-default) + 2);
      background: var(--lv-row-bg-current, var(--lv-chart-surface));
    }

    .header-cell.pinned-left > .header-button,
    .cell.pinned-left > *,
    .group-cell.pinned-left > * {
      position: relative;
      z-index: calc(var(--zIndex-default) + 2);
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
    }

    .cell-value {
      display: block;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .cell-action {
      display: block;
      width: 100%;
      height: 100%;
      min-width: 0;
      overflow: hidden;
      border: 0;
      background: transparent;
      color: inherit;
      cursor: pointer;
      padding: 0 var(--base-size-8);
      font: inherit;
      text-align: inherit;
    }

    .cell-action:focus-visible {
      position: relative;
      z-index: calc(var(--zIndex-default) + 4);
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
      outline-offset: calc(-1 * var(--base-size-2));
    }

    button.header-button {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      width: 100%;
      min-height: calc(var(--lv-button-height-sm, var(--control-small-size)) + var(--base-size-6));
      border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
      border-bottom: var(--borderWidth-thick) solid transparent;
      background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
      color: var(--lv-button-invisible-fg-rest, inherit);
      cursor: pointer;
      padding: 0 var(--base-size-8);
      font: var(--lv-type-caption);
      letter-spacing: 0;
      text-align: left;
      text-transform: uppercase;
    }

    button.header-button > span:first-child {
      flex: 1 1 auto;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    button.header-button:hover,
    button.header-button:focus-visible {
      border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-default)));
      background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover));
      color: var(--lv-fg-default);
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
      outline-offset: var(--focus-outline-offset, var(--base-size-2));
    }

    .sort {
      display: inline-grid;
      min-width: var(--base-size-20);
      place-items: center;
      color: var(--lv-fg-link);
      font: var(--lv-type-body);
      opacity: 0;
    }

    .sorted .sort {
      opacity: 1;
    }

    .column-resizer {
      position: absolute;
      inset-block: 5px;
      right: -3px;
      z-index: calc(var(--zIndex-default) + 3);
      width: 6px;
      cursor: col-resize;
    }

    .column-resizer::after {
      content: '';
      position: absolute;
      inset-block: 3px;
      left: 2px;
      width: 2px;
      border-radius: var(--lv-radius-full);
      background: transparent;
    }

    .header-cell:hover .column-resizer::after,
    .column-resizer.resizing::after {
      background: var(--lv-fg-link);
    }

    .table-frame {
      position: relative;
      display: flex;
      flex: 1 1 auto;
      flex-direction: column;
      min-height: 0;
      min-width: 0;
      margin-top: -1px;
      overflow: hidden;
      border-top: 1px solid var(--lv-line-default);
      background: var(--lv-chart-surface);
    }

    .table-scrollport {
      position: relative;
      flex: 1 1 auto;
      overflow: auto;
      min-height: 0;
      min-width: 0;
      background: var(--lv-chart-surface);
      overscroll-behavior: none;
      scrollbar-gutter: stable;
    }

    .table-scrollport:focus-visible {
      outline: 2px solid var(--lv-fg-accent, currentColor);
      outline-offset: -2px;
    }

    .table-plane {
      position: relative;
      isolation: isolate;
      width: max(100%, var(--lv-table-width, 1080px));
      min-width: var(--lv-table-width, 1080px);
    }

    .canvas {
      position: relative;
      z-index: 0;
      width: max(100%, var(--lv-table-width, 1080px));
      min-width: var(--lv-table-width, 1080px);
    }

    .grid-lines {
      position: absolute;
      inset: 0;
      z-index: 0;
      pointer-events: none;
    }

    .grid-line {
      position: absolute;
      top: 0;
      bottom: 0;
      width: 1px;
      background: var(--lv-line-muted);
    }

    .resize-guide {
      position: absolute;
      top: 0;
      bottom: 0;
      left: var(--lv-resize-guide-x, -9999px);
      z-index: var(--zIndex-overlay);
      width: 0;
      border-left: 2px solid var(--lv-fg-link);
      box-shadow: 0 0 0 var(--borderWidth-default) var(--borderColor-accent-muted);
      pointer-events: none;
    }

    .row {
      position: absolute;
      inset-inline: 0;
      z-index: 1;
      height: var(--lv-row-height, 34px);
      --lv-row-bg: var(--lv-chart-surface);
      --lv-row-bg-hover: var(--control-transparent-bgColor-hover);
      --lv-row-bg-selected: var(--bgColor-accent-muted);
      --lv-row-bg-current: var(--lv-row-bg);
      background: var(--lv-row-bg-current);
      color: var(--lv-fg-default);
    }

    .zebra .row:nth-child(even) {
      --lv-row-bg: var(--lv-table-stripe);
    }

    .grid-rows .row,
    .grid-full .row {
      border-bottom: var(--lv-border-muted);
    }

    .row:hover {
      --lv-row-bg-current: var(--lv-row-bg-hover);
    }

    .row.hovered {
      --lv-row-bg-current: var(--lv-row-bg-hover);
    }

    .row.selected {
      --lv-row-bg-current: var(--lv-row-bg-selected);
    }

    .row.selected:hover,
    .row.selected.hovered {
      --lv-row-bg-current: var(--lv-row-bg-selected);
    }

    .row.highlight-dimmed {
      opacity: 0.24;
    }

    .row.highlighted {
      box-shadow: inset 3px 0 0 var(--lv-line-accent);
    }

    .row.skeleton-row {
      pointer-events: none;
    }

    .row.skeleton-row:hover {
      background: var(--lv-row-bg);
    }

    .cell {
      display: flex;
      align-items: center;
      min-width: 0;
      border: 0;
      background: transparent;
      color: inherit;
      cursor: default;
      font: inherit;
      padding: 0 var(--base-size-8);
      font: var(--lv-type-body);
      text-align: left;
    }

    .density-compact .cell {
      padding: 0 var(--base-size-6);
      font: var(--lv-type-caption);
    }

    .density-spacious .cell {
      padding: 0 var(--base-size-12);
      font: var(--lv-type-body-large);
    }

    .grid-columns .cell,
    .grid-full .cell {
      border-right: var(--lv-border-muted);
    }

    .cell:last-child {
      border-right: 0;
    }

    .cell.active {
      outline: var(--lv-border-width-focus) solid var(--lv-fg-link);
      outline-offset: var(--base-size-negative-2);
      background: var(--bgColor-accent-muted);
    }

    .skeleton-cell {
      cursor: default;
    }

    .cell.has-background {
      background: color-mix(in srgb, var(--lv-cell-bg-color), transparent var(--lv-cell-bg-fade, 78%));
    }

    .cell.has-data-bar {
      position: relative;
      isolation: isolate;
    }

    .cell-data-bar {
      position: absolute;
      inset-block: 5px;
      left: 6px;
      z-index: var(--zIndex-behind);
      width: var(--lv-cell-bar-width, 0%);
      border-radius: var(--lv-radius-tight);
      background: color-mix(in srgb, var(--lv-cell-bar-color, var(--lv-fg-link)), transparent 74%);
    }

    .cell-badge {
      display: inline-flex;
      max-width: 100%;
      align-items: center;
      justify-content: center;
      overflow: hidden;
      border: 1px solid currentColor;
      border-radius: var(--lv-radius-full);
      padding: 1px 7px;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    .cell-badge.tone-success {
      background: var(--lv-bg-success-muted);
      color: var(--lv-fg-success);
    }

    .cell-badge.tone-danger {
      background: var(--lv-bg-danger-muted);
      color: var(--lv-fg-danger);
    }

    .cell-badge.tone-warning {
      background: var(--lv-bg-warning-muted);
      color: var(--lv-fg-warning);
    }

    .cell-badge.tone-muted {
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
    }

    .cell-badge.tone-accent,
    .cell-badge.tone-blue {
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-link);
    }

    .conditional-cue {
      display: inline-block;
      margin-inline-end: var(--base-size-4);
      font-weight: var(--base-text-weight-semibold);
    }

    .conditional-cue-label {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      margin: -1px;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }

    .grid-none .grid-lines,
    .grid-rows .grid-lines {
      display: none;
    }

    .skeleton-line {
      display: block;
      width: min(76%, 140px);
      height: 9px;
      overflow: hidden;
      border-radius: var(--lv-radius-full);
      background: linear-gradient(
        90deg,
        var(--lv-bg-panel-muted) 0%,
        color-mix(in srgb, var(--lv-fg-muted), transparent 82%) 45%,
        var(--lv-bg-panel-muted) 90%
      );
      background-size: 220% 100%;
      animation: shimmer var(--base-duration-1000) var(--motion-easing-move) infinite;
      opacity: 0.78;
    }

    .skeleton-cell:nth-child(2n) .skeleton-line {
      width: min(58%, 120px);
    }

    .right {
      justify-content: end;
      font-variant-numeric: tabular-nums;
    }

    .empty {
      display: grid;
      min-height: 240px;
      place-items: center;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-large);
    }

    .loading {
      position: absolute;
      inset-inline: 0;
      top: 0;
      z-index: var(--zIndex-sticky);
      height: var(--base-size-4);
      overflow: hidden;
      background: var(--lv-bg-accent-muted);
    }

    .loading::after {
      content: '';
      display: block;
      width: 34%;
      height: 100%;
      background: var(--lv-fg-link);
      animation: load var(--base-duration-900) var(--motion-easing-move) infinite;
    }

    .footer {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      min-height: calc(var(--control-small-size) + var(--base-size-6));
      border-top: var(--lv-border-default);
      background: var(--lv-bg-panel-muted);
      padding: var(--base-size-6) var(--control-small-paddingInline-normal);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .footer span {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .footer span:last-child {
      flex: 0 0 auto;
      margin-left: auto;
      text-align: right;
    }

    .footer strong {
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-medium);
    }

    @keyframes load {
      0% { transform: translateX(-100%); }
      100% { transform: translateX(310%); }
    }

    @keyframes shimmer {
      0% { background-position: 120% 0; }
      100% { background-position: -120% 0; }
    }

    @media (max-width: 760px) {
      .shell {
        min-height: 360px;
      }
    }
  `]

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('pointerdown', this.handleOutsidePointerDown)
    document.addEventListener('keydown', this.handleDocumentKeyDown)
    if (this.hasUpdated) queueMicrotask(() => this.startViewportObserver())
  }

  firstUpdated(): void {
    this.startViewportObserver()
  }

  private startViewportObserver(): void {
    const viewport = this.bodyViewportRef.value
    if (!viewport) return
    this.resizeObserver?.disconnect()
    const syncViewport = () => {
      const viewportHeight = viewport.clientHeight
      if (viewportHeight === this.viewportHeight) return
      this.viewportHeight = viewportHeight
      this.virtualizationController.setViewport(this.viewportTop, this.viewportHeight)
      this.scheduleEnsureBlocksForScroll()
    }
    syncViewport()
    this.resizeObserver = new ResizeObserver(syncViewport)
    this.resizeObserver.observe(viewport)
  }

  disconnectedCallback(): void {
    document.removeEventListener('pointerdown', this.handleOutsidePointerDown)
    document.removeEventListener('keydown', this.handleDocumentKeyDown)
    this.resizeObserver?.disconnect()
    if (this.scrollFrame) {
      cancelAnimationFrame(this.scrollFrame)
      this.scrollFrame = 0
    }
    this.clearResizeGuide()
    this.clearJumpTimer()
    super.disconnectedCallback()
  }

  willUpdate(changedProperties: Map<PropertyKey, unknown>): void {
	const previousTable = changedProperties.get('table')
	if (previousTable) {
	  this.table = preserveCardinality(previousTable as TableSignal, this.table)
	}
    if (this.lastResetVersion !== this.table.resetVersion) {
      this.lastResetVersion = this.table.resetVersion
      this.blockCache = emptyBlocks()
      this.shouldResetScroll = true
      this.expectedBlocks.clear()
      this.latestAcceptedSeq.clear()
      this.clearJumpTimer()
      this.clearLocalSelection()
    }
    this.mergeIncomingBlocks()
    if (changedProperties.has('table')) {
      this.syncSelectedRowFromTableSelection()
    }
  }

  updated(): void {
    if (this.shouldResetScroll) {
      this.shouldResetScroll = false
      queueMicrotask(() => {
        const viewport = this.bodyViewportRef.value
        if (!viewport) return
        viewport.scrollTop = 0
        viewport.scrollLeft = 0
        this.viewportTop = 0
        this.viewportHeight = viewport.clientHeight
        this.virtualizationController.setViewport(this.viewportTop, this.viewportHeight)
        this.scheduleEnsureBlocksForScroll()
      })
    }
  }

  get columns(): TableColumn[] {
    return Array.isArray(this.table?.columns) ? this.table.columns : []
  }

  get loadedRows(): Array<{ row: TableRow; index: number }> {
    return sortedBlockRows(this.blocks, this.availableRows)
  }

  get visibleRows(): VisibleRowSlot[] {
    if (this.availableRows <= 0) return []
    const rowMap = new Map(this.loadedRows.map((item) => [item.index, item.row]))
    const { first, last } = this.virtualizationController.visibleRange(this.availableRows, this.rowHeight, 2)
    const rows: VisibleRowSlot[] = []
    for (let index = first; index < last; index++) {
      const row = rowMap.get(index)
      rows.push(row ? { kind: 'row', row, index } : { kind: 'skeleton', index })
    }
    return rows
  }

  get visibleLoading(): boolean {
    return this.visibleRows.some((row) => row.kind === 'skeleton') || this.expectedBlocks.size > 0
  }

  get availableRows(): number {
    return Math.max(0, this.table.availableRows ?? 0)
  }

  get blocks(): Record<BlockID, TableBlock> {
    return this.blockCache
  }

  get chunkSize(): number {
    return Math.max(1, this.table.chunkSize || defaultChunkSize)
  }

  get rowHeight(): number {
    return Math.max(1, this.table.rowHeight || defaultRowHeight)
  }

  private gridTemplateFor(columns: TableColumn[]): string {
    return this.columnPixelWidths(columns).map((size) => `minmax(${size}px, ${size}fr)`).join(' ')
  }

  private tableWidthFor(columns: TableColumn[]): number {
    return this.columnPixelWidths(columns).reduce((sum, size) => sum + size, 0)
  }

  private columnLineOffsetsFor(columns: TableColumn[]): string[] {
    const widths = this.columnPixelWidths(columns)
    const total = widths.reduce((sum, width) => sum + width, 0)
    let offset = 0
    return widths.slice(0, -1).map((width) => {
      offset += width
      return `${total > 0 ? (offset / total) * 100 : 0}%`
    })
  }

  private columnPixelWidths(columns: TableColumn[]): number[] {
    return this.columnController.pixelWidths(columns)
  }

  private columnPixelWidth(column: TableColumn): number {
    return this.columnController.pixelWidth(column)
  }

  private minColumnSize(column: TableColumn): number {
    return this.columnController.minSize(column)
  }

  private tanstackRowsForSlots(slots: VisibleRowSlot[]): TanStackTableRow[] {
    return slots.filter((slot): slot is Extract<VisibleRowSlot, { kind: 'row' }> => slot.kind === 'row').map(({ row, index }) => ({
      ...row,
      __absoluteIndex: index,
      __rowKey: rowKey(row, index),
    }))
  }

  private columnsForTanStack(): TableColumn[] {
    return this.columns
  }

  private groupHeaderSegments(headers: any[], force = false): Array<{ label: string; span: number; rowHeader: boolean; column: any }> {
    if (!force && !headers.some((header) => header.column.columnDef.meta?.column?.group)) return []
    const segments: Array<{ label: string; span: number; rowHeader: boolean; column: any }> = []
    for (const header of headers) {
      const column = header.column.columnDef.meta?.column as TableColumn | undefined
      if (!column) continue
      const rowHeader = column.role === 'row_header'
      const label = rowHeader ? '' : column.group || ''
      const previous = segments[segments.length - 1]
      if (previous && previous.label === label && previous.rowHeader === rowHeader) {
        previous.span++
        continue
      }
      segments.push({ label, span: 1, rowHeader, column: header.column })
    }
    return segments
  }

  private tanstackColumnDefs(): Array<ColumnDef<typeof reportTableFeatures, TanStackTableRow, unknown>> {
    return this.columnsForTanStack().map((column) => ({
      id: column.key,
      accessorKey: column.key,
      header: column.label,
      cell: (info: any) => formatCell(info.getValue(), column, this.table.type !== 'table'),
      size: defaultColumnSize(column),
      minSize: this.minColumnSize(column),
      enableResizing: true,
      meta: { align: column.align, column },
    })) as Array<ColumnDef<typeof reportTableFeatures, TanStackTableRow, unknown>>
  }

  private tanstackTable(rows: TanStackTableRow[]) {
    // Keep one stable identity column visible while the rest of a wide table
    // scrolls. Pinning every row-header column can exceed a narrow card's
    // viewport, causing the browser to clamp several sticky cells onto the
    // same right edge.
    const pinnedColumns = this.columnController.pinnedKeys(this.columns, this.columnVisibility)
    const sorting: SortingState = this.table.sort?.key
      ? [{ id: this.table.sort.key, desc: this.table.sort.direction === 'desc' }]
      : []
    return this.tableController.table(
      {
        features: reportTableFeatures,
        columns: this.tanstackColumnDefs(),
        data: rows,
        getRowId: (row: TanStackTableRow) => row.__rowKey,
        getCoreRowModel: createCoreRowModel(),
        manualSorting: true,
        manualFiltering: true,
        manualPagination: true,
        enableRowSelection: true,
        enableMultiRowSelection: true,
        columnResizeMode: 'onEnd',
        renderFallbackValue: '-',
        state: {
          sorting,
          columnVisibility: this.columnVisibility,
          columnSizing: this.columnSizing,
          columnPinning: { left: pinnedColumns, right: [] },
          rowSelection: this.rowSelection,
        },
        onColumnVisibilityChange: (updater: unknown) => {
          this.columnVisibility = applyUpdater(updater, this.columnVisibility)
        },
        onColumnSizingChange: (updater: unknown) => {
          this.columnSizing = applyUpdater(updater, this.columnSizing)
        },
        onRowSelectionChange: () => {},
      } as any,
    ) as any
  }

  handleScroll(event: Event): void {
    const target = event.currentTarget as HTMLDivElement
    this.viewportTop = target.scrollTop
    this.viewportHeight = target.clientHeight
    this.virtualizationController.setViewport(this.viewportTop, this.viewportHeight)
    this.scheduleEnsureBlocksForScroll()
  }

  sortColumn(column: TableColumn): void {
    const current = this.table?.sort ?? defaultSort
    const direction: SortDirection = current.key === column.key
      ? current.direction === 'asc' ? 'desc' : 'asc'
      : defaultDirection(column)
    this.emitBlock('all', 0, { key: column.key, direction }, this.table.resetVersion + 1)
  }

  private resolvedTableId(): string {
    return this.tableId.trim()
  }

  selectCell(row: TableRow, _column: TableColumn, absoluteIndex: number, event: MouseEvent): void {
    const key = rowKey(row, absoluteIndex)
    this.selectRow(key, row, event)
  }

  private selectRow(key: string, row: TableRow, event: MouseEvent): void {
    const selected = this.rowIsSelected(row, key)
    const action = this.selectionController.action(selected, this.selectedRowCount(), event)
    this.selectedCellKey = ''
    this.emitRowSelection(key, row, action)
  }

  private syncSelectedRowFromTableSelection(): void {
    const selection = this.table?.selection ?? []
    if (selection.length === 0) {
      this.clearLocalSelection()
      return
    }
    this.rowSelection = tableRowSelectionFromEntries(
      this.loadedRows.map((item) => ({ row: item.row, key: rowKey(item.row, item.index) })),
      this.table?.interaction,
      selection,
    )
    this.selectedCellKey = ''
  }

  private clearLocalSelection(): void {
    this.selectedCellKey = ''
    this.rowSelection = {}
  }

  private selectedRowCount(): number {
    return this.selectionController.count(this.table?.selection)
  }

  private selectionLabels(): string[] {
    return this.selectionController.labels(this.table?.selection)
  }

  private rowIsSelected(row: TableRow, key: string): boolean {
    return this.selectionController.isSelected(row, key, this.table?.interaction, this.table?.selection)
  }

  private emitRowSelection(key: string, row: TableRow, selectionAction: RowClickSelectionAction): void {
    const command = buildRowSelectionCommand({
      sourceId: this.resolvedTableId(),
      interaction: this.table?.interaction,
      key,
      row,
      selectionAction,
    })
    if (!command) return
    this.dispatchEvent(
      new CustomEvent('lv-interaction-select', {
        bubbles: true,
        composed: true,
        detail: command,
      }),
    )
  }

  private columnPinPosition(column: any): false | 'left' | 'right' {
    return column?.getIsPinned?.() ?? callMemoOrStaticFn(column, 'getIsPinned', column_getIsPinned) ?? false
  }

  private isLastLeftPinnedColumn(column: any): boolean {
    return Boolean(column?.getIsLastColumn?.('left') ?? callMemoOrStaticFn(column, 'getIsLastColumn', column_getIsLastColumn, 'left'))
  }

  private pinnedCellClass(column: any): string {
    const pinPosition = this.columnPinPosition(column)
    if (pinPosition !== 'left') return ''
    return `pinned-left ${this.isLastLeftPinnedColumn(column) ? 'pinned-left-edge' : ''}`
  }

  private pinnedCellStyle(column: any): string {
    if (this.columnPinPosition(column) !== 'left') return ''
    const offset = column.getStart?.('left') ?? callMemoOrStaticFn(column, 'getStart', column_getStart, 'left') ?? 0
    return `--lv-pin-left:${Math.max(0, Number(offset) || 0)}px`
  }

  private beginColumnResize(event: MouseEvent | TouchEvent, header: any): void {
    event.preventDefault()
    event.stopPropagation()
    const clientX = resizeClientX(event)
    const column = header.column.columnDef.meta?.column as TableColumn | undefined
    if (clientX === null || !column) return
    this.resizeDrag = {
      columnKey: column.key,
      startClientX: clientX,
      startSize: this.columnPixelWidth(column),
      minSize: this.minColumnSize(column),
    }
    this.scheduleResizeGuideUpdate(event)
    document.addEventListener('mousemove', this.handleResizeGuideMove)
    document.addEventListener('mouseup', this.handleResizeGuideEnd, { once: true })
    document.addEventListener('touchmove', this.handleResizeGuideMove, { passive: true })
    document.addEventListener('touchend', this.handleResizeGuideEnd, { once: true })
    document.addEventListener('touchcancel', this.handleResizeGuideEnd, { once: true })
  }

  private scheduleResizeGuideUpdate(event: MouseEvent | TouchEvent): void {
    const clientX = resizeClientX(event)
    if (clientX === null) return
    if (this.resizeGuideFrame) cancelAnimationFrame(this.resizeGuideFrame)
    this.resizeGuideFrame = requestAnimationFrame(() => {
      this.resizeGuideFrame = 0
      const plane = this.renderRoot.querySelector<HTMLElement>('.table-plane')
      if (!plane) return
      const scaleX = resizePlaneScaleX(plane)
      this.resizeGuideX = resizeGuideX(plane, clientX)
      if (this.resizeDrag) {
        this.columnSizing = { ...this.columnSizing, [this.resizeDrag.columnKey]: resizedColumnWidth(this.resizeDrag, clientX, scaleX) }
      }
    })
  }

  private clearResizeGuide(): void {
    document.removeEventListener('mousemove', this.handleResizeGuideMove)
    document.removeEventListener('mouseup', this.handleResizeGuideEnd)
    document.removeEventListener('touchmove', this.handleResizeGuideMove)
    document.removeEventListener('touchend', this.handleResizeGuideEnd)
    document.removeEventListener('touchcancel', this.handleResizeGuideEnd)
    if (this.resizeGuideFrame) cancelAnimationFrame(this.resizeGuideFrame)
    this.resizeGuideFrame = 0
    this.resizeDrag = undefined
    this.resizeGuideX = -1
  }

  private resizeColumnByKeyboard(column: TableColumn, event: KeyboardEvent): void {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
    event.preventDefault()
    event.stopPropagation()
    const delta = event.key === 'ArrowRight' ? 16 : -16
    const width = Math.max(this.minColumnSize(column), this.columnPixelWidth(column) + delta)
    this.columnSizing = { ...this.columnSizing, [column.key]: width }
  }

  private renderGroupHeaderRows(headers: any[], force = false) {
    const groupHeaders = this.groupHeaderSegments(headers, force)
    if (!groupHeaders.length) return nothing
    return html`
      <div class="group-head" role="row">
        ${groupHeaders.map((group) => html`
          <div
			class=${`group-cell ${group.rowHeader ? 'row-header' : 'metric-group'} ${this.pinnedCellClass(group.column)}`}
            role="columnheader"
            style=${`grid-column:span ${group.span};${this.pinnedCellStyle(group.column)}`}
          >
            <span class="cell-value">${group.label}</span>
          </div>
        `)}
      </div>
    `
  }

  private renderHeaderRow(headers: any[]) {
    return html`
      <div class="head" role="row">
        ${headers.map((header: any) => {
          const column = header.column.columnDef.meta?.column as TableColumn | undefined
          if (!column) return nothing
          const sorted = this.table?.sort?.key === header.column.id
          const sortMark = this.table?.sort?.direction === 'asc' ? '↑' : '↓'
          return html`
            <div
              class=${`header-cell ${column.role === 'row_header' ? 'row-header' : ''} ${this.pinnedCellClass(header.column)} ${sorted ? 'sorted' : ''}`}
              role="columnheader"
              style=${this.pinnedCellStyle(header.column)}
            >
              <button class="header-button" type="button" title=${column.label} @click=${() => this.sortColumn(column)}>
                <span>${flexRender(header.column.columnDef.header, header.getContext())}</span>
                <span class="sort">${sortMark}</span>
              </button>
              ${header.column.getCanResize?.() ? html`
                <span
                  class=${`column-resizer ${this.resizeDrag?.columnKey === column.key ? 'resizing' : ''}`}
                  role="separator"
                  tabindex="0"
                  aria-label=${`Resize ${column.label} column`}
                  aria-orientation="vertical"
                  aria-valuemin=${this.minColumnSize(column)}
                  aria-valuenow=${this.columnPixelWidth(column)}
                  @keydown=${(event: KeyboardEvent) => this.resizeColumnByKeyboard(column, event)}
                  @mousedown=${(event: MouseEvent) => this.beginColumnResize(event, header)}
                  @touchstart=${(event: TouchEvent) => this.beginColumnResize(event, header)}
                ></span>
              ` : nothing}
            </div>
          `
        })}
      </div>
    `
  }

  private renderSkeletonSegment(headers: any[], index: number) {
    return html`
      <div
        class="row skeleton-row"
        role="row"
        aria-busy="true"
        style=${`top:${index * this.rowHeight}px`}
      >
        ${headers.map((header: any) => {
          const column = header.column.columnDef.meta?.column as TableColumn | undefined
          if (!column) return nothing
          return html`
            <span
              class=${`cell skeleton-cell ${column.role === 'row_header' ? 'row-header' : ''} ${this.pinnedCellClass(header.column)} ${column.align === 'right' ? 'right' : ''}`}
              role="cell"
              style=${this.pinnedCellStyle(header.column)}
            >
              <span class="skeleton-line"></span>
            </span>
          `
        })}
      </div>
    `
  }

  private cellStyle(row: TableRow, column: TableColumn, pinnedColumn: any): string {
    const styles = [this.pinnedCellStyle(pinnedColumn)].filter(Boolean)
    const value = row[column.key]
    const conditional = conditionalCellAppearance(row, column)
    const background = conditional.background ? undefined : this.formattingController.background(value, column)
    if (conditional.background) {
      styles.push(`background-color:${conditional.background}`)
    } else if (background) {
      const percent = this.formattingController.percent(value, background)
      const color = this.formattingController.color(background.highColor || background.background || background.color, 'warning')
      styles.push(`--lv-cell-bg-color:${color}`)
      styles.push(`--lv-cell-bg-fade:${Math.max(66, 92 - Math.round(percent * 0.22))}%`)
    }
    if (conditional.foreground) {
      styles.push(`color:${conditional.foreground}`)
    } else {
      const text = this.formattingController.textColor(value, column)
      if (text?.color) styles.push(`color:${this.formattingController.color(text.color)}`)
    }
    const bar = this.formattingController.dataBar(column)
    if (bar) {
      styles.push(`--lv-cell-bar-width:${this.formattingController.percent(value, bar)}%`)
      styles.push(`--lv-cell-bar-color:${this.formattingController.color(bar.color || bar.highColor || 'accent')}`)
    }
    return styles.join(';')
  }

  private cellClass(column: TableColumn, cellKey: string, row: TableRow, pinnedColumn: any): string {
    const value = row[column.key]
    return [
      'cell',
      column.align === 'right' ? 'right' : '',
      column.role === 'row_header' ? 'row-header' : '',
      this.pinnedCellClass(pinnedColumn),
      cellKey === this.selectedCellKey ? 'active' : '',
      conditionalCellAppearance(row, column).background || this.formattingController.background(value, column) ? 'has-background' : '',
      this.formattingController.dataBar(column) ? 'has-data-bar' : '',
    ].filter(Boolean).join(' ')
  }

  private renderCellValue(row: TableRow, column: TableColumn, formatted: unknown) {
    const value = row[column.key]
    const conditional = conditionalCellAppearance(row, column)
    const cue = conditional.icon
      ? html`<span class="conditional-cue" aria-hidden="true">${conditional.icon}</span><span class="conditional-cue-label">Status: ${conditional.iconLabel}</span>`
      : nothing
    const badge = this.formattingController.badge(column)
    if (badge?.values) {
      const tone = badge.values[String(value)] ?? badge.values[String(value).toLowerCase()]
      if (tone) {
        return html`${cue}<span class=${`cell-badge tone-${this.formattingController.tone(tone)}`}>${formatted}</span>`
      }
    }
    return html`${cue}${formatted}`
  }

  private renderRowSegment(cells: any[], row: TableRow, index: number, key: string) {
    const selected = this.rowIsSelected(row, key)
    const hovered = key === this.hoveredRowId
    const highlightActive = this.table.highlight?.active === true
    const highlighted = row.__lv_highlighted === true
    return html`
      <div
        class=${`row ${selected ? 'selected' : ''} ${hovered ? 'hovered' : ''} ${highlighted ? 'highlighted' : ''} ${highlightActive && !highlighted ? 'highlight-dimmed' : ''}`}
        role="row"
        aria-selected=${selected ? 'true' : 'false'}
        style=${`top:${index * this.rowHeight}px`}
        @mouseenter=${() => { this.hoveredRowId = key }}
        @mouseleave=${() => { if (this.hoveredRowId === key) this.hoveredRowId = '' }}
        @click=${(event: MouseEvent) => this.selectRow(key, row, event)}
      >
        ${cells.map((cell: any) => {
          const column = cell.column.columnDef.meta?.column ?? this.columns.find((item) => item.key === cell.column.id)
          if (!column) return nothing
          const cellKey = `${key}:${cell.column.id}`
          const formatted = flexRender(cell.column.columnDef.cell, cell.getContext())
          return html`
            <div
              class=${this.cellClass(column, cellKey, row, cell.column)}
              role="cell"
              style=${this.cellStyle(row, column, cell.column)}
            >
              <button
                class="cell-action"
                type="button"
                aria-label=${`${column.label}: ${String(row[cell.column.id] ?? '')}`}
                title=${String(row[cell.column.id] ?? '')}
                @click=${(event: MouseEvent) => {
                  event.stopPropagation()
                  this.selectCell(row, column, index, event)
                }}
              >
                ${this.formattingController.dataBar(column) ? html`<span class="cell-data-bar" aria-hidden="true"></span>` : nothing}
                <span class="cell-value">${this.renderCellValue(row, column, formatted)}</span>
              </button>
            </div>
          `
        })}
      </div>
    `
  }

  render() {
    const visibleRows = this.visibleRows
    const tanstack = this.tanstackTable(this.tanstackRowsForSlots(visibleRows))
    const headers = visibleHeaders(tanstack, this.columnVisibility)
    const columns = visibleColumnsFromHeaders(headers, this.columns)
    const columnModels = allTableColumns(tanstack)
    const tanstackRows = new Map((tanstack.getRowModel?.().rows ?? []).map((row: any) => [row.id, row]))
    const totalHeight = this.availableRows * this.rowHeight
    const hasGroupHeaderRow = headers.some((header: any) => header.column.columnDef.meta?.column?.group)
    const rowRange = this.rowRangeText()
    const selectedCount = this.selectedRowCount()
    const hasSelection = selectedCount > 0
    const selectedText = selectedCount === 0 ? 'No selection' : selectedCount === 1 ? '1 row selected' : `${selectedCount} rows selected`
    const loading = Boolean(this.table.loadingBlock) || this.visibleLoading
    const gridTemplate = this.gridTemplateFor(columns)
    const tableWidth = this.tableWidthFor(columns)
    const columnLineOffsets = this.columnLineOffsetsFor(columns)
    const shellStyle = [
      `--lv-table-columns:${gridTemplate}`,
      `--lv-table-width:${tableWidth}px`,
      `--lv-row-height:${this.rowHeight}px`,
      `--lv-group-head-height:${groupHeaderHeight}px`,
      `--lv-head-top:${hasGroupHeaderRow ? groupHeaderHeight : 0}px`,
    ].join(';')
    const style = this.table.style
    const shellClass = [
      'shell',
      `density-${style.density}`,
      `grid-${style.grid}`,
      style.zebra ? 'zebra' : '',
    ].filter(Boolean).join(' ')

    return html`
      <section class=${shellClass} style=${shellStyle}>
        ${this.table.highlight?.announcement ? html`<span class="conditional-cue-label" aria-live="polite">${this.table.highlight.announcement}</span>` : nothing}
        <div class="toolbar">
          <div class="toolbar-title">
            <h2>${this.table?.title ?? 'Orders'}</h2>
          </div>
          <div class="visual-actions">
            <slot name="agent-action"></slot>
            <button class="icon-action" type="button" aria-label="Expand table" title="Expand table" @click=${() => this.runAction('focus')}>${visualMenuIcon('focus')}</button>
            <details class="visual-options">
              <summary aria-label="Visual options" title="Visual options">${lucideIcon(EllipsisVertical)}</summary>
              <div class="menu" role="menu">
                <button type="button" role="menuitem" @click=${() => this.runAction('show-data')}>${visualMenuIcon('show-data')}<span>Show data</span></button>
                <button type="button" role="menuitem" @click=${() => this.runAction('copy-data')}>${visualMenuIcon('copy-data')}<span>Copy data</span></button>
                <button type="button" role="menuitem" @click=${() => this.runAction('export-csv')}>${visualMenuIcon('export-csv')}<span>Export CSV</span></button>
                <button type="button" role="menuitem" ?disabled=${!hasSelection} @click=${() => this.runAction('clear-selection')}>${visualMenuIcon('clear-selection')}<span>Clear selection</span></button>
                <div class="menu-divider"></div>
                <div class="column-menu" @click=${(event: Event) => event.stopPropagation()}>
                  <span>Columns</span>
                  ${columnModels.map((column: any) => {
                    const checked = columnIsVisible(column, this.columnVisibility)
                    return html`
                      <label>
                        <input
                          type="checkbox"
                          aria-label=${column.columnDef.header}
                          .checked=${checked}
                          ?disabled=${!columnCanHide(column) || checked && columns.length <= 1}
                          @change=${columnVisibilityHandler(column, (next) => {
                            this.columnVisibility = { ...this.columnVisibility, [column.id]: next }
                          })}
                        />
                        ${column.columnDef.header}
                      </label>
                    `
                  })}
                </div>
              </div>
            </details>
          </div>
        </div>
        ${this.table?.error ? html`<div class="error" role="status" aria-live="polite">${this.table.error}</div>` : nothing}
        <div class="table-frame">
          ${loading ? html`<div class="loading" aria-hidden="true"></div>` : nothing}
          <div class="table-scrollport" role="table" aria-label=${this.table?.title ?? 'Orders'} tabindex="0" ${ref(this.bodyViewportRef)} @scroll=${this.handleScroll}>
            <div class="table-plane">
              ${this.resizeGuideX >= 0 ? html`<span class="resize-guide" style=${`--lv-resize-guide-x:${this.resizeGuideX}px`}></span>` : nothing}
              ${this.renderGroupHeaderRows(headers)}
              ${this.renderHeaderRow(headers)}
              ${this.availableRows === 0 && !loading ? html`<div class="empty">Waiting for table data</div>` : html`
                <div class="canvas" role="rowgroup" style=${`height:${totalHeight}px`}>
                  <div class="grid-lines" aria-hidden="true">
                    ${columnLineOffsets.map((offset) => html`<span class="grid-line" style=${`left:${offset}`}></span>`)}
                  </div>
                  ${visibleRows.map((slot) => {
                    if (slot.kind === 'skeleton') return this.renderSkeletonSegment(headers, slot.index)
                    const key = rowKey(slot.row, slot.index)
                    const tanstackRow = tanstackRows.get(key)
                    return this.renderRowSegment(tanstackRow ? visibleCellsForRow(tanstackRow, this.columnVisibility) : [], slot.row, slot.index, key)
                  })}
                </div>
              `}
            </div>
          </div>
        </div>
        <div class="footer">
          <span><strong>${rowRange}</strong>${this.visibleLoading ? html` · loading` : nothing}${this.table.isCapped ? html` · browsing first ${this.table.rowCap.toLocaleString()}` : nothing}</span>
          <span>${selectedText}</span>
        </div>
      </section>
    `
  }

  private ensureBlocksForScroll(): void {
    if (this.availableRows <= 0) return
    const currentStart = Math.floor(Math.floor(this.viewportTop / this.rowHeight) / this.chunkSize) * this.chunkSize
    const desired = this.desiredStarts(currentStart)
    const desiredSet = new Set(desired)
    const loadedStarts = new Set(blockIDs.map((id) => this.blocks[id]?.start ?? -1))
    const expectedStarts = new Set([...this.expectedBlocks.values()].map((request) => request.start))
    const missingStarts = desired.filter((start) => !loadedStarts.has(start) && !expectedStarts.has(start))

    if (missingStarts.length > 1 || !loadedStarts.has(currentStart) && !expectedStarts.has(currentStart)) {
      this.scheduleJumpBlock(currentStart)
      return
    }

    this.clearJumpTimer()
    const usedBlocks = new Set<BlockID>()

    for (const start of missingStarts) {
      const block = this.reusableBlock(desiredSet, usedBlocks)
      if (!block) continue
      usedBlocks.add(block)
      this.emitBlock(block, start, this.table.sort, this.table.resetVersion)
    }
  }

  private scheduleEnsureBlocksForScroll(): void {
    if (this.scrollFrame) return
    this.scrollFrame = requestAnimationFrame(() => {
      this.scrollFrame = 0
      this.ensureBlocksForScroll()
    })
  }

  private scheduleJumpBlock(start: number): void {
    this.pendingJumpStart = start
    this.requestUpdate()
    // Keep one bounded trailing request alive while fast scrolling updates the
    // destination. Restarting the timer for every crossed chunk can postpone
    // loading indefinitely until scrolling stops.
    if (this.jumpTimer) return
    this.jumpTimer = window.setTimeout(() => {
      this.jumpTimer = 0
      this.emitBlock('all', this.pendingJumpStart, this.table.sort, this.table.resetVersion)
    }, 75)
  }

  private clearJumpTimer(): void {
    if (!this.jumpTimer) return
    clearTimeout(this.jumpTimer)
    this.jumpTimer = 0
  }

  private desiredStarts(currentStart: number): number[] {
    return this.virtualizationController.desiredStarts(currentStart, this.availableRows, this.chunkSize)
  }

  private reusableBlock(desiredStarts: Set<number>, usedBlocks: Set<BlockID>): BlockID | undefined {
    return blockIDs.find((id) => !usedBlocks.has(id) && !desiredStarts.has(this.blocks[id]?.start ?? -1))
      ?? blockIDs.find((id) => !usedBlocks.has(id))
  }

  private emitBlock(block: BlockID | 'all', start: number, sort = this.table.sort, resetVersion = this.table.resetVersion): void {
    const tableId = this.resolvedTableId()
    if (!tableId) return
    const count = this.chunkSize
    const requestSeq = ++this.requestSeq
    if (block === 'all') {
      this.expectedBlocks.clear()
      const starts = this.allBlockStarts(start)
      blockIDs.forEach((id, index) => {
        const expectedStart = starts[index]
        this.expectedBlocks.set(id, { start: expectedStart, requestSeq, resetVersion, sort })
      })
    } else {
      this.expectedBlocks.set(block, { start, requestSeq, resetVersion, sort })
    }
    this.requestUpdate()
    this.dispatchEvent(new CustomEvent<VisualWindowCommand>('lv-visual-window-change', {
      bubbles: true,
      composed: true,
      detail: {
        visual: tableId,
        block,
        start,
        count,
        requestSeq,
        sort,
        resetVersion,
      },
    }))
  }

  private allBlockStarts(start: number): number[] {
    return this.virtualizationController.allBlockStarts(start, this.chunkSize)
  }

  private rowRangeText(): string {
	return this.virtualizationController.rowRangeText(this.table, this.availableRows, this.rowHeight)
  }

  private mergeIncomingBlocks(): void {
    const defaults = emptyBlocks()
    for (const id of blockIDs) {
      const incoming = this.table.blocks[id]
      if (!incoming) continue
      if (!this.shouldAcceptBlock(id, incoming)) continue
      const defaultBlock = defaults[id]
      const carriesRows = incoming.rows.length > 0
      const carriesNonDefaultStart = incoming.start !== defaultBlock.start
      const cacheIsEmpty = this.blockCache[id].rows.length === 0
      if (carriesRows || carriesNonDefaultStart || cacheIsEmpty) {
        this.blockCache[id] = { ...incoming, rows: incoming.rows }
      }

      // A matching response fulfils the request even when it contains no rows.
      // Empty windows are valid after a filter reduces a deeply scrolled table;
      // keeping them pending leaves the table's loading indicator stuck forever.
      if (incoming.requestSeq > 0) this.latestAcceptedSeq.set(id, incoming.requestSeq)
      const expected = this.expectedBlocks.get(id)
      if (expected && this.blockMatchesExpected(incoming, expected)) {
        this.expectedBlocks.delete(id)
      }
    }
  }

  private shouldAcceptBlock(id: BlockID, incoming: TableBlock): boolean {
    const expected = this.expectedBlocks.get(id)
    if (expected) return this.blockMatchesExpected(incoming, expected)

    if (incoming.requestSeq > 0) {
      const lastAcceptedSeq = this.latestAcceptedSeq.get(id) ?? 0
      return incoming.requestSeq >= lastAcceptedSeq
        && incoming.resetVersion === this.table.resetVersion
        && sameSort(incoming.sort, this.table.sort)
    }

    return incoming.resetVersion === 0
      || incoming.resetVersion === this.table.resetVersion
      && sameSort(incoming.sort, this.table.sort)
  }

  private blockMatchesExpected(block: TableBlock, expected: ExpectedBlockRequest): boolean {
    return block.start === expected.start
      && block.requestSeq === expected.requestSeq
      && block.resetVersion === expected.resetVersion
      && sameSort(block.sort, expected.sort)
  }

  private runAction(action: VisualAction): void {
    const tableId = this.resolvedTableId()
    this.renderRoot.querySelector<HTMLDetailsElement>('.visual-options')?.removeAttribute('open')
    if (action === 'clear-selection') {
      if (tableId) {
        this.dispatchEvent(
          new CustomEvent('lv-interaction-select', {
            bubbles: true,
            composed: true,
            detail: {
              sourceKind: 'visual',
              sourceId: tableId,
              interactionKind: this.table?.interaction?.kind || 'row_selection',
              action: 'clear',
              toggle: this.table?.interaction?.toggle !== false,
              mappings: [],
            },
          }),
        )
      }
    }
    this.dispatchEvent(
      new CustomEvent('lv-visual-action', {
        bubbles: true,
        composed: true,
        detail: {
          action,
          visualType: 'table',
          visualId: tableId,
          title: this.table?.title ?? 'Orders',
          columns: this.columns,
          rows: this.exportRows(),
          selection: this.selectionLabels(),
          table: {
            ...(this.table ?? emptyTable),
            blocks: this.blocks,
            rows: this.exportRows(),
            columns: this.columns,
          },
        },
      }),
    )
  }

  private exportRows(): TableRow[] {
    return this.loadedRows.map(({ row }) => {
      const next: TableRow = {}
      for (const column of this.columns) {
        next[column.key] = formatCell(row[column.key], column, this.table.type !== 'table')
      }
      return next
    })
  }
}

if (!customElements.get('lv-report-table')) customElements.define('lv-report-table', ReportTable)
