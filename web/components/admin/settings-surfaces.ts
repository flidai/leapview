import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { CircleSlash2, Info, Pencil, UserPlus, X } from 'lucide'
import { DatastarLit } from '../shared/datastar-lit'
import { entityDetailStyles, renderEntityDetail } from '../shared/entity-detail'
import { lucideIcon } from '../shared/lucide-icons'
import type { AccessActivitySignal, AccessAdministrationSignal, AccessGroupSignal, AccessPrincipalSignal, AuditLogSignal, ServiceAccountSignal, ServiceAccountsSignal, WorkspaceRegistrySignal } from '../../generated/signals'
import '../shared/entity-list'
import type { EntityListColumn, EntityListItem } from '../shared/entity-list'
import '../shared/entity-multi-select'
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
  .danger { color: var(--lv-fg-danger); }
  code { overflow-wrap: anywhere; }
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
  .facts { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); column-gap: var(--base-size-48); row-gap: var(--base-size-16); margin: 0; }
  .fact { display: grid; min-width: 0; grid-template-columns: minmax(7rem, 0.8fr) minmax(0, 1.2fr); align-items: baseline; gap: var(--base-size-16); }
  .fact dt { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .fact dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
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
  @media (max-width: 760px) {
    .facts { grid-template-columns: minmax(0, 1fr); }
    .fact { grid-template-columns: minmax(6.5rem, 0.7fr) minmax(0, 1.3fr); }
    .detail-section { padding-block: var(--base-size-20); }
    .detail-empty-row { grid-template-columns: minmax(6.5rem, 0.7fr) minmax(0, 1.3fr); }
    .activity-item { grid-template-columns: 10px minmax(0, 1fr); }
    .activity-item > time { grid-column: 2; }
  }
  @media (max-width: 480px) {
    .fact, .detail-empty-row { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-4); }
  }
