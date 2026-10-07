import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { builderTestDocument as testDocument } from './dashboard-builder-test-fixtures'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/dashboard-builder-test')
const documentWithProductFonts = (agentReady = false) => testDocument({ agentReady })
  .replaceAll('system-ui', '"Inter Variable", Inter, system-ui')
  .replace('<head>', '<head><style>@font-face{font-family:"Inter Variable";src:url("/static/files/inter-latin-wght-normal.woff2") format("woff2");font-weight:100 900;}</style>')

async function withTimeout<T>(operation: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      operation,
      new Promise<T>((_, reject) => {
        timer = setTimeout(() => reject(new Error(message)), timeoutMs)
      }),
    ])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(documentWithProductFonts(url.searchParams.has('agent-ready')))
      return
    }
    const fileRoot = url.pathname.startsWith('/static/') ? projectRoot : root
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
  if (!address || typeof address === 'string') throw new Error('dashboard builder test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

async function measureInspector(width: number) {
  const page = await browser.newPage({ viewport: { width, height: 1000 } })
  try {
    page.setDefaultTimeout(6_000)
    page.setDefaultNavigationTimeout(6_000)
    await page.goto(baseURL, { waitUntil: 'domcontentloaded' })
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'), null, { timeout: 6_000 })
    await withTimeout(page.evaluate(() => document.fonts.ready), 6_000, 'product fonts did not settle')
    return await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const awaitUpdate = async () => {
        let timer: number | undefined
        try {
          await Promise.race([
            element.updateComplete,
            new Promise((_, reject) => { timer = window.setTimeout(() => reject(new Error('dashboard builder update did not settle')), 6_000) }),
          ])
        } finally {
          if (timer !== undefined) window.clearTimeout(timer)
        }
      }
      await awaitUpdate()
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const pages = structuredClone(element.builder.pages)
      const visual = pages[0].visuals[0]
      visual.type = 'combo'
      visual.title = 'Actual, budget, and forecast revenue'
      visual.slots = [
        { id: 'dimension-0', label: 'Finance month', kind: 'dimension', fieldId: 'finance_month', alias: 'Finance month' },
        { id: 'metric-0', label: 'Net revenue', kind: 'metric', fieldId: 'net_revenue', alias: 'Net revenue' },
        { id: 'metric-1', label: 'Budget revenue', kind: 'metric', fieldId: 'budget_revenue', alias: 'Budget revenue' },
        { id: 'metric-2', label: 'Forecast revenue', kind: 'metric', fieldId: 'forecast_revenue', alias: 'Forecast revenue' },
      ]
      visual.queryOptions = { supportsSort: true, supportsLimit: true, sort: [{ field: 'Finance month', direction: 'asc' }], limit: 24 }
      visual.formatOptions = [
        { key: 'axisVisible', label: 'Show axes', section: 'Display', control: 'toggle', value: 'true', choices: [] },
        { key: 'legend', label: 'Legend', section: 'Display', control: 'select', value: 'bottom', choices: [{ value: 'none', label: 'None' }, { value: 'top', label: 'Top' }, { value: 'right', label: 'Right' }, { value: 'bottom', label: 'Bottom' }, { value: 'left', label: 'Left' }] },
        { key: 'labels.density', label: 'Data labels', section: 'Display', control: 'select', value: 'hidden', choices: [{ value: 'hidden', label: 'Hidden' }, { value: 'automatic', label: 'Automatic' }, { value: 'dense', label: 'Dense' }, { value: 'always', label: 'Always' }] },
        { key: 'labels.maxCharacters', label: 'Maximum label characters', section: 'Labels', control: 'number', value: '24', choices: [] },
        { key: 'labels.minimumSpacing', label: 'Minimum label spacing', section: 'Labels', control: 'number', value: '0', choices: [] },
        { key: 'labels.tooltipFallback', label: 'Tooltip for truncated labels', section: 'Labels', control: 'toggle', value: 'true', choices: [] },
        { key: 'stacking', label: 'Stacking', section: 'Chart', control: 'select', value: 'none', choices: [{ value: 'none', label: 'None' }, { value: 'normal', label: 'Normal' }, { value: 'percent', label: 'Percent' }] },
        { key: 'orientation', label: 'Orientation', section: 'Chart', control: 'select', value: '', choices: [{ value: 'horizontal', label: 'Horizontal' }, { value: 'vertical', label: 'Vertical' }] },
        { key: 'showSymbols', label: 'Show symbols', section: 'Chart', control: 'toggle', value: 'false', choices: [] },
        { key: 'smooth', label: 'Smooth lines', section: 'Chart', control: 'toggle', value: 'false', choices: [] },
        { key: 'step', label: 'Stepped lines', section: 'Chart', control: 'toggle', value: 'false', choices: [] },
        { key: 'dataZoom', label: 'Data zoom', section: 'Interaction', control: 'toggle', value: 'false', choices: [] },
        { key: 'symbolSize', label: 'Symbol size', section: 'Chart', control: 'number', value: '', choices: [] },
        { key: 'labelPosition', label: 'Label position', section: 'Display', control: 'select', value: 'automatic', choices: [{ value: 'automatic', label: 'Automatic' }, { value: 'inside', label: 'Inside' }, { value: 'outside', label: 'Outside' }, { value: 'top', label: 'Top' }] },
        { key: 'displayUnits', label: 'Display units', section: 'Values', control: 'select', value: 'auto', choices: [{ value: 'auto', label: 'Auto' }, { value: 'none', label: 'None' }, { value: 'thousands', label: 'Thousands' }, { value: 'millions', label: 'Millions' }, { value: 'billions', label: 'Billions' }, { value: 'trillions', label: 'Trillions' }] },
      ]
      mergePatch({ builder: {
        semanticModel: { id: 'commerce', title: 'Orders', datasets: [{ id: 'orders', title: 'Orders', fields: [
          { id: 'finance_month', label: 'Finance month', kind: 'dimension', dataType: 'string' },
          { id: 'net_revenue', label: 'Net revenue', kind: 'metric', dataType: 'decimal' },
          { id: 'budget_revenue', label: 'Budget revenue', kind: 'metric', dataType: 'decimal' },
          { id: 'forecast_revenue', label: 'Forecast revenue', kind: 'metric', dataType: 'decimal' },
        ] }] },
        pages,
      } })
      await awaitUpdate()
      const root = element.shadowRoot as ShadowRoot
      const visualPane = root.querySelector('.visual-builder') as HTMLElement
      const visualRight = visualPane.getBoundingClientRect().left + visualPane.clientWidth
      const controls = Array.from(root.querySelectorAll<HTMLElement>(
        '.visual-reference-link, .visual-picker-button, [data-query-control], .field-token',
      ))
      return {
        viewportWidth: innerWidth,
        visualClientWidth: visualPane.clientWidth,
        visualScrollWidth: visualPane.scrollWidth,
        referenceCount: root.querySelectorAll('.visual-reference-link').length,
        pickerButtonCount: root.querySelectorAll('.visual-picker-button').length,
        queryControlCount: root.querySelectorAll('[data-query-control]').length,
        controlsFit: controls.every((node) => node.getBoundingClientRect().right <= visualRight + 1),
      }
    })
  } finally {
    await page.close()
  }
}

