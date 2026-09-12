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
    hasEmpty: Boolean(element.shadowRoot.querySelector('.empty-state')),
  }))
  expect(working).toEqual({ text: 'Working', hasEmpty: false })
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
        schemaVersion: 4, visualID: 'agent_chart_1', rendererID: 'echarts', specRevision: 'sha256:chat-chart', dataRevision: 1,
        spec: { kind: 'cartesian', mark: 'bar', title: 'Orders', datasets: [{ id: 'primary', fields: [field('label', 'dimension', 'string', 'Status'), field('value', 'metric', 'decimal', 'Orders')] }], dataBudget: { maxRows: 50, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Orders by status' }, interactions: [], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } },
        dataState: { kind: 'inline', specRevision: 'sha256:chat-chart', dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: 'sha256:chat-chart', dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['delivered', 42]], completeness: 'complete' }] },
        selection: [], status: { kind: 'ready' }, diagnostics: [],
      },
      agent_table_1: {
        schemaVersion: 4, visualID: 'agent_table_1', rendererID: 'tanstack', specRevision: 'sha256:chat-table', dataRevision: 1,
        spec: { kind: 'table', title: 'Orders', datasets: [{ id: 'primary', fields: [field('order_id', 'identity', 'string', 'Order')] }], dataBudget: { maxRows: 50, requiredCompleteness: 'partial' }, accessibility: { title: 'Orders', description: 'Orders' }, interactions: [], columns: [{ field: { dataset: 'primary', field: 'order_id' }, label: 'Order' }], defaultSort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], presentation: { rowHeight: 34, striped: true, showHeader: true } },
        dataState: { kind: 'windowed', specRevision: 'sha256:chat-table', dataRevision: 1, generation: 1, schema: { id: 'primary', fields: [field('order_id', 'identity', 'string', 'Order')] }, cardinality: { kind: 'exact', count: 1 }, availableRows: 1, rowCap: 50, chunkSize: 50, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }], blocks: { a: { id: 'a', start: 0, rows: [['o1']], requestSeq: 0, resetVersion: 0, sort: [{ field: { dataset: 'primary', field: 'order_id' }, direction: 'ascending' }] } } },
        selection: [], status: { kind: 'ready' }, diagnostics: [],
      },
    }
    thread.transcript = [
      {
        id: 'tool-chart',
        kind: 'tool',
        name: 'query_visual',
        status: 'complete',
        resultJson: '{\n  "ok": true,\n  "type": "bar",\n  "id": "agent_chart_1",\n  "signal": "visuals.agent_chart_1"\n}',
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
    await thread.updateComplete
  })
  await page.waitForFunction(() => Boolean(
    document.querySelector('lv-chat-thread')!
      .shadowRoot!
      .querySelector('lv-visual-artifact[artifact-id="agent_chart_1"]')
      ?.shadowRoot
      ?.querySelector('lv-visualization-host'),
  ))
  await page.waitForFunction(() => Boolean(
    document.querySelector('lv-chat-thread')!
      .shadowRoot!
      .querySelector('lv-visual-artifact[artifact-id="agent_table_1"]')
      ?.shadowRoot
      ?.querySelector('lv-visualization-host'),
  ))

  const rendered = await page.evaluate(() => {
    const root = document.querySelector('lv-chat-thread')!.shadowRoot!
    return {
      chart: (root.querySelector('lv-visual-artifact[artifact-id="agent_chart_1"]')?.shadowRoot?.querySelector('lv-visualization-host') as any)?.envelope?.spec?.kind,
      table: (root.querySelector('lv-visual-artifact[artifact-id="agent_table_1"]')?.shadowRoot?.querySelector('lv-visualization-host') as any)?.envelope?.spec?.kind,
      toolRows: root.querySelectorAll('.tool-call').length,
      bodyText: root.textContent || '',
      artifactBackground: getComputedStyle(root.querySelector('lv-visual-artifact')!.shadowRoot!.querySelector('.artifact')!).backgroundColor,
      artifactBorderTopWidth: getComputedStyle(root.querySelector('lv-visual-artifact')!.shadowRoot!.querySelector('.artifact')!).borderTopWidth,
    }
  })
  expect(rendered.chart).toBe('cartesian')
  expect(rendered.table).toBe('table')
  expect(rendered.toolRows).toBe(0)
  expect(rendered.bodyText.includes('delivered')).toBe(false)
  expect(rendered.artifactBackground).toBe('rgb(1, 2, 3)')
  expect(rendered.artifactBorderTopWidth).toBe('2px')

  await page.close()
})

test('chat thread hides tool activity while keeping Working and run errors visible', async () => {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.evaluate(async () => {
    await customElements.whenDefined('lv-chat-thread')
    const thread = document.querySelector('lv-chat-thread') as any
    thread.status = { enabled: true, running: true }
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
      runError: root.querySelector('.message.error')?.textContent?.replace(/\s+/g, ' ').trim(),
      assistantMarkdown: (root.querySelector('.agent-markdown') as any)?.value,
    }
  })

  expect(state.working).toBe('Working')
  expect(state.toolRows).toBe(0)
  expect(state.codeBlocks).toBe(0)
  expect(state.agentTurns).toBe(1)
  expect(state.runError).toBe('The dashboard could not be loaded.')
  expect(state.text).toContain('Find the sales dashboard.')
  expect(state.assistantMarkdown).toBe('I could not load that dashboard.')
  expect(state.text).not.toContain('Catalog lookup failed.')
  expect(state.text).not.toContain('secret tool result')
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

test('message actions copy exact text and prepare edits without submitting', async () => {
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
    await page.getByRole('button', { name: 'Ask again', exact: true }).click()
    expect(await page.evaluate(() => (window as any).reused)).toEqual([{ text: 'Explain revenue', references: [], editMessageId: 'u1' }, { text: 'Explain revenue', references: [] }])
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
