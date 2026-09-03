import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import {
  ArrowUpDown,
  Bot,
  BookOpen,
  Boxes,
  Cable,
  CheckCircle2,
  ChartColumn,
  ChartNoAxesColumnIncreasing,
  ChevronDown,
  ChevronRight,
  Circle,
  Clock3,
  Component,
  Database,
  Download,
  FilePenLine,
  FileText,
  BadgeCheck,
  LayoutDashboard,
  LockKeyhole,
  EllipsisVertical,
  Plus,
  Plug,
  Play,
  RefreshCw,
  Search,
  Star,
  Table2,
  TableProperties,
  UsersRound,
  UserRound,
  Waypoints,
  Workflow,
  XCircle,
  type IconNode,
} from 'lucide'
import { lucideIcon } from './lucide-icons'
import './user-avatar'

export type EntityListItem = {
  id: string
  title: string
  description?: string
  href?: string
  avatarUrl?: string
  icon?: string
  iconNode?: IconNode
  iconColor?: string
  iconButtonLabel?: string
  projectId?: string
  dashboardId?: string
  iconTreatment?: 'plain' | 'framed' | 'none'
  meta?: string
  category?: string
  group?: string
  columns?: Record<string, string | number>
  columnTitles?: Record<string, string>
  sortValues?: Record<string, string | number>
  badges?: EntityListBadge[]
  favorite?: boolean
  favoriteLabel?: string
  actions?: EntityListRowAction[]
}

export type EntityListRowAction = {
  label: string
  action: string
  icon?: 'play' | 'refresh' | 'details' | 'cancel' | 'more'
  disabled?: boolean
}

export type EntityListBadge = {
  icon: 'popularity' | 'featured'
  label: string
  level?: 'low' | 'medium' | 'high'
  text?: string
}

export type EntityListColumn = {
  id: string
  label: string
  width?: string
  align?: 'left' | 'right'
  sortable?: boolean
  render?: 'badges' | 'actions' | 'status'
}

export type EntityListFilter = {
  id: string
  label: string
  href?: string
}

export type EntityListAction = {
  id: string
  label: string
  emphasis?: 'primary' | 'default'
}

