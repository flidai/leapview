/**
 * Evaluation-only source formats. These exports are not imported by production
 * discovery, builder, APIs or runtime. Every accepted submission is lowered to
 * the generated canonical v1 boundary before real Go compilation.
 */
import Ajv, { type ValidateFunction } from 'ajv/dist/2020'
import addFormats from 'ajv-formats'
import { posix } from 'node:path'
import { parse, stringify } from 'yaml'
import canonicalSchema from '../../schemas/json/dashboard-document.schema.json'

export type Candidate = 'A' | 'B' | 'C'
/** Source-root-relative YAML/JSON dashboard paths; includes retain their names. */
export type SourceFiles = Record<string, string>
type Shape = Record<string, any>
const versions = { A: 'leapview.dev/v1', B: 'leapview.dashboard-prototype/b1', C: 'leapview.dashboard-prototype/c1' }
const marks = ['area', 'bar', 'combo', 'kpi', 'table']
const clone = <T>(value: T): T => structuredClone(value)
const family = (mark: string) => mark === 'kpi' ? 'kpi' : mark === 'table' ? 'table' : 'cartesian'
const object = (properties: Shape, required: string[] = Object.keys(properties)): Shape =>
  ({ type: 'object', properties, required, additionalProperties: false })
const ref = (name: string) => ({ $ref: '#/$defs/' + name })

/** Flatten only generated object inheritance; never restate its public fields. */
function flattened(defs: Shape, name: string): Shape {
  const node = defs[name]
  if (!node || node.type !== 'object') throw new Error('Expected generated object definition ' + name)
  const properties: Shape = {}, required: string[] = []
  for (const parent of node.allOf || []) {
    if (typeof parent.$ref !== 'string' || !parent.$ref.startsWith('#/$defs/')) throw new Error('Unsupported generated object inheritance')
    const shape = flattened(defs, parent.$ref.slice(8))
    Object.assign(properties, shape.properties); required.push(...shape.required)
  }
  Object.assign(properties, clone(node.properties || {})); required.push(...(node.required || []))
  return object(properties, [...new Set(required)])
}

function machineSchema(candidate: Candidate): Shape {
  const schema: Shape = clone(canonicalSchema)
  if (candidate === 'A') return schema
  delete schema['x-apigen-contracts']
  schema.title = 'Evaluation-only Dashboard prototype ' + candidate
  const defs = schema.$defs
  defs.DashboardApiVersion = { type: 'string', const: versions[candidate] }
  defs.DashboardVisualType.enum = marks
  if (candidate === 'B') {
    const aggregate = flattened(defs, 'AggregateDashboardQuery')
    delete aggregate.properties.dimensions
    aggregate.required = aggregate.required.filter((key: string) => key !== 'dimensions')
    const group = object({ category: ref('DashboardDimensionSelection'), series: ref('DashboardDimensionSelection') }, [])
    group.dependentRequired = { series: ['category'] }
    aggregate.properties.group = group
    aggregate.required.push('group')
    defs.PrototypeAggregate = aggregate
    const branches = marks.map(mark => {
      const visual = flattened(defs, 'NamedDashboardVisual')
      visual.properties.type = { const: mark, type: 'string' }
      visual.required = visual.required.filter((key: string) => key !== 'presentation')
      const presentation = flattened(defs, mark === 'kpi' ? 'KPIDashboardPresentation' : mark === 'table' ? 'TableDashboardPresentation' : 'CartesianDashboardPresentation')
      delete presentation.properties.type
      presentation.required = presentation.required.filter((key: string) => key !== 'type')
      if (mark !== 'combo') delete presentation.properties.series
      visual.properties.presentation = presentation
      if (mark === 'table') visual.properties.query = ref('RecordsDashboardQuery')
      else {
        const query = clone(aggregate)
        if (mark === 'kpi') {
          query.properties.group = object({})
          query.properties.metrics.minItems = 1
          query.properties.metrics.maxItems = 1
        } else {
          query.properties.group.required = ['category']
          if (mark === 'combo') delete query.properties.group.properties.series
        }
        visual.properties.query = query
      }
      return visual
    })
    defs.NamedDashboardVisual = { oneOf: branches }
  } else {
    const base = defs.DashboardPageComponentBase
    delete base.properties.placement
    base.required = base.required.filter((key: string) => key !== 'placement')
    const positive = { type: 'integer', minimum: 1, maximum: 2147483647 }
    const gap = { type: 'integer', minimum: 0, maximum: 2147483647 }
    const identity = clone(base.properties.id)
    defs.PrototypeRowItem = object({
      component: identity, columnSpan: positive, rowSpan: positive, gapBefore: gap, rowOffset: gap,
    }, ['component', 'columnSpan'])
    defs.PrototypeRow = object({
      id: identity, height: positive, gapBefore: gap,
      items: { type: 'array', minItems: 1, items: ref('PrototypeRowItem') },
    }, ['id', 'height', 'items'])
    defs.DashboardPage.properties.rows = { type: 'array', items: ref('PrototypeRow') }
    defs.DashboardPage.required.push('rows')
  }
  return schema
}

