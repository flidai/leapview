import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

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
