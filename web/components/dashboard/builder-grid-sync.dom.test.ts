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

test('GridStack reconciles a complete CFO page layout without shifting canonical nodes', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const result = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const currentPages = structuredClone(element.builder.pages)
      const sourceVisual = currentPages[0].visuals[0]
      const visuals = [
        ['cash', { col: 1, row: 6, colSpan: 6, rowSpan: 6 }],
        ['ebitda', { col: 9, row: 3, colSpan: 4, rowSpan: 3 }],
        ['margin', { col: 5, row: 3, colSpan: 4, rowSpan: 3 }],
        ['monthly', { col: 7, row: 6, colSpan: 6, rowSpan: 4 }],
        ['product', { col: 1, row: 12, colSpan: 6, rowSpan: 5 }],
        ['revenue', { col: 1, row: 3, colSpan: 4, rowSpan: 3 }],
        ['variance', { col: 7, row: 10, colSpan: 6, rowSpan: 5 }],
      ] as const
      const filters = [
        ['filter-channel', { col: 1, row: 1, colSpan: 4, rowSpan: 2 }],
        ['filter-region', { col: 5, row: 1, colSpan: 4, rowSpan: 2 }],
        ['filter-period', { col: 9, row: 1, colSpan: 4, rowSpan: 2 }],
      ] as const
      const makePage = (target: boolean) => ({
        ...currentPages[0],
        visuals: visuals.map(([id, initialPlacement]) => {
          const placement = target ? {
            cash: { col: 7, row: 11, colSpan: 6, rowSpan: 6 },
            ebitda: { col: 5, row: 8, colSpan: 4, rowSpan: 3 },
            margin: { col: 1, row: 6, colSpan: 4, rowSpan: 3 },
            monthly: { col: 1, row: 16, colSpan: 6, rowSpan: 4 },
            product: { col: 1, row: 11, colSpan: 6, rowSpan: 5 },
            revenue: { col: 1, row: 3, colSpan: 4, rowSpan: 3 },
            variance: { col: 5, row: 3, colSpan: 6, rowSpan: 5 },
          }[id] : initialPlacement
          return { ...sourceVisual, id, visualId: id, title: id, placement, slots: [] }
        }),
        filterComponents: filters.map(([id, initialPlacement]) => ({
          id,
          filterId: id,
          label: id,
          controlType: 'multiSelect',
          placement: initialPlacement,
        })),
      })
      const expected = (page: any) => Object.fromEntries([
        ...page.visuals.map((item: any) => [item.id, item.placement]),
        ...page.filterComponents.map((item: any) => [item.id, item.placement]),
      ])
      const readActual = (root: ShadowRoot) => Object.fromEntries(
        Array.from(root.querySelectorAll<HTMLElement>('.canvas > .grid-stack-item')).map((tile) => {
          const node = (tile as any).gridstackNode
          return [tile.getAttribute('gs-id'), {
            col: (node?.x ?? -1) + 1,
            row: (node?.y ?? -1) + 1,
            colSpan: node?.w ?? -1,
            rowSpan: node?.h ?? -1,
          }]
        }),
      )

      const initialPage = makePage(false)
      mergePatch({ builder: { pages: [initialPage, currentPages[1]] } })
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const initialGrid = (root.querySelector('.canvas') as any)?.gridstack
      const initialActual = readActual(root)

      const targetPage = makePage(true)
      mergePatch({
        builder: {
          pages: [targetPage, currentPages[1]],
          revision: { id: 'rev-8', number: 8, contentHash: 'sha256:cfo-layout' },
        },
      })
      await element.updateComplete
      await new Promise((resolve) => setTimeout(resolve, 25))
      const canvas = root.querySelector('.canvas') as any
      return {
        retainedGrid: initialGrid === canvas.gridstack,
        initialExpected: expected(initialPage),
        initialActual,
        targetExpected: expected(targetPage),
        targetActual: readActual(root),
        itemCount: root.querySelectorAll('.canvas > .grid-stack-item').length,
      }
    })

    expect(result.itemCount).toBe(10)
    expect(result.retainedGrid).toBe(true)
    expect(result.initialActual).toEqual(result.initialExpected)
    expect(result.targetActual).toEqual(result.targetExpected)
  } finally {
    await page.close()
  }
})

