// Native bounded capture and verification of named Go execution receipts.
import { execFileSync, spawn } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { appendFileSync, existsSync, mkdirSync, readFileSync, realpathSync, readdirSync, writeFileSync } from 'node:fs'
import { delimiter, dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'
import { checkoutSnapshot, sha256 } from './audit_source.mjs'
import { indexGoEvents } from './go_receipt_index.mjs'

const limitation = 'Selected Go/native/embed source inputs include ignored generated files. Selected effective Go build settings and GOMAXPROCS/GOMEMLIMIT/GODEBUG are bound by digest without exposing values. Compiler-generated intermediates, external system headers/libraries, other runtime environment variables and service/testdata inputs remain outside this identity; this is not a complete hermetic compilation or journey identity.'
const equal = (a, b) => JSON.stringify(a) === JSON.stringify(b)
const timeoutMS = value => {
  const match = /^(\d+)(ms|s|m)$/.exec(value)
  const duration = match ? Number(match[1]) * { ms: 1, s: 1000, m: 60000 }[match[2]] : NaN
  if (!(duration > 0 && duration <= 600000)) throw new Error('timeout must be between 1ms and 10m')
  return duration
}
function optionsFor(options) {
  const result = { packages: options.packages ?? ['./internal/app'], run: options.run ?? '^TestRouteInventory$', tags: options.tags ?? [], cgo: options.cgo ?? '1', timeout: options.timeout ?? '2m' }
  if (!Array.isArray(result.packages) || !result.packages.length || !result.packages.every(path => typeof path === 'string' && /^(?:\.\/|[a-zA-Z0-9_])[a-zA-Z0-9_.\/-]*$/.test(path))) throw new Error('invalid Go package arguments')
  if (typeof result.run !== 'string' || !Array.isArray(result.tags) || !result.tags.every(tag => /^[a-zA-Z0-9_]+$/.test(tag)) || !['0', '1'].includes(result.cgo)) throw new Error('invalid Go test options')
  timeoutMS(result.timeout)
  return result
}
const environment = options => ({ ...process.env, GOFLAGS: '', GOENV: 'off', GOWORK: 'off', GOTOOLCHAIN: 'local', CGO_ENABLED: options.cgo })
const buildArgs = options => ['-p=1', '-mod=readonly', `-tags=${options.tags.join(',')}`]
const testArgs = options => ['test', '-json', '-count=1', ...buildArgs(options), `-timeout=${options.timeout}`, `-run=${options.run}`, ...options.packages]
function goTool(root, options) {
  const executable = (process.env.PATH ?? '').split(delimiter).map(path => join(path, 'go')).find(existsSync)
  if (!executable) throw new Error('Go executable is unavailable')
  const path = realpathSync(executable)
  const env = environment(options)
  const identityFields = ['GOVERSION', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'GOROOT']
  const buildFields = ['GO386', 'GOAMD64', 'GOARM', 'GOARM64', 'GOMIPS', 'GOMIPS64', 'GOPPC64', 'GORISCV64', 'GOWASM', 'GOEXPERIMENT',
    'CC', 'CXX', 'AR', 'PKG_CONFIG', 'CGO_CFLAGS', 'CGO_CPPFLAGS', 'CGO_CXXFLAGS', 'CGO_FFLAGS', 'CGO_LDFLAGS']
  const fields = JSON.parse(execFileSync(path, ['env', '-json', ...identityFields, ...buildFields], { cwd: root, env, encoding: 'utf8', timeout: 30000, maxBuffer: 1024 * 1024 }))
  const digests = (names, values) => Object.fromEntries(names.map(name => [name, values[name] === undefined ? null : sha256(String(values[name]))]))
  return { ...Object.fromEntries(identityFields.map(name => [name, fields[name]])), executable: path, executableSHA256: sha256(readFileSync(path)),
    buildSettingsSHA256: digests(buildFields, fields), runtimeEnvironmentSHA256: digests(['GOMAXPROCS', 'GOMEMLIMIT', 'GODEBUG'], env) }
}
function jsonObjects(text) {
  const objects = []
  let start = -1, depth = 0, quoted = false, escaped = false
  for (let i = 0; i < text.length; i++) {
    const char = text[i]
    if (start < 0) {
      if (/\s/.test(char)) continue
      if (char !== '{') throw new Error('invalid go list JSON stream')
      start = i
    }
    if (quoted) { if (escaped) escaped = false; else if (char === '\\') escaped = true; else if (char === '"') quoted = false }
    else if (char === '"') quoted = true
    else if (char === '{') depth++
    else if (char === '}' && --depth === 0) { objects.push(JSON.parse(text.slice(start, i + 1))); start = -1 }
  }
  if (start >= 0 || !objects.length) throw new Error('incomplete go list JSON stream')
  return objects
}
function fileInput(path) {
  const actual = realpathSync(path), bytes = readFileSync(actual)
  return { path, actualPath: actual, bytes: bytes.length, sha256: sha256(bytes) }
}
function goInputs(root, options, toolchain) {
  const argv = ['list', '-e', '-deps', '-test', '-json', ...buildArgs(options), ...options.packages]
  const rows = jsonObjects(execFileSync(toolchain.executable, argv, { cwd: root, env: environment(options), encoding: 'utf8', maxBuffer: 64 * 1024 * 1024, timeout: 120000 }))
  const paths = new Set()
  for (const pkg of rows) {
    for (const field of ['GoFiles', 'CgoFiles', 'TestGoFiles', 'XTestGoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'EmbedFiles', 'TestEmbedFiles', 'XTestEmbedFiles']) {
      for (const name of pkg[field] ?? []) paths.add(resolve(pkg.Dir, name))
    }
    if (pkg.Module?.GoMod) {
      paths.add(pkg.Module.GoMod)
      const sum = join(dirname(pkg.Module.GoMod), 'go.sum')
      if (existsSync(sum)) paths.add(sum)
    }
  }
  return { scope: 'go-list-selected-source-native-and-embed-files', complete: rows.every(pkg => !pkg.Error && !pkg.DepsErrors?.length),
    probeArgv: argv, files: [...paths].sort().map(fileInput) }
}
export async function runGoProcess(executable, argv, root, env, out, wallMS, graceMS = 1000) {
  const stdout = join(out, 'stdout.jsonl'), stderr = join(out, 'stderr.log')
  writeFileSync(stdout, '', { flag: 'wx' }); writeFileSync(stderr, '', { flag: 'wx' })
  const startedAt = new Date().toISOString()
  return await new Promise(resolveRun => {
    const child = spawn(executable, argv, { cwd: root, env, detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'pipe'] })
    let terminationReason = null, processError = null, bytes = 0, escalation, closeResult, cleanupComplete = false
    const settle = () => {
      if (!closeResult || (terminationReason && !cleanupComplete)) return
      clearTimeout(timer); clearTimeout(escalation)
      resolveRun({ startedAt, endedAt: new Date().toISOString(), ...closeResult, terminationReason, processError })
    }
    const kill = signal => {
      try { if (process.platform === 'win32') child.kill(signal); else if (child.pid) process.kill(-child.pid, signal) } catch (error) { if (error.code !== 'ESRCH') throw error }
    }
    const stop = reason => {
      if (terminationReason) return
      terminationReason = reason
      kill('SIGTERM'); escalation = setTimeout(() => { kill('SIGKILL'); cleanupComplete = true; settle() }, graceMS)
    }
    const timer = setTimeout(() => stop('wall_timeout'), wallMS)
    for (const [stream, path] of [[child.stdout, stdout], [child.stderr, stderr]]) {
      stream.on('data', chunk => { bytes += chunk.length; if (bytes <= 64 * 1024 * 1024) appendFileSync(path, chunk); else stop('output_limit') })
    }
    child.on('error', error => { processError = error.message })
    child.on('close', (exitCode, signal) => {
      closeResult = { exitCode, signal }; settle()
    })
  })
}
function artifact(out, file) {
  const bytes = readFileSync(join(out, file))
  return { file, bytes: bytes.length, sha256: sha256(bytes) }
}
export async function captureGoReceipt({ root, out, ...supplied }) {
  root = resolve(root); out = resolve(out)
  const options = optionsFor(supplied)
  mkdirSync(out, { recursive: true })
  if (readdirSync(out).length) throw new Error('receipt output directory must be empty; preserve each invocation separately')
  const source = checkoutSnapshot(root), toolchain = goTool(root, options), inputManifest = goInputs(root, options, toolchain)
  const argv = testArgs(options)
  const execution = await runGoProcess(toolchain.executable, argv, root, environment(options), out, timeoutMS(options.timeout) + 120000)
  const after = checkoutSnapshot(root)
  let inputsStable = false, inputCheckError = null
  try { inputsStable = equal(inputManifest, goInputs(root, options, toolchain)) } catch (error) { inputCheckError = error.message }
  writeFileSync(join(out, 'go-inputs.json'), `${JSON.stringify(inputManifest, null, 2)}\n`)
  const receipt = { schemaVersion: 1, kind: 'named-go-execution', source, sourceAfter: after,
    sourceStable: equal(source, after), inputsStable, inputCheckError, options, toolchain,
    command: { executable: toolchain.executable, argv, cwd: root }, execution,
    artifacts: { stdout: artifact(out, 'stdout.jsonl'), stderr: artifact(out, 'stderr.log'), inputs: artifact(out, 'go-inputs.json') },
    index: indexGoEvents(readFileSync(join(out, 'stdout.jsonl'), 'utf8')), limitations: [limitation] }
  writeFileSync(join(out, 'receipt.json'), `${JSON.stringify(receipt, null, 2)}\n`)
  return receipt
}
function verifiedBytes(directory, metadata, expectedName) {
  if (metadata?.file !== expectedName || !/^[a-f0-9]{64}$/.test(metadata.sha256) || !Number.isSafeInteger(metadata.bytes)) throw new Error('invalid receipt artifact metadata')
  const bytes = readFileSync(join(directory, expectedName))
  if (bytes.length !== metadata.bytes || sha256(bytes) !== metadata.sha256) throw new Error(`receipt checksum mismatch: ${expectedName}`)
  return bytes
}
export function loadGoReceipt(path, root, expectedSource) {
  path = resolve(path); root = resolve(root)
  const bytes = readFileSync(path), receipt = JSON.parse(bytes), directory = dirname(path)
  if (receipt.schemaVersion !== 1 || receipt.kind !== 'named-go-execution') throw new Error('invalid Go receipt schema')
  if (!equal(receipt.source, expectedSource) || !equal(receipt.sourceAfter, expectedSource) || receipt.sourceStable !== true) throw new Error('Go receipt source mismatch')
  const options = optionsFor(receipt.options), toolchain = goTool(root, options)
  if (!equal(receipt.command, { executable: toolchain.executable, argv: testArgs(options), cwd: root }) || !equal(receipt.toolchain, toolchain)) throw new Error('Go receipt command/toolchain mismatch')
  const execution = receipt.execution
  if (!execution || !Number.isFinite(Date.parse(execution.startedAt)) || !Number.isFinite(Date.parse(execution.endedAt)) || Date.parse(execution.endedAt) < Date.parse(execution.startedAt) ||
    !(execution.exitCode === null || Number.isSafeInteger(execution.exitCode)) || !(execution.signal === null || typeof execution.signal === 'string') ||
    ![null, 'wall_timeout', 'output_limit'].includes(execution.terminationReason) || !(execution.processError === null || typeof execution.processError === 'string') ||
    (execution.exitCode === null && !execution.signal && !execution.processError)) throw new Error('invalid Go execution metadata')
  const stdout = verifiedBytes(directory, receipt.artifacts.stdout, 'stdout.jsonl')
  verifiedBytes(directory, receipt.artifacts.stderr, 'stderr.log')
  const inputManifest = JSON.parse(verifiedBytes(directory, receipt.artifacts.inputs, 'go-inputs.json'))
  if (!receipt.inputsStable || !equal(inputManifest, goInputs(root, options, toolchain))) throw new Error('Go inputs changed or capture was unstable')
  const rawIndex = indexGoEvents(stdout.toString('utf8'))
  if (!equal(rawIndex, receipt.index)) throw new Error('Go receipt index mismatch')
  if (Object.values(rawIndex.eventTimeRange).filter(Boolean).some(time => Date.parse(time) < Date.parse(execution.startedAt) || Date.parse(time) > Date.parse(execution.endedAt))) throw new Error('Go events outside captured execution interval')
  if (execution.exitCode === 0 && (rawIndex.packages.some(row => row.outcome === 'failed') || rawIndex.builds.some(row => row.outcome === 'failed'))) throw new Error('Go receipt exit/outcome mismatch')
  const index = { ...rawIndex, tests: rawIndex.tests.map(row => ({ ...row, fresh: row.fresh && inputManifest.complete && !execution.terminationReason && !execution.processError && !execution.signal })) }
  return { path, sha256: sha256(bytes), receipt, inputManifest, index }
}
export function attachGoEvidence(features, receipts) {
  return features.map(feature => ({ ...feature, executionEvidence: receipts.flatMap(input => {
    if (feature.kind !== 'route-registration' || feature.source !== 'internal/app/route_inventory_test.go') return []
    return input.index.tests.filter(row => row.package === 'github.com/flidai/leapview/internal/app' && row.test === 'TestRouteInventory' && row.fresh).map(row => ({
      classification: 'registration_access_contract', scope: 'Mounted route/access contract parity only; handler journeys remain unverified.',
      package: row.package, test: row.test, outcome: row.outcome, packageOutcome: row.packageOutcome, invocationExitCode: input.receipt.execution.exitCode,
      receiptPath: input.path, receiptSHA256: input.sha256 }))
  }) }))
}
export function receiptExitCode(receipt) {
  const { execution } = receipt
  return receipt.sourceStable && receipt.inputsStable && !receipt.index.errors.length && !execution.terminationReason && !execution.processError && !execution.signal
    ? (execution.exitCode ?? 1) : 2
}
async function main() {
  const { values } = parseArgs({ options: { root: { type: 'string' }, out: { type: 'string' }, package: { type: 'string', multiple: true }, run: { type: 'string' }, tags: { type: 'string' }, cgo: { type: 'string' }, timeout: { type: 'string' }, help: { type: 'boolean' } } })
  if (values.help) { console.log('Usage: task audit:go-receipt -- --package ./internal/app --run ^TestRouteInventory$ --tags duckdb_arrow --timeout 2m [--out DIR]\nCaptures a fresh count=1 invocation, raw logs and named outcomes. Prepare generated Go sources first. Use an ignored or external output directory; each invocation is retained separately.'); return }
  const root = resolve(values.root ?? dirname(dirname(fileURLToPath(import.meta.url))))
  const out = resolve(values.out ?? join(root, '.tmp/audit/go-receipts', randomUUID()))
  const receipt = await captureGoReceipt({ root, out, packages: values.package, run: values.run, tags: values.tags?.split(',').filter(Boolean), cgo: values.cgo, timeout: values.timeout })
  console.log(JSON.stringify({ receipt: join(out, 'receipt.json'), exitCode: receipt.execution.exitCode, sourceStable: receipt.sourceStable, inputsStable: receipt.inputsStable, namedTests: receipt.index.tests.length }))
  process.exitCode = receiptExitCode(receipt)
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main()
