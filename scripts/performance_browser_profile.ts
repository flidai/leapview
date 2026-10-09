import { chromium, type CDPSession, type Page } from '@playwright/test'
import { mkdir, writeFile } from 'node:fs/promises'
import { createHash } from 'node:crypto'
import { gzipSync, brotliCompressSync, constants } from 'node:zlib'
import { join } from 'node:path'
import { ensureDashboardVisualizationsMounted } from './dashboard_visualization_readiness'

const routes = [
  { name: 'dense', path: '/dashboards/dashboard:visual-showcase/pages/overview' },
  { name: 'tables', path: '/dashboards/dashboard:visual-showcase/pages/tables' },
  { name: 'maps', path: '/dashboards/dashboard:visual-showcase/pages/chart-map' },
]
const mapIDs = ['customer_point_map', 'customer_revenue_heat_map', 'customer_density_map']
const durationMetrics = ['ScriptDuration', 'LayoutDuration', 'RecalcStyleDuration', 'TaskDuration']
const gaugeMetrics = ['JSHeapUsedSize', 'JSHeapTotalSize', 'Nodes', 'Documents', 'JSEventListeners']

export function payloadProfile(bytes: Uint8Array) {
  if (!bytes.byteLength) throw new Error('empty route payload')
  return { rawBytes: bytes.byteLength, sha256: createHash('sha256').update(bytes).digest('hex'),
    offlineGzipBytes: gzipSync(bytes, { level: 6 }).byteLength,
    offlineBrotliBytes: brotliCompressSync(bytes, { params: { [constants.BROTLI_PARAM_QUALITY]: 5 } }).byteLength,
    compression: 'deterministic offline gzip6/Brotli5 of decoded HTML; actual wire compression is reported separately' }
}

export function traceProfile(events: Array<{ name?: string, ph?: string, dur?: number }>) {
  const categories = new Map<string, { count: number, durationMicroseconds: number }>()
  for (const event of events) {
    if (event.ph !== 'X' || !event.name || !/parse|compile|layout|paint|render/i.test(event.name)) continue
    if (typeof event.dur !== 'number' || !Number.isFinite(event.dur) || event.dur < 0) throw new Error('invalid trace duration')
    const row = categories.get(event.name) ?? { count: 0, durationMicroseconds: 0 }
    row.count++; row.durationMicroseconds += event.dur; categories.set(event.name, row)
  }
  return { events: Object.fromEntries([...categories].sort(([left], [right]) => left.localeCompare(right))),
    limitation: 'instrumented trace categories may overlap and span threads; their sums are not navigation wall time or user p95' }
}

async function metrics(client: CDPSession) {
  const result = await client.send('Performance.getMetrics')
  const all = new Map(result.metrics.map((row: { name: string, value: number }) => [row.name, row.value]))
  return Object.fromEntries([...durationMetrics, ...gaugeMetrics].map(name => [name, all.get(name) ?? null]))
}

async function trace(client: CDPSession, file: string) {
  const complete = new Promise<{ stream?: string }>(resolve => client.once('Tracing.tracingComplete', resolve))
  await client.send('Tracing.end')
  const { stream } = await complete
  if (!stream) throw new Error('browser trace stream missing')
  let bytes = Buffer.alloc(0)
  try {
    while (true) {
      const chunk = await client.send('IO.read', { handle: stream, size: 1 << 20 })
      bytes = Buffer.concat([bytes, Buffer.from(chunk.data, chunk.base64Encoded ? 'base64' : 'utf8')])
      if (bytes.byteLength > 32 << 20) throw new Error('bounded trace exceeds32MiB')
      if (chunk.eof) break
    }
  } finally { await client.send('IO.close', { handle: stream }) }
  await writeFile(file, bytes, { flag: 'wx', mode: 0o600 })
  const parsed = JSON.parse(bytes.toString('utf8'))
  if (!Array.isArray(parsed.traceEvents) || !parsed.traceEvents.length) throw new Error('empty trace')
  return { file, bytes: bytes.byteLength, sha256: createHash('sha256').update(bytes).digest('hex'), ...traceProfile(parsed.traceEvents) }
}

async function idle(page: Page) {
  await page.waitForFunction(() => {
    const dashboard = document.querySelector('lv-dashboard-page') as any
    const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
    return dashboard?.status?.loading === false && hosts.length > 0 && hosts.every(host => host.envelope?.status?.kind === 'ready')
  }, undefined, { timeout: 120_000 })
}

async function tableScroll(page: Page) {
  const tables = page.locator('lv-report-table')
  for (let index = 0; index < await tables.count(); index++) {
    const table = tables.nth(index)
    const possible = await table.evaluate((element: any) => {
      const port = element.shadowRoot?.querySelector('.table-scrollport') as HTMLElement | null
      return Boolean(port && port.clientHeight > 0 && port.scrollHeight > port.clientHeight + 30)
    })
    if (!possible) continue
    await table.scrollIntoViewIfNeeded()
    const result = await table.evaluate(async (element: any) => {
      const port = element.shadowRoot.querySelector('.table-scrollport') as HTMLElement
      const before = { top: port.scrollTop, first: element.visibleRows?.[0]?.index ?? null }
      const positions = [0.25, 0.75, 0.5, 0]
      const observations = []
      for (const fraction of positions) {
        const started = performance.now()
        port.scrollTop = (port.scrollHeight - port.clientHeight) * fraction
        port.dispatchEvent(new Event('scroll'))
        await element.updateComplete
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
        const deadline = performance.now() + 30_000
        while (element.visibleLoading) {
          if (performance.now() > deadline) throw new Error('selected table virtual window did not settle')
          await new Promise(resolve => setTimeout(resolve, 20))
        }
        observations.push({ fraction, scrollTop: port.scrollTop, first: element.visibleRows?.[0]?.index ?? null,
          loaded: element.visibleRows?.filter((row: any) => row.kind === 'row').length ?? 0,
          measuredMs: performance.now() - started })
      }
      return { before, observations }
    })
    if (!result.observations.some(row => row.scrollTop > 30 && row.first !== result.before.first)) throw new Error('table did not scroll its virtual window')
    if (result.observations.some(row => row.loaded < 1)) throw new Error('selected table showed no loaded rows after scrolling')
    return result
  }
  throw new Error('fixed table route has no scrollable production table')
}

