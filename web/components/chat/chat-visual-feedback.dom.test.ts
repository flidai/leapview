import { expect, test } from 'bun:test'
import { chatPageBrowserFixture, testDocument } from './chat-page-browser.test-fixture'

const fixture = chatPageBrowserFixture()

test('Saved visuals follows the account library, including other conversations and final removal', async () => {
  const page = await fixture.browser.newPage()
  try {
    let count = 0
    await page.route('**/visuals/saved', route => route.fulfill({ contentType: 'text/html', body: `<lv-saved-visual-library></lv-saved-visual-library><script>parent.postMessage({type:'lv-saved-visual-library',library:{visuals:${JSON.stringify(Array.from({length: count}, () => ({id:'saved',sourceKey:'other/chart',title:'Revenue'})))}}},location.origin)</script>` }))
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    expect(await chat.getByRole('link', { name: 'Saved visuals', exact: true }).count()).toBe(0)
    count = 1
    await page.reload()
    await chat.getByRole('link', { name: 'Saved visuals', exact: true }).waitFor()
    expect(await chat.evaluate((e: any) => e.visualLibraryState.savedIds)).toEqual([])
    await chat.evaluate(async (e: any) => { e.savedBuilderHref = '/dashboards/sales/edit'; await e.updateComplete })
    const preview = await chat.getByRole('button', { name: 'Preview dashboard', exact: true }).boundingBox()
    const saved = await chat.getByRole('link', { name: 'Saved visuals', exact: true }).boundingBox()
    expect(saved?.height).toBe(preview?.height)
    expect(saved?.y).toBe(preview?.y)
    count = 0
    await chat.evaluate((e: any) => {
      const controller = e.shadowRoot.querySelector('lv-agent-visual-library')
      const frame = controller.shadowRoot.querySelector('iframe')
      frame.contentWindow.location.reload()
    })
    await chat.getByRole('link', { name: 'Saved visuals', exact: true }).waitFor({ state: 'detached' })
  } finally { await page.close() }
})

for (const route of ['', '/new']) test(`dashboard submit opens assembly before any agent tools (${route || 'conversation'})`, async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(`${fixture.baseURL}${route}`)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    const state = await chat.evaluate(async (e: any) => {
      const event = new CustomEvent<{ input: string; references: never[]; surface?: string }>('lv-chat-submit', { bubbles: true, composed: true, detail: { input: 'Build a complete sales dashboard', references: [] } })
      e.showOptimisticTurn(event)
      await e.updateComplete
      return { builderOpen: e.builderOpen, surface: event.detail.surface, visible: !e.shadowRoot.querySelector('.builder-stage').hidden, animation: Boolean(e.shadowRoot.querySelector('lv-dashboard-generation')) }
    })
    expect(state).toEqual({ builderOpen: true, surface: 'chat', visible: true, animation: true })
    expect(await chat.locator('.builder-frame').isVisible()).toBe(false)
  } finally { await page.close() }
})

test('ordinary chat prompts keep the conversation layout', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    expect(await chat.evaluate(async (e: any) => {
      e.showOptimisticTurn(new CustomEvent('lv-chat-submit', { detail: { input: 'Explain revenue', references: [] } }))
      await e.updateComplete
      return e.builderOpen
    })).toBe(false)
  } finally { await page.close() }
})

test('individual preview has a complete frame and times out missing payloads with retry', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 900 } })
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await page.clock.install()
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { transcript: [{ id: 'missing', kind: 'tool', name: 'query_visual', status: 'complete', artifact: { id: 'missing', type: 'bar', summary: 'Revenue by category' } }] } })
      await e.updateComplete
      e.openDashboardPreview()
      await e.updateComplete
    })
    const borders = await chat.locator('.preview-panel').evaluate(element => {
      const style = getComputedStyle(element)
      return [style.borderTopWidth, style.borderRightWidth, style.borderBottomWidth, style.borderLeftWidth]
    })
    expect(borders).toEqual(['1px','1px','1px','1px'])
    await page.clock.fastForward(16000)
    await chat.getByRole('button', { name: 'Retry visual', exact: true }).waitFor()
    const reloaded = page.waitForNavigation()
    await chat.getByRole('button', { name: 'Retry visual', exact: true }).click()
    await reloaded
    await chat.locator('lv-chat-composer').waitFor()
    expect(await chat.getByText('Loading visual…', { exact: true }).count()).toBe(0)
  } finally { await page.close() }
})

