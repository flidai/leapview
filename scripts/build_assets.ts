import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'
import { buildMapLibreWorker } from './build_maplibre_worker'
import {
  frontendCommitIdentity,
  frontendLockfileSha256,
  frontendPackageManager,
  frontendSourceInputDigest,
} from './frontend_bundle_identity'
import { posix } from 'node:path'

type BuildOptions = Parameters<typeof Bun.build>[0]
type BuildResult = Awaited<ReturnType<typeof Bun.build>>

type AssetBuild = {
  label: string
  clean: string[]
  options: BuildOptions
}

type MetafileOutput = {
  bytes: number
  entryPoint?: string
  imports?: Array<{ path: string }>
}

type BuildMetafile = {
  outputs: Record<string, MetafileOutput>
}

type BundleBytes = {
  rawBytes: number
  gzipBytes: number
}

type BundleEvidenceMeasurement = BundleBytes & {
  files: string[]
}

type FrontendBundleEvidence = {
  version: 1
  generatedBy: 'scripts/build_assets.ts'
  identity: FrontendBundleBuildIdentity
  entries: Record<string, BundleEvidenceMeasurement>
  aggregate: BundleEvidenceMeasurement
}

type FrontendBundleBuildIdentity = {
  commit: string | null
  commitSource: 'git' | 'build-arg' | 'unavailable'
  sourceInputDigest: string
  bunVersion: string
  nodeVersion: string
  platform: string
  architecture: string
  packageManager: string
  lockfilePath: string
  lockfileSha256: string
}

const externalModules = [datastarRuntimeURL]

const standaloneEntries = [
  { name: 'login-background-loader', path: 'login-background-loader.js' },
  { name: 'theme', path: 'theme.js' },
] as const

const builds: AssetBuild[] = [
  {
    label: 'frontend',
    clean: [
      'static/app-shell.js',
      'static/catalog-page.js',
      'static/dashboard-page.js',
      'static/dashboard-builder.js',
      'static/project-page.js',
      'static/data-explorer.js',
      'static/chat-page.js',
      'static/admin-page.js',
      'static/login-page.js',
      'static/monaco-editor-worker.js',
      'static/url-sync.js',
      'static/datastar-inspector.js',
      'static/command.js',
      'static/topology-background.js',
      'static/semantic-model-graph.js',
      'static/asset-lineage-graph.js',
      'static/admin-page.css',
      'static/monaco-editor-css.css',
      'static/semantic-model-graph.css',
      'static/asset-lineage-graph.css',
      'static/vega-sandbox.js',
      'static/chunks/*',
    ],
    options: {
      entrypoints: [
        'web/components/app/app-shell.ts',
        'web/components/app/catalog-page.ts',
        'web/components/dashboard/dashboard-page.ts',
        'web/components/dashboard/dashboard-builder.ts',
        'web/components/project/project-page.ts',
        'web/components/data/data-explorer.ts',
        'web/components/chat/chat-page.ts',
        'web/components/admin/admin-page.ts',
        'web/components/login/login-page.ts',
        'web/components/shared/monaco-editor-worker.ts',
        'web/components/dashboard/filters/url-sync.ts',
        'web/components/inspector/datastar-inspector.ts',
        'web/components/shared/command.ts',
        'web/components/login/topology-background.ts',
        'web/components/shared/semantic-model-graph.ts',
        'web/components/shared/asset-lineage-graph.ts',
      ],
      target: 'browser',
      format: 'esm',
      splitting: true,
      metafile: true,
      define: { 'process.env.NODE_ENV': '"production"' },
      external: externalModules,
      outdir: 'static',
      naming: { entry: '[name].[ext]', chunk: 'chunks/shared-[name]-[hash].[ext]' },
    },
  },
]

const buildResults: Array<{ build: AssetBuild; result: BuildResult }> = []
for (const build of builds) {
  buildResults.push({ build, result: await runBuild(build) })
}
await Bun.write('static/monaco-editor-css.css', Bun.file('static/admin-page.css'))
await buildMapLibreWorker('static')
await writeFrontendBundleEvidence(buildResults)
await validateProductionJavaScriptBundles()
await writeStaticAssetVersion()

async function runBuild(build: AssetBuild): Promise<BuildResult> {
  await cleanPaths(build.clean)
  const result = await Bun.build(build.options)
  for (const log of result.logs) {
    console.error(log)
  }
  if (!result.success) {
    throw new Error(`failed to build ${build.label}`)
  }
  return result
}

