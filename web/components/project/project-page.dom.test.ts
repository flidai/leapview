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
        searchLabel: input.getAttribute('aria-label'),
        rows: root.querySelectorAll('lv-record-table').length,
        columns: table?.table?.columns?.map((column: any) => column.header),
        firstRow: table?.table?.rows?.[0],
        detail,
      }
    })
    expect(state.title).toBe('Develop')
    expect(state.searchLabel).toBe('Search project assets')
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

test('semantic model breadcrumb uses the plain list-page icon identity', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=semantic-detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const icon = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const glyph = element.shadowRoot?.querySelector('h1 .asset-glyph') as HTMLElement | null
      const parent = root.querySelector('.breadcrumb-header nav a') as HTMLElement
      const title = root.querySelector('.breadcrumb-header h1') as HTMLElement
      const parentBox = parent.getBoundingClientRect()
      const titleBox = title.getBoundingClientRect()
      return {
        svg: glyph?.querySelector('svg')?.innerHTML ?? '',
        plain: glyph?.classList.contains('breadcrumb'),
        background: glyph ? getComputedStyle(glyph).backgroundColor : '',
        borderWidth: glyph ? getComputedStyle(glyph).borderTopWidth : '',
        separatorIcons: root.querySelectorAll('.breadcrumb-separator svg').length,
        parentFontSize: getComputedStyle(parent).fontSize,
        parentColor: getComputedStyle(parent).color,
        titleFontSize: getComputedStyle(title).fontSize,
        titleColor: getComputedStyle(title).color,
        titleFontWeight: getComputedStyle(title).fontWeight,
        verticalCenterDelta: Math.abs((parentBox.top + parentBox.bottom) / 2 - (titleBox.top + titleBox.bottom) / 2),
      }
    })
    expect(icon.svg).toContain('M6 12h12')
    expect(icon.svg).not.toContain('M21 8a2 2 0 00-1-1.73')
    expect(icon.plain).toBe(true)
    expect(icon.background).toBe('rgba(0, 0, 0, 0)')
    expect(icon.borderWidth).toBe('0px')
    expect(icon.separatorIcons).toBe(1)
    expect(icon.titleFontSize).toBe(icon.parentFontSize)
    expect(icon.titleColor).toBe(icon.parentColor)
    expect(icon.titleFontWeight).toBe('400')
    expect(icon.verticalCenterDelta).toBeLessThan(0.5)
  } finally {
    await page.close()
  }
})

test('semantic model overview separates summary metadata from model inspection', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=semantic-detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const details = root.querySelector('#details') as HTMLElement
      return {
        graphCount: details.querySelectorAll('lv-semantic-model-graph').length,
        panelHeadings: Array.from(details.querySelectorAll('.semantic-overview-panel h2')).map((node) => node.textContent?.trim()),
        description: details.querySelector('.semantic-overview-description')?.textContent?.trim(),
        key: details.querySelector('.semantic-overview-key code')?.textContent?.trim(),
        owner: details.querySelector('.semantic-overview-owner dd')?.textContent?.trim(),
        tags: Array.from(details.querySelectorAll('.semantic-overview-tag')).map((node) => node.textContent?.trim()),
        refreshStatus: details.querySelector('.semantic-overview-refresh-status strong')?.textContent?.trim(),
        pipeline: details.querySelector<HTMLAnchorElement>('.semantic-overview-pipeline')?.textContent?.trim(),
        pipelineHref: details.querySelector<HTMLAnchorElement>('.semantic-overview-pipeline')?.getAttribute('href'),
        lastRefreshed: details.querySelector('.semantic-overview-last-refreshed time')?.textContent?.trim(),
        lastRefreshedValue: details.querySelector('.semantic-overview-last-refreshed time')?.getAttribute('datetime'),
        refreshHref: details.querySelector<HTMLAnchorElement>('.semantic-overview-refresh-link')?.getAttribute('href'),
        version: details.querySelector('.semantic-overview-version dd')?.textContent?.trim(),
        versionHref: details.querySelector<HTMLAnchorElement>('.semantic-overview-version-link')?.getAttribute('href'),
        summaryLabels: Array.from(details.querySelectorAll('.semantic-summary-card span')).map((node) => node.textContent?.trim()),
        modelHrefs: Array.from(details.querySelectorAll<HTMLAnchorElement>('.semantic-overview-model-link')).map((link) => link.getAttribute('href')),
        impactHeadings: Array.from(details.querySelectorAll('.semantic-overview-impact h2')).map((node) => node.textContent?.trim()),
        upstreamFacts: Array.from(details.querySelectorAll('.semantic-overview-upstream .semantic-overview-impact-fact')).map((node) => node.textContent?.trim()),
        lineageHref: details.querySelector<HTMLAnchorElement>('.semantic-overview-lineage-link')?.getAttribute('href'),
        downstreamSummary: details.querySelector('.semantic-overview-downstream .semantic-overview-impact-fact')?.textContent?.trim(),
        downstreamAsset: details.querySelector<HTMLAnchorElement>('.semantic-overview-downstream-asset')?.textContent?.trim(),
        downstreamAssetHref: details.querySelector<HTMLAnchorElement>('.semantic-overview-downstream-asset')?.getAttribute('href'),
        openModelLinks: details.querySelectorAll('.semantic-model-summary-heading > a').length,
        typeLabels: Array.from(details.querySelectorAll('dt')).filter((node) => node.textContent?.trim() === 'Type').length,
      }
    })
    expect(state.graphCount).toBe(0)
    expect(state.panelHeadings).toEqual(['About', 'Operational state'])
    expect(state.description).toBe('Governed orders model.')
    expect(state.key).toBe('semantic_model:orders')
    expect(state.owner).toBe('Finance')
    expect(state.tags).toEqual(['finance', 'governed'])
    expect(state.refreshStatus).toBe('Succeeded')
    expect(state.pipeline).toBe('orders-refresh')
    expect(state.pipelineHref).toBe('/pipelines/pipeline:orders-refresh/details')
    expect(state.lastRefreshed).not.toBe('2026-08-24T14:32:05Z')
    expect(state.lastRefreshedValue).toBe('2026-08-24T14:32:05Z')
    expect(state.refreshHref).toBe('/semantic-models/semantic:orders/refreshes')
    expect(state.version).toBe('9')
    expect(state.versionHref).toBe('/semantic-models/semantic:orders/versions')
    expect(state.summaryLabels).toEqual(['Datasets', 'Dimensions', 'Metrics', 'Relationships'])
    expect(state.modelHrefs).toEqual([
      '/semantic-models/semantic:orders/definition?view=datasets',
      '/semantic-models/semantic:orders/definition?view=dimensions',
      '/semantic-models/semantic:orders/definition?view=metrics',
      '/semantic-models/semantic:orders/definition?view=relationships',
    ])
    expect(state.openModelLinks).toBe(0)
    expect(state.typeLabels).toBe(0)
    expect(state.impactHeadings).toEqual(['Upstream', 'Downstream impact'])
    expect(state.upstreamFacts).toEqual(['2 governed datasets', '1 refresh pipeline'])
    expect(state.lineageHref).toBe('/semantic-models/semantic:orders/lineage')
    expect(state.downstreamSummary).toBe('1 dashboard')
    expect(state.downstreamAsset).toBe('Executive Sales')
    expect(state.downstreamAssetHref).toBe('/dashboards/dashboard:executive-sales/details')
  } finally {
    await page.close()
  }
})

