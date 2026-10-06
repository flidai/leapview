import type { CartesianVisualizationSpec, VisualizationAxisConfiguration, VisualizationEnvelope, VisualizationFieldRef } from '../../generated/visualization'
import { categoryIdentity } from '../dashboard/visualization/adapters/echarts/category-colors'

export type ExplorerVisualizationPage = { key: string; index: number }
type CategoryPage = ExplorerVisualizationPage & { count: number; first: number; last: number; total: number }
type ExplorerVisualization = { envelope: VisualizationEnvelope; minimumHeight: number; categoryPage?: CategoryPage }
const chartChromeHeight = 128
const maximumChartHeight = 4096

/** Keep Explorer layout and naming separate from the governed result envelope. */
export function explorerVisualization(envelope: VisualizationEnvelope, title: string, requestedPage?: ExplorerVisualizationPage): ExplorerVisualization {
  // Other transports retain their server identity, which may be needed to
  // request additional data. Explorer results are inline frames.
  if (envelope.dataState.kind !== 'inline') return { envelope, minimumHeight: 0 }
  const layout = horizontalBarLayout(envelope)
  const key = JSON.stringify([envelope.visualID, envelope.specRevision, envelope.dataRevision, envelope.dataState.generation])
  const count = layout ? Math.ceil(layout.categories.length / layout.pageSize) : 0
  const requestedIndex = requestedPage?.key === key && Number.isFinite(requestedPage.index) ? Math.trunc(requestedPage.index) : 0
  const index = Math.max(0, Math.min(requestedIndex, count - 1))
  const first = layout ? index * layout.pageSize : 0
  const visibleCategories = layout?.categories.slice(first, first + layout.pageSize)
  const visibleCategorySet = new Set(visibleCategories)
  const categoryPage = layout && count > 1
    ? { key, index, count, first: first + 1, last: first + visibleCategories!.length, total: layout.categories.length }
    : undefined
  const minimumHeight = layout ? chartChromeHeight + visibleCategories!.length * layout.rowHeight : 0
  const specRevision = JSON.stringify([envelope.specRevision, 'explorer-presentation', title, index])
  const spec = categoryPage ? withGlobalValueAxis(envelope) : envelope.spec
  return {
    minimumHeight,
    categoryPage,
    envelope: {
      ...envelope,
      specRevision,
      spec: {
        ...spec,
        title,
        accessibility: { ...envelope.spec.accessibility, title },
        ...(envelope.spec.metadataBindings ? { metadataBindings: { ...envelope.spec.metadataBindings, title: undefined } } : {}),
      },
      dataState: {
        ...envelope.dataState,
        specRevision,
        datasets: envelope.dataState.datasets.map((dataset) => ({
          ...dataset,
          specRevision,
          // Slice categories, never rows: every series belonging to a category
          // stays together. The original governed frame remains untouched.
          rows: categoryPage && dataset.id === layout!.dataset
            ? dataset.rows.filter((row) => visibleCategorySet.has(categoryIdentity(row[layout!.fieldIndex])))
            : dataset.rows,
        })),
      },
    },
  }
}

function horizontalBarLayout(envelope: VisualizationEnvelope) {
  const spec = envelope.spec
  if (spec.kind !== 'cartesian' || (spec.mark !== 'bar' && spec.mark !== 'column') || envelope.dataState.kind !== 'inline') return undefined
  const orientation = spec.presentation.orientation ?? (spec.mark === 'bar' ? 'horizontal' : 'vertical')
  if (orientation !== 'horizontal') return undefined
  const field = spec.datasets.find((dataset) => dataset.id === spec.x.dataset)?.fields.find((field) => field.id === spec.x.field)
  if (field?.dataType === 'temporal' || field?.dataType === 'date') return undefined
  const dataset = envelope.dataState.datasets.find((dataset) => dataset.id === spec.x.dataset)
  const fieldIndex = dataset?.columns.indexOf(spec.x.field) ?? -1
  if (!dataset || fieldIndex < 0) return undefined
  const categories = [...new Set(dataset.rows.map((row) => categoryIdentity(row[fieldIndex])))]
  if (categories.length === 0) return undefined
  const stacked = (spec.presentation.stacking ?? (spec.presentation.stacked ? 'normal' : 'none')) !== 'none'
  const seriesCount = stacked ? 1 : spec.series ? distinctFieldCount(envelope, spec.series) : spec.y.length
  const rowHeight = Math.min(maximumChartHeight - chartChromeHeight, Math.max(32, 12 + seriesCount * 16))
  // Bound both categories and physical canvas size, including grouped bars.
  // This avoids oversized high-DPI canvases for large governed result limits.
  const pageSize = Math.max(1, Math.min(50, Math.floor((maximumChartHeight - chartChromeHeight) / rowHeight)))
  return { dataset: dataset.id, fieldIndex, categories, rowHeight, pageSize }
}

