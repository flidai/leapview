import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser
const capturedAppendBodies: string[] = []
const projectRoot = process.cwd()
const assetRoot = join(projectRoot, '.tmp/data-explorer-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const requestURL = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (requestURL.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    if (requestURL.pathname === '/append-body-check') {
      let body = ''
      request.setEncoding('utf8')
      for await (const chunk of request) body += chunk
      capturedAppendBodies.push(body)
      response.setHeader('content-type', 'application/json')
      response.end('{}')
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
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
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

test('dashboard picker keeps the exploration while offering an explicit fork and refresh', async () => {
  const page = await browser.newPage({ viewport: { width: 1200, height: 800 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer-dashboard-picker'))
    const state = await page.evaluate(async () => {
      const picker = document.createElement('lv-data-explorer-dashboard-picker') as any
      picker.state = {
        enabled: true,
        targets: [{
          id: 'dashboard:authored', title: 'Authored Sales', semanticModelId: 'model:sales',
          draftId: 'draft:one', revisionToken: 'opaque-cas',
          pages: [{ id: 'overview', title: 'Overview', placement: { col: 1, colSpan: 6, row: 1, rowSpan: 4 } }],
        }],
        forkTargets: [{ id: 'dashboard:project', title: 'Project Sales', forkHref: '/dashboards/dashboard:project/fork' }],
      }
      picker.spec = {
        schemaVersion: 1, modelId: 'model:sales', dimensions: [{ field: 'orders.status' }],
        metrics: [{ field: 'orders.revenue' }], filters: [], sort: [], limit: 100,
      }
      let refresh: any
      let append: any
      picker.addEventListener('lv-data-explorer-dashboard-refresh', (event: CustomEvent) => { refresh = event.detail })
      picker.addEventListener('lv-data-explorer-add-to-dashboard', (event: CustomEvent) => { append = event.detail })
      document.body.append(picker)
      await picker.updateComplete
      const shadow = () => picker.shadowRoot as ShadowRoot
      const button = (label: string) => Array.from(shadow().querySelectorAll('button')).find((candidate) => candidate.textContent?.includes(label)) as HTMLButtonElement
      const targetBridge = document.createElement('span')
      targetBridge.setAttribute('data-lv-dashboard-action', 'target')
      const refreshBridge = document.createElement('span')
      refreshBridge.setAttribute('data-lv-dashboard-action', 'refresh')
      document.body.append(targetBridge, refreshBridge)
      button('Add to dashboard').click()
      await picker.updateComplete
      picker.state = { ...picker.state, state: 'ready' }
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'datastar-patch-signals', el: targetBridge } }))
      await picker.updateComplete
      await picker.updateComplete
      const fork = shadow().querySelector<HTMLAnchorElement>('[data-dashboard-fork]')
      button('Refresh targets').click()
      await picker.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'datastar-patch-signals', el: refreshBridge } }))
      picker.state = { ...picker.state, state: 'ready' }
      await picker.updateComplete
      await picker.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'datastar-patch-signals', el: targetBridge } }))
      picker.state = { ...picker.state, state: 'ready' }
      await picker.updateComplete
      await picker.updateComplete
      button('Add visual').click()
      await picker.updateComplete
      const appendBeforeError = append
      picker.state = { ...picker.state, state: 'error', message: 'Target is no longer editable.' }
      await picker.updateComplete
      return {
        forkHref: fork?.getAttribute('href'), forkTarget: fork?.getAttribute('target'),
        refreshModelId: refresh?.modelId, specAfterRefresh: picker.spec,
        appendBeforeError, append, errorRole: shadow().querySelector('[role="alert"]')?.textContent?.trim(),
      }
    })
    expect(state.forkHref).toBe('/dashboards/dashboard:project/fork')
    expect(state.forkTarget).toBe('_blank')
    expect(state.refreshModelId).toBe('model:sales')
    expect(state.specAfterRefresh).toMatchObject({ modelId: 'model:sales', metrics: [{ field: 'orders.revenue' }] })
    expect(state.appendBeforeError).toMatchObject({ dashboardId: 'dashboard:authored', pageId: 'overview' })
    expect(state.append).toMatchObject({ dashboardId: 'dashboard:authored', pageId: 'overview', revisionToken: 'opaque-cas', placementChoice: 'half', spec: { modelId: 'model:sales' } })
    expect(state.errorRole).toBe('Target is no longer editable.')
  } finally {
    await page.close()
  }
})

