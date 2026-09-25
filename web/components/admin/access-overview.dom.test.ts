import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => { fixture = await startAdminPageTestFixture() })
afterAll(async () => { if (fixture) await stopAdminPageTestFixture(fixture) }, 15_000)

test('roles and permissions is a catalogue, not a project-wide assignment manager', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-access-overview'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'admin', title: 'Roles & permissions', active: 'access', headerTitle: 'Roles & permissions' },
        adminAccess: {
          principals: [], groups: [], sessions: [], activity: [], loading: false, projectId: 'project-demo', policyRevision: 7,
          rolePresets: [{ role: 'viewer', name: 'Viewer', profile: 'leapview.permissions/v1', description: 'View approved dashboards.', permissions: ['dashboard.read', 'dashboard.create'], details: [{ action: 'dashboard.read', displayName: 'View dashboard', family: 'Dashboards', scope: 'resource', resourceKinds: ['Dashboard'], description: 'View an approved dashboard.', prerequisites: [] }, { action: 'dashboard.create', displayName: 'Create dashboards', family: 'Dashboards', scope: 'project', description: 'Create dashboards.', prerequisites: [] }] }],
          roleAssignments: [{ bindingId: 'binding-1', projectId: 'project-demo', role: 'viewer', permissions: ['dashboard.read'], subjectType: 'group', subjectId: 'group-1', subjectName: 'Analytics', policyRevision: 7 }],
        },
      })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const overview = admin.shadowRoot.querySelector('lv-access-overview') as any
      await overview.updateComplete
      const root = overview.shadowRoot as ShadowRoot
      const tabs = Array.from(root.querySelectorAll<HTMLButtonElement>('[role="tab"]')).map((tab) => tab.textContent?.trim())
      const list = root.querySelector('.role-catalogue lv-entity-list') as HTMLElement
      const firstHeader = list.querySelector('thead th') as HTMLElement
      const roleRow = list.querySelector('.entity-list-table-row') as HTMLElement
      const firstCell = roleRow.querySelector('th') as HTMLElement
      const firstColumn = {
        headerCase: getComputedStyle(firstHeader).textTransform,
        headerColor: getComputedStyle(firstHeader).color,
        valueCase: getComputedStyle(firstCell).textTransform,
        valueColor: getComputedStyle(firstCell).color,
        mainColor: getComputedStyle(root.querySelector('h1') as HTMLElement).color,
      }
      roleRow.click()
      await overview.updateComplete
      const drawer = root.querySelector('lv-drawer') as HTMLElement
      return {
        heading: root.querySelector('h1')?.textContent?.trim(),
        tabs,
        firstColumn,
        listText: root.querySelector('.role-catalogue lv-entity-list')?.textContent,
        drawerText: drawer.textContent?.replace(/\s+/g, ' ').trim(),
        permissionHeading: drawer.querySelector('.catalog-drawer-body h3')?.textContent?.trim(),
        rolePermissionRows: Array.from(drawer.querySelectorAll('.role-permission-list li')).map((row) => [row.querySelector('code')?.textContent?.trim(), row.querySelector('span')?.textContent?.trim()]),
        rolePermissionCards: drawer.querySelectorAll('.role-permission-group').length,
        grantButton: Boolean(root.querySelector('button.primary, lv-role-grant-dialog')),
        assignmentView: Boolean(root.querySelector('[aria-label="Assignments"], [aria-label="Assignment view"]')),
      }
    })
    expect(state.heading).toBe('Roles & permissions')
    expect(state.tabs).toEqual(['Roles', 'Permissions'])
    expect(state.firstColumn.headerCase).toBe('none')
    expect(state.firstColumn.valueCase).toBe('none')
    expect(state.firstColumn.headerColor).toBe(state.firstColumn.mainColor)
    expect(state.firstColumn.valueColor).toBe(state.firstColumn.mainColor)
    expect(state.listText).toContain('Viewer')
    expect(state.listText).not.toContain('Analytics')
    expect(state.permissionHeading).toBe('Permissions (2)')
    expect(state.rolePermissionRows).toEqual([['dashboard.read', 'View dashboard'], ['dashboard.create', 'Create dashboards']])
    expect(state.rolePermissionCards).toBe(0)
    expect(state.drawerText).toContain('View dashboard')
    expect(state.drawerText).toContain('dashboard.read')
    expect(state.drawerText).not.toContain('Permission profile')
    expect(state.grantButton).toBe(false)
    expect(state.assignmentView).toBe(false)
  } finally {
    await page.close()
  }
})

