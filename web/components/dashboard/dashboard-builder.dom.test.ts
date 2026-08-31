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

test('dashboard builder renders bottom page tabs, canvas, and visual builder with typed actions', async () => {
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
        pageNavigation: root.querySelector('.page-bar .page-tabs')?.getAttribute('aria-label'),
        visualBuilder: root.querySelector('.visual-builder .pane-title')?.textContent?.trim(),
        pageTabs: root.querySelectorAll('.page-tab[role="tab"]').length,
        visuals: root.querySelectorAll('.visual').length,
        diagnostics: root.querySelectorAll('.diagnostic').length,
        evidence: root.querySelector('.evidence')?.textContent?.trim(),
        builderCommand,
      }
    })
    expect(state.title).toBe('Revenue draft')
    expect(state.pageNavigation).toBe('Dashboard pages')
    expect(state.visualBuilder).toBe('Sales by status')
    expect(state.pageTabs).toBe(2)
    expect(state.visuals).toBe(1)
    expect(state.diagnostics).toBe(1)
    expect(state.evidence).toContain('project')
    expect(state.builderCommand).toBe(true)
  } finally {
    await page.close()
  }
})

test('dashboard builder places the page tab bar below the canvas without consuming a side rail', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const body = root.querySelector('.body') as HTMLElement
      const regions = Array.from(body.children).map((child) => child.className)
      const boxes = ['.canvas-pane', '.page-bar', '.right-dock'].map((selector) => {
        const box = (root.querySelector(selector) as HTMLElement).getBoundingClientRect()
        return { selector, left: box.left, right: box.right, width: box.width }
      })
      const canvas = root.querySelector('.canvas-pane')?.getBoundingClientRect()
      const pageBar = root.querySelector('.page-bar')?.getBoundingClientRect()
      const visualBuilder = root.querySelector('.visual-builder')?.getBoundingClientRect()
      const dataPane = root.querySelector('.data-pane')?.getBoundingClientRect()
      const pickerCatalog = root.querySelector('.visual-picker-catalog') as HTMLElement
      const pickerGrid = root.querySelector('.visual-picker') as HTMLElement
      const pickerButtons = Array.from(root.querySelectorAll('.visual-picker-button')).map((button) => ({
        type: button.getAttribute('data-visual-type'),
        group: button.getAttribute('data-visual-group'),
        label: button.getAttribute('aria-label'),
        title: button.getAttribute('title'),
        hasIcon: Boolean(button.querySelector('svg')),
        hasVisibleLabel: Boolean(button.querySelector(':scope > span:not(.sr-only)')),
        iconType: button.querySelector('svg')?.getAttribute('data-icon-type'),
        filledMarks: button.querySelectorAll('svg .visual-icon-primary, svg .visual-icon-secondary, svg .visual-icon-tertiary').length,
        color: getComputedStyle(button).color,
      }))
      const tabs = Array.from(root.querySelectorAll('.inspector-tab')).map((tab) => ({
        id: tab.getAttribute('data-inspector-tab'),
        label: tab.textContent?.trim(),
        selected: tab.getAttribute('aria-selected'),
      }))
      return {
        regions,
        boxes,
        pickerButtons,
        pickerGroups: [...new Set(pickerButtons.map((button) => button.group))],
        pickerColumns: getComputedStyle(pickerGrid).gridTemplateColumns.split(' ').length,
        pickerHasScroll: pickerCatalog.scrollHeight > pickerCatalog.clientHeight || pickerCatalog.scrollWidth > pickerCatalog.clientWidth,
        referenceHref: root.querySelector<HTMLAnchorElement>('.visual-reference-link')?.getAttribute('href'),
        tabs,
        panel: root.querySelector('[role="tabpanel"]')?.getAttribute('aria-label'),
        pageBarBelowCanvas: Boolean(canvas && pageBar && pageBar.top >= canvas.top + canvas.height - 1),
        pageBarSharesCanvasWidth: Boolean(canvas && pageBar && pageBar.left >= canvas.left - 1 && pageBar.right <= canvas.right + 1),
        pageBarBeforeVisualBuilder: Boolean(pageBar && visualBuilder && pageBar.bottom <= visualBuilder.bottom + 1),
        rightDockContainsAuthoringPanes: root.querySelectorAll('.right-dock > .filters-pane, .right-dock > .visual-builder, .right-dock > .data-pane').length === 3,
        dataPaneRightOfVisual: Boolean(visualBuilder && dataPane && dataPane.left >= visualBuilder.right - 1),
        pageBarVisible: Boolean(pageBar && pageBar.bottom <= innerHeight + 1),
        horizontalOverflow: document.documentElement.scrollWidth > innerWidth || document.body.scrollWidth > innerWidth,
        verticalOverflow: document.documentElement.scrollHeight > innerHeight || document.body.scrollHeight > innerHeight,
      }
    })
    expect(state.regions).toEqual(['canvas-pane', 'page-bar', 'right-dock'])
    expect(state.boxes.map((region) => region.selector)).toEqual(['.canvas-pane', '.page-bar', '.right-dock'])
    expect(state.boxes[0].width).toBeGreaterThan(state.boxes[2].width)
    expect(state.pageBarBelowCanvas).toBe(true)
    expect(state.pageBarSharesCanvasWidth).toBe(true)
    expect(state.pageBarBeforeVisualBuilder).toBe(true)
    expect(state.rightDockContainsAuthoringPanes).toBe(true)
    expect(state.dataPaneRightOfVisual).toBe(true)
    expect(state.pageBarVisible).toBe(true)
    expect(state.pickerButtons).toHaveLength(26)
    expect(state.pickerButtons.map((button) => button.type)).toEqual(['line', 'area', 'bar', 'column', 'candlestick', 'combo', 'waterfall', 'pie', 'donut', 'funnel', 'scatter', 'heatmap', 'boxplot', 'histogram', 'treemap', 'sankey', 'graph', 'tree', 'sunburst', 'gauge', 'map', 'radar', 'kpi', 'table', 'matrix', 'pivot'])
    expect(state.pickerButtons.every((button) => button.hasIcon && button.label?.endsWith(' visual') && button.title)).toBe(true)
    expect(state.pickerButtons.every((button) => !button.hasVisibleLabel)).toBe(true)
    expect(state.pickerButtons.every((button) => button.iconType === button.type && button.filledMarks > 0)).toBe(true)
    expect(state.pickerGroups).toEqual(['Cartesian', 'Part to whole', 'Distribution', 'Hierarchy & flow', 'Specialized', 'Tables'])
    expect(state.pickerColumns).toBe(7)
    expect(state.pickerHasScroll).toBe(false)
    expect(state.referenceHref).toBe('/docs/visuals/bar')
    expect(state.tabs).toEqual([
      { id: 'build', label: 'Build', selected: 'true' },
      { id: 'format', label: 'Format', selected: 'false' },
    ])
    expect(state.panel).toBe('Build visual')
    expect(state.horizontalOverflow).toBe(false)
    expect(state.verticalOverflow).toBe(false)
  } finally {
    await page.close()
  }
})

test('dashboard builder collapses right panes, persists the choice, and uses icon actions', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const before = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      return {
        canvasWidth: root.querySelector('.canvas-pane').getBoundingClientRect().width,
        panes: Array.from(root.querySelectorAll('.right-dock > .pane')).map((pane: Element) => pane.getAttribute('data-collapsed')),
        historyIcons: Array.from(root.querySelectorAll('[data-builder-action="undo"], [data-builder-action="redo"]')).map((button: Element) => ({
          label: button.getAttribute('aria-label'),
          hasIcon: Boolean(button.querySelector('svg[data-lucide="icon"]')),
          text: button.textContent?.trim(),
        })),
        toggleTargets: Array.from(root.querySelectorAll('[data-pane-toggle]')).map((button: Element) => ({
          pane: button.getAttribute('data-pane-toggle'),
          controls: button.getAttribute('aria-controls'),
          controlsExistingTarget: Boolean(root.querySelector(`#${button.getAttribute('aria-controls')}`)),
          hasIcon: Boolean(button.querySelector('svg[data-lucide="icon"]')),
        })),
      }
    })

    const visualsToggle = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const root = element.shadowRoot
      ;(root.querySelector('[data-pane-toggle="visuals"]') as HTMLButtonElement).click()
      await element.updateComplete
      const collapsed = {
        pane: root.querySelector('.visual-builder')?.getAttribute('data-collapsed'),
        hidden: (root.querySelector('#builder-visuals-content') as HTMLElement).hidden,
        expanded: root.querySelector('[data-pane-toggle="visuals"]')?.getAttribute('aria-expanded'),
      }
      ;(root.querySelector('[data-pane-toggle="visuals"]') as HTMLButtonElement).click()
      await element.updateComplete
      return { collapsed, reopened: root.querySelector('.visual-builder')?.getAttribute('data-collapsed') }
    })

    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const root = element.shadowRoot
      ;(root.querySelector('[data-pane-toggle="filters"]') as HTMLButtonElement).click()
      ;(root.querySelector('[data-pane-toggle="data"]') as HTMLButtonElement).click()
      await element.updateComplete
    })
    const collapsed = await page.locator('lv-dashboard-builder').evaluate((element: any) => {
      const root = element.shadowRoot
      const filters = root.querySelector('.filters-pane') as HTMLElement
      const data = root.querySelector('.data-pane') as HTMLElement
      return {
        canvasWidth: root.querySelector('.canvas-pane').getBoundingClientRect().width,
        filtersCollapsed: filters.dataset.collapsed,
        dataCollapsed: data.dataset.collapsed,
        filtersHidden: (root.querySelector('#builder-filters-content') as HTMLElement).hidden,
        dataHidden: (root.querySelector('#builder-data-content') as HTMLElement).hidden,
        filtersExpanded: root.querySelector('[data-pane-toggle="filters"]')?.getAttribute('aria-expanded'),
        filterToggleTitle: root.querySelector('[data-pane-toggle="filters"]')?.getAttribute('title'),
      }
    })

    await page.reload()
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const restored = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      return {
        filters: root.querySelector('.filters-pane')?.getAttribute('data-collapsed'),
        visuals: root.querySelector('.visual-builder')?.getAttribute('data-collapsed'),
        data: root.querySelector('.data-pane')?.getAttribute('data-collapsed'),
      }
    })
    await page.setViewportSize({ width: 900, height: 900 })
    const responsive = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const dock = root.querySelector('.right-dock') as HTMLElement
      const filters = root.querySelector('.filters-pane') as HTMLElement
      const data = root.querySelector('.data-pane') as HTMLElement
      return {
        dockOverflow: dock.scrollWidth > dock.clientWidth + 1,
        filtersOverflow: filters.scrollWidth > filters.clientWidth + 1,
        dataOverflow: data.scrollWidth > data.clientWidth + 1,
      }
    })
    await page.setViewportSize({ width: 1100, height: 900 })
    const stacked = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const filters = root.querySelector('.filters-pane') as HTMLElement
      const data = root.querySelector('.data-pane') as HTMLElement
      return {
        filtersHeight: filters.getBoundingClientRect().height,
        dataHeight: data.getBoundingClientRect().height,
        filtersOverflow: filters.scrollHeight > filters.clientHeight + 1,
        dataOverflow: data.scrollHeight > data.clientHeight + 1,
      }
    })

    expect(before.panes).toEqual(['false', 'false', 'false'])
    expect(before.historyIcons).toEqual([
      { label: 'Undo', hasIcon: true, text: 'Undo' },
      { label: 'Redo', hasIcon: true, text: 'Redo' },
    ])
    expect(before.toggleTargets).toEqual([
      { pane: 'filters', controls: 'builder-filters-content', controlsExistingTarget: true, hasIcon: true },
      { pane: 'visuals', controls: 'builder-visuals-content', controlsExistingTarget: true, hasIcon: true },
      { pane: 'data', controls: 'builder-data-content', controlsExistingTarget: true, hasIcon: true },
    ])
    expect(visualsToggle).toEqual({ collapsed: { pane: 'true', hidden: true, expanded: 'false' }, reopened: 'false' })
    expect(collapsed.canvasWidth).toBeGreaterThan(before.canvasWidth + 200)
    expect(collapsed.filtersCollapsed).toBe('true')
    expect(collapsed.dataCollapsed).toBe('true')
    expect(collapsed.filtersHidden).toBe(true)
    expect(collapsed.dataHidden).toBe(true)
    expect(collapsed.filtersExpanded).toBe('false')
    expect(collapsed.filterToggleTitle).toBe('Expand Filters pane')
    expect(restored).toEqual({ filters: 'true', visuals: 'false', data: 'true' })
    expect(responsive).toEqual({ dockOverflow: false, filtersOverflow: false, dataOverflow: false })
    expect(stacked.filtersHeight).toBeLessThanOrEqual(57)
    expect(stacked.dataHeight).toBeLessThanOrEqual(57)
    expect(stacked.filtersOverflow).toBe(false)
    expect(stacked.dataOverflow).toBe(false)
  } finally {
    await page.close()
  }
})

