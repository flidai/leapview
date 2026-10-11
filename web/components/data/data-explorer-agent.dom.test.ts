import { afterAll, beforeAll, expect, test } from 'bun:test'
import { expect as browserExpect, type Browser, type Page } from '@playwright/test'
import { createDataExplorerDOMFixture } from './data-explorer.test-fixture'

let fixture: Awaited<ReturnType<typeof createDataExplorerDOMFixture>>
let baseURL = ''
let browser: Browser
let inspectorModule = ''
let appCSS = ''
let appShellModule = ''

beforeAll(async () => {
  fixture = await createDataExplorerDOMFixture()
  baseURL = fixture.baseURL
  browser = fixture.browser
  const inspectorBuild = await Bun.build({
    entrypoints: ['web/components/inspector/datastar-inspector.ts'], target: 'browser', format: 'esm',
  })
  if (!inspectorBuild.success) throw new Error('Inspector fixture build failed')
  inspectorModule = await inspectorBuild.outputs[0].text()
  const shellBuild = await Bun.build({ entrypoints: ['web/components/app/app-shell.ts'], target: 'browser', format: 'esm' })
  if (!shellBuild.success) throw new Error('App shell fixture build failed')
  appShellModule = await shellBuild.outputs[0].text()
  const cssPath = '.tmp/data-explorer-test/agent-inspector-app.css'
  const cssBuild = Bun.spawn(['./node_modules/.bin/tailwindcss', '-i', './static/app.input.css', '-o', cssPath], {
    stdout: 'ignore', stderr: 'pipe',
  })
  const cssErrors = await new Response(cssBuild.stderr).text()
  if (await cssBuild.exited) throw new Error(`Inspector CSS fixture build failed: ${cssErrors}`)
  appCSS = await Bun.file(cssPath).text()
})

afterAll(async () => {
  await fixture?.close()
}, 15_000)

test('Explorer agent labels describe data while dashboard and builder labels remain unchanged', async () => {
  const page = await browser.newPage({ viewport: { width: 768, height: 844 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        agent: { conversations: [], activeConversationId: '', transcript: [],
          status: { enabled: true, running: false }, composer: { value: '', disabled: false, placeholder: '' } },
        agentContext: { surface: 'data', pageTitle: '', referenceLimit: 12, references: [],
          exploration: { schemaVersion: 1, modelId: 'private-model-id', datasetId: 'private-dataset-id',
            dimensions: [], metrics: [], filters: [], sort: [], limit: 10 } },
      })
      const element = document.createElement('lv-data-explorer') as any
      document.body.append(element)
      await element.updateComplete
    })
    const explorer = page.locator('lv-data-explorer')
    await explorer.getByRole('button', { name: 'Ask about this data', exact: true }).click()
    const drawer = explorer.locator('lv-chat-drawer')
    await drawer.evaluate((element: any) => element.updateComplete)
    const labels = () => drawer.evaluate((element: any) => {
      const root = element.shadowRoot as ShadowRoot
      const composer = root.querySelector('lv-chat-composer') as any
      return { dialog: root.querySelector('aside')?.getAttribute('aria-label'),
        heading: root.querySelector('.title')?.textContent?.trim(),
        context: root.querySelector('.context')?.getAttribute('aria-label'),
        current: root.querySelector('.page-context')?.textContent?.trim(),
        welcome: root.querySelector('.welcome')?.getAttribute('aria-label'),
        placeholder: composer.placeholder, disabled: composer.disabled,
        leaksRawIDs: /private-model-id|private-dataset-id/.test(root.textContent ?? '') }
    })
    expect(await labels()).toMatchObject({ dialog: 'Explorer agent', heading: 'Explorer agent',
      context: 'Included data context', current: 'Current exploration', welcome: 'Start a data conversation',
      placeholder: 'Ask about this data…', disabled: false, leaksRawIDs: false })
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ agentContext: { surface: 'dashboard', exploration: null } })
    })
    await drawer.evaluate(async (element: any) => { element.requestUpdate(); await element.updateComplete })
    expect(await labels()).toMatchObject({ dialog: 'Dashboard agent', heading: 'Dashboard agent',
      context: 'Included dashboard context', current: 'Current page', welcome: 'Start a dashboard conversation',
      placeholder: 'Ask about this dashboard…' })
    await drawer.evaluate(async (element: any) => { element.embedded = true; await element.updateComplete })
    expect((await labels()).heading).toBe('Dashboard agent')
    expect((await labels()).placeholder).toBe('Ask to change this dashboard…')
  } finally { await page.close() }
})

