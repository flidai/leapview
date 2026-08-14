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
      await page.clock.install({ time: new Date('2026-08-12T12:00:00Z') })
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
          headers: Array.from(root.querySelectorAll('thead th')).map((header) => header.textContent?.trim()),
          workspaces: rows.map((row) => row.querySelectorAll('.entity-list-cell')[0]?.textContent?.trim()),
          refreshed: rows.map((row) => row.querySelectorAll('.entity-list-cell')[2]?.textContent?.trim()),
          refreshedTitles: rows.map((row) => row.querySelectorAll('.entity-list-cell')[2]?.getAttribute('title')),
          workspaceOptions: Array.from(root.querySelectorAll('.entity-filter option')).map((option) => ({
            id: (option as HTMLOptionElement).value,
            label: option.textContent?.trim(),
          })),
          listBackground: getComputedStyle(list).backgroundColor,
          hasIcons: rows.every((row) => Boolean(row.querySelector('.entity-list-icon svg'))),
          popularityLabels: rows.map((row) => row.querySelector('.entity-list-badge')?.getAttribute('aria-label') ?? ''),
          popularityLevels: rows.map((row) => row.querySelector('.entity-list-badge')?.classList.contains('is-high') ? 'high' : row.querySelector('.entity-list-badge')?.classList.contains('is-medium') ? 'medium' : row.querySelector('.entity-list-badge')?.classList.contains('is-low') ? 'low' : ''),
          emptyPopularityLabels: rows.map((row) => row.querySelector('.entity-list-badge-empty')?.getAttribute('aria-label') ?? ''),
          emptyPopularityOpacity: getComputedStyle(rows[3].querySelector('.entity-list-badge-empty') as HTMLElement).color,
          popularityColoredBars: rows.slice(0, 3).map((row) => {
            const paths = Array.from(row.querySelectorAll('.entity-list-badge path'))
            const mutedStroke = getComputedStyle(rows[2].querySelectorAll('.entity-list-badge path')[2]).stroke
            return paths.filter((path) => getComputedStyle(path).stroke !== mutedStroke).length
          }),
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
        rowCount: 4,
        hrefs: ['/dashboards/executive-sales', '/dashboards/operations-health', '/dashboards/inventory-risk', '/dashboards/customer-detail'],
        titles: ['Executive Sales Dashboard', 'Operations Health', 'Inventory Risk', 'Customer Detail'],
        descriptions: ['Fixture report', 'Fulfillment and delivery performance.', 'Stock exposure and replenishment.', 'Customer profile details.'],
        headers: ['Dashboard', 'Workspace', 'Popularity', 'Last refreshed'],
        workspaces: ['Sales', 'Operations', 'Operations', 'Customer Success'],
        refreshed: ['2 hr ago', '19 hr ago', '2 days ago', '—'],
        refreshedTitles: [expect.stringContaining('Aug 12, 2026'), expect.stringContaining('Aug 11, 2026'), expect.stringContaining('Aug 10, 2026'), ''],
        workspaceOptions: [
          { id: 'all', label: 'All workspaces' },
          { id: 'sales', label: 'Sales' },
          { id: 'operations', label: 'Operations' },
          { id: 'customer-success', label: 'Customer Success' },
        ],
        listBackground: 'rgb(238, 242, 246)',
        hasIcons: true,
        popularityLabels: ['High popularity — top 10% in the last 30 days', 'Medium popularity — top 20% in the last 30 days', 'Low popularity — top 30% in the last 30 days', ''],
        popularityLevels: ['high', 'medium', 'low', ''],
        emptyPopularityLabels: ['', '', '', 'No popularity data'],
        emptyPopularityOpacity: 'rgb(129, 139, 152)',
        popularityColoredBars: [3, 2, 1],
        hasChevrons: false,
        fullWidth: true,
        maxRowHeight: 52,
        totalListHeight: 240,
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

test('shared entity list sorts rows by popularity rank', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      const rows = () => Array.from(list.querySelectorAll('.entity-list-table-row')).map((row: Element) => row.querySelector('.entity-list-title')?.textContent?.trim())
      const popularityHeader = list.querySelector('button[aria-label="Sort by Popularity"]') as HTMLButtonElement
      const before = rows()
      popularityHeader.click()
      await list.updateComplete
      const ascending = rows()
      const ascendingSort = popularityHeader.closest('th')?.getAttribute('aria-sort')
      popularityHeader.click()
      await list.updateComplete
      return { before, ascending, ascendingSort, descending: rows(), descendingSort: popularityHeader.closest('th')?.getAttribute('aria-sort') }
    })

    expect(state.before).toEqual(['Executive Sales Dashboard', 'Operations Health', 'Inventory Risk', 'Customer Detail'])
    expect(state.ascending).toEqual(['Customer Detail', 'Inventory Risk', 'Operations Health', 'Executive Sales Dashboard'])
    expect(state.ascendingSort).toBe('ascending')
    expect(state.descending).toEqual(['Executive Sales Dashboard', 'Operations Health', 'Inventory Risk', 'Customer Detail'])
    expect(state.descendingSort).toBe('descending')
  } finally {
    await page.close()
  }
})

test('last refreshed sorting uses timestamps rather than relative labels', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.clock.install({ time: new Date('2026-08-12T12:00:00Z') })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      const rows = () => Array.from(list.querySelectorAll('.entity-list-table-row')).map((row: Element) => row.querySelector('.entity-list-title')?.textContent?.trim())
      const header = list.querySelector('button[aria-label="Sort by Last refreshed"]') as HTMLButtonElement
      header.click()
      await list.updateComplete
      const ascending = rows()
      header.click()
      await list.updateComplete
      return { ascending, descending: rows() }
    })

    expect(state.ascending).toEqual(['Inventory Risk', 'Operations Health', 'Executive Sales Dashboard', 'Customer Detail'])
    expect(state.descending).toEqual(['Executive Sales Dashboard', 'Operations Health', 'Inventory Risk', 'Customer Detail'])
  } finally {
    await page.close()
  }
})

