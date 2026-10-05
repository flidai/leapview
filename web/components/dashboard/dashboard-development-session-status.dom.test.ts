import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testDocument } from './dashboard-page-test-fixtures'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const assetRoot = join(projectRoot, '.tmp/dashboard-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const pathname = new URL(request.url ?? '/', 'http://127.0.0.1').pathname
    if (pathname === '/dashboards/executive-sales/pages/overview') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument().replace('<lv-dashboard-page></lv-dashboard-page>', '<lv-dashboard-page development-session-events-path="/development-session/events"></lv-dashboard-page>'))
      return
    }
    const root = pathname.startsWith('/static/vendor/') ? projectRoot : assetRoot
    const file = normalize(join(root, pathname))
    if (!file.startsWith(root)) { response.writeHead(404); response.end('not found'); return }
    try {
      response.setHeader('content-type', file.endsWith('.css') ? 'text/css' : 'text/javascript')
      response.end(await readFile(file))
    } catch { response.writeHead(404); response.end('not found') }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  server.closeAllConnections()
  await new Promise<void>((resolve, reject) => server.close((error: NodeJS.ErrnoException | undefined) => {
    if (error && error.code !== 'ERR_SERVER_NOT_RUNNING') reject(error)
    else resolve()
  }))
}, 30_000)

test('ordinary local dashboard shows invalid edit diagnostics and reloads after a valid activation', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => {
      class LocalEvents {
        constructor(readonly url: string) { (window as any).__lvEventsPath = url }
        addEventListener(type: string, listener: EventListener) {
          if (type === 'development-session') (window as any).__lvSessionListener = listener
        }
        close() {}
      }
      Object.defineProperty(window, 'EventSource', { configurable: true, value: LocalEvents })
    })
    await page.goto(`${baseURL}/dashboards/executive-sales/pages/overview`)
    await page.waitForFunction(() => typeof (window as any).__lvSessionListener === 'function')
    expect(await page.evaluate(() => (window as any).__lvEventsPath)).toBe('/development-session/events')
    await page.evaluate(() => {
      const emit = (record: unknown) => (window as any).__lvSessionListener(new MessageEvent('development-session', { data: JSON.stringify(record) }))
      emit({ revision: 1, lastValid: { candidateId: 'candidate-a', artifactDigest: 'artifact-a', graphDigest: 'graph-a' } })
      emit({ revision: 2, attempted: { artifactDigest: 'artifact-b', graphDigest: 'graph-b' }, lastValid: { candidateId: 'candidate-a', artifactDigest: 'artifact-a', graphDigest: 'graph-a' }, diagnostics: [{ path: 'dashboards/sales.yaml', line: 9, message: 'Invalid YAML' }] })
    })
    const status = page.locator('lv-dashboard-page lv-dashboard-development-session-status')
    await page.waitForFunction(() => document.querySelector('lv-dashboard-page')?.shadowRoot?.querySelector('lv-dashboard-development-session-status')?.hasAttribute('visible'))
    expect(await status.evaluate((element) => element.shadowRoot?.textContent)).toContain('Local changes not applied')
    expect(await status.evaluate((element) => element.shadowRoot?.textContent)).toContain('dashboards/sales.yaml:9')
    const reloaded = page.waitForEvent('load')
    await page.evaluate(() => (window as any).__lvSessionListener(new MessageEvent('development-session', { data: JSON.stringify({ revision: 3, lastValid: { candidateId: 'candidate-b', artifactDigest: 'artifact-b', graphDigest: 'graph-b' } }) })))
    await reloaded
    await page.waitForFunction(() => typeof (window as any).__lvSessionListener === 'function')
    expect(await page.locator('lv-dashboard-page').getAttribute('development-session-events-path')).toBe('/development-session/events')
  } finally {
    await page.close()
  }
}, 30_000)
