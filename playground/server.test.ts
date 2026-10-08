import { expect, test } from 'bun:test'
import { playgroundBuildResponse, playgroundResponse, precompressPlaygroundAssets } from './server'

test('the public response function remains a direct Bun fetch handler', async () => {
  const server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch: playgroundResponse })
  try {
    const page = await fetch(server.url)
    expect(page.status).toBe(200)
    expect(await page.text()).toContain('<playground-app>')
    // Bun supplies its Server as the callback's second argument. It must never
    // be interpreted as a build context, even when no assets have been built.
    const missing = await fetch(new URL('/assets/missing-response-test.js', server.url))
    expect(missing.status).toBe(404)
    expect(await missing.text()).toBe('Not found')
    expect(await fetch(new URL('/__playground/events', server.url)).then(response => response.status)).toBe(204)
  } finally { await server.stop(true) }
})

test('browser contract validation is precompiled and matches both schemas for every scenario', async () => {
  const { mkdtemp, rm } = await import('node:fs/promises')
  const { tmpdir } = await import('node:os')
  const { join } = await import('node:path')
  const { pathToFileURL } = await import('node:url')
  const fixtures = await import('./dashboard-contract-fixtures')
  const outputDirectory = await mkdtemp(join(tmpdir(), 'leapview-dashboard-validator-'))
  try {
    // Use the same browser plugin as the Playground, outside bun:test's resolver.
    const child = Bun.spawn([process.execPath, '-e', `
      import { dashboardValidationPlugin } from './playground/dashboard-contract-validation-build';
      const result = await Bun.build({
        entrypoints: ['./playground/dashboard-contract-fixtures.ts'],
        target: 'browser', format: 'esm',
        plugins: [dashboardValidationPlugin()],
        outdir: process.env.PLAYGROUND_VALIDATION_TEST_OUTPUT,
      });
      if (!result.success) throw new AggregateError(result.logs, 'Validation build failed');
    `], { cwd: new URL('..', import.meta.url).pathname, env: { ...process.env, PLAYGROUND_VALIDATION_TEST_OUTPUT: outputDirectory }, stdout: 'pipe', stderr: 'pipe' })
    const [code, stdout, stderr] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()])
    expect(code, `${stdout}${stderr}`).toBe(0)
    const path = join(outputDirectory, 'dashboard-contract-fixtures.js')
    const bundle = await Bun.file(path).text()
    expect(bundle).not.toContain('node_modules/ajv/dist/compile/')
    const originalFunction = globalThis.Function
    try {
      // Loading and using browser validators must also work without dynamic code evaluation.
      globalThis.Function = new Proxy(originalFunction, { apply() { throw new Error('Runtime schema compilation is forbidden') }, construct() { throw new Error('Runtime schema compilation is forbidden') } })
      const browserFixtures = await import(pathToFileURL(path).href)
      const sources = [...fixtures.dashboardScenarios.map(fixture => fixture.source), 'spec: [', 'kind: Dashboard\nkind: Dashboard', fixtures.afterYAML.replace('columnSpan: 12', 'columnSpan: 0').replace('  id: dashboard:executive-sales\n', '')]
      for (const source of sources) {
        for (const baseline of [false, true]) {
          expect(browserFixtures.inspectDashboard(source, baseline)).toEqual(fixtures.inspectDashboard(source, baseline))
        }
      }
    } finally { globalThis.Function = originalFunction }
  } finally { await rm(outputDirectory, { recursive: true, force: true }) }
}, 30000)


