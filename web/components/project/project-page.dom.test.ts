import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testDocument, type ProjectTableElement } from './project-page.dom.fixture'

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
      const root = (element.shadowRoot as ShadowRoot)!
      const input = root.querySelector('input[type="search"]') as HTMLInputElement
      input.value = 'orders'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const table = root.querySelector('lv-record-table') as ProjectTableElement
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
    const firstRow = state.firstRow!
    expect(firstRow.name?.description).toBe('Raw orders.')
    expect(firstRow.key).toBe('source:orders')
    expect(firstRow.actions).toEqual([])
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
        const table = (element.shadowRoot as ShadowRoot)?.querySelector('lv-record-table') as ProjectTableElement
        await table?.updateComplete
        return {
          links: Array.from(table?.querySelectorAll<HTMLAnchorElement>('a') ?? []).map((link) => link.getAttribute('href')),
          filters: (element.shadowRoot as ShadowRoot)?.querySelectorAll('select').length ?? 0,
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
      const root = (element.shadowRoot as ShadowRoot)!
      const glyph = (element.shadowRoot as ShadowRoot)?.querySelector('h1 .asset-glyph') as HTMLElement | null
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
      element.style.setProperty('--lv-border-muted', '1px solid currentColor')
      element.style.setProperty('--lv-border-default', '1px solid currentColor')
      element.style.setProperty('--lv-chrome-rule-gutter', '44px')
      element.style.setProperty('--lv-page-content-max-width', '72rem')
      const root = element.shadowRoot! as ShadowRoot
      const details = root.querySelector('#details') as HTMLElement
      const overview = details.querySelector('.semantic-model-overview') as HTMLElement
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
        contentRules: {
          afterOverview: getComputedStyle(overview).borderBottomWidth,
          beforeContents: getComputedStyle(details.querySelector('.semantic-model-summary') as HTMLElement).borderTopWidth,
          beforeImpact: getComputedStyle(details.querySelector('.semantic-overview-impact-grid') as HTMLElement).borderTopWidth,
        },
        overviewLayout: {
          width: overview.getBoundingClientRect().width,
          availableWidth: details.getBoundingClientRect().width,
          left: overview.getBoundingClientRect().left,
          availableLeft: details.getBoundingClientRect().left,
          panelBorders: Array.from(details.querySelectorAll<HTMLElement>('.semantic-overview-panel')).map((node) => getComputedStyle(node).borderTopWidth),
          summaryBorders: Array.from(details.querySelectorAll<HTMLElement>('.semantic-summary-card')).map((node) => getComputedStyle(node).borderTopWidth),
          impactBorders: Array.from(details.querySelectorAll<HTMLElement>('.semantic-overview-impact')).map((node) => getComputedStyle(node).borderTopWidth),
        },
        chromeRuleExtensions: {
          breadcrumb: getComputedStyle(root.querySelector('.breadcrumb-header') as HTMLElement, '::before').width,
          tabs: getComputedStyle(root.querySelector('.asset-body > .tabs') as HTMLElement, '::before').width,
        },
      }
    })
    expect(state.graphCount).toBe(0)
    expect(state.panelHeadings).toEqual(['About', 'Data state'])
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
    expect(state.contentRules).toEqual({ afterOverview: '0px', beforeContents: '0px', beforeImpact: '0px' })
    expect(state.overviewLayout.width).toBe(1152)
    expect(state.overviewLayout.width).toBeLessThan(state.overviewLayout.availableWidth)
    expect(state.overviewLayout.left).toBe(state.overviewLayout.availableLeft)
    expect(state.overviewLayout.panelBorders).toEqual(['0px', '0px'])
    expect(state.overviewLayout.summaryBorders).toEqual(['0px', '0px', '0px', '0px'])
    expect(state.overviewLayout.impactBorders).toEqual(['0px', '0px'])
    expect(state.chromeRuleExtensions).toEqual({ breadcrumb: '44px', tabs: '44px' })
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

