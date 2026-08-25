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
    expect(state.evidence).toContain('project')
    expect(state.builderCommand).toBe(true)
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps metadata quiet and groups secondary actions behind More', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const toolbar = root.querySelector('.toolbar-actions') as HTMLElement
      return {
        badgeCount: root.querySelectorAll('.meta .badge').length,
        metadataLines: root.querySelectorAll('.meta > span').length,
        topLevelActions: Array.from(toolbar.children).map((child) => child.localName === 'details' ? 'more' : child.textContent?.trim()),
        moreLabel: root.querySelector('.more-actions summary')?.textContent?.trim(),
        moreAriaLabel: root.querySelector('.more-actions summary')?.getAttribute('aria-label'),
      }
    })
    expect(state.badgeCount).toBe(0)
    expect(state.metadataLines).toBe(1)
    expect(state.topLevelActions).toEqual(['Preview', 'more', 'Publish'])
    expect(state.moreLabel).toBe('More')
    expect(state.moreAriaLabel).toBe('More dashboard actions')
  } finally {
    await page.close()
  }
})

test('dashboard builder renders governed previews without nesting an interactive host in a button', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const revision = 'sha256:builder-preview'
      const dataState = { kind: 'inline', specRevision: revision, dataRevision: 1, generation: 1, datasets: [] }
      mergePatch({
        builderVisuals: {
          'sales-chart': {
            schemaVersion: 10, visualID: 'sales-chart', rendererID: 'echarts', specRevision: revision, dataRevision: 1,
            spec: { kind: 'cartesian', title: 'Sales by status', fields: [], x: { dataset: 'primary', field: 'category' }, y: [{ dataset: 'primary', field: 'value' }] },
            dataState: { schemaVersion: 1, encoding: 'json', kind: 'inline', specRevision: revision, dataRevision: 1, generation: 1, payload: JSON.stringify(dataState) },
            selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [], servingStateID: 'serving-test', streamGeneration: 1, filterRevision: 0, interactionRevision: 0, consumerIdentity: 'visual:sales-chart',
          },
        },
      })
      await element.updateComplete
      const root = element.shadowRoot
      const host = root.querySelector('.visual-preview lv-visualization-host') as any
      const previewWrapper = root.querySelector('.visual-preview') as HTMLElement | null
      const hostBox = host?.getBoundingClientRect()
      const wrapperBox = previewWrapper?.getBoundingClientRect()
      return {
        hostCount: root.querySelectorAll('.visual-preview lv-visualization-host').length,
        visualTag: root.querySelector('.visual')?.localName,
        visualRole: root.querySelector('.visual')?.getAttribute('role'),
        hostVisualID: host?.envelope?.visualID,
        hostPointerEvents: host ? getComputedStyle(host).pointerEvents : '',
        wrapperHeight: wrapperBox?.height ?? 0,
        hostHeight: hostBox?.height ?? 0,
        emptyPreviewCount: root.querySelectorAll('.visual-preview-empty').length,
        previewTitleCount: root.querySelectorAll('.visual-preview ~ .visual-title').length,
        previewTypeCount: root.querySelectorAll('.visual-preview ~ .visual-type').length,
      }
    })
    expect(state.hostCount).toBe(1)
    expect(state.visualTag).toBe('div')
    expect(state.visualRole).toBe('button')
    expect(state.hostVisualID).toBe('sales-chart')
    expect(state.hostPointerEvents).toBe('none')
    expect(state.wrapperHeight).toBeGreaterThan(0)
    expect(state.hostHeight).toBeGreaterThan(0)
    expect(state.emptyPreviewCount).toBe(0)
    expect(state.previewTitleCount).toBe(0)
    expect(state.previewTypeCount).toBe(0)
  } finally {
    await page.close()
  }
})

test('dashboard builder uses a full-bleed central canvas and keeps no-preview guidance actionable', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const scroll = root.querySelector('.canvas-scroll') as HTMLElement
      const canvas = root.querySelector('.canvas') as HTMLElement
      return {
        canvasScrollCount: root.querySelectorAll('.canvas-scroll').length,
        scrollPadding: getComputedStyle(scroll).padding,
        canvasBorder: getComputedStyle(canvas).border,
        canvasRadius: getComputedStyle(canvas).borderRadius,
        canvasShadow: getComputedStyle(canvas).boxShadow,
        emptyPreview: root.querySelector('.visual-preview-empty')?.textContent?.trim(),
        addPageLabel: root.querySelector('button[aria-label="Add page"]')?.textContent?.trim(),
      }
    })
    expect(state.canvasScrollCount).toBe(1)
    expect(state.scrollPadding).toBe('0px')
    expect(state.canvasBorder).toMatch(/^0px none /)
    expect(state.canvasRadius).toBe('0px')
    expect(state.canvasShadow).toBe('none')
    expect(state.emptyPreview).toContain('Add fields')
    expect(state.addPageLabel).toBe('Add page')
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

