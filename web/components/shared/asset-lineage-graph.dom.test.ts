import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { expect as browserExpect } from '@playwright/test'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/asset-lineage-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const file = normalize(join(root, url.pathname))
    if (!file.startsWith(root)) {
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
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('asset lineage graph carries React Flow layout styles inside shadow hosts', async () => {
  const page = await browser.newPage({ viewport: { width: 1180, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-asset-lineage-graph'))
    await page.waitForFunction(() => {
      const host = document.querySelector('lineage-test-host') as HTMLElement & { shadowRoot: ShadowRoot }
      return Boolean(host?.shadowRoot?.querySelector('.react-flow__node'))
    })

    const state = await page.evaluate(() => {
      const host = document.querySelector('lineage-test-host') as HTMLElement & { shadowRoot: ShadowRoot }
      const graph = host.shadowRoot.querySelector('lv-asset-lineage-graph') as HTMLElement
      const flow = graph.querySelector('.react-flow') as HTMLElement
      const viewport = graph.querySelector('.react-flow__viewport') as HTMLElement
      const node = graph.querySelector('.react-flow__node') as HTMLElement
      const edge = graph.querySelector('.react-flow__edges') as HTMLElement
      const edgePath = graph.querySelector('.react-flow__edge') as SVGElement
      const controlButton = graph.querySelector('.asset-lineage-viewport-controls button') as HTMLElement
      const flowRect = flow.getBoundingClientRect()
      const nodeRect = node.getBoundingClientRect()
      const controlRect = controlButton.getBoundingClientRect()
      return {
        flowHeight: Math.round(flowRect.height),
        flowWidth: Math.round(flowRect.width),
        inspectorPanels: graph.querySelectorAll('.asset-lineage-panel').length,
        viewportPosition: getComputedStyle(viewport).position,
        viewportMatchesFlowWidth: Math.round(Number.parseFloat(getComputedStyle(viewport).width)) === Math.round(flowRect.width),
        edgePosition: getComputedStyle(edge).position,
        edgeRouting: edgePath.getAttribute('class') ?? '',
        nodePosition: getComputedStyle(node).position,
        nodeInsideFlow: nodeRect.top >= flowRect.top && nodeRect.top < flowRect.bottom,
        controlDisplay: getComputedStyle(controlButton).display,
        controlWidth: Math.round(controlRect.width),
        controlHeight: Math.round(controlRect.height),
      }
    })

    expect(state).toEqual({
      flowHeight: expect.any(Number),
      flowWidth: 900,
      inspectorPanels: 0,
      viewportPosition: 'absolute',
      viewportMatchesFlowWidth: true,
      edgePosition: 'absolute',
      edgeRouting: expect.stringContaining('react-flow__edge-smoothstep'),
      nodePosition: 'absolute',
      nodeInsideFlow: true,
      controlDisplay: 'flex',
      controlWidth: 36,
      controlHeight: 36,
    })
  } finally {
    await page.close()
  }
})

test('asset lineage selection clears from the background and Escape', async () => {
  const page = await browser.newPage({ viewport: { width: 1180, height: 760 } })
  try {
    await page.goto(baseURL)
    const graph = page.locator('lineage-test-host').locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    expect(await graph.locator('.asset-lineage-node-selected').count()).toBe(1)
    expect(await graph.locator('.asset-lineage-clear-button').count()).toBe(0)

    await graph.locator('.react-flow__renderer').dispatchEvent('click')
    expect(await graph.locator('.asset-lineage-node-selected').count()).toBe(0)

    await graph.locator('.react-flow__node').filter({ hasText: 'orders' }).click()
    expect(await graph.locator('.asset-lineage-node-selected').count()).toBe(1)
    await graph.locator('.asset-lineage-node-selected').press('Escape')
    expect(await graph.locator('.asset-lineage-node-selected').count()).toBe(0)

  } finally {
    await page.close()
  }
})

test('asset lineage keeps the complete upstream and downstream path highlighted when an intermediate model is selected', async () => {
  const page = await browser.newPage({ viewport: { width: 1180, height: 760 } })
  try {
    await page.goto(baseURL)
    const graph = page.locator('lineage-test-host').locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    await graph.evaluate((element: HTMLElement & { graph: any }) => {
      element.graph = {
        nodes: [
          { id: 'connection', label: 'CFO demo managed files', kind: 'connection', rank: -2 },
          { id: 'source', label: 'Microsoft Financial Sample', kind: 'source', rank: -1, selected: true },
          { id: 'model', label: 'P&L Lines', kind: 'model', rank: 0 },
          { id: 'semantic', label: 'CFO Finance Model', kind: 'semantic_model', rank: 1 },
          { id: 'dashboard', label: 'CFO Command Center', kind: 'dashboard', rank: 2 },
        ],
        edges: [
          { id: 'connection-source', source: 'connection', target: 'source', kind: 'lineage_connection_source' },
          { id: 'source-model', source: 'source', target: 'model', kind: 'lineage_source_model' },
          { id: 'model-semantic', source: 'model', target: 'semantic', kind: 'lineage_model_semantic_model' },
          { id: 'semantic-dashboard', source: 'semantic', target: 'dashboard', kind: 'lineage_semantic_model_dashboard' },
        ],
      }
    })
    await graph.locator('.asset-lineage-node', { hasText: 'P&L Lines' }).click()

    const states = await graph.locator('.asset-lineage-node').evaluateAll((nodes) => Object.fromEntries(nodes.map((node) => [
      node.querySelector('.asset-lineage-node-title')?.textContent?.trim(),
      Array.from(node.classList).find((name) => name.match(/^asset-lineage-node-(selected|upstream|downstream|unrelated)$/)),
    ])))
    expect(states).toEqual({
      'CFO demo managed files': 'asset-lineage-node-upstream',
      'Microsoft Financial Sample': 'asset-lineage-node-upstream',
      'P&L Lines': 'asset-lineage-node-selected',
      'CFO Finance Model': 'asset-lineage-node-downstream',
      'CFO Command Center': 'asset-lineage-node-downstream',
    })
  } finally {
    await page.close()
  }
})

test('asset lineage uses a non-looping route for peers in the same rank', async () => {
  const page = await browser.newPage({ viewport: { width: 1180, height: 760 } })
  try {
    await page.goto(baseURL)
    const graph = page.locator('lineage-test-host').locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    await graph.evaluate((element: HTMLElement & { graph: any }) => {
      element.graph = {
        nodes: [
          { id: 'model-a', label: 'Model A', kind: 'model', rank: -1 },
          { id: 'model-b', label: 'Model B', kind: 'model', rank: -1 },
        ],
        edges: [{ id: 'model-a-model-b', source: 'model-a', target: 'model-b', kind: 'uses_model' }],
      }
    })
    const edge = graph.locator('.react-flow__edge[data-id="model-a-model-b"]')
    await edge.waitFor({ state: 'attached' })
    expect(await edge.getAttribute('class')).toContain('react-flow__edge-smoothstep')
  } finally {
    await page.close()
  }
})

test('asset lineage keeps dense graphs readable on initial fit', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 720 } })
  try {
    await page.goto(`${baseURL}?dense=1`)
    const graph = page.locator('lineage-test-host').locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    await page.waitForFunction(() => {
      const graph = document.querySelector('lineage-test-host')?.shadowRoot?.querySelector('lv-asset-lineage-graph')
      return graph?.querySelectorAll('.react-flow__node').length === 14
    })

    const state = await graph.evaluate((element) => {
      const viewport = element.querySelector('.react-flow__viewport') as HTMLElement
      const flow = element.querySelector('.react-flow') as HTMLElement
      const selected = element.querySelector('.asset-lineage-node-selected') as HTMLElement
      const match = (viewport as HTMLElement).style.transform.match(/scale\(([-\d.]+)\)/)
      const flowRect = flow.getBoundingClientRect()
      const selectedRect = selected.getBoundingClientRect()
      const nodeRects = Array.from(element.querySelectorAll<HTMLElement>('.react-flow__node')).map((node) => node.getBoundingClientRect())
      return {
        scale: Number(match?.[1]),
        selectedVisible: selectedRect.top >= flowRect.top
          && selectedRect.bottom <= flowRect.bottom
          && selectedRect.left < flowRect.right
          && selectedRect.right > flowRect.left,
        allNodesVerticallyVisible: nodeRects.every((rect) => rect.top >= flowRect.top && rect.bottom <= flowRect.bottom && rect.left >= flowRect.left && rect.right <= flowRect.right),
      }
    })
    expect(state.scale).toBeGreaterThan(0)
    expect(state.selectedVisible).toBe(true)
    expect(state.allNodesVerticallyVisible).toBe(true)
  } finally {
    await page.close()
  }
})

