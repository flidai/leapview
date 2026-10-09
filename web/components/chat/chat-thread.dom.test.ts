import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let browser: Browser
let baseURL = ''

beforeAll(async () => {
  const root = join(process.cwd(), '.tmp/chat-thread-test')
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname !== '/') {
      const file = normalize(join(root, url.pathname))
      if (!file.startsWith(root)) {
        response.writeHead(404)
        response.end('not found')
        return
      }
      try {
        response.setHeader('content-type', 'text/javascript')
        response.end(await readFile(file))
        return
      } catch {
        response.writeHead(404)
        response.end('not found')
        return
      }
    }
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(`
      <!doctype html>
      <html>
        <head>
          <style>
            :root {
              --lv-chart-surface: rgb(1, 2, 3);
              --lv-border-default: 2px solid rgb(4, 5, 6);
              ${typographyTestTokens}
              --fontStack-monospace: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
	              --lv-bg-app: rgb(11, 12, 13);
              --lv-bg-page: #fff;
              --lv-bg-panel: #fff;
              --lv-bg-panel-muted: #f6f8fa;
              --lv-bg-control: #f6f8fa;
              --lv-fg-default: #24292f;
              --lv-fg-muted: #57606a;
              --lv-fg-accent: #0969da;
              --lv-line-muted: #d8dee4;
              --lv-border-width: 1px;
              --lv-border-muted: 1px solid #d8dee4;
              --lv-radius-default: 6px;
              --base-size-4: 4px;
              --base-size-8: 8px;
              --base-size-12: 12px;
              --base-size-16: 16px;
              --base-size-20: 20px;
              --lv-space-sm: 8px;









              --lv-chat-thread-padding: 16px;
              --lv-chat-stack-width: 760px;
              --lv-chat-stack-gap: 16px;
              --lv-chat-message-width: 760px;
              --lv-chat-message-gap: 8px;
              --lv-chat-agent-item-gap: 8px;
              --lv-chat-empty-min-height: 180px;
              --lv-chat-bubble-padding-block: 12px;
              --lv-chat-bubble-padding-inline: 16px;
              --lv-chat-markdown-block-gap: 10px;
              --lv-chat-markdown-list-indent: 20px;
              --lv-chat-markdown-list-item-gap: 2px;
              --lv-chat-code-radius: 4px;
              --lv-chat-code-padding-block: 1px;
              --lv-chat-code-padding-inline: 4px;
              --lv-chat-code-font-scale: 0.92em;
              --lv-chat-pre-padding-block: 9px;
              --lv-chat-pre-padding-inline: 10px;
              --lv-chat-quote-border-width: 2px;
              --lv-chat-link-underline-thickness: 1px;
              --lv-chat-link-underline-offset: 2px;
            }
          </style>
          <script type="module" src="/chat-under-test.js"></script>
        </head>
        <body><lv-chat-thread></lv-chat-thread><lv-visual-modal></lv-visual-modal></body>
      </html>
    `)
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

test('chat thread uses the surrounding app surface background', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.waitForFunction(() => customElements.get('lv-chat-thread'))

  const background = await page.locator('lv-chat-thread').evaluate((element: any) => {
    const thread = (element.shadowRoot as ShadowRoot).querySelector('.thread') as HTMLElement
    return getComputedStyle(thread).backgroundColor
  })

  expect(background).toBe('rgb(11, 12, 13)')
  await page.close()
})

test('narrow chat wraps messages and keeps long context and visual cards inside the panel', async () => {
  const page = await browser.newPage({ viewport: { width: 340, height: 800 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-thread'))
    const layout = await page.locator('lv-chat-thread').evaluate(async (element: any) => {
      element.style.height = '700px'
      element.style.setProperty('--lv-chat-stack-width', '100%')
      element.status = { enabled: true, running: false }
      element.transcript = [
        { id: 'user-1', kind: 'user', text: 'Review this dashboard', references: [{ reference: { kind: 'dashboard', id: 'finance' }, name: 'Actual, budget, and forecast revenue dashboard for the finance team', href: '/dashboards/finance', hierarchy: [], locations: [], context: [] }] },
        { id: 'tool-1', kind: 'tool', name: 'query_visual', status: 'complete', artifact: { id: 'revenue', type: 'combo', summary: 'Actual, budget, and forecast revenue for the finance team' } },
        { id: 'assistant-1', kind: 'assistant', text: 'The dashboard and this conversation are still available. Review revenue and cash together.' },
      ]
      element.conversationId = 'conversation-1'
      element.dashboardPreviewAvailable = true
      await element.updateComplete
      const scroll = element.shadowRoot.querySelector('.scroll') as HTMLElement
      const card = element.shadowRoot.querySelector('.visual-reference') as HTMLElement
      return { width: scroll.clientWidth, scrollWidth: scroll.scrollWidth, cardRight: card.getBoundingClientRect().right }
    })
    expect(layout.scrollWidth).toBeLessThanOrEqual(layout.width + 1)
    expect(layout.cardRight).toBeLessThanOrEqual(340)
    expect(await page.getByRole('link', { name: 'Open in Explorer' }).count()).toBe(0)
  } finally {
    await page.close()
  }
})

test('chat thread distinguishes unavailable, empty, and working states', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.transcript = []
    thread.status = { enabled: false, running: false, error: 'Agent is not configured.' }
    await thread.updateComplete
  })

  const unavailable = await page.locator('lv-chat-thread').evaluate((element: any) => ({
    title: element.shadowRoot.querySelector('.empty-title')?.textContent?.trim(),
    detail: element.shadowRoot.querySelector('.empty-detail')?.textContent?.trim(),
    hasAlert: Boolean(element.shadowRoot.querySelector('.alert')),
  }))
  expect(unavailable).toEqual({ title: 'Agent unavailable', detail: 'Agent is not configured.', hasAlert: false })

  await page.locator('lv-chat-thread').evaluate(async (thread: any) => {
    thread.status = { enabled: true, running: true }
    await thread.updateComplete
  })
  const working = await page.locator('lv-chat-thread').evaluate((element: any) => ({
    text: element.shadowRoot.querySelector('.working')?.textContent?.replace(/\s+/g, ' ').trim(),
    label: element.shadowRoot.querySelector('.working')?.getAttribute('aria-label'),
    hasEmpty: Boolean(element.shadowRoot.querySelector('.empty-state')),
  }))
  expect(working).toEqual({ text: '', label: 'Working', hasEmpty: false })
  await page.close()
})

test('chat thread preserves plain user message text without template whitespace', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.transcript = [{
      id: 'user-1',
      kind: 'user',
      text: '# nice!',
    }]
    await thread.updateComplete
  })

  const state = await page.locator('lv-chat-thread').evaluate((element: any) => {
    const bubble = (element.shadowRoot as ShadowRoot).querySelector('.message.user .bubble.plain') as HTMLElement
    const rect = bubble.getBoundingClientRect()
    return {
      text: bubble.textContent,
      width: Math.round(rect.width),
      whiteSpace: getComputedStyle(bubble).whiteSpace,
    }
  })

  expect(state.text).toBe('# nice!')
  expect(state.whiteSpace).toBe('pre-wrap')
  expect(state.width).toBeLessThan(140)
  await page.close()
})

