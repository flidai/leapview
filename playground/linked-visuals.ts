import { LitElement, css, html } from 'lit'
import { state } from 'lit/decorators.js'
import type { FilterMenuCommand } from '../web/generated/signals'
import type { VisualizationWindowRequest } from '../web/generated/visualization'
import { applyOptimisticInteraction, validateInteractionCommand, type OptimisticInteractionCommand } from '../web/components/dashboard/interaction-selection'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import '../web/components/shared/filter-menu'
import '../web/components/dashboard/visualization/host'
import '../web/components/dashboard/visual-modal'
import { answerChartWindow } from './chart-fixtures'
import { exampleChromeStyles, exampleDetails } from './example-chrome'
import { createLinkedVisualFixture, defaultLinkedState, linkedFilterMenu, linkedInteractionID, linkedRegionField, linkedRegions, linkedSelections, linkedSortFields, linkedStatuses, linkedVisualIDs, normalizeLinkedState, projectLinkedSelections, type LinkedExampleState } from './linked-visual-fixtures'

export class PlaygroundLinkedVisuals extends LitElement {
  @state() private model: LinkedExampleState = defaultLinkedState()
  @state() private fixture = createLinkedVisualFixture()
  @state() private events: Array<{ name: string; detail: unknown }> = []
  private revision = 1

  static styles = [settingsLayoutStyles, css`
    :host { display: block; min-width: 0; container-type: inline-size; color: var(--lv-fg-default); font: var(--lv-type-body); }
    * { box-sizing: border-box; }
    .composition { display: grid; min-width: 0; gap: var(--base-size-16); }
    .filter-bar { display: flex; flex-wrap: wrap; align-items: center; gap: var(--base-size-12); }
    .summary { flex: 1; min-width: 12rem; margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .summary-row { display: grid; grid-template-columns: minmax(0, 22rem) minmax(0, 1fr); align-items: center; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-chart-surface); }
    .summary-row .filter-bar { min-width: 0; padding: var(--base-size-16) var(--base-size-24); }
    .visual { min-width: 0; overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-chart-surface); }
    .chart { height: var(--playground-preview-height, 290px); }
    .kpi { height: 8rem; border: 0; border-right: var(--lv-border-default); border-radius: 0; background: transparent; }
    .table { height: 310px; }
    lv-visualization-host { display: block; width: 100%; height: 100%; }
    .documentation { display: grid; gap: var(--base-size-12); color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .log-heading { display: flex; flex-wrap: wrap; justify-content: space-between; align-items: center; gap: var(--base-size-8); }
    .log-heading h3 { margin: 0; color: var(--lv-fg-default); font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); }
    code, pre { font: var(--lv-type-mono); overflow-wrap: anywhere; }
    pre { margin: 0; max-height: 24rem; overflow: auto; padding: var(--base-size-12); white-space: pre-wrap; background: var(--lv-bg-panel-muted); }
    @container (max-width: 700px) {
      .summary-row { grid-template-columns: minmax(0, 1fr); }
      .kpi { border-right: 0; }
      .summary-row .filter-bar { border-top: var(--lv-border-default); padding: var(--base-size-16); }
    }
  `, exampleChromeStyles]

  connectedCallback(): void {
    super.connectedCallback()
    this.addEventListener('lv-filter-menu-command', this.filterCommand as EventListener)
    this.addEventListener('lv-interaction-select', this.selectRegion as EventListener)
    this.addEventListener('lv-visualization-window-request', this.windowRequest as EventListener)
    this.addEventListener('lv-visual-action', this.visualAction as EventListener)
  }

  disconnectedCallback(): void {
    this.removeEventListener('lv-filter-menu-command', this.filterCommand as EventListener)
    this.removeEventListener('lv-interaction-select', this.selectRegion as EventListener)
    this.removeEventListener('lv-visualization-window-request', this.windowRequest as EventListener)
    this.removeEventListener('lv-visual-action', this.visualAction as EventListener)
    super.disconnectedCallback()
  }

