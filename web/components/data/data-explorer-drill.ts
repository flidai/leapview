import type { ExplorationFilter, ExplorationSpec, ExplorationTimeGrain, ExplorationVisualizationConfig, ExplorationVisualizationFieldRef } from '../../generated/exploration'
import type { DataExploreFieldSignal } from '../../generated/signals'
import type { OptimisticInteractionCommand } from '../dashboard/interaction-selection'
import { canonicalExplorationSpec, makeExplorationFilter } from './data-explorer-spec'

export type ExplorationInteractionMode = 'drill' | 'explore_from_here'

export type ExplorationInteractionErrorCode =
  | 'invalid_mode'
  | 'invalid_action'
  | 'empty_interaction'
  | 'unknown_field'
  | 'unavailable_field'
  | 'not_dimension'
  | 'unsupported_type'
  | 'unsupported_value'
  | 'invalid_grain'
  | 'mismatched_grain'
  | 'missing_grain'
  | 'filter_limit'

export type ExplorationInteractionResult =
  | Readonly<{ ok: true; spec: ExplorationSpec; addedFilters: number }>
  | Readonly<{ ok: false; code: ExplorationInteractionErrorCode; error: string; field?: string }>

const MAX_FILTERS = 100
const timeGrains: ReadonlySet<string> = new Set<ExplorationTimeGrain>([
  'second', 'minute', 'hour', 'day', 'week', 'month', 'quarter', 'year',
])

/**
 * Adds a visual selection to a governed exploration without executing it.
 *
 * `drill` returns a table-shaped view at the dataset's governed row grain;
 * `explore_from_here` changes only filters and keeps visualization/pivot
 * configuration intact. Both modes preserve the rest of the canonical spec.
 */