test('chat thread renders turn-scoped references inside the user message bubble', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.transcript = [{
      id: 'user-1',
      kind: 'user',
      text: 'Why did revenue fall?',
      references: [{
        reference: { kind: 'visual', id: 'executive-sales.revenue' },
        name: 'Revenue by month',
        visualType: 'line',
        hierarchy: ['Sales', 'Executive Sales', 'Overview'],
        href: '/dashboards/executive-sales/pages/overview',
        locations: [],
        context: ['current_page'],
      }],
    }]
    await thread.updateComplete
  })

  const state = await page.locator('lv-chat-thread').evaluate((element: any) => {
    const bubble = (element.shadowRoot as ShadowRoot).querySelector('.message.user .bubble') as HTMLElement
    const reference = bubble.querySelector('.turn-reference') as HTMLAnchorElement
    return {
      bubbleText: bubble.textContent.replace(/\s+/g, ' ').trim(),
      referenceHref: reference.getAttribute('href'),
      referenceInsideBubble: bubble.contains(reference),
      referenceText: reference.textContent?.replace(/\s+/g, ' ').trim(),
      tooltip: reference.getAttribute('title'),
      accessibleName: reference.getAttribute('aria-label'),
      hasVisibleMetadata: Boolean(reference.querySelector('.turn-reference-hierarchy, .turn-reference-type')),
      iconClass: reference.querySelector('.turn-reference-icon svg')?.getAttribute('class'),
      iconColor: (reference.querySelector('.turn-reference-icon svg') as SVGElement).style.color,
    }
  })

  expect(state).toEqual({
    bubbleText: 'Revenue by month Why did revenue fall?',
    referenceHref: '/dashboards/executive-sales/pages/overview',
    referenceInsideBubble: true,
    referenceText: 'Revenue by month',
    tooltip: 'Revenue by month · Sales / Executive Sales / Overview · Visual',
    accessibleName: 'Revenue by month · Sales / Executive Sales / Overview · Visual',
    hasVisibleMetadata: false,
    iconClass: 'reference-icon-visual',
    iconColor: 'var(--lv-asset-visual-accent, var(--lv-fg-muted))',
  })
  await page.close()
})

test('chat thread uses the shared visual identity and color for references', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    const reference = (id: string, visualType: string) => ({
      reference: { kind: 'visual', id },
      name: id,
      visualType,
      hierarchy: ['Sales'],
      href: `/${id}`,
      locations: [],
      context: [],
    })
    thread.transcript = [{
      id: 'user-1',
      kind: 'user',
      text: 'Compare these',
      references: [reference('trend', 'line'), reference('revenue', 'kpi'), reference('orders', 'table')],
    }]
    await thread.updateComplete
  })

  const icons = await page.locator('lv-chat-thread').evaluate((element: any) => (
    Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('.turn-reference-icon svg'))
      .map((icon: any) => ({ className: icon.getAttribute('class'), color: icon.style.color }))
  ))
  expect(icons).toEqual([
    { className: 'reference-icon-visual', color: 'var(--lv-asset-visual-accent, var(--lv-fg-muted))' },
    { className: 'reference-icon-visual', color: 'var(--lv-asset-visual-accent, var(--lv-fg-muted))' },
    { className: 'reference-icon-visual', color: 'var(--lv-asset-visual-accent, var(--lv-fg-muted))' },
  ])
  await page.close()
})

test('chat thread renders visual artifacts with dashboard web components', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    await customElements.whenDefined('lv-visual-modal')
    const thread = document.querySelector('lv-chat-thread') as any
    const field = (id: string, role: string, dataType: string, label: string) => ({ id, role, dataType, nullable: false, label })
    thread.visuals = {
      agent_chart_1: {
        schemaVersion: 14, visualID: 'agent_chart_1', rendererID: 'echarts', specRevision: 'sha256:chat-chart', dataRevision: 1,
        spec: { kind: 'cartesian', mark: 'bar', title: 'Orders', datasets: [{ id: 'primary', fields: [field('label', 'dimension', 'string', 'Status'), field('value', 'metric', 'decimal', 'Orders')] }], dataBudget: { maxRows: 50, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Orders by status' }, interactions: [], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } },
        dataState: { kind: 'inline', specRevision: 'sha256:chat-chart', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:chat-chart', dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['delivered', 42]], completeness: 'complete' }] },
        selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
      },
      agent_table_1: {
        schemaVersion: 14, visualID: 'agent_table_1', rendererID: 'tanstack', specRevision: 'sha256:chat-table', dataRevision: 1,
        spec: { kind: 'table', title: 'Orders', datasets: [{ id: 'primary', fields: [field('order_id', 'identity', 'string', 'Order')] }], dataBudget: { maxRows: 50, requiredCompleteness: 'partial' }, accessibility: { title: 'Orders', description: 'Orders' }, interactions: [], columns: [{ field: { dataset: 'primary', field: 'order_id' }, label: 'Order', formatting: [] }], defaultSort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], presentation: { rowHeight: 34, striped: true, showHeader: true } },
        dataState: { kind: 'windowed', specRevision: 'sha256:chat-table', dataRevision: 1, generation: 1, schema: { id: 'primary', fields: [field('order_id', 'identity', 'string', 'Order')] }, cardinality: { kind: 'exact', count: 1 }, availableRows: 1, rowCap: 50, chunkSize: 50, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], blocks: { a: { id: 'a', start: 0, rows: [['o1']], requestSeq: 0, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }] } } },
        selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
      },
    }
    thread.transcript = [
      {
        id: 'tool-chart',
        kind: 'tool',
        name: 'query_visual',
        status: 'complete',
        argumentsJson: JSON.stringify({ semanticModelId: 'semantic:sales', visual: { type: 'bar', query: { type: 'aggregate', dimensions: ['country'], metrics: ['revenue'], limit: 25 }, presentation: { type: 'cartesian' } } }),
        resultJson: JSON.stringify({ ok: true, type: 'bar', id: 'agent_chart_1', datasetId: 'orders', semanticModelRef: { kind: 'semantic_model', id: 'semantic:sales' }, fields: [
          { fieldId: 'semantic:sales.country', role: 'dimension', alias: 'country', explorerFieldId: 'orders.country', label: 'Country' },
          { fieldId: 'semantic:sales.revenue', role: 'metric', alias: 'revenue', label: 'Revenue' },
        ] }),
        artifact: {
          type: 'bar',
          id: 'agent_chart_1',
          summary: 'Created chart.',
        },
      },
      {
        id: 'tool-table',
        kind: 'tool',
        name: 'query_visual',
        status: 'complete',
        resultJson: '{\n  "ok": true,\n  "type": "table",\n  "id": "agent_table_1",\n  "signal": "visuals.agent_table_1"\n}',
        artifact: {
          type: 'table',
          id: 'agent_table_1',
          summary: 'Created table.',
        },
      },
    ]
    thread.surface = 'drawer'
    await thread.updateComplete
  })
  await page.waitForFunction(() => Boolean(
    document.querySelector('lv-chat-thread')!
      .shadowRoot!
      .querySelector('.artifact-card[data-visual-id="agent_chart_1"]'),
  ))
  await page.locator('lv-visual-artifact[artifact-id="agent_table_1"]').scrollIntoViewIfNeeded()
  await page.waitForFunction(() => Boolean(
    document.querySelector('lv-chat-thread')!
      .shadowRoot!
      .querySelector('lv-visual-artifact[artifact-id="agent_table_1"]')
      ?.shadowRoot
      ?.querySelector<any>('lv-visualization-host')?.envelope?.spec?.kind === 'table',
  ))

  const rendered = await page.evaluate(() => {
    const thread = document.querySelector('lv-chat-thread')!
    const root = thread.shadowRoot!
    let explorerHref = ''
    thread.addEventListener('lv-chat-visual-open', (event: Event) => {
      explorerHref = (event as CustomEvent<{ explorerHref: string }>).detail.explorerHref
    }, { once: true })
    root.querySelector<HTMLButtonElement>('.artifact-card[data-visual-id="agent_chart_1"]')?.click()
    return {
      chartCard: root.querySelector('.artifact-card[data-visual-id="agent_chart_1"]')?.textContent?.includes('Orders'),
      table: (root.querySelector('lv-visual-artifact[artifact-id="agent_table_1"]')?.shadowRoot?.querySelector('lv-visualization-host') as any)?.envelope?.spec?.kind,
      explorerHref,
      toolRows: root.querySelectorAll('.tool-call').length,
      bodyText: root.textContent || '',
      artifactBackground: getComputedStyle(root.querySelector('lv-visual-artifact')!.shadowRoot!.querySelector('.artifact')!).backgroundColor,
      artifactBorderTopWidth: getComputedStyle(root.querySelector('lv-visual-artifact')!.shadowRoot!.querySelector('.artifact')!).borderTopWidth,
    }
  })
  expect(rendered.chartCard).toBe(true)
  expect(rendered.table).toBe('table')
  const explorerURL = new URL(rendered.explorerHref!, 'https://example.test')
  expect(explorerURL.pathname).toBe('/explore')
  expect(explorerURL.searchParams.get('mode')).toBe('explore')
  expect(explorerURL.searchParams.get('v')).toBe('2')
  expect([...explorerURL.searchParams.keys()].sort()).toEqual(['mode', 'state', 'v'])
  expect(JSON.parse(explorerURL.searchParams.get('state')!)).toEqual({
    schemaVersion: 1,
    modelId: 'semantic:sales',
    datasetId: 'orders',
    dimensions: [{ field: 'orders.country' }],
    metrics: [{ field: 'revenue' }],
    filters: [],
    sort: [],
    limit: 25,
  })
  expect(rendered.toolRows).toBe(0)
  expect(rendered.bodyText.includes('delivered')).toBe(false)
  expect(rendered.artifactBackground).toBe('rgb(1, 2, 3)')
  expect(rendered.artifactBorderTopWidth).toBe('2px')

  const drawer = await page.locator('lv-chat-thread').evaluate(async (thread: any) => {
    thread.surface = 'drawer'
    await thread.updateComplete
    return {
      toolRows: thread.shadowRoot.querySelectorAll('.tool-call').length,
      artifacts: thread.shadowRoot.querySelectorAll('lv-visual-artifact').length,
    }
  })
  expect(drawer).toEqual({ toolRows: 0, artifacts: 1 })

  await page.close()
})