export const candidateSchemas: Record<Candidate, Shape> = {
  A: machineSchema('A'), B: machineSchema('B'), C: machineSchema('C'),
}

const ajv = new Ajv({ strict: false, allErrors: true, validateFormats: true, strictNumbers: true })
addFormats(ajv, ['date', 'date-time'])
ajv.addFormat('double', { type: 'number', validate: Number.isFinite })
ajv.addFormat('int32', { type: 'number', validate: (value: number) => Number.isInteger(value) && value >= -2147483648 && value <= 2147483647 })
ajv.addFormat('int64', { type: 'number', validate: (value: number) => Number.isSafeInteger(value) })
const validators = new Map<string, ValidateFunction>()
function validator(candidate: Candidate, collection?: string): ValidateFunction {
  const key = candidate + ':' + (collection || 'document')
  let validate = validators.get(key)
  if (!validate) {
    const schema = clone(candidateSchemas[candidate])
    if (collection) {
      const names: Record<string, string> = { visuals: 'NamedDashboardVisual', pages: 'DashboardPage', filters: 'DashboardFilter' }
      delete schema.anyOf
      Object.assign(schema, object({
        [collection]: { type: 'array', items: ref(names[collection]) },
        includes: ref('DashboardIncludes'),
      }, [collection]))
    }
    validate = ajv.compile(schema)
    validators.set(key, validate)
  }
  return validate
}
function assertSchema(value: Shape, candidate: Candidate, path: string, collection?: string) {
  const validate = validator(candidate, collection)
  if (!validate(value)) throw new Error(path + ': ' + ajv.errorsText(validate.errors, { separator: '; ' }))
}
function finiteJSON(value: unknown, ancestors = new Set<unknown>()) {
  if (typeof value === 'number' && !Number.isFinite(value)) throw new Error('Nonfinite source number')
  if (value === null || typeof value !== 'object') return
  if (ancestors.has(value)) throw new Error('Cyclic source value')
  ancestors.add(value)
  for (const child of Object.values(value)) finiteJSON(child, ancestors)
  ancestors.delete(value)
}
function safePath(path: string) {
  if (!path || posix.isAbsolute(path) || path.includes('\\') || path.split('/').some(part => part === '..' || part === '.' || !part)) throw new Error('Unconfined source path ' + path)
  if (!/\.(yaml|yml|json)$/.test(path)) throw new Error('Unsupported source file ' + path)
}
function unique(values: Shape[], context: string) {
  const seen = new Set<string>()
  for (const value of values) {
    if (seen.has(value.id)) throw new Error(context + ': duplicate identity ' + value.id)
    seen.add(value.id)
  }
}

interface Inspected { values: Record<string, Shape>; roles: Record<string, string | undefined> }
/** Literal-path common capability, deliberately narrower than production globs. */
function inspect(files: SourceFiles, candidate: Candidate): Inspected {
  const values: Record<string, Shape> = {}, roles: Record<string, string | undefined> = {}
  for (const [path, text] of Object.entries(files)) {
    safePath(path)
    if (typeof text !== 'string') throw new Error('Source text must be a string')
    const value = parse(text)
    finiteJSON(value)
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error(path + ': expected source object')
    values[path] = value
  }
  const roots = Object.keys(values).filter(path => values[path].kind === 'Dashboard')
  if (roots.length !== 1) throw new Error('Evaluation source set requires exactly one Dashboard root')
  const active = new Set<string>()
  const collect = (path: string, collection?: string): Record<string, Shape[]> => {
    if (active.has(path)) throw new Error('Fragment include cycle: ' + path)
    if (!(path in values)) throw new Error('Missing included source: ' + path)
    if (path in roles) throw new Error('Repeated fragment source: ' + path)
    roles[path] = collection
    active.add(path)
    assertSchema(values[path], candidate, path, collection)
    const content = collection ? values[path] : values[path].spec
    const included: Record<string, Shape[]> = { visuals: [], filters: [], pages: [] }
    if (content.includes?.components !== undefined) throw new Error('Component fragments are outside the qualified prototype capability')
    for (const name of ['visuals', 'filters', 'pages']) {
      for (const target of content.includes?.[name] || []) {
        if (typeof target !== 'string' || posix.isAbsolute(target) || target.includes('\\') || /[*?\[\]{}]/.test(target)) throw new Error('Only confined literal include paths are qualified')
        const resolved = posix.normalize(posix.join(posix.dirname(roots[0]), target))
        safePath(resolved)
        const nested = collect(resolved, name)
        for (const key of ['visuals', 'filters', 'pages']) included[key].push(...nested[key])
      }
      included[name].push(...(content[name] || []))
    }
    active.delete(path)
    return included
  }
  const expanded = collect(roots[0])
  if (Object.keys(roles).length !== Object.keys(values).length) throw new Error('Unreferenced evaluation source file')
  for (const name of ['visuals', 'filters', 'pages']) unique(expanded[name], name)
  for (const page of expanded.pages) {
    unique(page.components, 'page ' + page.id + ' components')
    unique(page.filterBindings || [], 'page ' + page.id + ' filter bindings')
  }
  return { values, roles }
}

