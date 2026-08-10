import { LitElement, css, html, nothing } from 'lit'
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
  @property({ type: String }) pageId = ''
  @property({ type: Boolean, reflect: true }) loading: DashboardStatus['loading'] = false
  @property({ type: Boolean, reflect: true }) pending = false

  @property({ type: Boolean, reflect: true, attribute: 'data-open' })
  private open = storedFilterDockOpen()
  @state() private mobile = isMobileFilterDock()

  private mobileQuery: MediaQueryList | null = null

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
  }

  static styles = css`
    :host {
      position: relative;
      z-index: var(--zIndex-sticky, 50);
      display: block;
      width: var(--lv-page-rail-width-collapsed);
      min-width: 0;
      min-height: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      transition: width var(--lv-duration-fast) var(--motion-easing-move);
    }

    :host([data-open]) {
      width: var(--lv-dashboard-filter-open-width);
    }

    aside {
      display: grid;
      width: var(--lv-page-rail-width-collapsed);
      box-sizing: border-box;
      min-width: 0;
      min-height: 0;
      height: 100%;
      overflow: hidden;
      border-left: var(--lv-border-default);
      background: var(--lv-bg-panel-muted);
      transition:
        width var(--lv-duration-fast) var(--motion-easing-move),
        background-color var(--lv-duration-fast) var(--motion-easing-move);
    }

    aside[data-open] {
      position: relative;
      width: 100%;
      border-left: var(--lv-border-default);
      background: var(--lv-bg-app);
    }

    button {
      font: inherit;
    }

    .rail {
      display: flex;
      width: 100%;
      height: 100%;
      min-width: 0;
      min-height: 0;
      box-sizing: border-box;
      align-items: center;
      align-content: start;
      flex-direction: column;
      justify-items: center;
      justify-content: flex-start;
      gap: var(--base-size-8);
      border: 0;
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: var(--base-size-16) 0;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .rail:hover {
      color: var(--lv-fg-default);
      background: var(--lv-bg-control-hover);
    }

    .rail:focus-visible,
    .icon-button:focus-visible,
    .footer-button:focus-visible {
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent);
      outline-offset: calc(-1 * var(--base-size-2));
    }

    aside[data-open] .rail {
      display: none;
    }

    .rail span {
      writing-mode: vertical-rl;
      line-height: 1;
    }

    .rail-count {
      display: grid;
      min-width: 18px;
      min-height: 18px;
      place-items: center;
      border-radius: var(--lv-radius-full);
      background: var(--lv-line-accent);
      color: var(--lv-fg-on-emphasis);
      font: var(--lv-type-caption);
      line-height: 1;
    }

    .panel {
      display: none;
      position: static;
      width: auto;
      height: auto;
      max-width: none;
      max-height: none;
      box-sizing: border-box;
      min-width: 0;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr) auto;
      overflow: hidden;
      margin: 0;
      border: 0;
      background: var(--lv-bg-app);
      color: inherit;
      padding: 0;
    }

    aside[data-open] .panel[open] {
      display: grid;
    }

    .panel::backdrop {
      background: transparent;
    }

    .panel-header,
    .panel-footer {
      position: relative;
      z-index: 1;
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-app);
      padding: var(--base-size-12);
    }

    .panel-header {
      justify-content: space-between;
      border-bottom: var(--lv-border-muted);
    }

    .panel-heading {
      display: grid;
      min-width: 0;
      gap: var(--base-size-2);
    }

    .panel-heading strong {
      font: var(--lv-type-section-title);
    }

    .panel-summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .icon-button {
      display: inline-grid;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      flex: 0 0 auto;
      place-items: center;
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
    }

    .icon-button:hover {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
    }

    .panel-scroll {
      min-width: 0;
      min-height: 0;
      overflow: auto;
      overscroll-behavior: contain;
      padding: var(--base-size-12);
    }

    .filter-group {
      display: grid;
      gap: var(--base-size-8);
    }

    .filter-group + .filter-group {
      margin-top: var(--base-size-16);
      padding-top: var(--base-size-16);
      border-top: var(--lv-border-muted);
    }

    .group-heading {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--base-size-8);
    }

    .group-title {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      letter-spacing: .02em;
      text-transform: uppercase;
    }

    .group-count {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .panel-footer {
      display: grid;
      border-top: var(--lv-border-muted);
    }

    .footer-row {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: flex-end;
      gap: var(--base-size-8);
    }

    .footer-row:first-child {
      justify-content: space-between;
    }

    .footer-button {
      min-height: var(--control-medium-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: 0 var(--lv-space-control);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .footer-button:hover:not(:disabled) {
      background: var(--lv-bg-control-hover);
    }

    .footer-button.primary {
      border-color: var(--lv-line-accent);
      background: var(--lv-line-accent);
      color: var(--lv-fg-on-emphasis);
    }

    .footer-button.reset {
      display: inline-flex;
      align-items: center;
      gap: var(--base-size-4);
      border-color: transparent;
      background: transparent;
      color: var(--lv-fg-muted);
    }

    .footer-button:disabled {
      cursor: default;
      opacity: .48;
    }

    @media (max-width: 640px) {
      :host,
      :host([data-open]) {
        z-index: var(--zIndex-modal, 200);
        width: 100%;
      }

      aside {
        width: 100%;
        border-left: 0;
        border-top: var(--lv-border-default);
      }

      aside[data-open] {
        position: fixed;
        inset: 0;
        width: 100%;
        height: 100dvh;
        border: 0;
        box-shadow: none;
      }

      .panel[open] {
        position: fixed;
        inset: 0;
        width: 100%;
        height: 100dvh;
      }

      .rail {
        min-height: 68px;
        height: auto;
        flex-direction: row;
        justify-content: center;
        padding: var(--base-size-12);
      }

      .rail span,
      aside[data-open] .rail span {
        writing-mode: horizontal-tb;
      }

      .panel-header,
      .panel-footer {
        padding:
          max(var(--base-size-12), env(safe-area-inset-top))
          max(var(--base-size-12), env(safe-area-inset-right))
          var(--base-size-12)
          max(var(--base-size-12), env(safe-area-inset-left));
      }

      .panel-footer {
        padding-top: var(--base-size-12);
        padding-bottom: max(var(--base-size-12), env(safe-area-inset-bottom));
      }

      .panel-scroll {
        padding: var(--base-size-12);
      }
    }

    @media (prefers-reduced-motion: reduce) {
      aside {
        transition: none;
      }
    }
  `

  render() {
    const visibleBindings = this.visibleBindings()
    const activeCount = visibleBindings.filter(binding => this.isActive(this.expression(binding))).length
    return html`
      <aside ?data-open=${this.open} aria-label="Report filters" @keydown=${this.onKeyDown}>
        <button
          class="rail"
          type="button"
          title="Open filters"
          aria-label=${activeCount > 0 ? `Filters, ${activeCount} active` : 'Filters'}
          aria-expanded=${String(this.open)}
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
            .pending=${this.pending}
            .stale=${this.loading}
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

  private toggle = async (): Promise<void> => {
    if (this.open) {
      await this.close()
      return
    }
    this.open = true
    storeFilterDockOpen(true)
    await this.updateComplete
    this.syncDialogMode()
    this.focusCloseButton()
  }

  private close = async (): Promise<void> => {
    this.closeDialog()
    this.open = false
    storeFilterDockOpen(false)
    await this.updateComplete
    this.renderRoot.querySelector<HTMLButtonElement>('.rail')?.focus({ preventScroll: true })
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
