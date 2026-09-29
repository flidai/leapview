import type {
  DashboardComponentSignal, DashboardFilterContract, DashboardFilterExpression,
  DashboardFilterState, DashboardPageSignal, DashboardCompiledFilterBinding,
} from '../../generated/signals'
import type { ExplorationFilter, ExplorationSpec } from '../../generated/exploration'
import { canonicalExplorationSpec } from '../data/data-explorer-spec'

const supportedExpressionKinds = new Set([
  'unfiltered', 'null_check', 'set', 'comparison', 'range', 'relative_period',
])

/**
 * Builds a handoff request for a specific placed visual. The URL contains
 * typed, applied control values only. The server must re-read the authored
 * visual and authorize its model before redirecting to Explorer.
 */
export function dashboardExploreHref(
  page: DashboardPageSignal,
  visual: DashboardComponentSignal,
  contract: DashboardFilterContract,
  filterState: DashboardFilterState,
): string | undefined {
  if (page.presentation !== 'app' || visual.kind !== 'visual' || !visual.visual?.trim()
    || !page.dashboardId?.trim() || !page.pageId?.trim() || !page.modelId?.trim()
    || !page.components.some((component) => component.id === visual.id && component.visual === visual.visual)) return undefined

  const filters: ExplorationFilter[] = []
  const bindingIDs: string[] = []
  for (const binding of Object.values(contract.bindings)) {
    if (!bindingApplies(binding, page, visual.id) || !binding.readerEditable) continue
    const field = contract.definitions[binding.filter]
    const applied = filterState.appliedControls[binding.key]?.resolvedExpression
      ?? filterState.appliedControls[binding.key]?.expression
    if (!field?.field?.trim() || !applied) continue
    if (!supportedExpressionKinds.has(applied.kind) || !binding.id?.trim()) return undefined
    filters.push({
      field: field.field.trim(),
      ...(field.dataset?.trim() ? { datasetId: field.dataset.trim() } : {}),
      expression: cloneExpression(applied),
    })
    bindingIDs.push(binding.id.trim())
  }

  const spec: ExplorationSpec = canonicalExplorationSpec({
    schemaVersion: 1,
    modelId: page.modelId.trim(),
    dimensions: [], metrics: [], filters, sort: [], limit: 1,
  })
  const params = new URLSearchParams()
  params.set('v', '2')
  params.set('mode', 'explore')
  params.set('state', JSON.stringify(spec))
  params.set('returnSurface', 'dashboard')
  params.set('returnDashboard', page.dashboardId)
  params.set('returnPage', page.pageId)
  for (const id of bindingIDs) params.append('filterBinding', id)
  return `/dashboards/${encodeURIComponent(page.dashboardId)}/pages/${encodeURIComponent(page.pageId)}/components/${encodeURIComponent(visual.id)}/explore?${params}`
}

function bindingApplies(binding: DashboardCompiledFilterBinding, page: DashboardPageSignal, componentID: string): boolean {
  if (binding.scope === 'page' && binding.pageID !== page.pageId) return false
  return binding.targets.length === 0 || binding.targets.includes(`${page.pageId}/${componentID}`)
}

function cloneExpression(expression: DashboardFilterExpression): ExplorationFilter['expression'] {
  // These contracts share a discriminator/value wire format. Copy the live
  // signal so URL encoding cannot mutate the dashboard's applied state.
  return JSON.parse(JSON.stringify(expression)) as ExplorationFilter['expression']
}
