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
        const hosts = Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('lv-visualization-host'))
        deferredByMode[`${mode.presentation}:${mode.readOnly}`] = hosts.length > 0
          && hosts.every((host) => host.deferMount && host.hasAttribute('defer-mount'))
      }

      await element.ensureVisualizationsMounted()
      const hosts = Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('lv-visualization-host'))
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
        mounted: hosts.every((host) => ((host.shadowRoot as ShadowRoot)?.querySelector('.renderer')?.childElementCount ?? 0) > 0),
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

for (const emptyResponse of [false, true]) test(`windowed table reconciles cached rows after a delayed ${emptyResponse ? 'empty' : 'populated'} jump response`, async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => Boolean([...((document.querySelector('lv-dashboard-page') as any)?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? [])].find((host: any) => host.envelope?.visualID === 'orders')?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.table-scrollport')))
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any, emptyResponse: boolean) => {
      const host = [...(dashboard.shadowRoot as ShadowRoot).querySelectorAll('lv-visualization-host')].find((item: any) => item.envelope?.visualID === 'orders') as any
      const table = (host.shadowRoot as ShadowRoot).querySelector('lv-report-table') as any
      await table.updateComplete
      const base = table.table
      const rows = (start: number) => Array.from({ length: 100 }, (_, offset) => ({ ...(base.blocks.a.rows[0] ?? {}), order_id: `bounce-${start + offset}` }))
      const block = (start: number, requestSeq = 0) => ({ ...base.blocks.a, start, requestSeq, resetVersion: base.resetVersion, rows: rows(start) })
      const emptyBlock = (start: number, requestSeq: number) => ({ ...block(start, requestSeq), rows: [] })
      table.clearJumpTimer()
      table.expectedBlocks.clear()
      table.table = { ...base, availableRows: 1000, rowCap: 10000, chunkSize: 100, cardinality: { kind: 'exact', value: 1000 }, blocks: { a: block(0), b: block(100), c: block(200) }, loadingBlock: '' }
      await table.updateComplete
      const viewport = (table.shadowRoot as ShadowRoot).querySelector('.table-scrollport') as HTMLElement
      const requests: any[] = []
      table.addEventListener('lv-visualization-window-request', (event: CustomEvent) => { requests.push(event.detail); event.stopImmediatePropagation() }, { capture: true })
      const waitForRequest = async (start: number, after = -1) => {
        const deadline = Date.now() + 2000
        while (!requests.some(r => r.start === start && r.requestSeq > after)) {
          if (Date.now() > deadline) throw new Error(`No request for row ${start}`)
          await new Promise(resolve => setTimeout(resolve, 10))
        }
        return requests.find(r => r.start === start && r.requestSeq > after)
      }
      await new Promise(resolve => requestAnimationFrame(resolve))
      viewport.scrollTop = 500 * table.rowHeight
      viewport.dispatchEvent(new Event('scroll'))
      const jump = await waitForRequest(500)
      viewport.scrollTop = 0
      viewport.dispatchEvent(new Event('scroll'))
      await new Promise((resolve) => window.setTimeout(resolve, 20))
      const jumpBlock = emptyResponse ? emptyBlock : block
      table.table = { ...table.table, blocks: { a: jumpBlock(400, jump.requestSeq), b: jumpBlock(500, jump.requestSeq), c: jumpBlock(600, jump.requestSeq) }, loadingBlock: '' }
      await table.updateComplete
      const followUp = await waitForRequest(0, jump.requestSeq)
      table.table = { ...table.table, blocks: { a: emptyBlock(0, followUp.requestSeq), b: block(100, followUp.requestSeq), c: block(200, followUp.requestSeq) }, loadingBlock: '' }
      await table.updateComplete
      await new Promise((resolve) => window.setTimeout(resolve, 140))
      return { jump, followUp, requests, requestCount: requests.length, skeletons: table.visibleRows.filter((row: any) => row.kind === 'skeleton').length }
    }, emptyResponse)
    expect(result.jump).toEqual(expect.objectContaining({ blockID: 'all', start: 500, limit: 100 }))
    expect(result.followUp).toEqual(expect.objectContaining({ blockID: 'all', start: 0, limit: 100 }))
    expect(result.requests.filter((r: any) => r.requestSeq >= result.jump.requestSeq).map((r: any) => [r.blockID, r.start])).toEqual([['all', 500], ['all', 0]])
    expect(result.skeletons).toBeGreaterThan(0)
  } finally { await page.close() }
})

