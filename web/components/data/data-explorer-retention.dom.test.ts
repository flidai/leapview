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

test('selecting a field in another semantic model clears the previous model time', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const command = await page.evaluate(() => {
      const explorer = document.createElement('lv-data-explorer') as any
      explorer.embedded = true
      const time = { field: 'orders.created_at', grain: 'month' }
      const spec = { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, time }
      const current = { spec, semanticModelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {}, time }
      const explore = { command: current, fields: [] }
      Object.defineProperty(explorer, 'dataExplorer', { value: {
        selectedObject: { key: 'orders', semanticModelId: 'sales' }, explore,
      } })
      explorer.toggleUnifiedField(
        { id: 'stock.available', kind: 'metric', datasetId: 'stock', compatible: true },
        { key: 'stock', semanticModelId: 'inventory', datasetId: 'stock', columns: [{ key: 'status' }] },
        explore,
        true,
      )
      cancelAnimationFrame(explorer.exploreFrame)
      return explorer.optimisticExplore
    })
    expect(command.spec.modelId).toBe('inventory')
    expect(command.spec.datasetId).toBe('stock')
    expect(command.spec.metrics).toEqual([{ field: 'stock.available' }])
    expect(command.spec.time).toBeUndefined()
    expect(command.time).toBeUndefined()
  } finally { await page.close() }
})

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
        durationMs: error ? 0 : 4, requestSeq, truncated: false, warnings: [], error, sql: error ? '' : 'select status from orders',
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
          actions: Array.from(element.shadowRoot?.querySelectorAll('.query-actions .text-button:not([aria-controls="semantic-filter-dock"])') ?? []).map((button: any) => button.textContent?.replace(/\s+/g, ' ').trim() ?? ''),
          execution: element.shadowRoot?.querySelector('.execution-state')?.textContent?.replace(/\s+/g, ' ').trim() ?? '',
          columns: element.shadowRoot?.querySelector('.header-columns summary')?.textContent?.replace(/\s+/g, ' ').trim() ?? '',
          views: Array.from(element.shadowRoot?.querySelectorAll('[aria-label="Result views"] button') ?? []).map((button: any) => button.textContent?.trim()),
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
      update({ ...command, action: 'configure', filterSuggestions: { field: 'orders.status', suggestionRequestSeq: 1 } },
        { ...result(1), columns: [], rows: [], rowsReturned: 0 }, status(1, 'stale'))
      const suggestions = await settle()
      if (!suggestions.table.includes('delivered') || !suggestions.columns.includes('1/1') || !suggestions.views.includes('SQL / Details')) {
        throw new Error(`Suggestions cleared the unchanged result controls: ${JSON.stringify(suggestions)}`)
      }
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
    expect(state.draft.views).not.toContain('SQL / Details')
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
    expect(state.acknowledged.execution).toBe('Query edited · run to update results')
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

