import { afterAll, beforeAll, expect, test } from 'bun:test'
import { createServer, type Server } from 'node:http'
import { readFile } from 'node:fs/promises'
import { join, normalize } from 'node:path'
import { chromium, type Browser } from '@playwright/test'
import { typographyTestTokens } from '../test-typography-tokens'

let server: Server
let baseURL = ''
let browser: Browser

const projectRoot = process.cwd()
const root = join(projectRoot, '.tmp/admin-page-test')

beforeAll(async () => {
  server = createServer(async (request, response) => {
    const url = new URL(request.url ?? '/', 'http://127.0.0.1')
    if (url.pathname === '/') {
      response.setHeader('content-type', 'text/html')
      response.end(testDocument())
      return
    }
    const fileRoot = url.pathname.startsWith('/static/vendor/') ? projectRoot : root
    const file = normalize(join(fileRoot, url.pathname))
    if (!file.startsWith(fileRoot)) {
      response.writeHead(404)
      response.end('not found')
      return
    }
    try {
      response.setHeader('content-type', file.endsWith('.css') ? 'text/css' : 'text/javascript')
      response.end(await readFile(file))
    } catch {
      response.writeHead(404)
      response.end('not found')
    }
  })
  await new Promise<void>((resolve) => server.listen(0, resolve))
  const address = server.address()
  if (!address || typeof address === 'string') throw new Error('test server did not bind to a port')
  baseURL = `http://127.0.0.1:${address.port}`
  browser = await chromium.launch()
})

afterAll(async () => {
  await browser?.close()
  await new Promise<void>((resolve, reject) => server.close((error) => error ? reject(error) : resolve()))
}, 15_000)

test('publications admin renders lifecycle controls and emits typed commands', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'Publications', active: 'publications', headerTitle: 'Publications',
        headerDetail: 'Public dashboard lifecycle.',
        sidebar: { label: 'Admin', railLabel: 'Admin', ariaLabel: 'Admin navigation', storageKey: 'admin', activeId: 'publications', numbered: false, collapsible: false, items: [{ id: 'publications', title: 'Publications', href: '/admin/publications', active: true }] },
        publications: [{ workspaceId: 'visuals', name: 'website-showcase', dashboard: 'visual-showcase', defaultPage: 'overview', status: 'active', origins: ['https://leapview.dev'], generation: 'state-2', publicUrl: 'https://app.leapview.dev/public/dashboards/id', embedUrl: 'https://app.leapview.dev/embed/dashboards/id', iframeSnippet: '<iframe></iframe>', configuredAt: '2026-07-20', history: ['2026-07-20 · configured · owner'] }],
      } })
      const element = document.querySelector('lv-admin-page') as any
      await element.updateComplete
      let detail: unknown = null
      element.addEventListener('lv-publication-command', (event: CustomEvent) => { detail = event.detail })
      const buttons = Array.from(element.shadowRoot.querySelectorAll('button')) as HTMLButtonElement[]
      buttons.find((button) => button.textContent?.trim() === 'Suspend')?.click()
      return {
        text: element.shadowRoot.textContent.replace(/\s+/g, ' ').trim(),
        cards: element.shadowRoot.querySelectorAll('.publication-card').length,
        detail,
      }
    })
    expect(state.cards).toBe(1)
    expect(state.text).toContain('website-showcase')
    expect(state.text).toContain('Lifecycle history')
    expect(state.detail).toEqual({ workspaceId: 'visuals', publication: 'website-showcase', action: 'suspend' })
  } finally {
    await page.close()
  }
})

test('profile settings renders the signed-in identity and editable local fields', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'Profile', active: 'profile', headerTitle: 'Profile', headerDetail: 'Manage your photo and display name.',
      }, personalSettings: {
        active: 'profile',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', avatarUrl: '/profile/avatar.png', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], scopes: [] },
      } })
      const element = document.querySelector('lv-admin-page') as any
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await element.updateComplete
      const root = element.shadowRoot!
      const profile = root.querySelector('lv-personal-settings') as any
      await profile.updateComplete
      const profileRoot = profile.shadowRoot as ShadowRoot
      const main = root.querySelector('.main') as HTMLElement
      const route = root.querySelector('.route') as HTMLElement
      const header = root.querySelector('.page-header') as HTMLElement
      const avatarTrigger = profileRoot.querySelector('.avatar-trigger') as HTMLButtonElement
      const fieldLabel = profileRoot.querySelector('.settings-label') as HTMLElement
      avatarTrigger.click()
      await profile.updateComplete
      const avatarMenuItems = Array.from(profileRoot.querySelectorAll('[role="menuitem"]')).map((item) => item.textContent?.trim())
      const avatarMenuOpen = avatarTrigger.getAttribute('aria-expanded')
      avatarTrigger.focus()
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      await profile.updateComplete
      const avatarMenuClosed = !profileRoot.querySelector('[role="menu"]')
      const avatarTriggerFocused = profileRoot.activeElement === avatarTrigger
      let themeCommand: unknown = null
      let appliedTheme: unknown = null
      profile.addEventListener('lv-personal-theme-command', (event: CustomEvent) => { themeCommand = event.detail }, { once: true })
      document.addEventListener('leapview-theme-change', (event: CustomEvent) => { appliedTheme = event.detail?.mode }, { once: true })
      const theme = profileRoot.querySelector('select[name="theme"]') as HTMLSelectElement
      avatarTrigger.click()
      await profile.updateComplete
      theme.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, composed: true }))
      await profile.updateComplete
      const avatarMenuClosedOnOutsidePointer = !profileRoot.querySelector('[role="menu"]')
      theme.value = 'dark_colorblind'
      theme.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        text: profileRoot.textContent?.replace(/\s+/g, ' ').trim(),
        nestedHeadings: profileRoot.querySelectorAll('h2').length,
        mainCentered: Math.abs((main.getBoundingClientRect().left + main.getBoundingClientRect().width / 2) - (route.getBoundingClientRect().left + route.getBoundingClientRect().width / 2)) <= 1,
        mainWidth: Math.round(main.getBoundingClientRect().width),
        headerGap: Math.round(profile.getBoundingClientRect().top - header.getBoundingClientRect().bottom),
        fieldLabelFontSize: getComputedStyle(fieldLabel).fontSize,
        avatarMenuItems,
        avatarMenuOpen,
        avatarMenuClosed,
        avatarTriggerFocused,
        avatarMenuClosedOnOutsidePointer,
        hasHiddenFileInput: Boolean(profileRoot.querySelector('input[type="file"].avatar-input')),
        themeOptions: Array.from(profileRoot.querySelectorAll('select[name="theme"] option')).map((option) => option.textContent?.trim()),
        themeCommand,
        appliedTheme,
      }
    })
    expect(state.title).toBe('Profile')
    expect(state.text).toContain('Profile picture')
    expect(state.text).toContain('jacob@example.com')
    expect(state.text).toContain('Display name')
    expect(state.text).toContain('Theme')
    expect(state.themeOptions).toEqual([
      'System',
      'Light default',
      'Dark default',
      'Soft dark',
      'Light protanopia and deuteranopia',
      'Dark protanopia and deuteranopia',
      'Light tritanopia',
      'Dark tritanopia',
    ])
    expect(state.themeCommand).toEqual({ action: 'save', theme: 'dark_colorblind' })
    expect(state.appliedTheme).toBe('dark_colorblind')
    expect(state.nestedHeadings).toBe(0)
    expect(state.mainCentered).toBe(true)
    expect(state.mainWidth).toBe(640)
    expect(state.headerGap).toBeGreaterThanOrEqual(16)
    expect(state.fieldLabelFontSize).toBe('14px')
    expect(state.avatarMenuItems).toEqual(['Change avatar', 'Remove avatar'])
    expect(state.avatarMenuOpen).toBe('true')
    expect(state.avatarMenuClosed).toBe(true)
    expect(state.avatarTriggerFocused).toBe(true)
    expect(state.avatarMenuClosedOnOutsidePointer).toBe(true)
    expect(state.hasHiddenFileInput).toBe(true)
  } finally {
    await page.close()
  }
})

