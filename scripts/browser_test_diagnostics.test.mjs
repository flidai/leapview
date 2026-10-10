import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import test from 'node:test'
import { ProtocolDiagnostics } from './browser_test_diagnostics.mjs'

const wire = (direction, value) => `2026-10-10T12:00:00.000Z pw:protocol ${direction} ${JSON.stringify(value)}`

test('retains the actual evaluation error and lifecycle without fixture payloads', () => {
  const capture = new ProtocolDiagnostics()
  capture.consume(wire('SEND ►', { id: 7, sessionId: 's1', method: 'Runtime.callFunctionOn', params: { functionDeclaration: 'PRIVATE_CODE', arguments: [{ value: 'PRIVATE_SIGNAL' }] } }))
  capture.consume(wire('◀ RECV', { sessionId: 's1', method: 'Page.frameNavigated', params: { frame: { id: 'f1', url: 'https://private.test/?token=SECRET' } } }))
  const failure = capture.consume(wire('◀ RECV', { id: 7, sessionId: 's1', error: { code: -32000, message: 'Promise was collected' } }))
  assert.equal(failure.failure, true)
  const receipt = capture.snapshot()
  assert.deepEqual(receipt.errors[0], { id: 7, sessionId: 's1', method: 'Runtime.callFunctionOn', code: -32000, message: 'Promise was collected' })
  assert.equal(receipt.events.some(event => event.method === 'Page.frameNavigated'), true)
  assert.equal(receipt.pending.length, 0)
  assert.doesNotMatch(JSON.stringify(receipt), /PRIVATE|SECRET|private.test/)
})

test('uses session and request identity and bounds retained history', () => {
  const capture = new ProtocolDiagnostics()
  for (const sessionId of ['one', 'two']) capture.consume(wire('SEND ►', { id: 1, sessionId, method: 'Runtime.evaluate' }))
  capture.consume(wire('◀ RECV', { id: 1, sessionId: 'one', result: {} }))
  assert.equal(capture.snapshot().pending[0].sessionId, 'two')
  for (let id = 0; id < 400; id++) capture.consume(wire('◀ RECV', { method: 'Runtime.executionContextDestroyed', params: { executionContextId: id } }))
  assert.equal(capture.snapshot().events.length, 80)
  assert.ok(capture.snapshot().omitted > 0)
})

test('retains failed asset requests but omits response bodies and headers', () => {
  const capture = new ProtocolDiagnostics()
  capture.consume(wire('◀ RECV', { method: 'Network.responseReceived', params: { headers: { Authorization: 'SECRET' } } }))
  capture.consume(wire('◀ RECV', { method: 'Network.loadingFailed', params: { requestId: 'asset1', type: 'Script', errorText: 'net::ERR_NETWORK_CHANGED' } }))
  const { elapsedMs, ...event } = capture.snapshot().events[0]
  assert.ok(elapsedMs >= 0)
  assert.deepEqual(event, { method: 'Network.loadingFailed', requestId: 'asset1', type: 'Script', errorText: 'net::ERR_NETWORK_CHANGED' })
  assert.doesNotMatch(JSON.stringify(capture.snapshot()), /SECRET/)
  assert.equal(capture.consume('ordinary assertion output'), null)
})

test('wrapper preserves failures, filters raw protocol and overrides a shared DEBUG_FILE', () => {
  const code = `
    if (process.env.DEBUG_FILE) process.exit(90);
    console.error(${JSON.stringify(wire('SEND ►', { id: 2, method: 'Runtime.evaluate', params: { expression: 'PRIVATE_CODE' } }))});
    console.error(${JSON.stringify(wire('◀ RECV', { id: 2, error: { code: -32000, message: 'Promise was collected' } }))});
    console.error('original assertion failure');
    process.exit(23);
  `
  const result = spawnSync(process.execPath, ['scripts/browser_test_diagnostics.mjs', '--', process.execPath, '-e', code], {
    encoding: 'utf8', env: { ...process.env, DEBUG_FILE: '/unused/shared.log' },
  })
  assert.equal(result.status, 23)
  assert.match(result.stderr, /Promise was collected/)
  assert.match(result.stderr, /original assertion failure/)
  assert.doesNotMatch(result.stderr, /PRIVATE_CODE|pw:protocol/)
})

test('successful child output and exit status are unchanged', () => {
  const result = spawnSync(process.execPath, ['scripts/browser_test_diagnostics.mjs', '--', process.execPath, '-e', 'console.error("13 pass"); console.log("stdout")'], { encoding: 'utf8' })
  assert.equal(result.status, 0)
  assert.equal(result.stderr, '13 pass\n')
  assert.equal(result.stdout, 'stdout\n')
})

test('signal termination remains a failing exit and reports the pending call', () => {
  const code = `console.error(${JSON.stringify(wire('SEND ►', { id: 1, method: 'Runtime.evaluate' }))}); process.kill(process.pid, 'SIGTERM')`
  const result = spawnSync(process.execPath, ['scripts/browser_test_diagnostics.mjs', '--', process.execPath, '-e', code], { encoding: 'utf8' })
  assert.equal(result.status, 143)
  assert.match(result.stderr, /"pending":\[\{"id":1,"method":"Runtime.evaluate"\}\]/)
})

test('interrupting the wrapper cannot turn a graceful child shutdown into success', { timeout: 5_000 }, async () => {
  const code = 'process.on("SIGTERM",()=>process.exit(0));console.log("ready");setInterval(()=>{},1000)'
  const child = spawn(process.execPath, ['scripts/browser_test_diagnostics.mjs', '--', process.execPath, '-e', code], { stdio: ['ignore', 'pipe', 'pipe'] })
  let stderr = ''
  child.stderr.on('data', chunk => { stderr += chunk })
  child.stdout.once('data', () => child.kill('SIGTERM'))
  const exit = await new Promise((resolve, reject) => {
    child.on('error', reject)
    child.on('close', resolve)
  })
  assert.equal(exit, 143)
  assert.match(stderr, /diagnostics: SIGTERM/)
})

test('malformed protocol is counted and never forwarded as raw payload', () => {
  const capture = new ProtocolDiagnostics()
  assert.deepEqual(capture.consume('pw:protocol SEND ► {PRIVATE_TRUNCATED'), { failure: false })
  assert.equal(capture.snapshot().malformed, 1)
  assert.doesNotMatch(JSON.stringify(capture.snapshot()), /PRIVATE/)
})

test('repeated preview errors cannot consume the report for a later evaluation failure', () => {
  const preview = wire('◀ RECV', { id: 3, error: { code: -32000, message: 'Could not find object with given id' } })
  const collected = wire('◀ RECV', { id: 4, error: { code: -32000, message: 'Promise was collected' } })
  const code = `for(let i=0;i<30;i++)console.error(${JSON.stringify(preview)});console.error(${JSON.stringify(collected)});process.exit(1)`
  const result = spawnSync(process.execPath, ['scripts/browser_test_diagnostics.mjs', '--', process.execPath, '-e', code], { encoding: 'utf8' })
  assert.equal(result.status, 1)
  const immediate = result.stderr.split('\n').filter(line => line.startsWith('[browser protocol diagnostics: protocol error]'))
  assert.equal(immediate.length, 2)
  assert.match(immediate[1], /Promise was collected/)
})
