import { html } from 'lit'
import { CheckCircle2, Clock3, XCircle } from 'lucide'
import type { AdminContentSectionSignal, AdminPageSignal, AdminPublicationSignal, AdminQueryDetailSignal, AdminQueryHistoryFilters, AdminStorageSignal, FilterMenuCommand, FilterMenuSignal, RecordTableSignal } from '../../generated/signals'
import type { EntityListColumn, EntityListFilter, EntityListItem } from '../shared/entity-list'

export const emptyStorage: AdminStorageSignal = {
  summary: {
    totalDataSizeLabel: '',
    tableCount: 0,
    dataFileCount: 0,
  },
  status: '',
  tables: [],
}

export const storageColumns: EntityListColumn[] = [
  { id: 'name', label: 'Name', width: '155px' },
  { id: 'schema', label: 'Schema', width: '85px' },
  { id: 'type', label: 'Type', width: '60px', render: 'quiet-status' },
  { id: 'rows', label: 'Rows', width: '85px', align: 'right' as const },
  { id: 'columns', label: 'Columns', width: '70px', align: 'right' as const },
  { id: 'files', label: 'Files', width: '55px', align: 'right' as const },
  { id: 'size', label: 'Data size', width: '85px', align: 'right' as const },
  { id: 'snapshot', label: 'Snapshot', width: '75px', align: 'right' as const },
]

export const publicationColumns: EntityListColumn[] = [
  { id: 'name', label: 'Publication', width: '28%' },
  { id: 'project', label: 'Project', width: '18%' },
  { id: 'dashboard', label: 'Dashboard', width: '34%' },
  { id: 'status', label: 'Status', width: '20%', render: 'status' },
]

export const queryFailedStatuses = new Set(['error', 'failed', 'failure', 'timeout', 'canceled', 'cancelled'])
export const queryRunningStatuses = new Set(['running', 'queued', 'pending'])
export const queryHistoryColumnsStorageKey = 'leapview-admin-query-events-columns'

export function publicationKey(publication: Pick<AdminPublicationSignal, 'projectId' | 'name'>): string {
  return `${publication.projectId}/${publication.name}`
}

export function publicationStatusLabel(status: string): string {
  const normalized = status.trim().toLowerCase()
  if (!normalized) return 'Unknown'
  return normalized.charAt(0).toUpperCase() + normalized.slice(1)
}

export function publicationListItem(publication: AdminPublicationSignal): EntityListItem {
  return {
    id: publicationKey(publication),
    title: publication.name,
    description: publication.defaultPage ? `${publication.dashboard} / ${publication.defaultPage}` : publication.dashboard,
    icon: 'dashboard',
    iconTreatment: 'plain',
    columns: {
      project: publication.projectId,
      dashboard: publication.dashboard,
      status: publicationStatusLabel(publication.status),
    },
    sortValues: {
      project: publication.projectId,
      dashboard: publication.dashboard,
      status: publication.status,
    },
  }
}

export function publicationFact(label: string, value: string) {
  return html`<div class="publication-drawer-fact"><dt>${label}</dt><dd><code>${value}</code></dd></div>`
}

export function isPersonalSettings(active: string): boolean {
  return active === 'profile' || active === 'security' || active === 'api-tokens' || active === 'api-token-new' || active === 'archived-chats'
}

export function isProductSettings(active: string): boolean {
  return active === 'general' || active === 'authentication' || active === 'system'
}

export const emptyQueryHistoryTable: RecordTableSignal = {
  columns: [],
  rows: [],
  empty: 'No query events match these filters.',
}

export const emptyQueryDetail: AdminQueryDetailSignal = {
  eventId: '',
  loading: false,
  error: '',
  connectionWaitMs: 0,
  durationMs: 0,
  databaseMs: 0,
  planningMs: 0,
  rowsReturned: 0,
}

export function tableRows(table: RecordTableSignal | undefined | null): Array<Record<string, unknown>> {
  return Array.isArray(table?.rows) ? table.rows as Array<Record<string, unknown>> : []
}