test('lineage toolbar opens assets and filters actual dependency paths', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const graph = page.locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    await graph.evaluate((element: HTMLElement & { graph: any }) => {
      element.graph = {
        nodes: [
          { id: 'a', label: 'Orders source', kind: 'source', href: '/sources/a' },
          { id: 'b', label: 'Sales model', kind: 'model', selected: true, href: '/models/b' },
          { id: 'c', label: 'Revenue dashboard', kind: 'dashboard', href: '/dashboards/c' },
          { id: 'd', label: 'Other source', kind: 'source' },
        ],
        edges: [{ id: 'ab', source: 'a', target: 'b', kind: 'uses' }, { id: 'bc', source: 'b', target: 'c', kind: 'uses' }],
      }
    })
    await browserExpect.poll(() => graph.getByRole('link', { name: 'Open asset' }).getAttribute('href')).toBe('/models/b')
    await graph.getByRole('button', { name: /^Upstream/ }).click()
    await browserExpect.poll(() => graph.locator('.react-flow__node').count()).toBe(2)
    expect(await graph.locator('.react-flow__node').allTextContents()).toEqual(expect.arrayContaining([expect.stringContaining('Orders source'), expect.stringContaining('Sales model')]))
    await graph.getByRole('button', { name: /^Downstream/ }).click()
    await browserExpect.poll(() => graph.locator('.react-flow__node').count()).toBe(2)
    expect(await graph.locator('.react-flow__node').allTextContents()).toEqual(expect.arrayContaining([expect.stringContaining('Revenue dashboard')]))
    await graph.getByRole('button', { name: 'Show all', exact: true }).click()
    await browserExpect.poll(() => graph.locator('.react-flow__node').count()).toBe(4)
    await graph.getByRole('combobox', { name: 'Find asset' }).selectOption('a')
    await browserExpect.poll(() => graph.getByRole('link', { name: 'Open asset' }).getAttribute('href')).toBe('/sources/a')
    await graph.getByRole('button', { name: 'Focus selected' }).click()
    await graph.getByRole('button', { name: 'Fit graph' }).click()
  } finally { await page.close() }
})

