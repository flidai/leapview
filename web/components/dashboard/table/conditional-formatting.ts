import type { VisualizationColorIntent, VisualizationConditionalFormat } from '../../../generated/visualization'
import { resolveConditionalFormat, type ResolvedConditionalStyle } from '../visualization/conditional-format'
import type { TableColumn, TableRow } from './types'

export type ConditionalCellAppearance = Readonly<{
  background?: string
  foreground?: string
  icon?: string
  iconLabel?: string
}>

export function conditionalCellAppearance(row: TableRow, column: TableColumn): ConditionalCellAppearance {
  const formats = column.conditionalFormatting ?? []
  const background = resolveTarget(formats, 'cell_background', row)
  const foreground = resolveTarget(formats, 'cell_foreground', row)
  const icon = resolveTarget(formats, 'icon', row) ?? background ?? foreground
  const backgroundColor = background ? conditionalBackground(background) : undefined
  const foregroundColor = backgroundColor
    ? 'var(--lv-fg-default)'
    : foreground?.color ? foregroundIntentColor(foreground.color) : undefined
  return {
    ...(backgroundColor ? { background: backgroundColor, foreground: foregroundColor } : foregroundColor ? { foreground: foregroundColor } : {}),
    ...(icon?.icon ? { iconLabel: iconLabel(icon.icon) } : {}),
  }
}

function resolveTarget(
  formats: VisualizationConditionalFormat[],
  target: VisualizationConditionalFormat['target'],
  row: TableRow,
): ResolvedConditionalStyle | undefined {
  const format = formats.find((candidate) => candidate.target === target)
  if (!format) return undefined
  const columns = Object.keys(row)
  const values = columns.map((column) => row[column])
  return resolveConditionalFormat(format, columns, values).style
}

function conditionalBackground(style: ResolvedConditionalStyle): string | undefined {
  if (style.gradient) {
    const low = foregroundIntentColor(style.gradient.low)
    const high = foregroundIntentColor(style.gradient.high)
    const lowPercent = Math.round((1 - style.gradient.ratio) * 100)
    return `color-mix(in srgb, color-mix(in srgb, ${low} ${lowPercent}%, ${high}) 20%, transparent)`
  }
  if (!style.color) return undefined
  switch (style.color) {
    case 'success': return 'var(--lv-bg-success-muted)'
    case 'warning': return 'var(--lv-bg-warning-muted)'
    case 'danger': return 'var(--lv-bg-danger-muted)'
    case 'neutral': return 'var(--lv-bg-control)'
    default: return `color-mix(in srgb, ${foregroundIntentColor(style.color)} 18%, transparent)`
  }
}

function foregroundIntentColor(intent: VisualizationColorIntent): string {
  switch (intent) {
    case 'accent': return 'var(--lv-fg-accent)'
    case 'neutral': return 'var(--lv-fg-muted)'
    case 'ink': return 'var(--lv-fg-default)'
    case 'success': return 'var(--lv-fg-success)'
    case 'warning': return 'var(--lv-fg-warning)'
    case 'danger': return 'var(--lv-fg-danger)'
    default: return `var(--lv-data-${Number(intent.slice(5))})`
  }
}

function iconLabel(icon: string): string {
  switch (icon) {
    case 'arrow_up': return 'increasing'
    case 'arrow_down': return 'decreasing'
    case 'triangle_up': return 'higher'
    case 'triangle_down': return 'lower'
    case 'warning': return 'warning'
    default: return icon.replaceAll('_', ' ')
  }
}
