import { LitElement, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import {
  ArrowUpDown,
  Bot,
  BookOpen,
  Boxes,
  Cable,
  ChartColumn,
  Component,
  Database,
  Download,
  LayoutDashboard,
  Plug,
  Search,
  Table2,
  TableProperties,
  UsersRound,
  UserRound,
  Workflow,
  type IconNode,
} from 'lucide'
import { lucideIcon } from './lucide-icons'

export type EntityListItem = {
  id: string
  title: string
  description?: string
  href: string
  icon?: string
  meta?: string
  category?: string
  group?: string
  columns?: Record<string, string | number>
}

export type EntityListGroup = {
  id: string
  label: string
}

export type EntityListColumn = {
  id: string
  label: string
  width?: string
  align?: 'left' | 'right'
  sortable?: boolean
}

export type EntityListFilter = {
  id: string
  label: string
  href?: string
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
    left: var(--base-size-12);
    width: var(--base-size-16);
    height: var(--base-size-16);
    color: var(--lv-fg-muted);
    pointer-events: none;
  }

  .entity-search input[type='search'],
  .entity-filter {
    box-sizing: border-box;
    height: var(--control-medium-size);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .entity-search input[type='search'] {
    width: 100%;
    min-width: 0;
    padding: 0 var(--base-size-12) 0 var(--base-size-36);
    outline: 0;
  }

  .entity-search input[type='search']::placeholder {
    color: var(--lv-fg-muted);
    opacity: 1;
  }

  .entity-search input[type='search']:focus-visible,
  .entity-filter:focus-visible,
  .entity-export:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .entity-filter {
    min-width: 4.75rem;
    padding: 0 var(--base-size-8);
  }

  .entity-export {
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

  .entity-list-items {
    min-width: 0;
    overflow: hidden;
    background: var(--lv-bg-page);
  }

  .entity-list-table-wrap {
    min-width: 0;
    overflow-x: auto;
    scrollbar-width: thin;
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
    font-weight: var(--base-text-weight-medium);
    line-height: var(--base-text-lineHeight-tight);
  }

  .entity-list-group-row th {
    height: var(--control-medium-size);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-muted);
    padding-inline: var(--base-size-8);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
  }

  .entity-list-group-count {
    margin-left: var(--base-size-4);
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
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-link);
  }

  .entity-list-icon-connection {
    border-color: var(--lv-asset-connection-border, var(--lv-line-muted));
    background: var(--lv-asset-connection-bg, var(--lv-bg-panel-muted));
    color: var(--lv-asset-connection-accent, var(--lv-fg-muted));
  }

  .entity-list-icon-source {
    border-color: var(--lv-asset-source-border, var(--lv-line-muted));
    background: var(--lv-asset-source-bg, var(--lv-bg-panel-muted));
    color: var(--lv-asset-source-accent, var(--lv-fg-muted));
  }

  .entity-list-icon-user,
  .entity-list-icon-application {
    width: var(--base-size-24);
    height: var(--base-size-24);
    border-radius: var(--lv-radius-full);
  }

  .entity-list-icon-user {
    background: var(--lv-bg-accent-muted);
    color: var(--lv-fg-accent);
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
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-semibold);
    line-height: var(--base-text-lineHeight-tight);
  }

  .entity-list-description,
  .entity-list-meta {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    line-height: var(--base-text-lineHeight-tight);
  }

  .entity-list-cell {
    overflow: hidden;
    color: var(--lv-fg-muted);
    font: var(--lv-type-body);
    text-overflow: ellipsis;
    white-space: nowrap;
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
  @property({ attribute: false }) groups: EntityListGroup[] = []
  @property({ attribute: 'list-label' }) listLabel = 'List'
  @property({ attribute: 'export-filename' }) exportFilename = ''
  @property({ attribute: 'search-placeholder' }) searchPlaceholder = 'Search'
  @property({ attribute: 'empty-text' }) emptyText = 'No results found.'
  @property({ attribute: 'initial-query' }) initialQuery = ''
  @property({ attribute: 'active-filter' }) activeFilter = ''
  @state() private query = ''
  @state() private filter = ''
  @state() private sortColumnId = ''
  @state() private sortDirection: 'asc' | 'desc' = 'asc'

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
      <section class="entity-list" aria-label=${this.listLabel}>
        <div class="entity-toolbar">
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
          ${this.exportFilename ? html`
            <div class="entity-toolbar-actions">
              <button class="entity-export" type="button" @click=${this.exportCSV}>
                ${lucideIcon(Download, { size: 14, strokeWidth: 2 })}
                <span>Export CSV</span>
              </button>
            </div>
          ` : ''}
        </div>
        ${items.length ? html`
          <div class="entity-list-items entity-list-table-wrap">
            <table class="entity-list-table" aria-label=${this.listLabel}>
              <colgroup>
                ${columns.map((column) => html`<col style=${column.width ? `width: ${column.width}` : ''}>`)}
              </colgroup>
              <thead>
                <tr>
                  ${columns.map((column) => {
                    const direction = this.sortColumnId === column.id ? this.sortDirection : false
                    const sortable = column.sortable !== false
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
              ${this.renderBodies(items, columns)}
            </table>
          </div>
        ` : html`<div class="entity-list-empty" role="status">${this.query.trim() ? 'No results match your search.' : this.emptyText}</div>`}
      </section>
    `
  }

  private visibleItems(): EntityListItem[] {
    // Query and filter state are server-driven. Keep rendering the last
    // authoritative payload while the debounced page-stream request is in
    // flight instead of applying a second, client-side filter here.
    const visible = this.items
    const column = this.resolvedColumns().find((candidate) => candidate.id === this.sortColumnId)
    if (!column || column.sortable === false) return visible
    return visible
      .map((item, index) => ({ item, index }))
      .sort((left, right) => {
        const result = compareEntityValues(this.itemValue(left.item, column), this.itemValue(right.item, column), this.sortDirection)
        return result || left.index - right.index
      })
      .map(({ item }) => item)
  }

  private resolvedColumns(): EntityListColumn[] {
    return this.columns.length ? this.columns : [{ id: 'name', label: 'Name' }]
  }

  private itemValue(item: EntityListItem, column: EntityListColumn): string | number {
    return column.id === 'name' ? item.title : item.columns?.[column.id] ?? ''
  }

  private renderBodies(items: EntityListItem[], columns: EntityListColumn[]) {
    if (!this.groups.length) return html`<tbody>${items.map((item) => this.renderItem(item, columns))}</tbody>`
    return this.groups.map((group) => {
      const groupedItems = items.filter((item) => item.group === group.id)
      if (!groupedItems.length) return ''
      return html`
        <tbody aria-label=${`${group.label} ${groupedItems.length}`}>
          <tr class="entity-list-group-row">
            <th colspan=${columns.length} scope="colgroup">${group.label}<span class="entity-list-group-count">${groupedItems.length}</span></th>
          </tr>
          ${groupedItems.map((item) => this.renderItem(item, columns))}
        </tbody>
      `
    })
  }

  private renderItem(item: EntityListItem, columns: EntityListColumn[]) {
    return html`
      <tr class="entity-list-table-row">
        <th scope="row">
          <a class="entity-list-identity" href=${item.href}>
            <span class=${`entity-list-icon entity-list-icon-${item.icon || 'default'}`} aria-hidden="true">
              ${lucideIcon(entityIcon(item.icon))}
            </span>
            <span class="entity-list-copy">
              <span class="entity-list-title">${item.title}</span>
              ${item.description ? html`<span class="entity-list-description">${item.description}</span>` : ''}
            </span>
          </a>
        </th>
        ${columns.slice(1).map((column) => {
          const value = item.columns?.[column.id]
          return html`<td class=${`entity-list-cell ${column.align === 'right' ? 'is-right' : ''}`} title=${String(value ?? '')}>${value == null || value === '' ? '—' : value}</td>`
        })}
      </tr>
    `
  }

  private exportCSV = () => {
    const columns = this.resolvedColumns()
    const rows = [
      columns.map((column) => column.label),
      ...this.visibleItems().map((item) => columns.map((column) => String(this.itemValue(item, column)))),
    ]
    const csv = rows.map((row) => row.map((value) => `"${value.replaceAll('"', '""')}"`).join(',')).join('\n')
    const link = document.createElement('a')
    link.href = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }))
    link.download = this.exportFilename
    link.click()
    URL.revokeObjectURL(link.href)
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

function entityIcon(type = ''): IconNode {
  switch (type) {
    case 'dashboard': return LayoutDashboard
    case 'workspace': return Boxes
    case 'group': return UsersRound
    case 'user': return UserRound
    case 'application': return Bot
    case 'connection': return Plug
    case 'source': return Cable
    case 'catalog': return BookOpen
    case 'model_table': return TableProperties
    case 'semantic_model': return Database
    case 'table': return Table2
    case 'visual': return ChartColumn
    case 'workflow': return Workflow
    case 'component': return Component
    default: return Boxes
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
