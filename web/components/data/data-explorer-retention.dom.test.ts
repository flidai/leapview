import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { currentVisualizationSchemaVersion } from '../../generated/visualization/schema-version'

function explorerTableEnvelope(): VisualizationEnvelope {
  const specRevision = `sha256:${'2'.repeat(64)}`
  const field = { id: 'status', role: 'dimension', dataType: 'string', nullable: false, label: 'Status' } as const
  const sort = [{ field: { dataset: 'primary', field: 'status' }, direction: 'ascending' }] as const
  return {
    schemaVersion: currentVisualizationSchemaVersion,
    visualID: 'explore-table',
    rendererID: 'tanstack',
    specRevision,
    dataRevision: 1,
    spec: {
      kind: 'table',
      title: 'Orders',
      datasets: [{ id: 'primary', fields: [field] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' },
      accessibility: { title: 'Orders', description: 'Governed Orders result' },
      interactions: [],
      columns: [{ field: { dataset: 'primary', field: 'status' }, label: 'Status', formatting: [] }],
      defaultSort: sort,
      presentation: { rowHeight: 32, striped: false, showHeader: true },
    },
    dataState: {
      kind: 'windowed',
      specRevision,
      dataRevision: 1,
      generation: 1,
      schema: { id: 'primary', fields: [field] },
      cardinality: { kind: 'exact', count: 1 },
      availableRows: 1,
      rowCap: 100,
      chunkSize: 50,
      resetVersion: 0,
      sort,
      blocks: { a: { id: 'a', start: 0, rows: [['delivered']], requestSeq: 1, resetVersion: 0, sort } },
    },
    selection: [],
    highlights: [],
    status: { kind: 'ready' },
    diagnostics: [],
  } as VisualizationEnvelope
}

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
  if (!server?.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error && (error as NodeJS.ErrnoException).code !== 'ERR_SERVER_NOT_RUNNING' ? reject(error) : resolve())
    server.closeIdleConnections()
  })
}, 15_000)

