type Placement = { col: number; row: number; colSpan: number; rowSpan: number }

// Default sizing matches imported agent visuals; Visual magic requests compact
// cards. Preserve nonvisual components and any supplied visual placements.
export function arrangeDashboardVisuals(
  visuals: Array<{ id: string; type: string; placement?: Placement }>,
  obstacles: Placement[],
  grid: { columns: number; rowHeight: number; gap: number },
  options: { compact?: boolean } = {},
) {
  const columns = Math.max(1, grid.columns)
  const gap = Math.max(0, grid.gap)
  const pitch = Math.max(1, grid.rowHeight) + gap
  const occupied = [...obstacles, ...visuals.flatMap(visual => visual.placement ? [visual.placement] : [])].map(p => ({ ...p }))
  const metrics = visuals.filter(v => v.type === 'kpi')
  const charts = visuals.filter(v => !['kpi', 'table', 'matrix', 'pivot', 'map', 'heatmap'].includes(v.type))
  const kpiColumns = options.compact ? Math.min(4, columns) : Math.min(4, Math.max(1, metrics.length))
  // Full KPI rows lead the dashboard; a partial row belongs at the end so
  // compact cards don't leave unused space between subsequent visuals.
  const completeMetricCount = Math.floor(metrics.length / kpiColumns) * kpiColumns
  const trailingMetrics = options.compact ? metrics.slice(completeMetricCount) : []
  const ordered = options.compact
    ? [...metrics.slice(0, completeMetricCount), ...charts, ...visuals.filter(v => ['table', 'matrix', 'pivot', 'map', 'heatmap'].includes(v.type)), ...trailingMetrics]
    : [...metrics, ...visuals.filter(v => v.type !== 'kpi')]
  const hero = options.compact ? undefined : charts.length >= 3 && charts.length % 2 === 1 ? charts[0].id : visuals.length === 1 ? charts[0]?.id : undefined
  let trailingRow: number | undefined
  const placements = ordered.map(visual => {
    if (visual.placement) {
      const p = visual.placement
      return { componentId: visual.id, placement: { column: p.col, row: p.row, columnSpan: p.colSpan, rowSpan: p.rowSpan } }
    }
    const dense = ['table', 'matrix', 'pivot'].includes(visual.type)
    const wide = dense || ['map', 'heatmap'].includes(visual.type) || visual.id === hero || (options.compact && charts.length === 1 && visual.id === charts[0].id)
    // Pair charts, using three across for a final odd group so a chart is
    // not left alone on a row. Dense tables and maps follow on wide rows.
    const chartIndex = charts.findIndex(v => v.id === visual.id)
    const chartColumns = options.compact && charts.length >= 3 && charts.length % 2 === 1 && chartIndex >= charts.length - 3 ? 3 : 2
    const metricIndex = metrics.findIndex(v => v.id === visual.id)
    const fullMetric = options.compact && metricIndex >= 0 && metricIndex < completeMetricCount
    const metricColumn = metricIndex % kpiColumns
    const width = fullMetric
      ? Math.floor(columns * (metricColumn + 1) / kpiColumns) - Math.floor(columns * metricColumn / kpiColumns)
      : Math.max(1, wide ? columns : Math.floor(columns / (visual.type === 'kpi' ? kpiColumns : chartColumns)))
    const height = Math.max(1, Math.floor(((visual.type === 'kpi' ? 128 : dense ? 384 : 320) + gap + pitch / 2) / pitch))
    const trailing = trailingMetrics.some(v => v.id === visual.id)
    if (trailing && trailingRow === undefined) trailingRow = Math.max(1, ...occupied.map(p => p.row + p.rowSpan))
    for (let row = trailing ? trailingRow! : 1; ; row++) {
      for (let col = 1; col + width - 1 <= columns; col++) {
        if (occupied.some(p => col < p.col + p.colSpan && col + width > p.col && row < p.row + p.rowSpan && row + height > p.row)) continue
        const placed = { col, row, colSpan: width, rowSpan: height }
        occupied.push(placed)
        return { componentId: visual.id, placement: { column: col, row, columnSpan: width, rowSpan: height } }
      }
    }
  })
  // Use the remaining width at a row's right edge, but never change an
  // authored footprint or expand through another component.
  for (const visual of ordered) {
    if (visual.placement) continue
    const update = placements.find(p => p.componentId === visual.id)!
    const p = update.placement
    const current = occupied.find(rect => rect.col === p.column && rect.row === p.row && rect.colSpan === p.columnSpan && rect.rowSpan === p.rowSpan)!
    // Visual magic keeps metrics compact and charts readable without stretching
    // an isolated card across the page. Tables and maps retain their wide view.
    const compactWidth = visual.type === 'kpi' ? p.columnSpan : columns
    let right = options.compact && !['table', 'matrix', 'pivot', 'map', 'heatmap'].includes(visual.type)
      ? Math.min(columns + 1, p.column + compactWidth) : columns + 1
    for (const other of occupied) {
      if (other === current || other.col < p.column + p.columnSpan || other.row >= p.row + p.rowSpan || other.row + other.rowSpan <= p.row) continue
      right = Math.min(right, other.col)
    }
    p.columnSpan = right - p.column
    current.colSpan = p.columnSpan
  }
  return placements
}
