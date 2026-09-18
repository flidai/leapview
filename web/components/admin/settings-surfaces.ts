import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Bot, CalendarDays, CircleSlash2, Info, Pencil, Plus, Trash2, UserPlus, X } from 'lucide'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure } from '../shared/command-failure'
import { entityDetailStyles, renderEntityDetail } from '../shared/entity-detail'
import { lucideIcon } from '../shared/lucide-icons'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import { settingsSurfaceStyles } from './settings-surfaces.styles'
import { identitySourceLabel, principalInitials, currentPrincipalAvatarUrl, initialsForValue, memberActionLabel, formatAccessDate, humanizeAccessValue, principalActivityLabel, formValue } from './settings-surfaces.helpers'
import type { AccessActivitySignal, AccessAdministrationSignal, AccessGroupSignal, AccessPrincipalSignal, AuditEventSignal, AuditLogFilters, AuditLogSignal, ServiceAccountSecretSignal, ServiceAccountSignal, ServiceAccountsSignal, ProjectRegistrySignal } from '../../generated/signals'
import '../shared/entity-list'
import '../shared/user-avatar'
import type { EntityListColumn, EntityListItem } from '../shared/entity-list'
import '../shared/entity-multi-select'
import '../shared/one-time-secret'
import '../shared/record-table'
import type { EntityMultiSelectItem } from '../shared/entity-multi-select'
import '../shared/select-menu'
import '../shared/drawer'

type DatastarFetchOwnerDetail = { type?: string; el?: Element }

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
  { id: 'access', label: 'Access changes', description: 'Recorded access-change events', filters: { action: 'access.changed' } },
  { id: 'credentials', label: 'Credentials', description: 'Service-principal credential events', filters: { resourceKind: 'service_principal_secret' } },
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

type ServiceSecretExpirationPreset = '30' | '60' | '90' | 'custom' | 'none'

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
      actions: [{ label: secret.revokedAt ? 'Credential revoked' : 'Revoke credential', action: 'revoke', icon: 'cancel', disabled: busy || Boolean(secret.revokedAt) }],
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
    { value: 'none', label: 'No expiration' },
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
  @state() private busy = false
  @state() private commandError = ''
  @state() private createAccountOpen = false
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
      actions: html`<button class="service-detail-action danger" type="button" ?disabled=${this.busy} @click=${() => { this.deleteAccountOpen = true }}>${lucideIcon(Trash2, { size: 15, strokeWidth: 2 })}<span>Delete service account</span></button>`,
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
            ${this.secretExpirationPreset === 'custom' ? html`<input name="customExpiration" aria-label="Custom expiration date" type="date" min=${serviceDateInputValueInDays(1)} .value=${this.secretCustomExpiration} @input=${this.updateSecretCustomExpiration} required>` : nothing}
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
    if (event.detail.action !== 'revoke') return
    const secret = this.accounts.secrets?.find((candidate) => candidate.id === event.detail.item.id)
    if (secret && !secret.revokedAt) this.pendingSecretRevocation = secret
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
    if (this.secretExpirationPreset === 'none') return ''
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
        <label class="audit-filter">Project<input id="audit-project-id" type="search" aria-label="Project" name="projectId" .value=${filters.projectId || ''} ?disabled=${disabled} placeholder="Project ID"></label>
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

if (!customElements.get('lv-project-registry')) customElements.define('lv-project-registry', LeapViewProjectRegistry)
if (!customElements.get('lv-service-accounts')) customElements.define('lv-service-accounts', LeapViewServiceAccounts)
if (!customElements.get('lv-audit-log')) customElements.define('lv-audit-log', LeapViewAuditLog)
if (!customElements.get('lv-principal-administration')) customElements.define('lv-principal-administration', LeapViewPrincipalAdministration)
if (!customElements.get('lv-group-administration')) customElements.define('lv-group-administration', LeapViewGroupAdministration)

export { LeapViewProjectRegistry, LeapViewServiceAccounts, LeapViewAuditLog, LeapViewPrincipalAdministration, LeapViewGroupAdministration }
export { setDatastarLitRuntimeForTests } from '../shared/datastar-lit'
