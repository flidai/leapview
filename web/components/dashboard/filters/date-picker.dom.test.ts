import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser
const root = join(process.cwd(), '.tmp/date-picker-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const file = normalize(join(root, url.pathname))
    if (!file.startsWith(root)) {
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
  if (!address || typeof address === 'string') throw new Error('date picker test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('date range owns compact calendars, Monday-first weeks, and canonical selection events', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'purchase_date', label: 'Purchase date', field: 'orders.purchase_date', valueKind: 'date',
        predicates: [{ kind: 'range', operators: [] }], options: { kind: 'none', limit: 0, values: [] },
        timezone: 'UTC', calendar: 'gregorian', weekStart: 'monday',
      }
      leaf.binding = {
        key: 'purchase_date', id: 'purchase_date', filter: 'purchase_date', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.expression = {
        kind: 'range',
        lower: { value: { kind: 'date', value: '2025-02-01' }, inclusive: true },
        upper: { value: { kind: 'date', value: '2025-02-10' }, inclusive: true },
      }
      leaf.presentation = { style: 'date_range', search: false, selectAll: false, showCounts: false, showSummary: false, compact: false }
      const outside = document.createElement('button')
      const mutations: unknown[] = []
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => mutations.push(event.detail))
      document.body.append(leaf, outside)
      await leaf.updateComplete
      const pickers = Array.from((leaf.shadowRoot as ShadowRoot).querySelectorAll('lv-date-picker')) as any[]
      const first = pickers[0]
      const trigger = (first.shadowRoot as ShadowRoot).querySelector('.date-trigger') as HTMLButtonElement
      trigger.click()
      await first.updateComplete
      const popover = (first.shadowRoot as ShadowRoot).querySelector('.date-popover') as HTMLElement
      const calendar = (first.shadowRoot as ShadowRoot).querySelector('.calendar-grid') as HTMLElement
      const geometry = {
        controls: pickers.length,
        open: popover.matches(':popover-open'),
        width: popover.getBoundingClientRect().width,
        overflow: getComputedStyle(popover).overflowY,
        calendarOverflow: getComputedStyle(calendar).overflowY,
        scrolls: popover.scrollHeight > popover.clientHeight || calendar.scrollHeight > calendar.clientHeight,
        weekdays: Array.from((first.shadowRoot as ShadowRoot).querySelectorAll('.calendar-weekday')).map(node => node.textContent?.trim()),
        year: ((first.shadowRoot as ShadowRoot).querySelector('.year-control') as HTMLInputElement).value,
      }
      ;((first.shadowRoot as ShadowRoot).querySelector('[data-date="2025-02-08"]') as HTMLButtonElement).click()
      await leaf.updateComplete
      const selected = {
        display: (first.shadowRoot as ShadowRoot).querySelector('.date-value')?.textContent?.trim(),
        mutations: mutations.length,
      }
      trigger.click()
      await first.updateComplete
      const year = (first.shadowRoot as ShadowRoot).querySelector('.year-control') as HTMLInputElement
      year.focus()
      year.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, composed: true }))
      await leaf.updateComplete
      const whileInside = {
        open: (first.shadowRoot as ShadowRoot).querySelector('.date-popover')?.matches(':popover-open'),
        mutations: mutations.length,
      }
      outside.focus()
      await leaf.updateComplete
      return { geometry, selected, whileInside, mutations }
    })
    expect(result.geometry).toMatchObject({
      controls: 2, open: true, overflow: 'hidden', calendarOverflow: 'visible', scrolls: false,
      weekdays: ['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su'], year: '2025',
    })
    expect(result.geometry.width).toBeGreaterThanOrEqual(240)
    expect(result.geometry.width).toBeLessThanOrEqual(320)
    expect(result.selected).toEqual({ display: '08-02-2025', mutations: 0 })
    expect(result.whileInside).toEqual({ open: true, mutations: 0 })
    expect(result.mutations).toEqual([{
      bindingKey: 'purchase_date',
      expression: {
        kind: 'range',
        lower: { value: { kind: 'date', value: '2025-02-08' }, inclusive: true },
        upper: { value: { kind: 'date', value: '2025-02-10' }, inclusive: true },
      },
    }])
  } finally {
    await page.close()
  }
})

test('date range Escape discards the draft and non-editable controls stay disabled', async () => {
  const page = await browser.newPage({ viewport: { width: 640, height: 520 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'purchase_date', label: 'Purchase date', field: 'orders.purchase_date', valueKind: 'date',
        predicates: [{ kind: 'range', operators: [] }], options: { kind: 'none', limit: 0, values: [] },
        timezone: 'UTC', calendar: 'gregorian', weekStart: 'monday',
      }
      leaf.binding = {
        key: 'purchase_date', id: 'purchase_date', filter: 'purchase_date', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.expression = { kind: 'range', lower: { value: { kind: 'date', value: '2025-02-01' }, inclusive: true } }
      leaf.presentation = { style: 'date_range', search: false, selectAll: false, showCounts: false, showSummary: false, compact: false }
      const mutations: unknown[] = []
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => mutations.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      const picker = (leaf.shadowRoot as ShadowRoot).querySelector('lv-date-picker') as any
      (picker.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.date-trigger')!.click()
      await picker.updateComplete
      ;((picker.shadowRoot as ShadowRoot).querySelector('[data-date="2025-02-08"]') as HTMLButtonElement).click()
      await leaf.updateComplete
      ;((picker.shadowRoot as ShadowRoot).querySelector('.date-trigger') as HTMLButtonElement).dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true }))
      await leaf.updateComplete
      const afterEscape = (picker.shadowRoot as ShadowRoot).querySelector('.date-value')?.textContent?.trim()
      leaf.binding = { ...leaf.binding, readerEditable: false }
      await leaf.updateComplete
      return { afterEscape, disabled: ((picker.shadowRoot as ShadowRoot).querySelector('.date-trigger') as HTMLButtonElement).disabled, mutations }
    })
    expect(result).toEqual({ afterEscape: '01-02-2025', disabled: true, mutations: [] })
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  return `
    <!doctype html>
    <html>
      <head><style>
        body { ${typographyTestTokens} --lv-bg-panel: #fff; --lv-bg-overlay: #fff; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-danger: #d1242f; --lv-line-accent: #0969da; --lv-accent: #0969da; --lv-border-default: 1px solid #d8dee4; --lv-radius-default: 6px; --lv-radius-tight: 4px; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-16: 16px; --control-medium-size: 32px; --lv-control-compact: 28px; --lv-border-width-focus: 2px; --lv-shadow-floating-lg: 0 8px 24px #0002; }
      </style></head>
      <body><script type="module" src="/date-picker-under-test.js"></script></body>
    </html>
  `
}
