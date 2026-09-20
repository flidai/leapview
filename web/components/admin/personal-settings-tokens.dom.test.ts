import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

test('personal API tokens persist and render an explicit empty authority list', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 800 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-tokens', headerTitle: 'API tokens', headerDetail: 'Manage personal API and CLI credentials.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [{
          value: 'RESOURCE_READ', label: 'Read dashboard', description: 'Read this dashboard.', category: 'Dashboard',
          permissions: [{ action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_1' } }],
        }] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      personal.tokenView = 'create'
      await personal.updateComplete
      const name = root.querySelector('#token-name') as HTMLInputElement
      name.value = 'Authentication-only automation'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await personal.updateComplete

      let command: any = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete

      mergePatch({ personalSettings: { tokens: { items: [
        { id: 'token-empty', name: 'Authentication-only automation', permissionProfile: 'leapview.permissions/v1', permissions: [], capabilities: [], createdAt: '2026-08-12T06:40:00Z', lastUsedAt: '', expiresAt: '', revokedAt: '' },
      ] } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      personal.tokenView = 'list'
      await personal.updateComplete
      return {
        command,
        tokenSummary: root.querySelector('.token-meta')?.textContent?.replace(/\s+/g, ' ').trim(),
      }
    })

    expect(state.command).toMatchObject({ action: 'create', name: 'Authentication-only automation', permissions: [] })
    expect(Date.parse((state.command as { expiresAt: string }).expiresAt)).toBeGreaterThan(Date.now())
    expect(state.tokenSummary).toContain('No project or resource authority')
  } finally {
    await page.close()
  }
})

test('personal API token UI submits action-target pairs without a Cartesian expansion', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const target = { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_1' }
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-tokens', headerTitle: 'API tokens', headerDetail: 'Manage personal API and CLI credentials.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [
          { value: 'RESOURCE_USE', label: 'Use dashboard', description: 'Use this dashboard.', category: 'Dashboard', permissions: [{ action: 'dashboard.read', profile: 'leapview.permissions/v1', target }] },
          { value: 'RESOURCE_READ', label: 'Read dashboard', description: 'Read this dashboard.', category: 'Dashboard', permissions: [{ action: 'dashboard.read', profile: 'leapview.permissions/v1', target }] },
        ] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      personal.tokenView = 'create'
      await personal.updateComplete
      ;(root.querySelector('#token-name') as HTMLInputElement).value = 'Dashboard reader'
      ;(root.querySelector('#token-name') as HTMLInputElement).dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('input[type="checkbox"][value="typed-0"]') as HTMLInputElement).click()
      await personal.updateComplete
      let command: unknown = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      mergePatch({ personalSettings: { tokens: { items: [
        { id: 'token-typed', name: 'Dashboard reader', permissionProfile: 'leapview.permissions/v1', permissions: [{ action: 'dashboard.read', profile: 'leapview.permissions/v1', target }], capabilities: [], createdAt: '2026-08-12T06:40:00Z', lastUsedAt: '', expiresAt: '', revokedAt: '' },
      ] } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      personal.tokenView = 'list'
      await personal.updateComplete
      return {
        command,
        summary: root.querySelector('.token-meta')?.textContent?.replace(/\s+/g, ' ').trim(),
      }
    })
    const command = state.command as { action: string, name: string, permissions: unknown[], expiresAt: string }
    expect(command).toMatchObject({
      action: 'create', name: 'Dashboard reader',
      permissions: [{ action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_1' } }],
    })
    expect(typeof command.expiresAt).toBe('string')
    expect(Date.parse(command.expiresAt)).toBeGreaterThan(Date.now())
    expect(state.summary).toContain('dashboard.read · dashboard dashboard_1')
  } finally {
    await page.close()
  }
})
