import { expect, test } from 'bun:test'
import { chatThreadBrowserFixture } from './chat-thread-browser.test-fixture'

const fixture = chatThreadBrowserFixture()

test('dashboard replies keep a short summary and reveal the full explanation on demand', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
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
      expect(await page.getByText('Earlier draft needed correction.', { exact: true }).count()).toBe(0)
      expect(await page.getByRole('link', { name: 'Open in Builder' }).count()).toBe(1)
      const disclosure = thread.locator('.run-steps')
      await disclosure.locator('summary').click()
      expect(await page.getByText('Revenue by country', { exact: true }).isVisible()).toBe(true)
      expect(await page.getByText('Earlier draft needed correction.', { exact: true }).count()).toBe(0)
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
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
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
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
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

for (const running of [true, false]) test(`dashboard cards remain available during a tool-only turn${running ? ' while running' : ' without a final answer'}`, async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    await page.evaluate(async running => {
      await customElements.whenDefined('lv-chat-thread')
      const thread = document.querySelector('lv-chat-thread') as any
      thread.status = { enabled: true, running, runId: 'inspection' }
      thread.conversationId = 'conversation'
      thread.dashboardPreviewAvailable = true
      thread.dashboardId = 'finance'
      thread.pageArtifacts = [{ id: 'revenue', type: 'bar', summary: 'Revenue' }]
      thread.transcript = [
        { id: 'greeting', kind: 'assistant', text: 'Ready.' },
        { id: 'user', kind: 'user', text: 'Inspect my dashboard' },
        { id: 'tool', kind: 'tool', name: 'list_dashboards', status: running ? 'running' : 'complete', runId: 'inspection' },
      ]
      thread.addEventListener('lv-chat-dashboard-preview', (event: CustomEvent) => { (window as any).selectedVisual = event.detail.artifactId })
      await thread.updateComplete
    }, running)
    const card = page.getByRole('button', { name: 'Open Revenue in visuals sidebar', exact: true })
    expect(await card.count()).toBe(1)
    expect(await card.isVisible()).toBe(true)
    await card.click()
    expect(await page.evaluate(() => (window as any).selectedVisual)).toBe('revenue')
    await page.locator('lv-chat-thread').evaluate(async (thread: any) => {
      thread.status = { enabled: true, running: false }
      thread.transcript = [...thread.transcript, { id: 'answer', kind: 'assistant', text: 'Your dashboard is available.' }]
      await thread.updateComplete
    })
    expect(await card.count()).toBe(1)
    const reply = page.locator('.agent-turn').filter({ hasText: 'Your dashboard is available.' })
    expect(await reply.locator('.visual-reference').count()).toBe(1)
    expect(await reply.getByRole('group', { name: 'Answer actions' }).count()).toBe(1)
  } finally { await page.close() }
})

test('chat keeps retries in collapsed activity while showing the final answer', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
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

test('a later dashboard edit failure remains visible after an earlier successful preview', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-thread'))
    const thread = page.locator('lv-chat-thread')
    await thread.evaluate(async (e: any) => {
      e.status = {enabled:true,running:false}
      e.conversationId = 'conversation'
      e.transcript = [
        {id:'user',kind:'user',text:'Update my dashboard'},
        {id:'preview',kind:'tool',name:'preview_dashboard_draft',toolCallId:'preview',status:'complete',argumentsJson:'{"dashboardId":"finance"}'},
        {id:'failed-edit',kind:'tool',name:'edit_dashboard_source',status:'error',error:'The requested new chart could not be added.'},
        {id:'answer',kind:'assistant',text:'The existing dashboard is available, but the new chart was not added.'},
      ]
      await e.updateComplete
    })
    expect(await thread.getByText('The requested new chart could not be added.',{exact:true}).isVisible()).toBe(true)
  } finally {await page.close()}
})
