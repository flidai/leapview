// Maintained version of the October audit's build_inventory.py catalog scan.
// This command inventories declarations; it never executes product tests.
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, lstatSync, mkdirSync, readFileSync, readlinkSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'
import { parse } from 'yaml'
import { auditFrontendTestRegistration } from './frontend_test_registration.mjs'

const owners = {
  access: 'FAI-1089', compiler: 'FAI-1090', data: 'FAI-1091', analytics: 'FAI-1092',
  dashboards: 'FAI-1093', explorer: 'FAI-1094', agent: 'FAI-1095', lifecycle: 'FAI-1096',
  desktop: 'FAI-1097', site: 'FAI-1098', shared: 'FAI-1099',
}
const domainPrefixes = [
  ['internal/access/', 'access'], ['internal/admin/', 'access'], ['internal/manageddata/', 'data'],
  ['internal/analytics/', 'analytics'], ['internal/dashboard/', 'dashboards'], ['internal/project/', 'compiler'],
  ['internal/agent/', 'agent'], ['pkg/agent/', 'agent'], ['desktop/', 'desktop'],
  ['internal/app/site/', 'site'], ['docs/', 'site'], ['site/', 'site'],
  ...['deployment', 'servingstate', 'runtimehost', 'refresh', 'release', 'recoveryset'].map(name => [`internal/${name}/`, 'lifecycle']),
  ['web/components/dashboard/', 'dashboards'], ['web/components/data/', 'explorer'],
  ['web/components/project/', 'explorer'], ['web/components/chat/', 'agent'],
  ['web/components/admin/', 'access'], ['web/components/login/', 'access'],
]
const apiOwners = {
  Access: 'access', 'Current User': 'access', Audit: 'access', Agent: 'agent', BI: 'analytics',
  Connections: 'analytics', Credentials: 'analytics', 'Saved Explorations': 'explorer',
  'Dashboard Authoring': 'dashboards', Publications: 'dashboards', 'Managed Data': 'data',
  Deployments: 'lifecycle', Delivery: 'lifecycle', Releases: 'lifecycle', 'Refresh Runs': 'lifecycle',
  Instance: 'lifecycle', System: 'lifecycle', Projects: 'compiler', Search: 'compiler',
}
const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')
const domainFor = path => domainPrefixes.find(([prefix]) => path.startsWith(prefix))?.[1] ?? 'shared'
const sorted = values => [...values].sort()
const counts = values => Object.fromEntries(sorted(new Set(values)).map(value => [value, values.filter(item => item === value).length]))

