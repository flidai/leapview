export const catalogPinsStorageKey = 'leapview.dashboard-catalog.pins.v1'
export const catalogPinLinksStorageKey = 'leapview.dashboard-catalog.pin-links.v1'
export const catalogPinsChangedEvent = 'leapview-dashboard-pins-change'

export function scopedCatalogPinsStorageKey(principalID: string): string {
  return `${catalogPinsStorageKey}:${principalID}`
}

export function scopedCatalogPinLinksStorageKey(principalID: string): string {
  return `${catalogPinLinksStorageKey}:${principalID}`
}

export type PinnedDashboardLink = { id: string, title: string, href: string, icon?: string }

export function readPinnedDashboardLinks(principalID: string): PinnedDashboardLink[] {
  if (!principalID.trim()) return []
  try {
    const links: unknown = JSON.parse(localStorage.getItem(scopedCatalogPinLinksStorageKey(principalID)) ?? '[]')
    if (!Array.isArray(links)) return []
    return links.filter((link): link is PinnedDashboardLink =>
      Boolean(link && typeof link === 'object' && typeof link.id === 'string' &&
        typeof link.title === 'string' && typeof link.href === 'string' &&
        (link.icon === undefined || typeof link.icon === 'string') &&
        link.id.trim() && link.title.trim() && link.href.startsWith('/') && !link.href.startsWith('//')))
  } catch {
    return []
  }
}

export function syncPinnedDashboardLinks(principalID: string, pinnedIDs: string[], dashboards: (PinnedDashboardLink & { dashboardId: string, appearanceIcon?: string })[], preserveMissing = false): void {
  if (!principalID.trim()) return
  const previous = readPinnedDashboardLinks(principalID)
  const links = pinnedIDs.flatMap(id => {
    const dashboard = dashboards.find(candidate => candidate.dashboardId === id || candidate.id === id)
    if (dashboard) return [{ id: dashboard.dashboardId, title: dashboard.title, href: dashboard.href, icon: dashboard.appearanceIcon || 'layout-dashboard' }]
    const existing = preserveMissing ? previous.find(link => link.id === id || id.endsWith(`:${link.id}`)) : undefined
    return existing ? [existing] : []
  })
  if (JSON.stringify(previous) === JSON.stringify(links)) return
  try {
    localStorage.setItem(scopedCatalogPinLinksStorageKey(principalID), JSON.stringify(links))
  } catch {
    // Pins still work in the catalog when browser storage is unavailable.
  }
  window.dispatchEvent(new Event(catalogPinsChangedEvent))
}

export function dashboardIsPinned(pinnedIDs: string[], dashboard: { id: string, dashboardId: string }): boolean {
  return pinnedIDs.some(id => id === dashboard.dashboardId || id === dashboard.id)
}

export function nextDashboardPins(pinnedIDs: string[], dashboardID: string): string[] {
  const remaining = pinnedIDs.filter(id => id !== dashboardID && !id.endsWith(`:${dashboardID}`))
  return remaining.length === pinnedIDs.length ? [...remaining, dashboardID] : remaining
}
