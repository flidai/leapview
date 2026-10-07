type RecordCellTone = 'default' | 'accent' | 'success' | 'attention' | 'danger' | 'muted'
export type RecordStatusIcon = 'check' | 'x' | 'clock' | 'dot'

export type RecordCell = {
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
  additions?: number
  deletions?: number
}

export type RecordColumn = {
  id: string
  header: string
  kind?: 'text' | 'code' | 'expression' | 'badge' | 'status' | 'query' | 'diff' | 'number' | 'link' | 'tags' | 'entity' | 'button' | 'actions'
  align?: 'left' | 'center' | 'right'
  hrefKey?: string
  width?: string
  sortable?: boolean
  toggleable?: boolean
  mobileHidden?: boolean
}

export type RecordRow = Record<string, unknown>

export function cellLabel(value: unknown): string {
  if (value == null || value === '') return '-'
  if (typeof value === 'object' && 'label' in value) {
    const label = (value as RecordCell).label ?? (value as RecordCell).value
    return label == null || label === '' ? '-' : String(label)
  }
  return String(value)
}

export function cellDescription(value: unknown): string {
  return typeof value === 'object' && value && 'description' in value ? String((value as RecordCell).description ?? '') : ''
}

export function cellHref(column: RecordColumn, value: unknown, row: RecordRow): string {
  if (typeof value === 'object' && value && 'href' in value) return String((value as RecordCell).href ?? '')
  return column.hrefKey ? cellLabel(row[column.hrefKey]) : ''
}

export function cellIcon(value: unknown): string {
  return typeof value === 'object' && value && 'icon' in value ? String((value as RecordCell).icon ?? '') : ''
}

export function cellIconTreatment(value: unknown): 'plain' | 'framed' {
  if (typeof value === 'object' && value && 'iconTreatment' in value) {
    return (value as RecordCell).iconTreatment ?? 'framed'
  }
  return 'framed'
}

export function cellTone(value: unknown): RecordCellTone {
  if (typeof value === 'object' && value && 'tone' in value) {
    return (value as RecordCell).tone ?? 'default'
  }
  return 'default'
}

export function cellAction(value: unknown): string {
  return typeof value === 'object' && value && 'action' in value ? String((value as RecordCell).action ?? '') : ''
}

export function statusIcon(value: unknown, label: string): RecordStatusIcon {
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

export function sortPrimitive(value: unknown): string | number {
  if (typeof value === 'number') return value
  if (typeof value === 'object' && value && 'value' in value && typeof (value as RecordCell).value === 'number') {
    return (value as RecordCell).value as number
  }
  return cellLabel(value).toLowerCase()
}
