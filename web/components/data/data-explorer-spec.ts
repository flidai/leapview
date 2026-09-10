import type {
  ExplorationFilter,
  ExplorationFilterExpression,
  ExplorationFilterValue,
  ExplorationSpec,
} from '../../generated/exploration'
import type { DataExploreCommand } from '../../generated/signals'
import type { DataExploreDatasetSignal, DataExploreFieldSignal, DataExplorerObjectSignal } from '../../generated/signals'

/** The controls intentionally use the values from the authored contract. */
export const explorationTimeGrains = ['second', 'minute', 'hour', 'day', 'week', 'month', 'quarter', 'year'] as const
export type ExplorationTimeGrainValue = typeof explorationTimeGrains[number]

/** Keep UI choices inside the server-side ExplorationRowLimit bounds. */
export const explorationLimitOptions = [50, 100, 250, 500, 1000] as const

export const unsupportedRelativeTimeRangeMessage = 'Relative time ranges are not supported yet. Choose All available or an absolute range before running the exploration.'

type ExplorationSortValue = ExplorationSpec['sort'][number]
type ExplorationTimeValue = NonNullable<ExplorationSpec['time']>
type ExplorationPivotValue = NonNullable<ExplorationSpec['pivot']>
type ExplorationDimensionValue = ExplorationSpec['dimensions'][number]
type ExplorationMetricValue = ExplorationSpec['metrics'][number]

export type ExplorationPivotSection = 'rows' | 'columns' | 'metrics'

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

/** Returns selectable sort references in authored order, including a time field. */
export function explorationSortFields(spec: ExplorationSpec): string[] {
  const fields = [...spec.dimensions, ...spec.metrics].flatMap((ref) => [ref.field, ref.alias?.trim() ?? ''])
  if (spec.time && !fields.includes(spec.time.field)) fields.push(spec.time.field)
  return Array.from(new Set(fields.filter(Boolean)))
}

/** Adds a sort key or updates its direction without disturbing other keys. */
export function upsertExplorationSort(
  spec: ExplorationSpec,
  field: string,
  direction: ExplorationSortValue['direction'] = 'asc',
): ExplorationSpec {
  const key = field.trim()
  if (!key || !explorationSortFields(spec).includes(key)) return spec
  const index = spec.sort.findIndex((sort) => sort.field === key)
  const sort = [...spec.sort]
  if (index >= 0) sort[index] = { ...sort[index], direction }
  else sort.push({ field: key, direction })
  return { ...spec, sort }
}

export function removeExplorationSort(spec: ExplorationSpec, index: number): ExplorationSpec {
  if (index < 0 || index >= spec.sort.length) return spec
  return { ...spec, sort: spec.sort.filter((_, current) => current !== index) }
}

/** Moves a sort key while retaining the user's priority order. */
export function moveExplorationSort(spec: ExplorationSpec, index: number, delta: -1 | 1): ExplorationSpec {
  const target = index + delta
  if (index < 0 || target < 0 || index >= spec.sort.length || target >= spec.sort.length) return spec
  const sort = [...spec.sort]
  const [entry] = sort.splice(index, 1)
  sort.splice(target, 0, entry!)
  return { ...spec, sort }
}

export function boundedExplorationLimit(value: number, fallback = 100): number {
  const candidate = Number.isFinite(value) ? Math.trunc(value) : fallback
  return Math.min(1000, Math.max(1, candidate || fallback))
}

/** Returns the pivot object without adding browser-only state to the spec. */
export function pivotForSpec(spec: ExplorationSpec): ExplorationPivotValue {
  return spec.pivot ?? { rows: [], columns: [], metrics: [] }
}

export function updateExplorationPivot(
  spec: ExplorationSpec,
  section: ExplorationPivotSection,
  refs: ExplorationPivotValue[ExplorationPivotSection],
): ExplorationSpec {
  const pivot = pivotForSpec(spec)
  return { ...spec, pivot: { ...pivot, [section]: [...refs] } }
}

export function removeExplorationPivot(spec: ExplorationSpec): ExplorationSpec {
  if (!spec.pivot) return spec
  const { pivot: _pivot, ...withoutPivot } = spec
  // Keep the property present so a complete-spec update can explicitly clear
  // a pivot instead of allowing a controller merge to restore the old value.
  return { ...withoutPivot, pivot: undefined } as ExplorationSpec
}

