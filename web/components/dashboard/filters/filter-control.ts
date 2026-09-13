import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronDown, Search } from 'lucide'
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
import { lucideIcon } from '../../shared/lucide-icons'
import { toggleAnchoredPopover } from '../../shared/anchored-popover'
import { formatDisplayDate } from './date-picker'
import { timestampDateBound } from './timestamp-date-range'
import { rangeDraftFromExpression, rangeValidationMessage, type RangeDraft } from './filter-range'
import { filterControlStyles } from './filter-control-styles'
import type { DatePickerInputDetail, DashboardDatePicker } from './date-picker'

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
const NULL_OPTION_KEY = '__lv_null_option__'
const maxRelativePeriodCount = 1000

export class DashboardFilterLeaf extends LitElement {
  @property({ attribute: false }) definition?: DashboardCompiledFilterDefinition
  @property({ attribute: false }) binding?: DashboardCompiledFilterBinding
  @property({ attribute: false }) expression: DashboardFilterExpression = unfiltered
  @property({ attribute: false }) options?: DashboardFilterOptionPage
  @property({ attribute: false }) presentation?: DashboardFilterPresentation
  @property({ attribute: false }) optionContext = ''
  @property({ type: Boolean }) optionRequestReady = true
  @property({ type: Boolean, reflect: true }) pending = false
  @property({ type: Boolean, reflect: true }) stale = false
  @property({ type: Boolean }) showTitle = true
  @property({ type: Boolean }) showClearAction = false
  @property({ type: Boolean }) parentRangeCommitBoundary = false
  @property({ type: Boolean }) autoHeight = false

  private hasRequestedOptions = false
  private optionDirty = true
  private requestedOptionContext = ''
  private requestedOptionCursor = ''
  private acceptedOptionRequestGeneration = 0
  private acceptedOptionServingStateID = ''
  private acceptedOptionQueryKey = ''
  private loadedOptionPage?: {
    queryKey: string
    items: DashboardFilterOptionItem[]
    complete: boolean
    nextCursor?: string
  }
  private resizeObserver?: ResizeObserver
  @state() private optionLoading = false
  @state() private rangeDraft?: RangeDraft
  @state() private rangeError = ''
  @state() private dropdownOpen = false
  @state() private dropdownSearch = ''
  private dropdownSearchTimer = 0
  private dropdownOpenOnPointerDown = false

  static styles = filterControlStyles

  protected firstUpdated() {
    this.requestInitialOptions()
    this.resizeObserver = new ResizeObserver(([entry]) => {
      if (!entry) return
      this.applyResponsiveLayout(entry.contentRect.width, entry.contentRect.height)
    })
    this.resizeObserver.observe(this)
  }

  disconnectedCallback(): void {
    if (this.dropdownSearchTimer) window.clearTimeout(this.dropdownSearchTimer)
    this.resizeObserver?.disconnect()
    this.resizeObserver = undefined
    super.disconnectedCallback()
  }