function transformVisual(visual: Shape, direction: 'encode' | 'lower') {
  if (!marks.includes(visual.type)) throw new Error('Unsupported prototype visual ' + visual.type)
  if (direction === 'encode') {
    if (visual.query.type === 'aggregate') {
      const dimensions = visual.query.dimensions
      if (dimensions.length > 2 || (visual.type === 'kpi' && dimensions.length) || (visual.type === 'combo' && dimensions.length > 1)) throw new Error('Unsupported grouping for prototype ' + visual.type)
      visual.query.group = {}
      if (dimensions.length) visual.query.group.category = dimensions[0]
      if (dimensions.length === 2) visual.query.group.series = dimensions[1]
      delete visual.query.dimensions
    }
    if (visual.presentation.type !== family(visual.type)) throw new Error('Visual/presentation mismatch')
    delete visual.presentation.type
    if (!Object.keys(visual.presentation).length) delete visual.presentation
  } else {
    if (visual.query.type === 'aggregate') {
      const group = visual.query.group
      visual.query.dimensions = []
      if (Object.hasOwn(group, 'category')) visual.query.dimensions.push(group.category)
      if (Object.hasOwn(group, 'series')) visual.query.dimensions.push(group.series)
      delete visual.query.group
    }
    visual.presentation = { ...(visual.presentation || {}), type: family(visual.type) }
  }
}
function int32(value: number, context: string) {
  if (!Number.isSafeInteger(value) || value < 1 || value > 2147483647) throw new Error(context + ': coordinate outside int32 bounds')
  return value
}
function encodePage(page: Shape) {
  const placed = page.components.map((component: Shape) => ({ id: component.id, ...component.placement }))
    .sort((a: Shape, b: Shape) => a.row - b.row || a.column - b.column)
  const rows: Shape[] = []
  for (const placement of placed) {
    let row = rows[rows.length - 1]
    if (!row || placement.row >= row.start + row.height) {
      const before = row ? row.start + row.height : 1
      const gapBefore = placement.row - before
      if (gapBefore < 0) throw new Error('Unsupported staggered row-spanning layout')
      row = { id: 'row-' + placement.id, start: placement.row, height: placement.rowSpan, gapBefore, items: [] }
      rows.push(row)
    }
    row.height = Math.max(row.height, placement.row - row.start + placement.rowSpan)
    const previous = row.items[row.items.length - 1]
    const before = previous ? previous.column + previous.columnSpan : 1
    const gapBefore = placement.column - before
    if (gapBefore < 0) throw new Error('Unsupported staggered row-spanning layout: overlapping columns')
    row.items.push({ rowOffset: placement.row - row.start, component: placement.id, column: placement.column, columnSpan: placement.columnSpan, rowSpan: placement.rowSpan, gapBefore })
  }
  // A later group's extent may invalidate an earlier start calculation.
  for (let index = 1; index < rows.length; index++) {
    if (rows[index].start < rows[index - 1].start + rows[index - 1].height) throw new Error('Unsupported staggered row-spanning layout')
  }
  page.rows = rows.map(row => ({
    id: row.id, height: row.height, ...(row.gapBefore ? { gapBefore: row.gapBefore } : {}),
    items: row.items.map((item: Shape) => ({
      component: item.component, columnSpan: item.columnSpan,
      ...(item.rowSpan !== row.height ? { rowSpan: item.rowSpan } : {}),
      ...(item.rowOffset ? { rowOffset: item.rowOffset } : {}),
      ...(item.gapBefore ? { gapBefore: item.gapBefore } : {}),
    })),
  }))
  for (const component of page.components) delete component.placement
}
function lowerPage(page: Shape, columns: number) {
  unique(page.rows, 'page ' + page.id + ' rows')
  const components = new Map<string, Shape>(page.components.map((component: Shape) => [component.id, component]))
  const placed = new Set<string>()
  let rowStart = 1
  for (const row of page.rows) {
    rowStart = int32(rowStart + (row.gapBefore ?? 0), 'row start')
    let column = 1
    for (const item of row.items) {
      column = int32(column + (item.gapBefore ?? 0), 'column start')
      const component = components.get(item.component)
      if (!component) throw new Error('Unknown row component ' + item.component)
      if (placed.has(item.component)) throw new Error('Duplicate row component ' + item.component)
      placed.add(item.component)
      const rowSpan = item.rowSpan ?? row.height
      const itemRow = int32(rowStart + (item.rowOffset ?? 0), 'item row')
      if ((item.rowOffset ?? 0) + rowSpan > row.height) throw new Error('Item rowSpan exceeds row height')
      if (column + item.columnSpan - 1 > columns) throw new Error('Row item exceeds grid columns')
      int32(itemRow + rowSpan - 1, 'row extent')
      component.placement = { column, row: itemRow, columnSpan: item.columnSpan, rowSpan }
      column += item.columnSpan
    }
    rowStart += row.height
  }
  if (placed.size !== components.size) throw new Error('Every component needs exactly one row placement')
  delete page.rows
}

