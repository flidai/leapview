import type { DataExplorerCommand } from '../../generated/signals'
import { canonicalExplorationSpec, explorationSpecFromCommand } from './data-explorer-spec'

export type DataExplorerHistoryMode = 'push' | 'replace'

// Dashboard handoffs may offer a return affordance, but never turn arbitrary
// URL input into a navigation target. This context is not part of a saved or
// shared exploration specification.
export function dashboardReturnPath(search: string): string {
  const raw = new URLSearchParams(search).get('returnTo') ?? ''
  if (!raw.startsWith('/') || raw.startsWith('//') || raw.includes('\\')) return ''
  try {
    const target = new URL(raw, 'https://leapview.invalid')
    if (target.origin !== 'https://leapview.invalid' || target.search || target.hash) return ''
    return /(?:^|\/)dashboards\/[^/]+\/pages\/[^/]+$/.test(target.pathname) ? target.pathname : ''
  } catch {
    return ''
  }
}

// A saved link goes through the server's authorized reopen route. It must not
// carry the browser's draft state, which may differ from the saved revision.
export function savedExplorationShareURL(id: string, includeArchived = false): string {
  const selected = id.trim()
  if (!selected) return ''
  const archived = includeArchived ? '&includeArchived=true' : ''
  return `/explore/saved/${encodeURIComponent(selected)}?navigation=true${archived}`
}

export function dataExplorerExportURL(command: DataExplorerCommand, format: 'csv' | 'parquet'): string {
  const source = new URL(dataExplorerURL(command), 'https://leapview.invalid')
  if (source.searchParams.get('mode') !== 'explore' || !source.searchParams.has('state')) return ''
  const params = new URLSearchParams()
  params.set('v', '2')
  params.set('mode', 'explore')
  params.set('state', source.searchParams.get('state')!)
  params.set('format', format)
  return `/explore/export?${params.toString()}`
}

export function dataExplorerURL(command: DataExplorerCommand, savedID?: string, includeArchived = false): string {
  const mode = command.mode === 'explore' ? 'explore' : 'browse'
  const objectKey = command.objectKey || ''
  const params = new URLSearchParams()
  if (savedID?.trim()) params.set('saved', savedID.trim())
  if (includeArchived) params.set('includeArchived', 'true')
  if (mode === 'explore') {
    // Authored draft edits update spec before the debounced server response.
    // Compatibility signal members can still describe the previous query.
    const spec = command.explore!.spec ?? explorationSpecFromCommand(command.explore!)
    if (!spec.modelId.trim()) return '/explore'
    params.set('v', '2')
    params.set('mode', 'explore')
    params.set('state', JSON.stringify(canonicalExplorationSpec(spec)))
  } else if (objectKey) {
    params.set('object', objectKey)
    const spec = command.explore?.spec
    if (spec?.filters?.length) {
      params.set('v', '2')
      params.set('mode', 'browse')
      params.set('state', JSON.stringify(canonicalExplorationSpec(spec)))
    }
  }
  return params.toString() ? `/explore?${params.toString()}` : '/explore'
}

export function updateDataExplorerURL(command: DataExplorerCommand, mode: DataExplorerHistoryMode, savedID?: string, includeArchived = false): string {
  const base = dataExplorerURL(command, savedID, includeArchived)
  if (typeof window === 'undefined') return base
  const context = dashboardReturnPath(window.location.search)
  const target = new URL(base, window.location.origin)
  if (context && target.pathname === '/explore') target.searchParams.set('returnTo', context)
  const next = `${target.pathname}${target.search}`
  const current = `${window.location.pathname}${window.location.search}`
  if (next === current) return next
  window.history[mode === 'push' ? 'pushState' : 'replaceState'](window.history.state, '', next)
  return next
}