test('lineage zoom, fit, resize and signal refresh preserve a usable view', async () => {
  const page = await browser.newPage({ viewport: { width: 1180, height: 760 } })
  try {
    await page.goto(baseURL)
    const graph = page.locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    const scale = () => graph.locator('.react-flow__viewport').evaluate(el => Number((el as HTMLElement).style.transform.match(/scale\(([^)]+)/)?.[1]))
    await browserExpect.poll(scale).toBeGreaterThan(0)
    await graph.getByRole('button', { name: 'Focus selected' }).click()
    await browserExpect.poll(scale).toBeCloseTo(1, 2)
    await graph.getByRole('button', { name: 'Zoom in' }).click()
    await browserExpect.poll(scale).toBeGreaterThan(1)
    await graph.getByRole('button', { name: 'Zoom out' }).click()
    await browserExpect.poll(scale).toBeLessThan(1.05)
    await graph.getByRole('button', { name: 'Show all', exact: true }).click()
    await graph.evaluate((el: HTMLElement & { graph: any }) => { el.graph = structuredClone(el.graph); el.style.width = '346px' })
    await browserExpect.poll(() => graph.locator('.asset-lineage-node-selected').count()).toBe(0)
    const allVisible = () => graph.evaluate(el => {
      const flow = el.querySelector('.react-flow')!.getBoundingClientRect()
      return [...el.querySelectorAll('.react-flow__node')].every(node => {
        const r = node.getBoundingClientRect()
        return r.left >= flow.left && r.right <= flow.right && r.top >= flow.top && r.bottom <= flow.bottom
      })
    })
    await browserExpect.poll(allVisible).toBe(true)
    for (let i = 0; i < 4; i++) await graph.getByRole('button', { name: 'Zoom in' }).click()
    await browserExpect.poll(allVisible).toBe(false)
    await graph.getByRole('button', { name: 'Fit graph' }).click()
    await browserExpect.poll(allVisible).toBe(true)
  } finally { await page.close() }
}, 15_000)

