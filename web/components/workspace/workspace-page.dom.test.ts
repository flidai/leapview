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
const root = join(projectRoot, '.tmp/workspace-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument('workspace'))
      return
    }
    if (url.pathname === '/connections') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument('connections'))
      return
    }
    if (url.pathname === '/asset') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument('asset'))
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
  { name: 'desktop', width: 1280, height: 820 },
  { name: 'mobile', width: 390, height: 820 },
]) {
  test(`workspace route roots compose UI on ${viewport.name}`, async () => {
    const page = await browser.newPage({ viewport })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-workspace-page') && customElements.get('lv-record-table'))
      await page.locator('lv-workspace-page').evaluate((element: any) => element.updateComplete)
      const workspaceState = await page.evaluate(() => {
        const workspace = document.querySelector('lv-workspace-page') as any
        const workspacePage = workspace.shadowRoot.querySelector('.page') as HTMLElement
        const workspaceToolbar = workspace.shadowRoot.querySelector('.toolbar') as HTMLElement
        const workspaceRecordTable = workspace.shadowRoot.querySelector('lv-record-table') as HTMLElement
        const workspaceTablePanel = workspaceRecordTable.parentElement as HTMLElement
        const workspaceGlyph = workspace.shadowRoot.querySelector('.record-entity-icon') as HTMLElement | null
        const workspaceDashboardGlyph = workspace.shadowRoot.querySelector('.record-icon-dashboard') as HTMLElement | null
        const workspaceRowActionIcon = workspace.shadowRoot.querySelector('.record-actions svg') as SVGElement
        const workspaceRowActionLink = workspace.shadowRoot.querySelector('.record-actions .record-icon-action') as HTMLElement
        const workspaceNameCell = workspace.shadowRoot.querySelector('tbody tr:first-child td:first-child') as HTMLElement
        const workspaceTypeCell = workspace.shadowRoot.querySelector('tbody tr:first-child td:nth-child(2)') as HTMLElement
        const workspaceHeaderCell = workspace.shadowRoot.querySelector('thead th') as HTMLElement
        const workspaceFirstRow = workspace.shadowRoot.querySelector('tbody tr:first-child') as HTMLElement
        const workspaceSearch = workspace.shadowRoot.querySelector('.search input[type="search"]') as HTMLInputElement
        const workspaceSearchForm = workspace.shadowRoot.querySelector('.search') as HTMLElement
        const workspaceAccessButton = workspace.shadowRoot.querySelector('lv-workspace-access-control')?.shadowRoot?.querySelector('.trigger') as HTMLElement | null
        const workspaceAssetTitle = workspace.shadowRoot.querySelector('tbody tr:first-child .record-entity-label') as HTMLElement
        const workspaceAssetEntity = workspace.shadowRoot.querySelector('tbody tr:first-child .record-entity') as HTMLElement
        const nameCellRight = workspaceNameCell.getBoundingClientRect().right
        const workspacePageRect = workspacePage.getBoundingClientRect()
        const isMobile = window.innerWidth <= 720
        return {
          workspaceTitle: workspace.shadowRoot.querySelector('h1')?.textContent?.trim(),
          workspaceHasAsset: Boolean(workspaceRecordTable && workspace.shadowRoot.querySelector('.record-entity-label')),
          workspaceTableVariant: workspaceRecordTable.getAttribute('variant'),
          workspaceTableHeaders: Array.from(workspaceRecordTable.querySelectorAll('thead th button span:first-child')).map((header) => header.textContent?.trim()),
          workspaceTableMinWidth: getComputedStyle(workspaceRecordTable.querySelector('table') as HTMLElement).minWidth,
          workspaceTableBackground: getComputedStyle(workspaceRecordTable.querySelector('.record-table-wrap') as HTMLElement).backgroundColor,
          workspaceRowActionCounts: Array.from(workspaceRecordTable.querySelectorAll('tbody tr')).map((row) => row.querySelectorAll('.record-icon-action').length),
          workspaceTableHeaderBackground: getComputedStyle(workspaceRecordTable.querySelector('thead th') as HTMLElement).backgroundColor,
          workspaceTablePanelBorder: getComputedStyle(workspaceTablePanel).borderTopWidth,
          workspaceHasAccess: Boolean(workspace.shadowRoot.querySelector('lv-workspace-access-control')),
          workspaceHasAccessAction: workspaceAccessButton?.textContent?.trim(),
          workspaceHasAssetFilter: Boolean(workspace.shadowRoot.querySelector('.asset-filter select')),
          workspaceFilterOptions: Array.from(workspace.shadowRoot.querySelectorAll('.asset-filter option')).map((option) => option.textContent?.trim()),
          workspaceHasHeaderDetail: Boolean(workspace.shadowRoot.querySelector('.page-header .page-detail')),
          workspaceIsStyled: getComputedStyle(workspacePage).paddingTop !== '0px',
          workspacePageCentered: isMobile || Math.abs((workspacePageRect.left + workspacePageRect.width / 2) - window.innerWidth / 2) <= 1,
          workspacePageConstrained: isMobile || Math.round(workspacePageRect.width) < window.innerWidth,
          workspaceToolbarDisplay: getComputedStyle(workspaceToolbar).display,
          workspaceHasGlyphs: Boolean(workspace.shadowRoot.querySelector('.record-entity-icon')),
          workspaceHasDescriptions: Boolean(workspace.shadowRoot.querySelector('.record-entity-description')),
          workspaceNamesUseIconTrack: Array.from(workspace.shadowRoot.querySelectorAll('.record-entity')).every((entity) => !entity.classList.contains('record-entity-no-icon')),
          workspaceGlyphHasIcon: Boolean(workspaceGlyph?.querySelector('svg')),
          workspaceGlyphBackground: workspaceGlyph ? getComputedStyle(workspaceGlyph).backgroundColor : '',
          workspaceDashboardGlyphBorderColor: workspaceDashboardGlyph ? getComputedStyle(workspaceDashboardGlyph).borderTopColor : '',
          workspaceRowActionIconWidth: getComputedStyle(workspaceRowActionIcon).width,
          workspaceRowActionBorderColor: getComputedStyle(workspaceRowActionLink).borderTopColor,
          workspaceSearchFontSize: getComputedStyle(workspaceSearch).fontSize,
          workspaceSearchHeight: Math.round(workspaceSearch.getBoundingClientRect().height),
          workspaceSearchSpansToolbar: Math.abs(workspaceSearchForm.getBoundingClientRect().width - workspaceToolbar.getBoundingClientRect().width) <= 1,
          workspaceHeaderFontSize: getComputedStyle(workspaceHeaderCell).fontSize,
          workspaceCellFontSize: getComputedStyle(workspaceTypeCell).fontSize,
          workspaceTitleFontSize: getComputedStyle(workspaceAssetTitle).fontSize,
          workspaceTitleFontWeight: getComputedStyle(workspaceAssetTitle).fontWeight,
          workspaceAssetVerticalAlignment: getComputedStyle(workspaceAssetEntity).alignItems,
          workspaceRowHeight: Math.round(workspaceFirstRow.getBoundingClientRect().height),
          workspaceTitleFitsNameColumn: workspaceAssetTitle.getBoundingClientRect().right <= nameCellRight,
        }
      })

      expect(workspaceState).toEqual({
        workspaceTitle: 'LeapView Workspace',
        workspaceHasAsset: true,
        workspaceTableVariant: 'primary',
        workspaceTableHeaders: ['Name', 'Type', 'Actions'],
        workspaceTableMinWidth: '640px',
        workspaceTableBackground: 'rgb(238, 242, 246)',
        workspaceRowActionCounts: [1, 2],
        workspaceTableHeaderBackground: 'rgb(238, 242, 246)',
        workspaceTablePanelBorder: '0px',
        workspaceHasAccess: true,
        workspaceIsStyled: true,
        workspacePageCentered: true,
        workspacePageConstrained: true,
          workspaceToolbarDisplay: 'flex',
        workspaceHasGlyphs: true,
        workspaceHasDescriptions: false,
        workspaceNamesUseIconTrack: true,
        workspaceGlyphHasIcon: true,
        workspaceGlyphBackground: 'rgb(221, 244, 255)',
        workspaceDashboardGlyphBorderColor: 'rgb(210, 191, 255)',
        workspaceRowActionIconWidth: '16px',
        workspaceRowActionBorderColor: 'rgba(0, 0, 0, 0)',
          workspaceSearchFontSize: '14px',
          workspaceSearchHeight: 32,
          workspaceSearchSpansToolbar: false,
          workspaceHasAccessAction: 'Manage access',
          workspaceHasAssetFilter: true,
          workspaceFilterOptions: ['All', 'Dashboard'],
          workspaceHasHeaderDetail: false,
        workspaceHeaderFontSize: '12px',
        workspaceCellFontSize: '14px',
        workspaceTitleFontSize: '14px',
        workspaceTitleFontWeight: '600',
        workspaceAssetVerticalAlignment: 'center',
        workspaceRowHeight: 46,
        workspaceTitleFitsNameColumn: true,
      })

      await page.goto(`${baseURL}/connections`)
      await page.waitForFunction(() => customElements.get('lv-connections-page') && customElements.get('lv-record-table'))
      await page.locator('lv-connections-page').evaluate((element: any) => element.updateComplete)
      const connectionsState = await page.evaluate(() => {
        const connections = document.querySelector('lv-connections-page') as any
        const connectionsPage = connections.shadowRoot.querySelector('.page') as HTMLElement
        const connectionsPageRect = connectionsPage.getBoundingClientRect()
        const isMobile = window.innerWidth <= 720
        return {
          connectionsTitle: connections.shadowRoot.querySelector('h1')?.textContent?.trim(),
          connectionsHasSource: connections.shadowRoot.textContent?.includes('Orders source') ?? false,
          connectionsHasEntityList: Boolean(connections.shadowRoot.querySelector('.entity-list-items')),
          connectionsHasRecordTable: Boolean(connections.shadowRoot.querySelector('lv-record-table')),
          connectionsFilterOptions: Array.from(connections.shadowRoot.querySelectorAll('.entity-filter option')).map((option) => option.textContent?.trim()),
          connectionsIsStyled: getComputedStyle(connectionsPage).paddingTop !== '0px',
          connectionsPageCentered: isMobile || Math.abs((connectionsPageRect.left + connectionsPageRect.width / 2) - window.innerWidth / 2) <= 1,
          connectionsPageConstrained: isMobile || Math.round(connectionsPageRect.width) < window.innerWidth,
        }
      })
      expect(connectionsState).toEqual({
        connectionsTitle: 'Connections',
        connectionsHasSource: true,
        connectionsHasEntityList: true,
        connectionsHasRecordTable: false,
        connectionsFilterOptions: ['All', 'Connection', 'Source'],
        connectionsIsStyled: true,
        connectionsPageCentered: true,
        connectionsPageConstrained: true,
      })

      await page.goto(`${baseURL}/asset`)
      await page.waitForFunction(() => customElements.get('lv-workspace-asset-page') && customElements.get('lv-record-table'))
      await page.locator('lv-workspace-asset-page').evaluate((element: any) => element.updateComplete)
      const assetState = await page.evaluate(() => {
        const asset = document.querySelector('lv-workspace-asset-page') as any
        const assetHeader = asset.shadowRoot.querySelector('.breadcrumb-header') as HTMLElement
        const assetTabs = asset.shadowRoot.querySelector('.asset-body > .tabs') as HTMLElement
        const assetFirstTab = asset.shadowRoot.querySelector('.asset-body > .tabs a') as HTMLElement
        const assetSectionBody = asset.shadowRoot.querySelector('.section-body') as HTMLElement
        const semanticGraph = asset.shadowRoot.querySelector('lv-semantic-model-graph') as HTMLElement
        const firstRecordTable = asset.shadowRoot.querySelector('lv-record-table') as HTMLElement
        const semanticGraphSection = asset.shadowRoot.querySelector('.semantic-model-section') as HTMLElement
        const assetPage = asset.shadowRoot.querySelector('.asset-page') as HTMLElement
        return {
          assetTitle: asset.shadowRoot.querySelector('h1 span:last-child')?.textContent?.trim(),
          assetHasOverview: asset.shadowRoot.textContent?.includes('Overview') ?? false,
          assetHasRecordTable: Boolean(asset.shadowRoot.querySelector('lv-record-table')),
          assetHasSemanticGraph: Boolean(semanticGraph),
          assetSemanticGraphBeforeRecordTable: Boolean(semanticGraph && firstRecordTable && semanticGraph.compareDocumentPosition(firstRecordTable) & Node.DOCUMENT_POSITION_FOLLOWING),
          assetHasDataModelHeading: Array.from(asset.shadowRoot.querySelectorAll('h2')).some((heading) => heading.textContent?.trim() === 'Data model'),
          assetGraphFlushLeft: semanticGraphSection ? Math.round(semanticGraphSection.getBoundingClientRect().left - assetSectionBody.getBoundingClientRect().left) : -1,
          assetHeaderDisplay: getComputedStyle(assetHeader).display,
          assetTabsPaddingLeft: getComputedStyle(assetTabs).paddingLeft,
          assetFirstTabInset: Math.round(assetFirstTab.getBoundingClientRect().left - assetTabs.getBoundingClientRect().left),
          assetUsesDocumentScroll: getComputedStyle(assetSectionBody).overflowY === 'visible' && assetPage.scrollHeight === assetPage.clientHeight,
          assetExtendsPastViewport: assetPage.getBoundingClientRect().height > window.innerHeight,
        }
      })
      expect(assetState).toEqual({
        assetTitle: 'Olist Commerce',
        assetHasOverview: true,
        assetHasRecordTable: true,
        assetHasSemanticGraph: true,
        assetSemanticGraphBeforeRecordTable: true,
        assetHasDataModelHeading: false,
        assetGraphFlushLeft: 0,
        assetHeaderDisplay: 'grid',
        assetTabsPaddingLeft: '16px',
        assetFirstTabInset: 16,
        assetUsesDocumentScroll: true,
        assetExtendsPastViewport: true,
      })
    } finally {
      await page.close()
    }
  })
}

