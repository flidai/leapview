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

test('catalog introduces authoring as an explicit New dashboard action with a creation icon', async () => {
  const page = await browser.newPage({ viewport: { width: 900, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const action = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      element.setAttribute('create-draft-href', '/dashboards/new')
      await element.updateComplete
      const trigger = element.shadowRoot.querySelector('.catalog-create-draft') as HTMLAnchorElement
      return { label: trigger?.textContent?.trim(), tagName: trigger?.tagName, href: trigger?.getAttribute('href'), hasIcon: Boolean(trigger?.querySelector('svg')) }
    })
    expect(action).toEqual({ label: 'New dashboard', tagName: 'A', href: '/dashboards/new', hasIcon: true })
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
      const shell = root.querySelector('.catalog-create-dialog-shell') as HTMLElement
      const close = root.querySelector('.catalog-create-dialog-close') as HTMLButtonElement
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
        advanced: dialog.querySelector('summary')?.textContent?.trim(),
        closeHasSVG: Boolean(close.querySelector('svg')),
        closeText: close.textContent?.trim(),
        noHorizontalOverflow: shell.scrollWidth <= shell.clientWidth,
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
      closeHasSVG: true,
      closeText: '',
      noHorizontalOverflow: true,
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
      const select = root.querySelector('#catalog-create-draft-model') as HTMLSelectElement
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
      const select = root.querySelector('#catalog-create-draft-model') as HTMLSelectElement
      const submit = root.querySelector('[type="submit"]') as HTMLButtonElement
      return {
        withinViewport: rect.left >= 0 && rect.right <= window.innerWidth && rect.top >= 0 && rect.bottom <= window.innerHeight,
        noHorizontalOverflow: dialog.scrollWidth <= dialog.clientWidth,
        selectDisabled: select.disabled,
        submitDisabled: submit.disabled,
        help: root.querySelector('#catalog-create-draft-model-help')?.textContent?.trim(),
      }
    })

    expect(state).toEqual({
      withinViewport: true,
      noHorizontalOverflow: true,
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
          descriptionCount: rows.filter((row) => row.querySelector('.entity-list-description')).length,
          headers: Array.from(root.querySelectorAll('thead th')).map((header) => header.textContent?.trim()),
          dataModels: rows.map((row) => row.querySelectorAll('.entity-list-cell')[0]?.textContent?.trim()),
          owners: rows.map((row) => row.querySelectorAll('.entity-list-cell')[1]?.querySelector('.entity-list-person-avatar')?.getAttribute('aria-label') ?? '—'),
          ownerAvatars: rows.map((row) => Boolean(row.querySelectorAll('.entity-list-cell')[1]?.querySelector('lv-user-avatar'))),
          statuses: rows.map((row) => row.querySelectorAll('.entity-list-cell')[3]?.textContent?.trim()),
          updated: rows.map((row) => row.querySelectorAll('.entity-list-cell')[4]?.querySelector('.entity-list-datetime-value')?.textContent?.trim() ?? '—'),
          updatedTitles: rows.map((row) => row.querySelectorAll('.entity-list-cell')[4]?.querySelector('.entity-list-datetime')?.getAttribute('aria-label') ?? ''),
          lastOpened: rows.map((row) => row.querySelectorAll('.entity-list-cell')[5]?.querySelector('.entity-list-datetime-value')?.textContent?.trim() ?? '—'),
          listBackground: getComputedStyle(list).backgroundColor,
          hasIcons: rows.every((row) => Boolean(row.querySelector('.entity-list-icon svg'))),
          popularityLabels: rows.map((row) => row.querySelector('.entity-list-popularity')?.getAttribute('aria-label') ?? ''),
          popularityLevels: rows.map((row) => row.querySelector('.entity-list-popularity')?.classList.contains('is-high') ? 'high' : row.querySelector('.entity-list-popularity')?.classList.contains('is-medium') ? 'medium' : row.querySelector('.entity-list-popularity')?.classList.contains('is-low') ? 'low' : ''),
          popularityColoredBars: rows.slice(0, 3).map((row) => {
            const paths = Array.from(row.querySelectorAll('.entity-list-popularity path'))
            const mutedStroke = getComputedStyle(rows[2].querySelectorAll('.entity-list-popularity path')[2]).stroke
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
          actionLabels: rows.map((row) => row.querySelector('.entity-list-row-action')?.getAttribute('aria-label')),
        }
      })

      expect(state).toEqual({
        title: 'Dashboards',
        rowCount: 4,
        hrefs: ['/dashboards/executive-sales', '/dashboards/operations-health', '/dashboards/inventory-risk', '/dashboards/customer-detail'],
        titles: ['Executive Sales Dashboard', 'Operations Health', 'Inventory Risk', 'Customer Detail'],
        descriptionCount: 0,
        headers: ['Dashboard', 'Data model', 'Owner', 'Popularity', 'Status', 'Updated', 'Last opened', 'Actions'],
        dataModels: ['Olist', 'Operations', 'Inventory', 'Customers'],
        owners: ['Analytics', 'Operations', 'Supply chain', '—'],
        statuses: ['Published', 'Published', 'Published', 'Published'],
        updated: ['Aug 12', 'Aug 11', 'Aug 10', '—'],
        updatedTitles: [expect.stringContaining('Aug 12, 2026'), expect.stringContaining('Aug 11, 2026'), expect.stringContaining('Aug 10, 2026'), ''],
        lastOpened: ['—', '—', '—', '—'],
        ownerAvatars: [true, true, true, false],
        listBackground: 'rgb(238, 242, 246)',
        hasIcons: true,
        popularityLabels: ['High popularity — top 10% in the last 30 days', 'Medium popularity — top 20% in the last 30 days', 'Low popularity — top 30% in the last 30 days', 'No popularity data yet'],
        popularityLevels: ['high', 'medium', 'low', ''],
        popularityColoredBars: [3, 2, 1],
        iconsAreFramed: true,
        framedIconBorderWidth: '1px',
        framedIconBackground: 'rgb(251, 239, 255)',
        originBadges: 0,
        tabs: ['All dashboards', 'Favorites', 'My dashboards'],
        hasChevrons: false,
        fullWidth: true,
        maxRowHeight: 52,
        totalListHeight: viewport.name === 'mobile' ? 259 : 240,
        hasCardGrid: false,
        hasOpenLabel: false,
        sectionWidth: Math.min(viewport.width, 1152),
        centeredDelta: 0,
        actionLabels: [
          'More actions for Executive Sales Dashboard',
          'More actions for Operations Health',
          'More actions for Inventory Risk',
          'More actions for Customer Detail',
        ],
      })
    } finally {
      await page.close()
    }
  })
}