function normalizeOutputPath(path: string): string {
  const normalized = posix.normalize(path).replace(/^\.\//, '')
  if (normalized === '..' || normalized.startsWith('../') || normalized.startsWith('/')) {
    throw new Error(`frontend bundle evidence encountered an unsafe output path ${path}`)
  }
  return normalized
}

function resolveOutputImport(imported: string): string | null {
  if (!imported.startsWith('.')) return null
  // Bun metafile import paths are relative to the configured outdir, even
  // when the importing output itself lives under a nested chunks directory.
  return normalizeOutputPath(imported)
}

function logicalEntryName(entryPoint: string): string {
  const fileName = entryPoint.split('/').at(-1) ?? entryPoint
  return fileName.replace(/\.[^.]+$/, '')
}

async function measureBundleFile(path: string): Promise<BundleBytes> {
  const file = Bun.file(`static/${path}`)
  if (!(await file.exists())) throw new Error(`frontend bundle evidence is missing emitted file static/${path}`)
  const bytes = new Uint8Array(await file.arrayBuffer())
  return { rawBytes: bytes.byteLength, gzipBytes: Bun.gzipSync(bytes).byteLength }
}

async function frontendBuildIdentity(): Promise<FrontendBundleBuildIdentity> {
  return {
    ...(await frontendCommitIdentity()),
    sourceInputDigest: frontendSourceInputDigest(),
    bunVersion: Bun.version,
    nodeVersion: process.version,
    platform: process.platform,
    architecture: process.arch,
    packageManager: frontendPackageManager(),
    lockfilePath: 'bun.lock',
    lockfileSha256: frontendLockfileSha256(),
  }
}

async function writeFrontendBundleEvidence(results: Array<{ build: AssetBuild; result: BuildResult }>): Promise<void> {
  const outputMetadata = new Map<string, MetafileOutput>()
  const entryOutputs = new Map<string, string>()
  for (const { build, result } of results) {
    const configuredEntryPoints = new Set((build.options.entrypoints ?? []).map(String))
    const metafile = (result as BuildResult & { metafile?: BuildMetafile }).metafile
    if (!metafile || typeof metafile !== 'object' || !metafile.outputs || typeof metafile.outputs !== 'object') {
      throw new Error('frontend bundle evidence requires Bun metafile output from every production build')
    }
    for (const [rawPath, output] of Object.entries(metafile.outputs)) {
      const outputPath = normalizeOutputPath(rawPath)
      if (!outputPath.endsWith('.js') && !outputPath.endsWith('.mjs')) continue
      if (outputMetadata.has(outputPath)) throw new Error(`frontend bundle evidence has duplicate emitted file ${outputPath}`)
      outputMetadata.set(outputPath, output)
      if (output.entryPoint && configuredEntryPoints.has(output.entryPoint)) {
        const logicalName = logicalEntryName(output.entryPoint)
        if (entryOutputs.has(logicalName)) throw new Error(`frontend bundle evidence has duplicate logical entry ${logicalName}`)
        entryOutputs.set(logicalName, outputPath)
      }
    }
  }
  if (entryOutputs.size === 0) throw new Error('frontend bundle evidence found no logical production entrypoints')

  const filesByEntry = new Map<string, string[]>()
  for (const [logicalName, outputPath] of [...entryOutputs.entries()].sort(([left], [right]) => left.localeCompare(right))) {
    const files = new Set<string>()
    const visit = (path: string): void => {
      if (files.has(path)) return
      const output = outputMetadata.get(path)
      if (!output) throw new Error(`frontend bundle evidence cannot resolve emitted import ${path} for ${logicalName}`)
      files.add(path)
      for (const imported of output.imports ?? []) {
        const importedPath = resolveOutputImport(imported.path)
        if (importedPath) visit(importedPath)
      }
    }
    visit(outputPath)
    filesByEntry.set(logicalName, [...files].sort())
  }

  const workerGlob = new Bun.Glob('static/chunks/maplibre-gl-worker-*.mjs')
  const workerPaths: string[] = []
  for await (const path of workerGlob.scan({ cwd: '.', onlyFiles: true })) workerPaths.push(path.replace(/^static\//, ''))
  if (workerPaths.length !== 1) throw new Error(`frontend bundle evidence expected exactly one MapLibre worker, found ${workerPaths.length}`)
  filesByEntry.set('maplibre-gl-worker', workerPaths)

  const runtimePath = datastarRuntimeURL.split('?')[0].replace(/^\/+/, '').replace(/^static\//, '')
  if (!runtimePath) throw new Error('frontend bundle evidence could not resolve the external Datastar runtime path')
  filesByEntry.set('datastar-runtime', [runtimePath])

  for (const { name, path } of standaloneEntries) {
    if (filesByEntry.has(name)) throw new Error(`frontend bundle evidence has duplicate logical entry ${name}`)
    if (!(await Bun.file(`static/${path}`).exists())) {
      throw new Error(`frontend bundle evidence is missing standalone shipped JavaScript static/${path}`)
    }
    filesByEntry.set(name, [path])
  }

  const accountedOutputs = new Set([...filesByEntry.values()].flat())
  const unaccountedOutputs = [...outputMetadata.keys()].filter((path) => !accountedOutputs.has(path)).sort()
  if (unaccountedOutputs.length > 0) {
    throw new Error(`frontend bundle evidence found unaccounted emitted JavaScript: ${unaccountedOutputs.join(', ')}`)
  }

  const shippedJavaScript: string[] = []
  for await (const path of new Bun.Glob('static/**/*.{js,mjs}').scan({ cwd: '.', dot: true, onlyFiles: true })) {
    shippedJavaScript.push(path.replace(/^static\//, ''))
  }
  shippedJavaScript.sort()
  const accountedJavaScript = new Set([...filesByEntry.values()].flat())
  const omittedJavaScript = shippedJavaScript.filter((path) => !accountedJavaScript.has(path))
  if (omittedJavaScript.length > 0) {
    throw new Error(`frontend bundle evidence omitted shipped JavaScript: ${omittedJavaScript.join(', ')}`)
  }

  const fileBytes = new Map<string, BundleBytes>()
  const measure = async (path: string): Promise<BundleBytes> => {
    const existing = fileBytes.get(path)
    if (existing) return existing
    const bytes = await measureBundleFile(path)
    fileBytes.set(path, bytes)
    return bytes
  }
  const entries: Record<string, BundleEvidenceMeasurement> = {}
  const aggregateFiles = new Set<string>()
  for (const [logicalName, files] of [...filesByEntry.entries()].sort(([left], [right]) => left.localeCompare(right))) {
    let rawBytes = 0
    let gzipBytes = 0
    for (const path of files) {
      const bytes = await measure(path)
      rawBytes += bytes.rawBytes
      gzipBytes += bytes.gzipBytes
      aggregateFiles.add(path)
    }
    entries[logicalName] = { files, rawBytes, gzipBytes }
  }
  let aggregateRawBytes = 0
  let aggregateGzipBytes = 0
  for (const path of [...aggregateFiles].sort()) {
    const bytes = await measure(path)
    aggregateRawBytes += bytes.rawBytes
    aggregateGzipBytes += bytes.gzipBytes
  }

  const evidence: FrontendBundleEvidence = {
    version: 1,
    generatedBy: 'scripts/build_assets.ts',
    identity: await frontendBuildIdentity(),
    entries,
    aggregate: {
      files: [...aggregateFiles].sort(),
      rawBytes: aggregateRawBytes,
      gzipBytes: aggregateGzipBytes,
    },
  }
  await Bun.$`mkdir -p .tmp`.quiet()
  await Bun.write('.tmp/frontend-bundle-evidence.json', `${JSON.stringify(evidence, null, 2)}\n`)
}

async function cleanPaths(paths: string[]): Promise<void> {
  await Promise.all(paths.map((path) => removePath(path)))
}

async function removePath(path: string): Promise<void> {
  const glob = new Bun.Glob(path)
  let removed = false
  for await (const match of glob.scan({ cwd: '.', dot: true, onlyFiles: false })) {
    await Bun.$`rm -rf ${match}`.quiet()
    removed = true
  }
  if (!removed && !path.includes('*')) {
    await Bun.$`rm -rf ${path}`.quiet()
  }
}

async function validateProductionJavaScriptBundles(): Promise<void> {
  const forbiddenHosts = ['cdn.jsdelivr.net', 'unpkg.com', 'esm.sh', 'skypack.dev']
  const files = new Bun.Glob('static/**/*.{js,mjs}')

  for await (const path of files.scan({ cwd: '.', dot: true, onlyFiles: true })) {
    const text = await Bun.file(path).text()

    for (const host of forbiddenHosts) {
      if (text.includes(host)) {
        throw new Error(`${path} references external asset host ${host}; production bundles must be self-contained`)
      }
    }
  }
}

async function writeStaticAssetVersion(): Promise<void> {
  const paths: string[] = []
  for (const pattern of ['static/**/*.css', 'static/**/*.{js,mjs}']) {
    const glob = new Bun.Glob(pattern)
    for await (const path of glob.scan({ cwd: '.', dot: true, onlyFiles: true })) {
      paths.push(path)
    }
  }
  paths.sort()
  const hasher = new Bun.CryptoHasher('sha256')
  for (const path of paths) {
    hasher.update(path)
    hasher.update(new Uint8Array(await Bun.file(path).arrayBuffer()))
  }
  await Bun.write('static/asset-version.txt', hasher.digest('hex').slice(0, 16) + '\n')
}
