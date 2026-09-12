import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { evaluateAcrossContextTurnover, testDocument } from './dashboard-page-test-fixtures'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/dashboard-page-test')

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
        tableAskActionRow: tableAsk.assignedSlot?.parentElement?.className,
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
    expect(visualActionsAtRest.tableAskRight).toBeLessThanOrEqual(visualActionsAtRest.tableExpandLeft)
    expect(visualActionsAtRest.tableExpandLeft - visualActionsAtRest.tableAskRight).toBeGreaterThanOrEqual(4)
    expect(visualActionsAtRest.tableExpandRight).toBeLessThanOrEqual(visualActionsAtRest.tableOptionsLeft)
    expect(visualActionsAtRest.tableRight - visualActionsAtRest.tableOptionsRight).toBe(8)

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
    await drawer.getByRole('button', { name: 'Summarize the key takeaways on this page.', exact: true }).click()
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
    await drawer.getByRole('status').filter({ hasText: 'Working' }).waitFor()
  } finally { await page.close() }
})
