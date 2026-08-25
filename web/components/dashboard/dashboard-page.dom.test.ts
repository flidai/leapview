import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'
import validateVisualizationEnvelope from '../../generated/visualization/validate'
import type { DashboardVisualizationSignal } from '../../generated/signals'
import type {
  VisualizationDataState,
  VisualizationDataStateTransport,
  VisualizationEnvelope,
  VisualizationField,
  VisualizationSpecBase,
} from '../../generated/visualization'

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
    const requests = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
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
      return seen
    })
    expect(requests).toHaveLength(1)
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

for (const viewport of [{ name: 'desktop', width: 1280, height: 820 }, { name: 'mobile', width: 390, height: 820 }]) {
  test(`dashboard composes envelope-native visuals on ${viewport.name}`, async () => {
    const page = await browser.newPage({ viewport })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-dashboard-page') && customElements.get('lv-visualization-host'))
      await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.title === 'Executive Sales Dashboard')
      await page.waitForFunction(() => {
        const dashboard = document.querySelector('lv-dashboard-page') as any
        const hosts = Array.from(dashboard?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []) as any[]
        const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
        return Boolean(tableHost?.shadowRoot?.querySelector('lv-report-table'))
      })
      const state = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
        await element.updateComplete
        const root = element.shadowRoot
        const hosts = Array.from(root.querySelectorAll('lv-visualization-host')) as any[]
        await Promise.all(hosts.map((host) => host.updateComplete))
        const tableHost = hosts.find((host) => host.envelope?.visualID === 'orders')
        const table = tableHost?.shadowRoot?.querySelector('lv-report-table') as any
        await table?.updateComplete
        const kpiHost = hosts.find((host) => host.envelope?.visualID === 'orders_kpi')
        const kpi = kpiHost?.shadowRoot?.querySelector('.lv-kpi-card') as HTMLElement | null
        const kpiLabel = kpi?.querySelector('.lv-visualization-label') as HTMLElement | null
        const kpiValue = kpi?.querySelector('.lv-visualization-kpi') as HTMLElement | null
        const canvas = root.querySelector('lv-report-canvas') as any
        await canvas.updateComplete
        const canvasViewport = canvas.shadowRoot.querySelector('.viewport') as HTMLElement
        const assigned = (canvas.shadowRoot.querySelector('slot') as HTMLSlotElement).assignedElements() as HTMLElement[]
        const visualFrame = (id: string) => assigned.find((item) => (item.querySelector('lv-visualization-host') as any)?.envelope?.visualID === id)?.getBoundingClientRect()
        const chart = visualFrame('orders_chart')
        const tableFrame = visualFrame('orders')
        return {
          title: root.querySelector('h1')?.textContent?.trim(), hostCount: hosts.length,
          legacyCount: root.querySelectorAll('lv-echart, lv-kpi-card, lv-report-table').length,
          kinds: hosts.map((host) => host.envelope?.spec?.kind).sort(),
          statuses: Object.fromEntries(hosts.map((host) => [host.envelope?.visualID, host.envelope?.status?.kind])),
          tableText: table?.shadowRoot?.textContent?.replace(/\s+/g, ' ').trim(),
          tableUpgraded: Boolean(table?.updateComplete && table?.shadowRoot?.childElementCount),
          tableAlert: tableHost?.shadowRoot?.querySelector('[role="alert"]')?.textContent?.trim(),
          tableAlertCount: tableHost?.shadowRoot?.querySelectorAll('[role="alert"]').length ?? 0,
          tableLiveCount: table?.shadowRoot?.querySelectorAll('[aria-live]').length ?? 0,
          interactiveCellButtons: table?.shadowRoot?.querySelectorAll('.cell[role="cell"] button.cell-action').length ?? 0,
          legacyCellButtons: table?.shadowRoot?.querySelectorAll('button[role="cell"]').length ?? 0,
          kpi: {
            tone: kpi?.dataset.tone,
            label: kpiLabel?.textContent?.trim(),
            value: kpiValue?.textContent?.trim(),
            note: kpi?.querySelector('.lv-visualization-note')?.textContent?.trim(),
            display: kpi ? getComputedStyle(kpi).display : '',
            valueSize: kpiValue ? Number.parseFloat(getComputedStyle(kpiValue).fontSize) : 0,
            labelSize: kpiLabel ? Number.parseFloat(getComputedStyle(kpiLabel).fontSize) : 0,
          },
          presentationMode: canvas.shadowRoot.querySelector('.surface')?.dataset.presentationMode,
          canvasScrollbarWidth: getComputedStyle(canvasViewport, '::-webkit-scrollbar').width,
          canvasScrollbarTrack: getComputedStyle(canvasViewport, '::-webkit-scrollbar-track').backgroundColor,
          canvasScrollbarThumb: getComputedStyle(canvasViewport, '::-webkit-scrollbar-thumb').backgroundColor,
          chartHeight: chart?.height ?? 0, tableHeight: tableFrame?.height ?? 0,
          tableAfterChart: (tableFrame?.top ?? 0) > (chart?.bottom ?? 0),
        }
      })
      expect(state.title).toBe('Executive Sales Dashboard')
      expect(state.hostCount).toBe(3)
      expect(state.legacyCount).toBe(0)
      expect(state.kinds).toEqual(['cartesian', 'kpi', 'table'])
      expect(state.statuses).toEqual({ orders_kpi: 'ready', orders_chart: 'loading', orders: 'error' })
      expect(state.tableAlert).toBe('Ratings query failed')
      expect(state.tableAlertCount).toBe(1)
      expect(state.tableLiveCount).toBe(1)
      expect(state.interactiveCellButtons).toBeGreaterThan(0)
      expect(state.legacyCellButtons).toBe(0)
      expect(state.tableText).toContain('o1')
      expect(state.tableUpgraded).toBe(true)
      expect(state.kpi).toMatchObject({ tone: 'ink', label: 'Orders', value: '42', note: 'Filtered', display: 'grid' })
      expect(state.kpi.valueSize).toBeGreaterThan(state.kpi.labelSize)
      if (viewport.name === 'mobile') {
        expect(state.presentationMode).toBe('mobile')
        expect(state.canvasScrollbarWidth).toBe('auto')
        expect(state.chartHeight).toBeGreaterThanOrEqual(280)
        expect(state.tableHeight).toBeLessThanOrEqual(700)
        expect(state.tableAfterChart).toBe(true)
      } else {
        expect(state.presentationMode).toBe('fit-width')
        expect(state.canvasScrollbarWidth).toBe('8px')
        expect(state.canvasScrollbarTrack).toBe('rgb(234, 238, 242)')
        expect(state.canvasScrollbarThumb).toBe('rgb(140, 149, 159)')
      }
    } finally { await page.close() }
  })
}

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

