import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import { exampleDetails, exampleChromeStyles } from './example-chrome'
import { CircleAlert, FolderOpen, Search, Users } from 'lucide'
import type { DashboardAppearanceSignal } from '../web/generated/signals'
import { lucideIcon } from '../web/components/shared/lucide-icons'
import { fieldTypeIcon } from '../web/components/shared/field-type-icon'
import { LeapViewToastRegion } from '../web/components/shared/toast'
import { settingsLayoutStyles, renderSettingsActions, renderSettingsRow, renderSettingsSection } from '../web/components/shared/settings-layout'
import { settingsFieldStyles } from '../web/components/shared/settings-field-styles'
import { emptyStateStyles, renderEmptyState } from '../web/components/shared/empty-state'
import { pageHeaderStyles, renderPageHeader } from '../web/components/shared/page-header'
import { breadcrumbStyles, renderAssetBreadcrumbGlyph, renderBreadcrumb } from '../web/components/shared/breadcrumb'
import { entityDetailStyles, renderEntityDetail } from '../web/components/shared/entity-detail'
import { clampFittedScale, clampScale, resolvedLayoutMode, type ZoomCommand, type ZoomState } from '../web/components/dashboard/report-view-state'
import '../web/components/shared/drawer'
import '../web/components/shared/user-avatar'
import '../web/components/shared/brand-mark'
import '../web/components/shared/one-time-secret'
import '../web/components/app/dashboard-icon-picker'
import '../web/components/project/dashboard-appearance-editor'
import '../web/components/dashboard/report-footer'
import { demoSecret, surfaceDocs, surfaceExamples } from './surface-fixtures'

export { surfaceExamples }

export class PlaygroundSurfaces extends LitElement {
  @property() example = 'drawer'
  @state() private variant = 'standard'
  @state() private longText = false
  @state() private disabled = false
  @state() private drawerOpen = false
  @state() private drawerModal = true
  @state() private drawerWide = false
  @state() private name = 'Analytics workspace'
  @state() private savedName = 'Analytics workspace'
  @state() private actionDone = false
  @state() private appearance: DashboardAppearanceSignal = { icon: 'layout-dashboard', color: 'purple', revision: 1 }
  @state() private zoom: ZoomState = { layoutMode: 'desktop', layout: 'desktop', mode: 'actual-size', scale: 1 }
  @state() private logs: string[] = []
  private drawerTrigger?: HTMLElement
  private zoomResizeObserver?: ResizeObserver
  @state() private lastToast = 0

  static styles = [settingsLayoutStyles, settingsFieldStyles, emptyStateStyles, pageHeaderStyles, breadcrumbStyles, entityDetailStyles, css`
    :host { display: block; min-width: 0; color: var(--lv-fg-default); font: var(--lv-type-body); }
    * { box-sizing: border-box; }
    .stack { display: grid; gap: var(--base-size-20); min-width: 0; }
    .controls, .row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--base-size-12); }
    .controls { padding: var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); }
    .controls label { display: inline-flex; align-items: center; gap: var(--base-size-8); font: var(--lv-type-caption); }
    .preview { min-width: 0; min-height: 14rem; padding: var(--base-size-24); border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); }
    .column { display: grid; align-content: start; min-width: 0; gap: var(--base-size-16); }
    .identity-samples { display: grid; grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr)); gap: var(--base-size-24); }
    .identity-sample { display: grid; align-content: start; justify-items: start; gap: var(--base-size-12); }
    .brand { display: flex; align-items: center; gap: var(--base-size-12); font: var(--lv-type-section-title); }
    .brand.accent { color: var(--lv-fg-accent); }
    .caption, .documentation p { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .drawer-content { display: grid; gap: var(--base-size-20); padding: var(--base-size-8); }
    .drawer-content h2 { margin: 0; font: var(--lv-type-section-title); }
    .drawer-content p { margin: 0; }
    .detail-section { padding: var(--base-size-24) 0; }
    .detail-section h2 { margin: 0 0 var(--base-size-16); font: var(--lv-type-section-title); }
    .documentation { display: grid; gap: var(--base-size-12); }
    .documentation p { margin: 0; }
    code, pre { font-family: var(--fontStack-monospace); font-size: .8rem; overflow-wrap: anywhere; }
    pre { white-space: pre-wrap; max-height: 15rem; overflow: auto; margin: 0; padding: var(--base-size-12); border-radius: var(--lv-radius-small); background: var(--lv-bg-panel-muted); }
    .log-heading { display: flex; justify-content: space-between; align-items: center; gap: var(--base-size-8); }
    .log-heading h3 { margin: 0; font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); }
    .report-window { overflow: auto; height: 270px; border: var(--lv-border-muted); background: var(--lv-bg-panel-muted); padding: var(--base-size-12); }
    .report-page { width: 600px; height: 220px; padding: var(--base-size-20); transform-origin: top left; background: var(--lv-bg-panel); border: var(--lv-border-default); }
    .report-page.mobile { width: 300px; }
    .report-page h2 { margin: 0; font: var(--lv-type-section-title); }
    .report-page p { color: var(--lv-fg-muted); }
    :host([preview-only]) .controls, :host([preview-only]) .documentation { display: none; }
    :host([preview-only]) .preview { border: 0; border-radius: 0; }
    @media (max-width: 500px) { .preview { padding: var(--base-size-12); } }
  `, exampleChromeStyles]