test('windowed table only shows loading for missing visible rows', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => Boolean([...((document.querySelector('lv-dashboard-page') as any)?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? [])].find((host: any) => host.envelope?.visualID === 'orders')?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.table-scrollport')))
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const tableHost = [...(dashboard.shadowRoot as ShadowRoot).querySelectorAll('lv-visualization-host')].find((host: any) => host.envelope?.visualID === 'orders') as any
      const table = (tableHost.shadowRoot as ShadowRoot).querySelector('lv-report-table') as any
      await table.updateComplete
      const base = table.table
      const seed = base.blocks.a.rows[0] ?? {}
      const rows = (start: number) => Array.from({ length: 100 }, (_, offset) => ({ ...seed, order_id: `synthetic-${start + offset}` }))
      const block = (start: number, present = true) => ({ ...base.blocks.a, start, requestSeq: 0, resetVersion: base.resetVersion, rows: present ? rows(start) : [] })
      table.clearJumpTimer()
      table.expectedBlocks.clear()
      table.table = {
        ...base,
        availableRows: 500,
        rowCap: 10000,
        chunkSize: 100,
        cardinality: { kind: 'exact', value: 500 },
        blocks: { a: block(0), b: block(100), c: block(200) },
        loadingBlock: '',
      }
      await table.updateComplete
      const viewport = (table.shadowRoot as ShadowRoot).querySelector('.table-scrollport') as HTMLElement
      const requests: unknown[] = []
      table.addEventListener('lv-visualization-window-request', (event: CustomEvent) => {
        requests.push(event.detail)
        event.stopImmediatePropagation()
      }, { capture: true })
      const snapshot = () => ({
        skeletons: table.visibleRows.filter((row: any) => row.kind === 'skeleton').length,
        visibleLoading: table.visibleLoading,
        loadingBar: (table.shadowRoot as ShadowRoot).querySelector('.loading') !== null,
        footer: (table.shadowRoot as ShadowRoot).querySelector('.footer')?.textContent?.replace(/\s+/g, ' ').trim(),
      })
      viewport.scrollTop = 200 * table.rowHeight
      viewport.dispatchEvent(new Event('scroll'))
      await new Promise((resolve) => window.setTimeout(resolve, 140))
      const prefetch = snapshot()
      table.clearJumpTimer()
      table.expectedBlocks.clear()
      table.table = { ...table.table, blocks: { a: block(0), b: block(100), c: block(200, false) }, loadingBlock: '' }
      await table.updateComplete
      viewport.scrollTop = 200 * table.rowHeight
      viewport.dispatchEvent(new Event('scroll'))
      await new Promise((resolve) => window.setTimeout(resolve, 140))
      return { prefetch, missing: snapshot(), requests }
    })
    expect(result.requests).toEqual(expect.arrayContaining([
      expect.objectContaining({ blockID: 'a', start: 300, limit: 100 }),
      expect.objectContaining({ blockID: 'all', start: 200, limit: 100 }),
    ]))
    expect(result.prefetch).toEqual({ skeletons: 0, visibleLoading: false, loadingBar: false, footer: expect.not.stringContaining('loading') })
    expect(result.missing.skeletons).toBeGreaterThan(0)
    expect(result.missing.visibleLoading).toBe(true)
    expect(result.missing.loadingBar).toBe(true)
  } finally {
    await page.close()
  }
})

test('empty table distinguishes completed results from waiting and keeps its message visible', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-report-table'))
    const result = await page.evaluate(async () => {
      const table = document.createElement('lv-report-table') as any
      table.style.cssText = 'display:block;width:380px;height:350px;'
      document.body.prepend(table)
      await table.updateComplete
      const waiting = table.shadowRoot.querySelector('.empty')?.textContent
      table.table = { ...table.table, cardinality: { kind: 'exact', value: 0 } }
      await table.updateComplete
      const message = table.shadowRoot.querySelector('.empty') as HTMLElement
      const scrollport = table.shadowRoot.querySelector('.table-scrollport') as HTMLElement
      scrollport.scrollLeft = 200
      await new Promise(resolve => requestAnimationFrame(resolve))
      const outer = scrollport.getBoundingClientRect()
      const inner = message.getBoundingClientRect()
      return { waiting, message: message.textContent, visible: inner.left >= outer.left && inner.right <= outer.right + 1 }
    })
    expect(result.waiting).toBe('Waiting for table data')
    expect(result.message).toBe('No rows to display')
    expect(result.visible).toBe(true)
  } finally { await page.close() }
})

