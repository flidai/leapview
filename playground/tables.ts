import { previewCode } from './example-code'
import { readState } from './example-state'
import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import { exampleDetails, exampleChromeStyles } from './example-chrome'
import type { DataExploreCommand, DataExplorerCommand } from '../web/generated/signals'
import type { EntityListItem } from '../web/components/shared/entity-list'
import type { WindowedTablePayload, WindowedTableRequest } from '../web/components/shared/windowed-table'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import '../web/components/shared/record-table'
import '../web/components/shared/windowed-table'
import '../web/components/shared/entity-list'
import '../web/components/data/preview-table'
import '../web/components/data/explore-table'
import { answerWindow, entityColumns, entityFixtures, exploreCommand, exploreFixture, previewCommand, previewFixture, recordFixture, tableRows, windowedFixture, type TableState } from './table-fixtures'

export { tableExamples } from './catalog'

const docs: Record<string, { tag: string; source: string; properties: string; events: string; note: string }> = {
  record: { tag: 'lv-record-table', source: 'web/components/shared/record-table.ts', properties: 'table { columns, rows, columnSelector, density, rowAction } · variant: minimal | primary | compact', events: 'lv-record-table-action { action, row }', note: 'Sort columns, show or hide columns, expand and copy SQL, or inspect and refresh a record. Column visibility persists under a playground-specific key.' },
  windowed: { tag: 'lv-windowed-table', source: 'web/components/shared/windowed-table.ts', properties: 'table { columns, blocks, totalRows, chunkSize, resetVersion, sort, visibleColumns, columnWidths } · compact', events: 'lv-windowed-table-request · lv-windowed-table-columns · lv-windowed-table-column-widths', note: 'Scroll through 1,000 deterministic rows. The local adapter fills the three block slots, honors sort and reset versions, and preserves column visibility and widths.' },
  'entity-list': { tag: 'lv-entity-list', source: 'web/components/shared/entity-list.ts', properties: 'items · columns · filters · clientFilter · groupBy · compact · mobileCards · rowAction', events: 'lv-entity-list-query · lv-entity-list-favorite-toggle · lv-entity-list-pin-toggle · lv-entity-list-row-action', note: 'Search, category filtering, sorting, and group collapse use production behavior. Favorites, pins, refresh, and inspection update local fixture state.' },
  'data-preview': { tag: 'lv-data-preview-table', source: 'web/components/data/preview-table.ts', properties: 'preview: DataPreviewSignal · command: DataExplorerCommand', events: 'lv-data-preview-table-command { block, start, count, requestSeq, resetVersion, sort, visibleColumns, columnWidths }', note: 'The production data-browser wrapper forwards window requests to the local fixture. Error recovery through Retry or Reset view also stays local.' },
  'data-explore': { tag: 'lv-data-explore-table', source: 'web/components/data/explore-table.ts', properties: 'result: DataExploreResultSignal · command: DataExploreCommand · visibleColumns', events: 'lv-data-explore-table-command { sort } or { columnWidths }', note: 'The production exploration wrapper shows 75 result rows. Header sorting produces a command that reorders the local result and advances its request version.' },
}

export class PlaygroundTables extends LitElement {
  @property() example = 'record'
  @state() private status: TableState = 'populated'
  @state() private compact = false
  @state() private grouped = true
  @state() private mobileCards = true
  @state() private truncated = false
  @state() private variant = 'minimal'
  @state() private table: WindowedTablePayload = windowedFixture()
  @state() private previewCommand = previewCommand()
  @state() private exploreCommand = exploreCommand()
  @state() private entities = entityFixtures()
  @state() private records = recordFixture()
  @state() private events: Array<{ name: string; detail: unknown }> = []
  @state() private selected = ''
  @state() private instance = 0

  getExampleCode() {
    return previewCode(this, [
      "import '../web/components/shared/record-table'",
      "import '../web/components/shared/windowed-table'",
      "import '../web/components/shared/entity-list'",
      "import '../web/components/data/preview-table'",
      "import '../web/components/data/explore-table'",
    ])
  }