test('removing and restoring visuals after grid sorting leaves no orphan tiles', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const states = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const pages = structuredClone(element.builder.pages)
      const source = pages[0].visuals[0]
      const visuals = [
        { ...source, id: 'first', placement: { col: 1, row: 8, colSpan: 4, rowSpan: 3 } },
        { ...source, id: 'second', placement: { col: 1, row: 1, colSpan: 4, rowSpan: 3 } },
        { ...source, id: 'copy', placement: { col: 5, row: 1, colSpan: 4, rowSpan: 3 } },
      ]
      const states: string[][] = []
      for (const ids of [['first', 'second', 'copy'], ['first', 'second'], ['first', 'second', 'copy'], ['first', 'second']]) {
        mergePatch({ builder: { pages: [{ ...pages[0], visuals: visuals.filter(visual => ids.includes(visual.id)), filterComponents: [], headers: [], placeholders: [] }, pages[1]] } })
        await element.updateComplete
        await new Promise(resolve => setTimeout(resolve, 25))
        states.push(Array.from(element.shadowRoot.querySelectorAll('.canvas > .grid-stack-item'), (tile: any) => tile.getAttribute('gs-id')).sort())
      }
      return states
    })
    expect(states).toEqual([
      ['copy', 'first', 'second'], ['first', 'second'],
      ['copy', 'first', 'second'], ['first', 'second'],
    ])
  } finally {
    await page.close()
  }
})

test('all eight resize handles remain above the real chart header', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const hits = await page.locator('lv-dashboard-builder').evaluate(async (element: any, preview) => {
      document.body.style.setProperty('--zIndex-sticky', '100')
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { preview: { active: true } }, builderVisuals: { 'sales-chart': preview } })
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      await (root.querySelector('lv-visualization-host') as any).updateComplete
      await new Promise(resolve => setTimeout(resolve, 200))
      return Array.from(root.querySelectorAll<HTMLElement>('.visual[data-selected="true"] > .ui-resizable-handle')).map(handle => {
        const box = handle.getBoundingClientRect()
        return { handle: handle.className, reachable: root.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2) === handle }
      })
    }, governedBarPreviewEnvelope('sha256:resize-hit-targets'))
    expect(hits).toHaveLength(8)
    expect(hits.filter(hit => !hit.reachable)).toEqual([])
  } finally {
    await page.close()
  }
})

test('an authored zero grid gap does not restore the default margin', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const margin = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const pages = structuredClone(element.builder.pages)
      pages[0].grid.gap = 0
      mergePatch({ builder: { pages } })
      await element.updateComplete
      return element.shadowRoot.querySelector('.canvas').gridstack.opts.margin
    })
    expect(margin).toBe(0)
  } finally {
    await page.close()
  }
})

