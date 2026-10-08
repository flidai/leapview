import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, symlinkSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { readFileSync, existsSync } from 'node:fs'
import { test } from 'node:test'
import { createHash } from 'node:crypto'
import { classifyFile, inventoryFiles, collectFeatures, attachHistoricalEvidence, loadControllerCatalogs, sourceFilesSHA256 } from './audit_inventory.mjs'
import { captureGoReceipt } from './go_receipts.mjs'

function fixture() {
  return {
    texts: {
      'internal/app/route_inventory_test.go': 'const nonAPIRouteInventory = `\nGET /\nPOST /auth/local/login\n`',
      'internal/app/site/http/routes.go': 'mux.HandleFunc("GET /docs/{path...}", docs)',
      'internal/project/contracts/registry.gen.go': 'var ConnectorRegistry = []ConnectorProfile{{Key: "s3"}}',
      'internal/project/contracts/path_options.gen.go': 'var FormatRegistry = []FormatProfile{{Name: "parquet"}}',
      'schemas/json/model.schema.json': '{}',
      'desktop/installer-contract.ts': 'export const installer = {}',
    },
    openapi: { paths: { '/api/v1/data': { get: { operationId: 'getData', tags: ['Managed Data'] } } } },
    visuals: { documents: [{ source: 'table' }] },
    agentManifest: { schemaVersion: 1, tools: [{ name: 'query_visual' }, { name: 'query_semantic_model' }] },
  }
}

function controllerFixture(variant = 'standalone') {
  return { schemaVersion: 1, product: 'leapviewctl', scope: 'constructed-explicit-commands',
    build: { variant, goos: 'linux', goarch: 'amd64', cgoEnabled: variant === 'host-payload', tags: variant === 'host-payload' ? ['duckdb_arrow'] : [] },
    source: { commit: 'd'.repeat(40), workingTreeStatus: '', trackedDiffSHA256: 'a'.repeat(64), sourceFilesSHA256: 'b'.repeat(64) },
    commands: [
      { path: [], pathText: '', use: 'leapviewctl', aliases: [], hidden: false, effectiveHidden: false, hasHandler: true, disableFlagParsing: false, flags: [], inheritedFlags: [] },
      { path: ['qualify'], pathText: 'qualify', use: 'qualify', aliases: [], hidden: false, effectiveHidden: false, hasHandler: true, disableFlagParsing: false, flags: [], inheritedFlags: [] },
      { path: ['qualify', 'client-worker'], pathText: 'qualify client-worker', use: 'client-worker', aliases: ['worker'], hidden: true, effectiveHidden: true, hasHandler: true, disableFlagParsing: false,
        flags: [{ name: 'target', shorthand: '', type: 'string', hidden: false, required: true }], inheritedFlags: [] },
    ] }
}

test('constructed controller catalogs preserve hidden commands and bind both shipped variants', () => {
  const controllerCatalogs = ['standalone', 'host-payload'].map(variant => ({ manifest: controllerFixture(variant), sha256: 'c'.repeat(64) }))
  const result = collectFeatures({ ...fixture(), controllerCatalogs })
  const rows = result.features.filter(row => row.kind === 'controller-command')
  assert.equal(rows.length, 6)
  assert.equal(new Set(rows.map(row => row.id)).size, 6)
  assert.ok(rows.every(row => row.ownerIssue === 'FAI-1096' && row.currentDisposition === 'not_run'))
  const worker = rows.find(row => row.detail.build.variant === 'host-payload' && row.detail.path.join(' ') === 'qualify client-worker')
  assert.equal(worker.detail.hidden, true)
  assert.deepEqual(worker.detail.aliases, ['worker'])
  assert.equal(worker.detail.flags[0].required, true)
  assert.equal(worker.detail.catalogSHA256, 'c'.repeat(64))
  assert.ok(!result.limitations.some(item => item.startsWith('Missing controller')))
  assert.ok(result.limitations.some(item => item.includes('implicit help/completion')))
  const missing = collectFeatures({ ...fixture(), controllerCatalogs: [controllerCatalogs[0]] })
  assert.ok(missing.limitations.some(item => item.includes('Missing controller catalog: host-payload')))
})