test('only content-hashed browser chunks are immutable across repeat loads', async () => {
  const { mkdtemp, rm } = await import('node:fs/promises')
  const { tmpdir } = await import('node:os')
  const { join } = await import('node:path')
  const outputDirectory = await mkdtemp(join(tmpdir(), 'leapview-playground-cache-'))
  const immutable = 'public, max-age=31536000, immutable'
  try {
    for (const file of ['app.js', 'app.css', 'product.css', 'monaco-editor-worker.js', 'chunks/example-deadbeef.js', 'chunks/example-ab12cd34.css', 'chunks/maplibre-gl-worker-6.10.0.mjs', 'chunks/unhashed.js']) {
      await Bun.write(join(outputDirectory, file), file)
    }
    const build = { buildID: 'cache-policy-test', outputDirectory }
    const cases = [
      ['/', 'no-store'],
      ['/index.html', 'no-store'],
      ['/assets/app.js', 'no-store'],
      ['/assets/app.css', 'no-store'],
      ['/static/app.css', 'no-store'],
      ['/static/monaco-editor-worker.js', 'no-store'],
      ['/assets/chunks/example-deadbeef.js', immutable],
      ['/static/chunks/example-deadbeef.js', immutable],
      ['/assets/chunks/example-ab12cd34.css', immutable],
      ['/assets/chunks/maplibre-gl-worker-6.10.0.mjs', 'no-store'],
      ['/assets/chunks/unhashed.js', 'no-store'],
    ]
    for (const [path, policy] of cases) {
      for (const method of ['GET', 'HEAD']) {
        const response = await playgroundBuildResponse(new Request(`http://localhost${path}`, { method }), build)
        expect(response.status, `${method} ${path}`).toBe(200)
        expect(response.headers.get('Cache-Control'), `${method} ${path}`).toBe(policy)
        if (method === 'HEAD') expect(await response.text()).toBe('')
      }
    }
  } finally { await rm(outputDirectory, { recursive: true, force: true }) }
})

test('dashboard initial module graph defers the Monaco editor runtime', async () => {
  const child = Bun.spawn([process.execPath, '-e', `
    import { resolve, dirname } from 'node:path';
    import { dashboardValidationPlugin } from './playground/dashboard-contract-validation-build';
    const result = await Bun.build({
      entrypoints: ['./playground/dashboard-contract.ts'],
      target: 'browser', format: 'esm', splitting: true,
      plugins: [dashboardValidationPlugin()],
    });
    if (!result.success) throw new AggregateError(result.logs, 'Dashboard load-path build failed');
    const sources = new Map(await Promise.all(result.outputs.filter(file => file.path.endsWith('.js')).map(async file => [resolve(file.path), await file.text()])));
    const entry = result.outputs.find(file => file.kind === 'entry-point' && file.path.endsWith('.js'));
    const parser = new Bun.Transpiler({ loader: 'js' });
    const visited = new Set();
    function visit(path) {
      if (visited.has(path)) return;
      visited.add(path);
      for (const dependency of parser.scan(sources.get(path)).imports) {
        if (dependency.kind === 'import-statement' && dependency.path.startsWith('.')) visit(resolve(dirname(path), dependency.path));
      }
    }
    visit(resolve(entry.path));
    console.log(JSON.stringify({
      initialIncludesMonaco: [...visited].some(path => sources.get(path).includes('node_modules/monaco-editor-core/')),
      deferredMonacoExists: [...sources].some(([path, source]) => !visited.has(path) && source.includes('node_modules/monaco-editor-core/')),
    }));
  `], { cwd: new URL('..', import.meta.url).pathname, stdout: 'pipe', stderr: 'pipe' })
  const [code, stdout, stderr] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()])
  expect(code, `${stdout}${stderr}`).toBe(0)
  expect(JSON.parse(stdout)).toEqual({ initialIncludesMonaco: false, deferredMonacoExists: true })
}, 30000)


