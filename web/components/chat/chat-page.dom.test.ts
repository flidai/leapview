import { expect, test } from 'bun:test'
import type { Page } from '@playwright/test'
import { chatPageBrowserFixture, testDocument } from './chat-page-browser.test-fixture'

const fixture = chatPageBrowserFixture()

for (const viewport of [
  { name: 'desktop', width: 1280, height: 820 },
  { name: 'mobile', width: 390, height: 820 },
]) {
  test(`chat page composes route UI on ${viewport.name}`, async () => {
    const page = await fixture.browser.newPage({ viewport })
    try {
      await page.goto(fixture.baseURL)
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

test('chat donut panel keeps outside value labels and places the legend below the chart', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    const state = await page.evaluate(async () => {
      await customElements.whenDefined('lv-chat-visual-panel')
      const panel = document.createElement('lv-chat-visual-panel') as any
      panel.payload = {
        spec: { kind: 'proportional', mark: 'donut', titleVisible: true, presentation: { legend: 'right', labelPosition: 'outside' } },
      }
      document.body.append(panel)
      await panel.updateComplete
      const displayed = panel.shadowRoot.querySelector('lv-visual-artifact')?.payload
      panel.saving = true
      await panel.updateComplete
      return {
        savedTitleVisible: panel.payload.spec.titleVisible,
        displayedTitleVisible: displayed.spec.titleVisible,
        savedLegend: panel.payload.spec.presentation.legend,
        displayedLegend: panel.shadowRoot.querySelector('lv-visual-artifact')?.payload?.spec.presentation.legend,
        displayedLabels: panel.shadowRoot.querySelector('lv-visual-artifact')?.payload?.spec.presentation.labelPosition,
        stablePayload: displayed === panel.shadowRoot.querySelector('lv-visual-artifact')?.payload,
      }
    })
    expect(state).toEqual({ savedTitleVisible: true, displayedTitleVisible: false, savedLegend: 'right', displayedLegend: 'bottom', displayedLabels: 'outside', stablePayload: true })
  } finally {
    await page.close()
  }
})

test('chat visual panel preserves a hidden proportional legend', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    const legend = await page.evaluate(async () => {
      await customElements.whenDefined('lv-chat-visual-panel')
      const panel = document.createElement('lv-chat-visual-panel') as any
      panel.payload = { spec: { kind: 'proportional', mark: 'donut', presentation: { legend: 'hidden', labelPosition: 'outside' } } }
      document.body.append(panel)
      await panel.updateComplete
      return panel.shadowRoot.querySelector('lv-visual-artifact')?.payload?.spec.presentation.legend
    })
    expect(legend).toBe('hidden')
  } finally {
    await page.close()
  }
})

for (const viewport of [
  { name: 'desktop', width: 1280, height: 820, expectedSurfaceWidth: 760 },
  { name: 'narrow desktop', width: 700, height: 820, expectedSurfaceWidth: 668 },
  { name: 'mobile', width: 390, height: 820, expectedSurfaceWidth: 366 },
]) {
  test(`new chat page centers the title and composer on ${viewport.name}`, async () => {
    const page = await fixture.browser.newPage({ viewport })
    try {
      await page.goto(`${fixture.baseURL}/new`)
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
          { label: 'Build a dashboard', prompt: 'Build a complete dashboard from my selected data source, with key metrics, trends, comparisons, and useful filters. Arrange it clearly and open the preview.' },
          { label: 'Spot a change', prompt: 'What changed most in the last 30 days?' },
          { label: 'Explain a metric', prompt: 'Explain how revenue is calculated.' },
          { label: 'Review a dashboard', prompt: 'Summarize the Executive Sales dashboard.' },
        ],
        starterDraft: 'Build a complete dashboard from my selected data source, with key metrics, trends, comparisons, and useful filters. Arrange it clearly and open the preview.',
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${fixture.baseURL}/new`)
    await page.waitForFunction(() => customElements.get('lv-chat-page'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      mergePatch({ agent: { activeConversationId: 'c3' } })
    })
    await page.waitForURL(`${fixture.baseURL}/chats/c3`)
    expect(new URL(page.url()).pathname).toBe('/chats/c3')
  } finally {
    await page.close()
  }
})

test('new chat submits Enter and navigates from the command signal before the answer arrives', async () => {
  fixture.draftTurnRequests = 0
  fixture.draftTurnAnswerSent = false
  fixture.draftTurnAnswerFinished = false
  fixture.releaseDraftTurnAnswer = null
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${fixture.baseURL}/new`)
    await page.waitForFunction(() => customElements.get('lv-chat-page') && customElements.get('lv-chat-composer'))
    await page.locator('lv-chat-page').evaluate(async (element: any) => {
      await element.updateComplete
      const composer = element.shadowRoot.querySelector('lv-chat-composer') as any
      await composer.updateComplete
    })

    const textarea = page.locator('lv-chat-page').locator('lv-chat-composer').locator('textarea')
    await textarea.fill('What changed most recently?')
    await textarea.press('Enter')

    await page.waitForURL(`${fixture.baseURL}/chats/c3`)
    expect(fixture.draftTurnRequests).toBe(1)
    expect(fixture.draftTurnAnswerSent).toBe(false)
    expect(new URL(page.url()).pathname).toBe('/chats/c3')
  } finally {
    const release = fixture.releaseDraftTurnAnswer as (() => void) | null
    release?.()
    for (let attempt = 0; attempt < 50 && !fixture.draftTurnAnswerFinished; attempt += 1) {
      await new Promise<void>((resolve) => setTimeout(resolve, 10))
    }
    await page.close()
  }
})

