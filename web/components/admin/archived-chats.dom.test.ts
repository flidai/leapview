import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

test('archived chat menu enters on the first action from its trigger', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-archived-chats'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: {
          kind: 'admin', title: 'Archived chats', active: 'archived-chats', headerTitle: 'Archived chats', headerDetail: 'Manage archived conversations.',
        },
        chatManagement: {
          action: '', conversationId: '', completedRequestId: '',
          archivedConversations: [{ id: 'archived-1', title: 'Last quarter', status: 'archived', updatedAt: '2026-09-17T00:45:00Z' }],
        },
      })
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const archived = (admin.shadowRoot as ShadowRoot).querySelector('lv-archived-chats') as any
      await archived.updateComplete
      const root = archived.shadowRoot as ShadowRoot
      const historyActions = Array.from(root.querySelectorAll('.history-action strong')).map((item) => item.textContent?.trim())
      const trigger = root.querySelector('summary') as HTMLElement
      trigger.focus()
      trigger.click()
      await archived.updateComplete
      trigger.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true }))
      return {
        historyActions,
        open: (root.querySelector('details') as HTMLDetailsElement).open,
        focusedAction: (root.activeElement as HTMLElement | null)?.textContent?.trim(),
      }
    })
    expect(state).toEqual({ historyActions: ['Archive all chats', 'Delete all chats'], open: true, focusedAction: 'Select' })
  } finally {
    await page.close()
  }
})
