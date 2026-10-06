import { css, html, nothing } from 'lit'
import { ChevronDown, Filter, SlidersHorizontal, X } from 'lucide'
import type { ExplorationSpec } from '../../generated/exploration'
import type { DataExploreCommand, DataExploreFieldSignal, DataExploreSignal } from '../../generated/signals'
import { lucideIcon } from '../shared/lucide-icons'
import type { DataExplorerFilterControlDetail } from './data-explorer-query-controls'
import { filterOperator, filterValues } from './data-explorer-spec'
import { fieldLabel, label } from './data-explorer-view-model'

type FieldKind = 'dimension' | 'metric'

export function isOutsideSemanticFields(event: PointerEvent, root: ParentNode): boolean {
  const path = event.composedPath()
  return ['.semantic-fields', '.semantic-fields-toggle'].every((selector) => {
    const target = root.querySelector(selector)
    return !target || !path.includes(target)
  })
}

function closeSemanticFieldsOnEscape(event: KeyboardEvent, expanded: boolean, togglePane?: () => void) {
  if (event.key !== 'Escape' || !expanded) return
  const currentTarget = event.currentTarget as HTMLElement
  togglePane?.()
  const opener = currentTarget.classList.contains('semantic-fields-toggle')
    ? currentTarget
    : currentTarget.previousElementSibling
  if (opener instanceof HTMLButtonElement) opener.focus()
  event.stopPropagation()
}

