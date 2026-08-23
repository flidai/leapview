import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { MoreHorizontal } from 'lucide'
import type {
  ConnectionAdministrationSignal,
  ConnectionLifecycleActionSignal,
  ConnectionLifecycleSignal,
} from '../../generated/signals'
import { lucideIcon } from '../shared/lucide-icons'
import { browserCommandFailure, type BrowserCommandFailure } from '../shared/command-failure'
import '../shared/drawer'
import '../shared/loading-spinner'

class LeapViewConnectionAdministration extends LitElement {
  @property({ attribute: false }) lifecycles: ConnectionLifecycleSignal[] = []
  @property({ attribute: false }) administration: ConnectionAdministrationSignal = emptyAdministration()
  @property() surface: 'list' | 'detail' = 'detail'
  @property() environment = ''
  @state() private drawerOpen = false
  @state() private selectedLogical = ''
  @state() private authenticationMode = 'external_bundle'
  @state() private terminalFailure: BrowserCommandFailure | null = null

  static get styles() {
    return css`
      :host { display: inline-flex; min-width: 0; align-items: center; gap: var(--base-size-8); }
      button, input, select, textarea { font: inherit; }
      .lifecycle-actions { display: inline-flex; min-width: 0; align-items: center; gap: var(--base-size-8); }
      .status-badge { display: inline-flex; min-height: var(--base-size-24); align-items: center; border: var(--lv-border-muted); border-radius: var(--lv-radius-full); background: var(--lv-bg-panel-muted); color: var(--lv-fg-muted); padding: 0 var(--base-size-8); font: var(--lv-type-caption); white-space: nowrap; }
      .status-badge[data-tone='success'] { border-color: var(--lv-line-success-muted); color: var(--lv-fg-success); }
      .status-badge[data-tone='warning'] { border-color: var(--lv-line-warning-muted); color: var(--lv-fg-warning); }
      .status-badge[data-tone='danger'] { border-color: var(--lv-line-danger-muted); color: var(--lv-fg-danger); }
      .button { display: inline-flex; min-height: var(--lv-button-height-sm); align-items: center; justify-content: center; gap: var(--base-size-6); border: var(--borderWidth-default) solid var(--lv-button-border-rest); border-radius: var(--lv-button-radius); background: var(--lv-button-bg-rest); color: var(--lv-button-fg-rest); padding: 0 var(--lv-button-padding-inline-sm); cursor: pointer; font: var(--lv-type-caption); font-weight: var(--base-text-weight-medium); white-space: nowrap; }
      .button.primary { border-color: var(--lv-button-accent-border-rest); background: var(--lv-button-accent-bg-rest); color: var(--lv-button-accent-fg-rest); }
      .button.danger { color: var(--lv-fg-danger); }
      .button:disabled { opacity: .6; cursor: wait; }
      .menu { position: relative; }
      .menu summary { display: inline-grid; width: var(--control-medium-size); height: var(--control-medium-size); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-muted); cursor: pointer; list-style: none; place-items: center; }
      .menu summary::-webkit-details-marker { display: none; }
      .menu-items { position: absolute; z-index: var(--z-index-dropdown); top: calc(100% + var(--base-size-4)); right: 0; display: grid; width: max-content; min-width: 10.5rem; gap: var(--base-size-2); border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); padding: var(--base-size-4); }
      .menu-items button { border: 0; border-radius: var(--lv-radius-default); background: transparent; color: var(--lv-fg-default); padding: var(--base-size-8); text-align: left; cursor: pointer; }
      .menu-items button:hover { background: var(--lv-bg-control-hover); }
      .menu-items button.danger { color: var(--lv-fg-danger); }
      .drawer-body { display: grid; gap: var(--base-size-16); padding: var(--base-size-16) var(--base-size-20) var(--base-size-24); }
      .form-section { display: grid; gap: var(--base-size-12); }
      .form-section + .form-section { border-top: var(--lv-border-muted); padding-top: var(--base-size-16); }
      .form-section h2 { margin: 0; color: var(--lv-fg-default); font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
      .credential-hint { margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
      .credential-hint.wide { grid-column: 1 / -1; }
      .form-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--base-size-12); }
      label { display: grid; min-width: 0; gap: var(--base-size-6); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
      label.wide { grid-column: 1 / -1; }
      input, select, textarea { width: 100%; min-width: 0; box-sizing: border-box; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding: var(--base-size-8) var(--base-size-12); }
      input, select { min-height: var(--control-medium-size); }
      input:read-only { background: var(--lv-bg-panel-muted); color: var(--lv-fg-muted); }
      textarea { min-height: 5.5rem; resize: vertical; font-family: var(--fontStack-monospace); }
      .form-status { border-radius: var(--lv-radius-default); padding: var(--base-size-8) var(--base-size-12); font: var(--lv-type-body-compact); }
      .form-status.error { border: var(--lv-border-danger); background: var(--lv-bg-danger-muted); color: var(--lv-fg-danger); }
      .form-status-actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--base-size-8); margin-top: var(--base-size-8); }
      .form-status-actions button { border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding: var(--base-size-4) var(--base-size-8); cursor: pointer; font: var(--lv-type-caption); }
      .terminal-failure { display: grid; gap: var(--base-size-6); border: var(--lv-border-danger); border-radius: var(--lv-radius-default); background: var(--lv-bg-danger-muted); color: var(--lv-fg-danger); padding: var(--base-size-8) var(--base-size-12); font: var(--lv-type-body-compact); }
      .drawer-footer { display: flex; align-items: center; justify-content: flex-end; gap: var(--base-size-8); border-top: var(--lv-border-muted); padding: var(--base-size-12) var(--base-size-20); }
      @media (max-width: 640px) { .form-grid { grid-template-columns: 1fr; } }
    `
  }

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    super.disconnectedCallback()
  }

  updated(changed: Map<string, unknown>): void {
    if (changed.has('administration') && this.administration.status.message) this.drawerOpen = false
  }

  render() {
    const lifecycle = this.selectedLifecycle()
    return html`
      ${this.terminalFailure && !this.drawerOpen ? this.renderTerminalFailure() : nothing}
      ${this.renderTrigger(lifecycle)}
      ${this.drawerOpen && lifecycle ? this.renderDrawer(lifecycle) : nothing}
    `
  }

  private renderTrigger(lifecycle?: ConnectionLifecycleSignal) {
    if (this.surface === 'list') {
      const missing = this.lifecycles.filter((item) => item.state === 'missing' && item.actions.some((action) => action.id === 'configure'))
      if (!missing.length) return nothing
      return html`<button class="button primary" type="button" @click=${() => this.openDrawer(missing[0])}>Configure connection</button>`
    }
    if (!lifecycle) return nothing
    const primary = lifecycle.actions.find((action) => action.primary)
    const secondary = lifecycle.actions.filter((action) => !action.primary)
    return html`
      <div class="lifecycle-actions">
        <span class="status-badge" data-tone=${lifecycle.tone}>${lifecycle.statusLabel}</span>
        ${primary ? this.renderAction(primary, lifecycle, true) : nothing}
        ${secondary.length ? html`
          <details class="menu">
            <summary aria-label="More connection actions">${lucideIcon(MoreHorizontal)}</summary>
            <div class="menu-items">${secondary.map((action) => this.renderAction(action, lifecycle, false))}</div>
          </details>
        ` : nothing}
      </div>
    `
  }

  private renderAction(action: ConnectionLifecycleActionSignal, lifecycle: ConnectionLifecycleSignal, primary: boolean) {
    return html`
      <button
        class=${primary ? 'button primary' : action.destructive ? 'danger' : ''}
        type="button"
        ?disabled=${this.commandBusy}
        @click=${() => this.handleAction(action, lifecycle)}
      >
        ${primary && this.commandBusy ? html`<lv-loading-spinner aria-hidden="true"></lv-loading-spinner>` : nothing}
        ${action.label}
      </button>
    `
  }

  private handleAction(action: ConnectionLifecycleActionSignal, lifecycle: ConnectionLifecycleSignal) {
    if (action.id === 'configure' || action.id === 'edit') {
      this.openDrawer(lifecycle)
      return
    }
    if (action.id === 'disable' && !window.confirm(`Disable ${lifecycle.logicalConnection}? Dependent sources will stop using this connection until it is enabled and tested again.`)) return
    this.dispatchEvent(new CustomEvent('lv-connection-administration-action', {
      bubbles: true,
      composed: true,
      detail: this.commandFor(lifecycle, action.id),
    }))
  }

  private openDrawer(lifecycle: ConnectionLifecycleSignal) {
    this.selectedLogical = lifecycle.logicalConnection
    this.authenticationMode = lifecycle.authenticationMode || 'external_bundle'
    this.drawerOpen = true
  }

  private renderDrawer(lifecycle: ConnectionLifecycleSignal) {
    const confirm = this.administration.command.confirmationToken && this.administration.command.logicalConnection === lifecycle.logicalConnection
    return html`
      <lv-drawer open size="wide" label=${`${lifecycle.exists ? 'Edit' : 'Configure'} ${lifecycle.logicalConnection}`} @lv-drawer-close=${() => { this.drawerOpen = false }}>
        <div slot="title">${lifecycle.exists ? 'Edit connection' : 'Configure connection'}</div>
        <p slot="subtitle">${lifecycle.logicalConnection} · ${this.environment || 'current environment'}</p>
        <form class="drawer-body" id="connection-configuration-form" @submit=${(event: SubmitEvent) => this.save(event, lifecycle)}>
          ${this.renderFailureStatus()}
          <section class="form-section">
            <h2>Connection</h2>
            <div class="form-grid">
              <label>Logical connection<input name="logicalConnection" .value=${lifecycle.logicalConnection} readonly></label>
              <label>Connector<input name="connectorKind" .value=${lifecycle.connectorKind} readonly></label>
            </div>
          </section>
          <section class="form-section">
            <h2>Endpoint</h2>
            <div class="form-grid">
              <label>Host<input name="host" .value=${lifecycle.host}></label>
              <label>Port<input name="port" inputmode="numeric" .value=${lifecycle.port}></label>
              <label>Database<input name="database" .value=${lifecycle.database}></label>
              <label>Object scope<input name="objectScope" .value=${lifecycle.objectScope}></label>
              <label>Source identity<input name="sourceIdentity" .value=${lifecycle.sourceIdentity}></label>
              <label>TLS mode<input name="tlsMode" .value=${lifecycle.tlsMode}></label>
              <label class="wide">Options (JSON)<textarea name="options" .value=${lifecycle.options}></textarea></label>
            </div>
          </section>
          <section class="form-section">
            <h2>Authentication</h2>
            <div class="form-grid">
              <label class="wide">Mode
                <select name="authenticationMode" .value=${this.authenticationMode} @change=${(event: Event) => { this.authenticationMode = (event.currentTarget as HTMLSelectElement).value }}>
                  <option value="external_bundle">External credential bundle</option>
                  <option value="workload_identity">Workload identity</option>
                  <option value="none">No credentials</option>
                </select>
              </label>
              ${this.authenticationMode === 'external_bundle' ? html`
                <p class="credential-hint wide">Credential references are write-only. Re-enter all four reference fields when saving a change.</p>
                <label>Credential project<input name="credentialProjectId" .value=${lifecycle.credentialProjectId} required></label>
                <label>Credential environment<input name="credentialEnvironment" .value=${lifecycle.credentialEnvironment} required></label>
                <label>Secret path<input name="secretPath" .value=${lifecycle.secretPath} placeholder="/connections/warehouse" required></label>
                <label>Secret key<input name="secretKey" .value=${lifecycle.secretKey} required></label>
              ` : nothing}
            </div>
          </section>
          <div class="drawer-footer">
          <button class="button" type="button" @click=${() => { this.drawerOpen = false }}>Cancel</button>
          <button class="button primary" type="submit" form="connection-configuration-form" ?disabled=${this.commandBusy}>
            ${this.commandBusy ? html`<lv-loading-spinner aria-hidden="true"></lv-loading-spinner>` : nothing}
            ${confirm ? 'Confirm update' : lifecycle.exists ? 'Save changes' : 'Configure'}
          </button>
          </div>
        </form>
      </lv-drawer>
    `
  }

  private save(event: SubmitEvent, lifecycle: ConnectionLifecycleSignal) {
    event.preventDefault()
    const form = event.currentTarget as HTMLFormElement
    if (!form.reportValidity()) return
    const data = new FormData(form)
    const command = this.commandFor(lifecycle, lifecycle.exists ? 'update' : 'create')
    for (const key of ['authenticationMode', 'connectorKind', 'credentialEnvironment', 'credentialProjectId', 'database', 'host', 'logicalConnection', 'objectScope', 'options', 'port', 'secretKey', 'secretPath', 'sourceIdentity', 'tlsMode'] as const) {
      ;(command as Record<string, unknown>)[key] = String(data.get(key) ?? '')
    }
    if (this.administration.command.logicalConnection === lifecycle.logicalConnection) {
      command.confirmationToken = this.administration.command.confirmationToken
    }
    this.dispatchEvent(new CustomEvent('lv-connection-administration-save', { bubbles: true, composed: true, detail: command }))
  }

  private commandFor(lifecycle: ConnectionLifecycleSignal, action: string) {
    return {
      action,
      assetId: lifecycle.assetId,
      authenticationMode: lifecycle.authenticationMode,
      confirmationToken: '',
      connectorKind: lifecycle.connectorKind,
      credentialEnvironment: lifecycle.credentialEnvironment,
      credentialProjectId: lifecycle.credentialProjectId,
      database: lifecycle.database,
      expectedRevision: lifecycle.revision,
      host: lifecycle.host,
      logicalConnection: lifecycle.logicalConnection,
      objectScope: lifecycle.objectScope,
      options: lifecycle.options,
      port: lifecycle.port,
      secretKey: lifecycle.secretKey,
      secretPath: lifecycle.secretPath,
      sourceIdentity: lifecycle.sourceIdentity,
      surface: this.surface,
      tlsMode: lifecycle.tlsMode,
    }
  }

  private selectedLifecycle(): ConnectionLifecycleSignal | undefined {
    return this.lifecycles.find((item) => item.logicalConnection === this.selectedLogical) || this.lifecycles[0]
  }

  private get commandBusy(): boolean {
    return this.administration.status.loading && !this.terminalFailure
  }

  private renderFailureStatus() {
    const message = this.terminalFailure?.message || this.administration.status.error
    if (!message) return nothing
    return html`
      <div class="form-status error" role="alert" aria-live="assertive">
        <div>${message}</div>
        <div class="form-status-actions">
          <span>Inputs and the current connection state were kept.</span>
          <button type="button" @click=${this.reloadAfterFailure}>Reload latest connection state</button>
        </div>
      </div>
    `
  }

  private renderTerminalFailure() {
    const failure = this.terminalFailure
    if (!failure) return nothing
    return html`
      <div class="terminal-failure" role="alert" aria-live="assertive">
        <div>${failure.message}</div>
        <div class="form-status-actions">
          <span>Connection state was kept.</span>
          <button type="button" @click=${this.reloadAfterFailure}>Reload latest connection state</button>
        </div>
      </div>
    `
  }

  private readonly handleDatastarFetch = (event: Event): void => {
    const failure = browserCommandFailure(event, 'Connection action')
    if (!failure) return
    this.terminalFailure = failure
  }

  private readonly reloadAfterFailure = (): void => {
    if (typeof window !== 'undefined') window.location.reload()
  }
}

function emptyAdministration(): ConnectionAdministrationSignal {
  return {
    command: {
      action: '', assetId: '', authenticationMode: '', confirmationToken: '', connectorKind: '', credentialEnvironment: '', credentialProjectId: '', database: '', expectedRevision: 0,
      host: '', logicalConnection: '', objectScope: '', options: '', port: '', secretKey: '', secretPath: '', sourceIdentity: '', surface: '', tlsMode: '',
    },
    status: { error: '', loading: false, message: '' },
  }
}

if (!customElements.get('lv-connection-administration')) customElements.define('lv-connection-administration', LeapViewConnectionAdministration)
