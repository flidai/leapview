import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let baseURL = ''
let browser: Browser

const root = join(process.cwd(), '.tmp/windowed-table-test')

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

test('mobile windowed tables expose horizontal scrolling and a visible swipe hint', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 560 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-windowed-table'))
    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-windowed-table') as any
      element.table = {
        title: 'Customers',
        columns: [{ key: 'id', label: 'ID', width: 300 }, { key: 'notes', label: 'Notes', width: 500 }],
        totalRows: 1,
        availableRows: 1,
        blocks: { a: { start: 0, requestSeq: 0, resetVersion: 0, sort: {}, rows: [{ id: '1', notes: 'Ready' }] } },
      }
      document.body.append(element)
      await element.updateComplete
      const scrollport = element.shadowRoot.querySelector('.scrollport') as HTMLElement
      const hint = element.shadowRoot.querySelector('.scroll-hint') as HTMLElement
      return {
        role: scrollport.getAttribute('role'),
        label: scrollport.getAttribute('aria-label'),
        tabIndex: scrollport.getAttribute('tabindex'),
        hint: hint.textContent?.replace(/\s+/g, ' ').trim(),
        hintDisplay: getComputedStyle(hint).display,
        hintVisible: hint.getBoundingClientRect().height > 0,
      }
    })
    expect(state).toEqual({
      role: 'region', label: 'Scrollable Customers table', tabIndex: '0',
      hint: 'Swipe horizontally to see more columns →', hintDisplay: 'block', hintVisible: true,
    })
  } finally {
    await page.close()
  }
})

