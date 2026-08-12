import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { DatastarLit } from '../shared/datastar-lit'
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

const emptyProductSettings: ProductSettingsSignal = {
  active: 'general', canManage: false,
  general: { displayName: '', revision: 0, updatedAt: '', instanceId: '', canonicalOrigin: '', environment: '' },
  authentication: {
    browserEnabled: false, apiTokenOnly: false,
    local: { available: false, enabled: false }, oidc: { available: false, enabled: false },
    azure: { available: false, enabled: false }, scim: { available: false, enabled: false }, managedBy: 'deployment',
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
  @state() private message = ''
  private lastRevision = -1

  static styles = [settingsFieldStyles, css`
    :host { display: block; min-width: 0; color: var(--lv-fg-default); font: var(--lv-type-body); }
    .settings { display: grid; gap: var(--base-size-24); max-width: 72rem; }
    .panel { display: grid; gap: 1rem; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: 1rem; }
    .panel h2, .panel h3, .panel p { margin: 0; }
    .panel h2 { font: var(--lv-type-section-title); }
    .panel h3 { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .hint { color: var(--lv-fg-muted); font: var(--lv-type-caption); line-height: var(--base-text-lineHeight-snug); }
    .row { display: grid; grid-template-columns: minmax(10rem, 15rem) minmax(0, 1fr); gap: .75rem; align-items: center; border-top: var(--lv-border-muted); padding-top: .75rem; }
    .row:first-of-type { border-top: 0; padding-top: 0; }
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
    .status-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr)); gap: .65rem; }
    .status-card { display: grid; gap: .3rem; border: var(--lv-border-muted); border-radius: var(--lv-radius-small); padding: .7rem; }
    .status-card strong { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .status { font: var(--lv-type-caption); }
    .status.enabled { color: var(--lv-fg-success); }
    .status.disabled { color: var(--lv-fg-muted); }
    .notice { border-radius: var(--lv-radius-small); background: var(--lv-bg-panel-muted); color: var(--lv-fg-muted); padding: .6rem .7rem; font: var(--lv-type-caption); }
    .message { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    @media (max-width: 620px) { .row { grid-template-columns: 1fr; gap: .35rem; } }
  `]

  private get settings(): ProductSettingsSignal {
    return this.signal<ProductSettingsSignal>('productSettings', emptyProductSettings)
  }

  override updated(): void {
    const revision = this.settings.general.revision
    if (revision !== this.lastRevision) {
      this.lastRevision = revision
      this.displayNameDraft = this.settings.general.displayName
    }
    const active = this.settings.active as ProductSection
    if ((active === 'general' || active === 'authentication' || active === 'system') && active !== this.selectedSection) this.selectedSection = active
  }

  render() {
    const settings = this.settings
    return html`
      <div class="settings" aria-label="Product settings">
        ${settings.error ? html`<div class="notice" role="alert">${settings.error}</div>` : nothing}
        ${this.selectedSection === 'general' ? this.renderGeneral(settings.general, settings.canManage) : nothing}
        ${this.selectedSection === 'authentication' ? this.renderAuthentication(settings.authentication, settings.api) : nothing}
        ${this.selectedSection === 'system' ? this.renderSystem(settings.system, settings.api) : nothing}
        ${this.message ? html`<div class="message" role="status">${this.message}</div>` : nothing}
      </div>
    `
  }

  private renderGeneral(general: ProductGeneralSignal, canManage: boolean) {
    const previewName = this.displayNameDraft.trim() || 'LeapView'
    const resetDisabled = !canManage || this.busy || (general.displayName === 'LeapView' && !general.logo)
    return html`
      <section class="panel" aria-label="Instance identity settings">
        <h2>Instance identity</h2>
        <p class="hint">Choose the name and logo shown in the application. Customized instances retain a subtle link back to LeapView.</p>
        <div class="identity-preview" aria-label="Instance identity preview">
          ${general.logo
            ? html`<img src=${general.logo.url} alt="">`
            : html`<span class="identity-fallback" aria-hidden="true">${previewName.slice(0, 1).toLocaleUpperCase()}</span>`}
          <span class="identity-copy">
            <span class="identity-name">${previewName}</span>
            <a class="attribution" href="https://leapview.dev" target="_blank" rel="noreferrer">Powered by LeapView</a>
          </span>
        </div>
        <div class="row">
          <div class="settings-field"><span class="settings-label">Instance name</span><span class="settings-description">Used in navigation and browser titles · 120 characters maximum</span></div>
          <div class="inline">
            <input type="text" maxlength="120" .value=${this.displayNameDraft} ?disabled=${!canManage || this.busy} @input=${this.handleDisplayNameInput}>
            <button class="action primary" type="button" ?disabled=${!canManage || this.busy || this.displayNameDraft.trim() === general.displayName} @click=${this.saveDisplayName}>Save</button>
          </div>
        </div>
        <div class="row">
          <div class="settings-field"><span class="settings-label">Instance logo</span><span class="settings-description">JPEG, PNG, or WebP · 5 MB maximum</span></div>
          <div class="inline">
            ${general.logo ? html`<div class="logo"><img src=${general.logo.url} alt="Product logo"><span class="hint">${general.logo.width} × ${general.logo.height}</span></div>` : html`<span class="settings-value">No logo configured</span>`}
            <label class=${`file-action ${!canManage || this.busy ? 'disabled' : ''}`}>
              <span>${general.logo ? 'Change logo' : 'Upload logo'}</span>
              <input type="file" accept="image/jpeg,image/png,image/webp" ?disabled=${!canManage || this.busy} @change=${this.handleLogoFile}>
            </label>
            ${general.logo ? html`<button class="action danger" type="button" ?disabled=${!canManage || this.busy} @click=${this.removeLogo}>Remove</button>` : nothing}
          </div>
        </div>
        <div class="row">
          <div class="settings-field"><span class="settings-label">LeapView defaults</span><span class="settings-description">Restore the default name and remove the custom logo</span></div>
          <button class="action" type="button" ?disabled=${resetDisabled} @click=${this.resetIdentity}>Reset to LeapView</button>
        </div>
        ${!canManage ? html`<div class="notice">You have read-only access. MANAGE_PLATFORM is required to change product identity.</div>` : nothing}
      </section>
      <section class="panel" aria-label="Instance details">
        <h2>Instance details</h2>
        <p class="hint">Read-only deployment metadata for this installation.</p>
        <div class="row"><div class="settings-label">Instance ID</div><span class="settings-value">${general.instanceId || 'Unknown'}</span></div>
        <div class="row"><div class="settings-label">Canonical origin</div><span class="settings-value">${general.canonicalOrigin || 'Unknown'}</span></div>
        <div class="row"><div class="settings-label">Environment</div><span class="settings-value">${general.environment || 'Unknown'}</span></div>
        <div class="row"><div class="settings-label">Last updated</div><span class="settings-value">${general.updatedAt || 'Unknown'} · revision ${general.revision || 'unknown'}</span></div>
      </section>
    `
  }

  private renderAuthentication(auth: ProductAuthenticationSignal, api: ProductAPIStatusSignal) {
    return html`
      <section class="panel" aria-label="Authentication settings">
        <h2>Authentication</h2>
        <p class="hint">Deployment-managed authentication configuration. Secrets, issuer URLs, tenant IDs, and callback URLs are never exposed here.</p>
        <div class="notice">Managed by <strong>${auth.managedBy || 'deployment'}</strong>; configuration changes are made through deployment settings.</div>
        <div class="status-grid">
          ${this.statusCard('Browser sign-in', auth.browserEnabled, auth.browserEnabled ? 'Enabled' : 'Disabled')}
          ${this.statusCard('API-token-only mode', auth.apiTokenOnly, auth.apiTokenOnly ? 'Enabled' : 'Disabled')}
          ${this.statusCard('Local credentials', auth.local.enabled, availabilityLabel(auth.local))}
          ${this.statusCard('OIDC', auth.oidc.enabled, `${availabilityLabel(auth.oidc)}${auth.oidc.provider ? ` · ${auth.oidc.provider}` : ''}`)}
          ${this.statusCard('Azure', auth.azure.enabled, availabilityLabel(auth.azure))}
          ${this.statusCard('SCIM provisioning', auth.scim.enabled, availabilityLabel(auth.scim))}
        </div>
        <h3>API and protocol availability</h3>
        <div class="status-grid">
          ${this.statusCard('Bearer credentials', api.bearerCredentials.enabled, availabilityLabel(api.bearerCredentials))}
          ${this.statusCard('Service principals', api.servicePrincipals.enabled, availabilityLabel(api.servicePrincipals))}
          ${this.statusCard('OAuth', api.oauth.enabled, availabilityLabel(api.oauth))}
          ${this.statusCard('MCP', api.mcp.enabled, availabilityLabel(api.mcp))}
          ${this.statusCard('External MCP issuer', api.externalMcpIssuer, api.externalMcpIssuer ? 'Configured' : 'Not configured')}
        </div>
      </section>
    `
  }

  private renderSystem(system: ProductSystemSignal, api: ProductAPIStatusSignal) {
    const build = system.build
    const limits = system.limits
    const agent = system.agent
    return html`
      <section class="panel" aria-label="System settings">
        <h2>System</h2>
        <p class="hint">Runtime health and safe operational metadata for this instance.</p>
        <div class="status-grid">
          ${this.statusCard('Runtime health', system.runtime.health === 'healthy', system.runtime.health || 'Unknown')}
          ${this.statusCard('Control plane', system.runtime.controlPlane === 'available', system.runtime.controlPlane || 'Unknown')}
          ${this.statusCard('Agent', agent.configured, agent.configured ? `${agent.provider || 'Configured'}${agent.modelConfigured ? ' · model ready' : ' · model missing'}` : 'Not configured')}
          ${this.statusCard('Storage backend', Boolean(system.storageBackend), system.storageBackend || 'Unknown')}
        </div>
        <div class="row"><div class="settings-label">Instance ID</div><span class="settings-value">${system.instanceId || 'Unknown'}</span></div>
        <div class="row"><div class="settings-label">Canonical origin</div><span class="settings-value">${system.canonicalOrigin || 'Unknown'}</span></div>
        <div class="row"><div class="settings-label">Environment</div><span class="settings-value">${system.environment || 'Unknown'}</span></div>
        <h3>Build</h3>
        <div class="status-grid">
          ${this.statusCard('Version', Boolean(build.version), build.version || 'Unknown')}
          ${this.statusCard('Revision', Boolean(build.revision), build.revision || 'Unknown')}
          ${this.statusCard('Build time', Boolean(build.buildTime), build.buildTime || 'Unknown')}
          ${this.statusCard('Build state', !build.dirty, build.dirty ? 'Dirty' : build.development ? 'Development' : 'Release')}
        </div>
        <h3>Limits</h3>
        <div class="status-grid">
          ${this.limitCard('Query result rows', limits.queryResultMaxRows)}
          ${this.limitCard('Query result bytes', limits.queryResultMaxBytes)}
          ${this.limitCard('Managed-data files', limits.managedDataMaxFiles)}
          ${this.limitCard('Managed-data file bytes', limits.managedDataMaxFileBytes)}
          ${this.limitCard('Managed-data revision bytes', limits.managedDataMaxRevisionBytes)}
        </div>
        <h3>API and protocol</h3>
        <p class="hint">${api.externalMcpIssuer ? 'External MCP issuer is available.' : 'External MCP issuer is not configured.'} API capabilities remain deployment-managed.</p>
      </section>
      <section class="panel" aria-label="About LeapView">
        <h2>About LeapView</h2>
        <p>Powered by LeapView, open-source dashboards-as-code business intelligence.</p>
        <div class="about-links">
          <a href="https://leapview.dev" target="_blank" rel="noreferrer">LeapView website</a>
          <a href="https://github.com/flidai/leapview" target="_blank" rel="noreferrer">View source</a>
        </div>
        <p class="hint">Build ${build.version || 'development'}${build.revision ? ` · ${build.revision}` : ''}</p>
      </section>
    `
  }

  private statusCard(label: string, enabled: boolean, value: string) {
    return html`<div class="status-card"><strong>${label}</strong><span class=${`status ${enabled ? 'enabled' : 'disabled'}`}>${value}</span></div>`
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

  private emitCommand(command: ProductSettingsCommand): void {
    this.dispatchEvent(new CustomEvent<ProductSettingsCommand>('lv-product-settings-command', { bubbles: true, composed: true, detail: command }))
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
          'Content-Type': file.type,
          'If-Match': productETag(settings.general.revision),
          'X-CSRF-Token': csrfToken(),
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

function csrfToken(): string {
  return document.querySelector('meta[name="csrf-token"]')?.getAttribute('content') ?? ''
}

if (!customElements.get('lv-product-settings')) customElements.define('lv-product-settings', LeapViewProductSettings)
