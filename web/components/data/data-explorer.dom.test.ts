import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testVisualizationEnvelopes } from '../dashboard/dashboard-page-test-fixtures'
import { assertDataExplorerResponsiveDrawers } from '../../test/data-explorer-responsive'
import { assertExploreTablePresentation } from '../../test/explore-table-presentation'
import { assertExploreTablePresentationReset } from '../../test/explore-table-presentation-reset'

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

test('native table clears drag widths on saved baseline and explicit presentation resets', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await assertExploreTablePresentationReset(page)
  } finally { await page.close() }
})

test('native exploration table honors authored presentation without copying window rows', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await assertExploreTablePresentation(page)
  } finally { await page.close() }
})

test('dashboard handoff keeps a compact return link through Explorer URL edits', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/?returnTo=%2Fdashboards%2Fdashboard%3Aexecutive-sales%2Fpages%2Foverview`)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] } })
      const explorer = document.createElement('lv-data-explorer') as any
      document.body.append(explorer)
      await explorer.updateComplete
      const before = (explorer.shadowRoot.querySelector('.return-link') as HTMLAnchorElement | null)?.getAttribute('href')
      const historyLength = window.history.length
      explorer.syncDataExplorerURL({ mode: 'browse', objectKey: 'model:orders' })
      await explorer.updateComplete
      const after = (explorer.shadowRoot.querySelector('.return-link') as HTMLAnchorElement | null)?.getAttribute('href')
      const search = window.location.search
      explorer.remove()
      return { before, after, search, historyLength, updatedHistoryLength: window.history.length }
    })
    expect(state.before).toBe('/dashboards/dashboard:executive-sales/pages/overview')
    expect(state.after).toBe(state.before)
    expect(new URLSearchParams(state.search).get('returnTo')).toBe(state.before ?? null)
    expect(new URLSearchParams(state.search).get('object')).toBe('model:orders')
    expect(state.updatedHistoryLength).toBe(state.historyLength)
  } finally { await page.close() }
})

test('Share menu appends a fresh canonical exploration through an inline authored-dashboard picker', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  const captured: { body?: Record<string, unknown>; operationID?: string } = {}
  let detailLoads = 0
  let appendAttempts = 0
  await page.route('**/explore/dashboard-targets**', async (route) => {
    const url = new URL(route.request().url())
    if (decodeURIComponent(url.pathname).endsWith('/dashboard:authored-sales')) {
      detailLoads += 1
      await route.fulfill({ json: {
        id: 'dashboard:authored-sales', title: 'Sales', semanticModel: 'semantic-model:sales', draftId: 'draft-authored-sales',
        revisionToken: detailLoads === 1 ? 'opaque-revision' : 'fresh-revision', pages: [{ id: 'overview', title: 'Overview' }],
      } })
    } else {
      await route.fulfill({ json: { items: [{ id: 'dashboard:authored-sales', title: 'Sales', semanticModel: 'semantic-model:sales' }] } })
    }
  })
  await page.route('**/explore/add-to-dashboard', async (route) => {
    captured.body = route.request().postDataJSON() as Record<string, unknown>
    captured.operationID = route.request().headers()['x-leapview-operation-id']
    appendAttempts += 1
    if (appendAttempts === 1) {
      await route.fulfill({ status: 409, body: 'conflict' })
      return
    }
    await route.fulfill({ status: 201, json: { dashboardId: 'dashboard:authored-sales', revision: { revisionId: 'revision-2', number: 2, contentHash: `sha256:${'b'.repeat(64)}` } } })
  })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const spec = { schemaVersion: 1, modelId: 'semantic-model:sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [{ field: 'orders.revenue' }], filters: [], sort: [], limit: 100 }
      const exploreCommand = { spec, semanticModelId: spec.modelId, datasetId: spec.datasetId, dimensions: ['orders.status'], metrics: ['orders.revenue'], filters: [], sort: [], limit: 100, requestSeq: 1, resetVersion: 0, columnWidths: {} }
      const explore = {
        command: exploreCommand, semanticModels: [{ id: spec.modelId, title: 'Sales', datasets: [] }], datasets: [],
        fields: [
          { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true },
          { id: 'orders.revenue', label: 'Revenue', kind: 'metric', datasetId: 'orders', type: 'decimal', compatible: true, selected: true },
        ],
        result: { columns: [{ key: 'status' }, { key: 'revenue' }], rows: [{ status: 'paid', revenue: 25 }], rowsReturned: 1, durationMs: 4, requestSeq: 1, truncated: false, warnings: [] },
        status: { state: 'success', requestSeq: 1, loading: false, stale: false },
      }
      mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] }, dataExplorer: {
        objects: [], selectedKey: '', command: { mode: 'explore', objectKey: '', explore: exploreCommand }, explore,
        preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} }, warnings: [],
      } })
      const element = document.createElement('lv-data-explorer')
      element.setAttribute('data-dashboard-targets-url', '/explore/dashboard-targets')
      element.setAttribute('data-dashboard-append-url', '/explore/add-to-dashboard')
      element.setAttribute('data-dashboard-append-operation-id', 'executeDashboardAuthoringCommand')
      document.body.append(element)
    })
    const explorer = page.locator('lv-data-explorer')
    await explorer.locator('summary[aria-label="Share or export exploration"]').click()
    const picker = explorer.locator('.dashboard-append-picker')
    await picker.locator('summary').click()
    await page.waitForFunction(() => document.querySelector('lv-data-explorer')?.shadowRoot?.querySelector('option[value="dashboard:authored-sales"]'))
    await picker.getByLabel('Choose dashboard').selectOption('dashboard:authored-sales')
    await page.waitForFunction(() => document.querySelector('lv-data-explorer')?.shadowRoot?.querySelector<HTMLSelectElement>('[aria-label="Choose dashboard page"]')?.value === 'overview')
    const position = await picker.locator('.dashboard-append-picker-panel').evaluate((node) => getComputedStyle(node).position)
    expect(position).toBe('static')
    await picker.getByRole('button', { name: 'Add tile' }).click()
    await page.waitForFunction(() => document.querySelector('lv-data-explorer')?.shadowRoot?.querySelector('[role="status"]')?.textContent?.includes('target was refreshed'))
    expect(detailLoads).toBe(2)
    await picker.getByRole('button', { name: 'Add tile' }).click()
    await page.waitForFunction(() => document.querySelector('lv-data-explorer')?.shadowRoot?.querySelector('[role="status"]')?.textContent?.includes('Exploration added as an independent tile.'))
    expect(captured.operationID).toBe('executeDashboardAuthoringCommand')
    expect(appendAttempts).toBe(2)
    expect(captured.body).toMatchObject({
      dashboardId: 'dashboard:authored-sales', pageId: 'overview', revisionToken: 'fresh-revision', placementChoice: 'half',
      spec: { modelId: 'semantic-model:sales', dimensions: [{ field: 'orders.status' }], metrics: [{ field: 'orders.revenue' }] },
    })
    expect(await picker.locator('[role="status"] a').getAttribute('href')).toBe('/dashboards/dashboard%3Aauthored-sales/edit?draft=draft-authored-sales')
  } finally {
    await page.close()
  }
})

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
      for (let index = 0; index < 20 && !(element.shadowRoot as ShadowRoot)?.querySelector('lv-data-preview-table'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const root = (element.shadowRoot as ShadowRoot)
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
      const firstHeader = (grid.shadowRoot as ShadowRoot).querySelector('.header-cell button') as HTMLButtonElement
      firstHeader.click()
      const resizer = (grid.shadowRoot as ShadowRoot).querySelector('.column-resizer') as HTMLElement
      resizer.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, clientX: 160 }))
      document.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, clientX: 230 }))
      await new Promise((resolve) => requestAnimationFrame(resolve))
      document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, clientX: 230 }))
      const scrollport = (grid.shadowRoot as ShadowRoot).querySelector('.scrollport') as HTMLDivElement
      scrollport.scrollTop = 9000
      scrollport.dispatchEvent(new Event('scroll'))
      await new Promise((resolve) => setTimeout(resolve, 80))
      const cellRect = ((grid.shadowRoot as ShadowRoot).querySelector('.cell') as HTMLElement).getBoundingClientRect()
      const tableRect = ((grid.shadowRoot as ShadowRoot).querySelector('.plane') as HTMLElement).getBoundingClientRect()
      const selectedNodeRevealedOnHydration = Boolean(root.querySelector('.object-button.is-selected')?.closest('.object-node')?.hasAttribute('open'))
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
        selectedNodeRevealedOnHydration,
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
        tableFooterHeight: Math.round(((grid.shadowRoot as ShadowRoot).querySelector('.footer') as HTMLElement).getBoundingClientRect().height),
        tableFooterDisplay: getComputedStyle((grid.shadowRoot as ShadowRoot).querySelector('.footer') as HTMLElement).display,
        tableFooterText: (grid.shadowRoot as ShadowRoot).querySelector('.footer')?.textContent?.replace(/\s+/g, ' ').trim(),
        tableToolbarHeight: Math.round(((grid.shadowRoot as ShadowRoot).querySelector('.toolbar') as HTMLElement).getBoundingClientRect().height),
        sidebarCollapsed,
        widthBeforeKeyboardResize,
        widthAfterKeyboardResize,
        tableWidthBeforeCollapse,
        tableWidthAfterCollapse,
        togglePositionBeforeCollapse: { x: Math.round(togglePositionBeforeCollapse.x), y: Math.round(togglePositionBeforeCollapse.y) },
        togglePositionAfterCollapse: { x: Math.round(togglePositionAfterCollapse.x), y: Math.round(togglePositionAfterCollapse.y) },
        collapsedToggleLabel,
        rowCount: (grid.shadowRoot as ShadowRoot).querySelectorAll('.row[role="row"]').length,
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
    expect(state.selectedFieldStates).toEqual(['true', 'false'])
    expect(state.selectedNodeText).not.toContain('olist · orders')
    expect(state.selectedNodeText).toBe('orders')
    expect(state.selectedNodeSubtitle).toBe('orders')
    expect(state.selectedNodeRevealedOnHydration).toBe(true)
    expect(state.rowClickExpanded).toBe(false)
    expect(state.expandClickExpanded).toBe(true)
    expect(state.resourceSummaries).toContain('olist (2)')
    expect(state.resourceIcons).toEqual(['Project resource'])
    expect(state.columnSearchMatchCount).toBe(2)
    expect(state.columnSearchMatchesOpen).toBe(true)
    expect(state.hasHeaderColumnsControl).toBe('Columns1/2')
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
    expect(state.commands.every((command) => typeof command.clientId === 'string' && command.clientId.startsWith('explorer-'))).toBe(true)
    expect(state.commands.some((command) => command.objectKey === 'model:model:olist.customers' && command.visibleColumns?.length === 0 && Object.keys(command.columnWidths ?? {}).length === 0)).toBe(true)
    expect(state.commands.some((command) => command.sort?.column === 'order_id')).toBe(true)
    expect(state.commands.some((command) => command.visibleColumns?.length === 1 && command.visibleColumns[0] === 'order_id')).toBe(true)
    expect(state.commands.some((command) => command.objectKey === 'model:model:olist.orders' && command.columnWidths?.order_id > 200)).toBe(true)
    expect(state.commands.some((command) => command.block && command.start > 0 && command.count === 100 && command.requestSeq > 0)).toBe(true)
  } finally {
    await page.close()
  }
})

test('Rows filter dock applies a filter without switching to Analyze', async () => {
  const page = await browser.newPage({ viewport: { width: 420, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-explorer-query-controls'))
    await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const object = { key: 'model:zip', resourceId: 'model:zip', layer: 'model', semanticModelId: 'semantic-model:visuals', datasetId: 'zip_geolocations', title: 'ZIP locations', columnCount: 2, columns: [{ key: 'city' }, { key: 'state' }] }
      const spec = { schemaVersion: 1, modelId: 'semantic-model:visuals', datasetId: 'zip_geolocations', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 }
      const fields = [
        { id: 'zip_geolocations.state', label: 'State', kind: 'dimension', datasetId: 'zip_geolocations', type: 'string', compatible: true, selected: false },
        ...Array.from({ length: 16 }, (_, index) => ({ id: `zip_geolocations.field_${index + 1}`, label: `Field ${index + 1}`, kind: 'dimension', datasetId: 'zip_geolocations', type: 'string', compatible: true, selected: false })),
      ]
      const explore = { command: { spec, semanticModelId: spec.modelId, datasetId: spec.datasetId, dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }, semanticModels: [], datasets: [], fields, result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] }, status: { loading: false, stale: false, requestSeq: 0, state: 'idle' } }
      const command = { mode: 'browse', objectKey: object.key, explore: explore.command, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] }, dataExplorer: { objects: [object], selectedKey: object.key, selectedObject: object, command, explore, preview: { columns: object.columns, totalRows: 1, availableRows: 1, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: { a: { start: 0, requestSeq: 0, resetVersion: 0, sort: {}, rows: [{ city: 'sao paulo', state: 'SP' }] } }, totalRowLabel: '1', sort: {}, error: '' }, warnings: [] } })
      ;(window as any).rowFilterCommands = []
      element.addEventListener('lv-data-explorer-command', (event: Event) => (window as any).rowFilterCommands.push((event as CustomEvent).detail))
      document.body.append(element)
    })
    const explorer = page.locator('lv-data-explorer')
    const narrowLayout = await explorer.evaluate((host) => {
      const root = (host as HTMLElement).shadowRoot!
      const route = root.querySelector('.route')!.getBoundingClientRect()
      const results = root.querySelector('.main')!.getBoundingClientRect()
      return { routeHeight: route.height, resultsTop: results.top, collapsed: root.querySelector('.explorer')?.classList.contains('browser-collapsed') }
    })
    expect(narrowLayout.collapsed).toBe(true)
    expect(narrowLayout.routeHeight).toBeLessThanOrEqual(620)
    expect(narrowLayout.resultsTop).toBeLessThan(300)
    await explorer.getByRole('button', { name: 'Open data browser' }).click()
    expect(await explorer.locator('.explorer').evaluate((node) => node.classList.contains('browser-collapsed'))).toBe(false)
    await explorer.getByRole('button', { name: 'Close data browser' }).click()
    expect(await explorer.locator('.explorer').evaluate((node) => node.classList.contains('browser-collapsed'))).toBe(true)
    await explorer.getByRole('button', { name: 'Filters', exact: true }).click()
    await explorer.getByLabel('Add filter', { exact: true }).click()
    const chooser = explorer.locator('.semantic-filter-options')
    const chooserLayout = await explorer.evaluate((host) => {
      const root = (host as HTMLElement).shadowRoot!
      const options = root.querySelector('.semantic-filter-options') as HTMLElement
      const editor = root.querySelector('.semantic-filter-body lv-data-explorer-query-controls') as HTMLElement
      const optionsRect = options.getBoundingClientRect()
      return {
        maxHeight: options.clientHeight,
        scrollHeight: options.scrollHeight,
        optionsBottom: optionsRect.bottom,
        editorTop: editor.getBoundingClientRect().top,
      }
    })
    expect(chooserLayout.maxHeight).toBeLessThanOrEqual(200)
    expect(chooserLayout.scrollHeight).toBeGreaterThan(chooserLayout.maxHeight)
    expect(chooserLayout.editorTop).toBeGreaterThanOrEqual(chooserLayout.optionsBottom)
    await explorer.locator('.semantic-filter-header strong').click()
    expect(await explorer.locator('.semantic-filter-picker').evaluate((picker) => (picker as HTMLDetailsElement).open)).toBe(false)
    expect(await explorer.locator('.semantic-filter-panel').count()).toBe(1)
    const chooserSummary = explorer.getByLabel('Add filter', { exact: true })
    await chooserSummary.click()
    await chooserSummary.press('Escape')
    expect(await explorer.locator('.semantic-filter-picker').evaluate((picker) => (picker as HTMLDetailsElement).open)).toBe(false)
    expect(await explorer.locator('.semantic-filter-panel').count()).toBe(1)
    await chooserSummary.click()
    await chooser.getByRole('button', { name: 'State, zip_geolocations', exact: true }).click()
    const editor = explorer.locator('lv-data-explorer-query-controls')
    await editor.getByLabel('Value').fill('SP')
    await editor.getByRole('button', { name: 'Apply' }).click()
    const command = await page.evaluate(() => (window as any).rowFilterCommands.at(-1))
    expect(command.mode).toBe('browse')
    expect(command.explore.spec.filters).toHaveLength(1)
    expect(command.explore.spec.filters[0].field).toBe('zip_geolocations.state')
    expect(new URL(page.url()).searchParams.get('mode')).toBe('browse')
  } finally {
    await page.close()
  }
})

test('Rows field checkboxes only change visible preview columns', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const object = { key: 'model:zip', resourceId: 'model:zip', layer: 'model', semanticModelId: 'semantic-model:visuals', datasetId: 'zip_geolocations', title: 'ZIP locations', columnCount: 2, columns: [{ key: 'city', label: 'City', type: 'string' }, { key: 'state', label: 'State', type: 'string' }] }
      const exploreCommand = { semanticModelId: 'semantic-model:visuals', datasetId: 'zip_geolocations', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }
      const command = { mode: 'browse', objectKey: object.key, explore: exploreCommand, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] }, dataExplorer: { objects: [object], selectedKey: object.key, selectedObject: object, command, explore: { command: exploreCommand, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] } }, preview: { columns: object.columns, totalRows: 1, availableRows: 1, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: { a: { start: 0, requestSeq: 0, resetVersion: 0, sort: {}, rows: [{ city: 'sao paulo', state: 'SP' }] } }, totalRowLabel: '1', sort: {}, error: '' }, warnings: [] } })
      ;(window as any).rowColumnCommands = []
      element.addEventListener('lv-data-explorer-command', (event: Event) => (window as any).rowColumnCommands.push((event as CustomEvent).detail))
      document.body.append(element)
    })
    const explorer = page.locator('lv-data-explorer')
    await explorer.locator('.object-node').evaluate((node) => (node as HTMLDetailsElement).open = true)
    await explorer.locator('.object-node .field-button').first().click()
    const immediate = await explorer.evaluate(async (host) => {
      const element = host as any
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const preview = root.querySelector('lv-data-preview-table') as any
      return {
        columnCount: root.querySelector('.header-columns summary')?.textContent?.replace(/\s+/g, ' ').trim(),
        selected: root.querySelector('.object-node .field-button')?.classList.contains('is-selected'),
        previewColumns: preview.command.visibleColumns,
      }
    })
    expect(immediate.columnCount).toContain('1/2')
    expect(immediate.selected).toBe(false)
    expect(immediate.previewColumns).toEqual(['state'])
    await page.waitForFunction(() => (window as any).rowColumnCommands.length > 0)
    const command = await page.evaluate(() => (window as any).rowColumnCommands.at(-1))
    expect(command.mode).toBe('browse')
    expect(command.visibleColumns).toEqual(['state'])
    expect(new URL(page.url()).searchParams.get('mode')).not.toBe('explore')
    await explorer.locator('.object-node .field-button').first().click()
    const restored = await explorer.evaluate(async (host) => {
      const element = host as any
      await element.updateComplete
      return {
        columnCount: element.shadowRoot.querySelector('.header-columns summary')?.textContent?.replace(/\s+/g, ' ').trim(),
        previewColumns: element.shadowRoot.querySelector('lv-data-preview-table')?.command.visibleColumns,
      }
    })
    expect(restored.columnCount).toContain('2/2')
    expect(restored.previewColumns).toEqual([])
    await page.waitForFunction(() => (window as any).rowColumnCommands.length > 1)
    expect(await page.evaluate(() => (window as any).rowColumnCommands.at(-1).mode)).toBe('browse')
  } finally {
    await page.close()
  }
})

test('data explorer lists earlier and revisioned saves in one picker', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [], preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
          command: { mode: 'browse', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
          explore: { command: { semanticModelId: '', datasetId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], warnings: [] } },
          warnings: [],
        },
        savedExplorations: { enabled: true, list: {
          items: [], includeArchived: false,
          legacyItems: [{ id: 'explore_1', name: 'Orders by month', openHref: '/explore?saved=explore_1' }],
        }, command: { action: 'create' }, save: { state: 'idle' } },
      })
      document.body.append(document.createElement('lv-data-explorer'))
    })
    const picker = page.locator('.saved-exploration-picker')
    await picker.locator('summary').click()
    expect(await picker.locator('summary').textContent()).toContain('Saved explorations (1)')
    const earlier = picker.getByRole('link', { name: 'Orders by month', exact: true })
    expect(await earlier.isVisible()).toBe(true)
    expect(await earlier.getAttribute('href')).toBe('/explore?saved=explore_1')
    expect(await picker.locator('.saved-exploration-list').evaluate((node) => getComputedStyle(node).position)).toBe('absolute')
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ savedExplorations: { list: { items: [{ id: 'exploration:new', title: 'Orders by status', status: 'active' }] } } })
    })
    await picker.getByRole('link', { name: 'Orders by status', exact: true }).waitFor({ state: 'visible' })
    expect(await picker.locator('summary').textContent()).toContain('Saved explorations (2)')
    expect(await earlier.isVisible()).toBe(true)
    expect(await picker.getByRole('link', { name: 'Orders by status', exact: true }).getAttribute('href')).toBe('/explore/saved/exploration%3Anew?navigation=true')
    expect(await page.locator('.saved-exploration-picker').count()).toBe(1)
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
        semanticModelId: 'semantic:sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [],
        limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {},
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
      return Array.from((element.shadowRoot as ShadowRoot)?.querySelectorAll<HTMLElement>('.object-button') ?? []).map((button) => ({
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
          explore: { command: { dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], warnings: [] } },
          warnings: [],
        },
      })
      document.body.append(element)
      for (let index = 0; index < 10; index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      return (element.shadowRoot as ShadowRoot)?.querySelector('.main .empty')?.textContent?.trim()
    })

    expect(message).toBe('Select a data object to begin.')
  } finally {
    await page.close()
  }
})

test('clearing the time field removes it from the live exploration URL', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-explorer-query-controls'))
    await page.evaluate(async () => {
      const object = {
        key: 'model:orders', resourceId: 'model:orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders',
        title: 'Orders', columnCount: 2, columns: [{ key: 'status', label: 'Status' }],
      }
      const spec = {
        schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }],
        metrics: [], filters: [], sort: [], limit: 50, time: { field: 'orders.purchase_date', grain: 'month' },
      }
      const command = {
        spec, semanticModelId: 'sales', datasetId: 'orders', dimensions: ['orders.status'], metrics: [], filters: [], sort: [],
        time: { field: 'orders.purchase_date', grain: 'month' }, limit: 50, requestSeq: 1, resetVersion: 1, columnWidths: {},
      }
      const dataset = { id: 'orders', title: 'Orders', grainEntity: 'order_id', grainFields: ['order_id'], fieldCount: 2, entities: [] }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [object], selectedKey: object.key, selectedObject: object,
          command: { mode: 'explore', objectKey: object.key, explore: command },
          explore: {
            command, semanticModels: [{ id: 'sales', title: 'Sales', datasets: [dataset] }],
            datasets: [dataset], selectedSemanticModel: { id: 'sales', title: 'Sales', datasets: [dataset] }, selectedDataset: dataset,
            fields: [
              { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true },
              { id: 'orders.purchase_date', label: 'Purchase date', kind: 'dimension', datasetId: 'orders', type: 'date', compatible: true, selected: false },
            ],
            result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 1, truncated: false, warnings: [] },
          },
        },
      })
      document.body.append(document.createElement('lv-data-explorer'))
    })
    await page.locator('lv-data-explorer .semantic-filter-rail').click()
    const controls = page.locator('lv-data-explorer .semantic-filter-dock lv-data-explorer-query-controls:not([filtereditoronly])')
    await controls.locator('.query-config summary').click()
    await controls.getByRole('combobox', { name: 'Time field' }).selectOption('')
    const encoded = JSON.parse(new URL(page.url()).searchParams.get('state')!)
    expect(encoded.time).toBeUndefined()
  } finally {
    await page.close()
  }
})

test('governed result views switch locally and disappear when the result becomes stale', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const chartEnvelope = testVisualizationEnvelopes().orders_chart
    if (chartEnvelope.dataState.kind !== 'inline') throw new Error('Expected inline chart fixture')
    chartEnvelope.dataState.datasets[0].rows = Array.from({ length: 55 }, (_, index) => [`Status ${index + 1}`, index + 1])
    const state = await page.evaluate(async (chart) => {
      const element = document.createElement('lv-data-explorer') as any
      const spec = { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [{ field: 'revenue' }], filters: [], sort: [], limit: 100 }
      const command = { spec, semanticModelId: 'sales', datasetId: 'orders', dimensions: ['orders.status'], metrics: ['revenue'], filters: [], sort: [], limit: 100, requestSeq: 1, resetVersion: 0, columnWidths: {} }
      const object = { key: 'model:orders:sales', resourceId: 'model:orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1, columns: [{ key: 'status', label: 'Status' }] }
      const explore = {
        command, semanticModels: [{ id: 'sales', title: 'Sales', datasets: [] }], datasets: [], fields: [
          { id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true, selected: true },
          { id: 'revenue', label: 'Revenue', kind: 'metric', datasetId: 'orders', type: 'decimal', compatible: true, selected: true },
        ],
        result: { columns: [{ key: 'status', label: 'Status' }, { key: 'revenue', label: 'Revenue' }], rows: [{ status: 'delivered', revenue: 42 }], rowsReturned: 1, durationMs: 4, requestSeq: 1, truncated: false, warnings: [], sql: 'select status, sum(revenue)' },
        status: { state: 'success', requestSeq: 1, loading: false, stale: false }, views: { chart }, recommendedView: 'chart',
      }
      const dataExplorer = {
        objects: [object], selectedKey: object.key, selectedObject: object,
        preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, sort: {} },
        command: { mode: 'explore', objectKey: object.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 1, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: command },
        explore, warnings: [],
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] }, dataExplorer })
      document.body.append(element)
      const root = element.shadowRoot as ShadowRoot
      for (let index = 0; index < 20 && !root?.querySelector('lv-data-explore-table'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const viewButtons = () => Array.from(root.querySelectorAll<HTMLButtonElement>('[aria-label="Result views"] button'))
      const initial = { buttons: viewButtons().map((button) => button.textContent?.trim()), table: Boolean(root.querySelector('lv-data-explore-table')) }
      viewButtons().find((button) => button.textContent?.trim() === 'Chart')?.click()
      await element.updateComplete
      const chartHost = root.querySelector('lv-visualization-host') as HTMLElement | null
      await (chartHost as any)?.updateComplete
      const chartView = {
        host: Boolean(chartHost),
        table: Boolean(root.querySelector('lv-data-explore-table')),
        actions: chartHost?.shadowRoot?.querySelectorAll('.visual-actions button, .visual-options summary').length ?? 0,
      }
      const categoryPages = root.querySelector<HTMLElement>('[aria-label="Chart category pages"]')!
      const previous = categoryPages.querySelector<HTMLButtonElement>('[aria-label="Previous chart categories"]')!
      const next = categoryPages.querySelector<HTMLButtonElement>('[aria-label="Next chart categories"]')!
      const readPage = () => ({
        label: categoryPages.querySelector('[role="status"]')?.textContent?.trim(),
        previousDisabled: previous.disabled, nextDisabled: next.disabled,
      })
      const firstPage = readPage()
      const chartRegion = root.querySelector<HTMLElement>('[aria-label="Chart results"]')!
      const readability = {
        height: chartHost!.getBoundingClientRect().height,
        viewportHeight: chartRegion.clientHeight,
        scrollHeight: chartRegion.scrollHeight,
        overflow: getComputedStyle(chartRegion).overflowY,
      }
      next.click()
      await element.updateComplete
      const lastPage = readPage()
      previous.click()
      await element.updateComplete
      const returnedPage = readPage()
      mergePatch({ dataExplorer: { explore: { views: {}, status: { state: 'stale', requestSeq: 2, loading: false, stale: true } } } })
      await element.updateComplete
      const stale = { host: Boolean(root.querySelector('lv-visualization-host')), table: Boolean(root.querySelector('lv-data-explore-table')), buttons: viewButtons().map((button) => button.textContent?.trim()) }
      mergePatch({ dataExplorer: { explore: { views: {}, result: { requestSeq: 2, rows: [{ status: 'shipped', revenue: 7 }] }, status: { state: 'success', requestSeq: 2, loading: false, stale: false } } } })
      await element.updateComplete
      return { initial, chartView, firstPage, lastPage, returnedPage, readability, stale, nextRunWithoutChart: { host: Boolean(root.querySelector('lv-visualization-host')), table: Boolean(root.querySelector('lv-data-explore-table')) } }
    }, chartEnvelope)
    expect(state.initial).toEqual({ buttons: ['Table', 'Chart', 'SQL / Details'], table: true })
    expect(state.chartView).toEqual({ host: true, table: false, actions: 0 })
    expect(state.firstPage).toEqual({ label: 'Showing 1–50 of 55 categories', previousDisabled: true, nextDisabled: false })
    expect(state.lastPage).toEqual({ label: 'Showing 51–55 of 55 categories', previousDisabled: false, nextDisabled: true })
    expect(state.returnedPage).toEqual(state.firstPage)
    expect(state.readability.height).toBeGreaterThanOrEqual(50 * 32)
    expect(state.readability.height).toBeLessThanOrEqual(4096)
    expect(state.readability.scrollHeight).toBeGreaterThan(state.readability.viewportHeight)
    expect(state.readability.overflow).toBe('auto')
    expect(state.stale).toEqual({ host: false, table: true, buttons: [] })
    expect(state.nextRunWithoutChart).toEqual({ host: false, table: true })
  } finally {
    await page.close()
  }
})

test('data explorer builds a governed semantic exploration and filter command', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-explore-table'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-data-explorer') as any
      const pageSignal = {
        kind: 'data', title: 'Data Explorer', description: 'Inspect or explore data.', tabs: [],
      }
      const exploreCommand = {
        semanticModelId: 'sales', datasetId: 'orders', dimensions: ['orders.status'], metrics: ['revenue'],
        filters: [], sort: [{ field: 'revenue', direction: 'desc' }], limit: 100, requestSeq: 1, resetVersion: 1, columnWidths: {},
      }
      const selectedObject = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'orders',
        description: 'One row per order.', grain: 'order_id', columnCount: 2, rowCountLabel: '10',
        columns: [
          { key: 'order_id', label: 'Order ID', type: 'string' },
          { key: 'status', label: 'Status', type: 'string' },
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
            { id: 'customers.customer_id', label: 'Customer ID', kind: 'dimension', datasetId: 'customers', type: 'string', compatible: true, relationshipPath: ['orders_customers'], selected: false },
            { id: 'operations_customers.customer_id', label: 'Customer ID', kind: 'dimension', datasetId: 'operations_customers', type: 'string', compatible: true, relationshipPath: ['orders_customers'], selected: false },
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
      for (let index = 0; index < 20 && !(element.shadowRoot as ShadowRoot)?.querySelector('.field-button'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }

      const root = (element.shadowRoot as ShadowRoot)
      const resetToTable = root.querySelector<HTMLButtonElement>('[aria-label="Return to all table columns"]')!
      resetToTable.click()
      const resetToTableCommand = commands.at(-1)?.explore
      const customersTable = Array.from(root.querySelectorAll<HTMLElement>('.object-button')).find((button) => button.textContent?.includes('customers'))!
      customersTable.click()
      await element.updateComplete
      const tableSelectionCommand = commands.at(-1)?.explore
      const orderID = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('Order ID'))
      if (!orderID) throw new Error(`Order ID field was not rendered: ${root.textContent}`)
      orderID.click()
      await element.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))

      const statusRow = Array.from(root.querySelectorAll<HTMLElement>('.column-item')).find((row) => row.textContent?.includes('Status'))
      const filterButton = statusRow?.querySelector<HTMLButtonElement>('.field-action')
      if (!filterButton) throw new Error(`Status filter button was not rendered: ${root.textContent}`)
      filterButton.click()
      await element.updateComplete
      const pickerSummary = root.querySelector<HTMLElement>('.semantic-filter-picker > summary')!
      pickerSummary.click()
      const filterChoices = Array.from(root.querySelectorAll<HTMLButtonElement>('.semantic-filter-option')).map((button) => button.getAttribute('aria-label'))
      pickerSummary.click()
      const controls = root.querySelector('.semantic-filter-panel lv-data-explorer-query-controls') as any
      const filterInDock = Boolean(controls) && !root.querySelector('.semantic-result lv-data-explorer-query-controls')
      await controls.updateComplete
      const controlsRoot = controls.shadowRoot as ShadowRoot
      const filterInput = controlsRoot.querySelector<HTMLInputElement>('#filter-value-input')!
      filterInput.value = 'delivered'
      filterInput.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      await controls.updateComplete
      const applyButton = Array.from(controlsRoot.querySelectorAll<HTMLButtonElement>('.filter-editor .text-button')).find((button) => button.textContent?.trim() === 'Apply')
      if (!applyButton) throw new Error(`Apply filter button was not rendered: ${root.textContent}`)
      applyButton.click()
      await element.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))
      root.querySelector<HTMLButtonElement>('.semantic-filter-card button')?.click()
      await element.updateComplete
      await controls.updateComplete
      const editValue = controlsRoot.querySelector<HTMLInputElement>('#filter-value-input')?.value
      Array.from(controlsRoot.querySelectorAll<HTMLButtonElement>('.filter-editor .text-button')).find((button) => button.textContent?.trim() === 'Cancel')?.click()
      await element.updateComplete

      const table = root.querySelector('lv-data-explore-table') as any
      await table.updateComplete
      const resultPane = root.querySelector<HTMLElement>('.semantic-result')!
      const stateField = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('State'))!
      const skuField = Array.from(root.querySelectorAll<HTMLButtonElement>('.field-button')).find((button) => button.textContent?.includes('SKU'))!
      skuField.click()
      const initialState = {
        modes: Array.from(root.querySelectorAll('.mode-button')).map((button) => ({ text: button.textContent?.trim(), pressed: button.getAttribute('aria-pressed') })),
        hasBreadcrumb: Boolean(root.querySelector('[aria-label="Breadcrumb"]')),
        resourceTables: root.querySelector('.resource-group')?.textContent?.replace(/\s+/g, ' ').trim(),
        querySummary: Array.from(root.querySelectorAll('.selected-fields-heading .query-summary')).map((item) => item.textContent?.replace(/\s+/g, ' ').trim()),
        filterChips: Array.from(root.querySelectorAll('.semantic-filter-card')).map((item) => item.textContent?.replace(/\s+/g, ' ').trim()),
        filterChoices,
        editValue,
        filterInDock,
        grain: root.querySelector('.result-meta')?.textContent?.replace(/\s+/g, ' ').trim(),
        tableRows: table.result.rows,
        resultLayout: { overflowY: getComputedStyle(resultPane).overflowY, scrollable: resultPane.scrollHeight > resultPane.clientHeight, tableHeight: table.getBoundingClientRect().height },
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
      const rebaseField = Array.from(root.querySelectorAll<HTMLInputElement>('.semantic-fields .semantic-field input[type="checkbox"]')).find((input) => input.parentElement?.textContent?.includes('Status'))!
      const rebaseFieldState = rebaseField && {
        disabled: rebaseField.disabled,
        text: rebaseField.parentElement?.textContent?.replace(/\s+/g, ' ').trim(),
        title: rebaseField.closest('.semantic-field')?.getAttribute('title'),
        visible: rebaseField.getBoundingClientRect().width > 0,
      }
      rebaseField?.click()
      await element.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const rebaseCommand = commands.at(-1)?.explore
      return {
        ...initialState,
        unavailableField,
        rebaseField: rebaseFieldState,
        rebaseCommand,
        tableSelectionCommand,
        resetToTableCommand,
        commands,
      }
    })

    expect(state.modes).toEqual([
      { text: 'Rows', pressed: 'false' },
      { text: 'Analyze', pressed: 'true' },
    ])
    expect(state.hasBreadcrumb).toBe(false)
    expect(state.resourceTables).toContain('orders')
    expect(state.querySummary).not.toContain('3 columns')
    expect(state.filterChips.some((chip) => chip?.includes('Status') && chip.includes('delivered'))).toBe(true)
    expect(state.filterChoices).toContain('Customer ID, customers')
    expect(state.filterChoices).toContain('Customer ID, operations_customers')
    expect(state.editValue).toBe('delivered')
    expect(state.filterInDock).toBe(true)
    expect(state.grain).not.toContain('Grouped by:')
    expect(state.tableRows).toEqual([{ status: 'delivered', revenue: 1200 }])
    expect(state.resultLayout).toMatchObject({ overflowY: 'auto' })
    expect(state.resultLayout.tableHeight).toBeGreaterThan(200)
    expect(state.relatedField.disabled).toBe(false)
    expect(state.relatedField.text).toContain('related')
    expect(state.relatedField.title).toContain('orders_customers')
    expect(state.unavailableField.disabled).toBe(true)
    expect(state.unavailableField.text).toContain('unavailable')
    expect(state.unavailableField.title).toContain('no grain-preserving relationship path')
    expect(state.rebaseField.disabled).toBe(false)
    expect(state.rebaseField.visible).toBe(true)
    expect(state.rebaseField.title).toContain('change grain from Customers to Orders')
    expect(state.rebaseCommand.spec.datasetId).toBe('orders')
    expect(state.rebaseCommand.spec.dimensions).toEqual([{ field: 'customers.state' }, { field: 'orders.status' }])
    await assertDataExplorerResponsiveDrawers(page)
    expect(state.tableSelectionCommand.datasetId).toBe('customers')
    expect(state.tableSelectionCommand.dimensions).toEqual(['customers.customer_id', 'customers.state'])
    expect(state.tableSelectionCommand.metrics).toEqual([])
    expect(state.resetToTableCommand.dimensions).toEqual(['orders.order_id', 'orders.status'])
    expect(state.resetToTableCommand.metrics).toEqual([])
    expect(state.resetToTableCommand.spec.dimensions.map((dimension: { field: string }) => dimension.field)).toEqual(['orders.order_id', 'orders.status'])
    expect(state.resetToTableCommand.spec.metrics).toEqual([])
    expect(state.commands.some((command) => command.explore?.dimensions?.includes('items.sku'))).toBe(false)
    expect(state.commands.some((command) => command.mode === 'explore' && command.explore?.dimensions?.includes('orders.order_id'))).toBe(true)
    expect(state.commands.some((command) => command.explore?.spec?.filters?.[0]?.field === 'orders.status' && command.explore.spec.filters[0].expression?.value?.value === 'delivered')).toBe(true)
  } finally {
    await page.close()
  }
}, 15_000)

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
      const previewAlert = ((preview.shadowRoot as ShadowRoot)!.querySelector('[role="alert"]') as HTMLElement).textContent!.replace(/\s+/g, ' ').trim()
      const previewButtons = Array.from((preview.shadowRoot as ShadowRoot)!.querySelectorAll<HTMLButtonElement>('.failure button'))
      previewButtons[0].click()
      previewButtons[1].click()

      const object = {
        key: 'model:model:sales.orders', resourceId: 'model:sales.orders', layer: 'model',
        semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1,
        columns: [{ key: 'status', label: 'Status', type: 'string' }],
      }
      const exploreCommand = {
        semanticModelId: 'sales', datasetId: 'orders', dimensions: ['orders.status'], metrics: [], filters: [], sort: [],
        limit: 100, requestSeq: 4, resetVersion: 3, columnWidths: {},
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
      for (let index = 0; index < 20 && !(explorer.shadowRoot as ShadowRoot)?.querySelector('.result-failure'); index += 1) {
        await explorer.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const failure = (explorer.shadowRoot as ShadowRoot)!.querySelector('.result-failure') as any
      const exploreAlert = failure.textContent!.replace(/\s+/g, ' ').trim()
      const exploreButtons = Array.from(failure.querySelectorAll('button')) as HTMLButtonElement[]
      exploreButtons[0].click()
      await explorer.updateComplete
      const refreshedFailure = (explorer.shadowRoot as ShadowRoot)!.querySelector('.result-failure') as HTMLElement
      const refreshedButtons = Array.from(refreshedFailure.querySelectorAll('button')) as HTMLButtonElement[]
      refreshedButtons[1].click()
      await explorer.updateComplete
      return { previewAlert, previewCommands, exploreAlert, exploreCommands }
    })

    expect(state.previewAlert).toContain('Preview timed out.')
    expect(state.previewCommands[0]).toMatchObject({ objectKey: 'orders', requestSeq: 8, resetVersion: 2 })
    expect(state.previewCommands[1]).toMatchObject({ objectKey: 'orders', offset: 0, start: 0, block: 'all', requestSeq: 8, resetVersion: 3, sort: {} })
    expect(state.exploreAlert).toContain('Query service is unavailable.')
    expect(state.exploreCommands[0]).toMatchObject({ action: 'run', explore: { semanticModelId: 'sales', datasetId: 'orders', action: 'run' } })
    expect(state.exploreCommands[1]).toMatchObject({ action: 'configure', explore: { spec: { dimensions: [], metrics: [], filters: [], sort: [] } } })
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
