import { afterAll, afterEach, beforeAll, beforeEach, expect, test } from 'bun:test'
import { chromium, expect as browserExpect, type Browser, type Page } from '@playwright/test'
import { chartExamples } from './chart-fixtures'
import { playgroundResponse } from './server'
import { startTestPlayground } from './test-server'

let browser: Browser
let server: Awaited<ReturnType<typeof startTestPlayground>>
let page: Page
let errors: string[]
let unexpectedRequests: string[]

beforeAll(async () => {
  server = await startTestPlayground()
  browser = await chromium.launch()
}, 60_000)
afterAll(async () => { await browser?.close(); await server?.stop(true) })
beforeEach(async () => {
  errors = []
  unexpectedRequests = []
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' })
  page.setDefaultTimeout(7000)
  page.on('pageerror', error => errors.push(error.message))
  page.on('request', request => {
    const url = new URL(request.url())
    if (url.protocol === 'blob:' || url.protocol === 'data:') return
    if (url.origin !== server.url.origin || !['/', '/index.html'].includes(url.pathname) && !url.pathname.startsWith('/assets/') && !url.pathname.startsWith('/static/')) unexpectedRequests.push(request.url())
  })
  page.on('response', response => { if (response.status() >= 400) errors.push(`${response.status()} ${response.url()}`) })
})
afterEach(async () => {
  await page?.close()
  expect(unexpectedRequests).toEqual([])
  expect(errors).toEqual([])
})

async function open(route: string) {
  await page.goto(`${server.url}#${route}`)
  await browserExpect(page.locator('playground-app')).toBeVisible()
}

for (const example of chartExamples) {
  test(`standalone ${example.id} renders the production adapter without backend requests`, async () => {
    await open(`charts/${example.id}`)
    const host = page.locator('lv-visualization-host')
    await browserExpect(host).toBeVisible()
    await host.evaluate(async (element: any) => { await element.ensureMounted() })
    await browserExpect(host.locator('.error')).toHaveCount(0)
    expect(await host.locator('.renderer').evaluate(element => element.childElementCount)).toBeGreaterThan(0)
    await browserExpect(host.locator('.renderer')).toHaveAttribute('aria-hidden', 'false')
  }, 20_000)
}

for (const id of ['buttons', 'fields', 'select', 'multiselect', 'date-picker', 'filter-menu', 'toast', 'loading']) {
  test(`control ${id} renders standalone`, async () => {
    await open(`controls/${id}`)
    await browserExpect(page.getByRole('region', { name: 'Interactive component preview' })).toBeVisible()
    await browserExpect(page.locator('.documentation')).toContainText('Source')
  })
}

test('select keyboard interactions update the public value and respect disabled state', async () => {
  await open('controls/select')
  await page.locator('.example-details > summary').click()
  const trigger = page.getByRole('button', { name: 'Refresh frequency', exact: true })
  await trigger.focus()
  await page.keyboard.press('Enter')
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await browserExpect(page.getByRole('region', { name: 'Event log' })).toContainText('lv-select-change')
  await trigger.click()
  await page.keyboard.press('Escape')
  await browserExpect(trigger).toBeFocused()
  await page.getByLabel('Disabled', { exact: true }).check()
  await browserExpect(trigger).toBeDisabled()
})

test('filter fixture handles search, selection, loading and errors locally', async () => {
  await open('controls/filter-menu')
  await page.locator('.example-details > summary').click()
  await page.locator('lv-filter-menu').getByRole('button').first().click()
  await page.getByPlaceholder('Search status').fill('Draft')
  await browserExpect(page.getByRole('region', { name: 'Event log' })).toContainText('search')
  await page.locator('lv-filter-menu').getByRole('checkbox', { name: 'Draft', exact: true }).check()
  await browserExpect(page.getByRole('region', { name: 'Event log' })).toContainText('toggle')
  await page.keyboard.press('Escape')
  await page.getByLabel('Loading', { exact: true }).check()
  await page.locator('lv-filter-menu').getByRole('button').first().click()
  await browserExpect(page.locator('lv-filter-menu')).toContainText('Loading')
  await page.keyboard.press('Escape')
  await page.getByLabel('Loading', { exact: true }).uncheck()
  await page.getByLabel('Error', { exact: true }).check()
  await page.locator('lv-filter-menu').getByRole('button').first().click()
  await browserExpect(page.locator('lv-filter-menu')).toContainText('Options could not be loaded')
})