test('personal API tokens use authorized scope and permission selectors', async () => {
  const page = await browser.newPage({ viewport: { width: 1440, height: 700 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'API tokens', active: 'api-tokens', headerTitle: 'API tokens', headerDetail: 'Manage personal API and CLI credentials.',
      }, personalSettings: {
        active: 'api-tokens',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], scopes: [
          { kind: 'workspace', workspaceId: 'sales', label: 'Sales analytics', description: 'Revenue and pipeline reporting.', privileges: [
            { value: 'USE_WORKSPACE', label: 'Use workspace', description: 'Open and use the workspace.', category: 'Workspace' },
            { value: 'VIEW_ITEM', label: 'View content', description: 'View dashboards and other workspace content.', category: 'Workspace' },
            { value: 'EDIT_ITEM', label: 'Edit content', description: 'Create and update workspace content.', category: 'Workspace' },
            { value: 'MANAGE_ITEM', label: 'Manage content', description: 'Delete and administer workspace content.', category: 'Workspace' },
            { value: 'QUERY_DATA', label: 'Query data', description: 'Run governed queries against workspace data.', category: 'Data' },
            { value: 'PREVIEW_DATA', label: 'Preview data', description: 'Preview source and model data.', category: 'Data' },
            { value: 'REFRESH_DATA', label: 'Refresh data', description: 'Start and manage data refreshes.', category: 'Data' },
            { value: 'VIEW_DATA', label: 'View managed data', description: 'View managed-data metadata and revisions.', category: 'Data' },
            { value: 'INGEST_DATA', label: 'Ingest data', description: 'Upload and ingest managed data.', category: 'Data' },
            { value: 'AUTHOR_PROJECT', label: 'Author project', description: 'Create and synchronize project candidates.', category: 'Projects and releases' },
            { value: 'PUBLISH_RELEASE', label: 'Publish releases', description: 'Publish project releases.', category: 'Projects and releases' },
            { value: 'USE_AGENT', label: 'Use agent', description: 'Start and continue agent conversations.', category: 'Agent' },
            { value: 'MANAGE_PUBLICATIONS', label: 'Manage publications', description: 'Configure and control public dashboards.', category: 'Administration' },
          ] },
          { kind: 'workspace', workspaceId: 'operations', label: 'Operations', description: 'Access limited to this workspace.', privileges: [
            { value: 'USE_WORKSPACE', label: 'Use workspace', description: 'Open and use the workspace.', category: 'Workspace' },
          ] },
        ] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = admin.shadowRoot.querySelector('lv-personal-settings') as any
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      const scope = root.querySelector('#token-scope') as HTMLSelectElement
      const name = root.querySelector('#token-name') as HTMLInputElement
      const create = root.querySelector('button[type="submit"]') as HTMLButtonElement
      const initial = {
        scopeOptions: Array.from(scope.options).map((option) => option.textContent?.trim()),
        createDisabled: create.disabled,
        rawWorkspaceField: Boolean(root.querySelector('input[placeholder*="Workspace ID"]')),
        rawPrivilegeField: Boolean(root.querySelector('input[placeholder*="Privileges"]')),
      }

      scope.value = 'workspace:sales'
      scope.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      name.value = 'Sales automation'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await personal.updateComplete
      const add = root.querySelector('.permission-trigger') as HTMLButtonElement
      add.click()
      await personal.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const menu = root.querySelector('.permission-menu') as HTMLElement
      const permissionList = root.querySelector('.permission-list') as HTMLElement
      const menuRect = menu.getBoundingClientRect()
      const menuLayout = {
        bottom: Math.round(menuRect.bottom),
        viewportHeight: innerHeight,
        listScrollable: permissionList.scrollHeight > permissionList.clientHeight,
        listOverflowY: getComputedStyle(permissionList).overflowY,
      }
      const search = root.querySelector('.permission-search input') as HTMLInputElement
      const searchFocused = root.activeElement === search
      search.value = 'query'
      search.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await personal.updateComplete
      const filteredPermissions = Array.from(root.querySelectorAll('.permission-option .settings-label')).map((label) => label.textContent?.trim())
      const queryPermission = root.querySelector('input[type="checkbox"][value="QUERY_DATA"]') as HTMLInputElement
      queryPermission.click()
      await personal.updateComplete
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      await personal.updateComplete
      await Promise.resolve()
      const scopeDescription = root.querySelector('.scope-description')?.textContent?.trim()
      const selectedPermissions = Array.from(root.querySelectorAll('.selected-permission .settings-label')).map((label) => label.textContent?.trim())
      const triggerFocused = root.activeElement === add

      let command: unknown = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      const form = root.querySelector('.token-form') as HTMLFormElement
      form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      const pending = {
        name: (root.querySelector('#token-name') as HTMLInputElement).value,
        selectedPermissions: root.querySelectorAll('.selected-permission').length,
        buttonText: (root.querySelector('button[type="submit"]') as HTMLButtonElement).textContent?.trim(),
      }
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', argsRaw: { status: '403' } } }))
      await personal.updateComplete
      const failed = {
        name: (root.querySelector('#token-name') as HTMLInputElement).value,
        selectedPermissions: root.querySelectorAll('.selected-permission').length,
        error: root.querySelector('[role="alert"]')?.textContent?.trim(),
        createDisabled: (root.querySelector('button[type="submit"]') as HTMLButtonElement).disabled,
      }
      form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      mergePatch({ personalSettings: { tokens: { items: [
        { id: 'token-1', name: 'Sales automation', workspaceId: 'sales', privileges: ['QUERY_DATA'], createdAt: '2026-08-12T06:40:00Z' },
      ], newToken: 'lv_created_secret' } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await personal.updateComplete
      const succeeded = {
        name: (root.querySelector('#token-name') as HTMLInputElement).value,
        selectedPermissions: root.querySelectorAll('.selected-permission').length,
        tokenNames: Array.from(root.querySelectorAll('.card:last-child .settings-label')).map((element) => element.textContent?.trim()),
        notice: root.querySelector('[role="status"]')?.textContent?.trim(),
      }
      return {
        initial,
        menuLayout,
        scopeDescription,
        filteredPermissions,
        selectedPermissions,
        menuClosed: !root.querySelector('.permission-menu'),
        searchFocused,
        triggerFocused,
        command,
        pending,
        failed,
        succeeded,
      }
    })

    await page.setViewportSize({ width: 390, height: 700 })
    const mobile = await page.evaluate(async () => {
      const admin = document.querySelector('lv-admin-page') as any
      const personal = admin.shadowRoot.querySelector('lv-personal-settings') as any
      const root = personal.shadowRoot as ShadowRoot
      const scope = root.querySelector('#token-scope') as HTMLSelectElement
      scope.value = 'workspace:sales'
      scope.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await personal.updateComplete
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      const menu = root.querySelector('.permission-menu') as HTMLElement
      const close = root.querySelector('.permission-menu-close') as HTMLButtonElement
      const rect = menu.getBoundingClientRect()
      const state = {
        position: getComputedStyle(menu).position,
        left: Math.round(rect.left), right: Math.round(rect.right), bottom: Math.round(rect.bottom),
        viewportWidth: innerWidth, viewportHeight: innerHeight,
      }
      close.click()
      await personal.updateComplete
      return { ...state, closed: !root.querySelector('.permission-menu') }
    })

    expect(state.initial).toEqual({
      scopeOptions: ['Choose a scope', 'Sales analytics', 'Operations'],
      createDisabled: true,
      rawWorkspaceField: false,
      rawPrivilegeField: false,
    })
    expect(state.scopeDescription).toBe('Revenue and pipeline reporting.')
    expect(state.menuLayout.bottom).toBeLessThanOrEqual(state.menuLayout.viewportHeight - 16)
    expect(state.menuLayout.listScrollable).toBe(true)
    expect(state.menuLayout.listOverflowY).toBe('auto')
    expect(state.filteredPermissions).toEqual(['Query data'])
    expect(state.selectedPermissions).toEqual(['Query data'])
    expect(state.menuClosed).toBe(true)
    expect(state.searchFocused).toBe(true)
    expect(state.triggerFocused).toBe(true)
    expect(state.command).toMatchObject({
      action: 'create', name: 'Sales automation', workspaceId: 'sales', privileges: ['QUERY_DATA'],
    })
    expect(state.pending).toEqual({ name: 'Sales automation', selectedPermissions: 1, buttonText: 'Creating…' })
    expect(state.failed).toEqual({
      name: 'Sales automation', selectedPermissions: 1,
      error: 'Token creation failed because this page expired. Reload the page and try again.',
      createDisabled: false,
    })
    expect(state.succeeded.name).toBe('')
    expect(state.succeeded.selectedPermissions).toBe(0)
    expect(state.succeeded.tokenNames).toContain('Sales automation')
    expect(state.succeeded.notice).toContain('Copy this token now')
    expect(mobile.position).toBe('fixed')
    expect(mobile.left).toBeGreaterThanOrEqual(16)
    expect(mobile.right).toBeLessThanOrEqual(mobile.viewportWidth - 16)
    expect(mobile.bottom).toBeLessThanOrEqual(mobile.viewportHeight - 16)
    expect(mobile.closed).toBe(true)
  } finally {
    await page.close()
  }
})

test('members directory list delegates search and filtering to the page stream', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-entity-list'))
      const state = await page.evaluate(async () => {
        const admin = document.querySelector('lv-admin-page') as any
        const root = admin.shadowRoot as ShadowRoot
        const list = root.querySelector('lv-entity-list') as any
      if (!list) throw new Error('members entity list was not rendered')
      await list.updateComplete
      const rows = () => Array.from(root.querySelectorAll('.entity-list-table-row')).map((row: Element) => row.textContent?.replace(/\s+/g, ' ').trim())
      const initial = {
        title: root.querySelector('table')?.getAttribute('aria-label'),
        avatarSrc: (root.querySelector('lv-user-avatar') as any)?.shadowRoot?.querySelector('img')?.getAttribute('src'),
        fallbackInitials: (() => {
          const avatars = Array.from(root.querySelectorAll('lv-user-avatar')) as any[]
          return avatars.find((avatar) => avatar.name === 'Local Developer')?.shadowRoot?.textContent?.trim()
        })(),
        groupRows: root.querySelectorAll('.entity-list-group-row').length,
        rows: rows(),
        headers: Array.from(root.querySelectorAll('thead th .entity-list-sort-button > span:first-child')).map((header) => header.textContent?.trim()),
        filterOptions: Array.from(root.querySelectorAll('select option')).map((option) => option.textContent?.trim()),
        lastSeenCells: Array.from(root.querySelectorAll('.entity-list-table-row td:last-child')).map((cell) => ({
          text: cell.textContent?.trim(),
          title: cell.getAttribute('title'),
        })),
        toolbarActions: Array.from(root.querySelectorAll('.entity-toolbar-actions button')).map((button) => button.textContent?.replace(/\s+/g, ' ').trim()),
      }
      const input = root.querySelector('input[type="search"]') as HTMLInputElement
      input.value = 'analyst'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await list.updateComplete
      const filtered = rows()
      const select = root.querySelector('select') as HTMLSelectElement
      select.value = 'inactive'
      select.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await list.updateComplete
      const inactiveRows = rows()
      const lastSeenSort = Array.from(root.querySelectorAll<HTMLButtonElement>('.entity-list-sort-button'))
        .find((button) => button.textContent?.includes('Last seen'))
      lastSeenSort?.click()
      await list.updateComplete
      return { initial, filtered, inactiveRows, sortedRows: rows() }
    })

    expect(state.initial.title).toBe('Members')
    expect(state.initial.avatarSrc).toBe('/profile/avatars/p1/avatar-digest')
    expect(state.initial.fallbackInitials).toBe('LD')
    expect(state.initial.groupRows).toBe(0)
    expect(state.initial.headers).toEqual(['Name', 'Email', 'Status', 'Teams', 'Joined', 'Last seen'])
    expect(state.initial.filterOptions).toEqual(['All', 'Active', 'Inactive'])
    expect(state.initial.lastSeenCells[0]?.text).toMatch(/^5m ago$/)
    expect(state.initial.lastSeenCells[0]?.title).toContain('UTC')
    expect(state.initial.lastSeenCells[1]).toEqual({ text: 'Never', title: '' })
    expect(state.initial.toolbarActions).toEqual(['Export CSV', 'Create local user'])
    expect(state.initial.rows).toHaveLength(2)
    // The list emits both changes but keeps the last server payload visible
    // until the page stream sends the filtered groups back.
    expect(state.filtered).toEqual(state.initial.rows)
    expect(state.inactiveRows).toEqual(state.initial.rows)
    expect(state.sortedRows[0]).toContain('Local Developer')
  } finally {
    await page.close()
  }
})

test('groups admin uses the reusable entity list and delegates search to the page stream', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-entity-list'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'Groups', active: 'groups', headerTitle: 'Groups', headerDetail: '',
        sections: [{ title: 'Groups', table: {
          columns: [], empty: 'No groups found.', rows: [
            { name: 'Operations Group', name_href: '/admin/groups/operations', provider: 'local', external_id: 'operations', member_count: 3, id: 'operations' },
            { name: 'Finance Group', name_href: '/admin/groups/finance', provider: 'scim', external_id: 'finance', member_count: 1, id: 'finance' },
          ],
        } }],
      } })
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const root = admin.shadowRoot as ShadowRoot
      const input = root.querySelector('.entity-search input') as HTMLInputElement
      const rows = () => Array.from(root.querySelectorAll('.entity-list-table-row')).map((row) => row.textContent?.replace(/\s+/g, ' ').trim())
      const before = rows()
      input.value = 'operations'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await admin.updateComplete
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        before,
        after: rows(),
        filterOptions: Array.from(root.querySelectorAll('.entity-filter option')).map((option) => option.textContent?.trim()),
        href: root.querySelector('.entity-list-identity')?.getAttribute('href'),
        toolbarActions: Array.from(root.querySelectorAll('.entity-toolbar-actions button')).map((button) => button.textContent?.replace(/\s+/g, ' ').trim()),
      }
    })

    expect(state.title).toBe('Groups')
    expect(state.before).toHaveLength(2)
    expect(state.after).toEqual(state.before)
    expect(state.filterOptions).toEqual(['All', 'Local', 'Scim'])
    expect(state.href).toBe('/admin/groups/operations')
    expect(state.toolbarActions).toEqual(['Export CSV', 'Create group'])
  } finally {
    await page.close()
  }
})

test('group detail lets the shared detail shell own the page heading and summary', async () => {
  const page = await browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'Groups', active: 'group-detail', headerTitle: 'Groups / Analysts', headerDetail: 'Local groups are editable.',
        metrics: [{ label: 'Provider', value: 'local' }, { label: 'Member count', value: '1' }],
      } })
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const root = admin.shadowRoot as ShadowRoot
      return {
        pageHeader: Boolean(root.querySelector('.page-header')),
        metrics: Boolean(root.querySelector('.metrics')),
        detail: Boolean(root.querySelector('lv-group-administration')),
      }
    })
    expect(state).toEqual({ pageHeader: false, metrics: false, detail: true })
  } finally {
    await page.close()
  }
})

