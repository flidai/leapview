import { expect, test } from 'bun:test'
import { readPinnedDashboardLinks, syncPinnedDashboardLinks } from './catalog-pins'

test('filtered search results preserve pinned shortcuts until the full catalog returns', () => {
  const values = new Map<string, string>()
  const previousStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage')
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => { values.set(key, value) },
    },
  })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: { dispatchEvent: () => true } })
  try {
    const dashboards = [
      { id: 'sales', dashboardId: 'sales', title: 'Sales', href: '/dashboards/sales' },
      { id: 'operations', dashboardId: 'operations', title: 'Operations', href: '/dashboards/operations' },
    ]
    const pinnedIDs = dashboards.map(dashboard => dashboard.dashboardId)
    syncPinnedDashboardLinks('jacob', pinnedIDs, dashboards)
    syncPinnedDashboardLinks('jacob', pinnedIDs, dashboards.slice(0, 1), true)
    expect(readPinnedDashboardLinks('jacob').map(link => link.id)).toEqual(pinnedIDs)
    syncPinnedDashboardLinks('jacob', pinnedIDs, [], true)
    expect(readPinnedDashboardLinks('jacob').map(link => link.id)).toEqual(pinnedIDs)
    expect(readPinnedDashboardLinks('another-user')).toEqual([])
    syncPinnedDashboardLinks('jacob', pinnedIDs, dashboards.slice(0, 1))
    expect(readPinnedDashboardLinks('jacob').map(link => link.id)).toEqual(['sales'])
  } finally {
    if (previousStorage) Object.defineProperty(globalThis, 'localStorage', previousStorage)
    else Reflect.deleteProperty(globalThis, 'localStorage')
    if (previousWindow) Object.defineProperty(globalThis, 'window', previousWindow)
    else Reflect.deleteProperty(globalThis, 'window')
  }
})
