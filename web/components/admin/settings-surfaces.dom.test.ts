import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let browser: Browser
let baseURL = ''
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/settings-surfaces-test')

beforeAll(async () => {
  await Bun.$`mkdir -p ${root}`
  const result = await Bun.build({ entrypoints: [join(projectRoot, 'web/components/admin/settings-surfaces.ts')], outdir: root, naming: 'settings-surfaces.js', target: 'browser', minify: false })
  if (!result.success) throw new Error('settings surface bundle failed')
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end('<!doctype html><main><lv-workspace-registry></lv-workspace-registry><lv-service-accounts></lv-service-accounts><lv-audit-log></lv-audit-log><script type="module" src="/settings-surfaces.js"></script></main>')
      return
    }
    const file = normalize(join(root, url.pathname))
    if (!file.startsWith(root)) { response.writeHead(404); response.end(); return }
    try { response.setHeader('content-type', 'text/javascript'); response.end(await readFile(file)) } catch { response.writeHead(404); response.end() }
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
})

test('settings surfaces render typed signals and emit commands', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-service-accounts'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminServiceAccounts: { items: [{ id: 'svc-1', displayName: 'CI', kind: 'service_principal' }], secrets: [] } }, getPath: (path: string) => (path === 'adminServiceAccounts' ? { items: [{ id: 'svc-1', displayName: 'CI', kind: 'service_principal' }], secrets: [] } : undefined), effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-service-accounts') as any
      element.requestUpdate()
      await element.updateComplete
      let detail: unknown = null
      element.addEventListener('lv-service-account-command', (event: CustomEvent) => { detail = event.detail })
      ;(element.shadowRoot.querySelector('tbody button') as HTMLButtonElement).click()
      return { text: element.shadowRoot.textContent?.replace(/\s+/g, ' ').trim(), detail }
    })
    expect(result.text).toContain('CI')
    expect(result.detail).toEqual({ action: 'select', accountId: 'svc-1' })
  } finally { await page.close() }
})
