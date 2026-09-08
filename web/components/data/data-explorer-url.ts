import type { DataExplorerCommand } from '../../generated/signals'
import { canonicalExplorationSpec, explorationSpecFor } from './data-explorer-spec'

export type DataExplorerHistoryMode = 'push' | 'replace'

/** Resolves an explorer path to the absolute URL that should be shared. */
export function absoluteDataExplorerURL(path: string): string {
  if (/^[a-z][a-z\d+.-]*:/i.test(path)) return path
  if (typeof window === 'undefined' || !window.location?.origin) return path
  return new URL(path, window.location.origin).toString()
}

export function dataExplorerURL(command: DataExplorerCommand, savedID?: string, includeArchived = false): string {
  const mode = command.mode === 'explore' ? 'explore' : 'browse'
  const objectKey = command.objectKey || ''
  const params = new URLSearchParams()
  if (savedID?.trim()) params.set('saved', savedID.trim())
  if (includeArchived) params.set('includeArchived', 'true')
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
  const next = dataExplorerURL(command, savedID, includeArchived)
  if (typeof window === 'undefined') return next

  const current = `${window.location.pathname}${window.location.search}`
  if (next === current) return next

  if (mode === 'push') {
    window.history.pushState(window.history.state, '', next)
  } else {
    window.history.replaceState(window.history.state, '', next)
  }
  return next
}