test('token values follow the shared theme and clean preview preserves control state', async () => {
  await open('tokens/colors')
  const token = page.locator('.token').filter({ hasText: '--lv-bg-accent-muted' }).first()
  await browserExpect(token).toBeVisible()
  const light = await token.locator('.value').textContent()
  await page.getByRole('button', { name: 'Switch to dark mode', exact: true }).click()
  await browserExpect(token.locator('.value')).not.toHaveText(light!)
  await page.getByRole('button', { name: 'UI components', exact: true }).click()
  await page.getByRole('link', { name: 'Select menu', exact: true }).click()
  await page.getByLabel('Disabled', { exact: true }).check()
  await page.getByRole('button', { name: 'Focus preview', exact: true }).click()
  await browserExpect(page.getByRole('navigation', { name: 'Examples' })).toBeHidden()
  await browserExpect(page.getByRole('button', { name: 'Refresh frequency', exact: true })).toBeDisabled()
  await page.keyboard.press('Escape')
  await browserExpect(page.getByLabel('Disabled', { exact: true })).toBeChecked()
})

test('chart fixtures, resizing, data actions and clean deep links work', async () => {
  await open('charts/bar')
  const host = page.locator('lv-visualization-host')
  await host.evaluate(async (element: any) => { await element.ensureMounted() })
  for (const fixture of ['single', 'missing', 'long-labels', 'dense']) {
    await page.getByLabel('Data fixture', { exact: true }).selectOption(fixture)
    await host.evaluate(async (element: any) => { await element.updateComplete; await element.ensureMounted() })
    await browserExpect(host.locator('.error')).toHaveCount(0)
  }
  await page.getByLabel('Preview width', { exact: true }).selectOption('360')
  await page.getByLabel('Preview height', { exact: true }).selectOption('260')
  await browserExpect(host).toHaveCSS('height', '260px')
  expect((await host.boundingBox())!.width).toBeLessThanOrEqual(360)
  await page.getByLabel('State', { exact: true }).selectOption('error')
  await browserExpect(host.getByRole('alert')).toContainText('Example error')
  await page.getByLabel('State', { exact: true }).selectOption('ready')
  await page.getByLabel('Visual options', { exact: true }).click()
  await page.getByRole('menuitem', { name: 'Show data', exact: true }).click()
  await browserExpect(page.getByRole('dialog')).toBeVisible()
  await page.keyboard.press('Escape')
  await page.goto(`${server.url}?preview=1&theme=dark&width=360&height=260#charts/bar`)
  await browserExpect(page.getByRole('navigation', { name: 'Examples' })).toBeHidden()
  await browserExpect(page.locator('html')).toHaveAttribute('data-color-mode', 'dark')
  await browserExpect(page.locator('lv-visualization-host')).toHaveCSS('height', '260px')
})

test('static server refuses backend methods and repository traversal', async () => {
  for (const path of ['/updates', '/api/data', '/.git/config', '/assets/..%2f..%2fpackage.json', '/assets/%zz']) {
    expect((await playgroundResponse(new Request(`http://localhost${path}`))).status).toBe(404)
  }
  expect((await playgroundResponse(new Request('http://localhost/', { method: 'POST' }))).status).toBe(405)
})

