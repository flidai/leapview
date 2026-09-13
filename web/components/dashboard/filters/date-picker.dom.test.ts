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
    expect(result.selected).toEqual({ display: '08/02/2025', mutations: 0 })
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
    expect(result).toEqual({ afterEscape: '01/02/2025', disabled: true, mutations: [] })
  } finally {
    await page.close()
  }
})

test('relative period displays the same normalized count that it commits', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'period', label: 'Relative period', field: 'orders.purchase_date', valueKind: 'date',
        predicates: [{ kind: 'relative_period', operators: [] }], options: { kind: 'none', limit: 0, values: [] },
      }
      leaf.binding = { key: 'period', readerEditable: true }
      leaf.presentation = { style: 'relative_period' }
      leaf.expression = { kind: 'relative_period', direction: 'previous', count: 1, unit: 'month', includeCurrent: false, anchor: 'current_time' }
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => {
        leaf.expression = event.detail.expression
      })
      document.body.append(leaf, document.createElement('button'))
      await leaf.updateComplete
    })
    const count = page.getByRole('spinbutton', { name: 'Period count', exact: true })
    for (const [entered, normalized] of [['0', '1'], ['-2', '1'], ['1.5', '1'], ['', '1'], ['1001', '1000'], ['1002', '1000'], ['2', '2']]) {
      await count.fill(entered!)
      await count.press('Tab')
      await page.waitForFunction(expected => (document.querySelector('lv-filter-leaf') as any).expression.count === Number(expected), normalized)
      expect(await count.inputValue()).toBe(normalized!)
    }
  } finally {
    await page.close()
  }
})

test('open multi-select refreshes invalidated choices so another value can be selected', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = { id: 'state', label: 'State', valueKind: 'string', options: { kind: 'distinct', limit: 50 } }
      leaf.binding = { key: 'state', readerEditable: true, selectionMode: 'multiple' }
      leaf.presentation = { style: 'dropdown', search: false }
      leaf.optionContext = 'initial'
      leaf.requests = 0
      leaf.addEventListener('lv-filter-options-needed', () => {
        leaf.requests++
        leaf.options = {
          bindingKey: 'state', servingStateID: 'serving', requestGeneration: leaf.requests, complete: true,
          items: ['SP', 'RJ'].map(value => ({ value: { kind: 'string', value }, label: value, available: true })),
        }
      })
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => {
        leaf.expression = event.detail.expression
        leaf.options = undefined // A filter revision invalidates the old server option page.
      })
      document.body.append(leaf)
      await leaf.updateComplete
    })
    await page.getByRole('button', { name: 'State: All', exact: true }).click()
    await page.getByRole('checkbox', { name: 'SP', exact: true }).check()
    await page.waitForFunction(() => (document.querySelector('lv-filter-leaf') as any).requests === 2, undefined, { timeout: 2000 })
    await page.getByRole('checkbox', { name: 'RJ', exact: true }).check()
    expect(await page.locator('lv-filter-leaf').evaluate((leaf: any) => leaf.expression.values.map((value: any) => value.value))).toEqual(['SP', 'RJ'])
    await page.locator('lv-filter-leaf').evaluate(async (leaf: any) => { await leaf.updateComplete; leaf.optionContext = 'new-dependency'; await leaf.updateComplete })
    await page.waitForFunction(() => (document.querySelector('lv-filter-leaf') as any).requests === 4, undefined, { timeout: 2000 })
    expect(await page.getByRole('checkbox', { name: 'SP', exact: true }).isChecked()).toBe(true)
    expect(await page.getByRole('checkbox', { name: 'RJ', exact: true }).isChecked()).toBe(true)
  } finally { await page.close() }
})

test('selecting a searched option preserves the query and its continuation cursor', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = { id: 'state', label: 'State', valueKind: 'string', options: { kind: 'distinct', limit: 2 } }
      leaf.binding = { key: 'state', readerEditable: true, selectionMode: 'multiple' }
      leaf.presentation = { style: 'dropdown', search: true }
      leaf.optionContext = 'revision-1'
      leaf.requests = []
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => {
        const request = event.detail
        leaf.requests.push(request)
        const generation = leaf.requests.length
        setTimeout(() => {
          leaf.options = {
            bindingKey: 'state', servingStateID: 'serving', requestGeneration: generation,
            complete: Boolean(request.cursor),
            nextCursor: request.cursor ? undefined : request.search ? 'searched-cursor' : 'unsearched-cursor',
            items: (request.cursor ? ['SP3'] : request.search ? ['SP', 'SP2'] : ['AC', 'AL']).map(value => ({
              value: { kind: 'string', value }, label: value, available: true,
            })),
          }
        }, 20)
      })
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => {
        leaf.expression = event.detail.expression
        leaf.options = undefined
        leaf.optionContext = 'revision-2'
      })
      document.body.append(leaf)
      await leaf.updateComplete
    })
    await page.getByRole('button', { name: 'State: All', exact: true }).click()
    await page.getByRole('searchbox', { name: 'Search State', exact: true }).fill('SP')
    await page.getByRole('checkbox', { name: 'SP', exact: true }).check()
    await page.waitForFunction(() => {
      const leaf = document.querySelector('lv-filter-leaf') as any
      return leaf.requests.length === 3 && !leaf.optionLoading
    })
    const refresh = await page.locator('lv-filter-leaf').evaluate((leaf: any) => leaf.requests.at(-1))
    expect(refresh).toMatchObject({ search: 'SP' })
    expect(await page.getByRole('checkbox', { name: 'SP2', exact: true }).isVisible()).toBe(true)
    await page.getByRole('button', { name: 'Load more values', exact: true }).click()
    await page.getByRole('checkbox', { name: 'SP3', exact: true }).waitFor()
    expect(await page.locator('lv-filter-leaf').evaluate((leaf: any) => leaf.requests.at(-1))).toMatchObject({
      search: 'SP', cursor: 'searched-cursor',
    })
    expect(await page.getByRole('checkbox', { name: 'SP', exact: true }).isChecked()).toBe(true)
    expect(await page.getByRole('checkbox', { name: 'SP2', exact: true }).isVisible()).toBe(true)
  } finally { await page.close() }
})