test('constructed controller catalogs reject malformed paths, build variants and private flag values', () => {
  const collect = manifest => collectFeatures({ ...fixture(), controllerCatalogs: [{ manifest, sha256: 'c'.repeat(64) }] })
  for (const mutate of [
    manifest => { manifest.commands.push(manifest.commands[0]) },
    manifest => { manifest.commands[2].pathText = 'wrong path' },
    manifest => { manifest.commands[2].path = ['space separated'] },
    manifest => { manifest.commands[2].effectiveHidden = false },
    manifest => { manifest.commands[2].flags[0].default = 'private-input' },
    manifest => { manifest.commands[2].currentValues = ['private-input'] },
    manifest => { manifest.build.privateInput = 'private-input' },
    manifest => { manifest.build.cgoEnabled = true },
    manifest => { manifest.build.tags = ['extra'] },
    manifest => { manifest.build.goos = 'darwin' },
    manifest => { manifest.commands[0].hasHandler = 'yes' },
  ]) {
    const manifest = controllerFixture()
    mutate(manifest)
    assert.throws(() => collect(manifest), /controller catalog/)
  }
})

test('controller catalog inputs hash exact file bytes and reject stale checkout identities', () => {
  const directory = mkdtempSync(join(tmpdir(), 'leapview-controller-catalogs-'))
  try {
    const manifest = controllerFixture()
    const path = join(directory, 'leapviewctl-standalone.json')
    const bytes = `${JSON.stringify(manifest, null, 3)}\n\n`
    writeFileSync(path, bytes)
    const result = loadControllerCatalogs(directory, manifest.source)
    assert.deepEqual(result.missingVariants, ['host-payload'])
    assert.equal(result.catalogs[0].sha256, createHash('sha256').update(bytes).digest('hex'))
    assert.notEqual(result.catalogs[0].sha256, createHash('sha256').update(JSON.stringify(manifest)).digest('hex'))
    for (const field of ['commit', 'workingTreeStatus', 'trackedDiffSHA256', 'sourceFilesSHA256']) {
      assert.throws(() => loadControllerCatalogs(directory, { ...manifest.source, [field]: 'changed' }), /stale controller catalog/)
    }
    writeFileSync(path, JSON.stringify(controllerFixture('host-payload')))
    assert.throws(() => loadControllerCatalogs(directory, manifest.source), /variant does not match filename/)
    writeFileSync(path, '{malformed')
    assert.throws(() => loadControllerCatalogs(directory, manifest.source), SyntaxError)
    rmSync(path)
    symlinkSync('/outside/private-catalog', path)
    assert.throws(() => loadControllerCatalogs(directory, manifest.source), /catalog file/)
  } finally {
    rmSync(directory, { recursive: true, force: true })
  }
})

