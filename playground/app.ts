import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import { ChevronDown, ExternalLink, Maximize2, Minimize2, Moon, PanelLeft, Sun } from 'lucide'
import { lucideIcon } from '../web/components/shared/lucide-icons'
import '../web/components/shared/brand-mark'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import { chartExamples } from './charts'
import { controlExamples } from './controls'
import { tokenExamples } from './tokens'
import { graphExamples } from './graphs'
import { contentExamples } from './content'
import { tableExamples } from './tables'
import { surfaceExamples } from './surfaces'
import { filterExamples } from './filters'

const parameters = new URLSearchParams(location.search)
const initialTheme = parameters.get('theme')
if (initialTheme) document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: initialTheme } }))

const groups = [
  { id: 'tokens', label: 'Design tokens', examples: tokenExamples },
  { id: 'controls', label: 'UI components', examples: controlExamples },
  { id: 'charts', label: 'Charts & data', examples: chartExamples },
  { id: 'graphs', label: 'Lineage & models', examples: graphExamples },
  { id: 'tables', label: 'Tables & lists', examples: tableExamples },
  { id: 'content', label: 'Editors & content', examples: contentExamples },
  { id: 'surfaces', label: 'Layout & identity', examples: surfaceExamples },
  { id: 'filters', label: 'Dashboard filters', examples: filterExamples },
]

class PlaygroundApp extends LitElement {
  @state() private route = location.hash.slice(1) || 'charts/bar'
  @state() private search = ''
  @state() private navigationOpen = false
  @state() private expandedGroups = new Set([this.route.split('/')[0]])
  @state() private width = ['360', '768', '1200'].includes(parameters.get('width') || '') ? parameters.get('width')! : 'responsive'
  @state() private height = ['260', '420', '640'].includes(parameters.get('height') || '') ? parameters.get('height')! : '420'
  @state() private theme = document.documentElement.dataset.themePreference || 'light'
  @state() private dark = document.documentElement.style.colorScheme === 'dark'
  @state() private previewOnly = parameters.get('preview') === '1'
  private readonly exitPreview = (event: KeyboardEvent) => {
    if (event.key !== 'Escape' || event.defaultPrevented) return
    // Let the focused production dialog or popup handle its own dismissal.
    if (event.composedPath().some(target => target instanceof Element && target.matches('dialog, [role=dialog], [role=alertdialog], [role=menu], [role=listbox], :popover-open'))) return
    if (this.previewOnly) void this.leavePreview()
    else if (this.navigationOpen) {
      this.navigationOpen = false
      this.renderRoot.querySelector<HTMLElement>('.browse-toggle')?.focus()
    }
  }
  private readonly navigate = async () => {
    const fromMobileNavigation = this.navigationOpen
    this.navigationOpen = false
    this.route = location.hash.slice(1) || 'charts/bar'
    this.expandedGroups = new Set([this.route.split('/')[0]])
    await this.updateComplete
    const main = this.renderRoot.querySelector<HTMLElement>('main')
    if (main) main.scrollTop = 0
    if (fromMobileNavigation) main?.focus({ preventScroll: true })
  }
  private readonly themeApplied = (event: Event) => {
    const { mode, resolvedMode } = (event as CustomEvent<{ mode: string; resolvedMode: string }>).detail
    this.theme = mode
    this.dark = resolvedMode === 'dark'
  }

  connectedCallback() {
    super.connectedCallback()
    document.addEventListener('leapview-theme-applied', this.themeApplied)
    window.addEventListener('hashchange', this.navigate)
    window.addEventListener('keydown', this.exitPreview)
  }
  disconnectedCallback() {
    document.removeEventListener('leapview-theme-applied', this.themeApplied)
    window.removeEventListener('hashchange', this.navigate)
    window.removeEventListener('keydown', this.exitPreview)
    super.disconnectedCallback()
  }

