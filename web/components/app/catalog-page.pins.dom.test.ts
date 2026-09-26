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

    const pin = page.getByRole('button', { name: 'Pin Operations Health', exact: true })
    const actionCell = pin.locator('xpath=ancestor::td')
    expect(await actionCell.count()).toBe(1)
    expect(await pin.evaluate((button) => getComputedStyle(button).opacity)).toBe('0')
    await pin.focus()
    expect(await pin.evaluate((button) => getComputedStyle(button).opacity)).toBe('1')
    await pin.evaluate((button: HTMLElement) => button.blur())
    await actionCell.locator('xpath=ancestor::tr').hover()
    expect(await pin.evaluate((button) => getComputedStyle(button).opacity)).toBe('1')

    await pin.click()
    await section.getByRole('link', { name: 'Operations Health' }).waitFor()
    expect(await page.locator('lv-catalog-page .entity-list-row-pin[aria-pressed="true"]').first().evaluate((button) => getComputedStyle(button).opacity)).toBe('1')
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

test('eight dashboard copies can be favorited and pinned independently', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    const addCopies = () => page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const source = element.page.dashboards[0]
      const copies = Array.from({ length: 8 }, (_, index) => ({
        ...source,
        id: `sales-copy-${index + 1}`,
        dashboardId: `sales-copy-${index + 1}`,
        title: `Sales copy ${index + 1}`,
        href: `/dashboards/sales-copy-${index + 1}`,
        catalogScope: 'mine',
        status: 'private',
      }))
      mergePatch({ page: { ...element.page, dashboards: [...element.page.dashboards, ...copies] } })
      await element.updateComplete
    })
    await addCopies()

    const catalog = page.locator('lv-catalog-page')
    const pinned = catalog.getByRole('region', { name: 'Pinned dashboards' })
    await catalog.getByRole('tab', { name: 'My dashboards' }).click()
    expect(await catalog.locator('lv-entity-list tbody tr').count()).toBe(8)

    for (const index of [1, 8]) {
      await catalog.getByRole('button', { name: `Add Sales copy ${index} to favorites` }).click()
    }
    for (let index = 1; index <= 8; index++) {
      const row = catalog.locator('lv-entity-list tbody tr').filter({ hasText: `Sales copy ${index}` })
      await row.locator('.entity-list-row-pin').evaluate((button: HTMLButtonElement) => button.click())
    }

    await catalog.getByRole('tab', { name: 'All dashboards' }).click()
    expect(await pinned.getByRole('link').count()).toBe(8)
    await catalog.getByRole('tab', { name: 'Favorites' }).click()
    expect(await catalog.locator('lv-entity-list tbody tr').count()).toBe(2)
    expect(await pinned.count()).toBe(0)

    await page.reload()
    await addCopies()
    expect(await pinned.getByRole('link').count()).toBe(8)
    for (let index = 1; index <= 8; index++) {
      await pinned.getByRole('button', { name: `Unpin Sales copy ${index}` }).evaluate((button: HTMLButtonElement) => button.click())
    }
    await pinned.waitFor({ state: 'detached' })
    await catalog.getByRole('tab', { name: 'My dashboards' }).click()
    expect(await catalog.getByRole('button', { name: 'Remove Sales copy 1 from favorites' }).count()).toBe(1)
    expect(await catalog.getByRole('button', { name: 'Remove Sales copy 8 from favorites' }).count()).toBe(1)
  } finally {
    await page.close()
  }
}, 30_000)
