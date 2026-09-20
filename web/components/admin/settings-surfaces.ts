import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Bot, CalendarDays, CircleSlash2, Info, Pencil, Plus, Trash2, UserPlus, X } from 'lucide'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure } from '../shared/command-failure'
import { entityDetailStyles, renderEntityDetail } from '../shared/entity-detail'
import { lucideIcon } from '../shared/lucide-icons'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import { settingsSurfaceStyles } from './settings-surfaces.styles'
import { identitySourceLabel, principalInitials, currentPrincipalAvatarUrl, initialsForValue, memberActionLabel, formatAccessDate, humanizeAccessValue, principalActivityLabel, formValue } from './settings-surfaces.helpers'
import type { AccessActivitySignal, AccessAdministrationSignal, AccessGroupSignal, AccessPrincipalSignal, ServiceAccountSecretSignal, ServiceAccountSignal, ServiceAccountsSignal, ProjectRegistrySignal } from '../../generated/signals'
import './access-settings'
import './settings-audit'
import '../shared/entity-list'
import '../shared/user-avatar'
import type { EntityListColumn, EntityListItem } from '../shared/entity-list'
import '../shared/entity-multi-select'
import '../shared/one-time-secret'
import '../shared/record-table'
import type { EntityMultiSelectItem } from '../shared/entity-multi-select'

const tableStyles = css`
  :host { display: block; color: var(--lv-fg-default); font: var(--lv-type-body); font-family: var(--fontStack-system); }
  .surface { display: grid; gap: 12px; min-width: 0; }
  h2, h3, p { margin: 0; }
  h2 { font: var(--lv-type-section-title); }
  h3 { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
  .muted { color: var(--lv-fg-muted); }
  .error { color: var(--lv-fg-danger); }
  .table-wrap { overflow-x: auto; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); }
  table { width: 100%; min-width: 620px; border-collapse: collapse; }
  th, td { padding: var(--base-size-8) var(--base-size-12); text-align: left; border-bottom: var(--lv-border-muted); vertical-align: top; }
  th { color: var(--lv-fg-muted); font: var(--lv-type-caption); text-transform: uppercase; letter-spacing: .03em; }
  tbody tr:last-child td { border-bottom: 0; }
  a { color: var(--lv-fg-link); text-decoration: none; }
  a:hover { text-decoration: underline; }
  button, input, select { box-sizing: border-box; min-height: var(--lv-control-small); border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-bg-control); color: inherit; padding: var(--base-size-4) var(--base-size-8); font: inherit; }
  button { cursor: pointer; }
  button[disabled] { cursor: default; opacity: .55; }
  .toolbar, .form { display: flex; flex-wrap: wrap; align-items: end; gap: 8px; }
  label { display: grid; gap: var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .empty { padding: var(--base-size-20) var(--base-size-12); color: var(--lv-fg-muted); }
  .actions { display: flex; flex-wrap: wrap; gap: 6px; }
  .notice { border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); padding: var(--base-size-12); }
  .state-banner { border: var(--lv-border-attention, var(--lv-border-muted)); border-radius: var(--lv-radius-default); background: var(--lv-bg-attention-muted, var(--lv-bg-panel-muted)); padding: var(--base-size-12); }
  .state-banner.error { border-color: var(--lv-border-danger, var(--lv-border-muted)); background: var(--lv-bg-danger-muted, var(--lv-bg-panel-muted)); color: var(--lv-fg-danger); }
  .danger { color: var(--lv-fg-danger); }
  code { overflow-wrap: anywhere; }
  .technical-details { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .technical-details summary { cursor: pointer; }
  dialog { width: min(30rem, calc(100vw - var(--base-size-32))); max-width: none; max-height: calc(100svh - var(--base-size-32)); overflow: auto; border: 0; border-radius: var(--lv-radius-large); background: transparent; color: inherit; padding: 0; }
  dialog::backdrop { background: var(--lv-modal-backdrop); }
  .modal { display: grid; overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); box-shadow: var(--lv-shadow-floating-lg); }
  .modal-header { display: flex; align-items: start; justify-content: space-between; gap: var(--base-size-16); border-bottom: var(--lv-border-muted); padding: var(--base-size-16) var(--base-size-20); }
  .modal-title { display: grid; gap: var(--base-size-4); }
  .modal-title h2 { font: var(--lv-type-section-title); }
  .modal-close { display: inline-flex; width: var(--control-medium-size); min-height: var(--control-medium-size); align-items: center; justify-content: center; border-color: transparent; background: transparent; color: var(--lv-fg-muted); padding: 0; }
  .modal-close:hover { border-color: var(--lv-line-muted); background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
  .modal-body { display: grid; gap: var(--base-size-16); padding: var(--base-size-20); }
  .modal-body .form { display: grid; align-items: stretch; }
  .modal-body input, .modal-body select { width: 100%; min-height: var(--control-medium-size); }
  .modal-actions { display: flex; justify-content: flex-end; gap: var(--base-size-8); }
  .primary { border-color: var(--lv-button-accent-border-rest); background: var(--lv-button-accent-bg-rest); color: var(--lv-button-accent-fg-rest); }
  .primary:hover { border-color: var(--lv-button-accent-border-hover); background: var(--lv-button-accent-bg-hover); }
  .password-result { display: grid; gap: var(--base-size-12); }
  .password-value { display: block; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); padding: var(--base-size-12); font-family: var(--fontStack-monospace); overflow-wrap: anywhere; user-select: all; }
  .status-active { color: var(--lv-fg-success); }
  .status-blocked, .status-disabled { color: var(--lv-fg-danger); }
  .primary-detail-action { display: inline-flex; align-items: center; gap: var(--base-size-6); color: var(--lv-fg-link); }
  .action-menu { position: relative; }
  .action-menu summary { display: inline-flex; min-height: var(--lv-control-small); box-sizing: border-box; align-items: center; border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-bg-control); padding: var(--base-size-4) var(--base-size-8); cursor: pointer; list-style: none; }
  .action-menu summary::-webkit-details-marker { display: none; }
  .action-menu[open] summary { background: var(--lv-bg-control-hover); }
  .action-menu-popover { position: absolute; z-index: 2; top: calc(100% + var(--base-size-4)); right: 0; display: grid; width: max-content; min-width: 180px; gap: var(--base-size-4); border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); box-shadow: var(--lv-shadow-floating-lg); padding: var(--base-size-6); }
  .action-menu-popover button { width: 100%; border-color: transparent; background: transparent; text-align: left; }
  .action-menu-popover button:hover { background: var(--lv-bg-control-hover); }
  .detail-section { display: grid; min-width: 0; align-content: start; gap: var(--base-size-16); border-top: var(--lv-border-muted); padding: var(--base-size-24) 0; }
  .detail-section .table-wrap { border: 0; border-radius: 0; }
  .detail-section table { min-width: 540px; }
  .detail-section th, .detail-section td { padding-inline: 0 var(--base-size-16); }
  .detail-section table.member-table { min-width: 0; }
  .member-table th:last-child, .member-table td:last-child { width: 1%; padding-right: 0; text-align: right; white-space: nowrap; }
  .member-table td:nth-child(2) { overflow-wrap: anywhere; }
  .card-header { display: flex; align-items: start; justify-content: space-between; gap: var(--base-size-12); }
  .card-header-copy { display: grid; gap: var(--base-size-4); }
  .section-heading { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-12); }
  .section-action { display: inline-flex; align-items: center; gap: var(--base-size-6); }
  .inline-value { display: flex; min-width: 0; align-items: center; gap: var(--base-size-6); }
  .inline-value code { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .text-button { min-height: auto; flex: 0 0 auto; border-color: transparent; background: transparent; color: var(--lv-fg-link); padding: var(--base-size-2); }
  .role-source { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .detail-subsection { display: grid; gap: var(--base-size-12); }
  .detail-empty-row { display: grid; grid-template-columns: minmax(10rem, 0.45fr) minmax(0, 1fr); gap: var(--base-size-16); color: var(--lv-fg-muted); }
  .detail-empty-row strong { color: var(--lv-fg-muted); font-weight: var(--base-text-weight-normal); }
  .detail-form { width: fit-content; }
  .activity-list { display: grid; gap: 0; margin: 0; padding: 0; list-style: none; }
  .activity-item { display: grid; grid-template-columns: 10px minmax(0, 1fr) auto; align-items: start; gap: var(--base-size-8); border-bottom: var(--lv-border-muted); padding: var(--base-size-8) 0; }
  .activity-item:last-child { border-bottom: 0; }
  .activity-dot { width: 8px; height: 8px; margin-top: 6px; border-radius: 50%; background: var(--lv-fg-muted); }
  .activity-copy { display: grid; gap: var(--base-size-2); }
  .detail-user-avatar { --lv-user-avatar-size: 100%; width: 100%; height: 100%; }
  @media (max-width: 760px) {
    .detail-section { padding-block: var(--base-size-20); }
    .detail-empty-row { grid-template-columns: minmax(6.5rem, 0.7fr) minmax(0, 1.3fr); }
    .activity-item { grid-template-columns: 10px minmax(0, 1fr); }
    .activity-item > time { grid-column: 2; }
  }
  @media (max-width: 480px) {
    .detail-empty-row { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-4); }
  }
`
import '../shared/select-menu'
import '../shared/drawer'

