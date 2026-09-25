import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { governedBarPreviewEnvelope, builderTestDocument as testDocument } from './dashboard-builder-test-fixtures'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/dashboard-builder-test')

test('dashboard builder refreshes once after a successful agent draft mutation, including when a later tool fails', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const result = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      let completions = 0
      element.addEventListener('lv-builder-agent-run-complete', () => completions++)
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const waitForUpdate = () => new Promise((resolve) => setTimeout(resolve, 25))
      mergePatch({ agent: { activeConversationId: 'conversation-1', status: { running: true, runId: 'run-read-only' }, transcript: [] } })
      await waitForUpdate()
      mergePatch({ agent: { status: { running: false }, transcript: [
        { id: 'tool-read-only', runId: 'run-read-only', kind: 'tool', name: 'get_dashboard_draft', status: 'complete' },
      ] } })
      await waitForUpdate()
      const afterReadOnlyCompletion = completions

      mergePatch({ agent: { status: { running: true, runId: 'run-1' } } })
      await new Promise((resolve) => setTimeout(resolve, 25))
      const whileRunning = completions
      mergePatch({ agent: { status: { running: false }, transcript: [
        { id: 'tool-write', runId: 'run-1', kind: 'tool', name: 'add_dashboard_visual', status: 'complete' },
        { id: 'tool-after-write', runId: 'run-1', kind: 'tool', name: 'assign_dashboard_field', status: 'error', error: 'stale revision' },
      ] } })
      await waitForUpdate()
      const afterMutationCompletion = completions
      mergePatch({ builder: { title: 'Refreshed draft' } })
      await waitForUpdate()
      const afterUnrelatedPatch = completions
      mergePatch({ agent: { status: { running: true, runId: 'run-read-only-after-write' } } })
      await waitForUpdate()
      mergePatch({ agent: { status: { running: false }, transcript: [
        { id: 'tool-write', runId: 'run-1', kind: 'tool', name: 'add_dashboard_visual', status: 'complete' },
        { id: 'tool-after-write', runId: 'run-1', kind: 'tool', name: 'assign_dashboard_field', status: 'error', error: 'stale revision' },
        { id: 'tool-read-only-after-write', runId: 'run-read-only-after-write', kind: 'tool', name: 'preview_dashboard_draft', status: 'complete' },
      ] } })
      await waitForUpdate()
      return { afterReadOnlyCompletion, whileRunning, afterMutationCompletion, afterUnrelatedPatch, afterSubsequentReadOnlyCompletion: completions }
    })
    expect(result).toEqual({ afterReadOnlyCompletion: 0, whileRunning: 0, afterMutationCompletion: 1, afterUnrelatedPatch: 1, afterSubsequentReadOnlyCompletion: 1 })
  } finally {
    await page.close()
  }
})

test('dashboard builder does not refresh after a failed or unrelated agent tool', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const completions = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      let count = 0
      element.addEventListener('lv-builder-agent-run-complete', () => count++)
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const waitForUpdate = () => new Promise((resolve) => setTimeout(resolve, 25))
      mergePatch({ agent: { status: { running: true, runId: 'run-failed-write' }, transcript: [] } })
      await waitForUpdate()
      mergePatch({ agent: { status: { running: false }, transcript: [
        { id: 'tool-failed-write', runId: 'run-failed-write', kind: 'tool', name: 'add_dashboard_page', status: 'error', error: 'stale revision' },
      ] } })
      await waitForUpdate()
      mergePatch({ agent: { status: { running: true, runId: 'run-unrelated-tool' } } })
      await waitForUpdate()
      mergePatch({ agent: { status: { running: false }, transcript: [
        { id: 'tool-unrelated', runId: 'run-unrelated-tool', kind: 'tool', name: 'query_semantic_model', status: 'complete' },
      ] } })
      await waitForUpdate()
      return count
    })
    expect(completions).toBe(0)
  } finally {
    await page.close()
  }
})

test('dashboard appearance picker only offers canonical colors', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const colors = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      ;(element.shadowRoot.querySelector('[data-builder-action="appearance"]') as HTMLButtonElement).click()
      await element.updateComplete
      const picker = element.shadowRoot.querySelector('lv-dashboard-icon-picker') as any
      await picker.updateComplete
      return [...picker.shadowRoot.querySelectorAll('.colors button')].map((button: Element) => button.getAttribute('aria-label'))
    })
    expect(colors).toEqual(['gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink'])
  } finally {
    await page.close()
  }
})

test('pivot-backed field wells describe their row and column roles', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const labels = await page.locator('lv-dashboard-builder').evaluate((element: any) => ['matrix', 'pivot'].map((type) => [
      element.fieldWellLabel({ type }, 'dimension'),
      element.fieldWellLabel({ type }, 'metric'),
    ]))
    expect(labels).toEqual([['Rows / Columns', 'Values'], ['Rows / Columns', 'Values']])
  } finally {
    await page.close()
  }
})