for (const viewport of [
  { name: 'desktop', width: 1440, height: 820 },
  { name: 'mobile', width: 390, height: 820 },
]) {
  test(`admin page composes route UI on ${viewport.name}`, async () => {
    const page = await browser.newPage({ viewport })
    try {
      await page.goto(baseURL)
      await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
      await page.locator('lv-admin-page').evaluate((element: any) => element.updateComplete)

      const state = await page.locator('lv-admin-page').evaluate(async (element: any) => {
        const root = element.shadowRoot
        const entityList = root.querySelector('lv-entity-list') as any
        await entityList?.updateComplete
        const main = root.querySelector('.main') as HTMLElement
        const mainRect = main.getBoundingClientRect()
        const routeRect = root.querySelector('.route')!.getBoundingClientRect()
        const isMobile = window.innerWidth <= 640
        return {
          title: root.querySelector('h1')?.textContent?.trim(),
          headerText: root.querySelector('header')?.textContent?.replace(/\s+/g, ' ').trim(),
          hasSubSidebar: Boolean(root.querySelector('lv-sub-sidebar')),
          hasEntityList: Boolean(entityList),
          mainCentered: isMobile || Math.abs((mainRect.left + mainRect.width / 2) - (routeRect.left + routeRect.width / 2)) <= 1,
          mainConstrained: isMobile || Math.round(mainRect.width) < Math.round(routeRect.width),
          hasRecordTable: Boolean(root.querySelector('lv-record-table')),
          recordTableVariant: root.querySelector('lv-record-table')?.getAttribute('variant'),
          documentOverflow: document.documentElement.scrollWidth - window.innerWidth,
          mainRight: Math.round(mainRect.right),
          routeRight: Math.round(routeRect.right),
          hasCreateLocalUserPanel: Boolean(root.querySelector('[aria-label="Create local user"]')),
          text: root.textContent,
        }
      })

      expect(state.title).toBe('Members')
      expect(state.headerText).toBe('Members')
      expect(state.hasSubSidebar).toBe(false)
      expect(state.hasEntityList).toBe(true)
      if (viewport.width > 640) {
        expect(state.mainCentered).toBe(true)
        expect(state.mainConstrained).toBe(true)
      }
      expect(state.hasRecordTable).toBe(false)
      expect(state.recordTableVariant).toBeUndefined()
      expect(state.hasCreateLocalUserPanel).toBe(false)
      expect(state.text ?? '').toMatch(/analyst@example\.com/)
      if (viewport.width <= 640) {
        expect(state.documentOverflow).toBe(0)
        expect(state.mainRight).toBeLessThanOrEqual(viewport.width)
        expect(state.routeRight).toBeLessThanOrEqual(viewport.width)
      }
    } finally {
      await page.close()
    }
  })
}

function queryAuditFixturePage() {
  const queryEvents = [
    {
      id: 'queryevent_1',
      workspaceId: 'sales',
      principalId: 'analyst',
      surface: 'api',
      operation: 'api_query',
      queryKind: 'semantic_aggregate',
      modelId: 'sales',
      target: 'orders',
      objectType: 'semantic_dataset',
      objectId: 'sales:orders',
      requestId: 'req_1',
      correlationId: 'corr_1',
      status: 'success',
      durationMs: 12,
      rowsReturned: 2,
      error: '',
      sql: 'select status from orders',
      planText: 'orders plan',
      queryJson: '{"workspaceId":"sales","target":"orders"}',
      createdAt: '2026-07-02T10:00:00Z',
    },
    {
      id: 'queryevent_2',
      workspaceId: 'operations',
      principalId: 'agent',
      surface: 'agent',
      operation: 'agent_query',
      queryKind: 'semantic_rows',
      modelId: 'operations',
      target: 'customers',
      objectType: 'agent_tool',
      objectId: 'query_semantic_dataset',
      requestId: 'call_1',
      correlationId: '',
      status: 'error',
      durationMs: 4,
      rowsReturned: 0,
      error: 'invalid field',
      sql: '',
      planText: '',
      queryJson: '{"workspaceId":"operations","target":"customers"}',
      createdAt: '2026-07-02T10:01:00Z',
    },
  ]
  return {
    kind: 'admin',
    title: 'Query History',
    active: 'queries',
    sidebar: {
      label: 'Admin',
      railLabel: 'Admin',
      ariaLabel: 'Admin navigation',
      storageKey: 'leapview-admin-sidebar-collapsed',
      activeId: 'queries',
      collapsible: false,
      numbered: false,
      items: [{ id: 'queries', title: 'Query History', href: '/admin/queries', active: true }],
    },
    headerTitle: 'Query History',
    headerDetail: 'Product query audit.',
    queryHistory: {
      table: queryAuditTableFixture(queryEvents),
      filterMenus: queryAuditFilterMenusFixture(),
      filters: {},
      nextCursor: 'cursor_next',
      loadedCountLabel: '2 queries loaded',
      hasMore: true,
      loading: false,
      error: '',
      limit: 50,
    },
    queryDetail: {
      eventId: 'queryevent_1',
      loading: false,
      error: '',
      status: 'success',
      statusLabel: 'Success',
      workspaceId: 'sales',
      principalId: 'analyst',
      surface: 'api',
      operation: 'api_query',
      queryKind: 'semantic_aggregate',
      modelId: 'sales',
      target: 'orders',
      objectType: 'semantic_dataset',
      objectId: 'sales:orders',
      requestId: 'req_1',
      correlationId: 'corr_1',
      durationMs: 12,
      rowsReturned: 2,
      queryError: '',
      sql: 'select status from orders',
      planText: 'orders plan',
      queryJson: '{"workspaceId":"sales","target":"orders"}',
      createdAt: '2026-07-02T10:00:00Z',
    },
  }
}

function queryAuditFilterMenusFixture() {
  return [
    {
      id: 'workspace',
      label: 'Workspace',
      summaryLabel: 'Workspace',
      mode: 'multi',
      search: '',
      selected: [],
      loading: false,
      error: '',
      placeholder: 'Search workspaces',
      emptyLabel: 'No workspaces found.',
      options: [
        { value: 'sales', label: 'sales', icon: 'workspace', countLabel: '1', selected: false, disabled: false },
        { value: 'operations', label: 'operations', icon: 'workspace', countLabel: '1', selected: false, disabled: false },
      ],
    },
    {
      id: 'principal',
      label: 'User',
      summaryLabel: 'User',
      mode: 'multi',
      search: '',
      selected: [],
      loading: false,
      error: '',
      placeholder: 'Search users',
      emptyLabel: 'No users found.',
      options: [
        { value: 'analyst', label: 'Me (analyst@example.com)', icon: 'user', countLabel: '1', selected: false, disabled: false },
        { value: 'agent', label: 'agent', icon: 'user', countLabel: '1', selected: false, disabled: false },
      ],
    },
    {
      id: 'surface',
      label: 'Source type',
      summaryLabel: 'Source type',
      mode: 'multi',
      search: '',
      selected: [],
      loading: false,
      error: '',
      placeholder: 'Search source types',
      emptyLabel: 'No source types found.',
      options: [
        { value: 'api', label: 'api', icon: 'source', countLabel: '1', selected: false, disabled: false },
        { value: 'agent', label: 'agent', icon: 'source', countLabel: '1', selected: false, disabled: false },
      ],
    },
    {
      id: 'kind',
      label: 'Kind',
      summaryLabel: 'Kind',
      mode: 'multi',
      search: '',
      selected: [],
      loading: false,
      error: '',
      placeholder: 'Search kinds',
      emptyLabel: 'No kinds found.',
      options: [
        { value: 'semantic_aggregate', label: 'semantic_aggregate', icon: 'kind', countLabel: '1', selected: false, disabled: false },
        { value: 'semantic_rows', label: 'semantic_rows', icon: 'kind', countLabel: '1', selected: false, disabled: false },
      ],
    },
    {
      id: 'status',
      label: 'Status',
      summaryLabel: 'Status',
      mode: 'multi',
      search: '',
      selected: [],
      loading: false,
      error: '',
      placeholder: 'Search statuses',
      emptyLabel: 'No statuses found.',
      options: [
        { value: 'success', label: 'success', icon: 'status', countLabel: '1', selected: false, disabled: false },
        { value: 'error', label: 'error', icon: 'status', countLabel: '1', selected: false, disabled: false },
      ],
    },
  ]
}

function queryAuditTableFixture(events: any[]) {
  return {
    columns: [
      { id: 'query', header: 'Query', kind: 'query', width: '560px', toggleable: false },
      { id: 'started_at', header: 'Started', width: '150px' },
      { id: 'duration_ms', header: 'Duration', kind: 'number', align: 'right', width: '105px' },
      { id: 'source', header: 'Source type', width: '120px' },
      { id: 'runtime', header: 'Runtime', kind: 'code', width: '130px' },
      { id: 'principal_id', header: 'User', kind: 'code', width: '150px' },
      { id: 'rows_returned', header: 'Rows', kind: 'number', align: 'right', width: '90px' },
      { id: 'operation', header: 'Operation', kind: 'code', width: '145px' },
      { id: 'kind', header: 'Kind', kind: 'code', width: '170px' },
      { id: 'model', header: 'Model', kind: 'code', width: '130px' },
      { id: 'target', header: 'Target', kind: 'code', width: '150px' },
      { id: 'object', header: 'Object', kind: 'code', width: '220px' },
      { id: 'request_id', header: 'Request ID', kind: 'code', width: '170px' },
      { id: 'correlation_id', header: 'Correlation ID', kind: 'code', width: '170px' },
      { id: 'error', header: 'Error', kind: 'code', width: '220px' },
    ],
    rows: events.map((event) => ({
      id: event.id,
      query: {
        label: event.sql || `${event.operation} · ${event.queryKind} · ${event.modelId}.${event.target}`,
        statusLabel: event.status,
        tone: event.status === 'success' ? 'success' : 'danger',
        icon: event.status === 'success' ? 'check' : 'x',
        expandedContent: event.sql || `${event.operation} · ${event.queryKind}`,
      },
      started_at: event.createdAt,
      duration_ms: { label: `${event.durationMs ?? 0} ms`, value: event.durationMs ?? 0 },
      source: event.surface,
      runtime: event.workspaceId || '-',
      principal_id: event.principalId,
      rows_returned: event.rowsReturned,
      operation: event.operation,
      kind: event.queryKind,
      model: event.modelId,
      target: event.target,
      object: [event.objectType, event.objectId].filter(Boolean).join(':') || '-',
      request_id: event.requestId,
      correlation_id: event.correlationId,
      error: event.error,
    })),
    empty: 'No query events match these filters.',
    minWidth: '1305px',
    density: 'tight',
    rowAction: 'detail',
    columnSelector: {
      enabled: true,
      label: 'Columns',
      defaultColumns: ['started_at', 'duration_ms', 'source', 'runtime', 'principal_id', 'rows_returned'],
    },
  }
}

