import { previewCode } from './example-code'
import { readState } from './example-state'
import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { keyed } from 'lit/directives/keyed.js'
import { exampleDetails, exampleChromeStyles } from './example-chrome'
import type { FilterMenuSignal, FilterMenuOptionSignal } from '../web/generated/signals'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import { settingsFieldStyles } from '../web/components/shared/settings-field-styles'
import '../web/components/shared/select-menu'
import '../web/components/shared/entity-multi-select'
import '../web/components/shared/filter-menu'
import '../web/components/shared/toast'
import '../web/components/shared/loading-spinner'
import '../web/components/dashboard/filters/date-picker'

export const controlExamples = [
  { id: 'buttons', label: 'Buttons' },
  { id: 'fields', label: 'Form fields' },
  { id: 'select', label: 'Select menu' },
  { id: 'multiselect', label: 'Entity multiselect' },
  { id: 'date-picker', label: 'Date picker' },
  { id: 'filter-menu', label: 'Filter menu' },
  { id: 'toast', label: 'Toasts' },
  { id: 'loading', label: 'Loading' },
]

const options = [
  { value: 'daily', label: 'Daily' },
  { value: 'weekly', label: 'Weekly' },
  { value: 'monthly', label: 'Monthly' },
  { value: 'yearly', label: 'Yearly · unavailable', disabled: true },
]
const entities = [
  { id: 'alex', label: 'Alex Morgan', detail: 'alex@example.test', kind: 'principal' },
  { id: 'sam', label: 'Sam Rivera', detail: 'sam@example.test', kind: 'principal' },
  { id: 'analytics', label: 'Analytics team', detail: '12 members', kind: 'group' },
  { id: 'finance', label: 'Finance team', detail: '8 members', kind: 'group', disabled: true, disabledLabel: 'Managed by your administrator' },
]
const filterOptions: FilterMenuOptionSignal[] = [
  { value: 'active', label: 'Active', description: 'Currently running', countLabel: '24', icon: 'status', disabled: false, selected: false },
  { value: 'draft', label: 'Draft', description: 'Work in progress', countLabel: '8', icon: 'status', disabled: false, selected: false },
  { value: 'archived', label: 'Archived', description: 'Read only', countLabel: '3', disabled: true, selected: false },
]
const componentNames: Record<string, string> = {
  buttons: '<button class="settings-button">',
  fields: '<input / select / textarea class="settings-input">',
  select: '<lv-select-menu>',
  multiselect: '<lv-entity-multi-select>',
  'date-picker': '<lv-date-picker>',
  'filter-menu': '<lv-filter-menu>',
  toast: '<lv-toast>',
  loading: '<lv-loading-spinner>',
}
const documentation: Record<string, { source: string; properties: string; events: string; note: string }> = {
  buttons: { source: 'web/components/shared/settings-layout.ts', properties: 'Native button · .settings-button · .primary · .danger · disabled', events: 'click', note: 'Production settings button styles, applied to native buttons.' },
  fields: { source: 'web/components/shared/settings-layout.ts', properties: 'Native input / select / textarea · .settings-input · value · disabled · readonly', events: 'input · change', note: 'Production settings field and label styles. The browser owns native field behavior.' },
  select: { source: 'web/components/shared/select-menu.ts', properties: 'options · value · label · disabled · leading slot', events: 'lv-select-change { value } · lv-select-toggle { open }', note: 'Use arrow keys to move through choices, Enter to select, and Escape to close.' },
  multiselect: { source: 'web/components/shared/entity-multi-select.ts', properties: 'items · selectedIds · label · disabled · emptyMessage · remoteSearch', events: 'lv-entity-selection-change { selectedIds } · lv-entity-search { query }', note: 'Search and selection run locally. The disabled Finance team remains visible.' },
  'date-picker': { source: 'web/components/dashboard/filters/date-picker.ts', properties: 'value (YYYY-MM-DD) · label · placeholder · weekStart · invalid · error · disabled', events: 'lv-date-input { value, displayValue } · lv-date-escape', note: 'The production calendar supports month navigation, date selection, and clearing.' },
  'filter-menu': { source: 'web/components/shared/filter-menu.ts', properties: 'menu: { id, label, summaryLabel, mode, search, selected, options, loading, error }', events: 'lv-filter-menu-command { menuId, action, search, value, selected }', note: 'A local fixture handles search, toggle, and clear commands using the production component contract.' },
  toast: { source: 'web/components/shared/toast.ts', properties: 'message · tone (info / success / error) · actionLabel · actionDisabled · dismissible', events: 'lv-toast-action · lv-toast-dismiss', note: 'Declarative examples keep notification lifecycle local to this preview.' },
  loading: { source: 'web/components/shared/loading-spinner.ts', properties: 'size (small / medium / large) · aria-label', events: 'No component events', note: 'Production loading indicators respect your system reduced-motion preference.' },
}

