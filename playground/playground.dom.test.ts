import { afterAll, afterEach, beforeAll, beforeEach, expect, test } from 'bun:test'
import { chromium, expect as browserExpect, type Browser, type Page } from '@playwright/test'
import { chartExamples } from './chart-fixtures'
import { playgroundResponse } from './server'
import { startTestPlayground } from './test-server'
import { tokenExamples } from './catalog'
import { openExample } from './browser-review/helpers'

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
  page = await browser.newPage({ baseURL: server.url.href, viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' })
  page.setDefaultTimeout(7000)
  page.on('pageerror', error => errors.push(error.message))
  page.on('request', request => {
    const url = new URL(request.url())
    if (url.protocol === 'blob:' || url.protocol === 'data:') return
    if (url.origin !== server.url.origin || !['/', '/index.html', '/__playground/events'].includes(url.pathname) && !url.pathname.startsWith('/assets/') && !url.pathname.startsWith('/static/')) unexpectedRequests.push(request.url())
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

async function delayControlsModule() {
  let release!: () => void
  let requested!: () => void
  const pending = new Promise<void>(resolve => { release = resolve })
  const started = new Promise<void>(resolve => { requested = resolve })
  await page.route('**/assets/chunks/controls-*.js', async route => {
    requested()
    await pending
    await route.continue()
  })
  return { started, release }
}

for (const [label, disabled] of [['Select menu', true], ['Buttons', false]] as const) {
  test(`delayed saved Select menu state ${disabled ? 'survives clicking its active link' : 'does not leak into Buttons'}`, async () => {
    const gate = await delayControlsModule()
    try {
      const url = new URL(server.url)
      url.searchParams.set('state', JSON.stringify({ version: 1, route: 'controls/select', width: 'responsive', height: '420', theme: 'light', preview: false, example: { disabled: true } }))
      url.hash = 'controls/select'
      await page.goto(url.href, { waitUntil: 'domcontentloaded' })
      await gate.started
      await browserExpect(page.locator('.viewport').getByRole('status')).toHaveText('Loading example…')
      await page.getByRole('navigation', { name: 'Examples', exact: true }).getByRole('link', { name: label, exact: true }).click()
      await browserExpect(page.getByRole('heading', { level: 1 })).toHaveText(label)
      gate.release()
      await browserExpect(page.locator('playground-controls')).toBeVisible()
      await browserExpect(page.getByLabel('Disabled', { exact: true })).toBeChecked({ checked: disabled })
    } finally { gate.release() }
  })
}

test('delayed mobile navigation cannot steal focus after choosing another example', async () => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open('tokens/colors')
  await browserExpect(page.locator('playground-tokens')).toBeVisible()
  const gate = await delayControlsModule()
  try {
    const browse = page.getByRole('button', { name: 'Browse', exact: true })
    const catalog = page.getByRole('navigation', { name: 'Examples', exact: true })
    await browse.click()
    await catalog.getByRole('button', { name: 'UI components', exact: true }).click()
    await catalog.getByRole('link', { name: 'Select menu', exact: true }).click()
    await gate.started
    await browse.click()
    await catalog.getByRole('button', { name: 'Design tokens', exact: true }).click()
    await catalog.getByRole('link', { name: 'Colors', exact: true }).click()
    await browserExpect(page.locator('main')).toBeFocused()
    await browse.click()
    const search = page.getByLabel('Find an example', { exact: true })
    await search.focus()
    gate.release()
    await page.evaluate(async () => {
      await customElements.whenDefined('playground-controls')
      await new Promise(requestAnimationFrame)
    })
    await browserExpect(search).toBeFocused()
    await browserExpect(page.getByRole('heading', { level: 1 })).toHaveText('Colors')
  } finally { gate.release() }
})

test('unknown prototype-named routes show the fallback and remain navigable', async () => {
  for (const route of ['constructor/bar', '__proto__/bar', 'toString/bar']) {
    await open(route)
    await browserExpect(page.locator('.viewport')).toHaveText('Choose an example from the navigation.')
    await page.getByRole('navigation', { name: 'Examples', exact: true }).getByRole('button', { name: 'UI components', exact: true }).click()
    await page.getByRole('link', { name: 'Select menu', exact: true }).click()
    await browserExpect(page.getByRole('button', { name: 'Refresh frequency', exact: true })).toBeVisible()
  }
})

test('a failed example module leaves navigation usable and reload can recover', async () => {
  const moduleURL = '**/assets/chunks/controls-*.js'
  await page.route(moduleURL, route => route.fulfill({ status: 200, contentType: 'text/javascript', body: 'throw new Error("Fixture module unavailable")' }))
  await open('controls/select')
  await browserExpect(page.locator('.viewport').getByRole('status')).toHaveText('Fixture module unavailable')
  await page.getByRole('navigation', { name: 'Examples', exact: true }).getByRole('button', { name: 'Design tokens', exact: true }).click()
  await page.getByRole('link', { name: 'Colors', exact: true }).click()
  await browserExpect(page.locator('playground-tokens')).toBeVisible()
  await page.unroute(moduleURL)
  await open('controls/select')
  await page.reload()
  await browserExpect(page.getByRole('button', { name: 'Refresh frequency', exact: true })).toBeVisible()
})

test('browser review waits for the actual preview while its module is delayed', async () => {
  const gate = await delayControlsModule()
  const opening = openExample(page, 'controls/select', { theme: 'light' })
  try {
    await gate.started
    await browserExpect(page.locator('.viewport').getByRole('status')).toHaveText('Loading example…')
    await page.evaluate(async () => { await document.fonts.ready })
    expect(await Promise.race([
      opening.then(() => 'ready'),
      new Promise<string>(resolve => setTimeout(() => resolve('pending'), 100)),
    ])).toBe('pending')
    gate.release()
    await opening
    await browserExpect(page.getByRole('button', { name: 'Refresh frequency', exact: true })).toBeVisible()
  } finally {
    gate.release()
    await opening
  }
})

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

for (const { id } of tokenExamples) {
  test(`token ${id} discovers production values standalone`, async () => {
    await open(`tokens/${id}`)
    await browserExpect.poll(() => page.locator('playground-tokens .token').count()).toBeGreaterThan(0)
    await browserExpect(page.locator('playground-tokens .count')).toContainText(/\d+ tokens/)
  })
}

for (const id of ['buttons', 'fields', 'select', 'multiselect', 'date-picker', 'filter-menu', 'toast', 'loading']) {
  test(`control ${id} renders standalone`, async () => {
    await open(`controls/${id}`)
    await browserExpect(page.getByRole('region', { name: 'Interactive component preview' })).toBeVisible()
    await browserExpect(page.locator('.documentation')).toContainText('Source')
  })
}

test('each lazy module loads its preview through mobile navigation', async () => {
  await page.setViewportSize({ width: 390, height: 844 })
  await open('tokens/colors')
  const navigation = page.getByRole('navigation', { name: 'Examples', exact: true })
  const examples = await page.getByRole('navigation', { name: 'Examples', exact: true, includeHidden: true }).locator('a[href^="#"]').evaluateAll(links =>
    [...new Map(links.map(link => [link.getAttribute('href')!, link.textContent!.trim()])).entries()],
  )
  expect(examples.length).toBeGreaterThan(0)
  const visitedModules = new Set<string>()
  for (const [href, label] of examples) {
    const route = href.slice(1)
    const module = route.startsWith('recipes/') ? route : route.split('/')[0]
    if (visitedModules.has(module)) continue
    visitedModules.add(module)
    await page.getByRole('button', { name: 'Browse', exact: true }).click()
    await page.getByLabel('Find an example', { exact: true }).fill(label)
    await navigation.getByRole('link', { name: label, exact: true }).click()
    await browserExpect(page).toHaveURL(`${server.url}${href}`)
    await browserExpect(page.getByRole('heading', { level: 1, name: label, exact: true })).toBeVisible()
    await browserExpect(page.locator('.viewport [part~="preview"]').first()).toBeVisible()
    for (const host of await page.locator('.viewport lv-visualization-host').all()) {
      await host.evaluate(async (element: any) => { await element.ensureMounted() })
      await browserExpect(host.locator('.error')).toHaveCount(0)
      expect(await host.locator('.renderer').evaluate(element => element.childElementCount)).toBeGreaterThan(0)
    }
  }
}, 120_000)

test('linked dashboard reloads and propagates table selection into its chart', async () => {
  await open('recipes/linked-visuals')
  for (const reload of [false, true]) {
    if (reload) await page.reload()
    await browserExpect(page.locator('playground-linked-visuals .dashboard')).toBeVisible()
    await browserExpect(page.locator('lv-visualization-host')).toHaveCount(3)
    for (const host of await page.locator('lv-visualization-host').all()) {
      await host.evaluate(async (element: any) => { await element.ensureMounted() })
      await browserExpect(host.locator('.error')).toHaveCount(0)
      expect(await host.locator('.renderer').evaluate(element => element.childElementCount)).toBeGreaterThan(0)
    }
  }
  await page.locator('lv-report-table').getByRole('button', { name: 'Region: West', exact: true }).press('Enter')
  await browserExpect(page.locator('playground-linked-visuals .filter-bar > .summary')).toContainText('West')
  await browserExpect.poll(() => page.locator('playground-linked-visuals .chart lv-visualization-host').evaluate((element: any) => element.envelope.highlights.length)).toBeGreaterThan(0)
  await page.getByRole('button', { name: 'Clear selection', exact: true }).click()
  await browserExpect(page.locator('playground-linked-visuals .filter-bar > .summary')).toContainText('select a region')
}, 20_000)

test('drawer recipe reloads and saves through its real nested controls', async () => {
  await open('recipes/overlay-form')
  for (const reload of [false, true]) {
    if (reload) await page.reload()
    await browserExpect(page.getByRole('button', { name: 'Edit schedule', exact: true })).toBeVisible()
  }
  await page.getByRole('button', { name: 'Edit schedule', exact: true }).click()
  await page.getByRole('textbox', { name: 'Report name', exact: true }).fill('Weekly review')
  await page.getByRole('button', { name: 'Delivery frequency', exact: true }).click()
  await page.getByRole('option', { name: 'Every month', exact: true }).click()
  await page.getByRole('button', { name: 'Save schedule', exact: true }).click()
  await browserExpect(page.locator('playground-overlay-recipe .summary')).toContainText('Weekly review')
  await browserExpect(page.locator('playground-overlay-recipe .summary')).toContainText('Every month')
  await browserExpect(page.getByRole('button', { name: 'Edit schedule', exact: true })).toBeFocused()
})

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
  await page.getByRole('button', { name: 'Preview', exact: true }).click()
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
  // Mounted rows include overscan behind the sticky header; choose a real visible cell.
  const visibleCellHandle = await table.evaluateHandle(element => {
    const root = element.shadowRoot!
    const viewport = root.querySelector('.table-scrollport')!.getBoundingClientRect()
    const bodyTop = Math.max(viewport.top, ...Array.from(root.querySelectorAll('.head, .group-head'), header => header.getBoundingClientRect().bottom))
    return Array.from(root.querySelectorAll<HTMLButtonElement>('.row[aria-selected] .cell-action')).find(button => {
      const bounds = button.getBoundingClientRect()
      const x = bounds.left + bounds.width / 2
      const y = bounds.top + bounds.height / 2
      const hit = root.elementFromPoint(x, y)
      return bounds.width > 0 && bounds.height > 0
        && bounds.top >= Math.max(bodyTop, 0) && bounds.bottom <= Math.min(viewport.bottom, innerHeight)
        && bounds.left >= Math.max(viewport.left, 0) && bounds.right <= Math.min(viewport.right, innerWidth)
        && hit !== null && button.contains(hit)
    }) ?? null
  })
  const visibleCell = visibleCellHandle.asElement()
  if (!visibleCell) throw new Error('No unobscured table cell is visible after scrolling')
  await visibleCell.click()
  await visibleCellHandle.dispose()
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
  await browserExpect(page.locator('#example-navigation').getByRole('status')).toContainText('No examples match')
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

test('usage disclosure preserves events and same-tab preview restores the interactive state', async () => {
  await open('controls/select')
  const originalURL = page.url()
  const originalPages = page.context().pages().length
  await browserExpect(page.getByRole('button', { name: 'Preview', exact: true })).toHaveCount(1)
  await browserExpect(page.locator('.toolbar a[target="_blank"]')).toHaveCount(0)
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
  await page.getByRole('button', { name: 'Preview', exact: true }).click()
  const exit = page.getByRole('button', { name: 'Exit preview', exact: true })
  await browserExpect(exit).toBeFocused()
  expect(page.url()).toBe(originalURL)
  expect(page.context().pages().length).toBe(originalPages)
  await browserExpect(summary).toBeHidden()
  await browserExpect(page.getByLabel('Disabled', { exact: true })).toBeHidden()
  await browserExpect(trigger).toBeDisabled()
  await exit.click()
  await browserExpect(page.getByRole('button', { name: 'Preview', exact: true })).toBeFocused()
  await browserExpect(page.getByLabel('Disabled', { exact: true })).toBeChecked()
  await summary.click()
  await browserExpect(log).toContainText('lv-select-change')
})

test('chart display options survive collapse and preview supports production dialogs', async () => {
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
  await page.getByRole('button', { name: 'Preview', exact: true }).click()
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
    await page.getByRole('button', { name: 'Preview', exact: true }).click()
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
    await browserExpect(page.getByRole('button', { name: 'Preview', exact: true })).toBeVisible()
  })
}

test('favorites support keyboard toggling, persisted shortcuts and search isolation', async () => {
  await open('controls/select')
  const favorite = page.getByRole('button', { name: 'Favorite Select menu', exact: true })
  await favorite.focus()
  await page.keyboard.press('Space')
  await browserExpect(favorite).toHaveAttribute('aria-pressed', 'true')
  const shortcuts = page.getByRole('navigation', { name: 'Example shortcuts', exact: true })
  await browserExpect(shortcuts.getByRole('link', { name: 'Select menu', exact: true })).toHaveAttribute('aria-current', 'page')

  const catalog = page.getByRole('navigation', { name: 'Examples', exact: true })
  await catalog.getByRole('link', { name: 'Date picker', exact: true }).click()
  const shortcut = shortcuts.getByRole('link', { name: 'Select menu', exact: true })
  await shortcut.focus()
  await page.keyboard.press('Enter')
  await browserExpect(page.getByRole('heading', { level: 1 })).toHaveText('Select menu')
  await page.reload()
  await browserExpect(favorite).toHaveAttribute('aria-pressed', 'true')
  await browserExpect(shortcuts.getByRole('region', { name: 'Recent', exact: true }).getByRole('link')).toHaveText(['Date picker'])

  await page.getByLabel('Find an example', { exact: true }).fill('select menu')
  await browserExpect(shortcuts).toBeHidden()
  await browserExpect(catalog.getByRole('link', { name: 'Select menu', exact: true })).toBeVisible()
  await page.getByLabel('Find an example', { exact: true }).fill('')
  await favorite.focus()
  await page.keyboard.press('Enter')
  await browserExpect(favorite).toHaveAttribute('aria-pressed', 'false')
  await browserExpect(shortcuts.getByRole('region', { name: 'Favorites', exact: true })).toHaveCount(0)
  await page.reload()
  await browserExpect(favorite).toHaveAttribute('aria-pressed', 'false')
})

test('recent shortcuts keep five distinct previous examples and omit favorites and the current route', async () => {
  await open('controls/buttons')
  await page.getByRole('button', { name: 'Favorite Buttons', exact: true }).click()
  const catalog = page.getByRole('navigation', { name: 'Examples', exact: true })
  for (const label of ['Form fields', 'Select menu', 'Entity multiselect', 'Date picker', 'Filter menu', 'Toasts', 'Loading']) {
    await catalog.getByRole('link', { name: label, exact: true }).click()
    await browserExpect(page.getByRole('heading', { level: 1 })).toHaveText(label)
  }
  const recent = page.getByRole('navigation', { name: 'Example shortcuts', exact: true }).getByRole('region', { name: 'Recent', exact: true })
  await browserExpect(recent.getByRole('link')).toHaveText(['Toasts', 'Filter menu', 'Date picker', 'Entity multiselect', 'Select menu'])
  await recent.getByRole('link', { name: 'Date picker', exact: true }).click()
  await browserExpect(recent.getByRole('link')).toHaveText(['Loading', 'Toasts', 'Filter menu', 'Entity multiselect', 'Select menu'])
  await browserExpect(recent.getByRole('link', { name: 'Buttons', exact: true })).toHaveCount(0)
  await browserExpect(recent.getByRole('link', { name: 'Date picker', exact: true })).toHaveCount(0)
  await page.reload()
  await browserExpect(recent.getByRole('link')).toHaveText(['Loading', 'Toasts', 'Filter menu', 'Entity multiselect', 'Select menu'])
})

test('pinned embedded previews never change favorites or recent navigation', async () => {
  await open('controls/select')
  await page.getByRole('button', { name: 'Favorite Select menu', exact: true }).click()
  await page.getByRole('navigation', { name: 'Examples', exact: true }).getByRole('link', { name: 'Buttons', exact: true }).click()
  await browserExpect(page.getByRole('heading', { level: 1 })).toHaveText('Buttons')
  const before = await page.evaluate(() => localStorage.getItem('leapview-playground:navigation:v1'))
  const review = page.locator('playground-review-tools')
  await review.locator('summary').first().click()
  await review.getByRole('button', { name: 'Pin comparison', exact: true }).click()
  const embedded = page.frameLocator('iframe[title="Pinned example comparison"]')
  await browserExpect(embedded.locator('playground-app')).toHaveAttribute('embedded', '')
  await browserExpect(embedded.getByRole('button', { name: /^Favorite / })).toHaveCount(0)
  await embedded.locator('playground-app').evaluate(() => { location.hash = 'controls/date-picker' })
  await browserExpect(embedded.locator('lv-date-picker')).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('leapview-playground:navigation:v1'))).toBe(before)
  await browserExpect(page.getByRole('heading', { level: 1 })).toHaveText('Buttons')
})

