import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => { fixture = await startAdminPageTestFixture() })
afterAll(async () => { if (fixture) await stopAdminPageTestFixture(fixture) }, 15_000)

test('profile avatar upload surfaces backend validation details', async () => {
  const page = await fixture.browser.newPage()
  try {
    await page.route('**/profile/avatar', (route) => route.fulfill({
      status: 422,
      headers: { 'content-type': 'application/problem+json' },
      body: JSON.stringify({ detail: 'Avatar uploads require image/jpeg, image/png, or image/webp' }),
    }))
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-personal-settings'))
    await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: { kind: 'admin', title: 'Profile', active: 'profile', headerTitle: 'Profile', headerDetail: '' }, personalSettings: {
        active: 'profile',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', avatarUrl: '', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], capabilities: [] },
      } })
      window.LeapViewCommand = { headers: () => ({}) }
    })
    const input = page.locator('lv-personal-settings').locator('input.avatar-input')
    await input.setInputFiles({ name: 'avatar.svg', mimeType: 'image/svg+xml', buffer: Buffer.from('<svg></svg>') })
    const alert = page.locator('lv-personal-settings').locator('[role="alert"]')
    await alert.waitFor()
    expect(await alert.textContent()).toBe('Avatar uploads require image/jpeg, image/png, or image/webp')
  } finally {
    await page.close()
  }
})