test('compact report footers hide refresh status before it can overlap view controls', async () => {
  const page = await browser.newPage({ viewport: { width: 760, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)

    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const footerHost = element.shadowRoot.querySelector('lv-report-footer') as any
      await footerHost.updateComplete
      const root = footerHost.shadowRoot
      const footer = root.querySelector('footer') as HTMLElement
      const status = root.querySelector('.status') as HTMLElement
      const controls = root.querySelector('lv-report-zoom') as HTMLElement
      const statusRect = status.getBoundingClientRect()
      const controlsRect = controls.getBoundingClientRect()
      return {
        footerWidth: Math.round(footer.getBoundingClientRect().width),
        statusDisplay: getComputedStyle(status).display,
        controlsWithinFooter: controlsRect.right <= footer.getBoundingClientRect().right,
        overlap: statusRect.width > 0 && statusRect.right > controlsRect.left,
      }
    })

    expect(result.footerWidth).toBeLessThanOrEqual(800)
    expect(result.statusDisplay).toBe('none')
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

test('mobile report tables expose horizontal scrolling and a visible swipe hint', async () => {
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
      const hint = table.shadowRoot.querySelector('.table-scroll-hint') as HTMLElement
      return {
        role: scrollport.getAttribute('role'),
        label: scrollport.getAttribute('aria-label'),
        tabIndex: scrollport.getAttribute('tabindex'),
        hint: hint.textContent?.replace(/\s+/g, ' ').trim(),
        hintDisplay: getComputedStyle(hint).display,
      }
    })
    expect(result).toEqual({
      role: 'table', label: 'Orders', tabIndex: '0',
      hint: 'Swipe horizontally to see more columns →', hintDisplay: 'block',
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
      selected.classList.add('hovered')
      return {
        selected: selectedColor,
        pinned: pinnedColor,
        hovered: getComputedStyle(selected).backgroundColor,
        pinnedHovered: getComputedStyle(pinned).backgroundColor,
        unselected: getComputedStyle(unselected).backgroundColor,
      }
    })
    expect(colors.selected).toBe('rgb(221, 244, 255)')
    expect(colors.pinned).toBe(colors.selected)
    expect(colors.hovered).toBe(colors.selected)
    expect(colors.pinnedHovered).toBe(colors.selected)
    expect(colors.pinned).not.toBe(colors.unselected)
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

test('dashboard agent drawer carries page context and explicit visual references', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (
      customElements.get('lv-dashboard-page')
        && customElements.get('lv-chat-drawer')
        && customElements.get('lv-chat-composer')
    ))
    await page.locator('lv-dashboard-page').evaluate((element: any) => element.updateComplete)

    const initial = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const root = element.shadowRoot
      const drawer = root.querySelector('lv-chat-drawer') as any
      const toggle = root.querySelector('.agent-toggle') as HTMLButtonElement
      const toggleStyle = getComputedStyle(toggle)
      return {
        hasToggle: Boolean(toggle),
        toggleHasVisibleSurface: toggleStyle.borderColor !== 'rgba(0, 0, 0, 0)'
          && toggleStyle.backgroundColor !== 'rgba(0, 0, 0, 0)',
        open: drawer?.open,
        drawerWidth: Math.round(drawer?.getBoundingClientRect().width ?? 0),
      }
    })
    expect(initial).toEqual({ hasToggle: true, toggleHasVisibleSurface: true, open: false, drawerWidth: 0 })

    const visualActionsAtRest = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const root = element.shadowRoot
      const frame = root.querySelector('[data-visual-id="orders_chart"]') as any
      const chart = frame?.querySelector('lv-visualization-host') as any
      const kpi = root.querySelector('[data-visual-id="orders_kpi"] lv-visualization-host') as any
      const table = root.querySelector('[data-visual-id="orders"] lv-visualization-host') as any
      await Promise.all([frame?.updateComplete, chart?.updateComplete, kpi?.updateComplete, table?.updateComplete])
      const ask = chart.querySelector('.ask-visual') as HTMLElement
      const kpiAsk = kpi.querySelector('.ask-visual') as HTMLElement
      const tableAsk = table.querySelector('.ask-visual') as HTMLElement
      const askStyle = getComputedStyle(ask)
      const expand = chart.shadowRoot.querySelector('[data-visualization-expand]') as HTMLElement
      const agentIconMarkup = root.querySelector('.agent-toggle svg')?.innerHTML
      const drawer = root.querySelector('lv-chat-drawer') as any
      return {
        askOpacity: askStyle.opacity,
        askPointerEvents: askStyle.pointerEvents,
        askBackground: askStyle.backgroundColor,
        askBoxShadow: askStyle.boxShadow,
        askRight: ask.getBoundingClientRect().right,
        expandLeft: expand.getBoundingClientRect().left,
        askActionRow: ask.assignedSlot?.parentElement?.className,
        kpiAskActionRow: kpiAsk.assignedSlot?.parentElement?.className,
        tableAskActionRow: tableAsk.assignedSlot?.parentElement?.className,
        askPressed: ask.getAttribute('aria-pressed'),
        askUsesAgentIcon: ask.querySelector('svg')?.innerHTML === agentIconMarkup
          && drawer.shadowRoot.querySelector('.title svg')?.innerHTML === agentIconMarkup,
        chartAction: expand.getAttribute('aria-label'),
        tableHasExpand: Boolean(table.shadowRoot.querySelector('[data-visualization-expand]')),
      }
    })
    expect(visualActionsAtRest).toMatchObject({
      askOpacity: '0',
      askPointerEvents: 'none',
      askBackground: 'rgba(0, 0, 0, 0)',
      askBoxShadow: 'none',
      askActionRow: 'visual-actions',
      kpiAskActionRow: 'headerless-actions',
      tableAskActionRow: 'headerless-actions',
      askPressed: 'false',
      askUsesAgentIcon: true,
      chartAction: 'Expand chart',
      tableHasExpand: false,
    })

    await page.locator('lv-dashboard-visual-frame[data-visual-id="orders_chart"]').hover()
    const visualActionsOnHover = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const frame = element.shadowRoot.querySelector('[data-visual-id="orders_chart"]') as any
      const chart = frame.querySelector('lv-visualization-host') as any
      const ask = chart.querySelector('.ask-visual') as HTMLElement
      const expand = chart.shadowRoot.querySelector('[data-visualization-expand]') as HTMLElement
      const askStyle = getComputedStyle(ask)
      return {
        askOpacity: askStyle.opacity,
        askPointerEvents: askStyle.pointerEvents,
        askRight: ask.getBoundingClientRect().right,
        expandLeft: expand.getBoundingClientRect().left,
      }
    })
    expect(visualActionsOnHover.askOpacity).toBe('1')
    expect(visualActionsOnHover.askPointerEvents).toBe('auto')
    expect(visualActionsOnHover.askRight).toBeLessThanOrEqual(visualActionsOnHover.expandLeft)

    await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      element.shadowRoot.querySelector('.agent-toggle').click()
      await element.updateComplete
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer')
      await drawer.updateComplete
    })
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const drawer = dashboard?.shadowRoot?.querySelector('lv-chat-drawer')
      return (drawer?.getBoundingClientRect().width ?? 0) >= 419.9
    })

    const opened = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const root = element.shadowRoot
      const drawer = root.querySelector('lv-chat-drawer') as any
      const drawerRoot = drawer.shadowRoot
      const drawerSurface = drawerRoot.querySelector('.drawer') as HTMLElement
      const header = drawerRoot.querySelector('.header') as HTMLElement
      const context = drawerRoot.querySelector('.context') as HTMLElement
      const toolbarAction = drawerRoot.querySelector('.toolbar-actions button') as HTMLElement
      const thread = drawerRoot.querySelector('lv-chat-thread') as any
      const composer = drawerRoot.querySelector('lv-chat-composer') as any
      const toggle = root.querySelector('.agent-toggle') as HTMLButtonElement
      const toggleRect = toggle.getBoundingClientRect()
      const toggleIconRect = toggle.querySelector('svg')!.getBoundingClientRect()
      return {
        open: drawer.open,
        drawerWidth: Math.round(drawer.getBoundingClientRect().width),
        pageContext: drawerRoot.querySelector('.page-context')?.textContent?.replace(/\s+/g, ' ').trim(),
        filterContext: drawerRoot.querySelector('.filter-context')?.textContent?.replace(/\s+/g, ' ').trim(),
        hasThread: Boolean(thread),
        hasComposer: Boolean(composer),
        contextInHeader: header.contains(context),
        contextBorder: getComputedStyle(context).borderBottomStyle,
        contextSharesSurface: getComputedStyle(context).backgroundColor === getComputedStyle(drawerSurface).backgroundColor,
        toolbarActionBorder: toolbarAction ? getComputedStyle(toolbarAction).borderStyle : 'missing',
        threadSharesSurface: getComputedStyle(thread.shadowRoot.querySelector('.thread')).backgroundColor === getComputedStyle(drawerSurface).backgroundColor,
        composerDockBorder: getComputedStyle(composer).borderTopStyle,
        composerShadow: getComputedStyle(composer.shadowRoot.querySelector('.composer-surface')).boxShadow,
        composerHeight: Math.round(composer.shadowRoot.querySelector('.composer-surface').getBoundingClientRect().height),
        toggleIconCenterOffset: Math.abs((toggleRect.left + toggleRect.width / 2) - (toggleIconRect.left + toggleIconRect.width / 2)),
      }
    })
    expect(opened).toMatchObject({
      open: true,
      pageContext: 'Overview',
      filterContext: '1 filter · 2 selections',
      hasThread: true,
      hasComposer: true,
      contextInHeader: true,
      contextBorder: 'none',
      contextSharesSurface: true,
      toolbarActionBorder: 'none',
      threadSharesSurface: true,
      toggleIconCenterOffset: 0,
      composerDockBorder: 'none',
      composerShadow: 'none',
    })
    expect(opened.composerHeight).toBeLessThan(80)
    expect(opened.drawerWidth).toBeGreaterThanOrEqual(360)
    expect(opened.drawerWidth).toBeLessThanOrEqual(520)

    const groupedSearch = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agentReferenceSearch: {
        query: 'orders', requestId: 1,
        results: [
		  { reference: { kind: 'visual', id: 'executive-sales.orders_chart' }, name: 'Orders by status', hierarchy: ['Sales', 'Executive Sales', 'Overview'], href: '/orders', locations: [{ dashboardId: 'executive-sales', pageId: 'overview', href: '/orders' }], context: ['current_page'] },
		  { reference: { kind: 'visual', id: 'executive-sales.finance_orders' }, name: 'Finance orders', description: 'Finance domain metric', hierarchy: ['Finance', 'Executive Sales', 'Overview'], href: '/finance', locations: [{ dashboardId: 'executive-sales', pageId: 'overview', href: '/finance' }], context: [] },
		  { reference: { kind: 'metric', id: 'olist.order_count' }, name: 'Orders count', description: 'Across the sales model', hierarchy: ['Sales', 'Olist'], href: '/metric', locations: [], context: [] },
        ],
      } })
      await element.updateComplete
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
      const textarea = composer.shadowRoot.querySelector('textarea') as HTMLTextAreaElement
      textarea.value = '@orders'
      textarea.setSelectionRange(textarea.value.length, textarea.value.length)
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await composer.updateComplete
      return {
        labels: Array.from(composer.shadowRoot.querySelectorAll('.mention-section-label')).map((node: any) => node.textContent.trim()),
        options: Array.from(composer.shadowRoot.querySelectorAll('.mention-option')).map((node: any) => node.textContent.replace(/\s+/g, ' ').trim()),
        onPage: Array.from(composer.shadowRoot.querySelector('[aria-label="On this page"]')?.querySelectorAll('.mention-option') ?? []).map((node: any) => node.textContent.replace(/\s+/g, ' ').trim()),
        accessible: Array.from(composer.shadowRoot.querySelector('[aria-label="All accessible"]')?.querySelectorAll('.mention-option') ?? []).map((node: any) => node.textContent.replace(/\s+/g, ' ').trim()),
      }
    })
    expect(groupedSearch.labels).toEqual(['On this page', 'All accessible'])
    expect(groupedSearch.options[0]).toContain('Orders')
	expect(groupedSearch.onPage).toContain('Finance orders Finance › Executive Sales › Overview Visual')
	expect(groupedSearch.accessible).not.toContain('Finance orders Finance › Executive Sales › Overview Visual')
	expect(groupedSearch.options.at(-1)).toBe('Orders count Sales › Olist Metric')

    await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agentContext: { referenceLimit: 1 } })
      await element.updateComplete
    })

    await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const frame = Array.from(element.shadowRoot.querySelectorAll('lv-dashboard-visual-frame'))
        .find((candidate: any) => candidate.getAttribute('data-visual-id') === 'orders_chart') as any
      frame.querySelector('.ask-visual').click()
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
    })

    const referenced = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      const drawerRoot = drawer.shadowRoot
      const composerRoot = drawerRoot.querySelector('lv-chat-composer')?.shadowRoot
      return {
        chip: composerRoot?.querySelector('.reference-chip')?.textContent?.replace(/\s+/g, ' ').trim(),
        highlighted: Boolean(element.shadowRoot.querySelector('lv-dashboard-visual-frame[data-agent-referenced]')),
		pressed: element.shadowRoot.querySelector('[data-visual-id="orders_chart"] .ask-visual')?.getAttribute('aria-pressed'),
      }
    })
    expect(referenced).toEqual({ chip: 'Orders by status', highlighted: true, pressed: 'true' })

    const limitReached = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const frame = Array.from(element.shadowRoot.querySelectorAll('lv-dashboard-visual-frame'))
        .find((candidate: any) => candidate.getAttribute('data-visual-id') === 'orders_kpi') as any
      frame.querySelector('.ask-visual').click()
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
      await composer.updateComplete
      return {
        chips: Array.from(composer.shadowRoot.querySelectorAll('.reference-chip')).map((node: any) => node.textContent?.replace(/\s+/g, ' ').trim()),
        status: drawer.shadowRoot.querySelector('[data-reference-limit-status]')?.textContent?.replace(/\s+/g, ' ').trim(),
      }
    })
    expect(limitReached).toEqual({ chips: ['Orders by status'], status: 'Up to 1 item can be attached' })

    const submitted = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const received: any[] = []
      element.addEventListener('lv-chat-submit', (event: CustomEvent) => received.push(event.detail), { once: true })
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
      const textarea = composer.shadowRoot.querySelector('textarea') as HTMLTextAreaElement
      textarea.value = 'Why did this decline?'
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true }))
      composer.shadowRoot.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await new Promise((resolve) => setTimeout(resolve, 0))
      return received[0]
    })
    expect(submitted).toEqual({
      input: 'Why did this decline?',
      references: [{
        reference: { kind: 'visual', id: 'executive-sales.orders_chart' },
        name: 'Orders by status',
        visualType: 'bar',
        hierarchy: ['project:leapview-evaluation', 'Executive Sales Dashboard', 'Overview'],
        href: '/dashboards/executive-sales/pages/overview',
        locations: [{ dashboardId: 'executive-sales', dashboardName: 'Executive Sales Dashboard', pageId: 'overview', pageName: 'Overview', href: '/dashboards/executive-sales/pages/overview' }],
        context: ['current_page', 'current_dashboard', 'current_project'],
      }],
    })

	const accepted = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
	  const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
	  mergePatch({ agent: {
		activeConversationId: 'agentconv_1',
		transcript: [{
		  id: 'user_1', kind: 'user', runId: 'run_1', text: 'Why did this decline?',
		  references: [{
			reference: { kind: 'visual', id: 'executive-sales.orders_chart' },
			name: 'Orders by status',
			hierarchy: ['Sales', 'Executive Sales Dashboard', 'Overview'],
			href: '/dashboards/executive-sales/pages/overview', locations: [], context: ['current_page'],
		  }],
		}],
		status: { enabled: true, running: true },
		composer: { value: '', disabled: true, placeholder: 'Agent is working…' },
	  } })
	  await element.updateComplete
	  const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
	  await drawer.updateComplete
	  const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
	  const thread = drawer.shadowRoot.querySelector('lv-chat-thread') as any
	  await Promise.all([composer.updateComplete, thread.updateComplete])
	  return {
		composerReferences: composer.references.length,
		draft: composer.shadowRoot.querySelector('textarea').value,
		bubble: thread.shadowRoot.querySelector('.message.user .bubble')?.textContent?.replace(/\s+/g, ' ').trim(),
		highlighted: Boolean(element.shadowRoot.querySelector('lv-dashboard-visual-frame[data-agent-referenced]')),
	  }
	})
	expect(accepted).toEqual({
	  composerReferences: 0,
	  draft: '',
	  bubble: 'Orders by status Why did this decline?',
	  highlighted: false,
	})
  } finally {
    await page.close()
  }
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
    await page.locator('lv-dashboard-page h1').click()
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

