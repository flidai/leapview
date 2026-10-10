import { expect, test } from 'bun:test'
import { buildTree, defaultExpandedIDs, flattenTree } from './hierarchy'
import type { TableHierarchy, TableRow } from './types'

const parentChild: TableHierarchy = { mode: 'parent_child', idField: 'id', parentField: 'parent', labelField: 'name' }
const nested: TableHierarchy = { mode: 'nested', childrenField: 'children', labelField: 'name' }

test('level paths distinguish typed and blank values while retaining duplicate source rows', () => {
  const values = [null, '', undefined, 1, '1', false, 'false', null]
  const rows = values.map((group, index) => ({ group, item: 'Leaf', amount: index }))
  const tree = buildTree(rows, { mode: 'levels', fields: ['group', 'item'] })
  expect(tree.roots).toHaveLength(7)
  expect(new Set(tree.roots.map(node => node.id)).size).toBe(7)
  expect(tree.roots.slice(0, 3).map(node => node.label)).toEqual(['(Blank)', '(Blank)', '(Blank)'])
  expect(tree.sourceRowCount).toBe(8)
  expect(tree.roots[0].synthetic).toBe(true)
  expect(tree.roots[0].row.amount).toBeUndefined()
  expect(tree.roots[0].children.map(node => node.row.amount)).toEqual([0, 7])
  expect(tree.roots[0].children[0].id).not.toBe(tree.roots[0].children[1].id)
  expect(tree.roots[0].children.map(node => node.sourceIndex)).toEqual([0, 7])
})

test('parent-child IDs retain scalar types and make unknown parents roots', () => {
  const tree = buildTree([
    { id: 1, parent: null, name: 'Numeric parent' },
    { id: '1', parent: null, name: 'String parent' },
    { id: 'number-child', parent: 1, name: 'Numeric child' },
    { id: 'string-child', parent: '1', name: 'String child' },
    { id: 'orphan', parent: 'missing', name: 'Orphan' },
  ], parentChild)
  expect(tree.roots.map(node => node.id)).toEqual(['number:1', 'string:1', 'string:orphan'])
  expect(tree.nodes.get('number:1')?.children.map(node => node.id)).toEqual(['string:number-child'])
  expect(tree.nodes.get('string:1')?.children.map(node => node.id)).toEqual(['string:string-child'])
  expect(tree.nodes.get('string:number-child')?.depth).toBe(1)
})

test('duplicate authored IDs and parent cycles fail explicitly', () => {
  expect(() => buildTree([{ id: 'same' }, { id: 'same' }], parentChild)).toThrow(/Duplicate hierarchy ID/)
  expect(() => buildTree([{ id: 'self', parent: 'self' }], parentChild)).toThrow(/parent cycle/)
  expect(() => buildTree([{ id: 'a', parent: 'b' }, { id: 'b', parent: 'a' }], parentChild)).toThrow(/parent cycle/)
  expect(() => buildTree([{ id: '', parent: null }], parentChild)).toThrow(/nonempty scalar ID/)
})

test('nested JSON descendants count toward the source-row budget', () => {
  const rows = [{ name: 'Root', children: '[{"name":"Child","children":[{"name":"Leaf"}]}]' }]
  const tree = buildTree(rows, nested, 3)
  expect(tree.sourceRowCount).toBe(3)
  expect(flattenTree(tree, defaultExpandedIDs(tree, 2)).map(node => [node.label, node.depth])).toEqual([
    ['Root', 0], ['Child', 1], ['Leaf', 2],
  ])
  expect(() => buildTree(rows, nested, 2)).toThrow(/source-row budget, including nested descendants/)
})

test('nested cycles, duplicate IDs, and invalid children fail before rendering', () => {
  const cyclic: TableRow = { name: 'Cycle' }
  cyclic.children = [cyclic]
  expect(() => buildTree([cyclic], nested)).toThrow(/contains a cycle/)
  expect(() => buildTree([{ id: 'same', children: [{ id: 'same' }] }], { ...nested, idField: 'id' })).toThrow(/Duplicate hierarchy ID/)
  expect(() => buildTree([{ children: '{}' }], nested)).toThrow(/array of children/)
  expect(() => buildTree([{ children: '[' }], nested)).toThrow(/invalid JSON children/)
  expect(() => buildTree([{ children: [42] }], nested)).toThrow(/row objects/)
})

test('nested adaptation and expansion support deep trees without recursion', () => {
  let row: TableRow = { name: 'Leaf' }
  for (let depth = 0; depth < 512; depth++) row = { name: `Level ${depth}`, children: [row] }
  const tree = buildTree([row], nested, 513)
  const visible = flattenTree(tree, defaultExpandedIDs(tree, 513))
  expect(visible).toHaveLength(513)
  expect(visible.at(-1)?.depth).toBe(512)
  expect(flattenTree(tree, new Set())).toHaveLength(1)
})

test('metric sorting compares exact decimals within sibling groups', () => {
  const tree = buildTree([
    { id: 'root', name: 'First group', amount: '0' },
    { id: 'high', parent: 'root', amount: '9007199254740993.126' },
    { id: 'low', parent: 'root', amount: '9007199254740993.125' },
    { id: 'negative-low', parent: 'root', amount: '-9007199254740993.126' },
    { id: 'negative-high', parent: 'root', amount: '-9007199254740993.125' },
    { id: 'other', name: 'Second group', amount: '1' },
    { id: 'other-child', parent: 'other', amount: '-10000000000000000' },
  ], parentChild)
  const expanded = defaultExpandedIDs(tree)
  const columns = [{ key: 'amount', label: 'Amount', role: 'metric' as const }]
  expect(flattenTree(tree, expanded, { key: 'amount', direction: 'asc' }, columns).map(node => node.id)).toEqual([
    'string:root', 'string:negative-low', 'string:negative-high', 'string:low', 'string:high', 'string:other', 'string:other-child',
  ])
  expect(flattenTree(tree, expanded, { key: 'amount', direction: 'desc' }, columns).map(node => node.id)).toEqual([
    'string:other', 'string:other-child', 'string:root', 'string:high', 'string:low', 'string:negative-high', 'string:negative-low',
  ])
})
