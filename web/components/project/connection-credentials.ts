import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { ConnectionCredentialCommandSignal, ConnectionCredentialSignal, ConnectionLifecycleSignal } from '../../generated/signals'
import { browserCommandFailure, ownsBrowserCommandFetch } from '../shared/command-failure'
import '../shared/drawer'

class LeapViewConnectionCredentials extends LitElement {
  @property({ attribute: false }) lifecycle!: ConnectionLifecycleSignal
  @property({ attribute: false }) credentials?: ConnectionCredentialSignal
  @state() private open = false
  @state() private versionId = ''
  @state() private operationId = ''
  @state() private pending = false
  @state() private failure = ''
  @state() private recoveryId = ''

  static styles = css`
    :host { display: inline-flex; }
    button, input, select { font: inherit; }
    button { border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding: var(--base-size-8) var(--base-size-12); cursor: pointer; }
    button:disabled { opacity: .6; cursor: default; }
    .body { display: grid; gap: var(--base-size-16); padding: var(--base-size-20); }
    form, section, label { display: grid; gap: var(--base-size-8); }
    section + section { border-top: var(--lv-border-muted); padding-top: var(--base-size-16); }
    input, select { box-sizing: border-box; width: 100%; min-width: 0; padding: var(--base-size-8); border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); }
    .actions { display: flex; flex-wrap: wrap; gap: var(--base-size-8); }
    p, h2 { margin: 0; font: inherit; }
    h2 { font-weight: var(--base-text-weight-semibold); }
    .hint { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    [role=alert] { color: var(--lv-fg-danger); }
  `