test('dashboard tabs expose favorites and owned dashboards without hiding either from All', async () => {
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
      localStorage.setItem('leapview.dashboard-catalog.favorites.v1', JSON.stringify(['operations-health', 'inventory-risk']))
      element.reloadDiscoveryPreferences()
      await element.updateComplete
      const rows = () => Array.from(element.shadowRoot.querySelectorAll('.entity-list-title')).map((row: Element) => row.textContent?.trim())
      const all = rows()
      ;(element.shadowRoot.querySelector('.catalog-tab:nth-child(2)') as HTMLButtonElement).click()
      await element.updateComplete
      const favorites = rows()
      ;(element.shadowRoot.querySelector('.catalog-tab:nth-child(3)') as HTMLButtonElement).click()
      await element.updateComplete
      return { all, favorites, mine: rows() }
    })

    expect(state.all).toEqual(['Operations Health', 'Inventory Risk', 'Executive Sales Dashboard', 'Customer Detail'])
    expect(state.favorites).toEqual(['Operations Health', 'Inventory Risk'])
    expect(state.mine).toEqual(['Executive Sales Dashboard'])
  } finally {
    await page.close()
  }
})

test('dashboard overflow actions open a permission-aware menu and details drawer', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        ...element.page,
        dashboards: element.page.dashboards.map((dashboard: any, index: number) => ({
          ...dashboard,
          catalogScope: index === 0 ? 'mine' : dashboard.catalogScope,
          href: index === 0 ? '/dashboards/executive-sales/preview?draft=draft-one&page=overview&revisionId=revision-one&revisionNumber=1&revisionContentHash=sha256%3Aone' : dashboard.href,
        })),
      } })
      await element.updateComplete
      const root = element.shadowRoot
      const list = root.querySelector('lv-entity-list') as any
      await list.updateComplete
      const trigger = list.querySelector('.entity-list-row-action') as HTMLButtonElement
      trigger.click()
      await element.updateComplete
      const menu = root.querySelector('[role="menu"]') as HTMLElement
      const menuLabels = Array.from(menu.querySelectorAll('[role="menuitem"]')).map((item: Element) => item.textContent?.trim())
      const rowHref = list.querySelector('.entity-list-identity')?.getAttribute('href')
      const editHref = menu.querySelector('a[role="menuitem"]')?.getAttribute('href')
      ;(menu.querySelector('[data-action="details"]') as HTMLButtonElement).click()
      await element.updateComplete
      const drawer = root.querySelector('.catalog-details-drawer') as HTMLElement
      const details = Array.from(drawer.querySelectorAll('dt, dd')).map((item: Element) => item.textContent?.trim())
      const title = drawer.querySelector('h2')?.textContent?.trim()
      const description = drawer.querySelector('.catalog-details-description')?.textContent?.trim()
      ;(drawer.querySelector('.catalog-details-close') as HTMLButtonElement).click()
      await element.updateComplete
      await new Promise<void>((resolve) => queueMicrotask(resolve))
      return {
        menuLabels,
        rowHref,
        editHref,
        title,
        description,
        details,
        drawerClosed: !root.querySelector('.catalog-details-drawer'),
        focusRestored: root.activeElement === trigger,
      }
    })

    expect(state.menuLabels).toEqual(['Edit dashboard', 'View details', 'Copy link', 'Archive'])
    expect(state.rowHref).toBe('/dashboards/executive-sales/preview?draft=draft-one&page=overview&revisionId=revision-one&revisionNumber=1&revisionContentHash=sha256%3Aone')
    expect(state.editHref).toBe('/dashboards/executive-sales/edit?draft=draft-one')
    expect(state.title).toBe('Executive Sales Dashboard')
    expect(state.description).toBe('Fixture report')
    expect(state.details).toEqual(expect.arrayContaining(['Data model', 'Olist model', 'Owner', 'Analytics', 'Status', 'Published', 'Pages', '1']))
    expect(state.drawerClosed).toBe(true)
    expect(state.focusRestored).toBe(true)
  } finally {
    await page.close()
  }
})

