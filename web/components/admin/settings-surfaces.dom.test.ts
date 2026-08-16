import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'

let server: Server
let browser: Browser
let baseURL = ''
const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/settings-surfaces-test')

beforeAll(async () => {
  await Bun.$`mkdir -p ${root}`
  const result = await Bun.build({ entrypoints: [join(projectRoot, 'web/components/admin/settings-surfaces.ts')], outdir: root, naming: 'settings-surfaces.js', target: 'browser', minify: false })
  if (!result.success) throw new Error('settings surface bundle failed')
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end('<!doctype html><main><lv-workspace-registry></lv-workspace-registry><lv-service-accounts></lv-service-accounts><lv-audit-log></lv-audit-log><lv-principal-administration></lv-principal-administration><lv-group-administration></lv-group-administration><script type="module" src="/settings-surfaces.js"></script></main>')
      return
    }
    const file = normalize(join(root, url.pathname))
    if (!file.startsWith(root)) { response.writeHead(404); response.end(); return }
    try { response.setHeader('content-type', 'text/javascript'); response.end(await readFile(file)) } catch { response.writeHead(404); response.end() }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
})

test('settings surfaces render typed signals and emit commands', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-service-accounts'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminServiceAccounts: { items: [{ id: 'svc-1', displayName: 'CI', kind: 'service_principal' }], secrets: [] } }, getPath: (path: string) => (path === 'adminServiceAccounts' ? { items: [{ id: 'svc-1', displayName: 'CI', kind: 'service_principal' }], secrets: [] } : undefined), effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-service-accounts') as any
      element.requestUpdate()
      await element.updateComplete
      let detail: unknown = null
      element.addEventListener('lv-service-account-command', (event: CustomEvent) => { detail = event.detail })
      ;(element.shadowRoot.querySelector('tbody button') as HTMLButtonElement).click()
      return { text: element.shadowRoot.textContent?.replace(/\s+/g, ' ').trim(), detail }
    })
    expect(result.text).toContain('CI')
    expect(result.detail).toEqual({ action: 'select', accountId: 'svc-1' })
  } finally { await page.close() }
})