test('authored page padding insets the scaled grid without changing canvas scale or cell geometry', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const builder = page.locator('lv-dashboard-builder')
    const desktop = await builder.evaluate(async (element: any) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const currentPages = structuredClone(element.builder.pages)
      const read = () => {
        const root = element.shadowRoot as ShadowRoot
        const fit = root.querySelector('.canvas-fit') as HTMLElement
        const canvas = root.querySelector('.canvas.grid-stack') as HTMLElement
        const fitRect = fit.getBoundingClientRect()
        const canvasRect = canvas.getBoundingClientRect()
        const gridItems = Array.from(root.querySelectorAll<HTMLElement>('.canvas > .grid-stack-item')).map((item) => {
          const node = (item as any).gridstackNode
          const rect = item.getBoundingClientRect()
          return {
            id: item.getAttribute('gs-id'),
            node: { x: node?.x ?? -1, y: node?.y ?? -1, w: node?.w ?? -1, h: node?.h ?? -1 },
            rect: { x: rect.x, y: rect.y, right: rect.right, bottom: rect.bottom, width: rect.width, height: rect.height },
          }
        }).sort((a, b) => String(a.id).localeCompare(String(b.id)))
        let overlapCount = 0
        for (let i = 0; i < gridItems.length; i++) for (let j = i + 1; j < gridItems.length; j++) {
          const a = gridItems[i].rect, b = gridItems[j].rect
          if (Math.min(a.right, b.right) - Math.max(a.x, b.x) > 1 && Math.min(a.bottom, b.bottom) - Math.max(a.y, b.y) > 1) overlapCount++
        }
        const page = element.builder.pages.find((item: any) => item.id === element.builder.selectedPageId)
        const scale = Number(canvas.style.getPropertyValue('--builder-canvas-scale'))
        return {
          padding: page.grid.padding,
          logicalWidth: page.canvas.width,
          scale,
          fit: { x: fitRect.x, y: fitRect.y, width: fitRect.width, height: fitRect.height },
          canvas: { x: canvasRect.x, y: canvasRect.y, width: canvasRect.width, height: canvasRect.height, bottom: canvasRect.bottom },
          css: { position: getComputedStyle(canvas).position, width: getComputedStyle(canvas).width, minHeight: getComputedStyle(canvas).minHeight, offset: canvas.style.getPropertyValue('--builder-grid-offset'), gridWidth: canvas.style.getPropertyValue('--builder-grid-width') },
          gridItems,
          overlapCount,
        }
      }
      const applyPadding = async (padding: number) => {
        const current = structuredClone(element.builder.pages[0])
        mergePatch({ builder: { pages: [{ ...current, grid: { ...current.grid, padding } }, currentPages[1]] } })
        await element.updateComplete
        await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
        return read()
      }
      const baseline = read()
      const padded = await applyPadding(24)
      const zero = await applyPadding(0)
      const restored = await applyPadding(baseline.padding)
      return { baseline, padded, zero, restored }
    })

    expect(desktop.baseline.padding).toBe(16)
    expect(desktop.padded.padding).toBe(24)
    expect(desktop.padded.scale).toBeCloseTo(desktop.baseline.scale, 4)
    expect(desktop.padded.fit.width).toBeCloseTo(desktop.baseline.fit.width, 2)
    expect(desktop.padded.canvas.x - desktop.baseline.canvas.x).toBeCloseTo(8 * desktop.baseline.scale, 1)
    expect(desktop.padded.canvas.y - desktop.baseline.canvas.y).toBeCloseTo(8 * desktop.baseline.scale, 1)
    expect(desktop.baseline.canvas.width - desktop.padded.canvas.width).toBeCloseTo(16 * desktop.baseline.scale, 1)
    expect(desktop.padded.fit.height).toBeCloseTo(desktop.baseline.fit.height, 2)
    expect(desktop.padded.canvas.height).toBeCloseTo(desktop.baseline.canvas.height - 16 * desktop.baseline.scale, 1)
    expect(desktop.baseline.fit.y + desktop.baseline.fit.height - desktop.baseline.canvas.bottom).toBeCloseTo(16 * desktop.baseline.scale, 1)
    expect(desktop.padded.fit.y + desktop.padded.fit.height - desktop.padded.canvas.bottom).toBeCloseTo(24 * desktop.baseline.scale, 1)
    expect(desktop.padded.gridItems.map((item: any) => [item.id, item.node])).toEqual(desktop.baseline.gridItems.map((item: any) => [item.id, item.node]))
    expect(desktop.baseline.overlapCount).toBe(0)
    expect(desktop.padded.overlapCount).toBe(0)
    expect(desktop.zero.padding).toBe(0)
    expect(desktop.zero.canvas.x).toBeCloseTo(desktop.zero.fit.x, 2)
    expect(desktop.zero.canvas.y).toBeCloseTo(desktop.zero.fit.y, 2)
    expect(desktop.zero.canvas.width).toBeCloseTo(desktop.zero.logicalWidth * desktop.zero.scale, 2)
    expect(desktop.zero.canvas.height).toBeCloseTo(desktop.zero.fit.height, 1)
    expect(desktop.restored.padding).toBe(16)
    expect(desktop.restored.canvas.x).toBeCloseTo(desktop.baseline.canvas.x, 2)
    expect(desktop.restored.canvas.y).toBeCloseTo(desktop.baseline.canvas.y, 2)
    expect(desktop.restored.canvas.width).toBeCloseTo(desktop.baseline.canvas.width, 2)
    expect(desktop.restored.gridItems.map((item: any) => [item.id, item.node])).toEqual(desktop.baseline.gridItems.map((item: any) => [item.id, item.node]))

    await page.setViewportSize({ width: 390, height: 844 })
    await page.waitForTimeout(100)
    const mobile = await builder.evaluate(async (element: any) => {
      await element.updateComplete
      const canvas = element.shadowRoot.querySelector('.canvas.grid-stack') as HTMLElement
      const scroll = element.shadowRoot.querySelector('.canvas-scroll') as HTMLElement
      return { position: getComputedStyle(canvas).position, width: canvas.getBoundingClientRect().width, availableWidth: scroll.clientWidth, height: canvas.style.height }
    })
    expect(mobile.position).toBe('relative')
    expect(mobile.width).toBeLessThanOrEqual(mobile.availableWidth + 1)
    expect(mobile.height).toBe('')
    await page.screenshot({ path: join(root, 'builder-grid-padding-mobile.png'), fullPage: true })
  } finally {
    await page.close()
  }
})
