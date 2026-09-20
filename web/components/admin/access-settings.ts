import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import type { AccessSettingsSignal } from '../../generated/signals'
import { browserCommandFailure } from '../shared/command-failure'
import { DatastarLit } from '../shared/datastar-lit'

type DatastarFetchOwnerDetail = { type?: string; el?: Element }

const styles = css`
  :host { display: block; color: var(--lv-fg-default); font: var(--lv-type-body); font-family: var(--fontStack-system); }
  .surface, .detail-subsection { display: grid; gap: 12px; min-width: 0; }
  h2, h3, p { margin: 0; }
  h2 { font: var(--lv-type-section-title); }
  h3 { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
  .muted, .technical-details { color: var(--lv-fg-muted); }
  .technical-details { font: var(--lv-type-caption); }
  .technical-details summary { cursor: pointer; }
  .error, .danger { color: var(--lv-fg-danger); }
  .notice, .state-banner { border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); padding: var(--base-size-12); }
  .state-banner.error { border-color: var(--lv-border-danger); background: var(--lv-bg-danger-muted); }
  .form { display: flex; flex-wrap: wrap; align-items: end; gap: 8px; }
  label { display: grid; gap: var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  button, input, select { box-sizing: border-box; min-height: var(--lv-control-small); border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-bg-control); color: inherit; padding: var(--base-size-4) var(--base-size-8); font: inherit; }
  button { cursor: pointer; }
  button[disabled] { cursor: default; opacity: .55; }
  .primary { border-color: var(--lv-button-accent-border-rest); background: var(--lv-button-accent-bg-rest); color: var(--lv-button-accent-fg-rest); }
  .table-wrap { overflow-x: auto; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); }
  table { width: 100%; min-width: 620px; border-collapse: collapse; }
  th, td { padding: var(--base-size-8) var(--base-size-12); text-align: left; border-bottom: var(--lv-border-muted); vertical-align: top; }
  th { color: var(--lv-fg-muted); font: var(--lv-type-caption); text-transform: uppercase; letter-spacing: .03em; }
  tbody tr:last-child td { border-bottom: 0; }
  .actions { display: flex; flex-wrap: wrap; gap: 6px; }
  .empty { padding: var(--base-size-20) var(--base-size-12); color: var(--lv-fg-muted); }
  code { overflow-wrap: anywhere; }
`

const emptyAccessSettings: AccessSettingsSignal = {
  projectId: '', policyRevision: 0, policyDigest: '', roleBindings: [], roles: [],
  effectiveAccess: { decisions: [], loading: true },
  grantAdministrationAvailable: false,
  grantAdministrationLabel: 'Grant administration unavailable in this surface.',
  loading: true,
}

function settingsState(error: string): { kind: 'unauthorized' | 'degraded' | 'stale' | 'error'; label: string } | null {
  const normalized = error.toLowerCase()
  if (!normalized) return null
  if (normalized.includes('forbidden') || normalized.includes('not permitted') || normalized.includes('permission')) return { kind: 'unauthorized', label: 'You are not authorized to inspect or change this project access policy.' }
  if (normalized.includes('stale') || normalized.includes('conflict') || normalized.includes('newer change')) return { kind: 'stale', label: 'This access policy is stale. Reload the current policy before retrying.' }
  if (normalized.includes('unavailable') || normalized.includes('generation') || normalized.includes('evidence')) return { kind: 'degraded', label: 'Access evidence is degraded. The page may not include every effective authority.' }
  return { kind: 'error', label: 'Access settings could not be loaded. No mutation was applied.' }
}

function authorityDescription(authority: string): string {
  switch (authority.toLowerCase()) {
    case 'direct': return 'A directly assigned project role.'
    case 'group-derived': return 'Inherited from a group membership; remove the group role to revoke it.'
    case 'owner': return 'Ownership authority for this resource.'
    case 'platform': return 'Instance-wide administrator authority.'
    case 'compiled': return 'Immutable serving-generation evidence; not editable here.'
    case 'denied': return 'No applicable authority was found.'
    default: return 'Authority source reported by the active policy.'
  }
}

function humanize(value: string): string {
  const normalized = value.replace(/[._-]+/g, ' ').trim()
  return normalized ? normalized[0].toUpperCase() + normalized.slice(1) : '—'
}

function subjectLabel(subjectType: string): string {
  return subjectType.toLowerCase() === 'group' ? 'Group' : subjectType.toLowerCase() === 'principal' ? 'Person' : humanize(subjectType)
}

function formValue(form: HTMLFormElement, name: string): string {
  return (form.elements.namedItem(name) as HTMLInputElement | HTMLSelectElement | null)?.value.trim() || ''
}

