import type { AuditEventSignal, AuditLogFilters, AuditLogSignal } from '../../generated/signals'

export function auditFilterOptions(items: AuditEventSignal[], key: 'action' | 'resourceKind', allLabel: string, currentValue = '') {
  const values = Array.from(new Set([...items.map((item) => item[key]), currentValue].filter(Boolean))).sort((left, right) => left.localeCompare(right))
  return [{ value: '', label: allLabel }, ...values.map((value) => ({ value, label: humanizeAuditValue(value) }))]
}

export function humanizeAuditValue(value?: string): string {
  const normalized = (value || '')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/[._-]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
    .toLowerCase()
  return normalized ? normalized[0].toUpperCase() + normalized.slice(1) : '—'
}

export function formatAuditTimestamp(value: string): string {
  if (!value) return 'Unknown time'
  const timestamp = new Date(value)
  if (Number.isNaN(timestamp.valueOf())) return value
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(timestamp)
}

export function formatAuditTableTimestamp(value: string): string {
  if (!value) return 'Unknown time'
  const timestamp = new Date(value)
  if (Number.isNaN(timestamp.valueOf())) return value
  const now = new Date()
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  const sameDay = (left: Date, right: Date) => left.getFullYear() === right.getFullYear()
    && left.getMonth() === right.getMonth()
    && left.getDate() === right.getDate()
  const time = new Intl.DateTimeFormat(undefined, { hour: 'numeric', minute: '2-digit' }).format(timestamp)
  if (sameDay(timestamp, now)) return `Today, ${time}`
  if (sameDay(timestamp, yesterday)) return `Yesterday, ${time}`
  const date = new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    ...(timestamp.getFullYear() === now.getFullYear() ? {} : { year: 'numeric' as const }),
  }).format(timestamp)
  return `${date}, ${time}`
}

export function auditActorLabel(event: AuditEventSignal): string {
  return event.principalName?.trim() || event.principalEmail?.trim() || (event.principalId ? 'Unknown user' : 'System')
}

export function auditActorDescription(event: AuditEventSignal): string {
  const email = event.principalEmail?.trim() || ''
  return email && email !== auditActorLabel(event) ? email : ''
}

export function auditResourceDescription(event: AuditEventSignal): string {
  if (!event.resourceId?.trim()) return ''
  switch ((event.resourceKind || '').trim().toLowerCase()) {
    case 'agent_tool':
    case 'tool':
    case 'dashboard':
    case 'model':
    case 'semantic_model':
    case 'project':
    case 'publication':
      return humanizeAuditValue(event.resourceId)
    default:
      return ''
  }
}

export function auditStatusCell(status?: string) {
  const normalized = (status || '').toLowerCase()
  if (!normalized) return { label: '—', tone: 'muted' as const, icon: 'dot' as const }
  if (normalized === 'success' || normalized === 'succeeded' || normalized === 'ok') return { label: 'Succeeded', tone: 'success' as const, icon: 'check' as const }
  if (normalized === 'failure' || normalized === 'failed' || normalized === 'error') return { label: 'Failed', tone: 'danger' as const, icon: 'x' as const }
  if (normalized === 'pending' || normalized === 'queued' || normalized === 'running') return { label: humanizeAuditValue(status), tone: 'attention' as const, icon: 'clock' as const }
  return { label: humanizeAuditValue(status), tone: 'muted' as const, icon: 'dot' as const }
}

export type AuditPresetID = 'security' | 'access' | 'credentials' | 'failed'

export type AuditPreset = {
  id: AuditPresetID
  label: string
  description: string
  filters: AuditLogFilters
}

export const auditPresets: AuditPreset[] = [
  { id: 'security', label: 'Security', description: 'Principal and account security events', filters: { resourceKind: 'principal' } },
  { id: 'access', label: 'Role changes', description: 'Recorded role-binding changes', filters: { resourceKind: 'role_binding' } },
  { id: 'credentials', label: 'Service accounts', description: 'Service-account and credential events', filters: { resourceKind: 'service_principal' } },
  // AuditLogFilters deliberately has no status field. Failed events are therefore
  // narrowed in the already-loaded rows while the command remains contract-valid.
  { id: 'failed', label: 'Failed events', description: 'Failed events in the loaded result set', filters: {} },
]

export function auditPresetItems(signal: AuditLogSignal, preset: AuditPresetID | ''): AuditEventSignal[] {
  if (preset !== 'failed') return signal.items
  return signal.items.filter((event) => {
    const status = (event.status || '').toLowerCase()
    return status === 'failure' || status === 'failed' || status === 'error'
  })
}

export function auditPrincipalHref(principalID?: string): string {
  return principalID ? `/admin/principals/${encodeURIComponent(principalID)}` : ''
}

export function auditResourceHref(event: AuditEventSignal): string {
  if (!event.resourceId) return ''
  switch ((event.resourceKind || '').toLowerCase()) {
    case 'principal':
    case 'user':
      return `/admin/principals/${encodeURIComponent(event.resourceId)}`
    case 'group':
      return `/admin/groups/${encodeURIComponent(event.resourceId)}`
    default:
      return ''
  }
}

export function auditEventSummary(event: AuditEventSignal): string {
  const actor = auditActorLabel(event)
  const action = humanizeAuditValue(event.action).toLowerCase()
  const resourceDescription = auditResourceDescription(event)
  const resource = event.resourceKind
    ? ` on ${humanizeAuditValue(event.resourceKind)}${resourceDescription ? ` ${resourceDescription}` : ''}`
    : ''
  return `${actor} ${action}${resource}`
}

export function auditMetadataText(event: AuditEventSignal): string {
  if (!event.metadata || Object.keys(event.metadata).length === 0) return '{}'
  try {
    return JSON.stringify(event.metadata, null, 2)
  } catch {
    return String(event.metadata)
  }
}

export function auditTable(signal: AuditLogSignal, items = signal.items) {
  return {
    columns: [
      { id: 'time', header: 'Time', kind: 'entity' as const, width: '180px' },
      { id: 'action', header: 'Action', kind: 'badge' as const, width: '190px' },
      { id: 'actor', header: 'Actor', kind: 'entity' as const, width: '180px', mobileHidden: true },
      { id: 'resource', header: 'Resource', kind: 'entity' as const, width: '220px', mobileHidden: true },
      { id: 'capability', header: 'Capability', width: '150px', mobileHidden: true },
      { id: 'status', header: 'Status', kind: 'status' as const, width: '120px', mobileHidden: true },
    ],
    rows: items.map((event) => ({
      id: event.id,
      time: { label: formatAuditTableTimestamp(event.createdAt) },
      action: { label: humanizeAuditValue(event.action), tone: 'accent' as const },
      actor: event.principalId
        ? { label: auditActorLabel(event), description: auditActorDescription(event), href: event.principalName || event.principalEmail ? auditPrincipalHref(event.principalId) : '' }
        : { label: 'System', description: 'Automated operation' },
      resource: { label: humanizeAuditValue(event.resourceKind), description: auditResourceDescription(event), href: auditResourceHref(event) },
      capability: humanizeAuditValue(event.capability),
      status: auditStatusCell(event.status),
    })),
    empty: signal.loading && !signal.items.length ? 'Loading audit events…' : 'No audit events match these filters.',
    minWidth: '720px',
    density: 'tight' as const,
    rowAction: 'open',
  }
}