  protected updated(changed: Map<PropertyKey, unknown>) {
    if (changed.has('options')) {
      if (this.options) {
        if (
          this.acceptedOptionServingStateID
          && this.options.servingStateID !== this.acceptedOptionServingStateID
        ) {
          this.acceptedOptionRequestGeneration = 0
          this.loadedOptionPage = undefined
          this.acceptedOptionQueryKey = ''
        }
        const queryKey = this.optionQueryKey(this.dropdownSearch)
        const sameAcceptedPage = this.options.requestGeneration > 0
          && this.options.requestGeneration === this.acceptedOptionRequestGeneration
          && this.options.servingStateID === this.acceptedOptionServingStateID
          && queryKey === this.acceptedOptionQueryKey
        const staleOptionPage =
          this.options.requestGeneration > 0
          && this.acceptedOptionRequestGeneration > 0
          && this.options.requestGeneration < this.acceptedOptionRequestGeneration
        if (!sameAcceptedPage && !staleOptionPage) {
          const append = Boolean(this.requestedOptionCursor)
            && this.loadedOptionPage?.queryKey === queryKey
          const items = append
            ? mergeOptionItems(this.loadedOptionPage?.items ?? [], this.options.items)
            : [...this.options.items]
          this.loadedOptionPage = {
            queryKey,
            items,
            complete: this.options.complete,
            ...(this.options.nextCursor ? { nextCursor: this.options.nextCursor } : {}),
          }
          this.acceptedOptionRequestGeneration = Math.max(
            this.acceptedOptionRequestGeneration,
            this.options.requestGeneration,
          )
          this.acceptedOptionServingStateID = this.options.servingStateID
          this.acceptedOptionQueryKey = queryKey
          this.optionLoading = false
          this.optionDirty = false
          this.requestedOptionContext = ''
          this.requestedOptionCursor = ''
        }
      } else if (changed.get('options') !== undefined) {
        this.optionLoading = false
        this.optionDirty = true
        if (this.hasRequestedOptions && (this.visibleOptionsControl() || this.dropdownFocused())) this.requestOptions()
      }
    }
    if (changed.has('optionContext') && changed.get('optionContext') !== undefined) {
      this.optionDirty = true
      if (this.visibleOptionsControl() || this.dropdownFocused()) this.requestOptions()
    }
    if (
      (changed.has('optionRequestReady') && changed.get('optionRequestReady') === false && this.optionRequestReady)
      || (changed.has('stale') && changed.get('stale') === true && !this.stale)
    ) {
      if (this.visibleOptionsControl() || this.dropdownFocused()) this.requestOptions()
    }
    if (
      (changed.has('definition') || changed.has('binding') || changed.has('presentation'))
      && this.visibleOptionsControl()
    ) {
      this.requestOptions()
    }
    if (changed.has('presentation') || changed.has('definition') || changed.has('autoHeight')) {
      // Report canvases scale visually; layout contracts resolve against the untransformed CSS box.
      this.applyResponsiveLayout(this.clientWidth, this.clientHeight)
    }
    if (changed.has('expression') || changed.has('presentation') || changed.has('definition')) {
      this.syncRangeDraft()
    }
  }

  render() {
    const definition = this.definition
    const binding = this.binding
    if (!definition || !binding) return nothing
    const presentation = this.presentation ?? defaultPresentation(definition)
    const label = presentation.title || binding.paneLabel || definition.label
    const operationalStatus = this.optionLoading && !this.options ? 'Loading values' : undefined
    const selectionSummary = presentation.showSummary ? expressionSummary(this.expression) : undefined
    const active = this.expression.kind !== 'unfiltered'
    return html`
      <fieldset
        class=${`${presentation.style} ${this.autoHeight ? 'auto-height' : 'bounded'}`}
        ?disabled=${!binding.readerEditable}
        aria-busy=${String(this.pending || this.optionLoading)}
        @focusout=${this.onFilterFocusOut}
      >
        <legend class="visually-hidden">${label}</legend>
        ${this.showTitle || operationalStatus || this.showClearAction ? html`
          <div class="field-heading" data-title=${String(this.showTitle)}>
            ${this.showTitle ? html`<span class="field-title" aria-hidden="true" title=${label}>${label}</span>` : nothing}
            ${operationalStatus ? html`<span class="status" aria-live="polite" title=${operationalStatus}>${operationalStatus}</span>` : nothing}
            ${this.showClearAction ? html`
              <button
                class="filter-clear"
                type="button"
                data-active=${String(active)}
                aria-label=${`Clear ${label}`}
                aria-hidden=${String(!active)}
                tabindex=${active ? 0 : -1}
                ?disabled=${!active || !binding.readerEditable || this.stale}
                @click=${this.clearFilter}
              >Clear</button>
            ` : nothing}
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
    const selected = selectedOptionKeys(this.expression)
    const advanced = Boolean(this.presentation?.search || this.presentation?.selectAll || this.binding?.selectionMode !== 'single')
    if (advanced) return this.renderSearchableDropdown(selected)
    const selectedKey = selected.values().next().value ?? ''
    return html`
      <div class="simple-dropdown">
        <select
          aria-label=${this.presentation?.ariaLabel || this.definition?.label || 'Filter'}
          .value=${selectedKey}
          @focus=${this.requestOptions}
          @change=${this.onDropdown}
        >
          <option value="">All</option>
          ${this.optionItems().map((option) => html`
            <option
              value=${filterOptionKey(option)}
              ?selected=${selected.has(filterOptionKey(option))}
              ?disabled=${!option.available && !option.selected}
            >${option.label}${option.count === undefined ? '' : ` (${option.count})`}</option>
          `)}
        </select>
        ${this.renderLoadMoreOptions()}
      </div>
    `
  }