function transform(files: SourceFiles, candidate: Candidate, direction: 'encode' | 'lower'): SourceFiles {
  const inputCandidate = direction === 'encode' ? 'A' : candidate
  const inspected = inspect(files, inputCandidate)
  if (candidate === 'A') return { ...files }
  const values = clone(inspected.values)
  const root = Object.values(values).find(value => value.kind === 'Dashboard')!
  const defaultColumns = root.spec.layout?.columns ?? 12
  for (const [path, value] of Object.entries(values)) {
    const content = value.kind === 'Dashboard' ? value.spec : value
    if (candidate === 'B') for (const visual of content.visuals || []) transformVisual(visual, direction)
    if (candidate === 'C') for (const page of content.pages || []) {
      if (direction === 'encode') encodePage(page)
      else lowerPage(page, page.layout?.columns ?? defaultColumns)
    }
    if (value.kind === 'Dashboard') value.apiVersion = direction === 'encode' ? versions[candidate] : versions.A
  }
  const output = Object.fromEntries(Object.entries(values).map(([path, value]) => [path, stringify(value)]))
  inspect(output, direction === 'encode' ? candidate : 'A')
  return output
}

export function encodeSourceFiles(files: SourceFiles, candidate: Candidate): SourceFiles {
  return transform(files, candidate, 'encode')
}
export function lowerSourceFiles(files: SourceFiles, candidate: Candidate): SourceFiles {
  return transform(files, candidate, 'lower')
}

