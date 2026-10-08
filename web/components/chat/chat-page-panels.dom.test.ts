import { expect, test } from 'bun:test'
import { chatPageBrowserFixture, openDashboardTestVisual } from './chat-page-browser.test-fixture'

const fixture = chatPageBrowserFixture()

test('shrinking chat switches visual panels without creating an empty dashboard', async () => {
 const page = await fixture.browser.newPage({viewport:{width:1400,height:900}})
 try {
  let creates = 0
  await page.route('**/dashboards/new', route => {creates++;return route.fulfill({contentType:'text/html',body:'<lv-dashboard-builder></lv-dashboard-builder>'})})
  await page.route('**/chats/c1/visuals/chart-dashboard/dashboards', route => route.fulfill({json:{dashboards:[],canCreate:true}}))
  await openDashboardTestVisual(page, fixture.baseURL)
  const chat = page.locator('lv-chat-page')
  await page.getByRole('button',{name:'Close add to dashboard',exact:true}).click()
  expect(await chat.locator('lv-chat-visual-panel').count()).toBe(1)
  await page.getByRole('button',{name:'Shrink chat',exact:true}).click()
  expect(await chat.locator('lv-chat-visual-panel').count()).toBe(0)
  expect(await page.getByRole('region',{name:'Dashboard preview'}).isVisible()).toBe(true)
  expect(creates).toBe(0)
  await chat.evaluate(async(e:any)=>{e.openVisual(new CustomEvent('lv-chat-visual-open',{detail:{artifactId:'chart-dashboard',title:'Net sales by country'}}));await e.updateComplete})
  expect(await chat.locator('lv-chat-visual-panel').count()).toBe(0)
  await chat.evaluate(async(e:any)=>{e.savedBuilderHref='/dashboards/demo/edit?embed=chat&page=overview';await e.updateComplete})
  await page.getByRole('button',{name:'Preview dashboard',exact:true}).click()
  expect(await page.getByRole('region',{name:'Dashboard builder'}).isVisible()).toBe(true)
  expect(await chat.locator('lv-chat-visual-panel').count()).toBe(0)
  expect(creates).toBe(0)
 } finally {await page.close()}
})
