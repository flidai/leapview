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
      const controlButton = graph.querySelector('.react-flow__controls-button') as HTMLElement
      const controlIcon = controlButton.querySelector('svg') as SVGElement
      const flowRect = flow.getBoundingClientRect()
      const nodeRect = node.getBoundingClientRect()
      const controlRect = controlButton.getBoundingClientRect()
      const controlIconRect = controlIcon.getBoundingClientRect()
      return {
        flowHeight: Math.round(flowRect.height),
        viewportPosition: getComputedStyle(viewport).position,
        viewportMatchesFlowWidth: Math.round(Number.parseFloat(getComputedStyle(viewport).width)) === Math.round(flowRect.width),
        edgePosition: getComputedStyle(edge).position,
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
      flowHeight: 420,
      viewportPosition: 'absolute',
      viewportMatchesFlowWidth: true,
      edgePosition: 'absolute',
      nodePosition: 'absolute',
      nodeInsideFlow: true,
      controlDisplay: 'flex',
      controlWidth: 26,
      controlHeight: 26,
      controlIconWidth: 12,
      controlIconFill: 'rgb(36, 41, 47)',
    })
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
          customElements.define('lineage-test-host', class extends HTMLElement {
            connectedCallback() {
              const root = this.attachShadow({ mode: 'open' })
              root.innerHTML = '<lv-asset-lineage-graph style="display:block;width:900px;height:420px"></lv-asset-lineage-graph>'
              root.querySelector('lv-asset-lineage-graph').graph = graph
            }
          })
        </script>
      </body>
    </html>
  `
}
