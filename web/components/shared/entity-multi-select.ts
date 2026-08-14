import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Search, UserRound, Users } from 'lucide'
import { lucideIcon } from './lucide-icons'

export type EntityMultiSelectItem = {
  id: string
  label: string
  detail?: string
  kind?: 'principal' | 'group' | string
  disabled?: boolean
  disabledLabel?: string
}

export class EntityMultiSelect extends LitElement {
  @property({ attribute: false }) items: EntityMultiSelectItem[] = []
  @property({ attribute: false }) selectedIds: string[] = []
  @property() label = 'Options'
  @property() searchPlaceholder = 'Search...'
  @property() emptyMessage = 'No options available.'
  @property() noResultsMessage = 'No matching options.'
  @property({ type: Boolean }) disabled = false
  @property({ type: Boolean }) remoteSearch = false

  @state() private query = ''

  static styles = css`
    :host {
      display: grid;
      min-width: 0;
      gap: var(--base-size-8);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .toolbar {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
    }

    .search {
      display: flex;
      min-width: 0;
      min-height: var(--lv-control-medium);
      flex: 1 1 auto;
      align-items: center;
      gap: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
      padding: 0 var(--base-size-12);
    }

    .search:hover,
    .search:focus-within {
      border-color: var(--lv-line-accent);
      background: var(--lv-bg-control-hover);
    }

    .search-icon,
    .entity-icon {
      display: inline-flex;
      flex: 0 0 auto;
      align-items: center;
      justify-content: center;
    }

    .search-icon {
      width: var(--lv-icon-sm);
      height: var(--lv-icon-sm);
    }

    input {
      font: inherit;
    }

    input[type='search'] {
      min-width: 0;
      min-height: calc(var(--lv-control-medium) - var(--base-size-2));
      flex: 1 1 auto;
      border: 0;
      background: transparent;
      color: var(--lv-fg-default);
      outline: 0;
      padding: 0;
    }

    input[type='search']::placeholder {
      color: var(--lv-fg-muted);
    }

    .selection-count {
      flex: 0 0 auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      white-space: nowrap;
    }

    .list {
      display: grid;
      max-height: min(18rem, 42svh);
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
    }

    .item {
      display: grid;
      min-width: 0;
      grid-template-columns: auto var(--base-size-32) minmax(0, 1fr);
      align-items: center;
      gap: var(--lv-space-control);
      border-bottom: var(--lv-border-muted);
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: var(--lv-space-control) var(--base-size-12);
    }

    .item:last-child {
      border-bottom: 0;
    }

    .item:hover,
    .item:focus-within {
      background: var(--lv-bg-control-hover);
    }

    .item-disabled {
      cursor: not-allowed;
      opacity: var(--opacity-disabled);
    }

    input[type='checkbox'] {
      width: var(--base-size-16);
      height: var(--base-size-16);
      min-height: 0;
      margin: 0;
      accent-color: var(--lv-bg-accent);
    }

    .entity-icon {
      width: var(--base-size-32);
      height: var(--base-size-32);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
    }

    .entity-icon-group {
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-accent);
    }

    .copy {
      display: grid;
      min-width: 0;
      gap: var(--base-size-2);
    }

    .item-label,
    .item-detail {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .item-label {
      font: var(--lv-type-body-snug);
      font-weight: var(--base-text-weight-semibold);
    }

    .item-detail {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .empty {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-snug);
      padding: var(--base-size-20) var(--base-size-16);
      text-align: center;
    }

    @media (max-width: 30rem) {
      .toolbar {
        align-items: stretch;
        flex-direction: column;
      }

      .search,
      .selection-count {
        width: 100%;
        box-sizing: border-box;
      }
    }
  `

  render() {
    const normalizedQuery = this.query.trim().toLowerCase()
    const visibleItems = normalizedQuery && !this.remoteSearch
      ? this.items.filter((item) => `${item.label} ${item.detail ?? ''}`.toLowerCase().includes(normalizedQuery))
      : this.items
    const selected = new Set(this.selectedIds)
    return html`
      <div class="toolbar">
        <label class="search">
          <span class="search-icon" aria-hidden="true">${lucideIcon(Search, { size: 16 })}</span>
          <input
            type="search"
            autocomplete="off"
            aria-label=${`Search ${this.label.toLowerCase()}`}
            placeholder=${this.searchPlaceholder}
            .value=${this.query}
            ?disabled=${this.disabled}
            @input=${this.handleSearch}
          >
        </label>
        <span class="selection-count" aria-live="polite">${selected.size} selected</span>
      </div>
      ${this.items.length === 0
        ? html`<div class="empty">${this.emptyMessage}</div>`
        : visibleItems.length === 0
          ? html`<div class="empty">${this.noResultsMessage}</div>`
          : html`
            <div class="list" role="listbox" aria-label=${this.label} aria-multiselectable="true">
              ${visibleItems.map((item) => html`
                <label class=${item.disabled ? 'item item-disabled' : 'item'}>
                  <input
                    type="checkbox"
                    value=${item.id}
                    aria-label=${item.label}
                    .checked=${selected.has(item.id)}
                    ?disabled=${this.disabled || item.disabled}
                    @change=${(event: Event) => this.toggle(item.id, (event.currentTarget as HTMLInputElement).checked)}
                  >
                  <span class=${item.kind === 'group' ? 'entity-icon entity-icon-group' : 'entity-icon'} aria-hidden="true">
                    ${lucideIcon(item.kind === 'group' ? Users : UserRound, { size: 16 })}
                  </span>
                  <span class="copy">
                    <span class="item-label">${item.label}</span>
                    ${item.detail || item.disabledLabel ? html`<span class="item-detail">${item.disabledLabel || item.detail}</span>` : nothing}
                  </span>
                </label>
              `)}
            </div>
          `}
    `
  }

  clear(): void {
    this.query = ''
    this.selectedIds = []
  }

  private readonly handleSearch = (event: Event): void => {
    this.query = (event.currentTarget as HTMLInputElement).value
    this.dispatchEvent(new CustomEvent('lv-entity-search', {
      bubbles: true,
      composed: true,
      detail: { query: this.query },
    }))
  }

  private toggle(id: string, checked: boolean): void {
    const selected = new Set(this.selectedIds)
    if (checked) selected.add(id)
    else selected.delete(id)
    const visibleIDs = new Set(this.items.map((item) => item.id))
    const preserved = this.selectedIds.filter((selectedID) => selected.has(selectedID) && !visibleIDs.has(selectedID))
    this.selectedIds = [...preserved, ...this.items.filter((item) => selected.has(item.id)).map((item) => item.id)]
    this.dispatchEvent(new CustomEvent('lv-entity-selection-change', {
      bubbles: true,
      composed: true,
      detail: { selectedIds: [...this.selectedIds] },
    }))
  }
}

if (!customElements.get('lv-entity-multi-select')) customElements.define('lv-entity-multi-select', EntityMultiSelect)

declare global {
  interface HTMLElementTagNameMap {
    'lv-entity-multi-select': EntityMultiSelect
  }
}