test('dashboard builder changes the selected visual type without creating a visual', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const beforeCount = root.querySelectorAll('.visual').length
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      const line = root.querySelector('button[data-visual-type="line"]') as HTMLButtonElement | null
      line?.click()
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 20))
      return {
        typeButton: Boolean(line),
        beforeCount,
        afterCount: root.querySelectorAll('.visual').length,
        command,
      }
    })
    expect(state.typeButton).toBe(true)
    expect(state.beforeCount).toBe(state.afterCount)
    expect(state.command).toMatchObject({ action: 'set_visual_type', pageId: 'overview', visualId: 'sales-chart', type: 'line' })
  } finally {
    await page.close()
  }
})

test('dashboard builder reselects a legacy mismatched type to repair its query family', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const source = element.builder.pages[0].visuals[0]
      mergePatch({ builder: {
        pages: [{ ...element.builder.pages[0], visuals: [{
          ...source,
          type: 'donut',
          previewError: 'visual type "donut" is incompatible with records query',
          slots: [{ id: 'detail-0', label: 'Revenue', kind: 'detail', fieldId: 'revenue', required: false }],
        }] }, element.builder.pages[1]],
        selectedPageId: 'overview',
        selectedVisualId: source.id,
      } })
      await element.updateComplete
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      element.shadowRoot.querySelector<HTMLButtonElement>('button[data-visual-picker-type="donut"]')?.click()
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { command, message: element.shadowRoot.querySelector('.pane-header [role="status"]')?.textContent?.trim() }
    })
    expect(state.command).toMatchObject({ action: 'set_visual_type', pageId: 'overview', visualId: 'sales-chart', type: 'donut' })
    expect(state.message).toContain('Repairing')
  } finally {
    await page.close()
  }
})

test('dashboard builder explains incomplete target visuals without hidden retained fields', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const source = element.builder.pages[0].visuals[0]
      const sourceSlots = [
        { id: 'dimension-0', label: 'Status', kind: 'dimension', fieldId: 'orders.status', required: true },
        { id: 'metric-0', label: 'Total', kind: 'metric', fieldId: 'orders.total', required: true },
      ]
      const revision = 'sha256:stale-table-preview'
      const dataState = { kind: 'inline', specRevision: revision, dataRevision: 1, generation: 1, datasets: [] }
      mergePatch({ builder: {
        visualCatalog: element.builder.visualCatalog.map((entry: any) => entry.type === 'map'
          ? { ...entry, roles: ['detail'], roleLimits: [{ role: 'detail', minimum: 2, maximum: 2 }] }
          : entry.type === 'bar'
            ? { ...entry, roles: ['dimension', 'metric'], roleLimits: [{ role: 'dimension', minimum: 0, maximum: 2 }, { role: 'metric', minimum: 1, maximum: 0 }] }
            : { ...entry, roleLimits: entry.roleLimits ?? [] }),
        pages: [{ ...element.builder.pages[0], visuals: [{ ...source, type: 'map', slots: [] }] }, element.builder.pages[1]],
        selectedPageId: 'overview', selectedVisualId: 'sales-chart',
        preview: { ...element.builder.preview, active: false, error: 'visual "sales-chart": map visual requires exactly two dimensions' },
      }, builderVisuals: {
        'sales-chart': {
          schemaVersion: 10, visualID: 'sales-chart', rendererID: 'echarts', specRevision: revision, dataRevision: 1,
          spec: { kind: 'cartesian', title: 'Stale table preview', accessibility: { title: 'Stale table preview', description: 'Preview from the prior visual type.' }, fields: [], x: { dataset: 'primary', field: 'category' }, y: [{ dataset: 'primary', field: 'value' }] },
          dataState: { schemaVersion: 1, encoding: 'json', kind: 'inline', specRevision: revision, dataRevision: 1, generation: 1, payload: JSON.stringify(dataState) },
          selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [], servingStateID: 'serving-test', streamGeneration: 1, filterRevision: 0, interactionRevision: 0, consumerIdentity: 'visual:sales-chart',
        },
      } })
      await element.updateComplete
      const root = element.shadowRoot
      const switched = {
        requirement: root.querySelector('.visual-requirements')?.textContent?.replace(/\s+/g, ' ').trim(),
        retained: root.querySelectorAll('.retained-field, .retained-fields').length,
        canvas: root.querySelector('.visual-preview-empty')?.textContent?.trim(),
        banner: root.querySelector('.preview-error')?.textContent?.trim(),
        previewHosts: root.querySelectorAll('.visual-preview lv-visualization-host').length,
        headerHelper: root.querySelector('.visual-builder .pane-header-details')?.textContent?.replace(/\s+/g, ' ').trim(),
        pickerReference: root.querySelector('.visual-reference-link')?.textContent?.trim(),
        interactionDisclosure: {
          tag: root.querySelector('.interaction-editor')?.tagName,
          open: (root.querySelector('.interaction-editor') as HTMLDetailsElement | null)?.open,
        },
        disabledTypes: root.querySelectorAll('.visual-picker-button:disabled').length,
      }
      mergePatch({ builder: {
        pages: [{ ...element.builder.pages[0], visuals: [{ ...source, type: 'bar', slots: sourceSlots }] }, element.builder.pages[1]],
        preview: { ...element.builder.preview, active: true, error: '' },
      } })
      await element.updateComplete
      return {
        switched,
        restored: {
          requirement: root.querySelector('.visual-requirements')?.textContent?.replace(/\s+/g, ' ').trim(),
          wells: Array.from(root.querySelectorAll('.field-token-label')).map((node: any) => node.textContent?.trim()),
          retained: root.querySelectorAll('.retained-field').length,
        },
      }
    })
    expect(state.switched.requirement).toBe('Needs 2 coordinate columns.')
    expect(state.switched.retained).toBe(0)
    expect(state.switched.canvas).toContain('Add 2 coordinate columns')
    expect(state.switched.banner).toBeUndefined()
    expect(state.switched.previewHosts).toBe(0)
    expect(state.switched.headerHelper).toBeUndefined()
    expect(state.switched.pickerReference).toBe('Reference')
    expect(state.switched.interactionDisclosure).toEqual({ tag: 'DETAILS', open: false })
    expect(state.switched.disabledTypes).toBe(0)
    expect(state.restored.requirement).toBeUndefined()
    expect(state.restored.wells).toEqual(['Status', 'Total'])
    expect(state.restored.retained).toBe(0)
  } finally {
    await page.close()
  }
})

test('dashboard builder deselects on the empty canvas and adds a visual directly from the picker', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const selectedBefore = root.querySelector('.visual[data-selected="true"]')?.getAttribute('gs-id')
      const addButtonBefore = root.querySelector('button[data-builder-action="add-visual"]')
      ;(root.querySelector('.canvas') as HTMLElement).click()
      await element.updateComplete
      const selectedAfter = root.querySelector('.visual[data-selected="true"]')?.getAttribute('gs-id') ?? ''
      const headingAfter = root.querySelector('.visual-builder .pane-title')?.textContent?.trim()
      const fieldWellsAfter = root.querySelectorAll('.field-wells').length
      const helperAfter = root.querySelector('.visual-builder .pane-header-details')?.textContent?.replace(/\s+/g, ' ').trim()
      const placeholderAfter = root.querySelectorAll('.visual-builder .format-placeholder').length
      const addButtonAfter = root.querySelector('button[data-builder-action="add-visual"]')
      const column = root.querySelector('button[data-visual-type="column"]') as HTMLButtonElement | null
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      column?.click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return {
        selectedBefore,
        selectedAfter,
        headingAfter,
        fieldWellsAfter,
        helperAfter,
        placeholderAfter,
        addButtonBefore: Boolean(addButtonBefore),
        addButtonAfter: Boolean(addButtonAfter),
        columnLabel: column?.getAttribute('aria-label'),
        command,
      }
    })
    expect(state.selectedBefore).toBe('sales-chart')
    expect(state.selectedAfter).toBe('')
    expect(state.headingAfter).toBe('Add a visual')
    expect(state.fieldWellsAfter).toBe(0)
    expect(state.helperAfter).toBeUndefined()
    expect(state.placeholderAfter).toBe(0)
    expect(state.addButtonBefore).toBe(false)
    expect(state.addButtonAfter).toBe(false)
    expect(state.columnLabel).toBe('Add Column chart visual')
    expect(state.command).toMatchObject({ action: 'add_visual', pageId: 'overview', visualId: '', type: 'column' })
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps visual actions out of the header and supports copy, paste, and immediate delete shortcuts', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'c', ctrlKey: true, bubbles: true, cancelable: true }))
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'v', ctrlKey: true, bubbles: true, cancelable: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Delete', bubbles: true, cancelable: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      return {
        actionCount: root.querySelectorAll('[data-visual-action]').length,
        heading: root.querySelector('.visual-builder .pane-title')?.textContent?.trim(),
        commands,
      }
    })
    expect(state.actionCount).toBe(0)
    expect(state.heading).toBe('Sales by status')
    expect(state.commands).toHaveLength(2)
    expect(state.commands[0]).toMatchObject({ action: 'duplicate_visual', pageId: 'overview', visualId: 'sales-chart' })
    expect(state.commands[1]).toMatchObject({ action: 'remove_visual', pageId: 'overview', visualId: 'sales-chart' })
  } finally {
    await page.close()
  }
})