test('table sorting, selection and scroll windows work while expanded', async () => {
  await open('charts/table')
  await page.getByLabel('Data fixture', { exact: true }).selectOption('dense')
  const table = page.locator('lv-report-table')
  await browserExpect(table.getByRole('button', { name: 'Revenue', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Expand table', exact: true }).click()
  await browserExpect(page.getByRole('dialog')).toBeVisible()
  await table.getByRole('button', { name: 'Revenue', exact: true }).click()
  await browserExpect.poll(async () => page.locator('lv-visualization-host').evaluate((element: any) => element.envelope.dataState.sort[0].field.field)).toBe('value')
  await table.locator('.table-scrollport').evaluate(element => { element.scrollTop = 17000 })
  await browserExpect.poll(async () => table.locator('.row[aria-selected]').count()).toBeGreaterThan(0)
  await browserExpect.poll(async () => page.locator('lv-visualization-host').evaluate((element: any) => Math.max(...Object.values(element.envelope.dataState.blocks).map((block: any) => block.start)))).toBeGreaterThan(400)
  await table.locator('.row[aria-selected]').first().click()
  await browserExpect.poll(async () => page.locator('lv-visualization-host').evaluate((element: any) => element.envelope.selection.length)).toBeGreaterThan(0)
  await page.keyboard.press('Escape')
  await browserExpect(page.getByRole('dialog')).toBeHidden()
  await browserExpect(page.locator('.preview lv-visualization-host')).toBeVisible()
}, 20_000)


test('navigation keeps the preview in view when selecting an example at the end of the catalog', async () => {
  await open('charts/bar')
  await page.getByRole('link', { name: 'Pivot', exact: true }).click()
  await browserExpect(page.locator('lv-visualization-host')).toBeInViewport()
  await browserExpect(page.locator('main')).toHaveAttribute('aria-label', 'Pivot')
})

for (const layer of ['point', 'heat', 'density', 'choropleth', 'path', 'reference']) {
  test(`map ${layer} layer renders with local assets`, async () => {
    await open('charts/map')
    await page.getByLabel('Map layer', { exact: true }).selectOption(layer)
    const host = page.locator('lv-visualization-host')
    await host.evaluate(async (element: any) => { await element.ensureMounted() })
    await browserExpect(host.locator('.renderer')).toHaveAttribute('aria-hidden', 'false')
    await browserExpect(host.locator('.error')).toHaveCount(0)
    await browserExpect(host.locator('canvas')).toBeVisible()
  }, 20_000)
}


test('category dropdowns support keyboard navigation, active routes and search', async () => {
  await open('charts/bar')
  const nav = page.getByRole('navigation', { name: 'Examples' })
  const tokens = nav.getByRole('button', { name: 'Design tokens', exact: true })
  await browserExpect(tokens).toHaveAttribute('aria-expanded', 'false')
  await browserExpect(nav.getByRole('button', { name: 'Charts & data', exact: true })).toHaveAttribute('aria-expanded', 'true')
  await tokens.focus()
  await page.keyboard.press('Enter')
  await browserExpect(nav.getByRole('link', { name: 'Colors', exact: true })).toBeVisible()
  await page.keyboard.press('Space')
  await browserExpect(nav.getByRole('link', { name: 'Colors', exact: true })).toBeHidden()
  await browserExpect(tokens).toBeFocused()

  const controls = nav.getByRole('button', { name: 'UI components', exact: true })
  await controls.click()
  await nav.getByRole('link', { name: 'Select menu', exact: true }).click()
  await browserExpect(page.locator('main')).toHaveAttribute('aria-label', 'Select menu')
  await controls.click()
  await page.getByRole('button', { name: 'Switch to dark mode', exact: true }).click()
  await browserExpect(controls).toHaveAttribute('aria-expanded', 'false')
  await page.evaluate(() => { location.hash = 'graphs/asset-lineage' })
  await browserExpect(nav.getByRole('button', { name: 'Lineage & models', exact: true })).toHaveAttribute('aria-expanded', 'true')

  const search = page.getByLabel('Find an example', { exact: true })
  await search.fill('windowed')
  await browserExpect(nav.getByRole('button', { name: 'Tables & lists', exact: true })).toHaveAttribute('aria-expanded', 'true')
  await browserExpect(nav.getByRole('link', { name: 'Windowed table', exact: true })).toBeVisible()
  await browserExpect(nav.getByRole('button')).toHaveCount(1)
  await search.fill('ui components')
  await browserExpect(nav.getByRole('link', { name: 'Select menu', exact: true })).toBeVisible()
  await search.fill('no-such-example')
  await browserExpect(page.getByRole('status')).toContainText('No examples match')
  await search.fill('')
  await browserExpect(nav.getByRole('button', { name: 'Lineage & models', exact: true })).toHaveAttribute('aria-expanded', 'true')
  await page.setViewportSize({ width: 390, height: 844 })
  await page.getByRole('button', { name: 'Browse', exact: true }).click()
  await nav.getByRole('button', { name: 'Design tokens', exact: true }).click()
  await browserExpect(nav.getByRole('link', { name: 'Colors', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
})

test('sun and moon toggle follows the resolved theme and persists light or dark mode', async () => {
  await page.emulateMedia({ colorScheme: 'dark' })
  await open('charts/bar')
  await browserExpect(page.getByRole('combobox', { name: 'Theme', exact: true })).toHaveCount(0)
  const toLight = page.getByRole('button', { name: 'Switch to light mode', exact: true })
  await toLight.focus()
  await page.keyboard.press('Enter')
  await browserExpect(page.locator('html')).toHaveAttribute('data-theme-preference', 'light')
  await page.reload()
  await browserExpect(page.getByRole('button', { name: 'Switch to dark mode', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Switch to dark mode', exact: true }).click()
  await page.reload()
  await browserExpect(page.locator('html')).toHaveAttribute('data-theme-preference', 'dark')
  await browserExpect(toLight).toBeVisible()
  await page.goto(`${server.url}?theme=dark_colorblind#tokens/colors`)
  await browserExpect(page.locator('html')).toHaveAttribute('data-theme-preference', 'dark_colorblind')
  await toLight.click()
  await browserExpect(page.locator('html')).toHaveAttribute('data-theme-preference', 'light')
})


test('mobile Browse preserves preview space and closes on selection or Escape', async () => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open('charts/bar')
  const browse = page.getByRole('button', { name: 'Browse', exact: true })
  const navigation = page.getByRole('navigation', { name: 'Examples' })
  await browserExpect(navigation).toBeHidden()
  await browserExpect(page.locator('lv-visualization-host')).toBeInViewport()
  await browse.click()
  await browserExpect(navigation).toBeVisible()
  await navigation.getByRole('link', { name: 'Bar chart', exact: true }).click()
  await browserExpect(navigation).toBeHidden()
  await browserExpect(page.locator('main')).toBeFocused()
  await browse.click()
  await page.getByLabel('Find an example', { exact: true }).fill('select menu')
  await navigation.getByRole('link', { name: 'Select menu', exact: true }).click()
  await browserExpect(navigation).toBeHidden()
  await browserExpect(page.getByRole('heading', { level: 1 })).toHaveText('Select menu')
  await browserExpect(page.locator('main')).toBeFocused()
  await browse.click()
  await page.keyboard.press('Escape')
  await browserExpect(navigation).toBeHidden()
  await browserExpect(browse).toBeFocused()
})

test('usage disclosure preserves events and focus preview restores the interactive state', async () => {
  await open('controls/select')
  const summary = page.locator('.example-details > summary')
  const log = page.getByRole('region', { name: 'Event log' })
  await browserExpect(log).toBeHidden()
  const trigger = page.getByRole('button', { name: 'Refresh frequency', exact: true })
  await trigger.focus()
  await page.keyboard.press('Enter')
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await summary.focus()
  await page.keyboard.press('Enter')
  await browserExpect(log).toContainText('lv-select-change')
  await page.keyboard.press('Space')
  await browserExpect(log).toBeHidden()
  await page.getByLabel('Disabled', { exact: true }).check()
  await page.getByRole('button', { name: 'Focus preview', exact: true }).click()
  const exit = page.getByRole('button', { name: 'Exit preview', exact: true })
  await browserExpect(exit).toBeFocused()
  await browserExpect(summary).toBeHidden()
  await browserExpect(page.getByLabel('Disabled', { exact: true })).toBeHidden()
  await browserExpect(trigger).toBeDisabled()
  await exit.click()
  await browserExpect(page.getByRole('button', { name: 'Focus preview', exact: true })).toBeFocused()
  await browserExpect(page.getByLabel('Disabled', { exact: true })).toBeChecked()
  await summary.click()
  await browserExpect(log).toContainText('lv-select-change')
})

test('chart display options survive collapse and focus mode supports production dialogs', async () => {
  await open('charts/bar')
  const summary = page.locator('.display-options > summary')
  const axes = page.getByLabel('Show axes', { exact: true })
  await browserExpect(axes).toBeHidden()
  await summary.focus()
  await page.keyboard.press('Enter')
  await axes.uncheck()
  await page.getByLabel('Legend', { exact: true }).selectOption('hidden')
  await summary.click()
  await browserExpect(axes).toBeHidden()
  await summary.click()
  await browserExpect(axes).not.toBeChecked()
  await browserExpect(page.getByLabel('Legend', { exact: true })).toHaveValue('hidden')
  await page.getByRole('button', { name: 'Focus preview', exact: true }).click()
  await page.getByLabel('Visual options', { exact: true }).click()
  await page.getByRole('menuitem', { name: 'Show data', exact: true }).click()
  await browserExpect(page.getByRole('dialog')).toBeVisible()
  await page.keyboard.press('Escape')
  await browserExpect(page.getByRole('dialog')).toBeHidden()
  await browserExpect(page.getByRole('button', { name: 'Exit preview', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Exit preview', exact: true }).click()
  await browserExpect(axes).not.toBeChecked()
})

for (const type of ['bar', 'table']) {
  test(`expanded ${type} blocks background controls and aligns toolbar actions`, async () => {
    await open(`charts/${type}`)
    await page.getByRole('button', { name: 'Focus preview', exact: true }).click()
    const exit = page.locator('.exit-preview')
    const exitBounds = (await exit.boundingBox())!
    const expand = page.getByRole('button', { name: type === 'table' ? 'Expand table' : 'Expand chart', exact: true })
    await expand.click()
    const dialog = page.getByRole('dialog')
    const close = page.getByRole('button', { name: 'Close visual modal', exact: true })
    const options = page.getByLabel('Visual options', { exact: true })
    await browserExpect(dialog).toBeVisible()
    await browserExpect(close).toBeVisible()
    await page.locator('lv-visualization-host').evaluate(async (element: any) => { await element.ensureMounted() })
    await browserExpect(close).toBeFocused()
    await page.keyboard.press('Tab')
    await browserExpect(exit).not.toBeFocused()
    await page.keyboard.press('Shift+Tab')
    await browserExpect(close).toBeFocused()
    await browserExpect.poll(async () => {
      const a = (await close.boundingBox())!, b = (await options.boundingBox())!
      return Math.abs(a.y + a.height / 2 - b.y - b.height / 2)
    }).toBeLessThan(1)
    const a = (await close.boundingBox())!, b = (await options.boundingBox())!
    expect(Math.abs(a.height - b.height)).toBeLessThan(1)
    expect(Math.abs(a.width - b.width)).toBeLessThan(1)
    expect(a.x).toBeGreaterThan(b.x + b.width)
    await exit.evaluate((element: HTMLElement) => element.focus())
    await browserExpect(exit).not.toBeFocused()
    // A real click at the background control hits the modal backdrop instead.
    await page.mouse.click(exitBounds.x + exitBounds.width / 2, exitBounds.y + exitBounds.height / 2)
    await browserExpect(dialog).toBeHidden()
    await browserExpect(exit).toBeVisible()
    await browserExpect(expand).toBeFocused()
    await expand.click()
    await close.click()
    await browserExpect(dialog).toBeHidden()
    await browserExpect(page.locator('.preview lv-visualization-host')).toBeVisible()
    await expand.click()
    await page.keyboard.press('Escape')
    await browserExpect(dialog).toBeHidden()
    await browserExpect(exit).toBeVisible()
    await exit.click()
    await browserExpect(page.getByRole('button', { name: 'Focus preview', exact: true })).toBeVisible()
  })
}