`

const emptyAccessAdministration: AccessAdministrationSignal = { principals: [], groups: [], workspaces: [], sessions: [], roleAssignments: [], activity: [], loading: true }

abstract class LeapViewAccessAdministrationBase extends DatastarLit(LitElement) {
  static styles = [entityDetailStyles, tableStyles]
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
    if (signal.loading && !signal.principals.length) return html`<p class="muted" aria-live="polite">Loading principals…</p>`
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
              <label>Email<input name="email" type="email" required autocomplete="off" placeholder="person@example.com"></label>
              <label>Display name<input name="displayName" required autocomplete="off" placeholder="Display name"></label>
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
    const workspaces = new Map((signal.workspaces || []).map((workspace) => [workspace.id, workspace.name || workspace.id]))
    const actions = html`
      ${principal.capabilities.canBlock ? html`<button class="primary-detail-action" @click=${() => this.blockPrincipal(principal)}>${lucideIcon(CircleSlash2, { size: 16, strokeWidth: 2 })}<span>Block access</span></button>` : nothing}
      ${principal.capabilities.canUnblock ? html`<button class="primary-detail-action" @click=${() => this.emit({ action: 'unblock_principal', principalId: principal.id })}>Unblock access</button>` : nothing}
      ${principal.capabilities.canResetPassword || (principal.capabilities.canManageSessions && signal.sessions.length) || principal.capabilities.canDelete ? html`
        <details class="action-menu">
          <summary>More actions</summary>
          <div class="action-menu-popover">
            ${principal.capabilities.canResetPassword ? html`<button @click=${() => this.resetPassword(principal)}>Reset password</button>` : nothing}
            ${principal.capabilities.canManageSessions && signal.sessions.length ? html`<button @click=${() => this.revokeAllSessions(principal)}>Revoke all sessions</button>` : nothing}
            ${principal.capabilities.canDelete ? html`<button class="danger" @click=${() => this.deletePrincipal(principal)}>Delete user</button>` : nothing}
          </div>
        </details>` : nothing}`
    const notice = principal.identitySource === 'external' ? html`<div class="detail-notice" role="note"><span class="detail-notice-icon" aria-hidden="true">${lucideIcon(Info, { size: 18, strokeWidth: 2 })}</span><p><strong>${source} owns this identity.</strong> Profile fields and synchronized memberships are read-only in LeapView. Block access locally or revoke sessions here; update or permanently remove the user in ${source}.</p></div>`
      : principal.identitySource === 'system' ? html`<div class="detail-notice" role="note"><span class="detail-notice-icon" aria-hidden="true">${lucideIcon(Info, { size: 18, strokeWidth: 2 })}</span><p><strong>System-managed account.</strong> Profile fields are read-only because this account is provisioned by LeapView configuration. Block access locally or revoke sessions here; update it through its provisioning source.</p></div>` : nothing
    return renderEntityDetail({
      label: 'User administration', feedback: this.feedback(), backHref: '/admin/principals', backLabel: 'All users',
      avatar: principalInitials(principal), title: principal.displayName || principal.email || principal.id, subtitle: principal.email,
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
          <div class="detail-subsection">${signal.roleAssignments.length ? html`<h3>Workspace roles</h3><div class="table-wrap"><table><thead><tr><th>Workspace</th><th>Role</th><th>Granted through</th></tr></thead><tbody>${signal.roleAssignments.map((assignment) => html`<tr><td>${workspaces.get(assignment.workspaceId) || assignment.workspaceId}</td><td>${humanizeAccessValue(assignment.role)}</td><td>${assignment.sourceType === 'group' ? html`<a href=${`/admin/groups/${encodeURIComponent(assignment.sourceId)}`}>Via ${assignment.sourceName}</a>` : 'Direct assignment'}</td></tr>`)}</tbody></table></div>` : html`<div class="detail-empty-row"><strong>Workspace roles</strong><span>No workspace roles assigned.</span></div>`}</div>
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
    const workspaces = signal.workspaces || []
    const selectedWorkspace = workspaces[0]?.id || ''
    return html`<dialog data-access-create-dialog aria-labelledby="create-group-title" @cancel=${this.cancelCreate} @click=${this.closeOnBackdrop}>
      <section class="modal">
        <header class="modal-header">
          <div class="modal-title"><h2 id="create-group-title">Create group</h2><p class="muted">Organize users and assign access as a team.</p></div>
          <button class="modal-close" type="button" aria-label="Close create group" @click=${this.requestCreateClose}>${lucideIcon(X, { size: 18 })}</button>
        </header>
        <div class="modal-body">
          ${this.feedback()}
          ${!workspaces.length ? html`<p class="notice">Create a workspace before creating a group.</p>` : html`
            <form class="form" @submit=${(event: SubmitEvent) => this.createGroup(event)}>
              <label>Workspace<select name="workspaceId" aria-label="Workspace" required><option value="">Select a workspace</option>${workspaces.map((workspace) => html`<option value=${workspace.id} ?selected=${workspace.id === selectedWorkspace}>${workspace.name || workspace.id}</option>`)}</select></label>
              <label>Group name<input name="displayName" required autocomplete="off" placeholder="Analytics team"></label>
              <div class="modal-actions"><button type="button" @click=${this.requestCreateClose}>Cancel</button><button class="primary" type="submit" ?disabled=${signal.loading}>Create group</button></div>
            </form>
          `}
        </div>
      </section>
    </dialog>`
  }

  private renderDetail(signal: AccessAdministrationSignal, group: AccessGroupSignal) {
    const memberIDs = new Set(group.members.map((member) => member.id))
    const candidates = signal.principals.filter((principal) => principal.kind === 'user' && !memberIDs.has(principal.id))
    const workspaces = new Map((signal.workspaces || []).map((workspace) => [workspace.id, workspace.name || workspace.id]))
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
      subtitle: group.workspaceId ? `${workspaces.get(group.workspaceId) || group.workspaceId} workspace` : group.externalId ? `External ID ${group.externalId}` : undefined,
      badges: html`<span class="badge">${provider}</span><span class="badge status-active">${local ? 'Managed in LeapView' : 'Synchronized'}</span>`,
      actions, notice,
      sections: html`
        <section class="detail-section" aria-labelledby="group-overview-title">
          <div class="section-heading"><h2 id="group-overview-title">Overview</h2></div>
          <dl class="facts">
            <div class="fact"><dt>Provider</dt><dd>${provider}</dd></div>
            <div class="fact"><dt>Workspace</dt><dd>${group.workspaceId ? workspaces.get(group.workspaceId) || group.workspaceId : 'Global'}</dd></div>
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
              <label>Group name<input name="displayName" required autocomplete="off" .value=${group.name}></label>
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

  private createGroup(event: SubmitEvent): void { event.preventDefault(); const form = event.currentTarget as HTMLFormElement; this.emit({ action: 'create_group', workspaceId: formValue(form, 'workspaceId'), displayName: formValue(form, 'displayName') }) }
  private updateGroup(event: SubmitEvent, group: AccessGroupSignal): void { event.preventDefault(); this.emit({ action: 'update_group', groupId: group.id, workspaceId: group.workspaceId, displayName: formValue(event.currentTarget as HTMLFormElement, 'displayName'), revision: group.revision }); this.closeDetailDialog() }
  private memberPickerItems(candidates: AccessPrincipalSignal[]): EntityMultiSelectItem[] {
    return candidates.map((principal) => ({ id: principal.id, label: principal.displayName || principal.email, detail: principal.displayName ? principal.email : '', kind: 'principal' }))
  }
  private readonly updateSelectedMembers = (event: CustomEvent<{ selectedIds: string[] }>): void => { this.selectedMemberIds = event.detail.selectedIds }
  private addMembers(event: SubmitEvent, group: AccessGroupSignal): void {
    event.preventDefault()
    if (this.selectedMemberIds.length === 0) return
    this.emit({ action: 'add_group_member', groupId: group.id, workspaceId: group.workspaceId, principalIds: [...this.selectedMemberIds] })
    this.closeDetailDialog()
  }
  private removeMember(group: AccessGroupSignal, principalId: string, label: string): void { if (window.confirm(`Remove ${label} from ${group.name}?`)) this.emit({ action: 'remove_group_member', groupId: group.id, workspaceId: group.workspaceId, principalId }) }
  private deleteGroup(group: AccessGroupSignal): void { if (window.confirm(`Delete ${group.name}? Access granted through this group will be removed.`)) this.emit({ action: 'delete_group', groupId: group.id, workspaceId: group.workspaceId }) }
}

