import { LitElement, css, html, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import {
	Activity,
	ArrowLeft,
	Bot,
	Database,
	Globe,
	History,
	Layers,
  LayoutDashboard,
  Menu,
  MessagesSquare,
	Monitor,
	Moon,
	PanelLeftClose,
	PanelLeftOpen,
	Plus,
	Plug,
	Search,
	Settings,
  Sun,
  TableProperties,
	Users,
	UsersRound,
	User,
  X,
  type IconNode,
} from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import { leapViewBrandName } from '../shared/brand-mark'
import '../shared/loading-spinner'

type NavItem = {
  id: string
  label: string
  href: string
  icon: string
  meta?: string
  disabled?: boolean
}

type NavGroup = {
  label: string
  items: NavItem[]
}

type SidebarConfig = {
  active: string
  admin?: boolean
  workspaceTitle?: string
  dashboardTitle?: string
  pageTitle?: string
  modelTitle?: string
  modelId?: string
  dashboardId?: string
  userRole?: string
  compact?: boolean
  primaryAction?: SidebarAction
  history?: SidebarHistory
  groups: NavGroup[]
}

type SidebarAction = {
  label: string
  href: string
  icon: IconName
}

type SidebarHistory = {
  label: string
  emptyText?: string
  items: SidebarHistoryItem[]
}

type SidebarHistoryItem = {
  id: string
  title: string
  href: string
  active?: boolean
  pending?: boolean
}

type SidebarStatus = {
  loading?: boolean
  lastUpdated?: string
  error?: string
}

type ThemeMode = 'system' | 'light' | 'dark'

type IconName =
  | 'catalog'
  | 'back'
  | 'bot'
  | 'database'
  | 'dashboard'
  | 'chat'
  | 'globe'
  | 'history'
  | 'model'
  | 'data'
  | 'cache'
  | 'settings'
  | 'system'
  | 'sun'
  | 'moon'
  | 'activity'
  | 'users'
  | 'users-round'
  | 'user'
  | 'search'
  | 'collapse'
  | 'expand'
  | 'menu'
  | 'close'
  | 'plus'

const defaultConfig: SidebarConfig = {
  active: 'dashboards',
  workspaceTitle: 'LeapView Workspace',
  groups: [
    { label: 'Workspace', items: [{ id: 'dashboards', label: 'Dashboards', href: '/', icon: 'dashboard' }] },
  ],
}

const configConverter = {
  fromAttribute(value: string | null): SidebarConfig {
    if (!value) return defaultConfig
    try {
      return { ...defaultConfig, ...JSON.parse(value) } as SidebarConfig
    } catch {
      return defaultConfig
    }
  },
  toAttribute(value: SidebarConfig): string {
    return JSON.stringify(value ?? defaultConfig)
  },
}

const statusConverter = {
  fromAttribute(value: string | null): SidebarStatus {
    if (!value) return {}
    try {
      return JSON.parse(value) as SidebarStatus
    } catch {
      return {}
    }
  },
  toAttribute(value: SidebarStatus): string {
    return JSON.stringify(value ?? {})
  },
}

class LeapViewSidebar extends LitElement {
  @property({ attribute: 'config', converter: configConverter }) config: SidebarConfig = defaultConfig
  @property({ attribute: 'status', converter: statusConverter }) status: SidebarStatus = {}
  @state() private mode: ThemeMode = storedThemeMode()
  @state() private collapsed = storedCollapsed()
  @state() private mobileOpen = false
  @state() private searchQuery = ''
  private collapseStateInitialized = false
  private mobileMediaQuery?: MediaQueryList

  static styles = css`
    :host {
      --lv-sidebar-width: var(--lv-sidebar-width-expanded);
      display: block;
      width: var(--lv-sidebar-width);
      min-height: 100svh;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      transition: width var(--motion-transition-stateChange);
    }

    :host([data-collapsed]) {
      --lv-sidebar-width: var(--lv-sidebar-width-collapsed);
    }

    aside {
      position: sticky;
      top: 0;
      display: grid;
      width: var(--lv-sidebar-width);
      min-height: 100svh;
      grid-template-rows: auto minmax(0, 1fr) auto;
      background: var(--lv-sidebar-bg);
      transition: width var(--motion-transition-stateChange);
    }

    .brand {
      display: grid;
      gap: var(--base-size-12);
      padding: var(--base-size-12);
    }

    .brand-row {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-12);
    }

    .brand-back {
      position: relative;
      box-sizing: border-box;
      display: grid;
      min-width: 0;
      flex: 1 1 auto;
      grid-template-columns: calc(var(--control-xsmall-size) + var(--base-size-2)) minmax(0, 1fr);
      min-height: var(--control-medium-size);
      align-items: center;
      gap: var(--base-size-8);
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      padding: 0 var(--control-xsmall-paddingInline-normal);
      text-decoration: none;
      font: var(--lv-type-body);
    }

    .brand-back:hover,
    .brand-back:focus-visible {
      background: var(--control-bgColor-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    .brand-back-icon {
      display: grid;
      width: var(--control-xsmall-size);
      height: var(--control-xsmall-size);
      flex: 0 0 auto;
      place-items: center;
    }

    .brand-back-text {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .name {
      overflow: hidden;
      min-width: 0;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body-large);
      font-weight: var(--base-text-weight-semibold);
      letter-spacing: 0;
    }

    .collapse-button {
      display: grid;
      width: var(--lv-button-height-xs);
      height: var(--lv-button-height-xs);
      flex: 0 0 auto;
      place-items: center;
      margin-left: auto;
      border: var(--borderWidth-default) solid var(--lv-button-invisible-border-rest);
      border-radius: var(--lv-button-radius);
      background: var(--lv-button-invisible-bg-rest);
      color: var(--lv-button-invisible-icon-rest);
      cursor: pointer;
      padding: 0;
    }

    .collapse-button:hover,
    .collapse-button:focus-visible {
      border-color: var(--lv-button-invisible-border-hover);
      background: var(--lv-button-invisible-bg-hover);
      color: var(--lv-fg-default);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .mobile-menu-button,
    .mobile-close-button,
    .mobile-backdrop,
    .mobile-drawer-header {
      display: none;
    }

    .mobile-header {
      display: none;
    }

    .sidebar-search {
      position: relative;
      display: grid;
      min-width: 0;
    }

    .sidebar-search-icon {
      position: absolute;
      top: 50%;
      left: var(--control-xsmall-paddingInline-normal);
      display: grid;
      width: var(--control-xsmall-size);
      height: var(--control-xsmall-size);
      place-items: center;
      color: var(--lv-fg-muted);
      pointer-events: none;
      transform: translateY(-50%);
    }

    .sidebar-search input {
      box-sizing: border-box;
      width: 100%;
      min-height: var(--control-medium-size);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control, var(--lv-bg-panel-muted));
      color: var(--lv-fg-default);
      padding: 0 var(--control-xsmall-paddingInline-normal) 0 calc(var(--control-xsmall-size) + var(--base-size-8));
      font: var(--lv-type-body);
    }

    .sidebar-search input::placeholder {
      color: var(--lv-fg-muted);
      opacity: 1;
    }

    .sidebar-search input:focus {
      border-color: var(--lv-fg-accent);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .mobile-sidebar-search {
      display: none;
    }

    nav {
      display: grid;
      align-content: start;
      gap: var(--base-size-8);
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
    }

    .nav-group {
      display: grid;
      gap: var(--base-size-2);
    }

    .nav-group-label {
      overflow: hidden;
      margin: var(--base-size-4) var(--control-xsmall-paddingInline-normal) var(--base-size-2);
      color: var(--fgColor-disabled);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-caption);
    }

    .search-empty {
      margin: var(--base-size-8) var(--control-xsmall-paddingInline-normal);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .primary-action {
      margin-bottom: var(--base-size-4);
    }

    .primary-action .nav-item {
      min-height: var(--control-medium-size);
      border-color: transparent;
      background: transparent;
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-medium);
    }

    .primary-action .nav-item:hover,
    .primary-action .nav-item:focus-visible {
      border-color: transparent;
      background: var(--control-bgColor-hover);
      color: var(--lv-fg-default);
    }

    .primary-action .nav-icon {
      width: calc(var(--control-xsmall-size) + var(--base-size-2));
      height: calc(var(--control-xsmall-size) + var(--base-size-2));
      border-radius: var(--lv-radius-full);
      background: var(--control-bgColor-hover);
      color: var(--lv-fg-default);
      transition:
        background var(--motion-transition-stateChange),
        transform var(--motion-transition-stateChange);
    }

    .primary-action .nav-item:hover .nav-icon,
    .primary-action .nav-item:focus-visible .nav-icon {
      background: var(--lv-bg-selected);
      transform: rotate(-3deg) scale(1.06);
    }

    .history {
      display: grid;
      gap: var(--base-size-4);
      min-height: 0;
      padding-top: var(--base-size-8);
    }

    .history-label {
      overflow: hidden;
      margin:
        0
        var(--control-xsmall-paddingInline-normal)
        0
        calc(var(--control-xsmall-paddingInline-normal) + var(--lv-border-width));
      color: var(--fgColor-disabled);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-caption);
      letter-spacing: 0;
    }

    .history-list {
      display: grid;
      gap: var(--base-size-2);
      min-height: 0;
    }

    .nav-item.history-item {
      grid-template-columns: minmax(0, 1fr) auto;
    }

    .history-title {
      overflow: hidden;
      min-width: 0;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body);
    }

    .history-empty {
      padding: var(--base-size-4) var(--control-xsmall-paddingInline-normal);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      line-height: var(--base-text-lineHeight-tight);
    }

    .pending-spinner {
      --lv-spinner-size: var(--lv-spinner-size-sm);
    }

    a,
    button {
      font: inherit;
    }

    .nav-item {
      position: relative;
      box-sizing: border-box;
      display: grid;
      grid-template-columns: calc(var(--control-xsmall-size) + var(--base-size-2)) minmax(0, 1fr) auto;
      min-height: var(--control-medium-size);
      align-items: center;
      gap: var(--base-size-8);
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      padding: 0 var(--control-xsmall-paddingInline-normal);
      text-decoration: none;
      font: var(--lv-type-body);
    }

    .nav-text {
      display: grid;
      gap: 0;
      min-width: 0;
    }

    .nav-text strong {
      overflow: hidden;
      color: inherit;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body);
    }

    .nav-item:hover,
    .nav-item:focus-visible {
      background: var(--control-bgColor-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    .nav-item[aria-current='page'] {
      border-color: transparent;
      background: var(--control-bgColor-hover);
      color: var(--lv-fg-default);
    }

    .nav-item[aria-current='page']::before {
      content: none;
    }

    .nav-item.disabled {
      cursor: not-allowed;
      opacity: var(--opacity-disabled);
    }

    .nav-icon {
      display: grid;
      width: var(--control-xsmall-size);
      height: var(--control-xsmall-size);
      place-items: center;
      border-radius: var(--lv-radius-default);
      background: transparent;
    }

    svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: 2;
    }

    .footer {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--base-size-6);
      align-items: center;
      padding: var(--base-size-8);
      border-top: var(--lv-border-muted);
      background: transparent;
    }

    .user-card {
      display: grid;
      grid-template-columns: var(--control-small-size) minmax(0, 1fr);
      min-height: calc(var(--control-medium-size) + var(--base-size-2));
      align-items: center;
      gap: var(--base-size-8);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      padding: 0 var(--control-xsmall-paddingInline-normal);
    }

    .user-card:hover {
      background: var(--control-bgColor-hover);
    }

    .avatar {
      display: grid;
      width: var(--control-xsmall-size);
      height: var(--control-xsmall-size);
      place-items: center;
      border-radius: 50%;
      background: var(--bgColor-neutral-muted);
      color: var(--lv-fg-default);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      letter-spacing: 0;
    }

    .user-text {
      display: grid;
      gap: var(--base-size-2);
      min-width: 0;
    }

    .user-name,
    .user-role {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .user-name {
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .user-role {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .actions {
      display: flex;
      gap: var(--base-size-4);
      align-items: center;
      justify-content: end;
    }

    .theme-button {
      display: inline-flex;
      width: var(--lv-button-height);
      height: var(--lv-button-height);
      min-height: var(--lv-button-height);
      align-items: center;
      justify-content: center;
      gap: var(--base-size-8);
      border: var(--borderWidth-default) solid var(--lv-button-border-rest);
      border-radius: var(--lv-button-radius);
      background: var(--lv-button-bg-rest);
      color: var(--lv-button-fg-rest);
      cursor: pointer;
      padding: 0;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    .theme-button:hover,
    .theme-button:focus-visible {
      border-color: var(--lv-button-border-hover);
      background: var(--lv-button-bg-hover);
      color: var(--lv-fg-default);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .theme-button {
      border-color: var(--lv-button-border-rest);
      background: var(--lv-button-bg-rest);
      color: var(--lv-button-fg-rest);
    }

    :host([data-collapsed]) .brand {
      justify-items: center;
      gap: 0;
      padding: var(--base-size-8) var(--base-size-6);
    }

    :host([data-collapsed]) .brand-row {
      display: grid;
      justify-items: center;
      gap: var(--base-size-8);
    }

    :host([data-collapsed]) .name,
    :host([data-collapsed]) .nav-group-label,
    :host([data-collapsed]) .nav-text,
    :host([data-collapsed]) .history,
    :host([data-collapsed]) .user-text {
      display: none;
    }

    :host([data-collapsed]) .collapse-button {
      margin-left: 0;
    }

    :host([data-collapsed]) nav {
      gap: var(--base-size-8);
      padding: var(--base-size-8) var(--base-size-4);
    }

    :host([data-collapsed]) .nav-group {
      justify-items: center;
      gap: var(--base-size-8);
    }

    :host([data-collapsed]) .nav-item {
      width: var(--base-size-36);
      min-height: var(--base-size-36);
      grid-template-columns: 1fr;
      justify-items: center;
      gap: 0;
      padding: 0;
    }

    :host([data-collapsed]) .nav-icon {
      width: var(--control-small-size);
      height: var(--control-small-size);
    }

    :host([data-collapsed]) .nav-item[aria-current='page']::before {
      content: none;
    }

    :host([data-collapsed]) .footer {
      grid-template-columns: 1fr;
      padding: var(--base-size-8) var(--base-size-4);
    }

    :host([data-collapsed]) .actions {
      display: grid;
      justify-content: center;
      justify-items: center;
    }

    :host([data-collapsed]) .theme-button {
      width: calc(var(--lv-button-height) + var(--base-size-2));
      min-height: calc(var(--lv-button-height) + var(--base-size-2));
      height: calc(var(--lv-button-height) + var(--base-size-2));
      padding: 0;
    }

    :host([data-collapsed]) .user-card {
      grid-template-columns: 1fr;
      justify-items: center;
      padding: 0;
    }

    @media (max-width: 640px) {
      :host,
      :host([data-collapsed]) {
        --lv-sidebar-width: 100%;
        width: 100%;
        min-height: var(--control-large-size);
      }

      aside {
        position: relative;
        display: block;
        width: 100%;
        min-height: var(--control-large-size);
      }

      .brand {
        display: none;
      }

      .mobile-header {
        display: flex;
        min-width: 0;
        min-height: var(--control-large-size);
        align-items: center;
        gap: var(--base-size-8);
        padding: 0 var(--base-size-12);
      }

      .mobile-header-title {
        min-width: 0;
        flex: 1 1 auto;
        color: var(--lv-fg-default);
        font: var(--lv-type-body-large);
        font-weight: var(--base-text-weight-semibold);
      }

      .mobile-sidebar-search {
        display: grid;
        margin-bottom: var(--base-size-8);
      }

      .mobile-menu-button,
      .mobile-close-button {
        display: inline-grid;
        width: var(--lv-button-height-xs);
        height: var(--lv-button-height-xs);
        place-items: center;
        border: var(--lv-border-transparent);
        border-radius: var(--lv-button-radius);
        background: transparent;
        color: var(--lv-fg-muted);
        cursor: pointer;
        padding: 0;
      }

      .mobile-menu-button:hover,
      .mobile-menu-button:focus-visible,
      .mobile-close-button:hover,
      .mobile-close-button:focus-visible {
        background: var(--control-bgColor-hover);
        color: var(--lv-fg-default);
        outline: var(--focus-outline);
        outline-offset: var(--focus-outline-offset);
      }

      .collapse-button,
      :host([data-collapsed]) .collapse-button {
        display: none;
      }

      .mobile-backdrop {
        position: fixed;
        z-index: var(--z-index-report-sidebar);
        inset: 0;
        display: block;
        border: 0;
        background: var(--lv-modal-backdrop);
        cursor: pointer;
        opacity: 0;
        pointer-events: none;
        transition: opacity var(--motion-transition-stateChange), visibility var(--motion-transition-stateChange);
        visibility: hidden;
      }

      nav {
        position: fixed;
        z-index: var(--z-index-sidebar);
        top: 0;
        bottom: 0;
        left: 0;
        box-sizing: border-box;
        display: grid;
        width: min(20rem, calc(100vw - var(--base-size-32)));
        min-height: 100svh;
        align-content: start;
        overflow-y: auto;
        border: 0;
        border-right: var(--lv-border-default);
        background: var(--lv-sidebar-bg);
        box-shadow: var(--lv-shadow-floating);
        padding: var(--base-size-12);
        pointer-events: none;
        transform: translateX(-100%);
        transition: transform var(--motion-transition-stateChange), visibility var(--motion-transition-stateChange);
        visibility: hidden;
      }

      aside[data-mobile-open] nav {
        pointer-events: auto;
        transform: translateX(0);
        visibility: visible;
      }

      aside[data-mobile-open] .mobile-backdrop {
        opacity: 1;
        pointer-events: auto;
        visibility: visible;
      }

      .mobile-drawer-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        margin-bottom: var(--base-size-8);
        border-bottom: var(--lv-border-muted);
        padding-bottom: var(--base-size-8);
      }

      .mobile-drawer-title {
        font: var(--lv-type-body-large);
        font-weight: var(--base-text-weight-semibold);
      }

      .history,
      :host([data-collapsed]) .history {
        display: grid;
      }

      .nav-group,
      :host([data-collapsed]) .nav-group {
        display: grid;
        gap: var(--base-size-2);
        min-width: 0;
      }

      :host([data-collapsed]) nav {
        gap: var(--base-size-8);
        padding: var(--base-size-12);
      }

      .nav-item,
      :host([data-collapsed]) .nav-item {
        width: 100%;
        min-height: var(--control-medium-size);
        grid-template-columns: calc(var(--control-xsmall-size) + var(--base-size-2)) minmax(0, 1fr) auto;
        justify-items: stretch;
        gap: var(--base-size-8);
        padding: 0 var(--control-xsmall-paddingInline-normal);
      }

      :host([data-collapsed]) .nav-text {
        display: grid;
      }

      :host([data-collapsed]) .nav-icon {
        width: var(--control-xsmall-size);
        height: var(--control-xsmall-size);
      }

      .footer {
        display: none;
      }
    }

  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('leapview-theme-applied', this.onThemeApplied as EventListener)
    document.addEventListener('keydown', this.onKeyDown)
    this.mobileMediaQuery = window.matchMedia('(max-width: 640px)')
    this.mobileMediaQuery.addEventListener('change', this.onMobileViewportChange)
    this.mode = storedThemeMode()
    this.syncCollapsedState()
  }

  disconnectedCallback(): void {
    document.removeEventListener('leapview-theme-applied', this.onThemeApplied as EventListener)
    document.removeEventListener('keydown', this.onKeyDown)
    this.mobileMediaQuery?.removeEventListener('change', this.onMobileViewportChange)
    this.mobileMediaQuery = undefined
    super.disconnectedCallback()
  }

  private onThemeApplied = (event: CustomEvent<{ mode: ThemeMode }>): void => {
    this.mode = normalizeThemeMode(event.detail?.mode)
  }

  private changeTheme(mode: ThemeMode): void {
    this.dispatchEvent(new CustomEvent('leapview-theme-change', {
      detail: { mode },
      bubbles: true,
      composed: true,
    }))
  }

  protected willUpdate(changedProperties: PropertyValues<this>): void {
    const previousConfig = changedProperties.get('config') as SidebarConfig | undefined
    const enteringCompactMode = this.config.compact && !previousConfig?.compact
    const adminModeChanged = this.config.admin !== previousConfig?.admin
    if (changedProperties.has('config') && (!this.collapseStateInitialized || enteringCompactMode || adminModeChanged)) {
      this.collapsed = this.config.admin ? false : storedCollapsed(this.config.compact)
      this.collapseStateInitialized = true
    }
  }

  protected updated(): void {
    this.syncCollapsedState()
  }

  private syncCollapsedState(): void {
    if (this.effectiveCollapsed) {
      this.setAttribute('data-collapsed', '')
    } else {
      this.removeAttribute('data-collapsed')
    }
  }

  private toggleCollapsed(): void {
    if (this.config.admin) return
    this.collapsed = !this.collapsed
    try {
      localStorage.setItem('leapview-sidebar-collapsed', String(this.collapsed))
    } catch {
      // Ignore storage failures; the current session state still updates.
    }
    this.dispatchEvent(new CustomEvent('lv-sidebar-collapse', {
      detail: { collapsed: this.collapsed },
      bubbles: true,
      composed: true,
    }))
  }

  private get isMobileViewport(): boolean {
    return this.mobileMediaQuery?.matches ?? (typeof window !== 'undefined' && window.matchMedia('(max-width: 640px)').matches)
  }

  private onMobileViewportChange = (event: MediaQueryListEvent): void => {
    if (!event.matches) this.mobileOpen = false
    this.requestUpdate()
  }

  private toggleMobileNavigation(): void {
    this.mobileOpen = !this.mobileOpen
    if (!this.mobileOpen) return
    void this.updateComplete.then(() => this.shadowRoot?.querySelector<HTMLElement>('nav a')?.focus())
  }

  private closeMobileNavigation(restoreFocus = false): void {
    if (!this.mobileOpen) return
    this.mobileOpen = false
    if (restoreFocus) {
      void this.updateComplete.then(() => this.shadowRoot?.querySelector<HTMLButtonElement>('.mobile-menu-button')?.focus())
    }
  }

  private onKeyDown = (event: KeyboardEvent): void => {
    if (event.key !== 'Escape' || !this.mobileOpen) return
    event.preventDefault()
    this.closeMobileNavigation(true)
  }

  render() {
    const collapsed = this.effectiveCollapsed
    const mobileNavigationClosed = this.isMobileViewport && !this.mobileOpen
    const groups = this.filteredGroups()
    return html`
      <aside aria-label="${leapViewBrandName} workspace" ?data-mobile-open=${this.mobileOpen}>
        <header class="brand">
          <div class="brand-row">
            ${this.config.admin && this.config.primaryAction ? html`
              <a
                class="nav-item brand-back"
                href=${this.config.primaryAction.href}
                aria-label=${this.config.primaryAction.label}
                title=${this.config.primaryAction.label}
                @click=${(event: MouseEvent) => this.followInternalLink(event, this.config.primaryAction!.href)}
              >
                <span class="brand-back-icon">${icon(this.config.primaryAction.icon)}</span>
                <span class="brand-back-text">${this.config.primaryAction.label}</span>
              </a>
            ` : html`<span class="name">${leapViewBrandName}</span>`}
            ${this.config.admin ? null : html`
              <button
                class="collapse-button"
                type="button"
                aria-label=${collapsed ? 'Expand navigation' : 'Collapse navigation'}
                aria-pressed=${String(collapsed)}
                title=${collapsed ? 'Expand navigation' : 'Collapse navigation'}
                @click=${this.toggleCollapsed}
              >
                ${icon(collapsed ? 'expand' : 'collapse')}
              </button>
            `}
          </div>
          ${this.config.admin ? this.renderSearch() : null}
        </header>

        <div class="mobile-header">
          ${this.config.admin && this.config.primaryAction ? html`
            <a
              class="mobile-header-title brand-back"
              href=${this.config.primaryAction.href}
              aria-label=${this.config.primaryAction.label}
              title=${this.config.primaryAction.label}
              @click=${(event: MouseEvent) => this.followInternalLink(event, this.config.primaryAction!.href)}
            >
              <span class="brand-back-icon">${icon(this.config.primaryAction.icon)}</span>
              <span class="brand-back-text">${this.config.primaryAction.label}</span>
            </a>
          ` : html`<strong class="mobile-header-title">${leapViewBrandName}</strong>`}
          <button
            class="mobile-menu-button"
            type="button"
            aria-label="Open navigation"
            aria-hidden=${String(this.mobileOpen)}
            aria-controls="mobile-navigation"
            aria-expanded=${String(this.mobileOpen)}
            title="Open navigation"
            ?inert=${this.mobileOpen}
            @click=${this.toggleMobileNavigation}
          >
            ${icon('menu')}
          </button>
        </div>

        <div class="mobile-backdrop" aria-hidden="true" @click=${() => this.closeMobileNavigation(true)}></div>

        <nav id="mobile-navigation" aria-label="Primary" aria-hidden=${String(mobileNavigationClosed)} ?inert=${mobileNavigationClosed}>
          <div class="mobile-drawer-header">
            ${this.config.admin && this.config.primaryAction ? html`
              <a
                class="mobile-drawer-title nav-item brand-back"
                href=${this.config.primaryAction.href}
                aria-label=${this.config.primaryAction.label}
                @click=${(event: MouseEvent) => this.followInternalLink(event, this.config.primaryAction!.href)}
              >
                <span class="brand-back-icon">${icon(this.config.primaryAction.icon)}</span>
                <span class="brand-back-text">${this.config.primaryAction.label}</span>
              </a>
            ` : html`<strong class="mobile-drawer-title">${leapViewBrandName}</strong>`}
            <button class="mobile-close-button" type="button" aria-label="Close navigation" title="Close navigation" @click=${() => this.closeMobileNavigation(true)}>
              ${icon('close')}
            </button>
          </div>
          ${this.config.admin ? this.renderSearch(true) : null}
          ${this.config.primaryAction && !this.config.admin ? html`
            <section class="nav-group primary-action" aria-label=${this.config.primaryAction.label}>
              ${this.renderLink({
                id: 'primary-action',
                label: this.config.primaryAction.label,
                href: this.config.primaryAction.href,
                icon: this.config.primaryAction.icon,
              })}
            </section>
          ` : null}
          ${groups.length > 0 ? groups.map((group) => html`
            <section class="nav-group" aria-label=${group.label}>
              <strong class="nav-group-label">${group.label}</strong>
              ${group.items.map((item) => item.disabled ? this.renderDisabledItem(item) : this.renderLink(item))}
            </section>
          `) : this.config.admin ? html`<p class="search-empty">No matching pages</p>` : null}
          ${this.renderHistory()}
        </nav>

        <footer class="footer">
          <div class="user-card" title="Jacob Nielsen">
            <span class="avatar" aria-hidden="true">JN</span>
            <span class="user-text">
              <strong class="user-name">Jacob Nielsen</strong>
              <span class="user-role">${this.config.userRole ?? 'Local workspace'}</span>
            </span>
          </div>
          <div class="actions">
            <button class="theme-button" type="button" aria-label=${this.themeLabel()} title=${this.themeTitle()} @click=${() => this.changeTheme(this.nextTheme())}>
              ${icon(this.themeIcon())}
            </button>
          </div>
        </footer>
      </aside>
    `
  }

  private get effectiveCollapsed(): boolean {
    return !this.config.admin && this.collapsed
  }

  private renderSearch(mobile = false) {
    return html`
      <label class=${mobile ? 'sidebar-search mobile-sidebar-search' : 'sidebar-search'}>
        <span class="sidebar-search-icon" aria-hidden="true">${icon('search')}</span>
        <input
          type="search"
          aria-label="Search admin navigation"
          placeholder="Search..."
          autocomplete="off"
          .value=${this.searchQuery}
          @input=${this.updateSearchQuery}
        />
      </label>
    `
  }

  private updateSearchQuery(event: Event): void {
    this.searchQuery = (event.target as HTMLInputElement).value
  }

  private filteredGroups(): NavGroup[] {
    if (!this.config.admin || !this.searchQuery.trim()) return this.config.groups
    const query = this.searchQuery.trim().toLocaleLowerCase()
    return this.config.groups.flatMap((group) => {
      const groupMatches = group.label.toLocaleLowerCase().includes(query)
      const items = groupMatches ? group.items : group.items.filter((item) => item.label.toLocaleLowerCase().includes(query))
      return items.length > 0 ? [{ ...group, items }] : []
    })
  }

  private nextTheme(): ThemeMode {
    if (this.mode === 'system') return 'light'
    if (this.mode === 'light') return 'dark'
    return 'system'
  }

  private themeLabel(): string {
    if (this.mode === 'system') return 'System'
    if (this.mode === 'light') return 'Light'
    return 'Dark'
  }

  private themeTitle(): string {
    const next = this.nextTheme()
    const nextLabel = next === 'system' ? 'System preference' : next === 'light' ? 'Light mode' : 'Dark mode'
    return `${this.themeLabel()} theme. Switch to ${nextLabel}.`
  }

  private themeIcon(): IconName {
    if (this.mode === 'system') return 'system'
    if (this.mode === 'light') return 'sun'
    return 'moon'
  }

  private renderLink(item: NavItem) {
    const current = item.id === this.config.active
    return html`
      <a class="nav-item" href=${item.href} aria-current=${current ? 'page' : 'false'} aria-label=${item.label} title=${item.label} @click=${(event: MouseEvent) => this.followInternalLink(event, item.href)}>
        <span class="nav-icon">${icon(item.icon)}</span>
        <span class="nav-text">
          <strong>${item.label}</strong>
        </span>
      </a>
    `
  }

  private renderDisabledItem(item: NavItem) {
    return html`
      <span class="nav-item disabled" aria-disabled="true" aria-label=${item.label} title=${item.label}>
        <span class="nav-icon">${icon(item.icon)}</span>
        <span class="nav-text">
          <strong>${item.label}</strong>
        </span>
      </span>
    `
  }

  private renderHistory() {
    const history = this.config.history
    if (!history) return null
    const items = Array.isArray(history.items) ? history.items : []
    return html`
      <section class="history" aria-label=${history.label || 'Chats'}>
        <strong class="history-label">${history.label || 'Chats'}</strong>
        <div class="history-list">
          ${items.length === 0 ? html`<span class="history-empty">${history.emptyText || 'No chats yet.'}</span>` : null}
          ${items.map((item) => this.renderHistoryItem(item))}
        </div>
      </section>
    `
  }

  private renderHistoryItem(item: SidebarHistoryItem) {
    const title = item.title || 'Conversation'
    return html`
      <a class="nav-item history-item" href=${item.href} aria-current=${item.active ? 'page' : 'false'} aria-label=${title} title=${title} @click=${(event: MouseEvent) => this.followInternalLink(event, item.href)}>
        <span class="history-title">${title}</span>
        ${item.pending ? html`<lv-loading-spinner class="pending-spinner" aria-label="Title loading"></lv-loading-spinner>` : null}
      </a>
    `
  }

  private followInternalLink(event: MouseEvent, href: string): void {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    const target = new URL(href, window.location.href)
    if (target.origin !== window.location.origin || target.href === window.location.href) return
    event.preventDefault()
    this.closeMobileNavigation()
    window.location.assign(target.href)
  }
}

function icon(name: string) {
  const icons: Record<IconName, IconNode> = {
    catalog: Layers,
    dashboard: LayoutDashboard,
    chat: MessagesSquare,
    bot: Bot,
    database: Database,
    globe: Globe,
    history: History,
    model: Database,
    data: Plug,
    cache: TableProperties,
    settings: Settings,
    system: Monitor,
    sun: Sun,
    moon: Moon,
    activity: Activity,
    back: ArrowLeft,
    users: Users,
    'users-round': UsersRound,
    user: User,
    search: Search,
    collapse: PanelLeftClose,
    expand: PanelLeftOpen,
    menu: Menu,
    close: X,
    plus: Plus,
  }

  return lucideIcon(icons[name as IconName] ?? Layers)
}

function storedCollapsed(fallback = false): boolean {
  try {
    const stored = localStorage.getItem('leapview-sidebar-collapsed')
    if (stored === 'true') return true
    if (stored === 'false') return false
  } catch {
    // Ignore storage failures and use the route's default state.
  }
  return fallback
}

function storedThemeMode(): ThemeMode {
  try {
    return normalizeThemeMode(localStorage.getItem('leapview-color-mode') || document.documentElement.dataset.colorMode)
  } catch {
    return normalizeThemeMode(document.documentElement.dataset.colorMode)
  }
}

function normalizeThemeMode(mode: string | null | undefined): ThemeMode {
  if (mode === 'light' || mode === 'dark') return mode
  return 'system'
}

customElements.define('lv-sidebar', LeapViewSidebar)
