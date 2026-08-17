import { chromium, type Browser } from '@playwright/test'
import { createServer, type Server } from 'node:http'
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'

type BridgeVariant = 'legacy' | 'ignition' | 'datastar-lit'
type BenchmarkResult = {
  variant: BridgeVariant
  iterations: number
  warmup: number
  initialMs: number
  updateTotalMs: number
  updateMeanMs: number
  updateP50Ms: number
  updateP95Ms: number
  updateMaxMs: number
  usedJSHeapSize: number | null
  renderedTextLength: number
  jsonParseCalls: number
  jsonParseMs: number
  jsonStringifyCalls: number
  jsonStringifyMs: number
  setAttributeCalls: number
  setAttributeMs: number
  hostAttributeMutations: number
  hostChildListMutations: number
  shadowMutations: number
  litUpdates: number
  longTaskCount: number
  longTaskMs: number
}

const projectRoot = process.cwd()
const outDir = join(projectRoot, '.tmp/datastar-bridge-bench')
const resultPath = join(projectRoot, '.tmp/datastar-bridge-benchmark.json')
const variants: BridgeVariant[] = ['legacy', 'ignition', 'datastar-lit']
const iterations = Number(Bun.env.LEAPVIEW_BRIDGE_BENCH_ITERATIONS ?? 120)
const warmup = Number(Bun.env.LEAPVIEW_BRIDGE_BENCH_WARMUP ?? 20)

await buildBenchmarkBundle()

const server = await startServer()
let browser: Browser | null = null

try {
  browser = await chromium.launch()
  const results: BenchmarkResult[] = []
  for (const variant of variants) {
    results.push(await runVariant(browser, server.baseURL, variant))
  }
  await writeFile(resultPath, `${JSON.stringify(results, null, 2)}\n`)
  printResults(results)
  console.log(`\nraw results: ${relative(resultPath)}`)
} finally {
  await browser?.close()
  await server.close()
}

async function buildBenchmarkBundle(): Promise<void> {
  await rm(outDir, { recursive: true, force: true })
  await mkdir(outDir, { recursive: true })
  const result = await Bun.build({
    entrypoints: ['web/benchmarks/datastar-bridge.ts'],
    target: 'browser',
    format: 'esm',
    external: [datastarRuntimeURL],
    outdir: outDir,
    naming: { entry: 'datastar-bridge-bench.[ext]' },
  })
  for (const log of result.logs) console.error(log)
  if (!result.success) throw new Error('failed to build Datastar bridge benchmark bundle')
}

async function startServer(): Promise<{ baseURL: string; close: () => Promise<void> }> {
  const server: Server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.writeHead(302, { location: '/legacy' })
      response.end()
      return
    }
    if (variants.includes(url.pathname.slice(1) as BridgeVariant)) {
      response.setHeader('content-type', 'text/html')
      response.end(documentForVariant(url.pathname.slice(1) as BridgeVariant))
      return
    }

    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : outDir
    const filePath = normalize(join(fileRoot, url.pathname))
    if (!filePath.startsWith(fileRoot)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', filePath.endsWith('.css') ? 'text/css' : 'text/javascript')
      response.end(await readFile(filePath))
    } catch {
      response.writeHead(404)
      response.end('not found')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('benchmark server did not bind')
  return {
    baseURL: `http://127.0.0.1:${address.port}`,
    close: () => new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve())),
  }
}

async function runVariant(browser: Browser, baseURL: string, variant: BridgeVariant): Promise<BenchmarkResult> {
  const page = await browser.newPage({ viewport: { width: 1366, height: 900 } })
  try {
    await page.goto(`${baseURL}/${variant}`)
    await page.waitForFunction(() => typeof window.runDatastarBridgeBenchmark === 'function')
    const result = await page.evaluate(
      ({ iterations, warmup }) => window.runDatastarBridgeBenchmark?.({ iterations, warmup }),
      { iterations, warmup },
    )
    if (!result) throw new Error(`benchmark returned no result for ${variant}`)
    if (result.renderedTextLength <= 0) throw new Error(`benchmark did not render content for ${variant}`)
    return result
  } finally {
    await page.close()
  }
}

function documentForVariant(variant: BridgeVariant): string {
  const payload = benchmarkSignals()
  const tag = `bench-${variant}-page`
  return `
    <!doctype html>
    <html>
      <head>
        <meta charset="utf-8">
        <title>LeapView Datastar Bridge Benchmark - ${variant}</title>
        <style>
          html, body { margin: 0; min-height: 100%; font-family: system-ui, sans-serif; }
          main { padding: 16px; }
          section { display: block; max-width: 480px; }
          h1 { margin: 0 0 12px; font-size: 20px; }
          dl { display: grid; grid-template-columns: 120px 1fr; gap: 6px 12px; margin: 0; }
          dt { color: #57606a; }
          dd { margin: 0; font-variant-numeric: tabular-nums; }
        </style>
        <script>${instrumentationScript(variant)}</script>
      </head>
      <body>
        <main data-signals="${attr(payload)}">
          ${componentMarkup(variant, payload)}
        </main>
        <script type="module" src="${datastarRuntimeURL}"></script>
        <script type="module" src="/datastar-bridge-bench.js"></script>
      </body>
    </html>
  `
}