test('Explorer agent waits for governed data context while keeping active Stop reachable', async () => {
  const page = await browser.newPage({ viewport: { width: 375, height: 844 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        agent: { conversations: [], activeConversationId: '', transcript: [],
          status: { enabled: true, running: false, canContinue: false },
          composer: { value: 'Keep this unsent data question', disabled: false, placeholder: '' } },
        agentContext: { surface: 'data', modelId: '', datasetId: null, exploration: null,
          referenceLimit: 12, references: [] } })
      const element = document.createElement('lv-data-explorer') as any
      document.body.append(element)
      await element.updateComplete
    })
    const explorer = page.locator('lv-data-explorer')
    await explorer.getByRole('button', { name: 'Ask about this data', exact: true }).click()
    const drawer = explorer.locator('lv-chat-drawer'), composer = drawer.locator('lv-chat-composer')
    await expect(composer.getByRole('combobox').isDisabled()).resolves.toBe(true)
    await expect(composer.getByRole('button', { name: 'Send', exact: true }).isDisabled()).resolves.toBe(true)
    expect(await drawer.getByRole('status').textContent()).toBe('Select a model or switch to Analyze to ask about data.')
    const disabledPrompts = await drawer.locator('.prompt').evaluateAll((items: HTMLButtonElement[]) => items.length > 0 && items.every(item => item.disabled))
    expect(disabledPrompts).toBe(true)
    expect(await composer.getByRole('combobox').inputValue()).toBe('Keep this unsent data question')
    await drawer.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ agent: { status: { canContinue: true }, composer: { value: '' } } })
      e.requestUpdate(); await e.updateComplete
    })
    expect(await composer.getByRole('button', { name: 'Continue response', exact: true }).isDisabled()).toBe(true)
    await drawer.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ agent: { status: { running: true, runId: 'owned-active-run' } } })
      e.requestUpdate(); await e.updateComplete
    })
    expect(await composer.getByRole('button', { name: 'Stop response', exact: true }).isEnabled()).toBe(true)
    await drawer.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ agent: { status: { running: false, runId: '', canContinue: true } },
        agentContext: { modelId: 'selected-model', datasetId: 'selected-dataset',
          exploration: { schemaVersion: 1, modelId: 'selected-model', datasetId: 'selected-dataset', dimensions: [], metrics: [], filters: [], sort: [], limit: 10 } } })
      e.requestUpdate(); await e.updateComplete
    })
    expect(await composer.getByRole('combobox').isEnabled()).toBe(true)
    expect(await composer.getByRole('button', { name: 'Continue response', exact: true }).isEnabled()).toBe(true)
    expect(await drawer.getByRole('status').count()).toBe(0)
    expect(await drawer.locator('.prompt').evaluateAll((items: HTMLButtonElement[]) => items.every(item => !item.disabled))).toBe(true)
    await drawer.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ agentContext: { surface: 'dashboard', exploration: null } })
      e.requestUpdate(); await e.updateComplete
    })
    expect(await composer.getByRole('combobox').isEnabled()).toBe(true)
    await drawer.getByRole('button', { name: 'Close agent', exact: true }).click()
    expect(await explorer.locator('lv-chat-drawer').count()).toBe(1)
      expect(await explorer.locator('lv-chat-drawer').isVisible()).toBe(false)
      expect(await explorer.locator('lv-chat-drawer').getAttribute('open')).toBeNull()
      expect(await explorer.locator('lv-chat-drawer').locator('aside').getAttribute('inert')).toBe('')
  } finally { await page.close() }
})

for (const width of [375, 767, 768, 769, 1024, 1280, 1440]) {
  test(`Explorer agent preserves a usable route at ${width}px beside app navigation`, async () => {
    const page = await browser.newPage({ viewport: { width, height: 844 } })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-data-explorer'))
      await page.evaluate(async ({ navWidth }) => {
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
        mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] }, agent: { conversations: [], activeConversationId: '', transcript: [], status: { enabled: false, running: false }, composer: { value: '', disabled: true, placeholder: 'Agent is not configured.' } } })
        const element = document.createElement('lv-data-explorer') as any
        element.style.marginLeft = `${navWidth}px`
        element.style.width = `calc(100% - ${navWidth}px)`
        document.body.append(element)
        await element.updateComplete
      }, { navWidth: width > 720 ? 248 : 44 })
      const explorer = page.locator('lv-data-explorer')
      const geometry = () => explorer.evaluate((element: any) => {
        const root = element.shadowRoot as ShadowRoot
        const route = root.querySelector('.route')!.getBoundingClientRect()
        const header = root.querySelector('.header')!.getBoundingClientRect()
        const drawer = root.querySelector('lv-chat-drawer')?.getBoundingClientRect()
        return { routeWidth: route.width, routeRight: route.right, headerWidth: header.width,
          headerRight: header.right, drawerLeft: drawer?.left, drawerRight: drawer?.right,
          drawerPosition: drawer ? getComputedStyle(root.querySelector('lv-chat-drawer')!).position : '' }
      })
      const closed = await geometry()
      const ask = explorer.getByRole('button', { name: 'Ask about this data', exact: true })
      await ask.click()
      await explorer.evaluate((element: any) => element.updateComplete)
      const open = await geometry()
      if (closed.routeWidth <= 900) {
        expect(open.drawerPosition).toBe('fixed')
        expect(open.drawerLeft).toBe(0)
        expect(open.drawerRight).toBe(width)
        expect(open.headerWidth).toBe(closed.headerWidth)
      } else {
        expect(open.drawerPosition).not.toBe('fixed')
        expect(open.headerWidth).toBeGreaterThanOrEqual(400)
        expect(open.headerRight).toBeLessThanOrEqual(open.drawerLeft!)
        expect(open.drawerRight).toBeLessThanOrEqual(open.routeRight)
      }
      await explorer.locator('lv-chat-drawer').getByRole('button', { name: 'Close agent', exact: true }).click()
      await explorer.evaluate((element: any) => element.updateComplete)
      expect(await explorer.locator('lv-chat-drawer').count()).toBe(1)
      expect(await explorer.locator('lv-chat-drawer').isVisible()).toBe(false)
      expect(await explorer.locator('lv-chat-drawer').getAttribute('open')).toBeNull()
      expect(await explorer.locator('lv-chat-drawer').locator('aside').getAttribute('inert')).toBe('')
      expect((await geometry()).headerWidth).toBe(closed.headerWidth)
      expect(await ask.getAttribute('aria-expanded')).toBe('false')
      await ask.click()
      await explorer.locator('lv-chat-drawer').getByRole('button', { name: 'Close agent', exact: true }).press('Escape')
      await explorer.evaluate((element: any) => element.updateComplete)
      expect(await explorer.locator('lv-chat-drawer').count()).toBe(1)
      expect(await explorer.locator('lv-chat-drawer').isVisible()).toBe(false)
      expect(await explorer.locator('lv-chat-drawer').getAttribute('open')).toBeNull()
      expect(await explorer.locator('lv-chat-drawer').locator('aside').getAttribute('inert')).toBe('')
      expect((await geometry()).headerWidth).toBe(closed.headerWidth)
    } finally {
      await page.close()
    }
  })
}

