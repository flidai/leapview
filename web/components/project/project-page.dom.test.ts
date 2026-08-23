import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/project-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(url.searchParams.get('root') ?? 'project'))
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

test('project asset list renders current resource signals and filter event', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=project`)
    await page.waitForFunction(() => customElements.get('lv-project-page'))
    const state = await page.locator('lv-project-page').evaluate(async (element: any) => {
      await element.updateComplete
      let detail: unknown = null
      element.addEventListener('lv-project-asset-filter', (event: CustomEvent) => { detail = event.detail }, { once: true })
      const root = element.shadowRoot!
      const input = root.querySelector('input[type="search"]') as HTMLInputElement
      input.value = 'orders'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const table = root.querySelector('lv-record-table') as any
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        rows: root.querySelectorAll('lv-record-table').length,
        columns: table?.table?.columns?.map((column: any) => column.header),
        firstRow: table?.table?.rows?.[0],
        detail,
      }
    })
    expect(state.title).toBe('Develop')
    expect(state.rows).toBe(1)
    expect(state.columns).toEqual(['Name', 'Type', 'Identifier'])
    expect(state.firstRow.name.description).toBe('Raw orders.')
    expect(state.firstRow.key).toBe('source:orders')
    expect(state.firstRow.actions).toEqual([])
    expect(state.detail).toEqual({ type: 'source', query: 'orders' })
  } finally {
    await page.close()
  }
})

test('fixed project areas keep canonical asset links', async () => {
  for (const [rootName, expectedHref] of [['sources', '/sources/source:orders/details'], ['models', '/models/model:orders/details'], ['semantic-models', '/semantic-models/semantic:orders/details']] as const) {
    const page = await browser.newPage()
    try {
      await page.goto(`${baseURL}/?root=${rootName}`)
      await page.waitForFunction(() => customElements.get('lv-project-page'))
      const links = await page.locator('lv-project-page').evaluate(async (element: any) => {
        await element.updateComplete
        const table = element.shadowRoot?.querySelector('lv-record-table') as any
        await table?.updateComplete
        return {
          links: Array.from(table?.querySelectorAll('a') ?? []).map((link: HTMLAnchorElement) => link.getAttribute('href')),
          filters: element.shadowRoot?.querySelectorAll('select').length ?? 0,
        }
      })
      expect(links.links).toContain(expectedHref)
      expect(links.filters).toBe(0)
    } finally {
      await page.close()
    }
  }
})

test('semantic model asset details use the Waypoints SVG identity', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=semantic-detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const icon = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return element.shadowRoot?.querySelector('h1 .asset-glyph svg')?.innerHTML ?? ''
    })
    expect(icon).toContain('M6 12h12')
    expect(icon).not.toContain('M21 8a2 2 0 00-1-1.73')
  } finally {
    await page.close()
  }
})

test('asset data section embeds the shared explorer without a duplicate route header', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 } })
  try {
    await page.goto(`${baseURL}/?root=model-data`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page') && customElements.get('lv-data-explorer'))
    const data = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const explorer = element.shadowRoot?.querySelector('lv-data-explorer') as any
      await explorer?.updateComplete
      const preview = explorer?.shadowRoot?.querySelector('lv-data-preview-table') as any
      await preview?.updateComplete
      const table = preview?.shadowRoot?.querySelector('lv-windowed-table') as any
      await table?.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await table?.updateComplete
      const scrollport = table?.shadowRoot?.querySelector('.scrollport') as HTMLElement | null
      explorer?.emitCommand({ objectKey: 'orders' })
      return {
        embedded: explorer?.hasAttribute('embedded') ?? false,
        routeHeaders: explorer?.shadowRoot?.querySelectorAll('.header').length ?? 0,
        visibleRouteHeaders: Array.from(explorer?.shadowRoot?.querySelectorAll('.header') ?? []).filter((node: any) => getComputedStyle(node).display !== 'none').length,
        browserVisible: getComputedStyle(explorer?.shadowRoot?.querySelector('.browser') as Element).display !== 'none',
        pathname: window.location.pathname,
        assetHeight: Math.round(element.getBoundingClientRect().height),
        viewportHeight: scrollport?.clientHeight ?? 0,
        renderedRows: table?.shadowRoot?.querySelectorAll('.canvas > .row').length ?? 0,
        windowHeight: window.innerHeight,
      }
    })
    expect(data.embedded).toBe(true)
    expect(data.routeHeaders).toBe(1)
    expect(data.visibleRouteHeaders).toBe(0)
    expect(data.browserVisible).toBe(false)
    expect(data.pathname).toBe('/')
    expect(data.assetHeight).toBeLessThanOrEqual(data.windowHeight)
    expect(data.viewportHeight).toBeGreaterThan(0)
    expect(data.viewportHeight).toBeLessThan(data.windowHeight)
    expect(data.renderedRows).toBeLessThan(100)
  } finally {
    await page.close()
  }
})

test('connections list and asset detail render without workspace terminology', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=connections`)
    await page.waitForFunction(() => customElements.get('lv-connections-page'))
    const connections = await page.locator('lv-connections-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return { title: root.querySelector('h1')?.textContent?.trim(), rows: root.querySelectorAll('tbody tr').length, text: root.textContent }
    })
    expect(connections.title).toBe('Connections')
    expect(connections.rows).toBe(1)
    expect(connections.text.toLowerCase()).not.toContain('workspace')

    await page.goto(`${baseURL}/?root=detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const detail = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return { title: root.querySelector('h1')?.textContent?.trim(), tabs: Array.from(root.querySelectorAll('.tabs a')).map((tab: Element) => tab.textContent?.trim()), text: root.textContent }
    })
    expect(detail.title).toBe('orders')
    expect(detail.tabs).toEqual(expect.arrayContaining(['Details', 'Definition']))
    expect(detail.text.toLowerCase()).not.toContain('workspace')

    await page.goto(`${baseURL}/?root=connection-detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const connection = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return {
        hasCatalogShell: Boolean(root.querySelector('.asset-page.connection-asset-page')),
        hasStandaloneEntityShell: Boolean(root.querySelector('.detail-surface')),
        heading: root.querySelector('.breadcrumb-header h1')?.textContent?.trim(),
        tabs: Array.from(root.querySelectorAll('.asset-body > .tabs a')).map((tab: Element) => tab.textContent?.trim()),
        hasAdministration: Boolean(root.querySelector('lv-connection-administration')),
        overview: root.querySelector('.detail-section[aria-label="Overview"]')?.textContent?.trim(),
      }
    })
    expect(connection.hasCatalogShell).toBe(true)
    expect(connection.hasStandaloneEntityShell).toBe(false)
    expect(connection.heading).toContain('Warehouse')
    expect(connection.tabs).toEqual(expect.arrayContaining(['Details', 'Definition', 'Lineage']))
    expect(connection.hasAdministration).toBe(true)
    expect(connection.overview).toContain('DuckDB')
  } finally {
    await page.close()
  }
})