  private renderSearchableDropdown(selected: Set<string>) {
    const items = this.dropdownItems()
    const label = this.presentation?.ariaLabel || this.definition?.label || 'Filter'
    const multiple = this.binding?.selectionMode !== 'single'
    const summary = this.dropdownSummary(selected)
    return html`
      <button
        class="dropdown-trigger"
        type="button"
        aria-label=${`${label}: ${summary}`}
        aria-haspopup="dialog"
        aria-expanded=${String(this.dropdownOpen)}
        @pointerdown=${this.onDropdownTriggerPointerDown}
        @click=${this.toggleDropdown}
      >
        <span class="dropdown-value">${summary}</span>
        ${lucideIcon(ChevronDown)}
      </button>
      <div
        class="dropdown-popover"
        data-toolbar=${String(Boolean(this.presentation?.search || selected.size > 0))}
        popover="auto"
        role="dialog"
        aria-label=${`${label} filter options`}
        @toggle=${this.onDropdownToggle}
      >
        ${this.presentation?.search || selected.size > 0 ? html`<div class="dropdown-toolbar">
          ${this.presentation?.search ? html`
            <label class="dropdown-search">
              ${lucideIcon(Search)}
              <input
                type="search"
                placeholder="Search"
                aria-label=${`Search ${label}`}
                .value=${this.dropdownSearch}
                @input=${this.onDropdownSearch}
              >
            </label>
          ` : nothing}
          <button
            class="dropdown-clear"
            type="button"
            aria-label=${`Clear ${label} filter`}
            ?disabled=${selected.size === 0}
            @click=${this.clearDropdownSelection}
          >Clear filter</button>
        </div>` : nothing}
        <div class="dropdown-options" role="group" aria-label=${`${label} options`}>
          ${items.map((option) => html`
            <label class="dropdown-option" data-unavailable=${String(!option.available)}>
              <input
                type=${multiple ? 'checkbox' : 'radio'}
                name=${this.binding?.key ?? 'filter'}
                aria-label=${option.count === undefined ? option.label : `${option.label} (${option.count})`}
                .checked=${selected.has(filterOptionKey(option))}
                ?disabled=${!option.available && !option.selected}
                @change=${() => this.selectDropdownOption(option, multiple)}
              >
              <span class="dropdown-option-label">${option.label}</span>
              ${option.count === undefined ? nothing : html`<span class="dropdown-option-count">${option.count}</span>`}
            </label>
          `)}
          ${items.length === 0 ? html`<p class="dropdown-empty">${this.optionLoading ? 'Loading values...' : 'No values found'}</p>` : nothing}
          ${this.renderLoadMoreOptions()}
        </div>
      </div>
    `
  }

  private dropdownItems(): DashboardFilterOptionItem[] {
    const items = this.optionItems()
    const search = this.dropdownSearch.trim().toLocaleLowerCase()
    return search ? items.filter(option => option.label.toLocaleLowerCase().includes(search)) : items
  }

  private dropdownSummary(selected: Set<string>): string {
    if (selected.size === 0) return 'All'
    const selectedLabels = this.optionItems().filter(option => selected.has(filterOptionKey(option))).map(option => option.label)
    if (selected.size === 1) return selectedLabels[0] ?? '1 selected'
    return `${selected.size} selected`
  }

  private toggleDropdown = () => {
    const popover = this.renderRoot.querySelector<HTMLElement>('.dropdown-popover')
    const trigger = this.renderRoot.querySelector<HTMLElement>('.dropdown-trigger')
    if (!popover || !trigger) return
    const wasOpenOnPointerDown = this.dropdownOpenOnPointerDown
    this.dropdownOpenOnPointerDown = false
    if (wasOpenOnPointerDown) {
      if (popover.matches(':popover-open')) popover.hidePopover()
      this.dropdownOpen = false
      return
    }
    this.dropdownOpen = toggleAnchoredPopover(trigger, popover, {
      minWidth: this.presentation?.search ? 240 : 0,
    })
    if (!this.dropdownOpen) return
    this.requestOptions()
    queueMicrotask(() => this.renderRoot.querySelector<HTMLInputElement>('.dropdown-search input')?.focus())
  }

  private onDropdownTriggerPointerDown = (event: PointerEvent) => {
    if (event.button !== 0) return
    const popover = this.renderRoot.querySelector<HTMLElement>('.dropdown-popover')
    this.dropdownOpenOnPointerDown = Boolean(popover?.matches(':popover-open'))
  }

  private onDropdownToggle = (event: Event) => {
    this.dropdownOpen = (event as Event & { newState?: string }).newState === 'open'
    if (this.dropdownOpen || !this.dropdownSearch) return
    this.dropdownSearch = ''
    if (this.definition?.options.kind === 'distinct') this.optionDirty = true
  }