test('managed dashboard menu offers an editable copy without edit or archive actions', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const menu = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      await element.updateComplete
      const root = element.shadowRoot
      const list = root.querySelector('lv-entity-list') as any
      await list.updateComplete
      ;(list.querySelector('.entity-list-row-action') as HTMLButtonElement).click()
      await element.updateComplete
      return Array.from(root.querySelectorAll('[role="menuitem"]')).map((item: Element) => ({
        label: item.textContent?.trim(),
        href: item.getAttribute('href'),
      }))
    })

    expect(menu).toEqual([
      { label: 'Make an editable copy', href: '/dashboards/executive-sales/fork' },
      { label: 'View details', href: null },
      { label: 'Copy link', href: null },
    ])
  } finally {
    await page.close()
  }
})

test('My dashboards removes the redundant owner column', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const headers = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        ...element.page,
        dashboards: element.page.dashboards.map((dashboard: any) => ({ ...dashboard, catalogScope: 'mine', owner: 'You' })),
      } })
      await element.updateComplete
      ;(element.shadowRoot.querySelector('.catalog-tab:nth-child(3)') as HTMLButtonElement).click()
      await element.updateComplete
      return Array.from(element.shadowRoot.querySelectorAll('thead th')).map((header: Element) => header.textContent?.trim())
    })
    expect(headers).toEqual(['Dashboard', 'Data model', 'Popularity', 'Status', 'Updated', 'Last opened', 'Actions'])
  } finally {
    await page.close()
  }
})

test('data model is a dedicated sortable column instead of dashboard subtitle metadata', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
      const rows = Array.from(list.querySelectorAll('tbody tr.entity-list-table-row')) as HTMLTableRowElement[]
      return {
        headers: Array.from(list.querySelectorAll('thead th')).map((header: Element) => header.textContent?.trim()),
        models: rows.map((row) => row.querySelectorAll('.entity-list-cell')[0]?.textContent?.trim()),
        subtitles: rows.map((row) => row.querySelector('.entity-list-meta')?.textContent?.trim() ?? ''),
        sortable: Boolean(list.querySelector('button[aria-label="Sort by Data model"]')),
      }
    })

    expect(state).toEqual({
      headers: ['Dashboard', 'Data model', 'Owner', 'Popularity', 'Status', 'Updated', 'Last opened', 'Actions'],
      models: ['Olist', 'Operations', 'Inventory', 'Customers'],
      subtitles: ['', '', '', ''],
      sortable: true,
    })
  } finally {
    await page.close()
  }
})