test('visual inspector controls stay within the pane at desktop and narrow widths', async () => {
  const desktop = await measureInspector(1440)
  const narrow = await measureInspector(700)
  for (const layout of [desktop, narrow]) {
    expect(layout.visualScrollWidth).toBe(layout.visualClientWidth)
    expect(layout.controlsFit).toBe(true)
    expect(layout.referenceCount).toBe(2)
    expect(layout.pickerButtonCount).toBe(27)
    expect(layout.queryControlCount).toBeGreaterThan(3)
  }
}, 15_000)

test('builder agent uses the compact main-agent welcome layout and starter prompts only fill the composer', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 650 } })
  try {
    await page.addInitScript(() => {
      ;(window as any).agentSubmits = 0
      document.addEventListener('lv-chat-submit', () => (window as any).agentSubmits++)
    })
    await page.goto(`${baseURL}/?agent-ready=1`)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const builder = page.locator('lv-dashboard-builder')
    await builder.locator('[data-pane-toggle="agent"]').click()
    const drawer = builder.locator('lv-chat-drawer[open]')
    expect(await drawer.getByRole('heading', { name: 'What should I change?' }).isVisible()).toBe(true)
    for (const label of [/Add a chart:/, /Change a visual:/, /Move a chart:/, /Resize a chart:/]) {
      expect(await drawer.getByRole('button', { name: label }).isVisible()).toBe(true)
    }
    expect(await drawer.getByText('Type @ to attach a chart on this page.').isVisible()).toBe(true)
    const geometry = await drawer.evaluate((element) => {
      const root = element.shadowRoot!
      const welcome = root.querySelector('.welcome')!
      const composer = root.querySelector('lv-chat-composer')!
      const prompts = root.querySelector('.prompts')!
      return {
        composerBeforePrompts: composer.getBoundingClientRect().bottom <= prompts.getBoundingClientRect().top,
        composerInsideWelcome: welcome.contains(composer),
        compactPrompts: [...prompts.querySelectorAll('button')].every((button) => button.getBoundingClientRect().height <= 40),
        noOverflow: root.querySelector('.drawer')!.scrollWidth <= root.querySelector('.drawer')!.clientWidth,
      }
    })
    expect(geometry).toEqual({ composerBeforePrompts: true, composerInsideWelcome: true, compactPrompts: true, noOverflow: true })
    await drawer.getByRole('button', { name: /Resize a chart:/ }).click()
    expect(await drawer.locator('textarea').inputValue()).toBe('Resize this chart to make it taller.')
    expect(await page.evaluate(() => (window as any).agentSubmits)).toBe(0)
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { status: { enabled: true, running: true } } })
    })
    const activeLayout = await drawer.evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const body = root.querySelector('.drawer')!.getBoundingClientRect()
      const composer = root.querySelector('lv-chat-composer')!.getBoundingClientRect()
      return { welcomeHidden: !root.querySelector('.welcome'), composerAtBottom: Math.abs(composer.bottom - body.bottom) < 1 }
    })
    expect(activeLayout).toEqual({ welcomeHidden: true, composerAtBottom: true })
  } finally { await page.close() }
})

