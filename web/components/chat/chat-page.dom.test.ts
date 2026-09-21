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
        await Promise.all(Array.from(stage.children).flatMap((child) => child.getAnimations().map((animation) => animation.finished)))
        const intro = root.querySelector('.new-chat-intro') as HTMLElement
        const hint = root.querySelector('.new-chat-context-hint') as HTMLElement
        const starters = Array.from(root.querySelectorAll('.prompt-starter')) as HTMLButtonElement[]
        const starterGroup = root.querySelector('.prompt-starters') as HTMLElement
        const composer = root.querySelector('lv-chat-composer') as any
        const composerRoot = composer?.shadowRoot
        const composerSurface = composerRoot?.querySelector('.composer-surface') as HTMLElement
        const headingRect = root.querySelector('.new-chat-heading')!.getBoundingClientRect()
        const stageRect = stage.getBoundingClientRect()
        const composerRect = composer.getBoundingClientRect()
        const surfaceRect = composerSurface.getBoundingClientRect()
        const introStyle = getComputedStyle(intro)
        const composerStyle = getComputedStyle(composer)
        const clusterTop = intro.getBoundingClientRect().top
        const clusterBottom = hint.getBoundingClientRect().bottom
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
          hasAgentMark: Boolean(root.querySelector('.new-chat-heading .agent-mark')),
          descriptionCount: root.querySelectorAll('.new-chat-description').length,
          contextHint: hint.textContent?.replace(/\s+/g, ' ').trim(),
          starters: starters.map((button) => ({
            label: button.querySelector('.prompt-starter-label')?.textContent?.trim(),
            prompt: button.getAttribute('title'),
          })),
          promptsFollowComposer: composer.nextElementSibling === starterGroup,
          promptsAreCompact: starters.every((button) => button.getBoundingClientRect().height <= 40),
          starterDraft: textarea.value,
          starterFocused: composerRoot?.activeElement === textarea,
          starterSubmits: submits,
          titleCenterOffset: Math.round(Math.abs((headingRect.left + headingRect.width / 2) - window.innerWidth / 2)),
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
          stageFlexDirection: getComputedStyle(stage).flexDirection,
          promptLayout: getComputedStyle(starterGroup).display,
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
        hasAgentMark: true,
        descriptionCount: 0,
        contextHint: 'Type @ to attach a dashboard, metric, model, page, or visual.',
        starters: [
          { label: 'Spot a change', prompt: 'What changed most in the last 30 days?' },
          { label: 'Explain a metric', prompt: 'Explain how revenue is calculated.' },
          { label: 'Review a dashboard', prompt: 'Summarize the Executive Sales dashboard.' },
        ],
        starterDraft: 'What changed most in the last 30 days?',
        starterFocused: true,
        starterSubmits: 0,
        promptsFollowComposer: true,
        promptsAreCompact: true,
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
        stageFlexDirection: 'column',
        promptLayout: 'flex',
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

