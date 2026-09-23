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
      ;(root.querySelector('.permission-resource-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-resource-option input[type="checkbox"]') as HTMLInputElement).click()
      await personal.updateComplete
      ;(root.querySelector('.permission-resource-menu-footer .primary') as HTMLButtonElement).click()
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
    expect(state.summary).toContain('Revenue dashboard · Dashboard · View dashboard')
    expect(state.summary).not.toContain('dashboard.read')
  } finally {
    await page.close()
  }
})

test('project-level token permissions explain their fixed scope and submit the exact pair', async () => {
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
        category: policy.querySelector('.permission-policy-header .settings-description')?.textContent?.trim(),
        scope: policy.querySelector('.permission-fixed-scope .settings-label')?.textContent?.trim(),
        explanation: policy.querySelector('.permission-fixed-scope .settings-description')?.textContent?.trim(),
        status: policy.querySelector('.permission-policy-ready')?.textContent?.trim(),
        configured: policy.dataset.configured,
        hasScopeControls: Boolean(policy.querySelector('input[type="radio"], .permission-resource-trigger')),
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
      category: 'Project administration',
      scope: 'Current project',
      explanation: 'This action applies to the current project and cannot be narrowed to an individual resource.',
      status: 'Ready',
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
    expect(layout.optionHeight).toBeGreaterThanOrEqual(48)
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
      ;(root.querySelector('input[type="radio"][value="future"]') as HTMLInputElement).click()
      await personal.updateComplete
      const summary = root.querySelector('.permission-scope-option:has(input[value="future"]:checked) .settings-label')?.textContent?.trim()
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
      ;(root.querySelector('.permission-resource-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      const dashboardB = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).find((option) => option.textContent?.includes('Dashboard B'))
      ;(dashboardB?.querySelector('input') as HTMLInputElement).click()
      await personal.updateComplete
      // Inserting a new pair before B changes the old positional identity from B to C.
      mergePatch({ personalSettings: { tokens: { capabilities: [
        capability('dashboard_b', 'Dashboard B'),
        capability('dashboard_c', 'Dashboard C'),
        capability('dashboard_a', 'Dashboard A'),
      ] } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await personal.updateComplete
      const selectedResources = Array.from(root.querySelectorAll('.permission-resource-chip')).map((chip) => chip.textContent?.replace(/\s+/g, ' ').trim())
      let command: unknown = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return { selectedResources, command }
    })
    expect(state.selectedResources).toEqual(['Dashboard B'])
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
      ;(root.querySelector('.permission-resource-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      const dashboardB = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).find((option) => option.textContent?.includes('Dashboard B'))
      ;(dashboardB?.querySelector('input') as HTMLInputElement).click()
      await personal.updateComplete
      mergePatch({ personalSettings: { tokens: { capabilities: [capability('dashboard_a', 'Dashboard A')] } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await personal.updateComplete
      const removedSelectionCount = root.querySelectorAll('.permission-resource-chip').length
      const removedPairConfigured = root.querySelector('.permission-policy')?.getAttribute('data-configured')
      ;(root.querySelector('.permission-policy-remove') as HTMLButtonElement).click()
      await personal.updateComplete
      let command: unknown = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return { removedSelectionCount, removedPairConfigured, command }
    })
    expect(state.removedSelectionCount).toBe(0)
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
      const selectedAtStart = Array.from(root.querySelectorAll('.permission-resource-chip')).map((chip) => chip.textContent?.replace(/\s+/g, ' ').trim())
      const changes: unknown[][] = []
      picker.addEventListener('lv-personal-token-permissions-change', (event: CustomEvent) => changes.push(event.detail.permissions))
      ;(root.querySelector('.permission-resource-trigger') as HTMLButtonElement).click()
      await picker.updateComplete
      const initiallyChecked = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).filter((option) => (option.querySelector('input') as HTMLInputElement).checked).map((option) => option.textContent?.trim())
      const optionB = Array.from(root.querySelectorAll<HTMLElement>('.permission-resource-option')).find((option) => option.textContent?.includes('Dashboard B'))
      ;(optionB?.querySelector('input') as HTMLInputElement).click()
      await personal.updateComplete
      await picker.updateComplete
      return {
        selectedAtStart,
        initiallyChecked,
        emitted: changes.at(-1),
        parentSelected: personal.tokenSelectedPermissions,
      }
    })

    expect(state.selectedAtStart).toEqual(['Dashboard A'])
    expect(state.initiallyChecked).toEqual(['Dashboard A'])
    expect(state.emitted).toEqual([
      { action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_a' } },
      { action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { scope: 'resource', projectId: 'project_1', resourceKind: 'dashboard', resourceId: 'dashboard_b' } },
    ])
    expect(state.parentSelected).toEqual(state.emitted)
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
      const current = root.querySelector('.permission-policy input[type="radio"][value="current"]') as HTMLInputElement
      current.click()
      await personal.updateComplete

      mergePatch({ personalSettings: { tokens: { capabilities: [capability('dashboard_a'), capability('dashboard_b'), capability('dashboard_c')] } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await personal.updateComplete
      const picker = root.querySelector('lv-personal-token-permission-picker') as any
      await picker.updateComplete
      const checkedScope = (root.querySelector('.permission-policy input[type="radio"]:checked') as HTMLInputElement)?.value
      const selectedResources = Array.from(root.querySelectorAll('.permission-resource-chip')).map((chip) => chip.textContent?.replace(/\s+/g, ' ').trim())
      let command: unknown = null
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return { checkedScope, selectedResources, command }
    })

    expect(state.checkedScope).toBe('specific')
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
