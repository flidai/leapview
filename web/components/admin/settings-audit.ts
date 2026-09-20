import { LitElement, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure } from '../shared/command-failure'
import { settingsSurfaceStyles } from './settings-surfaces.styles'
import type { AuditEventSignal, AuditLogFilters, AuditLogSignal } from '../../generated/signals'
import '../shared/drawer'
import '../shared/record-table'

type DatastarFetchOwnerDetail = { type?: string; el?: Element }

function ownsAdminActionFetch(element: Element, event: Event): boolean {
  const owner = (event as CustomEvent<DatastarFetchOwnerDetail>).detail?.el
  if (!(owner instanceof Element)) return false
  const root = element.getRootNode()
  const rootHost = 'host' in root ? (root as ShadowRoot).host : null
  const actionHost = rootHost instanceof Element ? rootHost.closest('lv-admin-page') ?? rootHost : element.closest('lv-admin-page')
  return owner === actionHost
}

function auditFilterOptions(items: AuditEventSignal[], key: 'action' | 'resourceKind', allLabel: string, currentValue = '') {
  const values = Array.from(new Set([...items.map((item) => item[key]), currentValue].filter(Boolean))).sort((left, right) => left.localeCompare(right))
  return [{ value: '', label: allLabel }, ...values.map((value) => ({ value, label: humanizeAuditValue(value) }))]
}

