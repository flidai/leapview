import type {
  VisualizationColorIntent,
  VisualizationComparisonOperator,
  VisualizationConditionalFormat,
  VisualizationConditionalStyle,
  VisualizationIconIntent,
} from '../../../generated/visualization'
import { parseDecimal } from './decimal'

export type ResolvedConditionalStyle = Readonly<{
  color?: VisualizationColorIntent
  icon?: VisualizationIconIntent
  gradient?: Readonly<{
    low: VisualizationColorIntent
    high: VisualizationColorIntent
    ratio: number
  }>
}>

export type ConditionalFormatOutcome = 'matched' | 'default' | 'null' | 'invalid'

export type ConditionalFormatResult = Readonly<{
  style: ResolvedConditionalStyle
  outcome: ConditionalFormatOutcome
  diagnostic?: string
}>

export function conditionalStyleColor(
  style: ResolvedConditionalStyle,
  resolve: (intent: VisualizationColorIntent) => string,
): string | undefined {
  if (style.color) return resolve(style.color)
  if (!style.gradient) return undefined
  const low = resolve(style.gradient.low)
  const high = resolve(style.gradient.high)
  const lowRGB = parseColor(low)
  const highRGB = parseColor(high)
  if (!lowRGB || !highRGB) return style.gradient.ratio < 0.5 ? low : high
  const ratio = style.gradient.ratio
  const channel = (index: number) => Math.round(lowRGB[index]! + (highRGB[index]! - lowRGB[index]!) * ratio)
  return `rgb(${channel(0)} ${channel(1)} ${channel(2)})`
}

export function contrastTextColor(background: string, candidates: readonly string[]): string {
  const backgroundRGB = parseColor(background)
  if (!backgroundRGB || candidates.length === 0) return candidates[0] ?? '#000000'
  return [...candidates].sort((left, right) => contrastRatio(backgroundRGB, parseColor(right)) - contrastRatio(backgroundRGB, parseColor(left)))[0]!
}

export function conditionalIconGlyph(icon: VisualizationIconIntent | undefined): string {
  switch (icon) {
    case 'circle': return '●'
    case 'square': return '■'
    case 'diamond': return '◆'
    case 'triangle_up': return '▲'
    case 'triangle_down': return '▼'
    case 'arrow_up': return '↑'
    case 'arrow_down': return '↓'
    case 'warning': return '⚠'
    default: return ''
  }
}

export function resolveConditionalFormat(
  format: VisualizationConditionalFormat,
  columns: readonly string[],
  row: readonly unknown[],
): ConditionalFormatResult {
  const rule = format.rule
  if (rule.kind === 'field') {
    const index = columns.indexOf(rule.source.field)
    if (index < 0) return invalid(format, rule.defaultStyle, 'source field is unavailable')
    const value = row[index]
    if (value === null || value === undefined) return { style: resolvedStyle(rule.nullStyle), outcome: 'null' }
    if (typeof value !== 'string' && typeof value !== 'number' && typeof value !== 'boolean') {
      return invalid(format, rule.defaultStyle, 'expected a scalar bound-field value')
    }
    const style = rule.values[String(value)]
    return style
      ? { style: resolvedStyle(style), outcome: 'matched' }
      : { style: resolvedStyle(rule.defaultStyle), outcome: 'default' }
  }

  const index = columns.indexOf(format.field.field)
  if (index < 0) {
    const fallback = rule.nullStyle
    return invalid(format, fallback, 'target field is unavailable')
  }
  const value = row[index]
  if (value === null || value === undefined) return { style: resolvedStyle(rule.nullStyle), outcome: 'null' }
  const numericValue = conditionalNumericValue(value)
  if (numericValue === undefined) {
    return invalid(format, rule.nullStyle, 'expected a finite numeric value')
  }

  if (rule.kind === 'gradient') {
    const ratio = Math.max(0, Math.min(1, (numericValue - rule.minimum) / (rule.maximum - rule.minimum)))
    const low = rule.low.color!
    const high = rule.high.color!
    return {
      style: {
        gradient: { low, high, ratio },
        icon: ratio < 0.5 ? rule.low.icon : rule.high.icon,
      },
      outcome: 'matched',
    }
  }

  const match = rule.rules.find((candidate) => comparisonMatches(numericValue, candidate.operator, candidate.value))
  return match
    ? { style: resolvedStyle(match.style), outcome: 'matched' }
    : { style: resolvedStyle(rule.defaultStyle), outcome: 'default' }
}

function conditionalNumericValue(value: unknown): number | undefined {
  if (typeof value === 'number') return Number.isFinite(value) ? value : undefined
  if (typeof value !== 'string') return undefined
  const parsed = parseDecimal(value)
  if (!parsed) return undefined
  const converted = Number(value)
  const zero = parsed.integer === '0' && /^0*$/.test(parsed.fraction)
  if (!Number.isFinite(converted) || (converted === 0 && !zero)) return undefined
  return converted
}

function comparisonMatches(value: number, operator: VisualizationComparisonOperator, threshold: number): boolean {
  switch (operator) {
    case 'less_than': return value < threshold
    case 'less_or_equal': return value <= threshold
    case 'greater_than': return value > threshold
    case 'greater_or_equal': return value >= threshold
    case 'equal': return Object.is(value, threshold)
    case 'not_equal': return !Object.is(value, threshold)
  }
}

function resolvedStyle(style: VisualizationConditionalStyle): ResolvedConditionalStyle {
  return {
    ...(style.color ? { color: style.color } : {}),
    ...(style.icon ? { icon: style.icon } : {}),
  }
}

function invalid(
  format: VisualizationConditionalFormat,
  fallback: VisualizationConditionalStyle,
  reason: string,
): ConditionalFormatResult {
  return {
    style: resolvedStyle(fallback),
    outcome: 'invalid',
    diagnostic: `conditional formatting ${JSON.stringify(format.id)} ${reason}`,
  }
}

function parseColor(value: string): readonly [number, number, number] | undefined {
  const hex = value.trim().match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i)?.[1]
  if (hex) {
    const expanded = hex.length === 3 ? [...hex].map((part) => `${part}${part}`).join('') : hex
    return [0, 2, 4].map((offset) => Number.parseInt(expanded.slice(offset, offset + 2), 16)) as unknown as readonly [number, number, number]
  }
  const rgb = value.trim().match(/^rgba?\(\s*(\d+(?:\.\d+)?)\s*[, ]\s*(\d+(?:\.\d+)?)\s*[, ]\s*(\d+(?:\.\d+)?)/i)
  if (!rgb) return undefined
  const channels = rgb.slice(1, 4).map(Number)
  if (channels.some((channel) => !Number.isFinite(channel) || channel < 0 || channel > 255)) return undefined
  return channels as unknown as readonly [number, number, number]
}

function contrastRatio(
  background: readonly [number, number, number],
  foreground: readonly [number, number, number] | undefined,
): number {
  if (!foreground) return 0
  const luminance = (color: readonly [number, number, number]) => {
    const channels = color.map((channel) => {
      const normalized = channel / 255
      return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4
    })
    return channels[0]! * 0.2126 + channels[1]! * 0.7152 + channels[2]! * 0.0722
  }
  const first = luminance(background)
  const second = luminance(foreground)
  return (Math.max(first, second) + 0.05) / (Math.min(first, second) + 0.05)
}