test('dashboard builder restores exact revisions through toolbar undo and redo', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      const undo = root.querySelector<HTMLButtonElement>('[data-builder-action="undo"]')!
      const redo = root.querySelector<HTMLButtonElement>('[data-builder-action="redo"]')!
      const initiallyDisabled = { undo: undo.disabled, redo: redo.disabled }

      root.querySelector<HTMLButtonElement>('.field')?.click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { revision: { id: 'rev-8', number: 8, contentHash: 'sha256:def' } } })
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      const undoEnabledAfterEdit = !undo.disabled
      undo.click()
      await new Promise((resolve) => setTimeout(resolve, 20))

      mergePatch({ builder: { revision: { id: 'rev-9', number: 9, contentHash: 'sha256:ghi' } } })
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      const redoEnabledAfterUndo = !redo.disabled
      redo.click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { initiallyDisabled, undoEnabledAfterEdit, redoEnabledAfterUndo, commands }
    })
    expect(state.initiallyDisabled).toEqual({ undo: true, redo: true })
    expect(state.undoEnabledAfterEdit).toBe(true)
    expect(state.redoEnabledAfterUndo).toBe(true)
    expect(state.commands[1]).toMatchObject({
      action: 'restore_revision',
      revisionId: 'rev-8',
      targetRevisionId: 'rev-7',
      targetRevisionNumber: '7',
      targetRevisionContentHash: 'sha256:abc',
    })
    expect(state.commands[2]).toMatchObject({
      action: 'restore_revision',
      revisionId: 'rev-9',
      targetRevisionId: 'rev-8',
      targetRevisionNumber: '8',
      targetRevisionContentHash: 'sha256:def',
    })
  } finally {
    await page.close()
  }
})

test('dashboard builder exposes field-token remove, role movement, and reorder affordances', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const tokens = Array.from(root.querySelectorAll<HTMLElement>('.field-token')).map((token) => ({
        text: token.textContent?.trim(),
        actions: Array.from(token.querySelectorAll<HTMLElement>('[data-field-action]')).map((action) => ({
          action: action.getAttribute('data-field-action'),
          label: action.getAttribute('aria-label'),
          disabled: (action as HTMLButtonElement).disabled,
        })),
      }))
      const moveRole = root.querySelector<HTMLElement>('.field-token [data-field-action="move-role"]')
      return { tokens, moveRoleLabel: moveRole?.getAttribute('aria-label') }
    })
    expect(state.tokens).toHaveLength(1)
    expect(state.tokens[0].actions.map((item) => item.action)).toEqual(['remove', 'move-up', 'move-down', 'move-role'])
    expect(state.tokens[0].actions.map((item) => item.disabled)).toEqual([false, true, true, true])
    expect(state.tokens[0].actions.map((item) => item.label)).toEqual([
      'Remove Status field',
      'Move Status field up',
      'Move Status field down',
      'Move Status field to another role',
    ])
    expect(state.moveRoleLabel).toBe('Move Status field to another role')
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps format controls visible and persistent across inspector tabs', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const formatTab = root.querySelector<HTMLButtonElement>('[data-inspector-tab="format"]')
      formatTab?.click()
      await element.updateComplete
      const controlSelectors = [
        'input[data-format-control="title-text"]',
        'input[data-format-control="title-visible"]',
        'input[data-format-control="axisVisible"]',
        'select[data-format-control="legend"]',
        'select[data-format-control="labels.density"]',
        'select[data-format-control="stacking"]',
      ]
      const controls = controlSelectors.map((selector) => {
        const control = root.querySelector<HTMLInputElement | HTMLSelectElement>(selector)
        return { selector, present: Boolean(control), label: control?.getAttribute('aria-label'), disabled: control?.disabled ?? null, value: control?.value ?? null, checked: control instanceof HTMLInputElement ? control.checked : null }
      })
      const legend = root.querySelector<HTMLSelectElement>('select[data-format-control="legend"]')
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      if (legend) {
        legend.value = 'bottom'
        legend.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      }
      await new Promise((resolve) => setTimeout(resolve, 20))
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const pages = element.builder.pages.map((page: any) => ({
        ...page,
        visuals: page.visuals.map((visual: any) => visual.id === 'sales-chart' ? {
          ...visual,
          title: 'Bookings',
          titleVisible: true,
          legendVisible: false,
          axisVisible: true,
          dataLabelsVisible: false,
          formatOptions: visual.formatOptions.map((option: any) => option.key === 'legend' ? { ...option, value: 'bottom' } : option),
        } : visual),
      }))
      mergePatch({ builder: { pages, revision: { id: 'rev-8', number: 8, contentHash: 'sha256:def' } } })
      await element.updateComplete
      root.querySelector<HTMLButtonElement>('[data-inspector-tab="build"]')?.click()
      await element.updateComplete
      root.querySelector<HTMLButtonElement>('[data-inspector-tab="format"]')?.click()
      await element.updateComplete
      return {
        controls,
        persisted: {
          title: root.querySelector<HTMLInputElement>('input[data-format-control="title-text"]')?.value,
          titleVisible: root.querySelector<HTMLInputElement>('input[data-format-control="title-visible"]')?.checked,
          legend: root.querySelector<HTMLSelectElement>('select[data-format-control="legend"]')?.value,
          axis: root.querySelector<HTMLInputElement>('input[data-format-control="axisVisible"]')?.checked,
          dataLabels: root.querySelector<HTMLSelectElement>('select[data-format-control="labels.density"]')?.value,
        },
        command,
      }
    })
    expect(state.controls.map((control) => control.present)).toEqual([true, true, true, true, true, true])
    expect(state.controls.map((control) => control.disabled)).toEqual([false, false, false, false, false, false])
    expect(state.controls.map((control) => control.label)).toEqual(['Title text', 'Show title', 'Show axes', 'Legend', 'Data labels', 'Stacking'])
    expect(state.controls.map((control) => control.value)).toEqual(['Sales by status', 'on', 'on', 'right', 'hidden', 'none'])
    expect(state.command).toMatchObject({ action: 'update_visual_format', pageId: 'overview', visualId: 'sales-chart', formatKey: 'legend', formatValue: 'bottom' })
    expect(state.persisted).toEqual({ title: 'Bookings', titleVisible: true, legend: 'bottom', axis: true, dataLabels: 'hidden' })
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps the independent Data pane usable across dock breakpoints', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const measure = async (width: number) => {
      await page.setViewportSize({ width, height: 820 })
      return page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
        await element.updateComplete
        const root = element.shadowRoot
        const box = (selector: string) => (root.querySelector(selector) as HTMLElement).getBoundingClientRect()
        const canvas = box('.canvas-pane')
        const pageBar = box('.page-bar')
        const visual = box('.visual-builder')
        const data = box('.data-pane')
        const search = box('.data-pane input[aria-label="Search fields"]')
        return {
          canvas: { right: canvas.right, bottom: canvas.bottom },
          pageBar: { top: pageBar.top, bottom: pageBar.bottom },
          visual: { left: visual.left, top: visual.top, right: visual.right, bottom: visual.bottom },
          data: { left: data.left, top: data.top, right: data.right, bottom: data.bottom },
          searchVisible: search.width > 0 && search.height > 0,
          semanticHelper: root.querySelector('.data-pane .pane-hint')?.textContent?.trim(),
          horizontalOverflow: document.documentElement.scrollWidth > innerWidth || document.body.scrollWidth > innerWidth,
        }
      })
    }

    const stackedRight = await measure(1100)
    expect(stackedRight.visual.left).toBeGreaterThanOrEqual(stackedRight.canvas.right - 1)
    expect(stackedRight.data.left).toBeCloseTo(stackedRight.visual.left, 0)
    expect(stackedRight.data.top).toBeGreaterThanOrEqual(stackedRight.visual.bottom - 1)
    expect(stackedRight.searchVisible).toBe(true)
    expect(stackedRight.semanticHelper).toBeUndefined()
    expect(stackedRight.horizontalOverflow).toBe(false)

    const belowCanvas = await measure(900)
    expect(belowCanvas.visual.top).toBeGreaterThanOrEqual(belowCanvas.pageBar.bottom - 1)
    expect(belowCanvas.data.top).toBeCloseTo(belowCanvas.visual.top, 0)
    expect(belowCanvas.data.left).toBeGreaterThanOrEqual(belowCanvas.visual.right - 1)
    expect(belowCanvas.searchVisible).toBe(true)
    expect(belowCanvas.horizontalOverflow).toBe(false)
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps bottom-tab navigation and add-page actions wired', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const pageBar = root.querySelector('.page-bar') as HTMLElement
      let pageSelect: Record<string, unknown> | undefined
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-page-select', (event: CustomEvent) => { pageSelect = event.detail }, { once: true })
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })

      const detailsTab = pageBar.querySelector('.page-tab[aria-selected="false"]') as HTMLButtonElement
      detailsTab.click()
      await element.updateComplete
      const selectedAfterNavigation = root.querySelector('.page-tab[aria-selected="true"]')?.textContent?.trim()
      const pageNavigationLabel = pageBar.querySelector('.page-tabs')?.getAttribute('aria-label')

      ;(root.querySelector('.page-bar button[aria-label="Add page"]') as HTMLButtonElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { pageNavigationLabel, selectedAfterNavigation, pageSelect, command }
    })
    expect(state.pageNavigationLabel).toBe('Dashboard pages')
    expect(state.selectedAfterNavigation).toBe('Details')
    expect(state.pageSelect).toMatchObject({ pageId: 'details' })
    expect(state.command).toMatchObject({ action: 'add_page', pageId: '' })
  } finally {
    await page.close()
  }
})

test('dashboard builder edits page name and grid through the page Format contract', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      ;(root.querySelector('.page-tab[data-page-id="details"]') as HTMLButtonElement).click()
      ;(root.querySelector('[data-inspector-tab="format"]') as HTMLButtonElement).click()
      await element.updateComplete
      const before = {
        heading: root.querySelector('.visual-builder .pane-title')?.textContent?.trim(),
        panel: root.querySelector('[role="tabpanel"]')?.getAttribute('aria-label'),
        controls: Array.from(root.querySelectorAll<HTMLInputElement>('[data-page-control]')).map((control) => ({ key: control.dataset.pageControl, value: control.value, min: control.min })),
      }
      const title = root.querySelector<HTMLInputElement>('[data-page-control="title"]')!
      title.value = 'Order details'
      title.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      const columns = root.querySelector<HTMLInputElement>('[data-page-control="columns"]')!
      columns.value = '14'
      columns.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { before, commands }
    })
    expect(state.before.heading).toBe('Details')
    expect(state.before.panel).toBe('Format page')
    expect(state.before.controls).toEqual([
      { key: 'title', value: 'Details', min: '' },
      { key: 'columns', value: '12', min: '1' },
      { key: 'rowHeight', value: '48', min: '1' },
      { key: 'gap', value: '16', min: '0' },
      { key: 'padding', value: '16', min: '0' },
    ])
    expect(state.commands[0]).toMatchObject({ action: 'rename_page', pageId: 'details', title: 'Order details' })
    expect(state.commands[1]).toMatchObject({ action: 'update_page_layout', pageId: 'details', columns: 14, rowHeight: 48, gap: 16, padding: 16 })
  } finally {
    await page.close()
  }
})

