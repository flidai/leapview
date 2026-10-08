export type LineageLayoutNode = {
  id: string
  label: string
  kind: string
  rank?: number
  side?: string
  selected?: boolean
}

export type LineageLayoutEdge = { source: string; target: string }
export type LineagePosition = { x: number; y: number }

export const LINEAGE_NODE_WIDTH = 224
export const LINEAGE_NODE_HEIGHT = 100
const COLUMN_GAP = 112
const ROW_GAP = 36
const MARGIN = 48
// Long shortcut edges can otherwise expand a sparse graph quadratically.
const MAX_VIRTUAL_VERTICES = 10_000
const MAX_VIRTUAL_VERTICES_PER_NODE = 4

type Vertex = { id: string; rank: number; incoming: Vertex[]; outgoing: Vertex[] }

const compare = (a: string, b: string): number => a < b ? -1 : a > b ? 1 : 0

/** Top-left positions for a left-to-right, dependency-ranked graph. Presentation
 * metadata deliberately does not affect placement, including active selection. */
export function layoutLineageGraph(
  nodes: readonly LineageLayoutNode[],
  edges: readonly LineageLayoutEdge[],
): Map<string, LineagePosition> {
  const ids = [...new Set(nodes.map(node => node.id))].sort(compare)
  if (!ids.length) return new Map()
  const adjacency = new Map(ids.map(id => [id, new Set<string>()]))
  for (const { source, target } of edges) {
    if (source !== target && adjacency.has(source) && adjacency.has(target)) adjacency.get(source)!.add(target)
  }
  const successors = new Map(ids.map(id => [id, [...adjacency.get(id)!].sort(compare)]))

  // A cycle forms one rank. Rank the resulting DAG so malformed/cyclic lineage
  // still renders without dropping nodes or making the layout input-order based.
  const components = stronglyConnectedComponents(ids, successors)
  const componentOf = new Map<string, number>()
  components.forEach((members, index) => members.forEach(id => componentOf.set(id, index)))
  const componentEdges = components.map(() => new Set<number>())
  const indegrees = components.map(() => 0)
  for (const source of ids) {
    for (const target of successors.get(source)!) {
      const from = componentOf.get(source)!
      const to = componentOf.get(target)!
      if (from === to || componentEdges[from]!.has(to)) continue
      componentEdges[from]!.add(to)
      indegrees[to]!++
    }
  }
  const ranks = components.map(() => 0)
  const ready = indegrees.flatMap((degree, index) => degree === 0 ? [index] : [])
  for (let cursor = 0; cursor < ready.length; cursor++) {
    const from = ready[cursor]!
    for (const to of componentEdges[from]!) {
      ranks[to] = Math.max(ranks[to]!, ranks[from]! + 1)
      if (--indegrees[to]! === 0) ready.push(to)
    }
  }

  // Estimate expansion before allocating any virtual vertices. If it exceeds
  // the budget, order real vertices using their direct neighbors instead. The
  // dependency ranks remain unchanged, and the choice is input-order invariant.
  const virtualVertexBudget = Math.min(MAX_VIRTUAL_VERTICES, ids.length * MAX_VIRTUAL_VERTICES_PER_NODE)
  let virtualVertexCount = 0
  for (const source of ids) {
    for (const target of successors.get(source)!) {
      virtualVertexCount += Math.max(0, ranks[componentOf.get(target)!]! - ranks[componentOf.get(source)!]! - 1)
      if (virtualVertexCount > virtualVertexBudget) break
    }
    if (virtualVertexCount > virtualVertexBudget) break
  }
  const useVirtualVertices = virtualVertexCount <= virtualVertexBudget

  const layers: Vertex[][] = []
  const vertices = new Map<string, Vertex>()
  const addVertex = (id: string, rank: number): Vertex => {
    const vertex: Vertex = { id, rank, incoming: [], outgoing: [] }
    const layer = layers[rank] ??= []
    layer.push(vertex)
    return vertex
  }
  for (const id of ids) vertices.set(id, addVertex(id, ranks[componentOf.get(id)!]!))
  const connect = (from: Vertex, to: Vertex): void => {
    from.outgoing.push(to)
    to.incoming.push(from)
  }
  for (const source of ids) {
    for (const target of successors.get(source)!) {
      let from = vertices.get(source)!
      const to = vertices.get(target)!
      if (from.rank === to.rank) continue
      // Virtual vertices let edges spanning several ranks participate in ordering.
      for (let rank = from.rank + 1; useVirtualVertices && rank < to.rank; rank++) {
        const intermediate = addVertex(JSON.stringify([source, target, rank]), rank)
        connect(from, intermediate)
        from = intermediate
      }
      connect(from, to)
    }
  }

  // Alternating barycenter sweeps bring related branches together. Existing order
  // breaks ties, making repeated layouts and shuffled payloads produce the same result.
  const order = new Map<Vertex, number>()
  const indexLayer = (layer: Vertex[]): void => {
    layer.forEach((vertex, index) => order.set(vertex, index - (layer.length - 1) / 2))
  }
  layers.forEach(indexLayer)
  const sortLayer = (layer: Vertex[], direction: 'incoming' | 'outgoing'): void => {
    const barycenter = new Map(layer.map(vertex => {
      const adjacent = vertex[direction]
      return [vertex, adjacent.length
        ? adjacent.reduce((sum, neighbor) => sum + order.get(neighbor)!, 0) / adjacent.length
        : order.get(vertex)!]
    }))
    layer.sort((a, b) => barycenter.get(a)! - barycenter.get(b)! || order.get(a)! - order.get(b)!)
    indexLayer(layer)
  }
  for (let pass = 0; pass < 6; pass++) {
    for (let rank = 1; rank < layers.length; rank++) sortLayer(layers[rank]!, 'incoming')
    for (let rank = layers.length - 2; rank >= 0; rank--) sortLayer(layers[rank]!, 'outgoing')
  }

  const maxRows = layers.reduce((max, layer) => Math.max(max, layer.length), 0)
  const positions = new Map<string, LineagePosition>()
  for (const id of ids) {
    const vertex = vertices.get(id)!
    positions.set(id, {
      x: MARGIN + vertex.rank * (LINEAGE_NODE_WIDTH + COLUMN_GAP),
      y: MARGIN + (order.get(vertex)! + (maxRows - 1) / 2) * (LINEAGE_NODE_HEIGHT + ROW_GAP),
    })
  }
  return positions
}