test('active chat shows a submitted turn immediately and replaces it with durable state', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${fixture.baseURL}/list`)
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
  const page = await fixture.browser.newPage({ viewport: { width: 1600, height: 900 } })
  try {
    await page.goto(`${fixture.baseURL}/list`)
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${fixture.baseURL}/list`)
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${fixture.baseURL}/list`)
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${fixture.baseURL}/list`)
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
      const menuItems = Array.from(menu.querySelectorAll('[role="menuitem"]')).map((item) => item.textContent?.trim())
      trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true }))
      const initialArrowFocus = (root.activeElement as HTMLElement)?.textContent?.trim()
      const rename = menu.querySelector<HTMLButtonElement>('[role="menuitem"]:first-of-type')!
      rename.focus()
      rename.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true }))
      const arrowFocus = (root.activeElement as HTMLElement)?.textContent?.trim()
      rename.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true }))
      const escaped = { open: menu.matches(':popover-open'), focus: (root.activeElement as HTMLElement)?.getAttribute('aria-label') }
      trigger.click()
      await list.updateComplete
      const reopenedMenu = row.querySelector<HTMLElement>('.options-panel')!
      reopenedMenu.querySelector<HTMLButtonElement>('[role="menuitem"]:first-of-type')!.click()
      return { menuItems, initialArrowFocus, arrowFocus, escaped, actions }
    })
    expect(state.menuItems).toEqual(['Rename', 'Delete chat'])
    expect(state.initialArrowFocus).toBe('Rename')
    expect(state.arrowFocus).toBe('Delete chat')
    expect(state.escaped).toEqual({ open: false, focus: 'More actions for Revenue check' })
    expect(state.actions).toEqual([{ action: 'rename', conversationId: 'c1', title: 'Revenue check', href: '/chats/c1' }])
  } finally {
    await page.close()
  }
})

test('last chat row menu stays visible and keeps delete accessible', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 390, height: 300 } })
  try {
    await page.goto(`${fixture.baseURL}/list`)
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${fixture.baseURL}/unavailable-new`)
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
      starterCount: 4,
      startersDisabled: true,
      composerDisabled: true,
      placeholder: 'Agent is not configured.',
    })

    await page.goto(`${fixture.baseURL}/unavailable-list`)
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${fixture.baseURL}/unhydrated`)
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

