import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import validateVisualizationEnvelope from '../../generated/visualization/validate'

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
        expect(state.presentationMode).toBe('responsive')
        expect(state.chartHeight).toBeGreaterThanOrEqual(280)
        expect(state.tableHeight).toBeLessThanOrEqual(700)
        expect(state.tableAfterChart).toBe(true)
      } else {
        expect(state.presentationMode).toBe('fit-width')
      }
    } finally { await page.close() }
  })
}

test('embed presentation keeps page navigation and removes non-navigation chrome', async () => {
  const page = await browser.newPage({ viewport: { width: 760, height: 620 } })
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
    expect(state.agentActionCount).toBe(0)
    expect(state.canvasWidth).toBeGreaterThan(500)
    expect(state.documentOverflow).toBe(0)
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
      const before = shell.style.getPropertyValue('--lv-table-columns')
      handle.focus()
      handle.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }))
      await table.updateComplete
      return {
        label: handle.getAttribute('aria-label'),
        role: handle.getAttribute('role'),
        tabIndex: handle.tabIndex,
        changed: shell.style.getPropertyValue('--lv-table-columns') !== before,
      }
    })
    expect(result).toMatchObject({ role: 'separator', tabIndex: 0, changed: true })
    expect(result.label).toMatch(/^Resize .+ column$/)
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
        mappings: [{ field: 'orders.status', fact: 'orders', value: 'delivered', label: 'Delivered' }],
      }
      source.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: command }))
      const optimistic = await readSelection()

      mergePatch({
        interactionSelections: [{
          sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection',
          entries: [{ label: 'Delivered', mappings: [{ field: 'orders.status', fact: 'orders', value: 'delivered' }] }],
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
        mappings: [{ field: 'orders.status', fact: 'orders', value: 'delivered', label: 'Delivered' }],
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
		  { reference: { workspaceId: 'sales', type: 'visual', id: 'executive-sales.orders_chart' }, name: 'Orders by status', workspace: { id: 'sales', name: 'Sales' }, hierarchy: ['Sales', 'Executive Sales', 'Overview'], href: '/orders', locations: [{ dashboardId: 'executive-sales', pageId: 'overview', href: '/orders' }], context: ['current_page'] },
		  { reference: { workspaceId: 'finance', type: 'visual', id: 'executive-sales.foreign_orders' }, name: 'Finance orders', description: 'From another workspace', workspace: { id: 'finance', name: 'Finance' }, hierarchy: ['Finance', 'Executive Sales', 'Overview'], href: '/finance', locations: [{ dashboardId: 'executive-sales', pageId: 'overview', href: '/finance' }], context: [] },
		  { reference: { workspaceId: 'sales', type: 'measure', id: 'olist.order_count' }, name: 'Orders count', description: 'Across the sales workspace', workspace: { id: 'sales', name: 'Sales' }, hierarchy: ['Sales', 'Olist'], href: '/measure', locations: [], context: ['current_workspace'] },
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
	expect(groupedSearch.onPage).not.toContain('Finance orders Finance › Executive Sales › Overview Visual')
	expect(groupedSearch.accessible).toContain('Finance orders Finance › Executive Sales › Overview Visual')
	expect(groupedSearch.options.at(-1)).toBe('Orders count Sales › Olist Measure')

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
        reference: { workspaceId: 'sales', type: 'visual', id: 'executive-sales.orders_chart' },
        name: 'Orders by status',
        visualType: 'bar',
        workspace: { id: 'sales', name: 'sales' },
        hierarchy: ['sales', 'Executive Sales Dashboard', 'Overview'],
        href: '/workspaces/sales/dashboards/executive-sales/pages/overview',
        locations: [{ dashboardId: 'executive-sales', dashboardName: 'Executive Sales Dashboard', pageId: 'overview', pageName: 'Overview', href: '/workspaces/sales/dashboards/executive-sales/pages/overview' }],
        context: ['current_page', 'current_dashboard', 'current_workspace'],
      }],
    })

	const accepted = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
	  const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
	  mergePatch({ agent: {
		activeConversationId: 'agentconv_1',
		transcript: [{
		  id: 'user_1', kind: 'user', runId: 'run_1', text: 'Why did this decline?',
		  references: [{
			reference: { workspaceId: 'sales', type: 'visual', id: 'executive-sales.orders_chart' },
			name: 'Orders by status', workspace: { id: 'sales', name: 'Sales' },
			hierarchy: ['Sales', 'Executive Sales Dashboard', 'Overview'],
			href: '/workspaces/sales/dashboards/executive-sales/pages/overview', locations: [], context: ['current_page'],
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
      return {
        pageSidebar: Math.round(pageSidebar.getBoundingClientRect().width),
        filters: Math.round(filterDock.shadowRoot.querySelector('aside').getBoundingClientRect().width),
      }
    })

    expect(widths.pageSidebar).toBeGreaterThan(0)
    expect(widths.filters).toBe(widths.pageSidebar)
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
      await dock.updateComplete
      const before = canvas.getBoundingClientRect()
      ;(dock.shadowRoot.querySelector('.rail') as HTMLButtonElement).click()
      await dock.updateComplete
      await new Promise(requestAnimationFrame)
      await Promise.all(dock.getAnimations().map((animation: Animation) => animation.finished))
      const after = canvas.getBoundingClientRect()
      const pane = dock.getBoundingClientRect()
      return {
        beforeWidth: Math.round(before.width),
        afterWidth: Math.round(after.width),
        canvasRight: Math.round(after.right),
        paneLeft: Math.round(pane.left),
        paneWidth: Math.round(pane.width),
        expanded: dock.hasAttribute('data-open'),
      }
    })

    expect(result.expanded).toBe(true)
    expect(result.paneWidth).toBeGreaterThan(240)
    expect(result.afterWidth).toBeLessThan(result.beforeWidth)
    expect(result.canvasRight).toBeLessThanOrEqual(result.paneLeft)
  } finally {
    await page.close()
  }
})

