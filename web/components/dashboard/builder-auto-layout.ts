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
  const completeMetricCount = Math.floor(metrics.length / kpiColumns) * kpiColumns
  const partialMetrics = options.compact ? metrics.slice(completeMetricCount) : []
  const trend = (items: typeof visuals) => items.find(v => ['line', 'area', 'combo'].includes(v.type)) ?? items[0]
  // Put compact KPIs above a supporting chart, beside a larger main chart.
  // The two columns share a height, so neither a blank KPI band nor a cavity
  // below its cards is needed. Authored placements stay untouched.
  const featured = options.compact && columns > 1 && partialMetrics.length > 0 && partialMetrics.every(v => !v.placement)
    ? trend(charts.filter(v => !v.placement)) : undefined
  const companion = featured ? charts.find(v => v !== featured && !v.placement) : undefined
  const regularCharts = charts.filter(v => v !== featured && v !== companion)
  const hero = options.compact
    ? regularCharts.length % 2 === 1 ? trend(regularCharts)?.id : undefined
    : charts.length >= 3 && charts.length % 2 === 1 ? charts[0].id : visuals.length === 1 ? charts[0]?.id : undefined
  const ordered = options.compact
    ? [...metrics, ...(featured ? [featured] : []), ...(companion ? [companion] : []), ...regularCharts.filter(v => v.id === hero), ...regularCharts.filter(v => v.id !== hero), ...visuals.filter(v => ['table', 'matrix', 'pivot', 'map', 'heatmap'].includes(v.type))]
    : [...metrics, ...visuals.filter(v => v.type !== 'kpi')]
  const rowsFor = (pixels: number) => Math.max(1, Math.floor((pixels + gap + pitch / 2) / pitch))
  const fitted = new Map<string, Placement>()
  const findSpace = (width: number, height: number): Placement => {
    for (let row = 1; ; row++) for (let col = 1; col + width - 1 <= columns; col++) {
      if (!occupied.some(p => col < p.col + p.colSpan && col + width > p.col && row < p.row + p.rowSpan && row + height > p.row)) {
        return { col, row, colSpan: width, rowSpan: height }
      }
    }
  }
  const placements = ordered.map(visual => {
    if (visual.placement) {
      const p = visual.placement
      return { componentId: visual.id, placement: { column: p.col, row: p.row, columnSpan: p.colSpan, rowSpan: p.rowSpan } }
    }
    if (featured && partialMetrics.some(v => v.id === visual.id) && !fitted.has(visual.id)) {
      const metricHeight = rowsFor(128)
      const supportHeight = companion ? rowsFor(320) : 0
      const height = Math.max(rowsFor(320), partialMetrics.length * metricHeight + supportHeight)
      const block = findSpace(columns, height)
      const leftWidth = Math.max(1, Math.floor(columns / 3))
      const metricRows = height - supportHeight
      partialMetrics.forEach((metric, i) => fitted.set(metric.id, {
        col: block.col, row: block.row + Math.floor(metricRows * i / partialMetrics.length), colSpan: leftWidth,
        rowSpan: Math.floor(metricRows * (i + 1) / partialMetrics.length) - Math.floor(metricRows * i / partialMetrics.length),
      }))
      fitted.set(featured.id, { col: block.col + leftWidth, row: block.row, colSpan: columns - leftWidth, rowSpan: height })
      if (companion) fitted.set(companion.id, { col: block.col, row: block.row + metricRows, colSpan: leftWidth, rowSpan: supportHeight })
      occupied.push(...fitted.values())
    }
    let placed = fitted.get(visual.id)
    if (!placed) {
      const dense = ['table', 'matrix', 'pivot'].includes(visual.type)
      const wide = dense || ['map', 'heatmap'].includes(visual.type) || visual.id === hero
      const metricIndex = metrics.findIndex(v => v.id === visual.id)
      const fullMetric = options.compact && metricIndex >= 0 && metricIndex < completeMetricCount
      const metricColumn = metricIndex % kpiColumns
      const width = fullMetric
        ? Math.floor(columns * (metricColumn + 1) / kpiColumns) - Math.floor(columns * metricColumn / kpiColumns)
        : Math.max(1, wide ? columns : Math.floor(columns / (visual.type === 'kpi' ? kpiColumns : 2)))
      const height = rowsFor(visual.type === 'kpi' ? 128 : dense ? 384 : 320)
      placed = findSpace(width, height)
      occupied.push(placed)
    }
    return { componentId: visual.id, placement: { column: placed.col, row: placed.row, columnSpan: placed.colSpan, rowSpan: placed.rowSpan } }
  })
  // Use the remaining width at a row's right edge, but never change an
  // authored footprint or expand through another component.
  for (const visual of ordered) {
    if (visual.placement) continue
    const update = placements.find(p => p.componentId === visual.id)!
    const p = update.placement
    const current = occupied.find(rect => rect.col === p.column && rect.row === p.row && rect.colSpan === p.columnSpan && rect.rowSpan === p.rowSpan)!
    // Keep compact KPI widths; chart rows use the available horizontal space.
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