test('viewport controls explain their actions and keep zoom anchored without changing the selected path', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const graph = page.locator('lv-asset-lineage-graph')
    await graph.locator('.asset-lineage-node').first().waitFor()
    await graph.evaluate((el: HTMLElement) => { el.style.width = '346px' })
    const controls = graph.getByRole('group', { name: 'Graph view' })
    const zoom = controls.getByRole('status', { name: 'Zoom level' })
    await browserExpect(zoom).not.toHaveText('100%')
    await browserExpect(controls.getByRole('button', { name: 'Fit graph', exact: true })).toBeDisabled()
    await controls.getByRole('button', { name: 'Actual size (100%)' }).click()
    await browserExpect(zoom).toHaveText('100%')
    const viewport = () => graph.evaluate(el => {
      const transform = (el.querySelector('.react-flow__viewport') as HTMLElement).style.transform
      const match = transform.match(/translate\(([-\d.]+)px, ([-\d.]+)px\) scale\(([-\d.]+)\)/)!
      const bounds = el.querySelector('.react-flow')!.getBoundingClientRect()
      const x = Number(match[1]), y = Number(match[2]), scale = Number(match[3])
      return { scale, centerX: (bounds.width / 2 - x) / scale, centerY: (bounds.height / 2 - y) / scale }
    })
    const before = await viewport()
    await controls.getByRole('button', { name: 'Zoom in', exact: true }).click()
    await browserExpect(zoom).toHaveText('125%')
    const after = await viewport()
    expect(after.centerX).toBeCloseTo(before.centerX, 1)
    expect(after.centerY).toBeCloseTo(before.centerY, 1)
    await graph.getByRole('combobox', { name: 'Find asset' }).selectOption('source')
    await browserExpect(zoom).toHaveText('125%')
    await graph.locator('.react-flow__renderer').dispatchEvent('click')
    await browserExpect(zoom).toHaveText('125%')
    await graph.getByRole('combobox', { name: 'Find asset' }).selectOption('dashboard')
    await browserExpect(zoom).toHaveText('125%')
    await controls.getByRole('button', { name: 'Zoom out', exact: true }).click()
    await browserExpect(zoom).toHaveText('100%')
    await controls.getByRole('button', { name: 'Zoom in', exact: true }).click({ clickCount: 3 })
    await browserExpect(zoom).toHaveText('200%')
    await browserExpect(controls.getByRole('button', { name: 'Zoom in', exact: true })).toBeDisabled()
    await browserExpect(controls.getByRole('button', { name: 'Fit graph', exact: true })).toBeEnabled()
    await controls.getByRole('button', { name: 'Fit graph', exact: true }).click()
    await browserExpect(controls.getByRole('button', { name: 'Fit graph', exact: true })).toBeDisabled()
    expect(await graph.locator('.asset-lineage-node-selected').count()).toBe(1)
    await graph.getByRole('button', { name: /^Upstream/ }).click()
    await browserExpect(graph.locator('.asset-lineage-node')).toHaveCount(2)
    await controls.getByRole('button', { name: 'Actual size (100%)' }).click()
    await browserExpect(graph.getByRole('button', { name: /^Upstream/ })).toHaveAttribute('aria-pressed', 'true')
  } finally { await page.close() }
}, 15_000)

test('expand opens the whole lineage explorer across the page and restores it on exit', async () => {
  const page = await browser.newPage({ viewport: { width: 1180, height: 760 } })
  try {
    await page.goto(baseURL)
    const graph = page.locator('lv-asset-lineage-graph')
    await graph.locator('.asset-lineage-node').first().waitFor()
    await graph.getByRole('button', { name: /^Upstream/ }).click()
    const initial = await graph.locator('.asset-lineage-root').boundingBox()
    const selected = await graph.getByRole('combobox', { name: 'Find asset' }).inputValue()
    await graph.getByRole('button', { name: 'Expand to full page' }).click()
    const expanded = graph.getByRole('dialog', { name: 'Full-page lineage explorer' })
    await browserExpect(expanded).toBeVisible()
    expect(await expanded.boundingBox()).toEqual({ x: 0, y: 0, width: 1180, height: 760 })
    await browserExpect(expanded.getByRole('combobox', { name: 'Find asset' })).toHaveValue(selected)
    await browserExpect(expanded.getByRole('button', { name: /^Upstream/ })).toHaveAttribute('aria-pressed', 'true')
    await expanded.getByRole('button', { name: 'Zoom in', exact: true }).click()
    await browserExpect(expanded.getByRole('status', { name: 'Zoom level' })).toHaveText('125%')
    await expanded.getByRole('button', { name: 'Zoom out', exact: true }).click()
    await browserExpect(expanded.getByRole('status', { name: 'Zoom level' })).toHaveText('100%')
    await expanded.getByRole('button', { name: 'Exit full page' }).press('Escape')
    await browserExpect(expanded).not.toBeVisible()
    await browserExpect(graph.getByRole('button', { name: 'Expand to full page' })).toBeFocused()
    expect(await graph.locator('.asset-lineage-root').boundingBox()).toEqual(initial)
    await browserExpect(graph.getByRole('combobox', { name: 'Find asset' })).toHaveValue(selected)
    await browserExpect(graph.getByRole('button', { name: /^Upstream/ })).toHaveAttribute('aria-pressed', 'true')
    expect(await page.evaluate(() => document.documentElement.style.overflow)).toBe('')
    await graph.getByRole('button', { name: 'Expand to full page' }).click()
    await expanded.getByRole('button', { name: 'Exit full page' }).click()
    await browserExpect(expanded).not.toBeVisible()
    await graph.getByRole('button', { name: 'Expand to full page' }).click()
    await graph.evaluate(element => element.remove())
    expect(await page.evaluate(() => document.documentElement.style.overflow)).toBe('')
    expect(await page.locator('dialog:modal').count()).toBe(0)
  } finally { await page.close() }
}, 15_000)

