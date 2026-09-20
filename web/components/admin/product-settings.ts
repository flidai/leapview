import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure } from '../shared/command-failure'
import { settingsFieldStyles } from '../shared/settings-field-styles'
import type {
  ProductAPIStatusSignal,
  ProductAuthenticationSignal,
  ProductAvailabilitySignal,
  ProductGeneralSignal,
  ProductSettingsCommand,
  ProductSettingsSignal,
  ProductSystemSignal,
} from '../../generated/signals'

type ProductSection = 'general' | 'authentication' | 'system'
type StatusTone = 'positive' | 'neutral' | 'warning' | 'negative'

const emptyProductSettings: ProductSettingsSignal = {
  active: 'general', canManage: false,
  general: { displayName: '', revision: 0, updatedAt: '', instanceId: '', canonicalOrigin: '', environment: '' },
  authentication: {
    browserEnabled: false, apiTokenOnly: false,
    local: { available: false, enabled: false }, oidc: { available: false, enabled: false },
    azure: { available: false, enabled: false }, scim: { available: false, enabled: false }, managedBy: 'deployment',
    platformAdministrators: [], platformAdministrationRevision: '', platformAdministrationAvailable: false,
  },
  api: {
    bearerCredentials: { available: false, enabled: false }, servicePrincipals: { available: false, enabled: false },
    oauth: { available: false, enabled: false }, mcp: { available: false, enabled: false }, externalMcpIssuer: false,
  },
  system: {
    instanceId: '', canonicalOrigin: '', environment: '',
    build: { version: '', revision: '', buildTime: '', dirty: false, development: false },
    storageBackend: '', agent: { available: false, configured: false, modelConfigured: false },
    limits: { queryResultMaxRows: 0, queryResultMaxBytes: 0, managedDataMaxFiles: 0, managedDataMaxFileBytes: 0, managedDataMaxRevisionBytes: 0 },
    runtime: { health: '', controlPlane: '', environment: '' },
  },
}

/**
 * Platform settings rendered from the page-stream `productSettings` signal.
 * Text edits and destructive actions emit typed commands; only a binary logo
 * body uses the same-origin browser upload route.
 */