test('a new Gauge starts with an available governed measure', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const command = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await element.updateComplete
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(element.shadowRoot.querySelector('[data-visual-picker-type="gauge"]') as HTMLButtonElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return command
    })
    expect(command).toMatchObject({ action: 'add_visual', pageId: 'overview', type: 'gauge', title: 'Total', fieldId: 'orders.total', role: 'metric' })
  } finally {
    await page.close()
  }
})

test('an existing empty Gauge offers a one-click measure repair', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const pages = structuredClone(element.builder.pages)
      pages[0].visuals[0].type = 'gauge'
      pages[0].visuals[0].slots = []
      mergePatch({ builder: { pages } })
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const button = root.querySelector('.visual-preview-empty button') as HTMLButtonElement
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      button.click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { label: button.textContent?.trim(), command }
    })
    expect(state.label).toBe('Use Total measure')
    expect(state.command).toMatchObject({ action: 'assign_field', pageId: 'overview', visualId: 'sales-chart', fieldId: 'orders.total', role: 'metric' })
  } finally {
    await page.close()
  }
})

test('an existing Gauge can explicitly restore automatic range', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const command = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const pages = structuredClone(element.builder.pages)
      pages[0].visuals[0].type = 'gauge'
      pages[0].visuals[0].formatOptions = [
        { key: 'minimum', label: 'Minimum', section: 'Scale', control: 'number', value: '0', choices: [] },
        { key: 'maximum', label: 'Maximum', section: 'Scale', control: 'number', value: '100', choices: [] },
      ]
      mergePatch({ builder: { pages } })
      await element.updateComplete
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(element.shadowRoot.querySelector('[data-format-section="Scale"] button') as HTMLButtonElement).click()
      await new Promise(resolve => setTimeout(resolve, 20))
      return command
    })
    expect(command).toMatchObject({ action: 'update_visual_format', pageId: 'overview', visualId: 'sales-chart', formatKey: 'autoRange', formatValue: 'true' })
  } finally {
    await page.close()
  }
})

test('filter settings prevent invalid requirements and visibly restore a blank label', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { filters: [{ id: 'state', label: 'State', dimension: 'orders.status', controlType: 'multiSelect', required: false, readerEditable: true, urlParameter: 'state', targets: [], bindings: [] }] } })
      await element.updateComplete
      element.shadowRoot.querySelector('.filter-card').click()
      await element.updateComplete
      element.shadowRoot.querySelector('.filter-settings').open = true
      element.testFilterCommands = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => element.testFilterCommands.push(event.detail))
    })
    const editor = page.getByRole('region', { name: 'Configure State filter', exact: true })
    expect(await editor.getByRole('checkbox', { name: 'Required', exact: true }).isDisabled()).toBe(true)
    const label = editor.getByRole('textbox', { name: 'Label', exact: true })
    await label.fill('   ')
    await label.press('Tab')
    expect(await label.inputValue()).toBe('State')
    const parameter = editor.getByRole('textbox', { name: 'URL parameter', exact: true })
    await parameter.fill(' state ')
    await parameter.press('Tab')
    expect(await parameter.inputValue()).toBe('state')
    expect(await page.locator('lv-dashboard-builder').evaluate((element: any) => element.testFilterCommands)).toEqual([])
  } finally { await page.close() }
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

test('dashboard builder explains invalid map coordinates without exposing compiler IDs', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const message = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const source = element.builder.pages[0].visuals[0]
      mergePatch({ builder: { pages: [{ ...element.builder.pages[0], visuals: [{ ...source, type: 'map', previewError: 'visual "sales-chart" IR: geographic layer "points": latitude field must be numeric' }] }, element.builder.pages[1]] } })
      await element.updateComplete
      return element.shadowRoot.querySelector('.visual-preview-empty')?.textContent?.trim()
    })
    expect(message).toContain('Choose numeric latitude and longitude fields to preview this map.')
    expect(message).not.toContain('sales-chart')
  } finally { await page.close() }
})

test('a new Map asks for numeric coordinate dimensions without inventing fields', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const catalog = structuredClone(element.builder.visualCatalog)
      const map = catalog.find((entry: any) => entry.type === 'map')
      map.roles = ['dimension', 'metric']
      map.roleLimits = [{ role: 'dimension', minimum: 2, maximum: 2 }, { role: 'metric', minimum: 0, maximum: 1 }]
      mergePatch({ builder: { visualCatalog: catalog } })
      await element.updateComplete
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await element.updateComplete
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(element.shadowRoot.querySelector('[data-visual-picker-type="map"]') as HTMLButtonElement).click()
      await element.updateComplete
      const page = structuredClone(element.builder.pages[0])
      const visual = { ...page.visuals[0], id: 'visual_map', visualId: 'visual_map', type: 'map', title: 'Map', slots: [] }
      page.visuals = [...page.visuals, visual]
      mergePatch({ builder: {
        revision: { id: 'rev-8', number: 8, contentHash: 'sha256:map' },
        pages: [page, element.builder.pages[1]], selectedVisualId: 'visual_map',
      } })
      await element.updateComplete
      return {
        command,
        canvas: element.shadowRoot.querySelector('.visual[data-visual-type="map"] .visual-preview-empty')?.textContent?.replace(/\s+/g, ' ').trim(),
        slotLabel: element.shadowRoot.querySelector('.visual[data-visual-type="map"] .visual-type')?.textContent?.trim(),
        useButtons: element.shadowRoot.querySelectorAll('.visual[data-visual-type="map"] .visual-preview-empty button').length,
        previewHosts: element.shadowRoot.querySelectorAll('.visual[data-visual-type="map"] .visual-preview lv-visualization-host').length,
      }
    })
    expect(state.command).toMatchObject({ action: 'add_visual', type: 'map' })
    expect(state.command).not.toHaveProperty('fieldId')
    expect(state.command).not.toHaveProperty('role')
    expect(state.canvas).toContain('Map preview unavailable')
    expect(state.canvas).toContain('Choose numeric latitude and longitude fields to preview this map.')
    expect(state.slotLabel).toBe('map · 0 field slots')
    expect(state.useButtons).toBe(0)
    expect(state.previewHosts).toBe(0)
  } finally { await page.close() }
})