export const semanticLayoutStyles = css`
  .query-bar {
    display: grid;
    gap: var(--base-size-8);
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-12) var(--base-size-16);
    background: var(--lv-bg-app);
  }
  .semantic-result {
    display: grid;
    min-width: 0;
    min-height: 0;
    grid-template-rows: auto auto minmax(18rem, 1fr);
    overflow-x: hidden;
    overflow-y: auto;
    overscroll-behavior: contain;
  }
  .semantic .explorer { grid-template-columns: minmax(0, 1fr); }
  .semantic .explorer > .browser, .semantic .browser-resizer { display: none; }
  .semantic-layout { --explorer-filter-width: 38px; grid-template-columns: 240px minmax(0, 1fr) var(--explorer-filter-width); }
  .semantic-layout.filters-open { --explorer-filter-width: 320px; }
  .semantic-fields { overflow: auto; padding: 16px; background: var(--lv-bg-panel-muted); border-right: var(--lv-border-muted); min-height: 0; }
  .semantic-fields input, .semantic-fields select { width: 100%; margin-bottom: 12px; }
  .semantic-fields input[type='search'] { padding: 8px 10px; border: var(--lv-border-muted); border-radius: 6px; background: var(--lv-bg-panel); color: var(--lv-fg-default); font: var(--lv-type-body); }
  .semantic-fields summary { display: block; cursor: pointer; padding: 10px 0; font: var(--lv-type-caption); color: var(--lv-fg-muted); }
  .semantic-fields summary::before { content: '▸'; margin-right: 6px; }
  .semantic-fields details[open] > summary::before { content: '▾'; }
  .semantic-field:has(input:checked) { background: var(--lv-bg-panel); border-radius: 4px; }
  .semantic-fields h3 { font: var(--lv-type-caption); color: var(--lv-fg-muted); margin: 20px 0 10px; }
  .semantic-filter-dock { display: grid; min-width: 0; min-height: 0; border-left: var(--lv-border-default); background: var(--lv-bg-app); }
  .semantic-filter-rail { display: flex; align-items: center; flex-direction: column; gap: 8px; border: 0; background: transparent; color: var(--lv-fg-muted); padding: 16px 0; cursor: pointer; font: var(--lv-type-caption); text-transform: uppercase; }
  .semantic-filter-trigger { display: inline-flex; align-items: center; gap: var(--base-size-6); white-space: nowrap; }
  .semantic-filter-trigger[aria-expanded='true'] { background: var(--lv-bg-control-hover); }
  .semantic-filter-trigger .filter-count { display: inline-grid; place-items: center; min-width: var(--base-size-16); padding: 0 var(--base-size-4); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); font: var(--lv-type-caption); }
  .semantic-filter-trigger:focus-visible, .semantic-filter-rail:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-line-accent); outline-offset: calc(-1 * var(--lv-border-width-focus)); }
  .semantic-filter-rail:hover { background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
  .semantic-filter-rail span { writing-mode: vertical-rl; }
  .semantic-filter-rail b { display: grid; min-width: 18px; min-height: 18px; place-items: center; border-radius: 50%; background: var(--lv-line-accent); color: var(--lv-fg-on-emphasis); font: var(--lv-type-caption); }
  .semantic-filter-panel { display: grid; grid-template-rows: auto minmax(0, 1fr) auto; min-height: 0; }
  .semantic-filter-header, .semantic-filter-footer { display: flex; align-items: center; justify-content: space-between; gap: 8px; border-bottom: var(--lv-border-muted); padding: 12px; }
  .semantic-filter-header strong { display: block; font: var(--lv-type-section-title); }
  .semantic-filter-header small { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .semantic-filter-header button, .semantic-filter-footer button { border: 0; background: transparent; color: var(--lv-fg-muted); cursor: pointer; font: var(--lv-type-caption); }
  .semantic-filter-footer { border-top: var(--lv-border-muted); border-bottom: 0; }
  .semantic-filter-footer button:disabled { opacity: .5; cursor: not-allowed; }
  .semantic-filter-body { overflow: auto; padding: 12px; }
  .semantic-filter-section-title { display: flex; justify-content: space-between; color: var(--lv-fg-muted); font: var(--lv-type-caption); text-transform: uppercase; margin-bottom: 10px; }
  .semantic-filter-add { display: grid; gap: 6px; margin-bottom: 12px; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .semantic-filter-picker { min-width: 0; }
  .semantic-filter-picker summary { text-transform: none; }
  .semantic-filter-picker summary { display: flex; min-height: var(--control-medium-size); box-sizing: border-box; align-items: center; gap: 8px; border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); color: var(--lv-fg-default); padding: 0 8px; cursor: pointer; font: var(--lv-type-body); list-style: none; }
  .semantic-filter-picker summary::-webkit-details-marker { display: none; }
  .semantic-filter-picker summary:focus-visible { outline: 2px solid var(--lv-line-accent); outline-offset: 2px; }
  .semantic-filter-picker summary::after { content: '▾'; margin-left: auto; color: var(--lv-fg-muted); }
  .semantic-filter-picker[open] > summary::after { transform: rotate(180deg); }
  .semantic-filter-options { display: grid; box-sizing: border-box; max-height: min(14rem, 32vh); gap: 2px; overflow: auto; overscroll-behavior: contain; margin-top: 4px; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: 4px; }
  .semantic-filter-option { display: grid; min-width: 0; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 8px; width: 100%; border: 0; border-radius: var(--lv-radius-default); background: transparent; color: var(--lv-fg-default); padding: 6px 8px; cursor: pointer; font: var(--lv-type-body); text-align: left; }
  .semantic-filter-option:hover, .semantic-filter-option:focus-visible { background: var(--lv-bg-control-hover); outline: 0; }
  .semantic-filter-option > span, .semantic-filter-option > small { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .semantic-filter-option > small { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .semantic-filter-body .selected-field-row { display: block; margin-bottom: 12px; }
  .semantic-filter-body .selected-field-row > .query-label { display: none; }
  .semantic-filter-body .selected-field-values { display: grid; justify-items: start; }
  .semantic-filter-body .chip { max-width: 100%; white-space: normal; text-align: left; }
  .semantic-filter-card { display: grid; gap: 8px; width: 100%; box-sizing: border-box; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); padding: 10px; background: var(--lv-bg-panel-muted); }
  .semantic-filter-card strong { font: var(--lv-type-body); }
  .semantic-filter-card-row { display: flex; align-items: center; justify-content: space-between; gap: 8px; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .semantic-filter-card-row button { border: 0; background: transparent; color: var(--lv-fg-link); cursor: pointer; font: var(--lv-type-caption); }
  .semantic-filter-body lv-data-explorer-query-controls { margin-top: 12px; }
  .semantic-field { display: flex; gap: 8px; align-items: center; padding: 5px 0; }
  .semantic-field label { display: flex; gap: 8px; flex: 1; min-width: 0; align-items: center; font: var(--lv-type-body); }
  .semantic-field-label { display: grid; min-width: 0; gap: var(--base-size-2); }
  .semantic-field-metadata { color: var(--lv-fg-muted); font: var(--lv-type-caption); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .semantic-field-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .semantic-field input { width: auto; margin: 0; }
  .semantic-field button { flex: none; }
  .field-grain-note { flex: none; color: var(--lv-fg-muted); font: var(--lv-type-caption); white-space: nowrap; }
  .semantic-layout .query-label { min-width: 0; }
  .selected-fields-heading { display: flex; align-items: center; flex-wrap: wrap; gap: var(--base-size-8); }
  .selected-fields-heading > strong { font: var(--lv-type-section-title); }
  .selected-fields-heading .query-actions { margin-left: auto; }
  .selected-fields-heading select { width: auto; min-width: 100px; }
  .selected-field-row { display: grid; grid-template-columns: 76px minmax(0, 1fr); gap: var(--base-size-12); align-items: start; }
  .selected-field-row > .query-label { padding-top: 4px; }
  .selected-field-values { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; min-width: 0; }
  .selected-field-values .chip { display: inline-flex; align-items: center; gap: 5px; max-width: 100%; }
  .selected-field-values .chip svg { flex: none; }
  .selected-field-values .query-summary { padding: 4px 0; }
  @media (max-width: 1100px) {
    .semantic-layout { grid-template-columns: 200px minmax(0, 1fr) var(--explorer-filter-width); }
  }
  @media (max-width: 720px) {
    .semantic-layout { position: relative; grid-template-columns: minmax(0, 1fr) 38px; grid-template-rows: auto minmax(24rem, 1fr); }
    .semantic-fields { max-height: 12rem; border-right: 0; border-bottom: var(--lv-border-muted); }
    .semantic-result { min-height: 24rem; grid-column: 1; }
    .semantic-filter-dock { grid-column: 2; grid-row: 1 / -1; }
    .filters-open .semantic-filter-dock { position: absolute; z-index: var(--zIndex-sticky, 50); inset: 0 0 0 auto; width: min(320px, 85vw); box-shadow: var(--lv-shadow-floating-sm); }
  }
`

