import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { governedBarPreviewEnvelope, builderTestDocument as testDocument } from './dashboard-builder-test-fixtures'

let server: Server
let baseURL = ''
let browser: Browser
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/dashboard-builder-test')

test('filter settings prevent invalid requirements and visibly restore a blank label', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { filters: [{ id: 'state', label: 'State', dimension: 'orders.status', controlType: 'multiSelect', required: false, readerEditable: true, urlParameter: 'state', targets: [], bindings: [] }] } })
      await element.updateComplete
      element.shadowRoot.querySelector('.filter-card').click()
      await element.updateComplete
      element.shadowRoot.querySelector('.filter-settings').open = true
      element.testFilterCommands = []
      element.addEventListener('lv-builder-command', (event: CustomEvent) => element.testFilterCommands.push(event.detail))
    })
    const editor = page.getByRole('region', { name: 'Configure State filter', exact: true })
    expect(await editor.getByRole('checkbox', { name: 'Required', exact: true }).isDisabled()).toBe(true)
    const label = editor.getByRole('textbox', { name: 'Label', exact: true })
    await label.fill('   ')
    await label.press('Tab')
    expect(await label.inputValue()).toBe('State')
    const parameter = editor.getByRole('textbox', { name: 'URL parameter', exact: true })
    await parameter.fill(' state ')
    await parameter.press('Tab')
    expect(await parameter.inputValue()).toBe('state')
    expect(await page.locator('lv-dashboard-builder').evaluate((element: any) => element.testFilterCommands)).toEqual([])
  } finally { await page.close() }
})

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
  if (!address || typeof address === 'string') throw new Error('dashboard builder test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('compiled multi-series previews retain their data despite picker default limits', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any, preview) => {
      await element.updateComplete
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const source = element.builder.pages[0].visuals[0]
      mergePatch({ builder: {
        visualCatalog: element.builder.visualCatalog.map((entry: any) => entry.type === 'bar'
          ? { ...entry, roleLimits: [{ role: 'dimension', minimum: 1, maximum: 1 }, { role: 'metric', minimum: 1, maximum: 0 }] } : entry),
        pages: [{ ...element.builder.pages[0], visuals: [{ ...source, slots: [
          { id: 'dimension-0', label: 'Status', kind: 'dimension', fieldId: 'orders.status', required: true },
          { id: 'dimension-1', label: 'Month', kind: 'dimension', fieldId: 'orders.month', required: true },
          { id: 'metric-0', label: 'Total', kind: 'metric', fieldId: 'orders.total', required: true },
        ] }] }, element.builder.pages[1]],
        preview: { ...element.builder.preview, active: true, error: '' },
      }, builderVisuals: { 'sales-chart': preview } })
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const host = root.querySelector('lv-visualization-host') as any
      await host?.updateComplete
      return {
        placeholder: root.querySelector('.visual-preview-empty')?.textContent,
        requirements: root.querySelector('.visual-requirements')?.textContent,
        rows: host?.envelope.dataState.datasets[0].rows,
        fields: [...root.querySelectorAll('.field-token-label')].map(node => node.textContent?.trim()),
      }
    }, governedBarPreviewEnvelope('sha256:compiled-series'))
    expect(state.placeholder).toBeUndefined()
    expect(state.requirements).toBeUndefined()
    expect(state.rows).toEqual([['Delivered', 42], ['Shipped', 7]])
    expect(state.fields).toContain('Month')
  } finally {
    await page.close()
  }
})