type DatastarFetchOwnerDetail = { type?: string; el?: Element }

const serviceAccountSecretLifetimeOptions = [
  { value: 30, label: '30 days' },
  { value: 90, label: '90 days' },
  { value: 180, label: '180 days (recommended)' },
  { value: 365, label: '1 year (maximum)' },
] as const
// Keep the Settings default aligned with access.ServicePrincipalSecretDefaultLifetime.
const serviceAccountSecretDefaultLifetimeDays = 180

function auditDateTimeLocal(value: string | undefined): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? '' : date.toISOString().slice(0, 16)
}

function auditTimestamp(value: string): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? '' : date.toISOString()
}

// Datastar's fetch lifecycle is document-global. Settings controls may only
// consume a successful completion from the lv-admin-page host whose data-on
// attributes initiated their command; unrelated page fetches must not unlock
// a still-running mutation.
function ownsAdminActionFetch(element: Element, event: Event): boolean {
  const owner = (event as CustomEvent<DatastarFetchOwnerDetail>).detail?.el
  if (!(owner instanceof Element)) return false
  const root = element.getRootNode()
  const rootHost = 'host' in root ? (root as ShadowRoot).host : null
  const actionHost = rootHost instanceof Element ? rootHost.closest('lv-admin-page') ?? rootHost : element.closest('lv-admin-page')
  return owner === actionHost
}

const emptyAccessAdministration: AccessAdministrationSignal = { principals: [], groups: [], projects: [], sessions: [], roleAssignments: [], activity: [], loading: true }

abstract class LeapViewAccessAdministrationBase extends DatastarLit(LitElement) {
  static styles = [entityDetailStyles, settingsSurfaceStyles]
  @property({ type: Boolean }) createOpen = false
  @state() private passwordCopied = false
  private dismissedTemporaryPassword = ''
  get accessState(): AccessAdministrationSignal { return this.signal('adminAccess', emptyAccessAdministration) }
  protected updated(): void {
    const redirectTo = this.accessState.redirectTo
    if (redirectTo && window.location.pathname !== redirectTo) window.location.assign(redirectTo)
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('dialog[data-access-create-dialog]')
    if (this.createOpen && dialog && !dialog.open) {
      dialog.showModal()
      window.setTimeout(() => dialog.querySelector<HTMLElement>('input, select, button')?.focus(), 0)
    } else if (!this.createOpen && dialog?.open) {
      dialog.close()
    }
  }
  protected emit(detail: Record<string, unknown>): void {
    this.dispatchEvent(new CustomEvent('lv-access-admin-command', { bubbles: true, composed: true, detail }))
  }
  protected feedback() {
    const signal = this.accessState
    return html`
      ${signal.error ? html`<p class="error" role="alert">${signal.error}</p>` : nothing}
      ${signal.message ? html`<p role="status">${signal.message}</p>` : nothing}
      ${signal.temporaryPassword ? html`<div class="notice" role="status"><strong>Copy this temporary password now:</strong> <code>${signal.temporaryPassword}</code> <button type="button" @click=${this.copyTemporaryPassword}>${this.passwordCopied ? 'Copied' : 'Copy'}</button></div>` : nothing}
    `
  }

  protected hasUndismissedTemporaryPassword(): boolean {
    const password = this.accessState.temporaryPassword || ''
    return Boolean(password && password !== this.dismissedTemporaryPassword)
  }

  protected renderTemporaryPasswordSuccess() {
    const signal = this.accessState
    return html`<div class="password-result">
      <p role="status">${signal.message || 'Local user created.'}</p>
      <p class="muted">This password is shown once. Send it to the user through a secure channel.</p>
      <code class="password-value">${signal.temporaryPassword}</code>
      <div class="modal-actions">
        <button type="button" @click=${this.copyTemporaryPassword}>${this.passwordCopied ? 'Copied' : 'Copy password'}</button>
        <button class="primary" type="button" @click=${this.requestCreateClose}>Done</button>
      </div>
    </div>`
  }

  protected requestCreateClose = (): void => {
    const password = this.accessState.temporaryPassword || ''
    if (this.hasUndismissedTemporaryPassword() && !this.passwordCopied && !window.confirm('Close without copying the temporary password? It will not be shown again.')) return
    if (password) this.dismissedTemporaryPassword = password
    this.passwordCopied = false
    this.dispatchEvent(new CustomEvent('lv-access-create-close', { bubbles: true, composed: true }))
  }

  protected cancelCreate = (event: Event): void => {
    event.preventDefault()
    this.requestCreateClose()
  }

  protected closeOnBackdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.requestCreateClose()
  }

  private copyTemporaryPassword = async (): Promise<void> => {
    const password = this.accessState.temporaryPassword || ''
    if (!password) return
    await navigator.clipboard.writeText(password)
    this.passwordCopied = true
  }
}

class LeapViewPrincipalAdministration extends LeapViewAccessAdministrationBase {
  @state() private copiedPrincipalID = false
  render() {
    const signal = this.accessState
    if (signal.loading && !signal.principals.length) return html`<p class="muted" aria-live="polite">Loading users…</p>`
    const principal = signal.principals.find((item) => item.id === signal.selectedPrincipalId)
    if (!principal) return this.renderCreate(signal)
    return this.renderDetail(signal, principal)
  }

  private renderCreate(signal: AccessAdministrationSignal) {
    return html`<dialog data-access-create-dialog aria-labelledby="create-local-user-title" @cancel=${this.cancelCreate} @click=${this.closeOnBackdrop}>
      <section class="modal">
        <header class="modal-header">
          <div class="modal-title"><h2 id="create-local-user-title">Create local user</h2><p class="muted">Add a user who signs in with a LeapView password.</p></div>
          <button class="modal-close" type="button" aria-label="Close create local user" @click=${this.requestCreateClose}>${lucideIcon(X, { size: 18 })}</button>
        </header>
        <div class="modal-body">
          ${this.hasUndismissedTemporaryPassword() ? this.renderTemporaryPasswordSuccess() : html`
            ${this.feedback()}
            <form class="form" @submit=${(event: SubmitEvent) => this.createPrincipal(event)}>
              <label>Email<input id="create-local-user-email" aria-label="Email" name="email" type="email" required autocomplete="off" placeholder="person@example.com"></label>
              <label>Display name<input id="create-local-user-display-name" aria-label="Display name" name="displayName" required autocomplete="off" placeholder="Display name"></label>
              <div class="modal-actions"><button type="button" @click=${this.requestCreateClose}>Cancel</button><button class="primary" type="submit" ?disabled=${signal.loading}>Create user</button></div>
            </form>
          `}
        </div>
      </section>
    </dialog>`
  }

