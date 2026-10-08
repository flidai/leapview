import { mkdir } from 'node:fs/promises'
import { join } from 'node:path'
import { expect, test } from 'bun:test'
import { chatPageBrowserFixture } from './chat-page-browser.test-fixture'
import { windowedTablePreviewEnvelope } from '../dashboard/dashboard-builder-test-fixtures'

const fixture = chatPageBrowserFixture()

test('dashboard tables forward sorting and paging to the retained builder', async () => {
 const page = await fixture.browser.newPage()
 try {
  await page.route('**/dashboards/demo/edit?*', route => route.fulfill({ contentType: 'text/html', body: '<lv-dashboard-builder></lv-dashboard-builder>' }))
  await page.goto(fixture.baseURL)
  const chat = page.locator('lv-chat-page')
  await chat.locator('lv-chat-composer').waitFor()
  await chat.evaluate(async (e: any) => {
   e.savedBuilderHref = '/dashboards/demo/edit?embed=chat&page=details'
   e.restoredBuilderHref = e.savedBuilderHref
   await e.updateComplete
  })
  await page.frameLocator('.builder-frame').locator('lv-dashboard-builder').waitFor({ state: 'attached' })
  await chat.evaluate(async (e: any, envelope) => {
   const child = e.builderFrame.contentWindow
   child.requests = []
   child.addEventListener('message', (event: MessageEvent) => { if (event.data.type === 'lv-builder-visual-window') child.requests.push(event.data) })
   e.savedDashboardArtifacts = [{ id: 'detail', type: 'table', summary: 'Amounts' }]
   e.savedDashboardVisuals = { detail: envelope }
   e.dashboardPageId = 'details'
   e.dashboardPreview = true
   e.selectedPreviewVisual = 'detail'
   e.visitedVisuals = ['detail']
   await e.updateComplete
  }, windowedTablePreviewEnvelope())
  const table = chat.locator('lv-report-table')
  await table.locator('.header-button').click()
  await page.waitForFunction(() => (document.querySelector('lv-chat-page') as any).builderFrame.contentWindow.requests.some((item: any) => item.request.sort[0].direction === 'descending'))
  const request = await chat.evaluate((e: any) => e.builderFrame.contentWindow.requests.find((item: any) => item.request.sort[0].direction === 'descending'))
  expect(request.pageId).toBe('details')
  expect(request.request.visualID).toBe('sales-chart')
  expect(request.request.sort[0].direction).toBe('descending')
  const sorted = windowedTablePreviewEnvelope()
  if (sorted.dataState.kind !== 'windowed') throw new Error('expected windowed table')
  sorted.dataState.sort = request.request.sort
  sorted.dataState.resetVersion = request.request.resetVersion
  sorted.dataState.blocks = Object.fromEntries(['a', 'b', 'c'].map((id, index) => [id, { id, start: index * 50, rows: Array.from({ length: 50 }, (_, i) => [249 - index * 50 - i]), requestSeq: request.request.requestSeq, resetVersion: request.request.resetVersion, sort: request.request.sort }]))
  await page.frameLocator('.builder-frame').locator('body').evaluate((_, envelope) => {
   window.parent.postMessage({ type: 'lv-builder-saved', revisionId: 'rev', pageId: 'details', href: '/dashboards/demo/edit?embed=chat&page=details', reference: { reference: { kind: 'dashboard', id: 'demo' }, name: 'Demo', hierarchy: [], locations: [], context: [] }, components: [{ id: 'sales-chart', pageId: 'details', artifactId: 'detail' }], artifacts: [{ id: 'detail', type: 'table', summary: 'Amounts' }], visuals: { detail: envelope } }, window.parent.location.origin)
  }, sorted)
  await page.waitForFunction(() => {
   const chat = document.querySelector('lv-chat-page') as any
   return chat.savedDashboardVisuals.detail.dataState.sort[0].direction === 'descending'
  })
  expect(await table.evaluate((e: any) => ({ sort: e.table.sort, first: e.table.blocks.a.rows[0].amount }))).toEqual({ sort: { key: 'amount', direction: 'desc' }, first: 249 })
  const requestCount = await chat.evaluate((e: any) => e.builderFrame.contentWindow.requests.length)
  await table.locator('.table-scrollport').evaluate(e => { e.scrollTop = 5100; e.dispatchEvent(new Event('scroll')) })
  await page.waitForFunction(count => (document.querySelector('lv-chat-page') as any).builderFrame.contentWindow.requests.slice(count).some((item: any) => item.request.start >= 150 && item.request.sort[0].direction === 'descending'), requestCount)
 } finally { await page.close() }
})

