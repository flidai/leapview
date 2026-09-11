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
}, 15_000)

afterAll(async () => {
  await browser?.close()
  if (!server?.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error && (error as NodeJS.ErrnoException).code !== 'ERR_SERVER_NOT_RUNNING' ? reject(error) : resolve())
    server.closeIdleConnections()
  })
}, 15_000)
test('data explorer renders object browser and emits preview commands', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-preview-table') && customElements.get('lv-windowed-table'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const pageSignal = {
        kind: 'data',
        title: 'Data Explorer',
        description: 'Inspect rows.',
        selectedObject: 'model:model:olist.orders',
        tabs: [],
      }
      const dataExplorer = {
        objects: [
          {
            key: 'source:source:olist.orders',
            resourceId: 'source:olist.orders',
            layer: 'source',
            source: 'orders',
            title: 'orders source',
            columnCount: 2,
            rowCountLabel: '10',
            columns: [{ key: 'order_id', label: 'order_id', type: 'VARCHAR' }],
          },
          {
            key: 'model:model:olist.regions',
            resourceId: 'model:olist.regions',
            layer: 'model',
            semanticModelId: 'olist',
            datasetId: 'regions',
            title: 'regions',
            columnCount: 1,
            rowCountLabel: '5',
            columns: [{ key: 'region', label: 'region', type: 'VARCHAR' }],
          },
          {
            key: 'model:model:olist.orders',
            resourceId: 'model:olist.orders',
            layer: 'model',
            semanticModelId: 'olist',
            datasetId: 'orders',
            title: 'orders',
            columnCount: 2,
            rowCountLabel: '10',
            columns: [
              { key: 'order_id', label: 'order_id', type: 'VARCHAR' },
              { key: 'status', label: 'status', type: 'VARCHAR' },
            ],
          },
          {
            key: 'source:source:olist.orders',
            resourceId: 'source:olist.orders',
            layer: 'source',
            source: 'orders',
            title: 'orders source',
            columnCount: 2,
            rowCountLabel: '10',
            columns: [{ key: 'order_id', label: 'order_id', type: 'VARCHAR' }],
          },
          {
            key: 'model:model:olist.customers',
            resourceId: 'model:olist.customers',
            layer: 'model',
            semanticModelId: 'olist',
            datasetId: 'customers',
            title: 'customers',
            columnCount: 1,
            rowCountLabel: 'Unknown',
            columns: [{ key: 'status', label: 'Status', type: 'string' }],
          },
        ],
        selectedKey: 'model:model:olist.orders',
        selectedObject: {
          key: 'model:model:olist.orders',
          resourceId: 'model:olist.orders',
          layer: 'model',
          semanticModelId: 'olist',
          datasetId: 'orders',
          title: 'orders',
          description: 'One row per order.',
          grain: 'order_id',
          columnCount: 2,
          rowCountLabel: '10',
          columns: [
            { key: 'order_id', label: 'order_id', type: 'VARCHAR', nullable: false, primaryKey: true, description: 'Stable order identifier.' },
            { key: 'status', label: 'status', type: 'VARCHAR', nullable: true, defaultValue: "'created'" },
          ],
        },
        preview: {
          columns: [
            { key: 'order_id', label: 'order_id', type: 'VARCHAR' },
            { key: 'status', label: 'status', type: 'VARCHAR' },
          ],
          totalRows: 500,
          availableRows: 500,
          chunkSize: 100,
          rowHeight: 32,
          resetVersion: 0,
          blocks: {
            a: { start: 0, requestSeq: 0, resetVersion: 0, sort: {}, rows: [
              { order_id: 'o1', status: 'delivered' },
              { order_id: 'o2', status: 'a very long status value that should truncate inside the cell without changing layout' },
            ] },
            b: { start: 100, requestSeq: 0, resetVersion: 0, sort: {}, rows: [{ order_id: 'o100', status: 'processing' }] },
            c: { start: 200, requestSeq: 0, resetVersion: 0, sort: {}, rows: [] },
          },
          totalRowLabel: '500',
          sort: {},
          sql: 'SELECT * FROM model.orders',
          error: '',
        },
        command: { objectKey: 'model:model:olist.orders', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
        warnings: [],
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: pageSignal, dataExplorer })
      const commands: any[] = []
      element.addEventListener('lv-data-explorer-command', (event: CustomEvent) => commands.push(event.detail))
      document.body.append(element)
      for (let index = 0; index < 20 && !element.shadowRoot?.querySelector('lv-data-preview-table'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const root = element.shadowRoot
      const previewTable = root.querySelector('lv-data-preview-table') as any
      await previewTable.updateComplete
      const grid = previewTable.renderRoot.querySelector('lv-windowed-table') as any
      await grid.updateComplete
      const customers = Array.from(root.querySelectorAll<HTMLButtonElement>('.object-button')).find((button) => button.textContent?.includes('customers'))!
      const customersNode = customers.closest('.object-node') as HTMLDetailsElement
      customers.click()
      const rowClickExpanded = customersNode.open
      ;(customers.querySelector('.object-expand') as HTMLElement).click()
      const expandClickExpanded = customersNode.open
      await element.updateComplete
      await previewTable.updateComplete
      await grid.updateComplete
      const firstHeader = grid.shadowRoot.querySelector('.header-cell button') as HTMLButtonElement
      firstHeader.click()
      const resizer = grid.shadowRoot.querySelector('.column-resizer') as HTMLElement
      resizer.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, clientX: 160 }))
      document.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, clientX: 230 }))
      await new Promise((resolve) => requestAnimationFrame(resolve))
      document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, clientX: 230 }))
      const scrollport = grid.shadowRoot.querySelector('.scrollport') as HTMLDivElement
      scrollport.scrollTop = 9000
      scrollport.dispatchEvent(new Event('scroll'))
      await new Promise((resolve) => setTimeout(resolve, 80))
      const cellRect = grid.shadowRoot.querySelector('.cell')!.getBoundingClientRect()
      const tableRect = grid.shadowRoot.querySelector('.plane')!.getBoundingClientRect()
      const selectedNodeExpandedByDefault = Boolean(root.querySelector('.object-button.is-selected')?.closest('.object-node')?.hasAttribute('open'))
      const searchInput = root.querySelector<HTMLInputElement>('.search input')!
      searchInput.value = 'status'
      searchInput.dispatchEvent(new Event('input', { bubbles: true }))
      await element.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const columnSearchMatches = Array.from(root.querySelectorAll<HTMLDetailsElement>('.object-node[data-column-match="true"]'))
      const resizerControl = root.querySelector<HTMLElement>('.browser-resizer')!
      const widthBeforeKeyboardResize = Number(resizerControl.getAttribute('aria-valuenow'))
      resizerControl.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
      await element.updateComplete
      const widthAfterKeyboardResize = Number(root.querySelector<HTMLElement>('.browser-resizer')?.getAttribute('aria-valuenow'))
      const sidebarToggle = root.querySelector<HTMLButtonElement>('.sidebar-toggle')!
      const tableWidthBeforeCollapse = Math.round(grid.getBoundingClientRect().width)
      const togglePositionBeforeCollapse = sidebarToggle.getBoundingClientRect()
      sidebarToggle.click()
      await element.updateComplete
      const sidebarCollapsed = !root.querySelector('.tree') && sidebarToggle.getAttribute('aria-expanded') === 'false'
      const tableWidthAfterCollapse = Math.round(grid.getBoundingClientRect().width)
      const togglePositionAfterCollapse = sidebarToggle.getBoundingClientRect()
      const collapsedToggleLabel = sidebarToggle.getAttribute('aria-label')
      sidebarToggle.click()
      await element.updateComplete
      const headerColumnCheckboxes = Array.from(root.querySelectorAll<HTMLInputElement>('.header-column-menu input'))
      headerColumnCheckboxes.at(-1)?.click()
      await element.updateComplete
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        groups: Array.from(root.querySelectorAll('summary')).map((item) => item.textContent?.trim()),
        hasBreadcrumb: Boolean(root.querySelector('[aria-label="Breadcrumb"]')),
        hasDescription: Boolean(root.querySelector('.detail')),
        hasSelectedHeader: Boolean(root.querySelector('.selected-header')),
        badgeCount: root.querySelectorAll('.badge').length,
        hasSearch: Boolean(root.querySelector('.search input')),
        searchLabel: root.querySelector('.search input')?.getAttribute('aria-label'),
        tabs: Array.from(root.querySelectorAll('.object-tab')).map((tab) => tab.textContent?.trim()),
        selectedColumns: Array.from(root.querySelector('.object-button.is-selected')?.closest('.object-node')?.querySelectorAll('.column-item .field-button > span:nth-child(3)') ?? []).map((item) => item.textContent?.trim()),
        selectedFieldStates: Array.from(root.querySelector('.object-button.is-selected')?.closest('.object-node')?.querySelectorAll('.column-item .field-button') ?? []).map((item) => item.getAttribute('aria-pressed')),
        selectedNodeText: root.querySelector('.object-button.is-selected .object-label strong')?.textContent?.trim(),
        selectedNodeSubtitle: root.querySelector('.object-button.is-selected .object-label small')?.textContent?.trim(),
        selectedNodeExpandedByDefault,
        rowClickExpanded,
        expandClickExpanded,
        resourceSummaries: Array.from(root.querySelectorAll('.resource-group > summary')).map((item) => item.textContent?.replace(/\s+/g, ' ').trim()),
        resourceIcons: Array.from(root.querySelectorAll('.resource-icon')).map((item) => item.getAttribute('title')),
        columnSearchMatchCount: columnSearchMatches.length,
        columnSearchMatchesOpen: columnSearchMatches.every((node) => node.open),
        hasHeaderColumnsControl: root.querySelector('.header-columns summary')?.textContent?.replace(/\s+/g, ' ').trim(),
        hasPreviewTable: Boolean(previewTable),
        hasWindowedTable: Boolean(grid),
        tableKey: grid.table?.tableKey,
        tableRowHeight: grid.table?.rowHeight,
        tableFooterHeight: Math.round(grid.shadowRoot.querySelector('.footer')!.getBoundingClientRect().height),
        tableFooterDisplay: getComputedStyle(grid.shadowRoot.querySelector('.footer')!).display,
        tableFooterText: grid.shadowRoot.querySelector('.footer')?.textContent?.replace(/\s+/g, ' ').trim(),
        tableToolbarHeight: Math.round(grid.shadowRoot.querySelector('.toolbar')!.getBoundingClientRect().height),
        sidebarCollapsed,
        widthBeforeKeyboardResize,
        widthAfterKeyboardResize,
        tableWidthBeforeCollapse,
        tableWidthAfterCollapse,
        togglePositionBeforeCollapse: { x: Math.round(togglePositionBeforeCollapse.x), y: Math.round(togglePositionBeforeCollapse.y) },
        togglePositionAfterCollapse: { x: Math.round(togglePositionAfterCollapse.x), y: Math.round(togglePositionAfterCollapse.y) },
        collapsedToggleLabel,
        rowCount: grid.shadowRoot.querySelectorAll('.row[role="row"]').length,
        firstCellWidth: Math.round(cellRect.width),
        tableWidth: Math.round(tableRect.width),
        commands,
      }
    })

    expect(state.title).toBe('Data Explorer')
    expect(state.groups.join(' ')).not.toContain('Sources')
    expect(state.groups.join(' ')).toContain('olist')
    expect(state.groups.join(' ')).not.toContain('Models')
    expect(state.hasBreadcrumb).toBe(false)
    expect(state.hasDescription).toBe(false)
    expect(state.hasSelectedHeader).toBe(false)
    expect(state.badgeCount).toBe(0)
    expect(state.hasSearch).toBe(true)
    expect(state.searchLabel).toBe('Search data')
    expect(state.tabs).toEqual([])
    expect(state.selectedColumns).toEqual(['order_id', 'status'])
    expect(state.selectedFieldStates).toEqual(['true', 'true'])
    expect(state.selectedNodeText).not.toContain('olist · orders')
    expect(state.selectedNodeText).toBe('orders')
    expect(state.selectedNodeSubtitle).toBe('orders')
    expect(state.selectedNodeExpandedByDefault).toBe(false)
    expect(state.rowClickExpanded).toBe(false)
    expect(state.expandClickExpanded).toBe(true)
    expect(state.resourceSummaries).toContain('olist (2)')
    expect(state.resourceIcons).toEqual(['Project resource'])
    expect(state.columnSearchMatchCount).toBe(2)
    expect(state.columnSearchMatchesOpen).toBe(true)
    expect(state.hasHeaderColumnsControl).toBe('Columns2/2')
    expect(state.hasPreviewTable).toBe(true)
    expect(state.hasWindowedTable).toBe(true)
    expect(state.tableKey).toBe('model:model:olist.orders')
    expect(state.tableRowHeight).toBe(32)
    expect(state.tableFooterDisplay).toBe('flex')
    expect(state.tableFooterHeight).toBeGreaterThan(0)
    expect(state.tableFooterText).toMatch(/^\d+-\d+ of 500(?: · loading)?$/)
    expect(state.tableToolbarHeight).toBe(0)
    expect(state.sidebarCollapsed).toBe(true)
    expect(state.widthAfterKeyboardResize).toBe(state.widthBeforeKeyboardResize + 16)
    expect(state.tableWidthAfterCollapse).toBeGreaterThan(state.tableWidthBeforeCollapse)
    expect(Math.abs(state.togglePositionAfterCollapse.x - state.togglePositionBeforeCollapse.x)).toBeLessThanOrEqual(8)
    expect(Math.abs(state.togglePositionAfterCollapse.y - state.togglePositionBeforeCollapse.y)).toBeLessThanOrEqual(8)
    expect(state.collapsedToggleLabel).toBe('Open data browser')
    expect(state.rowCount).toBeGreaterThan(0)
    expect(state.tableWidth).toBeGreaterThan(700)
    expect(state.firstCellWidth).toBeGreaterThan(100)
    expect(state.commands.some((command) => command.objectKey === 'model:model:olist.customers')).toBe(true)
    expect(state.commands.some((command) => command.objectKey === 'model:model:olist.customers' && command.visibleColumns?.length === 0 && Object.keys(command.columnWidths ?? {}).length === 0)).toBe(true)
    expect(state.commands.some((command) => command.sort?.column === 'order_id')).toBe(true)
    expect(state.commands.some((command) => command.visibleColumns?.length === 1 && command.visibleColumns[0] === 'order_id')).toBe(true)
    expect(state.commands.some((command) => command.objectKey === 'model:model:olist.orders' && command.columnWidths?.order_id > 200)).toBe(true)
    expect(state.commands.some((command) => command.block && command.start > 0 && command.count === 100 && command.requestSeq > 0)).toBe(true)
  } finally {
    await page.close()
  }
})