test('semantic model Model page exposes every view directly and synchronizes URL history', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=semantic-definition`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const labels = () => Array.from(root.querySelectorAll('.semantic-model-nav-item')).map((node: Element) => node.textContent?.replace(/\s+/g, ' ').trim())
      const metrics = root.querySelector<HTMLButtonElement>('[data-model-view="metrics"]')!
      metrics.click()
      await element.updateComplete
      const search = root.querySelector<HTMLInputElement>('.semantic-object-search')!
      search.value = 'order_count'
      search.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const objectTable = root.querySelector('lv-record-table') as any
      const filteredRows = objectTable?.table?.rows?.length
      const metricsURL = window.location.search
      root.querySelector<HTMLButtonElement>('[data-model-view="source"]')!.click()
      await element.updateComplete
      return {
        labels: labels(),
        filteredRows,
        metricsURL,
        sourceURL: window.location.search,
        sourceVisible: Boolean(root.querySelector('lv-config-viewer')),
        diagramVisibleAfterSource: Boolean(root.querySelector('lv-semantic-model-graph')),
      }
    })
    expect(state.labels).toEqual(['Diagram', 'Datasets 2', 'Dimensions 1', 'Metrics 1', 'Relationships 1', 'Source'])
    expect(state.filteredRows).toBe(1)
    expect(state.metricsURL).toContain('view=metrics')
    expect(state.sourceURL).toContain('view=source')
    expect(state.sourceVisible).toBe(true)
    expect(state.diagramVisibleAfterSource).toBe(false)

    await page.goBack()
    const restored = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return {
        active: element.shadowRoot!.querySelector('.semantic-model-nav-item[data-active="true"]')?.textContent?.replace(/\s+/g, ' ').trim(),
        metricsVisible: Boolean(element.shadowRoot!.querySelector('lv-record-table')),
      }
    })
    expect(restored).toEqual({ active: 'Metrics 1', metricsVisible: true })
  } finally {
    await page.close()
  }
})

test('semantic model Model page supports direct object links and remembers the last view', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=semantic-definition&view=dimensions`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const direct = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return element.shadowRoot!.querySelector('.semantic-model-nav-item[data-active="true"]')?.textContent?.replace(/\s+/g, ' ').trim()
    })
    expect(direct).toBe('Dimensions 1')

    await page.goto(`${baseURL}/?root=semantic-detail`)
    await page.goto(`${baseURL}/?root=semantic-definition`)
    const remembered = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return element.shadowRoot!.querySelector('.semantic-model-nav-item[data-active="true"]')?.textContent?.replace(/\s+/g, ' ').trim()
    })
    expect(remembered).toBe('Dimensions 1')
  } finally {
    await page.close()
  }
})

test('semantic model offers a permission-gated dashboard creation entry', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=semantic-detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const action = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      element.setAttribute('create-dashboard-href', '/dashboards/new?semanticModel=semantic%3Aorders')
      await element.updateComplete
      const link = element.shadowRoot.querySelector('.actions .action-link') as HTMLAnchorElement
      return { label: link?.textContent?.trim(), href: link?.getAttribute('href'), hasIcon: Boolean(link?.querySelector('svg')) }
    })
    expect(action).toEqual({ label: 'Create dashboard', href: '/dashboards/new?semanticModel=semantic%3Aorders', hasIcon: true })
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
    expect(detail.tabs).toEqual(expect.arrayContaining(['Overview', 'Definition']))
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
        tabCounts: root.querySelectorAll('.asset-body > .tabs .count').length,
        hasAdministration: Boolean(root.querySelector('lv-connection-administration')),
        overview: root.querySelector('.semantic-overview-state')?.textContent?.trim(),
        panels: Array.from(root.querySelectorAll('.semantic-overview-panel h2')).map((node) => node.textContent?.trim()),
        contents: root.querySelector('.semantic-model-summary h2')?.textContent?.trim(),
        downstream: root.querySelector('.semantic-overview-downstream')?.textContent?.replace(/\s+/g, ' ').trim(),
        downstreamHref: root.querySelector<HTMLAnchorElement>('.semantic-overview-downstream-asset')?.getAttribute('href'),
      }
    })
    expect(connection.hasCatalogShell).toBe(true)
    expect(connection.hasStandaloneEntityShell).toBe(false)
    expect(connection.heading).toContain('Warehouse')
    expect(connection.tabs).toEqual(expect.arrayContaining(['Overview', 'Definition', 'Lineage']))
    expect(connection.tabCounts).toBe(0)
    expect(connection.hasAdministration).toBe(true)
    expect(connection.overview).toContain('DuckDB')
    expect(connection.panels).toEqual(['About', 'Operational state'])
    expect(connection.contents).toBeUndefined()
    expect(connection.downstream).toContain('1 source')
    expect(connection.downstreamHref).toBe('/sources/source:orders/details')
  } finally {
    await page.close()
  }
})

