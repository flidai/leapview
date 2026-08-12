import { LitElement, css, html, nothing } from 'lit'
import { DatastarLit } from '../shared/datastar-lit'
import type { AuditLogSignal, ServiceAccountSignal, ServiceAccountsSignal, WorkspaceRegistrySignal } from '../../generated/signals'

const tableStyles = css`
  :host { display: block; color: var(--lv-fg-default); font: var(--lv-type-body); font-family: var(--fontStack-system); }
  .surface { display: grid; gap: 12px; min-width: 0; }
  h2, p { margin: 0; }
  h2 { font: var(--lv-type-section-title); }
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
  .badge { display: inline-flex; padding: var(--base-size-2) var(--base-size-6); border-radius: var(--lv-radius-full); background: var(--lv-bg-selected); font: var(--lv-type-caption); }
  .actions { display: flex; flex-wrap: wrap; gap: 6px; }
`

class LeapViewWorkspaceRegistry extends DatastarLit(LitElement) {
  static styles = tableStyles
  get registry(): WorkspaceRegistrySignal { return this.signal('adminWorkspaces', { items: [], loading: false, hasMore: false }) }
  render() {
    const signal = this.registry
    return html`<section class="surface" aria-label="Workspaces">
      <h2>Workspaces</h2>
      ${signal.error ? html`<p class="error" role="alert">${signal.error}</p>` : nothing}
      ${signal.loading ? html`<p class="muted" aria-live="polite">Loading workspaces…</p>` : nothing}
      ${signal.items?.length ? html`<div class="table-wrap"><table>
        <thead><tr><th>Name</th><th>Owner</th><th>Administrators</th><th>Deployment</th><th>Links</th></tr></thead>
        <tbody>${signal.items.map((item) => html`<tr>
          <td><a href=${item.href}>${item.title || item.id}</a>${item.description ? html`<div class="muted">${item.description}</div>` : nothing}<div class="muted">${item.environment || '—'}</div></td>
          <td>${item.owner ? html`${item.owner.displayName}${item.owner.email ? html`<div class="muted">${item.owner.email}</div>` : nothing}` : html`<span class="muted">Unassigned</span>`}</td>
          <td>${item.administrators?.length ? item.administrators.map((admin) => html`<div>${admin.displayName}<span class="muted"> · ${admin.role || 'admin'}</span></div>`) : html`<span class="muted">None</span>`}</td>
          <td><span class="badge">${item.deploymentStatus || item.servingStateStatus || 'Not deployed'}</span>${item.currentDeploymentId ? html`<div class="muted">${item.currentDeploymentId}</div>` : nothing}</td>
          <td class="actions"><a href=${item.links.workspace}>Open</a>${item.links.connections ? html`<a href=${item.links.connections}>Connections</a>` : nothing}${item.links.publications ? html`<a href=${item.links.publications}>Publications</a>` : nothing}</td>
        </tr>`)}</tbody>
      </table></div>` : html`<p class="empty">${signal.empty || 'No workspaces are available.'}</p>`}
    </section>`
  }
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

export { LeapViewWorkspaceRegistry, LeapViewServiceAccounts, LeapViewAuditLog }
export { setDatastarLitRuntimeForTests } from '../shared/datastar-lit'