test('date picker bounds navigation, validates years, and clears or escapes cleanly', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-date-picker'))
    const result = await page.evaluate(async () => {
      const picker = document.createElement('lv-date-picker') as any
      picker.label = 'Start date'
      picker.value = '0001-01-02'
      picker.weekStart = 'sunday'
      const events: unknown[] = []
      picker.addEventListener('lv-date-input', (event: CustomEvent) => events.push(event.detail))
      document.body.append(picker)
      await picker.updateComplete

      const root = () => picker.shadowRoot as ShadowRoot
      const trigger = () => root().querySelector<HTMLButtonElement>('.date-trigger')!
      const yearInput = () => root().querySelector<HTMLInputElement>('.year-control')!
      const month = () => root().querySelector('.month-label')?.textContent?.trim()
      const dateValues = () => Array.from(root().querySelectorAll<HTMLButtonElement>('.calendar-day')).map(day => day.dataset.date)
      const open = async () => {
        trigger().click()
        await picker.updateComplete
      }
      const setYear = async (value: string) => {
        const input = yearInput()
        input.value = value
        input.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
        await picker.updateComplete
        return yearInput().value
      }

      await open()
      const initial = {
        month: month(), year: yearInput().value,
        hasYearOneDate: dateValues().includes('0001-01-01'),
        hasAccidental1901Date: dateValues().some(value => value?.startsWith('1901-')),
        previousSpilloverDisabled: root().querySelector<HTMLButtonElement>('[data-date="0000-12-31"]')?.disabled,
        firstFocusableDate: root().querySelector<HTMLButtonElement>('[tabindex="0"]')?.dataset.date,
        previousDisabled: root().querySelector<HTMLButtonElement>('[aria-label="Previous month"]')?.disabled,
      }
      root().querySelector<HTMLButtonElement>('[data-date="0000-12-31"]')?.click()
      await picker.updateComplete
      const ignoredOutOfRangeDate = picker.value

      root().querySelector<HTMLButtonElement>('[aria-label="Next month"]')!.click()
      await picker.updateComplete
      const nextMonth = month()
      root().querySelector<HTMLButtonElement>('[aria-label="Previous month"]')!.click()
      await picker.updateComplete
      const previousMonth = month()

      const validYear = await setYear('99')
      const invalidFraction = await setYear('1.5')
      const invalidLow = await setYear('0')
      const invalidHigh = await setYear('10000')
      await setYear('9999')
      for (let index = 0; index < 11; index += 1) {
        root().querySelector<HTMLButtonElement>('[aria-label="Next month"]')!.click()
        await picker.updateComplete
      }
      const upperBound = {
        month: month(), year: yearInput().value,
        nextSpilloverDisabled: root().querySelector<HTMLButtonElement>('[data-date="10000-01-01"]')?.disabled,
        nextDisabled: root().querySelector<HTMLButtonElement>('[aria-label="Next month"]')?.disabled,
      }
      root().querySelector<HTMLButtonElement>('[aria-label="Next month"]')!.click()
      await picker.updateComplete
      const afterDisabledNext = { month: month(), year: yearInput().value }

      root().querySelector<HTMLButtonElement>('.calendar-footer button')!.click()
      await picker.updateComplete
      const afterClear = {
        value: picker.value,
        display: root().querySelector('.date-placeholder')?.textContent?.trim(),
        open: root().querySelector('.date-popover')?.matches(':popover-open'),
      }

      picker.value = '2025-02-01'
      await picker.updateComplete
      await open()
      root().querySelector<HTMLElement>('.date-popover')!.dispatchEvent(new KeyboardEvent('keydown', {
        key: 'Escape', bubbles: true, composed: true,
      }))
      await picker.updateComplete
      await Promise.resolve()
      const afterEscape = {
        value: picker.value,
        open: root().querySelector('.date-popover')?.matches(':popover-open'),
        focusedTrigger: root().activeElement === trigger(),
      }
      return {
        initial, ignoredOutOfRangeDate, nextMonth, previousMonth,
        validYear, invalidFraction, invalidLow, invalidHigh,
        upperBound, afterDisabledNext, afterClear, afterEscape, events,
      }
    })

    expect(result).toMatchObject({
      initial: {
        month: 'January', year: '1', hasYearOneDate: true,
        hasAccidental1901Date: false, previousSpilloverDisabled: true,
        firstFocusableDate: '0001-01-02', previousDisabled: true,
      },
      ignoredOutOfRangeDate: '0001-01-02',
      nextMonth: 'February', previousMonth: 'January',
      validYear: '99', invalidFraction: '99', invalidLow: '99', invalidHigh: '99',
      upperBound: { month: 'December', year: '9999', nextSpilloverDisabled: true, nextDisabled: true },
      afterDisabledNext: { month: 'December', year: '9999' },
      afterClear: { value: '', display: 'No date', open: false },
      afterEscape: { value: '2025-02-01', open: false, focusedTrigger: true },
      events: [{ value: '', displayValue: '' }],
    })
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

test('timestamp date ranges commit whole calendar days and retain their displayed dates', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'purchase_time', label: 'Purchase period', field: 'orders.purchase_timestamp', valueKind: 'timestamp',
        predicates: [{ kind: 'range', operators: [] }], options: { kind: 'none', limit: 0, values: [] },
        timezone: 'America/New_York', calendar: 'gregorian', weekStart: 'monday',
      }
      leaf.binding = { key: 'purchase_time', id: 'purchase_time', filter: 'purchase_time', scope: 'report',
        default: { kind: 'unfiltered' }, readerEditable: true, targets: [], incomingDependencies: [] }
      leaf.presentation = { style: 'date_range', search: false, selectAll: false, showCounts: false, showSummary: false, compact: false }
      const mutations: any[] = []
      leaf.addEventListener('lv-filter-mutate', (event: CustomEvent) => mutations.push(event.detail.expression))
      document.body.append(leaf)
      await leaf.updateComplete
      const pickers = [...leaf.shadowRoot.querySelectorAll('lv-date-picker')] as any[]
      for (const picker of pickers) {
        picker.value = '2025-03-09'
        picker.dispatchEvent(new CustomEvent('lv-date-input', { detail: { value: picker.value }, bubbles: true, composed: true }))
        await leaf.updateComplete
      }
      pickers[1].dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, composed: true }))
      await leaf.updateComplete
      leaf.expression = mutations[0]
      await leaf.updateComplete
      await leaf.updateComplete
      await Promise.all(pickers.map(picker => picker.updateComplete))
      return { expression: mutations[0], displayed: pickers.map(picker => picker.shadowRoot.querySelector('.date-value').textContent.trim()) }
    })
    expect(result.expression).toEqual({ kind: 'range',
      lower: { value: { kind: 'timestamp', value: '2025-03-09T05:00:00Z' }, inclusive: true },
      upper: { value: { kind: 'timestamp', value: '2025-03-10T04:00:00Z' }, inclusive: false },
    })
    expect(result.displayed).toEqual(['09/03/2025', '09/03/2025'])
  } finally { await page.close() }
})