test('workspace registry uses the shared searchable entity list', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-workspace-registry') && customElements.get('lv-entity-list'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const registry = {
        items: [
          {
            id: 'sales', title: 'Sales', description: 'Revenue reporting', href: '/workspaces/sales',
            owner: { subjectType: 'principal', subjectId: 'owner-1', displayName: 'Ada Lovelace', email: 'ada@example.com' },
            administrators: [{ subjectType: 'principal', subjectId: 'admin-1', displayName: 'Grace Hopper', role: 'admin' }],
            environment: 'production', deploymentStatus: 'Active', updatedAt: '2026-08-11T08:00:00Z', links: { self: '/api/v1/workspaces/sales', workspace: '/workspaces/sales' },
          },
          {
            id: 'retail', title: 'Retail', description: 'Store operations', href: '/workspaces/retail',
            administrators: [], environment: 'development', servingStateStatus: 'Not deployed', updatedAt: '2026-08-10T08:00:00Z',
            links: { self: '/api/v1/workspaces/retail', workspace: '/workspaces/retail' },
          },
        ],
        loading: false,
        hasMore: false,
      }
      runtime.setDatastarLitRuntimeForTests?.({
        root: { adminWorkspaces: registry },
        getPath: (path: string) => path === 'adminWorkspaces' ? registry : undefined,
        effect: (fn: () => void) => { fn(); return () => {} },
      })
      const element = document.querySelector('lv-workspace-registry') as any
      element.requestUpdate()
      await element.updateComplete
      const list = element.shadowRoot.querySelector('lv-entity-list') as any
      await list.updateComplete
      const rows = () => Array.from(element.shadowRoot.querySelectorAll('.entity-list-table-row')).map((row: Element) => row.textContent?.replace(/\s+/g, ' ').trim())
      const initialRows = rows()
      const input = element.shadowRoot.querySelector('.entity-search input') as HTMLInputElement
      input.value = 'retail'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await list.updateComplete
      return {
        hasSharedList: Boolean(list),
        ownTableCount: element.shadowRoot.querySelectorAll(':scope > section > .table-wrap').length,
        headings: Array.from(element.shadowRoot.querySelectorAll('h2')).map((heading) => heading.textContent?.trim()),
        headers: Array.from(element.shadowRoot.querySelectorAll('.entity-list-sort-button > span:first-child')).map((header) => header.textContent?.trim()),
        initialRows,
        filteredRows: rows(),
        firstHref: element.shadowRoot.querySelector('.entity-list-identity')?.getAttribute('href'),
        workspaceIconsArePlain: Array.from(element.shadowRoot.querySelectorAll('.entity-list-icon')).every((icon) => icon.classList.contains('is-plain')),
        workspaceIconBorderWidth: getComputedStyle(element.shadowRoot.querySelector('.entity-list-icon') as HTMLElement).borderTopWidth,
        workspaceIconBackground: getComputedStyle(element.shadowRoot.querySelector('.entity-list-icon') as HTMLElement).backgroundColor,
      }
    })

    expect(result.hasSharedList).toBe(true)
    expect(result.ownTableCount).toBe(0)
    expect(result.headings).toEqual([])
    expect(result.headers).toEqual(['Name', 'Owner', 'Administrators', 'Environment', 'Deployment', 'Updated'])
    expect(result.initialRows).toHaveLength(2)
    expect(result.initialRows[0]).toContain('Sales Revenue reporting')
    expect(result.initialRows[0]).toContain('Ada Lovelace')
    expect(result.initialRows[0]).toContain('Grace Hopper')
    expect(result.initialRows[0]).toContain('production')
    expect(result.initialRows[0]).toContain('Active')
    expect(result.filteredRows).toHaveLength(1)
    expect(result.filteredRows[0]).toContain('Retail Store operations')
    expect(result.firstHref).toBe('/workspaces/retail')
    expect(result.workspaceIconsArePlain).toBe(true)
    expect(result.workspaceIconBorderWidth).toBe('0px')
    expect(result.workspaceIconBackground).toBe('rgba(0, 0, 0, 0)')
  } finally { await page.close() }
})

