import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, expect as browserExpect, type Browser } from '@playwright/test'

let server: Server
let browser: Browser
let baseURL = ''

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/report-canvas-test')

beforeAll(async () => {
  await Bun.$`rm -rf ${root}`.quiet()
  const built = await Bun.build({
    entrypoints: [
      join(projectRoot, 'web/components/dashboard/report-canvas.ts'),
      join(projectRoot, 'web/components/dashboard/table/report-table.ts'),
      join(projectRoot, 'web/components/dashboard/filters/url-sync.ts'),
    ],
    target: 'browser',
    format: 'esm',
    outdir: root,
    naming: { entry: '[name].js' },
  })
  if (!built.success) throw new Error('failed to build report canvas test bundles')

  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/report-canvas.js' || url.pathname === '/report-table.js' || url.pathname === '/url-sync.js') {
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
      return
    }
    response.setHeader('content-type', 'text/html')
    response.end(testDocument())
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('report canvas test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('report canvas restores route-scoped view preferences through URL sync history', async () => {
  const page = await browser.newPage({ viewport: { width: 1000, height: 700 } })
  try {
    await page.addInitScript(() => {
      localStorage.setItem('leapview-report-layout:/report-a', 'desktop')
      localStorage.setItem('leapview-report-zoom:/report-a', 'custom')
      localStorage.setItem('leapview-report-zoom-scale:/report-a', '1.5')
      localStorage.setItem('leapview-report-layout:/report-b', 'auto')
      localStorage.setItem('leapview-report-zoom:/report-b', 'fit-page')
      localStorage.setItem('leapview-report-zoom-scale:/report-b', '0.4')
    })
    await page.goto(`${baseURL}/report-a`)
    await page.waitForFunction(() => customElements.get('lv-report-canvas') && Boolean((window as any).DatastarURLSync))

    const waitForState = async (pathname: string, layoutMode: string, layout: string, mode: string, scale: number) => {
      await page.waitForFunction(({ pathname: expectedPathname, layoutMode: expectedLayoutMode, layout: expectedLayout, mode: expectedMode, scale: expectedScale }) => {
        const canvas = document.querySelector('lv-report-canvas') as any
        const surface = document.querySelector('lv-report-canvas')?.shadowRoot?.querySelector('.surface') as HTMLElement | null
        return canvas?.layoutMode === expectedLayoutMode
          && window.location.pathname === expectedPathname
          && surface?.dataset.layout === expectedLayout
          && surface?.dataset.presentationMode === expectedMode
          && Math.abs(Number(surface?.dataset.scale) - expectedScale) < 0.001
      }, { pathname, layoutMode, layout, mode, scale })
    }

    await waitForState('/report-a', 'desktop', 'desktop', 'custom', 1.5)

    await page.evaluate(() => {
      const urlSync = (window as any).DatastarURLSync
      if (!urlSync) throw new Error('Datastar URL sync was not installed')
      urlSync.push({}, '/report-b')
    })
    await waitForState('/report-b', 'auto', 'desktop', 'fit-page', 0.725)

    await page.goBack({ waitUntil: 'load' })
    await waitForState('/report-a', 'desktop', 'desktop', 'custom', 1.5)

    await page.goForward({ waitUntil: 'load' })
    await waitForState('/report-b', 'auto', 'desktop', 'fit-page', 0.725)
  } finally {
    await page.close()
  }
}, 20_000)

function testDocument(): string {
  return `<!doctype html><html><head><style>html,body{width:100%;height:100%;margin:0;}lv-report-canvas{display:block;width:100%;height:100%;--lv-report-canvas-bg:#eef1f4;--lv-report-page-bg:#fff;--lv-scrollbar-thumb:#8c959f;--lv-scrollbar-thumb-hover:#6e7781;}</style></head><body><lv-report-canvas width="1366" height="768"></lv-report-canvas><script type="module" src="/report-canvas.js"></script><script type="module" src="/report-table.js"></script><script type="module" src="/url-sync.js"></script></body></html>`
}

test('mobile cards use compact chart heights and content-sized table heights', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => document.querySelector('lv-report-canvas')?.getAttribute('data-layout') === 'mobile')
    await page.waitForFunction(() => customElements.get('lv-report-table'))
    await page.locator('lv-report-canvas').evaluate(canvas => {
      for (const kind of ['cartesian', 'table', 'matrix', 'pivot']) {
        const card = document.createElement('div')
        card.setAttribute('data-canvas-visual', '')
        card.dataset.componentKind = 'visual'
        card.dataset.visualType = kind
        if (kind !== 'cartesian') {
          // Mobile table height is an allocation cap; actual rows supply the
          // card's intrinsic height through the real renderer's sizing loop.
          card.style.setProperty('--lv-mobile-table-height', '254px')
          const table = document.createElement('lv-report-table') as any
          const sort = {key:'id', direction:'asc'}
          table.table = {...table.table, id:kind, type:kind, title:kind,
            columns:[{key:'id',label:'Record',role:'row_header'}], availableRows:100,
            cardinality:{kind:'exact',value:100}, sort,
            blocks:{...table.table.blocks, a:{start:0, requestSeq:0, resetVersion:0, sort,
              rows:Array.from({length:100}, (_, index) => ({id:`row-${index}`}))}}}
          card.append(table)
        }
        canvas.append(card)
      }
    })
    expect(await page.locator('[data-visual-type="cartesian"]').evaluate(e => e.getBoundingClientRect().height)).toBe(360)
    const tableCards = page.locator('[data-visual-type="table"], [data-visual-type="matrix"], [data-visual-type="pivot"]')
    const heights = () => tableCards.evaluateAll(cards => cards.map(card => card.getBoundingClientRect().height))
    await browserExpect.poll(heights).toEqual([254,254,254])
    expect(await tableCards.locator('.table-scrollport').evaluateAll(viewports => viewports.every(viewport => viewport.scrollHeight > viewport.clientHeight))).toBe(true)

    const setRows = async (count: number) => {
      await tableCards.locator('lv-report-table').evaluateAll((tables, count) => {
        for (const element of tables) {
          const table = element as any
          const resetVersion = table.table.resetVersion + 1
          table.table = {...table.table, resetVersion, availableRows:count, cardinality:{kind:'exact',value:count},
            blocks:{...table.table.blocks, a:{start:0, requestSeq:resetVersion, resetVersion, sort:table.table.sort,
              rows:Array.from({length:count}, (_, index) => ({id:`row-${index}`}))}}}
        }
      }, count)
    }
    await setRows(1)
    await browserExpect.poll(async () => (await heights()).every(height => height > 0 && height < 254)).toBe(true)
    const shortHeights = await heights()
    expect(await tableCards.locator('.table-scrollport').evaluateAll(viewports => viewports.every(viewport => viewport.scrollHeight - viewport.clientHeight <= 1))).toBe(true)
    await setRows(100)
    await browserExpect.poll(heights).toEqual([254,254,254])
    await setRows(1)
    await browserExpect.poll(heights).toEqual(shortHeights)
  } finally { await page.close() }
})