async function openDashboardTestVisual(page: Page): Promise<void> {
  await page.goto(`${fixture.baseURL}/chats/c1`)
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

for (const createNew of [false, true]) {
  test(`chat adds a visual to ${createNew ? 'a new' : 'an existing'} dashboard and offers another visual`, async () => {
    const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
    const requests: { body: any; csrf: string | undefined; key: string | undefined }[] = []
    try {
      await page.route('**/chats/c1/visuals/chart-dashboard/dashboards', async route => {
        if (route.request().method() === 'GET') {
          await route.fulfill({ json: { dashboards: [{ id: 'dashboard:finance', title: 'Finance', pages: [{ id: 'overview', title: 'Overview' }, { id: 'details', title: 'Details' }] }], canCreate: true } })
        } else {
          const headers = route.request().headers()
          requests.push({ body: route.request().postDataJSON(), csrf: headers['x-csrf-token'], key: headers['idempotency-key'] })
          await route.fulfill({ json: { dashboardId: createNew ? 'dashboard:new' : 'dashboard:finance', title: createNew ? 'CFO review' : 'Finance', componentId: 'imported-chart-component', pageId: createNew ? 'overview' : 'details', href: `/dashboards/dashboard:finance/edit?page=${createNew ? 'overview' : 'details'}&returnChat=c1` } })
        }
      })
      await openDashboardTestVisual(page)
      const picker = page.locator('lv-chat-dashboard-picker')
      await picker.getByRole('radio', { name: 'Finance', exact: true }).waitFor()
      if (createNew) {
        await picker.getByRole('radio', { name: 'New dashboard', exact: true }).check()
        await picker.getByRole('textbox', { name: 'Dashboard name' }).fill('CFO review')
      } else {
        await picker.getByLabel('Page', { exact: true }).selectOption('details')
      }
      await picker.getByRole('button', { name: createNew ? 'Create dashboard and add' : 'Add visual', exact: true }).click()
      await picker.getByRole('heading', { name: 'Visual added' }).waitFor()
      expect(requests).toHaveLength(1)
      expect(requests[0].body).toEqual(createNew ? { title: 'CFO review' } : { dashboardId: 'dashboard:finance', pageId: 'details' })
      expect(requests[0].csrf).toBe('test-csrf')
      expect(requests[0].key).toMatch(/^[0-9a-f-]{14}7[0-9a-f-]{21}$/)
      expect(await picker.getByRole('link', { name: 'Open dashboard' }).getAttribute('href')).toBe(`/dashboards/dashboard:finance/edit?page=${createNew ? 'overview' : 'details'}&returnChat=c1`)
      const membership = await page.locator('lv-chat-page').evaluate((chat: any) => ({ pageId: chat.dashboardPageId, copy: chat.dashboardCopies['chart-dashboard'], href: chat.savedBuilderHref }))
      expect(membership.pageId).toBe(createNew ? 'overview' : 'details')
      expect(membership.copy).toEqual({ id: 'imported-chart-component', pageId: createNew ? 'overview' : 'details' })
      expect(membership.href).toContain(`page=${createNew ? 'overview' : 'details'}`)
      if (!createNew) {
        await picker.getByRole('button', { name: 'Done', exact: true }).click()
        await page.getByRole('button', { name: 'Remove from dashboard', exact: true }).waitFor()
        expect(await page.getByRole('button', { name: 'Add to dashboard', exact: true }).count()).toBe(0)
        expect(await page.locator('lv-chat-visual-panel').count()).toBe(0)
        return
      }
      await page.locator('lv-chat-composer').evaluate((element: any) => element.setDraft('Show margin by product', false))
      await picker.getByRole('button', { name: 'Add another visual' }).click()
      await page.locator('div.dashboard-destination').waitFor()
      expect(await page.locator('lv-chat-composer').getByRole('combobox').inputValue()).toBe('Show margin by product')
      expect(await page.locator('lv-chat-composer').getByRole('combobox').evaluate(element => element.getRootNode() instanceof ShadowRoot && (element.getRootNode() as ShadowRoot).activeElement === element)).toBe(true)
      expect(await page.locator('lv-chat-visual-panel').count()).toBe(0)
    } finally { await page.close() }
  })
}

test('dashboard save retries preserve the command identity and keep failures in the picker', async () => {
  const page = await fixture.browser.newPage()
  const keys: string[] = []
  try {
    await page.route('**/chats/c1/visuals/chart-dashboard/dashboards', async route => {
      if (route.request().method() === 'GET') {
        await route.fulfill({ json: { dashboards: [], canCreate: true } })
      } else {
        keys.push(route.request().headers()['idempotency-key'])
        await route.fulfill(keys.length === 1 ? { status: 503, json: {} } : { json: { dashboardId: 'dashboard:new', title: 'CFO review', href: '/dashboards/dashboard:new/edit', pageId: 'overview' } })
      }
    })
    await openDashboardTestVisual(page)
    const picker = page.locator('lv-chat-dashboard-picker')
    await picker.getByRole('textbox', { name: 'Dashboard name' }).fill('CFO review')
    await picker.getByRole('button', { name: 'Create dashboard and add' }).click()
    await picker.getByRole('alert').waitFor()
    expect(await picker.getByRole('heading', { name: 'Visual added' }).count()).toBe(0)
    await picker.getByRole('button', { name: 'Create dashboard and add' }).click()
    await picker.getByRole('heading', { name: 'Visual added' }).waitFor()
    expect(keys).toHaveLength(2)
    expect(keys[1]).toBe(keys[0])
  } finally { await page.close() }
})


test('dashboard search cannot submit a destination hidden by the filter', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.route('**/chats/c1/visuals/chart-dashboard/dashboards', route => route.fulfill({ json: {
      dashboards: Array.from({ length: 7 }, (_, i) => ({ id: `dashboard:${i}`, title: `Finance ${i}`, pages: [{ id: 'overview', title: 'Overview' }] })), canCreate: true,
    } }))
    await openDashboardTestVisual(page)
    const picker = page.locator('lv-chat-dashboard-picker')
    await picker.getByRole('searchbox', { name: 'Find a dashboard' }).fill('Finance 6')
    expect(await picker.getByRole('button', { name: 'Add visual', exact: true }).isDisabled()).toBe(true)
    await picker.getByRole('radio', { name: 'Finance 6', exact: true }).check()
    expect(await picker.getByRole('button', { name: 'Add visual', exact: true }).isEnabled()).toBe(true)
  } finally { await page.close() }
})