test('active chat shows a submitted turn immediately and replaces it with durable state', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-page') && customElements.get('lv-chat-composer'))
    const lifecycle = await page.locator('lv-chat-page').evaluate(async (element: any) => {
      await element.updateComplete
      const composer = element.shadowRoot.querySelector('lv-chat-composer') as HTMLElement
      composer.dispatchEvent(new CustomEvent('lv-chat-submit', {
        bubbles: true,
        composed: true,
        detail: { input: 'What changed this month?', references: [] },
      }))
      await element.updateComplete
      const thread = element.shadowRoot.querySelector('lv-chat-thread') as any
      await thread.updateComplete
      const optimistic = {
        pending: element.pending,
        transcript: thread.transcript,
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { transcript: [
        { id: 'ready', kind: 'assistant', markdown: 'Ready.', conversationId: 'c1' },
        { id: 'accepted-user', kind: 'user', text: 'What changed this month?', conversationId: 'c1' },
      ], status: { enabled: true, running: true } } })
      await new Promise<void>(resolve => requestAnimationFrame(() => resolve()))
      await element.updateComplete
      await thread.updateComplete
      return { optimistic, durable: thread.transcript }
    })
    expect(lifecycle.optimistic.pending).toBe(true)
    expect(lifecycle.optimistic.transcript.at(-1)).toMatchObject({ kind: 'user', text: 'What changed this month?' })
    expect(lifecycle.durable.at(-1)?.id).toBe('accepted-user')
    expect(lifecycle.durable.some((item: any) => item.id?.startsWith('optimistic-'))).toBe(false)
  } finally {
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
        newChatHref: listRoot?.querySelector('a.new-chat-link')?.getAttribute('href'),
        headerActions: Array.from(listRoot?.querySelectorAll('.header-actions .new-chat-link') ?? []).map((action: any) => action.textContent.trim()),
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
          title: row.querySelector('.title')?.textContent?.trim(),
          date: row.querySelector('.date')?.textContent?.trim(),
          optionsLabel: row.querySelector('.options-button')?.getAttribute('aria-label'),
          quickActions: Array.from(row.querySelectorAll('.quick-action')).map((action: any) => action.getAttribute('aria-label')),
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
    expect(initial.headerActions).toEqual(['Archived chats', 'Delete all chats', 'New chat'])
    expect(initial.headerOrder).toEqual(['h2', 'header-actions'])
    expect(initial.metrics).toEqual({
      titleFontSize: '20px',
      searchHeight: 40,
      buttonHeight: 32,
      buttonBackground: 'rgb(255, 255, 255)',
      buttonColor: 'rgb(36, 41, 47)',
      rowHeight: 53,
      dateDistanceFromRowEnd: 58,
    })
    expect(initial.tableHeaders).toEqual(['Conversation'])
    expect(initial.rows).toContainEqual({ href: '/chats/c1', label: 'Revenue check', active: 'true', title: 'Revenue check', date: 'Jan 2', optionsLabel: 'More actions for Revenue check', quickActions: ['Pin Revenue check', 'Archive Revenue check'] })
    expect(initial.rows).toContainEqual({ href: '/chats/c2', label: 'Inventory status', active: 'false', title: 'Inventory status', date: 'Jan 3', optionsLabel: 'More actions for Inventory status', quickActions: ['Pin Inventory status', 'Archive Inventory status'] })
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
        title: row.querySelector('.title')?.textContent?.trim(),
        date: row.querySelector('.date')?.textContent?.trim(),
      }))
    })

    expect(filteredRows).toEqual([{ href: '/chats/c2', title: 'Inventory status', date: 'Jan 3' }])

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

test('chat history keeps a readable centered width on wide screens and fits narrow screens', async () => {
  const page = await browser.newPage({ viewport: { width: 1600, height: 900 } })
  try {
    await page.goto(`${baseURL}/list`)
    await page.waitForFunction(() => customElements.get('lv-chat-list'))
    const bounds = () => page.locator('lv-chat-list').evaluate((element: HTMLElement) => {
      const shell = element.shadowRoot!.querySelector<HTMLElement>('.shell')!
      const rect = shell.getBoundingClientRect()
      return { left: rect.left, right: rect.right, width: rect.width, viewport: window.innerWidth }
    })
    const wide = await bounds()
    expect(wide.width).toBeLessThanOrEqual(760)
    expect(Math.abs(wide.left - (wide.viewport - wide.right))).toBeLessThanOrEqual(2)
    await page.setViewportSize({ width: 390, height: 800 })
    const narrow = await bounds()
    expect(narrow.width).toBeLessThanOrEqual(390)
    expect(narrow.left).toBeGreaterThanOrEqual(0)
    expect(narrow.right).toBeLessThanOrEqual(390)
  } finally {
    await page.close()
  }
})