test('semantic model Definition page exposes every view directly and synchronizes URL history', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=semantic-definition`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot! as ShadowRoot
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
      const duplicateCount = root.querySelector('.semantic-object-list-header p')?.textContent?.trim() ?? null
      const metricsURL = window.location.search
      root.querySelector<HTMLButtonElement>('[data-model-view="source"]')!.click()
      await element.updateComplete
      return {
        activeTab: root.querySelector('.tabs a[aria-current="page"]')?.textContent?.trim(),
        labels: labels(),
        filteredRows,
        duplicateCount,
        metricsURL,
        sourceURL: window.location.search,
        sourceVisible: Boolean(root.querySelector('lv-config-viewer')),
        diagramVisibleAfterSource: Boolean(root.querySelector('lv-semantic-model-graph')),
      }
    })
    expect(state.activeTab).toBe('Definition')
    expect(state.labels).toEqual(['Diagram', 'Datasets 2', 'Dimensions 1', 'Metrics 1', 'Relationships 1', 'Source'])
    expect(state.filteredRows).toBe(1)
    expect(state.duplicateCount).toBeNull()
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

test('semantic model Definition uses a full-width horizontal section bar', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=semantic-definition`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot! as ShadowRoot
      const view = root.querySelector<HTMLElement>('.semantic-model-view')!
      const navigation = root.querySelector<HTMLElement>('#definition .semantic-model-navigation')!
      const layout = root.querySelector<HTMLElement>('.semantic-model-layout')!
      return {
        repeatedTitle: root.querySelectorAll('#definition h2').length,
        background: getComputedStyle(view).backgroundColor,
        bodyPadding: getComputedStyle(root.querySelector<HTMLElement>('.section-body')!).paddingLeft,
        toggle: Boolean(root.querySelector('[data-toggle-definition-navigation]')),
        navigationDirection: getComputedStyle(navigation).flexDirection,
        navigationOverflow: getComputedStyle(navigation).overflowX,
        navigationTopSpacing: getComputedStyle(navigation).marginTop,
        navigationWidth: Math.round(navigation.getBoundingClientRect().width),
        layoutWidth: Math.round(layout.getBoundingClientRect().width),
        diagramVisible: Boolean(root.querySelector('lv-semantic-model-graph')),
      }
    })
    expect(state.repeatedTitle).toBe(0)
    expect(state.background).toBe('rgba(0, 0, 0, 0)')
    expect(state.bodyPadding).toBe('0px')
    expect(state.toggle).toBe(false)
    expect(state.navigationDirection).toBe('row')
    expect(state.navigationOverflow).toBe('auto')
    expect(state.navigationTopSpacing).toBe('12px')
    expect(state.navigationWidth).toBe(state.layoutWidth)
    expect(state.diagramVisible).toBe(true)
    await page.setViewportSize({ width: 240, height: 800 })
    const narrow = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      const root = element.shadowRoot! as ShadowRoot
      const navigation = root.querySelector<HTMLElement>('#definition .semantic-model-navigation')!
      const source = root.querySelector<HTMLButtonElement>('[data-model-view="source"]')!
      source.click()
      await element.updateComplete
      return {
        scrollable: navigation.scrollWidth > navigation.clientWidth,
        sourceSelected: source.getAttribute('data-active'),
        sourceVisible: Boolean(root.querySelector('lv-config-viewer')),
      }
    })
    expect(narrow).toEqual({ scrollable: true, sourceSelected: 'true', sourceVisible: true })
  } finally {
    await page.close()
  }
})

