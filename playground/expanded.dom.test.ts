import { afterAll, afterEach, beforeAll, beforeEach, expect, test } from 'bun:test'
import { chromium, expect as browserExpect, type Browser, type Page } from '@playwright/test'
import { startTestPlayground } from './test-server'

let browser: Browser
let server: Awaited<ReturnType<typeof startTestPlayground>>
let page: Page
let errors: string[]
let unexpectedRequests: string[]
let requestPaths: Set<string>

beforeAll(async () => {
  server = await startTestPlayground()
  browser = await chromium.launch()
}, 60_000)
afterAll(async () => { await browser?.close(); await server?.stop(true) })
beforeEach(async () => {
  errors = []
  unexpectedRequests = []
  requestPaths = new Set()
  page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' })
  page.setDefaultTimeout(7000)
  page.on('pageerror', error => errors.push(error.message))
  page.on('request', request => {
    const url = new URL(request.url())
    if (url.protocol === 'blob:' || url.protocol === 'data:') return
    requestPaths.add(url.pathname)
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

async function openDetails() {
  const details = page.locator('main .example-details')
  await details.locator(':scope > summary').click()
  await browserExpect(details).toHaveAttribute('open', '')
}

const routes: Array<[string, string]> = [
  ['graphs/asset-lineage', 'lv-asset-lineage-graph .react-flow__node'],
  ['graphs/semantic-model', 'lv-semantic-model-graph .react-flow__node'],
  ['tables/record', 'lv-record-table table'],
  ['tables/windowed', 'lv-windowed-table .scrollport'],
  ['tables/entity-list', 'lv-entity-list table'],
  ['tables/data-preview', 'lv-data-preview-table lv-windowed-table .scrollport'],
  ['tables/data-explore', 'lv-data-explore-table lv-windowed-table .scrollport'],
  ['content/code-editor', 'lv-code-editor .monaco-editor'],
  ['content/code-block', 'lv-code-block pre'],
  ['content/config-viewer', 'lv-config-viewer .tree'],
  ['content/markdown-view', 'lv-markdown-view h1, lv-markdown-view h2'],
  ['content/visual-artifact', 'lv-visual-artifact lv-visualization-host'],
  ['content/chat-composer', 'lv-chat-composer textarea'],
  ['surfaces/drawer', 'playground-surfaces .preview button'],
  ['surfaces/identity', 'lv-user-avatar'],
  ['surfaces/toast-region', 'playground-surfaces .preview button'],
  ['surfaces/one-time-secret', 'lv-one-time-secret'],
  ['surfaces/empty-state', 'playground-surfaces .empty-state'],
  ['surfaces/page-header', 'playground-surfaces .page-header'],
  ['surfaces/breadcrumb', 'playground-surfaces .breadcrumb'],
  ['surfaces/settings', 'playground-surfaces form'],
  ['surfaces/entity-detail', 'playground-surfaces .detail-section'],
  ['surfaces/icon-picker', 'lv-dashboard-icon-picker [role=dialog]'],
  ['surfaces/appearance', 'lv-dashboard-appearance-editor'],
  ['surfaces/report-footer', 'lv-report-footer lv-report-zoom'],
  ['filters/leaf', 'lv-filter-leaf'],
  ['filters/pane', 'lv-filter-pane-card'],
  ['filters/slicer', 'lv-slicer'],
  ['filters/dock', 'lv-filter-dock'],
]

for (const [route, selector] of routes) {
  test(`expanded catalog ${route} renders production content without backend requests`, async () => {
    await open(route)
    await browserExpect(page.locator(selector).first()).toBeVisible({ timeout: 15000 })
    const documentation = page.locator('main .documentation')
    await browserExpect(documentation).toBeHidden()
    await browserExpect(documentation).toContainText(/source/i)
    // Each renderer owns its wrapper; cover keyboard disclosure access once
    // per renderer while checking the closed default on every catalog route.
    if (['graphs/asset-lineage', 'tables/record', 'content/code-editor', 'surfaces/drawer', 'filters/leaf'].includes(route)) {
      const summary = page.locator('main .example-details > summary')
      await summary.focus()
      await page.keyboard.press('Enter')
      await browserExpect(documentation).toBeVisible()
      await page.keyboard.press('Enter')
      await browserExpect(documentation).toBeHidden()
    }
    if (route.endsWith('visual-artifact')) {
      await page.locator('lv-visualization-host').evaluate(async (element: any) => { await element.ensureMounted() })
      await browserExpect(page.locator('lv-visualization-host .error')).toHaveCount(0)
    }
  }, 20_000)
}

test('lineage selection, scope, and expanded dialog stay local and restore the graph', async () => {
  await open('graphs/asset-lineage')
  const graph = page.locator('lv-asset-lineage-graph')
  await graph.getByRole('button', { name: 'Source Orders', exact: true }).click()
  await browserExpect(page.locator('playground-graphs .selection-feedback')).toContainText('orders-source')
  await browserExpect(page.locator('playground-graphs .documentation')).toBeHidden()
  await openDetails()
  await browserExpect(page.locator('playground-graphs .documentation')).toContainText('lv-lineage-select')
  await browserExpect(page.locator('playground-graphs .documentation')).toContainText('orders-source')
  await graph.getByRole('button', { name: 'Show all upstream', exact: true }).click()
  await browserExpect(page.getByLabel('Graph scope')).toHaveValue('full')
  await graph.getByRole('button', { name: 'Expand to full page', exact: true }).click()
  await browserExpect(graph.getByRole('dialog')).toBeVisible()
  await page.keyboard.press('Escape')
  await browserExpect(graph.locator('dialog')).not.toHaveAttribute('open', '')
  await browserExpect(graph.getByRole('button', { name: 'Expand to full page', exact: true })).toBeFocused()
})

test('semantic relationship inspector and field visibility use the real graph', async () => {
  await open('graphs/semantic-model')
  const graph = page.locator('lv-semantic-model-graph')
  await browserExpect(graph.locator('.react-flow__edge')).toHaveCount(2)
  await graph.locator('.react-flow__edge').first().press('Enter')
  await browserExpect(graph.locator('.semantic-model-relationship-inspector')).toContainText('Relationship')
  await graph.getByRole('button', { name: 'All', exact: true }).click()
  await browserExpect(graph.locator('.semantic-model-field-name').filter({ hasText: /^revenue$/ })).toBeVisible()
  await page.getByLabel('Graph scenario').selectOption('composite')
  await browserExpect(graph.locator('.semantic-model-edge-label')).toContainText('composite key')
})

test('windowed table fulfills deep scroll, sorting and column changes', async () => {
  await open('tables/windowed')
  const table = page.locator('lv-windowed-table')
  await table.locator('.scrollport').evaluate(element => { element.scrollTop = 19000 })
  await browserExpect.poll(() => table.evaluate((element: any) => Math.max(...Object.values(element.table.blocks).map((block: any) => block.start)))).toBeGreaterThan(400)
  await table.getByRole('button', { name: 'Revenue', exact: true }).click()
  await browserExpect.poll(() => table.evaluate((element: any) => element.table.sort.key)).toBe('revenue')
  await browserExpect.poll(() => table.evaluate((element: any) => element.table.blocks.a.rows[0].revenue)).toBe(45)
  await table.getByLabel('Choose visible columns').click()
  await table.getByRole('checkbox', { name: 'Fulfilled', exact: true }).uncheck()
  await browserExpect(table.getByRole('button', { name: 'Fulfilled', exact: true })).toHaveCount(0)
  await openDetails()
  await browserExpect(page.locator('playground-tables .entries')).toContainText('lv-windowed-table-columns')
})

test('record table expands actual SQL and acknowledges refresh actions', async () => {
  await open('tables/record')
  const table = page.locator('lv-record-table')
  await table.getByRole('button', { name: 'Expand query text', exact: true }).first().click()
  await browserExpect(table.locator('lv-code-block').first()).toContainText('analytics.daily_orders')
  await table.getByRole('button', { name: 'Refresh', exact: true }).first().click()
  await openDetails()
  await browserExpect(page.locator('playground-tables .entries')).toContainText('refresh')
})

test('entity list filters and toggles favorite without server state', async () => {
  await open('tables/entity-list')
  const list = page.locator('lv-entity-list')
  await list.getByRole('searchbox', { name: 'Search dashboards' }).fill('Revenue')
  await browserExpect(list.locator('.entity-list-table-row')).toHaveCount(1)
  const favorite = list.getByRole('button', { name: 'Favorite Revenue overview', exact: true })
  await browserExpect(favorite).toHaveAttribute('aria-pressed', 'true')
  await favorite.click()
  await browserExpect(favorite).toHaveAttribute('aria-pressed', 'false')
  await openDetails()
  await browserExpect(page.locator('playground-tables .entries')).toContainText('favorite-toggle')
})

test('data preview retry clears the fixture error and preserves rows', async () => {
  await open('tables/data-preview')
  await page.getByLabel('Table state').selectOption('error')
  const preview = page.locator('lv-data-preview-table')
  await browserExpect(preview.getByRole('alert')).toBeVisible()
  await preview.getByRole('button', { name: 'Retry', exact: true }).click()
  await browserExpect(preview.getByRole('alert')).toHaveCount(0)
  await browserExpect(page.getByLabel('Table state')).toHaveValue('populated')
  await browserExpect(preview).toContainText('Acme Studio')
})

test('data exploration sort updates the typed command and result', async () => {
  await open('tables/data-explore')
  const table = page.locator('lv-data-explore-table')
  await table.getByRole('button', { name: 'Revenue', exact: true }).click()
  await browserExpect.poll(() => table.evaluate((element: any) => element.command.sort[0])).toEqual({ field: 'revenue', direction: 'asc' })
  await table.getByRole('button', { name: 'Revenue', exact: true }).click()
  await browserExpect.poll(() => table.evaluate((element: any) => element.command.sort[0].direction)).toBe('desc')
  await browserExpect.poll(() => table.evaluate((element: any) => element.result.rows[0].revenue > element.result.rows.at(-1).revenue)).toBe(true)
})

test('Monaco edits through the real editor and loads local stylesheet and worker', async () => {
  await open('content/code-editor')
  const editor = page.locator('lv-code-editor')
  await browserExpect(editor.locator('.monaco-editor')).toBeVisible({ timeout: 15000 })
  await browserExpect(editor.locator('.fallback-note')).toHaveCount(0)
  await editor.locator('.view-lines').click()
  await page.keyboard.press('ControlOrMeta+End')
  await page.keyboard.type('\n-- playground editing')
  await openDetails()
  await browserExpect(page.getByRole('region', { name: 'Public event log' })).toContainText('playground editing')
  // Exercise the production environment's actual module-worker factory, even
  // when this SQL document does not yet need Monaco's background services.
  await page.evaluate(() => {
    const environment = (globalThis as unknown as { MonacoEnvironment: { getWorker: () => Worker } }).MonacoEnvironment
    ;(globalThis as unknown as { playgroundTestWorker: Worker }).playgroundTestWorker = environment.getWorker()
  })
  await browserExpect.poll(() => requestPaths.has('/static/monaco-editor-worker.js')).toBe(true)
  expect(requestPaths.has('/static/monaco-editor-css.css')).toBe(true)
  await page.evaluate(() => (globalThis as unknown as { playgroundTestWorker: Worker }).playgroundTestWorker.terminate())
}, 25_000)

test('configuration viewer parses, filters, and exposes invalid and empty states', async () => {
  await open('content/config-viewer')
  const viewer = page.locator('lv-config-viewer')
  await viewer.getByRole('searchbox', { name: 'Filter configuration' }).fill('missing-key-example')
  await browserExpect(viewer.locator('.tree')).toBeEmpty()
  await page.getByLabel('Content state').selectOption('invalid')
  await browserExpect(viewer.getByRole('alert')).toBeVisible()
  await page.getByLabel('Content state').selectOption('empty')
  await browserExpect(viewer).toContainText('Loading configuration')
})

test('composer submits and accepts a local draft through public events', async () => {
  await open('content/chat-composer')
  const composer = page.locator('lv-chat-composer')
  await composer.locator('textarea').fill('Show revenue by region')
  await composer.getByRole('button', { name: 'Send', exact: true }).click()
  await openDetails()
  await browserExpect(page.getByRole('region', { name: 'Public event log' })).toContainText('lv-chat-submit')
  await browserExpect(page.getByRole('region', { name: 'Public event log' })).toContainText('Show revenue by region')
  await page.getByRole('button', { name: 'Accept draft', exact: true }).click()
  await browserExpect(composer.locator('textarea')).toHaveValue('')
})

test('drawer cycles keyboard focus, saves locally, and restores trigger focus', async () => {
  await open('surfaces/drawer')
  const trigger = page.getByRole('button', { name: 'Open drawer', exact: true })
  await trigger.click()
  const drawer = page.locator('lv-drawer')
  await browserExpect(drawer.getByRole('dialog')).toBeVisible()
  await drawer.getByLabel('Workspace name', { exact: true }).fill('Local workspace')
  await drawer.getByRole('button', { name: 'Save', exact: true }).click()
  await drawer.getByRole('button', { name: 'Close drawer', exact: true }).focus()
  await page.keyboard.press('Tab')
  await browserExpect(drawer.getByRole('button', { name: 'Close Playground settings', exact: true })).toBeFocused()
  await page.keyboard.press('Tab')
  await browserExpect(drawer.getByLabel('Workspace name', { exact: true })).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await browserExpect(drawer.getByRole('button', { name: 'Close Playground settings', exact: true })).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await browserExpect(drawer.getByRole('button', { name: 'Close drawer', exact: true })).toBeFocused()
  await page.keyboard.press('Escape')
  await browserExpect(drawer.getByRole('dialog')).toHaveCount(0)
  await browserExpect(trigger).toBeFocused()
})

test('drawer preserves native Tab traversal into nested production controls and skips disabled fields', async () => {
  await open('controls/select')
  await browserExpect(page.locator('playground-controls lv-select-menu')).toBeVisible()
  await open('surfaces/drawer')
  await page.getByRole('button', { name: 'Open drawer', exact: true }).click()
  const drawer = page.locator('lv-drawer')
  await browserExpect(drawer.getByRole('dialog')).toBeVisible()
  await drawer.evaluate(async (element) => {
    const nameField = element.querySelector('#surface-workspace-name')
    if (!nameField) throw new Error('Drawer fixture is missing its name field')
    const disabled = document.createElement('input')
    disabled.disabled = true
    disabled.setAttribute('aria-label', 'Unavailable drawer field')
    const menu = document.createElement('lv-select-menu') as HTMLElement & {
      label: string
      options: Array<{ value: string; label: string }>
      updateComplete: Promise<boolean>
    }
    menu.label = 'Nested refresh frequency'
    menu.options = [{ value: 'daily', label: 'Daily' }, { value: 'weekly', label: 'Weekly' }]
    nameField.closest('.settings-row')!.after(disabled, menu)
    await menu.updateComplete
  })
  const name = drawer.getByLabel('Workspace name', { exact: true })
  const nested = drawer.getByRole('button', { name: 'Nested refresh frequency', exact: true })
  const save = drawer.getByRole('button', { name: 'Save', exact: true })
  await browserExpect(drawer.getByLabel('Unavailable drawer field')).toBeDisabled()
  await name.focus()
  await page.keyboard.press('Tab')
  await browserExpect(nested).toBeFocused()
  await page.keyboard.press('Tab')
  await browserExpect(save).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await browserExpect(nested).toBeFocused()
  await page.keyboard.press('Shift+Tab')
  await browserExpect(name).toBeFocused()
})

test('icon picker and appearance editor acknowledge actual icon and color selection', async () => {
  await open('surfaces/icon-picker')
  let picker = page.locator('lv-dashboard-icon-picker')
  await picker.getByRole('searchbox', { name: 'Search icons' }).fill('chart-column')
  await picker.getByRole('button', { name: 'chart-column', exact: true }).click()
  await browserExpect(picker.getByRole('button', { name: 'chart-column', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await open('surfaces/appearance')
  const editor = page.locator('lv-dashboard-appearance-editor')
  await editor.getByRole('button', { name: 'Change icon', exact: true }).click()
  picker = editor.locator('lv-dashboard-icon-picker')
  await picker.getByRole('button', { name: 'blue', exact: true }).click()
  await browserExpect(editor.locator('.dashboard-appearance-color')).toHaveText('blue')
  await browserExpect(editor.getByText('Saving appearance…')).toHaveCount(0)
})

test('report footer zoom resizes the local report and remains usable on mobile', async () => {
  await open('surfaces/report-footer')
  const report = page.locator('playground-surfaces .report-page')
  const before = (await report.boundingBox())!.width
  await page.getByRole('button', { name: 'Zoom in', exact: true }).click()
  await browserExpect.poll(async () => (await report.boundingBox())!.width).toBeGreaterThan(before)
  await browserExpect(page.locator('playground-surfaces')).toContainText('110%')
  await page.setViewportSize({ width: 390, height: 844 })
  await browserExpect(page.getByRole('button', { name: 'Zoom in', exact: true })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390)
})

test('filter mutation commits locally and numeric ranges reject reversed bounds', async () => {
  await open('filters/leaf')
  await openDetails()
  await page.getByLabel('Filter presentation').selectOption('buttons')
  await page.locator('lv-filter-leaf').getByRole('button', { name: 'Europe', exact: true }).click()
  await browserExpect(page.locator('playground-filters .documentation')).toContainText('lv-filter-mutate')
  await browserExpect(page.locator('playground-filters .documentation')).toContainText('Europe')
  await page.getByLabel('Filter presentation').selectOption('numeric_range')
  const leaf = page.locator('lv-filter-leaf')
  await leaf.getByLabel('Minimum', { exact: true }).fill('100')
  await leaf.getByLabel('Maximum', { exact: true }).fill('10')
  await leaf.getByLabel('Maximum', { exact: true }).press('Enter')
  await browserExpect(leaf.getByRole('alert')).toBeVisible()
  await leaf.getByLabel('Maximum', { exact: true }).fill('200')
  await leaf.getByLabel('Maximum', { exact: true }).press('Enter')
  await browserExpect(leaf.getByRole('alert')).toHaveCount(0)
  await browserExpect(page.locator('playground-filters .documentation').locator('pre').last()).toContainText('range')
})

test('filter dock opens, closes, and reports local pane state', async () => {
  await open('filters/dock')
  await openDetails()
  const dock = page.locator('lv-filter-dock')
  await dock.getByRole('button', { name: 'Filters', exact: true }).click()
  await browserExpect(dock.getByLabel('Filters pane', { exact: true })).toBeVisible()
  await browserExpect(page.locator('playground-filters .documentation')).toContainText('lv-filter-dock-state')
  await dock.getByRole('button', { name: 'Close filters', exact: true }).first().click()
  await browserExpect(dock.getByLabel('Filters pane', { exact: true })).toBeHidden()
})