test('owned dashboards use the signed-in display name and avatar instead of You', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const owner = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        ...element.page,
        dashboards: element.page.dashboards.map((dashboard: any, index: number) => index === 0
          ? { ...dashboard, catalogScope: 'mine', owner: 'You' }
          : dashboard),
      } })
      await element.updateComplete
      const cell = element.shadowRoot.querySelectorAll('.entity-list-cell')[1] as HTMLElement
      const avatar = cell.querySelector('lv-user-avatar') as any
      return {
        text: cell.textContent?.trim(),
        label: cell.querySelector('.entity-list-person-avatar')?.getAttribute('aria-label'),
        avatarName: avatar?.name,
        avatarURL: avatar?.imageUrl,
        title: cell.getAttribute('title'),
      }
    })

    expect(owner).toEqual({
      text: 'Jacob Nielsen',
      label: 'Jacob Nielsen',
      avatarName: 'Jacob Nielsen',
      avatarURL: '/profile/avatars/jacob/avatar-digest',
      title: '',
    })
  } finally {
    await page.close()
  }
})

test('dashboard owners render as compact accessible avatars with hover labels', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
    })
    await page.locator('lv-catalog-page').locator('lv-entity-list').locator('.entity-list-person-avatar').first().hover()
    const state = await page.locator('lv-catalog-page').evaluate((element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      const ownerCell = list.querySelector('tbody tr.entity-list-table-row')?.querySelectorAll('.entity-list-cell')[1] as HTMLElement
      const owner = ownerCell.querySelector('.entity-list-person-avatar') as HTMLElement
      const tooltip = owner?.querySelector('.entity-list-hover-tooltip') as HTMLElement
      return {
        cellTitle: ownerCell.title,
        label: owner?.getAttribute('aria-label'),
        hasAvatar: Boolean(owner?.querySelector('lv-user-avatar')),
        hasVisibleName: Boolean(ownerCell.querySelector('.entity-list-person-name')),
        hovered: owner?.matches(':hover'),
        tooltipText: tooltip?.textContent?.trim(),
        tooltipVisibleOnHover: getComputedStyle(tooltip).visibility,
      }
    })

    expect(state).toEqual({
      cellTitle: '',
      label: 'Analytics',
      hasAvatar: true,
      hasVisibleName: false,
      hovered: true,
      tooltipText: 'Analytics',
      tooltipVisibleOnHover: 'visible',
    })
  } finally {
    await page.close()
  }
})

test('dashboard titles use regular emphasis and popularity has a dedicated hoverable column', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
    })
    await page.locator('lv-catalog-page').locator('lv-entity-list').locator('.entity-list-popularity').first().hover()
    const state = await page.locator('lv-catalog-page').evaluate((element: any) => {
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      const rows = Array.from(list.querySelectorAll('tbody tr.entity-list-table-row')) as HTMLTableRowElement[]
      const firstPopularity = rows[0].querySelector('.entity-list-popularity') as HTMLElement
      const missingPopularity = rows[3].querySelector('.entity-list-popularity') as HTMLElement
      return {
        headers: Array.from(list.querySelectorAll('thead th')).map((header: Element) => header.textContent?.trim()),
        titleWeight: getComputedStyle(rows[0].querySelector('.entity-list-title') as HTMLElement).fontWeight,
        firstLabel: firstPopularity?.getAttribute('aria-label'),
        firstTooltip: firstPopularity?.querySelector('.entity-list-hover-tooltip')?.textContent?.trim(),
        firstTooltipVisibility: getComputedStyle(firstPopularity?.querySelector('.entity-list-hover-tooltip') as HTMLElement).visibility,
        missingLabel: missingPopularity?.getAttribute('aria-label'),
      }
    })

    expect(state).toEqual({
      headers: ['Dashboard', 'Data model', 'Owner', 'Popularity', 'Status', 'Updated', 'Last opened', 'Actions'],
      titleWeight: '400',
      firstLabel: 'High popularity — top 10% in the last 30 days',
      firstTooltip: 'High popularity — top 10% in the last 30 days',
      firstTooltipVisibility: 'visible',
      missingLabel: 'No popularity data yet',
    })
  } finally {
    await page.close()
  }
})