const entityListStyles = `
  .entity-list {
    display: grid;
    min-width: 0;
    gap: var(--base-size-16);
  }

  .entity-toolbar {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
  }

  .entity-toolbar-actions {
    display: flex;
    margin-left: auto;
    align-items: center;
    gap: var(--base-size-8);
  }

  .entity-toolbar-trailing {
    display: flex;
    margin-left: auto;
    align-items: center;
    gap: var(--base-size-8);
  }

  .entity-search {
    position: relative;
    display: flex;
    min-width: 12rem;
    width: min(100%, 19rem);
    flex: 0 1 19rem;
    align-items: center;
  }

  .entity-search svg {
    position: absolute;
    left: var(--base-size-12, 12px);
    width: var(--base-size-16);
    height: var(--base-size-16);
    color: var(--lv-fg-muted);
    pointer-events: none;
  }

  .entity-search input[type='search'],
  .entity-filter {
    box-sizing: border-box;
    height: var(--control-medium-size, 32px);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .entity-search input[type='search'] {
    width: 100%;
    min-width: 0;
    padding: 0 var(--base-size-12, 12px) 0 var(--base-size-36, 36px);
    outline: 0;
  }

  .entity-search input[type='search']::placeholder {
    color: var(--lv-fg-muted);
    opacity: 1;
  }

  .entity-search input[type='search']:focus-visible,
  .entity-filter:focus-visible,
  .entity-export:focus-visible,
  .entity-action:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .entity-filter {
    min-width: 4.75rem;
    padding: 0 var(--base-size-8);
  }

  .entity-export,
  .entity-action {
    display: inline-flex;
    min-height: var(--lv-button-height);
    align-items: center;
    justify-content: center;
    gap: var(--base-size-6);
    border: var(--borderWidth-default) solid var(--lv-button-border-rest);
    border-radius: var(--lv-button-radius);
    background: var(--lv-button-bg-rest);
    color: var(--lv-button-fg-rest);
    padding: 0 var(--lv-button-padding-inline-sm);
    cursor: pointer;
    font: var(--lv-type-body);
    white-space: nowrap;
  }

  .entity-export:hover {
    border-color: var(--lv-button-border-hover);
    background: var(--lv-button-bg-hover);
  }

  .entity-action-primary {
    border-color: var(--lv-button-accent-border-rest);
    background: var(--lv-button-accent-bg-rest);
    color: var(--lv-button-accent-fg-rest);
  }

  .entity-action-primary:hover {
    border-color: var(--lv-button-accent-border-hover);
    background: var(--lv-button-accent-bg-hover);
  }

  .entity-list-row-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--base-size-4);
  }

  .entity-list-row-action {
    display: inline-flex;
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    align-items: center;
    justify-content: center;
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: var(--lv-fg-muted);
    cursor: pointer;
  }

  .entity-list-row-action:hover:not(:disabled),
  .entity-list-row-action:focus-visible {
    background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
  }

  .entity-list-row-action:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .entity-list-row-action:disabled {
    cursor: not-allowed;
    opacity: 0.45;
  }

  .entity-list-items {
    min-width: 0;
    overflow: hidden;
    background: var(--lv-bg-page);
  }

  .entity-list-table-wrap {
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: thin;
    -webkit-overflow-scrolling: touch;
  }

  .entity-list-table-wrap:focus-visible {
    outline: 2px solid var(--lv-fg-accent, currentColor);
    outline-offset: 2px;
  }

  .entity-list-scroll-hint {
    display: none;
    margin: var(--base-size-4) var(--base-size-8) 0;
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-align: right;
  }

  @media (max-width: 640px) {
    .entity-list-scroll-hint {
      display: block;
    }

  }

  .entity-list-table {
    width: 100%;
    min-width: 42rem;
    border-collapse: separate;
    border-spacing: 0;
    table-layout: fixed;
  }

  .entity-list-table th,
  .entity-list-table td {
    min-width: 0;
    padding: 0 var(--base-size-4);
    text-align: left;
    vertical-align: middle;
  }

  .entity-list-table thead th {
    height: var(--control-medium-size);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .entity-list-sort-button {
    display: inline-flex;
    max-width: 100%;
    align-items: center;
    gap: var(--base-size-4);
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: inherit;
    font: inherit;
    line-height: inherit;
    text-align: inherit;
  }

  .entity-list-sort-button:not(:disabled) {
    cursor: pointer;
  }

  .entity-list-sort-button:not(:disabled):hover {
    color: var(--lv-fg-default);
  }

  .entity-list-sort-button:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .entity-list-sort-button.is-right {
    justify-content: flex-end;
    width: 100%;
  }

  .entity-list-sort-indicator {
    display: inline-grid;
    width: var(--base-size-12);
    height: var(--base-size-12);
    place-items: center;
    flex: 0 0 auto;
    color: var(--lv-fg-muted);
  }

  .entity-list-sort-indicator.is-active {
    color: var(--lv-fg-default);
  }

  .entity-list-table th.is-right,
  .entity-list-table td.is-right {
    text-align: right;
  }

  .entity-list-table-row {
    height: 3.25rem;
    transition: background-color var(--motion-transition-stateChange);
  }

  .entity-list-group-row th {
    height: 2.75rem;
    padding: var(--base-size-4);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
  }

  .entity-list-group-row + .entity-list-table-row {
    border-top: 0;
  }

  .entity-list-group-toggle {
    display: flex;
    width: 100%;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: var(--lv-fg-default);
    padding: var(--base-size-6) var(--base-size-8);
    cursor: pointer;
    font: var(--lv-type-body-compact);
    text-align: left;
  }

  .entity-list-group-toggle:focus-visible {
    outline: 0;
  }

  .entity-list-group-chevron,
  .entity-list-group-icon {
    display: inline-grid;
    flex: 0 0 auto;
    place-items: center;
    color: var(--lv-fg-muted);
  }

  .entity-list-group-chevron {
    width: var(--base-size-24, 24px);
    height: var(--base-size-24, 24px);
    margin-block: calc(var(--base-size-4, 4px) * -1);
    border-radius: var(--lv-radius-default);
    transition: background-color var(--motion-transition-stateChange), color var(--motion-transition-stateChange);
  }

  .entity-list-group-toggle:hover .entity-list-group-chevron,
  .entity-list-group-toggle:focus-visible .entity-list-group-chevron,
  .entity-list-group.is-collapsed .entity-list-group-chevron {
    background: var(--lv-bg-control-hover);
    color: var(--lv-fg-default);
  }

  .entity-list-group-toggle:focus-visible .entity-list-group-chevron {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .entity-list-group-label {
    overflow: hidden;
    font-weight: var(--base-text-weight-semibold);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .entity-list-group-count {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .entity-list-table-row.is-actionable {
    cursor: pointer;
  }

  .entity-list-table-row:hover,
  .entity-list-table-row:focus-within {
    background: var(--lv-bg-control-hover);
  }

  .entity-list-icon,
  .entity-list-chevron {
    display: grid;
    place-items: center;
  }

  .entity-list-icon {
    width: var(--control-medium-size, 32px);
    height: var(--control-medium-size, 32px);
    flex: 0 0 var(--control-medium-size, 32px);
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: var(--lv-fg-muted);
  }

  .entity-list-icon svg {
    width: var(--base-size-20, 20px);
    height: var(--base-size-20, 20px);
  }

  .entity-list-icon.is-framed {
    border: var(--lv-border-muted);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-link);
  }

  button.entity-list-icon { padding: 0; cursor: pointer; }
  button.entity-list-icon:hover { filter: brightness(1.08); }
  button.entity-list-icon:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
  ${['gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink', 'coral'].map((color) => `
  .entity-list-icon.is-framed.color-${color} {
    border-color: var(--display-${color}-borderColor-muted, ${color === 'purple' ? 'var(--lv-asset-dashboard-border)' : 'var(--lv-line-muted)'});
    background: var(--display-${color}-bgColor-muted, ${color === 'purple' ? 'var(--lv-asset-dashboard-bg)' : 'var(--lv-bg-panel-muted)'});
    color: var(--display-${color}-fgColor, ${color === 'purple' ? 'var(--lv-asset-dashboard-accent)' : 'var(--lv-fg-muted)'});
  }`).join('\n')}

  .entity-list-icon-spacer {
    display: block;
    width: var(--control-medium-size, 32px);
    height: var(--control-medium-size, 32px);
    flex: 0 0 var(--control-medium-size, 32px);
  }

  .entity-list-group .entity-list-table-row .entity-list-identity {
    padding-left: var(--base-size-24, 24px);
  }

  .entity-list-icon-connection {
    color: var(--lv-asset-connection-accent, var(--lv-fg-muted));
  }

  .entity-list-icon-source {
    color: var(--lv-asset-source-accent, var(--lv-fg-muted));
  }

  .entity-list-icon-connection.is-framed {
    border-color: var(--lv-asset-connection-border, var(--lv-line-muted));
    background: var(--lv-asset-connection-bg, var(--lv-bg-panel-muted));
  }

  .entity-list-icon-source.is-framed {
    border-color: var(--lv-asset-source-border, var(--lv-line-muted));
    background: var(--lv-asset-source-bg, var(--lv-bg-panel-muted));
  }

  .entity-list-icon-dashboard.is-framed {
    border-color: var(--lv-asset-dashboard-border, var(--lv-line-muted));
    background: var(--lv-asset-dashboard-bg, var(--lv-bg-panel-muted));
    color: var(--lv-asset-dashboard-accent, var(--lv-fg-muted));
  }

  .entity-list-icon-application {
    border-radius: var(--lv-radius-full);
  }

  .entity-list-identity {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
    border-radius: var(--lv-radius-default);
    color: inherit;
    text-decoration: none;
  }

  .entity-list-identity-row {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
  }

  .entity-list-identity-row .entity-list-identity {
    flex: 1 1 auto;
  }

  .entity-list-favorite {
    display: inline-grid;
    width: var(--control-medium-size, 32px);
    height: var(--control-medium-size, 32px);
    flex: 0 0 auto;
    place-items: center;
    border: 0;
    border-radius: var(--lv-radius-default);
    color: var(--lv-fg-muted);
    background: transparent;
    padding: 0;
    cursor: pointer;
  }

  .entity-list-favorite:hover { color: var(--lv-fg-default); background: var(--lv-bg-control-hover); }
  .entity-list-favorite:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
  .entity-list-favorite[aria-pressed='true'] { color: var(--display-yellow-fgColor); }
  .entity-list-favorite[aria-pressed='true'] svg { fill: currentColor; }

  .entity-list-copy {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  .entity-list-title,
  .entity-list-description,
  .entity-list-meta {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .entity-list-title {
    font: var(--lv-type-body-compact);
    font-weight: var(--base-text-weight-semibold);
  }

  .entity-list.is-title-normal .entity-list-title {
    font-weight: var(--base-text-weight-normal);
  }

  .entity-list-title-row {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-6);
  }

  .entity-list-badge {
    display: inline-flex;
    width: auto;
    height: var(--base-size-16);
    flex: 0 0 auto;
    align-items: center;
    gap: var(--base-size-4);
    color: var(--fgColor-disabled);
    font: var(--lv-type-caption);
    white-space: nowrap;
  }

  .entity-list-badge-featured { color: var(--display-purple-fgColor, var(--lv-fg-default)); }

  .entity-list-badge-popularity svg path {
    stroke: currentColor;
  }

  .entity-list-badge-popularity.is-low svg path:nth-child(1),
  .entity-list-badge-popularity.is-medium svg path:nth-child(1),
  .entity-list-badge-popularity.is-high svg path:nth-child(1),
  .entity-list-badge-popularity.is-medium svg path:nth-child(2),
  .entity-list-badge-popularity.is-high svg path:nth-child(2),
  .entity-list-badge-popularity.is-high svg path:nth-child(3) {
    stroke: var(--display-blue-fgColor);
  }

  .entity-list-badge-empty {
    display: inline-block;
    width: var(--base-size-16);
    color: var(--fgColor-disabled);
    text-align: center;
  }

  .entity-list-description,
  .entity-list-meta {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .entity-list-secondary {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-6);
  }

  .entity-list-secondary-separator { color: var(--lv-fg-muted); font: var(--lv-type-caption); }

  .entity-list-meta {
    display: inline-flex;
    min-width: 0;
    flex: 0 1 auto;
    align-items: center;
    gap: var(--base-size-4);
  }

  .entity-list-meta svg { width: var(--base-size-12); height: var(--base-size-12); flex: 0 0 auto; }

  .entity-list-cell {
    overflow: hidden;
    color: var(--lv-fg-muted);
    font: var(--lv-type-body);
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .entity-list-status {
    display: inline-flex;
    align-items: center;
    gap: var(--base-size-6);
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-medium);
    white-space: nowrap;
  }

  .entity-list-status-icon {
    display: inline-flex;
    width: var(--base-size-16);
    height: var(--base-size-16);
    flex: none;
    align-items: center;
    justify-content: center;
    color: var(--lv-fg-muted);
  }

  .entity-list-status-icon svg {
    display: block;
    width: var(--base-size-16);
    height: var(--base-size-16);
  }

  .entity-list-status.is-success .entity-list-status-icon { color: var(--lv-fg-success); }
  .entity-list-status.is-danger .entity-list-status-icon { color: var(--lv-fg-danger); }
  .entity-list-status.is-attention .entity-list-status-icon { color: var(--lv-fg-warning); }

  .entity-list.is-compact {
    gap: var(--base-size-8);
  }

  .entity-list.is-compact .entity-list-table-row {
    height: 2.5rem;
  }

  .entity-list.is-compact .entity-list-table thead th {
    height: 2rem;
  }

  .entity-list.is-compact .entity-list-icon {
    width: var(--base-size-24);
    height: var(--base-size-24);
    border: 0;
    background: transparent;
  }

  .entity-list.is-compact .entity-list-copy {
    gap: 0;
  }

  .entity-list.is-compact .entity-list-description {
    display: none;
  }

  .entity-list.is-compact .entity-list-row-action {
    width: var(--base-size-28);
    height: var(--base-size-28);
  }

  .entity-list-empty {
    padding: var(--base-size-24) var(--base-size-16);
    color: var(--lv-fg-muted);
    font: var(--lv-type-body);
    text-align: center;
  }

  .entity-list-visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
    clip-path: inset(50%);
  }

  @media (max-width: 720px) {
    .entity-toolbar {
      align-items: stretch;
      flex-wrap: wrap;
    }

    .entity-search {
      width: 100%;
      flex-basis: 100%;
    }

    .entity-filter {
      margin-left: auto;
    }

    .entity-toolbar-trailing { width: 100%; margin-left: 0; }
    .entity-list-badge-text { display: none; }

    .entity-list-table th,
    .entity-list-table td {
      padding-inline: var(--base-size-4);
    }
  }
`