test('lineage graph reconnects without becoming blank', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const graph = page.locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    await graph.evaluate((element) => {
      const parent = element.parentNode!
      element.remove()
      parent.append(element)
    })
    await graph.locator('.react-flow__node').first().waitFor({ timeout: 3000 })
    expect(await graph.locator('.react-flow__node').count()).toBe(2)
  } finally { await page.close() }
})


test('focused scope includes direct dependencies and scope events remain separate from Fit', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}?focused=1`)
    const graph = page.locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    await graph.evaluate((el: HTMLElement & { graph: any; events?: any[] }) => {
      el.events = []
      el.addEventListener('lv-lineage-select', (event: Event) => el.events!.push(['select', (event as CustomEvent).detail]))
      el.addEventListener('lv-lineage-scope-change', (event: Event) => el.events!.push(['scope', (event as CustomEvent).detail]))
      el.graph = {
        nodes: [
          { id: 'a', label: 'Original source', kind: 'source' },
          { id: 'b', label: 'Direct model', kind: 'model' },
          { id: 'c', label: 'Selected report', kind: 'dashboard', selected: true },
        ],
        edges: [{ id: 'ab', source: 'a', target: 'b', kind: 'uses' }, { id: 'bc', source: 'b', target: 'c', kind: 'uses' }],
      }
    })
    await browserExpect(graph.locator('.react-flow__node')).toHaveCount(2)
    await browserExpect(graph.getByRole('status', { name: 'Zoom level' })).toHaveText('100%')
    await graph.getByRole('button', { name: 'Zoom in', exact: true }).click()
    await graph.getByRole('button', { name: 'Show all upstream', exact: true }).click()
    await browserExpect(graph.locator('.react-flow__node')).toHaveCount(3)
    await browserExpect(graph.getByRole('status', { name: 'Zoom level' })).toHaveText('125%')
    await graph.getByRole('button', { name: 'Fit graph', exact: true }).click()
    await browserExpect(graph.locator('.react-flow__node')).toHaveCount(3)
    expect(await graph.evaluate((el: any) => el.scope)).toBe('full')
    await graph.getByRole('button', { name: 'Show direct dependencies', exact: true }).click()
    await browserExpect(graph.locator('.react-flow__node')).toHaveCount(2)
    await graph.getByRole('combobox', { name: 'Find asset' }).selectOption('b')
    await browserExpect(graph.locator('.react-flow__node')).toHaveCount(3)
    expect(await graph.evaluate((el: any) => el.events)).toEqual([
      ['scope', { scope: 'full' }], ['scope', { scope: 'focused' }], ['select', { id: 'b' }],
    ])
    await graph.getByRole('button', { name: 'Show all', exact: true }).click()
    expect(await graph.evaluate((el: any) => el.scope)).toBe('full')
    await browserExpect(graph.locator('.asset-lineage-node-selected')).toHaveCount(0)
  } finally { await page.close() }
})

test('run graph preserves status overlays, run scope labels, custom dialog titles and focus containment', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}?focused=1`)
    const graph = page.locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    await graph.evaluate((el: any) => {
      el.scopeMode = 'run'
      el.dialogTitle = 'Pipeline run graph'
      el.graph = {
        nodes: [
          { id: 'a', label: 'Ingest', kind: 'source', runStatus: 'succeeded', runStatusLabel: 'Succeeded' },
          { id: 'b', label: 'Build', kind: 'model', selected: true, runStatus: 'running', runStatusLabel: 'Running now', runAnimate: true },
        ],
        edges: [{ id: 'ab', source: 'a', target: 'b', kind: 'uses' }],
      }
    })
    await browserExpect(graph.getByRole('button', { name: 'Model Build, Running now', exact: true })).toHaveClass(/asset-lineage-node-run-animated/)
    await browserExpect(graph.locator('.asset-lineage-node-run-succeeded .asset-lineage-node-run-status')).toHaveText('Succeeded')
    await graph.getByRole('button', { name: 'Show full run graph', exact: true }).click()
    await browserExpect(graph.getByRole('button', { name: 'Show focused path', exact: true })).toBeVisible()
    await graph.getByRole('button', { name: 'Expand to full page' }).click()
    const dialog = graph.getByRole('dialog', { name: 'Pipeline run graph' })
    await browserExpect(dialog).toBeVisible()
    for (let i = 0; i < 24; i++) {
      await page.keyboard.press('Tab')
      expect(await dialog.evaluate(el => el.matches(':focus-within'))).toBe(true)
    }
    await page.keyboard.press('Escape')
    await browserExpect(dialog).not.toBeVisible()
    await browserExpect(graph.getByRole('button', { name: 'Expand to full page' })).toBeFocused()
  } finally { await page.close() }
})

