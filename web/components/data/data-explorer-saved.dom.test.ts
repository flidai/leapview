import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/data-explorer-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : root
    const file = normalize(join(fileRoot, url.pathname))
    if (!file.startsWith(fileRoot)) {
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
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('saved explorations render in their own row and emit the canonical current spec', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const state = await page.evaluate(async () => {
      const spec = { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 }
      const command = {
        spec, semanticModelId: 'sales', datasetId: 'orders', dimensions: ['orders.status'], metrics: [], filters: [], sort: [],
        limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {},
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [], command: { mode: 'explore', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: command },
          explore: { command, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] } }, warnings: [],
        },
        savedExplorations: { enabled: true, list: { items: [], includeArchived: false }, command: { action: 'create' }, save: { state: 'saved' } },
      })
      const element = document.createElement('lv-data-explorer') as any
      const commands: any[] = []
      element.addEventListener('lv-saved-exploration-command', (event: CustomEvent) => commands.push(event.detail))
      document.body.append(element)
      for (let index = 0; index < 20 && !element.shadowRoot?.querySelector('.saved-explorations'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const root = element.shadowRoot as ShadowRoot
      const name = root.querySelector<HTMLInputElement>('input[aria-label="Saved exploration name"]')!
      name.value = 'Orders by status'
      name.dispatchEvent(new Event('input', { bubbles: true }))
      root.querySelector<HTMLButtonElement>('.saved-exploration-actions button')!.click()
      await element.updateComplete
      return {
        routeClasses: root.querySelector('.route')!.className,
        gridRows: getComputedStyle(root.querySelector('.route')!).gridTemplateRows,
        command: commands[0],
      }
    })
    expect(state.routeClasses).toContain('saved-enabled')
    expect(state.gridRows.split(' ').length).toBeGreaterThanOrEqual(3)
    expect(state.command).toMatchObject({ action: 'create', title: 'Orders by status', visibility: 'private', spec: { modelId: 'sales', datasetId: 'orders' } })
  } finally {
    await page.close()
  }
})

function testDocument() {
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { font-family: Inter, system-ui, sans-serif; }
          lv-data-explorer { display: block; min-height: 720px; }
        </style>
      </head>
      <body>
        <main data-signals="{}"></main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/data-explorer-under-test.js"></script>
      </body>
    </html>
  `
}