test('Data Explorer reveals a newly hydrated selection and preserves manual sidebar collapse', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 800 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))

    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const orders = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model',
        semanticModelId: 'sales', datasetId: 'orders', title: 'Sales Orders', columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      }
      const customers = {
        ...orders, key: 'model:model:sales.customers', resourceId: 'model:sales.customers',
        datasetId: 'customers', title: 'Customers', columns: [{ key: 'name', label: 'Name', type: 'string' }],
      }
      const filter = {
        field: 'orders.status', datasetId: 'orders',
        expression: { kind: 'comparison', operator: 'equals', value: { kind: 'string', value: 'shipped' } },
      }
      const spec = {
        schemaVersion: 1, modelId: 'sales', datasetId: 'orders',
        dimensions: [{ field: 'orders.status' }], metrics: [], filters: [filter], sort: [], limit: 100,
      }
      const exploreCommand = {
        spec, semanticModelId: 'sales', datasetId: 'orders', dimensions: ['orders.status'], metrics: [],
        filters: [filter], sort: [], limit: 100, requestSeq: 1, resetVersion: 1, columnWidths: {},
      }
      const dataExplorer = {
        objects: [orders, customers], selectedKey: orders.key, selectedObject: orders,
        preview: {
          columns: orders.columns, totalRows: 1, availableRows: 1, chunkSize: 100, rowHeight: 32,
          resetVersion: 1, blocks: { first: { start: 0, requestSeq: 1, resetVersion: 1, sort: {}, rows: [{ status: 'shipped' }] } },
          totalRowLabel: '1', sort: {}, sql: '', error: '', loading: false, stale: false,
        },
        command: {
          mode: 'browse', objectKey: orders.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100,
          requestSeq: 1, resetVersion: 1, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand,
        },
        explore: {
          command: exploreCommand,
          semanticModels: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }, { id: 'customers', title: 'Customers', fieldCount: 1, entities: [] }] }],
          datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }],
          fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', compatible: true, selected: false }],
          result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] },
          status: { loading: false, stale: false, requestSeq: 0, state: 'idle' },
        },
        warnings: [],
      }
      mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] }, dataExplorer })
      const element = document.createElement('lv-data-explorer') as any
      document.body.append(element)
      const settle = async () => {
        for (let index = 0; index < 6; index += 1) {
          await element.updateComplete
          await new Promise((resolve) => requestAnimationFrame(resolve))
        }
      }
      await settle()
      const root = element.shadowRoot as ShadowRoot
      const selected = root.querySelector<HTMLElement>('.object-button.is-selected')!
      const objectNode = selected.closest<HTMLDetailsElement>('.object-node')!
      const group = selected.closest<HTMLDetailsElement>('.resource-group')!
      const initiallyRevealed = { groupOpen: group.open, objectOpen: objectNode.open }

      objectNode.open = false
      await new Promise((resolve) => setTimeout(resolve, 30))
      mergePatch({ dataExplorer: { preview: { totalRows: 2 } } })
      await settle()
      const objectRemainsCollapsed = !objectNode.open

      group.open = false
      await new Promise((resolve) => setTimeout(resolve, 30))
      mergePatch({ dataExplorer: { warnings: ['unrelated update'] } })
      await settle()
      const groupRemainsCollapsed = !group.open
      return { initiallyRevealed, objectRemainsCollapsed, groupRemainsCollapsed }
    })

    expect(state.initiallyRevealed).toEqual({ groupOpen: true, objectOpen: true })
    expect(state.objectRemainsCollapsed).toBe(true)
    expect(state.groupRemainsCollapsed).toBe(true)
  } finally {
    await page.close()
  }
})


test('SQL line wrapping can be enabled and disabled without changing the query', async () => {
  const page = await browser.newPage({ viewport: { width: 500, height: 700 } })
  const sql = `SELECT '${'a long SQL value '.repeat(40)}' AS description FROM orders`
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer-sql'))
    await page.evaluate((query) => {
      const element = document.createElement('lv-data-explorer-sql') as any
      element.sql = query
      document.body.append(element)
    }, sql)
    const viewer = page.locator('lv-data-explorer-sql')
    await viewer.getByRole('button', { name: 'Format SQL', exact: true }).click()
    await viewer.locator('lv-code-block').evaluate(async (element) => { await (element as any).updateComplete })
    const code = viewer.locator('pre')
    await code.waitFor()
    const dimensions = () => code.evaluate((element) => ({
      whiteSpace: getComputedStyle(element).whiteSpace,
      height: element.scrollHeight,
      width: element.clientWidth,
      contentWidth: element.scrollWidth,
      text: element.textContent,
    }))
    const unwrapped = await dimensions()
    expect(unwrapped.whiteSpace).toBe('pre')
    expect(unwrapped.contentWidth).toBeGreaterThan(unwrapped.width)
    const wrap = viewer.getByRole('button', { name: 'Wrap SQL lines', exact: true })
    await wrap.click()
    expect(await wrap.getAttribute('aria-pressed')).toBe('true')
    const wrapped = await dimensions()
    expect(wrapped.whiteSpace).toBe('pre-wrap')
    expect(wrapped.height).toBeGreaterThan(unwrapped.height)
    expect(wrapped.contentWidth).toBeLessThanOrEqual(wrapped.width + 1)
    expect(wrapped.text).toBe(sql)
    await wrap.click()
    expect(await wrap.getAttribute('aria-pressed')).toBe('false')
    const restored = await dimensions()
    expect(restored.whiteSpace).toBe('pre')
    expect(restored.contentWidth).toBeGreaterThan(restored.width)
    expect(restored.height).toBe(unwrapped.height)
    expect(restored.text).toBe(sql)
  } finally { await page.close() }
})

