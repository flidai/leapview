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
    const allDashboards = page.locator('lv-catalog-page lv-entity-list[list-label="All dashboards"]')
    expect(await section.count()).toBe(0)
    expect(await allDashboards.locator('tbody tr').count()).toBe(4)

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
    expect(await section.getByRole('table', { name: 'Pinned dashboards' }).count()).toBe(1)
    expect(await section.getByRole('searchbox').count()).toBe(0)
    expect(await section.getByRole('columnheader', { name: 'Data model' }).count()).toBe(1)
    expect(await section.getByRole('columnheader', { name: 'Owner' }).count()).toBe(1)
    expect(await section.getByRole('columnheader', { name: 'Popularity' }).count()).toBe(1)
    expect(await section.locator('tbody tr').count()).toBe(1)
    expect(await allDashboards.locator('tbody tr').count()).toBe(3)
    expect(await section.locator('.entity-list-row-pin[aria-pressed="true"]').first().evaluate((button) => getComputedStyle(button).opacity)).toBe('1')
    await page.getByRole('button', { name: 'Pin Executive Sales Dashboard', exact: true }).click()
    expect(await section.getByRole('link').count()).toBe(2)
    expect(await allDashboards.locator('tbody tr').count()).toBe(2)
    expect(await section.getByRole('link', { name: 'Executive Sales Dashboard' }).getAttribute('href')).toBe('/dashboards/executive-sales')
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem('leapview.dashboard-catalog.pins.v1') ?? '[]'))).toEqual(['operations-health', 'executive-sales'])

    await page.reload()
    await section.getByRole('link', { name: 'Executive Sales Dashboard' }).waitFor()
    await section.getByRole('button', { name: 'Unpin Operations Health' }).click()
    expect(await section.getByRole('link', { name: 'Operations Health' }).count()).toBe(0)
    expect(await allDashboards.locator('tbody tr').count()).toBe(3)
    await section.getByRole('button', { name: 'Unpin Executive Sales Dashboard' }).click()
    await section.waitFor({ state: 'detached' })
    expect(await allDashboards.locator('tbody tr').count()).toBe(4)
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem('leapview.dashboard-catalog.pins.v1') ?? '[]'))).toEqual([])
  } finally {
    await page.close()
  }
})

test('eight dashboard copies keep favorites and pins independent across catalog views', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    const catalog = page.locator('lv-catalog-page')
    await catalog.evaluate(async (element: any) => {
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
      localStorage.setItem('leapview.dashboard-catalog.favorites.v1', JSON.stringify(['sales-copy-1', 'sales-copy-8']))
      localStorage.setItem('leapview.dashboard-catalog.pins.v1', JSON.stringify(copies.map((copy: any) => copy.dashboardId)))
      element.reloadDiscoveryPreferences()
      await element.updateComplete
    })

    const pinned = catalog.locator('.pinned-dashboards')
    expect(await pinned.getByRole('link').count()).toBe(8)
    expect(await pinned.locator('tbody tr').count()).toBe(8)
    expect(await catalog.locator('lv-entity-list[list-label="All dashboards"] tbody tr').count()).toBe(4)
    await catalog.getByRole('tab', { name: 'My dashboards' }).click()
    expect(await catalog.locator('lv-entity-list tbody tr').count()).toBe(8)
    await catalog.getByRole('tab', { name: 'Favorites' }).click()
    expect(await catalog.locator('lv-entity-list tbody tr').count()).toBe(2)
    expect(await pinned.count()).toBe(0)

    await catalog.evaluate(async (element: any) => {
      localStorage.setItem('leapview.dashboard-catalog.pins.v1', '[]')
      element.reloadDiscoveryPreferences()
      await element.updateComplete
    })
    await catalog.getByRole('tab', { name: 'All dashboards' }).click()
    expect(await pinned.count()).toBe(0)
    await catalog.getByRole('tab', { name: 'Favorites' }).click()
    expect(await catalog.locator('lv-entity-list tbody tr').count()).toBe(2)
  } finally {
    await page.close()
  }
})