test('chat thread explains unsupported queries and offers saved visuals separately from Explore', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.conversationId = 'conversation-1'
    thread.visuals = Object.fromEntries(['filtered', 'secondary', 'records', 'input-only', 'unqualified'].map((id) => [id, {
      schemaVersion: 14, visualID: id, rendererID: 'echarts', specRevision: `sha256:${id}`, dataRevision: 1,
      spec: { kind: 'cartesian', mark: 'bar', title: 'Orders', datasets: [{ id: 'primary', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Orders' }] }], dataBudget: { maxRows: 50, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Orders' }, interactions: [], x: { dataset: 'primary', field: 'value' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } },
      dataState: { kind: 'inline', specRevision: `sha256:${id}`, dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: `sha256:${id}`, dataRevision: 1, generation: 1, columns: ['value'], rows: [[42]], completeness: 'complete' }] },
      selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
    }]))
    const args = (extraVisual: Record<string, unknown> = {}, filters?: unknown[]) => JSON.stringify({
      semanticModelId: 'semantic:sales',
      ...(filters ? { filters } : {}),
      visual: { type: 'bar', query: { type: 'aggregate', dimensions: ['status'], metrics: ['orders.count'] }, ...extraVisual },
    })
    const result = (id: string, explorerFieldId?: string) => JSON.stringify({
      ok: true, type: 'bar', id, datasetId: 'orders', semanticModelRef: { kind: 'semantic_model', id: 'semantic:sales' },
      fields: [{ fieldId: 'semantic:sales.status', role: 'dimension', alias: 'status', ...(explorerFieldId ? { explorerFieldId } : {}), label: 'Status' }],
    })
    thread.transcript = [
      { id: 'filtered', kind: 'tool', name: 'query_visual', status: 'complete', argumentsJson: args({}, [{ id: 'status-filter', dimension: 'status', default: { type: 'relativePeriod', direction: 'previous', count: 1, unit: 'month' }, control: { type: 'singleSelect' } }]), resultJson: result('filtered', 'orders.status'), artifact: { type: 'bar', id: 'filtered', summary: 'Filtered chart.' } },
      { id: 'secondary', kind: 'tool', name: 'query_visual', status: 'complete', argumentsJson: args({ datasets: { comparison: { type: 'aggregate', dimensions: [], metrics: ['orders.count'] } } }), resultJson: result('secondary', 'orders.status'), artifact: { type: 'bar', id: 'secondary', summary: 'Secondary chart.' } },
      { id: 'records', kind: 'tool', name: 'query_visual', status: 'complete', argumentsJson: JSON.stringify({ semanticModelId: 'semantic:sales', visual: { type: 'bar', query: { type: 'records', dataset: 'orders', fields: ['status'] } } }), resultJson: result('records', 'orders.status'), artifact: { type: 'bar', id: 'records', summary: 'Record chart.' } },
      { id: 'input-only', kind: 'tool', name: 'query_visual', status: 'complete', inputJson: args(), resultJson: result('input-only', 'orders.status'), artifact: { type: 'bar', id: 'input-only', summary: 'Input only chart.' } },
      { id: 'unqualified', kind: 'tool', name: 'query_visual', status: 'complete', argumentsJson: args(), resultJson: result('unqualified'), artifact: { type: 'bar', id: 'unqualified', summary: 'Unqualified chart.' } },
    ]
    thread.surface = 'drawer'
    await thread.updateComplete
  })
  await page.waitForFunction(() => document.querySelector('lv-chat-thread')?.shadowRoot?.querySelectorAll('lv-visual-artifact').length === 5)
  const actions = await page.locator('lv-chat-thread').evaluate((thread: any) => Array.from(thread.shadowRoot.querySelectorAll('lv-visual-artifact')).map((artifact: any) => Boolean(artifact.shadowRoot.querySelector('lv-visualization-host')?.querySelector('[slot="agent-action"]'))))
  expect(actions).toEqual([false, false, false, false, false])
  const exploreLinks = await page.locator('lv-chat-thread').evaluate((thread: any) => Array.from(thread.shadowRoot.querySelectorAll('lv-visual-artifact')).map((artifact: any) => artifact.explorerHref))
  expect(exploreLinks).toEqual(['', '', '', '', ''])
  const auditLinks = await page.locator('lv-chat-thread').evaluate((thread: any) => Array.from(thread.shadowRoot.querySelectorAll('lv-visual-artifact')).map((artifact: any) => artifact.shadowRoot.querySelector('a')?.getAttribute('href')))
  expect(auditLinks).toEqual(['filtered', 'secondary', 'records', 'input-only', 'unqualified'].map(id => `/chats/conversation-1/visuals/${id}/explore`))
  await page.close()
})

