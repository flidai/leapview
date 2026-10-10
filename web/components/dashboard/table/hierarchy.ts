import { decimalShift, parseDecimal, type DecimalParts } from '../visualization/decimal'
import type { TableColumn, TableHierarchy, TableRow, TableSort } from './types'

export interface HierarchyNode {
  id: string
  label: string
  row: TableRow
  depth: number
  sourceIndex: number | null
  synthetic: boolean
  children: HierarchyNode[]
}

export interface HierarchyTree {
  roots: HierarchyNode[]
  nodes: Map<string, HierarchyNode>
  /** Includes descendants in nested input, and excludes generated groups. */
  sourceRowCount: number
}

function identity(value: unknown, field: string): string {
  if ((typeof value !== 'string' && typeof value !== 'number' && typeof value !== 'boolean') || value === '' || (typeof value === 'number' && !Number.isFinite(value))) {
    throw new Error(`Hierarchy field "${field}" requires a nonempty scalar ID.`)
  }
  return `${typeof value}:${String(value)}`
}

function label(value: unknown): string {
  return value === null || value === undefined || value === '' ? '(Blank)' : String(value)
}

// Two independent rolling hashes keep generated IDs bounded even for very deep
// input. Authored IDs are preserved through the typed identity encoding above.
function generatedID(parent: string, value: string): string {
  let a = 2166136261, b = 5381
  const input = `${parent}\0${value}`
  for (let index = 0; index < input.length; index++) {
    const code = input.charCodeAt(index)
    a = Math.imul(a ^ code, 16777619)
    b = Math.imul(b, 33) ^ code
  }
  return `path:${(a >>> 0).toString(36)}:${(b >>> 0).toString(36)}`
}

function register(tree: HierarchyTree, node: HierarchyNode): void {
  if (tree.nodes.has(node.id)) throw new Error(`Duplicate hierarchy ID "${node.id}". Each node needs a unique ID.`)
  tree.nodes.set(node.id, node)
}

/** Adapts complete input without recursive calls or a maximum hierarchy depth. */
export function buildTree(rows: TableRow[], config: TableHierarchy, maxSourceRows = 10000): HierarchyTree {
  const tree: HierarchyTree = { roots: [], nodes: new Map(), sourceRowCount: 0 }
  const budget = Number.isFinite(maxSourceRows) ? Math.max(0, Math.floor(maxSourceRows)) : 10000
  if (rows.length > budget) throw new Error(`Hierarchy exceeds the ${budget.toLocaleString()} source-row budget.`)
  if (config.mode === 'levels') {
    if (!config.fields.length || config.fields.some(field => !field) || new Set(config.fields).size !== config.fields.length) {
      throw new Error('Hierarchy levels require a nonempty list of distinct fields.')
    }
    const childMaps = new Map<string, Map<string, HierarchyNode>>()
    for (const [sourceIndex, row] of rows.entries()) {
      let parent: HierarchyNode | undefined
      const dimensions: TableRow = {}
      for (const [depth, field] of config.fields.entries()) {
        const value = row[field]
        dimensions[field] = value
        const parentID = parent?.id ?? 'root'
        let siblings = childMaps.get(parentID)
        if (!siblings) childMaps.set(parentID, siblings = new Map())
        const key = `${typeof value}:${JSON.stringify(value)}`
        const last = depth === config.fields.length - 1
        let node = siblings.get(key)
        // Equal terminal paths remain separate source rows; no metric is summed.
        if (!node || last) {
          const duplicate = last && node ? `:row:${sourceIndex}` : ''
          node = {
            id: generatedID(parentID, `${field}:${key}${duplicate}`), label: label(value),
            row: last ? row : { ...dimensions }, depth, sourceIndex: last ? sourceIndex : null,
            synthetic: !last, children: [],
          }
          register(tree, node)
          if (!siblings.has(key)) siblings.set(key, node)
          ;(parent ? parent.children : tree.roots).push(node)
        }
        parent = node
      }
      tree.sourceRowCount++
    }
  } else if (config.mode === 'parent_child') {
    const parents = new Map<string, string | null>()
    for (const [sourceIndex, row] of rows.entries()) {
      const id = identity(row[config.idField], config.idField)
      const parent = row[config.parentField]
      parents.set(id, parent === null || parent === undefined || parent === '' ? null : identity(parent, config.parentField))
      register(tree, { id, label: label(row[config.labelField]), row, depth: 0, sourceIndex, synthetic: false, children: [] })
    }
    // Validate parent chains iteratively. Orphans intentionally become roots.
    const settled = new Set<string>()
    for (const id of tree.nodes.keys()) {
      const path = new Set<string>()
      let current: string | null | undefined = id
      while (current && tree.nodes.has(current) && !settled.has(current)) {
        if (path.has(current)) throw new Error(`Hierarchy contains a parent cycle at "${current}".`)
        path.add(current)
        current = parents.get(current)
      }
      for (const item of path) settled.add(item)
    }
    for (const node of tree.nodes.values()) {
      const parent = tree.nodes.get(parents.get(node.id) ?? '')
      ;(parent ? parent.children : tree.roots).push(node)
    }
    const stack = [...tree.roots]
    while (stack.length) {
      const node = stack.pop()!
      for (const child of node.children) { child.depth = node.depth + 1; stack.push(child) }
    }
    tree.sourceRowCount = rows.length
  } else if (config.mode === 'nested') {
    type Frame = { row: TableRow; parent?: HierarchyNode; index: number; exit?: boolean }
    const stack: Frame[] = []
    const active = new Set<TableRow>()
    for (let index = rows.length - 1; index >= 0; index--) stack.push({ row: rows[index], index })
    while (stack.length) {
      const frame = stack.pop()!
      if (frame.exit) { active.delete(frame.row); continue }
      if (!frame.row || typeof frame.row !== 'object' || Array.isArray(frame.row)) throw new Error('Nested hierarchy children must be row objects.')
      if (active.has(frame.row)) throw new Error(`Nested hierarchy contains a cycle at "${label(frame.row[config.labelField])}".`)
      if (tree.sourceRowCount >= budget) throw new Error(`Hierarchy exceeds the ${budget.toLocaleString()} source-row budget, including nested descendants.`)
      active.add(frame.row)
      const id = config.idField ? identity(frame.row[config.idField], config.idField) : generatedID(frame.parent?.id ?? 'root', String(frame.index))
      const node: HierarchyNode = {
        id, label: label(frame.row[config.labelField]), row: frame.row, depth: frame.parent ? frame.parent.depth + 1 : 0,
        sourceIndex: tree.sourceRowCount++, synthetic: false, children: [],
      }
      register(tree, node)
      ;(frame.parent ? frame.parent.children : tree.roots).push(node)
      let children = frame.row[config.childrenField]
      if (typeof children === 'string') {
        if (children.trim() === '') children = null
        else {
          try { children = JSON.parse(children) }
          catch { throw new Error(`Hierarchy field "${config.childrenField}" contains invalid JSON children.`) }
        }
      }
      if (children !== null && children !== undefined && !Array.isArray(children)) throw new Error(`Hierarchy field "${config.childrenField}" must contain an array of children.`)
      stack.push({ ...frame, exit: true })
      if (Array.isArray(children)) {
        if (children.length > budget - tree.sourceRowCount) throw new Error(`Hierarchy exceeds the ${budget.toLocaleString()} source-row budget, including nested descendants.`)
        for (let index = children.length - 1; index >= 0; index--) stack.push({ row: children[index] as TableRow, parent: node, index })
      }
    }
  } else {
    throw new Error('Unknown table hierarchy mode.')
  }
  return tree
}

