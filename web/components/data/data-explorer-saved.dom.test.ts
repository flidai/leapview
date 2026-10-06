import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/data-explorer-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
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
      response.setHeader('content-type', url.pathname.endsWith('.css') ? 'text/css' : 'text/javascript')
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
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('saved explorations render in their own row and emit the canonical current spec', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const state = await page.evaluate(async () => {
      const spec = { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 }
      const command = {
        spec, semanticModelId: 'sales', datasetId: 'orders', dimensions: ['orders.status'], metrics: [], filters: [], sort: [],
        limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {},
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [], command: { mode: 'explore', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: command },
          explore: { command, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] } }, warnings: [],
        },
        savedExplorations: { enabled: true, list: { items: [], includeArchived: false }, command: { action: 'create' }, save: { state: 'saved' } },
      })
      const element = document.createElement('lv-data-explorer') as any
      const commands: any[] = []
      element.addEventListener('lv-saved-exploration-command', (event: CustomEvent) => commands.push(event.detail))
      document.body.append(element)
      for (let index = 0; index < 20 && !element.shadowRoot?.querySelector('.saved-explorations'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const root = element.shadowRoot as ShadowRoot
      const name = root.querySelector<HTMLInputElement>('input[aria-label="Saved exploration name"]')!
      name.value = 'Orders by status'
      name.dispatchEvent(new Event('input', { bubbles: true }))
      root.querySelector<HTMLButtonElement>('.saved-exploration-actions button')!.click()
      await element.updateComplete
      let copiedURL = ''
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (value: string) => { copiedURL = value } } })
      const explorerTopBeforeShare = root.querySelector('.explorer')!.getBoundingClientRect().top
      root.querySelector<HTMLElement>('.saved-exploration-sharing summary')!.click()
      const explorerTopAfterShare = root.querySelector('.explorer')!.getBoundingClientRect().top
      root.querySelector<HTMLButtonElement>('.saved-exploration-sharing button')!.click()
      for (let index = 0; index < 10 && !root.querySelector('.saved-exploration-sharing [role="status"]'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const shareStatus = root.querySelector('.saved-exploration-sharing [role="status"]')?.textContent?.trim()
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async () => { throw new Error('unavailable') } } })
      root.querySelector<HTMLButtonElement>('.saved-exploration-sharing button')!.click()
      for (let index = 0; index < 10 && !root.querySelector('.saved-exploration-share-fallback'); index += 1) {
        await element.updateComplete
        await new Promise((resolve) => requestAnimationFrame(resolve))
      }
      const exportFormats = Array.from(root.querySelectorAll<HTMLAnchorElement>('.saved-exploration-sharing a[href^="/explore/export"]')).map((link) => new URL(link.href).searchParams.get('format'))
      mergePatch({ dataExplorer: { explore: { result: { truncated: true } } } })
      await element.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))
      return {
        routeClasses: root.querySelector('.route')!.className,
        gridRows: getComputedStyle(root.querySelector('.route')!).gridTemplateRows,
        command: commands[0],
        copiedURL,
        shareStatus,
        sharePanelOpen: root.querySelector<HTMLDetailsElement>('.saved-exploration-sharing')?.open,
        shareTriggerInHeader: Boolean(root.querySelector('.header .saved-exploration-sharing summary')),
        shareTriggerInSavedBar: Boolean(root.querySelector('.saved-explorations .saved-exploration-sharing summary')),
        explorerTopBeforeShare,
        explorerTopAfterShare,
        fallbackURL: root.querySelector<HTMLAnchorElement>('.saved-exploration-share-fallback')?.href,
        exportFormats,
        truncatedExportLinks: root.querySelectorAll('.saved-exploration-sharing a[href^="/explore/export"]').length,
        truncatedExportMessage: root.querySelector('.saved-exploration-export-unavailable')?.textContent?.trim(),
      }
    })
    expect(state.routeClasses).toContain('saved-enabled')
    expect(state.gridRows.split(' ').length).toBeGreaterThanOrEqual(3)
    expect(state.command).toMatchObject({ action: 'create', title: 'Orders by status', visibility: 'private', spec: { modelId: 'sales', datasetId: 'orders' } })
    expect(new URL(state.copiedURL).searchParams.get('mode')).toBe('explore')
    expect(state.shareStatus).toBe('Link copied.')
    expect(state.sharePanelOpen).toBe(true)
    expect(state.shareTriggerInHeader).toBe(true)
    expect(state.shareTriggerInSavedBar).toBe(false)
    expect(state.explorerTopAfterShare).toBe(state.explorerTopBeforeShare)
    expect(state.fallbackURL).toBe(state.copiedURL)
    expect(state.exportFormats).toEqual(['csv', 'parquet'])
    expect(state.truncatedExportLinks).toBe(2)
    expect(state.truncatedExportMessage).toBe('Export complete results up to 10,000 rows and 32 MiB. Add filters for larger results.')
    await page.locator('.saved-exploration-sharing > summary').focus()
    await page.keyboard.press('Escape')
    await page.setViewportSize({ width: 390, height: 820 })
    expect(await page.locator('.saved-exploration-actions').isVisible()).toBe(false)
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    expect(await page.locator('.saved-exploration-actions').isVisible()).toBe(true)
    await page.keyboard.press('Escape')
    expect(await page.locator('.saved-exploration-actions').isVisible()).toBe(false)
    await page.locator('.saved-exploration-sharing > summary').click()
    const shareMenuBounds = await page.evaluate(() => document.querySelector('lv-data-explorer')?.shadowRoot?.querySelector('.saved-exploration-sharing-actions')?.getBoundingClientRect().toJSON())
    expect(shareMenuBounds?.width).toBeGreaterThan(200)
    expect(shareMenuBounds?.left).toBeGreaterThanOrEqual(0)
    expect(shareMenuBounds?.right).toBeLessThanOrEqual(390)
    await page.locator('.saved-exploration-sharing > summary').focus()
    await page.keyboard.press('Escape')
    expect(await page.locator('.saved-exploration-sharing').evaluate((menu: HTMLDetailsElement) => menu.open)).toBe(false)
    await page.locator('.saved-exploration-sharing > summary').click()
    await page.locator('h1').click()
    expect(await page.locator('.saved-exploration-sharing').evaluate((menu: HTMLDetailsElement) => menu.open)).toBe(false)
  } finally {
    await page.close()
  }
})

