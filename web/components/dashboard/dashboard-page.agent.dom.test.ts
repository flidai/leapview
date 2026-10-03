import { afterAll, beforeAll, expect, setDefaultTimeout, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { mkdir, readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { evaluateAcrossContextTurnover, testDocument, testVisualizationEnvelopes } from './dashboard-page-test-fixtures'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/dashboard-page-test')

setDefaultTimeout(15_000)

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

test('reopening an active agent drawer refocuses the composer and preserves return focus', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-drawer') && customElements.get('lv-chat-composer'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { status: { enabled: true, running: false }, composer: { value: '', disabled: false, placeholder: 'Ask' } } })
    })
    const result = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const trigger = root.querySelector('.agent-toggle') as HTMLButtonElement
      const drawer = root.querySelector('lv-chat-drawer') as any
      trigger.focus()
      drawer.openDrawer()
      await drawer.updateComplete
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
      await composer.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      trigger.focus()
      drawer.openDrawer()
      await drawer.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const composerFocused = composer.shadowRoot.activeElement === composer.shadowRoot.querySelector('textarea')
      drawer.open = false
      await drawer.updateComplete
      return { composerFocused, focusReturned: root.activeElement === trigger }
    })
    expect(result).toEqual({ composerFocused: true, focusReturned: true })
  } finally { await page.close() }
})

test('dashboard agent reads fresh signal state between render cycles', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-drawer'))
    const observed = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      const runtime = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      runtime.mergePatch({ agent: { activeConversationId: 'conversation-one' } })
      await drawer.updateComplete
      runtime.mergePatch({ agent: { activeConversationId: 'conversation-two' } })
      // The signal patch schedules Lit asynchronously. Reads made by event
      // handlers in this gap must not reuse the previous render snapshot.
      return drawer.agent.activeConversationId
    })
    expect(observed).toBe('conversation-two')
  } finally { await page.close() }
})

test('dashboard agent clears draft and references when the active conversation changes', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-drawer') && customElements.get('lv-chat-composer'))
    const state = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const runtime = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      runtime.mergePatch({
        agent: {
          activeConversationId: 'conversation-one',
          status: { enabled: true, running: false },
          composer: { value: '', disabled: false, placeholder: 'Ask' },
        },
      })
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
      drawer.openDrawer()
      await drawer.updateComplete
      drawer.openWithReference({
        reference: { kind: 'visual', id: 'sales.orders' },
        name: 'Orders',
        hierarchy: ['Sales'],
        href: '/dashboards/sales/pages/overview',
        locations: [],
        context: ['current_page'],
      })
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
      composer.setDraft('Keep this draft')
      await composer.updateComplete
      runtime.mergePatch({ agent: { activeConversationId: 'conversation-two' } })
      await drawer.updateComplete
      await composer.updateComplete
      return {
        draft: composer.shadowRoot.querySelector('textarea')?.value,
        references: composer.references.length,
      }
    })
    expect(state).toEqual({ draft: '', references: 0 })
  } finally { await page.close() }
})

