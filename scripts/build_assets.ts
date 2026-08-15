import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'

type BuildOptions = Parameters<typeof Bun.build>[0]

type AssetBuild = {
  label: string
  clean: string[]
  options: BuildOptions
}

const externalModules = [datastarRuntimeURL]

const builds: AssetBuild[] = [
  {
    label: 'frontend',
    clean: [
      'static/app-shell.js',
      'static/catalog-page.js',
      'static/dashboard-page.js',
      'static/dashboard-builder.js',
      'static/workspace-page.js',
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
        'web/components/workspace/workspace-page.ts',
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
      define: { 'process.env.NODE_ENV': '"production"' },
      external: externalModules,
      outdir: 'static',
      naming: { entry: '[name].[ext]', chunk: 'chunks/shared-[name]-[hash].[ext]' },
    },
  },
]

for (const build of builds) {
  await runBuild(build)
}
await Bun.write('static/monaco-editor-css.css', Bun.file('static/admin-page.css'))
await validateProductionJavaScriptBundles()
await writeStaticAssetVersion()

async function runBuild(build: AssetBuild): Promise<void> {
  await cleanPaths(build.clean)
  const result = await Bun.build(build.options)
  for (const log of result.logs) {
    console.error(log)
  }
  if (!result.success) {
    throw new Error(`failed to build ${build.label}`)
  }
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
  const files = new Bun.Glob('static/**/*.js')

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
  for (const pattern of ['static/**/*.css', 'static/**/*.js']) {
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