  private renderDetail(signal: AccessAdministrationSignal, principal: AccessPrincipalSignal) {
    const source = identitySourceLabel(principal)
    const status = principal.disabledAt ? 'Disabled' : principal.blockedAt ? 'Blocked' : 'Active'
    const projects = new Map((signal.projects || []).map((project) => [project.id, project.name || project.id]))
    const actions = html`
      ${principal.capabilities.canBlock ? html`<button class="primary-detail-action" @click=${() => this.blockPrincipal(principal)}>${lucideIcon(CircleSlash2, { size: 16, strokeWidth: 2 })}<span>Block access</span></button>` : nothing}
      ${principal.capabilities.canUnblock ? html`<button class="primary-detail-action" @click=${() => this.emit({ action: 'unblock_principal', principalId: principal.id })}>Unblock access</button>` : nothing}
      ${principal.capabilities.canResetPassword || principal.capabilities.canManageSessions || principal.capabilities.canDelete ? html`
        <details class="action-menu">
          <summary>More actions</summary>
          <div class="action-menu-popover">
            ${principal.capabilities.canResetPassword ? html`<button @click=${() => this.resetPassword(principal)}>Reset password</button>` : nothing}
            ${principal.capabilities.canManageSessions ? html`
              <button @click=${() => this.revokeAllSessions(principal)}>Revoke all sessions</button>
              ${principal.capabilities.canRevokeAllCredentials !== false ? html`<button class="danger" @click=${() => this.revokeAllCredentials(principal)}>Revoke all credentials</button>` : nothing}
            ` : nothing}
            ${principal.capabilities.canDelete ? html`<button class="danger" @click=${() => this.deletePrincipal(principal)}>Delete user</button>` : nothing}
          </div>
        </details>` : nothing}`
    const notice = principal.identitySource === 'external' ? html`<div class="detail-notice" role="note"><span class="detail-notice-icon" aria-hidden="true">${lucideIcon(Info, { size: 18, strokeWidth: 2 })}</span><p><strong>${source} owns this identity.</strong> Profile fields and synchronized memberships are read-only in LeapView. Block access locally or revoke sessions here; update or permanently remove the user in ${source}.</p></div>`
      : principal.identitySource === 'system' ? html`<div class="detail-notice" role="note"><span class="detail-notice-icon" aria-hidden="true">${lucideIcon(Info, { size: 18, strokeWidth: 2 })}</span><p><strong>System-managed account.</strong> Profile fields are read-only because this account is provisioned by LeapView configuration. Block access locally or revoke sessions here; update it through its provisioning source.</p></div>` : nothing
    const avatarUrl = currentPrincipalAvatarUrl(this.signal<{ sidebar?: { userAvatarUrl?: string } }>('chrome', {}), principal.id)
    return renderEntityDetail({
      label: 'User administration', feedback: this.feedback(), backHref: '/admin/principals', backLabel: 'All users',
      avatar: avatarUrl ? html`<lv-user-avatar class="detail-user-avatar" .name=${principal.displayName || principal.email} .imageUrl=${avatarUrl} aria-hidden="true"></lv-user-avatar>` : principalInitials(principal), title: principal.displayName || principal.email || principal.id, subtitle: principal.email,
      badges: html`<span class="badge">${source}</span><span class=${`badge status-${status.toLowerCase()}`} data-user-status>${status}</span>`,
      actions, notice,
      sections: html`
        <section class="detail-section" aria-labelledby="user-overview-title">
          <div class="card-header"><h2 id="user-overview-title">Overview</h2></div>
          <dl class="facts">
            <div class="fact"><dt>Email</dt><dd>${principal.email || '—'}</dd></div>
            <div class="fact"><dt>Authentication</dt><dd>${principal.hasLocalPassword ? 'Local password' : source}</dd></div>
            <div class="fact"><dt>Created</dt><dd>${formatAccessDate(principal.createdAt)}</dd></div>
            <div class="fact"><dt>Last updated</dt><dd>${formatAccessDate(principal.updatedAt)}</dd></div>
            <div class="fact"><dt>Last activity</dt><dd>${formatAccessDate(principal.lastSeenAt)}</dd></div>
            <div class="fact"><dt>Principal ID</dt><dd class="inline-value"><code title=${principal.id}>${principal.id}</code><button class="text-button" type="button" @click=${() => this.copyPrincipalID(principal.id)}>${this.copiedPrincipalID ? 'Copied' : 'Copy'}</button></dd></div>
          </dl>
          ${principal.capabilities.canUpdateProfile ? html`<form class="form" @submit=${(event: SubmitEvent) => this.updatePrincipal(event, principal)}>
            <label>Display name<input name="displayName" required .value=${principal.displayName}></label>
            <button type="submit">Save name</button>
          </form>` : nothing}
        </section>
        <section class="detail-section" aria-labelledby="user-access-title">
          <div class="card-header"><h2 id="user-access-title">Access</h2></div>
          <div class="detail-subsection">${signal.roleAssignments.length ? html`<h3>Project roles</h3><div class="table-wrap"><table><thead><tr><th>Project</th><th>Role</th><th>Granted through</th></tr></thead><tbody>${signal.roleAssignments.map((assignment) => html`<tr><td>${projects.get(assignment.projectId) || assignment.projectId}</td><td>${humanizeAccessValue(assignment.role)}</td><td>${assignment.sourceType === 'group' ? html`<a href=${`/admin/groups/${encodeURIComponent(assignment.sourceId)}`}>Via ${assignment.sourceName}</a>` : 'Direct assignment'}</td></tr>`)}</tbody></table></div>` : html`<div class="detail-empty-row"><strong>Project roles</strong><span>No project roles assigned.</span></div>`}</div>
          <div class="detail-subsection">${principal.groups.length ? html`<h3>Groups</h3><div class="table-wrap"><table><thead><tr><th>Group</th><th>Source</th></tr></thead><tbody>${principal.groups.map((group) => html`<tr><td><a href=${`/admin/groups/${encodeURIComponent(group.id)}`}>${group.name || group.id}</a></td><td>${group.provider || 'local'}</td></tr>`)}</tbody></table></div>` : html`<div class="detail-empty-row"><strong>Groups</strong><span>No group memberships.</span></div>`}</div>
        </section>
        <section class="detail-section" aria-labelledby="user-security-title">
          <div class="card-header"><h2 id="user-security-title">Security</h2></div>
          ${principal.disabledAt ? html`<p class="notice">This account was disabled by ${source} on ${formatAccessDate(principal.disabledAt)}.</p>` : principal.blockedAt ? html`<p class="notice">LeapView access has been blocked since ${formatAccessDate(principal.blockedAt)}.</p>` : nothing}
          ${principal.capabilities.canManageSessions ? html`${signal.sessions.length ? html`<div class="table-wrap"><table><thead><tr><th>Session</th><th>Last seen</th><th>Expires</th><th></th></tr></thead><tbody>${signal.sessions.map((session) => html`<tr><td>${humanizeAccessValue(session.kind)}</td><td>${formatAccessDate(session.lastSeenAt || session.createdAt)}</td><td>${formatAccessDate(session.expiresAt)}</td><td><button @click=${() => this.emit({ action: 'revoke_session', principalId: principal.id, sessionId: session.id })}>Revoke</button></td></tr>`)}</tbody></table></div>` : html`<div class="detail-empty-row"><strong>Active sessions</strong><span>No active sessions.</span></div>`}` : nothing}
        </section>
        <section class="detail-section" aria-labelledby="user-activity-title">
          <div class="card-header"><h2 id="user-activity-title">Recent activity</h2><a href="/admin/audit">View audit log</a></div>
          ${signal.activity.length ? html`<ol class="activity-list">${signal.activity.map((activity) => html`<li class="activity-item"><span class="activity-dot" aria-hidden="true"></span><div class="activity-copy"><span>${principalActivityLabel(activity)}</span>${activity.status && activity.status !== 'success' ? html`<span class="error">${humanizeAccessValue(activity.status)}</span>` : nothing}</div><time datetime=${activity.createdAt}>${formatAccessDate(activity.createdAt)}</time></li>`)}</ol>` : html`<p class="muted">No recent administrative activity.</p>`}
        </section>
      `,
    })
  }