test('short report tables keep the footer next to the final row while long tables remain scrollable', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-report-table'))
    const result = await page.evaluate(async () => {
      const table = document.createElement('lv-report-table') as any
      table.style.cssText = 'display:block;width:760px;height:540px;'
      const sort = { key: 'id', direction: 'asc' }
      const rows = (count: number) => Array.from({ length: count }, (_, index) => ({ id: `row-${index + 1}` }))
      const signal = (count: number) => ({
        ...table.table,
        columns: [{ key: 'id', label: 'ID' }],
        sort,
        resetVersion: 1,
        cardinality: { kind: 'exact', value: count },
        availableRows: count,
        rowCap: 1_000,
        chunkSize: 100,
        rowHeight: 32,
        blocks: { a: { start: 0, requestSeq: 0, resetVersion: 1, sort, rows: rows(count) } },
      })
      table.table = signal(6)
      document.body.prepend(table)
      await table.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const root = table.shadowRoot as ShadowRoot
      const canvas = root.querySelector('.canvas') as HTMLElement
      const footer = root.querySelector('.footer') as HTMLElement
      const shortGap = Math.round(footer.getBoundingClientRect().top - canvas.getBoundingClientRect().bottom)

      table.table = signal(100)
      await table.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const scrollport = root.querySelector('.table-scrollport') as HTMLElement
      return {
        shortGap,
        longTableScrolls: scrollport.scrollHeight > scrollport.clientHeight,
        footerWithinTable: footer.getBoundingClientRect().bottom <= table.getBoundingClientRect().bottom + 1,
      }
    })
    expect(result.shortGap).toBeLessThanOrEqual(2)
    expect(result.longTableScrolls).toBe(true)
    expect(result.footerWithinTable).toBe(true)
  } finally { await page.close() }
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
        const root = (element.shadowRoot as ShadowRoot)
        const hosts = Array.from(root.querySelectorAll('lv-visualization-host'))
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
        const canvasViewport = (canvas.shadowRoot as ShadowRoot).querySelector('.viewport') as HTMLElement
        const assigned = ((canvas.shadowRoot as ShadowRoot).querySelector('slot') as HTMLSlotElement).assignedElements() as HTMLElement[]
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
          presentationMode: (canvas.shadowRoot as ShadowRoot).querySelector<HTMLElement>('.surface')?.dataset.presentationMode,
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
      const canvas = (dashboard.shadowRoot as ShadowRoot).querySelector('lv-report-canvas') as any
      await canvas.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))
      await canvas.updateComplete
      const hosts = Array.from((dashboard.shadowRoot as ShadowRoot).querySelectorAll('lv-visualization-host'))
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
        scale: (canvas.shadowRoot as ShadowRoot).querySelector<HTMLElement>('.surface')?.dataset.scale,
        inverseScale: getComputedStyle((canvas.shadowRoot as ShadowRoot).querySelector<HTMLElement>('.surface')!).getPropertyValue('--report-canvas-inverse-scale'),
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

