import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import validateVisualizationEnvelope from '../../generated/visualization/validate'
import { testDocument, testVisualizationEnvelopes } from './dashboard-page-test-fixtures'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/dashboard-page-test')

test('dashboard fixtures satisfy the fail-closed visualization contract', () => {
  for (const [id, envelope] of Object.entries(testVisualizationEnvelopes())) {
    if (!validateVisualizationEnvelope(envelope)) {
      throw new Error(`${id}: ${JSON.stringify((validateVisualizationEnvelope as typeof validateVisualizationEnvelope & { errors?: unknown }).errors)}`)
    }
  }
})

test('dashboard header exposes favorite and contextual actions without crowding the breadcrumb', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)
    const action = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      localStorage.removeItem('leapview.dashboard-catalog.favorites.v1')
      window.dispatchEvent(new StorageEvent('storage', { key: 'leapview.dashboard-catalog.favorites.v1' }))
      element.setAttribute('authoring-action-label', 'Continue editing')
      element.setAttribute('authoring-action-href', '/dashboards/executive-sales/edit?draft=draft-7&page=overview')
      await element.updateComplete
      const favorite = element.shadowRoot.querySelector('.dashboard-favorite') as HTMLButtonElement
      const trigger = element.shadowRoot.querySelector('.dashboard-options-trigger') as HTMLButtonElement
      const initialFavoriteLabel = favorite.getAttribute('aria-label')
      favorite.click()
      trigger.click()
      await element.updateComplete
      const link = element.shadowRoot.querySelector('.dashboard-options-menu a') as HTMLAnchorElement
      const open = trigger.getAttribute('aria-expanded')
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
      await element.updateComplete
      return {
        breadcrumb: Array.from(element.shadowRoot.querySelectorAll('.breadcrumb-label')).map((item: Element) => item.textContent?.trim()),
        headingOrder: Array.from(element.shadowRoot.querySelector('.dashboard-heading').children).map((item: Element) => item.className),
        controlsRemainOutsideUtilityActions: !element.shadowRoot.querySelector('.actions .dashboard-favorite, .actions .dashboard-options'),
        initialFavoriteLabel,
        favoriteLabel: favorite.getAttribute('aria-label'),
        favoritePressed: favorite.getAttribute('aria-pressed'),
        storedFavorites: JSON.parse(localStorage.getItem('leapview.dashboard-catalog.favorites.v1') ?? '[]'),
        triggerLabel: trigger.getAttribute('aria-label'),
        triggerHasPopup: trigger.getAttribute('aria-haspopup'),
        open,
        closed: trigger.getAttribute('aria-expanded'),
        label: link?.textContent?.trim(),
        href: link?.getAttribute('href'),
        directActionCount: element.shadowRoot.querySelectorAll('.authoring-action').length,
      }
    })
    expect(action).toEqual({
      breadcrumb: ['Dashboards', 'Executive Sales Dashboard'],
      headingOrder: ['breadcrumb', 'icon-button dashboard-favorite', 'dashboard-options'],
      controlsRemainOutsideUtilityActions: true,
      initialFavoriteLabel: 'Add Executive Sales Dashboard to favorites',
      favoriteLabel: 'Remove Executive Sales Dashboard from favorites',
      favoritePressed: 'true',
      storedFavorites: ['executive-sales'],
      triggerLabel: 'Dashboard options',
      triggerHasPopup: 'menu',
      open: 'true',
      closed: 'false',
      label: 'Continue editing',
      href: '/dashboards/executive-sales/edit?draft=draft-7&page=overview',
      directActionCount: 0,
    })
  } finally {
    await page.close()
  }
})

test('dashboard refresh loading does not mark unrelated filter controls stale', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)
    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const slicer = element.shadowRoot.querySelector('lv-slicer') as any
      await slicer.updateComplete
      const leaf = slicer.shadowRoot.querySelector('lv-filter-leaf') as any
      await leaf.updateComplete
      return {
        stale: leaf.stale,
        pending: leaf.pending,
        disabled: leaf.shadowRoot.querySelector('fieldset')?.disabled,
        status: leaf.shadowRoot.querySelector('.status')?.textContent?.trim(),
      }
    })
    expect(result).toEqual({ stale: false, pending: false, disabled: false, status: undefined })
  } finally {
    await page.close()
  }
})

test('dashboard coalesces duplicate option requests for one binding context', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)
    const stateFilter = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const seen: unknown[] = []
      element.addEventListener('lv-filter-options-request', (event: CustomEvent) => seen.push(event.detail))
      for (let index = 0; index < 2; index++) {
        element.dispatchEvent(new CustomEvent('lv-filter-options-needed', {
          bubbles: true,
          composed: true,
          detail: { bindingKey: 'fb_state', search: '', limit: 50 },
        }))
      }
      await element.updateComplete
      return {
        requests: seen,
        definition: element.signal('filterContract', {}).definitions?.state,
        binding: element.signal('filterContract', {}).bindings?.fb_state,
        page: element.signal('filterOptionPages', {}).fb_state,
        purchaseDateDefinition: element.signal('filterContract', {}).definitions?.purchase_date,
        purchaseDateBinding: element.signal('filterContract', {}).bindings?.fb_purchase_date,
      }
    })
    expect(stateFilter.requests).toHaveLength(1)
    expect(stateFilter.definition).toMatchObject({
      field: 'sales_orders.state',
      options: { kind: 'distinct', limit: 50, values: [] },
    })
    expect(stateFilter.binding).toMatchObject({ selectionMode: 'multiple', maxSelectedValues: 50 })
    expect(stateFilter.page).toMatchObject({
      bindingKey: 'fb_state', complete: true,
      items: [{ label: 'SP', available: true }],
    })
    expect(stateFilter.purchaseDateDefinition).toMatchObject({
      field: 'sales_orders.purchase_date', valueKind: 'date',
      predicates: [{ kind: 'range', operators: [] }],
      options: { kind: 'none', limit: 0, values: [] },
    })
    expect(stateFilter.purchaseDateBinding).toMatchObject({ scope: 'report', default: { kind: 'unfiltered' } })
  } finally {
    await page.close()
  }
})

test('dashboard categorical filter options expose their visible labels to assistive technology', async () => {
  const page = await browser.newPage({ viewport: { width: 800, height: 600 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const labels = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'status', label: 'Status', field: 'orders.status', valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'static', limit: 10, includeNull: false, values: [] },
      }
      leaf.binding = {
        key: 'fb_status', id: 'status', filter: 'status', scope: 'page', default: { kind: 'unfiltered' },
        selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true,
        paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'list', search: false, selectAll: false, showCounts: true, showSummary: false, compact: false,
      }
      leaf.options = {
        bindingKey: 'fb_status', items: [
          { value: { kind: 'string', value: 'paid' }, label: 'Paid', selected: false, available: true, count: 7 },
          { value: { kind: 'string', value: 'refunded' }, label: 'Refunded', selected: false, available: true },
        ], complete: true,
      }
      document.body.append(leaf)
      await leaf.updateComplete
      return Array.from(leaf.shadowRoot.querySelectorAll<HTMLInputElement>('input')).map((input) => input.getAttribute('aria-label'))
    })
    expect(labels).toEqual(['Paid (7)', 'Refunded'])
  } finally {
    await page.close()
  }
})

test('responsive report canvas derives browser geometry from canonical grid placement', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-report-canvas'))
    const geometry = await page.evaluate(async () => {
      const canvas = document.createElement('lv-report-canvas') as any
      canvas.style.width = '1200px'
      canvas.style.height = '720px'
      canvas.width = 0
      canvas.height = 0
      canvas.columns = 12
      canvas.rowHeight = 48
      canvas.gap = 16
      canvas.padding = 16
      const frame = (col: number, row: number, colSpan: number, rowSpan: number) => {
        const element = document.createElement('div')
        element.dataset.canvasVisual = ''
        element.dataset.col = String(col)
        element.dataset.row = String(row)
        element.dataset.colSpan = String(colSpan)
        element.dataset.rowSpan = String(rowSpan)
        canvas.append(element)
        return element
      }
      const left = frame(1, 1, 4, 2)
      const right = frame(5, 1, 8, 2)
      document.body.append(canvas)
      await canvas.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      return {
        canvas: canvas.getBoundingClientRect().toJSON(),
        left: left.getBoundingClientRect().toJSON(),
        right: right.getBoundingClientRect().toJSON(),
      }
    })
    expect(geometry.left.width).toBeGreaterThan(0)
    expect(geometry.right.width).toBeGreaterThan(geometry.left.width)
    expect(geometry.left.right).toBeLessThan(geometry.right.left)
    expect(geometry.right.right).toBeLessThanOrEqual(geometry.canvas.right - 15)
  } finally {
    await page.close()
  }
})

test('desktop canonical grids keep stable canvas geometry and scroll when the viewport narrows', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-report-canvas'))
    const geometry = await page.evaluate(async () => {
      const canvas = document.createElement('lv-report-canvas') as any
      canvas.style.width = '1000px'
      canvas.style.height = '600px'
      canvas.width = 0
      canvas.height = 0
      canvas.columns = 12
      canvas.rowHeight = 48
      canvas.gap = 16
      canvas.padding = 16
      const visual = document.createElement('div')
      visual.dataset.canvasVisual = ''
      visual.dataset.col = '1'
      visual.dataset.row = '1'
      visual.dataset.colSpan = '6'
      visual.dataset.rowSpan = '4'
      canvas.append(visual)
      document.body.append(canvas)
      await canvas.updateComplete
      document.dispatchEvent(new CustomEvent('lv-report-zoom-command', {
        detail: { layout: 'desktop', mode: 'actual-size' },
      }))
      await canvas.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))

      const viewport = canvas.shadowRoot.querySelector('.viewport') as HTMLElement
      const before = {
        left: visual.style.left,
        width: visual.style.width,
        horizontalScroll: viewport.scrollWidth > viewport.clientWidth,
      }
      canvas.style.width = '800px'
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      return {
        before,
        after: {
          left: visual.style.left,
          width: visual.style.width,
          horizontalScroll: viewport.scrollWidth > viewport.clientWidth,
        },
      }
    })

    expect(geometry.after.left).toBe(geometry.before.left)
    expect(geometry.after.width).toBe(geometry.before.width)
    expect(geometry.before.horizontalScroll).toBe(true)
    expect(geometry.after.horizontalScroll).toBe(true)
  } finally {
    await page.close()
  }
})

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
    if (!file.startsWith(fileRoot)) { response.writeHead(404); response.end('not found'); return }
    try {
      response.setHeader('content-type', file.endsWith('.css') ? 'text/css' : 'text/javascript')
      response.end(await readFile(file))
    } catch { response.writeHead(404); response.end('not found') }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('embed presentation keeps page navigation and removes non-navigation chrome', async () => {
  const page = await browser.newPage({ viewport: { width: 863, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.title === 'Executive Sales Dashboard')
    const state = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      element.presentation = 'embed'
      await element.updateComplete
      const root = element.shadowRoot
      const visible = (selector: string) => {
        const node = root.querySelector(selector) as HTMLElement | null
        return Boolean(node && getComputedStyle(node).display !== 'none')
      }
      const canvas = root.querySelector('lv-report-canvas') as HTMLElement
      return {
        reflected: element.getAttribute('presentation'),
        sidebarVisible: visible('lv-sub-sidebar'),
        backLinkCount: root.querySelector('lv-sub-sidebar')?.shadowRoot?.querySelectorAll('.back-link').length ?? 0,
        headerVisible: visible('.header'),
        footerVisible: visible('lv-report-footer'),
        hasAgentToggle: Boolean(root.querySelector('.agent-toggle')),
        hasAgentDrawer: Boolean(root.querySelector('lv-chat-drawer')),
        attribution: root.querySelector('.publication-attribution')?.textContent?.trim(),
        attributionHref: root.querySelector('.publication-attribution')?.getAttribute('href'),
        agentActionCount: root.querySelectorAll('.ask-visual').length,
        canvasWidth: canvas.getBoundingClientRect().width,
        documentOverflow: document.documentElement.scrollWidth - window.innerWidth,
      }
    })
    expect(state.reflected).toBe('embed')
    expect(state.sidebarVisible).toBe(true)
    expect(state.backLinkCount).toBe(0)
    expect(state.headerVisible).toBe(false)
    expect(state.footerVisible).toBe(false)
    expect(state.hasAgentToggle).toBe(false)
    expect(state.hasAgentDrawer).toBe(false)
    expect(state.attribution).toBe('Powered by LeapView')
    expect(state.attributionHref).toBe('https://leapview.dev')
    expect(state.agentActionCount).toBe(0)
    expect(state.canvasWidth).toBeGreaterThan(500)
    expect(state.documentOverflow).toBe(0)
  } finally {
    await page.close()
  }
})