test('dashboard builder exposes guarded Apply and Cancel actions for deferred filters', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const key = 'dashboard:revenue/report/status'
      const unfiltered = { kind: 'unfiltered' }
      const selected = { kind: 'set', operator: 'in', values: [{ kind: 'string', value: 'paid' }] }
      const contract = {
        applicationMode: 'deferred',
        definitions: {},
        bindings: { [key]: { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] } },
      }
      const applied = (expression: any) => ({ expression, resolvedExpression: expression })
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const commands: any[] = []
      element.addEventListener('lv-builder-filter-command', (event: CustomEvent) => commands.push(event.detail))
      mergePatch({
        builderFilterContract: contract,
        builderFilterState: { revision: 1, appliedControls: { [key]: applied(unfiltered) }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' },
      })
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const initial = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-filter-apply], [data-filter-cancel]')).map((button) => ({ label: button.textContent?.trim(), disabled: button.disabled }))
      element.dispatchEvent(new CustomEvent('lv-filter-mutate', { bubbles: true, composed: true, detail: { bindingKey: key, expression: selected } }))
      await element.updateComplete
      const whilePending = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-filter-apply], [data-filter-cancel]')).map((button) => button.disabled)
      mergePatch({ builderFilterState: { revision: 2, appliedControls: { [key]: applied(unfiltered) }, draftControls: { [key]: selected }, dirtyBindings: [key], defaultsRevision: 'defaults-1' } })
      await element.updateComplete
      await element.updateComplete
      const dirty = Array.from(root.querySelectorAll<HTMLButtonElement>('[data-filter-apply], [data-filter-cancel]')).map((button) => ({ label: button.textContent?.trim(), disabled: button.disabled }))
      ;(root.querySelector<HTMLButtonElement>('[data-filter-apply]'))!.click()
      await element.updateComplete
      mergePatch({ builderFilterState: { revision: 3, appliedControls: { [key]: applied(selected) }, draftControls: { [key]: selected }, dirtyBindings: [key], defaultsRevision: 'defaults-1' } })
      await element.updateComplete
      await element.updateComplete
      ;(root.querySelector<HTMLButtonElement>('[data-filter-cancel]'))!.click()
      await element.updateComplete
      return { initial, whilePending, dirty, commands }
    })
    expect(state.initial).toEqual([])
    expect(state.whilePending).toEqual([true, true])
    expect(state.dirty).toEqual([{ label: 'Cancel', disabled: false }, { label: 'Apply (1)', disabled: false }])
    expect(state.commands.map((command: any) => command.kind)).toEqual(['mutate', 'apply', 'cancel'])
    expect(state.commands[1]).toMatchObject({ kind: 'apply', baseRevision: 2 })
    expect(state.commands[2]).toMatchObject({ kind: 'cancel', baseRevision: 3 })
  } finally {
    await page.close()
  }
})

test('dashboard builder keeps search and cursor changes distinct during option request dedupe', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const state = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      await element.updateComplete
      const key = 'dashboard:revenue/report/status'
      const unfiltered = { kind: 'unfiltered' }
      const contract = {
        applicationMode: 'immediate',
        definitions: { status: { id: 'status', label: 'Status', field: 'orders.status', dataset: 'orders', valueKind: 'string', predicates: [{ kind: 'set', operators: ['in'] }], options: { kind: 'distinct', limit: 20, includeNull: false, values: [] } } },
        bindings: { [key]: { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'multiple', maxSelectedValues: 0, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] } },
      }
      const requests: any[] = []
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      element.addEventListener('lv-builder-filter-options-request', (event: CustomEvent) => requests.push(event.detail))
      mergePatch({ builderFilterContract: contract, builderFilterState: { revision: 1, appliedControls: { [key]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' } })
      await element.updateComplete
      const request = (search: string, cursor?: string) => element.dispatchEvent(new CustomEvent('lv-filter-options-needed', { bubbles: true, composed: true, detail: { bindingKey: key, search, cursor, limit: 20 } }))
      request('')
      request('paid')
      request('paid', 'cursor-1')
      await element.updateComplete
      return { generations: requests.map((item) => item.requestGeneration), searches: requests.map((item) => item.search), cursors: requests.map((item) => item.cursor) }
    })
    expect(state).toEqual({ generations: [1, 2, 3], searches: ['', 'paid', 'paid'], cursors: [undefined, undefined, 'cursor-1'] })
  } finally {
    await page.close()
  }
})