test('permission catalogue is a static searchable list of exact actions', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-access-overview'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({
        page: { kind: 'admin', title: 'Roles & permissions', active: 'access', headerTitle: 'Roles & permissions' },
        adminAccess: {
          principals: [], groups: [], sessions: [], activity: [], loading: false, projectId: 'project-demo', policyRevision: 9,
          rolePresets: [], roleAssignments: [], permissionCatalog: [
            { action: 'dashboard.read', displayName: 'View dashboard', family: 'Dashboard', scope: 'resource', resourceKinds: ['dashboard'], description: 'View an approved dashboard definition and shell.' },
            { action: 'dashboard.create', displayName: 'Create dashboard', family: 'Dashboard', scope: 'project', description: 'Create a dashboard in the bound Project.' },
            { action: 'project.access.delegate', displayName: 'Delegate project access', family: 'Project administration', scope: 'project', description: 'Issue authority within an explicit envelope.' },
          ],
        },
      })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const overview = admin.shadowRoot.querySelector('lv-access-overview') as any
      await overview.updateComplete
      const root = overview.shadowRoot as ShadowRoot
      ;(Array.from(root.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find((tab) => tab.textContent?.trim() === 'Permissions') as HTMLButtonElement).click()
      await overview.updateComplete
      const list = root.querySelector('.role-catalogue lv-entity-list') as HTMLElement
      const rows = list.querySelectorAll('.entity-list-table-row')
      const headings = Array.from(list.querySelectorAll('thead th')).map((cell) => cell.textContent?.trim())
      const values = Array.from(rows).map((row) => Array.from(row.querySelectorAll('th, td')).map((cell) => {
        const mobileLabel = cell.querySelector('.entity-list-mobile-cell-label')?.textContent ?? ''
        return cell.textContent?.replace(mobileLabel, '').trim()
      }))
      const meaningTitle = rows[0].querySelector('td')?.getAttribute('title')
      const firstHeader = list.querySelector('thead th') as HTMLElement
      const firstCell = rows[0].querySelector('th') as HTMLElement
      const firstColumn = {
        headerCase: getComputedStyle(firstHeader).textTransform,
        headerColor: getComputedStyle(firstHeader).color,
        valueCase: getComputedStyle(firstCell).textTransform,
        valueColor: getComputedStyle(firstCell).color,
        mainColor: getComputedStyle(root.querySelector('h1') as HTMLElement).color,
      }
      const search = list.querySelector('input[type="search"]') as HTMLInputElement
      search.value = 'project.access.delegate'
      search.dispatchEvent(new Event('input', { bubbles: true }))
      await (list as any).updateComplete
      const exactIDSearch = Array.from(list.querySelectorAll('.entity-list-table-row')).map((row) => row.textContent?.replace(/\s+/g, ' ').trim())
      search.value = ''
      search.dispatchEvent(new Event('input', { bubbles: true }))
      await (list as any).updateComplete
      const permissionRow = list.querySelector('.entity-list-table-row') as HTMLElement
      const interactiveRow = permissionRow.classList.contains('is-actionable') || permissionRow.hasAttribute('tabindex')
      permissionRow.click()
      await overview.updateComplete
      const drawerOnClick = Boolean(root.querySelector('lv-drawer'))
      ;(Array.from(root.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find((tab) => tab.textContent?.trim() === 'Roles') as HTMLButtonElement).click()
      await overview.updateComplete
      return { rows: rows.length, headings, values, meaningTitle, firstColumn, exactIDSearch, grouped: Boolean(list.querySelector('.entity-list-group-row')), listText: list.textContent, interactiveRow, drawerOnClick, closed: !root.querySelector('lv-drawer') }
    })
    expect(state.rows).toBe(3)
    expect(state.headings).toEqual(['Permission', 'Meaning', 'Scope'])
    expect(state.values[0]).toEqual(['dashboard.read', 'View dashboard', 'Resource'])
    expect(state.values[1]).toEqual(['dashboard.create', 'Create dashboard', 'Project'])
    expect(state.values[2]).toEqual(['project.access.delegate', 'Delegate project access', 'Project'])
    expect(state.meaningTitle).toBe('View an approved dashboard definition and shell.')
    expect(state.firstColumn.headerCase).toBe('none')
    expect(state.firstColumn.valueCase).toBe('none')
    expect(state.firstColumn.headerColor).toBe(state.firstColumn.mainColor)
    expect(state.firstColumn.valueColor).toBe(state.firstColumn.mainColor)
    expect(state.exactIDSearch).toHaveLength(1)
    expect(state.exactIDSearch[0]).toContain('project.access.delegate')
    expect(state.grouped).toBe(false)
    expect(state.listText).not.toContain('Create a dashboard in the bound Project.')
    expect(state.interactiveRow).toBe(false)
    expect(state.drawerOnClick).toBe(false)
    expect(state.closed).toBe(true)
  } finally {
    await page.close()
  }
})
