import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => { fixture = await startAdminPageTestFixture() })
afterAll(async () => { if (fixture) await stopAdminPageTestFixture(fixture) }, 15_000)

test('access overview explains assignments and grants a maintained role', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-access-overview') && customElements.get('lv-role-grant-dialog'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'admin', title: 'Access overview', active: 'access', headerTitle: 'Access overview', headerDetail: 'See who can access the current project and why.' },
        adminAccess: {
          principals: [{ id: 'principal-1', kind: 'user', email: 'ada@example.com', displayName: 'Ada', identitySource: 'local', hasLocalPassword: true, groups: [], capabilities: { canUpdateProfile: true, canResetPassword: true, canBlock: true, canUnblock: false, canDelete: true, canManageSessions: true } }],
          groups: [{ id: 'group-1', name: 'Analytics', provider: 'local', members: [], capabilities: { canUpdate: true, canDelete: true, canManageMembers: true } }],
          sessions: [], activity: [], loading: false, projectId: 'project-demo', policyRevision: 7,
          rolePresets: [{ role: 'viewer', name: 'Viewer', description: 'View approved dashboards.', permissions: ['dashboard.read', 'semantic.consume'] }],
          roleAssignments: [{ bindingId: 'binding-1', projectId: 'project-demo', role: 'explorer', capabilities: [], permissions: ['dashboard.read', 'semantic.read', 'semantic.query'], status: 'Active', sourceType: 'group', sourceId: 'group-1', sourceName: 'Analytics', subjectType: 'group', subjectId: 'group-1', subjectName: 'Analytics', policyRevision: 7 }],
        },
      })
      const admin = document.querySelector('lv-admin-page') as any
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await admin.updateComplete
      const overview = admin.shadowRoot.querySelector('lv-access-overview') as any
      await overview.updateComplete
      const root = overview.shadowRoot as ShadowRoot
      let command: unknown = null
      admin.addEventListener('lv-access-admin-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.access-summary .primary') as HTMLButtonElement).click()
      await overview.updateComplete
      const grant = root.querySelector('lv-role-grant-dialog') as any
      await grant.updateComplete
      const grantRoot = grant.shadowRoot as ShadowRoot
      const identity = grantRoot.querySelector('select') as HTMLSelectElement
      identity.value = 'principal:principal-1'
      identity.dispatchEvent(new Event('change', { bubbles: true }))
      ;(grantRoot.querySelector('input[value="viewer"]') as HTMLInputElement).click()
      await grant.updateComplete
      ;(grantRoot.querySelector('form') as HTMLFormElement).requestSubmit()
      const why = root.querySelector('.access-assignment details') as HTMLDetailsElement
      why.open = true
      return {
        heading: root.querySelector('h1')?.textContent?.trim(),
        text: root.textContent?.replace(/\s+/g, ' ').trim(),
        dialogWasOpen: Boolean(grantRoot.querySelector('dialog')?.open),
        command,
        why: why.textContent?.replace(/\s+/g, ' ').trim(),
      }
    })
    expect(state.heading).toBe('Access overview')
    expect(state.text).toContain('Analytics')
    expect(state.text).toContain('they do not independently grant a role')
    expect(state.dialogWasOpen).toBe(true)
    expect(state.command).toEqual({ action: 'grant_role', subjectType: 'principal', subjectId: 'principal-1', role: 'viewer', expectedRevision: 7 })
    expect(state.why).toContain('assigned to Analytics')
    expect(state.why).toContain('revision 7')
  } finally {
    await page.close()
  }
})

test('authentication bypass explains why a group role cannot be granted', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-access-overview'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'admin', title: 'Access overview', active: 'access', headerTitle: 'Access overview' },
        adminAccess: {
          principals: [], groups: [{ id: 'group-1', name: 'Analytics', provider: 'local', members: [], capabilities: { canUpdate: true, canDelete: true, canManageMembers: true } }],
          sessions: [], activity: [], loading: false, projectId: 'project-demo', policyRevision: 7,
          roleMutationUnavailableReason: 'Role changes require a signed-in account. This dev server uses authentication bypass.',
          rolePresets: [{ role: 'viewer', name: 'Viewer', description: 'View dashboards.', permissions: ['dashboard.read'] }],
          roleAssignments: [{ bindingId: 'binding-1', projectId: 'project-demo', role: 'viewer', capabilities: [], permissions: ['dashboard.read'], status: 'Active', sourceType: 'group', sourceId: 'group-1', sourceName: 'Analytics', subjectType: 'group', subjectId: 'group-1', subjectName: 'Analytics', policyRevision: 7 }],
        },
      })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const overview = admin.shadowRoot.querySelector('lv-access-overview') as any
      await overview.updateComplete
      const root = overview.shadowRoot as ShadowRoot
      ;(root.querySelector('.access-summary .primary') as HTMLButtonElement).click()
      await overview.updateComplete
      const grant = root.querySelector('lv-role-grant-dialog') as any
      await grant.updateComplete
      return {
        explanation: root.querySelector('[role="note"]')?.textContent,
        removeDisabled: (root.querySelector('.access-assignment .danger') as HTMLButtonElement).disabled,
        submitDisabled: (grant.shadowRoot.querySelector('button[type="submit"]') as HTMLButtonElement).disabled,
        dialogExplanation: grant.shadowRoot.querySelector('[role="note"]')?.textContent,
      }
    })
    expect(state.explanation).toContain('authentication bypass')
    expect(state.removeDisabled).toBe(true)
    expect(state.submitDisabled).toBe(true)
    expect(state.dialogExplanation).toContain('authentication bypass')
  } finally {
    await page.close()
  }
})
