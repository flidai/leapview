import { readState } from './example-state'
import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { styleMap } from 'lit/directives/style-map.js'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import { exampleChromeStyles, exampleDetails } from './example-chrome'

export { tokenExamples } from './catalog'

type Token = { name: string; value: string; group: string }

function groupFor(name: string, value: string): string {
  if (CSS.supports('color', value) && !/^(inherit|initial|unset|revert|currentcolor)$/i.test(value)) return 'colors'
  if (/font|type-|typography|text-|lineheight|line-height|letterspacing/i.test(name)) return 'typography'
  if (/border|radius|shadow|outline/i.test(name)) return 'borders'
  if (/duration|easing|ease-|motion|transition|animation/i.test(name)) return 'motion'
  if (/size|space|spacing|gap|padding|margin|height|width|breakpoint|viewport|control-|stack-/i.test(name)) return 'spacing'
  return 'other'
}

/** Read authored token names from stylesheets; computed style supplies current theme values. */
function discoverTokenNames(): { names: Set<string>; inaccessible: number } {
  const names = new Set<string>()
  const visited = new Set<CSSStyleSheet>()
  let inaccessible = 0
  const declarations = (style: CSSStyleDeclaration) => {
    for (const name of Array.from(style)) if (name.startsWith('--')) names.add(name)
  }
  const rules = (list: CSSRuleList) => {
    for (const rule of Array.from(list)) {
      if ('style' in rule && rule.style instanceof CSSStyleDeclaration) declarations(rule.style)
      if ('cssRules' in rule) rules((rule as CSSGroupingRule).cssRules)
      if (rule instanceof CSSImportRule && rule.styleSheet) sheet(rule.styleSheet)
    }
  }
  const sheet = (stylesheet: CSSStyleSheet) => {
    if (visited.has(stylesheet)) return
    visited.add(stylesheet)
    try { rules(stylesheet.cssRules) } catch { inaccessible++ }
  }
  for (const stylesheet of [...Array.from(document.styleSheets), ...document.adoptedStyleSheets]) sheet(stylesheet)
  declarations(document.documentElement.style)
  return { names, inaccessible }
}

export class PlaygroundTokens extends LitElement {
  @property() example = 'colors'
  @state() private tokens: Token[] = []
  @state() private search = ''
  @state() private productOnly = true
  @state() private inaccessible = 0
  private names = new Set<string>()
  private observer?: MutationObserver
  private frame = 0

  getExampleCode() {
    const query = this.search.trim().toLowerCase()
    const names = this.tokens.filter(token => token.group === this.example && (!this.productOnly || token.name.startsWith('--lv-')) && (!query || `${token.name} ${token.value}`.toLowerCase().includes(query)))
    return '/* Reference production tokens; import the product stylesheet first. */\n' + names.map(token => `/* ${token.name}: use var(${token.name}) */`).join('\n')
  }

  getExampleState() { return { search: this.search, productOnly: this.productOnly } }

  async restoreExampleState(value: Record<string, unknown>) {
    await this.updateComplete
    Object.assign(this, readState(value, this.getExampleState(), {}))
    
  }

  static styles = [settingsLayoutStyles, css`
    :host { display: block; color: var(--lv-fg-default); font: var(--lv-type-body); min-width: 0; }
    * { box-sizing: border-box; }
    .toolbar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--base-size-12); margin-bottom: var(--base-size-16); }
    .toolbar input[type=search] { flex: 1 1 16rem; }
    .toolbar label { display: inline-flex; align-items: center; gap: var(--base-size-8); }
    .toolbar input[type=checkbox] { accent-color: var(--lv-bg-accent); }
    .note, .count { color: var(--lv-fg-muted); font: var(--lv-type-caption); margin: 0 0 var(--base-size-16); }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 19rem), 1fr)); gap: var(--base-size-12); }
    .token { min-width: 0; overflow: hidden; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); }
    .sample { display: flex; align-items: center; min-height: 5rem; padding: var(--base-size-16); overflow: hidden; background: var(--lv-bg-panel-muted); }
    .swatch { width: 100%; height: 3rem; border: 1px solid var(--lv-line-muted); border-radius: var(--lv-radius-small); }
    .body { padding: var(--base-size-12); display: grid; gap: var(--base-size-8); }
    code { font-family: var(--fontStack-monospace); font-size: .75rem; overflow-wrap: anywhere; }
    .value { color: var(--lv-fg-muted); }
    .aliases { display: grid; gap: var(--base-size-4); }
    .documentation { display: grid; gap: var(--base-size-12); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .bar { height: var(--base-size-16); min-width: 1px; max-width: 100%; background: var(--lv-bg-accent); border-radius: var(--base-size-2); }
    .box { width: 6rem; height: 3rem; background: var(--lv-bg-panel); }
    details { min-width: 0; }
    summary { cursor: pointer; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    :host([preview-only]) .toolbar, :host([preview-only]) .note, :host([preview-only]) .count { display: none; }
  `, exampleChromeStyles]