function humanizeAuditValue(value?: string): string {
  const normalized = (value || '')
    .replace(/([a-z0-9])([A-Z])/g, '$1 $2')
    .replace(/[._-]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
    .toLowerCase()
  return normalized ? normalized[0].toUpperCase() + normalized.slice(1) : '—'
}

function formatAuditTimestamp(value: string): string {
  if (!value) return 'Unknown time'
  const timestamp = new Date(value)
  if (Number.isNaN(timestamp.valueOf())) return value
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(timestamp)
}

function formatAuditTableTimestamp(value: string): string {
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

function auditActorLabel(event: AuditEventSignal): string {
  return event.principalName?.trim() || event.principalEmail?.trim() || (event.principalId ? 'Unknown user' : 'System')
}

function auditActorDescription(event: AuditEventSignal): string {
  const email = event.principalEmail?.trim() || ''
  return email && email !== auditActorLabel(event) ? email : ''
}

function auditResourceDescription(event: AuditEventSignal): string {
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

function auditStatusCell(status?: string) {
  const normalized = (status || '').toLowerCase()
  if (!normalized) return { label: '—', tone: 'muted' as const, icon: 'dot' as const }
  if (normalized === 'success' || normalized === 'succeeded' || normalized === 'ok') return { label: 'Succeeded', tone: 'success' as const, icon: 'check' as const }
  if (normalized === 'failure' || normalized === 'failed' || normalized === 'error') return { label: 'Failed', tone: 'danger' as const, icon: 'x' as const }
  if (normalized === 'pending' || normalized === 'queued' || normalized === 'running') return { label: humanizeAuditValue(status), tone: 'attention' as const, icon: 'clock' as const }
  return { label: humanizeAuditValue(status), tone: 'muted' as const, icon: 'dot' as const }
}

type AuditPresetID = 'security' | 'access' | 'credentials' | 'failed'

type AuditPreset = {
  id: AuditPresetID
  label: string
  description: string
  filters: AuditLogFilters
}

const auditPresets: AuditPreset[] = [
  { id: 'security', label: 'Security', description: 'Principal and account security events', filters: { resourceKind: 'principal' } },
  { id: 'access', label: 'Role changes', description: 'Recorded role-binding changes', filters: { resourceKind: 'role_binding' } },
  { id: 'credentials', label: 'Service accounts', description: 'Service-account and credential events', filters: { resourceKind: 'service_principal' } },
  // AuditLogFilters deliberately has no status field. Failed events are therefore
  // narrowed in the already-loaded rows while the command remains contract-valid.
  { id: 'failed', label: 'Failed events', description: 'Failed events in the loaded result set', filters: {} },
]

function auditPresetItems(signal: AuditLogSignal, preset: AuditPresetID | ''): AuditEventSignal[] {
  if (preset !== 'failed') return signal.items
  return signal.items.filter((event) => {
    const status = (event.status || '').toLowerCase()
    return status === 'failure' || status === 'failed' || status === 'error'
  })
}

function auditPrincipalHref(principalID?: string): string {
  return principalID ? `/admin/principals/${encodeURIComponent(principalID)}` : ''
}

function auditResourceHref(event: AuditEventSignal): string {
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

function auditEventSummary(event: AuditEventSignal): string {
  const actor = auditActorLabel(event)
  const action = humanizeAuditValue(event.action).toLowerCase()
  const resourceDescription = auditResourceDescription(event)
  const resource = event.resourceKind
    ? ` on ${humanizeAuditValue(event.resourceKind)}${resourceDescription ? ` ${resourceDescription}` : ''}`
    : ''
  return `${actor} ${action}${resource}`
}

function auditMetadataText(event: AuditEventSignal): string {
  if (!event.metadata || Object.keys(event.metadata).length === 0) return '{}'
  try {
    return JSON.stringify(event.metadata, null, 2)
  } catch {
    return String(event.metadata)
  }
}

function auditTable(signal: AuditLogSignal, items = signal.items) {
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

class LeapViewAuditLog extends DatastarLit(LitElement) {
  static styles = settingsSurfaceStyles
  @state() private busy = false
  @state() private commandError = ''
  @state() private selectedEventID = ''
  @state() private activePreset: AuditPresetID | '' = ''
  @state() private copiedAuditValue = ''
  private pendingSignalKey = ''

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    super.disconnectedCallback()
  }

  get audit(): AuditLogSignal { return this.signal('adminAuditLog', { items: [], filters: {}, loadedCount: 0, loading: false, hasMore: false }) }
  override updated(): void {
    const key = JSON.stringify(this.audit)
    if (this.busy && key !== this.pendingSignalKey) this.busy = false
    this.pendingSignalKey = key
  }

  private emit(detail: Record<string, unknown>) {
    this.commandError = ''
    this.busy = true
    this.pendingSignalKey = JSON.stringify(this.audit)
    this.dispatchEvent(new CustomEvent('lv-audit-log-command', { bubbles: true, composed: true, detail }))
  }

  private handleDatastarFetch = (event: Event): void => {
    if (!this.busy) return
    const detail = (event as CustomEvent<DatastarFetchOwnerDetail>).detail
    if (!ownsAdminActionFetch(this, event)) return
    if (detail?.type === 'finished') {
      // Filtering or loading an empty page can legitimately return the same
      // signal payload. Transport completion still marks the command done.
      this.busy = false
      return
    }
    const failure = browserCommandFailure(event, 'Audit log update')
    if (!failure) return
    this.busy = false
    this.commandError = failure.message
  }

  private filterSelect(key: 'action' | 'resourceKind', value: string): void {
    if (this.busy) return
    this.activePreset = ''
    this.emit({ action: 'filter', filters: { ...this.audit.filters, [key]: value } })
  }

  private applyPreset(preset: AuditPreset): void {
    if (this.busy) return
    this.activePreset = preset.id
    this.emit({ action: 'filter', filters: { ...preset.filters } })
  }

  private submitFilter(event: SubmitEvent): void {
    event.preventDefault()
    const form = event.currentTarget as HTMLFormElement
    const value = (name: string) => (form.elements.namedItem(name) as HTMLInputElement)?.value.trim() || ''
    this.activePreset = ''
    this.emit({
      action: 'filter',
      filters: {
        projectId: value('projectId'),
        principalId: value('principalId'),
        action: this.audit.filters?.action || '',
        resourceKind: this.audit.filters?.resourceKind || '',
        resourceId: value('resourceId'),
        from: value('from'),
        to: value('to'),
      },
    })
  }

  private handleRecordTableAction = (event: CustomEvent<{ action?: string; row?: { id?: string } }>): void => {
    if (event.detail?.action !== 'open' || !event.detail.row?.id) return
    this.selectedEventID = String(event.detail.row.id)
    this.copiedAuditValue = ''
  }

  private closeEventDrawer = (): void => {
    this.selectedEventID = ''
    this.copiedAuditValue = ''
  }

  private async copyAuditValue(value: string): Promise<void> {
    if (!value) return
    try {
      await navigator.clipboard?.writeText(value)
      this.copiedAuditValue = value
    } catch {
      this.copiedAuditValue = ''
    }
  }

  private renderCopyButton(value: string): unknown {
    if (!value) return nothing
    return html`<button class="audit-drawer-copy" type="button" @click=${() => this.copyAuditValue(value)}>${this.copiedAuditValue === value ? 'Copied' : 'Copy'}</button>`
  }

  private renderAuditIdentifier(label: string, value: string, href = '') {
    if (!value) return html`<span class="muted">—</span>`
    return html`${href ? html`<a href=${href}>${value}</a>` : html`<code>${value}</code>`}${this.renderCopyButton(value)}`
  }

  private renderAuditDrawer(event: AuditEventSignal) {
    const status = auditStatusCell(event.status)
    return html`<lv-drawer
      open
      size="wide"
      label="Audit event details"
      .modal=${false}
      @lv-drawer-close=${this.closeEventDrawer}
    >
      <div slot="title" class="audit-drawer-title">
        <h2>${humanizeAuditValue(event.action)}</h2>
        <p>${auditEventSummary(event)}</p>
      </div>
      <div class="audit-drawer-body">
        <section class="audit-drawer-section" aria-label="Event summary">
          <h3>Event summary</h3>
          <dl class="audit-drawer-facts">
            <div class="audit-drawer-fact"><dt>Time</dt><dd><time datetime=${event.createdAt || nothing}>${formatAuditTimestamp(event.createdAt)}</time></dd></div>
            <div class="audit-drawer-fact"><dt>Exact timestamp</dt><dd><code data-audit-exact-timestamp>${event.createdAt || '—'}</code>${this.renderCopyButton(event.createdAt)}</dd></div>
            <div class="audit-drawer-fact"><dt>Event ID</dt><dd>${this.renderAuditIdentifier('Event ID', event.id)}</dd></div>
            <div class="audit-drawer-fact"><dt>Actor</dt><dd>${event.principalId ? html`<a href=${auditPrincipalHref(event.principalId)}>${auditActorLabel(event)}</a>${event.principalEmail ? html`<span class="muted">${event.principalEmail}</span>` : nothing}` : html`<span>System</span>`}</dd></div>
            ${event.principalId ? html`<div class="audit-drawer-fact"><dt>Actor ID</dt><dd>${this.renderAuditIdentifier('Actor ID', event.principalId)}</dd></div>` : nothing}
            <div class="audit-drawer-fact"><dt>Capability</dt><dd>${humanizeAuditValue(event.capability)}</dd></div>
            <div class="audit-drawer-fact"><dt>Status</dt><dd><span class=${`audit-drawer-status audit-drawer-status-${status.tone}`}>${status.label}</span></dd></div>
          </dl>
        </section>
        <section class="audit-drawer-section" aria-label="Resource details">
          <h3>Resource</h3>
          <dl class="audit-drawer-facts">
            <div class="audit-drawer-fact"><dt>Resource type</dt><dd>${humanizeAuditValue(event.resourceKind)}</dd></div>
            <div class="audit-drawer-fact"><dt>Resource ID</dt><dd>${this.renderAuditIdentifier('Resource ID', event.resourceId, auditResourceHref(event))}</dd></div>
            ${event.projectId ? html`<div class="audit-drawer-fact"><dt>Project ID</dt><dd>${this.renderAuditIdentifier('Project ID', event.projectId)}</dd></div>` : nothing}
            ${event.requestId ? html`<div class="audit-drawer-fact"><dt>Request ID</dt><dd>${this.renderAuditIdentifier('Request ID', event.requestId)}</dd></div>` : nothing}
            ${event.correlationId ? html`<div class="audit-drawer-fact"><dt>Correlation ID</dt><dd>${this.renderAuditIdentifier('Correlation ID', event.correlationId)}</dd></div>` : nothing}
          </dl>
        </section>
        <details class="audit-drawer-metadata">
          <summary>Raw metadata</summary>
          <pre><code>${auditMetadataText(event)}</code></pre>
        </details>
      </div>
    </lv-drawer>`
  }

  render() {
    const signal = this.audit
    const filters = signal.filters || {}
    const disabled = this.busy || signal.loading
    const actionOptions = auditFilterOptions(signal.items, 'action', 'All actions', filters.action)
    const resourceKindOptions = auditFilterOptions(signal.items, 'resourceKind', 'All resource kinds', filters.resourceKind)
    const selectedEvent = signal.items.find((event) => event.id === this.selectedEventID)
    const visibleItems = auditPresetItems(signal, this.activePreset)
    const loadedLabel = signal.loading
      ? 'Loading audit events…'
      : this.activePreset === 'failed'
        ? `${visibleItems.length} failed events in ${signal.loadedCount || signal.items.length} loaded`
        : signal.items.length ? `${signal.loadedCount || signal.items.length} events loaded` : 'No audit events'
    return html`<section class="surface audit-surface" aria-label="Audit log">
      ${signal.error ? html`<p class="error" role="alert">${signal.error}</p>` : nothing}
      ${this.commandError ? html`<p class="error" role="alert">${this.commandError}</p>` : nothing}
      <div class="audit-presets" aria-label="Audit log quick filters">
        <span class="audit-presets-label">Quick filters</span>
        ${auditPresets.map((preset) => html`<button
          class="audit-preset"
          type="button"
          title=${preset.description}
          aria-pressed=${this.activePreset === preset.id ? 'true' : 'false'}
          data-audit-preset=${preset.id}
          ?disabled=${disabled}
          @click=${() => this.applyPreset(preset)}
        >${preset.label}</button>`)}
      </div>
      <form class="audit-toolbar" aria-label="Audit log filters" @submit=${this.submitFilter}>
        <label class="audit-filter">Project <span class="audit-filter-note">(server-bound)</span><input id="audit-project-id" type="search" aria-label="Project" aria-describedby="audit-project-help" name="projectId" .value=${filters.projectId || ''} ?disabled=${disabled} readonly title="The server selects the active project" placeholder="Project ID"><small id="audit-project-help">The active project is selected by the server and cannot be changed here.</small></label>
        <label class="audit-filter">Actor<input id="audit-principal-id" type="search" aria-label="Actor" name="principalId" .value=${filters.principalId || ''} ?disabled=${disabled} placeholder="Principal ID"></label>
        <div class="audit-filter"><span>Action</span><lv-select-menu label="Action" .options=${actionOptions} .value=${filters.action || ''} ?disabled=${disabled} @lv-select-change=${(event: CustomEvent<{ value: string }>) => this.filterSelect('action', event.detail.value)}></lv-select-menu></div>
        <div class="audit-filter"><span>Resource kind</span><lv-select-menu label="Resource kind" .options=${resourceKindOptions} .value=${filters.resourceKind || ''} ?disabled=${disabled} @lv-select-change=${(event: CustomEvent<{ value: string }>) => this.filterSelect('resourceKind', event.detail.value)}></lv-select-menu></div>
        <label class="audit-filter">Resource ID<input id="audit-resource-id" type="search" aria-label="Resource ID" name="resourceId" .value=${filters.resourceId || ''} ?disabled=${disabled} placeholder="Resource ID"></label>
        <label class="audit-filter">From<input id="audit-from" type="date" aria-label="From" name="from" .value=${filters.from || ''} ?disabled=${disabled}></label>
        <label class="audit-filter">To<input id="audit-to" type="date" aria-label="To" name="to" .value=${filters.to || ''} ?disabled=${disabled}></label>
        <div class="audit-actions"><button class="primary" type="submit" ?disabled=${disabled}>Filter</button><button type="button" ?disabled=${disabled} @click=${() => { this.activePreset = ''; this.emit({ action: 'clear', filters: {} }) }}>Clear</button></div>
      </form>
      <div class="audit-results">
        <lv-record-table variant="compact" .table=${auditTable(signal, visibleItems)} @lv-record-table-action=${this.handleRecordTableAction}></lv-record-table>
        <div class="audit-footer" aria-live="polite">
          <span>${loadedLabel}</span>
          ${signal.hasMore ? html`<button class="audit-load-more" type="button" ?disabled=${disabled} @click=${() => this.emit({ action: 'load_more', filters, pageToken: signal.nextCursor })}>${signal.loading ? 'Loading…' : 'Load more'}</button>` : nothing}
        </div>
      </div>
      ${selectedEvent ? this.renderAuditDrawer(selectedEvent) : nothing}
    </section>`
  }
}

if (!customElements.get('lv-audit-log')) customElements.define('lv-audit-log', LeapViewAuditLog)

export { LeapViewAuditLog }
