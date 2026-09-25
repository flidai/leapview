import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

test('personal API tokens use the standard list with edit navigation and inline deletion', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 800 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const base = { permissionProfile: 'leapview.permissions/v1', permissions: [], capabilities: [], lastUsedAt: '', revokedAt: '' }
      mergePatch({ page: { kind: 'admin', title: 'API tokens', active: 'api-tokens', headerTitle: 'API tokens', headerDetail: '' }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { permissionOptionsReady: true, capabilities: [], newToken: 'lv_test_secret', items: [
          { ...base, id: 'active', name: 'Production sync', description: 'Nightly sync', createdAt: '2026-08-12T06:40:00Z', lastUsedAt: '2026-09-23T06:40:00Z', expiresAt: '2099-01-01T00:00:00Z' },
          { ...base, id: 'expired', name: 'Old export', description: '', createdAt: '2020-01-01T00:00:00Z', expiresAt: '2020-02-01T00:00:00Z' },
          { ...base, id: 'revoked', name: 'Retired job', description: 'No longer used', createdAt: '2026-08-12T06:40:00Z', expiresAt: '2099-01-01T00:00:00Z', revokedAt: '2026-09-20T06:40:00Z' },
        ] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      const list = root.querySelector('lv-entity-list') as HTMLElement & { updateComplete: Promise<unknown> }
      await list.updateComplete
      const table = list.querySelector('.entity-list-table') as HTMLTableElement
      const deleteButton = table.querySelector<HTMLButtonElement>('tbody tr:first-child .entity-list-row-action')
      const hasDeleteAction = deleteButton?.getAttribute('aria-label') === 'Delete Production sync'
      const editHref = table.querySelector<HTMLAnchorElement>('tbody tr:first-child .entity-list-identity')?.getAttribute('href')
      deleteButton?.click()
      await personal.updateComplete
      return {
        headers: Array.from(table.querySelectorAll('thead th')).map((cell) => cell.textContent?.trim()),
        rows: Array.from(table.querySelectorAll('tbody tr')).map((row) => ({
          name: row.querySelector('.entity-list-title')?.textContent?.trim(),
          cells: row.children.length,
          status: row.querySelector('td:nth-child(7) .entity-list-status > span:last-child')?.textContent?.trim(),
          created: row.querySelector<HTMLTimeElement>('td:nth-child(5) time')?.dateTime,
          modified: row.querySelector<HTMLTimeElement>('td:nth-child(6) time')?.dateTime,
        })),
        secretOutsideTable: Boolean(root.querySelector('.token-new-secret lv-one-time-secret')) && !table.querySelector('lv-one-time-secret'),
        hasCardRows: Boolean(root.querySelector('.token-list .token-row')),
        wideRoute: Boolean((admin.shadowRoot as ShadowRoot).querySelector('.main-token-list')),
        standardList: list.getAttribute('row-action') === 'open',
        hasDeleteAction,
        editHref,
        noTokenDrawer: !root.querySelector('lv-drawer'),
        deleteConfirmation: Boolean(root.querySelector('[data-token-delete-dialog]')),
      }
    })
    expect(state.headers).toEqual(['Token', 'Description', 'Permissions', 'Last used', 'Created', 'Last modified', 'Status', 'Actions'])
    expect(state.rows).toEqual([
      { name: 'Production sync', cells: 8, status: 'Active', created: '2026-08-12T06:40:00Z', modified: '2026-08-12T06:40:00Z' },
      { name: 'Old export', cells: 8, status: 'Expired', created: '2020-01-01T00:00:00Z', modified: '2020-01-01T00:00:00Z' },
      { name: 'Retired job', cells: 8, status: 'Revoked', created: '2026-08-12T06:40:00Z', modified: '2026-09-20T06:40:00Z' },
    ])
    expect(state.secretOutsideTable).toBe(true)
    expect(state.hasCardRows).toBe(false)
    expect(state.wideRoute).toBe(true)
    expect(state.standardList).toBe(true)
    expect(state.hasDeleteAction).toBe(true)
    expect(state.editHref).toBe('/admin/api-tokens/active/edit')
    expect(state.noTokenDrawer).toBe(true)
    expect(state.deleteConfirmation).toBe(true)
    await page.setViewportSize({ width: 390, height: 800 })
    const mobile = await page.locator('lv-personal-settings').evaluate((personal) => {
      const wrap = personal.shadowRoot?.querySelector('lv-entity-list .entity-list-table-wrap') as HTMLElement
      return { scrollWidth: wrap.scrollWidth, clientWidth: wrap.clientWidth }
    })
    expect(mobile.scrollWidth).toBeGreaterThan(mobile.clientWidth)
  } finally {
    await page.close()
  }
})

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
          value: 'RESOURCE_READ', actionLabel: 'Read dashboard', label: 'Read dashboard', description: 'Read this dashboard.', category: 'Dashboard',
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
      const permissionCell = root.querySelector('lv-entity-list .entity-list-table-row td:nth-child(3)')
      return {
        command,
        permissionSummary: permissionCell?.textContent?.replace(permissionCell.querySelector('.entity-list-mobile-cell-label')?.textContent ?? '', '').trim(),
        editHref: root.querySelector('lv-entity-list .entity-list-identity')?.getAttribute('href'),
      }
    })

    expect(state.command).toMatchObject({ action: 'create', name: 'Authentication-only automation', permissions: [] })
    expect(Date.parse((state.command as { expiresAt: string }).expiresAt)).toBeGreaterThan(Date.now())
    expect(state.permissionSummary).toBe('No permissions')
    expect(state.editHref).toBe('/admin/api-tokens/token-empty/edit')
  } finally {
    await page.close()
  }
})