test('preview chat keeps expansion controls without dashboard actions', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      e.dashboardPreview = true
      e.builderOpen = true
      await e.updateComplete
      ;(window as any).retainedBuilder = e.shadowRoot.querySelector('.builder-frame')
    })
    expect(await chat.getByRole('button', { name: 'Chart assist', exact: true }).count()).toBe(0)
    expect(await chat.getByRole('button', { name: 'Visual magic', exact: true }).count()).toBe(0)
    await page.getByRole('button', { name: 'Expand chat', exact: true }).click()
    expect(await chat.evaluate((e: any) => e.builderOpen)).toBe(false)
    expect(await chat.evaluate((e: any) => e.shadowRoot.querySelector('.builder-frame') === (window as any).retainedBuilder)).toBe(true)
  } finally { await page.close() }
})

test('dashboard membership follows Preview, delete, Undo, and saved-library imports', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { transcript: [{ kind: 'tool', name: 'query_visual', status: 'complete', artifact: { id: 'chart-one', type: 'bar', summary: 'Revenue' } }] } })
      e.dashboardPreview = true
      e.pendingPreviewArtifacts = ['chart-one']
      await e.updateComplete
    })
    const project = async (components: Array<{id: string; pageId: string; savedVisualId?: string}>) => {
      await page.frameLocator('.builder-frame').locator('body').evaluate((_, components) => {
        window.parent.postMessage({ type: 'lv-builder-saved', revisionId: 'rev', pageId: 'overview', href: '/dashboards/demo/edit', components, artifacts: [], visuals: {},
          reference: { reference: { kind: 'dashboard', id: 'demo' }, name: 'Demo', hierarchy: [], href: '/dashboards/demo/edit', locations: [], context: [] },
        }, window.parent.location.origin)
      }, components)
      await page.waitForTimeout(50)
    }
    await project([])
    expect(await chat.evaluate((e: any) => e.pendingPreviewArtifacts)).toEqual(['chart-one'])
    await project([{id: 'visual_1', pageId: 'overview'}])
    expect(await chat.getByRole('button', {name: 'Remove from dashboard', exact: true}).count()).toBe(1)
    await project([])
    expect(await chat.getByRole('button', {name: 'Add to dashboard', exact: true}).count()).toBe(1)
    await project([{id: 'visual_1', pageId: 'overview'}])
    expect(await chat.getByRole('button', {name: 'Remove from dashboard', exact: true}).count()).toBe(1)
    await project([{id: 'saved_import', pageId: 'another-page', savedVisualId: 'saved-one'}])
    await chat.evaluate(async (e: any) => {
      e.handleVisualLibraryState(new CustomEvent('lv-visual-library-state', {detail: {savedIds: ['chart-one'], libraryIds: {'chart-one': 'saved-one'}, savingId: '', error: ''}}))
      await e.updateComplete
    })
    expect(await chat.getByRole('button', {name: 'Add to dashboard', exact: true}).count()).toBe(1)
    expect(await chat.evaluate((e: any) => e.dashboardCopies['chart-one'])).toBeUndefined()
    await project([{id: 'saved_import', pageId: 'another-page', savedVisualId: 'saved-one'}, {id: 'second_copy', pageId: 'overview', savedVisualId: 'saved-one'}])
    await project([{id: 'second_copy', pageId: 'overview', savedVisualId: 'saved-one'}])
    expect(await chat.getByRole('button', {name: 'Remove from dashboard', exact: true}).count()).toBe(1)
    await project([])
    expect(await chat.getByRole('button', {name: 'Add to dashboard', exact: true}).count()).toBe(1)
    // A new tab has no history links; the authored source identity still matches.
    await chat.evaluate((e: any) => {
      e.dashboardCopyLinks = {}
      e.visualLibraryState = {savedIds:['chart-one'],libraryIds:{'chart-one':'11111111-1111-1111-1111-111111111111'},savingId:'',error:''}
    })
    await project([{id: 'saved_11111111111111111111111111111111_22222222222222222222222222222222',pageId:'overview'}])
    expect(await chat.getByRole('button', {name:'Remove from dashboard',exact:true}).count()).toBe(1)
  } finally { await page.close() }
})