for (const width of [375, 390, 768]) {
  test(`Rows filter drawer remains inside its results pane at ${width}px beside the app rail`, async () => {
    const page = await browser.newPage({ viewport: { width, height: 844 } })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-data-explorer') && customElements.get('lv-data-explorer-query-controls'))
      await page.evaluate(async () => {
        const element = document.createElement('lv-data-explorer') as any
        // Reproduce the collapsed application rail as well as Explorer's own
        // 44px data browser rail; viewport units cannot describe this pane.
        element.style.marginLeft = '44px'
        element.style.width = 'calc(100% - 44px)'
        const object = { key: 'model:orders', resourceId: 'model:orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1, columns: [{ key: 'status' }] }
        const spec = { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 }
        const command = { spec, semanticModelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
        mergePatch({
          page: { kind: 'data', title: 'Data Explorer', tabs: [] },
          dataExplorer: {
            objects: [object], selectedKey: object.key, selectedObject: object,
            command: { mode: 'browse', objectKey: object.key, explore: command, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
            explore: { command, semanticModels: [], datasets: [], fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', datasetId: 'orders', type: 'string', compatible: true }], result: { columns: [], rows: [], warnings: [] }, status: { loading: false, stale: false, requestSeq: 0, state: 'idle' } },
            preview: { columns: object.columns, totalRows: 1, availableRows: 1, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: { a: { start: 0, requestSeq: 0, resetVersion: 0, sort: {}, rows: [{ status: 'paid' }] } }, totalRowLabel: '1', sort: {}, error: '' }, warnings: [],
          },
        })
        ;(window as any).drawerCommands = []
        element.addEventListener('lv-data-explorer-command', (event: Event) => (window as any).drawerCommands.push((event as CustomEvent).detail))
        document.body.append(element)
        await element.updateComplete
      })
      const explorer = page.locator('lv-data-explorer')
      const closeBrowser = explorer.getByRole('button', { name: 'Close data browser', exact: true })
      if (await closeBrowser.count()) await closeBrowser.click()
      await explorer.getByRole('button', { name: 'Filters', exact: true }).click()
      const containment = await explorer.evaluate((host) => {
        const root = (host as HTMLElement).shadowRoot!
        const pane = root.querySelector('.main')!.getBoundingClientRect()
        const dock = root.querySelector('.semantic-filter-dock')!.getBoundingClientRect()
        const heading = root.querySelector('.semantic-filter-header strong')!.getBoundingClientRect()
        return { paneLeft: pane.left, paneRight: pane.right, dockLeft: dock.left, dockRight: dock.right, headingLeft: heading.left }
      })
      expect(containment.dockLeft).toBeGreaterThanOrEqual(containment.paneLeft)
      expect(containment.dockRight).toBeLessThanOrEqual(containment.paneRight)
      expect(containment.headingLeft).toBeGreaterThanOrEqual(containment.paneLeft)
      await explorer.getByLabel('Add filter', { exact: true }).click()
      await explorer.getByRole('button', { name: 'Status, orders', exact: true }).click()
      const editor = explorer.locator('lv-data-explorer-query-controls')
      await editor.getByLabel('Value', { exact: true }).fill('paid')
      await editor.getByRole('button', { name: 'Apply', exact: true }).click()
      expect(await page.evaluate(() => (window as any).drawerCommands.at(-1).explore.spec.filters[0].field)).toBe('orders.status')
      await explorer.getByRole('button', { name: 'Close filters', exact: true }).click()
      expect(await explorer.locator('.semantic-filter-panel').count()).toBe(0)
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true)
    } finally { await page.close() }
  })
}



async function openTransportAgent(page: Page, command = false, styled = false, expanded = false) {
  if (command) await page.route(baseURL + '/', route => route.fulfill({ contentType: 'text/html', body: `
    <!doctype html><main data-signals="{}"></main>
    <lv-data-explorer data-indicator="agentTurnPending" data-on:lv-chat-submit="$agent.composer.value = evt.detail.input; $agent.composer.editMessageId = evt.detail.editMessageId || ''; $agentContext.references = evt.detail.references; @post('/chats/turns', {retry: 'never', retryMaxCount: 0, openWhenHidden: true})"></lv-data-explorer>
    <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
    <script type="module" src="/data-explorer-under-test.js"></script>` }))
  await page.goto(expanded ? baseURL + '/?chat=expanded' : baseURL)
  await page.waitForFunction(() => customElements.get('lv-data-explorer'))
  if (styled) {
    await page.addStyleTag({ content: appCSS })
    await page.addScriptTag({ type: 'module', content: appShellModule })
    await page.waitForFunction(() => customElements.get('lv-app-shell'))
  }
  await page.evaluate(async (styled) => {
    const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
    mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] },
      agent: { conversations: [], activeConversationId: 'existing-conversation',
        transcript: [{ id: 'original-message', kind: 'user', text: 'Original question', runId: 'previous-run' }],
        status: { enabled: true, running: false, runId: null, error: null },
        composer: { value: '', disabled: false, placeholder: '' } },
      agentContext: { surface: 'data', referenceLimit: 12, references: [],
        exploration: { schemaVersion: 1, modelId: 'governed-model', datasetId: 'governed-dataset',
          dimensions: [], metrics: [], filters: [], sort: [], limit: 10 } } })
    const element = document.querySelector('lv-data-explorer') as any ?? document.createElement('lv-data-explorer')
    if (styled) {
      const shell = document.createElement('lv-app-shell')
      element.slot = 'page'
      shell.append(element)
      document.body.append(shell)
    } else if (!element.isConnected) document.body.append(element)
    element.requestUpdate(); await element.updateComplete
    ;(window as any).fetchFinished = 0
    document.addEventListener('datastar-fetch', event => { if ((event as CustomEvent).detail.type === 'finished') (window as any).fetchFinished++ })
  }, styled)
  const explorer = page.locator('lv-data-explorer')
  await explorer.locator('lv-chat-drawer').evaluate((element: any) => element.updateComplete)
  if (await explorer.locator('lv-chat-drawer').getAttribute('open') === null) {
    await explorer.getByRole('button', { name: 'Ask about this data', exact: true }).click()
  }
  await explorer.locator('lv-chat-drawer lv-chat-composer textarea').waitFor()
  return explorer
}

function signalEvent(signals: Record<string, unknown>, onlyIfMissing = false): string {
  return `event: datastar-patch-signals\ndata: onlyIfMissing ${onlyIfMissing}\ndata: signals ${JSON.stringify(signals)}\n\n`
}