test('focused filter options recover when a filter revision supersedes an in-flight request', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    const result = await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const key = 'dashboard:revenue/report/status'
      const unfiltered = { kind: 'unfiltered' }
      const binding = { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'single', maxSelectedValues: 1, required: false, readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] }
      const definition = { id: 'status', label: 'Status', field: 'orders.status', dataset: 'orders', valueKind: 'string', predicates: [{ kind: 'set', operators: ['in'] }], options: { kind: 'distinct', limit: 20, includeNull: false, values: [] } }
      const state = (revision: number) => ({ revision, appliedControls: { [key]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: 'defaults-1' })
      mergePatch({ builderFilterContract: { applicationMode: 'immediate', definitions: { status: definition }, bindings: { [key]: binding } }, builderFilterState: state(1) })
      await element.updateComplete
      const leaf: any = document.createElement('lv-filter-leaf')
      Object.assign(leaf, { definition, binding, presentation: { style: 'dropdown', search: false, selectAll: false, showCounts: false, showSummary: false, compact: false }, expression: unfiltered, optionRequestReady: true, optionContext: element.builderFilterOptionContext(binding, 'overview') })
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => element.dispatchEvent(new CustomEvent('lv-filter-options-needed', { detail: event.detail })))
      document.body.append(leaf)
      await leaf.updateComplete
      const requests: any[] = []
      element.addEventListener('lv-builder-filter-options-request', (event: CustomEvent) => requests.push(event.detail))
      leaf.shadowRoot.querySelector('select').focus()
      await leaf.updateComplete
      mergePatch({ builderFilterState: state(2), builderFilterOptionPages: {} })
      await element.updateComplete
      leaf.optionContext = element.builderFilterOptionContext(binding, 'overview')
      await leaf.updateComplete
      leaf.options = { bindingKey: key, servingStateID: 'generation-7', filterRevision: 2, requestGeneration: 2, items: [{ value: { kind: 'string', value: 'paid' }, label: 'Paid', null: false, selected: false, available: true }], complete: true }
      await leaf.updateComplete
      await leaf.updateComplete
      return { revisions: requests.map((request) => request.filterRevision), loading: leaf.optionLoading, labels: [...leaf.shadowRoot.querySelector('select').options].map((option: any) => option.text) }
    })
    expect(result.revisions).toEqual([1, 2])
    expect(result.loading).toBe(false)
    expect(result.labels).toContain('Paid')
  } finally { await page.close() }
})