test('dashboard builder stacks visual tiles within the mobile canvas viewport', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const visual = (id: string, title: string, row: number, type = 'bar') => ({
        id, title, type, placement: { col: 1, row, colSpan: 4, rowSpan: 4 },
        slots: [], filters: [],
      })
      mergePatch({
        builder: {
          pages: [{
            id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 },
        // Stream order is intentionally different from authored canvas order.
            visuals: [visual('three', 'Three', 11, 'table'), visual('one', 'One', 1, 'kpi'), visual('two', 'Two', 6)],
          }],
          selectedPageId: 'overview', selectedVisualId: 'one',
        },
      })
      await element.updateComplete
      const root = element.shadowRoot
      const canvas = root.querySelector('.canvas') as HTMLElement
      const scroll = root.querySelector('.canvas-scroll') as HTMLElement
      const canvasBox = canvas.getBoundingClientRect()
      const visuals = Array.from(root.querySelectorAll('.visual')).map((node) => {
        const box = (node as HTMLElement).getBoundingClientRect()
        const tile = node as HTMLElement
        const style = getComputedStyle(tile)
        return { title: tile.querySelector('.visual-title')?.textContent?.trim(), left: box.left, right: box.right, top: box.top, bottom: box.bottom, height: box.height, type: tile.getAttribute('data-visual-type'), position: style.position, order: style.order, topOffset: style.top, leftOffset: style.left, authoredTop: tile.style.top }
      })
      return {
        canvasWidth: canvasBox.width,
        canvasHeight: canvasBox.height,
        scrollHeight: scroll.scrollHeight,
        scrollWidth: scroll.scrollWidth,
        scrollClientWidth: scroll.clientWidth,
        visuals,
        documentHorizontalOverflow: document.documentElement.scrollWidth > innerWidth || document.body.scrollWidth > innerWidth,
      }
    })
    expect(state.canvasWidth).toBeGreaterThan(0)
    expect(state.canvasHeight).toBeLessThan(1000)
    expect(state.scrollHeight).toBeLessThan(1200)
    expect(state.scrollWidth).toBeLessThanOrEqual(state.scrollClientWidth)
    expect(state.documentHorizontalOverflow).toBe(false)
    expect(state.visuals).toHaveLength(3)
    expect(state.visuals.map((visual) => visual.title)).toEqual(['Three', 'One', 'Two'])
    expect(state.visuals.map((visual) => visual.order)).toEqual(['2', '0', '1'])
    const kpi = state.visuals.find((visual) => visual.type === 'kpi')
    const chart = state.visuals.find((visual) => visual.type === 'bar')
    const table = state.visuals.find((visual) => visual.type === 'table')
    expect(kpi?.height).toBeLessThan(chart?.height ?? 0)
    expect(table?.height).toBeLessThanOrEqual(256)
    const flow = [...state.visuals].sort((left, right) => Number(left.order) - Number(right.order))
    for (const visual of state.visuals) {
      expect(visual.position).toBe('relative')
      // Relative flow resolves auto offsets to 0px; authored top/left values
      // remain on the inline style but no longer offset the mobile tile.
      expect(visual.topOffset).toBe('0px')
      expect(visual.leftOffset).toBe('0px')
      expect(visual.left).toBeGreaterThanOrEqual(-1)
      expect(visual.right).toBeLessThanOrEqual(state.canvasWidth + 1)
      expect(visual.bottom).toBeGreaterThan(visual.top)
    }
    expect(flow[1].authoredTop).not.toBe('0px')
    expect(flow[2].authoredTop).not.toBe('0px')
    expect(flow[1].top).toBeGreaterThan(flow[0].bottom)
    expect(flow[2].top).toBeGreaterThan(flow[1].bottom)
  } finally {
    await page.close()
  }
})

test('dashboard builder can reload a page-scoped preview through page-base-href links', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const href = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      element.setAttribute('page-base-href', '/dashboards/revenue/builder?draft=draft-7')
      await element.updateComplete
      return element.shadowRoot.querySelector('.page-tab[href*="page=details"]')?.getAttribute('href')
    })
    expect(href).toBe('/dashboards/revenue/builder?draft=draft-7&page=details')
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