test('confirmed current-run authoring opens assembly and a conversation switch clears it', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { status: { enabled: true, running: true, runId: 'new-run' }, transcript: [
        { id: 'user', kind: 'user', text: 'Give me an overview of sales' },
        { id: 'old', kind: 'tool', name: 'create_dashboard_draft', runId: 'old-run', status: 'complete' },
      ] } })
      await e.updateComplete
    })
    expect(await chat.evaluate((e: any) => e.builderOpen)).toBe(false)
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { transcript: [
        { id: 'user', kind: 'user', text: 'Give me an overview of sales' },
        { id: 'current', kind: 'tool', name: 'create_dashboard_draft', runId: 'new-run', status: 'running' },
      ] } })
      await e.updateComplete
    })
    expect(await chat.evaluate((e: any) => e.builderOpen)).toBe(true)
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { activeConversationId: 'c2', transcript: [], status: { enabled: true, running: false } } })
      await e.updateComplete
    })
    expect(await chat.evaluate((e: any) => ({ builder: e.builderOpen, preview: e.dashboardPreview }))).toEqual({ builder: false, preview: false })
  } finally { await page.close() }
})

test('a later ordinary run clears dashboard assembly progress in the same conversation', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    const state = await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      e.showOptimisticTurn(new CustomEvent('lv-chat-submit', { detail: { input: 'Build a sales dashboard', references: [] } }))
      mergePatch({ agent: { transcript: [{ id: 'build-user', kind: 'user', text: 'Build a sales dashboard' }], status: { enabled: true, running: false, runId: 'build-run' } } })
      await e.updateComplete
      e.showOptimisticTurn(new CustomEvent('lv-chat-submit', { detail: { input: 'Explain revenue', references: [] } }))
      mergePatch({ agent: { transcript: [{ id: 'explain-user', kind: 'user', text: 'Explain revenue' }], status: { enabled: true, running: true, runId: 'explain-run' } } })
      await e.updateComplete
      await e.updateComplete
      const thread = e.shadowRoot.querySelector('lv-chat-thread')
      return { prompt: e.generationPrompt, assembly: Boolean(e.shadowRoot.querySelector('lv-dashboard-generation')), dashboardGenerating: thread.dashboardGenerating, builder: e.builderOpen, preview: e.dashboardPreview, frameVisible: !e.shadowRoot.querySelector('.builder-stage').hidden, previewParam: new URL(location.href).searchParams.get('preview') }
    })
    expect(state).toEqual({ prompt: '', assembly: false, dashboardGenerating: false, builder: false, preview: false, frameVisible: false, previewParam: null })
  } finally { await page.close() }
})

// Keep the production shell's command bridge and indicator in these transport
// regressions: invoking showOptimisticTurn alone misses terminal HTTP failures.
function commandDocument(scenario: 'active' | 'new'): string {
  const command = `$agent.status.error = ''; $agent.composer.value = evt.detail.input; $agent.composer.editMessageId = evt.detail.editMessageId || ''; $agentContext.surface = evt.detail.surface || 'chat'; $agentContext.references = evt.detail.references; @post('/chats/turns', {retry: 'never', retryMaxCount: 0, openWhenHidden: true})`
  return testDocument(scenario === 'new' ? 'new' : 'conversation', scenario)
    .replace(/ data-on:lv-chat-submit="[^"]*"/, '')
    .replace('<lv-chat-page', `<lv-chat-page data-indicator="agentTurnPending" data-on:lv-chat-submit="${command}"`)
}

for (const scenario of ['active', 'new'] as const) test(`real ${scenario} dashboard creation command keeps chat context and assembly progress`, async () => {
  const page = await fixture.browser.newPage()
  try {
    let request: any
    await page.route(fixture.baseURL + '/', route => route.fulfill({ contentType: 'text/html', body: commandDocument(scenario) }))
    await page.route('**/chats/turns', route => {
      request = JSON.parse(route.request().postData()!)
      return route.fulfill({ contentType: 'text/event-stream', body: 'event: datastar-patch-signals\ndata: signals {"agent":{"status":{"enabled":true,"running":true,"runId":"accepted-build"}}}\n\n' })
    })
    await page.goto(fixture.baseURL)
    const textarea = page.locator('lv-chat-composer textarea')
    await textarea.fill('Build a sales dashboard')
    await textarea.press('Enter')
    await page.waitForFunction(() => (document.querySelector('lv-chat-page') as any).agent.status.runId === 'accepted-build')
    expect(request.agentContext).toMatchObject({ surface: 'chat', references: [] })
    expect(request.agentContext.dashboardId ?? '').toBe('')
    expect(request.agentContext.pageId ?? '').toBe('')
    expect(await page.locator('lv-dashboard-generation').isVisible()).toBe(true)
    expect(await textarea.isDisabled()).toBe(true)
  } finally { await page.close() }
})

