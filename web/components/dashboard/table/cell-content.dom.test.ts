import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, expect as browserExpect, type Browser, type Page } from '@playwright/test'
import { testVisualizationEnvelopes } from '../dashboard-page-test-fixtures'
import type { TableHierarchy } from './types'

let server: Server
let browser: Browser
let baseURL = ''
const fixtureRoot = join(process.cwd(), '.tmp/visualization-host-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(`<!doctype html><html><body><script type="module">
        import '/visualization-host-under-test.js';
        const host = document.createElement('lv-visualization-host');
        host.id = 'media-host';
        host.style.cssText = 'display:block;width:700px;height:400px';
        host.envelope = ${JSON.stringify(testVisualizationEnvelopes().orders)};
        host.envelope.status = {kind:'ready'};
        document.body.append(host);
        await host.ensureMounted();
        const table = host.shadowRoot.querySelector('lv-report-table');
        const sort = {key:'id',direction:'asc'};
        table.table = {...table.table, availableRows:1, cardinality:{kind:'exact',value:1}, sort,
          interaction:{kind:'row_selection',mappings:[{field:'id',value:'id'}],targets:[]},
          columns:[
            {key:'label',label:'Part',role:'row_header'},
            {key:'preview',label:'Preview',role:'row_header',content:{kind:'image',display:'tooltip',altField:'label',width:160,height:100}},
            {key:'url',label:'Datasheet',role:'row_header',content:{kind:'link',labelField:'label',newTab:false}}
          ],
          blocks:{a:{start:0,requestSeq:0,resetVersion:0,sort,rows:[{id:'part-1',label:'Drive motor',preview:'/motor.svg',url:'/datasheet'}]}}
        };
        window.__mediaTable = table;
        window.__rowSelections = 0;
        table.addEventListener('lv-interaction-select', () => window.__rowSelections++);
        await table.updateComplete;
        window.__mediaReady = true;
      </script></body></html>`)
      return
    }
    if (url.pathname.endsWith('.svg')) {
      response.setHeader('content-type', 'image/svg+xml')
      response.end('<svg xmlns="http://www.w3.org/2000/svg" width="160" height="100"><rect width="160" height="100" fill="steelblue"/></svg>')
      return
    }
    const file = normalize(join(fixtureRoot, url.pathname))
    if (!file.startsWith(`${fixtureRoot}/`)) { response.writeHead(404).end(); return }
    try {
      response.setHeader('content-type', 'text/javascript')
      response.end(await readFile(file))
    } catch { response.writeHead(404).end() }
  })
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('media fixture did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()))
}, 15_000)

async function openFixture(): Promise<Page> {
  const page = await browser.newPage()
  await page.goto(baseURL)
  await page.waitForFunction(() => (window as any).__mediaReady)
  return page
}

test('image preview supports focus, Escape, and keyboard activation without selecting its row', async () => {
  const page = await openFixture()
  try {
    const trigger = page.getByRole('button', { name: 'View image: Drive motor', exact: true })
    const preview = page.locator('lv-table-cell-content .preview')
    await trigger.focus()
    await browserExpect(trigger).toHaveAttribute('aria-expanded', 'true')
    await browserExpect(preview).toBeVisible()
    await browserExpect(preview.getByRole('img', { name: 'Drive motor' })).toBeVisible()
    await page.keyboard.press('Escape')
    await browserExpect(trigger).toHaveAttribute('aria-expanded', 'false')
    await browserExpect(preview).toBeHidden()
    await browserExpect(trigger).toBeFocused()
    await page.keyboard.press('Enter')
    await browserExpect(preview).toBeVisible()
    await page.evaluate(() => {
      const table = (window as any).__mediaTable
      table.shadowRoot.querySelector('.table-scrollport').dispatchEvent(new Event('scroll'))
    })
    await browserExpect(preview).toBeHidden()
    expect(await page.evaluate(() => (window as any).__rowSelections)).toBe(0)
  } finally { await page.close() }
})

test('resetting a recycled media cell closes the old image preview and updates its accessible label', async () => {
  const page = await openFixture()
  try {
    await page.getByRole('button', { name: 'View image: Drive motor', exact: true }).focus()
    await browserExpect(page.locator('lv-table-cell-content .preview')).toBeVisible()
    await page.evaluate(async () => {
      const table = (window as any).__mediaTable
      const block = table.table.blocks.a
      table.table = {...table.table, resetVersion:1, blocks:{a:{...block,resetVersion:1,requestSeq:1,
        rows:[{...block.rows[0],label:'Replacement gearbox',preview:'/gearbox.svg'}]}}}
      await table.updateComplete
    })
    const trigger = page.getByRole('button', { name: 'View image: Replacement gearbox', exact: true })
    await browserExpect(trigger).toHaveAttribute('aria-expanded', 'false')
    await browserExpect(page.locator('lv-table-cell-content .preview')).toBeHidden()
    await trigger.press('Enter')
    await browserExpect(page.locator('lv-table-cell-content .preview img')).toHaveAttribute('src', `${baseURL}/gearbox.svg`)
    await browserExpect(page.locator('lv-table-cell-content .preview')).toBeVisible()
    expect(await page.evaluate(() => (window as any).__rowSelections)).toBe(0)
  } finally { await page.close() }
})

test('native cell links retain their destination without triggering row selection', async () => {
  const page = await openFixture()
  try {
    const link = page.getByRole('link', { name: 'Drive motor', exact: true })
    await browserExpect(link).toHaveAttribute('href', `${baseURL}/datasheet`)
    // Keep this page open while allowing click propagation to expose any row-selection regression.
    await link.evaluate(element => element.addEventListener('click', event => event.preventDefault()))
    await link.click()
    expect(await page.evaluate(() => (window as any).__rowSelections)).toBe(0)
    await page.getByRole('button', { name: 'Part: Drive motor', exact: true }).click()
    expect(await page.evaluate(() => (window as any).__rowSelections)).toBe(1)
  } finally { await page.close() }
})

test('table height grows to its allocation, shrinks with rows, and fits a new allocation after reconnect', async () => {
  const page = await openFixture()
  try {
    await page.evaluate(async () => {
      const source = (window as any).__mediaTable.table
      document.querySelector('#media-host')!.remove()
      const allocation = document.createElement('div')
      allocation.style.cssText = 'width:700px;height:500px'
      const table = document.createElement('lv-report-table') as any
      table.id = 'sizing-table'
      table.tableId = 'sizing'
      table.maxHeight = 300
      let resetVersion = 0
      ;(window as any).__setSizingRows = (count: number) => {
        resetVersion++
        table.table = {...source, id:'sizing', resetVersion, availableRows:count,
          cardinality:{kind:'exact',value:count}, columns:[{key:'id',label:'Record',role:'row_header'}],
          blocks:{a:{start:0,requestSeq:resetVersion,resetVersion,sort:source.sort,
            rows:Array.from({length:count}, (_, index) => ({id:`row-${index}`}))}}}
      }
      ;(window as any).__sizingEvents = []
      table.addEventListener('lv-table-size-change', (event: CustomEvent) => (window as any).__sizingEvents.push(event.detail))
      ;(window as any).__setSizingRows(1)
      allocation.append(table)
      document.body.append(allocation)
      await table.updateComplete
    })
    const table = page.locator('#sizing-table')
    const height = () => table.evaluate(element => element.getBoundingClientRect().height)
    await browserExpect.poll(height).toBeLessThan(200)
    const shortHeight = await height()
    expect(shortHeight).toBeGreaterThan(30)

    await page.evaluate(() => (window as any).__setSizingRows(100))
    await browserExpect.poll(height).toBe(300)
    expect(await table.locator('.table-scrollport').evaluate(element => element.scrollHeight > element.clientHeight)).toBe(true)

    await page.evaluate(() => (window as any).__setSizingRows(1))
    await browserExpect.poll(height).toBe(shortHeight)
    expect(await table.locator('.table-scrollport').evaluate(element => element.scrollHeight - element.clientHeight)).toBeLessThanOrEqual(1)

    await table.evaluate(async (element: any) => {
      element.remove()
      await Promise.resolve()
      const allocation = document.createElement('div')
      allocation.style.cssText = 'width:700px;height:220px'
      element.maxHeight = 220
      allocation.append(element)
      document.body.append(allocation)
      ;(window as any).__setSizingRows(100)
      await element.updateComplete
    })
    await browserExpect.poll(height).toBe(220)
    expect(await table.locator('.table-scrollport').evaluate(element => element.scrollHeight > element.clientHeight)).toBe(true)
    expect(await page.evaluate(() => (window as any).__sizingEvents.at(-1).height)).toBe(220)
  } finally { await page.close() }
})

for (const hierarchy of [
  { mode: 'levels', fields: ['label'], label: 'Part' },
  { mode: 'parent_child', idField: 'id', parentField: 'parent', labelField: 'label' },
  { mode: 'nested', childrenField: 'children', labelField: 'label', idField: 'id' },
] satisfies TableHierarchy[]) {
  test(`${hierarchy.mode} headers announce the active local sort across repeated keyboard changes`, async () => {
    const page = await openFixture()
    try {
      await page.evaluate(async (hierarchy) => {
        const table = (window as any).__mediaTable
        const sort = { key: 'amount', direction: 'asc' }
        const rows = [
          { id: 'a', label: 'Alpha', amount: 10 },
          { id: 'b', label: 'Bravo', amount: 20 },
        ]
        table.table = { ...table.table, type: 'matrix', hierarchy, sort,
          availableRows: rows.length, cardinality: { kind: 'exact', value: rows.length },
          columns: [{ key: 'label', label: 'Part', role: 'row_header' }, { key: 'amount', label: 'Amount', role: 'metric' }],
          blocks: { a: { start: 0, requestSeq: 0, resetVersion: 0, sort, rows } },
        }
        await table.updateComplete
      }, hierarchy)
      const amount = page.getByRole('columnheader').filter({ has: page.locator('[data-column-key="amount"]') })
      const part = page.getByRole('columnheader').filter({ has: page.locator('[data-column-key="__lv_hierarchy"]') })
      const labels = page.locator('lv-report-table .hierarchy-label')
      await browserExpect(amount).toHaveAttribute('aria-sort', 'ascending')
      for (const direction of ['descending', 'ascending', 'descending']) {
        await amount.getByRole('button', { name: 'Amount', exact: true }).press('Enter')
        await browserExpect(amount).toHaveAttribute('aria-sort', direction)
        await browserExpect(part).toHaveAttribute('aria-sort', 'none')
        await browserExpect.poll(() => labels.allTextContents()).toEqual(direction === 'ascending' ? ['Alpha', 'Bravo'] : ['Bravo', 'Alpha'])
      }
      for (const direction of ['ascending', 'descending', 'ascending']) {
        await part.getByRole('button', { name: 'Part', exact: true }).press('Space')
        await browserExpect(part).toHaveAttribute('aria-sort', direction)
        await browserExpect(amount).toHaveAttribute('aria-sort', 'none')
        await browserExpect.poll(() => labels.allTextContents()).toEqual(direction === 'ascending' ? ['Alpha', 'Bravo'] : ['Bravo', 'Alpha'])
      }
    } finally { await page.close() }
  })
}