export class PlaygroundControls extends LitElement {
  @property() example = 'buttons'
  @state() private disabled = false
  @state() private invalid = false
  @state() private empty = false
  @state() private loading = false
  @state() private sunday = false
  @state() private selected = 'weekly'
  @state() private selectedIds = ['alex']
  @state() private date = '2026-10-02'
  @state() private filterSelected = ['active']
  @state() private filterSearch = ''
  @state() private toastVisible = true
  @state() private logs: string[] = []
  @state() private fieldName = ''
  @state() private fieldSearch = ''
  @state() private fieldLimit = '100'
  @state() private fieldSchedule = 'Daily'
  @state() private fieldDescription = ''
  @state() private fieldNotifications = false
  @state() private fieldVisibility = 'private'

  getExampleCode() {
    return previewCode(this, [
      "import { settingsLayoutStyles } from '../web/components/shared/settings-layout'",
      "import { settingsFieldStyles } from '../web/components/shared/settings-field-styles'",
      "import '../web/components/shared/select-menu'",
      "import '../web/components/shared/entity-multi-select'",
      "import '../web/components/shared/filter-menu'",
      "import '../web/components/shared/toast'",
      "import '../web/components/shared/loading-spinner'",
      "import '../web/components/dashboard/filters/date-picker'",
    ], ["settingsLayoutStyles", "settingsFieldStyles"])
  }

  getExampleState() {
    return {
      disabled: this.disabled,
      invalid: this.invalid,
      empty: this.empty,
      loading: this.loading,
      sunday: this.sunday,
      selected: this.selected,
      selectedIds: this.selectedIds,
      date: this.date,
      filterSelected: this.filterSelected,
      filterSearch: this.filterSearch,
      toastVisible: this.toastVisible,
      fieldName: this.fieldName,
      fieldSearch: this.fieldSearch,
      fieldLimit: this.fieldLimit,
      fieldSchedule: this.fieldSchedule,
      fieldDescription: this.fieldDescription,
      fieldNotifications: this.fieldNotifications,
      fieldVisibility: this.fieldVisibility,
    }
  }

  async restoreExampleState(value: Record<string, unknown>) {
    await this.updateComplete
    Object.assign(this, readState(value, this.getExampleState(), { selected: ['daily', 'weekly', 'monthly'], selectedIds: ['alex', 'sam', 'analytics'], filterSelected: ['active', 'draft'], fieldSchedule: ['Daily', 'Weekly', 'Monthly'], fieldVisibility: ['private', 'team'] }))
  }

