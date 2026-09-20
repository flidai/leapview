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
      response.end('<!doctype html><main><lv-admin-page data-on:lv-service-account-command="service-account-command" data-on:lv-audit-log-command="audit-log-command"><lv-project-registry></lv-project-registry><lv-service-accounts></lv-service-accounts><lv-audit-log></lv-audit-log><lv-access-settings></lv-access-settings><lv-principal-administration></lv-principal-administration><lv-group-administration></lv-group-administration></lv-admin-page><script type="module" src="/settings-surfaces.js"></script></main>')
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
      ;((element.shadowRoot as ShadowRoot).querySelector('tbody button') as HTMLButtonElement).click()
      return {
        text: (element.shadowRoot as ShadowRoot).textContent?.replace(/\s+/g, ' ').trim(),
        detail,
        displayNameLabel: (element.shadowRoot as ShadowRoot).querySelector('input[name="displayName"]')?.getAttribute('aria-label'),
      }
    })
    expect(result.text).toContain('CI')
    expect(result.detail).toEqual({ action: 'select', accountId: 'svc-1' })
    expect(result.displayNameLabel).toBe('New account')
  } finally { await page.close() }
})

test('access settings preserve policy revision and binding identity in durable commands', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-access-settings'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state: any = {
        projectId: 'project:active', policyRevision: 7, policyDigest: 'digest-7',
        roleBindings: [], roles: [{ name: 'viewer', capabilities: ['query:read'] }],
        grantAdministrationAvailable: false,
        grantAdministrationLabel: 'Grant administration unavailable in this surface.',
        loading: false,
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccessSettings: state }, getPath: (path: string) => path === 'adminAccessSettings' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-access-settings') as any
      element.requestUpdate()
      await element.updateComplete
      const details: unknown[] = []
      element.addEventListener('lv-access-settings-command', (event: CustomEvent) => details.push(event.detail))
      const shadow = element.shadowRoot as ShadowRoot
      const value = (name: string) => shadow.querySelector(`[name="${name}"]`) as HTMLInputElement | HTMLSelectElement
      value('bindingId').value = 'finance-viewers'
      value('bindingName').value = 'Finance viewers'
      value('subjectType').value = 'group'
      value('subjectId').value = 'group-finance'
      value('role').value = 'viewer'
      ;(shadow.querySelector('form') as HTMLFormElement).dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))

      state.policyRevision = 8
      state.roleBindings = [{ id: 'finance-viewers', name: 'Finance viewers', subjectType: 'group', subjectId: 'group-finance', role: 'viewer', capabilities: ['query:read'] }]
      element.requestUpdate()
      await element.updateComplete
      window.confirm = () => true
      ;(element.shadowRoot as ShadowRoot).querySelector('tbody button')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      return { text: (element.shadowRoot as ShadowRoot).textContent?.replace(/\s+/g, ' ').trim(), details }
    })
    expect(result.text).toContain('Grant administration unavailable')
    expect(result.text).toContain('group-finance')
    expect(result.details).toEqual([
      { action: 'create', bindingId: 'finance-viewers', bindingName: 'Finance viewers', subjectType: 'group', subjectId: 'group-finance', role: 'viewer', expectedRevision: 7 },
      { action: 'delete', bindingId: 'finance-viewers', expectedRevision: 8 },
    ])
  } finally { await page.close() }
})