test('app report frame uses a settings-style searchable page sidebar with Back at the top', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => localStorage.setItem('leapview-report-sidebar-collapsed', 'false'))
    await page.goto(baseURL)
    await page.waitForFunction(() => (
      customElements.get('lv-dashboard-page')
        && customElements.get('lv-sub-sidebar')
        && (document.querySelector('lv-dashboard-page') as any)?.page
    ))
    await page.waitForTimeout(250)

    const state = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const sidebar = element.shadowRoot.querySelector('lv-sub-sidebar') as any
      await sidebar.updateComplete
      const root = sidebar.shadowRoot!
      const reportHeader = element.shadowRoot.querySelector('.header') as HTMLElement
      const railFooter = root.querySelector('.sidebar-footer') as HTMLElement
      const back = root.querySelector('.back-link') as HTMLAnchorElement
      const backLabel = back.querySelector('.back-label') as HTMLElement
      const search = root.querySelector('.sidebar-search input') as HTMLInputElement
      const collapse = root.querySelector('.collapse') as HTMLButtonElement
      const header = root.querySelector('header') as HTMLElement
      const firstPage = root.querySelector('.item-link') as HTMLElement
      const main = element.shadowRoot.querySelector('.main') as HTMLElement
      const reportFooter = element.shadowRoot.querySelector('lv-report-footer') as HTMLElement
      const breadcrumb = reportHeader.querySelector('.breadcrumb') as HTMLElement
      const title = breadcrumb.querySelector('h1') as HTMLElement
      const dashboardGlyph = breadcrumb.querySelector('.dashboard-appearance-glyph') as HTMLElement
      const expandedWidth = Math.round(sidebar.getBoundingClientRect().width)
      const expandedPageTop = Math.round(firstPage.getBoundingClientRect().top)
      const backIconMarkup = back.querySelector('svg')?.innerHTML
      const expandedToggleIconMarkup = collapse.querySelector('svg')?.innerHTML
      const reportHeaderRect = reportHeader.getBoundingClientRect()
      const railFooterRect = railFooter.getBoundingClientRect()
      const backRect = back.getBoundingClientRect()
      const expandedBackLabelDisplay = getComputedStyle(backLabel).display
      const sidebarRect = sidebar.getBoundingClientRect()
      const mainRect = main.getBoundingClientRect()
      const breadcrumbRect = breadcrumb.getBoundingClientRect()
      const reportFooterRect = reportFooter.getBoundingClientRect()
      const searchRect = search.getBoundingClientRect()
      search.value = 'det'
      search.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await sidebar.updateComplete
      const filteredPages = Array.from(root.querySelectorAll('.item-title')).map(item => item.textContent?.trim())
      search.value = ''
      search.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await sidebar.updateComplete
      collapse.click()
      await sidebar.updateComplete
      await new Promise(resolve => setTimeout(resolve, 200))
      const collapsedPageTop = Math.round(
        (root.querySelector('.item-link') as HTMLElement).getBoundingClientRect().top,
      )
      const collapsedToggleIconMarkup = root.querySelector('.collapse svg')?.innerHTML
      const collapsedMainRect = main.getBoundingClientRect()
      const collapsedRailFooterRect = railFooter.getBoundingClientRect()
      const collapsedBackRect = back.getBoundingClientRect()
      const collapsedBackLabelDisplay = getComputedStyle(backLabel).display
      const collapsedBreadcrumbRect = breadcrumb.getBoundingClientRect()
      const collapsedSidebarRect = sidebar.getBoundingClientRect()
      const collapsedReportFooterRect = reportFooter.getBoundingClientRect()
      return {
        href: back.getAttribute('href'),
        label: back.getAttribute('aria-label'),
        text: back.textContent?.trim(),
        backTag: back.tagName,
        title: back.getAttribute('title'),
        expandedBackLabelDisplay,
        collapsedBackLabelDisplay,
        reportTitle: title.textContent?.trim(),
        reportTitleCount: element.shadowRoot.querySelectorAll('h1').length,
        breadcrumbLabel: breadcrumb.getAttribute('aria-label'),
        breadcrumbItems: Array.from(breadcrumb.querySelectorAll('.breadcrumb-item')).map(item => ({
          text: item.querySelector('.breadcrumb-label')?.textContent?.trim(),
          href: item.querySelector('a')?.getAttribute('href') ?? null,
          current: item.getAttribute('aria-current'),
        })),
        breadcrumbSeparatorCount: breadcrumb.querySelectorAll('.breadcrumb-separator').length,
        dashboardGlyph: {
          icon: dashboardGlyph.getAttribute('data-icon'),
          color: dashboardGlyph.getAttribute('data-color'),
          ariaHidden: dashboardGlyph.getAttribute('aria-hidden'),
          width: Math.round(dashboardGlyph.getBoundingClientRect().width),
          height: Math.round(dashboardGlyph.getBoundingClientRect().height),
          background: getComputedStyle(dashboardGlyph).backgroundColor,
          borderWidth: getComputedStyle(dashboardGlyph).borderTopWidth,
          gap: getComputedStyle(dashboardGlyph.parentElement!).gap,
          svgCount: dashboardGlyph.querySelectorAll('svg').length,
        },
        sidebarTitleCount: root.querySelectorAll('.sidebar-title').length,
        sidebarBackCount: root.querySelectorAll('.back-link').length,
        backInRailFooter: railFooter.contains(back),
        railFooterAligned: Math.round(railFooterRect.left) === Math.round(sidebarRect.left),
        backInset: Math.round(backRect.left - sidebarRect.left),
        backAtTop: backRect.top < expandedPageTop,
        searchBelowBack: searchRect.top >= backRect.bottom,
        searchLabel: search.getAttribute('aria-label'),
        searchPlaceholder: search.getAttribute('placeholder'),
        filteredPages,
        reportHeaderAligned: Math.round(reportHeaderRect.left) === Math.round(mainRect.left),
        breadcrumbInset: Math.round(breadcrumbRect.left - mainRect.left),
        sidebarStartsWithHeader: Math.abs(sidebarRect.top - reportHeaderRect.top) < 2,
        mainBelowHeader: Math.abs(mainRect.top - reportHeaderRect.bottom) < 2,
        railHeaderCount: element.shadowRoot.querySelectorAll('.rail-header').length,
        sidebarFooterAtBottom: Math.abs(sidebarRect.bottom - railFooterRect.bottom) < 2,
        railFooterMatchesReportFooter: Math.abs(railFooterRect.top - reportFooterRect.top) < 2
          && Math.abs(railFooterRect.bottom - reportFooterRect.bottom) < 2,
        footerAligned: Math.round(reportFooterRect.left) === Math.round(mainRect.left),
        collapsedSidebarFooterAtBottom: Math.abs(collapsedSidebarRect.bottom - collapsedRailFooterRect.bottom) < 2,
        collapsedRailFooterMatchesReportFooter: Math.abs(collapsedRailFooterRect.top - collapsedReportFooterRect.top) < 2
          && Math.abs(collapsedRailFooterRect.bottom - collapsedReportFooterRect.bottom) < 2,
        collapsedBreadcrumbInset: Math.round(collapsedBreadcrumbRect.left - collapsedMainRect.left),
        collapsedFooterAligned: Math.round(collapsedReportFooterRect.left) === Math.round(collapsedMainRect.left),
        collapsedBackCentered: Math.abs(
          (collapsedBackRect.left + collapsedBackRect.width / 2)
            - (collapsedSidebarRect.left + collapsedSidebarRect.width / 2),
        ) < 2,
        collapsedBackWidth: Math.round(collapsedBackRect.width),
        breadcrumbMovesWithCanvas: Math.round(collapsedBreadcrumbRect.left - breadcrumbRect.left)
          === Math.round(collapsedMainRect.left - mainRect.left),
        collapseInHeader: header.contains(collapse),
        expandedWidth,
        collapseTag: collapse.tagName,
        collapseLabel: collapse.getAttribute('aria-label'),
        collapsed: sidebar.hasAttribute('data-collapsed'),
        railLabelDisplay: getComputedStyle(root.querySelector('.rail-label')).display,
        collapsedPageMovesUp: collapsedPageTop < expandedPageTop,
        toggleIconDistinctFromBack: expandedToggleIconMarkup !== backIconMarkup,
        toggleIconChanges: collapsedToggleIconMarkup !== expandedToggleIconMarkup,
      }
    })

    expect(state).toEqual({
      href: '/',
      label: 'Back to dashboards',
      text: 'Back',
      backTag: 'A',
      title: 'Back to dashboards',
      expandedBackLabelDisplay: 'block',
      collapsedBackLabelDisplay: 'none',
      reportTitle: 'Executive Sales Dashboard',
      reportTitleCount: 1,
      breadcrumbLabel: 'Breadcrumb',
      breadcrumbItems: [
        { text: 'Dashboards', href: '/', current: null },
        { text: 'Executive Sales Dashboard', href: null, current: 'page' },
      ],
      breadcrumbSeparatorCount: 1,
      dashboardGlyph: {
        icon: 'gallery-vertical-end',
        color: 'blue',
        ariaHidden: 'true',
        width: 16,
        height: 16,
        background: 'rgba(0, 0, 0, 0)',
        borderWidth: '0px',
        gap: '8px',
        svgCount: 1,
      },
      sidebarTitleCount: 0,
      sidebarBackCount: 1,
      backInRailFooter: false,
      railFooterAligned: true,
      backInset: 8,
      backAtTop: true,
      searchBelowBack: true,
      searchLabel: 'Search pages',
      searchPlaceholder: 'Search pages',
      filteredPages: ['Details'],
      reportHeaderAligned: true,
      breadcrumbInset: 16,
      sidebarStartsWithHeader: true,
      mainBelowHeader: true,
      railHeaderCount: 0,
      sidebarFooterAtBottom: true,
      railFooterMatchesReportFooter: true,
      footerAligned: true,
      collapsedSidebarFooterAtBottom: true,
      collapsedRailFooterMatchesReportFooter: true,
      collapsedBreadcrumbInset: 16,
      collapsedFooterAligned: true,
      collapsedBackCentered: true,
      collapsedBackWidth: 28,
      breadcrumbMovesWithCanvas: true,
      collapseInHeader: false,
      expandedWidth: 144,
      collapseTag: 'BUTTON',
      collapseLabel: 'Expand Report pages',
      collapsed: true,
      railLabelDisplay: 'none',
      collapsedPageMovesUp: true,
      toggleIconDistinctFromBack: true,
      toggleIconChanges: true,
    })
  } finally {
    await page.close()
  }
})

test('report pages rail resizes accessibly and persists its expanded width', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => {
      if (sessionStorage.getItem('leapview-report-sidebar-resize-test-ready')) return
      sessionStorage.setItem('leapview-report-sidebar-resize-test-ready', 'true')
      localStorage.setItem('leapview-report-sidebar-collapsed', 'false')
      localStorage.removeItem('leapview-report-sidebar-width')
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => (
      customElements.get('lv-dashboard-page')
        && customElements.get('lv-sub-sidebar')
        && (document.querySelector('lv-dashboard-page') as any)?.page
    ))

    const keyboardState = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const sidebar = element.shadowRoot.querySelector('lv-sub-sidebar') as any
      await sidebar.updateComplete
      const handle = sidebar.shadowRoot.querySelector('.resize-handle') as HTMLElement
      handle.dispatchEvent(new KeyboardEvent('keydown', {
        key: 'ArrowRight', bubbles: true, cancelable: true,
      }))
      await sidebar.updateComplete
      return {
        label: handle.getAttribute('aria-label'),
        orientation: handle.getAttribute('aria-orientation'),
        role: handle.getAttribute('role'),
        tabIndex: handle.tabIndex,
        value: handle.getAttribute('aria-valuenow'),
      }
    })
    expect(keyboardState).toEqual({
      label: 'Resize report pages',
      orientation: 'vertical',
      role: 'separator',
      tabIndex: 0,
      value: '152',
    })
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement
      const sidebar = dashboard?.shadowRoot?.querySelector('lv-sub-sidebar') as HTMLElement
      return Math.round(sidebar?.getBoundingClientRect().width ?? 0) === 152
    })
    expect(await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sub-sidebar') as HTMLElement
      const main = element.shadowRoot.querySelector('.main') as HTMLElement
      const header = element.shadowRoot.querySelector('.header') as HTMLElement
      const footer = element.shadowRoot.querySelector('lv-report-footer') as HTMLElement
      return [main, header, footer].every(node => (
        Math.round(node.getBoundingClientRect().left) === Math.round(sidebar.getBoundingClientRect().right)
      ))
    })).toBe(true)

    const handleBox = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sub-sidebar') as HTMLElement
      const handle = sidebar.shadowRoot!.querySelector('.resize-handle') as HTMLElement
      const box = handle.getBoundingClientRect()
      return { x: box.x, y: box.y, width: box.width, height: box.height }
    })
    await page.mouse.move(handleBox.x + handleBox.width / 2, handleBox.y + 100)
    await page.mouse.down()
    await page.mouse.move(handleBox.x + handleBox.width / 2 + 32, handleBox.y + 100)
    await page.mouse.up()
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement
      const sidebar = dashboard?.shadowRoot?.querySelector('lv-sub-sidebar') as HTMLElement
      return Math.round(sidebar?.getBoundingClientRect().width ?? 0) === 184
    })

    expect(await page.evaluate(() => localStorage.getItem('leapview-report-sidebar-width'))).toBe('184')
    await page.reload()
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as HTMLElement
      const sidebar = dashboard?.shadowRoot?.querySelector('lv-sub-sidebar') as HTMLElement
      return Math.round(sidebar?.getBoundingClientRect().width ?? 0) === 184
    })

    const collapsedState = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const sidebar = element.shadowRoot.querySelector('lv-sub-sidebar') as any
      await sidebar.updateComplete
      const collapse = sidebar.shadowRoot.querySelector('.collapse') as HTMLButtonElement
      const handle = sidebar.shadowRoot.querySelector('.resize-handle') as HTMLElement
      collapse.click()
      await sidebar.updateComplete
      await new Promise(resolve => setTimeout(resolve, 200))
      return {
        width: Math.round(sidebar.getBoundingClientRect().width),
        handleDisplay: getComputedStyle(handle).display,
      }
    })
    expect(collapsedState).toEqual({ width: 38, handleDisplay: 'none' })
  } finally {
    await page.evaluate(() => {
      localStorage.removeItem('leapview-report-sidebar-width')
      localStorage.removeItem('leapview-report-sidebar-collapsed')
    }).catch(() => undefined)
    await page.close()
  }
}, 15_000)

test('narrow dashboards let viewers preserve the desktop canvas with internal scrollbars', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (
      customElements.get('lv-dashboard-page')
        && customElements.get('lv-report-zoom')
        && (document.querySelector('lv-dashboard-page') as any)?.page
    ))

    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const footer = element.shadowRoot.querySelector('lv-report-footer') as any
      await footer.updateComplete
      const view = footer.shadowRoot.querySelector('lv-report-zoom') as any
      const canvas = element.shadowRoot.querySelector('lv-report-canvas') as any
      await Promise.all([view.updateComplete, canvas.updateComplete])
      const details = view.shadowRoot.querySelector('[data-control="layout"]') as HTMLDetailsElement
      details.open = true
      await view.updateComplete
      const control = view.shadowRoot.querySelector('[data-layout="desktop"]') as HTMLButtonElement
      control.click()
      await Promise.all([view.updateComplete, canvas.updateComplete])
      details.open = true
      await view.updateComplete
      ;(view.shadowRoot.querySelector('[data-mode="actual-size"]') as HTMLButtonElement).click()
      await Promise.all([view.updateComplete, canvas.updateComplete])
      await new Promise(requestAnimationFrame)
      const surface = canvas.shadowRoot.querySelector('.surface') as HTMLElement
      const viewport = canvas.shadowRoot.querySelector('.viewport') as HTMLElement
      const assigned = (canvas.shadowRoot.querySelector('slot') as HTMLSlotElement).assignedElements() as HTMLElement[]
      const chart = assigned.find((item) => item.dataset.visualType === 'bar')?.getBoundingClientRect()
      const table = assigned.find((item) => item.dataset.visualType === 'table')?.getBoundingClientRect()
      return {
        controlDisplay: getComputedStyle(view).display,
        controlHeight: Math.round(control.getBoundingClientRect().height),
        headerControl: Boolean(element.shadowRoot.querySelector('lv-report-view')),
        bottomControl: Boolean(footer.shadowRoot.querySelector('lv-report-zoom')),
        layout: surface.dataset.layout,
        mode: surface.dataset.presentationMode,
        horizontalScroll: viewport.scrollWidth > viewport.clientWidth,
        verticalScroll: viewport.scrollHeight > viewport.clientHeight,
        chartAndTableKeepCanvasPositions: (table?.top ?? 0) > (chart?.top ?? 0) + 300,
        stored: localStorage.getItem('leapview-report-layout:/'),
      }
    })

    expect(result).toEqual({
      controlDisplay: 'block',
      controlHeight: 32,
      headerControl: false,
      bottomControl: true,
      layout: 'desktop',
      mode: 'actual-size',
      horizontalScroll: true,
      verticalScroll: true,
      chartAndTableKeepCanvasPositions: true,
      stored: 'desktop',
    })
  } finally {
    await page.close()
  }
})