for (const viewport of [
  { name: 'compact desktop', width: 706, height: 793 },
  { name: 'mobile', width: 390, height: 820 },
]) {
  test(`workspace catalog renders compact full-width rows on ${viewport.name}`, async () => {
    const page = await browser.newPage({ viewport })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-workspace-page') && customElements.get('lv-entity-list'))
      await page.evaluate(async () => {
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
        mergePatch({ page: {
          kind: 'workspace',
          title: 'Workspaces',
          description: 'View published BI workspaces.',
          workspaces: [
            { id: 'operations', title: 'Operations Workspace', description: 'Fulfillment and delivery analysis.', href: '/workspaces/operations' },
            { id: 'sales', title: 'Sales Workspace', description: 'Revenue, orders, and product category analysis.', href: '/workspaces/sales' },
            { id: 'visuals', title: 'Visuals Workspace', description: 'Developer QA workspace for exhaustive dashboard visual and table renderer coverage.', href: '/workspaces/visuals' },
          ],
        } })
      })
      await page.locator('lv-workspace-page').evaluate((element: any) => element.updateComplete)

      const state = await page.locator('lv-workspace-page').evaluate((element: any) => {
        const list = element.shadowRoot.querySelector('.entity-list-items') as HTMLElement
        const table = element.shadowRoot.querySelector('.entity-list-table') as HTMLElement
        const rows = Array.from(element.shadowRoot.querySelectorAll('tbody tr.entity-list-table-row')) as HTMLTableRowElement[]
        const listRect = list.getBoundingClientRect()
        const tableRect = table.getBoundingClientRect()
        return {
          rowCount: rows.length,
          hrefs: rows.map((row) => row.querySelector('.entity-list-identity')?.getAttribute('href')),
          titles: rows.map((row) => row.querySelector('.entity-list-title')?.textContent?.trim()),
          listBackground: getComputedStyle(list).backgroundColor,
          hasStatuses: rows.some((row) => Boolean(row.querySelector('.workspace-status'))),
          hasIcons: rows.every((row) => Boolean(row.querySelector('.entity-list-icon svg'))),
          hasChevrons: rows.every((row) => Boolean(row.querySelector('.entity-list-chevron svg'))),
          fullWidth: rows.every((row) => Math.abs(row.getBoundingClientRect().width - tableRect.width) <= 1),
          maxRowHeight: Math.max(...rows.map((row) => Math.round(row.getBoundingClientRect().height))),
          totalListHeight: Math.round(listRect.height),
          hasOpenButton: Boolean(element.shadowRoot.querySelector('.primary-link')),
        }
      })

      expect(state).toEqual({
        rowCount: 3,
        hrefs: ['/workspaces/operations', '/workspaces/sales', '/workspaces/visuals'],
        titles: ['Operations Workspace', 'Sales Workspace', 'Visuals Workspace'],
        listBackground: 'rgb(238, 242, 246)',
        hasStatuses: false,
        hasIcons: true,
        hasChevrons: false,
        fullWidth: true,
        maxRowHeight: 52,
        totalListHeight: 188,
        hasOpenButton: false,
      })
    } finally {
      await page.close()
    }
  })
}

