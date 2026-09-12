import { afterAll, beforeAll, expect, setDefaultTimeout, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser
let draftTurnRequests = 0
let draftTurnAnswerSent = false
let draftTurnAnswerFinished = false
let releaseDraftTurnAnswer: (() => void) | null = null

setDefaultTimeout(15_000)

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/chat-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (request.method === 'POST' && url.pathname === '/chats/turns') {
      draftTurnRequests += 1
      response.writeHead(200, {
        'cache-control': 'no-cache',
        'content-type': 'text/event-stream',
        connection: 'close',
      })
      response.write('event: datastar-patch-signals\ndata: signals {"agent":{"activeConversationId":"c3"}}\n\n')
      await new Promise<void>((resolve) => {
        releaseDraftTurnAnswer = resolve
      })
      draftTurnAnswerSent = true
      if (response.destroyed) {
        draftTurnAnswerFinished = true
        return
      }
      response.write('event: datastar-patch-signals\ndata: signals {"agent":{"transcript":[{"id":"fake-answer","kind":"assistant","markdown":"Fake answer","conversationId":"c3"}]}}\n\n')
      response.end()
      draftTurnAnswerFinished = true
      releaseDraftTurnAnswer = null
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
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  server.closeAllConnections()
  await new Promise<void>((resolve, reject) => server.close((error: NodeJS.ErrnoException | undefined) => {
    if (error && error.code !== 'ERR_SERVER_NOT_RUNNING') reject(error)
    else resolve()
  }))
}, 15_000)

for (const viewport of [
  { name: 'desktop', width: 1280, height: 820 },
  { name: 'mobile', width: 390, height: 820 },
]) {
  test(`chat page composes route UI on ${viewport.name}`, async () => {
    const page = await browser.newPage({ viewport })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => (
        customElements.get('lv-chat-page')
          && customElements.get('lv-chat-thread')
          && customElements.get('lv-chat-composer')
      ))
      await page.locator('lv-chat-page').evaluate((element: any) => element.updateComplete)

      const state = await page.locator('lv-chat-page').evaluate((element: any) => {
        const root = (element.shadowRoot as ShadowRoot)
        const composer = root.querySelector('lv-chat-composer') as any
        const thread = root.querySelector('lv-chat-thread') as any
        const threadRoot = thread?.shadowRoot
        return {
          title: root.querySelector('h1')?.textContent?.trim(),
          hasRouteHeader: Boolean(root.querySelector('header')),
          hasDescription: Boolean(root.querySelector('.conversation-description')),
          hasSubSidebar: Boolean(root.querySelector('lv-sub-sidebar')),
          hasThread: Boolean(thread),
          hasComposer: Boolean(composer),
          emptyState: threadRoot?.querySelector('.empty')?.textContent?.trim() ?? null,
          conversationId: thread?.conversationId,
          composerDisabled: composer?.disabled,
          composerPending: composer?.pending,
        }
      })

      expect(state).toEqual({
        title: 'Revenue check',
        hasRouteHeader: false,
        hasDescription: false,
        hasSubSidebar: false,
        hasThread: true,
        hasComposer: true,
        emptyState: null,
        conversationId: 'c1',
        composerDisabled: false,
        composerPending: false,
      })
    } finally {
      await page.close()
    }
  })
}

