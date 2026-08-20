import { virtualRowRange } from '../../shared/table-window'
import {
  rowClickSelectionAction,
  rowIsSelected,
  selectedRowCount,
  selectionLabels,
  type RowClickSelectionAction,
} from './selection'
import { defaultChunkSize, defaultRowHeight, type TableColumn, type TableFormattingRule, type TableRow, type TableSignal } from './types'

export type ReportTableViewport = {
  top: number
  height: number
}

export class ReportTableVirtualizationController {
  private viewport: ReportTableViewport = { top: 0, height: 0 }

  setViewport(top: number, height: number): ReportTableViewport {
    this.viewport = { top: Math.max(0, top), height: Math.max(0, height) }
    return this.state
  }

  get state(): ReportTableViewport {
    return { ...this.viewport }
  }

  visibleRange(availableRows: number, rowHeight: number, overscan = 2): { first: number; last: number } {
    if (availableRows <= 0) return { first: 0, last: 0 }
    return virtualRowRange(
      availableRows,
      this.viewport.top,
      this.viewport.height || rowHeight,
      Math.max(1, rowHeight),
      overscan,
    )
  }

  desiredStarts(currentStart: number, availableRows: number, chunkSize: number): number[] {
    const size = Math.max(1, chunkSize || defaultChunkSize)
    const starts = currentStart <= 0
      ? [0, size, size * 2]
      : [Math.max(0, currentStart - size), currentStart, currentStart + size]
    return starts.filter((start, index, all) => start < availableRows && all.indexOf(start) === index)
  }

  allBlockStarts(start: number, chunkSize: number): number[] {
    const size = Math.max(1, chunkSize || defaultChunkSize)
    const currentStart = Math.max(0, Math.floor(start / size) * size)
    if (currentStart <= 0) return [0, size, size * 2]
    return [Math.max(0, currentStart - size), currentStart, currentStart + size]
  }

  rowRangeText(table: TableSignal, availableRows: number, rowHeight: number): string {
    if (!availableRows) return table.cardinality.kind === 'exact' ? 'No rows' : 'No loaded rows'
    const firstIndex = Math.min(availableRows - 1, Math.max(0, Math.floor(this.viewport.top / Math.max(1, rowHeight))))
    const visibleRows = Math.max(1, Math.ceil((this.viewport.height || rowHeight) / Math.max(1, rowHeight)))
    const lastIndex = Math.min(availableRows, firstIndex + visibleRows)
    const total = table.cardinality.value.toLocaleString()
    const cardinality = table.cardinality.kind === 'exact' ? total
      : table.cardinality.kind === 'estimated' ? `~${total}`
        : table.cardinality.kind === 'lower_bound' ? `${total}+` : total
    return `${(firstIndex + 1).toLocaleString()}-${lastIndex.toLocaleString()} of ${cardinality}`
  }
}

export class ReportTableSelectionController {
  action(selected: boolean, selectedCount: number, event: Pick<MouseEvent, 'metaKey' | 'ctrlKey'>): RowClickSelectionAction {
    return rowClickSelectionAction({
      selected,
      selectedCount,
      metaKey: event.metaKey,
      ctrlKey: event.ctrlKey,
    })
  }

  isSelected(row: TableRow, key: string, interaction: TableSignal['interaction'], selection: TableSignal['selection']): boolean {
    return rowIsSelected(row, key, interaction, selection)
  }

  count(selection: TableSignal['selection']): number {
    return selectedRowCount(selection)
  }

  labels(selection: TableSignal['selection']): string[] {
    return selectionLabels(selection)
  }
}

export class ReportTableColumnController {
  constructor(private readonly sizing: () => Record<string, number>) {}

  defaultSize(column: TableColumn): number {
    const configuredWidth = Number(column.width)
    if (Number.isFinite(configuredWidth) && configuredWidth > 0) return configuredWidth
    const widths: Record<string, number> = {
      order_id: 240,
      purchase_date: 126,
      status: 128,
      state: 78,
      category: 210,
      revenue: 130,
      review_score: 104,
      delivery_days: 108,
    }
    if (widths[column.key]) return widths[column.key]
    if (column.align === 'right') return 120
    return 140
  }

  minSize(column: TableColumn): number {
    return column.key === 'order_id' || column.key === 'category' ? 160 : 64
  }

  pixelWidth(column: TableColumn): number {
    return Math.max(this.minSize(column), this.sizing()[column.key] ?? this.defaultSize(column))
  }

  pixelWidths(columns: TableColumn[]): number[] {
    return columns.map((column) => this.pixelWidth(column))
  }

  tableWidth(columns: TableColumn[]): number {
    return this.pixelWidths(columns).reduce((sum, size) => sum + size, 0)
  }

  lineOffsets(columns: TableColumn[]): number[] {
    let offset = 0
    return this.pixelWidths(columns).slice(0, -1).map((width) => {
      offset += width
      return offset
    })
  }
}

/** Formatting policy used by cells; all CSS tokens remain server-governed. */
export class ReportTableFormattingController {
  tone(value: string | undefined, fallback = 'accent'): string {
    return String(value || fallback).toLowerCase().replace(/[^a-z0-9_-]/g, '') || fallback
  }

  color(value: string | undefined, fallback = 'accent'): string {
    switch (this.tone(value, fallback)) {
      case 'success':
      case 'green': return 'var(--lv-fg-success)'
      case 'danger':
      case 'red': return 'var(--lv-fg-danger)'
      case 'warning':
      case 'yellow': return 'var(--lv-fg-warning)'
      case 'muted':
      case 'gray': return 'var(--lv-fg-muted)'
      default: return 'var(--lv-fg-link)'
    }
  }

  percent(value: unknown, rule: TableFormattingRule): number {
    const next = Number(value)
    if (!Number.isFinite(next)) return 0
    const min = typeof rule.min === 'number' ? rule.min : 0
    const max = typeof rule.max === 'number' && rule.max > min ? rule.max : Math.max(min + 1, next)
    return Math.max(0, Math.min(100, ((next - min) / (max - min)) * 100))
  }

  badge(column: TableColumn): TableFormattingRule | undefined {
    return column.formatting?.find((rule) => rule.kind === 'badge')
  }

  dataBar(column: TableColumn): TableFormattingRule | undefined {
    return column.formatting?.find((rule) => rule.kind === 'data_bar')
  }

  background(value: unknown, column: TableColumn): TableFormattingRule | undefined {
    return column.formatting?.find((rule) => rule.kind === 'background_scale' && this.matches(value, rule))
  }

  textColor(value: unknown, column: TableColumn): TableFormattingRule | undefined {
    return column.formatting?.find((rule) => rule.kind === 'text_color' && this.matches(value, rule))
  }

  private matches(value: unknown, rule: TableFormattingRule): boolean {
    const next = Number(value)
    if (!Number.isFinite(next)) return false
    if (typeof rule.min === 'number' && next < rule.min) return false
    if (typeof rule.max === 'number' && next > rule.max) return false
    return true
  }
}

export const reportTableDefaults = {
  rowHeight: defaultRowHeight,
  chunkSize: defaultChunkSize,
}
