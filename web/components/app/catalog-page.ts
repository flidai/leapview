import { LitElement, css, html } from 'lit'
import type { CatalogPageSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import '../shared/entity-list'

class LeapViewCatalogPage extends DatastarLit(LitElement) {
  static styles = [pageHeaderStyles, css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      font-family: var(--lv-font-family-ui, var(--fontStack-system));
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
            columns: {
              semanticModel: dashboard.semanticModel || '—',
              pages: `${dashboard.pageCount} ${dashboard.pageCount === 1 ? 'page' : 'pages'}`,
            },
          }))}
          .columns=${[
            { id: 'name', label: 'Name', width: '52%' },
            { id: 'semanticModel', label: 'Semantic model', width: '28%' },
            { id: 'pages', label: 'Pages', width: '20%', align: 'right' as const },
          ]}
          .filters=${[{ id: 'all', label: 'All' }]}
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