test('date-range slicers rearrange at contract boundaries without removing either input', async () => {
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
        inputs: leaf.shadowRoot.querySelectorAll('.range input[type="date"]').length,
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
    expect(result.inline.inputs).toBe(2)
    expect(result.inline.columns.split(' ')).toHaveLength(2)
    expect(result.stacked).toMatchObject({ variant: 'stacked', fit: 'fit', inputs: 2, columns: '240px' })
    expect(result.invalid).toMatchObject({ variant: 'stacked', fit: 'too-small', inputs: 2 })
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
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page)
    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      let command: any
      element.addEventListener('lv-filter-command', (event: CustomEvent) => {
        command = event.detail
      }, { once: true })
      element.filterController.mutate('fb_state', {
        kind: 'set',
        operator: 'in',
        values: [{ kind: 'string', value: 'CA' }],
      })
      element.requestUpdate()
      await element.updateComplete
      const optimistic = element.filterController.projected.appliedControls.fb_state.expression

      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({
        filterValidation: {
          accepted: false,
          message: 'range lower bound must not exceed upper bound',
          currentRevision: 0,
          clientMutationID: command.clientMutationID,
        },
      })
      await element.updateComplete
      return {
        optimistic,
        reconciled: element.filterController.projected.appliedControls.fb_state.expression,
        pending: element.filterController.pending,
        alert: element.shadowRoot.querySelector('[role="alert"]')?.textContent?.trim(),
      }
    })
    expect(result).toEqual({
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
      const retained = Array.from(leaf.shadowRoot.querySelectorAll('option')).map((option: HTMLOptionElement) => ({
        label: option.textContent?.trim(),
        selected: option.selected,
      }))
      leaf.shadowRoot.querySelector('select').focus()
      await leaf.updateComplete
      const afterOpen = seen.length
      leaf.shadowRoot.querySelector('select').blur()
      leaf.optionContext = 'context-two'
      await leaf.updateComplete
      const afterDependencyChange = seen.length
      const whileDeferred = Array.from(leaf.shadowRoot.querySelectorAll('option')).map((option: HTMLOptionElement) => option.textContent?.trim())
      leaf.shadowRoot.querySelector('select').focus()
      await leaf.updateComplete
      return { retained, afterOpen, afterDependencyChange, whileDeferred, afterRefocus: seen.length }
    })
    expect(requests).toEqual({
      retained: [
        { label: 'All', selected: false },
        { label: 'AC', selected: true },
      ],
      afterOpen: 1,
      afterDependencyChange: 1,
      whileDeferred: ['All', 'AC'],
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

test('dashboard agent drawer folds out with the dashboard motion contract', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.addInitScript(() => localStorage.removeItem('leapview-dashboard-agent-state'))
    await page.goto(baseURL)
    await page.waitForFunction(() => (
      customElements.get('lv-dashboard-page')
        && customElements.get('lv-chat-drawer')
        && (document.querySelector('lv-dashboard-page') as any)?.page
    ))

    const motion = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const route = root.querySelector('.route') as HTMLElement
      const drawer = root.querySelector('lv-chat-drawer') as HTMLElement
      const toggle = root.querySelector('.agent-toggle') as HTMLButtonElement
      const before = getComputedStyle(route)
      const closedWidth = drawer.getBoundingClientRect().width
      toggle.click()
      await element.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      return {
        transitionProperty: before.transitionProperty,
        transitionDuration: before.transitionDuration,
        animatedProperties: route.getAnimations().map((animation) => (
          'transitionProperty' in animation ? (animation as CSSTransition).transitionProperty : ''
        )),
        closedWidth: Math.round(closedWidth),
        openingWidth: Math.round(drawer.getBoundingClientRect().width),
      }
    })

    expect(motion.transitionProperty).toContain('grid-template-columns')
    expect(motion.transitionDuration).toBe('0.16s')
    expect(motion.animatedProperties).toContain('grid-template-columns')
    expect(motion.closedWidth).toBe(0)
    expect(motion.openingWidth).toBeGreaterThan(0)
    expect(motion.openingWidth).toBeLessThan(420)

    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const drawer = dashboard?.shadowRoot?.querySelector('lv-chat-drawer')
      return (drawer?.getBoundingClientRect().width ?? 0) >= 419.9
    })
    const openWidth = await page.locator('lv-dashboard-page').evaluate((element: any) => (
      Math.round(element.shadowRoot.querySelector('lv-chat-drawer')?.getBoundingClientRect().width ?? 0)
    ))
    expect(openWidth).toBe(420)

    await page.emulateMedia({ reducedMotion: 'reduce' })
    const reducedMotionDuration = await page.locator('lv-dashboard-page').evaluate((element: any) => (
      getComputedStyle(element.shadowRoot.querySelector('.route')).transitionDuration
    ))
    expect(reducedMotionDuration).toBe('0s')
  } finally {
    await page.close()
  }
})

