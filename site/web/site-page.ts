import { LitElement, css, html } from 'lit'
import { Blocks, BookOpen, Bot, Boxes, ChartNoAxesCombined, Check, CodeXml, Copy, Database, GitBranch, Menu, Monitor, Moon, PanelLeftClose, PanelLeftOpen, Radio, Search, Server, SquareMousePointer, SquareTerminal, Sun, X, type IconNode } from 'lucide'
import { DatastarLit } from '../../web/components/shared/datastar-lit'
import { lucideIcon } from '../../web/components/shared/lucide-icons'
import '../../web/components/shared/brand-mark'
import '../../web/components/shared/code-block'
import type {
  DashboardCompiledFilterBinding,
  DashboardCompiledFilterDefinition,
  DashboardFilterExpression,
  DashboardFilterPresentation,
} from '../../web/generated/signals'
import type { VisualizationEnvelope } from '../../web/generated/visualization'
import { kpiLayoutFeatures, resolveKPIWidgetLayout } from '../../web/components/dashboard/visualization/kpi-layout'
import {
  layoutRequirements,
  resolveWidgetLayout,
  widgetChrome,
  type WidgetContractID,
  type WidgetLayoutFeature,
  type WidgetLayoutResolution,
} from '../../web/components/dashboard/visualization/layout'
import { visualExampleHighlightLines } from './visual-example-highlights'

type ThemeMode = 'system' | 'light' | 'dark'
type VisualPayload = VisualizationEnvelope
type VisualShowcaseDocument = {
  slug: string
  title: string
  visualID: string
}

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

class SiteDocsDrawerToggle extends LitElement {
  static properties = {
    placement: { type: String },
  }

  declare placement: string

  private open = false
  private readonly handleDrawerState = (event: Event) => {
    this.open = Boolean((event as CustomEvent<{ open?: boolean }>).detail?.open)
    this.requestUpdate()
  }

  static styles = css`
    :host {
      display: none;
    }

    @media (max-width: 56.25rem) {
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
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('leapview-docs-drawer-state', this.handleDrawerState)
  }

  disconnectedCallback(): void {
    document.removeEventListener('leapview-docs-drawer-state', this.handleDrawerState)
    super.disconnectedCallback()
  }

  render() {
    const closeControl = this.placement === 'drawer'
    const label = closeControl || this.open ? 'Close documentation menu' : 'Open documentation menu'
    const icon = closeControl || this.open ? PanelLeftClose : PanelLeftOpen
    return html`<button type="button" aria-label=${label} aria-controls="site-docs-sidebar" aria-expanded=${String(this.open)} @click=${this.toggleDrawer}>${lucideIcon(closeControl ? X : icon, { size: 18, strokeWidth: 2 })}</button>`
  }

  private toggleDrawer = (): void => {
    document.dispatchEvent(
      new CustomEvent('leapview-docs-drawer-request', {
        detail: { open: this.placement === 'drawer' ? false : !this.open },
      }),
    )
  }
}

if (!customElements.get('lv-site-docs-drawer-toggle')) {
  customElements.define('lv-site-docs-drawer-toggle', SiteDocsDrawerToggle)
}

const docsDrawerFocusableSelector = 'a[href], button:not([disabled]), summary, input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

function docsDrawerFocusableElements(sidebar: HTMLElement): HTMLElement[] {
  const focusable: HTMLElement[] = []
  const collect = (root: ParentNode): void => {
    for (const element of root.querySelectorAll<HTMLElement>('*')) {
      if (element.matches(docsDrawerFocusableSelector) && element.getClientRects().length > 0 && !element.closest('[inert]')) focusable.push(element)
      if (element.shadowRoot) collect(element.shadowRoot)
    }
  }
  collect(sidebar)
  return focusable
}

function deepestActiveElement(): Element | null {
  let active: Element | null = document.activeElement
  while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement
  return active
}

function syncDocsDrawer(open = false): void {
  const layout = document.querySelector<HTMLElement>('.site-docs-layout')
  const sidebar = document.querySelector<HTMLElement>('.site-docs-sidebar')
  if (!layout || !sidebar) return
  const header = document.querySelector<HTMLElement>('.site-header')
  const content = layout.querySelector<HTMLElement>('.site-docs-content')

  const compact = window.matchMedia('(max-width: 56.25rem)').matches
  const nextOpen = compact && open
  const wasOpen = layout.classList.contains('site-docs-drawer-open')
  layout.classList.toggle('site-docs-drawer-open', nextOpen)
  sidebar.inert = compact && !nextOpen
  sidebar.setAttribute('aria-hidden', String(compact && !nextOpen))
  if (header) header.inert = nextOpen
  if (content) content.inert = nextOpen
  document.body.classList.toggle('site-docs-drawer-open', nextOpen)
  document.dispatchEvent(
    new CustomEvent('leapview-docs-drawer-state', {
      detail: { open: nextOpen },
    }),
  )
  if (nextOpen && !wasOpen) {
    requestAnimationFrame(() => {
      revealCurrentDocsLink()
      docsDrawerFocusableElements(sidebar)[0]?.focus()
    })
  }
  if (compact && wasOpen && !nextOpen) {
    document.querySelector<HTMLElement>('lv-site-docs-drawer-toggle:not([placement])')?.shadowRoot?.querySelector<HTMLButtonElement>('button')?.focus()
  }
}

document.addEventListener('leapview-docs-drawer-request', (event) => {
  const requested = (event as CustomEvent<{ open?: boolean }>).detail?.open
  const currentlyOpen = document.querySelector('.site-docs-layout')?.classList.contains('site-docs-drawer-open') ?? false
  syncDocsDrawer(typeof requested === 'boolean' ? requested : !currentlyOpen)
})

document.addEventListener('click', (event) => {
  if ((event.target as Element).closest('[data-site-docs-drawer-close]')) syncDocsDrawer(false)
})

document.addEventListener('keydown', (event) => {
  const layout = document.querySelector<HTMLElement>('.site-docs-layout')
  if (!layout?.classList.contains('site-docs-drawer-open')) return
  if (event.key === 'Escape') {
    event.preventDefault()
    syncDocsDrawer(false)
    return
  }
  if (event.key !== 'Tab') return
  const sidebar = document.querySelector<HTMLElement>('.site-docs-sidebar')
  if (!sidebar) return
  const focusable = docsDrawerFocusableElements(sidebar)
  if (focusable.length === 0) return
  const active = deepestActiveElement()
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (event.shiftKey && (active === first || !active || !sidebar.contains(active))) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && (active === last || !active || !sidebar.contains(active))) {
    event.preventDefault()
    first.focus()
  }
})

window.addEventListener('resize', () => syncDocsDrawer(document.querySelector('.site-docs-layout')?.classList.contains('site-docs-drawer-open')))

const docsSidebarScrollStorageKey = 'leapview:docs-sidebar-scroll:v1'

type DocsSidebarScrollAnchor = {
  id: string
  kind: 'group' | 'link'
  offset: number
}

type DocsSidebarScrollState = {
  anchor?: DocsSidebarScrollAnchor
  scrollTop: number
}

function initializeDocsSidebarScroll(): void {
  const sidebar = document.querySelector<HTMLElement>('.site-docs-sidebar')
  if (!sidebar) return

  let persistenceFrame = 0
  sidebar.addEventListener('scroll', () => {
    if (persistenceFrame !== 0) return
    persistenceFrame = requestAnimationFrame(() => {
      persistenceFrame = 0
      persistDocsSidebarScroll(sidebar)
    })
  }, { passive: true })
  window.addEventListener('pagehide', () => persistDocsSidebarScroll(sidebar), { once: true })

  requestAnimationFrame(() => {
    restoreDocsSidebarScroll(sidebar)
    revealCurrentDocsLink()
  })
}

function persistDocsSidebarScroll(sidebar: HTMLElement): void {
  const state: DocsSidebarScrollState = {
    anchor: currentDocsSidebarAnchor(sidebar),
    scrollTop: sidebar.scrollTop,
  }
  try {
    sessionStorage.setItem(docsSidebarScrollStorageKey, JSON.stringify(state))
  } catch {
    // Storage can be unavailable in restricted browsing contexts. Navigation
    // remains usable through the active-link reveal below.
  }
}

function currentDocsSidebarAnchor(sidebar: HTMLElement): DocsSidebarScrollAnchor | undefined {
  const sidebarTop = sidebar.getBoundingClientRect().top
  for (const row of docsSidebarRows(sidebar)) {
    const bounds = row.getBoundingClientRect()
    if (bounds.height === 0 || bounds.bottom <= sidebarTop) continue
    if (row.matches('a')) {
      const href = row.getAttribute('href')
      if (href) return { id: href, kind: 'link', offset: bounds.top - sidebarTop }
    } else {
      const group = row.parentElement?.getAttribute('data-site-docs-group')
      if (group) return { id: group, kind: 'group', offset: bounds.top - sidebarTop }
    }
  }
  return undefined
}

function restoreDocsSidebarScroll(sidebar: HTMLElement): void {
  const state = storedDocsSidebarScroll()
  if (!state) return

  sidebar.scrollTop = state.scrollTop
  const savedAnchor = state.anchor
  if (!savedAnchor) return
  const anchor = docsSidebarRows(sidebar).find((row) => docsSidebarRowMatchesAnchor(row, savedAnchor))
  if (!anchor || anchor.getBoundingClientRect().height === 0) return

  const currentOffset = anchor.getBoundingClientRect().top - sidebar.getBoundingClientRect().top
  sidebar.scrollTop += currentOffset - savedAnchor.offset
}

function storedDocsSidebarScroll(): DocsSidebarScrollState | undefined {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(docsSidebarScrollStorageKey) ?? 'null')
    if (!value || typeof value !== 'object') return undefined
    const state = value as Partial<DocsSidebarScrollState>
    if (!Number.isFinite(state.scrollTop) || Number(state.scrollTop) < 0) return undefined
    if (state.anchor !== undefined && !validDocsSidebarAnchor(state.anchor)) return undefined
    return { anchor: state.anchor, scrollTop: Number(state.scrollTop) }
  } catch {
    return undefined
  }
}

function validDocsSidebarAnchor(value: unknown): value is DocsSidebarScrollAnchor {
  if (!value || typeof value !== 'object') return false
  const anchor = value as Partial<DocsSidebarScrollAnchor>
  return (anchor.kind === 'group' || anchor.kind === 'link') && typeof anchor.id === 'string' && anchor.id !== '' && Number.isFinite(anchor.offset)
}

function docsSidebarRows(sidebar: HTMLElement): HTMLElement[] {
  return Array.from(sidebar.querySelectorAll<HTMLElement>('.site-docs-link, .site-docs-nav-group > summary'))
}

function docsSidebarRowMatchesAnchor(row: HTMLElement, anchor: DocsSidebarScrollAnchor): boolean {
  if (anchor.kind === 'link') return row.matches('a') && row.getAttribute('href') === anchor.id
  return row.matches('summary') && row.parentElement?.getAttribute('data-site-docs-group') === anchor.id
}

function revealCurrentDocsLink(): void {
  document.querySelector<HTMLElement>('.site-docs-link-current')?.scrollIntoView({
    block: 'nearest',
    inline: 'nearest',
  })
}

syncDocsDrawer()
initializeDocsSidebarScroll()

class SiteMarkdownCopy extends LitElement {
  static properties = {
    markdown: { type: String },
  }

  declare markdown: string

  private copied = false
  private resetTimer?: number

  static styles = css`
    :host {
      display: inline-block;
    }

