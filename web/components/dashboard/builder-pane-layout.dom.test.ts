import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { builderTestDocument as testDocument } from './dashboard-builder-test-fixtures'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/dashboard-builder-test')
const documentWithProductFonts = () => testDocument()
  .replaceAll('system-ui', '"Inter Variable", Inter, system-ui')
  .replace('<head>', '<head><style>@font-face{font-family:"Inter Variable";src:url("/static/files/inter-latin-wght-normal.woff2") format("woff2");font-weight:100 900;}</style>')

async function withTimeout<T>(operation: Promise<T>, timeoutMs: number, message: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined
  try {
    return await Promise.race([
      operation,
      new Promise<T>((_, reject) => {
        timer = setTimeout(() => reject(new Error(message)), timeoutMs)
      }),
    ])
  } finally {
    if (timer) clearTimeout(timer)
  }
}

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(documentWithProductFonts())
      return
    }
    const fileRoot = url.pathname.startsWith('/static/') ? projectRoot : root
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
  if (!address || typeof address === 'string') throw new Error('dashboard builder test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 30_000)

async function measureInspector(width: number) {
  const page = await browser.newPage({ viewport: { width, height: 1000 } })
  try {
    page.setDefaultTimeout(6_000)
    page.setDefaultNavigationTimeout(6_000)
    await page.goto(baseURL, { waitUntil: 'domcontentloaded' })
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'), null, { timeout: 6_000 })
    await withTimeout(page.evaluate(() => document.fonts.ready), 6_000, 'product fonts did not settle')
    return await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const awaitUpdate = async () => {
        let timer: number | undefined
        try {
          await Promise.race([
            element.updateComplete,
            new Promise((_, reject) => { timer = window.setTimeout(() => reject(new Error('dashboard builder update did not settle')), 6_000) }),
          ])
        } finally {
          if (timer !== undefined) window.clearTimeout(timer)
        }
      }
      await awaitUpdate()
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const pages = structuredClone(element.builder.pages)
      const visual = pages[0].visuals[0]
      visual.type = 'combo'
      visual.title = 'Actual, budget, and forecast revenue'
      visual.slots = [
        { id: 'dimension-0', label: 'Finance month', kind: 'dimension', fieldId: 'finance_month', alias: 'Finance month' },
        { id: 'metric-0', label: 'Net revenue', kind: 'metric', fieldId: 'net_revenue', alias: 'Net revenue' },
        { id: 'metric-1', label: 'Budget revenue', kind: 'metric', fieldId: 'budget_revenue', alias: 'Budget revenue' },
        { id: 'metric-2', label: 'Forecast revenue', kind: 'metric', fieldId: 'forecast_revenue', alias: 'Forecast revenue' },
      ]
      visual.queryOptions = { supportsSort: true, supportsLimit: true, sort: [{ field: 'Finance month', direction: 'asc' }], limit: 24 }
      visual.formatOptions = [
        { key: 'axisVisible', label: 'Show axes', section: 'Display', control: 'toggle', value: 'true', choices: [] },
        { key: 'legend', label: 'Legend', section: 'Display', control: 'select', value: 'bottom', choices: [{ value: 'none', label: 'None' }, { value: 'top', label: 'Top' }, { value: 'right', label: 'Right' }, { value: 'bottom', label: 'Bottom' }, { value: 'left', label: 'Left' }] },
        { key: 'labels.density', label: 'Data labels', section: 'Display', control: 'select', value: 'hidden', choices: [{ value: 'hidden', label: 'Hidden' }, { value: 'automatic', label: 'Automatic' }, { value: 'dense', label: 'Dense' }, { value: 'always', label: 'Always' }] },
        { key: 'labels.maxCharacters', label: 'Maximum label characters', section: 'Labels', control: 'number', value: '24', choices: [] },
        { key: 'labels.minimumSpacing', label: 'Minimum label spacing', section: 'Labels', control: 'number', value: '0', choices: [] },
        { key: 'labels.tooltipFallback', label: 'Tooltip for truncated labels', section: 'Labels', control: 'toggle', value: 'true', choices: [] },
        { key: 'stacking', label: 'Stacking', section: 'Chart', control: 'select', value: 'none', choices: [{ value: 'none', label: 'None' }, { value: 'normal', label: 'Normal' }, { value: 'percent', label: 'Percent' }] },
        { key: 'orientation', label: 'Orientation', section: 'Chart', control: 'select', value: '', choices: [{ value: 'horizontal', label: 'Horizontal' }, { value: 'vertical', label: 'Vertical' }] },
        { key: 'showSymbols', label: 'Show symbols', section: 'Chart', control: 'toggle', value: 'false', choices: [] },
        { key: 'smooth', label: 'Smooth lines', section: 'Chart', control: 'toggle', value: 'false', choices: [] },
        { key: 'step', label: 'Stepped lines', section: 'Chart', control: 'toggle', value: 'false', choices: [] },
        { key: 'dataZoom', label: 'Data zoom', section: 'Interaction', control: 'toggle', value: 'false', choices: [] },
        { key: 'symbolSize', label: 'Symbol size', section: 'Chart', control: 'number', value: '', choices: [] },
        { key: 'labelPosition', label: 'Label position', section: 'Display', control: 'select', value: 'automatic', choices: [{ value: 'automatic', label: 'Automatic' }, { value: 'inside', label: 'Inside' }, { value: 'outside', label: 'Outside' }, { value: 'top', label: 'Top' }] },
        { key: 'displayUnits', label: 'Display units', section: 'Values', control: 'select', value: 'auto', choices: [{ value: 'auto', label: 'Auto' }, { value: 'none', label: 'None' }, { value: 'thousands', label: 'Thousands' }, { value: 'millions', label: 'Millions' }, { value: 'billions', label: 'Billions' }, { value: 'trillions', label: 'Trillions' }] },
      ]
      mergePatch({ builder: {
        semanticModel: { id: 'commerce', title: 'Orders', datasets: [{ id: 'orders', title: 'Orders', fields: [
          { id: 'finance_month', label: 'Finance month', kind: 'dimension', dataType: 'string' },
          { id: 'net_revenue', label: 'Net revenue', kind: 'metric', dataType: 'decimal' },
          { id: 'budget_revenue', label: 'Budget revenue', kind: 'metric', dataType: 'decimal' },
          { id: 'forecast_revenue', label: 'Forecast revenue', kind: 'metric', dataType: 'decimal' },
        ] }] },
        pages,
      } })
      await awaitUpdate()
      const root = element.shadowRoot as ShadowRoot
      const visualPane = root.querySelector('.visual-builder') as HTMLElement
      const visualRight = visualPane.getBoundingClientRect().left + visualPane.clientWidth
      const controls = Array.from(root.querySelectorAll<HTMLElement>(
        '.visual-reference-link, .visual-picker-button, [data-query-control], .field-token',
      ))
      return {
        viewportWidth: innerWidth,
        visualClientWidth: visualPane.clientWidth,
        visualScrollWidth: visualPane.scrollWidth,
        referenceCount: root.querySelectorAll('.visual-reference-link').length,
        pickerButtonCount: root.querySelectorAll('.visual-picker-button').length,
        queryControlCount: root.querySelectorAll('[data-query-control]').length,
        controlsFit: controls.every((node) => node.getBoundingClientRect().right <= visualRight + 1),
      }
    })
  } finally {
    await page.close()
  }
}

test('visual inspector controls stay within the pane at desktop and narrow widths', async () => {
  const desktop = await measureInspector(1440)
  const narrow = await measureInspector(700)
  for (const layout of [desktop, narrow]) {
    expect(layout.visualScrollWidth).toBe(layout.visualClientWidth)
    expect(layout.controlsFit).toBe(true)
    expect(layout.referenceCount).toBe(2)
    expect(layout.pickerButtonCount).toBe(27)
    expect(layout.queryControlCount).toBeGreaterThan(3)
  }
}, 45_000)