test('dashboard agent clears a closed draft without stealing focus on conversation switch', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-drawer') && customElements.get('lv-chat-composer'))
    const state = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const runtime = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      runtime.mergePatch({
        agent: {
          activeConversationId: 'conversation-one',
          status: { enabled: true, running: false },
          composer: { value: '', disabled: false, placeholder: 'Ask' },
        },
      })
      const root = element.shadowRoot as ShadowRoot
      const drawer = root.querySelector('lv-chat-drawer') as any
      const trigger = root.querySelector('.agent-toggle') as HTMLButtonElement
      await drawer.updateComplete
      drawer.openDrawer()
      await drawer.updateComplete
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
      composer.setDraft('Keep this draft')
      await composer.updateComplete
      drawer.open = false
      await drawer.updateComplete
      trigger.focus()

      runtime.mergePatch({ agent: { activeConversationId: 'conversation-two' } })
      await drawer.updateComplete
      await composer.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      return {
        draft: composer.shadowRoot.querySelector('textarea')?.value,
        drawerOpen: drawer.open,
        focusOutsideDrawer: root.activeElement === trigger,
      }
    })
    expect(state).toEqual({ draft: '', drawerOpen: false, focusOutsideDrawer: true })
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
    await page.waitForFunction(() => {
      const element = document.querySelector('lv-dashboard-page') as any
      return Boolean(element?.page && !element.isUpdatePending)
    })
    const moduleHandle = await page.evaluateHandle(() => import('/static/vendor/datastar-1.0.2.js?v=dev'))

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

    await page.waitForFunction(() => {
      const root = document.querySelector('lv-dashboard-page')?.shadowRoot
      return Boolean(
        root?.querySelector('[data-visual-id="orders_chart"] lv-visualization-host')
        && root.querySelector('[data-visual-id="orders_kpi"] lv-visualization-host')
        && root.querySelector('[data-visual-id="orders"] lv-visualization-host')?.shadowRoot?.querySelector('lv-report-table'),
      )
    })

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
      const reportTable = table.shadowRoot.querySelector('lv-report-table') as any
      await reportTable.updateComplete
      const tableExpand = reportTable.shadowRoot.querySelector('.visual-actions .icon-action') as HTMLElement
      const tableOptions = reportTable.shadowRoot.querySelector('.visual-options summary') as HTMLElement
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
        tableAskActionRow: tableAsk.assignedSlot?.assignedSlot?.parentElement?.className,
        tableActionCenters: [tableAsk, tableExpand, tableOptions].map(item => item.getBoundingClientRect().top + item.getBoundingClientRect().height / 2),
        tableAskLeft: tableAsk.getBoundingClientRect().left,
        tableAskRight: tableAsk.getBoundingClientRect().right,
        tableExpandLeft: tableExpand.getBoundingClientRect().left,
        tableExpandRight: tableExpand.getBoundingClientRect().right,
        tableOptionsLeft: tableOptions.getBoundingClientRect().left,
        tableOptionsRight: tableOptions.getBoundingClientRect().right,
        tableRight: reportTable.getBoundingClientRect().right,
        askPressed: ask.getAttribute('aria-pressed'),
        askUsesAgentIcon: ask.querySelector('svg')?.innerHTML === agentIconMarkup
          && drawer.shadowRoot.querySelector('.title svg')?.innerHTML === agentIconMarkup,
        chartAction: expand.getAttribute('aria-label'),
        tableHasExpand: Boolean(table.shadowRoot.querySelector('[data-visualization-expand]')),
        zoomedTableActionHeights: await (async () => {
          const surface = root.querySelector('lv-report-canvas').shadowRoot.querySelector('.surface') as HTMLElement
          surface.style.cssText += `--report-canvas-scale:.47;--report-canvas-inverse-scale:${1 / .47}`
          await new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
          return [tableAsk, tableExpand, tableOptions].map(item => item.getBoundingClientRect().height)
        })(),
      }
    })
    expect(visualActionsAtRest).toMatchObject({
      askOpacity: '0',
      askPointerEvents: 'none',
      askBackground: 'rgba(0, 0, 0, 0)',
      askBoxShadow: 'none',
      askActionRow: 'visual-actions',
      kpiAskActionRow: 'visual-actions',
      tableAskActionRow: 'visual-actions',
      askPressed: 'false',
      askUsesAgentIcon: true,
      chartAction: 'Expand chart',
      tableHasExpand: false,
    })
    expect(Math.max(...visualActionsAtRest.tableActionCenters) - Math.min(...visualActionsAtRest.tableActionCenters)).toBeLessThanOrEqual(1)
    expect(visualActionsAtRest.tableAskRight).toBeLessThanOrEqual(visualActionsAtRest.tableExpandLeft)
    expect(visualActionsAtRest.tableExpandLeft - visualActionsAtRest.tableAskRight).toBeGreaterThanOrEqual(4)
    expect(visualActionsAtRest.tableExpandRight).toBeLessThanOrEqual(visualActionsAtRest.tableOptionsLeft)
    expect(visualActionsAtRest.tableRight - visualActionsAtRest.tableOptionsRight).toBe(8)
    for (const height of visualActionsAtRest.zoomedTableActionHeights) expect(height).toBeGreaterThanOrEqual(31)

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

    await moduleHandle.evaluate((module: any, search: any) => module.mergePatch({ agentReferenceSearch: search }), {
      query: 'orders', requestId: 1,
      results: [
        { reference: { kind: 'visual', id: 'executive-sales.orders_chart' }, name: 'Orders by status', hierarchy: ['Sales', 'Executive Sales', 'Overview'], href: '/orders', locations: [{ dashboardId: 'executive-sales', pageId: 'overview', href: '/orders' }], context: ['current_page'] },
        { reference: { kind: 'visual', id: 'executive-sales.finance_orders' }, name: 'Finance orders', description: 'Finance domain metric', hierarchy: ['Finance', 'Executive Sales', 'Overview'], href: '/finance', locations: [{ dashboardId: 'executive-sales', pageId: 'overview', href: '/finance' }], context: [] },
        { reference: { kind: 'metric', id: 'olist.order_count' }, name: 'Orders count', description: 'Across the sales model', hierarchy: ['Sales', 'Olist'], href: '/metric', locations: [], context: [] },
      ],
    })
    await page.waitForFunction((requestId) => {
      const element = document.querySelector('lv-dashboard-page') as any
      const search = element?.signal('agentReferenceSearch', null)
      return Boolean(element && !element.isUpdatePending && search?.query === 'orders' && search?.requestId === requestId)
    }, 1)
    const groupedSearch = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
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
    expect(groupedSearch.onPage).toContain('Finance orders Finance / Executive Sales / Overview Visual')
    expect(groupedSearch.accessible).not.toContain('Finance orders Finance / Executive Sales / Overview Visual')
    expect(groupedSearch.options.at(-1)).toBe('Orders count Sales / Olist Metric')

    await moduleHandle.evaluate((module: any) => module.mergePatch({ agentContext: { referenceLimit: 1 } }))
    await page.waitForFunction(() => {
      const element = document.querySelector('lv-dashboard-page') as any
      const context = element?.signal('agentContext', null)
      return Boolean(element && !element.isUpdatePending && context?.referenceLimit === 1)
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

    await moduleHandle.evaluate((module: any, agent: any) => module.mergePatch({ agent }), {
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
    })
    await page.waitForFunction((conversationID) => {
      const element = document.querySelector('lv-dashboard-page') as any
      const agent = element?.signal('agent', null)
      return Boolean(element && !element.isUpdatePending && agent?.activeConversationId === conversationID)
    }, 'agentconv_1')
    const accepted = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
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

test('Escape dismisses the agent mention picker before closing the drawer', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-page') && customElements.get('lv-chat-composer'))
    await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const trigger = root.querySelector('.agent-toggle') as HTMLButtonElement
      trigger.focus()
      trigger.click()
      const drawer = root.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
      await composer.updateComplete
    })
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const drawer = dashboard?.shadowRoot?.querySelector('lv-chat-drawer') as any
      const composer = drawer?.shadowRoot?.querySelector('lv-chat-composer') as any
      return composer?.shadowRoot?.activeElement === composer?.shadowRoot?.querySelector('textarea')
    })
    await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
      const textarea = composer.shadowRoot.querySelector('textarea') as HTMLTextAreaElement
      textarea.value = '@'
      textarea.setSelectionRange(textarea.value.length, textarea.value.length)
      textarea.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await composer.updateComplete
    })
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      const drawer = dashboard?.shadowRoot?.querySelector('lv-chat-drawer') as any
      const composer = drawer?.shadowRoot?.querySelector('lv-chat-composer') as any
      return Boolean(composer?.shadowRoot?.querySelector('.mention-picker'))
    })

    await page.keyboard.press('Escape')
    const afterPickerEscape = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer') as any
      await composer.updateComplete
      await new Promise<void>((resolve) => setTimeout(resolve, 0))
      return {
        open: drawer.open,
        pickerVisible: Boolean(composer.shadowRoot.querySelector('.mention-picker')),
        composerFocused: composer.shadowRoot.activeElement === composer.shadowRoot.querySelector('textarea'),
      }
    })
    expect(afterPickerEscape).toEqual({ open: true, pickerVisible: false, composerFocused: true })

    await page.keyboard.press('Escape')
    await page.waitForFunction(() => {
      const dashboard = document.querySelector('lv-dashboard-page') as any
      return dashboard?.shadowRoot?.querySelector('lv-chat-drawer')?.open === false
    })
    const afterDrawerEscape = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const root = element.shadowRoot
      return {
        open: root.querySelector('lv-chat-drawer')?.open,
        focusReturned: root.activeElement === root.querySelector('.agent-toggle'),
      }
    })
    expect(afterDrawerEscape).toEqual({ open: false, focusReturned: true })
  } finally { await page.close() }
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
  } finally { await page.close() }
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

    await page.evaluate(async () => {
      const element = document.querySelector('lv-dashboard-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: {
        activeConversationId: 'agentconv_saved',
        transcript: [{ id: 'user_saved', kind: 'user', text: 'Persisted question' }],
      } })
      await element.updateComplete
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
    })
    await page.locator('lv-chat-drawer').locator('[aria-label="Close agent"]').click()
    await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
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