test('data explorer distinguishes same-title aliases with dataset subtitles', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))

    const aliases = await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const objects = [
        {
          key: 'model:[12:model:orders][14:semantic:sales][13:order_history]', resourceId: 'model:orders', layer: 'model',
          semanticModelId: 'semantic:sales', datasetId: 'order_history', title: 'Orders', columnCount: 1,
          columns: [{ key: 'status', label: 'Status', type: 'string' }],
        },
        {
          key: 'model:[12:model:orders][14:semantic:sales][6:orders]', resourceId: 'model:orders', layer: 'model',
          semanticModelId: 'semantic:sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
          columns: [{ key: 'status', label: 'Status', type: 'string' }],
        },
      ]
      const exploreCommand = {
        spec: { schemaVersion: 1, modelId: 'semantic:sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 },
        requestSeq: 0, resetVersion: 0, columnWidths: {},
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects, selectedKey: objects[0].key, selectedObject: objects[0],
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
          command: { mode: 'browse', objectKey: objects[0].key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
          explore: {
            command: exploreCommand, semanticModels: [{ id: 'semantic:sales', title: 'Sales', datasets: [] }], datasets: [], fields: [],
            result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] },
          },
          warnings: [],
        },
      })
      document.body.append(element)
      for (let index = 0; index < 10; index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      return Array.from(element.shadowRoot?.querySelectorAll<HTMLElement>('.object-button') ?? []).map((button) => ({
        title: button.querySelector('.object-label strong')?.textContent?.trim(),
        subtitle: button.querySelector('.object-label small')?.textContent?.trim(),
      }))
    })

    expect(aliases).toEqual([
      { title: 'semantic:sales.Orders', subtitle: 'order_history' },
      { title: 'semantic:sales.Orders', subtitle: 'orders' },
    ])
  } finally {
    await page.close()
  }
})

