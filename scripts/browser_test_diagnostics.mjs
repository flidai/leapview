import { spawn } from 'node:child_process'
import { constants } from 'node:os'
import { createInterface } from 'node:readline'
import { pathToFileURL } from 'node:url'

const evaluationMethods = new Set(['Runtime.evaluate', 'Runtime.callFunctionOn', 'Runtime.awaitPromise'])
const lifecycleMethods = new Set([
  'Runtime.executionContextCreated', 'Runtime.executionContextDestroyed', 'Runtime.executionContextsCleared',
  'Page.frameNavigated', 'Page.frameDetached', 'Inspector.targetCrashed',
  'Target.detachedFromTarget', 'Target.targetDestroyed', 'Network.loadingFailed',
])

// Keep protocol identity and errors, never expressions, signal values, URLs,
// headers or response bodies. A separate collector belongs to each test process.
export class ProtocolDiagnostics {
  pending = new Map()
  events = []
  errors = []
  omitted = 0
  malformed = 0
  startedAt = performance.now()

  consume(line) {
    const marker = line.indexOf('pw:protocol ')
    if (marker < 0) return null
    const json = line.indexOf('{', marker)
    let message
    try { message = JSON.parse(line.slice(json)) } catch { this.malformed++; return { failure: false } }
    const sending = line.slice(marker, json).includes('SEND')
    const { id, sessionId, method, params = {}, error } = message
    const key = `${sessionId ?? ''}:${id}`
    const identity = { ...(id === undefined ? {} : { id }), ...(sessionId === undefined ? {} : { sessionId }) }
    if (sending) {
      if (evaluationMethods.has(method)) {
        if (this.pending.size === 80) { this.pending.delete(this.pending.keys().next().value); this.omitted++ }
        this.pending.set(key, { ...identity, method })
        this.record({ ...identity, method, event: 'sent' })
      }
    } else if (id !== undefined) {
      const request = this.pending.get(key)
      this.pending.delete(key)
      if (error) {
        const failure = { ...identity, method: request?.method ?? 'other', code: error.code, message: String(error.message).slice(0, 300) }
        if (this.errors.length === 20) { this.errors.shift(); this.omitted++ }
        this.errors.push(failure)
        this.record({ ...failure, event: 'error' })
        return { failure: true, errorKey: JSON.stringify([failure.method, failure.code, failure.message]) }
      }
      if (request) this.record({ ...request, event: 'completed' })
    } else if (lifecycleMethods.has(method)) {
      const event = { ...identity, method }
      if (params.frame?.id) event.frameId = params.frame.id
      if (params.context?.id) event.executionContextId = params.context.id
      for (const field of ['executionContextId', 'frameId', 'targetId', 'requestId', 'type', 'errorText', 'canceled']) {
        if (params[field] !== undefined) event[field] = params[field]
      }
      this.record(event)
    }
    return { failure: false }
  }

  record(event) {
    if (this.events.length === 80) { this.events.shift(); this.omitted++ }
    this.events.push({ ...event, elapsedMs: Math.round(performance.now() - this.startedAt) })
  }

  snapshot() {
    return { errors: this.errors, pending: [...this.pending.values()], events: this.events, omitted: this.omitted, malformed: this.malformed }
  }
}

export async function run(command, args) {
  const env = { ...process.env, DEBUG: 'pw:protocol', DEBUG_COLORS: '0' }
  // Playwright opens this path with truncation in every child process.
  delete env.DEBUG_FILE
  const capture = new ProtocolDiagnostics()
  const child = spawn(command, args, { env, stdio: ['inherit', 'inherit', 'pipe'] })
  const reportedErrors = new Set()
  let interruptedBy
  const report = reason => {
    console.error(`[browser protocol diagnostics: ${reason}] ${JSON.stringify(capture.snapshot())}`)
  }
  const lines = createInterface({ input: child.stderr })
  lines.on('line', line => {
    const protocol = capture.consume(line)
    if (!protocol) process.stderr.write(`${line}\n`)
    // Emit before test cleanup/outer watchdog can erase the first failure.
    else if (protocol.failure && !reportedErrors.has(protocol.errorKey) && reportedErrors.size < 20) {
      reportedErrors.add(protocol.errorKey)
      report('protocol error')
    }
  })
  const handlers = new Map(['SIGINT', 'SIGTERM', 'SIGQUIT'].map(signal => [signal, () => {
    interruptedBy ??= signal
    report(signal)
    child.kill(signal)
  }]))
  for (const [signal, handler] of handlers) process.on(signal, handler)
  try {
    return await new Promise(resolve => {
      child.on('error', error => { console.error(error.message); resolve(127) })
      child.on('close', (code, signal) => {
        const exitCode = interruptedBy ? 128 + constants.signals[interruptedBy] : code ?? 128 + (constants.signals[signal] ?? 1)
        if (exitCode !== 0) report(`exit ${exitCode}`)
        resolve(exitCode)
      })
    })
  } finally {
    for (const [signal, handler] of handlers) process.off(signal, handler)
    lines.close()
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const [, , separator, command, ...args] = process.argv
  if (separator !== '--' || !command) { console.error('usage: browser_test_diagnostics.mjs -- command [arguments]'); process.exitCode = 2 }
  else process.exitCode = await run(command, args)
}