class EntityList extends LitElement {
  @property({ attribute: false }) items: EntityListItem[] = []
  @property({ attribute: false }) columns: EntityListColumn[] = []
  @property({ attribute: false }) filters: EntityListFilter[] = []
  @property({ attribute: false }) actions: EntityListAction[] = []
  @property({ attribute: false }) toolbarTrailing: unknown = nothing
  @property({ attribute: 'list-label' }) listLabel = 'List'
  @property({ attribute: 'export-filename' }) exportFilename = ''
  @property({ attribute: 'search-placeholder' }) searchPlaceholder = 'Search'
  @property({ attribute: 'empty-text' }) emptyText = 'No results found.'
  @property({ attribute: 'initial-query' }) initialQuery = ''
  @property({ attribute: 'active-filter' }) activeFilter = ''
  @property({ attribute: 'group-by' }) groupBy = ''
  @property({ attribute: 'group-icon' }) groupIcon = ''
  @property({ type: Boolean }) compact = false
  @property({ attribute: 'title-emphasis' }) titleEmphasis: 'strong' | 'normal' = 'strong'
  @property({ attribute: 'row-action' }) rowAction = ''
  @property({ attribute: 'min-width' }) minWidth = ''
  @property({ type: Boolean, attribute: 'client-filter' }) clientFilter = false
  @property({ type: Boolean, attribute: 'show-toolbar' }) showToolbar = true
  @state() private query = ''
  @state() private filter = ''
  @state() private sortColumnId = ''
  @state() private sortDirection: 'asc' | 'desc' = 'asc'
  @state() private collapsedGroups: string[] = []