test('filter suggestions support keyboard and pointer selection before Apply', async () => {
  const page = await browser.newPage({ viewport: { width: 500, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer-query-controls'))
    await page.evaluate(() => {
      const element = document.createElement('lv-data-explorer-query-controls') as any
      element.filterEditorOnly = true
      element.filterField = 'orders.quantity'
      element.fields = [{ id: 'orders.quantity', label: 'Quantity', kind: 'dimension', datasetId: 'orders', type: 'integer', compatible: true, selected: false }]
      element.suggestionRequestSeq = 1
      element.suggestions = {
        field: 'orders.quantity', requestSeq: 0, suggestionRequestSeq: 1, loading: false, stale: false, truncated: false,
        values: [{ label: 'Ten items', value: { kind: 'integer', value: '10' } }, { label: 'Twenty items', value: { kind: 'integer', value: '20' } }],
      }
      ;(window as any).filterChanges = []
      element.addEventListener('lv-data-explorer-filter-change', (event: Event) => (window as any).filterChanges.push((event as CustomEvent).detail))
      document.body.append(element)
    })
    const editor = page.locator('lv-data-explorer-query-controls')
    const value = editor.getByRole('combobox', { name: 'Value', exact: true })
    await value.click()
    expect(await value.getAttribute('aria-expanded')).toBe('true')
    await value.press('ArrowDown')
    expect(await editor.getByRole('option', { name: 'Ten items' }).getAttribute('aria-selected')).toBe('true')
    await value.press('ArrowDown')
    await value.press('Enter')
    expect(await value.inputValue()).toBe('20')
    expect(await value.getAttribute('aria-expanded')).toBe('false')
    expect(await page.evaluate(() => (window as any).filterChanges)).toEqual([{ action: 'value', value: '20' }])
    await value.click()
    await editor.getByRole('option', { name: 'Ten items' }).click()
    expect(await value.inputValue()).toBe('10')
    expect(await value.getAttribute('aria-expanded')).toBe('false')
    await editor.getByRole('button', { name: 'Apply', exact: true }).click()
    expect(await page.evaluate(() => (window as any).filterChanges)).toEqual([
      { action: 'value', value: '20' }, { action: 'value', value: '10' }, { action: 'apply' },
    ])
  } finally { await page.close() }
})

test('query controls hydrate canonical select values on their first render', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer-query-controls'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer-query-controls') as any
      const spec = {
        schemaVersion: 1,
        modelId: 'sales',
        datasetId: 'orders',
        dimensions: [{ field: 'orders.status' }],
        metrics: [{ field: 'orders.net_total' }],
        filters: [],
        time: {
          field: 'orders.purchase_date',
          grain: 'day',
          range: {
            kind: 'absolute',
            lower: { value: { kind: 'date', value: '2026-01-01' }, inclusive: true },
            upper: { value: { kind: 'date', value: '2026-01-31' }, inclusive: true },
          },
        },
        sort: [{ field: 'orders.purchase_date', direction: 'desc' }],
        limit: 100,
      }
      element.command = {
        spec,
        semanticModelId: 'sales',
        datasetId: 'orders',
        dimensions: ['orders.status'],
        metrics: ['orders.net_total'],
        filters: [],
        sort: [{ field: 'orders.purchase_date', direction: 'desc' }],
        limit: 100,
        requestSeq: 0,
        resetVersion: 0,
        columnWidths: {},
      }
      element.fields = [
        { id: 'orders.created_at', label: 'Created at', kind: 'dimension', datasetId: 'orders', type: 'timestamp', compatible: true, selected: false },
        { id: 'orders.purchase_date', label: 'Purchase date', kind: 'dimension', datasetId: 'orders', type: 'date', compatible: true, selected: false },
        { id: 'shipments.created_at', label: 'Shipment created at', kind: 'dimension', datasetId: 'shipments', type: 'timestamp', compatible: false, selected: false },
        { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true },
        { id: 'orders.net_total', label: 'Net total', kind: 'metric', datasetId: 'orders', type: 'decimal', compatible: true, selected: true },
      ]
      element.filterField = 'orders.net_total'
      element.filterOperator = 'greater_than'
      element.filterValue = '10'
      element.filtersOnly = true
      document.body.append(element)
      await element.updateComplete

      const root = element.shadowRoot as ShadowRoot
      const value = (selector: string) => root.querySelector<HTMLSelectElement>(selector)?.value
      return {
        timeField: value('[aria-label="Time field"]'),
        timeGrain: value('[aria-label="Time grain"]'),
        timeRange: value('[aria-label="Time range"]'),
        rowLimit: value('[aria-label="Row limit"]'),
        sortField: value('[aria-label="Sort field 1"]'),
        sortDirection: value('[aria-label="Sort direction 1"]'),
        filterOperator: value('.filter-editor select'),
        rangeFrom: root.querySelector<HTMLInputElement>('[aria-label="Time range from"]')?.value,
        rangeTo: root.querySelector<HTMLInputElement>('[aria-label="Time range to"]')?.value,
        unavailableTimeDisabled: root.querySelector<HTMLOptionElement>('option[value="shipments.created_at"]')?.disabled, progressiveDisclosure: { columnsOpen: root.querySelector<HTMLDetailsElement>('.field-picker')?.open, moreOpen: root.querySelector<HTMLDetailsElement>('.query-config')?.open, exclusiveGroups: Array.from(root.querySelectorAll<HTMLDetailsElement>('.field-group')).every((group) => group.name === 'data-explorer-field-group') },
        optionsSummary: root.querySelector('.query-config summary')?.textContent?.replace(/\s+/g, ' ').trim(),
      }
    })

    expect(state).toEqual({
      timeField: 'orders.purchase_date',
      timeGrain: 'day',
      timeRange: 'absolute',
      rowLimit: undefined,
      sortField: 'orders.purchase_date',
      sortDirection: 'desc',
      filterOperator: 'greater_than',
      rangeFrom: '2026-01-01',
      rangeTo: '2026-01-31',
      unavailableTimeDisabled: true, progressiveDisclosure: { columnsOpen: undefined, moreOpen: false, exclusiveGroups: true },
      optionsSummary: 'Time & sort Purchase date · 1 sort',
    })
  } finally {
    await page.close()
  }
})