test('fit width never exposes a horizontal canvas scrollbar when vertical scrolling is needed', async () => {
  const page = await browser.newPage({ viewport: { width: 640, height: 620 } })
  try {
    await page.addInitScript(() => {
      localStorage.setItem('leapview-report-layout:/', 'desktop')
      localStorage.setItem('leapview-report-zoom:/', 'actual-size')
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)

    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const footer = element.shadowRoot.querySelector('lv-report-footer') as any
      await footer.updateComplete
      const toolbar = footer.shadowRoot.querySelector('lv-report-zoom') as any
      const canvas = element.shadowRoot.querySelector('lv-report-canvas') as any
      await Promise.all([toolbar.updateComplete, canvas.updateComplete])
      ;(toolbar.shadowRoot.querySelector('[data-mode="fit-width"]') as HTMLButtonElement).click()
      await Promise.all([toolbar.updateComplete, canvas.updateComplete])
      await new Promise(requestAnimationFrame)
      await new Promise(requestAnimationFrame)
      const surface = canvas.shadowRoot.querySelector('.surface') as HTMLElement
      const viewport = canvas.shadowRoot.querySelector('.viewport') as HTMLElement
      const frame = canvas.shadowRoot.querySelector('.frame-wrap') as HTMLElement
      const viewportRect = viewport.getBoundingClientRect()
      const frameRect = frame.getBoundingClientRect()
      return {
        mode: surface.dataset.presentationMode,
        overflowX: getComputedStyle(viewport).overflowX,
        scrollbarGutter: getComputedStyle(viewport).scrollbarGutter,
        horizontalOverflow: viewport.scrollWidth - viewport.clientWidth,
        verticalScroll: viewport.scrollHeight > viewport.clientHeight,
        frameWithinViewport: frameRect.right <= viewportRect.right,
      }
    })

    expect(result).toEqual({
      mode: 'fit-width',
      overflowX: 'hidden',
      scrollbarGutter: 'stable',
      horizontalOverflow: 0,
      verticalScroll: true,
      frameWithinViewport: true,
    })
  } finally {
    await page.close()
  }
})

test('canvas dropdown popovers follow the authored report zoom scale', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => {
      localStorage.setItem('leapview-report-layout:/', 'desktop')
      localStorage.setItem('leapview-report-zoom:/', 'custom')
      localStorage.setItem('leapview-report-zoom-scale:/', '0.5')
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)

    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const canvas = element.shadowRoot.querySelector('lv-report-canvas') as any
      const slicer = element.shadowRoot.querySelector('lv-slicer') as any
      await Promise.all([canvas.updateComplete, slicer.updateComplete])
      const leaf = slicer.shadowRoot.querySelector('lv-filter-leaf') as any
      await leaf.updateComplete
      const trigger = leaf.shadowRoot.querySelector('.dropdown-trigger') as HTMLElement
      trigger.click()
      await leaf.updateComplete
      await new Promise(requestAnimationFrame)
      const popover = leaf.shadowRoot.querySelector('.dropdown-popover') as HTMLElement
      const triggerRect = trigger.getBoundingClientRect()
      const popoverRect = popover.getBoundingClientRect()
      const surface = canvas.shadowRoot.querySelector('.surface') as HTMLElement
      return {
        canvasScale: Number(surface.dataset.scale),
        triggerScale: triggerRect.width / trigger.offsetWidth,
        popoverScale: popoverRect.width / popover.offsetWidth,
        popoverTransform: popover.style.transform,
        visualGap: popoverRect.top - triggerRect.bottom,
      }
    })

    expect(result.canvasScale).toBe(0.5)
    expect(result.triggerScale).toBeCloseTo(0.5)
    expect(result.popoverScale).toBeCloseTo(result.triggerScale)
    expect(result.popoverTransform).toBe('scale(0.5)')
    expect(result.visualGap).toBeCloseTo(2)
  } finally {
    await page.evaluate(() => {
      localStorage.removeItem('leapview-report-layout:/')
      localStorage.removeItem('leapview-report-zoom:/')
      localStorage.removeItem('leapview-report-zoom-scale:/')
    }).catch(() => undefined)
    await page.close()
  }
})

test('bottom report toolbar separates layout, fit actions, and zoom presets on compact screens', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.addInitScript(() => {
      localStorage.removeItem('leapview-report-layout:/')
      localStorage.removeItem('leapview-report-zoom:/')
      localStorage.removeItem('leapview-report-zoom-scale:/')
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)

    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const footer = element.shadowRoot.querySelector('lv-report-footer') as any
      await footer.updateComplete
      const toolbar = footer.shadowRoot.querySelector('lv-report-zoom') as any
      const canvas = element.shadowRoot.querySelector('lv-report-canvas') as any
      await Promise.all([toolbar.updateComplete, canvas.updateComplete])
      const root = toolbar.shadowRoot
      const layoutMenu = root.querySelector('[data-control="layout"]') as HTMLDetailsElement
      const zoomMenu = root.querySelector('[data-control="zoom-presets"]') as HTMLDetailsElement
      const slider = root.querySelector('.slider') as HTMLElement
      const controls = root.querySelector('.zoom') as HTMLElement
      const initialLayoutTriggerLabel = root.querySelector('[data-control="layout"] summary')?.getAttribute('aria-label')
      const layoutHasIcon = Boolean(root.querySelector('[data-control="layout"] summary svg'))

      layoutMenu.open = true
      await toolbar.updateComplete
      ;(root.querySelector('[data-layout="desktop"]') as HTMLButtonElement).click()
      await Promise.all([toolbar.updateComplete, canvas.updateComplete])

      ;(root.querySelector('[data-mode="fit-page"]') as HTMLButtonElement).click()
      await Promise.all([toolbar.updateComplete, canvas.updateComplete])
      await new Promise(requestAnimationFrame)
      const fitPageSurface = canvas.shadowRoot.querySelector('.surface') as HTMLElement
      const fitPageMode = fitPageSurface.dataset.presentationMode
      const fitPageScale = Number(fitPageSurface.dataset.scale)
      const fitPageSelected = root.querySelector('[data-mode="fit-page"]')?.getAttribute('aria-pressed')

      zoomMenu.open = true
      await toolbar.updateComplete
      ;(root.querySelector('[data-scale="1.25"]') as HTMLButtonElement).click()
      await Promise.all([toolbar.updateComplete, canvas.updateComplete])
      await new Promise(requestAnimationFrame)

      const surface = canvas.shadowRoot.querySelector('.surface') as HTMLElement
      return {
        layoutTriggerLabel: initialLayoutTriggerLabel,
        layoutHasIcon,
        layoutOptions: Array.from(root.querySelectorAll('[data-layout]')).map((node: any) => node.dataset.layout),
        fitActions: Array.from(root.querySelectorAll('.fit-action')).map((node: any) => node.dataset.mode),
        presetValues: Array.from(root.querySelectorAll('[data-scale]')).map((node: any) => node.dataset.scale),
        percent: root.querySelector('[data-control="zoom-presets"] summary')?.textContent?.trim(),
        sliderDisplay: getComputedStyle(slider).display,
        toolbarOverflow: controls.scrollWidth - controls.clientWidth,
        fitPageMode,
        fitPageScale,
        fitPageSelected,
        layout: surface.dataset.layout,
        mode: surface.dataset.presentationMode,
        scale: Number(surface.dataset.scale),
        zoomMenuOpen: zoomMenu.open,
      }
    })

    expect(result).toEqual({
      layoutTriggerLabel: 'Layout, Auto, currently Mobile',
      layoutHasIcon: true,
      layoutOptions: ['auto', 'desktop', 'mobile'],
      fitActions: ['fit-width', 'fit-page', 'actual-size'],
      presetValues: ['0.5', '0.75', '1', '1.25', '1.5', '2'],
      percent: '125%',
      sliderDisplay: 'none',
      toolbarOverflow: 0,
      fitPageMode: 'fit-page',
      fitPageScale: expect.any(Number),
      fitPageSelected: 'true',
      layout: 'desktop',
      mode: 'custom',
      scale: 1.25,
      zoomMenuOpen: false,
    })
    expect(result.fitPageScale).toBeLessThan(0.5)
  } finally {
    await page.close()
  }
})

test('compact report footers keep refresh failures visible and announced without overlapping view controls', async () => {
  const page = await browser.newPage({ viewport: { width: 760, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)

    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      await element.updateComplete
      const footerHost = element.shadowRoot.querySelector('lv-report-footer') as any
      await footerHost.updateComplete
      const root = footerHost.shadowRoot
      const footer = root.querySelector('footer') as HTMLElement
      const status = root.querySelector('.status') as HTMLElement
      const controls = root.querySelector('lv-report-zoom') as HTMLElement
      const idleStatusDisplay = getComputedStyle(status).display

      mergePatch({ status: { loading: false, error: 'refresh failed' } })
      await element.updateComplete
      await footerHost.updateComplete

      const statusRect = status.getBoundingClientRect()
      const controlsRect = controls.getBoundingClientRect()
      return {
        footerWidth: Math.round(footer.getBoundingClientRect().width),
        idleStatusDisplay,
        errorVisible: statusRect.width > 0 && statusRect.height > 0,
        errorText: status.innerText.trim(),
        errorRole: status.getAttribute('role'),
        errorLive: status.getAttribute('aria-live'),
        errorAtomic: status.getAttribute('aria-atomic'),
        controlsWithinFooter: controlsRect.right <= footer.getBoundingClientRect().right,
        overlap: statusRect.width > 0 && statusRect.right > controlsRect.left,
      }
    })

    expect(result.footerWidth).toBeLessThanOrEqual(800)
    expect(result.idleStatusDisplay).toBe('none')
    expect(result.errorVisible).toBe(true)
    expect(result.errorText).toBe('Refresh failed')
    expect(result.errorRole).toBe('alert')
    expect(result.errorLive).toBe('assertive')
    expect(result.errorAtomic).toBe('true')
    expect(result.controlsWithinFooter).toBe(true)
    expect(result.overlap).toBe(false)
  } finally {
    await page.close()
  }
})

test('mobile layout pins the bottom toolbar while the stacked report content scrolls', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 600 } })
  try {
    await page.addInitScript(() => localStorage.setItem('leapview-report-layout:/', 'mobile'))
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)

    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const canvas = element.shadowRoot.querySelector('lv-report-canvas') as any
      await canvas.updateComplete
      await new Promise(requestAnimationFrame)
      const surface = canvas.shadowRoot.querySelector('.surface') as HTMLElement
      const body = element.shadowRoot.querySelector('.body') as HTMLElement
      const footer = element.shadowRoot.querySelector('lv-report-footer') as HTMLElement
      const footerBottomBefore = footer.getBoundingClientRect().bottom
      body.scrollTo({ top: body.scrollHeight, behavior: 'instant' })
      await new Promise(requestAnimationFrame)
      return {
        layout: surface.dataset.layout,
        bodyOverflowY: getComputedStyle(body).overflowY,
        bodyClientHeight: body.clientHeight,
        bodyScrollHeight: body.scrollHeight,
        bodyScrollTop: body.scrollTop,
        bodyTabIndex: body.tabIndex,
        bodyLabel: body.getAttribute('aria-label'),
        footerBottomBefore,
        footerBottomAfter: footer.getBoundingClientRect().bottom,
        viewportHeight: window.innerHeight,
        horizontalOverflow: document.documentElement.scrollWidth - window.innerWidth,
      }
    })

    expect(result.layout).toBe('mobile')
    expect(result.bodyOverflowY).toBe('auto')
    expect(result.bodyScrollHeight).toBeGreaterThan(result.bodyClientHeight)
    expect(result.bodyScrollTop).toBeGreaterThan(0)
    expect(result.bodyTabIndex).toBe(0)
    expect(result.bodyLabel).toBe('Scrollable report content')
    expect(Math.round(result.footerBottomBefore)).toBe(result.viewportHeight)
    expect(Math.round(result.footerBottomAfter)).toBe(result.viewportHeight)
    expect(result.horizontalOverflow).toBe(0)
  } finally {
    await page.close()
  }
})

test('the closed filter control follows scrolling in Mobile layout', async () => {
  const page = await browser.newPage({ viewport: { width: 863, height: 700 } })
  try {
    await page.addInitScript(() => {
      localStorage.setItem('leapview-report-layout:/', 'mobile')
      localStorage.setItem('leapview:filters-open', 'closed')
    })
    await page.goto(baseURL)
    await page.waitForLoadState('networkidle')
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)

    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const body = root.querySelector('.body') as HTMLElement
      const dock = root.querySelector('lv-filter-dock') as any
      await dock.updateComplete
      const rail = dock.shadowRoot.querySelector('button.rail') as HTMLElement
      const bodyRect = body.getBoundingClientRect()
      const before = rail.getBoundingClientRect()
      const dockPosition = getComputedStyle(dock).position
      body.scrollTop = body.scrollHeight
      await new Promise(requestAnimationFrame)
      const after = rail.getBoundingClientRect()
      const scrolled = body.scrollTop > 0
      rail.click()
      await dock.updateComplete
      const panel = dock.shadowRoot.querySelector('.panel') as HTMLElement
      return {
        dockPosition,
        bodyTop: Math.round(bodyRect.top),
        bodyBottom: Math.round(bodyRect.bottom),
        beforeTop: Math.round(before.top),
        afterTop: Math.round(after.top),
        afterBottom: Math.round(after.bottom),
        scrolled,
        expanded: rail.getAttribute('aria-expanded'),
        panelDisplay: getComputedStyle(panel).display,
      }
    })

    expect(result.dockPosition).toBe('sticky')
    expect(result.scrolled).toBe(true)
    expect(result.afterTop).toBe(result.beforeTop)
    expect(result.afterTop).toBeGreaterThanOrEqual(result.bodyTop)
    expect(result.afterBottom).toBeLessThanOrEqual(result.bodyBottom)
    expect(result.expanded).toBe('true')
    expect(result.panelDisplay).toBe('grid')
  } finally {
    await page.close()
  }
})

test('auto layout follows the viewport and does not stack when desktop side panels narrow the canvas', async () => {
  const page = await browser.newPage({ viewport: { width: 760, height: 620 } })
  try {
    await page.addInitScript(() => localStorage.setItem('leapview:filters-open', 'open'))
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)

    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const canvas = element.shadowRoot.querySelector('lv-report-canvas') as any
      await canvas.updateComplete
      await new Promise(requestAnimationFrame)
      const surface = canvas.shadowRoot.querySelector('.surface') as HTMLElement
      const viewport = canvas.shadowRoot.querySelector('.viewport') as HTMLElement
      return {
        canvasWidth: Math.round(canvas.getBoundingClientRect().width),
        layout: surface.dataset.layout,
        mode: surface.dataset.presentationMode,
        horizontalScroll: viewport.scrollWidth > viewport.clientWidth,
      }
    })

    expect(result.canvasWidth).toBeLessThan(640)
    expect(result.layout).toBe('desktop')
    expect(result.mode).toBe('fit-width')
    expect(result.horizontalScroll).toBe(false)
  } finally {
    await page.close()
  }
})

