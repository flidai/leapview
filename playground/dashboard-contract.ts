import { LitElement, css, html } from 'lit'
import { state } from 'lit/decorators.js'
import { parse } from 'yaml'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import '../web/components/shared/code-editor'
import { exampleChromeStyles, exampleDetails } from './example-chrome'
import { readState, stateRecord } from './example-state'
import { compilerEvidenceMetadata, dashboardScenario, dashboardScenarios, inspectDashboard, matchingCompilerEvidence, type RecordedCompilerEvidence } from './dashboard-contract-fixtures'

const sourceLimit = 20000
const defaults = { version: 'after', scenario: 'corrected-monthly', source: dashboardScenario('corrected-monthly').source }
const choices = { version: ['before', 'after'], scenario: dashboardScenarios.map(fixture => fixture.id) }
function display(value: unknown): string {
  if (value == null) return '—'
  try { return typeof value === 'object' ? JSON.stringify(value) : String(value) }
  catch { return 'Unsupported cyclic YAML value' }
}

export class PlaygroundDashboardContract extends LitElement {
  @state() private version = defaults.version
  @state() private scenario = defaults.scenario
  @state() private source = defaults.source
  @state() private inputNotice = ''
  @state() private referencesOpen = false
  private validationSource = ''
  private readonly validationCache = new Map<boolean, ReturnType<typeof inspectDashboard>>()

  getExampleState() {
    return { version: this.version, scenario: this.scenario, source: this.source }
  }

  async restoreExampleState(value: Record<string, unknown>) {
    await this.updateComplete
    // Keep existing local/shared snapshots usable after separating source and schema controls.
    const migrated = { ...value }
    if (migrated.scenario === 'valid') migrated.scenario = migrated.version === 'before' ? 'original-guide' : 'corrected-monthly'
    if (migrated.scenario === 'invalid-placement') migrated.scenario = 'zero-span'
    const controls = readState(migrated, defaults, choices)
    this.version = controls.version
    this.scenario = controls.scenario
    this.source = typeof value.source === 'string' && value.source.length <= sourceLimit ? controls.source : this.scenarioSource()
    this.inputNotice = ''
    await this.updateComplete
  }

  /** The review panel exports the current authored document, never inferred runtime code. */
  getExampleCode() { return this.source }

  static styles = [settingsLayoutStyles, css`
    :host { display: block; min-width: 0; container-type: inline-size; color: var(--lv-fg-default); font: var(--lv-type-body); }
    * { box-sizing: border-box; }
    .stack { display: grid; gap: var(--base-size-16); }
    .controls label { min-width: 0; max-width: 100%; display: grid; gap: var(--base-size-4); font: var(--lv-type-caption); }
    .controls select { max-width: 100%; }
    .intro { display: grid; gap: var(--base-size-8); }
    h2 { margin: 0; font: var(--lv-type-section-title); }
    h3 { margin: 0; font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); }
    p { margin: 0; }
    .muted { color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .workspace { display: grid; grid-template-columns: minmax(0, 1.3fr) minmax(0, 1fr); align-items: start; gap: var(--base-size-16); }
    .source-pane, .review-pane { display: grid; min-width: 0; gap: var(--base-size-12); }
    .pane-heading { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: var(--base-size-8); }
    .badge { display: inline-flex; padding: var(--base-size-4) var(--base-size-8); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    lv-code-editor { display: block; min-width: 0; }
    .card { min-width: 0; display: grid; gap: var(--base-size-12); padding: var(--base-size-16); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); }
    .selected { border-color: var(--lv-fg-accent); }
    .schema-results { display: grid; gap: var(--base-size-12); }
    .accepted { color: var(--lv-fg-success); }
    .rejected { color: var(--lv-fg-danger); }
    .status { font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); }
    .issues { display: grid; gap: var(--base-size-8); margin: 0; padding-left: var(--base-size-20); font: var(--lv-type-caption); }
    .issues li { overflow-wrap: anywhere; }
    .more-issues > summary { cursor: pointer; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .more-issues[open] > summary { margin-bottom: var(--base-size-12); }
    dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: var(--base-size-8) var(--base-size-12); margin: 0; font: var(--lv-type-caption); }
    dt { color: var(--lv-fg-muted); }
    dd { margin: 0; overflow-wrap: anywhere; }
    .query-summary h3 { margin-bottom: var(--base-size-12); }
    .query-summary { padding-top: var(--base-size-12); border-top: var(--lv-border-muted); }
    code { font: var(--lv-type-mono); overflow-wrap: anywhere; }
    pre { max-height: 22rem; overflow: auto; margin: var(--base-size-12) 0 0; padding: var(--base-size-12); background: var(--lv-bg-panel-muted); font: var(--lv-type-mono); white-space: pre-wrap; overflow-wrap: anywhere; }
    .more-issues dl { margin-top: var(--base-size-12); }
    summary { cursor: pointer; font: var(--lv-type-body-compact); }
    .comparison > section { margin-top: var(--base-size-12); }
    .references[open] > summary { margin-bottom: var(--base-size-12); }
    .intro > .badge { justify-self: start; }
    .documentation { display: grid; gap: var(--base-size-12); color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    @container (max-width: 850px) { .workspace { grid-template-columns: minmax(0, 1fr); } }
    @container (max-width: 420px) { .card { padding: var(--base-size-12); } dl { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-4); } dd { margin-bottom: var(--base-size-8); } }
  `, exampleChromeStyles]

