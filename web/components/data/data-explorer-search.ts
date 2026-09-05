import type { DataExplorerObjectSignal } from '../../generated/signals'

export function filterObjects(objects: DataExplorerObjectSignal[], query: string): DataExplorerObjectSignal[] {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return objects
  return objects.filter((object) => objectSearchValues(object)
    .some((value) => value.toLowerCase().includes(normalized)))
}

function objectSearchValues(object: DataExplorerObjectSignal): string[] {
  return [
    object.title,
    object.description,
    object.layer,
    object.resourceId,
    object.semanticModelId,
    object.datasetId,
    ...(object.columns ?? []).flatMap((column) => [column.key, column.label, column.type, column.description]),
  ].map((value) => String(value ?? ''))
}

export function objectColumnMatchesSearch(object: DataExplorerObjectSignal, query: string): boolean {
  const normalized = query.trim().toLowerCase()
  if (!normalized) return false
  return (object.columns ?? []).some((column) => [column.key, column.label, column.type, column.description]
    .some((value) => String(value ?? '').toLowerCase().includes(normalized)))
}
