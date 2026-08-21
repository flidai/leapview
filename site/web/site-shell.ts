import { LitElement, css, html } from 'lit'
import { Menu, Monitor, Moon, Search, Sun, X } from 'lucide'
import { DatastarLit } from '../../web/components/shared/datastar-lit'
import { lucideIcon } from '../../web/components/shared/lucide-icons'

type ThemeMode = 'system' | 'light' | 'dark'

const nextThemeMode: Record<ThemeMode, ThemeMode> = {
  system: 'light',
  light: 'dark',
  dark: 'system',
}

const themeLabels: Record<ThemeMode, string> = {
  system: 'System theme',
  light: 'Light theme',
  dark: 'Dark theme',
}

class SiteThemeToggle extends LitElement {
  private themeMode: ThemeMode = currentThemeMode()
  private readonly handleThemeApplied = (event: Event) => {
    this.themeMode = normalizeThemeMode((event as CustomEvent<{ mode?: string }>).detail?.mode)
    this.requestUpdate()
  }

  static styles = css`
    :host {
      display: block;
    }

    button {
      display: inline-grid;
      width: var(--site-interactive-target-size);
      height: var(--site-interactive-target-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: inherit;
    }

    button:hover,
    button:focus-visible {
      border-color: var(--lv-button-border-hover);
      background: var(--lv-button-bg-hover);
      color: var(--lv-fg-default);
    }

    button:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    [hidden] {
      display: none;
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('leapview-theme-applied', this.handleThemeApplied)
  }

  disconnectedCallback(): void {
    document.removeEventListener('leapview-theme-applied', this.handleThemeApplied)
    super.disconnectedCallback()
  }

  render() {
    const nextMode = nextThemeMode[this.themeMode]
    const label = `${themeLabels[this.themeMode]}. Switch to ${themeLabels[nextMode]}.`
    return html`<button type="button" data-theme-toggle data-theme-mode=${this.themeMode} aria-label=${label} title=${label} @click=${this.toggleTheme}>
      <span data-theme-icon="system" ?hidden=${this.themeMode !== 'system'}>${lucideIcon(Monitor)}</span>
      <span data-theme-icon="light" ?hidden=${this.themeMode !== 'light'}>${lucideIcon(Sun)}</span>
      <span data-theme-icon="dark" ?hidden=${this.themeMode !== 'dark'}>${lucideIcon(Moon)}</span>
    </button>`
  }

  private toggleTheme(): void {
    const nextMode = nextThemeMode[this.themeMode]
    this.themeMode = nextMode
    this.requestUpdate()
    document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: nextMode } }))
  }
}

if (!customElements.get('lv-site-theme-toggle')) {
  customElements.define('lv-site-theme-toggle', SiteThemeToggle)
}

class SiteMobileMenu extends LitElement {
  private open = false
  showcase = false

  static properties = {
    showcase: { type: Boolean },
  }

  static styles = css`
    :host {
      display: none;
    }

    @media (width < 48rem) {
      :host {
        display: block;
      }
    }

    button {
      display: inline-grid;
      width: var(--site-interactive-target-size);
      height: var(--site-interactive-target-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: inherit;
    }

    button:hover,
    button:focus-visible {
      border-color: var(--lv-button-border-hover);
      background: var(--lv-button-bg-hover);
      color: var(--lv-fg-default);
    }

    button:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    nav {
      position: fixed;
      z-index: var(--zIndex-overlay);
      top: calc(var(--site-header-height) + var(--base-size-8));
      right: var(--base-size-16);
      display: grid;
      min-width: calc(var(--base-size-128) + var(--base-size-64));
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      box-shadow: var(--shadow-floating-medium);
    }

    a {
      display: flex;
      min-height: var(--control-minTarget-auto);
      align-items: center;
      padding: var(--base-size-12) var(--base-size-16);
      color: var(--lv-fg-default);
      font-size: var(--text-body-size-medium);
      font-weight: var(--base-text-weight-medium);
      text-decoration: none;
    }

    a:hover,
    a:focus-visible {
      background: var(--lv-bg-control);
      color: var(--lv-fg-accent);
    }

    nav[hidden] {
      display: none;
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('keydown', this.handleKeydown)
  }

  disconnectedCallback(): void {
    document.removeEventListener('keydown', this.handleKeydown)
    super.disconnectedCallback()
  }

  render() {
    const label = this.open ? 'Close site navigation' : 'Open site navigation'
    return html`<button type="button" aria-label=${label} aria-controls="site-mobile-navigation" aria-expanded=${String(this.open)} @click=${this.toggle}>${lucideIcon(this.open ? X : Menu, { size: 20, strokeWidth: 2 })}</button>
      <nav id="site-mobile-navigation" aria-label="Site navigation" ?hidden=${!this.open}>
        <a href="/docs" @click=${this.close}>Docs</a>
        <a href="/docs/search" @click=${this.close}>Search</a>
        <a href="/visuals" @click=${this.close}>Visuals</a>
        ${this.showcase ? html`<a href="/showcase" @click=${this.close}>Live demo</a>` : null}
      </nav>`
  }

  private toggle = (): void => {
    this.open = !this.open
    this.requestUpdate()
  }

  private close = (): void => {
    this.open = false
    this.requestUpdate()
  }

  private readonly handleKeydown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape' && this.open) this.close()
  }
}

