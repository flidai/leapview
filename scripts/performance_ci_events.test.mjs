import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ciExecutionRows } from './performance_ci_events.mjs'

const events = rows => rows.map(row => JSON.stringify({ Package: 'example/app', ...row })).join('\n')
const passed = [{ Action: 'start' }, { Action: 'run', Test: 'TestOne' },
  { Action: 'pass', Test: 'TestOne' }, { Action: 'pass' }]
test('execution parity retains nested executions and skip reasons', () => {
  const result = ciExecutionRows(events([{ Action: 'start' },
    { Action: 'run', Test: 'TestOne' }, { Action: 'run', Test: 'TestOne/service' },
    { Action: 'output', Test: 'TestOne/service', Output: '    app_test.go:42: dedicated PostgreSQL lane\n' },
    { Action: 'output', Test: 'TestOne/service', Output: '--- SKIP: TestOne/service (0.00s)\n' },
    { Action: 'skip', Test: 'TestOne/service' }, { Action: 'pass', Test: 'TestOne' }, { Action: 'pass' }]))
  assert.deepEqual(result, [{ test: 'TestOne', outcome: 'passed', skipOutput: [] },
    { test: 'TestOne/service', outcome: 'skipped', skipOutput: ['    app_test.go:42: dedicated PostgreSQL lane'] }])
})
test('missing named runs, cached packages and malformed streams never satisfy parity', () => {
  assert.throws(() => ciExecutionRows(events([{ Action: 'start' }, { Action: 'pass' }])))
  assert.throws(() => ciExecutionRows(events(passed.filter(row => row.Action !== 'run'))))
  assert.throws(() => ciExecutionRows(events([...passed.slice(0, -1),
    { Action: 'output', Output: 'ok example/app (cached)\n' }, { Action: 'pass' }])))
  assert.throws(() => ciExecutionRows(events(passed) + '\nnot-json'))
  assert.throws(() => ciExecutionRows(events(passed.map(row => row.Action === 'pass' && row.Test ?
    { ...row, Action: 'fail' } : row))))
})
