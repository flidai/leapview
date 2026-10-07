import { filterDockStyles } from './filter-dock.styles'
import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { RotateCcw, SlidersHorizontal, X } from 'lucide'
import type {
  DashboardCompiledFilterBinding,
  DashboardFilterContract,
  DashboardFilterExpression,
  DashboardFilterOptionPage,
  DashboardFilterState,
  DashboardStatus,
} from '../../../generated/signals'
import { lucideIcon } from '../../shared/lucide-icons'
import './filter-control'

class LeapViewFilterDock extends LitElement {
  @property({ attribute: false }) contract?: DashboardFilterContract
  @property({ attribute: false }) filterState?: DashboardFilterState
  @property({ attribute: false }) optionPages: Record<string, DashboardFilterOptionPage> = {}
  @property({ attribute: false }) optionContexts: Record<string, string> = {}
  @property({ type: Boolean }) optionRequestReady = true
  @property({ attribute: false }) pendingBindingKeys: string[] = []
  @property({ type: String }) pageId = ''
  @property({ type: Boolean, reflect: true }) loading: DashboardStatus['loading'] = false
  @property({ type: Boolean, reflect: true }) pending = false
  @property({ type: Boolean, attribute: false }) externalTrigger = false

  @property({ type: Boolean, reflect: true, attribute: 'data-open' })
  private open = storedFilterDockOpen()
  @state() private mobile = isMobileFilterDock()

  private mobileQuery: MediaQueryList | null = null
  private returnFocus?: HTMLElement

  connectedCallback(): void {
    super.connectedCallback()
    this.mobileQuery = window.matchMedia(mobileFilterDockQuery)
    this.mobile = this.mobileQuery.matches
    this.mobileQuery.addEventListener('change', this.onMobileQueryChange)
    window.addEventListener('keydown', this.onWindowKeyDown, true)
  }

  disconnectedCallback(): void {
    this.mobileQuery?.removeEventListener('change', this.onMobileQueryChange)
    this.mobileQuery = null
    window.removeEventListener('keydown', this.onWindowKeyDown, true)
    super.disconnectedCallback()
  }

  protected firstUpdated(): void {
    this.syncDialogMode()
    if (this.open) this.focusCloseButton()
    this.emitState()
  }

  static styles = filterDockStyles

  render() {
    const visibleBindings = this.visibleBindings()
    const activeCount = visibleBindings.filter(binding => this.isActive(this.expression(binding))).length
    const railSuppressed = this.mobile && this.externalTrigger
    return html`
      <aside ?data-open=${this.open} aria-label="Report filters" @keydown=${this.onKeyDown}>
        <button
          class="rail"
          type="button"
          title="Open filters"
          aria-label=${activeCount > 0 ? `Filters, ${activeCount} active` : 'Filters'}
          aria-expanded=${String(this.open)}
          aria-hidden=${railSuppressed ? 'true' : nothing}
          data-suppressed=${railSuppressed ? '' : nothing}
          tabindex=${railSuppressed ? '-1' : nothing}
          ?inert=${railSuppressed}
          @click=${this.toggle}
        >
          ${lucideIcon(SlidersHorizontal)}
          <span>Filters</span>
          ${activeCount > 0 ? html`<span class="rail-count" aria-hidden="true">${activeCount}</span>` : nothing}
        </button>
        <dialog
          class="panel"
          role=${this.mobile ? 'dialog' : 'region'}
          aria-label="Filters pane"
          aria-modal=${this.mobile && this.open ? 'true' : nothing}
          @cancel=${this.onDialogCancel}
        >
          ${this.contract && this.filterState ? this.renderCompiledPane(visibleBindings, activeCount) : html`
            <header class="panel-header">
              <strong>Filters</strong>
              <button class="icon-button close-button" type="button" aria-label="Close filters" @click=${this.close}>${lucideIcon(X)}</button>
            </header>
            <p class="panel-scroll" role="status">Filter state is unavailable.</p>
          `}
        </dialog>
      </aside>
    `
  }