for (const running of [false, true]) test(`Explorer SSE reconnect preserves ${running ? 'running' : 'idle'} chat while refreshing governed context`, async () => {
  const page = await browser.newPage({ viewport: { width: 768, height: 900 } })
  const browserErrors: string[] = []
  page.on('pageerror', error => browserErrors.push(error.message))
  try {
    await page.addInitScript(() => {
      ;(window as any).restoredConversations = []
      document.addEventListener('lv-chat-restore', event => {
        (window as any).restoredConversations.push((event as CustomEvent).detail.conversationId)
      })
    })
    const turns: any[] = []
    await page.route('**/chats/turns', route => {
      turns.push(JSON.parse(route.request().postData()!))
      return route.fulfill({ status: 204 })
    })
    const explorer = await openTransportAgent(page, true)
    const drawer = explorer.locator('lv-chat-drawer'), composer = drawer.locator('lv-chat-composer')
    const input = composer.getByRole('combobox')
    const reference = { reference: { kind: 'dataset', id: 'governed-dataset' }, name: 'Attached governed dataset',
      hierarchy: [], href: '', locations: [], context: [] }
    const draft = 'Keep this unsent Explorer question  '
    await drawer.evaluate((element: any, reference) => element.openWithReference(reference), reference)
    await input.fill(draft)
    await explorer.evaluate(async (element: any, { running, reference, draft }) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ dataExplorer: element.dataExplorer,
        agent: { status: { running, runId: running ? 'live-run' : 'previous-run', canContinue: !running, error: '' },
        composer: { value: draft } }, agentContext: { references: [reference] },
        agentReferenceSearch: { query: 'governed', requestId: 7, results: [reference] },
        agentVisuals: { retainedChart: {
          schemaVersion: 14, visualID: 'retainedChart', rendererID: 'echarts', specRevision: 'retained-spec', dataRevision: 1,
          spec: { kind: 'cartesian', datasets: [] },
          dataState: { kind: 'inline', datasets: [] }, selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
        } } })
      element.requestUpdate(); await element.updateComplete
      const trigger = document.createElement('button')
      trigger.id = 'reconnect-explorer'
      trigger.textContent = 'Reconnect Explorer stream'
      trigger.setAttribute('data-on:click', "@get('/updates?route=data&surface=explore', {retry: 'never', retryMaxCount: 0, openWhenHidden: true})")
      document.body.append(trigger)
    }, { running, reference, draft })
    await drawer.evaluate((element: any) => element.updateComplete)
    const before = await drawer.evaluate((element: any) => ({ agent: element.agent, visuals: element.visuals }))
    expect(await page.evaluate(() => (window as any).restoredConversations)).toEqual(['existing-conversation'])
    let reconnects = 0
    await page.route('**/updates?route=data&surface=explore**', route => {
      reconnects++
      // A new /updates connection replays missing-only chat defaults, then the
      // current governed Explorer context and authoritative conversation list.
      return route.fulfill({ contentType: 'text/event-stream', body:
        signalEvent({ agent: { conversations: [], activeConversationId: '', transcript: [],
          status: { enabled: true, running: false, runId: '', canContinue: false, error: '' },
          composer: { value: '', disabled: false, placeholder: '' } },
          agentVisuals: {}, agentReferenceSearch: { query: '', requestId: 0, results: [] },
          agentContext: { references: [] } }, true)
        + signalEvent({ dataExplorer: { warnings: [`Governed context revision ${reconnects}`] },
          agent: { conversations: [{ id: 'fresh-conversation', title: `Fresh conversation list ${reconnects}`,
            principalId: 'current-principal', status: 'ready', messageCount: 2, createdAt: '', updatedAt: '' }],
            status: { enabled: true } },
          agentContext: { surface: 'data', modelId: 'refreshed-model', datasetId: 'refreshed-dataset',
            exploration: { schemaVersion: 1, modelId: 'refreshed-model', datasetId: 'refreshed-dataset',
              dimensions: [], metrics: [], filters: [], sort: [], limit: 25 + reconnects } } }) })
    })
    for (const revision of [1, 2]) {
      await page.locator('#reconnect-explorer').evaluate((element: HTMLButtonElement) => element.click())
      await page.waitForFunction(revision => {
        const explorer = document.querySelector('lv-data-explorer') as any
        return explorer.dataExplorer.warnings[0] === `Governed context revision ${revision}`
          && explorer.shadowRoot.querySelector('lv-chat-drawer').context.exploration.limit === 25 + revision
      }, revision, { timeout: 5000 })
      await drawer.evaluate(async (element: any) => { await element.updateComplete; await element.shadowRoot.querySelector('lv-chat-composer').updateComplete })
      expect(await input.inputValue()).toBe(draft)
      expect(await composer.evaluate((element: any) => element.references)).toEqual([reference])
      const after = await drawer.evaluate((element: any) => ({ agent: element.agent, visuals: element.visuals, context: element.context }))
      expect(after.agent.activeConversationId).toBe('existing-conversation')
      expect(after.agent.transcript).toEqual(before.agent.transcript)
      expect(after.agent.status).toEqual(before.agent.status)
      expect(after.agent.composer).toEqual(before.agent.composer)
      expect(after.visuals).toEqual(before.visuals)
      expect(after.context.references).toEqual([reference])
      expect(after.context).toMatchObject({ modelId: 'refreshed-model', datasetId: 'refreshed-dataset' })
      expect(after.agent.conversations[0].title).toBe(`Fresh conversation list ${revision}`)
      expect(await explorer.evaluate((element: any) => element.signal('agentReferenceSearch', {}))).toEqual({ query: 'governed', requestId: 7, results: [reference] })
      expect(await drawer.getByText('Original question', { exact: true }).count()).toBe(1)
      expect(await page.evaluate(() => (window as any).restoredConversations)).toEqual(['existing-conversation'])
      if (running) expect(await composer.getByRole('button', { name: 'Stop response', exact: true }).isEnabled()).toBe(true)
    }
    expect(reconnects).toBe(2)
    expect(browserErrors).toEqual([])
    if (!running) {
      await input.press('Enter')
      await page.waitForFunction(() => (window as any).fetchFinished === 3)
      expect(turns).toHaveLength(1)
      expect(turns[0].agent.activeConversationId).toBe('existing-conversation')
      expect(turns[0].agent.composer.value).toBe(draft.trim())
      expect(turns[0].agentContext.references).toEqual([reference])
      expect(turns[0].agentContext.exploration).toMatchObject({ modelId: 'refreshed-model', datasetId: 'refreshed-dataset', limit: 27 })
    }
  } finally { await page.close() }
})