test('access settings explain direct, inherited, owner, platform, compiled, and denied authority', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-access-settings'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state: any = {
        projectId: 'project:active', policyRevision: 9, policyDigest: 'digest-9', roleBindings: [], roles: [],
        grantAdministrationAvailable: false, grantAdministrationLabel: 'Grant administration unavailable in this surface.', loading: false,
        effectiveAccess: {
          loading: false,
          decisions: [
            { allowed: true, authority: 'Direct', capability: 'RESOURCE_READ', reason: 'direct role binding', resourceKind: 'dashboard', resourceId: 'dashboard-sales', inherited: false, owner: false, platform: false, subjectType: 'principal', subjectId: 'principal-admin' },
            { allowed: true, authority: 'Group-derived', capability: 'RESOURCE_USE', reason: 'group-inherited role binding', resourceKind: 'dashboard', resourceId: 'dashboard-sales', inherited: true, owner: false, platform: false, subjectType: 'group', subjectId: 'group-sales' },
            { allowed: true, authority: 'Owner', capability: 'PROJECT_ADMIN', reason: 'owner role binding', resourceKind: 'project', resourceId: 'project:active', inherited: false, owner: true, platform: false, subjectType: 'principal', subjectId: 'principal-admin' },
            { allowed: true, authority: 'Platform', capability: 'PROJECT_ADMIN', reason: 'platform administrator', resourceKind: 'project', resourceId: 'project:active', inherited: false, owner: false, platform: true, subjectType: 'principal', subjectId: 'principal-admin' },
            { allowed: true, authority: 'Compiled', capability: 'RESOURCE_READ', reason: 'direct grant', resourceKind: 'dashboard', resourceId: 'dashboard-sales', inherited: false, owner: false, platform: false, grantId: 'compiled-grant', subjectType: 'principal', subjectId: 'principal-admin' },
            { allowed: false, authority: 'Denied', capability: 'RESOURCE_EDIT', reason: 'no direct, inherited, owner, or platform authority', resourceKind: 'dashboard', resourceId: 'dashboard-sales', inherited: false, owner: false, platform: false },
          ],
        },
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccessSettings: state }, getPath: (path: string) => path === 'adminAccessSettings' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-access-settings') as any
      element.requestUpdate()
      await element.updateComplete
      const shadow = element.shadowRoot as ShadowRoot
      return { text: shadow.textContent?.replace(/\s+/g, ' ').trim(), rows: shadow.querySelectorAll('table tbody tr').length }
    })
    expect(result.text).toContain('Effective access explanation')
    expect(result.text).toContain('Direct')
    expect(result.text).toContain('Group-derived')
    expect(result.text).toContain('Owner')
    expect(result.text).toContain('Platform')
    expect(result.text).toContain('Compiled')
    expect(result.text).toContain('Denied')
    expect(result.text).toContain('no direct, inherited, owner, or platform authority')
    expect(result.rows).toBe(6)
  } finally { await page.close() }
})

test('access settings bound explanation has explicit loading and error states', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-access-settings'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state: any = {
        projectId: 'project:active', policyRevision: 9, policyDigest: 'digest-9', roleBindings: [], roles: [],
        grantAdministrationAvailable: false, grantAdministrationLabel: 'Grant administration unavailable in this surface.', loading: false,
        effectiveAccess: { loading: true, decisions: [] },
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccessSettings: state }, getPath: (path: string) => path === 'adminAccessSettings' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-access-settings') as any
      element.requestUpdate(); await element.updateComplete
      const loadingText = (element.shadowRoot as ShadowRoot).textContent?.replace(/\s+/g, ' ').trim()
      state.effectiveAccess = { loading: false, decisions: [], error: 'Effective access explanation is unavailable.' }
      element.requestUpdate(); await element.updateComplete
      const shadow = element.shadowRoot as ShadowRoot
      return { loadingText, errorText: shadow.textContent?.replace(/\s+/g, ' ').trim(), alert: shadow.querySelector('[role="alert"]')?.textContent }
    })
    expect(result.loadingText).toContain('Loading effective access explanation…')
    expect(result.errorText).toContain('Effective access explanation is unavailable.')
    expect(result.alert).toBe('Effective access explanation is unavailable.')
  } finally { await page.close() }
})

