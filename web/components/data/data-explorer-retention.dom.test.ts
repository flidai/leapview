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

test('Data Explorer retains the last good result through draft and run lifecycle failures', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 800 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-explore-table'))

    const state = await page.evaluate(async () => {
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
        const table = element.shadowRoot?.querySelector('lv-data-explore-table') as any
        await table?.updateComplete
        const grid = table?.shadowRoot?.querySelector('lv-windowed-table') as any
        await grid?.updateComplete
        return {
          table: grid?.shadowRoot?.textContent ?? '',
          failure: element.shadowRoot?.querySelector('.result-failure')?.textContent?.replace(/\s+/g, ' ').trim() ?? '',
          actions: Array.from(element.shadowRoot?.querySelectorAll('.query-actions .text-button') ?? []).map((button: any) => button.textContent?.replace(/\s+/g, ' ').trim() ?? ''),
          execution: element.shadowRoot?.querySelector('.execution-state')?.textContent?.replace(/\s+/g, ' ').trim() ?? '',
        }
      }
      const update = (next: any, nextResult: any, nextStatus: any, selectedObject = orders) => mergePatch({
        dataExplorer: {
          selectedKey: selectedObject.key, selectedObject,
          command: { mode: 'explore', objectKey: selectedObject.key, explore: next },
          explore: { command: next, result: nextResult, status: nextStatus },
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
      ;(element as any).exploreExecutionState = 'running'
      const suggestionDuringRunCommand = {
        ...configured,
        action: 'configure',
        filterSuggestions: { field: 'orders.status', limit: 50, search: '', suggestionRequestSeq: 1 },
      }
      update(suggestionDuringRunCommand, result(3), status(configured.requestSeq, 'stale', 'configuration changed; run the exploration to refresh results'))
      const suggestionDuringRun = await settle()
      update({ ...configured, action: 'stop' }, result(3), status(3, 'cancelled', 'exploration stopped'))
      const stopped = await settle()
      update({ ...configured, action: 'run' }, result(4, 'Query service is unavailable.'), { ...status(4, 'error'), error: 'Query service is unavailable.' })
      const errored = await settle()
      update({ ...configured, action: 'run', requestSeq: 5, resetVersion: 5 }, result(5), status(5, 'success'))
      await settle()
      const commands: any[] = []
      element.addEventListener('lv-data-explorer-command', (event: CustomEvent) => commands.push(event.detail))
      const clientState = (element as any).clientState
      const activeRunID = clientState.nextRunID()
      const runningCommand = { ...configured, action: 'run', requestSeq: 6, resetVersion: 6 }
      ;(element as any).latestExploreRequestSeq = 6
      ;(element as any).exploreExecutionState = 'running'
      ;(element as any).exploreTransportAction = 'run'
      ;(element as any).optimisticExplore = runningCommand
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      const uncertain = await settle()
      ;(element as any).stopExplore(runningCommand)
      const firstStop = commands.at(-1)
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      const failedStop = await settle()
      ;(element as any).stopExplore(runningCommand)
      const retriedStop = commands.at(-1)
      ;(element as any).runExplore(runningCommand)
      const latestRun = commands.at(-1)
      const latestRunCommand = (element as any).optimisticExplore
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      const failedRunLatest = await settle()
      ;(element as any).stopExplore(latestRunCommand)
      const recoveryStopAfterFailedRetry = commands.at(-1)
      ;(element as any).emitExploreSpec({ ...latestRunCommand.spec, limit: 50 }, latestRunCommand)
      const editBeforeConfigure = await settle()
      await new Promise((resolve) => setTimeout(resolve, 360))
      const editDuringConfigure = await settle()
      const configureDraft = (element as any).optimisticExplore
      const firstConfigure = commands.at(-1)
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      const failedConfigure = await settle()
      ;(element as any).stopExplore(configureDraft)
      const recoveryStopForLatestDraft = commands.at(-1)
      const lateResult = { ...result(7), rows: [{ status: 'late old result' }] }
      update(latestRunCommand, lateResult, status(7, 'cancelled', 'exploration stopped'))
      const lateWhileUncertain = await settle()
      const executionAfterOldStatus = (element as any).exploreExecutionState
      const failureAfterOldStatus = Boolean((element as any).exploreTransportFailure)
      const suggestionCommand = {
        ...configureDraft, action: 'configure',
        filterSuggestions: { field: 'orders.status', limit: 50, search: '', suggestionRequestSeq: 1 },
      }
      update(suggestionCommand, result(5), status(configureDraft.requestSeq, 'success'))
      const suggestionWhileUncertain = await settle()
      const runIDAfterSuggestion = clientState.runID()
      const executionAfterSuggestion = (element as any).exploreExecutionState
      const failureAfterSuggestion = Boolean((element as any).exploreTransportFailure)
      const emptyResult = { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: configureDraft.requestSeq, truncated: false, warnings: [], error: '' }
      update({ ...configureDraft, action: 'configure', filterSuggestions: null }, emptyResult, status(configureDraft.requestSeq, 'stale'))
      const acknowledged = await settle()
      const runIDAfterAck = clientState.runID()
      const executionAfterAck = (element as any).exploreExecutionState
      const failureAfterAck = Boolean((element as any).exploreTransportFailure)
      update(latestRunCommand, lateResult, status(7, 'cancelled', 'exploration stopped'))
      const lateOld = await settle()
      const customerCommand = { ...configured, action: 'configure', requestSeq: 9, resetVersion: 9, spec: { ...configured.spec, datasetId: 'customers', dimensions: [{ field: 'customers.state' }] } }
      update(customerCommand, { ...result(9), columns: [], rows: [], rowsReturned: 0 }, status(9, 'stale'), customers)
      const changed = await settle()
      return { initial, draft, loading, suggestionDuringRun, stopped, errored, uncertain, failedStop, firstStop, retriedStop, latestRun, failedRunLatest, recoveryStopAfterFailedRetry, editBeforeConfigure, editDuringConfigure, failedConfigure, recoveryStopForLatestDraft, configureDraft, firstConfigure, lateWhileUncertain, executionAfterOldStatus, failureAfterOldStatus, suggestionWhileUncertain, runIDAfterSuggestion, executionAfterSuggestion, failureAfterSuggestion, acknowledged, runIDAfterAck, executionAfterAck, failureAfterAck, lateOld, changed, activeRunID }
    })

    expect(state.initial.table).toContain('delivered')
    expect(state.draft.table).toContain('delivered')
    expect(state.loading.table).toContain('delivered')
    expect(state.suggestionDuringRun.actions).toEqual(['Stop'])
    expect(state.suggestionDuringRun.execution).toContain('Running exploration')
    expect(state.suggestionDuringRun.execution).not.toContain('configuration changed')
    expect(state.stopped.table).toContain('delivered')
    expect(state.errored.table).toContain('delivered')
    expect(state.errored.failure).toContain('Query service is unavailable.')
    expect(state.uncertain.table).toContain('delivered')
    expect(state.uncertain.actions).toEqual(['Stop', 'Run latest'])
    expect(state.uncertain.execution).toContain('service is temporarily unavailable')
    expect(state.uncertain.execution).toContain('outcome is unknown')
    expect(state.uncertain.execution).not.toContain('run failed')
    expect(state.failedStop.actions).toEqual(['Stop', 'Run latest'])
    expect(state.firstStop.runId).toBeUndefined()
    expect(state.retriedStop.runId).toBeUndefined()
    expect(state.latestRun.runId).not.toBe(state.activeRunID)
    expect(state.failedRunLatest.actions).toEqual(['Stop', 'Run latest'])
    expect(state.recoveryStopAfterFailedRetry.action).toBe('stop')
    expect(state.recoveryStopAfterFailedRetry.runId).toBeUndefined()
    expect(state.editBeforeConfigure.actions).toEqual(['Stop', 'Run latest'])
    expect(state.editBeforeConfigure.table).toContain('delivered')
    expect(state.editDuringConfigure.actions).toEqual(['Stop', 'Run latest'])
    expect(state.editDuringConfigure.table).toContain('delivered')
    expect(state.firstConfigure.explore.action).toBe('configure')
    expect(state.failedConfigure.actions).toEqual(['Stop', 'Run latest'])
    expect(state.failedConfigure.execution).toContain('outcome is unknown')
    expect(state.recoveryStopForLatestDraft.runId).toBeUndefined()
    expect(state.recoveryStopForLatestDraft.explore.requestSeq).toBe(state.configureDraft.requestSeq)
    expect(state.lateWhileUncertain.actions).toEqual(['Stop', 'Run latest'])
    expect(state.executionAfterOldStatus).toBe('uncertain')
    expect(state.failureAfterOldStatus).toBe(true)
    expect(state.suggestionWhileUncertain.actions).toEqual(['Stop', 'Run latest'])
    expect(state.suggestionWhileUncertain.execution).toContain('outcome is unknown')
    expect(state.executionAfterSuggestion).toBe('uncertain')
    expect(state.failureAfterSuggestion).toBe(true)
    expect(state.runIDAfterSuggestion).toBe(state.latestRun.runId)
    expect(state.acknowledged.actions).toEqual(['Run'])
    expect(state.acknowledged.table).toContain('delivered')
    expect(state.acknowledged.execution).toContain('stale')
    expect(state.acknowledged.execution).not.toContain('outcome is unknown')
    expect(state.executionAfterAck).toBe('idle')
    expect(state.failureAfterAck).toBe(false)
    expect(state.runIDAfterAck).toBe('')
    expect(state.lateOld.actions).toEqual(['Run'])
    expect(state.lateOld.table).toContain('delivered')
    expect(state.lateOld.table).not.toContain('late old result')
    expect(state.changed.table).not.toContain('delivered')
  } finally {
    await page.close()
  }
})

function testDocument() {
  return `<!doctype html><html><head><style>html,body{margin:0;min-height:100%}lv-data-explorer{display:block;min-height:720px}</style></head><body><main data-signals="{}"></main><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script><script type="module" src="/data-explorer-under-test.js"></script></body></html>`
}
