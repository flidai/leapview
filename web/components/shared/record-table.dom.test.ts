import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser, type Page } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/record-table-test')

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
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('record table renders cells and sorts through TanStack headers', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.table = {
        columns: [
          { id: 'name', header: 'Name', kind: 'entity', width: '260px' },
          { id: 'status', header: 'Status', kind: 'status', width: '150px' },
          { id: 'score', header: 'Score', kind: 'number', align: 'right', width: '110px' },
          { id: 'key', header: 'Key', kind: 'code', width: '160px' },
          { id: 'kind', header: 'Kind', kind: 'badge', width: '130px' },
          { id: 'roles', header: 'Roles', kind: 'tags', width: '180px' },
          { id: 'sql', header: 'SQL', kind: 'expression', width: '220px' },
          { id: 'actions', header: 'Actions', kind: 'actions', align: 'right', sortable: false, width: '96px' },
        ],
        rows: [
          {
            name: { label: 'Orders', description: 'Fact table', href: '/orders', icon: 'table' },
            status: { label: 'succeeded', tone: 'success' },
            score: 2,
            key: 'orders',
            kind: { label: 'table', tone: 'muted' },
            roles: ['sales', 'ops'],
            sql: 'select * from orders',
            actions: [{ label: 'Open', href: '/orders/open', icon: 'open' }],
          },
          {
            name: { label: 'Customers', description: 'Dimension table', href: '/customers', icon: 'semantic_model' },
            status: { label: 'queued', tone: 'attention' },
            score: 1,
            key: 'customers',
            kind: { label: 'view', tone: 'accent' },
            roles: ['crm'],
            sql: 'select * from customers',
            actions: [{ label: 'Open', href: '/customers/open', icon: 'open' }],
          },
        ],
        empty: 'No records.',
        minWidth: '1300px',
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)

    const initial = await tableState(page)
    expect(initial.firstNames).toEqual(['Orders', 'Customers'])
    expect(initial.hasEntityLink).toBe(true)
    expect(initial.hasStatusIcon).toBe(true)
    expect(initial.hasCode).toBe(true)
    expect(initial.hasExpression).toBe(true)
    expect(initial.badges).toEqual(['table', 'view'])
    expect(initial.tags).toEqual(['sales', 'ops', 'crm'])
    expect(initial.actionHref).toBe('/orders/open')
    expect(initial.minWidth).toBe('1300px')
    expect(initial.headerPosition).toBe('sticky')
    expect(initial.hasSelector).toBe(false)

    await page.locator('lv-record-table th:nth-child(3) button').click()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect((await tableState(page)).firstNames).toEqual(['Customers', 'Orders'])

    await page.locator('lv-record-table th:nth-child(3) button').click()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect((await tableState(page)).firstNames).toEqual(['Orders', 'Customers'])
  } finally {
    await page.close()
  }
})

test('mobile record tables expose a horizontal-scroll affordance without changing desktop chrome', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.table = {
        columns: [{ id: 'name', header: 'Name' }, { id: 'identifier', header: 'Identifier' }],
        rows: [{ name: 'Orders', identifier: 'source:orders' }],
        minWidth: '640px',
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    const mobile = await page.locator('lv-record-table').evaluate((element) => {
      const region = element.querySelector('.record-table-wrap') as HTMLElement
      const hint = element.querySelector('.record-table-scroll-hint') as HTMLElement
      return {
        role: region.getAttribute('role'),
        label: region.getAttribute('aria-label'),
        tabIndex: region.getAttribute('tabindex'),
        hint: hint.textContent?.replace(/\s+/g, ' ').trim(),
        hintDisplay: getComputedStyle(hint).display,
      }
    })
    expect(mobile).toEqual({
      role: 'region', label: 'Scrollable table', tabIndex: '0',
      hint: 'Swipe horizontally to see more columns →', hintDisplay: 'block',
    })
  } finally {
    await page.close()
  }

  const desktop = await browser.newPage({ viewport: { width: 1280, height: 620 } })
  try {
    await desktop.goto(baseURL)
    await desktop.waitForFunction(() => customElements.get('lv-record-table'))
    await desktop.locator('lv-record-table').evaluate((element: any) => {
      element.table = { columns: [{ id: 'name', header: 'Name' }], rows: [{ name: 'Orders' }], minWidth: '640px' }
    })
    await desktop.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect(await desktop.locator('lv-record-table .record-table-scroll-hint').evaluate((element) => getComputedStyle(element).display)).toBe('none')
  } finally {
    await desktop.close()
  }
})

