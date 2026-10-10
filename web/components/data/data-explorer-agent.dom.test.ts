import { afterAll, beforeAll, expect, test } from 'bun:test'
import type { Browser, Page } from '@playwright/test'
import { createDataExplorerDOMFixture } from './data-explorer.test-fixture'

let fixture: Awaited<ReturnType<typeof createDataExplorerDOMFixture>>
let baseURL = ''
let browser: Browser

beforeAll(async () => {
  fixture = await createDataExplorerDOMFixture()
  baseURL = fixture.baseURL
  browser = fixture.browser
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
    expect(await explorer.locator('lv-chat-drawer').count()).toBe(0)
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
      expect(await explorer.locator('lv-chat-drawer').count()).toBe(0)
      expect((await geometry()).headerWidth).toBe(closed.headerWidth)
      expect(await ask.getAttribute('aria-expanded')).toBe('false')
      await ask.click()
      await explorer.locator('lv-chat-drawer').getByRole('button', { name: 'Close agent', exact: true }).press('Escape')
      await explorer.evaluate((element: any) => element.updateComplete)
      expect(await explorer.locator('lv-chat-drawer').count()).toBe(0)
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



async function openTransportAgent(page: Page, command = false) {
  if (command) await page.route(baseURL + '/', route => route.fulfill({ contentType: 'text/html', body: `
    <!doctype html><main data-signals="{}"></main>
    <lv-data-explorer data-indicator="agentTurnPending" data-on:lv-chat-submit="$agent.composer.value = evt.detail.input; $agent.composer.editMessageId = evt.detail.editMessageId || ''; $agentContext.references = evt.detail.references; @post('/chats/turns', {retry: 'never', retryMaxCount: 0, openWhenHidden: true})"></lv-data-explorer>
    <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
    <script type="module" src="/data-explorer-under-test.js"></script>` }))
  await page.goto(baseURL)
  await page.waitForFunction(() => customElements.get('lv-data-explorer'))
  await page.evaluate(async () => {
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
    if (!element.isConnected) document.body.append(element)
    element.requestUpdate(); await element.updateComplete
    ;(window as any).fetchFinished = 0
    document.addEventListener('datastar-fetch', event => { if ((event as CustomEvent).detail.type === 'finished') (window as any).fetchFinished++ })
  })
  const explorer = page.locator('lv-data-explorer')
  await explorer.getByRole('button', { name: 'Ask about this data', exact: true }).click()
  await explorer.locator('lv-chat-drawer lv-chat-composer textarea').waitFor()
  return explorer
}

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
