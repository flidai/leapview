import { html } from 'lit'
import { Pin, PinOff } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'

type PinnedDashboard = { id: string, dashboardId: string, title: string, href: string }

export function renderCatalogPinnedDashboards(
  dashboards: PinnedDashboard[],
  recordOpen: (id: string) => void,
  unpin: (id: string) => void,
) {
  if (!dashboards.length) return null
  return html`
    <section class="pinned-dashboards" aria-labelledby="pinned-dashboards-heading">
      <h2 id="pinned-dashboards-heading">Pinned dashboards</h2>
      <ul class="pinned-dashboards-list">
        ${dashboards.map(dashboard => html`
          <li class="pinned-dashboard">
            <a href=${dashboard.href} @click=${() => recordOpen(dashboard.id)}>${lucideIcon(Pin, { size: 16 })}<span>${dashboard.title}</span></a>
            <button type="button" aria-label=${`Unpin ${dashboard.title}`} title=${`Unpin ${dashboard.title}`} @click=${() => unpin(dashboard.dashboardId)}>${lucideIcon(PinOff, { size: 16 })}</button>
          </li>
        `)}
      </ul>
    </section>
  `
}
