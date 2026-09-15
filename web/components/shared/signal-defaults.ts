import type { DashboardStatus } from '../../generated/signals'

/** Return a complete status envelope for components before their first patch. */
export function emptyDashboardStatus(): DashboardStatus {
  return {
    loading: false,
    error: '',
    generation: 0,
    lastUpdated: '',
    refreshId: '',
    setupRequired: false,
    progressPercent: 100,
  }
}