  getExampleState(): Record<string, unknown> {
    return { ...this.model, statuses: [...this.model.statuses], selectedRegions: [...this.model.selectedRegions] }
  }

  async restoreExampleState(value: Record<string, unknown>): Promise<void> {
    await this.updateComplete
    this.model = normalizeLinkedState(value)
    this.refreshData()
    this.events = []
    await this.updateComplete
  }

  getExampleCode(): string {
    const inputs = { chart: this.fixture.chart, table: this.fixture.table, kpi: this.fixture.kpi }
    return `import { html } from 'lit'
import type { FilterMenuSignal } from './web/generated/signals'
import type { VisualizationEnvelope } from './web/generated/visualization'
import './web/components/shared/filter-menu'
import './web/components/dashboard/visualization/host'

const menu: FilterMenuSignal = ${JSON.stringify(linkedFilterMenu(this.model), null, 2)}
const visuals: Record<'chart' | 'table' | 'kpi', VisualizationEnvelope> = ${JSON.stringify(inputs, null, 2)}

export const example = html\`
  <lv-filter-menu .menu=\${menu}></lv-filter-menu>
  <lv-visualization-host style="display: block; height: 8rem" .envelope=\${visuals.kpi}></lv-visualization-host>
  <lv-visualization-host style="display: block; height: 320px" .envelope=\${visuals.chart}></lv-visualization-host>
  <lv-visualization-host style="display: block; height: 310px" .envelope=\${visuals.table}></lv-visualization-host>
\`

// These public inputs reproduce the current filter, selection, and table sort.
// Handle lv-filter-menu-command to supply filtered envelopes and menu state.
// Handle lv-interaction-select with applyOptimisticInteraction, then project
// selection/highlights with web/components/dashboard/interaction-selection.
// Handle lv-visualization-window-request to supply sorted table windows.`
  }

  render() {
    const selected = this.model.selectedRegions
    return html`
      <section class="composition" part="preview" aria-label="Linked regional analytics">
        <div class="summary-row">
          <div class="visual kpi"><lv-visualization-host .envelope=${this.fixture.kpi}></lv-visualization-host></div>
          <div class="filter-bar">
            <lv-filter-menu .menu=${linkedFilterMenu(this.model)}></lv-filter-menu>
            <p class="summary" role="status">${selected.length ? `${selected.join(', ')} · ${this.fixture.highlightedOrders} of ${this.fixture.orderCount} orders highlighted` : `${this.fixture.orderCount} orders · select a region to highlight`}</p>
            <button class="settings-button" ?disabled=${!selected.length} @click=${this.clearSelection}>Clear selection</button>
            <button class="settings-button" @click=${this.reset}>Reset</button>
          </div>
        </div>
        <div class="visual chart"><lv-visualization-host .envelope=${this.fixture.chart}></lv-visualization-host></div>
        <div class="visual table"><lv-visualization-host .envelope=${this.fixture.table}></lv-visualization-host></div>
      </section>
      <lv-visual-modal></lv-visual-modal>
      ${exampleDetails(html`<div class="documentation">
        <p>Status filters update every view. Region selection highlights the linked view while keeping its totals intact. Use table cells for keyboard selection; select another region to add it, or select it again to clear it.</p>
        <p><strong>Production components:</strong> <code>web/components/shared/filter-menu.ts</code>, <code>web/components/dashboard/visualization/host.ts</code>.</p>
        <p><strong>Selection:</strong> <code>web/components/dashboard/interaction-selection.ts</code> projects canonical selection and highlight state into each envelope.</p>
        <p><strong>Events:</strong> <code>lv-filter-menu-command</code>, <code>lv-interaction-select</code>, <code>lv-visualization-window-request</code>, <code>lv-visual-action</code>.</p>
        <details><summary>Current inputs</summary><pre>${JSON.stringify({ state: this.getExampleState(), chart: this.fixture.chart, table: this.fixture.table, kpi: this.fixture.kpi }, null, 2)}</pre></details>
        <div class="log-heading"><h3>Public event log</h3><button class="settings-button" @click=${() => { this.events = [] }}>Clear log</button></div>
        <pre aria-label="Public event log">${this.events.map((event) => `${event.name}\n${JSON.stringify(event.detail, null, 2)}`).join('\n\n') || 'No events yet.'}</pre>
      </div>`)}
    `
  }

