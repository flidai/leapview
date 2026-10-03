import { afterEach, expect, test } from 'bun:test'
import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const outputDirectory = '.tmp/production-topology-build-test'
const forbiddenHosts = ['cdn.jsdelivr.net', 'unpkg.com', 'esm.sh', 'skypack.dev']

test('Tailwind watcher reports source changes and excludes ignored directories', async () => {
  const require = createRequire(import.meta.url)
  const watcher = require('@parcel/watcher')
  const directory = await mkdtemp(join(tmpdir(), 'leapview-css-watcher-'))
  const source = join(directory, 'source.html')
  const ignored = join(directory, 'ignored', 'source.html')
  await mkdir(join(directory, 'ignored'))
  const events: { path: string; type: string }[] = []
  let subscription: { unsubscribe(): Promise<void> } | undefined
  let watcherError: Error | null = null
  try {
    subscription = await watcher.subscribe(directory, (error: Error | null, batch: { path: string; type: string }[]) => {
      watcherError = error
      if (!error) events.push(...batch)
    }, { ignore: ['ignored/**'] })
    await writeFile(ignored, '<div class="hidden"></div>')
    await writeFile(source, '<div class="flex"></div>')
    const deadline = performance.now() + 3500
    while (!watcherError && !events.some(event => event.path === source) && performance.now() < deadline) {
      await Bun.sleep(10)
    }
    expect(watcherError).toBeNull()
    expect(events.some(event => event.path === source && event.type === 'create')).toBe(true)
    expect(events.some(event => event.path === ignored)).toBe(false)
  } finally {
    await subscription?.unsubscribe()
    await rm(directory, { recursive: true, force: true })
  }
})

afterEach(async () => {
  await Bun.$`rm -rf ${outputDirectory}`.quiet()
})

test('production topology JavaScript has no external CDN dependencies', async () => {
  const result = await Bun.build({
    entrypoints: ['web/components/login/topology-background.ts'],
    target: 'browser',
    format: 'esm',
    define: { 'process.env.NODE_ENV': '"production"' },
    outdir: outputDirectory,
  })

  expect(result.success).toBe(true)

  const forbiddenReferences: string[] = []
  const files = new Bun.Glob('**/*.js')
  for await (const path of files.scan({ cwd: outputDirectory, onlyFiles: true })) {
    const source = await Bun.file(`${outputDirectory}/${path}`).text()
    for (const host of forbiddenHosts) {
      if (source.includes(host)) forbiddenReferences.push(`${path}: ${host}`)
    }
  }

  expect(forbiddenReferences).toEqual([])
})

test('production build does not publish the retired Vega-Lite sandbox', async () => {
  expect(await Bun.file('static/vega-sandbox.js').exists()).toBe(false)
})