test('desktop report tables distribute columns across the available visual width', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      return Boolean(tableHost?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.table-scrollport'))
    })
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const hosts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      const table = tableHost.shadowRoot.querySelector('lv-report-table') as any
      await table.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await table.updateComplete
      const scrollport = table.shadowRoot.querySelector('.table-scrollport') as HTMLElement
      const plane = table.shadowRoot.querySelector('.table-plane') as HTMLElement
      const header = table.shadowRoot.querySelector('.head') as HTMLElement
      return {
        viewportWidth: scrollport.clientWidth,
        planeWidth: plane.offsetWidth,
        headerWidth: header.offsetWidth,
        horizontalOverflow: scrollport.scrollWidth - scrollport.clientWidth,
      }
    })
    expect(result.viewportWidth).toBeGreaterThan(700)
    expect(Math.abs(result.planeWidth - result.viewportWidth)).toBeLessThanOrEqual(1)
    expect(Math.abs(result.headerWidth - result.viewportWidth)).toBeLessThanOrEqual(1)
    expect(result.horizontalOverflow).toBeLessThanOrEqual(4)
  } finally {
    await page.close()
  }
})

test('mobile report tables keep accessible horizontal scrolling and native scrollbar presentation', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      return Boolean(tableHost?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.table-scrollport'))
    })
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const hosts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      const table = tableHost.shadowRoot.querySelector('lv-report-table') as any
      await table.updateComplete
      const scrollport = table.shadowRoot.querySelector('.table-scrollport') as HTMLElement
      return {
        role: scrollport.getAttribute('role'),
        label: scrollport.getAttribute('aria-label'),
        tabIndex: scrollport.getAttribute('tabindex'),
        hasHint: table.shadowRoot.querySelector('.table-scroll-hint') !== null,
        scrollbarWidth: getComputedStyle(scrollport).scrollbarWidth,
        scrollbarColor: getComputedStyle(scrollport).scrollbarColor,
        webkitScrollbarWidth: getComputedStyle(scrollport, '::-webkit-scrollbar').width,
      }
    })
    expect(result).toEqual({
      role: 'table', label: 'Orders', tabIndex: '0',
      hasHint: false,
      scrollbarWidth: 'auto', scrollbarColor: 'auto', webkitScrollbarWidth: 'auto',
    })
  } finally {
    await page.close()
  }
})

test('windowed table keeps a bounded DOM and requests unloaded chunks while scrolling', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      return Boolean(tableHost?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.table-scrollport'))
    })
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const hosts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      const table = tableHost.shadowRoot.querySelector('lv-report-table') as any
      await table.updateComplete
      const scrollport = table.shadowRoot.querySelector('.table-scrollport') as HTMLElement
      const request = new Promise<any>((resolve, reject) => {
        const timeout = window.setTimeout(() => reject(new Error('window request was not emitted')), 1_000)
        dashboard.addEventListener('lv-visualization-window-request', (event: Event) => {
          window.clearTimeout(timeout)
          resolve((event as CustomEvent).detail)
        }, { once: true })
      })
      scrollport.scrollTop = 100 * 28
      scrollport.dispatchEvent(new Event('scroll'))
      const detail = await request
      await table.updateComplete
      return {
        detail,
        renderedRows: table.shadowRoot.querySelectorAll('.canvas > .row').length,
        totalRows: table.table.availableRows,
        loadingVisible: table.shadowRoot.textContent?.includes('loading'),
      }
    })
    expect(result.detail).toMatchObject({
      visualID: 'orders', specRevision: `sha256:${'3'.repeat(64)}`, dataRevision: 1,
      resetVersion: 0, limit: 50,
    })
    expect(result.detail.requestSeq).toBeGreaterThan(0)
    expect(result.detail.start).toBeGreaterThanOrEqual(50)
    expect(['all', 'a', 'b', 'c']).toContain(result.detail.blockID)
    expect(result.renderedRows).toBeLessThan(40)
    expect(result.totalRows).toBe(250)
    expect(result.loadingVisible).toBe(true)
  } finally { await page.close() }
})

test('windowed table keeps requesting the latest chunk during continuous fast scrolling', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      return Boolean(tableHost?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.table-scrollport'))
    })
    const requestedDuringScroll = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const hosts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      const table = tableHost.shadowRoot.querySelector('lv-report-table') as any
      table.table = {
        ...table.table,
        cardinality: { kind: 'exact', value: 1_000 },
        availableRows: 1_000,
      }
      await table.updateComplete
      const scrollport = table.shadowRoot.querySelector('.table-scrollport') as HTMLElement
      let requested = false
      dashboard.addEventListener('lv-visualization-window-request', () => {
        requested = true
      })
      for (let index = 0; index < 10; index++) {
        scrollport.scrollTop = (200 + index * 60) * 28
        scrollport.dispatchEvent(new Event('scroll'))
        await new Promise((resolve) => window.setTimeout(resolve, 20))
      }
      const duringScroll = requested
      await new Promise((resolve) => window.setTimeout(resolve, 120))
      return duringScroll
    })
    expect(requestedDuringScroll).toBe(true)
  } finally {
    await page.close()
  }
})

test('selected sticky table cells preserve the visible row highlight', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      return Boolean(tableHost?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.row'))
    })
    const colors = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const hosts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      const table = tableHost.shadowRoot.querySelector('lv-report-table') as any
      table.table = {
        ...table.table,
        interaction: {
          kind: 'row_selection',
          mappings: [{ field: 'orders.order_id', dataset: 'orders', value: 'order_id' }],
        },
        selection: [{
          label: 'o1',
          mappings: [{ field: 'orders.order_id', dataset: 'orders', value: 'o1' }],
        }],
      }
      table.style.setProperty('--bgColor-accent-muted', 'rgb(221, 244, 255)')
      await table.updateComplete
      const selected = table.shadowRoot.querySelector('.row[aria-selected="true"]') as HTMLElement
      const pinned = selected.querySelector('.cell.pinned-left') as HTMLElement
      const unselected = table.shadowRoot.querySelector('.row[aria-selected="false"]') as HTMLElement
      const selectedColor = getComputedStyle(selected).backgroundColor
      const pinnedColor = getComputedStyle(pinned).backgroundColor
      const pinnedHeader = table.shadowRoot.querySelector('.header-cell.pinned-left-edge') as HTMLElement
      selected.classList.add('hovered')
      return {
        selected: selectedColor,
        pinned: pinnedColor,
        hovered: getComputedStyle(selected).backgroundColor,
        pinnedHovered: getComputedStyle(pinned).backgroundColor,
        unselected: getComputedStyle(unselected).backgroundColor,
        dividerWidth: getComputedStyle(pinnedHeader, '::after').width,
        nativeCellTitles: table.shadowRoot.querySelectorAll('.cell[title]').length,
        accessibleCellLabels: table.shadowRoot.querySelectorAll('.cell-action[aria-label]').length,
      }
    })
    expect(colors.selected).toBe('rgb(221, 244, 255)')
    expect(colors.pinned).toBe(colors.selected)
    expect(colors.hovered).toBe(colors.selected)
    expect(colors.pinnedHovered).toBe(colors.selected)
    expect(colors.pinned).not.toBe(colors.unselected)
    expect(colors.dividerWidth).toBe('1px')
    expect(colors.nativeCellTitles).toBe(0)
    expect(colors.accessibleCellLabels).toBeGreaterThan(0)
  } finally {
    await page.close()
  }
})

test('report tables omit semantic headers without shifting body rows when showHeader is false', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      return Boolean(tableHost?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.canvas > .row'))
    })
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const tableHost = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host'))
        .find((candidate: any) => candidate.envelope?.visualID === 'orders') as any
      const table = tableHost.shadowRoot.querySelector('lv-report-table') as any
      table.table = { ...table.table, style: { ...table.table.style, showHeader: false } }
      await table.updateComplete
      const root = table.shadowRoot
      const shell = root.querySelector('.shell') as HTMLElement
      const firstRow = root.querySelector('.canvas > .row') as HTMLElement
      return {
        headerNodes: root.querySelectorAll('.head, .group-head, [role="columnheader"]').length,
        rowCount: root.querySelectorAll('.canvas > .row').length,
        cellActionLabels: root.querySelectorAll('.cell-action[aria-label]').length,
        headOffset: shell.style.getPropertyValue('--lv-head-top'),
        firstRowTop: firstRow?.style.top,
      }
    })
    expect(result.headerNodes).toBe(0)
    expect(result.rowCount).toBeGreaterThan(0)
    expect(result.cellActionLabels).toBeGreaterThan(0)
    expect(result.headOffset).toBe('0px')
    expect(result.firstRowTop).toBe('0px')
  } finally {
    await page.close()
  }
})

test('table resize handles expose keyboard increments and accessible labels', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
      const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
      return Boolean(tableHost?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.column-resizer'))
    })
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const host = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host'))
        .find((candidate: any) => candidate.envelope?.visualID === 'orders') as any
      const table = host.shadowRoot.querySelector('lv-report-table') as any
      await table.updateComplete
      const root = table.shadowRoot
      const handle = root.querySelector('.column-resizer') as HTMLElement
      const shell = root.querySelector('.shell') as HTMLElement
      const frame = root.querySelector('.table-frame') as HTMLElement
      const scrollport = root.querySelector('.table-scrollport') as HTMLElement
      const actionSizes = Array.from(root.querySelectorAll('.visual-actions .icon-action, .visual-options summary'))
        .map((control: Element) => {
          const bounds = control.getBoundingClientRect()
          return { width: bounds.width, height: bounds.height }
        })
      const before = shell.style.getPropertyValue('--lv-table-columns')
      handle.focus()
      handle.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
      await table.updateComplete
      return {
        label: handle.getAttribute('aria-label'),
        role: handle.getAttribute('role'),
        tabIndex: handle.tabIndex,
        valueMinimum: handle.getAttribute('aria-valuemin'),
        valueNow: handle.getAttribute('aria-valuenow'),
        frameRole: frame.getAttribute('role'),
        tableRole: scrollport.getAttribute('role'),
        tableLabel: scrollport.getAttribute('aria-label'),
        actionSizes,
        changed: shell.style.getPropertyValue('--lv-table-columns') !== before,
      }
    })
    expect(result).toMatchObject({
      role: 'separator',
      tabIndex: 0,
      valueMinimum: expect.stringMatching(/^\d+$/),
      valueNow: expect.stringMatching(/^\d+$/),
      frameRole: null,
      tableRole: 'table',
      tableLabel: 'Orders',
      changed: true,
    })
    expect(result.label).toMatch(/^Resize .+ column$/)
    expect(result.actionSizes.length).toBeGreaterThan(0)
    expect(result.actionSizes.every(({ width, height }: { width: number; height: number }) => width >= 32 && height >= 32)).toBe(true)
  } finally { await page.close() }
})

test('dashboard refresh progress is owned by the latest stream generation', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.title === 'Executive Sales Dashboard')
    const states = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      const read = async () => {
        await element.updateComplete
        const progress = element.shadowRoot.querySelector('[data-dashboard-refresh-progress]')
        return { generation: progress?.getAttribute('data-generation'), now: progress?.getAttribute('aria-valuenow'), complete: progress?.getAttribute('data-complete') }
      }
      const initial = await read()
      mergePatch({ status: { generation: 4, refreshId: 'refresh-4', loading: true, progressPercent: 25 } })
      const active = await read()
      mergePatch({ status: { generation: 4, refreshId: 'refresh-4', loading: false, progressPercent: 100 } })
      const complete = await read()
      return { initial, active, complete }
    })
    expect(states).toEqual({
      initial: { generation: '3', now: '50', complete: 'false' },
      active: { generation: '4', now: '25', complete: 'false' },
      complete: { generation: '4', now: '100', complete: 'true' },
    })
  } finally { await page.close() }
})

test('dashboard keeps the source visualization selected through canonicalization and clearing', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.title === 'Executive Sales Dashboard')
    const selections = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({
        interactionSelections: [],
        status: { generation: 3, refreshId: 'refresh-3', loading: false, progressPercent: 100 },
      })
      await element.updateComplete
      const readSelection = async () => {
        await element.updateComplete
        await Promise.resolve()
        await element.updateComplete
        const host = Array.from(element.shadowRoot.querySelectorAll('lv-visualization-host') as NodeListOf<any>)
          .find((candidate: any) => candidate.envelope?.visualID === 'orders_chart')
        return host.envelope.selection
      }
      await element.updateComplete
      const source = Array.from(element.shadowRoot.querySelectorAll('lv-visualization-host') as NodeListOf<any>)
        .find((host: any) => host.envelope?.visualID === 'orders_chart')
      const command = {
        sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection', action: 'set', toggle: true,
        mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered', label: 'Delivered' }],
      }
      source.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: command }))
      const optimistic = await readSelection()

      mergePatch({
        interactionSelections: [{
          sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
          entries: [{ label: 'Delivered', mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered' }] }],
        }],
        status: { generation: 4, refreshId: 'refresh-4', loading: false, progressPercent: 100 },
      })
      const canonical = await readSelection()

      mergePatch({
        interactionSelections: [],
        status: { generation: 5, refreshId: 'refresh-5', loading: false, progressPercent: 100 },
      })
      const cleared = await readSelection()
      return { optimistic, canonical, cleared, command }
    })
    const selected = [{
      datum: { dataset: 'primary', dataRevision: 1, identity: { label: 'delivered' } }, label: 'Delivered',
    }]
    expect(selections).toEqual({
      optimistic: selected,
      canonical: selected,
      cleared: [],
      command: {
        sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection', action: 'set', toggle: true,
        mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered', label: 'Delivered' }],
        specRevision: `sha256:${'2'.repeat(64)}`,
        dataRevision: 1,
        servingStateID: 'serving-test',
        filterRevision: 0,
        interactionRevision: 0,
      },
    })
  } finally { await page.close() }
})