test('dashboard picker keeps pending state across cached target renders and releases it on server response', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer-dashboard-picker'))
    const result = await page.evaluate(async () => {
      const picker = document.createElement('lv-data-explorer-dashboard-picker') as any
      picker.state = {
        enabled: true, state: 'saved', targets: [{
          id: 'dashboard:authored', title: 'Authored', semanticModelId: 'model:sales',
          revisionToken: 'cached-cas', pages: [{ id: 'overview', title: 'Overview', placement: { col: 1, colSpan: 6, row: 1, rowSpan: 4 } }],
        }],
      }
      document.body.append(picker)
      await picker.updateComplete
      const shadow = () => picker.shadowRoot as ShadowRoot
      const button = (label: string) => Array.from(shadow().querySelectorAll('button')).find((candidate) => candidate.textContent?.includes(label)) as HTMLButtonElement
      const targetBridge = document.createElement('span')
      targetBridge.setAttribute('data-lv-dashboard-action', 'target')
      const appendBridge = document.createElement('span')
      appendBridge.setAttribute('data-lv-dashboard-action', 'append')
      document.body.append(targetBridge, appendBridge)
      button('Add to dashboard').click()
      await picker.updateComplete
      const pendingAfterOpen = button('Add visual').disabled && Boolean(shadow().querySelector('[role="status"]'))
      picker.state = { ...picker.state, state: 'saved' }
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'datastar-patch-signals', el: targetBridge } }))
      await picker.updateComplete
      await picker.updateComplete
      button('Add visual').click()
      await picker.updateComplete
      const pendingBeforeResponse = (shadow().querySelector('.primary-button') as HTMLButtonElement).disabled
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'datastar-patch-signals', el: appendBridge } }))
      picker.state = { ...picker.state, state: 'saved' }
      await picker.updateComplete
      await picker.updateComplete
      const releasedAfterResponse = !(shadow().querySelector('.primary-button') as HTMLButtonElement).disabled
      return { pendingAfterOpen, pendingBeforeResponse, releasedAfterResponse }
    })
    expect(result).toMatchObject({ pendingAfterOpen: true, pendingBeforeResponse: true, releasedAfterResponse: true })
  } finally {
    await page.close()
  }
})

test('dashboard picker sanitizes append failures and always releases pending state', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer-dashboard-picker'))
    const results = await page.evaluate(async () => {
      const target = {
        id: 'dashboard:authored', title: 'Authored', semanticModelId: 'model:sales', revisionToken: 'opaque-cas',
        pages: [{ id: 'overview', title: 'Overview', placement: { col: 1, colSpan: 6, row: 1, rowSpan: 4 } }],
      }
      const setup = async (submitAppend = true) => {
        const picker = document.createElement('lv-data-explorer-dashboard-picker') as any
        picker.state = { enabled: true, state: 'ready', targets: [target] }
        document.body.append(picker)
        await picker.updateComplete
        const shadow = () => picker.shadowRoot as ShadowRoot
        const button = (label: string) => Array.from(shadow().querySelectorAll('button')).find((candidate) => candidate.textContent?.includes(label)) as HTMLButtonElement
        const targetBridge = document.createElement('span')
        targetBridge.setAttribute('data-lv-dashboard-action', 'target')
        const refreshBridge = document.createElement('span')
        refreshBridge.setAttribute('data-lv-dashboard-action', 'refresh')
        const appendBridge = document.createElement('span')
        appendBridge.setAttribute('data-lv-dashboard-action', 'append')
        document.body.append(targetBridge, refreshBridge, appendBridge)
        button('Add to dashboard').click()
        await picker.updateComplete
        document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'datastar-patch-signals', el: targetBridge } }))
        picker.state = { ...picker.state, state: 'ready' }
        await picker.updateComplete
        await picker.updateComplete
        if (submitAppend) {
          button('Add visual').click()
          await picker.updateComplete
        }
        return { picker, shadow, button, appendBridge, refreshBridge, targetBridge }
      }
      const outcomes: Record<string, unknown> = {}
      for (const status of [403, 409]) {
        const { picker, shadow, button, appendBridge, refreshBridge, targetBridge } = await setup()
        document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: appendBridge, argsRaw: { status } } }))
        await picker.updateComplete
        outcomes[String(status)] = {
          pending: (shadow().querySelector('.primary-button') as HTMLButtonElement).disabled,
          alert: shadow().querySelector('[role="alert"]')?.textContent?.trim(),
          containsServerText: shadow().textContent?.includes('secret compiler detail') ?? false,
        }
        picker.remove()
        appendBridge.remove()
        refreshBridge.remove()
        targetBridge.remove()
      }
      const network = await setup()
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retries-failed', el: network.appendBridge } }))
      await network.picker.updateComplete
      outcomes.network = {
        pending: (network.shadow().querySelector('.primary-button') as HTMLButtonElement).disabled,
        alert: network.shadow().querySelector('[role="alert"]')?.textContent?.trim(),
      }
      network.picker.remove()
      network.appendBridge.remove()
      network.refreshBridge.remove()
      network.targetBridge.remove()

      const refreshNetwork = await setup(false)
      const refreshButton = Array.from(refreshNetwork.shadow().querySelectorAll('button')).find((candidate) => candidate.textContent?.includes('Refresh targets')) as HTMLButtonElement
      refreshButton.click()
      await refreshNetwork.picker.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retries-failed', el: refreshNetwork.refreshBridge } }))
      await refreshNetwork.picker.updateComplete
      outcomes.refreshNetwork = {
        pending: (Array.from(refreshNetwork.shadow().querySelectorAll('button')).find((candidate) => candidate.textContent?.includes('Refresh targets')) as HTMLButtonElement).disabled,
        alert: refreshNetwork.shadow().querySelector('[role="alert"]')?.textContent?.trim(),
      }
      refreshNetwork.picker.remove()
      refreshNetwork.appendBridge.remove()
      refreshNetwork.refreshBridge.remove()
      refreshNetwork.targetBridge.remove()
      return outcomes
    })
    expect(results['403']).toMatchObject({ pending: false, containsServerText: false })
    expect(String((results['403'] as any).alert)).toContain('not permitted')
    expect(results['409']).toMatchObject({ pending: false, containsServerText: false })
    expect(String((results['409'] as any).alert)).toContain('conflicted')
    expect(results.network).toMatchObject({ pending: false })
    expect(String((results.network as any).alert)).toContain('could not reach')
    expect(results.refreshNetwork).toMatchObject({ pending: false })
    expect(String((results.refreshNetwork as any).alert)).toContain('could not reach')
  } finally {
    await page.close()
  }
})