test('data explorer prompts for a selection when objects are available', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))

    const message = await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [{
            key: 'model:model:orders', resourceId: 'model:orders', layer: 'model',
            semanticModelId: 'semantic:sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
            columns: [{ key: 'order_id', label: 'Order ID', type: 'string' }],
          }],
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
          command: { offset: 0, limit: 100, start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
          explore: { command: { spec: { schemaVersion: 1, modelId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 }, requestSeq: 0, resetVersion: 0, columnWidths: {} }, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], warnings: [] } },
          warnings: [],
        },
      })
      document.body.append(element)
      for (let index = 0; index < 10; index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      return element.shadowRoot?.querySelector('.main .empty')?.textContent?.trim()
    })

    expect(message).toBe('Select a data object to begin.')
  } finally {
    await page.close()
  }
})

test('data explorer emits configure, run, stop, and fresh rerun commands', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 800 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const lifecycle = await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const object = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 2,
        columns: [{ key: 'status', label: 'Status', type: 'string' }, { key: 'revenue', label: 'Revenue', type: 'decimal' }],
      }
      const exploreCommand = {
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 },
        requestSeq: 3, resetVersion: 3, columnWidths: {}, action: 'configure',
      }
      const fields = [
        { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true },
        { id: 'revenue', label: 'Revenue', kind: 'metric', datasetId: 'orders', type: 'decimal', compatible: true, selected: false },
      ]
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [object], selectedKey: object.key, selectedObject: object,
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, loading: false, stale: false, resetVersion: 0, blocks: {}, sort: {} },
          command: { mode: 'explore', objectKey: object.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 3, resetVersion: 3, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand },
          explore: {
            command: exploreCommand, semanticModels: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 2, entities: [] }] }],
            datasets: [{ id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 2, entities: [] }], fields,
            selectedSemanticModel: { id: 'sales', title: 'Sales', datasets: [] }, selectedDataset: { id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 2, entities: [] },
            result: { columns: [{ key: 'status', label: 'Status' }], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 3, truncated: false, warnings: [] },
            status: { loading: false, stale: false, requestSeq: 3, state: 'success' },
          }, warnings: [],
        },
      })
      const commands: any[] = []
      element.addEventListener('lv-data-explorer-command', (event: CustomEvent) => commands.push(event.detail))
      document.body.append(element)
      for (let index = 0; index < 10; index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const root = element.shadowRoot!
      const metric = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('Revenue'))!
      metric.click()
      await new Promise((resolve) => setTimeout(resolve, 380))
      const configure = commands.at(-1)
      const runButton = Array.from(root.querySelectorAll<HTMLButtonElement>('.query-actions .text-button')).find((button) => button.textContent?.includes('Run'))!
      const hasRunAfterConfigure = Boolean(runButton)
      const hasStopAfterConfigure = Boolean(root.querySelector('.query-actions .text-button')) && Array.from(root.querySelectorAll<HTMLButtonElement>('.query-actions .text-button')).some((button) => button.textContent?.includes('Stop'))
      runButton.click()
      await element.updateComplete
      const run = commands.at(-1)
      const stopButton = Array.from(root.querySelectorAll<HTMLButtonElement>('.query-actions .text-button')).find((button) => button.textContent?.includes('Stop'))!
      stopButton.click()
      await element.updateComplete
      const stop = commands.at(-1)
      const rerunButton = Array.from(root.querySelectorAll<HTMLButtonElement>('.query-actions .text-button')).find((button) => button.textContent?.includes('Run'))!
      rerunButton.click()
      await element.updateComplete
      const rerun = commands.at(-1)
      // Hydrate the command as a real Run response before editing again. The
      // outer lifecycle action must not leak into the following configure.
      mergePatch({ dataExplorer: { command: { action: 'run', requestSeq: rerun?.explore?.requestSeq }, explore: { command: { action: 'run', requestSeq: rerun?.explore?.requestSeq } } } })
      await element.updateComplete
      const metricAfterRun = Array.from(element.shadowRoot!.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('Revenue'))!
      metricAfterRun.click()
      await new Promise((resolve) => setTimeout(resolve, 380))
      const configureAfterRunResponse = commands.at(-1)
      return {
        configure: { action: configure?.action, nestedAction: configure?.explore?.action, requestSeq: configure?.explore?.requestSeq },
        hasRunAfterConfigure,
        hasStopAfterConfigure,
        clientIDs: commands.map((command) => command.clientId),
        run: { action: run?.action, nestedAction: run?.explore?.action, requestSeq: run?.explore?.requestSeq, runId: run?.runId },
        stop: { action: stop?.action, nestedAction: stop?.explore?.action, requestSeq: stop?.explore?.requestSeq, runId: stop?.runId },
        rerun: { action: rerun?.action, requestSeq: rerun?.explore?.requestSeq, runId: rerun?.runId },
        configureAfterRunResponse: { action: configureAfterRunResponse?.action, nestedAction: configureAfterRunResponse?.explore?.action },
      }
    })
    expect(lifecycle.configure.action).toBe('configure')
    expect(lifecycle.configure.nestedAction).toBe('configure')
    expect(lifecycle.configure.requestSeq).toBeGreaterThan(0)
    expect(lifecycle.run.requestSeq).toBeGreaterThan(lifecycle.configure.requestSeq)
    expect(lifecycle.hasRunAfterConfigure).toBe(true)
    expect(lifecycle.hasStopAfterConfigure).toBe(false)
    expect(lifecycle.clientIDs.length).toBe(5)
    expect(lifecycle.clientIDs.every((clientID: unknown) => typeof clientID === 'string' && clientID.length > 0)).toBe(true)
    expect(new Set(lifecycle.clientIDs).size).toBe(1)
    expect(lifecycle.run.action).toBe('run')
    expect(lifecycle.run.nestedAction).toBe('run')
    expect(lifecycle.run.runId).toBeTruthy()
    expect(lifecycle.stop).toEqual({ action: 'stop', nestedAction: 'stop', requestSeq: lifecycle.run.requestSeq, runId: lifecycle.run.runId })
    expect(lifecycle.rerun.action).toBe('run')
    expect(lifecycle.rerun.requestSeq).toBeGreaterThan(lifecycle.stop.requestSeq)
    expect(lifecycle.rerun.runId).not.toBe(lifecycle.run.runId)
    expect(lifecycle.configureAfterRunResponse).toEqual({ action: 'configure', nestedAction: 'configure' })
  } finally {
    await page.close()
  }
})