test('visualization host renders the shared title and preserves the live source through fullscreen', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.title === 'Executive Sales Dashboard')
    const initial = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const hosts = Array.from(element.shadowRoot.querySelectorAll('lv-visualization-host') as NodeListOf<any>)
      const host = hosts.find((candidate: any) => candidate.envelope?.visualID === 'orders_chart')
      await host.updateComplete
      const title = host.shadowRoot.querySelector('[data-visualization-title]')?.textContent?.trim()
      const expand = host.shadowRoot.querySelector('button[aria-label="Expand chart"]') as HTMLButtonElement | null
      return { title, expand: expand?.title }
    })
    expect(initial).toEqual({
      title: 'Orders by status',
      expand: 'Expand chart',
    })

    await page.locator('[data-visualization-id="orders_chart"][data-visualization-expand]').click()
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page')
      return Boolean(dashboard?.shadowRoot?.querySelector('lv-visual-modal')?.shadowRoot?.querySelector('[role="dialog"]'))
    })
    const focused = await page.locator('lv-dashboard-page').evaluate((dashboard: any) => {
      const host = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host') as NodeListOf<any>)
        .find((candidate: any) => candidate.envelope?.visualID === 'orders_chart') as HTMLElement | undefined
      const modal = dashboard.shadowRoot.querySelector('lv-visual-modal') as HTMLElement
      return {
        dialog: modal.shadowRoot?.querySelector('[role="dialog"]')?.getAttribute('aria-label'),
        sourceParent: host?.parentElement?.localName,
        sourceSlot: host?.getAttribute('slot'),
        sourceTitle: host?.shadowRoot?.querySelector('[data-visualization-title]')?.textContent?.trim(),
      }
    })
    expect(focused).toEqual({
      dialog: 'Orders by status',
      sourceParent: 'lv-visual-modal',
      sourceSlot: 'focus-visual',
      sourceTitle: 'Orders by status',
    })

    const focusedStatus = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const source = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host') as NodeListOf<any>)
        .find((candidate: any) => candidate.envelope?.visualID === 'orders_chart') as any
      source.envelope = { ...source.envelope, status: { kind: 'partial', message: 'Focused refresh' } }
      await source.updateComplete
      return source.envelope?.status
    })
    expect(focusedStatus).toEqual({ kind: 'partial', message: 'Focused refresh' })

    await page.locator('button[aria-label="Close visual modal"]').click()
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page')
      const modal = dashboard?.shadowRoot?.querySelector('lv-visual-modal')
      return !modal?.shadowRoot?.querySelector('[role="dialog"]') && !modal?.querySelector('[slot="focus-visual"]')
    })
    const restored = await page.locator('lv-dashboard-page').evaluate((dashboard: any) => {
      const host = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host') as NodeListOf<any>)
        .find((candidate: any) => candidate.envelope?.visualID === 'orders_chart') as any
      return {
        sourceParent: host?.parentElement?.localName,
        sourceSlot: host?.getAttribute('slot'),
        status: host?.envelope?.status,
      }
    })
    expect(restored).toEqual({
      sourceParent: 'lv-dashboard-visual-frame',
      sourceSlot: null,
      status: { kind: 'partial', message: 'Focused refresh' },
    })
  } finally { await page.close() }
})

test('collapsed filters and page navigation use the same rail width', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.addInitScript(() => {
      localStorage.setItem('leapview-report-sidebar-collapsed', 'true')
      localStorage.setItem('leapview:filters-open', 'closed')
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => (
      customElements.get('lv-dashboard-page')
        && customElements.get('lv-sub-sidebar')
        && customElements.get('lv-filter-dock')
        && (document.querySelector('lv-dashboard-page') as any)?.page
    ))

    const widths = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const pageSidebar = root.querySelector('lv-sub-sidebar') as any
      const filterDock = root.querySelector('lv-filter-dock') as any
      await Promise.all([pageSidebar.updateComplete, filterDock.updateComplete])
      const filterRail = filterDock.shadowRoot.querySelector('aside') as HTMLElement
      return {
        pageSidebar: Math.round(pageSidebar.getBoundingClientRect().width),
        filters: Math.round(filterRail.getBoundingClientRect().width),
        filterBackground: getComputedStyle(filterRail).backgroundColor,
      }
    })

    expect(widths.pageSidebar).toBeGreaterThan(0)
    expect(widths.filters).toBe(widths.pageSidebar)
    expect(widths.filterBackground).toBe('rgb(246, 248, 250)')
  } finally {
    await page.close()
  }
})

test('opening the desktop filter pane reduces the usable canvas instead of covering it', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.addInitScript(() => localStorage.setItem('leapview:filters-open', 'closed'))
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)
    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const dock = root.querySelector('lv-filter-dock') as any
      const canvas = root.querySelector('.canvas-wrap') as HTMLElement
      const reportCanvas = root.querySelector('lv-report-canvas') as HTMLElement
      const footer = root.querySelector('lv-report-footer') as HTMLElement
      await dock.updateComplete
      const before = canvas.getBoundingClientRect()
      ;(dock.shadowRoot.querySelector('.rail') as HTMLButtonElement).click()
      await dock.updateComplete
      await new Promise(requestAnimationFrame)
      await Promise.all(dock.getAnimations().map((animation: Animation) => animation.finished))
      const after = canvas.getBoundingClientRect()
      const pane = dock.getBoundingClientRect()
      const report = reportCanvas.getBoundingClientRect()
      const footerRect = footer.getBoundingClientRect()
      const canvasStyle = getComputedStyle(canvas)
      return {
        beforeWidth: Math.round(before.width),
        afterWidth: Math.round(after.width),
        canvasRight: Math.round(after.right),
        paneLeft: Math.round(pane.left),
        paneWidth: Math.round(pane.width),
        expanded: dock.hasAttribute('data-open'),
        paddingRight: canvasStyle.paddingRight,
        paddingBottom: canvasStyle.paddingBottom,
        scrollbarToFiltersGap: Math.round(pane.left - report.right),
        scrollbarToFooterGap: Math.round(footerRect.top - report.bottom),
      }
    })

    expect(result.expanded).toBe(true)
    expect(result.paneWidth).toBeGreaterThan(240)
    expect(result.afterWidth).toBeLessThan(result.beforeWidth)
    expect(result.canvasRight).toBeLessThanOrEqual(result.paneLeft)
    expect(result.paddingRight).toBe('0px')
    expect(result.paddingBottom).toBe('0px')
    expect(result.scrollbarToFiltersGap).toBe(0)
    expect(result.scrollbarToFooterGap).toBe(0)
  } finally {
    await page.close()
  }
})

test('mobile report header combines page and filter controls without stacked rails', async () => {
  const page = await browser.newPage({ viewport: { width: 640, height: 820 } })
  try {
    await page.addInitScript(() => localStorage.setItem('leapview:filters-open', 'closed'))
    await page.goto(baseURL)
    const dashboard = page.locator('lv-dashboard-page')
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)
    const compact = await dashboard.evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const dock = element.shadowRoot.querySelector('lv-filter-dock') as any
      await dock.updateComplete
      const header = root.querySelector('.header') as HTMLElement
      const pageMenu = root.querySelector('.mobile-page-menu') as HTMLDetailsElement
      const breadcrumb = root.querySelector('.breadcrumb') as HTMLElement
      const breadcrumbCurrent = breadcrumb.querySelector('[aria-current="page"]') as HTMLElement
      const filterTrigger = root.querySelector('.mobile-filter-toggle') as HTMLButtonElement
      const agentTrigger = root.querySelector('.agent-toggle') as HTMLButtonElement
      const dockRail = dock.shadowRoot.querySelector('.rail') as HTMLButtonElement
      const actions = root.querySelector('.actions') as HTMLElement
      const actionRects = Array.from(actions.children).map((child: any) => child.getBoundingClientRect())
      filterTrigger.focus()
      const filterFocus = getComputedStyle(filterTrigger)
      const filterFocusStyle = { style: filterFocus.outlineStyle, width: filterFocus.outlineWidth }
      agentTrigger.focus()
      const agentFocus = getComputedStyle(agentTrigger)
      const agentFocusStyle = { style: agentFocus.outlineStyle, width: agentFocus.outlineWidth }
      return {
        sidebarDisplay: getComputedStyle(root.querySelector('lv-sub-sidebar')).display,
        headerHeight: Math.round(header.getBoundingClientRect().height),
        pageMenuDisplay: getComputedStyle(pageMenu).display,
        breadcrumbLabels: Array.from(breadcrumb.querySelectorAll('.breadcrumb-label')).map((item: any) => item.textContent.trim()),
        breadcrumbCurrentDisplay: getComputedStyle(breadcrumbCurrent).display,
        pageLabel: pageMenu.querySelector('summary')?.textContent?.replace(/\s+/g, ' ').trim(),
        pageOptions: Array.from(pageMenu.querySelectorAll('a')).map((item: any) => item.textContent.trim()),
        filterLabel: filterTrigger.getAttribute('aria-label'),
        dockWidth: Math.round(dock.getBoundingClientRect().width),
        dockHeight: Math.round(dock.getBoundingClientRect().height),
        canvasTop: Math.round(root.querySelector('.canvas-wrap').getBoundingClientRect().top),
        headerBottom: Math.round(header.getBoundingClientRect().bottom),
        headerOverflow: header.scrollWidth - header.clientWidth,
        actionsOverlap: actionRects.some((rect, index) => index > 0 && rect.left < actionRects[index - 1].right),
        filterTextDisplay: getComputedStyle(filterTrigger.querySelector('.mobile-filter-label')).display,
        askTextDisplay: getComputedStyle(root.querySelector('.agent-toggle span')).display,
        filterFocusStyle,
        agentFocusStyle,
        dockRailDisplay: getComputedStyle(dockRail).display,
        dockRailTabIndex: dockRail.tabIndex,
        dockRailAriaHidden: dockRail.getAttribute('aria-hidden'),
        dockRailInert: dockRail.inert,
      }
    })
    expect(compact).toMatchObject({
      sidebarDisplay: 'none',
      pageMenuDisplay: 'block',
      breadcrumbLabels: ['Dashboards', 'Executive Sales Dashboard'],
      breadcrumbCurrentDisplay: 'flex',
      pageLabel: 'Overview',
      pageOptions: ['Overview', 'Details'],
      filterLabel: 'Filters, 1 active',
      dockWidth: 0,
      dockHeight: 0,
      headerOverflow: 0,
      actionsOverlap: false,
      filterTextDisplay: 'none',
      askTextDisplay: 'none',
      filterFocusStyle: { style: 'solid', width: '2px' },
      agentFocusStyle: { style: 'solid', width: '2px' },
      dockRailDisplay: 'none',
      dockRailTabIndex: -1,
      dockRailAriaHidden: 'true',
      dockRailInert: true,
    })
    expect(compact.headerHeight).toBeLessThanOrEqual(64)
    expect(compact.canvasTop - compact.headerBottom).toBeLessThanOrEqual(16)

    const pageMenu = page.locator('lv-dashboard-page .mobile-page-menu')
    const pageMenuSummary = page.locator('lv-dashboard-page .mobile-page-menu summary')
    await pageMenuSummary.click()
    expect(await pageMenu.getAttribute('open')).not.toBeNull()
    await dashboard.evaluate((element: any) => {
      element.shadowRoot.querySelector('.header').dispatchEvent(new PointerEvent('pointerdown', {
        bubbles: true,
        composed: true,
      }))
    })
    expect(await pageMenu.getAttribute('open')).toBeNull()

    await pageMenuSummary.click()
    await page.keyboard.press('Escape')
    const dismissedMenu = await dashboard.evaluate((element: any) => {
      const menu = element.shadowRoot.querySelector('.mobile-page-menu') as HTMLDetailsElement
      return {
        open: menu.open,
        summaryFocused: element.shadowRoot.activeElement === menu.querySelector('summary'),
      }
    })
    expect(dismissedMenu).toEqual({ open: false, summaryFocused: true })

    const toggle = page.locator('lv-dashboard-page button.mobile-filter-toggle')
    await toggle.click()
    const opened = await dashboard.evaluate(async (element: any) => {
      const dock = element.shadowRoot.querySelector('lv-filter-dock') as any
      await dock.updateComplete
      const panel = dock.shadowRoot.querySelector('.panel')
      const background = element.shadowRoot.querySelector('.agent-toggle')
      background.focus()
      return {
        expanded: element.shadowRoot.querySelector('.mobile-filter-toggle').getAttribute('aria-expanded'),
        panelDisplay: getComputedStyle(panel).display,
        panelRole: panel.getAttribute('role'),
        panelModal: panel.getAttribute('aria-modal'),
        panelTopLayer: panel.matches(':modal'),
        asidePosition: getComputedStyle(dock.shadowRoot.querySelector('aside')).position,
        focused: dock.shadowRoot.activeElement?.getAttribute('aria-label'),
        backgroundFocusBlocked: element.shadowRoot.activeElement !== background,
      }
    })
    expect(opened).toEqual({
      expanded: 'true',
      panelDisplay: 'grid',
      panelRole: 'dialog',
      panelModal: 'true',
      panelTopLayer: true,
      asidePosition: 'fixed',
      focused: 'Close filters',
      backgroundFocusBlocked: true,
    })

    await page.keyboard.press('Shift+Tab')
    const wrappedFocus = await dashboard.evaluate((element: any) => {
      const dock = element.shadowRoot.querySelector('lv-filter-dock') as any
      const active = dock.shadowRoot.activeElement
      return {
        insideDrawer: active instanceof HTMLElement,
        insidePanel: dock.shadowRoot.querySelector('.panel').contains(active),
        backgroundFocused: element.shadowRoot.activeElement?.classList.contains('agent-toggle') ?? false,
      }
    })
    expect(wrappedFocus.insideDrawer).toBe(true)
    expect(wrappedFocus.insidePanel).toBe(true)
    expect(wrappedFocus.backgroundFocused).toBe(false)

    await page.keyboard.press('Escape')
    const closed = await dashboard.evaluate(async (element: any) => {
      const dock = element.shadowRoot.querySelector('lv-filter-dock') as any
      await dock.updateComplete
      await element.updateComplete
      const trigger = element.shadowRoot.querySelector('.mobile-filter-toggle')
      const background = element.shadowRoot.querySelector('.agent-toggle')
      return {
        expanded: trigger.getAttribute('aria-expanded'),
        triggerFocusRestored: element.shadowRoot.activeElement === trigger,
        backgroundFocusBlocked: element.shadowRoot.activeElement !== background,
      }
    })
    expect(closed).toEqual({
      expanded: 'false',
      triggerFocusRestored: true,
      backgroundFocusBlocked: true,
    })
  } finally {
    await page.close()
  }
})

test('single-page mobile dashboards show page context without an empty menu', async () => {
  const page = await browser.newPage({ viewport: { width: 320, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)
    const state = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({
        page: {
          pages: [{ id: 'overview', title: 'Overview', href: '/dashboards/executive-sales/pages/overview', active: true }],
        },
      })
      await element.updateComplete
      const root = element.shadowRoot
      const label = root.querySelector('.mobile-page-label') as HTMLElement
      const header = root.querySelector('.header') as HTMLElement
      const actionRects = Array.from(root.querySelector('.actions').children)
        .filter((child: any) => getComputedStyle(child).display !== 'none')
        .map((child: any) => child.getBoundingClientRect())
      return {
        menuCount: root.querySelectorAll('.mobile-page-menu').length,
        label: label.textContent?.trim(),
        labelAria: label.getAttribute('aria-label'),
        labelDisplay: getComputedStyle(label).display,
        headerOverflow: header.scrollWidth - header.clientWidth,
        actionsOverlap: actionRects.some((rect, index) => index > 0 && rect.left < actionRects[index - 1].right),
      }
    })

    expect(state).toEqual({
      menuCount: 0,
      label: 'Overview',
      labelAria: 'Current page: Overview',
      labelDisplay: 'block',
      headerOverflow: 0,
      actionsOverlap: false,
    })
  } finally {
    await page.close()
  }
})