export function queryHistoryPresentation(table: RecordTableSignal): RecordTableSignal {
  return {
    ...table,
    columnSelector: table.columnSelector ? {
      ...table.columnSelector,
      storageKey: table.columnSelector.storageKey || queryHistoryColumnsStorageKey,
    } : undefined,
    columns: table.columns.map((column) => ({
      ...column,
      mobileHidden: column.id !== 'query' && column.id !== 'started_at',
    })),
    rows: tableRows(table).map((row) => {
      const startedAt = recordValueLabel(row.started_at)
      const timestamp = adminTimestamp(startedAt)
      return {
        ...row,
        started_at: startedAt
          ? { label: formatQueryHistoryDate(startedAt), value: timestamp || startedAt }
          : { label: 'Unknown', value: '' },
      }
    }),
  }
}

export function storageFilters(tables: AdminStorageSignal['tables']): EntityListFilter[] {
  const schemas = Array.from(new Set(tables.map((table) => table.schema || 'default'))).sort((left, right) => left.localeCompare(right))
  return [{ id: 'all', label: 'All schemas' }, ...schemas.map((schema) => ({ id: schema, label: schema }))]
}

export function storageTypeLabel(type: string): string {
  return type.trim().toLowerCase() === 'view' ? 'View' : 'Table'
}

export function queryDateInputValue(value: string | undefined, end = false): string {
  if (!value) return ''
  const timestamp = adminTimestamp(value)
  if (!timestamp) return value.slice(0, 10)
  const date = new Date(timestamp)
  if (end && date.getUTCHours() === 0 && date.getUTCMinutes() === 0 && date.getUTCSeconds() === 0 && date.getUTCMilliseconds() === 0) date.setUTCDate(date.getUTCDate() - 1)
  return date.toISOString().slice(0, 10)
}

export function queryFilterSummaryChips(filters: AdminQueryHistoryFilters, menus: FilterMenuSignal[]): string[] {
  const chips: string[] = []
  for (const menu of menus) {
    const key = queryFilterKey(menu.id)
    const selected = key ? filters[key] ?? menu.selected ?? [] : menu.selected ?? []
    if (!selected.length) continue
    const labels = selected.map((value) => menu.options?.find((option) => option.value === value)?.label || value)
    chips.push(`${menu.label}: ${labels.join(', ')}`)
  }
  if (filters.search?.trim()) chips.push(`Search: ${filters.search.trim()}`)
  if (filters.target?.trim()) chips.push(`Target: ${filters.target.trim()}`)
  if (filters.from || filters.to) {
    const from = queryDateInputValue(filters.from)
    const to = queryDateInputValue(filters.to, true)
    chips.push(`Date: ${from || 'Any'} – ${to || 'Any'}`)
  }
  return chips
}

export function queryFiltersAfterMenuCommand(filters: AdminQueryHistoryFilters, command: FilterMenuCommand): AdminQueryHistoryFilters {
  const key = queryFilterKey(command.menuId)
  const selected = command.action === 'clear'
    ? []
    : command.action === 'toggle'
      ? toggleQueryFilterValue(key ? filters[key] : undefined, command.value || '')
      : command.selected ?? []
  const next = { ...filters }
  if (key) next[key] = selected
  return next
}

export type QueryMultiFilterKey = 'projects' | 'principals' | 'surfaces' | 'kinds' | 'statuses'

export function queryFilterKey(menuID: string | undefined): QueryMultiFilterKey | '' {
  switch (menuID) {
    case 'project': return 'projects'
    case 'principal': return 'principals'
    case 'surface': return 'surfaces'
    case 'kind': return 'kinds'
    case 'status': return 'statuses'
    default: return ''
  }
}

export function toggleQueryFilterValue(values: string[] | undefined, value: string): string[] {
  const selected = [...(values ?? [])]
  if (!value) return selected
  const index = selected.indexOf(value)
  if (index >= 0) selected.splice(index, 1)
  else selected.push(value)
  return selected
}