function semanticFieldMetadata(field: DataExploreFieldSignal): string {
  return field.type?.trim() ?? ''
}

function semanticFieldDescription(field: DataExploreFieldSignal): string {
  return [semanticFieldMetadata(field), field.description, field.id, field.compatibilityReason].filter(Boolean).join(' · ')
}

export function renderSemanticFilterTrigger(count: number, open: boolean, toggle: () => void) {
  return html`<button type="button" class="text-button semantic-filter-trigger" aria-label=${`Filters, ${count} active`} aria-expanded=${String(open)} aria-controls="semantic-filter-dock" @click=${toggle}>${lucideIcon(SlidersHorizontal, { size: 15 })}<span>Filters</span><span class="filter-count" aria-hidden="true">${count}</span></button>`
}

export function renderSemanticFieldPane(
  spec: ExplorationSpec,
  explore: DataExploreSignal,
  selected: Set<string>,
  search: string,
  actions: {
    expanded?: boolean
    togglePane?: () => void
    model: (id: string) => void
    search: (value: string) => void
    toggle: (field: DataExploreFieldSignal, kind: FieldKind) => void
    filter: (id: string) => void
  },
) {
  return html`<button type="button" class="semantic-fields-toggle" aria-label="Model & fields" aria-expanded=${String(Boolean(actions.expanded))} @click=${actions.togglePane} @keydown=${(event: KeyboardEvent) => closeSemanticFieldsOnEscape(event, Boolean(actions.expanded), actions.togglePane)}>Model & fields ${lucideIcon(actions.expanded ? X : ChevronDown, { size: 16 })}</button>
  <aside class=${`semantic-fields${actions.expanded ? ' is-open' : ''}`} aria-label="Semantic model fields" @keydown=${(event: KeyboardEvent) => closeSemanticFieldsOnEscape(event, Boolean(actions.expanded), actions.togglePane)}>
    <label>Semantic model
      <select aria-label="Semantic model" .value=${spec.modelId} @change=${(event: Event) => actions.model((event.target as HTMLSelectElement).value)}>
        ${explore.semanticModels.map((model) => html`<option value=${model.id} .selected=${model.id === spec.modelId}>${model.title}</option>`)}
      </select>
    </label>
    <input type="search" aria-label="Search semantic fields" placeholder="Search fields" .value=${search} @input=${(event: Event) => actions.search((event.target as HTMLInputElement).value)} />
    ${(spec.mode === 'records' ? ['dimension'] as const : ['dimension', 'metric'] as const).map((kind) => html`
      <h3>${kind === 'dimension' ? 'Dimensions' : 'Metrics'}</h3>
      ${Array.from(new Set(explore.fields.filter((field) => field.kind === kind && (field.compatible !== false || field.rebaseDatasetId || selected.has(field.id))).map((field) => field.datasetId || 'Shared'))).map((group) => {
        const fields = explore.fields.filter((field) => field.kind === kind && (field.compatible !== false || field.rebaseDatasetId || selected.has(field.id)) && (field.datasetId || 'Shared') === group && (field.label + ' ' + field.id).toLowerCase().includes(search.toLowerCase()))
        if (!fields.length) return nothing
        return html`<details ?open=${Boolean(search) || fields.some((field) => selected.has(field.id))}>
          <summary>${label(group)} · ${fields.length}</summary>
          ${fields.map((field) => html`<div class="semantic-field" title=${semanticFieldDescription(field)}>
            <label><input type="checkbox" .checked=${selected.has(field.id)} ?disabled=${field.compatible === false && !field.rebaseDatasetId && !selected.has(field.id)} @change=${() => actions.toggle(field, kind)} /><span class="semantic-field-label"><span class="semantic-field-name">${field.label || field.id}</span><span class="semantic-field-metadata">${semanticFieldMetadata(field)}</span></span>${field.compatible === false && field.rebaseDatasetId ? html`<span class="field-grain-note">↗ ${label(field.rebaseDatasetId)}</span>` : nothing}</label>
            ${kind === 'dimension' && field.compatible !== false ? html`<button class="field-action" aria-label=${'Filter ' + field.label} @click=${() => actions.filter(field.id)}>${lucideIcon(Filter, { size: 13 })}</button>` : nothing}
          </div>`)}
        </details>`
      })}
    `)}
  </aside>`
}

