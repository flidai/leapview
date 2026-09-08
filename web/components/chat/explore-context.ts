import type { AgentContextSignal, DataExplorerCommand } from '../../generated/signals'
import { dataExplorerURL } from '../data/data-explorer-url'

/**
 * Returns a canonical, live explorer link for an already-authorized agent
 * context. It never reconstructs a query from transcript text or tool rows.
 */
export function exploreContextHref(context: AgentContextSignal | null | undefined): string | undefined {
  const spec = context?.exploration
  if (!spec?.modelId?.trim()) return undefined
  const base = dataExplorerURL({ mode: 'explore', explore: { spec } } as DataExplorerCommand)
  if (context?.surface !== 'dashboard' || !context.dashboardId?.trim() || !context.pageId?.trim()) return base
  const queryStart = base.indexOf('?')
  const params = new URLSearchParams(queryStart >= 0 ? base.slice(queryStart + 1) : '')
  params.set('returnSurface', 'dashboard')
  params.set('returnDashboard', context.dashboardId.trim())
  params.set('returnPage', context.pageId.trim())
  return `/explore?${params.toString()}`
}