  createRenderRoot(): HTMLElement {
    return this
  }

  connectedCallback(): void {
    super.connectedCallback()
    this.query = this.initialQuery
    this.filter = this.activeFilter || this.filters[0]?.id || ''
  }

  updated(changed: Map<string, unknown>): void {
    if (changed.has('initialQuery') && this.initialQuery !== this.query) this.query = this.initialQuery
    if (changed.has('activeFilter') && this.activeFilter && this.activeFilter !== this.filter) this.filter = this.activeFilter
    if (changed.has('filters') && !this.filters.some((filter) => filter.id === this.filter)) this.filter = this.activeFilter || this.filters[0]?.id || ''
    if (changed.has('columns') && this.sortColumnId && !this.resolvedColumns().some((column) => column.id === this.sortColumnId)) this.sortColumnId = ''
  }

  render() {
    const items = this.visibleItems()
    const columns = this.resolvedColumns()
    return html`
      <style>${entityListStyles}</style>
      <section class=${`entity-list ${this.compact ? 'is-compact' : ''} ${this.titleEmphasis === 'normal' ? 'is-title-normal' : ''}`} aria-label=${this.listLabel}>
        ${this.showToolbar ? html`<div class="entity-toolbar">
          <form class="entity-search" @submit=${this.preventSubmit}>
            ${lucideIcon(Search, { size: 16, strokeWidth: 1.8 })}
            <input
              type="search"
              aria-label=${this.searchPlaceholder}
              placeholder=${this.searchPlaceholder}
              autocomplete="off"
              .value=${this.query}
              @input=${this.handleQueryInput}
            >
          </form>
          ${this.filters.length ? html`
            <label>
              <span class="entity-list-visually-hidden">Filter list</span>
              <select class="entity-filter" aria-label="Filter list" .value=${this.filter} @change=${this.handleFilterChange}>
                ${this.filters.map((filter) => html`<option value=${filter.id}>${filter.label}</option>`)}
              </select>
            </label>
          ` : ''}
          ${this.toolbarTrailing !== nothing ? html`<div class="entity-toolbar-trailing">${this.toolbarTrailing}</div>` : ''}
          ${this.exportFilename || this.actions.length ? html`
            <div class="entity-toolbar-actions">
              ${this.exportFilename ? html`<button class="entity-export" type="button" @click=${this.exportCSV}>
                ${lucideIcon(Download, { size: 14, strokeWidth: 2 })}
                <span>Export CSV</span>
              </button>` : ''}
              ${this.actions.map((action) => html`
                <button
                  class=${`entity-action ${action.emphasis === 'primary' ? 'entity-action-primary' : ''}`}
                  type="button"
                  @click=${() => this.emitAction(action)}
                >
                  ${lucideIcon(Plus, { size: 14, strokeWidth: 2 })}
                  <span>${action.label}</span>
                </button>
              `)}
            </div>
          ` : ''}
        </div>` : ''}
        ${items.length ? html`
          <div class="entity-list-items entity-list-table-wrap" role="region" aria-label="Scrollable ${this.listLabel} table" tabindex="0">
            <table class="entity-list-table" aria-label=${this.listLabel} style=${this.minWidth ? `min-width: ${this.minWidth}` : ''}>
              <colgroup>
                ${columns.map((column) => html`<col style=${column.width ? `width: ${column.width}` : ''}>`)}
              </colgroup>
              <thead>
                <tr>
                  ${columns.map((column) => {
                    const direction = this.sortColumnId === column.id ? this.sortDirection : false
                    const sortable = column.sortable !== false && column.render !== 'actions'
                    if (column.render === 'actions') {
                      return html`<th class=${column.align === 'right' ? 'is-right' : ''} scope="col"><span class="entity-list-visually-hidden">${column.label}</span></th>`
                    }
                    return html`
                      <th
                        class=${column.align === 'right' ? 'is-right' : ''}
                        scope="col"
                        aria-sort=${direction === 'asc' ? 'ascending' : direction === 'desc' ? 'descending' : 'none'}
                      >
                        <button
                          type="button"
                          class=${`entity-list-sort-button ${column.align === 'right' ? 'is-right' : ''}`}
                          aria-label=${`Sort by ${column.label}`}
                          ?disabled=${!sortable}
                          @click=${() => sortable && this.toggleSort(column.id)}
                        >
                          <span>${column.label}</span>
                          <span class=${`entity-list-sort-indicator ${direction ? 'is-active' : ''}`} aria-hidden="true">
                            ${sortable ? this.sortIndicator(direction) : ''}
                          </span>
                        </button>
                      </th>
                    `
                  })}
                </tr>
              </thead>
              ${this.groupBy
                ? this.groupedItems(items).map((group) => this.renderGroup(group, columns))
                : html`<tbody>${items.map((item) => this.renderItem(item, columns))}</tbody>`}
            </table>
            <p class="entity-list-scroll-hint" aria-hidden="true">Swipe horizontally to see more columns <span aria-hidden="true">→</span></p>
          </div>
        ` : html`<div class="entity-list-empty" role="status">${this.query.trim() ? 'No results match your search.' : this.emptyText}</div>`}
      </section>
    `
  }

