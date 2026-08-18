import type { TableColumn, TableRow } from './types'
import { formatValue } from '../visualization/format'

export function formatCell(value: unknown, column: TableColumn): string {
	if (column.visualizationFormat) {
		// TanStack represents null pivot cells with an empty/display placeholder
		// while creating the cell context. Normalize only those known placeholders;
		// numeric strings still fail the typed visualization contract.
		if (value === null || value === undefined || value === '' || value === '-' || value === '—') return '—'
		try {
			return formatValue('en-US', column.visualizationFormat, value)
		} catch (error) {
			const message = error instanceof Error ? error.message : String(error)
			const valueKind = value === column.key ? 'column-key string'
				: value === column.label ? 'column-label string'
					: value === '-' || value === '—' || value === '' ? 'display-placeholder string'
						: typeof value === 'string' && value.trim() !== '' && Number.isFinite(Number(value)) ? 'numeric string'
							: value === null ? 'null' : typeof value
			throw new Error(`table column ${JSON.stringify(column.key)} cannot format ${valueKind}: ${message}`)
		}
	}
  if (value === null || value === undefined || value === '') return '-'
  const format = column.format || inferredFormat(column)
  if (format === 'currency' && Number.isFinite(Number(value))) {
    return `R$ ${Number(value).toLocaleString(undefined, { maximumFractionDigits: 2 })}`
  }
  if (format === 'integer' && Number.isFinite(Number(value))) {
    return Number(value).toLocaleString(undefined, { maximumFractionDigits: 0 })
  }
  if (format === 'decimal' && Number.isFinite(Number(value))) {
    return Number(value).toFixed(2)
  }
  if (format === 'days' && Number.isFinite(Number(value))) {
    return `${Number(value)}d`
  }
  if (Number.isFinite(Number(value)) && column.align === 'right') {
    return Number(value).toLocaleString(undefined, { maximumFractionDigits: 2 })
  }
  return String(value)
}

function inferredFormat(column: TableColumn): string {
	if (column.key === 'revenue' || column.metric === 'revenue') return 'currency'
  if (column.key === 'review_score') return 'decimal'
  if (column.key === 'delivery_days') return 'days'
  return ''
}

export function defaultDirection(column: TableColumn): 'asc' | 'desc' {
  return ['revenue', 'review_score', 'delivery_days', 'purchase_date'].includes(column.key) || column.role === 'metric' ? 'desc' : 'asc'
}

export function rowKey(row: TableRow, fallback: number): string {
  const id = row.order_id
  if (typeof id === 'string' && id) return id
  const rowID = row.__rowKey
  if (typeof rowID === 'string' && rowID) return rowID
  return String(fallback)
}
