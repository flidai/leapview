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
  grid.batchUpdate()
  for (const { tile, x, y, width, height } of canonicalGridNodes(root, components)) {
    grid.update(tile, { x, y, w: width, h: height })
  }
  grid.batchUpdate(false)
}

function canonicalGridNodes(root: ShadowRoot | null, components: CanonicalGridComponent[]): CanonicalGridNode[] {
  return components.flatMap((component) => {
    const tile = root?.querySelector<GridItemHTMLElement>(`[gs-id="${CSS.escape(component.id)}"]`)
    return tile ? [{
      tile,
      x: Math.max(0, component.placement.col - 1),
      y: Math.max(0, component.placement.row - 1),
      width: Math.max(1, component.placement.colSpan),
      height: Math.max(1, component.placement.rowSpan),
    }] : []
  })
}
