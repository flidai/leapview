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

export function conditionalFormatHasColor(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  target: VisualizationConditionalFormat['target'],
): boolean {
  const format = envelope.spec.conditionalFormatting?.find((candidate) =>
    candidate.target === target && candidate.field.dataset === ref.dataset && candidate.field.field === ref.field)
  if (!format) return false
  const rule = format.rule
  if (rule.nullStyle.color) return true
  if (rule.kind === 'gradient') return Boolean(rule.low.color || rule.high.color)
  if (rule.defaultStyle.color) return true
  if (rule.kind === 'rules') return rule.rules.some((candidate) => Boolean(candidate.style.color))
  return Object.values(rule.values).some((style) => Boolean(style.color))
}

export function heatmapDefaultColor(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  extent: { minimum: number; maximum: number },
  primary: string,
  context: RendererContext,
): (params: { value?: unknown }) => string {
  const dataset = inlineDataset(envelope, ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  return (params) => {
    const raw = Array.isArray(params.value) && index >= 0 ? params.value[index] : params.value
    if (typeof raw !== 'number' || !Number.isFinite(raw)) return context.colors.muted
    const ratio = extent.maximum === extent.minimum
      ? 1
      : Math.min(1, Math.max(0, (raw - extent.minimum) / (extent.maximum - extent.minimum)))
    return heatmapValueColor(primary, ratio)
  }
}

function heatmapValueColor(primary: string, ratio: number): string {
  const longHex = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(primary)
  const shortHex = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(primary)
  const channels = longHex
    ? longHex.slice(1).map((channel) => Number.parseInt(channel, 16))
    : shortHex
      ? shortHex.slice(1).map((channel) => Number.parseInt(channel + channel, 16))
      : undefined
  if (!channels) return primary
  const alpha = Math.round((0.18 + 0.82 * ratio) * 100) / 100
  return `rgba(${channels[0]}, ${channels[1]}, ${channels[2]}, ${alpha})`
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
