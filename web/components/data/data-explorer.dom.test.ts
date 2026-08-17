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
        selectedObject: 'model_table:model_table:olist.orders',
        tabs: [],
      }
      const dataExplorer = {
        objects: [
          {
            key: 'source:source:olist.orders',
            resourceId: 'source:olist.orders',
            layer: 'source',
            modelId: 'olist',
            source: 'orders',
            title: 'orders source',
            columnCount: 2,
            rowCountLabel: '10',
            columns: [{ key: 'order_id', label: 'order_id', type: 'VARCHAR' }],
          },
          {
            key: 'model_table:model_table:olist.regions',
            resourceId: 'model_table:olist.regions',
            layer: 'model_table',
            modelId: 'olist',
            table: 'regions',
            title: 'regions',
            columnCount: 1,
            rowCountLabel: '5',
            columns: [{ key: 'region', label: 'region', type: 'VARCHAR' }],
          },
          {
            key: 'model_table:model_table:olist.orders',
            resourceId: 'model_table:olist.orders',
            layer: 'model_table',
            modelId: 'olist',
            table: 'orders',
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
            modelId: 'olist',
            source: 'orders',
            title: 'orders source',
            columnCount: 2,
            rowCountLabel: '10',
            columns: [{ key: 'order_id', label: 'order_id', type: 'VARCHAR' }],
          },
          {
            key: 'model_table:model_table:olist.customers',
            resourceId: 'model_table:olist.customers',
            layer: 'model_table',
            modelId: 'olist',
            table: 'customers',
            title: 'customers',
            columnCount: 1,
            rowCountLabel: 'Unknown',
            columns: [{ key: 'status', label: 'Status', type: 'string' }],
          },
        ],
        selectedKey: 'model_table:model_table:olist.orders',
        selectedObject: {
          key: 'model_table:model_table:olist.orders',
          resourceId: 'model_table:olist.orders',
          layer: 'model_table',
          modelId: 'olist',
          table: 'orders',
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
        command: { objectKey: 'model_table:model_table:olist.orders', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
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
        tabs: Array.from(root.querySelectorAll('.object-tab')).map((tab) => tab.textContent?.trim()),
        selectedColumns: Array.from(root.querySelector('.object-button.is-selected')?.closest('.object-node')?.querySelectorAll('.column-item .field-button > span:nth-child(3)') ?? []).map((item) => item.textContent?.trim()),
        selectedFieldStates: Array.from(root.querySelector('.object-button.is-selected')?.closest('.object-node')?.querySelectorAll('.column-item .field-button') ?? []).map((item) => item.getAttribute('aria-pressed')),
        selectedNodeText: root.querySelector('.object-button.is-selected')?.textContent?.replace(/\s+/g, ' ').trim(),
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
    expect(state.groups.join(' ')).not.toContain('Model tables')
    expect(state.groups.join(' ')).not.toContain('Semantic views')
    expect(state.hasBreadcrumb).toBe(false)
    expect(state.hasDescription).toBe(false)
    expect(state.hasSelectedHeader).toBe(false)
    expect(state.badgeCount).toBe(0)
    expect(state.hasSearch).toBe(true)
    expect(state.tabs).toEqual([])
    expect(state.selectedColumns).toEqual(['order_id', 'status'])
    expect(state.selectedFieldStates).toEqual(['true', 'true'])
    expect(state.selectedNodeText).not.toContain('olist · orders')
    expect(state.selectedNodeText).toBe('orders')
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
    expect(state.tableKey).toBe('model_table:model_table:olist.orders')
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
    expect(state.commands.some((command) => command.objectKey === 'model_table:model_table:olist.customers')).toBe(true)
    expect(state.commands.some((command) => command.objectKey === 'model_table:model_table:olist.customers' && command.visibleColumns?.length === 0 && Object.keys(command.columnWidths ?? {}).length === 0)).toBe(true)
    expect(state.commands.some((command) => command.sort?.column === 'order_id')).toBe(true)
    expect(state.commands.some((command) => command.visibleColumns?.length === 1 && command.visibleColumns[0] === 'order_id')).toBe(true)
    expect(state.commands.some((command) => command.objectKey === 'model_table:model_table:olist.orders' && command.columnWidths?.order_id > 200)).toBe(true)
    expect(state.commands.some((command) => command.block && command.start > 0 && command.count === 100 && command.requestSeq > 0)).toBe(true)
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
            key: 'model_table:model:orders', resourceId: 'model:orders', layer: 'model_table',
            modelId: 'semantic:sales', table: 'orders', title: 'Orders', columnCount: 1,
            columns: [{ key: 'order_id', label: 'Order ID', type: 'string' }],
          }],
          preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
          command: { offset: 0, limit: 100, start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
          explore: { command: { dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }, models: [], datasets: [], fields: [], result: { columns: [], rows: [], warnings: [] } },
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
        modelId: 'sales', datasetId: 'orders', dimensions: ['orders.status'], metrics: ['revenue'],
        filters: [], sort: [{ field: 'revenue', direction: 'desc' }], limit: 100, requestSeq: 1, resetVersion: 1, columnWidths: {},
      }
      const selectedObject = {
        key: 'model_table:model_table:sales.orders', resourceId: 'model_table:sales.orders', layer: 'model_table', modelId: 'sales', table: 'orders', title: 'orders',
        description: 'One row per order.', grain: 'order_id', columnCount: 2, rowCountLabel: '10',
        columns: [
          { key: 'order_id', label: 'Order ID', type: 'string' },
          { key: 'status', label: 'Status', type: 'string' },
        ],
      }
      const customersObject = {
        key: 'model_table:model_table:sales.customers', resourceId: 'model_table:sales.customers', layer: 'model_table', modelId: 'sales', table: 'customers', title: 'customers',
        columnCount: 2, rowCountLabel: '10', columns: [
          { key: 'customer_id', label: 'Customer ID', type: 'string' },
          { key: 'state', label: 'State', type: 'string' },
        ],
      }
      const itemsObject = {
        key: 'model_table:model_table:sales.items', resourceId: 'model_table:sales.items', layer: 'model_table', modelId: 'sales', table: 'items', title: 'items',
        columnCount: 1, rowCountLabel: '10', columns: [{ key: 'sku', label: 'SKU', type: 'string' }],
      }
      const dataExplorer = {
        objects: [selectedObject, customersObject, itemsObject], selectedKey: selectedObject.key, selectedObject, preview: {
          columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, totalRowLabel: 'Unknown', sort: {},
        },
        command: { mode: 'explore', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: exploreCommand },
        explore: {
          command: exploreCommand,
          models: [{ id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', grain: 'order_id', fieldCount: 3 }] }],
          datasets: [{ id: 'orders', title: 'Orders', grain: 'order_id', fieldCount: 3 }],
          selectedModel: { id: 'sales', title: 'Sales', datasets: [{ id: 'orders', title: 'Orders', grain: 'order_id', fieldCount: 3 }] },
          selectedDataset: { id: 'orders', title: 'Orders', grain: 'order_id', fieldCount: 3 },
          fields: [
            { id: 'orders.order_id', label: 'Order ID', kind: 'dimension', modelTable: 'orders', type: 'string', compatible: true, selected: false },
            { id: 'orders.status', label: 'Status', kind: 'dimension', modelTable: 'orders', type: 'string', compatible: true, selected: true },
            { id: 'customers.customer_id', label: 'Customer ID', kind: 'dimension', modelTable: 'customers', type: 'string', compatible: true, relationshipPath: ['orders_customers'], selected: false },
            { id: 'customers.state', label: 'State', kind: 'dimension', modelTable: 'customers', type: 'string', compatible: true, relationshipPath: ['orders_customers'], selected: false },
            { id: 'items.sku', label: 'SKU', kind: 'dimension', modelTable: 'items', type: 'string', compatible: false, compatibilityReason: 'Not available from Orders because no grain-preserving relationship path reaches Items.', selected: false },
            { id: 'revenue', label: 'Revenue', kind: 'metric', modelTable: 'orders', type: 'sum', compatible: true, selected: true },
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

      const table = root.querySelector('lv-data-explore-table') as any
      await table.updateComplete
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
        ...exploreCommand, datasetId: 'customers', dimensions: ['customers.state'], metrics: [], sort: [], requestSeq: 100, resetVersion: 100,
      }
      const customerExplorer = {
        ...dataExplorer,
        selectedKey: customersObject.key,
        selectedObject: customersObject,
        command: { ...dataExplorer.command, objectKey: customersObject.key, explore: customerCommand },
        explore: {
          ...dataExplorer.explore,
          command: customerCommand,
          selectedDataset: { id: 'customers', title: 'Customers', grain: 'customer_id', fieldCount: 1 },
          fields: [
            { id: 'orders.order_id', label: 'Order ID', kind: 'dimension', modelTable: 'orders', type: 'string', compatible: false, rebaseDatasetId: 'orders', compatibilityReason: 'Select Order ID and change grain from Customers to Orders.', selected: false },
            { id: 'orders.status', label: 'Status', kind: 'dimension', modelTable: 'orders', type: 'string', compatible: false, rebaseDatasetId: 'orders', compatibilityReason: 'Select Status and change grain from Customers to Orders.', selected: false },
            { id: 'customers.state', label: 'State', kind: 'dimension', modelTable: 'customers', type: 'string', compatible: true, selected: true },
            { id: 'items.sku', label: 'SKU', kind: 'dimension', modelTable: 'items', type: 'string', compatible: false, compatibilityReason: 'No safe base supports this field with the selection.', selected: false },
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
        unavailableField,
        rebaseField: { disabled: rebaseField.disabled, text: rebaseField.textContent?.replace(/\s+/g, ' ').trim(), title: rebaseField.title },
        rebaseCommand,
        tableSelectionCommand,
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
    expect(state.relatedField.disabled).toBe(false)
    expect(state.relatedField.text).toContain('related')
    expect(state.relatedField.title).toContain('orders_customers')
    expect(state.unavailableField.disabled).toBe(true)
    expect(state.unavailableField.text).toContain('unavailable')
    expect(state.unavailableField.title).toContain('no grain-preserving relationship path')
    expect(state.rebaseField.disabled).toBe(false)
    expect(state.rebaseField.text).toContain('changes grain')
    expect(state.rebaseField.title).toContain('change grain from Customers to Orders')
    expect(state.rebaseCommand.datasetId).toBe('customers')
    expect(state.rebaseCommand.dimensions).toEqual(['customers.state', 'orders.status'])
    expect(state.tableSelectionCommand.datasetId).toBe('customers')
    expect(state.tableSelectionCommand.dimensions).toEqual(['customers.customer_id', 'customers.state'])
    expect(state.tableSelectionCommand.metrics).toEqual([])
    expect(state.commands.some((command) => command.explore?.dimensions?.includes('items.sku'))).toBe(false)
    expect(state.commands.some((command) => command.mode === 'explore' && command.explore?.dimensions?.includes('orders.order_id'))).toBe(true)
    expect(state.commands.some((command) => command.explore?.filters?.[0]?.field === 'orders.status' && command.explore.filters[0].values[0] === 'delivered')).toBe(true)
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
