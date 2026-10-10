import type {
  VisualizationEnvelope,
  VisualizationReferenceReducer,
  VisualizationTextBinding,
} from '../../../generated/visualization'

export type ResolvedVisualizationMetadata = Readonly<{
  title: string
  subtitle?: string
  description: string
  summary?: string
}>

// resolveVisualizationMetadata evaluates compiler-approved text bindings over
// the current governed frames. It is deliberately total: missing, empty, or
// incompatible data returns the authored fallback and never leaks a raw value
// through a diagnostic string.
export function resolveVisualizationMetadata(envelope: VisualizationEnvelope): ResolvedVisualizationMetadata {
  const bindings = envelope.spec.metadataBindings
  const title = resolveTextBinding(envelope, bindings?.title) ?? envelope.spec.title
  const subtitle = resolveTextBinding(envelope, bindings?.subtitle) ?? envelope.spec.subtitle
  const description = resolveTextBinding(envelope, bindings?.description) ?? envelope.spec.accessibility.description
  const summary = resolveTextBinding(envelope, bindings?.summary) ?? envelope.spec.accessibility.summary
  return {
    title,
    ...(subtitle ? { subtitle } : {}),
    description,
    ...(summary ? { summary } : {}),
  }
}

export function resolveTextBinding(envelope: VisualizationEnvelope, binding?: VisualizationTextBinding): string | undefined {
  if (!binding) return undefined
  if (envelope.dataState.kind !== 'inline') return binding.fallback
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === binding.field.dataset)
  const index = dataset?.columns.indexOf(binding.field.field) ?? -1
  if (!dataset || index < 0) return binding.fallback
  const values = dataset.rows
    .map((row) => row[index])
    .filter((value): value is string | number | boolean => value !== null && value !== undefined && isScalar(value))
  const reduced = reduce(values, binding.reducer)
  if (reduced === undefined) return binding.fallback
  if (typeof reduced === 'string' && reduced.trim() === '') return binding.fallback
  return `${binding.prefix}${String(reduced)}${binding.suffix}`
}

function reduce(values: readonly (string | number | boolean)[], reducer: VisualizationReferenceReducer): string | number | boolean | undefined {
  if (values.length === 0) return undefined
  switch (reducer) {
    case 'first': return values[0]
    case 'last': return values.at(-1)
    case 'minimum':
    case 'maximum': {
      const comparable = homogeneousComparable(values)
      if (!comparable) return undefined
      const sorted = [...comparable].sort((left, right) => left < right ? -1 : left > right ? 1 : 0)
      return reducer === 'minimum' ? sorted[0] : sorted.at(-1)
    }
    case 'mean':
    case 'median': {
      if (!values.every((value) => typeof value === 'number' && Number.isFinite(value))) return undefined
      const numbers = values as number[]
      if (reducer === 'mean') return numbers.reduce((sum, value) => sum + value, 0) / numbers.length
      const sorted = [...numbers].sort((left, right) => left - right)
      const middle = Math.floor(sorted.length / 2)
      return sorted.length % 2 === 1 ? sorted[middle] : (sorted[middle - 1]! + sorted[middle]!) / 2
    }
  }
}

function homogeneousComparable(values: readonly (string | number | boolean)[]): readonly (string | number)[] | undefined {
  const kind = typeof values[0]
  if ((kind !== 'string' && kind !== 'number') || !values.every((value) => typeof value === kind)) return undefined
  if (kind === 'number' && !values.every((value) => Number.isFinite(value))) return undefined
  return values as readonly (string | number)[]
}

function isScalar(value: unknown): value is string | number | boolean {
  return typeof value === 'string' || typeof value === 'boolean' || (typeof value === 'number' && Number.isFinite(value))
}

export function visualizationSharedHeader(envelope?: VisualizationEnvelope): 'chart' | 'map' | 'visualization' | undefined {
  const kind = envelope?.spec.kind
  if (!kind || kind === 'kpi' || kind === 'table' || kind === 'matrix' || kind === 'pivot') return undefined
  if (kind === 'geographic') return 'map'
  return 'chart'
}
