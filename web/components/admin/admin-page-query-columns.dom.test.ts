import { afterAll, beforeAll, expect, test } from 'bun:test'
import { expect as browserExpect } from '@playwright/test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.test-fixture'
import { queryAuditFixturePage } from './admin-page-query.test-fixture'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

for (const theme of ['light', 'dark'] as const) {
  test(`query history Columns menu dismisses outside its shadow root and cleans up listeners (${theme})`, async () => {
    const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 900 }, colorScheme: theme })
    try {
      await page.goto(fixture.baseURL)
      await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
      await page.evaluate(async ({ fixture, theme }) => {
        // Use the shipped theme tokens rather than this fixture's light-only overrides.
        document.head.querySelectorAll('style').forEach(style => style.remove())
        document.documentElement.dataset.colorMode = theme
        document.documentElement.dataset.lightTheme = 'light'
        document.documentElement.dataset.darkTheme = 'dark'
        const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
        mergePatch({ page: fixture, adminQueryHistory: fixture.queryHistory, adminQueryDetail: { eventId: '', loading: false, error: '' } })
        const element = document.querySelector('lv-admin-page') as any
        await element.updateComplete
      }, { fixture: queryAuditFixturePage(), theme })
      await page.addStyleTag({ path: 'static/app.css' })
      const table = page.locator('lv-admin-page lv-record-table')
      const selector = table.locator('.record-table-column-selector')
      const trigger = selector.locator('summary')
      await trigger.click()
      await browserExpect(selector).toHaveAttribute('open', '')
      // With only two rows, the menu extends below the panel's footer.
      // Its last option must remain reachable rather than being clipped.
      await browserExpect(selector.getByRole('checkbox', { name: 'Error', exact: true })).toBeInViewport({ timeout: 1_000 })
      await selector.getByRole('checkbox', { name: 'Error', exact: true }).check()
      await browserExpect(table.getByRole('button', { name: 'Sort by Error', exact: true })).toHaveCount(1)

      // Inside controls are retargeted to lv-admin-page at document level.
      // They must keep the menu open and still update column visibility.
      await selector.getByRole('checkbox', { name: 'Runtime', exact: true }).uncheck()
      await browserExpect(selector).toHaveAttribute('open', '')
      await browserExpect(table.getByRole('button', { name: 'Sort by Runtime', exact: true })).toHaveCount(0)
      await page.locator('lv-admin-page #query-filter-search').click()
      await browserExpect(selector).not.toHaveAttribute('open', '')
      await browserExpect(page.locator('lv-admin-page #query-filter-search')).toBeFocused()

      await trigger.click()
      await browserExpect(selector).toHaveAttribute('open', '')
      await selector.getByRole('checkbox', { name: 'Runtime', exact: true }).focus()
      await page.keyboard.press('Escape')
      await browserExpect(selector).not.toHaveAttribute('open', '')
      await browserExpect(trigger).toBeFocused()
      await trigger.click()
      await browserExpect(selector).toHaveAttribute('open', '')
      await trigger.click()
      await browserExpect(selector).not.toHaveAttribute('open', '')

      await trigger.click()
      const detachedStillOpen = await table.evaluate(async (element: any) => {
        const parent = element.parentElement!
        element.remove()
        document.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, composed: true }))
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true }))
        await element.updateComplete
        const stillOpen = element.querySelector('details').open
        parent.append(element)
        await element.updateComplete
        return stillOpen
      })
      expect(detachedStillOpen).toBe(true)
      // Reconnection installs dismissal again.
      await page.locator('lv-admin-page #query-filter-search').click()
      await browserExpect(selector).not.toHaveAttribute('open', '')
    } finally {
      await page.close()
    }
  })
}