    button {
      display: inline-flex;
      box-sizing: border-box;
      height: 33px;
      align-items: center;
      flex-shrink: 0;
      gap: var(--base-size-6);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: inherit;
      font-size: var(--text-body-size-small);
      line-height: 1.3;
      padding: 0 var(--base-size-12);
      transition: border-color var(--motion-duration-medium);
    }

    button:hover,
    button:focus-visible {
      border-color: var(--lv-button-border-hover);
    }

    button:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    @media (prefers-reduced-motion: reduce) {
      button {
        transition: none;
      }
    }
  `

  disconnectedCallback(): void {
    window.clearTimeout(this.resetTimer)
    super.disconnectedCallback()
  }

  render() {
    const label = this.copied ? 'Markdown copied' : 'Copy Markdown'
    return html`<button type="button" aria-label=${label} @click=${this.copyMarkdown}>
      ${lucideIcon(this.copied ? Check : Copy, { size: 16, strokeWidth: 2 })}
      <span>${this.copied ? 'Copied' : 'Copy Markdown'}</span>
    </button>`
  }

  private copyMarkdown = async (): Promise<void> => {
    if (!this.markdown) return

    try {
      await writeClipboard(this.markdown)
    } catch {
      return
    }

    this.copied = true
    this.requestUpdate()
    window.clearTimeout(this.resetTimer)
    this.resetTimer = window.setTimeout(() => {
      this.copied = false
      this.requestUpdate()
    }, 2_000)
  }
}

if (!customElements.get('lv-site-markdown-copy')) {
  customElements.define('lv-site-markdown-copy', SiteMarkdownCopy)
}

type ResolvedThemeMode = 'light' | 'dark'

let mermaidModule: Promise<(typeof import('mermaid'))['default']> | undefined
let mermaidRenderSequence = 0
let mermaidRenderQueue: Promise<void> = Promise.resolve()

function loadMermaid(): Promise<(typeof import('mermaid'))['default']> {
  mermaidModule ??= import('mermaid').then((module) => module.default)
  return mermaidModule
}

function resolvedThemeMode(): ResolvedThemeMode {
  const colorScheme = document.documentElement.style.colorScheme
  if (colorScheme === 'dark' || colorScheme === 'light') return colorScheme
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function mermaidAccessibleTitle(source: string): string {
  const accessibilityTitle = source.match(/^\s*accTitle:\s*(.+?)\s*$/m)?.[1]
  if (accessibilityTitle) return accessibilityTitle

  const frontmatter = source.match(/^---\s*\n([\s\S]*?)\n---\s*\n/)
  const frontmatterTitle = frontmatter?.[1].match(/^title:\s*["']?(.+?)["']?\s*$/m)?.[1]
  return frontmatterTitle || 'Documentation diagram'
}

class SiteMermaid extends LitElement {
  static properties = {
    source: { type: String },
  }

  declare source: string
  private renderGeneration = 0
  private readonly handleThemeApplied = (event: Event): void => {
    const detail = (event as CustomEvent<{ resolvedMode?: string }>).detail
    const theme = detail?.resolvedMode === 'dark' ? 'dark' : 'light'
    if (this.dataset.renderedTheme !== theme) void this.draw(theme)
  }

  static styles = css`
    :host {
      display: block;
      width: 100%;
      min-width: 0;
      color: var(--lv-fg-default);
    }

