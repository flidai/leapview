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

test('catalog introduces authoring as New dashboard', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const action = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      element.setAttribute('create-draft-href', '/dashboards/new')
      await element.updateComplete
      const trigger = element.shadowRoot.querySelector('.catalog-create-draft') as HTMLAnchorElement
      return { label: trigger?.textContent?.trim(), tagName: trigger?.tagName, href: trigger?.getAttribute('href') }
    })
    expect(action).toEqual({ label: 'New dashboard', tagName: 'A', href: '/dashboards/new' })
  } finally {
    await page.close()
  }
})

test('new dashboard trigger opens an accessible native dialog with the create form contract', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      element.setAttribute('create-draft-href', '/dashboards/new')
      element.setAttribute('create-draft-models', JSON.stringify([
        { id: 'semantic:sales_overview', title: 'sales_overview' },
        { id: 'semantic:Kpis', title: 'Sales KPIs' },
      ]))
      element.setAttribute('create-draft-csrf-token', 'csrf-modal')
      element.setAttribute('create-draft-idempotency-key', 'idem-modal')
      await element.updateComplete
      const trigger = element.shadowRoot.querySelector('.catalog-create-draft') as HTMLAnchorElement
      trigger.click()
      await element.updateComplete
      await new Promise<void>((resolve) => setTimeout(resolve, 10))
      const root = element.shadowRoot
      const dialog = root.querySelector('dialog') as HTMLDialogElement
      const form = root.querySelector('.catalog-create-dialog-form') as HTMLFormElement
      return {
        tagName: trigger.tagName,
        href: trigger.getAttribute('href'),
        hasDialogHint: trigger.getAttribute('aria-haspopup'),
        controls: trigger.getAttribute('aria-controls'),
        open: dialog.open,
        labelledBy: dialog.getAttribute('aria-labelledby'),
        title: root.querySelector('#catalog-create-draft-title')?.textContent?.trim(),
		activeField: root.activeElement?.id ?? '',
        method: form.method,
        action: new URL(form.action).pathname,
        nameRequired: root.querySelector<HTMLInputElement>('[name="title"]')?.required,
        modelOptions: Array.from(root.querySelectorAll('#catalog-create-draft-model option')).map((option) => ({ id: option.getAttribute('value'), title: option.textContent?.trim() })),
        selectedModel: root.querySelector<HTMLSelectElement>('#catalog-create-draft-model')?.value,
        csrf: root.querySelector<HTMLInputElement>('[name="gorilla.csrf.Token"]')?.value,
        idempotency: root.querySelector<HTMLInputElement>('[name="idempotencyKey"]')?.value,
        advanced: root.querySelector('summary')?.textContent?.trim(),
      }
    })

    expect(state).toEqual({
      tagName: 'A',
      href: '/dashboards/new',
      hasDialogHint: 'dialog',
      controls: 'catalog-create-draft-dialog',
      open: true,
      labelledBy: 'catalog-create-draft-title',
      title: 'New dashboard',
      activeField: 'catalog-create-draft-name',
      method: 'post',
      action: '/dashboards/new',
      nameRequired: true,
      modelOptions: [
        { id: '', title: 'Select a data model' },
        { id: 'semantic:sales_overview', title: 'Sales Overview' },
        { id: 'semantic:Kpis', title: 'Sales KPIs' },
      ],
      selectedModel: '',
      csrf: 'csrf-modal',
      idempotency: 'idem-modal',
      advanced: 'Advanced settings',
    })
  } finally {
    await page.close()
  }
})