test('inventory accounts for missing files and symlinks without reading their targets', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-inventory-'))
  try {
    mkdirSync(join(root, 'internal'))
    writeFileSync(join(root, 'internal/example.go'), 'package example')
    symlinkSync('/outside/secret', join(root, 'link'))
    const rows = inventoryFiles(root, ['link', 'gone.go', 'internal/example.go'])
    assert.deepEqual(rows.map(row => row.path), ['gone.go', 'internal/example.go', 'link'])
    assert.equal(rows[0].state, 'missing')
    assert.equal(rows[2].state, 'symlink')
    assert.equal(rows[2].bytes, Buffer.byteLength('/outside/secret'))
    assert.equal(rows[2].sha256.length, 64)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('nested modules, vendor, generators and Nix inputs have explicit classifications', () => {
  assert.equal(classifyFile('pkg/apigen/go.mod', '').category, 'contract-or-configuration')
  assert.equal(classifyFile('desktop/vendor/dependency/index.js', '').category, 'vendor')
  assert.equal(classifyFile('internal/app/tools/clidocgen/main.go', '').category, 'generator')
  assert.equal(classifyFile('nix/toolchain.nix', '').category, 'infrastructure-or-build')
  assert.equal(classifyFile('internal/access/api/gen/models.go', '// Code generated. DO NOT EDIT.').category, 'generated')
})

test('canonical catalogs include API, main/site routes, agent tools and actual Desktop installer source', () => {
  const result = collectFeatures(fixture())
  assert.ok(result.features.some(row => row.name === 'GET /api/v1/data' && row.domain === 'data'))
  assert.ok(result.features.some(row => row.name === 'GET /docs/{path...}' && row.domain === 'site'))
  assert.deepEqual(result.features.filter(row => row.kind === 'agent-tool').map(row => row.name), ['query_semantic_model', 'query_visual'])
  assert.equal(result.features.find(row => row.name === 'installer-contract').source, 'desktop/installer-contract.ts')
  assert.ok(result.features.every(row => row.currentDisposition === 'not_run' && row.ownerIssue))
  assert.ok(result.limitations.some(item => item.includes('runtime CLI')))
})

test('runtime CLI distinguishes help groups from runnable commands and includes root', () => {
  const result = collectFeatures({ ...fixture(), cliManifest: { schemaVersion: 2, commands: [
    { path: [], runnable: true }, { path: ['data'], runnable: false }, { path: ['data', 'upload'], runnable: true },
  ] } })
  const rows = result.features.filter(row => row.kind === 'cli-command')
  assert.equal(rows.length, 3)
  assert.equal(rows.find(row => row.name === 'leapview').detail.runnable, true)
  assert.equal(rows.find(row => row.name === 'leapview data').detail.runnable, false)
  assert.equal(rows.find(row => row.name === 'leapview data upload').ownerIssue, 'FAI-1091')
})

test('missing required catalogs, malformed entries and duplicate declarations fail visibly', () => {
  assert.throws(() => collectFeatures({ ...fixture(), visuals: {} }), /visual catalog/)
  assert.throws(() => collectFeatures({ ...fixture(), openapi: { paths: {} } }), /API catalog/)
  assert.throws(() => collectFeatures({ ...fixture(), agentManifest: { schemaVersion: 1, tools: [{ name: 'same' }, { name: 'same' }] } }), /duplicate/)
  assert.throws(() => collectFeatures({ ...fixture(), cliManifest: { schemaVersion: 2, commands: [{ path: ['x'] }] } }), /runnable/)
  assert.throws(() => collectFeatures({ ...fixture(), texts: { ...fixture().texts, 'internal/project/contracts/registry.gen.go': 'var ConnectorRegistry = []ConnectorProfile{}' } }), /empty connector/)
})

test('historical passes and exclusions remain separate from current dispositions', () => {
  const { features } = collectFeatures(fixture())
  const feature = features.find(row => row.name === 'GET /api/v1/data')
  const result = attachHistoricalEvidence(features, { snapshots: { current: { commit: 'old-commit' } }, currentRows: [
    { featureId: feature.id, kind: feature.kind, name: feature.name, currentDisposition: 'passed', evidence: [{ classification: 'executed_suite', outcome: 'passed' }, { classification: 'excluded' }] },
  ] })
  const row = result.find(row => row.id === feature.id)
  assert.equal(row.currentDisposition, 'not_run')
  assert.equal(row.historicalEvidence[0].inventoryCommit, 'old-commit')
  assert.equal(row.historicalEvidence[0].evidence.length, 2)
  assert.equal(features.find(row => row.id === feature.id).historicalEvidence, undefined)
  assert.throws(() => attachHistoricalEvidence(features, {}), /invalid historical ledger/)
})

test('the command emits deterministic inventories, verifies execution receipts and reports deleted tracked files', async () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-inventory-command-'))
  try {
    const input = fixture()
    const sources = { ...input.texts,
      'docs/api/openapi.yaml': JSON.stringify(input.openapi),
      'docs/visuals/catalog.json': JSON.stringify(input.visuals),
      'docs/reference/agent-tools/manifest.json': JSON.stringify(input.agentManifest),
      'docs/reference/cli/manifest.json': JSON.stringify({ schemaVersion: 2, commands: [{ path: [], runnable: true }] }),
      'Taskfile.yml': JSON.stringify({ tasks: { 'ci:lane:frontend': { cmds: [] }, 'ci:lane:frontend:shard': { cmds: [] } } }),
      'package.json': JSON.stringify({ scripts: {} }),
      'go.mod': 'module example/inventory\n\ngo 1.22\n',
      'receipt-fixture/receipt_test.go': 'package receipt\nimport "testing"\nfunc TestNamed(t *testing.T) {}\n',
      '.gitignore': '.tmp/\n',
      'deleted.go': 'package deleted',
    }
    for (const [path, body] of Object.entries(sources)) {
      const file = join(root, path)
      mkdirSync(join(file, '..'), { recursive: true })
      writeFileSync(file, body)
    }
    const git = (...args) => execFileSync('git', ['-C', root, ...args], { stdio: 'pipe' })
    git('init')
    git('add', '.')
    git('-c', 'user.name=Inventory Test', '-c', 'user.email=inventory@example.invalid', 'commit', '-m', 'test fixture')
    rmSync(join(root, 'deleted.go'))
    const checksum = value => createHash('sha256').update(value).digest('hex')
    const catalogDir = join(root, '.tmp', 'controller-catalogs')
    mkdirSync(catalogDir, { recursive: true })
    const expectedSource = { commit: git('rev-parse', 'HEAD').toString().trim(), workingTreeStatus: git('status', '--porcelain=v1', '-z').toString(),
      trackedDiffSHA256: checksum(git('diff', '--no-ext-diff', '--binary', 'HEAD')), sourceFilesSHA256: sourceFilesSHA256(root) }
    const controllerPaths = ['standalone', 'host-payload'].map(variant => {
      const manifest = { ...controllerFixture(variant), source: expectedSource }
      const path = join(catalogDir, `leapviewctl-${variant}.json`)
      writeFileSync(path, `${JSON.stringify(manifest, null, 2)}\n`)
      return path
    })
    const out = join(root, '.tmp', 'inventory')
    const receiptOut = join(root, '.tmp', 'go-receipt')
    const captured = await captureGoReceipt({ root, out: receiptOut, packages: ['./receipt-fixture'], run: '^TestNamed$', cgo: '0', timeout: '30s' })
    assert.equal(captured.execution.exitCode, 0)
    const receiptPath = join(receiptOut, 'receipt.json')
    const script = fileURLToPath(new URL('./audit_inventory.mjs', import.meta.url))
    const run = () => execFileSync(process.execPath, [script, '--root', root, '--out', out, '--controller-catalog-dir', catalogDir, '--go-receipt', receiptPath], { stdio: 'pipe' })
    run()
    const first = readFileSync(join(out, 'files.json'), 'utf8')
    const summaryBytes = readFileSync(join(out, 'summary.json'), 'utf8')
    run()
    assert.equal(readFileSync(join(out, 'files.json'), 'utf8'), first)
    assert.equal(readFileSync(join(out, 'summary.json'), 'utf8'), summaryBytes)
    assert.equal(JSON.parse(first).length, Object.keys(sources).length)
    assert.equal(JSON.parse(first).find(row => row.path === 'deleted.go').state, 'missing')
    const summary = JSON.parse(readFileSync(join(out, 'summary.json')))
    assert.equal(summary.sourceCommit, git('rev-parse', 'HEAD').toString().trim())
    assert.equal(summary.currentDispositions.not_run, summary.featureCount)
    const features = JSON.parse(readFileSync(join(out, 'features.json')))
    assert.equal(features.find(row => row.kind === 'cli-command').name, 'leapview')
    assert.equal(summary.runtimeCLIManifestSHA256.length, 64)
    assert.deepEqual(summary.missingControllerVariants, [])
    assert.equal(features.filter(row => row.kind === 'controller-command').length, 6)
    for (const path of controllerPaths) {
      assert.equal(summary.controllerCatalogs.find(input => input.path === path).sha256, checksum(readFileSync(path)))
    }
    assert.equal(existsSync(join(out, 'frontend-registration.json')), true)
    const execution = JSON.parse(readFileSync(join(out, 'go-execution.json')))
    assert.equal(execution.length, 1)
    assert.ok(execution[0].index.tests.some(row => row.test === 'TestNamed' && row.fresh))
    assert.equal(summary.goReceipts[0].sha256, checksum(readFileSync(receiptPath)))
    assert.equal(summary.goReceipts[0].freshNamedTests, 1)
    assert.equal(summary.featuresWithExecutionEvidence, 0)
    assert.ok(features.every(feature => feature.executionEvidence.length === 0))
    const stderr = readFileSync(join(receiptOut, 'stderr.log'))
    writeFileSync(join(receiptOut, 'stderr.log'), 'changed')
    assert.throws(run, /checksum mismatch/)
    writeFileSync(join(receiptOut, 'stderr.log'), stderr)
    writeFileSync(join(root, 'untracked.go'), 'package changed')
    assert.throws(run, /source workingTreeStatus differs|source sourceFilesSHA256 differs/)
    const before = sourceFilesSHA256(root)
    writeFileSync(join(root, 'untracked.go'), 'package changedagain')
    assert.notEqual(sourceFilesSHA256(root), before)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