test('empty workspace search results preserve the catalog search input', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-workspace-page') && customElements.get('lv-entity-list'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'workspace',
        title: 'Workspaces',
        workspaces: [{ id: 'operations', title: 'Operations Workspace', description: 'Operations', href: '/workspaces/operations' }],
      } })
      const workspace = document.querySelector('lv-workspace-page') as any
      await workspace.updateComplete
      const input = workspace.shadowRoot.querySelector('.entity-search input') as HTMLInputElement
      input.value = 'missing workspace'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await workspace.updateComplete
      mergePatch({ page: { workspaces: [] } })
      await workspace.updateComplete
      const list = workspace.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
      return {
        input: (workspace.shadowRoot.querySelector('.entity-search input') as HTMLInputElement | null)?.value,
        empty: workspace.shadowRoot.querySelector('.entity-list-empty')?.textContent?.trim(),
        hasAssetToolbar: Boolean(workspace.shadowRoot.querySelector('.toolbar-filters')),
      }
    })

    expect(state).toEqual({
      input: 'missing workspace',
      empty: 'No results match your search.',
      hasAssetToolbar: false,
    })
  } finally {
    await page.close()
  }
})

test('workspace asset search delegates filtering to the page stream', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-workspace-page'))
    await page.locator('lv-workspace-page').evaluate((element: any) => element.updateComplete)
    await page.waitForFunction(() => Boolean((document.querySelector('lv-workspace-page') as any)?.shadowRoot?.querySelector('lv-workspace-access-control')?.shadowRoot?.querySelector('.trigger')))

    const state = await page.evaluate(async () => {
      const workspace = document.querySelector('lv-workspace-page') as any
      const root = workspace.shadowRoot
      const input = root.querySelector('.toolbar .search input[type="search"]') as HTMLInputElement
      const form = root.querySelector('.toolbar .search') as HTMLFormElement
      const before = Array.from(root.querySelectorAll('.record-entity-label')).map((link) => link.textContent?.trim())
      input.value = 'customer'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await workspace.updateComplete
      input.focus()
      const focusedStyle = getComputedStyle(input)
      const after = Array.from(root.querySelectorAll('.record-entity-label')).map((link) => link.textContent?.trim())
      input.value = 'olist'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await workspace.updateComplete
      const afterKeySearch = Array.from(root.querySelectorAll('.record-entity-label')).map((link) => link.textContent?.trim())
      return {
        before,
        after,
        afterKeySearch,
        focusedBorderColor: focusedStyle.borderTopColor,
        focusedOutlineStyle: focusedStyle.outlineStyle,
        hasSubmitButton: Boolean(root.querySelector('.toolbar .search button[type="submit"]')),
        formAction: form.getAttribute('action'),
        inputAutocomplete: input.getAttribute('autocomplete'),
      }
    })

    expect(state.before).toEqual(['Executive Sales Dashboard', 'Customer Segments'])
    // The component emits the query but does not filter stale rows locally.
    // The page stream replaces these rows when its debounced response arrives.
    expect(state.after).toEqual(state.before)
    expect(state.afterKeySearch).toEqual(state.before)
    expect(state.focusedBorderColor).toBe('rgb(216, 222, 228)')
    expect(state.focusedOutlineStyle).toBe('solid')
    expect(state.hasSubmitButton).toBe(false)
    expect(state.formAction).toBeNull()
    expect(state.inputAutocomplete).toBe('off')
  } finally {
    await page.close()
  }
})

