import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, symlinkSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { readFileSync, existsSync } from 'node:fs'
import { test } from 'node:test'
import { classifyFile, inventoryFiles, collectFeatures, attachHistoricalEvidence } from './audit_inventory.mjs'

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

test('the command emits deterministic inventories and reports deleted tracked files', () => {
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
    const out = join(root, '.tmp', 'inventory')
    const script = fileURLToPath(new URL('./audit_inventory.mjs', import.meta.url))
    const run = () => execFileSync(process.execPath, [script, '--root', root, '--out', out], { stdio: 'pipe' })
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
    assert.equal(existsSync(join(out, 'frontend-registration.json')), true)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