test('token details open a prefilled edit form and submit a revision-bound update', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const modifiedAt = '2026-08-12T06:41:00Z'
      window.history.replaceState({}, '', '/admin/api-tokens/token-edit/edit')
      mergePatch({ page: { kind: 'admin', title: 'Edit personal access token', active: 'api-token-edit' }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { permissionOptionsReady: true, capabilities: [], items: [{
          id: 'token-edit', name: 'Original', description: 'Current use', modifiedAt, createdAt: '2026-08-12T06:40:00Z',
          permissionProfile: 'leapview.permissions/v1', permissions: [], capabilities: [], lastUsedAt: '', revokedAt: '', expiresAt: '2026-12-01T10:30:00Z',
        }] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      const name = root.querySelector('#token-name') as HTMLInputElement
      const populated = { name: name.value, description: (root.querySelector('#token-description') as HTMLTextAreaElement).value, expiry: (root.querySelector('#token-custom-expiration') as HTMLInputElement).value }
      name.value = 'Renamed'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await personal.updateComplete
      let command: any = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      return { populated, command, button: root.querySelector('.token-actions .primary')?.textContent?.trim() }
    })
    expect(state.populated).toEqual({ name: 'Original', description: 'Current use', expiry: '2026-12-01' })
    expect(state.command).toMatchObject({ action: 'update', tokenId: 'token-edit', expectedModifiedAt: '2026-08-12T06:41:00Z', name: 'Renamed', permissions: [], expiresAt: '2026-12-01T10:30:00Z' })
    expect(state.button).toBe('Saving…')
  } finally {
    await page.close()
  }
})

