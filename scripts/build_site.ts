import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'
import { buildMapLibreWorker } from './build_maplibre_worker'
import { createHash } from 'node:crypto'
import { mkdir } from 'node:fs/promises'

await Bun.$`rm -rf site/static/site-page.js site/static/vega-sandbox.js site/static/chunks site/static/geometry site/static/map-assets site/static/shared site/static/vendor`.quiet()
await Bun.$`mkdir -p site/static/geometry site/static/map-assets site/static/shared/files site/static/vendor/integrations`.quiet()

const mapStyleSource = 'static/map-assets/leapview-streets/style.json'
const mapStyleBytes = new Uint8Array(await Bun.file(mapStyleSource).arrayBuffer())
const mapStyleDigest = createHash('sha256').update(mapStyleBytes).digest('hex')
const mapStyleDirectory = `site/static/map-assets/leapview-streets/styles/${mapStyleDigest}`
await mkdir(mapStyleDirectory, { recursive: true })

const geometryCopies: Promise<number>[] = []
const geometryGlob = new Bun.Glob('static/geometry/*.geojson')
for await (const sourcePath of geometryGlob.scan({ cwd: '.', onlyFiles: true })) {
  const fileName = sourcePath.slice('static/geometry/'.length)
  geometryCopies.push(Bun.write(`site/static/geometry/${fileName}`, Bun.file(sourcePath)))
}
if (geometryCopies.length === 0) throw new Error('no geographic assets found')

const integrationLogoCopies: Promise<number>[] = []
const integrationLogoGlob = new Bun.Glob('static/vendor/integrations/*.svg')
for await (const sourcePath of integrationLogoGlob.scan({ cwd: '.', onlyFiles: true })) {
  const fileName = sourcePath.slice('static/vendor/integrations/'.length)
  integrationLogoCopies.push(Bun.write(`site/static/vendor/integrations/${fileName}`, Bun.file(sourcePath)))
}
if (integrationLogoCopies.length === 0) throw new Error('no integration logo assets found')

const fontCopies: Promise<number>[] = []
const fontGlob = new Bun.Glob('static/files/inter-*.woff2')
for await (const sourcePath of fontGlob.scan({ cwd: '.', onlyFiles: true })) {
  const fileName = sourcePath.slice('static/files/'.length)
  fontCopies.push(Bun.write(`site/static/shared/files/${fileName}`, Bun.file(sourcePath)))
}
if (fontCopies.length === 0) throw new Error('no Inter font assets found')

await Promise.all([
	Bun.write(`${mapStyleDirectory}/style.json`, mapStyleBytes),
  Bun.write('site/static/shared/app.css', Bun.file('static/app.css')),
  Bun.write('site/static/shared/theme.js', Bun.file('static/theme.js')),
  Bun.write('site/static/vendor/datastar-1.0.2.js', Bun.file('static/vendor/datastar-1.0.2.js')),
  Bun.write('site/static/vendor/github-mark.svg', Bun.file('static/vendor/github-mark.svg')),
  Bun.write('site/static/vendor/mcp-mark.svg', Bun.file('static/vendor/mcp-mark.svg')),
  ...geometryCopies,
  ...integrationLogoCopies,
  ...fontCopies,
])

const result = await Bun.build({
  entrypoints: ['site/web/site-page.ts'],
  target: 'browser',
  format: 'esm',
  splitting: true,
  minify: true,
  define: { 'process.env.NODE_ENV': '"production"' },
  external: [datastarRuntimeURL],
  outdir: 'site/static',
  naming: { entry: '[name].[ext]', chunk: 'chunks/[name]-[hash].[ext]' },
})

for (const log of result.logs) {
  console.error(log)
}
if (!result.success) {
  throw new Error('failed to build LeapView site assets')
}
await buildMapLibreWorker('site/static')

const entry = Bun.file('site/static/site-page.js')
if (entry.size >= 250_000) {
  throw new Error(`site entrypoint is ${entry.size} bytes; budget is 250000 bytes`)
}
