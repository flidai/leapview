import type { VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { axis, inlineDataset, type EChartsTranslation } from './common'
import { categoryIdentity } from './category-colors'
import { parseDecimal } from '../../decimal'

type CartesianSpec = Extract<VisualizationEnvelope['spec'], { kind: 'cartesian' }>

export function multiMeasureComboAxes(
  envelope: VisualizationEnvelope,
  context: RendererContext,
  spec: CartesianSpec,
  horizontal: boolean,
  values: CartesianSpec['y'],
  comboByField: Map<string, NonNullable<CartesianSpec['presentation']['comboSeries']>[number]>,
  stack: string,
  resolveAxisType: (ref: VisualizationFieldRef, fallback: 'category' | 'value') => 'category' | 'value' | 'time' = (_ref, fallback) => fallback,
): { xAxis: EChartsTranslation | EChartsTranslation[]; yAxis: EChartsTranslation | EChartsTranslation[] } {
  const primaryRefs = values.filter((value) => comboByField.get(value.field)?.axis !== 'secondary')
  const secondaryRefs = values.filter((value) => comboByField.get(value.field)?.axis === 'secondary')
  const primaryRef = primaryRefs[0] ?? values[0]!
  const primaryScope = primaryRefs.length > 0 ? primaryRefs : [primaryRef]
  const primaryValueAxis = axis(envelope, primaryRef, resolveAxisType(primaryRef, 'value'), context, 'primary_y', primaryScope)
  if (stack === 'percent') applyPercentAxis(primaryValueAxis, context)
  if (secondaryRefs.length === 0) {
    return horizontal
      ? { xAxis: primaryValueAxis, yAxis: axis(envelope, spec.x, resolveAxisType(spec.x, 'category'), context, 'x', [spec.x]) }
      : { xAxis: axis(envelope, spec.x, resolveAxisType(spec.x, 'category'), context, 'x', [spec.x]), yAxis: primaryValueAxis }
  }
  const secondaryRef = secondaryRefs[0]!
  primaryValueAxis.splitNumber = 4
  const secondaryValueAxis = axis(envelope, secondaryRef, resolveAxisType(secondaryRef, 'value'), context, 'secondary_y', secondaryRefs)
  // Both value axes retain independent scales and formatters, while aligned
  // ticks keep the primary grid authoritative and prevent duplicate lines.
  configureSecondaryComboAxis(secondaryValueAxis)
  return horizontal
    ? { xAxis: [primaryValueAxis, secondaryValueAxis], yAxis: axis(envelope, spec.x, resolveAxisType(spec.x, 'category'), context, 'x', [spec.x]) }
    : { xAxis: axis(envelope, spec.x, resolveAxisType(spec.x, 'category'), context, 'x', [spec.x]), yAxis: [primaryValueAxis, secondaryValueAxis] }
}

export function configureSecondaryComboAxis(axisOption: EChartsTranslation): void {
  axisOption.splitNumber = 4
  axisOption.alignTicks = true
  if (axisOption.splitLine?.show !== true) axisOption.splitLine = { ...axisOption.splitLine, show: false }
}

export function hideCartesianAxes(option: EChartsTranslation): EChartsTranslation {
  const hidden = (axis: unknown): unknown => Array.isArray(axis)
    ? axis.map((value) => ({ ...(value as EChartsTranslation), show: false }))
    : axis && typeof axis === 'object' ? { ...(axis as EChartsTranslation), show: false } : axis
  return { ...option, xAxis: hidden(option.xAxis), yAxis: hidden(option.yAxis) }
}

export function applyPercentAxis(axisOption: EChartsTranslation, context: RendererContext): void {
  const formatter = new Intl.NumberFormat(context.locale, { maximumFractionDigits: 1 })
  axisOption.axisLabel = { ...axisOption.axisLabel, formatter: (value: unknown) => typeof value === 'number' ? `${formatter.format(value)}%` : String(value) }
}

export function heatmapDataZoom(envelope: VisualizationEnvelope, spec: CartesianSpec): EChartsTranslation[] | undefined {
  if (!spec.presentation.dataZoom) return undefined
  const categories = categoricalFieldValues(envelope, spec.x)
  if (categories.length === 0) return []
  const endIndex = Math.min(6, Math.max(0, categories.length - 1))
  const stringDomain = categories.every((category) => typeof category === 'string')
  const startValue = stringDomain ? categories[0] ?? '' : 0
  const endValue = stringDomain ? categories[endIndex] ?? startValue : endIndex
  return [
    { id: 'dataZoom:heatmap:inside', type: 'inside', xAxisIndex: 0, filterMode: 'filter', startValue, endValue },
    {
      id: 'dataZoom:heatmap:slider', type: 'slider', xAxisIndex: 0, filterMode: 'filter', startValue, endValue,
      bottom: 64, height: 14, showDetail: false, brushSelect: false,
    },
  ]
}

export function categoricalFieldValues(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): unknown[] {
  const dataset = inlineDataset(envelope, ref.dataset)
  const fieldIndex = dataset?.columns.indexOf(ref.field) ?? -1
  if (!dataset || fieldIndex < 0) return []
  const seen = new Set<string>()
  return dataset.rows.flatMap((row) => {
    const value = row[fieldIndex]
    const identity = categoryIdentity(value)
    if (seen.has(identity)) return []
    seen.add(identity)
    return [value]
  })
}

export function rawCategoricalFieldValue(envelope: VisualizationEnvelope, ref: VisualizationFieldRef, category: unknown): unknown {
  const dataset = inlineDataset(envelope, ref.dataset)
  const fieldIndex = dataset?.columns.indexOf(ref.field) ?? -1
  if (!dataset || fieldIndex < 0) return category
  const identity = categoryIdentity(category)
  const row = dataset.rows.find((candidate) => categoryIdentity(candidate[fieldIndex]) === identity)
  return row ? row[fieldIndex] : category
}

export function humanizeCategoryLabel(value: unknown): string {
  return String(value ?? '').replaceAll('_', ' ')
}

export function colorWithAlpha(color: string, alpha: number): string {
  const longHex = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(color)
  if (longHex) return `rgba(${Number.parseInt(longHex[1]!, 16)}, ${Number.parseInt(longHex[2]!, 16)}, ${Number.parseInt(longHex[3]!, 16)}, ${alpha})`
  const shortHex = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(color)
  if (shortHex) return `rgba(${Number.parseInt(shortHex[1]! + shortHex[1]!, 16)}, ${Number.parseInt(shortHex[2]! + shortHex[2]!, 16)}, ${Number.parseInt(shortHex[3]! + shortHex[3]!, 16)}, ${alpha})`
  return color
}

export function finiteFieldExtent(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): { minimum: number; maximum: number } {
  const dataset = inlineDataset(envelope, ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  const values = index < 0 ? [] : (dataset?.rows ?? []).flatMap((row) => {
    const value = row[index]
    const numeric = finiteNumericValue(value)
    return numeric === undefined ? [] : [numeric]
  })
  if (values.length === 0) return { minimum: 0, maximum: 1 }
  const minimum = Math.min(...values)
  const maximum = Math.max(...values)
  if (minimum >= 0 && maximum > 0) return { minimum: 0, maximum }
  if (minimum < 0 && maximum <= 0) return { minimum, maximum: 0 }
  if (minimum !== maximum) return { minimum, maximum }
  return { minimum: 0, maximum: 1 }
}

function finiteNumericValue(value: unknown): number | undefined {
  if (typeof value === 'number') return Number.isFinite(value) ? value : undefined
  if (typeof value !== 'string' || !parseDecimal(value)) return undefined
  const numeric = Number(value)
  return Number.isFinite(numeric) && (numeric !== 0 || /^-?0(?:\.0*)?$/.test(value)) ? numeric : undefined
}
