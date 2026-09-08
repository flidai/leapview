import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const assetRoot = join(projectRoot, '.tmp/data-explorer-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const requestURL = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (requestURL.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(requestURL.searchParams.get('case') ?? 'data'))
      return
    }
    const fileRoot = requestURL.pathname.startsWith('/static/vendor/') ? projectRoot : assetRoot
    const file = normalize(join(fileRoot, requestURL.pathname))
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
  if (!address || typeof address === 'string') throw new Error('test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  if (!server?.listening) return
  await new Promise<void>((resolve, reject) => {
    server.close((error) => error && (error as NodeJS.ErrnoException).code !== 'ERR_SERVER_NOT_RUNNING' ? reject(error) : resolve())
    server.closeIdleConnections()
  })
}, 15_000)

test('data explorer agent context exposes the current canonical query without a dashboard return', async () => {
  const page = await browser.newPage()
  await page.addInitScript(() => localStorage.clear())
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const href = await page.locator('lv-data-explorer').evaluate(async (element: any) => {
      await element.updateComplete
      const ask = element.shadowRoot.querySelector('.ask-button') as HTMLButtonElement | null
      if (!ask) return null
      ask.click()
      await element.updateComplete
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      if (!drawer) return null
      await drawer.updateComplete
      return drawer.shadowRoot.querySelector('.context-explore')?.getAttribute('href') ?? null
    })
    expect(href).toBeTruthy()
    const params = new URLSearchParams(href!.split('?')[1])
    expect(params.get('v')).toBe('2')
    expect(params.get('mode')).toBe('explore')
    expect(params.get('returnSurface')).toBeNull()
    expect(params.get('returnDashboard')).toBeNull()
    expect(params.get('returnPage')).toBeNull()
    expect(JSON.parse(params.get('state')!)).toEqual({
      schemaVersion: 1,
      modelId: 'commerce',
      datasetId: 'orders',
      dimensions: [{ field: 'orders.status' }],
      metrics: [{ field: 'order_count' }],
      filters: [],
      sort: [],
      limit: 100,
    })
  } finally {
    await page.close()
  }
})

test('dashboard-shaped context without canonical exploration does not expose an explore link', async () => {
  const page = await browser.newPage()
  await page.addInitScript(() => localStorage.clear())
  try {
    await page.goto(`${baseURL}/?case=dashboard-without-exploration`)
    await page.waitForFunction(() => customElements.get('lv-chat-drawer'))
    const link = await page.locator('lv-chat-drawer').evaluate(async (element: any) => {
      await element.updateComplete
      return element.shadowRoot.querySelector('.context-explore')
    })
    expect(link).toBeNull()
  } finally {
    await page.close()
  }
})

test('command patches replace the chat explore link with the fresh canonical query', async () => {
  const page = await browser.newPage()
  await page.addInitScript(() => localStorage.clear())
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const links = await page.locator('lv-data-explorer').evaluate(async (element: any) => {
      await element.updateComplete
      element.shadowRoot.querySelector('.ask-button')?.click()
      await element.updateComplete
      const drawer = element.shadowRoot.querySelector('lv-chat-drawer') as any
      await drawer.updateComplete
      const before = drawer.shadowRoot.querySelector('.context-explore')?.getAttribute('href') ?? null
      const nextSpec = {
        schemaVersion: 1,
        modelId: 'commerce',
        datasetId: 'orders',
        dimensions: [{ field: 'orders.created_at' }],
        metrics: [{ field: 'revenue' }],
        filters: [],
        sort: [{ field: 'revenue', direction: 'desc' }],
        limit: 25,
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      // This is the signal patch emitted by /explore/command. The context is
      // replaced from the same server projection as dataExplorer, rather than
      // being reconstructed from the optimistic browser command.
      mergePatch({
        agentContext: {
          surface: 'data', dashboardId: '', dashboardTitle: '', pageId: '', pageTitle: '',
          modelId: 'commerce', datasetId: 'orders', exploration: nextSpec, generation: 0,
          filters: { revision: 0, appliedControls: {}, draftControls: {}, dirtyBindings: [], defaultsRevision: '' },
          references: [], referenceLimit: 12,
        },
      })
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      await drawer.updateComplete
      return { before, after: drawer.shadowRoot.querySelector('.context-explore')?.getAttribute('href') ?? null }
    })
    expect(links.before).toBeTruthy()
    expect(links.after).toBeTruthy()
    expect(links.after).not.toBe(links.before)
    const params = new URLSearchParams(links.after!.split('?')[1])
    expect(params.get('returnSurface')).toBeNull()
    expect(params.get('returnDashboard')).toBeNull()
    expect(params.get('returnPage')).toBeNull()
    expect(JSON.parse(params.get('state')!)).toEqual({
      schemaVersion: 1,
      modelId: 'commerce',
      datasetId: 'orders',
      dimensions: [{ field: 'orders.created_at' }],
      metrics: [{ field: 'revenue' }],
      filters: [],
      sort: [{ field: 'revenue', direction: 'desc' }],
      limit: 25,
    })
  } finally {
    await page.close()
  }
})

