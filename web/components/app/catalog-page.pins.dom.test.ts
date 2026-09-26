import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { testDocument } from './catalog-page.test-fixture'

let server: Server, browser: Browser
let baseURL = ''
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

test('dashboard pins appear in the catalog only while dashboards are pinned', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    const section = page.locator('lv-catalog-page .pinned-dashboards')
    expect(await section.count()).toBe(0)

    await page.getByRole('button', { name: 'Pin Operations Health', exact: true }).click()
    await section.getByRole('link', { name: 'Operations Health' }).waitFor()
    await page.getByRole('button', { name: 'Pin Executive Sales Dashboard', exact: true }).click()
    expect(await section.getByRole('link').count()).toBe(2)
    expect(await section.getByRole('link', { name: 'Executive Sales Dashboard' }).getAttribute('href')).toBe('/dashboards/executive-sales')
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem('leapview.dashboard-catalog.pins.v1') ?? '[]'))).toEqual(['operations-health', 'executive-sales'])

    await page.reload()
    await section.getByRole('link', { name: 'Executive Sales Dashboard' }).waitFor()
    await section.getByRole('button', { name: 'Unpin Operations Health' }).click()
    expect(await section.getByRole('link', { name: 'Operations Health' }).count()).toBe(0)
    await section.getByRole('button', { name: 'Unpin Executive Sales Dashboard' }).click()
    await section.waitFor({ state: 'detached' })
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem('leapview.dashboard-catalog.pins.v1') ?? '[]'))).toEqual([])
  } finally {
    await page.close()
  }
})