test('asset Definition tab renders an outline and highlighted Transform SQL', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=model-definition`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const definition = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const viewer = root.querySelector('lv-config-viewer') as any
      await viewer?.updateComplete
      return {
        activeTab: root.querySelector('.tabs a[aria-current="page"]')?.textContent?.trim(),
        label: root.querySelector('#definition')?.getAttribute('aria-label'),
        configuration: viewer?.configuration,
        sqlRows: viewer?.shadowRoot?.querySelectorAll('.sql-row').length ?? 0,
        transformSections: root.querySelectorAll('.transform-section').length,
        sqlCode: (viewer?.shadowRoot?.querySelector('.sql-row lv-code-block') as any)?.code,
      }
    })
    expect(definition.activeTab).toBe('Definition')
    expect(definition.label).toBe('Definition')
    expect(definition.configuration).toContain('definition:')
    expect(definition.sqlRows).toBe(1)
    expect(definition.transformSections).toBe(0)
    expect(definition.sqlCode).toBe('select * from source.orders\n')
  } finally {
    await page.close()
  }
})

test('model Refreshes tab renders compact history and opens signal-driven run details', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=model-refresh`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page') && customElements.get('lv-drawer'))
    const refresh = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const runTable = root.querySelector('lv-record-table') as any
      await runTable?.updateComplete
      runTable?.querySelector<HTMLElement>('tbody tr.record-row')?.click()
      return {
        activeTab: root.querySelector('.tabs a.active')?.textContent?.trim(),
        headings: Array.from(root.querySelectorAll('#refreshes .detail-section h2')).map((heading: any) => heading.textContent?.trim()),
        text: root.querySelector('#refreshes')?.textContent?.replace(/\s+/g, ' ').trim(),
        lastRefreshedWide: root.querySelector('#refreshes .facts .wide p')?.textContent?.trim(),
        columns: runTable?.table?.columns?.map((column: any) => column.header),
        rowAction: runTable?.table?.rowAction,
        runRows: runTable?.querySelectorAll('tbody tr').length,
      }
    })
    expect(refresh.activeTab).toBe('Refreshes')
    expect(refresh.headings).toEqual(['Refresh history'])
    expect(refresh.text).not.toContain('2026-08-24 14:32 UTC')
    expect(refresh.lastRefreshedWide).toBeUndefined()
    expect(refresh.columns).toEqual(['Status', 'Started', 'Duration', 'Trigger', 'Initiated by'])
    expect(refresh.rowAction).toBe('open-refresh-run')
    expect(refresh.runRows).toBe(1)
    expect(refresh.text).not.toContain('DuckLake snapshot')
    expect(refresh.text).not.toContain('Status available')
    await page.waitForFunction(() => new URL(location.href).searchParams.get('refresh') === 'run:model:orders')
    const drawer = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const detail = root.querySelector('lv-drawer') as any
      await detail?.updateComplete
      return {
        signal: element.signals.refreshRunDrawer,
        label: detail?.getAttribute('label'),
        title: root.querySelector('.refresh-run-drawer-title h1')?.textContent?.trim(),
        subtitle: root.querySelector('.refresh-run-drawer-subtitle')?.textContent?.trim(),
        sections: Array.from(root.querySelectorAll('.refresh-run-drawer-body .detail-section')).map((section: any) => ({
          title: section.querySelector('h2')?.textContent?.trim(),
          text: section.textContent?.replace(/\s+/g, ' ').trim(),
        })),
      }
    })
    expect(drawer.signal).toEqual({ open: true, runId: 'run:model:orders' })
    expect(drawer.label).toBe('run:model:orders refresh details')
    expect(drawer.title).toBe('Refresh run')
    expect(drawer.subtitle).toBe('failed · 2026-08-24T14:32:00Z')
    expect(drawer.sections.map((section: any) => section.title)).toEqual(['Overview', 'Context', 'Execution', 'Error'])
    expect(drawer.sections.at(-1)?.text).toContain('Artifact digest mismatch')

    await page.evaluate(() => history.back())
    await page.waitForFunction(() => !new URL(location.href).searchParams.has('refresh'))
    const afterBack = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return { signal: element.signals.refreshRunDrawer, drawer: Boolean(element.shadowRoot!.querySelector('lv-drawer')) }
    })
    expect(afterBack).toEqual({ signal: { open: false, runId: '' }, drawer: false })

    await page.goto(`${baseURL}/?root=model-refresh&refresh=run:model:orders`)
    await page.waitForFunction(() => Boolean((document.querySelector('lv-project-asset-page') as any)?.signals?.refreshRunDrawer?.open))
    expect(await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return element.shadowRoot!.querySelector('.refresh-run-drawer-title h1')?.textContent?.trim()
    })).toBe('Refresh run')
  } finally {
    await page.close()
  }
})