test('principal administration exposes local controls and keeps external profiles read-only', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-principal-administration'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state = {
        principals: [
          { id: 'local-1', kind: 'user', email: 'local@example.com', displayName: 'Local User', identitySource: 'local', hasLocalPassword: true, revision: 'rev-1', createdAt: '2026-08-01T10:00:00Z', updatedAt: '2026-08-02T11:00:00Z', lastSeenAt: '2026-08-03T12:00:00Z', groups: [{ id: 'group-1', name: 'Analysts', provider: 'local' }], capabilities: { canUpdateProfile: true, canResetPassword: true, canBlock: true, canUnblock: false, canDelete: true, canManageSessions: true } },
          { id: 'sso-1', kind: 'user', email: 'sso@example.com', displayName: 'SSO User', identitySource: 'external', identityProvider: 'okta', hasLocalPassword: false, groups: [], capabilities: { canUpdateProfile: false, canResetPassword: false, canBlock: true, canUnblock: false, canDelete: false, canManageSessions: true } },
        ],
        groups: [], projects: [{ id: 'sales', name: 'Sales' }],
        sessions: [{ id: 'session-1', kind: 'browser', createdAt: '2026-08-03T10:00:00Z', lastSeenAt: '2026-08-03T12:00:00Z', expiresAt: '2026-08-10T10:00:00Z' }],
        roleAssignments: [
          { projectId: 'sales', resourceKind: 'project', role: 'viewer', capabilities: [], sourceType: 'direct', sourceId: 'local-1', sourceName: 'Local User' },
          { projectId: 'sales', resourceKind: 'project', role: 'editor', capabilities: [], sourceType: 'group', sourceId: 'group-1', sourceName: 'Analysts' },
        ],
        activity: [{ id: 'event-1', action: 'principal.updated', actorId: 'admin-1', actorName: 'Admin User', status: 'success', createdAt: '2026-08-02T11:00:00Z' }],
        selectedPrincipalId: 'local-1', loading: false,
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccess: state }, getPath: (path: string) => path === 'adminAccess' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-principal-administration') as any
      element.requestUpdate(); await element.updateComplete
      const commands: unknown[] = []
      element.addEventListener('lv-access-admin-command', (event: CustomEvent) => { commands.push(event.detail) })
      const form = element.shadowRoot.querySelector('form') as HTMLFormElement
      ;(form.elements.namedItem('displayName') as HTMLInputElement).value = 'Updated User'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      ;(window as any).confirm = () => true
      ;(Array.from(element.shadowRoot.querySelectorAll('button')) as HTMLButtonElement[]).find((button) => button.textContent?.includes('Revoke all sessions'))?.click()
      const localText = element.shadowRoot.textContent?.replace(/\s+/g, ' ').trim()
      const local = {
        headings: Array.from(element.shadowRoot.querySelectorAll('h2')).map((heading: Element) => heading.textContent?.trim()),
        buttons: Array.from(element.shadowRoot.querySelectorAll('button, summary')).map((button: Element) => button.textContent?.replace(/\s+/g, ' ').trim()),
        status: element.shadowRoot.querySelector('[data-user-status]')?.textContent?.trim(),
        sharedLayout: Boolean(element.shadowRoot.querySelector('.detail-surface .detail-sections')),
        cardCount: element.shadowRoot.querySelectorAll('.detail-card').length,
      }
      state.selectedPrincipalId = 'sso-1'
      element.requestUpdate(); await element.updateComplete
      return { commands, localText, local, externalText: element.shadowRoot.textContent?.replace(/\s+/g, ' ').trim(), externalForm: Boolean(element.shadowRoot.querySelector('form')) }
    })
    expect(result.commands).toEqual([
      { action: 'update_principal', principalId: 'local-1', displayName: 'Updated User', revision: 'rev-1' },
      { action: 'revoke_all_sessions', principalId: 'local-1' },
    ])
    expect(result.localText).toContain('Reset password')
    expect(result.localText).toContain('Principal ID')
    expect(result.localText).toContain('Sales')
    expect(result.localText).toContain('Via Analysts')
    expect(result.localText).toContain('Admin User updated the user profile')
    expect(result.local.headings).toEqual(['Overview', 'Access', 'Security', 'Recent activity'])
    expect(result.local.buttons).toContain('Revoke all sessions')
    expect(result.local.status).toBe('Active')
    expect(result.local.sharedLayout).toBe(true)
    expect(result.local.cardCount).toBe(0)
    expect(result.externalText).toContain('OKTA owns this identity')
    expect(result.externalForm).toBe(false)
  } finally { await page.close() }
})

test('principal creation opens as a modal and transitions to the one-time password result', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-principal-administration'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state: any = { principals: [], groups: [], sessions: [], loading: false }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccess: state }, getPath: (path: string) => path === 'adminAccess' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-principal-administration') as any
      element.createOpen = true
      element.requestUpdate(); await element.updateComplete
      const dialog = element.shadowRoot.querySelector('dialog') as HTMLDialogElement
      let detail: unknown = null
      element.addEventListener('lv-access-admin-command', (event: CustomEvent) => { detail = event.detail })
      const form = dialog.querySelector('form') as HTMLFormElement
      ;(form.elements.namedItem('email') as HTMLInputElement).value = 'new@example.com'
      ;(form.elements.namedItem('displayName') as HTMLInputElement).value = 'New User'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      state.temporaryPassword = 'temporary-password-value'
      state.message = 'Local user created. Copy the temporary password now.'
      element.requestUpdate(); await element.updateComplete
      return {
        open: (element.shadowRoot.querySelector('dialog') as HTMLDialogElement).open,
        detail,
        formAfterSuccess: Boolean(element.shadowRoot.querySelector('dialog form')),
        successText: element.shadowRoot.querySelector('dialog')?.textContent?.replace(/\s+/g, ' ').trim(),
      }
    })
    expect(result.open).toBe(true)
    expect(result.detail).toEqual({ action: 'create_principal', email: 'new@example.com', displayName: 'New User' })
    expect(result.formAfterSuccess).toBe(false)
    expect(result.successText).toContain('temporary-password-value')
    expect(result.successText).toContain('Copy password')
  } finally { await page.close() }
})

