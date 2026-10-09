import { expect, test } from 'bun:test'
import { chatPageBrowserFixture } from './chat-page-browser.test-fixture'

const fixture = chatPageBrowserFixture()

for (const viewport of [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'mobile', width: 390, height: 820 },
]) test(`top-right visuals toggle opens and closes the sidebar on ${viewport.name}`, async () => {
  const page = await fixture.browser.newPage({ viewport })
  let dashboardRequests = 0
  try {
    await page.route('**/dashboards/new', async route => {
      dashboardRequests += 1
      await route.fulfill({ contentType: 'text/html', body: '<lv-dashboard-builder></lv-dashboard-builder>' })
    })
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { transcript: [{ id: 'chart-one', kind: 'tool', name: 'query_visual', status: 'complete',
        argumentsJson: JSON.stringify({ semanticModelId: 'sales' }),
        artifact: { id: 'chart-one', type: 'bar', summary: 'Revenue' },
      }] } })
      await e.updateComplete
      ;(window as any).retainedBuilder = e.shadowRoot.querySelector('.builder-frame')
    })
    const toggle = chat.locator('.conversation-titlebar .chat-size-toggle')
    const sidebar = chat.getByRole('region', { name: 'Dashboard preview', exact: true })
    expect(await toggle.getAttribute('aria-label')).toBe('Open visuals sidebar')
    expect(await toggle.getAttribute('aria-expanded')).toBe('false')
    await toggle.click()
    expect(await sidebar.isVisible()).toBe(true)
    expect(await toggle.getAttribute('aria-label')).toBe('Close visuals sidebar')
    expect(await toggle.getAttribute('aria-expanded')).toBe('true')
    expect(new URL(page.url()).searchParams.get('preview')).toBe('dashboard')
    await toggle.click()
    expect(await sidebar.isVisible()).toBe(false)
    expect(await toggle.getAttribute('aria-label')).toBe('Open visuals sidebar')
    expect(await toggle.getAttribute('aria-expanded')).toBe('false')
    expect(new URL(page.url()).searchParams.has('preview')).toBe(false)
    await toggle.click()
    expect(await sidebar.isVisible()).toBe(true)
    expect(await chat.locator('.preview-card:not([hidden])').getAttribute('data-preview-visual')).toBe('chart-one')
    expect(await chat.evaluate((e: any) => ({
      builderOpen: e.builderOpen,
      retainedBuilder: e.shadowRoot.querySelector('.builder-frame') === (window as any).retainedBuilder,
    }))).toEqual({ builderOpen: false, retainedBuilder: true })
    expect(dashboardRequests).toBe(0)
  } finally { await page.close() }
})