test('semantic model exposes the same Refreshes history surface', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=semantic-refreshes`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const table = root.querySelector('lv-record-table') as any
      await table?.updateComplete
      return {
        activeTab: root.querySelector('.tabs a.active')?.textContent?.trim(),
        label: root.querySelector('#refreshes')?.getAttribute('aria-label'),
        rows: table?.querySelectorAll('tbody tr.record-row').length,
        rowAction: table?.table?.rowAction,
      }
    })
    expect(state).toEqual({ activeTab: 'Refreshes', label: 'Refreshes', rows: 1, rowAction: 'open-refresh-run' })
  } finally {
    await page.close()
  }
})

test('Versions uses a compact table and a deep-linked comparison drawer', async () => {
  const page = await browser.newPage({ viewport: { width: 1180, height: 760 } })
  try {
    await page.goto(`${baseURL}/?root=model-versions`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page') && customElements.get('lv-drawer'))
    const tableState = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const table = element.shadowRoot!.querySelector('lv-record-table') as any
      await table?.updateComplete
      const additions = table?.querySelector('.record-diff-additions') as HTMLElement | null
      const deletions = table?.querySelector('.record-diff-deletions') as HTMLElement | null
      table?.querySelector<HTMLElement>('tbody tr.record-row')?.click()
      return {
        columns: table?.table?.columns?.map((column: any) => column.header),
        versions: table?.table?.rows?.map((row: any) => ({ version: row.version, contentHash: row.content_hash })),
        rowAction: table?.table?.rowAction,
        drawerBeforeSignal: Boolean(element.shadowRoot!.querySelector('lv-drawer')),
        diff: [additions?.textContent, deletions?.textContent],
        diffColors: [additions ? getComputedStyle(additions).color : '', deletions ? getComputedStyle(deletions).color : ''],
      }
    })
    expect(tableState).toEqual({
      columns: ['Version', 'Content hash', 'Published', 'Changes', 'Status', 'Published by'],
      versions: [{ version: 2, contentHash: 'sha256:curre' }, { version: 1, contentHash: 'sha256:previ' }],
      rowAction: 'open-asset-version',
      drawerBeforeSignal: false,
      diff: ['+2', '-1'],
      diffColors: ['rgb(9, 105, 218)', 'rgb(209, 36, 47)'],
    })
    await page.waitForFunction(() => new URL(location.href).searchParams.get('version') === 'state:current')
    const drawer = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      element.style.setProperty('--base-size-12', '12px')
      element.style.setProperty('--base-size-16', '16px')
      await element.updateComplete
      const root = element.shadowRoot!
      const detail = root.querySelector('lv-drawer') as any
      await detail?.updateComplete
      return {
        signal: element.signals.assetVersionDrawer,
        label: detail?.getAttribute('label'),
        title: root.querySelector('.version-drawer-title h1')?.textContent?.trim(),
        subtitle: root.querySelector('.version-drawer-subtitle')?.textContent?.trim(),
        sections: Array.from(root.querySelectorAll('.version-drawer-body .detail-section')).map((section: any) => section.querySelector('h2')?.textContent?.trim()),
        factRowGaps: Array.from(root.querySelector('.version-drawer-body .facts')!.children).slice(1).map((row: any, index) => {
          const previous = root.querySelector('.version-drawer-body .facts')!.children[index].getBoundingClientRect()
          return Math.round(row.getBoundingClientRect().top - previous.bottom)
        }),
        factRows: Array.from(root.querySelectorAll('.version-drawer-body .facts > div')).map((row: any) => {
          const label = row.children[0]?.getBoundingClientRect()
          const value = row.children[1]?.getBoundingClientRect()
          return {
            labelTop: Math.round(label?.top ?? 0),
            valueTop: Math.round(value?.top ?? 0),
            labelLeft: Math.round(label?.left ?? 0),
            valueLeft: Math.round(value?.left ?? 0),
          }
        }),
        changes: root.querySelector('.version-changes pre')?.textContent,
        configuration: (root.querySelector('lv-code-block') as any)?.code,
      }
    })
    expect(drawer.signal).toEqual({ open: true, versionId: 'state:current' })
    expect(drawer.label).toBe('Version 2 details')
    expect(drawer.title).toBe('Version 2')
    expect(drawer.subtitle).toBe('current · 2026-08-24T14:57:00Z')
    expect(drawer.sections).toEqual(['Overview', 'Provenance', 'Changes from previous version', 'Compiled configuration'])
    expect(Math.min(...drawer.factRowGaps)).toBeGreaterThanOrEqual(12)
    expect(drawer.factRows.length).toBeGreaterThan(0)
    expect(drawer.factRows.every((row: any) => Math.abs(row.labelTop - row.valueTop) <= 2)).toBe(true)
    expect(drawer.factRows.every((row: any) => row.valueLeft > row.labelLeft)).toBe(true)
    expect(drawer.changes).toContain('+    "revenue"')
    expect(drawer.configuration).toContain('"revenue"')

    await page.evaluate(() => history.back())
    await page.waitForFunction(() => !new URL(location.href).searchParams.has('version'))
    expect(await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return { signal: element.signals.assetVersionDrawer, drawer: Boolean(element.shadowRoot!.querySelector('lv-drawer')) }
    })).toEqual({ signal: { open: false, versionId: '' }, drawer: false })

    await page.goto(`${baseURL}/?root=model-versions&version=state:previous`)
    await page.waitForFunction(() => Boolean((document.querySelector('lv-project-asset-page') as any)?.signals?.assetVersionDrawer?.open))
    expect(await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return {
        title: root.querySelector('.version-drawer-title h1')?.textContent?.trim(),
        firstVersion: root.querySelector('.version-changes .empty')?.textContent?.trim(),
      }
    })).toEqual({ title: 'Version 1', firstVersion: 'This is the first recorded version.' })
  } finally {
    await page.close()
  }
}, 15_000)

test('model field rows open a signal-driven responsive drawer and synchronize browser history', async () => {
  const page = await browser.newPage({ viewport: { width: 1180, height: 760 } })
  try {
    await page.goto(`${baseURL}/?root=model-field-drawer`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page') && customElements.get('lv-drawer'))
    await page.locator('lv-project-asset-page').evaluate((element: HTMLElement) => { element.dataset.instance = 'original' })
    const before = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const table = Array.from(element.shadowRoot!.querySelectorAll('lv-record-table'))
        .find((candidate: any) => candidate.table?.rowAction === 'open-model-field') as any
      await table?.updateComplete
      table?.querySelector<HTMLElement>('tbody tr.record-row')?.click()
      return {
        columns: table?.table?.columns?.map((column: any) => column.header),
        drawer: Boolean(element.shadowRoot!.querySelector('lv-drawer')),
      }
    })
    expect(before.columns).toEqual(['Field', 'Type', 'Description', 'Status'])
    expect(before.drawer).toBe(false)
    await page.waitForFunction(() => new URL(location.href).searchParams.get('field') === 'customer_id')
    const desktop = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const drawer = root.querySelector('lv-drawer') as any
      await drawer?.updateComplete
      return {
        samePage: element.dataset.instance,
        signal: element.signals.modelFieldDrawer,
        drawerLabel: drawer?.getAttribute('label'),
        title: root.querySelector('.field-drawer-title h1')?.textContent?.trim(),
        subtitle: root.querySelector('.field-drawer-subtitle')?.textContent?.trim(),
        sections: Array.from(root.querySelectorAll('.field-drawer-body .detail-section')).map((section: any) => ({
          title: section.querySelector('h2')?.textContent?.trim(),
          text: section.textContent?.replace(/\s+/g, ' ').trim(),
        })),
        width: Math.round(drawer.shadowRoot?.querySelector('.drawer')?.getBoundingClientRect().width ?? 0),
      }
    })
    expect(desktop.samePage).toBe('original')
    expect(desktop.signal).toEqual({ open: true, fieldKey: 'customer_id' })
    expect(desktop.drawerLabel).toBe('customer_id field details')
    expect(desktop.title).toBe('customer_id')
    expect(desktop.subtitle).toBe('Customer ID')
    expect(desktop.sections).toEqual([
      { title: 'Overview', text: 'Overview Label Customer ID Description Stable customer identifier' },
      { title: 'Schema', text: 'Schema Logical type String Physical type varchar Nullable Yes DuckLake snapshot 17' },
      { title: 'Contract', text: 'Contract Expected type String Status Contracted Provenance Declared in YAML' },
      { title: 'Semantics', text: 'Semantics Entities customer Grain Yes' },
    ])
    expect(desktop.width).toBeGreaterThan(400)
    expect(desktop.width).toBeLessThan(600)

    await page.evaluate(() => history.back())
    await page.waitForFunction(() => !new URL(location.href).searchParams.has('field'))
    const afterBack = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return {
        samePage: element.dataset.instance,
        signal: element.signals.modelFieldDrawer,
        drawer: Boolean(element.shadowRoot!.querySelector('lv-drawer')),
      }
    })
    expect(afterBack).toEqual({ samePage: 'original', signal: { open: false, fieldKey: '' }, drawer: false })

    await page.evaluate(() => history.forward())
    await page.waitForFunction(() => new URL(location.href).searchParams.get('field') === 'customer_id')
    await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const drawer = element.shadowRoot!.querySelector('lv-drawer') as any
      await drawer?.updateComplete
      drawer?.shadowRoot?.querySelector<HTMLButtonElement>('button.close')?.click()
    })
    await page.waitForFunction(() => !new URL(location.href).searchParams.has('field'))
    const afterClose = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return { signal: element.signals.modelFieldDrawer, drawer: Boolean(element.shadowRoot!.querySelector('lv-drawer')) }
    })
    expect(afterClose).toEqual({ signal: { open: false, fieldKey: '' }, drawer: false })

    await page.goto(`${baseURL}/?root=model-field-drawer&field=customer_id`)
    await page.waitForFunction(() => Boolean((document.querySelector('lv-project-asset-page') as any)?.signals?.modelFieldDrawer?.open))
    const deepLink = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return {
        signal: element.signals.modelFieldDrawer,
        title: element.shadowRoot!.querySelector('.field-drawer-title h1')?.textContent?.trim(),
      }
    })
    expect(deepLink).toEqual({ signal: { open: true, fieldKey: 'customer_id' }, title: 'customer_id' })

    await page.setViewportSize({ width: 390, height: 760 })
    const mobileWidth = await page.locator('lv-project-asset-page lv-drawer').evaluate(async (drawer: any) => {
      await drawer.updateComplete
      return Math.round(drawer.shadowRoot?.querySelector('.drawer')?.getBoundingClientRect().width ?? 0)
    })
    expect(mobileWidth).toBe(390)
  } finally {
    await page.close()
  }
}, 15_000)

test('unavailable pipeline shows guidance without an unrelated connections action', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipeline-unavailable`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const run = root.querySelector('button[aria-label*="Run now unavailable"]') as HTMLButtonElement | null
      return {
        runDisabled: run?.disabled,
        hasConnectionsAction: Boolean(root.querySelector('a.action-link[href="/connections"]')),
        overview: root.querySelector('#details')?.textContent?.trim(),
      }
    })
    expect(state.runDisabled).toBe(true)
    expect(state.hasConnectionsAction).toBe(false)
    expect(state.overview).toContain('Refresh state could not be loaded')
    expect(state.overview).toContain('refresh runtime')
  } finally {
    await page.close()
  }
})