function distinctFieldCount(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): number {
  if (envelope.dataState.kind !== 'inline') return 0
  const dataset = envelope.dataState.datasets.find((dataset) => dataset.id === ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  if (!dataset || index < 0) return 0
  return new Set(dataset.rows.map((row) => categoryIdentity(row[index]))).size
}

// Pagination must not change the meaning of a bar's length. Resolve automatic
// value bounds once from the full result, retaining authored axis settings.
function withGlobalValueAxis(envelope: VisualizationEnvelope): VisualizationEnvelope['spec'] {
  const spec = envelope.spec
  if (spec.kind !== 'cartesian' || envelope.dataState.kind !== 'inline') return spec
  const dataset = envelope.dataState.datasets.find((dataset) => dataset.id === spec.x.dataset)
  if (!dataset || spec.y.some((ref) => ref.dataset !== dataset.id)) return spec
  const categoryIndex = dataset.columns.indexOf(spec.x.field)
  const valueIndices = spec.y.map((ref) => dataset.columns.indexOf(ref.field))
  if (categoryIndex < 0 || valueIndices.some((index) => index < 0)) return spec
  const authored = spec.axes?.find((axis) => axis.id === 'primary_y')
  if (authored?.minimum !== undefined && authored.maximum !== undefined) return spec
  const stack = spec.presentation.stacking ?? (spec.presentation.stacked ? 'normal' : 'none')
  const totals = new Map<string, { positive: number; negative: number }>()
  const values: number[] = []
  for (const row of dataset.rows) {
    const category = categoryIdentity(row[categoryIndex])
    const total = totals.get(category) ?? { positive: 0, negative: 0 }
    for (const index of valueIndices) {
      const raw = row[index]
      const value = typeof raw === 'number' ? raw : typeof raw === 'string' && raw.trim() !== '' ? Number(raw) : Number.NaN
      if (!Number.isFinite(value)) continue
      values.push(value)
      if (value >= 0) total.positive += value
      else total.negative += value
    }
    totals.set(category, total)
  }
  const extentValues = stack === 'none' ? values : [...totals.values()].flatMap(({ positive, negative }) => [
    stack === 'percent' && positive > 0 ? 100 : positive,
    stack === 'percent' && negative < 0 ? -100 : negative,
  ])
  const logarithmic = authored?.scale === 'log'
  const validValues = extentValues.filter((value) => Number.isFinite(value) && (!logarithmic || value > 0))
  if (validValues.length === 0) return spec
  const includeZero = !logarithmic && (stack === 'percent' || authored?.zero !== 'exclude')
  let minimum = validValues.reduce((lowest, value) => Math.min(lowest, value), includeZero ? 0 : Infinity)
  let maximum = validValues.reduce((highest, value) => Math.max(highest, value), includeZero ? 0 : -Infinity)
  minimum = authored?.minimum ?? minimum
  maximum = authored?.maximum ?? maximum
  if (minimum >= maximum) {
    if (authored?.maximum !== undefined && authored.minimum === undefined) {
      minimum = logarithmic ? maximum / 10 : maximum - (Math.abs(maximum) * 0.05 || 1)
    } else {
      if (authored?.minimum === undefined && (!includeZero || minimum < 0)) minimum = logarithmic ? minimum / 10 : minimum - (Math.abs(minimum) * 0.05 || 1)
      maximum = logarithmic ? Math.max(maximum, minimum) * 10 : Math.max(maximum, minimum) + (Math.abs(minimum) * 0.05 || 1)
    }
  }
  const axis: VisualizationAxisConfiguration = {
    id: 'primary_y', type: 'automatic', scale: 'automatic', zero: 'automatic', inversion: 'automatic',
    tickDensity: 'automatic', ticks: 'automatic', grid: 'automatic', labelRotation: 'automatic', dateUnit: 'automatic',
    ...authored, minimum, maximum,
  }
  const axes = spec.axes?.some((axis) => axis.id === 'primary_y')
    ? spec.axes.map((existing) => existing.id === 'primary_y' ? axis : existing)
    : [...(spec.axes ?? []), axis]
  return { ...spec, axes } satisfies CartesianVisualizationSpec
}