test('scaled report tables preserve action targets and virtual row geometry', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => Boolean([...((document.querySelector('lv-dashboard-page') as any)?.shadowRoot?.querySelectorAll('lv-visualization-host') ?? [])].find((host: any) => host.envelope?.visualID === 'orders')?.shadowRoot?.querySelector('lv-report-table')?.shadowRoot?.querySelector('.table-scrollport')))
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      await dashboard.ensureVisualizationsMounted()
      const canvas = (dashboard.shadowRoot as ShadowRoot).querySelector('lv-report-canvas') as any
      const host = [...(dashboard.shadowRoot as ShadowRoot).querySelectorAll('lv-visualization-host')].find((item: any) => item.envelope?.visualID === 'orders') as any
      const table = (host.shadowRoot as ShadowRoot).querySelector('lv-report-table') as any
      document.dispatchEvent(new CustomEvent('lv-report-zoom-command', { detail: { mode: 'custom', scale: 0.7 } }))
      await canvas.updateComplete
      await table.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))
      await table.updateComplete
      const root = table.shadowRoot as ShadowRoot
      const rows = [...root.querySelectorAll<HTMLElement>('.canvas > .row[role="row"]')]
      const actions = rows.flatMap((row) => [...row.querySelectorAll<HTMLElement>('.cell-action')])
      const rowRects = rows.map((row) => {
        const rect = row.getBoundingClientRect()
        return { top: rect.top, bottom: rect.bottom, height: rect.height }
      })
      const actionRects = actions.slice(0, 12).map((action) => {
        const rect = action.getBoundingClientRect()
        return { top: rect.top, bottom: rect.bottom, height: rect.height, label: action.getAttribute('aria-label') }
      })
      const beforeScroll = rows.slice(0, 4).map((row) => row.textContent?.replace(/\s+/g, ' ').trim())
      const viewport = root.querySelector<HTMLElement>('.table-scrollport')!
      viewport.scrollTop = table.rowHeight * 10
      viewport.dispatchEvent(new Event('scroll'))
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const afterScroll = [...root.querySelectorAll<HTMLElement>('.canvas > .row[role="row"]')].slice(0, 4).map((row) => ({ top: row.getBoundingClientRect().top, text: row.textContent?.replace(/\s+/g, ' ').trim() }))
      const details = root.querySelector('.visual-options') as HTMLDetailsElement
      ;(details.querySelector('summary') as HTMLElement).click()
      await new Promise<void>((resolve) => setTimeout(resolve, 0))
      const menu = details.querySelector('.menu') as HTMLElement
      const menuRect = menu.getBoundingClientRect()
      const menuButtons = [...menu.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
      const menuState = {
        top: menuRect.top,
        bottom: menuRect.bottom,
        scrollable: menu.scrollHeight > menu.clientHeight,
        minimumButtonHeight: Math.min(...menuButtons.map((button) => button.getBoundingClientRect().height)),
        hostOpen: host.hasAttribute('data-visual-menu-open'),
        cardOpen: host.closest('[data-canvas-visual]')?.hasAttribute('data-visual-menu-open') ?? false,
        cardOverflow: getComputedStyle(host.closest('[data-canvas-visual]')!).overflow,
      }
      return {
        scale: Number((canvas.shadowRoot as ShadowRoot).querySelector<HTMLElement>('.surface')?.dataset.scale),
        rowHeight: table.rowHeight,
        authoredRowHeight: table.table.rowHeight,
        rowRects,
        actionRects,
        beforeScroll,
        afterScroll,
        menuState,
      }
    })
    expect(result.scale).toBeCloseTo(0.7, 2)
    expect(result.rowHeight).toBeCloseTo(result.authoredRowHeight / result.scale, 2)
    expect(result.actionRects.length).toBeGreaterThan(0)
    expect(Math.min(...result.actionRects.map((rect: any) => rect.height))).toBeGreaterThanOrEqual(24)
    for (const rect of result.actionRects) {
      const row = result.rowRects.find((candidate: any) => rect.top >= candidate.top - 1 && rect.bottom <= candidate.bottom + 1)
      expect(row).toBeDefined()
    }
    expect(result.afterScroll.length).toBeGreaterThan(0)
    expect(result.afterScroll.map((row: any) => row.text)).toContain('o11')
    expect(result.menuState.top).toBeGreaterThanOrEqual(0)
    expect(result.menuState.bottom).toBeLessThanOrEqual(820)
    expect(result.menuState.minimumButtonHeight).toBeGreaterThanOrEqual(24)
    expect(result.menuState.hostOpen).toBe(true)
    expect(result.menuState.cardOpen).toBe(true)
    expect(result.menuState.cardOverflow).toBe('visible')
  } finally { await page.close() }
})