    figure {
      display: grid;
      min-width: 0;
      margin: 0;
      gap: var(--base-size-12);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-20);
    }

    .canvas {
      display: grid;
      min-width: 0;
      min-height: var(--base-size-64);
      place-items: center;
      overflow: auto hidden;
    }

    .canvas svg {
      display: block;
      width: auto;
      max-width: 100%;
      height: auto;
      max-height: min(38rem, 70svh);
    }

    figcaption,
    .error {
      margin: 0;
      color: var(--lv-fg-muted);
      font-size: var(--text-body-size-small);
      line-height: var(--base-text-lineHeight-relaxed);
    }

    figcaption {
      text-align: center;
    }

    .error {
      color: var(--lv-fg-danger);
    }

    [hidden] {
      display: none;
    }

    @media (width < 48rem) {
      figure {
        padding: var(--base-size-12);
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('leapview-theme-applied', this.handleThemeApplied)
  }

  disconnectedCallback(): void {
    document.removeEventListener('leapview-theme-applied', this.handleThemeApplied)
    this.renderGeneration += 1
    super.disconnectedCallback()
  }

  protected updated(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('source')) {
      this.setAttribute('aria-label', mermaidAccessibleTitle(this.source ?? ''))
      void this.draw(resolvedThemeMode())
    }
  }

  render() {
    const title = mermaidAccessibleTitle(this.source ?? '')
    return html`<figure>
      <div class="canvas" aria-busy="true"></div>
      <p class="error" role="alert" hidden></p>
      <figcaption>${title}</figcaption>
    </figure>`
  }

  private async draw(theme: ResolvedThemeMode): Promise<void> {
    const generation = ++this.renderGeneration
    await this.updateComplete
    const source = this.source?.trim()
    const canvas = this.renderRoot.querySelector<HTMLElement>('.canvas')
    const error = this.renderRoot.querySelector<HTMLElement>('.error')
    if (!source || !canvas || !error) return

    canvas.setAttribute('aria-busy', 'true')
    error.hidden = true
    const task = async (): Promise<void> => {
      try {
        const mermaid = await loadMermaid()
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: 'strict',
          suppressErrorRendering: true,
          theme: 'base',
          fontFamily: cssToken(this, '--fontStack-system'),
          themeVariables: mermaidThemeVariables(this),
          flowchart: { htmlLabels: false, useMaxWidth: true },
        })
        const id = `leapview-docs-diagram-${++mermaidRenderSequence}`
        const result = await mermaid.render(id, source)
        if (generation !== this.renderGeneration || !this.isConnected) return

        canvas.innerHTML = result.svg
        const svg = canvas.querySelector('svg')
        if (svg) {
          svg.setAttribute('role', 'img')
          svg.style.maxWidth = '100%'
          svg.style.height = 'auto'
        }
        result.bindFunctions?.(canvas)
        canvas.setAttribute('aria-busy', 'false')
        this.dataset.renderedTheme = theme
      } catch (cause) {
        if (generation !== this.renderGeneration || !this.isConnected) return
        canvas.replaceChildren()
        canvas.setAttribute('aria-busy', 'false')
        error.textContent = `Diagram could not be rendered: ${cause instanceof Error ? cause.message : String(cause)}`
        error.hidden = false
      }
    }

    const queued = mermaidRenderQueue.then(task, task)
    mermaidRenderQueue = queued.then(
      () => undefined,
      () => undefined,
    )
    await queued
  }
}

function cssToken(element: Element, name: string): string {
  const value = getComputedStyle(element).getPropertyValue(name).trim()
  if (!value) throw new Error(`Required diagram token ${name} is unavailable`)
  return value
}

function mermaidThemeVariables(element: Element): Record<string, string> {
  const background = cssToken(element, '--lv-bg-panel')
  const foreground = cssToken(element, '--lv-fg-default')
  const muted = cssToken(element, '--lv-fg-muted')
  const accent = cssToken(element, '--lv-fg-accent')
  const accentBackground = cssToken(element, '--lv-bg-accent-muted')
  const control = cssToken(element, '--lv-bg-control')
  const border = cssToken(element, '--lv-line-muted')

  return {
    background,
    primaryColor: accentBackground,
    primaryTextColor: foreground,
    primaryBorderColor: accent,
    secondaryColor: control,
    secondaryTextColor: foreground,
    secondaryBorderColor: border,
    tertiaryColor: background,
    tertiaryTextColor: foreground,
    tertiaryBorderColor: border,
    lineColor: muted,
    textColor: foreground,
    mainBkg: accentBackground,
    nodeBorder: accent,
    clusterBkg: control,
    clusterBorder: border,
    edgeLabelBackground: background,
    noteBkgColor: control,
    noteBorderColor: border,
    noteTextColor: foreground,
  }
}

if (!customElements.get('lv-site-mermaid')) {
  customElements.define('lv-site-mermaid', SiteMermaid)
}

function enhanceDocsCodeBlocks(): void {
  document.querySelectorAll<HTMLElement>('.site-docs-article pre').forEach((pre) => {
    if (pre.closest('lv-code-block, lv-site-mermaid')) return

    const code = pre.querySelector('code')
    const languageClass = Array.from(code?.classList ?? []).find((name) => name.startsWith('language-'))
    const language = languageClass?.slice('language-'.length).toLowerCase() ?? ''
    if (language === 'mermaid') {
      const diagram = document.createElement('lv-site-mermaid') as SiteMermaid
      diagram.source = code?.textContent ?? pre.textContent ?? ''
      pre.replaceWith(diagram)
      return
    }
    const block = document.createElement('lv-code-block') as HTMLElement & {
      clearFocusedLines(): void
      code: string
      copy: boolean
      focusLines(lines: readonly number[]): void
      highlightedLines: number[]
      toolbar: boolean
    }

    block.setAttribute('language', language || 'text')
    block.code = code?.textContent ?? pre.textContent ?? ''
    const keyFields = pre.previousElementSibling
    const visualExample = keyFields?.matches('.site-visual-key-fields') ? keyFields.previousElementSibling : null
    if (language === 'yaml' && keyFields instanceof HTMLElement && visualExample?.matches('lv-site-visual-example')) {
      const fields = JSON.parse(keyFields.dataset.keyFields ?? '[]') as string[]
      const exampleID = visualExample.getAttribute('example-id') ?? ''
      block.dataset.visualExample = exampleID
      block.dataset.highlightedFields = fields.join(',')
      block.highlightedLines = visualExampleHighlightLines(block.code, fields)
      block.id = `visual-example-${exampleID}-yaml`
      enhanceVisualKeyFieldControls(keyFields, block)
    }
    block.copy = true
    block.toolbar = true
    pre.replaceWith(block)
  })
}

