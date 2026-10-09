import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.test-fixture'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

test('query audit distinguishes an unused history from filters with no matches', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1000, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
    const state = await page.evaluate(async () => {
      const element = document.querySelector('lv-admin-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const history = {
        table: { columns: [], rows: [], empty: 'No query events match these filters.' },
        filters: {}, nextCursor: '', loadedCountLabel: '0 queries loaded', hasMore: false, loading: false, error: '', limit: 50,
      }
      mergePatch({
        page: { kind: 'admin', title: 'Query history', active: 'queries', headerTitle: 'Query history', headerDetail: 'Inspect query activity.' },
        adminQueryHistory: history,
        adminQueryDetail: { eventId: '', loading: false, error: '' },
      })
      await element.updateComplete
      const table = element.shadowRoot?.querySelector('lv-record-table') as any
      await table.updateComplete
      const unused = table.table.empty
      mergePatch({ adminQueryHistory: { ...history, filters: { search: 'missing' } } })
      await element.updateComplete
      await table.updateComplete
      return { unused, filtered: table.table.empty }
    })

    expect(state).toEqual({
      unused: 'No query activity yet. Run a dashboard or agent query to see it here.',
      filtered: 'No query events match these filters.',
    })
  } finally {
    await page.close()
  }
})

test('query audit closes the column selector when interacting with the history filters', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
    await page.evaluate(async (fixture) => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: fixture, adminQueryHistory: fixture.queryHistory, adminQueryDetail: { eventId: '', loading: false, error: '' } })
      const element = document.createElement('lv-admin-page') as any
      document.body.replaceChildren(element)
      await element.updateComplete
      await (element.shadowRoot.querySelector('lv-record-table') as any).updateComplete
    }, {
      kind: 'admin', active: 'queries', title: 'Query history', headerTitle: 'Query history',
      queryHistory: {
        table: {
          columns: [{ id: 'query', header: 'Query', toggleable: false }, { id: 'started', header: 'Started' }, { id: 'runtime', header: 'Runtime' }],
          rows: [{ id: 'query-one', query: 'select 1', started: '2026-07-02', runtime: 'sales' }],
          columnSelector: { enabled: true, label: 'Columns', defaultColumns: ['started', 'runtime'] },
        },
        filters: {}, loadedCountLabel: '1 query loaded', hasMore: false, loading: false, error: '', limit: 50,
      },
    })

    const table = page.locator('lv-admin-page lv-record-table')
    await table.locator('.record-table-column-selector summary').click()
    await table.getByRole('checkbox', { name: 'Runtime', exact: true }).uncheck()
    expect(await table.locator('details').evaluate((element: HTMLDetailsElement) => element.open)).toBe(true)
    await page.locator('lv-admin-page').getByRole('button', { name: 'Last hour', exact: true }).click()
    await table.evaluate((element: any) => element.updateComplete)
    expect(await table.locator('details').evaluate((element: HTMLDetailsElement) => element.open)).toBe(false)
    expect(await table.locator('thead').textContent()).not.toContain('Runtime')
  } finally {
    await page.close()
  }
})