test('filter pane groups scope and exposes clear, reset, apply, and cancel actions', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-dock'))
    const result = await page.evaluate(async () => {
      localStorage.setItem('leapview:filters-open', 'open')
      const dock = document.createElement('lv-filter-dock') as any
      const definition = {
        id: 'state',
        label: 'State',
        field: 'orders.state',
        valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'static', limit: 2, values: [
          { value: { kind: 'string', value: 'CA' }, label: 'CA' },
          { value: { kind: 'string', value: 'SP' }, label: 'SP' },
        ] },
        timezone: 'UTC',
        calendar: 'gregorian',
        weekStart: 'monday',
      }
      dock.pageId = 'overview'
      dock.contract = {
        applicationMode: 'deferred',
        definitions: { state: definition },
        bindings: {
          report_state: {
            key: 'report_state', id: 'report_state', filter: 'state', scope: 'report',
            default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
            readerEditable: true, paneVisible: true, paneOrder: 0, targets: [],
            optionDependencies: [],
          },
          page_state: {
            key: 'page_state', id: 'page_state', filter: 'state', scope: 'page', pageID: 'overview',
            default: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'SP' }] },
            selectionMode: 'single', maxSelectedValues: 1,
            readerEditable: true, paneVisible: true, paneOrder: 1, targets: [],
            optionDependencies: [],
          },
          hidden_page_state: {
            key: 'hidden_page_state', id: 'hidden_page_state', filter: 'state', scope: 'page', pageID: 'overview',
            default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
            readerEditable: true, paneVisible: false, paneOrder: 2, targets: [],
            optionDependencies: [],
          },
          locked_report_state: {
            key: 'locked_report_state', id: 'locked_report_state', filter: 'state', scope: 'report',
            default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
            readerEditable: false, paneVisible: false, paneOrder: 3, targets: [],
            optionDependencies: [],
          },
        },
      }
      dock.filterState = {
        revision: 4,
        appliedControls: {
          report_state: {
            expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] },
            resolvedExpression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] },
          },
          page_state: {
            expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] },
            resolvedExpression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] },
          },
        },
        draftControls: {
          report_state: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'SP' }] },
        },
        dirtyBindings: ['report_state'],
        defaultsRevision: 'defaults',
      }
      const events: Array<{ type: string; detail: unknown }> = []
      for (const type of [
        'lv-filter-clear',
        'lv-filter-reset-binding',
        'lv-filter-reset-scope',
        'lv-filter-apply',
        'lv-filter-cancel',
      ]) {
        dock.addEventListener(type, (event: Event) => {
          events.push({ type, detail: (event as CustomEvent).detail })
        })
      }
      document.body.append(dock)
      await dock.updateComplete
      const cards = Array.from(dock.shadowRoot.querySelectorAll('lv-filter-pane-card')) as any[]
      await Promise.all(cards.map(card => card.updateComplete))
      const reportCard = cards.find(card => card.binding.key === 'report_state')
      const pageCard = cards.find(card => card.binding.key === 'page_state')
      ;(reportCard.shadowRoot.querySelector('button[aria-label="Clear State"]') as HTMLButtonElement).click()
      ;(pageCard.shadowRoot.querySelector('button[aria-label="Reset State to default"]') as HTMLButtonElement).click()
      ;(dock.shadowRoot.querySelector('button[data-reset-scope="page"]') as HTMLButtonElement).click()
      ;(dock.shadowRoot.querySelector('button[data-reset-scope="dashboard"]') as HTMLButtonElement).click()
      ;(dock.shadowRoot.querySelector('button[data-filter-apply]') as HTMLButtonElement).click()
      ;(dock.shadowRoot.querySelector('button[data-filter-cancel]') as HTMLButtonElement).click()
      return {
        groups: Array.from(dock.shadowRoot.querySelectorAll('.group-title')).map(node => node.textContent?.trim()),
        activeCards: cards.filter(card => card.hasAttribute('active')).map(card => card.binding.key),
        dirtyCards: cards.filter(card => card.hasAttribute('dirty')).map(card => card.binding.key),
        resetCards: cards
          .filter(card => card.shadowRoot.querySelector('button[aria-label="Reset State to default"]'))
          .map(card => card.binding.key),
        events,
      }
    })
    expect(result.groups).toEqual(['Filters on all pages', 'Filters on this page'])
    expect(result.activeCards).toEqual(['report_state', 'page_state'])
    expect(result.dirtyCards).toEqual(['report_state'])
    expect(result.resetCards).toEqual(['page_state'])
    expect(result.events).toEqual([
      { type: 'lv-filter-clear', detail: { bindingKey: 'report_state' } },
      { type: 'lv-filter-reset-binding', detail: { bindingKey: 'page_state' } },
      { type: 'lv-filter-reset-scope', detail: { scope: 'page', bindingKeys: ['hidden_page_state', 'page_state'] } },
      { type: 'lv-filter-reset-scope', detail: { scope: 'dashboard', bindingKeys: ['hidden_page_state', 'page_state', 'report_state'] } },
      { type: 'lv-filter-apply', detail: null },
      { type: 'lv-filter-cancel', detail: null },
    ])
  } finally {
    await page.close()
  }
})

test('range and text leaves expose visible input semantics', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const binding = {
        key: 'filter', id: 'filter', filter: 'filter', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [],
        optionDependencies: [],
      }
      const text = document.createElement('lv-filter-leaf') as any
      text.definition = {
        id: 'category', label: 'Category', field: 'orders.category', valueKind: 'string',
        predicates: [{ kind: 'comparison', operators: ['contains'] }],
        options: { kind: 'none', limit: 0, values: [] },
      }
      text.binding = binding
      text.presentation = {
        style: 'input', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      const range = document.createElement('lv-filter-leaf') as any
      range.definition = {
        id: 'revenue', label: 'Revenue', field: 'orders.revenue', valueKind: 'decimal',
        predicates: [{ kind: 'range', operators: [] }],
        options: { kind: 'none', limit: 0, values: [] },
      }
      range.binding = { ...binding, key: 'revenue' }
      range.presentation = {
        style: 'numeric_range', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      document.body.append(text, range)
      await Promise.all([text.updateComplete, range.updateComplete])
      return {
        operator: text.shadowRoot.querySelector('.operator')?.textContent?.trim(),
        placeholder: text.shadowRoot.querySelector('input')?.getAttribute('placeholder'),
        rangeLabels: Array.from(range.shadowRoot.querySelectorAll('.field-label')).map(node => node.textContent?.trim()),
        rangePlaceholders: Array.from(range.shadowRoot.querySelectorAll('.range input')).map(node => node.getAttribute('placeholder')),
      }
    })
    expect(result).toEqual({
      operator: 'Contains',
      placeholder: 'Enter value',
      rangeLabels: ['Minimum', 'Maximum'],
      rangePlaceholders: ['No minimum', 'No maximum'],
    })
  } finally {
    await page.close()
  }
})

test('range filters keep open bounds blank and commit the compound edit once', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'delivery_days', label: 'Delivery days', field: 'orders.delivery_days', valueKind: 'integer',
        predicates: [{ kind: 'range', operators: [] }],
        options: { kind: 'none', limit: 0, values: [] },
      }
      leaf.binding = {
        key: 'delivery_days', id: 'delivery_days', filter: 'delivery_days', scope: 'page', pageID: 'filters',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'numeric_range', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      const outside = document.createElement('button')
      outside.textContent = 'Outside'
      const mutations: unknown[] = []
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => mutations.push(event.detail))
      document.body.append(leaf, outside)
      await leaf.updateComplete

      const inputs = () => Array.from(leaf.shadowRoot.querySelectorAll('.range input')) as HTMLInputElement[]
      const [minimum, maximum] = inputs()
      minimum.focus()
      minimum.value = '1'
      minimum.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      maximum.focus()
      await leaf.updateComplete
      const afterTabbing = { values: inputs().map(input => input.value), mutations: mutations.length }

      // An unrelated canonical render must not overwrite an in-progress range edit.
      leaf.expression = { kind: 'unfiltered' }
      await leaf.updateComplete
      const afterCanonicalRender = inputs().map(input => input.value)

      outside.focus()
      await leaf.updateComplete
      const afterCommit = { values: inputs().map(input => input.value), mutations: [...mutations] }
      return { afterTabbing, afterCanonicalRender, afterCommit }
    })
    expect(result).toEqual({
      afterTabbing: { values: ['1', ''], mutations: 0 },
      afterCanonicalRender: ['1', ''],
      afterCommit: {
        values: ['1', ''],
        mutations: [{
          bindingKey: 'delivery_days',
          expression: {
            kind: 'range',
            lower: { value: { kind: 'integer', value: '1' }, inclusive: true },
          },
        }],
      },
    })
  } finally {
    await page.close()
  }
})

test('range filters reject reversed bounds without replacing the draft', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'delivery_days', label: 'Delivery days', field: 'orders.delivery_days', valueKind: 'integer',
        predicates: [{ kind: 'range', operators: [] }],
        options: { kind: 'none', limit: 0, values: [] },
      }
      leaf.binding = {
        key: 'delivery_days', id: 'delivery_days', filter: 'delivery_days', scope: 'page', pageID: 'filters',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'numeric_range', search: false, selectAll: false,
        showCounts: false, showSummary: false, compact: false,
      }
      const mutations: unknown[] = []
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => mutations.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      const inputs = Array.from(leaf.shadowRoot.querySelectorAll('.range input')) as HTMLInputElement[]
      inputs[0].value = '10'
      inputs[0].dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      inputs[1].value = '5'
      inputs[1].dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      inputs[1].dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, composed: true }))
      await leaf.updateComplete
      return {
        values: Array.from(leaf.shadowRoot.querySelectorAll('.range input')).map((input: HTMLInputElement) => input.value),
        error: leaf.shadowRoot.querySelector('[role="alert"]')?.textContent?.trim() ?? null,
        invalid: Array.from(leaf.shadowRoot.querySelectorAll('.range input')).map((input: HTMLInputElement) => input.getAttribute('aria-invalid')),
        mutations,
      }
    })
    expect(result).toEqual({
      values: ['10', '5'],
      error: 'Minimum must be less than or equal to maximum.',
      invalid: ['true', 'true'],
      mutations: [],
    })
  } finally {
    await page.close()
  }
})

test('active dashboard slicers reserve an atomic clear action', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-slicer'))
    const result = await page.evaluate(async () => {
      const slicer = document.createElement('lv-slicer') as any
      slicer.definition = {
        id: 'delivery_days', label: 'Delivery days', field: 'orders.delivery_days', valueKind: 'integer',
        predicates: [{ kind: 'range', operators: [] }],
        options: { kind: 'none', limit: 0, values: [] },
      }
      slicer.binding = {
        key: 'delivery_days', id: 'delivery_days', filter: 'delivery_days', scope: 'page', pageID: 'filters',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      slicer.expression = {
        kind: 'range',
        lower: { value: { kind: 'integer', value: '132' }, inclusive: true },
        upper: { value: { kind: 'integer', value: '434' }, inclusive: true },
      }
      slicer.presentation = {
        style: 'numeric_range', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      const mutations: unknown[] = []
      slicer.addEventListener('lv-filter-mutate', (event: CustomEvent) => mutations.push(event.detail))
      document.body.append(slicer)
      await slicer.updateComplete
      const leaf = slicer.shadowRoot.querySelector('lv-filter-leaf') as any
      await leaf.updateComplete
      const clear = leaf.shadowRoot.querySelector('button[aria-label="Clear Delivery days"]') as HTMLButtonElement
      const activeVisibility = getComputedStyle(clear).visibility
      slicer.pending = true
      await slicer.updateComplete
      await leaf.updateComplete
      const pendingDisabled = clear.disabled
      const inputs = Array.from(leaf.shadowRoot.querySelectorAll('.range input')) as HTMLInputElement[]
      inputs[0].focus()
      inputs[0].value = '200'
      inputs[0].dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      clear.focus()
      clear.click()
      await leaf.updateComplete
      slicer.expression = { kind: 'unfiltered' }
      await slicer.updateComplete
      await leaf.updateComplete
      return {
        activeVisibility,
        pendingDisabled,
        inactiveVisibility: getComputedStyle(clear).visibility,
        values: Array.from(leaf.shadowRoot.querySelectorAll('.range input')).map((input: HTMLInputElement) => input.value),
        mutations,
      }
    })
    expect(result).toEqual({
      activeVisibility: 'visible',
      pendingDisabled: false,
      inactiveVisibility: 'hidden',
      values: ['', ''],
      mutations: [{ bindingKey: 'delivery_days', expression: { kind: 'unfiltered' } }],
    })
  } finally {
    await page.close()
  }
})

test('range pane clear discards its draft without an intermediate mutation', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-pane-card'))
    const events = await page.evaluate(async () => {
      const card = document.createElement('lv-filter-pane-card') as any
      card.definition = {
        id: 'delivery_days', label: 'Delivery days', field: 'orders.delivery_days', valueKind: 'integer',
        predicates: [{ kind: 'range', operators: [] }],
        options: { kind: 'none', limit: 0, values: [] },
      }
      card.binding = {
        key: 'delivery_days', id: 'delivery_days', filter: 'delivery_days', scope: 'page', pageID: 'filters',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      card.expression = {
        kind: 'range',
        lower: { value: { kind: 'integer', value: '132' }, inclusive: true },
        upper: { value: { kind: 'integer', value: '434' }, inclusive: true },
      }
      card.presentation = {
        style: 'numeric_range', search: false, selectAll: false,
        showCounts: false, showSummary: false, compact: false,
      }
      card.pending = true
      const seen: string[] = []
      card.addEventListener('lv-filter-mutate', () => seen.push('mutate'))
      card.addEventListener('lv-filter-clear', () => seen.push('clear'))
      document.body.append(card)
      await card.updateComplete
      const leaf = card.shadowRoot.querySelector('lv-filter-leaf') as any
      await leaf.updateComplete
      const minimum = leaf.shadowRoot.querySelector('.range input') as HTMLInputElement
      minimum.focus()
      minimum.value = '200'
      minimum.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      const clear = card.shadowRoot.querySelector('button[aria-label="Clear Delivery days"]') as HTMLButtonElement
      clear.focus()
      clear.click()
      return seen
    })
    expect(events).toEqual(['clear'])
  } finally {
    await page.close()
  }
})

test('clearing a text filter emits the typed unfiltered mutation normalized to clear', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf') && customElements.get('lv-filter-pane-card'))
    const result = await page.evaluate(async () => {
      const definition = {
        id: 'category', label: 'Category', field: 'orders.category', valueKind: 'string',
        predicates: [{ kind: 'comparison', operators: ['contains'] }],
        options: { kind: 'none', limit: 0, values: [] },
      }
      const binding = {
        key: 'category', id: 'category', filter: 'category', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = definition
      leaf.binding = binding
      leaf.expression = { kind: 'comparison', operator: 'contains', value: { kind: 'string', value: 'computers' } }
      leaf.presentation = { style: 'input', search: false, selectAll: false, showCounts: false, showSummary: true, compact: false }
      const events: unknown[] = []
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => events.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      const input = leaf.shadowRoot.querySelector('input') as HTMLInputElement
      input.value = ''
      input.dispatchEvent(new Event('change', { bubbles: true }))
      return events
    })
    expect(result).toEqual([{ bindingKey: 'category', expression: { kind: 'unfiltered' } }])
  } finally { await page.close() }
})