test('side agent keeps the composer visible and starter prompts never submit automatically', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 650 } })
  try {
    await page.goto(baseURL)
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { transcript: [], status: { enabled: true, running: false }, composer: { value: '', disabled: false, placeholder: 'Ask' } } })
      ;(window as any).sideSubmits = 0
      document.addEventListener('lv-chat-submit', () => (window as any).sideSubmits++)
    })
    await page.locator('.agent-toggle').click()
    const drawer = page.locator('lv-chat-drawer[open]')
    await drawer.getByRole('button', { name: 'Summarize: Summarize the key takeaways on this page.', exact: true }).click()
    expect(await drawer.locator('textarea').inputValue()).toBe('Summarize the key takeaways on this page.')
    expect(await page.evaluate(() => (window as any).sideSubmits)).toBe(0)
    const geometry = await drawer.evaluate(element => ({ bottom: element.getBoundingClientRect().bottom, composerBottom: element.shadowRoot!.querySelector('lv-chat-composer')!.getBoundingClientRect().bottom, viewport: innerHeight }))
    expect(geometry.bottom).toBeLessThanOrEqual(geometry.viewport + 1)
    expect(geometry.composerBottom).toBeLessThanOrEqual(geometry.viewport + 1)
    await evaluateAcrossContextTurnover(page, () => page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agentTurnPending: true })
    }))
    expect(await drawer.getByRole('button', { name: 'New chat', exact: true }).isDisabled()).toBe(true)
    await drawer.getByRole('status', { name: 'Working' }).waitFor()
  } finally { await page.close() }
})

