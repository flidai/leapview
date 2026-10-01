import { css, html } from 'lit'
import { PinOff } from 'lucide'
import { readStringList, writeStorage } from '../app/catalog-preferences'
import { scopedCatalogPinsStorageKey, syncPinnedDashboardLinks, type PinnedDashboardLink } from '../app/catalog-pins'
import { lucideIcon } from '../shared/lucide-icons'
import { lucideIconByCanonicalName } from '../shared/lucide-catalog'
import { renderSidebarChatHistoryItem, type SidebarHistoryItem } from './sidebar-chat-history'

export const sidebarPinnedDashboardStyles = css`
  .pinned-dashboard-row > .nav-item { flex: 1; min-width: 0; }
  .pinned-dashboard-row:hover > .nav-item,
  .pinned-dashboard-row:focus-within > .nav-item {
    padding-right: calc(var(--control-small-size) + var(--base-size-8));
    background: transparent;
  }
  @media (hover: none) {
    .pinned-dashboard-row > .nav-item { padding-right: calc(var(--control-small-size) + var(--base-size-8)); }
  }
`

export function unpinSidebarDashboard(principalID: string, dashboardID: string): void {
  if (!principalID) return
  const key = scopedCatalogPinsStorageKey(principalID)
  const pinnedIDs = readStringList(key)
  const remaining = pinnedIDs.filter(id => id !== dashboardID && !id.endsWith(`:${dashboardID}`))
  if (remaining.length === pinnedIDs.length) return
  writeStorage(key, remaining)
  syncPinnedDashboardLinks(principalID, remaining, [], true)
}

export function renderSidebarPinnedDashboard(
  dashboard: PinnedDashboardLink,
  active: boolean,
  followInternalLink: (event: MouseEvent, href: string) => void,
  unpinDashboard: (event: MouseEvent, dashboard: PinnedDashboardLink) => void,
) {
  return html`
    <div class="history-row pinned-dashboard-row">
      <a class="nav-item" href=${dashboard.href} aria-label=${dashboard.title} aria-current=${active ? 'page' : 'false'} title=${dashboard.title} @click=${(event: MouseEvent) => followInternalLink(event, dashboard.href)}>
        <span class="nav-icon">${lucideIcon(lucideIconByCanonicalName(dashboard.icon || 'layout-dashboard'))}</span>
        <span class="nav-text"><strong>${dashboard.title}</strong></span>
      </a>
      <div class="history-actions" aria-label=${`Quick actions for ${dashboard.title}`}>
        <button class="history-action" type="button" aria-label=${`Unpin ${dashboard.title}`} title="Unpin dashboard" @click=${(event: MouseEvent) => unpinDashboard(event, dashboard)}>${lucideIcon(PinOff, { size: 16 })}</button>
      </div>
    </div>
  `
}

export function renderSidebarPinnedItems(
  dashboards: PinnedDashboardLink[],
  chats: SidebarHistoryItem[],
  activeDashboardID: string | undefined,
  followInternalLink: (event: MouseEvent, href: string) => void,
  unpinDashboard: (event: MouseEvent, dashboard: PinnedDashboardLink) => void,
  chatAction: (action: string, item: SidebarHistoryItem) => void,
) {
  if (!dashboards.length && !chats.length) return null
  return html`
    <section class="nav-group pinned-items" aria-label="Pinned">
      <strong class="nav-group-label" role="heading" aria-level="2">Pinned</strong>
      ${dashboards.map(dashboard => renderSidebarPinnedDashboard(dashboard, activeDashboardID === dashboard.id, followInternalLink, unpinDashboard))}
      ${chats.map(chat => renderSidebarChatHistoryItem(chat, followInternalLink, chatAction))}
    </section>
  `
}