test('dashboard builder reorders, duplicates, and immediately deletes pages through bounded commands', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      const overview = root.querySelector('.page-tab[data-page-id="overview"]') as HTMLElement
      const details = root.querySelector('.page-tab[data-page-id="details"]') as HTMLElement
      const dataTransfer = new DataTransfer()
      overview.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, dataTransfer }))
      const target = details.getBoundingClientRect()
      details.dispatchEvent(new DragEvent('dragover', { bubbles: true, cancelable: true, dataTransfer, clientX: target.right - 1 }))
      details.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer, clientX: target.right - 1 }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      const menu = root.querySelector('.page-actions') as HTMLDetailsElement
      menu.open = true
      ;(menu.querySelector('button:nth-of-type(3)') as HTMLButtonElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      const nextMenu = root.querySelector('.page-actions') as HTMLDetailsElement
      nextMenu.open = true
      const deleteButton = nextMenu.querySelector('.page-delete') as HTMLButtonElement
      const deleteDisabled = deleteButton.disabled
      deleteButton.click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      const selectedPageBeforeFailure = root.querySelector('.page-tab[aria-selected="true"]')?.getAttribute('data-page-id')
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      await new Promise<void>((resolve) => queueMicrotask(resolve))
      await element.updateComplete
      return {
        commands,
        deleteDisabled,
        actionLabels: Array.from(nextMenu.querySelectorAll('button')).map((button) => button.textContent?.replace(/\s+/g, ' ').trim()),
        selectedPageBeforeFailure,
        selectedPage: root.querySelector('.page-tab[aria-selected="true"]')?.getAttribute('data-page-id'),
      }
    })
    expect(state.deleteDisabled).toBe(false)
    expect(state.actionLabels).toEqual(['Move earlier', 'Move later', 'Duplicate page', 'Delete page'])
    expect(state.commands[0]).toMatchObject({ action: 'move_page', pageId: 'overview', index: 1 })
    expect(state.commands[1]).toMatchObject({ action: 'duplicate_page', pageId: 'overview', newPageId: '', title: 'Overview copy' })
    expect(state.commands[2]).toMatchObject({ action: 'remove_page', pageId: 'overview' })
    expect(state.selectedPageBeforeFailure).toBe('details')
    expect(state.selectedPage).toBe('overview')
  } finally {
    await page.close()
  }
})

test('dashboard builder initializes GridStack tiles with stable ids and dedicated controls', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const canvas = root.querySelector('.canvas') as any
      const visual = root.querySelector('.visual') as HTMLElement & { gridstackNode?: { id?: string } }
      return {
        hasGridStack: Boolean(canvas?.gridstack),
        nodeID: visual.gridstackNode?.id,
        visualID: visual.getAttribute('gs-id'),
        contentWrapper: Boolean(visual.querySelector('.grid-stack-item-content')),
        dragHeader: visual.querySelector('.visual-drag-header')?.getAttribute('title'),
        cornerHandle: Boolean(visual.querySelector('.visual-drag-handle')),
        resizeHandle: Boolean(visual.querySelector('.ui-resizable-se')),
        instructions: visual.getAttribute('aria-describedby'),
      }
    })
    expect(state).toEqual({
      hasGridStack: true,
      nodeID: 'sales-chart',
      visualID: 'sales-chart',
      contentWrapper: true,
      dragHeader: 'Drag to move Sales by status',
      cornerHandle: false,
      resizeHandle: true,
      instructions: 'dashboard-builder-grid-help',
    })
  } finally {
    await page.close()
  }
})

test('dashboard builder emits one canonical atomic placement command after a GridStack change', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const command = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const canvas = root.querySelector('.canvas') as any
      const visual = root.querySelector('.visual') as any
      let detail: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { detail = event.detail }, { once: true })
      canvas.gridstack.update(visual, { x: 2, y: 3, w: 5, h: 6 })
      await new Promise((resolve) => setTimeout(resolve, 20))
      return detail
    })
    expect(command).toMatchObject({
      action: 'set_placements',
      pageId: 'overview',
      placements: [{ componentId: 'sales-chart', placement: { column: 3, row: 1, columnSpan: 5, rowSpan: 6 } }],
    })
  } finally {
    await page.close()
  }
})

test('dashboard builder supports keyboard move and resize through the same atomic placement path', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const commands = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const visual = root.querySelector('.visual') as HTMLElement
      const received: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { received.push(event.detail) })
      visual.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', altKey: true, bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      visual.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', altKey: true, shiftKey: true, bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      return received.map((item) => ({
        action: item.action,
        placement: (item.placements as any[])?.[0]?.placement,
      }))
    })
    expect(commands).toEqual([
      { action: 'set_placements', placement: { column: 2, row: 1, columnSpan: 6, rowSpan: 5 } },
      { action: 'set_placements', placement: { column: 2, row: 1, columnSpan: 7, rowSpan: 5 } },
    ])
  } finally {
    await page.close()
  }
})

test('dashboard builder does not persist breakpoint-derived mobile stacking', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const canvas = root.querySelector('.canvas') as any
      const visual = root.querySelector('.visual') as HTMLElement
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      visual.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', altKey: true, bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { hasGridStack: Boolean(canvas.gridstack), command }
    })
    expect(state).toEqual({ hasGridStack: false, command: undefined })
  } finally {
    await page.close()
  }
})

test('dashboard builder disables GridStack editing in read-only state and reinitializes on revision cutover', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const canvas = root.querySelector('.canvas') as any
      const firstGrid = canvas.gridstack
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { capabilities: { canEdit: false }, revision: { id: 'rev-8', number: 8, contentHash: 'sha256:def' } } })
      await element.updateComplete
      const visual = root.querySelector('.visual') as HTMLElement
      const secondGrid = canvas.gridstack
      return {
        disabled: visual.classList.contains('ui-draggable-disabled') && visual.classList.contains('ui-resizable-disabled'),
        reinitialized: Boolean(secondGrid) && firstGrid !== secondGrid,
        firstDestroyed: !firstGrid.el,
      }
    })
    expect(state).toEqual({ disabled: true, reinitialized: true, firstDestroyed: true })
  } finally {
    await page.close()
  }
})

test('dashboard builder switches Build and Format inspector tabs', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const format = root.querySelector('[data-inspector-tab="format"]') as HTMLButtonElement
      const build = root.querySelector('[data-inspector-tab="build"]') as HTMLButtonElement
      const initial = {
        selected: root.querySelector('.inspector-tab[aria-selected="true"]')?.getAttribute('data-inspector-tab'),
        panel: root.querySelector('[role="tabpanel"]')?.getAttribute('aria-label'),
        dataPaneVisible: Boolean(root.querySelector('.data-pane')),
        dataSearchCount: root.querySelectorAll('.data-pane input[aria-label="Search fields"]').length,
      }
      format.click()
      await element.updateComplete
      const formatted = {
        selected: root.querySelector('.inspector-tab[aria-selected="true"]')?.getAttribute('data-inspector-tab'),
        panel: root.querySelector('[role="tabpanel"]')?.getAttribute('aria-label'),
        formatControls: root.querySelectorAll('[data-format-control]').length,
        dataPaneVisible: Boolean(root.querySelector('.data-pane')),
        dataSearchCount: root.querySelectorAll('.data-pane input[aria-label="Search fields"]').length,
      }
      build.click()
      await element.updateComplete
      const rebuilt = {
        selected: root.querySelector('.inspector-tab[aria-selected="true"]')?.getAttribute('data-inspector-tab'),
        panel: root.querySelector('[role="tabpanel"]')?.getAttribute('aria-label'),
        hasFieldBrowser: Boolean(root.querySelector('.field-browser')),
        dataPaneVisible: Boolean(root.querySelector('.data-pane')),
        dataSearchCount: root.querySelectorAll('.data-pane input[aria-label="Search fields"]').length,
      }
      return { initial, formatted, rebuilt }
    })
    expect(state.initial).toEqual({ selected: 'build', panel: 'Build visual', dataPaneVisible: true, dataSearchCount: 1 })
    expect(state.formatted.selected).toBe('format')
    expect(state.formatted.panel).toBe('Format visual')
    expect(state.formatted.formatControls).toBe(6)
    expect(state.formatted.dataPaneVisible).toBe(true)
    expect(state.formatted.dataSearchCount).toBe(1)
    expect(state.rebuilt).toEqual({ selected: 'build', panel: 'Build visual', hasFieldBrowser: true, dataPaneVisible: true, dataSearchCount: 1 })
  } finally {
    await page.close()
  }
})

test('dashboard builder filters fields and drops a metric into its well', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const search = root.querySelector('.data-pane input[aria-label="Search fields"]') as HTMLInputElement
      search.value = 'Total'
      search.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const filteredLabels = Array.from(root.querySelectorAll('.data-pane .field .field-label')).map((node) => node.textContent?.trim())
      const dataTransfer = new DataTransfer()
      dataTransfer.setData('text/leapview-field', 'orders.total')
      const well = root.querySelector('.field-well-target[data-drop-well="metric"]') as HTMLElement
      let command: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      well.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      search.value = ''
      search.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const allLabels = Array.from(root.querySelectorAll('.data-pane .field .field-label')).map((node) => node.textContent?.trim())
      return {
        dataPane: Boolean(root.querySelector('.data-pane')),
        filteredLabels,
        allLabels,
        wellLabel: well.getAttribute('aria-label'),
        command,
      }
    })
    expect(state.dataPane).toBe(true)
    expect(state.filteredLabels).toEqual(['Total'])
    expect(state.allLabels).toEqual(['Total', 'Status'])
    expect(state.wellLabel).toBe('Drop metric field in X-axis')
    expect(state.command).toMatchObject({ action: 'assign_field', pageId: 'overview', visualId: 'sales-chart', fieldId: 'orders.total', role: 'metric' })
  } finally {
    await page.close()
  }
})