test('chat thread hides processing rows while retaining finished errors and answers', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.status = { enabled: true, running: false }
    thread.transcript = [
      { id: 'user-1', kind: 'user', text: 'Find the sales dashboard.' },
      {
        id: 'tool-running', kind: 'tool', name: 'catalog_search', status: 'running',
        inputJson: '{"query":"sales"}', resultJson: 'items[1]{id}: sales',
      },
      {
        id: 'tool-error', kind: 'tool', name: 'catalog_get', status: 'error',
        inputJson: '{"id":"dashboard:sales"}', error: 'Catalog lookup failed.',
        resultJson: '{"error":"secret tool result"}',
      },
      {
        id: 'tool-complete', kind: 'tool', name: 'catalog_list', status: 'complete',
        resultJson: 'items[1]{id}: dashboard:sales',
      },
      { id: 'run-error', kind: 'error', text: 'The dashboard could not be loaded.' },
      { id: 'assistant-1', kind: 'assistant', markdown: 'I could not load that dashboard.' },
    ]
    await thread.updateComplete
  })

  const state = await page.locator('lv-chat-thread').evaluate((element: any) => {
    const root = element.shadowRoot
    const text = root.textContent?.replace(/\s+/g, ' ').trim() || ''
    return {
      text,
      working: root.querySelector('.working')?.textContent?.replace(/\s+/g, ' ').trim(),
      toolRows: root.querySelectorAll('.tool-call').length,
      codeBlocks: root.querySelectorAll('lv-code-block').length,
      agentTurns: root.querySelectorAll('.agent-turn').length,
      errors: Array.from(root.querySelectorAll('.message.error')).map((node: any) => node.textContent?.replace(/\s+/g, ' ').trim()),
      assistantMarkdown: (root.querySelector('.agent-markdown') as any)?.value,
      transcriptTools: element.transcript.filter((item: any) => item.kind === 'tool').map((item: any) => ({ name: item.name, inputJson: item.inputJson, resultJson: item.resultJson })),
    }
  })

  expect(state.working).toBeUndefined()
  expect(state.toolRows).toBe(0)
  expect(state.codeBlocks).toBe(0)
  expect(state.agentTurns).toBe(1)
  expect(state.errors).toEqual(['Catalog lookup failed.', 'The dashboard could not be loaded.'])
  expect(state.text).toContain('Find the sales dashboard.')
  expect(state.assistantMarkdown).toBe('I could not load that dashboard.')
  expect(state.text).not.toContain('secret tool result')
  expect(state.transcriptTools).toEqual([
    { name: 'catalog_search', inputJson: '{"query":"sales"}', resultJson: 'items[1]{id}: sales' },
    { name: 'catalog_get', inputJson: '{"id":"dashboard:sales"}', resultJson: '{"error":"secret tool result"}' },
    { name: 'catalog_list', inputJson: undefined, resultJson: 'items[1]{id}: dashboard:sales' },
  ])
  await page.close()
})

test('chat thread expands completed steps with an elapsed label while keeping answers visible', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  const thread = page.locator('lv-chat-thread')
  await thread.evaluate(async (element: any) => {
    element.status = { enabled: true, running: false }
    element.transcript = [
      { id: 'u1', kind: 'user', text: 'Show revenue', createdAt: '2026-09-28T10:00:00Z' },
      { id: 't1', kind: 'tool', name: 'catalog_search', status: 'complete', createdAt: '2026-09-28T10:00:02Z', inputJson: '{"secret":"hidden"}' },
      { id: 'a1', kind: 'assistant', markdown: 'Looking for the model.', createdAt: '2026-09-28T10:00:03Z' },
      { id: 't2', kind: 'tool', name: 'query_visual', status: 'complete', createdAt: '2026-09-28T10:00:07Z' },
      { id: 'a2', kind: 'assistant', markdown: 'Here is the chart.', createdAt: '2026-09-28T10:00:11Z' },
    ]
    await element.updateComplete
  })
  const details = thread.locator('.run-steps')
  expect(await details.count()).toBe(1)
  expect(await details.getAttribute('open')).toBeNull()
  expect(await details.locator('summary').textContent()).toContain('Worked for 11s')
  expect(await thread.locator('.agent-markdown').evaluate((node: any) => node.value)).toBe('Here is the chart.')
  await details.locator('summary').click()
  expect(await details.getAttribute('open')).not.toBeNull()
  const steps = await details.locator('.run-step').allTextContents()
  expect(steps).toEqual(['Looking for the model.'])
  expect((await details.textContent()) || '').not.toContain('secret')
  await page.close()
})

test('visual replies keep a summary and every chart visible while details stay collapsed', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  const thread = page.locator('lv-chat-thread')
  await thread.evaluate(async (element: any) => {
    element.status = { enabled: true, running: false }
    element.transcript = [
      { id: 'u1', kind: 'user', text: 'Show revenue by state', createdAt: '2026-09-28T10:00:00Z' },
      { id: 't1', kind: 'tool', name: 'query_visual', status: 'complete', createdAt: '2026-09-28T10:00:02Z', artifact: { type: 'bar', id: 'revenue-by-state', summary: 'Revenue by State' } },
      { id: 't2', kind: 'tool', name: 'query_visual', status: 'complete', artifact: { type: 'pie', id: 'revenue-mix', summary: 'Revenue mix' } },
      { id: 'a1', kind: 'assistant', markdown: 'Created revenue by state and revenue mix.\n\n| State | Revenue |\n| --- | ---: |\n| SP | 5,998,226.96 |\n\nData freshness: current.', createdAt: '2026-09-28T10:00:14Z' },
    ]
    await element.updateComplete
  })
  const details = thread.locator('.run-steps')
  expect(await details.locator('summary').textContent()).toContain('Worked for 14s')
  expect(await thread.locator('lv-visual-artifact').count()).toBe(2)
  for (const card of await thread.locator('lv-visual-artifact').all()) expect(await card.isVisible()).toBe(true)
  expect(await page.getByText('Created revenue by state and revenue mix.', {exact:true}).first().isVisible()).toBe(true)
  expect(await details.locator('.agent-markdown').isVisible()).toBe(false)
  expect(await details.locator('.agent-markdown').count()).toBe(1)
  await details.locator('summary').click()
  expect(await thread.locator('.dashboard-summary').isVisible()).toBe(false)
  expect(await details.locator('.agent-markdown').isVisible()).toBe(true)
  expect(await details.locator('.agent-markdown').evaluate((node: any) => node.value)).toContain('Data freshness: current.')
  await page.close()
})

test('chat thread hides recovered tool errors only within the same user turn', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  const errors = await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.status = { enabled: true, running: false }
    thread.transcript = [
      { id: 'u1', kind: 'user', text: 'Show revenue' },
      { id: 'e1', kind: 'tool', name: 'catalog_get', status: 'error', argumentsJson: '{"ref":"dashboard:a"}', error: 'Recovered lookup.' },
      { id: 's1', kind: 'tool', name: 'catalog_get', status: 'complete', argumentsJson: '{"ref":"dashboard:a"}' },
      { id: 'e2', kind: 'tool', name: 'query_visual', status: 'error', error: 'Unresolved visual error.' },
      { id: 'u2', kind: 'user', text: 'Try again' },
      { id: 's2', kind: 'tool', name: 'query_visual', status: 'complete' },
      { id: 'e3', kind: 'tool', name: 'catalog_search', status: 'error', error: 'Current lookup error.' },
    ]
    await thread.updateComplete
    return Array.from(thread.shadowRoot.querySelectorAll('.message.error')).map((node: any) => node.textContent?.trim())
  })
  expect(errors).toEqual(['Unresolved visual error.', 'Current lookup error.'])
  await page.close()
})