export function explorationSpecFromInteraction(
  spec: ExplorationSpec,
  fields: readonly DataExploreFieldSignal[],
  command: OptimisticInteractionCommand,
  mode: ExplorationInteractionMode,
  drillFieldIDs: readonly string[] = [],
): ExplorationInteractionResult {
  if (mode !== 'drill' && mode !== 'explore_from_here') {
    return failure('invalid_mode', 'Choose Drill or Explore from here before applying the selection.')
  }
  if (!command || command.sourceKind !== 'visual' || !command.sourceId?.trim() || !command.interactionKind?.trim()) {
    return failure('empty_interaction', 'The visual selection is no longer available. Select a value and try again.')
  }
  if (command.action !== 'set' && command.action !== 'replace' && command.action !== 'clear') {
    return failure('invalid_action', 'The visual selection action is not supported. Select a value and try again.')
  }
  if (command.action === 'clear' || !Array.isArray(command.mappings) || command.mappings.length === 0) {
    return failure('empty_interaction', 'Select at least one value before drilling or exploring from here.')
  }

  const nextFilters: ExplorationFilter[] = []
  for (const mapping of command.mappings) {
    const fieldID = typeof mapping?.field === 'string' ? mapping.field.trim() : ''
    if (!fieldID) return failure('empty_interaction', 'The visual selection does not identify a field.')

    const field = fields.find((candidate) => candidate.id === fieldID)
    if (!field) return failure('unknown_field', `The selected field ${JSON.stringify(fieldID)} is not available in this exploration.`, fieldID)
    if (field.kind !== 'dimension') return failure('not_dimension', `The selected field ${JSON.stringify(fieldID)} is a metric; only dimensions can be used for a drill filter.`, fieldID)
    if (field.compatible === false || field.availability === 'unavailable') {
      return failure('unavailable_field', `${field.label || fieldID} is unavailable for this exploration.`, fieldID)
    }
    if (typeof field.datasetId !== 'string' || !field.datasetId.trim()) {
      return failure('unsupported_type', `${field.label || fieldID} has no dataset binding and cannot be filtered safely.`, fieldID)
    }
    if (!supportedFieldType(field.type)) {
      return failure('unsupported_type', `${field.label || fieldID} has an unsupported type for a typed equality filter.`, fieldID)
    }

    if (!isInteractionScalar(mapping.value)) {
      return failure('unsupported_value', `${field.label || fieldID} does not contain a supported scalar value.`, fieldID)
    }
    if (mapping.grain !== undefined && typeof mapping.grain !== 'string') {
      return failure('invalid_grain', `${field.label || fieldID} has an invalid time grain.`, fieldID)
    }
    const grain = mapping.grain?.trim()
    if (grain) {
      if (!timeGrains.has(grain) || !isTemporalType(field.type)) {
        return failure('invalid_grain', `${field.label || fieldID} does not support the selected time grain ${JSON.stringify(grain)}.`, fieldID)
      }
      const configuredGrains = configuredGrainsFor(spec, fieldID)
      if (configuredGrains.some((configured) => configured !== grain)) {
        return failure('mismatched_grain', `${field.label || fieldID} is configured at a different time grain.`, fieldID)
      }
    }

    const filter = mapping.value === null
      ? makeExplorationFilter(field, 'is_null', [], field.type, spec.datasetId)
      : makeExplorationFilter(field, 'equals', scalarValue(mapping.value), field.type, spec.datasetId)
    if (!filter) {
      return failure('unsupported_value', `${field.label || fieldID} does not contain a valid finite value for its declared type.`, fieldID)
    }
    nextFilters.push(filter)
  }

  const filters = deduplicateFilters([...spec.filters, ...nextFilters])
  if (filters.length > MAX_FILTERS) {
    return failure('filter_limit', `This interaction would create ${filters.length} filters; the governed limit is ${MAX_FILTERS}.`)
  }

  const withoutPivot = mode === 'drill' ? withoutSpecPivot(spec) : spec
  const drillDimensions = mode === 'drill' ? governedDrillDimensions(spec, fields, drillFieldIDs) : undefined
  if (drillDimensions && !drillDimensions.ok) return drillDimensions
  const drillSpec = drillDimensions?.ok
    ? {
        ...withoutPivot,
        dimensions: drillDimensions.dimensions,
        metrics: [],
        sort: drillSort(spec, drillDimensions.dimensions),
        table: { ...withoutPivot.table, columns: drillTableColumns(spec, drillDimensions.dimensions) },
      }
    : withoutPivot
  const next: ExplorationSpec = {
    ...drillSpec,
    filters,
    ...(mode === 'drill' ? { visualization: tableVisualization(drillSpec) } : {}),
  }
  return { ok: true, spec: canonicalExplorationSpec(next), addedFilters: filters.length - deduplicateFilters(spec.filters).length }
}

type DrillDimensionsResult =
  | Readonly<{ ok: true; dimensions: ExplorationSpec['dimensions'] }>
  | Readonly<{ ok: false; code: 'missing_grain'; error: string }>

function governedDrillDimensions(
  spec: ExplorationSpec,
  fields: readonly DataExploreFieldSignal[],
  drillFieldIDs: readonly string[],
): DrillDimensionsResult {
  const datasetID = spec.datasetId?.trim() ?? ''
  const requested = Array.from(new Set(drillFieldIDs.map((field) => field.trim()).filter(Boolean)))
  if (!datasetID || requested.length === 0) {
    return { ok: false, code: 'missing_grain', error: 'This dataset does not expose a governed row grain for drill-to-rows.' }
  }
  const dimensions: ExplorationSpec['dimensions'] = []
  for (const requestedID of requested) {
    const field = fields.find((candidate) => candidate.id === requestedID)
      ?? fields.find((candidate) => candidate.id === `${datasetID}.${requestedID}`)
    if (!field || field.kind !== 'dimension' || field.compatible === false || field.datasetId !== datasetID) {
      return { ok: false, code: 'missing_grain', error: `The governed row-grain field ${JSON.stringify(requestedID)} is unavailable.` }
    }
    dimensions.push({ field: field.id })
  }
  return { ok: true, dimensions }
}

