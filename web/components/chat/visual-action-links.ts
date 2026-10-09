import type { ExplorationSpec, ExplorationFilter, ExplorationFilterExpression } from '../../generated/exploration'
import type { ChatTranscriptItemSignal, ChatStatus, DataExplorerCommand } from '../../generated/signals'
import type { DashboardTimeGrain } from '../../generated/dashboard'
import { dataExplorerURL } from '../data/data-explorer-url'

type JsonRecord = Record<string, unknown>
type ExploreDimension = { sourceField: string; field: string; resultAlias?: string; grain?: DashboardTimeGrain; alias?: string }

export function queryVisualExplorerURL(item: ChatTranscriptItemSignal | undefined, artifactType: string, artifactID: string): string {
  if (item?.kind !== 'tool' || item.name !== 'query_visual' || item.status !== 'complete' || item.error || !item.argumentsJson || !item.resultJson) return ''

  let input: unknown
  let result: unknown
  try {
    input = JSON.parse(item.argumentsJson)
    result = JSON.parse(item.resultJson)
  } catch {
    return ''
  }
  if (!isRecord(input) || !isNonEmptyString(input.semanticModelId) || !isRecord(input.visual) || !isRecord(result)) return ''
  if (!isRecord(result.semanticModelRef) || result.semanticModelRef.id !== input.semanticModelId || result.id !== artifactID || result.type !== artifactType || result.ok !== true || !isNonEmptyString(result.datasetId) || !Array.isArray(result.fields)) return ''
  const filters = exploreFilters(input.filters, result, input.semanticModelId)
  if (filters === null) return ''

  const visual = input.visual
  if (!isEmptyRecord(visual.datasets) || !isEmptyArray(visual.calculations) || !isEmptyArray(visual.interactions)) return ''
  if (!isRecord(visual.query) || visual.query.type !== 'aggregate' || visual.type !== artifactType) return ''

  const query = visual.query
  if (!hasOnlyKeys(query, ['type', 'dimensions', 'metrics', 'sort', 'limit'])) return ''
  if (!Array.isArray(query.dimensions) || !Array.isArray(query.metrics)) return ''

  const dimensions: string[] = []
  const sortFields = new Map<string, string>()
  const selectedFields = new Set<string>()
  let time: { field: string; grain: DashboardTimeGrain; alias?: string } | undefined
  for (const value of query.dimensions) {
    const dimension = exploreDimension(value, result.fields, result.semanticModelRef.id)
    if (!dimension || selectedFields.has(dimension.field)) return ''
    selectedFields.add(dimension.field)
    if (dimension.grain) {
      if (time) return ''
      time = { field: dimension.field, grain: dimension.grain, ...(dimension.alias ? { alias: dimension.alias } : {}) }
      sortFields.set(dimension.sourceField, dimension.field)
      if (dimension.alias) sortFields.set(dimension.alias, dimension.field)
      if (dimension.resultAlias) sortFields.set(dimension.resultAlias, dimension.field)
    } else {
      if (dimension.alias) return ''
      dimensions.push(dimension.field)
      sortFields.set(dimension.sourceField, dimension.field)
      if (dimension.resultAlias) sortFields.set(dimension.resultAlias, dimension.field)
    }
  }

  const metrics: string[] = []
  for (const value of query.metrics) {
    const metric = exploreMetric(value)
    if (!metric || selectedFields.has(metric)) return ''
    selectedFields.add(metric)
    metrics.push(metric)
    sortFields.set(metric, metric)
  }
  if (selectedFields.size === 0) return ''

  const sort = exploreSort(query.sort, sortFields)
  if (sort === null) return ''

  const explicitLimit = query.limit
  const maxRows = isRecord(visual.dataBudget) ? visual.dataBudget.maxRows : undefined
  const limit = (isRecord(result.completeness) ? result.completeness.limit : undefined) ?? explicitLimit ?? maxRows ?? 50
  if (!Number.isSafeInteger(limit) || (limit as number) <= 0 || (limit as number) > 1000) return ''

  const spec: ExplorationSpec = {
    schemaVersion: 1,
    modelId: input.semanticModelId.trim(), datasetId: result.datasetId,
    dimensions: dimensions.map(field => ({ field })),
    metrics: metrics.map(field => ({ field })), filters, sort,
    ...(time ? { time } : {}), limit: limit as number,
  }
  return dataExplorerURL({ mode: 'explore', explore: { spec } } as DataExplorerCommand)
}

// A filter can be reopened only when the execution recorded an exact Explorer
// field binding. Never infer join paths from the base dataset or field spelling.
function exploreFilters(value: unknown, result: JsonRecord, modelID: string): ExplorationFilter[] | null {
  if (value === undefined) return []
  if (!Array.isArray(value)) return null
  const filters: ExplorationFilter[] = []
  for (const filter of value) {
    if (!isRecord(filter) || !isNonEmptyString(filter.dimension)) return null
    if (!isEmptyArray(filter.targets)) return null
    const expression = filter.default
    if (expression === undefined || (isRecord(expression) && expression.type === 'unfiltered')) continue
    if (!isRecord(expression)) return null
    const fieldID = filter.dimension.includes('.') ? filter.dimension : `${modelID}.${filter.dimension}`
    const usage = Array.isArray(result.fields) ? result.fields.find(usage => isRecord(usage) && usage.fieldId === fieldID && usage.role === 'dimension') : undefined
    if (!isRecord(usage) || !isNonEmptyString(usage.explorerFieldId)) return null
    const scopes = Array.isArray(result.filters) ? result.filters.filter(usage => isRecord(usage) && usage.fieldId === fieldID) : []
    const datasets = new Set(scopes.map(usage => isRecord(usage) ? usage.resolvedDatasetId : undefined).filter(isNonEmptyString))
    if (datasets.size > 1) return null
    const translated = exploreFilterExpression(expression)
    if (!translated) return null
    filters.push({ field: usage.explorerFieldId, datasetId: datasets.values().next().value, expression: translated })
  }
  return filters
}

