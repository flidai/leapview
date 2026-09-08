import type { DataExplorerCommand } from '../../generated/signals'
import { canonicalExplorationSpec, explorationSpecFor } from './data-explorer-spec'

export type DataExplorerHistoryMode = 'push' | 'replace'

export type DataExplorerModelSection = 'details' | 'data' | 'definition' | 'refreshes' | 'versions' | 'lineage'

/** Closed, same-origin routes to return to after an explorer handoff. */
export type DataExplorerReturnContext =
  | { surface: 'chat'; conversationId: string }
  | { surface: 'dashboard'; dashboardId: string; pageId: string }
  | { surface: 'model'; asset: string; section: DataExplorerModelSection }

/** Route IDs are opaque identifiers, never paths or URLs. */
export function isValidDataExplorerRouteID(value: string): boolean {
  return /^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$/.test(value)
}

/**
 * Reads only the closed return fields understood by the explorer. Invalid or
 * unknown surfaces are discarded so history updates cannot carry arbitrary
 * query parameters or redirect targets forward.
 */
export function dataExplorerReturnContextFromSearch(search: string): DataExplorerReturnContext | undefined {
  const params = new URLSearchParams(search)
  const surface = singleReturnParam(params, 'returnSurface')
  if (surface === 'chat') {
    const conversationId = singleReturnParam(params, 'returnConversation')
    return conversationId && isValidDataExplorerRouteID(conversationId) ? { surface, conversationId } : undefined
  }
  if (surface === 'dashboard') {
    const dashboardId = singleReturnParam(params, 'returnDashboard')
    const pageId = singleReturnParam(params, 'returnPage')
    return dashboardId && pageId && isValidDataExplorerRouteID(dashboardId) && isValidDataExplorerRouteID(pageId)
      ? { surface, dashboardId, pageId }
      : undefined
  }
  if (surface === 'model') {
    const asset = singleReturnParam(params, 'returnAsset')
    const section = singleReturnParam(params, 'returnSection')
    return asset && section && asset.startsWith('model:') && isValidDataExplorerRouteID(asset) && isDataExplorerModelSection(section)
      ? { surface, asset, section }
      : undefined
  }
  return undefined
}

function singleReturnParam(params: URLSearchParams, key: string): string | undefined {
  const values = params.getAll(key)
  if (values.length !== 1) return undefined
  const value = values[0].trim()
  return value || undefined
}

/** Resolves an explorer path to the absolute URL that should be shared. */
export function absoluteDataExplorerURL(path: string): string {
  if (/^[a-z][a-z\d+.-]*:/i.test(path)) return path
  if (typeof window === 'undefined' || !window.location?.origin) return path
  return new URL(path, window.location.origin).toString()
}

export function dataExplorerURL(command: DataExplorerCommand, savedID?: string, includeArchived = false, returnContext?: DataExplorerReturnContext): string {
  const mode = command.mode === 'explore' ? 'explore' : 'browse'
  const objectKey = command.objectKey || ''
  const params = new URLSearchParams()
  if (savedID?.trim()) params.set('saved', savedID.trim())
  if (includeArchived) params.set('includeArchived', 'true')
  appendDataExplorerReturnContext(params, returnContext)
  if (mode === 'explore') {
    const spec = explorationSpecFor(command.explore)
    if (!spec.modelId?.trim()) return params.toString() ? `/explore?${params.toString()}` : '/explore'
    params.set('v', '2')
    params.set('mode', 'explore')
    params.set('state', JSON.stringify(canonicalExplorationSpec(spec)))
  } else if (objectKey) {
    params.set('object', objectKey)
  }
  return params.toString() ? `/explore?${params.toString()}` : '/explore'
}

function appendDataExplorerReturnContext(params: URLSearchParams, context?: DataExplorerReturnContext): void {
  if (!context) return
  if (context.surface === 'chat') {
    const conversationId = context.conversationId.trim()
    if (!isValidDataExplorerRouteID(conversationId)) return
    params.set('returnSurface', 'chat')
    params.set('returnConversation', conversationId)
    return
  }
  if (context.surface === 'dashboard') {
    const dashboardId = context.dashboardId.trim()
    const pageId = context.pageId.trim()
    if (!isValidDataExplorerRouteID(dashboardId) || !isValidDataExplorerRouteID(pageId)) return
    params.set('returnSurface', 'dashboard')
    params.set('returnDashboard', dashboardId)
    params.set('returnPage', pageId)
    return
  }
  const asset = context.asset.trim()
  const section = context.section.trim()
  if (!asset.startsWith('model:') || !isValidDataExplorerRouteID(asset) || !isDataExplorerModelSection(section)) return
  params.set('returnSurface', 'model')
  params.set('returnAsset', asset)
  params.set('returnSection', section)
}

function isDataExplorerModelSection(value: string): value is DataExplorerModelSection {
  return value === 'details' || value === 'data' || value === 'definition' || value === 'refreshes' || value === 'versions' || value === 'lineage'
}

/** Opens one authorized saved exploration for browser navigation. The server
 * resolves the saved revision before redirecting to its canonical state URL;
 * a saved ID alone is metadata and cannot hydrate another person's query.
 */
export function savedExplorationURL(savedID: string, includeArchived = false): string {
  const id = savedID.trim()
  if (!id) return '/explore'
  const params = new URLSearchParams({ navigation: 'true' })
  if (includeArchived) params.set('includeArchived', 'true')
  return `/explore/saved/${encodeURIComponent(id)}?${params.toString()}`
}

/** Returns a live download link for the canonical authored explorer state. */
export function dataExplorerExportURL(command: DataExplorerCommand, format: 'csv' | 'parquet'): string {
  const current = dataExplorerURL(command)
  const queryStart = current.indexOf('?')
  const query = queryStart >= 0 ? current.slice(queryStart + 1) : ''
  const params = new URLSearchParams(query)
  params.set('format', format)
  return `/explore/export?${params.toString()}`
}

/**
 * Synchronizes durable explorer state without putting transient command fields
 * (request sequence, reset sequence, or table viewport state) in history.
 *
 * The server owns URL decoding and hydration. This helper only changes the
 * browser address bar; a popstate is handled by the page lifecycle below.
 */
export function updateDataExplorerURL(command: DataExplorerCommand, mode: DataExplorerHistoryMode, savedID?: string, includeArchived = false): string {
  if (typeof window === 'undefined') return dataExplorerURL(command, savedID, includeArchived)

  const returnContext = dataExplorerReturnContextFromSearch(window.location.search)
  const next = dataExplorerURL(command, savedID, includeArchived, returnContext)

  const current = `${window.location.pathname}${window.location.search}`
  if (next === current) return next

  if (mode === 'push') {
    window.history.pushState(window.history.state, '', next)
  } else {
    window.history.replaceState(window.history.state, '', next)
  }
  return next
}