  private async copyPrincipalID(principalID: string): Promise<void> {
    await navigator.clipboard.writeText(principalID)
    this.copiedPrincipalID = true
  }

  private createPrincipal(event: SubmitEvent): void {
    event.preventDefault()
    const form = event.currentTarget as HTMLFormElement
    this.emit({ action: 'create_principal', email: formValue(form, 'email'), displayName: formValue(form, 'displayName') })
  }
  private updatePrincipal(event: SubmitEvent, principal: AccessPrincipalSignal): void {
    event.preventDefault()
    this.emit({ action: 'update_principal', principalId: principal.id, displayName: formValue(event.currentTarget as HTMLFormElement, 'displayName'), revision: principal.revision })
  }
  private resetPassword(principal: AccessPrincipalSignal): void {
    if (window.confirm(`Reset the local password for ${principal.displayName || principal.email}? Existing sessions will remain active.`)) this.emit({ action: 'reset_password', principalId: principal.id })
  }
  private blockPrincipal(principal: AccessPrincipalSignal): void {
    if (window.confirm(`Block ${principal.displayName || principal.email}? Their active sessions will be revoked immediately.`)) this.emit({ action: 'block_principal', principalId: principal.id })
  }
  private revokeAllSessions(principal: AccessPrincipalSignal): void {
    if (window.confirm(`Revoke all active sessions for ${principal.displayName || principal.email}?`)) this.emit({ action: 'revoke_all_sessions', principalId: principal.id })
  }
  private revokeAllCredentials(principal: AccessPrincipalSignal): void {
    const label = principal.displayName || principal.email
    if (window.confirm(`Revoke every active credential for ${label}? This invalidates sessions, API tokens, service secrets, authoring sessions, and OAuth sessions. The principal remains enabled and can sign in again; block access first if re-authentication must stop. This action cannot be undone.`)) this.emit({ action: 'revoke_all_credentials', principalId: principal.id })
  }
  private deletePrincipal(principal: AccessPrincipalSignal): void {
    if (window.confirm(`Delete ${principal.displayName || principal.email}? This cannot be undone.`)) this.emit({ action: 'delete_principal', principalId: principal.id })
  }
}

class LeapViewGroupAdministration extends LeapViewAccessAdministrationBase {
  @state() private detailDialog: 'rename' | 'add-member' | '' = ''
  @state() private selectedMemberIds: string[] = []

  protected updated(): void {
    super.updated()
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('dialog[data-group-detail-dialog]')
    if (this.detailDialog && dialog && !dialog.open) {
      dialog.showModal()
      window.setTimeout(() => dialog.querySelector<HTMLElement>('input, select, button')?.focus(), 0)
    }
  }

  render() {
    const signal = this.accessState
    if (signal.loading && !signal.groups.length) return html`<p class="muted" aria-live="polite">Loading groups…</p>`
    const group = signal.groups.find((item) => item.id === signal.selectedGroupId)
    if (!group) return this.renderCreate(signal)
    return this.renderDetail(signal, group)
  }

  private renderCreate(signal: AccessAdministrationSignal) {
    return html`<dialog data-access-create-dialog aria-labelledby="create-group-title" @cancel=${this.cancelCreate} @click=${this.closeOnBackdrop}>
      <section class="modal">
        <header class="modal-header">
          <div class="modal-title"><h2 id="create-group-title">Create group</h2><p class="muted">Organize users and assign access as a team.</p></div>
          <button class="modal-close" type="button" aria-label="Close create group" @click=${this.requestCreateClose}>${lucideIcon(X, { size: 18 })}</button>
        </header>
        <div class="modal-body">
          ${this.feedback()}
          <form class="form" @submit=${(event: SubmitEvent) => this.createGroup(event)}>
            <label>Group name<input id="create-group-display-name" aria-label="Group name" name="displayName" required autocomplete="off" placeholder="Analytics team"></label>
            <div class="modal-actions"><button type="button" @click=${this.requestCreateClose}>Cancel</button><button class="primary" type="submit" ?disabled=${signal.loading}>Create group</button></div>
          </form>
        </div>
      </section>
    </dialog>`
  }

  private renderDetail(signal: AccessAdministrationSignal, group: AccessGroupSignal) {
    const memberIDs = new Set(group.members.map((member) => member.id))
    const candidates = signal.principals.filter((principal) => principal.kind === 'user' && !memberIDs.has(principal.id))
    const provider = group.provider || 'external'
    const local = provider.toLowerCase() === 'local'
    const actions = group.capabilities.canUpdate || group.capabilities.canDelete ? html`
      <details class="action-menu">
        <summary>More actions</summary>
        <div class="action-menu-popover">
          ${group.capabilities.canUpdate ? html`<button type="button" @click=${(event: Event) => this.openDetailDialog('rename', event)}>${lucideIcon(Pencil, { size: 15 })} Rename group</button>` : nothing}
          ${group.capabilities.canDelete ? html`<button class="danger" type="button" @click=${() => this.deleteGroup(group)}>Delete group</button>` : nothing}
        </div>
      </details>` : nothing
    const notice = !local ? html`<div class="detail-notice" role="note"><span class="detail-notice-icon" aria-hidden="true">${lucideIcon(Info, { size: 18, strokeWidth: 2 })}</span><p><strong>${provider.toUpperCase()} owns this group.</strong> Its profile and membership are synchronized and read-only in LeapView. Update or remove it through its provisioning source.</p></div>` : nothing
    return html`${renderEntityDetail({
      label: 'Group administration', feedback: this.feedback(), backHref: '/admin/groups', backLabel: 'All groups',
      avatar: initialsForValue(group.name || group.id), title: group.name || group.id,
      subtitle: group.externalId ? `External ID ${group.externalId}` : undefined,
      badges: html`<span class="badge">${provider}</span><span class="badge status-active">${local ? 'Managed in LeapView' : 'Synchronized'}</span>`,
      actions, notice,
      sections: html`
        <section class="detail-section" aria-labelledby="group-overview-title">
          <div class="section-heading"><h2 id="group-overview-title">Overview</h2></div>
          <dl class="facts">
            <div class="fact"><dt>Provider</dt><dd>${provider}</dd></div>
            <div class="fact"><dt>Scope</dt><dd>Global</dd></div>
            <div class="fact"><dt>Created</dt><dd>${formatAccessDate(group.createdAt)}</dd></div>
            <div class="fact"><dt>Member count</dt><dd>${group.members.length}</dd></div>
            <div class="fact"><dt>Group ID</dt><dd><code title=${group.id}>${group.id}</code></dd></div>
            ${group.externalId ? html`<div class="fact"><dt>External ID</dt><dd><code>${group.externalId}</code></dd></div>` : nothing}
          </dl>
        </section>
        <section class="detail-section" aria-labelledby="group-members-title">
          <div class="section-heading">
            <h2 id="group-members-title">Members</h2>
            ${group.capabilities.canManageMembers ? html`<button class="section-action" type="button" @click=${() => this.openDetailDialog('add-member')}>${lucideIcon(UserPlus, { size: 16 })}<span>Add members</span></button>` : nothing}
          </div>
          ${group.members.length ? html`<div class="table-wrap"><table class="member-table"><thead><tr><th>Member</th><th>Email</th><th></th></tr></thead><tbody>${group.members.map((member) => html`<tr><td><a href=${`/admin/principals/${encodeURIComponent(member.id)}`}>${member.displayName || member.email}</a></td><td>${member.email}</td><td>${group.capabilities.canManageMembers ? html`<button @click=${() => this.removeMember(group, member.id, member.displayName || member.email)}>Remove</button>` : nothing}</td></tr>`)}</tbody></table></div>` : html`<div class="detail-empty-row"><strong>Members</strong><span>No members.</span></div>`}
        </section>
      `,
    })}${this.renderDetailDialog(signal, group, candidates)}`
  }