export function classifyFile(path, body) {
  let category = 'asset-or-project-metadata'
  if (/(^|\/)vendor\//.test(path)) category = 'vendor'
  else if (/(?:_test\.go|\.(?:test|spec)\.[^.]+)$/.test(path) || path.includes('/testdata/')) category = 'test-or-fixture'
  else if (/Code generated/.test(body.slice(0, 400)) || /\/(?:generated|gen)\/|\.gen\.|_generated\./.test(path)) category = 'generated'
  else if (/\/generate\/|internal\/app\/tools\/[^/]*gen\//.test(path)) category = 'generator'
  else if (/^(?:docs|adr)\//.test(path) || path.endsWith('.md')) category = 'documentation'
  else if (/^(?:deploy|nix|\.github)\//.test(path) || /^(?:Dockerfile(?:\..*)?|Taskfile\.yml|flake\.(?:nix|lock))$/.test(path)) category = 'infrastructure-or-build'
  else if (/^(?:dashboards|evaluation|examples)\//.test(path)) category = 'fixture-or-resource'
  else if (/\.(?:json|yaml|yml|toml|tsp|cue|sql|lock)$|(?:^|\/)go\.(?:mod|sum)$/.test(path)) category = 'contract-or-configuration'
  else if (/\.(?:go|ts|tsx|js|mjs|cjs|py|sh|css|html)$/.test(path)) category = 'authored-code'
  const domain = domainFor(path)
  return { domain, ownerIssue: owners[domain], category }
}

export function inventoryFiles(root, paths) {
  return sorted(new Set(paths)).map(path => {
    const target = join(root, path)
    let bytes, state
    try {
      const stat = lstatSync(target)
      if (stat.isSymbolicLink()) { state = 'symlink'; bytes = Buffer.from(readlinkSync(target)) }
      else if (stat.isFile()) { state = 'present'; bytes = readFileSync(target) }
      else state = 'non_file'
    } catch (error) {
      if (error.code !== 'ENOENT') throw error
      state = 'missing'
    }
    return { path, ...classifyFile(path, bytes?.toString('utf8', 0, 400) ?? ''), state,
      bytes: bytes?.length ?? null, sha256: bytes ? sha256(bytes) : null, review: 'inventory-only' }
  })
}

function routeDomain(path) {
  if (/^\/(?:admin\/agent|chats)/.test(path)) return 'agent'
  if (path.startsWith('/candidates/')) return 'lifecycle'
  if (/^\/(?:dashboards|public\/dashboards|embed\/dashboards)/.test(path)) return 'dashboards'
  if (/^\/explore(?:\/|$)|\/data\/command/.test(path)) return 'explorer'
  if (/^\/(?:sources|models|semantic-models|pipelines|connections|catalog|search)(?:\/|$)/.test(path)) return 'compiler'
  if (path.startsWith('/upload-protocols')) return 'data'
  if (/^\/(?:admin|auth|oauth|scim|profile|device|login|logout|product\/logo)(?:\/|$)|^\/\.well-known\/oauth/.test(path)) return 'access'
  if (path === '/.well-known/leapview') return 'desktop'
  return 'shared'
}

export function collectFeatures({ texts, openapi, visuals, agentManifest, cliManifest }) {
  const features = [], ids = new Set(), limitations = []
  function add(kind, name, source, domain = domainFor(source), detail = {}) {
    if (typeof name !== 'string' || !name.trim()) throw new Error(`invalid ${kind} name`)
    let id = `${kind}:${source}:${name}`
    if (ids.has(id) && kind === 'cli-registration') id += `#L${detail.line}`
    if (ids.has(id)) throw new Error(`duplicate declaration: ${id}`)
    ids.add(id)
    features.push({ id, kind, name, source, domain, ownerIssue: owners[domain], detail,
      currentDisposition: 'not_run', dispositionReason: 'No execution receipt has been assigned to this snapshot.' })
  }
  if (!openapi?.paths || !Object.keys(openapi.paths).length) throw new Error('missing or empty API catalog')
  for (const [path, item] of Object.entries(openapi.paths)) {
    for (const [method, operation] of Object.entries(item)) {
      if (!['get', 'post', 'put', 'patch', 'delete', 'head', 'options', 'trace'].includes(method)) continue
      if (!operation?.operationId || !Array.isArray(operation.tags)) throw new Error(`invalid API operation: ${method} ${path}`)
      const domain = operation.tags.map(tag => apiOwners[tag]).find(Boolean)
      if (!domain) limitations.push(`API owner needs review: ${method.toUpperCase()} ${path}, tags ${operation.tags.join(', ')}`)
      add('api-operation', `${method.toUpperCase()} ${path}`, 'docs/api/openapi.yaml', domain ?? 'shared',
        { operationId: operation.operationId, tags: operation.tags })
    }
  }
  const routeSource = 'internal/app/route_inventory_test.go'
  const routeTable = texts[routeSource]?.match(/const nonAPIRouteInventory = `([\s\S]*?)`/)
  if (!routeTable) throw new Error('missing main route inventory contract')
  for (const entry of routeTable[1].split('\n').map(line => line.trim()).filter(Boolean)) {
    if (!/^[A-Z*]+ \/\S*$/.test(entry)) throw new Error(`invalid route inventory row: ${entry}`)
    add('route-registration', entry, routeSource, routeDomain(entry.slice(entry.indexOf(' ') + 1)),
      { runtimeContract: 'TestRouteInventory', coverageScope: 'Mounted route parity; handler journeys need separate evidence.' })
  }
  for (const [source, body] of Object.entries(texts)) {
    if (!source.endsWith('.go') || source.endsWith('_test.go')) continue
    if (source.startsWith('internal/app/site/http/')) {
      for (const match of body.matchAll(/\.(?:Handle|HandleFunc)\("((?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|CONNECT|TRACE) \/[^"\n]*)"/g)) {
        add('route-registration', match[1], source, 'site', { line: body.slice(0, match.index).split('\n').length, sourceLiteral: true })
      }
    }
    if (source.includes('/cli/') || source.includes('/cmd/')) {
      for (const match of body.matchAll(/\bUse:\s*"([^"\n]+)"/g)) {
        // Identical local Uses may belong to different parents; retain each source location.
        const line = body.slice(0, match.index).split('\n').length
        add('cli-registration', match[1], source, undefined, { line, sourceLiteral: true })
      }
    }
  }
  const schemas = sorted(Object.keys(texts).filter(path => /^schemas\/json\/[^/]+\.schema\.json$/.test(path)))
  if (!schemas.length) throw new Error('missing resource schema catalog')
  for (const source of schemas) {
    add('resource-schema', source.split('/').at(-1).replace('.schema.json', ''), source, 'compiler')
  }
  if (!Array.isArray(visuals?.documents) || !visuals.documents.length) throw new Error('missing or empty visual catalog')
  for (const visual of visuals.documents) add('visual', visual.source, 'docs/visuals/catalog.json', 'dashboards')
  const connectors = [...(texts['internal/project/contracts/registry.gen.go']?.matchAll(/\{Key: "([^"]+)"/g) ?? [])]
  if (!connectors.length) throw new Error('missing or empty connector registry')
  for (const match of connectors) add('connector', match[1], 'internal/project/contracts/registry.gen.go', 'data')
  const formats = texts['internal/project/contracts/path_options.gen.go']?.split('var FormatRegistry =')[1]
  if (!formats) throw new Error('missing format registry')
  const formatEntries = [...formats.matchAll(/\{Name: "([^"]+)"/g)]
  if (!formatEntries.length) throw new Error('empty format registry')
  for (const match of formatEntries) add('source-format', match[1], 'internal/project/contracts/path_options.gen.go', 'data')
  if (agentManifest?.schemaVersion !== 1 || !Array.isArray(agentManifest.tools) || !agentManifest.tools.length) throw new Error('missing or invalid agent manifest')
  for (const tool of agentManifest.tools) add('agent-tool', tool.name, 'docs/reference/agent-tools/manifest.json', 'agent',
    { effect: tool.effect, authzMode: tool.authzMode, operationId: tool.operationId })
  for (const name of ['discovery', 'auth', 'profiles', 'deep-link', 'recovery', 'remote-lifecycle', 'application-navigation',
    'application-shutdown', 'managed-policy', 'updater', 'update-coordinator', 'diagnostics', 'native-menu', 'installer-contract']) {
    add('desktop-capability', name, name === 'installer-contract' ? 'desktop/installer-contract.ts' : `desktop/src/${name}.ts`, 'desktop')
  }
  if (cliManifest) {
    if (cliManifest.schemaVersion !== 2 || !Array.isArray(cliManifest.commands) || !cliManifest.commands.length) throw new Error('invalid runtime CLI manifest')
    const cliOwners = { login: 'access', logout: 'access', admin: 'access', agent: 'agent', data: 'data',
      dashboards: 'dashboards', 'semantic-models': 'dashboards', validate: 'compiler', plan: 'compiler', schema: 'compiler',
      export: 'compiler', init: 'compiler', doctor: 'shared', serve: 'shared', build: 'lifecycle', rollback: 'lifecycle',
      deploy: 'lifecycle', dev: 'compiler', publish: 'lifecycle', host: 'lifecycle', search: 'compiler' }
    for (const command of cliManifest.commands) {
      if (!Array.isArray(command.path) || command.path.some(part => typeof part !== 'string' || !part || /\s/.test(part))) throw new Error('invalid runtime CLI path')
      if (typeof command.runnable !== 'boolean') throw new Error('runtime CLI entry must declare runnable')
      add('cli-command', ['leapview', ...command.path].join(' '), 'docs/reference/cli/manifest.json', cliOwners[command.path[0]] ?? 'shared',
        { runnable: command.runnable, effect: command.effect, confirmation: command.confirmation })
    }
  } else limitations.push('Missing runtime CLI catalog: run task cli-docs:generate, then regenerate this inventory.')
  limitations.push('Static CLI Uses are source declarations; the runtime manifest resolves reachable parent paths.',
    'leapviewctl and hostinstaller have static declarations only; their full constructed command trees need separate runtime inventories.',
    'Public-site routes are source literals; TestRouteInventory covers the main application.',
    'Source presence and test registration do not establish feature execution or exhaustive behavior coverage.')
  return { features: features.sort((a, b) => a.id.localeCompare(b.id, 'en')), limitations }
}

export function attachHistoricalEvidence(features, previous) {
  if (!Array.isArray(previous?.currentRows) || !previous.snapshots?.current?.commit) throw new Error('invalid historical ledger')
  const previousRows = previous.currentRows
  return features.map(feature => {
    const exact = previousRows.filter(row => row.featureId === feature.id)
    const equivalent = previousRows.filter(row => row.kind === feature.kind && row.name === feature.name)
    return { ...feature, historicalEvidence: (exact.length ? exact : equivalent.length === 1 ? equivalent : [])
    .map(row => ({ inventoryCommit: previous.snapshots?.current?.commit ?? null, featureId: row.featureId,
      namedTests: row.namedTests ?? [], evidence: row.evidence ?? [],
      qualification: 'Historical evidence only; never a current-snapshot pass.' })) }
  })
}

function git(root, ...args) {
  return execFileSync('git', ['-C', root, ...args], { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 })
}
function main() {
  const { values } = parseArgs({ options: {
    root: { type: 'string' }, out: { type: 'string' }, 'previous-ledger': { type: 'string' }, help: { type: 'boolean' },
  } })
  if (values.help) {
    console.log('Usage: node scripts/audit_inventory.mjs [--root REPO] [--out DIR] [--previous-ledger JSON]\nGenerate files, feature declarations, component CI registration, and a summary. Runs no tests.\nPrepare with bun install --frozen-lockfile; task cli-docs:generate supplies the optional runtime CLI catalog.')
    return
  }
  const root = resolve(values.root ?? dirname(dirname(fileURLToPath(import.meta.url))))
  const out = resolve(values.out ?? join(root, '.tmp/audit/inventory'))
  const head = git(root, 'rev-parse', 'HEAD').trim()
  const workingTreeStatus = git(root, 'status', '--porcelain=v1', '-z')
  const trackedDiffSHA256 = sha256(git(root, 'diff', '--no-ext-diff', '--binary', 'HEAD'))
  const paths = git(root, 'ls-files', '-z').split('\0').filter(Boolean)
  const files = inventoryFiles(root, paths)
  const texts = Object.fromEntries(files.filter(row => row.state === 'present' && /\.go$|\.schema\.json$/.test(row.path))
    .map(row => [row.path, readFileSync(join(root, row.path), 'utf8')]))
  const readJSON = path => JSON.parse(readFileSync(join(root, path), 'utf8'))
  const cliPath = 'docs/reference/cli/manifest.json'
  const cliManifest = existsSync(join(root, cliPath)) ? readJSON(cliPath) : undefined
  const catalogs = collectFeatures({ texts, openapi: parse(readFileSync(join(root, 'docs/api/openapi.yaml'), 'utf8')),
    visuals: readJSON('docs/visuals/catalog.json'), agentManifest: readJSON('docs/reference/agent-tools/manifest.json'), cliManifest })
  const previousBytes = values['previous-ledger'] ? readFileSync(resolve(values['previous-ledger'])) : undefined
  const features = (previousBytes ? attachHistoricalEvidence(catalogs.features, JSON.parse(previousBytes)) : catalogs.features)
    .map(feature => ({ ...feature, sourcePresent: existsSync(join(root, feature.source)) }))
  const taskfileBytes = readFileSync(join(root, 'Taskfile.yml'))
  const packageBytes = readFileSync(join(root, 'package.json'))
  const registration = auditFrontendTestRegistration({ taskfile: parse(taskfileBytes.toString()), packageJson: JSON.parse(packageBytes),
    componentTestFiles: paths.filter(path => path.startsWith('web/components/') && path.endsWith('.test.ts')) })
  const modules = paths.filter(path => /(^|\/)go\.mod$/.test(path))
  const summary = {
    schemaVersion: 1, sourceCommit: head, workingTreeStatus, trackedDiffSHA256,
    fileManifestSHA256: sha256(JSON.stringify(files)), trackedFiles: files.length,
    filesByState: counts(files.map(row => row.state)), filesByCategory: counts(files.map(row => row.category)),
    filesByDomain: counts(files.map(row => row.domain)), goModules: sorted(modules),
    featureCount: features.length, featuresByKind: counts(features.map(row => row.kind)),
    currentDispositions: counts(features.map(row => row.currentDisposition)),
    missingFeatureSources: sorted(new Set(features.filter(row => !row.sourcePresent).map(row => row.source))),
    historicalLedgerSHA256: previousBytes ? sha256(previousBytes) : null,
    historicalLedgerPath: previousBytes ? resolve(values['previous-ledger']) : null,
    runtimeCLIManifestSHA256: cliManifest ? sha256(readFileSync(join(root, cliPath))) : null,
    commandInputs: { taskfileSHA256: sha256(taskfileBytes), packageJSONSHA256: sha256(packageBytes) },
    limitations: [...catalogs.limitations, 'Historical receipts retain their original identities and do not set current dispositions.',
      'Frontend component registration uses the maintained Taskfile reachability audit. Other test/command mappings remain to be reviewed.'],
  }
  if (git(root, 'rev-parse', 'HEAD').trim() !== head || git(root, 'status', '--porcelain=v1', '-z') !== workingTreeStatus ||
    sha256(git(root, 'diff', '--no-ext-diff', '--binary', 'HEAD')) !== trackedDiffSHA256) {
    throw new Error('source changed while generating the inventory; rerun on a stable checkout')
  }
  mkdirSync(out, { recursive: true })
  for (const [name, data] of Object.entries({ files, features, 'frontend-registration': registration, summary })) {
    writeFileSync(join(out, `${name}.json`), `${JSON.stringify(data, null, 2)}\n`)
  }
  console.log(JSON.stringify({ output: out, sourceCommit: head, trackedFiles: files.length, features: features.length,
    missingFeatureSources: summary.missingFeatureSources, runtimeCLI: Boolean(cliManifest), currentDispositions: summary.currentDispositions }, null, 2))
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