  private renderCompiledPane(bindings: DashboardCompiledFilterBinding[], activeCount: number) {
    const reportBindings = bindings.filter(binding => binding.scope === 'report')
    const pageBindings = bindings.filter(binding => binding.scope === 'page')
    const resettableBindings = Object.values(this.contract?.bindings ?? {})
      .filter(binding => binding.readerEditable)
    const dashboardBindingKeys = resettableBindings
      .map(binding => binding.key)
      .sort()
    const pageBindingKeys = resettableBindings
      .filter(binding => binding.scope === 'page' && binding.pageID === this.pageId)
      .map(binding => binding.key)
      .sort()
    return html`
      <header class="panel-header">
        <div class="panel-heading">
          <strong>Filters</strong>
          <span class="panel-summary">${activeCount === 0 ? 'No active filters' : `${activeCount} active filter${activeCount === 1 ? '' : 's'}`}</span>
        </div>
        <button class="icon-button close-button" type="button" aria-label="Close filters" title="Close filters" @click=${this.close}>
          ${lucideIcon(X)}
        </button>
      </header>
      <div class="panel-scroll">
        ${this.renderGroup('Filters on all pages', reportBindings)}
        ${this.renderGroup('Filters on this page', pageBindings)}
      </div>
      <footer class="panel-footer">
        <div class="footer-row">
          <button
            class="footer-button reset"
            type="button"
            data-reset-scope="page"
            ?disabled=${pageBindingKeys.length === 0 || this.pending || this.loading}
            @click=${() => this.resetScope('page', pageBindingKeys)}
          >${lucideIcon(RotateCcw)} Reset page</button>
          <button
            class="footer-button reset"
            type="button"
            data-reset-scope="dashboard"
            ?disabled=${dashboardBindingKeys.length === 0 || this.pending || this.loading}
            @click=${() => this.resetScope('dashboard', dashboardBindingKeys)}
          >Reset all</button>
        </div>
        ${this.contract?.applicationMode === 'deferred' ? html`
          <div class="footer-row">
            <button
              class="footer-button"
              type="button"
              data-filter-cancel
              ?disabled=${this.dirtyCount === 0 || this.pending}
              @click=${this.cancel}
            >Cancel</button>
            <button
              class="footer-button primary"
              type="button"
              data-filter-apply
              ?disabled=${this.dirtyCount === 0 || this.pending}
              @click=${this.apply}
            >Apply ${this.dirtyCount > 0 ? `(${this.dirtyCount})` : ''}</button>
          </div>
        ` : nothing}
      </footer>
    `
  }

  private renderGroup(label: string, bindings: DashboardCompiledFilterBinding[]) {
    if (bindings.length === 0) return nothing
    return html`
      <section class="filter-group" aria-labelledby=${`filter-group-${label.replaceAll(' ', '-').toLowerCase()}`}>
        <div class="group-heading">
          <h2 class="group-title" id=${`filter-group-${label.replaceAll(' ', '-').toLowerCase()}`}>${label}</h2>
          <span class="group-count">${bindings.length}</span>
        </div>
        ${bindings.map(binding => {
          const definition = this.contract?.definitions[binding.filter]
          const expression = this.expression(binding)
          return html`<lv-filter-pane-card
            .definition=${definition}
            .binding=${binding}
            .expression=${expression}
            .options=${this.optionPages[binding.key]}
            .optionContext=${this.optionContexts[binding.key] ?? ''}
            .optionRequestReady=${this.optionRequestReady}
            .pending=${this.pendingBindingKeys.includes(binding.key)}
            .stale=${false}
            .active=${this.isActive(expression)}
            .dirty=${this.filterState?.dirtyBindings.includes(binding.key) ?? false}
          ></lv-filter-pane-card>`
        })}
      </section>
    `
  }

  private visibleBindings(): DashboardCompiledFilterBinding[] {
    return Object.values(this.contract?.bindings ?? {})
      .filter(binding => binding.paneVisible && (binding.scope === 'report' || binding.pageID === this.pageId))
      .sort((left, right) => left.paneOrder - right.paneOrder || left.key.localeCompare(right.key))
  }

  private expression(binding: DashboardCompiledFilterBinding): DashboardFilterExpression {
    return this.filterState?.draftControls[binding.key]
      ?? this.filterState?.appliedControls[binding.key]?.expression
      ?? binding.default
  }

  private isActive(expression: DashboardFilterExpression): boolean {
    return expression.kind !== 'unfiltered'
  }

  private get dirtyCount(): number {
    return this.filterState?.dirtyBindings.length ?? 0
  }

  public async openPanel(returnFocus?: HTMLElement): Promise<void> {
    if (this.open) return
    this.returnFocus = returnFocus
    this.open = true
    storeFilterDockOpen(true)
    await this.updateComplete
    this.syncDialogMode()
    this.focusCloseButton()
    this.emitState()
  }

  private toggle = async (): Promise<void> => {
    if (this.open) {
      await this.close()
      return
    }
    await this.openPanel(this.renderRoot.querySelector<HTMLButtonElement>('.rail') ?? undefined)
  }

  private close = async (): Promise<void> => {
    this.closeDialog()
    this.open = false
    storeFilterDockOpen(false)
    await this.updateComplete
    const focusTarget = this.returnFocus ?? this.renderRoot.querySelector<HTMLButtonElement>('.rail') ?? undefined
    this.returnFocus = undefined
    focusTarget?.focus({ preventScroll: true })
    this.emitState()
  }

