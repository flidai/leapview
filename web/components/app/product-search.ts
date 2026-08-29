import { LitElement, css, html, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Search, type IconNode } from 'lucide'
import { assetAccentColor, assetPresentation } from '../shared/asset-presentation'
import { lucideIcon } from '../shared/lucide-icons'
import {
  ProductSearchService,
  type ProductSearchItem,
} from './product-search-service'

class LeapViewProductSearch extends LitElement {
  @property({ type: Boolean }) open = false
  @state() private query = ''
  @state() private results: ProductSearchItem[] = []
  @state() private loading = false
  @state() private error = ''
  @state() private selectedIndex = 0
  private readonly service = new ProductSearchService()
  private searchTimer?: number
  private searchController?: AbortController

  static styles = css`
    :host {
      display: contents;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    dialog {
      box-sizing: border-box;
      width: min(40rem, calc(100vw - var(--base-size-32)));
      max-width: none;
      max-height: min(34rem, calc(100svh - var(--base-size-48)));
      margin: 12svh auto 0;
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-large, var(--lv-radius-default));
      background: var(--lv-bg-overlay);
      box-shadow: var(--lv-shadow-floating-lg);
      color: var(--lv-fg-default);
      padding: 0;
    }

    dialog::backdrop {
      background: var(--lv-modal-backdrop);
    }

    .search-shell {
      display: grid;
      max-height: inherit;
      grid-template-rows: auto minmax(0, 1fr) auto;
    }

    .search-field {
      display: grid;
      min-width: 0;
      grid-template-columns: var(--control-medium-size) minmax(0, 1fr) auto;
      align-items: center;
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-12);
    }

    .search-icon {
      display: grid;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      place-items: center;
      color: var(--lv-fg-muted);
    }

    input {
      min-width: 0;
      border: 0;
      outline: 0;
      background: transparent;
      color: var(--lv-fg-default);
      font: var(--lv-type-body-large);
      padding: 0;
    }

    input::placeholder {
      color: var(--lv-fg-subtle);
    }

    kbd {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      padding: var(--base-size-2) var(--base-size-6);
    }

    .results {
      min-height: 8rem;
      overflow-y: auto;
      padding: var(--base-size-8);
      scrollbar-gutter: stable;
    }

    .result {
      display: grid;
      min-width: 0;
      grid-template-columns: var(--base-size-20) minmax(0, 1fr) auto;
      gap: var(--base-size-4) var(--base-size-12);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      padding: var(--base-size-8) var(--base-size-12);
      text-decoration: none;
    }

    .result[aria-selected='true'],
    .result:hover,
    .result:focus-visible {
      background: var(--control-bgColor-hover);
      outline: none;
    }

    .result-label,
    .result-description {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .result-label {
      grid-column: 2;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .result-description,
    .result-kind,
    .empty {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .result-description {
      grid-column: 2 / -1;
    }

    .result-kind {
      grid-column: 3;
      align-self: center;
      white-space: nowrap;
    }

    .result-icon {
      display: grid;
      width: var(--base-size-20);
      height: var(--base-size-20);
      grid-column: 1;
      grid-row: 1 / span 2;
      place-items: center;
      color: var(--lv-fg-muted);
    }

    .result-icon svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .empty {
      margin: 0;
      padding: var(--base-size-24) var(--base-size-12);
      text-align: center;
    }

    .search-help {
      display: flex;
      min-height: var(--control-medium-size);
      align-items: center;
      gap: var(--base-size-12);
      border-top: var(--lv-border-muted);
      color: var(--lv-fg-subtle);
      font: var(--lv-type-caption);
      padding: var(--base-size-6) var(--base-size-12);
    }

    .search-help span:last-child {
      margin-left: auto;
    }

    @media (max-width: 640px) {
      dialog {
        width: calc(100vw - var(--base-size-16));
        max-height: calc(100svh - var(--base-size-16));
        margin-top: var(--base-size-8);
      }

      .search-help {
        display: none;
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    window.addEventListener('keydown', this.windowKeydown, true)
  }

  protected updated(changed: PropertyValues<this>): void {
    if (!changed.has('open')) return
    const dialog = this.shadowRoot?.querySelector('dialog')
    if (this.open) {
      this.cancelSearch()
      this.query = ''
      this.results = []
      this.loading = false
      this.error = ''
      this.selectedIndex = 0
      if (dialog && !dialog.open) dialog.showModal()
      void this.updateComplete.then(() => this.shadowRoot?.querySelector<HTMLInputElement>('input')?.focus())
      return
    }
    this.cancelSearch()
    if (dialog?.open) dialog.close()
  }

  disconnectedCallback(): void {
    window.removeEventListener('keydown', this.windowKeydown, true)
    this.cancelSearch()
    super.disconnectedCallback()
  }

  render() {
    return html`
      <dialog role="dialog" aria-modal="true" aria-label="Search LeapView" @cancel=${this.cancel} @click=${this.closeFromBackdrop}>
        <div class="search-shell">
          <label class="search-field">
            <span class="search-icon" aria-hidden="true">${icon(Search)}</span>
            <input
              type="search"
              aria-label="Search LeapView"
              aria-controls="product-search-results"
              aria-activedescendant=${this.results[this.selectedIndex]?.id ?? ''}
              placeholder="Search assets"
              autocomplete="off"
              .value=${this.query}
              @input=${this.inputChanged}
              @keydown=${this.inputKeydown}
            >
            <kbd aria-hidden="true">⌘K</kbd>
          </label>
          <div id="product-search-results" class="results" role="listbox" aria-label="Search results" aria-busy=${String(this.loading)}>
            ${this.results.map((result, index) => html`
              <a
                id=${result.id}
                class="result"
                role="option"
                aria-selected=${String(index === this.selectedIndex)}
                href=${result.href}
                @mouseenter=${() => { this.selectedIndex = index }}
                @focus=${() => { this.selectedIndex = index }}
                @click=${this.resultClicked}
              >
                <span class="result-icon" aria-hidden="true">${resultIcon(result.resourceKind)}</span>
                <span class="result-label">${result.label}</span>
                <span class="result-kind">${result.kind}</span>
                <span class="result-description">${result.description}</span>
              </a>
            `)}
            ${this.loading ? html`<p class="empty" role="status">Searching…</p>` : null}
            ${!this.loading && this.error ? html`<p class="empty" role="alert">${this.error}</p>` : null}
            ${!this.loading && !this.error && this.results.length === 0 ? html`
              <p class="empty">${this.query.trim()
                ? 'No matching assets'
                : 'Search dashboards, models, sources, connections, semantic models, and pipelines'}</p>
            ` : null}
          </div>
          <footer class="search-help" aria-hidden="true">
            <span>↑↓ Navigate</span>
            <span>↵ Open</span>
            <span>Esc Close</span>
          </footer>
        </div>
      </dialog>
    `
  }

  private inputChanged = (event: InputEvent): void => {
    this.query = (event.target as HTMLInputElement).value
    this.error = ''
    this.selectedIndex = 0
    window.clearTimeout(this.searchTimer)
    this.searchTimer = window.setTimeout(() => void this.loadResults(this.query), 180)
  }

  private async loadResults(query: string): Promise<void> {
    this.searchController?.abort()
    const controller = new AbortController()
    this.searchController = controller
    this.loading = query.trim().length > 0
    try {
      const results = await this.service.search(query, controller.signal)
      if (controller.signal.aborted) return
      this.results = results
      this.selectedIndex = Math.min(this.selectedIndex, Math.max(0, results.length - 1))
      this.error = ''
    } catch (error) {
      if (controller.signal.aborted) return
      this.results = []
      this.error = 'Search is temporarily unavailable'
    } finally {
      if (!controller.signal.aborted) this.loading = false
    }
  }

  private inputKeydown = (event: KeyboardEvent): void => {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      this.selectedIndex = Math.min(this.results.length - 1, this.selectedIndex + 1)
      this.scrollSelectedIntoView()
      return
    }
    if (event.key === 'ArrowUp') {
      event.preventDefault()
      this.selectedIndex = Math.max(0, this.selectedIndex - 1)
      this.scrollSelectedIntoView()
      return
    }
    if (event.key === 'Enter' && this.results[this.selectedIndex]) {
      event.preventDefault()
      window.location.assign(this.results[this.selectedIndex].href)
    }
  }

  private windowKeydown = (event: KeyboardEvent): void => {
    if (!this.open || event.key !== 'Escape') return
    event.preventDefault()
    event.stopPropagation()
    this.requestClose()
  }

  private scrollSelectedIntoView(): void {
    void this.updateComplete.then(() => this.shadowRoot?.getElementById(this.results[this.selectedIndex]?.id ?? '')?.scrollIntoView({ block: 'nearest' }))
  }

  private cancel = (event: Event): void => {
    event.preventDefault()
    this.requestClose()
  }

  private closeFromBackdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.requestClose()
  }

  private resultClicked = (): void => {
    this.requestClose()
  }

  private requestClose(): void {
    this.dispatchEvent(new CustomEvent('product-search-close', { bubbles: true, composed: true }))
  }

  private cancelSearch(): void {
    window.clearTimeout(this.searchTimer)
    this.searchController?.abort()
  }
}

function icon(node: IconNode) {
  return lucideIcon(node, { size: 18 })
}

function resultIcon(kind: string) {
  const presentation = assetPresentation(kind)
  return lucideIcon(presentation.icon, { color: assetAccentColor(kind), size: 16 })
}

customElements.define('lv-product-search', LeapViewProductSearch)