test('chat thread keeps an error when a different request to the same tool succeeds', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  const errors = await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.status = { enabled: true, running: false }
    thread.transcript = [
      { id: 'user', kind: 'user', text: 'Compare both dashboards' },
      { id: 'failed', kind: 'tool', name: 'catalog_get', status: 'error', argumentsJson: '{"ref":{"id":"dashboard:a"}}', error: 'Dashboard A is unavailable.' },
      { id: 'other', kind: 'tool', name: 'catalog_get', status: 'complete', argumentsJson: '{"ref":{"id":"dashboard:b"}}' },
    ]
    await thread.updateComplete
    const afterOtherResult = Array.from(thread.shadowRoot.querySelectorAll('.message.error')).map((node: any) => node.textContent?.trim())
    thread.transcript = [...thread.transcript,
      { id: 'retry', kind: 'tool', name: 'catalog_get', status: 'complete', argumentsJson: '{"ref":{"id":"dashboard:a"}}' },
    ]
    await thread.updateComplete
    return {
      afterOtherResult,
      afterMatchingRetry: Array.from(thread.shadowRoot.querySelectorAll('.message.error')).map((node: any) => node.textContent?.trim()),
    }
  })
  expect(errors).toEqual({ afterOtherResult: ['Dashboard A is unavailable.'], afterMatchingRetry: [] })
  await page.close()
})

test('chat thread keeps a repaired visual presentation error inside Worked', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  const state = await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    const query = { type: 'aggregate', metrics: [{ metric: 'net_sales' }], dimensions: [{ dimension: 'country' }] }
    thread.status = { enabled: true, running: false }
    thread.transcript = [
      { id: 'user', kind: 'user', text: 'Show net sales by country' },
      { id: 'failed', kind: 'tool', name: 'query_visual', status: 'error', error: 'Unsupported orientation.', argumentsJson: JSON.stringify({ semanticModelId: 'semantic-model:finance', visual: { type: 'bar', query, presentation: { orientation: 'horizontal' } } }) },
      { id: 'fixed', kind: 'tool', name: 'query_visual', status: 'complete', artifact: { type: 'bar', id: 'by-country', summary: 'Net sales by country' }, argumentsJson: JSON.stringify({ semanticModelId: 'semantic-model:finance', visual: { type: 'bar', query } }) },
    ]
    await thread.updateComplete
    return {
      visibleErrors: thread.shadowRoot.querySelectorAll('.message.error').length,
      steps: thread.shadowRoot.querySelector('.run-step-list')?.textContent?.replace(/\s+/g, ' ').trim(),
      visual: Boolean(thread.shadowRoot.querySelector('lv-visual-artifact')),
    }
  })
  expect(state).toEqual({ visibleErrors: 0, steps: undefined, visual: true })
  await page.close()
})

test('chat thread does not treat different visual filters or datasets as retries', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const errors = await page.evaluate(async () => {
      await customElements.whenDefined('lv-chat-thread')
      const thread = document.querySelector('lv-chat-thread') as any
      const query = { type: 'aggregate', metrics: [{ metric: 'net_sales' }], dimensions: [{ dimension: 'country' }] }
      const visual = { type: 'bar', query }
      const input = (extra: Record<string, unknown>, visualExtra: Record<string, unknown> = {}) => JSON.stringify({ semanticModelId: 'semantic-model:finance', ...extra, visual: { ...visual, ...visualExtra } })
      thread.status = { enabled: true, running: false }
      thread.transcript = [
        { id: 'user', kind: 'user', text: 'Compare revenue charts' },
        { id: 'filtered-error', kind: 'tool', name: 'query_visual', status: 'error', error: 'Filtered chart failed.', argumentsJson: input({ filters: [{ id: 'region', dimension: 'region', default: { type: 'text', value: 'west' } }] }) },
        { id: 'filtered-success', kind: 'tool', name: 'query_visual', status: 'complete', argumentsJson: input({ filters: [{ id: 'region', dimension: 'region', default: { type: 'text', value: 'east' } }] }) },
        { id: 'dataset-error', kind: 'tool', name: 'query_visual', status: 'error', error: 'Comparison dataset failed.', argumentsJson: input({}, { datasets: { comparison: { type: 'aggregate', metrics: ['gross_sales'] } } }) },
        { id: 'dataset-success', kind: 'tool', name: 'query_visual', status: 'complete', argumentsJson: input({}, { datasets: { comparison: { type: 'aggregate', metrics: ['net_sales'] } } }) },
      ]
      await thread.updateComplete
      return Array.from(thread.shadowRoot.querySelectorAll('.message.error')).map((node: any) => node.textContent?.trim())
    })
    expect(errors).toEqual(['Filtered chart failed.', 'Comparison dataset failed.'])
  } finally {
    await page.close()
  }
})

test('chat thread hides failed tool lookups once the turn produces a visual', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  const state = await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.status = { enabled: true, running: false }
    thread.transcript = [
      { id: 'user', kind: 'user', text: 'Show net sales by country' },
      { id: 'failed', kind: 'tool', name: 'catalog_get', status: 'error', error: 'Catalog resource not found.', argumentsJson: '{"ref":"unknown"}' },
      { id: 'found', kind: 'tool', name: 'catalog_get', status: 'complete', argumentsJson: '{"ref":"finance"}' },
      { id: 'visual', kind: 'tool', name: 'query_visual', status: 'complete', artifact: { type: 'donut', id: 'by-country', summary: 'Net sales by country' } },
      { id: 'answer', kind: 'assistant', markdown: 'Visual' },
    ]
    await thread.updateComplete
    return {
      topLevelErrors: thread.shadowRoot.querySelectorAll('.agent-stack > .message.error').length,
      workedErrors: Array.from(thread.shadowRoot.querySelectorAll('.run-steps .message.error')).map((node: any) => node.textContent?.trim()),
      numberedSteps: thread.shadowRoot.querySelectorAll('.run-steps ol').length,
      toolLabels: thread.shadowRoot.querySelector('.run-steps')?.textContent?.includes('Catalog Get'),
      visual: Boolean(thread.shadowRoot.querySelector('lv-visual-artifact')),
    }
  })
  expect(state).toEqual({ topLevelErrors: 0, workedErrors: [], numberedSteps: 0, toolLabels: undefined, visual: true })
  await page.close()
})

