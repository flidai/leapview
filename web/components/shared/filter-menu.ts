import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Check, ChevronDown, Search, User, X } from 'lucide'
import type { FilterMenuSignal } from '../../generated/signals'
import { lucideIcon } from './lucide-icons'

type FilterMenuCommandAction = 'search' | 'toggle' | 'clear'

const emptyMenu: FilterMenuSignal = {
  id: '',
  label: '',
  summaryLabel: '',
  mode: 'multi',
  search: '',
  selected: [],
  options: [],
  loading: false,
  error: '',
  placeholder: 'Search',
  emptyLabel: 'No options found.',
}

class FilterMenu extends LitElement {
  @property({ attribute: false }) menu: FilterMenuSignal | null = null
  @state() private open = false
  @state() private draftSearch = ''
  private searchTimer: ReturnType<typeof setTimeout> | null = null
  private suppressNextClick = false

  static styles = css`
    :host {
      position: relative;
      display: inline-block;
      min-width: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .trigger {
      display: inline-flex;
      min-width: 0;
      min-height: var(--lv-control-medium);
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-body-compact);
      padding: 0 var(--lv-space-control);
      white-space: nowrap;
    }

    .trigger:hover,
    .trigger:focus-visible,
    .trigger[aria-expanded="true"] {
      border-color: var(--lv-border-accent);
      outline: 0;
    }

    .summary {
      overflow: hidden;
      max-width: 16rem;
      text-overflow: ellipsis;
    }

    .menu {
      position: absolute;
      z-index: 40;
      top: calc(100% + var(--base-size-6));
      left: 0;
      display: grid;
      width: min(22rem, calc(100vw - var(--base-size-24)));
      max-height: min(28rem, calc(100svh - var(--base-size-32)));
      grid-template-rows: auto minmax(0, 1fr) auto;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-lg);
    }

    .search {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr);
      align-items: center;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-8);
      color: var(--lv-fg-muted);
    }

    .search input {
      min-width: 0;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small);
      background: var(--lv-bg-input);
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      padding: var(--base-size-8) var(--lv-space-control);
    }

    .options {
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-4);
    }

    .option {
      display: grid;
      min-width: 0;
      grid-template-columns: auto auto minmax(0, 1fr) auto;
      align-items: center;
      gap: var(--base-size-8);
      border-radius: var(--lv-radius-small);
      cursor: pointer;
      padding: var(--lv-space-sm) var(--base-size-8);
      font: var(--lv-type-body);
    }

    .option:hover,
    .option:focus-within {
      background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    }

    .option input {
      width: var(--base-size-16);
      height: var(--base-size-16);
      margin: 0;
    }

    .option-icon {
      display: inline-flex;
      color: var(--lv-fg-muted);
    }

    .option-label {
      overflow: hidden;
      min-width: 0;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .option-description,
    .count,
    .empty,
    .error,
    .loading {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .option-description {
      display: block;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .empty,
    .error,
    .loading {
      padding: var(--base-size-12);
    }

    .error {
      color: var(--lv-fg-danger);
    }

    .footer {
      display: flex;
      justify-content: flex-end;
      border-top: var(--lv-border-muted);
      padding: var(--base-size-8);
    }

    .clear {
      display: inline-flex;
      min-height: var(--lv-control-small);
      align-items: center;
      gap: var(--base-size-6);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-caption);
      padding: 0 var(--base-size-8);
    }

    .clear:hover,
    .clear:focus-visible {
      background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
      outline: 0;
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    window.addEventListener('keydown', this.handleWindowKeydown)
  }

  disconnectedCallback(): void {
    if (this.searchTimer) clearTimeout(this.searchTimer)
    window.removeEventListener('keydown', this.handleWindowKeydown)
    super.disconnectedCallback()
  }

  updated(changed: Map<string, unknown>): void {
    if (changed.has('menu')) {
      this.draftSearch = this.currentMenu().search ?? ''
    }
  }

  render() {
    const menu = this.currentMenu()
    const summary = menu.summaryLabel || menu.label
    return html`
      <button
        type="button"
        class="trigger"
        aria-haspopup="menu"
        aria-expanded=${this.open ? 'true' : 'false'}
        @pointerdown=${this.handleTriggerPointerDown}
        @click=${this.handleTriggerClick}
        @keydown=${this.handleTriggerKeydown}
      >
        <span class="summary">${summary}</span>
        ${lucideIcon(ChevronDown, { size: 14, strokeWidth: 2 })}
      </button>
      ${this.open ? html`
        <div class="menu" role="menu" aria-label=${menu.label}>
          <label class="search">
            ${lucideIcon(Search, { size: 15, strokeWidth: 2 })}
            <input
              type="search"
              aria-label=${menu.label ? `Search ${menu.label}` : 'Search filter options'}
              placeholder=${menu.placeholder || 'Search'}
              .value=${this.draftSearch}
              @input=${this.handleSearchInput}
            >
          </label>
          <div class="options">
            ${menu.loading ? html`<div class="loading">Loading...</div>` : nothing}
            ${menu.error ? html`<div class="error">${menu.error}</div>` : nothing}
            ${!menu.loading && !menu.error && !menu.options?.length ? html`<div class="empty">${menu.emptyLabel || 'No options found.'}</div>` : nothing}
            ${!menu.loading && !menu.error ? menu.options?.map((option) => html`
              <label class="option">
                <input
                  type="checkbox"
                  aria-label=${option.label || option.value}
                  .checked=${Boolean(option.selected)}
                  ?disabled=${option.disabled}
                  @change=${() => this.emitCommand('toggle', { value: option.value })}
                >
                <span class="option-icon" aria-hidden="true">${this.renderOptionIcon(option.icon)}</span>
                <span class="option-label">
                  ${option.label || option.value}
                  ${option.description ? html`<span class="option-description">${option.description}</span>` : nothing}
                </span>
                ${option.countLabel ? html`<span class="count">${option.countLabel}</span>` : nothing}
              </label>
            `) : nothing}
          </div>
          <div class="footer">
            <button type="button" class="clear" ?disabled=${!menu.selected?.length} @click=${() => this.emitCommand('clear')}>
              ${lucideIcon(X, { size: 13, strokeWidth: 2 })}
              <span>Clear</span>
            </button>
          </div>
        </div>
      ` : nothing}
    `
  }

  private currentMenu(): FilterMenuSignal {
    return this.menu ?? emptyMenu
  }

  private handleTriggerPointerDown = (event: PointerEvent): void => {
    event.preventDefault()
    event.stopPropagation()
    this.suppressNextClick = true
    this.open = !this.open
  }

  private handleTriggerClick = (event: MouseEvent): void => {
    event.stopPropagation()
    if (this.suppressNextClick) {
      this.suppressNextClick = false
      return
    }
    this.open = !this.open
  }

  private handleTriggerKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    this.open = !this.open
  }

  private handleSearchInput = (event: Event): void => {
    this.draftSearch = (event.currentTarget as HTMLInputElement).value
    if (this.searchTimer) clearTimeout(this.searchTimer)
    this.searchTimer = setTimeout(() => {
      this.emitCommand('search', { search: this.draftSearch })
    }, 200)
  }

  private emitCommand(action: FilterMenuCommandAction, detail: { search?: string; value?: string } = {}): void {
    const menu = this.currentMenu()
    this.dispatchEvent(new CustomEvent('lv-filter-menu-command', {
      bubbles: true,
      composed: true,
      detail: {
        menuId: menu.id,
        action,
        search: detail.search ?? this.draftSearch,
        value: detail.value ?? '',
        selected: menu.selected ?? [],
      },
    }))
  }

  private renderOptionIcon(icon: string | undefined) {
    switch (icon) {
      case 'user':
        return lucideIcon(User, { size: 15, strokeWidth: 2 })
      case 'status':
        return lucideIcon(Check, { size: 15, strokeWidth: 2 })
      default:
        return nothing
    }
  }

  private handleWindowKeydown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape') this.open = false
  }
}

if (!customElements.get('lv-filter-menu')) customElements.define('lv-filter-menu', FilterMenu)
