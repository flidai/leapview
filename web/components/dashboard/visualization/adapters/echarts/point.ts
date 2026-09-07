import type { VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { axis, field, formatField, inlineDataset, labelFormatter, legend, selectedDatasetSource, type EChartsTranslation } from './common'
import { applyDecisionContext } from './cartesian'
import { conditionalItemColor } from './conditional-color'
import { resolveConditionalFormat, type ConditionalFormatResult } from '../../conditional-format'
import { echartsLabelPolicy } from './label-policy'
import { categoryIdentity, type CategoryColorRegistry } from './category-colors'

type PointSpec = Extract<VisualizationEnvelope['spec'], { kind: 'point' }>
const warningPointSymbol = 'path://M0,-10 L9,8 L-9,8 Z M-1,-4 L-1,2 L1,2 L1,-4 Z M-1,4 L-1,6 L1,6 L1,4 Z'

export function pointOption(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): EChartsTranslation {
  const spec = envelope.spec as PointSpec
  const dataset = inlineDataset(envelope, spec.x.dataset)
  const labels = spec.label
    ? echartsLabelPolicy(envelope, spec.label.dataset, spec.presentation.labelPolicy, labelFormatter(envelope, spec.label, context), context)
    : { label: { show: false }, labelLayout: { hideOverlap: true } }
  const categoricalRef = spec.colorScale?.kind === 'categorical' ? spec.color : undefined
  const categories = categoricalRef ? pointCategories(envelope, categoricalRef) : []
  if (categoricalRef) categoryColors.register(envelope, categoricalRef, categories.map((category) => category.value))
  const markFill = pointMarkFill(envelope, context)
  const series = categoricalRef
    ? categories.length > 0
      ? categories.map((category) => pointCategorySeries(envelope, spec, context, categoryColors, category, labels, markFill))
      : [pointSeries(envelope, spec, labels, markFill)]
    : [pointSeries(envelope, spec, labels, markFill)]
  const option: EChartsTranslation = {
    grid: { left: 12, right: spec.colorScale?.kind === 'quantitative' ? 54 : 16, top: 16, bottom: 16, containLabel: true },
    xAxis: pointAxis(envelope, spec.x, pointAxisType(envelope, spec.x), context),
    yAxis: axis(envelope, spec.y, 'value', context, 'primary_y'),
    legend: pointLegend(spec, context, categories),
    series,
    ...(categoricalRef && dataset ? { dataset: pointCategoryDatasets(envelope, dataset, categoricalRef, categories) } : {}),
    ...(spec.colorScale?.kind === 'quantitative' && spec.color && markFill?.hasColor !== true ? {
      visualMap: {
        type: 'continuous',
        dimension: spec.color.field,
        ...pointColorDomain(envelope, spec.color, spec.colorScale.minimum, spec.colorScale.maximum),
        calculable: true,
        right: 0,
        top: 'middle',
        textStyle: { color: context.colors.muted },
        inRange: { color: context.colors.data },
      },
    } : {}),
    ...(spec.presentation.brush.length > 0 ? pointBrush(spec) : {}),
  }
  return applyDecisionContext(envelope, context, option)
}

type PointCategory = Readonly<{
  key: string
  value: unknown
  name: string
  rowIndexes: number[]
}>

type PointMarkFill = Readonly<{
  color?: (params: { value?: unknown }) => string | undefined
  symbol?: (value: unknown, params?: unknown) => string
  symbolRotate?: (value: unknown, params?: unknown) => number
  hasColor: boolean
  hasIcon: boolean
}>

function pointSeries(
  envelope: VisualizationEnvelope,
  spec: PointSpec,
  labels: EChartsTranslation,
  markFill: PointMarkFill | undefined,
): EChartsTranslation {
  return {
    id: 'series:primary:point',
    type: 'scatter',
    encode: pointEncode(spec),
    symbolSize: pointSymbolSize(envelope, spec),
    itemStyle: {
      opacity: spec.presentation.overplot === 'opacity' ? spec.presentation.opacity : 1,
      ...(markFill?.hasColor && markFill.color ? { color: markFill.color } : {}),
    },
    ...pointMarkSymbols(markFill),
    ...labels,
    label: { ...labels.label, position: 'top' },
    large: largePointMode(envelope, spec, markFill),
    largeThreshold: spec.presentation.largeThreshold,
    progressiveThreshold: spec.presentation.largeThreshold,
  }
}

function pointCategorySeries(
  envelope: VisualizationEnvelope,
  spec: PointSpec,
  context: RendererContext,
  categoryColors: CategoryColorRegistry,
  category: PointCategory,
  labels: EChartsTranslation,
  markFill: PointMarkFill | undefined,
): EChartsTranslation {
  const ref = spec.color!
  const datasetID = `dataset:point:${encodeURIComponent(category.key)}`
  return {
    id: `series:primary:point:${encodeURIComponent(category.key)}`,
    name: category.name,
    datasetId: datasetID,
    type: 'scatter',
    encode: pointEncode(spec),
    symbolSize: pointSymbolSize(envelope, spec),
    itemStyle: {
      opacity: spec.presentation.overplot === 'opacity' ? spec.presentation.opacity : 1,
      color: pointCategoryColor(envelope, ref, context, categoryColors, category.value, markFill),
    },
    ...pointMarkSymbols(markFill),
    ...labels,
    label: { ...labels.label, position: 'top' },
    large: largePointMode(envelope, spec, markFill),
    largeThreshold: spec.presentation.largeThreshold,
    progressiveThreshold: spec.presentation.largeThreshold,
    // Kept on the translation so cross-highlight and diagnostics can retain
    // the source row tuple after ECharts applies the category transform.
    __lv_source_row_indices: category.rowIndexes,
  }
}

function pointCategoryDatasets(
  envelope: VisualizationEnvelope,
  dataset: NonNullable<ReturnType<typeof inlineDataset>>,
  ref: VisualizationFieldRef,
  categories: readonly PointCategory[],
): EChartsTranslation[] {
  const sourceID = `dataset:${dataset.id}`
  return [
    { id: sourceID, source: selectedDatasetSource(envelope, dataset) },
    ...categories.map((category) => ({
      id: `dataset:point:${encodeURIComponent(category.key)}`,
      fromDatasetId: sourceID,
      transform: { type: 'filter', config: { dimension: ref.field, '=': category.value } },
    })),
  ]
}

function pointEncode(spec: PointSpec): EChartsTranslation {
  return {
    x: spec.x.field,
    y: spec.y.field,
    ...(spec.color ? { itemName: spec.color.field } : {}),
    ...(spec.series ? { itemGroupId: spec.series.field } : {}),
    ...(spec.tooltip ? { tooltip: spec.tooltip.map((item) => item.field) } : {}),
  }
}

function pointLegend(spec: PointSpec, context: RendererContext, categories: readonly PointCategory[]): EChartsTranslation | undefined {
  const result = legend(spec.presentation.legend, context, categories.length > 4)
  if (!result || !spec.colorScale || spec.colorScale.kind !== 'categorical' || !spec.color) return result
  result.data = categories.map((category) => category.name)
  result.selectedMode = 'multiple'
  return result
}

function largePointMode(envelope: VisualizationEnvelope, spec: PointSpec, markFill: PointMarkFill | undefined): boolean {
  const dataset = inlineDataset(envelope, spec.x.dataset)
  const rows = dataset?.rows ?? []
  // ECharts large scatter mode does not preserve per-datum item styling.
  if (markFill) return false
  return spec.presentation.brush.length === 0 && (
    spec.presentation.largeMode === 'always'
    || spec.presentation.largeMode === 'automatic' && rows.length >= spec.presentation.largeThreshold
  )
}

function pointMarkSymbols(markFill: PointMarkFill | undefined): EChartsTranslation {
  return markFill?.hasIcon && markFill.symbol ? { symbol: markFill.symbol, symbolRotate: markFill.symbolRotate } : {}
}

function pointMarkFill(envelope: VisualizationEnvelope, context: RendererContext): PointMarkFill | undefined {
  const format = envelope.spec.conditionalFormatting?.find((candidate) => candidate.target === 'mark_fill')
  if (!format) return undefined
  const color = conditionalItemColor(envelope, format.field, 'mark_fill', context)
  const dataset = inlineDataset(envelope, format.field.dataset)
  const hasColor = conditionalFormatStyles(format).every((style) => style.color !== undefined)
  const hasIcon = conditionalFormatStyles(format).some((style) => style.icon !== undefined)
  const symbol = dataset && hasIcon
    ? (value: unknown) => conditionalPointSymbol(dataset.columns, format, value)
    : undefined
  const symbolRotate = dataset && hasIcon
    ? (value: unknown) => conditionalPointSymbolRotation(dataset.columns, format, value)
    : undefined
  return {
    color,
    hasColor,
    hasIcon,
    ...(symbol ? { symbol } : {}),
    ...(symbolRotate ? { symbolRotate } : {}),
  }
}

function pointCategoryColor(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  context: RendererContext,
  categoryColors: CategoryColorRegistry,
  categoryValue: unknown,
  markFill: PointMarkFill | undefined,
): (params: { value?: unknown[] }) => string {
  return (params) => {
    const governed = Array.isArray(params.value) && markFill?.hasColor && markFill.color ? markFill.color(params) : undefined
    return governed ?? categoryColors.color(envelope, ref, categoryValue, context)
  }
}

function conditionalFormatStyles(format: NonNullable<PointSpec['conditionalFormatting']>[number]): readonly { color?: unknown; icon?: unknown }[] {
  const rule = format.rule
  if (rule.kind === 'gradient') return [rule.low, rule.high, rule.nullStyle]
  if (rule.kind === 'field') return [...Object.values(rule.values), rule.nullStyle, rule.defaultStyle]
  return [...rule.rules.map((candidate) => candidate.style), rule.nullStyle, rule.defaultStyle]
}

function conditionalPointResult(columns: readonly string[], format: NonNullable<PointSpec['conditionalFormatting']>[number], value: unknown): ConditionalFormatResult | undefined {
  return Array.isArray(value) ? resolveConditionalFormat(format, columns, value) : undefined
}

function conditionalPointSymbol(columns: readonly string[], format: NonNullable<PointSpec['conditionalFormatting']>[number], value: unknown): string {
  return pointSymbolForIcon(conditionalPointResult(columns, format, value)?.style.icon)
}

function conditionalPointSymbolRotation(columns: readonly string[], format: NonNullable<PointSpec['conditionalFormatting']>[number], value: unknown): number {
  return pointSymbolRotation(conditionalPointResult(columns, format, value)?.style.icon)
}

function pointSymbolForIcon(icon: NonNullable<ConditionalFormatResult['style']>['icon']): string {
  switch (icon) {
    case 'square': return 'rect'
    case 'diamond': return 'diamond'
    case 'triangle_up':
    case 'triangle_down':
      return 'triangle'
    case 'warning': return warningPointSymbol
    case 'arrow_up':
    case 'arrow_down': return 'arrow'
    case 'circle':
    default: return 'circle'
  }
}

function pointSymbolRotation(icon: NonNullable<ConditionalFormatResult['style']>['icon']): number {
  switch (icon) {
    case 'triangle_down':
    case 'arrow_down': return 180
    default: return 0
  }
}

function pointCategories(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): PointCategory[] {
  const dataset = inlineDataset(envelope, ref.dataset)
  const index = categoryValueIndex(envelope, ref)
  if (!dataset || index < 0) return []
  return orderedPointCategories(index, dataset.rows)
}

export function pointCategoryRowIndexes(
  envelope: VisualizationEnvelope,
  columns: readonly string[],
  rows: readonly (readonly unknown[])[],
): number[][] | undefined {
  const spec = envelope.spec
  if (spec.kind !== 'point' || spec.colorScale?.kind !== 'categorical' || !spec.color) return undefined
  const index = columns.indexOf(spec.color.field)
  if (index < 0) return undefined
  return orderedPointCategories(index, rows).map((category) => category.rowIndexes)
}

function orderedPointCategories(index: number, rows: readonly (readonly unknown[])[]): PointCategory[] {
  const categories = new Map<string, { value: unknown; rowIndexes: number[] }>()
  for (const [rowIndex, row] of rows.entries()) {
    const value = row[index]
    const key = categoryIdentity(value)
    const existing = categories.get(key)
    if (existing) existing.rowIndexes.push(rowIndex)
    else categories.set(key, { value, rowIndexes: [rowIndex] })
  }
  const ordered = [...categories.entries()]
    .sort(([left], [right]) => left.localeCompare(right, 'en'))
  const names = new Map<string, number>()
  for (const [, category] of ordered) {
    const name = pointCategoryName(category.value)
    names.set(name, (names.get(name) ?? 0) + 1)
  }
  return ordered.map(([key, category]) => {
    const baseName = pointCategoryName(category.value)
    if (names.get(baseName) === 1) return { ...category, key, name: baseName }
    return { ...category, key, name: `${baseName} [${categoryIdentity(category.value)}]` }
  })
}

function pointCategoryName(value: unknown): string {
  if (value === null) return '(null)'
  if (value === undefined) return '(undefined)'
  if (value === '') return '(empty)'
  return String(value)
}

function categoryValueIndex(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): number {
  return inlineDataset(envelope, ref.dataset)?.columns.indexOf(ref.field) ?? -1
}

function pointAxisType(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): 'value' | 'time' {
  const dataType = field(envelope, ref)?.dataType
  return dataType === 'temporal' || dataType === 'date' ? 'time' : 'value'
}

function pointAxis(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  type: 'value' | 'time',
  context: RendererContext,
): EChartsTranslation {
  const result = axis(envelope, ref, type, context, 'x')
  if (type !== 'time') return result
  const definition = field(envelope, ref)
  const dateFormatter = new Intl.DateTimeFormat(context.locale, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    timeZone: 'UTC',
  })
  result.splitNumber = 6
  result.axisLabel = {
    ...result.axisLabel,
    hideOverlap: true,
    formatter: (value: unknown) => {
      if (definition?.format) return formatField(envelope, ref, value, context)
      const date = new Date(value as string | number)
      return Number.isFinite(date.getTime()) ? dateFormatter.format(date) : formatField(envelope, ref, value, context)
    },
  }
  return result
}