  private visibleItems(): EntityListItem[] {
    // Query and filter state are server-driven. Keep rendering the last
    // authoritative payload while the debounced page-stream request is in
    // flight instead of applying a second, client-side filter here. Small,
    // already-complete registries can opt into local filtering explicitly.
    let visible = this.items
    if (this.clientFilter) {
      const query = this.query.trim().toLocaleLowerCase()
      const filter = this.filter.trim().toLocaleLowerCase()
      visible = visible.filter((item) => {
        if (filter && filter !== 'all' && item.category?.toLocaleLowerCase() !== filter) return false
        if (!query) return true
        const searchable = [item.title, item.description, item.meta, item.group, ...Object.values(item.columns ?? {})]
          .filter((value) => value != null)
          .join(' ')
          .toLocaleLowerCase()
        return searchable.includes(query)
      })
    }
    const column = this.resolvedColumns().find((candidate) => candidate.id === this.sortColumnId)
    if (!column || column.sortable === false) return visible
    return visible
      .map((item, index) => ({ item, index }))
      .sort((left, right) => {
        const result = compareEntityValues(this.sortValue(left.item, column), this.sortValue(right.item, column), this.sortDirection)
        return result || left.index - right.index
      })
      .map(({ item }) => item)
  }

  private resolvedColumns(): EntityListColumn[] {
    return this.columns.length ? this.columns : [{ id: 'name', label: 'Name' }]
  }