test('asset Definition tab renders authored YAML and SQL as separate code sections', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=model-definition`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const definition = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return {
        activeTab: root.querySelector('.tabs a[aria-current="page"]')?.textContent?.trim(),
        label: root.querySelector('#definition')?.getAttribute('aria-label'),
        sections: Array.from(root.querySelectorAll('#definition .detail-section')).map((section: any) => ({
          title: section.querySelector('h2')?.textContent?.trim(),
          code: section.querySelector('lv-code-block')?.code,
          language: section.querySelector('lv-code-block')?.getAttribute('language'),
        })),
      }
    })
    expect(definition.activeTab).toBe('Definition')
    expect(definition.label).toBe('Asset definition')
    expect(definition.sections).toEqual([
      { title: 'Configuration', code: 'kind: Model\n', language: 'yaml' },
      { title: 'SQL', code: 'select * from source.orders', language: 'sql' },
    ])
  } finally {
    await page.close()
  }
})

test('unavailable pipeline shows visible recovery guidance and connections action', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipeline-unavailable`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const run = root.querySelector('button[aria-label*="Run now unavailable"]') as HTMLButtonElement | null
      const recovery = root.querySelector('a.action-link[href="/connections"]') as HTMLAnchorElement | null
      const recoveryStyle = recovery ? getComputedStyle(recovery) : null
      return {
        runDisabled: run?.disabled,
        recoveryText: recovery?.textContent?.trim(),
        recoveryBackground: recoveryStyle?.backgroundColor,
        recoveryForeground: recoveryStyle?.color,
        recoveryMinHeight: recoveryStyle?.minHeight,
        overview: root.querySelector('.detail-section[aria-label="Overview"]')?.textContent?.trim(),
      }
    })
    expect(state.runDisabled).toBe(true)
    expect(state.recoveryText).toContain('Review connections')
    expect(state.recoveryBackground).toBe('rgb(9, 105, 218)')
    expect(state.recoveryForeground).toBe('rgb(255, 255, 255)')
    expect(state.recoveryMinHeight).toBe('32px')
    expect(state.overview).toContain('Refresh state could not be loaded')
  } finally {
    await page.close()
  }
})

