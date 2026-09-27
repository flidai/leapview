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

test('dashboard pins share one searchable table across views and keep sidebar links in sync', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    const catalog = page.locator('lv-catalog-page')
    const allTable = catalog.getByRole('table', { name: 'All dashboards' })
    expect(await catalog.locator('lv-entity-list').count()).toBe(1)
    expect(await allTable.locator('tbody tr').count()).toBe(4)
    expect(await catalog.getByRole('searchbox').count()).toBe(1)

    const pin = catalog.getByRole('button', { name: 'Pin Operations Health' })
    expect(await pin.evaluate(button => getComputedStyle(button).opacity)).toBe('0')
    await pin.focus()
    expect(await pin.evaluate(button => getComputedStyle(button).opacity)).toBe('1')
    const unpinnedColor = await pin.evaluate(button => getComputedStyle(button).color)
    await pin.click()
    const pinned = catalog.getByRole('button', { name: 'Unpin Operations Health' })
    expect(await pinned.evaluate(button => getComputedStyle(button).opacity)).toBe('1')
    expect(await pinned.evaluate(button => getComputedStyle(button).color)).not.toBe(unpinnedColor)
    expect(await allTable.locator('tbody tr').count()).toBe(4)
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem('leapview.dashboard-catalog.pin-links.v1:jacob') ?? '[]'))).toEqual([
      { id: 'operations-health', title: 'Operations Health', href: '/dashboards/operations-health', icon: 'package-check' },
    ])

    await catalog.getByRole('button', { name: 'Add Operations Health to favorites' }).click()
    await catalog.getByRole('tab', { name: 'Favorites' }).click()
    expect(await catalog.getByRole('table', { name: 'Favorite dashboards' }).getByRole('button', { name: 'Unpin Operations Health' }).count()).toBe(1)
    await catalog.getByRole('tab', { name: 'All dashboards' }).click()
    await catalog.getByRole('button', { name: 'Pin Executive Sales Dashboard' }).click()
    await catalog.getByRole('button', { name: 'Unpin Operations Health' }).click()
    await catalog.getByRole('button', { name: 'Unpin Executive Sales Dashboard' }).click()
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem('leapview.dashboard-catalog.pin-links.v1:jacob') ?? '[]'))).toEqual([])

    await catalog.evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const source = element.page.dashboards[0]
      const copies = Array.from({ length: 8 }, (_, index) => ({
        ...source, id: `sales-copy-${index + 1}`, dashboardId: `sales-copy-${index + 1}`,
        title: `Sales copy ${index + 1}`, href: `/dashboards/sales-copy-${index + 1}`,
        catalogScope: 'mine', status: 'private_draft',
      }))
      mergePatch({ page: { ...element.page, dashboards: [...element.page.dashboards, ...copies] } })
      localStorage.setItem('leapview.dashboard-catalog.favorites.v1', JSON.stringify(['sales-copy-1', 'sales-copy-8']))
      localStorage.setItem('leapview.dashboard-catalog.pins.v1:jacob', JSON.stringify(copies.map((copy: any) => copy.dashboardId)))
      element.reloadDiscoveryPreferences()
    })
    expect(await catalog.getByRole('table', { name: 'All dashboards' }).locator('tbody tr').count()).toBe(12)
    expect(await page.evaluate(() => JSON.parse(localStorage.getItem('leapview.dashboard-catalog.pin-links.v1:jacob') ?? '[]').length)).toBe(8)
    await catalog.getByRole('tab', { name: 'My dashboards' }).click()
    expect(await catalog.getByRole('table', { name: 'My dashboards' }).locator('tbody tr').count()).toBe(8)
    expect(await catalog.getByRole('button', { name: 'Unpin Sales copy 1' }).count()).toBe(1)
    await catalog.getByRole('tab', { name: 'Favorites' }).click()
    expect(await catalog.getByRole('table', { name: 'Favorite dashboards' }).locator('tbody tr').count()).toBe(2)
    expect(await catalog.getByRole('button', { name: 'Unpin Sales copy 8' }).count()).toBe(1)
    await catalog.getByRole('searchbox').fill('Sales copy 1')
    expect(await catalog.getByRole('searchbox').inputValue()).toBe('Sales copy 1')
    await page.reload()
    expect(await catalog.getByRole('button', { name: 'Unpin Sales copy 1' }).count()).toBe(0)
    expect(await catalog.locator('lv-entity-list').count()).toBe(1)
  } finally {
    await page.close()
  }
}, 30_000)

test('search result patches preserve pinned shortcuts outside the result set', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    const catalog = page.locator('lv-catalog-page')
    await catalog.getByRole('button', { name: 'Pin Operations Health' }).click()
    await catalog.getByRole('button', { name: 'Pin Executive Sales Dashboard' }).click()
    const links = () => page.evaluate(() => JSON.parse(localStorage.getItem('leapview.dashboard-catalog.pin-links.v1:jacob') ?? '[]').map((link: any) => link.id))
    expect(await links()).toEqual(['operations-health', 'executive-sales'])
    await catalog.evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { dashboards: element.page.dashboards.filter((dashboard: any) => dashboard.dashboardId === 'operations-health'), listQuery: 'operations' } })
      await element.updateComplete
    })
    expect(await links()).toEqual(['operations-health', 'executive-sales'])
    await catalog.evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { dashboards: [], listQuery: 'no matches' } })
      await element.updateComplete
    })
    expect(await links()).toEqual(['operations-health', 'executive-sales'])
  } finally {
    await page.close()
  }
})