  private itemValue(item: EntityListItem, column: EntityListColumn): string | number {
    return item.sortValues?.[column.id] ?? (column.id === 'name' ? item.title : item.columns?.[column.id] ?? '')
  }

  private exportValue(item: EntityListItem, column: EntityListColumn): string | number {
    return column.id === 'name' ? item.title : item.columns?.[column.id] ?? ''
  }

  private groupedItems(items: EntityListItem[]): Array<{ key: string; label: string; items: EntityListItem[] }> {
    const groups = new Map<string, { key: string; label: string; items: EntityListItem[] }>()
    for (const item of items) {
      const value = this.groupValue(item)
      const label = value || 'Ungrouped'
      const key = value || '__ungrouped__'
      const group = groups.get(key) ?? { key, label, items: [] }
      group.items.push(item)
      groups.set(key, group)
    }
    return Array.from(groups.values())
  }

  private groupValue(item: EntityListItem): string {
    if (this.groupBy === 'group') return String(item.group ?? '').trim()
    if (this.groupBy === 'category') return String(item.category ?? '').trim()
    if (this.groupBy === 'name') return String(item.title ?? '').trim()
    return String(item.columns?.[this.groupBy] ?? '').trim()
  }

  private renderGroup(group: { key: string; label: string; items: EntityListItem[] }, columns: EntityListColumn[]) {
    const collapsed = this.collapsedGroups.includes(group.key)
    return html`
      <tbody class=${collapsed ? 'entity-list-group is-collapsed' : 'entity-list-group'} data-group=${group.key}>
        <tr class="entity-list-group-row">
          <th colspan=${columns.length} scope="rowgroup">
            <button
              type="button"
              class="entity-list-group-toggle"
              aria-expanded=${collapsed ? 'false' : 'true'}
              @click=${() => this.toggleGroup(group.key)}
            >
              <span class="entity-list-group-chevron" aria-hidden="true">${lucideIcon(collapsed ? ChevronRight : ChevronDown, { size: 14, strokeWidth: 2 })}</span>
              ${this.groupIcon ? html`<span class="entity-list-group-icon" aria-hidden="true">${lucideIcon(entityIcon(this.groupIcon), { size: 16, strokeWidth: 1.8 })}</span>` : ''}
              <span class="entity-list-group-label">${group.label}</span>
              <span class="entity-list-group-count" aria-label=${`${group.items.length} ${group.items.length === 1 ? 'item' : 'items'}`}>${group.items.length}</span>
            </button>
          </th>
        </tr>
        ${collapsed ? '' : group.items.map((item) => this.renderItem(item, columns))}
      </tbody>
    `
  }

  private toggleGroup(groupKey: string): void {
    const collapsed = new Set(this.collapsedGroups)
    if (collapsed.has(groupKey)) collapsed.delete(groupKey)
    else collapsed.add(groupKey)
    this.collapsedGroups = Array.from(collapsed)
  }

  private sortValue(item: EntityListItem, column: EntityListColumn): string | number {
    return item.sortValues?.[column.id] ?? this.itemValue(item, column)
  }

  private renderItem(item: EntityListItem, columns: EntityListColumn[]) {
    const badgesColumn = columns.some((column) => column.render === 'badges')
    return html`
      <tr class=${`entity-list-table-row ${this.rowAction ? 'is-actionable' : ''}`} @click=${() => this.emitItemAction(item)}>
        ${columns.map((column) => column.id === 'name'
          ? this.renderIdentityCell(item, badgesColumn)
          : this.renderDataCell(item, column))}
      </tr>
    `
  }