test('pipeline detail run action emits canonical pipeline command detail', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipeline-detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const detail = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      let command: unknown = null
      let documentCommand: unknown = null
      element.addEventListener('lv-run-refresh-pipeline', (event: CustomEvent) => { command = event.detail }, { once: true })
      document.addEventListener('lv-run-refresh-pipeline', (event: CustomEvent) => { documentCommand = event.detail }, { once: true })
      const button = element.shadowRoot?.querySelector('button[aria-label="Run now"]') as HTMLButtonElement | null
      button?.dispatchEvent(new MouseEvent('click', { bubbles: true, composed: true }))
      return { command, documentCommand, button: Boolean(button), disabled: button?.disabled, labels: Array.from(element.shadowRoot?.querySelectorAll('button') ?? []).map((candidate: any) => candidate.getAttribute('aria-label')) }
    })
    expect(detail).toEqual({ command: { action: 'run', assetId: 'pipeline:sales', pipelineId: 'pipeline:sales', runId: '' }, documentCommand: { action: 'run', assetId: 'pipeline:sales', pipelineId: 'pipeline:sales', runId: '' }, button: true, disabled: false, labels: ['Run now'] })
  } finally {
    await page.close()
  }
})

test('pipeline terminal command failure clears loading and offers reload guidance', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    await page.waitForFunction(() => customElements.get('lv-pipelines-page'))
    const state = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(() => resolve()))
      await element.updateComplete
      const feedback = element.shadowRoot?.querySelector('[role="alert"]') as HTMLElement | null
      const retry = feedback?.querySelector('button') as HTMLButtonElement | null
      return {
        message: feedback?.textContent?.trim(),
        failureKind: element.terminalFailure?.kind,
        retryLabel: retry?.textContent?.trim(),
        pendingAfterFailure: element.commandPendingFor('pipeline:sales'),
      }
    })
    expect(state.failureKind).toBe('unavailable')
    expect(state.message).toContain('previous state was kept')
    expect(state.retryLabel).toBe('Reload latest pipeline state')
    expect(state.pendingAfterFailure).toBe(false)
  } finally {
    await page.close()
  }
})