  private onDropdownSearch = (event: Event) => {
    this.dropdownSearch = (event.currentTarget as HTMLInputElement).value
    if (this.definition?.options.kind !== 'distinct') return
    this.optionDirty = true
    if (this.dropdownSearchTimer) window.clearTimeout(this.dropdownSearchTimer)
    this.dropdownSearchTimer = window.setTimeout(() => {
      this.dropdownSearchTimer = 0
      this.loadOptions(this.dropdownSearch)
    }, 180)
  }

  private clearDropdownSelection = () => {
    this.commit(unfiltered)
    if (this.binding?.selectionMode === 'single') this.renderRoot.querySelector<HTMLElement>('.dropdown-popover')?.hidePopover()
  }

  private selectDropdownOption(option: DashboardFilterOptionItem, multiple: boolean) {
    this.toggleOption(option, multiple)
    if (!multiple) this.renderRoot.querySelector<HTMLElement>('.dropdown-popover')?.hidePopover()
  }

  private renderCategorical(buttons: boolean) {
    const selected = selectedOptionKeys(this.expression)
    const multiple = this.binding?.selectionMode !== 'single'
    if (buttons) {
      return html`<div class="buttons" role="group" aria-label=${this.definition?.label ?? 'Filter options'}>
        ${this.optionItems().map((option) => html`
          <button
            type="button"
            aria-pressed=${String(selected.has(filterOptionKey(option)))}
            ?disabled=${!option.available && !option.selected}
            @click=${() => this.toggleOption(option, multiple)}
          >${option.label}</button>
        `)}
        ${this.renderLoadMoreOptions()}
      </div>`
    }
    return html`<div class="options" role=${multiple ? 'group' : 'radiogroup'}>
      ${this.optionItems().map((option) => html`
        <label class="option" data-unavailable=${String(!option.available)}>
          <input
            type=${multiple ? 'checkbox' : 'radio'}
            name=${this.binding?.key ?? 'filter'}
            aria-label=${option.count === undefined ? option.label : `${option.label} (${option.count})`}
            .checked=${selected.has(filterOptionKey(option))}
            ?disabled=${!option.available && !option.selected}
            @change=${() => this.toggleOption(option, multiple)}
          >
          <span>${option.label}${option.count === undefined ? '' : ` (${option.count})`}</span>
        </label>
      `)}
      ${this.renderLoadMoreOptions()}
    </div>`
  }

  private renderLoadMoreOptions() {
    if (!this.hasMoreOptions()) return nothing
    return html`
      <button
        class="load-more-options"
        type="button"
        ?disabled=${this.optionLoading}
        @click=${this.loadMoreOptions}
      >${this.optionLoading ? 'Loading more values...' : 'Load more values'}</button>
    `
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
    const draft = this.rangeDraft ?? rangeDraftFromExpression(this.expression, this.definition?.timezone)
    const invalid = this.rangeError !== ''
    return html`<div class="range">
      <label>
        <span class="field-label">${type === 'number' ? 'Minimum' : 'Start'}</span>
        ${type === 'date' ? html`
          <lv-date-picker
            .value=${draft.lower}
            label="Start date"
            placeholder="DD/MM/YYYY"
            .weekStart=${this.definition?.weekStart || 'monday'}
            .error=${invalid ? this.rangeError : ''}
            ?invalid=${invalid}
            ?disabled=${!this.binding?.readerEditable}
            @lv-date-input=${this.onRangeDateInput}
            @lv-date-escape=${this.onRangeEscape}
            @keydown=${this.onRangeKeyDown}
          ></lv-date-picker>
        ` : html`
          <input
            type="number"
            aria-label="Minimum"
            placeholder="No minimum"
            step=${this.definition?.valueKind === 'decimal' ? 'any' : nothing}
            aria-invalid=${String(invalid)}
            aria-describedby=${invalid ? 'range-error' : nothing}
            .value=${draft.lower}
            @input=${this.onRangeInput}
            @keydown=${this.onRangeKeyDown}
          >
        `}
      </label>
      <label>
        <span class="field-label">${type === 'number' ? 'Maximum' : 'End'}</span>
        ${type === 'date' ? html`
          <lv-date-picker
            .value=${draft.upper}
            label="End date"
            placeholder="DD/MM/YYYY"
            .weekStart=${this.definition?.weekStart || 'monday'}
            .error=${invalid ? this.rangeError : ''}
            ?invalid=${invalid}
            ?disabled=${!this.binding?.readerEditable}
            @lv-date-input=${this.onRangeDateInput}
            @lv-date-escape=${this.onRangeEscape}
            @keydown=${this.onRangeKeyDown}
          ></lv-date-picker>
        ` : html`
          <input
            type="number"
            aria-label="Maximum"
            placeholder="No maximum"
            step=${this.definition?.valueKind === 'decimal' ? 'any' : nothing}
            aria-invalid=${String(invalid)}
            aria-describedby=${invalid ? 'range-error' : nothing}
            .value=${draft.upper}
            @input=${this.onRangeInput}
            @keydown=${this.onRangeKeyDown}
          >
        `}
      </label>
      ${invalid ? html`<p class="range-error" id="range-error" role="alert">${this.rangeError}</p>` : nothing}
    </div>`
  }

