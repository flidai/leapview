import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { CheckCircle2, Clock3, Copy, Table2, XCircle } from 'lucide'
import type { AdminPageSignal, AdminContentSectionSignal, AdminPublicationSignal, AdminQueryDetailSignal, AdminQueryHistoryFilters, AdminQueryHistorySignal, AdminStorageSignal, FilterMenuCommand, FilterMenuSignal, RecordTableSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure } from '../shared/command-failure'
import { entityDetailStyles, renderEntityDetail } from '../shared/entity-detail'
import { lucideIcon } from '../shared/lucide-icons'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import { checkSignalContract } from '../shared/signal-contract'
import '../shared/code-block'
import '../shared/drawer'
import '../shared/entity-list'
import '../shared/filter-menu'
import '../shared/record-table'
import '../shared/user-avatar'
import './agent-tools'
import './agent-prompt-editor'
import './personal-settings'
import './product-settings'
import './settings-surfaces'

const emptyStorage: AdminStorageSignal = {
  summary: {
    totalDataSizeLabel: '',
    tableCount: 0,
    dataFileCount: 0,
  },
  status: '',
  tables: [],
}

const storageColumns = [
  { id: 'name', label: 'Name', width: '155px' },
  { id: 'schema', label: 'Schema', width: '85px' },
  { id: 'type', label: 'Type', width: '60px' },
  { id: 'rows', label: 'Rows', width: '85px', align: 'right' as const },
  { id: 'columns', label: 'Columns', width: '70px', align: 'right' as const },
  { id: 'files', label: 'Files', width: '55px', align: 'right' as const },
  { id: 'size', label: 'Data size', width: '85px', align: 'right' as const },
  { id: 'snapshot', label: 'Snapshot', width: '75px', align: 'right' as const },
]