function ownsAdminActionFetch(element: Element, event: Event): boolean {
  const owner = (event as CustomEvent<DatastarFetchOwnerDetail>).detail?.el
  if (!(owner instanceof Element)) return false
  const root = element.getRootNode()
  const rootHost = 'host' in root ? (root as ShadowRoot).host : null
  const actionHost = rootHost instanceof Element ? rootHost.closest('lv-admin-page') ?? rootHost : element.closest('lv-admin-page')
  return owner === actionHost
}

class LeapViewAccessSettings extends DatastarLit(LitElement) {
  static styles = styles
  @state() private busy = false
  @state() private commandError = ''
  private pendingSignalKey = ''

  override connectedCallback(): void { super.connectedCallback(); document.addEventListener('datastar-fetch', this.handleDatastarFetch) }
  override disconnectedCallback(): void { document.removeEventListener('datastar-fetch', this.handleDatastarFetch); super.disconnectedCallback() }
  get settings(): AccessSettingsSignal { return this.signal('adminAccessSettings', emptyAccessSettings) }
  override updated(): void { const key = JSON.stringify(this.settings); if (this.busy && key !== this.pendingSignalKey) this.busy = false; this.pendingSignalKey = key }

  private emit(detail: Record<string, unknown>): void {
    this.commandError = ''
    this.busy = true
    this.pendingSignalKey = JSON.stringify(this.settings)
    this.dispatchEvent(new CustomEvent('lv-access-settings-command', { bubbles: true, composed: true, detail }))
  }

  private handleDatastarFetch = (event: Event): void => {
    if (!this.busy || !ownsAdminActionFetch(this, event)) return
    const detail = (event as CustomEvent<DatastarFetchOwnerDetail>).detail
    if (detail?.type === 'finished') { this.busy = false; return }
    const failure = browserCommandFailure(event, 'Access settings update')
    if (!failure) return
    this.busy = false
    this.commandError = failure.message
  }

  render() {
    const signal = this.settings
    const stateError = settingsState(signal.error || '')
    const commandState = settingsState(this.commandError || '')
    const mutationUnavailable = stateError?.kind === 'unauthorized' || stateError?.kind === 'error' || commandState?.kind === 'unauthorized' || commandState?.kind === 'error'
    return html`<section class="surface" aria-label="Access settings">
      <h2>Access settings</h2>
      <p class="muted">Direct role bindings for the <strong>server-bound active project</strong>${signal.projectId ? html`<details class="technical-details"><summary>Technical project identifier</summary><code>${signal.projectId}</code></details>` : nothing}. The policy revision changes whenever another administrator updates access.</p>
      <div class="notice" role="note"><strong>Grant administration unavailable.</strong> ${signal.grantAdministrationLabel || emptyAccessSettings.grantAdministrationLabel}</div>
      ${stateError ? html`<p class=${`state-banner ${stateError.kind === 'unauthorized' || stateError.kind === 'error' ? 'error' : ''}`} role="alert" aria-live="assertive"><strong>${humanize(stateError.kind)}:</strong> ${stateError.label}</p>` : nothing}
      ${signal.error ? html`<p class="error" role="alert"><span class="technical-details">Technical detail:</span> ${signal.error}</p>` : nothing}
      ${commandState ? html`<p class=${`state-banner ${commandState.kind === 'unauthorized' || commandState.kind === 'error' ? 'error' : ''}`} role="alert" aria-live="assertive"><strong>${humanize(commandState.kind)}:</strong> ${commandState.label}</p>` : nothing}
      ${this.commandError ? html`<p class="error" role="alert">${this.commandError}</p>` : nothing}
      ${signal.message ? html`<p role="status">${signal.message}</p>` : nothing}
      ${signal.loading ? html`<p class="muted" aria-live="polite">${signal.roleBindings.length ? 'Refreshing access policy…' : 'Loading access settings…'}</p>` : nothing}
      <form class="form" @submit=${this.createBinding}>
        <label>Binding identifier<input name="bindingId" required placeholder="finance-viewers" aria-describedby="access-binding-help" ?disabled=${this.busy || mutationUnavailable}></label>
        <label>Binding name (optional)<input name="bindingName" placeholder="Finance viewers" aria-describedby="access-binding-help" ?disabled=${this.busy || mutationUnavailable}></label>
        <span id="access-binding-help" class="muted">Use a stable identifier from Principals or Groups. The identifier is kept as technical evidence; names are shown in the review below.</span>
        <label>Authority subject<select name="subjectType" ?disabled=${this.busy || mutationUnavailable}><option value="principal">Person</option><option value="group">Group</option></select></label>
        <label>Subject identifier<input name="subjectId" required placeholder="principal-123" aria-describedby="access-subject-help" ?disabled=${this.busy || mutationUnavailable}></label>
        <span id="access-subject-help" class="muted">Choose the person or group that should receive this project role.</span>
        <label>Project role<select name="role" required ?disabled=${this.busy || mutationUnavailable || !signal.roles.length}>${signal.roles.map((role) => html`<option value=${role.name}>${humanize(role.name)}</option>`)}</select></label>
        <button class="primary" type="submit" ?disabled=${this.busy || mutationUnavailable || !signal.roles.length}>Create binding</button>
      </form>
      ${signal.roleBindings.length ? html`<div class="table-wrap"><table><thead><tr><th>Subject</th><th>Type</th><th>Role</th><th>Actions</th></tr></thead><tbody>${signal.roleBindings.map((binding) => html`<tr>
        <td><strong>${binding.name || `${subjectLabel(binding.subjectType)} assignment`}</strong><div class="muted">Directly assigned authority</div><details class="technical-details"><summary>Technical evidence</summary><div>Subject: <code>${binding.subjectId}</code></div><div>Binding: <code>${binding.id}</code></div></details></td>
        <td>${subjectLabel(binding.subjectType)}</td><td>${humanize(binding.role)}</td>
        <td class="actions"><button class="danger" ?disabled=${this.busy || mutationUnavailable} aria-label=${`Delete ${binding.name || `${subjectLabel(binding.subjectType)} assignment`}`} @click=${() => this.deleteBinding(binding)}>Delete</button></td>
      </tr>`)}</tbody></table></div>` : html`<p class="empty">No direct role bindings are present. This is an empty direct-assignment list, not proof that the administrator has no access; review effective access below.</p>`}
      ${this.renderEffectiveAccess(signal)}
    </section>`
  }

