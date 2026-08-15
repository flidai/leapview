import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'

type BuildOptions = Parameters<typeof Bun.build>[0]

type FixtureBuild = {
  label: string
  clean: string[]
  options: BuildOptions
  copy?: Array<{ from: string; to: string }>
}

const externalModules = [datastarRuntimeURL]

const fixtures = new Map<string, FixtureBuild>([
  ['app-shell', single('app-shell', 'web/components/app/app-shell.ts', '.tmp/app-shell-test/app-shell-under-test.js')],
  ['catalog-page', single('catalog-page', 'web/components/app/catalog-page.ts', '.tmp/catalog-page-test/catalog-page-under-test.js')],
  [
    'dashboard-page',
    split(
      'dashboard-page',
      'web/components/dashboard/dashboard-page.ts',
      '.tmp/dashboard-page-test',
      'dashboard-page-under-test.js',
      'chunks/[name]-[hash].[ext]',
    ),
  ],
  ['dashboard-builder', single('dashboard-builder', 'web/components/dashboard/dashboard-builder.ts', '.tmp/dashboard-builder-test/dashboard-builder-under-test.js')],
  [
    'chat-page',
    split('chat-page', 'web/components/chat/chat-page.ts', '.tmp/chat-page-test', 'chat-page-under-test.js', 'chunks/[name]-[hash].[ext]'),
  ],
  [
    'chat-composer',
    single('chat-composer', 'web/components/chat/chat-composer.ts', '.tmp/chat-composer-test/chat-composer-under-test.js'),
  ],
  ['chat-thread', split('chat-thread', 'web/components/chat/chat-page.ts', '.tmp/chat-thread-test', 'chat-under-test.js', 'chunks/[name]-[hash].[ext]')],
  ['code-block', single('code-block', 'web/components/shared/code-block.ts', '.tmp/code-block-test/code-block-under-test.js')],
  ['workspace-page', single('workspace-page', 'web/components/workspace/workspace-page.ts', '.tmp/workspace-page-test/workspace-page-under-test.js')],
  ['data-explorer', single('data-explorer', 'web/components/data/data-explorer.ts', '.tmp/data-explorer-test/data-explorer-under-test.js')],
  ['windowed-table', single('windowed-table', 'web/components/shared/windowed-table.ts', '.tmp/windowed-table-test/windowed-table-under-test.js')],
  ['filter-menu', single('filter-menu', 'web/components/shared/filter-menu.ts', '.tmp/filter-menu-test/filter-menu-under-test.js')],
  ['entity-multi-select', single('entity-multi-select', 'web/components/shared/entity-multi-select.ts', '.tmp/entity-multi-select-test/entity-multi-select-under-test.js')],
  ['datastar-inspector', single('datastar-inspector', 'web/components/inspector/datastar-inspector.ts', '.tmp/datastar-inspector-test/datastar-inspector-under-test.js')],
  ['admin-page', pageWithMonacoWorker('admin-page', 'web/components/admin/admin-page.ts', '.tmp/admin-page-test', 'admin-page-under-test.js')],
  ['code-editor', pageWithMonacoWorker('code-editor', 'web/components/shared/code-editor.ts', '.tmp/code-editor-test', 'code-editor-under-test.js')],
  ['markdown-view', single('markdown-view', 'web/components/shared/markdown-view.ts', '.tmp/markdown-view-test/markdown-view-under-test.js')],
  ['login-page', single('login-page', 'web/components/login/login-page.ts', '.tmp/login-page-test/login-page-under-test.js')],
  [
    'topology-background',
    single(
      'topology-background',
      'web/components/login/topology-background.ts',
      '.tmp/topology-background-test/topology-background-under-test.js',
    ),
  ],
  ['record-table', single('record-table', 'web/components/shared/record-table.ts', '.tmp/record-table-test/record-table-under-test.js')],
  ['visual-modal', single('visual-modal', 'web/components/dashboard/visual-modal.ts', '.tmp/visual-modal-under-test.js')],
  [
    'semantic-model-graph',
    {
      label: 'semantic-model-graph',
      clean: ['.tmp/semantic-model-graph-test'],
      options: {
        entrypoints: ['web/components/shared/semantic-model-graph.ts'],
        target: 'browser',
        format: 'esm',
        external: externalModules,
        outdir: '.tmp/semantic-model-graph-test',
        naming: { entry: 'semantic-model-graph.[ext]' },
      },
      copy: [{ from: '.tmp/semantic-model-graph-test/semantic-model-graph.js', to: '.tmp/semantic-model-graph-test/semantic-model-graph-under-test.js' }],
    },
  ],
  [
    'asset-lineage',
    {
      label: 'asset-lineage',
      clean: ['.tmp/asset-lineage-test'],
      options: {
        entrypoints: ['web/components/shared/asset-lineage-graph.ts'],
        target: 'browser',
        format: 'esm',
        external: externalModules,
        outdir: '.tmp/asset-lineage-test',
        naming: { entry: 'asset-lineage-graph.[ext]' },
      },
      copy: [{ from: '.tmp/asset-lineage-test/asset-lineage-graph.js', to: '.tmp/asset-lineage-test/asset-lineage-graph-under-test.js' }],
    },
  ],
])