export function updateExplorationPivotWindow(
  spec: ExplorationSpec,
  key: 'limit' | 'offset',
  value: number,
): ExplorationSpec {
  const pivot = pivotForSpec(spec)
  const numeric = key === 'offset'
    ? Math.max(0, Math.trunc(Number(value) || 0))
    : boundedExplorationLimit(Number(value), spec.limit)
  const window = {
    ...pivot.window,
    limit: pivot.window?.limit ?? spec.limit,
    ...(key === 'limit' ? { limit: numeric } : { offset: numeric }),
  }
  return { ...spec, pivot: { ...pivot, window } }
}

/**
 * Performs the client-side checks that can be made without semantic-model
 * metadata. The server remains authoritative; these messages prevent users
 * from submitting an obviously unsafe pivot and explain the correction.
 */
export function explorationPivotValidation(spec: ExplorationSpec, fields: DataExploreFieldSignal[] = []): string[] {
  const pivot = spec.pivot
  if (!pivot) return []
  const messages: string[] = []
  const seen = new Map<string, string>()
  const check = (refs: Array<ExplorationDimensionValue | ExplorationMetricValue>, section: string) => {
    refs.forEach((ref) => {
      const field = ref.field.trim()
      const previous = seen.get(field)
      if (previous) messages.push(`${field} is configured as both ${previous} and ${section}; choose one pivot role.`)
      else seen.set(field, section)
      const signal = fields.find((candidate) => candidate.id === field)
      if (signal?.compatible === false && !signal.rebaseDatasetId) {
        messages.push(`${field} is unavailable for this exploration and cannot be used in the pivot.`)
      }
      if (section === 'metric' && signal && signal.kind !== 'metric') messages.push(`${field} is a dimension; choose a metric for pivot values.`)
      if (section !== 'metric' && signal?.kind === 'metric') messages.push(`${field} is a metric; choose a dimension for pivot ${section}.`)
    })
  }
  check(pivot.rows, 'rows')
  check(pivot.columns, 'columns')
  check(pivot.metrics, 'metric')
  if (!pivot.rows.length) messages.push('Add at least one row dimension before running a pivot.')
  if (!pivot.columns.length) messages.push('Add at least one column dimension before running a pivot.')
  if (!pivot.metrics.length) messages.push('Add at least one metric before running a pivot.')
  if (pivot.window && (!Number.isInteger(pivot.window.limit) || pivot.window.limit < 1 || pivot.window.limit > 1000)) {
    messages.push('Pivot limit must be between 1 and 1000 rows or columns.')
  }
  if (pivot.window?.offset !== undefined && (!Number.isInteger(pivot.window.offset) || pivot.window.offset < 0)) {
    messages.push('Pivot offset cannot be negative.')
  }
  if ((pivot.window?.offset ?? 0) > 0) {
    messages.push('Pivot row-window offsets are not available yet. Use an offset of 0.')
  }
  return Array.from(new Set(messages))
}

/** Returns the client-side blockers that make an explicit Run unsafe. */
export function explorationRunValidation(spec: ExplorationSpec, fields: DataExploreFieldSignal[] = []): string[] {
  const messages: string[] = []
  if (!spec.modelId.trim()) messages.push('Choose a semantic model before running the exploration.')
  if (!spec.dimensions.length && !spec.metrics.length && !spec.time) messages.push('Select at least one field or time grain before running the exploration.')
  if (spec.time?.range?.kind === 'relative') messages.push(unsupportedRelativeTimeRangeMessage)
  messages.push(...explorationPivotValidation(spec, fields))
  return Array.from(new Set(messages))
}

export function setExplorationTime(
  spec: ExplorationSpec,
  field: string,
  grain: ExplorationTimeGrainValue = 'day',
): ExplorationSpec {
  const key = field.trim()
  const previousTime = spec.time
  const previousTimeKeys = previousTime
    ? new Set([previousTime.field.trim(), previousTime.alias?.trim() ?? ''].filter(Boolean))
    : undefined
  if (!key) {
    const { time: _time, ...withoutTime } = spec
    return {
      ...withoutTime,
      // Preserve an explicit undefined marker for complete-spec callers.
      time: undefined,
      sort: previousTimeKeys
        ? spec.sort.filter((sort) => !previousTimeKeys.has(sort.field))
        : spec.sort,
    } as ExplorationSpec
  }
  const next: ExplorationTimeValue = { ...spec.time, field: key, grain }
  const sort = previousTime && previousTime.field.trim() !== key && previousTimeKeys
    ? spec.sort.filter((entry) => !previousTimeKeys.has(entry.field))
    : spec.sort
  return { ...spec, time: next, sort }
}

