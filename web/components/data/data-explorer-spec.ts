import type {
  ExplorationFilter,
  ExplorationFilterExpression,
  ExplorationFilterValue,
  ExplorationSpec,
} from '../../generated/exploration'
import type { DataExploreCommand } from '../../generated/signals'
import type { DataExploreDatasetSignal, DataExploreFieldSignal, DataExplorerObjectSignal } from '../../generated/signals'

export const emptyExplorationSpec: ExplorationSpec = {
  schemaVersion: 1,
  modelId: '',
  dimensions: [],
  metrics: [],
  filters: [],
  sort: [],
  limit: 100,
}

export const emptyDataExploreCommand: DataExploreCommand = {
  spec: emptyExplorationSpec,
  requestSeq: 0,
  resetVersion: 0,
  columnWidths: {},
}

/**
 * Reads exploration state from signals that may still be partially hydrated.
 * The fallback is only for browser-side compatibility; server URL decoding
 * remains responsible for rejecting malformed canonical state.
 */
export function explorationSpecFor(command: Pick<Partial<DataExploreCommand>, 'spec'> | null | undefined): ExplorationSpec {
  return command?.spec ?? emptyExplorationSpec
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

export function objectDatasetID(object: DataExplorerObjectSignal): string {
  return object.datasetId?.trim() || object.title.trim()
}

export function fieldColumnID(field: DataExploreFieldSignal): string {
  const parts = field.id.split('.')
  return parts[parts.length - 1] || field.id
}

/** Maps a result column name back to a selected field or authored alias. */
export function explorationSortFieldForResult(spec: ExplorationSpec, resultKey: string): string | undefined {
  const key = resultKey.trim()
  if (!key) return undefined
  const refs = explorationSortRefs(spec)
  for (const ref of refs) {
    const alias = ref.alias?.trim()
    if (alias === key) return alias
  }
  for (const ref of refs) {
    if (ref.field === key) return ref.field
  }
  const names = new Map<string, number>()
  for (const ref of refs) {
    const name = ref.field.slice(ref.field.lastIndexOf('.') + 1) || ref.field
    names.set(name, (names.get(name) ?? 0) + 1)
  }
  for (const ref of refs) {
    const separator = ref.field.indexOf('.')
    const table = separator > 0 ? ref.field.slice(0, separator) : ''
    const name = ref.field.slice(ref.field.lastIndexOf('.') + 1) || ref.field
    const derived = (names.get(name) ?? 0) > 1 && table ? `${table}__${name}` : name
    if (derived === key) return ref.field
  }
  return undefined
}

/** Maps a canonical sort reference to the matching rendered result column. */
export function explorationResultKeyForSort(spec: ExplorationSpec, sortField: string, resultKeys: string[]): string | undefined {
  const canonical = sortField.trim()
  if (!canonical) return undefined
  const ref = explorationSortRefs(spec).find((candidate) => candidate.field === canonical || candidate.alias?.trim() === canonical)
  if (!ref) return undefined
  const alias = ref.alias?.trim()
  if (alias) return resultKeys.find((key) => key === alias) ?? resultKeys.find((key) => explorationSortFieldForResult(spec, key) === canonical)
  return resultKeys.find((key) => key === canonical) ?? resultKeys.find((key) => explorationSortFieldForResult(spec, key) === canonical)
}

type ExplorationSortRef = { field: string; alias?: string }

function explorationSortRefs(spec: ExplorationSpec): ExplorationSortRef[] {
  const refs: ExplorationSortRef[] = [...spec.dimensions, ...spec.metrics]
  if (!spec.time) return refs

  const dimensionIndex = spec.dimensions.findIndex((ref) => ref.field === spec.time!.field)
  if (dimensionIndex >= 0) {
    const dimension = refs[dimensionIndex]
    // The backend gives an authored dimension alias precedence. When the
    // dimension has no alias, a time alias decorates that same output ref.
    if (!dimension.alias?.trim() && spec.time.alias?.trim()) {
      refs[dimensionIndex] = { ...dimension, alias: spec.time.alias }
    }
    return refs
  }

  refs.push({ field: spec.time.field, alias: spec.time.alias })
  return refs
}

export function exploreContextMatchesObject(command: DataExploreCommand, object: DataExplorerObjectSignal): boolean {
  return explorationSpecFor(command).modelId === (object.semanticModelId ?? '')
}

export function fieldLabel(id: string, fields: DataExploreFieldSignal[]): string {
  return fields.find((field) => field.id === id)?.label ?? label(id)
}

export function datasetGrainLabel(dataset: DataExploreDatasetSignal): string {
  const fields = dataset.grainFields ?? []
  return fields.length ? `${dataset.grainEntity} (${fields.join(', ')})` : dataset.grainEntity
}

export function toggleExplorationField(spec: ExplorationSpec, fieldID: string, kind: 'dimension' | 'metric'): ExplorationSpec {
  const key = kind === 'metric' ? 'metrics' : 'dimensions'
  const values = spec[key]
  const selected = values.some((value) => value.field === fieldID)
  const next = selected ? values.filter((value) => value.field !== fieldID) : [...values, { field: fieldID }]
  return { ...spec, [key]: next, sort: explorationSortsWithoutField(spec, fieldID) } as ExplorationSpec
}

export function removeExplorationField(spec: ExplorationSpec, fieldID: string, kind: 'dimension' | 'metric'): ExplorationSpec {
  const key = kind === 'metric' ? 'metrics' : 'dimensions'
  return { ...spec, [key]: spec[key].filter((field) => field.field !== fieldID), sort: explorationSortsWithoutField(spec, fieldID) } as ExplorationSpec
}

export function explorationSortsWithoutField(spec: ExplorationSpec, fieldID: string): ExplorationSpec['sort'] {
  const ref = [...spec.dimensions, ...spec.metrics].find((candidate) => candidate.field === fieldID)
  const alias = ref?.alias?.trim()
  return spec.sort.filter((sort) => sort.field !== fieldID && (!alias || sort.field !== alias))
}

/** Canonicalizes authored JSON without changing array order or dropping spec fields. */
export function canonicalExplorationSpec(spec: ExplorationSpec): ExplorationSpec {
  return canonicalJSON(spec) as ExplorationSpec
}

type ComparisonFilterOperator = Extract<ExplorationFilterExpression, { kind: 'comparison' }>['operator']
type NullCheckFilterOperator = Extract<ExplorationFilterExpression, { kind: 'null_check' }>['operator']
type SetFilterOperator = Extract<ExplorationFilterExpression, { kind: 'set' }>['operator']
export type ExploreFilterOperator = ComparisonFilterOperator | NullCheckFilterOperator | SetFilterOperator

const explorationFilterOperators = new Set<ExploreFilterOperator>([
  'is_null', 'is_not_null', 'in', 'not_in', 'equals', 'not_equals', 'contains', 'not_contains',
  'starts_with', 'ends_with', 'greater_than', 'greater_than_or_equal', 'less_than', 'less_than_or_equal',
])

export function filterOperator(filter: ExplorationFilter): ExploreFilterOperator | '' {
  const expression = filter.expression
  if ('operator' in expression) return expression.operator
  return ''
}

export function filterValues(filter: ExplorationFilter): string[] {
  const expression = filter.expression
  if (expression.kind === 'set') return expression.values.map(filterValue)
  if (expression.kind === 'comparison') return [filterValue(expression.value)]
  if (expression.kind === 'range') return [expression.lower, expression.upper]
    .filter((bound) => bound !== undefined)
    .map((bound) => filterValue(bound.value))
  return []
}

type ExplorationFilterField = string | Pick<DataExploreFieldSignal, 'id' | 'kind' | 'datasetId'>

export function makeExplorationFilter(field: ExplorationFilterField, operator: string, values: string[], type?: string): ExplorationFilter | undefined {
  if (!explorationFilterOperators.has(operator as ExploreFilterOperator)) return undefined
  if ((operator === 'is_null' || operator === 'is_not_null') && values.length !== 0) return undefined
  if ((operator === 'in' || operator === 'not_in') && values.length < 1) return undefined
  if (operator !== 'is_null' && operator !== 'is_not_null' && operator !== 'in' && operator !== 'not_in' && values.length !== 1) return undefined
  const typedValues = values.map((value) => typedFilterValue(value, type))
  if (typedValues.some((value) => value === undefined)) return undefined
  let expression: ExplorationFilterExpression
  if (operator === 'is_null' || operator === 'is_not_null') {
    expression = { kind: 'null_check', operator: operator as NullCheckFilterOperator }
  } else if (operator === 'in' || operator === 'not_in') {
    expression = { kind: 'set', operator: operator as SetFilterOperator, values: typedValues as ExplorationFilterValue[] }
  } else {
    expression = { kind: 'comparison', operator: operator as ComparisonFilterOperator, value: typedValues[0]! }
  }
  const fieldID = typeof field === 'string' ? field : field.id
  const datasetID = typeof field === 'string' ? undefined : localDimensionDatasetID(field)
  return { field: fieldID, ...(datasetID ? { datasetId: datasetID } : {}), expression }
}

/** Physical dimensions are qualified by their owning dataset in the signal.
 * Conformed dimensions use their semantic ID while datasetId identifies the
 * binding owner, so their filters must stay unscoped for multi-dataset plans.
 */
function localDimensionDatasetID(field: Pick<DataExploreFieldSignal, 'id' | 'kind' | 'datasetId'>): string | undefined {
  if (field.kind !== 'dimension') return undefined
  const datasetID = field.datasetId.trim()
  return datasetID && field.id.startsWith(`${datasetID}.`) ? datasetID : undefined
}

function filterValue(value: ExplorationFilterValue): string {
  return String(value.value)
}

function canonicalJSON(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalJSON)
  if (value && typeof value === 'object') {
    const source = value as Record<string, unknown>
    return Object.fromEntries(Object.keys(source).sort().map((key) => [key, canonicalJSON(source[key])]))
  }
  return value
}