test('unrelated builder clicks neither open nor refocus the Agent', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.addInitScript(() => {
      ;(window as any).unrelatedAgentSubmits = 0
      document.addEventListener('lv-chat-submit', () => (window as any).unrelatedAgentSubmits++)
    })
    await page.goto(`${baseURL}/?agent-ready=1`, { waitUntil: 'domcontentloaded' })
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const builder = page.locator('lv-dashboard-builder')
    const agentToggle = builder.locator('[data-pane-toggle="agent"]')
    const search = builder.getByRole('searchbox', { name: 'Search fields' })
    await search.click()
    expect(await builder.locator('.agent-pane').getAttribute('data-collapsed')).toBe('true')
    await agentToggle.click()
    const composer = builder.locator('lv-chat-drawer[open]').locator('lv-chat-composer')
    await composer.locator('textarea').fill('Do not send this question')
    await search.click()
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ builder: { title: 'Updated after another click' } })
    })
    const state = await builder.evaluate(async (element: any) => {
      await element.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const root = element.shadowRoot as ShadowRoot
      const search = root.querySelector('.data-pane input.search')
      const drawer = root.querySelector('lv-chat-drawer') as any
      return {
        agentOpen: drawer.open,
        agentFocus: drawer.shadowRoot?.activeElement?.tagName ?? null,
        searchFocused: root.activeElement === search,
        submits: (window as any).unrelatedAgentSubmits,
      }
    })
    expect(state).toEqual({ agentOpen: true, agentFocus: null, searchFocused: true, submits: 0 })
  } finally { await page.close() }
})

