export function responsiveBarCategoryAxis(option: Record<string, any>, width: number, compact: boolean): unknown {
  if (!Array.isArray(option.series) || !option.series.some((series: Record<string, any>) => series?.type === 'bar')) return undefined
  const axes = Array.isArray(option.yAxis) ? option.yAxis : [option.yAxis]
  let hasCategoryAxis = false
  const result = axes.map((axis: Record<string, any> | undefined) => {
    if (!axis || axis.type !== 'category') return axis
    hasCategoryAxis = true
    const label = axis.axisLabel ?? {}
    // Bound only the display text. The formatter, category values, and tooltip
    // keep their full labels, while the plot retains space on narrow cards.
    const budget = Math.min(160, Math.floor(width * 0.4))
    return {
      ...axis,
      axisLabel: {
        ...label,
        width: compact ? Math.min((typeof label.width === 'number' && Number.isFinite(label.width) ? label.width : budget), budget) : label.width ?? null,
        overflow: compact ? 'truncate' : label.overflow ?? null,
        ellipsis: compact ? label.ellipsis ?? '…' : label.ellipsis ?? null,
      },
    }
  })
  if (!hasCategoryAxis) return undefined
  return Array.isArray(option.yAxis) ? result : result[0]
}

export function compactInset(value: unknown, fallback: number): unknown {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.min(value, fallback)
  if (typeof value === 'string') return value
  return fallback
}

export function compactBottomInset(value: unknown, fallback: number, preserveExisting: boolean): unknown {
  if (preserveExisting && typeof value === 'number' && Number.isFinite(value)) return Math.max(value, fallback)
  if (typeof value === 'string') return value
  return fallback
}