function identitySourceLabel(principal: AccessPrincipalSignal): string {
  if (principal.identitySource === 'local') return 'Local'
  if (principal.identityProvider) return principal.identityProvider.toUpperCase()
  return principal.identitySource || 'System'
}

function principalInitials(principal: AccessPrincipalSignal): string {
  return initialsForValue(principal.displayName || principal.email || principal.id)
}

function initialsForValue(value: string): string {
  const words = value.trim().split(/\s+/).filter(Boolean)
  return (words.length > 1 ? `${words[0][0]}${words[1][0]}` : value.slice(0, 2)).toUpperCase()
}

function memberActionLabel(count: number): string {
  if (count === 0) return 'Add members'
  return `Add ${count} ${count === 1 ? 'member' : 'members'}`
}

function formatAccessDate(value?: string): string {
  if (!value) return 'Never'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(parsed)
}

function humanizeAccessValue(value: string): string {
  const normalized = value.replace(/[._-]+/g, ' ').trim()
  return normalized ? normalized[0].toUpperCase() + normalized.slice(1) : '—'
}

function principalActivityLabel(activity: AccessActivitySignal): string {
  const actor = activity.actorName || activity.actorId || 'System'
  const labels: Record<string, string> = {
    'principal.local_user.created': 'created the local user',
    'principal.updated': 'updated the user profile',
    'principal.local_password.reset': 'reset the local password',
    'principal.blocked': 'blocked access',
    'principal.unblocked': 'unblocked access',
    'principal.sessions.revoked': 'revoked all sessions',
  }
  return `${actor} ${labels[activity.action] || humanizeAccessValue(activity.action).toLowerCase()}`
}

function formValue(form: HTMLFormElement, name: string): string {
  return (form.elements.namedItem(name) as HTMLInputElement | HTMLSelectElement | null)?.value.trim() || ''
}

class LeapViewWorkspaceRegistry extends DatastarLit(LitElement) {
  static styles = tableStyles
  get registry(): WorkspaceRegistrySignal { return this.signal('adminWorkspaces', { items: [], loading: false, hasMore: false }) }
  render() {
    const signal = this.registry
    return html`<section class="surface" aria-label="Workspaces">
      ${signal.error ? html`<p class="error" role="alert">${signal.error}</p>` : nothing}
      ${signal.loading ? html`<p class="muted" aria-live="polite">Loading workspaces…</p>` : nothing}
      <lv-entity-list
        .items=${workspaceListItems(signal)}
        .columns=${workspaceListColumns()}
        client-filter
        search-placeholder="Search workspaces by name, owner, or environment"
        list-label="Workspaces"
        empty-text=${signal.empty || 'No workspaces are available.'}
      ></lv-entity-list>
    </section>`
  }
}