test('query audit page filters table rows and exposes optional metadata columns', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
    const state = await page.evaluate(async (fixture) => {
      localStorage.removeItem('leapview-admin-query-events-columns')
      const element = document.createElement('lv-admin-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: fixture, adminQueryHistory: fixture.queryHistory, adminQueryDetail: { eventId: '', loading: false, error: '' } })
      ;(window as any).queryHistoryCommands = []
      element.addEventListener('lv-query-history-command', (event: CustomEvent) => {
        ;(window as any).queryHistoryCommands.push(event.detail)
      })
      document.body.replaceChildren(element)
      await element.updateComplete
      const root = element.shadowRoot
      const search = root.querySelector<HTMLInputElement>('#query-filter-search')!
      search.value = 'select status'
      search.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 250))
      await element.updateComplete
      const commandAfterSearch = (window as any).queryHistoryCommands.at(-1)
      const menus = Array.from(root.querySelectorAll('lv-filter-menu')) as any[]
      menus[0]?.shadowRoot?.querySelector<HTMLButtonElement>('.trigger')?.click()
      await menus[0]?.updateComplete
      const workspaceMenuSearch = menus[0]?.shadowRoot?.querySelector<HTMLInputElement>('.search input')
      if (workspaceMenuSearch) {
        workspaceMenuSearch.value = 'oper'
        workspaceMenuSearch.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      }
      await new Promise((resolve) => setTimeout(resolve, 250))
      await element.updateComplete
      const filterSearchCommand = (window as any).queryHistoryCommands.at(-1)
      menus[2]?.shadowRoot?.querySelector<HTMLButtonElement>('.trigger')?.click()
      await menus[2]?.updateComplete
      menus[2]?.shadowRoot?.querySelector<HTMLInputElement>('.option input')?.click()
      await element.updateComplete
      const filterToggleCommand = (window as any).queryHistoryCommands.at(-1)
      const table = root.querySelector('lv-record-table') as any
      const rowText = table?.textContent ?? ''
      table.querySelector('.record-table-column-selector summary')?.click()
      Array.from(table.querySelectorAll('label'))
        .find((label) => label.textContent?.includes('Runtime'))
        ?.querySelector('input')
        ?.click()
      await table.updateComplete
      const hiddenRuntimeText = table.textContent ?? ''
      const visibleHeaderLabels = (recordTable: Element) => Array.from(recordTable.querySelectorAll('thead th')).map((header: Element) => header.querySelector('.record-table-sort span:first-child')?.textContent?.trim() ?? '')
      const hiddenRuntimeHeaders = visibleHeaderLabels(table)
      table.querySelector<HTMLButtonElement>('.record-query-expand')?.click()
      await table.updateComplete
      const expandedCodeBlock = table.querySelector('.record-query-expanded-cell lv-code-block') as (HTMLElement & { updateComplete: Promise<boolean> }) | null
      await expandedCodeBlock?.updateComplete
      const expandedQueryText = expandedCodeBlock?.shadowRoot?.querySelector('code')?.textContent
        ?? table.querySelector('.record-query-expanded-cell')?.textContent
        ?? ''
      const drawerAfterExpand = root.querySelector('lv-drawer')?.textContent ?? ''
      table.querySelector<HTMLButtonElement>('.record-query-expand')?.click()
      await table.updateComplete
      table.querySelector<HTMLElement>('tbody tr.record-row')?.click()
      await element.updateComplete
      const detailCommand = (window as any).queryHistoryCommands.at(-1)
      mergePatch({ adminQueryDetail: fixture.queryDetail })
      await element.updateComplete
      const drawer = root.querySelector('lv-drawer') as any
      const drawerPanel = drawer?.shadowRoot?.querySelector('.drawer') as HTMLElement | null
      const drawerText = drawer?.textContent ?? ''
      const drawerCodeBlock = drawer?.querySelector('lv-code-block') as (HTMLElement & { updateComplete: Promise<boolean> }) | null
      await drawerCodeBlock?.updateComplete
      const drawerCode = drawerCodeBlock?.shadowRoot?.querySelector('code')?.textContent ?? drawerCodeBlock?.querySelector('code')?.textContent ?? ''
      const drawerAnimationName = drawerPanel ? getComputedStyle(drawerPanel).animationName : ''
      const status = drawer?.querySelector('.query-detail-status') as HTMLElement | null
      const statusIcon = status?.querySelector('svg') as SVGElement | null
      const statusText = status?.querySelector('span') as HTMLElement | null
      const statusColor = status ? getComputedStyle(status).color : ''
      const statusTextColor = statusText ? getComputedStyle(statusText).color : ''
      const statusIconColor = statusIcon ? getComputedStyle(statusIcon).color : ''
      const hasSubtitle = Boolean(drawer?.querySelector('.query-detail-subtitle'))
      const usesSharedDrawer = drawer?.tagName === 'LV-DRAWER'
      const drawerIsModal = drawer?.modal
      const drawerModal = drawerPanel?.getAttribute('aria-modal') ?? null
      const drawerClose = drawer?.shadowRoot?.querySelector<HTMLButtonElement>('.close')
      drawerClose?.focus()
      const tabEvent = new KeyboardEvent('keydown', { key: 'Tab', bubbles: true, cancelable: true, composed: true })
      drawerClose?.dispatchEvent(tabEvent)
      const nonModalAllowsTab = !tabEvent.defaultPrevented
      drawerClose?.click()
      await element.updateComplete
      const closeCommand = (window as any).queryHistoryCommands.at(-1)
      mergePatch({ adminQueryDetail: { eventId: '', loading: false, error: '' } })
      await element.updateComplete
      const hasDrawerAfterClose = Boolean(root.querySelector('lv-drawer'))
      table.querySelector<HTMLElement>('tbody tr.record-row')?.click()
      await element.updateComplete
      mergePatch({ adminQueryDetail: fixture.queryDetail })
      await element.updateComplete
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      await element.updateComplete
      const escapeCommand = (window as any).queryHistoryCommands.at(-1)
      mergePatch({ adminQueryDetail: { eventId: '', loading: false, error: '' } })
      await element.updateComplete
      const hasDrawerAfterEscape = Boolean(root.querySelector('lv-drawer'))
      Array.from(table.querySelectorAll('label'))
        .find((label) => label.textContent?.includes('Operation'))
        ?.querySelector('input')
        ?.click()
      await table.updateComplete
      const operationHeaders = visibleHeaderLabels(table)
      const operationText = table.textContent ?? ''
      const hasDetailAction = Boolean(table.querySelector('.record-icon-action[aria-label="Details"]'))
      const recreated = document.createElement('lv-admin-page') as any
      document.body.replaceChildren(recreated)
      await recreated.updateComplete
      const recreatedTable = recreated.shadowRoot.querySelector('lv-record-table') as any
      const refreshedHeaders = visibleHeaderLabels(recreatedTable)
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        hasFilters: root.querySelectorAll('lv-filter-menu').length === 5,
        firstMenuText: menus[0]?.shadowRoot?.textContent ?? '',
        hasMetrics: Boolean(root.querySelector('.metrics')),
        hasColumnSelector: Boolean(table.querySelector('.record-table-column-selector')),
        queryStatusLabel: table.querySelector('.record-query-status')?.getAttribute('aria-label'),
        hasStatusHeader: visibleHeaderLabels(table).includes('Status'),
        sourceBadgeCount: table.querySelectorAll('.record-badge').length,
        rowHeight: Math.round(table.querySelector('tbody tr:first-child')?.getBoundingClientRect().height ?? 0),
        rowText,
        commandAfterSearch,
        filterSearchCommand,
        filterToggleCommand,
        hiddenRuntimeText,
        hiddenRuntimeHeaders,
        expandedQueryText,
        drawerAfterExpand,
        drawerText,
        detailCommand,
        closeCommand,
        escapeCommand,
        drawerHasCodeBlock: Boolean(drawerCodeBlock),
        drawerCode,
        drawerAnimationName,
        usesSharedDrawer,
        drawerIsModal,
        drawerModal,
        nonModalAllowsTab,
        statusColor,
        statusTextColor,
        statusIconColor,
        hasSubtitle,
        hasDrawerAfterClose,
        hasDrawerAfterEscape,
        operationHeaders,
        operationText,
        hasDetailAction,
        refreshedHeaders,
      }
    }, queryAuditFixturePage())

    expect(state.title).toBe('Query History')
    expect(state.hasFilters).toBe(true)
    expect(state.firstMenuText).toMatch(/Workspace/)
    expect(state.hasMetrics).toBe(false)
    expect(state.rowText).toMatch(/Query/)
    expect(state.rowText).not.toMatch(/Status/)
    expect(state.rowText).toMatch(/Started/)
    expect(state.rowText).toMatch(/Source type/)
    expect(state.rowText).toMatch(/Runtime/)
    expect(state.rowText).toMatch(/User/)
    expect(state.rowText).toMatch(/analyst/)
    expect(state.rowText).toMatch(/select status from orders/)
    expect(state.rowText).toMatch(/orders/)
    expect(state.rowText).toMatch(/customers/)
    expect(state.rowText).not.toMatch(/stale_page_event/)
    expect(state.commandAfterSearch).toMatchObject({ action: 'reset', limit: 50, filters: { search: 'select status' } })
    expect(state.filterSearchCommand).toMatchObject({ action: 'filter_search', filterMenu: { menuId: 'workspace', action: 'search', search: 'oper' } })
    expect(state.filterToggleCommand).toMatchObject({ action: 'filter_toggle', filterMenu: { menuId: 'surface', action: 'toggle', value: 'api' } })
    expect(state.hasColumnSelector).toBe(true)
    expect(state.hasStatusHeader).toBe(false)
    expect(state.sourceBadgeCount).toBe(0)
    expect(state.queryStatusLabel).toBe('success')
    expect(state.rowHeight).toBeLessThanOrEqual(44)
    expect(state.hiddenRuntimeHeaders).not.toContain('Runtime')
    expect(state.hiddenRuntimeHeaders).not.toContain('Status')
    expect(state.hiddenRuntimeHeaders[0]).toBe('Query')
    expect(state.hiddenRuntimeText).toMatch(/select status from orders/)
    expect(state.expandedQueryText).toMatch(/SELECT\s+status\s+FROM\s+orders/i)
    expect(state.drawerAfterExpand).toBe('')
    expect(state.detailCommand).toMatchObject({ action: 'select_detail', eventId: 'queryevent_1', limit: 50 })
    expect(state.drawerText).toMatch(/Finished|Success|success/i)
    expect(state.drawerText).toMatch(/analyst/)
    expect(state.drawerText).toMatch(/api/)
    expect(state.drawerText).toMatch(/sales/)
    expect(state.hasSubtitle).toBe(false)
    expect(state.statusTextColor).toBe(state.statusColor)
    expect(state.statusIconColor).not.toBe(state.statusColor)
    expect(state.drawerText).toMatch(/queryevent_1/)
    expect(state.drawerText).toMatch(/req_1/)
    expect(state.drawerText).toMatch(/corr_1/)
    expect(state.drawerHasCodeBlock).toBe(true)
    expect(state.drawerCode).toContain('SELECT')
    expect(state.drawerCode).toMatch(/\nFROM\n\s+orders/)
    expect(state.drawerText).toMatch(/12 ms/)
    expect(state.drawerText).toMatch(/semantic_aggregate/)
    expect(state.drawerText).toMatch(/semantic_dataset:sales:orders/)
    expect(state.drawerText).toMatch(/Rows returned/)
    expect(state.drawerAnimationName).toContain('drawer-slide-in')
    expect(state.usesSharedDrawer).toBe(true)
    expect(state.drawerIsModal).toBe(false)
    expect(state.drawerModal).toBeNull()
    expect(state.nonModalAllowsTab).toBe(true)
    expect(state.closeCommand).toMatchObject({ action: 'close_detail' })
    expect(state.escapeCommand).toMatchObject({ action: 'close_detail' })
    expect(state.hasDrawerAfterClose).toBe(false)
    expect(state.hasDrawerAfterEscape).toBe(false)
    expect(state.operationHeaders).toContain('Operation')
    expect(state.operationText).toMatch(/api_query/)
    expect(state.hasDetailAction).toBe(false)
    expect(state.refreshedHeaders).toContain('Runtime')
    expect(state.refreshedHeaders).not.toContain('Operation')
    expect(state.refreshedHeaders).not.toContain('Status')
  } finally {
    await page.close()
  }
})

test('query audit emits load more commands from backend-driven history state', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
      const state = await page.evaluate(async (fixture) => {
        const element = document.createElement('lv-admin-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: fixture, adminQueryHistory: fixture.queryHistory, adminQueryDetail: { eventId: '', loading: false, error: '' } })
      ;(window as any).queryHistoryCommands = []
      element.addEventListener('lv-query-history-command', (event: CustomEvent) => {
        ;(window as any).queryHistoryCommands.push(event.detail)
      })
      document.body.replaceChildren(element)
      await element.updateComplete
      const root = element.shadowRoot
      const footerText = root.querySelector('.query-history-footer')?.textContent ?? ''
      root.querySelector<HTMLButtonElement>('.query-history-load-more')?.click()
      await element.updateComplete
      const command = (window as any).queryHistoryCommands.at(-1)
      mergePatch({ adminQueryHistory: {
        ...fixture.queryHistory,
        table: {
          ...fixture.queryHistory.table,
          rows: [fixture.queryHistory.table.rows[1]],
        },
        filterMenus: fixture.queryHistory.filterMenus.map((menu: any) => menu.id === 'workspace' ? {
          ...menu,
          summaryLabel: 'operations',
          selected: ['operations'],
          options: menu.options.map((option: any) => ({ ...option, selected: option.value === 'operations' })),
        } : menu),
        filters: { workspaces: ['operations'] },
        nextCursor: '',
        hasMore: false,
        loadedCountLabel: '1 query loaded',
      } })
      await element.updateComplete
      const updatedText = root.textContent ?? ''
      const workspaceMenu = root.querySelector('lv-filter-menu') as HTMLElement | null
      return {
        footerText,
        command,
        updatedText,
        hasLoadMoreAfterPatch: Boolean(root.querySelector('.query-history-load-more')),
        workspaceFilterText: workspaceMenu?.shadowRoot?.textContent ?? '',
      }
    }, queryAuditFixturePage())

    expect(state.footerText).toMatch(/2 queries loaded/)
    expect(state.command).toMatchObject({ action: 'load_more', pageToken: 'cursor_next', limit: 50 })
    expect(state.updatedText).toMatch(/customers/)
    expect(state.updatedText).not.toMatch(/orders/)
    expect(state.hasLoadMoreAfterPatch).toBe(false)
    expect(state.workspaceFilterText).toMatch(/operations/)
  } finally {
    await page.close()
  }
})

