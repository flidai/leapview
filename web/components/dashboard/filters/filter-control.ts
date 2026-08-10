import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import type {
  DashboardCompiledFilterBinding,
  DashboardCompiledFilterDefinition,
  DashboardFilterExpression,
  DashboardFilterOptionItem,
  DashboardFilterOptionPage,
  DashboardFilterPresentation,
  DashboardFilterValue,
} from '../../../generated/signals'
import {
  resolveWidgetLayout,
  type WidgetContractID,
  type WidgetLayoutResolution,
} from '../visualization/layout'

export type FilterMutationDetail = {
  bindingKey: string
  expression: DashboardFilterExpression
}

export type FilterOptionsNeededDetail = {
  bindingKey: string
  search: string
  cursor?: string
  limit: number
}

const unfiltered: DashboardFilterExpression = { kind: 'unfiltered' }

export class DashboardFilterLeaf extends LitElement {
  @property({ attribute: false }) definition?: DashboardCompiledFilterDefinition
  @property({ attribute: false }) binding?: DashboardCompiledFilterBinding
  @property({ attribute: false }) expression: DashboardFilterExpression = unfiltered
  @property({ attribute: false }) options?: DashboardFilterOptionPage
  @property({ attribute: false }) presentation?: DashboardFilterPresentation
  @property({ type: Boolean, reflect: true }) pending = false
  @property({ type: Boolean, reflect: true }) stale = false
  @property({ type: Boolean }) showTitle = true

  private hasRequestedOptions = false
  private optionRequestAttempts = 0
  private optionRetryTimer?: ReturnType<typeof setTimeout>
  private optionRetryDelay = 1_200
  private resizeObserver?: ResizeObserver
  @state() private optionLoading = false

  static styles = css`
    :host { display: block; min-width: 0; font: inherit; }
    fieldset { display: grid; min-width: 0; gap: var(--base-size-6); border: 0; margin: 0; padding: 0; }
    legend.visually-hidden {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0 0 0 0);
      clip-path: inset(50%);
      white-space: nowrap;
    }
    .field-heading {
      display: flex;
      min-width: 0;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--base-size-6);
      font: var(--lv-type-caption);
      line-height: var(--base-text-lineHeight-tight);
    }
    .field-heading[data-title='false'] { justify-content: flex-end; }
    .field-title {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-weight: var(--base-text-weight-semibold);
    }
    input, select, button {
      min-height: var(--control-medium-size);
      font: var(--lv-type-body);
      line-height: var(--base-text-lineHeight-tight);
    }
    input, select {
      width: 100%; min-width: 0; border: var(--lv-border-default);
      border-radius: var(--lv-radius-default); background: var(--lv-bg-panel);
      color: inherit; padding-inline: var(--base-size-8); box-sizing: border-box;
    }
    .options { display: grid; max-height: 220px; gap: 2px; overflow: auto; }
    .option { display: flex; align-items: center; gap: 8px; border-radius: 4px; padding: 4px; }
    .option[data-unavailable='true'] { color: var(--lv-fg-muted); }
    .option input { width: auto; min-height: 0; }
    .buttons { display: flex; flex-wrap: wrap; gap: 4px; }
    .buttons button[aria-pressed='true'] { background: var(--bgColor-accent-muted); }
    .range { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; }
    :host([data-layout-variant='stacked']) .range { grid-template-columns: minmax(0, 1fr); }
    .range label { display: grid; min-width: 0; gap: var(--base-size-4); }
    .field-label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }
    .input-control { display: grid; gap: var(--base-size-4); }
    .operator {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }
    .relative { display: grid; grid-template-columns: 1fr 72px 1fr; gap: 6px; }
    :host([data-layout-variant='stacked']) .relative { grid-template-columns: minmax(0, 1fr); }
    .status,
    .selection-summary {
      min-width: 0;
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      line-height: var(--base-text-lineHeight-tight);
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .status { flex: 0 1 auto; }
    :host([pending]) fieldset { opacity: .78; }
    button:focus-visible, input:focus-visible, select:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-accent); outline-offset: var(--base-size-2); }
  `

  protected firstUpdated() {
    this.requestInitialOptions()
    this.resizeObserver = new ResizeObserver(([entry]) => {
      if (!entry) return
      this.applyResponsiveLayout(entry.contentRect.width, entry.contentRect.height)
    })
    this.resizeObserver.observe(this)
  }