test('Explorer shows a real rejected agent command and preserves draft, references, and edit target for retry', async () => {
  const page = await browser.newPage({ viewport: { width: 768, height: 844 } })
  try {
    const requests: any[] = []
    await page.route('**/chats/turns', route => {
      requests.push(JSON.parse(route.request().postData()!))
      return requests.length === 1
        ? route.fulfill({ status: 403, contentType: 'text/plain', body: 'UI command operation identity is missing\n' })
        : route.fulfill({ contentType: 'text/event-stream', body: `event: datastar-patch-signals\ndata: signals ${JSON.stringify({
          agent: { status: { running: false, runId: null, error: null }, transcript: [
            { id: 'original-message', kind: 'user', text: 'Explain governed revenue', runId: 'edited-run' },
            { id: 'edited-answer', kind: 'assistant', text: 'The governed result is ready.', runId: 'edited-run' },
          ] },
        })}\n\n` })
    })
    const explorer = await openTransportAgent(page, true)
    const drawer = explorer.locator('lv-chat-drawer'), composer = drawer.locator('lv-chat-composer')
    await drawer.locator('lv-chat-thread').evaluate(element => element.dispatchEvent(new CustomEvent('lv-chat-reuse', {
      bubbles: true, composed: true, detail: { text: 'Explain governed revenue', editMessageId: 'original-message',
        references: [{ reference: { kind: 'dataset', id: 'governed-dataset' }, name: 'Governed dataset' }] } })))
    await composer.getByRole('combobox').press('Enter')
    await page.waitForFunction(() => (window as any).fetchFinished === 1)
    if (process.env.LEAPVIEW_EXPLORER_FEEDBACK_SCREENSHOT_DIR) await page.screenshot({
      path: `${process.env.LEAPVIEW_EXPLORER_FEEDBACK_SCREENSHOT_DIR}/http403-edit-recovery.png`, fullPage: true })
    expect(await drawer.getByRole('alert').filter({ hasText: 'Sending your message is not permitted' }).count()).toBe(1)
    expect(await composer.getByRole('combobox').inputValue()).toBe('Explain governed revenue')
    expect(await composer.getByRole('button', { name: 'Save & send', exact: true }).isEnabled()).toBe(true)
    expect(await composer.evaluate((element: any) => ({ edit: element.editMessageId, references: element.references })))
      .toMatchObject({ edit: 'original-message', references: [{ reference: { kind: 'dataset', id: 'governed-dataset' } }] })
    await composer.getByRole('combobox').press('Enter')
    await page.waitForFunction(() => (window as any).fetchFinished === 2)
    expect(requests).toHaveLength(2)
    expect(requests[1].agent.composer).toMatchObject({ value: 'Explain governed revenue', editMessageId: 'original-message' })
    expect(requests[1].agentContext.references[0].reference.id).toBe('governed-dataset')
    expect(await drawer.getByRole('alert').count()).toBe(0)
    expect(await drawer.getByText('The governed result is ready.', { exact: true }).count()).toBe(1)
    await explorer.evaluate(element => document.dispatchEvent(new CustomEvent('datastar-fetch', {
      detail: { type: 'retries-failed', el: element, argsRaw: {} } })))
    expect(await drawer.getByRole('alert').count()).toBe(0)
  } finally { await page.close() }
})

test('Explorer agent owns network recovery and disarms it for accepted and completed turns', async () => {
  const page = await browser.newPage()
  try {
    const explorer = await openTransportAgent(page)
    const drawer = explorer.locator('lv-chat-drawer'), textarea = drawer.locator('lv-chat-composer textarea')
    await explorer.evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ agent: { activeConversationId: '', transcript: [] } })
      element.requestUpdate(); await element.updateComplete
    })
    expect(await drawer.getByRole('region', { name: 'Start a data conversation' }).count()).toBe(1)
    await textarea.fill('Keep this network draft')
    await textarea.press('Enter')
    await explorer.evaluate(async (element: any) => {
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retries-failed', el: document.body, argsRaw: {} } }))
      await element.updateComplete
    })
    expect(await drawer.getByRole('alert').count()).toBe(0)
    await explorer.evaluate(async (element: any) => {
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retries-failed', el: element, argsRaw: {} } }))
      await element.updateComplete
    })
    expect(await drawer.getByRole('alert').filter({ hasText: 'check the connection and retry' }).count()).toBe(1)
    expect(await textarea.inputValue()).toBe('Keep this network draft')
    expect(await textarea.isEnabled()).toBe(true)
    await drawer.getByRole('button', { name: 'New chat', exact: true }).click()
    expect(await drawer.getByRole('alert').count()).toBe(0)
    for (const acceptance of ['running', 'completed', 'transcript']) {
      await textarea.fill(`Accept via ${acceptance}`)
      await textarea.press('Enter')
      await explorer.evaluate(async (element: any, acceptance) => {
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
        const patch = acceptance === 'running' ? { status: { running: true, runId: 'running-run' } }
          : acceptance === 'completed' ? { status: { running: false, runId: 'completed-run' } }
          : { transcript: [{ id: 'durable-user', kind: 'user', text: 'Accepted question', runId: 'durable-run' }] }
        mergePatch({ agent: patch })
        // Acceptance may arrive on /updates before Lit renders the command failure.
        document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: '403' } } }))
        element.requestUpdate(); await element.updateComplete
      }, acceptance)
      expect(await drawer.getByRole('alert').count()).toBe(0)
      await explorer.evaluate(async (element: any) => {
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
        mergePatch({ agent: { status: { running: false } } })
        element.requestUpdate(); await element.updateComplete
        document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retries-failed', el: element, argsRaw: {} } }))
      })
      expect(await drawer.getByRole('alert').count()).toBe(0)
    }
  } finally { await page.close() }
})


