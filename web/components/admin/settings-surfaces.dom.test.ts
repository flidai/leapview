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
      response.end('<!doctype html><main><lv-admin-page data-on:lv-service-account-command="service-account-command" data-on:lv-audit-log-command="audit-log-command"><lv-project-registry></lv-project-registry><lv-service-accounts></lv-service-accounts><lv-audit-log></lv-audit-log><lv-principal-administration></lv-principal-administration><lv-group-administration></lv-group-administration></lv-admin-page><script type="module" src="/settings-surfaces.js"></script></main>')
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
      const root = element.shadowRoot as ShadowRoot
      const sharedList = root.querySelector('lv-entity-list')
      ;(root.querySelector('.entity-action') as HTMLButtonElement).click()
      await element.updateComplete
      const displayNameLabel = root.querySelector('input[name="displayName"]')?.getAttribute('aria-label')
      ;(root.querySelector('[data-service-account-dialog="create"] .modal-close') as HTMLButtonElement).click()
      await element.updateComplete
      ;(root.querySelector('.entity-list-row-action') as HTMLButtonElement).click()
      return {
        text: root.textContent?.replace(/\s+/g, ' ').trim(),
        detail,
        displayNameLabel,
        hasSharedList: Boolean(sharedList),
        customTableCount: root.querySelectorAll(':scope > .table-wrap').length,
        pageHeading: root.querySelector('.page-header h1')?.textContent?.trim(),
      }
    })
    expect(result.text).toContain('CI')
    expect(result.detail).toEqual({ action: 'select', accountId: 'svc-1' })
    expect(result.displayNameLabel).toBe('Display name')
    expect(result.hasSharedList).toBe(true)
    expect(result.customTableCount).toBe(0)
    expect(result.pageHeading).toBe('Service accounts')
  } finally { await page.close() }
})