test('dashboard builder retains the draft and exposes terminal command recovery', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      ;(element.shadowRoot.querySelector('button[aria-label="Add page"]') as HTMLButtonElement).click()
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: document.body, argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(resolve))
      await element.updateComplete
      const unrelatedIgnored = element.terminalFailure == null
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(resolve))
      await element.updateComplete
      const alert = element.shadowRoot?.querySelector('[role="alert"]') as HTMLElement | null
      const buttons = Array.from(alert?.querySelectorAll('button') ?? []) as HTMLButtonElement[]
      const beforeDismiss = {
        title: element.shadowRoot?.querySelector('h1')?.textContent?.trim(),
        pageCount: element.shadowRoot?.querySelectorAll('.page-tab[role="tab"]').length,
        failureKind: element.terminalFailure?.kind,
        message: alert?.textContent?.trim(),
        actions: buttons.map((button) => button.textContent?.trim()),
      }
      buttons.find((button) => button.textContent?.includes('Dismiss'))?.click()
      await element.updateComplete
      return { ...beforeDismiss, unrelatedIgnored, alertAfterDismiss: Boolean(element.shadowRoot?.querySelector('[role="alert"]')) }
    })
    expect(state.title).toBe('Revenue draft')
    expect(state.pageCount).toBe(2)
    expect(state.failureKind).toBe('unavailable')
    expect(state.message).toContain('previous state was kept')
    expect(state.actions).toEqual(['Reload latest draft', 'Dismiss'])
    expect(state.unrelatedIgnored).toBe(true)
    expect(state.alertAfterDismiss).toBe(false)
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
          preview: { href: '/dashboards/revenue/preview?draft=draft-7&revisionId=rev-8&revisionNumber=8&revisionContentHash=sha256%3Adef' },
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
      projectId: 'sales', dashboardId: 'revenue', draftId: 'draft-7',
      revision: { id: 'rev-7', number: 7, contentHash: 'sha256:abc' },
      title: 'Revenue draft', lifecycle: 'draft', visibility: 'private', hasUnpublishedChanges: true,
      origin: { kind: 'file', label: 'Project file', sourcePath: 'dashboards/revenue.yaml' },
      sourceEvidence: { kind: 'project', projectId: 'sales', dashboardId: 'revenue', generationId: 'generation-7' },
      semanticModel: { id: 'commerce', title: 'Orders', datasets: [{ id: 'orders', title: 'Orders', fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', dataType: 'string' }, { id: 'orders.total', label: 'Total', kind: 'metric', dataType: 'decimal' }] }] },
      pages: [
        { id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [{ id: 'sales-chart', title: 'Sales by status', type: 'bar', placement: { col: 1, row: 1, colSpan: 6, rowSpan: 5 }, slots: [{ id: 'category', label: 'Category', kind: 'dimension', fieldId: 'orders.status', required: true }], filters: [] }] },
        { id: 'details', title: 'Details', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [] },
      ],
      selectedPageId: 'overview', selectedVisualId: 'sales-chart',
      capabilities: { canEdit: true, canShare: true, canPublish: true, canPreview: true, canExport: true, canAddPage: true, canAddVisual: true },
      diagnostics: [{ severity: 'warning', code: 'FIELD_REQUIRED', message: 'Add a metric to complete this visual.' }],
      preview: { active: false, mode: 'draft', loading: false, href: '/dashboards/revenue/preview?draft=draft-7&revisionId=rev-7&revisionNumber=7&revisionContentHash=sha256%3Aabc' }, save: { state: 'dirty', message: '2 changes' },
    },
    status: { loading: false, error: '', generation: 0, lastUpdated: '', refreshId: '', setupRequired: false, progressPercent: 100 },
    runtime: { kind: 'dashboard_builder', projectId: 'sales', servingStateId: 'generation-7', dashboardId: 'revenue' },
  }
  return `<!doctype html><html><head><style>html,body{margin:0;min-height:100%;}body{${typographyTestTokens}--lv-bg-app:#f6f8fa;--lv-bg-panel:#fff;--lv-bg-panel-muted:#f6f8fa;--lv-bg-control:#f6f8fa;--lv-bg-input:#fff;--lv-bg-accent-muted:#ddf4ff;--lv-bg-danger-muted:#ffebe9;--lv-fg-default:#24292f;--lv-fg-muted:#57606a;--lv-fg-accent:#0969da;--lv-fg-danger:#d1242f;--lv-fg-warning:#9a6700;--lv-fg-success:#1a7f37;--lv-border-muted:#d8dee4;--lv-border-default:#d0d7de;--lv-line-default:#d0d7de;--lv-line-muted:#d8dee4;--lv-border-width-focus:2px;--lv-radius-default:6px;--lv-radius-small:4px;--lv-radius-full:999px;--base-size-2:2px;--base-size-4:4px;--base-size-6:6px;--base-size-8:8px;--base-size-12:12px;--base-size-16:16px;--control-medium-size:32px;--control-small-size:28px;--lv-button-radius:6px;--lv-button-padding-inline:12px;--lv-button-fg-rest:#24292f;--lv-button-bg-rest:#fff;--lv-button-bg-hover:#f6f8fa;--lv-button-accent-border-rest:#0969da;--lv-button-accent-fg-rest:#fff;--lv-button-accent-bg-rest:#0969da;--lv-button-accent-bg-hover:#0757b3;--lv-shadow-floating-sm:0 2px 8px rgb(0 0 0 / 12%);}</style></head><body><main data-signals="${escapeHTML(JSON.stringify(signals))}"><lv-dashboard-builder back-href="/dashboards/revenue" preview-href="/dashboards/revenue/preview?draft=draft-7&revisionId=rev-6&revisionNumber=6&revisionContentHash=sha256%3Aold"></lv-dashboard-builder></main><script type="module" src="/dashboard-builder-under-test.js"></script><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script></body></html>`
}

function escapeHTML(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}