export function formatQueryHistoryDate(value: string): string {
  const timestamp = adminTimestamp(value)
  if (!timestamp) return value
  return new Intl.DateTimeFormat(undefined, {
    month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit',
  }).format(timestamp)
}

export function adminGroupTable(page: AdminPageSignal): RecordTableSignal | undefined {
  return page.sections?.find((section) => section.title === 'Groups')?.table
}

export function adminPrincipalListItems(page: AdminPageSignal) {
  return (page.directoryList?.items ?? []).map((item) => ({
    id: item.id,
    title: item.name,
    description: item.username,
    href: item.href,
    avatarUrl: item.avatarUrl,
    icon: 'user',
    columns: {
      email: item.email || '—',
      status: item.status === 'inactive' ? 'Inactive' : 'Active',
      teams: `${item.groupCount} ${item.groupCount === 1 ? 'team' : 'teams'}`,
      joined: formatAdminListDate(item.joinedAt),
      lastSeen: formatAdminLastSeen(item.lastSeenAt),
    },
    columnTitles: {
      lastSeen: item.lastSeenAt ? formatAdminExactDate(item.lastSeenAt) : '',
    },
    sortValues: {
      lastSeen: adminTimestamp(item.lastSeenAt),
    },
  }))
}

export function adminPrincipalListColumns() {
  return [
    { id: 'name', label: 'Name', width: '27%' },
    { id: 'email', label: 'Email', width: '22%' },
    { id: 'status', label: 'Status', width: '14%' },
    { id: 'teams', label: 'Teams', width: '12%' },
    { id: 'joined', label: 'Joined', width: '12%' },
    { id: 'lastSeen', label: 'Last seen', width: '13%' },
  ]
}

export function adminPrincipalListFilters() {
  return [
    { id: 'all', label: 'All' },
    { id: 'active', label: 'Active' },
    { id: 'inactive', label: 'Inactive' },
  ]
}

export function formatAdminListDate(value: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' }).format(date)
}

export function formatAdminLastSeen(value: string): string {
  const timestamp = adminTimestamp(value)
  if (!timestamp) return 'Never'
  const elapsed = Math.max(0, Date.now() - timestamp)
  if (elapsed < 60_000) return 'Now'
  if (elapsed < 60 * 60_000) return `${Math.floor(elapsed / 60_000)}m ago`
  if (elapsed < 24 * 60 * 60_000) return `${Math.floor(elapsed / (60 * 60_000))}h ago`
  if (elapsed < 7 * 24 * 60 * 60_000) return `${Math.floor(elapsed / (24 * 60 * 60_000))}d ago`
  return formatAdminListDate(value)
}

export function formatAdminExactDate(value: string): string {
  const timestamp = adminTimestamp(value)
  if (!timestamp) return ''
  return new Intl.DateTimeFormat('en-US', {
    year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', second: '2-digit',
    timeZone: 'UTC', timeZoneName: 'short',
  }).format(timestamp)
}

export function adminTimestamp(value: string): number {
  if (!value) return 0
  const timestamp = Date.parse(value)
  return Number.isNaN(timestamp) ? 0 : timestamp
}

export function adminGroupListItems(page: AdminPageSignal) {
  return tableRows(adminGroupTable(page)).map((row) => {
    const name = recordValueLabel(row.name) || recordValueLabel(row.id) || 'Unnamed group'
    const provider = recordValueLabel(row.provider)
    const externalID = recordValueLabel(row.external_id)
    const memberCount = Number(row.member_count ?? 0)
    const roles = Array.isArray(row.roles) ? row.roles.map(String).join(', ') : recordValueLabel(row.roles)
    return {
      id: name,
      title: name,
      href: recordValueHref(row.name) || recordValueLabel(row.name_href) || '#',
      icon: 'group',
      iconTreatment: 'plain' as const,
      category: provider.toLowerCase(),
      columns: {
        provider: provider || '—',
        externalID: externalID || '—',
        roles: roles || '—',
        members: String(memberCount),
      },
    }
  })
}