class LeapViewAdminPage extends DatastarLit(LitElement) {
  @state() private queryFilters: AdminQueryHistoryFilters = {}
  @state() private copiedQueryDetailValue = ''
  @state() private publicationBusy = ''
  @state() private publicationMessage = ''
  @state() private accessCreateDialog: 'principal' | 'group' | '' = ''
  private queryFilterTimer: ReturnType<typeof setTimeout> | null = null
  private lastQueryHistoryKey = ''

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
  }

  static styles = [pageHeaderStyles, entityDetailStyles, css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      background: var(--lv-bg-app);
    }

    .route {
      display: grid;
      min-height: 100svh;
      grid-template-columns: minmax(0, 1fr);
      align-items: start;
      background: var(--lv-bg-app);
    }

    .main {
      display: grid;
      width: min(100%, var(--lv-page-content-max-width));
      min-width: 0;
      min-height: 100svh;
      align-content: start;
      gap: var(--base-size-12);
      box-sizing: border-box;
      justify-self: center;
      padding: var(--base-size-16);
    }

    .main-storage {
      width: 100%;
      grid-template-rows: minmax(0, 1fr);
      align-content: stretch;
      gap: 0;
      justify-self: stretch;
      padding: 0;
    }

    .main-settings {
      width: min(calc(100% - var(--base-size-48)), var(--lv-settings-content-max-width));
      gap: var(--base-size-24);
      padding: var(--base-size-64) 0;
    }

    .main-profile {
      width: min(calc(100% - var(--base-size-48)), 46rem);
    }

    .main-settings .page-title-block {
      display: grid;
      gap: var(--base-size-8);
    }

    .main-settings .page-header .page-detail {
      margin-top: 0;
    }

    .main-settings .page-header h1 {
      font: var(--lv-type-page-title);
    }

    header {
      display: grid;
      min-width: 0;
      gap: var(--base-size-4);
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

    .main-directory {
      gap: var(--base-size-16);
      padding: var(--base-size-24);
    }

    .main-directory h1 {
      font: var(--lv-type-page-title);
    }

    .metrics {
      display: grid;
          max-width: var(--lv-page-content-max-width);
      grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
      gap: var(--base-size-12);
    }

    .metric,
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

    .metric {
      display: grid;
      align-content: start;
      gap: var(--base-size-4);
      padding: var(--base-size-16);
    }

    .metric .label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: uppercase;
    }

    .metric .value {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-section-title);
    }

    .metric .meta,
    .empty {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .empty {
      padding: var(--base-size-12);
    }

    .warnings {
      display: grid;
          max-width: var(--lv-page-content-max-width);
      gap: var(--base-size-8);
    }

    .warning {
      border: var(--lv-border-attention);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-attention-muted);
      padding: var(--lv-space-control) var(--base-size-12);
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
    }

    .section {
      display: grid;
      min-width: 0;
      align-content: start;
      gap: var(--base-size-12);
    }

    .detail-section {
      gap: var(--base-size-16);
      border-top: var(--lv-border-muted);
      padding: var(--base-size-24) 0;
    }

    .detail-section .table-panel {
      border: 0;
      border-radius: 0;
    }

    .publication-list {
      display: grid;
      gap: var(--base-size-12);
    }

    .publication-card {
      display: grid;
      min-width: 0;
      gap: var(--base-size-12);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-16);
    }

    .publication-heading,
    .publication-actions {
      display: flex;
      flex-wrap: wrap;
      gap: var(--base-size-8);
      align-items: center;
      justify-content: space-between;
    }

    .publication-heading strong,
    .publication-heading code {
      overflow-wrap: anywhere;
    }

    .publication-status {
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-control);
      padding: var(--base-size-2) var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: capitalize;
    }

    .publication-details {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
      gap: var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    .publication-details span {
      display: grid;
      gap: var(--base-size-2);
    }

    .publication-details code {
      overflow-wrap: anywhere;
      color: var(--lv-fg-default);
    }

    .publication-actions button,
    .publication-actions a {
      min-height: var(--control-medium-size);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      padding: 0 var(--base-size-12);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-body);
      line-height: var(--control-medium-size);
      text-decoration: none;
    }

    .publication-actions button:disabled {
      cursor: wait;
      opacity: 0.6;
    }

    .publication-history {
      display: grid;
      gap: var(--base-size-4);
      margin: 0;
      padding-left: var(--base-size-20);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    h2 {
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    .card-facts {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
      gap: var(--base-size-12);
    }

    .local-user-panel {
      display: grid;
          max-width: var(--lv-page-content-max-width);
      gap: var(--base-size-12);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-12);
    }

    .local-user-action {
      min-height: var(--control-medium-size);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      padding: 0 var(--base-size-12);
    }

    .section {
      min-width: 0;
    }

    .local-user-action:hover,
    .local-user-action:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .local-user-action:disabled {
      cursor: not-allowed;
      opacity: 0.64;
    }

    .local-user-result {
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
    }

    .local-user-result code {
      color: var(--lv-fg-default);
    }

    .query-audit {
      display: grid;
      min-width: 0;
      gap: var(--base-size-12);
    }

    .query-filters {
      display: flex;
      flex-wrap: wrap;
      gap: var(--base-size-8);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-12);
    }

    .query-filter {
      display: grid;
      flex: 1 1 16rem;
      gap: var(--base-size-4);
      min-width: 0;
    }

    .query-filter label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: uppercase;
    }

    .query-filter input {
      min-width: 0;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small);
      background: var(--lv-bg-input);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      padding: var(--base-size-8) var(--lv-space-control);
    }

    .query-history-footer {
      display: flex;
      min-height: 2.75rem;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-12);
      border-top: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-12);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    .query-history-error {
      color: var(--lv-fg-danger);
    }

    .query-history-load-more {
      min-height: var(--lv-control-medium);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: inherit;
      padding: 0 var(--base-size-12);
    }

    .query-history-load-more:hover,
    .query-history-load-more:focus-visible {
      background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
      outline: 0;
    }

    .query-history-load-more:disabled {
      cursor: not-allowed;
      opacity: 0.64;
    }

    .query-detail-copy-row {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
    }

    .query-detail-status {
      display: inline-flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-6);
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-semibold);
    }

    .query-detail-status svg {
      display: block;
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .query-detail-status-success svg {
      color: var(--lv-fg-success);
    }

    .query-detail-status-danger svg {
      color: var(--lv-fg-danger);
    }

    .query-detail-status-attention svg {
      color: var(--lv-fg-warning);
    }

    .query-detail-status-muted svg {
      color: var(--lv-fg-muted);
    }

    .query-detail-copy {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: inherit;
    }

    .query-detail-copy {
      width: var(--base-size-20);
      height: var(--base-size-20);
      flex: none;
      padding: 0;
    }

    .query-detail-copy:hover,
    .query-detail-copy:focus-visible {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
      color: var(--lv-fg-default);
      outline: 0;
    }

    .query-detail-body {
      display: grid;
      align-content: start;
      gap: var(--base-size-16);
      min-width: 0;
    }

    .query-detail-section {
      display: grid;
      gap: var(--base-size-8);
      min-width: 0;
    }

    .query-detail-section h2,
    .query-detail-section summary {
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    .query-detail-facts {
      display: grid;
      gap: var(--base-size-6);
    }

    .query-detail-fact {
      display: grid;
      grid-template-columns: minmax(7rem, 0.44fr) minmax(0, 1fr);
      gap: var(--base-size-12);
      min-width: 0;
      align-items: start;
      font: var(--lv-type-body-compact);
    }

    .query-detail-fact span {
      color: var(--lv-fg-muted);
    }

    .query-detail-fact code,
    .query-detail-fact strong {
      min-width: 0;
      overflow-wrap: anywhere;
    }

    .query-detail-fact code,
    .query-detail-code {
      font-family: var(--fontStack-monospace);
    }

    .query-detail-code {
      max-height: 15rem;
      min-width: 0;
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-default);
      margin: 0;
      padding: var(--base-size-12);
      font: var(--lv-type-body);
      white-space: pre;
    }

    .query-detail-error {
      border-color: var(--lv-line-danger-muted, var(--lv-line-muted));
      background: var(--lv-bg-danger-muted, var(--lv-bg-panel-muted));
    }

    .query-detail-raw {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-12);
    }

    .query-detail-raw summary {
      cursor: pointer;
    }

    @media (max-width: 640px) {
      .route {
        grid-template-columns: 1fr;
      }

      .main {
        padding: var(--base-size-12);
      }

      .main-settings {
        gap: var(--base-size-24);
        padding: var(--base-size-32) var(--base-size-16) var(--base-size-64);
      }

      .main-profile {
        width: 100%;
      }

      .main-settings .page-header h1 {
        font: var(--lv-type-page-title);
      }

      .local-user-action {
        width: 100%;
      }
    }
  `]

  disconnectedCallback(): void {
    if (this.queryFilterTimer) clearTimeout(this.queryFilterTimer)
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    super.disconnectedCallback()
  }

  updated(): void {
    checkSignalContract('admin page', this.page, { kind: 'required', title: 'required' })
    const historyKey = JSON.stringify(this.currentQueryHistory().filters)
    if (historyKey !== this.lastQueryHistoryKey) {
      this.lastQueryHistoryKey = historyKey
      const history = this.currentQueryHistory()
      this.queryFilters = { ...history.filters }
      if (this.queryDetail?.eventId && !tableRows(history.table).some((row) => String(row.id ?? '') === this.queryDetail?.eventId)) {
        this.closeQueryDetail()
      }
    }
    if (this.accessCreateDialog === 'principal' && this.page?.active !== 'principals') this.accessCreateDialog = ''
    if (this.accessCreateDialog === 'group' && this.page?.active !== 'groups') this.accessCreateDialog = ''
  }

  get page(): AdminPageSignal | null {
    return this.signal<AdminPageSignal | null>('page', null)
  }

  get queryHistory(): AdminQueryHistorySignal | null {
    return this.signal<AdminQueryHistorySignal | null>('adminQueryHistory', null)
  }

  get queryDetail(): AdminQueryDetailSignal | null {
    return this.signal<AdminQueryDetailSignal | null>('adminQueryDetail', null)
  }

  get agentPrompt(): string {
    return this.signal<string>('adminAgentCommand.systemPrompt', '')
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    const mainClass = [
      'main',
      page.active === 'principals' || page.active === 'groups' || page.active === 'principal-detail' || page.active === 'group-detail' || page.active === 'projects-admin' || page.active === 'storage' || page.active === 'storage-detail' ? 'main-directory' : '',
      isPersonalSettings(page.active) || isProductSettings(page.active) ? 'main-settings' : '',
      page.active === 'profile' ? 'main-profile' : '',
    ].filter(Boolean).join(' ')
    return html`
      <div class="route">
        <section class=${mainClass} aria-label="Admin">
          ${page.active === 'principal-detail' || page.active === 'group-detail' || page.active === 'storage-detail' ? nothing : renderPageHeader(page.headerTitle || page.title, page.headerDetail)}
          ${page.empty ? html`<div class="panel"><div class="empty">${page.empty}</div></div>` : nothing}
          ${page.metrics?.length && page.active !== 'queries' && page.active !== 'principal-detail' && page.active !== 'group-detail' && page.active !== 'storage-detail' ? html`
            <div class="metrics">
              ${page.metrics.map((metric) => html`
                <div class="metric">
                  <span class="label">${metric.label}</span>
                  <span class="value">${metric.value || '-'}</span>
                  ${metric.detail ? html`<span class="meta">${metric.detail}</span>` : nothing}
                </div>
              `)}
            </div>
          ` : nothing}
          ${this.renderLocalUserAdmin(page)}
          ${page.active === 'principals' && page.directoryList
            ? html`<lv-entity-list
                .items=${adminPrincipalListItems(page)}
                .columns=${adminPrincipalListColumns()}
                .filters=${adminPrincipalListFilters()}
                .actions=${[{ id: 'create-principal', label: 'Create local user', emphasis: 'primary' }]}
                initial-query=${page.listQuery ?? ''}
                active-filter=${page.listFilter ?? 'all'}
                search-placeholder=${page.directoryList.searchPlaceholder}
                list-label="Members"
                empty-text="No members match the current filters."
                export-filename="members.csv"
                @lv-entity-list-action=${this.handleEntityListAction}
              ></lv-entity-list>`
            : page.active === 'groups'
              ? html`<lv-entity-list .items=${adminGroupListItems(page)} .columns=${adminGroupListColumns()} .filters=${adminGroupListFilters(page)} .actions=${[{ id: 'create-group', label: 'Create group', emphasis: 'primary' }]} initial-query=${page.listQuery ?? ''} active-filter=${page.listFilter ?? 'all'} search-placeholder="Search groups by name or ID" empty-text="No groups found." export-filename="groups.csv" @lv-entity-list-action=${this.handleEntityListAction}></lv-entity-list>`
            : isPersonalSettings(page.active) ? html`<lv-personal-settings></lv-personal-settings>`
              : isProductSettings(page.active) ? html`<lv-product-settings></lv-product-settings>`
                : page.active === 'projects-admin' ? html`<lv-project-registry></lv-project-registry>`
                  : page.active === 'service-accounts' ? html`<lv-service-accounts></lv-service-accounts>`
                    : page.active === 'audit' ? html`<lv-audit-log></lv-audit-log>`
                      : page.active === 'storage' ? this.renderStorage(page) : page.active === 'storage-detail' ? this.renderStorageDetail(page) : page.active === 'agent' ? this.renderAgent(page) : page.active === 'queries' ? this.renderQueries(page) : page.active === 'publications' ? this.renderPublications(page.publications ?? []) : page.active === 'principal-detail' || page.active === 'group-detail' ? nothing : page.sections?.map((section) => renderSection(section))}
        </section>
      </div>
    `
  }

  private renderLocalUserAdmin(page: AdminPageSignal) {
    if (page.active === 'principals' || page.active === 'principal-detail') return html`<lv-principal-administration .createOpen=${this.accessCreateDialog === 'principal'} @lv-access-create-close=${this.closeAccessCreateDialog}></lv-principal-administration>`
    if (page.active === 'groups' || page.active === 'group-detail') return html`<lv-group-administration .createOpen=${this.accessCreateDialog === 'group'} @lv-access-create-close=${this.closeAccessCreateDialog}></lv-group-administration>`
    return nothing
  }

  private handleEntityListAction(event: CustomEvent<{ id: string }>): void {
    if (event.detail.id === 'create-principal') this.accessCreateDialog = 'principal'
    if (event.detail.id === 'create-group') this.accessCreateDialog = 'group'
  }

  private closeAccessCreateDialog = (): void => {
    this.accessCreateDialog = ''
  }

  private renderAgent(page: AdminPageSignal) {
    const agent = page.agent
    const systemPrompt = this.agentPrompt || agent?.systemPrompt || ''
    return html`
      ${agent ? html`
        <section class="section" aria-label="System prompt">
          <h2>System prompt</h2>
          <slot name="agent-prompt">
            <lv-agent-prompt-editor value=${systemPrompt} .value=${systemPrompt} ?disabled=${!agent.canWrite}></lv-agent-prompt-editor>
          </slot>
        </section>
        <section class="section" aria-label="Tools">
          <h2>Tools</h2>
          <lv-agent-tools .tools=${agent.tools}></lv-agent-tools>
        </section>
      ` : nothing}
    `
  }

  private renderStorage(page: AdminPageSignal) {
    const storage = page.storage ?? emptyStorage
    const items = (storage.tables ?? []).map((table) => ({
      id: table.key,
      title: table.name,
      href: `/admin/storage/tables/${encodeURIComponent(table.schema || 'default')}/${encodeURIComponent(table.name)}`,
      icon: table.type === 'view' ? 'view' : 'table',
      iconTreatment: 'plain' as const,
      columns: {
        schema: table.schema || 'default',
        type: table.type || 'table',
        rows: table.rowCountLabel || table.rowCount || '—',
        columns: table.columnCount ?? '—',
        files: table.fileCount ?? 0,
        size: table.sizeLabel || '—',
        snapshot: table.beginSnapshot || '—',
      },
      sortValues: {
        rows: table.rowCount ?? 0,
        columns: table.columnCount ?? 0,
        files: table.fileCount ?? 0,
        size: table.sizeBytes ?? 0,
        snapshot: table.beginSnapshot ?? 0,
      },
    }))
    return html`
      <lv-entity-list
        .items=${items}
        .columns=${storageColumns}
        client-filter
        list-label="Storage tables"
        search-placeholder="Search storage tables"
        empty-text=${storage.status || 'No storage tables found.'}
      ></lv-entity-list>
    `
  }

  private renderStorageDetail(page: AdminPageSignal) {
    if (page.empty) return nothing
    return renderEntityDetail({
      label: 'Storage table details',
      backHref: '/admin/storage',
      backLabel: 'All storage tables',
      avatar: lucideIcon(Table2, { size: 32, strokeWidth: 1.75 }),
      avatarTreatment: 'plain',
      title: page.headerTitle || page.title,
      subtitle: page.headerDetail,
      sections: html`
        ${page.metrics?.length ? html`
          <section class="section detail-section" aria-label="Overview">
            <h2>Overview</h2>
            <dl class="facts">
              ${page.metrics.map((metric) => html`
                <div class="fact">
                  <dt>${metric.label}</dt>
                  <dd>${metric.value || '-'}</dd>
                </div>
              `)}
            </dl>
          </section>
        ` : nothing}
        ${page.sections?.map((section) => renderSection(section, true))}
      `,
    })
  }

  private renderQueries(page: AdminPageSignal) {
    const history = this.currentQueryHistory(page)
    const rows = tableRows(history.table)
    const detail = this.queryDetail ?? emptyQueryDetail
    return html`
      <section class="query-audit" aria-label="Query audit">
        <div class="query-filters" aria-label="Query event filters" @lv-filter-menu-command=${this.handleFilterMenuCommand}>
          ${history.filterMenus?.map((menu) => this.renderFilterMenu(menu))}
          ${this.renderTextFilter('search', 'Statement / ID')}
        </div>
        <div class="panel table-panel" @lv-record-table-action=${this.handleQueryTableAction}>
          <lv-record-table variant="compact" .table=${history.table}></lv-record-table>
          <div class="query-history-footer" aria-live="polite">
            <span class=${history.error ? 'query-history-error' : ''}>${history.error || history.loadedCountLabel || `${rows.length} queries loaded`}</span>
            ${history.hasMore ? html`
              <button
                type="button"
                class="query-history-load-more"
                ?disabled=${history.loading}
                @click=${this.loadMoreQueryHistory}
              >
                ${history.loading ? 'Loading...' : 'Load more'}
              </button>
            ` : nothing}
          </div>
        </div>
        ${detail.eventId || detail.loading || detail.error ? this.renderQueryDetail(detail) : nothing}
      </section>
    `
  }

  private renderPublications(publications: AdminPublicationSignal[]) {
    return html`
      <section class="publication-list" aria-label="Dashboard publications">
        ${publications.map((publication) => {
          const key = `${publication.projectId}/${publication.name}`
          const busy = this.publicationBusy === key
          return html`
            <article class="publication-card">
              <div class="publication-heading">
                <strong>${publication.name}</strong>
                <span class="publication-status">${publication.status}</span>
              </div>
              <div class="publication-details">
                <span>Project <code>${publication.projectId}</code></span>
                <span>Dashboard <code>${publication.dashboard}${publication.defaultPage ? ` / ${publication.defaultPage}` : ''}</code></span>
                <span>Generation <code>${publication.generation || '-'}</code></span>
                <span>Allowed origins <code>${publication.origins.length ? publication.origins.join(', ') : 'Direct view only'}</code></span>
                <span>Suspended <code>${publication.suspendedAt || '-'}</code></span>
                <span>Rotated <code>${publication.rotatedAt || '-'}</code></span>
              </div>
              <div class="publication-actions">
                <a href=${publication.publicUrl} target="_blank" rel="noreferrer">Open</a>
                <button type="button" @click=${() => this.copyPublication(publication.publicUrl, 'Public link copied')}>Copy link</button>
                <button type="button" @click=${() => this.copyPublication(publication.iframeSnippet, 'Iframe copied')}>Copy iframe</button>
                ${publication.status === 'suspended'
                  ? html`<button type="button" ?disabled=${busy} @click=${() => this.mutatePublication(publication, 'resume')}>Resume</button>`
                  : publication.status === 'active'
                    ? html`<button type="button" ?disabled=${busy} @click=${() => this.mutatePublication(publication, 'suspend')}>Suspend</button>`
                    : nothing}
                <button type="button" ?disabled=${busy || publication.status === 'unconfigured'} @click=${() => this.rotatePublication(publication)}>Rotate URL</button>
              </div>
              ${publication.history.length ? html`
                <details>
                  <summary>Lifecycle history</summary>
                  <ul class="publication-history">${publication.history.map((event) => html`<li>${event}</li>`)}</ul>
                </details>
              ` : nothing}
            </article>
          `
        })}
        ${this.publicationMessage ? html`<span class="local-user-result" role="alert" aria-live="assertive">${this.publicationMessage}</span>` : nothing}
      </section>
    `
  }

  private async copyPublication(value: string, message: string): Promise<void> {
    await navigator.clipboard.writeText(value)
    this.publicationMessage = message
  }

  private rotatePublication(publication: AdminPublicationSignal): void {
    if (window.confirm(`Rotate ${publication.name}? The current public URL will stop working immediately.`)) {
      void this.mutatePublication(publication, 'rotate')
    }
  }

  private mutatePublication(publication: AdminPublicationSignal, action: 'suspend' | 'resume' | 'rotate'): void {
    const key = `${publication.projectId}/${publication.name}`
    this.publicationBusy = key
    this.publicationMessage = ''
    this.dispatchEvent(new CustomEvent('lv-publication-command', {
      bubbles: true,
      composed: true,
      detail: { projectId: publication.projectId, publication: publication.name, action },
    }))
  }

  private handleDatastarFetch = (event: Event): void => {
    // Datastar emits this event for every command on the page. Only consume
    // terminal failures while a publication mutation is waiting; unrelated
    // admin surfaces must keep their own state and feedback.
    if (!this.publicationBusy) return
    const failure = browserCommandFailure(event, 'Publication update')
    if (!failure) return
    this.publicationBusy = ''
    this.publicationMessage = failure.message
  }

  private renderTextFilter(key: keyof AdminQueryHistoryFilters, label: string) {
    return html`
      <div class="query-filter">
        <label for=${`query-filter-${key}`}>${label}</label>
        <input
          id=${`query-filter-${key}`}
          type="search"
          .value=${this.queryFilters[key] ?? this.currentQueryHistory().filters[key] ?? ''}
          @input=${(event: Event) => this.setQueryFilter(key, (event.currentTarget as HTMLInputElement).value)}
        >
      </div>
    `
  }

  private renderFilterMenu(menu: FilterMenuSignal) {
    return html`<lv-filter-menu .menu=${menu}></lv-filter-menu>`
  }

  private setQueryFilter(key: keyof AdminQueryHistoryFilters, value: string) {
    const filters = { ...this.queryFilters, [key]: value }
    this.queryFilters = filters
    if (this.queryFilterTimer) clearTimeout(this.queryFilterTimer)
    this.queryFilterTimer = setTimeout(() => {
      this.emitQueryHistoryCommand('reset', filters, '')
    }, 200)
  }

  private handleFilterMenuCommand = (event: CustomEvent<FilterMenuCommand>): void => {
    const command = event.detail
    if (!command?.menuId) return
    const action = command.action === 'search' ? 'filter_search' : command.action === 'clear' ? 'filter_clear' : 'filter_toggle'
    this.emitQueryHistoryCommand(action, this.currentQueryHistory().filters, '', '', command)
  }

  private loadMoreQueryHistory = () => {
    const history = this.currentQueryHistory()
    if (!history.hasMore || history.loading || !history.nextCursor) return
    this.emitQueryHistoryCommand('load_more', history.filters, history.nextCursor)
  }

  private emitQueryHistoryCommand(action: 'reset' | 'load_more' | 'select_detail' | 'close_detail' | 'filter_search' | 'filter_toggle' | 'filter_clear', filters: AdminQueryHistoryFilters, pageToken: string, eventId = '', filterMenu?: FilterMenuCommand) {
    const history = this.currentQueryHistory()
    this.dispatchEvent(new CustomEvent('lv-query-history-command', {
      bubbles: true,
      composed: true,
      detail: {
        action,
        filters,
        pageToken,
        limit: history.limit || 50,
        eventId,
        filterMenu,
      },
    }))
  }

  private currentQueryHistory(page = this.page): AdminQueryHistorySignal {
    const pageHistory = page ? (page as AdminPageSignal & { queryHistory?: AdminQueryHistorySignal }).queryHistory : null
    const history = this.queryHistory ?? pageHistory ?? null
    if (history) return history
    return {
      table: emptyQueryHistoryTable,
      filters: {},
      nextCursor: '',
      loadedCountLabel: '0 queries loaded',
      hasMore: false,
      loading: false,
      error: '',
      limit: 50,
    }
  }

  private handleQueryTableAction = (event: CustomEvent) => {
    if (event.detail?.action !== 'detail') return
    const eventId = String(event.detail.row?.id ?? '')
    if (!eventId) return
    this.copiedQueryDetailValue = ''
    this.emitQueryHistoryCommand('select_detail', this.currentQueryHistory().filters, '', eventId)
  }

  private closeQueryDetail = () => {
    this.copiedQueryDetailValue = ''
    this.emitQueryHistoryCommand('close_detail', this.currentQueryHistory().filters, '')
  }

  private renderQueryDetail(event: AdminQueryDetailSignal) {
    const statusTone = queryEventStatusTone(event.status ?? '')
    return html`
      <lv-drawer
        open
        size="wide"
        label="Query event detail"
        .modal=${false}
        @lv-drawer-close=${this.closeQueryDetail}
      >
        <div slot="title" class=${`query-detail-status query-detail-status-${statusTone}`}>
          ${lucideIcon(queryEventStatusIconComponent(event.status ?? ''), { size: 16, strokeWidth: 2 })}
          <span>${event.loading ? 'Loading' : event.statusLabel || queryEventStatusLabel(event.status ?? '')}</span>
        </div>
        <div class="query-detail-body">
          ${event.loading ? html`<section class="query-detail-section"><p class="detail">Loading query details...</p></section>` : nothing}
          ${event.error && !event.status ? html`<section class="query-detail-section"><pre class="query-detail-code query-detail-error"><code>${event.error}</code></pre></section>` : nothing}
          <section class="query-detail-section" aria-label="Query identity">
            <h2>Query identity</h2>
            <div class="query-detail-facts">
              ${this.renderCopyableFact('ID', event.eventId)}
              ${this.renderCopyableFact('Request ID', event.requestId)}
              ${this.renderCopyableFact('Correlation ID', event.correlationId)}
            </div>
          </section>
          <section class="query-detail-section" aria-label="Query text">
            <h2>Query text</h2>
            <lv-code-block language="sql" format copy .code=${event.sql || event.eventId || ''}></lv-code-block>
          </section>
          <section class="query-detail-section" aria-label="Timing">
            <h2>Timing</h2>
            <div class="query-detail-facts">
              ${queryDetailFact('Duration', `${event.durationMs ?? 0} ms`)}
              ${queryDetailFact('Planning', `${event.planningMs ?? 0} ms`)}
              ${queryDetailFact('Connection wait', `${event.connectionWaitMs ?? 0} ms`)}
              ${queryDetailFact('Database', `${event.databaseMs ?? 0} ms`)}
              ${queryDetailFact('Started at', event.createdAt)}
              ${queryDetailFact('Operation', event.operation)}
              ${queryDetailFact('Kind', event.queryKind)}
            </div>
          </section>
          <section class="query-detail-section" aria-label="Query target">
            <h2>Query target</h2>
            <div class="query-detail-facts">
              ${queryDetailFact('Project', event.projectId)}
              ${queryDetailFact('Principal', event.principalId)}
              ${queryDetailFact('Source type', event.surface)}
              ${queryDetailFact('Model', event.modelId)}
              ${queryDetailFact('Target', event.target)}
              ${queryDetailFact('Object', queryDetailObjectLabel(event))}
            </div>
          </section>
          <section class="query-detail-section" aria-label="Result">
            <h2>Result</h2>
            <div class="query-detail-facts">
              ${queryDetailFact('Rows returned', String(event.rowsReturned ?? 0))}
              ${queryDetailFact('Status', event.status)}
            </div>
            ${event.queryError ? html`<pre class="query-detail-code query-detail-error"><code>${event.queryError}</code></pre>` : nothing}
          </section>
          ${event.planText || event.queryJson ? html`
            <details class="query-detail-raw">
              <summary>Raw metadata</summary>
              ${event.planText ? html`<pre class="query-detail-code"><code>${event.planText}</code></pre>` : nothing}
              ${event.queryJson ? html`<pre class="query-detail-code"><code>${formatQueryJSON(event.queryJson)}</code></pre>` : nothing}
            </details>
          ` : nothing}
        </div>
      </lv-drawer>
    `
  }

  private renderCopyableFact(label: string, value: string | undefined | null) {
    const normalized = value == null || value === '' ? '-' : String(value)
    return html`
      <div class="query-detail-fact">
        <span>${label}</span>
        <div class="query-detail-copy-row">
          <code>${normalized}</code>
          ${normalized !== '-' ? html`
            <button
              type="button"
              class="query-detail-copy"
              aria-label=${`Copy ${label}`}
              title=${this.copiedQueryDetailValue === normalized ? 'Copied' : `Copy ${label}`}
              @click=${() => this.copyQueryDetailValue(normalized)}
            >
              ${lucideIcon(Copy, { size: 13, strokeWidth: 2 })}
            </button>
          ` : nothing}
        </div>
      </div>
    `
  }

  private async copyQueryDetailValue(value: string): Promise<void> {
    try {
      await navigator.clipboard?.writeText(value)
      this.copiedQueryDetailValue = value
    } catch {
      this.copiedQueryDetailValue = ''
    }
  }

}

function isPersonalSettings(active: string): boolean {
  return active === 'profile' || active === 'security' || active === 'api-tokens'
}

function isProductSettings(active: string): boolean {
  return active === 'general' || active === 'authentication' || active === 'system'
}

const emptyQueryHistoryTable: RecordTableSignal = {
  columns: [],
  rows: [],
  empty: 'No query events match these filters.',
}

const emptyQueryDetail: AdminQueryDetailSignal = {
  eventId: '',
  loading: false,
  error: '',
  connectionWaitMs: 0,
  durationMs: 0,
  databaseMs: 0,
  planningMs: 0,
  rowsReturned: 0,
}

function tableRows(table: RecordTableSignal | undefined | null): Array<Record<string, unknown>> {
  return Array.isArray(table?.rows) ? table.rows as Array<Record<string, unknown>> : []
}

function adminGroupTable(page: AdminPageSignal): RecordTableSignal | undefined {
  return page.sections?.find((section) => section.title === 'Groups')?.table
}

function adminPrincipalListItems(page: AdminPageSignal) {
  return (page.directoryList?.items ?? []).map((item) => ({
    id: item.id,
    title: item.name,
    description: item.username,
    href: item.href,
    avatarUrl: item.avatarUrl,
    icon: 'user',
    columns: {
      email: item.email || '—',
      status: item.status === 'inactive' ? 'Inactive' : 'Active',
      teams: `${item.groupCount} ${item.groupCount === 1 ? 'team' : 'teams'}`,
      joined: formatAdminListDate(item.joinedAt),
      lastSeen: formatAdminLastSeen(item.lastSeenAt),
    },
    columnTitles: {
      lastSeen: item.lastSeenAt ? formatAdminExactDate(item.lastSeenAt) : '',
    },
    sortValues: {
      lastSeen: adminTimestamp(item.lastSeenAt),
    },
  }))
}

function adminPrincipalListColumns() {
  return [
    { id: 'name', label: 'Name', width: '27%' },
    { id: 'email', label: 'Email', width: '22%' },
    { id: 'status', label: 'Status', width: '14%' },
    { id: 'teams', label: 'Teams', width: '12%' },
    { id: 'joined', label: 'Joined', width: '12%' },
    { id: 'lastSeen', label: 'Last seen', width: '13%' },
  ]
}

function adminPrincipalListFilters() {
  return [
    { id: 'all', label: 'All' },
    { id: 'active', label: 'Active' },
    { id: 'inactive', label: 'Inactive' },
  ]
}

function formatAdminListDate(value: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', timeZone: 'UTC' }).format(date)
}

function formatAdminLastSeen(value: string): string {
  const timestamp = adminTimestamp(value)
  if (!timestamp) return 'Never'
  const elapsed = Math.max(0, Date.now() - timestamp)
  if (elapsed < 60_000) return 'Now'
  if (elapsed < 60 * 60_000) return `${Math.floor(elapsed / 60_000)}m ago`
  if (elapsed < 24 * 60 * 60_000) return `${Math.floor(elapsed / (60 * 60_000))}h ago`
  if (elapsed < 7 * 24 * 60 * 60_000) return `${Math.floor(elapsed / (24 * 60 * 60_000))}d ago`
  return formatAdminListDate(value)
}

function formatAdminExactDate(value: string): string {
  const timestamp = adminTimestamp(value)
  if (!timestamp) return ''
  return new Intl.DateTimeFormat('en-US', {
    year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', second: '2-digit',
    timeZone: 'UTC', timeZoneName: 'short',
  }).format(timestamp)
}

function adminTimestamp(value: string): number {
  if (!value) return 0
  const timestamp = Date.parse(value)
  return Number.isNaN(timestamp) ? 0 : timestamp
}

function adminGroupListItems(page: AdminPageSignal) {
  return tableRows(adminGroupTable(page)).map((row) => {
    const name = recordValueLabel(row.name) || recordValueLabel(row.id) || 'Unnamed group'
    const provider = recordValueLabel(row.provider)
    const externalID = recordValueLabel(row.external_id)
    const memberCount = Number(row.member_count ?? 0)
    const roles = Array.isArray(row.roles) ? row.roles.map(String).join(', ') : recordValueLabel(row.roles)
    return {
      id: name,
      title: name,
      href: recordValueHref(row.name) || recordValueLabel(row.name_href) || '#',
      icon: 'group',
      iconTreatment: 'plain' as const,
      category: provider.toLowerCase(),
      columns: {
        provider: provider || '—',
        externalID: externalID || '—',
        roles: roles || '—',
        members: String(memberCount),
      },
    }
  })
}

function adminGroupListColumns() {
  return [
    { id: 'name', label: 'Name', width: '30%' },
    { id: 'provider', label: 'Provider', width: '16%' },
    { id: 'externalID', label: 'External ID', width: '23%' },
    { id: 'roles', label: 'Roles', width: '21%' },
    { id: 'members', label: 'Members', width: '10%', align: 'right' as const },
  ]
}

function adminGroupListFilters(page: AdminPageSignal) {
  const providers = page.listFilterOptions?.length
    ? page.listFilterOptions
    : Array.from(new Set(adminGroupListItems(page).map((item) => item.category).filter(Boolean))).sort()
  return [{ id: 'all', label: 'All' }, ...providers.map((provider) => ({ id: provider, label: provider.charAt(0).toUpperCase() + provider.slice(1) }))]
}

function recordValueLabel(value: unknown): string {
  if (value && typeof value === 'object') {
    const cell = value as { label?: unknown; value?: unknown }
    return String(cell.label ?? cell.value ?? '')
  }
  return String(value ?? '')
}

function recordValueHref(value: unknown): string {
  if (!value || typeof value !== 'object') return ''
  return String((value as { href?: unknown }).href ?? '')
}

function queryDetailObjectLabel(event: AdminQueryDetailSignal): string {
  const object = [event.objectType, event.objectId].filter(Boolean).join(':')
  if (object) return object
  return [event.modelId, event.target].filter(Boolean).join(':') || '-'
}

function queryEventStatusTone(status: string): string {
  switch (status) {
    case 'success':
      return 'success'
    case 'canceled':
      return 'muted'
    case 'timeout':
      return 'attention'
    default:
      return 'danger'
  }
}

function queryEventStatusIcon(status: string): string {
  switch (status) {
    case 'success':
      return 'check'
    case 'canceled':
    case 'timeout':
      return 'clock'
    default:
      return 'x'
  }
}

function queryEventStatusIconComponent(status: string): any {
  switch (queryEventStatusIcon(status)) {
    case 'check':
      return CheckCircle2
    case 'clock':
      return Clock3
    default:
      return XCircle
  }
}

function queryEventStatusLabel(status: string): string {
  switch (status) {
    case 'success':
      return 'Finished'
    case 'canceled':
      return 'Canceled'
    case 'timeout':
      return 'Timeout'
    default:
      return status || 'Error'
  }
}

function queryDetailFact(label: string, value: string | number | undefined | null) {
  return html`
    <div class="query-detail-fact">
      <span>${label}</span>
      <code>${value == null || value === '' ? '-' : String(value)}</code>
    </div>
  `
}

function formatQueryJSON(value: string): string {
  try {
    return JSON.stringify(JSON.parse(value), null, 2)
  } catch {
    return value
  }
}

function renderSection(section: AdminContentSectionSignal, detail = false) {
  return html`
    <section class=${detail ? 'section detail-section' : 'section'} aria-label=${section.title}>
      <h2>${section.title}</h2>
      ${section.table?.columns?.length
        ? html`<div class="panel table-panel"><lv-record-table variant="compact" .table=${section.table}></lv-record-table></div>`
        : detail
          ? html`<dl class="facts">${section.facts?.map((fact) => html`
              <div class="fact">
                <dt>${fact.label}</dt>
                <dd>${fact.value || '-'}</dd>
              </div>
            `)}</dl>`
          : html`<div class="card-facts">${section.facts?.map((fact) => html`
              <div class="metric">
                <span class="label">${fact.label}</span>
                <span class="value">${fact.value || '-'}</span>
              </div>
            `)}</div>`}
    </section>
  `
}

if (!customElements.get('lv-admin-page')) customElements.define('lv-admin-page', LeapViewAdminPage)