  connectedCallback() { super.connectedCallback(); document.addEventListener('datastar-fetch', this.fetchResult) }
  disconnectedCallback() { document.removeEventListener('datastar-fetch', this.fetchResult); super.disconnectedCallback() }
  updated(changed: Map<string, unknown>) {
    const previous = changed.get('lifecycle') as ConnectionLifecycleSignal | undefined
    if (previous && previous.logicalConnection !== this.lifecycle.logicalConnection) {
      this.open = false; this.versionId = ''; this.operationId = ''; this.recoveryId = ''; this.failure = ''; this.pending = false
    }
    if (changed.has('credentials')) {
      const value = this.current
      if (value?.operationId && (!this.operationId || value.operationId === this.operationId)) this.operationId = value.operationId
      if (value?.command.versionId) this.versionId = value.command.versionId
      if (!value?.status.loading) this.pending = false
    }
  }
  private get current() {
    const value = this.credentials
    return value?.command.logicalConnection === this.lifecycle?.logicalConnection && value.command.assetId === this.lifecycle?.assetId ? value : undefined
  }
  private get available() {
    const c = this.lifecycle
    return c?.credentialsAvailable === true && c.exists && c.enabled && c.canManage && c.connectorKind === 'postgres' && c.authenticationMode === 'external_bundle'
  }
  private get operation() { return this.operationId && this.current?.operationId === this.operationId ? this.current : undefined }
  private get terminal() { return this.operation?.phase === 'aborted' || (this.operation?.phase === 'completed' && this.operation.runtimeReady === true) }
  private get busy() { return !this.failure && (this.pending || this.current?.status.loading === true) }
  private command(action: string): ConnectionCredentialCommandSignal {
    return { action, assetId: this.lifecycle.assetId, logicalConnection: this.lifecycle.logicalConnection,
      versionId: this.versionId, receiptId: this.current?.receiptId ?? '',
      expectedRevision: this.current?.bindingRevision || this.lifecycle.revision, operationId: this.operationId,
      username: '', password: '', beforeVersionId: '' }
  }
  private emit(action: string, fields: Partial<ConnectionCredentialCommandSignal> = {}, read = false) {
    this.failure = ''
    if (!read) this.pending = true
    this.dispatchEvent(new CustomEvent(read ? 'lv-connection-credential-query' : 'lv-connection-credential-command', {
      bubbles: true, composed: true, detail: { ...this.command(action), ...fields },
    }))
  }
  private save(event: SubmitEvent) {
    event.preventDefault()
    const form = event.currentTarget as HTMLFormElement
    if (!form.reportValidity()) return
    const data = new FormData(form)
    const fields = { password: String(data.get('password') ?? '') }
    form.reset()
    if (this.terminal) { this.operationId = ''; this.versionId = ''; this.recoveryId = '' }
    this.emit('save', { ...fields, receiptId: '' })
  }
  private resetMissing() {
    if (this.operation?.phase !== 'not_found') return
    this.operationId = ''; this.recoveryId = ''; this.failure = ''; this.pending = false
    this.emit('list', { operationId: '', receiptId: '' }, true)
  }
  private prepare() {
    if (!this.operationId || this.terminal) this.operationId = crypto.randomUUID()
    this.emit('prepare')
  }
  private readonly fetchResult = (event: Event) => {
    let owner: Element = this
    let owns = false
    while (!owns) {
      owns = ownsBrowserCommandFetch(owner, event)
      const root = owner.getRootNode()
      if (owns || !('host' in root)) break
      owner = (root as ShadowRoot).host
    }
    if (!this.pending || !owns) return
    const failure = browserCommandFailure(event, 'Credential action')
    if (failure) { this.pending = false; this.failure = failure.message }
    if ((event as CustomEvent).detail?.type === 'finished') this.pending = false
  }
  render() {
    if (!this.available) return nothing
    const value = this.current
    const versionStatus = value?.versionStatus?.versionId === this.versionId ? value.versionStatus : undefined
    const retired = versionStatus?.state === 'retired_local'
    const operation = this.operation
    const inFlight = Boolean(this.operationId) && !this.terminal
    const receiptCurrent = Boolean(value?.receiptId) && value?.command.versionId === this.versionId && Date.parse(value?.receiptExpiresAt ?? '') > Date.now()
    return html`
      <button type="button" @click=${() => { this.open = true; this.emit('list', {}, true) }}>Credentials</button>
      ${this.open ? html`<lv-drawer open size="wide" label="Connection credentials" @lv-drawer-close=${() => { this.open = false }}>
        <span slot="title">Connection credentials</span>
        <span slot="subtitle">${this.lifecycle.logicalConnection}</span>
        <div class="body">
          ${this.failure || value?.status.error ? html`<p role="alert">${this.failure || 'Credential action could not complete. Check the current status before continuing.'}</p>` : nothing}
          <section>
            <h2>Save a credential draft</h2>
            <p class="hint">Saving and testing a draft do not activate it. Existing connection configuration is kept.</p>
            <form @submit=${this.save}>
              <p class="hint">Uses the existing connection user: ${this.lifecycle.sourceIdentity || 'configured source identity'}.</p>
              <label>Password<input name="password" type="password" autocomplete="new-password" required ?disabled=${inFlight}></label>
              <button type="submit" ?disabled=${this.busy || inFlight}>Save draft</button>
            </form>
          </section>
          <section>
            <h2>Choose and test a saved draft</h2>
            <label>Saved draft<select .value=${this.versionId} ?disabled=${inFlight} @change=${(event: Event) => { this.versionId = (event.target as HTMLSelectElement).value; if (this.terminal) this.operationId = '' }}>
              <option value="">Choose a draft</option>
              ${this.versionId && !value?.drafts.some(draft => draft.versionId === this.versionId) ? html`<option value=${this.versionId}>${this.versionId} · selected draft</option>` : nothing}
              ${(value?.drafts ?? []).map(draft => html`<option value=${draft.versionId}>${draft.versionId} · ${draft.createdAt}</option>`)}
            </select></label>
            <div class="actions">
              <button type="button" ?disabled=${this.busy} @click=${() => this.emit('list', {}, true)}>Refresh drafts</button>
              ${value?.nextBeforeVersionId ? html`<button type="button" ?disabled=${this.busy} @click=${() => this.emit('list', { beforeVersionId: value.nextBeforeVersionId }, true)}>Older drafts</button>` : nothing}
              <button type="button" ?disabled=${!this.versionId || retired || this.busy || operation?.phase === 'not_found'} @click=${() => this.emit('validate')}>Test draft</button>
            </div>
            ${receiptCurrent ? html`<p>Draft tested. Activation requires a separate confirmation.</p>` : nothing}
            ${!inFlight ? html`<button type="button" ?disabled=${!receiptCurrent || retired || this.busy} @click=${this.prepare}>Prepare activation</button>` : nothing}
          </section>
          <section>
            <h2>Version dependencies and retirement</h2>
            <p class="hint">Retiring a version blocks its future use on this instance. It does not revoke the upstream password or remove encrypted backup history. Retained activations, releases and agent configuration can prevent retirement.</p>
            <button type="button" ?disabled=${!this.versionId || this.busy} @click=${() => this.emit('version_status', {}, true)}>Inspect version dependencies</button>
            ${versionStatus ? html`<p>Version state: ${versionStatus.state}</p>
              ${versionStatus.dependencies.length ? html`<ul>${versionStatus.dependencies.map(dependency => html`<li>${dependency.kind} · ${dependency.id}</li>`)}</ul>` : nothing}
              ${versionStatus.moreDependencies ? html`<p>Additional retained dependencies also prevent retirement.</p>` : nothing}
              ${!inFlight && versionStatus.state === 'available' && !versionStatus.dependencies.length && !versionStatus.moreDependencies ? html`<button type="button" ?disabled=${this.busy} @click=${() => this.emit('retire')}>Retire version locally</button>` : nothing}
              ${retired ? html`<p>This version cannot be re-enabled. Save a new draft to use credentials again.</p>` : nothing}
            ` : nothing}
          </section>
          <section>
            <h2>Recover an activation</h2>
            <p class="hint">Keep the operation ID before leaving this page. Use it to check an interrupted activation after reopening the page.</p>
            <form @submit=${(event: SubmitEvent) => { event.preventDefault(); if (!(event.currentTarget as HTMLFormElement).reportValidity()) return; this.operationId = this.recoveryId; this.emit('status', {}, true) }}>
              <label>Operation ID<input name="operationId" .value=${this.recoveryId} @input=${(event: Event) => { this.recoveryId = (event.target as HTMLInputElement).value.trim() }} required maxlength="36" pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"></label>
              <button type="submit">Recover operation</button>
            </form>
          </section>
          ${this.operationId ? html`<section>
            <h2>Activation</h2>
            <label>Current operation ID<input readonly .value=${this.operationId}></label>
            <p>Status: ${operation?.phase || 'Awaiting confirmation'}</p>
            ${operation?.runtimeReady === true ? html`<p role="status">Credentials are active and runtime is ready.</p>` : html`<p class="hint">Runtime readiness has not been confirmed.</p>`}
            ${['preparing', 'prepared'].includes(operation?.phase ?? '') && !receiptCurrent ? html`<p class="hint">Test the selected draft again before continuing activation.</p>` : nothing}
            <div class="actions">
              <button type="button" @click=${() => this.emit('status', {}, true)}>Check activation status</button>
              ${operation?.phase === 'not_found' ? html`<p>The server confirmed this operation does not exist.</p><button type="button" @click=${this.resetMissing}>Start again</button>` : nothing}
              ${inFlight && operation?.phase !== 'not_found' ? html`<button type="button" ?disabled=${this.busy || (['preparing', 'prepared'].includes(operation?.phase ?? '') && !receiptCurrent)} @click=${() => this.emit('retry')}>${operation?.phase === 'prepared' ? 'Continue activation' : 'Recover activation'}</button>` : nothing}
              ${inFlight && ['preparing', 'prepared'].includes(operation?.phase ?? '') ? html`<button type="button" ?disabled=${this.busy} @click=${() => this.emit('abort')}>Cancel activation</button>` : nothing}
            </div>
          </section>` : nothing}
        </div>
      </lv-drawer>` : nothing}
    `
  }
}
if (!customElements.get('lv-connection-credentials')) customElements.define('lv-connection-credentials', LeapViewConnectionCredentials)