function pointSymbolSize(envelope: VisualizationEnvelope, spec: PointSpec): number | ((value: unknown[]) => number) {
  if (!spec.size || !spec.sizeScale) return 10
  const dataset = inlineDataset(envelope, spec.size.dataset)
  const index = dataset?.columns.indexOf(spec.size.field) ?? -1
  const values = (dataset?.rows ?? []).map((row) => row[index]).filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  const minimum = spec.sizeScale.minimum ?? (values.length ? Math.min(...values) : 0)
  const maximum = spec.sizeScale.maximum ?? (values.length ? Math.max(...values) : minimum)
  return (value: unknown[]) => {
    const raw = Number(value[index])
    if (!Number.isFinite(raw) || maximum <= minimum) return spec.sizeScale!.minimumPixels
    const ratio = Math.max(0, Math.min(1, (raw - minimum) / (maximum - minimum)))
    return spec.sizeScale!.minimumPixels + ratio * (spec.sizeScale!.maximumPixels - spec.sizeScale!.minimumPixels)
  }
}

function pointColorDomain(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  authoredMinimum?: number,
  authoredMaximum?: number,
): { min: number, max: number } {
  const dataset = inlineDataset(envelope, ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  const values = (dataset?.rows ?? [])
    .map((row) => row[index])
    .filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  let min = authoredMinimum ?? (values.length ? Math.min(...values) : 0)
  let max = authoredMaximum ?? (values.length ? Math.max(...values) : 1)
  if (min < max) return { min, max }
  if (authoredMinimum !== undefined && authoredMaximum === undefined) max = min + Math.max(1, Math.abs(min) * 0.01)
  else if (authoredMaximum !== undefined && authoredMinimum === undefined) min = max - Math.max(1, Math.abs(max) * 0.01)
  else {
    const delta = Math.max(1, Math.abs(min) * 0.01)
    min -= delta
    max += delta
  }
  return { min, max }
}

function pointBrush(spec: PointSpec): EChartsTranslation {
  const toolbox = spec.presentation.brush.map((gesture) => gesture === 'rectangle' ? 'rect' : 'polygon')
  return {
    brush: { toolbox, brushMode: 'multiple', transformable: false, throttleType: 'debounce', throttleDelay: 120 },
    toolbox: { feature: { brush: { type: toolbox } }, right: 8, top: 0 },
  }
}
