import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import type { SemanticModelGraphSignal } from '../web/generated/signals'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import '../web/components/shared/asset-lineage-graph'
import '../web/components/shared/semantic-model-graph'
import { lineageFixture, semanticFixture, type LineageFixture, type LineageScenario, type SemanticScenario } from './graph-fixtures'

export const graphExamples = [
  { id: 'asset-lineage', label: 'Asset lineage graph' },
  { id: 'semantic-model', label: 'Semantic model graph' },
]

export class PlaygroundGraphs extends LitElement {
  @property() example = 'asset-lineage'
  @state() private scenario: LineageScenario | SemanticScenario = 'dependencies'
  @state() private scope: 'focused' | 'full' = 'focused'
  @state() private animateRun = false
  @state() private lineage: LineageFixture = lineageFixture('dependencies')
  @state() private semantic: SemanticModelGraphSignal = semanticFixture('sales')
  @state() private events: Array<{ name: string; detail: unknown }> = []
  @state() private lastSelected = ''
  @state() private instance = 0

  static styles = [settingsLayoutStyles, css`
    :host { display: block; min-width: 0; color: var(--lv-fg-default); font: var(--lv-type-body); }
    * { box-sizing: border-box; }
    .controls { display: flex; flex-wrap: wrap; align-items: end; gap: var(--base-size-12); margin-bottom: var(--base-size-16); }
    label { display: grid; gap: var(--base-size-4); font: var(--lv-type-caption); }
    label.check { display: flex; align-items: center; min-height: var(--control-medium-size); gap: var(--base-size-8); }
    input[type=checkbox] { accent-color: var(--lv-bg-accent); }
    .preview { height: var(--playground-preview-height, 420px); min-width: 0; position: relative; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); overflow: hidden; }
    lv-asset-lineage-graph, lv-semantic-model-graph { display: block; width: 100%; height: 100%; min-width: 0; }
    .empty { position: absolute; inset: 5rem var(--base-size-16) auto; pointer-events: none; text-align: center; color: var(--lv-fg-muted); font: var(--lv-type-body); }
    .documentation { display: grid; gap: var(--base-size-16); margin-top: var(--base-size-16); font: var(--lv-type-body-compact); }
    .documentation p { margin: 0; color: var(--lv-fg-muted); }
    dl { display: grid; gap: var(--base-size-12); margin: 0; }
    dt { font-weight: var(--base-text-weight-semibold); margin-bottom: var(--base-size-4); }
    dd { margin: 0; color: var(--lv-fg-muted); overflow-wrap: anywhere; }
    code { font-family: var(--fontStack-monospace); font-size: .8rem; overflow-wrap: anywhere; }
    details { min-width: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); padding: var(--base-size-12); }
    summary { cursor: pointer; font-weight: var(--base-text-weight-semibold); }
    pre { white-space: pre-wrap; overflow-wrap: anywhere; overflow: auto; max-height: 20rem; margin: var(--base-size-12) 0 0; padding: var(--base-size-12); background: var(--lv-bg-panel-muted); font: var(--lv-type-mono); }
    .log-header { display: flex; justify-content: space-between; align-items: center; gap: var(--base-size-12); }
    h3 { margin: 0; font: var(--lv-type-section-title); }
    :host([preview-only]) .controls, :host([preview-only]) .documentation { display: none; }
  `]

  protected willUpdate(changed: Map<PropertyKey, unknown>) {
    if (changed.has('example')) {
      this.scenario = this.example === 'semantic-model' ? 'sales' : 'dependencies'
      this.scope = 'focused'
      this.animateRun = false
      this.reset()
    }
  }

  private reset() {
    this.lineage = lineageFixture(this.example === 'asset-lineage' ? this.scenario as LineageScenario : 'dependencies', this.animateRun)
    this.semantic = semanticFixture(this.example === 'semantic-model' ? this.scenario as SemanticScenario : 'sales')
    this.events = []
    this.lastSelected = ''
    this.instance++
  }

  private changeScenario = (event: Event) => {
    this.scenario = (event.target as HTMLSelectElement).value as LineageScenario | SemanticScenario
    this.scope = this.scenario === 'wide' || this.scenario === 'run' ? 'full' : 'focused'
    this.reset()
  }

  private record = (event: CustomEvent<{ id?: string; scope?: 'focused' | 'full' }>) => {
    if (event.type === 'lv-lineage-select') this.lastSelected = event.detail.id ?? ''
    if (event.type === 'lv-lineage-scope-change' && event.detail.scope) this.scope = event.detail.scope
    this.events = [{ name: event.type, detail: event.detail }, ...this.events].slice(0, 16)
  }