test('chat list opens archived chats from its header', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/list`)
    await page.waitForFunction(() => customElements.get('lv-chat-list'))
    await page.evaluate(() => {
      ;(window as any).archiveOpens = 0
      document.addEventListener('lv-chat-settings-open', () => { (window as any).archiveOpens++ })
    })
    await page.locator('lv-chat-page').locator('lv-chat-list').getByRole('button', { name: 'Archived chats' }).click()
    expect(await page.evaluate(() => (window as any).archiveOpens)).toBe(1)
  } finally {
    await page.close()
  }
})

test('chat list exposes bulk deletion and archive row action on hover', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/list`)
    await page.waitForFunction(() => customElements.get('lv-chat-page') && customElements.get('lv-chat-list'))
    await page.locator('lv-chat-page').evaluate(async (element: any) => {
      const list = (element.shadowRoot as ShadowRoot).querySelector('lv-chat-list') as any
      await list.updateComplete
      ;(window as any).listActions = []
      list.addEventListener('lv-chat-action', (event: Event) => {
        event.stopPropagation()
        ;(window as any).listActions.push((event as CustomEvent).detail)
      })
    })
    const list = page.locator('lv-chat-page').locator('lv-chat-list')
    const firstRow = list.locator('tbody tr').first()
    const quickActions = firstRow.locator('.quick-actions')
    expect(await quickActions.evaluate((element) => getComputedStyle(element).opacity)).toBe('0')
    await firstRow.hover()
    await page.waitForFunction(() => {
      const chatPage = document.querySelector('lv-chat-page') as any
      const chatList = chatPage?.shadowRoot?.querySelector('lv-chat-list') as any
      const actions = chatList?.shadowRoot?.querySelector('.quick-actions')
      return actions && getComputedStyle(actions).opacity === '1'
    })
    expect(await quickActions.evaluate((element) => getComputedStyle(element).opacity)).toBe('1')
    await firstRow.getByRole('button', { name: 'Archive Revenue check', exact: true }).click()
    await list.getByRole('button', { name: 'Delete all chats', exact: true }).click()
    expect(await page.evaluate(() => (window as any).listActions.map((action: any) => ({ action: action.action, conversationId: action.conversationId })))).toEqual([
      { action: 'archive', conversationId: 'c1' },
      { action: 'delete_active', conversationId: '' },
    ])
  } finally {
    await page.close()
  }
})