test('overlapping exploration and agent commands both recover from an ambiguous owned failure', async () => {
  const page = await browser.newPage()
  try {
    const explorer = await openTransportAgent(page)
    await explorer.evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const spec = { schemaVersion: 1, modelId: 'governed-model', datasetId: 'governed-dataset',
        dimensions: [], metrics: [{ field: 'revenue' }], filters: [], sort: [], limit: 10 }
      const command = { spec, semanticModelId: spec.modelId, datasetId: spec.datasetId, dimensions: [],
        metrics: ['revenue'], filters: [], sort: [], limit: 10, requestSeq: 0, resetVersion: 0, columnWidths: {} }
      const object = { key: 'model:orders', resourceId: 'model:orders', layer: 'model', title: 'Orders',
        semanticModelId: spec.modelId, datasetId: spec.datasetId, columns: [], columnCount: 0 }
      mergePatch({ dataExplorer: { objects: [object], selectedKey: object.key, selectedObject: object,
        command: { mode: 'explore', objectKey: object.key, explore: command }, warnings: [],
        preview: { columns: [], blocks: {}, sort: {} }, explore: { command, semanticModels: [], datasets: [],
          fields: [{ id: 'revenue', label: 'Revenue', kind: 'metric', datasetId: spec.datasetId, compatible: true, selected: true }],
          result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, warnings: [] },
          status: { state: 'idle', loading: false, stale: false, requestSeq: 0 } } } })
      element.requestUpdate(); await element.updateComplete
    })
    await explorer.getByRole('button', { name: 'Run', exact: true }).click()
    const drawer = explorer.locator('lv-chat-drawer'), textarea = drawer.locator('lv-chat-composer textarea')
    await textarea.fill('Explain this pending exploration')
    await textarea.press('Enter')
    await explorer.evaluate(async (element: any) => {
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      await element.updateComplete
    })
    expect(await drawer.getByRole('alert').filter({ hasText: 'Sending your message' }).count()).toBe(1)
    expect(await textarea.isEnabled()).toBe(true)
    expect(await textarea.inputValue()).toBe('Explain this pending exploration')
    expect(await explorer.locator('.execution-state').textContent()).toContain('outcome is unknown')
    expect(await explorer.getByRole('button', { name: 'Run latest', exact: true }).isEnabled()).toBe(true)
    expect(await explorer.getByRole('button', { name: 'Stop', exact: true }).isEnabled()).toBe(true)
    await explorer.evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ agent: { status: { error: 'The server rejected the governed context.' } } })
      element.requestUpdate(); await element.updateComplete; await element.updateComplete
    })
    expect(await drawer.getByRole('alert').filter({ hasText: 'Sending your message' }).count()).toBe(0)
    expect(await drawer.getByText('The server rejected the governed context.', { exact: true }).count()).toBe(1)
  } finally { await page.close() }
})

for (const width of [375, 768, 1440]) {
  test(`Explorer agent keeps the actual inspector clear of Send at ${width}px and restores it when unavailable`, async () => {
    const page = await browser.newPage({ viewport: { width, height: 900 } })
    try {
      await page.goto(baseURL)
      await page.addStyleTag({ content: appCSS })
      await page.addScriptTag({ type: 'module', content: inspectorModule })
      await page.waitForFunction(() => customElements.get('datastar-inspector'))
      await page.evaluate(async () => {
        const modulePath = '/static/vendor/datastar-1.0.2.js?v=dev'
        const { mergePatch } = await import(modulePath) as any
        mergePatch({ page: { kind: 'data', title: 'Data Explorer', tabs: [] },
          agent: { conversations: [], activeConversationId: '', transcript: [],
            status: { enabled: true, running: false }, composer: { value: '', disabled: false } },
          agentContext: { surface: 'data', modelId: 'semantic:sales', datasetId: 'orders', references: [],
            exploration: { schemaVersion: 1, modelId: 'semantic:sales', datasetId: 'orders',
              dimensions: [], metrics: [], filters: [], sort: [], limit: 100 } },
        })
        const explorer = document.createElement('lv-data-explorer') as any
        explorer.style.height = '100dvh'
        explorer.addEventListener('lv-chat-submit', (event: CustomEvent) => {
          (window as any).__inspectorAgentSubmission = event.detail.input
        })
        document.body.append(explorer, document.createElement('datastar-inspector'))
        await explorer.updateComplete
      })
      const explorer = page.locator('lv-data-explorer')
      const inspector = page.locator('datastar-inspector')
      const inspectorVisible = () => inspector.evaluate((element) => getComputedStyle(element).display !== 'none')
      expect(await inspectorVisible()).toBe(true)
      await explorer.getByRole('button', { name: 'Ask about this data', exact: true }).click()
      const drawer = explorer.locator('lv-chat-drawer')
      const composer = drawer.locator('lv-chat-composer')
      await composer.locator('textarea').fill('Preserved Explorer draft')
      expect(await inspectorVisible()).toBe(false)
      expect(await explorer.getAttribute('data-agent-open')).toBe('')
      expect(await composer.locator('textarea').inputValue()).toBe('Preserved Explorer draft')
      const send = composer.getByRole('button', { name: 'Send', exact: true })
      await send.click({ timeout: 2_000 })
      expect(await page.evaluate(() => (window as any).__inspectorAgentSubmission)).toBe('Preserved Explorer draft')
      const context = await drawer.evaluate((element: any) => element.context)
      expect(context.modelId).toBe('semantic:sales')
      expect(context.datasetId).toBe('orders')
      await page.evaluate(async () => {
        const modulePath = '/static/vendor/datastar-1.0.2.js?v=dev'
        const { mergePatch } = await import(modulePath) as any
        mergePatch({ agent: { status: { enabled: false } } })
        await (document.querySelector('lv-data-explorer') as any).updateComplete
      })
      expect(await inspectorVisible()).toBe(true)
      expect(await explorer.getAttribute('data-agent-open')).toBeNull()
      await page.evaluate(async () => {
        const modulePath = '/static/vendor/datastar-1.0.2.js?v=dev'
        const { mergePatch } = await import(modulePath) as any
        mergePatch({ agent: { status: { enabled: true } } })
        await (document.querySelector('lv-data-explorer') as any).updateComplete
      })
      expect(await inspectorVisible()).toBe(false)
      await drawer.getByRole('button', { name: 'Close agent', exact: true }).click()
      expect(await inspectorVisible()).toBe(true)
      expect(await explorer.getAttribute('data-agent-open')).toBeNull()
      await explorer.getByRole('button', { name: 'Ask about this data', exact: true }).click()
      expect(await inspectorVisible()).toBe(false)
      await page.evaluate(async () => {
        const modulePath = '/static/vendor/datastar-1.0.2.js?v=dev'
        const { mergePatch } = await import(modulePath) as any
        mergePatch({ agent: null })
        await (document.querySelector('lv-data-explorer') as any).updateComplete
      })
      expect(await inspectorVisible()).toBe(true)
      expect(await explorer.getAttribute('data-agent-open')).toBeNull()
    } finally {
      await page.close()
    }
  }, 15_000)
}