test('service account detail uses shared lists and focused credential confirmations', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-service-accounts') && customElements.get('lv-select-menu'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state = {
        items: [{ id: 'svc-1', displayName: 'Production deploy', kind: 'service_principal', createdAt: '2026-08-01T10:00:00Z', updatedAt: '2026-08-02T11:00:00Z' }],
        selectedId: 'svc-1',
        secrets: [{ id: 'secret-1', servicePrincipalId: 'svc-1', name: 'CI pipeline', createdAt: '2026-08-03T12:00:00Z', expiresAt: '2026-11-01T23:59:59Z' }],
        createdSecret: 'lv_service_secret_once', loading: false, hasMore: false,
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminServiceAccounts: state }, getPath: (path: string) => path === 'adminServiceAccounts' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-service-accounts') as any
      element.requestUpdate(); await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const commands: unknown[] = []
      element.addEventListener('lv-service-account-command', (event: CustomEvent) => { commands.push(event.detail) })
      const initial = {
        sharedDetail: Boolean(root.querySelector('.detail-surface .detail-sections')),
        sharedCredentialList: Boolean(root.querySelector('lv-entity-list')),
        ownTableCount: root.querySelectorAll(':scope > .table-wrap').length,
        headings: Array.from(root.querySelectorAll('h2')).map((heading) => heading.textContent?.trim()),
        secretNotice: root.querySelector('lv-one-time-secret')?.shadowRoot?.querySelector('[role="status"]')?.textContent?.replace(/\s+/g, ' ').trim(),
        backHref: root.querySelector('.back-link')?.getAttribute('href'),
      }

      ;(Array.from(root.querySelectorAll<HTMLButtonElement>('button')).find((button) => button.textContent?.includes('Create credential')) as HTMLButtonElement).click()
      await element.updateComplete
      const secretDialog = root.querySelector('[data-service-account-dialog="secret"]') as HTMLDialogElement
      const expiration = secretDialog.querySelector('lv-select-menu') as any
      await expiration.updateComplete
      const expirationOptions = (expiration.options as Array<{ label: string }>).map((option) => option.label)
      expiration.dispatchEvent(new CustomEvent('lv-select-change', { bubbles: true, composed: true, detail: { value: 'custom' } }))
      await element.updateComplete
      const customExpiration = secretDialog.querySelector<HTMLInputElement>('input[name="customExpiration"]')
      const customExpirationRange = { min: customExpiration?.min, max: customExpiration?.max }
      ;(secretDialog.querySelector('lv-select-menu') as HTMLElement).dispatchEvent(new CustomEvent('lv-select-change', { bubbles: true, composed: true, detail: { value: '90' } }))
      await element.updateComplete
      const form = secretDialog.querySelector('form') as HTMLFormElement
      ;(form.elements.namedItem('secretName') as HTMLInputElement).value = 'Warehouse sync'
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await element.updateComplete
      const owner = document.querySelector('lv-admin-page') as Element
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: owner } }))
      await element.updateComplete

      ;(root.querySelector('.entity-list-row-action') as HTMLButtonElement).click()
      await element.updateComplete
      const revokeDialog = root.querySelector('[data-service-account-dialog="revoke"]') as HTMLDialogElement
      ;(Array.from(revokeDialog.querySelectorAll<HTMLButtonElement>('button')).find((button) => button.textContent?.includes('I understand')) as HTMLButtonElement).click()
      await element.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: owner } }))
      await element.updateComplete

      ;(Array.from(root.querySelectorAll<HTMLButtonElement>('button')).find((button) => button.textContent?.includes('Delete service account')) as HTMLButtonElement).click()
      await element.updateComplete
      const deleteDialog = root.querySelector('[data-service-account-dialog="delete"]') as HTMLDialogElement
      ;(Array.from(deleteDialog.querySelectorAll<HTMLButtonElement>('button')).find((button) => button.textContent?.includes('I understand')) as HTMLButtonElement).click()
      await element.updateComplete
      return {
        initial,
        expirationLabel: (expiration.shadowRoot as ShadowRoot).querySelector('.value')?.textContent?.trim(),
        expirationOptions,
        customExpirationRange,
        commands,
        revokeTitle: revokeDialog.querySelector('h2')?.textContent?.trim(),
        deleteTitle: deleteDialog.querySelector('h2')?.textContent?.trim(),
      }
    })
    expect(result.initial).toEqual({
      sharedDetail: true,
      sharedCredentialList: true,
      ownTableCount: 0,
      headings: ['Overview', 'Credentials'],
      secretNotice: expect.stringContaining('Copy this credential now'),
      backHref: '/admin/service-accounts',
    })
    expect(result.expirationLabel).toMatch(/^90 days \(.+\)$/)
    expect(result.expirationOptions).toContain('Custom date')
    expect(result.expirationOptions).not.toContain('No expiration')
    expect(result.customExpirationRange.min).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(result.customExpirationRange.max).toMatch(/^\d{4}-\d{2}-\d{2}$/)
    expect(new Date(result.customExpirationRange.max as string).valueOf() - new Date(result.customExpirationRange.min as string).valueOf()).toBeLessThanOrEqual(363 * 24 * 60 * 60 * 1000)
    expect(result.commands).toHaveLength(3)
    expect(result.commands[0]).toMatchObject({ action: 'create_secret', accountId: 'svc-1', secretName: 'Warehouse sync' })
    expect((result.commands[0] as { expiresAt: string }).expiresAt).toMatch(/^\d{4}-\d{2}-\d{2}T/)
    expect(result.commands[1]).toEqual({ action: 'revoke_secret', accountId: 'svc-1', secretId: 'secret-1' })
    expect(result.commands[2]).toEqual({ action: 'delete', accountId: 'svc-1' })
    expect(result.revokeTitle).toBe('Revoke this credential?')
    expect(result.deleteTitle).toBe('Delete this service account?')
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
    expect(result).toEqual({ accountDisabled: true, auditDisabled: true, accountStillDisabled: true, auditStillDisabled: true, auditLabels: ['Project', 'Actor', 'Resource ID', 'From', 'To'], accountUnlocked: true, auditUnlocked: true })
  } finally { await page.close() }
})