test('catalog discovery keeps source, sorting, and filters in one compact toolbar', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        ...element.page,
        dashboards: element.page.dashboards.map((dashboard: any, index: number) => ({
          ...dashboard,
          featured: index === 1,
          status: index === 3 ? 'private_draft' : dashboard.status,
        })),
      } })
      await element.updateComplete
      const root = element.shadowRoot
      const list = root.querySelector('lv-entity-list') as any
      await list.updateComplete
      const filter = root.querySelector('.catalog-filter') as HTMLDetailsElement
      filter.open = true
      await element.updateComplete
      const model = root.querySelector('[aria-label="Filter by data model"]') as HTMLSelectElement
      model.value = 'operations'
      model.dispatchEvent(new Event('change', { bubbles: true }))
      await element.updateComplete
      await list.updateComplete
      return {
        controls: Array.from(root.querySelectorAll('.catalog-discovery-control')).map((control: Element) => control.getAttribute('aria-label') ?? control.textContent?.trim()),
        models: Array.from(model.options).map((option) => option.textContent?.trim()),
        visible: Array.from(list.querySelectorAll('.entity-list-title')).map((title: Element) => title.textContent?.trim()),
        activeFilters: root.querySelector('.catalog-filter-count')?.textContent?.trim(),
        dataModels: Array.from(list.querySelectorAll('.entity-list-cell:first-of-type')).map((model: Element) => model.textContent?.trim()),
        featured: Array.from(list.querySelectorAll('.entity-list-badge-featured')).map((badge: Element) => badge.textContent?.trim()),
        popularity: Array.from(list.querySelectorAll('.entity-list-popularity')).map((badge: Element) => badge.getAttribute('aria-label')),
        hasRedundantFavoritesFilter: Array.from(root.querySelectorAll('.catalog-filter-check')).some((label: Element) => label.textContent?.includes('Favorites only')),
      }
    })

    expect(state).toEqual({
      controls: ['Filter dashboards', 'Sort dashboards'],
      models: ['All data models', 'Customers', 'Inventory', 'Olist', 'Operations'],
      visible: ['Operations Health'],
      activeFilters: '1',
      dataModels: ['Operations'],
      featured: ['Featured'],
      popularity: ['Medium popularity — top 20% in the last 30 days'],
      hasRedundantFavoritesFilter: false,
    })
  } finally {
    await page.close()
  }
})

test('dashboard favorites persist and rank first while recently viewed remains a sort choice', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      localStorage.clear()
      element.reloadDiscoveryPreferences()
      await element.updateComplete
      let list = element.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
      const favorites = () => Array.from(list.querySelectorAll('.entity-list-favorite')).map((button: Element) => button.getAttribute('aria-label'))
      const titles = () => Array.from(list.querySelectorAll('.entity-list-title')).map((title: Element) => title.textContent?.trim())
      const favoriteLabels = favorites()
      ;(list.querySelectorAll('.entity-list-favorite')[2] as HTMLButtonElement).click()
      await element.updateComplete
      list = element.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
      const ranked = titles()
      const pressed = (list.querySelector('.entity-list-favorite') as HTMLButtonElement).getAttribute('aria-pressed')
      const storedFavorites = JSON.parse(localStorage.getItem('leapview.dashboard-catalog.favorites.v1') ?? '[]')
      const operations = list.querySelector('a[data-item-id="operations-health"]') as HTMLAnchorElement
      operations.addEventListener('click', (event) => event.preventDefault(), { once: true })
      operations.click()
      const sort = element.shadowRoot.querySelector('[aria-label="Sort dashboards"]') as HTMLSelectElement
      sort.value = 'recent'
      sort.dispatchEvent(new Event('change', { bubbles: true }))
      await element.updateComplete
      await list.updateComplete
      return { favoriteLabels, ranked, pressed, storedFavorites, recent: titles() }
    })

    expect(state.favoriteLabels).toContain('Add Inventory Risk to favorites')
    expect(state.ranked[0]).toBe('Inventory Risk')
    expect(state.pressed).toBe('true')
    expect(state.storedFavorites).toContain('inventory-risk')
    expect(state.recent[0]).toBe('Operations Health')
  } finally {
    await page.close()
  }
})