function workspaceListItems(signal: WorkspaceRegistrySignal): EntityListItem[] {
  return (signal.items ?? []).map((item) => {
    const administrators = (item.administrators ?? []).map((administrator) => administrator.displayName).filter(Boolean)
    const deployment = item.deploymentStatus || item.servingStateStatus || 'Not deployed'
    return {
      id: item.id,
      title: item.title || item.id,
      description: item.description,
      href: item.href || item.links.workspace,
      icon: 'workspace',
      iconTreatment: 'plain',
      columns: {
        owner: item.owner?.displayName || 'Unassigned',
        administrators: administrators.length ? administrators.join(', ') : 'None',
        environment: item.environment || '—',
        deployment,
        updated: formatWorkspaceDate(item.updatedAt),
      },
      columnTitles: {
        owner: item.owner?.email || item.owner?.displayName || '',
        administrators: administrators.join(', '),
        deployment: item.currentDeploymentId || deployment,
        updated: item.updatedAt || '',
      },
      sortValues: {
        updated: workspaceTimestamp(item.updatedAt),
      },
    }
  })
}

function workspaceListColumns(): EntityListColumn[] {
  return [
    { id: 'name', label: 'Name', width: '27%' },
    { id: 'owner', label: 'Owner', width: '18%' },
    { id: 'administrators', label: 'Administrators', width: '18%' },
    { id: 'environment', label: 'Environment', width: '13%' },
    { id: 'deployment', label: 'Deployment', width: '13%' },
    { id: 'updated', label: 'Updated', width: '11%' },
  ]
}

function formatWorkspaceDate(value = ''): string {
  const timestamp = workspaceTimestamp(value)
  if (!timestamp) return '—'
  return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' }).format(timestamp)
}

function workspaceTimestamp(value = ''): number {
  const timestamp = Date.parse(value)
  return Number.isNaN(timestamp) ? 0 : timestamp
}

class LeapViewServiceAccounts extends DatastarLit(LitElement) {
  static styles = tableStyles
  get accounts(): ServiceAccountsSignal { return this.signal('adminServiceAccounts', { items: [], secrets: [], loading: false, hasMore: false }) }
  private emit(detail: Record<string, unknown>) { this.dispatchEvent(new CustomEvent('lv-service-account-command', { bubbles: true, composed: true, detail })) }
  render() {
    const signal = this.accounts
    return html`<section class="surface" aria-label="Service accounts">
      <h2>Service accounts</h2>
      ${signal.error ? html`<p class="error" role="alert">${signal.error}</p>` : nothing}
      <form class="form" @submit=${(event: SubmitEvent) => { event.preventDefault(); const form = event.currentTarget as HTMLFormElement; const input = form.elements.namedItem('displayName') as HTMLInputElement; this.emit({ action: 'create', displayName: input.value }); input.value = '' }}>
        <label>New account<input name="displayName" required placeholder="Display name"></label><button type="submit">Create</button>
      </form>
      ${signal.createdSecret ? html`<p role="status"><strong>Copy this secret now:</strong> <code>${signal.createdSecret}</code></p>` : nothing}
      ${signal.items?.length ? html`<div class="table-wrap"><table><thead><tr><th>Account</th><th>Status</th><th>Secrets</th><th>Actions</th></tr></thead><tbody>
        ${signal.items.map((account) => html`<tr>
          <td><strong>${account.displayName || account.id}</strong><div class="muted">${account.id}</div></td><td>${account.disabledAt ? 'Disabled' : 'Active'}</td>
          <td>${account.id === signal.selectedId ? (signal.secrets?.length || 0) : '—'}</td>
          <td class="actions"><button @click=${() => this.emit({ action: 'select', accountId: account.id })}>Secrets</button><button @click=${() => this.deleteAccount(account)}>Delete</button></td>
        </tr>`)}</tbody></table></div>` : html`<p class="empty">No service accounts have been created.</p>`}
      ${signal.selectedId ? html`<div><h3>Secrets</h3><form class="form" @submit=${(event: SubmitEvent) => { event.preventDefault(); const form = event.currentTarget as HTMLFormElement; const name = (form.elements.namedItem('secretName') as HTMLInputElement).value; this.emit({ action: 'create_secret', accountId: signal.selectedId, secretName: name }); }}><label>Secret name<input name="secretName" required placeholder="CI pipeline"></label><button type="submit">Create secret</button></form>${signal.secrets?.length ? html`<div class="table-wrap"><table><thead><tr><th>Name</th><th>Created</th><th>Expires</th><th></th></tr></thead><tbody>${signal.secrets.map((secret) => html`<tr><td>${secret.name}</td><td>${secret.createdAt || '—'}</td><td>${secret.expiresAt || 'Never'}</td><td><button ?disabled=${Boolean(secret.revokedAt)} @click=${() => this.emit({ action: 'revoke_secret', accountId: signal.selectedId, secretId: secret.id })}>${secret.revokedAt ? 'Revoked' : 'Revoke'}</button></td></tr>`)}</tbody></table></div>` : html`<p class="empty">No secrets have been created.</p>`}</div>` : nothing}
    </section>`
  }