test('dashboard builder blocks record-only and full-role field assignments before command dispatch', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        builder: {
          semanticModel: {
            id: 'commerce', title: 'Orders', datasets: [{ id: 'orders', title: 'Orders', fields: [
              { id: 'purchase_date', label: 'Purchase date', kind: 'dimension', roles: ['dimension'], dataType: 'Date' },
              { id: 'category', label: 'Category', kind: 'dimension', roles: ['dimension'], dataType: 'String' },
              { id: 'orders.purchase_month', label: 'Purchase month', kind: 'dimension', roles: ['detail'], dataType: 'String' },
              { id: 'order_count', label: 'Orders', kind: 'metric', roles: ['metric'], dataType: 'Number' },
            ] }],
          },
          visualCatalog: element.builder.visualCatalog.map((entry: any) => entry.type === 'donut'
            ? { ...entry, roleLimits: [{ role: 'dimension', minimum: 1, maximum: 1 }, { role: 'metric', minimum: 1, maximum: 1 }] }
            : { ...entry, roleLimits: entry.roleLimits ?? [] }),
          pages: element.builder.pages.map((pageSignal: any) => pageSignal.id !== 'overview' ? pageSignal : {
            ...pageSignal,
            visuals: [{
              ...pageSignal.visuals[0], type: 'donut',
              slots: [
                { id: 'category', label: 'Purchase date', kind: 'dimension', fieldId: 'purchase_date', required: true },
                { id: 'value', label: 'Orders', kind: 'metric', fieldId: 'order_count', required: true },
              ],
            }],
          }),
          selectedPageId: 'overview', selectedVisualId: 'sales-chart',
        },
      })
      await element.updateComplete
      const root = element.shadowRoot
      const rows = Array.from(root.querySelectorAll<HTMLElement>('.data-pane .field'))
      const row = (label: string) => rows.find((candidate) => candidate.querySelector('.field-label')?.textContent?.trim() === label)!
      const commands: unknown[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => commands.push(event.detail))
      row('Category').click()
      row('Purchase month').click()
      await element.updateComplete
      return {
        commands,
        used: ['Purchase date', 'Orders'].map((label) => ({ label, tag: row(label).tagName, used: row(label).getAttribute('data-used') })),
        blocked: ['Category', 'Purchase month'].map((label) => ({
          label,
          tag: row(label).tagName,
          role: row(label).getAttribute('role'),
          name: row(label).getAttribute('aria-label'),
          context: row(label).querySelector('.field-context')?.textContent?.trim(),
        })),
      }
    })
    expect(state.commands).toEqual([])
    expect(state.used).toEqual([
      { label: 'Purchase date', tag: 'BUTTON', used: 'true' },
      { label: 'Orders', tag: 'BUTTON', used: 'true' },
    ])
    expect(state.blocked[0]).toMatchObject({ label: 'Category', tag: 'DIV', role: 'note' })
    expect(state.blocked[0].name).toContain('Category is full')
    expect(state.blocked[0].context).toContain('Category is full')
    expect(state.blocked[1]).toMatchObject({ label: 'Purchase month', tag: 'DIV', role: 'note' })
    expect(state.blocked[1].name).toContain('Not compatible with the selected donut visual')
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps table columns within the bound records dataset', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        builder: {
          semanticModel: {
            id: 'commerce', title: 'Sales', datasets: [
              { id: 'sales_orders', title: 'Sales orders', fields: [
                { id: 'revenue', datasetId: 'sales_orders', label: 'Revenue', kind: 'metric', roles: ['metric'], dataType: 'number' },
                { id: 'sales_orders.revenue', datasetId: 'sales_orders', label: 'Revenue', kind: 'dimension', roles: ['detail'], dataType: 'number' },
                { id: 'sales_orders.customer_id', datasetId: 'sales_orders', label: 'Customer ID', kind: 'dimension', roles: ['detail'], dataType: 'string' },
              ] },
              { id: 'sales_customers', title: 'Sales customers', fields: [
                { id: 'sales_customers.customer_id', datasetId: 'sales_customers', label: 'Customer ID', kind: 'dimension', roles: ['detail'], dataType: 'string' },
              ] },
            ],
          },
          pages: element.builder.pages.map((pageSignal: any) => pageSignal.id !== 'overview' ? pageSignal : {
            ...pageSignal,
            visuals: [{
              ...pageSignal.visuals[0], type: 'table', datasetId: 'sales_orders',
              slots: [{ id: 'field-0', label: 'Revenue', kind: 'detail', fieldId: 'revenue', required: false }],
            }],
          }),
          selectedPageId: 'overview', selectedVisualId: 'sales-chart',
        },
      })
      await element.updateComplete
      const root = element.shadowRoot
      const rows = Array.from(root.querySelectorAll<HTMLElement>('.data-pane .field'))
      const row = (label: string, context: string, group?: string) => rows.find((candidate) => candidate.querySelector('.field-label')?.textContent?.trim() === label && (candidate.querySelector('.field-context')?.textContent ?? '').trim().startsWith(context) && (!group || candidate.closest('.field-section')?.getAttribute('data-field-group') === group))!
      const metricRevenue = rows.find((candidate) => candidate.getAttribute('aria-label')?.startsWith('Revenue. Measure.'))!
      const physicalRevenue = rows.find((candidate) => candidate.getAttribute('aria-label')?.startsWith('Revenue. Dimension.') && (candidate.querySelector('.field-context')?.textContent ?? '').trim().startsWith('Sales orders'))!
      const ordersCustomer = row('Customer ID', 'Sales orders', 'dimension')
      const customersCustomer = row('Customer ID', 'Sales customers')
      const commands: unknown[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => commands.push(event.detail))
      customersCustomer.click()
      ordersCustomer.click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return {
        commands,
        metricRevenue: { tag: metricRevenue.tagName, used: metricRevenue.getAttribute('data-used') },
        physicalRevenue: { tag: physicalRevenue.tagName, used: physicalRevenue.getAttribute('data-used') },
        ordersCustomer: { tag: ordersCustomer.tagName, role: ordersCustomer.getAttribute('role') },
        customersCustomer: { tag: customersCustomer.tagName, role: customersCustomer.getAttribute('role'), aria: customersCustomer.getAttribute('aria-label') },
      }
    })
    expect(state.metricRevenue).toEqual({ tag: 'DIV', used: null })
    expect(state.physicalRevenue).toEqual({ tag: 'BUTTON', used: 'true' })
    expect(state.ordersCustomer).toEqual({ tag: 'BUTTON', role: null })
    expect(state.customersCustomer.tag).toBe('DIV')
    expect(state.customersCustomer.role).toBe('note')
    expect(state.customersCustomer.aria).toContain('Not compatible with the selected table visual')
    expect(state.commands).toHaveLength(1)
    expect(state.commands[0]).toMatchObject({ action: 'assign_field', pageId: 'overview', visualId: 'sales-chart', fieldId: 'sales_orders.customer_id', role: 'detail' })
  } finally {
    await page.close()
  }
})

test('dashboard builder creates smart governed visuals from fields on an empty canvas', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        builder: {
          pages: [{ id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [] }],
          selectedPageId: 'overview', selectedVisualId: '',
        },
      })
      await element.updateComplete
      const root = element.shadowRoot
      const fields = Array.from(root.querySelectorAll<HTMLButtonElement>('.data-pane .field'))
      const metric = fields.find((field) => field.querySelector('.field-label')?.textContent?.trim() === 'Total')!
      const dimension = fields.find((field) => field.querySelector('.field-label')?.textContent?.trim() === 'Status')!
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      const dataTransfer = new DataTransfer()
      metric.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, dataTransfer }))
      await element.updateComplete
      const canvas = root.querySelector<HTMLElement>('.canvas')!
      const dragState = {
        metricEnabled: !metric.disabled,
        metricDragging: metric.getAttribute('data-dragging'),
        canvasDragging: canvas.getAttribute('data-field-dragging'),
        hint: root.querySelector('.canvas-field-drop-hint')?.textContent?.trim(),
      }
      canvas.dispatchEvent(new DragEvent('drop', { bubbles: true, cancelable: true, dataTransfer }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      dimension.click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { dragState, canvasDraggingAfterDrop: canvas.getAttribute('data-field-dragging'), dimensionEnabled: !dimension.disabled, commands }
    })
    expect(state.dragState).toEqual({ metricEnabled: true, metricDragging: 'true', canvasDragging: 'true', hint: 'Drop on the canvas to create a KPI visual' })
    expect(state.canvasDraggingAfterDrop).toBe('false')
    expect(state.dimensionEnabled).toBe(true)
    expect(state.commands).toHaveLength(2)
    expect(state.commands[0]).toMatchObject({ action: 'add_visual', pageId: 'overview', type: 'kpi', title: 'Total', fieldId: 'orders.total', role: 'metric' })
    expect(state.commands[1]).toMatchObject({ action: 'add_visual', pageId: 'overview', type: 'table', title: 'Status', fieldId: 'orders.status', role: 'detail' })
  } finally {
    await page.close()
  }
})

test('dashboard builder highlights compatible visual and field-well drop targets', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const metric = Array.from(root.querySelectorAll<HTMLButtonElement>('.data-pane .field')).find((field) => field.querySelector('.field-label')?.textContent?.trim() === 'Total')!
      const dataTransfer = new DataTransfer()
      metric.dispatchEvent(new DragEvent('dragstart', { bubbles: true, cancelable: true, dataTransfer }))
      await element.updateComplete
      const during = {
        visual: root.querySelector('.visual')?.getAttribute('data-field-drop'),
        metricWell: root.querySelector('[data-drop-well="metric"]')?.getAttribute('data-field-drop'),
        dimensionWell: root.querySelector('[data-drop-well="dimension"]')?.getAttribute('data-field-drop'),
      }
      metric.dispatchEvent(new DragEvent('dragend', { bubbles: true, cancelable: true, dataTransfer }))
      await element.updateComplete
      return { during, canvasDraggingAfter: root.querySelector('.canvas')?.getAttribute('data-field-dragging') }
    })
    expect(state.during).toEqual({ visual: 'compatible', metricWell: 'compatible', dimensionWell: 'incompatible' })
    expect(state.canvasDraggingAfter).toBe('false')
  } finally {
    await page.close()
  }
})

