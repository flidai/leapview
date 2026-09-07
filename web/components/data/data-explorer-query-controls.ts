import { LitElement, css, html, nothing } from 'lit'
import { property } from 'lit/decorators.js'
import { ChevronDown, ChevronRight, ChevronUp, Filter, Plus, Search, Sigma, Table2, X } from 'lucide'
import type { DataExploreFieldSignal, DataExploreFilterSuggestionsSignal, DataExploreCommand } from '../../generated/signals'
import type { ExplorationSpec } from '../../generated/exploration'
import { lucideIcon } from '../shared/lucide-icons'
import {
  boundedExplorationLimit,
  emptyDataExploreCommand,
  explorationLimitOptions,
  explorationPivotValidation,
  explorationSortFields,
  explorationTimeGrains,
  filterOperatorsForType,
  fieldLabel,
  moveExplorationSort,
  pivotForSpec,
  removeExplorationPivot,
  removeExplorationSort,
  setExplorationTime,
  setExplorationTimeRange,
  unsupportedRelativeTimeRangeMessage,
  updateExplorationPivot,
  upsertExplorationSort,
} from './data-explorer-spec'

type ExecutionState = 'idle' | 'pending' | 'running' | 'stopped'
type PivotSection = 'rows' | 'columns' | 'metrics'
type TimeRange = NonNullable<NonNullable<ExplorationSpec['time']>['range']>
type FilterControlAction = 'apply' | 'cancel' | 'operator' | 'value'

export type DataExplorerFilterControlDetail = {
  action: FilterControlAction
  operator?: string
  value?: string
}

/**
 * Query-builder controls are deliberately a leaf component. It emits a
 * complete durable ExplorationSpec and never owns request, URL, or result
 * state. That keeps the route element focused on the server signal loop.
 */
export class DataExplorerQueryControls extends LitElement {
  @property({ attribute: false }) command: DataExploreCommand = emptyDataExploreCommand
  @property({ attribute: false }) fields: DataExploreFieldSignal[] = []
  @property({ attribute: false }) suggestions?: DataExploreFilterSuggestionsSignal
  @property({ attribute: false }) executionState: ExecutionState = 'idle'
  @property({ type: String }) filterField = ''
  @property({ type: String }) filterOperator = 'equals'
  @property({ type: String }) filterValue = ''