test('remounted explorer advances filter suggestion sequence for the same client', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 800 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const originalSessionStorage = Object.getOwnPropertyDescriptor(window, 'sessionStorage')
      // Exercise the embedded/private-browsing path where browser storage is
      // unavailable. The tab-level fallback must still survive a remount.
      Object.defineProperty(window, 'sessionStorage', { configurable: true, get: () => { throw new Error('storage unavailable') } })
      const object = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      }
      const exploreCommand = {
        action: 'configure',
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 },
        requestSeq: 2, resetVersion: 2, columnWidths: {},
      }
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [object], selectedKey: object.key, selectedObject: object,
          command: { action: 'configure', mode: 'explore', objectKey: object.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 2, resetVersion: 2, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand },
          explore: {
            command: exploreCommand,
            semanticModels: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }] }],
            datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }],
            fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true }],
            selectedSemanticModel: { id: 'sales', title: 'Sales', datasets: [] },
            selectedDataset: { id: 'orders', title: 'Orders', fieldCount: 1, entities: [] },
            result: { columns: [{ key: 'status', label: 'Status' }], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 2, truncated: false, warnings: [] },
            status: { loading: false, stale: true, requestSeq: 2, state: 'stale' },
          },
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, loading: false, stale: false, resetVersion: 0, blocks: {}, sort: {} },
          warnings: [],
        },
      })
      const mount = () => {
        const explorer = document.createElement('lv-data-explorer') as any
        const commands: any[] = []
        explorer.addEventListener('lv-data-explorer-command', (event: CustomEvent) => commands.push(event.detail))
        document.body.append(explorer)
        return { explorer, commands }
      }
      const waitForSuggestion = async (commands: any[]) => {
        for (let index = 0; index < 10; index += 1) {
          const suggestion = commands.find((command) => command.explore?.filterSuggestions)
          if (suggestion) return suggestion
          await new Promise((resolve) => setTimeout(resolve, 40))
        }
        return undefined
      }
      const first = mount()
      for (let index = 0; index < 10; index += 1) {
        await first.explorer.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const firstControls = first.explorer.shadowRoot!.querySelector('lv-data-explorer-query-controls') as any
      await firstControls.updateComplete
      firstControls.shadowRoot!.querySelector<HTMLButtonElement>('.field-action')!.click()
      const firstSuggestion = await waitForSuggestion(first.commands)
      first.explorer.remove()
      const second = mount()
      for (let index = 0; index < 10; index += 1) {
        await second.explorer.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const secondControls = second.explorer.shadowRoot!.querySelector('lv-data-explorer-query-controls') as any
      await secondControls.updateComplete
      secondControls.shadowRoot!.querySelector<HTMLButtonElement>('.field-action')!.click()
      const secondSuggestion = await waitForSuggestion(second.commands)
      const result = {
        firstClientID: firstSuggestion?.clientId,
        secondClientID: secondSuggestion?.clientId,
        firstRequestSeq: firstSuggestion?.explore?.filterSuggestions?.suggestionRequestSeq,
        secondRequestSeq: secondSuggestion?.explore?.filterSuggestions?.suggestionRequestSeq,
      }
      if (originalSessionStorage) Object.defineProperty(window, 'sessionStorage', originalSessionStorage)
      return result
    })
    expect(state.firstClientID).toBeTruthy()
    expect(state.secondClientID).toBe(state.firstClientID)
    expect(state.secondRequestSeq).toBeGreaterThan(state.firstRequestSeq)
  } finally {
    await page.close()
  }
})