test('dashboard builder presents a role-first field catalog with business context and used state', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        builder: {
          semanticModel: {
            id: 'commerce',
            title: 'Orders',
            datasets: [
              {
                id: 'orders',
                title: 'Orders',
                fields: [
                  { id: 'orders.status', label: 'Status', kind: 'dimension', dataType: 'String', description: 'Order status' },
                  { id: 'orders.total', label: 'Total', kind: 'metric', dataType: 'Decimal', description: 'Order total' },
                  { id: 'orders.ordered_at', label: 'Ordered at', kind: 'dimension', dataType: 'DateTime', description: 'Order timestamp' },
                  { id: 'orders.payload', label: 'Payload', kind: 'dimension', dataType: 'Opaque', description: 'Raw payload' },
                ],
              },
              {
                id: 'orders-copy',
                title: 'Mirror',
                fields: [
                  { id: 'orders.status', label: 'Status copy', kind: 'dimension', dataType: 'String' },
                  { id: 'orders.total', label: 'Total copy', kind: 'metric', dataType: 'Decimal' },
                ],
              },
            ],
          },
        },
      })
      await element.updateComplete
      const root = element.shadowRoot
      const sections = Array.from(root.querySelectorAll('.data-pane .field-section')).map((section) => ({
        group: section.getAttribute('data-field-group'),
        title: section.querySelector('.field-section-title')?.textContent?.trim(),
        fields: Array.from(section.querySelectorAll('.field')).map((field) => ({
          label: field.querySelector('.field-label')?.textContent?.trim(),
          context: field.querySelector('.field-context')?.textContent?.trim(),
          used: field.getAttribute('data-used'),
          usedMarker: Boolean(field.querySelector('.field-used')),
          scalarTypeLabels: field.querySelectorAll('.field-type').length,
        })),
      }))
      const filter = root.querySelector('.data-pane .field-filter') as HTMLElement
      const filters = Array.from(filter.querySelectorAll('button')).map((button) => ({
        role: button.getAttribute('data-field-filter'),
        pressed: button.getAttribute('aria-pressed'),
      }))
      const status = sections.flatMap((section) => section.fields).find((field) => field.label === 'Status')
      const typeLabelCount = sections.flatMap((section) => section.fields).reduce((count, field) => count + field.scalarTypeLabels, 0)

      const timeFilter = filter.querySelector('button[data-field-filter="time"]') as HTMLButtonElement
      timeFilter.click()
      await element.updateComplete
      const timeOnlySections = Array.from(root.querySelectorAll('.data-pane .field-section')).map((section) => section.getAttribute('data-field-group'))
      const timeOnlyFields = Array.from(root.querySelectorAll('.data-pane .field-section .field .field-label')).map((field) => field.textContent?.trim())

      // A table visual deliberately makes measures incompatible. The catalog
      // keeps those rows in a native, collapsed disclosure rather than
      // dropping them from the governed field list.
      const allFilter = filter.querySelector('button[data-field-filter="all"]') as HTMLButtonElement
      allFilter.click()
      await element.updateComplete
      const tablePages = element.builder.pages.map((item: any) => ({
        ...item,
        visuals: item.visuals.map((itemVisual: any) => ({ ...itemVisual, type: 'table' })),
      }))
      mergePatch({ builder: { pages: tablePages } })
      await element.updateComplete
      const unsupported = root.querySelector('.data-pane details.unsupported-fields') as HTMLDetailsElement | null
      const unsupportedSummary = unsupported?.querySelector('summary')?.textContent?.trim()
      const unsupportedFieldLabels = Array.from(root.querySelectorAll('.data-pane .unsupported-fields .field-label')).map((field) => field.textContent?.trim())

      return { sections, filters, unsupportedOpen: unsupported?.hasAttribute('open') ?? null, unsupportedSummary, unsupportedFieldLabels, status, typeLabelCount, timeOnlySections, timeOnlyFields }
    })
    expect(state.filters).toEqual([
      { role: 'all', pressed: 'true' },
      { role: 'metric', pressed: 'false' },
      { role: 'dimension', pressed: 'false' },
      { role: 'time', pressed: 'false' },
    ])
    expect(state.sections.map((section) => ({ group: section.group, title: section.title }))).toEqual([
      { group: 'metric', title: 'Measures' },
      { group: 'dimension', title: 'Dimensions' },
      { group: 'time', title: 'Time' },
    ])
    expect(state.sections.flatMap((section) => section.fields.map((field) => field.context))).toContain('Orders')
    expect(state.sections.flatMap((section) => section.fields).filter((field) => field.label === 'Status')).toHaveLength(1)
    expect(state.status).toEqual({ label: 'Status', context: 'Orders', used: 'true', usedMarker: true, scalarTypeLabels: 0 })
    expect(state.typeLabelCount).toBe(0)
    expect(state.unsupportedOpen).toBe(false)
    expect(state.unsupportedSummary).toMatch(/not supported/i)
    expect(state.unsupportedSummary).toMatch(/2/)
    expect(state.unsupportedFieldLabels).toEqual(['Total', 'Payload'])
    expect(state.timeOnlySections).toEqual(['time'])
    expect(state.timeOnlyFields).toEqual(['Ordered at'])
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
      let visibilityCommand: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { visibilityCommand = event.detail }, { once: true })
      const more = root.querySelector('.more-actions') as HTMLDetailsElement
      more.open = true
      ;(root.querySelector('.more-menu button[aria-label="Toggle dashboard visibility"]') as HTMLButtonElement).click()
      return {
        badgeCount: root.querySelectorAll('.meta .badge').length,
        metadataLines: root.querySelectorAll('.meta > span').length,
        topLevelActions: Array.from(toolbar.children).map((child) => child.localName === 'details' ? 'more' : child.textContent?.trim()),
        moreLabel: root.querySelector('.more-actions summary')?.textContent?.trim(),
        moreAriaLabel: root.querySelector('.more-actions summary')?.getAttribute('aria-label'),
        visibilityCommand,
      }
    })
    expect(state.badgeCount).toBe(0)
    expect(state.metadataLines).toBe(1)
    expect(state.topLevelActions).toEqual(['Undo', 'Redo', 'Preview', 'more', 'Publish'])
    expect(state.moreLabel).toBe('More')
    expect(state.moreAriaLabel).toBe('More dashboard actions')
    expect(state.visibilityCommand).toMatchObject({ action: 'set_visibility', visibility: 'organization' })
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps governed previews interactive beneath a dedicated authoring header', async () => {
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
            spec: { kind: 'cartesian', title: 'Sales by status', accessibility: { title: 'Sales by status', description: 'Sales grouped by status.' }, fields: [], x: { dataset: 'primary', field: 'category' }, y: [{ dataset: 'primary', field: 'value' }] },
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
        hostAuthoring: host?.authoring,
        hostPointerEvents: host ? getComputedStyle(host).pointerEvents : '',
        wrapperInert: previewWrapper?.hasAttribute('inert'),
        wrapperAriaHidden: previewWrapper?.getAttribute('aria-hidden'),
        dragHeaderSlot: host?.querySelector('[slot="authoring-drag-handle"]')?.getAttribute('title'),
        wrapperHeight: wrapperBox?.height ?? 0,
        hostHeight: hostBox?.height ?? 0,
        emptyPreviewCount: root.querySelectorAll('.visual-preview-empty').length,
        previewTitleCount: root.querySelectorAll('.visual-preview ~ .visual-title').length,
        previewTypeCount: root.querySelectorAll('.visual-preview ~ .visual-type').length,
      }
    })
    expect(state.hostCount).toBe(1)
    expect(state.visualTag).toBe('div')
    expect(state.visualRole).toBe('group')
    expect(state.hostVisualID).toBe('sales-chart')
    expect(state.hostAuthoring).toBe(true)
    expect(state.hostPointerEvents).toBe('auto')
    expect(state.wrapperInert).toBe(false)
    expect(state.wrapperAriaHidden).toBe(null)
    expect(state.dragHeaderSlot).toBe('Drag to move Sales by status')
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
        addPageHasIcon: Boolean(root.querySelector('button[aria-label="Add page"] svg[data-lucide="icon"]')),
      }
    })
    expect(state.canvasScrollCount).toBe(1)
    expect(state.scrollPadding).toBe('0px')
    expect(state.canvasBorder).toMatch(/^0px none /)
    expect(state.canvasRadius).toBe('0px')
    expect(state.canvasShadow).toBe('none')
    expect(state.emptyPreview).toContain('Add fields')
    expect(state.addPageHasIcon).toBe(true)
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
      const nestedMainCount = root.querySelectorAll('main').length
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: undefined, status: { loading: true, error: '', generation: 0, lastUpdated: '', refreshId: '', setupRequired: false, progressPercent: 0 } })
      await element.updateComplete
      const loadingState = root.querySelector('.state') as HTMLElement | null
      return {
        display: responsiveDisplay,
        hasSearchLabel,
        buttonLabels,
        loading: loadingState?.textContent?.trim(),
        nestedMainCount,
      }
    })
    expect(state.display).toBe('block')
    expect(state.hasSearchLabel).toBe(true)
    expect(state.buttonLabels).toContain('Publish')
    expect(state.loading).toContain('Loading dashboard builder')
    expect(state.nestedMainCount).toBe(0)
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
        const contentBox = (node.querySelector('.grid-stack-item-content') as HTMLElement).getBoundingClientRect()
        const tile = node as HTMLElement
        const style = getComputedStyle(tile)
        return { title: tile.querySelector('.visual-drag-header')?.textContent?.trim(), left: box.left, right: box.right, top: box.top, bottom: box.bottom, width: box.width, height: box.height, contentWidth: contentBox.width, contentHeight: contentBox.height, type: tile.getAttribute('data-visual-type'), position: style.position, order: style.order, topOffset: style.top, leftOffset: style.left, authoredTop: tile.style.top }
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
      expect(visual.contentWidth).toBeCloseTo(visual.width, 0)
      expect(visual.contentHeight).toBeCloseTo(visual.height, 0)
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
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      element.setAttribute('page-base-href', '/dashboards/revenue/builder?draft=draft-7')
      await element.updateComplete
      const root = element.shadowRoot
      return {
        href: root.querySelector('.page-tab[href*="page=details"]')?.getAttribute('href'),
        currentPage: root.querySelector('.page-tab[aria-current="page"]')?.textContent?.trim(),
        routeTabRole: root.querySelector('.page-tab')?.getAttribute('role'),
        tablistRole: root.querySelector('.page-tabs')?.getAttribute('role'),
      }
    })
    expect(state).toEqual({
      href: '/dashboards/revenue/builder?draft=draft-7&page=details',
      currentPage: 'Overview',
      routeTabRole: null,
      tablistRole: null,
    })
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
      const visualBuilder = root.querySelector('.visual-builder') as HTMLElement
      const host = element as HTMLElement
      host.scrollTop = host.scrollHeight - host.clientHeight
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const reachable = visualBuilder.getBoundingClientRect()
      return {
        hostOverflowY: getComputedStyle(host).overflowY,
        hostScrollHeight: host.scrollHeight,
        hostClientHeight: host.clientHeight,
        visualBuilderReachable: reachable.top < innerHeight && reachable.bottom > 0,
        hostHorizontalOverflow: host.scrollWidth > host.clientWidth,
        documentHorizontalOverflow: document.documentElement.scrollWidth > innerWidth || document.body.scrollWidth > innerWidth,
      }
    })
    expect(state.hostOverflowY).toBe('auto')
    expect(state.hostScrollHeight).toBeGreaterThan(state.hostClientHeight)
    expect(state.visualBuilderReachable).toBe(true)
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
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      let visualCommand: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { visualCommand = event.detail }, { once: true })
      ;(root.querySelector('button[data-visual-type="bar"]') as HTMLButtonElement).click()
      return {
        commandPageID: command?.pageId,
        visualCommandPageID: visualCommand?.pageId,
        selectedTab: root.querySelector('.page-tab[aria-selected="true"]')?.textContent?.trim(),
        selectedBuilderTitle: root.querySelector('.visual-builder .pane-title')?.textContent?.trim(),
        emptyCanvas: Boolean(root.querySelector('.visual-empty')),
      }
    })
    expect(state.commandPageID).toBe('')
    expect(state.visualCommandPageID).toBe('page-2')
    expect(state.selectedTab).toBe('Page 2')
    expect(state.selectedBuilderTitle).toBe('Add a visual')
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
      return { ...beforeDismiss, unrelatedIgnored, pendingAddPage: element.pendingAddPage, alertAfterDismiss: Boolean(element.shadowRoot?.querySelector('[role="alert"]')) }
    })
    expect(state.title).toBe('Revenue draft')
    expect(state.pageCount).toBe(2)
    expect(state.failureKind).toBe('unavailable')
    expect(state.message).toContain('previous state was kept')
    expect(state.actions).toEqual(['Reload latest draft', 'Dismiss'])
    expect(state.unrelatedIgnored).toBe(true)
    expect(state.pendingAddPage).toBeNull()
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

