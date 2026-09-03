import type { DataExplorerCommand } from '../../generated/signals'
import { canonicalExplorationSpec, explorationSpecFor } from './data-explorer-spec'

export type DataExplorerHistoryMode = 'push' | 'replace'

export function dataExplorerURL(command: DataExplorerCommand): string {
  const mode = command.mode === 'explore' ? 'explore' : 'browse'
  const objectKey = command.objectKey || ''
  const params = new URLSearchParams()
  if (mode === 'explore') {
    const spec = explorationSpecFor(command.explore)
    if (!spec.modelId?.trim()) return '/explore'
    params.set('v', '2')
    params.set('mode', 'explore')
    params.set('state', JSON.stringify(canonicalExplorationSpec(spec)))
  } else if (objectKey) {
    params.set('object', objectKey)
  }
  return params.toString() ? `/explore?${params.toString()}` : '/explore'
}

/**
 * Synchronizes durable explorer state without putting transient command fields
 * (request sequence, reset sequence, or table viewport state) in history.
 *
 * The server owns URL decoding and hydration. This helper only changes the
 * browser address bar; a popstate is handled by the page lifecycle below.
 */
export function updateDataExplorerURL(command: DataExplorerCommand, mode: DataExplorerHistoryMode): string {
  const next = dataExplorerURL(command)
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