test('data explorer tolerates a partially hydrated legacy exploration command', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))

    const rendered = await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const object = { key: 'model:model:orders', resourceId: 'model:orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1, columns: [{ key: 'status', label: 'Status', type: 'string' }] }
      const legacyExploreCommand = { modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [object], selectedKey: object.key, selectedObject: object,
          command: { mode: 'explore', objectKey: object.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: legacyExploreCommand },
          explore: { command: legacyExploreCommand, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] } },
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} }, warnings: [],
        },
      })
      document.body.append(element)
      for (let index = 0; index < 10; index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      return element.shadowRoot?.querySelector('.main')?.textContent?.replace(/\s+/g, ' ').trim()
    })

    expect(rendered).toContain('Select at least one field')
  } finally {
    await page.close()
  }
})

test('related physical filter uses the active exploration dataset scope', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 800 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))

    const filter = await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const object = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model',
        semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      }
      const dataset = { id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 1, entities: [] }
      const exploreCommand = {
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 },
        requestSeq: 1, resetVersion: 1, columnWidths: {},
      }
      const fields = [
        { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true },
        { id: 'customers.state', label: 'State', kind: 'dimension', datasetId: 'customers', type: 'string', compatible: true, relationshipPath: ['orders_customers'], selected: false },
      ]
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [object], selectedKey: object.key, selectedObject: object,
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
          command: { mode: 'explore', objectKey: object.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 1, resetVersion: 1, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand },
          explore: {
            command: exploreCommand,
            semanticModels: [{ id: 'sales', title: 'Sales', datasets: [dataset] }],
            datasets: [dataset], selectedSemanticModel: { id: 'sales', title: 'Sales', datasets: [dataset] }, selectedDataset: dataset,
            fields,
            result: { columns: [{ key: 'status', label: 'Status' }], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 1, truncated: false, warnings: [] },
          },
          warnings: [],
        },
      })
      const commands: any[] = []
      element.addEventListener('lv-data-explorer-command', (event: CustomEvent) => commands.push(event.detail))
      document.body.append(element)
      for (let index = 0; index < 20 && !element.shadowRoot?.querySelector('lv-data-explorer-query-controls'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const controls = element.shadowRoot!.querySelector('lv-data-explorer-query-controls') as any
      await controls.updateComplete
      const controlRoot = controls.shadowRoot!
      const stateField = Array.from(controlRoot.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('State'))!
      const filterButton = stateField.parentElement?.querySelector<HTMLButtonElement>('.field-action')
      if (!filterButton) throw new Error(`related field filter button was not rendered: ${controlRoot.textContent}`)
      filterButton.click()
      await element.updateComplete
      await controls.updateComplete
      const filterInput = controls.shadowRoot!.querySelector<HTMLInputElement>('.filter-editor label:nth-child(3) input')!
      filterInput.value = 'CA'
      filterInput.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      await controls.updateComplete
      const applyButton = Array.from(controls.shadowRoot!.querySelectorAll<HTMLButtonElement>('.filter-editor .text-button')).find((button) => button.textContent?.trim() === 'Apply')
      if (!applyButton) throw new Error(`related filter apply button was not rendered: ${controls.shadowRoot!.textContent}`)
      applyButton.click()
      await new Promise((resolve) => setTimeout(resolve, 380))
      return commands.flatMap((command) => command.explore?.spec?.filters ?? []).find((candidate: any) => candidate.field === 'customers.state')
    })

    expect(filter).toMatchObject({ field: 'customers.state', datasetId: 'orders' })
  } finally {
    await page.close()
  }
})

