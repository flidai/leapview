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

for (const inspectedAgain of [false, true]) test(`reopening a generated dashboard reply loads all visual cards without leaving chat${inspectedAgain ? ' after a later read-only preview' : ''}`, async () => {
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
  await chat.evaluate(async (e:any, inspectedAgain) => {
   const {mergePatch} = await import('/static/vendor/datastar-1.0.2.js?v=dev' as string)
   mergePatch({agent:{status:{enabled:true,running:false},transcript:[
    ...['create_dashboard_draft','preview_dashboard_draft'].map(name => ({id:name,kind:'tool',name,runId:'saved-build',toolCallId:name,status:'complete'})),
    {id:'answer',kind:'assistant',text:'Your dashboard is ready.\n\nFull explanation.'},
    ...(inspectedAgain ? [{id:'inspection',kind:'tool',name:'preview_dashboard_draft',runId:'later-inspection',toolCallId:'inspection',status:'complete'}] : []),
   ]}})
   await e.updateComplete
  }, inspectedAgain)
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
