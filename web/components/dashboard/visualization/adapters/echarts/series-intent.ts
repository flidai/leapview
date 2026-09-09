import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import { categoryIdentity } from './category-colors'

export type CartesianSpec = Extract<VisualizationEnvelope['spec'], { kind: 'cartesian' }>

type CartesianCategoryValue = string | number | boolean | null | undefined
export type CartesianCategory = Readonly<{ value: CartesianCategoryValue; key: string; name: string }>
type CartesianCategoryLookup = Readonly<{ byIdentity: ReadonlyMap<string, CartesianCategory>; byDisplay: ReadonlyMap<string, readonly CartesianCategory[]>; byName: ReadonlyMap<string, CartesianCategory> }>

export function comboAxisField(spec: CartesianSpec, axis: 'primary' | 'secondary'): CartesianSpec['y'][number] | undefined {
  if (spec.mark !== 'combo' || spec.y.length === 0) return undefined
  const configured = spec.presentation.comboSeries
  if (configured === undefined) return axis === 'primary' ? spec.y[0] : undefined
  if (spec.series !== undefined) {
    return configured.some((item) => item.axis === axis) ? spec.y[0] : undefined
  }
  return spec.y.find((candidate) => configured.some((item) => String(item.seriesValue) === candidate.field && item.axis === axis))
}

export function orderedY(spec: CartesianSpec): CartesianSpec['y'] {
  const intents = spec.presentation.seriesIntent ?? []
  const explicit = [...intents]
    .filter((intent) => intent.order !== undefined)
    .sort((left, right) => left.order! - right.order! || left.value.localeCompare(right.value, 'en'))
  const orderless = intents.filter((intent) => intent.order === undefined)
  const ordered: CartesianSpec['y'] = []
  const seen = new Set<string>()
  for (const intent of [...explicit, ...orderless]) {
    const value = spec.y.find((candidate) => candidate.field === intent.value)
    if (!value || seen.has(value.field)) continue
    seen.add(value.field)
    ordered.push(value)
  }
  return [...ordered, ...spec.y.filter((value) => !seen.has(value.field))]
}

export function orderedCartesianCategories(values: readonly unknown[]): CartesianCategory[] {
  const unique = new Map<string, CartesianCategoryValue>()
  for (const value of values) {
    if (value !== null && value !== undefined && typeof value !== 'string' && typeof value !== 'number' && typeof value !== 'boolean') continue
    const key = categoryIdentity(value)
    if (!unique.has(key)) unique.set(key, value)
  }
  const categories = [...unique.entries()]
    .sort(([left], [right]) => left.localeCompare(right, 'en'))
    .map(([key, value]) => ({ value, key, name: cartesianCategoryName(value) }))
  const names = new Map<string, number>()
  for (const category of categories) names.set(category.name, (names.get(category.name) ?? 0) + 1)
  return categories.map((category) => names.get(category.name) === 1
    ? category
    : { ...category, name: `${category.name} [${category.key}]` })
}

export function cartesianCategoryLookup(categories: readonly CartesianCategory[]): CartesianCategoryLookup {
  const byIdentity = new Map(categories.map((category) => [category.key, category] as const))
  const byDisplay = new Map<string, CartesianCategory[]>()
  const byName = new Map<string, CartesianCategory>()
  for (const category of categories) {
    const matches = byDisplay.get(String(category.value)) ?? []
    matches.push(category)
    byDisplay.set(String(category.value), matches)
    byName.set(category.name, category)
  }
  return { byIdentity, byDisplay, byName }
}

export function resolveCartesianCategory(lookup: CartesianCategoryLookup, rawValue: unknown): CartesianCategory | undefined {
  const identity = categoryIdentity(rawValue)
  const exact = lookup.byIdentity.get(identity)
  if (exact) return exact
  if (typeof rawValue !== 'string') return undefined
  const named = lookup.byName.get(rawValue)
  if (named) return named
  const displayMatches = lookup.byDisplay.get(rawValue) ?? []
  return displayMatches.length === 1 ? displayMatches[0] : undefined
}

function cartesianCategoryName(value: unknown): string {
  if (value === null) return '(null)'
  if (value === undefined) return '(undefined)'
  if (value === '') return '(empty)'
  return String(value)
}

export function conditionalColorWithFallback(
  conditional: ((params: { value?: unknown }) => string | undefined) | undefined,
  fallback: string | ((params: { value?: unknown }) => string | undefined),
): string | ((params: { value?: unknown }) => string | undefined) {
  if (!conditional) return fallback
  return (params) => conditional(params) ?? (typeof fallback === 'function' ? fallback(params) : fallback)
}

export function conditionalColorChain(
  conditionals: readonly (((params: { value?: unknown }) => string | undefined) | undefined)[],
  fallback: string | ((params: { value?: unknown }) => string | undefined),
): string | ((params: { value?: unknown }) => string | undefined) {
  let result = fallback
  for (let index = conditionals.length - 1; index >= 0; index--) result = conditionalColorWithFallback(conditionals[index], result)
  return result
}

export function stackingMode(spec: CartesianSpec): 'none' | 'normal' | 'percent' {
  return spec.presentation.stacking ?? (spec.presentation.stacked ? 'normal' : 'none')
}

export function cartesianIsHorizontal(spec: CartesianSpec): boolean {
  if (spec.presentation.orientation !== undefined) return spec.presentation.orientation === 'horizontal'
  if (spec.mark === 'bar') return true
  if (spec.mark !== 'combo') return false
  // A combo policy is validated before it reaches a renderer, so bar and
  // column marks cannot be mixed. Infer the default axis direction from the
  // one applicable bar/column family while allowing line/area-only combos to
  // retain the ordinary vertical Cartesian layout.
  return spec.presentation.comboSeries?.some((item) => item.mark === 'bar') ?? false
}

export function seriesID(dataset = 'primary', value = 'value'): string { return `series:${dataset}:${value}` }

export function cartesianSeriesType(mark: CartesianSpec['mark']): string {
  switch (mark) {
    case 'bar': case 'column': case 'waterfall': case 'histogram': return 'bar'
    case 'candlestick': return 'candlestick'
    case 'boxplot': return 'boxplot'
    default: return 'line'
  }
}