test('data preview and semantic query failures expose retry and reset actions', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-preview-table'))

    const state = await page.evaluate(async () => {
      const preview = document.createElement('lv-data-preview-table') as any
      preview.preview = {
        columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32,
        resetVersion: 2, blocks: {}, totalRowLabel: 'Unknown', sort: {}, error: 'Preview timed out.',
      }
      preview.command = {
        objectKey: 'orders', offset: 100, limit: 100, block: 'orders:100', start: 100, count: 100,
        requestSeq: 7, resetVersion: 2, sort: { column: 'created_at', direction: 'desc' },
        visibleColumns: ['id'], columnWidths: {},
      }
      const previewCommands: any[] = []
      preview.addEventListener('lv-data-preview-table-command', (event: CustomEvent) => previewCommands.push(event.detail))
      document.body.append(preview)
      await preview.updateComplete
      const previewAlert = preview.shadowRoot!.querySelector('[role="alert"]')!.textContent!.replace(/\s+/g, ' ').trim()
      const previewButtons = Array.from(preview.shadowRoot!.querySelectorAll<HTMLButtonElement>('.failure button'))
      previewButtons[0].click()
      previewButtons[1].click()

      const object = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model',
        semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      }
      const exploreCommand = {
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 },
        requestSeq: 4, resetVersion: 3, columnWidths: {},
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [object], selectedKey: object.key, selectedObject: object,
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
          command: { mode: 'explore', objectKey: object.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand },
          explore: {
            command: exploreCommand,
            semanticModels: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }] }],
            datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }],
            fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true }],
            result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 4, truncated: false, warnings: [], error: 'Query service is unavailable.' },
          }, warnings: [],
        },
      })
      const explorer = document.createElement('lv-data-explorer') as any
      const exploreCommands: any[] = []
      explorer.addEventListener('lv-data-explorer-command', (event: CustomEvent) => exploreCommands.push(event.detail))
      document.body.append(explorer)
      for (let index = 0; index < 20 && !explorer.shadowRoot?.querySelector('.result-failure'); index += 1) {
        await explorer.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const failure = explorer.shadowRoot!.querySelector('.result-failure')!
      const exploreAlert = failure.textContent!.replace(/\s+/g, ' ').trim()
      const exploreButtons = Array.from(failure.querySelectorAll<HTMLButtonElement>('button'))
      exploreButtons[0].click()
      await explorer.updateComplete
      const resetButton = explorer.shadowRoot!.querySelector<HTMLButtonElement>('.result-failure button:last-of-type')!
      resetButton.click()
      await explorer.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 0))
      return {
        previewAlert,
        previewCommands,
        exploreAlert,
        exploreCommands,
        retryRequestSeq: exploreCommands[0]?.explore?.requestSeq,
        resetRequestSeq: exploreCommands[1]?.explore?.requestSeq,
      }
    })

    expect(state.previewAlert).toContain('Preview timed out.')
    expect(state.previewCommands[0]).toMatchObject({ objectKey: 'orders', requestSeq: 8, resetVersion: 2 })
    expect(state.previewCommands[1]).toMatchObject({ objectKey: 'orders', offset: 0, start: 0, block: 'all', requestSeq: 8, resetVersion: 3, sort: {} })
    expect(state.exploreAlert).toContain('Query service is unavailable.')
    expect(state.exploreCommands).toHaveLength(2)
    expect(state.exploreCommands[0].explore.spec).toMatchObject({ modelId: 'sales', datasetId: 'orders' })
    expect(state.exploreCommands[0]).toMatchObject({ action: 'run', explore: { action: 'run' } })
    expect(state.exploreCommands[1]).toMatchObject({ action: 'configure', explore: { action: 'configure' } })
    expect(state.resetRequestSeq).toBeGreaterThan(state.retryRequestSeq)
    expect(state.exploreCommands[1].explore.spec).toMatchObject({ dimensions: [], metrics: [], filters: [], sort: [] })
  } finally {
    await page.close()
  }
})

test('query controls explain and disable unsupported relative time ranges', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer-query-controls'))

    const state = await page.evaluate(async () => {
      const controls = document.createElement('lv-data-explorer-query-controls') as any
      controls.command = {
        spec: {
          schemaVersion: 1, modelId: 'sales', datasetId: 'orders',
          dimensions: [], metrics: [], filters: [], sort: [], limit: 100,
          time: { field: 'orders.created_at', grain: 'day', range: { kind: 'relative', direction: 'previous', count: 3, unit: 'month', includeCurrent: true, anchor: 'current_time' } },
        },
        requestSeq: 1, resetVersion: 1, columnWidths: {},
      }
      controls.fields = [{ id: 'orders.created_at', label: 'Created at', kind: 'dimension', datasetId: 'orders', type: 'date', compatible: true, selected: false }]
      document.body.append(controls)
      await controls.updateComplete
      const root = controls.shadowRoot!
      const range = root.querySelector<HTMLSelectElement>('select[aria-label="Time range"]')!
      return {
        relativeDisabled: range.querySelector<HTMLOptionElement>('option[value="relative"]')?.disabled,
        message: root.querySelector<HTMLElement>('[role="alert"]')?.textContent,
      }
    })

    expect(state.relativeDisabled).toBe(true)
    expect(state.message).toContain('Relative time ranges are not supported yet')
  } finally {
    await page.close()
  }
})

test('non-embedded explorer reloads the selected Back/Forward entries exactly once', { timeout: 15_000 }, async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  const reloadURLs: string[] = []
  const onRequest = (request: import('@playwright/test').Request) => {
    const url = new URL(request.url())
    if (url.pathname === '/explore' && url.search) reloadURLs.push(request.url())
  }
  page.on('request', onRequest)
  try {
    await page.goto(`${baseURL}/explore`)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))

    const historyState = await page.evaluate(async () => {
      const object = (table: string) => ({
        key: `model:model:sales.${table}`,
        resourceId: `model:sales.${table}`,
        layer: 'model', semanticModelId: 'sales', datasetId: table, title: table, columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      })
      const first = object('orders')
      const second = object('customers')
      const third = object('products')
      const exploreCommand = {
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 },
        requestSeq: 0, resetVersion: 0, columnWidths: {},
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [first, second, third], selectedKey: first.key, selectedObject: first,
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, blocks: {}, sort: {} },
          command: { mode: 'browse', objectKey: first.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, },
          explore: {
            command: exploreCommand, semanticModels: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }] }],
            datasets: [{ id: 'orders', title: 'Orders', fieldCount: 1, entities: [] }], fields: [],
            result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] },
          }, warnings: [],
        },
      })
      const explorer = document.querySelector('lv-data-explorer') as any
      for (let index = 0; index < 20 && explorer.shadowRoot?.querySelectorAll('.object-button').length !== 3; index += 1) {
        await explorer.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const buttons = Array.from(explorer.shadowRoot.querySelectorAll<HTMLButtonElement>('.object-button'))
      buttons.find((button) => button.textContent?.includes('customers'))?.click()
      buttons.find((button) => button.textContent?.includes('products'))?.click()
      return {
        first: first.key, second: second.key, third: third.key,
        current: new URL(window.location.href).searchParams.get('object'),
      }
    })

    expect(historyState.current).toBe(historyState.third)
    expect(reloadURLs).toHaveLength(0)

    const backRequest = page.waitForRequest((request) => {
      const url = new URL(request.url())
      return url.pathname === '/explore' && url.search
    })
    const backLoad = page.waitForEvent('load')
    const backNavigation = page.goBack({ waitUntil: 'commit' })
    const [backDocument] = await Promise.all([backRequest, backNavigation, backLoad])
    expect(backDocument.isNavigationRequest()).toBe(true)
    expect(backDocument.resourceType()).toBe('document')
    await backDocument.response()
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && document.querySelector('lv-data-explorer'))
    expect(new URL(page.url()).searchParams.get('object')).toBe(historyState.second)
    expect(reloadURLs).toHaveLength(1)

    const forwardRequest = page.waitForRequest((request) => {
      const url = new URL(request.url())
      return url.pathname === '/explore' && url.search
    })
    const forwardLoad = page.waitForEvent('load')
    const forwardNavigation = page.goForward({ waitUntil: 'commit' })
    const [forwardDocument] = await Promise.all([forwardRequest, forwardNavigation, forwardLoad])
    expect(forwardDocument.isNavigationRequest()).toBe(true)
    expect(forwardDocument.resourceType()).toBe('document')
    await forwardDocument.response()
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && document.querySelector('lv-data-explorer'))
    expect(new URL(page.url()).searchParams.get('object')).toBe(historyState.third)
    expect(reloadURLs).toHaveLength(2)
  } finally {
    page.off('request', onRequest)
    await page.close()
  }
})