export function renderSelectedFieldRows(spec: ExplorationSpec, fields: DataExploreFieldSignal[], remove: (id: string, kind: FieldKind) => void) {
  return (spec.mode === 'records' ? ['dimension'] as const : ['dimension', 'metric'] as const).map((kind) => html`
    <div class="selected-field-row">
      <span class="query-label">${kind === 'dimension' ? spec.mode === 'records' ? 'Fields' : 'Group by' : 'Metrics'}</span>
      <div class="selected-field-values">
        ${spec[kind === 'dimension' ? 'dimensions' : 'metrics'].length
          ? spec[kind === 'dimension' ? 'dimensions' : 'metrics'].map((field) => html`
              <button type="button" class="chip" aria-label=${`Remove ${fieldLabel(field.field, fields)} ${kind}`} title=${`Remove ${fieldLabel(field.field, fields)} ${kind}`} @click=${() => remove(field.field, kind)}>${fieldLabel(field.field, fields)} ${lucideIcon(X, { size: 12 })}</button>
            `)
          : html`<span class="query-summary">None selected</span>`}
      </div>
    </div>
  `)
}

export function renderSelectedFilterRow(
  spec: ExplorationSpec,
  renderFilter: (filter: ExplorationSpec['filters'][number], index: number) => ReturnType<typeof html>,
  clear: () => void,
  showClear = true,
) {
  return html`<div class="selected-field-row">
    <span class="query-label">Filters</span>
    <div class="selected-field-values">
      ${spec.filters.length
        ? spec.filters.map((filter, index) => renderFilter(filter, index))
        : html`<span class="query-summary">No filters</span>`}
      ${spec.filters.length && showClear ? html`<button type="button" class="chip" @click=${clear}>Clear filters</button>` : nothing}
    </div>
  </div>`
}

export function renderSemanticFilterControls(
  spec: ExplorationSpec,
  explore: DataExploreSignal,
  command: DataExploreCommand,
  editor: { field: string; operator: string; value: string; suggestionRequestSeq?: number },
  renderFilter: (filter: ExplorationSpec['filters'][number], index: number) => ReturnType<typeof html>,
  clear: () => void,
  change: (detail: DataExplorerFilterControlDetail) => void,
) {
  return html`${renderSelectedFilterRow(spec, renderFilter, clear, false)}
    <lv-data-explorer-query-controls
      .filterEditorOnly=${true}
      .command=${command}
      .fields=${explore.fields}
      .suggestions=${explore.filterSuggestions}
      .suggestionRequestSeq=${editor.suggestionRequestSeq ?? 0}
      .filterField=${editor.field}
      .filterOperator=${editor.operator}
      .filterValue=${editor.value}
      @lv-data-explorer-filter-change=${(event: CustomEvent<DataExplorerFilterControlDetail>) => change(event.detail)}
    ></lv-data-explorer-query-controls>`
}

function renderSemanticFilterCard(
  filter: ExplorationSpec['filters'][number],
  fields: DataExploreFieldSignal[],
  edit: () => void,
  remove: () => void,
) {
  return html`<div class="semantic-filter-card">
    <strong>${fieldLabel(filter.field, fields)}</strong>
    <div class="semantic-filter-card-row"><span>${filterOperator(filter).replaceAll('_', ' ')} ${filterValues(filter).join(', ')}</span><button type="button" @click=${edit}>Edit</button><button type="button" aria-label=${`Remove ${fieldLabel(filter.field, fields)} filter`} @click=${remove}>${lucideIcon(X, { size: 14 })}</button></div>
  </div>`
}