test('chat thread waits until the active run ends before showing unresolved errors', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  const states = await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    const transcript = [
      { id: 'u1', kind: 'user', text: 'Earlier question', runId: 'run-old' },
      { id: 'old-error', kind: 'tool', name: 'catalog_get', status: 'error', error: 'Earlier failure.', runId: 'run-old' },
      { id: 'u2', kind: 'user', text: 'Show revenue', runId: 'run-new' },
      { id: 'progress', kind: 'assistant', markdown: 'Retrying the visual.', runId: 'run-new' },
      { id: 'new-error', kind: 'tool', name: 'query_visual', status: 'error', argumentsJson: '{"semanticModelId":"orders"}', error: 'Temporary visual failure.', runId: 'run-new' },
    ]
    const visible = () => ({
      errors: Array.from(thread.shadowRoot.querySelectorAll('.message.error')).map((node: any) => node.textContent?.trim()),
      answers: Array.from(thread.shadowRoot.querySelectorAll('.agent-markdown')).map((node: any) => node.value),
      stepsLabel: (Array.from(thread.shadowRoot.querySelectorAll('.run-steps summary')) as HTMLElement[]).at(-1)?.textContent?.trim(),
      stepsText: (Array.from(thread.shadowRoot.querySelectorAll('.run-step-list')) as HTMLElement[]).at(-1)?.textContent?.replace(/\s+/g, ' ').trim(),
    })
    thread.status = { enabled: true, running: true, runId: 'run-new' }
    thread.transcript = transcript
    await thread.updateComplete
    const running = visible()
    thread.status = { enabled: true, running: false, runId: 'run-new' }
    await thread.updateComplete
    const failed = visible()
    thread.transcript = [
      ...transcript,
      { id: 'success', kind: 'tool', name: 'query_visual', status: 'complete', argumentsJson: '{"semanticModelId":"orders"}', runId: 'run-new' },
      { id: 'final', kind: 'assistant', markdown: 'Open the chart.', runId: 'run-new' },
    ]
    await thread.updateComplete
    return { running, failed, recovered: visible() }
  })
  expect(states).toEqual({
    running: { errors: ['Earlier failure.'], answers: [], stepsLabel: 'Working', stepsText: 'Retrying the visual.' },
    failed: { errors: ['Earlier failure.', 'Temporary visual failure.'], answers: ['Retrying the visual.'], stepsLabel: undefined, stepsText: undefined },
    recovered: { errors: ['Earlier failure.'], answers: ['Open the chart.'], stepsLabel: 'View steps', stepsText: 'Retrying the visual.' },
  })
  await page.close()
})

test('chat thread keeps only the last assistant message in each user turn', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  const answers = await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.status = { enabled: true, running: false }
    thread.transcript = [
      { id: 'user', kind: 'user', text: 'Show revenue' },
      { id: 'progress-1', kind: 'assistant', markdown: 'I will query the model.' },
      { id: 'tool-error', kind: 'tool', name: 'query_visual', status: 'error', argumentsJson: '{"semanticModelId":"orders"}', error: 'Invalid option.' },
      { id: 'progress-2', kind: 'assistant', markdown: 'Retrying without the option.' },
      { id: 'tool-success', kind: 'tool', name: 'query_visual', status: 'complete', argumentsJson: '{"semanticModelId":"orders"}' },
      { id: 'answer-1', kind: 'assistant', markdown: 'The chart is ready.' },
      { id: 'final', kind: 'assistant', markdown: 'Open the Revenue chart.' },
      { id: 'user-2', kind: 'user', text: 'What is the total?' },
      { id: 'answer-2', kind: 'assistant', markdown: 'The total is 42.' },
    ]
    await thread.updateComplete
    return {
      answers: Array.from(thread.shadowRoot.querySelectorAll('.agent-markdown')).map((node: any) => node.value),
      errors: thread.shadowRoot.querySelectorAll('.message.error').length,
    }
  })
  expect(answers).toEqual({ answers: ['Open the Revenue chart.', 'The total is 42.'], errors: 0 })
  await page.close()
})

test('drawer shows only working state until the active run finishes', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.locator('lv-chat-thread').evaluate(async (thread: any) => {
      thread.surface = 'drawer'
      thread.status = { enabled: true, running: true }
      thread.transcript = [
        { id: 'user', kind: 'user', text: 'Add a chart.' },
        { id: 'running', kind: 'tool', name: 'catalog_search', status: 'running' },
        { id: 'complete', kind: 'tool', name: 'catalog_get', status: 'complete' },
        { id: 'failed', kind: 'tool', name: 'edit_dashboard_source', status: 'error', error: 'Could not edit dashboard.' },
        { id: 'answer', kind: 'assistant', markdown: 'I could not add the chart.' },
      ]
      await thread.updateComplete
    })
    const state = await page.locator('lv-chat-thread').evaluate((thread: any) => ({
      toolRows: thread.shadowRoot.querySelectorAll('.tool-call').length,
      working: Boolean(thread.shadowRoot.querySelector('.working')),
      error: thread.shadowRoot.querySelector('.message.error')?.textContent?.trim(),
      answer: thread.shadowRoot.querySelector('.agent-markdown')?.value,
      user: thread.shadowRoot.querySelector('.message.user')?.textContent?.trim(),
    }))
    expect(state.toolRows).toBe(0)
    expect(state.working).toBe(true)
    expect(state.error).toBeUndefined()
    expect(state.answer).toBeUndefined()
    expect(state.user).toContain('Add a chart.')
  } finally {
    await page.close()
  }
})

test('tool failure remains accessible without its processing row', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.locator('lv-chat-thread').evaluate(async (thread: any) => {
      thread.status = { enabled: true, running: false }
      thread.transcript = [{ id: 'failed-tool', kind: 'tool', name: 'catalog_get', status: 'error', resultJson: '{"ok":false}', error: 'Network timed out.' }]
      await thread.updateComplete
    })
    const thread = page.locator('lv-chat-thread')
    expect(await thread.locator('.tool-call').count()).toBe(0)
    expect((await thread.getByRole('alert').textContent())?.trim()).toBe('Network timed out.')
  } finally {
    await page.close()
  }
})

test('chat thread keeps structured tool history when durable history replaces live activity', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.transcript = [
      { id: 'tool-running', toolCallId: 'call-1', kind: 'tool', name: 'catalog_search', status: 'running', inputJson: '{"query":"sales"}' },
      { id: 'tool-complete', toolCallId: 'call-2', kind: 'tool', name: 'catalog_list', status: 'complete', resultJson: 'items[1]{id}: sales' },
    ]
    await thread.updateComplete
    thread.transcript = [
      { id: 'tool-running', toolCallId: 'call-1', kind: 'tool', name: 'catalog_search', status: 'complete', inputJson: '{"query":"sales"}', resultJson: 'items[1]{id}: sales' },
      { id: 'tool-complete', toolCallId: 'call-2', kind: 'tool', name: 'catalog_list', status: 'complete', resultJson: 'items[1]{id}: sales' },
    ]
    await thread.updateComplete
  })
  const state = await page.locator('lv-chat-thread').evaluate((element: any) => {
    const root = element.shadowRoot as ShadowRoot
    return {
      rows: root.querySelectorAll('.tool-call').length,
      transcript: element.transcript.map((item: any) => ({ name: item.name, status: item.status, resultJson: item.resultJson })),
      stepsOpen: root.querySelector('.run-steps')?.hasAttribute('open'),
      stepText: root.querySelector('.run-step-list')?.textContent,
    }
  })
  expect(state.rows).toBe(0)
  expect(state.transcript).toEqual([
    { name: 'catalog_search', status: 'complete', resultJson: 'items[1]{id}: sales' },
    { name: 'catalog_list', status: 'complete', resultJson: 'items[1]{id}: sales' },
  ])
  expect(state.stepsOpen).toBe(false)
  expect(state.stepText).toBeUndefined()
  expect(await page.locator('.run-steps a').getAttribute('href')).toBe('/search?q=sales&limit=10')
  await page.close()
})

