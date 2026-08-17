import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/semantic-model-graph-test')

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

for (const viewport of [
  { name: 'desktop', width: 1180, height: 760 },
  { name: 'mobile', width: 390, height: 720 },
]) {
  test(`semantic model graph renders table joins on ${viewport.name}`, async () => {
    const page = await browser.newPage({ viewport })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-semantic-model-graph'))
      await page.waitForFunction(() => document.querySelectorAll('lv-semantic-model-graph .react-flow__node').length >= 2)
      await page.waitForFunction(() => document.querySelectorAll('lv-semantic-model-graph .react-flow__edge').length >= 1)

      const state = await page.evaluate(() => {
        const graph = document.querySelector('lv-semantic-model-graph') as HTMLElement
        const flow = graph.querySelector('.react-flow') as HTMLElement
        const nodes = Array.from(graph.querySelectorAll('.react-flow__node')) as HTMLElement[]
        const edges = Array.from(graph.querySelectorAll('.react-flow__edge')) as HTMLElement[]
        const labels = Array.from(graph.querySelectorAll('.semantic-model-edge-label')).map((label) => label.textContent?.trim())
        const endpointLabels = Array.from(graph.querySelectorAll('.semantic-model-edge-endpoint')).map((label) => label.textContent?.trim())
        const nodeTexts = nodes.map((node) => node.textContent ?? '')
        const headerTexts = Array.from(graph.querySelectorAll('.semantic-model-node-header')).map((node) => node.textContent?.trim())
        const badges = Array.from(graph.querySelectorAll('.semantic-model-node-badge')).map((badge) => badge.textContent?.trim())
        const joinRow = graph.querySelector<HTMLElement>('.semantic-model-field-join')
        const grainName = graph.querySelector<HTMLElement>('.semantic-model-field-grain .semantic-model-field-name')
        const grainMarker = graph.querySelector<HTMLElement>('.semantic-model-field-grain-marker')
        const entityBadges = Array.from(graph.querySelectorAll<HTMLElement>('.semantic-model-node-entities'))
        const resetButton = graph.querySelector('.semantic-model-reset-button') as HTMLButtonElement | null
        const flowRect = flow.getBoundingClientRect()
        const nodeRects = nodes.map((node) => node.getBoundingClientRect())
        const overlap = nodeRects.length >= 2 && !(
          nodeRects[0].right <= nodeRects[1].left
          || nodeRects[1].right <= nodeRects[0].left
          || nodeRects[0].bottom <= nodeRects[1].top
          || nodeRects[1].bottom <= nodeRects[0].top
        )
        return {
          nodeCount: nodes.length,
          edgeCount: edges.length,
          labels,
          endpointLabels,
          hasOrders: nodeTexts.some((text) => text.includes('orders')),
          hasCustomers: nodeTexts.some((text) => text.includes('customers')),
          hasJoinLabel: nodeTexts.some((text) => text.includes('Join')),
          hasJoinRow: Boolean(graph.querySelector('.semantic-model-field-join')),
          hasGrainMarker: nodeTexts.some((text) => text.includes('G')),
          grainText: grainName?.textContent?.trim(),
          grainTitle: grainName?.getAttribute('title'),
          grainFontWeight: grainName ? getComputedStyle(grainName).fontWeight : '',
          grainMarkerText: grainMarker?.textContent?.trim(),
          grainMarkerTitle: grainMarker?.getAttribute('title'),
          entityTitles: entityBadges.map((badge) => badge.getAttribute('title')),
          hasBadgeElement: Boolean(graph.querySelector('.semantic-model-node-badge')),
          badges,
          joinRowBackground: joinRow ? getComputedStyle(joinRow).backgroundColor : '',
          joinRowBoxShadow: joinRow ? getComputedStyle(joinRow).boxShadow : '',
          hasTypeIcon: Boolean(graph.querySelector('.semantic-model-type-icon')),
          headerTexts,
          hasReset: Boolean(resetButton),
          resetText: resetButton?.textContent?.trim(),
          resetLabel: resetButton?.getAttribute('aria-label'),
          resetTitle: resetButton?.getAttribute('title'),
          hasResetIcon: Boolean(resetButton?.querySelector('.semantic-model-reset-icon')),
          flowHeight: Math.round(flowRect.height),
          nodesInsideFlow: nodeRects.every((rect) => rect.width > 0 && rect.height > 0 && rect.top >= flowRect.top && rect.bottom <= flowRect.bottom),
          overlap,
        }
      })

      expect(state.nodeCount).toBe(2)
      expect(state.edgeCount).toBe(1)
      expect(state.labels).toContain('*:1')
      expect(state.endpointLabels).toEqual(['*', '1'])
      expect(state.hasOrders).toBe(true)
      expect(state.hasCustomers).toBe(true)
      expect(state.hasJoinLabel).toBe(false)
      expect(state.hasJoinRow).toBe(true)
      expect(state.hasGrainMarker).toBe(true)
      expect(state.grainText).toBe('order_id')
      expect(state.grainTitle).toBe('order_id (grain: order); entities: order')
      expect(Number(state.grainFontWeight)).toBeGreaterThanOrEqual(600)
      expect(state.grainMarkerText).toBe('G')
      expect(state.grainMarkerTitle).toBe('Grain field for order')
      expect(state.entityTitles).toContain('Entities: order (primary) [order_id]; customer (foreign) [customer_id]')
      expect(state.hasBadgeElement).toBe(true)
      expect(state.badges).toEqual(['dataset', '2 metrics', 'grain: order', '2 entities', 'grain: customer', '1 entity'])
      expect(state.joinRowBackground).not.toBe('')
      expect(state.joinRowBoxShadow).toContain('inset')
      expect(state.hasTypeIcon).toBe(true)
      expect(state.headerTexts).toEqual(['orders', 'customers'])
      expect(state.hasReset).toBe(true)
      expect(state.resetText).toBe('')
      expect(state.resetLabel).toBe('Reset layout')
      expect(state.resetTitle).toBe('Reset layout')
      expect(state.hasResetIcon).toBe(true)
      expect(state.flowHeight).toBe(460)
      expect(state.nodesInsideFlow).toBe(true)
      expect(state.overlap).toBe(false)
    } finally {
      await page.close()
    }
  })
}

