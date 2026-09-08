import { existsSync, lstatSync, readdirSync, readFileSync, realpathSync } from 'node:fs'
import { execFileSync } from 'node:child_process'
import { join, relative } from 'node:path'

import { createHash } from 'node:crypto'

export type FrontendCommitIdentity = {
  commit: string | null
  commitSource: 'git' | 'build-arg' | 'unavailable'
}

export type FrontendBundleWriterMode = 'update' | 'propose' | 'apply'

export type FrontendBundleWriterIdentity = Pick<FrontendCommitIdentity, 'commit' | 'commitSource'>

const COMMIT_SHA = /^[0-9a-f]{40}$/
// Keep this list limited to authored inputs that can change the production
// output. Generated static bundles are deliberately excluded: they are the
// evidence, not an input to the build. The standalone runtime files are
// shipped without passing through Bun.build and therefore belong here too.
const SOURCE_ROOTS = [
  'package.json',
  'bun.lock',
  'tsconfig.json',
  'static/app.input.css',
  'static/login-background-loader.js',
  'static/theme.js',
  'static/vendor/datastar-1.0.2.js',
  'scripts/build_assets.ts',
  'scripts/frontend_bundle_options.ts',
  'scripts/build_maplibre_worker.ts',
  'scripts/generate_lucide_icon_catalog.ts',
  'scripts/generate_visualization_validator.ts',
  'web',
]

export const FRONTEND_LOCKFILE_PATH = 'bun.lock'

function sourceFiles(path: string): string[] {
  if (!existsSync(path)) throw new Error(`frontend bundle identity: required source input ${path} is missing`)
  const stat = lstatSync(path)
  if (stat.isFile()) return [path]
  if (!stat.isDirectory()) throw new Error(`frontend bundle identity: source input ${path} is not a regular file or directory`)
  return readdirSync(path, { withFileTypes: true })
    .sort((left, right) => left.name.localeCompare(right.name))
    .flatMap((entry) => sourceFiles(join(path, entry.name)))
}

export function frontendSourceInputDigest(root = '.'): string {
  const paths = SOURCE_ROOTS.flatMap((path) => sourceFiles(join(root, path)))
    .map((path) => relative(root, path).split('\\').join('/'))
    .sort()
  const hasher = new Bun.CryptoHasher('sha256')
  for (const path of paths) {
    hasher.update(path)
    hasher.update('\0')
    hasher.update(new Uint8Array(readFileSync(join(root, path))))
  }
  return hasher.digest('hex')
}

export function frontendPackageManager(root = '.'): string {
  const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8')) as { packageManager?: unknown }
  if (typeof manifest.packageManager !== 'string' || manifest.packageManager.length === 0) {
    throw new Error('frontend bundle identity: package.json must declare a non-empty packageManager')
  }
  return manifest.packageManager
}

export function frontendLockfileSha256(root = '.'): string {
  const path = join(root, FRONTEND_LOCKFILE_PATH)
  if (!existsSync(path)) throw new Error(`frontend bundle identity: required lockfile ${FRONTEND_LOCKFILE_PATH} is missing`)
  return createHash('sha256').update(readFileSync(path)).digest('hex')
}

export async function frontendCommitIdentity(): Promise<FrontendCommitIdentity> {
  const providedBuildRevision = process.env.BUILD_REVISION
  if (providedBuildRevision !== undefined) {
    const buildRevision = providedBuildRevision.trim()
    if (!COMMIT_SHA.test(buildRevision)) {
      throw new Error('frontend bundle identity: provided BUILD_REVISION must be a 40-character lowercase commit SHA')
    }
    return { commit: buildRevision, commitSource: 'build-arg' }
  }
  try {
    const revision = (await Bun.$`git rev-parse HEAD`.quiet()).text().trim()
    if (COMMIT_SHA.test(revision)) return { commit: revision, commitSource: 'git' }
  } catch {
    // Source archives and Docker build contexts intentionally have no Git metadata.
  }
  return { commit: null, commitSource: 'unavailable' }
}

export function currentGitRevision(): string | null {
  try {
    const revision = execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim()
    return COMMIT_SHA.test(revision) ? revision : null
  } catch {
    return null
  }
}

function gitOutput(arguments_: string[], cwd: string): string {
  return execFileSync('git', arguments_, {
    cwd,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  }).trim()
}

export function assertFrontendBundleWriterProvenance(
  mode: FrontendBundleWriterMode,
  identity: FrontendBundleWriterIdentity,
  environment: Record<string, string | undefined> = process.env,
  cwd = process.cwd(),
): void {
  if (environment.BUILD_REVISION !== undefined) {
    throw new Error(`frontend bundle budget: ${mode} requires the real Git HEAD; BUILD_REVISION cannot override writer provenance`)
  }
  if (identity.commitSource !== 'git' || identity.commit === null) {
    throw new Error(`frontend bundle budget: ${mode} requires evidence recorded from real Git metadata`)
  }
  if (!COMMIT_SHA.test(identity.commit)) {
    throw new Error(`frontend bundle budget: ${mode} requires a 40-character recorded Git commit SHA`)
  }

  let head: string
  let insideWorkTree: string
  let workTreeRoot: string
  let canonicalCwd: string
  let status: string
  try {
    canonicalCwd = realpathSync(cwd)
    workTreeRoot = gitOutput(['rev-parse', '--show-toplevel'], cwd)
    insideWorkTree = gitOutput(['rev-parse', '--is-inside-work-tree'], cwd)
    head = gitOutput(['rev-parse', '--verify', 'HEAD'], cwd)
    status = gitOutput(['status', '--porcelain=v1', '--untracked-files=all'], cwd)
  } catch (error) {
    throw new Error(`frontend bundle budget: ${mode} requires a clean Git checkout with real metadata (${error instanceof Error ? error.message : String(error)})`)
  }
  if (insideWorkTree !== 'true' || !COMMIT_SHA.test(head)) {
    throw new Error(`frontend bundle budget: ${mode} requires a real 40-character Git HEAD in a work tree`)
  }
  if (workTreeRoot !== canonicalCwd) {
    throw new Error(`frontend bundle budget: ${mode} must run from the Git worktree root (${workTreeRoot})`)
  }
  if (head !== identity.commit) {
    throw new Error(`frontend bundle budget: ${mode} evidence commit ${identity.commit} does not match real Git HEAD ${head}`)
  }
  if (status.length > 0) {
    throw new Error(`frontend bundle budget: ${mode} requires a clean nonignored working tree and index (git status is not empty)`)
  }
}