  getExampleState() {
    return {
      status: this.status,
      compact: this.compact,
      grouped: this.grouped,
      mobileCards: this.mobileCards,
      truncated: this.truncated,
      variant: this.variant,
    }
  }

  async restoreExampleState(value: Record<string, unknown>) {
    await this.updateComplete
    Object.assign(this, readState(value, this.getExampleState(), { status: ['populated', 'empty', 'loading', 'error'], variant: ['minimal', 'primary', 'compact'] }))
    this.reset()
  }

  static styles = [settingsLayoutStyles, css`
    :host { display: block; min-width: 0; color: var(--lv-fg-default); font: var(--lv-type-body); }
    * { box-sizing: border-box; }
    .controls { display: flex; flex-wrap: wrap; align-items: end; gap: var(--base-size-12); margin-bottom: var(--base-size-16); }
    label { display: grid; gap: var(--base-size-4); font: var(--lv-type-caption); }
    label.check { display: flex; align-items: center; min-height: var(--control-medium-size); gap: var(--base-size-8); }
    input[type=checkbox] { accent-color: var(--lv-bg-accent); }
    .preview { min-width: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); overflow: auto; padding: var(--base-size-16); }
    .preview.is-windowed { height: var(--playground-preview-height, 420px); overflow: hidden; padding: 0; }
    lv-record-table, lv-entity-list { display: block; min-width: 0; }
    lv-windowed-table, lv-data-preview-table, lv-data-explore-table { display: grid; width: 100%; height: 100%; min-width: 0; min-height: 0; }
    .documentation { display: grid; gap: var(--base-size-16); margin-top: var(--base-size-16); font: var(--lv-type-body-compact); }
    .documentation p { margin: 0; color: var(--lv-fg-muted); }
    dl { display: grid; gap: var(--base-size-12); margin: 0; }
    dt { font-weight: var(--base-text-weight-semibold); margin-bottom: var(--base-size-4); }
    dd { margin: 0; color: var(--lv-fg-muted); overflow-wrap: anywhere; }
    code, pre { font-family: var(--fontStack-monospace); font-size: .8rem; overflow-wrap: anywhere; }
    pre { margin: 0; white-space: pre-wrap; padding: var(--base-size-8); background: var(--lv-bg-panel-muted); }
    .log-header { display: flex; justify-content: space-between; align-items: center; gap: var(--base-size-12); }
    .entries { display: grid; gap: var(--base-size-8); max-height: 18rem; overflow: auto; }
    h3 { margin: 0; font: var(--lv-type-section-title); }
    :host([preview-only]) .controls, :host([preview-only]) .documentation { display: none; }
    .selection-feedback { color: var(--lv-fg-muted); font: var(--lv-type-caption); margin: var(--base-size-12) 0 0; }
    :host([preview-only]) .selection-feedback { display: none; }
  `, exampleChromeStyles]

  protected willUpdate(changed: Map<PropertyKey, unknown>) {
    if (changed.has('example')) {
      this.status = 'populated'
      this.compact = this.truncated = false
      this.variant = 'minimal'
      this.reset()
    }
  }

  private reset = () => {
    this.table = windowedFixture(this.status)
    this.previewCommand = previewCommand()
    this.exploreCommand = exploreCommand()
    this.records = recordFixture(this.status === 'empty', this.compact)
    this.entities = this.status === 'empty' ? [] : entityFixtures()
    this.events = []
    this.selected = ''
    this.instance++
  }

  private log(name: string, detail: unknown) {
    this.events = [{ name, detail }, ...this.events].slice(0, 20)
  }

  private onWindow = (event: CustomEvent<WindowedTableRequest>) => {
    this.log(event.type, event.detail)
    if (this.status === 'loading' || this.status === 'error') return
    this.table = answerWindow(this.table, this.status === 'empty' ? [] : tableRows(), event.detail)
  }

  private onColumns = (event: CustomEvent<{ visibleColumns: string[] }>) => {
    this.log(event.type, event.detail)
    this.table = { ...this.table, visibleColumns: event.detail.visibleColumns }
  }