const requested = Bun.argv.slice(2)
if (requested.length === 0) {
  console.error(`usage: bun scripts/build_test_assets.ts ${Array.from(fixtures.keys()).join('|')} [...]`)
  process.exit(2)
}

for (const name of requested) {
  const fixture = fixtures.get(name)
  if (!fixture) {
    console.error(`unknown test fixture ${JSON.stringify(name)}`)
    process.exit(2)
  }
  await runBuild(fixture)
}

function single(label: string, entrypoint: string, outputPath: string): FixtureBuild {
  const output = outputParts(outputPath)
  return {
    label,
    clean: [outputPath],
    options: {
      entrypoints: [entrypoint],
      target: 'browser',
      format: 'esm',
      external: externalModules,
      outdir: output.dir,
      naming: { entry: output.name },
    },
  }
}

function split(label: string, entrypoint: string, outdir: string, entryName: string, chunkName: string): FixtureBuild {
  return {
    label,
    clean: [outdir],
    options: {
      entrypoints: [entrypoint],
      target: 'browser',
      format: 'esm',
      splitting: true,
      external: externalModules,
      outdir,
      naming: { entry: entryName, chunk: chunkName },
    },
  }
}

function pageWithMonacoWorker(label: string, entrypoint: string, outdir: string, entryName: string): FixtureBuild {
  const entryBase = entryName.replace(/\.js$/, '')
  return {
    label,
    clean: [outdir],
    options: {
      entrypoints: [entrypoint, 'web/components/shared/monaco-editor-worker.ts'],
      target: 'browser',
      format: 'esm',
      splitting: true,
      external: externalModules,
      outdir,
      naming: { entry: '[name].[ext]', chunk: `chunks/${entryBase}-[name]-[hash].[ext]` },
    },
    copy: [
      { from: `${outdir}/${entrypointName(entrypoint)}.js`, to: `${outdir}/${entryName}` },
      { from: `${outdir}/monaco-editor-worker.js`, to: `${outdir}/static/monaco-editor-worker.js` },
      { from: `${outdir}/${entrypointName(entrypoint)}.css`, to: `${outdir}/static/monaco-editor-css.css` },
    ],
  }
}

async function runBuild(build: FixtureBuild): Promise<void> {
  await cleanPaths(build.clean)
  const result = await Bun.build(build.options)
  for (const log of result.logs) {
    console.error(log)
  }
  if (!result.success) {
    throw new Error(`failed to build ${build.label}`)
  }
  for (const copy of build.copy ?? []) {
    await ensureParentDir(copy.to)
    await Bun.write(copy.to, Bun.file(copy.from))
  }
}

async function cleanPaths(paths: string[]): Promise<void> {
  await Promise.all(paths.map((path) => Bun.$`rm -rf ${path}`.quiet()))
}

function outputParts(path: string): { dir: string; name: string } {
  const slash = path.lastIndexOf('/')
  if (slash < 0) {
    return { dir: '.', name: path }
  }
  return { dir: path.slice(0, slash), name: path.slice(slash + 1) }
}

function entrypointName(path: string): string {
  const slash = path.lastIndexOf('/')
  const name = slash < 0 ? path : path.slice(slash + 1)
  return name.replace(/\.[^.]+$/, '')
}

async function ensureParentDir(path: string): Promise<void> {
  const slash = path.lastIndexOf('/')
  if (slash < 0) return
  await Bun.$`mkdir -p ${path.slice(0, slash)}`.quiet()
}
