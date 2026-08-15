import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/dashboard-builder-test')

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

test('dashboard builder renders field explorer, canvas, and properties with typed actions', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      let builderCommand = false
      element.addEventListener('lv-builder-command', () => { builderCommand = true }, { once: true })
      ;(root.querySelector('.field') as HTMLButtonElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        panes: Array.from(root.querySelectorAll('.pane-title')).map((node) => node.textContent?.trim()),
        pageTabs: root.querySelectorAll('.page-tab[role="tab"]').length,
        visuals: root.querySelectorAll('.visual').length,
        diagnostics: root.querySelectorAll('.diagnostic').length,
        evidence: root.querySelector('.evidence')?.textContent?.trim(),
        builderCommand,
      }
    })
    expect(state.title).toBe('Revenue draft')
    expect(state.panes).toEqual(['Orders', 'Visual properties'])
    expect(state.pageTabs).toBe(2)
    expect(state.visuals).toBe(1)
    expect(state.diagnostics).toBe(1)
    expect(state.evidence).toContain('workspace')
    expect(state.builderCommand).toBe(true)
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps an accessible responsive surface and exposes loading state', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const buttonLabels = Array.from(root.querySelectorAll('button')).map((button) => button.getAttribute('aria-label') || button.textContent?.trim())
      const responsiveDisplay = getComputedStyle(root.querySelector('.body') as HTMLElement).display
      const hasSearchLabel = Boolean(root.querySelector('label input[aria-label], label .sr-only'))
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: undefined, status: { loading: true, error: '', generation: 0, lastUpdated: '', refreshId: '', setupRequired: false, progressPercent: 0 } })
      await element.updateComplete
      const loadingState = root.querySelector('.state') as HTMLElement | null
      return { display: responsiveDisplay, hasSearchLabel, buttonLabels, loading: loadingState?.textContent?.trim() }
    })
    expect(state.display).toBe('block')
    expect(state.hasSearchLabel).toBe(true)
    expect(state.buttonLabels).toContain('Publish')
    expect(state.loading).toContain('Loading dashboard builder')
  } finally {
    await page.close()
  }
})

test('dashboard builder exposes the full mobile surface through one vertical scroll container', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const properties = root.querySelector('.properties') as HTMLElement
      const initial = properties.getBoundingClientRect()
      const host = element as HTMLElement
      host.scrollTop = host.scrollHeight - host.clientHeight
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const reachable = properties.getBoundingClientRect()
      return {
        hostOverflowY: getComputedStyle(host).overflowY,
        hostScrollHeight: host.scrollHeight,
        hostClientHeight: host.clientHeight,
        propertiesInitiallyBelowViewport: initial.top >= innerHeight,
        propertiesReachable: reachable.top < innerHeight && reachable.bottom > 0,
        hostHorizontalOverflow: host.scrollWidth > host.clientWidth,
        documentHorizontalOverflow: document.documentElement.scrollWidth > innerWidth || document.body.scrollWidth > innerWidth,
      }
    })
    expect(state.hostOverflowY).toBe('auto')
    expect(state.hostScrollHeight).toBeGreaterThan(state.hostClientHeight)
    expect(state.propertiesInitiallyBelowViewport).toBe(true)
    expect(state.propertiesReachable).toBe(true)
    expect(state.hostHorizontalOverflow).toBe(false)
    expect(state.documentHorizontalOverflow).toBe(false)
  } finally {
    await page.close()
  }
})

test('dashboard builder selects a newly added page after the authoritative command patch', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('button[aria-label="Add page"]') as HTMLButtonElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))

      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        builder: {
          pages: [
            {
              id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 },
              visuals: [{ id: 'sales-chart', title: 'Sales by status', type: 'bar', placement: { col: 1, row: 1, colSpan: 6, rowSpan: 5 }, slots: [{ id: 'category', label: 'Category', kind: 'dimension', fieldId: 'orders.status', required: true }], filters: [] }],
            },
            { id: 'details', title: 'Details', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [] },
            { id: 'page-2', title: 'Page 2', canvas: { width: 1366, height: 940 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [] },
          ],
          revision: { id: 'rev-8', number: 8, contentHash: 'sha256:def' },
          selectedPageId: 'overview', selectedVisualId: 'sales-chart',
        },
      })
      await element.updateComplete
      await element.updateComplete
      let visualCommand: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { visualCommand = event.detail }, { once: true })
      ;(root.querySelector('.add-visual button') as HTMLButtonElement).click()
      return {
        commandPageID: command?.pageId,
        visualCommandPageID: visualCommand?.pageId,
        selectedTab: root.querySelector('.page-tab[aria-selected="true"]')?.textContent?.trim(),
        selectedProperties: root.querySelector('.property-value')?.textContent?.trim(),
        emptyCanvas: Boolean(root.querySelector('.visual-empty')),
      }
    })
    expect(state.commandPageID).toBe('')
    expect(state.visualCommandPageID).toBe('page-2')
    expect(state.selectedTab).toBe('Page 2')
    expect(state.selectedProperties).toBe('Page 2')
    expect(state.emptyCanvas).toBe(true)
  } finally {
    await page.close()
  }
})

