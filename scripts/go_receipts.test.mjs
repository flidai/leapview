import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { checkoutSnapshot } from './audit_source.mjs'
import { captureGoReceipt, loadGoReceipt, attachGoEvidence, runGoProcess, receiptExitCode } from './go_receipts.mjs'

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'leapview-go-receipt-'))
  writeFileSync(join(root, '.gitignore'), '.tmp/\ngenerated.go\n')
  writeFileSync(join(root, 'go.mod'), 'module example/receipt\n\ngo 1.22\n')
  writeFileSync(join(root, 'generated.go'), 'package receipt\nconst generatedValue = 7\n')
  writeFileSync(join(root, 'receipt_test.go'), `package receipt
import "testing"
func TestFresh(t *testing.T) { t.Run("case/a#01",func(t *testing.T) { if generatedValue != 7 { t.Fatal(generatedValue) } }) }
func TestSkip(t *testing.T) { t.Skip("fixture skip") }
func TestFails(t *testing.T) { t.Fatal("fixture failure") }
`)
  const git = (...args) => execFileSync('git', ['-C', root, ...args], { stdio: 'pipe' })
  git('init'); git('add', '.')
  git('-c', 'user.name=Receipt Test', '-c', 'user.email=receipt@example.invalid', 'commit', '-m', 'fixture')
  return root
}

test('native capture binds ignored Go inputs, command, toolchain, raw logs and fresh named outcomes', async () => {
  const root = fixture()
  try {
    const out = join(root, '.tmp', 'passed')
    const result = await captureGoReceipt({ root, out, packages: ['./...'], run: '^Test(Fresh|Skip)$', tags: [], cgo: '0', timeout: '30s' })
    assert.equal(result.execution.exitCode, 0)
    assert.ok(result.command.argv.includes('-count=1'))
    assert.ok(result.toolchain.GOVERSION.startsWith('go'))
    const receipt = loadGoReceipt(join(out, 'receipt.json'), root, checkoutSnapshot(root))
    assert.ok(receipt.index.tests.some(row => row.test === 'TestFresh/case/a#01' && row.fresh))
    assert.ok(receipt.index.tests.some(row => row.test === 'TestSkip' && row.outcome === 'skipped'))
    assert.ok(receipt.inputManifest.files.some(row => row.path === join(root, 'generated.go')))
    for (const [name, alternative, other] of [['GOAMD64', 'v2', 'v1'], ['GOMAXPROCS', '3', '2']]) {
      const previous = process.env[name]
      process.env[name] = previous === alternative ? other : alternative
      try { assert.throws(() => loadGoReceipt(join(out, 'receipt.json'), root, checkoutSnapshot(root)), /command\/toolchain mismatch/) }
      finally { if (previous === undefined) delete process.env[name]; else process.env[name] = previous }
    }
    const original = readFileSync(join(out, 'stdout.jsonl'))
    writeFileSync(join(out, 'stdout.jsonl'), `${original}\n`)
    assert.throws(() => loadGoReceipt(join(out, 'receipt.json'), root, checkoutSnapshot(root)), /checksum/)
    writeFileSync(join(out, 'stdout.jsonl'), original)
    writeFileSync(join(root, 'generated.go'), 'package receipt\nconst generatedValue = 8\n')
    assert.throws(() => loadGoReceipt(join(out, 'receipt.json'), root, checkoutSnapshot(root)), /Go inputs changed/)
    writeFileSync(join(root, 'generated.go'), 'package receipt\nconst generatedValue = 7\n')
    assert.throws(() => loadGoReceipt(join(out, 'receipt.json'), root, { ...checkoutSnapshot(root), commit: 'stale' }), /source mismatch/)
    const originalReceipt = readFileSync(join(out, 'receipt.json'))
    const changedReceipt = JSON.parse(originalReceipt)
    changedReceipt.command.argv = changedReceipt.command.argv.filter(arg => arg !== '-count=1')
    writeFileSync(join(out, 'receipt.json'), JSON.stringify(changedReceipt))
    assert.throws(() => loadGoReceipt(join(out, 'receipt.json'), root, checkoutSnapshot(root)), /command\/toolchain mismatch/)
    changedReceipt.command = result.command
    changedReceipt.execution.startedAt = '2020-01-01T00:00:00Z'
    changedReceipt.execution.endedAt = '2020-01-01T00:00:01Z'
    writeFileSync(join(out, 'receipt.json'), JSON.stringify(changedReceipt))
    assert.throws(() => loadGoReceipt(join(out, 'receipt.json'), root, checkoutSnapshot(root)), /outside captured execution interval/)
    writeFileSync(join(out, 'receipt.json'), originalReceipt)
    const failureOut = join(root, '.tmp', 'failed')
    const failed = await captureGoReceipt({ root, out: failureOut, packages: ['./...'], run: '^TestFails$', tags: [], cgo: '0', timeout: '30s' })
    assert.equal(failed.execution.exitCode, 1)
    assert.equal(loadGoReceipt(join(failureOut, 'receipt.json'), root, checkoutSnapshot(root)).index.tests[0].outcome, 'failed')
  } finally { rmSync(root, { recursive: true, force: true }) }
})