test('query audit detail drawer behaves as a mobile overlay', async () => {
  const page = await browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
      const state = await page.evaluate(async (fixture) => {
        const element = document.createElement('lv-admin-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: fixture, adminQueryHistory: fixture.queryHistory, adminQueryDetail: { eventId: '', loading: false, error: '' } })
      document.body.replaceChildren(element)
      await element.updateComplete
      const root = element.shadowRoot
      const table = root.querySelector('lv-record-table') as any
      table.querySelector<HTMLElement>('tbody tr.record-row')?.click()
      await element.updateComplete
      mergePatch({ adminQueryDetail: fixture.queryDetail })
      await element.updateComplete
      const drawer = root.querySelector('lv-drawer') as any
      const overlay = drawer.shadowRoot.querySelector('.overlay') as HTMLElement
      const drawerPanel = drawer.shadowRoot.querySelector('.drawer') as HTMLElement
      const overlayRect = overlay.getBoundingClientRect()
      const drawerRect = drawerPanel.getBoundingClientRect()
      const tableRect = table.getBoundingClientRect()
      return {
        drawerText: drawer.textContent ?? '',
        drawerPosition: getComputedStyle(overlay).position,
        drawerWidth: Math.round(drawerRect.width),
        viewportWidth: window.innerWidth,
        drawerCoversTableHorizontally: overlayRect.left <= Math.max(0, tableRect.left) && overlayRect.right >= Math.min(window.innerWidth, tableRect.right),
        drawerModal: drawerPanel.getAttribute('aria-modal'),
      }
    }, queryAuditFixturePage())

    expect(state.drawerText).toMatch(/queryevent_1/)
    expect(state.drawerPosition).toBe('fixed')
    expect(state.drawerWidth).toBe(state.viewportWidth)
    expect(state.drawerCoversTableHorizontally).toBe(true)
    expect(state.drawerModal).toBeNull()
  } finally {
    await page.close()
  }
})

test('query audit drawer does not block selecting another row', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
      const state = await page.evaluate(async (fixture) => {
        const element = document.createElement('lv-admin-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: fixture, adminQueryHistory: fixture.queryHistory, adminQueryDetail: { eventId: '', loading: false, error: '' } })
      document.body.replaceChildren(element)
      await element.updateComplete
      const root = element.shadowRoot
      const table = root.querySelector('lv-record-table') as any
      const rows = Array.from(table.querySelectorAll<HTMLElement>('tbody tr.record-row'))
      rows[0]?.click()
      await element.updateComplete
      mergePatch({ adminQueryDetail: fixture.queryDetail })
      await element.updateComplete
      const firstDrawer = root.querySelector('lv-drawer') as any
      const firstDrawerText = firstDrawer?.textContent ?? ''
      const firstOverlay = firstDrawer?.shadowRoot?.querySelector('.overlay') as HTMLElement | null
      const overlayPointerEvents = firstOverlay ? getComputedStyle(firstOverlay).pointerEvents : ''
      const overlayBackground = firstOverlay ? getComputedStyle(firstOverlay).backgroundColor : ''
      rows[1]?.click()
      await element.updateComplete
      mergePatch({ adminQueryDetail: {
        ...fixture.queryDetail,
        eventId: 'queryevent_2',
        status: 'error',
        statusLabel: 'Error',
        workspaceId: 'operations',
        principalId: 'agent',
        surface: 'agent',
        operation: 'agent_query',
        queryKind: 'semantic_rows',
        modelId: 'operations',
        target: 'customers',
        objectType: 'agent_tool',
        objectId: 'query_semantic_dataset',
        requestId: 'call_1',
        correlationId: '',
        durationMs: 4,
        rowsReturned: 0,
        queryError: 'invalid field',
        sql: '',
        planText: '',
        queryJson: '{"workspaceId":"operations","target":"customers"}',
        createdAt: '2026-07-02T10:01:00Z',
      } })
      await element.updateComplete
      const secondDrawerText = root.querySelector('lv-drawer')?.textContent ?? ''
      return {
        firstDrawerText,
        secondDrawerText,
        overlayPointerEvents,
        overlayBackground,
      }
    }, queryAuditFixturePage())

    expect(state.overlayPointerEvents).toBe('none')
    expect(state.overlayBackground).toBe('rgba(0, 0, 0, 0)')
    expect(state.firstDrawerText).toMatch(/queryevent_1/)
    expect(state.firstDrawerText).toMatch(/analyst/)
    expect(state.secondDrawerText).toMatch(/queryevent_2/)
    expect(state.secondDrawerText).toMatch(/agent/)
    expect(state.secondDrawerText).toMatch(/invalid field/)
  } finally {
    await page.close()
  }
})

test('admin storage route renders storage explorer from typed signal data', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-storage-explorer'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-admin-page') as any
      const table = {
        key: 'ducklake-catalog\u0000model\u0000orders',
        databaseId: 'ducklake-catalog',
        databaseName: 'DuckLake catalog',
        databasePath: '/tmp/leapview/leapview.db',
        modelId: 'ducklake',
        modelName: 'DuckLake',
        schema: 'model',
        name: 'orders',
        type: 'table',
        tableId: 42,
        tableUuid: 'table-uuid',
        duckLakePath: 'model/orders/',
        beginSnapshot: 7,
        endSnapshot: 0,
        rowCount: 32000204,
        rowCountLabel: '32,000,204',
        columnCount: 1,
        fileCount: 1,
        sizeBytes: 12288,
        sizeLabel: '12 KiB',
        columns: [{ id: 91, name: 'order_id', type: 'VARCHAR', ordinal: 1, nullable: 'No', default: '', initialDefault: '', defaultValueType: 'literal', defaultValueDialect: 'duckdb', beginSnapshot: 7, containsNull: 'No', containsNan: '-', minValue: 'o_001', maxValue: 'o_999', extraStats: '' }],
        files: [{ id: 9, path: 'model/orders/file.parquet', format: 'parquet', recordCount: 32000204, recordCountLabel: '32,000,204', sizeBytes: 12288, sizeLabel: '12 KiB', beginSnapshot: 7, endSnapshot: 0 }],
        history: [{ snapshotId: 7, time: '2026-07-03T10:00:00Z', schemaVersion: 1, source: 'table,data_file', changes: 'tables_inserted_into', author: 'tester', message: 'materialize orders', extraInfo: '{}' }],
        servingStates: [{ workspaceId: 'sales', environment: 'dev', servingStateId: 'state_1', status: 'active', snapshotId: 7, digest: 'digest', active: true, activatedAt: 'now' }],
      }
      const storage = {
        summary: {
          catalogPath: '/tmp/leapview/leapview.db',
          dataPath: '/tmp/leapview/data',
          catalogSizeLabel: '32 KiB',
          dataSizeLabel: '12 KiB',
          totalSizeLabel: '44 KiB',
          totalDataSizeLabel: '12 KiB',
          databaseCount: 1,
          tableCount: 1,
          snapshotCount: 1,
          dataFileCount: 1,
        },
        status: '',
        warnings: ['Storage warning'],
        selectedKey: 'ducklake-catalog\u0000model\u0000orders',
        tables: [table],
        snapshots: [{ id: 7, time: '2026-07-03T10:00:00Z', schemaVersion: 1, author: 'tester', message: 'materialize', changes: 'tables_inserted_into', extraInfo: '{}', protected: true, servingStateCount: 1 }],
        servingStates: [{ workspaceId: 'sales', environment: 'dev', servingStateId: 'state_1', status: 'active', snapshotId: 7, digest: 'digest', active: true, activatedAt: 'now' }],
        selectedTable: table,
      }
      const pageSignal = {
        kind: 'admin',
        title: 'Storage',
        active: 'storage',
        sidebar: {
          label: 'Admin',
          railLabel: 'Admin',
          ariaLabel: 'Admin navigation',
          storageKey: 'leapview-admin-sidebar-collapsed',
          activeId: 'storage',
          collapsible: false,
          numbered: false,
          items: [{ id: 'storage', title: 'Storage', href: '/admin/storage', active: true }],
        },
        headerTitle: 'Storage',
        headerDetail: 'Read-only DuckLake catalog and table metadata.',
        metrics: [{ label: 'Tables', value: '1' }],
        storage,
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: pageSignal, adminStorage: storage })
      document.body.append(element)
      await element.updateComplete
      const explorer = element.shadowRoot.querySelector('lv-storage-explorer') as any
      await explorer.updateComplete
      const schemaText = explorer.shadowRoot.textContent
      const filesTab = Array.from(explorer.shadowRoot.querySelectorAll<HTMLButtonElement>('.storage-tab')).find((button) => button.textContent?.includes('Data files'))
      filesTab?.click()
      await explorer.updateComplete
      const filesText = explorer.shadowRoot.textContent
      return {
        hasPageTitle: Boolean(element.shadowRoot.querySelector('h1')),
        explorerTitle: explorer.shadowRoot.querySelector('h2')?.textContent?.trim(),
        hasGenericMetrics: Boolean(element.shadowRoot.querySelector('.metrics')),
        warning: explorer.shadowRoot.textContent?.includes('Storage warning'),
        hasExplorer: Boolean(explorer),
        explorerHeight: Math.round(explorer.shadowRoot.querySelector('.storage-explorer')?.getBoundingClientRect().height ?? 0),
        searchInBrowserMenu: Boolean(explorer.shadowRoot.querySelector('.storage-browser-menu .storage-search input')),
        searchInPageHeader: Boolean(explorer.shadowRoot.querySelector('.storage-explorer-header .storage-search input')),
        hasGlobalSummary: Boolean(explorer.shadowRoot.querySelector('.storage-summary')),
        detailBadges: explorer.shadowRoot.querySelectorAll('.storage-detail-header > span, .storage-columns-header > span').length,
        databaseTreeBadges: Array.from(explorer.shadowRoot.querySelectorAll('.storage-db > summary em')).map((badge) => badge.textContent?.trim()),
        schemaTreeBadges: Array.from(explorer.shadowRoot.querySelectorAll('.storage-schema > summary em')).map((badge) => badge.textContent?.trim()),
        tableListSizes: Array.from(explorer.shadowRoot.querySelectorAll('.storage-table-size')).map((size) => size.textContent?.trim()),
        searchBorder: getComputedStyle(explorer.shadowRoot.querySelector('.storage-search input')!).border,
        metricsOverflow: getComputedStyle(explorer.shadowRoot.querySelector('.storage-metrics')!).overflowX,
        metricsWrap: getComputedStyle(explorer.shadowRoot.querySelector('.storage-metrics')!).flexWrap,
        explorerText: explorer.shadowRoot.textContent,
        schemaText,
        filesText,
      }
    })

    expect(state.hasPageTitle).toBe(false)
    expect(state.explorerTitle).toBe('Storage')
    expect(state.hasGenericMetrics).toBe(false)
    expect(state.warning).toBe(true)
    expect(state.hasExplorer).toBe(true)
    expect(state.explorerHeight).toBeGreaterThan(500)
    expect(state.searchInBrowserMenu).toBe(true)
    expect(state.searchInPageHeader).toBe(false)
    expect(state.hasGlobalSummary).toBe(true)
    expect(state.detailBadges).toBe(0)
    expect(state.databaseTreeBadges).toEqual([])
    expect(state.schemaTreeBadges).toEqual([])
    expect(state.tableListSizes).toEqual(['12 KiB'])
    expect(state.searchBorder).toContain('0px')
    expect(state.metricsOverflow).toBe('hidden')
    expect(state.metricsWrap).toBe('wrap')
    expect(state.explorerText ?? '').toMatch(/orders/)
    expect(state.explorerText ?? '').toMatch(/DuckLake catalog/)
    expect(state.explorerText ?? '').toMatch(/\/tmp\/leapview\/leapview\.db/)
    expect(state.explorerText ?? '').toMatch(/Table UUID/)
    expect(state.explorerText ?? '').toMatch(/table-uuid/)
    expect(state.explorerText ?? '').toMatch(/DuckLake path/)
    expect(state.explorerText ?? '').toMatch(/model\/orders\//)
    expect(state.schemaText ?? '').toMatch(/Column ID/)
    expect(state.schemaText ?? '').toMatch(/literal/)
    expect(state.schemaText ?? '').toMatch(/duckdb/)
    expect(state.schemaText ?? '').toMatch(/Nulls/)
    expect(state.schemaText ?? '').toMatch(/o_001/)
    expect(state.schemaText ?? '').toMatch(/o_999/)
    expect(state.filesText ?? '').toMatch(/model\/orders\/file\.parquet/)
    expect(state.explorerText ?? '').toMatch(/32,000,204/)
    expect(state.explorerText ?? '').not.toMatch(/32000204/)
    expect(state.explorerText ?? '').not.toMatch(/dep_1/)
    expect(state.explorerText ?? '').toMatch(/12 KiB/)
  } finally {
    await page.close()
  }
})