test('dashboard builder authors report filters from governed fields through focused code mutations', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      const select = root.querySelector('.filter-add-select') as HTMLSelectElement
      select.value = 'orders.status'
      select.dispatchEvent(new Event('change', { bubbles: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { filters: [{ id: 'filter_1', label: 'Status', dimension: 'orders.status', controlType: 'multiSelect', required: false, readerEditable: true, targets: [], bindings: [{ id: 'filter_1', scope: 'report', targets: [] }] }] } })
      await element.updateComplete
      ;(root.querySelector('.filter-card') as HTMLButtonElement).click()
      await element.updateComplete
      ;(root.querySelectorAll('.filter-scope-option input')[1] as HTMLInputElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      ;(root.querySelectorAll('.filter-scope-option input')[0] as HTMLInputElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      ;(root.querySelector('.visual[gs-id="sales-chart"]') as HTMLElement).click()
      await element.updateComplete
      ;(root.querySelector('.filter-card') as HTMLButtonElement).click()
      await element.updateComplete
      ;(root.querySelectorAll('.filter-scope-option input')[2] as HTMLInputElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      const control = root.querySelector('.filter-editor select') as HTMLSelectElement
      control.value = 'singleSelect'
      control.dispatchEvent(new Event('change', { bubbles: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      const filterPane = root.querySelector('.filters-pane') as HTMLElement
      const filterSettings = root.querySelector('.filter-settings') as HTMLDetailsElement
      const filterActions = Array.from(root.querySelectorAll<HTMLButtonElement>('.filter-editor-actions button')).map((button) => button.textContent?.replace(/\s+/g, ' ').trim())
      ;(root.querySelector('.filter-remove') as HTMLButtonElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return {
        panes: Array.from(root.querySelectorAll('.right-dock > .pane')).map((pane: Element) => pane.classList.contains('filters-pane') ? 'filters' : pane.classList.contains('visual-builder') ? 'visual' : 'data'),
        scopes: Array.from(root.querySelectorAll('.filter-scope-heading')).map((heading: Element) => heading.textContent?.replace(/\s+/g, ' ').trim()),
        hasIntroCopy: Boolean(filterPane.querySelector('.pane-hint')),
        hasIdleDropZone: Boolean(filterPane.querySelector('.filter-drop-zone')),
        scopeOptionHelpCount: filterPane.querySelectorAll('.filter-scope-option small').length,
        settingsOpen: filterSettings.open,
        filterActions,
        commands,
      }
    })
    expect(state.panes).toEqual(['filters', 'visual', 'data'])
    expect(state.scopes).toEqual(['All pages1'])
    expect(state.hasIntroCopy).toBe(false)
    expect(state.hasIdleDropZone).toBe(false)
    expect(state.scopeOptionHelpCount).toBe(0)
    expect(state.settingsOpen).toBe(false)
    expect(state.filterActions).toEqual(['Add to canvas', 'Delete'])
    expect(state.commands[0]).toMatchObject({ action: 'add_filter', fieldId: 'orders.status', dataset: 'orders', controlType: 'multiSelect' })
    expect(state.commands[1]).toMatchObject({ action: 'set_filter_scope', filterId: 'filter_1', scope: 'page', pageId: 'overview' })
    expect(state.commands[2]).toMatchObject({ action: 'set_filter_scope', filterId: 'filter_1', scope: 'report' })
    expect(state.commands[2]).not.toHaveProperty('targets')
    expect(state.commands[3]).toMatchObject({ action: 'set_filter_scope', filterId: 'filter_1', scope: 'page', pageId: 'overview', targets: ['sales-chart'] })
    expect(state.commands[4]).toMatchObject({ action: 'update_filter', filterId: 'filter_1', controlType: 'singleSelect', readerEditable: true, required: false })
    expect(state.commands[5]).toMatchObject({ action: 'remove_filter', filterId: 'filter_1' })
  } finally {
    await page.close()
  }
})

test('dashboard builder places, moves, and removes canonical filter slicers on the shared grid', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const commands: Record<string, any>[] = []
      const filterCommands: Record<string, any>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      element.addEventListener('lv-builder-filter-command', (event: CustomEvent) => { filterCommands.push(event.detail) })
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { filters: [{ id: 'filter_1', label: 'Status', dimension: 'orders.status', controlType: 'multiSelect', required: false, readerEditable: true, targets: [], bindings: [{ id: 'filter_1', scope: 'report', targets: [] }] }] } })
      await element.updateComplete
      ;(root.querySelector('.filter-card') as HTMLButtonElement).click()
      await element.updateComplete
      ;(root.querySelector('.filter-placement-action') as HTMLButtonElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))

      const pages = structuredClone(element.builder.pages)
      pages[0].filterComponents = [{ id: 'status-slicer', filterId: 'filter_1', label: 'Status', controlType: 'multiSelect', placement: { col: 7, row: 1, colSpan: 3, rowSpan: 2 } }]
      const bindingKey = 'dashboard:revenue/report/filter_1'
      const unfiltered = { kind: 'unfiltered' }
      mergePatch({
        builder: { pages, revision: { id: 'rev-8', number: 8, contentHash: 'sha256:slicer' } },
        builderFilterContract: {
          applicationMode: 'immediate',
          definitions: { filter_1: { id: 'filter_1', label: 'Status', field: 'orders.status', dataset: 'orders', valueKind: 'string', predicates: [{ kind: 'set', operators: ['in', 'not_in'] }], options: { kind: 'distinct', limit: 50, includeNull: false, values: [] }, timezone: 'UTC', calendar: 'gregorian', weekStart: 'monday' } },
          bindings: { [bindingKey]: { key: bindingKey, id: 'filter_1', filter: 'filter_1', scope: 'report', default: unfiltered, selectionMode: 'multiple', maxSelectedValues: 0, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: ['overview/sales-chart'], optionDependencies: [] } },
        },
        builderFilterState: { revision: 1, appliedControls: { [bindingKey]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' },
        builderFilterOptionPages: { [bindingKey]: { bindingKey, servingStateID: 'generation-7', streamGeneration: 0, filterRevision: 1, requestGeneration: 1, items: [{ value: { kind: 'string', value: 'complete' }, label: 'Complete', null: false, selected: false, available: true }], complete: true, consumerIdentity: `option:${bindingKey}` } },
        builderFilterValidation: { accepted: true, message: '', currentRevision: 1, clientMutationID: '' },
      })
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      await element.updateComplete

      const slicer = root.querySelector('lv-slicer') as any
      await slicer.updateComplete
      const slicerLeaf = slicer.shadowRoot.querySelector('lv-filter-leaf') as any
      await slicerLeaf.updateComplete
      const slicerSelect = slicerLeaf.shadowRoot.querySelector('select') as HTMLSelectElement
      slicerSelect.value = JSON.stringify({ kind: 'string', value: 'complete' })
      slicerSelect.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await element.updateComplete

      const tile = root.querySelector('.filter-component') as HTMLElement
      const tileLabel = tile.getAttribute('aria-label')
      const selected = tile.getAttribute('data-selected')
      const preview = tile.textContent?.replace(/\s+/g, ' ').trim()
      tile.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', altKey: true, bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 20))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      ;(root.querySelector('.filter-placement-action') as HTMLButtonElement).click()
      await new Promise((resolve) => setTimeout(resolve, 20))

      return {
        commands,
        filterCommands,
        tileLabel,
        selected,
        preview,
        gridItems: root.querySelectorAll('.canvas > .grid-stack-item').length,
      }
    })
    expect(state.commands[0]).toMatchObject({ action: 'add_filter_component', pageId: 'overview', filterId: 'filter_1', componentId: '' })
    expect(state.commands[1]).toMatchObject({ action: 'set_placements', pageId: 'overview' })
    expect(state.commands[1].placements.map((placement: any) => placement.componentId).sort()).toEqual(['sales-chart', 'status-slicer'])
    expect(state.commands[2]).toMatchObject({ action: 'remove_filter_component', pageId: 'overview', componentId: 'status-slicer' })
    expect(state.filterCommands[0]).toMatchObject({ kind: 'mutate', baseRevision: 1, bindingKey: 'dashboard:revenue/report/filter_1', operation: 'set', expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'complete' }] } })
    expect(state.tileLabel).toContain('selected dashboard slicer')
    expect(state.selected).toBe('true')
    expect(state.preview).toContain('Status')
    expect(state.preview).toContain('Interactive draft preview')
    expect(state.gridItems).toBe(2)
  } finally {
    await page.close()
  }
})

test('dashboard builder accepts only the current filter option request generation', async () => {
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
        bindings: { [key]: { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] } },
      }
      const requests: any[] = []
      element.addEventListener('lv-builder-filter-options-request', (event: CustomEvent) => requests.push(event.detail))
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builderFilterContract: contract, builderFilterState: { revision: 1, appliedControls: { [key]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' } })
      await element.updateComplete
      element.dispatchEvent(new CustomEvent('lv-filter-options-needed', { bubbles: true, composed: true, detail: { bindingKey: key, search: '', limit: 20 } }))
      await element.updateComplete
      const valid = (requestGeneration: number, filterRevision = 1) => ({
        [key]: { bindingKey: key, servingStateID: 'generation-7', streamGeneration: 0, filterRevision, requestGeneration, items: [{ value: { kind: 'string', value: 'paid' } , label: 'Paid', null: false, selected: false, available: true }], complete: true, consumerIdentity: `option:${key}` },
      })
      mergePatch({ builderFilterOptionPages: valid(1) })
      await element.updateComplete
      const accepted = Object.keys(element.builderFilterOptionPages)
      element.dispatchEvent(new CustomEvent('lv-filter-options-needed', { bubbles: true, composed: true, detail: { bindingKey: key, search: '', limit: 20 } }))
      await element.updateComplete
      mergePatch({ builderFilterOptionPages: valid(1) })
      await element.updateComplete
      return { requests, accepted, afterStaleGeneration: Object.keys(element.builderFilterOptionPages), items: element.builderFilterOptionPages[key]?.items.map((item: any) => item.label) ?? [] }
    })
    expect(state.requests).toHaveLength(2)
    expect(state.requests.map((request: any) => request.requestGeneration)).toEqual([1, 2])
    expect(state.accepted).toEqual(['dashboard:revenue/report/status'])
    expect(state.afterStaleGeneration).toEqual([])
    expect(state.items).toEqual([])
  } finally {
    await page.close()
  }
})

test('dashboard builder clears pending filter commands and surfaces transport failures', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const key = 'dashboard:revenue/report/status'
      const unfiltered = { kind: 'unfiltered' }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        builderFilterContract: { applicationMode: 'immediate', definitions: { status: { id: 'status', label: 'Status', field: 'orders.status', dataset: 'orders', valueKind: 'string', predicates: [{ kind: 'set', operators: ['in'] }], options: { kind: 'distinct', limit: 20, includeNull: false, values: [] } } }, bindings: { [key]: { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] } } },
        builderFilterState: { revision: 1, appliedControls: { [key]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' },
      })
      await element.updateComplete
      element.dispatchEvent(new CustomEvent('lv-filter-mutate', { bubbles: true, composed: true, detail: { bindingKey: key, expression: { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'paid' }] } } }))
      const pendingBefore = element.builderFilterController.pending
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: element, argsRaw: { status: 503 } } }))
      await element.updateComplete
      return { pendingBefore, pendingAfter: element.builderFilterController.pending, error: element.shadowRoot.querySelector('.filter-validation')?.textContent?.trim() ?? '' }
    })
    expect(state.pendingBefore).toBe(true)
    expect(state.pendingAfter).toBe(false)
    expect(state.error).toContain('Dashboard filter update could not be completed')
  } finally {
    await page.close()
  }
})

test('dashboard builder authors one visual interaction target at a time', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const visual = (id: string, visualId: string, title: string, fields: string[], placement: Record<string, number>) => ({
        id, visualId, title, titleVisible: true, type: 'bar', legendVisible: true, axisVisible: true, dataLabelsVisible: false, formatOptions: [], placement,
        slots: fields.map((fieldId, index) => ({ id: `slot-${index}`, label: fieldId, kind: index === 0 ? 'dimension' : 'metric', fieldId, required: true })), filters: [],
      })
      const source = {
        ...visual('source-component', 'source-visual', 'Sales by status', ['orders.status', 'orders.total'], { col: 1, row: 1, colSpan: 4, rowSpan: 4 }),
        interaction: { configured: true, editable: true, mode: 'single', toggle: true, mappings: [{ field: 'orders.status', value: 'orders.status' }], targets: ['filter-visual'], highlightTargets: ['highlight-visual'], noneTargets: [] },
      }
      mergePatch({ builder: { selectedVisualId: 'source-component', pages: [{ id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [
        source,
        visual('filter-component', 'filter-visual', 'Filtered revenue', ['orders.total'], { col: 5, row: 1, colSpan: 4, rowSpan: 4 }),
        visual('filter-component-copy', 'filter-visual', 'Filtered revenue copy', ['orders.total'], { col: 9, row: 1, colSpan: 4, rowSpan: 4 }),
        visual('highlight-component', 'highlight-visual', 'Revenue comparison', ['orders.status', 'orders.total'], { col: 1, row: 5, colSpan: 4, rowSpan: 4 }),
      ], filterComponents: [] }] } })
      await element.updateComplete
      const rows = Array.from(root.querySelectorAll<HTMLElement>('[data-interaction-target]')).map((row) => ({
        id: row.dataset.interactionTarget,
        title: row.querySelector('.interaction-target-title')?.textContent?.trim(),
        checked: row.querySelector<HTMLInputElement>('input:checked')?.value,
        options: Array.from(row.querySelectorAll<HTMLInputElement>('input')).map((input) => ({ value: input.value, disabled: input.disabled })),
      }))
      root.querySelector<HTMLInputElement>('[data-interaction-target="highlight-visual"] input[value="none"]')?.click()
      await new Promise((resolve) => setTimeout(resolve, 20))
      return { rows, commands, sectionLabel: root.querySelector('.interaction-editor')?.getAttribute('aria-label') }
    })
    expect(state.sectionLabel).toBe('Visual interactions')
    expect(state.rows).toHaveLength(2)
    expect(state.rows.map((row) => ({ id: row.id, checked: row.checked }))).toEqual([
      { id: 'filter-visual', checked: 'filter' },
      { id: 'highlight-visual', checked: 'highlight' },
    ])
    expect(state.rows[0].options.find((option) => option.value === 'highlight')?.disabled).toBe(true)
    expect(state.rows[1].options.every((option) => !option.disabled)).toBe(true)
    expect(state.commands.at(-1)).toMatchObject({ action: 'set_interaction_target', pageId: 'overview', visualId: 'source-component', targetVisualId: 'highlight-component', effect: 'none' })
  } finally {
    await page.close()
  }
})

