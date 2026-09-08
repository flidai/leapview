import type { DataExploreSignal, DataExplorerObjectSignal } from '../../generated/signals'

export type ResourceGroup = {
  id: string
  title: string
  objects: DataExplorerObjectSignal[]
}

export function groupObjectsBySemanticModel(objects: DataExplorerObjectSignal[], semanticModels: DataExploreSignal['semanticModels'] = []): ResourceGroup[] {
  const groups = new Map<string, ResourceGroup>()
  const modelTitles = new Map(semanticModels.map((model) => [model.id, model.title]))
  for (const object of objects) {
    if (object.layer === 'source') continue
    const id = object.semanticModelId || object.layer
    if (!groups.has(id)) {
      groups.set(id, { id, title: modelTitles.get(id) || object.semanticModelId || 'Data objects', objects: [] })
    }
    groups.get(id)!.objects.push(object)
  }
  return Array.from(groups.values()).filter((group) => group.objects.length > 0)
}