  static styles = css`
    :host { display: grid; min-width: 0; }
    .field-picker, .query-config { display: grid; gap: var(--base-size-8); border-bottom: var(--lv-border-muted); background: var(--lv-bg-panel-muted); padding: var(--base-size-8) var(--base-size-16); }
    .field-picker summary, .query-config summary { display: flex; min-height: var(--control-small-size); align-items: center; gap: var(--base-size-6); padding: 0; color: var(--lv-fg-default); font: var(--lv-type-body); text-transform: none; }
    summary::-webkit-details-marker { display: none; }
    .chevron { display: inline-grid; }
    details[open] > summary .chevron { transform: rotate(90deg); }
    .search { position: relative; min-width: 0; max-width: 32rem; }
    .search input { width: 100%; min-width: 0; height: var(--control-small-size); border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); color: var(--lv-fg-default); padding: 0 var(--base-size-8) 0 var(--base-size-32); font: var(--lv-type-body); box-sizing: border-box; }
    .search-icon { position: absolute; left: var(--base-size-8); top: 50%; display: grid; color: var(--lv-fg-muted); transform: translateY(-50%); }
    .field-groups { display: grid; max-height: 15rem; grid-template-columns: repeat(auto-fit, minmax(15rem, 1fr)); gap: var(--base-size-8); overflow: auto; padding: 0; }
    .field-group { min-width: 0; margin: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: var(--base-size-4); }
    .field-group summary { display: flex; min-height: var(--control-small-size); align-items: center; gap: var(--base-size-6); padding: var(--base-size-4) var(--base-size-6); color: var(--lv-fg-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-medium); text-transform: uppercase; }
    .object-list { display: grid; gap: var(--base-size-2); padding: var(--base-size-2); }
    .field-row { display: grid; min-width: 0; grid-template-columns: minmax(0, 1fr) auto; align-items: center; border-radius: var(--lv-radius-default); }
    .field-row:hover, .field-row:focus-within { background: var(--lv-bg-control-hover); }
    .field-row.is-unavailable { opacity: .58; }
    button, select, input { font: inherit; }
    button { border: 0; border-radius: var(--lv-radius-default); background: transparent; color: var(--lv-fg-default); cursor: pointer; }
    .field-button { display: grid; min-width: 0; grid-template-columns: 1rem minmax(0, 1fr); gap: var(--base-size-6); align-items: center; padding: var(--base-size-6); text-align: left; }
    .field-button strong, .field-button small { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .field-button strong { font: var(--lv-type-body); }
    .field-button small { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .field-button.is-selected strong { color: var(--lv-fg-accent); }
    .field-button:disabled { cursor: not-allowed; }
    .field-action { display: grid; width: var(--control-small-size); height: var(--control-small-size); place-items: center; color: var(--lv-fg-muted); }
    .config-note { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .filter-editor { display: grid; grid-template-columns: minmax(8rem, 1fr) minmax(8rem, 1fr) minmax(12rem, 2fr) auto; gap: var(--base-size-8); align-items: end; border-bottom: var(--lv-border-muted); background: var(--lv-bg-panel-muted); padding: var(--base-size-12) var(--base-size-16); }
    .filter-editor label { display: grid; min-width: 0; gap: var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-medium); }
    .filter-editor input, .filter-editor select { min-width: 0; height: var(--control-medium-size); border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); color: var(--lv-fg-default); padding: 0 var(--base-size-8); font: var(--lv-type-body); }
    .filter-actions { display: flex; gap: var(--base-size-4); }
    .config-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(11rem, 1fr)); gap: var(--base-size-8); align-items: end; }
    .config-grid label, .sort-list label, .pivot-section label { display: grid; min-width: 0; gap: var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-medium); }
    .config-grid input, .config-grid select, .sort-list select, .pivot-section select, .pivot-section input { min-width: 0; height: var(--control-medium-size); border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); color: var(--lv-fg-default); padding: 0 var(--base-size-8); font: var(--lv-type-body); }
    .sort-list, .pivot-section { display: grid; gap: var(--base-size-6); }
    .sort-item { display: grid; grid-template-columns: minmax(0, 1fr) minmax(8rem, 1fr) auto auto auto; gap: var(--base-size-6); align-items: end; }
    .pivot-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr)); gap: var(--base-size-12); }
    .pivot-item { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: var(--base-size-6); align-items: end; }
    .icon-button { display: inline-grid; width: var(--control-medium-size); height: var(--control-medium-size); place-items: center; border: var(--lv-border-default); background: var(--lv-bg-control); }
    .text-button { min-height: var(--control-medium-size); padding: 0 var(--base-size-12); font: var(--lv-type-body); font-weight: var(--base-text-weight-medium); }
    .icon-button:disabled, .text-button:disabled { cursor: not-allowed; opacity: .5; }
    .pivot-totals { display: flex; flex-wrap: wrap; gap: var(--base-size-8); align-items: center; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .pivot-totals label { display: inline-flex; align-items: center; gap: var(--base-size-4); font-weight: var(--base-text-weight-normal); }
    .pivot-error { color: var(--lv-fg-danger); font: var(--lv-type-caption); }
    @media (max-width: 760px) { .sort-item { grid-template-columns: 1fr 1fr auto auto auto; } .filter-editor { grid-template-columns: 1fr; } }
  `

  render() {
    const spec = this.command?.spec ?? emptyDataExploreCommand.spec
    const query = this.fieldQuery.toLowerCase()
    const fields = this.fields.filter((field) => !query || [field.id, field.label, field.datasetId, field.type, field.description].some((value) => String(value ?? '').toLowerCase().includes(query)))
    const selected = new Set([...spec.dimensions, ...spec.metrics].map((ref) => ref.field))
    return html`
      ${this.filterField ? this.renderFilterEditor(spec) : nothing}
      <details class="field-picker" open>
        <summary><span class="chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span><span>${selected.size ? `${selected.size} selected` : 'Choose fields'}</span><span class="config-note">Rows are dimensions; Analyze values are metrics.</span></summary>
        <label class="search"><span class="search-icon" aria-hidden="true">${lucideIcon(Search, { size: 15 })}</span><input type="search" aria-label="Search semantic fields" .value=${this.fieldQuery} @input=${this.changeFieldQuery} placeholder="Search fields" autocomplete="off" /></label>
        ${fields.length ? html`<div class="field-groups">${groupFields(fields).map((group) => html`
          <details class="field-group" open><summary><span class="chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 13 })}</span><span aria-hidden="true">${lucideIcon(group.kind === 'metric' ? Sigma : Table2, { size: 13 })}</span><span>${group.label}</span><em>${group.fields.length}</em></summary>
          <div class="object-list">${group.fields.map((field) => this.renderField(field, selected.has(field.id), spec))}</div></details>
        `)}</div>` : html`<p class="config-note">No semantic fields match this search.</p>`}
      </details>
      ${this.renderConfig(spec)}
    `
  }