  disconnectedCallback(): void {
    this.clearOptionRetry()
    this.resizeObserver?.disconnect()
    this.resizeObserver = undefined
    super.disconnectedCallback()
  }

  protected updated(changed: Map<PropertyKey, unknown>) {
    if (changed.has('options')) {
      if (this.options) {
        this.optionRequestAttempts = 0
        this.clearOptionRetry()
        this.optionLoading = false
      } else if (changed.get('options') !== undefined) {
        this.optionRequestAttempts = 0
        this.clearOptionRetry()
        this.optionLoading = this.hasRequestedOptions
        if (this.hasRequestedOptions && !this.stale) this.requestOptions()
      }
    }
    if (changed.has('stale') && changed.get('stale') === true && !this.stale) {
      if (this.hasRequestedOptions) this.requestOptions()
      else this.requestInitialOptions()
    }
    if (changed.has('presentation') || changed.has('definition')) {
      const rect = this.getBoundingClientRect()
      this.applyResponsiveLayout(rect.width, rect.height)
    }
  }

  render() {
    const definition = this.definition
    const binding = this.binding
    if (!definition || !binding) return nothing
    const presentation = this.presentation ?? defaultPresentation(definition)
    const label = presentation.title || binding.paneLabel || definition.label
    const operationalStatus = this.stale
      ? 'Waiting for current data'
      : this.optionLoading
        ? 'Loading values'
        : this.pending
          ? 'Updating'
          : undefined
    const selectionSummary = presentation.showSummary ? expressionSummary(this.expression) : undefined
    return html`
      <fieldset ?disabled=${!binding.readerEditable || this.stale} aria-busy=${String(this.pending)}>
        <legend class="visually-hidden">${label}</legend>
        ${this.showTitle || operationalStatus ? html`
          <div class="field-heading" data-title=${String(this.showTitle)}>
            ${this.showTitle ? html`<span class="field-title" aria-hidden="true" title=${label}>${label}</span>` : nothing}
            ${operationalStatus ? html`<span class="status" aria-live="polite" title=${operationalStatus}>${operationalStatus}</span>` : nothing}
          </div>
        ` : nothing}
        ${this.renderControl(presentation)}
        ${selectionSummary ? html`<span class="selection-summary" title=${selectionSummary}>${selectionSummary}</span>` : nothing}
      </fieldset>
    `
  }

  private renderControl(presentation: DashboardFilterPresentation) {
    switch (presentation.style) {
      case 'dropdown':
        return this.renderDropdown()
      case 'list':
        return this.renderCategorical(false)
      case 'buttons':
        return this.renderCategorical(true)
      case 'input':
        return this.renderInput()
      case 'numeric_range':
        return this.renderRange('number')
      case 'date_range':
        return this.renderRange('date')
      case 'relative_period':
        return this.renderRelative()
    }
  }

  private renderDropdown() {
    const selected = selectedValues(this.expression)
    return html`
      <select aria-label=${this.presentation?.ariaLabel || this.definition?.label || 'Filter'} @focus=${this.requestOptions} @change=${this.onDropdown}>
        <option value="">All</option>
        ${this.optionItems().map((option) => html`
          <option
            value=${valueKey(option.value)}
            ?selected=${selected.has(valueKey(option.value))}
            ?disabled=${!option.available && !option.selected}
          >${option.label}${option.count === undefined ? '' : ` (${option.count})`}</option>
        `)}
      </select>
    `
  }

  private renderCategorical(buttons: boolean) {
    const selected = selectedValues(this.expression)
    const multiple = this.binding?.selectionMode !== 'single'
    if (buttons) {
      return html`<div class="buttons" role="group" aria-label=${this.definition?.label ?? 'Filter options'}>
        ${this.optionItems().map((option) => html`
          <button
            type="button"
            aria-pressed=${String(selected.has(valueKey(option.value)))}
            ?disabled=${!option.available && !option.selected}
            @click=${() => this.toggleOption(option.value, multiple)}
          >${option.label}</button>
        `)}
      </div>`
    }
    return html`<div class="options" role=${multiple ? 'group' : 'radiogroup'}>
      ${this.optionItems().map((option) => html`
        <label class="option" data-unavailable=${String(!option.available)}>
          <input
            type=${multiple ? 'checkbox' : 'radio'}
            name=${this.binding?.key ?? 'filter'}
            .checked=${selected.has(valueKey(option.value))}
            ?disabled=${!option.available && !option.selected}
            @change=${() => this.toggleOption(option.value, multiple)}
          >
          <span>${option.label}${option.count === undefined ? '' : ` (${option.count})`}</span>
        </label>
      `)}
    </div>`
  }

