import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/catalog-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : root
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
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

for (const viewport of [
  { name: 'compact desktop', width: 706, height: 793 },
  { name: 'mobile', width: 390, height: 820 },
]) {
  test(`catalog page renders compact full-width dashboard rows on ${viewport.name}`, async () => {
    const page = await browser.newPage({ viewport })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-catalog-page'))
      await page.locator('lv-catalog-page').evaluate((element: any) => element.updateComplete)

      const state = await page.locator('lv-catalog-page').evaluate((element: any) => {
        const root = element.shadowRoot
        const section = root.querySelector('section') as HTMLElement
        const list = root.querySelector('.entity-list-items') as HTMLElement
        const table = root.querySelector('.entity-list-table') as HTMLElement
        const rows = Array.from(root.querySelectorAll('tbody tr.entity-list-table-row')) as HTMLTableRowElement[]
        const sectionRect = section.getBoundingClientRect()
        const listRect = list.getBoundingClientRect()
        const tableRect = table.getBoundingClientRect()
        return {
          title: root.querySelector('h1')?.textContent?.trim(),
          rowCount: rows.length,
          hrefs: rows.map((row) => row.querySelector('.entity-list-identity')?.getAttribute('href')),
          titles: rows.map((row) => row.querySelector('.entity-list-title')?.textContent?.trim()),
          descriptions: rows.map((row) => row.querySelector('.entity-list-description')?.textContent?.trim()),
          pages: rows.map((row) => row.querySelectorAll('.entity-list-cell')[1]?.textContent?.trim()),
          listBackground: getComputedStyle(list).backgroundColor,
          hasIcons: rows.every((row) => Boolean(row.querySelector('.entity-list-icon svg'))),
          hasChevrons: rows.every((row) => Boolean(row.querySelector('.entity-list-chevron svg'))),
          fullWidth: rows.every((row) => Math.abs(row.getBoundingClientRect().width - tableRect.width) <= 1),
          maxRowHeight: Math.max(...rows.map((row) => Math.round(row.getBoundingClientRect().height))),
          totalListHeight: Math.round(listRect.height),
          hasCardGrid: Boolean(root.querySelector('.grid, article')),
          hasOpenLabel: rows.some((row) => row.textContent?.includes('Open')),
          sectionWidth: Math.round(sectionRect.width),
          centeredDelta: Math.round(Math.abs((sectionRect.left + sectionRect.width / 2) - window.innerWidth / 2)),
        }
      })

      expect(state).toEqual({
        title: 'Dashboards',
        rowCount: 2,
        hrefs: ['/dashboards/executive-sales', '/dashboards/operations-health'],
        titles: ['Executive Sales Dashboard', 'Operations Health'],
        descriptions: ['Fixture report', 'Fulfillment and delivery performance.'],
        pages: ['1 page', '3 pages'],
        listBackground: 'rgb(238, 242, 246)',
        hasIcons: true,
        hasChevrons: false,
        fullWidth: true,
        maxRowHeight: 52,
        totalListHeight: 136,
        hasCardGrid: false,
        hasOpenLabel: false,
        sectionWidth: Math.min(viewport.width, 1152),
        centeredDelta: 0,
      })
    } finally {
      await page.close()
    }
  })
}

test('shared entity list sorts rows from a column header', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      const rows = () => Array.from(list.querySelectorAll('.entity-list-table-row')).map((row: Element) => row.querySelector('.entity-list-title')?.textContent?.trim())
      const pagesHeader = list.querySelector('button[aria-label="Sort by Pages"]') as HTMLButtonElement
      const before = rows()
      pagesHeader.click()
      await list.updateComplete
      const ascending = rows()
      const ascendingSort = pagesHeader.closest('th')?.getAttribute('aria-sort')
      pagesHeader.click()
      await list.updateComplete
      return { before, ascending, ascendingSort, descending: rows(), descendingSort: pagesHeader.closest('th')?.getAttribute('aria-sort') }
    })

    expect(state.before).toEqual(['Executive Sales Dashboard', 'Operations Health'])
    expect(state.ascending).toEqual(['Executive Sales Dashboard', 'Operations Health'])
    expect(state.ascendingSort).toBe('ascending')
    expect(state.descending).toEqual(['Operations Health', 'Executive Sales Dashboard'])
    expect(state.descendingSort).toBe('descending')
  } finally {
    await page.close()
  }
})

test('catalog page explains an empty dashboard collection', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page') && customElements.get('lv-entity-list'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { ...element.page, dashboards: [] } })
      await element.updateComplete
      return {
        empty: element.shadowRoot.querySelector('[role="status"]')?.textContent?.trim(),
        cards: element.shadowRoot.querySelectorAll('article').length,
      }
    })

    expect(state.empty).toContain('No dashboards')
    expect(state.cards).toBe(0)
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  const page = {
    kind: 'catalog',
    title: 'Dashboards',
    description: 'Reports backed by semantic models.',
    dashboards: [
      {
        id: 'executive-sales',
        title: 'Executive Sales Dashboard',
        description: 'Fixture report',
        semanticModel: 'olist',
        pageCount: 1,
        tags: ['sales'],
        href: '/dashboards/executive-sales',
      },
      {
        id: 'operations-health',
        title: 'Operations Health',
        description: 'Fulfillment and delivery performance.',
        semanticModel: 'operations',
        pageCount: 3,
        tags: ['operations'],
        href: '/dashboards/operations-health',
      },
    ],
  }
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-page: #eef2f6; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-line-muted: #d8dee4; --lv-line-accent: #0969da; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --lv-radius-full: 999px; --lv-page-content-max-width: 72rem; --lv-asset-dashboard-bg: #fbefff; --lv-asset-dashboard-accent: #8250df; --lv-asset-dashboard-border: #d2bfff; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --borderWidth-default: 1px; --borderWidth-thick: 2px; --control-medium-size: 32px; --motion-transition-stateChange: 160ms ease; }
        </style>
      </head>
      <body>
        <main data-signals="${escapeHTML(JSON.stringify({ page }))}">
          <lv-catalog-page></lv-catalog-page>
        </main>
        <script type="module" src="/catalog-page-under-test.js"></script>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
      </body>
    </html>
  `
}

function escapeHTML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}
