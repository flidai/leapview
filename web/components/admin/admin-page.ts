import { LitElement, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { CheckCircle2, Clock3, Copy, Table2, XCircle } from 'lucide'
import type { AdminPageSignal, AdminContentSectionSignal, AdminPublicationSignal, AdminQueryDetailSignal, AdminQueryHistoryFilters, AdminQueryHistorySignal, AdminStorageSignal, FilterMenuCommand, FilterMenuSignal, RecordTableSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure } from '../shared/command-failure'
import { entityDetailStyles, renderEntityDetail } from '../shared/entity-detail'
import { lucideIcon } from '../shared/lucide-icons'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import { adminPageLayoutStyles } from './admin-page-layout.styles'
import { adminPageQueryStyles } from './admin-page-query.styles'
import { emptyStorage, storageColumns, publicationColumns, queryFailedStatuses, queryRunningStatuses, queryHistoryColumnsStorageKey, publicationKey, publicationStatusLabel, publicationListItem, publicationFact, isPersonalSettings, isProductSettings, emptyQueryHistoryTable, emptyQueryDetail, tableRows, queryHistoryPresentation, storageFilters, storageTypeLabel, queryDateInputValue, queryFilterSummaryChips, queryFiltersAfterMenuCommand, queryFilterKey, toggleQueryFilterValue, formatQueryHistoryDate, adminGroupTable, adminPrincipalListItems, adminPrincipalListColumns, adminPrincipalListFilters, formatAdminListDate, formatAdminLastSeen, formatAdminExactDate, adminTimestamp, adminGroupListItems, adminGroupListColumns, adminGroupListFilters, recordValueLabel, recordValueHref, queryDetailObjectLabel, queryDetailSummary, queryEventStatusTone, queryEventStatusIcon, queryEventStatusIconComponent, queryEventStatusLabel, queryDetailFact, formatQueryJSON, renderSection } from './admin-page-presenters'
import { checkSignalContract } from '../shared/signal-contract'
import type { EntityListColumn, EntityListItem, EntityListFilter } from '../shared/entity-list'
import '../shared/code-block'
import '../shared/drawer'
import '../shared/entity-list'
import '../shared/filter-menu'
import '../shared/record-table'
import '../shared/user-avatar'
import './agent-settings'
import './archived-chats'
import './personal-settings'
import './product-settings'
import './settings-surfaces'

class LeapViewAdminPage extends DatastarLit(LitElement) {
  @state() private queryFilters: AdminQueryHistoryFilters = {}
  @state() private copiedQueryDetailValue = ''
  @state() private publicationBusy = ''
  @state() private publicationMessage = ''
  @state() private deliveryBusy = false
  @state() private deliveryMessage = ''
  @state() private selectedPublicationKey = ''
  @state() private accessCreateDialog: 'principal' | 'group' | '' = ''
  private queryFilterTimer: ReturnType<typeof setTimeout> | null = null
  private lastQueryHistoryKey = ''

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
  }

  static styles = [pageHeaderStyles, entityDetailStyles, adminPageLayoutStyles, adminPageQueryStyles]

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
    if (this.selectedPublicationKey && this.page?.active !== 'publications') this.selectedPublicationKey = ''
    if (this.selectedPublicationKey && !(this.page?.publications ?? []).some((publication) => publicationKey(publication) === this.selectedPublicationKey)) this.selectedPublicationKey = ''
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
      page.active === 'principals' || page.active === 'groups' || page.active === 'principal-detail' || page.active === 'group-detail' || page.active === 'access' || page.active === 'service-accounts' || page.active === 'service-accounts-new' || page.active === 'storage' || page.active === 'storage-detail' || page.active === 'publications' || page.active === 'delivery' ? 'main-directory' : '',
      isPersonalSettings(page.active) || isProductSettings(page.active) ? 'main-settings' : '',
      page.active === 'profile' ? 'main-profile' : '',
      page.active === 'security' ? 'main-security' : '',
    ].filter(Boolean).join(' ')
    return html`
      <div class="route">
        <section class=${mainClass} aria-label="Admin">
          ${page.active === 'principal-detail' || page.active === 'group-detail' || page.active === 'storage-detail' || page.active === 'service-accounts' || page.active === 'service-accounts-new' || page.active === 'api-tokens' || page.active === 'api-token-new' ? nothing : renderPageHeader(page.headerTitle || page.title, page.headerDetail)}
          ${page.empty && page.active !== 'publications' && page.active !== 'storage' ? html`<div class="panel"><div class="empty">${page.empty}</div></div>` : nothing}
          ${page.metrics?.length && page.active !== 'agent' && page.active !== 'queries' && page.active !== 'principal-detail' && page.active !== 'group-detail' && page.active !== 'storage-detail' && !(page.active === 'storage' && page.storage?.status?.trim()) ? html`
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
                list-label="Users"
                empty-text="No users match the current filters."
                export-filename="users.csv"
                @lv-entity-list-action=${this.handleEntityListAction}
              ></lv-entity-list>`
            : page.active === 'groups'
              ? html`<lv-entity-list .items=${adminGroupListItems(page)} .columns=${adminGroupListColumns()} .filters=${adminGroupListFilters(page)} .actions=${[{ id: 'create-group', label: 'Create group', emphasis: 'primary' }]} initial-query=${page.listQuery ?? ''} active-filter=${page.listFilter ?? 'all'} search-placeholder="Search groups by name or ID" empty-text="No groups found." export-filename="groups.csv" @lv-entity-list-action=${this.handleEntityListAction}></lv-entity-list>`
            : page.active === 'archived-chats' ? html`<lv-archived-chats></lv-archived-chats>`
              : isPersonalSettings(page.active) ? html`<lv-personal-settings token-view=${page.active === 'api-token-new' ? 'create' : 'list'}></lv-personal-settings>`
              : isProductSettings(page.active) ? html`<lv-product-settings></lv-product-settings>`
                : page.active === 'service-accounts' || page.active === 'service-accounts-new' ? html`<lv-service-accounts .createAccountOpen=${page.active === 'service-accounts-new'}></lv-service-accounts>`
                    : page.active === 'audit' ? html`<lv-audit-log></lv-audit-log>`
                      : page.active === 'access' ? html`<lv-access-settings></lv-access-settings>`
                        : page.active === 'delivery' ? html`<section class="delivery-surface" aria-label="Delivery" aria-busy=${this.deliveryBusy ? 'true' : 'false'} @lv-record-table-action=${this.handleDeliveryTableAction}>${this.deliveryMessage ? html`<p class="local-user-result" role="status" aria-live="polite">${this.deliveryMessage}</p>` : nothing}${this.renderDeliverySections(page)}</section>`
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

  private renderDeliverySections(page: AdminPageSignal) {
    return page.sections?.map((section) => {
      if (!this.deliveryBusy || !section.table) return renderSection(section)
      const rows = (section.table.rows ?? []).map((row) => {
        const actions = Array.isArray(row.actions)
          ? row.actions.map((action) => ({ ...(action as Record<string, unknown>), disabled: true }))
          : row.actions
        return actions === row.actions ? row : { ...row, actions }
      })
      return renderSection({ ...section, table: { ...section.table, rows } })
    })
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
    return agent ? html`<lv-agent-settings .agent=${agent} .prompt=${systemPrompt}></lv-agent-settings>` : nothing
  }

  private renderStorage(page: AdminPageSignal) {
    const storage = page.storage ?? emptyStorage
    const storageError = storage.status.trim()
    const items = (storage.tables ?? []).map((table) => ({
      id: table.key,
      title: table.name,
      href: `/admin/storage/tables/${encodeURIComponent(table.schema || 'default')}/${encodeURIComponent(table.name)}`,
      icon: table.type === 'view' ? 'view' : 'table',
      iconTreatment: 'plain' as const,
      columns: {
        schema: table.schema || 'default',
        type: storageTypeLabel(table.type),
        rows: table.rowCountLabel || table.rowCount || '—',
        columns: table.columnCount ?? '—',
        files: table.fileCount ?? 0,
        size: table.sizeLabel || '—',
        snapshot: table.beginSnapshot || '—',
      },
      category: table.schema || 'default',
      sortValues: {
        rows: table.rowCount ?? 0,
        columns: table.columnCount ?? 0,
        files: table.fileCount ?? 0,
        size: table.sizeBytes ?? 0,
        snapshot: table.beginSnapshot ?? 0,
      },
    }))
    return html`
      ${storageError ? html`
        <section class="storage-error" role="alert" aria-label="Storage unavailable">
          <strong>Storage metadata is temporarily unavailable.</strong>
          <p>We could not load the table catalog. Try again later or contact an administrator if the problem continues.</p>
          <button type="button" class="storage-retry" @click=${this.retryStorage}>Retry</button>
          <details>
            <summary>Technical details</summary>
            <code>${storageError}</code>
          </details>
        </section>
      ` : nothing}
      <lv-entity-list
        .items=${items}
        .columns=${storageColumns}
        .filters=${storageFilters(storage.tables)}
        group-by=""
        ?client-filter=${!storageError}
        list-label="Storage tables"
        search-placeholder="Search storage tables"
        empty-text=${storageError ? 'No storage tables are available.' : 'No storage tables found.'}
        .showToolbar=${!storageError}
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
    const table = queryHistoryPresentation(history.table)
    const detail = this.queryDetail ?? emptyQueryDetail
    return html`
      <section class="query-audit" aria-label="Query audit">
        <div class="query-filters" aria-label="Query event filters" @lv-filter-menu-command=${this.handleFilterMenuCommand}>
          ${this.renderQueryFilterShortcuts(history)}
          ${this.renderQueryTimePresets()}
          ${history.filterMenus?.map((menu) => this.renderFilterMenu(menu))}
          ${this.renderTextFilter('search', 'Statement / ID')}
          ${this.renderQueryDateFilters()}
          ${this.renderQueryFilterSummary(history)}
        </div>
        <div class="panel table-panel" @lv-record-table-action=${this.handleQueryTableAction}>
          <lv-record-table variant="compact" .table=${table}></lv-record-table>
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
    const selected = publications.find((publication) => publicationKey(publication) === this.selectedPublicationKey)
    return html`
      <section class="publication-list" aria-label="Dashboard publications">
        <lv-entity-list
          .items=${publications.map(publicationListItem)}
          .columns=${publicationColumns}
          client-filter
          row-action="open"
          list-label="Dashboard publications"
          search-placeholder="Search publications"
          empty-text="No dashboard publications have been configured."
          @lv-entity-list-row-action=${this.handlePublicationListAction}
        ></lv-entity-list>
        ${this.publicationMessage ? html`<span class="local-user-result" role="alert" aria-live="assertive">${this.publicationMessage}</span>` : nothing}
        ${selected ? this.renderPublicationDrawer(selected) : nothing}
      </section>
    `
  }

  private handlePublicationListAction = (event: CustomEvent<{ action?: string; item?: { id?: string } }>): void => {
    if (event.detail?.action !== 'open' || !event.detail.item?.id) return
    this.selectedPublicationKey = event.detail.item.id
  }

  private closePublicationDrawer = (): void => {
    this.selectedPublicationKey = ''
  }

  private renderPublicationDrawer(publication: AdminPublicationSignal) {
    const busy = this.publicationBusy === publicationKey(publication)
    return html`
      <lv-drawer
        open
        size="wide"
        label="Publication details"
        .modal=${false}
        @lv-drawer-close=${this.closePublicationDrawer}
      >
        <div slot="title" class="publication-drawer-title">
          <h2>${publication.name}</h2>
          <p>${publication.projectId} · ${publication.dashboard}${publication.defaultPage ? ` / ${publication.defaultPage}` : ''}</p>
          <span class="publication-drawer-status">${publicationStatusLabel(publication.status)}</span>
        </div>
        <div class="publication-drawer-body">
          <section class="publication-drawer-section" aria-label="Publication URLs">
            <h3>URLs</h3>
            <dl class="publication-drawer-facts">
              <div class="publication-drawer-fact">
                <dt>Public URL</dt>
                <dd><a href=${publication.publicUrl} target="_blank" rel="noreferrer">${publication.publicUrl || 'Not configured'}</a><button class="publication-drawer-copy" type="button" @click=${() => this.copyPublication(publication.publicUrl, 'Public link copied')}>Copy link</button></dd>
              </div>
              <div class="publication-drawer-fact">
                <dt>Embed URL</dt>
                <dd><a href=${publication.embedUrl} target="_blank" rel="noreferrer">${publication.embedUrl || 'Not configured'}</a><button class="publication-drawer-copy" type="button" @click=${() => this.copyPublication(publication.embedUrl, 'Embed link copied')}>Copy</button></dd>
              </div>
            </dl>
          </section>
          <section class="publication-drawer-section" aria-label="Publication details">
            <h3>Configuration</h3>
            <dl class="publication-drawer-facts">
              ${publicationFact('Generation', publication.generation || '-')}
              ${publicationFact('Allowed origins', publication.origins.length ? publication.origins.join(', ') : 'Direct view only')}
              ${publicationFact('Configured', publication.configuredAt || '-')}
              ${publicationFact('Suspended', publication.suspendedAt || '-')}
              ${publicationFact('Rotated', publication.rotatedAt || '-')}
            </dl>
          </section>
          <section class="publication-drawer-section" aria-label="Publication actions">
            <h3>Actions</h3>
            <div class="publication-drawer-actions">
              <a href=${publication.publicUrl} target="_blank" rel="noreferrer">Open</a>
              <button type="button" @click=${() => this.copyPublication(publication.iframeSnippet, 'Iframe copied')}>Copy iframe</button>
              ${publication.status === 'suspended'
                ? html`<button type="button" ?disabled=${busy} @click=${() => this.mutatePublication(publication, 'resume')}>Resume</button>`
                : publication.status === 'active'
                  ? html`<button type="button" ?disabled=${busy} @click=${() => this.mutatePublication(publication, 'suspend')}>Suspend</button>`
                  : nothing}
              <button type="button" ?disabled=${busy || publication.status === 'unconfigured'} @click=${() => this.rotatePublication(publication)}>Rotate URL</button>
            </div>
          </section>
          <section class="publication-drawer-section" aria-label="Lifecycle history">
            <h3>Lifecycle history</h3>
            ${publication.history.length
              ? html`<ul class="publication-history">${publication.history.map((event) => html`<li>${event}</li>`)}</ul>`
              : html`<p class="publication-history-empty">No lifecycle events recorded.</p>`}
          </section>
        </div>
      </lv-drawer>
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
      detail: { publication: publication.name, action, expectedRevision: publication.revision },
    }))
  }

  private handleDatastarFetch = (event: Event): void => {
    // Datastar emits this event for every command on the page. Only consume
    // terminal failures while a publication mutation is waiting; unrelated
    // admin surfaces must keep their own state and feedback.
    if (this.publicationBusy) {
      const failure = browserCommandFailure(event, 'Publication update')
      if (failure) {
        this.publicationBusy = ''
        this.publicationMessage = failure.message
      } else if ((event as CustomEvent<{ type?: string }>).detail?.type === 'finished') {
        this.publicationBusy = ''
      }
    }
    if (this.deliveryBusy) {
      const failure = browserCommandFailure(event, 'Delivery rollback')
      if (failure) {
        this.deliveryBusy = false
        this.deliveryMessage = failure.kind === 'conflict' ? 'Rollback target is stale. Reload delivery state before choosing a retained generation.' : failure.message
      } else if ((event as CustomEvent<{ type?: string }>).detail?.type === 'finished') {
        this.deliveryBusy = false
        this.deliveryMessage = 'Rollback request completed. Delivery state has been refreshed.'
      }
    }
  }

  private handleDeliveryTableAction = (event: CustomEvent): void => {
    if (event.detail?.action !== 'rollback' || this.deliveryBusy) return
    const generation = String(event.detail.row?.generationId ?? event.detail.row?.generation ?? '')
    if (!generation) return
    const label = String(event.detail.row?.generation ?? 'this retained generation')
    if (!window.confirm(`Roll back to ${label}? Traffic will switch to this retained serving state.`)) return
    this.deliveryBusy = true
    this.deliveryMessage = ''
    this.dispatchEvent(new CustomEvent('lv-delivery-rollback', { bubbles: true, composed: true, detail: { generation } }))
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

  private renderQueryFilterShortcuts(history: AdminQueryHistorySignal) {
    const statusMenu = history.filterMenus?.find((menu) => menu.id === 'status')
    const statusValues = statusMenu?.options?.map((option) => option.value) ?? []
    const failed = statusValues.filter((value) => queryFailedStatuses.has(value.trim().toLowerCase()))
    const running = statusValues.filter((value) => queryRunningStatuses.has(value.trim().toLowerCase()))
    if (!failed.length && !running.length) return nothing
    return html`
      <div class="query-filter-shortcuts" aria-label="Query history shortcuts">
        <span class="query-time-presets-label">Shortcuts</span>
        ${failed.length ? html`<button type="button" class="query-filter-shortcut" @click=${() => this.setQueryStatusShortcut(failed)}>Failed</button>` : nothing}
        ${running.length ? html`<button type="button" class="query-filter-shortcut" @click=${() => this.setQueryStatusShortcut(running)}>Running</button>` : nothing}
      </div>
    `
  }

  private renderQueryTimePresets() {
    return html`
      <div class="query-time-presets" aria-label="Query history time presets">
        <span class="query-time-presets-label">Time</span>
        <button type="button" class="query-time-preset" @click=${() => this.setQueryTimeRange('hour')}>Last hour</button>
        <button type="button" class="query-time-preset" @click=${() => this.setQueryTimeRange('day')}>Last 24 hours</button>
        <button type="button" class="query-time-preset" @click=${() => this.setQueryTimeRange('week')}>Last 7 days</button>
        <button type="button" class="query-time-preset" @click=${() => this.setQueryTimeRange('today')}>Current date</button>
      </div>
    `
  }

  private renderQueryDateFilters() {
    const filters = this.queryFilters
    return html`
      <div class="query-date-filters" aria-label="Custom query history date range">
        <div class="query-date-filter">
          <label for="query-filter-from">From</label>
          <input id="query-filter-from" type="date" .value=${queryDateInputValue(filters.from)} @change=${(event: Event) => this.setQueryDateFilter('from', (event.currentTarget as HTMLInputElement).value)}>
        </div>
        <div class="query-date-filter">
          <label for="query-filter-to">To</label>
          <input id="query-filter-to" type="date" .value=${queryDateInputValue(filters.to, true)} @change=${(event: Event) => this.setQueryDateFilter('to', (event.currentTarget as HTMLInputElement).value)}>
        </div>
      </div>
    `
  }

  private renderQueryFilterSummary(history: AdminQueryHistorySignal) {
    const filters = this.queryFilters
    const chips = queryFilterSummaryChips(filters, history.filterMenus ?? [])
    return html`
      <div class="query-filter-summary" aria-live="polite">
        <span class="query-filter-summary-label">${chips.length ? 'Active filters' : 'No filters applied'}</span>
        ${chips.map((chip) => html`<span class="query-filter-chip">${chip}</span>`)}
        ${chips.length ? html`<button type="button" class="query-filter-clear" @click=${this.clearQueryFilters}>Clear all</button>` : nothing}
      </div>
    `
  }

  private renderFilterMenu(menu: FilterMenuSignal) {
    return html`<lv-filter-menu .menu=${menu}></lv-filter-menu>`
  }

  private setQueryFilter(key: keyof AdminQueryHistoryFilters, value: string) {
    const filters = { ...this.queryFilters, [key]: value }
    this.queryFilters = filters
    this.cancelQueryFilterTimer()
    this.queryFilterTimer = setTimeout(() => {
      this.emitQueryHistoryCommand('reset', filters, '')
    }, 200)
  }

  private setQueryStatusShortcut(statuses: string[]) {
    this.cancelQueryFilterTimer()
    const filters = { ...this.queryFilters, statuses: [...statuses] }
    this.queryFilters = filters
    this.emitQueryHistoryCommand('reset', filters, '')
  }

  private setQueryTimeRange(range: 'hour' | 'day' | 'week' | 'today') {
    const now = new Date()
    const from = new Date(now)
    if (range === 'hour') from.setTime(now.getTime() - 60 * 60 * 1000)
    if (range === 'day') from.setTime(now.getTime() - 24 * 60 * 60 * 1000)
    if (range === 'week') from.setTime(now.getTime() - 7 * 24 * 60 * 60 * 1000)
    if (range === 'today') {
      from.setUTCHours(0, 0, 0, 0)
      const to = new Date(from)
      to.setUTCDate(to.getUTCDate() + 1)
      this.setQueryDateRange(from, to)
      return
    }
    this.setQueryDateRange(from, now)
  }

  private setQueryDateRange(from: Date, to: Date) {
    this.cancelQueryFilterTimer()
    const filters = { ...this.queryFilters, from: from.toISOString(), to: to.toISOString() }
    this.queryFilters = filters
    this.emitQueryHistoryCommand('reset', filters, '')
  }

  private setQueryDateFilter(key: 'from' | 'to', value: string) {
    this.cancelQueryFilterTimer()
    const filters = { ...this.queryFilters }
    if (!value) {
      delete filters[key]
    } else {
      const date = new Date(`${value}T00:00:00.000Z`)
      if (key === 'to') date.setUTCDate(date.getUTCDate() + 1)
      filters[key] = date.toISOString()
    }
    this.queryFilters = filters
    this.emitQueryHistoryCommand('reset', filters, '')
  }

  private clearQueryFilters = () => {
    this.cancelQueryFilterTimer()
    this.queryFilters = {}
    this.emitQueryHistoryCommand('reset', {}, '')
  }

  private cancelQueryFilterTimer(): void {
    if (!this.queryFilterTimer) return
    clearTimeout(this.queryFilterTimer)
    this.queryFilterTimer = null
  }

  private handleFilterMenuCommand = (event: CustomEvent<FilterMenuCommand>): void => {
    const command = event.detail
    if (!command?.menuId) return
    const action = command.action === 'search' ? 'filter_search' : command.action === 'clear' ? 'filter_clear' : 'filter_toggle'
    const currentFilters = { ...this.currentQueryHistory().filters, ...this.queryFilters }
    const nextFilters = queryFiltersAfterMenuCommand(currentFilters, command)
    if (action !== 'filter_search') this.queryFilters = nextFilters
    // The server applies toggle/clear commands from the menu payload. Send
    // the current selection so a toggle is not applied twice; keep the next
    // selection locally for an immediate active-filter summary.
    this.emitQueryHistoryCommand(action, currentFilters, '', '', command)
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
        <div slot="title" class="query-detail-title">
          <div class=${`query-detail-status query-detail-status-${statusTone}`}>
            ${lucideIcon(queryEventStatusIconComponent(event.status ?? ''), { size: 16, strokeWidth: 2 })}
            <span>${event.loading ? 'Loading' : event.statusLabel || queryEventStatusLabel(event.status ?? '')}</span>
          </div>
          <span class="query-detail-subtitle">${queryDetailSummary(event)}</span>
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

  private retryStorage = (): void => {
    window.location.reload()
  }

}

if (!customElements.get('lv-admin-page')) customElements.define('lv-admin-page', LeapViewAdminPage)
