import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { CatalogPageSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import '../shared/entity-list'
import { lucideIconByCanonicalName } from '../shared/lucide-catalog'
import { lucideIcon } from '../shared/lucide-icons'

interface CreateDraftModel {
  id: string
  title: string
}

type CatalogDashboard = CatalogPageSignal['dashboards'][number] & { featured?: boolean }
type CatalogSort = 'recommended' | 'recent' | 'popular' | 'updated' | 'name'

const catalogFavoritesStorageKey = 'leapview.dashboard-catalog.favorites.v1'
const catalogRecentsStorageKey = 'leapview.dashboard-catalog.recents.v1'

class LeapViewCatalogPage extends DatastarLit(LitElement) {
  @property({ attribute: 'create-draft-href' }) createDraftHref = ''
  @property({ attribute: 'create-draft-models' }) createDraftModelsJSON = ''
  @property({ attribute: 'create-draft-csrf-token' }) createDraftCSRFToken = ''
  @property({ attribute: 'create-draft-idempotency-key' }) createDraftIdempotencyKey = ''
  @state() private catalogScope: 'all' | 'mine' | 'shared' = 'all'
  @state() private catalogSort: CatalogSort = 'recommended'
  @state() private catalogModel = 'all'
  @state() private catalogStatus = 'all'
  @state() private catalogFavoritesOnly = false
  @state() private catalogFeaturedOnly = false
  @state() private favoriteDashboardIDs: string[] = []
  @state() private recentDashboardIDs: Record<string, string> = {}
  @state() private createDraftOpen = false
  private freshnessTimer: number | undefined
  private autoOpenChecked = false
  private createDraftTrigger: HTMLAnchorElement | null = null
  static styles = [pageHeaderStyles, css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    :host > section {
      display: grid;
      width: min(100%, var(--lv-page-content-max-width));
      min-width: 0;
      min-height: 100svh;
      align-content: start;
      gap: var(--base-size-16);
      box-sizing: border-box;
      margin-inline: auto;
      padding: var(--base-size-24);
    }

    @media (max-width: 720px) {
      :host > section {
        padding: var(--base-size-12);
      }
    }

    .catalog-create-draft { display: inline-flex; align-items: center; min-height: var(--control-medium-size); padding: 0 var(--base-size-12); border: var(--lv-border-default); border-radius: var(--lv-radius-default); color: var(--lv-button-fg-rest); background: var(--lv-button-bg-rest); cursor: pointer; font: var(--lv-type-body-compact); }
    .catalog-create-draft:hover { background: var(--lv-button-bg-hover, var(--lv-bg-control-hover)); }
    .catalog-create-draft:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }

    .catalog-create-dialog { box-sizing: border-box; width: min(calc(100% - var(--base-size-32)), 32rem); max-width: 32rem; max-height: calc(100svh - var(--base-size-32)); margin: auto; padding: 0; overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-panel, var(--lv-radius-default)); color: var(--lv-fg-default); background: var(--lv-bg-panel); box-shadow: var(--lv-shadow-floating-lg); }
    .catalog-create-dialog::backdrop { background: var(--lv-modal-backdrop); }
    .catalog-create-dialog-shell { display: grid; width: 100%; min-width: 0; max-height: inherit; overflow-x: hidden; overflow-y: auto; }
    .catalog-create-dialog-header { display: flex; min-width: 0; align-items: flex-start; justify-content: space-between; gap: var(--base-size-16); box-sizing: border-box; padding: var(--base-size-20) var(--base-size-24) var(--base-size-12); }
    .catalog-create-dialog-header > div { min-width: 0; }
    .catalog-create-dialog-header h2 { margin: 0; color: var(--lv-fg-default); font: var(--lv-type-section-title); }
    .catalog-create-dialog-header p { margin: var(--base-size-4) 0 0; color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .catalog-create-dialog-close { display: inline-grid; width: var(--control-medium-size); height: var(--control-medium-size); flex: 0 0 auto; place-items: center; border: var(--lv-border-default); border-radius: var(--lv-radius-default); color: var(--lv-fg-default); background: transparent; cursor: pointer; }
    .catalog-create-dialog-close svg { display: block; }
    .catalog-create-dialog-close:hover { background: var(--lv-bg-control-hover); }
    .catalog-create-dialog-close:focus-visible,
    .catalog-create-dialog button:focus-visible,
    .catalog-create-dialog input:focus-visible,
    .catalog-create-dialog select:focus-visible,
    .catalog-create-dialog summary:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .catalog-create-dialog-form { display: grid; min-width: 0; gap: var(--base-size-16); box-sizing: border-box; padding: var(--base-size-12) var(--base-size-24) var(--base-size-24); }
    .catalog-create-field { display: grid; min-width: 0; gap: var(--base-size-6); }
    .catalog-create-field label,
    .catalog-create-advanced summary { color: var(--lv-fg-default); font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); }
    .catalog-create-field input,
    .catalog-create-field select,
    .catalog-create-advanced-content input { box-sizing: border-box; width: 100%; max-width: 100%; min-height: var(--control-medium-size); border: var(--lv-border-default); border-radius: var(--lv-radius-default); color: var(--lv-fg-default); background: var(--lv-bg-control); padding: 0 var(--base-size-8); font: var(--lv-type-body-compact); }
    .catalog-create-field select:disabled { color: var(--lv-fg-muted); background: var(--lv-bg-control-hover); cursor: not-allowed; }
    .catalog-create-advanced { min-width: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); padding: var(--base-size-8) var(--base-size-12); }
    .catalog-create-advanced summary { cursor: pointer; }
    .catalog-create-advanced-content { display: grid; gap: var(--base-size-6); padding-top: var(--base-size-12); }
    .catalog-create-help { margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .catalog-create-no-models { color: var(--lv-fg-danger, var(--lv-fg-muted)); }
    .catalog-create-dialog-actions { display: flex; align-items: center; justify-content: flex-end; gap: var(--base-size-8); padding-top: var(--base-size-4); }
    .catalog-create-dialog-actions button { min-height: var(--control-medium-size); padding: 0 var(--base-size-12); border: var(--lv-border-default); border-radius: var(--lv-radius-default); color: var(--lv-fg-default); background: var(--lv-bg-control); cursor: pointer; font: var(--lv-type-body-compact); }
    .catalog-create-dialog-actions button[type='submit'] { border-color: var(--lv-button-accent-border-rest, var(--lv-line-accent)); color: var(--lv-button-accent-fg-rest, var(--lv-fg-on-emphasis)); background: var(--lv-button-accent-bg-rest, var(--lv-line-accent)); }
    .catalog-create-dialog-actions button:hover { background: var(--lv-bg-control-hover); }
    .catalog-create-dialog-actions button[type='submit']:hover { background: var(--lv-button-accent-bg-hover, var(--lv-line-accent)); }
    .catalog-create-dialog-actions button:disabled { cursor: not-allowed; opacity: .6; }

    .catalog-tabs { display: flex; align-items: center; gap: var(--base-size-4); border-bottom: var(--lv-border-muted); }
    .catalog-tab { position: relative; min-height: var(--control-medium-size); border: 0; color: var(--lv-fg-muted); background: transparent; padding: 0 var(--base-size-12); cursor: pointer; font: var(--lv-type-body-compact); }
    .catalog-tab:hover { color: var(--lv-fg-default); }
    .catalog-tab[aria-selected='true'] { color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }
    .catalog-tab[aria-selected='true']::after { position: absolute; right: var(--base-size-8); bottom: -1px; left: var(--base-size-8); height: 2px; border-radius: var(--lv-radius-full); background: var(--lv-fg-accent); content: ''; }
    .catalog-tab:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }

    .catalog-discovery-control {
      box-sizing: border-box;
      min-height: var(--control-medium-size);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      background: var(--lv-bg-panel);
      font: var(--lv-type-body-compact);
    }

    select.catalog-discovery-control { min-width: 9.25rem; padding: 0 var(--base-size-8); }
    .catalog-discovery-control:focus-visible,
    .catalog-filter select:focus-visible,
    .catalog-filter input:focus-visible,
    .catalog-filter button:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }

    .catalog-filter { position: relative; }
    .catalog-filter summary {
      display: inline-flex;
      min-width: 5.5rem;
      align-items: center;
      justify-content: center;
      gap: var(--base-size-6);
      padding: 0 var(--base-size-8);
      cursor: pointer;
      list-style: none;
      user-select: none;
    }
    .catalog-filter summary::-webkit-details-marker { display: none; }
    .catalog-filter[open] summary { background: var(--lv-bg-control-hover); }
    .catalog-filter-count {
      display: inline-grid;
      min-width: var(--base-size-16);
      height: var(--base-size-16);
      place-items: center;
      border-radius: var(--lv-radius-full);
      color: var(--lv-fg-on-emphasis);
      background: var(--lv-fg-accent);
      padding-inline: 2px;
      font: var(--lv-type-caption);
    }
    .catalog-filter-popover {
      position: absolute;
      z-index: 10;
      top: calc(100% + var(--base-size-6));
      right: 0;
      display: grid;
      width: min(20rem, calc(100vw - var(--base-size-32)));
      gap: var(--base-size-12);
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-panel, var(--lv-radius-default));
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-lg);
      padding: var(--base-size-16);
    }
    .catalog-filter-field { display: grid; gap: var(--base-size-6); color: var(--lv-fg-default); font: var(--lv-type-body-compact); }
    .catalog-filter-field > span { font-weight: var(--base-text-weight-semibold); }
    .catalog-filter-field select { box-sizing: border-box; width: 100%; min-height: var(--control-medium-size); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); color: var(--lv-fg-default); background: var(--lv-bg-control); padding: 0 var(--base-size-8); font: inherit; }
    .catalog-filter-check { display: flex; align-items: center; gap: var(--base-size-8); color: var(--lv-fg-default); font: var(--lv-type-body-compact); }
    .catalog-filter-actions { display: flex; justify-content: flex-end; border-top: var(--lv-border-muted); padding-top: var(--base-size-12); }
    .catalog-filter-actions button { border: 0; color: var(--lv-fg-link); background: transparent; padding: var(--base-size-4); cursor: pointer; font: var(--lv-type-body-compact); }

    @media (max-width: 720px) {
      .catalog-create-dialog { width: calc(100% - var(--base-size-16)); max-height: calc(100svh - var(--base-size-16)); }
      .catalog-create-dialog-header { padding-inline: var(--base-size-16); }
      .catalog-create-dialog-form { padding-inline: var(--base-size-16); }
      .catalog-create-dialog-actions { position: sticky; bottom: 0; margin-inline: calc(var(--base-size-16) * -1); padding: var(--base-size-12) var(--base-size-16); border-top: var(--lv-border-muted); background: var(--lv-bg-panel); }
      .catalog-create-dialog-actions button { flex: 1; }
      .catalog-filter { flex: 1; }
      .catalog-filter summary { width: 100%; }
      select.catalog-discovery-control { flex: 1; min-width: 0; }
    }

  `]

  override connectedCallback(): void {
    super.connectedCallback()
	this.reloadDiscoveryPreferences()
	this.freshnessTimer = window.setInterval(() => this.requestUpdate(), 60_000)
  }

  override disconnectedCallback(): void {
	if (this.freshnessTimer !== undefined) window.clearInterval(this.freshnessTimer)
	this.freshnessTimer = undefined
    super.disconnectedCallback()
  }

  updated(): void {
    const page = this.page
    if (page) checkSignalContract('catalog page', page, { kind: 'required', dashboards: 'required' })

    if (!this.autoOpenChecked && this.createDraftHref) {
      this.autoOpenChecked = true
      if (this.urlCreateDraftRequested()) this.createDraftOpen = true
    }

    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('#catalog-create-draft-dialog')
    if (this.createDraftOpen && dialog && !dialog.open) {
      dialog.showModal()
      window.setTimeout(() => {
        const target = dialog.querySelector<HTMLElement>('[autofocus]') ?? dialog.querySelector<HTMLElement>('input:not([type="hidden"]), select, button')
        target?.focus({ preventScroll: true })
      }, 0)
    } else if (!this.createDraftOpen && dialog?.open) {
      dialog.close()
    }
  }

  get page(): CatalogPageSignal | null {
    return this.signal<CatalogPageSignal | null>('page', null)
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    const sourceDashboards = page.dashboards as CatalogDashboard[]
    const dashboards = this.visibleDashboards(sourceDashboards)
    const models = this.createDraftModels()
    return html`
      <section aria-label="LeapView dashboard catalog">
        ${renderPageHeader(page.title, '', '', this.createDraftHref ? html`<a class="catalog-create-draft" href=${this.createDraftHref} aria-haspopup="dialog" aria-controls="catalog-create-draft-dialog" @click=${this.handleCreateDraftTrigger}>New dashboard</a>` : undefined)}
        <nav class="catalog-tabs" aria-label="Dashboard views" role="tablist">
          ${this.renderCatalogTab('all', 'All dashboards')}
          ${this.renderCatalogTab('mine', 'My dashboards')}
          ${this.renderCatalogTab('shared', 'Shared with me')}
        </nav>
        <lv-entity-list
          .items=${dashboards.map((dashboard) => {
            const appearance = { icon: dashboard.appearanceIcon || 'layout-dashboard', color: dashboard.appearanceColor || 'purple' }
            return ({
            id: dashboard.id,
            dashboardId: dashboard.dashboardId,
            title: dashboard.title,
            description: dashboard.description || 'Dashboard',
            meta: semanticModelLabel(dashboard.semanticModel),
            href: dashboard.href,
            icon: 'dashboard',
            favorite: this.favoriteDashboardIDs.includes(dashboard.id),
            favoriteLabel: this.favoriteDashboardIDs.includes(dashboard.id) ? `Remove ${dashboard.title} from favorites` : `Add ${dashboard.title} to favorites`,
            badges: [
              ...(dashboard.featured ? [{ icon: 'featured' as const, label: 'Featured dashboard', text: 'Featured' }] : []),
              ...(dashboard.popularity ? [{
                icon: 'popularity' as const,
                level: dashboard.popularity,
                label: popularityLabel(dashboard.popularity),
                text: popularityPercentile(dashboard.popularity),
              }] : []),
            ],
            iconNode: lucideIconByCanonicalName(appearance.icon),
            iconColor: appearance.color,
            iconTreatment: 'framed' as const,
            columns: {
              owner: dashboard.owner || '—',
              status: dashboardStatusLabel(dashboard.status),
              updated: formatRelativeTime(dashboard.updatedAt || dashboard.lastRefreshedAt),
            },
            sortValues: {
              owner: dashboard.owner || '',
              status: dashboardStatusRank(dashboard.status),
              updated: dashboard.updatedAt || dashboard.lastRefreshedAt || '',
            },
            columnTitles: {
              updated: formatExactTime(dashboard.updatedAt || dashboard.lastRefreshedAt),
            },
          })})}
          .toolbarTrailing=${this.renderDiscoveryToolbar(sourceDashboards)}
          .columns=${[
            { id: 'name', label: 'Dashboard', width: '46%' },
            { id: 'owner', label: 'Owner', width: '16%' },
            { id: 'status', label: 'Status', width: '20%', render: 'status' as const },
            { id: 'updated', label: 'Updated', width: '18%' },
          ]}
          initial-query=${page.listQuery ?? ''}
          active-filter=${page.listFilter ?? 'all'}
          search-placeholder="Search dashboards"
          empty-text=${this.activeCatalogFilterCount() ? 'No dashboards match these filters.' : this.catalogScope === 'all' ? 'No dashboards are available.' : 'No dashboards in this view.'}
          @lv-entity-list-favorite-toggle=${this.toggleDashboardFavorite}
          @lv-entity-list-item-activate=${this.recordDashboardOpen}
        ></lv-entity-list>
        ${this.createDraftHref ? this.renderCreateDraftDialog(models) : ''}
      </section>
    `
  }

  private renderDiscoveryToolbar(dashboards: CatalogDashboard[]) {
    const activeFilters = this.activeCatalogFilterCount()
    const models = Array.from(new Set(dashboards.map((dashboard) => dashboard.semanticModel?.trim()).filter((model): model is string => Boolean(model))))
      .sort((left, right) => semanticModelLabel(left).localeCompare(semanticModelLabel(right)))
    const hasFeatured = dashboards.some((dashboard) => dashboard.featured)
    return html`
      <details class="catalog-filter">
        <summary class="catalog-discovery-control" aria-label="Filter dashboards">
          <span>Filter</span>
          ${activeFilters ? html`<span class="catalog-filter-count" aria-label=${`${activeFilters} active filters`}>${activeFilters}</span>` : ''}
        </summary>
        <div class="catalog-filter-popover">
          <label class="catalog-filter-field">
            <span>Data model</span>
            <select aria-label="Filter by data model" .value=${this.catalogModel} @change=${this.changeCatalogModel}>
              <option value="all">All data models</option>
              ${models.map((model) => html`<option value=${model}>${semanticModelTitle(model)}</option>`)}
            </select>
          </label>
          <label class="catalog-filter-field">
            <span>Status</span>
            <select aria-label="Filter by status" .value=${this.catalogStatus} @change=${this.changeCatalogStatus}>
              <option value="all">All statuses</option>
              <option value="published">Published</option>
              <option value="private_draft">Private drafts</option>
              <option value="unpublished_changes">Unpublished changes</option>
            </select>
          </label>
          <label class="catalog-filter-check"><input type="checkbox" .checked=${this.catalogFavoritesOnly} @change=${this.changeCatalogFavoritesOnly}> Favorites only</label>
          ${hasFeatured ? html`<label class="catalog-filter-check"><input type="checkbox" .checked=${this.catalogFeaturedOnly} @change=${this.changeCatalogFeaturedOnly}> Featured only</label>` : ''}
          ${activeFilters ? html`<div class="catalog-filter-actions"><button type="button" @click=${this.clearCatalogFilters}>Clear filters</button></div>` : ''}
        </div>
      </details>
      <select class="catalog-discovery-control" aria-label="Sort dashboards" .value=${this.catalogSort} @change=${this.changeCatalogSort}>
        <option value="recommended">Recommended</option>
        <option value="recent">Recently viewed</option>
        <option value="popular">Most popular</option>
        <option value="updated">Recently updated</option>
        <option value="name">Name</option>
      </select>
    `
  }

  private visibleDashboards(dashboards: CatalogDashboard[]): CatalogDashboard[] {
    const filtered = dashboards.filter((dashboard) => {
      if (this.catalogScope !== 'all' && dashboard.catalogScope !== this.catalogScope) return false
      if (this.catalogModel !== 'all' && dashboard.semanticModel !== this.catalogModel) return false
      if (this.catalogStatus !== 'all' && dashboard.status !== this.catalogStatus) return false
      if (this.catalogFavoritesOnly && !this.favoriteDashboardIDs.includes(dashboard.id)) return false
      if (this.catalogFeaturedOnly && !dashboard.featured) return false
      return true
    })
    return filtered
      .map((dashboard, index) => ({ dashboard, index }))
      .sort((left, right) => this.compareDashboards(left.dashboard, right.dashboard) || left.index - right.index)
      .map(({ dashboard }) => dashboard)
  }

  private compareDashboards(left: CatalogDashboard, right: CatalogDashboard): number {
    const name = () => left.title.localeCompare(right.title, undefined, { numeric: true, sensitivity: 'base' })
    const updated = () => timestamp(right.updatedAt || right.lastRefreshedAt) - timestamp(left.updatedAt || left.lastRefreshedAt)
    const recent = () => timestamp(this.recentDashboardIDs[right.id]) - timestamp(this.recentDashboardIDs[left.id])
    const popularity = () => popularityRank(right.popularity) - popularityRank(left.popularity)
    const recommended = () =>
      Number(this.favoriteDashboardIDs.includes(right.id)) - Number(this.favoriteDashboardIDs.includes(left.id)) ||
      Number(Boolean(right.featured)) - Number(Boolean(left.featured)) || popularity() || recent() || updated() || name()
    switch (this.catalogSort) {
      case 'recent': return recent() || recommended()
      case 'popular': return popularity() || recommended()
      case 'updated': return updated() || name()
      case 'name': return name()
      default: return recommended()
    }
  }

  reloadDiscoveryPreferences(): void {
    this.favoriteDashboardIDs = readStringList(catalogFavoritesStorageKey)
    this.recentDashboardIDs = readStringRecord(catalogRecentsStorageKey)
  }

  private toggleDashboardFavorite = (event: CustomEvent<{ item?: { id?: string } }>): void => {
    const id = event.detail?.item?.id?.trim()
    if (!id) return
    const favorites = new Set(this.favoriteDashboardIDs)
    if (favorites.has(id)) favorites.delete(id)
    else favorites.add(id)
    this.favoriteDashboardIDs = Array.from(favorites)
    writeStorage(catalogFavoritesStorageKey, this.favoriteDashboardIDs)
  }

  private recordDashboardOpen = (event: CustomEvent<{ item?: { id?: string } }>): void => {
    const id = event.detail?.item?.id?.trim()
    if (!id) return
    this.recentDashboardIDs = { ...this.recentDashboardIDs, [id]: new Date().toISOString() }
    writeStorage(catalogRecentsStorageKey, this.recentDashboardIDs)
  }

  private activeCatalogFilterCount(): number {
    return Number(this.catalogModel !== 'all') + Number(this.catalogStatus !== 'all') + Number(this.catalogFavoritesOnly) + Number(this.catalogFeaturedOnly)
  }

  private changeCatalogSort = (event: Event): void => { this.catalogSort = (event.currentTarget as HTMLSelectElement).value as CatalogSort }
  private changeCatalogModel = (event: Event): void => { this.catalogModel = (event.currentTarget as HTMLSelectElement).value }
  private changeCatalogStatus = (event: Event): void => { this.catalogStatus = (event.currentTarget as HTMLSelectElement).value }
  private changeCatalogFavoritesOnly = (event: Event): void => { this.catalogFavoritesOnly = (event.currentTarget as HTMLInputElement).checked }
  private changeCatalogFeaturedOnly = (event: Event): void => { this.catalogFeaturedOnly = (event.currentTarget as HTMLInputElement).checked }
  private clearCatalogFilters = (): void => {
    this.catalogModel = 'all'
    this.catalogStatus = 'all'
    this.catalogFavoritesOnly = false
    this.catalogFeaturedOnly = false
  }

  private renderCreateDraftDialog(models: CreateDraftModel[]) {
    const selectedModel = this.selectedSemanticModel()
    const hasModels = models.length > 0
    return html`
      <dialog id="catalog-create-draft-dialog" class="catalog-create-dialog" aria-labelledby="catalog-create-draft-title" aria-describedby="catalog-create-draft-description" @cancel=${this.cancelCreateDraft} @click=${this.closeCreateDraftOnBackdrop}>
        <div class="catalog-create-dialog-shell">
          <header class="catalog-create-dialog-header">
            <div>
              <h2 id="catalog-create-draft-title">New dashboard</h2>
              <p id="catalog-create-draft-description">Private until you publish.</p>
            </div>
            <button class="catalog-create-dialog-close" type="button" aria-label="Close new dashboard" @click=${this.closeCreateDraft}>${lucideIcon(lucideIconByCanonicalName('x'), { size: 16, strokeWidth: 2 })}</button>
          </header>
          <form class="catalog-create-dialog-form" method="post" action=${this.createDraftHref}>
            <div class="catalog-create-field">
              <label for="catalog-create-draft-name">Dashboard name</label>
              <input id="catalog-create-draft-name" name="title" type="text" autocomplete="off" required autofocus placeholder="Sales overview">
            </div>
            <div class="catalog-create-field">
              <label for="catalog-create-draft-model">Data model</label>
              <select id="catalog-create-draft-model" name="semanticModel" required ?disabled=${!hasModels} aria-describedby=${hasModels ? undefined : 'catalog-create-draft-model-help'}>
                ${hasModels ? html`
                  <option value="" disabled ?selected=${!models.some((model) => model.id === selectedModel)}>Select a data model</option>
                  ${models.map((model) => html`<option value=${model.id} ?selected=${model.id === selectedModel}>${model.title}</option>`)}
                ` : html`<option value="" selected>No data models available</option>`}
              </select>
              ${hasModels ? '' : html`<p id="catalog-create-draft-model-help" class="catalog-create-help catalog-create-no-models" role="status">No data models are available. Add one in Develop, then try again.</p>`}
            </div>
            <details class="catalog-create-advanced">
              <summary>Advanced settings</summary>
              <div class="catalog-create-advanced-content">
                <label for="catalog-create-draft-slug">URL slug (optional)</label>
                <input id="catalog-create-draft-slug" name="slug" type="text" autocomplete="off" placeholder="sales-overview">
              </div>
            </details>
            <input type="hidden" name="gorilla.csrf.Token" value=${this.createDraftCSRFToken}>
            <input type="hidden" name="idempotencyKey" value=${this.createDraftIdempotencyKey}>
            <div class="catalog-create-dialog-actions">
              <button type="button" @click=${this.closeCreateDraft}>Cancel</button>
              <button type="submit" ?disabled=${!hasModels}>Create dashboard</button>
            </div>
          </form>
        </div>
      </dialog>
    `
  }

  private createDraftModels(): CreateDraftModel[] {
    let value: unknown
    try {
      value = JSON.parse(this.createDraftModelsJSON)
    } catch {
      return []
    }
    if (!Array.isArray(value)) return []
    return value.flatMap((entry) => {
      if (!entry || typeof entry !== 'object') return []
      const id = typeof (entry as { id?: unknown }).id === 'string' ? (entry as { id: string }).id.trim() : ''
      if (!id) return []
      const rawTitle = typeof (entry as { title?: unknown }).title === 'string' ? (entry as { title: string }).title.trim() : ''
      return [{ id, title: humanizeCreateDraftModelTitle(rawTitle || id) }]
    })
  }

  private selectedSemanticModel(): string {
    return new URL(window.location.href).searchParams.get('semanticModel')?.trim() ?? ''
  }

  private urlCreateDraftRequested(): boolean {
    return new URL(window.location.href).searchParams.get('create') === 'dashboard'
  }

  private handleCreateDraftTrigger = (event: MouseEvent): void => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    const trigger = event.currentTarget
    this.createDraftTrigger = trigger instanceof HTMLAnchorElement ? trigger : null
    this.createDraftOpen = true
  }

  private closeCreateDraft = (): void => {
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('#catalog-create-draft-dialog')
    if (dialog?.open) dialog.close()
    this.createDraftOpen = false
    const trigger = this.createDraftTrigger
    this.createDraftTrigger = null
    queueMicrotask(() => trigger?.focus({ preventScroll: true }))
  }

  private cancelCreateDraft = (event: Event): void => {
    event.preventDefault()
    this.closeCreateDraft()
  }

  private closeCreateDraftOnBackdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.closeCreateDraft()
  }

  private renderCatalogTab(scope: 'all' | 'mine' | 'shared', label: string) {
    return html`<button class="catalog-tab" type="button" role="tab" aria-selected=${String(this.catalogScope === scope)} @click=${() => { this.catalogScope = scope }}>${label}</button>`
  }
}

customElements.define('lv-catalog-page', LeapViewCatalogPage)

function capitalize(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

function humanizeCreateDraftModelTitle(value: string): string {
  if (!value || value !== value.toLowerCase()) return value
  return value
    .replaceAll('_', ' ')
    .replaceAll('-', ' ')
    .split(/\s+/)
    .filter(Boolean)
    .map((word) => capitalize(word))
    .join(' ')
}

function popularityLabel(level: 'low' | 'medium' | 'high'): string {
  return `${capitalize(level)} popularity — ${popularityPercentile(level).toLowerCase()} in the last 30 days`
}

function popularityPercentile(level: 'low' | 'medium' | 'high'): string {
  return level === 'high' ? 'Top 10%' : level === 'medium' ? 'Top 20%' : 'Top 30%'
}

function popularityRank(level: CatalogDashboard['popularity']): number {
  return level === 'high' ? 3 : level === 'medium' ? 2 : level === 'low' ? 1 : 0
}

function semanticModelTitle(value: string | undefined): string {
  if (!value) return 'Unknown'
  const segment = value.split(':').pop() ?? value
  return humanizeCreateDraftModelTitle(segment)
}

function semanticModelLabel(value: string | undefined): string {
  return `${semanticModelTitle(value)} model`
}

function timestamp(value: string | undefined): number {
  if (!value) return 0
  const result = new Date(value).getTime()
  return Number.isNaN(result) ? 0 : result
}

function readStringList(key: string): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key) ?? '[]')
    return Array.isArray(value) ? value.filter((entry): entry is string => typeof entry === 'string' && Boolean(entry.trim())) : []
  } catch {
    return []
  }
}

function readStringRecord(key: string): Record<string, string> {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(key) ?? '{}')
    if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
    return Object.fromEntries(Object.entries(value).filter((entry): entry is [string, string] => typeof entry[1] === 'string'))
  } catch {
    return {}
  }
}

function writeStorage(key: string, value: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // Discovery preferences are a progressive enhancement when storage is unavailable.
  }
}

function formatRelativeTime(value: string | undefined): string {
  if (!value) return '—'
  const refreshedAt = new Date(value)
  if (Number.isNaN(refreshedAt.getTime())) return '—'
  const elapsed = Date.now() - refreshedAt.getTime()
  if (elapsed < 0) return formatRefreshedDate(refreshedAt)
  const minutes = Math.floor(elapsed / 60_000)
  if (minutes < 1) return 'Just now'
  if (minutes < 60) return `${minutes} min ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours} hr ago`
  if (hours < 48) return 'Yesterday'
  const days = Math.floor(hours / 24)
  if (days < 7) return `${days} days ago`
  return formatRefreshedDate(refreshedAt)
}

function formatExactTime(value: string | undefined): string {
  if (!value) return ''
  const refreshedAt = new Date(value)
  if (Number.isNaN(refreshedAt.getTime())) return ''
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(refreshedAt)
}

function dashboardStatusLabel(value: CatalogPageSignal['dashboards'][number]['status']): string {
  switch (value) {
    case 'private_draft': return 'Private draft'
    case 'unpublished_changes': return 'Unpublished changes'
    default: return 'Published'
  }
}

function dashboardStatusRank(value: CatalogPageSignal['dashboards'][number]['status']): number {
  switch (value) {
    case 'unpublished_changes': return 3
    case 'private_draft': return 2
    default: return 1
  }
}

function formatRefreshedDate(value: Date): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(value)
}
