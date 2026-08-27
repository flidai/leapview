import { LitElement, css, html } from 'lit'
import { state } from 'lit/decorators.js'
import type { ChromeSignal, DashboardPageSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import '../navigation/sidebar'
import './product-search'

const emptyChrome: ChromeSignal = {
  sidebar: {
    productName: 'LeapView',
    active: '',
    dashboardId: '',
    dashboardTitle: '',
    pageTitle: '',
    userSettingsHref: '/admin/profile',
    modelId: '',
    modelTitle: '',
    compact: false,
    groups: [],
  },
}

class LeapViewAppShell extends DatastarLit(LitElement) {
  @state() private productSearchOpen = false

  static styles = css`
    :host {
      display: grid;
      min-height: 100svh;
      grid-template-columns: auto minmax(0, 1fr);
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    :host([data-dashboard]) {
      grid-template-columns: minmax(0, 1fr);
    }

    lv-sidebar {
      border-right: var(--lv-border-default);
      min-width: var(--lv-sidebar-width);
    }

    lv-sidebar[data-collapsed] {
      border-right: 0;
    }

    main {
      min-width: 0;
      min-height: 100svh;
    }

    ::slotted([slot='page']) {
      display: block;
      min-width: 0;
      min-height: 100svh;
    }

    @media (max-width: 640px) {
      :host {
        height: 100svh;
        min-height: 0;
        grid-template-columns: calc(var(--base-size-28) + (var(--base-size-8) * 2)) minmax(0, 1fr);
        grid-template-rows: minmax(0, 1fr);
        overflow: hidden;
      }

      :host([data-dashboard]) {
        grid-template-columns: minmax(0, 1fr);
      }

      lv-sidebar {
        border-right: 0;
        border-bottom: 0;
        min-width: 0;
      }

      main {
        min-height: 0;
        overflow-y: auto;
      }

      ::slotted([slot='page']) {
        min-height: 100%;
      }

      ::slotted(lv-dashboard-page[slot='page']) {
        height: 100%;
        min-height: 0;
      }
    }
  `

  updated(): void {
    checkSignalContract('chrome', this.chrome, { sidebar: 'required' })
    this.toggleAttribute('data-dashboard', this.isAppDashboard)
  }

  get chrome(): ChromeSignal {
    return this.signal<ChromeSignal>('chrome', emptyChrome)
  }

  connectedCallback(): void {
    super.connectedCallback()
    this.addEventListener('click', this.followSidebarLinkFromHost)
    this.addEventListener('product-search-open', this.openProductSearch)
    window.addEventListener('keydown', this.handleProductSearchShortcut)
  }

  disconnectedCallback(): void {
    this.removeEventListener('click', this.followSidebarLinkFromHost)
    this.removeEventListener('product-search-open', this.openProductSearch)
    window.removeEventListener('keydown', this.handleProductSearchShortcut)
    super.disconnectedCallback()
  }

  render() {
    return html`
      ${this.isAppDashboard ? null : html`<lv-sidebar .config=${this.chrome.sidebar}></lv-sidebar>`}
      <main>
        <slot name="page"></slot>
      </main>
      <lv-product-search
        .open=${this.productSearchOpen}
        @product-search-close=${this.closeProductSearch}
      ></lv-product-search>
    `
  }

  private get isAppDashboard(): boolean {
    const page = this.signal<DashboardPageSignal | null>('page', null)
    return page?.kind === 'dashboard' && page.presentation === 'app'
  }

  private openProductSearch = (): void => {
    this.productSearchOpen = true
  }

  private closeProductSearch = (): void => {
    this.productSearchOpen = false
  }

  private handleProductSearchShortcut = (event: KeyboardEvent): void => {
    if (event.defaultPrevented || event.repeat || event.altKey || event.key.toLocaleLowerCase() !== 'k' || (!event.metaKey && !event.ctrlKey)) return
    event.preventDefault()
    this.productSearchOpen = true
  }

  private followSidebarLinkFromHost = (event: MouseEvent): void => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return

    const sidebar = this.shadowRoot?.querySelector('lv-sidebar') as HTMLElement | null
    const root = sidebar?.shadowRoot
    if (!sidebar || !root) return

    const path = event.composedPath()
    if (event.target !== this && !path.includes(sidebar)) return
    if (path.some((node) => node instanceof HTMLAnchorElement)) return

    const sidebarRect = sidebar.getBoundingClientRect()
    if (event.clientX < sidebarRect.left || event.clientX > sidebarRect.right || event.clientY < sidebarRect.top || event.clientY > sidebarRect.bottom) return

    const link = Array.from(root.querySelectorAll<HTMLAnchorElement>('a[href]')).find((candidate) => {
      const rect = candidate.getBoundingClientRect()
      return event.clientX >= rect.left && event.clientX <= rect.right && event.clientY >= rect.top && event.clientY <= rect.bottom
    })
    if (!link) return

    const target = new URL(link.getAttribute('href') || '', window.location.href)
    if (target.origin !== window.location.origin || target.href === window.location.href) return

    event.preventDefault()
    window.location.assign(target.href)
  }
}

customElements.define('lv-app-shell', LeapViewAppShell)
