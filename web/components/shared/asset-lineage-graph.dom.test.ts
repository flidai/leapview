import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
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
      const controlButton = graph.querySelector('.react-flow__controls-button') as HTMLElement
      const controlIcon = controlButton.querySelector('svg') as SVGElement
      const flowRect = flow.getBoundingClientRect()
      const nodeRect = node.getBoundingClientRect()
      const controlRect = controlButton.getBoundingClientRect()
      const controlIconRect = controlIcon.getBoundingClientRect()
      return {
        flowHeight: Math.round(flowRect.height),
        flowWidth: Math.round(flowRect.width),
        graphActions: Array.from(graph.querySelectorAll('.asset-lineage-actions button')).map((button) => button.textContent?.trim()),
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
        controlIconWidth: Math.round(controlIconRect.width),
        controlIconFill: getComputedStyle(controlIcon).fill,
      }
    })

    expect(state).toEqual({
      flowHeight: expect.any(Number),
      flowWidth: 900,
      graphActions: ['Show all upstream', 'Fit', 'Expand graph'],
      inspectorPanels: 0,
      viewportPosition: 'absolute',
      viewportMatchesFlowWidth: true,
      edgePosition: 'absolute',
      edgeRouting: expect.stringContaining('react-flow__edge-smoothstep'),
      nodePosition: 'absolute',
      nodeInsideFlow: true,
      controlDisplay: 'flex',
      controlWidth: 26,
      controlHeight: 26,
      controlIconWidth: 12,
      controlIconFill: 'rgb(36, 41, 47)',
    })
    expect(state.flowHeight).toBeLessThan(420)
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
    expect(await edge.getAttribute('class')).toContain('react-flow__edge-default')
  } finally {
    await page.close()
  }
})

test('asset lineage keeps the selected node readable on a dense mobile fit', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 720 } })
  try {
    await page.goto(`${baseURL}?dense=1`)
    const graph = page.locator('lineage-test-host').locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    await page.waitForFunction(() => {
      const graph = document.querySelector('lineage-test-host')?.shadowRoot?.querySelector('lv-asset-lineage-graph')
      return graph?.querySelectorAll('.react-flow__node').length === 1
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
        renderedTitlePixels: Number.parseFloat(getComputedStyle(selected.querySelector('.asset-lineage-node-title')!).fontSize) * Number(match?.[1]),
        selectedVisible: selectedRect.top >= flowRect.top
          && selectedRect.bottom <= flowRect.bottom
          && selectedRect.left < flowRect.right
          && selectedRect.right > flowRect.left,
        focusedNodeCount: nodeRects.length,
        titleWhiteSpace: getComputedStyle(selected.querySelector('.asset-lineage-node-title')!).whiteSpace,
        titleTextOverflow: getComputedStyle(selected.querySelector('.asset-lineage-node-title')!).textOverflow,
      }
    })
    expect(state.scale).toBeGreaterThanOrEqual(0.85)
    expect(state.renderedTitlePixels).toBeGreaterThanOrEqual(12)
    expect(state.selectedVisible).toBe(true)
    expect(state.focusedNodeCount).toBe(1)
    expect(state.titleWhiteSpace).toBe('normal')
    expect(state.titleTextOverflow).toBe('clip')
  } finally {
    await page.close()
  }
})