test('short desktop visual cards keep an open action menu visible and interactive', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => (document.querySelector('lv-dashboard-page') as any)?.page?.title === 'Executive Sales Dashboard')
    const dashboard = page.locator('lv-dashboard-page')
    const target = await dashboard.evaluate(async (element: any) => {
      await element.ensureVisualizationsMounted()
      const host = [...(element.shadowRoot as ShadowRoot).querySelectorAll('lv-visualization-host')]
        .find((candidate: any) => candidate.envelope?.visualID === 'orders_kpi') as any
      const card = host.closest('[data-canvas-visual]') as HTMLElement
      const details = host.shadowRoot.querySelector('.visual-options') as HTMLDetailsElement
      const summary = details.querySelector('summary') as HTMLElement
      let action = ''
      host.addEventListener('lv-visual-action', (event: CustomEvent) => { action = event.detail.action })
      summary.click()
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const button = details.querySelectorAll<HTMLElement>('[role="menuitem"]')[2]!
      const buttonRect = button.getBoundingClientRect()
      const cardRect = card.getBoundingClientRect()
      const frame = card.shadowRoot?.querySelector('.frame') as HTMLElement
      const sibling = (element.shadowRoot as ShadowRoot).querySelector('[data-visual-id="orders_chart"]') as HTMLElement
      ;(element as any).__shortCardMenuAction = () => action
      return {
        point: { x: buttonRect.left + buttonRect.width / 2, y: buttonRect.top + buttonRect.height / 2 },
        cardBottom: cardRect.bottom,
        menuBottom: Math.max(...Array.from(details.querySelectorAll('[role="menuitem"]')).map((item) => (item as HTMLElement).getBoundingClientRect().bottom)),
        cardOverflow: getComputedStyle(card).overflow,
        frameOverflow: frame ? getComputedStyle(frame).overflow : '',
        cardZIndex: getComputedStyle(card).zIndex,
        siblingTop: sibling.getBoundingClientRect().top,
        open: details.open,
        hostMenuOpen: host.hasAttribute('data-visual-menu-open'),
        cardTag: card.localName,
        cardMenuOpen: card.hasAttribute('data-visual-menu-open'),
      }
    })
    expect(target).toMatchObject({ hostMenuOpen: true, cardTag: 'lv-dashboard-visual-frame' })
    await page.mouse.click(target.point.x, target.point.y)
    const action = await dashboard.evaluate((element: any) => element.__shortCardMenuAction?.())
    expect(target.open).toBe(true)
    expect(target.cardMenuOpen).toBe(true)
    expect(target.menuBottom).toBeGreaterThan(target.cardBottom)
    expect(target.cardOverflow).toBe('visible')
    expect(target.frameOverflow).toBe('visible')
    expect(Number(target.cardZIndex)).toBeGreaterThan(0)
    expect(target.point.y).toBeGreaterThanOrEqual(target.siblingTop)
    expect(action).toBe('export-csv')
  } finally { await page.close() }
})

for (const start of [50, 950]) {
  test(`table scrolling loads missing rows at ${start} and completes at the browse boundary`, async () => {
    const page = await browser.newPage()
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-report-table'))
      const result = await page.evaluate(async (start) => {
        const table = document.createElement('lv-report-table') as any
        table.style.cssText = 'display:block;width:500px;height:320px;'
        const sort = { key: 'id', direction: 'asc' }
        const block = (start: number, count: number, requestSeq = 0) => ({ start, requestSeq, resetVersion: 1, sort, rows: Array.from({ length: count }, (_, i) => ({ id: String(start + i) })) })
        table.tableId = 'orders'
        table.table = { ...table.table, columns: [{ key: 'id', label: 'ID' }], sort, resetVersion: 1, cardinality: { kind: 'exact', value: 2000 }, availableRows: 1000, rowCap: 100, isCapped: true, chunkSize: 50, rowHeight: 32, blocks: { a: block(0, 50), b: block(50, 0), c: block(100, 0) } }
        document.body.prepend(table)
        await table.updateComplete
        await new Promise(resolve => requestAnimationFrame(resolve))
        const requests: any[] = []
        table.addEventListener('lv-visual-window-change', (event: CustomEvent) => {
          requests.push(event.detail)
          const request = event.detail
          const starts = request.start ? [request.start - 50, request.start, request.start + 50] : [0, 50, 100]
          const blocks = Object.fromEntries(['a', 'b', 'c'].flatMap((id, i) => starts[i]! >= 1000 ? [] : [[id, block(starts[i]!, Math.min(50, 1000 - starts[i]!), request.requestSeq)]]))
          table.table = { ...table.table, blocks }
        })
        const viewport = table.shadowRoot.querySelector('.table-scrollport') as HTMLElement
        viewport.scrollTop = start * 32
        viewport.dispatchEvent(new Event('scroll'))
        await new Promise(resolve => setTimeout(resolve, 200))
        await table.updateComplete
        return { footer: table.shadowRoot.querySelector('.footer').textContent, requests: requests.length, loading: table.visibleLoading, pending: table.expectedBlocks.size, skeletons: table.visibleRows.filter((row: any) => row.kind === 'skeleton').length }
      }, start)
      expect(result.requests).toBeGreaterThan(0)
      expect(result.loading).toBe(false)
      expect(result.pending).toBe(0)
      expect(result.footer).toContain('browsing first 1,000')
      expect(result.skeletons).toBe(0)
    } finally { await page.close() }
  })
}

