import type { DataExplorerCommand, ResourceAssetSummarySignal } from '../../generated/signals'
import { dataExplorerURL } from '../data/data-explorer-url'

/**
 * Builds the model detail entry point for the canonical explorer. The model
 * asset key is only a browser selection; /explore performs the recipient's
 * normal authorization and active-generation projection before any query.
 */
export function modelExploreHref(asset: ResourceAssetSummarySignal): string | undefined {
  if (asset.type !== 'model') return undefined
  // Resource ID is the stable explorer selection across catalog projections;
  // display keys can be a bare model name in one surface and a prefixed key
  // in another.
  const objectKey = asset.id?.trim() || asset.key?.trim()
  if (!objectKey) return undefined
  const base = dataExplorerURL({ mode: 'browse', objectKey } as DataExplorerCommand)
  const queryStart = base.indexOf('?')
  const params = new URLSearchParams(queryStart >= 0 ? base.slice(queryStart + 1) : '')
  params.set('returnSurface', 'model')
  params.set('returnAsset', asset.id.trim())
  params.set('returnSection', 'data')
  return `/explore?${params.toString()}`
}