  private renderFilterEditor(spec: ExplorationSpec) {
    const field = this.fields.find((candidate) => candidate.id === this.filterField)
    const type = field?.type
    const disabled = this.filterOperator === 'is_null' || this.filterOperator === 'is_not_null'
    const suggestions = this.suggestions?.field === this.filterField ? this.suggestions : undefined
    const inputType = filterInputType(type)
    const options = filterOperatorsForType(type)
    return html`<section class="filter-editor" aria-label="Add filter">
      <label>Field<input .value=${fieldLabel(this.filterField, this.fields)} disabled /></label>
      <label>Condition<select .value=${this.filterOperator} @change=${(event: Event) => this.emitFilterChange({ action: 'operator', operator: (event.target as HTMLSelectElement).value })}>
        ${options.map((option) => html`<option value=${option.value}>${option.label}</option>`)}
      </select></label>
      <label>Value
        <input
          type=${inputType}
          inputmode=${filterInputMode(type)}
          list=${suggestions?.values.length ? 'data-explorer-filter-values' : nothing}
          placeholder=${filterPlaceholder(type, this.filterOperator)}
          .value=${this.filterValue}
          ?disabled=${disabled}
          @input=${(event: Event) => this.emitFilterChange({ action: 'value', value: (event.target as HTMLInputElement).value })}
          @keydown=${(event: KeyboardEvent) => { if (event.key === 'Enter') this.emitFilterChange({ action: 'apply' }) }}
        />
        ${suggestions?.values.length ? html`<datalist id="data-explorer-filter-values">${suggestions.values.map((suggestion) => html`<option value=${String(suggestion.value.value)} label=${suggestion.label}></option>`)}</datalist>` : nothing}
      </label>
      <div class="filter-actions">
        <button type="button" class="text-button" @click=${() => this.emitFilterChange({ action: 'cancel' })}>Cancel</button>
        <button type="button" class="text-button" ?disabled=${!disabled && !this.filterValue.trim()} @click=${() => this.emitFilterChange({ action: 'apply' })}>Apply</button>
      </div>
      ${suggestions?.loading ? html`<span class="config-note" role="status">Loading governed suggestions…</span>` : nothing}
      ${suggestions?.stale ? html`<span class="config-note" role="status">Suggestions are stale; continue typing or apply your value.</span>` : nothing}
      ${suggestions?.error ? html`<span class="pivot-error" role="alert">${suggestions.error}</span>` : nothing}
      ${suggestions?.truncated ? html`<span class="config-note">Showing the first ${suggestions.values.length} suggestions.</span>` : nothing}
    </section>`
  }

  private fieldQuery = ''

  private renderField(field: DataExploreFieldSignal, selected: boolean, spec: ExplorationSpec) {
    const compatible = field.compatible !== false
    const rebaseable = !compatible && Boolean(field.rebaseDatasetId)
    const selectable = compatible || rebaseable
    const related = field.relationshipPath ?? []
    const explanation = compatible ? related.length ? `Related through ${related.join(' → ')}` : field.description || field.id : field.compatibilityReason || `Not compatible with ${spec.datasetId || 'this dataset'}`
    return html`<div class=${`field-row${selectable ? '' : ' is-unavailable'}`} title=${explanation}>
      <button type="button" class=${selected ? 'field-button is-selected' : 'field-button'} aria-pressed=${String(selected)} aria-disabled=${String(!selectable)} ?disabled=${!selectable} title=${explanation} @click=${() => this.emitFieldChange(field, spec)}>
        <span aria-hidden="true">${lucideIcon(selected ? X : Plus, { size: 13 })}</span><span><strong>${field.label || field.id}</strong><small>${field.datasetId || 'Shared'} · ${field.kind === 'metric' ? 'metric' : field.type || 'dimension'}${related.length ? ' · related' : ''}${rebaseable ? ' · changes grain' : ''}</small></span>
      </button>
      ${field.kind === 'dimension' && compatible ? html`<button type="button" class="field-action" title="Filter ${field.label}" aria-label="Filter ${field.label}" @click=${() => this.emitFilterOpen(field.id)}>${lucideIcon(Filter, { size: 13 })}</button>` : nothing}
    </div>`
  }