test('dynamic option controls request explicit cursors and retain prior pages', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-filter-leaf'))
    const result = await page.evaluate(async () => {
      const leaf = document.createElement('lv-filter-leaf') as any
      leaf.definition = {
        id: 'status', label: 'Status', field: 'orders.status', valueKind: 'string',
        predicates: [{ kind: 'set', operators: ['in'] }],
        options: { kind: 'distinct', limit: 1, values: [] },
      }
      leaf.binding = {
        key: 'fb_status', id: 'status', filter: 'status', scope: 'page', pageID: 'overview',
        default: { kind: 'unfiltered' }, selectionMode: 'multiple', maxSelectedValues: 0,
        readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [],
      }
      leaf.presentation = {
        style: 'list', search: false, selectAll: false, showCounts: false, showSummary: false, compact: false,
      }
      const requests: unknown[] = []
      leaf.addEventListener('lv-filter-options-needed', (event: CustomEvent) => requests.push(event.detail))
      document.body.append(leaf)
      await leaf.updateComplete
      leaf.options = {
        bindingKey: 'fb_status', servingStateID: 'serving', streamGeneration: 1,
        filterRevision: 1, requestGeneration: 1, complete: false, nextCursor: 'cursor-2',
        consumerIdentity: 'option:fb_status',
        items: [{ value: { kind: 'string', value: 'first' }, label: 'First', null: false, selected: false, available: true }],
      }
      await leaf.updateComplete
      await leaf.updateComplete
      const beforeLoadMore = {
        requests: requests.length,
        labels: Array.from((leaf.shadowRoot as ShadowRoot).querySelectorAll('.option span')).map((item) => item.textContent?.trim()),
        loadMore: (leaf.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.load-more-options')?.textContent?.trim(),
      }
      ;(leaf.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.load-more-options')!.click()
      await leaf.updateComplete
      await leaf.updateComplete
      const afterLoadMoreRequest = requests.at(-1)
      leaf.options = {
        bindingKey: 'fb_status', servingStateID: 'serving', streamGeneration: 1,
        filterRevision: 1, requestGeneration: 2, complete: true,
        consumerIdentity: 'option:fb_status',
        items: [{ value: { kind: 'string', value: 'second' }, label: 'Second', null: false, selected: false, available: true }],
      }
      await leaf.updateComplete
      await leaf.updateComplete
      const afterSecondPage = {
        labels: Array.from((leaf.shadowRoot as ShadowRoot).querySelectorAll('.option span')).map((item) => item.textContent?.trim()),
        hasLoadMore: Boolean((leaf.shadowRoot as ShadowRoot).querySelector('.load-more-options')),
      }
      // The parent may project the same accepted page again during an
      // unrelated rerender. That projection must not discard page one.
      leaf.options = { ...leaf.options, items: [...leaf.options.items] }
      await leaf.updateComplete
      await leaf.updateComplete
      const afterDuplicatePage = Array.from((leaf.shadowRoot as ShadowRoot).querySelectorAll('.option span')).map((item) => item.textContent?.trim())
      // A late first page must not replace the accepted continuation.
      leaf.options = {
        bindingKey: 'fb_status', servingStateID: 'serving', streamGeneration: 1,
        filterRevision: 1, requestGeneration: 1, complete: false, nextCursor: 'cursor-2',
        consumerIdentity: 'option:fb_status',
        items: [{ value: { kind: 'string', value: 'first' }, label: 'First', null: false, selected: false, available: true }],
      }
      await leaf.updateComplete
      await leaf.updateComplete
      return {
        beforeLoadMore,
        afterLoadMoreRequest,
        afterSecondPage,
        afterDuplicatePage,
        afterStalePage: Array.from((leaf.shadowRoot as ShadowRoot).querySelectorAll('.option span')).map((item) => item.textContent?.trim()),
      }
    })
    expect(result).toEqual({
      beforeLoadMore: { requests: 1, labels: ['First'], loadMore: 'Load more values' },
      afterLoadMoreRequest: { bindingKey: 'fb_status', search: '', cursor: 'cursor-2', limit: 1 },
      afterSecondPage: { labels: ['First', 'Second'], hasLoadMore: false },
      afterDuplicatePage: ['First', 'Second'],
      afterStalePage: ['First', 'Second'],
    })
  } finally {
    await page.close()
  }
})

test('pending page preview shows loading without false invalid-preview errors', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { preview: { active: false, loading: true, error: '' } }, builderVisuals: null })
      await element.updateComplete
    })
    const builder = page.locator('lv-dashboard-builder')
    expect(await builder.getByText('Loading page data…', { exact: false }).count()).toBeGreaterThan(0)
    expect(await builder.locator('.visual-preview-empty').first().innerText()).toContain('Loading ')
    expect(await builder.locator('.visual-preview-empty').first().innerText()).not.toContain('unavailable')
  } finally { await page.close() }
})

