import { afterEach, expect, test } from 'bun:test'
import { productionMinify } from './frontend_bundle_options'
import type { FrontendBundleEvidence } from './frontend_bundle_budget'
import { verifyFrontendBundleFiles } from './frontend_bundle_files'
import { pathToFileURL } from 'node:url'

const outputDirectory = '.tmp/production-topology-build-test'
const minifyOutputDirectory = '.tmp/production-whitespace-minify-test'
const forbiddenHosts = ['cdn.jsdelivr.net', 'unpkg.com', 'esm.sh', 'skypack.dev']

afterEach(async () => {
  await Bun.$`rm -rf ${outputDirectory}`.quiet()
  await Bun.$`rm -rf ${minifyOutputDirectory}`.quiet()
})

test('production minification removes formatting without renaming or transforming syntax', async () => {
  expect(productionMinify).toEqual({ whitespace: true, syntax: false, identifiers: false })
  const entrypoint = `${minifyOutputDirectory}/entry.ts`
  await Bun.write(entrypoint, `/*! @license whitespace-only fixture */
export function preserveReadableIdentifier(value: number) {
  const preserveThisConst = value + 1
  return preserveThisConst
}
`)

  const unminified = await Bun.build({
    entrypoints: [entrypoint],
    target: 'browser',
    format: 'esm',
    outdir: `${minifyOutputDirectory}/unminified`,
  })
  const minified = await Bun.build({
    entrypoints: [entrypoint],
    target: 'browser',
    format: 'esm',
    minify: productionMinify,
    outdir: `${minifyOutputDirectory}/minified`,
  })

  expect(unminified.success).toBe(true)
  expect(minified.success).toBe(true)
  const unminifiedBytes = new Uint8Array(await Bun.file(`${minifyOutputDirectory}/unminified/entry.js`).arrayBuffer())
  const minifiedFile = Bun.file(`${minifyOutputDirectory}/minified/entry.js`)
  const minifiedBytes = new Uint8Array(await minifiedFile.arrayBuffer())
  const minifiedText = await minifiedFile.text()
  const codeWithoutLicense = minifiedText.replace(/\/\*![\s\S]*?\*\//g, '').trimEnd()

  expect(minifiedBytes.byteLength).toBeLessThan(unminifiedBytes.byteLength)
  expect(Bun.gzipSync(minifiedBytes).byteLength).toBeLessThan(Bun.gzipSync(unminifiedBytes).byteLength)
  expect(minifiedText).toContain('/*! @license whitespace-only fixture */')
  expect(minifiedText).toContain('function preserveReadableIdentifier')
  expect(minifiedText).toContain('const preserveThisConst=')
  expect(minifiedText).toContain('return preserveThisConst')
  expect(codeWithoutLicense).not.toMatch(/\r?\n/)

  const emittedModule = await import(`${pathToFileURL(`${minifyOutputDirectory}/minified/entry.js`).href}?fixture=${Date.now()}`)
  expect(emittedModule.preserveReadableIdentifier(4)).toBe(5)
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

test('prepared production evidence measures emitted output and covers every shipped JavaScript file', async () => {
  const evidencePath = '.tmp/frontend-bundle-evidence.json'
  const evidenceFile = Bun.file(evidencePath)
  expect(await evidenceFile.exists()).toBe(true)
  const evidence = await evidenceFile.json() as FrontendBundleEvidence

  await verifyFrontendBundleFiles(evidence, evidencePath)

  const shippedJavaScript: string[] = []
  for await (const path of new Bun.Glob('static/**/*.{js,mjs}').scan({ cwd: '.', dot: true, onlyFiles: true })) {
    shippedJavaScript.push(path.replace(/^static\//, ''))
  }
  shippedJavaScript.sort()
  expect(evidence.aggregate.files).toEqual(shippedJavaScript)
  expect(Object.keys(evidence.entries)).toHaveLength(20)
  expect(evidence.aggregate.rawBytes).toBeGreaterThan(0)
  expect(evidence.aggregate.gzipBytes).toBeGreaterThan(0)
})
