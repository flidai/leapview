import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, expect as expectLocator, type Browser, type Page } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/data-explorer-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/' || url.pathname === '/explore') {
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
  if (!address || typeof address === 'string') throw new Error('data explorer error test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
}, 15_000)

afterAll(async () => {
  await browser?.close()
  if (!server?.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error && (error as NodeJS.ErrnoException).code !== 'ERR_SERVER_NOT_RUNNING' ? reject(error) : resolve())
    server.closeIdleConnections()
  })
}, 15_000)

async function mountExploreFailure(page: Page, validQuery: boolean) {
  await page.evaluate(async (valid) => {
    const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
    const object = {
      key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
      columns: [{ key: 'status', label: 'Status', type: 'string' }],
    }
    const exploreCommand = {
      action: 'run',
      spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: valid ? [{ field: 'orders.status' }] : [], metrics: [], filters: [], sort: [], limit: 100 },
      requestSeq: 1, resetVersion: 1, columnWidths: {},
    }
    mergePatch({
      page: { kind: 'data', title: 'Data Explorer', tabs: [] },
      dataExplorer: {
        objects: [object], selectedKey: object.key, selectedObject: object,
        preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
        command: { mode: 'explore', objectKey: object.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 1, resetVersion: 1, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand },
        explore: {
          command: exploreCommand, views: {}, recommendedView: 'table', defaultView: 'table',
          semanticModels: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }] }],
          datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }],
          fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: valid }],
          result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 1, truncated: false, warnings: [], error: 'Qualification-injected semantic failure.' },
          status: { loading: false, stale: false, requestSeq: 1, state: 'error', error: 'Qualification-injected semantic failure.' },
        }, warnings: [],
      },
    })
    const explorer = document.createElement('lv-data-explorer') as any
    const commands: any[] = []
    explorer.addEventListener('lv-data-explorer-command', (event: CustomEvent) => commands.push(event.detail))
    document.body.append(explorer)
    ;(window as any).__dataExplorerErrorCommands = commands
    for (let index = 0; index < 20 && !explorer.shadowRoot?.querySelector('.result-failure'); index += 1) {
      await explorer.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))
    }
  }, validQuery)
}

test('semantic error notices stay outside the result body and valid Retry is actionable', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    await mountExploreFailure(page, true)
    const structure = await page.evaluate(() => {
      const root = document.querySelector('lv-data-explorer')!.shadowRoot!
      const result = root.querySelector('.semantic-result')!
      return Array.from(result.children).map((child) => ({
        name: child.className || child.localName,
        hasFailure: Boolean(child.querySelector('.result-failure')),
        hasResults: Boolean(child.querySelector('lv-data-explorer-results')),
      }))
    })
    expect(structure).toEqual([
      { name: 'query-bar', hasFailure: false, hasResults: false },
      { name: 'lv-data-explorer-query-controls', hasFailure: false, hasResults: false },
      { name: 'result-notices', hasFailure: true, hasResults: false },
      { name: 'result-body', hasFailure: false, hasResults: true },
    ])
    const retry = page.locator('lv-data-explorer .result-failure button').filter({ hasText: 'Retry' })
    await expectLocator(retry).toBeVisible()
    await expectLocator(retry).toBeEnabled()
    await retry.click()
    await expectLocator.poll(() => page.evaluate(() => (window as any).__dataExplorerErrorCommands.length)).toBe(1)
  } finally {
    await page.close()
  }
})

test('invalid semantic query errors offer reset without retry or result-body empty state', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    await mountExploreFailure(page, false)
    const failure = page.locator('lv-data-explorer .result-failure')
    await expectLocator(failure).toContainText('Qualification-injected semantic failure.')
    await expectLocator(failure.getByRole('button', { name: 'Retry' })).toHaveCount(0)
    await expectLocator(failure.getByRole('button', { name: 'Reset query' })).toHaveCount(1)
    await expectLocator(page.locator('lv-data-explorer .result-body > p.empty')).toHaveCount(0)
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