/** Iterative Kosaraju traversal avoids call-stack limits on long model chains. */
function stronglyConnectedComponents(ids: string[], successors: Map<string, string[]>): string[][] {
  const visited = new Set<string>()
  const finished: string[] = []
  for (const root of ids) {
    if (visited.has(root)) continue
    visited.add(root)
    const stack = [{ id: root, next: 0 }]
    while (stack.length) {
      const frame = stack[stack.length - 1]!
      const adjacent = successors.get(frame.id)!
      if (frame.next === adjacent.length) {
        finished.push(frame.id)
        stack.pop()
      } else {
        const next = adjacent[frame.next++]!
        if (!visited.has(next)) {
          visited.add(next)
          stack.push({ id: next, next: 0 })
        }
      }
    }
  }
  const predecessors = new Map(ids.map(id => [id, [] as string[]]))
  for (const [source, targets] of successors) for (const target of targets) predecessors.get(target)!.push(source)
  visited.clear()
  const components: string[][] = []
  for (const root of finished.reverse()) {
    if (visited.has(root)) continue
    const members: string[] = []
    const stack = [root]
    visited.add(root)
    while (stack.length) {
      const id = stack.pop()!
      members.push(id)
      for (const previous of predecessors.get(id)!) {
        if (!visited.has(previous)) {
          visited.add(previous)
          stack.push(previous)
        }
      }
    }
    components.push(members)
  }
  return components
}