test('token rotation requires confirmation and submits the current revision', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      window.history.replaceState({}, '', '/admin/api-tokens/token-rotate/edit')
      mergePatch({ page: { kind: 'admin', title: 'Edit personal access token', active: 'api-token-edit' }, personalSettings: {
        active: 'api-tokens', profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { permissionOptionsReady: true, capabilities: [], items: [{
          id: 'token-rotate', name: 'Original', description: '', modifiedAt: '2026-08-12T06:41:00Z', createdAt: '2026-08-12T06:40:00Z',
          permissionProfile: 'leapview.permissions/v1', permissions: [], capabilities: [], lastUsedAt: '', revokedAt: '', expiresAt: '2026-12-01T10:30:00Z',
        }] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      const rotate = Array.from(root.querySelectorAll<HTMLButtonElement>('.token-actions button')).find((button) => button.textContent?.trim() === 'Rotate secret')
      rotate?.click()
      await personal.updateComplete
      const confirmation = Boolean(root.querySelector('[data-token-rotate-dialog]'))
      let command: any = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('[data-token-rotate-dialog] .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return { confirmation, command }
    })
    expect(state.confirmation).toBe(true)
    expect(state.command).toMatchObject({ action: 'rotate', tokenId: 'token-rotate', expectedModifiedAt: '2026-08-12T06:41:00Z' })
  } finally { await page.close() }
})

test('personal API token UI submits action-target pairs without a Cartesian expansion', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const target = { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_1' }
      const pipelineTarget = { scope: 'resource', projectId: 'project_1', resourceKind: 'pipeline', resourceId: 'pipeline_1' }
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-tokens', headerTitle: 'API tokens', headerDetail: 'Manage personal API and CLI credentials.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [
          { value: 'DASHBOARD_READ', actionLabel: 'View dashboard', label: 'Revenue dashboard', description: 'Dashboard · View dashboard', category: 'Dashboards', permissions: [{ action: 'dashboard.read', profile: 'leapview.permissions/v1', target }] },
          { value: 'PIPELINE_RUN', actionLabel: 'Run pipeline', label: 'Nightly refresh', description: 'Pipeline · Run pipeline', category: 'Pipelines', permissions: [{ action: 'pipeline.run', profile: 'leapview.permissions/v1', target: pipelineTarget }] },
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
      ;(root.querySelector('.permission-option') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      ;(root.querySelector('.permission-resource-option input[type="checkbox"]') as HTMLInputElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      await personal.updateComplete
      const selectedScope = root.querySelector('.permission-scope-trigger')?.textContent?.trim()
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
      const permissionCell = root.querySelector('lv-entity-list .entity-list-table-row td:nth-child(3)')
      return {
        command,
        selectedScope,
        summary: permissionCell?.textContent?.replace(permissionCell.querySelector('.entity-list-mobile-cell-label')?.textContent ?? '', '').trim(),
        inlineDetails: Boolean(root.querySelector('lv-entity-list details')),
      }
    })
    const command = state.command as { action: string, name: string, permissions: unknown[], expiresAt: string }
    expect(command).toMatchObject({
      action: 'create', name: 'Dashboard reader',
      permissions: [{ action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_1' } }],
    })
    expect(typeof command.expiresAt).toBe('string')
    expect(Date.parse(command.expiresAt)).toBeGreaterThan(Date.now())
    expect(state.selectedScope).toBe('Specific: Revenue dashboard')
    expect(state.summary).toBe('1 permission')
    expect(state.inlineDetails).toBe(false)
  } finally {
    await page.close()
  }
})

test('project-level token permissions show their fixed scope and submit the exact pair', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const permission = {
        action: 'delivery.rollback',
        profile: 'leapview.permissions/v1',
        target: { scope: 'project', projectId: 'project_1' },
      }
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-token-new', headerTitle: 'New personal access token', headerDetail: 'Create a scoped credential.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [{
          value: 'delivery-rollback', actionLabel: 'Roll back releases', label: 'Roll back releases', description: 'Current project', category: 'Project administration', permissions: [permission],
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
      name.value = 'Release rollback'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-option') as HTMLButtonElement).click()
      await personal.updateComplete
      const policy = root.querySelector('.permission-policy') as HTMLElement
      const presentation = {
        title: policy.querySelector('.permission-policy-header .settings-label')?.textContent?.trim(),
        scope: policy.querySelector('.permission-scope-fixed')?.textContent?.trim(),
        configured: policy.dataset.configured,
        hasScopeControls: Boolean(policy.querySelector('.permission-scope-trigger')),
      }
      let command: unknown = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return { presentation, command }
    })

    expect(state.presentation).toEqual({
      title: 'Roll back releases',
      scope: 'Current project',
      configured: 'true',
      hasScopeControls: false,
    })
    expect(state.command).toMatchObject({
      action: 'create',
      name: 'Release rollback',
      permissions: [{
        action: 'delivery.rollback',
        profile: 'leapview.permissions/v1',
        target: { scope: 'project', projectId: 'project_1' },
      }],
    })
    expect((state.command as { permissions: unknown[] }).permissions).toHaveLength(1)
  } finally {
    await page.close()
  }
})

test('permission picker opens above its trigger when the space below cannot fit its controls', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const layout = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-token-new', headerTitle: 'New personal access token', headerDetail: 'Create a scoped credential.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [{
          value: 'delivery-rollback', actionLabel: 'Roll back releases', label: 'Roll back releases', description: 'Current project', category: 'Project administration', permissions: [{
            action: 'delivery.rollback', profile: 'leapview.permissions/v1', target: { scope: 'project', projectId: 'project_1' },
          }],
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
      const picker = root.querySelector('.permission-picker') as HTMLElement
      picker.style.position = 'fixed'
      picker.style.right = '16px'
      picker.style.bottom = '16px'
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const trigger = (root.querySelector('.permission-trigger') as HTMLElement).getBoundingClientRect()
      const menu = root.querySelector('.permission-menu') as HTMLElement
      const menuRect = menu.getBoundingClientRect()
      const option = root.querySelector('.permission-option') as HTMLButtonElement
      const optionRect = option.getBoundingClientRect()
      return {
        placement: menu.dataset.placement,
        triggerGap: Math.round(trigger.top - menuRect.bottom),
        optionHeight: Math.round(optionRect.height),
        optionAtCenter: option.contains(root.elementFromPoint(optionRect.x + 20, optionRect.y + optionRect.height / 2)),
      }
    })

    expect(layout).toEqual({
      placement: 'above',
      triggerGap: 6,
      optionHeight: expect.any(Number),
      optionAtCenter: true,
    })
    expect(layout.optionHeight).toBeGreaterThanOrEqual(32)
  } finally {
    await page.close()
  }
})

