import { Database, Eye, Server, Table2 } from 'lucide'
import type {
  DataExploreDatasetSignal,
  DataExploreFieldSignal,
  DataExploreSignal,
  DataExplorerObjectSignal,
} from '../../generated/signals'
import { fieldColumnID, objectDatasetID } from './data-explorer-controller'

export type ResourceGroup = {
  id: string
  title: string
  objects: DataExplorerObjectSignal[]
}

export type ExploreFieldGroup = {
  id: string
  kind: 'dimension' | 'metric'
  label: string
  fields: DataExploreFieldSignal[]
}

export function localPreviewDimensions(object: DataExplorerObjectSignal, fields: DataExploreFieldSignal[]): string[] {
  const datasetID = objectDatasetID(object)
  const localFields = fields.filter((field) => field.kind !== 'metric' && field.datasetId === datasetID)
  const localByColumn = new Map(localFields.map((field) => [fieldColumnID(field), field.id]))
  const ordered = (object.columns ?? []).map((column) => localByColumn.get(column.key) ?? `${datasetID}.${column.key}`)
  const seen = new Set(ordered)
  for (const field of localFields) {
    if (!seen.has(field.id)) ordered.push(field.id)
  }
  return ordered
}

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

export function groupExploreFields(fields: DataExploreFieldSignal[]): ExploreFieldGroup[] {
  const groups = new Map<string, ExploreFieldGroup>()
  for (const field of fields) {
    const crossDatasetMetric = field.kind === 'metric' && !field.datasetId
    const id = crossDatasetMetric ? 'cross-dataset:metric' : `${field.datasetId}:${field.kind}`
    if (!groups.has(id)) {
      groups.set(id, {
        id,
        kind: field.kind,
        label: crossDatasetMetric ? 'Multiple datasets · Metrics' : `${label(field.datasetId)} · ${field.kind === 'metric' ? 'Metrics' : 'Dimensions'}`,
        fields: [],
      })
    }
    groups.get(id)!.fields.push(field)
  }
  return Array.from(groups.values())
}

export function fieldLabel(id: string, fields: DataExploreFieldSignal[]): string {
  return fields.find((field) => field.id === id)?.label ?? label(id)
}

export function datasetGrainLabel(dataset: DataExploreDatasetSignal): string {
  const fields = dataset.grainFields ?? []
  return fields.length ? `${dataset.grainEntity} (${fields.join(', ')})` : dataset.grainEntity
}

export function iconForLayer(layer: string): any {
  switch (layer) {
    case 'source': return Server
    case 'semantic_view': return Eye
    case 'model': return Table2
    default: return Database
  }
}

export function layerLabel(layer: string): string {
  switch (layer) {
    case 'source': return 'Source'
    case 'model': return 'Model'
    case 'semantic_view': return 'Semantic view'
    default: return label(layer)
  }
}

export function queryTargetLabel(object: DataExplorerObjectSignal): string {
  const target = object.source || object.datasetId || object.title
  const model = object.semanticModelId ? `${object.semanticModelId} · ` : ''
  return `${layerLabel(object.layer)} · ${model}${target}`
}

export function label(value: unknown): string {
  if (value == null || value === '') return '-'
  return String(value)
}
