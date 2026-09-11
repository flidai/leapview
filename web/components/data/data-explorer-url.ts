import type { DataExplorerCommand } from '../../generated/signals'

export function dataExplorerURL(command: DataExplorerCommand): string {
  const mode = command.mode === 'explore' ? 'explore' : 'browse'
  const objectKey = command.objectKey || ''
  const params = new URLSearchParams()
  if (mode === 'explore') {
    params.set('v', '1')
    params.set('mode', 'explore')
    if (command.explore?.semanticModelId) params.set('semanticModel', command.explore.semanticModelId)
    if (command.explore?.datasetId) params.set('dataset', command.explore.datasetId)
    for (const field of command.explore?.dimensions ?? []) params.append('dimension', field)
    for (const metric of command.explore?.metrics ?? []) params.append('metric', metric)
    for (const filter of command.explore?.filters ?? []) params.append('filter', JSON.stringify(filter))
    for (const sort of command.explore?.sort ?? []) params.append('sort', JSON.stringify(sort))
    if (command.explore?.time) params.set('time', JSON.stringify(command.explore.time))
    if (command.explore?.limit && command.explore.limit !== 100) params.set('limit', String(command.explore.limit))
  } else if (objectKey) {
    params.set('object', objectKey)
  }
  return params.toString() ? `/explore?${params.toString()}` : '/explore'
}
