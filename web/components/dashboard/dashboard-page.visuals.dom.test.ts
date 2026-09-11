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

test('every viewer presentation defers hosts and explicit capture readiness propagates failures', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.title === 'Executive Sales Dashboard')
    const state = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const modes = [
        { presentation: 'app', readOnly: false },
        { presentation: 'public', readOnly: false },
        { presentation: 'embed', readOnly: false },
        { presentation: 'app', readOnly: true },
      ]
      const deferredByMode: Record<string, boolean> = {}
      for (const mode of modes) {
        element.presentation = mode.presentation
        element.readOnly = mode.readOnly
        await element.updateComplete
        const hosts = Array.from(element.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
        deferredByMode[`${mode.presentation}:${mode.readOnly}`] = hosts.length > 0
          && hosts.every((host) => host.deferMount && host.hasAttribute('defer-mount'))
      }

      await element.ensureVisualizationsMounted()
      const hosts = Array.from(element.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
      const original = hosts[0].ensureMounted
      hosts[0].ensureMounted = async () => { throw new Error('capture mount failed') }
      let failure = ''
      try {
        await element.ensureVisualizationsMounted()
      } catch (error) {
        failure = error instanceof Error ? error.message : String(error)
      } finally {
        hosts[0].ensureMounted = original
      }
      return {
        deferredByMode,
        mounted: hosts.every((host) => (host.shadowRoot?.querySelector('.renderer')?.childElementCount ?? 0) > 0),
        failure,
      }
    })
    expect(state.deferredByMode).toEqual({
      'app:false': true,
      'public:false': true,
      'embed:false': true,
      'app:true': true,
    })
    expect(state.mounted).toBe(true)
    expect(state.failure).toBe('capture mount failed')
  } finally {
    await page.close()
  }
})

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
        await element.ensureVisualizationsMounted()
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
          deferredHosts: hosts.filter((host) => host.deferMount && host.hasAttribute('defer-mount')).length,
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
            notes: [...(kpi?.querySelectorAll('.lv-visualization-note') ?? [])].map((note) => note.textContent?.trim()),
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
      expect(state.deferredHosts).toBe(state.hostCount)
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
      expect(state.kpi).toMatchObject({
        tone: 'ink', label: 'Orders', value: '42',
        note: 'Selection highlighted. Comparison total is unchanged.',
        notes: ['Selection highlighted. Comparison total is unchanged.', 'Filtered'],
        display: 'grid',
      })
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

test('visualization actions keep touch targets and spacing when a report is scaled', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.title === 'Executive Sales Dashboard')
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      document.documentElement.style.setProperty('--control-xsmall-size', '20px')
      document.documentElement.style.setProperty('--lv-button-height-xs', '20px')
      document.documentElement.style.setProperty('--control-small-size', '20px')
      document.documentElement.style.setProperty('--lv-button-height-sm', '20px')
      document.dispatchEvent(new CustomEvent('lv-report-zoom-command', { detail: { mode: 'custom', scale: 0.8 } }))
      const canvas = dashboard.shadowRoot.querySelector('lv-report-canvas') as any
      await canvas.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))
      await canvas.updateComplete
      const hosts = Array.from(dashboard.shadowRoot.querySelectorAll('lv-visualization-host')) as any[]
      const chart = hosts.find((host) => host.envelope?.visualID === 'orders_chart')
      const options = chart?.shadowRoot?.querySelector('.visual-options') as HTMLDetailsElement | null
      if (options) {
        options.open = true
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const actions = [
        chart?.shadowRoot?.querySelector('[data-visualization-expand]'),
        chart?.shadowRoot?.querySelector('.visual-options summary'),
      ].filter(Boolean) as HTMLElement[]
      const rects = actions.map((action) => {
        const rect = action.getBoundingClientRect()
        return { width: rect.width, height: rect.height, left: rect.left, right: rect.right }
      })
      const menuRects = Array.from(chart?.shadowRoot?.querySelectorAll('.visual-options .menu button') ?? []).map((button) => {
        const rect = (button as HTMLElement).getBoundingClientRect()
        return { width: rect.width, height: rect.height, top: rect.top, bottom: rect.bottom }
      })
      return {
        scale: canvas.shadowRoot.querySelector('.surface')?.dataset.scale,
        inverseScale: getComputedStyle(canvas.shadowRoot.querySelector('.surface')).getPropertyValue('--report-canvas-inverse-scale'),
        rects,
        menuRects,
        gap: rects.length === 2 ? rects[1].left - rects[0].right : 0,
      }
    })
    expect(result.scale).toBe('0.8')
    expect(Number(result.inverseScale)).toBeCloseTo(1.25)
    expect(result.rects).toHaveLength(2)
    for (const rect of result.rects) {
      expect(rect.width).toBeGreaterThanOrEqual(24)
      expect(rect.height).toBeGreaterThanOrEqual(24)
    }
    expect(result.gap).toBeGreaterThanOrEqual(0)
    expect(result.menuRects.length).toBeGreaterThan(0)
    for (const rect of result.menuRects) {
      expect(rect.height).toBeGreaterThanOrEqual(24)
    }
    for (let index = 1; index < result.menuRects.length; index += 1) {
      expect(result.menuRects[index].top).toBeGreaterThanOrEqual(result.menuRects[index - 1].bottom)
    }
  } finally { await page.close() }
})
