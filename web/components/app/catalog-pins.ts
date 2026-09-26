export const catalogPinsStorageKey = 'leapview.dashboard-catalog.pins.v1'

export function dashboardIsPinned(pinnedIDs: string[], dashboard: { id: string, dashboardId: string }): boolean {
  return pinnedIDs.some(id => id === dashboard.dashboardId || id === dashboard.id)
}

export function nextDashboardPins(pinnedIDs: string[], dashboardID: string): string[] {
  const remaining = pinnedIDs.filter(id => id !== dashboardID && !id.endsWith(`:${dashboardID}`))
  return remaining.length === pinnedIDs.length ? [...remaining, dashboardID] : remaining
}