function exploreFilterExpression(expression: JsonRecord): ExplorationFilterExpression | null {
  if (!['comparison', 'set', 'nullCheck', 'range'].includes(String(expression.type))) return null
  // These contracts share typed scalar values and bounds; only discriminator
  // and operator spelling differ. Preserve precision-safe numeric strings.
  const translate = (value: unknown): unknown => {
    if (Array.isArray(value)) return value.map(translate)
    if (!isRecord(value)) return value
    return Object.fromEntries(Object.entries(value).map(([key, item]) => {
      if (key === 'type') return ['kind', String(item).replace(/[A-Z]/g, letter => `_${letter.toLowerCase()}`)]
      if (key === 'operator') return [key, String(item).replace(/[A-Z]/g, letter => `_${letter.toLowerCase()}`)]
      return [key, translate(item)]
    }))
  }
  return translate(expression) as ExplorationFilterExpression
}

function exploreDimension(value: unknown, fields: unknown[], semanticModelID: string): ExploreDimension | undefined {
  let sourceField: string
  let grain: DashboardTimeGrain | undefined
  let alias: string | undefined
  if (isNonEmptyString(value)) {
    sourceField = value.trim()
  } else {
    if (!isRecord(value) || !hasOnlyKeys(value, ['dimension', 'grain', 'alias']) || !isNonEmptyString(value.dimension)) return
    sourceField = value.dimension.trim()
    const grains: DashboardTimeGrain[] = ['second', 'minute', 'hour', 'day', 'week', 'month', 'quarter', 'year']
    if (value.grain !== undefined && (typeof value.grain !== 'string' || !grains.includes(value.grain as DashboardTimeGrain))) return
    if (value.alias !== undefined && !isNonEmptyString(value.alias)) return
    if (value.alias !== undefined && value.grain === undefined) return
    grain = value.grain as DashboardTimeGrain | undefined
    alias = isNonEmptyString(value.alias) ? value.alias.trim() : undefined
  }

  const semanticFieldID = sourceField.includes('.') ? sourceField : `${semanticModelID}.${sourceField}`
  const usage = fields.find((value) => isRecord(value) && value.role === 'dimension' && value.fieldId === semanticFieldID && (!alias || value.alias === alias))
  if (!isRecord(usage) || !isNonEmptyString(usage.explorerFieldId)) return
  return {
    sourceField,
    field: usage.explorerFieldId.trim(),
    ...(isNonEmptyString(usage.alias) ? { resultAlias: usage.alias.trim() } : {}),
    ...(grain ? { grain } : {}),
    ...(alias ? { alias } : {}),
  }
}

function exploreMetric(value: unknown): string | undefined {
  if (isNonEmptyString(value)) return value.trim()
  if (!isRecord(value) || !hasOnlyKeys(value, ['metric']) || !isNonEmptyString(value.metric)) return
  return value.metric.trim()
}

function exploreSort(value: unknown, fields: Map<string, string>): { field: string; direction: 'asc' | 'desc' }[] | null {
  if (value === undefined) return []
  if (!Array.isArray(value)) return null
  const sort: { field: string; direction: 'asc' | 'desc' }[] = []
  for (const item of value) {
    if (!isRecord(item) || !hasOnlyKeys(item, ['field', 'direction']) || !isNonEmptyString(item.field)) return null
    const sourceField = item.field.trim()
    const targetField = fields.get(sourceField)
    if ((item.direction !== 'asc' && item.direction !== 'desc') || !targetField) return null
    sort.push({ field: targetField, direction: item.direction })
  }
  return sort
}

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0
}

function hasOnlyKeys(value: JsonRecord, allowed: string[]): boolean {
  return Object.keys(value).every((key) => allowed.includes(key))
}

function isEmptyArray(value: unknown): boolean {
  return value === undefined || (Array.isArray(value) && value.length === 0)
}

function isEmptyRecord(value: unknown): boolean {
  return value === undefined || (isRecord(value) && Object.keys(value).length === 0)
}


export function visualExplorerHref(conversation: string, artifact: string, run = ''): string {
  return conversation && artifact ? `/chats/${encodeURIComponent(conversation)}/visuals/${encodeURIComponent(artifact)}/explore${run ? `?run=${encodeURIComponent(run)}` : ''}` : ''
}

export function visualDataExplorerHref(item: ChatTranscriptItemSignal): string {
  return item.artifact ? queryVisualExplorerURL(item, item.artifact.type, item.artifact.id) : ''
}

// Tool output streams before the retained messages commit at run completion.
export function retainedVisualExplorerHref(conversation: string, item: ChatTranscriptItemSignal | undefined, status: ChatStatus): string {
  if (!item?.artifact || item.status !== 'complete' || item.error) return ''
  if (status.running && (!status.runId || item.runId === status.runId)) return ''
  return visualExplorerHref(conversation, item.artifact.id, item.runId)
}
