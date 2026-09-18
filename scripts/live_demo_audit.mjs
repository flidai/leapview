import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { chromium } from 'playwright'
import { parse } from 'yaml'

const baseURL = process.env.LEAPVIEW_BASE_URL || 'https://demo.leapview.dev'
const password = process.env.DEMO_LOGIN_PASSWORD
if (!password) throw new Error('DEMO_LOGIN_PASSWORD is required')

const evidence = '.tmp/live-demo-audit'
await mkdir(evidence, { recursive: true })

const dashboards = []
for (const name of ['executive-sales', 'fulfillment-operations', 'visual-showcase']) {
  const document = parse(await readFile(`dashboards/dashboards/${name}.yaml`, 'utf8'))
  dashboards.push({ name, pages: document.spec.pages.map((page) => page.id) })
}

const browser = await chromium.launch({ headless: true })
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
const page = await context.newPage()
const failures = []
const consoleErrors = []
page.on('console', (message) => {
  if (message.type() === 'error') consoleErrors.push(`${page.url()}: ${message.text()}`)
})
page.on('pageerror', (error) => consoleErrors.push(`${page.url()}: ${error.message}`))

try {
  await page.goto(new URL('/login', baseURL), { waitUntil: 'domcontentloaded', timeout: 60_000 })
  await page.getByLabel('Email').fill('demo@leapview.dev')
  await page.locator('input[name="password"]').fill(password)
  await page.locator('input[name="password"]').press('Enter')
  await page.waitForURL((url) => !url.pathname.startsWith('/login'), { timeout: 30_000 })

  const routes = [
    '/', '/explore', '/sources', '/models', '/semantic-models', '/pipelines', '/connections',
    '/chats', '/admin/profile', '/admin/api-tokens',
    ...dashboards.flatMap((dashboard) => dashboard.pages.map((pageID) =>
      `/dashboards/dashboard:${dashboard.name}/pages/${pageID}`)),
  ]

  for (const route of routes) {
    const response = await page.goto(new URL(route, baseURL), { waitUntil: 'domcontentloaded', timeout: 60_000 })
    const status = response?.status() ?? 0
    if (status >= 500 || status === 0) failures.push(`${route}: HTTP ${status}`)
    if (status === 200 && route.includes('/dashboards/')) {
      await page.locator('lv-dashboard-page').waitFor({ state: 'attached', timeout: 30_000 })
      try {
        await page.waitForFunction(() => {
          const dashboard = document.querySelector('lv-dashboard-page')
          return dashboard?.status?.loading === false
        }, undefined, { timeout: 45_000 })
      } catch {
        failures.push(`${route}: dashboard did not finish loading`)
      }
      const state = await page.locator('lv-dashboard-page').evaluate((dashboard) => ({
        error: dashboard.status?.error,
        visuals: dashboard.shadowRoot?.querySelectorAll('lv-visualization-host').length ?? 0,
        visualErrors: Array.from(dashboard.shadowRoot?.querySelectorAll('lv-visualization-host') ?? [])
          .map((host) => host.shadowRoot?.querySelector('[role="alert"]')?.textContent?.trim())
          .filter(Boolean),
      }))
      if (state.error) failures.push(`${route}: ${state.error}`)
      if (state.visualErrors.length) failures.push(`${route}: visual errors ${JSON.stringify(state.visualErrors)}`)
      const file = route.replaceAll('/', '_').replaceAll(':', '-') || '_home'
      await page.screenshot({ path: `${evidence}/${file}.png`, fullPage: true })
    }
  }

  const filterRoute = '/dashboards/dashboard:visual-showcase/pages/filters'
  await page.goto(new URL(filterRoute, baseURL), { waitUntil: 'domcontentloaded', timeout: 60_000 })
  await page.waitForFunction(() => {
    const dashboard = document.querySelector('lv-dashboard-page')
    return dashboard?.status?.loading === false
      && dashboard.shadowRoot?.querySelectorAll('lv-slicer').length === 8
  }, undefined, { timeout: 45_000 })
  const filterMatrix = await page.locator('lv-dashboard-page').evaluate((dashboard) =>
    Array.from(dashboard.shadowRoot.querySelectorAll('lv-slicer')).map((slicer) => ({
      id: slicer.definition?.id,
      kind: slicer.definition?.valueKind,
      style: slicer.presentation?.style,
    })))
  const expected = [
    'purchase_date:date:date_range', 'purchase_time:timestamp:date_range', 'state:string:dropdown',
    'category_text:string:input', 'order_status:string:list', 'delivered:boolean:buttons',
    'delivery_days:integer:numeric_range', 'revenue_amount:decimal:numeric_range',
  ].sort()
  const actual = filterMatrix.map((item) => `${item.id}:${item.kind}:${item.style}`).sort()
  if (JSON.stringify(actual) !== JSON.stringify(expected)) failures.push(`filter matrix: ${JSON.stringify(filterMatrix)}`)

  const revision = async () => page.locator('lv-dashboard-page').evaluate((dashboard) => dashboard.canonicalFilterState?.revision ?? -1)
  const mutate = async (label, action) => {
    const before = await revision()
    await action()
    await page.waitForFunction((previous) => {
      const dashboard = document.querySelector('lv-dashboard-page')
      return dashboard?.canonicalFilterState?.revision > previous && dashboard?.filterController?.pending === false
    }, before, { timeout: 30_000 })
    console.log(`verified filter: ${label}`)
  }
  await mutate('state dropdown', async () => {
    await page.getByRole('button', { name: /^State:/ }).click()
    await page.getByRole('dialog', { name: 'State filter options' }).getByRole('checkbox').nth(1).check()
  })
  await mutate('status list', () => page.getByRole('radio').first().check())
  await mutate('category input', async () => {
    const input = page.getByRole('textbox', { name: 'Category text, Contains' })
    await input.fill('bed')
    await input.press('Tab')
  })
  await mutate('boolean buttons', () => page.getByRole('button', { name: 'Delivered', exact: true }).click())
  await mutate('integer range', async () => {
    const region = page.getByRole('region', { name: 'Delivery days' })
    await region.getByLabel('Minimum').fill('0')
    await region.getByLabel('Maximum').fill('60')
    await region.getByLabel('Maximum').press('Tab')
  })
  await mutate('decimal range', async () => {
    const region = page.getByRole('region', { name: 'Order revenue' })
    await region.getByLabel('Minimum').fill('1.25')
    await region.getByLabel('Maximum').fill('1000.50')
    await region.getByLabel('Maximum').press('Tab')
  })
  await page.screenshot({ path: `${evidence}/filters-after.png`, fullPage: true })

  await writeFile(`${evidence}/report.json`, JSON.stringify({ routes, filterMatrix, consoleErrors, failures }, null, 2))
  if (consoleErrors.length || failures.length) {
    throw new Error(`live demo audit failed: ${JSON.stringify({ consoleErrors, failures })}`)
  }
  console.log(`live demo audit passed: ${routes.length} routes and ${filterMatrix.length} filter types`)
} catch (error) {
  await page.screenshot({ path: `${evidence}/failure.png`, fullPage: true }).catch(() => {})
  await writeFile(`${evidence}/fatal.txt`, String(error))
  throw error
} finally {
  await browser.close()
}
