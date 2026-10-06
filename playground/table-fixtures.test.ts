import { expect, test } from 'bun:test'
import { answerWindow, exploreCommand, exploreFixture, previewFixture, sortedTableRows, tableRows, windowedFixture } from './table-fixtures'

test('window response keeps neighboring blocks and preserves request identity after a jump', () => {
  const rows = tableRows()
  const original = windowedFixture()
  const table = answerWindow(original, rows, { block: 'all', start: 517, count: 50, requestSeq: 8, resetVersion: 3, sort: { key: 'id', direction: 'desc' } })
  expect(Object.values(table.blocks!).map(block => block!.start)).toEqual([450, 500, 550])
  const sorted = sortedTableRows(rows, { key: 'id', direction: 'desc' })
  for (const block of Object.values(table.blocks!)) {
    expect(block!.rows).toEqual(sorted.slice(block!.start, block!.start + 50))
    expect(block!.requestSeq).toBe(8)
    expect(block!.resetVersion).toBe(3)
  }
  expect(original.blocks!.a!.start).toBe(0)
  expect(rows[0]!.id).toBe(10001)
})

test('single-slot response leaves the other cached slots intact and handles end of data', () => {
  const original = windowedFixture()
  const table = answerWindow(original, tableRows(), { block: 'b', start: 980, count: 50, requestSeq: 4, resetVersion: 0, sort: {} })
  expect(table.blocks!.a).toBe(original.blocks!.a)
  expect(table.blocks!.c).toBe(original.blocks!.c)
  expect(table.blocks!.b!.rows).toHaveLength(20)
  expect(table.blocks!.b!.rows.at(-1)!.id).toBe(11000)
})

test('numeric sorting is stable and does not mutate fixture input', () => {
  const rows = [{ id: 2, revenue: 10 }, { id: 1, revenue: 10 }, { id: 3, revenue: 2 }]
  expect(sortedTableRows(rows, { column: 'revenue', direction: 'asc' }).map(row => row.id)).toEqual([3, 1, 2])
  expect(sortedTableRows(rows, { key: 'revenue', direction: 'desc' }).map(row => row.id)).toEqual([1, 2, 3])
  expect(rows.map(row => row.id)).toEqual([2, 1, 3])
})

test('preview wrapper translates sort keys without losing block identity', () => {
  const table = answerWindow(windowedFixture(), tableRows(), { block: 'all', start: 100, count: 50, requestSeq: 9, resetVersion: 2, sort: { key: 'revenue', direction: 'desc' } })
  const preview = previewFixture(table)
  expect(preview.sort).toEqual({ column: 'revenue', direction: 'desc' })
  expect(preview.blocks.b!.sort).toEqual(preview.sort)
  expect(preview.blocks.b!.requestSeq).toBe(9)
  expect(preview.blocks.b!.resetVersion).toBe(2)
  expect(preview.blocks.b!.rows).toEqual(table.blocks!.b!.rows)
})

test('empty, loading and failed windows retain distinguishable production states', () => {
  const empty = windowedFixture('empty')
  expect(empty.totalRows).toBe(0)
  expect(Object.values(empty.blocks!).flatMap(block => block!.rows)).toEqual([])
  expect(windowedFixture('loading').loadingBlock).toBe('all')
  expect(windowedFixture('loading').blocks).toEqual({})
  expect(windowedFixture('error').error).toBeTruthy()
  expect(windowedFixture('error').blocks!.a!.rows).toHaveLength(50)
})

test('exploration sort returns ordered results with the requested response sequence', () => {
  const command = { ...exploreCommand(), requestSeq: 7, sort: [{ field: 'revenue', direction: 'desc' as const }] }
  const result = exploreFixture(command, 'populated', true)
  expect(result.requestSeq).toBe(7)
  expect(result.truncated).toBe(true)
  const values = result.rows.map(row => Number(row.revenue))
  expect(values).toEqual([...values].sort((a, b) => b - a))
  expect(exploreFixture(command, 'error').error).toBeTruthy()
})


test('preview fixtures explicitly distinguish loading from settled current results', () => {
  for (const state of ['populated', 'empty', 'loading', 'error'] as const) {
    const preview = previewFixture(windowedFixture(state))
    expect(preview.loading).toBe(state === 'loading')
    expect(preview.stale).toBe(false)
  }
})

test('exploration fixtures initialize canonical selections for result-table sorting', () => {
  const command = exploreCommand()
  expect(command.spec).toEqual({
    schemaVersion: 1, modelId: 'playground-sales', datasetId: 'orders',
    dimensions: command.dimensions.map(field => ({ field })),
    metrics: command.metrics.map(field => ({ field })),
    filters: [], sort: [], limit: 75,
  })
})

test('exploration windows acknowledge table sequence independently of semantic sequence', () => {
  const command = {
    ...exploreCommand(), requestSeq: 40, resetVersion: 3,
    sort: [{ field: 'revenue', direction: 'desc' as const }],
    window: { block: 'all' as const, start: 50, count: 25, requestSeq: 8, resetVersion: 3 },
  }
  const result = exploreFixture(command)
  expect(result.requestSeq).toBe(40)
  expect(result.window?.totalRows).toBe(75)
  expect(result.window?.blocks.b?.rows).toEqual(result.rows.slice(50, 75))
  expect(result.window?.blocks.b).toMatchObject({ start: 50, requestSeq: 8, resetVersion: 3, sort: { column: 'revenue', direction: 'desc' } })
  expect(result.window?.blocks.c).toMatchObject({ start: 75, requestSeq: 8, resetVersion: 3, rows: [] })
})
