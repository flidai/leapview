import { readState } from './example-state'
import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import type { VisualizationEnvelope, VisualizationWindowRequest } from '../web/generated/visualization'
import type { OptimisticInteractionCommand } from '../web/components/dashboard/interaction-selection'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import '../web/components/dashboard/visualization/host'
import '../web/components/dashboard/visual-modal'
import { answerChartWindow, chartExamples, createChartFixture, defaultChartOptions, type ChartFixture, type ChartOptions } from './chart-fixtures'
import { exampleChromeStyles, exampleDetails } from './example-chrome'

export { chartExamples }

export class PlaygroundCharts extends LitElement {
  @property() example = 'line'
  @state() private options: ChartOptions = { ...defaultChartOptions }
  @state() private fixture?: ChartFixture
  @state() private events: Array<{ name: string; detail: unknown }> = []
  private revision = 0

  getExampleCode() {
    const height = Math.round(this.renderRoot.querySelector('.preview')?.getBoundingClientRect().height || 420)
    return [
      "import { html } from 'lit'",
      "import '../web/components/dashboard/visualization/host'",
      "import '../web/components/dashboard/visual-modal'",
      "import type { VisualizationEnvelope } from '../web/generated/visualization'",
      '', `const envelope: VisualizationEnvelope = ${JSON.stringify(this.fixture?.envelope, null, 2)}`,
      '', '// Load production tokens and wire the public selection/window events in the owner.',
      'html`<div style="height: ' + height + 'px">',
      '  <lv-visualization-host .envelope=${envelope}></lv-visualization-host>',
      '</div><lv-visual-modal></lv-visual-modal>`',
    ].join('\n')
  }

  getExampleState() {
    return {
      ...this.options,
    }
  }

  async restoreExampleState(value: Record<string, unknown>) {
    await this.updateComplete
    this.options = readState(value, defaultChartOptions, {
      scenario: ['standard', 'dense', 'long-labels', 'missing', 'single', 'zero', 'negative', 'precision'],
      status: ['ready', 'loading', 'empty', 'error'], legend: ['top', 'bottom', 'left', 'right', 'hidden'],
      labels: ['hidden', 'automatic', 'dense', 'always'], tooltip: ['default', 'value'],
      mapLayer: ['point', 'heat', 'density', 'choropleth', 'path', 'reference'], kpiMode: ['compact', 'bullet', 'progress'],
    })
    this.rebuild()
  }

  connectedCallback(): void {
    super.connectedCallback()
    this.addEventListener('lv-interaction-select', this.handleSelection as EventListener)
    this.addEventListener('lv-visualization-window-request', this.handleWindow as EventListener)
    this.addEventListener('lv-visual-action', this.handleAction as EventListener)
    document.addEventListener('leapview-theme-applied', this.handleTheme)
  }

  disconnectedCallback(): void {
    this.removeEventListener('lv-interaction-select', this.handleSelection as EventListener)
    this.removeEventListener('lv-visualization-window-request', this.handleWindow as EventListener)
    this.removeEventListener('lv-visual-action', this.handleAction as EventListener)
    document.removeEventListener('leapview-theme-applied', this.handleTheme)
    super.disconnectedCallback()
  }

  private handleTheme = (): void => { if (this.example === 'map') this.rebuild() }

  static styles = [settingsLayoutStyles, css`
    :host { display: block; min-width: 0; color: var(--lv-fg-default); }
    :host([preview-only]) .controls, :host([preview-only]) .documentation { display: none; }
    .controls { display: flex; flex-wrap: wrap; align-items: end; gap: var(--base-size-12); margin-bottom: var(--base-size-16); }
    .display-options { margin: 0; border: 0; padding: 0; }
    .display-options > summary { min-height: var(--control-medium-size); align-content: center; padding-inline: var(--base-size-8); color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .display-options > summary:focus-visible { outline: var(--borderWidth-thick) solid var(--focus-outlineColor); outline-offset: var(--base-size-2); }
    .display-options[open] { flex-basis: 100%; }
    .display-controls { display: flex; flex-wrap: wrap; align-items: end; gap: var(--base-size-12); padding-top: var(--base-size-12); }
    label { display: grid; gap: var(--base-size-4); font: var(--lv-type-caption); }
    label.check { display: flex; align-items: center; min-height: var(--control-medium-size); }
    .preview { height: var(--playground-preview-height, 420px); min-width: 0; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-chart-surface); overflow: hidden; }
    lv-visualization-host { display: block; width: 100%; height: 100%; }
    .documentation { display: grid; gap: var(--base-size-12); margin-top: var(--base-size-16); font: var(--lv-type-body-compact); }
    .documentation p { color: var(--lv-fg-muted); }
    details { margin-top: var(--base-size-12); border: var(--lv-border-default); border-radius: var(--lv-radius-default); padding: var(--base-size-12); }
    summary { cursor: pointer; font-weight: var(--base-text-weight-semibold); }
    pre { overflow: auto; max-height: 360px; padding: var(--base-size-12); background: var(--lv-bg-panel-muted); font: var(--lv-type-mono); white-space: pre-wrap; overflow-wrap: anywhere; }
    code { font-family: var(--fontStack-monospace); overflow-wrap: anywhere; }
    .log-heading { display: flex; justify-content: space-between; align-items: center; gap: var(--base-size-8); margin-top: var(--base-size-16); }
    .log-heading h3 { margin: 0; font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); }
    .empty-log { padding: var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); }
  `, exampleChromeStyles]