test('reopening a generated dashboard reply loads all visual cards without leaving chat', async () => {
 const page = await fixture.browser.newPage()
 try {
  let loads = 0
  await page.route('**/chats/*/actions/*/open?*', route => {
   loads++
   return route.fulfill({contentType:'text/html',body:'<lv-dashboard-builder>Existing preview</lv-dashboard-builder>'})
  })
  await page.goto(fixture.baseURL)
  const chat = page.locator('lv-chat-page')
  await chat.locator('lv-chat-composer').waitFor()
  await chat.evaluate(async (e:any) => {
   const {mergePatch} = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
   mergePatch({agent:{status:{enabled:true,running:false},transcript:[
    ...['create_dashboard_draft','preview_dashboard_draft'].map(name => ({id:name,kind:'tool',name,runId:'saved-build',toolCallId:name,status:'complete'})),
    {id:'answer',kind:'assistant',text:'Your dashboard is ready.\n\nFull explanation.'},
   ]}})
   await e.updateComplete
  })
  await page.waitForFunction(() => Boolean((document.querySelector('lv-chat-page') as any).restoredBuilderHref))
  await page.frameLocator('.builder-frame').getByText('Existing preview').waitFor({state:'attached'})
  await page.frameLocator('.builder-frame').locator('body').evaluate(() => {
   window.parent.postMessage({type:'lv-builder-saved',revisionId:'rev',pageId:'pies',pageTitle:'Pie charts',pages:[{id:'pies',title:'Pie charts'}],href:'/dashboards/demo/edit?embed=chat&page=pies',reference:{reference:{kind:'dashboard',id:'demo'},name:'Demo',hierarchy:[],locations:[],context:[]},components:[{id:'pie',pageId:'pies',artifactId:'pie'},{id:'bar',pageId:'pies',artifactId:'bar'}],artifacts:[{id:'pie',type:'pie',summary:'Revenue mix'},{id:'bar',type:'bar',summary:'Revenue trend'}],visuals:{}},window.parent.location.origin)
  })
  await page.getByRole('button',{name:'Open Revenue mix in visuals sidebar'}).waitFor()
  await page.getByRole('button',{name:'Open Revenue trend in visuals sidebar'}).waitFor()
  expect(await chat.evaluate((e:any)=>e.builderOpen)).toBe(false)
  expect(new URL(page.url()).searchParams.has('preview')).toBe(false)
  await page.getByRole('button',{name:'Open Revenue mix in visuals sidebar'}).click()
  expect(await page.getByText('Loading visual…',{exact:true}).isVisible()).toBe(true)
  expect(await page.getByRole('button',{name:'Remove from dashboard',exact:true}).isVisible()).toBe(true)
  expect(await page.getByRole('button',{name:'Add to dashboard',exact:true}).count()).toBe(0)
  await page.getByRole('button',{name:'Close visuals sidebar'}).click()
  await chat.evaluate(async (e:any)=>{e.requestUpdate();await e.updateComplete})
  expect(loads).toBe(1)
 } finally {await page.close()}
})