test('route receipt associations establish registration/access parity without claiming handler execution', () => {
  const features = [
    { id: 'route', kind: 'route-registration', source: 'internal/app/route_inventory_test.go', currentDisposition: 'not_run' },
    { id: 'site', kind: 'route-registration', source: 'internal/app/site/http/routes.go', currentDisposition: 'not_run' },
    { id: 'api', kind: 'api-operation', source: 'docs/api/openapi.yaml', currentDisposition: 'not_run' },
  ]
  const input = { path: '/receipt.json', sha256: 'a'.repeat(64), receipt: { execution: { exitCode: 0 } },
    index: { tests: [{ package: 'github.com/flidai/leapview/internal/app', test: 'TestRouteInventory', fresh: true, cached: false, outcome: 'passed', packageOutcome: 'passed' }] } }
  const result = attachGoEvidence(features, [input])
  assert.equal(result[0].currentDisposition, 'not_run')
  assert.equal(result[0].executionEvidence[0].classification, 'registration_access_contract')
  assert.match(result[0].executionEvidence[0].scope, /handler journeys/)
  assert.equal(result[1].executionEvidence.length, 0)
  assert.equal(result[2].executionEvidence.length, 0)
  input.index.tests[0].fresh = false
  assert.equal(attachGoEvidence(features, [input])[0].executionEvidence.length, 0)
})

test('bounded capture kills a redirected descendant after its group leader exits on SIGTERM', async t => {
  if (process.platform === 'win32') { t.skip('POSIX process groups required'); return }
  const root = mkdtempSync(join(tmpdir(), 'leapview-go-group-'))
  const out = join(root, 'output'), heartbeat = join(root, 'heartbeat')
  mkdirSync(out)
  const descendant = 'const fs=require("node:fs");process.on("SIGTERM",()=>{});let n=0;setInterval(()=>fs.writeFileSync(process.argv[1],String(++n)),20)'
  const parent = 'process.on("SIGTERM",()=>process.exit(0));require("node:child_process").spawn(process.execPath,["-e",process.argv[1],process.argv[2]],{stdio:"ignore"});setInterval(()=>{},1000)'
  try {
    const result = await runGoProcess(process.execPath, ['-e', parent, descendant, heartbeat], root, process.env, out, 2000, 100)
    assert.equal(result.terminationReason, 'wall_timeout')
    assert.equal(result.exitCode, 0)
    assert.notEqual(receiptExitCode({ execution: result, sourceStable: true, inputsStable: true, index: { errors: [] } }), 0)
    const stopped = readFileSync(heartbeat, 'utf8')
    await new Promise(resolve => setTimeout(resolve, 150))
    assert.equal(readFileSync(heartbeat, 'utf8'), stopped)
  } finally { rmSync(root, { recursive: true, force: true }) }
})
