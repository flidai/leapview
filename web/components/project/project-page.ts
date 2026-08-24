import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import {
  ArrowLeft,
  BookOpen,
  Cable,
  ChartColumn,
  Component,
  ExternalLink,
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
  Waypoints,
  Workflow,
  type IconNode,
} from 'lucide'
import type {
  ConnectionAdministrationSignal,
  ConnectionsPageSignal,
  DefinitionFactSignal,
  ModelFieldDrawerSignal,
  RecordTableSignal,
  ResourceAssetPageSignal,
  ResourceAssetSummarySignal,
  ResourceDetailSectionSignal,
  ResourcePageSignal,
  ResourceTabSignal,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { loadDatastarRuntime } from '../shared/datastar-runtime'
import { lucideIcon } from '../shared/lucide-icons'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import '../shared/entity-list'
import '../shared/loading-spinner'
import '../shared/record-table'
import '../shared/code-block'
import '../shared/drawer'
import { updateURLSearchParameter } from '../shared/url-search-state'
import './connection-administration'
import './dashboard-appearance-editor'
import './pipelines-page'

const emptyConnectionAdministration: ConnectionAdministrationSignal = {
  command: {
    action: '', assetId: '', authenticationMode: '', confirmationToken: '', connectorKind: '', credentialEnvironment: '', credentialProjectId: '', database: '', expectedRevision: 0,
    host: '', logicalConnection: '', objectScope: '', options: '', port: '', secretKey: '', secretPath: '', sourceIdentity: '', surface: '', tlsMode: '',
  },
  status: { error: '', loading: false, message: '' },
}

const emptyModelFieldDrawer: ModelFieldDrawerSignal = { fieldKey: '', open: false }

type ModelFieldDrawerRow = Record<string, unknown> & {
  fieldKey?: string
  label?: string
  logicalType?: string
  physicalType?: string
  nullable?: string
  contractType?: string
  metadataStatus?: string
  metadataProvenance?: string
  entities?: string
  grain?: string
  description?: string
  duckLakeSnapshot?: string
}

class LeapViewProjectPage extends DatastarLit(LitElement) {
  @state() private assetQuery: string | null = null
  @state() private assetType: string | null = null
  private lastPageKey = ''

  static get styles() {
    return [pageHeaderStyles, projectStyles]
  }

  updated(): void {
    const key = this.pageKey
    if (key !== this.lastPageKey) {
      this.lastPageKey = key
      this.assetQuery = null
      this.assetType = null
    }
    checkSignalContract('project page', this.page, { kind: 'required', title: 'required' })
  }

  get page(): ResourcePageSignal | null {
    return this.signal<ResourcePageSignal | null>('page', null)
  }

  private get pageKey(): string {
    const page = this.page
    return [page?.title ?? '', page?.assetList?.activeType ?? '', page?.assetList?.query ?? ''].join(':')
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    return this.renderAssetList(page, 'Project assets')
  }

  private renderAssetList(page: ResourcePageSignal, label: string) {
    const assetList = page.assetList
    const query = this.assetQuery ?? assetList?.query ?? ''
    // The page stream owns filtering. Keep the last server payload visible
    // until the debounced response replaces it.
    const assets = assetList?.assets ?? []
    const activeType = this.assetType ?? assetList?.activeType ?? ''
    return html`
      <section class="page" aria-label=${label}>
        ${renderPageHeader(page.title)}
        ${renderAssetToolbar(query, activeType, assetList?.tabs ?? [], 'Search project assets...', (event: Event) => {
          const value = (event.currentTarget as HTMLInputElement).value
          this.assetQuery = value
          dispatchProjectAssetFilter(event.currentTarget as EventTarget, activeType, value)
        }, (event: Event) => {
          const value = (event.currentTarget as HTMLSelectElement).value
          if (!(assetList?.tabs ?? []).some((tab) => (tab.id || 'all') === value)) return
          this.assetType = value === 'all' ? '' : value
          dispatchProjectAssetFilter(event.currentTarget as EventTarget, this.assetType, query)
        })}
        ${renderAssetTable(assets, query ? 'No assets match this search.' : assetList?.empty ?? 'No assets match this view.')}
      </section>
    `
  }
}

class LeapViewConnectionsPage extends DatastarLit(LitElement) {
  static get styles() {
    return [pageHeaderStyles, projectStyles]
  }

  updated(): void {
    checkSignalContract('connections page', this.page, { kind: 'required', title: 'required', connections: 'required' })
  }

  get page(): ConnectionsPageSignal | null {
    return this.signal<ConnectionsPageSignal | null>('page', null)
  }

  get connectionAdmin(): ConnectionAdministrationSignal {
    return this.signal<ConnectionAdministrationSignal>('connectionAdmin', emptyConnectionAdministration)
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    return html`
      <section class="page" aria-label="Connections">
        ${renderPageHeader(page.title, page.description ?? '', '', html`
          <lv-connection-administration
            surface="list"
            environment=${page.environment ?? ''}
            .lifecycles=${page.connections.map((connection) => connection.lifecycle)}
            .administration=${this.connectionAdmin}
          ></lv-connection-administration>
        `)}
        <lv-entity-list
          .items=${page.connections.map((connection) => ({
            id: connection.id,
            title: connection.title,
            description: connection.description,
            href: connection.detailHref,
            icon: 'connection',
            iconTreatment: 'plain' as const,
            columns: {
              kind: connection.kind,
              scope: connection.scope,
              sources: connection.sourceCount,
              credentials: connection.credentialStatus,
            },
          }))}
          .columns=${[
            { id: 'name', label: 'Name', width: '32%' },
            { id: 'kind', label: 'Kind / provider', width: '18%' },
            { id: 'scope', label: 'Scope', width: '16%' },
            { id: 'sources', label: 'Sources', width: '12%', align: 'right' },
            { id: 'credentials', label: 'Credentials', width: '22%' },
          ]}
          .filters=${[]}
          initial-query=${page.query ?? ''}
          search-placeholder="Search connections"
          empty-text="No connections match this search."
        ></lv-entity-list>
      </section>
    `
  }
}

class LeapViewProjectAssetPage extends DatastarLit(LitElement) {
  private modelFieldDrawerPageKey = ''
  private pushedModelFieldDrawerEntry = false

  static get styles() {
    return [projectStyles]
  }

  override connectedCallback(): void {
    super.connectedCallback()
    window.addEventListener('popstate', this.syncModelFieldDrawerFromLocation)
  }

  override disconnectedCallback(): void {
    window.removeEventListener('popstate', this.syncModelFieldDrawerFromLocation)
    super.disconnectedCallback()
  }

  updated(): void {
    checkSignalContract('project asset page', this.page, { title: 'required', breadcrumbs: 'required', tabs: 'required' })
    const page = this.page
    const drawerPageKey = page?.asset.type === 'model_table' && page.activeSection === 'details'
      ? page.asset.detailHref
      : ''
    if (drawerPageKey && drawerPageKey !== this.modelFieldDrawerPageKey) {
      this.modelFieldDrawerPageKey = drawerPageKey
      this.syncModelFieldDrawerFromLocation()
    }
  }

  get page(): ResourceAssetPageSignal | null {
    return this.signal<ResourceAssetPageSignal | null>('page', null)
  }

  get connectionAdmin(): ConnectionAdministrationSignal {
    return this.signal<ConnectionAdministrationSignal>('connectionAdmin', emptyConnectionAdministration)
  }

  get modelFieldDrawer(): ModelFieldDrawerSignal {
    return this.signal<ModelFieldDrawerSignal>('modelFieldDrawer', emptyModelFieldDrawer)
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    if (page.drawerParent) return this.renderDrawerPage(page)
    if (page.asset.type === 'connection') return this.renderConnectionPage(page)
    return html`
      ${this.renderAssetPage(page)}
      ${this.renderModelFieldDrawer(page)}
    `
  }

  private renderModelFieldDrawer(page: ResourceAssetPageSignal) {
    const field = this.selectedModelField(page)
    if (!field) return nothing
    const name = fieldValue(field, 'fieldKey')
    const label = fieldValue(field, 'label', name)
    return html`
      <lv-drawer
        open
        label=${`${name} field details`}
        .modal=${false}
        @lv-drawer-close=${this.closeModelFieldDrawer}
      >
        <div slot="title" class="source-drawer-title field-drawer-title">
          ${assetTypeGlyph('field', 'inline')}
          <h1>${name}</h1>
        </div>
        <p slot="subtitle" class="source-drawer-subtitle field-drawer-subtitle">${label}</p>
        <div class="source-drawer-body field-drawer-body">
          ${renderFacts('Overview', [
            fieldFact('Label', label),
            fieldFact('Description', fieldValue(field, 'description'), true),
          ], false)}
          ${renderFacts('Schema', [
            fieldFact('Logical type', fieldValue(field, 'logicalType')),
            fieldFact('Physical type', fieldValue(field, 'physicalType'), false, true),
            fieldFact('Nullable', fieldValue(field, 'nullable')),
            fieldFact('DuckLake snapshot', fieldValue(field, 'duckLakeSnapshot'), false, true),
          ], false)}
          ${renderFacts('Contract', [
            fieldFact('Expected type', fieldValue(field, 'contractType'), false, true),
            fieldFact('Status', fieldValue(field, 'metadataStatus')),
            fieldFact('Provenance', fieldValue(field, 'metadataProvenance')),
          ], false)}
          ${renderFacts('Semantics', [
            fieldFact('Entities', fieldValue(field, 'entities'), false, true),
            fieldFact('Grain', fieldValue(field, 'grain')),
          ], false)}
        </div>
      </lv-drawer>
    `
  }

  private selectedModelField(page: ResourceAssetPageSignal): ModelFieldDrawerRow | null {
    if (page.asset.type !== 'model_table' || page.activeSection !== 'details') return null
    const drawer = this.modelFieldDrawer
    const fieldKey = drawer.fieldKey.trim()
    if (!drawer.open || !fieldKey) return null
    for (const section of page.details?.sections ?? []) {
      for (const row of section.table?.rows ?? []) {
        const candidate = row as ModelFieldDrawerRow
        if (fieldValue(candidate, 'fieldKey', '') === fieldKey) return candidate
      }
    }
    return null
  }

  private handleRecordTableAction = (event: CustomEvent<{ action?: string, row?: ModelFieldDrawerRow }>): void => {
    if (event.detail?.action !== 'open-model-field') return
    const fieldKey = fieldValue(event.detail.row ?? {}, 'fieldKey', '')
    if (!fieldKey) return
    const wasOpen = this.modelFieldDrawer.open
    this.setModelFieldDrawer({ fieldKey, open: true })
    updateURLSearchParameter('field', fieldKey, wasOpen ? 'replace' : 'push')
    if (!wasOpen) this.pushedModelFieldDrawerEntry = true
  }

  private closeModelFieldDrawer = (): void => {
    this.setModelFieldDrawer(emptyModelFieldDrawer)
    if (this.pushedModelFieldDrawerEntry) {
      this.pushedModelFieldDrawerEntry = false
      window.history.back()
      return
    }
    updateURLSearchParameter('field', '', 'replace')
  }

  private syncModelFieldDrawerFromLocation = (): void => {
    this.pushedModelFieldDrawerEntry = false
    const fieldKey = new URLSearchParams(window.location.search).get('field')?.trim() ?? ''
    this.setModelFieldDrawer({ fieldKey, open: Boolean(fieldKey) })
  }

  private setModelFieldDrawer(drawer: ModelFieldDrawerSignal): void {
    void loadDatastarRuntime().then((runtime) => runtime.mergePatch({ modelFieldDrawer: drawer }))
  }

  private renderDrawerPage(page: ResourceAssetPageSignal) {
    const parent = page.drawerParent!
    return html`
      <div class="drawer-page">
        ${parent.asset.type === 'connection' ? this.renderConnectionPage(parent) : this.renderAssetPage(parent)}
        <lv-drawer
          open
          size="wide"
          label=${`${page.title} source details`}
          .modal=${false}
          @lv-drawer-close=${() => window.location.assign(parent.asset.detailHref)}
        >
          <div slot="title" class="source-drawer-title">
            ${assetTypeGlyph(page.asset.type, 'inline')}
            <h1>${page.title}</h1>
          </div>
          <p slot="subtitle" class="source-drawer-subtitle">Source in ${parent.title}</p>
          <div class="source-drawer-body">
            ${renderTabs(page.tabs)}
            <div class=${page.activeSection === 'lineage' ? 'source-drawer-section lineage-body' : 'source-drawer-section'}>
              ${this.renderSection(page)}
            </div>
          </div>
        </lv-drawer>
      </div>
    `
  }

  private renderConnectionPage(page: ResourceAssetPageSignal) {
    const lifecycle = page.connectionLifecycle
    const administration = this.connectionAdmin
    const feedback = administration.status.error
      ? html`<div class="connection-feedback error" role="alert">${administration.status.error}</div>`
      : administration.status.message
        ? html`<div class="connection-feedback success" role="status">${administration.status.message}</div>`
        : nothing
    const actions = lifecycle ? html`
      <lv-connection-administration
        surface="detail"
        environment=${page.environment ?? ''}
        .lifecycles=${[lifecycle]}
        .administration=${administration}
      ></lv-connection-administration>
    ` : nothing
    return html`
      <section class="asset-page connection-asset-page" aria-label="Connection detail">
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
            ${actions}
            ${page.actions?.map((action) => this.renderAction(action, page))}
          </div>
        </header>
        <div class="asset-body">
          ${renderTabs(page.tabs, 'Connection sections')}
          <div class=${page.activeSection === 'lineage' ? 'section-body lineage-body' : 'section-body'}>
            ${feedback}
            ${this.renderSection(page)}
          </div>
        </div>
      </section>
    `
  }

  private renderAssetPage(page: ResourceAssetPageSignal) {
    return html`
      <section
        class=${`asset-page${page.activeSection === 'data' ? ' data-asset-page' : ''}`}
        aria-label="Project asset detail"
        @lv-record-table-action=${this.handleRecordTableAction}
      >
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
            ${page.connectionLifecycle ? html`
              <lv-connection-administration
                surface="detail"
                environment=${page.environment ?? ''}
                .lifecycles=${[page.connectionLifecycle]}
                .administration=${this.connectionAdmin}
              ></lv-connection-administration>
            ` : nothing}
            ${page.actions?.map((action) => this.renderAction(action, page))}
          </div>
        </header>
        <div class="asset-body">
          ${renderTabs(page.tabs)}
          <div class=${page.activeSection === 'lineage' ? 'section-body lineage-body' : page.activeSection === 'data' ? 'section-body data-body' : page.activeSection === 'details' && page.details?.semanticModelGraph ? 'section-body graph-details-body' : 'section-body'}>
            ${this.renderSection(page)}
          </div>
        </div>
      </section>
    `
  }

  private renderSection(page: ResourceAssetPageSignal) {
    return page.activeSection === 'lineage'
      ? this.renderLineage(page)
      : page.activeSection === 'data'
        ? html`<lv-data-explorer embedded></lv-data-explorer>`
      : page.activeSection === 'definition'
        ? this.renderDefinition(page)
      : page.activeSection === 'refreshes'
        ? this.renderRefreshes(page)
      : page.activeSection === 'refresh'
        ? this.renderRefreshes(page)
        : page.activeSection === 'versions'
          ? this.renderVersions(page)
        : this.renderDetails(page)
  }

  private renderAction(action: NonNullable<ResourceAssetPageSignal['actions']>[number], page: ResourceAssetPageSignal) {
    if (action.command === 'run-refresh-pipeline') {
      return html`
        <button
          type="button"
          class="icon-link"
          title=${action.label}
          aria-label=${action.label}
          ?disabled=${Boolean(action.disabled || page.refresh?.running)}
          @click=${() => this.dispatchEvent(new CustomEvent('lv-run-refresh-pipeline', {
            bubbles: true,
            composed: true,
            detail: { action: 'run', assetId: page.assetId, pipelineId: page.assetId, runId: '' },
          }))}
        >
          ${page.refresh?.running ? html`<lv-loading-spinner aria-hidden="true"></lv-loading-spinner>` : lucideIcon(RefreshCw)}
        </button>
      `
    }
    if (action.icon === 'open') {
      return html`
        <a class="action-link" href=${action.href ?? '#'}>
          ${lucideIcon(ExternalLink)}
          <span>${action.label}</span>
        </a>
      `
    }
    return html`
      <a class="icon-link" href=${action.href ?? '#'} title=${action.label} aria-label=${action.label}>
        ${lucideIcon(ArrowLeft)}
      </a>
    `
  }

  private renderDetails(page: ResourceAssetPageSignal) {
    return html`
      <section class="details" id="details" aria-label="Asset details">
        ${page.details?.semanticModelGraph ? renderSemanticModelGraph(page.details.semanticModelGraph, page) : nothing}
        <div class="details-content">
		  ${page.dashboardAppearance ? html`<lv-dashboard-appearance-editor .appearance=${page.dashboardAppearance} .label=${page.title} .assetID=${page.assetId}></lv-dashboard-appearance-editor>` : nothing}
          ${renderFacts('Overview', page.details?.overview ?? [], true)}
          ${(page.details?.sections ?? []).map(renderDetailSection)}
        </div>
      </section>
    `
  }

  private renderDefinition(page: ResourceAssetPageSignal) {
    const sections = page.definition?.sections ?? []
    return html`
      <section class="details definition" id="definition" aria-label="Asset definition">
        <div class="details-content">
          ${sections.length > 0
            ? sections.map(renderDetailSection)
            : html`<div class="empty">No authored definition is available.</div>`}
        </div>
      </section>
    `
  }

  private renderLineage(page: ResourceAssetPageSignal) {
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

  private renderRefreshes(page: ResourceAssetPageSignal) {
    return html`
      <section class="details" id=${page.activeSection} aria-label=${page.activeSection === 'refresh' ? 'Model refresh' : 'Refresh runs'}>
        <div class="details-content">
          ${renderFacts('Refresh', page.refresh?.facts ?? [], true)}
          ${page.refresh?.runsTable ? renderRecordTableSection('Refreshes', page.refresh.runsTable) : nothing}
        </div>
      </section>
    `
  }

  private renderVersions(page: ResourceAssetPageSignal) {
    return html`
      <section class="details" id="versions" aria-label="Asset versions">
        ${renderRecordTableSection('Versions', page.versions?.table)}
      </section>
    `
  }
}

function renderAssetToolbar(query: string, activeType: string, tabs: ResourceTabSignal[], placeholder: string, onSearch: (event: Event) => void, onFilter: (event: Event) => void, actions: unknown = nothing) {
  return html`
    <div class="toolbar">
      <div class="toolbar-filters">
        <form class="search" @submit=${preventSubmit}>
          <input
            type="search"
            name="q"
            aria-label="Search project assets"
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
            <span class="visually-hidden">Filter project assets</span>
            <select aria-label="Filter project assets" .value=${activeType || 'all'} @change=${onFilter}>
              ${tabs.map((tab) => html`<option value=${tab.id || 'all'}>${tab.label}</option>`)}
            </select>
          </label>
        ` : nothing}
      </div>
      <div class="toolbar-actions">${actions}</div>
    </div>
  `
}

function dispatchProjectAssetFilter(target: EventTarget, type: string, query: string) {
  target.dispatchEvent(new CustomEvent('lv-project-asset-filter', {
    bubbles: true,
    composed: true,
    detail: { type, query },
  }))
}

function preventSubmit(event: Event) {
  event.preventDefault()
}

function renderAssetTable(assets: ResourceAssetSummarySignal[], empty: string) {
  if (!assets.length) return html`<div class="panel"><div class="empty">${empty}</div></div>`
  const hasParent = assets.some((asset) => asset.parentTitle && asset.parentTitle !== '-')
  const hasOpenAction = assets.some((asset) => asset.openHref && asset.openHref !== asset.detailHref)
  const table: RecordTableSignal = {
    columns: [
      { id: 'name', header: 'Name', kind: 'entity' },
      { id: 'type', header: 'Type', width: '150px' },
      ...(hasParent ? [{ id: 'parent', header: 'Parent', kind: 'link', hrefKey: 'parentHref', width: '180px' }] : []),
      { id: 'key', header: 'Identifier', kind: 'code', width: '220px' },
      ...(hasOpenAction ? [{ id: 'actions', header: 'Actions', kind: 'actions', align: 'right', width: '104px', sortable: false } as any] : []),
    ],
    rows: assets.map((asset) => {
      const actions = []
      if (asset.openHref && asset.openHref !== asset.detailHref) {
        actions.push({ label: 'Open asset', href: asset.openHref, icon: 'open' })
      }
      return {
        name: {
          label: asset.title,
          description: asset.description,
          href: asset.detailHref,
          icon: asset.type,
          iconTreatment: 'plain',
        },
        type: asset.typeLabel,
        parent: asset.parentTitle,
        parentHref: asset.parentHref,
        key: asset.key,
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

function renderTabs(tabs: ResourceTabSignal[], label = 'Asset sections') {
  if (!tabs.length) return nothing
  return html`
    <nav class="tabs" aria-label=${label}>
      ${tabs.map((tab) => html`
        <a class=${tab.active ? 'active' : ''} href=${tab.href} aria-current=${tab.active ? 'page' : nothing}>
          <span>${tab.label}</span>
          ${tab.count ? html`<span class="count">${tab.count}</span>` : nothing}
        </a>
      `)}
    </nav>
  `
}

function renderDetailSection(section: ResourceDetailSectionSignal) {
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

function renderSemanticModelGraph(graph: NonNullable<NonNullable<ResourceAssetPageSignal['details']>['semanticModelGraph']>, page: ResourceAssetPageSignal) {
  return html`
    <section class="semantic-model-section" aria-label="Data model graph">
        <lv-semantic-model-graph class="semantic-model-graph" .graph=${graph} storagekey=${page.assetId}></lv-semantic-model-graph>
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

function fieldValue(field: ModelFieldDrawerRow, key: keyof ModelFieldDrawerRow, fallback = '-'): string {
  const value = field[key]
  if (value == null || String(value).trim() === '') return fallback
  return String(value)
}

function fieldFact(label: string, value: string, wide = false, code = false): DefinitionFactSignal {
  return { label, value, ...(wide ? { wide: true } : {}), ...(code ? { code: true } : {}) }
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
    case 'metric':
      return Sigma
    case 'model_table':
      return TableProperties
    case 'page':
      return PanelTop
    case 'page_item':
      return Component
    case 'relationship':
      return Workflow
    case 'semantic_model':
      return Waypoints
    case 'source':
      return Cable
    case 'table':
      return Table2
    case 'visual':
      return ChartColumn
    case 'visual_element':
      return SquareDashedMousePointer
    default:
      return Component
  }
}

function assetPresentationToken(type: string): string {
  switch (type) {
    case 'catalog':
    case 'connection':
      return 'connection'
    case 'dashboard':
      return 'dashboard'
    case 'field':
    case 'relationship':
      return 'dimension'
    case 'filter':
      return 'filter'
    case 'metric':
      return 'metric'
    case 'model_table':
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

const projectStyles = css`
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

  .asset-page.data-asset-page {
    height: 100svh;
    min-height: 0;
    grid-template-rows: auto minmax(0, 1fr);
    overflow: hidden;
  }

  .connection-feedback {
    margin-bottom: var(--base-size-16);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    padding: var(--base-size-12) var(--base-size-16);
    font: var(--lv-type-body-compact);
  }

  .connection-feedback.error {
    border-color: var(--lv-line-danger-muted);
    color: var(--lv-fg-danger);
  }

  .connection-feedback.success {
    border-color: var(--lv-line-success-muted);
    color: var(--lv-fg-success);
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
    text-transform: uppercase;
  }

  .detail,
  .muted {
    margin-top: var(--base-size-4);
    overflow: hidden;
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body-compact);
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

  .action-link,
  .icon-link,
  .icon-button {
    display: inline-grid;
    place-items: center;
    border-radius: var(--lv-radius-default);
    text-decoration: none;
  }

  .action-link {
    display: inline-flex;
    min-height: var(--lv-button-height);
    align-items: center;
    justify-content: center;
    gap: var(--base-size-6);
    border: var(--borderWidth-default) solid var(--lv-button-accent-border-rest);
    border-radius: var(--lv-button-radius);
    background: var(--lv-button-accent-bg-rest);
    color: var(--lv-button-accent-fg-rest);
    padding: 0 var(--lv-button-padding-inline-sm);
    cursor: pointer;
    font: var(--lv-type-body);
    white-space: nowrap;
  }

  .action-link:hover {
    border-color: var(--lv-button-accent-border-hover);
    background: var(--lv-button-accent-bg-hover);
  }

  .action-link:active {
    border-color: var(--lv-button-accent-border-active);
    background: var(--lv-button-accent-bg-active);
  }

  .action-link:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
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

  .asset-kind-metric {
    background: var(--lv-asset-metric-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-metric-border, var(--lv-line-muted));
    color: var(--lv-asset-metric-accent, var(--lv-fg-muted));
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

  .breadcrumb-header nav a {
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

  .data-asset-page .asset-body {
    grid-template-rows: auto minmax(0, 1fr);
    overflow: hidden;
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

  .data-body {
    min-height: 0;
    overflow: hidden;
    padding: 0;
  }

  .data-body lv-data-explorer {
    height: 100%;
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

  .drawer-page {
    min-height: 100svh;
  }

  .source-drawer-title {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
  }

  .source-drawer-title h1 {
    font: var(--lv-type-section-title);
  }

  .source-drawer-subtitle {
    margin-top: var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
  }

  .source-drawer-body {
    display: grid;
    min-width: 0;
    align-content: start;
  }

  .source-drawer-section {
    min-width: 0;
    padding-top: var(--base-size-16);
  }

  .source-drawer-section.lineage-body {
    margin-inline: calc(-1 * var(--base-size-20));
    padding-top: 0;
  }

  .source-drawer-section .details-content {
    padding-inline: 0;
  }

  .source-drawer-section .lineage-graph {
    height: 20rem;
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

    .asset-page.data-asset-page {
      height: 100svh;
      min-height: 0;
      overflow: hidden;
    }

    .section-body {
      overflow: visible;
    }

    .data-asset-page .section-body {
      overflow: hidden;
    }

    .graph-details-body {
      overflow: visible;
    }

    .semantic-model-graph {
      height: 32rem;
    }
  }
`

if (!customElements.get('lv-project-page')) customElements.define('lv-project-page', LeapViewProjectPage)
if (!customElements.get('lv-project-asset-page')) customElements.define('lv-project-asset-page', LeapViewProjectAssetPage)
if (!customElements.get('lv-connections-page')) customElements.define('lv-connections-page', LeapViewConnectionsPage)
