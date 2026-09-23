import { expect, test } from 'bun:test'
import { flowCoverTransform, flowFieldSettings, generateFlowLinePoints } from '../site/web/site-flow-field'

test('site entrypoint is a production bundle with lazy feature chunks', async () => {
  const entry = Bun.file('site/static/site-page.js')
  expect(await entry.exists()).toBe(true)
  expect(entry.size).toBeLessThan(250_000)

  const source = await entry.text()
  expect(source).not.toContain('Lit is in dev mode')

  const chunks: string[] = []
  const glob = new Bun.Glob('site/static/chunks/*.js')
  for await (const path of glob.scan({ cwd: '.', onlyFiles: true })) chunks.push(path)
  expect(chunks.length).toBeGreaterThanOrEqual(3)

  const flowChunks = chunks.filter((path) => path.includes('site-flow-background'))
  expect(flowChunks).toHaveLength(1)
  expect(Bun.file(flowChunks[0]).size).toBeLessThan(20_000)
  expect(chunks.some((path) => path.includes('topology-background'))).toBe(false)
})

test('site build does not publish the retired Vega-Lite sandbox', async () => {
  expect(await Bun.file('site/static/vega-sandbox.js').exists()).toBe(false)
})

test('site build publishes content-addressed geographic assets', async () => {
  for (const fileName of ['br-states-ibge.geojson', 'world-countries-natural-earth-110m.geojson']) {
    const source = Bun.file(`static/geometry/${fileName}`)
    const published = Bun.file(`site/static/geometry/${fileName}`)
    expect(await published.exists()).toBe(true)
    expect(await published.arrayBuffer()).toEqual(await source.arrayBuffer())
  }
})

test('site build vendors the GitHub mark used by repository links', async () => {
  const mark = Bun.file('site/static/vendor/github-mark.svg')
  expect(await mark.exists()).toBe(true)
  expect(await mark.text()).toContain('viewBox="0 0 24 24"')
})

test('site build vendors the official MCP mark used by the interface diagram', async () => {
  const mark = Bun.file('site/static/vendor/mcp-mark.svg')
  expect(await mark.exists()).toBe(true)
  const source = await mark.text()
  expect(source).toContain('viewBox="0 0 180 180"')
  expect(source).toContain('stroke="currentColor"')
  expect(source).not.toContain('<script')
  expect(source).not.toContain('xlink:href')
})

test('site build vendors the featured integration logos', async () => {
  for (const name of [
    'postgresql',
    'mysql',
    'sqlite',
    'amazons3',
    'microsoftazure',
    'googlecloudstorage',
    'cloudflare',
    'hetzner',
    'csv',
    'json',
    'apacheparquet',
    'excel',
    'vortex',
    'deltalake',
    'apacheiceberg',
    'lance',
    'ducklake',
  ]) {
    const logo = Bun.file(`site/static/vendor/integrations/${name}.svg`)
    expect(await logo.exists()).toBe(true)
    const source = await logo.text()
    expect(source).toContain('<svg')
    expect(source).toContain('viewBox=')
    expect(source).not.toContain('<script')
    expect(source).not.toContain('xlink:href')
    expect(source).toContain('var(--main-text-secondary-color, #666)')
  }
  expect(await Bun.file('site/static/vendor/integrations/databricks.svg').exists()).toBe(false)
  expect(await Bun.file('site/static/vendor/integrations/microsoftfabric.svg').exists()).toBe(false)
  expect(await Bun.file('site/static/vendor/integrations/text.svg').exists()).toBe(false)
  expect(await Bun.file('site/static/vendor/integrations/blob.svg').exists()).toBe(false)
})

test('site build publishes every Inter subset referenced by the shared stylesheet', async () => {
  const sourceFonts: string[] = []
  const glob = new Bun.Glob('static/files/inter-*.woff2')
  for await (const path of glob.scan({ cwd: '.', onlyFiles: true })) sourceFonts.push(path)

  expect(sourceFonts.length).toBeGreaterThan(0)
  for (const sourcePath of sourceFonts) {
    const fileName = sourcePath.split('/').at(-1)
    expect(await Bun.file(`site/static/shared/files/${fileName}`).exists()).toBe(true)
  }
})

test('homepage flow background is composed only from continuous ribbons', async () => {
  const source = await Bun.file('site/web/site-flow-background.ts').text()

  expect(source).not.toContain('particle')
  expect(source).not.toContain('drawParticles')
  expect(source).not.toContain('setLineDash')
})

test('homepage flow geometry uses a stable reference field with restrained spin motion', () => {
  expect(flowFieldSettings.width).toBe(1720)
  expect(flowFieldSettings.height).toBe(1080)
  expect(flowFieldSettings.lineCount).toBe(24)
  expect(flowFieldSettings.pointCount).toBe(81)
  expect(flowFieldSettings.speed).toBe(0.66)
  expect(flowFieldSettings.rotate).toBe(-28)
  expect(flowFieldSettings.edgeFade).toBe(0.25)

  const initial = generateFlowLinePoints(0, 0)
  const advanced = generateFlowLinePoints(0, 1)
  const unrotated = generateFlowLinePoints(0, 0, { ...flowFieldSettings, rotate: 0 })
  expect(initial).toHaveLength(flowFieldSettings.pointCount)
  expect(unrotated[0]?.x).toBe(0)
  expect(unrotated.at(-1)?.x).toBe(flowFieldSettings.width)
  expect(advanced).not.toEqual(initial)
  expect(Math.max(...initial.map((point, index) => Math.abs(point.y - advanced[index]!.y)))).toBeLessThan(flowFieldSettings.amplitude * 0.75)

  const transform = flowCoverTransform(1920, 1412)
  expect(transform.scale).toBeCloseTo(1412 / flowFieldSettings.height, 5)
  expect(transform.offsetX).toBeLessThan(0)
  expect(transform.offsetY).toBeCloseTo(0, 5)
})

test('homepage product captures contain matching light and dark showcase frames', async () => {
  for (const path of [
    'site/static/product-dashboard-light.png',
    'site/static/product-dashboard-dark.png',
  ]) {
    const file = Bun.file(path)
    expect(await file.exists()).toBe(true)
    expect(file.size).toBeLessThan(300_000)
    const bytes = new Uint8Array(await file.arrayBuffer())
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength)
    expect(view.getUint32(16)).toBe(1440)
    expect(view.getUint32(20)).toBe(900)
  }
  expect(await Bun.file('site/static/product-dashboard.png').exists()).toBe(false)
})