const commonReference = [
  'Evaluation corpus capabilities: area/bar/combo/kpi aggregate visuals and table records. Governed model sales exposes purchase_date (Date; day/week/month), category and state dimensions; revenue and order_count metrics. No SQL or renderer options.',
  'Dimension selections are strings or {dimension,grain?,alias?}; metrics are ordered strings or {metric,alias?}; records fields are ordered strings or {field,alias?}. Sort fields reference result names; direction asc/desc and authored limits remain explicit.',
  'Preserve all stable IDs, untouched metadata/filters/page bindings/queries, ordered metrics/sorts/record fields/pages/components and shared visual references. Edit by ID, never by title or array index.',
  'Layout defaults are columns 12, rowHeight 48, gap 16, padding 16. Omission inherits defaults; page overrides preserve omitted keys. Explicit false and empty collections are retained. Null is rejected where generated canonical schema rejects it.',
  'Distinct filter options retain dataset/limit/dependsOn; page bindings retain component targets. Reusing a visual adds a reference, not a second visual definition. Records query dataset sales_orders exposes order_id, purchase_date, category and revenue.',
  'Confined literal visuals/pages/filters includes preserve source filenames and resolve from the dashboard root directory. Fragment objects contain the matching visuals, pages or filters array, with optional includes. No globs/component fragments or unreferenced files in this qualified subset. Duplicate identities, cyclic includes and source-root escape reject.',
].join('\n')
/** Equal common semantic guidance; only the source-form mapping varies. */
export function authoringReference(candidate: Candidate): string {
  const mapping = candidate === 'A'
    ? 'A: apiVersion leapview.dev/v1. Aggregate dimensions are ordered [category,series?], or [] for KPI. Explicit query.type. Required presentation.type cartesian/kpi/table. Components own placement {column,row,columnSpan,rowSpan}.'
    : candidate === 'B'
      ? 'B: apiVersion leapview.dashboard-prototype/b1. Explicit query.type; aggregate query.group has category and optional series; category lowers first, series second. KPI group:{}; table uses canonical records fields. presentation has no type; omit it for an empty policy. Its family derives from visual.type. Combo presentation.series preserves ordered {field,mark,axis}. Canonical dimensions and presentation.type are forbidden.'
      : 'C: apiVersion leapview.dashboard-prototype/c1. Visual/query/presentation fields are canonical. Components remain in original compact reading order and omit placement. Pages require rows:[{id,height,gapBefore?,items:[{component,columnSpan,rowSpan?,gapBefore?,rowOffset?}]}]. Start at grid row/column 1. Rows advance by previous height plus gapBefore; items advance by previous span plus gapBefore. Item rowSpan defaults to row height; rowOffset defaults to zero and offsets within its row block, requiring rowOffset+rowSpan<=height. Every component is placed once. Row IDs are stable local authoring IDs; row/item order does not reorder components.'
  const examples = referenceExamples.map((example, index) => {
    const source = encodeSourceFiles({ 'dashboards/example.yaml': stringify(example) }, candidate)
    return 'Example ' + (index + 1) + ':\n```yaml\n' + source['dashboards/example.yaml'] + '```'
  }).join('\n\n')
  return commonReference + '\n' + mapping + '\n\n' + examples + '\n'
}


// Exactly two shared semantic examples are mechanically encoded for every arm.
const exampleRoot = (visuals: Shape[], components: Shape[]): Shape => ({
  apiVersion: versions.A, kind: 'Dashboard',
  metadata: { id: 'dashboard:example', name: 'example', displayName: 'Example' },
  spec: { semanticModel: 'sales', filters: [], visuals, pages: [{ id: 'summary', title: 'Summary', components }] },
})
const referenceExamples = [
  exampleRoot([
    { id: 'monthly-sales', title: 'Monthly sales', type: 'area', query: { type: 'aggregate', dimensions: [{ dimension: 'purchase_date', grain: 'month', alias: 'month_key' }], metrics: ['revenue'], sort: [{ field: 'month_key', direction: 'asc' }], limit: 24 }, presentation: { type: 'cartesian' } },
    { id: 'sales-total', title: 'Sales total', type: 'kpi', query: { type: 'aggregate', dimensions: [], metrics: ['revenue'] }, presentation: { type: 'kpi', displayUnits: 'auto' } },
  ], [
    { id: 'monthly-panel', type: 'visual', visual: 'monthly-sales', placement: { column: 1, row: 1, columnSpan: 8, rowSpan: 6 } },
    { id: 'total-panel', type: 'visual', visual: 'sales-total', placement: { column: 9, row: 1, columnSpan: 4, rowSpan: 3 } },
  ]),
  exampleRoot([
    { id: 'order-details', title: 'Order details', type: 'table', query: { type: 'records', dataset: 'sales_orders', fields: ['order_id', 'purchase_date', 'revenue'], sort: [{ field: 'purchase_date', direction: 'desc' }], limit: 25 }, presentation: { type: 'table', rowHeight: 32, showHeader: true, striped: false } },
  ], [
    { id: 'details-panel', type: 'visual', visual: 'order-details', placement: { column: 1, row: 1, columnSpan: 12, rowSpan: 8 } },
  ]),
]

/** Closed fragment wrappers for the same generated definitions as the root schema. */
export const candidateFragmentSchemas = Object.fromEntries((['A', 'B', 'C'] as const).map(candidate => [candidate,
  Object.fromEntries(['visuals', 'pages', 'filters'].map(collection => {
    const schema = clone(candidateSchemas[candidate])
    const names: Record<string, string> = { visuals: 'NamedDashboardVisual', pages: 'DashboardPage', filters: 'DashboardFilter' }
    delete schema.anyOf
    Object.assign(schema, object({ [collection]: { type: 'array', items: ref(names[collection]) }, includes: ref('DashboardIncludes') }, [collection]))
    return [collection, schema]
  })),
])) as Record<Candidate, Record<string, Shape>>