if (!customElements.get('lv-site-mobile-menu')) {
  customElements.define('lv-site-mobile-menu', SiteMobileMenu)
}

type SiteSearchResult = {
  href: string
  summary: string
  title: string
}

type SiteSearchState = {
  loading?: boolean
  query: string
  resultQuery?: string
  results: SiteSearchResult[]
  total: number
}

const emptySiteSearch: SiteSearchState = { query: '', results: [], total: 0 }

class SiteSearch extends DatastarLit(LitElement) {
  private readonly handleGlobalKeydown = (event: KeyboardEvent): void => {
    const target = event.target as HTMLElement | null
    const editing = target?.matches('input, textarea, select, [contenteditable="true"]') ?? false
    const commandShortcut = (event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k'
    const slashShortcut = event.key === '/' && !event.metaKey && !event.ctrlKey && !event.altKey && !editing
    if (event.defaultPrevented || event.repeat || (!commandShortcut && !slashShortcut)) return

    event.preventDefault()
    this.openDialog()
  }

  static styles = css`
    :host {
      display: block;
    }

    slot:not([name]) {
      display: none;
    }

    button {
      font: inherit;
    }

    .trigger {
      display: inline-flex;
      min-height: var(--site-interactive-target-size);
      align-items: center;
      gap: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding-inline: var(--base-size-12) var(--base-size-8);
      font-size: var(--text-body-size-small);
      font-weight: var(--base-text-weight-medium);
    }

    .trigger:hover,
    .trigger:focus-visible {
      border-color: var(--lv-button-border-hover);
      background: var(--lv-button-bg-hover);
      color: var(--lv-fg-default);
    }

    button:focus-visible,
    ::slotted(.site-search-active-input:focus-visible) {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    kbd {
      border: var(--lv-border-muted);
      border-radius: var(--borderRadius-small);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
      padding: var(--base-size-2) var(--base-size-4);
      font-family: var(--fontStack-monospace);
      font-size: var(--text-caption-size);
      line-height: 1;
    }

    dialog {
      width: min(calc(100vw - var(--base-size-32)), calc(var(--base-size-128) * 5));
      max-width: none;
      margin: min(18vh, calc(var(--base-size-128) + var(--base-size-32))) auto auto;
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      box-shadow: var(--shadow-floating-large);
      padding: 0;
    }

    dialog::backdrop {
      background: var(--bgColor-black);
      opacity: 0.45;
    }

    .panel {
      display: grid;
      gap: var(--base-size-16);
      padding: var(--base-size-20);
    }

    header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-16);
    }

    h2 {
      margin: 0;
      font-size: var(--text-title-size-medium);
    }

    .close {
      display: inline-grid;
      width: var(--control-minTarget-auto);
      height: var(--control-minTarget-auto);
      place-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
    }

    .close:hover,
    .close:focus-visible {
      background: var(--lv-button-bg-hover);
      color: var(--lv-fg-default);
    }

    .controls {
      display: flex;
      gap: var(--base-size-8);
    }

    ::slotted(.site-search-active-input) {
      display: block;
      width: 100%;
      min-width: 0;
      min-height: var(--control-minTarget-auto);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      padding: var(--control-medium-paddingBlock) var(--control-medium-paddingInline-normal);
      font: inherit;
    }

    .results {
      max-height: min(50vh, calc(var(--base-size-128) * 3));
      overflow-y: auto;
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-12);
    }

    .status {
      margin: 0;
      color: var(--lv-fg-muted);
      font-size: var(--text-body-size-small);
    }

    ul {
      display: grid;
      gap: var(--base-size-4);
      margin: var(--base-size-8) 0 0;
      padding: 0;
      list-style: none;
    }

    a {
      display: grid;
      gap: var(--base-size-4);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      padding: var(--base-size-12);
      text-decoration: none;
    }

    a:hover,
    a:focus-visible {
      background: var(--lv-button-bg-hover);
    }

    a:focus-visible {
      outline: var(--focus-outline);
      outline-offset: calc(var(--focus-outline-offset) * -1);
    }

    a strong {
      font-size: var(--text-body-size-medium);
    }

    a span {
      display: -webkit-box;
      overflow: hidden;
      color: var(--lv-fg-muted);
      font-size: var(--text-body-size-small);
      line-height: var(--base-text-lineHeight-relaxed);
      -webkit-box-orient: vertical;
      -webkit-line-clamp: 2;
    }

    @media (width < 30rem) {
      .trigger kbd {
        display: none;
      }
      .panel {
        padding: var(--base-size-16);
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('keydown', this.handleGlobalKeydown)
  }

  disconnectedCallback(): void {
    document.removeEventListener('keydown', this.handleGlobalKeydown)
    super.disconnectedCallback()
  }

  render() {
    const state = this.signal<SiteSearchState>('docsSearch', emptySiteSearch)
    const query = state.query?.trim() ?? ''
    const results = Array.isArray(state.results) ? state.results : []
    const total = Number.isFinite(state.total) ? state.total : 0
    const loading = Boolean(state.loading) || (query !== '' && state.resultQuery !== query)

    return html`<slot></slot>
      <button class="trigger" type="button" aria-label="Search documentation" aria-keyshortcuts="/ Meta+K Control+K" @click=${this.openDialog}>
        ${lucideIcon(Search, { size: 16, strokeWidth: 2 })}
        <span>Search</span>
        <kbd aria-hidden="true">⌘K</kbd>
      </button>
      <dialog aria-labelledby="site-search-title" @click=${this.closeFromBackdrop}>
        <div class="panel" role="search">
          <header>
            <h2 id="site-search-title">Search documentation</h2>
            <button class="close" type="button" aria-label="Close search" @click=${this.closeDialog}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button>
          </header>
          <div class="controls" @keydown=${this.handleInputKeydown}>
            <slot name="input"></slot>
          </div>
          <section class="results" aria-live="polite" aria-busy=${String(loading)}>${this.renderResults(query, results, total, loading)}</section>
        </div>
      </dialog>`
  }

  private renderResults(query: string, results: SiteSearchResult[], total: number, loading: boolean) {
    if (!query) return html`<p class="status" role="status">Start typing to search the documentation.</p>`
    if (loading) return html`<p class="status" role="status">Searching…</p>`
    if (results.length === 0) return html`<p class="status" role="status">No results for “${query}”.</p>`
    const label = `${total} ${total === 1 ? 'result' : 'results'}`
    return html`<p class="status" role="status">${label}</p>
      <ul>
        ${results.map(
          (result) =>
            html`<li>
              <a href=${result.href}>
                <strong>${result.title}</strong>
                <span>${result.summary}</span>
              </a>
            </li>`,
        )}
      </ul>`
  }

  private openDialog = (): void => {
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('dialog')
    if (!dialog || dialog.open) return
    dialog.showModal()
    requestAnimationFrame(() => this.querySelector<HTMLInputElement>('input[slot="input"]')?.focus())
  }

  private closeDialog = (): void => {
    this.renderRoot.querySelector<HTMLDialogElement>('dialog')?.close()
  }

  private closeFromBackdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.closeDialog()
  }

  private handleInputKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'Enter' || event.isComposing) return
    const query = this.querySelector<HTMLInputElement>('input[slot="input"]')?.value.trim() ?? ''
    if (!query) return
    event.preventDefault()
    window.location.assign(`/docs/search?q=${encodeURIComponent(query)}`)
  }
}

if (!customElements.get('lv-site-search')) {
  customElements.define('lv-site-search', SiteSearch)
}


function currentThemeMode(): ThemeMode {
  try {
    return normalizeThemeMode(localStorage.getItem('leapview-color-mode'))
  } catch {
    return normalizeThemeMode(document.documentElement.dataset.colorMode)
  }
}

function normalizeThemeMode(mode: string | null | undefined): ThemeMode {
  return mode === 'light' || mode === 'dark' || mode === 'system' ? mode : 'system'
}