  private onWidths = (event: CustomEvent<{ columnWidths: Record<string, number> }>) => {
    this.log(event.type, event.detail)
    this.table = { ...this.table, columnWidths: event.detail.columnWidths }
  }

  private onPreview = (event: CustomEvent<Partial<DataExplorerCommand>>) => {
    this.log(event.type, event.detail)
    this.previewCommand = { ...this.previewCommand, ...event.detail }
    if (this.status === 'loading') return
    // The production failure view's Retry and Reset view send this same command.
    if (this.status === 'error') this.status = 'populated'
    const command = this.previewCommand
    const block = command.block === 'a' || command.block === 'b' || command.block === 'c' ? command.block : 'all'
    const direction = command.sort.direction === 'asc' || command.sort.direction === 'desc' ? command.sort.direction : ''
    this.table = answerWindow({ ...this.table, visibleColumns: command.visibleColumns, columnWidths: command.columnWidths }, this.status === 'empty' ? [] : tableRows(), {
      block, start: command.start, count: command.count, requestSeq: command.requestSeq, resetVersion: command.resetVersion,
      sort: { key: command.sort.column, column: command.sort.column, direction },
    })
  }

  private onExplore = (event: CustomEvent<Partial<DataExploreCommand>>) => {
    this.log(event.type, event.detail)
    const sorted = Boolean(event.detail.sort)
    this.exploreCommand = {
      ...this.exploreCommand, ...event.detail,
      requestSeq: this.exploreCommand.requestSeq + (sorted ? 1 : 0),
      resetVersion: this.exploreCommand.resetVersion + (sorted ? 1 : 0),
    }
  }

  private onEntity = (event: CustomEvent<{ item?: EntityListItem; action?: string; query?: string; filter?: string }>) => {
    const { item, action, query, filter } = event.detail
    this.log(event.type, item ? { id: item.id, action } : { query, filter })
    if (!item) return
    if (event.type === 'lv-entity-list-favorite-toggle') this.entities = this.entities.map((entry) => entry.id === item.id ? { ...entry, favorite: !entry.favorite } : entry)
    else if (event.type === 'lv-entity-list-pin-toggle') this.entities = this.entities.map((entry) => entry.id === item.id ? { ...entry, pinned: !entry.pinned } : entry)
    else if (action === 'refresh') this.entities = this.entities.map((entry) => entry.id === item.id ? { ...entry, columns: { ...entry.columns, status: 'Running' } } : entry)
    else this.selected = item.title
  }

  private onRecord = (event: CustomEvent<{ action: string; row: Record<string, unknown> }>) => {
    this.log(event.type, { action: event.detail.action, rowId: event.detail.row.id })
    if (event.detail.action === 'refresh') this.records = { ...this.records, rows: this.records.rows.map((row) => row.id === event.detail.row.id ? { ...row, status: { label: 'Running', tone: 'accent' } } : row) }
    else this.selected = String((event.detail.row.name as { label?: string })?.label ?? event.detail.row.id)
  }

  private checkbox(label: string, value: boolean, change: (value: boolean) => void) {
    return html`<label class="check"><input type="checkbox" .checked=${value} @change=${(event: Event) => change((event.target as HTMLInputElement).checked)}>${label}</label>`
  }

