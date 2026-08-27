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
      const pickerButtons = Array.from(root.querySelectorAll('.visual-picker-button')).map((button) => ({
        type: button.getAttribute('data-visual-picker-type'),
        label: button.getAttribute('aria-label'),
        title: button.getAttribute('title'),
        hasIcon: Boolean(button.querySelector('svg')),
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
        tabs,
        panel: root.querySelector('[role="tabpanel"]')?.getAttribute('aria-label'),
        pageBarBelowCanvas: Boolean(canvas && pageBar && pageBar.top >= canvas.top + canvas.height - 1),
        pageBarSharesCanvasWidth: Boolean(canvas && pageBar && pageBar.left >= canvas.left - 1 && pageBar.right <= canvas.right + 1),
        pageBarBeforeVisualBuilder: Boolean(pageBar && visualBuilder && pageBar.bottom <= visualBuilder.bottom + 1),
        rightDockContainsBothPanes: root.querySelectorAll('.right-dock > .visual-builder, .right-dock > .data-pane').length === 2,
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
    expect(state.rightDockContainsBothPanes).toBe(true)
    expect(state.dataPaneRightOfVisual).toBe(true)
    expect(state.pageBarVisible).toBe(true)
    expect(state.pickerButtons).toEqual([
      { type: 'bar', label: 'Bar chart', title: 'Bar', hasIcon: true, color: 'rgb(9, 105, 218)' },
      { type: 'column', label: 'Column chart', title: 'Column', hasIcon: true, color: 'rgb(26, 127, 55)' },
      { type: 'line', label: 'Line chart', title: 'Line', hasIcon: true, color: 'rgb(130, 80, 223)' },
      { type: 'area', label: 'Area chart', title: 'Area', hasIcon: true, color: 'rgb(207, 34, 46)' },
      { type: 'table', label: 'Table chart', title: 'Table', hasIcon: true, color: 'rgb(27, 124, 131)' },
    ])
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
          horizontalOverflow: document.documentElement.scrollWidth > innerWidth || document.body.scrollWidth > innerWidth,
        }
      })
    }

    const stackedRight = await measure(1100)
    expect(stackedRight.visual.left).toBeGreaterThanOrEqual(stackedRight.canvas.right - 1)
    expect(stackedRight.data.left).toBeCloseTo(stackedRight.visual.left, 0)
    expect(stackedRight.data.top).toBeGreaterThanOrEqual(stackedRight.visual.bottom - 1)
    expect(stackedRight.searchVisible).toBe(true)
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
        formatPlaceholder: root.querySelector('.format-placeholder')?.textContent?.trim(),
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
    expect(state.formatted.formatPlaceholder).toContain('Formatting is next.')
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
    expect(state.topLevelActions).toEqual(['Preview', 'more', 'Publish'])
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
        addPageLabel: root.querySelector('button[aria-label="Add page"]')?.textContent?.trim(),
      }
    })
    expect(state.canvasScrollCount).toBe(1)
    expect(state.scrollPadding).toBe('0px')
    expect(state.canvasBorder).toMatch(/^0px none /)
    expect(state.canvasRadius).toBe('0px')
    expect(state.canvasShadow).toBe('none')
    expect(state.emptyPreview).toContain('Add fields')
    expect(state.addPageLabel).toBe('+')
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
        return { title: tile.querySelector('.visual-drag-header')?.textContent?.trim(), left: box.left, right: box.right, top: box.top, bottom: box.bottom, height: box.height, type: tile.getAttribute('data-visual-type'), position: style.position, order: style.order, topOffset: style.top, leftOffset: style.left, authoredTop: tile.style.top }
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
      let visualCommand: Record<string, unknown> | undefined
      element.addEventListener('lv-builder-command', (event: CustomEvent) => { visualCommand = event.detail }, { once: true })
      ;(root.querySelector('.add-selected-visual') as HTMLButtonElement).click()
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
    expect(state.selectedBuilderTitle).toBe('Visual builder')
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
  return `<!doctype html><html><head><style>html,body{margin:0;min-height:100%;}body{${typographyTestTokens}--lv-bg-app:#f6f8fa;--lv-bg-panel:#fff;--lv-bg-panel-muted:#f6f8fa;--lv-bg-control:#f6f8fa;--lv-bg-control-hover:#f3f4f6;--lv-bg-input:#fff;--lv-bg-accent-muted:#ddf4ff;--lv-bg-danger-muted:#ffebe9;--lv-fg-default:#24292f;--lv-fg-muted:#57606a;--lv-fg-accent:#0969da;--lv-fg-danger:#d1242f;--lv-fg-warning:#9a6700;--lv-fg-success:#1a7f37;--lv-border-muted:#d8dee4;--lv-border-default:#d0d7de;--lv-line-default:#d0d7de;--lv-line-muted:#d8dee4;--lv-line-emphasis:#57606a;--lv-data-1:#0969da;--lv-data-1-muted:#ddf4ff;--lv-data-2:#1a7f37;--lv-data-2-muted:#dafbe1;--lv-data-3:#8250df;--lv-data-3-muted:#fbefff;--lv-data-4:#cf222e;--lv-data-4-muted:#ffebe9;--lv-data-5:#1b7c83;--lv-data-5-muted:#ddf4ff;--lv-border-width:1px;--lv-border-width-focus:2px;--lv-radius-default:6px;--lv-radius-small:4px;--lv-radius-full:999px;--base-size-2:2px;--base-size-4:4px;--base-size-6:6px;--base-size-8:8px;--base-size-12:12px;--base-size-16:16px;--control-medium-size:32px;--control-small-size:28px;--lv-button-radius:6px;--lv-button-padding-inline:12px;--lv-button-fg-rest:#24292f;--lv-button-bg-rest:#fff;--lv-button-bg-hover:#f6f8fa;--lv-button-accent-border-rest:#0969da;--lv-button-accent-fg-rest:#fff;--lv-button-accent-bg-rest:#0969da;--lv-button-accent-bg-hover:#0757b3;--lv-shadow-floating-sm:0 2px 8px rgb(0 0 0 / 12%);}</style></head><body><main data-signals="${escapeHTML(JSON.stringify(signals))}"><lv-dashboard-builder back-href="/dashboards/revenue" preview-href="/dashboards/revenue/preview?draft=draft-7&revisionId=rev-6&revisionNumber=6&revisionContentHash=sha256%3Aold"></lv-dashboard-builder></main><script type="module" src="/dashboard-builder-under-test.js"></script><script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script></body></html>`
}

function escapeHTML(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;')
}
