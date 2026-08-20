import { LitElement, css, html } from 'lit'
import { PanelLeftClose, PanelLeftOpen, X } from 'lucide'
import { lucideIcon } from '../../web/components/shared/lucide-icons'

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

function installDocsNavigation(): () => void {
  const handleDrawerRequest = (event: Event): void => {
    const requested = (event as CustomEvent<{ open?: boolean }>).detail?.open
    const currentlyOpen = document.querySelector('.site-docs-layout')?.classList.contains('site-docs-drawer-open') ?? false
    syncDocsDrawer(typeof requested === 'boolean' ? requested : !currentlyOpen)
  }

  const handleDocumentClick = (event: MouseEvent): void => {
    if ((event.target as Element).closest('[data-site-docs-drawer-close]')) syncDocsDrawer(false)
  }

  const handleDocumentKeydown = (event: KeyboardEvent): void => {
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
  }

  const handleResize = (): void => syncDocsDrawer(document.querySelector('.site-docs-layout')?.classList.contains('site-docs-drawer-open'))
  document.addEventListener('leapview-docs-drawer-request', handleDrawerRequest)
  document.addEventListener('click', handleDocumentClick)
  document.addEventListener('keydown', handleDocumentKeydown)
  window.addEventListener('resize', handleResize)
  const disposeSidebar = initializeDocsSidebarScroll()
  syncDocsDrawer()
  return () => {
    document.removeEventListener('leapview-docs-drawer-request', handleDrawerRequest)
    document.removeEventListener('click', handleDocumentClick)
    document.removeEventListener('keydown', handleDocumentKeydown)
    window.removeEventListener('resize', handleResize)
    disposeSidebar()
  }
}

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

function initializeDocsSidebarScroll(): () => void {
  const sidebar = document.querySelector<HTMLElement>('.site-docs-sidebar')
  if (!sidebar) return () => undefined

  let persistenceFrame = 0
  const handleScroll = (): void => {
    if (persistenceFrame !== 0) return
    persistenceFrame = requestAnimationFrame(() => {
      persistenceFrame = 0
      persistDocsSidebarScroll(sidebar)
    })
  }
  const handlePagehide = (): void => persistDocsSidebarScroll(sidebar)
  sidebar.addEventListener('scroll', handleScroll, { passive: true })
  window.addEventListener('pagehide', handlePagehide, { once: true })

  requestAnimationFrame(() => {
    restoreDocsSidebarScroll(sidebar)
    revealCurrentDocsLink()
  })
  return () => {
    if (persistenceFrame !== 0) cancelAnimationFrame(persistenceFrame)
    sidebar.removeEventListener('scroll', handleScroll)
    window.removeEventListener('pagehide', handlePagehide)
  }
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

const disposeInstalledDocsNavigation = installDocsNavigation()

/** Remove document/window listeners when a host tears down the site shell. */
export function disposeDocsNavigation(): void {
  disposeInstalledDocsNavigation()
}