test('dashboard agent opens an eligible query visual with a Save action', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  let savedVisual: Record<string, unknown> | null = null
  try {
    await page.route('**/explore/saved', async (route) => {
      savedVisual = route.request().postDataJSON() as Record<string, unknown>
      await route.fulfill({ status: 201, contentType: 'application/json', body: '{}' })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-page') && customElements.get('lv-chat-drawer'))
    const baseVisual = testVisualizationEnvelopes().orders_chart
    await page.evaluate(async ({ baseVisual }) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      const visual = {
            ...baseVisual,
            visualID: 'chat-chart',
            spec: { ...baseVisual.spec, title: 'Revenue by country' },
            status: { kind: 'ready' },
      }
      ;(window as any).__chatDrawerVisual = visual
      mergePatch({
        agent: {
          activeConversationId: 'chat-one',
          transcript: [{
            id: 'tool-chart',
            kind: 'tool',
            name: 'query_visual',
            status: 'complete',
            argumentsJson: JSON.stringify({ semanticModelId: 'semantic:sales', visual: { type: 'bar', query: { type: 'aggregate', dimensions: ['country'], metrics: ['revenue'], limit: 25 } } }),
            resultJson: JSON.stringify({ ok: true, type: 'bar', id: 'chat-chart', datasetId: 'orders', semanticModelRef: { kind: 'semantic_model', id: 'semantic:sales' }, fields: [
              { fieldId: 'semantic:sales.country', role: 'dimension', alias: 'country', explorerFieldId: 'orders.country', label: 'Country' },
              { fieldId: 'semantic:sales.revenue', role: 'metric', alias: 'revenue', label: 'Revenue' },
            ] }),
            artifact: { id: 'chat-chart', type: 'bar', summary: 'Created chart.' },
          }],
        },
        agentVisuals: { 'chat-chart': visual },
      })
    }, { baseVisual })
    await page.locator('lv-dashboard-page').evaluate((element: any) => {
      element.shadowRoot.querySelector('.agent-toggle').click()
    })
    await page.waitForFunction(() => Boolean(document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')?.shadowRoot?.querySelector('lv-chat-thread')?.shadowRoot?.querySelector('[data-visual-id="chat-chart"]')))
    await page.locator('lv-dashboard-page').evaluate((element: any) => {
      element.shadowRoot.querySelector('lv-chat-drawer').shadowRoot.querySelector('lv-chat-thread').shadowRoot.querySelector('[data-visual-id="chat-chart"]').click()
    })
    await page.waitForFunction(() => {
      const route = document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('.route')
      const drawer = route?.querySelector('lv-chat-drawer')
      const panel = drawer?.shadowRoot?.querySelector('lv-chat-visual-panel')
      if (!drawer || !panel || route?.getAnimations().some(animation => animation.playState === 'running')) return false
      const panelBounds = panel.getBoundingClientRect()
      const drawerBounds = drawer.getBoundingClientRect()
      return panelBounds.left >= drawerBounds.left && panelBounds.right <= drawerBounds.right + 1 && panelBounds.bottom <= drawerBounds.bottom + 1
    })
    const panelState = await page.locator('lv-dashboard-page').evaluate(async (element: any) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
      const panel = drawer.shadowRoot.querySelector('lv-chat-visual-panel') as any
      await panel.updateComplete
      return {
        title: panel.shadowRoot.querySelector('h2')?.textContent,
        hasSave: Boolean(panel.shadowRoot.querySelector('[aria-label="Save visual to Data Explorer"]')),
        hasExploreLink: Boolean(panel.shadowRoot.querySelector('a[aria-label="Open visual in Data Explorer"]')),
        explorerHref: panel.explorerHref,
        panelBounds: (() => {
          const panelBounds = panel.getBoundingClientRect()
          const drawerBounds = drawer.getBoundingClientRect()
          return {
            width: Math.round(panelBounds.width),
            height: Math.round(panelBounds.height),
            withinDrawer: panelBounds.left >= drawerBounds.left && panelBounds.right <= drawerBounds.right + 1 && panelBounds.bottom <= drawerBounds.bottom + 1,
          }
        })(),
      }
    })
    expect(panelState.title).toBe('Revenue by country')
    expect(panelState.hasSave).toBe(true)
    expect(panelState.hasExploreLink).toBe(false)
    expect(panelState.panelBounds.width).toBeGreaterThan(300)
    expect(panelState.panelBounds.height).toBeGreaterThan(400)
    expect(panelState.panelBounds.withinDrawer).toBe(true)
    const explorerURL = new URL(panelState.explorerHref!, 'https://example.test')
    expect(explorerURL.pathname).toBe('/explore')
    expect(explorerURL.searchParams.get('mode')).toBe('explore')
    expect(explorerURL.searchParams.get('semanticModel')).toBe('semantic:sales')
    expect(explorerURL.searchParams.get('dataset')).toBe('orders')
    expect(explorerURL.searchParams.getAll('dimension')).toEqual(['orders.country'])
    expect(explorerURL.searchParams.getAll('metric')).toEqual(['revenue'])
    expect(explorerURL.searchParams.get('limit')).toBe('25')

    const screenshotDir = process.env.LEAPVIEW_DASHBOARD_AGENT_SCREENSHOT_DIR
    if (screenshotDir) {
      await page.waitForFunction(() => Boolean(document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')?.shadowRoot?.querySelector('lv-chat-visual-panel')?.shadowRoot?.querySelector('lv-visual-artifact')?.shadowRoot?.querySelector('lv-visualization-host')?.shadowRoot?.querySelector('.renderer canvas')))
      await mkdir(screenshotDir, { recursive: true })
      await page.screenshot({ path: join(screenshotDir, 'dashboard-agent-visual-details.png'), fullPage: true })
    }

    await page.locator('lv-dashboard-page').evaluate((element: any) => {
      element.shadowRoot.querySelector('lv-chat-drawer').shadowRoot.querySelector('lv-chat-visual-panel').shadowRoot.querySelector('[aria-label="Save visual to Data Explorer"]').click()
    })
    await page.waitForFunction(() => document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')?.shadowRoot?.querySelector('lv-chat-visual-panel')?.shadowRoot?.textContent?.includes('Saved to Data Explorer.'))
    expect(savedVisual as unknown).toEqual({ title: 'Revenue by country', explorerUrl: panelState.explorerHref })

    const drawerRemainsOpen = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      const panel = drawer.shadowRoot.querySelector('lv-chat-visual-panel') as HTMLElement
      panel.shadowRoot!.querySelector('[aria-label="Close visual details"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true, cancelable: true }))
      return drawer.open
    })
    expect(drawerRemainsOpen).toBe(true)
    await page.waitForFunction(() => !document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')?.shadowRoot?.querySelector('lv-chat-visual-panel'))

    await page.locator('lv-dashboard-page').evaluate((element: any) => {
      element.shadowRoot.querySelector('lv-chat-drawer').shadowRoot.querySelector('lv-chat-thread').shadowRoot.querySelector('[aria-label="Open visual details: Revenue by country"]').click()
    })
    await page.waitForFunction(() => Boolean(document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')?.shadowRoot?.querySelector('lv-chat-visual-panel')))
    await page.locator('lv-dashboard-page').evaluate((element: any) => {
      element.shadowRoot.querySelector('lv-chat-drawer').shadowRoot.querySelector('[aria-label="New chat"]').click()
    })
    await page.waitForFunction(() => !document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')?.shadowRoot?.querySelector('lv-chat-visual-panel'))
    const afterNewChat = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      return {
        open: drawer.open,
        hasThread: Boolean(drawer.shadowRoot.querySelector('lv-chat-thread')),
        hasComposer: Boolean(drawer.shadowRoot.querySelector('lv-chat-composer')),
      }
    })
    expect(afterNewChat).toEqual({ open: true, hasThread: true, hasComposer: true })

    await page.locator('lv-dashboard-page').evaluate((element: any) => {
      element.shadowRoot.querySelector('lv-chat-drawer').shadowRoot.querySelector('lv-chat-thread').shadowRoot.querySelector('[data-visual-id="chat-chart"]').click()
    })
    await page.waitForFunction(() => Boolean(document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')?.shadowRoot?.querySelector('lv-chat-visual-panel')))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agentVisuals: { 'chat-chart': null } })
    })
    await page.waitForFunction(() => {
      const drawer = document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')
      const thread = drawer?.shadowRoot?.querySelector('lv-chat-thread') as HTMLElement | null
      return Boolean(thread && !thread.hidden && !drawer?.shadowRoot?.querySelector('lv-chat-visual-panel') && drawer?.shadowRoot?.querySelector('lv-chat-composer'))
    })
    const afterVisualRemoved = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      const thread = drawer.shadowRoot.querySelector('lv-chat-thread')
      return {
        hasVisualPanel: Boolean(drawer.shadowRoot.querySelector('lv-chat-visual-panel')),
        threadHidden: thread.hidden,
        hasComposer: Boolean(drawer.shadowRoot.querySelector('lv-chat-composer')),
        hasInlineArtifact: Boolean(thread.shadowRoot.querySelector('lv-visual-artifact[artifact-id="chat-chart"]')),
      }
    })
    expect(afterVisualRemoved).toEqual({ hasVisualPanel: false, threadHidden: false, hasComposer: true, hasInlineArtifact: true })

    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agentVisuals: { 'chat-chart': (window as any).__chatDrawerVisual } })
    })
    await page.waitForFunction(() => Boolean(document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')?.shadowRoot?.querySelector('lv-chat-thread')?.shadowRoot?.querySelector('[data-visual-id="chat-chart"]')))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { activeConversationId: 'chat-two', transcript: [{ id: 'user-two', kind: 'user', text: 'Second conversation' }] } })
    })
    await page.waitForFunction(() => {
      const drawer = document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-chat-drawer')
      const thread = drawer?.shadowRoot?.querySelector('lv-chat-thread') as HTMLElement | null
      return Boolean(thread && !thread.hidden && !drawer?.shadowRoot?.querySelector('lv-chat-visual-panel') && thread.shadowRoot?.textContent?.includes('Second conversation'))
    })
    const afterConversationSwitch = await page.locator('lv-dashboard-page').evaluate((element: any) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      return {
        hasVisualPanel: Boolean(drawer.shadowRoot.querySelector('lv-chat-visual-panel')),
        threadHidden: drawer.shadowRoot.querySelector('lv-chat-thread').hidden,
        hasComposer: Boolean(drawer.shadowRoot.querySelector('lv-chat-composer')),
      }
    })
    expect(afterConversationSwitch).toEqual({ hasVisualPanel: false, threadHidden: false, hasComposer: true })
  } finally { await page.close() }
})

