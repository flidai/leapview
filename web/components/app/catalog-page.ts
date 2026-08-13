import { LitElement, css, html } from 'lit'
import type { CatalogPageSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import '../shared/entity-list'

class LeapViewCatalogPage extends DatastarLit(LitElement) {
  private freshnessTimer: number | undefined

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

  `]

  updated(): void {
    const page = this.page
    if (!page) return
    checkSignalContract('catalog page', page, { kind: 'required', dashboards: 'required' })
  }

  connectedCallback(): void {
    super.connectedCallback()
    this.freshnessTimer = window.setInterval(() => this.requestUpdate(), 60_000)
  }

  disconnectedCallback(): void {
    if (this.freshnessTimer !== undefined) window.clearInterval(this.freshnessTimer)
    this.freshnessTimer = undefined
    super.disconnectedCallback()
  }

  get page(): CatalogPageSignal | null {
    return this.signal<CatalogPageSignal | null>('page', null)
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    return html`
      <section aria-label="LeapView dashboard catalog">
        ${renderPageHeader(page.title)}
        <lv-entity-list
          .items=${page.dashboards.map((dashboard) => ({
            id: dashboard.id,
            title: dashboard.title,
            description: dashboard.description || dashboard.semanticModel || 'Dashboard',
            href: dashboard.href,
            icon: 'dashboard',
            badges: dashboard.popularity ? [{
              icon: 'popularity' as const,
              level: dashboard.popularity,
              label: popularityLabel(dashboard.popularity),
            }] : [],
            columns: {
              workspace: dashboard.workspace,
              popularity: dashboard.popularity ? capitalize(dashboard.popularity) : '—',
              lastRefreshed: formatLastRefreshed(dashboard.lastRefreshedAt),
            },
            sortValues: {
              popularity: popularityRank(dashboard.popularity),
              lastRefreshed: dashboard.lastRefreshedAt || '',
            },
            columnTitles: {
              lastRefreshed: formatExactRefreshedAt(dashboard.lastRefreshedAt),
            },
          }))}
          .columns=${[
            { id: 'name', label: 'Dashboard', width: '48%' },
            { id: 'workspace', label: 'Workspace', width: '22%' },
            { id: 'popularity', label: 'Popularity', width: '12%', render: 'badges' as const },
            { id: 'lastRefreshed', label: 'Last refreshed', width: '18%' },
          ]}
          .filters=${[
            { id: 'all', label: 'All workspaces' },
            ...page.workspaceFilters.map((workspace) => ({ id: workspace.id, label: workspace.title })),
          ]}
          initial-query=${page.listQuery ?? ''}
          active-filter=${page.listFilter ?? 'all'}
          search-placeholder="Search dashboards"
          empty-text="No dashboards are available."
        ></lv-entity-list>
      </section>
    `
  }
}

customElements.define('lv-catalog-page', LeapViewCatalogPage)

function capitalize(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

function popularityLabel(level: 'low' | 'medium' | 'high'): string {
  const percentile = level === 'high' ? 'top 10%' : level === 'medium' ? 'top 20%' : 'top 30%'
  return `${capitalize(level)} popularity — ${percentile} in the last 30 days`
}

function popularityRank(level: 'low' | 'medium' | 'high' | undefined): number {
  return level === 'high' ? 3 : level === 'medium' ? 2 : level === 'low' ? 1 : 0
}

function formatLastRefreshed(value: string | undefined): string {
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

function formatExactRefreshedAt(value: string | undefined): string {
  if (!value) return ''
  const refreshedAt = new Date(value)
  if (Number.isNaN(refreshedAt.getTime())) return ''
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(refreshedAt)
}

function formatRefreshedDate(value: Date): string {
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(value)
}