test('connection terminal command failure keeps the drawer state and offers reload guidance', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=connection-admin`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const admin = element.shadowRoot?.querySelector('lv-connection-administration') as any
      await admin?.updateComplete
      const configure = Array.from(admin.shadowRoot?.querySelectorAll('button') ?? []).find((button: any) => button.textContent?.trim() === 'Configure') as HTMLButtonElement | undefined
      configure?.click()
      await new Promise<void>((resolve) => queueMicrotask(() => resolve()))
      await admin?.updateComplete
      const drawerOpenAfterClick = Boolean(admin.shadowRoot?.querySelector('lv-drawer'))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(() => resolve()))
      await admin?.updateComplete
      const alert = admin.shadowRoot?.querySelector('[role="alert"]') as HTMLElement | null
      const retry = alert?.querySelector('button') as HTMLButtonElement | null
      const drawerOpenBeforeRetry = Boolean(admin.shadowRoot?.querySelector('lv-drawer'))
      return {
        message: alert?.textContent?.trim(),
        failureKind: admin.terminalFailure?.kind,
        retryLabel: retry?.textContent?.trim(),
        drawerOpenAfterClick,
        drawerOpenStateAfterClick: admin.drawerOpen,
        drawerOpen: drawerOpenAfterClick && drawerOpenBeforeRetry,
      }
    })
    expect(state.failureKind).toBe('unavailable')
    expect(state.message).toContain('previous state was kept')
    expect(state.retryLabel).toBe('Reload latest connection state')
    expect(state.drawerOpenStateAfterClick).toBe(true)
    expect(state.drawerOpenAfterClick).toBe(true)
    expect(state.drawerOpen).toBe(true)
  } finally {
    await page.close()
  }
})

function testDocument(rootName: string): string {
  const page = rootName === 'connections' ? {
    kind: 'connections', title: 'Connections', description: 'Data connections.', connections: [{ id: 'conn', title: 'Warehouse', description: 'Primary warehouse.', detailHref: '/connections/conn', kind: 'DuckDB', scope: 'Project', sourceCount: 2, credentialStatus: 'Configured', lifecycle: lifecycle() }],
  } : rootName === 'connection-admin' ? {
    kind: 'connection', title: 'Warehouse', assetId: 'conn', activeSection: 'details', asset: { id: 'conn', key: 'connection:conn', title: 'Warehouse', description: 'Primary warehouse.', type: 'connection', typeLabel: 'Connection', detailHref: '/connections/conn/details', openHref: '/connections/conn/details' }, breadcrumbs: [{ label: 'Connections', href: '/connections' }, { label: 'Warehouse', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/connections/conn/details', active: true }], connectionLifecycle: lifecycle('missing'), details: { overview: [{ label: 'Kind', value: 'DuckDB' }], sections: [] },
  } : rootName === 'pipelines' ? {
    kind: 'pipelines', title: 'Pipelines', description: 'Pipeline monitor.', environment: 'dev', activeTab: 'pipelines', metrics: [], pipelines: [{ assetId: 'pipeline:sales', canRun: true, href: '/pipelines/pipeline:sales/details', id: 'pipeline:sales', pipelineId: 'pipeline:sales', running: false, schedule: 'manual', semanticModel: 'sales', status: 'succeeded', title: 'Sales refresh' }], runsTable: { columns: [], rows: [], empty: 'No runs.' },
  } : rootName === 'connection-detail' ? {
    kind: 'connection', title: 'Warehouse', assetId: 'conn', activeSection: 'details', asset: { id: 'conn', key: 'connection:conn', title: 'Warehouse', description: 'Primary warehouse.', type: 'connection', typeLabel: 'Connection', detailHref: '/connections/conn/details', openHref: '/connections/conn/details' }, breadcrumbs: [{ label: 'Connections', href: '/connections' }, { label: 'Warehouse', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/connections/conn/details', active: true }, { id: 'definition', label: 'Definition', href: '/connections/conn/definition' }, { id: 'lineage', label: 'Lineage', href: '/connections/conn/lineage' }], connectionLifecycle: lifecycle(), details: { overview: [{ label: 'Kind', value: 'DuckDB' }, { label: 'Scope', value: 'Project' }], sections: [] },
  } : rootName === 'model-definition' ? {
    kind: 'data', title: 'orders', assetId: 'model:orders', activeSection: 'definition', asset: { id: 'model:orders', key: 'orders', title: 'orders', type: 'model_table', typeLabel: 'Model table', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }, breadcrumbs: [{ label: 'Models', href: '/models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/models/model:orders/details' }, { id: 'definition', label: 'Definition', href: '/models/model:orders/definition', active: true }], definition: { sections: [{ title: 'Configuration', code: 'kind: Model\n', lang: 'yaml' }, { title: 'SQL', code: 'select * from source.orders', lang: 'sql' }] },
  } : rootName === 'pipeline-detail' ? {
    kind: 'data', title: 'Sales refresh', assetId: 'pipeline:sales', activeSection: 'details', asset: { id: 'pipeline:sales', key: 'sales', title: 'Sales refresh', type: 'refresh_pipeline', typeLabel: 'Pipeline', detailHref: '/pipelines/pipeline:sales/details', openHref: '/pipelines/pipeline:sales/details' }, breadcrumbs: [{ label: 'Pipelines', href: '/pipelines' }, { label: 'Sales refresh', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/pipelines/pipeline:sales/details', active: true }, { id: 'definition', label: 'Definition', href: '/pipelines/pipeline:sales/definition' }, { id: 'refreshes', label: 'Refreshes', href: '/pipelines/pipeline:sales/refreshes' }], refresh: { status: 'succeeded', running: false, canRun: true }, actions: [{ label: 'Run now', command: 'run-refresh-pipeline', disabled: false }], details: { overview: [{ label: 'Refresh status', value: 'succeeded' }], sections: [] },
  } : rootName === 'pipeline-unavailable' ? {
    kind: 'data', title: 'Sales refresh', assetId: 'pipeline:sales', activeSection: 'details', asset: { id: 'pipeline:sales', key: 'sales', title: 'Sales refresh', type: 'refresh_pipeline', typeLabel: 'Pipeline', detailHref: '/pipelines/pipeline:sales/details', openHref: '/pipelines/pipeline:sales/details' }, breadcrumbs: [{ label: 'Pipelines', href: '/pipelines' }, { label: 'Sales refresh', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/pipelines/pipeline:sales/details', active: true }, { id: 'definition', label: 'Definition', href: '/pipelines/pipeline:sales/definition' }, { id: 'refreshes', label: 'Refreshes', href: '/pipelines/pipeline:sales/refreshes' }], refresh: { status: 'unavailable', running: false, canRun: false }, actions: [{ label: 'Run now unavailable; review connections', command: 'run-refresh-pipeline', disabled: true }, { label: 'Back to pipelines', href: '/pipelines', icon: 'back' }, { label: 'Review connections', href: '/connections', icon: 'open' }], details: { overview: [{ label: 'Refresh status', value: 'unavailable' }, { label: 'Refresh guidance', value: 'Refresh state could not be loaded. Review Connections and runtime setup, then retry.', wide: true }], sections: [] },
  } : rootName === 'detail' ? {
    kind: 'data', title: 'orders', assetId: 'orders', activeSection: 'details', asset: { id: 'orders', key: 'model_table:orders', title: 'orders', type: 'model_table', typeLabel: 'Model table', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }, breadcrumbs: [{ label: 'Develop', href: '/models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/models/model:orders/details', active: true }, { id: 'definition', label: 'Definition', href: '/models/model:orders/definition' }], details: { overview: [{ label: 'Rows', value: '100' }], sections: [] },
  } : rootName === 'semantic-detail' ? {
    kind: 'data', title: 'orders', assetId: 'semantic:orders', activeSection: 'details', asset: { id: 'semantic:orders', key: 'semantic_model:orders', title: 'orders', type: 'semantic_model', typeLabel: 'Semantic model', detailHref: '/semantic-models/semantic:orders/details', openHref: '/semantic-models/semantic:orders/details' }, breadcrumbs: [{ label: 'Semantic models', href: '/semantic-models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/semantic-models/semantic:orders/details', active: true }], details: { overview: [{ label: 'Rows', value: '100' }], sections: [] },
  } : rootName === 'model-data' ? {
    kind: 'data', title: 'orders', assetId: 'model:orders', activeSection: 'data', asset: { id: 'model:orders', key: 'orders', title: 'orders', type: 'model_table', typeLabel: 'Model table', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }, breadcrumbs: [{ label: 'Models', href: '/models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/models/model:orders/details' }, { id: 'data', label: 'Data', href: '/models/model:orders/data', active: true }], details: { overview: [], sections: [] },
  } : rootName === 'models' ? {
    kind: 'data', title: 'Models', assetList: { activeType: 'model_table', assets: [{ id: 'model:orders', key: 'model_table:orders', title: 'orders', type: 'model_table', typeLabel: 'Model table', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }], empty: 'No models.', searchHref: '/models', tabs: [] },
  } : rootName === 'semantic-models' ? {
    kind: 'data', title: 'Semantic models', assetList: { activeType: 'semantic_model', assets: [{ id: 'semantic:orders', key: 'semantic_model:orders', title: 'orders', type: 'semantic_model', typeLabel: 'Semantic model', detailHref: '/semantic-models/semantic:orders/details', openHref: '/semantic-models/semantic:orders/details' }], empty: 'No semantic models.', searchHref: '/semantic-models', tabs: [] },
  } : {
    kind: 'data', title: 'Develop', assetList: { activeType: 'source', assets: [{ id: 'source:orders', key: 'source:orders', title: 'orders', description: 'Raw orders.', type: 'source', typeLabel: 'Source', detailHref: '/sources/source:orders/details', openHref: '/sources/source:orders/details' }], empty: 'No assets.', searchHref: '/sources', tabs: [] },
  }
  const rootTag = rootName === 'connections' ? 'lv-connections-page' : rootName === 'pipelines' ? 'lv-pipelines-page' : rootName === 'detail' || rootName === 'connection-detail' || rootName === 'connection-admin' || rootName === 'semantic-detail' || rootName === 'model-data' || rootName === 'model-definition' || rootName === 'pipeline-detail' || rootName === 'pipeline-unavailable' ? 'lv-project-asset-page' : 'lv-project-page'
  const previewRows = Array.from({ length: 100 }, (_, index) => ({ customer_id: `customer-${index + 1}`, city: 'Example' }))
  const dataExplorer = { objects: [{ key: 'orders', resourceId: 'model:orders', title: 'orders', layer: 'model_table' }], selectedKey: 'orders', selectedObject: { key: 'orders', resourceId: 'model:orders', title: 'orders', layer: 'model_table' }, preview: { columns: [{ key: 'customer_id', label: 'Customer ID', type: 'string' }, { key: 'city', label: 'City', type: 'string' }], totalRows: 99441, availableRows: 99441, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: { a: { start: 0, requestSeq: 0, resetVersion: 0, sort: {}, rows: previewRows } }, totalRowLabel: '99441', sort: {}, sql: '', error: '' }, explore: { command: { modelId: '', datasetId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }, models: [], datasets: [], fields: [], result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] } }, command: { mode: 'browse', objectKey: 'orders', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} }, warnings: [] }
  const signals = {
    page,
    dataExplorer,
    connectionAdmin: rootName === 'connection-admin'
      ? { command: { action: 'create', assetId: 'conn', authenticationMode: 'none', connectorKind: 'DuckDB', logicalConnection: 'warehouse', expectedRevision: 0, host: '', database: '', objectScope: '', options: '', port: '', sourceIdentity: '', tlsMode: '', credentialProjectId: '', credentialEnvironment: '', secretPath: '', secretKey: '', confirmationToken: '', surface: 'detail' }, status: { loading: false, error: '', message: '' } }
      : { command: {}, status: { loading: false, error: '', message: '' } },
    pipelineCommand: rootName === 'pipelines' ? { action: 'run', assetId: 'pipeline:sales', pipelineId: 'pipeline:sales', runId: '' } : {},
    pipelineCommandStatus: rootName === 'pipelines' ? { loading: true, error: '', message: '' } : { loading: false, error: '', message: '' },
  }
  return `<!doctype html><html><body style="--lv-button-height:32px;--lv-button-accent-bg-rest:#0969da;--lv-button-accent-fg-rest:#fff;--lv-button-accent-border-rest:#0969da"><main data-signals="${escapeHTML(JSON.stringify(signals))}"><${rootTag}></${rootTag}></main><script type="module" src="/project-page-under-test.js"></script>${rootName === 'model-data' ? '<script type="module" src="/data-explorer-under-test.js"></script>' : ''}<script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script></body></html>`
}

function lifecycle(state = 'ready') {
  return { actions: state === 'missing' ? [{ id: 'configure', label: 'Configure', primary: true, destructive: false }] : [], assetId: 'conn', authenticationMode: 'none', bindingId: '', canManage: state === 'missing', canTest: false, connectorKind: 'DuckDB', credentialEnvironment: '', credentialProjectId: '', database: '', diagnosticCode: '', enabled: true, exists: state !== 'missing', health: 'healthy', host: '', lastValidatedAt: '', logicalConnection: 'warehouse', objectScope: '', options: '', port: '', revision: 1, secretKey: '', secretPath: '', sourceIdentity: '', state, statusLabel: state === 'missing' ? 'Not configured' : 'Configured', tlsMode: '', tone: state === 'missing' ? 'warning' : 'success', validatedVersion: '' }
}

function escapeHTML(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}