function componentMarkup(variant: BridgeVariant, payload: Record<string, unknown>): string {
  if (variant !== 'legacy') return `<bench-${variant}-page></bench-${variant}-page>`
  const attrs = [
    ['page', payload.page],
    ['filtercontract', payload.filterContract],
    ['filterstate', payload.filterState],
    ['filteroptionpages', payload.filterOptionPages],
    ['visuals', payload.visuals],
    ['tables', payload.tables],
    ['status', payload.status],
  ].map(([name, value]) => `${name}="${attr(value)}"`).join('\n            ')
  const mirrors = [
    ['page', '$page'],
    ['filtercontract', '$filterContract'],
    ['filterstate', '$filterState'],
    ['filteroptionpages', '$filterOptionPages'],
    ['visuals', '$visuals'],
    ['tables', '$tables'],
    ['status', '$status'],
  ].map(([name, value]) => `data-attr:${name}="${value}"`).join('\n            ')
  return `<bench-legacy-page
            ${attrs}
            ${mirrors}
          ></bench-legacy-page>`
}

function instrumentationScript(variant: BridgeVariant): string {
  return `
    window.__DATSTAR_BRIDGE_BENCH__ = { variant: ${JSON.stringify(variant)}, bootStart: performance.now() };
    window.__DATSTAR_BRIDGE_BENCH_METRICS__ = {
      jsonParseCalls: 0,
      jsonParseMs: 0,
      jsonStringifyCalls: 0,
      jsonStringifyMs: 0,
      setAttributeCalls: 0,
      setAttributeMs: 0,
      hostAttributeMutations: 0,
      hostChildListMutations: 0,
      shadowMutations: 0,
      litUpdates: 0,
      longTaskCount: 0,
      longTaskMs: 0,
    };
    {
      const parse = JSON.parse;
      JSON.parse = function patchedParse(...args) {
        const start = performance.now();
        try {
          return parse.apply(this, args);
        } finally {
          const metrics = window.__DATSTAR_BRIDGE_BENCH_METRICS__;
          metrics.jsonParseCalls++;
          metrics.jsonParseMs += performance.now() - start;
        }
      };
      const stringify = JSON.stringify;
      JSON.stringify = function patchedStringify(...args) {
        const start = performance.now();
        try {
          return stringify.apply(this, args);
        } finally {
          const metrics = window.__DATSTAR_BRIDGE_BENCH_METRICS__;
          metrics.jsonStringifyCalls++;
          metrics.jsonStringifyMs += performance.now() - start;
        }
      };
      const setAttribute = Element.prototype.setAttribute;
      Element.prototype.setAttribute = function patchedSetAttribute(...args) {
        const start = performance.now();
        try {
          return setAttribute.apply(this, args);
        } finally {
          const metrics = window.__DATSTAR_BRIDGE_BENCH_METRICS__;
          metrics.setAttributeCalls++;
          metrics.setAttributeMs += performance.now() - start;
        }
      };
      if ('PerformanceObserver' in window) {
        try {
          const observer = new PerformanceObserver((list) => {
            for (const entry of list.getEntries()) {
              metrics.longTaskCount++;
              metrics.longTaskMs += entry.duration;
            }
          });
          observer.observe({ type: 'longtask', buffered: true });
        } catch {}
      }
    }
  `
}

