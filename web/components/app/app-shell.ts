import { LitElement, css, html } from 'lit'
import { state } from 'lit/decorators.js'
import type { ChromeSignal, DashboardPageSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import '../navigation/sidebar'
import './product-search'
import '../chat/chat-manager'
import type { ChatManager, ChatAction } from '../chat/chat-manager'

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
  @state() private pendingRemovalId = ''

  static styles = css`
    :host {
      display: grid;
      height: 100svh;
      min-height: 0;
      grid-template-columns: auto minmax(0, 1fr);
      overflow: hidden;
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
      min-height: 0;
      overflow-y: auto;
      overscroll-behavior: contain;
    }

    ::slotted([slot='page']) {
      display: block;
      min-width: 0;
      min-height: 100%;
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
    this.addEventListener('lv-chat-action', this.handleChatAction)
    this.addEventListener('lv-chat-settings-open', this.openChatSettings)
    window.addEventListener('keydown', this.handleProductSearchShortcut)
  }

  disconnectedCallback(): void {
    this.removeEventListener('click', this.followSidebarLinkFromHost)
    this.removeEventListener('product-search-open', this.openProductSearch)
    this.removeEventListener('lv-chat-action', this.handleChatAction)
    this.removeEventListener('lv-chat-settings-open', this.openChatSettings)
    window.removeEventListener('keydown', this.handleProductSearchShortcut)
    super.disconnectedCallback()
  }

  render() {
    return html`
      ${this.isAppDashboard ? null : html`<lv-sidebar .config=${this.chrome.sidebar} .pendingRemovalId=${this.pendingRemovalId}></lv-sidebar>`}
      <main>
        <slot name="page"></slot>
      </main>
      <lv-chat-manager @lv-chat-removal-pending=${(event: CustomEvent<{ conversationId: string }>) => { this.pendingRemovalId = event.detail.conversationId }}></lv-chat-manager>
      <lv-product-search
        .open=${this.productSearchOpen}
        @product-search-close=${this.closeProductSearch}
      ></lv-product-search>
    `
  }

  private handleChatAction = (event: Event) => {
    event.stopPropagation()
    this.renderRoot.querySelector<ChatManager>('lv-chat-manager')?.requestAction((event as CustomEvent<ChatAction>).detail)
  }

  private openChatSettings = (event: Event) => {
    event.stopPropagation()
    this.renderRoot.querySelector<ChatManager>('lv-chat-manager')?.openArchives()
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
    if (path.some((node) => node instanceof HTMLAnchorElement || node instanceof HTMLButtonElement)) return

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