test('dashboard agent restores its open state and active conversation after reload', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.addInitScript(() => {
      ;(window as any).__agentRestoreRequests = []
      if (localStorage.getItem('leapview-dashboard-agent-state') === null) {
        localStorage.setItem('leapview-dashboard-agent-state', JSON.stringify({
          open: true,
          conversationId: 'agentconv_saved',
        }))
      }
      window.addEventListener('lv-chat-restore', (event: Event) => {
        ;(window as any).__agentRestoreRequests.push((event as CustomEvent).detail)
        // This browser fixture has no dashboard command backend. Keep the test
        // focused on persistence and prevent Datastar from following the
        // synthetic restore command while assertions are running.
        // Datastar also listens on window. Stop later listeners on the same
        // target so the synthetic restore cannot race these assertions with a
        // navigation.
        event.stopImmediatePropagation()
      }, { capture: true })
    })
    await page.goto(baseURL)
    await page.waitForLoadState('networkidle')
    await page.waitForFunction(() => (
      customElements.get('lv-dashboard-page')
        && (window as any).__agentRestoreRequests?.length === 1
    ))

    const restoredShell = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      return {
        open: drawer.open,
        request: (window as any).__agentRestoreRequests[0],
      }
    })
    expect(restoredShell).toEqual({
      open: true,
      request: { conversationId: 'agentconv_saved' },
    })

    await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: {
        activeConversationId: 'agentconv_saved',
        transcript: [{ id: 'user_saved', kind: 'user', text: 'Persisted question' }],
      } })
      await element.updateComplete
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
      drawer.shadowRoot.querySelector('[aria-label="Close agent"]').click()
      await element.updateComplete
    })

    const closedState = await page.locator('lv-dashboard-page').evaluate((element: any) => ({
      open: element.shadowRoot.querySelector('lv-chat-drawer')?.open,
      persisted: JSON.parse(localStorage.getItem('leapview-dashboard-agent-state') || '{}'),
    }))
    expect(closedState).toEqual({
      open: false,
      persisted: { open: false, conversationId: 'agentconv_saved' },
    })

    await page.reload()
    await page.waitForLoadState('networkidle')
    await page.waitForFunction(() => (
      customElements.get('lv-dashboard-page')
        && (window as any).__agentRestoreRequests?.length === 1
    ))
    const reloadedClosedState = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      return {
        open: element.shadowRoot.querySelector('lv-chat-drawer')?.open,
        request: (window as any).__agentRestoreRequests[0],
      }
    })
    expect(reloadedClosedState).toEqual({
      open: false,
      request: { conversationId: 'agentconv_saved' },
    })
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  const page = {
    kind: 'dashboard', title: 'Executive Sales Dashboard', dashboardId: 'executive-sales', dashboardTitle: 'Executive Sales Dashboard',
    pageId: 'overview', pageTitle: 'Overview', headerDetail: '1. Overview', modelId: 'olist', modelTitle: 'Olist',
    canvas: { width: 1024, height: 720 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 },
    pages: [
      { id: 'overview', title: 'Overview', href: '/dashboards/executive-sales/pages/overview', active: true },
      { id: 'details', title: 'Details', href: '/dashboards/executive-sales/pages/details', active: false },
    ],
    components: [
      { id: 'title', kind: 'header', x: 16, y: 16, width: 456, height: 88, title: 'Executive Sales' },
      { id: 'state-slicer', kind: 'slicer', binding: { scope: 'page', id: 'state' }, presentation: { style: 'dropdown', search: true, selectAll: false, showCounts: false, showSummary: true, compact: false }, x: 488, y: 16, width: 216, height: 88 },
      { id: 'orders-kpi', kind: 'visual', visual: 'orders_kpi', x: 720, y: 16, width: 240, height: 88 },
      { id: 'orders-chart', kind: 'visual', visual: 'orders_chart', x: 16, y: 128, width: 456, height: 280 },
      { id: 'orders-table', kind: 'visual', visual: 'orders', x: 16, y: 760, width: 944, height: 280 },
    ],
  }
  const interactionSelections = [
      { sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection', entries: [{ label: 'Delivered', mappings: [{ field: 'orders.status', dataset: 'orders', value: 'delivered' }] }] },
      { sourceKind: 'visual', sourceId: 'orders', interactionKind: 'row_selection', entries: [{ label: 'o1', mappings: [{ field: 'orders.order_id', dataset: 'orders', value: 'o1' }] }] },
  ]
  const filterState = {
    revision: 0,
    appliedControls: {
      fb_state: {
        expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'SP' }] },
        resolvedExpression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'SP' }] },
      },
    },
    draftControls: {},
    dirtyBindings: [],
    defaultsRevision: 'v1',
  }
  const signals = {
    page,
    filterContract: {
      applicationMode: 'immediate',
      definitions: {
        state: {
          id: 'state', label: 'State', field: 'orders.state', valueKind: 'string',
          predicates: [{ kind: 'set', operators: ['in'] }],
          options: { kind: 'distinct', limit: 50, values: [] },
          timezone: 'UTC', calendar: 'gregorian', weekStart: 'monday',
        },
      },
      bindings: {
        fb_state: {
          key: 'fb_state', id: 'state', filter: 'state', scope: 'page', pageID: 'overview',
          default: { kind: 'unfiltered' }, selectionMode: 'multiple', maxSelectedValues: 0,
          readerEditable: true, paneVisible: true, paneOrder: 0, targets: ['overview/orders-chart'],
          optionDependencies: [],
        },
      },
    },
    filterState,
    filterOptionPages: {
      fb_state: {
        bindingKey: 'fb_state', items: [{ value: { kind: 'string', value: 'SP' }, label: 'SP', selected: false, available: true }],
        complete: true, servingStateID: 'serving-test', streamGeneration: 3, filterRevision: 0,
        requestGeneration: 0, consumerIdentity: 'option:fb_state',
      },
    },
    filterValidation: {
      accepted: true,
      message: '',
      currentRevision: 0,
      clientMutationID: '',
    },
    runtime: {
      kind: 'dashboard', clientId: 'dashboard-test', streamInstanceId: 'stream-test',
      projectId: 'project:leapview-evaluation', dashboardId: 'executive-sales', pageId: 'overview', servingStateId: 'serving-test',
    },
    interactionSelections,
    interactionRevision: 0,
    spatialSelections: [],
    visuals: testVisualizationSignals(),
    status: { loading: true, error: '', refreshId: 'refresh-3', generation: 3, lastUpdated: '2026-07-18T10:00:00Z', setupRequired: false, progressPercent: 50 },
    agent: {
      conversations: [],
      activeConversationId: '',
      transcript: [],
      status: { enabled: true, running: false },
      composer: { value: '', disabled: false, placeholder: 'Ask about this dashboard...' },
    },
    agentContext: {
      surface: 'dashboard',
      dashboardId: 'executive-sales',
      dashboardTitle: 'Executive Sales Dashboard',
      pageId: 'overview',
      pageTitle: 'Overview',
      modelId: 'olist',
      generation: 3,
      filters: filterState,
      references: [],
    },
    agentReferenceSearch: { query: '', requestId: 0, results: [] },
    agentVisuals: {},
  }
  const attr = (value: unknown) => escapeHTML(JSON.stringify(value))
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-panel: #fff; --lv-bg-panel-muted: #eaeef2; --lv-bg-control-hover: #f3f4f6; --lv-chart-surface: #fff; --lv-report-page-bg: #fff; --lv-report-canvas-bg: #eaeef2; --lv-report-rail-bg: #fff; --lv-bg-overlay: #fff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-line-muted: #d8dee4; --lv-scrollbar-thumb: #8c959f; --lv-scrollbar-thumb-hover: #6e7781; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-border-transparent: 1px solid transparent; --lv-radius-default: 6px; --lv-radius-full: 999px; --lv-page-rail-width-collapsed: 38px; --lv-dashboard-filter-open-width: 320px; --lv-dashboard-agent-width: 420px; --base-size-2: 2px; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --base-size-24: 24px; --borderWidth-default: 1px; --control-medium-size: 32px; --control-xlarge-size: 40px; --focus-outline: 2px solid #0969da; --focus-outline-offset: -2px; --zIndex-dropdown: 100; --zIndex-modal: 200; --zIndex-sticky: 50; --shadow-resting-small: 0 1px 2px rgb(0 0 0 / .08); --shadow-floating-small: 0 8px 24px rgb(0 0 0 / .12); --lv-duration-fast: 160ms; --spinner-size-small: 16px; --spinner-size-medium: 32px; --spinner-size-large: 64px; --base-duration-1000: 1000ms; --base-easing-linear: linear; --motion-easing-move: ease; --motion-transition-stateChange: 160ms ease; }
          body { --lv-loading-delay-short: 250ms; --lv-loading-delay-long: 500ms; }
          lv-dashboard-page { min-height: 720px; }
        </style>
      </head>
      <body>
        <main data-signals="${attr(signals)}">
          <lv-dashboard-page></lv-dashboard-page>
        </main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/dashboard-page-under-test.js"></script>
      </body>
    </html>
  `
}

function testVisualizationEnvelopes(): Record<string, VisualizationEnvelope> {
  const kpiRevision = `sha256:${'1'.repeat(64)}`
  const chartRevision = `sha256:${'2'.repeat(64)}`
  const tableRevision = `sha256:${'3'.repeat(64)}`
  const field = (id: string, role: VisualizationField['role'], dataType: VisualizationField['dataType'], label: string): VisualizationField => ({ id, role, dataType, nullable: false, label })
  const base = (title: string, fields: VisualizationField[]): Omit<VisualizationSpecBase, 'kind'> => ({ title, datasets: [{ id: 'primary', fields }], dataBudget: { maxRows: 1000, requiredCompleteness: 'complete' }, accessibility: { title, description: title }, interactions: [] })
  const inline = (revision: string, columns: string[], rows: unknown[][]): Extract<VisualizationDataState, { kind: 'inline' }> => ({ kind: 'inline', specRevision: revision, dataRevision: 1, generation: 3, datasets: [{ id: 'primary', specRevision: revision, dataRevision: 1, generation: 3, columns, rows, completeness: 'complete' }] })
  const envelopes = {
    orders_kpi: { schemaVersion: 11, visualID: 'orders_kpi', rendererID: 'html', specRevision: kpiRevision, dataRevision: 1, spec: { ...base('Orders', [field('value', 'metric', 'decimal', 'Orders')]), kind: 'kpi', value: { dataset: 'primary', field: 'value' }, presentation: { mode: 'compact', delta: 'absolute', favorableDirection: 'neutral', missingComparison: 'show_unavailable', ranges: [], tone: 'ink', note: 'Filtered' } }, dataState: inline(kpiRevision, ['value'], [[42]]), selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [] },
    orders_chart: { schemaVersion: 11, visualID: 'orders_chart', rendererID: 'echarts', specRevision: chartRevision, dataRevision: 1, spec: { ...base('Orders by status', [field('label', 'identity', 'string', 'Status'), field('value', 'metric', 'decimal', 'Orders')]), kind: 'cartesian', mark: 'bar', interactions: [{ id: 'selection', kind: 'select', mappings: [{ source: { dataset: 'primary', field: 'label' }, targetFieldID: 'orders.status', targetDatasetID: 'orders' }], targets: [{ visualID: 'orders_kpi', effect: 'highlight' }, { visualID: 'orders', effect: 'filter' }], mode: 'multiple', requiresStableIdentity: true }], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } }, dataState: inline(chartRevision, ['label', 'value'], [['delivered', 42], ['shipped', 7]]), selection: [], highlights: [], status: { kind: 'loading', message: 'Refreshing' }, diagnostics: [] },
    orders: { schemaVersion: 11, visualID: 'orders', rendererID: 'tanstack', specRevision: tableRevision, dataRevision: 1, spec: { ...base('Orders', [field('order_id', 'identity', 'string', 'Order')]), kind: 'table', dataBudget: { maxRows: 1000, requiredCompleteness: 'partial' }, columns: [{ field: { dataset: 'primary', field: 'order_id' }, label: 'Order', width: 180, formatting: [] }], defaultSort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], presentation: { rowHeight: 28, striped: true, showHeader: true } }, dataState: { kind: 'windowed', specRevision: tableRevision, dataRevision: 1, generation: 3, schema: { id: 'primary', fields: [field('order_id', 'identity', 'string', 'Order')] }, cardinality: { kind: 'exact', count: 250 }, availableRows: 250, rowCap: 1000, chunkSize: 50, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], blocks: { a: { id: 'a', start: 0, rows: Array.from({ length: 50 }, (_, index) => [`o${index + 1}`]), requestSeq: 0, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }] } } }, selection: [], highlights: [], status: { kind: 'error', message: 'Ratings query failed' }, diagnostics: [{ code: 'query_failed', severity: 'error', message: 'Ratings query failed' }] },
  }
  return envelopes
}

function testVisualizationSignals(): Record<string, DashboardVisualizationSignal> {
  const signals: Record<string, DashboardVisualizationSignal> = {}
  for (const [id, envelope] of Object.entries(testVisualizationEnvelopes())) {
    const { dataState, ...signal } = envelope
    signals[id] = {
      ...signal,
      servingStateID: 'serving-test',
      streamGeneration: 3,
      filterRevision: 0,
      interactionRevision: 0,
      consumerIdentity: `visual:${id}`,
      dataState: visualizationDataStateTransport(dataState),
    }
  }
  return signals
}

function visualizationDataStateTransport(dataState: VisualizationDataState): VisualizationDataStateTransport {
  return {
    schemaVersion: 1,
    encoding: 'json',
    kind: dataState.kind,
    specRevision: dataState.specRevision,
    dataRevision: dataState.dataRevision,
    generation: dataState.generation,
    payload: JSON.stringify(dataState),
  }
}

function escapeHTML(value: string): string { return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;') }
