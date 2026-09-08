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
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
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

test('data explorer builds a governed semantic exploration and filter command', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-explore-table'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const pageSignal = {
        kind: 'data', title: 'Data Explorer', description: 'Inspect or explore data.', tabs: [],
      }
      const exploreCommand = {
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [{ field: 'revenue' }],
          filters: [], sort: [{ field: 'revenue', direction: 'desc' }], limit: 100 },
        requestSeq: 1, resetVersion: 1, columnWidths: {},
      }
      const selectedObject = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'orders',
        description: 'One row per order.', grain: 'order_id', columnCount: 2, rowCountLabel: '10',
        columns: [
          { key: 'order_id', label: 'Order ID', type: 'string' },
          { key: 'status', label: 'Status', type: 'string' },
          { key: 'order_status', label: 'Order status', type: 'string' },
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
          semanticModels: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 3, entities: [] }] }],
          datasets: [{ id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 3, entities: [] }],
          selectedSemanticModel: { id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 3, entities: [] }] },
          selectedDataset: { id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 3, entities: [] },
          fields: [
            { id: 'orders.order_id', label: 'Order ID', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: false },
            { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true },
            { id: 'order_status', label: 'Order status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: false },
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
      const filterInput = root.querySelector<HTMLInputElement>('.filter-editor label:nth-child(3) input')!
      filterInput.value = 'delivered'
      filterInput.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      const applyButton = Array.from(root.querySelectorAll<HTMLButtonElement>('.filter-editor .text-button')).find((button) => button.textContent?.trim() === 'Apply')
      if (!applyButton) throw new Error(`Apply filter button was not rendered: ${root.textContent}`)
      applyButton.click()
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 380))
      const physicalFilter = commands.at(-1)?.explore?.spec?.filters?.at(-1)

      const semanticRow = Array.from(root.querySelectorAll<HTMLElement>('.column-item')).find((row) => row.textContent?.includes('Order status'))
      const semanticFilterButton = semanticRow?.querySelector<HTMLButtonElement>('.field-action')
      if (!semanticFilterButton) throw new Error(`Conformed dimension filter button was not rendered: ${root.textContent}`)
      semanticFilterButton.click()
      await element.updateComplete
      const semanticFilterInput = root.querySelector<HTMLInputElement>('.filter-editor label:nth-child(3) input')!
      semanticFilterInput.value = 'paid'
      semanticFilterInput.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      const semanticApplyButton = Array.from(root.querySelectorAll<HTMLButtonElement>('.filter-editor .text-button')).find((button) => button.textContent?.trim() === 'Apply')
      if (!semanticApplyButton) throw new Error(`Apply conformed filter button was not rendered: ${root.textContent}`)
      semanticApplyButton.click()
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 380))
      const semanticFilter = commands.at(-1)?.explore?.spec?.filters?.find((filter: any) => filter.field === 'order_status')

      const table = root.querySelector('lv-data-explore-table') as any
      await table.updateComplete
      const exploreRequestSeqBeforeWidth = table.command.requestSeq
      table.dispatchEvent(new CustomEvent('lv-data-explore-table-command', {
        bubbles: true, composed: true, detail: { columnWidths: { revenue: 240 } },
      }))
      await element.updateComplete
      const nestedWidthCommand = commands.at(-1)
      const windowedTable = table.shadowRoot.querySelector('lv-windowed-table')
      windowedTable.dispatchEvent(new CustomEvent('lv-windowed-table-request', {
        bubbles: true, composed: true, detail: { start: 0, sort: { key: 'status', direction: 'asc' } },
      }))
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 380))
      const derivedSortCommand = commands.at(-1)
      table.command = { ...table.command, spec: { ...table.command.spec, sort: [{ field: 'orders.status', direction: 'asc' }] } }
      await table.updateComplete
      const tableSortPayload = table.shadowRoot.querySelector('lv-windowed-table')?.table?.sort
      const stateField = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('State'))!
      const skuField = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('SKU'))!
      skuField.click()
      const initialState = {
        modes: Array.from(root.querySelectorAll('.mode-button')).map((button) => ({ text: button.textContent?.trim(), pressed: button.getAttribute('aria-pressed') })),
        hasBreadcrumb: Boolean(root.querySelector('[aria-label="Breadcrumb"]')),
        resourceTables: root.querySelector('.resource-group')?.textContent?.replace(/\s+/g, ' ').trim(),
        chips: Array.from(root.querySelectorAll('.selection-shelf .chip')).map((chip) => chip.textContent?.replace(/\s+/g, ' ').trim()),
        grain: root.querySelector('.result-meta')?.textContent?.replace(/\s+/g, ' ').trim(),
        tableRows: table.result.rows,
        relatedField: { disabled: stateField.disabled, text: stateField.textContent?.replace(/\s+/g, ' ').trim(), title: stateField.title },
      }
      const unavailableField = { disabled: skuField.disabled, text: skuField.textContent?.replace(/\s+/g, ' ').trim(), title: skuField.title }

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
        rebaseField: { disabled: rebaseField.disabled, text: rebaseField.textContent?.replace(/\s+/g, ' ').trim(), title: rebaseField.title },
        rebaseCommand,
        tableSelectionCommand,
        nestedWidthCommand,
        exploreRequestSeqBeforeWidth,
        derivedSortCommand,
        tableSortPayload,
        commands,
      }
    })

    expect(state.modes).toEqual([])
    expect(state.hasBreadcrumb).toBe(false)
    expect(state.resourceTables).toContain('orders')
    expect(state.chips.join(' ')).toContain('Order ID')
    expect(state.chips.join(' ')).toContain('Revenue')
    expect(state.grain).toContain('Grain: order_id')
    expect(state.tableRows).toEqual([{ status: 'delivered', revenue: 1200 }])
    expect(state.physicalFilter).toMatchObject({ field: 'orders.status', datasetId: 'orders' })
    expect(state.semanticFilter).toMatchObject({ field: 'order_status' })
    expect(state.semanticFilter).not.toHaveProperty('datasetId')
    expect(state.relatedField.disabled).toBe(false)
    expect(state.relatedField.text).toContain('related')
    expect(state.relatedField.title).toContain('orders_customers')
    expect(state.unavailableField.disabled).toBe(true)
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
    expect(state.nestedWidthCommand.explore.columnWidths).toEqual({ revenue: 240 })
    expect(state.nestedWidthCommand.columnWidths).toEqual({})
    expect(state.nestedWidthCommand.explore.requestSeq).toBe(state.exploreRequestSeqBeforeWidth)
    expect(state.nestedWidthCommand.requestSeq).toBe(0)
    expect(state.derivedSortCommand.explore.spec.sort).toEqual([{ field: 'orders.status', direction: 'asc' }])
    expect(state.tableSortPayload).toEqual({ key: 'status', column: 'status', direction: 'asc' })
    expect(state.commands.some((command) => command.explore?.spec?.dimensions?.some((field: any) => field.field === 'items.sku'))).toBe(false)
    expect(state.commands.some((command) => command.mode === 'explore' && command.explore?.spec?.dimensions?.some((field: any) => field.field === 'orders.order_id'))).toBe(true)
    expect(state.commands.some((command) => command.explore?.spec?.filters?.some((filter: any) => filter.field === 'orders.status' && filter.expression?.value?.value === 'delivered'))).toBe(true)
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
      exploreButtons[1].click()
      await explorer.updateComplete
      return { previewAlert, previewCommands, exploreAlert, exploreCommands }
    })

    expect(state.previewAlert).toContain('Preview timed out.')
    expect(state.previewCommands[0]).toMatchObject({ objectKey: 'orders', requestSeq: 8, resetVersion: 2 })
    expect(state.previewCommands[1]).toMatchObject({ objectKey: 'orders', offset: 0, start: 0, block: 'all', requestSeq: 8, resetVersion: 3, sort: {} })
    expect(state.exploreAlert).toContain('Query service is unavailable.')
    expect(state.exploreCommands[0].explore.spec).toMatchObject({ modelId: 'sales', datasetId: 'orders' })
    expect(state.exploreCommands[1].explore.spec).toMatchObject({ dimensions: [], metrics: [], filters: [], sort: [] })
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
          current: { id: 'exploration:active', title: 'Active', slug: 'active', visibility: 'private', status: 'active', semanticModelId: 'model:active', revision, detached: true, spec: { ...spec, modelId: 'model:baseline' } },
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
      const buttons = () => Array.from(element.shadowRoot.querySelectorAll<HTMLButtonElement>('.saved-exploration-actions button, .saved-exploration-current button'))
      buttons().find((button) => button.textContent?.trim() === 'Save')?.click()
      const duplicateInput = element.shadowRoot.querySelector<HTMLInputElement>('input[aria-label="Duplicate saved exploration name"]')!
      duplicateInput.value = 'Second copy'
      duplicateInput.dispatchEvent(new Event('input', { bubbles: true }))
      buttons().find((button) => button.textContent?.trim() === 'Duplicate')?.click()
      duplicateInput.value = 'Third copy'
      duplicateInput.dispatchEvent(new Event('input', { bubbles: true }))
      buttons().find((button) => button.textContent?.trim() === 'Duplicate')?.click()
      await element.updateComplete
      mergePatch({ savedExplorations: {
        enabled: true,
        list: { items: [], includeArchived: true, selectedId: 'exploration:archived' },
        current: { id: 'exploration:archived', title: 'Archived', slug: 'archived', visibility: 'private', status: 'archived', semanticModelId: 'model:active', revision, detached: true, spec },
        command: { action: 'create' }, save: { state: 'saved' },
      } })
      await element.updateComplete
      return {
        commands,
        archivedButtons: Array.from(element.shadowRoot.querySelectorAll<HTMLButtonElement>('.saved-exploration-current button')).map((button) => button.textContent?.trim()),
        archivedReadOnly: element.shadowRoot.textContent?.includes('Read-only archived copy') ?? false,
      }
    })
    expect(state.commands[0]).toMatchObject({ action: 'update', explorationId: 'exploration:active', spec: { modelId: 'model:active' }, expectedRevision: { revisionId: 'revision:1' } })
    expect(state.commands[1]).toMatchObject({ action: 'duplicate', sourceExplorationId: 'exploration:active', title: 'Second copy', expectedSourceRevision: { revisionId: 'revision:1' } })
    expect(state.commands[2]).toMatchObject({ action: 'duplicate', sourceExplorationId: 'exploration:active', title: 'Third copy', expectedSourceRevision: { revisionId: 'revision:1' } })
    expect(state.commands[1]).not.toHaveProperty('slug')
    expect(state.commands[2]).not.toHaveProperty('slug')
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