  private record(name: string, detail: unknown): void {
    this.events = [{ name, detail }, ...this.events].slice(0, 12)
    this.dispatchEvent(new CustomEvent('playground-event', { bubbles: true, composed: true, detail: { name, detail } }))
  }

  private filterCommand = (event: CustomEvent<FilterMenuCommand>): void => {
    const command = event.detail
    if (command.menuId !== 'linked-order-status') return
    if (command.action === 'search') {
      this.model = { ...this.model, statusSearch: typeof command.search === 'string' ? command.search.slice(0, 80) : '' }
    } else if (command.action === 'clear') {
      this.model = { ...this.model, statuses: [] }
      this.refreshData()
    } else if (command.action === 'toggle') {
      const status = linkedStatuses.find((value) => value === command.value)
      if (!status) return
      this.model = { ...this.model, statuses: this.model.statuses.includes(status) ? this.model.statuses.filter((value) => value !== status) : [...this.model.statuses, status] }
      this.refreshData()
    } else return
    this.record(event.type, command)
  }

  private selectRegion = (event: CustomEvent<OptimisticInteractionCommand>): void => {
    const command = event.detail
    const source = command.sourceId === linkedVisualIDs.chart ? 'chart' : command.sourceId === linkedVisualIDs.table ? 'table' : undefined
    if (!source) return
    const envelope = this.fixture[source]
    if (command.specRevision && command.specRevision !== envelope.specRevision) return
    if (command.dataRevision !== undefined && command.dataRevision !== envelope.dataRevision) return
    if (!validateInteractionCommand(command, { kind: linkedInteractionID, mappings: [{ field: linkedRegionField, value: 'region' }] })) return
    if (command.action !== 'clear' && !command.mappings.every((mapping) => linkedRegions.some((region) => region === mapping.value))) return
    const previous = source === this.model.selectionSource ? linkedSelections(this.model) : []
    const selections = applyOptimisticInteraction(previous, command)
    const selectedRegions = linkedRegions.filter((region) => selections.some((selection) => selection.entries?.some((entry) => entry.mappings?.some((mapping) => mapping.field === linkedRegionField && mapping.value === region))))
    this.model = { ...this.model, selectionSource: source, selectedRegions }
    this.fixture = projectLinkedSelections(this.fixture, this.model)
    this.record(event.type, command)
  }

  private windowRequest = (event: CustomEvent<VisualizationWindowRequest>): void => {
    const request = event.detail
    if (request.visualID !== linkedVisualIDs.table || request.sort.length !== 1) return
    const field = linkedSortFields.find((value) => value === request.sort[0]?.field.field)
    const direction = request.sort[0]?.direction
    if (!field || (direction !== 'ascending' && direction !== 'descending')) return
    const table = answerChartWindow(this.fixture.table, this.fixture.tableRows, request)
    if (table === this.fixture.table) return
    this.model = { ...this.model, sortField: field, sortDirection: direction }
    this.fixture = projectLinkedSelections({ ...this.fixture, table }, this.model)
    this.record(event.type, request)
  }

  private refreshData(): void {
    this.fixture = createLinkedVisualFixture(this.model, ++this.revision)
  }

  private clearSelection = (): void => {
    this.model = { ...this.model, selectedRegions: [] }
    this.fixture = projectLinkedSelections(this.fixture, this.model)
    this.record('clear-selection', {})
  }

  private reset = (): void => {
    this.model = defaultLinkedState()
    this.refreshData()
    this.record('reset', this.getExampleState())
  }

  private visualAction = (event: CustomEvent): void => { this.record(event.type, event.detail) }
}

if (!customElements.get('playground-linked-visuals')) customElements.define('playground-linked-visuals', PlaygroundLinkedVisuals)
