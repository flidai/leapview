import { expect, test } from 'bun:test'
import { chatPageBrowserFixture } from './chat-page-browser.test-fixture'

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
    expect(state).toEqual({ builderOpen: true, surface: 'builder', visible: true, animation: true })
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