test('create query auto-opens with the semantic model preselected and dismissal restores focus', async () => {
  const page = await browser.newPage({ viewport: { width: 960, height: 700 } })
  try {
    await page.goto(`${baseURL}/?create=dashboard&semanticModel=semantic%3Asales_overview`)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      element.setAttribute('create-draft-href', '/dashboards/new')
      element.setAttribute('create-draft-models', JSON.stringify([{ id: 'semantic:sales_overview', title: 'sales_overview' }]))
      await element.updateComplete
      await element.updateComplete
      await new Promise<void>((resolve) => queueMicrotask(resolve))
      const root = element.shadowRoot
      const dialog = root.querySelector('dialog') as HTMLDialogElement
      const select = root.querySelector('select') as HTMLSelectElement
      const autoOpened = dialog.open
      const close = root.querySelector('.catalog-create-dialog-close') as HTMLButtonElement
      close.click()
      await element.updateComplete
      await new Promise<void>((resolve) => queueMicrotask(resolve))
      return {
        autoOpened,
        selectedModel: select.value,
        closed: !dialog.open,
        restoredFocus: root.activeElement?.classList.contains('catalog-create-draft') ?? false,
        query: window.location.search,
      }
    })

    expect(state).toEqual({
      autoOpened: true,
      selectedModel: 'semantic:sales_overview',
      closed: true,
      restoredFocus: false,
      query: '?create=dashboard&semanticModel=semantic%3Asales_overview',
    })
  } finally {
    await page.close()
  }
})

test('native Escape and backdrop dismissal close the create dialog', async () => {
  const page = await browser.newPage({ viewport: { width: 960, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      element.setAttribute('create-draft-href', '/dashboards/new')
      element.setAttribute('create-draft-models', JSON.stringify([{ id: 'sales', title: 'Sales' }]))
      await element.updateComplete
      const root = element.shadowRoot
      const trigger = root.querySelector('.catalog-create-draft') as HTMLAnchorElement
      const dialog = () => root.querySelector('dialog') as HTMLDialogElement
      trigger.click()
      await element.updateComplete
      const escapedOpen = dialog().open
      dialog().dispatchEvent(new Event('cancel', { bubbles: true, cancelable: true }))
      await element.updateComplete
      await new Promise<void>((resolve) => queueMicrotask(resolve))
      const closedAfterEscape = !dialog().open
      const focusAfterEscape = root.activeElement === trigger
      trigger.click()
      await element.updateComplete
      dialog().dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await element.updateComplete
      await new Promise<void>((resolve) => queueMicrotask(resolve))
      return { escapedOpen, closedAfterEscape, focusAfterEscape, closedAfterBackdrop: !dialog().open, focusAfterBackdrop: root.activeElement === trigger }
    })

    expect(state).toEqual({ escapedOpen: true, closedAfterEscape: true, focusAfterEscape: true, closedAfterBackdrop: true, focusAfterBackdrop: true })
  } finally {
    await page.close()
  }
})

test('mobile create dialog stays within the viewport and disables submission without models', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      element.setAttribute('create-draft-href', '/dashboards/new')
      element.setAttribute('create-draft-models', '[]')
      await element.updateComplete
      const root = element.shadowRoot
      const trigger = root.querySelector('.catalog-create-draft') as HTMLAnchorElement
      trigger.click()
      await element.updateComplete
      const dialog = root.querySelector('dialog') as HTMLDialogElement
      const rect = dialog.getBoundingClientRect()
      const select = root.querySelector('select') as HTMLSelectElement
      const submit = root.querySelector('[type="submit"]') as HTMLButtonElement
      return {
        withinViewport: rect.left >= 0 && rect.right <= window.innerWidth && rect.top >= 0 && rect.bottom <= window.innerHeight,
        selectDisabled: select.disabled,
        submitDisabled: submit.disabled,
        help: root.querySelector('#catalog-create-draft-model-help')?.textContent?.trim(),
      }
    })

    expect(state).toEqual({
      withinViewport: true,
      selectDisabled: true,
      submitDisabled: true,
      help: 'No data models are available. Add one in Develop, then try again.',
    })
  } finally {
    await page.close()
  }
})