test('custom permission picker keeps actions compact and shows scope in each selected row', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const profile = 'leapview.permissions/v1'
      mergePatch({ page: { kind: 'admin', title: 'API tokens', active: 'api-token-new', headerTitle: 'New personal access token', headerDetail: '' }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [
          { value: 'source-create', actionLabel: 'Create sources', actionDescription: 'Create a Source in this project.', label: 'Create sources', description: 'Current project', category: 'Sources', permissions: [{ action: 'source.create', profile, target: { scope: 'project', projectId: 'project_1' } }] },
          { value: 'source-read', actionLabel: 'View source', actionDescription: 'Read a Source definition.', label: 'Revenue source', description: 'Sources · View source', category: 'Sources', permissions: [{ action: 'source.read', profile, target: { scope: 'resource', projectId: 'project_1', resourceKind: 'source', resourceId: 'source_1' } }] },
          { value: 'source-update', actionLabel: 'Edit source', actionDescription: 'Edit a Source definition.', label: 'Revenue source', description: 'Sources · Edit source', category: 'Sources', permissions: [{ action: 'source.update', profile, target: { scope: 'resource', projectId: 'project_1', resourceKind: 'source', resourceId: 'source_1' } }] },
        ] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      personal.style.setProperty('--lv-bg-page', '#101010')
      personal.style.setProperty('--lv-bg-panel', '#202020')
      personal.tokenView = 'create'
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      const name = root.querySelector('#token-name') as HTMLInputElement
      name.value = 'Source reader'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await personal.updateComplete
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      const menu = root.querySelector('.permission-menu') as HTMLElement
      const menuState = {
        width: Math.round(menu.getBoundingClientRect().width),
        height: Math.round(menu.getBoundingClientRect().height),
        optionFontSize: getComputedStyle(menu.querySelector('.permission-option') as HTMLElement).fontSize,
        checkboxCount: menu.querySelectorAll('.permission-option input[type="checkbox"]').length,
        hasHelp: Boolean(menu.querySelector('.permission-menu-help')),
        hasSelectedCounts: Boolean(menu.querySelector('.permission-menu-count, .permission-category-count')),
        optionDescriptions: menu.querySelectorAll('.permission-option .settings-description').length,
      }
      ;(root.querySelector('[data-policy*="source.create"]') as HTMLButtonElement).click()
      await personal.updateComplete
      const projectRow = root.querySelector('.permission-policy') as HTMLElement
      const projectScope = projectRow.querySelector('.permission-scope-fixed')?.textContent?.trim()
      const menuRemainsOpen = Boolean(root.querySelector('.permission-menu'))
      const projectChecked = (root.querySelector('[data-policy*="source.create"] input') as HTMLInputElement).checked
      ;(root.querySelector('[data-policy*="source.read"]') as HTMLButtonElement).click()
      await personal.updateComplete
      let resourceRow = Array.from(root.querySelectorAll<HTMLElement>('.permission-policy')).find((row) => row.dataset.permission?.includes('source.read')) as HTMLElement
      const resourceDescription = resourceRow.querySelector('.permission-policy-description')?.textContent?.trim()
      const needsScope = resourceRow.querySelector('.permission-scope-trigger')?.textContent?.trim()
      const resourceChecked = (root.querySelector('[data-policy*="source.read"] input') as HTMLInputElement).checked
      ;(root.querySelector('[data-policy*="source.update"]') as HTMLElement).click()
      await personal.updateComplete
      resourceRow = Array.from(root.querySelectorAll<HTMLElement>('.permission-policy')).find((row) => row.dataset.permission?.includes('source.read')) as HTMLElement
      const secondResourcePending = Array.from(root.querySelectorAll<HTMLElement>('.permission-policy')).some((row) => row.dataset.permission?.includes('source.update') && row.dataset.configured === 'false')
      const generationBlocked = (root.querySelector('.token-actions button[type="submit"]') as HTMLButtonElement).disabled
      ;(resourceRow.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      const oneScopePicker = root.querySelectorAll('.permission-scope-menu').length === 1 && !root.querySelector('.permission-resource-trigger')
      ;(root.querySelector('.permission-scope-option input[value="current"]') as HTMLInputElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      await personal.updateComplete
      resourceRow = Array.from(root.querySelectorAll<HTMLElement>('.permission-policy')).find((row) => row.dataset.permission?.includes('source.read')) as HTMLElement
      const configuredScope = resourceRow.querySelector('.permission-scope-trigger')?.textContent?.trim()
      const surface = {
        frame: getComputedStyle(root.querySelector('.permissions-card') as HTMLElement).backgroundColor,
        header: getComputedStyle(root.querySelector('.permissions-header') as HTMLElement).backgroundColor,
        rows: Array.from(root.querySelectorAll<HTMLElement>('.permission-policy')).map((row) => getComputedStyle(row).backgroundColor),
        technicalFooter: Boolean(root.querySelector('.permission-technical-details')),
      }
      const stillBlocked = (root.querySelector('.token-actions button[type="submit"]') as HTMLButtonElement).disabled
      const actionsBeforeRemove = personal.tokenSelectedPermissions.map((pair: any) => pair.action)
      const updateRow = Array.from(root.querySelectorAll<HTMLElement>('.permission-policy')).find((row) => row.dataset.permission?.includes('source.update')) as HTMLElement
      ;(updateRow.querySelector('.permission-policy-remove') as HTMLButtonElement).click()
      await personal.updateComplete
      const generationEnabled = !(root.querySelector('.token-actions button[type="submit"]') as HTMLButtonElement).disabled
      const generationState = { incomplete: personal.tokenPermissionsIncomplete, actionsBeforeRemove, actions: personal.tokenSelectedPermissions.map((pair: any) => pair.action) }
      ;(resourceRow.querySelector('.permission-policy-remove') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      const resourceUnchecked = !(root.querySelector('[data-policy*="source.read"] input') as HTMLInputElement).checked
      const resourceRowRemoved = !Array.from(root.querySelectorAll<HTMLElement>('.permission-policy')).some((row) => row.dataset.permission?.includes('source.read'))
      return { menuState, projectScope, menuRemainsOpen, projectChecked, resourceDescription, needsScope, resourceChecked, secondResourcePending, generationBlocked, oneScopePicker, configuredScope, surface, stillBlocked, generationEnabled, generationState, resourceUnchecked, resourceRowRemoved }
    })
    expect(state.menuState.width).toBe(320)
    expect(state.menuState.height).toBeLessThanOrEqual(320)
    expect(state.menuState.optionFontSize).toBe('14px')
    expect(state.menuState.checkboxCount).toBe(3)
    expect(state.menuState.hasHelp).toBe(false)
    expect(state.menuState.hasSelectedCounts).toBe(false)
    expect(state.menuState.optionDescriptions).toBe(0)
    expect(state.projectScope).toBe('Current project')
    expect(state.menuRemainsOpen).toBe(true)
    expect(state.projectChecked).toBe(true)
    expect(state.resourceDescription).toBe('Read a Source definition.')
    expect(state.needsScope).toBe('Choose scope')
    expect(state.resourceChecked).toBe(true)
    expect(state.secondResourcePending).toBe(true)
    expect(state.generationBlocked).toBe(true)
    expect(state.oneScopePicker).toBe(true)
    expect(state.configuredScope).toBe('All current sources (1)')
    expect(state.surface).toEqual({ frame: 'rgb(32, 32, 32)', header: 'rgb(32, 32, 32)', rows: ['rgb(16, 16, 16)', 'rgb(16, 16, 16)', 'rgb(16, 16, 16)'], technicalFooter: false })
    expect(state.stillBlocked).toBe(true)
    expect({ enabled: state.generationEnabled, state: state.generationState }).toEqual({ enabled: true, state: { incomplete: false, actionsBeforeRemove: ['source.create', 'source.read'], actions: ['source.create', 'source.read'] } })
    expect(state.resourceUnchecked).toBe(true)
    expect(state.resourceRowRemoved).toBe(true)
    await page.setViewportSize({ width: 390, height: 800 })
    const mobileMenu = await page.evaluate(async () => {
      const admin = document.querySelector('lv-admin-page') as any
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      const root = personal.shadowRoot as ShadowRoot
      await personal.updateComplete
      const rect = (root.querySelector('.permission-menu') as HTMLElement).getBoundingClientRect()
      return { left: Math.round(rect.left), right: Math.round(rect.right) }
    })
    expect(mobileMenu.left).toBeGreaterThanOrEqual(0)
    expect(mobileMenu.right).toBeLessThanOrEqual(390)
    const mobileScope = await page.evaluate(async () => {
      const admin = document.querySelector('lv-admin-page') as any
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      const root = personal.shadowRoot as ShadowRoot
      ;(root.querySelector('[data-policy*="source.read"]') as HTMLElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      const rect = (root.querySelector('.permission-scope-menu') as HTMLElement).getBoundingClientRect()
      return { left: rect.left, right: rect.right, bottom: rect.bottom, hasFooter: Boolean(root.querySelector('.permission-scope-menu-footer, .permission-scope-apply')) }
    })
    expect(mobileScope.left).toBeGreaterThanOrEqual(0)
    expect(mobileScope.right).toBeLessThanOrEqual(390)
    expect(mobileScope.bottom).toBeLessThanOrEqual(800)
    expect(mobileScope.hasFooter).toBe(false)
  } finally {
    await page.close()
  }
})

test('personal API token UI submits a future-resource scope only when the server offers it', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const profile = 'leapview.permissions/v1'
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-token-new', headerTitle: 'New personal access token', headerDetail: 'Create a scoped credential.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [
          { value: 'dashboard-1', actionLabel: 'View dashboard', label: 'Revenue dashboard', description: 'Dashboard · View dashboard', category: 'Dashboards', permissions: [{ action: 'dashboard.read', profile, target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_1' } }] },
          { value: 'dashboard-future', actionLabel: 'View dashboard', label: 'View dashboard', description: 'Current and future dashboards', category: 'Dashboards', permissions: [{ action: 'dashboard.read', profile, target: { scope: 'project', projectId: 'project_1', resourceKind: 'dashboard', includeFuture: true } }] },
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
      const name = root.querySelector('#token-name') as HTMLInputElement
      name.value = 'Dashboard monitor'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-option') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      ;(root.querySelector('.permission-scope-option input[value="future"]') as HTMLInputElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      await personal.updateComplete
      const summary = root.querySelector('.permission-scope-trigger')?.textContent?.trim()
      let command: unknown = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return { command, summary }
    })

    expect(state.summary).toBe('All current and future dashboards')
    expect(state.command).toMatchObject({
      action: 'create',
      permissions: [{
        action: 'dashboard.read',
        profile: 'leapview.permissions/v1',
        target: { scope: 'project', projectId: 'project_1', resourceKind: 'dashboard', includeFuture: true },
      }],
    })
    expect((state.command as { permissions: unknown[] }).permissions).toHaveLength(1)
  } finally {
    await page.close()
  }
})

test('personal token permission selections follow exact pairs through capability reorder and insertion', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const profile = 'leapview.permissions/v1'
      const capability = (resourceId: string, label: string) => ({
        value: resourceId,
        actionLabel: 'View dashboard',
        label,
        description: `Dashboard · ${label}`,
        category: 'Dashboards',
        permissions: [{ action: 'dashboard.read', profile, target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId } }],
      })
      const initial = [capability('dashboard_a', 'Dashboard A'), capability('dashboard_b', 'Dashboard B')]
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-token-new', headerTitle: 'New personal access token', headerDetail: 'Create a scoped credential.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: initial },
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
      name.value = 'Dashboard B reader'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-option') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      const dashboardB = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).find((option) => option.textContent?.includes('Dashboard B'))
      ;(dashboardB?.querySelector('input') as HTMLInputElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      await personal.updateComplete
      // Inserting a new pair before B changes the old positional identity from B to C.
      mergePatch({ personalSettings: { tokens: { capabilities: [
        capability('dashboard_b', 'Dashboard B'),
        capability('dashboard_c', 'Dashboard C'),
        capability('dashboard_a', 'Dashboard A'),
      ] } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await personal.updateComplete
      const selectedResources = [root.querySelector('.permission-scope-trigger')?.textContent?.trim()]
      let command: unknown = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return { selectedResources, command }
    })
    expect(state.selectedResources).toEqual(['Specific: Dashboard B'])
    expect(state.command).toMatchObject({
      action: 'create',
      name: 'Dashboard B reader',
      permissions: [{
        action: 'dashboard.read',
        profile: 'leapview.permissions/v1',
        target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_b' },
      }],
    })
  } finally {
    await page.close()
  }
})