test('pending visual type changes show loading until the server refreshes the visual fields', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const pages = structuredClone(element.builder.pages)
      pages[0].visuals[0].type = 'kpi'
      pages[0].visuals[0].slots = [{ id: 'metric-0', label: 'Total', kind: 'metric', fieldId: 'orders.total' }]
      const catalog = structuredClone(element.builder.visualCatalog)
      const line = catalog.find((entry: any) => entry.type === 'line')
      line.roleLimits = [{ role: 'dimension', minimum: 1, maximum: 1 }, { role: 'metric', minimum: 1, maximum: 0 }]
      mergePatch({ builder: { pages, visualCatalog: catalog } })
      await element.updateComplete
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(element.shadowRoot.querySelector('[data-visual-picker-type="line"]') as HTMLButtonElement).click()
      await element.updateComplete
      return {
        command,
        canvas: element.shadowRoot.querySelector('.visual-preview-empty')?.textContent?.replace(/\s+/g, ' ').trim(),
        requirements: element.shadowRoot.querySelector('.visual-requirements')?.textContent?.replace(/\s+/g, ' ').trim(),
      }
    })
    expect(state.command).toMatchObject({ action: 'set_visual_type', type: 'line' })
    expect(state.canvas).toContain('Loading line chart…')
    expect(state.canvas).not.toContain('Add 1 dimension')
    expect(state.canvas).not.toContain('preview unavailable')
    expect(state.requirements).toBe('Updating line chart preview…')
  } finally { await page.close() }
})

test('canvas selection does not open visual focus; the explicit expand action does', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any, preview) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { preview: { active: true } }, builderVisuals: { 'sales-chart': preview } })
      await element.updateComplete
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
      await element.updateComplete
      element.shadowRoot.querySelector('.visual')?.dispatchEvent(new MouseEvent('click', { bubbles: true, composed: true }))
      await element.updateComplete
      const modal = element.shadowRoot.querySelector('lv-visual-modal') as any
      const afterSelection = modal.shadowRoot.querySelector('[role="dialog"]') !== null
      const host = element.shadowRoot.querySelector('lv-visualization-host') as any
      host.shadowRoot.querySelector('[data-visualization-expand]')?.click()
      await modal.updateComplete
      await modal.updateComplete
      return { afterSelection, afterExpand: modal.shadowRoot.querySelector('[role="dialog"]') !== null }
    }, governedBarPreviewEnvelope('rev-7'))
    expect(state).toEqual({ afterSelection: false, afterExpand: true })
  } finally { await page.close() }
})

test('native resize handles suspend chart rendering until the placement save finishes', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => {
      const element = document.querySelector('lv-dashboard-builder') as any
      return Boolean(element?.builder?.pages?.length && !element.isUpdatePending)
    })
    const builder = page.locator('lv-dashboard-builder')
    await builder.evaluate(async (element: any, preview) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { preview: { active: true } }, builderVisuals: { 'sales-chart': preview } })
    }, governedBarPreviewEnvelope('rev-7'))
    const host = builder.locator('lv-visualization-host')
    await host.waitFor()
    await page.waitForFunction(() => {
      const builder = document.querySelector('lv-dashboard-builder') as any
      const host = builder?.shadowRoot?.querySelector('lv-visualization-host') as any
      return Boolean(host?.controller?.envelope)
    })
    expect(await host.evaluate((element: any) => Boolean(element.shadowRoot.querySelector('.renderer svg')))).toBe(true)
    expect(await host.evaluate((element: any) => element.shadowRoot.querySelectorAll('.renderer canvas').length)).toBe(0)
    const handle = builder.locator('.visual > .ui-resizable-se')
    const box = (await handle.boundingBox())!
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2)
    await page.mouse.down()
    await page.mouse.move(box.x + 40, box.y + 60, { steps: 8 })
    expect(await host.evaluate((element: any) => element.resizeSuspended)).toBe(true)
    await page.mouse.up()
    expect(await host.evaluate((element: any) => element.resizeSuspended)).toBe(true)
    await builder.evaluate((element: any) => document.dispatchEvent(new CustomEvent('datastar-fetch', {
      detail: { type: 'finished', el: element },
    })))
    expect(await host.evaluate((element: any) => element.resizeSuspended)).toBe(false)
    const resumed = await builder.evaluate((element: any) => {
      const host = element.shadowRoot.querySelector('lv-visualization-host')
      element.setPreviewResizeSuspended(true)
      element.destroyGridStack()
      return host.resizeSuspended
    })
    expect(resumed).toBe(false)
  } finally { await page.close() }
})

