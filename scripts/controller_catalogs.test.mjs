import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { checkoutSnapshot } from './audit_inventory.mjs'
import { sourceFingerprint, generateControllerCatalogs } from './controller_catalogs.mjs'

function fixture() {
  const root = mkdtempSync(join(tmpdir(), 'leapview-controller-broker-'))
  writeFileSync(join(root, 'source.go'), 'package fixture')
  const git = (...args) => execFileSync('git', ['-C', root, ...args], { stdio: 'pipe' })
  git('init'); git('add', '.')
  git('-c', 'user.name=Catalog Test', '-c', 'user.email=catalog@example.invalid', 'commit', '-m', 'fixture')
  return root
}

test('broker binds pre-compilation identity and explicit shipped tags to both Go invocations', () => {
  const root = fixture()
  try {
    const snapshot = checkoutSnapshot(root)
    const hash = value => createHash('sha256').update(value).digest('hex')
    const expected = hash(`${snapshot.commit}\n${hash(snapshot.workingTreeStatus)}\n${snapshot.trackedDiffSHA256}\n${snapshot.sourceFilesSHA256}\n`)
    assert.equal(sourceFingerprint(snapshot), expected)
    const calls = []
    generateControllerCatalogs(root, join(root, '.tmp/catalogs'), (command, args, options) => calls.push({ command, args, options }))
    assert.equal(calls.length, 2)
    assert.deepEqual(calls.map(call => call.options.env.CGO_ENABLED), ['0', '1'])
    assert.deepEqual(calls.map(call => call.args.find(arg => arg.startsWith('-tags='))), ['-tags=', '-tags=duckdb_arrow'])
    assert.ok(calls.every(call => call.command === 'go' && call.options.cwd === root && call.args.includes(`-ldflags=-X=main.compiledSourceFingerprint=${expected}`)))
    assert.ok(calls.every(call => call.options.env.GOFLAGS === '' && call.options.env.GOWORK === 'off' && call.options.env.GOENV === 'off'))
    assert.ok(calls.every(call => call.args.includes('-p=1') && call.args.includes('-mod=readonly')))
  } finally { rmSync(root, { recursive: true, force: true }) }
})

test('broker fails when source changes during compilation instead of generating the next variant', () => {
  const root = fixture()
  try {
    let calls = 0
    assert.throws(() => generateControllerCatalogs(root, join(root, '.tmp/catalogs'), () => {
      calls++
      writeFileSync(join(root, 'source.go'), 'package changed')
    }), /source changed/)
    assert.equal(calls, 1)
  } finally { rmSync(root, { recursive: true, force: true }) }
})