  private renderConfig(spec: ExplorationSpec) {
    const timeFields = this.fields.filter((field) => field.kind === 'dimension' && isTemporalType(field.type))
    const sortFields = explorationSortFields(spec)
    const errors = explorationPivotValidation(spec, this.fields)
    return html`<details class="query-config" aria-label="Query configuration" open>
      <summary><span class="chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span><span>Query configuration</span><span class="config-note">Time, sort, limit, and pivot</span></summary>
      <div class="config-grid">
        <label>Time field<select aria-label="Time field" .value=${spec.time?.field ?? ''} @change=${(event: Event) => this.changeTimeField((event.target as HTMLSelectElement).value, spec)}><option value="">No time field</option>${timeFields.map((field) => html`<option value=${field.id}>${field.label || field.id}</option>`)}</select></label>
        <label>Time grain<select aria-label="Time grain" .value=${spec.time?.grain ?? 'day'} ?disabled=${!spec.time} @change=${(event: Event) => this.changeTimeGrain((event.target as HTMLSelectElement).value, spec)}>${explorationTimeGrains.map((grain) => html`<option value=${grain}>${grain}</option>`)}</select></label>
        <label>Time range<select aria-label="Time range" .value=${spec.time?.range?.kind ?? 'all'} ?disabled=${!spec.time} @change=${(event: Event) => this.changeTimeRange((event.target as HTMLSelectElement).value, spec, timeFields)}><option value="all">All available</option><option value="relative" disabled>Relative (not supported)</option><option value="absolute">Absolute</option></select></label>
        <label>Row limit<select aria-label="Row limit" .value=${String(spec.limit)} @change=${(event: Event) => this.emitSpec({ ...spec, limit: boundedExplorationLimit(Number((event.target as HTMLSelectElement).value)) })}>${explorationLimitOptions.map((limit) => html`<option value=${limit}>${limit}</option>`)}</select></label>
      </div>
      ${spec.time?.range?.kind === 'relative' ? html`<p class="pivot-error" role="alert">${unsupportedRelativeTimeRangeMessage}</p>` : nothing}
      ${spec.time?.range?.kind === 'absolute' ? this.renderAbsoluteRange(spec.time.range, spec) : nothing}
      <div class="sort-list" aria-label="Sort order"><strong>Sort order</strong>${spec.sort.length ? spec.sort.map((sort, index) => html`<div class="sort-item"><label>Priority ${index + 1}<select aria-label=${`Sort field ${index + 1}`} .value=${sort.field} @change=${(event: Event) => this.changeSortField(index, (event.target as HTMLSelectElement).value, spec)}>${sortFields.map((field) => html`<option value=${field}>${fieldLabel(field, this.fields)}</option>`)}</select></label><label>Direction<select aria-label=${`Sort direction ${index + 1}`} .value=${sort.direction} @change=${(event: Event) => this.changeSortDirection(index, (event.target as HTMLSelectElement).value as 'asc' | 'desc', spec)}><option value="asc">Ascending</option><option value="desc">Descending</option></select></label><button type="button" class="icon-button" aria-label=${`Move sort ${index + 1} up`} ?disabled=${index === 0} @click=${() => this.emitSpec(moveExplorationSort(spec, index, -1))}>${lucideIcon(ChevronUp, { size: 14 })}</button><button type="button" class="icon-button" aria-label=${`Move sort ${index + 1} down`} ?disabled=${index === spec.sort.length - 1} @click=${() => this.emitSpec(moveExplorationSort(spec, index, 1))}>${lucideIcon(ChevronDown, { size: 14 })}</button><button type="button" class="icon-button" aria-label=${`Remove sort ${index + 1}`} @click=${() => this.emitSpec(removeExplorationSort(spec, index))}>${lucideIcon(X, { size: 14 })}</button></div>`) : html`<span class="config-note">No sort applied. Add one to order results by priority.</span>`}<button type="button" class="text-button" ?disabled=${!sortFields.some((field) => !spec.sort.some((sort) => sort.field === field))} @click=${() => this.addSort(spec)}>Add sort</button></div>
      ${this.renderPivot(spec, errors)}
    </details>`
  }