test('failed placement saves release suspended previews', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const resumed = await page.locator('lv-dashboard-builder').evaluate((element: any) => {
      element.gridResizeSavePending = true
      element.setPreviewResizeSuspended(true)
      element.commandPending = true
      element.activeCommandAction = 'set_placements'
      document.dispatchEvent(new CustomEvent('datastar-fetch', {
        detail: { type: 'error', argsRaw: { status: 409 }, el: element },
      }))
      return !element.gridResizeSavePending && !element.previewResizeSuspended && !element.commandPending
    })
    expect(resumed).toBe(true)
  } finally { await page.close() }
})

test('late window patches leave the current draft preview intact', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const observed = await page.locator('lv-dashboard-builder').evaluate(async (element: any, old) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { preview: { active: true } }, builderVisuals: { 'sales-chart': old } })
      await element.updateComplete
      const current = { ...old, servingStateID: 'next-draft', spec: { ...old.spec, title: 'Current draft table' } }
      mergePatch({ runtime: { servingStateId: 'next-draft' }, builder: { revision: { id: 'rev-8', number: 8 } }, builderVisuals: { 'sales-chart': current } })
      // No render between these patches: neither response may erase the other.
      mergePatch({ builderVisuals: { 'window:generation-7:overview:0:sales-chart': old } })
      await element.updateComplete
      const host = element.shadowRoot.querySelector('lv-visualization-host') as any
      return { revision: element.builder.revision.id, title: host.envelope.spec.title }
    }, governedBarPreviewEnvelope('sha256:window-race'))
    expect(observed).toEqual({ revision: 'rev-8', title: 'Current draft table' })
  } finally { await page.close() }
})

test('concurrent Datastar window reads both deliver their independent results', async () => {
  const page = await browser.newPage()
  try {
    await page.route('**/windows', async route => {
      const request = route.request().postDataJSON().visualWindowCommand
      await new Promise(resolve => setTimeout(resolve, request.visualID === 'first' ? 200 : 50))
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ status: request.visualID === 'first' ? { refreshId: 'first' } : { lastUpdated: 'second' } }) })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      element.setAttribute('data-on:lv-visualization-window-request', "$visualWindowCommand = evt.detail; @post('/windows', {filterSignals: {include: /^(?:visualWindowCommand)(?:[.]|$)/}, requestCancellation: 'disabled'})")
      await new Promise(resolve => requestAnimationFrame(resolve))
      for (const visualID of ['first', 'second']) {
        element.dispatchEvent(new CustomEvent('lv-visualization-window-request', { detail: { visualID } }))
        await new Promise(resolve => setTimeout(resolve, 20))
      }
    })
    await page.waitForFunction(() => {
      const builder = document.querySelector('lv-dashboard-builder') as any
      const results = builder.signal('status', {})
      return results.refreshId === 'first' && results.lastUpdated === 'second'
    })
  } finally { await page.close() }
})

test('compiled multi-series previews retain their data despite picker default limits', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any, preview) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const source = element.builder.pages[0].visuals[0]
      mergePatch({ builder: {
        visualCatalog: element.builder.visualCatalog.map((entry: any) => entry.type === 'bar'
          ? { ...entry, roleLimits: [{ role: 'dimension', minimum: 1, maximum: 1 }, { role: 'metric', minimum: 1, maximum: 0 }] } : entry),
        pages: [{ ...element.builder.pages[0], visuals: [{ ...source, slots: [
          { id: 'dimension-0', label: 'Status', kind: 'dimension', fieldId: 'orders.status', required: true },
          { id: 'dimension-1', label: 'Month', kind: 'dimension', fieldId: 'orders.month', required: true },
          { id: 'metric-0', label: 'Total', kind: 'metric', fieldId: 'orders.total', required: true },
        ] }] }, element.builder.pages[1]],
        preview: { ...element.builder.preview, active: true, error: '' },
      }, builderVisuals: { 'sales-chart': preview } })
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const host = root.querySelector('lv-visualization-host') as any
      await host?.updateComplete
      return {
        placeholder: root.querySelector('.visual-preview-empty')?.textContent,
        requirements: root.querySelector('.visual-requirements')?.textContent,
        rows: host?.envelope.dataState.datasets[0].rows,
        fields: [...root.querySelectorAll('.field-token-label')].map(node => node.textContent?.trim()),
      }
    }, governedBarPreviewEnvelope('sha256:compiled-series'))
    expect(state.placeholder).toBeUndefined()
    expect(state.requirements).toBeUndefined()
    expect(state.rows).toEqual([['Delivered', 42], ['Shipped', 7]])
    expect(state.fields).toContain('Month')
  } finally {
    await page.close()
  }
})