test('mobile filter dock is reachable before the canvas and opens with pointer activation', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.addInitScript(() => localStorage.setItem('leapview:filters-open', 'closed'))
    await page.goto(baseURL)
    const dashboard = page.locator('lv-dashboard-page')
    await dashboard.waitFor()
    const positions = await dashboard.evaluate(async (element: any) => {
      await element.updateComplete
      const dock = element.shadowRoot.querySelector('lv-filter-dock') as any
      await dock.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({
        filterValidation: {
          accepted: false,
          message: 'range lower bound exceeds upper bound',
          currentRevision: 0,
          clientMutationID: 'mobile-validation',
        },
      })
      await element.updateComplete
      const alert = element.shadowRoot.querySelector('[role="alert"]')
      return {
        dockTop: dock.getBoundingClientRect().top,
        canvasTop: element.shadowRoot.querySelector('.canvas-wrap').getBoundingClientRect().top,
        dockBottom: dock.getBoundingClientRect().bottom,
        alertTop: alert.getBoundingClientRect().top,
        dockBeforeCanvas: Boolean(
          dock.compareDocumentPosition(element.shadowRoot.querySelector('.canvas-wrap'))
          & Node.DOCUMENT_POSITION_FOLLOWING
        ),
      }
    })
    expect(positions.dockTop).toBeLessThan(positions.canvasTop)
    expect(positions.dockBottom).toBeLessThanOrEqual(positions.alertTop)
    expect(positions.dockBeforeCanvas).toBe(true)

    const toggle = page.locator('lv-filter-dock button.rail')
    await toggle.click()
    const opened = await dashboard.evaluate(async (element: any) => {
      const dock = element.shadowRoot.querySelector('lv-filter-dock') as any
      await dock.updateComplete
      const panel = dock.shadowRoot.querySelector('.panel')
      const background = element.shadowRoot.querySelector('.agent-toggle')
      background.focus()
      return {
        expanded: dock.shadowRoot.querySelector('button.rail').getAttribute('aria-expanded'),
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
        focusedClass: active?.className,
        backgroundFocused: element.shadowRoot.activeElement?.classList.contains('agent-toggle') ?? false,
      }
    })
    expect(wrappedFocus.insideDrawer).toBe(true)
    expect(wrappedFocus.focusedClass).toContain('close-button')
    expect(wrappedFocus.backgroundFocused).toBe(false)

    await page.keyboard.press('Escape')
    const closed = await dashboard.evaluate(async (element: any) => {
      const dock = element.shadowRoot.querySelector('lv-filter-dock') as any
      await dock.updateComplete
      const focused = dock.shadowRoot.activeElement?.className
      const background = element.shadowRoot.querySelector('.agent-toggle')
      background.focus()
      return {
        expanded: dock.shadowRoot.querySelector('button.rail').getAttribute('aria-expanded'),
        focused,
        backgroundFocusRestored: element.shadowRoot.activeElement === background,
      }
    })
    expect(closed).toEqual({
      expanded: 'false',
      focused: 'rail',
      backgroundFocusRestored: true,
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
        events,
      }
    })
    expect(result.groups).toEqual(['Filters on all pages', 'Filters on this page'])
    expect(result.activeCards).toEqual(['report_state', 'page_state'])
    expect(result.dirtyCards).toEqual(['report_state'])
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
      }
    })
    expect(result).toEqual({
      operator: 'Contains',
      placeholder: 'Enter value',
      rangeLabels: ['Minimum', 'Maximum'],
    })
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