test('dashboard Overview owns the persisted appearance editor and emits complete updates', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=dashboard-detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const host = page.locator('lv-project-asset-page')
    const initial = await host.evaluate(async (element: any) => {
      await element.updateComplete
	  const editor = element.shadowRoot!.querySelector('lv-dashboard-appearance-editor') as any
	  await editor.updateComplete
	  const root = editor.shadowRoot!
      return {
        current: root.querySelector('.dashboard-appearance-current')?.textContent?.trim(),
        editor: Boolean(root.querySelector('lv-dashboard-icon-picker')),
      }
    })
    expect(initial.current).toContain('chart-no-axes-combined')
    expect(initial.current).toContain('purple')
    expect(initial.editor).toBe(false)

    const detail = await host.evaluate(async (element: any) => {
      const selected = new Promise<unknown>((resolve) => element.addEventListener('lv-dashboard-appearance-change', (event: Event) => resolve((event as CustomEvent).detail), { once: true }))
	  const editor = element.shadowRoot!.querySelector('lv-dashboard-appearance-editor') as any
	  editor.shadowRoot!.querySelector<HTMLButtonElement>('.dashboard-appearance-edit')!.click()
	  await editor.updateComplete
	  const picker = editor.shadowRoot!.querySelector('lv-dashboard-icon-picker') as any
      await picker.updateComplete
      picker.shadowRoot!.querySelector<HTMLButtonElement>('.color.color-orange')!.click()
      return selected
    })
    expect(detail).toEqual({ icon: 'chart-no-axes-combined', color: 'orange' })
    const optimistic = await host.evaluate(async (element: any) => {
	  const editor = element.shadowRoot!.querySelector('lv-dashboard-appearance-editor') as any
	  await editor.updateComplete
	  const root = editor.shadowRoot!
      return {
        previewClass: root.querySelector('.dashboard-appearance-preview')?.className,
        status: root.querySelector('[role="status"]')?.textContent?.trim(),
      }
    })
    expect(optimistic.previewClass).toContain('appearance-color-orange')
    expect(optimistic.status).toBe('Saving appearance…')
    const failed = await host.evaluate(async (element: any) => {
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', argsRaw: { status: 503 } } }))
	  const editor = element.shadowRoot!.querySelector('lv-dashboard-appearance-editor') as any
	  await editor.updateComplete
	  const root = editor.shadowRoot!
      return {
        previewClass: root.querySelector('.dashboard-appearance-preview')?.className,
        error: root.querySelector('[role="alert"]')?.textContent?.trim(),
      }
    })
    expect(failed.previewClass).toContain('appearance-color-purple')
    expect(failed.error).toBe('Dashboard appearance could not be saved. Please try again.')
  } finally {
    await page.close()
  }
})

