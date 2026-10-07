import type { VisualizationEnvelope, VisualizationGeographicLayer } from '../../../../../generated/visualization'
import { parseDecimal } from '../../decimal'

export type MapValueRange = Readonly<{
  minimum: number
  maximum: number
  selectedMinimum: number
  selectedMaximum: number
  step: number
}>

export function mapValueRange(
  envelope: VisualizationEnvelope,
  layer: VisualizationGeographicLayer,
  previous?: MapValueRange,
): MapValueRange | undefined {
  const field = 'value' in layer ? layer.value : undefined
  if (!field) return undefined
  const values: number[] = []
  if (envelope.dataState.kind === 'inline') {
    const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === field.dataset)
    const index = dataset?.columns.indexOf(field.field) ?? -1
    if (dataset && index >= 0) for (const row of dataset.rows) {
      const value = numericMapValue(row[index])
      if (value !== undefined) values.push(value)
    }
  } else if (envelope.dataState.kind === 'spatial_tiled' && envelope.dataState.schema.id === field.dataset) {
    for (const domain of [...envelope.dataState.rawDomains, ...envelope.dataState.aggregateDomains]) {
      if (domain.field !== field.field) continue
      if (typeof domain.minimum === 'number' && Number.isFinite(domain.minimum)) values.push(domain.minimum)
      if (typeof domain.maximum === 'number' && Number.isFinite(domain.maximum)) values.push(domain.maximum)
    }
  }
  if (values.length === 0) return undefined
  const minimum = Math.min(...values), maximum = Math.max(...values)
  if (minimum === maximum) return undefined
  const definition = envelope.spec.datasets.find((candidate) => candidate.id === field.dataset)?.fields.find((candidate) => candidate.id === field.field)
  const resolution = Number.EPSILON * Math.max(1, Math.abs(minimum), Math.abs(maximum))
  const step = Math.max(definition?.dataType === 'integer' ? 1 : (maximum - minimum) / 100, resolution)
  // An untouched full range means "all values", including a replacement data
  // domain. Retain a user restriction only when it overlaps the new domain;
  // clamping a disjoint range to one endpoint would silently hide all peers.
  const restricted = previous && (previous.selectedMinimum > previous.minimum || previous.selectedMaximum < previous.maximum)
    && previous.selectedMinimum <= maximum && previous.selectedMaximum >= minimum
  return {
    minimum,
    maximum,
    selectedMinimum: clamp(restricted ? previous.selectedMinimum : minimum, minimum, maximum),
    selectedMaximum: clamp(restricted ? previous.selectedMaximum : maximum, minimum, maximum),
    step,
  }
}

export function withMapValueSelection(range: MapValueRange, selectedMinimum: number, selectedMaximum: number): MapValueRange {
  const lower = clamp(Math.min(selectedMinimum, selectedMaximum), range.minimum, range.maximum)
  const upper = clamp(Math.max(selectedMinimum, selectedMaximum), range.minimum, range.maximum)
  return { ...range, selectedMinimum: lower, selectedMaximum: upper }
}

export function mapValueFilterExpression(property: string, range: MapValueRange): unknown[] | undefined {
  if (range.selectedMinimum <= range.minimum && range.selectedMaximum >= range.maximum) return undefined
  // Public decimals may be exact strings. Coerce only the rendering/filter
  // expression, keeping source scalars intact for tooltips and commands.
  const numeric = ['to-number', ['get', property]]
  return ['case', ['in', ['typeof', ['get', property]], ['literal', ['number', 'string']]],
    ['all', ['>=', numeric, range.selectedMinimum], ['<=', numeric, range.selectedMaximum]], false]
}

export function mapValueFilteredEnvelope(
  envelope: VisualizationEnvelope,
  layers: readonly VisualizationGeographicLayer[],
  ranges: ReadonlyMap<string, MapValueRange>,
): VisualizationEnvelope {
  if (envelope.dataState.kind !== 'inline') return envelope
  const filters = layers.flatMap((layer) => {
    const field = 'value' in layer ? layer.value : undefined
    const range = ranges.get(layer.id)
    if (!field || !range || !mapValueFilterExpression(field.field, range)) return []
    return [{ dataset: field.dataset, field: field.field, range }]
  })
  if (filters.length === 0) return envelope
  return {
    ...envelope,
    dataState: {
      ...envelope.dataState,
      datasets: envelope.dataState.datasets.map((dataset) => {
        const datasetFilters = filters.flatMap((filter) => {
          if (filter.dataset !== dataset.id) return []
          const index = dataset.columns.indexOf(filter.field)
          return index >= 0 ? [{ index, range: filter.range }] : []
        })
        if (datasetFilters.length === 0) return dataset
        return {
          ...dataset,
          rows: dataset.rows.filter((row) => datasetFilters.every(({ index, range }) => {
            const value = numericMapValue(row[index])
            return value !== undefined && value >= range.selectedMinimum && value <= range.selectedMaximum
          })),
        }
      }),
    },
  }
}

export function combineMapFilters(base: unknown, range: unknown[] | undefined): unknown {
  if (!range) return base ?? null
  return base ? ['all', base, range] : range
}

export function mapValueRangePercent(value: number, range: MapValueRange): number {
  return Math.max(0, Math.min(100, 100 * (value - range.minimum) / (range.maximum - range.minimum)))
}

export function formatMapRangeValue(value: number): string {
  const absolute = Math.abs(value)
  if (absolute >= 1_000_000) return `${Number((value / 1_000_000).toFixed(1))}M`
  if (absolute >= 1_000) return `${Number((value / 1_000).toFixed(1))}k`
  return Number.isInteger(value) ? String(value) : String(Number(value.toFixed(2)))
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.max(minimum, Math.min(maximum, value))
}

function numericMapValue(value: unknown): number | undefined {
  if (typeof value !== 'number' && (typeof value !== 'string' || !parseDecimal(value))) return undefined
  const numeric = Number(value)
  return Number.isFinite(numeric) ? numeric : undefined
}