test('filter summaries are explicit while operational status remains visible', async () => {
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
        pendingInHeading: Boolean(status?.closest('.field-heading')),
      }
    })
    expect(result).toEqual({
      idle: { summary: null, status: null },
      explicit: '1 selected',
      pending: 'Updating',
      pendingInHeading: true,
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

test('pane defaults a relative-period definition to the structured shared leaf', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-dock'))
    const controls = await page.evaluate(async () => {
      localStorage.setItem('leapview:filters-open', 'open')
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
      const card = dock.shadowRoot.querySelector('lv-filter-pane-card') as any
      await card.updateComplete
      const leaf = card.shadowRoot.querySelector('lv-filter-leaf') as any
      await leaf.updateComplete
      return {
        textInputs: leaf.shadowRoot.querySelectorAll('input[type="text"]').length,
        direction: Boolean(leaf.shadowRoot.querySelector('select[aria-label="Direction"]')),
        count: Boolean(leaf.shadowRoot.querySelector('input[aria-label="Period count"]')),
        unit: Boolean(leaf.shadowRoot.querySelector('select[aria-label="Period unit"]')),
      }
    })
    expect(controls).toEqual({ textInputs: 0, direction: true, count: true, unit: true })
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

test('opened dynamic dropdowns refresh their option page after stale data becomes current', async () => {
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
      leaf.stale = true
      await leaf.updateComplete
      leaf.stale = false
      await leaf.updateComplete
      return { retained, afterOpen, afterRefresh: seen.length }
    })
    expect(requests).toEqual({
      retained: [
        { label: 'All', selected: false },
        { label: 'AC', selected: true },
      ],
      afterOpen: 1,
      afterRefresh: 2,
    })
  } finally {
    await page.close()
  }
})