  render() {
    const isLineage = this.example === 'asset-lineage'
    const variants = isLineage
      ? [['dependencies', 'Dependencies'], ['run', 'Pipeline run states'], ['wide', 'Many upstream models'], ['empty', 'Empty graph']]
      : [['sales', 'Sales relationships'], ['composite', 'Composite key · one to one'], ['disconnected', 'Disconnected dataset'], ['empty', 'Empty graph']]
    const fixture = isLineage ? this.lineage : this.semantic
    return html`
      <div class="controls">
        <label>Scenario<select class="settings-input" aria-label="Graph scenario" .value=${this.scenario} @change=${this.changeScenario}>${variants.map(([value, label]) => html`<option value=${value}>${label}</option>`)}</select></label>
        ${isLineage ? html`<label>Scope<select class="settings-input" aria-label="Graph scope" .value=${this.scope} @change=${(event: Event) => { this.scope = (event.target as HTMLSelectElement).value as 'focused' | 'full' }}><option value="focused">Focused path</option><option value="full">Full graph</option></select></label>` : nothing}
        ${isLineage && this.scenario === 'run' ? html`<label class="check"><input type="checkbox" .checked=${this.animateRun} @change=${(event: Event) => { this.animateRun = (event.target as HTMLInputElement).checked; this.lineage = lineageFixture('run', this.animateRun) }}>Animate running node</label>` : nothing}
        <button class="settings-button" @click=${this.reset}>Reload fixture</button>
      </div>
      <section class="preview" part="preview" aria-label=${isLineage ? 'Asset lineage preview' : 'Semantic model preview'}>
        ${keyed(`${this.example}:${this.instance}`, isLineage ? html`
          <lv-asset-lineage-graph .graph=${this.lineage} .scope=${this.scope} .scopeMode=${this.scenario === 'run' ? 'run' : 'dependencies'} .dialogTitle=${this.scenario === 'run' ? 'Sales pipeline run' : 'Sales asset dependencies'} @lv-lineage-select=${this.record} @lv-lineage-scope-change=${this.record}></lv-asset-lineage-graph>
        ` : html`<lv-semantic-model-graph .graph=${this.semantic} .storageKey=${`playground:semantic-model:${this.scenario}`}></lv-semantic-model-graph>`)}
        ${this.scenario === 'empty' ? html`<p class="empty" role="status">No ${isLineage ? 'assets' : 'datasets'} in this fixture. Choose another scenario to explore a connected graph.</p>` : nothing}
      </section>
      <section class="documentation">
        <p>${isLineage ? 'Select an asset to highlight its path. Switch scope, pan, zoom, or fit the included nodes. Expand graph opens the production dialog; Escape closes it.' : 'Select a dataset or relationship to inspect its connections. Use Related / All to reveal fields, drag datasets to arrange them, and Reset layout to restore the automatic layout.'}</p>
        <dl>
          <div><dt>Component</dt><dd><code>${isLineage ? '<lv-asset-lineage-graph>' : '<lv-semantic-model-graph>'}</code></dd></div>
          <div><dt>Source</dt><dd><code>${isLineage ? 'web/components/shared/asset-lineage-graph.ts' : 'web/components/shared/semantic-model-graph.ts'}</code></dd></div>
          <div><dt>Properties</dt><dd><code>${isLineage ? 'graph { nodes, edges } · scope: focused | full · scopeMode: dependencies | run · dialogTitle' : 'graph { datasets, nodes, edges } · storageKey'}</code></dd></div>
          <div><dt>Events & state</dt><dd>${isLineage ? html`<code>lv-lineage-select { id } · lv-lineage-scope-change { scope }</code>. Selection and expansion are owned by the component; this preview records selection and synchronizes the scope control. Expansion has no public custom event.` : 'No public custom events. The production component owns node and edge selection, field visibility, and layout changes. Drag positions persist in browser storage under a playground-specific key.'}</dd></div>
        </dl>
        <details><summary>Fixture · ${fixture.nodes.length} nodes, ${fixture.edges.length} edges</summary><pre>${JSON.stringify(fixture, null, 2)}</pre></details>
        ${isLineage ? html`<div class="log-header"><h3>Event log</h3><button class="settings-button" ?disabled=${this.events.length === 0} @click=${() => { this.events = [] }}>Clear log</button></div><p>Last selected asset: <code>${this.lastSelected || 'No selection event yet'}</code></p><div aria-live="polite">${this.events.length ? this.events.map((event) => html`<pre>${event.name} ${JSON.stringify(event.detail)}</pre>`) : html`<p>Interact with the graph to inspect emitted events.</p>`}</div>` : nothing}
      </section>
    `
  }
}

customElements.define('playground-graphs', PlaygroundGraphs)
