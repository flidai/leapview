import { expect, test } from 'bun:test'
import { chatPageBrowserFixture } from './chat-page-browser.test-fixture'
import { windowedTablePreviewEnvelope } from '../dashboard/dashboard-builder-test-fixtures'

const fixture = chatPageBrowserFixture()

test('a removed windowed table offers Add instead of nonfunctional sort controls', async () => {
 const page = await fixture.browser.newPage()
 try {
  await page.goto(fixture.baseURL)
  const chat = page.locator('lv-chat-page')
  await chat.locator('lv-chat-composer').waitFor()
  await chat.evaluate(async (e: any, envelope) => {
   e.retainedDashboardArtifacts = [{id:'removed', type:'table', summary:'Country performance'}]
   e.retainedDashboardVisuals = {removed:envelope}
   e.dashboardCopyLinks = {removed:[{id:'card', pageId:'overview'}]}
   e.dashboardPageId = 'overview'
   e.dashboardPreview = true
   e.selectedPreviewVisual = 'removed'
   e.visitedVisuals = ['removed']
   await e.updateComplete
  }, windowedTablePreviewEnvelope())
  expect(await chat.locator('lv-report-table').count()).toBe(0)
  expect(await chat.getByText('Add this table back to the dashboard to view, sort, or load more rows.').isVisible()).toBe(true)
  expect(await chat.getByRole('button',{name:'Add to dashboard',exact:true}).isVisible()).toBe(true)
  await chat.evaluate(async (e: any) => {
   e.dashboardCopies = {removed:{id:'card', pageId:'overview'}}
   await e.updateComplete
  })
  await chat.locator('lv-report-table .header-button').first().waitFor()
  expect(await chat.locator('lv-report-table .header-button').first().isEnabled()).toBe(true)
  expect(await chat.getByText('Add this table back to the dashboard to view, sort, or load more rows.').count()).toBe(0)
 } finally { await page.close() }
})

test('saving a generated visual retains removal until the builder is idle', async () => {
 const page = await fixture.browser.newPage()
 try {
  await page.goto(fixture.baseURL)
  const chat = page.locator('lv-chat-page')
  await chat.locator('lv-chat-composer').waitFor()
  const state = await chat.evaluate((e: any) => {
   e.dashboardRevisionId = 'revision'
   e.dashboardPageId = 'overview'
   e.builderUpdating = true
   e.pendingVisualRemoval = {artifactId: 'generated', revisionId: 'revision', pageId: 'overview'}
   e.dashboardComponents = [{id: 'card', pageId: 'overview', artifactId: 'generated', savedVisualId: 'saved-1'}]
   e.handleVisualLibraryState(new CustomEvent('lv-visual-library-state', {detail: {savedIds:['generated'], libraryIds:{generated:'saved-1'}, savingId:'', error:''}}))
   return {pending:e.pendingVisualRemoval, change:e.pendingDashboardChange}
  })
  expect(state.pending?.artifactId).toBe('generated')
  expect(state.change).toBeNull()
  const finished = await chat.evaluate((e: any) => {
   e.builderUpdating = false
   e.savedBuilderHref = '/dashboards/demo/edit'
   e.finishPendingVisualRemoval()
   return {pending:e.pendingVisualRemoval, change:e.pendingDashboardChange}
  })
  expect(finished.pending).toBeNull()
  expect(finished.change).toMatchObject({artifactId:'generated', componentId:'card', remove:true})
 } finally { await page.close() }
})

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

