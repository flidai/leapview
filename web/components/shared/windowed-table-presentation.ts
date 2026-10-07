import { html } from 'lit'
import { formatValue } from '../dashboard/visualization/format'
import type { WindowedTableColumn, WindowedTablePayload } from './windowed-table'

export function defaultColumnWidth(column: WindowedTableColumn, blocks: Required<WindowedTablePayload>['blocks']): number {
  if (Number.isFinite(column.width) && Number(column.width) > 0) return Number(column.width)
  if (isTemporalColumn(column)) {
    // Reserve enough room for the full displayed timestamp, including precision and offset.
    const lengths = Object.values(blocks).flatMap(block => block?.rows.map(row => cellLabel(row[column.key], column).length) ?? [])
    return Math.max(168, (Math.max(20, ...lengths) * 9) + 24)
  }
  if (column.align === 'right') return 128
  if (column.key.length > 24) return 240
  return 168
}

export function renderCell(value: unknown, column: WindowedTableColumn) {
  const text = cellLabel(value, column)
  if (value == null || value === '') return html`<span class="muted" aria-label="No value">-</span>`
  if (typeof value === 'number') return html`<span>${text}</span>`
  return html`<code>${text}</code>`
}

function isTemporalColumn(column: WindowedTableColumn): boolean {
  return /^(date|datetime|timestamp|timestamptz)(?:\b|_)/i.test(column.type || '')
}

export function cellLabel(value: unknown, column: WindowedTableColumn): string {
  if (value == null || value === '') return '-'
  if (column.format) {
    try { return formatValue('en-US', column.format, value) } catch { /* Preserve readable raw values if a format cannot represent them. */ }
  }
  if (typeof value === 'string' && isTemporalColumn(column)) {
    // Keep the source date order, time, precision and timezone; only separate
    // ISO date and time for readability.
    return value.replace(/^(\d{4}-\d{2}-\d{2})T(?=\d{2}:\d{2}:\d{2})/, '$1 ')
  }
  if (typeof value === 'object') return JSON.stringify(value)
  return String(value)
}