  static styles = [settingsLayoutStyles, css`
    :host { display: grid; grid-template-rows: auto minmax(0, 1fr); height: 100dvh; color: var(--lv-fg-default); background: var(--lv-bg-page); }
    * { box-sizing: border-box; }
    .app-header { display: flex; align-items: center; gap: var(--base-size-12); padding: var(--base-size-12) var(--base-size-20); border-bottom: var(--lv-border-muted); }
    .brand { display: flex; align-items: center; gap: var(--base-size-8); flex: 1; min-width: 0; font: var(--lv-type-body); }
    .brand strong { font-weight: var(--base-text-weight-semibold); }
    .brand span { color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .brand lv-brand-mark { --lv-brand-mark-size: var(--base-size-24); }
    .workspace { display: grid; grid-row: 2; grid-template-columns: 240px minmax(0, 1fr); min-height: 0; }
    aside { min-height: 0; overflow: auto; padding: var(--base-size-16); border-right: var(--lv-border-muted); }
    nav { display: grid; gap: var(--base-size-8); margin-top: var(--base-size-16); }
    .group-button { display: flex; align-items: center; gap: var(--base-size-8); width: 100%; min-height: var(--control-medium-size); padding: var(--base-size-8); border: 0; border-radius: var(--lv-radius-small); color: var(--lv-fg-default); background: transparent; font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); text-align: left; cursor: pointer; }
    .group-button:hover { background: var(--lv-bg-panel-muted); }
    .group-label { flex: 1; min-width: 0; }
    .count { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .chevron { display: inline-flex; transform: rotate(-90deg); }
    .group-button[aria-expanded='true'] .chevron { transform: none; }
    .group-links { padding: var(--base-size-4) 0 var(--base-size-8) var(--base-size-12); }
    .theme-toggle { width: var(--control-medium-size); padding: 0; flex-shrink: 0; }
    nav a { display: block; padding: var(--base-size-6) var(--base-size-8); border-radius: var(--lv-radius-small); color: var(--lv-fg-default); text-decoration: none; font: var(--lv-type-body-compact); }
    nav a:hover { background: var(--lv-bg-panel-muted); }
    nav a[aria-current='page'] { background: var(--lv-bg-accent-muted); color: var(--lv-fg-accent); font-weight: var(--base-text-weight-semibold); }
    a:focus-visible, input:focus-visible, button:focus-visible { outline: var(--borderWidth-thick) solid var(--focus-outlineColor); outline-offset: var(--base-size-2); }
    main { min-width: 0; min-height: 0; overflow: auto; padding: var(--base-size-24); }
    main:focus { outline: none; }
    .example-header { display: flex; flex-wrap: wrap; align-items: end; gap: var(--base-size-16); margin-bottom: var(--base-size-20); }
    .heading { flex: 1; min-width: 12rem; }
    h1 { margin: 0; font: var(--lv-type-page-title); overflow-wrap: anywhere; }
    .eyebrow { margin: 0 0 var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .toolbar { display: flex; align-items: end; flex-wrap: wrap; gap: var(--base-size-8); }
    label { display: grid; gap: var(--base-size-4); font: var(--lv-type-caption); }
    .toolbar .settings-input { max-width: 10rem; }
    .viewport { max-width: 100%; margin: 0 auto; }
    .workspace.solo { grid-template-columns: minmax(0, 1fr); }
    .workspace.solo main { padding-top: var(--base-size-64); }
    .exit-preview { position: fixed; z-index: var(--z-index-toast); top: var(--base-size-12); right: var(--base-size-16); }
    [hidden] { display: none !important; }
    .skip { position: fixed; top: -100px; }
    .skip:focus { top: 0; z-index: var(--z-index-toast); background: var(--lv-bg-page); padding: var(--base-size-8); }
    .search { width: 100%; }
    .browse-toggle { display: none; }
    .empty-search { color: var(--lv-fg-muted); font: var(--lv-type-body-compact); overflow-wrap: anywhere; }
    @media (max-width: 720px) {
      .app-header { padding: var(--base-size-12); }
      .brand { flex-wrap: wrap; column-gap: var(--base-size-6); row-gap: 0; }
      .brand span { display: none; }
      .browse-toggle { display: inline-flex; }
      .workspace { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(0, 1fr); }
      aside { display: none; max-height: 45dvh; border-right: 0; border-bottom: var(--lv-border-muted); }
      aside[data-open] { display: block; }
      main { grid-row: 2; padding: var(--base-size-16) var(--base-size-12); }
      .example-header { gap: var(--base-size-12); margin-bottom: var(--base-size-16); }
      .heading { flex-basis: 100%; }
      .toolbar { width: 100%; }
      .toolbar .settings-input { max-width: 8rem; }
      .open-preview { width: var(--control-medium-size); padding: 0; }
      .open-preview .action-label { display: none; }
    }
  `]