for (const authored of ["create_dashboard_draft", "edit_dashboard_source"]) for (const inspectedAgain of [false, true]) test(`reopening a ${authored} reply loads all visual cards without leaving chat${inspectedAgain ? ' after a later read-only preview' : ''}`, async () => {
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
  await chat.evaluate(async (e:any, {inspectedAgain, authored}) => {
   const {mergePatch} = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
   mergePatch({agent:{status:{enabled:true,running:false},transcript:[
    ...[authored,'preview_dashboard_draft'].map(name => ({id:name,kind:'tool',name,runId:'saved-build',toolCallId:name,status:'complete',argumentsJson:name===authored ? '{"source":"truncated…' : '{"dashboardId":"demo"}'})),
    {id:'answer',kind:'assistant',text:'Your dashboard is ready.\n\nFull explanation.'},
    ...(inspectedAgain ? [{id:'inspection',kind:'tool',name:'preview_dashboard_draft',runId:'later-inspection',toolCallId:'inspection',status:'complete'}] : []),
   ]}})
   await e.updateComplete
  }, {inspectedAgain, authored})
  await page.waitForFunction(() => Boolean((document.querySelector('lv-chat-page') as any).restoredBuilderHref))
  expect(await chat.evaluate((e:any) => e.restoredBuilderHref)).toContain('/actions/preview_dashboard_draft/open?run=saved-build')
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


test('an unfinished newer dashboard does not restore unrelated older chart cards', async () => {
 const page = await fixture.browser.newPage()
 try {
  let loads = 0
  await page.route('**/chats/*/actions/*/open?*', route => { loads++; return route.fulfill({contentType:'text/html',body:'Older dashboard'}) })
  await page.goto(fixture.baseURL)
  const chat = page.locator('lv-chat-page')
  await chat.locator('lv-chat-composer').waitFor()
  const restored = await chat.evaluate(async (e:any) => {
   const {mergePatch} = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
   mergePatch({agent:{status:{enabled:true,running:false},transcript:[
    ...['create_dashboard_draft','preview_dashboard_draft'].map(name=>({id:name,kind:'tool',name,runId:'older-build',toolCallId:name,status:'complete'})),
    {id:'new-create',kind:'tool',name:'create_dashboard_draft',runId:'new-build',toolCallId:'new-create',status:'complete'},
    {id:'new-preview',kind:'tool',name:'preview_dashboard_draft',runId:'new-build',toolCallId:'new-preview',status:'error',error:'Preview failed'},
   ]}})
   await e.updateComplete
   return e.restoredBuilderHref
  })
  expect(restored).toBeUndefined()
  expect(loads).toBe(0)
  expect(await chat.evaluate((e:any)=>e.builderOpen)).toBe(false)
 } finally { await page.close() }
})


for (const creation of [true, false]) test(`${creation ? 'new' : 'existing'} generated dashboard arranges only new pages after a ready projection`, async () => {
 const page = await fixture.browser.newPage()
 try {
  await page.route('**/chats/*/actions/*/open?*', route => route.fulfill({contentType:'text/html',body:'<lv-dashboard-builder>Generated</lv-dashboard-builder><script>window.arrangements=[];addEventListener("message",event=>{if(event.data.type==="lv-arrange-dashboard-visuals")window.arrangements.push(event.data)})</script>'}))
  await page.goto(fixture.baseURL)
  const chat = page.locator('lv-chat-page')
  await chat.locator('lv-chat-composer').waitFor()
  await chat.evaluate(async(e:any) => {
   const {mergePatch}=await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
   mergePatch({agent:{status:{enabled:true,running:true,runId:'new-build'}}})
   await e.updateComplete
  })
  await chat.evaluate(async(e:any, creation) => {
   const {mergePatch}=await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
   mergePatch({agent:{status:{enabled:true,running:false,runId:'new-build'},transcript:[...(creation ? ['create_dashboard_draft'] : []),'edit_dashboard_source','preview_dashboard_draft'].map(name=>({id:name,kind:'tool',name,runId:'new-build',toolCallId:name,status:'complete',resultJson:JSON.stringify({dashboardId:'demo'})}))}})
   await e.updateComplete
  },creation)
  const frame = page.frameLocator('.builder-frame')
  await frame.getByText('Generated').waitFor()
  for (const updating of [true, false, false]) {
   await frame.locator('body').evaluate((_, updating) => {
    window.parent.postMessage({type:'lv-builder-saved',canArrange:true,updating,revisionId:'rev',pageId:'overview',href:'/dashboards/demo/edit?embed=chat&page=overview',reference:{reference:{kind:'dashboard',id:'demo'},name:'Demo',hierarchy:[],locations:[],context:[]},components:[],artifacts:[],visuals:{}},window.parent.location.origin)
   },updating)
   await chat.evaluate(async(e:any)=>e.updateComplete)
  }
  await page.waitForFunction(()=>(document.querySelector('lv-chat-page') as any).savedBuilderHref.includes('/dashboards/demo/edit'))
  expect(await frame.locator('body').evaluate(()=>(window as any).arrangements)).toEqual(creation ? [{type:'lv-arrange-dashboard-visuals',reflow:true}] : [])
 } finally {await page.close()}
})

test('generated charts can be removed independently and added back after other edits', async () => {
 const page = await fixture.browser.newPage()
 try {
  const saves: URLSearchParams[] = []
  let saveError = ''
  const library: Array<{id:string;title:string;semanticModelId:string;sourceKey:string}> = []
  await page.route('**/visuals/saved', async route => {
   const form = new URLSearchParams(route.request().postData() ?? '')
   if (route.request().method() === 'POST') {
    saves.push(form)
    if (!saveError && !library.some(item=>item.sourceKey===form.get('sourceKey'))) library.push({id: 'saved-'+form.get('componentId'), title: form.get('title')!, semanticModelId:'sales', sourceKey:form.get('sourceKey')!})
   }
   const last = library.at(-1)
   const saved = library.find(item=>item.sourceKey===form.get('sourceKey')) ?? last
   return route.fulfill({contentType:'text/html',body:`<lv-saved-visual-library></lv-saved-visual-library><script>parent.postMessage({type:'lv-saved-visual-library',library:${JSON.stringify({visuals:library,sourceKey:saved?.sourceKey??'',savedId:saved?.id??'',error:saveError})}},location.origin)</script>`})
  })
  await page.route('**/dashboards/demo/edit?*', route => route.fulfill({contentType:'text/html',body:'<lv-dashboard-builder></lv-dashboard-builder><script>window.requests=[];addEventListener("message",event=>window.requests.push(event.data))</script>'}))
  await page.goto(fixture.baseURL)
  const chat = page.locator('lv-chat-page')
  await chat.locator('lv-chat-composer').waitFor()
  await chat.evaluate(async (e:any) => {e.savedBuilderHref='/dashboards/demo/edit?embed=chat&page=overview';e.restoredBuilderHref=e.savedBuilderHref;await e.updateComplete})
  const body = page.frameLocator('.builder-frame').locator('body')
  await body.waitFor({state:'attached'})
  const original: Array<{id:string;pageId:string;artifactId:string;savedVisualId?:string}> = [{id:'pie',pageId:'overview',artifactId:'pie'},{id:'bar',pageId:'overview',artifactId:'bar'}]
  const project = async (components: typeof original, revisionId: string) => {
   await body.evaluate((_, {components,revisionId}) => {
    parent.postMessage({type:'lv-builder-saved',revisionId,pageId:'overview',pageTitle:'Overview',href:'/dashboards/demo/edit?embed=chat&page=overview',reference:{reference:{kind:'dashboard',id:'demo'},name:'Demo',hierarchy:[],locations:[],context:[]},components,artifacts:components.map(c=>({id:c.artifactId,type:'bar',summary:c.artifactId==='pie'?'Revenue mix':'Revenue trend'})),visuals:{}},location.origin)
   },{components,revisionId})
   await page.waitForFunction(revision => (document.querySelector('lv-chat-page') as any).dashboardRevisionId===revision,revisionId)
  }
  await project(original,'rev-1')
  await page.getByRole('button',{name:'Open Revenue mix in visuals sidebar'}).click()
  await page.getByRole('button',{name:'Remove from dashboard',exact:true}).click()
  await page.waitForFunction(()=> (document.querySelector('lv-chat-page') as any).builderFrame.contentWindow.requests.some((r:any)=>r.type==='lv-remove-dashboard-visual'&&r.componentId==='pie'))
  expect(saves[0].get('dashboardId')).toBe('demo')
  expect(saves[0].get('revisionId')).toBe('rev-1')
  expect(saves[0].get('definition')).toBeNull()
  await project([original[1]],'rev-2')
  expect(await page.getByRole('button',{name:'Open Revenue mix in visuals sidebar'}).count()).toBe(1)
  expect(await page.getByRole('button',{name:'Add to dashboard',exact:true}).isVisible()).toBe(true)
  await page.getByRole('button',{name:'Open Revenue trend in visuals sidebar'}).click()
  await page.getByRole('button',{name:'Remove from dashboard',exact:true}).click()
  await page.waitForFunction(()=> (document.querySelector('lv-chat-page') as any).builderFrame.contentWindow.requests.filter((r:any)=>r.type==='lv-remove-dashboard-visual').length===2)
  expect(saves[1].get('componentId')).toBe('bar')
  expect(saves[1].get('revisionId')).toBe('rev-2')
  await project([],'rev-3')
  await page.getByRole('button',{name:'Open Revenue mix in visuals sidebar'}).click()
  await page.getByRole('button',{name:'Add to dashboard',exact:true}).click()
  await page.waitForFunction(()=> (document.querySelector('lv-chat-page') as any).builderFrame.contentWindow.requests.some((r:any)=>r.type==='lv-add-saved-visual'))
  const request = await chat.evaluate((e:any)=>e.builderFrame.contentWindow.requests.find((r:any)=>r.type==='lv-add-saved-visual'))
  expect(request.id).toBe('saved-pie')
  expect(request.pageId).toBe('overview')
  const id = await chat.evaluate((e:any)=>e.pendingDashboardChange.componentId)
  await project([{id,pageId:'overview',artifactId:'imported-pie',savedVisualId:'saved-pie'}] as typeof original,'rev-4')
  expect(await page.getByRole('button',{name:'Open Revenue mix in visuals sidebar'}).count()).toBe(1)
  expect(await chat.evaluate((e:any)=>Object.keys(e.dashboardCopies))).toContain('pie')
  expect(await chat.evaluate((e:any)=>e.dashboardCopies.bar)).toBeUndefined()
  expect(await page.getByRole('button',{name:'Remove from dashboard',exact:true}).isVisible()).toBe(true)
  expect(saves.length).toBe(2)
  await page.getByRole('button',{name:'Remove from dashboard',exact:true}).click()
  await page.waitForFunction(()=> (document.querySelector('lv-chat-page') as any).builderFrame.contentWindow.requests.filter((r:any)=>r.type==='lv-remove-dashboard-visual').length===3)
  expect(saves[2].get('componentId')).toBe(id)
  expect(saves[2].get('revisionId')).toBe('rev-4')
  await project([],'rev-5')
  await page.getByRole('button',{name:'Add to dashboard',exact:true}).click()
  await page.waitForFunction(()=> (document.querySelector('lv-chat-page') as any).builderFrame.contentWindow.requests.filter((r:any)=>r.type==='lv-add-saved-visual').length===2)
  const restoredId = await chat.evaluate((e:any)=>e.pendingDashboardChange.componentId)
  await project([{id:restoredId,pageId:'overview',artifactId:'second-pie',savedVisualId:'saved-pie'}],'rev-6')
  saveError = 'The dashboard changed. Refresh it before saving this visual.'
  await page.getByRole('button',{name:'Remove from dashboard',exact:true}).click()
  await page.getByRole('alert').getByText(saveError).waitFor()
  expect(await chat.evaluate((e:any)=>e.builderFrame.contentWindow.requests.filter((r:any)=>r.type==='lv-remove-dashboard-visual').length)).toBe(3)
  expect(await page.getByRole('button',{name:'Open Revenue mix in visuals sidebar'}).count()).toBe(1)
 } finally {await page.close()}
})

test('dashboard chart cards stay with their answer and share its copy action', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.goto(fixture.baseURL)
    await page.locator('lv-chat-composer').waitFor()
    await page.locator('lv-chat-thread').evaluate(async (thread: any) => {
      thread.status = { enabled: true, running: false }
      thread.conversationId = 'conversation'
      thread.dashboardPreviewAvailable = true
      thread.dashboardId = 'finance'
      thread.pageArtifacts = [{ id: 'revenue', type: 'bar', summary: 'Revenue' }, { id: 'margin', type: 'kpi', summary: 'Margin' }]
      thread.transcript = [
        { id: 'other-user', kind: 'user', text: 'Inspect another dashboard' },
        { id: 'other-preview', kind: 'tool', name: 'preview_dashboard_draft', toolCallId: 'other', status: 'complete', argumentsJson: '{"dashboardId":"other"}' },
        { id: 'other-answer', kind: 'assistant', text: 'The other dashboard is available.' },
        { id: 'user', kind: 'user', text: 'Build a finance dashboard' },
        { id: 'edit', kind: 'tool', name: 'edit_dashboard_source', toolCallId: 'edit', status: 'complete', argumentsJson: '{"dashboardId":"finance"}' },
        { id: 'preview', kind: 'tool', name: 'preview_dashboard_draft', toolCallId: 'preview', status: 'complete', argumentsJson: '{"dashboardId":"finance"}' },
        { id: 'answer', kind: 'assistant', text: 'Your finance dashboard is ready.\n\nIt includes revenue and margin.' },
        { id: 'later-user', kind: 'user', text: 'Hello' },
        { id: 'later-answer', kind: 'assistant', text: 'Hello again.' },
      ]
      await thread.updateComplete
    })
    const reply = page.locator('.agent-turn').filter({ hasText: 'Your finance dashboard is ready.' })
    expect(await reply.getByRole('button', { name: 'Open Revenue in visuals sidebar' }).count()).toBe(1)
    expect(await reply.getByRole('button', { name: 'Open Margin in visuals sidebar' }).count()).toBe(1)
    expect(await reply.getByRole('group', { name: 'Answer actions' }).count()).toBe(1)
    expect(await reply.getByRole('link', { name: 'Open in Builder' }).count()).toBe(0)
    await reply.locator('.run-steps summary').click()
    expect(await reply.getByRole('link', { name: 'Open in Builder' }).count()).toBe(0)
    expect(await page.getByRole('link', { name: 'Open in Builder' }).count()).toBe(1)
    expect(await page.locator('.agent-turn').filter({ hasText: 'Hello again.' }).locator('.visual-reference').count()).toBe(0)
    expect(await page.locator('lv-chat-thread').locator('.stack > .page-visuals').count()).toBe(0)
  } finally { await page.close() }
})
