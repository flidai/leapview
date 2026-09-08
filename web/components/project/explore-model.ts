import type { DataExplorerCommand, ResourceAssetSummarySignal } from '../../generated/signals'
import { dataExplorerURL } from '../data/data-explorer-url'

/**
 * Builds the model detail entry point for the canonical explorer. The model
 * asset key is only a browser selection; /explore performs the recipient's
 * normal authorization and active-generation projection before any query.
 */
export function modelExploreHref(asset: ResourceAssetSummarySignal): string | undefined {
  if (asset.type !== 'model') return undefined
  const resourceID = asset.id?.trim() ?? ''
  const displayKey = asset.key?.trim() ?? ''
  // The resource ID is the authoritative explorer selection. The server
  // resolves it against both the physical object resource ID and its bound
  // browser key; the display key is only a defensive fallback for partial
  // project bootstrap payloads. Keep the return context on that same ID.
  const objectKey = resourceID || displayKey
  if (!objectKey) return undefined
  const base = dataExplorerURL({ mode: 'browse', objectKey } as DataExplorerCommand)
  const queryStart = base.indexOf('?')
  const params = new URLSearchParams(queryStart >= 0 ? base.slice(queryStart + 1) : '')
  params.set('returnSurface', 'model')
  params.set('returnAsset', resourceID || objectKey)
  params.set('returnSection', 'data')
  return `/explore?${params.toString()}`
}
