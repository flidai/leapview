import type { SemanticModelGraphNodeSignal, SemanticModelGraphSignal } from '../../generated/signals'

const LAYOUT_SWEEP_COUNT = 4

export function orderDatasetRanks(
  graph: SemanticModelGraphSignal,
  ranks: Map<string, number>,
  rankValues: number[],
): Map<number, SemanticModelGraphNodeSignal[]> {
  const ordered = new Map<number, SemanticModelGraphNodeSignal[]>()
  const nodesByID = new Map(graph.nodes.map((node) => [node.id, node]))
  for (const rank of rankValues) {
    const nodes = graph.nodes
      .filter((node) => (ranks.get(node.id) ?? 0) === rank)
      .sort((left, right) => left.id.localeCompare(right.id))
    ordered.set(rank, nodes)
  }

  const incoming = new Map<string, string[]>()
  const outgoing = new Map<string, string[]>()
  for (const edge of graph.edges) {
    if (!nodesByID.has(edge.source) || !nodesByID.has(edge.target) || edge.source === edge.target) continue
    incoming.set(edge.target, [...(incoming.get(edge.target) ?? []), edge.source])
    outgoing.set(edge.source, [...(outgoing.get(edge.source) ?? []), edge.target])
  }

  // Alternate barycenter sweeps so a rank follows the vertical order of its
  // connected neighbors. Stable ties keep layouts deterministic.
  for (let iteration = 0; iteration < LAYOUT_SWEEP_COUNT; iteration += 1) {
    for (const rank of rankValues.slice(1)) reorderRank(ordered, rank, incoming)
    for (const rank of [...rankValues].reverse().slice(1)) reorderRank(ordered, rank, outgoing)
  }
  return ordered
}

function reorderRank(
  ordered: Map<number, SemanticModelGraphNodeSignal[]>,
  rank: number,
  neighbors: Map<string, string[]>,
): void {
  const nodes = ordered.get(rank)
  if (!nodes || nodes.length < 2) return
  const positions = new Map<string, number>()
  for (const rankNodes of ordered.values()) rankNodes.forEach((node, index) => positions.set(node.id, index))
  const previousOrder = new Map(nodes.map((node, index) => [node.id, index]))
  nodes.sort((left, right) => {
    const leftScore = barycenter(neighbors.get(left.id) ?? [], positions)
    const rightScore = barycenter(neighbors.get(right.id) ?? [], positions)
    if (leftScore !== rightScore) return leftScore - rightScore
    const stableOrder = (previousOrder.get(left.id) ?? 0) - (previousOrder.get(right.id) ?? 0)
    return stableOrder || left.id.localeCompare(right.id)
  })
}

function barycenter(neighborIDs: string[], positions: Map<string, number>): number {
  const neighborPositions = neighborIDs
    .map((id) => positions.get(id))
    .filter((position): position is number => position !== undefined)
    .sort((left, right) => left - right)
  if (neighborPositions.length === 0) return Number.POSITIVE_INFINITY
  return neighborPositions[Math.floor((neighborPositions.length - 1) / 2)] ?? Number.POSITIVE_INFINITY
}

export function splitDatasetRankNodes(
  nodes: SemanticModelGraphNodeSignal[],
  columnCount: number,
  heightFor: (node: SemanticModelGraphNodeSignal) => number,
  verticalGap: number,
): SemanticModelGraphNodeSignal[][] {
  if (columnCount <= 1) return [nodes]
  const count = Math.min(columnCount, nodes.length)
  const prefix = [0]
  for (const node of nodes) prefix.push((prefix.at(-1) ?? 0) + heightFor(node))
  const segmentHeight = (start: number, end: number) => (prefix[end] ?? 0) - (prefix[start] ?? 0) + Math.max(0, end - start - 1) * verticalGap
  const cost = Array.from({ length: count + 1 }, () => Array<number>(nodes.length + 1).fill(Number.POSITIVE_INFINITY))
  const cuts = Array.from({ length: count + 1 }, () => Array<number>(nodes.length + 1).fill(-1))
  cost[0]![0] = 0

  for (let columns = 1; columns <= count; columns += 1) {
    for (let end = columns; end <= nodes.length; end += 1) {
      for (let start = columns - 1; start < end; start += 1) {
        const prior = cost[columns - 1]?.[start] ?? Number.POSITIVE_INFINITY
        if (!Number.isFinite(prior)) continue
        const candidate = Math.max(prior, segmentHeight(start, end))
        const current = cost[columns]?.[end] ?? Number.POSITIVE_INFINITY
        if (candidate < current || (candidate === current && start < (cuts[columns]?.[end] ?? Number.MAX_SAFE_INTEGER))) {
          cost[columns]![end] = candidate
          cuts[columns]![end] = start
        }
      }
    }
  }

  const starts = Array<number>(count)
  let end = nodes.length
  for (let columns = count; columns > 0; columns -= 1) {
    const start = cuts[columns]?.[end]
    if (start === undefined || start < 0) return fallbackRankSplit(nodes, count)
    starts[columns - 1] = start
    end = start
  }
  return starts.map((start, index) => nodes.slice(start, index + 1 < starts.length ? starts[index + 1] : nodes.length))
}

function fallbackRankSplit(nodes: SemanticModelGraphNodeSignal[], columnCount: number): SemanticModelGraphNodeSignal[][] {
  const columns: SemanticModelGraphNodeSignal[][] = []
  let start = 0
  for (let column = 0; column < columnCount; column += 1) {
    const size = Math.ceil((nodes.length - start) / (columnCount - column))
    columns.push(nodes.slice(start, start + size))
    start += size
  }
  return columns
}