test('non-embedded explorer keeps the selected saved deep link during URL updates', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/explore?saved=exploration%3Aactive`)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const url = await page.evaluate(async () => {
      const object = (table: string) => ({
        key: `model:model:sales.${table}`,
        resourceId: `model:sales.${table}`,
        layer: 'model', semanticModelId: 'sales', datasetId: table, title: table, columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      })
      const orders = object('orders')
      const customers = object('customers')
      const exploreCommand = {
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 },
        requestSeq: 0, resetVersion: 0, columnWidths: {},
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        dataExplorer: {
          objects: [orders, customers], selectedKey: orders.key, selectedObject: orders,
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, blocks: {}, sort: {} },
          command: { mode: 'browse', objectKey: orders.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
          explore: {
            command: exploreCommand, semanticModels: [], datasets: [], fields: [],
            result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] },
          }, warnings: [],
        },
        savedExplorations: {
          enabled: true, list: { items: [], includeArchived: false, selectedId: 'exploration:active' },
          command: { action: 'create' }, save: { state: 'saved' },
        },
      })
      const explorer = document.querySelector('lv-data-explorer') as any
      for (let index = 0; index < 20 && explorer.shadowRoot?.querySelectorAll('.object-button').length !== 2; index += 1) {
        await explorer.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      Array.from(explorer.shadowRoot.querySelectorAll<HTMLButtonElement>('.object-button')).find((button) => button.textContent?.includes('customers'))?.click()
      return window.location.href
    })
    expect(new URL(url).searchParams.get('saved')).toBe('exploration:active')
    expect(new URL(url).searchParams.get('object')).toContain('customers')
  } finally {
    await page.close()
  }
})

test('saved metadata patches do not replace an in-flight optimistic query URL', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/explore?saved=exploration%3Aactive`)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const url = await page.evaluate(async () => {
      const spec = { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 }
      const object = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      }
      const exploreCommand = { spec, requestSeq: 1, resetVersion: 1, columnWidths: {} }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        dataExplorer: {
          objects: [object], selectedKey: object.key, selectedObject: object,
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
          command: { mode: 'explore', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 1, resetVersion: 1, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand },
          explore: { command: exploreCommand, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 1, truncated: false, warnings: [] } }, warnings: [],
        },
        savedExplorations: {
          enabled: true, list: { items: [], includeArchived: false, selectedId: 'exploration:active' },
          current: { id: 'exploration:active', title: 'Active', slug: 'active', visibility: 'private', status: 'active', semanticModelId: 'sales', revision: { revisionId: 'revision:1', number: 1, contentHash: 'sha256:' + 'a'.repeat(64) }, detached: false, spec },
          command: { action: 'create' }, save: { state: 'saved' },
        },
      })
      const explorer = document.querySelector('lv-data-explorer') as any
      for (let index = 0; index < 20 && !explorer.shadowRoot?.querySelector('.field-button'); index += 1) {
        await explorer.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      // This follows the real edit path: emitExplore records an optimistic
      // command and updates the canonical URL before the response arrives.
      explorer.emitExplore({ dimensions: [{ field: 'orders.status' }] }, undefined, undefined, true)
      const optimisticURL = window.location.href
      // A concurrent save-state/list patch must not restore the stale server
      // command while the optimistic explorer request remains unresolved.
      mergePatch({ savedExplorations: { enabled: true, list: { items: [], includeArchived: false, selectedId: 'exploration:active' }, command: { action: 'create' }, save: { state: 'saving' } } })
      await explorer.updateComplete
      return { optimisticURL, currentURL: window.location.href }
    })
    expect(new URL(url.currentURL).searchParams.get('state')).toContain('orders.status')
    expect(url.currentURL).toBe(url.optimisticURL)
  } finally {
    await page.close()
  }
})