  connectedCallback() {
    super.connectedCallback()
    document.addEventListener('leapview-theme-applied', this.refresh)
    window.addEventListener('load', this.discover)
    this.observer = new MutationObserver(this.refresh)
    this.observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-color-mode', 'data-light-theme', 'data-dark-theme', 'style'] })
    this.discover()
  }

  disconnectedCallback() {
    document.removeEventListener('leapview-theme-applied', this.refresh)
    window.removeEventListener('load', this.discover)
    this.observer?.disconnect()
    cancelAnimationFrame(this.frame)
    super.disconnectedCallback()
  }

  private discover = () => {
    const found = discoverTokenNames()
    this.names = found.names
    this.inaccessible = found.inaccessible
    this.refresh()
  }

  private refresh = () => {
    cancelAnimationFrame(this.frame)
    this.frame = requestAnimationFrame(() => {
      const computed = getComputedStyle(document.documentElement)
      this.tokens = Array.from(this.names).map((name) => {
        const value = computed.getPropertyValue(name).trim()
        return { name, value, group: groupFor(name, value) }
      }).filter((token) => token.value).sort((a, b) => a.name.localeCompare(b.name))
    })
  }

  private sample(token: Token) {
    const reference = `var(${token.name})`
    if (token.group === 'colors') return html`<div class="swatch" style=${styleMap({ backgroundColor: reference })}></div>`
    if (token.group === 'typography') {
      const style: Record<string, string> = {}
      if (/fontstack|family/i.test(token.name)) style.fontFamily = reference
      else if (/weight/i.test(token.name)) style.fontWeight = reference
      else if (/lineheight|line-height/i.test(token.name)) style.lineHeight = reference
      else if (/letterspacing/i.test(token.name)) style.letterSpacing = reference
      else if (/size/i.test(token.name)) style.fontSize = reference
      else if (CSS.supports('font', token.value)) style.font = reference
      return html`<span style=${styleMap(style)}>The quick brown fox<br>0123456789</span>`
    }
    if (token.group === 'spacing' && CSS.supports('width', token.value)) return html`<div class="bar" style=${styleMap({ width: reference })}></div>`
    if (token.group === 'borders') {
      const style: Record<string, string> = {}
      if (/radius/i.test(token.name)) style.borderRadius = reference
      else if (/shadow/i.test(token.name)) style.boxShadow = reference
      else if (CSS.supports('border', token.value)) style.border = reference
      else if (CSS.supports('border-width', token.value)) { style.borderWidth = reference; style.borderStyle = 'solid' }
      else if (CSS.supports('outline', token.value)) style.outline = reference
      return html`<div class="box" style=${styleMap(style)}></div>`
    }
    return html`<code>${token.value}</code>`
  }

  render() {
    const query = this.search.trim().toLowerCase()
    const selected = this.tokens.filter((token) => token.group === this.example && (!this.productOnly || token.name.startsWith('--lv-')) && (!query || `${token.name} ${token.value}`.toLowerCase().includes(query)))
    const grouped = new Map<string, Token[]>()
    for (const token of selected) {
      const aliases = grouped.get(token.value) ?? []
      aliases.push(token)
      grouped.set(token.value, aliases)
    }
    return html`<div data-fixture-controls class="toolbar"><input class="settings-input" type="search" aria-label="Search token names and values" placeholder="Search tokens…" .value=${this.search} @input=${(event: Event) => { this.search = (event.target as HTMLInputElement).value }}><label><input type="checkbox" .checked=${this.productOnly} @change=${(event: Event) => { this.productOnly = (event.target as HTMLInputElement).checked }}>LeapView tokens only</label><button class="settings-button" @click=${this.discover}>Refresh</button></div>
      <p class="count" role="status">${selected.length} tokens · ${grouped.size} distinct values${this.inaccessible ? ` · ${this.inaccessible} stylesheet(s) inaccessible to the browser` : ''}</p>
      <div class="grid" part="preview">${Array.from(grouped.values()).map((aliases) => html`<article class="token"><div class="sample" aria-hidden="true">${this.sample(aliases[0])}</div><div class="body"><code>${aliases[0].name}</code><code class="value">${aliases[0].value}</code>${aliases.length > 1 ? html`<details><summary>${aliases.length - 1} equivalent token${aliases.length === 2 ? '' : 's'}</summary><div class="aliases">${aliases.slice(1).map((token) => html`<code>${token.name}</code>`)}</div></details>` : nothing}</div></article>`)}</div>
      ${selected.length ? nothing : html`<p class="note">No matching tokens.</p>`}
      ${exampleDetails(html`<div class="documentation">
        <p>Live values from the production stylesheets. Matching values share a card; themes update the samples.</p>
        <p><strong>Source:</strong> <code>static/app.input.css</code> and its imported tokens. Theme: <code>static/theme.js</code>.</p>
        <p>Search names or values. Turn off “LeapView tokens only” to include Primer tokens. Refresh rescans loaded stylesheets.</p>
      </div>`)}`
  }
}

customElements.define('playground-tokens', PlaygroundTokens)