test('personal token permission reconciliation drops a removed pair before submission', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const profile = 'leapview.permissions/v1'
      const capability = (resourceId: string, label: string) => ({
        value: resourceId,
        actionLabel: 'View dashboard',
        label,
        description: `Dashboard · ${label}`,
        category: 'Dashboards',
        permissions: [{ action: 'dashboard.read', profile, target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId } }],
      })
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-token-new', headerTitle: 'New personal access token', headerDetail: 'Create a scoped credential.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [capability('dashboard_a', 'Dashboard A'), capability('dashboard_b', 'Dashboard B')] },
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
      name.value = 'Dashboard reader'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-option') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      const dashboardB = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).find((option) => option.textContent?.includes('Dashboard B'))
      ;(dashboardB?.querySelector('input') as HTMLInputElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      await personal.updateComplete
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      mergePatch({ personalSettings: { tokens: { capabilities: [capability('dashboard_a', 'Dashboard A')] } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await personal.updateComplete
      const scopeFooterVisible = Boolean(root.querySelector('.permission-scope-menu-footer, .permission-scope-apply'))
      const remainingSelectedResources = root.querySelectorAll('.permission-resource-option input:checked').length
      const removedPairConfigured = root.querySelector('.permission-policy')?.getAttribute('data-configured')
      ;(root.querySelector('.permission-policy-remove') as HTMLButtonElement).click()
      await personal.updateComplete
      let command: unknown = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return { scopeFooterVisible, remainingSelectedResources, removedPairConfigured, command }
    })
    expect(state.scopeFooterVisible).toBe(false)
    expect(state.remainingSelectedResources).toBe(0)
    expect(state.removedPairConfigured).toBe('false')
    expect(state.command).toMatchObject({ action: 'create', name: 'Dashboard reader', permissions: [] })
  } finally {
    await page.close()
  }
})