async function styledFocus(page: Page) {
  return page.evaluate(() => {
    let focused = document.activeElement as HTMLElement | null
    while (focused?.shadowRoot?.activeElement) focused = focused.shadowRoot.activeElement as HTMLElement
    const rect = focused?.getBoundingClientRect()
    let inside = false
    for (let node: Node | null = focused; node; node = node.parentNode ?? (node.getRootNode() as ShadowRoot).host ?? null) {
      if (node instanceof Element && node.localName === 'lv-chat-drawer') inside = true
    }
    let hit = rect ? document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2) : null
    while (hit?.shadowRoot) {
      const next = hit.shadowRoot.elementFromPoint(rect!.x + rect!.width / 2, rect!.y + rect!.height / 2)
      if (!next || next === hit) break
      hit = next
    }
    let reachable = false
    for (let node: Node | null = hit; node; node = node.parentNode ?? (node.getRootNode() as ShadowRoot).host ?? null) {
      if (node === focused) reachable = true
    }
    return { inside, reachable, tag: focused?.localName, label: focused?.getAttribute('aria-label'), visible: Boolean(rect && rect.width > 0 && rect.height > 0
      && rect.x >= 0 && rect.y >= 0 && rect.right <= innerWidth && rect.bottom <= innerHeight) }
  })
}

for (const width of [375, 1440]) test(`styled Explorer preserves draft and references across Close and Escape at ${width}px`, async () => {
  const page = await browser.newPage({ viewport: { width, height: 900 } })
  try {
    const explorer = await openTransportAgent(page, false, true)
    const drawer = explorer.locator('lv-chat-drawer'), input = drawer.locator('lv-chat-composer textarea')
    const draft = 'Unsent governed question with trailing spaces  '
    await drawer.evaluate((element: any) => element.openWithReference({ reference: { kind: 'dataset', id: 'governed-dataset' }, name: 'Governed dataset' }))
    await input.fill(draft)
    for (const close of ['button', 'escape']) {
      if (close === 'button') await drawer.getByRole('button', { name: 'Close agent', exact: true }).click()
      else await input.press('Escape')
      await explorer.getByRole('button', { name: 'Ask about this data', exact: true }).click()
      expect(await input.inputValue()).toBe(draft)
      expect(await drawer.locator('lv-chat-composer').evaluate((element: any) => element.references.map((item: any) => item.reference.id))).toEqual(['governed-dataset'])
      expect((await styledFocus(page)).inside).toBe(true)
    }
    await drawer.getByRole('button', { name: 'New chat', exact: true }).click()
    expect(await input.inputValue()).toBe('')
    expect(await drawer.locator('lv-chat-composer').evaluate((element: any) => element.references)).toEqual([])
  } finally { await page.close() }
})

for (const width of [375, 768, 1440]) test(`styled covering Explorer chat contains composed keyboard focus and restores alongside mode at ${width}px`, async () => {
  const page = await browser.newPage({ viewport: { width, height: 900 } })
  try {
    const explorer = await openTransportAgent(page, false, true)
    const drawer = explorer.locator('lv-chat-drawer'), input = drawer.locator('lv-chat-composer textarea')
    const first = drawer.getByRole('button', { name: 'New chat', exact: true })
    const last = drawer.getByRole('button', { name: 'Send', exact: true })
    await input.fill('Preserve this responsive draft')
    if (width === 1440) {
      await first.focus(); await page.keyboard.press('Shift+Tab')
      expect(await styledFocus(page)).toMatchObject({ inside: false, visible: true, reachable: true })
      await drawer.getByRole('button', { name: 'Expand chat', exact: true }).click()
    }
    await last.focus(); await page.keyboard.press('Tab')
    expect(await styledFocus(page)).toMatchObject({ inside: true, label: 'New chat', visible: true, reachable: true })
    await page.keyboard.press('Shift+Tab')
    expect(await styledFocus(page)).toMatchObject({ inside: true, label: 'Send', visible: true, reachable: true })
    expect(await drawer.getByRole('dialog').getAttribute('aria-modal')).toBe('true')
    if (width !== 1440) {
      await page.setViewportSize({ width: 1440, height: 900 })
      await page.waitForFunction(() => document.querySelector('lv-data-explorer')?.shadowRoot?.querySelector('lv-chat-drawer')?.shadowRoot?.querySelector('aside')?.getAttribute('aria-modal') === 'false')
      await first.focus(); await page.keyboard.press('Shift+Tab')
      expect(await styledFocus(page)).toMatchObject({ inside: false, visible: true, reachable: true })
      await page.setViewportSize({ width, height: 900 })
      await drawer.getByRole('button', { name: 'Expand chat', exact: true }).click()
    }
    await drawer.getByRole('button', { name: 'Close agent', exact: true }).click()
    expect(await drawer.getByRole('dialog').isVisible()).toBe(true)
    expect(await input.inputValue()).toBe('Preserve this responsive draft')
    expect(await drawer.getByRole('button', { name: 'Expand chat', exact: true }).count()).toBe(1)
    await drawer.getByRole('button', { name: 'Close agent', exact: true }).click()
    expect(await styledFocus(page)).toMatchObject({ inside: false, label: 'Ask about this data', visible: true, reachable: true })
  } finally { await page.close() }
})