test('semantic model graph persists dragged node layout and resets it', async () => {
  const page = await browser.newPage({ viewport: { width: 1180, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => document.querySelectorAll('lv-semantic-model-graph .react-flow__node').length >= 2)

    const node = page.locator('lv-semantic-model-graph .react-flow__node').filter({ hasText: 'orders' })
    const nodeCount = await node.count()
    expect(nodeCount).toBe(1)
    const before = await node.boundingBox()
    if (!before) throw new Error('orders node has no bounding box')
    await page.mouse.move(before.x + before.width / 2, before.y + 18)
    await page.mouse.down()
    await page.mouse.move(before.x + before.width / 2 + 90, before.y + 58, { steps: 8 })
    await page.mouse.up()
    await page.waitForFunction(() => localStorage.length > 0)

    const after = await node.boundingBox()
    if (!after) throw new Error('orders node has no bounding box after drag')
    const customers = page.locator('lv-semantic-model-graph .react-flow__node').filter({ hasText: 'customers' })
    const customersCount = await customers.count()
    expect(customersCount).toBe(1)
    await customers.click()
    const afterSelect = await node.boundingBox()
    if (!afterSelect) throw new Error('orders node has no bounding box after selection')
    const persisted = await page.evaluate(() => {
      const keys = Array.from({ length: localStorage.length }, (_, index) => localStorage.key(index) ?? '')
      const key = keys.find((candidate) => candidate.startsWith('leapview:semantic-model-graph:v2:'))
      return {
        keyFound: Boolean(key),
        value: key ? localStorage.getItem(key) ?? '' : '',
      }
    })
    expect(Math.round(after.x)).not.toBe(Math.round(before.x))
    expect(Math.round(afterSelect.x)).toBe(Math.round(after.x))
    expect(Math.round(afterSelect.y)).toBe(Math.round(after.y))
    expect(persisted.keyFound).toBe(true)
    expect(persisted.value).toContain('orders')

    await page.locator('lv-semantic-model-graph .semantic-model-reset-button').click()
    const remaining = await page.evaluate(() => Array.from({ length: localStorage.length }, (_, index) => localStorage.key(index) ?? '').filter((key) => key.startsWith('leapview:semantic-model-graph:v2:')).length)
    expect(remaining).toBe(0)
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
            --lv-asset-model-table-bg: #ddf4ff;
            --lv-asset-model-table-border: #b6e3ff;





            --base-size-4: 4px;
            --base-size-6: 6px;
            --base-size-8: 8px;
            --base-size-10: 10px;
            --base-size-12: 12px;
            --base-size-16: 16px;
            --borderWidth-default: 1px;
            --borderRadius-default: 6px;
            --lv-border-default: 1px solid #d0d7de;
            --lv-border-muted: 1px solid #d8dee4;
            --lv-radius-full: 999px;
          }
        </style>
      </head>
      <body>
        <lv-semantic-model-graph storagekey="test:semantic_model:olist" style="display:block;width:min(980px,100vw);height:460px"></lv-semantic-model-graph>
        <script type="module" src="/semantic-model-graph-under-test.js"></script>
        <script type="module">
          const graph = {
            datasets: ['orders'],
            nodes: [
              {
                id: 'orders',
                title: 'orders',
                grainEntity: 'order',
                entities: [
                  { name: 'order', type: 'primary', fields: ['order_id'], grain: true },
                  { name: 'customer', type: 'foreign', fields: ['customer_id'] },
                ],
                badges: ['dataset', '2 metrics'],
                fields: [
                  { name: 'order_id', label: 'Order ID', type: 'VARCHAR', grain: true, entities: ['order'] },
                  { name: 'customer_id', label: 'Customer ID', type: 'VARCHAR', entities: ['customer'], join: true, relationships: ['orders_customers'] },
                  { name: 'state', label: 'State', type: 'VARCHAR' },
                ],
              },
              {
                id: 'customers',
                title: 'customers',
                grainEntity: 'customer',
                entities: [{ name: 'customer', type: 'primary', fields: ['customer_id'], grain: true }],
                fields: [
                  { name: 'customer_id', label: 'Customer ID', type: 'VARCHAR', grain: true, entities: ['customer'], join: true, relationships: ['orders_customers'] },
                  { name: 'segment', label: 'Segment', type: 'VARCHAR' },
                ],
              },
            ],
            edges: [
              {
                id: 'orders_customers',
                source: 'orders',
                target: 'customers',
                sourceField: 'customer_id',
                targetField: 'customer_id',
                cardinality: 'many_to_one',
                label: '*:1',
              },
            ],
          }
          document.querySelector('lv-semantic-model-graph').graph = graph
        </script>
      </body>
    </html>
  `
}
