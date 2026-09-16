import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testDocument } from './project-page.dom.fixture'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/project-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument(url.searchParams.get('root') ?? 'pipelines'))
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : root
    const file = normalize(join(fileRoot, url.pathname))
    if (!file.startsWith(fileRoot)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(file))
    } catch {
      response.writeHead(404)
      response.end('not found')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('pipeline catalog and run monitor are separate list surfaces without local tabs', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(`${baseURL}/?root=pipelines`)
    await page.waitForFunction(() => customElements.get('lv-pipelines-page'))
    const catalog = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      return { title: root.querySelector('h1')?.textContent?.trim(), metrics: root.querySelectorAll('.metrics').length, tabs: root.querySelectorAll('.tabs').length, lists: root.querySelectorAll('lv-entity-list').length }
    })
    expect(catalog).toEqual({ title: 'Pipelines', metrics: 0, tabs: 0, lists: 1 })

    await page.goto(`${baseURL}/?root=runs`)
    const monitor = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot!
      const form = root.querySelector('.run-toolbar') as HTMLFormElement
      return { title: root.querySelector('h1')?.textContent?.trim(), metrics: root.querySelectorAll('.metric').length, tabs: root.querySelectorAll('.tabs').length,
        filters: root.querySelectorAll('.run-toolbar input, .run-toolbar select').length, action: form?.getAttribute('action'),
        range: (form?.querySelector('[name="range"]') as HTMLSelectElement)?.value, pageLink: root.querySelector('.run-pagination a')?.getAttribute('href') }
    })
    expect(monitor).toEqual({ title: 'Runs', metrics: 3, tabs: 0, filters: 4, action: '/runs', range: '7d', pageLink: '/runs?q=sales&range=7d&status=failed&trigger=manual&page=1' })
    const detail = await page.locator('lv-pipelines-page').evaluate(async (element: any) => {
      element.selectedRunID = 'run-failed'
      await element.updateComplete
      const root = element.shadowRoot!
      return { firstSection: root.querySelector('.run-detail-section h2')?.textContent?.trim(),
        actions: [...root.querySelectorAll('.run-detail-actions a, .run-detail-actions button')].map((item) => item.textContent?.trim()),
        error: root.querySelector('.run-detail-error')?.textContent?.trim() }
    })
    expect(detail).toEqual({ firstSection: 'Error', actions: ['View pipeline', 'Run again'], error: 'Source unavailable' })
  } finally {
    await page.close()
  }
}, 15_000)