test('narrow Definition deep links and history keep the selected section visible', async () => {
  const page = await browser.newPage({ viewport: { width: 240, height: 800 } })
  try {
    await page.goto(`${baseURL}/?root=semantic-definition&view=source`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const selectedSection = () => page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const navigation = (element.shadowRoot as ShadowRoot).querySelector<HTMLElement>('#definition .semantic-model-navigation')!
      const active = navigation.querySelector<HTMLElement>('[data-active="true"]')!
      const navBounds = navigation.getBoundingClientRect()
      const activeBounds = active.getBoundingClientRect()
      return {
        label: active.textContent?.replace(/\s+/g, ' ').trim(),
        scrollLeft: navigation.scrollLeft,
        visible: activeBounds.left >= navBounds.left - 1 && activeBounds.right <= navBounds.right + 1,
      }
    })
    expect(await selectedSection()).toEqual({ label: 'Source', scrollLeft: expect.any(Number), visible: true })

    await page.locator('lv-project-asset-page').evaluate((element: any) => {
      ;(element.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('[data-model-view="relationships"]')!.click()
    })
    expect(await selectedSection()).toEqual({ label: 'Relationships 1', scrollLeft: expect.any(Number), visible: true })
    await page.evaluate(() => history.back())
    await page.waitForFunction(() => window.location.search.includes('view=source'))
    expect(await selectedSection()).toEqual({ label: 'Source', scrollLeft: expect.any(Number), visible: true })

    await page.setViewportSize({ width: 960, height: 800 })
    await page.setViewportSize({ width: 240, height: 800 })
    expect(await selectedSection()).toEqual({ label: 'Source', scrollLeft: expect.any(Number), visible: true })

    await page.goto(`${baseURL}/?root=dashboard-definition&view=source`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    expect(await selectedSection()).toEqual({ label: 'Source', scrollLeft: expect.any(Number), visible: true })
  } finally {
    await page.close()
  }
})

test('simple source Definition exposes both views in the horizontal section bar', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=source-definition`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const state = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot! as ShadowRoot
      const navigation = root.querySelector<HTMLElement>('#definition .semantic-model-navigation')!
      const sectionNames = Array.from(navigation.querySelectorAll<HTMLButtonElement>('[data-definition-view]')).map((button) => button.textContent?.replace(/\s+/g, ' ').trim())
      const duplicateCount = root.querySelector('.semantic-object-list-header p')?.textContent?.trim() ?? null
      root.querySelector<HTMLButtonElement>('[data-definition-view="source"]')!.click()
      await element.updateComplete
      return {
        headings: Array.from(root.querySelectorAll('#definition h2')).map((node: Element) => node.textContent?.trim()),
        sectionNames,
        duplicateCount,
        navigationDirection: getComputedStyle(navigation).flexDirection,
        navigationTopSpacing: getComputedStyle(navigation).marginTop,
        toggle: Boolean(root.querySelector('[data-toggle-definition-navigation]')),
        sourceURL: window.location.search,
        source: (root.querySelector('lv-config-viewer') as any)?.configuration,
        background: getComputedStyle(root.querySelector<HTMLElement>('#definition')!).backgroundColor,
        bodyPadding: getComputedStyle(root.querySelector<HTMLElement>('.section-body')!).paddingLeft,
      }
    })
    expect(state).toEqual({
      headings: ['Configuration'],
      sectionNames: ['Fields 5', 'Source'],
      duplicateCount: null,
      navigationDirection: 'row',
      navigationTopSpacing: '12px',
      toggle: false,
      sourceURL: '?root=source-definition&view=source',
      source: 'kind: Source\n',
      background: 'rgba(0, 0, 0, 0)',
      bodyPadding: '0px',
    })
  } finally {
    await page.close()
  }
})

test('semantic model Definition page supports direct object links and remembers the last view', async () => {
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
      const link = (element.shadowRoot as ShadowRoot).querySelector('.actions .action-link') as HTMLAnchorElement
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
      const explorer = (element.shadowRoot as ShadowRoot)?.querySelector('lv-data-explorer') as any
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
      const root = (element.shadowRoot as ShadowRoot)!
      return { title: root.querySelector('h1')?.textContent?.trim(), rows: root.querySelectorAll('tbody tr').length, text: root.textContent }
    })
    expect(connections.title).toBe('Connections')
    expect(connections.rows).toBe(1)
    expect(connections.text.toLowerCase()).not.toContain('workspace')

    await page.goto(`${baseURL}/?root=detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const detail = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)!
      return { title: root.querySelector('h1')?.textContent?.trim(), tabs: Array.from(root.querySelectorAll('.tabs a')).map((tab: Element) => tab.textContent?.trim()), text: root.textContent }
    })
    expect(detail.title).toBe('orders')
    expect(detail.tabs).toEqual(expect.arrayContaining(['Overview', 'Definition']))
    expect(detail.text.toLowerCase()).not.toContain('workspace')

    await page.goto(`${baseURL}/?root=connection-detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const connection = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)!
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
    expect(connection.overview).toContain('Configured')
    expect(connection.panels).toEqual(['About', 'Connection state'])
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
      const root = (element.shadowRoot as ShadowRoot)!
      const viewer = root.querySelector('lv-config-viewer') as any
      await viewer?.updateComplete
      return {
        activeTab: root.querySelector('.tabs a[aria-current="page"]')?.textContent?.trim(),
        label: root.querySelector('#definition')?.getAttribute('aria-label'),
        sectionLabels: Array.from(root.querySelectorAll('#definition .semantic-model-navigation [data-definition-view]')).map((node: Element) => node.textContent?.trim()),
        configuration: viewer?.configuration,
        sqlRows: viewer?.shadowRoot?.querySelectorAll('.sql-row').length ?? 0,
        transformSections: root.querySelectorAll('.transform-section').length,
        sqlCode: (viewer?.shadowRoot?.querySelector('.sql-row lv-code-block')!)?.code,
      }
    })
    expect(definition.activeTab).toBe('Definition')
    expect(definition.label).toBe('Definition')
    expect(definition.sectionLabels).toEqual(['Source'])
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
      const root = (element.shadowRoot as ShadowRoot)!
      const runTable = root.querySelector('lv-record-table') as ProjectTableElement
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
      const root = (element.shadowRoot as ShadowRoot)!
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
      return { signal: element.signals.refreshRunDrawer, drawer: Boolean((element.shadowRoot as ShadowRoot)!.querySelector('lv-drawer')) }
    })
    expect(afterBack).toEqual({ signal: { open: false, runId: '' }, drawer: false })

    await page.goto(`${baseURL}/?root=model-refresh&refresh=run:model:orders`)
    await page.waitForFunction(() => Boolean((document.querySelector('lv-project-asset-page') as any)?.signals?.refreshRunDrawer?.open))
    expect(await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return (element.shadowRoot as ShadowRoot)!.querySelector('.refresh-run-drawer-title h1')?.textContent?.trim()
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
      const root = (element.shadowRoot as ShadowRoot)!
      const table = root.querySelector('lv-record-table') as ProjectTableElement
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
      const table = (element.shadowRoot as ShadowRoot)!.querySelector('lv-record-table') as ProjectTableElement
      await table?.updateComplete
      const additions = table?.querySelector('.record-diff-additions') as HTMLElement | null
      const deletions = table?.querySelector('.record-diff-deletions') as HTMLElement | null
      table?.querySelector<HTMLElement>('tbody tr.record-row')?.click()
      return {
        columns: table?.table?.columns?.map((column: any) => column.header),
        versions: table?.table?.rows?.map((row: any) => ({ version: row.version, contentHash: row.content_hash })),
        rowAction: table?.table?.rowAction,
        drawerBeforeSignal: Boolean((element.shadowRoot as ShadowRoot)!.querySelector('lv-drawer')),
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
      const root = (element.shadowRoot as ShadowRoot)!
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
      return { signal: element.signals.assetVersionDrawer, drawer: Boolean((element.shadowRoot as ShadowRoot)!.querySelector('lv-drawer')) }
    })).toEqual({ signal: { open: false, versionId: '' }, drawer: false })

    await page.goto(`${baseURL}/?root=model-versions&version=state:previous`)
    await page.waitForFunction(() => Boolean((document.querySelector('lv-project-asset-page') as any)?.signals?.assetVersionDrawer?.open))
    expect(await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)!
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
      const table = Array.from((element.shadowRoot as ShadowRoot)!.querySelectorAll('lv-record-table'))
        .find((candidate) => (candidate as ProjectTableElement).table?.rowAction === 'open-model-field') as ProjectTableElement | undefined
      await table?.updateComplete
      table?.querySelector<HTMLElement>('tbody tr.record-row')?.click()
      return {
        columns: table?.table?.columns?.map((column: any) => column.header),
        drawer: Boolean((element.shadowRoot as ShadowRoot)!.querySelector('lv-drawer')),
      }
    })
    expect(before.columns).toEqual(['Field', 'Type', 'Description', 'Status'])
    expect(before.drawer).toBe(false)
    await page.waitForFunction(() => new URL(location.href).searchParams.get('field') === 'customer_id')
    const desktop = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)!
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
        width: Math.round((drawer.shadowRoot as ShadowRoot)?.querySelector('.drawer')?.getBoundingClientRect().width ?? 0),
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
        drawer: Boolean((element.shadowRoot as ShadowRoot)!.querySelector('lv-drawer')),
      }
    })
    expect(afterBack).toEqual({ samePage: 'original', signal: { open: false, fieldKey: '' }, drawer: false })

    await page.evaluate(() => history.forward())
    await page.waitForFunction(() => new URL(location.href).searchParams.get('field') === 'customer_id')
    await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const drawer = (element.shadowRoot as ShadowRoot)!.querySelector('lv-drawer') as TestDomElement
      await drawer?.updateComplete
      drawer?.shadowRoot?.querySelector<HTMLButtonElement>('button.close')?.click()
    })
    await page.waitForFunction(() => !new URL(location.href).searchParams.has('field'))
    const afterClose = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return { signal: element.signals.modelFieldDrawer, drawer: Boolean((element.shadowRoot as ShadowRoot)!.querySelector('lv-drawer')) }
    })
    expect(afterClose).toEqual({ signal: { open: false, fieldKey: '' }, drawer: false })

    await page.goto(`${baseURL}/?root=model-field-drawer&field=customer_id`)
    await page.waitForFunction(() => Boolean((document.querySelector('lv-project-asset-page') as any)?.signals?.modelFieldDrawer?.open))
    const deepLink = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      return {
        signal: element.signals.modelFieldDrawer,
        title: (element.shadowRoot as ShadowRoot)!.querySelector('.field-drawer-title h1')?.textContent?.trim(),
      }
    })
    expect(deepLink).toEqual({ signal: { open: true, fieldKey: 'customer_id' }, title: 'customer_id' })

    await page.setViewportSize({ width: 390, height: 760 })
    const mobileWidth = await page.locator('lv-project-asset-page lv-drawer').evaluate(async (drawer: any) => {
      await drawer.updateComplete
      return Math.round((drawer.shadowRoot as ShadowRoot)?.querySelector('.drawer')?.getBoundingClientRect().width ?? 0)
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
      const root = (element.shadowRoot as ShadowRoot)!
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
    const panels = await host.evaluate(async (element: any) => {
      await element.updateComplete
      return Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('.semantic-overview-panel h2')).map((node) => node.textContent?.trim())
    })
    expect(panels).toEqual(['About'])
    const initial = await host.evaluate(async (element: any) => {
      await element.updateComplete
	  const editor = (element.shadowRoot as ShadowRoot)!.querySelector('lv-dashboard-appearance-editor') as TestDomElement
	  await editor.updateComplete
	  const root = (editor.shadowRoot as ShadowRoot)!
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
	  const editor = (element.shadowRoot as ShadowRoot)!.querySelector('lv-dashboard-appearance-editor') as TestDomElement
	  (editor.shadowRoot as ShadowRoot)!.querySelector<HTMLButtonElement>('.dashboard-appearance-edit')!.click()
	  await editor.updateComplete
	  const picker = (editor.shadowRoot as ShadowRoot)!.querySelector('lv-dashboard-icon-picker') as TestDomElement
      await picker.updateComplete
      ;(picker.shadowRoot as ShadowRoot)!.querySelector<HTMLButtonElement>('.color.color-orange')!.click()
      return selected
    })
    expect(detail).toEqual({ icon: 'chart-no-axes-combined', color: 'orange' })
    const optimistic = await host.evaluate(async (element: any) => {
	  const editor = (element.shadowRoot as ShadowRoot)!.querySelector('lv-dashboard-appearance-editor') as TestDomElement
	  await editor.updateComplete
	  const root = (editor.shadowRoot as ShadowRoot)!
      return {
        previewClass: root.querySelector('.dashboard-appearance-preview')?.className,
        status: root.querySelector('[role="status"]')?.textContent?.trim(),
      }
    })
    expect(optimistic.previewClass).toContain('appearance-color-orange')
    expect(optimistic.status).toBe('Saving appearance…')
    const failed = await host.evaluate(async (element: any) => {
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', argsRaw: { status: 503 } } }))
	  const editor = (element.shadowRoot as ShadowRoot)!.querySelector('lv-dashboard-appearance-editor') as TestDomElement
	  await editor.updateComplete
	  const root = (editor.shadowRoot as ShadowRoot)!
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
      const root = element.shadowRoot! as ShadowRoot
      return {
        views: Array.from(root.querySelectorAll<HTMLElement>('[data-definition-view]')).map((item) => item.dataset.definitionView),
        selected: root.querySelector<HTMLElement>('[data-definition-view][data-active="true"]')?.dataset.definitionView,
        navigationDirection: getComputedStyle(root.querySelector<HTMLElement>('.semantic-model-navigation')!).flexDirection,
        bodyPadding: getComputedStyle(root.querySelector<HTMLElement>('.section-body')!).paddingLeft,
        appearance: Boolean(root.querySelector('lv-dashboard-appearance-editor')),
      }
    })
    expect(definition.views).toEqual(['pages', 'filters', 'visuals', 'source'])
    expect(definition.selected).toBe('pages')
    expect(definition.navigationDirection).toBe('row')
    expect(definition.bodyPadding).toBe('0px')
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
      document.addEventListener('lv-run-refresh-pipeline', (event: Event) => { documentCommand = (event as CustomEvent).detail }, { once: true })
      const button = (element.shadowRoot as ShadowRoot)?.querySelector('button[aria-label="Run now"]') as HTMLButtonElement | null
      button?.dispatchEvent(new MouseEvent('click', { bubbles: true, composed: true }))
      return { command, documentCommand, button: Boolean(button), disabled: button?.disabled, labels: Array.from((element.shadowRoot as ShadowRoot)?.querySelectorAll('button') ?? []).map((candidate) => candidate.getAttribute('aria-label')) }
    })
    expect(detail).toEqual({ command: { action: 'run', assetId: 'pipeline:sales', pipelineId: 'pipeline:sales', runId: '' }, documentCommand: { action: 'run', assetId: 'pipeline:sales', pipelineId: 'pipeline:sales', runId: '' }, button: true, disabled: false, labels: ['Run now'] })
  } finally {
    await page.close()
  }
})

test('pipeline Overview reports executions independently of a published data snapshot', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipeline-detail`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const overview = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot! as ShadowRoot
      return {
        panels: Array.from(root.querySelectorAll('.semantic-overview-panel h2')).map((node: Element) => node.textContent?.trim()),
        status: root.querySelector('.semantic-overview-refresh-status strong')?.textContent?.trim(),
        recent: root.querySelector('.semantic-overview-recent-runs')?.textContent?.trim(),
        impact: Array.from(root.querySelectorAll('.semantic-overview-impact h2')).map((node: Element) => node.textContent?.trim()),
        target: root.querySelector<HTMLAnchorElement>('.semantic-overview-upstream-asset')?.getAttribute('href'),
        affected: root.querySelector<HTMLAnchorElement>('.semantic-overview-downstream-asset')?.getAttribute('href'),
      }
    })
    expect(overview.panels).toEqual(['About', 'Pipeline monitoring'])
    expect(overview.status).toBe('No runs recorded')
    expect(overview.recent).toContain('No pipeline runs have been recorded')
    expect(overview.impact).toEqual(['Refresh target', 'Affected assets'])
    expect(overview.target).toBe('/semantic-models/semantic-model:sales/details')
    expect(overview.affected).toBe('/dashboards/dashboard:executive-sales/details')
  } finally {
    await page.close()
  }
})