test.each(['back', 'close'])('full chat %s returns to the same route, conversation, draft and scroll after document reload', async (returnAction) => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.route('**/dashboards/return**', route => route.fulfill({ contentType: 'text/html', body: testDocument() }))
    const navigation = await Bun.build({ entrypoints: ['web/components/chat/chat-navigation.ts'], target: 'browser', format: 'esm' })
    if (!navigation.success) throw new Error('navigation helper fixture did not build')
    const navigationScript = await navigation.outputs[0].text()
    await page.route('**/return-navigation.js', route => route.fulfill({ contentType: 'text/javascript', body: navigationScript }))
    await page.route('**/chats/return-conversation**', route => route.fulfill({ contentType: 'text/html', body: '<button id="close">Close full chat</button><script type="module">import { returnFromFullChat } from "/return-navigation.js"; document.querySelector("#close").onclick = returnFromFullChat;</script>' }))
    await page.addInitScript(() => {
      window.addEventListener('lv-chat-restore', (event: Event) => event.stopImmediatePropagation(), { capture: true })
    })
    await page.goto(`${baseURL}/dashboards/return?filter=retained#visual`)
    await page.waitForFunction(() => Boolean((document.querySelector('lv-dashboard-page') as any)?.page))
    const hydrate = async () => page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: {
        activeConversationId: 'return-conversation',
        status: { enabled: true, running: false },
        composer: { value: '', disabled: false },
        transcript: Array.from({ length: 30 }, (_, i) => ({ id: `user-${i}`, kind: 'user', text: `Question ${i}: retained conversation position` })),
      } })
      const shell = document.querySelector('lv-dashboard-page') as any
      shell.setAgentDrawerOpen(true)
      await shell.updateComplete
      const drawer = shell.shadowRoot.querySelector('lv-chat-drawer') as any
      drawer.open = true
      await drawer.updateComplete
    })
    await hydrate()
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { status: { running: true } } })
    })
    await page.waitForFunction(() => {
      const drawer = (document.querySelector('lv-dashboard-page') as any)?.shadowRoot.querySelector('lv-chat-drawer')
      return drawer?.shadowRoot.querySelector('[aria-label="Open full chat"]')?.getAttribute('aria-disabled') === 'true'
    })
    expect(await page.locator('lv-chat-drawer [aria-label="Open full chat"]').getAttribute('href')).toBeNull()
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { status: { running: false } } })
    })
    await page.locator('lv-chat-drawer').evaluate(async (drawer: any) => {
      const composer = drawer.shadowRoot.querySelector('lv-chat-composer')
      await composer.updateComplete
      composer.setDraft('My unsent follow-up', false)
      const thread = drawer.shadowRoot.querySelector('lv-chat-thread')
      await thread.updateComplete
      thread.restoreScroll({ top: 150, follow: false })
    })
    await page.locator('lv-chat-drawer [aria-label="Open full chat"]').click()
    await page.waitForURL('**/chats/return-conversation?return=*')
    if (returnAction === 'back') await page.goBack()
    else await page.locator('#close').click()
    await page.waitForFunction(() => Boolean((document.querySelector('lv-dashboard-page') as any)?.page))
    await hydrate()
    await page.waitForFunction(() => {
      const drawer = (document.querySelector('lv-dashboard-page') as any)?.shadowRoot.querySelector('lv-chat-drawer')
      return drawer?.shadowRoot.querySelector('lv-chat-composer')?.snapshotDraft() === 'My unsent follow-up'
    })
    expect(page.url()).toBe(`${baseURL}/dashboards/return?filter=retained#visual`)
    const restored = await page.locator('lv-chat-drawer').evaluate((drawer: any) => ({
      conversationId: drawer.agent.activeConversationId,
      open: drawer.open,
      scroll: drawer.shadowRoot.querySelector('lv-chat-thread').snapshotScroll(),
    }))
    expect(restored.conversationId).toBe('return-conversation')
    expect(restored.open).toBe(true)
    expect(restored.scroll.follow).toBe(false)
    expect(restored.scroll.top).toBeCloseTo(150, 0)
  } finally { await page.close() }
})