  render() {
    const [groupID, exampleID] = this.route.split('/')
    const group = groups.find(item => item.id === groupID)
    const example = group?.examples.find(item => item.id === exampleID)
    const preview = example ? keyed(this.route, groupID === 'charts'
      ? html`<playground-charts .example=${exampleID} ?preview-only=${this.previewOnly}></playground-charts>`
      : groupID === 'graphs' ? html`<playground-graphs .example=${exampleID} ?preview-only=${this.previewOnly}></playground-graphs>`
      : groupID === 'tables' ? html`<playground-tables .example=${exampleID} ?preview-only=${this.previewOnly}></playground-tables>`
      : groupID === 'content' ? html`<playground-content .example=${exampleID} ?preview-only=${this.previewOnly}></playground-content>`
      : groupID === 'surfaces' ? html`<playground-surfaces .example=${exampleID} ?preview-only=${this.previewOnly}></playground-surfaces>`
      : groupID === 'filters' ? html`<playground-filters .example=${exampleID} ?preview-only=${this.previewOnly}></playground-filters>`
      : groupID === 'controls'
        ? html`<playground-controls .example=${exampleID} ?preview-only=${this.previewOnly}></playground-controls>`
        : html`<playground-tokens .example=${exampleID} ?preview-only=${this.previewOnly}></playground-tokens>`)
      : html`<p>Choose an example from the navigation.</p>`
    const visibleGroups = groups.map(item => ({ ...item, examples: this.matchingExamples(item) })).filter(item => item.examples.length > 0)
    const themeAction = this.dark ? 'Switch to light mode' : 'Switch to dark mode'
    return html`
      <a class="skip" ?hidden=${this.previewOnly} href="#main" @click=${(event: Event) => { event.preventDefault(); this.renderRoot.querySelector<HTMLElement>('main')?.focus() }}>Skip to preview</a>
      <header class="app-header" ?hidden=${this.previewOnly}>
        <div class="brand"><lv-brand-mark aria-hidden="true"></lv-brand-mark><strong>LeapView</strong><span>Playground</span></div>
        <button type="button" class="settings-button browse-toggle" aria-expanded=${String(this.navigationOpen)} aria-controls="example-navigation" @click=${() => { this.navigationOpen = !this.navigationOpen }}>${lucideIcon(PanelLeft, { size: 16 })} Browse</button>
        <button type="button" class="settings-button theme-toggle" aria-label=${themeAction} title=${themeAction} @click=${this.changeTheme}>${lucideIcon(this.dark ? Sun : Moon, { size: 18 })}</button>
      </header>
      <button type="button" class="settings-button exit-preview" ?hidden=${!this.previewOnly} @click=${this.leavePreview} title="Exit preview (Escape)">${lucideIcon(Minimize2, { size: 16 })} Exit preview</button>
      <div class=${this.previewOnly ? 'workspace solo' : 'workspace'}>
        <aside id="example-navigation" ?data-open=${this.navigationOpen} ?hidden=${this.previewOnly}><input aria-label="Find an example" placeholder="Search examples…" class="settings-input search" type="search" .value=${this.search} @input=${this.changeSearch}>
          <nav aria-label="Examples">${visibleGroups.map(item => html`<section>
            <button type="button" class="group-button" aria-expanded=${String(this.expandedGroups.has(item.id))} aria-controls=${`examples-${item.id}`} @click=${() => this.toggleGroup(item.id)}>
              <span class="chevron" aria-hidden="true">${lucideIcon(ChevronDown, { size: 16 })}</span><span class="group-label">${item.label}</span><span class="count" aria-hidden="true">${item.examples.length}</span>
            </button>
            <div class="group-links" id=${`examples-${item.id}`} ?hidden=${!this.expandedGroups.has(item.id)}>${item.examples.map(entry => html`<a href=${`#${item.id}/${entry.id}`} @click=${() => { if (this.route === `${item.id}/${entry.id}`) void this.navigate() }} aria-current=${this.route === `${item.id}/${entry.id}` ? 'page' : nothing}>${entry.label}</a>`)}</div>
          </section>`)}</nav>
          ${visibleGroups.length === 0 ? html`<p class="empty-search" role="status">No examples match “${this.search.trim()}”.</p>` : nothing}
        </aside>
        <main id="main" tabindex="-1" aria-label=${example?.label || 'Preview'}>
          <div class="example-header" ?hidden=${this.previewOnly}>
            <div class="heading"><p class="eyebrow">${group?.label || 'Playground'}</p><h1>${example?.label || 'Example not found'}</h1></div>
            <div class="toolbar">
              <label>Width<select aria-label="Preview width" class="settings-input" .value=${this.width} @change=${(event: Event) => { this.width = (event.target as HTMLSelectElement).value }}>
                <option value="responsive">Responsive</option><option value="360">360 px</option><option value="768">768 px</option><option value="1200">1200 px</option>
              </select></label>
              ${['charts', 'graphs', 'tables'].includes(groupID) ? html`<label>Height<select aria-label="Preview height" class="settings-input" .value=${this.height} @change=${(event: Event) => { this.height = (event.target as HTMLSelectElement).value }}>
                <option value="260">260 px</option><option value="420">420 px</option><option value="640">640 px</option>
              </select></label>` : nothing}
              <button type="button" class="settings-button focus-preview" aria-label="Focus preview" title="Hide controls and details" @click=${this.enterPreview}>${lucideIcon(Maximize2, { size: 16 })} Focus</button>
              <a class="settings-button open-preview" aria-label="Open default preview" title="Open default preview in a new tab" href=${`?preview=1&theme=${this.theme}&width=${this.width}&height=${this.height}#${this.route}`} target="_blank" rel="noopener">${lucideIcon(ExternalLink, { size: 16 })}<span class="action-label">Open preview</span></a>
            </div>
          </div>
          <div class="viewport" style=${`width: ${this.width === 'responsive' ? '100%' : this.width + 'px'}; --playground-preview-height: ${this.height}px`}>${preview}</div>
        </main>
      </div>`
  }

  private async enterPreview() {
    this.previewOnly = true
    await this.updateComplete
    this.renderRoot.querySelector<HTMLElement>('.exit-preview')?.focus()
  }

  private async leavePreview() {
    this.previewOnly = false
    await this.updateComplete
    this.renderRoot.querySelector<HTMLElement>('.focus-preview')?.focus()
  }

  private matchingExamples(group: typeof groups[number]) {
    const query = this.search.trim().toLowerCase()
    return group.examples.filter(entry => `${group.label} ${entry.label} ${entry.id}`.toLowerCase().includes(query))
  }

  private changeSearch(event: Event) {
    this.search = (event.target as HTMLInputElement).value
    this.expandedGroups = new Set(this.search.trim()
      ? groups.filter(group => this.matchingExamples(group).length > 0).map(group => group.id)
      : [this.route.split('/')[0]])
  }

  private toggleGroup(id: string) {
    const expanded = new Set(this.expandedGroups)
    if (expanded.has(id)) expanded.delete(id)
    else expanded.add(id)
    this.expandedGroups = expanded
  }

  private changeTheme() {
    document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: this.dark ? 'light' : 'dark' } }))
  }
}
customElements.define('playground-app', PlaygroundApp)