test('builder mutations do not set the Agent turn indicator, while chat requests do', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.evaluate(() => {
      const builder = document.querySelector('lv-dashboard-builder') as HTMLElement
      const wrapper = document.createElement('div')
      wrapper.id = 'agent-action-wrapper'
      wrapper.setAttribute('data-indicator', 'agentTurnPending')
      wrapper.setAttribute('data-on:lv-chat-submit', "@post('/agent-turn')")
      builder.id = 'builder-command-root'
      builder.setAttribute('data-on:lv-builder-command', "@post('/builder-command')")
      builder.parentElement!.replaceChild(wrapper, builder)
      wrapper.append(builder)

      const monitor = document.createElement('output')
      monitor.id = 'agent-indicator-state'
      monitor.setAttribute('data-text', "$agentTurnPending ? 'true' : 'false'")
      document.body.append(monitor)

      ;(window as any).__lvRequestHolds = []
      ;(window as any).__lvFetchLifecycle = []
      window.fetch = ((input: RequestInfo | URL) => new Promise<Response>((resolve) => {
        ;(window as any).__lvRequestHolds.push({
          url: String(input),
          finish: () => resolve(new Response(null, { status: 204 })),
        })
      })) as typeof window.fetch
      document.addEventListener('datastar-fetch', (event) => {
        const detail = (event as CustomEvent).detail
        ;(window as any).__lvFetchLifecycle.push({ type: detail.type, elementId: detail.el.id })
      })
    })

    const indicator = page.locator('#agent-indicator-state')
    const waitForIndicator = (value: 'true' | 'false') => page.waitForFunction((expected) => document.querySelector('#agent-indicator-state')?.textContent === expected, value)
    await waitForIndicator('false')
    expect(await indicator.textContent()).toBe('false')
    const builder = page.locator('#builder-command-root')
    await builder.evaluate((element) => element.dispatchEvent(new CustomEvent('lv-builder-command', { bubbles: true, composed: true })))
    await page.waitForFunction(() => (window as any).__lvRequestHolds.length === 1)
    await waitForIndicator('false')
    expect(await indicator.textContent()).toBe('false')
    await page.evaluate(() => (window as any).__lvRequestHolds[0].finish())
    await page.waitForFunction(() => (window as any).__lvFetchLifecycle.some((event: any) => event.type === 'finished' && event.elementId === 'builder-command-root'))

    await builder.evaluate((element) => element.dispatchEvent(new CustomEvent('lv-chat-submit', { bubbles: true, composed: true })))
    await page.waitForFunction(() => (window as any).__lvRequestHolds.length === 2)
    await waitForIndicator('true')
    expect(await indicator.textContent()).toBe('true')
    await page.evaluate(() => (window as any).__lvRequestHolds[1].finish())
    await page.waitForFunction(() => (window as any).__lvFetchLifecycle.some((event: any) => event.type === 'finished' && event.elementId === 'agent-action-wrapper'))
    await waitForIndicator('false')
    expect(await indicator.textContent()).toBe('false')
  } finally {
    await page.close()
  }
})

test('builder reload preserves the Agent pane preference without submitting a chat on visual edits', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.evaluate(() => localStorage.setItem('leapview-dashboard-builder-collapsed-panes', JSON.stringify({ version: 2, collapsed: [] })))
    await page.reload()
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { transcript: [], status: { enabled: true, running: false }, composer: { value: '', disabled: false } } })
      ;(window as any).unexpectedAgentSubmits = 0
      document.addEventListener('lv-chat-submit', () => (window as any).unexpectedAgentSubmits++)
    })
    await page.locator('lv-dashboard-builder').locator('button[data-visual-picker-type="line"]').click()
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ builder: { title: 'Visual type saved' }, agentContext: { pageTitle: 'Overview' } })
    })
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      return {
        agentCollapsed: root.querySelector('.agent-pane')?.getAttribute('data-collapsed'),
        agentDrawerOpen: (root.querySelector('lv-chat-drawer') as any).open,
        filtersCollapsed: root.querySelector('.filters-pane')?.getAttribute('data-collapsed'),
        agentSubmits: (window as any).unexpectedAgentSubmits,
      }
    })
    expect(state).toEqual({ agentCollapsed: 'false', agentDrawerOpen: true, filtersCollapsed: 'false', agentSubmits: 0 })
  } finally { await page.close() }
})