  private renderEffectiveAccess(signal: AccessSettingsSignal) {
    const explanation = signal.effectiveAccess ?? { decisions: [], loading: false, error: 'Effective access explanation is unavailable.' }
    return html`<section class="detail-subsection" aria-label="Effective access explanation"><div><h3>Effective access explanation</h3><p class="muted">Evidence for this administrator in the active project. Direct and inherited access are shown separately; compiled grants are immutable serving-generation evidence.</p></div>
      ${explanation.loading ? html`<p class="muted" aria-live="polite">Loading effective access explanation…</p>` : nothing}
      ${explanation.error ? html`<p class="state-banner" role="status" aria-live="polite"><strong>Partial evidence:</strong> Effective authority could not be fully resolved.</p><p class="error" role="alert">${explanation.error}</p>` : nothing}
      ${!explanation.loading && !explanation.error && explanation.decisions.length ? html`<div class="table-wrap"><table><thead><tr><th>Authority</th><th>Access</th><th>Capability</th><th>Resource</th><th>Reason</th><th>Subject</th></tr></thead><tbody>${explanation.decisions.map((decision) => html`<tr>
        <td><strong>${decision.authority}</strong><div class="muted">${authorityDescription(decision.authority)}</div></td><td>${decision.allowed ? 'Allowed' : 'Denied'}</td><td>${humanize(decision.capability)}</td>
        <td><strong>${humanize(decision.resourceKind)}</strong><details class="technical-details"><summary>Technical resource identifier</summary><code>${decision.resourceKind}/${decision.resourceId}</code></details></td><td>${decision.reason}</td>
        <td>${decision.subjectType && decision.subjectId ? html`${subjectLabel(decision.subjectType)}<details class="technical-details"><summary>Technical subject identifier</summary><code>${decision.subjectId}</code></details>` : 'No subject — evaluated as a fallback decision'}</td>
      </tr>`)}</tbody></table></div><p class="muted">Denied decisions are marked <strong>Denied</strong> with the reason “no direct, inherited, owner, or platform authority.”</p>` : nothing}
      ${!explanation.loading && !explanation.error && !explanation.decisions.length ? html`<p class="empty" role="status">No effective-access decisions were returned. This is an empty result for the active project, not a grant-management failure; if access is expected, reload the latest serving evidence.</p>` : nothing}
    </section>`
  }

  private createBinding = (event: SubmitEvent): void => {
    event.preventDefault()
    const form = event.currentTarget as HTMLFormElement
    this.emit({ action: 'create', bindingId: formValue(form, 'bindingId'), bindingName: formValue(form, 'bindingName'), subjectType: formValue(form, 'subjectType'), subjectId: formValue(form, 'subjectId'), role: formValue(form, 'role'), expectedRevision: this.settings.policyRevision })
  }

  private deleteBinding = (binding: AccessSettingsSignal['roleBindings'][number]): void => {
    const subject = binding.name || `${subjectLabel(binding.subjectType)} assignment`
    if (!window.confirm(`Delete ${subject}? This removes the direct ${humanize(binding.role)} role from this project. Group-derived, owner, platform, or compiled authority will not be changed.`)) return
    this.emit({ action: 'delete', bindingId: binding.id, expectedRevision: this.settings.policyRevision })
  }
}

if (!customElements.get('lv-access-settings')) customElements.define('lv-access-settings', LeapViewAccessSettings)

export { LeapViewAccessSettings }
