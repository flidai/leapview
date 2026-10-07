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

export function collectFeatures({ texts, openapi, visuals, agentManifest, cliManifest, controllerCatalogs = [] }) {
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
  const controllerVariants = new Set()
  for (const { manifest, sha256: catalogSHA256 } of controllerCatalogs) {
    validateControllerCatalog(manifest)
    const { variant } = manifest.build
    if (controllerVariants.has(variant)) throw new Error(`duplicate controller catalog variant: ${variant}`)
    if (!/^[a-f0-9]{64}$/.test(catalogSHA256)) throw new Error('invalid controller catalog checksum')
    controllerVariants.add(variant)
    for (const command of manifest.commands) {
      add('controller-command', `${['leapviewctl', ...command.path].join(' ')} [${variant}]`, 'cmd/leapviewctl/main.go', 'lifecycle',
        { ...command, build: manifest.build, catalogSHA256, scope: manifest.scope })
    }
  }
  for (const variant of ['standalone', 'host-payload']) {
    if (!controllerVariants.has(variant)) limitations.push(`Missing controller catalog: ${variant}; run task audit:controller-catalogs, then regenerate this inventory.`)
  }
  limitations.push('Static CLI Uses are source declarations; the runtime manifest resolves reachable parent paths.',
    'Controller catalogs inspect the explicit constructor tree without execution; Cobra implicit help/completion commands are excluded. Handler presence includes help callbacks and does not classify effects.',
    'Controller build catalogs cover their recorded OS/architecture; they do not establish execution or equivalence on other platforms.',
    'Public-site routes are source literals; TestRouteInventory covers the main application.',
    'Source presence and test registration do not establish feature execution or exhaustive behavior coverage.')
  return { features: features.sort((a, b) => a.id.localeCompare(b.id, 'en')), limitations }
}

export function validateControllerCatalog(manifest) {
  const fail = reason => { throw new Error(`invalid controller catalog: ${reason}`) }
  const token = value => typeof value === 'string' && value.length > 0 && !/\s/.test(value)
  const checksum = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value)
  const keys = (object, allowed) => object && typeof object === 'object' && Object.keys(object).every(key => allowed.includes(key))
  if (manifest?.schemaVersion !== 1 || manifest.product !== 'leapviewctl' || manifest.scope !== 'constructed-explicit-commands') fail('schema/product/scope')
  const build = manifest.build, source = manifest.source
  if (!build || build.goos !== 'linux' || !['amd64', 'arm64'].includes(build.goarch) || !Array.isArray(build.tags)) fail('build target')
  if (!keys(build, ['variant', 'goos', 'goarch', 'cgoEnabled', 'tags'])) fail('unsafe build metadata')
  if (!((build.variant === 'standalone' && build.cgoEnabled === false && build.tags.length === 0) ||
    (build.variant === 'host-payload' && build.cgoEnabled === true && JSON.stringify(build.tags) === '["duckdb_arrow"]'))) fail('unshipped build variant')
  if (!source || !/^(?:[a-f0-9]{40}|[a-f0-9]{64})$/.test(source.commit) || typeof source.workingTreeStatus !== 'string' ||
    !checksum(source.trackedDiffSHA256) || !checksum(source.sourceFilesSHA256)) fail('source identity')
  if (!Array.isArray(manifest.commands) || manifest.commands.length === 0) fail('empty command tree')
  const paths = new Map()
  for (const command of manifest.commands) {
    if (!keys(command, ['path', 'pathText', 'use', 'aliases', 'hidden', 'effectiveHidden', 'hasHandler', 'disableFlagParsing', 'flags', 'inheritedFlags'])) fail('unsafe command metadata')
    if (!Array.isArray(command.path) || !command.path.every(token) || command.pathText !== command.path.join(' ')) fail('command path')
    if (paths.has(command.pathText)) fail(`duplicate command path ${command.pathText}`)
    paths.set(command.pathText, command)
    if (typeof command.use !== 'string' || command.use.split(/\s/)[0] !== (command.path.at(-1) ?? 'leapviewctl')) fail('command use')
    for (const field of ['hidden', 'effectiveHidden', 'hasHandler', 'disableFlagParsing']) {
      if (typeof command[field] !== 'boolean') fail(`command ${field}`)
    }
    if (!Array.isArray(command.aliases) || !command.aliases.every(token) || new Set(command.aliases).size !== command.aliases.length) fail('command aliases')
    for (const field of ['flags', 'inheritedFlags']) {
      if (!Array.isArray(command[field])) fail(`command ${field}`)
      const names = new Set()
      for (const flag of command[field]) {
        if (!flag || Object.keys(flag).some(key => !['name', 'shorthand', 'type', 'hidden', 'required'].includes(key)) ||
          !token(flag.name) || !token(flag.type) || typeof flag.shorthand !== 'string' || flag.shorthand.length > 1 ||
          typeof flag.hidden !== 'boolean' || typeof flag.required !== 'boolean' || names.has(flag.name)) fail('unsafe or malformed flag metadata')
        names.add(flag.name)
      }
    }
  }
  if (!paths.has('')) fail('missing root')
  for (const command of manifest.commands) {
    const parent = command.path.length ? paths.get(command.path.slice(0, -1).join(' ')) : null
    if (command.path.length && !parent) fail('missing parent')
    if (command.effectiveHidden !== (command.hidden || (parent?.effectiveHidden ?? false))) fail('effective visibility')
  }
}

