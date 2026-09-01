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

class LeapViewCatalogPage extends DatastarLit(LitElement) {
  @property({ attribute: 'create-draft-href' }) createDraftHref = ''
  @property({ attribute: 'create-draft-models' }) createDraftModelsJSON = ''
  @property({ attribute: 'create-draft-csrf-token' }) createDraftCSRFToken = ''
  @property({ attribute: 'create-draft-idempotency-key' }) createDraftIdempotencyKey = ''
  @state() private catalogScope: 'all' | 'mine' | 'shared' = 'all'
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

    @media (max-width: 720px) {
      .catalog-create-dialog { width: calc(100% - var(--base-size-16)); max-height: calc(100svh - var(--base-size-16)); }
      .catalog-create-dialog-header { padding-inline: var(--base-size-16); }
      .catalog-create-dialog-form { padding-inline: var(--base-size-16); }
      .catalog-create-dialog-actions { position: sticky; bottom: 0; margin-inline: calc(var(--base-size-16) * -1); padding: var(--base-size-12) var(--base-size-16); border-top: var(--lv-border-muted); background: var(--lv-bg-panel); }
      .catalog-create-dialog-actions button { flex: 1; }
    }

  `]

  override connectedCallback(): void {
    super.connectedCallback()
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
    const dashboards = page.dashboards.filter((dashboard) => this.catalogScope === 'all' || dashboard.catalogScope === this.catalogScope)
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
            description: dashboard.description || dashboard.semanticModel || 'Dashboard',
            href: dashboard.href,
            icon: 'dashboard',
            badges: dashboard.popularity ? [{
              icon: 'popularity' as const,
              level: dashboard.popularity,
              label: popularityLabel(dashboard.popularity),
            }] : [],
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
          .columns=${[
            { id: 'name', label: 'Dashboard', width: '46%' },
            { id: 'owner', label: 'Owner', width: '16%' },
            { id: 'status', label: 'Status', width: '20%', render: 'status' as const },
            { id: 'updated', label: 'Updated', width: '18%' },
          ]}
          initial-query=${page.listQuery ?? ''}
          active-filter=${page.listFilter ?? 'all'}
          search-placeholder="Search dashboards"
          empty-text=${this.catalogScope === 'all' ? 'No dashboards are available.' : 'No dashboards in this view.'}
        ></lv-entity-list>
        ${this.createDraftHref ? this.renderCreateDraftDialog(models) : ''}
      </section>
    `
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
  const percentile = level === 'high' ? 'top 10%' : level === 'medium' ? 'top 20%' : 'top 30%'
  return `${capitalize(level)} popularity — ${percentile} in the last 30 days`
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