test('create form submits natively to the builder with hidden request values', async () => {
  const page = await browser.newPage({ viewport: { width: 960, height: 700 } })
  let postData = ''
  try {
    await page.route('**/dashboards/new', async (route) => {
      postData = route.request().postData() ?? ''
      await route.fulfill({ status: 200, contentType: 'text/html', body: 'builder' })
    })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      element.setAttribute('create-draft-href', '/dashboards/new')
      element.setAttribute('create-draft-models', JSON.stringify([{ id: 'sales', title: 'Sales' }]))
      element.setAttribute('create-draft-csrf-token', 'csrf-submit')
      element.setAttribute('create-draft-idempotency-key', 'idem-submit')
      await element.updateComplete
      const root = element.shadowRoot
      ;(root.querySelector('.catalog-create-draft') as HTMLAnchorElement).click()
      await element.updateComplete
      ;(root.querySelector('[name="title"]') as HTMLInputElement).value = 'Sales overview'
      ;(root.querySelector('[name="semanticModel"]') as HTMLSelectElement).value = 'sales'
      ;(root.querySelector('.catalog-create-dialog-form') as HTMLFormElement).requestSubmit()
    })
    await page.waitForTimeout(100)
    expect(postData).toContain('title=Sales+overview')
    expect(postData).toContain('semanticModel=sales')
    expect(postData).toContain('gorilla.csrf.Token=csrf-submit')
    expect(postData).toContain('idempotencyKey=idem-submit')
  } finally {
    await page.close()
  }
})

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
          owners: rows.map((row) => row.querySelectorAll('.entity-list-cell')[0]?.textContent?.trim()),
          statuses: rows.map((row) => row.querySelectorAll('.entity-list-cell')[1]?.textContent?.trim()),
          updated: rows.map((row) => row.querySelectorAll('.entity-list-cell')[2]?.textContent?.trim()),
          updatedTitles: rows.map((row) => row.querySelectorAll('.entity-list-cell')[2]?.getAttribute('title')),
          listBackground: getComputedStyle(list).backgroundColor,
          hasIcons: rows.every((row) => Boolean(row.querySelector('.entity-list-icon svg'))),
          popularityLabels: rows.map((row) => row.querySelector('.entity-list-badge')?.getAttribute('aria-label') ?? ''),
          popularityLevels: rows.map((row) => row.querySelector('.entity-list-badge')?.classList.contains('is-high') ? 'high' : row.querySelector('.entity-list-badge')?.classList.contains('is-medium') ? 'medium' : row.querySelector('.entity-list-badge')?.classList.contains('is-low') ? 'low' : ''),
          popularityColoredBars: rows.slice(0, 3).map((row) => {
            const paths = Array.from(row.querySelectorAll('.entity-list-badge path'))
            const mutedStroke = getComputedStyle(rows[2].querySelectorAll('.entity-list-badge path')[2]).stroke
            return paths.filter((path) => getComputedStyle(path).stroke !== mutedStroke).length
          }),
          iconsAreFramed: rows.every((row) => row.querySelector('.entity-list-icon')?.classList.contains('is-framed')),
          framedIconBorderWidth: getComputedStyle(rows[0].querySelector('.entity-list-icon') as HTMLElement).borderTopWidth,
          framedIconBackground: getComputedStyle(rows[0].querySelector('.entity-list-icon') as HTMLElement).backgroundColor,
          originBadges: rows.filter((row) => row.querySelector('.entity-list-label-badge')).length,
          tabs: Array.from(root.querySelectorAll('.catalog-tab')).map((tab) => tab.textContent?.trim()),
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
        headers: ['Dashboard', 'Owner', 'Status', 'Updated'],
        owners: ['Analytics', 'Operations', 'Supply chain', '—'],
        statuses: ['Published', 'Published', 'Published', 'Published'],
        updated: ['2 hr ago', '19 hr ago', '2 days ago', '—'],
        updatedTitles: [expect.stringContaining('Aug 12, 2026'), expect.stringContaining('Aug 11, 2026'), expect.stringContaining('Aug 10, 2026'), ''],
        listBackground: 'rgb(238, 242, 246)',
        hasIcons: true,
        popularityLabels: ['High popularity — top 10% in the last 30 days', 'Medium popularity — top 20% in the last 30 days', 'Low popularity — top 30% in the last 30 days', ''],
        popularityLevels: ['high', 'medium', 'low', ''],
        popularityColoredBars: [3, 2, 1],
        iconsAreFramed: true,
        framedIconBorderWidth: '1px',
        framedIconBackground: 'rgb(251, 239, 255)',
        originBadges: 0,
        tabs: ['All dashboards', 'My dashboards', 'Shared with me'],
        hasChevrons: false,
        fullWidth: true,
        maxRowHeight: 52,
        totalListHeight: viewport.name === 'mobile' ? 259 : 240,
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