  static styles = [settingsLayoutStyles, settingsFieldStyles, css`
    :host { display: block; min-width: 0; color: var(--lv-fg-default); font: var(--lv-type-body); }
    * { box-sizing: border-box; }
    .stack { display: grid; gap: var(--base-size-24); }
    .preview { display: grid; align-content: start; gap: var(--base-size-24); min-height: 15rem; padding: var(--base-size-24); border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); }
    .row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--base-size-12); }
    .column { display: grid; min-width: 0; gap: var(--base-size-12); max-width: 32rem; }
    .form { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 15rem), 1fr)); gap: var(--base-size-20); }
    .field { display: grid; gap: var(--base-size-6); min-width: 0; }
    .field input, .field select, .field textarea { width: 100%; }
    textarea { resize: vertical; }
    .caption, .note { color: var(--lv-fg-muted); font: var(--lv-type-caption); margin: 0; }
    .state-panel { display: flex; flex-wrap: wrap; align-items: center; gap: var(--base-size-16); padding: var(--base-size-12) var(--base-size-16); background: var(--lv-bg-panel-muted); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); }
    .state-panel label, .check { display: inline-flex; align-items: center; gap: var(--base-size-8); }
    input[type=checkbox], input[type=radio] { accent-color: var(--lv-bg-accent); }
    .state-panel input, .check input { width: var(--base-size-16); height: var(--base-size-16); }
    .error { color: var(--lv-fg-danger); font: var(--lv-type-caption); }
    [aria-invalid=true] { border-color: var(--lv-fg-danger); }
    h3 { margin: 0; font: var(--lv-type-section-title); }
    dl { display: grid; gap: var(--base-size-12); margin: 0; }
    dt { font-weight: var(--base-text-weight-semibold); margin-bottom: var(--base-size-4); }
    dd { margin: 0; overflow-wrap: anywhere; color: var(--lv-fg-muted); }
    code, pre { font-family: var(--fontStack-monospace); font-size: .8rem; overflow-wrap: anywhere; }
    pre { white-space: pre-wrap; margin: 0; }
    .log { display: grid; gap: var(--base-size-12); border-top: var(--lv-border-muted); padding-top: var(--base-size-20); }
    .log-header { display: flex; justify-content: space-between; align-items: center; gap: var(--base-size-12); }
    .entries { display: grid; gap: var(--base-size-8); max-height: 16rem; overflow: auto; }
    .entry { padding: var(--base-size-8); background: var(--lv-bg-panel-muted); border-radius: var(--lv-radius-small); }
    lv-date-picker { max-width: 24rem; }
    lv-toast { max-width: 36rem; }
    .spinner { display: grid; justify-items: center; align-content: center; gap: var(--base-size-12); min-width: 6rem; }
    :host([preview-only]) .state-panel, :host([preview-only]) .note, :host([preview-only]) .documentation, :host([preview-only]) .log { display: none; }
    :host([preview-only]) .preview { border: 0; border-radius: 0; }
    @media (max-width: 500px) { .preview { padding: var(--base-size-16); } }
  `, exampleChromeStyles]

  protected willUpdate(changed: Map<PropertyKey, unknown>) {
    if (changed.has('example')) {
      this.disabled = this.invalid = this.empty = this.loading = this.sunday = false
      this.logs = []
      this.toastVisible = true
      this.filterSearch = ''
    }
  }

  private record = (event: Event) => {
    const detail = event instanceof CustomEvent ? event.detail : undefined
    this.log(event.type, detail)
  }

  private log(name: string, detail?: unknown) {
    this.logs = [`${name}${detail === undefined ? '' : `  ${JSON.stringify(detail)}`}`, ...this.logs].slice(0, 20)
  }

  private updateField(event: Event): void {
    const field = event.target as HTMLInputElement
    switch (field.name) {
      case 'name': this.fieldName = field.value; break
      case 'search': this.fieldSearch = field.value; break
      case 'limit': this.fieldLimit = field.value; break
      case 'schedule': this.fieldSchedule = field.value; break
      case 'description': this.fieldDescription = field.value; break
      case 'notifications': this.fieldNotifications = field.checked; break
      case 'visibility': this.fieldVisibility = field.value; break
    }
    this.log(event.type, { field: field.name, value: field.type === 'checkbox' ? field.checked : field.value })
  }

  private stateControl(label: string, checked: boolean, change: (checked: boolean) => void) {
    return html`<label><input type="checkbox" .checked=${checked} @change=${(event: Event) => change((event.target as HTMLInputElement).checked)}>${label}</label>`
  }

  private renderStates() {
    if (this.example === 'loading') return nothing
    return html`<div data-fixture-controls class="state-panel" aria-label="Example states">
      ${['buttons', 'fields', 'select', 'multiselect', 'date-picker'].includes(this.example) ? this.stateControl('Disabled', this.disabled, (value) => { this.disabled = value }) : nothing}
      ${['fields', 'date-picker', 'filter-menu'].includes(this.example) ? this.stateControl('Error', this.invalid, (value) => { this.invalid = value }) : nothing}
      ${['select', 'multiselect', 'filter-menu'].includes(this.example) ? this.stateControl('Empty options', this.empty, (value) => { this.empty = value }) : nothing}
      ${this.example === 'date-picker' ? this.stateControl('Sunday first', this.sunday, (value) => { this.sunday = value }) : nothing}
      ${this.example === 'filter-menu' ? this.stateControl('Loading', this.loading, (value) => { this.loading = value }) : nothing}
      ${this.example === 'toast' ? html`<button class="settings-button" @click=${() => { this.toastVisible = true; this.log('restore-toasts') }}>Restore toasts</button>` : nothing}
    </div>`
  }