function drillSort(spec: ExplorationSpec, dimensions: ExplorationSpec['dimensions']): ExplorationSpec['sort'] {
  const allowed = new Set(dimensions.flatMap((field) => [field.field, field.alias?.trim() ?? '']))
  if (spec.time) {
    allowed.add(spec.time.field)
    if (spec.time.alias?.trim()) allowed.add(spec.time.alias.trim())
  }
  return spec.sort.filter((sort) => allowed.has(sort.field))
}

function drillTableColumns(spec: ExplorationSpec, dimensions: ExplorationSpec['dimensions']): NonNullable<ExplorationSpec['table']>['columns'] {
  return deduplicateFields([
    ...dimensions.map(({ field }) => ({ field })),
    ...(spec.time && !dimensions.some((dimension) => dimension.field === spec.time?.field) ? [{ field: spec.time.field }] : []),
  ])
}

/** A descriptive alias for callers that prefer an imperative name. */
export const applyExplorationInteraction = explorationSpecFromInteraction

function tableVisualization(spec: ExplorationSpec): ExplorationVisualizationConfig {
  const existingColumns = spec.table?.columns?.length
    ? spec.table.columns.map(({ field, format }) => ({ field, ...(format ? { format } : {}) }))
    : []
  const fields = existingColumns.length
    ? existingColumns
    : deduplicateFields([
      ...spec.dimensions.map(({ field }) => ({ field })),
      ...(spec.time ? [{ field: spec.time.field }] : []),
      ...spec.metrics.map(({ field }) => ({ field })),
    ])
  return { kind: 'table', columns: fields }
}

function configuredGrainsFor(spec: ExplorationSpec, fieldID: string): string[] {
  const grains: string[] = []
  for (const ref of spec.dimensions) {
    if (ref.field === fieldID && ref.grain) grains.push(ref.grain)
  }
  if (spec.time?.field === fieldID) grains.push(spec.time.grain)
  return grains
}

function deduplicateFields(fields: ExplorationVisualizationFieldRef[]): ExplorationVisualizationFieldRef[] {
  const seen = new Set<string>()
  return fields.filter((field) => {
    if (seen.has(field.field)) return false
    seen.add(field.field)
    return true
  })
}

function deduplicateFilters(filters: readonly ExplorationFilter[]): ExplorationFilter[] {
  const seen = new Set<string>()
  return filters.filter((filter) => {
    const key = stableJSON(filter)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function stableJSON(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(',')}]`
  if (value && typeof value === 'object') {
    return `{${Object.keys(value as Record<string, unknown>).sort().map((key) => `${JSON.stringify(key)}:${stableJSON((value as Record<string, unknown>)[key])}`).join(',')}}`
  }
  return JSON.stringify(value)
}

function scalarValue(value: string | number | boolean | null): string[] {
  return [typeof value === 'string' ? value : String(value)]
}

function supportedFieldType(type: string | undefined): boolean {
  if (type === undefined) return true
  if (typeof type !== 'string' || !type.trim()) return false
  const normalized = type.trim().toLowerCase()
  return normalized.includes('bool')
    || normalized.includes('int')
    || normalized === 'number'
    || /decimal|numeric|double|float/.test(normalized)
    || normalized === 'date'
    || normalized.endsWith('.date')
    || normalized === 'day'
    || /timestamp|datetime/.test(normalized)
    || /string|text|char|uuid/.test(normalized)
}

function isTemporalType(type: string | undefined): boolean {
  if (typeof type !== 'string') return false
  const normalized = type?.trim().toLowerCase() ?? ''
  return normalized === 'date' || normalized.endsWith('.date') || normalized === 'day' || /timestamp|datetime/.test(normalized)
}

function isInteractionScalar(value: unknown): value is string | number | boolean | null {
  return value === null
    || typeof value === 'string'
    || typeof value === 'boolean'
    || (typeof value === 'number' && Number.isFinite(value))
}

function withoutSpecPivot(spec: ExplorationSpec): ExplorationSpec {
  const { pivot: _pivot, ...withoutPivot } = spec
  return withoutPivot as ExplorationSpec
}

function failure(code: ExplorationInteractionErrorCode, error: string, field?: string): ExplorationInteractionResult {
  return { ok: false, code, error, ...(field ? { field } : {}) }
}