test('dashboard builder exposes guarded Apply and Cancel actions for deferred filters', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const key = 'dashboard:revenue/report/status'
      const unfiltered = { kind: 'unfiltered' }
      const selected = { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'paid' }] }
      const contract = {
        applicationMode: 'deferred',
        definitions: {},
        bindings: { [key]: { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] } },
      }
      const applied = (expression: any) => ({ expression, resolvedExpression: expression })
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const commands: any[] = []
      element.addEventListener('lv-builder-filter-command', (event: CustomEvent) => commands.push(event.detail))
      mergePatch({
        builderFilterContract: contract,
        builderFilterState: { revision: 1, appliedControls: { [key]: applied(unfiltered) }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' },
      })
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const initial = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-filter-apply], [data-filter-cancel]')).map((button) => ({ label: button.textContent?.trim(), disabled: button.disabled }))
      element.dispatchEvent(new CustomEvent('lv-filter-mutate', { bubbles: true, composed: true, detail: { bindingKey: key, expression: selected } }))
      await element.updateComplete
      const whilePending = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-filter-apply], [data-filter-cancel]')).map((button) => button.disabled)
      mergePatch({ builderFilterState: { revision: 2, appliedControls: { [key]: applied(unfiltered) }, draftControls: { [key]: selected }, dirtyBindings: [key], defaultsRevision: 'defaults-1' } })
      await element.updateComplete
      await element.updateComplete
      const dirty = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-filter-apply], [data-filter-cancel]')).map((button) => ({ label: button.textContent?.trim(), disabled: button.disabled }))
      ;(root.querySelector<HTMLButtonElement>('[data-filter-apply]'))!.click()
      await element.updateComplete
      mergePatch({ builderFilterState: { revision: 3, appliedControls: { [key]: applied(selected) }, draftControls: { [key]: selected }, dirtyBindings: [key], defaultsRevision: 'defaults-1' } })
      await element.updateComplete
      await element.updateComplete
      ;(root.querySelector<HTMLButtonElement>('[data-filter-cancel]'))!.click()
      await element.updateComplete
      return { initial, whilePending, dirty, commands }
    })
    expect(state.initial).toEqual([])
    expect(state.whilePending).toEqual([true, true])
    expect(state.dirty).toEqual([{ label: 'Cancel', disabled: false }, { label: 'Apply (1)', disabled: false }])
    expect(state.commands.map((command: any) => command.kind)).toEqual(['mutate', 'apply', 'cancel'])
    expect(state.commands[1]).toMatchObject({ kind: 'apply', baseRevision: 2 })
    expect(state.commands[2]).toMatchObject({ kind: 'cancel', baseRevision: 3 })
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps search and cursor changes distinct during option request dedupe', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const key = 'dashboard:revenue/report/status'
      const unfiltered = { kind: 'unfiltered' }
      const contract = {
        applicationMode: 'immediate',
        definitions: { status: { id: 'status', label: 'Status', field: 'orders.status', dataset: 'orders', valueKind: 'string', predicates: [{ kind: 'set', operators: ['in'] }], options: { kind: 'distinct', limit: 20, includeNull: false, values: [] } } },
        bindings: { [key]: { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'multiple', maxSelectedValues: 0, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] } },
      }
      const requests: any[] = []
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      element.addEventListener('lv-builder-filter-options-request', (event: CustomEvent) => requests.push(event.detail))
      mergePatch({ builderFilterContract: contract, builderFilterState: { revision: 1, appliedControls: { [key]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' } })
      await element.updateComplete
      const request = (search: string, cursor?: string) => element.dispatchEvent(new CustomEvent('lv-filter-options-needed', { bubbles: true, composed: true, detail: { bindingKey: key, search, cursor, limit: 20 } }))
      request('')
      request('paid')
      request('paid', 'cursor-1')
      await element.updateComplete
      return { generations: requests.map((item) => item.requestGeneration), searches: requests.map((item) => item.search), cursors: requests.map((item) => item.cursor) }
    })
    expect(state).toEqual({ generations: [1, 2, 3], searches: ['', 'paid', 'paid'], cursors: [undefined, undefined, 'cursor-1'] })
  } finally {
    await page.close()
  }
})