test('dashboard picker serializes refresh and ignores unrelated query fetches during append', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer-dashboard-picker'))
    const result = await page.evaluate(async () => {
      const picker = document.createElement('lv-data-explorer-dashboard-picker') as any
      picker.state = {
        enabled: true, state: 'ready', targets: [{
          id: 'dashboard:authored', title: 'Authored', semanticModelId: 'model:sales', revisionToken: 'opaque-cas',
          pages: [{ id: 'overview', title: 'Overview', placement: { col: 1, colSpan: 6, row: 1, rowSpan: 4 } }],
        }],
      }
      document.body.append(picker)
      await picker.updateComplete
      const shadow = () => picker.shadowRoot as ShadowRoot
      const button = (label: string) => Array.from(shadow().querySelectorAll('button')).find((candidate) => candidate.textContent?.includes(label)) as HTMLButtonElement
      const targetBridge = document.createElement('span')
      targetBridge.setAttribute('data-lv-dashboard-action', 'target')
      const appendBridge = document.createElement('span')
      appendBridge.setAttribute('data-lv-dashboard-action', 'append')
      document.body.append(targetBridge, appendBridge)
      button('Add to dashboard').click()
      await picker.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'datastar-patch-signals', el: targetBridge } }))
      picker.state = { ...picker.state, state: 'ready' }
      await picker.updateComplete
      await picker.updateComplete
      let appendCount = 0
      picker.addEventListener('lv-data-explorer-add-to-dashboard', () => appendCount++)
      ;(shadow().querySelector('.primary-button') as HTMLButtonElement).click()
      await picker.updateComplete
      const refresh = Array.from(shadow().querySelectorAll('button')).find((candidate) => candidate.textContent?.includes('Refresh targets')) as HTMLButtonElement
      const pendingBefore = (shadow().querySelector('.primary-button') as HTMLButtonElement).disabled
      refresh?.click()
      ;(shadow().querySelector('.primary-button') as HTMLButtonElement).click()
      await picker.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: document.createElement('span'), argsRaw: { status: 503 } } }))
      await picker.updateComplete
      return { pendingBefore, pendingAfter: (shadow().querySelector('.primary-button') as HTMLButtonElement).disabled, refreshDisabled: refresh.disabled, appendCount }
    })
    expect(result).toEqual({ pendingBefore: true, pendingAfter: true, refreshDisabled: true, appendCount: 1 })
  } finally {
    await page.close()
  }
})

test('dashboard picker bridge sends the generated append envelope key', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer-dashboard-picker'))
    const before = capturedAppendBodies.length
    await page.evaluate(async () => {
      const bridge = document.createElement('span')
      bridge.setAttribute('data-lv-dashboard-action', 'append')
      bridge.setAttribute('data-on:lv-data-explorer-add-to-dashboard__document', "$addExplorationToDashboard = evt.detail; @post('/append-body-check', {filterSignals: {include: /^(?:addExplorationToDashboard)(?:[.]|$)/}, headers: {}})")
      document.body.append(bridge)
      await new Promise((resolve) => setTimeout(resolve, 50))
      bridge.dispatchEvent(new CustomEvent('lv-data-explorer-add-to-dashboard', {
        bubbles: true, composed: true,
        detail: { dashboardId: 'dashboard:authored', pageId: 'overview', revisionToken: 'opaque-cas', placementChoice: 'half', spec: { schemaVersion: 1, modelId: 'model:sales', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 } },
      }))
      await new Promise((resolve) => setTimeout(resolve, 100))
    })
    expect(capturedAppendBodies.length).toBeGreaterThan(before)
    const payload = JSON.parse(capturedAppendBodies[capturedAppendBodies.length - 1])
    expect(payload).toHaveProperty('addExplorationToDashboard')
    expect(payload).not.toHaveProperty('dataExplorerAddToDashboard')
  } finally {
    await page.close()
  }
})

function testDocument() {
  return `
    <!doctype html>
    <html><head><style>html, body { margin: 0; min-height: 100%; }</style></head>
    <body>
      <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
      <script type="module" src="/data-explorer-under-test.js"></script>
    </body></html>
  `
}