for (const viewport of [
  { name: 'desktop', width: 1280, height: 820, expectedSurfaceWidth: 760 },
  { name: 'narrow desktop', width: 700, height: 820, expectedSurfaceWidth: 668 },
  { name: 'mobile', width: 390, height: 820, expectedSurfaceWidth: 366 },
]) {
  test(`new chat page centers the title and composer on ${viewport.name}`, async () => {
    const page = await browser.newPage({ viewport })
    try {
      await page.goto(`${baseURL}/new`)
      await page.waitForFunction(() => (
        customElements.get('lv-chat-page')
          && customElements.get('lv-chat-composer')
      ))
      await page.locator('lv-chat-page').evaluate((element: any) => element.updateComplete)

      const state = await page.locator('lv-chat-page').evaluate(async (element: any) => {
        const root = (element.shadowRoot as ShadowRoot)
        const title = root.querySelector('h1') as HTMLElement
        const stage = root.querySelector('.new-chat-stage') as HTMLElement
        const intro = root.querySelector('.new-chat-intro') as HTMLElement
        const description = root.querySelector('.new-chat-description') as HTMLElement
        const hint = root.querySelector('.new-chat-context-hint') as HTMLElement
        const starters = Array.from(root.querySelectorAll('.prompt-starter')) as HTMLButtonElement[]
        const starterGrid = root.querySelector('.prompt-starters') as HTMLElement
        const composer = root.querySelector('lv-chat-composer') as any
        const composerRoot = composer?.shadowRoot
        const composerSurface = composerRoot?.querySelector('.composer-surface') as HTMLElement
        const titleRect = title.getBoundingClientRect()
        const stageRect = stage.getBoundingClientRect()
        const composerRect = composer.getBoundingClientRect()
        const surfaceRect = composerSurface.getBoundingClientRect()
        const introStyle = getComputedStyle(intro)
        const composerStyle = getComputedStyle(composer)
        const clusterTop = intro.getBoundingClientRect().top
        const clusterBottom = composerRect.bottom
        let submits = 0
        composer.addEventListener('lv-chat-submit', () => submits += 1)
        starters[0]?.click()
        await composer.updateComplete
        const textarea = composerRoot?.querySelector('textarea') as HTMLTextAreaElement
        return {
          title: title.textContent?.trim(),
          hasRouteHeader: Boolean(root.querySelector('header')),
          hasDescription: Boolean(root.querySelector('.conversation-description')),
          hasStartConversationBox: Boolean(root.querySelector('lv-chat-thread')?.shadowRoot?.querySelector('.empty')),
          hasThread: Boolean(root.querySelector('lv-chat-thread')),
          hasConversationTitlebar: Boolean(root.querySelector('.conversation-titlebar')),
          hasNewStage: Boolean(stage),
          hasComposer: Boolean(composer),
          composerDisabled: composer?.disabled,
          kicker: root.querySelector('.new-chat-kicker')?.textContent?.trim(),
          description: description.textContent?.trim(),
          contextHint: hint.textContent?.replace(/\s+/g, ' ').trim(),
          starters: starters.map((button) => ({
            label: button.querySelector('.prompt-starter-label')?.textContent?.trim(),
            prompt: button.querySelector('.prompt-starter-prompt')?.textContent?.trim(),
          })),
          starterDraft: textarea.value,
          starterFocused: composerRoot?.activeElement === textarea,
          starterSubmits: submits,
          titleCenterOffset: Math.round(Math.abs((titleRect.left + titleRect.width / 2) - window.innerWidth / 2)),
          composerBottomDistance: Math.round(window.innerHeight - composerRect.bottom),
          composerBorderTopWidth: getComputedStyle(composer).borderTopWidth,
          composerSurfaceWidth: Math.round(surfaceRect.width),
          composerSurfaceLeft: Math.round(surfaceRect.left),
          surfaceCenterOffset: Math.round(Math.abs((surfaceRect.left + surfaceRect.width / 2) - window.innerWidth / 2)),
          clusterCenterOffset: Math.round(Math.abs((clusterTop + (clusterBottom - clusterTop) / 2) - (stageRect.top + stageRect.height / 2))),
          introAnimationName: introStyle.animationName,
          introAnimationDuration: introStyle.animationDuration,
          composerAnimationName: composerStyle.animationName,
          composerAnimationDelay: composerStyle.animationDelay,
          stageJustifyContent: getComputedStyle(stage).justifyContent,
          promptColumns: getComputedStyle(starterGrid).gridTemplateColumns.split(' ').length,
          contextActionDisplay: getComputedStyle(composerRoot.querySelector('.context-button')).display,
          hasVerticalOverflow: document.documentElement.scrollHeight > window.innerHeight,
          hasHorizontalOverflow: document.documentElement.scrollWidth > window.innerWidth,
        }
      })

      expect(state).toMatchObject({
        title: 'Ask about your data',
        hasRouteHeader: false,
        hasDescription: false,
        hasStartConversationBox: false,
        hasThread: false,
        hasConversationTitlebar: false,
        hasNewStage: true,
        hasComposer: true,
        composerDisabled: false,
        kicker: 'LeapView Agent',
        description: 'Get clear answers grounded in the dashboards, metrics, and models you can access.',
        contextHint: 'Type @ to attach a dashboard, metric, model, page, or visual.',
        starters: [
          { label: 'Spot a change', prompt: 'What changed most in the last 30 days?' },
          { label: 'Explain a metric', prompt: 'Explain how revenue is calculated.' },
          { label: 'Review a dashboard', prompt: 'Summarize the Executive Sales dashboard.' },
        ],
        starterDraft: 'What changed most in the last 30 days?',
        starterFocused: true,
        starterSubmits: 0,
        titleCenterOffset: 0,
        composerBorderTopWidth: '0px',
        composerSurfaceWidth: viewport.expectedSurfaceWidth,
        composerSurfaceLeft: Math.round((viewport.width - viewport.expectedSurfaceWidth) / 2),
        surfaceCenterOffset: 0,
        introAnimationName: 'new-chat-enter',
        introAnimationDuration: '0.26s',
        composerAnimationName: 'new-chat-enter',
        composerAnimationDelay: '0.07s',
        stageJustifyContent: viewport.name === 'desktop' ? 'center' : 'flex-start',
        promptColumns: viewport.name === 'desktop' ? 3 : 1,
        contextActionDisplay: 'none',
        hasVerticalOverflow: false,
        hasHorizontalOverflow: false,
      })
      if (viewport.name === 'desktop') expect(state.clusterCenterOffset).toBeLessThanOrEqual(24)
      expect(state.composerBottomDistance).toBeGreaterThan(0)
    } finally {
      await page.close()
    }
  })
}