test('focused filter options recover when a filter revision supersedes an in-flight request', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const result = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const key = 'dashboard:revenue/report/status'
      const unfiltered = { kind: 'unfiltered' }
      const binding = { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] }
      const definition = { id: 'status', label: 'Status', field: 'orders.status', dataset: 'orders', valueKind: 'string', predicates: [{ kind: 'set', operators: ['in'] }], options: { kind: 'distinct', limit: 20, includeNull: false, values: [] } }
      const state = (revision: number) => ({ revision, appliedControls: { [key]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' })
      mergePatch({ builderFilterContract: { applicationMode: 'immediate', definitions: { status: definition }, bindings: { [key]: binding } }, builderFilterState: state(1) })
      await element.updateComplete
      const leaf: any = document.createElement('lv-filter-leaf')
      Object.assign(leaf, { definition, binding, presentation: { style: 'dropdown', search: false, selectAll: false, showCounts: false, showSummary: false, compact: false }, expression: unfiltered, optionRequestReady: true, optionContext: element.builderFilterOptionContext(binding, 'overview') })
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => element.dispatchEvent(new CustomEvent('lv-filter-options-needed', { detail: event.detail })))
      document.body.append(leaf)
      await leaf.updateComplete
      const requests: any[] = []
      element.addEventListener('lv-builder-filter-options-request', (event: CustomEvent) => requests.push(event.detail))
      leaf.shadowRoot.querySelector('select').focus()
      await leaf.updateComplete
      mergePatch({ builderFilterState: state(2), builderFilterOptionPages: {} })
      await element.updateComplete
      leaf.optionContext = element.builderFilterOptionContext(binding, 'overview')
      await leaf.updateComplete
      leaf.options = { bindingKey: key, servingStateID: 'generation-7', filterRevision: 2, requestGeneration: 2, items: [{ value: { kind: 'string', value: 'paid' }, label: 'Paid', null: false, selected: false, available: true }], complete: true }
      await leaf.updateComplete
      await leaf.updateComplete
      return { revisions: requests.map((request) => request.filterRevision), loading: leaf.optionLoading, labels: [...leaf.shadowRoot.querySelector('select').options].map((option: any) => option.text) }
    })
    expect(result.revisions).toEqual([1, 2])
    expect(result.loading).toBe(false)
    expect(result.labels).toContain('Paid')
  } finally { await page.close() }
})

test('dynamic option controls request explicit cursors and retain prior pages', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'status', label: 'Status', field: 'orders.status', valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'distinct', limit: 1, values: [] },
      }
      leaf.binding = {
        key: 'fb_status', id: 'status', filter: 'status', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'multiple', maxSelectedValues: 0,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'list', search: false, selectAll: false, showCounts: false, showSummary: false, compact: false,
      }
      const requests: unknown[] = []
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => requests.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      leaf.options = {
        bindingKey: 'fb_status', servingStateID: 'serving', streamGeneration: 1,
        filterRevision: 1, requestGeneration: 1, complete: false, nextCursor: 'cursor-2',
        consumerIdentity: 'option:fb_status',
        items: [{ value: { kind: 'string', value: 'first' }, label: 'First', null: false, selected: false, available: true }],
      }
      await leaf.updateComplete
      await leaf.updateComplete
      const beforeLoadMore = {
        requests: requests.length,
        labels: Array.from((leaf.shadowRoot as ShadowRoot).querySelectorAll('.option span')).map((item) => item.textContent?.trim()),
        loadMore: (leaf.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.load-more-options')?.textContent?.trim(),
      }
      ;(leaf.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.load-more-options')!.click()
      await leaf.updateComplete
      await leaf.updateComplete
      const afterLoadMoreRequest = requests.at(-1)
      leaf.options = {
        bindingKey: 'fb_status', servingStateID: 'serving', streamGeneration: 1,
        filterRevision: 1, requestGeneration: 2, complete: true,
        consumerIdentity: 'option:fb_status',
        items: [{ value: { kind: 'string', value: 'second' }, label: 'Second', null: false, selected: false, available: true }],
      }
      await leaf.updateComplete
      await leaf.updateComplete
      const afterSecondPage = {
        labels: Array.from((leaf.shadowRoot as ShadowRoot).querySelectorAll('.option span')).map((item) => item.textContent?.trim()),
        hasLoadMore: Boolean((leaf.shadowRoot as ShadowRoot).querySelector('.load-more-options')),
      }
      // The parent may project the same accepted page again during an
      // unrelated rerender. That projection must not discard page one.
      leaf.options = { ...leaf.options, items: [...leaf.options.items] }
      await leaf.updateComplete
      await leaf.updateComplete
      const afterDuplicatePage = Array.from((leaf.shadowRoot as ShadowRoot).querySelectorAll('.option span')).map((item) => item.textContent?.trim())
      // A late first page must not replace the accepted continuation.
      leaf.options = {
        bindingKey: 'fb_status', servingStateID: 'serving', streamGeneration: 1,
        filterRevision: 1, requestGeneration: 1, complete: false, nextCursor: 'cursor-2',
        consumerIdentity: 'option:fb_status',
        items: [{ value: { kind: 'string', value: 'first' }, label: 'First', null: false, selected: false, available: true }],
      }
      await leaf.updateComplete
      await leaf.updateComplete
      return {
        beforeLoadMore,
        afterLoadMoreRequest,
        afterSecondPage,
        afterDuplicatePage,
        afterStalePage: Array.from((leaf.shadowRoot as ShadowRoot).querySelectorAll('.option span')).map((item) => item.textContent?.trim()),
      }
    })
    expect(result).toEqual({
      beforeLoadMore: { requests: 1, labels: ['First'], loadMore: 'Load more values' },
      afterLoadMoreRequest: { bindingKey: 'fb_status', search: '', cursor: 'cursor-2', limit: 1 },
      afterSecondPage: { labels: ['First', 'Second'], hasLoadMore: false },
      afterDuplicatePage: ['First', 'Second'],
      afterStalePage: ['First', 'Second'],
    })
  } finally {
    await page.close()
  }
})