test('phone headers keep page actions below the title and default table values readable', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => Boolean(document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-visualization-host')?.shadowRoot))
    await page.locator('lv-report-table .table-scrollport').first().waitFor()
    const result = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const root = dashboard.shadowRoot as ShadowRoot
      const title = root.querySelector('.dashboard-heading')!.getBoundingClientRect()
      const actions = root.querySelector('.actions')!.getBoundingClientRect()
      const host = [...root.querySelectorAll('lv-visualization-host')].find((h: any) => h.envelope?.visualID === 'orders') as any
      const table = host.shadowRoot.querySelector('lv-report-table') as any
      table.table = { ...table.table, columns: table.table.columns.map((column: any) => ({ ...column, width: undefined })) }
      await table.updateComplete
      // Allow ResizeObserver to measure the mounted narrow scrollport.
      await new Promise(resolve => setTimeout(resolve, 100))
      return { actionsBelowTitle: actions.top >= title.bottom, defaultWidths: table.columns.map((column: any) => table.columnPixelWidth(column)) }
    })
    expect(result.actionsBelowTitle).toBe(true)
    expect(result.defaultWidths.length).toBeGreaterThan(0)
    expect(Math.min(...result.defaultWidths)).toBeGreaterThanOrEqual(168)
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ page: { pages: ['overview', 'statement', 'liquidity', 'drivers'].map((id, index) => ({
        id, title: id, href: `/dashboards/executive-sales/pages/${id}`, active: index === 0,
      })) } })
    })
    await page.locator('.mobile-page-menu summary').click()
    // Exercise hit testing: chart/table stacking must not intercept the last option.
    await page.locator('.mobile-page-menu a').last().click({ trial: true })
  } finally { await page.close() }
})

test('table data updates preserve desktop canvas positions', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 940 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => Boolean(document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('[data-visual-id="orders"]')?.getAttribute('style')?.includes('left:')))
    const results = await page.locator('lv-dashboard-page').evaluate(async (dashboard: any) => {
      const root = dashboard.shadowRoot as ShadowRoot
      const frame = root.querySelector('[data-visual-id="orders"]') as HTMLElement
      const geometry = () => ['left', 'top', 'width', 'height'].map(key => frame.style.getPropertyValue(key))
      const before = geometry()
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      const envelope = structuredClone(dashboard.visuals.orders)
      envelope.dataRevision++
      envelope.dataState.availableRows = 2
      envelope.dataState.cardinality = { kind: 'exact', count: 2 }
      envelope.dataState.blocks.a.rows = envelope.dataState.blocks.a.rows.slice(0, 2)
      envelope.dataState.dataRevision = envelope.dataRevision
      mergePatch({ visuals: { orders: { dataRevision: envelope.dataRevision, dataState: {
        dataRevision: envelope.dataRevision, payload: JSON.stringify(envelope.dataState),
      } } } })
      await dashboard.updateComplete
      return { before, after: geometry(), mobileHeight: frame.style.getPropertyValue('--lv-mobile-table-height') }
    })
    expect(results.before.every(Boolean)).toBe(true)
    expect(results.after).toEqual(results.before)
    expect(results.mobileHeight).not.toBe('')
  } finally { await page.close() }
})