  private renderAbsoluteRange(range: Extract<TimeRange, { kind: 'absolute' }>, spec: ExplorationSpec) {
    const fieldType = this.fields.find((field) => field.id === spec.time?.field)?.type
    const inputType = isDateType(fieldType) ? 'date' : 'text'
    return html`<div class="config-grid" aria-label="Absolute time range"><label>From<input aria-label="Time range from" type=${inputType} placeholder=${inputType === 'text' ? 'YYYY-MM-DDTHH:MM:SSZ' : ''} .value=${range.lower ? String(range.lower.value.value) : ''} @change=${(event: Event) => this.changeAbsolute('lower', (event.target as HTMLInputElement).value, spec, fieldType)} /></label><label>To<input aria-label="Time range to" type=${inputType} placeholder=${inputType === 'text' ? 'YYYY-MM-DDTHH:MM:SSZ' : ''} .value=${range.upper ? String(range.upper.value.value) : ''} @change=${(event: Event) => this.changeAbsolute('upper', (event.target as HTMLInputElement).value, spec, fieldType)} /></label><span class="config-note">Use an ISO date or RFC3339 timestamp. Leave one side empty for an open bound.</span></div>`
  }

  private renderPivot(spec: ExplorationSpec, errors: string[]) {
    const pivot = spec.pivot
    const dimensions = this.fields.filter((field) => field.kind === 'dimension' && field.compatible !== false)
    const metrics = this.fields.filter((field) => field.kind === 'metric' && field.compatible !== false)
    return html`<details class="pivot-config" ?open=${Boolean(pivot)}><summary><span class="chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span><span>Pivot</span><span class="config-note">Rows × columns × metrics</span></summary>${pivot ? html`<div class="pivot-grid">${this.renderPivotSection('rows', pivot.rows, dimensions, spec)}${this.renderPivotSection('columns', pivot.columns, dimensions, spec)}${this.renderPivotSection('metrics', pivot.metrics, metrics, spec)}</div><div class="pivot-totals" aria-label="Pivot totals"><span>Totals</span>${(['rows', 'columns', 'grand'] as const).map((key) => html`<label><input type="checkbox" aria-label=${`Pivot ${key} totals`} .checked=${pivot.totals?.[key] === true} @change=${(event: Event) => this.changePivotTotal(key, (event.target as HTMLInputElement).checked, spec)} /> ${key}</label>`)}<label>Pivot limit<input aria-label="Pivot limit" type="number" min="1" max="1000" .value=${String(pivot.window?.limit ?? spec.limit)} @change=${(event: Event) => this.changePivotWindow('limit', (event.target as HTMLInputElement).value, spec)} /></label><label>Offset<input aria-label="Pivot offset" type="number" min="0" max="1000" .value=${String(pivot.window?.offset ?? 0)} @change=${(event: Event) => this.changePivotWindow('offset', (event.target as HTMLInputElement).value, spec)} /></label><button type="button" class="text-button" @click=${() => this.emitSpec(removeExplorationPivot(spec))}>Clear pivot</button></div>${errors.length ? html`<div class="pivot-error" role="alert">${errors.map((error) => html`<div>${error}</div>`)}</div>` : nothing}` : html`<button type="button" class="text-button" @click=${() => this.enablePivot(spec)}>Configure pivot</button>`}</details>`
  }

