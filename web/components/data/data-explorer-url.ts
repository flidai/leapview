import type { DataExplorerCommand } from '../../generated/signals'
import { canonicalExplorationSpec, explorationSpecFromCommand } from './data-explorer-spec'

export type DataExplorerHistoryMode = 'push' | 'replace'

export function dataExplorerURL(command: DataExplorerCommand, savedID?: string, includeArchived = false): string {
  const mode = command.mode === 'explore' ? 'explore' : 'browse'
  const objectKey = command.objectKey || ''
  const params = new URLSearchParams()
  if (savedID?.trim()) params.set('saved', savedID.trim())
  if (includeArchived) params.set('includeArchived', 'true')
  if (mode === 'explore') {
    const spec = explorationSpecFromCommand(command.explore!)
    if (!spec.modelId.trim()) return '/explore'
    params.set('v', '2')
    params.set('mode', 'explore')
    params.set('state', JSON.stringify(canonicalExplorationSpec(spec)))
  } else if (objectKey) {
    params.set('object', objectKey)
  }
  return params.toString() ? `/explore?${params.toString()}` : '/explore'
}

export function updateDataExplorerURL(command: DataExplorerCommand, mode: DataExplorerHistoryMode, savedID?: string, includeArchived = false): string {
  const next = dataExplorerURL(command, savedID, includeArchived)
  if (typeof window === 'undefined') return next
  const current = `${window.location.pathname}${window.location.search}`
  if (next === current) return next
  window.history[mode === 'push' ? 'pushState' : 'replaceState'](window.history.state, '', next)
  return next
}