  private renderDetailDialog(signal: AccessAdministrationSignal, group: AccessGroupSignal, candidates: AccessPrincipalSignal[]) {
    if (this.detailDialog === 'rename') return html`
      <dialog data-group-detail-dialog="rename" aria-labelledby="rename-group-title" @cancel=${this.cancelDetailDialog} @click=${this.closeDetailDialogOnBackdrop}>
        <section class="modal">
          <header class="modal-header">
            <div class="modal-title"><h2 id="rename-group-title">Rename group</h2><p class="muted">Change the name shown throughout LeapView.</p></div>
            <button class="modal-close" type="button" aria-label="Close rename group" @click=${this.closeDetailDialog}>${lucideIcon(X, { size: 18 })}</button>
          </header>
          <div class="modal-body">
            <form class="form" @submit=${(event: SubmitEvent) => this.updateGroup(event, group)}>
              <label>Group name<input id="rename-group-display-name" aria-label="Group name" name="displayName" required autocomplete="off" .value=${group.name}></label>
              <div class="modal-actions"><button type="button" @click=${this.closeDetailDialog}>Cancel</button><button class="primary" type="submit" ?disabled=${signal.loading}>Rename group</button></div>
            </form>
          </div>
        </section>
      </dialog>`
    if (this.detailDialog === 'add-member') return html`
      <dialog data-group-detail-dialog="add-member" aria-labelledby="add-group-member-title" @cancel=${this.cancelDetailDialog} @click=${this.closeDetailDialogOnBackdrop}>
        <section class="modal">
          <header class="modal-header">
            <div class="modal-title"><h2 id="add-group-member-title">Add members</h2><p class="muted">Select one or more users to add to ${group.name}.</p></div>
            <button class="modal-close" type="button" aria-label="Close add members" @click=${this.closeDetailDialog}>${lucideIcon(X, { size: 18 })}</button>
          </header>
          <div class="modal-body">
            ${candidates.length ? html`
              <form class="form" @submit=${(event: SubmitEvent) => this.addMembers(event, group)}>
                <lv-entity-multi-select
                  label="Users"
                  searchPlaceholder="Search users..."
                  .items=${this.memberPickerItems(candidates)}
                  .selectedIds=${this.selectedMemberIds}
                  ?disabled=${signal.loading}
                  @lv-entity-selection-change=${this.updateSelectedMembers}
                ></lv-entity-multi-select>
                <div class="modal-actions"><button type="button" @click=${this.closeDetailDialog}>Cancel</button><button class="primary" type="submit" ?disabled=${signal.loading || this.selectedMemberIds.length === 0}>${memberActionLabel(this.selectedMemberIds.length)}</button></div>
              </form>` : html`<div><p>Everyone eligible is already a member.</p><div class="modal-actions"><button type="button" @click=${this.closeDetailDialog}>Done</button></div></div>`}
          </div>
        </section>
      </dialog>`
    return nothing
  }

  private openDetailDialog(dialog: 'rename' | 'add-member', event?: Event): void {
    ;(event?.currentTarget as HTMLElement | undefined)?.closest('details')?.removeAttribute('open')
    if (dialog === 'add-member') this.selectedMemberIds = []
    this.detailDialog = dialog
  }

  private closeDetailDialog = (): void => {
    this.renderRoot.querySelector<HTMLDialogElement>('dialog[data-group-detail-dialog]')?.close()
    this.detailDialog = ''
  }

  private cancelDetailDialog = (event: Event): void => {
    event.preventDefault()
    this.closeDetailDialog()
  }

  private closeDetailDialogOnBackdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.closeDetailDialog()
  }

  private createGroup(event: SubmitEvent): void { event.preventDefault(); const form = event.currentTarget as HTMLFormElement; this.emit({ action: 'create_group', displayName: formValue(form, 'displayName') }) }
  private updateGroup(event: SubmitEvent, group: AccessGroupSignal): void { event.preventDefault(); this.emit({ action: 'update_group', groupId: group.id, displayName: formValue(event.currentTarget as HTMLFormElement, 'displayName'), revision: group.revision }); this.closeDetailDialog() }
  private memberPickerItems(candidates: AccessPrincipalSignal[]): EntityMultiSelectItem[] {
    return candidates.map((principal) => ({ id: principal.id, label: principal.displayName || principal.email, detail: principal.displayName ? principal.email : '', kind: 'principal' }))
  }
  private readonly updateSelectedMembers = (event: CustomEvent<{ selectedIds: string[] }>): void => { this.selectedMemberIds = event.detail.selectedIds }
  private addMembers(event: SubmitEvent, group: AccessGroupSignal): void {
    event.preventDefault()
    if (this.selectedMemberIds.length === 0) return
    this.emit({ action: 'add_group_member', groupId: group.id, principalIds: [...this.selectedMemberIds] })
    this.closeDetailDialog()
  }
  private removeMember(group: AccessGroupSignal, principalId: string, label: string): void { if (window.confirm(`Remove ${label} from ${group.name}?`)) this.emit({ action: 'remove_group_member', groupId: group.id, principalId }) }
  private deleteGroup(group: AccessGroupSignal): void { if (window.confirm(`Delete ${group.name}? Access granted through this group will be removed.`)) this.emit({ action: 'delete_group', groupId: group.id }) }
}

class LeapViewProjectRegistry extends DatastarLit(LitElement) {
  static styles = settingsSurfaceStyles
  get registry(): ProjectRegistrySignal { return this.signal('adminProjects', { items: [], loading: false, hasMore: false }) }
  render() {
    const signal = this.registry
    return html`<section class="surface" aria-label="Projects">
      ${signal.error ? html`<p class="error" role="alert">${signal.error}</p>` : nothing}
      ${signal.loading ? html`<p class="muted" aria-live="polite">Loading projects…</p>` : nothing}
      <lv-entity-list
        .items=${projectListItems(signal)}
        .columns=${projectListColumns()}
        client-filter
        search-placeholder="Search projects by name, owner, or environment"
        list-label="Projects"
        empty-text=${signal.empty || 'No projects are available.'}
      ></lv-entity-list>
    </section>`
  }
}

function projectListItems(signal: ProjectRegistrySignal): EntityListItem[] {
  return (signal.items ?? []).map((item) => {
    const administrators = (item.administrators ?? []).map((administrator) => administrator.displayName).filter(Boolean)
    const deployment = item.deploymentStatus || item.servingStateStatus || 'Not deployed'
    return {
      id: item.id,
      title: item.title || item.id,
      description: item.description,
      href: item.href || item.links.project,
      icon: 'database',
      iconTreatment: 'plain',
      columns: {
        owner: item.owner?.displayName || 'Unassigned',
        administrators: administrators.length ? administrators.join(', ') : 'None',
        environment: item.environment || '—',
        deployment,
        updated: formatProjectDate(item.updatedAt),
      },
      columnTitles: {
        owner: item.owner?.email || item.owner?.displayName || '',
        administrators: administrators.join(', '),
        deployment: item.currentDeploymentId || deployment,
        updated: item.updatedAt || '',
      },
      sortValues: {
        updated: projectTimestamp(item.updatedAt),
      },
    }
  })
}