test('precompressed assets negotiate gzip without changing their bytes, MIME type or cache policy', async () => {
  const { mkdtemp, rm } = await import('node:fs/promises')
  const { tmpdir } = await import('node:os')
  const { join } = await import('node:path')
  const outputDirectory = await mkdtemp(join(tmpdir(), 'leapview-playground-gzip-'))
  const source = 'console.log("dashboard source");\n'.repeat(300)
  const build = { buildID: 'gzip-policy-test', outputDirectory }
  let server: ReturnType<typeof Bun.serve> | undefined
  try {
    for (const file of ['app.js', 'product.css', 'chunks/example-deadbeef.js']) await Bun.write(join(outputDirectory, file), source)
    await precompressPlaygroundAssets(outputDirectory)
    await Bun.write(join(outputDirectory, 'chunks/identity-ab12cd34.js'), source)
    const cases = [
      ['gzip', 'gzip'],
      ['br, gzip;q=0.8', 'gzip'],
      ['gzip;q=0.5, identity;q=1', null],
      ['gzip;q=1, identity;q=0', 'gzip'],
      ['*;q=0, gzip;q=1', 'gzip'],
      ['gzip;q=invalid', null],
      ['xgzip', null],
      ['GZIP; Q=1', 'gzip'],
      ['*;q=0.8', 'gzip'],
      ['gzip;q=0', null],
      ['gzip;q=0, *;q=1', null],
      ['*;q=1, gzip;q=0.000', null],
      ['br', null],
      ['', null],
    ] as const
    for (const [acceptEncoding, encoding] of cases) {
      const headers = { 'Accept-Encoding': acceptEncoding }
      const response = await playgroundBuildResponse(new Request('http://localhost/assets/chunks/example-deadbeef.js', { headers }), build)
      expect(response.status).toBe(200)
      expect(response.headers.get('Content-Encoding')).toBe(encoding)
      expect(response.headers.get('Vary')).toBe('Accept-Encoding')
      expect(response.headers.get('Content-Type')).toBe(Bun.file(join(outputDirectory, 'app.js')).type)
      expect(response.headers.get('Cache-Control')).toBe('public, max-age=31536000, immutable')
      const bytes = new Uint8Array(await response.arrayBuffer())
      expect(new TextDecoder().decode(encoding ? Bun.gunzipSync(bytes) : bytes)).toBe(source)
      const head = await playgroundBuildResponse(new Request('http://localhost/assets/chunks/example-deadbeef.js', { method: 'HEAD', headers }), build)
      expect([...head.headers]).toEqual([...response.headers])
      expect(Number(head.headers.get('Content-Length'))).toBe(bytes.byteLength)
      expect(await head.text()).toBe('')
    }
    for (const path of ['/assets/app.js', '/static/app.css']) {
      const response = await playgroundBuildResponse(new Request(`http://localhost${path}`, { headers: { 'Accept-Encoding': 'gzip' } }), build)
      expect(response.headers.get('Content-Encoding')).toBe('gzip')
      expect(response.headers.get('Cache-Control')).toBe('no-store')
      expect(Bun.gunzipSync(new Uint8Array(await response.arrayBuffer())).byteLength).toBe(new TextEncoder().encode(source).byteLength)
    }
    const fallback = await playgroundBuildResponse(new Request('http://localhost/assets/chunks/identity-ab12cd34.js', { headers: { 'Accept-Encoding': 'gzip' } }), build)
    expect(fallback.headers.get('Content-Encoding')).toBeNull()
    expect(await fallback.text()).toBe(source)
    for (const method of ['GET', 'HEAD']) {
      const unacceptable = await playgroundBuildResponse(new Request('http://localhost/assets/app.js', { method, headers: { 'Accept-Encoding': 'gzip;q=0, identity;q=0' } }), build)
      expect(unacceptable.status).toBe(406)
      expect(unacceptable.headers.get('Content-Type')).toBe('text/plain; charset=utf-8')
      expect(unacceptable.headers.get('Content-Length')).toBe(String('No acceptable content encoding'.length))
      if (method === 'HEAD') expect(await unacceptable.text()).toBe('')
    }
    for (const path of ['/', '/static/theme.js', '/static/favicon.svg']) {
      for (const acceptEncoding of ['identity;q=0', '*;q=0', 'gzip;q=1, identity;q=0']) {
        for (const method of ['GET', 'HEAD']) {
          const refused = await playgroundBuildResponse(new Request(`http://localhost${path}`, { method, headers: { 'Accept-Encoding': acceptEncoding } }), build)
          expect(refused.status, `${method} ${path}: ${acceptEncoding}`).toBe(406)
          expect(refused.headers.get('Vary')).toBe('Accept-Encoding')
          expect(refused.headers.get('Cache-Control')).toBe('no-store')
          if (method === 'HEAD') expect(await refused.text()).toBe('')
        }
      }
    }
    const html = await playgroundBuildResponse(new Request('http://localhost/', { headers: { 'Accept-Encoding': 'gzip' } }), build)
    expect(html.headers.get('Content-Encoding')).toBeNull()
    expect(html.headers.get('Cache-Control')).toBe('no-store')
    server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch: request => playgroundBuildResponse(request, build) })
    const gzip = await fetch(new URL('/assets/app.js', server.url), { headers: { 'Accept-Encoding': 'gzip' } })
    expect(gzip.headers.get('Content-Encoding')).toBe('gzip')
    expect(await gzip.text()).toBe(source)
    const identity = await fetch(new URL('/assets/app.js', server.url), { headers: { 'Accept-Encoding': 'gzip;q=0' } })
    expect(identity.headers.get('Content-Encoding')).toBeNull()
    expect(await identity.text()).toBe(source)
  } finally { await server?.stop(true); await rm(outputDirectory, { recursive: true, force: true }) }
})