  private renderRelative() {
    const relative = this.expression.kind === 'relative_period' ? this.expression : undefined
    return html`<div class="relative">
      <select aria-label="Direction" data-relative="direction" @change=${this.onRelative}>
        ${['previous', 'current', 'next'].map((value) => html`<option value=${value} ?selected=${(relative?.direction ?? 'previous') === value}>${value}</option>`)}
      </select>
      <input type="number" min="1" max=${maxRelativePeriodCount} step="1" aria-label="Period count" data-relative="count" .value=${String(relative?.count ?? 1)} @change=${this.onRelative}>
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
    const option = this.optionItems().find((candidate) => filterOptionKey(candidate) === key)
    if (!option) return
    if (option.null) {
      this.commit({ kind: 'null_check', operator: 'is_null' })
    } else if (option.value) {
      this.commit(setExpression([option.value]))
    }
  }

  private toggleOption(option: DashboardFilterOptionItem, multiple: boolean) {
    this.commit(filterOptionToggleExpression(this.expression, option, multiple))
  }

  private onRangeInput = () => {
    const inputs = [...this.renderRoot.querySelectorAll<HTMLInputElement>('.range input')]
    const [lower = '', upper = ''] = inputs.map((input) => input.value)
    this.rangeDraft = {
      lower,
      upper,
      baseExpression: this.rangeDraft?.baseExpression ?? expressionKey(this.expression),
      dirty: true,
    }
    this.rangeError = rangeValidationMessage(lower, upper, this.definition?.valueKind)
  }

  private onRangeDateInput = (event: CustomEvent<DatePickerInputDetail>) => {
    const pickers = [...this.renderRoot.querySelectorAll<DashboardDatePicker>('.range lv-date-picker')]
    const changed = event.currentTarget as DashboardDatePicker
    const changedIndex = pickers.indexOf(changed)
    if (changedIndex < 0) return
    const values = pickers.map(picker => picker.value)
    values[changedIndex] = event.detail.value
    const [lower = '', upper = ''] = values
    this.rangeDraft = {
      lower,
      upper,
      baseExpression: this.rangeDraft?.baseExpression ?? expressionKey(this.expression),
      dirty: true,
    }
    this.rangeError = rangeValidationMessage(lower, upper, this.definition?.valueKind)
  }

  private onRangeKeyDown = (event: KeyboardEvent) => {
    if (event.key === 'Escape') {
      event.preventDefault()
      this.discardRangeDraft()
      return
    }
    if (event.key !== 'Enter') return
    event.preventDefault()
    this.commitRangeDraft()
  }

  private onRangeEscape = () => {
    this.discardRangeDraft()
  }

  private onFilterFocusOut = (event: FocusEvent) => {
    if (this.parentRangeCommitBoundary) return
    const fieldset = event.currentTarget as HTMLFieldSetElement
    if (event.relatedTarget instanceof Node && fieldset.contains(event.relatedTarget)) return
    this.commitRangeDraft()
  }

  public commitRangeDraft() {
    if (!this.rangeDraft?.dirty || !this.rangePresentation()) return
    const { lower, upper } = this.rangeDraft
    const validation = rangeValidationMessage(lower, upper, this.definition?.valueKind)
    if (validation) {
      this.rangeError = validation
      return
    }
    let expression: DashboardFilterExpression
    try {
      expression = !lower && !upper ? unfiltered : {
        kind: 'range',
        ...(lower ? { lower: this.definition?.valueKind === 'timestamp'
          ? timestampDateBound(lower, this.definition.timezone, false)
          : { value: typedValue(this.definition!, lower), inclusive: true } } : {}),
        ...(upper ? { upper: this.definition?.valueKind === 'timestamp'
          ? timestampDateBound(upper, this.definition.timezone, true)
          : { value: typedValue(this.definition!, upper), inclusive: true } } : {}),
      }
    } catch (error) {
      this.rangeError = error instanceof Error ? error.message : 'Choose a valid date range.'
      return
    }
    this.rangeDraft = { lower, upper, baseExpression: expressionKey(expression), dirty: false }
    this.rangeError = ''
    if (!sameExpression(expression, this.expression)) this.commit(expression)
  }

  public discardRangeDraft() {
    this.rangeDraft = rangeDraftFromExpression(this.expression, this.definition?.timezone)
    this.rangeError = ''
  }

  private syncRangeDraft() {
    if (!this.rangePresentation()) {
      if (this.rangeDraft) this.rangeDraft = undefined
      if (this.rangeError) this.rangeError = ''
      return
    }
    const key = expressionKey(this.expression)
    if (this.rangeDraft?.dirty && this.rangeDraft.baseExpression === key) return
    const next = rangeDraftFromExpression(this.expression, this.definition?.timezone)
    if (
      this.rangeDraft?.lower === next.lower
      && this.rangeDraft.upper === next.upper
      && this.rangeDraft.baseExpression === next.baseExpression
      && !this.rangeDraft.dirty
    ) return
    this.rangeDraft = next
    this.rangeError = ''
  }

  private rangePresentation(): boolean {
    const style = (this.presentation ?? (this.definition ? defaultPresentation(this.definition) : undefined))?.style
    return style === 'numeric_range' || style === 'date_range'
  }

  private onRelative = () => {
    const direction = this.renderRoot.querySelector<HTMLSelectElement>('[data-relative="direction"]')?.value ?? 'previous'
    const input = this.renderRoot.querySelector<HTMLInputElement>('[data-relative="count"]')
    const count = normalizeRelativePeriodCount(Number(input?.value ?? '1'))
    if (input) input.value = String(count)
    const unit = this.renderRoot.querySelector<HTMLSelectElement>('[data-relative="unit"]')?.value ?? 'month'
    this.commit({
      kind: 'relative_period',
      direction: direction as 'previous' | 'current' | 'next',
      count,
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

  private clearFilter = () => {
    if (this.expression.kind === 'unfiltered') return
    this.rangeDraft = rangeDraftFromExpression(unfiltered)
    this.rangeError = ''
    this.commit(unfiltered)
  }

  private optionItems(): DashboardFilterOptionItem[] {
    const selected = selectedOptionKeys(this.expression)
    const base = this.options
      ? this.dynamicOptionItems()
      : this.definition?.options.kind === 'static'
        ? this.definition.options.values.map((option) => ({
            ...option,
            null: false,
            selected: selected.has(valueKey(option.value)),
            available: true,
          }))
        : []
    const items = base.map((option) => ({
      ...option,
      selected: selected.has(filterOptionKey(option)),
    }))
    const present = new Set(items.map(filterOptionKey))
    if (this.expression.kind === 'null_check' && this.expression.operator === 'is_null' && !present.has(NULL_OPTION_KEY)) {
      items.push({ null: true, label: '(null)', selected: true, available: false })
    }
    for (const value of selectedValueObjects(this.expression)) {
      if (present.has(valueKey(value))) continue
      items.push({
        value,
        null: false,
        label: String(value.value),
        selected: true,
        available: false,
      })
    }
    return items
  }

  private requestInitialOptions() {
    if (this.visibleOptionsControl()) this.requestOptions()
  }

  private requestOptions = () => {
    if (this.options && !this.optionDirty) return
    this.loadOptions()
  }

  private loadMoreOptions = () => {
    const cursor = this.loadedOptionPage?.queryKey === this.optionQueryKey(this.dropdownSearch)
      ? this.loadedOptionPage.nextCursor
      : undefined
    if (!cursor || this.optionLoading) return
    this.loadOptions(this.dropdownSearch, cursor)
  }

  private hasMoreOptions(): boolean {
    if (!this.options || this.definition?.options.kind !== 'distinct') return false
    if (this.loadedOptionPage?.queryKey !== this.optionQueryKey(this.dropdownSearch)) return false
    return Boolean(this.loadedOptionPage.nextCursor) && !this.loadedOptionPage.complete
  }

  private dynamicOptionItems(): DashboardFilterOptionItem[] {
    const queryKey = this.optionQueryKey(this.dropdownSearch)
    if (this.loadedOptionPage?.queryKey === queryKey) return this.loadedOptionPage.items
    return this.options?.items ?? []
  }

  private optionQueryKey(search: string): string {
    return `${this.optionContext}\u0000${search}`
  }

  private loadOptions(search = '', cursor?: string) {
    if (
      this.stale
      || !this.optionRequestReady
      || !this.binding
      || !this.definition
      || this.definition.options.kind === 'none'
      || this.definition.options.kind === 'static'
    ) return
    const requestContext = `${this.optionQueryKey(search)}\u0000${cursor ?? ''}`
    if (this.optionLoading && this.requestedOptionContext === requestContext) return
    this.hasRequestedOptions = true
    this.optionLoading = true
    this.requestedOptionContext = requestContext
    this.requestedOptionCursor = cursor ?? ''
    this.dispatchEvent(new CustomEvent<FilterOptionsNeededDetail>('lv-filter-options-needed', {
      bubbles: true, composed: true,
      detail: {
        bindingKey: this.binding.key,
        search,
        ...(cursor ? { cursor } : {}),
        limit: this.definition.options.limit || 50,
      },
    }))
  }

  private visibleOptionsControl(): boolean {
    const style = (this.presentation ?? (this.definition ? defaultPresentation(this.definition) : undefined))?.style
    return style === 'list' || style === 'buttons'
  }

  private dropdownFocused(): boolean {
    // Native popovers close synchronously; their toggle event is deferred.
    // Read the actual state so a just-closed control does not fetch again.
    return Boolean(this.renderRoot.querySelector('.dropdown-popover')?.matches(':popover-open'))
      || this.shadowRoot?.activeElement?.tagName === 'SELECT'
  }

  private applyResponsiveLayout(width: number, height: number): void {
    const presentation = this.presentation ?? (this.definition ? defaultPresentation(this.definition) : undefined)
    const contractID = presentation ? slicerContractID(presentation.style) : undefined
    if (!presentation || !contractID || width <= 0 || (!this.autoHeight && height <= 0)) {
      delete this.dataset.layoutVariant
      delete this.dataset.layoutFit
      return
    }
    // Pane cards grow with their content, so only width selects their arrangement.
    const resolution = resolveWidgetLayout(
      contractID,
      { width, height: this.autoHeight ? Number.POSITIVE_INFINITY : height },
      presentation.showSummary ? ['summary'] : [],
    )
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
  @property({ attribute: false }) optionContext = ''
  @property({ type: Boolean }) optionRequestReady = true
  @property({ type: Boolean, reflect: true }) pending = false
  @property({ type: Boolean, reflect: true }) stale = false
  @property({ type: Boolean, reflect: true }) active = false
  @property({ type: Boolean, reflect: true }) dirty = false

  protected leaf(showTitle = true, autoHeight = false, showClearAction = false, parentRangeCommitBoundary = false) {
    return html`<lv-filter-leaf
      .definition=${this.definition}
      .binding=${this.binding}
      .expression=${this.expression}
      .options=${this.options}
      .presentation=${this.presentation}
      .optionContext=${this.optionContext}
      .optionRequestReady=${this.optionRequestReady}
      .pending=${this.pending}
      .stale=${this.stale}
      .showTitle=${showTitle}
      .showClearAction=${showClearAction}
      .parentRangeCommitBoundary=${parentRangeCommitBoundary}
      .autoHeight=${autoHeight}
    ></lv-filter-leaf>`
  }
}

export class DashboardFilterPaneCard extends FilterShell {
  static styles = css`
    :host { display: block; min-width: 0; }
    section {
      display: grid;
      min-width: 0;
      box-sizing: border-box;
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
      align-items: start;
      justify-content: space-between;
      gap: var(--base-size-8);
    }
    .title {
      min-width: 0;
      overflow-wrap: anywhere;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }
    .pending-badge {
      margin-left: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }
    .actions:empty { display: none; }
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
    const editable = this.binding?.readerEditable === true && !this.stale
    const hasMeaningfulDefault = this.binding != null && this.binding.default.kind !== 'unfiltered'
    return html`
      <section aria-label=${label} @focusout=${this.onCardFocusOut}>
        <div class="card-header">
          <span class="title">${label}${this.dirty ? html`<span class="pending-badge">Pending</span>` : nothing}</span>
          <div class="actions">
            ${this.expression.kind !== 'unfiltered' ? html`<button
              type="button"
              aria-label=${`Clear ${label}`}
              title="Clear filter"
              ?disabled=${!editable}
              @click=${this.clear}
            >Clear</button>` : nothing}
            ${hasMeaningfulDefault ? html`
              <button
                type="button"
                aria-label=${`Reset ${label} to default`}
                title="Reset to default"
                ?disabled=${!editable || sameExpression(this.expression, this.binding?.default)}
                @click=${this.reset}
              >Reset</button>
            ` : nothing}
          </div>
        </div>
        ${this.leaf(false, true, false, true)}
      </section>
    `
  }