test('invalid saved shortcuts cannot introduce unknown routes or duplicate links', async () => {
  await open('controls/buttons')
  await page.evaluate(() => localStorage.setItem('leapview-playground:navigation:v1', JSON.stringify({
    version: 1,
    favorites: ['controls/select', 'https://example.invalid/', 'controls/select', 42, 'missing/example'],
    recent: ['controls/select', 'controls/fields', 'controls/fields', 'missing/example'],
    lastVisited: 'controls/buttons',
  })))
  await page.reload()
  const shortcuts = page.getByRole('navigation', { name: 'Example shortcuts', exact: true })
  await browserExpect(shortcuts.getByRole('region', { name: 'Favorites', exact: true }).getByRole('link')).toHaveText(['Select menu'])
  await browserExpect(shortcuts.getByRole('region', { name: 'Recent', exact: true }).getByRole('link')).toHaveText(['Form fields'])
  expect(await shortcuts.getByRole('link').evaluateAll(links => links.map(link => link.getAttribute('href')))).toEqual(['#controls/select', '#controls/fields'])

  await page.evaluate(() => localStorage.setItem('leapview-playground:navigation:v1', '{broken json'))
  await page.reload()
  await browserExpect(shortcuts).toHaveCount(0)
  await page.getByRole('button', { name: 'Favorite Buttons', exact: true }).click()
  await browserExpect(shortcuts.getByRole('link', { name: 'Buttons', exact: true })).toBeVisible()
})