test('new chat navigates when the created conversation signal arrives', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/new`)
    await page.waitForFunction(() => customElements.get('lv-chat-page'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { activeConversationId: 'c3' } })
    })
    await page.waitForURL(`${baseURL}/chats/c3`)
    expect(new URL(page.url()).pathname).toBe('/chats/c3')
  } finally {
    await page.close()
  }
})

test('new chat submits Enter and navigates from the command signal before the answer arrives', async () => {
  draftTurnRequests = 0
  draftTurnAnswerSent = false
  draftTurnAnswerFinished = false
  releaseDraftTurnAnswer = null
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/new`)
    await page.waitForFunction(() => customElements.get('lv-chat-page') && customElements.get('lv-chat-composer'))
    await page.locator('lv-chat-page').evaluate(async (element: any) => {
      await element.updateComplete
      const composer = element.shadowRoot.querySelector('lv-chat-composer') as any
      await composer.updateComplete
    })

    const textarea = page.locator('lv-chat-page').locator('lv-chat-composer').locator('textarea')
    await textarea.fill('What changed most recently?')
    await textarea.press('Enter')

    await page.waitForURL(`${baseURL}/chats/c3`)
    expect(draftTurnRequests).toBe(1)
    expect(draftTurnAnswerSent).toBe(false)
    expect(new URL(page.url()).pathname).toBe('/chats/c3')
  } finally {
    const release = releaseDraftTurnAnswer as (() => void) | null
    release?.()
    for (let attempt = 0; attempt < 50 && !draftTurnAnswerFinished; attempt += 1) {
      await new Promise<void>((resolve) => setTimeout(resolve, 10))
    }
    await page.close()
  }
})

