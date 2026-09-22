import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

test('admin page does not expose the archived chat settings surface', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: {
          kind: 'admin', title: 'Archived chats', active: 'archived-chats', headerTitle: 'Archived chats', headerDetail: 'Manage archived conversations.',
        },
      })
      const admin = document.querySelector('lv-admin-page') as any
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await admin.updateComplete
      const root = admin.shadowRoot as ShadowRoot
      return {
        archivedElement: Boolean(root.querySelector('lv-archived-chats')),
        activeSettingsElement: Boolean(root.querySelector('lv-personal-settings, lv-product-settings')),
      }
    })
    expect(state).toEqual({ archivedElement: false, activeSettingsElement: false })
  } finally {
    await page.close()
  }
})