test('pending page preview shows loading without false invalid-preview errors', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { preview: { active: false, loading: true, error: '' } }, builderVisuals: null })
      await element.updateComplete
    })
    const builder = page.locator('lv-dashboard-builder')
    expect(await builder.getByText('Loading page data…', { exact: false }).count()).toBeGreaterThan(0)
    expect(await builder.locator('.visual-preview-empty').first().innerText()).toContain('Loading ')
    expect(await builder.locator('.visual-preview-empty').first().innerText()).not.toContain('unavailable')
  } finally { await page.close() }
})

test('background dashboard updates preserve an in-flight filter continuation', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const key = 'fb_status'
      const unfiltered = { kind: 'unfiltered' }
      const binding = { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'multiple', readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] }
      const definition = { id: 'status', label: 'Status', field: 'orders.status', valueKind: 'string', predicates: [{ kind: 'set', operators: ['in'] }], options: { kind: 'distinct', limit: 1, values: [] } }
      element.optionRequests = []
      element.addEventListener('lv-builder-filter-options-request', (event: CustomEvent) => {
        const request = event.detail
        element.optionRequests.push(request)
        if (element.optionRequests.length > 4) return
        if (request.cursor) setTimeout(() => mergePatch({ status: { lastUpdated: '2026-09-13T12:00:00Z' } }), 20)
        setTimeout(() => mergePatch({ builderFilterOptionPages: { [key]: {
          bindingKey: key, servingStateID: 'generation-7', streamGeneration: 0, filterRevision: 1,
          requestGeneration: request.requestGeneration, complete: Boolean(request.cursor),
          ...(!request.cursor ? { nextCursor: 'cursor-2' } : {}),
          items: [{ value: { kind: 'string', value: request.cursor ? 'second' : 'first' }, label: request.cursor ? 'Second' : 'First', null: false, available: true, selected: false }],
        } } }), request.cursor ? 150 : 30)
      })
      mergePatch({
        builder: { filters: [{ id: 'status', label: 'Status', dimension: 'orders.status', controlType: 'multiSelect', required: false, readerEditable: true, targets: [], bindings: [] }] },
        builderFilterContract: { applicationMode: 'immediate', definitions: { status: definition }, bindings: { [key]: binding } },
        builderFilterState: { revision: 1, appliedControls: { [key]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: '1' },
      })
      await element.updateComplete
    })
    await page.getByRole('button', { name: 'Status: All', exact: true }).click()
    const options = page.getByRole('dialog', { name: 'Status filter options', exact: true })
    await options.getByRole('button', { name: 'Load more values', exact: true }).click()
    await options.getByRole('checkbox', { name: 'Second', exact: true }).waitFor({ timeout: 1500 })
    expect(await options.getByRole('checkbox', { name: 'First', exact: true }).isVisible()).toBe(true)
    const requests = await page.locator('lv-dashboard-builder').evaluate((element: any) => element.optionRequests)
    expect(requests).toHaveLength(2)
    expect(requests[1]).toMatchObject({ cursor: 'cursor-2', requestGeneration: 2 })
  } finally { await page.close() }
})

test('filter settings stay with their selected card and fit a narrow pane', async () => {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1050 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { filters: ['First filter', 'Second filter'].map((label, index) => ({ id: `filter_${index}`, label, dimension: 'orders.status', controlType: 'multiSelect', required: false, readerEditable: true, targets: [], bindings: [] })) } })
      await element.updateComplete
    })
    await page.getByRole('button', { name: /First filter/ }).click()
    const editor = page.getByRole('region', { name: 'Configure First filter filter', exact: true })
    await editor.locator('summary').click()
    const metrics = await editor.evaluate((element) => {
      const right = element.getBoundingClientRect().right
      const controls = [...element.querySelectorAll('input[type=text],select,button,.filter-scope-option')]
      const scopes = [...element.querySelectorAll('.filter-scope-option')].map(x => x.getBoundingClientRect())
      return { overflows: controls.some(x => x.getBoundingClientRect().right > right + 1), stacked: scopes[1]!.top >= scopes[0]!.bottom }
    })
    expect(metrics).toEqual({ overflows: false, stacked: true })
    const editorBox = await editor.boundingBox()
    const nextBox = await page.getByRole('button', { name: /Second filter/ }).boundingBox()
    expect(nextBox!.y).toBeGreaterThanOrEqual(editorBox!.y + editorBox!.height - 1)
  } finally { await page.close() }
})