test('workspace asset filter emits an update event without navigating', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-workspace-page'))
    await page.locator('lv-workspace-page').evaluate((element: any) => element.updateComplete)

    const state = await page.evaluate(async () => {
      const workspace = document.querySelector('lv-workspace-page') as any
      const select = workspace.shadowRoot.querySelector('.asset-filter select') as HTMLSelectElement
      let detail: unknown = null
      workspace.addEventListener('lv-workspace-asset-filter', (event: CustomEvent) => { detail = event.detail })
      select.value = 'dashboard'
      select.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await workspace.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { assetList: { assets: workspace.page.assetList.assets.slice(0, 1) } } })
      await workspace.updateComplete
      return {
        detail,
        url: window.location.href,
        selectedType: (workspace.shadowRoot.querySelector('.asset-filter select') as HTMLSelectElement).value,
        assetTitles: Array.from(workspace.shadowRoot.querySelectorAll('.record-entity-label')).map((link) => link.textContent?.trim()),
      }
    })

    expect(state.detail).toEqual({ type: 'dashboard', query: '' })
    expect(state.url).toBe(`${baseURL}/`)
    expect(state.selectedType).toBe('dashboard')
    expect(state.assetTitles).toEqual(['Executive Sales Dashboard'])
  } finally {
    await page.close()
  }
})