test('background dashboard updates preserve an in-flight filter continuation', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const key = 'fb_status'
      const unfiltered = { kind: 'unfiltered' }
      const binding = { key, id: 'status', filter: 'status', scope: 'report', default: unfiltered, selectionMode: 'multiple', readerEditable: true, paneVisible: true, paneOrder: 0, targets: [], optionDependencies: [] }
      const definition = { id: 'status', label: 'Status', field: 'orders.status', valueKind: 'string', predicates: [{ kind: 'set', operators: ['in'] }], options: { kind: 'distinct', limit: 1, values: [] } }
      element.optionRequests = []
      element.addEventListener('lv-builder-filter-options-request', (event: CustomEvent) => {
        const request = event.detail
        element.optionRequests.push(request)
        if (element.optionRequests.length > 4) return
        if (request.cursor) setTimeout(() => mergePatch({ status: { lastUpdated: '2026-09-13T12:00:00Z' } }), 20)
        setTimeout(() => mergePatch({ builderFilterOptionPages: { [key]: {
          bindingKey: key, servingStateID: 'generation-7', streamGeneration: 0, filterRevision: 1,
          requestGeneration: request.requestGeneration, complete: Boolean(request.cursor),
          ...(!request.cursor ? { nextCursor: 'cursor-2' } : {}),
          items: [{ value: { kind: 'string', value: request.cursor ? 'second' : 'first' }, label: request.cursor ? 'Second' : 'First', null: false, available: true, selected: false }],
        } } }), request.cursor ? 150 : 30)
      })
      mergePatch({
        builder: { filters: [{ id: 'status', label: 'Status', dimension: 'orders.status', controlType: 'multiSelect', required: false, readerEditable: true, targets: [], bindings: [] }] },
        builderFilterContract: { applicationMode: 'immediate', definitions: { status: definition }, bindings: { [key]: binding } },
        builderFilterState: { revision: 1, appliedControls: { [key]: { expression: unfiltered, resolvedExpression: unfiltered } }, draftControls: {}, dirtyBindings: [], defaultsRevision: '1' },
      })
      await element.updateComplete
    })
    await page.getByRole('button', { name: 'Status: All', exact: true }).click()
    const options = page.getByRole('dialog', { name: 'Status filter options', exact: true })
    await options.getByRole('button', { name: 'Load more values', exact: true }).click()
    await options.getByRole('checkbox', { name: 'Second', exact: true }).waitFor({ timeout: 1500 })
    expect(await options.getByRole('checkbox', { name: 'First', exact: true }).isVisible()).toBe(true)
    const requests = await page.locator('lv-dashboard-builder').evaluate((element: any) => element.optionRequests)
    expect(requests).toHaveLength(2)
    expect(requests[1]).toMatchObject({ cursor: 'cursor-2', requestGeneration: 2 })
  } finally { await page.close() }
})

test('filter settings stay with their selected card and fit a narrow pane', async () => {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1050 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-dashboard-builder'))
    await page.locator('lv-dashboard-builder').evaluate(async (element: any) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ builder: { filters: ['First filter', 'Second filter'].map((label, index) => ({ id: `filter_${index}`, label, dimension: 'orders.status', controlType: 'multiSelect', required: false, readerEditable: true, targets: [], bindings: [] })) } })
      await element.updateComplete
    })
    await page.getByRole('button', { name: /First filter/ }).click()
    const editor = page.getByRole('region', { name: 'Configure First filter filter', exact: true })
    await editor.locator('summary').click()
    const metrics = await editor.evaluate((element) => {
      const right = element.getBoundingClientRect().right
      const controls = [...element.querySelectorAll('input[type=text],select,button,.filter-scope-option')]
      const scopes = [...element.querySelectorAll('.filter-scope-option')].map(x => x.getBoundingClientRect())
      return { overflows: controls.some(x => x.getBoundingClientRect().right > right + 1), stacked: scopes[1]!.top >= scopes[0]!.bottom }
    })
    expect(metrics).toEqual({ overflows: false, stacked: true })
    const editorBox = await editor.boundingBox()
    const nextBox = await page.getByRole('button', { name: /Second filter/ }).boundingBox()
    expect(nextBox!.y).toBeGreaterThanOrEqual(editorBox!.y + editorBox!.height - 1)
  } finally { await page.close() }
})