test('drawer Preview creates a builder draft with a stable retry and preserves its conversation', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  const requests: Array<{ key?: string; body: unknown }> = []
  try {
    await page.route('**/chats/builder-chat/visuals/chat-chart/dashboards', async route => {
      requests.push({ key: route.request().headers()['idempotency-key'], body: route.request().postDataJSON() })
      await route.fulfill(requests.length === 1
        ? { status: 503, json: { error: 'unavailable' } }
        : { status: 200, json: { dashboardId: 'created', title: 'Revenue by country', pageId: 'overview', href: '/dashboards/created/edit?page=overview' } })
    })
    await page.route('**/dashboards/created/edit?*', route => route.fulfill({ contentType: 'text/html', body: '<h1>Dashboard builder</h1>' }))
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-drawer'))
    await page.evaluate(async (baseVisual) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: {
        activeConversationId: 'builder-chat', status: { enabled: true, running: false }, composer: { value: '', disabled: false },
        transcript: [{ id: 'tool', kind: 'tool', name: 'query_visual', status: 'complete', resultJson: JSON.stringify({ ok: true, type: 'bar', id: 'chat-chart', semanticModelRef: { kind: 'semantic_model', id: 'semantic:sales' }, datasetId: 'orders', fields: [{ fieldId: 'semantic:sales.country', role: 'dimension', alias: 'country', explorerFieldId: 'orders.country', label: 'Country' }, { fieldId: 'semantic:sales.revenue', role: 'metric', alias: 'revenue', label: 'Revenue' }] }), artifact: { id: 'chat-chart', type: 'bar', summary: 'Created chart.' } }],
      }, agentVisuals: { 'chat-chart': { ...baseVisual, visualID: 'chat-chart', spec: { ...baseVisual.spec, title: 'Revenue by country' } } } })
      const shell = document.querySelector('lv-dashboard-page') as any
      shell.setAgentDrawerOpen(true)
      await shell.updateComplete
      const drawer = shell.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
      drawer.shadowRoot.querySelector('lv-chat-composer').setDraft('My unsent builder edit', false)
    }, testVisualizationEnvelopes().orders_chart)
    const drawer = page.locator('lv-chat-drawer')
    // This fixture supplies a governed visualization envelope; eligibility of
    // rendered transcript cards is covered by the Save-action test above.
    await drawer.evaluate((host: any) => host.shadowRoot.querySelector('lv-chat-thread').dispatchEvent(new CustomEvent('lv-chat-visual-open', { detail: { artifactId: 'chat-chart', title: 'Revenue by country', explorerHref: '' }, bubbles: true, composed: true })))
    const preview = drawer.getByRole('button', { name: 'Preview', exact: true })
    await preview.click()
    await drawer.getByRole('alert').waitFor()
    expect(page.url()).toBe(`${baseURL}/`)
    await preview.click()
    await page.waitForURL('**/dashboards/created/edit?page=overview')
    expect(requests).toHaveLength(2)
    expect(requests[0].key).toBe(requests[1].key)
    expect(requests[0].body).toEqual({ title: 'Revenue by country' })
    const handoff = await page.evaluate(() => Object.values(JSON.parse(sessionStorage.getItem('leapview-builder-chat-handoffs-v1') || '{}')) as any[])
    expect(handoff[0]?.state.conversationId).toBe('builder-chat')
    expect(handoff[0]?.state.draft).toBe('My unsent builder edit')
    expect(handoff[0]?.state.selectedVisualId).toBe('')
  } finally { await page.close() }
})