export class LeapViewProductSettings extends DatastarLit(LitElement) {
  @state() private selectedSection: ProductSection = 'general'
  @state() private displayNameDraft = ''
  @state() private busy = false
  @state() private commandBusy = false
  @state() private commandError = ''
  @state() private platformCommandBusy = false
  @state() private platformCommandError = ''
  @state() private platformPrincipalDraft = ''
  @state() private message = ''
  private lastRevision = -1
  private pendingRevision = -1
  private pendingError = ''
  private pendingPlatformRevision = ''
  private pendingPlatformError = ''

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    super.disconnectedCallback()
  }

  static styles = [settingsFieldStyles, css`
    :host { display: block; min-width: 0; color: var(--lv-fg-default); font: var(--lv-type-body); }
    .settings { display: grid; min-width: 0; gap: var(--base-size-24); max-width: var(--lv-page-content-max-width); }
    .panel { display: grid; min-width: 0; gap: var(--base-size-16); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: var(--base-size-20); }
    .panel h2, .panel h3, .panel p { margin: 0; }
    .panel h2 { font: var(--lv-type-section-title); }
    .panel h3.section-title { font: var(--lv-type-section-title); }
    .panel h3 { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .panel-heading { display: grid; min-width: 0; gap: var(--base-size-4); }
    .hint { color: var(--lv-fg-muted); font: var(--lv-type-caption); line-height: var(--base-text-lineHeight-snug); }
    .settings-rows { display: grid; min-width: 0; }
    .settings-row, .row { display: grid; grid-template-columns: minmax(11rem, .7fr) minmax(0, 1.3fr); gap: var(--base-size-16); align-items: center; border-top: var(--lv-border-muted); padding-top: var(--base-size-12); }
    .settings-row:first-child, .row:first-of-type { border-top: 0; padding-top: 0; }
    input[type="text"] { box-sizing: border-box; width: min(100%, 32rem); border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-bg-control); color: inherit; padding: .45rem .6rem; font: var(--lv-type-body-compact); }
    button.action { border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-button-bg-rest); color: var(--lv-button-fg-rest); cursor: pointer; padding: .42rem .7rem; font: var(--lv-type-body-compact); }
    button.action.primary { border-color: var(--lv-bg-accent); background: var(--lv-bg-accent); color: var(--lv-fg-on-accent); }
    button.action.danger { border-color: var(--lv-border-danger); color: var(--lv-fg-danger); }
    button.action:disabled { cursor: not-allowed; opacity: .55; }
    .inline { display: flex; flex-wrap: wrap; align-items: center; gap: .5rem; }
    .logo { display: flex; align-items: center; gap: .75rem; }
    .logo img { width: 3.25rem; height: 3.25rem; border: var(--lv-border-muted); border-radius: var(--lv-radius-small); object-fit: contain; background: var(--lv-bg-panel); }
    .identity-preview { display: flex; min-width: 0; align-items: center; gap: var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); padding: var(--base-size-12); }
    .identity-preview img, .identity-fallback { box-sizing: border-box; display: grid; width: var(--control-large-size); height: var(--control-large-size); flex: 0 0 auto; place-items: center; border: var(--lv-border-muted); border-radius: var(--lv-radius-small); background: var(--lv-bg-panel); object-fit: contain; color: var(--lv-fg-muted); font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .identity-copy { display: grid; min-width: 0; gap: var(--base-size-2); }
    .identity-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .attribution { color: var(--lv-fg-muted); text-decoration: none; font: var(--lv-type-caption); }
    .attribution:hover, .attribution:focus-visible { color: var(--lv-fg-default); text-decoration: underline; }
    .file-action { position: relative; display: inline-flex; align-items: center; border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-button-bg-rest); color: var(--lv-button-fg-rest); cursor: pointer; padding: .42rem .7rem; font: var(--lv-type-body-compact); }
    .file-action:focus-within { outline: var(--borderWidth-thick) solid var(--focus-outlineColor); outline-offset: -1px; }
    .file-action.disabled { cursor: not-allowed; opacity: .55; }
    .file-action input { position: absolute; width: 1px; height: 1px; opacity: 0; overflow: hidden; }
    .about-links { display: flex; flex-wrap: wrap; gap: var(--base-size-16); }
    .about-links a { color: var(--lv-fg-accent); }
    .status-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(12rem, 100%), 1fr)); gap: var(--base-size-8); }
    .status-card { display: grid; min-width: 0; gap: var(--base-size-4); border: var(--lv-border-muted); border-radius: var(--lv-radius-small); padding: var(--base-size-12); }
    .status-card strong { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .status { overflow-wrap: anywhere; font: var(--lv-type-caption); }
    .status-positive, .status.enabled { color: var(--lv-fg-success); }
    .status-neutral, .status.disabled { color: var(--lv-fg-muted); }
    .status-warning { color: var(--lv-fg-warning); }
    .status-negative { color: var(--lv-fg-danger); }
    .notice { border-radius: var(--lv-radius-small); background: var(--lv-bg-panel-muted); color: var(--lv-fg-muted); padding: var(--base-size-8) var(--base-size-12); font: var(--lv-type-caption); }
    .message { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    @media (max-width: 760px) {
      .settings { gap: var(--base-size-16); }
      .panel { padding: var(--base-size-16); }
      .settings-row, .row { grid-template-columns: 1fr; gap: var(--base-size-6); align-items: start; }
      .inline { align-items: stretch; }
      .inline input[type="text"] { flex: 1 1 12rem; min-width: 0; }
    }
    @media (max-width: 480px) {
      .panel { padding: var(--base-size-12); }
      .status-grid { grid-template-columns: 1fr; }
      .inline { align-items: stretch; flex-direction: column; }
      .inline input[type="text"], .inline .file-action, .inline button { width: 100%; }
      .logo { align-items: flex-start; }
    }
  `]

  private get settings(): ProductSettingsSignal {
    return this.signal<ProductSettingsSignal>('productSettings', emptyProductSettings)
  }

  override updated(): void {
    const settings = this.settings
    const revision = settings.general.revision
    if (revision !== this.lastRevision) {
      this.lastRevision = revision
      this.displayNameDraft = this.settings.general.displayName
    }
    if (this.commandBusy && (revision !== this.pendingRevision || (settings.error ?? '') !== this.pendingError)) {
      this.commandBusy = false
    }
    const platformRevision = settings.authentication.platformAdministrationRevision
    if (this.platformCommandBusy && (platformRevision !== this.pendingPlatformRevision || (settings.error ?? '') !== this.pendingPlatformError)) {
      this.platformCommandBusy = false
    }
    const active = settings.active as ProductSection
    if ((active === 'general' || active === 'authentication' || active === 'system') && active !== this.selectedSection) this.selectedSection = active
  }

  render() {
    const settings = this.settings
    return html`
      <div class="settings" aria-label="Product settings">
        ${settings.error ? html`<div class="notice" role="alert">${settings.error}</div>` : nothing}
        ${this.selectedSection === 'general' ? this.renderGeneral(settings.general, settings.canManage) : nothing}
        ${this.selectedSection === 'authentication' ? this.renderAuthentication(settings.authentication, settings.api) : nothing}
        ${this.selectedSection === 'system' ? this.renderSystem(settings.system) : nothing}
        ${this.commandError ? html`<div class="notice" role="alert">${this.commandError}</div>` : nothing}
        ${this.message ? html`<div class="message" role="status">${this.message}</div>` : nothing}
        ${this.platformCommandError ? html`<div class="notice" role="alert">${this.platformCommandError}</div>` : nothing}
        ${this.platformCommandBusy ? html`<div class="message" role="status" aria-live="polite">Updating platform authority…</div>` : nothing}
      </div>
    `
  }

  private renderGeneral(general: ProductGeneralSignal, canManage: boolean) {
    const previewName = this.displayNameDraft.trim() || 'LeapView'
    const disabled = this.busy || this.commandBusy
    // Keep reset available while a name mutation is pending so a user can
    // choose the explicit restore-default action; the in-flight command is
    // still serialized by the page command transport and all other controls
    // remain disabled until its signal patch or failure arrives.
    const resetDisabled = !canManage || this.busy || (general.displayName === 'LeapView' && !general.logo)
    return html`
      <section class="panel" aria-label="Instance identity settings">
        <div class="panel-heading">
          <h2>Instance identity</h2>
          <p class="hint">Choose the name and logo shown in the application. Customized instances retain a subtle link back to LeapView.</p>
        </div>
        <div class="identity-preview" aria-label="Instance identity preview">
          ${general.logo
            ? html`<img src=${general.logo.url} alt="">`
            : html`<span class="identity-fallback" aria-hidden="true">${previewName.slice(0, 1).toLocaleUpperCase()}</span>`}
          <span class="identity-copy">
            <span class="identity-name">${previewName}</span>
            <a class="attribution" href="https://leapview.dev" target="_blank" rel="noreferrer">Powered by LeapView</a>
          </span>
        </div>
        <div class="settings-rows">
          <div class="settings-row row">
            <div class="settings-field"><label class="settings-label" for="product-instance-name">Instance name</label><span class="settings-description">Used in navigation and browser titles · 120 characters maximum</span></div>
            <div class="inline">
              <input id="product-instance-name" aria-label="Instance name" type="text" maxlength="120" .value=${this.displayNameDraft} ?disabled=${!canManage || disabled} @input=${this.handleDisplayNameInput}>
              <button class="action primary" type="button" ?disabled=${!canManage || disabled || this.displayNameDraft.trim() === general.displayName} @click=${this.saveDisplayName}>Save</button>
            </div>
          </div>
          <div class="settings-row row">
            <div class="settings-field"><span class="settings-label">Instance logo</span><span class="settings-description">JPEG, PNG, or WebP · 5 MB maximum</span></div>
            <div class="inline">
              ${general.logo ? html`<div class="logo"><img src=${general.logo.url} alt="Product logo"><span class="hint">${general.logo.width} × ${general.logo.height}</span></div>` : html`<span class="settings-value">No logo configured</span>`}
              <label class=${`file-action ${!canManage || disabled ? 'disabled' : ''}`}>
                <span>${general.logo ? 'Change logo' : 'Upload logo'}</span>
                <input id="product-logo-upload" aria-label=${general.logo ? 'Change logo' : 'Upload logo'} type="file" accept="image/jpeg,image/png,image/webp" ?disabled=${!canManage || disabled} @change=${this.handleLogoFile}>
              </label>
              ${general.logo ? html`<button class="action danger" type="button" ?disabled=${!canManage || disabled} @click=${this.removeLogo}>Remove</button>` : nothing}
            </div>
          </div>
          <div class="settings-row row">
            <div class="settings-field"><span class="settings-label">LeapView defaults</span><span class="settings-description">Restore the default name and remove the custom logo</span></div>
            <button class="action" type="button" ?disabled=${resetDisabled} @click=${this.resetIdentity}>Reset to LeapView</button>
          </div>
        </div>
        ${!canManage ? html`<div class="notice">You have read-only access. Platform administrator access is required to change product identity.</div>` : nothing}
      </section>
      <section class="panel" aria-label="Instance details">
        <div class="panel-heading">
          <h2>Instance details</h2>
          <p class="hint">Read-only deployment metadata for this installation.</p>
        </div>
        <div class="settings-rows">
          ${this.settingsRow('Instance ID', general.instanceId || 'Unknown')}
          ${this.settingsRow('Canonical origin', general.canonicalOrigin || 'Unknown')}
          ${this.settingsRow('Environment', general.environment || 'Unknown')}
          ${this.settingsRow('Last updated', `${general.updatedAt || 'Unknown'} · revision ${general.revision || 'unknown'}`)}
        </div>
      </section>
    `
  }

  private renderAuthentication(auth: ProductAuthenticationSignal, api: ProductAPIStatusSignal) {
    return html`
      <section class="panel" aria-label="Sign-in and provisioning settings">
        <div class="panel-heading">
          <h2>Sign-in &amp; provisioning</h2>
          <p class="hint">Review deployment-managed sign-in and provisioning capabilities. Secrets, issuer URLs, tenant IDs, and callback URLs are never exposed here.</p>
        </div>
        <div class="notice">Managed by <strong>${auth.managedBy || 'deployment'}</strong>; configuration changes are made through deployment settings.</div>
        <div class="status-grid">
          ${this.statusCard('Browser sign-in', statusTone(auth.browserEnabled), auth.browserEnabled ? 'Enabled' : 'Disabled')}
          ${this.statusCard('API-token-only mode', statusTone(auth.apiTokenOnly), auth.apiTokenOnly ? 'Enabled' : 'Disabled')}
          ${this.statusCard('Local credentials', availabilityTone(auth.local), availabilityLabel(auth.local))}
          ${this.statusCard('OIDC', availabilityTone(auth.oidc), `${availabilityLabel(auth.oidc)}${auth.oidc.provider ? ` · ${auth.oidc.provider}` : ''}`)}
          ${this.statusCard('Azure', availabilityTone(auth.azure), availabilityLabel(auth.azure))}
          ${this.statusCard('SCIM provisioning', availabilityTone(auth.scim), availabilityLabel(auth.scim))}
        </div>
      </section>
      <section class="panel" aria-label="API and protocols settings">
        <div class="panel-heading">
          <h2>API &amp; protocols</h2>
          <p class="hint">Review deployment-managed API credentials, service identities, and protocol endpoints.</p>
        </div>
        <div class="status-grid">
          ${this.statusCard('Bearer credentials', availabilityTone(api.bearerCredentials), availabilityLabel(api.bearerCredentials))}
          ${this.statusCard('Service principals', availabilityTone(api.servicePrincipals), availabilityLabel(api.servicePrincipals))}
          ${this.statusCard('OAuth', availabilityTone(api.oauth), availabilityLabel(api.oauth))}
          ${this.statusCard('MCP', availabilityTone(api.mcp), availabilityLabel(api.mcp))}
          ${this.statusCard('External MCP issuer', statusTone(api.externalMcpIssuer), api.externalMcpIssuer ? 'Configured' : 'Not configured')}
        </div>
      </section>
      <section class="panel" aria-label="Platform authority settings">
        <div class="panel-heading">
          <h3 class="section-title">Platform authority</h3>
          <p class="hint">Grant and revoke platform administrator bindings. Changes require a recent interactive browser sign-in; service credentials cannot perform them.</p>
        </div>
        ${!auth.platformAdministrationAvailable
          ? html`<div class="notice" role="alert">${auth.platformAdministrationError || 'Platform authority history is unavailable.'}</div>`
          : html`
            <div class="inline">
              <input id="platform-administrator-principal" aria-label="Principal ID" type="text" placeholder="Principal ID" .value=${this.platformPrincipalDraft} @input=${this.handlePlatformPrincipalInput} ?disabled=${!this.settings.canManage || this.platformCommandBusy}>
              <button class="action primary" type="button" ?disabled=${!this.settings.canManage || this.platformCommandBusy || !this.platformPrincipalDraft.trim()} @click=${this.grantPlatformAdministrator}>${this.platformCommandBusy ? 'Granting…' : 'Grant platform administrator'}</button>
            </div>
            <div class="status-grid" aria-label="Platform authority bindings">
              ${auth.platformAdministrators.length === 0
                ? html`<div class="notice">No platform authority bindings have been recorded.</div>`
                : auth.platformAdministrators.map((administrator) => html`
                  <div class="status-card">
                    <strong>${administrator.displayName || administrator.email || administrator.principalId}</strong>
                    <span class="settings-value">${administrator.email || administrator.principalId} · ${administrator.role}</span>
                    <span class=${`status ${administrator.revokedAt ? 'disabled' : 'enabled'}`}>${administrator.revokedAt ? `Revoked ${administrator.revokedAt}` : `Granted ${administrator.grantedAt}`}</span>
                    ${administrator.revokedAt ? nothing : html`<button class="action danger" type="button" ?disabled=${this.platformCommandBusy} @click=${() => this.revokePlatformAdministrator(administrator.principalId)}>${this.platformCommandBusy ? 'Revoking…' : 'Revoke'}</button>`}
                  </div>
                `)}
            </div>`}
      </section>
    `
  }

  private renderSystem(system: ProductSystemSignal) {
    const build = system.build
    const limits = system.limits
    const agent = system.agent
    return html`
      <section class="panel" aria-label="Runtime health settings">
        <div class="panel-heading">
          <h2>Runtime health</h2>
          <p class="hint">Runtime health and safe operational metadata for this instance.</p>
        </div>
        <div class="status-grid">
          ${this.statusCard('Runtime health', runtimeTone(system.runtime.health), system.runtime.health || 'Unknown')}
          ${this.statusCard('Control plane', runtimeTone(system.runtime.controlPlane, 'available'), system.runtime.controlPlane || 'Unknown')}
          ${this.statusCard('Agent', agentTone(agent), agent.configured ? `${agent.provider || 'Configured'}${agent.modelConfigured ? ' · model ready' : ' · model missing'}` : 'Not configured')}
          ${this.statusCard('Storage backend', system.storageBackend ? 'positive' : 'neutral', system.storageBackend || 'Unknown')}
        </div>
        <div class="settings-rows">
          ${this.settingsRow('Instance ID', system.instanceId || 'Unknown')}
          ${this.settingsRow('Canonical origin', system.canonicalOrigin || 'Unknown')}
          ${this.settingsRow('Environment', system.environment || 'Unknown')}
        </div>
      </section>
      <section class="panel" aria-label="Build settings">
        <div class="panel-heading">
          <h2>Build</h2>
          <p class="hint">Version and release metadata reported by the running instance.</p>
        </div>
        <div class="status-grid">
          ${this.statusCard('Version', build.version ? 'neutral' : 'warning', build.version || 'Unknown')}
          ${this.statusCard('Revision', build.revision ? 'neutral' : 'warning', build.revision || 'Unknown')}
          ${this.statusCard('Build time', build.buildTime ? 'neutral' : 'warning', build.buildTime || 'Unknown')}
          ${this.statusCard('Build state', build.dirty ? 'negative' : build.development ? 'warning' : 'positive', build.dirty ? 'Dirty' : build.development ? 'Development' : 'Release')}
        </div>
      </section>
      <section class="panel" aria-label="Limits settings">
        <div class="panel-heading">
          <h2>Limits</h2>
          <p class="hint">Configured query and managed-data budgets for this instance.</p>
        </div>
        <div class="status-grid">
          ${this.limitCard('Query result rows', limits.queryResultMaxRows)}
          ${this.limitCard('Query result bytes', limits.queryResultMaxBytes)}
          ${this.limitCard('Managed-data files', limits.managedDataMaxFiles)}
          ${this.limitCard('Managed-data file bytes', limits.managedDataMaxFileBytes)}
          ${this.limitCard('Managed-data revision bytes', limits.managedDataMaxRevisionBytes)}
        </div>
      </section>
      <section class="panel" aria-label="About LeapView">
        <div class="panel-heading">
          <h2>About LeapView</h2>
          <p>Powered by LeapView, open-source dashboards-as-code business intelligence.</p>
        </div>
        <div class="about-links">
          <a href="https://leapview.dev" target="_blank" rel="noreferrer">LeapView website</a>
          <a href="https://github.com/flidai/leapview" target="_blank" rel="noreferrer">View source</a>
        </div>
        <p class="hint">Build ${build.version || 'development'}${build.revision ? ` · ${build.revision}` : ''}</p>
      </section>
    `
  }

  private settingsRow(label: string, value: string) {
    return html`<div class="settings-row"><div class="settings-field"><span class="settings-label">${label}</span></div><span class="settings-value">${value}</span></div>`
  }

  private statusCard(label: string, tone: StatusTone, value: string) {
    return html`<div class="status-card"><strong>${label}</strong><span class=${`status status-${tone} ${tone === 'positive' ? 'enabled' : tone === 'neutral' ? 'disabled' : ''}`} data-status=${tone} role="status">${value}</span></div>`
  }

  private limitCard(label: string, value: number) {
    return html`<div class="status-card"><strong>${label}</strong><span class="settings-value">${formatLimit(value)}</span></div>`
  }

  private handleDisplayNameInput = (event: Event): void => {
    this.displayNameDraft = (event.currentTarget as HTMLInputElement).value
  }

  private saveDisplayName = (): void => {
    this.emitCommand({ action: 'save_display_name', displayName: this.displayNameDraft, revision: this.settings.general.revision })
  }

  private removeLogo = (): void => {
    this.emitCommand({ action: 'remove_logo', revision: this.settings.general.revision })
  }

  private resetIdentity = (): void => {
    this.emitCommand({ action: 'reset_identity', revision: this.settings.general.revision })
  }

  private handlePlatformPrincipalInput = (event: Event): void => {
    this.platformPrincipalDraft = (event.currentTarget as HTMLInputElement).value
  }

  private grantPlatformAdministrator = (): void => {
    this.emitPlatformAdministratorCommand('grant_platform_administrator', this.platformPrincipalDraft.trim())
  }

  private revokePlatformAdministrator = (principalId: string): void => {
    this.emitPlatformAdministratorCommand('revoke_platform_administrator', principalId)
  }

  private emitPlatformAdministratorCommand(action: 'grant_platform_administrator' | 'revoke_platform_administrator', principalId: string): void {
    this.commandError = ''
    this.platformCommandError = ''
    this.message = ''
    this.platformCommandBusy = true
    this.pendingPlatformRevision = this.settings.authentication.platformAdministrationRevision
    this.pendingPlatformError = this.settings.error ?? ''
    this.dispatchEvent(new CustomEvent<ProductSettingsCommand>('lv-platform-administrator-command', {
      bubbles: true, composed: true,
      detail: { action, principalId, expectedRevision: this.settings.authentication.platformAdministrationRevision, revision: 0 },
    }))
  }

  private emitCommand(command: ProductSettingsCommand): void {
    this.commandError = ''
    if (command.action !== 'refresh') {
      this.message = ''
      this.commandBusy = true
      this.pendingRevision = this.settings.general.revision
      this.pendingError = this.settings.error ?? ''
    }
    this.dispatchEvent(new CustomEvent<ProductSettingsCommand>('lv-product-settings-command', { bubbles: true, composed: true, detail: command }))
  }

  private handleDatastarFetch = (event: Event): void => {
    if (!this.commandBusy && !this.platformCommandBusy) return
    const failure = browserCommandFailure(event, this.platformCommandBusy ? 'Platform authority update' : 'Product settings update')
    if (!failure) return
    if (this.platformCommandBusy) {
      this.platformCommandBusy = false
      this.platformCommandError = failure.message
    } else {
      this.commandBusy = false
      this.commandError = failure.message
    }
  }

  private handleLogoFile = async (event: Event): Promise<void> => {
    const input = event.currentTarget as HTMLInputElement
    const file = input.files?.[0]
    if (!file) return
    const settings = this.settings
    if (!settings.canManage) return
    this.busy = true
    this.message = ''
    try {
      const response = await fetch('/admin/product-logo', {
        method: 'PUT',
        credentials: 'same-origin',
        headers: {
          ...window.LeapViewCommand.headers('uploadProductLogo', productETag(settings.general.revision)),
          'Content-Type': file.type,
        },
        body: file,
      })
      if (!response.ok) throw new Error(`Logo upload failed (${response.status})`)
      this.message = 'Logo uploaded.'
      this.dispatchEvent(new CustomEvent('lv-product-settings-logo-updated', { bubbles: true, composed: true, detail: { etag: response.headers.get('ETag') ?? '' } }))
      this.emitCommand({ action: 'refresh', revision: 0 })
    } catch (error) {
      this.message = error instanceof Error ? error.message : 'Logo upload failed.'
    } finally {
      this.busy = false
      input.value = ''
    }
  }
}

function availabilityLabel(status: ProductAvailabilitySignal): string {
  if (!status.available) return 'Unavailable'
  return status.enabled ? 'Enabled' : 'Disabled'
}

function statusTone(enabled: boolean): StatusTone {
  return enabled ? 'positive' : 'neutral'
}

function availabilityTone(status: ProductAvailabilitySignal): StatusTone {
  if (!status.available) return 'neutral'
  return status.enabled ? 'positive' : 'neutral'
}

function runtimeTone(value: string, healthyValue = 'healthy'): StatusTone {
  if (!value) return 'neutral'
  return value === healthyValue || value === 'available' ? 'positive' : 'negative'
}

function agentTone(agent: ProductSystemSignal['agent']): StatusTone {
  if (!agent.configured) return 'neutral'
  return agent.modelConfigured ? 'positive' : 'warning'
}

function formatLimit(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return 'Not configured'
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)} GB`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)} MB`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)} KB`
  return value.toLocaleString()
}

function productETag(revision: number): string {
  return `"product-${revision}"`
}

if (!customElements.get('lv-product-settings')) customElements.define('lv-product-settings', LeapViewProductSettings)