test('dashboard builder follows the streamed exact-revision preview href', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const initialHref = root.querySelector('a.button')?.getAttribute('href') ?? ''
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        builder: {
          revision: { id: 'rev-8', number: 8, contentHash: 'sha256:def' },
          preview: { href: '/workspaces/sales/dashboards/revenue/preview?draft=draft-7&revisionId=rev-8&revisionNumber=8&revisionContentHash=sha256%3Adef' },
        },
      })
      await element.updateComplete
      return { initialHref, updatedHref: root.querySelector('a.button')?.getAttribute('href') ?? '' }
    })
    expect(state.initialHref).toContain('revisionNumber=7')
    expect(state.updatedHref).toContain('revisionNumber=8')
    expect(state.updatedHref).toContain('revisionId=rev-8')
    expect(state.updatedHref).not.toContain('revisionNumber=6')
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  const signals = {
    builder: {
      workspaceId: 'sales', dashboardId: 'revenue', draftId: 'draft-7',
      revision: { id: 'rev-7', number: 7, contentHash: 'sha256:abc' },
      title: 'Revenue draft', lifecycle: 'draft', visibility: 'private', hasUnpublishedChanges: true,
      origin: { kind: 'file', label: 'Project file', sourcePath: 'dashboards/revenue.yaml' },
      sourceEvidence: { kind: 'workspace', workspaceId: 'sales', dashboardId: 'revenue', revision: { id: 'rev-7', number: 7, contentHash: 'sha256:abc' } },
      semanticModel: { id: 'commerce', title: 'Orders', tables: [{ id: 'orders', title: 'Orders', fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', dataType: 'string' }, { id: 'orders.total', label: 'Total', kind: 'measure', dataType: 'decimal' }] }] },
      pages: [
        { id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [{ id: 'sales-chart', title: 'Sales by status', type: 'bar', placement: { col: 1, row: 1, colSpan: 6, rowSpan: 5 }, slots: [{ id: 'category', label: 'Category', kind: 'dimension', fieldId: 'orders.status', required: true }], filters: [] }] },
        { id: 'details', title: 'Details', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [] },
      ],
      selectedPageId: 'overview', selectedVisualId: 'sales-chart',
      capabilities: { canEdit: true, canShare: true, canPublish: true, canPreview: true, canExport: true, canAddPage: true, canAddVisual: true },
      diagnostics: [{ severity: 'warning', code: 'FIELD_REQUIRED', message: 'Add a measure to complete this visual.' }],
      preview: { active: false, mode: 'draft', loading: false, href: '/workspaces/sales/dashboards/revenue/preview?draft=draft-7&revisionId=rev-7&revisionNumber=7&revisionContentHash=sha256%3Aabc' }, save: { state: 'dirty', message: '2 changes' },
    },
    status: { loading: false, error: '', generation: 0, lastUpdated: '', refreshId: '', setupRequired: false, progressPercent: 100 },
    runtime: { kind: 'dashboard_builder', workspaceId: 'sales', dashboardId: 'revenue' },
  }
  return `<!doctype html><html><head><style>html,body{margin:0;min-height:100%;}body{${typographyTestTokens}--lv-bg-app:#f6f8fa;--lv-bg-panel:#fff;--lv-fg-default:#24292f;--lv-fg-muted:#57606a;--lv-border-muted:#d8dee4;--lv-border-default:#d0d7de;}</style></head><body><main data-signals="${escapeHTML(JSON.stringify(signals))}"><lv-dashboard-builder back-href="/dashboards/revenue" preview-href="/workspaces/sales/dashboards/revenue/preview?draft=draft-7&revisionId=rev-6&revisionNumber=6&revisionContentHash=sha256%3Aold"></lv-dashboard-builder></main><script type="module" src="/dashboard-builder-under-test.js"></script><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script></body></html>`
}

function escapeHTML(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}