test('narrow filter cards retain full labels and compact slicers keep both date bounds visible', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-pane-card'))
    const result = await page.evaluate(async () => {
      const definition = { id: 'period', label: 'Purchase period', field: 'orders.purchase_date', valueKind: 'date', predicates: [{ kind: 'range', operators: [] }], options: { kind: 'none', limit: 0, values: [] }, timezone: 'UTC', calendar: 'gregorian', weekStart: 'monday' }
      const binding = { key: 'period', id: 'period', filter: 'period', scope: 'report', default: { kind: 'unfiltered' }, selectionMode: 'single', maxSelectedValues: 1, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] }
      const card = document.createElement('lv-filter-pane-card') as any
      card.definition = definition; card.binding = binding; card.style.width = '158px'
      const slicer = document.createElement('lv-slicer') as any
      slicer.definition = definition; slicer.binding = binding; slicer.style.cssText = 'width:320px;height:110px'
      document.body.append(card, slicer)
      await card.updateComplete; await slicer.updateComplete
      const leaf = slicer.shadowRoot.querySelector('lv-filter-leaf') as any
      await leaf.updateComplete
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)))
      const title = card.shadowRoot.querySelector('.title') as HTMLElement
      const emptyClearCount = card.shadowRoot.querySelectorAll('.actions button').length
      const pickers = [...leaf.shadowRoot.querySelectorAll('lv-date-picker')] as HTMLElement[]
      const box = slicer.getBoundingClientRect()
      const boundsVisible = pickers.length === 2 && pickers.every(picker => picker.getBoundingClientRect().bottom <= box.bottom + 1)
      card.expression = { kind: 'range', lower: { value: { kind: 'date', value: '2025-01-01' }, inclusive: true } }
      await card.updateComplete
      return { emptyClearCount, titleFits: title.scrollWidth <= title.clientWidth, boundsVisible, activeClearCount: card.shadowRoot.querySelectorAll('.actions button').length }
    })
    expect(result).toEqual({ emptyClearCount: 0, titleFits: true, boundsVisible: true, activeClearCount: 1 })
  } finally { await page.close() }
})