test('admin agent route renders prompt editor, tools catalog, and emits save command', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-agent-prompt-editor') && customElements.get('lv-agent-tools'))

    const state = await page.evaluate(async () => {
      const waitFor = async (predicate: () => boolean, timeoutMs = 5000): Promise<void> => {
        const started = performance.now()
        while (!predicate()) {
          if (performance.now() - started > timeoutMs) throw new Error('timed out waiting for condition')
          await new Promise((resolve) => setTimeout(resolve, 20))
        }
      }
      const element = document.createElement('lv-admin-page') as any
      const pageSignal = {
        kind: 'admin',
        title: 'Agent',
        active: 'agent',
        sidebar: {
          label: 'Admin',
          railLabel: 'Admin',
          ariaLabel: 'Admin navigation',
          storageKey: 'leapview-admin-sidebar-collapsed',
          activeId: 'agent',
          collapsible: false,
          numbered: false,
          items: [{ id: 'agent', title: 'Agent', href: '/admin/agent', active: true }],
        },
        headerTitle: 'Agent',
        headerDetail: 'Platform agent prompt and read-only tool inventory.',
        metrics: [{ label: 'Tools', value: '1' }],
        agent: {
          enabled: true,
          model: 'fake-model',
          systemPrompt: 'Initial prompt',
          canWrite: true,
          updatePath: '/admin/agent/config',
          tools: [{
            name: 'query_visual',
            description: 'Query visual data.',
            inputSchema: {
              type: 'object',
              required: ['dashboardId'],
              properties: {
                dashboardId: { type: 'string', description: 'Dashboard identifier.' },
                mode: { enum: ['summary', 'detail'], description: 'Result detail level.' },
              },
              additionalProperties: false,
            },
          }],
        },
        sections: [{
          title: 'Tools',
          table: {
            columns: [{ id: 'name', header: 'Name', kind: 'code' }],
            rows: [{ name: 'query_visual' }],
            empty: 'No tools configured.',
          },
        }],
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: pageSignal, adminAgentCommand: { systemPrompt: 'Signal prompt' } })
      document.body.append(element)
      await element.updateComplete
      let command: unknown = null
      element.addEventListener('lv-agent-system-prompt-save', (event: CustomEvent) => { command = event.detail })
      const root = element.shadowRoot
      const editor = root.querySelector('lv-agent-prompt-editor') as any
      const toolsCatalog = root.querySelector('lv-agent-tools') as any
      await editor.updateComplete
      await toolsCatalog.updateComplete
      const editorRoot = editor.shadowRoot
      await customElements.whenDefined('lv-code-editor')
      await waitFor(() => Boolean(editorRoot.querySelector('lv-code-editor')))
      const controlRow = editorRoot.querySelector('.prompt-control-row')!
      const actions = editorRoot.querySelector('.prompt-actions')!
      const body = editorRoot.querySelector('.prompt-body')!
      const markdownView = editorRoot.querySelector('lv-markdown-view') as any
      const preSwitchState = {
        hasCodeEditor: Boolean(editorRoot.querySelector('lv-code-editor')),
        hasMarkdownView: Boolean(markdownView),
        markdownViewCompact: markdownView?.compact,
        markdownValue: markdownView?.value,
        hasLoading: Boolean(editorRoot.querySelector('.editor-loading')),
        hasTextarea: Boolean(editorRoot.querySelector('textarea')),
        hasSaveButton: Boolean(editorRoot.querySelector('.save-button')),
        status: editorRoot.querySelector('.prompt-status')?.textContent?.trim() ?? '',
      }
      const editButton = editorRoot.querySelector<HTMLButtonElement>('.mode-toggle button[aria-label="Edit"]')!
      editButton.click()
      await editor.updateComplete
      const immediateSwitchState = {
        hasCodeEditor: Boolean(editorRoot.querySelector('lv-code-editor')),
        hasLoading: Boolean(editorRoot.querySelector('.editor-loading')),
        hasTextarea: Boolean(editorRoot.querySelector('textarea')),
      }
      await editor.updateComplete
      const codeEditor = editorRoot.querySelector('lv-code-editor') as any
      await codeEditor.updateComplete
      await waitFor(() => Boolean(codeEditor.shadowRoot.querySelector('.view-line')))
      const editorFontSize = getComputedStyle(codeEditor.shadowRoot.querySelector('.view-line')!).fontSize
      const seededEditorValue = codeEditor.value
      codeEditor.value = 'Updated prompt'
      codeEditor.dispatchEvent(new CustomEvent('lv-code-editor-change', {
        bubbles: true,
        composed: true,
        detail: { value: 'Updated prompt' },
      }))
      await codeEditor.updateComplete
      await editor.updateComplete
      const dirtyState = {
        hasSaveButton: Boolean(editorRoot.querySelector('.save-button')),
        saveText: editorRoot.querySelector('.save-button')?.textContent?.trim(),
        status: editorRoot.querySelector('.prompt-status')?.textContent?.trim(),
      }
      codeEditor.value = 'Signal prompt'
      codeEditor.dispatchEvent(new CustomEvent('lv-code-editor-change', {
        bubbles: true,
        composed: true,
        detail: { value: 'Signal prompt' },
      }))
      await codeEditor.updateComplete
      await editor.updateComplete
      const revertedState = {
        hasSaveButton: Boolean(editorRoot.querySelector('.save-button')),
        status: editorRoot.querySelector('.prompt-status')?.textContent?.trim() ?? '',
      }
      codeEditor.value = 'Updated prompt'
      codeEditor.dispatchEvent(new CustomEvent('lv-code-editor-change', {
        bubbles: true,
        composed: true,
        detail: { value: 'Updated prompt' },
      }))
      await codeEditor.updateComplete
      await editor.updateComplete
      editorRoot.querySelector<HTMLButtonElement>('.save-button')?.click()
      await editor.updateComplete
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        hasEditor: Boolean(editor),
        hasToolsCatalog: Boolean(toolsCatalog),
        hasGenericToolsRecordTable: Boolean(root.querySelector('section[aria-label="Tools"] lv-record-table')),
        toolsCatalogText: toolsCatalog.shadowRoot.textContent,
        hasCodeEditor: Boolean(codeEditor),
        preSwitchState,
        immediateSwitchState,
        actionsInControlRow: actions.parentElement === controlRow,
        actionsBeforeBody: Boolean(actions.compareDocumentPosition(body) & Node.DOCUMENT_POSITION_FOLLOWING),
        actionsAfterBody: Boolean(actions.compareDocumentPosition(body) & Node.DOCUMENT_POSITION_PRECEDING),
        dirtyState,
        revertedState,
        editorFontSize,
        seededEditorValue,
        editorValue: codeEditor.value,
        hasSaveAfterSave: Boolean(editorRoot.querySelector('.save-button')),
        activeMode: editorRoot.querySelector('.mode-toggle button[aria-pressed="true"]')?.getAttribute('aria-label'),
        status: editorRoot.querySelector('.prompt-status')?.textContent?.trim(),
        command,
      }
    })

    expect(state.title).toBe('Agent')
    expect(state.hasEditor).toBe(true)
    expect(state.hasToolsCatalog).toBe(true)
    expect(state.hasGenericToolsRecordTable).toBe(false)
    expect(state.toolsCatalogText ?? '').toMatch(/query_visual/)
    expect(state.toolsCatalogText ?? '').toMatch(/dashboardId/)
    expect(state.hasCodeEditor).toBe(true)
    expect(state.preSwitchState).toEqual({
      hasCodeEditor: true,
      hasMarkdownView: true,
      markdownViewCompact: true,
      markdownValue: 'Signal prompt',
      hasLoading: false,
      hasTextarea: false,
      hasSaveButton: false,
      status: '',
    })
    expect(state.immediateSwitchState).toEqual({ hasCodeEditor: true, hasLoading: false, hasTextarea: false })
    expect(state.actionsInControlRow).toBe(true)
    expect(state.actionsBeforeBody).toBe(true)
    expect(state.actionsAfterBody).toBe(false)
    expect(state.editorFontSize).toBe('13px')
    expect(state.seededEditorValue).toBe('Signal prompt')
    expect(state.editorValue).toBe('Updated prompt')
    expect(state.dirtyState).toEqual({ hasSaveButton: true, saveText: 'Save', status: 'Unsaved changes' })
    expect(state.revertedState).toEqual({ hasSaveButton: false, status: '' })
    expect(state.hasSaveAfterSave).toBe(false)
    expect(state.activeMode).toBe('Edit')
    expect(state.status).toBe('Saved')
    expect(state.command).toEqual({ systemPrompt: 'Updated prompt' })
  } finally {
    await page.close()
  }
})

test('admin agent prompt editor disables saves for read-only users', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-agent-prompt-editor'))

    const state = await page.evaluate(async () => {
      const waitFor = async (predicate: () => boolean, timeoutMs = 5000): Promise<void> => {
        const started = performance.now()
        while (!predicate()) {
          if (performance.now() - started > timeoutMs) throw new Error('timed out waiting for condition')
          await new Promise((resolve) => setTimeout(resolve, 20))
        }
      }
      const element = document.createElement('lv-admin-page') as any
      const pageSignal = {
        kind: 'admin',
        title: 'Agent',
        active: 'agent',
        sidebar: {
          label: 'Admin',
          railLabel: 'Admin',
          ariaLabel: 'Admin navigation',
          storageKey: 'leapview-admin-sidebar-collapsed',
          activeId: 'agent',
          collapsible: false,
          numbered: false,
          items: [{ id: 'agent', title: 'Agent', href: '/admin/agent', active: true }],
        },
        headerTitle: 'Agent',
        headerDetail: 'Platform agent prompt and read-only tool inventory.',
        agent: {
          enabled: true,
          model: 'fake-model',
          systemPrompt: 'Initial prompt',
          canWrite: false,
          updatePath: '/admin/agent/config',
          tools: [],
        },
        sections: [],
      }
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: pageSignal, adminAgentCommand: { systemPrompt: '' } })
      document.body.append(element)
      await element.updateComplete
      let command: unknown = null
      element.addEventListener('lv-agent-system-prompt-save', (event: CustomEvent) => { command = event.detail })
      const editor = element.shadowRoot.querySelector('lv-agent-prompt-editor') as any
      await editor.updateComplete
      const editorRoot = editor.shadowRoot
      const editButton = editorRoot.querySelector<HTMLButtonElement>('.mode-toggle button[aria-label="Edit"]')!
      editButton.click()
      await customElements.whenDefined('lv-code-editor')
      await waitFor(() => Boolean(editorRoot.querySelector('lv-code-editor')))
      await editor.updateComplete
      const codeEditor = editorRoot.querySelector('lv-code-editor') as any
      await codeEditor.updateComplete
      const saveButton = editorRoot.querySelector<HTMLButtonElement>('.save-button')
      return {
        codeEditorDisabled: codeEditor.disabled,
        hasSaveButton: Boolean(saveButton),
        status: editorRoot.querySelector('.prompt-status')?.textContent?.trim(),
        command,
      }
    })

    expect(state.codeEditorDisabled).toBe(true)
    expect(state.hasSaveButton).toBe(false)
    expect(state.status).toBe('Read-only')
    expect(state.command).toBeNull()
  } finally {
    await page.close()
  }
})