test('group administration makes synchronized membership read-only', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-group-administration'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state = {
        principals: [], sessions: [], selectedGroupId: 'scim-1', loading: false,
        groups: [{ id: 'scim-1', name: 'Directory Team', provider: 'scim', externalId: 'team-42', members: [{ id: 'p1', email: 'user@example.com', displayName: 'User' }], capabilities: { canUpdate: false, canDelete: false, canManageMembers: false } }],
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccess: state }, getPath: (path: string) => path === 'adminAccess' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-group-administration') as any
      element.requestUpdate(); await element.updateComplete
      return {
        text: element.shadowRoot.textContent?.replace(/\s+/g, ' ').trim(),
        buttons: Array.from(element.shadowRoot.querySelectorAll('button')).map((button: Element) => button.textContent?.trim()),
        headings: Array.from(element.shadowRoot.querySelectorAll('h2')).map((heading: Element) => heading.textContent?.trim()),
        sharedLayout: Boolean(element.shadowRoot.querySelector('.detail-surface .detail-sections')),
        backHref: element.shadowRoot.querySelector('.back-link')?.getAttribute('href'),
      }
    })
    expect(result.text).toContain('SCIM owns this group')
    expect(result.text).toContain('synchronized and read-only')
    expect(result.buttons).toEqual([])
    expect(result.headings).toEqual(['Overview', 'Members'])
    expect(result.sharedLayout).toBe(true)
    expect(result.backHref).toBe('/admin/groups')
  } finally { await page.close() }
})