test('visible dynamic controls request replacement options when a filter revision invalidates their page', async () => {
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
      leaf.optionRetryDelay = 10
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
      leaf.options = undefined
      await leaf.updateComplete
      await leaf.updateComplete
      await new Promise(resolve => setTimeout(resolve, 30))
      return {
        requests: requests.length,
        status: leaf.shadowRoot.querySelector('.status')?.textContent?.trim(),
      }
    })
    expect(result).toEqual({ requests: 3, status: 'Loading values' })
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
      window.addEventListener('lv-chat-restore', (event: Event) => {
        ;(window as any).__agentRestoreRequests.push((event as CustomEvent).detail)
        // This browser fixture has no dashboard command backend. Keep the test
        // focused on persistence and prevent Datastar from following the
        // synthetic restore command while assertions are running.
        event.stopPropagation()
      }, { capture: true })
    })
    await page.goto(baseURL)
    await page.evaluate(() => {
      localStorage.setItem('leapview-dashboard-agent-state', JSON.stringify({
        open: true,
        conversationId: 'agentconv_saved',
      }))
    })
    await page.reload()
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
      { sourceKind: 'visual', sourceId: 'orders_chart', interactionKind: 'selection', entries: [{ label: 'Delivered', mappings: [{ field: 'orders.status', fact: 'orders', value: 'delivered' }] }] },
      { sourceKind: 'visual', sourceId: 'orders', interactionKind: 'row_selection', entries: [{ label: 'o1', mappings: [{ field: 'orders.order_id', fact: 'orders', value: 'o1' }] }] },
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
        bindingKey: 'fb_state', options: [{ value: { kind: 'string', value: 'SP' }, label: 'SP', selected: false, available: true }],
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
      dashboardId: 'executive-sales', pageId: 'overview', servingStateId: 'serving-test',
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
      workspaceId: 'sales',
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
          body { --fontStack-system: system-ui; --fontStack-monospace: ui-monospace; --text-codeBlock-size: 13px; --base-text-weight-normal: 400; --base-text-weight-medium: 500; --base-text-weight-semibold: 600; --base-text-lineHeight-tight: 1.25; --base-text-lineHeight-snug: 1.375; --base-text-lineHeight-normal: 1.5; --base-text-lineHeight-relaxed: 1.625; --lv-type-caption: 400 12px/1.25 system-ui; --lv-type-secondary: 400 12px/1.625 system-ui; --lv-type-body: 400 14px/1.5 system-ui; --lv-type-body-large: 400 16px/1.5 system-ui; --lv-type-section-title: 600 16px/1.5 system-ui; --lv-type-page-title: 600 20px/1.625 system-ui; --lv-type-title-large: 600 32px/1.5 system-ui; --lv-type-code-block: 400 13px/1.5 ui-monospace; --lv-type-code-inline: 400 0.9285em ui-monospace; --lv-bg-app: #f6f8fa; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-chart-surface: #fff; --lv-report-page-bg: #fff; --lv-report-canvas-bg: #eaeef2; --lv-report-rail-bg: #fff; --lv-bg-overlay: #fff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-line-muted: #d8dee4; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-border-transparent: 1px solid transparent; --lv-radius-default: 6px; --lv-radius-full: 999px; --lv-page-rail-width-collapsed: 38px; --lv-dashboard-filter-open-width: 320px; --lv-dashboard-agent-width: 420px; --base-size-2: 2px; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --base-size-24: 24px; --control-medium-size: 32px; --control-xlarge-size: 40px; --zIndex-dropdown: 100; --zIndex-modal: 200; --zIndex-sticky: 50; --shadow-resting-small: 0 1px 2px rgb(0 0 0 / .08); --shadow-floating-small: 0 8px 24px rgb(0 0 0 / .12); --lv-duration-fast: 160ms; --lv-spinner-size-md: 16px; --lv-spinner-duration: 1800ms; --motion-easing-move: ease; --motion-transition-stateChange: 160ms ease; }
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

function testVisualizationEnvelopes() {
  const kpiRevision = `sha256:${'1'.repeat(64)}`
  const chartRevision = `sha256:${'2'.repeat(64)}`
  const tableRevision = `sha256:${'3'.repeat(64)}`
  const field = (id: string, role: string, dataType: string, label: string) => ({ id, role, dataType, nullable: false, label })
  const base = (title: string, fields: unknown[]) => ({ title, datasets: [{ id: 'primary', fields }], dataBudget: { maxRows: 1000, requiredCompleteness: 'complete' }, accessibility: { title, description: title }, interactions: [] })
  const inline = (revision: string, columns: string[], rows: unknown[][]) => ({ kind: 'inline', specRevision: revision, dataRevision: 1, generation: 3, datasets: [{ id: 'primary', specRevision: revision, dataRevision: 1, generation: 3, columns, rows, completeness: 'complete' }] })
  return {
    orders_kpi: { schemaVersion: 9, visualID: 'orders_kpi', rendererID: 'html', specRevision: kpiRevision, dataRevision: 1, spec: { ...base('Orders', [field('value', 'measure', 'decimal', 'Orders')]), kind: 'kpi', value: { dataset: 'primary', field: 'value' }, presentation: { mode: 'compact', delta: 'absolute', favorableDirection: 'neutral', missingComparison: 'show_unavailable', ranges: [], tone: 'ink', note: 'Filtered' } }, dataState: inline(kpiRevision, ['value'], [[42]]), selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [] },
    orders_chart: { schemaVersion: 9, visualID: 'orders_chart', rendererID: 'echarts', specRevision: chartRevision, dataRevision: 1, spec: { ...base('Orders by status', [field('label', 'identity', 'string', 'Status'), field('value', 'measure', 'decimal', 'Orders')]), kind: 'cartesian', mark: 'bar', interactions: [{ id: 'selection', kind: 'select', mappings: [{ source: { dataset: 'primary', field: 'label' }, targetFieldID: 'orders.status', targetFactID: 'orders' }], targets: [{ visualID: 'orders_kpi', effect: 'highlight' }, { visualID: 'orders', effect: 'filter' }], mode: 'multiple', requiresStableIdentity: true }], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } }, dataState: inline(chartRevision, ['label', 'value'], [['delivered', 42], ['shipped', 7]]), selection: [], highlights: [], status: { kind: 'loading', message: 'Refreshing' }, diagnostics: [] },
    orders: { schemaVersion: 9, visualID: 'orders', rendererID: 'tanstack', specRevision: tableRevision, dataRevision: 1, spec: { ...base('Orders', [field('order_id', 'identity', 'string', 'Order')]), kind: 'table', dataBudget: { maxRows: 1000, requiredCompleteness: 'partial' }, columns: [{ field: { dataset: 'primary', field: 'order_id' }, label: 'Order', width: 180, formatting: [] }], defaultSort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], presentation: { rowHeight: 28, striped: true, showHeader: true } }, dataState: { kind: 'windowed', specRevision: tableRevision, dataRevision: 1, generation: 3, schema: { id: 'primary', fields: [field('order_id', 'identity', 'string', 'Order')] }, cardinality: { kind: 'exact', count: 250 }, availableRows: 250, rowCap: 1000, chunkSize: 50, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], blocks: { a: { id: 'a', start: 0, rows: Array.from({ length: 50 }, (_, index) => [`o${index + 1}`]), requestSeq: 0, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }] } } }, selection: [], highlights: [], status: { kind: 'error', message: 'Ratings query failed' }, diagnostics: [{ code: 'query_failed', severity: 'error', message: 'Ratings query failed' }] },
  }
}

function testVisualizationSignals() {
  return Object.fromEntries(Object.entries(testVisualizationEnvelopes()).map(([id, envelope]) => {
    const { dataState, ...signal } = envelope
    return [id, {
      ...signal,
      servingStateID: 'serving-test',
      streamGeneration: 3,
      filterRevision: 0,
      interactionRevision: 0,
      consumerIdentity: `visual:${id}`,
      dataState: { schemaVersion: 1, encoding: 'json', kind: dataState.kind, specRevision: dataState.specRevision, dataRevision: dataState.dataRevision, generation: dataState.generation, payload: JSON.stringify(dataState) },
    }]
  }))
}

function escapeHTML(value: string): string { return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;') }
