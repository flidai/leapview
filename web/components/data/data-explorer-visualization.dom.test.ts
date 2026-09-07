import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { currentVisualizationSchemaVersion } from '../../generated/visualization/schema-version'

function explorerTableEnvelope(): VisualizationEnvelope {
  const specRevision = `sha256:${'1'.repeat(64)}`
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
    if (url.pathname === '/' || url.pathname === '/explore') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(url.pathname === '/explore'))
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

test('data explorer builds a governed semantic visualization and drill route', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-explorer-results') && customElements.get('lv-visualization-host'))

    const state = await page.evaluate(async ({ tableEnvelope }) => {
      const element = document.createElement('lv-data-explorer') as any
      const pageSignal = {
        kind: 'data', title: 'Data Explorer', description: 'Inspect or explore data.', tabs: [],
      }
      const exploreCommand = {
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [{ field: 'revenue' }],
          filters: [], time: { field: 'orders.created_at', grain: 'month', alias: 'period', range: {
            kind: 'absolute', lower: { value: { kind: 'date', value: '2026-01-01' }, inclusive: true },
            upper: { value: { kind: 'date', value: '2026-04-01' }, inclusive: false },
          } }, sort: [{ field: 'revenue', direction: 'desc' }], limit: 100 },
        requestSeq: 1, resetVersion: 1, columnWidths: {},
      }
      const selectedObject = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'orders',
        description: 'One row per order.', grain: 'order_id', columnCount: 3, rowCountLabel: '10',
        columns: [
          { key: 'order_id', label: 'Order ID', type: 'string' },
          { key: 'status', label: 'Status', type: 'string' },
          { key: 'order_status', label: 'Order status', type: 'string' },
          { key: 'created_at', label: 'Created at', type: 'date' },
        ],
      }
      const customersObject = {
        key: 'model:model:sales.customers', resourceId: 'model:sales.customers', layer: 'model', semanticModelId: 'sales', datasetId: 'customers', title: 'customers',
        columnCount: 2, rowCountLabel: '10', columns: [
          { key: 'customer_id', label: 'Customer ID', type: 'string' },
          { key: 'state', label: 'State', type: 'string' },
        ],
      }
      const itemsObject = {
        key: 'model:model:sales.items', resourceId: 'model:sales.items', layer: 'model', semanticModelId: 'sales', datasetId: 'items', title: 'items',
        columnCount: 1, rowCountLabel: '10', columns: [{ key: 'sku', label: 'SKU', type: 'string' }],
      }
      const dataExplorer = {
        objects: [selectedObject, customersObject, itemsObject], selectedKey: selectedObject.key, selectedObject, preview: {
          columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, totalRowLabel: 'Unknown', sort: {},
        },
        command: { mode: 'explore', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand },
        explore: {
          command: exploreCommand,
          views: { table: tableEnvelope }, recommendedView: 'table', defaultView: 'table',
          semanticModels: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 3, entities: [] }] }],
          datasets: [{ id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 3, entities: [] }],
          selectedSemanticModel: { id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 3, entities: [] }] },
          selectedDataset: { id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 3, entities: [] },
          fields: [
            { id: 'orders.order_id', label: 'Order ID', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: false },
            { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true },
            { id: 'order_status', label: 'Order status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: false },
            { id: 'orders.created_at', label: 'Created at', kind: 'dimension', datasetId: 'orders', type: 'date', compatible: true, selected: false },
            { id: 'customers.customer_id', label: 'Customer ID', kind: 'dimension', datasetId: 'customers', type: 'string', compatible: true, relationshipPath: ['orders_customers'], selected: false },
            { id: 'customers.state', label: 'State', kind: 'dimension', datasetId: 'customers', type: 'string', compatible: true, relationshipPath: ['orders_customers'], selected: false },
            { id: 'items.sku', label: 'SKU', kind: 'dimension', datasetId: 'items', type: 'string', compatible: false, compatibilityReason: 'Not available from Orders because no grain-preserving relationship path reaches Items.', selected: false },
            { id: 'revenue', label: 'Revenue', kind: 'metric', datasetId: 'orders', type: 'sum', compatible: true, selected: true },
          ],
          result: {
            columns: [{ key: 'status', label: 'Status' }, { key: 'revenue', label: 'Revenue', type: 'decimal' }],
            rows: [{ status: 'delivered', revenue: 1200 }], rowsReturned: 1, durationMs: 8, requestSeq: 1,
            sql: 'SELECT status, SUM(revenue)', plan: 'orders aggregate', truncated: false, warnings: [],
          },
          status: { loading: false, stale: false, requestSeq: 1, state: 'success' },
        }, warnings: [],
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: pageSignal, dataExplorer })
      const commands: any[] = []
      element.addEventListener('lv-data-explorer-command', (event: CustomEvent) => commands.push(event.detail))
      document.body.append(element)
      for (let index = 0; index < 20 && !element.shadowRoot?.querySelector('.field-button'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }

      const root = element.shadowRoot
      const customersTable = Array.from(root.querySelectorAll<HTMLElement>('.object-button')).find((button) => button.textContent?.includes('customers'))!
      customersTable.click()
      await element.updateComplete
      const tableSelectionCommand = commands.at(-1)?.explore
      const orderID = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('Order ID'))
      if (!orderID) throw new Error(`Order ID field was not rendered: ${root.textContent}`)
      orderID.click()
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 380))

      const statusRow = Array.from(root.querySelectorAll<HTMLElement>('.column-item')).find((row) => row.textContent?.includes('Status'))
      const filterButton = statusRow?.querySelector<HTMLButtonElement>('.field-action')
      if (!filterButton) throw new Error(`Status filter button was not rendered: ${root.textContent}`)
      filterButton.click()
      await element.updateComplete
      const filterControls = root.querySelector('lv-data-explorer-query-controls') as any
      await filterControls.updateComplete
      const filterRoot = filterControls.shadowRoot!
      const filterInput = filterRoot.querySelector<HTMLInputElement>('.filter-editor label:nth-child(3) input')!
      filterInput.value = 'delivered'
      filterInput.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const updatedFilterControls = root.querySelector('lv-data-explorer-query-controls') as any
      await updatedFilterControls.updateComplete
      const updatedFilterRoot = updatedFilterControls.shadowRoot!
      const applyButton = Array.from(updatedFilterRoot.querySelectorAll<HTMLButtonElement>('.filter-editor .text-button')).find((button) => button.textContent?.trim() === 'Apply')
      if (!applyButton) throw new Error(`Apply filter button was not rendered: ${updatedFilterRoot.textContent}`)
      applyButton.click()
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 380))
      const physicalFilter = commands.at(-1)?.explore?.spec?.filters?.at(-1)

      const semanticRow = Array.from(root.querySelectorAll<HTMLElement>('.column-item')).find((row) => row.textContent?.includes('Order status'))
      const semanticFilterButton = semanticRow?.querySelector<HTMLButtonElement>('.field-action')
      if (!semanticFilterButton) throw new Error(`Conformed dimension filter button was not rendered: ${root.textContent}`)
      semanticFilterButton.click()
      await element.updateComplete
      const semanticFilterControls = root.querySelector('lv-data-explorer-query-controls') as any
      await semanticFilterControls.updateComplete
      const semanticFilterRoot = semanticFilterControls.shadowRoot!
      const semanticFilterInput = semanticFilterRoot.querySelector<HTMLInputElement>('.filter-editor label:nth-child(3) input')!
      semanticFilterInput.value = 'paid'
      semanticFilterInput.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const updatedSemanticControls = root.querySelector('lv-data-explorer-query-controls') as any
      await updatedSemanticControls.updateComplete
      const updatedSemanticRoot = updatedSemanticControls.shadowRoot!
      const semanticApplyButton = Array.from(updatedSemanticRoot.querySelectorAll<HTMLButtonElement>('.filter-editor .text-button')).find((button) => button.textContent?.trim() === 'Apply')
      if (!semanticApplyButton) throw new Error(`Apply conformed filter button was not rendered: ${updatedSemanticRoot.textContent}`)
      semanticApplyButton.click()
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 380))
      const semanticFilter = commands.at(-1)?.explore?.spec?.filters?.find((filter: any) => filter.field === 'order_status')

      const results = root.querySelector('lv-data-explorer-results') as any
      await results.updateComplete
      const host = results.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement
      if (!host) throw new Error(`shared visualization host was not rendered: ${results.shadowRoot?.textContent}`)
      const stateField = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('State'))!
      const skuField = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('SKU'))!
      const initialState = {
        modes: Array.from(root.querySelectorAll('.mode-button')).map((button) => ({ text: button.textContent?.trim(), pressed: button.getAttribute('aria-pressed') })),
        hasBreadcrumb: Boolean(root.querySelector('[aria-label="Breadcrumb"]')),
        resourceTables: root.querySelector('.resource-group')?.textContent?.replace(/\s+/g, ' ').trim(),
        chips: Array.from(root.querySelectorAll('.selection-shelf .chip')).map((chip) => chip.textContent?.replace(/\s+/g, ' ').trim()),
        grain: results.shadowRoot?.querySelector('[data-result-grain]')?.textContent?.replace(/\s+/g, ' ').trim(),
        resultRows: results.result.rows,
        resultView: results.shadowRoot?.querySelector('[role="tab"][aria-selected="true"]')?.textContent?.trim(),
        hasResultsSurface: Boolean(results),
        hasSharedHost: Boolean(host),
        timeSelection: exploreCommand.spec.time,
        relatedField: { disabled: stateField.disabled, text: stateField.textContent?.replace(/\s+/g, ' ').trim(), title: stateField.title },
      }
      const commandCountBeforeWindow = commands.length
      const windowRequest = {
        visualID: 'explore-table',
        specRevision: tableEnvelope.specRevision,
        dataRevision: tableEnvelope.dataRevision,
        requestSeq: 2,
        resetVersion: 0,
        start: 0,
        limit: 50,
        blockID: 'a',
        sort: [{ field: { dataset: 'primary', field: 'status' }, direction: 'descending' }],
      }
      host.dispatchEvent(new CustomEvent('lv-visualization-window-request', {
        bubbles: true,
        composed: true,
        detail: windowRequest,
      }))
      await element.updateComplete
      const windowCommand = commands.slice(commandCountBeforeWindow).find((candidate) => candidate.action === 'run')
      const commandCountAfterValidWindow = commands.length
      const invalidWindowMessages: string[] = []
      for (const detail of [
        { ...windowRequest, visualID: 'old-table' },
        { ...windowRequest, specRevision: `sha256:${'f'.repeat(64)}` },
        { ...windowRequest, dataRevision: 2 },
        { ...windowRequest, requestSeq: 0 },
        { ...windowRequest, resetVersion: 2 },
        { ...windowRequest, blockID: ' ' },
      ]) {
        host.dispatchEvent(new CustomEvent('lv-visualization-window-request', { bubbles: true, composed: true, detail }))
        await element.updateComplete
        invalidWindowMessages.push(root.querySelector<HTMLElement>('.result-error[role="alert"]')?.textContent?.replace(/\s+/g, ' ').trim() ?? '')
      }
      const invalidWindowCommandCount = commands.length - commandCountAfterValidWindow
      const interaction = {
        sourceKind: 'visual', sourceId: 'explore-table', interactionKind: 'selection',
        action: 'set', toggle: false,
        mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered', label: 'Delivered' }],
      }
      const commandCountBeforeExploreFromHere = commands.length
      host.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: interaction }))
      await results.updateComplete
      const exploreFromHereButton = Array.from(results.shadowRoot?.querySelectorAll<HTMLButtonElement>('button') ?? [])
        .find((button) => button.textContent?.includes('Explore from here'))
      if (!exploreFromHereButton) throw new Error('explore-from-here action was not rendered')
      exploreFromHereButton.click()
      await element.updateComplete
      const exploreFromHereCommand = commands.slice(commandCountBeforeExploreFromHere)
        .find((candidate) => candidate.action === 'configure' && candidate.explore?.action === 'configure')

      const refreshedResults = root.querySelector('lv-data-explorer-results') as any
      await refreshedResults.updateComplete
      const refreshedHost = refreshedResults.shadowRoot?.querySelector('lv-visualization-host') as HTMLElement
      const commandCountBeforeDrill = commands.length
      refreshedHost.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: interaction }))
      await refreshedResults.updateComplete
      const drillButton = Array.from(refreshedResults.shadowRoot?.querySelectorAll<HTMLButtonElement>('button') ?? [])
        .find((button) => button.textContent?.includes('Drill to rows'))
      if (!drillButton) throw new Error('drill-to-rows action was not rendered')
      drillButton.click()
      await element.updateComplete
      const drillCommand = commands.slice(commandCountBeforeDrill)
        .find((candidate) => candidate.action === 'run' && candidate.explore?.action === 'run')
      skuField.click()
      await results.updateComplete
      const unavailableField = { disabled: skuField.disabled, pressed: skuField.getAttribute('aria-pressed'), text: skuField.textContent?.replace(/\s+/g, ' ').trim(), title: skuField.title }

      const customerCommand = {
        ...exploreCommand, spec: { ...exploreCommand.spec, datasetId: 'customers', dimensions: [{ field: 'customers.state' }], metrics: [], sort: [] }, requestSeq: 100, resetVersion: 100,
      }
      const customerExplorer = {
        ...dataExplorer,
        selectedKey: customersObject.key,
        selectedObject: customersObject,
        command: { ...dataExplorer.command, objectKey: customersObject.key, explore: customerCommand },
        explore: {
          ...dataExplorer.explore,
          command: customerCommand,
          views: null, recommendedView: 'table', defaultView: 'table',
          selectedDataset: { id: 'customers', title: 'Customers', grainEntity: 'customer_id', grainFields: ['customer_id'], fieldCount: 1, entities: [] },
          fields: [
            { id: 'orders.order_id', label: 'Order ID', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: false, rebaseDatasetId: 'orders', compatibilityReason: 'Select Order ID and change grain from Customers to Orders.', selected: false },
            { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: false, rebaseDatasetId: 'orders', compatibilityReason: 'Select Status and change grain from Customers to Orders.', selected: false },
            { id: 'customers.state', label: 'State', kind: 'dimension', datasetId: 'customers', type: 'string', compatible: true, selected: true },
            { id: 'items.sku', label: 'SKU', kind: 'dimension', datasetId: 'items', type: 'string', compatible: false, compatibilityReason: 'No safe base supports this field with the selection.', selected: false },
          ],
          result: { columns: [{ key: 'state', label: 'State' }], rows: [{ state: 'SP' }], rowsReturned: 1, durationMs: 2, requestSeq: 100, truncated: false, warnings: [] },
        },
      }
      mergePatch({ dataExplorer: customerExplorer })
      for (let index = 0; index < 10; index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const rebaseField = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('Status'))!
      rebaseField.click()
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 380))
      const rebaseCommand = commands.at(-1)?.explore
      return {
        ...initialState,
        physicalFilter,
        semanticFilter,
        unavailableField,
        exploreFromHereCommand,
        drillCommand,
        rebaseField: { disabled: rebaseField.disabled, text: rebaseField.textContent?.replace(/\s+/g, ' ').trim(), title: rebaseField.title },
        rebaseCommand,
        tableSelectionCommand,
        windowCommand,
        invalidWindowMessages,
        invalidWindowCommandCount,
        commands,
      }
    }, { tableEnvelope: explorerTableEnvelope() })

    expect(state.modes).toEqual([
      { text: 'Rows', pressed: 'false' },
      { text: 'Analyze', pressed: 'true' },
    ])
    expect(state.hasBreadcrumb).toBe(false)
    expect(state.resourceTables).toContain('orders')
    expect(state.chips.join(' ')).toContain('Order ID')
    expect(state.chips.join(' ')).toContain('Revenue')
    expect(state.grain).toContain('Grain: order_id')
    expect(state.resultRows).toEqual([{ status: 'delivered', revenue: 1200 }])
    expect(state.resultView).toBe('Table')
    expect(state.hasResultsSurface).toBe(true)
    expect(state.hasSharedHost).toBe(true)
    expect(state.physicalFilter).toMatchObject({ field: 'orders.status', datasetId: 'orders' })
    expect(state.semanticFilter).toMatchObject({ field: 'order_status' })
    expect(state.semanticFilter).not.toHaveProperty('datasetId')
    expect(state.relatedField.disabled).toBe(false)
    expect(state.relatedField.text).toContain('related')
    expect(state.relatedField.title).toContain('orders_customers')
    expect(state.unavailableField.disabled).toBe(true)
    expect(state.unavailableField.pressed).toBe('false')
    expect(state.unavailableField.text).toContain('unavailable')
    expect(state.unavailableField.title).toContain('no grain-preserving relationship path')
    expect(state.rebaseField.disabled).toBe(false)
    expect(state.rebaseField.text).toContain('changes grain')
    expect(state.rebaseField.title).toContain('change grain from Customers to Orders')
    expect(state.rebaseCommand.spec.datasetId).toBe('customers')
    expect(state.rebaseCommand.spec.dimensions.map((field: any) => field.field)).toEqual(['customers.state', 'orders.status'])
    expect(state.tableSelectionCommand.spec.datasetId).toBe('customers')
    expect(state.tableSelectionCommand.spec.dimensions.map((field: any) => field.field)).toEqual(['customers.customer_id', 'customers.state'])
    expect(state.tableSelectionCommand.spec.metrics).toEqual([])
    expect(state.windowCommand.action).toBe('run')
    expect(state.windowCommand.explore.action).toBe('run')
    expect(state.windowCommand.explore.spec.sort).toEqual([{ field: 'orders.status', direction: 'desc' }])
    expect(state.invalidWindowCommandCount).toBe(0)
    expect(state.invalidWindowMessages).toEqual([
      expect.stringContaining('visual ID'),
      expect.stringContaining('spec revision'),
      expect.stringContaining('data revision'),
      expect.stringContaining('request sequence'),
      expect.stringContaining('reset version'),
      expect.stringContaining('block ID'),
    ])
    expect(state.exploreFromHereCommand.action).toBe('configure')
    expect(state.exploreFromHereCommand.explore.action).toBe('configure')
    expect(state.exploreFromHereCommand.explore.spec.filters).toContainEqual(expect.objectContaining({ field: 'orders.status', datasetId: 'orders' }))
    expect(state.drillCommand.action).toBe('run')
    expect(state.drillCommand.explore.action).toBe('run')
    expect(state.drillCommand.explore.spec.visualization.kind).toBe('table')
    expect(state.drillCommand.explore.spec.dimensions).toEqual([{ field: 'orders.order_id' }])
    expect(state.drillCommand.explore.spec.metrics).toEqual([])
    expect(state.drillCommand.explore.spec.filters).toContainEqual(expect.objectContaining({ field: 'orders.status', datasetId: 'orders' }))
    expect(state.drillCommand.explore.spec.time).toEqual(state.timeSelection)
    expect(state.commands.some((command) => command.explore?.spec?.dimensions?.some((field: any) => field.field === 'items.sku'))).toBe(false)
    expect(state.commands.some((command) => command.mode === 'explore' && command.explore?.spec?.dimensions?.some((field: any) => field.field === 'orders.order_id'))).toBe(true)
    expect(state.commands.some((command) => command.explore?.spec?.filters?.some((filter: any) => filter.field === 'orders.status' && filter.expression?.value?.value === 'delivered'))).toBe(true)
  } finally {
    await page.close()
  }
})

function testDocument(withExplorer = false) {
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
        ${withExplorer ? '<lv-data-explorer></lv-data-explorer>' : ''}
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/data-explorer-under-test.js"></script>
      </body>
    </html>
  `
}