test('token permission picker reflects controlled pairs and emits exact pair changes', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const profile = 'leapview.permissions/v1'
      const pair = (resourceId: string) => ({
        action: 'dashboard.read', profile,
        target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId },
      })
      const permissionA = pair('dashboard_a')
      const permissionB = pair('dashboard_b')
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-token-new', headerTitle: 'New personal access token', headerDetail: 'Create a scoped credential.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [
          { value: 'dashboard-a', actionLabel: 'View dashboard', label: 'Dashboard A', description: 'Dashboard · A', category: 'Dashboards', permissions: [permissionA] },
          { value: 'dashboard-b', actionLabel: 'View dashboard', label: 'Dashboard B', description: 'Dashboard · B', category: 'Dashboards', permissions: [permissionB] },
        ] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      personal.tokenView = 'create'
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      personal.tokenSelectedPermissions = [permissionA]
      await personal.updateComplete
      const picker = root.querySelector('lv-personal-token-permission-picker') as any
      await picker.updateComplete
      const selectedAtStart = [root.querySelector('.permission-scope-trigger')?.textContent?.trim()]
      const changes: unknown[][] = []
      picker.addEventListener('lv-personal-token-permissions-change', (event: CustomEvent) => changes.push(event.detail.permissions))
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await picker.updateComplete
      const compactScope = Boolean(root.querySelector('.permission-resource-option input:checked'))
      const initiallyChecked = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).filter((option) => (option.querySelector('input') as HTMLInputElement).checked).map((option) => option.textContent?.trim())
      const optionB = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).find((option) => option.textContent?.includes('Dashboard B'))
      ;(optionB?.querySelector('input') as HTMLInputElement).click()
      await picker.updateComplete
      const emittedImmediately = changes.length
      const firstSelection = changes.at(-1) ?? []
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      await picker.updateComplete
      const selectedAfterClose = personal.tokenSelectedPermissions.map((permission: any) => permission.target.resourceId)
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await picker.updateComplete
      const checkedAfterClose = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).filter((option) => (option.querySelector('input') as HTMLInputElement).checked).map((option) => option.textContent?.trim())
      const parentSelected = [...personal.tokenSelectedPermissions]
      const resourceOptions = () => Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option'))
      ;(resourceOptions().find((option) => option.textContent?.includes('Dashboard B'))?.querySelector('input') as HTMLInputElement).click()
      await picker.updateComplete
      ;(resourceOptions().find((option) => option.textContent?.includes('Dashboard A'))?.querySelector('input') as HTMLInputElement).click()
      await picker.updateComplete
      await personal.updateComplete
      return {
        compactScope,
        selectedAtStart,
        initiallyChecked,
        emittedImmediately,
        selectedAfterClose,
        checkedAfterClose,
        emitted: firstSelection,
        parentSelected,
        afterClearing: { permissions: personal.tokenSelectedPermissions, incomplete: personal.tokenPermissionsIncomplete, configured: root.querySelector('.permission-policy')?.getAttribute('data-configured') },
      }
    })

    expect(state.compactScope).toBe(true)
    expect(state.selectedAtStart).toEqual(['Specific: Dashboard A'])
    expect(state.initiallyChecked).toEqual(['Dashboard A'])
    expect(state.emittedImmediately).toBe(1)
    expect(state.selectedAfterClose).toEqual(['dashboard_a', 'dashboard_b'])
    expect(state.checkedAfterClose).toEqual(['Dashboard A', 'Dashboard B'])
    expect(state.emitted).toEqual([
      { action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_a' } },
      { action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_b' } },
    ])
    expect(state.parentSelected).toEqual(state.emitted)
    expect(state.afterClearing).toEqual({ permissions: [], incomplete: true, configured: 'false' })
  } finally {
    await page.close()
  }
})