test('dashboard tabs filter by ownership without hiding managed dashboards from All', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const dashboards = element.page.dashboards.map((dashboard: any, index: number) => ({
        ...dashboard,
        catalogScope: index === 0 ? 'mine' : index === 1 ? 'shared' : 'managed',
      }))
      mergePatch({ page: { ...element.page, dashboards } })
      await element.updateComplete
      const rows = () => Array.from(element.shadowRoot.querySelectorAll('.entity-list-title')).map((row: Element) => row.textContent?.trim())
      const all = rows()
      ;(element.shadowRoot.querySelector('.catalog-tab:nth-child(2)') as HTMLButtonElement).click()
      await element.updateComplete
      const mine = rows()
      ;(element.shadowRoot.querySelector('.catalog-tab:nth-child(3)') as HTMLButtonElement).click()
      await element.updateComplete
      return { all, mine, shared: rows() }
    })

    expect(state.all).toEqual(['Executive Sales Dashboard', 'Operations Health', 'Inventory Risk', 'Customer Detail'])
    expect(state.mine).toEqual(['Executive Sales Dashboard'])
    expect(state.shared).toEqual(['Operations Health'])
  } finally {
    await page.close()
  }
})

test('dashboard lifecycle statuses use distinct semantic icons and tones', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const statuses = ['published', 'private_draft', 'unpublished_changes', 'published']
      mergePatch({ page: { ...element.page, dashboards: element.page.dashboards.map((dashboard: any, index: number) => ({ ...dashboard, status: statuses[index] })) } })
      await element.updateComplete
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
      return Array.from(list.querySelectorAll('.entity-list-status')).map((status: Element) => ({
        className: status.className,
        label: status.textContent?.trim(),
        icon: status.querySelector('.entity-list-status-icon')?.innerHTML,
      }))
    })

    expect(state.map(({ label, className }) => ({ label, className }))).toEqual([
      { label: 'Published', className: 'entity-list-status is-success' },
      { label: 'Private draft', className: 'entity-list-status is-muted' },
      { label: 'Unpublished changes', className: 'entity-list-status is-attention' },
      { label: 'Published', className: 'entity-list-status is-success' },
    ])
    expect(state[1].icon).toContain('cx="12" cy="16" r="1"')
    expect(state[2].icon).toContain('M14.364 13.634')
    expect(state[0].icon).not.toEqual(state[1].icon)
    expect(state[1].icon).not.toEqual(state[2].icon)
  } finally {
    await page.close()
  }
})

