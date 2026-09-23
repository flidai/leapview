import { afterEach, expect, test } from 'bun:test'

const outputDirectory = '.tmp/production-topology-build-test'
const forbiddenHosts = ['cdn.jsdelivr.net', 'unpkg.com', 'esm.sh', 'skypack.dev']

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