test('access settings describe unauthorized and stale mutation outcomes', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-access-settings'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state: any = {
        projectId: 'project:active', policyRevision: 11, policyDigest: 'digest-11', roleBindings: [],
        roles: [{ name: 'viewer', capabilities: ['query:read'] }], grantAdministrationAvailable: false,
        grantAdministrationLabel: 'Grant administration unavailable in this surface.', loading: false,
        effectiveAccess: { loading: false, decisions: [] },
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccessSettings: state }, getPath: (path: string) => path === 'adminAccessSettings' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-access-settings') as any
      element.requestUpdate(); await element.updateComplete
      const form = (element.shadowRoot as ShadowRoot).querySelector('form') as HTMLFormElement
      ;(form.elements.namedItem('bindingId') as HTMLInputElement).value = 'finance-viewers'
      ;(form.elements.namedItem('subjectId') as HTMLInputElement).value = 'group-finance'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await element.updateComplete
      const lockedDuringMutation = (form.querySelector('button[type="submit"]') as HTMLButtonElement).disabled
      state.error = 'Forbidden: project role binding administration is not permitted'
      element.requestUpdate(); await element.updateComplete
      const unauthorizedText = (element.shadowRoot as ShadowRoot).textContent?.replace(/\s+/g, ' ').trim() ?? ''
      state.error = ''
      ;(element as any).commandError = 'Access settings update conflicted with a newer change. Reload the latest state before retrying.'
      element.requestUpdate(); await element.updateComplete
      const staleText = (element.shadowRoot as ShadowRoot).textContent?.replace(/\s+/g, ' ').trim() ?? ''
      return { lockedDuringMutation, unauthorizedText, staleText }
    })
    expect(result.lockedDuringMutation).toBe(true)
    expect(result.unauthorizedText).toContain('Unauthorized')
    expect(result.unauthorizedText).toContain('not authorized')
    expect(result.staleText).toContain('Stale')
    expect(result.staleText).toContain('Reload the current policy')
  } finally { await page.close() }
})

test('service account and audit controls unlock when a no-op command finishes', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-service-accounts') && customElements.get('lv-audit-log'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const serviceState = { items: [{ id: 'svc-1', displayName: 'CI', kind: 'service_principal' }], secrets: [], selectedId: '', loading: false, hasMore: false }
      const auditState = { items: [], filters: {}, loadedCount: 0, loading: false, hasMore: false }
      runtime.setDatastarLitRuntimeForTests?.({
        root: { adminServiceAccounts: serviceState, adminAuditLog: auditState },
        getPath: (path: string) => path === 'adminServiceAccounts' ? serviceState : path === 'adminAuditLog' ? auditState : undefined,
        effect: (fn: () => void) => { fn(); return () => {} },
      })
      const accounts = document.querySelector('lv-service-accounts') as any
      const audit = document.querySelector('lv-audit-log') as any
      accounts.requestUpdate(); audit.requestUpdate()
      await accounts.updateComplete; await audit.updateComplete
      ;((accounts.shadowRoot as ShadowRoot).querySelector('tbody button') as HTMLButtonElement).click()
      await accounts.updateComplete
      const accountDisabled = ((accounts.shadowRoot as ShadowRoot).querySelector('tbody button') as HTMLButtonElement).disabled
      ;((audit.shadowRoot as ShadowRoot).querySelector('form') as HTMLFormElement).dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await audit.updateComplete
      const auditDisabled = ((audit.shadowRoot as ShadowRoot).querySelector('button[type="submit"]') as HTMLButtonElement).disabled
      const auditLabels = Array.from((audit.shadowRoot as ShadowRoot).querySelectorAll('input')).map((input) => input.getAttribute('aria-label'))
      const unrelatedOwner = document.createElement('lv-other-page')
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: unrelatedOwner } }))
      await accounts.updateComplete; await audit.updateComplete
      const accountStillDisabled = ((accounts.shadowRoot as ShadowRoot).querySelector('tbody button') as HTMLButtonElement).disabled
      const auditStillDisabled = ((audit.shadowRoot as ShadowRoot).querySelector('button[type="submit"]') as HTMLButtonElement).disabled
      const owner = document.querySelector('lv-admin-page') as Element
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: owner } }))
      await accounts.updateComplete; await audit.updateComplete
      return {
        accountDisabled,
        auditDisabled,
        accountStillDisabled,
        auditStillDisabled,
        auditLabels,
        accountUnlocked: !((accounts.shadowRoot as ShadowRoot).querySelector('tbody button') as HTMLButtonElement).disabled,
        auditUnlocked: !((audit.shadowRoot as ShadowRoot).querySelector('button[type="submit"]') as HTMLButtonElement).disabled,
      }
    })
    expect(result).toEqual({ accountDisabled: true, auditDisabled: true, accountStillDisabled: true, auditStillDisabled: true, auditLabels: ['Project', 'Actor', 'Action', 'Resource kind', 'Resource ID', 'From', 'To'], accountUnlocked: true, auditUnlocked: true })
  } finally { await page.close() }
})