test('workspace access drawer selects a role before searching and adds each result directly', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-workspace-page'))
    await page.locator('lv-workspace-page').evaluate((element: any) => element.updateComplete)

    const state = await page.evaluate(async () => {
      const workspace = document.querySelector('lv-workspace-page') as any
      const accessControl = workspace.shadowRoot.querySelector('lv-workspace-access-control') as any
      const searchEvents: unknown[] = []
      const upsertEvents: unknown[] = []
      const removeEvents: unknown[] = []
      accessControl.addEventListener('lv-workspace-access-search', (event: CustomEvent) => searchEvents.push(event.detail))
      accessControl.addEventListener('lv-workspace-access-upsert', (event: CustomEvent) => upsertEvents.push(event.detail))
      accessControl.addEventListener('lv-workspace-access-remove', (event: CustomEvent) => removeEvents.push(event.detail))
      accessControl.shadowRoot.querySelector('.trigger').click()
      await accessControl.updateComplete
      const drawer = accessControl.shadowRoot.querySelector('lv-drawer') as any
      const dialog = drawer?.shadowRoot?.querySelector('[role="dialog"]')
      const rolePicker = accessControl.shadowRoot.querySelector('.assignment-role') as HTMLSelectElement
      const search = accessControl.shadowRoot.querySelector('.access-search input') as HTMLInputElement
      const rolePrecedesSearch = Boolean(rolePicker.compareDocumentPosition(search) & Node.DOCUMENT_POSITION_FOLLOWING)
      const searchDisabledBeforeRole = search.disabled
      const roleOptions = Array.from(rolePicker.options).map((option) => ({
        value: (option as HTMLOptionElement).value,
        label: option.textContent?.trim(),
      }))
      rolePicker.value = 'data_deployer'
      rolePicker.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await accessControl.updateComplete
      search.value = 'finance'
      search.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 250))
      const candidates = Array.from(accessControl.shadowRoot.querySelectorAll<HTMLElement>('.candidate'))
      const candidateTypes = candidates.map((candidate) => candidate.dataset.subjectType)
      const addButtons = Array.from(accessControl.shadowRoot.querySelectorAll<HTMLButtonElement>('.candidate-add'))
      addButtons[0]?.click()
      accessControl.shadowRoot.querySelector<HTMLButtonElement>('button[aria-label="Remove Operations Group"]')?.click()
      const rowRole = accessControl.shadowRoot.querySelector('.row select') as HTMLSelectElement | null
      return {
        hasDrawer: Boolean(drawer),
        hasDialog: Boolean(dialog),
        modal: dialog?.getAttribute('aria-modal'),
        title: accessControl.shadowRoot.querySelector('.subtitle')?.textContent?.trim(),
        hasSubjectTypePicker: Boolean(accessControl.shadowRoot.querySelector('.composer-subject-type')),
        rolePrecedesSearch,
        searchDisabledBeforeRole,
        searchDisabledAfterRole: search.disabled,
        searchPlaceholder: search.placeholder,
        searchEvents,
        candidateTypes,
        candidateLabels: candidates.map((candidate) => candidate.textContent?.replace(/\s+/g, ' ').trim()),
        addButtonLabels: addButtons.map((button) => button.getAttribute('aria-label')),
        hasSelectedSubject: Boolean(accessControl.shadowRoot.querySelector('.selected-subject')),
        roleOptions,
        upsertEvents,
        removeEvents,
        rowRoleValue: rowRole?.value,
        principal: accessControl.shadowRoot.querySelector('.name')?.textContent?.trim(),
      }
    })

    expect(state).toEqual({
      hasDrawer: true,
      hasDialog: true,
      modal: 'true',
      title: 'LeapView Workspace roles apply to every published asset in this workspace.',
      hasSubjectTypePicker: false,
      rolePrecedesSearch: true,
      searchDisabledBeforeRole: true,
      searchDisabledAfterRole: false,
      searchPlaceholder: 'Search people and groups...',
      searchEvents: [{ search: 'finance' }],
      candidateTypes: ['principal', 'group'],
      candidateLabels: ['Ana Analyst ana@example.com', 'Analytics Group'],
      addButtonLabels: ['Add Ana Analyst as Data Deployer', 'Add Analytics as Data Deployer'],
      hasSelectedSubject: false,
      roleOptions: [
        { value: '', label: 'Select a role' },
        { value: 'viewer', label: 'Viewer' },
        { value: 'workspace_admin', label: 'Workspace Admin' },
        { value: 'data_deployer', label: 'Data Deployer' },
      ],
      upsertEvents: [{
        email: '',
        role: 'data_deployer',
        privilege: '',
        subjectType: 'principal',
        subjectId: 'principal_ana',
      }],
      removeEvents: [{
        principalId: '',
        bindingId: 'rolebinding_operations',
        subjectType: 'group',
        subjectId: 'group_operations',
      }],
      rowRoleValue: 'viewer',
      principal: 'analyst@example.com',
    })
  } finally {
    await page.close()
  }
})