test('admin agent tools catalog renders payload fields, JSON, empty, unsupported, and search', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-agent-tools'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-agent-tools') as any
      element.tools = [{
        name: 'query_visual',
        description: 'Query visual data.',
        effect: 'read',
        defaults: { mode: 'summary' },
        inputSchema: {
          type: 'object',
          required: ['dashboardId', 'mode'],
          properties: {
            dashboardId: { type: 'string', description: 'Dashboard identifier.' },
            filters: {
              type: 'object',
              properties: {
                dateRange: {
                  type: 'object',
                  required: ['start'],
                  properties: {
                    start: { type: 'string', description: 'Start date.' },
                    end: { type: 'string', description: 'End date.' },
                  },
                },
              },
            },
            metrics: { type: 'array', items: { type: 'string' }, description: 'Metric IDs.' },
            mode: { enum: ['summary', 'detail'], description: 'Result detail level.' },
            dimensions: { type: 'array', items: { $ref: '#/$defs/fieldRef' }, description: 'Dimension fields.' },
            series: { $ref: '#/$defs/fieldRef', description: 'Series field.' },
            sort: { type: 'array', items: { $ref: '#/$defs/sort' } },
            options: { type: 'object', additionalProperties: true, description: 'Renderer options.' },
            rendererOptions: {
              type: 'object',
              additionalProperties: { type: 'object', additionalProperties: true },
              description: 'Renderer-specific options.',
            },
          },
          $defs: {
            fieldRef: {
              type: 'object',
              additionalProperties: false,
              required: ['field'],
              properties: {
                field: { type: 'string', minLength: 1, description: 'Semantic field ID.' },
                alias: { type: 'string', description: 'Display alias.' },
              },
            },
            sort: {
              type: 'object',
              additionalProperties: false,
              required: ['field'],
              properties: {
                field: { type: 'string', minLength: 1 },
                direction: { type: 'string', enum: ['asc', 'desc'] },
              },
            },
          },
          additionalProperties: false,
        },
        outputSchema: {
          type: 'object',
          properties: {
            rows: { type: 'array', items: { type: 'object' } },
          },
          additionalProperties: false,
        },
      }, {
        name: 'no_input',
        description: 'No payload required.',
        inputSchema: { type: 'object', additionalProperties: false },
      }, {
        name: 'unsupported_input',
        description: 'Composition schema.',
        inputSchema: { oneOf: [{ type: 'string' }, { type: 'number' }] },
      }]
      document.body.append(element)
      await element.updateComplete
      const root = element.shadowRoot
      const firstText = root.textContent ?? ''
      const catalogHeight = Math.round(root.querySelector('.catalog')!.getBoundingClientRect().height)
      const listOverflow = getComputedStyle(root.querySelector('.list')!).overflowY
      const detailBodyOverflow = getComputedStyle(root.querySelector('.detail-body')!).overflowY
      const toolButtons = Array.from(root.querySelectorAll('.tool-button')).map((button) => button.textContent?.trim())
      const listText = root.querySelector('.list')?.textContent ?? ''
      const firstRows = Array.from(root.querySelectorAll('.fields tbody tr')).map((row) => Array.from(row.querySelectorAll('td')).map((cell) => cell.textContent?.trim()))
      const detailMeta = Array.from(root.querySelectorAll('.detail-meta .required-count')).map((item) => item.textContent?.trim())

      const jsonButton = root.querySelector<HTMLButtonElement>('.tabs button:nth-child(2)')!
      jsonButton.click()
      await element.updateComplete
      const jsonText = root.querySelector('.json')?.textContent ?? ''

      const outputButton = root.querySelector<HTMLButtonElement>('.tabs button:nth-child(3)')!
      outputButton.click()
      await element.updateComplete
      const outputText = root.querySelector('.json')?.textContent ?? ''

      const noInputButton = Array.from(root.querySelectorAll<HTMLButtonElement>('.tool-button')).find((button) => button.textContent?.includes('no_input'))!
      noInputButton.click()
      await element.updateComplete
      const noInputText = root.textContent ?? ''

      const unsupportedButton = Array.from(root.querySelectorAll<HTMLButtonElement>('.tool-button')).find((button) => button.textContent?.includes('unsupported_input'))!
      unsupportedButton.click()
      await element.updateComplete
      const unsupportedText = root.textContent ?? ''

      const search = root.querySelector<HTMLInputElement>('input[type="search"]')!
      search.value = 'filters.dateRange.start'
      search.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await element.updateComplete
      const searchRows = Array.from(root.querySelectorAll('.tool-button')).map((button) => button.textContent?.trim())
      return {
        firstText,
        catalogHeight,
        listOverflow,
        detailBodyOverflow,
        toolButtons,
        listText,
        firstRows,
        detailMeta,
        jsonText,
        outputText,
        noInputText,
        unsupportedText,
        searchRows,
      }
    })

    expect(state.firstText).toMatch(/query_visual/)
    expect(state.firstText).toMatch(/dashboardId, filters\.dateRange\.start, filters\.dateRange\.end \+10/)
    expect(state.catalogHeight).toBeGreaterThan(440)
    expect(state.listOverflow).toBe('auto')
    expect(state.detailBodyOverflow).toBe('auto')
    expect(state.toolButtons).toEqual(['query_visual', 'no_input', 'unsupported_input'])
    expect(state.listText).not.toMatch(/Query visual data/)
    expect(state.detailMeta).toEqual(['read', '6 required', 'dashboardId, filters.dateRange.start, filters.dateRange.end +10', 'Defaults: mode=summary'])
    expect(state.firstRows).toContainEqual(['dashboardId', 'string', 'Yes', 'Dashboard identifier.'])
    expect(state.firstRows).toContainEqual(['filters.dateRange.start', 'string', 'Yes', 'Start date.'])
    expect(state.firstRows).toContainEqual(['filters.dateRange.end', 'string', 'No', 'End date.'])
    expect(state.firstRows).toContainEqual(['metrics', 'array<string>', 'No', 'Metric IDs.'])
    expect(state.firstRows).toContainEqual(['mode', 'enum: summary | detail', 'Yes', 'Result detail level.'])
    expect(state.firstRows).toContainEqual(['dimensions[].field', 'string', 'Yes', 'Semantic field ID.'])
    expect(state.firstRows).toContainEqual(['dimensions[].alias', 'string', 'No', 'Display alias.'])
    expect(state.firstRows).toContainEqual(['series.field', 'string', 'Yes', 'Semantic field ID.'])
    expect(state.firstRows).toContainEqual(['sort[].direction', 'enum: asc | desc', 'No', '-'])
    expect(state.firstRows).toContainEqual(['options', 'object<string, any>', 'No', 'Renderer options.'])
    expect(state.firstRows).toContainEqual(['rendererOptions', 'object<string, object>', 'No', 'Renderer-specific options.'])
    expect(state.jsonText).toMatch(/"dashboardId"/)
    expect(state.outputText).toMatch(/"rows"/)
    expect(state.noInputText).toMatch(/No input/)
    expect(state.unsupportedText).toMatch(/Schema is only available as JSON/)
    expect(state.searchRows).toHaveLength(1)
    expect(state.searchRows[0] ?? '').toMatch(/query_visual/)
  } finally {
    await page.close()
  }
})

test('agent prompt editor seeds edit mode from value attribute', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-agent-prompt-editor'))

    const state = await page.evaluate(async () => {
      const waitFor = async (predicate: () => boolean, timeoutMs = 5000): Promise<void> => {
        const started = performance.now()
        while (!predicate()) {
          if (performance.now() - started > timeoutMs) throw new Error('timed out waiting for condition')
          await new Promise((resolve) => setTimeout(resolve, 20))
        }
      }
      const element = document.createElement('lv-agent-prompt-editor') as any
      element.setAttribute('value', 'Attribute prompt')
      document.body.append(element)
      await element.updateComplete
      const root = element.shadowRoot
      const editButton = root.querySelector<HTMLButtonElement>('.mode-toggle button[aria-label="Edit"]')!
      editButton.click()
      await customElements.whenDefined('lv-code-editor')
      await waitFor(() => Boolean(root.querySelector('lv-code-editor')))
      await element.updateComplete
      const codeEditor = root.querySelector('lv-code-editor') as any
      await codeEditor.updateComplete
      return {
        activeMode: root.querySelector('.mode-toggle button[aria-pressed="true"]')?.getAttribute('aria-label'),
        codeEditorValue: codeEditor.value,
      }
    })

    expect(state.activeMode).toBe('Edit')
    expect(state.codeEditorValue).toBe('Attribute prompt')
  } finally {
    await page.close()
  }
})

test('agent prompt preview delegates to compact markdown view', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-agent-prompt-editor') && customElements.get('lv-markdown-view'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-agent-prompt-editor') as any
      element.value = [
        '# Hello darkness',
        '',
        'A paragraph with **strong**, _emphasis_, ~~strike~~, `inline code`, and https://example.com.',
        '',
        '## Section',
        '',
        '- One',
        '- Two',
        '  - Nested',
        '',
        '> Quoted guidance',
        '',
        '---',
        '',
        '| Name | Value |',
        '| --- | --- |',
        '| Tool | Enabled |',
        '',
        '```json',
        '{"enabled": true}',
        '```',
        '',
        '![Alt text](https://example.com/image.png)',
      ].join('\n')
      document.body.append(element)
      await element.updateComplete
      const root = element.shadowRoot
      const markdownView = root.querySelector('lv-markdown-view') as any
      await markdownView.updateComplete
      const h1 = markdownView.shadowRoot.querySelector('h1')!
      return {
        hasMarkdownView: Boolean(markdownView),
        compact: markdownView.compact,
        value: markdownView.value,
        emptyText: markdownView.emptyText,
        h1Text: h1.textContent,
      }
    })

    expect(state.hasMarkdownView).toBe(true)
    expect(state.compact).toBe(true)
    expect(state.value).toMatch(/^# Hello darkness/)
    expect(state.emptyText).toBe('No system prompt configured.')
    expect(state.h1Text).toBe('Hello darkness')
  } finally {
    await page.close()
  }
})