test('local group detail moves rename and add member into focused modals', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-group-administration'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state = {
        principals: [
          { id: 'member-1', kind: 'user', email: 'member@example.com', displayName: 'Existing Member', groups: [], capabilities: {} },
          { id: 'candidate-1', kind: 'user', email: 'candidate@example.com', displayName: 'Candidate User', groups: [], capabilities: {} },
          { id: 'candidate-2', kind: 'user', email: 'second@example.com', displayName: 'Second Candidate', groups: [], capabilities: {} },
        ],
        sessions: [], selectedGroupId: 'group-1', loading: false,
        projects: [{ id: 'sales', name: 'Sales' }],
        groups: [{
          id: 'group-1', name: 'Analysts', provider: 'local', externalId: 'analysts', revision: 'rev-1',
          members: [{ id: 'member-1', email: 'member@example.com', displayName: 'Existing Member' }],
          capabilities: { canUpdate: true, canDelete: true, canManageMembers: true },
        }],
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccess: state }, getPath: (path: string) => path === 'adminAccess' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-group-administration') as any
      element.requestUpdate(); await element.updateComplete
      const commands: unknown[] = []
      element.addEventListener('lv-access-admin-command', (event: CustomEvent) => { commands.push(event.detail) })
      const initial = {
        formCount: element.shadowRoot.querySelectorAll('.detail-section form').length,
        cardCount: element.shadowRoot.querySelectorAll('.detail-card').length,
        tableOverflow: (() => { const table = element.shadowRoot.querySelector('.table-wrap') as HTMLElement; return table.scrollWidth > table.clientWidth })(),
        buttons: Array.from(element.shadowRoot.querySelectorAll('button, summary')).map((item: Element) => item.textContent?.replace(/\s+/g, ' ').trim()),
      }

      ;(element.shadowRoot.querySelector('.action-menu summary') as HTMLElement).click()
      ;(Array.from(element.shadowRoot.querySelectorAll<HTMLButtonElement>('.action-menu-popover button'))).find((button) => button.textContent?.includes('Rename group'))?.click()
      await element.updateComplete
      const renameDialog = element.shadowRoot.querySelector('dialog[data-group-detail-dialog="rename"]') as HTMLDialogElement
      const renameForm = renameDialog.querySelector('form') as HTMLFormElement
      ;(renameForm.elements.namedItem('displayName') as HTMLInputElement).value = 'Revenue Analysts'
      renameForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await element.updateComplete

      ;(Array.from(element.shadowRoot.querySelectorAll<HTMLButtonElement>('button'))).find((button) => button.textContent?.trim() === 'Add members')?.click()
      await element.updateComplete
      const memberDialog = element.shadowRoot.querySelector('dialog[data-group-detail-dialog="add-member"]') as HTMLDialogElement
      const memberForm = memberDialog.querySelector('form') as HTMLFormElement
      const picker = memberDialog.querySelector('lv-entity-multi-select') as any
      await picker.updateComplete
      const checkboxes = Array.from(picker.shadowRoot.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
      checkboxes[0].click()
      checkboxes[1].click()
      await picker.updateComplete
      await element.updateComplete
      const submitLabel = memberForm.querySelector<HTMLButtonElement>('button[type="submit"]')?.textContent?.trim()
      memberForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await element.updateComplete

      return {
        initial,
        renameOpen: renameDialog.open,
        renameTitle: renameDialog.querySelector('h2')?.textContent?.trim(),
        memberOpen: memberDialog.open,
        memberTitle: memberDialog.querySelector('h2')?.textContent?.trim(),
        submitLabel,
        commands,
      }
    })
    expect(result.initial.formCount).toBe(0)
    expect(result.initial.cardCount).toBe(0)
    expect(result.initial.tableOverflow).toBe(false)
    expect(result.initial.buttons).toContain('Add members')
    expect(result.initial.buttons).toContain('More actions')
    expect(result.renameTitle).toBe('Rename group')
    expect(result.memberTitle).toBe('Add members')
    expect(result.submitLabel).toBe('Add 2 members')
    expect(result.renameOpen).toBe(false)
    expect(result.memberOpen).toBe(false)
    expect(result.commands).toEqual([
      { action: 'update_group', groupId: 'group-1', displayName: 'Revenue Analysts', revision: 'rev-1' },
      { action: 'add_group_member', groupId: 'group-1', principalIds: ['candidate-1', 'candidate-2'] },
    ])
  } finally { await page.close() }
})

test('group administration creates a local group in the selected workspace', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-group-administration'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state = {
        principals: [], groups: [], sessions: [], loading: false,
        projects: [{ id: 'operations', name: 'Operations' }, { id: 'sales', name: 'Sales' }],
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccess: state }, getPath: (path: string) => path === 'adminAccess' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-group-administration') as any
      element.createOpen = true
      element.requestUpdate(); await element.updateComplete
      let detail: unknown = null
      element.addEventListener('lv-access-admin-command', (event: CustomEvent) => { detail = event.detail })
      const form = element.shadowRoot.querySelector('form') as HTMLFormElement
      ;(form.elements.namedItem('displayName') as HTMLInputElement).value = 'Revenue analysts'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      return {
        detail,
        open: (element.shadowRoot.querySelector('dialog') as HTMLDialogElement).open,
        disabled: (form.querySelector('button[type="submit"]') as HTMLButtonElement).disabled,
      }
    })
    expect(result).toEqual({
      detail: { action: 'create_group', displayName: 'Revenue analysts' },
      open: true,
      disabled: false,
    })
  } finally { await page.close() }
})
