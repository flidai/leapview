import { expect, test } from 'bun:test'
import { layoutLineageGraph, LINEAGE_NODE_HEIGHT, LINEAGE_NODE_WIDTH } from './asset-lineage-layout'

const node = (id: string, kind = 'model') => ({ id, label: id, kind })
const edge = (source: string, target: string) => ({ source, target })

test('places every dependency in successive columns even for chains of the same resource kind', () => {
  const nodes = ['raw', 'staging', 'mart', 'semantic'].map(id => ({ ...node(id), rank: 2 }))
  const edges = [edge('raw', 'staging'), edge('staging', 'mart'), edge('mart', 'semantic')]
  const positions = layoutLineageGraph(nodes, edges)
  for (const { source, target } of edges) {
    expect(positions.get(target)!.x - positions.get(source)!.x).toBeGreaterThan(LINEAGE_NODE_WIDTH)
    expect(positions.get(target)!.y).toBe(positions.get(source)!.y)
  }
})

test('centers fan-in and fan-out and prevents crowded column overlap', () => {
  const branches = Array.from({ length: 12 }, (_, i) => node(`branch-${i}`))
  const positions = layoutLineageGraph([node('source'), ...branches, node('sink')], branches.flatMap(({ id }) => [edge('source', id), edge(id, 'sink')]))
  const ys = branches.map(({ id }) => positions.get(id)!.y).sort((a, b) => a - b)
  for (let i = 1; i < ys.length; i++) expect(ys[i]! - ys[i - 1]!).toBeGreaterThan(LINEAGE_NODE_HEIGHT)
  expect(positions.get('source')!.y).toBe((ys[0]! + ys.at(-1)!) / 2)
  expect(positions.get('sink')!.y).toBe(positions.get('source')!.y)
})

test('orders adjacent branches by their dependencies to remove avoidable crossings', () => {
  const positions = layoutLineageGraph(['a', 'b', 'c', 'd', 'root', 'sink'].map(id => node(id)), [
    edge('root', 'a'), edge('root', 'b'), edge('a', 'd'), edge('b', 'c'), edge('c', 'sink'), edge('d', 'sink'),
  ])
  expect((positions.get('a')!.y - positions.get('b')!.y) * (positions.get('d')!.y - positions.get('c')!.y)).toBeGreaterThan(0)
})

test('handles cycles, disconnected nodes, duplicate edges, and missing endpoints deterministically', () => {
  const nodes = ['a', 'b', 'c', 'd', 'isolated'].map(id => node(id))
  const edges = [edge('a', 'b'), edge('b', 'c'), edge('c', 'b'), edge('c', 'd'), edge('a', 'b'), edge('missing', 'a'), edge('isolated', 'isolated')]
  const positions = layoutLineageGraph(nodes, edges)
  expect(layoutLineageGraph([...nodes].reverse(), [...edges].reverse())).toEqual(positions)
  expect(positions.size).toBe(nodes.length)
  expect(positions.get('a')!.x).toBeLessThan(positions.get('b')!.x)
  expect(positions.get('c')!.x).toBeLessThan(positions.get('d')!.x)
  const boxes = [...positions.values()]
  for (let i = 0; i < boxes.length; i++) {
    expect(Number.isFinite(boxes[i]!.x) && Number.isFinite(boxes[i]!.y)).toBe(true)
    for (let j = i + 1; j < boxes.length; j++) {
      expect(Math.abs(boxes[i]!.x - boxes[j]!.x) >= LINEAGE_NODE_WIDTH || Math.abs(boxes[i]!.y - boxes[j]!.y) >= LINEAGE_NODE_HEIGHT).toBe(true)
    }
  }
})

test('ignores presentation metadata and does not mutate input', () => {
  const nodes = [node('a', 'dashboard'), node('b', 'connection'), node('c')]
  const edges = [edge('a', 'b'), edge('b', 'c')]
  const before = structuredClone({ nodes, edges })
  const positions = layoutLineageGraph(nodes, edges)
  expect(layoutLineageGraph(nodes.map((n, i) => ({ ...n, rank: 10 - i, side: 'upstream', selected: true })), edges)).toEqual(positions)
  expect({ nodes, edges }).toEqual(before)
  expect(layoutLineageGraph([], [])).toEqual(new Map())
})

test('lays out long dependency chains without exhausting the call stack', () => {
  const nodes = Array.from({ length: 12_000 }, (_, index) => node(String(index)))
  const edges = nodes.slice(1).map((current, index) => edge(nodes[index]!.id, current.id))
  const positions = layoutLineageGraph(nodes, edges)
  expect(positions.size).toBe(nodes.length)
  expect(positions.get(nodes.at(-1)!.id)!.x).toBeGreaterThan(positions.get('0')!.x)
  expect(positions.get(nodes.at(-1)!.id)!.y).toBe(positions.get('0')!.y)
})

test('keeps sparse chains with many long shortcuts compact and deterministic', () => {
  const nodes = Array.from({ length: 1_000 }, (_, index) => node(String(index)))
  const edges = nodes.slice(1).flatMap((current, index) => [
    edge(nodes[index]!.id, current.id),
    edge(nodes[0]!.id, current.id),
  ])
  const positions = layoutLineageGraph(nodes, edges)
  expect(positions.size).toBe(nodes.length)
  // With one real asset per rank, excess shortcut ordering must not turn the
  // chain into hundreds of empty rows occupied only by virtual vertices.
  expect(new Set([...positions.values()].map(position => position.y)).size).toBe(1)
  for (const { source, target } of edges) {
    expect(positions.get(target)!.x - positions.get(source)!.x).toBeGreaterThan(LINEAGE_NODE_WIDTH)
  }
  expect(layoutLineageGraph([...nodes].reverse(), [...edges].reverse())).toEqual(positions)
})
