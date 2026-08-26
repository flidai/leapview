import { LitElement, css, html, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import {
	Activity,
	ArrowLeft,
	Bot,
	Boxes,
	ChartNoAxesCombined,
	Code2,
	Database,
	Globe,
	History,
	Layers,
  LayoutDashboard,
  Menu,
  MessagesSquare,
	Monitor,
	PanelLeftClose,
	PanelLeftOpen,
	Plus,
	Plug,
	Search,
	Settings,
  TableProperties,
	Users,
	UsersRound,
	User,
	Waypoints,
  Workflow,
  X,
  type IconNode,
} from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import { leapViewBrandName } from '../shared/brand-mark'
import '../shared/loading-spinner'
import '../shared/user-avatar'

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

type SidebarArea = {
  id: string
  label: string
  href: string
  icon: string
}

type SidebarConfig = {
  active: string
  admin?: boolean
  area?: string
  areas?: SidebarArea[]
  productLogoUrl?: string
  productName?: string
  dashboardTitle?: string
  pageTitle?: string
  modelTitle?: string
  modelId?: string
  dashboardId?: string
  userRole?: string
  compact?: boolean
  primaryAction?: SidebarAction
  history?: SidebarHistory
  userAvatarUrl?: string
  userName?: string
  userSettingsHref?: string
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

type IconName =
  | 'catalog'
  | 'back'
  | 'bot'
  | 'database'
  | 'dashboard'
  | 'chat'
  | 'globe'
  | 'history'
  | 'insights'
  | 'model'
  | 'boxes'
  | 'waypoints'
  | 'data'
  | 'cache'
  | 'settings'
  | 'system'
  | 'activity'
  | 'users'
  | 'users-round'
  | 'user'
  | 'search'
  | 'collapse'
  | 'expand'
  | 'menu'
  | 'close'
  | 'code'
  | 'plus'
  | 'workflow'

const defaultConfig: SidebarConfig = {
  active: 'dashboards',
  area: 'insights',
  areas: [
    { id: 'insights', label: 'Insights', href: '/', icon: 'insights' },
    { id: 'develop', label: 'Develop', href: '/sources', icon: 'code' },
  ],
  userSettingsHref: '/admin/profile',
  groups: [
    { label: 'Insights', items: [{ id: 'dashboards', label: 'Dashboards', href: '/', icon: 'dashboard' }, { id: 'data-explorer', label: 'Data Explorer', href: '/explore', icon: 'database' }] },
  ],
}

const SIDEBAR_MIN_WIDTH = 160
const SIDEBAR_MAX_WIDTH = 384
const SIDEBAR_RESIZE_STEP = 8
const SIDEBAR_DEFAULT_WIDTH = 248
const ADMIN_SIDEBAR_DEFAULT_WIDTH = 192

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
  @state() private collapsed = storedCollapsed()
  @state() private mobileOpen = false
  @state() private searchQuery = ''
  @state() private liveUserAvatarUrl: string | undefined
  @state() private sidebarWidth = SIDEBAR_DEFAULT_WIDTH
  private collapseStateInitialized = false
  private loadedWidthStorageKey = ''
  private mobileMediaQuery?: MediaQueryList
  private resizeDrag?: { pointerId: number; startX: number; startWidth: number }

  static styles = css`
    :host {
      --lv-sidebar-width-default: var(--lv-sidebar-width-expanded);
      --lv-sidebar-width: var(--lv-sidebar-resized-width, var(--lv-sidebar-width-default));
      box-sizing: border-box;
      display: block;
      width: var(--lv-sidebar-width);
      height: 100svh;
      min-height: 0;
      max-height: 100svh;
      position: sticky;
      top: 0;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      transition: width var(--motion-transition-stateChange);
    }

    :host([data-collapsed]) {
      --lv-sidebar-width: var(--lv-sidebar-width-collapsed);
    }

    :host([data-admin]) {
      --lv-sidebar-width-default: var(--lv-admin-sidebar-width-expanded);
    }

    :host([data-resizing]),
    :host([data-resizing]) aside {
      transition: none;
      user-select: none;
    }

    :host([data-admin]) .brand {
      gap: var(--base-size-8);
      padding: var(--base-size-8);
    }

    :host([data-admin]) .brand-back,
    :host([data-admin]) .nav-item {
      min-height: var(--control-small-size);
      font: var(--lv-type-body-compact);
    }

    :host([data-admin]) .nav-text strong,
    :host([data-admin]) .sidebar-search input {
      font: var(--lv-type-body-compact);
    }

    :host([data-admin]) .sidebar-search input {
      min-height: var(--control-small-size);
    }

    aside {
      position: relative;
      display: grid;
      width: 100%;
      height: 100%;
      min-height: 0;
      max-height: 100%;
      grid-template-rows: auto minmax(0, 1fr) auto;
      background: var(--lv-sidebar-bg);
      transition: width var(--motion-transition-stateChange);
    }

    .resize-handle {
      position: absolute;
      z-index: calc(var(--zIndex-default) + 1);
      top: 0;
      right: calc(var(--base-size-4) * -1);
      bottom: 0;
      width: var(--base-size-8);
      border: 0;
      cursor: col-resize;
      outline: none;
      touch-action: none;
    }

    .resize-handle::after {
      content: '';
      position: absolute;
      top: 0;
      bottom: 0;
      left: calc(50% - var(--borderWidth-thin));
      width: var(--borderWidth-thin);
      background: transparent;
      transition: background var(--motion-transition-stateChange);
    }

    .resize-handle:hover::after,
    .resize-handle:focus-visible::after,
    :host([data-resizing]) .resize-handle::after {
      background: var(--borderColor-accent-emphasis);
    }

    :host([data-collapsed]) .resize-handle {
      display: none;
    }

    .brand {
      position: relative;
      box-sizing: border-box;
      display: grid;
      width: 100%;
      min-width: 0;
      gap: var(--base-size-12);
      padding: var(--base-size-12) var(--base-size-12) var(--base-size-8);
    }

    .brand-row {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-12);
    }

		.area-switcher {
			display: grid;
			grid-template-columns: repeat(2, minmax(0, 1fr));
			gap: var(--base-size-2);
			border: var(--lv-border-muted);
			border-radius: var(--lv-radius-default);
			background: var(--lv-bg-panel-muted, var(--control-bgColor-hover));
			padding: var(--base-size-2);
    }

    .area-item {
      display: grid;
      min-width: 0;
      min-height: var(--control-small-size);
      grid-template-columns: calc(var(--control-xsmall-size) + var(--base-size-2)) minmax(0, 1fr);
      align-items: center;
      justify-content: center;
      gap: var(--base-size-4);
      border-radius: calc(var(--lv-radius-default) - var(--base-size-2));
      color: var(--lv-fg-muted);
      padding: 0 var(--base-size-6);
      text-decoration: none;
      font: var(--lv-type-body-compact);
      transition:
        background-color var(--motion-transition-stateChange),
        box-shadow var(--motion-transition-stateChange),
        color var(--motion-transition-stateChange);
    }

    .area-item:hover,
    .area-item:focus-visible {
      background: var(--control-bgColor-hover);
      color: var(--lv-fg-default);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

		.area-item[aria-current='page'] {
			background: var(--lv-bg-accent-muted, var(--control-bgColor-hover));
			box-shadow: var(--lv-shadow-floating-sm, var(--lv-shadow-floating, none));
			color: var(--lv-fg-default);
			font-weight: var(--base-text-weight-medium);
    }

    .area-item[aria-current='page'] .area-icon {
      color: var(--lv-fg-accent);
    }

    .collapsed-area-switcher {
      display: none;
      position: relative;
      justify-self: center;
    }

    .collapsed-area-switch {
      position: relative;
      box-sizing: border-box;
      display: grid;
      width: var(--base-size-36);
      min-height: var(--base-size-36);
      place-items: center;
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-accent-muted, var(--control-bgColor-hover));
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: 0;
      text-decoration: none;
    }

    .collapsed-area-switch .area-icon {
      width: var(--base-size-16);
      height: var(--base-size-16);
      color: var(--lv-fg-accent);
    }

    .collapsed-area-switch:hover,
    .collapsed-area-switch:focus-visible {
      background: var(--control-bgColor-hover);
      color: var(--lv-fg-default);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .area-icon {
      display: grid;
      width: var(--control-xsmall-size);
      height: var(--control-xsmall-size);
      place-items: center;
    }

    .area-label {
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
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

    .brand-identity {
      display: grid;
      min-width: 0;
      flex: 1 1 auto;
      grid-template-columns: auto minmax(0, 1fr);
      align-items: center;
      column-gap: var(--base-size-8);
    }

    .product-logo {
      width: var(--control-small-size);
      height: var(--control-small-size);
      grid-row: 1 / span 2;
      border-radius: var(--lv-radius-small);
      object-fit: contain;
    }

    .powered-by {
      overflow: hidden;
      color: var(--lv-fg-muted);
      text-overflow: ellipsis;
      white-space: nowrap;
      text-decoration: none;
      font: var(--lv-type-caption);
    }

    .powered-by:hover,
    .powered-by:focus-visible {
      color: var(--lv-fg-default);
      text-decoration: underline;
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
    .mobile-drawer-header,
    .mobile-area-switcher,
    .mobile-footer {
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
      box-sizing: border-box;
      display: grid;
      width: 100%;
      min-width: 0;
      align-content: start;
      gap: var(--base-size-8);
      min-height: 0;
      overflow-x: hidden;
      overflow-y: auto;
      overscroll-behavior: contain;
      padding: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      scrollbar-color: var(--lv-scrollbar-thumb) transparent;
      scrollbar-gutter: auto;
      scrollbar-width: thin;
    }

    nav::-webkit-scrollbar {
      width: var(--base-size-6);
    }

    nav::-webkit-scrollbar-track {
      background: transparent;
    }

    nav::-webkit-scrollbar-thumb {
      border-radius: var(--lv-radius-full);
      background: var(--lv-scrollbar-thumb);
    }

    nav::-webkit-scrollbar-thumb:hover {
      background: var(--lv-scrollbar-thumb-hover);
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
        calc(var(--base-size-12) + var(--lv-border-width));
      color: var(--lv-fg-muted);
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
      padding: var(--base-size-4) var(--base-size-12);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
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

    nav .nav-item {
      gap: var(--base-size-4);
      padding-inline: var(--base-size-12);
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
      box-sizing: border-box;
      display: grid;
      width: 100%;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr);
      gap: var(--base-size-6);
      align-items: center;
      padding: var(--base-size-8);
      border-top: var(--lv-border-muted);
      background: transparent;
    }

    .user-card {
      box-sizing: border-box;
      display: grid;
      min-width: 0;
      grid-template-columns: var(--control-small-size) minmax(0, 1fr) var(--control-xsmall-size);
      min-height: calc(var(--control-medium-size) + var(--base-size-2));
      align-items: center;
      gap: var(--base-size-4);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-12);
      text-decoration: none;
    }

    .user-card:hover,
    .user-card:focus-visible {
      background: var(--control-bgColor-hover);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .user-settings-icon {
      display: grid;
      width: var(--control-xsmall-size);
      height: var(--control-xsmall-size);
      place-items: center;
      color: var(--lv-fg-muted);
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

    :host([data-collapsed]) .brand {
      justify-items: center;
      gap: var(--base-size-4);
      padding: var(--base-size-8) var(--base-size-4);
    }

    :host([data-collapsed]) .brand-row {
      display: grid;
      justify-items: center;
      gap: var(--base-size-8);
    }

    :host([data-collapsed]) .name,
    :host([data-collapsed]) .powered-by,
    :host([data-collapsed]) .nav-group-label,
    :host([data-collapsed]) .nav-text,
    :host([data-collapsed]) .history,
    :host([data-collapsed]) .user-text {
      display: none;
    }

    :host([data-collapsed]) .user-settings-icon {
      display: none;
    }

    :host([data-collapsed]) .area-switcher:not(.mobile-area-switcher) {
      display: none;
    }

    :host([data-collapsed]) .collapsed-area-switcher {
      display: grid;
    }

    :host([data-collapsed]) .area-item {
      width: var(--base-size-36);
      min-height: var(--base-size-36);
      grid-template-columns: 1fr;
      justify-items: center;
      padding: 0;
    }

    :host([data-collapsed]) .area-label {
      display: none;
    }

    :host([data-collapsed]) .brand-identity {
      display: grid;
      flex: none;
      grid-template-columns: auto;
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

    :host([data-collapsed]) .user-card {
      grid-template-columns: 1fr;
      justify-items: center;
      padding: 0;
    }

    @media (max-width: 640px) {
      :host,
      :host([data-collapsed]) {
        --lv-sidebar-width: 100%;
        position: relative;
        top: auto;
        width: 100%;
        height: auto;
        min-height: var(--control-large-size);
        max-height: none;
        overflow: visible;
      }

      aside {
        position: relative;
        display: block;
        width: 100%;
        height: auto;
        min-height: var(--control-large-size);
        max-height: none;
        overflow: visible;
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
      :host([data-collapsed]) .collapse-button,
      .resize-handle {
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
        display: flex;
        flex-direction: column;
        width: min(20rem, calc(100vw - var(--base-size-32)));
        min-height: 100svh;
        overflow-y: auto;
        overscroll-behavior: contain;
        border: 0;
        border-right: var(--lv-border-default);
        background: var(--lv-sidebar-bg);
        box-shadow: var(--lv-shadow-floating);
        padding: var(--base-size-12);
        pointer-events: none;
        transform: translateX(-100%);
        transition: transform var(--motion-transition-stateChange), visibility var(--motion-transition-stateChange);
        visibility: hidden;
        scrollbar-gutter: auto;
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

			.area-switcher,
			:host([data-collapsed]) .area-switcher {
				display: grid;
				grid-template-columns: repeat(2, minmax(0, 1fr));
				justify-items: stretch;
				margin-bottom: var(--base-size-8);
				border: 0;
				background: transparent;
				padding: 0;
			}

      .area-item,
      :host([data-collapsed]) .area-item {
        width: auto;
        min-height: var(--control-small-size);
        grid-template-columns: var(--control-xsmall-size) minmax(0, auto);
        justify-items: stretch;
        padding: 0 var(--base-size-6);
      }

      .area-label,
      :host([data-collapsed]) .area-label {
        display: block;
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

      .mobile-footer {
        display: grid;
        margin-top: auto;
        border-top: var(--lv-border-muted);
        padding-top: var(--base-size-8);
      }
    }

  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('keydown', this.onKeyDown)
    document.addEventListener('leapview-avatar-change', this.onAvatarChange as EventListener)
    this.mobileMediaQuery = window.matchMedia('(max-width: 640px)')
    this.mobileMediaQuery.addEventListener('change', this.onMobileViewportChange)
    this.syncCollapsedState()
  }

  disconnectedCallback(): void {
    document.removeEventListener('keydown', this.onKeyDown)
    document.removeEventListener('leapview-avatar-change', this.onAvatarChange as EventListener)
    this.mobileMediaQuery?.removeEventListener('change', this.onMobileViewportChange)
    this.mobileMediaQuery = undefined
    super.disconnectedCallback()
  }

  protected willUpdate(changedProperties: PropertyValues<this>): void {
    const previousConfig = changedProperties.get('config') as SidebarConfig | undefined
    const enteringCompactMode = this.config.compact && !previousConfig?.compact
    const adminModeChanged = this.config.admin !== previousConfig?.admin
    if (changedProperties.has('config') && (!this.collapseStateInitialized || enteringCompactMode || adminModeChanged)) {
      this.collapsed = this.config.admin ? false : storedCollapsed(this.config.compact)
      this.collapseStateInitialized = true
    }
    if (changedProperties.has('config')) this.syncSidebarWidth()
  }

  protected updated(changedProperties: PropertyValues<this>): void {
    if (changedProperties.has('config')) this.rememberCurrentArea()
    this.toggleAttribute('data-admin', Boolean(this.config.admin))
    if (this.config.area) this.setAttribute('data-area', this.config.area)
    else this.removeAttribute('data-area')
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
    if (event.key !== 'Escape') return
    if (!this.mobileOpen) return
    event.preventDefault()
    this.closeMobileNavigation(true)
  }

  render() {
    const collapsed = this.effectiveCollapsed
    const mobileNavigationClosed = this.isMobileViewport && !this.mobileOpen
    const groups = this.filteredGroups()
    const productName = this.config.productName?.trim() || leapViewBrandName
    const productLogoUrl = this.config.productLogoUrl?.trim()
    const hasCustomIdentity = productName !== leapViewBrandName || Boolean(productLogoUrl)
    return html`
      <aside aria-label="${productName} navigation" ?data-mobile-open=${this.mobileOpen}>
        <span
          class="resize-handle"
          role="separator"
          tabindex="0"
          aria-label="Resize navigation sidebar"
          aria-orientation="vertical"
          aria-valuemin=${SIDEBAR_MIN_WIDTH}
          aria-valuemax=${SIDEBAR_MAX_WIDTH}
          aria-valuenow=${this.sidebarWidth}
          title="Drag to resize. Double-click to reset."
          @keydown=${this.resizeSidebarByKeyboard}
          @pointerdown=${this.beginSidebarResize}
          @pointermove=${this.continueSidebarResize}
          @pointerup=${this.endSidebarResize}
          @pointercancel=${this.endSidebarResize}
          @dblclick=${this.resetSidebarWidth}
        ></span>
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
            ` : html`
              <span class="brand-identity">
                ${productLogoUrl ? html`<img class="product-logo" src=${productLogoUrl} alt="">` : null}
                <span class="name">${productName}</span>
                ${hasCustomIdentity ? html`<a class="powered-by" href="https://leapview.dev" target="_blank" rel="noreferrer">Powered by LeapView</a>` : null}
              </span>
            `}
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
          ${this.config.admin ? null : this.renderAreaSwitcher()}
          ${this.config.admin ? null : this.renderCollapsedAreaSwitch()}
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
          ` : html`<strong class="mobile-header-title">${productName}</strong>`}
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
            ` : html`<strong class="mobile-drawer-title">${productName}</strong>`}
            <button class="mobile-close-button" type="button" aria-label="Close navigation" title="Close navigation" @click=${() => this.closeMobileNavigation(true)}>
              ${icon('close')}
            </button>
          </div>
          ${this.config.admin ? null : this.renderAreaSwitcher(true)}
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
              ${this.config.admin ? html`<strong class="nav-group-label">${group.label}</strong>` : null}
              ${group.items.map((item) => item.disabled ? this.renderDisabledItem(item) : this.renderLink(item))}
            </section>
          `) : this.config.admin ? html`<p class="search-empty">No matching pages</p>` : null}
          ${this.renderHistory()}
          ${this.config.admin ? null : html`<div class="mobile-footer">${this.renderUserCard()}</div>`}
        </nav>

        <footer class="footer">
          ${this.renderUserCard()}
        </footer>
      </aside>
    `
  }

  private onAvatarChange = (event: CustomEvent<{ url?: string }>): void => {
    this.liveUserAvatarUrl = event.detail?.url?.trim() || ''
  }

  private get widthStorageKey(): string {
    return this.config.admin ? 'leapview-admin-sidebar-width' : 'leapview-sidebar-width'
  }

  private get defaultSidebarWidth(): number {
    return this.config.admin ? ADMIN_SIDEBAR_DEFAULT_WIDTH : SIDEBAR_DEFAULT_WIDTH
  }

  private syncSidebarWidth(): void {
    const storageKey = this.widthStorageKey
    if (storageKey === this.loadedWidthStorageKey) return
    this.loadedWidthStorageKey = storageKey
    const storedWidth = storedSidebarWidth(storageKey)
    this.sidebarWidth = storedWidth ?? this.defaultSidebarWidth
    if (storedWidth === undefined) {
      this.style.removeProperty('--lv-sidebar-resized-width')
    } else {
      this.style.setProperty('--lv-sidebar-resized-width', `${storedWidth}px`)
    }
  }

  private applySidebarWidth(width: number, persist: boolean): void {
    const nextWidth = Math.round(Math.min(SIDEBAR_MAX_WIDTH, Math.max(SIDEBAR_MIN_WIDTH, width)))
    this.sidebarWidth = nextWidth
    this.style.setProperty('--lv-sidebar-resized-width', `${nextWidth}px`)
    if (!persist) return
    try {
      localStorage.setItem(this.widthStorageKey, String(nextWidth))
    } catch {
      // Ignore storage failures; the current session state still updates.
    }
  }

  private beginSidebarResize = (event: PointerEvent): void => {
    if (event.button !== 0 || this.isMobileViewport || this.effectiveCollapsed) return
    event.preventDefault()
    const handle = event.currentTarget as HTMLElement
    this.resizeDrag = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startWidth: this.getBoundingClientRect().width,
    }
    handle.setPointerCapture?.(event.pointerId)
    this.toggleAttribute('data-resizing', true)
  }

  private continueSidebarResize = (event: PointerEvent): void => {
    if (!this.resizeDrag || event.pointerId !== this.resizeDrag.pointerId) return
    this.applySidebarWidth(this.resizeDrag.startWidth + event.clientX - this.resizeDrag.startX, false)
  }

  private endSidebarResize = (event: PointerEvent): void => {
    if (!this.resizeDrag || event.pointerId !== this.resizeDrag.pointerId) return
    const handle = event.currentTarget as HTMLElement
    if (handle.hasPointerCapture?.(event.pointerId)) handle.releasePointerCapture(event.pointerId)
    this.resizeDrag = undefined
    this.toggleAttribute('data-resizing', false)
    this.applySidebarWidth(this.sidebarWidth, true)
  }

  private resizeSidebarByKeyboard = (event: KeyboardEvent): void => {
    let nextWidth: number | undefined
    if (event.key === 'ArrowLeft') nextWidth = this.sidebarWidth - SIDEBAR_RESIZE_STEP
    if (event.key === 'ArrowRight') nextWidth = this.sidebarWidth + SIDEBAR_RESIZE_STEP
    if (event.key === 'Home') nextWidth = SIDEBAR_MIN_WIDTH
    if (event.key === 'End') nextWidth = SIDEBAR_MAX_WIDTH
    if (nextWidth === undefined) return
    event.preventDefault()
    this.applySidebarWidth(nextWidth, true)
  }

  private resetSidebarWidth = (): void => {
    try {
      localStorage.removeItem(this.widthStorageKey)
    } catch {
      // Ignore storage failures; the current session state still updates.
    }
    this.sidebarWidth = this.defaultSidebarWidth
    this.style.removeProperty('--lv-sidebar-resized-width')
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

  private renderAreaSwitcher(mobile = false) {
    const areas = Array.isArray(this.config.areas) ? this.config.areas : []
    if (areas.length < 2) return null
    return html`
      <div class=${mobile ? 'area-switcher mobile-area-switcher' : 'area-switcher'} role="navigation" aria-label="Product mode">
        ${areas.map((area) => {
          const current = area.id === this.config.area
          const href = this.areaHref(area, current)
          return html`
            <a
              class="area-item"
              href=${href}
              aria-current=${current ? 'page' : 'false'}
              aria-label=${area.label}
              title=${area.label}
              @click=${(event: MouseEvent) => this.followInternalLink(event, href)}
            >
              <span class="area-icon" aria-hidden="true">${icon(area.icon)}</span>
              <span class="area-label">${area.label}</span>
            </a>
          `
        })}
      </div>
    `
  }

  private renderCollapsedAreaSwitch() {
    const areas = Array.isArray(this.config.areas) ? this.config.areas : []
    if (areas.length < 2) return null
    const current = areas.find((area) => area.id === this.config.area) ?? areas[0]
    const destination = areas.find((area) => area.id !== current.id) ?? areas[0]
    const href = this.areaHref(destination, false)
    return html`
      <div class="collapsed-area-switcher">
        <a
          class="collapsed-area-switch"
          href=${href}
          aria-label=${`Switch to ${destination.label}`}
          title=${`Switch to ${destination.label}`}
          @click=${(event: MouseEvent) => this.followInternalLink(event, href)}
        >
          <span class="area-icon" aria-hidden="true">${icon(current.icon)}</span>
        </a>
      </div>
    `
  }

  private renderUserCard() {
    const userName = this.config.userName?.trim() || 'Local user'
    const userAvatarUrl = this.liveUserAvatarUrl ?? this.config.userAvatarUrl?.trim()
    const href = this.config.userSettingsHref || '/admin/profile'
    return html`
      <a
        class="user-card"
        href=${href}
        aria-label=${`Open settings for ${userName}`}
        title=${userName}
        @click=${(event: MouseEvent) => this.followInternalLink(event, href)}
      >
        <lv-user-avatar .name=${userName} .imageUrl=${userAvatarUrl ?? ''} aria-hidden="true"></lv-user-avatar>
        <span class="user-text">
          <strong class="user-name">${userName}</strong>
          <span class="user-role">${this.config.userRole ?? 'Local user'}</span>
        </span>
        <span class="user-settings-icon" aria-hidden="true">${icon('settings')}</span>
      </a>
    `
  }

  private areaHref(area: SidebarArea, current: boolean): string {
    if (current && typeof window !== 'undefined') return `${window.location.pathname}${window.location.search}${window.location.hash}`
    try {
      const stored = localStorage.getItem(areaStorageKey(area.id))
      if (stored && stored.startsWith('/') && !stored.startsWith('//')) return stored
    } catch {
      // Storage is an enhancement; canonical area roots remain usable without it.
    }
    return area.href
  }

  private rememberCurrentArea(): void {
    const area = this.config.area?.trim()
    if (!area || this.config.admin || typeof window === 'undefined') return
    try {
      localStorage.setItem(areaStorageKey(area), `${window.location.pathname}${window.location.search}${window.location.hash}`)
    } catch {
      // Ignore storage failures; the link still follows the canonical area root.
    }
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
        ${item.pending ? html`<lv-loading-spinner size="small" aria-label="Title loading"></lv-loading-spinner>` : null}
      </a>
    `
  }

  private followInternalLink(event: MouseEvent, href: string): void {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    const target = new URL(href, window.location.href)
    if (target.origin !== window.location.origin) return
    if (target.href === window.location.href) {
      event.preventDefault()
      return
    }
    event.preventDefault()
    this.rememberCurrentArea()
    this.closeMobileNavigation()
    window.location.assign(target.href)
  }
}

function icon(name: string) {
  const icons: Record<IconName, IconNode> = {
    catalog: Layers,
    boxes: Boxes,
    dashboard: LayoutDashboard,
    chat: MessagesSquare,
    bot: Bot,
    database: Database,
    globe: Globe,
    history: History,
    insights: ChartNoAxesCombined,
    model: Database,
    data: Plug,
    cache: TableProperties,
    code: Code2,
    waypoints: Waypoints,
    settings: Settings,
    system: Monitor,
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
    workflow: Workflow,
  }

  return lucideIcon(icons[name as IconName] ?? Layers)
}

function areaStorageKey(area: string): string {
  return `leapview-area-last-${area}`
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

function storedSidebarWidth(storageKey: string): number | undefined {
  try {
    const width = Number(localStorage.getItem(storageKey))
    if (Number.isFinite(width) && width >= SIDEBAR_MIN_WIDTH && width <= SIDEBAR_MAX_WIDTH) return Math.round(width)
  } catch {
    // Ignore storage failures and use the route's default width.
  }
  return undefined
}

customElements.define('lv-sidebar', LeapViewSidebar)