  private renderIdentityCell(item: EntityListItem, badgesColumn: boolean) {
    const iconTreatment = item.iconTreatment ?? (item.icon ? 'plain' : 'none')
    const iconColorClass = iconTreatment === 'framed'
      ? ` color-${item.iconColor || 'purple'}`
      : item.iconColor ? ` color-${item.iconColor}` : ''
    const icon = item.icon === 'none'
      ? nothing
      : item.icon === 'user'
        ? html`<lv-user-avatar .name=${item.title} .imageUrl=${item.avatarUrl ?? ''} aria-hidden="true"></lv-user-avatar>`
        : iconTreatment === 'none'
          ? html`<span class="entity-list-icon-spacer" aria-hidden="true"></span>`
          : item.iconButtonLabel
            ? html`<button type="button" class=${`entity-list-icon is-${iconTreatment} entity-list-icon-${item.icon || 'default'}${iconColorClass}`} aria-label=${item.iconButtonLabel} @click=${(event: Event) => this.activateIcon(event, item)}>${lucideIcon(item.iconNode ?? entityIcon(item.icon))}</button>`
            : html`<span class=${`entity-list-icon is-${iconTreatment} entity-list-icon-${item.icon || 'default'}${iconColorClass}`} aria-hidden="true">${lucideIcon(item.iconNode ?? entityIcon(item.icon))}</span>`
    const copy = html`
      <span class="entity-list-copy">
        <span class="entity-list-title-row">
          <span class="entity-list-title">${item.title}</span>
          ${badgesColumn ? '' : (item.badges ?? []).map((badge) => this.renderBadge(badge))}
        </span>
        ${item.description || item.meta ? html`<span class="entity-list-secondary">
          ${item.description ? html`<span class="entity-list-description">${item.description}</span>` : ''}
          ${item.description && item.meta ? html`<span class="entity-list-secondary-separator" aria-hidden="true">·</span>` : ''}
          ${item.meta ? html`<span class="entity-list-meta">${lucideIcon(Database, { size: 12, strokeWidth: 1.8 })}<span>${item.meta}</span></span>` : ''}
        </span>` : ''}
      </span>
    `
    const favorite = item.favoriteLabel ? html`
      <button
        type="button"
        class="entity-list-favorite"
        aria-label=${item.favoriteLabel}
        aria-pressed=${String(Boolean(item.favorite))}
        @click=${(event: Event) => this.toggleFavorite(event, item)}
      >${lucideIcon(Star, { size: 16, strokeWidth: 1.8 })}</button>
    ` : ''
    return html`
      <th scope="row">
        ${item.iconButtonLabel
          ? html`<span class="entity-list-identity-row">${favorite}${icon}${item.href
            ? html`<a class="entity-list-identity" data-item-id=${item.id} href=${item.href} @click=${(event: Event) => this.activateIdentity(event, item)}>${copy}</a>`
            : html`<span class="entity-list-identity">${copy}</span>`}</span>`
          : item.href
            ? html`<span class="entity-list-identity-row">${favorite}<a class="entity-list-identity" data-item-id=${item.id} href=${item.href} @click=${(event: Event) => this.activateIdentity(event, item)}>${icon}${copy}</a></span>`
            : html`<span class="entity-list-identity-row">${favorite}<span class="entity-list-identity">${icon}${copy}</span></span>`}
      </th>
    `
  }

  private renderDataCell(item: EntityListItem, column: EntityListColumn) {
    const value = item.columns?.[column.id]
    const badges = item.badges ?? []
    const title = column.render === 'badges'
      ? badges.map((badge) => badge.label).join(', ')
      : item.columnTitles?.[column.id] ?? String(value ?? '')
    return html`
      <td class=${`entity-list-cell ${column.align === 'right' ? 'is-right' : ''}`} title=${title}>
        ${column.render === 'badges'
          ? (badges.length ? badges.map((badge) => this.renderBadge(badge)) : html`
              <span class="entity-list-badge-empty" role="img" aria-label="No popularity data">—</span>
            `)
          : column.render === 'actions'
            ? html`<span class="entity-list-row-actions">${(item.actions ?? []).map((action) => html`
                <button
                  type="button"
                  class="entity-list-row-action"
                  title=${action.label}
                  aria-label=${action.label}
                  ?disabled=${action.disabled}
                  @click=${(event: Event) => this.emitRowAction(event, action, item)}
                  aria-haspopup=${action.icon === 'more' ? 'menu' : nothing}
                >${lucideIcon(entityActionIcon(action.icon), { size: 15, strokeWidth: 2 })}</button>
	              `)}</span>`
            : column.render === 'status'
              ? this.renderStatus(value)
              : (value == null || value === '' ? '—' : value)}
      </td>
    `
  }

  private renderBadge(badge: EntityListBadge) {
    return html`
      <span class=${`entity-list-badge entity-list-badge-${badge.icon}${badge.level ? ` is-${badge.level}` : ''}`} role="img" aria-label=${badge.label} title=${badge.label}>
        ${lucideIcon(badgeIcon(badge.icon), { size: 16, strokeWidth: 2.5 })}
        ${badge.text ? html`<span class="entity-list-badge-text">${badge.text}</span>` : ''}
      </span>
    `
  }

  private toggleFavorite(event: Event, item: EntityListItem): void {
    event.preventDefault()
    event.stopPropagation()
    this.dispatchEvent(new CustomEvent('lv-entity-list-favorite-toggle', {
      bubbles: true,
      composed: true,
      detail: { item },
    }))
  }

  private activateIdentity(event: Event, item: EntityListItem): void {
    event.stopPropagation()
    this.dispatchEvent(new CustomEvent('lv-entity-list-item-activate', {
      bubbles: true,
      composed: true,
      detail: { item },
    }))
  }

  private activateIcon(event: Event, item: EntityListItem): void {
    event.preventDefault()
    event.stopPropagation()
    this.dispatchEvent(new CustomEvent('lv-entity-list-icon-activate', {
      bubbles: true,
      composed: true,
      detail: { item, anchor: event.currentTarget },
    }))
  }

  private renderStatus(value: string | number | undefined) {
    const label = value == null || value === '' ? '—' : String(value)
    const status = entityStatusPresentation(label)
    return html`
      <span class=${`entity-list-status is-${status.tone}`}>
        <span class="entity-list-status-icon" aria-hidden="true">${lucideIcon(status.icon, { size: 16, strokeWidth: 2 })}</span>
        <span>${label}</span>
      </span>
    `
  }

