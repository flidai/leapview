import type { AgentContextSignal, DataExplorerCommand } from '../../generated/signals'
import { dataExplorerURL } from '../data/data-explorer-url'

// Only link an exploration that the server has already placed in the authorized
// agent context. Transcript text and visual result rows are not query sources.
export function exploreContextHref(context: AgentContextSignal | null | undefined): string | undefined {
  const spec = context?.exploration
  if (!spec?.modelId?.trim()) return undefined
  return dataExplorerURL({ mode: 'explore', explore: { spec } } as DataExplorerCommand)
}