test('dashboard builder gates publishing on exact draft state and visible validation', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const commands: Record<string, unknown>[] = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { commands.push(event.detail) })
      const publish = root.querySelector<HTMLButtonElement>('[data-builder-action="publish"]')!
      const initial = { disabled: publish.disabled, label: publish.textContent?.trim(), preview: root.querySelector<HTMLAnchorElement>('[data-builder-action="preview"]')?.getAttribute('href') }
      publish.click()
      await element.updateComplete
      const publishing = { disabled: publish.disabled, label: publish.textContent?.trim() }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { hasUnpublishedChanges: false, lifecycle: 'published', save: { state: 'saved', message: 'Saved' } } })
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: element } }))
      await element.updateComplete
      const published = { disabled: publish.disabled, label: publish.textContent?.trim() }
      mergePatch({ builder: { hasUnpublishedChanges: true, diagnostics: [{ severity: 'error', code: 'INVALID_VISUAL', message: 'Choose a supported field.' }], preview: { href: '' } } })
      await element.updateComplete
      const details = root.querySelector<HTMLDetailsElement>('.secondary-details')!
      const blocked = { disabled: publish.disabled, title: publish.title, detailsOpen: details.open, summary: details.querySelector('summary')?.textContent?.trim(), previewLink: Boolean(root.querySelector('[data-builder-action="preview"]')) }
      return { initial, publishing, published, blocked, commands }
    })
    expect(state.initial.disabled).toBe(false)
    expect(state.initial.label).toBe('Publish')
    expect(state.initial.preview).toContain('revisionNumber=7')
    expect(state.publishing).toEqual({ disabled: true, label: 'Publishing…' })
    expect(state.published).toEqual({ disabled: true, label: 'Published' })
    expect(state.blocked.disabled).toBe(true)
    expect(state.blocked.title).toContain('Fix 1 validation error')
    expect(state.blocked.detailsOpen).toBe(true)
    expect(state.blocked.summary).toBe('Fix 1 validation error')
    expect(state.blocked.previewLink).toBe(false)
    expect(state.commands.at(-1)).toMatchObject({ action: 'publish', revisionId: 'rev-7', revisionNumber: '7' })
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  const visualCatalog = [
    ['line', 'Line chart', 'Cartesian'], ['area', 'Area chart', 'Cartesian'], ['bar', 'Bar chart', 'Cartesian'], ['column', 'Column chart', 'Cartesian'], ['pie', 'Pie chart', 'Part to whole'], ['donut', 'Donut chart', 'Part to whole'], ['scatter', 'Scatter chart', 'Distribution'], ['funnel', 'Funnel chart', 'Part to whole'],
    ['treemap', 'Treemap', 'Hierarchy & flow'], ['gauge', 'Gauge', 'Specialized'], ['heatmap', 'Heatmap', 'Distribution'], ['sankey', 'Sankey', 'Hierarchy & flow'], ['graph', 'Graph', 'Hierarchy & flow'], ['map', 'Map', 'Specialized'],
    ['candlestick', 'Candlestick chart', 'Cartesian'], ['boxplot', 'Boxplot', 'Distribution'], ['combo', 'Combo chart', 'Cartesian'], ['waterfall', 'Waterfall chart', 'Cartesian'], ['histogram', 'Histogram', 'Distribution'], ['radar', 'Radar chart', 'Specialized'], ['tree', 'Tree', 'Hierarchy & flow'], ['sunburst', 'Sunburst', 'Hierarchy & flow'], ['kpi', 'KPI', 'Specialized'],
    ['table', 'Table', 'Tables'], ['matrix', 'Matrix', 'Tables'], ['pivot', 'Pivot', 'Tables'],
  ].map(([type, label, group]) => ({ type, label, group, referenceHref: `/docs/visuals/${type}`, roles: type === 'table' || type === 'map' ? ['detail'] : type === 'kpi' || type === 'gauge' || type === 'histogram' || type === 'boxplot' ? ['metric'] : ['dimension', 'metric'] }))
  const signals = {
    builder: {
      projectId: 'sales', dashboardId: 'revenue', draftId: 'draft-7',
      revision: { id: 'rev-7', number: 7, contentHash: 'sha256:abc' },
      title: 'Revenue draft', lifecycle: 'draft', visibility: 'private', hasUnpublishedChanges: true,
      origin: { kind: 'file', label: 'Project file', sourcePath: 'dashboards/revenue.yaml' },
      sourceEvidence: { kind: 'project', projectId: 'sales', dashboardId: 'revenue', generationId: 'generation-7' },
      semanticModel: { id: 'commerce', title: 'Orders', datasets: [{ id: 'orders', title: 'Orders', fields: [{ id: 'orders.status', label: 'Status', kind: 'dimension', dataType: 'string' }, { id: 'orders.total', label: 'Total', kind: 'metric', dataType: 'decimal' }] }] },
      visualCatalog,
      filters: [],
      pages: [
        { id: 'overview', title: 'Overview', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [{ id: 'sales-chart', visualId: 'sales-chart', title: 'Sales by status', titleVisible: true, type: 'bar', legendVisible: true, axisVisible: true, dataLabelsVisible: false, formatOptions: [
          { key: 'axisVisible', label: 'Show axes', section: 'Display', control: 'toggle', value: 'true', choices: [] },
          { key: 'legend', label: 'Legend', section: 'Display', control: 'select', value: 'right', choices: [{ value: 'none', label: 'None' }, { value: 'top', label: 'Top' }, { value: 'right', label: 'Right' }, { value: 'bottom', label: 'Bottom' }, { value: 'left', label: 'Left' }] },
          { key: 'labels.density', label: 'Data labels', section: 'Display', control: 'select', value: 'hidden', choices: [{ value: 'hidden', label: 'Hidden' }, { value: 'automatic', label: 'Automatic' }, { value: 'dense', label: 'Dense' }, { value: 'always', label: 'Always' }] },
          { key: 'stacking', label: 'Stacking', section: 'Chart', control: 'select', value: 'none', choices: [{ value: 'none', label: 'None' }, { value: 'normal', label: 'Normal' }, { value: 'percent', label: 'Percent' }] },
        ], placement: { col: 1, row: 1, colSpan: 6, rowSpan: 5 }, slots: [{ id: 'category', label: 'Category', kind: 'dimension', fieldId: 'orders.status', required: true }], filters: [] }], filterComponents: [] },
        { id: 'details', title: 'Details', canvas: { width: 1200, height: 800 }, grid: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, visuals: [], filterComponents: [] },
      ],
      selectedPageId: 'overview', selectedVisualId: 'sales-chart',
      capabilities: { canEdit: true, canShare: true, canPublish: true, canPreview: true, canExport: true, canAddPage: true, canAddVisual: true },
      diagnostics: [{ severity: 'warning', code: 'FIELD_REQUIRED', message: 'Add a metric to complete this visual.' }],
      preview: { active: false, mode: 'draft', loading: false, href: '/dashboards/revenue/preview?draft=draft-7&revisionId=rev-7&revisionNumber=7&revisionContentHash=sha256%3Aabc' }, save: { state: 'dirty', message: '2 changes' },
    },
    status: { loading: false, error: '', generation: 0, lastUpdated: '', refreshId: '', setupRequired: false, progressPercent: 100 },
    runtime: { kind: 'dashboard_builder', projectId: 'sales', servingStateId: 'generation-7', dashboardId: 'revenue' },
  }
  return `<!doctype html><html><head><style>html,body{margin:0;min-height:100%;}body{${typographyTestTokens}--lv-bg-app:#f6f8fa;--lv-bg-panel:#fff;--lv-bg-panel-muted:#f6f8fa;--lv-bg-control:#f6f8fa;--lv-bg-control-hover:#f3f4f6;--lv-bg-input:#fff;--lv-bg-accent-muted:#ddf4ff;--lv-bg-danger-muted:#ffebe9;--lv-fg-default:#24292f;--lv-fg-muted:#57606a;--lv-fg-accent:#0969da;--lv-fg-danger:#d1242f;--lv-fg-warning:#9a6700;--lv-fg-success:#1a7f37;--lv-border-muted:#d8dee4;--lv-border-default:#d0d7de;--lv-line-default:#d0d7de;--lv-line-muted:#d8dee4;--lv-line-emphasis:#57606a;--lv-data-1:#0969da;--lv-data-1-muted:#ddf4ff;--lv-data-2:#1a7f37;--lv-data-2-muted:#dafbe1;--lv-data-3:#8250df;--lv-data-3-muted:#fbefff;--lv-data-4:#cf222e;--lv-data-4-muted:#ffebe9;--lv-data-5:#1b7c83;--lv-data-5-muted:#ddf4ff;--lv-data-6:#bf3989;--lv-data-6-muted:#ffeff7;--lv-border-width:1px;--lv-border-width-focus:2px;--lv-radius-default:6px;--lv-radius-small:4px;--lv-radius-full:999px;--base-size-2:2px;--base-size-4:4px;--base-size-6:6px;--base-size-8:8px;--base-size-12:12px;--base-size-16:16px;--control-medium-size:32px;--control-small-size:28px;--lv-button-radius:6px;--lv-button-padding-inline:12px;--lv-button-fg-rest:#24292f;--lv-button-bg-rest:#fff;--lv-button-bg-hover:#f6f8fa;--lv-button-accent-border-rest:#0969da;--lv-button-accent-fg-rest:#fff;--lv-button-accent-bg-rest:#0969da;--lv-button-accent-bg-hover:#0757b3;--lv-shadow-floating-sm:0 2px 8px rgb(0 0 0 / 12%);}</style></head><body><main data-signals="${escapeHTML(JSON.stringify(signals))}"><lv-dashboard-builder back-href="/dashboards/revenue" preview-href="/dashboards/revenue/preview?draft=draft-7&revisionId=rev-6&revisionNumber=6&revisionContentHash=sha256%3Aold"></lv-dashboard-builder></main><script type="module" src="/dashboard-builder-under-test.js"></script><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script></body></html>`
}

function escapeHTML(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}