test('scope controls graph inclusion separately from Fit, and Expand opens a full-width workspace', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 720 } })
  try {
    await page.goto(baseURL)
    const graph = page.locator('lineage-test-host').locator('lv-asset-lineage-graph')
    await graph.locator('.react-flow__node').first().waitFor()
    await graph.evaluate((element: HTMLElement & { graph: any }) => {
      element.style.width = '346px'
      element.graph = {
        nodes: [
          { id: 'source-upstream', label: 'Earlier source outside the focus', kind: 'source', rank: -2 },
          { id: 'source-direct', label: 'Direct source dependency with a long complete title', kind: 'source', rank: -1 },
          { id: 'model-selected', label: 'Selected model with a long complete title', kind: 'model', rank: 0, selected: true },
        ],
        edges: [
          { id: 'earlier-direct', source: 'source-upstream', target: 'source-direct', kind: 'uses_source' },
          { id: 'direct-selected', source: 'source-direct', target: 'model-selected', kind: 'uses_source' },
        ],
      }
    })
    await graph.locator('.asset-lineage-node-selected').filter({ hasText: 'Selected model with a long complete title' }).waitFor()
    const state = await graph.evaluate((element) => {
      const flow = element.querySelector('.react-flow') as HTMLElement
      const selected = element.querySelector('.asset-lineage-node-selected') as HTMLElement
      const direct = Array.from(element.querySelectorAll<HTMLElement>('.react-flow__node')).find((node) => node.textContent?.includes('Direct source dependency'))!
      const indirect = Array.from(element.querySelectorAll<HTMLElement>('.react-flow__node')).find((node) => node.textContent?.includes('Earlier source'))
      const match = (element.querySelector('.react-flow__viewport') as HTMLElement).style.transform.match(/scale\(([-\d.]+)\)/)
      const visible = (node: HTMLElement) => {
        const rect = node.getBoundingClientRect()
        const bounds = flow.getBoundingClientRect()
        return rect.top >= bounds.top && rect.bottom <= bounds.bottom && rect.left < bounds.right && rect.right > bounds.left
      }
      return {
        zoom: Number(match?.[1]),
        nodeCount: element.querySelectorAll('.react-flow__node').length,
        selectedVisible: visible(selected),
        directVisible: visible(direct),
        indirectIncluded: indirect !== undefined,
        edgeIDs: Array.from(element.querySelectorAll<HTMLElement>('.react-flow__edge')).map((edge) => edge.getAttribute('data-id')),
        completeTitle: selected.querySelector('.asset-lineage-node-title')?.textContent?.trim(),
        clippedTitle: getComputedStyle(selected.querySelector('.asset-lineage-node-title')!).textOverflow,
      }
    })
    expect(state.zoom).toBeGreaterThanOrEqual(0.85)
    expect(state.nodeCount).toBe(2)
    expect(state.selectedVisible).toBe(true)
    expect(state.directVisible).toBe(true)
    expect(state.indirectIncluded).toBe(false)
    expect(state.edgeIDs).toEqual(['direct-selected'])
    expect(state.completeTitle).toBe('Selected model with a long complete title')
    expect(state.clippedTitle).toBe('clip')

    const scopeToggle = graph.getByRole('button', { name: 'Show all upstream' })
    await scopeToggle.click()
    await graph.locator('.react-flow__node').filter({ hasText: 'Earlier source' }).waitFor()
    expect(await graph.locator('.react-flow__node').count()).toBe(3)
    expect(await graph.locator('.react-flow__edge').count()).toBe(2)
    expect(await graph.locator('.asset-lineage-node-selected').textContent()).toContain('Selected model')
    await page.waitForFunction(() => {
      const graph = document.querySelector('lineage-test-host')?.shadowRoot?.querySelector('lv-asset-lineage-graph')
      const flow = graph?.querySelector('.react-flow')
      const selected = graph?.querySelector('.asset-lineage-node-selected')
      if (!flow || !selected) return false
      const flowBounds = flow.getBoundingClientRect()
      const selectedBounds = selected.getBoundingClientRect()
      return selectedBounds.top >= flowBounds.top && selectedBounds.bottom <= flowBounds.bottom
        && selectedBounds.left >= flowBounds.left && selectedBounds.right <= flowBounds.right
    })
    const fullScopeView = await graph.evaluate((element) => {
      const flow = element.querySelector('.react-flow') as HTMLElement
      const selected = element.querySelector('.asset-lineage-node-selected') as HTMLElement
      const scale = Number((element.querySelector('.react-flow__viewport') as HTMLElement).style.transform.match(/scale\(([-\d.]+)\)/)?.[1])
      const selectedBounds = selected.getBoundingClientRect()
        const flowBounds = flow.getBoundingClientRect()
        return {
          scale,
          selectedVisible: selectedBounds.top >= flowBounds.top && selectedBounds.bottom <= flowBounds.bottom
          && selectedBounds.left >= flowBounds.left && selectedBounds.right <= flowBounds.right,
      }
    })
    expect(fullScopeView.selectedVisible).toBe(true)
    expect(fullScopeView.scale).toBeCloseTo(state.zoom, 2)

    await graph.getByRole('button', { name: 'Fit', exact: true }).click()
    expect(await graph.locator('.react-flow__node').count()).toBe(3)
    expect(await graph.getByRole('button', { name: 'Show direct dependencies' }).count()).toBe(1)
    await graph.getByRole('button', { name: 'Show direct dependencies' }).click()
    expect(await graph.locator('.react-flow__node').count()).toBe(2)

    const expand = graph.getByRole('button', { name: 'Expand graph' })
    const before = await graph.evaluate((element) => element.getBoundingClientRect().width)
    await expand.click()
    const close = graph.getByRole('button', { name: 'Close graph' })
    await close.waitFor()
    const expanded = await graph.evaluate((element) => ({
      width: element.querySelector('dialog')!.getBoundingClientRect().width,
      viewportWidth: document.documentElement.clientWidth,
      position: getComputedStyle(element.querySelector('dialog')!).position,
      modal: element.querySelector('dialog')!.matches(':modal'),
      label: element.querySelector('dialog')!.getAttribute('aria-label'),
    }))
    expect(expanded.width).toBeGreaterThanOrEqual(expanded.viewportWidth - 2)
    expect(expanded.position).toBe('fixed')
    expect(expanded.modal).toBe(true)
    expect(expanded.label).toBe('Expanded dependency graph')
    expect((await graph.locator('.asset-lineage-dialog-title').textContent())?.trim()).toBe('Expanded dependency graph')
    expect(await graph.locator('.react-flow__node').count()).toBe(2)
    expect(await graph.locator('.asset-lineage-node-selected').textContent()).toContain('Selected model')
    await page.waitForFunction(() => {
      const graph = document.querySelector('lineage-test-host')?.shadowRoot?.querySelector('lv-asset-lineage-graph')
      const flow = graph?.querySelector('.react-flow')
      const selected = graph?.querySelector('.asset-lineage-node-selected')
      if (!flow || !selected) return false
      const flowBounds = flow.getBoundingClientRect()
      const selectedBounds = selected.getBoundingClientRect()
      const center = (selectedBounds.left + selectedBounds.right) / 2
      const fraction = (center - flowBounds.left) / flowBounds.width
      return fraction >= 0.4 && fraction <= 0.6
    })
    const dialog = graph.locator('dialog')
    await page.waitForFunction(() => {
      const dialog = document.querySelector('lineage-test-host')?.shadowRoot?.querySelector('lv-asset-lineage-graph dialog')
      return Boolean(dialog?.matches(':focus-within'))
    })
    for (let index = 0; index < 24; index++) {
      await page.keyboard.press('Tab')
      expect(await dialog.evaluate((element) => element.matches(':focus-within'))).toBe(true)
    }
    await page.keyboard.press('Escape')
    await graph.getByRole('button', { name: 'Expand graph' }).waitFor()
    expect(await graph.getByRole('button', { name: 'Expand graph' }).evaluate((element) => element.matches(':focus'))).toBe(true)
    expect(await graph.locator('.asset-lineage-node-selected').textContent()).toContain('Selected model')
    await graph.getByRole('button', { name: 'Expand graph' }).click()
    await graph.getByRole('button', { name: 'Close graph' }).click()
    await graph.getByRole('button', { name: 'Expand graph' }).waitFor()
  } finally {
    await page.close()
  }
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
            --lv-type-body-compact: 400 14px/1.4 system-ui;
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
              element.graph = initialGraph
            }
          })
        </script>
      </body>
    </html>
  `
}
