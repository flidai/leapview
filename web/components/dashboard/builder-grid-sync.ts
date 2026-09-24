import type { GridItemHTMLElement, GridStack } from 'gridstack'

type CanonicalGridComponent = {
  id: string
  placement: {
    col: number
    row: number
    colSpan: number
    rowSpan: number
  }
}

type CanonicalGridNode = {
  id: string
  tile: GridItemHTMLElement
  x: number
  y: number
  width: number
  height: number
}

export function applyCanonicalGridAttributes(root: ShadowRoot | null, components: CanonicalGridComponent[]): void {
  for (const { tile, x, y, width, height } of canonicalGridNodes(root, components)) {
    tile.setAttribute('gs-x', String(x))
    tile.setAttribute('gs-y', String(y))
    tile.setAttribute('gs-w', String(width))
    tile.setAttribute('gs-h', String(height))
    if (tile.gridstackNode) Object.assign(tile.gridstackNode, { x, y, w: width, h: height })
  }
}

export function syncGridStackNodesToCanonical(grid: GridStack, root: ShadowRoot | null, components: CanonicalGridComponent[]): void {
  // Apply the complete layout atomically. Per-widget update() resolves
  // collisions against widgets that still have their previous positions,
  // which can shift later canonical placements during an SSE reconciliation.
  grid.load(canonicalGridNodes(root, components).map(({ id, x, y, width, height }) => ({
    id,
    x,
    y,
    w: width,
    h: height,
  })), false)
}

function canonicalGridNodes(root: ShadowRoot | null, components: CanonicalGridComponent[]): CanonicalGridNode[] {
  return components.flatMap((component) => {
    const tile = root?.querySelector<GridItemHTMLElement>(`[gs-id="${CSS.escape(component.id)}"]`)
    return tile ? [{
      id: component.id,
      tile,
      x: Math.max(0, component.placement.col - 1),
      y: Math.max(0, component.placement.row - 1),
      width: Math.max(1, component.placement.colSpan),
      height: Math.max(1, component.placement.rowSpan),
    }] : []
  })
}