test('chat list page renders searchable conversation history', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/list`)
    await page.waitForFunction(() => customElements.get('lv-chat-page') && customElements.get('lv-chat-list'))
    await page.locator('lv-chat-page').evaluate((element: any) => element.updateComplete)

    const initial = await page.locator('lv-chat-page').evaluate((element: any) => {
      const root = (element.shadowRoot as ShadowRoot)
      const list = root.querySelector('lv-chat-list') as any
      const listRoot = list?.shadowRoot
      return {
        hasThread: Boolean(root.querySelector('lv-chat-thread')),
        hasComposer: Boolean(root.querySelector('lv-chat-composer')),
        hasRouteHeader: Boolean(root.querySelector('header')),
        hasChatList: Boolean(list),
        activeConversationId: list?.activeConversationId,
        title: listRoot?.querySelector('h2')?.textContent?.trim(),
        searchPlaceholder: listRoot?.querySelector('.search')?.getAttribute('placeholder'),
        newChatHref: listRoot?.querySelector('.new-chat-link')?.getAttribute('href'),
        headerOrder: Array.from(listRoot?.querySelector('.header')?.children ?? []).map((child: any) => child.className || child.tagName.toLowerCase()),
        metrics: (() => {
          const title = listRoot?.querySelector('h2') as HTMLElement
          const search = listRoot?.querySelector('.search') as HTMLElement
          const link = listRoot?.querySelector('.new-chat-link') as HTMLElement
          const firstRow = listRoot?.querySelector('tbody tr') as HTMLElement
          const firstDate = firstRow?.querySelector('.date') as HTMLElement
          const rowRect = firstRow.getBoundingClientRect()
          const dateRect = firstDate.getBoundingClientRect()
          const linkStyle = getComputedStyle(link)
          return {
            titleFontSize: getComputedStyle(title).fontSize,
            searchHeight: Math.round(search.getBoundingClientRect().height),
            buttonHeight: Math.round(link.getBoundingClientRect().height),
            buttonBackground: linkStyle.backgroundColor,
            buttonColor: linkStyle.color,
            rowHeight: Math.round(firstRow.getBoundingClientRect().height),
            dateDistanceFromRowEnd: Math.round(rowRect.right - dateRect.right),
          }
        })(),
        tableHeaders: Array.from(listRoot?.querySelectorAll('thead th') ?? []).map((header: any) => header.textContent.trim()),
        rows: Array.from(listRoot?.querySelectorAll('tbody tr') ?? []).map((row: any) => ({
          href: row.querySelector('.primary-link')?.getAttribute('href'),
          label: row.querySelector('.primary-link')?.getAttribute('aria-label'),
          active: row.getAttribute('data-active'),
          text: row.textContent.replace(/\s+/g, ' ').trim(),
          optionsLabel: row.querySelector('.options-button')?.getAttribute('aria-label'),
        })),
      }
    })

    expect(initial.hasThread).toBe(false)
    expect(initial.hasComposer).toBe(false)
    expect(initial.hasRouteHeader).toBe(false)
    expect(initial.hasChatList).toBe(true)
    expect(initial.activeConversationId).toBe('c1')
    expect(initial.title).toBe('Chats')
    expect(initial.searchPlaceholder).toBe('Search chats...')
    expect(initial.newChatHref).toBe('/chats/new')
    expect(initial.headerOrder).toEqual(['h2', 'new-chat-link'])
    expect(initial.metrics).toEqual({
      titleFontSize: '20px',
      searchHeight: 40,
      buttonHeight: 32,
      buttonBackground: 'rgb(255, 255, 255)',
      buttonColor: 'rgb(36, 41, 47)',
      rowHeight: 53,
      dateDistanceFromRowEnd: 12,
    })
    expect(initial.tableHeaders).toEqual(['Conversation'])
    expect(initial.rows).toContainEqual({ href: '/chats/c1', label: 'Revenue check', active: 'true', text: 'Revenue check Jan 2', optionsLabel: undefined })
    expect(initial.rows).toContainEqual({ href: '/chats/c2', label: 'Inventory status', active: 'false', text: 'Inventory status Jan 3', optionsLabel: undefined })

    await page.locator('lv-chat-page').evaluate((element: any) => {
      const input = ((element.shadowRoot as ShadowRoot).querySelector('lv-chat-list') as TestDomElement).shadowRoot!.querySelector('.search') as HTMLInputElement
      input.value = 'inventory'
      input.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true, inputType: 'insertText', data: 'inventory' }))
    })
    await page.locator('lv-chat-page').evaluate(async (element: any) => {
      const list = (element.shadowRoot as ShadowRoot).querySelector('lv-chat-list') as any
      await list.updateComplete
    })

    const filteredRows = await page.locator('lv-chat-page').evaluate((element: any) => {
      const root = ((element.shadowRoot as ShadowRoot).querySelector('lv-chat-list') as TestDomElement).shadowRoot!
      return Array.from(root.querySelectorAll('tbody tr')).map((row: any) => ({
        href: row.querySelector('.primary-link')?.getAttribute('href'),
        text: row.textContent.replace(/\s+/g, ' ').trim(),
      }))
    })

    expect(filteredRows).toEqual([{ href: '/chats/c2', text: 'Inventory status Jan 3' }])

    const scrollState = await page.evaluate(() => ({
      innerHeight,
      scrollHeight: document.documentElement.scrollHeight,
      bodyScrollHeight: document.body.scrollHeight,
      hasVerticalOverflow: document.documentElement.scrollHeight > window.innerHeight,
    }))
    expect(scrollState.hasVerticalOverflow).toBe(false)
  } finally {
    await page.close()
  }
})

test('unconfigured agent uses intentional unavailable states', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/unavailable-new`)
    await page.waitForFunction(() => customElements.get('lv-chat-page'))
    const newState = await page.locator('lv-chat-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)
      const composer = root.querySelector('lv-chat-composer') as any
      await composer.updateComplete
      return {
        title: root.querySelector('.new-chat-title')?.textContent?.trim(),
        description: root.querySelector('.new-chat-description')?.textContent?.trim(),
        starterCount: root.querySelectorAll('.prompt-starter').length,
        startersDisabled: Array.from(root.querySelectorAll<HTMLButtonElement>('.prompt-starter')).every((button) => button.disabled),
        composerDisabled: composer.disabled,
        placeholder: composer.shadowRoot.querySelector('textarea')?.getAttribute('placeholder'),
      }
    })
    expect(newState).toEqual({
      title: 'Ask about your data',
      description: 'Get clear answers grounded in the dashboards, metrics, and models you can access.',
      starterCount: 3,
      startersDisabled: true,
      composerDisabled: true,
      placeholder: 'Agent is not configured.',
    })

    await page.goto(`${baseURL}/unavailable-list`)
    await page.waitForFunction(() => customElements.get('lv-chat-list'))
    const listState = await page.locator('lv-chat-page').evaluate(async (element: any) => {
      await element.updateComplete
      const list = element.shadowRoot.querySelector('lv-chat-list') as any
      await list.updateComplete
      const root = list.shadowRoot
      return {
        title: root.querySelector('.empty-title')?.textContent?.trim(),
        detail: root.querySelector('.empty-detail')?.textContent?.trim(),
        hasSearch: Boolean(root.querySelector('.search')),
        newChatDisabled: root.querySelector('.new-chat-link')?.hasAttribute('disabled'),
      }
    })
    expect(listState).toEqual({ title: 'No chats yet', detail: 'Agent is not configured.', hasSearch: false, newChatDisabled: true })
  } finally {
    await page.close()
  }
})