function testDocument(): string {
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          body {
            margin: 0;
            --base-text-weight-medium: 500;
            --base-text-weight-semibold: 600;
            --base-text-lineHeight-tight: 1.25;
            --base-text-lineHeight-normal: 1.5;
            --lv-type-caption: 400 12px/1.25 system-ui;
            --lv-type-body: 400 14px/1.5 system-ui;
            --lv-type-code-inline: 400 0.9285em ui-monospace;
            --lv-bg-app: #f6f8fa;
            --lv-bg-page: #f6f8fa;
            --lv-bg-panel: #fff;
            --lv-bg-panel-muted: #f6f8fa;
            --lv-fg-default: #24292f;
            --lv-fg-muted: #57606a;
            --lv-fg-link: #0969da;
            --lv-line-muted: #d8dee4;
            --lv-line-accent: #0969da;
            --lv-fg-on-emphasis: #fff;








            --base-size-2: 2px;
            --base-size-4: 4px;
            --base-size-6: 6px;
            --base-size-8: 8px;
            --base-size-12: 12px;
            --base-size-16: 16px;
            --borderWidth-thin: 1px;
            --borderWidth-default: 1px;
            --borderWidth-thicker: 2px;
            --borderRadius-default: 6px;
            --lv-border-default: 1px solid #d0d7de;
            --shadow-resting-small: none;
          }
        </style>
      </head>
      <body>
        <lineage-test-host></lineage-test-host>
        <script type="module" src="/asset-lineage-graph-under-test.js"></script>
        <script type="module">
          const graph = {
            nodes: [
              { id: 'source', label: 'orders', kind: 'source', meta: 'olist.orders', rank: -1 },
              { id: 'dashboard', label: 'Executive Sales Dashboard', kind: 'dashboard', meta: 'executive-sales', rank: 0, selected: true },
            ],
            edges: [{ id: 'source-dashboard', source: 'source', target: 'dashboard', kind: 'uses_source', label: 'Provides source' }],
          }
          const denseGraph = {
            nodes: [
              { id: 'connection', label: 'Finance connection', kind: 'connection', rank: -3 },
              { id: 'source', label: 'Finance source', kind: 'source', rank: -2 },
              ...Array.from({ length: 10 }, (_, index) => ({ id: \`model-\${index}\`, label: \`Finance model \${index + 1}\`, kind: 'model', rank: -1 })),
              { id: 'semantic', label: 'Finance semantic model', kind: 'semantic_model', rank: 0, selected: true },
              { id: 'dashboard', label: 'Finance dashboard', kind: 'dashboard', rank: 1 },
            ],
            edges: [],
          }
          const initialGraph = location.search.includes('dense=1') ? denseGraph : graph
          customElements.define('lineage-test-host', class extends HTMLElement {
            connectedCallback() {
              const root = this.attachShadow({ mode: 'open' })
              root.innerHTML = '<lv-asset-lineage-graph style="display:block;width:900px;height:420px"></lv-asset-lineage-graph>'
              const element = root.querySelector('lv-asset-lineage-graph')
              if (location.search.includes('dense=1')) element.style.width = '346px'
              element.scope = location.search.includes('focused=1') ? 'focused' : 'full'
              element.graph = initialGraph
            }
          })
        </script>
      </body>
    </html>
  `
}