test('chat thread hides orphaned historical tools while keeping the new run indicator', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.status = { enabled: true, running: true, runId: 'run-new' }
    thread.transcript = [
      { id: 'tool-old', runId: 'run-old', toolCallId: 'call-old', kind: 'tool', name: 'catalog_search', status: 'running' },
    ]
    await thread.updateComplete
  })
  const state = await page.locator('lv-chat-thread').evaluate((element: any) => {
    const root = element.shadowRoot as ShadowRoot
    return {
      rows: root.querySelectorAll('.tool-call').length,
      working: root.querySelector('.working')?.textContent?.replace(/\s+/g, ' ').trim(),
    }
  })
  expect(state).toEqual({ rows: 0, working: '' })
  await page.close()
})

test('chat thread renders assistant markdown through shared markdown view', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    await customElements.whenDefined('lv-markdown-view')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.transcript = [{
      id: 'assistant-1',
      kind: 'assistant',
      markdown: [
        '# Assistant heading',
        '',
        'A paragraph with **strong** text and `code`.',
        '',
        '- One',
        '- Two',
      ].join('\n'),
    }]
    await thread.updateComplete
  })

  const state = await page.locator('lv-chat-thread').evaluate(async (element: any) => {
    const markdownView = (element.shadowRoot as ShadowRoot).querySelector('lv-markdown-view') as any
    await markdownView.updateComplete
    return {
      hasMarkdownView: Boolean(markdownView),
      value: markdownView.value,
      h1Text: (markdownView.shadowRoot as ShadowRoot).querySelector('h1')?.textContent,
      hasStrong: Boolean((markdownView.shadowRoot as ShadowRoot).querySelector('strong')),
      hasCode: Boolean((markdownView.shadowRoot as ShadowRoot).querySelector('code')),
      hasList: Boolean((markdownView.shadowRoot as ShadowRoot).querySelector('ul')),
    }
  })

  expect(state.hasMarkdownView).toBe(true)
  expect(state.value).toMatch(/^# Assistant heading/)
  expect(state.h1Text).toBe('Assistant heading')
  expect(state.hasStrong).toBe(true)
  expect(state.hasCode).toBe(true)
  expect(state.hasList).toBe(true)
  await page.close()
})

test('chat thread rejects payloads embedded in artifact metadata', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.transcript = [{
      id: 'tool-chart',
      kind: 'tool',
      name: 'query_visual',
      status: 'complete',
      artifact: {
        type: 'bar',
        id: 'legacy_chart_1',
        patch: {
          visuals: {
            legacy_chart_1: {
              version: 3,
              id: 'legacy_chart_1',
              shape: 'category_value',
              renderer: 'echarts',
              type: 'bar',
              title: 'Legacy Orders',
              unit: '',
              interaction: {},
              dimensions: ['status'],
              metric: 'order_count',
              metrics: ['order_count'],
              series: [],
              options: {},
              rendererOptions: {},
              selection: [],
              data: [{ label: 'delivered', value: 42 }],
            },
          },
        },
      },
    }]
    thread.surface = 'drawer'
    await thread.updateComplete
  })
  const artifact = page.locator('lv-chat-thread').locator('lv-visual-artifact[artifact-id="legacy_chart_1"]')
  await artifact.waitFor()
  const state = await artifact.evaluate((element) => ({
    hasChart: Boolean((element.shadowRoot as ShadowRoot)?.querySelector('lv-echart')),
    text: (element.shadowRoot as ShadowRoot)?.textContent?.trim(),
  }))
  expect(state.hasChart).toBe(false)
  expect(state.text).toBe('Artifact data is unavailable.')
  await page.close()
})

test('message actions copy exact text and prepare edits without redundant ask again', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.evaluate(() => {
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (text: string) => { (window as any).copied = text } } })
      const thread = document.querySelector('lv-chat-thread') as any
      thread.status = { enabled: true, running: false }
      thread.transcript = [{ id: 'u1', kind: 'user', text: 'Explain revenue', references: [] }, { id: 'a1', kind: 'assistant', markdown: '**Revenue** is sales.', status: 'complete' }]
      ;(window as any).reused = []
      ;(window as any).submitted = 0
      document.addEventListener('lv-chat-reuse', (event: Event) => (window as any).reused.push((event as CustomEvent).detail))
      document.addEventListener('lv-chat-submit', () => (window as any).submitted++)
    })
    await page.getByRole('group', { name: 'Answer actions' }).getByRole('button', { name: 'Copy message' }).click()
    expect(await page.evaluate(() => (window as any).copied)).toBe('**Revenue** is sales.')
    await page.locator('.message.user').hover()
    await page.getByRole('button', { name: 'Edit message' }).click()
    expect(await page.getByRole('button', { name: 'Ask again', exact: true }).count()).toBe(0)
    expect(await page.evaluate(() => (window as any).reused)).toEqual([{ text: 'Explain revenue', references: [], editMessageId: 'u1' }])
    expect(await page.evaluate(() => (window as any).submitted)).toBe(0)
    await page.evaluate(() => { (document.querySelector('lv-chat-thread') as any).status = { enabled: true, running: true } })
    expect(await page.getByRole('button', { name: 'Edit message' }).isDisabled()).toBe(true)
  } finally { await page.close() }
})

test('message edit action ignores a transcript item without a persisted ID', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.evaluate(async () => {
      const thread = document.querySelector('lv-chat-thread') as any
      thread.status = { enabled: true, running: false }
      thread.transcript = [{ id: '', kind: 'user', text: 'Unpersisted prompt', references: [] }]
      ;(window as any).reuseCount = 0
      document.addEventListener('lv-chat-reuse', () => (window as any).reuseCount++)
      await thread.updateComplete
    })
    await page.locator('.message.user').hover()
    await page.getByRole('button', { name: 'Edit message' }).click()
    expect(await page.evaluate(() => (window as any).reuseCount)).toBe(0)
  } finally { await page.close() }
})