  private renderInput() {
    const comparison = this.expression.kind === 'comparison' ? this.expression : undefined
    const operator = comparison?.operator ?? firstComparisonOperator(this.definition)
    return html`
      <div class="input-control">
        <span class="operator">${operatorLabel(operator)}</span>
        <input
          type=${this.definition?.valueKind === 'integer' || this.definition?.valueKind === 'decimal' ? 'number' : 'text'}
          .value=${comparison ? String(comparison.value.value) : ''}
          placeholder="Enter value"
          aria-label=${`${this.presentation?.ariaLabel || this.definition?.label || 'Filter value'}, ${operatorLabel(operator)}`}
          @change=${(event: Event) => {
            const value = (event.currentTarget as HTMLInputElement).value
            this.commit(value === '' ? unfiltered : {
              kind: 'comparison', operator, value: typedValue(this.definition!, value),
            })
          }}
        >
      </div>
    `
  }

  private renderRange(type: 'number' | 'date') {
    const range = this.expression.kind === 'range' ? this.expression : undefined
    return html`<div class="range">
      <label>
        <span class="field-label">${type === 'number' ? 'Minimum' : 'Start'}</span>
        <input
          type=${type}
          aria-label=${type === 'number' ? 'Minimum' : 'Start date'}
          .value=${range?.lower ? String(range.lower.value.value) : ''}
          @change=${this.onRange}
        >
      </label>
      <label>
        <span class="field-label">${type === 'number' ? 'Maximum' : 'End'}</span>
        <input
          type=${type}
          aria-label=${type === 'number' ? 'Maximum' : 'End date'}
          .value=${range?.upper ? String(range.upper.value.value) : ''}
          @change=${this.onRange}
        >
      </label>
    </div>`
  }

  private renderRelative() {
    const relative = this.expression.kind === 'relative_period' ? this.expression : undefined
    return html`<div class="relative">
      <select aria-label="Direction" data-relative="direction" @change=${this.onRelative}>
        ${['previous', 'current', 'next'].map((value) => html`<option value=${value} ?selected=${(relative?.direction ?? 'previous') === value}>${value}</option>`)}
      </select>
      <input type="number" min="1" max="1000" aria-label="Period count" data-relative="count" .value=${String(relative?.count ?? 1)} @change=${this.onRelative}>
      <select aria-label="Period unit" data-relative="unit" @change=${this.onRelative}>
        ${['day', 'week', 'month', 'quarter', 'year'].map((value) => html`<option value=${value} ?selected=${(relative?.unit ?? 'month') === value}>${value}</option>`)}
      </select>
    </div>`
  }

  private onDropdown = (event: Event) => {
    const key = (event.currentTarget as HTMLSelectElement).value
    if (!key) {
      this.commit(unfiltered)
      return
    }
    const option = this.optionItems().find((candidate) => valueKey(candidate.value) === key)
    if (option) this.commit(setExpression([option.value]))
  }

  private toggleOption(value: DashboardFilterValue, multiple: boolean) {
    const values = multiple ? [...selectedValueObjects(this.expression)] : []
    const key = valueKey(value)
    const next = values.some((item) => valueKey(item) === key)
      ? values.filter((item) => valueKey(item) !== key)
      : [...values, value]
    this.commit(next.length === 0 ? unfiltered : setExpression(next))
  }

  private onRange = () => {
    const inputs = [...this.renderRoot.querySelectorAll<HTMLInputElement>('.range input')]
    const [from, to] = inputs.map((input) => input.value)
    if (!from && !to) {
      this.commit(unfiltered)
      return
    }
    this.commit({
      kind: 'range',
      ...(from ? { lower: { value: typedValue(this.definition!, from), inclusive: true } } : {}),
      ...(to ? { upper: { value: typedValue(this.definition!, to), inclusive: true } } : {}),
    })
  }

