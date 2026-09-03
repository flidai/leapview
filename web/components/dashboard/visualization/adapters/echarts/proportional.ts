import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { formatDisplayField, formatField, inlineDataset, legend, type EChartsTranslation } from './common'
import { conditionalItemColor } from './conditional-color'
import { echartsLabelPolicy } from './label-policy'
import type { CategoryColorRegistry } from './category-colors'

const CENTER_GRAPHIC_ID = 'graphic:proportional:center'

export function proportionalOption(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): EChartsTranslation {
  const spec = envelope.spec
  if (spec.kind !== 'proportional') return {}
  const presentation = spec.presentation
  const radius = spec.mark === 'donut'
    ? [percent(presentation.innerRadius, 0.45), percent(presentation.outerRadius, 0.72)]
    : presentation.outerRadius ? percent(presentation.outerRadius, 0.72) : undefined
  const dataset = inlineDataset(envelope, spec.category.dataset)
  const categoryIndex = dataset?.columns.indexOf(spec.category.field) ?? -1
  const valueIndex = dataset?.columns.indexOf(spec.value.field) ?? -1
  const outside = presentation.labelPosition !== 'inside'
  const isPie = spec.mark === 'pie' || spec.mark === 'donut'
  const labels = echartsLabelPolicy(envelope, spec.value.dataset, presentation.labelPolicy, ({ value }) => {
    const row = Array.isArray(value) ? value : []
    const amount = formatDisplayField(envelope, spec.value, valueIndex >= 0 ? row[valueIndex] : undefined, context)
    if (!outside) return amount
    const category = formatField(envelope, spec.category, categoryIndex >= 0 ? row[categoryIndex] : undefined, context)
    return `${category}: ${amount}`
  }, context)
  const categoryValues = categoryIndex < 0 ? [] : (dataset?.rows ?? []).map((row) => row[categoryIndex])
  categoryColors.register(envelope, spec.category, categoryValues)
  const governedColor = conditionalItemColor(envelope, spec.value, 'mark_fill', context)
    ?? conditionalItemColor(envelope, spec.value, 'series_color', context)
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
      minShowLabelAngle: minimumLabelAngle(presentation.labelPolicy.density),
      labelLine: {
        show: outside,
        length: 10,
        length2: 8,
        lineStyle: { color: context.colors.muted },
      },
    } : {}),
    roseType: presentation.rose ? 'radius' : false,
    itemStyle: {
      color: governedColor ?? ((params: { value?: unknown[] }) => categoryColors.color(envelope, spec.category, Array.isArray(params.value) ? params.value[categoryIndex] : undefined, context)),
    },
  }
  if (radius !== undefined) series.radius = radius
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
  return {
    legend: legend(presentation.legend, context, true),
    series: [series],
    aria: { decal: { show: repeatsColors } },
    ...center,
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
  return new Set(values.map((value) => `${typeof value}:${String(value)}`)).size
}

function minimumLabelAngle(density: string): number {
  if (density === 'always') return 0
  if (density === 'dense') return 1
  return 3
}

function percent(value: number | undefined, fallback: number): string {
  return `${Math.round((value ?? fallback) * 10000) / 100}%`
}