test('coalesced table windows retain the newer sort and rendered rows', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any, source) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const schema = source.spec.datasets[0]
      const spec = {
        kind: 'table', title: 'Orders', datasets: [schema],
        accessibility: source.spec.accessibility, dataBudget: source.spec.dataBudget, interactions: [],
        columns: [{ field: { dataset: 'primary', field: 'category' }, label: 'Status', width: 160, formatting: [] }],
        presentation: { rowHeight: 32, showHeader: true, striped: false },
      }
      element.testTableWindow = (resetVersion: number, requestSeq: number, direction: string) => {
        const sort = [{ field: { dataset: 'primary', field: 'category' }, direction }]
        const rows = direction === 'ascending' ? [['A', 1], ['B', 2], ['C', 3]] : [['C', 3], ['B', 2], ['A', 1]]
        const data = {
          kind: 'windowed', specRevision: source.specRevision, dataRevision: source.dataRevision, generation: 1,
          schema, cardinality: { kind: 'exact', count: 3 }, availableRows: 3, rowCap: 100, chunkSize: 3,
          resetVersion, sort, blocks: { a: { id: 'a', start: 0, rows, requestSeq, resetVersion, sort } },
        }
        return { ...source, rendererID: 'tanstack', spec,
          dataState: { ...source.dataState, kind: 'windowed', payload: JSON.stringify(data) } }
      }
      const builder = element.builder
      mergePatch({ builder: { preview: { active: true }, pages: [{ ...builder.pages[0], visuals: [{
        ...builder.pages[0].visuals[0], type: 'table', slots: [{ id: 'detail', label: 'Status', kind: 'detail', fieldId: 'orders.status', required: true }],
      }] }, builder.pages[1]] }, builderVisuals: { 'sales-chart': element.testTableWindow(1, 0, 'descending') } })
      await element.updateComplete
    }, governedBarPreviewEnvelope('sha256:sort-race'))
    await page.locator('lv-report-table').waitFor()
    const initial = await page.locator('lv-report-table').evaluate((table: any) => table.table.sort.direction)
    expect(initial).toBe('desc')
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const key = 'window:generation-7:overview:0:sales-chart'
      // Both responses arrive before Lit can render the newer sort.
      mergePatch({ builderVisuals: { [key]: element.testTableWindow(2, 3, 'ascending') } })
      mergePatch({ builderVisuals: { [key]: element.testTableWindow(1, 2, 'descending') } })
      await element.updateComplete
    })
    await page.waitForFunction(() => {
      const builder = document.querySelector('lv-dashboard-builder') as any
      const host = builder.shadowRoot.querySelector('lv-visualization-host') as any
      return host?.shadowRoot.querySelector('lv-report-table')?.table.resetVersion === 2
    }, undefined, { timeout: 2000 })
    const result = await page.locator('lv-report-table').evaluate(async (table: any) => {
      await table.updateComplete
      return { reset: table.table.resetVersion, sort: table.table.sort.direction,
        rows: table.visibleRows.filter((slot: any) => slot.kind === 'row').map((slot: any) => slot.row.category) }
    })
    expect(result).toEqual({ reset: 2, sort: 'asc', rows: ['A', 'B', 'C'] })
  } finally { await page.close() }
})

test('an unrelated draft revision keeps an existing windowed visual mounted', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const preserved = await page.locator('lv-dashboard-builder').evaluate(async (element: any, source) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const schema = source.spec.datasets[0]
      const spec = {
        kind: 'table', title: 'Orders', datasets: [schema],
        accessibility: source.spec.accessibility, dataBudget: source.spec.dataBudget, interactions: [],
        columns: [{ field: { dataset: 'primary', field: 'category' }, label: 'Status', width: 160, formatting: [] }],
        presentation: { rowHeight: 32, showHeader: true, striped: false },
      }
      const data = {
        kind: 'windowed', specRevision: source.specRevision, dataRevision: source.dataRevision, generation: 1,
        schema, cardinality: { kind: 'exact', count: 1 }, availableRows: 1, rowCap: 100, chunkSize: 1,
        resetVersion: 1, sort: [], blocks: { a: { id: 'a', start: 0, rows: [['Open', 1]], requestSeq: 1, resetVersion: 1, sort: [] } },
      }
      const table = { ...source, rendererID: 'tanstack', spec,
        dataState: { ...source.dataState, kind: 'windowed', payload: JSON.stringify(data) } }
      const builder = element.builder
      mergePatch({ builder: { preview: { active: true }, pages: [{ ...builder.pages[0], visuals: [{
        ...builder.pages[0].visuals[0], type: 'table', slots: [{ id: 'detail', label: 'Status', kind: 'detail', fieldId: 'orders.status', required: true }],
      }] }, builder.pages[1]] }, builderVisuals: { 'sales-chart': table } })
      await element.updateComplete
      const host = element.shadowRoot.querySelector('lv-visualization-host')
      mergePatch({ builder: { revision: { id: 'rev-unrelated', number: 8 } } })
      await element.updateComplete
      return host === element.shadowRoot.querySelector('lv-visualization-host')
    }, governedBarPreviewEnvelope('sha256:window-preserve'))
    expect(preserved).toBe(true)
  } finally { await page.close() }
})