test('a selected dashboard builder command preserves its scoped builder context', async () => {
  const page = await fixture.browser.newPage()
  try {
    let request: any
    await page.route(fixture.baseURL + '/', route => route.fulfill({ contentType: 'text/html', body: commandDocument('active') }))
    await page.route('**/chats/turns', route => {
      request = JSON.parse(route.request().postData()!)
      return route.fulfill({ status: 204 })
    })
    await page.goto(fixture.baseURL)
    await page.locator('lv-chat-composer textarea').waitFor()
    await page.locator('lv-chat-page').evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agentContext: { surface: 'chat', dashboardId: 'sales', pageId: 'overview', references: [] } })
      e.savedBuilderHref = '/dashboards/sales/edit?embed=chat&page=overview'
      e.enterBuilder()
      await e.updateComplete
    })
    await page.locator('lv-chat-composer textarea').fill('Add a revenue trend')
    const response = page.waitForResponse('**/chats/turns')
    await page.locator('lv-chat-composer textarea').press('Enter')
    await response
    expect(request.agentContext).toMatchObject({ surface: 'builder', dashboardId: 'sales', pageId: 'overview' })
  } finally { await page.close() }
})

for (const scenario of ['active', 'new'] as const) for (const input of ['Build a sales dashboard', 'Explain revenue']) test(`rejected ${scenario} command restores the editable draft: ${input}`, async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 1000 } })
  try {
    const requests: any[] = []
    await page.route(fixture.baseURL + '/', route => route.fulfill({ contentType: 'text/html', body: commandDocument(scenario) }))
    await page.route('**/chats/turns', route => {
      requests.push(JSON.parse(route.request().postData()!))
      return route.fulfill({ status: requests.length === 1 ? 400 : 204, contentType: 'text/plain', body: requests.length === 1 ? 'select a dashboard page before asking the builder agent to add visuals\n' : '' })
    })
    await page.goto(fixture.baseURL)
    await page.evaluate(() => {
      (window as any).fetchFinished = 0
      document.addEventListener('datastar-fetch', event => { if ((event as CustomEvent).detail.type === 'finished') (window as any).fetchFinished++ })
    })
    const chat = page.locator('lv-chat-page')
    const textarea = page.locator('lv-chat-composer textarea')
    await textarea.fill(input)
    await textarea.press('Enter')
    await page.waitForFunction(() => (window as any).fetchFinished === 1)
    expect(await chat.evaluate((e: any) => ({ pending: e.pending, optimistic: Boolean(e.optimisticTurn), running: e.agent.status.running }))).toEqual({ pending: false, optimistic: false, running: false })
    expect(await textarea.isDisabled()).toBe(false)
    expect(await textarea.inputValue()).toBe(input)
    expect(await chat.getByRole('alert').filter({ hasText: 'draft is invalid or incomplete' }).count()).toBe(1)
    expect(await chat.locator('.agent-turn').filter({ hasText: input }).count()).toBe(0)
    if (process.env.LEAPVIEW_CHAT_SCREENSHOT_DIR && input.startsWith('Build')) {
      await page.screenshot({ path: `${process.env.LEAPVIEW_CHAT_SCREENSHOT_DIR}/chat-${scenario}-rejected-recovered.png`, fullPage: true })
    }
    await textarea.fill('Explain monthly revenue')
    await textarea.press('Enter')
    await page.waitForFunction(() => (window as any).fetchFinished === 2)
    expect(requests).toHaveLength(2)
    expect(requests[1].agent.composer.value).toBe('Explain monthly revenue')
    expect(await chat.getByRole('alert').filter({ hasText: 'draft is invalid or incomplete' }).count()).toBe(0)
  } finally { await page.close() }
})

test('unrelated command errors and errors after server acceptance preserve an active run', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    await page.locator('lv-chat-composer textarea').waitFor()
    const state = await page.locator('lv-chat-page').evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      e.showOptimisticTurn(new CustomEvent('lv-chat-submit', { detail: { input: 'Build a sales dashboard', references: [] } }))
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', el: document.body, argsRaw: { status: '400' } } }))
      await e.updateComplete
      const unrelated = { pending: e.pending, optimistic: Boolean(e.optimisticTurn), error: e.commandError ?? '' }
      mergePatch({ agent: { transcript: [{ id: 'accepted', kind: 'user', text: 'Build a sales dashboard', runId: 'accepted-run' }], status: { enabled: true, running: true, runId: 'accepted-run' } } })
      // The command stream can fail after the /updates stream accepted the run,
      // before Lit's next render has cleared the local optimistic submission.
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'retries-failed', el: e, argsRaw: {} } }))
      await e.updateComplete
      return { unrelated, accepted: { pending: e.pending, error: e.commandError ?? '', runId: e.agent.status.runId, generation: e.generationPrompt } }
    })
    expect(state.unrelated).toEqual({ pending: true, optimistic: true, error: '' })
    expect(state.accepted).toEqual({ pending: true, error: '', runId: 'accepted-run', generation: 'Build a sales dashboard' })
  } finally { await page.close() }
})