  private deleteAccount(account: ServiceAccountSignal): void {
    if (window.confirm(`Delete ${account.displayName || account.id}? Its credentials will stop working immediately.`)) {
      this.emit({ action: 'delete', accountId: account.id })
    }
  }
}

class LeapViewAuditLog extends DatastarLit(LitElement) {
  static styles = tableStyles
  get audit(): AuditLogSignal { return this.signal('adminAuditLog', { items: [], filters: {}, loadedCount: 0, loading: false, hasMore: false }) }
  private emit(detail: Record<string, unknown>) { this.dispatchEvent(new CustomEvent('lv-audit-log-command', { bubbles: true, composed: true, detail })) }
  render() {
    const signal = this.audit
    const filters = signal.filters || {}
    const submit = (event: SubmitEvent) => { event.preventDefault(); const form = event.currentTarget as HTMLFormElement; const value = (name: string) => (form.elements.namedItem(name) as HTMLInputElement)?.value || ''; this.emit({ action: 'filter', filters: { workspaceId: value('workspaceId'), principalId: value('principalId'), action: value('action'), targetType: value('targetType'), targetId: value('targetId'), from: value('from'), to: value('to') } }) }
    return html`<section class="surface" aria-label="Audit log"><h2>Audit log</h2><p class="muted">Read-only product activity.</p>${signal.error ? html`<p class="error" role="alert">${signal.error}</p>` : nothing}
      <form class="toolbar" @submit=${submit}><label>Workspace<input name="workspaceId" value=${filters.workspaceId || ''}></label><label>Actor<input name="principalId" value=${filters.principalId || ''}></label><label>Action<input name="action" value=${filters.action || ''}></label><label>Target type<input name="targetType" value=${filters.targetType || ''}></label><button type="submit">Filter</button><button type="button" @click=${() => this.emit({ action: 'clear', filters: {} })}>Clear</button></form>
      ${signal.items?.length ? html`<div class="table-wrap"><table><thead><tr><th>Time</th><th>Action</th><th>Actor</th><th>Target</th><th>Status</th></tr></thead><tbody>${signal.items.map((event) => html`<tr><td>${event.createdAt}</td><td>${event.action}</td><td>${event.principalId || 'System'}</td><td>${event.targetType} / ${event.targetId}</td><td>${event.status || '—'}</td></tr>`)}</tbody></table></div>` : html`<p class="empty">No audit events match these filters.</p>`}
      ${signal.hasMore ? html`<button @click=${() => this.emit({ action: 'load_more', filters, pageToken: signal.nextCursor })}>Load more</button>` : nothing}
    </section>`
  }
}

if (!customElements.get('lv-workspace-registry')) customElements.define('lv-workspace-registry', LeapViewWorkspaceRegistry)
if (!customElements.get('lv-service-accounts')) customElements.define('lv-service-accounts', LeapViewServiceAccounts)
if (!customElements.get('lv-audit-log')) customElements.define('lv-audit-log', LeapViewAuditLog)
if (!customElements.get('lv-principal-administration')) customElements.define('lv-principal-administration', LeapViewPrincipalAdministration)
if (!customElements.get('lv-group-administration')) customElements.define('lv-group-administration', LeapViewGroupAdministration)

export { LeapViewWorkspaceRegistry, LeapViewServiceAccounts, LeapViewAuditLog, LeapViewPrincipalAdministration, LeapViewGroupAdministration }
export { setDatastarLitRuntimeForTests } from '../shared/datastar-lit'