test('reload restores the same dashboard draft and page without creating another dashboard', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.route('**/dashboards/demo/edit**', route => route.fulfill({ contentType: 'text/html', body: '<p>Saved dashboard draft</p>' }))
    await page.goto(`${fixture.baseURL}/chats/c1?preview=builder`)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      e.dashboardPreview = true
      e.builderOpen = true
      e.pendingPreviewArtifacts = ['chart-one']
      await e.updateComplete
    })
    await page.frameLocator('.builder-frame').locator('body').evaluate(() => {
      window.parent.postMessage({
        type: 'lv-builder-saved', revisionId: 'rev-2', pageId: 'details',
        href: '/dashboards/demo/edit?embed=chat&draft=draft-1&page=details',
        reference: { reference: { kind: 'dashboard', id: 'demo' }, name: 'Demo', hierarchy: [], locations: [], context: [] },
        components: [{id: 'visual_1', pageId: 'details'}], artifacts: [], visuals: {},
      }, window.parent.location.origin)
    })
    await page.waitForFunction(() => (document.querySelector('lv-chat-page') as any)?.savedBuilderHref.includes('demo'))
    expect(new URL(page.url()).searchParams.get('dashboard')).toBe('/dashboards/demo/edit?embed=chat&draft=draft-1&page=details')
    await page.reload()
    await page.getByRole('button', { name: 'Expand chat', exact: true }).waitFor()
    await page.frameLocator('.builder-frame').getByText('Saved dashboard draft').waitFor()
    expect(await chat.evaluate((e: any) => e.dashboardCopyLinks['chart-one'])).toEqual([{id: 'visual_1', pageId: 'details'}])
    await page.getByRole('button', {name: 'Expand chat', exact: true}).click()
    await page.reload()
    expect(await chat.evaluate((e: any) => e.builderOpen)).toBe(false)
    expect(await chat.evaluate((e: any) => e.savedBuilderHref)).toContain('/dashboards/demo/edit')
  } finally { await page.close() }
})

test('preview restoration rejects external and non-builder destinations', async () => {
  const page = await fixture.browser.newPage()
  try {
    for (const href of ['https://example.com/dashboards/demo/edit', '/logout', '/dashboards/demo/delete']) {
      await page.goto(`${fixture.baseURL}/chats/c1?preview=builder&dashboard=${encodeURIComponent(href)}`)
      await page.locator('lv-chat-composer').waitFor()
      expect(await page.locator('lv-chat-page').evaluate((e: any) => e.savedBuilderHref)).toBe('')
      expect(await page.locator('.builder-frame').getAttribute('src')).toBe(null)
    }
  } finally { await page.close() }
})