export function sourceFilesSHA256(root) {
  const paths = git(root, 'ls-files', '--cached', '--others', '--exclude-standard', '-z').split('\0').filter(Boolean)
  const rows = inventoryFiles(root, paths).sort((a, b) => Buffer.compare(Buffer.from(a.path), Buffer.from(b.path)))
  return sha256(rows.map(row => `${row.path}\0${row.state}\0${row.sha256 ?? ''}\n`).join(''))
}

export function checkoutSnapshot(root) {
  return { commit: git(root, 'rev-parse', 'HEAD').trim(), workingTreeStatus: git(root, 'status', '--porcelain=v1', '-z'),
    trackedDiffSHA256: sha256(git(root, 'diff', '--no-ext-diff', '--binary', 'HEAD')), sourceFilesSHA256: sourceFilesSHA256(root) }
}

export function loadControllerCatalogs(directory, snapshot) {
  const catalogs = [], missingVariants = []
  for (const variant of ['standalone', 'host-payload']) {
    const path = join(directory, `leapviewctl-${variant}.json`)
    let stat
    try { stat = lstatSync(path) } catch (error) {
      if (error.code !== 'ENOENT') throw error
      missingVariants.push(variant)
      continue
    }
    if (!stat.isFile()) throw new Error(`invalid controller catalog file: ${path}`)
    const bytes = readFileSync(path)
    const manifest = JSON.parse(bytes)
    validateControllerCatalog(manifest)
    if (manifest.build.variant !== variant) throw new Error(`controller catalog variant does not match filename: ${path}`)
    for (const field of ['commit', 'workingTreeStatus', 'trackedDiffSHA256', 'sourceFilesSHA256']) {
      if (manifest.source[field] !== snapshot[field]) throw new Error(`stale controller catalog ${path}: source ${field} differs; regenerate it`)
    }
    catalogs.push({ manifest, path, sha256: sha256(bytes) })
  }
  return { catalogs, missingVariants }
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
    root: { type: 'string' }, out: { type: 'string' }, 'previous-ledger': { type: 'string' }, 'controller-catalog-dir': { type: 'string' }, help: { type: 'boolean' },
  } })
  if (values.help) {
    console.log('Usage: node scripts/audit_inventory.mjs [--root REPO] [--out DIR] [--previous-ledger JSON] [--controller-catalog-dir DIR]\nGenerate files, feature declarations, component CI registration, and a summary. Runs no tests.\nPrepare with bun install --frozen-lockfile; task cli-docs:generate supplies the optional runtime CLI catalog; task audit:controller-catalogs supplies both shipped controller build variants.')
    return
  }
  const root = resolve(values.root ?? dirname(dirname(fileURLToPath(import.meta.url))))
  const out = resolve(values.out ?? join(root, '.tmp/audit/inventory'))
  const snapshot = checkoutSnapshot(root)
  const { commit: head, workingTreeStatus, trackedDiffSHA256, sourceFilesSHA256: sourceFilesChecksum } = snapshot
  const paths = git(root, 'ls-files', '-z').split('\0').filter(Boolean)
  const files = inventoryFiles(root, paths)
  const texts = Object.fromEntries(files.filter(row => row.state === 'present' && /\.go$|\.schema\.json$/.test(row.path))
    .map(row => [row.path, readFileSync(join(root, row.path), 'utf8')]))
  const readJSON = path => JSON.parse(readFileSync(join(root, path), 'utf8'))
  const cliPath = 'docs/reference/cli/manifest.json'
  const cliBytes = existsSync(join(root, cliPath)) ? readFileSync(join(root, cliPath)) : undefined
  const cliManifest = cliBytes ? JSON.parse(cliBytes) : undefined
  const controllerInputs = loadControllerCatalogs(resolve(values['controller-catalog-dir'] ?? join(root, '.tmp/audit/controller-catalogs')),
    snapshot)
  const catalogs = collectFeatures({ texts, openapi: parse(readFileSync(join(root, 'docs/api/openapi.yaml'), 'utf8')),
    visuals: readJSON('docs/visuals/catalog.json'), agentManifest: readJSON('docs/reference/agent-tools/manifest.json'), cliManifest,
    controllerCatalogs: controllerInputs.catalogs })
  const previousBytes = values['previous-ledger'] ? readFileSync(resolve(values['previous-ledger'])) : undefined
  const features = (previousBytes ? attachHistoricalEvidence(catalogs.features, JSON.parse(previousBytes)) : catalogs.features)
    .map(feature => ({ ...feature, sourcePresent: existsSync(join(root, feature.source)) }))
  const taskfileBytes = readFileSync(join(root, 'Taskfile.yml'))
  const packageBytes = readFileSync(join(root, 'package.json'))
  const registration = auditFrontendTestRegistration({ taskfile: parse(taskfileBytes.toString()), packageJson: JSON.parse(packageBytes),
    componentTestFiles: paths.filter(path => path.startsWith('web/components/') && path.endsWith('.test.ts')) })
  const modules = paths.filter(path => /(^|\/)go\.mod$/.test(path))
  const summary = {
    schemaVersion: 1, sourceCommit: head, workingTreeStatus, trackedDiffSHA256, sourceFilesSHA256: sourceFilesChecksum,
    fileManifestSHA256: sha256(JSON.stringify(files)), trackedFiles: files.length,
    filesByState: counts(files.map(row => row.state)), filesByCategory: counts(files.map(row => row.category)),
    filesByDomain: counts(files.map(row => row.domain)), goModules: sorted(modules),
    featureCount: features.length, featuresByKind: counts(features.map(row => row.kind)),
    currentDispositions: counts(features.map(row => row.currentDisposition)),
    missingFeatureSources: sorted(new Set(features.filter(row => !row.sourcePresent).map(row => row.source))),
    historicalLedgerSHA256: previousBytes ? sha256(previousBytes) : null,
    historicalLedgerPath: previousBytes ? resolve(values['previous-ledger']) : null,
    runtimeCLIManifestSHA256: cliBytes ? sha256(cliBytes) : null,
    controllerCatalogs: controllerInputs.catalogs.map(({ manifest, path, sha256 }) => ({ path, sha256, build: manifest.build, scope: manifest.scope, commandCount: manifest.commands.length })),
    missingControllerVariants: controllerInputs.missingVariants,
    commandInputs: { taskfileSHA256: sha256(taskfileBytes), packageJSONSHA256: sha256(packageBytes) },
    limitations: [...catalogs.limitations, 'Historical receipts retain their original identities and do not set current dispositions.',
      'Frontend component registration uses the maintained Taskfile reachability audit. Other test/command mappings remain to be reviewed.'],
  }
  if (git(root, 'rev-parse', 'HEAD').trim() !== head || git(root, 'status', '--porcelain=v1', '-z') !== workingTreeStatus ||
    sha256(git(root, 'diff', '--no-ext-diff', '--binary', 'HEAD')) !== trackedDiffSHA256 || sourceFilesSHA256(root) !== sourceFilesChecksum ||
    controllerInputs.catalogs.some(input => sha256(readFileSync(input.path)) !== input.sha256)) {
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