  protected willUpdate(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('example')) {
      this.zoomResizeObserver?.disconnect(); this.zoomResizeObserver = undefined
      this.variant = 'standard'; this.longText = false; this.disabled = false; this.actionDone = false
      this.drawerOpen = false; this.drawerModal = true; this.drawerWide = false; this.logs = []
      this.lastToast = 0
      this.name = this.savedName = 'Analytics workspace'
      this.appearance = { icon: 'layout-dashboard', color: 'purple', revision: 1 }
      this.zoom = { layoutMode: 'desktop', layout: 'desktop', mode: 'actual-size', scale: 1 }
    }
  }

  protected updated(changed: Map<PropertyKey, unknown>): void {
    if (this.example === 'report-footer' && (changed.has('example') || changed.has('zoom'))) {
      void this.publishZoomState()
    }
  }

  disconnectedCallback(): void {
    this.zoomResizeObserver?.disconnect()
    this.zoomResizeObserver = undefined
    super.disconnectedCallback()
  }

  private async publishZoomState(): Promise<void> {
    // The footer creates its zoom child in its own render, after this host's render.
    const footer = this.renderRoot.querySelector<LitElement>('lv-report-footer')
    if (footer) await footer.updateComplete
    if (!this.isConnected || this.example !== 'report-footer') return
    document.dispatchEvent(new CustomEvent<ZoomState>('lv-report-zoom-state', { detail: this.zoom }))
    const viewport = this.renderRoot.querySelector<HTMLElement>('.report-window')
    if (viewport && !this.zoomResizeObserver && typeof ResizeObserver === 'function') {
      this.zoomResizeObserver = new ResizeObserver(() => {
        if (this.zoom.layoutMode === 'auto' || this.zoom.mode === 'fit-width' || this.zoom.mode === 'fit-page') this.applyZoom({})
      })
      this.zoomResizeObserver.observe(viewport)
    }
  }

  private record(name: string, detail: unknown = {}): void {
    this.logs = [`${name}\n${JSON.stringify(detail, null, 2)}`, ...this.logs].slice(0, 12)
    this.dispatchEvent(new CustomEvent('playground-event', { bubbles: true, composed: true, detail: { name, detail } }))
  }

  private variants(): Array<[string, string]> {
    switch (this.example) {
      case 'empty-state': return [['standard', 'Empty'], ['search', 'No search results'], ['error', 'Error']]
      case 'settings': return [['standard', 'Panel'], ['card', 'Card'], ['plain', 'Plain']]
      case 'report-footer': return [['standard', 'Refreshed'], ['loading', 'Loading'], ['error', 'Error'], ['empty', 'Not refreshed']]
      case 'toast-region': return [['standard', 'Info · timed'], ['success', 'Success · timed'], ['error', 'Error · timed'], ['undo', 'Undo · persistent']]
      case 'entity-detail': return [['standard', 'User'], ['team', 'Team'], ['notice', 'With notice']]
      default: return []
    }
  }

  render() {
    const docs = surfaceDocs[this.example]
    if (!docs) return html`<p>Choose a surface example.</p>`
    const variants = this.variants()
    return html`<div class="stack">
      <div class="controls" aria-label="Surface fixture controls">
        ${variants.length ? html`<label>Variant<select class="settings-input" aria-label="Variant" .value=${this.variant} @change=${(event: Event) => { this.variant = (event.target as HTMLSelectElement).value; this.actionDone = false }}>${variants.map(([value, label]) => html`<option value=${value}>${label}</option>`)}</select></label>` : nothing}
        <label><input type="checkbox" .checked=${this.longText} @change=${(event: Event) => { this.longText = (event.target as HTMLInputElement).checked }}>Long content</label>
        ${['drawer', 'settings', 'page-header', 'entity-detail'].includes(this.example) ? html`<label><input type="checkbox" .checked=${this.disabled} @change=${(event: Event) => { this.disabled = (event.target as HTMLInputElement).checked }}>Disable fixture actions</label>` : nothing}
        ${this.example === 'drawer' ? html`<label><input type="checkbox" .checked=${this.drawerModal} @change=${(event: Event) => { this.drawerModal = (event.target as HTMLInputElement).checked }}>Modal</label><label><input type="checkbox" .checked=${this.drawerWide} @change=${(event: Event) => { this.drawerWide = (event.target as HTMLInputElement).checked }}>Wide</label>` : nothing}
      </div>
      <div class="preview" part="preview">${keyed(this.example, this.renderExample())}</div>
      ${exampleDetails(html`<div class="documentation">
        <p>${docs.note}</p><p><strong>Component:</strong> <code>${docs.component}</code></p>
        <p><strong>Source:</strong> <code>${docs.source}</code></p><p><strong>Inputs:</strong> ${docs.inputs}</p><p><strong>Events:</strong> <code>${docs.events}</code></p>
        <div class="log-heading"><h3>Public event log</h3><button class="settings-button" @click=${() => { this.logs = [] }}>Clear log</button></div>
        <pre aria-label="Public event log">${this.logs.join('\n\n')}</pre>
      </div>`)}
    </div>`
  }

  private renderExample() {
    const title = this.longText ? 'Quarterly operational analytics for enterprise accounts across every region' : 'Operational analytics'
    switch (this.example) {
      case 'drawer': return html`<div class="column"><button class="settings-button primary" @click=${this.openDrawer}>Open drawer</button></div>
        <lv-drawer .open=${this.drawerOpen} .modal=${this.drawerModal} .closeOnOutside=${!this.drawerModal} .size=${this.drawerWide ? 'wide' : 'default'} label="Playground settings" @lv-drawer-close=${this.closeDrawer}>
          <span slot="title">${this.longText ? title : 'Workspace settings'}</span><span slot="subtitle">Local fixture · changes stay in this preview</span>
          <div class="drawer-content">${this.settingsContent('plain')}<button class="settings-button" @click=${this.closeDrawer}>Close drawer</button></div>
        </lv-drawer>`
      case 'identity': return html`<div class="identity-samples">
        ${['Alex Morgan', 'Sam', '', this.longText ? 'María Fernanda García Fernández' : '李 明'].map((name) => html`<div class="identity-sample"><div class="row"><lv-user-avatar .name=${name} size="small" role="img" aria-label=${name || 'Unknown user'}></lv-user-avatar><lv-user-avatar .name=${name} size="medium" role="img" aria-label=${name || 'Unknown user'}></lv-user-avatar></div><span class="caption">${name || 'Unknown user fallback'}</span></div>`)}
        <div class="identity-sample"><div class="brand"><lv-brand-mark aria-hidden="true"></lv-brand-mark>LeapView</div><div class="brand accent"><lv-brand-mark large aria-hidden="true"></lv-brand-mark>LeapView</div></div>
        <div class="identity-sample"><strong>Field types</strong>${['string', 'integer', 'date', 'timestamp', 'boolean', 'json', 'binary', 'unknown'].map((type) => html`<span class="row"><span aria-hidden="true">${lucideIcon(fieldTypeIcon(type), { size: 18 })}</span><span>${type}</span></span>`)}</div>
      </div>`
      case 'toast-region': return html`<div class="column"><div class="row"><button class="settings-button primary" @click=${this.showNotification}>Show notification</button><button class="settings-button" ?disabled=${!this.lastToast} @click=${() => { this.renderRoot.querySelector<LeapViewToastRegion>('lv-toast-region')?.dismiss(this.lastToast); this.record('dismiss()', { id: this.lastToast }); this.lastToast = 0 }}>Dismiss latest</button></div></div>
        <lv-toast-region @lv-toast-action=${(event: CustomEvent) => this.record(event.type)} @lv-toast-dismiss=${(event: CustomEvent) => this.record(event.type)}></lv-toast-region>`
      case 'one-time-secret': return html`<lv-one-time-secret .secret=${this.longText ? `${demoSecret}_${'long_example_'.repeat(12)}` : demoSecret} message="Dummy secret for this example. Copy to inspect the production feedback." copy-label="Copy dummy secret"></lv-one-time-secret>`
      case 'empty-state': return renderEmptyState({
        title: this.actionDone ? 'Example action complete' : this.variant === 'error' ? 'Could not load dashboards' : this.variant === 'search' ? 'No matching dashboards' : 'No dashboards yet',
        description: this.longText ? 'This workspace has no dashboards matching your current filters. Change the search terms or create an example dashboard to explore how longer guidance wraps in narrow containers.' : this.variant === 'error' ? 'Try the local action again.' : 'Create a dashboard or adjust your search.',
        icon: lucideIcon(this.variant === 'error' ? CircleAlert : this.variant === 'search' ? Search : FolderOpen, { size: 24 }), role: this.variant === 'error' ? 'alert' : 'status',
        actions: html`<button class="settings-button primary" @click=${() => { this.actionDone = true; this.record('click', { action: this.variant === 'error' ? 'retry' : 'create-example' }) }}>${this.variant === 'error' ? 'Retry' : 'Create example'}</button>`,
      })
      case 'page-header': return html`<div class="column">${renderPageHeader(title, this.longText ? 'Explore revenue, adoption, retention and operations across multiple teams, products and regions in a single shared analytics workspace.' : 'Explore performance across your workspace.', 'Analytics', html`<button class="settings-button primary" ?disabled=${this.disabled} @click=${() => { this.actionDone = !this.actionDone; this.record('click', { action: 'refresh', complete: this.actionDone }) }}>Refresh</button>`)}<p role="status">${this.actionDone ? 'Local refresh complete.' : 'Ready to refresh.'}</p></div>`
      case 'breadcrumb': return html`<div @click=${this.localLink}>${renderBreadcrumb([{ label: 'Workspace', href: '#surfaces/breadcrumb' }, { label: 'Dashboards', href: '#surfaces/breadcrumb', prefix: renderAssetBreadcrumbGlyph('dashboard') }, { label: title, current: true }], 'Example breadcrumb')}</div>`
      case 'settings': return this.settingsContent(this.variant === 'card' ? 'card' : this.variant === 'plain' ? 'plain' : 'panel')
      case 'entity-detail': return html`<div @click=${this.localLink}>${renderEntityDetail({ label: 'Example entity', backHref: '#surfaces/entity-detail', backLabel: 'All members', title: this.longText ? 'Alex Morgan — Regional Analytics and Operational Excellence' : this.variant === 'team' ? 'Analytics team' : 'Alex Morgan', subtitle: this.variant === 'team' ? '12 members · Local example' : 'alex@example.test', avatar: this.variant === 'team' ? lucideIcon(Users, { size: 32 }) : 'AM', avatarTreatment: this.variant === 'team' ? 'plain' : 'framed', badges: html`<span class="badge">${this.actionDone ? 'Viewer' : 'Editor'}</span><span class="badge">Active</span>`, actions: html`<button class="settings-button" ?disabled=${this.disabled} @click=${() => { this.actionDone = !this.actionDone; this.record('click', { action: 'change-role', role: this.actionDone ? 'Viewer' : 'Editor' }) }}>Change role</button>`, notice: this.variant === 'notice' ? html`<aside class="detail-notice"><span class="detail-notice-icon">${lucideIcon(CircleAlert)}</span><span><strong>Managed membership.</strong> This example notice comes from the shared entity-detail styles.</span></aside>` : nothing, sections: html`<section class="detail-section"><h2>Details</h2><dl class="facts"><div class="fact"><dt>Email</dt><dd>alex@example.test</dd></div><div class="fact"><dt>Team</dt><dd>Analytics</dd></div><div class="fact"><dt>Created</dt><dd>October 2, 2026</dd></div><div class="fact"><dt>Status</dt><dd>Active</dd></div></dl></section>` })}</div>`
      case 'icon-picker': return html`<lv-dashboard-icon-picker .icon=${this.appearance.icon} .color=${this.appearance.color} .label=${title} @lv-dashboard-appearance-select=${this.selectAppearance}></lv-dashboard-icon-picker>`
      case 'appearance': return html`<lv-dashboard-appearance-editor .appearance=${this.appearance} .label=${title} asset-id="playground-dashboard" @lv-dashboard-appearance-change=${this.selectAppearance}></lv-dashboard-appearance-editor>`
      case 'report-footer': return html`<div class="column" @lv-report-zoom-command=${this.zoomCommand}>
        <div class="report-window"><div style=${`width:${(this.zoom.layout === 'mobile' ? 300 : 600) * this.zoom.scale}px;height:${220 * this.zoom.scale}px`}><section class=${`report-page ${this.zoom.layout}`} style=${`transform:scale(${this.zoom.scale})`}><h2>${title}</h2><p>Local report page · ${this.zoom.layout} layout</p><p>${Math.round(this.zoom.scale * 100)}% · ${this.zoom.mode}</p></section></div></div>
        <lv-report-footer .status=${this.variant === 'error' ? { error: 'Local example refresh failure' } : this.variant === 'loading' ? { loading: true } : this.variant === 'empty' ? {} : { lastUpdated: '2026-10-02T09:30:00Z' }}></lv-report-footer>
      </div>`
      default: return nothing
    }
  }

  private settingsContent(appearance: 'panel' | 'card' | 'plain') {
    return html`<form class="settings-stack" @submit=${(event: Event) => { event.preventDefault(); this.savedName = this.name; this.record('submit', { name: this.savedName }) }}>
      ${renderSettingsSection({ label: 'Workspace settings', heading: 'General', description: 'Edit deterministic values in this preview.', appearance, content: html`
        ${renderSettingsRow({ label: this.longText ? 'Workspace display name shown in navigation and shared dashboard headers' : 'Workspace name', description: 'Changes apply only to this example.', controlId: 'surface-workspace-name', control: html`<input id="surface-workspace-name" class="settings-input" .value=${this.name} ?disabled=${this.disabled} @input=${(event: Event) => { this.name = (event.target as HTMLInputElement).value }}>` })}
        ${renderSettingsRow({ label: 'Saved value', control: html`<span class="settings-value" role="status">${this.savedName}</span>` })}
        ${renderSettingsActions(html`<button class="settings-button primary" type="submit" ?disabled=${this.disabled}>Save</button><button class="settings-button" type="button" ?disabled=${this.disabled} @click=${() => { this.name = this.savedName; this.record('click', { action: 'reset' }) }}>Reset</button>`)}
      ` })}
    </form>`
  }

  private openDrawer = async (event: Event): Promise<void> => {
    this.drawerTrigger = event.currentTarget as HTMLElement
    this.drawerOpen = true
    this.record('click', { action: 'open-drawer' })
    await this.updateComplete
    this.renderRoot.querySelector('lv-drawer')?.focusFirst()
  }

  private showNotification = (): void => {
    const region = this.renderRoot.querySelector<LeapViewToastRegion>('lv-toast-region')
    if (!region) return
    this.lastToast = region.show({
      message: this.longText ? 'This deterministic example notification explains an operation across several teams, including the next action and any relevant local context.' : this.variant === 'error' ? 'Example operation failed.' : 'Example changes saved.',
      tone: this.variant === 'error' ? 'error' : this.variant === 'success' ? 'success' : 'info',
      durationMs: 5000,
      ...(this.variant === 'undo' ? { action: { label: 'Undo', onClick: () => this.record('action callback', { action: 'undo' }) } } : {}),
    })
    this.record('show()', { id: this.lastToast, variant: this.variant })
  }

  private closeDrawer = (event: Event): void => {
    this.drawerOpen = false
    this.record(event.type, { action: 'close-drawer' })
    this.drawerTrigger?.focus()
  }

  private localLink = (event: MouseEvent): void => {
    const anchor = event.composedPath().find((node) => node instanceof HTMLAnchorElement) as HTMLAnchorElement | undefined
    if (!anchor) return
    event.preventDefault()
    this.record('click', { href: anchor.getAttribute('href'), label: anchor.textContent?.trim() })
  }

  private selectAppearance = (event: CustomEvent<{ icon?: string; color?: string }>): void => {
    this.record(event.type, event.detail)
    const reset = event.detail.icon === 'default' || event.detail.color === 'default'
    this.appearance = { icon: reset ? 'layout-dashboard' : event.detail.icon ?? this.appearance.icon, color: reset ? 'purple' : event.detail.color ?? this.appearance.color, revision: this.appearance.revision + 1 }
  }

  private zoomCommand = (event: CustomEvent<ZoomCommand>): void => {
    this.record(event.type, event.detail)
    this.applyZoom(event.detail)
  }

  private applyZoom(command: ZoomCommand): void {
    const layoutMode = command.layout ?? this.zoom.layoutMode
    const layout = resolvedLayoutMode(layoutMode)
    const mode = layout === 'mobile' ? 'mobile' : command.mode ?? (this.zoom.mode === 'mobile' ? 'fit-width' : this.zoom.mode)
    const viewport = this.renderRoot.querySelector<HTMLElement>('.report-window')
    const viewportStyle = viewport ? getComputedStyle(viewport) : undefined
    const width = (viewport?.clientWidth ?? 600) - (Number.parseFloat(viewportStyle?.paddingLeft ?? '0') || 0) - (Number.parseFloat(viewportStyle?.paddingRight ?? '0') || 0)
    const height = (viewport?.clientHeight ?? 220) - (Number.parseFloat(viewportStyle?.paddingTop ?? '0') || 0) - (Number.parseFloat(viewportStyle?.paddingBottom ?? '0') || 0)
    let scale = clampScale(command.scale ?? this.zoom.scale)
    if (mode === 'actual-size' || mode === 'mobile') scale = 1
    if (mode === 'fit-width') scale = clampFittedScale(width / 600)
    if (mode === 'fit-page') scale = clampFittedScale(Math.min(width / 600, height / 220))
    if (this.zoom.layoutMode !== layoutMode || this.zoom.layout !== layout || this.zoom.mode !== mode || this.zoom.scale !== scale) this.zoom = { layoutMode, layout, mode, scale }
  }
}

if (!customElements.get('playground-surfaces')) customElements.define('playground-surfaces', PlaygroundSurfaces)