test('CSV export uses displayed values instead of internal sort keys', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.clock.install({ time: new Date('2026-08-12T12:00:00Z') })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const csv = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      let exported: Blob | undefined
      URL.createObjectURL = (blob: Blob) => {
        exported = blob
        return 'blob:catalog-export'
      }
      URL.revokeObjectURL = () => {}
      HTMLAnchorElement.prototype.click = () => {}
      list.exportFilename = 'dashboards.csv'
      await list.updateComplete
      ;(list.querySelector('.entity-export') as HTMLButtonElement).click()
      return exported?.text()
    })

    expect(csv).toContain('"High"')
    expect(csv).toContain('"2 hr ago"')
    expect(csv).not.toContain('2026-08-12T09:42:00Z')
  } finally {
    await page.close()
  }
})

test('workspace dropdown emits the selected workspace filter', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const detail = await page.locator('lv-catalog-page').evaluate((element: any) => new Promise((resolve) => {
      element.addEventListener('lv-entity-list-query', (event: CustomEvent) => resolve(event.detail), { once: true })
      const select = element.shadowRoot.querySelector('.entity-filter') as HTMLSelectElement
      select.value = 'operations'
      select.dispatchEvent(new Event('change', { bubbles: true }))
    }))

    expect(detail).toEqual({ query: '', filter: 'operations' })
  } finally {
    await page.close()
  }
})

test('relative freshness labels update while the catalog remains open', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.clock.install({ time: new Date('2026-08-12T10:41:00Z') })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const freshness = () => page.locator('lv-catalog-page').evaluate((element: any) =>
      element.shadowRoot.querySelectorAll('.entity-list-cell')[2]?.textContent?.trim(),
    )

    expect(await freshness()).toBe('59 min ago')
    await page.clock.fastForward(60_000)
    expect(await freshness()).toBe('1 hr ago')
  } finally {
    await page.close()
  }
})

test('popularity meter uses Primer theme tokens in light and dark modes', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const colors = async () => page.locator('lv-catalog-page').evaluate((element: any) => {
      const root = element.shadowRoot
      const highPaths = Array.from(root.querySelectorAll('.entity-list-badge-popularity.is-high path')) as SVGPathElement[]
      const empty = root.querySelector('.entity-list-badge-empty') as HTMLElement
      return {
        bars: highPaths.map((path) => getComputedStyle(path).stroke),
        empty: getComputedStyle(empty).color,
      }
    })

    const light = await colors()
    await page.locator('body').evaluate((body) => body.setAttribute('data-color-mode', 'dark'))
    const dark = await colors()

    expect(light).toEqual({
      bars: ['rgb(31, 111, 235)', 'rgb(31, 111, 235)', 'rgb(31, 111, 235)'],
      empty: 'rgb(129, 139, 152)',
    })
    expect(dark).toEqual({
      bars: ['rgb(77, 160, 255)', 'rgb(77, 160, 255)', 'rgb(77, 160, 255)'],
      empty: 'rgb(101, 108, 118)',
    })
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
    workspaceFilters: [
      { id: 'sales', title: 'Sales' },
      { id: 'operations', title: 'Operations' },
      { id: 'customer-success', title: 'Customer Success' },
    ],
    dashboards: [
      {
        id: 'executive-sales',
        title: 'Executive Sales Dashboard',
        description: 'Fixture report',
        semanticModel: 'olist',
        pageCount: 1,
        tags: ['sales'],
        href: '/dashboards/executive-sales',
        popularity: 'high',
        workspace: 'Sales',
        lastRefreshedAt: '2026-08-12T09:42:00Z',
      },
      {
        id: 'operations-health',
        title: 'Operations Health',
        description: 'Fulfillment and delivery performance.',
        semanticModel: 'operations',
        pageCount: 3,
        tags: ['operations'],
        href: '/dashboards/operations-health',
        popularity: 'medium',
        workspace: 'Operations',
        lastRefreshedAt: '2026-08-11T16:20:00Z',
      },
      {
        id: 'inventory-risk',
        title: 'Inventory Risk',
        description: 'Stock exposure and replenishment.',
        semanticModel: 'inventory',
        pageCount: 2,
        tags: ['inventory'],
        href: '/dashboards/inventory-risk',
        popularity: 'low',
        workspace: 'Operations',
        lastRefreshedAt: '2026-08-10T11:05:00Z',
      },
      {
        id: 'customer-detail',
        title: 'Customer Detail',
        description: 'Customer profile details.',
        semanticModel: 'customers',
        pageCount: 1,
        tags: ['customers'],
        href: '/dashboards/customer-detail',
        workspace: 'Customer Success',
      },
    ],
  }
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-page: #eef2f6; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-line-muted: #d8dee4; --lv-line-accent: #0969da; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --lv-radius-full: 999px; --lv-page-content-max-width: 72rem; --lv-asset-dashboard-bg: #fbefff; --lv-asset-dashboard-accent: #8250df; --lv-asset-dashboard-border: #d2bfff; --fgColor-disabled: #818b98; --display-blue-fgColor: #1f6feb; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --borderWidth-default: 1px; --borderWidth-thick: 2px; --control-medium-size: 32px; --motion-transition-stateChange: 160ms ease; }
          body[data-color-mode='dark'] { --fgColor-disabled: #656c76; --display-blue-fgColor: #4da0ff; }
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
