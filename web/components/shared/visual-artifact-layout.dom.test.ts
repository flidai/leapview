import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let browser: Browser
let baseURL = ''
const bundleRoot = join(process.cwd(), '.tmp/chat-thread-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const path = (request.url ?? '/').split('?')[0]
    if (path === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const file = normalize(join(bundleRoot, path))
    if (!file.startsWith(bundleRoot)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(file))
    } catch {
      response.writeHead(404)
      response.end('not found')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('layout test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  if (!server?.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error ? reject(error) : resolve())
    server.closeIdleConnections()
  })
}, 15_000)

test('visual artifacts fill fixed cards with or without explore actions', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-visual-artifact'))
    await page.waitForFunction(() => Array.from(document.querySelectorAll('lv-visual-artifact')).filter((element) => element.shadowRoot?.querySelector('.content')).length === 4)
    const state = await page.evaluate(() => Object.fromEntries(
      ['chart-action', 'chart-no-action', 'table-action', 'table-no-action'].map((id) => {
        const host = document.querySelector(`#${id}`) as HTMLElement
        const root = host.shadowRoot!
        const card = root.querySelector('.artifact') as HTMLElement
        const content = root.querySelector('.content') as HTMLElement
        const action = root.querySelector('.explore-action') as HTMLAnchorElement | null
        return [id, {
          cardHeight: card.getBoundingClientRect().height,
          contentHeight: content.getBoundingClientRect().height,
          contentBottom: content.getBoundingClientRect().bottom,
          cardBottom: card.getBoundingClientRect().bottom,
          overflow: card.scrollHeight > card.clientHeight,
          actionHref: action?.getAttribute('href') ?? null,
        }]
      }),
    )) as Record<string, { cardHeight: number; contentHeight: number; contentBottom: number; cardBottom: number; overflow: boolean; actionHref: string | null }>

    for (const id of Object.keys(state)) {
      expect(state[id].cardHeight).toBe(320)
      expect(state[id].contentHeight).toBeGreaterThan(280)
      expect(state[id].contentBottom).toBeLessThanOrEqual(state[id].cardBottom + 1)
      expect(state[id].overflow).toBe(false)
    }
    expect(state['chart-action'].actionHref).toContain('/explore?')
    expect(state['chart-action'].actionHref).toContain('returnSurface=chat')
    expect(state['chart-action'].actionHref).toContain('returnConversation=conversation%3Alayout')
    expect(state['table-action'].actionHref).toContain('/explore?')
    expect(state['chart-no-action'].actionHref).toBeNull()
    expect(state['table-no-action'].actionHref).toBeNull()
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  return `<!doctype html><html><head><style>
    :root {
      --lv-chart-surface: rgb(1, 2, 3);
      --lv-border-default: 2px solid rgb(4, 5, 6);
      --lv-bg-panel: white;
      --lv-fg-accent: blue;
      --lv-line-muted: gray;
      --lv-radius-default: 6px;
      --lv-space-sm: 8px;
      --lv-type-body: 14px system-ui;
    }
    body { margin: 0; }
    #cards { display: grid; width: 720px; grid-template-columns: 1fr 1fr; gap: 12px; }
    lv-visual-artifact { display: block; width: 350px; height: 320px; }
  </style></head><body><main id="cards">
    <lv-visual-artifact id="chart-action" type="bar"></lv-visual-artifact>
    <lv-visual-artifact id="chart-no-action" type="bar"></lv-visual-artifact>
    <lv-visual-artifact id="table-action" type="table"></lv-visual-artifact>
    <lv-visual-artifact id="table-no-action" type="table"></lv-visual-artifact>
  </main><script type="module" src="/chat-under-test.js"></script><script type="module">
    customElements.whenDefined('lv-visual-artifact').then(() => {
      const field = (id, role, dataType, label) => ({ id, role, dataType, nullable: false, label })
      const chart = {
        schemaVersion: 4, visualID: 'layout-chart', rendererID: 'echarts', specRevision: 'sha256:layout-chart', dataRevision: 1,
        spec: { kind: 'cartesian', mark: 'bar', title: 'Orders', datasets: [{ id: 'primary', fields: [field('label', 'dimension', 'string', 'Label'), field('value', 'metric', 'decimal', 'Value')] }], dataBudget: { maxRows: 1, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Orders' }, interactions: [], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } },
        dataState: { kind: 'inline', specRevision: 'sha256:layout-chart', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:layout-chart', dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['a', 1]], completeness: 'complete' }] },
        selection: [], status: { kind: 'ready' }, diagnostics: [],
      }
      const table = {
        schemaVersion: 4, visualID: 'layout-table', rendererID: 'tanstack', specRevision: 'sha256:layout-table', dataRevision: 1,
        spec: { kind: 'table', title: 'Orders', datasets: [{ id: 'primary', fields: [field('label', 'identity', 'string', 'Label')] }], dataBudget: { maxRows: 1, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Orders' }, interactions: [], columns: [{ field: { dataset: 'primary', field: 'label' }, label: 'Label' }], defaultSort: [], presentation: { rowHeight: 34, striped: true, showHeader: true } },
        dataState: { kind: 'windowed', specRevision: 'sha256:layout-table', dataRevision: 1, generation: 1, schema: { id: 'primary', fields: [field('label', 'identity', 'string', 'Label')] }, cardinality: { kind: 'exact', count: 1 }, availableRows: 1, rowCap: 1, chunkSize: 1, resetVersion: 0, sort: [], blocks: { a: { id: 'a', start: 0, rows: [['a']], requestSeq: 0, resetVersion: 0, sort: [] } } },
        selection: [], status: { kind: 'ready' }, diagnostics: [],
      }
      const spec = { schemaVersion: 1, modelId: 'semantic:sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 1 }
      const set = (id, payload, withAction) => { const element = document.querySelector('#' + id); element.payload = payload; if (withAction) { element.exploration = spec; element.conversationId = 'conversation:layout' } }
      set('chart-action', chart, true); set('chart-no-action', chart, false); set('table-action', table, true); set('table-no-action', table, false)
    })
  </script></body></html>`
}