test('windowed table loads requested blocks and rejects stale payloads', async () => {
  const page = await browser.newPage({ viewport: { width: 960, height: 560 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-windowed-table'))

    const state = await page.evaluate(async () => {
      const makeBlock = (start: number, requestSeq: number, resetVersion: number, sort: Record<string, string>, rows: Array<Record<string, unknown>>) => ({ start, requestSeq, resetVersion, sort, rows })
      const makeRows = (start: number, count: number, prefix = 'row') => Array.from({ length: count }, (_, index) => ({
        id: `${prefix}-${start + index}`,
        state: index % 2 === 0 ? 'SP' : 'RJ',
        notes: `long value ${start + index}`,
      }))
      const makeTable = (overrides: Record<string, unknown>) => {
        const sort = { key: 'id', direction: 'asc' }
        return {
          title: 'Customers',
          columns: [
            { key: 'id', label: 'ID', type: 'VARCHAR', width: 180 },
            { key: 'state', label: 'State', type: 'VARCHAR', width: 120 },
            { key: 'notes', label: 'Notes', type: 'VARCHAR', width: 260 },
          ],
          totalRows: 200,
          availableRows: 200,
          chunkSize: 50,
          rowHeight: 34,
          resetVersion: 0,
          sort,
          blocks: {
            a: makeBlock(0, 0, 0, sort, []),
            b: makeBlock(50, 0, 0, sort, []),
            c: makeBlock(100, 0, 0, sort, []),
          },
          loadingBlock: '',
          error: '',
          visibleColumns: [],
          ...overrides,
        }
      }
      const element = document.createElement('lv-windowed-table') as any
      const sort = { key: 'id', direction: 'asc' }
      element.table = makeTable({
        sort,
        blocks: {
          a: makeBlock(0, 0, 0, sort, makeRows(0, 5)),
          b: makeBlock(50, 0, 0, sort, makeRows(50, 5)),
          c: makeBlock(100, 0, 0, sort, makeRows(100, 5)),
        },
      })
      const requests: any[] = []
      element.addEventListener('lv-windowed-table-request', (event: CustomEvent) => requests.push(event.detail))
      document.body.append(element)
      await element.updateComplete

      const root = element.shadowRoot!
      const scrollport = root.querySelector('.scrollport') as HTMLDivElement
      scrollport.scrollTop = 5200
      scrollport.dispatchEvent(new Event('scroll'))
      await new Promise((resolve) => setTimeout(resolve, 120))
      const firstRequest = requests.find((request) => request.start >= 100)
      const blockStarts = firstRequest.block === 'all'
        ? [Math.max(0, firstRequest.start - 50), firstRequest.start, firstRequest.start + 50]
        : [0, 50, 100]
      const responseBlocks = (requestSeq: number, prefix: string) => firstRequest.block === 'all'
        ? {
          a: makeBlock(blockStarts[0], requestSeq, 0, sort, makeRows(blockStarts[0], 50, prefix)),
          b: makeBlock(blockStarts[1], requestSeq, 0, sort, makeRows(blockStarts[1], 50, prefix)),
          c: makeBlock(blockStarts[2], requestSeq, 0, sort, makeRows(blockStarts[2], 50, prefix)),
        }
        : {
          a: makeBlock(0, 0, 0, sort, makeRows(0, 5)),
          b: makeBlock(50, 0, 0, sort, makeRows(50, 5)),
          c: makeBlock(100, 0, 0, sort, makeRows(100, 5)),
          [firstRequest.block]: makeBlock(firstRequest.start, requestSeq, 0, sort, makeRows(firstRequest.start, 5, prefix)),
        }

      element.table = makeTable({
        sort,
        blocks: responseBlocks(firstRequest.requestSeq - 1, 'stale'),
      })
      await element.updateComplete
      const staleText = root.textContent ?? ''

      element.table = makeTable({
        sort,
        blocks: responseBlocks(firstRequest.requestSeq, 'loaded'),
      })
      await element.updateComplete

      const rows = Array.from(root.querySelectorAll('.row[role="row"]')).map((row) => ({
        busy: row.getAttribute('aria-busy'),
        text: row.textContent?.replace(/\s+/g, ' ').trim(),
      }))
      return {
        firstRequest,
        staleAccepted: staleText.includes('stale'),
        loadedRows: rows.filter((row) => row.text?.includes('loaded')).length,
        skeletonRows: rows.filter((row) => row.busy === 'true').length,
      }
    })

    expect(state.firstRequest.block).toBeTruthy()
    expect(state.firstRequest.count).toBe(50)
    expect(state.staleAccepted).toBe(false)
    expect(state.loadedRows).toBeGreaterThan(0)
    expect(state.skeletonRows).toBeLessThan(10)
  } finally {
    await page.close()
  }
})

test('windowed table resizes columns and emits width state', async () => {
  const page = await browser.newPage({ viewport: { width: 960, height: 560 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-windowed-table'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-windowed-table') as any
      const sort = { key: 'id', direction: 'asc' }
      element.table = {
        title: 'Customers',
        columns: [
          { key: 'id', label: 'ID', type: 'VARCHAR', width: 180, minWidth: 120 },
          { key: 'state', label: 'State', type: 'VARCHAR', width: 120 },
          { key: 'notes', label: 'Notes', type: 'VARCHAR', width: 260 },
        ],
        totalRows: 10,
        availableRows: 10,
        chunkSize: 5,
        rowHeight: 34,
        resetVersion: 0,
        sort,
        blocks: {
          a: { start: 0, requestSeq: 0, resetVersion: 0, sort, rows: [{ id: 'customer-1', state: 'SP', notes: 'first row' }] },
          b: { start: 5, requestSeq: 0, resetVersion: 0, sort, rows: [] },
          c: { start: 10, requestSeq: 0, resetVersion: 0, sort, rows: [] },
        },
        visibleColumns: [],
        columnWidths: {},
      }
      const widthEvents: any[] = []
      element.addEventListener('lv-windowed-table-column-widths', (event: CustomEvent) => widthEvents.push(event.detail))
      document.body.append(element)
      await element.updateComplete

      const root = element.shadowRoot!
      const firstHeader = root.querySelector('.header-cell') as HTMLElement
      const firstWidthBefore = Math.round(firstHeader.getBoundingClientRect().width)
      const planeWidthBefore = Math.round((root.querySelector('.plane') as HTMLElement).getBoundingClientRect().width)
      const resizer = root.querySelector('.column-resizer') as HTMLElement
      resizer.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, clientX: 180 }))
      document.dispatchEvent(new MouseEvent('mousemove', { bubbles: true, clientX: 430 }))
      await new Promise((resolve) => requestAnimationFrame(resolve))
      document.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, clientX: 430 }))
      await element.updateComplete

      element.table = {
        ...element.table,
        blocks: {
          ...element.table.blocks,
          a: { start: 0, requestSeq: 1, resetVersion: 0, sort, rows: [{ id: 'customer-2', state: 'RJ', notes: 'new block row' }] },
        },
      }
      await element.updateComplete

      root.querySelector('.options summary')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      const stateCheckbox = Array.from(root.querySelectorAll<HTMLInputElement>('.menu input'))[1]
      stateCheckbox.checked = false
      stateCheckbox.dispatchEvent(new Event('change', { bubbles: true }))
      await element.updateComplete
      stateCheckbox.checked = true
      stateCheckbox.dispatchEvent(new Event('change', { bubbles: true }))
      await element.updateComplete

      const firstWidthAfter = Math.round((root.querySelector('.header-cell') as HTMLElement).getBoundingClientRect().width)
      const planeWidthAfter = Math.round((root.querySelector('.plane') as HTMLElement).getBoundingClientRect().width)
      return {
        firstWidthBefore,
        firstWidthAfter,
        planeWidthBefore,
        planeWidthAfter,
        widthEvents,
        guideCleared: !root.querySelector('.resize-guide'),
        text: root.textContent,
      }
    })

    expect(state.firstWidthBefore).toBeGreaterThan(180)
    expect(state.firstWidthAfter).toBeGreaterThanOrEqual(420)
    expect(state.planeWidthAfter).toBeGreaterThan(state.planeWidthBefore)
    expect(state.widthEvents.at(-1)?.columnWidths?.id).toBeGreaterThanOrEqual(420)
    expect(state.guideCleared).toBe(true)
    expect(state.text).toContain('customer-2')
  } finally {
    await page.close()
  }
})