test('audit log uses shared filters and record tables with readable event values', async () => {
  const page = await browser.newPage()
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-audit-log') && customElements.get('lv-record-table') && customElements.get('lv-select-menu'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state = {
        items: [
          { id: 'event-1', action: 'service_principal_secret.created', principalId: 'admin-1', principalName: 'Platform Admin', principalEmail: 'admin@example.com', resourceKind: 'service_principal_secret', resourceId: 'secret-1', capability: 'manage_credentials', status: 'success', createdAt: '2026-08-02T11:00:00Z' },
          { id: 'event-2', action: 'principal.blocked', resourceKind: 'principal', resourceId: 'user-1', status: 'failed', createdAt: '2026-08-01T09:30:00Z' },
        ],
        filters: {}, loadedCount: 2, loading: false, hasMore: true, nextCursor: 'cursor-2',
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAuditLog: state }, getPath: (path: string) => path === 'adminAuditLog' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-audit-log') as any
      element.requestUpdate(); await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const table = root.querySelector('lv-record-table') as HTMLElement
      const commands: unknown[] = []
      element.addEventListener('lv-audit-log-command', (event: CustomEvent) => commands.push(event.detail))
      const actionMenu = root.querySelector('lv-select-menu') as any
      await actionMenu.updateComplete
      ;(actionMenu.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.trigger')?.click()
      await actionMenu.updateComplete
      ;(actionMenu.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('[data-value="principal.blocked"]')?.click()
      await element.updateComplete
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: document.querySelector('lv-admin-page') } }))
      await element.updateComplete
      const beforeLoadMore = {
        headings: Array.from(root.querySelectorAll('h2')).map((heading) => heading.textContent?.trim()),
        hasRecordTable: Boolean(table),
        ownRawTables: root.querySelectorAll(':scope > .table-wrap, :scope > table').length,
        filterMenus: root.querySelectorAll('lv-select-menu').length,
        headers: Array.from(table.querySelectorAll('thead th')).map((header) => header.textContent?.replace(/\s+/g, ' ').trim()),
        rows: Array.from(table.querySelectorAll('tbody tr')).map((row) => row.textContent?.replace(/\s+/g, ' ').trim()),
        loadMoreLabel: root.querySelector('.audit-load-more')?.textContent?.trim(),
      }
      ;(root.querySelector('.audit-load-more') as HTMLButtonElement).click()
      return { beforeLoadMore, commands }
    })
    expect(result.beforeLoadMore.headings).toEqual([])
    expect(result.beforeLoadMore.hasRecordTable).toBe(true)
    expect(result.beforeLoadMore.ownRawTables).toBe(0)
    expect(result.beforeLoadMore.filterMenus).toBe(2)
    expect(result.beforeLoadMore.headers).toEqual(['Time', 'Action', 'Actor', 'Resource', 'Capability', 'Status'])
    expect(result.beforeLoadMore.rows[0]).toContain('Service principal secret created')
    expect(result.beforeLoadMore.rows[0]).toContain('Platform Admin')
    expect(result.beforeLoadMore.rows[0]).toContain('admin@example.com')
    expect(result.beforeLoadMore.rows[0]).not.toContain('admin-1')
    expect(result.beforeLoadMore.rows[0]).not.toContain('2026-08-02T11:00:00Z')
    expect(result.beforeLoadMore.rows[0]).not.toContain('service_principal_secret.created')
    expect(result.beforeLoadMore.rows[0]).not.toContain('secret-1')
    expect(result.beforeLoadMore.rows[0]).toContain('Manage credentials')
    expect(result.beforeLoadMore.rows[0]).toContain('Succeeded')
    expect(result.beforeLoadMore.rows[1]).toContain('System')
    expect(result.beforeLoadMore.loadMoreLabel).toBe('Load more')
    expect(result.commands[0]).toEqual({ action: 'filter', filters: { action: 'principal.blocked' } })
    expect(result.commands[1]).toEqual({ action: 'load_more', filters: {}, pageToken: 'cursor-2' })
  } finally { await page.close() }
})