test('shortcuts remain usable in memory when browser storage denies reads and writes', async () => {
  await page.addInitScript(() => {
    const read = Storage.prototype.getItem
    const write = Storage.prototype.setItem
    Storage.prototype.getItem = function(this: Storage, key: string) {
      if (key === 'leapview-playground:navigation:v1') throw new DOMException('Storage unavailable', 'SecurityError')
      return read.call(this, key)
    }
    Storage.prototype.setItem = function(this: Storage, key: string, value: string) {
      if (key === 'leapview-playground:navigation:v1') throw new DOMException('Storage unavailable', 'SecurityError')
      return write.call(this, key, value)
    }
  })
  await open('controls/buttons')
  await page.getByRole('button', { name: 'Favorite Buttons', exact: true }).click()
  const catalog = page.getByRole('navigation', { name: 'Examples', exact: true })
  await catalog.getByRole('link', { name: 'Form fields', exact: true }).click()
  await catalog.getByRole('link', { name: 'Select menu', exact: true }).click()
  const shortcuts = page.getByRole('navigation', { name: 'Example shortcuts', exact: true })
  await browserExpect(shortcuts.getByRole('region', { name: 'Favorites', exact: true }).getByRole('link')).toHaveText(['Buttons'])
  await browserExpect(shortcuts.getByRole('region', { name: 'Recent', exact: true }).getByRole('link')).toHaveText(['Form fields'])
  await shortcuts.getByRole('link', { name: 'Buttons', exact: true }).click()
  await browserExpect(page.getByRole('button', { name: 'Favorite Buttons', exact: true })).toHaveAttribute('aria-pressed', 'true')
})