function projectListColumns(): EntityListColumn[] {
  return [
    { id: 'name', label: 'Name', width: '27%' },
    { id: 'owner', label: 'Owner', width: '18%' },
    { id: 'administrators', label: 'Administrators', width: '18%' },
    { id: 'environment', label: 'Environment', width: '13%' },
    { id: 'deployment', label: 'Deployment', width: '13%' },
    { id: 'updated', label: 'Updated', width: '11%' },
  ]
}

function formatProjectDate(value = ''): string {
  const timestamp = projectTimestamp(value)
  if (!timestamp) return '—'
  return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' }).format(timestamp)
}

function projectTimestamp(value = ''): number {
  const timestamp = Date.parse(value)
  return Number.isNaN(timestamp) ? 0 : timestamp
}

type ServiceSecretExpirationPreset = '30' | '60' | '90' | 'custom'

function serviceAccountListItems(accounts: ServiceAccountSignal[], busy: boolean): EntityListItem[] {
  return accounts.map((account) => ({
    id: account.id,
    title: account.displayName || account.id,
    description: account.id,
    icon: 'application',
    iconTreatment: 'plain',
    columns: {
      status: account.disabledAt ? 'Disabled' : 'Active',
      created: serviceAccountDateLabel(account.createdAt),
      updated: serviceAccountDateLabel(account.updatedAt),
      actions: '',
    },
    columnTitles: {
      created: account.createdAt || '',
      updated: account.updatedAt || '',
    },
    sortValues: {
      created: account.createdAt || '',
      updated: account.updatedAt || '',
    },
    actions: [{ label: 'Manage credentials', action: 'select', icon: 'details', disabled: busy }],
  }))
}

function serviceAccountListColumns(): EntityListColumn[] {
  return [
    { id: 'name', label: 'Service account', width: '42%' },
    { id: 'status', label: 'Status', width: '16%', render: 'status' },
    { id: 'created', label: 'Created', width: '18%', render: 'datetime' },
    { id: 'updated', label: 'Updated', width: '18%', render: 'datetime' },
    { id: 'actions', label: 'Actions', width: '6%', align: 'right', sortable: false, render: 'actions' },
  ]
}

function serviceSecretListItems(secrets: ServiceAccountSecretSignal[], busy: boolean): EntityListItem[] {
  return secrets.map((secret) => {
    const expired = Boolean(secret.expiresAt && Date.parse(secret.expiresAt) <= Date.now())
    const status = secret.revokedAt ? 'Revoked' : expired ? 'Expired' : 'Active'
    return {
      id: secret.id,
      title: secret.name || secret.id,
      description: secret.id,
      icon: 'key',
      iconTreatment: 'plain',
      columns: {
        created: serviceAccountDateLabel(secret.createdAt),
        expires: secret.expiresAt ? serviceAccountDateLabel(secret.expiresAt) : 'Never',
        status,
        actions: '',
      },
      columnTitles: {
        created: secret.createdAt || '',
        expires: secret.expiresAt || '',
      },
      sortValues: {
        created: secret.createdAt || '',
        expires: secret.expiresAt || '',
      },
      actions: [
        { label: secret.revokedAt ? 'Credential revoked' : 'Revoke credential', action: 'revoke', icon: 'cancel', disabled: busy || Boolean(secret.revokedAt) },
        { label: 'Rotate credential', action: 'rotate', icon: 'refresh', disabled: busy || Boolean(secret.revokedAt) },
      ],
    }
  })
}

function serviceSecretListColumns(): EntityListColumn[] {
  return [
    { id: 'name', label: 'Credential', width: '38%' },
    { id: 'created', label: 'Created', width: '20%', render: 'datetime' },
    { id: 'expires', label: 'Expires', width: '20%', render: 'datetime' },
    { id: 'status', label: 'Status', width: '16%', render: 'status' },
    { id: 'actions', label: 'Actions', width: '6%', align: 'right', sortable: false, render: 'actions' },
  ]
}

function serviceAccountDateLabel(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return value
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric', year: 'numeric' }).format(date)
}

function serviceSecretExpirationOptions(): Array<{ value: ServiceSecretExpirationPreset, label: string }> {
  return ([30, 60, 90] as const).map((days) => ({
    value: String(days) as ServiceSecretExpirationPreset,
    label: `${days} days (${serviceEndOfDayInDays(days).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })})`,
  })).concat([
    { value: 'custom', label: 'Custom date' },
  ])
}

function serviceEndOfDayInDays(days: number): Date {
  const date = new Date()
  date.setDate(date.getDate() + days)
  date.setHours(23, 59, 59, 999)
  return date
}