test('narrow record tables keep entity columns usable without pinning them', async () => {
  const page = await browser.newPage({ viewport: { width: 520, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.setAttribute('variant', 'primary')
      element.table = {
        columns: [
          { id: 'name', header: 'Name', kind: 'entity' },
          { id: 'type', header: 'Type', width: '150px' },
          { id: 'parent', header: 'Parent', width: '180px' },
          { id: 'key', header: 'Identifier', kind: 'code', width: '220px' },
        ],
        rows: [{
          name: { label: 'Brazilian ZIP Geolocation', href: '/sources/geolocation', icon: 'source' },
          type: 'Source',
          parent: 'Olist DuckLake',
          key: 'olist_geolocation',
        }],
        minWidth: '640px',
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)

    const initial = await leadingColumnState(page)
    await page.locator('lv-record-table .record-table-wrap').evaluate((element) => { element.scrollLeft = 300 })
    const scrolled = await leadingColumnState(page)

    expect(initial.wrapScrollWidth).toBeGreaterThan(initial.wrapWidth)
    expect(initial.cellWidth).toBeGreaterThanOrEqual(220)
    expect(initial.cellWidth).toBeLessThanOrEqual(280)
    expect(initial.linkWidth).toBeGreaterThan(0)
    expect(initial.cellPosition).toBe('static')
    expect(scrolled.wrapScrollLeft).toBeGreaterThan(0)
    expect(scrolled.cellLeft).toBeLessThan(initial.cellLeft)
    expect(scrolled.linkLeft).toBeLessThan(initial.linkLeft)
    expect(scrolled.documentOverflow).toBe(0)
  } finally {
    await page.close()
  }
})

test('record table column toggles expose the column header as their accessible name', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.table = {
        columns: [
          { id: 'name', header: 'Name', toggleable: true },
          { id: 'status', header: 'Status', toggleable: true },
        ],
        rows: [{ name: 'Orders', status: 'Paid' }],
        columnSelector: { enabled: true, storageKey: 'record-table-a11y' },
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    await page.locator('lv-record-table summary').click()
    const labels = await page.locator('lv-record-table input[type="checkbox"]').evaluateAll((inputs) => inputs.map((input) => input.getAttribute('aria-label')))
    expect(labels).toEqual(['Name', 'Status'])
  } finally {
    await page.close()
  }
})

test('primary record table gives entity cells the full column width', async () => {
  const page = await browser.newPage({ viewport: { width: 760, height: 520 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.setAttribute('variant', 'primary')
      element.table = {
        columns: [
          { id: 'name', header: 'Name', kind: 'entity', width: '380px' },
          { id: 'type', header: 'Type', width: '160px' },
        ],
        rows: [{
          name: {
            label: 'Executive Sales Dashboard',
            description: 'Sales, order, category, and delivery overview.',
            href: '/dashboards/executive-sales',
            icon: 'dashboard',
            iconTreatment: 'framed',
          },
          type: 'Dashboard',
        }, {
          name: {
            label: 'Operations model',
            href: '/models/operations',
            icon: 'semantic_model',
            iconTreatment: 'plain',
          },
          type: 'Semantic model',
        }],
        empty: 'No records.',
        minWidth: '620px',
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    const state = await page.locator('lv-record-table').evaluate((element) => {
      const cell = element.querySelector('tbody td:first-child') as HTMLElement
      const link = element.querySelector('.record-entity-link') as HTMLElement
      const title = element.querySelector('.record-entity-label') as HTMLElement
      const description = element.querySelector('.record-entity-description') as HTMLElement
      const cellStyle = getComputedStyle(cell)
      const cellPadding = Number.parseFloat(cellStyle.paddingLeft) + Number.parseFloat(cellStyle.paddingRight)
      return {
        variant: element.getAttribute('variant'),
        linkWidth: Math.round(link.getBoundingClientRect().width),
        cellInnerWidth: Math.round(cell.getBoundingClientRect().width - cellPadding),
        titleFits: title.getBoundingClientRect().right <= cell.getBoundingClientRect().right,
        descriptionFits: description.getBoundingClientRect().right <= cell.getBoundingClientRect().right,
        iconTreatments: Array.from(element.querySelectorAll<HTMLElement>('.record-entity-icon')).map((icon) => ({
          framed: icon.classList.contains('is-framed'),
          plain: icon.classList.contains('is-plain'),
          background: getComputedStyle(icon).backgroundColor,
          borderWidth: getComputedStyle(icon).borderTopWidth,
        })),
      }
    })
    expect(state.variant).toBe('primary')
    expect(state.linkWidth).toBeGreaterThanOrEqual(state.cellInnerWidth - 4)
    expect(state.titleFits).toBe(true)
    expect(state.descriptionFits).toBe(true)
    expect(state.iconTreatments[0]).toEqual({ framed: true, plain: false, background: 'rgb(246, 248, 250)', borderWidth: '1px' })
    expect(state.iconTreatments[1]).toEqual({ framed: false, plain: true, background: 'rgba(0, 0, 0, 0)', borderWidth: '0px' })
  } finally {
    await page.close()
  }
})

test('compact record table keeps metadata dense and scalar placeholders muted', async () => {
  const page = await browser.newPage({ viewport: { width: 760, height: 520 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.setAttribute('variant', 'compact')
      element.table = {
        columns: [
          { id: 'name', header: 'Name', width: '220px' },
          { id: 'email', header: 'Email', kind: 'link', hrefKey: 'emailHref', width: '260px' },
          { id: 'roles', header: 'Roles', kind: 'number', width: '100px' },
          { id: 'key', header: 'Key', kind: 'code', width: '160px' },
        ],
        rows: [
          { name: 'Analyst', email: 'analyst@example.com', emailHref: 'mailto:analyst@example.com', roles: 3, key: 'analyst' },
          { name: '', email: '', roles: null, key: '' },
        ],
        empty: 'No records.',
        minWidth: '740px',
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    const initial = await page.locator('lv-record-table').evaluate((element) => {
      const wrap = element.querySelector('.record-table-wrap') as HTMLElement
      const firstHeader = element.querySelector('thead th') as HTMLElement
      const firstCell = element.querySelector('tbody td') as HTMLElement
      const firstCode = element.querySelector('tbody tr:first-child .record-code') as HTMLElement
      const numberHeader = element.querySelector('thead th:nth-child(3)') as HTMLElement
      const numberCell = element.querySelector('tbody tr:first-child td:nth-child(3)') as HTMLElement
      const unsortedIndicator = element.querySelector('thead th:first-child .record-table-sort-indicator') as HTMLElement
      return {
        variant: element.getAttribute('variant'),
        hasCompactClass: wrap.classList.contains('variant-compact'),
        headerPaddingTop: getComputedStyle(firstHeader).paddingTop,
        cellPaddingTop: getComputedStyle(firstCell).paddingTop,
        cellFontSize: getComputedStyle(firstCell).fontSize,
        codeFontSize: getComputedStyle(firstCode).fontSize,
        headerBackground: getComputedStyle(firstHeader).backgroundColor,
        headerTextTransform: getComputedStyle(firstHeader).textTransform,
        wrapBorderTopWidth: getComputedStyle(wrap).borderTopWidth,
        cellBorderBottomWidth: getComputedStyle(firstCell).borderBottomWidth,
        numberHeaderAlign: getComputedStyle(numberHeader).textAlign,
        numberCellAlign: getComputedStyle(numberCell).textAlign,
        mutedCount: element.querySelectorAll('.record-muted').length,
        unsortedIndicatorOpacity: getComputedStyle(unsortedIndicator).opacity,
      }
    })
    expect(initial.variant).toBe('compact')
    expect(initial.hasCompactClass).toBe(true)
    expect(initial.headerPaddingTop).toBe('8px')
    expect(initial.cellPaddingTop).toBe('8px')
    expect(initial.cellFontSize).toBe('14px')
    expect(initial.codeFontSize).toBe('14px')
    expect(initial.headerBackground).toBe('rgb(238, 242, 246)')
    expect(initial.headerTextTransform).toBe('none')
    expect(initial.wrapBorderTopWidth).toBe('0px')
    expect(initial.cellBorderBottomWidth).toBe('0px')
    expect(initial.numberHeaderAlign).toBe('right')
    expect(initial.numberCellAlign).toBe('right')
    expect(initial.mutedCount).toBe(4)
    expect(initial.unsortedIndicatorOpacity).toBe('0')

    await page.locator('lv-record-table th:nth-child(3) button').click()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    const sorted = await page.locator('lv-record-table').evaluate((element) => {
      const sortedIndicator = element.querySelector('thead th:nth-child(3) .record-table-sort-indicator') as HTMLElement
      return {
        sortedIndicatorOpacity: getComputedStyle(sortedIndicator).opacity,
        sortedIndicatorText: sortedIndicator.textContent?.trim(),
      }
    })
    expect(sorted.sortedIndicatorOpacity).toBe('1')
    expect(sorted.sortedIndicatorText).toBe('↑')
  } finally {
    await page.close()
  }
})

test('record table renders tight expandable query rows', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.evaluate(() => localStorage.removeItem('record-table-query-columns'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.setAttribute('variant', 'compact')
      element.table = {
        density: 'tight',
        columns: [
          { id: 'query', header: 'Query', kind: 'query', width: '520px', toggleable: false },
          { id: 'runtime', header: 'Runtime', width: '160px' },
          { id: 'actions', header: '', kind: 'actions', sortable: false, toggleable: false, width: '64px' },
        ],
        rows: [{
          id: 'query_1',
          query: {
            label: 'select customer_id, customer_city from customers where customer_state = $1 order by customer_id',
            statusLabel: 'success',
            tone: 'success',
            icon: 'check',
            expandedContent: 'select customer_id, customer_city\nfrom customers\nwhere customer_state = $1\norder by customer_id',
          },
          runtime: 'sales',
          actions: [{ label: 'Details', action: 'detail' }],
        }],
        columnSelector: {
          enabled: true,
          storageKey: 'record-table-query-columns',
          defaultColumns: ['runtime'],
        },
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)

    const collapsed = await queryRowState(page)
    expect(collapsed.headers).toEqual(['Query', 'Runtime', ''])
    expect(collapsed.menuLabels).toEqual(['Runtime'])
    expect(collapsed.statusLabel).toBe('success')
    expect(collapsed.queryText).toContain('select customer_id')
    expect(collapsed.hasExpandedRow).toBe(false)
    expect(collapsed.wrapHasTightDensity).toBe(true)
    expect(collapsed.cellPaddingTop).toBe('4px')
    expect(collapsed.rowHeight).toBeLessThanOrEqual(40)

    await page.locator('lv-record-table .record-query-expand').click()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    const expanded = await queryRowState(page)
    expect(expanded.hasExpandedRow).toBe(true)
    expect(expanded.hasCodeBlock).toBe(true)
    expect(expanded.expandedText).toContain('FROM')
    expect(expanded.formattedCode).toContain('SELECT')
    expect(expanded.formattedCode).toMatch(/\nFROM\n\s+customers/)
    expect(expanded.expandedColspan).toBe(3)

    await page.locator('lv-record-table .record-query-expand').click()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect((await queryRowState(page)).hasExpandedRow).toBe(false)
  } finally {
    await page.close()
  }
})

test('record table emits configured row actions without stealing interactive controls', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.table = {
        rowAction: 'detail',
        columns: [
          { id: 'query', header: 'Query', kind: 'query', width: '520px', toggleable: false },
          { id: 'runtime', header: 'Runtime', width: '160px' },
        ],
        rows: [{
          id: 'query_1',
          query: {
            label: 'select * from orders',
            statusLabel: 'success',
            tone: 'success',
            icon: 'check',
            expandedContent: 'select *\nfrom orders',
          },
          runtime: 'sales',
        }],
      }
      ;(window as any).recordTableActions = []
      element.addEventListener('lv-record-table-action', (event: CustomEvent) => {
        ;(window as any).recordTableActions.push(event.detail)
      })
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)

    await page.locator('lv-record-table tbody tr.record-row').click()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect(await rowActionState(page)).toEqual({ count: 1, action: 'detail', rowID: 'query_1', expanded: false })

    await page.locator('lv-record-table .record-query-expand').click()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect(await rowActionState(page)).toEqual({ count: 1, action: 'detail', rowID: 'query_1', expanded: true })

    await page.locator('lv-record-table tbody tr.record-row').focus()
    await page.keyboard.press('Enter')
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect(await rowActionState(page)).toEqual({ count: 2, action: 'detail', rowID: 'query_1', expanded: true })

    await page.keyboard.press('Space')
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect(await rowActionState(page)).toEqual({ count: 3, action: 'detail', rowID: 'query_1', expanded: true })

    await page.locator('lv-record-table').evaluate((element: any) => {
      element.table = {
        columns: [{ id: 'name', header: 'Name' }],
        rows: [{ id: 'plain_1', name: 'Plain row' }],
      }
      ;(window as any).recordTableActions = []
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    await page.locator('lv-record-table tbody tr.record-row').click()
    expect(await rowActionState(page)).toEqual({ count: 0, action: '', rowID: '', expanded: false })
  } finally {
    await page.close()
  }
})

test('record table renders configured empty state', async () => {
  const page = await browser.newPage({ viewport: { width: 500, height: 360 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.table = { columns: [{ id: 'name', header: 'Name' }], rows: [], empty: 'No records.' }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    const empty = await page.locator('lv-record-table .record-table-empty').textContent()
    expect(empty).toBe('No records.')
  } finally {
    await page.close()
  }
})

test('record table column selector hides, restores, and persists columns', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 620 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.evaluate(() => localStorage.removeItem('record-table-test-columns'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.table = {
        columns: [
          { id: 'name', header: 'Name', width: '220px' },
          { id: 'runtime', header: 'Runtime', width: '160px' },
          { id: 'rows', header: 'Rows', kind: 'number', width: '100px' },
          { id: 'actions', header: '', kind: 'actions', toggleable: false, width: '64px' },
        ],
        rows: [
          { name: 'select * from orders', runtime: 'sales', rows: 10, actions: [{ label: 'Details', action: 'detail' }] },
        ],
        columnSelector: {
          enabled: true,
          storageKey: 'record-table-test-columns',
          defaultColumns: ['name', 'runtime', 'rows'],
        },
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)

    const initial = await columnSelectorState(page)
    expect(initial.hasSelector).toBe(true)
    expect(initial.selectorInCorner).toBe(true)
    expect(initial.hasSeparateToolbar).toBe(false)
    expect(initial.headers).toEqual(['Name', 'Runtime', 'Rows', ''])
    expect(initial.menuLabels).toEqual(['Name', 'Runtime', 'Rows'])
    expect(initial.hasActionsColumn).toBe(true)

    await page.locator('lv-record-table .record-table-column-selector summary').click()
    await page.locator('lv-record-table .record-table-column-menu label', { hasText: 'Runtime' }).locator('input').uncheck()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    const hidden = await columnSelectorState(page)
    expect(hidden.headers).toEqual(['Name', 'Rows', ''])
    expect(hidden.hasRuntimeCell).toBe(false)
    expect(hidden.hasActionsColumn).toBe(true)
    expect(hidden.storedColumns).toEqual(['name', 'rows'])

    await page.locator('lv-record-table .record-table-column-menu label', { hasText: 'Runtime' }).locator('input').check()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect((await columnSelectorState(page)).headers).toEqual(['Name', 'Runtime', 'Rows', ''])

    await page.locator('lv-record-table .record-table-column-menu label', { hasText: 'Runtime' }).locator('input').uncheck()
    await page.locator('lv-record-table .record-table-column-menu label', { hasText: 'Rows' }).locator('input').uncheck()
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    const lastVisible = await columnSelectorState(page)
    expect(lastVisible.headers).toEqual(['Name', ''])
    expect(lastVisible.disabledChecks).toEqual(['Name'])

    await page.reload()
    await page.waitForFunction(() => customElements.get('lv-record-table'))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.table = {
        columns: [
          { id: 'name', header: 'Name', width: '220px' },
          { id: 'runtime', header: 'Runtime', width: '160px' },
          { id: 'rows', header: 'Rows', kind: 'number', width: '100px' },
          { id: 'actions', header: '', kind: 'actions', toggleable: false, width: '64px' },
        ],
        rows: [
          { name: 'select * from orders', runtime: 'sales', rows: 10, actions: [{ label: 'Details', action: 'detail' }] },
        ],
        columnSelector: {
          enabled: true,
          storageKey: 'record-table-test-columns',
          defaultColumns: ['name', 'runtime', 'rows'],
        },
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect((await columnSelectorState(page)).headers).toEqual(['Name', ''])

    await page.evaluate(() => localStorage.setItem('record-table-test-columns', JSON.stringify(['missing'])))
    await page.locator('lv-record-table').evaluate((element: any) => {
      element.table = {
        ...element.table,
        columns: [...element.table.columns, { id: 'request', header: 'Request', width: '120px' }],
      }
    })
    await page.locator('lv-record-table').evaluate((element: any) => element.updateComplete)
    expect((await columnSelectorState(page)).headers).toEqual(['Name', 'Runtime', 'Rows', '', 'Request'])
  } finally {
    await page.close()
  }
})

async function tableState(page: Page) {
  return page.locator('lv-record-table').evaluate((element) => {
    const rows = Array.from(element.querySelectorAll('tbody tr'))
    const firstNames = rows.map((row) => row.querySelector('.record-entity-label')?.textContent?.trim() ?? '')
    const table = element.querySelector('table') as HTMLTableElement
    const header = element.querySelector('th') as HTMLTableCellElement
    return {
      firstNames,
      hasEntityLink: Boolean(element.querySelector('a.record-entity-link[href="/orders"]')),
      hasStatusIcon: Boolean(element.querySelector('.record-status-icon svg')),
      hasCode: Boolean(element.querySelector('code.record-code')),
      hasExpression: Boolean(element.querySelector('code.record-expression')),
      badges: Array.from(element.querySelectorAll('.record-badge')).map((badge) => badge.textContent?.trim()),
      tags: Array.from(element.querySelectorAll('.record-tags span')).map((tag) => tag.textContent?.trim()),
      actionHref: element.querySelector<HTMLAnchorElement>('.record-icon-action')?.getAttribute('href'),
      minWidth: getComputedStyle(table).minWidth,
      headerPosition: getComputedStyle(header).position,
      hasSelector: Boolean(element.querySelector('.record-table-column-selector')),
    }
  })
}

async function leadingColumnState(page: Page) {
  return page.locator('lv-record-table').evaluate((element) => {
    const wrap = element.querySelector('.record-table-wrap') as HTMLElement
    const cell = element.querySelector('tbody td:first-child') as HTMLElement
    const link = element.querySelector('.record-entity-link') as HTMLElement
    return {
      wrapWidth: wrap.clientWidth,
      wrapScrollWidth: wrap.scrollWidth,
      wrapScrollLeft: wrap.scrollLeft,
      cellLeft: Math.round(cell.getBoundingClientRect().left),
      cellWidth: Math.round(cell.getBoundingClientRect().width),
      cellPosition: getComputedStyle(cell).position,
      linkLeft: Math.round(link.getBoundingClientRect().left),
      linkWidth: Math.round(link.getBoundingClientRect().width),
      documentOverflow: document.documentElement.scrollWidth - window.innerWidth,
    }
  })
}

async function rowActionState(page: Page) {
  return page.locator('lv-record-table').evaluate((element) => {
    const actions = (window as any).recordTableActions ?? []
    const last = actions[actions.length - 1] ?? {}
    return {
      count: actions.length,
      action: last.action ?? '',
      rowID: last.row?.id ?? '',
      expanded: Boolean(element.querySelector('.record-query-expanded-cell')),
    }
  })
}

async function queryRowState(page: Page) {
  return page.locator('lv-record-table').evaluate((element) => {
    const wrap = element.querySelector('.record-table-wrap') as HTMLElement
    const firstCell = element.querySelector('tbody tr:first-child td:first-child') as HTMLElement
    const firstRow = element.querySelector('tbody tr:first-child') as HTMLElement
    const expandedCell = element.querySelector('.record-query-expanded-cell') as HTMLTableCellElement | null
    const codeBlock = expandedCell?.querySelector('lv-code-block') as HTMLElement | null
    return {
      headers: Array.from(element.querySelectorAll('thead th')).map((header) => header.querySelector('.record-table-sort span:first-child')?.textContent?.trim() ?? ''),
      menuLabels: Array.from(element.querySelectorAll('.record-table-column-menu label')).map((label) => label.textContent?.trim() ?? ''),
      statusLabel: element.querySelector('.record-query-status')?.getAttribute('aria-label'),
      queryText: element.querySelector('.record-query-text')?.textContent?.trim(),
      hasExpandedRow: Boolean(expandedCell),
      expandedText: codeBlock?.shadowRoot?.querySelector('code')?.textContent ?? expandedCell?.textContent ?? '',
      hasCodeBlock: Boolean(codeBlock),
      formattedCode: codeBlock?.shadowRoot?.querySelector('code')?.textContent ?? codeBlock?.querySelector('code')?.textContent ?? '',
      expandedColspan: expandedCell?.colSpan ?? 0,
      wrapHasTightDensity: wrap.classList.contains('density-tight'),
      cellPaddingTop: getComputedStyle(firstCell).paddingTop,
      rowHeight: Math.round(firstRow.getBoundingClientRect().height),
    }
  })
}

async function columnSelectorState(page: Page) {
  return page.locator('lv-record-table').evaluate((element) => ({
    hasSelector: Boolean(element.querySelector('.record-table-column-selector')),
    selectorInCorner: Boolean(element.querySelector('.record-table-corner-selector .record-table-column-selector')),
    hasSeparateToolbar: Boolean(element.querySelector('.record-table-toolbar')),
    headers: Array.from(element.querySelectorAll('thead th')).map((header) => header.querySelector('.record-table-sort span:first-child')?.textContent?.trim() ?? ''),
    menuLabels: Array.from(element.querySelectorAll('.record-table-column-menu label')).map((label) => label.textContent?.trim() ?? ''),
    disabledChecks: Array.from(element.querySelectorAll<HTMLInputElement>('.record-table-column-menu input:disabled')).map((input) => input.closest('label')?.textContent?.trim() ?? ''),
    hasRuntimeCell: Array.from(element.querySelectorAll('tbody td')).some((cell) => cell.textContent?.trim() === 'sales'),
    hasActionsColumn: Boolean(element.querySelector('.record-icon-action')),
    storedColumns: JSON.parse(localStorage.getItem('record-table-test-columns') ?? '[]'),
  }))
}

function testDocument(): string {
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          body { ${typographyTestTokens} --fontStack-monospace: monospace; --lv-bg-page: #eef2f6; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-line-muted: #d8dee4; --lv-border-muted: 1px solid #d8dee4; --lv-border-transparent: 1px solid transparent; --lv-radius-default: 6px; --lv-radius-full: 999px; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --control-medium-size: 32px; }
        </style>
      </head>
      <body>
        <lv-record-table></lv-record-table>
        <script type="module" src="/record-table-under-test.js"></script>
      </body>
    </html>
  `
}
