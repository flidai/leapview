import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { Bookmark, RotateCcw } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import { loadSavedExplorations, type SavedExploration } from './saved-explorations'

/** The saved exploration control rendered in the Data Explorer header. */
class DataExplorerSaved extends LitElement {
  @state() private open = false
  @state() private items: SavedExploration[] = []
  @state() private loading = false
  @state() private error = ''
  private loaded = false
  private request = 0

  static styles = css`
    :host { display: block; }
    .saved-control { position: relative; }
    .icon-button {
      display: inline-grid;
      width: var(--control-small-size);
      height: var(--control-small-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      cursor: pointer;
    }
    .icon-button:hover, .icon-button:focus-visible { background: var(--lv-bg-control-hover); outline: 0; }
    .saved-button { display: inline-flex; width: auto; gap: var(--base-size-6); padding: 0 var(--base-size-8); }
    .saved-count {
      display: inline-grid;
      min-width: 1rem;
      height: 1rem;
      place-items: center;
      border-radius: 999px;
      background: var(--lv-bg-control-hover);
      padding: 0 var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-micro);
    }
    .saved-popover {
      position: absolute;
      top: calc(100% + var(--base-size-6));
      right: 0;
      z-index: var(--zIndex-overlay, 10);
      display: grid;
      width: min(23rem, calc(100vw - 2rem));
      max-height: min(28rem, calc(100svh - 5rem));
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
    }
    .saved-popover-header {
      display: flex;
      min-height: var(--control-large-size);
      align-items: center;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-6) var(--base-size-8) var(--base-size-6) var(--base-size-12);
    }
    .saved-popover-header h2 { flex: 1; margin: 0; color: var(--lv-fg-default); font: var(--lv-type-section-title); }
    .saved-state { margin: 0; padding: var(--base-size-12); color: var(--lv-fg-muted); font: var(--lv-type-body); }
    .saved-error { color: var(--lv-fg-danger); }
    .saved-list { min-height: 0; overflow: auto; padding: var(--base-size-4); }
    .saved-item { display: grid; gap: var(--base-size-2); border-radius: var(--lv-radius-default); padding: var(--base-size-8); color: var(--lv-fg-default); text-decoration: none; }
    .saved-item:hover, .saved-item:focus-visible { background: var(--lv-bg-control-hover); outline: 0; }
    .saved-item-title { overflow: hidden; font: var(--lv-type-body); font-weight: var(--base-text-weight-medium); text-overflow: ellipsis; white-space: nowrap; }
    .saved-item time { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  `

  connectedCallback(): void {
    super.connectedCallback()
    if (!this.loaded && !this.loading) void this.refresh()
  }

  render() {
    return html`
      <div class="saved-control">
        <button
          type="button"
          class="icon-button saved-button"
          aria-label="Saved explorations"
          aria-haspopup="dialog"
          aria-expanded=${String(this.open)}
          title="Saved explorations"
          @click=${this.toggle}
        >${lucideIcon(Bookmark, { size: 15 })}<span>Saved</span>${this.items.length ? html`<span class="saved-count">${this.items.length}</span>` : nothing}</button>
        ${this.open ? html`
          <section class="saved-popover" role="dialog" aria-label="Saved explorations" @keydown=${this.handleKeydown}>
            <header class="saved-popover-header">
              <h2>Saved explorations</h2>
              <button type="button" class="icon-button saved-refresh" aria-label="Refresh saved explorations" title="Refresh saved explorations" ?disabled=${this.loading} @click=${() => this.refresh()}>${lucideIcon(RotateCcw, { size: 14 })}</button>
            </header>
            ${this.loading && !this.items.length ? html`<p class="saved-state" role="status">Loading saved explorations…</p>` : nothing}
            ${this.error ? html`<p class="saved-state saved-error" role="alert">${this.error}</p>` : nothing}
            ${!this.loading && !this.error && !this.items.length ? html`<p class="saved-state">No saved explorations yet.</p>` : nothing}
            ${this.items.length ? html`
              <nav class="saved-list" aria-label="Saved explorations">
                ${this.items.map((item) => html`
                  <a class="saved-item" href=${item.href} @click=${() => this.open = false}>
                    <span class="saved-item-title">${item.title}</span>
                    <time datetime=${item.updatedAt}>${formatSavedDate(item.updatedAt)}</time>
                  </a>
                `)}
              </nav>
            ` : nothing}
          </section>
        ` : nothing}
      </div>
    `
  }

  private toggle() {
    this.open = !this.open
    if (this.open && !this.loaded && !this.loading) void this.refresh()
  }

  private async refresh() {
    const request = ++this.request
    this.loading = true
    this.error = ''
    try {
      const items = await loadSavedExplorations()
      if (request !== this.request) return
      this.items = items
      this.loaded = true
    } catch (error) {
      if (request !== this.request) return
      this.error = error instanceof Error ? error.message : 'Saved explorations could not be loaded.'
    } finally {
      if (request === this.request) this.loading = false
    }
  }

  private handleKeydown(event: KeyboardEvent) {
    if (event.key !== 'Escape') return
    event.stopPropagation()
    this.open = false
    this.renderRoot.querySelector<HTMLButtonElement>('.saved-button')?.focus()
  }
}

if (!customElements.get('lv-data-explorer-saved')) {
  customElements.define('lv-data-explorer-saved', DataExplorerSaved)
}

function formatSavedDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return 'Saved exploration'
  return `Updated ${new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(date)}`
}