function serviceDateInputValueInDays(days: number): string {
  const date = serviceEndOfDayInDays(days)
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

class LeapViewServiceAccounts extends DatastarLit(LitElement) {
  static styles = [pageHeaderStyles, entityDetailStyles, settingsSurfaceStyles]
  @property({ type: Boolean }) createAccountOpen = false
  @state() private busy = false
  @state() private commandError = ''
  @state() private createSecretOpen = false
  @state() private deleteAccountOpen = false
  @state() private pendingSecretRevocation: ServiceAccountSecretSignal | null = null
  @state() private secretExpirationPreset: ServiceSecretExpirationPreset = '90'
  @state() private secretCustomExpiration = ''
  private pendingSignalKey = ''
  private pendingAction = ''

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    super.disconnectedCallback()
  }

  get accounts(): ServiceAccountsSignal { return this.signal('adminServiceAccounts', { items: [], secrets: [], loading: false, hasMore: false }) }

  override updated(): void {
    const signal = this.accounts
    const key = JSON.stringify(signal)
    if (this.busy && key !== this.pendingSignalKey) this.completePendingCommand()
    this.pendingSignalKey = key
    this.openDialog('[data-service-account-dialog="create"]', this.createAccountOpen)
    this.openDialog('[data-service-account-dialog="secret"]', this.createSecretOpen)
    this.openDialog('[data-service-account-dialog="delete"]', this.deleteAccountOpen)
    this.openDialog('[data-service-account-dialog="revoke"]', Boolean(this.pendingSecretRevocation))
  }

  render() {
    const signal = this.accounts
    const selected = signal.items?.find((account) => account.id === signal.selectedId)
    return selected ? this.renderDetail(signal, selected) : this.renderList(signal)
  }

  private renderList(signal: ServiceAccountsSignal) {
    const feedback = this.renderFeedback(signal)
    return html`<section class="service-page" aria-label="Service accounts">
      ${renderPageHeader('Service accounts', 'Manage machine identities and credentials.')}
      ${feedback}
      ${signal.loading && !signal.items?.length ? html`<p class="muted" aria-live="polite">Loading service accounts…</p>` : html`
        <lv-entity-list
          .items=${serviceAccountListItems(signal.items ?? [], this.busy)}
          .columns=${serviceAccountListColumns()}
          .actions=${[{ id: 'create-service-account', label: 'Create service account', emphasis: 'primary' }]}
          client-filter
          list-label="Service accounts"
          search-placeholder="Search service accounts"
          empty-text="No service accounts have been created."
          @lv-entity-list-action=${this.handleListAction}
          @lv-entity-list-row-action=${this.handleAccountRowAction}
        ></lv-entity-list>
      `}
      ${this.renderCreateAccountDialog()}
    </section>`
  }

  private renderDetail(signal: ServiceAccountsSignal, account: ServiceAccountSignal) {
    return html`${renderEntityDetail({
      label: 'Service account details',
      feedback: this.renderFeedback(signal),
      backHref: '/admin/service-accounts',
      backLabel: 'All service accounts',
      avatar: lucideIcon(Bot, { size: 32, strokeWidth: 1.75 }),
      avatarTreatment: 'plain',
      title: account.displayName || account.id,
      subtitle: account.id,
      badges: html`<span class="badge" data-service-account-status>${account.disabledAt ? 'Disabled' : 'Active'}</span>`,
      actions: html`
        ${account.disabledAt
          ? html`<button class="service-detail-action" type="button" ?disabled=${this.busy} @click=${() => this.toggleAccount(account)}>${lucideIcon(Info, { size: 15, strokeWidth: 2 })}<span>Enable access</span></button>`
          : html`<button class="service-detail-action" type="button" ?disabled=${this.busy} @click=${() => this.toggleAccount(account)}>${lucideIcon(CircleSlash2, { size: 15, strokeWidth: 2 })}<span>Disable access</span></button>`}
        <button class="service-detail-action" type="button" ?disabled=${this.busy} @click=${() => this.revokeAll(account)}>Revoke all credentials</button>
        <button class="service-detail-action danger" type="button" ?disabled=${this.busy} @click=${() => { this.deleteAccountOpen = true }}>${lucideIcon(Trash2, { size: 15, strokeWidth: 2 })}<span>Delete service account</span></button>
      `,
      notice: signal.createdSecret ? this.renderCreatedSecret(signal.createdSecret) : nothing,
      sections: html`
        <section class="detail-section" aria-label="Overview">
          <h2>Overview</h2>
          <dl class="facts">
            <div class="fact"><dt>Service account ID</dt><dd><code>${account.id}</code></dd></div>
            <div class="fact"><dt>Status</dt><dd>${account.disabledAt ? 'Disabled' : 'Active'}</dd></div>
            <div class="fact"><dt>Created</dt><dd>${serviceAccountDateLabel(account.createdAt)}</dd></div>
            <div class="fact"><dt>Last updated</dt><dd>${serviceAccountDateLabel(account.updatedAt)}</dd></div>
          </dl>
        </section>
        <section class="detail-section" aria-label="Credentials">
          <div class="section-heading">
            <div class="card-header-copy"><h2>Credentials</h2><p class="muted">Create a separate credential for each application or automation.</p></div>
            <button class="service-detail-action primary" type="button" ?disabled=${this.busy} @click=${() => { this.createSecretOpen = true }}>${lucideIcon(Plus, { size: 15, strokeWidth: 2 })}<span>Create credential</span></button>
          </div>
          <lv-entity-list
            .items=${serviceSecretListItems(signal.secrets ?? [], this.busy)}
            .columns=${serviceSecretListColumns()}
            compact
            client-filter
            .showToolbar=${false}
            list-label="Service account credentials"
            empty-text="No credentials have been created."
            @lv-entity-list-row-action=${this.handleSecretRowAction}
          ></lv-entity-list>
        </section>
      `,
    })}
    ${this.renderCreateSecretDialog(account)}
    ${this.renderDeleteAccountDialog(account)}
    ${this.renderRevokeSecretDialog(account)}
    `
  }

  private renderFeedback(signal: ServiceAccountsSignal) {
    if (!signal.error && !this.commandError) return nothing
    return html`<div class="service-feedback">
      ${signal.error ? html`<p class="error" role="alert">${signal.error}</p>` : nothing}
      ${this.commandError ? html`<p class="error" role="alert">${this.commandError}</p>` : nothing}
    </div>`
  }

  private renderCreatedSecret(secret: string) {
    return html`<lv-one-time-secret secret=${secret} message="Copy this credential now. You will not be able to see it again." copy-label="Copy service account credential"></lv-one-time-secret>`
  }

  private renderCreateAccountDialog() {
    if (!this.createAccountOpen) return nothing
    return html`<dialog data-service-account-dialog="create" aria-labelledby="create-service-account-title" @cancel=${this.closeCreateAccount} @click=${this.closeCreateAccountOnBackdrop}>
      <section class="modal">
        <header class="modal-header">
          <div class="modal-title"><h2 id="create-service-account-title">Create service account</h2><p class="muted">Create a machine identity for an application, CLI, or automation.</p></div>
          <button class="modal-close" type="button" aria-label="Close" @click=${this.closeCreateAccount}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button>
        </header>
        <form class="modal-body" @submit=${this.createAccount}>
          <label class="service-dialog-field"><span>Display name</span><input id="service-account-display-name" aria-label="Display name" name="displayName" required autocomplete="off" placeholder="For example, Production deploy" ?disabled=${this.busy}></label>
          <div class="modal-actions"><button type="button" @click=${this.closeCreateAccount}>Cancel</button><button class="primary" type="submit" ?disabled=${this.busy}>${this.busy && this.pendingAction === 'create' ? 'Creating…' : 'Create service account'}</button></div>
        </form>
      </section>
    </dialog>`
  }

  private renderCreateSecretDialog(account: ServiceAccountSignal) {
    if (!this.createSecretOpen) return nothing
    const expirationOptions = serviceSecretExpirationOptions()
    return html`<dialog data-service-account-dialog="secret" aria-labelledby="create-service-secret-title" @cancel=${this.closeCreateSecret} @click=${this.closeCreateSecretOnBackdrop}>
      <section class="modal">
        <header class="modal-header">
          <div class="modal-title"><h2 id="create-service-secret-title">Create credential</h2><p class="muted">Create a credential for ${account.displayName || account.id}.</p></div>
          <button class="modal-close" type="button" aria-label="Close" @click=${this.closeCreateSecret}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button>
        </header>
        <form class="modal-body" @submit=${(event: SubmitEvent) => this.createSecret(event, account)}>
          <label class="service-dialog-field"><span>Credential name</span><input name="secretName" aria-label="Credential name" required autocomplete="off" placeholder="For example, CI pipeline" ?disabled=${this.busy}></label>
          <div class="service-dialog-field"><span>Expiration</span>
            <lv-select-menu class="service-expiration-control" label="Credential expiration" .options=${expirationOptions} .value=${this.secretExpirationPreset} @lv-select-change=${this.chooseSecretExpiration}>
              <span slot="leading">${lucideIcon(CalendarDays, { size: 16, strokeWidth: 2 })}</span>
            </lv-select-menu>
            ${this.secretExpirationPreset === 'custom' ? html`<input name="customExpiration" aria-label="Custom expiration date" type="date" min=${serviceDateInputValueInDays(1)} max=${serviceDateInputValueInDays(364)} .value=${this.secretCustomExpiration} @input=${this.updateSecretCustomExpiration} required>` : nothing}
          </div>
          <div class="modal-actions"><button type="button" @click=${this.closeCreateSecret}>Cancel</button><button class="primary" type="submit" ?disabled=${this.busy}>${this.busy && this.pendingAction === 'create_secret' ? 'Creating…' : 'Create credential'}</button></div>
        </form>
      </section>
    </dialog>`
  }

  private renderDeleteAccountDialog(account: ServiceAccountSignal) {
    if (!this.deleteAccountOpen) return nothing
    return html`<dialog data-service-account-dialog="delete" aria-labelledby="delete-service-account-title" @cancel=${this.closeDeleteAccount} @click=${this.closeDeleteAccountOnBackdrop}>
      <section class="modal">
        <header class="modal-header"><div class="modal-title"><h2 id="delete-service-account-title">Delete this service account?</h2></div><button class="modal-close" type="button" aria-label="Close" @click=${this.closeDeleteAccount}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button></header>
        <div class="service-delete-warning"><p>Applications using credentials for <strong>${account.displayName || account.id}</strong> will immediately lose access. You cannot undo this action.</p></div>
        <div class="service-delete-actions"><button type="button" @click=${this.closeDeleteAccount}>Cancel</button><button class="danger" type="button" ?disabled=${this.busy} @click=${() => this.confirmDeleteAccount(account)}>${this.busy && this.pendingAction === 'delete' ? 'Deleting…' : 'I understand, delete this service account'}</button></div>
      </section>
    </dialog>`
  }

  private renderRevokeSecretDialog(account: ServiceAccountSignal) {
    const secret = this.pendingSecretRevocation
    if (!secret) return nothing
    return html`<dialog data-service-account-dialog="revoke" aria-labelledby="revoke-service-secret-title" @cancel=${this.closeRevokeSecret} @click=${this.closeRevokeSecretOnBackdrop}>
      <section class="modal">
        <header class="modal-header"><div class="modal-title"><h2 id="revoke-service-secret-title">Revoke this credential?</h2></div><button class="modal-close" type="button" aria-label="Close" @click=${this.closeRevokeSecret}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button></header>
        <div class="service-delete-warning"><p>Applications using <strong>${secret.name}</strong> will immediately lose access. You cannot undo this action.</p></div>
        <div class="service-delete-actions"><button type="button" @click=${this.closeRevokeSecret}>Cancel</button><button class="danger" type="button" ?disabled=${this.busy} @click=${() => this.confirmRevokeSecret(account, secret)}>${this.busy && this.pendingAction === 'revoke_secret' ? 'Revoking…' : 'I understand, revoke this credential'}</button></div>
      </section>
    </dialog>`
  }

  private emit(detail: Record<string, unknown>): void {
    if (this.busy) return
    this.commandError = ''
    this.busy = true
    this.pendingAction = String(detail.action ?? '')
    this.pendingSignalKey = JSON.stringify(this.accounts)
    this.dispatchEvent(new CustomEvent('lv-service-account-command', { bubbles: true, composed: true, detail }))
  }

  private handleListAction = (event: CustomEvent<{ id: string }>): void => {
    if (event.detail.id === 'create-service-account' && !this.busy) this.createAccountOpen = true
  }

  private handleAccountRowAction = (event: CustomEvent<{ action: string, item: EntityListItem }>): void => {
    if (event.detail.action === 'select') this.emit({ action: 'select', accountId: event.detail.item.id })
  }

  private handleSecretRowAction = (event: CustomEvent<{ action: string, item: EntityListItem }>): void => {
    const secret = this.accounts.secrets?.find((candidate) => candidate.id === event.detail.item.id)
    if (!secret || secret.revokedAt) return
    if (event.detail.action === 'revoke') this.pendingSecretRevocation = secret
    if (event.detail.action === 'rotate') this.rotateSecret(secret)
  }

  private toggleAccount(account: ServiceAccountSignal): void {
    const action = account.disabledAt ? 'enable' : 'disable'
    if (window.confirm(`${action === 'disable' ? 'Disable' : 'Enable'} ${account.displayName || account.id}?`)) this.emit({ action, accountId: account.id })
  }

  private revokeAll(account: ServiceAccountSignal): void {
    if (window.confirm(`Revoke every credential for ${account.displayName || 'this service account'}? Every credential stops working immediately and cannot be restored.`)) this.emit({ action: 'revoke_all', accountId: account.id, reason: 'Settings operator action' })
  }

  private rotateSecret(secret: ServiceAccountSecretSignal): void {
    const accountID = this.accounts.selectedId || secret.servicePrincipalId
    if (window.confirm(`Rotate ${secret.name}? A replacement credential will be issued.`)) this.emit({ action: 'rotate_secret', accountId: accountID, secretId: secret.id, secretName: `${secret.name}-rotated`, revokePrevious: false })
  }

  private createAccount = (event: SubmitEvent): void => {
    event.preventDefault()
    const form = event.currentTarget as HTMLFormElement
    const displayName = (form.elements.namedItem('displayName') as HTMLInputElement).value.trim()
    if (displayName) this.emit({ action: 'create', displayName })
  }

  private createSecret(event: SubmitEvent, account: ServiceAccountSignal): void {
    event.preventDefault()
    const form = event.currentTarget as HTMLFormElement
    const secretName = (form.elements.namedItem('secretName') as HTMLInputElement).value.trim()
    const expiresAt = this.secretExpiration()
    if (!secretName || expiresAt === null) return
    this.emit({ action: 'create_secret', accountId: account.id, secretName, expiresAt })
  }

  private chooseSecretExpiration = (event: CustomEvent<{ value: ServiceSecretExpirationPreset }>): void => {
    this.secretExpirationPreset = event.detail.value
    if (event.detail.value !== 'custom') this.secretCustomExpiration = ''
  }

  private updateSecretCustomExpiration = (event: Event): void => {
    this.secretCustomExpiration = (event.currentTarget as HTMLInputElement).value
  }

  private secretExpiration(): string | null {
    if (this.secretExpirationPreset === 'custom') {
      if (!this.secretCustomExpiration) return null
      const date = new Date(`${this.secretCustomExpiration}T23:59:59.999`)
      return Number.isNaN(date.valueOf()) ? null : date.toISOString()
    }
    return serviceEndOfDayInDays(Number(this.secretExpirationPreset)).toISOString()
  }

  private confirmDeleteAccount(account: ServiceAccountSignal): void {
    this.emit({ action: 'delete', accountId: account.id })
  }

  private confirmRevokeSecret(account: ServiceAccountSignal, secret: ServiceAccountSecretSignal): void {
    this.emit({ action: 'revoke_secret', accountId: account.id, secretId: secret.id })
  }

  private closeCreateAccount = (event?: Event): void => { event?.preventDefault(); if (!this.busy) this.createAccountOpen = false }
  private closeCreateAccountOnBackdrop = (event: MouseEvent): void => { if (event.target === event.currentTarget) this.closeCreateAccount(event) }
  private closeCreateSecret = (event?: Event): void => { event?.preventDefault(); if (!this.busy) this.createSecretOpen = false }
  private closeCreateSecretOnBackdrop = (event: MouseEvent): void => { if (event.target === event.currentTarget) this.closeCreateSecret(event) }
  private closeDeleteAccount = (event?: Event): void => { event?.preventDefault(); if (!this.busy) this.deleteAccountOpen = false }
  private closeDeleteAccountOnBackdrop = (event: MouseEvent): void => { if (event.target === event.currentTarget) this.closeDeleteAccount(event) }
  private closeRevokeSecret = (event?: Event): void => { event?.preventDefault(); if (!this.busy) this.pendingSecretRevocation = null }
  private closeRevokeSecretOnBackdrop = (event: MouseEvent): void => { if (event.target === event.currentTarget) this.closeRevokeSecret(event) }

  private openDialog(selector: string, shouldOpen: boolean): void {
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>(selector)
    if (!shouldOpen || !dialog || dialog.open) return
    dialog.showModal()
    window.setTimeout(() => dialog.querySelector<HTMLElement>('input, button')?.focus(), 0)
  }

  private completePendingCommand(): void {
    const action = this.pendingAction
    this.busy = false
    this.pendingAction = ''
    if (action === 'create') this.createAccountOpen = false
    if (action === 'create_secret') {
      this.createSecretOpen = false
      this.secretExpirationPreset = '90'
      this.secretCustomExpiration = ''
    }
    if (action === 'delete') this.deleteAccountOpen = false
    if (action === 'revoke_secret') this.pendingSecretRevocation = null
  }

  private handleDatastarFetch = (event: Event): void => {
    if (!this.busy || !ownsAdminActionFetch(this, event)) return
    const detail = (event as CustomEvent<DatastarFetchOwnerDetail>).detail
    if (detail?.type === 'finished') {
      this.completePendingCommand()
      return
    }
    const failure = browserCommandFailure(event, 'Service account update')
    if (!failure) return
    this.busy = false
    this.pendingAction = ''
    this.commandError = failure.message
  }
}

if (!customElements.get('lv-project-registry')) customElements.define('lv-project-registry', LeapViewProjectRegistry)
if (!customElements.get('lv-service-accounts')) customElements.define('lv-service-accounts', LeapViewServiceAccounts)
if (!customElements.get('lv-principal-administration')) customElements.define('lv-principal-administration', LeapViewPrincipalAdministration)
if (!customElements.get('lv-group-administration')) customElements.define('lv-group-administration', LeapViewGroupAdministration)

export { LeapViewProjectRegistry, LeapViewServiceAccounts, LeapViewPrincipalAdministration, LeapViewGroupAdministration }
export { setDatastarLitRuntimeForTests } from '../shared/datastar-lit'