test('bfcache return authorizes a promoted conversation once before restoring drawer state', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.addInitScript(() => {
      ;(window as any).__returnRestores = []
      window.addEventListener('lv-chat-restore', (event: Event) => {
        ;(window as any).__returnRestores.push((event as CustomEvent).detail)
        event.stopImmediatePropagation()
      }, { capture: true })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => Boolean((document.querySelector('lv-dashboard-page') as any)?.page))
    await page.evaluate(async () => {
      history.replaceState({}, '', '/dashboards/bfcache?filter=kept')
      const shell = document.querySelector('lv-dashboard-page') as any
      shell.setAgentDrawerOpen(true)
      await shell.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { activeConversationId: '', transcript: [], status: { enabled: true, running: false }, composer: { value: '', disabled: false } } })
      await shell.updateComplete
      sessionStorage.setItem('leapview-chat-returns-v1', JSON.stringify({ promoted: {
        href: '/dashboards/bfcache?filter=kept', created: Date.now(),
        state: { conversationId: 'promoted-conversation', draft: 'Retained draft', references: [], editMessageId: '', selectedVisualId: '', selectedExplorerHref: '', selectedVisualTitle: '', scroll: { top: 0, follow: false } },
      } }))
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    expect(await page.evaluate(() => (window as any).__returnRestores)).toEqual([{ conversationId: 'promoted-conversation' }])
    // An absent/unauthorized response must not create a retry loop.
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { activeConversationId: '', transcript: [] } })
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    expect(await page.evaluate(() => (window as any).__returnRestores)).toHaveLength(1)
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { activeConversationId: 'promoted-conversation', transcript: [{ id: 'accepted', kind: 'user', text: 'New full chat question' }] } })
    })
    await page.waitForFunction(() => {
      const drawer = (document.querySelector('lv-dashboard-page') as any)?.shadowRoot.querySelector('lv-chat-drawer')
      return drawer?.agent.activeConversationId === 'promoted-conversation' && drawer.shadowRoot.querySelector('lv-chat-composer')?.snapshotDraft() === 'Retained draft'
    })
  } finally { await page.close() }
})


