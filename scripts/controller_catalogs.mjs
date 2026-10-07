// Bind constructor catalogs to the checkout before compilation, then require
// each generator's compiled binding to match that same checkout at runtime.
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'
import { checkoutSnapshot } from './audit_source.mjs'

const hash = value => createHash('sha256').update(value).digest('hex')
export function sourceFingerprint(snapshot) {
  return hash(`${snapshot.commit}\n${hash(snapshot.workingTreeStatus)}\n${snapshot.trackedDiffSHA256}\n${snapshot.sourceFilesSHA256}\n`)
}

export function generateControllerCatalogs(root, out, execute = execFileSync) {
  const snapshot = checkoutSnapshot(root)
  const fingerprint = sourceFingerprint(snapshot)
  for (const { cgo, tags } of [{ cgo: '0', tags: '' }, { cgo: '1', tags: 'duckdb_arrow' }]) {
    // Overlays, alternate modfiles and external workspaces can compile source
    // outside the fingerprint. Use this module with explicit bounded flags.
    // Only the checksum is embedded; no source or private values are added.
    execute('go', ['run', '-p=1', '-mod=readonly', `-tags=${tags}`, `-ldflags=-X=main.compiledSourceFingerprint=${fingerprint}`,
      './internal/app/tools/controllercatalog', '-root', root, '-out', out],
    { cwd: root, env: { ...process.env, CGO_ENABLED: cgo, GOFLAGS: '', GOWORK: 'off', GOENV: 'off' }, stdio: 'inherit' })
    if (sourceFingerprint(checkoutSnapshot(root)) !== fingerprint) throw new Error('source changed while compiling controller catalogs; rerun on a stable checkout')
  }
}

function main() {
  const { values } = parseArgs({ options: { root: { type: 'string' }, out: { type: 'string' }, help: { type: 'boolean' } } })
  if (values.help) {
    console.log('Usage: node scripts/controller_catalogs.mjs [--root REPO] [--out DIR]\nCompile both shipped controller variants and inventory explicit constructed commands without executing them. Outputs default to .tmp/audit/controller-catalogs. Requires prepared Go source generation; task audit:controller-catalogs supplies it.')
    return
  }
  const root = resolve(values.root ?? dirname(dirname(fileURLToPath(import.meta.url))))
  const out = resolve(values.out ?? join(root, '.tmp/audit/controller-catalogs'))
  generateControllerCatalogs(root, out)
}
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main()