test('query edits and saved metadata preserve the closed chat return link', async () => {
  const page = await browser.newPage()
  await page.addInitScript(() => localStorage.clear())
  try {
    await page.goto(`${baseURL}/?returnSurface=chat&returnConversation=conversation%3Asales`)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const state = await page.locator('lv-data-explorer').evaluate(async (element: any) => {
      await element.updateComplete
      const initialURL = window.location.href
      element.emitExplore({ limit: 25 }, undefined, undefined, true)
      const afterEditURL = window.location.href
      const optimistic = element.optimisticExplore
      if (!optimistic) throw new Error('query edit did not create an optimistic command')
      const nextTopCommand = {
        ...element.dataExplorer.command,
        action: 'configure', mode: 'explore', explore: optimistic,
        requestSeq: optimistic.requestSeq, resetVersion: optimistic.resetVersion,
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      // Complete the command with the canonical explorer projection, as the
      // mounted /explore/command response does. This releases the optimistic
      // URL before the subsequent saved metadata patch.
      mergePatch({ dataExplorer: { command: nextTopCommand, explore: { command: optimistic } } })
      for (let index = 0; index < 10; index += 1) {
        await element.updateComplete
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      }
      mergePatch({ savedExplorations: {
        enabled: true,
        list: { items: [], includeArchived: false, selectedId: 'exploration:sales' },
        command: { action: 'create' }, save: { state: 'saved' },
      } })
      for (let index = 0; index < 10; index += 1) {
        await element.updateComplete
        await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      }
      return {
        initialURL,
        afterEditURL,
        afterSaveURL: window.location.href,
        returnHref: element.shadowRoot.querySelector('.return-link')?.getAttribute('href') ?? null,
      }
    })
    for (const href of [state.initialURL, state.afterEditURL, state.afterSaveURL]) {
      const params = new URL(href).searchParams
      expect(params.get('returnSurface')).toBe('chat')
      expect(params.get('returnConversation')).toBe('conversation:sales')
      expect(params.has('returnURL')).toBe(false)
    }
    expect(new URL(state.afterEditURL).searchParams.get('state')).toContain('"limit":25')
    expect(new URL(state.afterSaveURL).searchParams.get('saved')).toBe('exploration:sales')
    expect(new URL(state.afterSaveURL).searchParams.get('state')).toContain('"limit":25')
    expect(state.returnHref).toBe('/chats/conversation%3Asales')
  } finally {
    await page.close()
  }
})

function testDocument(kind: string): string {
  const dataSpec = {
    schemaVersion: 1,
    modelId: 'commerce',
    datasetId: 'orders',
    dimensions: [{ field: 'orders.status' }],
    metrics: [{ field: 'order_count' }],
    filters: [],
    sort: [],
    limit: 100,
  }
  const dataCommand = { action: 'configure', spec: dataSpec, requestSeq: 0, resetVersion: 0, columnWidths: {} }
  const dashboardContext = {
    surface: 'dashboard', dashboardId: 'executive-sales', dashboardTitle: 'Executive Sales Dashboard',
    pageId: 'overview', pageTitle: 'Overview', modelId: 'commerce', generation: 3,
    filters: emptyFilterState(), references: [],
  }
  const dataContext = {
    surface: 'data', dashboardId: '', dashboardTitle: '', pageId: '', pageTitle: 'Data Explorer',
    modelId: 'commerce', datasetId: 'orders', exploration: dataSpec, generation: 0,
    filters: emptyFilterState(), references: [],
  }
  const signals = {
    agent: {
      conversations: [], activeConversationId: '', transcript: [],
      status: { enabled: true, running: false },
      composer: { value: '', disabled: false, placeholder: 'Ask about this data…' },
    },
    agentContext: kind === 'dashboard-without-exploration' ? dashboardContext : dataContext,
    agentReferenceSearch: { query: '', requestId: 0, results: [] },
    agentVisuals: {},
    page: { kind: 'data', title: 'Data Explorer', description: 'Inspect governed semantic data.', selectedObject: '', tabs: [], context: { active: true, environment: 'production', generationId: 'generation-1', objectCount: 1, projectId: 'sales' } },
    dataExplorer: {
      objects: [], selectedKey: '',
      command: { mode: 'explore', explore: dataCommand, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
      explore: {
        command: dataCommand, views: {}, recommendedView: 'table', defaultView: 'table', semanticModels: [], datasets: [], fields: [],
        result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] },
        status: { loading: false, stale: false, requestSeq: 0, state: 'idle' },
      },
      preview: { columns: [], totalRows: 0, availableRows: 0, chunkSize: 100, rowHeight: 32, resetVersion: 0, blocks: {}, loading: false, stale: false, sort: {}, totalRowLabel: 'Unknown' },
      warnings: [],
    },
    dataExplorerDashboard: { enabled: false, targets: [] },
    savedExplorations: { enabled: false, list: { items: [], includeArchived: false }, command: { action: 'create' }, save: { state: 'saved' } },
    filterState: emptyFilterState(), interactionSelections: [],
  }
  const encoded = JSON.stringify(signals).replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
  const body = kind === 'dashboard-without-exploration'
    ? '<lv-chat-drawer open></lv-chat-drawer>'
    : '<lv-data-explorer></lv-data-explorer>'
  return `<!doctype html><html><body><main data-signals="${encoded}">${body}</main><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script><script type="module" src="/data-explorer-under-test.js"></script></body></html>`
}

function emptyFilterState() {
  return { revision: 0, appliedControls: {}, draftControls: {}, dirtyBindings: [], defaultsRevision: '' }
}