  private exportCSV = () => {
    const columns = this.resolvedColumns()
    const rows = [
      columns.map((column) => column.label),
      ...this.visibleItems().map((item) => columns.map((column) => String(this.exportValue(item, column)))),
    ]
    const csv = rows.map((row) => row.map((value) => `"${value.replaceAll('"', '""')}"`).join(',')).join('\n')
    const link = document.createElement('a')
    link.href = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }))
    link.download = this.exportFilename
    link.click()
    URL.revokeObjectURL(link.href)
  }

  private emitAction(action: EntityListAction): void {
    this.dispatchEvent(new CustomEvent('lv-entity-list-action', {
      bubbles: true,
      composed: true,
      detail: { id: action.id },
    }))
  }

  private emitRowAction(event: Event, action: EntityListRowAction, item: EntityListItem): void {
    event.stopPropagation()
    this.dispatchEvent(new CustomEvent('lv-entity-list-row-action', {
      bubbles: true,
      composed: true,
      detail: { action: action.action, item, anchor: event.currentTarget },
    }))
  }

  private emitItemAction(item: EntityListItem): void {
    if (!this.rowAction) return
    this.dispatchEvent(new CustomEvent('lv-entity-list-row-action', {
      bubbles: true,
      composed: true,
      detail: { action: this.rowAction, item },
    }))
  }

  private toggleSort(columnId: string): void {
    if (this.sortColumnId === columnId) {
      this.sortDirection = this.sortDirection === 'asc' ? 'desc' : 'asc'
      return
    }
    this.sortColumnId = columnId
    this.sortDirection = 'asc'
  }

  private sortIndicator(direction: false | 'asc' | 'desc') {
    if (direction === 'asc') return html`<span>↑</span>`
    if (direction === 'desc') return html`<span>↓</span>`
    return lucideIcon(ArrowUpDown, { size: 12, strokeWidth: 2 })
  }

  private preventSubmit = (event: Event) => event.preventDefault()

  private handleQueryInput = (event: Event) => {
    this.query = (event.currentTarget as HTMLInputElement).value
    this.dispatchQueryState()
  }

  private handleFilterChange = (event: Event) => {
    const value = (event.currentTarget as HTMLSelectElement).value
    this.filter = value
    this.dispatchQueryState()
  }

  private dispatchQueryState = () => {
    this.dispatchEvent(new CustomEvent('lv-entity-list-query', {
      bubbles: true,
      composed: true,
      detail: { query: this.query, filter: this.filter },
    }))
  }
}

function badgeIcon(type: EntityListBadge['icon']): IconNode {
  switch (type) {
    case 'popularity': return ChartNoAxesColumnIncreasing
    case 'featured': return BadgeCheck
  }
}

function entityIcon(type = ''): IconNode {
  switch (type) {
    case 'dashboard': return LayoutDashboard
    case 'project': return Boxes
    case 'group': return UsersRound
    case 'user': return UserRound
    case 'application': return Bot
    case 'connection': return Plug
    case 'source': return Cable
    case 'catalog': return BookOpen
    case 'model_table': return TableProperties
    case 'semantic_model': return Waypoints
    case 'table': return Table2
    case 'schema': return Database
    case 'view': return TableProperties
    case 'visual': return ChartColumn
    case 'workflow': return Workflow
    case 'component': return Component
    default: return Boxes
  }
}

function entityActionIcon(type: EntityListRowAction['icon']): IconNode {
  switch (type) {
    case 'more': return EllipsisVertical
    case 'refresh': return RefreshCw
    case 'details': return FileText
    case 'cancel': return XCircle
    default: return Play
  }
}

function entityStatusPresentation(label: string): { icon: IconNode, tone: 'success' | 'danger' | 'attention' | 'muted' } {
  switch (label.trim().toLowerCase()) {
    case 'succeeded':
    case 'success':
    case 'healthy':
    case 'published':
      return { icon: CheckCircle2, tone: 'success' }
    case 'private draft':
      return { icon: LockKeyhole, tone: 'muted' }
    case 'unpublished changes':
      return { icon: FilePenLine, tone: 'attention' }
    case 'failed':
    case 'cancelled':
    case 'error':
      return { icon: XCircle, tone: 'danger' }
    case 'queued':
    case 'running':
    case 'prepared':
    case 'pending':
      return { icon: Clock3, tone: 'attention' }
    default:
      return { icon: Circle, tone: 'muted' }
  }
}

if (!customElements.get('lv-entity-list')) customElements.define('lv-entity-list', EntityList)

const entityValueCollator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

function compareEntityValues(left: string | number, right: string | number, direction: 'asc' | 'desc'): number {
  const leftText = String(left ?? '').trim()
  const rightText = String(right ?? '').trim()
  const leftEmpty = leftText === '' || leftText === '—'
  const rightEmpty = rightText === '' || rightText === '—'
  if (leftEmpty || rightEmpty) {
    if (leftEmpty && rightEmpty) return 0
    return leftEmpty ? 1 : -1
  }
  const result = entityValueCollator.compare(leftText, rightText)
  return direction === 'asc' ? result : -result
}