  private schemaResult(baseline: boolean) {
    if (this.validationSource !== this.source) {
      this.validationSource = this.source
      this.validationCache.clear()
    }
    let result = this.validationCache.get(baseline)
    if (!result) {
      result = inspectDashboard(this.source, baseline)
      this.validationCache.set(baseline, result)
    }
    return result
  }

  private scenarioSource() { return dashboardScenario(this.scenario).source }

  private resetSource() {
    this.source = this.scenarioSource()
    this.inputNotice = ''
  }

  private editSource(event: CustomEvent<{ value: string }>) {
    const source = event.detail.value
    if (source.length > sourceLimit) {
      this.source = source.slice(0, sourceLimit)
      // The parent binding may have the same capped value as its last render.
      // Always synchronize the editor's public input after a rejected edit.
      const editor = event.currentTarget as (HTMLElement & { value: string }) | null
      if (editor) editor.value = this.source
      this.inputNotice = `YAML was limited to ${sourceLimit.toLocaleString()} characters for this local example.`
      return
    }
    this.source = source
    this.inputNotice = ''
  }

  private schemaCard(baseline: boolean) {
    const result = this.schemaResult(baseline)
    const selected = baseline === (this.version === 'before')
    const shown = 3
    return html`<section class=${selected ? 'card selected' : 'card'} aria-label=${baseline ? 'Baseline schema validation' : 'Tightened schema validation'}>
      <div class="pane-heading"><h3>${baseline ? 'Baseline schema' : 'Tightened schema'}</h3>${selected ? html`<span class="badge">Selected contract</span>` : ''}</div>
      <p class=${result.valid ? 'status accepted' : 'status rejected'}>${result.valid ? 'Accepted' : 'Rejected'}</p>
      ${result.issues.length ? html`
        <ul class="issues">${result.issues.slice(0, shown).map(issue => html`<li><code>${issue.path || '/'}</code> — ${issue.message}</li>`)}</ul>
        ${result.issues.length > shown ? html`<details class="more-issues"><summary>${result.issues.length - shown} additional schema diagnostics</summary><ul class="issues">${result.issues.slice(shown).map(issue => html`<li><code>${issue.path || '/'}</code> — ${issue.message}</li>`)}</ul></details>` : ''}
      ` : html`<p class="muted">The YAML shape follows these rules.</p>`}
    </section>`
  }

  private compilerIntent(check: RecordedCompilerEvidence) {
    const intent = stateRecord(check.resolvedIntent)
    const visuals = Array.isArray(intent.visuals) ? intent.visuals.map(stateRecord) : []
    const visual = visuals.find(visual => visual.id === 'revenue-by-month') ?? visuals[0]
    if (!visual) return ''
    const query = stateRecord(visual.query)
    const aggregate = stateRecord(query.aggregate)
    const dimensions = Array.isArray(aggregate.dimensions) ? aggregate.dimensions.map(stateRecord) : []
    const metrics = Array.isArray(aggregate.metrics) ? aggregate.metrics.map(stateRecord) : []
    const sort = Array.isArray(aggregate.sort) ? aggregate.sort.map(stateRecord) : []
    const page = Array.isArray(intent.pages) ? stateRecord(intent.pages[0]) : {}
    const grid = stateRecord(page.grid)
    return html`<div class="query-summary"><dl>
      <dt>Visual</dt><dd><code>${display(visual.id)}</code> · ${display(visual.mark)}</dd>
      <dt>Grouping</dt><dd>${dimensions.map(dimension => `${display(dimension.fieldID)}${dimension.grain ? ` by ${display(dimension.grain)}` : ''}${dimension.alias ? ` as ${display(dimension.alias)}` : ''}`).join(', ') || 'No grouping (total)'}</dd>
      <dt>Metrics</dt><dd>${metrics.map(metric => display(metric.fieldID)).join(', ') || '—'}</dd>
      <dt>Sort</dt><dd>${sort.map(order => `${display(order.fieldID)} ${order.direction === 'asc' ? 'ascending' : order.direction === 'desc' ? 'descending' : display(order.direction)}`).join(', ') || '—'}</dd>
      <dt>Row limit</dt><dd>${display(aggregate.limit)}</dd>
      <dt>Grid columns</dt><dd>${display(grid.columns)}</dd>
      <dt>Reading order</dt><dd>${Array.isArray(page.readingOrder) ? page.readingOrder.map(display).join(', ') : '—'}</dd>
    </dl></div>`
  }

