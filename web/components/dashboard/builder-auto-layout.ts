type Placement = { col: number; row: number; colSpan: number; rowSpan: number }
type Visual = { id: string; type: string; placement?: Placement }
type Grid = { columns: number; rowHeight: number; gap: number }
type ArrangedVisual = { componentId: string; placement: { column: number; row: number; columnSpan: number; rowSpan: number } }

// Explicit arrangement fills equal-height bands so short KPIs cannot leave
// holes beside taller charts. Imports retain their supplied placements and
// fit additional visuals around those placements using their compact sizes.
export function arrangeDashboardVisuals(
  visuals: Visual[],
  obstacles: Placement[],
  grid: Grid,
): ArrangedVisual[] {
  if (visuals.some(visual => visual.placement)) return fitImportedVisuals(visuals, obstacles, grid)

  const columns = Math.max(1, Math.floor(grid.columns))
  const occupied = obstacles.map(placement => ({ ...placement }))
  const result: ArrangedVisual[] = []
  const kpis = visuals.filter(visual => visual.type === 'kpi')
  const charts = visuals.filter(visual => visual.type !== 'kpi')

  const placeBand = (band: Visual[], minimumWidth: number): void => {
    const height = visualHeight(band[0].type, grid)
    let offset = 0
    while (offset < band.length) {
      for (let row = 1; ; row++) {
        // Find contiguous space free for the entire height of this band.
        // Obstacles can split a band; each usable interval is filled in turn.
        const free = Array.from({ length: columns }, (_, index) => !occupied.some(placement =>
          index + 1 >= placement.col && index + 1 < placement.col + placement.colSpan
          && row < placement.row + placement.rowSpan && row + height > placement.row))
        let placed = false
        for (let start = 0; start < columns;) {
          if (!free[start]) { start++; continue }
          let end = start + 1
          while (end < columns && free[end]) end++
          const width = end - start
          const count = Math.min(band.length - offset, Math.floor(width / minimumWidth))
          if (count === 0) { start = end; continue }
          const baseWidth = Math.floor(width / count)
          const remainder = width % count
          let column = start + 1
          for (let index = 0; index < count; index++) {
            const columnSpan = baseWidth + (index >= count - remainder ? 1 : 0)
            result.push({ componentId: band[offset++].id, placement: { column, row, columnSpan, rowSpan: height } })
            occupied.push({ col: column, row, colSpan: columnSpan, rowSpan: height })
            column += columnSpan
          }
          placed = true
          break
        }
        if (placed) break
      }
    }
  }

  const kpiBandSize = Math.min(4, columns)
  for (let index = 0; index < kpis.length; index += kpiBandSize) {
    placeBand(kpis.slice(index, index + kpiBandSize), Math.max(1, Math.floor(columns / 4)))
  }
  for (let index = 0; index < charts.length;) {
    if (isWide(charts[index].type)) {
      placeBand([charts[index++]], columns)
      continue
    }
    let end = index + 1
    while (end < charts.length && !isWide(charts[end].type)) end++
    // An odd run starts with a full-width chart, followed by balanced pairs.
    // This also accounts for pie/donut charts when choosing the band widths.
    if ((end - index) % 2 === 1) placeBand([charts[index++]], Math.max(1, Math.floor(columns / 2)))
    while (index < end) {
      placeBand(charts.slice(index, index + 2), Math.max(1, Math.floor(columns / 2)))
      index += 2
    }
  }
  return result
}

function isWide(type: string): boolean {
  return ['table', 'matrix', 'pivot', 'map', 'heatmap'].includes(type)
}

function visualHeight(type: string, grid: Grid): number {
  const gap = Math.max(0, grid.gap)
  const pitch = Math.max(1, grid.rowHeight) + gap
  const pixels = type === 'kpi' ? 128 : ['table', 'matrix', 'pivot'].includes(type) ? 384 : 320
  return Math.max(1, Math.floor((pixels + gap + pitch / 2) / pitch))
}

function fitImportedVisuals(visuals: Visual[], obstacles: Placement[], grid: Grid): ArrangedVisual[] {
  const columns = Math.max(1, grid.columns)
  const gap = Math.max(0, grid.gap)
  const pitch = Math.max(1, grid.rowHeight) + gap
  const occupied = [...obstacles, ...visuals.flatMap(visual => visual.placement ? [visual.placement] : [])].map(p => ({ ...p }))
  const ordered = [...visuals.filter(v => v.type === 'kpi'), ...visuals.filter(v => v.type !== 'kpi')]
  const charts = ordered.filter(v => !['kpi', 'table', 'matrix', 'pivot', 'map', 'heatmap'].includes(v.type))
  const hero = charts.length >= 3 && charts.length % 2 === 1 ? charts[0].id : visuals.length === 1 ? charts[0]?.id : undefined
  const kpiColumns = Math.min(4, Math.max(1, ordered.filter(v => v.type === 'kpi').length))
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
