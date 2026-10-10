import { expect, test } from 'bun:test'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { parse, stringify } from 'yaml'
import { encodeSourceFiles, lowerSourceFiles, candidateSchemas, candidateFragmentSchemas, authoringReference, type Candidate, type SourceFiles } from './prototypes'

const here = import.meta.dir
function files(root: string): SourceFiles {
  const out: SourceFiles = {}
  const walk = (dir: string, prefix: string) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.isDirectory()) walk(join(dir, entry.name), prefix + entry.name + '/')
      else out[prefix + entry.name] = readFileSync(join(dir, entry.name), 'utf8')
    }
  }
  walk(root, '')
  return out
}
const manifest = JSON.parse(readFileSync(join(here, 'manifest.json'), 'utf8'))
const fixture = files(join(here, 'tasks/monthly-create/oracle'))
const edit = (source: SourceFiles, fn: (doc: any) => void) => {
  const copy = { ...source }, key = 'dashboards/evaluation.yaml', doc = parse(copy[key])
  fn(doc); copy[key] = stringify(doc); return copy
}

test('all frozen seeds, oracles and wrong-intent negatives round-trip through both prototypes', () => {
  let qualified = 0
  for (const task of manifest.tasks) for (const root of [task.seedRoot, task.oracleRoot, ...task.negatives.map((n: any) => n.sourceRoot)]) {
    const source = files(join(here, root))
    for (const candidate of ['B', 'C'] as const) {
      const encoded = encodeSourceFiles(source, candidate)
      const lowered = lowerSourceFiles(encoded, candidate)
      expect(Object.keys(lowered).sort()).toEqual(Object.keys(source).sort())
      for (const key of Object.keys(source)) expect(parse(lowered[key])).toEqual(parse(source[key]))
      qualified++
    }
  }
  expect(qualified).toBe(62)
}, 30000)