test('browser Back from an added visual builder retains the current chat and draft', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 900 } })
  const savedId = '11111111-1111-1111-1111-111111111111'
  let componentId = ''
  let builderLoads = 0
  let mainNavigations = 0
  const projection = () => ({
    href: '/dashboards/demo/edit?draft=draft-one&embed=chat&page=overview', revisionId: 'latest-revision',
    pageId: 'overview', pageTitle: 'Overview', pages: [{ id: 'overview', title: 'Overview' }],
    reference: { reference: { kind: 'dashboard', id: 'demo' }, name: 'Latest dashboard', hierarchy: [], locations: [], context: [] },
    components: [{ id: componentId, pageId: 'overview', savedVisualId: savedId }], artifacts: [], visuals: {},
  })
  try {
    await page.route('**/visuals/saved', route => route.fulfill({ contentType: 'text/html', body: `<lv-saved-visual-library></lv-saved-visual-library><script>
      parent.postMessage({type:'lv-saved-visual-library',library:{visuals:[{id:'${savedId}',sourceKey:'c1/chart-one',title:'Revenue'}]}},location.origin)
    </script>` }))
    await page.route('**/dashboards/new', route => {
      const values = new URLSearchParams(route.request().postData() ?? '')
      componentId = `saved_${savedId.replaceAll('-', '')}_${values.get('idempotencyKey')!.replaceAll('-', '')}`
      return route.fulfill({ contentType: 'text/html', body: `<div id="chat-dashboard-receipt"></div><script>
        parent.postMessage(${JSON.stringify({ type: 'lv-dashboard-mutation', ...projection() })},location.origin)
      </script>` })
    })
    await page.route('**/dashboards/demo/edit?*', route => {
      builderLoads += 1
      return route.fulfill({ contentType: 'text/html', body: `<lv-dashboard-builder>Latest builder</lv-dashboard-builder><script>
        parent.postMessage(${JSON.stringify({ type: 'lv-builder-saved', ...projection() })},location.origin)
      </script>` })
    })
    await page.goto(`${fixture.baseURL}/chats/c1`)
    page.on('request', request => { if (request.isNavigationRequest() && request.frame() === page.mainFrame()) mainNavigations += 1 })
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { transcript: [{ id: 'chart-one', kind: 'tool', name: 'query_visual', status: 'complete',
        argumentsJson: JSON.stringify({ semanticModelId: 'sales', visual: { type: 'bar' } }),
        artifact: { id: 'chart-one', type: 'bar', summary: 'Revenue' },
      }] } })
      await e.updateComplete
      ;(window as any).retainedChat = e
      ;(window as any).retainedThread = e.shadowRoot.querySelector('lv-chat-thread')
      ;(window as any).retainedComposer = e.shadowRoot.querySelector('lv-chat-composer')
      ;(window as any).retainedComposer.setDraft('Keep this unsent prompt')
    })
    await page.waitForFunction(() => (document.querySelector('lv-chat-page') as any).visualLibraryState.savedIds.includes('chart-one'))
    expect(await chat.getByRole('button', { name: 'Preview', exact: true }).count()).toBe(0)
    await chat.getByRole('button', { name: 'Open visuals sidebar', exact: true }).click()
    expect(await chat.getByRole('button', { name: 'Open in Builder', exact: true }).count()).toBe(0)
    expect(await chat.getByRole('button', { name: 'Preview', exact: true }).count()).toBe(0)
    await chat.getByRole('button', { name: 'Add to dashboard', exact: true }).click()
    await chat.getByRole('button', { name: 'Remove from dashboard', exact: true }).waitFor()
    expect(await chat.getByRole('button', { name: 'Open in Builder', exact: true }).count()).toBe(0)
    expect(await chat.locator('.conversation-titlebar').getByRole('button', { name: 'Preview', exact: true }).isVisible()).toBe(true)
    await chat.getByRole('button', { name: 'Preview', exact: true }).click()
    await page.frameLocator('.builder-frame').getByText('Latest builder').waitFor()
    await page.waitForURL(url => url.searchParams.get('preview') === 'builder')
    await page.getByRole('button', { name: 'Expand chat', exact: true }).waitFor()
    await chat.evaluate((e: any) => { (window as any).retainedBuilderDocument = e.shadowRoot.querySelector('.builder-frame').contentDocument })
    await page.goBack()
    await chat.getByRole('button', { name: 'Close visuals sidebar', exact: true }).first().waitFor()
    expect(await chat.evaluate((e: any) => e.builderOpen)).toBe(false)
    expect(new URL(page.url()).searchParams.get('preview')).toBe('dashboard')
    expect(await chat.getByRole('button', { name: 'Remove from dashboard', exact: true }).isVisible()).toBe(true)
    expect(await chat.evaluate((e: any) => ({
      retainedChat: e === (window as any).retainedChat,
      retainedThread: e.shadowRoot.querySelector('lv-chat-thread') === (window as any).retainedThread,
      retainedComposer: e.shadowRoot.querySelector('lv-chat-composer') === (window as any).retainedComposer,
      retainedBuilder: e.shadowRoot.querySelector('.builder-frame').contentDocument === (window as any).retainedBuilderDocument,
      draft: e.shadowRoot.querySelector('lv-chat-composer').shadowRoot.querySelector('textarea').value,
      revision: e.dashboardRevisionId,
    }))).toEqual({ retainedChat: true, retainedThread: true, retainedComposer: true, retainedBuilder: true, draft: 'Keep this unsent prompt', revision: 'latest-revision' })
    await page.goForward()
    await page.getByRole('button', { name: 'Expand chat', exact: true }).waitFor()
    await page.frameLocator('.builder-frame').getByText('Latest builder').waitFor()
    expect(await chat.evaluate((e: any) => e.shadowRoot.querySelector('.builder-frame').contentDocument === (window as any).retainedBuilderDocument)).toBe(true)
    expect(builderLoads).toBe(1)
    expect(mainNavigations).toBe(0)
  } finally { await page.close() }
}, 30_000)