  private renderExample() {
    switch (this.example) {
      case 'buttons': return html`<div class="column"><div class="row">${['Default', 'Primary', 'Danger'].map((label, index) => html`<button class=${`settings-button ${['', 'primary', 'danger'][index]}`} ?disabled=${this.disabled} @click=${() => this.log('click', { button: label })}>${label}</button>`)}</div><div class="row"><button class="settings-button" disabled>Disabled</button><button class="settings-button primary" disabled><lv-loading-spinner size="small" aria-hidden="true"></lv-loading-spinner>Saving…</button></div></div>`
      case 'fields': return html`<div class="form" @input=${this.updateField} @change=${this.updateField}>
        <label class="field"><span class="settings-label" id="name-label">Display name</span><input aria-labelledby="name-label" class="settings-input" name="name" .value=${this.fieldName} placeholder="Quarterly revenue" ?disabled=${this.disabled} aria-invalid=${String(this.invalid)} aria-describedby="name-hint"><span id="name-hint" class=${this.invalid ? 'error' : 'settings-description'}>${this.invalid ? 'Enter a display name.' : 'Shown in your workspace.'}</span></label>
        <label class="field"><span class="settings-label">Search</span><input class="settings-input" name="search" type="search" .value=${this.fieldSearch} placeholder="Search dashboards…" ?disabled=${this.disabled}></label>
        <label class="field"><span class="settings-label">Row limit</span><input class="settings-input" name="limit" type="number" min="1" max="1000" .value=${this.fieldLimit} ?disabled=${this.disabled}></label>
        <label class="field"><span class="settings-label">Refresh schedule</span><select class="settings-input" name="schedule" .value=${this.fieldSchedule} ?disabled=${this.disabled}><option>Daily</option><option>Weekly</option><option>Monthly</option></select></label>
        <label class="field"><span class="settings-label">Description</span><textarea class="settings-input" name="description" .value=${this.fieldDescription} rows="3" placeholder="Add a short description" ?disabled=${this.disabled}></textarea></label>
        <label class="field"><span class="settings-label">Read-only identifier</span><input class="settings-input" name="identifier" value="revenue_overview" readonly ?disabled=${this.disabled}></label>
        <label class="check"><input type="checkbox" name="notifications" .checked=${this.fieldNotifications} ?disabled=${this.disabled}>Email notifications</label>
        <div class="row" role="group" aria-label="Visibility"><label class="check"><input type="radio" name="visibility" value="private" .checked=${this.fieldVisibility === 'private'} ?disabled=${this.disabled}>Private</label><label class="check"><input type="radio" name="visibility" value="team" .checked=${this.fieldVisibility === 'team'} ?disabled=${this.disabled}>Team</label></div>
      </div>`
      case 'select': return html`<div class="column"><lv-select-menu label="Refresh frequency" .options=${this.empty ? [] : options} .value=${this.selected} ?disabled=${this.disabled} @lv-select-change=${(event: CustomEvent<{ value: string }>) => { this.selected = event.detail.value; this.record(event) }} @lv-select-toggle=${this.record}></lv-select-menu><p class="caption">Selected value: <code>${this.selected}</code></p></div>`
      case 'multiselect': return html`<div class="column"><lv-entity-multi-select label="Workspace members" .items=${this.empty ? [] : entities} .selectedIds=${this.selectedIds} ?disabled=${this.disabled} @lv-entity-selection-change=${(event: CustomEvent<{ selectedIds: string[] }>) => { this.selectedIds = event.detail.selectedIds; this.record(event) }} @lv-entity-search=${this.record}></lv-entity-multi-select><p class="caption">Selected IDs: <code>${JSON.stringify(this.selectedIds)}</code></p></div>`
      case 'date-picker': return html`<div class="column"><span class="settings-label">Reporting date</span><lv-date-picker label="Reporting date" .value=${this.date} .weekStart=${this.sunday ? 'sunday' : 'monday'} ?disabled=${this.disabled} ?invalid=${this.invalid} .error=${this.invalid ? 'Choose a date within the reporting period.' : ''} @lv-date-input=${(event: CustomEvent<{ value: string }>) => { this.date = event.detail.value; this.record(event) }} @lv-date-escape=${this.record}></lv-date-picker><p class="caption">ISO value: <code>${this.date || '(empty)'}</code></p></div>`
      case 'filter-menu': return html`<div class="column"><lv-filter-menu .menu=${{
        id: 'status', label: 'Status', summaryLabel: this.filterSelected.length ? `Status · ${this.filterSelected.length} selected` : 'Status', mode: 'multi',
        search: this.filterSearch, selected: this.filterSelected, loading: this.loading,
        error: this.invalid ? 'Options could not be loaded. Try again.' : '', placeholder: 'Search status', emptyLabel: 'No matching statuses.',
        options: (this.empty ? [] : filterOptions).filter((option) => option.label.toLowerCase().includes(this.filterSearch.toLowerCase())).map((option) => ({ ...option, selected: this.filterSelected.includes(option.value) })),
      } satisfies FilterMenuSignal} @lv-filter-menu-command=${this.onFilterCommand}></lv-filter-menu><p class="caption">Selected statuses: <code>${JSON.stringify(this.filterSelected)}</code></p></div>`
      case 'toast': return html`<div class="column">${this.toastVisible ? html`<lv-toast message="Dashboard saved." tone="success" dismissible @lv-toast-dismiss=${(event: Event) => { this.toastVisible = false; this.record(event) }}></lv-toast><lv-toast message="Refresh is scheduled for tomorrow." tone="info" actionLabel="Undo" @lv-toast-action=${(event: Event) => { this.toastVisible = false; this.record(event) }}></lv-toast><lv-toast message="We could not complete the refresh." tone="error" actionLabel="Retry" @lv-toast-action=${this.record}></lv-toast>` : html`<p class="caption">Toasts dismissed.</p>`}</div>`
      case 'loading': return html`<div class="row">${['small', 'medium', 'large'].map((size) => html`<div class="spinner"><lv-loading-spinner .size=${size} aria-label=${`Loading (${size})`}></lv-loading-spinner><span class="caption">${size}</span></div>`)}</div><div class="row"><button class="settings-button primary" disabled><lv-loading-spinner size="small" aria-hidden="true"></lv-loading-spinner>Publishing…</button></div>`
      default: return html`<p>Choose a control from the navigation.</p>`
    }
  }