test('Data Explorer retains the last good result through draft and run lifecycle failures', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 800 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-explorer-results') && customElements.get('lv-visualization-host'))

    const state = await page.evaluate(async ({ tableEnvelope }) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const orders = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model',
        semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      }
      const customers = {
        ...orders, key: 'model:model:sales.customers', resourceId: 'model:sales.customers',
        datasetId: 'customers', title: 'Customers', columns: [{ key: 'state', label: 'State', type: 'string' }],
      }
      const command = {
        spec: {
          schemaVersion: 1, modelId: 'sales', datasetId: 'orders',
          dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100,
        },
        requestSeq: 1, resetVersion: 1, columnWidths: {}, action: 'run',
      }
      const result = (requestSeq: number, error = '') => ({
        columns: error ? [] : [{ key: 'status', label: 'Status' }],
        rows: error ? [] : [{ status: 'delivered' }], rowsReturned: error ? 0 : 1,
        durationMs: error ? 0 : 4, requestSeq, truncated: false, warnings: [], error,
      })
      const status = (requestSeq: number, state: string, message = '') => ({
        loading: state === 'loading', stale: state === 'stale', requestSeq, state, error: '', message,
      })
      const dataExplorer = {
        objects: [orders, customers], selectedKey: orders.key, selectedObject: orders,
        preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
        command: { mode: 'explore', objectKey: orders.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 1, resetVersion: 1, sort: {}, visibleColumns: [], columnWidths: {}, explore: command },
        explore: {
          command,
          views: { table: tableEnvelope }, recommendedView: 'table', defaultView: 'table',
          semanticModels: [{ id: 'sales', title: 'Sales', datasets: [
            { id: 'orders', title: 'Orders', fieldCount: 1, entities: [] },
            { id: 'customers', title: 'Customers', fieldCount: 1, entities: [] },
          ] }],
          datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }],
          fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', compatible: true, selected: true }],
          selectedDataset: { id: 'orders', title: 'Orders', fieldCount: 1, entities: [] },
          result: result(1), status: status(1, 'success'),
        },
        warnings: [],
      }
      const element = document.createElement('lv-data-explorer') as any
      mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] }, dataExplorer })
      document.body.append(element)
      const settle = async () => {
        for (let index = 0; index < 4; index += 1) {
          await element.updateComplete
          await new Promise((resolve) => requestAnimationFrame(resolve))
        }
        const results = element.shadowRoot?.querySelector('lv-data-explorer-results') as any
        await results?.updateComplete
        const host = results?.shadowRoot?.querySelector('lv-visualization-host') as any
        await host?.updateComplete
        return {
          rows: results?.result?.rows ?? [],
          hasResultsSurface: Boolean(results),
          hasSharedHost: Boolean(host),
          selectedView: results?.shadowRoot?.querySelector('[role="tab"][aria-selected="true"]')?.textContent?.trim() ?? '',
          failure: element.shadowRoot?.querySelector('.result-failure')?.textContent?.replace(/\s+/g, ' ').trim() ?? '',
        }
      }
      const update = (next: any, nextResult: any, nextStatus: any, selectedObject = orders) => mergePatch({
        dataExplorer: {
          selectedKey: selectedObject.key, selectedObject,
          command: { mode: 'explore', objectKey: selectedObject.key, explore: next },
          // Datastar deep-merges objects. Preserve the current view map for
          // same-context lifecycle updates, but explicitly clear it when the
          // selected dataset changes.
          explore: {
            command: next, result: nextResult, status: nextStatus,
            ...(selectedObject === orders ? {} : { views: null }),
          },
        },
      })
      const initial = await settle()
      const configured = {
        ...command,
        action: 'configure',
        requestSeq: 2,
        resetVersion: 2,
        spec: {
          ...command.spec,
          filters: [{
            field: 'orders.status',
            datasetId: 'orders',
            expression: { kind: 'comparison', operator: 'equals', value: { kind: 'string', value: 'delivered' } },
          }],
        },
      }
      update(configured, result(2), status(2, 'stale'))
      const draft = await settle()
      update({ ...configured, action: 'run' }, result(3), status(3, 'loading', 'Running exploration…'))
      const loading = await settle()
      update({ ...configured, action: 'stop' }, result(3), status(3, 'cancelled', 'exploration stopped'))
      const stopped = await settle()
      update({ ...configured, action: 'run' }, result(4, 'Query service is unavailable.'), { ...status(4, 'error'), error: 'Query service is unavailable.' })
      const errored = await settle()
      update({ ...configured, action: 'run', requestSeq: 5, resetVersion: 5 }, result(5), status(5, 'success'))
      await settle()
      ;(element as any).exploreExecutionState = 'running'
      ;(element as any).optimisticExplore = { ...configured, action: 'run', requestSeq: 6, resetVersion: 6 }
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      const transport = await settle()
      // Stop marks the draft stopped optimistically, so exercise the
      // terminal transport failure while that lifecycle action is in flight.
      ;(element as any).exploreExecutionState = 'stopped'
      ;(element as any).exploreTransportAction = 'stop'
      ;(element as any).optimisticExplore = { ...configured, action: 'stop', requestSeq: 8, resetVersion: 8 }
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      const failedStop = await settle()
      const customerCommand = { ...configured, action: 'configure', requestSeq: 9, resetVersion: 9, spec: { ...configured.spec, datasetId: 'customers', dimensions: [{ field: 'customers.state' }] } }
      update(customerCommand, { ...result(9), columns: [], rows: [], rowsReturned: 0 }, status(9, 'stale'), customers)
      const changed = await settle()
      return { initial, draft, loading, stopped, errored, transport, failedStop, changed, runVisible: Boolean(element.shadowRoot?.querySelector('.query-actions .text-button')?.textContent?.includes('Run')) }
    }, { tableEnvelope: explorerTableEnvelope() })

    expect(state.initial.rows).toEqual([{ status: 'delivered' }])
    expect(state.initial.hasResultsSurface).toBe(true)
    expect(state.initial.hasSharedHost).toBe(true)
    expect(state.initial.selectedView).toBe('Table')
    expect(state.draft.rows).toEqual([{ status: 'delivered' }])
    expect(state.draft.hasSharedHost).toBe(true)
    expect(state.loading.rows).toEqual([{ status: 'delivered' }])
    expect(state.loading.hasSharedHost).toBe(true)
    expect(state.stopped.rows).toEqual([{ status: 'delivered' }])
    expect(state.stopped.hasSharedHost).toBe(true)
    expect(state.errored.rows).toEqual([{ status: 'delivered' }])
    expect(state.errored.hasSharedHost).toBe(true)
    expect(state.errored.failure).toContain('Query service is unavailable.')
    expect(state.transport.rows).toEqual([{ status: 'delivered' }])
    expect(state.transport.hasSharedHost).toBe(true)
    expect(state.transport.failure).toContain('service is temporarily unavailable')
    expect(state.failedStop.failure).toContain('service is temporarily unavailable')
    expect(state.runVisible).toBe(true)
    expect(state.changed.rows).not.toContainEqual({ status: 'delivered' })
    expect(state.changed.hasSharedHost).toBe(false)
  } finally {
    await page.close()
  }
})

function testDocument() {
  return `<!doctype html><html><head><style>html,body{margin:0;min-height:100%}lv-data-explorer{display:block;min-height:720px}</style></head><body><main data-signals="{}"></main><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script><script type="module" src="/data-explorer-under-test.js"></script></body></html>`
}