  private onRelative = () => {
    const direction = this.renderRoot.querySelector<HTMLSelectElement>('[data-relative="direction"]')?.value ?? 'previous'
    const count = Number(this.renderRoot.querySelector<HTMLInputElement>('[data-relative="count"]')?.value ?? '1')
    const unit = this.renderRoot.querySelector<HTMLSelectElement>('[data-relative="unit"]')?.value ?? 'month'
    this.commit({
      kind: 'relative_period',
      direction: direction as 'previous' | 'current' | 'next',
      count: Number.isInteger(count) && count > 0 ? count : 1,
      unit: unit as 'minute' | 'hour' | 'day' | 'week' | 'month' | 'quarter' | 'year',
      includeCurrent: false,
      anchor: 'current_time',
    })
  }

  private commit(expression: DashboardFilterExpression) {
    if (!this.binding?.readerEditable || this.stale) return
    this.dispatchEvent(new CustomEvent<FilterMutationDetail>('lv-filter-mutate', {
      bubbles: true, composed: true,
      detail: { bindingKey: this.binding.key, expression },
    }))
  }

  private optionItems(): DashboardFilterOptionItem[] {
    const selected = selectedValues(this.expression)
    const base = this.options
      ? this.options.items
      : this.definition?.options.kind === 'static'
        ? this.definition.options.values.map((option) => ({
            ...option,
            selected: selected.has(valueKey(option.value)),
            available: true,
          }))
        : []
    const items = base.map((option) => ({
      ...option,
      selected: selected.has(valueKey(option.value)),
    }))
    const present = new Set(items.map((option) => valueKey(option.value)))
    for (const value of selectedValueObjects(this.expression)) {
      if (present.has(valueKey(value))) continue
      items.push({
        value,
        label: String(value.value),
        selected: true,
        available: false,
      })
    }
    return items
  }

  private requestInitialOptions() {
    const style = (this.presentation ?? (this.definition ? defaultPresentation(this.definition) : undefined))?.style
    if (style === 'list' || style === 'buttons') this.requestOptions()
  }

  private requestOptions = () => {
    this.optionRequestAttempts = 0
    this.loadOptions()
  }

  private loadOptions() {
    if (
      this.stale
      || !this.binding
      || !this.definition
      || this.definition.options.kind === 'none'
      || this.definition.options.kind === 'static'
    ) return
    this.hasRequestedOptions = true
    this.optionLoading = true
    this.optionRequestAttempts++
    this.dispatchEvent(new CustomEvent<FilterOptionsNeededDetail>('lv-filter-options-needed', {
      bubbles: true, composed: true,
      detail: {
        bindingKey: this.binding.key,
        search: '',
        limit: this.definition.options.limit || 50,
      },
    }))
    this.clearOptionRetry()
    if (this.optionRequestAttempts >= 2) return
    this.optionRetryTimer = setTimeout(() => {
      this.optionRetryTimer = undefined
      if (!this.options && !this.stale && this.optionLoading && this.isConnected) this.loadOptions()
    }, this.optionRetryDelay)
  }

  private clearOptionRetry() {
    if (this.optionRetryTimer !== undefined) clearTimeout(this.optionRetryTimer)
    this.optionRetryTimer = undefined
  }

  private applyResponsiveLayout(width: number, height: number): void {
    const presentation = this.presentation ?? (this.definition ? defaultPresentation(this.definition) : undefined)
    const contractID = presentation ? slicerContractID(presentation.style) : undefined
    if (!presentation || !contractID || width <= 0 || height <= 0) {
      delete this.dataset.layoutVariant
      delete this.dataset.layoutFit
      return
    }
    const resolution = resolveWidgetLayout(contractID, { width, height }, presentation.showSummary ? ['summary'] : [])
    const requirement = selectedRequirement(resolution)
    this.dataset.layoutVariant = requirement.layout
    this.dataset.layoutFit = resolution.kind === 'fit' ? 'fit' : 'too-small'
  }
}