test('dashboard Definition starts with authored structure and excludes the appearance editor', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=dashboard-definition`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const definition = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return {
        views: Array.from(root.querySelectorAll<HTMLElement>('[data-definition-view]')).map((item) => item.dataset.definitionView),
        selected: root.querySelector<HTMLElement>('[data-definition-view][data-active="true"]')?.dataset.definitionView,
        appearance: Boolean(root.querySelector('lv-dashboard-appearance-editor')),
      }
    })
    expect(definition.views).toEqual(['pages', 'filters', 'visuals', 'source'])
    expect(definition.selected).toBe('pages')
    expect(definition.appearance).toBe(false)
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
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: document.body, argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(() => resolve()))
      await element.updateComplete
      const unrelatedIgnored = element.terminalFailure == null
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(() => resolve()))
      await element.updateComplete
      const feedback = element.shadowRoot?.querySelector('[role="alert"]') as HTMLElement | null
      const retry = feedback?.querySelector('button') as HTMLButtonElement | null
      return {
        message: feedback?.textContent?.trim(),
        failureKind: element.terminalFailure?.kind,
        retryLabel: retry?.textContent?.trim(),
        pendingAfterFailure: element.commandPendingFor('pipeline:sales'),
        unrelatedIgnored,
      }
    })
    expect(state.unrelatedIgnored).toBe(true)
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
      const form = admin.shadowRoot?.querySelector('form') as HTMLFormElement | null
      form?.requestSubmit()
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: document.body, argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(() => resolve()))
      await admin?.updateComplete
      const unrelatedIgnored = admin.terminalFailure == null
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
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
        unrelatedIgnored,
      }
    })
    expect(state.unrelatedIgnored).toBe(true)
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
    kind: 'connection', title: 'Warehouse', assetId: 'conn', activeSection: 'details', asset: { id: 'conn', key: 'connection:conn', title: 'Warehouse', description: 'Primary warehouse.', type: 'connection', typeLabel: 'Connection', detailHref: '/connections/conn/details', openHref: '/connections/conn/details' }, breadcrumbs: [{ label: 'Connections', href: '/connections' }, { label: 'Warehouse', current: true }], tabs: [{ id: 'details', label: 'Overview', href: '/connections/conn/details', active: true }, { id: 'definition', label: 'Definition', href: '/connections/conn/definition' }, { id: 'lineage', label: 'Lineage', href: '/connections/conn/lineage' }], connectionLifecycle: lifecycle(), details: { overview: [{ label: 'Type', value: 'Connection' }, { label: 'Key', value: 'connection:conn', code: true }, { label: 'Description', value: 'Primary warehouse.' }, { label: 'Kind', value: 'DuckDB' }, { label: 'Scope', value: 'Project' }], assetOverview: { upstreamAssets: [], downstreamAssets: [{ label: 'Orders', href: '/sources/source:orders/details', type: 'Source' }], pipelines: [] }, sections: [] },
  } : rootName === 'model-definition' ? {
    kind: 'data', title: 'orders', assetId: 'model:orders', activeSection: 'definition', asset: { id: 'model:orders', key: 'orders', title: 'orders', type: 'model', typeLabel: 'Model', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }, breadcrumbs: [{ label: 'Models', href: '/models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/models/model:orders/details' }, { id: 'definition', label: 'Definition', href: '/models/model:orders/definition', active: true }], definition: { sections: [{ title: 'Configuration', code: 'kind: Model\nspec:\n  definition:\n    type: sql\n    sql: |\n      select * from source.orders\n', lang: 'yaml' }, { title: 'SQL', code: 'select * from source.orders', lang: 'sql' }] },
  } : rootName === 'model-refresh' ? {
    kind: 'data', title: 'orders', assetId: 'model:orders', activeSection: 'refreshes', asset: { id: 'model:orders', key: 'orders', title: 'orders', type: 'model', typeLabel: 'Model', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }, breadcrumbs: [{ label: 'Models', href: '/models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/models/model:orders/details' }, { id: 'definition', label: 'Definition', href: '/models/model:orders/definition' }, { id: 'refreshes', label: 'Refreshes', href: '/models/model:orders/refreshes', active: true }], refresh: { status: 'available', running: false, lastSuccessful: '2026-08-24T14:32:00Z', facts: [{ label: 'Status', value: 'available' }, { label: 'Last refreshed', value: '2026-08-24 14:32 UTC', wide: true }, { label: 'Rows', value: '99,441' }, { label: 'Physical size', value: '7.6 MiB' }, { label: 'Data files', value: '1' }, { label: 'DuckLake snapshot', value: '2', code: true }], runsTable: { columns: [{ id: 'status', header: 'Status', kind: 'status' }, { id: 'started', header: 'Started' }, { id: 'duration', header: 'Duration' }, { id: 'trigger', header: 'Trigger' }, { id: 'triggered_by', header: 'Initiated by' }], rows: [{ status: { label: 'failed', tone: 'danger' }, started: '2026-08-24T14:32:00Z', duration: '5s', trigger: 'Pipeline', triggered_by: 'Local Developer', runId: 'run:model:orders', statusLabel: 'failed', startedAt: '2026-08-24T14:32:00Z', finishedAt: '2026-08-24T14:32:05Z', modelId: 'sales', environment: 'dev', servingStateId: 'generation:1', parentRunId: 'run:pipeline:sales', targetGeneration: 2, createdAt: '2026-08-24T14:31:59Z', updatedAt: '2026-08-24T14:32:05Z', error: 'Artifact digest mismatch' }], empty: 'No refresh runs.', rowAction: 'open-refresh-run' } },
  } : rootName === 'semantic-refreshes' ? {
    kind: 'data', title: 'Sales', assetId: 'semantic:sales', activeSection: 'refreshes', asset: { id: 'semantic:sales', key: 'sales', title: 'Sales', type: 'semantic_model', typeLabel: 'Semantic model', detailHref: '/semantic-models/semantic:sales/details', openHref: '/semantic-models/semantic:sales/details' }, breadcrumbs: [{ label: 'Semantic models', href: '/semantic-models' }, { label: 'Sales', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/semantic-models/semantic:sales/details' }, { id: 'refreshes', label: 'Refreshes', href: '/semantic-models/semantic:sales/refreshes', active: true }], refresh: { status: 'succeeded', running: false, lastSuccessful: '2026-08-24T14:32:05Z', facts: [{ label: 'Refresh status', value: 'succeeded' }, { label: 'Last refreshed', value: '2026-08-24T14:32:05Z' }], runsTable: { columns: [{ id: 'status', header: 'Status', kind: 'status' }, { id: 'started', header: 'Started' }, { id: 'duration', header: 'Duration' }, { id: 'trigger', header: 'Trigger' }, { id: 'triggered_by', header: 'Initiated by' }], rows: [{ status: { label: 'succeeded', tone: 'success' }, started: '2026-08-24T14:32:00Z', duration: '5s', trigger: 'Schedule', triggered_by: 'Scheduler', runId: 'run:semantic:sales', statusLabel: 'succeeded' }], empty: 'No refresh runs.', rowAction: 'open-refresh-run' } },
  } : rootName === 'model-versions' ? {
    kind: 'data', title: 'orders', assetId: 'model:orders', activeSection: 'versions', asset: { id: 'model:orders', key: 'orders', title: 'orders', type: 'model', typeLabel: 'Model', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }, breadcrumbs: [{ label: 'Models', href: '/models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/models/model:orders/details' }, { id: 'versions', label: 'Versions', href: '/models/model:orders/versions', active: true }], versions: { currentContentHash: 'sha256:current', table: { columns: [{ id: 'version', header: 'Version', kind: 'number' }, { id: 'content_hash', header: 'Content hash', kind: 'code' }, { id: 'published', header: 'Published' }, { id: 'diff_stat', header: 'Changes', kind: 'diff' }, { id: 'status', header: 'Status', kind: 'badge' }, { id: 'published_by', header: 'Published by' }], rows: [{ version: 2, content_hash: 'sha256:curre', published: '2026-08-24T14:57:00Z', diff_stat: { label: '2 additions, 1 deletion', additions: 2, deletions: 1 }, status: { label: 'current', tone: 'success' }, published_by: 'dev', versionId: 'state:current', statusLabel: 'current', contentHash: 'sha256:current', sourceFile: 'models/orders.yaml', environment: 'dev', snapshotId: 'snapshot:2', servingStateId: 'state:current', servingDigest: 'digest:current', createdAt: '2026-08-24T14:56:00Z', activatedAt: '2026-08-24T14:57:00Z', compiledConfiguration: '{\n  "fields": [\n    "order_id",\n    "revenue"\n  ]\n}\n', previousVersion: '1', changes: '--- sha256:previ\n+++ sha256:curre\n@@ -1,5 +1,6 @@\n {\n   "fields": [\n-    "order_id"\n+    "order_id",\n+    "revenue"\n   ]\n }\n', changesSummary: '' }, { version: 1, content_hash: 'sha256:previ', published: '2026-08-23T14:57:00Z', diff_stat: '-', status: { label: 'inactive', tone: 'muted' }, published_by: 'dev', versionId: 'state:previous', statusLabel: 'inactive', contentHash: 'sha256:previous', sourceFile: 'models/orders.yaml', environment: 'dev', snapshotId: 'snapshot:1', servingStateId: 'state:previous', servingDigest: 'digest:previous', createdAt: '2026-08-23T14:56:00Z', activatedAt: '2026-08-23T14:57:00Z', compiledConfiguration: '{\n  "fields": [\n    "order_id"\n  ]\n}\n', previousVersion: '', changes: '', changesSummary: 'This is the first recorded version.' }], empty: 'No versions.', rowAction: 'open-asset-version' } },
  } : rootName === 'pipeline-detail' ? {
    kind: 'data', title: 'Sales refresh', assetId: 'pipeline:sales', activeSection: 'details', asset: { id: 'pipeline:sales', key: 'sales', title: 'Sales refresh', type: 'refresh_pipeline', typeLabel: 'Pipeline', detailHref: '/pipelines/pipeline:sales/details', openHref: '/pipelines/pipeline:sales/details' }, breadcrumbs: [{ label: 'Pipelines', href: '/pipelines' }, { label: 'Sales refresh', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/pipelines/pipeline:sales/details', active: true }, { id: 'definition', label: 'Definition', href: '/pipelines/pipeline:sales/definition' }, { id: 'refreshes', label: 'Refreshes', href: '/pipelines/pipeline:sales/refreshes' }], refresh: { status: 'succeeded', running: false, canRun: true }, actions: [{ label: 'Run now', command: 'run-refresh-pipeline', disabled: false }], details: { overview: [{ label: 'Refresh status', value: 'succeeded' }], sections: [] },
  } : rootName === 'pipeline-unavailable' ? {
    kind: 'data', title: 'Sales refresh', assetId: 'pipeline:sales', activeSection: 'details', asset: { id: 'pipeline:sales', key: 'sales', title: 'Sales refresh', type: 'refresh_pipeline', typeLabel: 'Pipeline', detailHref: '/pipelines/pipeline:sales/details', openHref: '/pipelines/pipeline:sales/details' }, breadcrumbs: [{ label: 'Pipelines', href: '/pipelines' }, { label: 'Sales refresh', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/pipelines/pipeline:sales/details', active: true }, { id: 'definition', label: 'Definition', href: '/pipelines/pipeline:sales/definition' }, { id: 'refreshes', label: 'Refreshes', href: '/pipelines/pipeline:sales/refreshes' }], refresh: { status: 'unavailable', running: false, canRun: false }, actions: [{ label: 'Run now unavailable', command: 'run-refresh-pipeline', disabled: true }, { label: 'Back to pipelines', href: '/pipelines', icon: 'back' }], details: { overview: [{ label: 'Refresh status', value: 'unavailable' }, { label: 'Refresh guidance', value: 'Refresh state could not be loaded. Check the refresh runtime and try again.', wide: true }], sections: [] },
  } : rootName === 'dashboard-detail' || rootName === 'dashboard-definition' ? {
    kind: 'data', title: 'Executive Sales', assetId: 'dashboard:executive-sales', activeSection: rootName === 'dashboard-definition' ? 'definition' : 'details', asset: { id: 'dashboard:executive-sales', key: 'executive-sales', title: 'Executive Sales', type: 'dashboard', typeLabel: 'Dashboard', detailHref: '/dashboards/dashboard:executive-sales/details', openHref: '/dashboards/dashboard:executive-sales' }, breadcrumbs: [{ label: 'Dashboards', href: '/dashboards' }, { label: 'Executive Sales', current: true }], tabs: [{ id: 'details', label: 'Overview', href: '/dashboards/dashboard:executive-sales/details', active: rootName === 'dashboard-detail' }, { id: 'definition', label: 'Definition', href: '/dashboards/dashboard:executive-sales/definition', active: rootName === 'dashboard-definition' }], dashboardAppearance: { icon: 'chart-no-axes-combined', color: 'purple', revision: 2 }, definition: { sections: [{ title: 'Configuration', code: 'kind: Dashboard\n', lang: 'yaml' }] }, details: { overview: [{ label: 'Semantic model', value: 'semantic-model:sales' }], sections: [{ title: 'Pages (1)', table: { columns: [{ id: 'page', header: 'Page' }], rows: [{ page: 'Overview' }], empty: 'No pages.' } }, { title: 'Filters (1)', table: { columns: [{ id: 'filter', header: 'Filter' }], rows: [{ filter: 'Region' }], empty: 'No filters.' } }, { title: 'Visuals (1)', table: { columns: [{ id: 'visual', header: 'Visual' }], rows: [{ visual: 'Revenue' }], empty: 'No visuals.' } }] },
  } : rootName === 'model-field-drawer' ? {
    kind: 'data', title: 'Customers', assetId: 'model:sales_customers', activeSection: 'definition', asset: { id: 'model:sales_customers', key: 'sales_customers', title: 'Customers', type: 'model', typeLabel: 'Model', detailHref: '/models/model:sales_customers/details', openHref: '/models/model:sales_customers/details' }, breadcrumbs: [{ label: 'Models', href: '/models' }, { label: 'Customers', current: true }], tabs: [{ id: 'details', label: 'Overview', href: '/models/model:sales_customers/details' }, { id: 'definition', label: 'Definition', href: '/models/model:sales_customers/definition', active: true }], definition: { sections: [{ title: 'Configuration', code: 'kind: Model\n', lang: 'yaml' }] }, details: { overview: [{ label: 'Fields', value: '1' }], sections: [{ title: 'Fields (1)', table: { columns: [{ id: 'field', header: 'Field', kind: 'entity' }, { id: 'type', header: 'Type', kind: 'entity' }, { id: 'description', header: 'Description' }, { id: 'status', header: 'Status', kind: 'badge' }], rows: [{ fieldKey: 'customer_id', field: { label: 'customer_id', description: 'Customer ID' }, type: { label: 'varchar', description: 'Nullable' }, description: 'Stable customer identifier', status: { label: 'Contracted', tone: 'success' }, label: 'Customer ID', logicalType: 'String', physicalType: 'varchar', nullable: 'Yes', contractType: 'String', metadataStatus: 'Contracted', metadataProvenance: 'Declared in YAML', entities: 'customer', grain: 'Yes', duckLakeSnapshot: '17' }], empty: 'No fields.', rowAction: 'open-model-field' } }] },
  } : rootName === 'detail' ? {
    kind: 'data', title: 'orders', assetId: 'orders', activeSection: 'details', asset: { id: 'orders', key: 'model:orders', title: 'orders', type: 'model', typeLabel: 'Model', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }, breadcrumbs: [{ label: 'Develop', href: '/models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Overview', href: '/models/model:orders/details', active: true }, { id: 'definition', label: 'Definition', href: '/models/model:orders/definition' }], details: { overview: [{ label: 'Rows', value: '100' }], sections: [] },
  } : rootName === 'semantic-detail' || rootName === 'semantic-definition' ? {
    kind: 'data', title: 'orders', assetId: 'semantic:orders', activeSection: rootName === 'semantic-definition' ? 'definition' : 'details', asset: { id: 'semantic:orders', key: 'semantic_model:orders', title: 'orders', description: 'Governed orders model.', type: 'semantic_model', typeLabel: 'Semantic model', detailHref: '/semantic-models/semantic:orders/details', openHref: '/semantic-models/semantic:orders/details' }, breadcrumbs: [{ label: 'Semantic models', href: '/semantic-models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Overview', href: '/semantic-models/semantic:orders/details', active: rootName === 'semantic-detail' }, { id: 'definition', label: 'Model', href: '/semantic-models/semantic:orders/definition', active: rootName === 'semantic-definition' }, { id: 'data', label: 'Explore', href: '/semantic-models/semantic:orders/data' }, { id: 'refreshes', label: 'Refreshes', href: '/semantic-models/semantic:orders/refreshes' }, { id: 'versions', label: 'Versions', href: '/semantic-models/semantic:orders/versions' }, { id: 'lineage', label: 'Lineage', href: '/semantic-models/semantic:orders/lineage' }], details: { overview: [{ label: 'Type', value: 'Semantic model' }, { label: 'Key', value: 'semantic_model:orders', code: true }, { label: 'Description', value: 'Governed orders model.', wide: true }, { label: 'Refresh status', value: 'succeeded' }, { label: 'Last refreshed', value: '2026-08-24T14:32:05Z' }], assetOverview: { activeVersion: 9, owner: 'Finance', tags: ['finance', 'governed'], upstreamDatasetCount: 2, upstreamAssets: [], pipelines: [{ label: 'orders-refresh', href: '/pipelines/pipeline:orders-refresh/details', type: 'Pipeline' }], downstreamAssets: [{ label: 'Executive Sales', href: '/dashboards/dashboard:executive-sales/details', type: 'Dashboard' }] }, semanticModelGraph: { datasets: ['orders'], nodes: [], edges: [] }, sections: [{ title: 'Datasets (2)', table: { columns: [{ id: 'name', header: 'Name' }], rows: [{ name: 'orders' }, { name: 'customers' }], empty: 'No datasets.' } }, { title: 'Dimensions (1)', table: { columns: [{ id: 'name', header: 'Name' }], rows: [{ name: 'customer' }], empty: 'No dimensions.' } }, { title: 'Metrics (1)', table: { columns: [{ id: 'name', header: 'Name' }], rows: [{ name: 'order_count' }], empty: 'No metrics.' } }, { title: 'Relationships (1)', table: { columns: [{ id: 'id', header: 'ID' }], rows: [{ id: 'orders_customer' }], empty: 'No relationships.' } }] }, definition: { sections: [{ title: 'Configuration', code: 'kind: SemanticModel\nspec:\n  datasets:\n    orders: {}\n', lang: 'yaml' }] },
  } : rootName === 'model-data' ? {
    kind: 'data', title: 'orders', assetId: 'model:orders', activeSection: 'data', asset: { id: 'model:orders', key: 'orders', title: 'orders', type: 'model', typeLabel: 'Model', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }, breadcrumbs: [{ label: 'Models', href: '/models' }, { label: 'orders', current: true }], tabs: [{ id: 'details', label: 'Details', href: '/models/model:orders/details' }, { id: 'data', label: 'Data', href: '/models/model:orders/data', active: true }], details: { overview: [], sections: [] },
  } : rootName === 'models' ? {
    kind: 'data', title: 'Models', assetList: { activeType: 'model', assets: [{ id: 'model:orders', key: 'model:orders', title: 'orders', type: 'model', typeLabel: 'Model', detailHref: '/models/model:orders/details', openHref: '/models/model:orders/details' }], empty: 'No models.', searchHref: '/models', tabs: [] },
  } : rootName === 'semantic-models' ? {
    kind: 'data', title: 'Semantic models', assetList: { activeType: 'semantic_model', assets: [{ id: 'semantic:orders', key: 'semantic_model:orders', title: 'orders', type: 'semantic_model', typeLabel: 'Semantic model', detailHref: '/semantic-models/semantic:orders/details', openHref: '/semantic-models/semantic:orders/details' }], empty: 'No semantic models.', searchHref: '/semantic-models', tabs: [] },
  } : {
    kind: 'data', title: 'Develop', assetList: { activeType: 'source', assets: [{ id: 'source:orders', key: 'source:orders', title: 'orders', description: 'Raw orders.', type: 'source', typeLabel: 'Source', detailHref: '/sources/source:orders/details', openHref: '/sources/source:orders/details' }], empty: 'No assets.', searchHref: '/sources', tabs: [] },
  }
  const rootTag = rootName === 'connections' ? 'lv-connections-page' : rootName === 'pipelines' ? 'lv-pipelines-page' : rootName === 'detail' || rootName === 'connection-detail' || rootName === 'connection-admin' || rootName === 'semantic-detail' || rootName === 'semantic-definition' || rootName === 'semantic-refreshes' || rootName === 'model-data' || rootName === 'model-definition' || rootName === 'model-refresh' || rootName === 'model-versions' || rootName === 'model-field-drawer' || rootName === 'pipeline-detail' || rootName === 'pipeline-unavailable' || rootName === 'dashboard-detail' || rootName === 'dashboard-definition' ? 'lv-project-asset-page' : 'lv-project-page'
  const previewRows = Array.from({ length: 100 }, (_, index) => ({ customer_id: `customer-${index + 1}`, city: 'Example' }))
  const dataExplorer = { objects: [{ key: 'orders', resourceId: 'model:orders', title: 'orders', layer: 'model' }], selectedKey: 'orders', selectedObject: { key: 'orders', resourceId: 'model:orders', title: 'orders', layer: 'model' }, preview: { columns: [{ key: 'customer_id', label: 'Customer ID', type: 'string' }, { key: 'city', label: 'City', type: 'string' }], totalRows: 99441, availableRows: 99441, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: { a: { start: 0, requestSeq: 0, resetVersion: 0, sort: {}, rows: previewRows } }, totalRowLabel: '99441', sort: {}, sql: '', error: '' }, explore: { command: { semanticModelId: '', datasetId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] } }, command: { mode: 'browse', objectKey: 'orders', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} }, warnings: [] }
  const signals = {
    page,
    dataExplorer,
    connectionAdmin: rootName === 'connection-admin'
      ? { command: { action: 'create', assetId: 'conn', authenticationMode: 'none', connectorKind: 'DuckDB', logicalConnection: 'warehouse', expectedRevision: 0, host: '', database: '', objectScope: '', options: '', port: '', sourceIdentity: '', tlsMode: '', credentialProjectId: '', credentialEnvironment: '', secretPath: '', secretKey: '', confirmationToken: '', surface: 'detail' }, status: { loading: false, error: '', message: '' } }
      : { command: {}, status: { loading: false, error: '', message: '' } },
    pipelineCommand: rootName === 'pipelines' ? { action: 'run', assetId: 'pipeline:sales', pipelineId: 'pipeline:sales', runId: '' } : {},
    pipelineCommandStatus: rootName === 'pipelines' ? { loading: true, error: '', message: '' } : { loading: false, error: '', message: '' },
  }
  return `<!doctype html><html><body style="--lv-button-height:32px;--lv-button-accent-bg-rest:#0969da;--lv-button-accent-fg-rest:#fff;--lv-button-accent-border-rest:#0969da;--lv-fg-accent:#0969da;--lv-fg-danger:#d1242f"><main data-signals="${escapeHTML(JSON.stringify(signals))}"><${rootTag}></${rootTag}></main><script type="module" src="/project-page-under-test.js"></script>${rootName === 'model-data' ? '<script type="module" src="/data-explorer-under-test.js"></script>' : ''}<script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script></body></html>`
}

function lifecycle(state = 'ready') {
  return { actions: state === 'missing' ? [{ id: 'configure', label: 'Configure', primary: true, destructive: false }] : [], assetId: 'conn', authenticationMode: 'none', bindingId: '', canManage: state === 'missing', canTest: false, connectorKind: 'DuckDB', credentialEnvironment: '', credentialProjectId: '', database: '', diagnosticCode: '', enabled: true, exists: state !== 'missing', health: 'healthy', host: '', lastValidatedAt: '', logicalConnection: 'warehouse', objectScope: '', options: '', port: '', revision: 1, secretKey: '', secretPath: '', sourceIdentity: '', state, statusLabel: state === 'missing' ? 'Not configured' : 'Configured', tlsMode: '', tone: state === 'missing' ? 'warning' : 'success', validatedVersion: '' }
}

function escapeHTML(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}