test('dashboard replies keep a short summary and reveal the full explanation on demand', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-thread'))
    const answer = 'Created your dashboard on the selected page. One requested metric is unavailable.\n\n## Visuals\n\n- Revenue by month\n- Revenue by country\n\n## Filters\n\nCountry and reporting period are available.'
    await page.locator('lv-chat-thread').evaluate(async (e: any, answer: string) => {
      Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async (text: string) => { (window as any).copied = text } } })
      e.conversationId = 'conversation'
      e.status = { enabled: true, running: false }
      e.transcript = [
        { id: 'user', kind: 'user', text: 'Create a dashboard' },
        { id: 'retry', kind: 'tool', name: 'edit_dashboard_source', status: 'error', error: 'Earlier draft needed correction.' },
        { id: 'preview', kind: 'tool', name: 'preview_dashboard_draft', toolCallId: 'preview', runId: 'run', status: 'complete' },
        { id: 'answer', kind: 'assistant', markdown: answer },
      ]
      await e.updateComplete
    }, answer)
    const thread = page.locator('lv-chat-thread')
    for (const surface of ['page', 'drawer']) {
      await thread.evaluate(async (e: any, surface: string) => { e.surface = surface; await e.updateComplete }, surface)
      const summary = page.getByText('Created your dashboard on the selected page. One requested metric is unavailable.', { exact: true })
      expect(await summary.first().isVisible()).toBe(true)
      expect(await summary.last().isVisible()).toBe(false)
      expect(await page.getByText('Revenue by country', { exact: true }).isVisible()).toBe(false)
      expect(await page.getByText('Earlier draft needed correction.', { exact: true }).isVisible()).toBe(false)
      expect(await page.getByRole('link', { name: 'Open in Builder' }).count()).toBe(1)
      const disclosure = thread.locator('.run-steps')
      await disclosure.locator('summary').click()
      expect(await page.getByText('Revenue by country', { exact: true }).isVisible()).toBe(true)
      expect(await page.getByText('Earlier draft needed correction.', { exact: true }).isVisible()).toBe(true)
      expect(await page.getByText('Country and reporting period are available.', { exact: true }).isVisible()).toBe(true)
      expect(await summary.first().isVisible()).toBe(false)
      expect(await summary.last().isVisible()).toBe(true)
      await disclosure.locator('summary').click()
    }
    expect(await thread.locator('.run-steps summary').textContent()).toContain('View details')
    await page.getByRole('group', { name: 'Answer actions' }).getByRole('button', { name: 'Copy message' }).click()
    expect(await page.evaluate(() => (window as any).copied)).toBe(answer)
    await thread.evaluate(async (e: any) => {
      e.transcript = e.transcript.filter((item: any) => item.kind !== 'tool')
      await e.updateComplete
    })
    expect(await thread.locator('.run-steps').count()).toBe(0)
    expect(await page.getByText('Revenue by country', { exact: true }).isVisible()).toBe(true)
    await thread.evaluate(async (e: any) => {
      e.dashboardPreviewAvailable = true
      e.transcript = [
        { id: 'user', kind: 'user', text: 'Create a dashboard' },
        { id: 'visual', kind: 'tool', name: 'query_visual', status: 'complete', artifact: { id: 'revenue', type: 'bar', summary: 'Revenue' } },
        { id: 'preview', kind: 'tool', name: 'preview_dashboard_draft', toolCallId: 'preview', status: 'complete' },
        { id: 'answer', kind: 'assistant', text: 'Dashboard ready on the selected page.' },
      ]
      await e.updateComplete
    })
    expect(await page.getByText('Dashboard ready on the selected page.', { exact: true }).isVisible()).toBe(true)
    expect(await thread.locator('.run-steps').count()).toBe(0)
  } finally { await page.close() }
})

test('a preview action appears once while other dashboard destinations remain in steps', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-thread'))
    await page.locator('lv-chat-thread').evaluate(async (e: any) => {
      e.conversationId = 'conversation'
      e.status = { enabled: true, running: false }
      e.transcript = [
        { id: 'user', kind: 'user', text: 'Build the dashboard' },
        { id: 'source', kind: 'tool', name: 'get_dashboard_draft', toolCallId: 'source', runId: 'run', status: 'complete' },
        { id: 'preview', kind: 'tool', name: 'preview_dashboard_draft', toolCallId: 'preview', runId: 'run', status: 'complete' },
        { id: 'answer', kind: 'assistant', text: 'Dashboard ready.' },
      ]
      await e.updateComplete
    })
    const thread = page.locator('lv-chat-thread')
    expect(await thread.locator('a[href*="/actions/preview/open"]').count()).toBe(1)
    expect(await thread.locator('.run-steps a[href*="/actions/source/open"]').count()).toBe(1)
    expect(await thread.locator('.agent-stack > .tool-actions a[href*="/actions/preview/open"]').count()).toBe(1)
    await thread.evaluate(async (e: any) => {
      e.transcript = e.transcript.filter((item: any) => item.id !== 'source')
      await e.updateComplete
    })
    expect(await thread.locator('.run-steps').count()).toBe(0)
    expect(await thread.locator('a[href*="/actions/preview/open"]').count()).toBe(1)
  } finally { await page.close() }
})

test('builder chat opens each dashboard visual individually without duplicate add actions', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-thread'))
    await page.locator('lv-chat-thread').evaluate(async (e: any) => {
      e.dashboardPreviewAvailable = true
      e.transcript = [{id:'t',kind:'tool',name:'query_visual',status:'complete',artifact:{id:'chart-one',type:'bar',summary:'Revenue'}}]
      e.dashboardVisualIds = ['chart-one']
      e.addEventListener('lv-chat-dashboard-preview', (event: CustomEvent) => { (window as any).selectedVisual = event.detail.artifactId })
      await e.updateComplete
    })
    expect(await page.getByRole('button',{name:'Add to dashboard',exact:true}).count()).toBe(0)
    await page.getByRole('button',{name:'Open Revenue in visuals sidebar',exact:true}).click()
    expect(await page.evaluate(() => (window as any).selectedVisual)).toBe('chart-one')
  } finally {await page.close()}
})

test('chat keeps retries in collapsed activity while showing the final answer', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.evaluate(async () => {
      await customElements.whenDefined('lv-chat-thread')
      const thread = document.querySelector('lv-chat-thread') as any
      thread.status = { enabled: true, running: false }
      thread.transcript = [
        {id:'u',kind:'user',text:'Create a dashboard'},
        {id:'t1',kind:'tool',name:'query_semantic_model',argumentsJson:'{"modelId":"sales"}',status:'error',error:'Unknown field'},
        {id:'t2',kind:'tool',name:'query_semantic_model',argumentsJson:'{"modelId":"sales"}',status:'complete'},
        {id:'a',kind:'assistant',text:'Your dashboard is ready.'},
      ]
      await thread.updateComplete
    })
    expect(await page.getByRole('button', { name: /Query Semantic Model/ }).count()).toBe(0)
    expect(await page.getByText('Your dashboard is ready.', { exact: true }).isVisible()).toBe(true)
    expect(await page.getByRole('alert').count()).toBe(0)
    await page.locator('lv-chat-thread').evaluate(async (e: any) => {
      e.transcript = e.transcript.filter((item: any) => item.kind !== 'assistant')
      await e.updateComplete
    })
    expect(await page.getByText('This request stopped before a final answer was ready. You can ask the agent to continue.', {exact:true}).isVisible()).toBe(true)
    await page.locator('lv-chat-thread').evaluate(async (e: any) => {
      e.status = { enabled: true, running: false, error: 'Unable to finish this request.' }
      await e.updateComplete
    })
    expect(await page.getByRole('alert').getByText('Unable to finish this request.', {exact:true}).isVisible()).toBe(true)
  } finally { await page.close() }
})


test('catalog search receipts retain their exact destination once in reply details', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-thread'))
    await page.locator('lv-chat-thread').evaluate(async (thread: any) => {
      thread.status = { enabled: true, running: false }
      thread.transcript = [{ id: 'search', kind: 'tool', name: 'catalog_search', status: 'complete',
        argumentsJson: JSON.stringify({ query: 'sales', kinds: ['dashboard', 'model'], domain: 'finance', cursor: 'page/2', limit: 7 }),
      }]
      await thread.updateComplete
    })
    const thread = page.locator('lv-chat-thread')
    const link = thread.getByRole('link', { name: 'Open in Search', exact: true, includeHidden: true })
    expect(await link.count()).toBe(1)
    await thread.locator('.run-steps summary').click()
    expect(await link.isVisible()).toBe(true)
    const destination = new URL((await link.getAttribute('href'))!, baseURL)
    expect(destination.pathname).toBe('/search')
    expect(destination.searchParams.getAll('kind')).toEqual(['dashboard', 'model'])
    expect(Object.fromEntries(['q', 'domain', 'cursor', 'limit'].map(key => [key, destination.searchParams.get(key)]))).toEqual({ q: 'sales', domain: 'finance', cursor: 'page/2', limit: '7' })
  } finally { await page.close() }
})