test('adding an authorized resource after all-current selection never widens the token', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 700 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const profile = 'leapview.permissions/v1'
      const capability = (resourceId: string) => ({
        value: resourceId, actionLabel: 'View dashboard', label: `Dashboard ${resourceId.slice(-1).toUpperCase()}`,
        description: 'Dashboard · View dashboard', category: 'Dashboards',
        permissions: [{ action: 'dashboard.read', profile, target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId } }],
      })
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-token-new', headerTitle: 'New personal access token', headerDetail: 'Create a scoped credential.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [capability('dashboard_a'), capability('dashboard_b')] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      personal.tokenView = 'create'
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      const name = root.querySelector('#token-name') as HTMLInputElement
      name.value = 'Current dashboards reader'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-option') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      ;(root.querySelector('.permission-scope-option input[value="current"]') as HTMLInputElement).click()
      await (root.querySelector('lv-personal-token-permission-picker') as any).updateComplete
      await personal.updateComplete

      mergePatch({ personalSettings: { tokens: { capabilities: [capability('dashboard_a'), capability('dashboard_b'), capability('dashboard_c')] } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await personal.updateComplete
      const picker = root.querySelector('lv-personal-token-permission-picker') as any
      await picker.updateComplete
      const checkedScope = root.querySelector('.permission-scope-trigger')?.textContent?.trim()
      ;(root.querySelector('.permission-scope-trigger') as HTMLButtonElement).click()
      await picker.updateComplete
      const selectedResources = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).filter((option) => (option.querySelector('input') as HTMLInputElement).checked).map((option) => option.textContent?.trim())
      let command: unknown = null
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return { checkedScope, selectedResources, command }
    })

    expect(state.checkedScope).toBe('Specific: 2 dashboards')
    expect(state.selectedResources.sort()).toEqual(['Dashboard A', 'Dashboard B'])
    expect(state.command).toMatchObject({ action: 'create', name: 'Current dashboards reader', permissions: [
      { action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { resourceId: 'dashboard_a' } },
      { action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { resourceId: 'dashboard_b' } },
    ] })
    expect((state.command as { permissions: unknown[] }).permissions).toHaveLength(2)
  } finally {
    await page.close()
  }
})

