import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import {
  ArrowLeft,
  BookOpen,
  Box,
  Boxes,
  Cable,
  ChartColumn,
  Component,
  ExternalLink,
  FileText,
  GalleryVerticalEnd,
  LayoutDashboard,
  ListFilter,
  PanelTop,
  Plug,
  RefreshCw,
  Ruler,
  Search,
  Sigma,
  SquareDashedMousePointer,
  Table2,
  TableProperties,
  Workflow,
  type IconNode,
} from 'lucide'
import type {
  ConnectionsPageSignal,
  DefinitionFactSignal,
  RecordTableSignal,
  WorkspaceAccessSignal,
  WorkspaceAssetPageSignal,
  WorkspaceAssetSummarySignal,
  WorkspaceDetailSectionSignal,
  WorkspacePageSignal,
  WorkspaceTabSignal,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { lucideIcon } from '../shared/lucide-icons'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import '../shared/entity-list'
import '../shared/loading-spinner'
import '../shared/record-table'
import '../shared/code-block'
import '../shared/workspace-access-control'

const emptyWorkspaceAccess: WorkspaceAccessSignal = {
  workspace: {},
  roles: [],
  bindings: [],
  candidates: [],
  canManage: false,
  status: { loading: false, error: '', message: '' },
  command: { bindingId: '', email: '', principalId: '', privilege: '', role: '', subjectId: '', subjectType: '' },
  search: '',
  searchStatus: { loading: false, error: '' },
}

class LeapViewWorkspacePage extends DatastarLit(LitElement) {
  @state() private assetQuery: string | null = null
  @state() private assetType: string | null = null
  private lastPageKey = ''

  static get styles() {
    return [pageHeaderStyles, workspaceStyles]
  }

  updated(): void {
    const key = this.pageKey
    if (key !== this.lastPageKey) {
      this.lastPageKey = key
      this.assetQuery = null
      this.assetType = null
    }
    checkSignalContract('workspace page', this.page, { kind: 'required', title: 'required' })
  }

  get page(): WorkspacePageSignal | null {
    return this.signal<WorkspacePageSignal | null>('page', null)
  }

  get workspaceAccess(): WorkspaceAccessSignal {
    return this.signal<WorkspaceAccessSignal>('workspaceAccess', emptyWorkspaceAccess)
  }

  private get pageKey(): string {
    const page = this.page
    return [page?.workspaceId ?? '', page?.title ?? '', page?.assetList?.activeType ?? '', page?.assetList?.query ?? ''].join(':')
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    if (Array.isArray(page.workspaces)) return this.renderCatalog(page)
    if (!page.assetList?.searchHref && this.workspaceAccess?.canManage) return this.renderAccessPage(page)
    return this.renderAssetList(page, 'Workspace assets')
  }

  private renderCatalog(page: WorkspacePageSignal) {
    return html`
      <section class="page catalog" aria-label="LeapView workspaces">
        ${this.renderHeader('', page.title)}
        <lv-entity-list
          .items=${(page.workspaces ?? []).map((workspace) => ({
            id: workspace.id,
            title: workspace.title,
            href: workspace.href,
            icon: 'workspace',
            columns: { description: workspace.description },
          }))}
          .columns=${[
            { id: 'name', label: 'Name', width: '48%' },
            { id: 'description', label: 'Description', width: '52%' },
          ]}
          .filters=${[{ id: 'all', label: 'All' }]}
          initial-query=${page.listQuery ?? ''}
          active-filter=${page.listFilter ?? 'all'}
          search-placeholder="Search workspaces"
          empty-text="No workspaces are available."
        ></lv-entity-list>
      </section>
    `
  }

  private renderAssetList(page: WorkspacePageSignal, label: string) {
    const assetList = page.assetList
    const query = this.assetQuery ?? assetList?.query ?? ''
    // The page stream owns filtering. Keep the last server payload visible
    // until the debounced response replaces it.
    const assets = assetList?.assets ?? []
    const activeType = this.assetType ?? assetList?.activeType ?? ''
    return html`
      <section class="page" aria-label=${label}>
        ${renderPageHeader(page.title)}
        ${renderAssetToolbar(query, activeType, assetList?.tabs ?? [], 'Search workspace assets...', (event: Event) => {
          const value = (event.currentTarget as HTMLInputElement).value
          this.assetQuery = value
          dispatchWorkspaceAssetFilter(event.currentTarget as EventTarget, activeType, value)
        }, (event: Event) => {
          const value = (event.currentTarget as HTMLSelectElement).value
          if (!(assetList?.tabs ?? []).some((tab) => (tab.id || 'all') === value)) return
          this.assetType = value === 'all' ? '' : value
          dispatchWorkspaceAssetFilter(event.currentTarget as EventTarget, this.assetType, query)
        }, this.renderAccessControl())}
        ${renderAssetTable(assets, query ? 'No assets match this search.' : assetList?.empty ?? 'No assets match this view.')}
      </section>
    `
  }

  private renderAccessPage(page: WorkspacePageSignal) {
    return html`
      <section class="page" aria-label="Workspace permissions">
        ${this.renderHeader('Workspace', page.title, page.description, this.renderAccessControl())}
      </section>
    `
  }

  private renderAccessControl() {
    if (!this.workspaceAccess?.canManage) return nothing
    return html`
      <lv-workspace-access-control
        .access=${this.workspaceAccess}
        search=${this.workspaceAccess.search ?? ''}
      ></lv-workspace-access-control>
    `
  }

  private renderHeader(eyebrow: string, title: string, detail = '', actions: unknown = nothing) {
    return renderPageHeader(title, detail, eyebrow, actions)
  }
}

class LeapViewConnectionsPage extends DatastarLit(LitElement) {
  static get styles() {
    return [pageHeaderStyles, workspaceStyles]
  }

  updated(): void {
    checkSignalContract('connections page', this.page, { kind: 'required', title: 'required', assetList: 'required' })
  }

  get page(): ConnectionsPageSignal | null {
    return this.signal<ConnectionsPageSignal | null>('page', null)
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    const assetList = page.assetList
    return html`
      <section class="page" aria-label="Connections and sources">
        ${renderPageHeader(page.title)}
        <lv-entity-list
          .items=${(assetList?.assets ?? []).map((asset) => ({
            id: asset.id,
            title: asset.title,
            href: asset.detailHref,
            icon: asset.type,
            category: asset.type,
            columns: {
              type: asset.typeLabel,
              key: asset.key,
            },
          }))}
          .columns=${[
            { id: 'name', label: 'Name', width: '54%' },
            { id: 'type', label: 'Type', width: '20%' },
            { id: 'key', label: 'Key', width: '26%' },
          ]}
          .filters=${(assetList?.tabs ?? []).map((tab) => ({ id: tab.id || 'all', label: tab.label, href: tab.href }))}
          active-filter=${assetList?.activeType || 'all'}
          initial-query=${assetList?.query ?? ''}
          search-placeholder="Search connections and sources"
          empty-text=${assetList?.empty ?? 'No connection assets match this view.'}
        ></lv-entity-list>
      </section>
    `
  }
}

class LeapViewWorkspaceAssetPage extends DatastarLit(LitElement) {
  static get styles() {
    return workspaceStyles
  }

  updated(): void {
    checkSignalContract('workspace asset page', this.page, { title: 'required', breadcrumbs: 'required', tabs: 'required' })
  }

  get page(): WorkspaceAssetPageSignal | null {
    return this.signal<WorkspaceAssetPageSignal | null>('page', null)
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    return html`
      <section class="asset-page" aria-label="Workspace asset detail">
        <header class="breadcrumb-header">
          <nav aria-label="Breadcrumb">
            <ol>
              ${page.breadcrumbs.map((crumb) => html`
                <li>
                  ${crumb.current
                    ? html`<h1>${assetTypeGlyph(page.asset.type, 'inline')}<span>${crumb.label}</span></h1>`
                    : html`<a href=${crumb.href}>${crumb.label}</a>`}
                </li>
              `)}
            </ol>
          </nav>
          <div class="actions">
            ${page.actions?.map((action) => this.renderAction(action, page))}
          </div>
        </header>
        <div class="asset-body">
          ${renderTabs(page.tabs)}
          <div class=${page.activeSection === 'lineage' ? 'section-body lineage-body' : page.activeSection === 'details' && page.details?.semanticModelGraph ? 'section-body graph-details-body' : 'section-body'}>
            ${page.activeSection === 'lineage'
              ? this.renderLineage(page)
              : page.activeSection === 'refreshes'
                ? this.renderRefreshes(page)
                : this.renderDetails(page)}
          </div>
        </div>
      </section>
    `
  }

  private renderAction(action: NonNullable<WorkspaceAssetPageSignal['actions']>[number], page: WorkspaceAssetPageSignal) {
    if (action.command === 'run-refresh-pipeline') {
      return html`
        <button
          type="button"
          class="icon-link"
          title=${action.label}
          aria-label=${action.label}
          ?disabled=${Boolean(action.disabled || page.refresh?.running)}
          @click=${() => this.dispatchEvent(new CustomEvent('lv-run-refresh-pipeline', { bubbles: true, composed: true }))}
        >
          ${page.refresh?.running ? html`<lv-loading-spinner aria-hidden="true"></lv-loading-spinner>` : lucideIcon(RefreshCw)}
        </button>
      `
    }
    const icon = action.icon === 'open' ? ExternalLink : ArrowLeft
    return html`
      <a class="icon-link" href=${action.href ?? '#'} title=${action.label} aria-label=${action.label}>
        ${lucideIcon(icon)}
      </a>
    `
  }

  private renderDetails(page: WorkspaceAssetPageSignal) {
    return html`
      <section class="details" id="details" aria-label="Asset details">
        ${page.details?.semanticModelGraph ? renderSemanticModelGraph(page.details.semanticModelGraph, page) : nothing}
        <div class="details-content">
          ${renderFacts('Overview', page.details?.overview ?? [], true)}
          ${(page.details?.sections ?? []).map(renderDetailSection)}
        </div>
      </section>
    `
  }

  private renderLineage(page: WorkspaceAssetPageSignal) {
    return html`
      <section class="lineage" id="lineage" aria-label="Asset lineage">
        <lv-asset-lineage-graph class="lineage-graph" .graph=${page.lineage?.graph ?? { nodes: [], edges: [] }}></lv-asset-lineage-graph>
        <div class="lineage-grids">
          ${renderRecordTableSection('Uses', page.lineage?.usesTable)}
          ${renderRecordTableSection('Used by', page.lineage?.usedByTable)}
        </div>
      </section>
    `
  }

  private renderRefreshes(page: WorkspaceAssetPageSignal) {
    return html`
      <section class="details" id="refreshes" aria-label="Refresh runs">
        ${renderRecordTableSection('Refreshes', page.refresh?.runsTable)}
      </section>
    `
  }
}

function renderAssetToolbar(query: string, activeType: string, tabs: WorkspaceTabSignal[], placeholder: string, onSearch: (event: Event) => void, onFilter: (event: Event) => void, actions: unknown = nothing) {
  return html`
    <div class="toolbar">
      <div class="toolbar-filters">
        <form class="search" @submit=${preventSubmit}>
          <input
            type="search"
            name="q"
            .value=${query}
            placeholder=${placeholder}
            autocomplete="off"
            @input=${onSearch}
          />
          ${activeType ? html`<input type="hidden" name="type" value=${activeType} />` : nothing}
          <span class="search-icon" aria-hidden="true">${lucideIcon(Search)}</span>
        </form>
        ${tabs.length ? html`
          <label class="asset-filter">
            <span class="visually-hidden">Filter workspace assets</span>
            <select aria-label="Filter workspace assets" .value=${activeType || 'all'} @change=${onFilter}>
              ${tabs.map((tab) => html`<option value=${tab.id || 'all'}>${tab.label}</option>`)}
            </select>
          </label>
        ` : nothing}
      </div>
      <div class="toolbar-actions">${actions}</div>
    </div>
  `
}

function dispatchWorkspaceAssetFilter(target: EventTarget, type: string, query: string) {
  target.dispatchEvent(new CustomEvent('lv-workspace-asset-filter', {
    bubbles: true,
    composed: true,
    detail: { type, query },
  }))
}

function preventSubmit(event: Event) {
  event.preventDefault()
}

function renderAssetTable(assets: WorkspaceAssetSummarySignal[], empty: string) {
  if (!assets.length) return html`<div class="panel"><div class="empty">${empty}</div></div>`
  const table: RecordTableSignal = {
    columns: [
      { id: 'name', header: 'Name', kind: 'entity' },
      { id: 'type', header: 'Type', width: '150px' },
      { id: 'actions', header: 'Actions', kind: 'actions', align: 'right', width: '104px', sortable: false } as any,
    ],
    rows: assets.map((asset) => {
      const actions = [{ label: 'View details', href: asset.detailHref, icon: 'details' }]
      if (asset.openHref && asset.openHref !== asset.detailHref) {
        actions.push({ label: 'Open asset', href: asset.openHref, icon: 'open' })
      }
      return {
        name: {
          label: asset.title,
          href: asset.detailHref,
          icon: asset.type,
        },
        type: asset.typeLabel,
        actions,
      }
    }),
    empty,
    minWidth: '640px',
  }
  return html`
    <div class="panel table-panel">
      <lv-record-table variant="primary" .table=${table}></lv-record-table>
    </div>
  `
}

function renderTabs(tabs: WorkspaceTabSignal[]) {
  if (!tabs.length) return nothing
  return html`
    <nav class="tabs" aria-label="Asset sections">
      ${tabs.map((tab) => html`
        <a class=${tab.active ? 'active' : ''} href=${tab.href} aria-current=${tab.active ? 'page' : nothing}>
          <span>${tab.label}</span>
          ${tab.count ? html`<span class="count">${tab.count}</span>` : nothing}
        </a>
      `)}
    </nav>
  `
}

function renderDetailSection(section: WorkspaceDetailSectionSignal) {
  if (section.code) {
    return html`
      <section class="detail-section" aria-label=${section.title}>
        <h2>${section.title}</h2>
        <lv-code-block language=${section.lang || 'text'} .code=${section.code}></lv-code-block>
      </section>
    `
  }
  if (section.table?.columns?.length) return renderRecordTableSection(section.title, section.table)
  return renderFacts(section.title, section.facts ?? [], false)
}

function renderSemanticModelGraph(graph: NonNullable<NonNullable<WorkspaceAssetPageSignal['details']>['semanticModelGraph']>, page: WorkspaceAssetPageSignal) {
  return html`
    <section class="semantic-model-section" aria-label="Data model graph">
      <lv-semantic-model-graph class="semantic-model-graph" .graph=${graph} storagekey=${`${page.workspaceId}:${page.assetId}`}></lv-semantic-model-graph>
    </section>
  `
}

function renderFacts(title: string, facts: DefinitionFactSignal[], overview: boolean) {
  const filtered = facts.filter((fact) => fact.value?.trim())
  return html`
    <section class="detail-section" aria-label=${title}>
      <h2>${title}</h2>
      ${filtered.length
        ? html`
          <div class=${overview ? 'facts overview' : 'facts'}>
            ${filtered.map((fact) => html`
              <div class=${fact.wide ? 'wide' : ''}>
                <span>${fact.label}</span>
                ${fact.code ? html`<code>${fact.value}</code>` : html`<p>${fact.value}</p>`}
              </div>
            `)}
          </div>
        `
        : html`<div class="empty">No details are available.</div>`}
    </section>
  `
}

function renderRecordTableSection(title: string, table?: RecordTableSignal) {
  return html`
    <section class="detail-section" aria-label=${title}>
      <h2>${title}</h2>
      <lv-record-table .table=${table ?? null}></lv-record-table>
    </section>
  `
}

function assetTypeGlyph(type: string, size: 'table' | 'inline' = 'table') {
  return html`
    <span class=${`asset-glyph asset-kind-${assetPresentationToken(type)} ${size === 'inline' ? 'inline' : ''}`} aria-hidden="true">
      ${lucideIcon(assetIconNode(type), { size: size === 'inline' ? 14 : 16, strokeWidth: 1.75 })}
    </span>
  `
}

function assetIconNode(type: string): IconNode {
  switch (type) {
    case 'catalog':
      return BookOpen
    case 'connection':
      return Plug
    case 'dashboard':
      return LayoutDashboard
    case 'field':
      return Ruler
    case 'filter':
      return ListFilter
    case 'measure':
      return Sigma
    case 'model_table':
    case 'semantic_table':
      return TableProperties
    case 'page':
      return PanelTop
    case 'page_item':
      return Component
    case 'relationship':
      return Workflow
    case 'semantic_model':
      return Box
    case 'source':
      return Cable
    case 'table':
      return Table2
    case 'visual':
      return ChartColumn
    case 'visual_element':
      return SquareDashedMousePointer
    case 'workspace':
      return Boxes
    case 'workspace_group':
      return GalleryVerticalEnd
    default:
      return Component
  }
}

function assetPresentationToken(type: string): string {
  switch (type) {
    case 'catalog':
    case 'workspace':
    case 'workspace_group':
      return 'catalog'
    case 'connection':
      return 'connection'
    case 'dashboard':
      return 'dashboard'
    case 'field':
    case 'relationship':
      return 'dimension'
    case 'filter':
      return 'filter'
    case 'measure':
      return 'measure'
    case 'model_table':
    case 'semantic_table':
      return 'model-table'
    case 'page':
    case 'page_item':
      return 'page'
    case 'semantic_model':
      return 'semantic-model'
    case 'source':
      return 'source'
    case 'table':
      return 'table'
    case 'visual':
    case 'visual_element':
      return 'visual'
    default:
      return 'default'
  }
}

const workspaceStyles = css`
  :host {
    display: block;
    min-width: 0;
    min-height: 100svh;
    color: var(--lv-fg-default);
    font-family: var(--fontStack-system);
    background: var(--lv-bg-app);
  }

  .page,
  .asset-page {
    display: grid;
    width: min(100%, var(--lv-page-content-max-width));
    min-width: 0;
    min-height: 100svh;
    align-content: start;
    gap: var(--base-size-16);
    box-sizing: border-box;
    margin-inline: auto;
    background: var(--lv-bg-app);
    padding: var(--base-size-24);
  }

  .asset-page {
    width: 100%;
    grid-template-rows: auto auto;
    gap: 0;
    height: auto;
    margin-inline: 0;
    padding: 0;
    overflow: visible;
  }

  .catalog {
    gap: var(--base-size-16);
  }

  .header,
  .breadcrumb-header {
    display: grid;
    min-width: 0;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--base-size-8);
  }

  .breadcrumb-header {
    border-bottom: var(--lv-border-muted);
    padding: var(--lv-space-control) var(--base-size-16);
  }

  .title-block {
    min-width: 0;
  }

  h1,
  h2,
  p {
    margin: 0;
  }

  h1 {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-section-title);
    line-height: var(--base-text-lineHeight-tight);
  }

  h2 {
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-semibold);
  }

  .eyebrow {
    margin-bottom: var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    line-height: var(--base-text-lineHeight-tight);
    text-transform: uppercase;
  }

  .detail,
  .muted {
    margin-top: var(--base-size-4);
    overflow: hidden;
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body);
    line-height: var(--base-text-lineHeight-tight);
  }

  .actions,
  .row-actions {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    justify-content: flex-end;
    gap: var(--base-size-8);
  }

  .panel {
    min-width: 0;
    overflow: hidden;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
  }

  .panel.table-panel {
    border: 0;
    border-radius: 0;
    background: var(--lv-bg-page);
  }

  .primary-link,
  .icon-link,
  .icon-button {
    display: inline-grid;
    place-items: center;
    border-radius: var(--lv-radius-default);
    text-decoration: none;
  }

  .primary-link {
    min-height: var(--lv-button-height-sm);
    grid-auto-flow: column;
    gap: var(--base-size-6);
    border: var(--borderWidth-default) solid var(--lv-button-accent-border-rest);
    background: var(--lv-button-accent-bg-rest);
    color: var(--lv-button-accent-fg-rest);
    padding: 0 var(--lv-button-padding-inline-sm);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
  }

  .icon-link,
  .icon-button {
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    border: var(--lv-border-muted);
    padding: 0;
  }

  .icon-link {
    border-color: transparent;
    background: transparent;
    color: var(--lv-fg-muted);
    cursor: pointer;
  }

  .icon-link:hover,
  .icon-link:focus-visible {
    border-color: var(--lv-line-muted);
    background: var(--lv-bg-control-hover);
    color: var(--lv-fg-default);
    outline: 0;
  }

  .icon-link:disabled {
    opacity: 0.6;
    cursor: wait;
  }

  .icon-button {
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
  }

  button,
  input {
    font: inherit;
  }

  .toolbar {
    display: flex;
    min-width: 0;
    align-items: center;
    justify-content: space-between;
    gap: var(--base-size-8);
  }

  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
    clip-path: inset(50%);
  }

  .toolbar-filters,
  .toolbar-actions {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
  }

  .toolbar-filters {
    flex: 1 1 auto;
  }

  .toolbar-actions {
    flex: 0 0 auto;
  }

  .search {
    position: relative;
    display: flex;
    min-width: 12rem;
    width: min(100%, 19rem);
    flex: 0 1 19rem;
    align-items: center;
  }

  .search input[type='search'],
  .asset-filter select {
    box-sizing: border-box;
    height: var(--control-medium-size);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .search input[type='search'] {
    width: 100%;
    min-width: 0;
    padding: 0 var(--base-size-12) 0 var(--base-size-36);
    outline: 0;
    line-height: var(--base-text-lineHeight-tight);
  }

  .search input[type='search']::placeholder {
    color: var(--lv-fg-muted);
    opacity: 1;
  }

  .search input[type='search']:focus-visible,
  .asset-filter select:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset, var(--base-size-2));
  }

  .asset-filter {
    display: flex;
    min-width: 0;
  }

  .asset-filter select {
    min-width: 4.75rem;
    padding: 0 var(--base-size-8);
  }

  .search-icon {
    position: absolute;
    top: 50%;
    left: var(--base-size-12);
    display: grid;
    width: var(--base-size-16);
    height: var(--base-size-16);
    place-items: center;
    color: var(--lv-fg-muted);
    pointer-events: none;
    transform: translateY(-50%);
  }

  .tabs {
    display: flex;
    min-width: 0;
    flex-wrap: wrap;
    gap: var(--base-size-24);
    border-bottom: var(--lv-border-default);
  }

  .tabs a {
    display: inline-flex;
    min-height: var(--control-xlarge-size);
    align-items: center;
    gap: var(--base-size-8);
    border-bottom: 2px solid transparent;
    color: var(--lv-fg-muted);
    font: var(--lv-type-body);
    text-decoration: none;
  }

  .tabs a.active {
    border-bottom-color: var(--lv-accent);
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-medium);
  }

  .count {
    display: inline-grid;
    min-width: var(--base-size-16);
    place-items: center;
    border-radius: var(--lv-radius-full);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-muted);
    padding: 0 var(--base-size-6);
    font: var(--lv-type-caption);
  }

  code {
    color: var(--lv-fg-muted);
    font: var(--lv-type-code-inline);
  }

  .asset-glyph {
    display: inline-grid;
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    flex: 0 0 auto;
    place-items: center;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-muted);
  }

  .asset-glyph.inline {
    width: var(--base-size-20);
    height: var(--base-size-20);
  }

  .asset-kind-catalog {
    background: var(--lv-asset-catalog-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-catalog-border, var(--lv-line-muted));
    color: var(--lv-asset-catalog-accent, var(--lv-fg-muted));
  }

  .asset-kind-connection {
    background: var(--lv-asset-connection-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-connection-border, var(--lv-line-muted));
    color: var(--lv-asset-connection-accent, var(--lv-fg-muted));
  }

  .asset-kind-dashboard {
    background: var(--lv-asset-dashboard-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-dashboard-border, var(--lv-line-muted));
    color: var(--lv-asset-dashboard-accent, var(--lv-fg-muted));
  }

  .asset-kind-dimension {
    background: var(--lv-asset-dimension-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-dimension-border, var(--lv-line-muted));
    color: var(--lv-asset-dimension-accent, var(--lv-fg-muted));
  }

  .asset-kind-filter {
    background: var(--lv-asset-filter-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-filter-border, var(--lv-line-muted));
    color: var(--lv-asset-filter-accent, var(--lv-fg-muted));
  }

  .asset-kind-measure {
    background: var(--lv-asset-measure-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-measure-border, var(--lv-line-muted));
    color: var(--lv-asset-measure-accent, var(--lv-fg-muted));
  }

  .asset-kind-model-table {
    background: var(--lv-asset-model-table-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-model-table-border, var(--lv-line-muted));
    color: var(--lv-asset-model-table-accent, var(--lv-fg-muted));
  }

  .asset-kind-page {
    background: var(--lv-asset-page-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-page-border, var(--lv-line-muted));
    color: var(--lv-asset-page-accent, var(--lv-fg-muted));
  }

  .asset-kind-semantic-model {
    background: var(--lv-asset-semantic-model-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-semantic-model-border, var(--lv-line-muted));
    color: var(--lv-asset-semantic-model-accent, var(--lv-fg-muted));
  }

  .asset-kind-source {
    background: var(--lv-asset-source-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-source-border, var(--lv-line-muted));
    color: var(--lv-asset-source-accent, var(--lv-fg-muted));
  }

  .asset-kind-table {
    background: var(--lv-asset-table-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-table-border, var(--lv-line-muted));
    color: var(--lv-asset-table-accent, var(--lv-fg-muted));
  }

  .asset-kind-visual {
    background: var(--lv-asset-visual-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-visual-border, var(--lv-line-muted));
    color: var(--lv-asset-visual-accent, var(--lv-fg-muted));
  }

  .empty {
    color: var(--lv-fg-muted);
    padding: var(--base-size-12);
    font: var(--lv-type-body);
  }

  .breadcrumb-header ol {
    display: flex;
    min-width: 0;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-6);
    margin: 0;
    padding: 0;
    list-style: none;
    font: var(--lv-type-body);
  }

  .breadcrumb-header li:not(:last-child)::after {
    content: '/';
    margin-left: var(--base-size-6);
    color: var(--lv-fg-muted);
  }

  .breadcrumb-header a {
    color: var(--lv-fg-muted);
    text-decoration: none;
  }

  .breadcrumb-header h1 {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
  }

  .asset-body {
    display: grid;
    min-width: 0;
    min-height: 0;
    grid-template-rows: auto auto;
  }

  .asset-body > .tabs {
    padding-inline: var(--base-size-16);
  }

  .section-body {
    min-height: 0;
    overflow: visible;
    padding: var(--base-size-16);
  }

  .lineage-body {
    padding: 0;
  }

  .graph-details-body {
    padding: 0;
  }

  .details,
  .details-content,
  .lineage-grids {
    display: grid;
    align-content: start;
    gap: var(--base-size-24);
  }

  .details-content {
    padding: var(--base-size-16);
  }

  .lineage {
    display: grid;
    min-height: 0;
    align-content: start;
  }

  .lineage-graph {
    display: block;
    height: var(--lv-lineage-graph-height);
    min-height: 0;
    border-bottom: var(--lv-border-muted);
    background: var(--lv-bg-panel);
  }

  .semantic-model-section {
    min-height: 0;
  }

  .semantic-model-graph {
    display: block;
    height: min(72svh, 48rem);
    min-height: 0;
    overflow: hidden;
    border-bottom: var(--lv-border-muted);
    background: var(--lv-bg-panel);
  }

  .lineage-grids {
    padding: var(--base-size-16);
  }

  .detail-section {
    display: grid;
    min-width: 0;
    align-content: start;
    gap: var(--base-size-12);
    border-bottom: var(--lv-border-muted);
    padding-bottom: var(--base-size-20);
  }

  .detail-section:last-child {
    border-bottom: 0;
  }

  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
    gap: var(--base-size-12) var(--base-size-20);
  }

  .facts.overview {
    grid-template-columns: repeat(auto-fit, minmax(8rem, 1fr));
  }

  .facts .wide {
    grid-column: span 2;
  }

  .facts div {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  .facts span:first-child {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  .facts p,
  .facts code {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body);
  }

  .facts .wide p,
  .facts .wide code {
    white-space: pre-wrap;
  }

  @media (max-width: 720px) {
    .page {
      padding: var(--base-size-12);
    }

    .toolbar {
      align-items: stretch;
      flex-wrap: wrap;
    }

    .toolbar-filters {
      width: 100%;
      flex: 1 1 100%;
    }

    .search {
      flex: 1 1 auto;
      width: 100%;
    }

    .toolbar-actions {
      margin-left: auto;
    }

    .header,
    .breadcrumb-header {
      grid-template-columns: 1fr;
    }

    .asset-page {
      height: auto;
      min-height: 100svh;
      overflow: visible;
    }

    .section-body {
      overflow: visible;
    }

    .graph-details-body {
      overflow: visible;
    }

    .semantic-model-graph {
      height: 32rem;
    }
  }
`

if (!customElements.get('lv-workspace-page')) customElements.define('lv-workspace-page', LeapViewWorkspacePage)
if (!customElements.get('lv-workspace-asset-page')) customElements.define('lv-workspace-asset-page', LeapViewWorkspaceAssetPage)
if (!customElements.get('lv-connections-page')) customElements.define('lv-connections-page', LeapViewConnectionsPage)