  private onFilterCommand = (event: CustomEvent<{ action: string; search: string; value: string }>) => {
    this.record(event)
    const { action, search, value } = event.detail
    if (action === 'search') this.filterSearch = search
    if (action === 'clear') this.filterSelected = []
    if (action === 'toggle') this.filterSelected = this.filterSelected.includes(value) ? this.filterSelected.filter((item) => item !== value) : [...this.filterSelected, value]
  }

  render() {
    const doc = documentation[this.example]
    return html`<div class="stack">
      ${this.renderStates()}
      <section class="preview" part="preview" aria-label="Interactive component preview">${keyed(this.example, this.renderExample())}</section>
      ${exampleDetails(html`
        ${doc ? html`<p class="note">${doc.note}</p><dl class="documentation"><div><dt>Component</dt><dd><code>${componentNames[this.example]}</code></dd></div><div><dt>Source</dt><dd><code>${doc.source}</code></dd></div><div><dt>Properties & variants</dt><dd>${doc.properties}</dd></div><div><dt>Events</dt><dd><code>${doc.events}</code></dd></div></dl>` : nothing}
        <section class="log" aria-label="Event log"><div class="log-header"><h3>Event log</h3><button class="settings-button" ?disabled=${!this.logs.length} @click=${() => { this.logs = [] }}>Clear log</button></div><div class="entries" aria-live="polite" aria-relevant="additions">${this.logs.map((entry) => html`<pre class="entry">${entry}</pre>`)}</div></section>
      `)}
    </div>`
  }
}

customElements.define('playground-controls', PlaygroundControls)