  protected willUpdate(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('example') || !this.fixture) {
      this.options = { ...defaultChartOptions }
      this.events = []
      this.rebuild()
    }
  }

  private rebuild(): void {
    const style = getComputedStyle(this)
    const nullColor = style.getPropertyValue('--lv-fg-muted').trim()
    const stroke = style.getPropertyValue('--lv-chart-surface').trim()
    this.fixture = createChartFixture(this.example, this.options, ++this.revision, nullColor && stroke ? { nullColor, stroke } : undefined)
  }

  private change<K extends keyof ChartOptions>(key: K, value: ChartOptions[K]): void {
    this.options = { ...this.options, [key]: value }
    this.rebuild()
  }

  private select(key: keyof ChartOptions, label: string, values: Array<[string, string]>) {
    return html`<label>${label}<select aria-label=${label} class="settings-input" .value=${String(this.options[key])}
      @change=${(event: Event) => this.change(key, (event.target as HTMLSelectElement).value as ChartOptions[typeof key])}>
      ${values.map(([value, text]) => html`<option value=${value}>${text}</option>`)}</select></label>`
  }

  private checkbox(key: 'axes' | 'multiSeries' | 'stacked' | 'smooth' | 'step' | 'symbols', label: string) {
    return html`<label class="check"><input type="checkbox" .checked=${this.options[key]}
      @change=${(event: Event) => this.change(key, (event.target as HTMLInputElement).checked)}>${label}</label>`
  }

  render() {
    const fixture = this.fixture
    if (!fixture) return nothing
    const spec = fixture.envelope.spec
    const hasAxes = spec.kind === 'cartesian' || spec.kind === 'point'
    const hasSeries = ['line', 'area', 'bar', 'column', 'combo', 'radar'].includes(this.example)
    const hasLine = this.example === 'line' || this.example === 'area' || (this.example === 'combo' && this.options.multiSeries)
    const hasPresentation = 'legend' in spec.presentation
    const hasTooltip = spec.kind === 'cartesian' || spec.kind === 'point' || spec.kind === 'proportional'
    const adapter = fixture.envelope.rendererID
    return html`
      <div data-fixture-controls class="controls" aria-label="Chart fixture controls">
        ${this.select('scenario', 'Data fixture', [['standard', 'Standard'], ['dense', 'Dense data'], ['long-labels', 'Long labels'], ['missing', 'Missing values'], ['single', 'Single datum'], ['zero', 'Zero values'], ['negative', 'Mixed signs'], ['precision', 'High precision']])}
        ${this.select('status', 'State', [['ready', 'Ready'], ['loading', 'Loading'], ['empty', 'Empty'], ['error', 'Error']])}
        ${spec.kind === 'geographic' ? this.select('mapLayer', 'Map layer', [['point', 'Points'], ['heat', 'Weighted heatmap'], ['density', 'Point density'], ['choropleth', 'Choropleth'], ['path', 'Route lines'], ['reference', 'Reference boundaries']]) : nothing}
        ${spec.kind === 'kpi' ? this.select('kpiMode', 'KPI mode', [['compact', 'Compact'], ['bullet', 'Bullet'], ['progress', 'Progress']]) : nothing}
        <button class="settings-button" @click=${() => { this.options = { ...defaultChartOptions }; this.events = []; this.rebuild() }}>Reset</button>
        ${hasPresentation || hasAxes || hasSeries || hasTooltip ? html`<details class="display-options"><summary>Display options</summary><div class="display-controls">
          ${hasPresentation ? this.select('legend', 'Legend', [['bottom', 'Bottom'], ['top', 'Top'], ['left', 'Left'], ['right', 'Right'], ['hidden', 'Hidden']]) : nothing}
          ${hasPresentation && spec.kind !== 'geographic' ? this.select('labels', 'Labels', [['automatic', 'Automatic'], ['hidden', 'Hidden'], ['dense', 'Dense'], ['always', 'Always']]) : nothing}
          ${hasTooltip ? this.select('tooltip', 'Tooltip fields', [['default', 'Default fields'], ['value', 'First metric only']]) : nothing}
          ${hasAxes ? this.checkbox('axes', 'Show axes') : nothing}
          ${hasSeries ? this.checkbox('multiSeries', 'Multiple series') : nothing}
          ${['line', 'area', 'bar', 'column'].includes(this.example) ? this.checkbox('stacked', 'Stack series') : nothing}
          ${hasLine ? html`${this.checkbox('smooth', 'Smooth lines')}${this.checkbox('step', 'Stepped lines')}${this.checkbox('symbols', 'Show symbols')}` : nothing}
        </div></details>` : nothing}
      </div>
      <div class="preview" part="preview">
        ${keyed(spec.kind === 'geographic' ? `${this.example}:${this.options.mapLayer}` : this.example, html`<lv-visualization-host .envelope=${fixture.envelope}></lv-visualization-host>`)}
      </div>
      <lv-visual-modal></lv-visual-modal>
      ${exampleDetails(html`<div class="documentation">
        <p>${fixture.note}</p>
        <p><strong>Production source:</strong> <code>web/components/dashboard/visualization/host.ts</code><br>
          <strong>Adapter:</strong> <code>web/components/dashboard/visualization/adapters/${adapter}.ts</code><br>
          <strong>Contract:</strong> <code>api/visualization/main.tsp</code> · schema ${fixture.envelope.schemaVersion}</p>
        <p><strong>Public events:</strong> <code>lv-interaction-select</code> · <code>lv-visualization-window-request</code> · <code>lv-visual-action</code></p>
        <details><summary>Usage and current public input</summary>
          <pre>${"import '../web/components/dashboard/visualization/host'\n\nhtml`<lv-visualization-host .envelope=${envelope}></lv-visualization-host>`"}</pre>
          <p>The host takes a <code>VisualizationEnvelope</code> property. Theme tokens are inherited; locale comes from the document language. Windowing and selection are handled here through public events.</p>
          <pre>${JSON.stringify(fixture.envelope, null, 2)}</pre>
        </details>
        <div class="log-heading"><h3>Public event log</h3><button class="settings-button" @click=${() => { this.events = [] }}>Clear log</button></div>
        ${this.events.length ? html`<pre aria-label="Public event log">${this.events.map((event) => `${event.name}\n${JSON.stringify(event.detail, null, 2)}`).join('\n\n')}</pre>` : html`<p class="empty-log">No events yet.</p>`}
      </div>`)}
    `
  }

  private record(name: string, detail: unknown): void {
    this.events = [{ name, detail }, ...this.events].slice(0, 12)
    this.dispatchEvent(new CustomEvent('playground-event', { bubbles: true, composed: true, detail: { name, detail } }))
  }

  private handleWindow = (event: CustomEvent<VisualizationWindowRequest>): void => {
    if (!this.fixture) return
    this.record(event.type, event.detail)
    this.fixture = { ...this.fixture, envelope: answerChartWindow(this.fixture.envelope, this.fixture.rows, event.detail) }
  }

  private handleSelection = (event: CustomEvent<OptimisticInteractionCommand>): void => {
    const fixture = this.fixture
    if (!fixture || event.detail.sourceId !== fixture.envelope.visualID) return
    this.record(event.type, event.detail)
    const envelope = fixture.envelope
    const command = event.detail
    let selection: VisualizationEnvelope['selection'] = []
    if (command.action !== 'clear') {
      const interaction = envelope.spec.interactions.find((item) => item.id === command.interactionKind)
      const identity: Record<string, unknown> = {}
      for (const mapping of interaction?.mappings ?? []) {
        const selected = command.mappings.find((item) => item.field === mapping.targetFieldID)
        if (selected) identity[mapping.source.field] = selected.value
      }
      if (Object.keys(identity).length) {
        const next = { datum: { dataset: 'primary', dataRevision: envelope.dataRevision, identity }, label: Object.values(identity).join(' · ') }
        const matching = (item: VisualizationEnvelope['selection'][number]) => JSON.stringify(item.datum.identity) === JSON.stringify(identity)
        selection = command.toggle
          ? envelope.selection.some(matching) ? envelope.selection.filter((item) => !matching(item)) : [...envelope.selection, next]
          : [next]
      }
    }
    this.fixture = { ...fixture, envelope: { ...envelope, selection } }
  }

  private handleAction = (event: CustomEvent): void => { this.record(event.type, event.detail) }
}

if (!customElements.get('playground-charts')) customElements.define('playground-charts', PlaygroundCharts)