  private compilerCheck() {
    const check = matchingCompilerEvidence(this.source, this.scenario)
    return html`<section class="card" aria-label="LeapView compiler check" aria-live="polite">
      <div class="pane-heading"><h3>LeapView compiler check</h3><span class="badge">Recorded local run</span></div>
      <p class="muted">Checks model fields, query meaning and page layout. This result comes from a real offline compiler run, not from the browser.</p>
      ${check ? html`
        <p class=${check.valid ? 'status accepted' : 'status rejected'}>${check.valid ? 'Accepted' : 'Rejected'}</p>
        <p class="muted">Exact YAML match · fixed local project files.</p>
        ${check.issues.length ? html`<ul class="issues">${check.issues.slice(0, 3).map(issue => html`<li>${issue.fieldPath ? html`<code>${issue.fieldPath}</code> — ` : ''}${issue.message}</li>`)}</ul>
          ${check.issues.length > 3 ? html`<details class="more-issues"><summary>${check.issues.length - 3} additional compiler diagnostics</summary><ul class="issues">${check.issues.slice(3).map(issue => html`<li>${issue.fieldPath ? html`<code>${issue.fieldPath}</code> — ` : ''}${issue.message}</li>`)}</ul></details>` : ''}
        ` : html`<p class="muted">The selected dashboard compiles against the recorded project.</p>`}
        ${check.resolvedIntent ? html`<details class="more-issues"><summary>Verified query intent</summary>${this.compilerIntent(check)}</details><details class="more-issues"><summary>Full verified query and layout</summary><pre tabindex="0" role="region" aria-label="Verified query and layout JSON">${JSON.stringify(check.resolvedIntent, null, 2)}</pre></details>` : ''}
        <details class="more-issues"><summary>Evidence provenance</summary><dl>
          <dt>YAML digest</dt><dd><code>${check.sourceDigest}</code></dd>
          <dt>All project files</dt><dd><code>${check.sourceRootDigest}</code></dd>
          <dt>Compiler revision</dt><dd><code>${compilerEvidenceMetadata.compilerCommit}</code></dd>
          <dt>Compiler fingerprint</dt><dd><code>${compilerEvidenceMetadata.compilerFingerprint}</code></dd>
          <dt>Compiler source files</dt><dd><code>${compilerEvidenceMetadata.compilerSourceDigest}</code></dd>
          <dt>Generated schema</dt><dd><code>${compilerEvidenceMetadata.schemaDigest}</code></dd>
        </dl><p class="muted">The project digest includes the dashboard, fragments and every supporting resource. The compiler fingerprint includes local source changes and build settings.</p></details>
      ` : html`<p role="status" class="muted">Compiler result unavailable for edited YAML</p>
        <p class="muted">The YAML shape check stays live. Reset YAML to restore this scenario's recorded compiler result.</p>`}
    </section>`
  }

  private documentSummary() {
    let document: Record<string, unknown>
    try { document = stateRecord(parse(this.source)) } catch { return html`<p class="muted">Fix the YAML syntax to inspect document references.</p>` }
    const metadata = stateRecord(document.metadata)
    const spec = stateRecord(document.spec)
    const layout = stateRecord(spec.layout)
    const visuals = Array.isArray(spec.visuals) ? spec.visuals.map(stateRecord) : []
    const pages = Array.isArray(spec.pages) ? spec.pages.map(stateRecord) : []
    return html`<dl>
      <dt>Dashboard ID</dt><dd><code>${display(metadata.id)}</code></dd>
      <dt>Semantic model</dt><dd><code>${display(spec.semanticModel)}</code></dd>
      <dt>Grid columns</dt><dd>${layout.columns == null ? 'Inherited default' : display(layout.columns)}</dd>
      <dt>Visual IDs</dt><dd><code>${visuals.map(visual => display(visual.id)).join(', ') || '—'}</code></dd>
    </dl>
    ${visuals.map(visual => {
      const query = stateRecord(visual.query)
      return html`<dl class="query-summary"><dt>Query · ${display(visual.id)}</dt><dd><code>${display(query.type)}</code></dd>
        <dt>Dimensions</dt><dd><code>${display(query.dimensions)}</code></dd>
        <dt>Metrics</dt><dd><code>${display(query.metrics)}</code></dd>
      </dl>`
    })}
    ${pages.map(page => html`<dl class="query-summary"><dt>Page ID</dt><dd><code>${display(page.id)}</code></dd>
      ${(Array.isArray(page.components) ? page.components.map(stateRecord) : []).map(component => html`<dt>${display(component.id)}</dt><dd>visual <code>${display(component.visual)}</code><br>placement <code>${display(component.placement)}</code></dd>`)}
    </dl>`)}`
  }

  render() {
    return html`<div class="stack">
      <div data-fixture-controls class="controls" aria-label="Dashboard contract fixture controls">
        <label>Document scenario<select class="settings-input" aria-label="Document scenario" .value=${this.scenario} @change=${(event: Event) => { this.scenario = (event.target as HTMLSelectElement).value; this.resetSource() }}>
          ${dashboardScenarios.map(fixture => html`<option value=${fixture.id} ?selected=${fixture.id === this.scenario}>${fixture.label}</option>`)}
        </select></label>
        <label>Schema version<select class="settings-input" aria-label="Schema version" .value=${this.version} @change=${(event: Event) => { this.version = (event.target as HTMLSelectElement).value }}>
          <option value="before">Before · baseline rules</option><option value="after">After · tightened rules</option>
        </select></label>
        <button class="settings-button" @click=${this.resetSource}>Reset YAML</button>
      </div>
      <section part="preview" class="stack">
        <div class="intro">
          <span class="badge">leapview.dev/v1</span>
          <p class="muted">Edit the YAML and check its structure and recorded compiler result. Switching schema version keeps your edits.</p>
          <p class="muted">Compiler results apply to exact examples and their supporting project files. Edited YAML needs a new compiler run.</p>
        </div>
        <div class="workspace">
          <div class="source-pane">
            <div class="pane-heading"><h3>Editable dashboard YAML</h3><span class="badge">${dashboardScenario(this.scenario).label}</span></div>
            <lv-code-editor .value=${this.source} language="yaml" aria-label="Dashboard YAML source" @lv-code-editor-change=${this.editSource}></lv-code-editor>
            ${this.inputNotice ? html`<p role="status" class="muted">${this.inputNotice}</p>` : ''}
            <details class="card references" @toggle=${(event: Event) => { this.referencesOpen = (event.target as HTMLDetailsElement).open }}><summary>Document references</summary>${this.referencesOpen ? this.documentSummary() : ''}</details>
          </div>
          <div class="review-pane">
            <h3>YAML shape check</h3>
            <p class="muted">Live checks for required fields, allowed values and numeric bounds. An accepted shape does not prove that model fields or page layout are correct.</p>
            <div class="schema-results" aria-live="polite">${this.schemaCard(this.version === 'before')}<details class="comparison"><summary>Compare with other schema</summary>${this.schemaCard(this.version !== 'before')}</details></div>
            ${this.compilerCheck()}
          </div>
        </div>
      </section>
      ${exampleDetails(html`<div class="documentation">
        <p>The production code editor updates local YAML and the shape check. The browser never runs the compiler or a query.</p>
        <p>Validation runs against the tightened generated Dashboard JSON Schema. The baseline snapshot is reconstructed by removing the twelve added scalar minima. Referenced semantic models, grain compatibility, duplicate IDs, layout overlap and governed queries require the project compiler.</p>
        <p>Schema version, document scenario and bounded edited YAML survive Copy link, pinned comparison and local reload. Copy component code exports the current YAML.</p>
      </div>`)}
    </div>`
  }
}

customElements.define('playground-dashboard-contract', PlaygroundDashboardContract)