test('fixture summary follows chart options and route changes without including demo form controls', async () => {
  await open('charts/combo')
  const review = page.locator('playground-review-tools')
  await review.locator('summary').first().click()
  const coverage = review.locator('details.coverage')
  await coverage.locator(':scope > summary').click()
  await browserExpect(coverage).toContainText('Available options for this configuration, not test results.')
  const stateRow = coverage.locator('dl > div').filter({ has: page.locator('dt').filter({ hasText: /^State$/ }) })
  await browserExpect(stateRow.locator('dd')).toHaveText('Ready · Loading · Empty · Error')
  await browserExpect(coverage.locator('dt').filter({ hasText: /^Show axes$/ })).toHaveCount(1)
  await browserExpect(coverage.locator('dl')).not.toContainText('?lit')
  await browserExpect(coverage.locator('dt').filter({ hasText: /^Smooth lines$/ })).toHaveCount(1)

  await page.locator('.display-options > summary').click()
  await page.getByLabel('Multiple series', { exact: true }).uncheck()
  await coverage.getByRole('button', { name: 'Refresh fixture summary', exact: true }).click()
  await browserExpect(coverage.locator('dt').filter({ hasText: /^Smooth lines$/ })).toHaveCount(0)
  await page.getByLabel('State', { exact: true }).selectOption('error')
  await browserExpect(page.locator('lv-visualization-host').getByRole('alert')).toContainText('Example error')
  await coverage.getByRole('button', { name: 'Refresh fixture summary', exact: true }).click()
  await browserExpect(stateRow.locator('dd')).toHaveText('Ready · Loading · Empty · Error')

  await page.evaluate(() => { location.hash = 'charts/kpi' })
  await browserExpect(coverage.locator('dt').filter({ hasText: /^KPI mode$/ })).toHaveCount(1)
  await browserExpect(coverage.locator('dt').filter({ hasText: /^Multiple series$/ })).toHaveCount(0)
  await browserExpect(coverage).toContainText('Compact · Bullet · Progress')
  await page.evaluate(() => { location.hash = 'controls/fields' })
  await browserExpect(page.locator('playground-controls .preview select')).toBeVisible()
  await browserExpect(coverage.locator('dt')).toHaveText(['Disabled', 'Error'])
  await browserExpect(coverage).not.toContainText('Refresh schedule')
  await browserExpect(coverage).not.toContainText('Daily')
})