test('chat list row menu supports keyboard dismissal and dispatches actions', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/list`)
    await page.waitForFunction(() => customElements.get('lv-chat-page') && customElements.get('lv-chat-list'))
    const state = await page.locator('lv-chat-page').evaluate(async (element: any) => {
      const list = (element.shadowRoot as ShadowRoot).querySelector('lv-chat-list') as any
      await list.updateComplete
      const root = list.shadowRoot as ShadowRoot
      const actions: unknown[] = []
      list.addEventListener('lv-chat-action', (event: Event) => {
        event.stopPropagation()
        actions.push((event as CustomEvent).detail)
      })
      const row = root.querySelector('tbody tr') as HTMLElement
      const trigger = row.querySelector<HTMLButtonElement>('.options-button')!
      trigger.focus()
      trigger.click()
      await list.updateComplete
      const menu = row.querySelector<HTMLElement>('.options-panel')!
      trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true }))
      const initialArrowFocus = (root.activeElement as HTMLElement)?.textContent?.trim()
      const pin = menu.querySelector<HTMLButtonElement>('[role="menuitem"]:nth-of-type(2)')!
      pin.focus()
      pin.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true }))
      const arrowFocus = (root.activeElement as HTMLElement)?.textContent?.trim()
      pin.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true }))
      const escaped = { open: menu.matches(':popover-open'), focus: (root.activeElement as HTMLElement)?.getAttribute('aria-label') }
      trigger.click()
      await list.updateComplete
      const reopenedMenu = row.querySelector<HTMLElement>('.options-panel')!
      reopenedMenu.querySelector<HTMLButtonElement>('[role="menuitem"]:nth-of-type(2)')!.click()
      return { initialArrowFocus, arrowFocus, escaped, actions }
    })
    expect(state.initialArrowFocus).toBe('Select')
    expect(state.arrowFocus).toBe('Archive chat')
    expect(state.escaped).toEqual({ open: false, focus: 'More actions for Revenue check' })
    expect(state.actions).toEqual([{ action: 'pin', conversationId: 'c1', title: 'Revenue check', href: '/chats/c1' }])
  } finally {
    await page.close()
  }
})

test('last chat row menu stays visible and keeps delete accessible', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 300 } })
  try {
    await page.goto(`${baseURL}/list`)
    await page.waitForFunction(() => customElements.get('lv-chat-page') && customElements.get('lv-chat-list'))
    const list = page.locator('lv-chat-page').locator('lv-chat-list')
    await list.evaluate(async (element: any) => {
      await element.updateComplete
      ;(window as any).listActions = []
      element.addEventListener('lv-chat-action', (event: Event) => {
        event.stopPropagation()
        ;(window as any).listActions.push((event as CustomEvent).detail)
      })
    })
    await list.locator('tbody tr').last().getByRole('button', { name: 'More actions for Inventory status' }).click()
    const menu = list.getByRole('menu', { name: 'Actions for Inventory status' })
    const bounds = await menu.evaluate((element) => {
      const rect = element.getBoundingClientRect()
      return { visible: element.matches(':popover-open'), top: rect.top, bottom: rect.bottom, left: rect.left, right: rect.right, width: innerWidth, height: innerHeight }
    })
    expect(bounds.visible).toBe(true)
    expect(bounds.top).toBeGreaterThanOrEqual(0)
    expect(bounds.bottom).toBeLessThanOrEqual(bounds.height)
    expect(bounds.left).toBeGreaterThanOrEqual(0)
    expect(bounds.right).toBeLessThanOrEqual(bounds.width)
    await menu.getByRole('menuitem', { name: 'Delete chat' }).click()
    expect(await page.evaluate(() => (window as any).listActions.map((action: any) => action.action))).toEqual(['delete'])
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
        descriptionCount: root.querySelectorAll('.new-chat-description').length,
        starterCount: root.querySelectorAll('.prompt-starter').length,
        startersDisabled: Array.from(root.querySelectorAll<HTMLButtonElement>('.prompt-starter')).every((button) => button.disabled),
        composerDisabled: composer.disabled,
        placeholder: composer.shadowRoot.querySelector('textarea')?.getAttribute('placeholder'),
      }
    })
    expect(newState).toEqual({
      title: 'Ask about your data',
      descriptionCount: 0,
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
        newChatDisabled: root.querySelector('button[disabled]')?.hasAttribute('disabled'),
        hasArchivedAction: Boolean(Array.from((root as ShadowRoot).querySelectorAll('.header-actions button') as NodeListOf<HTMLButtonElement>).find((button) => button.textContent?.includes('Archive'))),
      }
    })
    expect(listState).toEqual({ title: 'No chats yet', detail: 'Agent is not configured.', hasSearch: false, newChatDisabled: true, hasArchivedAction: true })
  } finally {
    await page.close()
  }
})

test('chat switch waits for bootstrap before showing agent availability', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/unhydrated`)
    await page.waitForFunction(() => customElements.get('lv-chat-page'))
    const before = await page.locator('lv-chat-page').evaluate(async (element: any) => {
      await element.updateComplete
      return {
        loading: element.shadowRoot.querySelector('.loading-state')?.textContent?.trim(),
        unavailable: element.shadowRoot.textContent?.includes('Agent unavailable'),
        thread: Boolean(element.shadowRoot.querySelector('lv-chat-thread')),
      }
    })
    expect(before).toEqual({ loading: 'Loading chat…', unavailable: false, thread: false })

    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({
        page: { kind: 'chat', view: 'conversation', title: 'Chats', description: '' },
        agent: {
          conversations: [{ id: 'c1', title: 'Revenue check', updatedAt: '2026-01-02T10:00:00Z' }],
          activeConversationId: 'c1', transcript: [{ id: 'ready', kind: 'assistant', markdown: 'Ready.', conversationId: 'c1' }],
          status: { enabled: true, running: false },
          composer: { value: '', disabled: false, placeholder: 'Ask about dashboards, metrics, or models...' },
        },
      })
    })
    await page.waitForFunction(() => {
      const root = document.querySelector('lv-chat-page')?.shadowRoot
      return root?.querySelector('lv-chat-thread') && !root.querySelector('.loading-state')
    })
    const after = await page.locator('lv-chat-page').evaluate(async (element: any) => {
      const thread = element.shadowRoot.querySelector('lv-chat-thread') as any
      await thread.updateComplete
      return {
        title: element.shadowRoot.querySelector('h1')?.textContent?.trim(),
        unavailable: thread.shadowRoot.textContent?.includes('Agent unavailable'),
        transcript: thread.transcript,
      }
    })
    expect(after.title).toBe('Revenue check')
    expect(after.unavailable).toBe(false)
    expect(after.transcript).toHaveLength(1)

    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: {
        transcript: [],
        status: { enabled: false, running: false, error: 'Agent is not configured.' },
        composer: { value: '', disabled: true, placeholder: 'Agent is not configured.' },
      } })
    })
    await page.waitForFunction(() => {
      const thread = document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-thread')
      return thread?.shadowRoot?.querySelector('.empty-title')?.textContent?.trim() === 'Agent unavailable'
    })
  } finally {
    await page.close()
  }
})

function testDocument(view = 'conversation', scenario: 'active' | 'new' = 'active', enabled = true, hydrated = true): string {
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
        <main ${hydrated ? `data-signals="${escapeHTML(JSON.stringify({ page, agent, visuals: {}, tables: {} }))}"` : ''}>
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
