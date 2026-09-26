import { html, type TemplateResult } from 'lit'

export function renderCatalogPinnedDashboards(table: TemplateResult) {
  return html`
    <section class="pinned-dashboards" aria-labelledby="pinned-dashboards-heading">
      <h2 id="pinned-dashboards-heading">Pinned dashboards</h2>
      ${table}
    </section>
  `
}