export function renderSemanticFilterDock(
  spec: ExplorationSpec,
  explore: DataExploreSignal,
  command: DataExploreCommand,
  open: boolean,
  editor: { field: string; operator: string; value: string; suggestionRequestSeq?: number },
  actions: {
    toggle: () => void
    add: (field: string) => void
    clear: () => void
    editFilter: (filter: ExplorationSpec['filters'][number]) => void
    removeFilter: (index: number) => void
    changeFilter: (detail: DataExplorerFilterControlDetail) => void
    changeSpec: (spec: ExplorationSpec) => void
  },
  showQueryConfig = true,
) {
  const filterFields = explore.fields.filter((field) => field.kind === 'dimension' && field.compatible !== false)
  const closeFilters = (event: Event) => {
    const root = (event.currentTarget as HTMLElement).getRootNode() as ParentNode
    actions.toggle()
    requestAnimationFrame(() => root.querySelector<HTMLButtonElement>('.semantic-filter-trigger, .semantic-filter-rail')?.focus())
  }
  return html`<aside id="semantic-filter-dock" class="semantic-filter-dock" aria-label="Explorer filters" @keydown=${(event: KeyboardEvent) => {
    if (!open || event.key !== 'Escape') return
    const picker = (event.currentTarget as HTMLElement).querySelector<HTMLDetailsElement>('.semantic-filter-picker')
    if (picker?.open) {
      picker.open = false
      picker.querySelector('summary')?.focus()
    } else {
      closeFilters(event)
    }
    event.stopPropagation()
  }} @click=${(event: MouseEvent) => {
    const picker = (event.currentTarget as HTMLElement).querySelector<HTMLDetailsElement>('.semantic-filter-picker')
    if (picker?.open && !picker.contains(event.target as Node)) picker.open = false
  }}>
    ${open ? html`<div class="semantic-filter-panel">
      <header class="semantic-filter-header"><div><strong>Filters</strong><small>${spec.filters.length ? `${spec.filters.length} active` : 'No active filters'}</small></div><button type="button" aria-label="Close filters" @click=${closeFilters}>${lucideIcon(X, { size: 16 })}</button></header>
      <div class="semantic-filter-body">
        <div class="semantic-filter-section-title"><span>Filters on this query</span><span>${spec.filters.length}</span></div>
        <div class="semantic-filter-add">
          <span>Add filter</span>
          <details class="semantic-filter-picker">
            <summary aria-label="Add filter">Choose a field…</summary>
            <div class="semantic-filter-options" aria-label="Filter fields">
              ${filterFields.map((field) => {
                const fieldName = field.label || field.id
                const datasetName = field.datasetId || 'Shared'
                return html`<button type="button" class="semantic-filter-option" aria-label=${`${fieldName}, ${datasetName}`} @click=${(event: Event) => {
                  actions.add(field.id)
                  const picker = (event.currentTarget as HTMLButtonElement).closest('details')
                  if (picker) picker.open = false
                }}><span>${fieldName}</span><small>${datasetName}</small></button>`
              })}
            </div>
          </details>
        </div>
        ${renderSemanticFilterControls(spec, explore, command, editor, (filter, index) => renderSemanticFilterCard(filter, explore.fields, () => actions.editFilter(filter), () => actions.removeFilter(index)), actions.clear, actions.changeFilter)}
        ${showQueryConfig ? html`<lv-data-explorer-query-controls .filtersOnly=${true} .compactConfig=${true} .command=${command} .fields=${explore.fields} @lv-data-explorer-spec-change=${(event: CustomEvent<ExplorationSpec>) => actions.changeSpec(event.detail)}></lv-data-explorer-query-controls>` : nothing}
      </div>
      <footer class="semantic-filter-footer"><button type="button" ?disabled=${!spec.filters.length} @click=${actions.clear}>Reset all</button></footer>
    </div>` : html`<button type="button" class="semantic-filter-rail" aria-label=${spec.filters.length ? `Filters, ${spec.filters.length} active` : 'Filters'} aria-expanded="false" aria-controls="semantic-filter-dock" @click=${actions.toggle}>${lucideIcon(SlidersHorizontal, { size: 16 })}<span>Filters</span>${spec.filters.length ? html`<b>${spec.filters.length}</b>` : nothing}</button>`}
  </aside>`
}