export function defaultExpandedIDs(tree: HierarchyTree, depth = 1): Set<string> {
  const limit = Number.isFinite(depth) ? Math.max(0, depth) : 1
  return new Set([...tree.nodes.values()].filter(node => node.children.length && node.depth < limit).map(node => node.id))
}

function numericParts(value: unknown): DecimalParts | undefined {
  if (typeof value === 'string') return parseDecimal(value)
  if (typeof value !== 'number' || !Number.isFinite(value)) return undefined
  const raw = String(value)
  const exponent = /^(.+)e([+-]?\d+)$/.exec(raw)
  return parseDecimal(exponent ? decimalShift(exponent[1], Number(exponent[2])) : raw)
}

function compareDecimals(left: DecimalParts, right: DecimalParts): number {
  if (left.negative !== right.negative) return left.negative ? -1 : 1
  let comparison = left.integer.length - right.integer.length
  if (!comparison) comparison = left.integer < right.integer ? -1 : left.integer > right.integer ? 1 : 0
  if (!comparison) {
    const width = Math.max(left.fraction.length, right.fraction.length)
    const a = left.fraction.padEnd(width, '0'), b = right.fraction.padEnd(width, '0')
    comparison = a < b ? -1 : a > b ? 1 : 0
  }
  return left.negative ? -comparison : comparison
}

/** Flattens expanded nodes; optional sorting stays within sibling groups. */
export function flattenTree(tree: HierarchyTree, expanded: ReadonlySet<string>, sort?: TableSort, columns: TableColumn[] = []): HierarchyNode[] {
  const numeric = columns.find(column => column.key === sort?.key)?.role === 'metric'
  const decimals = new Map<string, DecimalParts | undefined>()
  const decimal = (node: HierarchyNode, value: unknown) => {
    if (!decimals.has(node.id)) decimals.set(node.id, numericParts(value))
    return decimals.get(node.id)
  }
  const ordered = (nodes: HierarchyNode[]) => {
    if (!sort?.key) return nodes
    return [...nodes].sort((a, b) => {
      const left = sort.key === '__lv_hierarchy' ? a.label : a.row[sort.key]
      const right = sort.key === '__lv_hierarchy' ? b.label : b.row[sort.key]
      const aDecimal = numeric ? decimal(a, left) : undefined
      const bDecimal = numeric ? decimal(b, right) : undefined
      const comparison = left === null || left === undefined ? right === null || right === undefined ? 0 : -1
        : right === null || right === undefined ? 1
          : aDecimal && bDecimal ? compareDecimals(aDecimal, bDecimal)
            : typeof left === 'number' && typeof right === 'number' ? left - right
              : String(left).localeCompare(String(right))
      return sort.direction === 'desc' ? -comparison : comparison
    })
  }
  const visible: HierarchyNode[] = []
  const stack = [...ordered(tree.roots)].reverse()
  while (stack.length) {
    const node = stack.pop()!
    visible.push(node)
    if (expanded.has(node.id)) {
      const children = ordered(node.children)
      for (let index = children.length - 1; index >= 0; index--) stack.push(children[index])
    }
  }
  return visible
}