  private emitState(): void {
    this.dispatchEvent(new CustomEvent('lv-filter-dock-state', {
      bubbles: true,
      composed: true,
      detail: { open: this.open },
    }))
  }

  private onKeyDown = (event: KeyboardEvent): void => {
    if (event.key !== 'Escape' || !this.open) return
    event.preventDefault()
    void this.close()
  }

  private onDialogCancel = (event: Event): void => {
    event.preventDefault()
    void this.close()
  }

  private onWindowKeyDown = (event: KeyboardEvent): void => {
    if (event.key !== 'Tab' || !this.open || !this.mobile) return
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('.panel')
    if (!dialog?.matches(':modal')) return
    this.trapFocus(event, dialog)
  }

  private onMobileQueryChange = (event: MediaQueryListEvent): void => {
    this.mobile = event.matches
    void this.updateComplete.then(() => {
      this.syncDialogMode()
      if (this.open) this.focusCloseButton()
    })
  }

  private syncDialogMode(): void {
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('.panel')
    if (!dialog) return
    if (!this.open) {
      if (dialog.open) dialog.close()
      return
    }
    if (this.mobile) {
      if (dialog.matches(':modal')) return
      if (dialog.open) dialog.close()
      dialog.showModal()
      return
    }
    if (dialog.matches(':modal')) dialog.close()
    if (!dialog.open) dialog.show()
  }

  private closeDialog(): void {
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('.panel')
    if (dialog?.open) {
      dialog.close()
    }
  }

  private trapFocus(event: KeyboardEvent, dialog: HTMLDialogElement): void {
    const focusable = deepFocusableElements(dialog)
    if (focusable.length === 0) {
      event.preventDefault()
      dialog.focus({ preventScroll: true })
      return
    }
    const active = deepActiveElement()
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    const activeInsideDialog = Boolean(active && focusable.includes(active))
    if (event.shiftKey && (!activeInsideDialog || active === first)) {
      event.preventDefault()
      last.focus({ preventScroll: true })
      return
    }
    if (!event.shiftKey && (!activeInsideDialog || active === last)) {
      event.preventDefault()
      first.focus({ preventScroll: true })
    }
  }

  private focusCloseButton(): void {
    this.renderRoot.querySelector<HTMLButtonElement>('.close-button')?.focus({ preventScroll: true })
  }

  private resetScope(scope: 'page' | 'dashboard', bindingKeys: string[]): void {
    this.dispatchEvent(new CustomEvent('lv-filter-reset-scope', {
      bubbles: true,
      composed: true,
      detail: { scope, bindingKeys: [...bindingKeys].sort() },
    }))
  }

  private apply = (): void => {
    this.dispatchEvent(new CustomEvent('lv-filter-apply', { bubbles: true, composed: true }))
  }

  private cancel = (): void => {
    this.dispatchEvent(new CustomEvent('lv-filter-cancel', { bubbles: true, composed: true }))
  }
}

const filterDockStorageKey = 'leapview:filters-open'
const mobileFilterDockQuery = '(max-width: 640px)'

function isMobileFilterDock(): boolean {
  return typeof window !== 'undefined' && window.matchMedia(mobileFilterDockQuery).matches
}

function deepFocusableElements(root: HTMLElement | ShadowRoot): HTMLElement[] {
  const elements: HTMLElement[] = []
  for (const child of root.children) {
    if (!(child instanceof HTMLElement)) continue
    if (isFocusable(child)) elements.push(child)
    if (child.shadowRoot) {
      elements.push(...deepFocusableElements(child.shadowRoot))
    } else {
      elements.push(...deepFocusableElements(child))
    }
  }
  return elements
}

function isFocusable(element: HTMLElement): boolean {
  if (!element.matches([
    'button:not([disabled])',
    'a[href]',
    'input:not([disabled])',
    'select:not([disabled])',
    'textarea:not([disabled])',
    '[tabindex]:not([tabindex="-1"])',
  ].join(','))) return false
  const style = getComputedStyle(element)
  return style.display !== 'none'
    && style.visibility !== 'hidden'
    && element.getClientRects().length > 0
}

function deepActiveElement(): HTMLElement | null {
  let active = document.activeElement
  while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement
  return active instanceof HTMLElement ? active : null
}

function storedFilterDockOpen(): boolean {
  try {
    return localStorage.getItem(filterDockStorageKey) === 'open'
  } catch {
    return false
  }
}

function storeFilterDockOpen(open: boolean): void {
  try {
    localStorage.setItem(filterDockStorageKey, open ? 'open' : 'closed')
  } catch {
    // The in-memory state is enough when storage is unavailable.
  }
}

if (!customElements.get('lv-filter-dock')) customElements.define('lv-filter-dock', LeapViewFilterDock)