test('filter summaries remain explicit without a layout-shifting update indicator', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'state', label: 'State', field: 'orders.state', valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: {
          kind: 'static', limit: 1,
          values: [{ value: { kind: 'string', value: 'CA' }, label: 'California' }],
        },
      }
      leaf.binding = {
        key: 'state', id: 'state', filter: 'state', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.expression = { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'CA' }] }
      leaf.presentation = {
        style: 'dropdown', search: false, selectAll: false,
        showCounts: false, showSummary: false, compact: false,
      }
      leaf.showTitle = false
      document.body.append(leaf)
      await leaf.updateComplete
      const idle = {
        summary: leaf.shadowRoot.querySelector('.selection-summary')?.textContent?.trim() ?? null,
        status: leaf.shadowRoot.querySelector('.status')?.textContent?.trim() ?? null,
      }

      leaf.presentation = { ...leaf.presentation, showSummary: true }
      await leaf.updateComplete
      const explicit = leaf.shadowRoot.querySelector('.selection-summary')?.textContent?.trim() ?? null

      leaf.presentation = { ...leaf.presentation, showSummary: false }
      leaf.pending = true
      await leaf.updateComplete
      const status = leaf.shadowRoot.querySelector('.status')
      return {
        idle,
        explicit,
        pending: status?.textContent?.trim() ?? null,
        pendingBusy: leaf.shadowRoot.querySelector('fieldset')?.getAttribute('aria-busy'),
        pendingHeading: Boolean(leaf.shadowRoot.querySelector('.field-heading')),
      }
    })
    expect(result).toEqual({
      idle: { summary: null, status: null },
      explicit: '1 selected',
      pending: null,
      pendingBusy: 'true',
      pendingHeading: false,
    })
  } finally {
    await page.close()
  }
})

test('date-range slicers rearrange at contract boundaries without removing either custom control', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.style.display = 'block'
      leaf.style.width = '268px'
      leaf.style.height = '78px'
      leaf.definition = {
        id: 'purchase_date', label: 'Purchase date', field: 'orders.purchase_date', valueKind: 'date',
        predicates: [{ kind: 'range', operators: [] }],
        options: { kind: 'none', limit: 0, values: [] },
      }
      leaf.binding = {
        key: 'purchase_date', id: 'purchase_date', filter: 'purchase_date', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'date_range', search: false, selectAll: false,
        showCounts: false, showSummary: false, compact: false,
      }
      document.body.append(leaf)
      await leaf.updateComplete
      const settle = async () => {
        await new Promise(requestAnimationFrame)
        await leaf.updateComplete
      }
      await settle()
      const snapshot = () => ({
        variant: leaf.dataset.layoutVariant,
        fit: leaf.dataset.layoutFit,
        controls: leaf.shadowRoot.querySelectorAll('.range lv-date-picker').length,
        columns: getComputedStyle(leaf.shadowRoot.querySelector('.range')).gridTemplateColumns,
      })
      const inline = snapshot()
      leaf.style.width = '240px'
      leaf.style.height = '138px'
      await settle()
      const stacked = snapshot()
      leaf.style.width = '171px'
      await settle()
      const invalid = snapshot()
      return { inline, stacked, invalid }
    })

    expect(result.inline.variant).toBe('inline')
    expect(result.inline.fit).toBe('fit')
    expect(result.inline.controls).toBe(2)
    expect(result.inline.columns.split(' ')).toHaveLength(2)
    expect(result.stacked).toMatchObject({ variant: 'stacked', fit: 'fit', controls: 2, columns: '240px' })
    expect(result.invalid).toMatchObject({ variant: 'stacked', fit: 'too-small', controls: 2 })
  } finally {
    await page.close()
  }
})

test('bounded list slicers keep every option inside the authored frame', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.style.display = 'block'
      leaf.style.width = '340px'
      leaf.style.height = '190px'
      leaf.definition = {
        id: 'status', label: 'Order status', field: 'orders.status', valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: {
          kind: 'static', limit: 50,
          values: ['approved', 'canceled', 'created', 'delivered', 'invoiced', 'processing', 'shipped', 'unavailable'],
        },
      }
      leaf.binding = {
        key: 'status', id: 'status', filter: 'status', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'list', search: false, selectAll: false,
        showCounts: false, showSummary: false, compact: false,
      }
      document.body.append(leaf)
      await leaf.updateComplete
      await new Promise(requestAnimationFrame)

      const leafRect = leaf.getBoundingClientRect()
      const options = leaf.shadowRoot.querySelector('.options') as HTMLElement
      const optionsRect = options.getBoundingClientRect()
      return {
        scrollable: options.scrollHeight > options.clientHeight,
        contained: optionsRect.bottom <= leafRect.bottom,
        overflow: getComputedStyle(options).overflowY,
      }
    })

    expect(result).toEqual({ scrollable: true, contained: true, overflow: 'auto' })
  } finally {
    await page.close()
  }
})

test('slicer layout resolution ignores report canvas transforms', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const canvas = document.createElement('div')
      canvas.style.transform = 'scale(.85)'
      canvas.style.transformOrigin = 'top left'

      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.style.display = 'block'
      leaf.style.width = '268px'
      leaf.style.height = '78px'
      leaf.definition = {
        id: 'purchase_date', label: 'Purchase date', field: 'orders.purchase_date', valueKind: 'date',
        predicates: [{ kind: 'range', operators: [] }],
        options: { kind: 'none', limit: 0, values: [] },
      }
      leaf.binding = {
        key: 'purchase_date', id: 'purchase_date', filter: 'purchase_date', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'date_range', search: false, selectAll: false,
        showCounts: false, showSummary: false, compact: false,
      }
      canvas.append(leaf)
      document.body.append(canvas)
      await leaf.updateComplete
      await new Promise(requestAnimationFrame)
      await leaf.updateComplete

      const range = leaf.shadowRoot.querySelector('.range') as HTMLElement
      const fieldset = leaf.shadowRoot.querySelector('fieldset') as HTMLElement
      return {
        cssSize: [leaf.clientWidth, leaf.clientHeight],
        visualSize: [Math.round(leaf.getBoundingClientRect().width), Math.round(leaf.getBoundingClientRect().height)],
        variant: leaf.dataset.layoutVariant,
        fit: leaf.dataset.layoutFit,
        columns: getComputedStyle(range).gridTemplateColumns,
        contentFits: fieldset.scrollWidth <= leaf.clientWidth && fieldset.scrollHeight <= leaf.clientHeight,
      }
    })

    expect(result).toEqual({
      cssSize: [268, 78],
      visualSize: [228, 66],
      variant: 'inline',
      fit: 'fit',
      columns: '131px 131px',
      contentFits: true,
    })
  } finally {
    await page.close()
  }
})

test('pane defaults a relative-period definition to the structured shared leaf', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-dock'))
    const controls = await page.evaluate(async () => {
      localStorage.removeItem('leapview:filters-open')
      const dock = document.createElement('lv-filter-dock') as any
      dock.pageId = 'overview'
      dock.contract = {
        applicationMode: 'immediate',
        definitions: {
          period: {
            id: 'period',
            label: 'Relative period',
            field: 'orders.created_at',
            valueKind: 'timestamp',
            predicates: [{ kind: 'relative_period', operators: [] }],
            options: { kind: 'none', limit: 0, values: [] },
            timezone: 'UTC',
            calendar: 'gregorian',
            weekStart: 'monday',
          },
        },
        bindings: {
          fb_period: {
            key: 'fb_period',
            id: 'period',
            filter: 'period',
            scope: 'page',
            pageID: 'overview',
            default: { kind: 'unfiltered' },
            selectionMode: 'single',
            maxSelectedValues: 1,
            readerEditable: true,
            paneVisible: true,
            paneOrder: 0,
            targets: [],
            optionDependencies: [],
          },
        },
      }
      dock.filterState = {
        revision: 0,
        appliedControls: {
          fb_period: {
            expression: { kind: 'unfiltered' },
            resolvedExpression: { kind: 'unfiltered' },
          },
        },
        draftControls: {},
        dirtyBindings: [],
        defaultsRevision: 'v1',
      }
      document.body.append(dock)
      await dock.updateComplete
      ;(dock.shadowRoot.querySelector('.rail') as HTMLButtonElement).click()
      await dock.updateComplete
      await new Promise(resolve => setTimeout(resolve, 250))
      const card = dock.shadowRoot.querySelector('lv-filter-pane-card') as any
      await card.updateComplete
      const leaf = card.shadowRoot.querySelector('lv-filter-leaf') as any
      await leaf.updateComplete
      const layoutStates = []
      for (let index = 0; index < 8; index++) {
        layoutStates.push(`${leaf.dataset.layoutVariant}:${leaf.dataset.layoutFit}`)
        await new Promise(requestAnimationFrame)
      }
      return {
        textInputs: leaf.shadowRoot.querySelectorAll('input[type="text"]').length,
        direction: Boolean(leaf.shadowRoot.querySelector('select[aria-label="Direction"]')),
        count: Boolean(leaf.shadowRoot.querySelector('input[aria-label="Period count"]')),
        unit: Boolean(leaf.shadowRoot.querySelector('select[aria-label="Period unit"]')),
        layoutStates: [...new Set(layoutStates)],
      }
    })
    expect(controls).toEqual({
      textInputs: 0,
      direction: true,
      count: true,
      unit: true,
      layoutStates: ['inline:fit'],
    })
  } finally {
    await page.close()
  }
})

test('rejected filter validation reconciles optimistic state and announces the error', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const element = document.querySelector('lv-dashboard-page') as any
      return Boolean(element?.page && !element.isUpdatePending)
    })
    const moduleHandle = await page.evaluateHandle(() => import('/static/vendor/datastar-1.0.2.js?v=dev'))
    const mutation = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      let command: any
      element.addEventListener('lv-filter-command', (event: CustomEvent) => {
        command = event.detail
      }, { once: true })
      element.filterController.mutate('fb_state', {
        kind: 'set',
        operator: 'in',
        values: [{ kind: 'string', value: 'CA' }],
      })
      return { command, optimistic: element.filterController.projected.appliedControls.fb_state.expression }
    })
    await moduleHandle.evaluate((module: any, validation: any) => module.mergePatch({ filterValidation: validation }), {
      accepted: false,
      message: 'range lower bound must not exceed upper bound',
      currentRevision: 0,
      clientMutationID: mutation.command.clientMutationID,
    })
    await page.waitForFunction((clientMutationID) => {
      const element = document.querySelector('lv-dashboard-page') as any
      const validation = element?.signal('filterValidation', null)
      return Boolean(element && !element.filterController.pending && !element.isUpdatePending && validation?.clientMutationID === clientMutationID)
    }, mutation.command.clientMutationID)
    const result = await page.locator('lv-dashboard-page').evaluate((element: any) => ({
      reconciled: element.filterController.projected.appliedControls.fb_state.expression,
      pending: element.filterController.pending,
      alert: element.shadowRoot.querySelector('[role="alert"]')?.textContent?.trim(),
    }))
    expect({ optimistic: mutation.optimistic, ...result }).toEqual({
      optimistic: {
        kind: 'set',
        operator: 'in',
        values: [{ kind: 'string', value: 'CA' }],
      },
      reconciled: {
        kind: 'set',
        operator: 'in',
        values: [{ kind: 'string', value: 'SP' }],
      },
      pending: false,
      alert: 'range lower bound must not exceed upper bound',
    })
  } finally {
    await page.close()
  }
})

test('canonical URL tombstones remove cleared filter parameters before history replacement', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)
    const result = await page.evaluate(async () => {
      const element = document.querySelector('lv-dashboard-page') as any
      const replacements: Record<string, unknown>[] = []
      ;(window as any).DatastarURLSync = {
        replace: (params: Record<string, unknown>) => {
          replacements.push(params)
          return window.location.pathname
        },
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ urlParams: { order_status: 'encoded-filter' } })
      const before = element.signal('urlParams', {})
      mergePatch({
        urlParams: { order_status: null },
        filterState: { revision: 1 },
      })
      await element.updateComplete
      return {
        before,
        after: element.signal('urlParams', {}),
        replacements,
      }
    })
    expect(result).toEqual({
      before: { order_status: 'encoded-filter' },
      after: {},
      replacements: [{}],
    })
  } finally {
    await page.close()
  }
})

test('visible dynamic list controls wait for the canonical session before requesting options', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const requests = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'state',
        label: 'State',
        field: 'orders.state',
        valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'distinct', limit: 50, values: [] },
        format: {},
      }
      leaf.binding = {
        key: 'fb_state',
        id: 'state',
        filter: 'state',
        scope: 'page',
        pageID: 'overview',
        default: { kind: 'unfiltered' },
        selectionMode: 'multiple',
        selectionLimit: 50,
        readerEditable: true,
        paneVisible: true,
        paneOrder: 0,
        paneLabel: 'State',
        targets: [],
        incomingDependencies: [],
      }
      leaf.presentation = {
        style: 'list', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      leaf.stale = true
      const seen: unknown[] = []
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => seen.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      const whileStale = seen.length
      leaf.stale = false
      await leaf.updateComplete
      return { whileStale, afterCurrent: seen.length, detail: seen[0] }
    })
    expect(requests).toEqual({
      whileStale: 0,
      afterCurrent: 1,
      detail: { bindingKey: 'fb_state', search: '', limit: 50 },
    })
  } finally {
    await page.close()
  }
})

test('visible dynamic list controls request options when their contract arrives after connection', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const requests = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      const seen: unknown[] = []
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => seen.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      leaf.definition = {
        id: 'status', label: 'Status', field: 'orders.status', valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'distinct', limit: 20, values: [] }, format: {},
      }
      leaf.binding = {
        key: 'fb_status', id: 'status', filter: 'status', scope: 'page', pageID: 'filters',
        default: { kind: 'unfiltered' }, selectionMode: 'single', selectionLimit: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, paneLabel: 'Status',
        targets: [], incomingDependencies: [],
      }
      leaf.presentation = {
        style: 'list', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      await leaf.updateComplete
      return seen
    })
    expect(requests).toEqual([{ bindingKey: 'fb_status', search: '', limit: 20 }])
  } finally {
    await page.close()
  }
})

test('static filter controls render compiled options without requesting an option page', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const state = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'delivered',
        label: 'Delivery state',
        field: 'orders.is_delivered',
        valueKind: 'boolean',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: {
          kind: 'static',
          limit: 2,
          values: [
            { value: { kind: 'boolean', value: true }, label: 'Delivered' },
            { value: { kind: 'boolean', value: false }, label: 'Not delivered' },
          ],
        },
        format: {},
      }
      leaf.binding = {
        key: 'fb_delivered',
        id: 'delivered',
        filter: 'delivered',
        scope: 'page',
        pageID: 'overview',
        default: { kind: 'unfiltered' },
        selectionMode: 'single',
        selectionLimit: 1,
        readerEditable: true,
        paneVisible: true,
        paneOrder: 0,
        paneLabel: 'Delivery state',
        targets: [],
        incomingDependencies: [],
      }
      leaf.presentation = {
        style: 'buttons', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      const requests: unknown[] = []
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => requests.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      return {
        requests: requests.length,
        buttons: Array.from(leaf.shadowRoot.querySelectorAll('button')).map((button: HTMLButtonElement) => button.textContent?.trim()),
      }
    })
    expect(state).toEqual({ requests: 0, buttons: ['Delivered', 'Not delivered'] })
  } finally {
    await page.close()
  }
})