test('refresh pipeline page renders run history and emits run-now events', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/asset`)
    await page.waitForFunction(() => customElements.get('lv-workspace-asset-page') && customElements.get('lv-record-table'))

    const state = await page.evaluate(async () => {
      const asset = document.querySelector('lv-workspace-asset-page') as any
      let refreshEvents = 0
      asset.addEventListener('lv-run-refresh-pipeline', () => { refreshEvents += 1 })
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'workspace_asset',
        title: 'Sales refresh',
        workspaceId: 'leapview',
        assetId: 'refresh_pipeline:sales-refresh',
        activeSection: 'refreshes',
        asset: {
          id: 'refresh_pipeline:sales-refresh',
          title: 'Sales refresh',
          description: '',
          type: 'refresh_pipeline',
          typeLabel: 'Refresh pipeline',
          key: 'sales-refresh',
          detailHref: '/workspaces/leapview/assets/refresh_pipeline:sales-refresh/details',
          openHref: '/workspaces/leapview/assets/refresh_pipeline:sales-refresh/details',
        },
        breadcrumbs: [
          { label: 'Workspaces', href: '/workspaces' },
          { label: 'LeapView Workspace', href: '/workspaces/leapview' },
          { label: 'Sales refresh', current: true },
        ],
        actions: [
          { label: 'Run now', icon: 'refresh', command: 'run-refresh-pipeline' },
          { label: 'Back to workspace', href: '/workspaces/leapview', icon: 'back' },
        ],
        tabs: [
          { id: 'details', label: 'Details', href: '/workspaces/leapview/assets/refresh_pipeline:sales-refresh/details', active: false },
          { id: 'refreshes', label: 'Refreshes', href: '/workspaces/leapview/assets/refresh_pipeline:sales-refresh/refreshes', active: true },
          { id: 'lineage', label: 'Lineage', href: '/workspaces/leapview/assets/refresh_pipeline:sales-refresh/lineage', active: false, count: 1 },
        ],
        refresh: {
          status: 'succeeded',
          running: false,
          lastSuccessful: '2026-06-26 10:00:12',
          runsTable: {
            columns: [
              { id: 'status', header: 'Status', kind: 'status' },
              { id: 'started', header: 'Started' },
              { id: 'run', header: 'Run ID', kind: 'code' },
            ],
            rows: [{ status: { label: 'succeeded', tone: 'success' }, started: '2026-06-26 10:00:00', run: 'matrun_123' }],
            empty: 'No refresh runs.',
          },
        },
      } })
      await asset.updateComplete
      const button = asset.shadowRoot.querySelector('button[aria-label="Run now"]') as HTMLButtonElement
      button.click()
      mergePatch({ page: { refresh: { running: true } } })
      await asset.updateComplete
      const runningButton = asset.shadowRoot.querySelector('button[aria-label="Run now"]') as HTMLButtonElement
      const spinner = runningButton.querySelector('lv-loading-spinner') as any
      await spinner?.updateComplete
      const spinnerSvg = spinner?.shadowRoot?.querySelector('svg') as SVGElement | null
      return {
        activeTab: asset.shadowRoot.querySelector('.tabs a.active')?.textContent?.trim(),
        hasRefreshButton: Boolean(button),
        recordTableText: asset.shadowRoot.querySelector('lv-record-table')?.textContent,
        refreshEvents,
        hasSpinner: Boolean(spinner),
        spinnerDuration: spinnerSvg ? getComputedStyle(spinnerSvg).animationDuration : '',
        spinnerInheritsButtonColor: Boolean(spinner && getComputedStyle(spinner).color === getComputedStyle(runningButton).color),
      }
    })

    expect(state.activeTab).toBe('Refreshes')
    expect(state.hasRefreshButton).toBe(true)
    expect(state.recordTableText ?? '').toMatch(/matrun_123/)
    expect(state.refreshEvents).toBe(1)
    expect(state.hasSpinner).toBe(true)
    expect(state.spinnerDuration).toBe('1.8s')
    expect(state.spinnerInheritsButtonColor).toBe(true)
  } finally {
    await page.close()
  }
})

test('workspace asset page does not render versions as a product surface', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(`${baseURL}/asset`)
    await page.waitForFunction(() => customElements.get('lv-workspace-asset-page') && customElements.get('lv-record-table'))

    const state = await page.evaluate(async () => {
      const asset = document.querySelector('lv-workspace-asset-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'workspace_asset',
        title: 'Executive Sales Dashboard',
        workspaceId: 'leapview',
        assetId: 'dashboard:executive-sales',
        activeSection: 'versions',
        asset: {
          id: 'dashboard:executive-sales',
          title: 'Executive Sales Dashboard',
          type: 'dashboard',
          typeLabel: 'Dashboard',
          key: 'executive-sales',
          detailHref: '/workspaces/leapview/assets/dashboard:executive-sales/details',
          openHref: '/dashboards/executive-sales',
        },
        breadcrumbs: [
          { label: 'Workspaces', href: '/workspaces' },
          { label: 'LeapView Workspace', href: '/workspaces/leapview' },
          { label: 'Executive Sales Dashboard', current: true },
        ],
        actions: [],
        tabs: [
          { id: 'details', label: 'Details', href: '/workspaces/leapview/assets/dashboard:executive-sales/details', active: false },
          { id: 'lineage', label: 'Lineage', href: '/workspaces/leapview/assets/dashboard:executive-sales/lineage', active: false, count: 1 },
        ],
        details: {
          overview: [
            { label: 'Type', value: 'Dashboard' },
          ],
          sections: [],
        },
      } })
      await asset.updateComplete
      const table = asset.shadowRoot.querySelector('lv-record-table') as HTMLElement | null
      return {
        tabText: asset.shadowRoot.querySelector('.tabs')?.textContent ?? '',
        sectionTitle: asset.shadowRoot.querySelector('.detail-section h2')?.textContent?.trim(),
        tableText: table?.textContent ?? '',
        bodyText: asset.shadowRoot.textContent ?? '',
      }
    })

    expect(state.tabText).not.toMatch(/Versions/)
    expect(state.sectionTitle).not.toBe('Versions')
    expect(state.tableText).not.toMatch(/Deployment digest/)
    expect(state.bodyText).not.toMatch(/Deployment digest/)
  } finally {
    await page.close()
  }
})

function testDocument(root: 'workspace' | 'connections' | 'asset'): string {
  const assetList = {
    workspaceId: 'leapview',
    searchHref: '/workspaces/leapview',
    tabs: [
      { id: '', label: 'All', href: '/workspaces/leapview', active: true },
      { id: 'dashboard', label: 'Dashboard', href: '/workspaces/leapview?type=dashboard', active: false },
    ],
    assets: [
      {
        id: 'semantic_model:olist',
        title: 'Executive Sales Dashboard',
        description: 'Sales, order, category, and delivery overview with deliberately long text for table fitting.',
        type: 'semantic_model',
        typeLabel: 'Semantic model',
        key: 'olist',
        parentTitle: '-',
        detailHref: '/workspaces/leapview/assets/semantic_model:olist/details',
        openHref: '/workspaces/leapview/assets/semantic_model:olist/details',
      },
      {
        id: 'dashboard:customers',
        title: 'Customer Segments',
        description: 'Customer cohort report.',
        type: 'dashboard',
        typeLabel: 'Dashboard',
        key: 'customers',
        parentTitle: '-',
        detailHref: '/workspaces/leapview/assets/dashboard:customers/details',
        openHref: '/dashboards/customers',
      },
    ],
    empty: 'No assets match this view.',
  }
  const workspacePage = {
    kind: 'workspace',
    title: 'LeapView Workspace',
    description: 'Published BI assets.',
    workspaceId: 'leapview',
    assetList,
  }
  const connectionsPage = {
    kind: 'connections',
    title: 'Connections',
    description: 'Connection-scoped data assets.',
    workspaceId: 'leapview',
    assetList: {
      ...assetList,
      searchHref: '/connections',
      tabs: [
        { id: '', label: 'All', href: '/connections', active: true },
        { id: 'connection', label: 'Connection', href: '/connections?type=connection', active: false },
        { id: 'source', label: 'Source', href: '/connections?type=source', active: false },
      ],
      assets: [{ ...assetList.assets[0], title: 'Orders source', type: 'source', typeLabel: 'Source', detailHref: '/connections/connection:olist/sources/source:orders/details' }],
    },
  }
  const assetPage = {
    kind: 'workspace_asset',
    title: 'Olist Commerce',
    workspaceId: 'leapview',
    assetId: 'semantic_model:olist',
    activeSection: 'details',
    asset: assetList.assets[0],
    breadcrumbs: [
      { label: 'Workspaces', href: '/workspaces' },
      { label: 'LeapView Workspace', href: '/workspaces/leapview' },
      { label: 'Olist Commerce', current: true },
    ],
    actions: [],
    tabs: [
      { id: 'details', label: 'Details', href: '/workspaces/leapview/assets/semantic_model:olist/details', active: true },
      { id: 'lineage', label: 'Lineage', href: '/workspaces/leapview/assets/semantic_model:olist/lineage', active: false, count: 1 },
    ],
    details: {
      overview: [
        { label: 'Type', value: 'Semantic model' },
        { label: 'Key', value: 'olist', code: true },
      ],
      semanticModelGraph: {
        facts: ['orders'],
        nodes: [{
          id: 'orders',
          title: 'orders',
          primaryKey: 'order_id',
          badges: ['fact', '2 measures'],
          fields: [
            { name: 'order_id', label: 'Order ID', primaryKey: true },
            { name: 'customer_id', label: 'Customer ID', join: true, relationships: ['orders_customers'] },
          ],
        }, {
          id: 'customers',
          title: 'customers',
          primaryKey: 'customer_id',
          fields: [{ name: 'customer_id', label: 'Customer ID', primaryKey: true, join: true, relationships: ['orders_customers'] }],
        }],
        edges: [{
          id: 'orders_customers',
          source: 'orders',
          target: 'customers',
          sourceField: 'customer_id',
          targetField: 'customer_id',
          cardinality: 'many_to_one',
          label: '*:1',
        }],
      },
      sections: [{
        title: 'Model tables (1)',
        table: {
          columns: [{ id: 'name', header: 'Name', kind: 'link', hrefKey: 'nameHref' }],
          rows: [{ name: 'orders', nameHref: '/workspaces/leapview/assets/model_table:olist.orders/details' }],
          empty: 'No model tables.',
        },
      }],
    },
  }
  const access = {
    workspace: { ID: 'leapview', Title: 'LeapView Workspace' },
    roles: [{ Name: 'viewer' }, { Name: 'workspace_admin' }, { Name: 'data_deployer' }],
    bindings: [{
      ID: 'rolebinding_analyst',
      SubjectType: 'principal',
      SubjectID: 'principal:analyst@example.com',
      PrincipalID: 'principal:analyst@example.com',
      Email: 'analyst@example.com',
      DisplayName: '',
      Role: 'viewer',
    }, {
      ID: 'rolebinding_operations',
      SubjectType: 'group',
      SubjectID: 'group_operations',
      PrincipalID: '',
      GroupID: 'group_operations',
      Email: '',
      GroupName: 'Operations Group',
      Role: 'viewer',
    }],
    candidates: [
      { subjectType: 'principal', subjectId: 'principal_ana', label: 'Ana Analyst', detail: 'ana@example.com' },
      { subjectType: 'group', subjectId: 'group_analytics', label: 'Analytics', detail: 'Group' },
    ],
    canManage: true,
    status: { loading: false, error: '', message: '' },
    command: { email: '', role: '', principalId: '' },
    search: 'ana',
    searchStatus: { loading: false, error: '' },
  }
  const route = root === 'connections'
    ? { signals: { page: connectionsPage }, element: '<lv-connections-page></lv-connections-page>' }
    : root === 'asset'
      ? { signals: { page: assetPage }, element: '<lv-workspace-asset-page></lv-workspace-asset-page>' }
      : { signals: { page: workspacePage, workspaceAccess: access }, element: '<lv-workspace-page></lv-workspace-page>' }
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-page: #eef2f6; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-link: #0969da; --lv-accent: #0969da; --lv-accent-fg: #fff; --lv-line-muted: #d8dee4; --lv-line-accent: #0969da; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-border-transparent: 1px solid transparent; --lv-radius-default: 6px; --lv-radius-tight: 4px; --lv-radius-full: 999px; --lv-page-content-max-width: 72rem; --lv-workspace-detail-max-width: 72rem; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-10: 10px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --base-size-24: 24px; --lv-space-control: 10px; --control-medium-size: 32px; --control-xlarge-size: 40px; --lv-spinner-size-md: 16px; --lv-spinner-duration: 1800ms; --lv-asset-dashboard-bg: #fbefff; --lv-asset-dashboard-accent: #8250df; --lv-asset-dashboard-border: #d2bfff; --lv-asset-semantic-model-bg: #ddf4ff; --lv-asset-semantic-model-accent: #0969da; --lv-asset-semantic-model-border: #b6e3ff; --z-index-inspector: 1000; --lv-modal-backdrop: rgb(0 0 0 / .28); }
          body { --focus-outline: 2px solid var(--lv-line-accent); --focus-outline-offset: 2px; }
          lv-workspace-page, lv-connections-page, lv-workspace-asset-page { display: block; min-height: 720px; }
        </style>
      </head>
      <body>
        <main data-signals="${escapeHTML(JSON.stringify(route.signals))}">
          ${route.element}
        </main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/workspace-page-under-test.js"></script>
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
