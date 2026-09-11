import type { VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { formatDisplayField, formatField, inlineDataset, legendDecoration, type EChartsTranslation } from './common'
import { conditionalIconGlyph, resolveConditionalFormat } from '../../conditional-format'
import { conditionalItemColor } from './conditional-color'
import { echartsLabelPolicy } from './label-policy'
import { categoryIdentity, type CategoryColorRegistry } from './category-colors'
import { conditionalColorWithFallback } from './series-intent'
import { chartCueFontStack, proportionalConditionalCueFormat } from '../../cue-font'

export { proportionalConditionalCueFormat } from '../../cue-font'

const CENTER_GRAPHIC_ID = 'graphic:proportional:center'

export function proportionalOption(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): EChartsTranslation {
  const spec = envelope.spec
  if (spec.kind !== 'proportional') return {}
  const presentation = spec.presentation
  const isPie = spec.mark === 'pie' || spec.mark === 'donut'
  const radius = spec.mark === 'donut'
    ? [percent(presentation.innerRadius, 0.45), percent(presentation.outerRadius, 0.72)]
    : spec.mark === 'pie' && presentation.outerRadius !== undefined ? percent(presentation.outerRadius, 0.72) : undefined
  const dataset = inlineDataset(envelope, spec.category.dataset)
  const categoryIndex = dataset?.columns.indexOf(spec.category.field) ?? -1
  const valueIndex = dataset?.columns.indexOf(spec.value.field) ?? -1
  const outside = presentation.labelPosition !== 'inside'
  const conditionalCue = proportionalConditionalCueFormat(envelope, spec.value)
  const compactOutsideCueLabels = conditionalCue !== undefined && outside && isPie && presentation.legend === 'bottom'
  const labels = echartsLabelPolicy(envelope, spec.value.dataset, presentation.labelPolicy, ({ value }) => {
    const row = Array.isArray(value) ? value : []
    const cue = conditionalCue && dataset ? resolveConditionalFormat(conditionalCue, dataset.columns, row).style.icon : undefined
    const amount = formatDisplayField(envelope, spec.value, valueIndex >= 0 ? row[valueIndex] : undefined, context)
    if (!outside) return [cue ? conditionalIconGlyph(cue) : '', amount].filter(Boolean).join(' ')
    const category = formatField(envelope, spec.category, categoryIndex >= 0 ? row[categoryIndex] : undefined, context)
    if (compactOutsideCueLabels) {
      const cueCategory = [cue ? conditionalIconGlyph(cue) : '', category].filter(Boolean).join(' ')
      return [cueCategory, amount].filter(Boolean).join('\n')
    }
    return [cue ? conditionalIconGlyph(cue) : '', `${category}: ${amount}`].filter(Boolean).join(' ')
  }, context)
  // Proportional sectors have no independent icon channel. If an authored
  // conditional outcome carries a cue, retain a truthful visible label even
  // when density, overlap, or sector-angle policies would otherwise suppress it.
  if (conditionalCue) {
    labels.label.show = true
    labels.labelLayout = { hideOverlap: false }
    if (isPie) labels.label.fontFamily = chartCueFontStack(context.fontFamily)
    // Keep the authored cue and formatted value together when native pie
    // layout narrows an outside label. ECharts wraps instead of truncating,
    // then uses the extra leader-line segment to keep the text off the ring.
    if (outside && isPie && presentation.legend === 'bottom') labels.label.overflow = 'break'
  }
  const categoryValues = categoryIndex < 0 ? [] : (dataset?.rows ?? []).map((row) => row[categoryIndex])
  categoryColors.register(envelope, spec.category, categoryValues)
  const markFill = conditionalItemColor(envelope, spec.value, 'mark_fill', context)
  const seriesFill = conditionalItemColor(envelope, spec.value, 'series_color', context)
  const categoryColor = (params: { value?: unknown }) => categoryColors.color(
    envelope,
    spec.category,
    Array.isArray(params.value) ? params.value[categoryIndex] : undefined,
    context,
  )
  // Icon-only conditional outcomes intentionally have no fill color. Keep
  // the normal, category-stable color for those rows instead of suppressing
  // the sector color with an undefined itemStyle callback result.
  const itemColor = markFill || seriesFill
    ? conditionalColorWithFallback(markFill, conditionalColorWithFallback(seriesFill, categoryColor))
    : categoryColor
  const categories = proportionalCategories(categoryValues, envelope, spec.category, context)
  const typedItemNames = categories.some((category, index) => categories.findIndex((candidate) => candidate.rawName === category.rawName) !== index)
  const series: EChartsTranslation = {
    id: `series:primary:${spec.mark}`, type: spec.mark === 'funnel' ? 'funnel' : 'pie',
    encode: { itemName: spec.category.field, value: spec.value.field },
    ...labels,
    label: {
      ...labels.label,
      position: outside ? 'outside' : 'inside',
      ...(outside && isPie ? { alignTo: 'edge', edgeDistance: 8, distanceToLabelLine: 4 } : {}),
    },
    ...(isPie ? {
      avoidLabelOverlap: true,
      minShowLabelAngle: conditionalCue ? 0 : minimumLabelAngle(presentation.labelPolicy.density),
      labelLine: {
        show: outside,
        length: 10,
        length2: 8,
        lineStyle: { color: context.colors.muted },
      },
    } : {}),
    ...(isPie ? { roseType: presentation.rose ? 'radius' : false } : {}),
    itemStyle: {
      color: itemColor,
    },
    // Explicit item names keep ECharts' legend and interaction events typed
    // when values such as 1, "1", and null stringify to the same name.
    ...(typedItemNames && dataset && categoryIndex >= 0 ? {
      data: dataset.rows.map((row, rowIndex) => {
        const category = categories.find((candidate) => candidate.identity === categoryIdentity(row[categoryIndex]))
        return {
          value: row,
          ...(category ? { name: category.name } : {}),
          __lv_dataset: dataset.id,
          __lv_row_index: rowIndex,
        }
      }),
    } : {}),
  }
  if (radius !== undefined) series.radius = radius
  if (presentation.legendTitle !== undefined) {
    if (presentation.legend === 'left') series.left = '12%'
    if (presentation.legend === 'right') series.right = '12%'
    if (presentation.legend === 'top') series.top = '12%'
    if (presentation.legend === 'bottom') series.bottom = '12%'
  }
  if (spec.mark === 'funnel') {
    series.orient = presentation.orientation
    if (outside && presentation.orientation === 'vertical') {
      series.left = '6%'
      series.right = '44%'
    }
    if (presentation.align !== undefined) series.funnelAlign = presentation.align
    series.sort = presentation.sort === 'ascending' ? 'ascending' : presentation.sort === 'descending' ? 'descending' : 'none'
  }
  const centerText = proportionalCenterText(envelope, context)
  const center = centerText === undefined ? {} : {
    graphic: [{
      id: CENTER_GRAPHIC_ID,
      type: 'text',
      left: 'center',
      top: 'middle',
      silent: true,
      style: {
        text: centerText,
        fill: context.colors.foreground,
        fontFamily: context.fontFamily,
        fontSize: 12,
        fontWeight: 600,
        lineHeight: 16,
        textAlign: 'center',
        textVerticalAlign: 'middle',
        rich: {
          centerValue: {
            fontSize: 18,
            fontWeight: 600,
            lineHeight: 22,
          },
          centerLabel: {
            fontSize: 13,
            fontWeight: 500,
            lineHeight: 18,
          },
        },
      },
    }],
  }
  const repeatsColors = uniqueValueCount(categoryValues) > context.colors.data.length
  const decoration = legendDecoration(presentation.legend, context, true, presentation, categories.map((category) => ({ value: category.name, name: category.label })))
  applyProportionalLegend(decoration.legend, presentation.legendItems, categories)
  const graphics = [...(decoration.graphic ?? []), ...(center.graphic ?? [])]
  return {
    ...decoration,
    ...(graphics.length ? { graphic: graphics } : {}),
    series: [series],
    aria: { decal: { show: repeatsColors } },
  }
}

export function proportionalCenterText(envelope: VisualizationEnvelope, context: RendererContext, activeRow?: readonly unknown[]): string | undefined {
  const spec = envelope.spec
  if (spec.kind !== 'proportional' || spec.mark !== 'donut') return undefined
  const dataset = inlineDataset(envelope, spec.value.dataset)
  const categoryIndex = dataset?.columns.indexOf(spec.category.field) ?? -1
  const valueIndex = dataset?.columns.indexOf(spec.value.field) ?? -1
  if (activeRow) {
    const category = formatField(envelope, spec.category, categoryIndex >= 0 ? activeRow[categoryIndex] : undefined, context)
    const amount = formatDisplayField(envelope, spec.value, valueIndex >= 0 ? activeRow[valueIndex] : undefined, context)
    return proportionalCenterValueText(amount, category)
  }
  if (spec.presentation.centerLabel) return spec.presentation.centerLabel
  const total = (dataset?.rows ?? []).reduce((sum, row) => {
    const value = valueIndex >= 0 ? Number(row[valueIndex]) : Number.NaN
    return Number.isFinite(value) ? sum + value : sum
  }, 0)
  return proportionalCenterValueText(formatDisplayField(envelope, spec.value, total, context), 'Total')
}

function proportionalCenterValueText(value: string, label: string): string {
  return `{centerValue|${escapeRichText(value)}}\n{centerLabel|${escapeRichText(label)}}`
}

function escapeRichText(value: string): string {
  return value.replaceAll('\\', '\\\\').replaceAll('{', '\\{').replaceAll('}', '\\}')
}

function uniqueValueCount(values: readonly unknown[]): number {
  return new Set(values.map(categoryIdentity)).size
}

function minimumLabelAngle(density: string): number {
  if (density === 'always') return 0
  if (density === 'dense') return 1
  return 3
}

function applyProportionalLegend(
  legend: EChartsTranslation | undefined,
  configuredItems: NonNullable<Extract<VisualizationEnvelope['spec'], { kind: 'proportional' }>['presentation']['legendItems']> | undefined,
  categories: readonly ProportionalCategory[],
): void {
  if (!legend) return
  const configured = configuredItems?.flatMap((item) => {
    const matches = categories.filter((category) => category.name === item.value || category.rawName === item.value || category.label === item.value)
    return matches.length === 1 ? [{ item, category: matches[0]! }] : []
  }) ?? []
  const configuredValues = new Set(configured.map(({ category }) => category.identity))
  const ordered = configured.length > 0
    ? [...configured.map(({ category }) => category), ...categories.filter((category) => !configuredValues.has(category.identity))]
    : categories
  legend.data = ordered.map((category) => ({ name: category.name }))
  const labels = new Map(configured.map(({ item, category }) => [category.name, item.label ?? category.label]))
  legend.formatter = (value: string) => labels.get(value) ?? categories.find((category) => category.name === value)?.label ?? value
}

type ProportionalCategory = Readonly<{
  identity: string
  rawName: string
  name: string
  label: string
  value: unknown
}>

export function proportionalCategories(
  values: readonly unknown[],
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  context: RendererContext,
): ProportionalCategory[] {
  const unique = new Map<string, unknown>()
  for (const value of values) {
    const identity = categoryIdentity(value)
    if (!unique.has(identity)) unique.set(identity, value)
  }
  const categories = [...unique.entries()].map(([identity, value]) => ({
    identity,
    rawName: String(value),
    name: String(value),
    label: formatField(envelope, ref, value, context),
    value,
  }))
  const names = new Map<string, number>()
  const labels = new Map<string, number>()
  for (const category of categories) {
    names.set(category.rawName, (names.get(category.rawName) ?? 0) + 1)
    labels.set(category.label, (labels.get(category.label) ?? 0) + 1)
  }
  return categories.map((category) => ({
    ...category,
    name: names.get(category.rawName) === 1 ? category.rawName : `${category.rawName} [${category.identity}]`,
    label: labels.get(category.label) === 1 ? category.label : `${category.label} [${category.identity}]`,
  }))
}

function percent(value: number | undefined, fallback: number): string {
  return `${Math.round((value ?? fallback) * 10000) / 100}%`
}
