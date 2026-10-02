import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import { ChevronDown, Moon, Sun } from 'lucide'
import { lucideIcon } from '../web/components/shared/lucide-icons'
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
  @state() private expandedGroups = new Set([this.route.split('/')[0]])
  @state() private width = ['360', '768', '1200'].includes(parameters.get('width') || '') ? parameters.get('width')! : 'responsive'
  @state() private height = ['260', '420', '640'].includes(parameters.get('height') || '') ? parameters.get('height')! : '420'
  @state() private theme = document.documentElement.dataset.themePreference || 'light'
  @state() private dark = document.documentElement.style.colorScheme === 'dark'
  @state() private previewOnly = parameters.get('preview') === '1'
  private readonly exitPreview = (event: KeyboardEvent) => { if (event.key === 'Escape') this.previewOnly = false }
  private readonly navigate = () => {
    this.route = location.hash.slice(1) || 'charts/bar'
    this.expandedGroups = new Set([...this.expandedGroups, this.route.split('/')[0]])
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
    :host { display: block; min-height: 100vh; }
    * { box-sizing: border-box; }
    header { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: var(--base-size-16); padding: var(--base-size-20) var(--base-size-24); border-bottom: var(--lv-border-muted); }
    h1 { margin: 0; font: var(--lv-type-section-title); }
    p { margin: var(--base-size-4) 0 0; color: var(--lv-fg-muted); }
    .workspace { display: grid; grid-template-columns: 240px minmax(0, 1fr); min-height: calc(100vh - 100px); }
    aside { position: sticky; top: 0; align-self: start; max-height: 100vh; overflow: auto; padding: var(--base-size-16); border-right: var(--lv-border-muted); }
    nav { display: grid; gap: var(--base-size-8); margin-top: var(--base-size-20); }
    .group-button { display: flex; align-items: center; gap: var(--base-size-8); width: 100%; min-height: var(--control-medium-size); padding: var(--base-size-8); border: 0; border-radius: var(--lv-radius-small); color: var(--lv-fg-default); background: transparent; font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); text-align: left; cursor: pointer; }
    .group-button:hover { background: var(--lv-bg-panel-muted); }
    .group-label { flex: 1; min-width: 0; }
    .count { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .chevron { display: inline-flex; transform: rotate(-90deg); }
    .group-button[aria-expanded='true'] .chevron { transform: none; }
    .group-links { padding: var(--base-size-4) 0 var(--base-size-8) var(--base-size-12); }
    .theme-toggle { width: var(--control-large-size); height: var(--control-large-size); padding: 0; flex-shrink: 0; }
    nav a { display: block; padding: var(--base-size-6) var(--base-size-8); border-radius: var(--lv-radius-small); color: var(--lv-fg-default); text-decoration: none; font: var(--lv-type-body-compact); }
    nav a:hover { background: var(--lv-bg-panel-muted); }
    nav a[aria-current='page'] { background: var(--lv-bg-accent-muted); color: var(--lv-fg-accent); font-weight: var(--base-text-weight-semibold); }
    a:focus-visible, input:focus-visible, button:focus-visible { outline: var(--borderWidth-thick) solid var(--focus-outlineColor); outline-offset: var(--base-size-2); }
    main { min-width: 0; padding: var(--base-size-24); }
    .toolbar { display: flex; align-items: end; flex-wrap: wrap; gap: var(--base-size-16); margin-bottom: var(--base-size-24); }
    label { display: grid; gap: var(--base-size-4); font: var(--lv-type-caption); }
    .viewport { max-width: 100%; margin: 0 auto; }
    .workspace.solo { display: block; min-height: 100vh; }
    [hidden] { display: none !important; }
    .skip { position: absolute; top: -100px; }
    .skip:focus { top: 0; z-index: 1; background: var(--lv-bg-page); padding: var(--base-size-8); }
    .search { width: 100%; }
    @media (max-width: 720px) {
      .workspace { grid-template-columns: minmax(0, 1fr); }
      aside { position: static; max-height: none; overflow: visible; border-right: 0; border-bottom: var(--lv-border-muted); }
      nav { max-height: 240px; overflow: auto; }
      main { padding: var(--base-size-12); }
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
      : html`<h2>Example not found</h2><p>Choose an example from the navigation.</p>`
    const visibleGroups = groups.map(item => ({ ...item, examples: this.matchingExamples(item) })).filter(item => item.examples.length > 0)
    const themeAction = this.dark ? 'Switch to light mode' : 'Switch to dark mode'
    return html`
      <a class="skip" ?hidden=${this.previewOnly} href="#main" @click=${(event: Event) => { event.preventDefault(); this.renderRoot.querySelector<HTMLElement>('main')?.focus() }}>Skip to preview</a>
      <header ?hidden=${this.previewOnly}><div><h1>LeapView playground</h1><p>Production components. Local fixtures. One shared design language.</p></div>
        <button type="button" class="settings-button theme-toggle" aria-label=${themeAction} title=${themeAction} @click=${this.changeTheme}>
          <span aria-hidden="true">${lucideIcon(this.dark ? Sun : Moon, { size: 20 })}</span>
        </button>
      </header>
      <div class=${this.previewOnly ? 'workspace solo' : 'workspace'}>
        <aside ?hidden=${this.previewOnly}><label>Find an example<input class="settings-input search" type="search" .value=${this.search} @input=${this.changeSearch}></label>
          <nav aria-label="Examples">${visibleGroups.map(item => html`<section>
            <button type="button" class="group-button" aria-expanded=${String(this.expandedGroups.has(item.id))} aria-controls=${`examples-${item.id}`} @click=${() => this.toggleGroup(item.id)}>
              <span class="chevron" aria-hidden="true">${lucideIcon(ChevronDown, { size: 16 })}</span><span class="group-label">${item.label}</span><span class="count" aria-hidden="true">${item.examples.length}</span>
            </button>
            <div class="group-links" id=${`examples-${item.id}`} ?hidden=${!this.expandedGroups.has(item.id)}>${item.examples.map(entry => html`<a href=${`#${item.id}/${entry.id}`} aria-current=${this.route === `${item.id}/${entry.id}` ? 'page' : nothing}>${entry.label}</a>`)}</div>
          </section>`)}</nav>
          ${visibleGroups.length === 0 ? html`<p role="status">No examples match “${this.search.trim()}”.</p>` : nothing}
        </aside>
        <main id="main" tabindex="-1" aria-label=${example?.label || 'Preview'}>
          <div class="toolbar" ?hidden=${this.previewOnly}>
            <label>Preview width<select aria-label="Preview width" class="settings-input" .value=${this.width} @change=${(event: Event) => { this.width = (event.target as HTMLSelectElement).value }}>
              <option value="responsive">Responsive</option><option value="360">360 px · compact</option><option value="768">768 px · medium</option><option value="1200">1200 px · wide</option>
            </select></label>
            <label>Chart height<select aria-label="Chart height" class="settings-input" .value=${this.height} @change=${(event: Event) => { this.height = (event.target as HTMLSelectElement).value }}>
              <option value="260">260 px</option><option value="420">420 px</option><option value="640">640 px</option>
            </select></label>
            <button class="settings-button" @click=${() => { this.previewOnly = true }} title="Hide controls without resetting the example; press Escape to return">Clean preview (Esc to return)</button>
            <a class="settings-button" href=${`?preview=1&theme=${this.theme}&width=${this.width}&height=${this.height}#${this.route}`} target="_blank" rel="noopener">Open default preview</a>
          </div>
          <div class="viewport" style=${`width: ${this.width === 'responsive' ? '100%' : this.width + 'px'}; --playground-preview-height: ${this.height}px`}>${preview}</div>
        </main>
      </div>`
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