  private clear = () => {
    this.rangeLeaf()?.discardRangeDraft()
    this.dispatchAction('lv-filter-clear')
  }

  private reset = () => {
    this.rangeLeaf()?.discardRangeDraft()
    this.dispatchAction('lv-filter-reset-binding')
  }

  private onCardFocusOut = (event: FocusEvent) => {
    const section = event.currentTarget as HTMLElement
    if (event.relatedTarget instanceof Node && section.contains(event.relatedTarget)) return
    this.rangeLeaf()?.commitRangeDraft()
  }

  private rangeLeaf(): DashboardFilterLeaf | null {
    return this.renderRoot.querySelector<DashboardFilterLeaf>('lv-filter-leaf')
  }

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
    return html`<section aria-label=${this.presentation?.ariaLabel || this.definition?.label || 'Slicer'}>${this.leaf(true, false, true)}</section>`
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

function expressionKey(expression: DashboardFilterExpression): string {
  return JSON.stringify(expression)
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

function selectedOptionKeys(expression: DashboardFilterExpression): Set<string> {
  const keys = new Set(selectedValueObjects(expression).map(valueKey))
  if (expression.kind === 'null_check' && expression.operator === 'is_null') keys.add(NULL_OPTION_KEY)
  return keys
}

function valueKey(value?: DashboardFilterValue): string {
  return value === undefined ? '' : JSON.stringify(value)
}

function mergeOptionItems(previous: DashboardFilterOptionItem[], next: DashboardFilterOptionItem[]): DashboardFilterOptionItem[] {
  const merged = new Map<string, DashboardFilterOptionItem>()
  for (const option of [...previous, ...next]) {
    merged.set(filterOptionKey(option), option)
  }
  return [...merged.values()]
}

export function filterOptionKey(option: Pick<DashboardFilterOptionItem, 'null' | 'value'>): string {
  return option.null ? NULL_OPTION_KEY : valueKey(option.value)
}

export function normalizeRelativePeriodCount(value: number): number {
  return Number.isInteger(value) && value >= 1
    ? Math.min(maxRelativePeriodCount, value)
    : 1
}

export function filterOptionToggleExpression(
  expression: DashboardFilterExpression,
  option: DashboardFilterOptionItem,
  multiple: boolean,
): DashboardFilterExpression {
  // Null is a predicate, not a DashboardFilterValue. Since the expression
  // union cannot represent null alongside ordinary values, selecting null
  // replaces the current set and selecting a value replaces null. This is
  // deterministic for both single- and multi-select controls.
  if (option.null) {
    return expression.kind === 'null_check' && expression.operator === 'is_null'
      ? unfiltered
      : { kind: 'null_check', operator: 'is_null' }
  }
  if (!option.value) return expression
  const values = multiple ? [...selectedValueObjects(expression)] : []
  const key = valueKey(option.value)
  const next = values.some((item) => valueKey(item) === key)
    ? values.filter((item) => valueKey(item) !== key)
    : [...values, option.value]
  return next.length === 0 ? unfiltered : setExpression(next)
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
      return `${comparisonOperatorSymbol(expression.operator)} ${displayFilterValue(expression.value)}`
    case 'range':
      if (expression.lower && !expression.upper) return `≥ ${displayFilterValue(expression.lower.value)}`
      if (!expression.lower && expression.upper) return `≤ ${displayFilterValue(expression.upper.value)}`
      return `${expression.lower ? displayFilterValue(expression.lower.value) : '…'} – ${expression.upper ? displayFilterValue(expression.upper.value) : '…'}`
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

function displayFilterValue(value: DashboardFilterValue): string {
  const raw = String(value.value)
  if (value.kind === 'date' || value.kind === 'timestamp') {
    return formatDisplayDate(raw.slice(0, 10)) || raw
  }
  return raw
}

if (!customElements.get('lv-filter-leaf')) customElements.define('lv-filter-leaf', DashboardFilterLeaf)
if (!customElements.get('lv-filter-pane-card')) customElements.define('lv-filter-pane-card', DashboardFilterPaneCard)
if (!customElements.get('lv-slicer')) customElements.define('lv-slicer', DashboardSlicer)