test('Preview creates an empty draft instead of implicitly adding every chat visual', async () => {
  const page = await fixture.browser.newPage()
  try {
    let submitted = ''
    await page.route('**/dashboards/new', async route => { submitted = route.request().postData() ?? ''; await route.fulfill({contentType:'text/html',body:'<lv-dashboard-builder></lv-dashboard-builder>'}) })
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      const {mergePatch} = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({agent:{transcript:[{kind:'tool',status:'complete',artifact:{id:'new-chart',type:'bar'},argumentsJson:JSON.stringify({semanticModelId:'semantic-model:sales',visual:{type:'bar'}})}]}})
      await e.updateComplete
      await e.saveDashboard(true)
    })
    await page.waitForTimeout(200)
    const form = new URLSearchParams(submitted)
    expect(form.get('semanticModel')).toBe('semantic-model:sales')
    expect(form.get('embed')).toBe('chat')
    expect(form.has('chatVisuals')).toBe(false)
  } finally {await page.close()}
})

test('preview chat follows the selected page and targets visual imports there', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {e.dashboardPreview=true;e.builderOpen=true;await e.updateComplete})
    const project = async (id: string, title: string) => {
      await page.frameLocator('.builder-frame').locator('body').evaluate((_, selected) => {
        window.parent.postMessage({type:'lv-builder-saved',revisionId:'revision-1',pageId:selected.id,pageTitle:selected.title,modelId:'semantic-model:sales',href:`/dashboards/demo/edit?embed=chat&page=${selected.id}`,
          reference:{reference:{kind:'dashboard',id:'demo'},name:`Sales · ${selected.title}`,hierarchy:[],locations:[],context:[]},components:[],artifacts:[],visuals:{}},window.parent.location.origin)
      },{id,title})
      await page.waitForFunction(id=>(document.querySelector('lv-chat-page') as any)?.context?.pageId===id,id)
    }
    await project('overview','Overview')
    await project('pies','Pie charts')
    expect(await chat.evaluate((e: any)=>e.context.dashboardId)).toBe('demo')
    expect(await chat.evaluate((e: any)=>e.references.filter((r: any)=>r.reference.id==='demo').map((r: any)=>r.name))).toEqual(['Sales · Pie charts'])
    expect(await chat.locator('.chat-pane-heading').innerText()).toContain('Pie charts')
    await page.getByRole('button',{name:'Expand chat',exact:true}).click()
    let posted = ''
    await page.route('**/dashboards/demo/draft/saved-visual', async route=>{posted=route.request().postData()??'';await route.fulfill({contentType:'text/html',body:'<div id="chat-dashboard-receipt"></div>'})})
    await chat.evaluate(async(e: any)=>e.addAgentVisual(new CustomEvent('lv-add-agent-visual',{detail:{savedId:'11111111-1111-1111-1111-111111111111',artifactId:'pie'}})))
    await page.waitForTimeout(200)
    expect(new URLSearchParams(posted).get('pageId')).toBe('pies')
    expect(new URL(new URL(page.url()).searchParams.get('dashboard')!,fixture.baseURL).searchParams.get('page')).toBe('pies')
  } finally {await page.close()}
})

test('preview header stays aligned and visual membership is scoped to the chosen page', async () => {
  const page = await fixture.browser.newPage({viewport: {width: 1400, height: 900}})
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      e.dashboardPreview = true; e.builderOpen = true
      e.dashboardPageId = 'pies'; e.dashboardPageTitle = 'Pie charts with a long name'; e.savedBuilderHref='/dashboards/demo/edit?embed=chat&page=pies'
      e.dashboardPages = [{id:'overview',title:'Overview'},{id:'pies',title:'Pie charts with a long name'}]
      e.fixVisualsMessage = 'Completed 1 visual. Your layout is unchanged.'
      e.dashboardCopyLinks = {one:[{id:'first',pageId:'overview'},{id:'second',pageId:'pies'}]}
      e.dashboardComponents = [{id:'first',pageId:'overview'},{id:'second',pageId:'pies'}]
      e.reconcileDashboardCopies(); await e.updateComplete
    })
    expect(await chat.evaluate((e: any)=>e.dashboardCopies.one)).toMatchObject({id:'second',pageId:'pies'})
    const heading = await chat.locator('.chat-pane-heading').boundingBox()
    const expand = await page.getByRole('button',{name:'Expand chat',exact:true}).boundingBox()
    expect(Math.abs(heading!.y + heading!.height / 2 - expand!.y - expand!.height / 2)).toBeLessThan(2)
    await chat.evaluate(async(e: any)=>{e.dashboardComponents=[{id:'first',pageId:'overview'}];e.reconcileDashboardCopies();await e.updateComplete})
    expect(await chat.evaluate((e: any)=>e.dashboardCopies.one)).toBeUndefined()
    await page.getByRole('button',{name:'Expand chat',exact:true}).click()
    expect((await chat.locator('.dashboard-destination').boundingBox())!.height).toBeLessThan(65)
  } finally {await page.close()}
})