test('updated sorting uses timestamps rather than relative labels', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.clock.install({ time: new Date('2026-08-12T12:00:00Z') })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      const rows = () => Array.from(list.querySelectorAll('.entity-list-table-row')).map((row: Element) => row.querySelector('.entity-list-title')?.textContent?.trim())
      const header = list.querySelector('button[aria-label="Sort by Updated"]') as HTMLButtonElement
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

    expect(csv).toContain('"Analytics"')
    expect(csv).toContain('"Published"')
    expect(csv).toContain('"2 hr ago"')
    expect(csv).not.toContain('2026-08-12T09:42:00Z')
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
      return {
        bars: highPaths.map((path) => getComputedStyle(path).stroke),
      }
    })

    const light = await colors()
    await page.locator('body').evaluate((body) => body.setAttribute('data-color-mode', 'dark'))
    const dark = await colors()

    expect(light).toEqual({
      bars: ['rgb(31, 111, 235)', 'rgb(31, 111, 235)', 'rgb(31, 111, 235)'],
    })
    expect(dark).toEqual({
      bars: ['rgb(77, 160, 255)', 'rgb(77, 160, 255)', 'rgb(77, 160, 255)'],
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

test('dashboard list displays persisted appearance without editing controls', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page') && customElements.get('lv-entity-list'))
    const catalog = page.locator('lv-catalog-page')
    const state = await catalog.evaluate(async (element: any) => {
      await element.updateComplete
      const list = element.shadowRoot!.querySelector('lv-entity-list') as any
      await list.updateComplete
      return {
        color: list.items[0].iconColor,
        hasIcon: Boolean(list.items[0].iconNode),
        iconButtonLabel: list.items[0].iconButtonLabel,
        picker: Boolean(element.shadowRoot!.querySelector('lv-dashboard-icon-picker')),
        customizeButton: Boolean(list.querySelector('button[aria-label^="Customize"]')),
      }
    })
    expect(state.color).toBe('purple')
    expect(state.hasIcon).toBe(true)
    expect(state.iconButtonLabel).toBeUndefined()
    expect(state.picker).toBe(false)
    expect(state.customizeButton).toBe(false)
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
        dashboardId: 'executive-sales',
        appearanceIcon: 'chart-no-axes-combined',
        appearanceColor: 'purple',
        catalogScope: 'managed',
        status: 'published',
        owner: 'Analytics',
        title: 'Executive Sales Dashboard',
        description: 'Fixture report',
        semanticModel: 'olist',
        pageCount: 1,
        tags: ['sales'],
        href: '/dashboards/executive-sales',
        popularity: 'high',
        lastRefreshedAt: '2026-08-12T09:42:00Z',
      },
      {
        id: 'operations-health',
        dashboardId: 'operations-health',
        appearanceIcon: 'package-check',
        appearanceColor: 'orange',
        catalogScope: 'managed',
        status: 'published',
        owner: 'Operations',
        title: 'Operations Health',
        description: 'Fulfillment and delivery performance.',
        semanticModel: 'operations',
        pageCount: 3,
        tags: ['operations'],
        href: '/dashboards/operations-health',
        popularity: 'medium',
        lastRefreshedAt: '2026-08-11T16:20:00Z',
      },
      {
        id: 'inventory-risk',
        dashboardId: 'inventory-risk',
        appearanceIcon: 'layout-dashboard',
        appearanceColor: 'purple',
        catalogScope: 'managed',
        status: 'published',
        owner: 'Supply chain',
        title: 'Inventory Risk',
        description: 'Stock exposure and replenishment.',
        semanticModel: 'inventory',
        pageCount: 2,
        tags: ['inventory'],
        href: '/dashboards/inventory-risk',
        popularity: 'low',
        lastRefreshedAt: '2026-08-10T11:05:00Z',
      },
      {
        id: 'customer-detail',
        dashboardId: 'customer-detail',
        appearanceIcon: 'layout-dashboard',
        appearanceColor: 'purple',
        catalogScope: 'managed',
        status: 'published',
        title: 'Customer Detail',
        description: 'Customer profile details.',
        semanticModel: 'customers',
        pageCount: 1,
        tags: ['customers'],
        href: '/dashboards/customer-detail',
      },
    ],
  }
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-page: #eef2f6; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-line-muted: #d8dee4; --lv-line-accent: #0969da; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --lv-radius-full: 999px; --lv-page-content-max-width: 72rem; --lv-asset-dashboard-bg: #fbefff; --lv-asset-dashboard-accent: #8250df; --lv-asset-dashboard-border: #d2bfff; --fgColor-disabled: #818b98; --display-blue-fgColor: #1f6feb; --display-purple-fgColor: #783ae4; --display-orange-fgColor: #a24610; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --borderWidth-default: 1px; --borderWidth-thick: 2px; --control-medium-size: 32px; --motion-transition-stateChange: 160ms ease; }
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