  private renderExample() {
    switch (this.example) {
      case 'record': return html`<lv-record-table .table=${this.records} .variant=${this.variant} @lv-record-table-action=${this.onRecord}></lv-record-table>`
      case 'windowed': return html`<lv-windowed-table .table=${this.table} ?compact=${this.compact} @lv-windowed-table-request=${this.onWindow} @lv-windowed-table-columns=${this.onColumns} @lv-windowed-table-column-widths=${this.onWidths}></lv-windowed-table>`
      case 'entity-list': return html`<lv-entity-list .items=${this.entities} .columns=${entityColumns} .filters=${[{ id: 'all', label: 'All categories' }, { id: 'commerce', label: 'Commerce' }, { id: 'finance', label: 'Finance' }]} list-label="Dashboards" search-placeholder="Search dashboards" empty-text="No dashboards in this fixture." client-filter ?compact=${this.compact} ?mobile-cards=${this.mobileCards} .groupBy=${this.grouped ? 'group' : ''} group-icon="folder" row-action="inspect" @lv-entity-list-query=${this.onEntity} @lv-entity-list-favorite-toggle=${this.onEntity} @lv-entity-list-pin-toggle=${this.onEntity} @lv-entity-list-row-action=${this.onEntity}></lv-entity-list>`
      case 'data-preview': return html`<lv-data-preview-table .preview=${previewFixture(this.table)} .command=${this.previewCommand} @lv-data-preview-table-command=${this.onPreview}></lv-data-preview-table>`
      case 'data-explore': return html`<lv-data-explore-table .result=${exploreFixture(this.exploreCommand, this.status, this.truncated)} .command=${this.exploreCommand} @lv-data-explore-table-command=${this.onExplore}></lv-data-explore-table>`
      default: return html`<p>Choose a table from the navigation.</p>`
    }
  }

  render() {
    const doc = docs[this.example]
    const windowed = ['windowed', 'data-preview', 'data-explore'].includes(this.example)
    const states: TableState[] = this.example === 'windowed' || this.example === 'data-preview' ? ['populated', 'empty', 'loading', 'error'] : this.example === 'data-explore' ? ['populated', 'empty', 'error'] : ['populated', 'empty']
    return html`<div data-fixture-controls class="controls">
      <label>State<select class="settings-input" aria-label="Table state" .value=${this.status} @change=${(event: Event) => { this.status = (event.target as HTMLSelectElement).value as TableState; this.reset() }}>${states.map((value) => html`<option value=${value}>${value[0].toUpperCase() + value.slice(1)}</option>`)}</select></label>
      ${this.example === 'record' ? html`<label>Variant<select class="settings-input" aria-label="Record table variant" .value=${this.variant} @change=${(event: Event) => { this.variant = (event.target as HTMLSelectElement).value }}>${['minimal', 'primary', 'compact'].map((value) => html`<option value=${value}>${value}</option>`)}</select></label>` : nothing}
      ${!this.example.startsWith('data-') ? this.checkbox('Compact', this.compact, (value) => { this.compact = value; this.records = { ...this.records, density: value ? 'tight' : 'normal' } }) : nothing}
      ${this.example === 'entity-list' ? html`${this.checkbox('Group by team', this.grouped, (value) => { this.grouped = value })}${this.checkbox('Mobile cards', this.mobileCards, (value) => { this.mobileCards = value })}` : nothing}
      ${this.example === 'data-explore' ? this.checkbox('Truncated result', this.truncated, (value) => { this.truncated = value }) : nothing}
      <button class="settings-button" @click=${this.reset}>Reload fixture</button>
    </div>
    <section class=${`preview ${windowed ? 'is-windowed' : ''}`} part="preview" aria-label="Interactive table preview">${keyed(`${this.example}:${this.instance}`, this.renderExample())}</section>
    ${this.selected ? html`<p class="selection-feedback" role="status">Inspecting <strong>${this.selected}</strong>.</p>` : nothing}
    ${doc ? exampleDetails(html`<section class="documentation"><p>${doc.note}</p><dl><div><dt>Component</dt><dd><code>&lt;${doc.tag}&gt;</code></dd></div><div><dt>Source</dt><dd><code>${doc.source}</code></dd></div><div><dt>Properties</dt><dd><code>${doc.properties}</code></dd></div><div><dt>Events</dt><dd><code>${doc.events}</code></dd></div></dl>
      <div class="log-header"><h3>Event log</h3><button class="settings-button" ?disabled=${!this.events.length} @click=${() => { this.events = [] }}>Clear log</button></div><div class="entries" aria-live="polite">${this.events.map((event) => html`<pre>${event.name} ${JSON.stringify(event.detail)}</pre>`)}</div>
    </section>`) : nothing}`
  }
}

customElements.define('playground-tables', PlaygroundTables)