function testDocument(view = 'conversation', scenario: 'active' | 'new' = 'active', enabled = true): string {
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
    ? ` data-on:lv-chat-submit="$agent.composer.value = evt.detail.input; @post('/chats/turns')"`
    : ''
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-panel: #fff; --lv-bg-control: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-bg-hover: #eff2f5; --lv-bg-accent-muted: #ddf4ff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-accent: #0969da; --lv-accent-fg: #fff; --lv-line-default: #d0d7de; --lv-line-muted: #d8dee4; --lv-line-accent: #0969da; --lv-line-accent-muted: #54aeff; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-border-transparent: 1px solid transparent; --lv-border-width-focus: 2px; --lv-radius-default: 6px; --lv-radius-tight: 4px; --lv-radius-large: 12px; --base-size-4: 4px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-36: 36px; --lv-space-2xs: 2px; --lv-space-xs: 4px; --lv-space-sm: 8px; --lv-space-md: 12px; --lv-space-lg: 16px; --lv-space-control: 10px; --control-medium-size: 32px; --control-large-size: 40px; --control-medium-paddingInline-spacious: 16px; --lv-control-medium: 32px; --button-primary-bgColor-rest: #0969da; --button-primary-bgColor-hover: #0757b3; --button-primary-fgColor-rest: #fff; --lv-chat-stack-width: 760px; --lv-chat-thread-padding: 16px; --lv-chat-thread-padding-compact: 12px; --lv-transition-fast: 160ms ease; --lv-transition-medium: 260ms ease; --shadow-resting-small: 0 1px 2px rgb(0 0 0 / .08); --lv-shadow-floating-sm: 0 8px 24px rgb(0 0 0 / .12); --duration-fast: 160ms; --ease-lv: ease; }
          lv-chat-page { min-height: 720px; }
        </style>
      </head>
      <body>
        <main data-signals="${escapeHTML(JSON.stringify({ page, agent, visuals: {}, tables: {} }))}">
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