export async function runBrowserProfile() {
  const base = Bun.env.LEAPVIEW_BASE_URL
  const output = Bun.env.LEAPVIEW_BROWSER_PROFILE_OUTPUT
  if (!base || !output) throw new Error('explicit admitted fixture URL and output are required')
  await mkdir(output, { recursive: true, mode: 0o700 })
  const browser = await chromium.launch()
  const records: any[] = []
  let failure: string | null = null
  try {
    for (const route of routes) {
      const page = await browser.newPage({ viewport: { width: 1440, height: 960 },
        ...(Bun.env.LEAPVIEW_QA_STORAGE_STATE ? { storageState: Bun.env.LEAPVIEW_QA_STORAGE_STATE } : {}) })
      const client = await page.context().newCDPSession(page)
      const pageErrors: string[] = []
      const streams = new Map<any, boolean>()
      const tiles: Array<{ visual: string, zoom: number, status: number }> = []
      page.on('pageerror', error => pageErrors.push(error.message))
      page.on('request', request => { if (new URL(request.url()).pathname === '/updates') streams.set(request, false) })
      page.on('requestfailed', request => { if (streams.has(request)) streams.set(request, true) })
      page.on('requestfinished', request => { if (streams.has(request)) streams.set(request, true) })
      page.on('response', response => {
        const match = /\/visuals\/([^/]+)\/tiles\/[^/]+\/(\d+)\/\d+\/\d+\.mvt$/.exec(new URL(response.url()).pathname)
        if (match) tiles.push({ visual: match[1]!, zoom: Number(match[2]), status: response.status() })
      })
      try {
        await client.send('Performance.enable')
        await client.send('Tracing.start', { categories: 'devtools.timeline,v8,disabled-by-default-v8.compile,blink.user_timing', transferMode: 'ReturnAsStream' })
        const before = await metrics(client), started = performance.now()
        const response = await page.goto(new URL(route.path, base).toString(), { waitUntil: 'domcontentloaded', timeout: 120_000 })
        if (!response?.ok() || new URL(page.url()).pathname !== route.path) throw new Error('wrong route or failed response')
        const body = await response.body()
        await idle(page)
        await ensureDashboardVisualizationsMounted(page, route.name === 'maps' ? mapIDs : [])
        const routeReadyMs = performance.now() - started
        const mounted = await page.locator('lv-dashboard-page').evaluate((dashboard: any) =>
          Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')).map((host: any) =>
            ({ visual: host.envelope?.visualID, state: host.envelope?.dataState?.kind, status: host.envelope?.status?.kind,
              canvas: Boolean(host.shadowRoot?.querySelector('canvas')) })))
        let scrolling = null
        if (route.name === 'tables') scrolling = await tableScroll(page)
        if (route.name === 'maps') {
          await page.waitForFunction((expected) => {
            const hosts = Array.from(document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
            return expected.every(id => hosts.some(host => host.envelope?.visualID === id && host.envelope?.dataState?.kind === 'spatial_tiled'
              && host.envelope?.status?.kind === 'ready' && Boolean(host.shadowRoot?.querySelector('canvas'))))
          }, mapIDs, { timeout: 120_000 })
          const deadline = performance.now() + 120_000
          while (!mapIDs.every(visual => tiles.some(tile => tile.visual === visual && tile.zoom > 0 && tile.status === 200))) {
            if (performance.now() > deadline) throw new Error('missing ready non-world map tiles')
            await new Promise(resolve => setTimeout(resolve, 100))
          }
        }
        const after = await metrics(client)
        const delivery = await page.evaluate(() => ({ navigation: performance.getEntriesByType('navigation').map(item => item.toJSON()),
          resources: performance.getEntriesByType('resource').map(item => item.toJSON()) }))
        const teardownStart = performance.now()
        await page.goto('about:blank')
        for (let wait = 0; wait < 50 && [...streams.values()].some(closed => !closed); wait++) await new Promise(resolve => setTimeout(resolve, 100))
        if (!streams.size || [...streams.values()].some(closed => !closed)) throw new Error('owned browser SSE requests did not close on teardown')
        if (pageErrors.length) throw new Error('browser correctness errors: ' + pageErrors.join('; '))
        records.push({ route: route.path, name: route.name, html: payloadProfile(body), contentEncoding: response.headers()['content-encoding'] ?? null,
          routeReadyMs, before, after, mounted, scrolling, tiles, delivery, teardown: { measuredMs: performance.now() - teardownStart, streams: streams.size, allClientRequestsClosed: true },
          trace: await trace(client, join(output, route.name + '-trace.json')) })
      } finally { await page.close() }
    }
  } catch (error) { failure = String(error) }
  finally { await browser.close() }
  await writeFile(join(output, 'profile.json'), JSON.stringify({ records, failure,
    limitation: 'instrumented fixed-route profile; offline compressed bytes are not HTTP wire bytes; no p95, throughput, production-capacity or optimization-gain claim; teardown proves client request closure only' }, null, 2), { flag: 'wx', mode: 0o600 })
  if (failure) throw new Error(failure)
}

if (import.meta.main) await runBrowserProfile()