test('audit filters keep the bound project read-only and emit date timestamps', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-audit-log'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state = { items: [], filters: { projectId: 'project:server-bound' }, loadedCount: 0, loading: false, hasMore: false }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAuditLog: state }, getPath: (path: string) => path === 'adminAuditLog' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-audit-log') as any
      element.requestUpdate()
      await element.updateComplete
      let detail: any = null
      element.addEventListener('lv-audit-log-command', (event: CustomEvent) => { detail = event.detail })
      const shadow = element.shadowRoot as ShadowRoot
      const project = shadow.querySelector('input[name="projectId"]') as HTMLInputElement
      const from = shadow.querySelector('input[name="from"]') as HTMLInputElement
      const to = shadow.querySelector('input[name="to"]') as HTMLInputElement
      from.value = '2026-09-01T00:00'
      to.value = '2026-10-01T00:00'
      ;(shadow.querySelector('form') as HTMLFormElement).dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      return { projectValue: project.value, projectReadOnly: project.readOnly, fromType: from.type, toType: to.type, detail }
    })
    expect(result).toEqual({
      projectValue: 'project:server-bound',
      projectReadOnly: true,
      fromType: 'datetime-local',
      toType: 'datetime-local',
      detail: { action: 'filter', filters: { projectId: 'project:server-bound', principalId: '', action: '', resourceKind: '', resourceId: '', from: '2026-09-01T00:00:00.000Z', to: '2026-10-01T00:00:00.000Z' } },
    })
  } finally { await page.close() }
})

test('audit clear resets live controls when the cleared signal arrives', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-audit-log'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state: any = {
        items: [],
        filters: {
          projectId: 'project:server-bound', principalId: 'principal-1', action: 'principal.updated',
          resourceKind: 'principal', resourceId: 'principal-1', from: '2026-09-01T00:00:00.000Z', to: '2026-10-01T00:00:00.000Z',
        },
        loadedCount: 0, loading: false, hasMore: false,
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAuditLog: state }, getPath: (path: string) => path === 'adminAuditLog' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-audit-log') as any
      element.requestUpdate()
      await element.updateComplete
      let detail: unknown = null
      element.addEventListener('lv-audit-log-command', (event: CustomEvent) => { detail = event.detail })
      const shadow = element.shadowRoot as ShadowRoot
      const currentValue = (name: string) => (shadow.querySelector(`[name="${name}"]`) as HTMLInputElement).value
      const initialValues = { principalId: currentValue('principalId'), action: currentValue('action'), resourceKind: currentValue('resourceKind'), resourceId: currentValue('resourceId'), from: currentValue('from'), to: currentValue('to') }
      ;(shadow.querySelector('button[type="button"]') as HTMLButtonElement).click()
      const clearedValuesBeforeResponse = { principalId: currentValue('principalId'), action: currentValue('action'), resourceKind: currentValue('resourceKind'), resourceId: currentValue('resourceId'), from: currentValue('from'), to: currentValue('to') }

      // Datastar's response replaces the signal with the server-bound project
      // and no user-selected filters. Exercise the live DOM after that patch.
      state.filters = { projectId: 'project:server-bound' }
      element.requestUpdate()
      await element.updateComplete
      const value = (name: string) => (shadow.querySelector(`[name="${name}"]`) as HTMLInputElement).value
      return {
        initialValues,
        clearedValuesBeforeResponse,
        values: { principalId: value('principalId'), action: value('action'), resourceKind: value('resourceKind'), resourceId: value('resourceId'), from: value('from'), to: value('to') },
        detail,
        signalFilters: state.filters,
      }
    })
    expect(result).toEqual({
      initialValues: { principalId: 'principal-1', action: 'principal.updated', resourceKind: 'principal', resourceId: 'principal-1', from: '2026-09-01T00:00', to: '2026-10-01T00:00' },
      clearedValuesBeforeResponse: { principalId: '', action: '', resourceKind: '', resourceId: '', from: '', to: '' },
      values: { principalId: '', action: '', resourceKind: '', resourceId: '', from: '', to: '' },
      detail: { action: 'clear', filters: {} },
      signalFilters: { projectId: 'project:server-bound' },
    })
  } finally { await page.close() }
})

