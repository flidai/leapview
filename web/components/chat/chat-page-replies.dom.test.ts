import { expect, test } from 'bun:test'
import { chatPageBrowserFixture } from './chat-page-browser.test-fixture'

const fixture = chatPageBrowserFixture()

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