test('dashboard builder stacks visual tiles within the mobile canvas viewport', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const visual = (id: string, title: string, row: number, type = 'bar') => ({
        id, title, type, placement: { col: 1, row, colSpan: 4, rowSpan: 4 },
        slots: [], filters: [],
      })
      mergePatch({
        builder: {
          pages: [{
            id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 },
        // Stream order is intentionally different from authored canvas order.
            visuals: [visual('three', 'Three', 11, 'table'), visual('one', 'One', 1, 'kpi'), visual('two', 'Two', 6)],
          }],
          selectedPageId: 'overview', selectedVisualId: 'one',
        },
      })
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)
      const canvas = root.querySelector('.canvas') as HTMLElement
      const scroll = root.querySelector('.canvas-scroll') as HTMLElement
      const canvasBox = canvas.getBoundingClientRect()
      const visuals = Array.from(root.querySelectorAll('.visual')).map((node) => {
        const box = (node as HTMLElement).getBoundingClientRect()
        const contentBox = (node.querySelector('.grid-stack-item-content') as HTMLElement).getBoundingClientRect()
        const tile = node as HTMLElement
        const style = getComputedStyle(tile)
        return { title: tile.querySelector('.visual-drag-header')?.textContent?.trim(), left: box.left, right: box.right, top: box.top, bottom: box.bottom, width: box.width, height: box.height, contentWidth: contentBox.width, contentHeight: contentBox.height, type: tile.getAttribute('data-visual-type'), position: style.position, order: style.order, topOffset: style.top, leftOffset: style.left, authoredTop: tile.style.top }
      })
      return {
        canvasWidth: canvasBox.width,
        canvasHeight: canvasBox.height,
        scrollHeight: scroll.scrollHeight,
        scrollWidth: scroll.scrollWidth,
        scrollClientWidth: scroll.clientWidth,
        visuals,
        documentHorizontalOverflow: document.documentElement.scrollWidth > innerWidth || document.body.scrollWidth > innerWidth,
      }
    })
    expect(state.canvasWidth).toBeGreaterThan(0)
    expect(state.canvasHeight).toBeLessThan(1000)
    expect(state.scrollHeight).toBeLessThan(1200)
    expect(state.scrollWidth).toBeLessThanOrEqual(state.scrollClientWidth)
    expect(state.documentHorizontalOverflow).toBe(false)
    expect(state.visuals).toHaveLength(3)
    expect(state.visuals.map((visual) => visual.title)).toEqual(['Three', 'One', 'Two'])
    expect(state.visuals.map((visual) => visual.order)).toEqual(['2', '0', '1'])
    const kpi = state.visuals.find((visual) => visual.type === 'kpi')
    const chart = state.visuals.find((visual) => visual.type === 'bar')
    const table = state.visuals.find((visual) => visual.type === 'table')
    expect(kpi?.height).toBeLessThan(chart?.height ?? 0)
    expect(table?.height).toBeLessThanOrEqual(256)
    const flow = [...state.visuals].sort((left, right) => Number(left.order) - Number(right.order))
    for (const visual of state.visuals) {
      expect(visual.position).toBe('relative')
      // Relative flow resolves auto offsets to 0px; authored top/left values
      // remain on the inline style but no longer offset the mobile tile.
      expect(visual.topOffset).toBe('0px')
      expect(visual.leftOffset).toBe('0px')
      expect(visual.left).toBeGreaterThanOrEqual(-1)
      expect(visual.right).toBeLessThanOrEqual(state.canvasWidth + 1)
      expect(visual.bottom).toBeGreaterThan(visual.top)
      expect(visual.contentWidth).toBeCloseTo(visual.width, 0)
      expect(visual.contentHeight).toBeCloseTo(visual.height, 0)
    }
    expect(flow[1].authoredTop).not.toBe('0px')
    expect(flow[2].authoredTop).not.toBe('0px')
    expect(flow[1].top).toBeGreaterThan(flow[0].bottom)
    expect(flow[2].top).toBeGreaterThan(flow[1].bottom)
  } finally {
    await page.close()
  }
})