test('saved exploration handoff keeps explicit targets, active authored spec, and archived copies read-only', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const state = await page.evaluate(async () => {
      const spec = { schemaVersion: 1, modelId: 'model:active', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 }
      const revision = { revisionId: 'revision:1', number: 1, contentHash: 'sha256:' + 'a'.repeat(64) }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        dataExplorer: {
          objects: [], selectedKey: '',
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
          command: { mode: 'explore', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: { spec, requestSeq: 0, resetVersion: 0, columnWidths: {} } },
          explore: { command: { spec, requestSeq: 0, resetVersion: 0, columnWidths: {} }, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] } },
          warnings: [],
        },
        savedExplorations: {
          enabled: true,
          list: { items: [], includeArchived: false, selectedId: 'exploration:active' },
          current: { id: 'exploration:active', title: 'Active', slug: 'active', visibility: 'organization', status: 'active', semanticModelId: 'model:active', revision, detached: true, spec: { ...spec, modelId: 'model:baseline' } },
          command: { action: 'create' }, save: { state: 'saved' },
        },
      })
      const element = document.createElement('lv-data-explorer') as any
      const commands: any[] = []
      element.addEventListener('lv-saved-exploration-command', (event: CustomEvent) => commands.push(event.detail))
      document.body.append(element)
      for (let index = 0; index < 20 && !element.shadowRoot?.querySelector('input[aria-label="Duplicate saved exploration name"]'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const currentQueryLink = element.shadowRoot.querySelector<HTMLAnchorElement>('a[aria-label="Open current exploration query link"]')!
      const latestSavedLink = element.shadowRoot.querySelector<HTMLAnchorElement>('a[aria-label="Open latest saved version"]')!
      const visibility = element.shadowRoot.querySelector<HTMLSelectElement>('select[aria-label="Saved exploration visibility"]')!
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async () => { throw new Error('clipboard permission denied') } } })
      element.shadowRoot.querySelector<HTMLButtonElement>('button[aria-label="Copy current exploration query link"]')!.click()
      for (let index = 0; index < 20 && !element.shadowRoot?.querySelector('[role="status"].saved-exploration-share-status'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const copyStatus = element.shadowRoot.querySelector<HTMLElement>('[role="status"].saved-exploration-share-status')
      const copyFallback = element.shadowRoot.querySelector<HTMLAnchorElement>('.saved-exploration-share-fallback')
      const links = {
        current: currentQueryLink.getAttribute('href'),
        latest: latestSavedLink.getAttribute('href'),
        latestLabel: latestSavedLink.textContent?.trim(),
      }
      const buttons = () => Array.from(element.shadowRoot.querySelectorAll<HTMLButtonElement>('.saved-exploration-actions button, .saved-exploration-current button'))
      const currentQueryName = element.shadowRoot.querySelector<HTMLInputElement>('input[aria-label="Current query name"]')!
      currentQueryName.value = 'Working query'
      currentQueryName.dispatchEvent(new Event('input', { bubbles: true }))
      buttons().find((button) => button.textContent?.trim() === 'Save as current query')?.click()
      buttons().find((button) => button.textContent?.trim() === 'Save')?.click()
      const duplicateInput = element.shadowRoot.querySelector<HTMLInputElement>('input[aria-label="Duplicate saved exploration name"]')!
      visibility.value = 'private'
      visibility.dispatchEvent(new Event('change', { bubbles: true }))
      duplicateInput.value = 'Second copy'
      duplicateInput.dispatchEvent(new Event('input', { bubbles: true }))
      buttons().find((button) => button.textContent?.trim() === 'Duplicate saved version')?.click()
      duplicateInput.value = 'Third copy'
      duplicateInput.dispatchEvent(new Event('input', { bubbles: true }))
      buttons().find((button) => button.textContent?.trim() === 'Duplicate saved version')?.click()
      await element.updateComplete
      mergePatch({ savedExplorations: {
        enabled: true,
        list: { items: [], includeArchived: true, selectedId: 'exploration:archived' },
        current: { id: 'exploration:archived', title: 'Archived', slug: 'archived', visibility: 'private', status: 'archived', semanticModelId: 'model:active', revision, detached: true, spec },
        command: { action: 'create' }, save: { state: 'saved' },
      } })
      await element.updateComplete
      const archivedButtons = Array.from(element.shadowRoot.querySelectorAll<HTMLButtonElement>('.saved-exploration-current button')).map((button) => button.textContent?.trim())
      const archivedReadOnly = element.shadowRoot.textContent?.includes('Read-only archived copy') ?? false
      mergePatch({ savedExplorations: {
        enabled: true,
        list: { items: [], includeArchived: false, selectedId: 'exploration:private' },
        current: { id: 'exploration:private', title: 'Private', slug: 'private', visibility: 'private', status: 'active', semanticModelId: 'model:active', revision, detached: true, spec },
        command: { action: 'create' }, save: { state: 'saved' },
      } })
      for (let index = 0; index < 20 && element.shadowRoot?.querySelector<HTMLSelectElement>('select[aria-label="Saved exploration visibility"]')?.value !== 'private'; index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const switchedVisibility = element.shadowRoot.querySelector<HTMLSelectElement>('select[aria-label="Saved exploration visibility"]')?.value
      mergePatch({ savedExplorations: {
        enabled: true,
        list: { items: [], includeArchived: false },
        current: null,
        command: { action: 'create' }, save: { state: 'saved' },
      } })
      for (let index = 0; index < 20 && !element.shadowRoot?.querySelector('input[aria-label="Saved exploration name"]'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const createVisibility = element.shadowRoot.querySelector<HTMLSelectElement>('select[aria-label="Saved exploration visibility"]')!
      createVisibility.value = 'organization'
      createVisibility.dispatchEvent(new Event('change', { bubbles: true }))
      Array.from(element.shadowRoot.querySelectorAll<HTMLButtonElement>('.saved-exploration-actions button')).find((button) => button.textContent?.trim() === 'Save current')?.click()
      return {
        commands,
        links,
        copy: {
          status: copyStatus?.textContent?.trim(),
          role: copyStatus?.getAttribute('role'),
          fallback: copyFallback?.getAttribute('href'),
        },
        switchedVisibility,
        createCommand: commands[commands.length - 1],
        archivedButtons,
        archivedReadOnly,
      }
    })
    expect(state.commands[0]).toMatchObject({ action: 'create', title: 'Working query', visibility: 'private', spec: { modelId: 'model:active' } })
    expect(state.commands[0]).not.toHaveProperty('explorationId')
    expect(state.commands[0]).not.toHaveProperty('sourceExplorationId')
    expect(state.commands[1]).toMatchObject({ action: 'update', explorationId: 'exploration:active', visibility: 'organization', spec: { modelId: 'model:active' }, expectedRevision: { revisionId: 'revision:1' } })
    expect(state.commands[2]).toMatchObject({ action: 'duplicate', sourceExplorationId: 'exploration:active', title: 'Second copy', visibility: 'organization', expectedSourceRevision: { revisionId: 'revision:1' } })
    expect(state.commands[3]).toMatchObject({ action: 'duplicate', sourceExplorationId: 'exploration:active', title: 'Third copy', visibility: 'organization', expectedSourceRevision: { revisionId: 'revision:1' } })
    expect(state.commands[2]).not.toHaveProperty('slug')
    expect(state.commands[3]).not.toHaveProperty('slug')
    expect(state.links.current).toContain('/explore?v=2&mode=explore&state=')
    expect(new URL(state.links.current!, 'http://127.0.0.1').searchParams.get('saved')).toBeNull()
    expect(state.links.latestLabel).toBe('Latest saved version')
    expect(state.links.latest).toBe('/explore/saved/exploration%3Aactive?navigation=true')
    expect(state.copy.status).toBe('Copy failed. Open the link directly below.')
    expect(state.copy.role).toBe('status')
    expect(state.copy.fallback).toMatch(/^http:\/\/127\.0\.0\.1:\d+\/explore\?v=2&mode=explore&state=/)
    expect(state.switchedVisibility).toBe('private')
    expect(state.createCommand).toMatchObject({ action: 'create', visibility: 'organization', spec: { modelId: 'model:active' } })
    expect(state.archivedButtons).not.toContain('Save')
    expect(state.archivedReadOnly).toBe(true)
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