function typedFilterValue(value: string, type?: string): ExplorationFilterValue | undefined {
  const normalized = type?.trim().toLowerCase() ?? ''
  if (normalized.includes('bool')) {
    if (value !== 'true' && value !== 'false') return undefined
    return { kind: 'boolean', value: value === 'true' }
  }
  if (normalized.includes('int')) {
    return /^(?:0|-?[1-9]\d*)$/.test(value) ? { kind: 'integer', value } : undefined
  }
  if (normalized === 'number' || normalized.includes('decimal') || normalized.includes('numeric') || normalized.includes('double') || normalized.includes('float')) {
    return /^-?(?:0|[1-9]\d*)(?:\.\d+)?$/.test(value) ? { kind: 'decimal', value } : undefined
  }
  if (normalized === 'date' || normalized.endsWith('.date')) {
    return validDate(value) ? { kind: 'date', value } : undefined
  }
  if (normalized.includes('timestamp') || normalized.includes('datetime')) {
    return validRFC3339(value) ? { kind: 'timestamp', value } : undefined
  }
  return { kind: 'string', value }
}

function validDate(value: string): boolean {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value)
  if (!match) return false
  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  if (month < 1 || month > 12 || day < 1) return false
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  return day <= days[month - 1]
}

function validRFC3339(value: string): boolean {
  const match = /^(\d{4}-\d{2}-\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(Z|[+-]\d{2}:\d{2})$/.exec(value)
  if (!match || !validDate(match[1])) return false
  const hour = Number(match[2])
  const minute = Number(match[3])
  const second = Number(match[4])
  if (hour > 23 || minute > 59 || second > 59) return false
  if (match[5] === 'Z') return true
  const offset = /[+-](\d{2}):(\d{2})/.exec(match[5])!
  return Number(offset[1]) <= 23 && Number(offset[2]) <= 59
}

function label(value: unknown): string {
  if (value == null || value === '') return '-'
  return String(value)
}
