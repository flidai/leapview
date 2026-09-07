export type VisualDataAction = 'copy-data' | 'export-csv'

export type VisualDataActionNotice = Readonly<{
  dataStatus?: string
  truncated?: boolean
}>

export type VisualDataSummary = Readonly<VisualDataActionNotice & {
  rows?: readonly unknown[]
  totalRows?: number
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