function enhanceVisualKeyFieldControls(
  container: HTMLElement,
  block: HTMLElement & { clearFocusedLines(): void; code: string; focusLines(lines: readonly number[]): void },
): void {
  let focusedField = ''
  let hoveredField = ''
  const lines = new Map<string, number[]>()
  const apply = (): void => {
    const field = focusedField || hoveredField
    if (!field) {
      block.clearFocusedLines()
      return
    }
    block.focusLines(lines.get(field) ?? [])
  }

  container.querySelectorAll<HTMLButtonElement>('[data-visual-key-field]').forEach((control) => {
    const field = control.dataset.visualKeyField ?? ''
    lines.set(field, visualExampleHighlightLines(block.code, [field]))
    control.setAttribute('aria-controls', block.id)
    control.addEventListener('focus', () => {
      focusedField = field
      apply()
    })
    control.addEventListener('blur', () => {
      focusedField = ''
      apply()
    })
    control.addEventListener('pointerenter', () => {
      hoveredField = field
      apply()
    })
    control.addEventListener('pointerleave', () => {
      hoveredField = ''
      apply()
    })
  })
}

type CalloutKind = 'note' | 'tip' | 'experimental' | 'warning' | 'danger'

const calloutKinds: Record<string, { kind: CalloutKind; label: string }> = {
  CAUTION: { kind: 'danger', label: 'Caution' },
  DANGER: { kind: 'danger', label: 'Danger' },
  EXPERIMENTAL: { kind: 'experimental', label: 'Experimental' },
  IMPORTANT: { kind: 'note', label: 'Important' },
  NOTE: { kind: 'note', label: 'Note' },
  TIP: { kind: 'tip', label: 'Tip' },
  WARNING: { kind: 'warning', label: 'Warning' },
}

function enhanceDocsCallouts(): void {
  document.querySelectorAll<HTMLElement>('.site-docs-article blockquote').forEach((blockquote) => {
    if (blockquote.classList.contains('site-docs-callout')) return
    const paragraph = blockquote.querySelector<HTMLElement>(':scope > p')
    if (!paragraph) return

    const walker = document.createTreeWalker(paragraph, NodeFilter.SHOW_TEXT)
    const markerNode = walker.nextNode() as Text | null
    const marker = markerNode?.data.match(/^\s*\[!(NOTE|TIP|EXPERIMENTAL|WARNING|CAUTION|DANGER|IMPORTANT)\]\s*/i)
    if (!markerNode || !marker) return

    const definition = calloutKinds[marker[1].toUpperCase()]
    markerNode.data = markerNode.data.slice(marker[0].length)
    blockquote.classList.add('site-docs-callout', `site-docs-callout-${definition.kind}`)
    blockquote.dataset.callout = definition.kind

    const label = document.createElement('p')
    label.className = 'site-docs-callout-label'
    const strong = document.createElement('strong')
    strong.textContent = definition.label
    label.append(strong)
    blockquote.prepend(label)
  })
}

async function writeClipboard(value: string): Promise<void> {
  if (navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(value)
    return
  }

  const textarea = document.createElement('textarea')
  textarea.value = value
  textarea.setAttribute('readonly', '')
  textarea.style.position = 'fixed'
  textarea.style.opacity = '0'
  document.body.append(textarea)
  textarea.select()
  const copied = document.execCommand('copy')
  textarea.remove()
  if (!copied) throw new Error('clipboard write failed')
}

enhanceDocsCodeBlocks()
enhanceDocsCallouts()

const featureIcons: Record<string, IconNode> = {
  agent: Bot,
  blocks: Blocks,
  boxes: Boxes,
  chart: ChartNoAxesCombined,
  'code-xml': CodeXml,
  database: Database,
  'git-branch': GitBranch,
  radio: Radio,
  server: Server,
  'square-mouse-pointer': SquareMousePointer,
  terminal: SquareTerminal,
}

class SiteFeatureIcon extends LitElement {
  static properties = {
    name: { type: String },
  }

  declare name: string

  static styles = css`
    :host {
      display: grid;
      width: var(--control-large-size);
      height: var(--control-large-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-control);
      color: var(--lv-fg-accent);
    }

    :host([plain]) {
      width: var(--base-size-28);
      height: var(--base-size-28);
      border: 0;
      border-radius: 0;
      background: transparent;
      color: var(--lv-fg-muted);
    }
  `

  render() {
    return lucideIcon(featureIcons[this.name] ?? Blocks, {
      size: 22,
      strokeWidth: 1.8,
    })
  }
}