test('pipeline Overview exposes a failed run and its diagnostic link', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipeline-failed`)
    await page.waitForFunction(() => customElements.get('lv-project-asset-page'))
    const overview = await page.locator('lv-project-asset-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot! as ShadowRoot
      return {
        status: root.querySelector('.semantic-overview-refresh-status strong')?.textContent?.trim(),
        error: root.querySelector('.semantic-overview-guidance')?.textContent?.trim(),
        latest: root.querySelector<HTMLAnchorElement>('.semantic-overview-actions a')?.getAttribute('href'),
        runs: root.querySelectorAll('.semantic-overview-run-list li').length,
      }
    })
    expect(overview).toEqual({
      status: 'Failed',
      error: 'Source unavailable',
      latest: '/pipelines/pipeline:sales/refreshes?refresh=run%3Afailed',
      runs: 1,
    })
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
      const feedback = (element.shadowRoot as ShadowRoot)?.querySelector('[role="alert"]') as HTMLElement | null
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

test('pipeline catalog and run monitor are separate list surfaces without local tabs', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    await page.waitForFunction(() => customElements.get('lv-pipelines-page'))
    const catalog = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return { title: root.querySelector('h1')?.textContent?.trim(), metrics: root.querySelectorAll('.metrics').length, tabs: root.querySelectorAll('.tabs').length, lists: root.querySelectorAll('lv-entity-list').length }
    })
    expect(catalog).toEqual({ title: 'Pipelines', metrics: 0, tabs: 0, lists: 1 })

    await page.goto(`${baseURL}/?root=runs`)
    const monitor = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const form = root.querySelector('.run-toolbar') as HTMLFormElement
      return { title: root.querySelector('h1')?.textContent?.trim(), metrics: root.querySelectorAll('.metric').length, tabs: root.querySelectorAll('.tabs').length,
        filters: root.querySelectorAll('.run-toolbar input, .run-toolbar select').length, action: form?.getAttribute('action'),
        range: (form?.querySelector('[name="range"]') as HTMLSelectElement)?.value, pageLink: root.querySelector('.run-pagination a')?.getAttribute('href') }
    })
    expect(monitor).toEqual({ title: 'Runs', metrics: 3, tabs: 0, filters: 4, action: '/runs', range: '7d', pageLink: '/runs?q=sales&range=7d&status=failed&trigger=manual&page=1' })
    const detail = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.selectedRunID = 'run-failed'
      await element.updateComplete
      const root = element.shadowRoot!
      return { firstSection: root.querySelector('.run-detail-section h2')?.textContent?.trim(),
        actions: [...root.querySelectorAll('.run-detail-actions a, .run-detail-actions button')].map((item) => item.textContent?.trim()),
        error: root.querySelector('.run-detail-error')?.textContent?.trim() }
    })
    expect(detail).toEqual({ firstSection: 'Error', actions: ['View pipeline', 'Run again'], error: 'Source unavailable' })
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
      const admin = (element.shadowRoot as ShadowRoot)?.querySelector('lv-connection-administration') as any
      await admin?.updateComplete
      const configure = Array.from((admin.shadowRoot as ShadowRoot)?.querySelectorAll('button') ?? []).find((button: any) => button.textContent?.trim() === 'Configure') as HTMLButtonElement | undefined
      configure?.click()
      await new Promise<void>((resolve) => queueMicrotask(() => resolve()))
      await admin?.updateComplete
      const drawerOpenAfterClick = Boolean((admin.shadowRoot as ShadowRoot)?.querySelector('lv-drawer'))
      const form = (admin.shadowRoot as ShadowRoot)?.querySelector('form') as HTMLFormElement | null
      form?.requestSubmit()
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: document.body, argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(() => resolve()))
      await admin?.updateComplete
      const unrelatedIgnored = admin.terminalFailure == null
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(() => resolve()))
      await admin?.updateComplete
      const alert = (admin.shadowRoot as ShadowRoot)?.querySelector('[role="alert"]') as HTMLElement | null
      const retry = alert?.querySelector('button') as HTMLButtonElement | null
      const drawerOpenBeforeRetry = Boolean((admin.shadowRoot as ShadowRoot)?.querySelector('lv-drawer'))
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