test('token creation opens directly with the scoped permission picker', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const permission = { action: 'dashboard.read', profile: 'leapview.permissions/v1',
        target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_1' } }
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-token-new', headerTitle: 'New personal access token', headerDetail: 'Create a scoped credential.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], permissionOptionsReady: true, capabilities: [{
          value: 'dashboard.read', actionLabel: 'View dashboard', label: 'Dashboard 1', description: 'Dashboard', category: 'Dashboards', permissions: [permission],
        }] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      personal.tokenView = 'create'
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      personal.style.setProperty('--lv-bg-page', 'rgb(16, 16, 16)')
      personal.style.setProperty('--lv-bg-input', 'rgb(32, 32, 32)')
      const emptyHeight = Math.round((root.querySelector('.selected-permissions-empty') as HTMLElement).getBoundingClientRect().height)
      return {
        presetsVisible: Boolean(root.querySelector('[data-token-preset], .token-task-templates, .token-preset-summary')),
        pickerVisible: Boolean(root.querySelector('.permission-trigger')),
        emptyHeight,
        namePlaceholder: (root.querySelector('#token-name') as HTMLInputElement).placeholder,
        descriptionPlaceholder: (root.querySelector('#token-description') as HTMLTextAreaElement).placeholder,
        fieldHelp: Array.from(root.querySelectorAll('label.token-field > .settings-description')).map((element) => element.textContent?.trim()),
        nameBackground: getComputedStyle(root.querySelector('#token-name') as HTMLInputElement).backgroundColor,
        descriptionBackground: getComputedStyle(root.querySelector('#token-description') as HTMLTextAreaElement).backgroundColor,
      }
    })
    expect(state).toEqual({ presetsVisible: false, pickerVisible: true, emptyHeight: 250, namePlaceholder: '', descriptionPlaceholder: '', fieldHelp: [
      'A unique name for this token. It may be visible to administrators.',
      'Explain what this token will be used for so you can identify it later.',
    ], nameBackground: 'rgb(16, 16, 16)', descriptionBackground: 'rgb(16, 16, 16)' })
  } finally {
    await page.close()
  }
})