export function setExplorationTimeRange(
  spec: ExplorationSpec,
  range: NonNullable<ExplorationTimeValue['range']> | undefined,
): ExplorationSpec {
  if (!spec.time) return spec
  return { ...spec, time: { ...spec.time, ...(range ? { range } : { range: undefined }) } }
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

type ExplorationFilterOption = { value: string; label: string }
const nullFilterOptions: ExplorationFilterOption[] = [{ value: 'is_null', label: 'Is null' }, { value: 'is_not_null', label: 'Is not null' }]
const orderedFilterOptions: ExplorationFilterOption[] = [
  { value: 'equals', label: 'Equals' },
  { value: 'not_equals', label: 'Does not equal' },
  { value: 'greater_than', label: 'Greater than' },
  { value: 'greater_than_or_equal', label: 'At least' },
  { value: 'less_than', label: 'Less than' },
  { value: 'less_than_or_equal', label: 'At most' },
]

function normalizedFieldType(type: string | undefined): string { return (type ?? '').trim().toLowerCase() }
function isBooleanType(type: string | undefined): boolean { return normalizedFieldType(type).includes('bool') }
function isNumericType(type: string | undefined): boolean {
  const normalized = normalizedFieldType(type)
  return normalized.includes('int') || normalized === 'number' || /decimal|numeric|double|float/.test(normalized)
}
function isTimestampType(type: string | undefined): boolean { return /timestamp|datetime/.test(normalizedFieldType(type)) }
function isDateType(type: string | undefined): boolean {
  const normalized = normalizedFieldType(type)
  return normalized === 'date' || normalized.endsWith('.date') || normalized === 'day'
}

export function filterOperatorsForType(type: string | undefined): ExplorationFilterOption[] {
  if (isBooleanType(type)) return [{ value: 'equals', label: 'Equals' }, { value: 'not_equals', label: 'Does not equal' }, ...nullFilterOptions]
  if (isNumericType(type) || isDateType(type) || isTimestampType(type)) return [...orderedFilterOptions, ...nullFilterOptions]
  return [
    ...orderedFilterOptions.slice(0, 2),
    { value: 'in', label: 'Is one of' },
    { value: 'not_in', label: 'Is not one of' },
    { value: 'contains', label: 'Contains' },
    { value: 'not_contains', label: 'Does not contain' },
    { value: 'starts_with', label: 'Starts with' },
    { value: 'ends_with', label: 'Ends with' },
    ...nullFilterOptions,
  ]
}

export function filterOperator(filter: ExplorationFilter): ExploreFilterOperator | 'range' | '' {
  const expression = filter.expression
  if (expression.kind === 'range') return 'range'
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

export function makeExplorationFilter(field: ExplorationFilterField, operator: string, values: string[], type?: string, queryDatasetID?: string): ExplorationFilter | undefined {
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
  const datasetID = typeof field === 'string' ? undefined : localDimensionDatasetID(field, queryDatasetID)
  return { field: fieldID, ...(datasetID ? { datasetId: datasetID } : {}), expression }
}

/** Physical dimensions are qualified by their owning dataset in the signal.
 * Conformed dimensions use their semantic ID while datasetId identifies the
 * binding owner, so their filters must stay unscoped for multi-dataset plans.
 */
function localDimensionDatasetID(field: Pick<DataExploreFieldSignal, 'id' | 'kind' | 'datasetId'>, queryDatasetID?: string): string | undefined {
  if (field.kind !== 'dimension') return undefined
  const datasetID = field.datasetId.trim()
  if (!datasetID || !field.id.startsWith(`${datasetID}.`)) return undefined
  // Filter scope is the active query root for a single-root exploration. The
  // physical field owner is only the fallback for callers building a
  // multi-dataset plan without an active root override.
  return queryDatasetID?.trim() || datasetID
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
  if (normalized === 'date' || normalized.endsWith('.date') || normalized === 'day') {
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
