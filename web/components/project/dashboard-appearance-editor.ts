import { LitElement, css, html, nothing, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { DashboardAppearanceSignal } from '../../generated/signals'
import { lucideIconByCanonicalName } from '../shared/lucide-catalog'
import { lucideIcon } from '../shared/lucide-icons'
import '../app/dashboard-icon-picker'

type AppearanceValue = Pick<DashboardAppearanceSignal, 'icon' | 'color'>

class DashboardAppearanceEditor extends LitElement {
  @property({ attribute: false }) appearance: DashboardAppearanceSignal | null = null
  @property() label = 'Dashboard'
  @property({ attribute: 'asset-id' }) assetID = ''
  @state() private editorOpen = false
  @state() private optimistic: AppearanceValue | null = null
  @state() private pending = false
  @state() private error = ''

  static styles = css`
    :host { display: grid; min-width: 0; align-content: start; border-bottom: var(--lv-border-muted); padding-bottom: var(--base-size-20); }
    .dashboard-appearance { display: grid; gap: var(--base-size-12); }
    h2 { color: var(--lv-fg-default); font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .dashboard-appearance-summary { display: grid; min-width: 0; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-12); }
    .dashboard-appearance-preview { display: grid; width: var(--base-size-48); height: var(--base-size-48); place-items: center; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--display-purple-bgColor-muted); color: var(--display-purple-fgColor); }
    .dashboard-appearance-preview.appearance-color-gray { background: var(--display-gray-bgColor-muted); color: var(--display-gray-fgColor); }
    .dashboard-appearance-preview.appearance-color-blue { background: var(--display-blue-bgColor-muted); color: var(--display-blue-fgColor); }
    .dashboard-appearance-preview.appearance-color-green { background: var(--display-green-bgColor-muted); color: var(--display-green-fgColor); }
    .dashboard-appearance-preview.appearance-color-yellow { background: var(--display-yellow-bgColor-muted); color: var(--display-yellow-fgColor); }
    .dashboard-appearance-preview.appearance-color-orange { background: var(--display-orange-bgColor-muted); color: var(--display-orange-fgColor); }
    .dashboard-appearance-preview.appearance-color-red { background: var(--display-red-bgColor-muted); color: var(--display-red-fgColor); }
    .dashboard-appearance-preview.appearance-color-purple { background: var(--display-purple-bgColor-muted); color: var(--display-purple-fgColor); }
    .dashboard-appearance-preview.appearance-color-pink { background: var(--display-pink-bgColor-muted); color: var(--display-pink-fgColor); }
    .dashboard-appearance-preview.appearance-color-coral { background: var(--display-coral-bgColor-muted); color: var(--display-coral-fgColor); }
    .dashboard-appearance-current { display: grid; min-width: 0; grid-template-columns: minmax(0, max-content) auto; gap: var(--base-size-4) var(--base-size-8); }
    .dashboard-appearance-current > span:first-child { grid-column: 1 / -1; color: var(--lv-fg-muted); font: var(--lv-type-caption); text-transform: uppercase; }
    .dashboard-appearance-current code { overflow: hidden; color: var(--lv-fg-default); text-overflow: ellipsis; white-space: nowrap; font: var(--lv-type-body-compact); }
    .dashboard-appearance-color { color: var(--lv-fg-muted); font: var(--lv-type-caption); text-transform: capitalize; }
    .dashboard-appearance-edit { display: inline-flex; min-height: var(--lv-button-height); align-items: center; justify-content: center; border: var(--lv-border-default); border-radius: var(--lv-button-radius); background: var(--lv-button-bg-rest); color: var(--lv-button-fg-rest); padding: 0 var(--lv-button-padding-inline-sm); cursor: pointer; font: var(--lv-type-body-compact); white-space: nowrap; }
    .dashboard-appearance-edit:hover { background: var(--lv-button-bg-hover); }
    .dashboard-appearance-edit:active { background: var(--lv-button-bg-active); }
    .dashboard-appearance-edit:focus-visible { outline: var(--focus-outline); outline-offset: var(--base-size-2); }
    .dashboard-appearance-editor { width: min(100%, 22.5rem); }
    .dashboard-appearance-status { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .dashboard-appearance-status.error { color: var(--lv-fg-danger); }
    @media (max-width: 720px) {
      .dashboard-appearance-summary { grid-template-columns: auto minmax(0, 1fr); }
      .dashboard-appearance-edit { grid-column: 1 / -1; justify-self: start; }
    }
  `

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleFetch)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleFetch)
    super.disconnectedCallback()
  }

  protected override updated(changed: PropertyValues<this>): void {
    if (changed.has('assetID')) this.resetState()
    const persisted = this.appearance
    if (this.pending && this.optimistic && persisted && persisted.icon === this.optimistic.icon && persisted.color === this.optimistic.color) {
      this.optimistic = null
      this.pending = false
    }
  }

  render() {
    if (!this.appearance) return nothing
    const appearance = this.optimistic ?? this.appearance
    return html`
      <section class="dashboard-appearance" aria-label="Dashboard appearance">
        <h2>Appearance</h2>
        <div class="dashboard-appearance-summary">
          <span class=${`dashboard-appearance-preview appearance-color-${appearanceColor(appearance.color)}`} aria-hidden="true">
            ${lucideIcon(lucideIconByCanonicalName(appearance.icon), { size: 24, strokeWidth: 1.8 })}
          </span>
          <div class="dashboard-appearance-current">
            <span>Current icon</span>
            <code>${appearance.icon}</code>
            <span class="dashboard-appearance-color">${appearanceColor(appearance.color)}</span>
          </div>
          <button type="button" class="dashboard-appearance-edit" aria-expanded=${this.editorOpen} @click=${() => { this.editorOpen = !this.editorOpen }}>${this.editorOpen ? 'Close editor' : 'Change icon'}</button>
        </div>
        ${this.editorOpen ? html`
          <div class="dashboard-appearance-editor" @lv-dashboard-appearance-select=${this.selectAppearance}>
            <lv-dashboard-icon-picker .icon=${appearance.icon} .color=${appearance.color} .label=${this.label}></lv-dashboard-icon-picker>
          </div>
        ` : nothing}
        ${this.pending ? html`<p class="dashboard-appearance-status" role="status">Saving appearance…</p>` : nothing}
        ${this.error ? html`<p class="dashboard-appearance-status error" role="alert">${this.error}</p>` : nothing}
      </section>
    `
  }

  private resetState(): void {
    this.editorOpen = false
    this.optimistic = null
    this.pending = false
    this.error = ''
  }

  private selectAppearance = (event: CustomEvent<{ icon?: string; color?: string }>) => {
    if (!this.appearance) return
    const current = this.optimistic ?? this.appearance
    const reset = event.detail.icon === 'default' || event.detail.color === 'default'
    const optimistic = reset
      ? { icon: 'layout-dashboard', color: 'purple' }
      : { icon: event.detail.icon ?? current.icon, color: event.detail.color ?? current.color }
    this.optimistic = optimistic
    this.pending = true
    this.error = ''
    this.dispatchEvent(new CustomEvent('lv-dashboard-appearance-change', {
      bubbles: true,
      composed: true,
      detail: reset ? { icon: 'default', color: 'default' } : optimistic,
    }))
  }

  private handleFetch = (event: Event): void => {
    if (!this.pending) return
    const detail = (event as CustomEvent<{ type?: string }>).detail
    if (detail?.type !== 'error' && detail?.type !== 'retries-failed') return
    this.optimistic = null
    this.pending = false
    this.error = 'Dashboard appearance could not be saved. Please try again.'
  }
}

function appearanceColor(value: string): string {
  return ['gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink', 'coral'].includes(value) ? value : 'purple'
}

if (!customElements.get('lv-dashboard-appearance-editor')) customElements.define('lv-dashboard-appearance-editor', DashboardAppearanceEditor)
