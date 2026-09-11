import type {
  DashboardComponentSignal,
  DashboardFilterExpression,
  DashboardFilterState,
  DashboardPageSignal,
  DashboardCompiledFilterBinding,
  DashboardFilterContract,
} from '../../generated/signals'
import type { ExplorationFilter, ExplorationSpec } from '../../generated/exploration'
import { canonicalExplorationSpec } from '../data/data-explorer-spec'

const supportedExpressionKinds = new Set([
  'unfiltered', 'null_check', 'set', 'comparison', 'range', 'relative_period',
])

/**
 * Builds the authenticated dashboard handoff URL from the current signal
 * state. The payload contains only typed filter values plus closed authored
 * binding selectors; dimensions, metrics, visual configuration, and source
 * identity remain server-owned authored state. The server reruns the
 * resulting canonical spec under the recipient's current authorization.
 */
export function dashboardExploreHref(
  page: DashboardPageSignal,
  visual: DashboardComponentSignal,
  contract: DashboardFilterContract,
  filterState: DashboardFilterState,
): string | undefined {
  const visualID = visual.visual?.trim()
  if (!visualID || visual.kind !== 'visual' || !page.dashboardId || !page.pageId || !page.modelId) return undefined

  const filters: ExplorationFilter[] = []
  const filterBindingIDs: string[] = []
  for (const binding of Object.values(contract.bindings)) {
    if (!dashboardBindingApplies(binding, page, visual.id)) continue
    // A non-editable authored predicate is part of the visual's fixed query
    // boundary, not a viewer control. Never copy a value for it from the
    // current filter state.
    if (!binding.readerEditable) continue
    const definition = contract.definitions[binding.filter]
    if (!definition?.field?.trim()) continue
    const applied = filterState.appliedControls[binding.key]?.resolvedExpression
      ?? filterState.appliedControls[binding.key]?.expression
    // An absent control is the authored default and needs no URL overlay.
    if (!applied) continue
    if (!supportedExpressionKinds.has(applied.kind)) return undefined
    const bindingID = binding.id?.trim()
    if (!bindingID) return undefined
    filters.push({
      field: definition.field.trim(),
      ...(definition.dataset?.trim() ? { datasetId: definition.dataset.trim() } : {}),
      // Dashboard and exploration contracts intentionally share the same
      // discriminator/value wire representation. Keep this cast at the
      // adapter boundary; server-side shape/model checks remain authoritative.
      expression: cloneExpression(applied),
    })
    // Keep the authored binding identity alongside the canonical filter. The
    // exploration schema intentionally has no dashboard-control IDs; this
    // closed, repeated query parameter lets the authorized server replace an
    // editable default without ever replacing a fixed predicate on the same
    // field. It is only a selector, never an authorization grant.
    filterBindingIDs.push(bindingID)
  }

  const spec: ExplorationSpec = canonicalExplorationSpec({
    schemaVersion: 1,
    modelId: page.modelId.trim(),
    dimensions: [],
    metrics: [],
    filters,
    sort: [],
    limit: 1,
  })
  const params = new URLSearchParams()
  params.set('mode', 'explore')
  params.set('v', '2')
  params.set('state', JSON.stringify(spec))
  params.set('returnSurface', 'dashboard')
  params.set('returnDashboard', page.dashboardId)
  params.set('returnPage', page.pageId)
  for (const bindingID of filterBindingIDs) params.append('filterBinding', bindingID)
  return `/dashboards/${encodeURIComponent(page.dashboardId)}/pages/${encodeURIComponent(page.pageId)}/components/${encodeURIComponent(visual.id)}/explore?${params.toString()}`
}

function dashboardBindingApplies(binding: DashboardCompiledFilterBinding, page: DashboardPageSignal, componentID: string): boolean {
  if (binding.scope === 'page' && binding.pageID !== page.pageId) return false
  if (binding.targets.length === 0) return true
  // Filter targets are compiled page/component identities, not authored
  // visual IDs. A visual can be placed more than once (and on more than one
  // page), so matching by visual ID would leak a control across placements.
  return binding.targets.includes(`${page.pageId}/${componentID}`)
}

function cloneExpression(value: DashboardFilterExpression): ExplorationFilter['expression'] {
  // The generated dashboard/exploration value variants are structurally
  // identical, but deep-copying prevents a live Datastar signal from being
  // mutated while URL encoding is in progress.
  return JSON.parse(JSON.stringify(value)) as ExplorationFilter['expression']
}