test('static dropdown selections emit a typed filter mutation', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const state = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'state',
        label: 'State',
        field: 'orders.state',
        valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: {
          kind: 'static',
          limit: 2,
          values: [
            { value: { kind: 'string', value: 'SP' }, label: 'SP' },
            { value: { kind: 'string', value: 'RJ' }, label: 'RJ' },
          ],
        },
        format: {},
      }
      leaf.binding = {
        key: 'fb_state',
        id: 'state',
        filter: 'state',
        scope: 'page',
        pageID: 'overview',
        default: { kind: 'unfiltered' },
        selectionMode: 'single',
        selectionLimit: 1,
        readerEditable: true,
        paneVisible: true,
        paneOrder: 0,
        paneLabel: 'State',
        targets: [],
        incomingDependencies: [],
      }
      leaf.presentation = {
        style: 'dropdown', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      const mutations: unknown[] = []
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => mutations.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      const select = leaf.shadowRoot.querySelector('select') as HTMLSelectElement
      const option = Array.from(select.options).find((candidate) => candidate.textContent?.trim() === 'SP')
      select.value = option?.value ?? ''
      select.dispatchEvent(new Event('change', { bubbles: true }))
      return mutations
    })
    expect(state).toEqual([{
      bindingKey: 'fb_state',
      expression: {
        kind: 'set',
        operator: 'in',
        values: [{ kind: 'string', value: 'SP' }],
      },
    }])
  } finally {
    await page.close()
  }
})

test('searchable multi-select dropdowns filter options, preserve multiple values, and clear to All', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const state = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'state', label: 'State', field: 'orders.state', valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'static', limit: 3, values: [
          { value: { kind: 'string', value: 'SP' }, label: 'Sao Paulo' },
          { value: { kind: 'string', value: 'RJ' }, label: 'Rio de Janeiro' },
          { value: { kind: 'string', value: 'MG' }, label: 'Minas Gerais' },
        ] },
        format: {},
      }
      leaf.binding = {
        key: 'fb_state', id: 'state', filter: 'state', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'multiple', maxSelectedValues: 0,
        readerEditable: true, paneVisible: true, paneOrder: 0, paneLabel: 'State',
        targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'dropdown', search: true, selectAll: true,
        showCounts: false, showSummary: true, compact: false,
      }
      const mutations: any[] = []
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => {
        mutations.push(event.detail)
        leaf.expression = event.detail.expression
      })
      document.body.append(leaf)
      await leaf.updateComplete
      const root = leaf.shadowRoot as ShadowRoot
      const trigger = root.querySelector<HTMLButtonElement>('.dropdown-trigger')!
      trigger.click()
      await leaf.updateComplete
      const popover = root.querySelector<HTMLElement>('.dropdown-popover')!
      const search = root.querySelector<HTMLInputElement>('.dropdown-search input')!
      const clear = root.querySelector<HTMLButtonElement>('.dropdown-clear')!
      const initialClearDisabled = clear.disabled
      const optionFontSize = getComputedStyle(root.querySelector<HTMLElement>('.dropdown-option')!).fontSize
      const triggerFontSize = getComputedStyle(trigger).fontSize
      search.value = 'rio'
      search.dispatchEvent(new Event('input', { bubbles: true }))
      await leaf.updateComplete
      const filtered = Array.from(root.querySelectorAll('.dropdown-option-label')).map((item) => item.textContent?.trim())
      root.querySelector<HTMLInputElement>('input[aria-label="Rio de Janeiro"]')!.click()
      await leaf.updateComplete
      search.value = ''
      search.dispatchEvent(new Event('input', { bubbles: true }))
      await leaf.updateComplete
      root.querySelector<HTMLInputElement>('input[aria-label="Sao Paulo"]')!.click()
      await leaf.updateComplete
      const selectedSummary = root.querySelector('.dropdown-value')?.textContent?.trim()
      const clearDisabledWithSelection = clear.disabled
      clear.click()
      await leaf.updateComplete
      return {
        expanded: trigger.getAttribute('aria-expanded'),
        popupRole: popover.getAttribute('role'),
        popupOpen: popover.matches(':popover-open'),
        popupPosition: getComputedStyle(popover).position,
        optionFontSize,
        triggerFontSize,
        filtered,
        selectedSummary,
        initialClearDisabled,
        clearDisabledWithSelection,
        mutations: mutations.map(item => item.expression),
        clearedSummary: root.querySelector('.dropdown-value')?.textContent?.trim(),
      }
    })
    expect(state.expanded).toBe('true')
    expect(state.popupRole).toBe('dialog')
    expect(state.popupOpen).toBe(true)
    expect(state.popupPosition).toBe('fixed')
    expect(state.optionFontSize).toBe('14px')
    expect(state.optionFontSize).toBe(state.triggerFontSize)
    expect(state.filtered).toEqual(['Rio de Janeiro'])
    expect(state.selectedSummary).toBe('2 selected')
    expect(state.initialClearDisabled).toBe(true)
    expect(state.clearDisabledWithSelection).toBe(false)
    expect(state.mutations).toEqual([
      { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'RJ' }] },
      { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'RJ' }, { kind: 'string', value: 'SP' }] },
      { kind: 'unfiltered' },
    ])
    expect(state.clearedSummary).toBe('All')
  } finally {
    await page.close()
  }
})

test('clicking an open dropdown trigger closes the popover', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.id = 'dropdown-toggle-regression'
      leaf.definition = {
        id: 'country', label: 'Country', field: 'orders.country', valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'static', limit: 2, values: [
          { value: { kind: 'string', value: 'US' }, label: 'United States' },
          { value: { kind: 'string', value: 'FR' }, label: 'France' },
        ] },
        format: {},
      }
      leaf.binding = {
        key: 'fb_country', id: 'country', filter: 'country', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'multiple', maxSelectedValues: 0,
        readerEditable: true, paneVisible: true, paneOrder: 0, paneLabel: 'Country',
        targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'dropdown', search: true, selectAll: true,
        showCounts: false, showSummary: true, compact: false,
      }
      document.body.append(leaf)
      await leaf.updateComplete
    })

    const leaf = page.locator('#dropdown-toggle-regression')
    const trigger = leaf.locator('.dropdown-trigger')
    const popover = leaf.locator('.dropdown-popover')
    await trigger.click()
    expect(await popover.evaluate((element) => element.matches(':popover-open'))).toBe(true)
    expect(await trigger.getAttribute('aria-expanded')).toBe('true')

    await trigger.click()
    expect(await popover.evaluate((element) => element.matches(':popover-open'))).toBe(false)
    expect(await trigger.getAttribute('aria-expanded')).toBe('false')
  } finally {
    await page.close()
  }
})

test('clearing a static dropdown visibly returns it to All', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const state = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'delivered', label: 'Delivery state', field: 'orders.delivered', valueKind: 'boolean',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: {
          kind: 'static', limit: 2,
          values: [
            { value: { kind: 'boolean', value: true }, label: 'Delivered' },
            { value: { kind: 'boolean', value: false }, label: 'Not delivered' },
          ],
        },
      }
      leaf.binding = {
        key: 'delivered', id: 'delivered', filter: 'delivered', scope: 'page', pageID: 'filters',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'dropdown', search: false, selectAll: false,
        showCounts: false, showSummary: false, compact: false,
      }
      document.body.append(leaf)
      await leaf.updateComplete
      const select = leaf.shadowRoot.querySelector('select') as HTMLSelectElement
      const delivered = Array.from(select.options).find(option => option.textContent?.trim() === 'Delivered')
      select.value = delivered?.value ?? ''
      select.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      leaf.expression = {
        kind: 'set', operator: 'in', values: [{ kind: 'boolean', value: true }],
      }
      await leaf.updateComplete
      const before = select.selectedOptions[0]?.textContent?.trim()
      leaf.expression = { kind: 'unfiltered' }
      await leaf.updateComplete
      return {
        before,
        after: select.selectedOptions[0]?.textContent?.trim(),
        value: select.value,
      }
    })
    expect(state).toEqual({ before: 'Delivered', after: 'All', value: '' })
  } finally {
    await page.close()
  }
})

test('closed dynamic dropdowns defer dependency refresh until they are focused again', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const requests = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'state',
        label: 'State',
        field: 'orders.state',
        valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'distinct', limit: 50, values: [] },
        format: {},
      }
      leaf.binding = {
        key: 'fb_state',
        id: 'state',
        filter: 'state',
        scope: 'page',
        pageID: 'overview',
        default: { kind: 'unfiltered' },
        selectionMode: 'multiple',
        selectionLimit: 50,
        readerEditable: true,
        paneVisible: true,
        paneOrder: 0,
        paneLabel: 'State',
        targets: [],
        incomingDependencies: [],
      }
      leaf.presentation = {
        style: 'dropdown', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      leaf.optionContext = 'context-one'
      leaf.expression = {
        kind: 'set', operator: 'in',
        values: [{ kind: 'string', value: 'AC' }],
      }
      const seen: unknown[] = []
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => seen.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      const retained = Array.from(leaf.shadowRoot.querySelectorAll('.dropdown-option')).map((option: HTMLLabelElement) => ({
        label: option.textContent?.trim(),
        selected: option.querySelector<HTMLInputElement>('input')?.checked,
      }))
      leaf.shadowRoot.querySelector<HTMLButtonElement>('.dropdown-trigger').click()
      await leaf.updateComplete
      const afterOpen = seen.length
      leaf.shadowRoot.querySelector<HTMLElement>('.dropdown-popover').hidePopover()
      leaf.optionContext = 'context-two'
      await leaf.updateComplete
      const afterDependencyChange = seen.length
      const whileDeferred = Array.from(leaf.shadowRoot.querySelectorAll('.dropdown-option')).map((option: HTMLLabelElement) => option.textContent?.trim())
      leaf.shadowRoot.querySelector<HTMLButtonElement>('.dropdown-trigger').click()
      await leaf.updateComplete
      return { retained, afterOpen, afterDependencyChange, whileDeferred, afterRefocus: seen.length }
    })
    expect(requests).toEqual({
      retained: [
        { label: 'AC', selected: true },
      ],
      afterOpen: 1,
      afterDependencyChange: 1,
      whileDeferred: ['AC'],
      afterRefocus: 2,
    })
  } finally {
    await page.close()
  }
})

test('visible dynamic controls refresh once when their option dependency context changes', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'status', label: 'Status', field: 'orders.status', valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'distinct', limit: 50, values: [] },
      }
      leaf.binding = {
        key: 'fb_status', id: 'status', filter: 'status', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'multiple', maxSelectedValues: 0,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [],
        optionDependencies: [],
      }
      leaf.presentation = {
        style: 'list', search: false, selectAll: false,
        showCounts: false, showSummary: true, compact: false,
      }
      leaf.optionContext = 'context-one'
      const requests: unknown[] = []
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => requests.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      leaf.options = {
        bindingKey: 'fb_status', servingStateID: 'serving', streamGeneration: 1,
        filterRevision: 1, requestGeneration: 1, complete: true,
        consumerIdentity: 'option:fb_status',
        items: [{ value: { kind: 'string', value: 'delivered' }, label: 'delivered', selected: false, available: true }],
      }
      await leaf.updateComplete
      leaf.optionContext = 'context-two'
      await leaf.updateComplete
      await leaf.updateComplete
      await new Promise(resolve => setTimeout(resolve, 30))
      return {
        requests: requests.length,
        status: leaf.shadowRoot.querySelector('.status')?.textContent?.trim(),
        options: Array.from(leaf.shadowRoot.querySelectorAll('.option span')).map((item: HTMLSpanElement) => item.textContent?.trim()),
      }
    })
    expect(result).toEqual({ requests: 2, status: undefined, options: ['delivered'] })
  } finally {
    await page.close()
  }
})

test('same-dashboard page navigation commits canonical history after the page patch', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.pageId === 'overview')
    const navigation = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const pushes: Array<{ params: Record<string, unknown>; path: string }> = []
      ;(window as any).DatastarURLSync = {
        push: (params: Record<string, unknown>, path: string) => {
          pushes.push({ params, path })
          return path
        },
      }
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-page-navigate', (event: CustomEvent) => {
        command = event.detail
      }, { once: true })
      const sidebar = element.shadowRoot.querySelector('lv-sub-sidebar')
      const details = sidebar.shadowRoot.querySelector('a[href$="/details"]') as HTMLAnchorElement
      details.click()
      await element.updateComplete

      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({
        page: {
          pageId: 'details',
          pageTitle: 'Details',
          pages: [
            { id: 'overview', title: 'Overview', href: '/dashboards/executive-sales/pages/overview', active: false },
            { id: 'details', title: 'Details', href: '/dashboards/executive-sales/pages/details', active: true },
          ],
        },
        urlParams: { state: 'canonical' },
      })
      await element.updateComplete
      return { command, pushes }
    })
    expect(navigation.command).toMatchObject({ pageID: 'details', baseFilterRevision: 0 })
    expect(String(navigation.command?.clientMutationID ?? '')).not.toBe('')
    expect(navigation.pushes).toEqual([{
      params: { state: 'canonical' },
      path: '/dashboards/executive-sales/pages/details',
    }])
  } finally {
    await page.close()
  }
})

test('read-only draft preview preserves native revision-pinned page navigation', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.pageId === 'overview')
    const navigation = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      element.readOnly = true
      await element.updateComplete
      let commands = 0
      element.addEventListener('lv-page-navigate', () => { commands += 1 })
      const sidebar = element.shadowRoot.querySelector('lv-sub-sidebar')
      const details = sidebar.shadowRoot.querySelector('a[href$="/details"]') as HTMLAnchorElement
      const click = new MouseEvent('click', { bubbles: true, composed: true, cancelable: true, button: 0 })
      details.dispatchEvent(click)
      return { commands, defaultPrevented: click.defaultPrevented, reflected: element.hasAttribute('read-only') }
    })
    expect(navigation).toEqual({ commands: 0, defaultPrevented: false, reflected: true })
  } finally {
    await page.close()
  }
})

test('collapsed report-page links dispatch navigation from a real pointer click', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => localStorage.setItem('leapview-report-sidebar-collapsed', 'true'))
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.pageId === 'overview')
    await page.locator('lv-dashboard-page').evaluate((element: any) => {
      ;(window as any).__pageNavigation = null
      element.addEventListener('lv-page-navigate', (event: CustomEvent) => {
        ;(window as any).__pageNavigation = event.detail
      }, { once: true })
    })

    const link = page.getByRole('link', { name: 'Details' })
    const box = await link.boundingBox()
    if (!box) throw new Error('details link has no pointer target')
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down()
    await page.mouse.up()

    await page.waitForFunction(() => Boolean((window as any).__pageNavigation))
    expect(await page.evaluate(() => (window as any).__pageNavigation)).toMatchObject({
      pageID: 'details',
      baseFilterRevision: 0,
    })
  } finally {
    await page.close()
  }
})