test('empty saved controls stay hidden while browsing raw rows', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    const state = await page.evaluate(async () => {
      const staleExploreCommand = {
        spec: { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [], limit: 100 },
        semanticModelId: 'sales', datasetId: 'orders', dimensions: [], metrics: [], filters: [], sort: [],
        limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {},
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [], command: { mode: 'browse', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: staleExploreCommand },
          explore: { command: staleExploreCommand, semanticModels: [], datasets: [], fields: [], result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] } }, warnings: [],
        },
        savedExplorations: { enabled: true, list: { items: [], includeArchived: false }, command: { action: 'create' }, save: { state: 'saved' } },
      })
      const element = document.createElement('lv-data-explorer') as any
      document.body.append(element)
      await element.updateComplete
      await new Promise((resolve) => requestAnimationFrame(resolve))
      const root = element.shadowRoot as ShadowRoot
      return {
        routeClasses: root.querySelector('.route')!.className,
        savedControlsPresent: Boolean(root.querySelector('.saved-explorations')),
      }
    })
    expect(state).toEqual({ routeClasses: 'route', savedControlsPresent: false })
  } finally {
    await page.close()
  }
})

test('saved actions stay compact, preserve command semantics, and remain usable after an error', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-data-explorer'))
    await page.evaluate(async () => {
      const spec = { schemaVersion: 1, modelId: 'sales', datasetId: 'orders', dimensions: [{ field: 'orders.status' }], metrics: [], filters: [], sort: [], limit: 100 }
      const command = { spec, semanticModelId: 'sales', datasetId: 'orders', dimensions: ['orders.status'], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const revision = { revisionId: 'rev-1', revision: 1 }
      const object = { key: 'model:orders', resourceId: 'model:orders', layer: 'model', semanticModelId: 'sales', datasetId: 'orders', title: 'Orders', columnCount: 1, columns: [{ key: 'status', label: 'Status' }] }
      mergePatch({
        page: { kind: 'data', title: 'Data Explorer', tabs: [] },
        dataExplorer: {
          objects: [object], selectedObject: object, selectedKey: object.key, command: { mode: 'explore', objectKey: object.key, offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: command },
          explore: { command, semanticModels: [], datasets: [], fields: [], result: { columns: object.columns, rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] } }, warnings: [],
        },
        savedExplorations: {
          enabled: true, list: { items: [{ id: 'saved-1', title: 'Orders by status', status: 'active' }], selectedId: 'saved-1', includeArchived: false }, command: { action: 'create' }, save: { state: 'saved' },
          current: { id: 'saved-1', title: 'Orders by status', slug: 'orders-by-status', visibility: 'private', status: 'active', detached: true, revision, spec },
        },
      })
      const element = document.createElement('lv-data-explorer')
      ;(window as any).savedCommands = []
      element.addEventListener('lv-saved-exploration-command', (event: Event) => (window as any).savedCommands.push((event as CustomEvent).detail))
      document.body.append(element)
    })
    const bar = page.getByRole('region', { name: 'Saved explorations', exact: true })
    await page.getByRole('button', { name: 'Save', exact: true }).click()
    expect(await page.evaluate(() => (window as any).savedCommands.at(-1))).toMatchObject({ action: 'update', explorationId: 'saved-1', spec: { limit: 100 }, expectedRevision: { revisionId: 'rev-1' } })
    expect(await bar.getByRole('textbox').count()).toBe(0)
    await page.getByRole('button', { name: 'Save as…', exact: true }).click()
    await page.getByRole('textbox', { name: 'Current query name', exact: true }).fill('Current draft copy')
    await page.getByRole('button', { name: 'Save current query as a copy', exact: true }).click()
    expect(await page.evaluate(() => (window as any).savedCommands.at(-1))).toMatchObject({ action: 'create', title: 'Current draft copy', visibility: 'private', spec: { limit: 100 } })
    await page.keyboard.press('Escape')
    expect(await page.getByRole('button', { name: 'Save as…', exact: true }).evaluate((button) => button.matches(':focus'))).toBe(true)
    await page.getByRole('button', { name: 'More saved exploration actions' }).click()
    await page.getByLabel('Saved exploration visibility', { exact: true }).selectOption('organization')
    expect(await page.getByRole('button', { name: 'Archive', exact: true }).isDisabled()).toBe(true)
    await page.getByLabel('Saved exploration visibility', { exact: true }).selectOption('private')
    expect(await page.getByRole('button', { name: 'Archive', exact: true }).isEnabled()).toBe(true)
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ savedExplorations: { save: { state: 'saving' } } })
    })
    expect(await page.getByLabel('Saved exploration visibility', { exact: true }).isDisabled()).toBe(true)
    expect(await page.getByRole('button', { name: 'Save', exact: true }).isDisabled()).toBe(true)
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ savedExplorations: { save: { state: 'saved' } } })
    })
    await page.getByRole('textbox', { name: 'Duplicate saved exploration name' }).fill('Stored version copy')
    await page.getByRole('button', { name: 'Duplicate saved version', exact: true }).click()
    expect(await page.evaluate(() => (window as any).savedCommands.at(-1))).toMatchObject({ action: 'duplicate', sourceExplorationId: 'saved-1', title: 'Stored version copy' })
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ savedExplorations: { save: { state: 'dirty' } } })
    })
    expect(await page.getByRole('button', { name: 'Archive', exact: true }).isDisabled()).toBe(true)
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ savedExplorations: { save: { state: 'error', message: 'Save failed. Try again.' } } })
    })
    expect(await page.getByRole('alert').textContent()).toContain('Save failed')
    expect(await page.getByRole('button', { name: 'Save', exact: true }).isVisible()).toBe(true)
    await page.setViewportSize({ width: 500, height: 620 })
    await page.getByLabel('Share or export exploration').click()
    const share = await page.locator('.saved-exploration-sharing-actions').boundingBox()
    expect(share!.x).toBeGreaterThanOrEqual(0)
    expect(share!.x + share!.width).toBeLessThanOrEqual(500)
    await page.keyboard.press('Escape')
    await page.getByRole('button', { name: 'More saved exploration actions' }).click()
    await page.setViewportSize({ width: 390, height: 620 })
    const menu = await page.locator('#saved-version-actions').boundingBox()
    expect(menu!.x).toBeGreaterThanOrEqual(0)
    expect(menu!.x + menu!.width).toBeLessThanOrEqual(390)
    await page.locator('h1').click()
    expect(await page.locator('#saved-version-actions').isVisible()).toBe(false)
    await page.getByLabel('Open saved explorations', { exact: true }).click()
    await page.keyboard.press('Escape')
    expect(await page.locator('.saved-exploration-picker').evaluate((menu: HTMLDetailsElement) => menu.open)).toBe(false)
  } finally { await page.close() }
}, 15_000)

function testDocument() {
  return `
    <!doctype html>
    <html>
      <head>
        <link rel="stylesheet" href="/static/app.css" />
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { font-family: Inter, system-ui, sans-serif; }
          lv-data-explorer { display: block; min-height: 720px; }
        </style>
      </head>
      <body>
        <main data-signals="{}"></main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/data-explorer-under-test.js"></script>
      </body>
    </html>
  `
}
