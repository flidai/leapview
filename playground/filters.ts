import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import { exampleDetails, exampleChromeStyles } from './example-chrome'
import type { DashboardFilterContract, DashboardFilterExpression, DashboardFilterPresentation, DashboardFilterState } from '../web/generated/signals'
import { DashboardFilterController } from '../web/components/dashboard/filters/filter-controller'
import '../web/components/dashboard/filters/filter-control'
import '../web/components/dashboard/filters/filter-dock'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import { filterDockFixture, filterFixture, filterStyles, restoreFilterExpression, type FilterScopes } from './filter-fixtures'

function initialDockState(): DashboardFilterState {
  return {
    revision: 0, defaultsRevision: 'fixture', dirtyBindings: [], draftControls: {},
    appliedControls: Object.fromEntries(['preview-filter', 'report-filter'].map(key => [key, { expression: { kind: 'unfiltered' as const }, resolvedExpression: { kind: 'unfiltered' as const } }])),
  }
}

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
  @state() private log = ''
  @state() private applicationMode: DashboardFilterContract['applicationMode'] = 'immediate'
  @state() private scopes: FilterScopes = 'both'
  @state() private dockState = initialDockState()
  static styles = [settingsLayoutStyles, css`
    :host { display:block; min-width:0; } .stack { display:grid; gap:var(--base-size-24); }
    .controls { display:flex; flex-wrap:wrap; gap:var(--base-size-12); align-items:center; }
    .preview { padding:var(--base-size-24); border:var(--lv-border-muted); border-radius:var(--lv-radius-large); background:var(--lv-bg-panel); min-height:15rem; }
    .preview > * { display:block; max-width:38rem; } pre, code { white-space:pre-wrap; overflow-wrap:anywhere; }
    .feedback { display:grid; gap:var(--base-size-8); margin-top:var(--base-size-16); font:var(--lv-type-body-compact); }
    .feedback p { margin:0; } .feedback dl { display:grid; gap:var(--base-size-8); margin:0; } .feedback dd { margin:0; color:var(--lv-fg-muted); overflow-wrap:anywhere; }
    :host([preview-only]) .controls, :host([preview-only]) .documentation { display:none; }
  `, exampleChromeStyles]
  private get contract(): DashboardFilterContract {
    return filterDockFixture(this.presentationStyle, this.applicationMode, this.scopes, this.disabled, this.empty, this.multiple)
  }

  getExampleState(): Record<string, unknown> {
    return structuredClone({ presentationStyle: this.presentationStyle, expression: this.expression, disabled: this.disabled, empty: this.empty, multiple: this.multiple, pending: this.pending, stale: this.stale, applicationMode: this.applicationMode, scopes: this.scopes, applied: Object.fromEntries(Object.entries(this.dockState.appliedControls).map(([key, value]) => [key, value.expression])), drafts: this.dockState.draftControls })
  }

  restoreExampleState(value: Record<string, unknown>): void {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return
    const styles = filterStyles.filter(style => this.example !== 'dock' || !['list', 'buttons'].includes(style))
    this.presentationStyle = styles.includes(value.presentationStyle as DashboardFilterPresentation['style']) ? value.presentationStyle as DashboardFilterPresentation['style'] : 'dropdown'
    for (const key of ['disabled', 'empty', 'multiple', 'pending', 'stale'] as const) this[key] = value[key] === true
    this.applicationMode = value.applicationMode === 'deferred' ? 'deferred' : 'immediate'
    this.scopes = value.scopes === 'page' || value.scopes === 'report' ? value.scopes : 'both'
    this.expression = restoreFilterExpression(value.expression, this.presentationStyle, this.multiple) ?? { kind: 'unfiltered' }
    const next = initialDockState()
    for (const key of Object.keys(next.appliedControls)) {
      const applied = value.applied && typeof value.applied === 'object' ? restoreFilterExpression((value.applied as Record<string, unknown>)[key], this.presentationStyle, this.multiple) : undefined
      if (applied) next.appliedControls[key] = { expression: applied, resolvedExpression: applied }
      const draft = value.drafts && typeof value.drafts === 'object' ? restoreFilterExpression((value.drafts as Record<string, unknown>)[key], this.presentationStyle, this.multiple) : undefined
      if (draft && this.applicationMode === 'deferred') { next.draftControls[key] = draft; next.dirtyBindings.push(key) }
    }
    this.dockState = next
    this.log = ''
  }

  getExampleCode(): string {
    if (this.example === 'dock') return `import { html } from 'lit'\nimport '../web/components/dashboard/filters/filter-dock'\nimport type { DashboardFilterContract, DashboardFilterState } from '../web/generated/signals'\n\nconst contract: DashboardFilterContract = ${JSON.stringify(this.contract, null, 2)}\nconst filterState: DashboardFilterState = ${JSON.stringify(this.dockState, null, 2)}\n\n// Handle lv-filter-mutate, lv-filter-clear, lv-filter-reset-binding,\n// lv-filter-reset-scope, lv-filter-apply and lv-filter-cancel in the owner.\nhtml\`<lv-filter-dock pageId="playground" .contract=\${contract}\n  .filterState=\${filterState} .pending=\${${this.pending}}></lv-filter-dock>\``
    const tag = this.example === 'pane' ? 'lv-filter-pane-card' : this.example === 'slicer' ? 'lv-slicer' : 'lv-filter-leaf'
    const fixture = filterFixture(this.presentationStyle, this.disabled, this.empty, this.multiple)
    return `import { html } from 'lit'\nimport '../web/components/dashboard/filters/filter-control'\n\nconst { definition, binding, presentation } = ${JSON.stringify(fixture, null, 2)}\nconst expression = ${JSON.stringify(this.expression, null, 2)}\n\n// The owner handles lv-filter-mutate { bindingKey, expression }, updates its\n// expression property and rerenders. Pane cards also emit clear/reset events.\nhtml\`<${tag} .definition=\${definition} .binding=\${binding}\n  .presentation=\${presentation} .expression=\${expression}\n  .pending=\${${this.pending}} .stale=\${${this.stale}}></${tag}>\``
  }

  private record(event: Event): void {
    this.log = `${event.type}: ${JSON.stringify((event as CustomEvent).detail ?? {}, null, 2)}`
  }

  /** Reuse the production optimistic transition logic with an immediate local acknowledgement. */
  private runDock(action: (controller: DashboardFilterController) => void): void {
    const controller = new DashboardFilterController(() => {
      this.dockState = { ...controller.projected, revision: this.dockState.revision + 1 }
      controller.reconcile(this.dockState)
    }, () => `fixture-${this.dockState.revision + 1}`)
    controller.setApplicationMode(this.applicationMode)
    controller.setDefaults(Object.fromEntries(Object.entries(this.contract.bindings).map(([key, binding]) => [key, binding.default])))
    controller.reconcile(this.dockState)
    action(controller)
  }

  private mutate(event: CustomEvent<{ bindingKey: string; expression: DashboardFilterExpression }>) {
    if (this.example === 'dock') this.runDock(controller => controller.mutate(event.detail.bindingKey, event.detail.expression))
    else this.expression = event.detail.expression
    this.record(event)
  }

  private reset = (event: CustomEvent<{ bindingKey?: string; bindingKeys?: string[]; scope?: 'page' | 'dashboard' }>): void => {
    this.record(event)
    if (this.example !== 'dock') { this.expression = event.type === 'lv-filter-clear' ? { kind: 'unfiltered' } : filterFixture(this.presentationStyle).binding.default; return }
    this.runDock(controller => {
      if (event.type === 'lv-filter-reset-scope') controller.reset(event.detail.scope ?? 'page', event.detail.bindingKeys ?? [])
      else if (event.detail.bindingKey) {
        if (event.type === 'lv-filter-clear') controller.clear(event.detail.bindingKey)
        else controller.resetBinding(event.detail.bindingKey)
      }
    })
  }

  private apply = (event: Event): void => { this.runDock(controller => controller.apply()); this.record(event) }
  private cancel = (event: Event): void => { this.runDock(controller => controller.cancel()); this.record(event) }
  private clearExpressions = (): void => {
    if (this.example === 'dock') this.runDock(controller => { for (const key of Object.keys(this.contract.bindings)) controller.clear(key) })
    else this.expression = { kind: 'unfiltered' }
  }

  private summary(expression: DashboardFilterExpression): string {
    switch (expression.kind) {
      case 'unfiltered': return 'All values'
      case 'set': return expression.values.map(value => String(value.value)).join(', ') || 'No values'
      case 'comparison': return `${expression.operator}: ${expression.value.value}`
      case 'range': return `${expression.lower?.value.value ?? 'Any'} – ${expression.upper?.value.value ?? 'Any'}`
      case 'relative_period': return `${expression.direction} ${expression.count} ${expression.unit}`
      case 'null_check': return expression.operator
    }
  }
  render() {
    const { definition, binding, presentation } = filterFixture(this.presentationStyle, this.disabled, this.empty, this.multiple)
    const preview = this.example === 'dock' ? html`<lv-filter-dock pageId="playground" .contract=${this.contract} .filterState=${this.dockState} .pending=${this.pending} .pendingBindingKeys=${this.pending ? Object.keys(this.contract.bindings) : []} @lv-filter-dock-state=${this.record} @lv-filter-mutate=${this.mutate} @lv-filter-clear=${this.reset} @lv-filter-reset-binding=${this.reset} @lv-filter-reset-scope=${this.reset} @lv-filter-apply=${this.apply} @lv-filter-cancel=${this.cancel}></lv-filter-dock>` : this.example === 'pane' ? html`<lv-filter-pane-card .definition=${definition} .binding=${binding} .presentation=${presentation} .expression=${this.expression} .pending=${this.pending} .stale=${this.stale} .active=${this.expression.kind !== 'unfiltered'} .dirty=${this.pending} @lv-filter-mutate=${this.mutate} @lv-filter-clear=${this.reset} @lv-filter-reset-binding=${this.reset}></lv-filter-pane-card>` : this.example === 'slicer' ? html`<lv-slicer .definition=${definition} .binding=${binding} .presentation=${presentation} .expression=${this.expression} .pending=${this.pending} .stale=${this.stale} auto-height @lv-filter-mutate=${this.mutate}></lv-slicer>` : html`<lv-filter-leaf .definition=${definition} .binding=${binding} .presentation=${presentation} .expression=${this.expression} .pending=${this.pending} .stale=${this.stale} .showClearAction=${true} .autoHeight=${true} @lv-filter-mutate=${this.mutate}></lv-filter-leaf>`
    return html`<div class="stack"><div data-fixture-controls class="controls">
      <label>Presentation <select class="settings-input" aria-label="Filter presentation" .value=${this.presentationStyle} @change=${(event: Event) => { this.presentationStyle = (event.target as HTMLSelectElement).value as typeof this.presentationStyle; this.expression = { kind: 'unfiltered' }; this.dockState = initialDockState() }}>${filterStyles.filter(style => this.example !== 'dock' || !['list', 'buttons'].includes(style)).map(style => html`<option value=${style}>${style}</option>`)}</select></label>
      ${this.example === 'dock' ? html`
        <label>Application <select class="settings-input" aria-label="Filter application" .value=${this.applicationMode} @change=${(event: Event) => { this.runDock(controller => controller.cancel()); this.applicationMode = (event.target as HTMLSelectElement).value as typeof this.applicationMode }}><option value="immediate">Immediate</option><option value="deferred">Deferred · Apply / Cancel</option></select></label>
        <label>Filter scopes <select class="settings-input" aria-label="Filter scopes" .value=${this.scopes} @change=${(event: Event) => { this.scopes = (event.target as HTMLSelectElement).value as FilterScopes }}><option value="both">Page and all pages</option><option value="page">Page only</option><option value="report">All pages only</option></select></label>
      ` : nothing}
      ${(['disabled', 'empty', 'multiple', 'pending', 'stale'] as const).filter(key => this.example !== 'dock' || key !== 'stale').map(key => html`<label><input type="checkbox" .checked=${this[key]} @change=${(event: Event) => { this[key] = (event.target as HTMLInputElement).checked }}>${key}</label>`)}
      <button class="settings-button" @click=${this.clearExpressions}>Clear expression</button>
    </div><section class="preview" part="preview">${keyed(this.presentationStyle, preview)}
      ${this.example === 'dock' ? html`<div class="feedback" aria-label="Applied and draft filters"><p role="status">${this.dockState.dirtyBindings.length ? `${this.dockState.dirtyBindings.length} unapplied changes` : 'All changes applied'}</p><dl>${Object.entries(this.contract.bindings).filter(([, item]) => item.paneVisible).map(([key, item]) => html`<div><dt>${item.scope === 'report' ? 'All pages' : 'This page'}</dt><dd>Applied: ${this.summary(this.dockState.appliedControls[key]?.expression ?? { kind: 'unfiltered' })}</dd>${this.dockState.draftControls[key] ? html`<dd>Draft: ${this.summary(this.dockState.draftControls[key])}</dd>` : nothing}</div>`)}</dl></div>` : nothing}
    </section>
    ${exampleDetails(html`<section class="documentation">
      <p><code>${this.example === 'dock' ? 'lv-filter-dock' : this.example === 'pane' ? 'lv-filter-pane-card' : this.example === 'slicer' ? 'lv-slicer' : 'lv-filter-leaf'}</code> · Source: <code>web/components/dashboard/filters/${this.example === 'dock' ? 'filter-dock' : 'filter-control'}.ts</code></p>
      <p>Inputs: definition, binding, expression, presentation, pending, stale; slicer autoHeight. Events: lv-filter-mutate { bindingKey, expression }; pane also lv-filter-clear and lv-filter-reset-binding. Dock inputs: contract, filterState, pageId; events also lv-filter-reset-scope, lv-filter-dock-state, lv-filter-apply and lv-filter-cancel.</p>
      ${this.example === 'dock' ? html`<p>The production DashboardFilterController handles local mutations with immediate fixture acknowledgements. Deferred edits and resets remain drafts until Apply; Cancel discards drafts. Reset page targets page bindings; Reset all targets page and report bindings. Changing application mode discards unapplied edits. Source: <code>web/components/dashboard/filters/filter-controller.ts</code>.</p>` : nothing}
      <p>Seven production presentations. Static options preserve the generated signal contract without remote option requests. Selection and range commits update local state. Enter a minimum above the maximum to inspect validation; use Enter or move focus outside to commit. Pending is a display state; stale and readerEditable govern editability.</p>
      <pre aria-label="Public event log" aria-live="polite">${this.log}</pre><pre>${JSON.stringify(this.example === 'dock' ? this.dockState : this.expression, null, 2)}</pre>
    </section>`)}</div>`
  }
}
customElements.define('playground-filters', PlaygroundFilters)
