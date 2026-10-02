import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import { chartExamples } from './charts'
import { controlExamples } from './controls'
import { tokenExamples } from './tokens'

const parameters = new URLSearchParams(location.search)
const initialTheme = parameters.get('theme')
if (initialTheme) document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: initialTheme } }))

const groups = [
  { id: 'tokens', label: 'Design tokens', examples: tokenExamples },
  { id: 'controls', label: 'UI components', examples: controlExamples },
  { id: 'charts', label: 'Charts & data', examples: chartExamples },
]

class PlaygroundApp extends LitElement {
  @state() private route = location.hash.slice(1) || 'charts/bar'
  @state() private search = ''
  @state() private width = ['360', '768', '1200'].includes(parameters.get('width') || '') ? parameters.get('width')! : 'responsive'
  @state() private height = ['260', '420', '640'].includes(parameters.get('height') || '') ? parameters.get('height')! : '420'
  @state() private theme = document.documentElement.dataset.themePreference || 'light'
  @state() private previewOnly = parameters.get('preview') === '1'
  private readonly exitPreview = (event: KeyboardEvent) => { if (event.key === 'Escape') this.previewOnly = false }
  private readonly navigate = () => { this.route = location.hash.slice(1) || 'charts/bar' }

  connectedCallback() {
    super.connectedCallback()
    window.addEventListener('hashchange', this.navigate)
    window.addEventListener('keydown', this.exitPreview)
  }
  disconnectedCallback() {
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
    nav { display: grid; gap: var(--base-size-20); margin-top: var(--base-size-20); }
    nav h2 { color: var(--lv-fg-muted); font: var(--lv-type-caption); margin: 0 0 var(--base-size-8); }
    nav a { display: block; padding: var(--base-size-6) var(--base-size-8); border-radius: var(--lv-radius-small); color: var(--lv-fg-default); text-decoration: none; font: var(--lv-type-body-compact); }
    nav a:hover { background: var(--lv-bg-panel-muted); }
    nav a[aria-current='page'] { background: var(--lv-bg-accent-muted); color: var(--lv-fg-accent); font-weight: var(--base-text-weight-semibold); }
    a:focus-visible, input:focus-visible { outline: var(--borderWidth-thick) solid var(--focus-outlineColor); outline-offset: var(--base-size-2); }
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
      nav { max-height: 190px; overflow: auto; grid-template-columns: repeat(3, minmax(0, 1fr)); }
      main { padding: var(--base-size-12); }
    }
  `]

  render() {
    const [groupID, exampleID] = this.route.split('/')
    const group = groups.find(item => item.id === groupID)
    const example = group?.examples.find(item => item.id === exampleID)
    const preview = example ? keyed(this.route, groupID === 'charts'
      ? html`<playground-charts .example=${exampleID} ?preview-only=${this.previewOnly}></playground-charts>`
      : groupID === 'controls'
        ? html`<playground-controls .example=${exampleID} ?preview-only=${this.previewOnly}></playground-controls>`
        : html`<playground-tokens .example=${exampleID} ?preview-only=${this.previewOnly}></playground-tokens>`)
      : html`<h2>Example not found</h2><p>Choose an example from the navigation.</p>`
    return html`
      <a class="skip" ?hidden=${this.previewOnly} href="#main" @click=${(event: Event) => { event.preventDefault(); this.renderRoot.querySelector<HTMLElement>('main')?.focus() }}>Skip to preview</a>
      <header ?hidden=${this.previewOnly}><div><h1>LeapView playground</h1><p>Production components. Local fixtures. One shared design language.</p></div>
        <label>Theme<select aria-label="Theme" class="settings-input" .value=${this.theme} @change=${this.changeTheme}>
          ${['light', 'dark', 'dark_dimmed', 'light_colorblind', 'dark_colorblind', 'light_tritanopia', 'dark_tritanopia'].map(theme => html`<option value=${theme}>${theme.replaceAll('_', ' ')}</option>`)}
        </select></label>
      </header>
      <div class=${this.previewOnly ? 'workspace solo' : 'workspace'}>
        <aside ?hidden=${this.previewOnly}><label>Find an example<input class="settings-input search" type="search" .value=${this.search} @input=${(event: Event) => { this.search = (event.target as HTMLInputElement).value }}></label>
          <nav aria-label="Examples">${groups.map(item => html`<section><h2>${item.label}</h2>${item.examples.filter(entry => `${entry.label} ${entry.id}`.toLowerCase().includes(this.search.toLowerCase())).map(entry => html`<a href=${`#${item.id}/${entry.id}`} aria-current=${this.route === `${item.id}/${entry.id}` ? 'page' : nothing}>${entry.label}</a>`)}</section>`)}</nav>
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

  private changeTheme(event: Event) {
    this.theme = (event.target as HTMLSelectElement).value
    document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: this.theme } }))
  }
}
customElements.define('playground-app', PlaygroundApp)