  private renderPivotSection(section: PivotSection, refs: Array<{ field: string }>, available: DataExploreFieldSignal[], spec: ExplorationSpec) {
    return html`<section class="pivot-section" aria-label=${`Pivot ${section}`}><strong>${section[0]!.toUpperCase()}${section.slice(1)}</strong>${refs.map((ref, index) => html`<div class="pivot-item"><label>${section} field ${index + 1}<select aria-label=${`Pivot ${section} field ${index + 1}`} .value=${ref.field} @change=${(event: Event) => this.changePivotRef(section, index, (event.target as HTMLSelectElement).value, spec)}>${available.map((field) => html`<option value=${field.id}>${fieldLabel(field.id, this.fields)}</option>`)}</select></label><button type="button" class="icon-button" aria-label=${`Remove pivot ${section} field ${index + 1}`} @click=${() => this.removePivotRef(section, index, spec)}>${lucideIcon(X, { size: 14 })}</button></div>`)}<button type="button" class="text-button" ?disabled=${!available.some((field) => !refs.some((ref) => ref.field === field.id))} @click=${() => this.addPivotRef(section, available, spec)}>Add ${section.slice(0, -1)}</button></section>`
  }

  private emitSpec(spec: ExplorationSpec) { this.dispatchEvent(new CustomEvent('lv-data-explorer-spec-change', { bubbles: true, composed: true, detail: spec })) }
  private emitFieldChange(field: DataExploreFieldSignal, spec: ExplorationSpec) { const key = field.kind === 'metric' ? 'metrics' : 'dimensions'; const values = spec[key]; const selected = values.some((ref) => ref.field === field.id); this.emitSpec({ ...spec, [key]: selected ? values.filter((ref) => ref.field !== field.id) : [...values, { field: field.id }] }) }
  private emitFilterOpen(field: string) { this.dispatchEvent(new CustomEvent('lv-data-explorer-filter-open', { bubbles: true, composed: true, detail: field })) }
  private emitFilterChange(detail: DataExplorerFilterControlDetail) { this.dispatchEvent(new CustomEvent('lv-data-explorer-filter-change', { bubbles: true, composed: true, detail })) }
  private changeFieldQuery = (event: Event) => { this.fieldQuery = (event.target as HTMLInputElement).value; this.requestUpdate() }
  private changeTimeField(field: string, spec: ExplorationSpec) { this.emitSpec(setExplorationTime(spec, field, spec.time?.grain ?? 'day')) }
  private changeTimeGrain(grain: string, spec: ExplorationSpec) { if (spec.time) this.emitSpec(setExplorationTime(spec, spec.time.field, grain as typeof explorationTimeGrains[number])) }
  private changeTimeRange(mode: string, spec: ExplorationSpec, fields: DataExploreFieldSignal[]) { if (!spec.time) return; if (mode === 'all') return this.emitSpec(setExplorationTimeRange(spec, undefined)); if (mode === 'relative') return; const type = fields.find((field) => field.id === spec.time?.field)?.type; const value = isDateType(type) ? new Date().toISOString().slice(0, 10) : new Date().toISOString(); this.emitSpec(setExplorationTimeRange(spec, { kind: 'absolute', lower: { value: { kind: isDateType(type) ? 'date' : 'timestamp', value }, inclusive: true } })) }
  private changeAbsolute(bound: 'lower' | 'upper', value: string, spec: ExplorationSpec, type?: string) { const range = spec.time?.range; if (!range || range.kind !== 'absolute') return; const next = { ...range }; if (!value.trim()) delete next[bound]; else next[bound] = { value: { kind: isDateType(type) ? 'date' : 'timestamp', value: value.trim() }, inclusive: true }; this.emitSpec(setExplorationTimeRange(spec, next)) }
  private addSort(spec: ExplorationSpec) { const field = explorationSortFields(spec).find((candidate) => !spec.sort.some((sort) => sort.field === candidate)); if (field) this.emitSpec(upsertExplorationSort(spec, field)) }
  private changeSortField(index: number, field: string, spec: ExplorationSpec) { if (spec.sort.some((entry, current) => current !== index && entry.field === field)) return; const sort = [...spec.sort]; sort[index] = { ...sort[index]!, field }; this.emitSpec({ ...spec, sort }) }
  private changeSortDirection(index: number, direction: 'asc' | 'desc', spec: ExplorationSpec) { const sort = [...spec.sort]; if (sort[index]) sort[index] = { ...sort[index]!, direction }; this.emitSpec({ ...spec, sort }) }
  private enablePivot(spec: ExplorationSpec) { this.emitSpec({ ...spec, pivot: { rows: spec.dimensions.map((ref) => ({ ...ref })), columns: [], metrics: spec.metrics.map((ref) => ({ ...ref })) } }) }
  private changePivotRef(section: PivotSection, index: number, field: string, spec: ExplorationSpec) { const pivot = pivotForSpec(spec); const refs = [...pivot[section]] as Array<{ field: string }>; if (refs.some((ref, current) => current !== index && ref.field === field)) return; refs[index] = { ...refs[index]!, field }; this.emitSpec(updateExplorationPivot(spec, section, refs as never)) }
  private addPivotRef(section: PivotSection, available: DataExploreFieldSignal[], spec: ExplorationSpec) { const pivot = pivotForSpec(spec); const field = available.find((candidate) => !pivot[section].some((ref) => ref.field === candidate.id)); if (field) this.emitSpec(updateExplorationPivot(spec, section, [...pivot[section], { field: field.id }] as never)) }
  private removePivotRef(section: PivotSection, index: number, spec: ExplorationSpec) { const pivot = pivotForSpec(spec); this.emitSpec(updateExplorationPivot(spec, section, pivot[section].filter((_, current) => current !== index) as never)) }
  private changePivotTotal(key: 'rows' | 'columns' | 'grand', value: boolean, spec: ExplorationSpec) { const pivot = pivotForSpec(spec); this.emitSpec({ ...spec, pivot: { ...pivot, totals: { ...pivot.totals, [key]: value } } }) }
  private changePivotWindow(key: 'limit' | 'offset', value: string, spec: ExplorationSpec) { const pivot = pivotForSpec(spec); const numeric = key === 'offset' ? Math.max(0, Math.trunc(Number(value) || 0)) : boundedExplorationLimit(Number(value), spec.limit); this.emitSpec({ ...spec, pivot: { ...pivot, window: { ...pivot.window, [key]: numeric, limit: pivot.window?.limit ?? spec.limit } } }) }
}