export function adminGroupListColumns() {
  return [
    { id: 'name', label: 'Name', width: '30%' },
    { id: 'provider', label: 'Provider', width: '16%' },
    { id: 'externalID', label: 'External ID', width: '23%' },
    { id: 'roles', label: 'Roles', width: '21%' },
    { id: 'members', label: 'Members', width: '10%', align: 'right' as const },
  ]
}

export function adminGroupListFilters(page: AdminPageSignal) {
  const providers = page.listFilterOptions?.length
    ? page.listFilterOptions
    : Array.from(new Set(adminGroupListItems(page).map((item) => item.category).filter(Boolean))).sort()
  return [{ id: 'all', label: 'All' }, ...providers.map((provider) => ({ id: provider, label: provider.charAt(0).toUpperCase() + provider.slice(1) }))]
}

export function recordValueLabel(value: unknown): string {
  if (value && typeof value === 'object') {
    const cell = value as { label?: unknown; value?: unknown }
    return String(cell.label ?? cell.value ?? '')
  }
  return String(value ?? '')
}

export function recordValueHref(value: unknown): string {
  if (!value || typeof value !== 'object') return ''
  return String((value as { href?: unknown }).href ?? '')
}

export function queryDetailObjectLabel(event: AdminQueryDetailSignal): string {
  const object = [event.objectType, event.objectId].filter(Boolean).join(':')
  if (object) return object
  return [event.modelId, event.target].filter(Boolean).join(':') || '-'
}

export function queryDetailSummary(event: AdminQueryDetailSignal): string {
  const target = [event.modelId, event.target].filter(Boolean).join('.')
  const parts = [target, event.operation, event.queryKind]
  const summary = parts.filter(Boolean).join(' · ')
  return summary ? `${summary}${event.durationMs ? ` · ${event.durationMs} ms` : ''}` : event.eventId || 'Query details'
}

export function queryEventStatusTone(status: string): string {
  switch (status) {
    case 'success':
      return 'success'
    case 'canceled':
      return 'muted'
    case 'timeout':
      return 'attention'
    default:
      return 'danger'
  }
}

export function queryEventStatusIcon(status: string): string {
  switch (status) {
    case 'success':
      return 'check'
    case 'canceled':
    case 'timeout':
      return 'clock'
    default:
      return 'x'
  }
}

export function queryEventStatusIconComponent(status: string): any {
  switch (queryEventStatusIcon(status)) {
    case 'check':
      return CheckCircle2
    case 'clock':
      return Clock3
    default:
      return XCircle
  }
}

export function queryEventStatusLabel(status: string): string {
  switch (status) {
    case 'success':
      return 'Finished'
    case 'canceled':
      return 'Canceled'
    case 'timeout':
      return 'Timeout'
    default:
      return status || 'Error'
  }
}

export function queryDetailFact(label: string, value: string | number | undefined | null) {
  return html`
    <div class="query-detail-fact">
      <span>${label}</span>
      <code>${value == null || value === '' ? '-' : String(value)}</code>
    </div>
  `
}

export function formatQueryJSON(value: string): string {
  try {
    return JSON.stringify(JSON.parse(value), null, 2)
  } catch {
    return value
  }
}

export function renderSection(section: AdminContentSectionSignal, detail = false) {
  return html`
    <section class=${detail ? 'section detail-section' : 'section'} aria-label=${section.title}>
      <h2>${section.title}</h2>
      ${section.table?.columns?.length
        ? html`<div class="panel table-panel"><lv-record-table variant="compact" .table=${section.table}></lv-record-table></div>`
        : detail
          ? html`<dl class="facts">${section.facts?.map((fact) => html`
              <div class="fact">
                <dt>${fact.label}</dt>
                <dd>${fact.value || '-'}</dd>
              </div>
            `)}</dl>`
          : html`<div class="card-facts">${section.facts?.map((fact) => html`
              <div class="metric">
                <span class="label">${fact.label}</span>
                <span class="value">${fact.value || '-'}</span>
              </div>
            `)}</div>`}
    </section>
  `
}
