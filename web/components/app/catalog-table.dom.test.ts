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

test('catalog aligns text and icon-only columns and keeps inactive sorting controls on hover', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page') && customElements.get('lv-entity-list'))
    const list = page.locator('lv-catalog-page lv-entity-list')
    await list.evaluate(async (element: any) => element.updateComplete)

    const initial = await list.evaluate((element: any) => {
      const root = element as HTMLElement
      const dataCells = root.querySelectorAll('tbody tr:first-child td')
      const buttons = Array.from(root.querySelectorAll<HTMLButtonElement>('.entity-list-sort-button'))
      return {
        listClass: root.querySelector('.entity-list')?.className,
        dataCellAlignments: Array.from(dataCells).map((cell) => getComputedStyle(cell).textAlign),
        ownerCellAlignment: getComputedStyle(dataCells[1]!).textAlign,
        popularityCellAlignment: getComputedStyle(dataCells[2]!).textAlign,
        actionsCellAlignment: getComputedStyle(root.querySelector('tbody tr:first-child td:last-child')!).textAlign,
        inactiveIndicatorOpacity: getComputedStyle(buttons.find((button) => button.textContent?.includes('Data model'))?.querySelector('.entity-list-sort-indicator')!).opacity,
        dataModelAriaLabel: buttons.find((button) => button.textContent?.includes('Data model'))?.getAttribute('aria-label'),
      }
    })

    const dataModelButton = list.locator('.entity-list-sort-button', { hasText: 'Data model' })
    await dataModelButton.hover()
    const hoveredOpacity = await dataModelButton.locator('.entity-list-sort-indicator').evaluate((indicator) => getComputedStyle(indicator).opacity)
    await dataModelButton.click()
    await list.evaluate(async (element: any) => element.updateComplete)
    const sorted = await dataModelButton.evaluate((button) => ({
      ariaLabel: button.getAttribute('aria-label'),
      ariaSort: button.closest('th')?.getAttribute('aria-sort'),
      indicatorOpacity: getComputedStyle(button.querySelector('.entity-list-sort-indicator')!).opacity,
    }))

    expect(initial).toEqual({
      listClass: expect.stringContaining('has-hover-sort-indicators'),
      dataCellAlignments: ['left', 'center', 'center', 'left', 'left', 'center'],
      ownerCellAlignment: 'center',
      popularityCellAlignment: 'center',
      actionsCellAlignment: 'center',
      inactiveIndicatorOpacity: '0',
      dataModelAriaLabel: 'Sort by Data model',
    })
    expect(hoveredOpacity).toBe('1')
    expect(sorted).toEqual({
      ariaLabel: 'Sort by Data model, currently sorted ascending',
      ariaSort: 'ascending',
      indicatorOpacity: '1',
    })
  } finally {
    await page.close()
  }
})

test('opening a dashboard records recency without reordering the catalog in place', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.addInitScript(() => localStorage.removeItem('leapview.dashboard-catalog.recents.v1'))
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page') && customElements.get('lv-entity-list'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const list = root.querySelector('lv-entity-list') as HTMLElement & { updateComplete: Promise<unknown> }
      await list.updateComplete
      const titles = () => Array.from(list.querySelectorAll('.entity-list-title')).map((title) => title.textContent?.trim())
      const before = titles()
      const target = list.querySelector('a[data-item-id="inventory-risk"]') as HTMLAnchorElement
      target.addEventListener('click', (event) => event.preventDefault(), { once: true })
      target.click()
      const secondTarget = list.querySelector('a[data-item-id="executive-sales"]') as HTMLAnchorElement
      secondTarget.addEventListener('click', (event) => event.preventDefault(), { once: true })
      secondTarget.click()
      await element.updateComplete
      await list.updateComplete
      return {
        before,
        after: titles(),
        stored: JSON.parse(localStorage.getItem('leapview.dashboard-catalog.recents.v1') ?? '{}'),
      }
    })

    expect(state.after).toEqual(state.before)
    expect(state.stored['inventory-risk']).toEqual(expect.any(String))
    expect(state.stored['executive-sales']).toEqual(expect.any(String))
  } finally {
    await page.close()
  }
})
