import { afterAll, beforeAll, setDefaultTimeout } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

export function chatPageBrowserFixture() {
  const fixture = { browser: undefined as unknown as Browser, baseURL: '', draftTurnRequests: 0, draftTurnAnswerSent: false, draftTurnAnswerFinished: false, releaseDraftTurnAnswer: null as (() => void) | null, savedVisualRequest: null as { body: string; csrf: string | undefined } | null }
  let server: Server
  setDefaultTimeout(15_000)

  const projectRoot = process.cwd()
  const root = join(projectRoot, '.tmp/chat-page-test')

  beforeAll(async () => {
    server = createServer(async (request, response) => {
      const url = new URL(request.url ?? '/', 'http://127.0.0.1')
      if (request.method === 'POST' && url.pathname === '/explore/saved') {
        const chunks: Buffer[] = []
        for await (const chunk of request) chunks.push(Buffer.from(chunk))
        fixture.savedVisualRequest = { body: Buffer.concat(chunks).toString(), csrf: request.headers['x-csrf-token'] as string | undefined }
        response.writeHead(201, { 'content-type': 'application/json' })
        response.end(JSON.stringify({ item: { id: 'saved-1', title: 'Revenue by country', href: '/explore?saved=saved-1' } }))
        return
      }
      if (request.method === 'POST' && url.pathname === '/chats/turns') {
        fixture.draftTurnRequests += 1
        response.writeHead(200, {
          'cache-control': 'no-cache',
          'content-type': 'text/event-stream',
          connection: 'close',
        })
        response.write('event: datastar-patch-signals\ndata: signals {"agent":{"activeConversationId":"c3"}}\n\n')
        await new Promise<void>((resolve) => {
          fixture.releaseDraftTurnAnswer = resolve
        })
        fixture.draftTurnAnswerSent = true
        if (response.destroyed) {
          fixture.draftTurnAnswerFinished = true
          return
        }
        response.write('event: datastar-patch-signals\ndata: signals {"agent":{"transcript":[{"id":"fake-answer","kind":"assistant","markdown":"Fake answer","conversationId":"c3"}]}}\n\n')
        response.end()
        fixture.draftTurnAnswerFinished = true
        fixture.releaseDraftTurnAnswer = null
        return
      }
      if (url.pathname === '/') {
        response.setHeader('content-type', 'text/html')
        response.end(testDocument())
        return
      }
      if (url.pathname === '/list') {
        response.setHeader('content-type', 'text/html')
        response.end(testDocument('list'))
        return
      }
      if (url.pathname === '/new') {
        response.setHeader('content-type', 'text/html')
        response.end(testDocument('new', 'new'))
        return
      }
      if (url.pathname === '/unavailable-new') {
        response.setHeader('content-type', 'text/html')
        response.end(testDocument('new', 'new', false))
        return
      }
      if (url.pathname === '/unavailable-list') {
        response.setHeader('content-type', 'text/html')
        response.end(testDocument('list', 'new', false))
        return
      }
      if (url.pathname === '/unhydrated') {
        response.setHeader('content-type', 'text/html')
        response.end(testDocument('conversation', 'active', true, false))
        return
      }
      if (url.pathname.startsWith('/chats/')) {
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
    if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
    fixture.baseURL = `http://127.0.0.1:${address.port}`
    fixture.browser = await chromium.launch()
  })

  afterAll(async () => {
    await fixture.browser?.close()
    server.closeAllConnections()
    await new Promise<void>((resolve, reject) => server.close((error: NodeJS.ErrnoException | undefined) => {
      if (error && error.code !== 'ERR_SERVER_NOT_RUNNING') reject(error)
      else resolve()
    }))
  }, 15_000)

  return fixture
}

export function testDocument(view = 'conversation', scenario: 'active' | 'new' = 'active', enabled = true, hydrated = true): string {
  const page = {
    kind: 'chat',
    view,
    title: 'Chats',
    description: 'Ask about governed BI or make authorized dashboard changes.',
  }
  const agent = {
    conversations: enabled ? [
      { id: 'c1', title: 'Revenue check', href: '/chats/c1', updatedAt: '2026-01-02T10:00:00Z' },
      { id: 'c2', title: 'Inventory status', href: '/chats/c2', updatedAt: '2026-01-03T10:00:00Z' },
    ] : [],
    activeConversationId: scenario === 'new' ? '' : 'c1',
    transcript: scenario === 'new' ? [] : [{ role: 'assistant', content: 'Ready.' }],
    status: { enabled, running: false, ...(enabled ? {} : { error: 'Agent is not configured.' }) },
    composer: { value: '', disabled: !enabled, placeholder: enabled ? 'Ask about dashboards, metrics, or models...' : 'Agent is not configured.' },
  }
  const submitCommand = scenario === 'new'
    ? ` data-on:lv-chat-submit="$agent.status.error = ''; $agent.composer.value = evt.detail.input; @post('/chats/turns')"`
    : ''
  return `
    <!doctype html>
    <html>
      <head>
        <meta name="csrf-token" content="test-csrf">
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-panel: #fff; --lv-bg-control: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-bg-hover: #eff2f5; --lv-bg-accent-muted: #ddf4ff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-accent: #0969da; --lv-accent-fg: #fff; --lv-line-default: #d0d7de; --lv-line-muted: #d8dee4; --lv-line-accent: #0969da; --lv-line-accent-muted: #54aeff; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-border-transparent: 1px solid transparent; --lv-border-width-focus: 2px; --lv-radius-default: 6px; --lv-radius-tight: 4px; --lv-radius-large: 12px; --base-size-4: 4px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-36: 36px; --lv-space-2xs: 2px; --lv-space-xs: 4px; --lv-space-sm: 8px; --lv-space-md: 12px; --lv-space-lg: 16px; --lv-space-control: 10px; --control-medium-size: 32px; --control-large-size: 40px; --control-medium-paddingInline-spacious: 16px; --lv-control-medium: 32px; --button-primary-bgColor-rest: #0969da; --button-primary-bgColor-hover: #0757b3; --button-primary-fgColor-rest: #fff; --lv-chat-stack-width: 760px; --lv-chat-thread-padding: 16px; --lv-chat-thread-padding-compact: 12px; --lv-transition-fast: 160ms ease; --lv-transition-medium: 260ms ease; --shadow-resting-small: 0 1px 2px rgb(0 0 0 / .08); --lv-shadow-floating-sm: 0 8px 24px rgb(0 0 0 / .12); --duration-fast: 160ms; --ease-lv: ease; }
          lv-chat-page { min-height: 720px; }
        </style>
      </head>
      <body>
        <main ${hydrated ? `data-signals="${escapeHTML(JSON.stringify({ page, agent, agentContext: { surface: 'chat', dashboardId: '', pageId: '', references: [] }, visuals: {}, tables: {} }))}"` : ''}>
          <lv-chat-page${submitCommand}></lv-chat-page>
        </main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/chat-page-under-test.js"></script>
      </body>
    </html>
  `
}

function escapeHTML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}

export async function openDashboardTestVisual(page: import('@playwright/test').Page, baseURL: string): Promise<void> {
  await page.goto(`${baseURL}/chats/c1`)
  await page.locator('lv-chat-page lv-chat-thread').waitFor()
  await page.locator('lv-chat-page').evaluate(async (chat: any) => {
    await chat.updateComplete
    const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
    const field = (id: string, role: string) => ({ id, role, dataType: role === 'metric' ? 'decimal' : 'string', nullable: false, label: id })
    mergePatch({ agent: { transcript: [{ id: 'tool-dashboard', kind: 'tool', name: 'query_visual', status: 'complete', artifact: { id: 'chart-dashboard', type: 'bar', summary: 'Net sales by country' } }] }, visuals: { 'chart-dashboard': {
schemaVersion: 14, visualID: 'chart-dashboard', rendererID: 'echarts', specRevision: `sha256:${'2'.repeat(64)}`, dataRevision: 1,
          spec: { kind: 'cartesian', mark: 'bar', title: 'Net sales by country', datasets: [{ id: 'primary', fields: [field('label', 'dimension'), field('value', 'metric')] }], dataBudget: { maxRows: 50, requiredCompleteness: 'complete' }, accessibility: { title: 'Net sales by country', description: 'Revenue' }, interactions: [], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } },
          dataState: { kind: 'inline', specRevision: `sha256:${'2'.repeat(64)}`, dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: `sha256:${'2'.repeat(64)}`, dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['France', 42]], completeness: 'complete' }] },
          selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
    } } })
    await chat.updateComplete
    chat.shadowRoot.querySelector('lv-chat-thread').dispatchEvent(new CustomEvent('lv-chat-visual-open', {
      detail: { artifactId: 'chart-dashboard', title: 'Net sales by country', explorerHref: '/explore?model=sales' }, bubbles: true, composed: true,
    }))
  })
  await page.getByRole('button', { name: 'Add to dashboard', exact: true }).click()
}