abstract class FilterShell extends LitElement {
  @property({ attribute: false }) definition?: DashboardCompiledFilterDefinition
  @property({ attribute: false }) binding?: DashboardCompiledFilterBinding
  @property({ attribute: false }) expression: DashboardFilterExpression = unfiltered
  @property({ attribute: false }) options?: DashboardFilterOptionPage
  @property({ attribute: false }) presentation?: DashboardFilterPresentation
  @property({ type: Boolean, reflect: true }) pending = false
  @property({ type: Boolean, reflect: true }) stale = false
  @property({ type: Boolean, reflect: true }) active = false
  @property({ type: Boolean, reflect: true }) dirty = false

  protected leaf(showTitle = true) {
    return html`<lv-filter-leaf
      .definition=${this.definition}
      .binding=${this.binding}
      .expression=${this.expression}
      .options=${this.options}
      .presentation=${this.presentation}
      .pending=${this.pending}
      .stale=${this.stale}
      .showTitle=${showTitle}
    ></lv-filter-leaf>`
  }
}

export class DashboardFilterPaneCard extends FilterShell {
  static styles = css`
    :host { display: block; }
    section {
      display: grid;
      gap: var(--base-size-8);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      padding: var(--lv-space-control);
      background: var(--lv-bg-panel);
      transition: border-color var(--lv-duration-fast), background-color var(--lv-duration-fast);
    }
    :host([active]) section {
      border-color: var(--lv-line-accent);
      background: var(--lv-accent-muted);
    }
    .card-header {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
    }
    .title {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }
    .pending-badge {
      margin-left: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }
    .actions { display: flex; flex: 0 0 auto; gap: var(--base-size-4); }
    button {
      min-height: var(--lv-control-compact);
      border: 0;
      border-radius: var(--lv-radius-tight, var(--lv-radius-default));
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0 var(--base-size-6);
      font: var(--lv-type-caption);
    }
    button:hover:not(:disabled) { background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
    button:focus-visible {
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent);
      outline-offset: var(--base-size-2);
    }
    button:disabled { cursor: default; opacity: .45; }
  `

  render() {
    const label = this.binding?.paneLabel || this.definition?.label || 'Filter'
    const editable = this.binding?.readerEditable === true && !this.pending && !this.stale
    return html`
      <section aria-label=${label}>
        <div class="card-header">
          <span class="title">${label}${this.dirty ? html`<span class="pending-badge">Pending</span>` : nothing}</span>
          <div class="actions">
            <button
              type="button"
              aria-label=${`Clear ${label}`}
              title="Clear filter"
              ?disabled=${!editable || this.expression.kind === 'unfiltered'}
              @click=${this.clear}
            >Clear</button>
            <button
              type="button"
              aria-label=${`Reset ${label} to default`}
              title="Reset to default"
              ?disabled=${!editable || sameExpression(this.expression, this.binding?.default)}
              @click=${this.reset}
            >Reset</button>
          </div>
        </div>
        ${this.leaf(false)}
      </section>
    `
  }

  private clear = () => this.dispatchAction('lv-filter-clear')
  private reset = () => this.dispatchAction('lv-filter-reset-binding')

  private dispatchAction(type: 'lv-filter-clear' | 'lv-filter-reset-binding') {
    if (!this.binding?.key) return
    this.dispatchEvent(new CustomEvent(type, {
      bubbles: true,
      composed: true,
      detail: { bindingKey: this.binding.key },
    }))
  }
}

export class DashboardSlicer extends FilterShell {
  static styles = css`
    :host { display: block; height: 100%; }
    section { height: 100%; padding: 8px 10px; box-sizing: border-box; }
    lv-filter-leaf { display: block; width: 100%; height: 100%; }
  `

  render() {
    return html`<section aria-label=${this.presentation?.ariaLabel || this.definition?.label || 'Slicer'}>${this.leaf()}</section>`
  }
}

function slicerContractID(style: DashboardFilterPresentation['style']): WidgetContractID | undefined {
  switch (style) {
    case 'dropdown':
    case 'input':
    case 'numeric_range':
    case 'date_range':
    case 'relative_period':
      return `slicer.${style}`
    case 'list':
    case 'buttons':
      return undefined
  }
}

function selectedRequirement(resolution: WidgetLayoutResolution) {
  return resolution.kind === 'fit' ? resolution : resolution.requirements.at(-1)!
}

