import { css, html, nothing } from 'lit'
import { Filter, X } from 'lucide'
import type { ExplorationSpec } from '../../generated/exploration'
import type { DataExploreFieldSignal, DataExploreSignal } from '../../generated/signals'
import { lucideIcon } from '../shared/lucide-icons'
import { fieldLabel, label } from './data-explorer-view-model'

type FieldKind = 'dimension' | 'metric'

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
    grid-template-rows: auto auto auto minmax(18rem, 1fr);
    overflow-x: hidden;
    overflow-y: auto;
    overscroll-behavior: contain;
  }
  .semantic .explorer { grid-template-columns: minmax(0, 1fr); }
  .semantic .explorer > .browser, .semantic .browser-resizer { display: none; }
  .semantic-layout { grid-template-columns: 240px minmax(0, 1fr); }
  .semantic-fields { overflow: auto; padding: 16px; background: var(--lv-bg-panel-muted); border-right: var(--lv-border-muted); min-height: 0; }
  .semantic-fields input, .semantic-fields select { width: 100%; margin-bottom: 12px; }
  .semantic-fields input[type='search'] { padding: 8px 10px; border: var(--lv-border-muted); border-radius: 6px; background: var(--lv-bg-panel); color: var(--lv-fg-default); font: var(--lv-type-body); }
  .semantic-fields summary { display: block; cursor: pointer; padding: 10px 0; font: var(--lv-type-caption); color: var(--lv-fg-muted); }
  .semantic-fields summary::before { content: '▸'; margin-right: 6px; }
  .semantic-fields details[open] > summary::before { content: '▾'; }
  .semantic-field:has(input:checked) { background: var(--lv-bg-panel); border-radius: 4px; }
  .semantic-fields h3 { font: var(--lv-type-caption); color: var(--lv-fg-muted); margin: 20px 0 10px; }
  .semantic-field { display: flex; gap: 8px; align-items: center; padding: 5px 0; }
  .semantic-field label { display: flex; gap: 8px; flex: 1; min-width: 0; align-items: center; font: var(--lv-type-body); }
  .semantic-field input { width: auto; margin: 0; }
  .semantic-field button { flex: none; }
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
    .semantic-layout { grid-template-columns: 200px minmax(0, 1fr); }
  }
  @media (max-width: 720px) {
    .semantic-layout { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(24rem, 1fr); }
    .semantic-fields { max-height: 12rem; border-right: 0; border-bottom: var(--lv-border-muted); }
    .semantic-result { min-height: 24rem; }
  }
`

export function renderSemanticFieldPane(
  spec: ExplorationSpec,
  explore: DataExploreSignal,
  selected: Set<string>,
  search: string,
  actions: {
    model: (id: string) => void
    search: (value: string) => void
    toggle: (field: DataExploreFieldSignal, kind: FieldKind) => void
    filter: (id: string) => void
  },
) {
  return html`<aside class="semantic-fields" aria-label="Semantic model fields">
    <label>Semantic model
      <select aria-label="Semantic model" .value=${spec.modelId} @change=${(event: Event) => actions.model((event.target as HTMLSelectElement).value)}>
        ${explore.semanticModels.map((model) => html`<option value=${model.id} .selected=${model.id === spec.modelId}>${model.title}</option>`)}
      </select>
    </label>
    <input type="search" aria-label="Search semantic fields" placeholder="Search fields" .value=${search} @input=${(event: Event) => actions.search((event.target as HTMLInputElement).value)} />
    ${(['dimension', 'metric'] as const).map((kind) => html`
      <h3>${kind === 'dimension' ? 'Dimensions' : 'Metrics'}</h3>
      ${Array.from(new Set(explore.fields.filter((field) => field.kind === kind && (field.compatible !== false || selected.has(field.id))).map((field) => field.datasetId || 'Shared'))).map((group) => {
        const fields = explore.fields.filter((field) => field.kind === kind && (field.compatible !== false || selected.has(field.id)) && (field.datasetId || 'Shared') === group && (field.label + ' ' + field.id).toLowerCase().includes(search.toLowerCase()))
        if (!fields.length) return nothing
        return html`<details ?open=${Boolean(search) || fields.some((field) => selected.has(field.id))}>
          <summary>${label(group)} · ${fields.length}</summary>
          ${fields.map((field) => html`<div class="semantic-field" title=${field.compatibilityReason || field.id}>
            <label><input type="checkbox" .checked=${selected.has(field.id)} ?disabled=${field.compatible === false} @change=${() => actions.toggle(field, kind)} />${field.label || field.id}</label>
            ${kind === 'dimension' && field.compatible !== false ? html`<button class="field-action" aria-label=${'Filter ' + field.label} @click=${() => actions.filter(field.id)}>${lucideIcon(Filter, { size: 13 })}</button>` : nothing}
          </div>`)}
        </details>`
      })}
    `)}
  </aside>`
}

export function renderSelectedFieldRows(spec: ExplorationSpec, fields: DataExploreFieldSignal[], remove: (id: string, kind: FieldKind) => void) {
  return (['dimension', 'metric'] as const).map((kind) => html`
    <div class="selected-field-row">
      <span class="query-label">${kind === 'dimension' ? 'Group by' : 'Measures'}</span>
      <div class="selected-field-values">
        ${spec[kind === 'dimension' ? 'dimensions' : 'metrics'].length
          ? spec[kind === 'dimension' ? 'dimensions' : 'metrics'].map((field) => html`
              <button type="button" class="chip" aria-label=${`Remove ${fieldLabel(field.field, fields)}`} title=${`Remove ${fieldLabel(field.field, fields)}`} @click=${() => remove(field.field, kind)}>${fieldLabel(field.field, fields)} ${lucideIcon(X, { size: 12 })}</button>
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
) {
  return html`<div class="selected-field-row">
    <span class="query-label">Filters</span>
    <div class="selected-field-values">
      ${spec.filters.length
        ? spec.filters.map((filter, index) => renderFilter(filter, index))
        : html`<span class="query-summary">No filters</span>`}
      ${spec.filters.length ? html`<button type="button" class="chip" @click=${clear}>Clear filters</button>` : nothing}
    </div>
  </div>`
}