test('service account secret creation exposes the finite default and emits the selected lifetime', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-service-accounts'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state = { items: [{ id: 'svc-1', displayName: 'CI', kind: 'service_principal' }], secrets: [], selectedId: 'svc-1', loading: false, hasMore: false }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminServiceAccounts: state }, getPath: (path: string) => path === 'adminServiceAccounts' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-service-accounts') as any
      element.requestUpdate()
      await element.updateComplete
      let detail: unknown = null
      element.addEventListener('lv-service-account-command', (event: CustomEvent) => { detail = event.detail })
      const shadow = element.shadowRoot as ShadowRoot
      const select = shadow.querySelector('select[name="secretLifetimeDays"]') as HTMLSelectElement
      const name = shadow.querySelector('input[name="secretName"]') as HTMLInputElement
      const form = name.closest('form') as HTMLFormElement
      const initialValue = select.value
      name.value = 'CI pipeline'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      const initialDetail = detail
      select.value = '365'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      return { initialValue, optionValues: Array.from(select.options).map((option) => option.value), initialDetail, selectedValue: select.value, detail }
    })
    expect(result.initialValue).toBe('180')
    expect(result.optionValues).toEqual(['30', '90', '180', '365'])
    expect(result.initialDetail).toEqual({ action: 'create_secret', accountId: 'svc-1', secretName: 'CI pipeline', secretLifetimeDays: 180 })
    expect(result.selectedValue).toBe('365')
    expect(result.detail).toEqual({ action: 'create_secret', accountId: 'svc-1', secretName: 'CI pipeline', secretLifetimeDays: 365 })
  } finally { await page.close() }
})