test('admin storage explorer keeps table, schema, and breadcrumb selection coherent', async () => {
  const page = await browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(baseURL)
    await page.waitForFunction(() => customElements.get('lv-storage-explorer'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-storage-explorer') as any
      const customers = {
        key: 'ducklake-catalog\u0000model\u0000customers',
        databaseId: 'ducklake-catalog',
        databaseName: 'DuckLake catalog',
        databasePath: '/tmp/leapview/leapview.db',
        modelId: 'ducklake',
        modelName: 'DuckLake',
        schema: 'model',
        name: 'customers',
        type: 'table',
        tableId: 41,
        tableUuid: 'customers-uuid',
        duckLakePath: 'model/customers/',
        beginSnapshot: 6,
        endSnapshot: 0,
        rowCount: 10,
        rowCountLabel: '10',
        columnCount: 1,
        fileCount: 1,
        sizeBytes: 12288,
        sizeLabel: '12 KiB',
        columns: [{ id: 81, name: 'customer_id', type: 'VARCHAR', ordinal: 1, nullable: 'No', default: '', initialDefault: '', defaultValueType: 'literal', defaultValueDialect: 'duckdb', beginSnapshot: 6, containsNull: 'No', containsNan: '-', minValue: 'c_001', maxValue: 'c_999', extraStats: '' }],
        files: [{ id: 1, path: 'model/customers/file.parquet', format: 'parquet', recordCount: 10, recordCountLabel: '10', sizeBytes: 12288, sizeLabel: '12 KiB', beginSnapshot: 6, endSnapshot: 0 }],
        history: [{ snapshotId: 6, time: '2026-07-03T10:00:00Z', schemaVersion: 1, source: 'table,data_file', changes: 'tables_inserted_into', author: 'tester', message: 'materialize customers', extraInfo: '{}' }],
        servingStates: [{ workspaceId: 'olist', environment: 'dev', servingStateId: 'state_1', status: 'active', snapshotId: 6, digest: 'digest', active: true, activatedAt: 'now' }],
      }
      const orders = {
        ...customers,
        key: 'ducklake-catalog\u0000model\u0000orders',
        name: 'orders',
        tableId: 42,
        tableUuid: 'orders-uuid',
        duckLakePath: 'model/orders/',
        rowCount: 20,
        rowCountLabel: '20',
        columns: [{ id: 82, name: 'order_id', type: 'VARCHAR', ordinal: 1, nullable: 'No', default: '', initialDefault: '', defaultValueType: 'literal', defaultValueDialect: 'duckdb', beginSnapshot: 6, containsNull: 'No', containsNan: '-', minValue: 'o_001', maxValue: 'o_999', extraStats: '' }],
        files: [{ id: 2, path: 'model/orders/file.parquet', format: 'parquet', recordCount: 20, recordCountLabel: '20', sizeBytes: 12288, sizeLabel: '12 KiB', beginSnapshot: 6, endSnapshot: 0 }],
        history: [{ snapshotId: 6, time: '2026-07-03T10:00:00Z', schemaVersion: 1, source: 'table,data_file', changes: 'tables_inserted_into', author: 'tester', message: 'materialize orders', extraInfo: '{}' }],
      }
      const storage = {
        summary: {
          catalogPath: '/tmp/leapview/leapview.db',
          dataPath: '/tmp/leapview/data',
          catalogSizeLabel: '32 KiB',
          dataSizeLabel: '24 KiB',
          totalSizeLabel: '56 KiB',
          totalDataSizeLabel: '24 KiB',
          databaseCount: 1,
          tableCount: 2,
          snapshotCount: 1,
          dataFileCount: 2,
        },
        status: '',
        warnings: [],
        selectedKey: customers.key,
        tables: [customers, orders],
        snapshots: [{ id: 6, time: '2026-07-03T10:00:00Z', schemaVersion: 1, author: 'tester', message: 'materialize', changes: 'tables_inserted_into', extraInfo: '{}', protected: true, servingStateCount: 1 }],
        servingStates: [{ workspaceId: 'olist', environment: 'dev', servingStateId: 'state_1', status: 'active', snapshotId: 6, digest: 'digest', active: true, activatedAt: 'now' }],
        selectedTable: customers,
      }
      element.storage = storage
      const commands: unknown[] = []
      element.addEventListener('lv-storage-table-select', (event: CustomEvent) => commands.push(event.detail))
      document.body.append(element)
      await element.updateComplete

      const root = element.shadowRoot
      const selectedNames = () => Array.from(root.querySelectorAll('.storage-table-button.is-selected')).map((button) => button.textContent?.trim())
      const tableSizes = () => Array.from(root.querySelectorAll('.storage-table-size')).map((size) => size.textContent?.trim())
      const detailText = () => root.querySelector('.storage-detail')?.textContent ?? ''
      const ordersButton = Array.from(root.querySelectorAll<HTMLButtonElement>('.storage-table-button')).find((button) => button.textContent?.includes('orders'))!
      const schemaSummary = root.querySelector<HTMLElement>('.storage-schema > summary')!

      ordersButton.click()
      await element.updateComplete
      const tabText = (tab: Element | null) => tab?.textContent?.replace(/\s+/g, ' ').trim()
      const defaultTabLabels = Array.from(root.querySelectorAll('.storage-tab')).map((tab) => tabText(tab))
      const activeTabBefore = tabText(root.querySelector('.storage-tab.is-active'))
      const schemaDetail = detailText()
      const filesTab = Array.from(root.querySelectorAll<HTMLButtonElement>('.storage-tab')).find((button) => button.textContent?.includes('Data files'))!
      filesTab.click()
      await element.updateComplete
      const filesDetail = detailText()
      const historyTab = Array.from(root.querySelectorAll<HTMLButtonElement>('.storage-tab')).find((button) => button.textContent?.includes('History'))!
      historyTab.click()
      await element.updateComplete
      const historyDetail = detailText()
      const afterOrders = {
        selectedNames: selectedNames(),
        tableSizes: tableSizes(),
        detail: detailText(),
        defaultTabLabels,
        activeTabBefore,
        schemaDetail,
        filesDetail,
        historyDetail,
        commands: [...commands],
      }

      schemaSummary.click()
      await element.updateComplete
      const afterSchema = {
        selectedNames: selectedNames(),
        detail: detailText(),
      }

      const schemaRowsBeforeBreadcrumb = root.querySelectorAll('lv-record-table tbody tr').length
      ordersButton.click()
      await element.updateComplete
      const schemaBreadcrumb = root.querySelector<HTMLButtonElement>('button[data-breadcrumb-kind="schema"]')!
      schemaBreadcrumb.click()
      await element.updateComplete
      const databaseBreadcrumb = root.querySelector<HTMLButtonElement>('button[data-breadcrumb-kind="database"]')!
      databaseBreadcrumb.click()
      await element.updateComplete
      const catalogTabs = Array.from(root.querySelectorAll('.storage-tab')).map((tab) => tabText(tab))
      const catalogActiveTab = tabText(root.querySelector('.storage-tab.is-active'))
      const catalogDefaultDetail = detailText()
      const catalogServingStatesTab = Array.from(root.querySelectorAll<HTMLButtonElement>('.storage-tab')).find((button) => button.textContent?.includes('Serving states'))!
      catalogServingStatesTab.click()
      await element.updateComplete
      const catalogServingStatesDetail = detailText()
      const catalogSnapshotsTab = Array.from(root.querySelectorAll<HTMLButtonElement>('.storage-tab')).find((button) => button.textContent?.includes('Snapshots'))!
      catalogSnapshotsTab.click()
      await element.updateComplete
      const catalogSnapshotsDetail = detailText()
      const afterBreadcrumb = {
        selectedNames: selectedNames(),
        detail: detailText(),
        schemaRows: root.querySelectorAll('lv-record-table tbody tr').length,
        schemaRowsBeforeBreadcrumb,
        catalogTabs,
        catalogActiveTab,
        catalogDefaultDetail,
        catalogServingStatesDetail,
        catalogSnapshotsDetail,
      }

      return { afterOrders, afterSchema, afterBreadcrumb }
    })

    expect(state.afterOrders.selectedNames).toHaveLength(1)
    expect(state.afterOrders.selectedNames[0]).toContain('orders')
    expect(state.afterOrders.tableSizes).toEqual(['12 KiB', '12 KiB'])
    expect(state.afterOrders.activeTabBefore).toContain('Schema')
    expect(state.afterOrders.defaultTabLabels).toEqual(['Schema 1', 'Data files 1', 'History 1'])
    expect(state.afterOrders.schemaDetail).toContain('order_id')
    expect(state.afterOrders.filesDetail).toContain('model/orders/file.parquet')
    expect(state.afterOrders.historyDetail).toContain('materialize orders')
    expect(state.afterOrders.historyDetail).toContain('tables_inserted_into')
    expect(state.afterOrders.commands).toEqual([{ databaseId: 'ducklake-catalog', schema: 'model', table: 'orders' }])

    expect(state.afterSchema.selectedNames).toHaveLength(0)
    expect(state.afterSchema.detail).toContain('Tables')
    expect(state.afterSchema.detail).toContain('customers')
    expect(state.afterSchema.detail).toContain('orders')

    expect(state.afterBreadcrumb.selectedNames).toHaveLength(0)
    expect(state.afterBreadcrumb.catalogActiveTab).toContain('Schemas')
    expect(state.afterBreadcrumb.catalogTabs).toEqual(['Schemas 1', 'Serving states 1', 'Snapshots 1'])
    expect(state.afterBreadcrumb.catalogDefaultDetail).toContain('Schemas')
    expect(state.afterBreadcrumb.catalogDefaultDetail).toContain('model')
    expect(state.afterBreadcrumb.catalogDefaultDetail).not.toContain('state_1')
    expect(state.afterBreadcrumb.catalogServingStatesDetail).toContain('state_1')
    expect(state.afterBreadcrumb.catalogSnapshotsDetail).toContain('materialize')
    expect(state.afterBreadcrumb.schemaRows).toBe(1)
    expect(state.afterBreadcrumb.schemaRowsBeforeBreadcrumb).toBe(2)
  } finally {
    await page.close()
  }
})

function testDocument(): string {
  const fiveMinutesAgo = new Date(Date.now() - 5 * 60_000).toISOString()
  const page = {
    kind: 'admin',
    title: 'Principals',
    active: 'principals',
    sidebar: {
      label: 'Admin',
      railLabel: 'Admin',
      ariaLabel: 'Admin navigation',
      storageKey: 'leapview-admin-sidebar-collapsed',
      activeId: 'principals',
      collapsible: false,
      numbered: false,
      items: [
        { id: 'principals', title: 'Principals', href: '/admin/principals', active: true },
        { id: 'groups', title: 'Groups', href: '/admin/groups', active: false },
        { id: 'agent', title: 'Agent', href: '/admin/agent', active: false },
        { id: 'storage', title: 'Storage', href: '/admin/storage', active: false },
        { id: 'queries', title: 'Queries', href: '/admin/queries', active: false },
      ],
    },
    headerTitle: 'Members',
    headerDetail: '',
    directoryList: {
      searchPlaceholder: 'Search by name or email',
      filterLabel: 'Filter members',
      items: [
        { id: 'p1', name: 'Analyst', username: 'analyst', avatarUrl: '/profile/avatars/p1/avatar-digest', email: 'analyst@example.com', href: '/admin/principals/p1', status: 'active', groupCount: 1, joinedAt: '2026-07-20', lastSeenAt: fiveMinutesAgo },
        { id: 'p2', name: 'Local Developer', username: 'dev', email: 'dev@localhost', href: '/admin/principals/p2', status: 'active', groupCount: 0, joinedAt: '2026-07-20', lastSeenAt: '' },
      ],
    },
  }
  const signals = escapeHTML(JSON.stringify({ page }))
  return `
    <!doctype html>
    <html>
      <head>
        <style>
          html, body { margin: 0; min-height: 100%; }
          body { ${typographyTestTokens} --lv-bg-app: #f6f8fa; --lv-bg-page: #fff; --lv-bg-panel: #fff; --lv-bg-panel-muted: #f6f8fa; --lv-bg-control: #f6f8fa; --lv-bg-control-hover: #f3f4f6; --lv-bg-accent: #0969da; --lv-bg-accent-muted: #ddf4ff; --lv-sidebar-bg: #f1f3f5; --lv-report-rail-bg: #ffffff; --lv-fg-default: #24292f; --lv-fg-muted: #57606a; --lv-fg-accent: #0969da; --lv-fg-link: #0969da; --lv-fg-success: #1a7f37; --lv-fg-warning: #9a6700; --lv-fg-danger: #d1242f; --lv-fg-on-accent: #fff; --lv-icon-muted: #57606a; --lv-line-muted: #d8dee4; --lv-border-width: 1px; --lv-border-default: 1px solid #d0d7de; --lv-border-muted: 1px solid #d8dee4; --lv-radius-default: 6px; --lv-radius-full: 999px; --lv-page-content-max-width: 72rem; --lv-settings-content-max-width: 40rem; --lv-workspace-detail-max-width: 72rem; --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-12: 12px; --base-size-16: 16px; --base-size-20: 20px; --base-size-24: 24px; --base-size-32: 32px; --base-size-40: 40px; --base-size-48: 48px; --base-size-64: 64px; --control-large-size: 40px; --lv-transition-fast: 160ms ease; }
          lv-admin-page { min-height: 720px; }
        </style>
      </head>
      <body>
        <main data-signals="${signals}">
          <lv-admin-page></lv-admin-page>
        </main>
        <script type="module" src="/static/vendor/datastar-1.0.2.js?v=dev"></script>
        <script type="module" src="/admin-page-under-test.js"></script>
      </body>
    </html>
  `
}

function escapeHTML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('"', '&quot;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
}
