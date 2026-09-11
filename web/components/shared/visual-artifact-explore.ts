import type { ExplorationSpec } from '../../generated/exploration'
import type { DataExplorerCommand } from '../../generated/signals'
import { dataExplorerURL } from '../data/data-explorer-url'

/**
 * Builds a live Explorer route from canonical query_visual metadata. The
 * Explorer destination performs the active-viewer authorization again.
 */
export function visualArtifactExploreHref(spec: ExplorationSpec | undefined, conversationId = ''): string | undefined {
  if (!spec?.modelId?.trim()) return undefined
  return dataExplorerURL(
    { mode: 'explore', explore: { spec } } as DataExplorerCommand,
    undefined,
    false,
    conversationId.trim() ? { surface: 'chat', conversationId } : undefined,
  )
}
