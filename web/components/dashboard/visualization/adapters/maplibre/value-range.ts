import type { VisualizationEnvelope, VisualizationGeographicLayer } from '../../../../../generated/visualization'

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
      const value = Number(row[index])
      if (Number.isFinite(value)) values.push(value)
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
  const step = definition?.dataType === 'integer' ? 1 : Math.max((maximum - minimum) / 100, Number.EPSILON)
  return {
    minimum,
    maximum,
    selectedMinimum: clamp(previous?.selectedMinimum ?? minimum, minimum, maximum),
    selectedMaximum: clamp(previous?.selectedMaximum ?? maximum, minimum, maximum),
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
  return ['all', ['has', property], ['>=', ['get', property], range.selectedMinimum], ['<=', ['get', property], range.selectedMaximum]]
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
            const value = Number(row[index])
            return Number.isFinite(value) && value >= range.selectedMinimum && value <= range.selectedMaximum
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