function testDocument() {
  return `<!doctype html><html><head><style>html,body{margin:0;min-height:100%}lv-data-explorer{display:block;min-height:720px}</style></head><body><main data-signals="{}"></main><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script><script type="module" src="/data-explorer-under-test.js"></script></body></html>`
}

test('semantic table exposes exact totals and serializes windows beyond the old cap', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explore-table'))
    const state = await page.evaluate(async () => {
      const table = document.createElement('lv-data-explore-table') as any
      const sort = { column: 'status', direction: 'asc' }
      table.command = { spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [{ field: 'orders.status', direction: 'asc' }], limit: 1000 }, requestSeq: 5, resetVersion: 2 }
      const block = { start: 0, requestSeq: 5, resetVersion: 2, sort, rows: [{ status: 'paid' }] }
      table.result = { columns: [{ key: 'status', label: 'Status' }], rows: block.rows, rowsReturned: 1, requestSeq: 5, durationMs: 0, truncated: true, warnings: [], window: { columns: [], totalRows: 2345, availableRows: 2345, totalRowLabel: '2345', chunkSize: 100, rowHeight: 32, resetVersion: 2, sort, blocks: { a: block }, loading: false, stale: false } }
      const events: any[] = []
      table.addEventListener('lv-data-explore-table-window', (event: CustomEvent) => events.push(event.detail))
      document.body.append(table)
      await table.updateComplete
      const inner = table.shadowRoot.querySelector('lv-windowed-table') as any
      const total = inner.table.totalRows
      const request = (id: string, start: number, requestSeq: number) => inner.dispatchEvent(new CustomEvent('lv-windowed-table-request', { detail: { block: id, start, count: 100, requestSeq, resetVersion: 2, sort }, bubbles: true, composed: true }))
      request('a', 1500, 11)
      request('b', 1600, 12)
      const before = events.length
      table.result = { ...table.result, requestSeq: 6, window: { ...table.result.window, blocks: { a: { ...block, start: 1500, requestSeq: 11 } } } }
      await table.updateComplete
      const after = events.map(event => event.window)
      table.remove()
      return { total, before, after }
    })
    expect(state.total).toBe(2345)
    expect(state.before).toBe(1)
    expect(state.after.map(window => window.start)).toEqual([1500, 1600])
    expect(state.after.map(window => window.requestSeq)).toEqual([11, 12])
  } finally { await page.close() }
})
