export type VisualDataAction = 'copy-data' | 'export-csv'

export type VisualDataActionNotice = Readonly<{
  dataStatus?: string
  truncated?: boolean
}>

export type VisualDataSummary = Readonly<VisualDataActionNotice & {
  rows?: readonly unknown[]
  totalRows?: number
}>

export type VisualDataDelimitedDetail = Readonly<{
  columns?: readonly Readonly<{ key: string; label: string }>[]
  rows?: readonly Readonly<Record<string, unknown>>[]
}>

export function visualDataActionNotice(detail: VisualDataActionNotice, action: VisualDataAction): string {
  const outcome = action === 'copy-data' ? 'Copied visual data.' : 'Downloaded CSV.'
  const status = detail.dataStatus?.trim()
  if (status) return `${outcome} ${status}`
  if (detail.truncated) return `${outcome} This is a bounded accessible preview; source data may be partial or truncated.`
  return outcome
}

export function visualDataSummary(detail: VisualDataSummary): string {
  const rows = detail.rows ?? []
  const totalRows = detail.totalRows ?? rows.length
  if (detail.dataStatus) return detail.dataStatus
  const shown = `${rows.length.toLocaleString()} row${rows.length === 1 ? '' : 's'}`
  const total = totalRows === rows.length ? shown : `${shown} of ${totalRows.toLocaleString()}`
  return `${total} from current visual data${detail.truncated ? ' (accessible preview)' : ''}`
}

/**
 * Serializes the bounded visual preview for clipboard TSV or downloaded CSV.
 * String cells and headers that could be interpreted as spreadsheet formulas are
 * prefixed with an apostrophe; numeric values retain their native sign/value.
 */
export function visualDataToDelimited(detail: VisualDataDelimitedDetail, delimiter: ',' | '\t'): string {
  const columns = detail.columns ?? []
  const rows = detail.rows ?? []
  return [
    columns.map((column) => escapeCell(column.label, delimiter)).join(delimiter),
    ...rows.map((row) => columns.map((column) => escapeCell(row[column.key], delimiter)).join(delimiter)),
  ].join('\n')
}

function escapeCell(value: unknown, delimiter: ',' | '\t'): string {
  const text = neutralizeFormula(value)
  if (delimiter === '\t') return text.replace(/[\t\r\n]/g, ' ')
  if (!/[",\r\n]/.test(text)) return text
  return `"${text.replace(/"/g, '""')}"`
}

function neutralizeFormula(value: unknown): string {
  const text = stringValue(value)
  if (typeof value === 'string' && /^[\s\p{Cc}]*[=+\-@]/u.test(text)) return `'${text}`
  return text
}

function stringValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  return String(value)
}
