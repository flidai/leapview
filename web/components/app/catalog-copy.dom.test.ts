import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testDocument } from './catalog-page.test-fixture'

let server: Server, browser: Browser, baseURL = ''
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/catalog-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : root
    const file = normalize(join(fileRoot, url.pathname))
    if (!file.startsWith(fileRoot)) {
      response.writeHead(404).end('not found')
      return
    }
    try {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(file))
    } catch {
      response.writeHead(404).end('not found')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('managed dashboard copy opens an in-page dialog without a slug field', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { ...element.page, dashboards: element.page.dashboards.map((dashboard: any, index: number) => index === 0 ? { ...dashboard, dashboardId: 'dashboard:cfo-command-center' } : dashboard) } })
      element.setAttribute('create-draft-csrf-token', 'csrf-copy')
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const list = root.querySelector('lv-entity-list') as any
      await list.updateComplete
      ;(list.querySelector('.entity-list-row-action') as HTMLButtonElement).click()
      await element.updateComplete
      const menu = Array.from(root.querySelectorAll('[role="menuitem"]')).map((item: Element) => ({ label: item.textContent?.trim(), href: item.getAttribute('href') }))
      ;(root.querySelector('[role="menuitem"]') as HTMLButtonElement).click()
      await element.updateComplete
      await new Promise<void>((resolve) => setTimeout(resolve, 10))
      const dialog = root.querySelector('#catalog-copy-draft-dialog') as HTMLDialogElement
      const form = dialog.querySelector('form') as HTMLFormElement
      const result = {
        menu, dialogOpen: dialog.open, title: dialog.querySelector('h2')?.textContent?.trim(),
        action: new URL(form.action).pathname, method: form.method,
        defaultTitle: dialog.querySelector<HTMLInputElement>('[name="title"]')?.value,
        slugFields: dialog.querySelectorAll('[name="slug"]').length,
        csrf: dialog.querySelector<HTMLInputElement>('[name="gorilla.csrf.Token"]')?.value,
        idempotencyIsUUIDv7: /^\w{8}-\w{4}-7\w{3}-[89ab]\w{3}-\w{12}$/i.test(dialog.querySelector<HTMLInputElement>('[name="idempotencyKey"]')?.value ?? ''),
      }
      ;(dialog.querySelector('.catalog-create-dialog-actions button[type="button"]') as HTMLButtonElement).click()
      await element.updateComplete
      await new Promise<void>((resolve) => queueMicrotask(resolve))
      return { ...result, cancelRestoresMenuTrigger: root.activeElement === list.querySelector('.entity-list-row-action') }
    })

    expect(state).toEqual({
      menu: [{ label: 'Make an editable copy', href: null }, { label: 'View details', href: null }, { label: 'Copy link', href: null }],
      dialogOpen: true, title: 'Make a copy', action: '/dashboards/dashboard:cfo-command-center/fork', method: 'post',
      defaultTitle: 'Executive Sales Dashboard copy', slugFields: 0, csrf: 'csrf-copy', idempotencyIsUUIDv7: true,
      cancelRestoresMenuTrigger: true,
    })
  } finally {
    await page.close()
  }
})
