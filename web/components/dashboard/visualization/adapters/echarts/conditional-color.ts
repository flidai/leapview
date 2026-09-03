import type { VisualizationColorIntent, VisualizationConditionalFormat, VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { conditionalStyleColor, resolveConditionalFormat } from '../../conditional-format'
import { inlineDataset } from './common'

export function seriesColor(value: string, intent: VisualizationColorIntent | undefined, context: RendererContext): string {
  switch (intent) {
    case 'accent': return context.colors.accent
    case 'neutral': return context.colors.muted
    case 'ink': return context.colors.foreground
    case 'success': return context.colors.success
    case 'warning': return context.colors.attention
    case 'danger': return context.colors.danger
  }
  if (intent?.startsWith('data_')) {
    const index = Number(intent.slice(5)) - 1
    if (Number.isInteger(index) && context.colors.data.length > 0) return context.colors.data[index % context.colors.data.length]!
  }
  let hash = 2166136261
  for (let index = 0; index < value.length; index++) hash = Math.imul(hash ^ value.charCodeAt(index), 16777619)
  return context.colors.data.length > 0 ? context.colors.data[(hash >>> 0) % context.colors.data.length]! : context.colors.accent
}

export function conditionalItemColor(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  target: VisualizationConditionalFormat['target'],
  context: RendererContext,
): ((params: { value?: unknown }) => string | undefined) | undefined {
  const format = envelope.spec.conditionalFormatting?.find((candidate) =>
    candidate.target === target && candidate.field.dataset === ref.dataset && candidate.field.field === ref.field)
  if (!format) return undefined
  return (params) => {
    if (!Array.isArray(params.value)) return undefined
    const dataset = inlineDataset(envelope, format.field.dataset)
    const result = dataset ? resolveConditionalFormat(format, dataset.columns, params.value) : undefined
    return result ? conditionalStyleColor(result.style, (intent) => seriesColor('', intent, context)) : undefined
  }
}

export function conditionalCategoryColor(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  category: VisualizationFieldRef,
  value: unknown,
  target: VisualizationConditionalFormat['target'],
  context: RendererContext,
): string | undefined {
  const format = envelope.spec.conditionalFormatting?.find((candidate) =>
    candidate.target === target && candidate.field.dataset === ref.dataset && candidate.field.field === ref.field)
  if (!format || format.rule.kind !== 'field') return undefined
  if (format.rule.source.dataset !== category.dataset || format.rule.source.field !== category.field) return undefined
  const style = value === null || value === undefined
    ? format.rule.nullStyle
    : format.rule.values[String(value)] ?? format.rule.defaultStyle
  return conditionalStyleColor(style, (intent) => seriesColor('', intent, context))
}
