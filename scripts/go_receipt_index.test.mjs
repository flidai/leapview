import assert from 'node:assert/strict'
import { test } from 'node:test'
import { indexGoEvents } from './go_receipt_index.mjs'

const log = events => events.map(event => JSON.stringify({ Time: '2026-10-07T12:00:00Z', ...event })).join('\n') + '\n'
const event = (Action, Package = 'example/app', Test, rest = {}) => ({ Action, Package, ...(Test ? { Test } : {}), ...rest })

test('index retains package and full subtest identity, named failure and skip outcomes', () => {
  const result = indexGoEvents(log([
    event('start'), event('run', undefined, 'TestRouteInventory'), event('run', undefined, 'TestRouteInventory/access/reader#01'),
    event('pass', undefined, 'TestRouteInventory/access/reader#01', { Elapsed: 0.1 }), event('pass', undefined, 'TestRouteInventory'),
    event('run', 'example/other', 'TestRouteInventory'), event('fail', 'example/other', 'TestRouteInventory'), event('fail', 'example/other'),
    event('run', undefined, 'TestOptional'), event('skip', undefined, 'TestOptional'), event('pass'),
  ]))
  assert.equal(result.tests.length, 4)
  assert.ok(result.tests.some(row => row.test === 'TestRouteInventory/access/reader#01' && row.outcome === 'passed' && row.fresh))
  assert.ok(result.tests.some(row => row.package === 'example/other' && row.outcome === 'failed'))
  assert.ok(result.tests.some(row => row.test === 'TestOptional' && row.outcome === 'skipped'))
  assert.deepEqual(result.errors, [])
})

test('cached package output disqualifies replayed named passes and package-only success creates no test pass', () => {
  const result = indexGoEvents(log([event('run', undefined, 'TestRouteInventory'), event('pass', undefined, 'TestRouteInventory'),
    event('output', undefined, undefined, { Output: 'ok  \texample/app\t(cached)\n' }), event('pass'), event('pass', 'example/no-tests')]))
  assert.equal(result.tests[0].fresh, false)
  assert.equal(result.tests[0].cached, true)
  assert.equal(result.tests.length, 1)
  assert.ok(result.packages.some(row => row.package === 'example/no-tests' && row.outcome === 'passed'))
})

test('partial, malformed and duplicate executions retain evidence without becoming fresh passes', () => {
  const partial = indexGoEvents(log([event('run', undefined, 'TestA'), event('pass', undefined, 'TestA'), event('run', undefined, 'TestB')]) + '{truncated')
  assert.equal(partial.tests.find(row => row.test === 'TestB').outcome, 'incomplete')
  assert.ok(partial.tests.every(row => !row.fresh))
  assert.equal(partial.errors.length, 1)
  const duplicate = indexGoEvents(log([event('run', undefined, 'TestA'), event('pass', undefined, 'TestA'), event('run', undefined, 'TestA'), event('pass')]))
  assert.ok(duplicate.errors.length > 0)
  assert.ok(duplicate.tests.every(row => !row.fresh))
})

test('build failures preserve ImportPath events and FailedBuild package identity', () => {
  const result = indexGoEvents(log([{ Action: 'build-output', ImportPath: 'example/broken', Output: 'compile error\n' },
    { Action: 'build-fail', ImportPath: 'example/broken' }, event('fail', 'example/app', undefined, { FailedBuild: 'example/broken' })]))
  assert.equal(result.builds[0].importPath, 'example/broken')
  assert.equal(result.builds[0].outcome, 'failed')
  assert.equal(result.packages[0].failedBuild, 'example/broken')
  assert.equal(result.tests.length, 0)
  assert.deepEqual(result.errors, [])
})

test('timestamp bounds preserve out-of-order event times and invalid timestamp types fail closed', () => {
  const result = indexGoEvents(log([event('run', undefined, 'TestA'),
    event('output', undefined, 'TestA', { Time: '2026-10-07T11:00:00Z', Output: 'output\n' }),
    event('pass', undefined, 'TestA'), event('pass')]))
  assert.equal(result.eventTimeRange.first, '2026-10-07T11:00:00Z')
  assert.equal(result.eventTimeRange.last, '2026-10-07T12:00:00Z')
  const invalid = indexGoEvents(log([event('run', undefined, 'TestA', { Time: 2026 }), event('pass', undefined, 'TestA'), event('pass')]))
  assert.ok(invalid.errors.length > 0)
  assert.equal(invalid.tests[0].fresh, false)
})
