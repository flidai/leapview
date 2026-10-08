type Placement = { col: number; row: number; colSpan: number; rowSpan: number }

// Match the sizing used when importing an agent visual. Preserve nonvisual
// components and any supplied visual placements, then fit new charts around them.
export function arrangeDashboardVisuals(
  visuals: Array<{ id: string; type: string; placement?: Placement }>,
  obstacles: Placement[],
  grid: { columns: number; rowHeight: number; gap: number },
) {
  const columns = Math.max(1, grid.columns)
  const gap = Math.max(0, grid.gap)
  const pitch = Math.max(1, grid.rowHeight) + gap
  const occupied = [...obstacles, ...visuals.flatMap(visual => visual.placement ? [visual.placement] : [])].map(p => ({ ...p }))
  const ordered = [...visuals.filter(v => v.type === 'kpi'), ...visuals.filter(v => v.type !== 'kpi')]
  const charts = ordered.filter(v => !['kpi', 'table', 'matrix', 'pivot', 'map', 'heatmap'].includes(v.type))
  const hero = charts.length >= 3 && charts.length % 2 === 1 ? charts[0].id : visuals.length === 1 ? charts[0]?.id : undefined
  const kpiColumns = Math.min(4, Math.max(1, ordered.filter(v => v.type === 'kpi' && !v.placement).length))
  const placements = ordered.map(visual => {
    if (visual.placement) {
      const p = visual.placement
      return { componentId: visual.id, placement: { column: p.col, row: p.row, columnSpan: p.colSpan, rowSpan: p.rowSpan } }
    }
    const dense = ['table', 'matrix', 'pivot'].includes(visual.type)
    const wide = dense || ['map', 'heatmap'].includes(visual.type) || visual.id === hero
    const width = Math.max(1, wide ? columns : Math.floor(columns / (visual.type === 'kpi' ? kpiColumns : 2)))
    const height = Math.max(1, Math.floor(((visual.type === 'kpi' ? 128 : dense ? 384 : 320) + gap + pitch / 2) / pitch))
    for (let row = 1; ; row++) {
      for (let col = 1; col + width - 1 <= columns; col++) {
        if (occupied.some(p => col < p.col + p.colSpan && col + width > p.col && row < p.row + p.rowSpan && row + height > p.row)) continue
        occupied.push({ col, row, colSpan: width, rowSpan: height })
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
    let right = columns + 1
    for (const other of occupied) {
      if (other === current || other.col < p.column + p.columnSpan || other.row >= p.row + p.rowSpan || other.row + other.rowSpan <= p.row) continue
      right = Math.min(right, other.col)
    }
    p.columnSpan = right - p.column
    current.colSpan = p.columnSpan
  }
  return placements
}
