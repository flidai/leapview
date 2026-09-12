import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import {
  ArrowLeft,
  BookOpen,
  ChartColumn,
  ChevronRight,
  ExternalLink,
  RefreshCw,
  Search,
} from 'lucide'
import type {
  AssetVersionDrawerSignal,
  ConnectionAdministrationSignal,
  ConnectionsPageSignal,
  DefinitionFactSignal,
  ModelFieldDrawerSignal,
  RefreshRunDrawerSignal,
  RecordTableSignal,
  ResourceAssetPageSignal,
  ResourceAssetSummarySignal,
  ResourceDetailSectionSignal,
  ResourcePageSignal,
  ResourceTabSignal,
  AssetOverviewLinkSignal,
  AssetOverviewSignal,
  PipelineOverviewMonitorSignal,
  PipelineOverviewRunSignal,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { assetPresentation } from '../shared/asset-presentation'
import { checkSignalContract } from '../shared/signal-contract'
import { loadDatastarRuntime } from '../shared/datastar-runtime'
import { lucideIcon } from '../shared/lucide-icons'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import { breadcrumbStyles, renderBreadcrumb, type BreadcrumbItem } from '../shared/breadcrumb'
import '../shared/entity-list'
import '../shared/loading-spinner'
import '../shared/record-table'
import '../shared/code-block'
import '../shared/config-viewer'
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
const emptyRefreshRunDrawer: RefreshRunDrawerSignal = { open: false, runId: '' }
const emptyAssetVersionDrawer: AssetVersionDrawerSignal = { open: false, versionId: '' }

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

type RefreshRunDrawerRow = Record<string, unknown> & {
  runId?: string
  statusLabel?: string
  startedAt?: string
  finishedAt?: string
  duration?: string
  trigger?: string
  triggered_by?: string
  modelId?: string
  environment?: string
  servingStateId?: string
  parentRunId?: string
  targetGeneration?: number
  createdAt?: string
  updatedAt?: string
  error?: string
}

type AssetVersionDrawerRow = Record<string, unknown> & {
  versionId?: string
  version?: string
  statusLabel?: string
  published?: string
  published_by?: string
  contentHash?: string
  sourceFile?: string
  environment?: string
  snapshotId?: string
  servingStateId?: string
  servingDigest?: string
  createdAt?: string
  activatedAt?: string
  compiledConfiguration?: string
  previousVersion?: string
  changes?: string
  changesSummary?: string
}

type SemanticModelView = 'diagram' | 'datasets' | 'dimensions' | 'metrics' | 'relationships' | 'source'

type AssetDefinitionView = {
  id: string
  label: string
  count?: number
  kind: 'section' | 'settings' | 'source'
  section?: ResourceDetailSectionSignal
}

class LeapViewProjectPage extends DatastarLit(LitElement) {
  @state() private assetQuery: string | null = null
  @state() private assetType: string | null = null
  private lastPageKey = ''

  static get styles() {
    return [pageHeaderStyles, projectStyles, breadcrumbStyles]
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
    return [pageHeaderStyles, projectStyles, breadcrumbStyles]
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
  @property({ attribute: 'create-dashboard-href' }) createDashboardHref = ''
  @state() private semanticModelView: SemanticModelView = 'diagram'
  @state() private semanticObjectQuery = ''
  @state() private assetDefinitionView = ''
  @state() private assetDefinitionQuery = ''
  private semanticModelPageKey = ''
  private assetDefinitionPageKey = ''
  private modelFieldDrawerPageKey = ''
  private pushedModelFieldDrawerEntry = false
  private refreshRunDrawerPageKey = ''
  private pushedRefreshRunDrawerEntry = false
  private assetVersionDrawerPageKey = ''
  private pushedAssetVersionDrawerEntry = false

  static get styles() {
    return [projectStyles, breadcrumbStyles]
  }

  override connectedCallback(): void {
    super.connectedCallback()
    window.addEventListener('popstate', this.syncModelFieldDrawerFromLocation)
    window.addEventListener('popstate', this.syncRefreshRunDrawerFromLocation)
    window.addEventListener('popstate', this.syncAssetVersionDrawerFromLocation)
    window.addEventListener('popstate', this.syncSemanticModelViewFromLocation)
    window.addEventListener('popstate', this.syncAssetDefinitionViewFromLocation)
  }

  override disconnectedCallback(): void {
    window.removeEventListener('popstate', this.syncModelFieldDrawerFromLocation)
    window.removeEventListener('popstate', this.syncRefreshRunDrawerFromLocation)
    window.removeEventListener('popstate', this.syncAssetVersionDrawerFromLocation)
    window.removeEventListener('popstate', this.syncSemanticModelViewFromLocation)
    window.removeEventListener('popstate', this.syncAssetDefinitionViewFromLocation)
    super.disconnectedCallback()
  }

  updated(): void {
    checkSignalContract('project asset page', this.page, { title: 'required', breadcrumbs: 'required', tabs: 'required' })
    const page = this.page
    const semanticModelPageKey = page?.asset.type === 'semantic_model' && page.activeSection === 'definition' ? page.asset.id : ''
    if (semanticModelPageKey && semanticModelPageKey !== this.semanticModelPageKey) {
      this.semanticModelPageKey = semanticModelPageKey
      this.semanticModelView = semanticModelViewFromLocation(true, semanticModelPageKey)
      rememberSemanticModelView(semanticModelPageKey, this.semanticModelView)
      this.semanticObjectQuery = ''
    }
    const assetDefinitionPageKey = page?.asset.type !== 'semantic_model' && page?.activeSection === 'definition'
      ? page.asset.id
      : ''
    if (page && assetDefinitionPageKey && assetDefinitionPageKey !== this.assetDefinitionPageKey) {
      this.assetDefinitionPageKey = assetDefinitionPageKey
      this.assetDefinitionView = assetDefinitionViewFromLocation(assetDefinitionViews(page))
      this.assetDefinitionQuery = ''
    }
    const drawerPageKey = page?.asset.type === 'model' && page.activeSection === 'definition'
      ? page.asset.detailHref
      : ''
    if (drawerPageKey && drawerPageKey !== this.modelFieldDrawerPageKey) {
      this.modelFieldDrawerPageKey = drawerPageKey
      this.syncModelFieldDrawerFromLocation()
    }
    const refreshDrawerPageKey = page?.activeSection === 'refreshes' && page.refresh?.runsTable
      ? `${page.asset.detailHref}/refreshes`
      : ''
    if (refreshDrawerPageKey && refreshDrawerPageKey !== this.refreshRunDrawerPageKey) {
      this.refreshRunDrawerPageKey = refreshDrawerPageKey
      this.syncRefreshRunDrawerFromLocation()
    }
    const versionDrawerPageKey = page?.activeSection === 'versions' && page.versions?.table
      ? `${page.asset.detailHref}/versions`
      : ''
    if (versionDrawerPageKey && versionDrawerPageKey !== this.assetVersionDrawerPageKey) {
      this.assetVersionDrawerPageKey = versionDrawerPageKey
      this.syncAssetVersionDrawerFromLocation()
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

  get refreshRunDrawer(): RefreshRunDrawerSignal {
    return this.signal<RefreshRunDrawerSignal>('refreshRunDrawer', emptyRefreshRunDrawer)
  }

  get assetVersionDrawer(): AssetVersionDrawerSignal {
    return this.signal<AssetVersionDrawerSignal>('assetVersionDrawer', emptyAssetVersionDrawer)
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    if (page.drawerParent) return this.renderDrawerPage(page)
    if (page.asset.type === 'connection') return html`
      ${this.renderConnectionPage(page)}
      ${this.renderAssetVersionDrawer(page)}
    `
    return html`
      ${this.renderAssetPage(page)}
      ${this.renderModelFieldDrawer(page)}
      ${this.renderRefreshRunDrawer(page)}
      ${this.renderAssetVersionDrawer(page)}
    `
  }

  private renderAssetVersionDrawer(page: ResourceAssetPageSignal) {
    const version = this.selectedAssetVersion(page)
    if (!version) return nothing
    const versionLabel = fieldValue(version, 'version')
    const changes = fieldValue(version, 'changes', '')
    const changesSummary = fieldValue(version, 'changesSummary', '')
    const configuration = fieldValue(version, 'compiledConfiguration', '')
    return html`
      <lv-drawer
        open
        size="wide"
        label=${`Version ${versionLabel} details`}
        .modal=${false}
        @lv-drawer-close=${this.closeAssetVersionDrawer}
      >
        <div slot="title" class="source-drawer-title version-drawer-title">
          <span aria-hidden="true">${lucideIcon(BookOpen)}</span>
          <h1>Version ${versionLabel}</h1>
        </div>
        <p slot="subtitle" class="source-drawer-subtitle version-drawer-subtitle">${[fieldValue(version, 'statusLabel'), fieldValue(version, 'published')].filter((value) => value && value !== '-').join(' · ')}</p>
        <div class="source-drawer-body version-drawer-body">
          ${renderFacts('Overview', [
            fieldFact('Status', fieldValue(version, 'statusLabel')),
            fieldFact('Published', fieldValue(version, 'published')),
            fieldFact('Published by', fieldValue(version, 'published_by')),
            fieldFact('Created', fieldValue(version, 'createdAt')),
            fieldFact('Activated', fieldValue(version, 'activatedAt')),
          ], false)}
          ${renderFacts('Provenance', [
            fieldFact('Content hash', fieldValue(version, 'contentHash'), true, true),
            fieldFact('Source file', fieldValue(version, 'sourceFile'), true, true),
            fieldFact('Environment', fieldValue(version, 'environment')),
            fieldFact('Snapshot', fieldValue(version, 'snapshotId'), false, true),
            fieldFact('Serving generation', fieldValue(version, 'servingStateId'), true, true),
            fieldFact('Serving digest', fieldValue(version, 'servingDigest'), true, true),
          ], false)}
          <section class="detail-section version-changes" aria-label="Changes from previous version">
            <h2>Changes from previous version</h2>
            ${changes
              ? html`<pre><code>${changes}</code></pre>`
              : html`<div class="empty">${changesSummary || 'No compiled configuration changes.'}</div>`}
          </section>
          <section class="detail-section" aria-label="Compiled configuration">
            <h2>Compiled configuration</h2>
            ${configuration
              ? html`<lv-code-block language="json" .code=${configuration}></lv-code-block>`
              : html`<div class="empty">No compiled configuration was recorded.</div>`}
          </section>
        </div>
      </lv-drawer>
    `
  }

  private selectedAssetVersion(page: ResourceAssetPageSignal): AssetVersionDrawerRow | null {
    if (page.activeSection !== 'versions') return null
    const drawer = this.assetVersionDrawer
    const versionId = drawer.versionId.trim()
    if (!drawer.open || !versionId) return null
    for (const row of page.versions?.table?.rows ?? []) {
      const candidate = row as AssetVersionDrawerRow
      if (fieldValue(candidate, 'versionId', '') === versionId) return candidate
    }
    return null
  }

  private renderRefreshRunDrawer(page: ResourceAssetPageSignal) {
    const run = this.selectedRefreshRun(page)
    if (!run) return nothing
    const runId = fieldValue(run, 'runId')
    const status = fieldValue(run, 'statusLabel')
    const started = fieldValue(run, 'startedAt')
    const error = fieldValue(run, 'error', '')
    return html`
      <lv-drawer
        open
        label=${`${runId} refresh details`}
        .modal=${false}
        @lv-drawer-close=${this.closeRefreshRunDrawer}
      >
        <div slot="title" class="source-drawer-title refresh-run-drawer-title">
          <span aria-hidden="true">${lucideIcon(RefreshCw)}</span>
          <h1>Refresh run</h1>
        </div>
        <p slot="subtitle" class="source-drawer-subtitle refresh-run-drawer-subtitle">${[status, started].filter((value) => value && value !== '-').join(' · ')}</p>
        <div class="source-drawer-body refresh-run-drawer-body">
          ${renderFacts('Overview', [
            fieldFact('Status', status),
            fieldFact('Started', started),
            fieldFact('Finished', fieldValue(run, 'finishedAt')),
            fieldFact('Duration', fieldValue(run, 'duration')),
          ], false)}
          ${renderFacts('Context', [
            fieldFact('Trigger', fieldValue(run, 'trigger')),
            fieldFact('Initiated by', fieldValue(run, 'triggered_by')),
            fieldFact('Semantic model', fieldValue(run, 'modelId'), false, true),
            fieldFact('Environment', fieldValue(run, 'environment')),
          ], false)}
          ${renderFacts('Execution', [
            fieldFact('Run ID', runId, true, true),
            fieldFact('Serving generation', fieldValue(run, 'servingStateId'), true, true),
            fieldFact('Parent run', fieldValue(run, 'parentRunId'), true, true),
            fieldFact('Target revision', fieldValue(run, 'targetGeneration')),
            fieldFact('Created', fieldValue(run, 'createdAt')),
            fieldFact('Updated', fieldValue(run, 'updatedAt')),
          ], false)}
          ${error ? renderFacts('Error', [fieldFact('Message', error, true)], false) : nothing}
        </div>
      </lv-drawer>
    `
  }

  private selectedRefreshRun(page: ResourceAssetPageSignal): RefreshRunDrawerRow | null {
    if (page.activeSection !== 'refreshes') return null
    const drawer = this.refreshRunDrawer
    const runId = drawer.runId.trim()
    if (!drawer.open || !runId) return null
    for (const row of page.refresh?.runsTable?.rows ?? []) {
      const candidate = row as RefreshRunDrawerRow
      if (fieldValue(candidate, 'runId', '') === runId) return candidate
    }
    return null
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
    if (page.asset.type !== 'model' || page.activeSection !== 'definition') return null
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
    if (event.detail?.action === 'open-model-field') {
      const fieldKey = fieldValue(event.detail.row ?? {}, 'fieldKey', '')
      if (!fieldKey) return
      const wasOpen = this.modelFieldDrawer.open
      this.setModelFieldDrawer({ fieldKey, open: true })
      updateURLSearchParameter('field', fieldKey, wasOpen ? 'replace' : 'push')
      if (!wasOpen) this.pushedModelFieldDrawerEntry = true
      return
    }
    if (event.detail?.action === 'open-refresh-run') {
      const runId = fieldValue(event.detail.row ?? {}, 'runId', '')
      if (!runId) return
      const wasOpen = this.refreshRunDrawer.open
      this.setRefreshRunDrawer({ open: true, runId })
      updateURLSearchParameter('refresh', runId, wasOpen ? 'replace' : 'push')
      if (!wasOpen) this.pushedRefreshRunDrawerEntry = true
      return
    }
    if (event.detail?.action === 'open-asset-version') {
      const versionId = fieldValue(event.detail.row ?? {}, 'versionId', '')
      if (!versionId) return
      const wasOpen = this.assetVersionDrawer.open
      this.setAssetVersionDrawer({ open: true, versionId })
      updateURLSearchParameter('version', versionId, wasOpen ? 'replace' : 'push')
      if (!wasOpen) this.pushedAssetVersionDrawerEntry = true
    }
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

  private closeRefreshRunDrawer = (): void => {
    this.setRefreshRunDrawer(emptyRefreshRunDrawer)
    if (this.pushedRefreshRunDrawerEntry) {
      this.pushedRefreshRunDrawerEntry = false
      window.history.back()
      return
    }
    updateURLSearchParameter('refresh', '', 'replace')
  }

  private syncRefreshRunDrawerFromLocation = (): void => {
    this.pushedRefreshRunDrawerEntry = false
    const runId = new URLSearchParams(window.location.search).get('refresh')?.trim() ?? ''
    this.setRefreshRunDrawer({ open: Boolean(runId), runId })
  }

  private setRefreshRunDrawer(drawer: RefreshRunDrawerSignal): void {
    void loadDatastarRuntime().then((runtime) => runtime.mergePatch({ refreshRunDrawer: drawer }))
  }

  private closeAssetVersionDrawer = (): void => {
    this.setAssetVersionDrawer(emptyAssetVersionDrawer)
    if (this.pushedAssetVersionDrawerEntry) {
      this.pushedAssetVersionDrawerEntry = false
      window.history.back()
      return
    }
    updateURLSearchParameter('version', '', 'replace')
  }

  private syncAssetVersionDrawerFromLocation = (): void => {
    this.pushedAssetVersionDrawerEntry = false
    const versionId = new URLSearchParams(window.location.search).get('version')?.trim() ?? ''
    this.setAssetVersionDrawer({ open: Boolean(versionId), versionId })
  }

  private setAssetVersionDrawer(drawer: AssetVersionDrawerSignal): void {
    void loadDatastarRuntime().then((runtime) => runtime.mergePatch({ assetVersionDrawer: drawer }))
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
      <section class="asset-page connection-asset-page" aria-label="Connection detail" @lv-record-table-action=${this.handleRecordTableAction}>
        <header class="breadcrumb-header">
          ${renderAssetBreadcrumb(page)}
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
          ${renderAssetBreadcrumb(page)}
          <div class="actions">
            ${this.createDashboardHref ? html`
              <a class="action-link" href=${this.createDashboardHref}>
                ${lucideIcon(ChartColumn)}
                <span>Create dashboard</span>
              </a>
            ` : nothing}
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
          <div class=${page.activeSection === 'lineage' ? 'section-body lineage-body' : page.activeSection === 'data' ? 'section-body data-body' : page.asset.type === 'semantic_model' && (page.activeSection === 'details' || page.activeSection === 'definition') ? 'section-body semantic-model-body' : 'section-body'}>
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
          ${page.refresh?.running ? html`<lv-loading-spinner size="small" aria-hidden="true"></lv-loading-spinner>` : lucideIcon(RefreshCw)}
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
    const details = page.details
    const isSemanticModel = page.asset.type === 'semantic_model'
    const modelHref = page.tabs.find((tab) => tab.id === 'definition')?.href ?? '#'
    const refreshHref = page.tabs.find((tab) => tab.id === 'refreshes')?.href ?? '#'
    const versionsHref = page.tabs.find((tab) => tab.id === 'versions')?.href ?? '#'
    const lineageHref = page.tabs.find((tab) => tab.id === 'lineage')?.href ?? '#'
    return html`
      <section class="details semantic-model-details-page" id="details" aria-label="Asset details">
        <div class="details-content semantic-model-overview">
          ${renderAssetOverview(page, details?.overview ?? [], details?.assetOverview, refreshHref, versionsHref)}
          ${page.dashboardAppearance ? html`<lv-dashboard-appearance-editor .appearance=${page.dashboardAppearance} .label=${page.title} .assetID=${page.assetId}></lv-dashboard-appearance-editor>` : nothing}
          ${renderAssetContents(details?.sections ?? [], page.asset.type, modelHref)}
          ${renderAssetImpact(details?.assetOverview, lineageHref, page.asset.type)}
        </div>
      </section>
    `
  }

  private renderDefinition(page: ResourceAssetPageSignal) {
    if (page.asset.type === 'semantic_model' && page.details?.semanticModelGraph) {
      return this.renderSemanticModelView(page)
    }
    return this.renderStructuredDefinition(page)
  }

  private selectAssetDefinitionView(view: string): void {
    if (view === this.assetDefinitionView && new URL(window.location.href).searchParams.get('view') === view) return
    this.assetDefinitionView = view
    this.assetDefinitionQuery = ''
    updateURLSearchParameter('view', view, 'push')
  }

  private syncAssetDefinitionViewFromLocation = (): void => {
    const page = this.page
    if (!page || page.asset.type === 'semantic_model' || page.activeSection !== 'definition') return
    this.assetDefinitionView = assetDefinitionViewFromLocation(assetDefinitionViews(page))
    this.assetDefinitionQuery = ''
  }

  private renderStructuredDefinition(page: ResourceAssetPageSignal) {
    const views = assetDefinitionViews(page)
    const selected = views.find((view) => view.id === this.assetDefinitionView) ?? views[0]
    const table = filterRecordTable(selected?.section?.table, this.assetDefinitionQuery)
    return html`
      <section class="semantic-model-view asset-definition-view" id="definition" aria-label="Definition">
        <header class="semantic-model-toolbar">
          <h2>Definition</h2>
        </header>
        <div class="semantic-model-layout">
          <nav class="semantic-model-navigation" aria-label="Definition views">
            <span class="semantic-model-navigation-label">Structure</span>
            ${views.filter((view) => view.kind !== 'source').map((view) => renderAssetDefinitionNavigationItem(
              view,
              selected?.id ?? '',
              (id) => this.selectAssetDefinitionView(id),
            ))}
            <span class="semantic-model-navigation-separator" aria-hidden="true"></span>
            ${views.filter((view) => view.kind === 'source').map((view) => renderAssetDefinitionNavigationItem(
              view,
              selected?.id ?? '',
              (id) => this.selectAssetDefinitionView(id),
            ))}
          </nav>
          <div class="semantic-model-content">
            ${selected?.kind === 'source'
              ? this.renderDefinitionSource(page, true)
              : selected?.kind === 'settings'
                  ? html`<div class="semantic-object-list">${renderFacts('Settings', definitionSettingsFacts(page.details?.overview ?? []), false)}</div>`
                  : selected?.section
                    ? html`
                      <div class="semantic-object-list">
                        <div class="semantic-object-list-header">
                          <div>
                            <h2>${semanticSectionName(selected.section.title)}</h2>
                            <p>${detailSectionCount(selected.section)} objects</p>
                          </div>
                          ${selected.section.table?.rows?.length
                            ? html`
                              <label class="semantic-object-search-wrap">
                                <span class="visually-hidden">Search ${semanticSectionName(selected.section.title).toLowerCase()}</span>
                                ${lucideIcon(Search, { size: 16 })}
                                <input
                                  class="semantic-object-search"
                                  type="search"
                                  placeholder="Search ${semanticSectionName(selected.section.title).toLowerCase()}…"
                                  .value=${this.assetDefinitionQuery}
                                  @input=${(event: Event) => { this.assetDefinitionQuery = (event.currentTarget as HTMLInputElement).value }}
                                />
                              </label>
                            `
                            : nothing}
                        </div>
                        ${selected.section.table?.columns?.length
                          ? html`<lv-record-table .table=${table ?? null}></lv-record-table>`
                          : renderDetailSection(selected.section)}
                      </div>
                    `
                    : html`<div class="empty">No structured definition is available.</div>`}
          </div>
        </div>
      </section>
    `
  }

  private selectSemanticModelView(view: SemanticModelView): void {
    if (view === this.semanticModelView && new URL(window.location.href).searchParams.get('view') === view) return
    this.semanticModelView = view
    this.semanticObjectQuery = ''
    updateURLSearchParameter('view', view, 'push')
    rememberSemanticModelView(this.semanticModelPageKey, view)
  }

  private syncSemanticModelViewFromLocation = (): void => {
    const page = this.page
    if (page?.asset.type !== 'semantic_model' || page.activeSection !== 'definition') return
    this.semanticModelView = semanticModelViewFromLocation(false, page.asset.id)
    rememberSemanticModelView(page.asset.id, this.semanticModelView)
    this.semanticObjectQuery = ''
  }

  private renderSemanticModelView(page: ResourceAssetPageSignal) {
    const sections = page.details?.sections ?? []
    const selectedSection = sections.find((section) => semanticSectionSlug(section.title) === this.semanticModelView)
    const table = filterRecordTable(selectedSection?.table, this.semanticObjectQuery)
    return html`
      <section class="semantic-model-view" id="definition" aria-label="Model">
        <header class="semantic-model-toolbar">
          <h2>Model</h2>
        </header>
        <div class="semantic-model-layout">
          <nav class="semantic-model-navigation" aria-label="Model views">
            ${renderSemanticModelNavigationItem('diagram', 'Diagram', undefined, this.semanticModelView, (view) => this.selectSemanticModelView(view))}
            <span class="semantic-model-navigation-label">Objects</span>
            ${sections.map((section) => renderSemanticModelNavigationItem(
              semanticSectionSlug(section.title),
              semanticSectionName(section.title),
              section.table?.rows?.length ?? 0,
              this.semanticModelView,
              (view) => this.selectSemanticModelView(view),
            ))}
            <span class="semantic-model-navigation-separator" aria-hidden="true"></span>
            ${renderSemanticModelNavigationItem('source', 'Source', undefined, this.semanticModelView, (view) => this.selectSemanticModelView(view))}
          </nav>
          <div class="semantic-model-content">
            ${this.semanticModelView === 'diagram'
              ? renderSemanticModelGraph(page.details!.semanticModelGraph!, page)
              : this.semanticModelView === 'source'
                ? this.renderDefinitionSource(page, true)
                : selectedSection
                  ? html`
                    <div class="semantic-object-list">
                      <div class="semantic-object-list-header">
                        <div>
                          <h2>${semanticSectionName(selectedSection.title)}</h2>
                          <p>${selectedSection.table?.rows?.length ?? 0} objects</p>
                        </div>
                        <label class="semantic-object-search-wrap">
                          <span class="visually-hidden">Search ${semanticSectionName(selectedSection.title).toLowerCase()}</span>
                          ${lucideIcon(Search, { size: 16 })}
                          <input
                            class="semantic-object-search"
                            type="search"
                            placeholder="Search ${semanticSectionName(selectedSection.title).toLowerCase()}…"
                            .value=${this.semanticObjectQuery}
                            @input=${(event: Event) => { this.semanticObjectQuery = (event.currentTarget as HTMLInputElement).value }}
                          />
                        </label>
                      </div>
                      <lv-record-table .table=${table ?? null}></lv-record-table>
                    </div>
                  `
                  : html`<div class="empty">This model view is unavailable.</div>`}
          </div>
        </div>
      </section>
    `
  }

  private renderDefinitionSource(page: ResourceAssetPageSignal, embedded = false) {
    const sections = page.definition?.sections ?? []
    const configuration = sections.find((section) => section.lang === 'yaml' || section.lang === 'json' || section.title.toLowerCase() === 'configuration')
    const transform = sections.find((section) => section.lang === 'sql' || section.title.toLowerCase() === 'sql' || section.title.toLowerCase() === 'transform')
    const otherSections = sections.filter((section) => section !== configuration && section !== transform)
    const fallbackSections = otherSections.length > 0 ? otherSections : configuration?.code ? [] : sections
    return html`
      <section class=${embedded ? 'details definition semantic-model-source' : 'details definition'} id=${embedded ? 'model-source' : 'definition'} aria-label="Asset definition">
        <div class="details-content">
          ${configuration?.code
            ? html`<section class="detail-section configuration-section" aria-label="Configuration">
                <h2>Configuration</h2>
                <lv-config-viewer .configuration=${configuration.code} .language=${configuration.lang}></lv-config-viewer>
              </section>`
            : nothing}
          ${fallbackSections.length > 0
            ? fallbackSections.map(renderDetailSection)
            : configuration?.code || transform?.code
              ? nothing
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
      <section class="details" id="refreshes" aria-label="Refreshes">
        <div class="details-content">
          ${page.refresh?.runsTable ? renderRecordTableSection('Refresh history', page.refresh.runsTable) : nothing}
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
        </a>
      `)}
    </nav>
  `
}

function renderDetailSection(section: ResourceDetailSectionSignal) {
  const id = detailSectionID(section.title)
  if (section.code) {
    return html`
      <section class="detail-section" id=${id} aria-label=${section.title}>
        <h2>${section.title}</h2>
        <lv-code-block language=${section.lang || 'text'} .code=${section.code}></lv-code-block>
      </section>
    `
  }
  if (section.table?.columns?.length) return renderRecordTableSection(section.title, section.table, id)
  return renderFacts(section.title, section.facts ?? [], false, id)
}

function renderSemanticModelGraph(graph: NonNullable<NonNullable<ResourceAssetPageSignal['details']>['semanticModelGraph']>, page: ResourceAssetPageSignal) {
  return html`
    <section class="semantic-model-section" aria-label="Data model graph">
        <lv-semantic-model-graph class="semantic-model-graph" .graph=${graph} storagekey=${page.assetId}></lv-semantic-model-graph>
    </section>
  `
}

function semanticSectionName(title: string): string {
  return title.replace(/\s*\(\d+\)\s*$/, '').trim()
}

function semanticSectionSlug(title: string): SemanticModelView {
  const value = semanticSectionName(title).toLowerCase()
  return isSemanticModelView(value) ? value : 'datasets'
}

function assetDefinitionSlug(title: string): string {
  return semanticSectionName(title)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

function assetDefinitionViews(page: ResourceAssetPageSignal): AssetDefinitionView[] {
  const views: AssetDefinitionView[] = []
  for (const section of page.details?.sections ?? []) {
    if (semanticSectionName(section.title).toLowerCase() === 'publications') continue
    views.push({
      id: assetDefinitionSlug(section.title),
      label: semanticSectionName(section.title),
      count: detailSectionCount(section),
      kind: 'section',
      section,
    })
  }
  if (page.asset.type === 'connection' || page.asset.type === 'refresh_pipeline' || page.asset.type === 'pipeline') {
    views.push({ id: 'settings', label: 'Settings', kind: 'settings' })
  }
  views.push({ id: 'source', label: 'Source', kind: 'source' })
  return views
}

function assetDefinitionViewFromLocation(views: AssetDefinitionView[]): string {
  const requested = new URL(window.location.href).searchParams.get('view')
  if (requested && views.some((view) => view.id === requested)) return requested
  return views[0]?.id ?? 'source'
}

function renderAssetDefinitionNavigationItem(
  view: AssetDefinitionView,
  activeView: string,
  onSelect: (view: string) => void,
) {
  return html`
    <button
      type="button"
      class="semantic-model-nav-item"
      data-definition-view=${view.id}
      data-active=${String(activeView === view.id)}
      aria-pressed=${String(activeView === view.id)}
      @click=${() => onSelect(view.id)}
    >
      <span>${view.label}</span>
      ${view.count === undefined ? nothing : html`<strong>${view.count}</strong>`}
    </button>
  `
}

function definitionSettingsFacts(facts: DefinitionFactSignal[]): DefinitionFactSignal[] {
  const overviewOnly = new Set([
    'Type', 'Key', 'Description', 'Refresh status', 'Last refreshed', 'Refresh guidance',
    'Next run', 'Current data version', 'Serving state', 'Rows', 'Physical size',
    'Data files', 'DuckLake snapshot', 'Schema status', 'Schema observed at',
  ])
  return facts.filter((fact) => !overviewOnly.has(fact.label))
}

function isSemanticModelView(value: string | null): value is SemanticModelView {
  return value === 'diagram'
    || value === 'datasets'
    || value === 'dimensions'
    || value === 'metrics'
    || value === 'relationships'
    || value === 'source'
}

function semanticModelViewStorageKey(assetID: string): string {
  return `leapview:semantic-model:${assetID}:view`
}

function semanticModelViewFromLocation(useStoredFallback: boolean, assetID: string): SemanticModelView {
  const requested = new URL(window.location.href).searchParams.get('view')
  if (isSemanticModelView(requested)) return requested
  if (useStoredFallback) {
    try {
      const stored = window.localStorage.getItem(semanticModelViewStorageKey(assetID))
      if (isSemanticModelView(stored)) return stored
    } catch {
      // Storage can be unavailable in privacy-restricted browser contexts.
    }
  }
  return 'diagram'
}

function rememberSemanticModelView(assetID: string, view: SemanticModelView): void {
  if (!assetID) return
  try {
    window.localStorage.setItem(semanticModelViewStorageKey(assetID), view)
  } catch {
    // Navigation still works when persistent browser storage is unavailable.
  }
}

function renderSemanticModelNavigationItem(
  view: SemanticModelView,
  label: string,
  count: number | undefined,
  activeView: SemanticModelView,
  onSelect: (view: SemanticModelView) => void,
) {
  return html`
    <button
      type="button"
      class="semantic-model-nav-item"
      data-model-view=${view}
      data-active=${String(activeView === view)}
      aria-pressed=${String(activeView === view)}
      @click=${() => onSelect(view)}
    >
      <span>${label}</span>
      ${count === undefined ? nothing : html`<strong>${count}</strong>`}
    </button>
  `
}

function semanticModelViewHref(modelHref: string, view: SemanticModelView): string {
  const separator = modelHref.includes('?') ? '&' : '?'
  return `${modelHref}${separator}view=${encodeURIComponent(view)}`
}

function assetDefinitionViewHref(definitionHref: string, view: string): string {
  const separator = definitionHref.includes('?') ? '&' : '?'
  return `${definitionHref}${separator}view=${encodeURIComponent(view)}`
}

function overviewFact(facts: DefinitionFactSignal[], label: string): DefinitionFactSignal | undefined {
  return facts.find((fact) => fact.label === label && fact.value?.trim())
}

function semanticRefreshTone(status: string): 'success' | 'warning' | 'danger' | 'muted' {
  switch (status.trim().toLowerCase()) {
    case 'succeeded':
    case 'success':
    case 'available':
      return 'success'
    case 'warning':
    case 'caution':
      return 'warning'
    case 'failed':
    case 'failure':
    case 'unavailable':
      return 'danger'
    default:
      return 'muted'
  }
}

function semanticRefreshLabel(status: string): string {
  const normalized = status.trim().replace(/[_-]+/g, ' ')
  if (!normalized) return 'Unknown'
  return normalized.charAt(0).toUpperCase() + normalized.slice(1)
}

function semanticRefreshDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function semanticRelativeDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const difference = date.getTime() - Date.now()
  const absolute = Math.abs(difference)
  if (absolute < 45_000) return 'just now'
  const units: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ['year', 365 * 24 * 60 * 60 * 1000],
    ['month', 30 * 24 * 60 * 60 * 1000],
    ['day', 24 * 60 * 60 * 1000],
    ['hour', 60 * 60 * 1000],
    ['minute', 60 * 1000],
  ]
  const [unit, milliseconds] = units.find(([, threshold]) => absolute >= threshold) ?? units[units.length - 1]
  return new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' }).format(Math.round(difference / milliseconds), unit)
}

function countLabel(count: number, singular: string, plural = `${singular}s`): string {
  return `${count.toLocaleString()} ${count === 1 ? singular : plural}`
}

function renderAssetOverview(
  page: ResourceAssetPageSignal,
  facts: DefinitionFactSignal[],
  overview: AssetOverviewSignal | undefined,
  refreshHref: string,
  versionsHref: string,
) {
  const key = overviewFact(facts, 'Key')
  const description = overviewFact(facts, 'Description')
  const owner = overview?.owner ?? overviewFact(facts, 'Owner')?.value
  const tags = overview?.tags?.length
    ? overview.tags
    : (overviewFact(facts, 'Tags')?.value ?? '').split(',').map((tag) => tag.trim()).filter(Boolean)
  const state = renderAssetState(page, facts, overview, refreshHref)
  return html`
    <div class=${`semantic-overview-panels ${state === nothing ? 'single' : ''}`}>
      <section class="semantic-overview-panel semantic-overview-about" aria-label="About">
        <h2>About</h2>
        <p class="semantic-overview-description">${description?.value ?? 'No description has been provided.'}</p>
        ${key ? html`
          <dl class="semantic-overview-properties">
            <div class="semantic-overview-key">
              <dt>Key</dt>
              <dd><code>${key.value}</code></dd>
            </div>
            ${owner ? html`
              <div class="semantic-overview-owner">
                <dt>Owner</dt>
                <dd>${owner}</dd>
              </div>
            ` : nothing}
            ${tags.length ? html`
              <div class="semantic-overview-tags">
                <dt>Tags</dt>
                <dd>${tags.map((tag) => html`<span class="semantic-overview-tag">${tag}</span>`)}</dd>
              </div>
            ` : nothing}
            ${overview?.activeVersion === undefined ? nothing : html`
              <div class="semantic-overview-version">
                <dt>Version</dt>
                <dd>${versionsHref === '#'
                  ? overview.activeVersion
                  : html`<a class="semantic-overview-version-link" href=${versionsHref}>${overview.activeVersion}</a>`}</dd>
              </div>
            `}
          </dl>
        ` : nothing}
      </section>
      ${state}
    </div>
    ${page.asset.type === 'refresh_pipeline' || page.asset.type === 'pipeline'
      ? renderPipelineRecentRuns(overview?.pipelineMonitor)
      : nothing}
  `
}

function renderOverviewStatus(label: string, value: string, tone: string) {
  return html`
    <div class="semantic-overview-refresh-status">
      <dt>${label}</dt>
      <dd data-tone=${tone}>
        <span class="semantic-overview-status-dot" aria-hidden="true"></span>
        <strong>${semanticRefreshLabel(value)}</strong>
      </dd>
    </div>
  `
}

function renderOverviewTime(label: string, value: string | undefined) {
  if (!value) return nothing
  return html`
    <div class="semantic-overview-last-refreshed">
      <dt>${label}</dt>
      <dd><time datetime=${value} title=${semanticRefreshDate(value)}>${semanticRelativeDate(value)}</time></dd>
    </div>
  `
}

function renderAssetState(
  page: ResourceAssetPageSignal,
  facts: DefinitionFactSignal[],
  overview: AssetOverviewSignal | undefined,
  refreshHref: string,
) {
  const type = page.asset.type
  if (type === 'dashboard') return nothing
  if (type === 'refresh_pipeline' || type === 'pipeline') {
    const monitor = overview?.pipelineMonitor
    const status = monitor?.status ?? 'unavailable'
    const latest = monitor?.latestRun
    return html`
      <section class="semantic-overview-panel semantic-overview-state" aria-label="Pipeline monitoring">
        <h2>Pipeline monitoring</h2>
        <dl class="semantic-overview-properties">
          ${renderOverviewStatus('Run status', pipelineRunLabel(status), semanticRefreshTone(status))}
          ${latest?.startedAt ? renderOverviewTime('Latest run', latest.startedAt) : nothing}
          ${latest?.duration ? html`<div><dt>Duration</dt><dd>${latest.duration}</dd></div>` : nothing}
          ${monitor?.lastSuccessfulAt ? renderOverviewTime('Last successful run', monitor.lastSuccessfulAt) : nothing}
          <div><dt>Schedule</dt><dd>${monitor?.schedule ?? 'Unavailable'}</dd></div>
          ${monitor?.schedule !== 'Manual only' ? html`
            <div><dt>Next run</dt><dd>${monitor?.nextRunAt
              ? html`<time datetime=${monitor.nextRunAt} title=${semanticRefreshDate(monitor.nextRunAt)}>${semanticRelativeDate(monitor.nextRunAt)}</time>`
              : 'Not available'}</dd></div>
          ` : nothing}
        </dl>
        ${latest?.status === 'failed' && latest.error ? html`<p class="semantic-overview-guidance" role="status">${latest.error}</p>` : nothing}
        ${status === 'unavailable' ? html`<p class="semantic-overview-guidance">Refresh state could not be loaded. Check the refresh runtime and try again.</p>` : nothing}
        <div class="semantic-overview-actions">
          ${latest ? html`<a class="semantic-overview-refresh-link" href=${latest.href}>View latest run</a>` : nothing}
          ${refreshHref === '#' ? nothing : html`<a class="semantic-overview-refresh-link" href=${refreshHref}>Refresh history</a>`}
        </div>
      </section>
    `
  }
  if (type === 'connection') {
    const lifecycle = page.connectionLifecycle
    if (!lifecycle) return nothing
    return html`
      <section class="semantic-overview-panel semantic-overview-state" aria-label="Connection state">
        <h2>Connection state</h2>
        <dl class="semantic-overview-properties">
          ${renderOverviewStatus('Status', lifecycle.statusLabel, lifecycle.tone)}
          ${renderOverviewTime('Last validated', lifecycle.lastValidatedAt)}
        </dl>
        ${lifecycle.state === 'missing' ? html`<p class="semantic-overview-note">Configuration is required before this connection can be used.</p>` : nothing}
      </section>
    `
  }
  if (type === 'source') {
    const status = overviewFact(facts, 'Schema status')?.value ?? 'not observed'
    const observed = overviewFact(facts, 'Schema observed at')?.value
    return html`
      <section class="semantic-overview-panel semantic-overview-state" aria-label="Schema observation">
        <h2>Schema observation</h2>
        <dl class="semantic-overview-properties">
          ${renderOverviewStatus('Status', status, semanticRefreshTone(status))}
          ${observed ? html`<div><dt>Observed at</dt><dd>${observed}</dd></div>` : nothing}
        </dl>
      </section>
    `
  }
  if (type === 'model') {
    const refreshed = overviewFact(facts, 'Last refreshed')?.value
    return html`
      <section class="semantic-overview-panel semantic-overview-state" aria-label="Data state">
        <h2>Data state</h2>
        <dl class="semantic-overview-properties">
          <div><dt>Last refreshed</dt><dd>${refreshed ?? 'Unknown'}</dd></div>
          ${['Rows', 'Physical size'].map((label) => {
            const fact = overviewFact(facts, label)
            return fact ? html`<div><dt>${label}</dt><dd>${fact.value}</dd></div>` : nothing
          })}
        </dl>
        ${refreshHref === '#' ? nothing : html`<div class="semantic-overview-actions"><a class="semantic-overview-refresh-link" href=${refreshHref}>Refresh history</a></div>`}
      </section>
    `
  }
  if (type === 'semantic_model') {
    const refreshStatus = overviewFact(facts, 'Refresh status')?.value
    const lastRefreshed = overviewFact(facts, 'Last refreshed')?.value
    const refreshGuidance = overviewFact(facts, 'Refresh guidance')?.value
    return html`
      <section class="semantic-overview-panel semantic-overview-state" aria-label="Data state">
        <h2>Data state</h2>
        <dl class="semantic-overview-properties">
          ${refreshStatus ? renderOverviewStatus('Refresh status', refreshStatus, semanticRefreshTone(refreshStatus)) : nothing}
          ${overview?.pipelines.length ? html`
            <div class="semantic-overview-pipeline-row">
              <dt>Pipeline</dt>
              <dd>
                <a class="semantic-overview-pipeline" href=${overview.pipelines[0].href}>${overview.pipelines[0].label}</a>
                ${overview.pipelines.length > 1 ? html`<span class="semantic-overview-more">+${overview.pipelines.length - 1} more</span>` : nothing}
              </dd>
            </div>
          ` : nothing}
          ${renderOverviewTime('Last refreshed', lastRefreshed)}
        </dl>
        ${refreshGuidance ? html`<p class="semantic-overview-guidance">${refreshGuidance}</p>` : nothing}
        ${refreshHref === '#' ? nothing : html`<div class="semantic-overview-actions"><a class="semantic-overview-refresh-link" href=${refreshHref}>Refresh history</a></div>`}
      </section>
    `
  }
  return nothing
}

function pipelineRunLabel(status: string): string {
  return status === 'not_run' ? 'No runs recorded' : status
}

function renderPipelineRecentRuns(monitor: PipelineOverviewMonitorSignal | undefined) {
  return html`
    <section class="semantic-overview-recent-runs" aria-label="Recent pipeline runs">
      <div class="semantic-model-summary-heading">
        <div><h2>Recent runs</h2><p>Latest recorded pipeline executions.</p></div>
      </div>
      ${monitor?.recentRuns.length ? html`
        <ol class="semantic-overview-run-list">
          ${monitor.recentRuns.map((run: PipelineOverviewRunSignal) => html`
            <li><a class="semantic-overview-run" href=${run.href}>
              <span class="semantic-overview-run-state" data-tone=${semanticRefreshTone(run.status)} aria-hidden="true"></span>
              <strong>${semanticRefreshLabel(run.status)}</strong>
              <span>${run.startedAt ? semanticRefreshDate(run.startedAt) : 'Start time unavailable'}</span>
              ${run.duration ? html`<span>${run.duration}</span>` : nothing}
            </a></li>
          `)}
        </ol>
      ` : html`<p class="semantic-overview-empty-impact">No pipeline runs have been recorded.</p>`}
    </section>
  `
}

function renderAssetContents(sections: ResourceDetailSectionSignal[], assetType: string, modelHref: string) {
  if (!sections.length) return nothing
  const isSemanticModel = assetType === 'semantic_model'
  const title = isSemanticModel ? 'Model contents' : `${assetTypeLabelForOverview(assetType)} contents`
  return html`
    <section class="semantic-model-summary" aria-label="Asset contents">
      <div class="semantic-model-summary-heading">
        <div>
          <h2>${title}</h2>
          <p>${assetContentsDescription(assetType)}</p>
        </div>
      </div>
      <div class="semantic-summary-cards">
        ${sections.map((section) => html`
          <a
            class=${`semantic-summary-card ${isSemanticModel ? 'semantic-overview-model-link' : 'asset-overview-content-link'}`}
            href=${isSemanticModel ? semanticModelViewHref(modelHref, semanticSectionSlug(section.title)) : assetDefinitionViewHref(modelHref, assetDefinitionSlug(section.title))}
          >
            <span>${semanticSectionName(section.title)}</span>
            <strong>${detailSectionCount(section)}</strong>
          </a>
        `)}
      </div>
    </section>
  `
}

function assetTypeLabelForOverview(assetType: string): string {
  return assetType.split('_').map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(' ')
}

function assetContentsDescription(assetType: string): string {
  switch (assetType) {
    case 'source': return 'Review the discovered fields available from this source.'
    case 'model': return 'Review the governed entities and fields in this model.'
    case 'semantic_model': return 'Browse the governed objects and relationships in this semantic model.'
    case 'dashboard': return 'Review the pages, filters, and visuals in this dashboard.'
    case 'connection': return 'Review the sources that use this connection.'
    default: return 'Review the configured contents of this asset.'
  }
}

function detailSectionCount(section: ResourceDetailSectionSignal): number {
  const match = section.title.match(/\((\d+)\)\s*$/)
  return match ? Number(match[1]) : section.table?.rows?.length ?? 0
}

function detailSectionID(title: string): string {
  return `detail-section-${assetDefinitionSlug(title)}`
}

function groupedAssetCounts(assets: AssetOverviewLinkSignal[]): string[] {
  const counts = new Map<string, number>()
  for (const asset of assets) counts.set(asset.type, (counts.get(asset.type) ?? 0) + 1)
  return Array.from(counts.entries()).map(([type, count]) => countLabel(count, type.toLowerCase()))
}

function renderImpactAssetLinks(assets: AssetOverviewLinkSignal[], className: string) {
  if (!assets.length) return nothing
  const visible = assets.slice(0, 3)
  return html`
    <ul class="semantic-overview-downstream-assets">
      ${visible.map((asset) => html`<li><a class=${className} href=${asset.href}>${asset.label}</a></li>`)}
    </ul>
    ${assets.length > visible.length ? html`<span class="semantic-overview-more">+${assets.length - visible.length} more</span>` : nothing}
  `
}

function renderAssetImpact(overview: AssetOverviewSignal | undefined, lineageHref: string, assetType: string) {
  if (!overview) return nothing
  const isSemanticModel = assetType === 'semantic_model'
  const isPipeline = assetType === 'refresh_pipeline' || assetType === 'pipeline'
  const upstreamFacts = isSemanticModel && overview.upstreamDatasetCount !== undefined
    ? [countLabel(overview.upstreamDatasetCount, 'governed dataset'), countLabel(overview.pipelines.length, 'refresh pipeline')]
    : groupedAssetCounts(overview.upstreamAssets)
  const downstreamFacts = groupedAssetCounts(overview.downstreamAssets)
  const showUpstream = upstreamFacts.length > 0 || isPipeline || isSemanticModel
  const showDownstream = downstreamFacts.length > 0 || assetType !== 'dashboard'
  if (!showUpstream && !showDownstream) return nothing
  return html`
    <section class=${`semantic-overview-impact-grid ${showUpstream && showDownstream ? '' : 'single'}`} aria-label="Lineage summary">
      ${showUpstream ? html`<article class="semantic-overview-impact semantic-overview-upstream">
        <h2>${isPipeline ? 'Refresh target' : 'Upstream'}</h2>
        ${upstreamFacts.length
          ? upstreamFacts.map((fact) => html`<p class="semantic-overview-impact-fact">${fact}</p>`)
          : html`<p class="semantic-overview-empty-impact">${isPipeline ? 'No semantic model target configured.' : 'No upstream dependencies.'}</p>`}
        ${isSemanticModel ? nothing : renderImpactAssetLinks(overview.upstreamAssets, 'semantic-overview-upstream-asset')}
        ${lineageHref === '#' ? nothing : html`<a class="semantic-overview-lineage-link" href=${lineageHref}>View lineage</a>`}
      </article>` : nothing}
      ${showDownstream ? html`<article class="semantic-overview-impact semantic-overview-downstream">
        <h2>${isPipeline ? 'Affected assets' : 'Downstream impact'}</h2>
        ${downstreamFacts.length
          ? downstreamFacts.map((fact) => html`<p class="semantic-overview-impact-fact">${fact}</p>`)
          : html`<p class="semantic-overview-empty-impact">No downstream assets.</p>`}
        ${renderImpactAssetLinks(overview.downstreamAssets, 'semantic-overview-downstream-asset')}
        ${lineageHref === '#' ? nothing : html`<a class="semantic-overview-downstream-link" href=${lineageHref}>View downstream lineage</a>`}
      </article>` : nothing}
    </section>
  `
}

function filterRecordTable(table: RecordTableSignal | undefined, query: string): RecordTableSignal | undefined {
  if (!table || !query.trim()) return table
  const normalized = query.trim().toLowerCase()
  return {
    ...table,
    rows: table.rows.filter((row) => JSON.stringify(row).toLowerCase().includes(normalized)),
  }
}

function renderFacts(title: string, facts: DefinitionFactSignal[], overview: boolean, id = '') {
  const filtered = facts.filter((fact) => fact.value?.trim())
  return html`
    <section class="detail-section" id=${id || nothing} aria-label=${title}>
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

function fieldValue(field: Record<string, unknown>, key: string, fallback = '-'): string {
  const value = field[key]
  if (value == null || String(value).trim() === '') return fallback
  return String(value)
}

function fieldFact(label: string, value: string, wide = false, code = false): DefinitionFactSignal {
  return { label, value, ...(wide ? { wide: true } : {}), ...(code ? { code: true } : {}) }
}

function renderRecordTableSection(title: string, table?: RecordTableSignal, id = '') {
  return html`
    <section class="detail-section" id=${id || nothing} aria-label=${title}>
      <h2>${title}</h2>
      <lv-record-table .table=${table ?? null}></lv-record-table>
    </section>
  `
}

function renderAssetBreadcrumb(page: ResourceAssetPageSignal) {
  return renderBreadcrumb(page.breadcrumbs.flatMap<BreadcrumbItem>((crumb) => {
    if (crumb.current) {
      return [{
        label: crumb.label,
        current: true as const,
        prefix: assetTypeGlyph(page.asset.type, 'breadcrumb'),
      }]
    }
    if (!crumb.href) return []
    return [{ label: crumb.label, href: crumb.href }]
  }))
}

function assetTypeGlyph(type: string, size: 'table' | 'inline' | 'breadcrumb' = 'table') {
  const presentation = assetPresentation(type)
  const iconSize = size === 'inline' ? 14 : 16
  return html`
    <span class=${`asset-glyph asset-kind-${presentation.token} ${size === 'table' ? '' : size}${size === 'breadcrumb' ? ' breadcrumb-glyph' : ''}`} aria-hidden="true">
      ${lucideIcon(presentation.icon, { size: iconSize, strokeWidth: 1.75 })}
    </span>
  `
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

  .asset-kind-model {
    background: var(--lv-asset-model-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-model-border, var(--lv-line-muted));
    color: var(--lv-asset-model-accent, var(--lv-fg-muted));
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

  .graph-details-body,
  .semantic-model-body {
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

  .semantic-model-details-page {
    gap: 0;
  }

  .semantic-model-overview {
    border-bottom: var(--lv-border-muted);
  }

  .semantic-overview-panels {
    display: grid;
    min-width: 0;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--base-size-12);
  }

  .semantic-overview-panels.single,
  .semantic-overview-impact-grid.single {
    grid-template-columns: minmax(0, 1fr);
  }

  .semantic-overview-panel {
    display: grid;
    min-width: 0;
    align-content: start;
    gap: var(--base-size-12);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    padding: var(--base-size-16);
  }

  .semantic-overview-description {
    max-width: 52rem;
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .semantic-overview-properties {
    display: grid;
    gap: var(--base-size-12);
    margin: 0;
  }

  .semantic-overview-properties div {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  .semantic-overview-properties dt {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .semantic-overview-properties dd {
    min-width: 0;
    margin: 0;
    overflow-wrap: anywhere;
    color: var(--lv-fg-default);
    font: var(--lv-type-body-compact);
  }

  .semantic-overview-properties code {
    font: var(--lv-type-code-inline);
  }

  .semantic-overview-tags dd,
  .semantic-overview-pipeline-row dd {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-6);
  }

  .semantic-overview-tag {
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-full);
    background: var(--lv-bg-panel-muted);
    padding: 1px var(--base-size-8);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .semantic-overview-refresh-status dd {
    display: flex;
    align-items: center;
    gap: var(--base-size-6);
  }

  .semantic-overview-status-dot {
    width: var(--base-size-8);
    height: var(--base-size-8);
    flex: 0 0 auto;
    border-radius: var(--lv-radius-full);
    background: var(--lv-fg-muted);
  }

  .semantic-overview-refresh-status dd[data-tone='success'] .semantic-overview-status-dot {
    background: var(--lv-fg-success);
  }

  .semantic-overview-refresh-status dd[data-tone='warning'] .semantic-overview-status-dot {
    background: var(--lv-fg-warning);
  }

  .semantic-overview-refresh-status dd[data-tone='danger'] .semantic-overview-status-dot {
    background: var(--lv-fg-danger);
  }

  .semantic-overview-guidance {
    color: var(--lv-fg-danger);
    font: var(--lv-type-body-compact);
  }

  .semantic-overview-note {
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
  }

  .semantic-overview-recent-runs {
    display: grid;
    gap: var(--base-size-12);
    border-top: var(--lv-border-muted);
    padding-top: var(--base-size-16);
  }

  .semantic-overview-run-list {
    display: grid;
    gap: 0;
    margin: 0;
    padding: 0;
    list-style: none;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    overflow: hidden;
  }

  .semantic-overview-run-list li + li {
    border-top: var(--lv-border-muted);
  }

  .semantic-overview-run {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-12);
    padding: var(--base-size-12);
    color: var(--lv-fg-default);
    font: var(--lv-type-body-compact);
    text-decoration: none;
  }

  .semantic-overview-run:hover,
  .semantic-overview-run:focus-visible {
    background: var(--lv-bg-control-hover);
  }

  .semantic-overview-run-state {
    width: var(--base-size-8);
    height: var(--base-size-8);
    flex: 0 0 auto;
    border-radius: var(--lv-radius-full);
    background: var(--lv-fg-muted);
  }

  .semantic-overview-run-state[data-tone='success'] { background: var(--lv-fg-success); }
  .semantic-overview-run-state[data-tone='warning'] { background: var(--lv-fg-warning); }
  .semantic-overview-run-state[data-tone='danger'] { background: var(--lv-fg-danger); }

  .semantic-overview-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--base-size-12);
  }

  .semantic-overview-refresh-link,
  .semantic-overview-version-link,
  .semantic-overview-pipeline,
  .semantic-overview-lineage-link,
  .semantic-overview-downstream-link,
  .semantic-overview-upstream-asset,
  .semantic-overview-downstream-asset {
    width: fit-content;
    color: var(--lv-fg-accent);
    font: var(--lv-type-body-compact);
    text-decoration: none;
  }

  .semantic-overview-refresh-link:hover,
  .semantic-overview-version-link:hover,
  .semantic-overview-pipeline:hover,
  .semantic-overview-lineage-link:hover,
  .semantic-overview-downstream-link:hover,
  .semantic-overview-upstream-asset:hover,
  .semantic-overview-downstream-asset:hover {
    text-decoration: underline;
  }

  .semantic-overview-more,
  .semantic-overview-empty-impact {
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
  }

  .semantic-model-summary {
    display: grid;
    gap: var(--base-size-12);
    border-top: var(--lv-border-muted);
    padding-top: var(--base-size-16);
  }

  .semantic-model-summary-heading,
  .semantic-object-list-header {
    display: flex;
    min-width: 0;
    align-items: center;
    justify-content: space-between;
    gap: var(--base-size-16);
  }

  .semantic-model-summary-heading > div,
  .semantic-object-list-header > div {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  .semantic-model-summary-heading p,
  .semantic-object-list-header p {
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
  }

  .semantic-model-summary-heading a {
    color: var(--lv-fg-accent);
    font: var(--lv-type-body-compact);
    text-decoration: none;
    white-space: nowrap;
  }

  .semantic-model-summary-heading a:hover {
    text-decoration: underline;
  }

  .semantic-summary-cards {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: var(--base-size-12);
  }

  .semantic-summary-card {
    display: grid;
    min-width: 0;
    gap: var(--base-size-8);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    padding: var(--base-size-12);
    text-decoration: none;
  }

  .semantic-summary-card:hover,
  .semantic-summary-card:focus-visible {
    border-color: var(--lv-line-accent, var(--lv-accent));
    background: var(--lv-bg-control-hover);
    outline: 0;
  }

  .semantic-summary-card span {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .semantic-summary-card strong {
    font: var(--lv-type-section-title);
  }

  .semantic-overview-impact-grid {
    display: grid;
    min-width: 0;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--base-size-12);
    border-top: var(--lv-border-muted);
    padding-top: var(--base-size-16);
  }

  .semantic-overview-impact-grid.single {
    grid-template-columns: minmax(0, 1fr);
  }

  .semantic-overview-impact {
    display: grid;
    min-width: 0;
    align-content: start;
    gap: var(--base-size-8);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    padding: var(--base-size-16);
  }

  .semantic-overview-impact-fact {
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .semantic-overview-downstream-assets {
    display: grid;
    gap: var(--base-size-4);
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .semantic-model-view {
    display: grid;
    min-height: 0;
    background: var(--lv-bg-panel);
  }

  .semantic-model-toolbar {
    display: flex;
    min-width: 0;
    min-height: var(--control-xlarge-size);
    align-items: center;
    justify-content: space-between;
    gap: var(--base-size-16);
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-8) var(--base-size-16);
  }

  .semantic-model-layout {
    display: grid;
    min-width: 0;
    grid-template-columns: 13rem minmax(0, 1fr);
    align-items: stretch;
  }

  .semantic-model-navigation {
    display: grid;
    align-content: start;
    gap: var(--base-size-4);
    border-right: var(--lv-border-muted);
    padding: var(--base-size-12);
  }

  .semantic-model-navigation-label {
    padding: var(--base-size-12) var(--lv-space-control) var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .semantic-model-navigation-separator {
    border-top: var(--lv-border-muted);
    margin: var(--base-size-4) 0;
  }

  .semantic-model-nav-item {
    display: flex;
    width: 100%;
    align-items: center;
    justify-content: space-between;
    gap: var(--base-size-8);
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: var(--lv-fg-muted);
    cursor: pointer;
    padding: var(--base-size-8) var(--lv-space-control);
    text-align: left;
    font: var(--lv-type-body-compact);
  }

  .semantic-model-nav-item[data-active='true'] {
    background: var(--lv-bg-control-hover);
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-medium);
  }

  .semantic-model-nav-item strong {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .semantic-model-nav-item:hover,
  .semantic-model-nav-item:focus-visible {
    color: var(--lv-fg-default);
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .semantic-model-content {
    min-width: 0;
  }

  .semantic-object-list {
    display: grid;
    min-width: 0;
    align-content: start;
    gap: var(--base-size-12);
    padding: var(--base-size-16);
  }

  .semantic-object-search-wrap {
    position: relative;
    display: flex;
    width: min(22rem, 40%);
    min-width: 12rem;
    align-items: center;
  }

  .semantic-object-search-wrap svg {
    position: absolute;
    left: var(--base-size-8);
    color: var(--lv-fg-muted);
    pointer-events: none;
  }

  .semantic-object-search {
    width: 100%;
    height: var(--control-medium-size);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    outline: 0;
    background: var(--lv-bg-app);
    color: var(--lv-fg-default);
    padding: 0 var(--base-size-8) 0 var(--base-size-32);
    font: var(--lv-type-body-compact);
  }

  .semantic-object-search:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .semantic-model-source .details-content {
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

  .version-drawer-body {
    gap: var(--base-size-20);
  }

  .version-changes pre {
    max-height: 24rem;
    margin: 0;
    overflow: auto;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    padding: var(--base-size-12);
    color: var(--lv-fg-default);
    font: var(--lv-type-code-block);
    white-space: pre;
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

  .source-drawer-body .facts,
  .source-drawer-body .facts.overview {
    grid-template-columns: minmax(0, 1fr);
    gap: var(--base-size-12);
  }

  .source-drawer-body .facts > div {
    grid-template-columns: minmax(7rem, .42fr) minmax(0, 1fr);
    align-items: start;
    gap: var(--base-size-16);
  }

  .source-drawer-body .facts .wide {
    grid-column: auto;
  }

  .source-drawer-body .facts span:first-child {
    font: var(--lv-type-body-compact);
    text-transform: none;
  }

  .source-drawer-body .facts p,
  .source-drawer-body .facts code,
  .source-drawer-body .facts .wide p,
  .source-drawer-body .facts .wide code {
    overflow-wrap: anywhere;
    text-overflow: clip;
    white-space: normal;
    font: var(--lv-type-body-compact);
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

    .semantic-summary-cards {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    .semantic-overview-panels,
    .semantic-overview-impact-grid {
      grid-template-columns: 1fr;
    }

    .semantic-model-layout {
      grid-template-columns: 1fr;
    }

    .semantic-model-navigation {
      display: flex;
      overflow-x: auto;
      border-right: 0;
      border-bottom: var(--lv-border-muted);
    }

    .semantic-model-navigation-label,
    .semantic-model-navigation-separator {
      display: none;
    }

    .semantic-model-nav-item {
      width: auto;
      flex: 0 0 auto;
    }

    .semantic-object-list-header {
      align-items: stretch;
      flex-direction: column;
    }

    .semantic-object-search-wrap {
      width: 100%;
    }

    .semantic-model-graph {
      height: 32rem;
    }
  }
`

if (!customElements.get('lv-project-page')) customElements.define('lv-project-page', LeapViewProjectPage)
if (!customElements.get('lv-project-asset-page')) customElements.define('lv-project-asset-page', LeapViewProjectAssetPage)
if (!customElements.get('lv-connections-page')) customElements.define('lv-connections-page', LeapViewConnectionsPage)