test('windowed table clears cached rows when table key changes without reset version change', async () => {
  const page = await browser.newPage({ viewport: { width: 960, height: 560 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-windowed-table'))

    const state = await page.evaluate(async () => {
      const sort = { key: 'id', direction: 'asc' }
      const element = document.createElement('lv-windowed-table') as any
      const makeTable = (tableKey: string, rows: Array<Record<string, unknown>>) => ({
        tableKey,
        title: 'Rows',
        columns: [
          { key: 'id', label: 'ID', type: 'VARCHAR', width: 180 },
          { key: 'state', label: 'State', type: 'VARCHAR', width: 120 },
        ],
        totalRows: 10,
        availableRows: 10,
        chunkSize: 5,
        rowHeight: 34,
        resetVersion: 0,
        sort,
        blocks: {
          a: { start: 0, requestSeq: 0, resetVersion: 0, sort, rows },
          b: { start: 5, requestSeq: 0, resetVersion: 0, sort, rows: [] },
          c: { start: 10, requestSeq: 0, resetVersion: 0, sort, rows: [] },
        },
        visibleColumns: [],
        columnWidths: {},
      })

      element.table = makeTable('project:customers', [{ id: 'customer-1', state: 'SP' }])
      document.body.append(element)
      await element.updateComplete
      const before = element.shadowRoot!.textContent ?? ''

      element.table = makeTable('project:orders', [])
      await element.updateComplete
      const after = element.shadowRoot!.textContent ?? ''
      const skeletonRows = element.shadowRoot!.querySelectorAll('.row[aria-busy="true"]').length

      return {
        beforeHadCustomer: before.includes('customer-1'),
        afterHasCustomer: after.includes('customer-1'),
        skeletonRows,
      }
    })

    expect(state.beforeHadCustomer).toBe(true)
    expect(state.afterHasCustomer).toBe(false)
    expect(state.skeletonRows).toBeGreaterThan(0)
  } finally {
    await page.close()
  }
})

function testDocument() {
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { font-family: Inter, system-ui, sans-serif; }
          lv-windowed-table { display: grid; width: 900px; height: 420px; min-height: 0; }
        </style>
      </head>
      <body>
        <script type="module" src="/windowed-table-under-test.js"></script>
      </body>
    </html>
  `
}