test('audit rows open a non-modal detail drawer and quick presets keep the command contract', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 844 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-audit-log') && customElements.get('lv-drawer'))
    const result = await page.evaluate(async () => {
      const runtime = await import('/settings-surfaces.js') as any
      const state = {
        items: [{
          id: 'event-detail-1', action: 'principal.blocked', principalId: 'admin-1', principalName: 'Platform Admin', principalEmail: 'admin@example.com', projectId: 'sales',
          resourceKind: 'principal', resourceId: 'user-1', capability: 'manage_identity', status: 'failed',
          requestId: 'request-1', correlationId: 'correlation-1', metadata: { reason: 'too many attempts', count: 3 },
          createdAt: '2026-08-02T11:00:00.123Z',
        }],
        filters: {}, loadedCount: 1, loading: false, hasMore: false,
      }
      runtime.setDatastarLitRuntimeForTests?.({ root: { adminAuditLog: state }, getPath: (path: string) => path === 'adminAuditLog' ? state : undefined, effect: (fn: () => void) => { fn(); return () => {} } })
      const element = document.querySelector('lv-audit-log') as any
      element.requestUpdate(); await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const commands: unknown[] = []
      element.addEventListener('lv-audit-log-command', (event: CustomEvent) => commands.push(event.detail))

      ;(root.querySelector('tbody tr.record-row') as HTMLElement).click()
      await element.updateComplete
      const drawer = root.querySelector('lv-drawer') as any
      const drawerPanel = drawer?.shadowRoot?.querySelector('.drawer') as HTMLElement | null
      const drawerText = drawer?.textContent?.replace(/\s+/g, ' ').trim() ?? ''
      const rawMetadata = drawer?.querySelector('.audit-drawer-metadata') as HTMLDetailsElement | null
      rawMetadata?.querySelector('summary')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await element.updateComplete

      const failedPreset = root.querySelector<HTMLButtonElement>('[data-audit-preset="failed"]')
      const rolePreset = root.querySelector<HTMLButtonElement>('[data-audit-preset="access"]')
      rolePreset?.click()
      const roleCommand = commands.at(-1) as { action?: string; filters?: Record<string, unknown> }
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: document.querySelector('lv-admin-page') } }))
      await element.updateComplete
      const serviceAccountPreset = root.querySelector<HTMLButtonElement>('[data-audit-preset="credentials"]')
      serviceAccountPreset?.click()
      const serviceAccountCommand = commands.at(-1) as { action?: string; filters?: Record<string, unknown> }
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: document.querySelector('lv-admin-page') } }))
      await element.updateComplete
      failedPreset?.click()
      const failedCommand = commands.at(-1) as { action?: string; filters?: Record<string, unknown> }
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'finished', el: document.querySelector('lv-admin-page') } }))
      await element.updateComplete
      const drawerClose = (drawer?.shadowRoot as ShadowRoot | null)?.querySelector('.close') as HTMLButtonElement | null
      drawerClose?.click()
      await element.updateComplete
      const drawerLinks = drawer?.querySelectorAll('.audit-drawer-fact a') as NodeListOf<HTMLAnchorElement> | undefined

      return {
        drawerText,
        rawMetadataOpen: rawMetadata?.open,
        drawerIsNonModal: drawer?.modal === false && drawerPanel?.getAttribute('aria-modal') === null,
        actorHref: drawer?.querySelector('.audit-drawer-fact a')?.getAttribute('href'),
        resourceHref: drawerLinks?.[drawerLinks.length - 1]?.getAttribute('href'),
        presetLabels: Array.from(root.querySelectorAll('[data-audit-preset]')).map((button) => button.textContent?.trim()),
        roleCommand,
        serviceAccountCommand,
        failedCommand,
        failedRows: root.querySelectorAll('tbody tr.record-row').length,
        drawerClosed: !root.querySelector('lv-drawer'),
      }
    })
    expect(result.drawerText).toContain('Principal blocked')
    expect(result.drawerText).toContain('Platform Admin')
    expect(result.drawerText).toContain('admin@example.com')
    expect(result.drawerText).toContain('admin-1')
    expect(result.drawerText).toContain('event-detail-1')
    expect(result.drawerText).toContain('2026-08-02T11:00:00.123Z')
    expect(result.drawerText).toContain('Manage identity')
    expect(result.drawerText).toContain('Failed')
    expect(result.drawerText).toContain('Raw metadata')
    expect(result.rawMetadataOpen).toBe(true)
    expect(result.drawerIsNonModal).toBe(true)
    expect(result.actorHref).toBe('/admin/principals/admin-1')
    expect(result.resourceHref).toBe('/admin/principals/user-1')
    expect(result.presetLabels).toEqual(['Security', 'Role changes', 'Service accounts', 'Failed events'])
    expect(result.roleCommand).toEqual({ action: 'filter', filters: { resourceKind: 'role_binding' } })
    expect(result.serviceAccountCommand).toEqual({ action: 'filter', filters: { resourceKind: 'service_principal' } })
    expect(result.failedCommand).toEqual({ action: 'filter', filters: {} })
    expect(result.failedRows).toBe(1)
    expect(result.drawerClosed).toBe(true)
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