for (const input of ['Build a sales dashboard', 'Explain revenue']) for (const outcome of ['rejected', 'accepted', 'service-error', 'repeated-service-error'] as const) test(`retry after a prior run error preserves current ${outcome} handling: ${input}`, async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.route(fixture.baseURL + '/', route => route.fulfill({ contentType: 'text/html', body: commandDocument('active') }))
    await page.route('**/chats/turns', async route => {
      if (outcome === 'rejected') return route.fulfill({ status: 400, contentType: 'text/plain', body: 'invalid context' })
      const patch = outcome === 'accepted'
        ? { agent: { status: { enabled: true, running: true, runId: 'retry-run', error: null }, transcript: [{ id: 'retry-user', kind: 'user', text: input, runId: 'retry-run' }] } }
        : { agent: { status: { enabled: true, running: false, error: outcome === 'repeated-service-error' ? 'The previous run failed.' : 'This retry could not start.' } } }
      return route.fulfill({ contentType: 'text/event-stream', body: `event: datastar-patch-signals\ndata: signals ${JSON.stringify(patch)}\n\n` })
    })
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    const textarea = page.locator('lv-chat-composer textarea')
    await textarea.waitFor()
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { status: { enabled: true, running: false, error: 'The previous run failed.' } } })
      await e.updateComplete
      ;(window as any).fetchFinished = 0
      document.addEventListener('datastar-fetch', event => { if ((event as CustomEvent).detail.type === 'finished') (window as any).fetchFinished++ })
    })
    await textarea.fill(input)
    await textarea.press('Enter')
    await page.waitForFunction(() => (window as any).fetchFinished === 1)
    expect(await chat.evaluate((e: any) => Boolean(e.optimisticTurn))).toBe(false)
    expect(await textarea.isDisabled()).toBe(outcome === 'accepted')
    if (outcome === 'rejected') {
      expect(await chat.getByRole('alert').filter({ hasText: 'draft is invalid or incomplete' }).count()).toBe(1)
      expect(await textarea.inputValue()).toBe(input)
    } else if (outcome === 'accepted') {
      expect(await chat.evaluate((e: any) => ({ error: e.commandError, runId: e.agent.status.runId }))).toEqual({ error: '', runId: 'retry-run' })
    } else {
      expect(await chat.evaluate((e: any) => e.agent.status.error)).toBe(outcome === 'repeated-service-error' ? 'The previous run failed.' : 'This retry could not start.')
    }
  } finally { await page.close() }
})

for (const scenario of ['active', 'new'] as const) for (const input of ['Build a sales dashboard', 'Explain revenue']) test(`queued ${scenario} retry stays pending until delayed updates acceptance: ${input}`, async () => {
  const page = await fixture.browser.newPage()
  let releaseAcceptance = () => {}
  const acceptance = new Promise<void>(resolve => { releaseAcceptance = resolve })
  try {
    await page.route(fixture.baseURL + '/', route => route.fulfill({ contentType: 'text/html', body: commandDocument(scenario)
      .replace('<lv-chat-page ', `<lv-chat-page data-init="@get('/updates?route=chat')" `) }))
    await page.route('**/updates?**', async route => {
      await acceptance
      const patch = { agent: { status: { enabled: true, running: true, runId: 'queued-run', error: null }, transcript: [{ id: 'queued-user', kind: 'user', text: input, runId: 'queued-run' }] } }
      return route.fulfill({ contentType: 'text/event-stream', body: `event: datastar-patch-signals\ndata: signals ${JSON.stringify(patch)}\n\n` })
    })
    await page.route('**/chats/turns', route => route.fulfill({ status: 204 }))
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    const textarea = page.locator('lv-chat-composer textarea')
    await textarea.waitFor()
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { status: { enabled: true, running: false, error: 'The previous run failed.' } } })
      await e.updateComplete
      ;(window as any).commandFinished = false
      document.addEventListener('datastar-fetch', event => {
        const detail = (event as CustomEvent).detail
        if (detail.type === 'finished' && detail.el === e) (window as any).commandFinished = true
      })
    })
    await textarea.fill(input)
    await textarea.press('Enter')
    await page.waitForFunction(() => (window as any).commandFinished)
    expect(await chat.evaluate((e: any) => e.pending)).toBe(true)
    expect(await textarea.isDisabled()).toBe(true)
    if (scenario === 'active') expect(await chat.evaluate((e: any) => e.optimisticTurn?.text)).toBe(input)
    releaseAcceptance()
    await page.waitForFunction(() => (document.querySelector('lv-chat-page') as any).agent.status.runId === 'queued-run')
    expect(await chat.evaluate((e: any) => ({ pending: e.pending, optimistic: Boolean(e.optimisticTurn), error: e.commandError }))).toEqual({ pending: true, optimistic: false, error: '' })
    expect(await textarea.isDisabled()).toBe(true)
  } finally {
    releaseAcceptance()
    await page.close()
  }
})