function defaultPresentation(definition: DashboardCompiledFilterDefinition): DashboardFilterPresentation {
  let style: DashboardFilterPresentation['style'] = 'input'
  if (definition.predicates.some((predicate) => predicate.kind === 'set')) style = 'dropdown'
  if (definition.predicates.some((predicate) => predicate.kind === 'range')) {
    style = definition.valueKind === 'date' || definition.valueKind === 'timestamp' ? 'date_range' : 'numeric_range'
  }
  if (definition.predicates.some((predicate) => predicate.kind === 'relative_period')) {
    style = 'relative_period'
  }
  return { style, search: false, selectAll: false, showCounts: false, showSummary: false, compact: false }
}

function firstComparisonOperator(definition?: DashboardCompiledFilterDefinition): 'equals' | 'not_equals' | 'contains' | 'not_contains' | 'starts_with' | 'ends_with' | 'greater_than' | 'greater_than_or_equal' | 'less_than' | 'less_than_or_equal' {
  const allowed = definition?.predicates.find((predicate) => predicate.kind === 'comparison')?.operators ?? []
  const operator = allowed[0] ?? 'equals'
  return operator as ReturnType<typeof firstComparisonOperator>
}

function operatorLabel(operator: ReturnType<typeof firstComparisonOperator>): string {
  return operator.replaceAll('_', ' ').replace(/^\w/, character => character.toUpperCase())
}

function sameExpression(left: DashboardFilterExpression, right?: DashboardFilterExpression): boolean {
  return JSON.stringify(left) === JSON.stringify(right ?? unfiltered)
}

function typedValue(definition: DashboardCompiledFilterDefinition, value: string): DashboardFilterValue {
  switch (definition.valueKind) {
    case 'boolean':
      return { kind: 'boolean', value: value === 'true' }
    case 'integer':
      return { kind: 'integer', value }
    case 'decimal':
      return { kind: 'decimal', value }
    case 'date':
      return { kind: 'date', value }
    case 'timestamp':
      return { kind: 'timestamp', value }
    default:
      return { kind: 'string', value }
  }
}

function setExpression(values: DashboardFilterValue[]): DashboardFilterExpression {
  return { kind: 'set', operator: 'in', values }
}

function selectedValueObjects(expression: DashboardFilterExpression): DashboardFilterValue[] {
  if (expression.kind === 'set') return expression.values
  if (expression.kind === 'comparison' && expression.operator === 'equals') return [expression.value]
  return []
}

function selectedValues(expression: DashboardFilterExpression): Set<string> {
  return new Set(selectedValueObjects(expression).map(valueKey))
}

function valueKey(value: DashboardFilterValue): string {
  return JSON.stringify(value)
}

export function expressionSummary(expression: DashboardFilterExpression): string {
  switch (expression.kind) {
    case 'unfiltered':
      return 'All values'
    case 'null_check':
      return expression.operator === 'is_null' ? 'Blank values' : 'Non-blank values'
    case 'set':
      return `${expression.values.length} selected`
    case 'comparison':
      return `${comparisonOperatorSymbol(expression.operator)} ${String(expression.value.value)}`
    case 'range':
      return `${expression.lower ? String(expression.lower.value.value) : '…'} – ${expression.upper ? String(expression.upper.value.value) : '…'}`
    case 'relative_period':
      return `${expression.direction} ${expression.count} ${expression.unit}`
  }
}

function comparisonOperatorSymbol(operator: Extract<DashboardFilterExpression, { kind: 'comparison' }>['operator']): string {
  switch (operator) {
    case 'equals': return '='
    case 'not_equals': return '≠'
    case 'greater_than': return '>'
    case 'greater_than_or_equal': return '≥'
    case 'less_than': return '<'
    case 'less_than_or_equal': return '≤'
    case 'contains': return 'contains'
    case 'not_contains': return 'does not contain'
    case 'starts_with': return 'starts with'
    case 'ends_with': return 'ends with'
  }
}

if (!customElements.get('lv-filter-leaf')) customElements.define('lv-filter-leaf', DashboardFilterLeaf)
if (!customElements.get('lv-filter-pane-card')) customElements.define('lv-filter-pane-card', DashboardFilterPaneCard)
if (!customElements.get('lv-slicer')) customElements.define('lv-slicer', DashboardSlicer)