test('project registry uses the shared searchable entity list', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-project-registry') && customElements.get('lv-entity-list'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const registry = {
        items: [
          {
            id: 'sales', title: 'Sales', description: 'Revenue reporting', href: '/projects/sales',
            owner: { subjectType: 'principal', subjectId: 'owner-1', displayName: 'Ada Lovelace', email: 'ada@example.com' },
            administrators: [{ subjectType: 'principal', subjectId: 'admin-1', displayName: 'Grace Hopper', role: 'admin' }],
            environment: 'production', deploymentStatus: 'Active', updatedAt: '2026-08-11T08:00:00Z', links: { self: '/api/v1/projects/sales', project: '/projects/sales' },
          },
          {
            id: 'retail', title: 'Retail', description: 'Store operations', href: '/projects/retail',
            administrators: [], environment: 'development', servingStateStatus: 'Not deployed', updatedAt: '2026-08-10T08:00:00Z',
            links: { self: '/api/v1/projects/retail', project: '/projects/retail' },
          },
        ],
        loading: false,
        hasMore: false,
      }
      runtime.setDatastarLitRuntimeForTests?.({
        root: { adminProjects: registry },
        getPath: (path: string) => path === 'adminProjects' ? registry : undefined,
        effect: (fn: () => void) => { fn(); return () => {} },
      })
      const element = document.querySelector('lv-project-registry') as any
      element.requestUpdate()
      await element.updateComplete
      const list = (element.shadowRoot as ShadowRoot).querySelector('lv-entity-list') as any
      await list.updateComplete
      const rows = () => Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('.entity-list-table-row')).map((row: Element) => row.textContent?.replace(/\s+/g, ' ').trim())
      const initialRows = rows()
      const input = (element.shadowRoot as ShadowRoot).querySelector('.entity-search input') as HTMLInputElement
      input.value = 'retail'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await list.updateComplete
      return {
        hasSharedList: Boolean(list),
        ownTableCount: (element.shadowRoot as ShadowRoot).querySelectorAll(':scope > section > .table-wrap').length,
        headings: Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('h2')).map((heading) => heading.textContent?.trim()),
        headers: Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('.entity-list-sort-button > span:first-child')).map((header) => header.textContent?.trim()),
        initialRows,
        filteredRows: rows(),
        firstHref: (element.shadowRoot as ShadowRoot).querySelector('.entity-list-identity')?.getAttribute('href'),
        projectIconsArePlain: Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('.entity-list-icon')).every((icon) => icon.classList.contains('is-plain')),
        projectIconBorderWidth: getComputedStyle((element.shadowRoot as ShadowRoot).querySelector('.entity-list-icon') as HTMLElement).borderTopWidth,
        projectIconBackground: getComputedStyle((element.shadowRoot as ShadowRoot).querySelector('.entity-list-icon') as HTMLElement).backgroundColor,
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
    expect(result.firstHref).toBe('/projects/retail')
    expect(result.projectIconsArePlain).toBe(true)
    expect(result.projectIconBorderWidth).toBe('0px')
    expect(result.projectIconBackground).toBe('rgba(0, 0, 0, 0)')
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
      const chrome = { sidebar: { userAvatarUrl: '/profile/avatars/local-1/avatar-digest' } }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAccess: state, chrome }, getPath: (path: string) => path === 'adminAccess' ? state : path === 'chrome' ? chrome : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-principal-administration') as any
      element.requestUpdate(); await element.updateComplete
      const commands: unknown[] = []
      element.addEventListener('lv-access-admin-command', (event: CustomEvent) => { commands.push(event.detail) })
      const form = (element.shadowRoot as ShadowRoot).querySelector('form') as HTMLFormElement
      ;(form.elements.namedItem('displayName') as HTMLInputElement).value = 'Updated User'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      ;(window as any).confirm = () => true
      ;(Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('button')) as HTMLButtonElement[]).find((button) => button.textContent?.includes('Revoke all sessions'))?.click()
      const localText = (element.shadowRoot as ShadowRoot).textContent?.replace(/\s+/g, ' ').trim()
      const local = {
        headings: Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('h2')).map((heading: Element) => heading.textContent?.trim()),
        buttons: Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('button, summary')).map((button: Element) => button.textContent?.replace(/\s+/g, ' ').trim()),
        status: (element.shadowRoot as ShadowRoot).querySelector('[data-user-status]')?.textContent?.trim(),
        sharedLayout: Boolean((element.shadowRoot as ShadowRoot).querySelector('.detail-surface .detail-sections')),
        cardCount: (element.shadowRoot as ShadowRoot).querySelectorAll('.detail-card').length,
        avatarSrc: ((element.shadowRoot as ShadowRoot).querySelector('lv-user-avatar') as any)?.shadowRoot?.querySelector('img')?.getAttribute('src'),
      }
      state.selectedPrincipalId = 'sso-1'
      element.requestUpdate(); await element.updateComplete
      return { commands, localText, local, externalText: (element.shadowRoot as ShadowRoot).textContent?.replace(/\s+/g, ' ').trim(), externalForm: Boolean((element.shadowRoot as ShadowRoot).querySelector('form')) }
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
    expect(result.local.avatarSrc).toBe('/profile/avatars/local-1/avatar-digest')
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
      const dialog = (element.shadowRoot as ShadowRoot).querySelector('dialog') as HTMLDialogElement
      let detail: unknown = null
      element.addEventListener('lv-access-admin-command', (event: CustomEvent) => { detail = event.detail })
      const form = dialog.querySelector('form') as HTMLFormElement
      const labels = Array.from(dialog.querySelectorAll('input')).map((input) => input.getAttribute('aria-label'))
      ;(form.elements.namedItem('email') as HTMLInputElement).value = 'new@example.com'
      ;(form.elements.namedItem('displayName') as HTMLInputElement).value = 'New User'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      state.temporaryPassword = 'temporary-password-value'
      state.message = 'Local user created. Copy the temporary password now.'
      element.requestUpdate(); await element.updateComplete
      return {
        open: ((element.shadowRoot as ShadowRoot).querySelector('dialog') as HTMLDialogElement).open,
        detail,
        labels,
        formAfterSuccess: Boolean((element.shadowRoot as ShadowRoot).querySelector('dialog form')),
        successText: (element.shadowRoot as ShadowRoot).querySelector('dialog')?.textContent?.replace(/\s+/g, ' ').trim(),
      }
    })
    expect(result.open).toBe(true)
    expect(result.detail).toEqual({ action: 'create_principal', email: 'new@example.com', displayName: 'New User' })
    expect(result.labels).toEqual(['Email', 'Display name'])
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
        text: (element.shadowRoot as ShadowRoot).textContent?.replace(/\s+/g, ' ').trim(),
        buttons: Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('button')).map((button: Element) => button.textContent?.trim()),
        headings: Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('h2')).map((heading: Element) => heading.textContent?.trim()),
        sharedLayout: Boolean((element.shadowRoot as ShadowRoot).querySelector('.detail-surface .detail-sections')),
        backHref: (element.shadowRoot as ShadowRoot).querySelector('.back-link')?.getAttribute('href'),
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
        formCount: (element.shadowRoot as ShadowRoot).querySelectorAll('.detail-section form').length,
        cardCount: (element.shadowRoot as ShadowRoot).querySelectorAll('.detail-card').length,
        tableOverflow: (() => { const table = (element.shadowRoot as ShadowRoot).querySelector('.table-wrap') as HTMLElement; return table.scrollWidth > table.clientWidth })(),
        buttons: Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('button, summary')).map((item: Element) => item.textContent?.replace(/\s+/g, ' ').trim()),
      }

      ;((element.shadowRoot as ShadowRoot).querySelector('.action-menu summary') as HTMLElement).click()
      ;(Array.from((element.shadowRoot as ShadowRoot).querySelectorAll<HTMLButtonElement>('.action-menu-popover button'))).find((button) => button.textContent?.includes('Rename group'))?.click()
      await element.updateComplete
      const renameDialog = (element.shadowRoot as ShadowRoot).querySelector('dialog[data-group-detail-dialog="rename"]') as HTMLDialogElement
      const renameForm = renameDialog.querySelector('form') as HTMLFormElement
      ;(renameForm.elements.namedItem('displayName') as HTMLInputElement).value = 'Revenue Analysts'
      renameForm.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await element.updateComplete

      ;(Array.from((element.shadowRoot as ShadowRoot).querySelectorAll<HTMLButtonElement>('button'))).find((button) => button.textContent?.trim() === 'Add members')?.click()
      await element.updateComplete
      const memberDialog = (element.shadowRoot as ShadowRoot).querySelector('dialog[data-group-detail-dialog="add-member"]') as HTMLDialogElement
      const memberForm = memberDialog.querySelector('form') as HTMLFormElement
      const picker = memberDialog.querySelector('lv-entity-multi-select') as any
      await picker.updateComplete
      const checkboxes = Array.from((picker.shadowRoot as ShadowRoot).querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
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

test('group administration creates a local group in the selected project', async () => {
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
      const form = (element.shadowRoot as ShadowRoot).querySelector('form') as HTMLFormElement
      ;(form.elements.namedItem('displayName') as HTMLInputElement).value = 'Revenue analysts'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      return {
        detail,
        open: ((element.shadowRoot as ShadowRoot).querySelector('dialog') as HTMLDialogElement).open,
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