test('removing a visual keeps the live builder mounted and sends its selected-page command', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async(e: any)=>{
      e.dashboardPreview=true; await e.updateComplete
      const frame=e.shadowRoot.querySelector('.builder-frame') as HTMLIFrameElement
      frame.contentDocument!.body.innerHTML='<lv-dashboard-builder></lv-dashboard-builder>'
      ;(window as any).builderDocument=frame.contentDocument
      ;(window as any).builderCommands=[]
      frame.contentWindow!.addEventListener('message',event=>(window as any).builderCommands.push(event.data))
      e.savedBuilderHref='/dashboards/demo/edit?embed=chat&page=pies';e.dashboardPageId='pies'
      e.dashboardCopies={one:{id:'second',pageId:'pies'}}
      e.toggleDashboardVisual('one')
    })
    await page.waitForFunction(()=>(window as any).builderCommands.length>0,{},{timeout:2000})
    expect(await page.evaluate(()=>(window as any).builderCommands[0])).toEqual({type:'lv-remove-dashboard-visual',pageId:'pies',componentId:'second'})
    expect(await chat.evaluate((e: any)=>e.shadowRoot.querySelector('.builder-frame').contentDocument===(window as any).builderDocument)).toBe(true)
    await chat.evaluate(async(e: any)=>{
      e.savingDashboard=false; e.dashboardCopies={}; e.dashboardPages=[{id:'pies',title:'Pie charts'},{id:'overview',title:'Overview'}]
      await e.addAgentVisual(new CustomEvent('lv-add-agent-visual',{detail:{savedId:'11111111-1111-1111-1111-111111111111',artifactId:'new-chart'}}))
    })
    await page.waitForFunction(()=>(window as any).builderCommands.length===2)
    expect(await page.evaluate(()=>(window as any).builderCommands[1])).toMatchObject({type:'lv-add-saved-visual',pageId:'pies',id:'11111111-1111-1111-1111-111111111111'})
    expect(await chat.evaluate((e: any)=>e.shadowRoot.querySelector('.builder-frame').contentDocument===(window as any).builderDocument)).toBe(true)
    await chat.evaluate(async(e: any)=>{e.savingDashboard=false; e.selectDashboardPage({target:{value:'overview'}});await e.updateComplete})
    await page.waitForFunction(()=>(window as any).builderCommands.length===3)
    expect(await page.evaluate(()=>(window as any).builderCommands[2])).toEqual({type:'lv-select-dashboard-page',pageId:'overview'})
  } finally {await page.close()}
})

test('side agent exposes dashboard-authored visuals in the individual side view', async () => {
 const page=await fixture.browser.newPage()
 try {
  await page.goto(fixture.baseURL)
  const chat=page.locator('lv-chat-page');await chat.locator('lv-chat-composer').waitFor()
  await chat.evaluate(async(e: any)=>{
   e.dashboardPreview=true;e.builderOpen=true;await e.updateComplete
  })
  await page.frameLocator('.builder-frame').locator('body').evaluate(()=>{
   window.parent.postMessage({type:'lv-builder-saved',revisionId:'rev-pie',pageId:'pies',pageTitle:'Pie charts',pages:[{id:'overview',title:'Overview'},{id:'pies',title:'Pie charts'}],href:'/dashboards/demo/edit?embed=chat&page=pies',reference:{reference:{kind:'dashboard',id:'demo'},name:'Demo',hierarchy:[],locations:[],context:[]},components:[{id:'component-pie',pageId:'pies',artifactId:'authored-pie'}],artifacts:[{id:'authored-pie',type:'pie',summary:'Agent pie chart'}],visuals:{}},window.parent.location.origin)
  })
  await page.getByRole('button',{name:'Open Agent pie chart in visuals sidebar',exact:true}).click()
  expect(await chat.evaluate((e: any)=>({builderOpen:e.builderOpen,selected:e.selectedPreviewVisual,page:e.dashboardPageId}))).toEqual({builderOpen:false,selected:'authored-pie',page:'pies'})
  expect(await page.getByRole('button',{name:'Remove from dashboard',exact:true}).isVisible()).toBe(true)
  expect(await chat.locator('.preview-card:not([hidden])').getAttribute('data-preview-visual')).toBe('authored-pie')
  expect(await page.getByText('Loading visual…',{exact:true}).isVisible()).toBe(true)
  expect(await page.getByRole('combobox',{name:'Dashboard page',exact:true}).inputValue()).toBe('pies')
  expect(await page.getByTitle('Save visual',{exact:true}).count()).toBe(0)
  expect(await page.getByTitle('Unsave visual',{exact:true}).count()).toBe(0)
  await page.getByRole('button',{name:'Open in Builder',exact:true}).click()
  expect(await chat.evaluate((e: any)=>({builderOpen:e.builderOpen,page:e.dashboardPageId}))).toEqual({builderOpen:true,page:'pies'})
 } finally {await page.close()}
})