function benchmarkSignals(): Record<string, unknown> {
  return {
    page: {
      kind: 'dashboard',
      title: 'Benchmark Dashboard',
      dashboardId: 'benchmark-dashboard',
      pageId: 'overview',
      canvas: { width: 1366, height: 940 },
      grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 },
      pages: [{ id: 'overview', title: 'Overview', href: '/dashboards/benchmark/pages/overview', active: true }],
      components: Array.from({ length: 16 }, (_, index) => ({
        id: `component_${index}`,
        kind: index % 4 === 0 ? 'table' : 'bar_chart',
        visual: `visual_${index % 8}`,
        table: `table_${index % 4}`,
        x: (index % 4) * 300,
        y: Math.floor(index / 4) * 180,
        width: 280,
        height: 160,
      })),
    },
    filterContract: {
      applicationMode: 'immediate',
      definitions: Object.fromEntries(['state', 'category', 'status', 'channel'].map((id) => [id, {
        id,
        label: id[0].toUpperCase() + id.slice(1),
        field: `orders.${id}`,
        valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in', 'not_in'] }],
        options: { kind: 'distinct', limit: 50, values: [] },
        timezone: 'UTC',
        calendar: 'gregorian',
        weekStart: 'monday',
      }])),
      bindings: Object.fromEntries(['state', 'category', 'status', 'channel'].map((id, order) => [`fb_${id}`, {
        key: `fb_${id}`, id, filter: id, scope: 'report',
        default: { kind: 'unfiltered' }, selectionMode: 'multiple', maxSelectedValues: 50,
        readerEditable: true, urlParam: id, urlEncoding: 'typed_v1',
        paneVisible: true, paneOrder: order, targets: [], optionDependencies: [],
      }])),
    },
    filterState: {
      revision: 0,
      appliedControls: Object.fromEntries(['state', 'category', 'status', 'channel'].map((id) => [`fb_${id}`, {
        expression: { kind: 'unfiltered' },
        resolvedExpression: { kind: 'unfiltered' },
      }])),
      draftControls: {},
      dirtyBindings: [],
      defaultsRevision: 'benchmark-v1',
    },
    filterOptionPages: {
      fb_state: { bindingKey: 'fb_state', items: optionList('state', 0, 8) },
      fb_category: { bindingKey: 'fb_category', items: optionList('category', 0, 12) },
      fb_status: { bindingKey: 'fb_status', items: optionList('status', 0, 10) },
      fb_channel: { bindingKey: 'fb_channel', items: optionList('channel', 0, 6) },
    },
    interactionSelections: [],
    spatialSelections: [],
    visuals: Object.fromEntries(Array.from({ length: 8 }, (_, index) => [
      `visual_${index}`,
      {
        version: 3,
        id: `visual_${index}`,
        kind: 'visual',
        shape: 'category_value',
        renderer: 'echarts',
        title: `Benchmark Visual ${index}`,
        interaction: { kind: 'point_selection', toggle: true, mappings: [{ field: 'orders.status', value: 'label' }] },
        dimensions: ['status'],
        metric: 'order_count',
        data: Array.from({ length: 24 }, (_, row) => ({ label: `Bucket ${row}`, value: index * 100 + row })),
      },
    ])),
    tables: Object.fromEntries(Array.from({ length: 4 }, (_, index) => [
      `table_${index}`,
      {
        version: 2,
        kind: 'data_table',
        title: `Benchmark Table ${index}`,
        columns: [
          { key: 'order_id', label: 'Order', width: 180 },
          { key: 'status', label: 'Status', width: 140 },
          { key: 'state', label: 'State', width: 100 },
          { key: 'category', label: 'Category', width: 180 },
        ],
        totalRows: 96,
        resetVersion: 0,
        blocks: {
          a: { start: 0, requestSeq: 0, rows: tableRows(index, 0) },
          b: { start: 32, requestSeq: 0, rows: tableRows(index, 32) },
          c: { start: 64, requestSeq: 0, rows: tableRows(index, 64) },
        },
      },
    ])),
    status: { loading: false, error: '', lastUpdated: 'initial', setupRequired: false },
  }
}

function optionList(prefix: string, iteration: number, count: number): Array<{ value: { kind: 'string'; value: string }; label: string; selected: boolean; available: boolean }> {
  return Array.from({ length: count }, (_, index) => ({
    value: { kind: 'string', value: `${prefix}-${iteration}-${index}` },
    label: `${prefix.toUpperCase()} ${iteration}-${index}`,
    selected: false,
    available: true,
  }))
}

function tableRows(tableIndex: number, start: number): Array<Record<string, unknown>> {
  return Array.from({ length: 32 }, (_, index) => ({
    order_id: `order-${tableIndex}-${start + index}`,
    status: index % 2 === 0 ? 'delivered' : 'shipped',
    state: index % 3 === 0 ? 'SP' : 'RJ',
    category: `category-${index % 6}`,
  }))
}

function attr(value: unknown): string {
  return escapeHTML(JSON.stringify(value))
}

function escapeHTML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}

function printResults(results: BenchmarkResult[]): void {
  console.table(results.map((result) => ({
    variant: result.variant,
    initial_ms: round(result.initialMs),
    update_mean_ms: round(result.updateMeanMs),
    update_p95_ms: round(result.updateP95Ms),
    update_max_ms: round(result.updateMaxMs),
    json_parse_calls: result.jsonParseCalls,
    json_parse_ms: round(result.jsonParseMs),
    json_stringify_calls: result.jsonStringifyCalls,
    json_stringify_ms: round(result.jsonStringifyMs),
    set_attr_calls: result.setAttributeCalls,
    set_attr_ms: round(result.setAttributeMs),
    host_attr_mutations: result.hostAttributeMutations,
    shadow_mutations: result.shadowMutations,
    lit_updates: result.litUpdates,
    long_task_ms: round(result.longTaskMs),
    heap_mb: result.usedJSHeapSize == null ? null : round(result.usedJSHeapSize / 1024 / 1024),
  })))
}

function round(value: number): number {
  return Math.round(value * 1000) / 1000
}

function relative(path: string): string {
  return path.startsWith(projectRoot) ? path.slice(projectRoot.length + 1) : path
}
