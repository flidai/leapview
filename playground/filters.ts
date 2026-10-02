import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import type { DashboardFilterExpression, DashboardFilterPresentation } from '../web/generated/signals'
import '../web/components/dashboard/filters/filter-control'
import '../web/components/dashboard/filters/filter-dock'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import { filterFixture, filterStyles } from './filter-fixtures'

export const filterExamples = [
  { id: 'leaf', label: 'Filter control' }, { id: 'pane', label: 'Filter pane card' }, { id: 'slicer', label: 'Dashboard slicer' }, { id: 'dock', label: 'Filter dock' },
]
export class PlaygroundFilters extends LitElement {
  @property() example = 'leaf'
  @state() private presentationStyle: DashboardFilterPresentation['style'] = 'dropdown'
  @state() private expression: DashboardFilterExpression = { kind: 'unfiltered' }
  @state() private disabled = false
  @state() private empty = false
  @state() private multiple = false
  @state() private pending = false
  @state() private stale = false
  @state() private log = 'Interact with the filter to inspect its expression.'
  static styles = [settingsLayoutStyles, css`
    :host { display:block; min-width:0; } .stack { display:grid; gap:var(--base-size-24); }
    .controls { display:flex; flex-wrap:wrap; gap:var(--base-size-12); align-items:center; }
    .preview { padding:var(--base-size-24); border:var(--lv-border-muted); border-radius:var(--lv-radius-large); background:var(--lv-bg-panel); min-height:15rem; }
    .preview > * { display:block; max-width:38rem; } pre, code { white-space:pre-wrap; overflow-wrap:anywhere; }
    :host([preview-only]) .controls, :host([preview-only]) .documentation { display:none; }
  `]
  private mutate(event: CustomEvent<{ expression: DashboardFilterExpression }>) {
    this.expression = event.detail.expression
    this.log = `${event.type}: ${JSON.stringify(event.detail, null, 2)}`
  }
  render() {
    const { definition, binding, presentation } = filterFixture(this.presentationStyle, this.disabled, this.empty, this.multiple)
    const reset = (event: Event) => { this.expression = event.type === 'lv-filter-clear' ? { kind: 'unfiltered' } : binding.default; this.log = event.type }
    const preview = this.example === 'dock' ? html`<lv-filter-dock pageId="playground" .contract=${{ applicationMode: 'immediate', definitions: { [definition.id]: definition }, bindings: { [binding.key]: binding } }} .filterState=${{ revision: 0, appliedControls: { [binding.key]: { expression: this.expression, resolvedExpression: this.expression } }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'fixture' }} .pending=${this.pending} .pendingBindingKeys=${this.pending ? [binding.key] : []} @lv-filter-dock-state=${(event: CustomEvent) => { this.log = `${event.type}: ${JSON.stringify(event.detail)}` }} @lv-filter-mutate=${this.mutate} @lv-filter-clear=${reset} @lv-filter-reset-binding=${reset} @lv-filter-reset-scope=${reset}></lv-filter-dock>` : this.example === 'pane' ? html`<lv-filter-pane-card .definition=${definition} .binding=${binding} .presentation=${presentation} .expression=${this.expression} .pending=${this.pending} .stale=${this.stale} .active=${this.expression.kind !== 'unfiltered'} .dirty=${this.pending} @lv-filter-mutate=${this.mutate} @lv-filter-clear=${reset} @lv-filter-reset-binding=${reset}></lv-filter-pane-card>` : this.example === 'slicer' ? html`<lv-slicer .definition=${definition} .binding=${binding} .presentation=${presentation} .expression=${this.expression} .pending=${this.pending} .stale=${this.stale} auto-height @lv-filter-mutate=${this.mutate}></lv-slicer>` : html`<lv-filter-leaf .definition=${definition} .binding=${binding} .presentation=${presentation} .expression=${this.expression} .pending=${this.pending} .stale=${this.stale} .showClearAction=${true} .autoHeight=${true} @lv-filter-mutate=${this.mutate}></lv-filter-leaf>`
    return html`<div class="stack"><div class="controls">
      <label>Presentation <select class="settings-input" aria-label="Filter presentation" .value=${this.presentationStyle} @change=${(event: Event) => { this.presentationStyle = (event.target as HTMLSelectElement).value as typeof this.presentationStyle; this.expression = { kind: 'unfiltered' } }}>${filterStyles.filter(style => this.example !== 'dock' || !['list', 'buttons'].includes(style)).map(style => html`<option value=${style}>${style}</option>`)}</select></label>
      ${(['disabled', 'empty', 'multiple', 'pending', 'stale'] as const).filter(key => this.example !== 'dock' || key !== 'stale').map(key => html`<label><input type="checkbox" .checked=${this[key]} @change=${(event: Event) => { this[key] = (event.target as HTMLInputElement).checked }}>${key}</label>`)}
      <button class="settings-button" @click=${() => { this.expression = { kind: 'unfiltered' } }}>Clear expression</button>
    </div><section class="preview" part="preview">${keyed(this.presentationStyle, preview)}</section>
    <section class="documentation"><h2>${filterExamples.find(item => item.id === this.example)?.label}</h2>
      <p><code>${this.example === 'dock' ? 'lv-filter-dock' : this.example === 'pane' ? 'lv-filter-pane-card' : this.example === 'slicer' ? 'lv-slicer' : 'lv-filter-leaf'}</code> · Source: <code>web/components/dashboard/filters/${this.example === 'dock' ? 'filter-dock' : 'filter-control'}.ts</code></p>
      <p>Inputs: definition, binding, expression, presentation, pending, stale; slicer autoHeight. Events: lv-filter-mutate { bindingKey, expression }; pane also lv-filter-clear and lv-filter-reset-binding. Dock inputs: contract, filterState, pageId; events also lv-filter-reset-scope and lv-filter-dock-state. The dock uses immediate application with local state.</p>
      <p>Seven production presentations. Static options preserve the generated signal contract without remote option requests. Selection and range commits update local state. Enter a minimum above the maximum to inspect validation; use Enter or move focus outside to commit. Pending is a display state; stale and readerEditable govern editability.</p>
      <pre aria-live="polite">${this.log}</pre><pre>${JSON.stringify(this.expression, null, 2)}</pre>
    </section></div>`
  }
}
customElements.define('playground-filters', PlaygroundFilters)
