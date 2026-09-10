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
    if (url.pathname === '/transport') {
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
  if (!server?.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error && (error as NodeJS.ErrnoException).code !== 'ERR_SERVER_NOT_RUNNING' ? reject(error) : resolve())
    server.closeIdleConnections()
  })
}, 15_000)

test('bundled Datastar isolates suggestions from a delayed run and reaches Stop', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 800 } })
  let releaseRun = () => {}
  let releaseFirstSuggestion = () => {}
  const firstSuggestionFinished = new Promise<void>((resolve) => { releaseFirstSuggestion = resolve })
  let runSeen = false
  let runAborted = false
  let stopSeen = false
  let suggestionCount = 0
  let firstSuggestionSeq = 0
  let firstSuggestionAborted = false
  page.on('requestfailed', (request) => {
    if (new URL(request.url()).pathname !== '/explore/command' || request.method() !== 'POST') return
    const command = request.postDataJSON()?.dataExplorerCommand
    if (command?.action === 'run') runAborted = true
    if (command?.explore?.filterSuggestions?.suggestionRequestSeq === firstSuggestionSeq) firstSuggestionAborted = true
  })
  try {
    await page.route('**/explore/command', async (route) => {
      const command = (route.request().postDataJSON() as any)?.dataExplorerCommand
      if (command?.explore?.filterSuggestions) {
        suggestionCount += 1
        if (suggestionCount === 1) {
          firstSuggestionSeq = command.explore.filterSuggestions.suggestionRequestSeq
          await firstSuggestionFinished
          return
        }
        releaseFirstSuggestion()
        const suggestion = command.explore.filterSuggestions
        await route.fulfill({
          status: 200,
          contentType: 'text/event-stream',
          body: `event: datastar-patch-signals\ndata: signals ${JSON.stringify({ dataExplorer: { explore: { filterSuggestions: {
            error: null, field: 'orders.status', loading: false, requestSeq: command.explore.requestSeq, suggestionRequestSeq: suggestion.suggestionRequestSeq,
            stale: false, truncated: false, type: null, values: [{ label: 'paid', value: { kind: 'string', value: 'paid' } }],
          } } } })}\n\n`,
        })
        return
      }
      if (command?.action === 'run') {
        runSeen = true
        await new Promise<void>((resolve) => { releaseRun = resolve })
        await route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' })
        return
      }
      if (command?.action === 'stop') {
        stopSeen = true
        await route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' })
        releaseRun()
        return
      }
      await route.fulfill({ status: 200, contentType: 'text/event-stream', body: '' })
    })
    await page.goto(`${baseURL}/transport`)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const object = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      }
      const exploreCommand = {
        action: 'configure',
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 },
        requestSeq: 1, resetVersion: 1, columnWidths: {},
      }
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [object], selectedKey: object.key, selectedObject: object,
          command: { mode: 'explore', objectKey: object.key, clientId: 'transport-client', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 1, resetVersion: 1, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand },
          explore: {
            command: exploreCommand,
            semanticModels: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }] }],
            datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }],
            fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true }],
            selectedSemanticModel: { id: 'sales', title: 'Sales', datasets: [] }, selectedDataset: { id: 'orders', title: 'Orders', fieldCount: 1, entities: [] },
            result: { columns: [{ key: 'status', label: 'Status' }], rows: [{ status: 'paid' }], rowsReturned: 1, durationMs: 1, requestSeq: 1, truncated: false, warnings: [] },
            status: { loading: false, stale: false, requestSeq: 1, state: 'success' },
            filterSuggestions: { error: 'old suggestion error', field: 'orders.status', loading: false, requestSeq: 1, suggestionRequestSeq: 0, stale: false, truncated: false, type: 'string', values: [] },
          },
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} }, warnings: [],
        },
      })
    })
    const explorer = page.locator('lv-data-explorer')
    await explorer.getByRole('button', { name: 'Run', exact: true }).click()
    await waitFor(() => runSeen)
    const controls = explorer.locator('lv-data-explorer-query-controls')
    await controls.getByRole('button', { name: 'Filter Status', exact: true }).click()
    await waitFor(() => suggestionCount === 1)
    const filterInput = controls.locator('.filter-editor label:nth-child(3) input')
    await filterInput.fill('p')
    await waitFor(() => suggestionCount === 2)
    await waitFor(() => firstSuggestionAborted)
    expect(runAborted).toBe(false)
    await waitFor(() => page.evaluate(() => {
      const root = (window as any).LeapViewDataExplorerTransport
      return root !== undefined
    }))
    let observed: any
    await waitFor(async () => {
      observed = await page.evaluate(async () => {
        const { getPath } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
        const suggestion = getPath('dataExplorer.explore.filterSuggestions')
        const result = getPath('dataExplorer.explore.result')
        const status = getPath('dataExplorer.explore.status')
        return { error: suggestion?.error, values: suggestion?.values, result, status }
      })
      return observed?.error === '' && observed?.values?.[0]?.label === 'paid' && observed?.result?.rows?.[0]?.status === 'paid' && observed?.status?.state === 'success'
    })
    expect(observed).toMatchObject({ error: '', values: [{ label: 'paid' }], result: { rows: [{ status: 'paid' }] }, status: { state: 'success' } })
    await explorer.getByRole('button', { name: 'Stop', exact: true }).click()
    await waitFor(() => stopSeen)
  } finally {
    releaseFirstSuggestion()
    releaseRun()
    await page.unroute('**/explore/command').catch(() => {})
    await page.close()
  }
})

function testDocument(): string {
  return `<!doctype html><html><head><style>html,body{margin:0;min-height:100%}lv-data-explorer{display:block;min-height:720px}</style></head><body><main data-signals="{}" data-on:lv-data-explorer-command="$dataExplorerCommand = evt.detail; @post('/explore/command', {requestCancellation: window.LeapViewDataExplorerTransport.requestCancellation(evt.detail)})"><lv-data-explorer></lv-data-explorer></main><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script><script type="module" src="/data-explorer-under-test.js"></script></body></html>`
}

async function waitFor(condition: () => boolean | Promise<boolean>, timeout = 5000): Promise<void> {
  const deadline = Date.now() + timeout
  while (!(await condition())) {
    if (Date.now() >= deadline) throw new Error('timed out waiting for browser transport condition')
    await new Promise((resolve) => setTimeout(resolve, 25))
  }
}
