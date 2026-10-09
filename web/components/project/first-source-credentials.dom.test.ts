import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser, type Page } from '@playwright/test'

let server: Server, browser: Browser, baseURL = ''
const root = join(process.cwd(), '.tmp/project-page-test')
beforeAll(async () => {
  server = createServer(async (request,response) => {
    const path = new URL(request.url ?? '/','http://localhost').pathname
    if (path === '/') { response.setHeader('content-type','text/html'); response.end('<!doctype html><script type="module" src="/project-page-under-test.js"></script><lv-first-source-credentials connection-id="connection:warehouse" source-host="db.example.test" source-database="warehouse" source-identity="reader"></lv-first-source-credentials>'); return }
    const fileRoot = path.startsWith('/static/vendor/') ? process.cwd() : root
    const file = normalize(join(fileRoot,path))
    if (!file.startsWith(fileRoot+'/')) { response.writeHead(404).end(); return }
    try { response.setHeader('content-type','text/javascript'); response.end(await readFile(file)) } catch { response.writeHead(404).end() }
  })
  await new Promise<void>(resolve=>server.listen(0,'127.0.0.1',resolve))
  const address = server.address(); if (!address || typeof address==='string') throw new Error('no fixture address')
  baseURL=`http://127.0.0.1:${address.port}`; browser=await chromium.launch()
})
afterAll(async()=>{ await browser?.close(); await new Promise<void>((resolve,reject)=>server.close(e=>e?reject(e):resolve())) })
async function open(page:Page) {
  await page.goto(baseURL); await page.waitForFunction(()=>customElements.get('lv-first-source-credentials'))
  await page.locator('lv-first-source-credentials').evaluate(async (el:any)=>{
    el.fixture={command:{action:'',versionId:'version-one',receiptId:'',operationId:''},drafts:[{versionId:'version-one',createdAt:'today'}]}
    el.signal=()=>el.fixture; (window as any).commands=[]
    for(const name of ['lv-first-source-command','lv-first-source-query'])el.addEventListener(name,(event:CustomEvent)=>(window as any).commands.push({...event.detail}))
    el.requestUpdate(); await el.updateComplete
  })
}
test('first-source password clears immediately and is not emitted by later metadata reads',async()=>{
  const page=await browser.newPage()
  try {
    await open(page)
    await page.getByLabel('Password',{exact:true}).fill('private-first-source-password')
    await page.getByRole('button',{name:'Save draft',exact:true}).click()
    expect(await page.getByLabel('Password',{exact:true}).inputValue()).toBe('')
    expect(await page.evaluate(()=>(window as any).commands[0])).toMatchObject({action:'save',password:'private-first-source-password'})
    await page.locator('lv-first-source-credentials').evaluate((el:any)=>{el.pending=false;el.requestUpdate()})
    await page.getByRole('button',{name:'Refresh drafts'}).click()
    expect(await page.evaluate(()=>(window as any).commands[1])).toMatchObject({action:'list',password:''})
  } finally {await page.close()}
})
test('preparation requires a fresh receipt for the selected version and preserves its new operation identity',async()=>{
  const page=await browser.newPage()
  try {
    await open(page)
    const prepare=page.getByRole('button',{name:'Prepare first publication',exact:true})
    expect(await prepare.isDisabled()).toBe(true)
    await page.locator('lv-first-source-credentials').evaluate(async(el:any)=>{el.fixture.command.receiptId='receipt';el.fixture.receiptExpiresAt=new Date(Date.now()+60000).toISOString();el.requestUpdate();await el.updateComplete})
    await page.getByLabel('Source digest',{exact:true}).fill('sha256:'+'a'.repeat(64))
    await page.getByLabel('Source attestation digest',{exact:true}).fill('sha256:'+'b'.repeat(64))
    await page.getByLabel('Delivery plan key').fill('retained-plan')
    await prepare.click()
    const command=await page.evaluate(()=>(window as any).commands[0])
    expect(command).toMatchObject({action:'prepare',versionId:'version-one',receiptId:'receipt',planIdempotencyKey:'retained-plan',password:''})
    expect(command.operationId).toMatch(/^[0-9a-f-]{36}$/)
    expect(await page.getByLabel('Preparation ID',{exact:true}).first().inputValue()).toBe(command.operationId)
    expect(await prepare.isDisabled()).toBe(true)
  } finally {await page.close()}
})
test('a cancelled preparation does not become active again after refreshing drafts',async()=>{
  const page=await browser.newPage()
  try {
    await open(page)
    await page.locator('lv-first-source-credentials').evaluate(async(el:any)=>{
      el.operationId='cancelled-operation'
      el.fixture.command={action:'abort',versionId:'version-one',operationId:'cancelled-operation'}
      el.fixture.phase='aborted';el.requestUpdate();await el.updateComplete
    })
    await page.getByRole('button',{name:'Refresh drafts'}).click()
    expect(await page.evaluate(()=>(window as any).commands[0].operationId)).toBe('')
    await page.locator('lv-first-source-credentials').evaluate(async(el:any)=>{
      el.fixture.command=(window as any).commands[0];el.fixture.phase='';el.pending=false
      el.requestUpdate();await el.updateComplete
    })
    expect(await page.getByRole('button',{name:'Save draft',exact:true}).isEnabled()).toBe(true)
  } finally {await page.close()}
})