for (const dismiss of ['Expand chat', 'browser Back', 'conversation change']) test(`a delayed builder load cannot reopen chat after ${dismiss}`, async () => {
  const page = await fixture.browser.newPage()
  let release: (() => void) | undefined
  let requested: (() => void) | undefined
  const started = new Promise<void>(resolve => { requested = resolve })
  try {
    await page.route('**/dashboards/demo/edit?*', async route => {
      requested!()
      await new Promise<void>(resolve => { release = resolve })
      await route.fulfill({ contentType: 'text/html', body: '<lv-dashboard-builder>Loaded draft</lv-dashboard-builder>' })
    })
    await page.goto(`${fixture.baseURL}/chats/c1`)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { transcript: [{ kind: 'tool', status: 'complete', artifact: { id: 'chart-one', type: 'bar' } }] } })
      e.savedBuilderHref = '/dashboards/demo/edit?embed=chat&page=overview'
      e.builderNeedsRefresh = true
      await e.updateComplete
    })
    await chat.getByRole('button', { name: 'Open visuals sidebar', exact: true }).click()
    await chat.getByRole('button', { name: 'Preview', exact: true }).click()
    await started
    if (dismiss === 'browser Back') await page.goBack()
    else if (dismiss === 'conversation change') await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { activeConversationId: 'c2' } })
      await e.updateComplete
    })
    else await chat.getByRole('button', { name: 'Expand chat', exact: true }).click()
    const returnedURL = page.url()
    release!()
    await page.frameLocator('.builder-frame').getByText('Loaded draft').waitFor({ state: 'attached' })
    expect(await chat.evaluate((e: any) => ({ builderOpen: e.builderOpen, pending: Boolean(e.pendingBuilderLayout) }))).toEqual({ builderOpen: false, pending: false })
    expect(page.url()).toBe(returnedURL)
    expect(new URL(page.url()).searchParams.get('preview')).toBe(dismiss === 'browser Back' ? null : 'dashboard')
  } finally { release?.(); await page.close() }
}, 30_000)

test('a failed builder load reports the response without pushing a builder history entry', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.route('**/dashboards/demo/edit?*', route => route.fulfill({ contentType: 'text/html', body: '<p>Draft unavailable</p>' }))
    await page.goto(`${fixture.baseURL}/chats/c1`)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    await chat.evaluate(async (e: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
      mergePatch({ agent: { transcript: [{ kind: 'tool', status: 'complete', artifact: { id: 'chart-one', type: 'bar' }, argumentsJson: JSON.stringify({ semanticModelId: 'sales' }) }] } })
      e.savedBuilderHref = '/dashboards/demo/edit?embed=chat&page=overview'
      e.builderNeedsRefresh = true
      await e.updateComplete
    })
    await chat.getByRole('button', { name: 'Open visuals sidebar', exact: true }).click()
    await chat.getByRole('button', { name: 'Preview', exact: true }).click()
    await chat.getByRole('alert').filter({ hasText: 'The dashboard could not be opened' }).waitFor()
    expect(await chat.evaluate((e: any) => ({ saving: e.savingDashboard, pending: Boolean(e.pendingBuilderLayout), needsRefresh: e.builderNeedsRefresh }))).toEqual({ saving: false, pending: false, needsRefresh: true })
    expect(new URL(page.url()).searchParams.get('preview')).toBe('dashboard')
  } finally { await page.close() }
})

test('top Preview opens only an existing dashboard and waits for pending dashboard changes', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    const chat = page.locator('lv-chat-page')
    await chat.locator('lv-chat-composer').waitFor()
    expect(await chat.getByRole('button', { name: 'Preview', exact: true }).count()).toBe(0)
    await chat.evaluate(async (e: any) => { await e.previewDashboard(); await e.updateComplete })
    expect(await chat.evaluate((e: any) => e.builderOpen)).toBe(false)
    await chat.evaluate(async (e: any) => {
      e.savedBuilderHref = '/dashboards/demo/edit?embed=chat&page=overview'
      await e.updateComplete
    })
    const preview = chat.locator('.conversation-titlebar').getByRole('button', { name: 'Preview', exact: true })
    expect(await preview.isVisible()).toBe(true)
    for (const pending of [{ savingDashboard: true }, { builderUpdating: true }, { pendingDashboardPageId: 'details' }]) {
      await chat.evaluate(async (e: any, pending) => {
        Object.assign(e, { savingDashboard: false, builderUpdating: false, pendingDashboardPageId: '' }, pending)
        await e.updateComplete
        await e.previewDashboard()
      }, pending)
      expect(await preview.isDisabled()).toBe(true)
      expect(await chat.evaluate((e: any) => e.builderOpen)).toBe(false)
    }
    await chat.evaluate(async (e: any) => {
      Object.assign(e, { savingDashboard: false, builderUpdating: false, pendingDashboardPageId: '' })
      await e.updateComplete
    })
    expect(await preview.isEnabled()).toBe(true)
  } finally { await page.close() }
})