test('date columns reveal exact localized date and time on hover and focus', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.clock.install({ time: new Date('2026-08-12T12:00:00Z') })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const state = await page.locator('lv-catalog-page').evaluate(async (element: any) => {
      localStorage.setItem('leapview.dashboard-catalog.recents.v1', JSON.stringify({
        'executive-sales': '2026-08-12T08:15:00Z',
        'operations-health': '2025-12-31T19:30:00Z',
      }))
      element.reloadDiscoveryPreferences()
      await element.updateComplete
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
      return Array.from(list.querySelectorAll('tbody tr')).map((row: Element) => ({
        title: row.querySelector('.entity-list-title')?.textContent?.trim(),
        updated: row.querySelector('.entity-list-datetime[data-column="updated"] .entity-list-datetime-value')?.textContent?.trim(),
        updatedLabel: row.querySelector('.entity-list-datetime[data-column="updated"]')?.getAttribute('aria-label'),
        lastOpened: row.querySelector('.entity-list-datetime[data-column="lastOpened"] .entity-list-datetime-value')?.textContent?.trim(),
        lastOpenedLabel: row.querySelector('.entity-list-datetime[data-column="lastOpened"]')?.getAttribute('aria-label'),
      }))
    })

    expect(state[0]).toEqual({
      title: 'Executive Sales Dashboard',
      updated: 'Aug 12',
      updatedLabel: expect.stringContaining('Updated: Aug 12, 2026, 9:42 AM'),
      lastOpened: 'Aug 12',
      lastOpenedLabel: expect.stringContaining('Last opened: Aug 12, 2026, 8:15 AM'),
    })
    expect(state[1]).toEqual({
      title: 'Operations Health',
      updated: 'Aug 11',
      updatedLabel: expect.stringContaining('Updated: Aug 11, 2026'),
      lastOpened: 'Dec 31, 2025',
      lastOpenedLabel: expect.stringContaining('Last opened: Dec 31, 2025'),
    })
    expect(state[2].lastOpened).toBeUndefined()

    await page.locator('lv-catalog-page').locator('lv-entity-list').locator('.entity-list-datetime[data-column="updated"]').first().hover()
    const visibility = await page.locator('lv-catalog-page').locator('lv-entity-list').locator('.entity-list-datetime[data-column="updated"] .entity-list-hover-tooltip').first().evaluate((tooltip) => getComputedStyle(tooltip).visibility)
    expect(visibility).toBe('visible')
    await page.mouse.move(0, 0)
    await page.locator('lv-catalog-page').locator('lv-entity-list').locator('.entity-list-datetime[data-column="lastOpened"]').first().focus()
    const focusVisibility = await page.locator('lv-catalog-page').locator('lv-entity-list').locator('.entity-list-datetime[data-column="lastOpened"] .entity-list-hover-tooltip').first().evaluate((tooltip) => getComputedStyle(tooltip).visibility)
    expect(focusVisibility).toBe('visible')
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
        iconWidth: Math.round(status.querySelector('.entity-list-status-icon svg')?.getBoundingClientRect().width ?? 0),
        fontWeight: getComputedStyle(status).fontWeight,
        title: status.closest('td')?.getAttribute('title'),
      }))
    })

    expect(state.map(({ label, className }) => ({ label, className }))).toEqual([
      { label: 'Published', className: 'entity-list-status is-success is-quiet' },
      { label: 'Draft', className: 'entity-list-status is-muted is-quiet' },
      { label: 'Changes pending', className: 'entity-list-status is-attention is-quiet' },
      { label: 'Published', className: 'entity-list-status is-success is-quiet' },
    ])
    expect(state.every(({ fontWeight }) => fontWeight === '400')).toBe(true)
    expect(state.every(({ iconWidth }) => iconWidth === 14)).toBe(true)
    expect(state[1].title).toContain('Private draft')
    expect(state[2].title).toContain('Unpublished changes')
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

test('CSV export uses compact displayed dates instead of internal sort keys', async () => {
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
    expect(csv).toContain('"Aug 12"')
    expect(csv).not.toContain('2026-08-12T09:42:00Z')
  } finally {
    await page.close()
  }
})

test('updated dates include the year when it differs from the current year', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.clock.install({ time: new Date('2027-01-02T10:41:00Z') })
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-catalog-page'))
    const updated = () => page.locator('lv-catalog-page').evaluate((element: any) =>
      element.shadowRoot.querySelector('.entity-list-datetime[data-column="updated"] .entity-list-datetime-value')?.textContent?.trim(),
    )

    expect(await updated()).toBe('Aug 12, 2026')
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
        <main data-signals="${escapeHTML(JSON.stringify({ page, chrome: { sidebar: { userName: 'Jacob Nielsen', userAvatarUrl: '/profile/avatars/jacob/avatar-digest' } } }))}">
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
