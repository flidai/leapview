import type { VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { axis, inlineDataset, type EChartsTranslation } from './common'

type CartesianSpec = Extract<VisualizationEnvelope['spec'], { kind: 'cartesian' }>

export function multiMeasureComboAxes(
  envelope: VisualizationEnvelope,
  context: RendererContext,
  spec: CartesianSpec,
  horizontal: boolean,
  values: CartesianSpec['y'],
  comboByField: Map<string, NonNullable<CartesianSpec['presentation']['comboSeries']>[number]>,
  stack: string,
): { xAxis: EChartsTranslation | EChartsTranslation[]; yAxis: EChartsTranslation | EChartsTranslation[] } {
  const primaryRefs = values.filter((value) => comboByField.get(value.field)?.axis !== 'secondary')
  const secondaryRefs = values.filter((value) => comboByField.get(value.field)?.axis === 'secondary')
  const primaryRef = primaryRefs[0] ?? values[0]!
  const primaryScope = primaryRefs.length > 0 ? primaryRefs : [primaryRef]
  const primaryValueAxis = axis(envelope, primaryRef, 'value', context, 'primary_y', primaryScope)
  if (stack === 'percent') applyPercentAxis(primaryValueAxis, context)
  if (secondaryRefs.length === 0) {
    return horizontal
      ? { xAxis: primaryValueAxis, yAxis: axis(envelope, spec.x, 'category', context, 'x', [spec.x]) }
      : { xAxis: axis(envelope, spec.x, 'category', context, 'x', [spec.x]), yAxis: primaryValueAxis }
  }
  const secondaryRef = secondaryRefs[0]!
  primaryValueAxis.splitNumber = 4
  const secondaryValueAxis = axis(envelope, secondaryRef, 'value', context, 'secondary_y', secondaryRefs)
  // Both value axes retain independent scales and formatters, while aligned
  // ticks keep the primary grid authoritative and prevent duplicate lines.
  configureSecondaryComboAxis(secondaryValueAxis)
  return horizontal
    ? { xAxis: [primaryValueAxis, secondaryValueAxis], yAxis: axis(envelope, spec.x, 'category', context, 'x', [spec.x]) }
    : { xAxis: axis(envelope, spec.x, 'category', context, 'x', [spec.x]), yAxis: [primaryValueAxis, secondaryValueAxis] }
}

export function configureSecondaryComboAxis(axisOption: EChartsTranslation): void {
  axisOption.splitNumber = 4
  axisOption.alignTicks = true
  axisOption.splitLine = { ...axisOption.splitLine, show: false }
}

export function applyPercentAxis(axisOption: EChartsTranslation, context: RendererContext): void {
  const formatter = new Intl.NumberFormat(context.locale, { maximumFractionDigits: 1 })
  axisOption.axisLabel = { ...axisOption.axisLabel, formatter: (value: unknown) => typeof value === 'number' ? `${formatter.format(value)}%` : String(value) }
}

export function heatmapDataZoom(envelope: VisualizationEnvelope, spec: CartesianSpec): EChartsTranslation[] | undefined {
  if (!spec.presentation.dataZoom) return undefined
  const categories = categoricalFieldValues(envelope, spec.x)
  if (categories.length === 0) return []
  const startValue = categories[0] ?? ''
  const endValue = categories[Math.min(6, Math.max(0, categories.length - 1))] ?? startValue
  return [
    { id: 'dataZoom:heatmap:inside', type: 'inside', xAxisIndex: 0, filterMode: 'filter', startValue, endValue },
    {
      id: 'dataZoom:heatmap:slider', type: 'slider', xAxisIndex: 0, filterMode: 'filter', startValue, endValue,
      bottom: 64, height: 14, showDetail: false, brushSelect: false,
    },
  ]
}

export function categoricalFieldValues(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): string[] {
  const dataset = inlineDataset(envelope, ref.dataset)
  const fieldIndex = dataset?.columns.indexOf(ref.field) ?? -1
  if (!dataset || fieldIndex < 0) return []
  return [...new Set(dataset.rows.map((row) => String(row[fieldIndex] ?? '')))]
}

export function rawCategoricalFieldValue(envelope: VisualizationEnvelope, ref: VisualizationFieldRef, category: unknown): unknown {
  const dataset = inlineDataset(envelope, ref.dataset)
  const fieldIndex = dataset?.columns.indexOf(ref.field) ?? -1
  if (!dataset || fieldIndex < 0) return category
  const row = dataset.rows.find((candidate) => String(candidate[fieldIndex] ?? '') === String(category ?? ''))
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
    return typeof value === 'number' && Number.isFinite(value) ? [value] : []
  })
  if (values.length === 0) return { minimum: 0, maximum: 1 }
  const minimum = Math.min(...values)
  const maximum = Math.max(...values)
  if (minimum >= 0 && maximum > 0) return { minimum: 0, maximum }
  if (minimum < 0 && maximum <= 0) return { minimum, maximum: 0 }
  if (minimum !== maximum) return { minimum, maximum }
  return { minimum: 0, maximum: 1 }
}