for (const width of [375, 1440]) test(`expanded Explorer URL and history keep parent and drawer open state consistent at ${width}px`, async () => {
  const page = await browser.newPage({ viewport: { width, height: 900 } })
  try {
    const explorer = await openTransportAgent(page, false, true, true)
    const drawer = explorer.locator('lv-chat-drawer'), input = drawer.locator('lv-chat-composer textarea')
    expect(await drawer.getByRole('dialog').getAttribute('aria-modal')).toBe('true')
    expect(await explorer.getAttribute('data-agent-open')).toBe('')
    await input.fill('Retain this history draft')
    await drawer.getByRole('button', { name: 'Close agent', exact: true }).click()
    expect(await drawer.getByRole('dialog').isVisible()).toBe(true)
    expect(new URL(page.url()).searchParams.get('chat')).toBeNull()
    await drawer.getByRole('button', { name: 'Close agent', exact: true }).click()
    expect(await drawer.isVisible()).toBe(false)
    expect(await explorer.getAttribute('data-agent-open')).toBeNull()
    await page.evaluate(() => {
      const url = new URL(location.href)
      url.searchParams.set('chat', 'expanded')
      history.pushState(null, '', url)
      window.dispatchEvent(new PopStateEvent('popstate'))
    })
    await drawer.getByRole('dialog').waitFor({ state: 'visible' })
    expect(await explorer.getAttribute('data-agent-open')).toBe('')
    expect(await drawer.getByRole('dialog').getAttribute('aria-modal')).toBe('true')
    expect(await input.inputValue()).toBe('Retain this history draft')
  } finally { await page.close() }
})

for (const width of [375, 768, 1440]) for (const action of ['Close', 'Escape']) {
  test(`automatically restored Explorer chat returns focus to Ask after ${action} at ${width}px`, async () => {
    const page = await browser.newPage({ viewport: { width, height: 900 } })
    try {
      await page.addInitScript(() => localStorage.setItem('leapview-data-explorer-agent-state', JSON.stringify({ open: true, conversationId: 'existing-conversation' })))
      const explorer = await openTransportAgent(page, false, true)
      const drawer = explorer.locator('lv-chat-drawer')
      expect(await explorer.getByRole('button', { name: 'Ask about this data', exact: true }).getAttribute('aria-expanded')).toBe('true')
      if (action === 'Close') await drawer.getByRole('button', { name: 'Close agent', exact: true }).click()
      else await drawer.locator('lv-chat-composer textarea').press('Escape')
      await drawer.evaluate((element: any) => element.updateComplete)
      await browserExpect(explorer.getByRole('button', { name: 'Ask about this data', exact: true })).toBeFocused()
      if (process.env.LEAPVIEW_EXPLORER_FEEDBACK_SCREENSHOT_DIR) await page.screenshot({
        path: `${process.env.LEAPVIEW_EXPLORER_FEEDBACK_SCREENSHOT_DIR}/restored-${action.toLowerCase()}-${width}.png` })
      expect(await styledFocus(page)).toMatchObject({ inside: false, label: 'Ask about this data', visible: true, reachable: true })
    } finally { await page.close() }
  }, 15_000)
}

for (const targetTag of ['button', 'h2']) test(`automatically restored Explorer chat preserves a valid ${targetTag} focus return target`, async () => {
  const page = await browser.newPage({ viewport: { width: 375, height: 900 } })
  try {
    await page.addInitScript((targetTag) => {
      localStorage.setItem('leapview-data-explorer-agent-state', JSON.stringify({ open: true }))
      document.addEventListener('DOMContentLoaded', () => {
        const target = document.createElement(targetTag)
        if (targetTag === 'h2') target.tabIndex = -1
        target.textContent = 'Original focus target'; target.id = 'original-focus-target'
        document.body.append(target); target.focus()
      })
    }, targetTag)
    const explorer = await openTransportAgent(page, false, true)
    await explorer.locator('lv-chat-drawer').getByRole('button', { name: 'Close agent', exact: true }).click()
    await explorer.locator('lv-chat-drawer').evaluate((element: any) => element.updateComplete)
    expect(await page.locator('#original-focus-target').evaluate(element => document.activeElement === element)).toBe(true)
  } finally { await page.close() }
})

for (const transition of ['reopen', 'disable']) test(`restored Explorer close focus does not override an immediate ${transition}`, async () => {
  const page = await browser.newPage({ viewport: { width: 375, height: 900 } })
  try {
    await page.addInitScript(() => localStorage.setItem('leapview-data-explorer-agent-state', JSON.stringify({ open: true })))
    const explorer = await openTransportAgent(page, false, true)
    await explorer.evaluate(async (element: any, transition) => {
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer')
      drawer.dispatchEvent(new CustomEvent('lv-chat-drawer-close', { bubbles: true, composed: true }))
      if (transition === 'reopen') element.shadowRoot.querySelector('.ask-button').click()
      else {
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
        mergePatch({ agent: { status: { enabled: false } } })
      }
      await element.updateComplete; await drawer.updateComplete
    }, transition)
    if (transition === 'reopen') expect(await styledFocus(page)).toMatchObject({ inside: true, visible: true, reachable: true })
    else expect(await explorer.getByRole('button', { name: 'Ask about this data', exact: true }).evaluate(element => element.getRootNode() instanceof ShadowRoot && (element.getRootNode() as ShadowRoot).activeElement === element)).toBe(false)
  } finally { await page.close() }
})