if (!customElements.get('lv-site-feature-icon')) {
  customElements.define('lv-site-feature-icon', SiteFeatureIcon)
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

type ArticleSection = { id: string; label: string; level: number }
type ArticleSectionNode = ArticleSection & { children: ArticleSectionNode[] }

class SiteArticleToc extends LitElement {
  private sections: ArticleSection[] = []
  private activeId = ''
  private visibleSectionIDs = new Map<string, string>()
  private observer?: IntersectionObserver

  static styles = css`
    :host {
      display: block;
      position: sticky;
      top: calc(var(--site-header-height) + var(--base-size-32));
      align-self: start;
      height: calc(100svh - var(--site-header-height) - var(--base-size-32));
      overflow: auto;
      scrollbar-width: none;
    }

    :host::-webkit-scrollbar {
      display: none;
    }

    h2 {
      margin: 0 0 0 var(--base-size-12);
      color: var(--lv-fg-subtle);
      font-size: var(--text-body-size-small);
      font-weight: var(--base-text-weight-normal);
      letter-spacing: 0.03em;
      line-height: 1.2;
      text-transform: uppercase;
    }

    ul {
      padding: 0;
      list-style: none;
    }

    ul#toc {
      position: relative;
      margin: 15px 0 0;
    }

    ul ul {
      margin: var(--base-size-2) 0 var(--base-size-2) 15px;
      border-left: var(--lv-border-muted);
    }

    ul ul ul {
      display: none;
    }

    li {
      font-size: var(--text-body-size-small);
      font-weight: var(--base-text-weight-normal);
      letter-spacing: 0.005em;
      line-height: 1;
      list-style: none;
    }

    a {
      display: inline-block;
      overflow: hidden;
      max-width: 100%;
      border-radius: var(--lv-radius-full);
      padding: var(--base-size-6) var(--base-size-12);
      color: var(--lv-fg-subtle);
      line-height: 1;
      text-decoration: none;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    a:hover,
    a:focus-visible,
    li.current > a {
      color: var(--lv-fg-default);
    }

    a:focus-visible {
      outline: var(--focus-outline);
      outline-offset: calc(-1 * var(--focus-outline-offset));
    }
  `

  connectedCallback() {
    super.connectedCallback()
    requestAnimationFrame(() => this.collectSections())
  }

  disconnectedCallback() {
    this.observer?.disconnect()
    super.disconnectedCallback()
  }

  private collectSections() {
    const article = document.querySelector<HTMLElement>('.site-docs-article')
    const headings = Array.from(article?.querySelectorAll<HTMLElement>(':scope > h2, :scope > h3, :scope > h4') ?? [])
    const used = new Set<string>()
    this.sections = headings.map((heading) => {
      let id =
        heading.id ||
        heading.textContent
          ?.trim()
          .toLowerCase()
          .replace(/[^a-z0-9]+/g, '-')
          .replace(/^-|-$/g, '') ||
        'section'
      const base = id
      let suffix = 2
      while (used.has(id)) id = `${base}-${suffix++}`
      used.add(id)
      heading.id = id
      return {
        id,
        label: heading.textContent?.trim() ?? '',
        level: Number(heading.tagName.slice(1)),
      }
    })
    this.visibleSectionIDs = new Map<string, string>()
    const indexVisibleSections = (nodes: ArticleSectionNode[], depth = 0, visibleAncestor = ''): void => {
      for (const node of nodes) {
        const visibleID = depth <= 1 ? node.id : visibleAncestor
        this.visibleSectionIDs.set(node.id, visibleID)
        indexVisibleSections(node.children, depth + 1, visibleID)
      }
    }
    indexVisibleSections(this.sectionTree())
    this.activeId = this.visibleSectionIDs.get(this.sections[0]?.id ?? '') ?? ''
    this.observer = new IntersectionObserver(
      (entries) => {
        const visible = entries.filter((entry) => entry.isIntersecting).sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0]
        const visibleID = visible?.target.id ? this.visibleSectionIDs.get(visible.target.id) : undefined
        if (visibleID && this.activeId !== visibleID) {
          this.setActiveSection(visibleID)
        }
      },
      { rootMargin: '-18% 0px -70% 0px', threshold: 0 },
    )
    headings.forEach((heading) => this.observer?.observe(heading))
    this.requestUpdate()
  }

  private setActiveSection(id: string): void {
    this.activeId = id
    this.requestUpdate()
    void this.updateComplete.then(() => this.revealActiveSection())
  }

  private revealActiveSection(): void {
    const active = this.renderRoot.querySelector<HTMLElement>('a.active')
    if (!active || active.getClientRects().length === 0) return
    const hostBounds = this.getBoundingClientRect()
    const activeBounds = active.getBoundingClientRect()
    const revealMargin = 8
    if (activeBounds.top < hostBounds.top + revealMargin) {
      this.scrollTop -= hostBounds.top + revealMargin - activeBounds.top
    } else if (activeBounds.bottom > hostBounds.bottom - revealMargin) {
      this.scrollTop += activeBounds.bottom - hostBounds.bottom + revealMargin
    }
  }

  private sectionTree(): ArticleSectionNode[] {
    const roots: ArticleSectionNode[] = []
    const stack: ArticleSectionNode[] = []

    for (const section of this.sections) {
      const node: ArticleSectionNode = { ...section, children: [] }
      while (stack.length && stack[stack.length - 1].level >= node.level) stack.pop()
      const parent = stack[stack.length - 1]
      if (parent) parent.children.push(node)
      else roots.push(node)
      stack.push(node)
    }

    return roots
  }

  private renderSections(sections: ArticleSectionNode[]): Array<ReturnType<typeof html>> {
    return sections.map(
      (section) => html`
        <li class=${section.id === this.activeId ? 'current' : ''}>
          <a class=${section.id === this.activeId ? 'active' : ''} data-level=${section.level} href=${`#${section.id}`}>${section.label}</a>
          ${
            section.children.length
              ? html`<ul>
                  ${this.renderSections(section.children)}
                </ul>`
              : null
          }
        </li>
      `,
    )
  }

  render() {
    if (!this.sections.length) return null
    return html`<nav aria-label="In this article">
      <h2>In this article</h2>
      <ul id="toc">
        ${this.renderSections(this.sectionTree())}
      </ul>
    </nav>`
  }
}

if (!customElements.get('lv-site-article-toc')) customElements.define('lv-site-article-toc', SiteArticleToc)

class SiteVisualExample extends DatastarLit(LitElement) {
  static properties = {
    exampleId: { type: String, attribute: 'example-id' },
  }

  declare exampleId: string

  static styles = css`
    :host {
      display: block;
      min-height: 28rem;
      margin-block: var(--base-size-24);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
      box-shadow: var(--shadow-resting-small);
      overflow: hidden;
    }

    lv-visualization-host {
      display: block;
      height: 28rem;
    }

    :host([type='kpi']) {
      min-height: 0;
      padding: var(--base-size-16);
      overflow: auto;
    }

    .layout-gallery {
      display: flex;
      flex-wrap: wrap;
      align-items: start;
      gap: var(--base-size-20);
    }

    .layout-preview {
      display: grid;
      gap: var(--base-size-8);
      margin: 0;
    }

    .layout-frame {
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
    }

    .layout-frame lv-visualization-host {
      width: 100%;
      height: 100%;
    }

    figcaption {
      color: var(--lv-fg-muted);
      font-size: var(--text-caption-size);
      line-height: var(--base-text-lineHeight-tight);
    }

  `

  render() {
    const visuals = this.signal<VisualPayload[]>('visuals', [])
    const visual = visuals.find((candidate) => candidate.visualID === this.exampleId)
    const visualType = visual?.spec.kind ?? ''
    if (this.getAttribute('type') !== visualType) {
      queueMicrotask(() => {
        if (visualType) this.setAttribute('type', visualType)
        else this.removeAttribute('type')
      })
    }
    if (!visual) return null
    if (visual.spec.kind !== 'kpi') {
      return html`<lv-visualization-host .envelope=${visual}></lv-visualization-host>`
    }
    const requirements = layoutRequirements('kpi', kpiLayoutFeatures(visual))
    return html`<div class="layout-gallery" aria-label="Automatic responsive layouts">
      ${requirements.map((requirement) => html`
        <figure class="layout-preview" data-layout-preview=${requirement.layout}>
          <div
            class="layout-frame"
            style=${`width: ${requirement.minimum.width}px; height: ${requirement.minimum.height}px`}
          >
            <lv-visualization-host .envelope=${visual}></lv-visualization-host>
          </div>
          <figcaption>
            ${requirement.minimum.width}×${requirement.minimum.height} · Automatically selected: ${requirement.layout}
          </figcaption>
        </figure>
      `)}
    </div>`
  }
}

if (!customElements.get('lv-site-visual-example')) {
  customElements.define('lv-site-visual-example', SiteVisualExample)
}

type KPIScenario = Readonly<{ id: string; label: string; description: string }>
type FilterScenario = Readonly<{
  id: string
  label: string
  description: string
  contract: WidgetContractID
  definition: DashboardCompiledFilterDefinition
  presentation: DashboardFilterPresentation
  expression: DashboardFilterExpression
}>

const kpiScenarios: readonly KPIScenario[] = [
  { id: 'total_orders', label: 'Basic value', description: 'Current value and an explicit note.' },
  { id: 'revenue_kpi_trend', label: 'Trend', description: 'Current value with an explicit sparkline.' },
  { id: 'revenue_kpi_unfavorable', label: 'Comparison', description: 'Baseline, delta, and authored unfavorable direction.' },
  { id: 'revenue_kpi_favorable', label: 'Comparison and trend', description: 'Baseline context and sparkline together.' },
  { id: 'revenue_kpi_bullet', label: 'Bullet', description: 'Goal, qualitative ranges, and measured value.' },
  { id: 'revenue_kpi_out_of_range', label: 'Progress', description: 'Goal progress with truthful out-of-range status.' },
  { id: 'revenue_kpi_status', label: 'Status', description: 'Qualitative status without implying a goal.' },
  { id: 'revenue_kpi_all_features', label: 'All features — stress test', description: 'Boundary coverage for subtitle, comparison, progress, goal, status, trend, and note.' },
  { id: 'revenue_kpi_missing_comparison', label: 'Missing comparison', description: 'Unavailable baseline remains visibly distinct from zero.' },
]

const filterBinding: DashboardCompiledFilterBinding = {
  key: 'qa-filter', id: 'qa-filter', filter: 'qa-filter', scope: 'page', pageID: 'responsive-widgets',
  default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
  readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
}

const filterScenarios: readonly FilterScenario[] = [
  {
    id: 'dropdown', label: 'Dropdown', description: 'One categorical selection with static options.', contract: 'slicer.dropdown',
    definition: filterDefinition('state', 'State', 'string', 'set', {
      kind: 'static', limit: 3, values: [
        { value: { kind: 'string', value: 'CA' }, label: 'California' },
        { value: { kind: 'string', value: 'NY' }, label: 'New York' },
        { value: { kind: 'string', value: 'TX' }, label: 'Texas' },
      ],
    }),
    presentation: filterPresentation('dropdown'),
    expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] },
  },
  {
    id: 'input', label: 'Comparison input', description: 'An explicit operator and numeric value.', contract: 'slicer.input',
    definition: filterDefinition('revenue', 'Revenue', 'decimal', 'comparison'),
    presentation: filterPresentation('input'),
    expression: { kind: 'comparison', operator: 'greater_than_or_equal', value: { kind: 'decimal', value: '1000' } },
  },
  {
    id: 'numeric-range', label: 'Numeric range', description: 'Minimum and maximum remain present in both layouts.', contract: 'slicer.numeric_range',
    definition: filterDefinition('order_value', 'Order value', 'decimal', 'range'),
    presentation: filterPresentation('numeric_range'),
    expression: {
      kind: 'range',
      lower: { value: { kind: 'decimal', value: '50' }, inclusive: true },
      upper: { value: { kind: 'decimal', value: '500' }, inclusive: true },
    },
  },
  {
    id: 'date-range', label: 'Date range', description: 'Start and end dates rearrange without overlap.', contract: 'slicer.date_range',
    definition: filterDefinition('purchase_date', 'Purchase date', 'date', 'range'),
    presentation: filterPresentation('date_range'),
    expression: {
      kind: 'range',
      lower: { value: { kind: 'date', value: '2026-01-01' }, inclusive: true },
      upper: { value: { kind: 'date', value: '2026-03-31' }, inclusive: true },
    },
  },
  {
    id: 'relative-period', label: 'Relative period', description: 'Direction, count, and unit remain explicit.', contract: 'slicer.relative_period',
    definition: filterDefinition('period', 'Relative period', 'timestamp', 'relative_period'),
    presentation: filterPresentation('relative_period'),
    expression: { kind: 'relative_period', direction: 'previous', count: 3, unit: 'month', includeCurrent: false, anchor: 'current_time' },
  },
]

class SiteResponsiveWidgetReference extends DatastarLit(LitElement) {
  private previewWidget: 'kpi' | 'date-range' = 'kpi'
  private previewWidth = 250
  private previewHeight = 130

  static styles = css`
    :host {
      display: grid;
      min-width: 0;
      gap: clamp(var(--base-size-40), 6vw, var(--base-size-64));
    }

    section,
    .section-heading,
    .scenario-copy,
    .playground-copy {
      display: grid;
    }

    section { gap: var(--base-size-20); }
    .section-heading { max-width: 48rem; gap: var(--base-size-6); }

    h2,
    h3,
    p { margin: 0; }

    h2,
    h3 { color: var(--lv-fg-default); }
    h2 { font-size: var(--text-title-size-large); line-height: var(--base-text-lineHeight-tight); }
    h3 { font-size: var(--text-title-size-medium); line-height: var(--base-text-lineHeight-tight); }
    p { color: var(--lv-fg-muted); line-height: var(--base-text-lineHeight-relaxed); }

    .scenario-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(min(100%, 33rem), 1fr));
      gap: var(--base-size-16);
    }

    .scenario {
      display: grid;
      min-width: 0;
      container-type: inline-size;
      align-content: start;
      gap: var(--base-size-16);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      box-shadow: var(--shadow-resting-small);
      padding: var(--base-size-16);
    }

    .scenario-copy { gap: var(--base-size-4); }

    .feature-list {
      display: flex;
      flex-wrap: wrap;
      gap: var(--base-size-4);
      margin-top: var(--base-size-4);
    }

    .feature {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      padding: var(--base-size-2) var(--base-size-8);
      font-size: var(--text-caption-size);
      line-height: var(--base-text-lineHeight-tight);
    }

    .frame-row {
      display: flex;
      min-width: 0;
      align-items: flex-start;
      gap: var(--base-size-16);
      overflow-x: auto;
      padding: var(--base-size-2) var(--base-size-2) var(--base-size-8);
    }

    figure {
      display: grid;
      flex: 0 0 auto;
      min-width: 0;
      gap: var(--base-size-8);
      margin: 0;
    }

    .layout-frame,
    .playground-frame {
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
    }

    .layout-frame lv-visualization-host,
    .layout-frame lv-slicer,
    .playground-frame lv-visualization-host,
    .playground-frame lv-slicer {
      display: block;
      width: 100%;
      height: 100%;
    }

    figcaption,
    .diagnostic {
      color: var(--lv-fg-muted);
      font-size: var(--text-caption-size);
      line-height: var(--base-text-lineHeight-tight);
    }

    figcaption { overflow-wrap: anywhere; }

    figcaption strong,
    .diagnostic strong { color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }

    .playground {
      display: grid;
      grid-template-columns: minmax(16rem, 22rem) minmax(0, 1fr);
      align-items: start;
      gap: var(--base-size-24);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      padding: var(--base-size-20);
    }

    .playground-copy { gap: var(--base-size-16); }
    .control { display: grid; gap: var(--base-size-6); }
    .control label { color: var(--lv-fg-default); font-size: var(--text-body-size-medium); font-weight: var(--base-text-weight-semibold); }
    .control-output { color: var(--lv-fg-muted); font-variant-numeric: tabular-nums; }
    select { min-height: var(--control-medium-size); border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); color: var(--lv-fg-default); padding-inline: var(--base-size-8); font: inherit; }
    input[type='range'] { width: 100%; accent-color: var(--lv-line-accent); }

    .playground-stage {
      min-width: 0;
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
      padding: var(--base-size-16);
    }

    .playground-stage figure { width: max-content; }
    .playground-frame[data-fit='too-small'] { outline: 2px solid var(--lv-line-danger); outline-offset: -2px; }

    @container (width < 532px) {
      .frame-row {
        flex-direction: column;
        overflow-x: visible;
      }
    }

    @media (width < 48rem) {
      .playground { grid-template-columns: minmax(0, 1fr); padding: var(--base-size-16); }
      .scenario { padding: var(--base-size-12); }
      .frame-row {
        flex-direction: column;
        overflow-x: auto;
      }
    }

    @media (width < 25rem) {
      .scenario { padding-inline: var(--base-size-8); }
    }
  `

  render() {
    const visuals = this.signal<VisualPayload[]>('visuals', [])
    const indexed = new Map(visuals.map((visual) => [visual.visualID, visual]))
    const scenarios = kpiScenarios.flatMap((scenario) => {
      const visual = indexed.get(scenario.id)
      return visual ? [{ scenario, visual }] : []
    })
    const playgroundKPI = indexed.get('revenue_kpi_favorable')
    return html`
      <section aria-labelledby="responsive-kpi-heading">
        <div class="section-heading">
          <h2 id="responsive-kpi-heading">KPI feature combinations</h2>
          <p>Each configuration uses one compiled payload in every registered layout. Feature chips describe authored intent; every fixed preview is an exact-minimum boundary test.</p>
        </div>
        <div class="scenario-grid">
          ${scenarios.map(({ scenario, visual }) => this.renderKPIScenario(scenario, visual))}
        </div>
      </section>
      <section aria-labelledby="responsive-filter-heading">
        <div class="section-heading">
          <h2 id="responsive-filter-heading">Dashboard filter controls</h2>
          <p>These are production slicer controls, not chart renderings. Every explicit field stays present while the layout changes around it; every fixed preview is an exact-minimum boundary test.</p>
        </div>
        <div class="scenario-grid">
          ${filterScenarios.map((scenario) => this.renderFilterScenario(scenario))}
        </div>
      </section>
      <section aria-labelledby="responsive-playground-heading">
        <div class="section-heading">
          <h2 id="responsive-playground-heading">Intermediate-size playground</h2>
          <p>Inspect dimensions between the fixed frames. A red inset marks a size that violates the hard minimum; content remains present for diagnosis.</p>
        </div>
        ${playgroundKPI ? this.renderPlayground(playgroundKPI) : html`<p>Loading compiled examples…</p>`}
      </section>
    `
  }

  private renderKPIScenario(scenario: KPIScenario, visual: VisualPayload) {
    const features = kpiLayoutFeatures(visual)
    return html`<article class="scenario" data-kpi-scenario=${scenario.id}>
      <div class="scenario-copy">
        <h3>${scenario.label}</h3>
        <p>${scenario.description}</p>
        <div class="feature-list" aria-label="Explicit features">
          ${(features.length ? features : ['value']).map((feature) => html`<span class="feature">${feature}</span>`)}
        </div>
      </div>
      <div class="frame-row">
        ${layoutRequirements('kpi', features).map((requirement) => this.renderKPIFrame(scenario.label, visual, requirement.layout, requirement.minimum.width, requirement.minimum.height))}
      </div>
    </article>`
  }

  private renderKPIFrame(label: string, visual: VisualPayload, layout: string, width: number, height: number) {
    const ariaLabel = `${label}, ${layout} layout, ${width}×${height}`
    return html`<figure data-layout-frame=${layout} aria-label=${ariaLabel} style=${frameWidth(width)}>
      <div class="layout-frame" style=${frameSize(width, height)}>
        <lv-visualization-host .envelope=${visual}></lv-visualization-host>
      </div>
      <figcaption><strong>${layout}</strong> · ${width}×${height}</figcaption>
    </figure>`
  }

  private renderFilterScenario(scenario: FilterScenario) {
    const chrome = widgetChrome(scenario.contract)
    const features = slicerLayoutFeatures(scenario.presentation)
    return html`<article class="scenario" data-filter-scenario=${scenario.id}>
      <div class="scenario-copy">
        <h3>${scenario.label}</h3>
        <p>${scenario.description}</p>
      </div>
      <div class="frame-row">
        ${layoutRequirements(scenario.contract, features).map((requirement) => {
          const width = requirement.minimum.width + chrome.width
          const height = requirement.minimum.height + chrome.height
          const ariaLabel = `${scenario.label}, ${requirement.layout} layout, ${width}×${height}`
          return html`<figure data-layout-frame=${requirement.layout} aria-label=${ariaLabel} style=${frameWidth(width)}>
            <div class="layout-frame" style=${frameSize(width, height)}>
              ${filterSlicer(scenario)}
            </div>
            <figcaption><strong>${requirement.layout}</strong> · ${width}×${height}</figcaption>
          </figure>`
        })}
      </div>
    </article>`
  }

  private renderPlayground(kpi: VisualPayload) {
    const isKPI = this.previewWidget === 'kpi'
    const filter = filterScenarios.find((scenario) => scenario.id === 'date-range')!
    const resolution = isKPI
      ? resolveKPIWidgetLayout(kpi, { width: this.previewWidth, height: this.previewHeight })
      : filterOuterResolution(filter, this.previewWidth, this.previewHeight)
    const selected = selectedLayout(resolution)
    return html`<div class="playground">
      <div class="playground-copy">
        <div class="control">
          <label for="preview-widget">Preview widget</label>
          <select id="preview-widget" aria-label="Preview widget" @change=${this.changePreviewWidget}>
            <option value="kpi" ?selected=${isKPI}>KPI · comparison and trend</option>
            <option value="date-range" ?selected=${!isKPI}>Filter · date range</option>
          </select>
        </div>
        <div class="control">
          <label for="preview-width">Preview width</label>
          <input id="preview-width" type="range" min="160" max="520" step="1" .value=${String(this.previewWidth)} aria-label="Preview width" @input=${this.changePreviewWidth}>
          <output class="control-output" for="preview-width">${this.previewWidth}px</output>
        </div>
        <div class="control">
          <label for="preview-height">Preview height</label>
          <input id="preview-height" type="range" min="80" max="320" step="1" .value=${String(this.previewHeight)} aria-label="Preview height" @input=${this.changePreviewHeight}>
          <output class="control-output" for="preview-height">${this.previewHeight}px</output>
        </div>
        <p class="diagnostic"><strong>${selected.layout}</strong> · ${resolution.kind === 'fit' ? 'fits' : 'below minimum'} · requires ${selected.minimum.width}×${selected.minimum.height}</p>
      </div>
      <div class="playground-stage">
        <figure>
          <div
            class="playground-frame"
            data-playground-frame
            data-selected-layout=${selected.layout}
            data-fit=${resolution.kind === 'fit' ? 'fit' : 'too-small'}
            style=${frameSize(this.previewWidth, this.previewHeight)}
          >
            ${isKPI ? html`<lv-visualization-host .envelope=${kpi}></lv-visualization-host>` : filterSlicer(filter)}
          </div>
          <figcaption><strong>${this.previewWidth}×${this.previewHeight}</strong> · selected ${selected.layout}</figcaption>
        </figure>
      </div>
    </div>`
  }

  private changePreviewWidget = (event: Event) => {
    this.previewWidget = (event.currentTarget as HTMLSelectElement).value === 'date-range' ? 'date-range' : 'kpi'
    this.requestUpdate()
  }

  private changePreviewWidth = (event: Event) => {
    this.previewWidth = Number((event.currentTarget as HTMLInputElement).value)
    this.requestUpdate()
  }

  private changePreviewHeight = (event: Event) => {
    this.previewHeight = Number((event.currentTarget as HTMLInputElement).value)
    this.requestUpdate()
  }
}

if (!customElements.get('lv-site-responsive-widget-reference')) {
  customElements.define('lv-site-responsive-widget-reference', SiteResponsiveWidgetReference)
}

function filterDefinition(
  id: string,
  label: string,
  valueKind: DashboardCompiledFilterDefinition['valueKind'],
  predicate: 'set' | 'comparison' | 'range' | 'relative_period',
  options: DashboardCompiledFilterDefinition['options'] = { kind: 'none', limit: 0, values: [] },
): DashboardCompiledFilterDefinition {
  return {
    id, label, field: `orders.${id}`, valueKind,
    predicates: [{ kind: predicate, operators: predicate === 'comparison' ? ['greater_than_or_equal'] : [] }],
    options, timezone: 'UTC', calendar: 'gregorian', weekStart: 'monday',
  }
}

function filterPresentation(style: DashboardFilterPresentation['style']): DashboardFilterPresentation {
  return { style, search: false, selectAll: false, showCounts: false, showSummary: false, compact: false }
}

function filterSlicer(scenario: FilterScenario) {
  return html`<lv-slicer
    .definition=${scenario.definition}
    .binding=${{ ...filterBinding, key: scenario.id, id: scenario.id, filter: scenario.definition.id }}
    .expression=${scenario.expression}
    .presentation=${scenario.presentation}
  ></lv-slicer>`
}

function slicerLayoutFeatures(presentation: DashboardFilterPresentation): WidgetLayoutFeature[] {
  return presentation.showSummary ? ['summary'] : []
}

function filterOuterResolution(scenario: FilterScenario, width: number, height: number): WidgetLayoutResolution {
  const chrome = widgetChrome(scenario.contract)
  return resolveWidgetLayout(scenario.contract, {
    width: Math.max(0, width - chrome.width),
    height: Math.max(0, height - chrome.height),
  }, slicerLayoutFeatures(scenario.presentation))
}

function selectedLayout(resolution: WidgetLayoutResolution) {
  return resolution.kind === 'fit' ? resolution : resolution.requirements.at(-1)!
}

function frameSize(width: number, height: number): string {
  return `width: ${width}px; height: ${height}px`
}

function frameWidth(width: number): string {
  return `width: ${width}px`
}

class SiteVisualShowcase extends DatastarLit(LitElement) {
  static styles = css`
    :host {
      display: block;
    }

    .showcase-section {
      display: grid;
      gap: var(--base-size-16);
    }

    .section-heading {
      display: grid;
      gap: var(--base-size-4);
    }

    h2,
    p {
      margin: 0;
    }

    h2 {
      color: var(--lv-fg-default);
      font-size: var(--text-title-size-large);
      font-weight: var(--base-text-weight-semibold);
      line-height: var(--base-text-lineHeight-tight);
    }

    p {
      color: var(--lv-fg-muted);
      font-size: var(--text-body-size-medium);
      line-height: var(--base-text-lineHeight-relaxed);
    }

    .chart-grid,
    .table-grid {
      display: grid;
      gap: var(--base-size-16);
    }

    .chart-grid {
      grid-template-columns: repeat(auto-fit, minmax(18rem, 1fr));
    }

    .table-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    .chart {
      min-width: 0;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
      box-shadow: var(--shadow-resting-small);
      overflow: hidden;
    }

    .chart .visual-frame {
      height: 20rem;
    }

    .table-card {
      min-width: 0;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-chart-surface);
      box-shadow: var(--shadow-resting-small);
      overflow: hidden;
    }

    .table-card .visual-frame {
      height: 26rem;
    }

    .table-card.featured {
      grid-column: 1 / -1;
    }

    .table-card.featured .visual-frame {
      height: 30rem;
    }

    .visual-frame {
      min-width: 0;
      overflow: hidden;
    }

    lv-visualization-host {
      display: block;
      height: 100%;
    }

    .visual-card-footer {
      display: flex;
      min-height: 3.25rem;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-12);
      padding: var(--base-size-8) var(--base-size-12);
      border-top: var(--lv-border-default);
      background: var(--lv-bg-panel-muted);
    }

    .visual-type {
      min-width: 0;
      overflow: hidden;
      color: var(--lv-fg-muted);
      font-size: var(--text-body-size-small);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .docs-link {
      display: inline-flex;
      min-height: 2rem;
      flex: none;
      align-items: center;
      gap: var(--base-size-4);
      padding: var(--base-size-4) var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      font-size: var(--text-body-size-small);
      font-weight: var(--base-text-weight-semibold);
      line-height: var(--base-text-lineHeight-normal);
      text-decoration: none;
    }

    .docs-link:hover,
    .docs-link:focus-visible {
      border-color: var(--lv-button-border-hover);
      background: var(--lv-button-bg-hover);
    }

    .docs-link:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    @media (width < 48rem) {
      .table-grid {
        grid-template-columns: minmax(0, 1fr);
      }

      .table-card.featured {
        grid-column: auto;
      }
    }
  `

  render() {
    const visuals = this.signal<VisualPayload[]>('visuals', [])
    const documents = this.signal<VisualShowcaseDocument[]>('visualDocuments', [])
    const entries = documents.flatMap((document) => {
      const visual = visuals.find((candidate) => candidate.visualID === document.visualID)
      return visual ? [{ document, visual }] : []
    })
    const charts = entries.filter(({ visual }) => !isTabularVisualType(visual.spec.kind))
    const tables = entries.filter(({ visual }) => isTabularVisualType(visual.spec.kind))
    return html`
      <section class="showcase-section" aria-labelledby="chart-showcase-heading">
        <div class="section-heading">
          <h2 id="chart-showcase-heading">Charts and KPIs</h2>
          <p>Renderer-neutral visual payloads adapted by the built-in ECharts and KPI renderers.</p>
        </div>
        <div class="chart-grid">${charts.map(({ document, visual }) => visualShowcaseCard(document, visual, 'chart'))}</div>
      </section>
      <section class="showcase-section" aria-labelledby="table-showcase-heading">
        <div class="section-heading">
          <h2 id="table-showcase-heading">Tables, matrices, and pivots</h2>
          <p>Virtualized table, matrix, and pivot payloads from the same generated visual catalog.</p>
        </div>
        <div class="table-grid">
          ${tables.map(({ document, visual }, index) => visualShowcaseCard(document, visual, `table-card ${index === 0 ? 'featured' : ''}`))}
        </div>
      </section>
    `
  }
}

function visualShowcaseCard(document: VisualShowcaseDocument, visual: VisualPayload, className: string) {
  const label = `Open ${document.title} documentation`
  return html`<article class=${className}>
    <div class="visual-frame"><lv-visualization-host .envelope=${visual}></lv-visualization-host></div>
    <footer class="visual-card-footer">
      <span class="visual-type">${document.title}</span>
      <a class="docs-link" href=${`/docs/${document.slug}`} aria-label=${label} title=${label}>
        ${lucideIcon(BookOpen, { size: 15, strokeWidth: 2 })}
        <span>View docs</span>
      </a>
    </footer>
  </article>`
}

if (!customElements.get('lv-site-visual-showcase')) {
  customElements.define('lv-site-visual-showcase', SiteVisualShowcase)
}

function isTabularVisualType(type: string): boolean {
  return type === 'table' || type === 'matrix' || type === 'pivot'
}

async function loadRouteComponents(): Promise<void> {
  const imports: Promise<unknown>[] = []
  if (document.querySelector('lv-site-visual-showcase, lv-site-visual-example, lv-site-responsive-widget-reference')) {
    imports.push(import('../../web/components/dashboard/visualization/host'))
  }
  if (document.querySelector('lv-site-responsive-widget-reference')) {
    imports.push(import('../../web/components/dashboard/filters/filter-control'))
  }
  if (document.querySelector('lv-site-flow-background')) {
    imports.push(import('./site-flow-background'))
  }
  await Promise.all(imports)
}

void loadRouteComponents()
