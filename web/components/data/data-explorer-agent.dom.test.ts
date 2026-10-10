import { afterAll, beforeAll, expect, test } from 'bun:test'
import type { Browser } from '@playwright/test'
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