test('A is byte-preserving and machine schemas/reference are available', () => {
  expect(encodeSourceFiles(fixture, 'A')).toEqual(fixture)
  expect(lowerSourceFiles(fixture, 'A')).toEqual(fixture)
  for (const candidate of ['A', 'B', 'C'] as const) {
    expect(candidateSchemas[candidate].$schema).toContain('2020-12')
    const guide = authoringReference(candidate)
    expect(guide).toContain('purchase_date')
    expect(guide.match(/```yaml/g)).toHaveLength(2)
    for (const schema of Object.values(candidateFragmentSchemas[candidate])) expect(schema.additionalProperties).toBe(false)
  }
})

test('B uses explicit roles, derived presentation and preserves metric/string/object selection order', () => {
  const encoded = encodeSourceFiles(fixture, 'B')
  const doc = parse(encoded['dashboards/evaluation.yaml'])
  expect(doc.apiVersion).toBe('leapview.dashboard-prototype/b1')
  expect(doc.spec.visuals[0].query.group.category.alias).toBe('purchase_month')
  expect(doc.spec.visuals[0].query.dimensions).toBeUndefined()
  expect(doc.spec.visuals[0].presentation).toBeUndefined()
  expect(doc.spec.visuals[1].query.group).toEqual({})
  const series = edit(fixture, d => d.spec.visuals[0].query.dimensions.push('category'))
  const b = encodeSourceFiles(series, 'B')
  expect(parse(b['dashboards/evaluation.yaml']).spec.visuals[0].query.group.series).toBe('category')
  expect(parse(lowerSourceFiles(b, 'B')['dashboards/evaluation.yaml'])).toEqual(parse(series['dashboards/evaluation.yaml']))
})

test('C row lowering preserves original component order and exact relative gaps/heights', () => {
  const source = files(join(here, 'tasks/move-resize/oracle'))
  const encoded = encodeSourceFiles(source, 'C')
  const doc = parse(encoded['dashboards/evaluation.yaml'])
  expect(doc.spec.pages[0].rows).toHaveLength(1)
  expect(doc.spec.pages[0].rows[0].height).toBe(6)
  expect(doc.spec.pages[0].rows[0].items[1].rowSpan).toBe(3)
  const zero = edit(encoded, d => d.spec.pages[0].rows[0].items[0].rowOffset = 0)
  expect(parse(lowerSourceFiles(zero, 'C')['dashboards/evaluation.yaml'])).toEqual(parse(source['dashboards/evaluation.yaml']))
  const permuted = edit(source, d => d.spec.pages[0].components.reverse())
  const c = encodeSourceFiles(permuted, 'C')
  expect(parse(lowerSourceFiles(c, 'C')['dashboards/evaluation.yaml'])).toEqual(parse(permuted['dashboards/evaluation.yaml']))
})

test('strict schemas reject unknowns, incompatible tags, null, nonfinite and bounds without coercion', () => {
  for (const candidate of ['B', 'C'] as const) {
    const source = encodeSourceFiles(fixture, candidate)
    for (const fn of [
      (d: any) => d.spec.extra = true,
      (d: any) => d.spec.layout.columns = null,
      (d: any) => d.spec.layout.gap = -1,
      (d: any) => d.spec.visuals[0].query.limit = Infinity,
      (d: any) => d.spec.visuals[0].query.limit = 2147483648,
      (d: any) => d.spec.visuals[0].query.limit = '30',
      (d: any) => d.spec.visuals[0].id = d.spec.visuals[1].id,
    ]) expect(() => lowerSourceFiles(edit(source, fn), candidate)).toThrow()
    expect(() => lowerSourceFiles(edit(source, d => d.apiVersion = 'leapview.dev/v1'), candidate)).toThrow()
  }
  const b = encodeSourceFiles(fixture, 'B')
  for (const fn of [
    (d: any) => d.spec.visuals[0].presentation = { type: 'cartesian' },
    (d: any) => d.spec.visuals[0].presentation = null,
    (d: any) => d.spec.visuals[0].query.dimensions = [],
    (d: any) => d.spec.visuals[0].query.group = { series: 'category' },
    (d: any) => d.spec.visuals[1].query.group = { category: 'category' },
    (d: any) => d.spec.visuals[0].query.type = 'records',
  ]) expect(() => lowerSourceFiles(edit(b, fn), 'B')).toThrow()
  const c = encodeSourceFiles(fixture, 'C')
  for (const fn of [
    (d: any) => d.spec.pages[0].rows[0].height = 0,
    (d: any) => d.spec.pages[0].rows[0].items[0].rowOffset = null,
    (d: any) => d.spec.pages[0].rows[0].items[0].rowOffset = '0',
    (d: any) => d.spec.pages[0].rows[0].items[0].rowOffset = -1,
    (d: any) => d.spec.pages[0].rows[0].items[0].rowOffset = 2147483648,
    (d: any) => d.spec.pages[0].rows[0].items[0].rowOffset = 1,
    (d: any) => d.spec.pages[0].rows[0].items[0].columnSpan = 0,
    (d: any) => d.spec.pages[0].rows[0].items[0].columnSpan = 13,
    (d: any) => d.spec.pages[0].rows[0].items[0].rowSpan = 9,
    (d: any) => d.spec.pages[0].rows[0].items[0].component = 'missing',
    (d: any) => d.spec.pages[0].rows[0].items.push(d.spec.pages[0].rows[0].items[0]),
    (d: any) => d.spec.pages[0].rows = [],
    (d: any) => d.spec.pages[0].rows[1].id = d.spec.pages[0].rows[0].id,
  ]) expect(() => lowerSourceFiles(edit(c, fn), 'C')).toThrow()
})

test('false, zero gaps, omitted defaults and explicit empty collections retain meaning', () => {
  const source = files(join(here, 'tasks/records-query/oracle'))
  const omitted = edit(source, d => { delete d.spec.layout; d.spec.pages[0].layout = {}; d.spec.visuals[0].query.sort = [] })
  for (const candidate of ['B', 'C'] as const) {
    const lowered = lowerSourceFiles(encodeSourceFiles(omitted, candidate), candidate)
    expect(parse(lowered['dashboards/evaluation.yaml'])).toEqual(parse(omitted['dashboards/evaluation.yaml']))
    expect(parse(lowered['dashboards/evaluation.yaml']).spec.visuals[2].presentation.striped).toBe(false)
  }
})

test('confined fragments retain filenames and root includes; duplicate/escaped/cyclic includes reject', () => {
  const source = files(join(here, 'tasks/second-page-reuse/oracle'))
  for (const candidate of ['B', 'C'] as const) {
    const encoded = encodeSourceFiles(source, candidate)
    const lowered = lowerSourceFiles(encoded, candidate)
    expect(parse(lowered['dashboards/evaluation.yaml']).spec.includes).toEqual(parse(source['dashboards/evaluation.yaml']).spec.includes)
    expect(() => lowerSourceFiles(edit(encoded, d => d.spec.includes.pages = ['../../outside.yaml']), candidate)).toThrow()
    const cycle = { ...encoded, 'dashboards/pages.yaml': stringify({ pages: [], includes: { pages: ['pages.yaml'] } }) }
    expect(() => lowerSourceFiles(cycle, candidate)).toThrow()
    const duplicate = edit(encoded, d => d.spec.includes.pages.push('pages.yaml'))
    expect(() => lowerSourceFiles(duplicate, candidate)).toThrow()
    expect(() => lowerSourceFiles({ ...encoded, 'dashboards/unused.yaml': 'pages: []' }, candidate)).toThrow()
  }
})

test('staggered row-spanning layouts fail clearly rather than changing placements', () => {
  const staggered = edit(fixture, d => {
    d.spec.pages[0].components[0].placement = { column: 1, row: 1, columnSpan: 4, rowSpan: 8 }
    d.spec.pages[0].components[1].placement = { column: 5, row: 2, columnSpan: 4, rowSpan: 1 }
    d.spec.pages[0].components.push({id:'third',type:'visual',visual:'total-revenue',placement:{column:5,row:4,columnSpan:4,rowSpan:1}})
  })
  expect(() => encodeSourceFiles(staggered, 'C')).toThrow('staggered')
})

test('duplicate YAML keys and cyclic source never become prototype input', () => {
  expect(() => lowerSourceFiles({ 'dashboards/evaluation.yaml': 'kind: Dashboard\nkind: Dashboard' }, 'B')).toThrow()
  expect(() => lowerSourceFiles({ 'dashboards/evaluation.yaml': 'spec: &cycle {self: *cycle}' }, 'C')).toThrow()
})



test('literal nested fragment includes use the canonical dashboard root directory', () => {
  const source = files(join(here, 'tasks/second-page-reuse/oracle'))
  const doc = parse(source['dashboards/evaluation.yaml'])
  const pages = parse(source['dashboards/pages.yaml'])
  doc.spec.includes.pages = ['nested/page-list.yaml']
  pages.includes = { pages: ['tail.yaml'] }
  const nested: SourceFiles = { ...source, 'dashboards/evaluation.yaml': stringify(doc), 'dashboards/nested/page-list.yaml': stringify(pages), 'dashboards/tail.yaml': 'pages: []\n' }
  delete nested['dashboards/pages.yaml']
  for (const candidate of ['A', 'B', 'C'] as const) {
    const lowered = lowerSourceFiles(encodeSourceFiles(nested, candidate), candidate)
    for (const key of Object.keys(nested)) expect(parse(lowered[key])).toEqual(parse(nested[key]))
  }
})