test.each([200, 503])('drawer ignores a previous conversation Preview response (%s)', async (status) => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  let release!: () => void
  let received!: () => void
  const requestReceived = new Promise<void>(resolve => { received = resolve })
  let requests = 0
  try {
    await page.route('**/chats/previous-preview/visuals/chart/dashboards', async route => {
      requests++
      await new Promise<void>(resolve => { release = resolve; received() })
      await route.fulfill({ status, json: status === 200
        ? { dashboardId: 'previous', title: 'Revenue', pageId: 'overview', href: '/dashboards/previous/edit?page=overview' }
        : { error: 'unavailable' } })
    })
    await page.route('**/dashboards/previous/edit?*', route => route.fulfill({ contentType: 'text/html', body: '<h1>Previous builder</h1>' }))
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-drawer'))
    await page.evaluate(async (visual) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { activeConversationId: 'previous-preview', status: { enabled: true, running: false }, composer: { value: '', disabled: false }, transcript: [] }, agentVisuals: { chart: visual } })
      const shell = document.querySelector('lv-dashboard-page') as any
      shell.setAgentDrawerOpen(true)
      await shell.updateComplete
      const drawer = shell.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
      drawer.selectedVisualID = 'chart'
      drawer.selectedVisualTitle = 'Revenue'
      void drawer.previewVisual()
    }, testVisualizationEnvelopes().orders_chart)
    await requestReceived
    await page.locator('lv-chat-drawer').evaluate(async (drawer: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { activeConversationId: 'current-preview' } })
      await drawer.updateComplete
      drawer.visualSaveError = 'Current conversation error'
      drawer.visualPreviewHref = '/dashboards/current/edit'
    })
    release()
    await page.waitForFunction(() => !(document.querySelector('lv-dashboard-page') as any)?.shadowRoot.querySelector('lv-chat-drawer')?.visualPreviewPending)
    expect(page.url()).toBe(`${baseURL}/`)
    expect(await page.locator('lv-chat-drawer').evaluate((drawer: any) => ({ error: drawer.visualSaveError, href: drawer.visualPreviewHref })))
      .toEqual({ error: 'Current conversation error', href: '/dashboards/current/edit' })
    if (status === 200) {
      await page.locator('lv-chat-drawer').evaluate(async (drawer: any) => {
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
        mergePatch({ agent: { activeConversationId: 'previous-preview' } })
        await drawer.updateComplete
        drawer.selectedVisualID = 'chart'
        drawer.selectedVisualTitle = 'Revenue'
        void drawer.previewVisual()
      })
      await page.waitForURL('**/dashboards/previous/edit?page=overview')
      expect(requests).toBe(1)
    }
  } finally { release?.(); await page.close() }
})