type ExploreFieldGroup = { kind: 'dimension' | 'metric'; label: string; fields: DataExploreFieldSignal[] }
function groupFields(fields: DataExploreFieldSignal[]): ExploreFieldGroup[] { const groups = new Map<string, ExploreFieldGroup>(); fields.forEach((field) => { const key = `${field.datasetId || 'shared'}:${field.kind}`; if (!groups.has(key)) groups.set(key, { kind: field.kind, label: `${field.datasetId || 'Shared'} · ${field.kind === 'metric' ? 'Metrics' : 'Dimensions'}`, fields: [] }); groups.get(key)!.fields.push(field) }); return Array.from(groups.values()) }
function isTemporalType(type: string | undefined): boolean { return /date|time|timestamp|datetime/i.test(type ?? '') }
function isDateType(type: string | undefined): boolean { const normalized = (type ?? '').toLowerCase(); return normalized === 'date' || normalized.endsWith('.date') || normalized === 'day' }

function normalizedFieldType(type: string | undefined): string { return (type ?? '').trim().toLowerCase() }
function isBooleanType(type: string | undefined): boolean { return normalizedFieldType(type).includes('bool') }
function isNumericType(type: string | undefined): boolean {
  const normalized = normalizedFieldType(type)
  return normalized.includes('int') || normalized === 'number' || /decimal|numeric|double|float/.test(normalized)
}
function isTimestampType(type: string | undefined): boolean { return /timestamp|datetime/.test(normalizedFieldType(type)) }
function filterInputType(type: string | undefined): 'date' | 'number' | 'text' { return isDateType(type) ? 'date' : isNumericType(type) ? 'number' : 'text' }
function filterInputMode(type: string | undefined): 'decimal' | 'numeric' | 'text' { return isNumericType(type) ? normalizedFieldType(type).includes('int') ? 'numeric' : 'decimal' : 'text' }
function filterPlaceholder(type: string | undefined, operator: string): string {
  if (operator === 'in' || operator === 'not_in') return 'Comma-separated values'
  if (isBooleanType(type)) return 'true or false'
  if (isTimestampType(type)) return 'YYYY-MM-DDTHH:MM:SSZ'
  if (isDateType(type)) return 'YYYY-MM-DD'
  return ''
}

if (!customElements.get('lv-data-explorer-query-controls')) customElements.define('lv-data-explorer-query-controls', DataExplorerQueryControls)
declare global { interface HTMLElementTagNameMap { 'lv-data-explorer-query-controls': DataExplorerQueryControls } }