test('submission distinguishes builder authoring from expanded main chat', async () => {
 const page = await fixture.browser.newPage()
 try {
  await page.goto(fixture.baseURL)
  const chat = page.locator('lv-chat-page')
  await chat.locator('lv-chat-composer').waitFor()
  const surfaces = await chat.evaluate(async (e: any) => {
   const surfaces: string[] = []
   e.addEventListener('lv-chat-submit', (event: CustomEvent) => surfaces.push(event.detail.surface))
   for (const builderOpen of [true, false]) {
    e.builderOpen = builderOpen
    await e.updateComplete
    e.shadowRoot.querySelector('.route').dispatchEvent(new CustomEvent('lv-chat-submit', {bubbles:true,composed:true,detail:{input:'Create a bar chart',references:[]}}))
   }
   return surfaces
  })
  expect(surfaces).toEqual(['builder', 'chat'])
 } finally { await page.close() }
})

for (const terminalError of ['', 'The final reply exceeded the conversation limit.']) test(`a live full-dashboard run opens its successful preview automatically and only once (${terminalError || 'success'})`, async () => {
 const page=await fixture.browser.newPage()
 try {
  await page.route('**/chats/*/actions/*/open?*', route=>route.fulfill({contentType:'text/html',body:'<lv-dashboard-builder>Generated dashboard</lv-dashboard-builder>'}))
  await page.goto(fixture.baseURL)
  const chat=page.locator('lv-chat-page')
  await chat.locator('lv-chat-composer').waitFor()
  await chat.evaluate(async (e:any)=>{
   const {mergePatch}=await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
   mergePatch({agent:{status:{enabled:true,running:true,runId:'full-build'}}})
   await e.updateComplete
  })
  await page.waitForFunction(()=>(document.querySelector('lv-chat-page') as any).dashboardGenerationRun==='full-build')
  // Terminal status may arrive before the last transcript projection.
  await chat.evaluate(async (e:any, error:string)=>{
   const {mergePatch}=await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
   mergePatch({agent:{status:{enabled:true,running:false,runId:'full-build',error}}})
   await e.updateComplete
  }, terminalError)
  expect(await chat.evaluate((e:any)=>e.builderOpen)).toBe(false)
  await chat.evaluate(async (e:any)=>{
   const {mergePatch}=await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
   mergePatch({agent:{transcript:['create_dashboard_draft','edit_dashboard_source','preview_dashboard_draft'].map(name=>({id:name,kind:'tool',name,runId:'full-build',toolCallId:name,status:'complete'}))}})
   await e.updateComplete
  })
  await page.waitForFunction(()=>(document.querySelector('lv-chat-page') as any).builderOpen)
  await page.frameLocator('.builder-frame').getByText('Generated dashboard').waitFor()
  expect(await chat.locator('.builder-frame').getAttribute('src')).toContain('embed=chat')
  await page.getByRole('button',{name:'Expand chat',exact:true}).click()
  await chat.evaluate(async (e:any)=>{e.requestUpdate();await e.updateComplete})
  expect(await chat.evaluate((e:any)=>e.builderOpen)).toBe(false)
 }finally{await page.close()}
})