test('chat visual card opens a side panel with the chart and Save action', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    fixture.savedVisualRequest = null
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-chat-page') && customElements.get('lv-chat-thread'))
    await page.locator('lv-chat-page lv-chat-thread').waitFor()
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      const field = (id: string, role: string) => ({ id, role, dataType: role === 'metric' ? 'decimal' : 'string', nullable: false, label: id })
      mergePatch({
        agent: { transcript: [{
          id: 'tool-chart', kind: 'tool', name: 'query_visual', status: 'complete', conversationId: 'c1',
          argumentsJson: JSON.stringify({ semanticModelId: 'sales', visual: { type: 'bar', query: { type: 'aggregate', dimensions: ['country'], metrics: ['revenue'] } } }),
          resultJson: JSON.stringify({ ok: true, type: 'bar', id: 'chart-1', datasetId: 'orders', semanticModelRef: { kind: 'semantic_model', id: 'sales' }, fields: [{ fieldId: 'sales.country', role: 'dimension', explorerFieldId: 'orders.country' }] }),
          artifact: { id: 'chart-1', type: 'bar', summary: 'Revenue by country' },
        }] },
        visuals: { 'chart-1': {
          schemaVersion: 14, visualID: 'chart-1', rendererID: 'echarts', specRevision: `sha256:${'2'.repeat(64)}`, dataRevision: 1,
          spec: { kind: 'cartesian', mark: 'bar', title: 'Revenue by country', datasets: [{ id: 'primary', fields: [field('label', 'dimension'), field('value', 'metric')] }], dataBudget: { maxRows: 50, requiredCompleteness: 'complete' }, accessibility: { title: 'Revenue by country', description: 'Revenue' }, interactions: [], x: { dataset: 'primary', field: 'label' }, y: [{ dataset: 'primary', field: 'value' }], presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, smooth: false, stacked: false, showSymbols: true, dataZoom: false, area: false, step: false } },
          dataState: { kind: 'inline', specRevision: `sha256:${'2'.repeat(64)}`, dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision: `sha256:${'2'.repeat(64)}`, dataRevision: 1, generation: 1, columns: ['label', 'value'], rows: [['France', 42]], completeness: 'complete' }] },
          selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
        } },
      })
    })
    await page.waitForFunction(() => Boolean(document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-thread')?.shadowRoot?.querySelector('[data-visual-id="chart-1"]')))
    await page.locator('lv-chat-page').evaluate((element: any) => {
      element.shadowRoot.querySelector('lv-chat-thread').shadowRoot.querySelector('[data-visual-id="chart-1"]').click()
    })
    const state = await page.locator('lv-chat-page').evaluate(async (element: any) => {
      await element.updateComplete
      const panel = element.shadowRoot.querySelector('lv-chat-visual-panel') as any
      await panel?.updateComplete
      return {
        open: Boolean(panel),
        title: panel?.shadowRoot.querySelector('h2')?.textContent,
        chart: panel?.shadowRoot.querySelector('lv-visual-artifact')?.payload?.spec?.kind,
        schemaVersion: panel?.shadowRoot.querySelector('lv-visual-artifact')?.payload?.schemaVersion,
        revisions: [panel?.payload?.specRevision, panel?.payload?.dataState?.specRevision, panel?.payload?.dataRevision, panel?.payload?.dataState?.dataRevision],
        save: Boolean(panel?.shadowRoot.querySelector('[aria-label="Save visual to Data Explorer"]')),
        hasExploreLink: Boolean(panel?.shadowRoot.querySelector('a[aria-label="Open visual in Data Explorer"]')),
        explorerHref: panel?.explorerHref,
        auditHref: panel?.auditHref,
        visualExplorerHref: panel?.shadowRoot.querySelector('lv-visual-artifact')?.explorerHref,
      }
    })
    expect(state.open).toBe(true)
    expect(state.title).toBe('Revenue by country')
    expect(state.chart).toBe('cartesian')
    expect(state.schemaVersion).toBe(14)
    expect(state.revisions).toEqual([`sha256:${'2'.repeat(64)}`, `sha256:${'2'.repeat(64)}`, 1, 1])
    expect(state.save).toBe(true)
    expect(state.hasExploreLink).toBe(false)
    expect(state.explorerHref).toContain('/explore?')
    expect(state.auditHref).toContain('/visuals/chart-1/explore')
    expect(state.visualExplorerHref).toBe(state.explorerHref)
    const latest = await page.locator('lv-chat-page').evaluate(async (chat: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      const original = chat.agent.transcript[0]
      const input = JSON.parse(original.argumentsJson)
      input.visual.query.metrics = ['profit']
      const payload = chat.visuals['chart-1']
      mergePatch({ agent: { transcript: [original, { ...original, id: 'new-tool', runId: 'new-run', argumentsJson: JSON.stringify(input) }] },
        visuals: { 'chart-1': { ...payload, specRevision: `sha256:${'3'.repeat(64)}`, spec: { ...payload.spec, title: 'Profit by country' } } },
      })
      await chat.updateComplete
      const panel = chat.shadowRoot.querySelector('lv-chat-visual-panel')
      await panel.updateComplete
      return { explorerHref: panel.explorerHref, auditHref: panel.auditHref, title: panel.title, payloadTitle: panel.payload.spec.title }
    })
    expect(latest.payloadTitle).toBe('Profit by country')
    expect(latest.title).toBe('Profit by country')
    expect(latest.explorerHref).not.toBe(state.explorerHref)
    expect(JSON.parse(new URL(latest.explorerHref, fixture.baseURL).searchParams.get('state')!).metrics).toEqual([{ field: 'profit' }])
    expect(latest.auditHref).toBe('/chats/c1/visuals/chart-1/explore?run=new-run')
    state.explorerHref = latest.explorerHref
    const screenshotDir = process.env.LEAPVIEW_CHAT_SCREENSHOT_DIR
    if (screenshotDir) {
      await page.waitForFunction(() => Boolean(document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-visual-panel')?.shadowRoot?.querySelector('lv-visual-artifact')?.shadowRoot?.querySelector('lv-visualization-host')?.shadowRoot?.querySelector('.renderer canvas')))
      await mkdir(screenshotDir, { recursive: true })
      await page.screenshot({ path: join(screenshotDir, 'chat-visual-panel-after.png'), fullPage: true })
    }
    await page.locator('lv-chat-page').evaluate((element: any) => element.shadowRoot.querySelector('lv-chat-visual-panel').shadowRoot.querySelector('[aria-label="Save visual to Data Explorer"]').click())
    await page.waitForFunction(() => document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-visual-panel')?.shadowRoot?.textContent?.includes('Saved to Data Explorer.'))
    expect(JSON.parse(fixture.savedVisualRequest!.body)).toMatchObject({ title: 'Profit by country', explorerUrl: state.explorerHref })
    expect(fixture.savedVisualRequest!.csrf).toBe('test-csrf')
    await page.setViewportSize({ width: 390, height: 820 })
    await page.waitForFunction(() => Boolean(document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('.main')?.hasAttribute('inert')))
    const mobilePanel = await page.locator('lv-chat-page').evaluate((element: any) => {
      const panel = element.shadowRoot.querySelector('lv-chat-visual-panel') as HTMLElement
      const bounds = panel.getBoundingClientRect()
      return { x: bounds.x, width: bounds.width, modal: panel.shadowRoot?.querySelector('aside')?.getAttribute('aria-modal'), chatInert: element.shadowRoot.querySelector('.main')?.inert }
    })
    expect(mobilePanel).toEqual({ x: 0, width: 390, modal: 'true', chatInert: true })
    if (screenshotDir) await page.screenshot({ path: join(screenshotDir, 'chat-visual-panel-mobile-after.png'), fullPage: true })
    await page.locator('lv-chat-page').evaluate((element: any) => element.shadowRoot.querySelector('lv-chat-visual-panel').shadowRoot.querySelector('[aria-label="Close visual details"]').click())
    expect(await page.locator('lv-chat-page').evaluate((element: any) => Boolean(element.shadowRoot.querySelector('lv-chat-visual-panel')))).toBe(false)
    await page.setViewportSize({ width: 1280, height: 820 })
    await page.getByRole('button', { name: 'Visuals (1)', exact: true }).click()
    const preview = page.locator('.preview-panel lv-visual-artifact')
    await preview.locator('lv-visualization-host').waitFor()
    expect(await preview.evaluate((artifact: any) => artifact.shadowRoot.querySelector('lv-visualization-host').exploreHref)).toBe(state.explorerHref)
    expect(await preview.locator('[slot="agent-action"]').count()).toBe(0)
    await page.evaluate(async () => {
      const chat = document.querySelector('lv-chat-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev')
      const item = chat.agent.transcript[0]
      const input = JSON.parse(item.argumentsJson)
      input.visual.query.type = 'records'
      mergePatch({ agent: { transcript: [{ ...item, runId: 'unsupported-run', argumentsJson: JSON.stringify(input) }] } })
    })
    await preview.getByRole('link', { name: 'View saved visual', exact: true }).waitFor()
    expect(await preview.getByRole('link', { name: 'View saved visual', exact: true }).getAttribute('href')).toBe('/chats/c1/visuals/chart-1/explore?run=unsupported-run')
    expect(await preview.evaluate((artifact: any) => artifact.shadowRoot.querySelector('lv-visualization-host').exploreHref)).toBe('')
  } finally {
    await page.close()
  }
})

test('Search Ask AI opens an unsent draft once and clears only its prompt fragment', async () => {
  const page = await fixture.browser.newPage()
  const requestsBefore = fixture.draftTurnRequests
  const prompt = 'Why did sales fall in Europe?'
  try {
    await page.addInitScript(() => window.history.replaceState({ fromSearch: 'retained-state' }, '', window.location.href))
    await page.goto(`${fixture.baseURL}/new?context=search#${new URLSearchParams({ prompt, source: 'search' })}`)
    const composer = page.locator('lv-chat-page lv-chat-composer')
    await page.waitForFunction(prompt => {
      const composer = document.querySelector('lv-chat-page')?.shadowRoot?.querySelector('lv-chat-composer') as any
      return composer?.getDraft?.() === prompt && !window.location.hash.includes('prompt=')
    }, prompt)
    expect(await composer.locator('textarea').inputValue()).toBe(prompt)
    expect(new URL(page.url()).hash).toBe('#source=search')
    expect(new URL(page.url()).search).toBe('?context=search')
    expect(await page.evaluate(() => window.history.state)).toEqual({ fromSearch: 'retained-state' })
    await composer.locator('textarea').fill('My revised question')
    await page.locator('lv-chat-page').evaluate(async (chat: any) => { chat.requestUpdate(); await chat.updateComplete })
    expect(await composer.locator('textarea').inputValue()).toBe('My revised question')
    expect(fixture.draftTurnRequests).toBe(requestsBefore)
  } finally { await page.close() }
})
