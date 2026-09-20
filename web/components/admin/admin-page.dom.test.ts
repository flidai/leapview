import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

test('profile settings renders the signed-in identity without unsupported editable fields', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'Profile', active: 'profile', headerTitle: 'Profile', headerDetail: 'Manage your photo and display name.',
      }, personalSettings: {
        active: 'profile',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', avatarUrl: '/profile/avatar.png', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: { localPasswordEnabled: true, sessions: [], authoringSessions: [] },
        tokens: { items: [], capabilities: [] },
      } })
      const element = document.querySelector('lv-admin-page') as any
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)!
      const profile = root.querySelector('lv-personal-settings') as any
      await profile.updateComplete
      const profileRoot = profile.shadowRoot as ShadowRoot
      const main = root.querySelector('.main') as HTMLElement
      const route = root.querySelector('.route') as HTMLElement
      const header = root.querySelector('.page-header') as HTMLElement
      const avatarTrigger = profileRoot.querySelector('.avatar-trigger') as HTMLButtonElement
      const fieldLabel = profileRoot.querySelector('.settings-label') as HTMLElement
      const profileRows = Array.from(profileRoot.querySelectorAll('.profile-row')) as HTMLElement[]
      const profileRowsLargeEnough = profileRows.every((row) => row.getBoundingClientRect().height >= 64)
      const avatarSize = Math.round(avatarTrigger.getBoundingClientRect().width)
      const displayNameInput = profileRoot.querySelector('#personal-display-name') as HTMLInputElement
      const displayNameInputStyle = getComputedStyle(displayNameInput)
      const displayNameInputWidth = Math.round(displayNameInput.getBoundingClientRect().width)
      const displayNameInputHeight = Math.round(displayNameInput.getBoundingClientRect().height)
      const displayNameSaveInitiallyHidden = !profileRoot.querySelector('[data-profile-save]')
      let profileCommand: unknown = null
      profile.addEventListener('lv-personal-profile-command', (event: CustomEvent) => { profileCommand = event.detail }, { once: true })
      displayNameInput.value = 'Jacob N.'
      displayNameInput.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await profile.updateComplete
      const displayNameSave = profileRoot.querySelector('[data-profile-save]') as HTMLButtonElement | null
      const displayNameSaveVisibleWhenDirty = Boolean(displayNameSave)
      displayNameSave?.click()
      await profile.updateComplete
      const profileControlSelectors = ['.avatar-control', '.profile-email', '.profile-name-form', '.theme-picker']
      const profileControlRightEdges = profileControlSelectors.map((selector) => Math.round((profileRoot.querySelector(selector) as HTMLElement | null)?.getBoundingClientRect().right ?? 0))
      const profileControlsRightAligned = profileControlRightEdges.every((edge) => edge > 0 && Math.abs(edge - profileControlRightEdges[0]) <= 1)
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
      document.addEventListener('leapview-theme-change', (event: Event) => { appliedTheme = (event as CustomEvent<{ mode?: string }>).detail?.mode }, { once: true })
      const theme = profileRoot.querySelector('.theme-trigger') as HTMLButtonElement
      avatarTrigger.click()
      await profile.updateComplete
      theme.dispatchEvent(new PointerEvent('pointerdown', { bubbles: true, composed: true }))
      await profile.updateComplete
      const avatarMenuClosedOnOutsidePointer = !profileRoot.querySelector('[role="menu"]')
      theme.click()
      await profile.updateComplete
      const themeListbox = profileRoot.querySelector('[role="listbox"]') as HTMLElement
      const selectedTheme = themeListbox.querySelector('[role="option"][aria-selected="true"]') as HTMLElement
      const selectedThemePreview = selectedTheme.querySelector('.theme-preview') as HTMLElement
      const themeOptions = Array.from(themeListbox.querySelectorAll('[role="option"]')) as HTMLButtonElement[]
      selectedTheme.focus()
      selectedTheme.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true }))
      const keyboardNextTheme = (profileRoot.activeElement as HTMLElement | null)?.dataset.theme
      profileRoot.activeElement?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, composed: true }))
      await profile.updateComplete
      const themeClosedWithEscape = !profileRoot.querySelector('[role="listbox"]')
      const themeTriggerFocusedAfterEscape = profileRoot.activeElement === theme
      theme.click()
      await profile.updateComplete
      const reopenedOptions = Array.from(profileRoot.querySelectorAll<HTMLButtonElement>('[role="option"]'))
      reopenedOptions.find((option) => option.dataset.theme === 'dark_colorblind')?.click()
      await profile.updateComplete
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        text: profileRoot.textContent?.replace(/\s+/g, ' ').trim(),
        sectionHeadings: Array.from(profileRoot.querySelectorAll('h2'), heading => heading.textContent?.trim()),
        mainCentered: Math.abs((main.getBoundingClientRect().left + main.getBoundingClientRect().width / 2) - (route.getBoundingClientRect().left + route.getBoundingClientRect().width / 2)) <= 1,
        mainWidth: Math.round(main.getBoundingClientRect().width),
        headerGap: Math.round(profile.getBoundingClientRect().top - header.getBoundingClientRect().bottom),
        fieldLabelFontSize: getComputedStyle(fieldLabel).fontSize,
        profileRowCount: profileRows.length,
        profileRowsLargeEnough,
        avatarSize,
        displayNameTextAlign: displayNameInputStyle.textAlign,
        displayNameInputWidth,
        displayNameInputHeight,
        displayNameSaveInitiallyHidden,
        displayNameSaveVisibleWhenDirty,
        titleControlPresent: Boolean(profileRoot.querySelector('#personal-title, [for="personal-title"]')),
        usernameControlPresent: Boolean(profileRoot.querySelector('#personal-username, [for="personal-username"]')),
        profileCommand,
        profileControlsRightAligned,
        emailIsReadOnlyText: profileRoot.querySelector('.profile-email')?.tagName === 'SPAN',
        avatarMenuItems,
        avatarMenuOpen,
        avatarMenuClosed,
        avatarTriggerFocused,
        avatarMenuClosedOnOutsidePointer,
        hiddenFileInputLabel: profileRoot.querySelector('input[type="file"].avatar-input')?.getAttribute('aria-label'),
        themeOptions: themeOptions.map((option) => option.querySelector('.theme-option-label')?.textContent?.trim()),
        themeGroups: Array.from(themeListbox.querySelectorAll('[role="group"]')).map((group) => group.getAttribute('aria-label')),
        selectedTheme: selectedTheme.dataset.theme,
        selectedThemeHasPreview: Boolean(selectedTheme.querySelector('.theme-preview')),
        selectedThemePreviewClipped: selectedThemePreview.scrollWidth > selectedThemePreview.clientWidth || selectedThemePreview.scrollHeight > selectedThemePreview.clientHeight,
        themeListboxClosed: !profileRoot.querySelector('[role="listbox"]'),
        themeTriggerValue: profileRoot.querySelector('.theme-trigger-label')?.textContent?.trim(),
        account: [profileRoot.querySelector('.account-id')?.textContent?.trim(), profileRoot.querySelector('[data-sign-out]')?.textContent?.trim()],
        keyboardNextTheme,
        themeClosedWithEscape,
        themeTriggerFocusedAfterEscape,
        themeCommand,
        appliedTheme,
      }
    })
    expect(state.title).toBe('Profile')
    expect(state.text).toContain('Profile picture')
    expect(state.text).toContain('jacob@example.com')
    expect(state.text).toContain('Display name')
    expect(state.titleControlPresent).toBe(false)
    expect(state.usernameControlPresent).toBe(false)
    expect(state.text).toContain('Theme')
    expect(state.text).toContain('Account')
    expect(state.text).toContain('Account ID')
    expect(state.account).toEqual(['principal-1', 'Sign out'])
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
    expect(state.themeGroups).toEqual(['Automatic', 'Standard', 'Accessibility'])
    expect(state.selectedTheme).toBe('system')
    expect(state.selectedThemeHasPreview).toBe(true)
    expect(state.selectedThemePreviewClipped).toBe(false)
    expect(state.themeListboxClosed).toBe(true)
    expect(state.themeTriggerValue).toBe('Dark protanopia and deuteranopia')
    expect(state.keyboardNextTheme).toBe('light')
    expect(state.themeClosedWithEscape).toBe(true)
    expect(state.themeTriggerFocusedAfterEscape).toBe(true)
    expect(state.themeCommand).toEqual({ action: 'save', theme: 'dark_colorblind' })
    expect(state.appliedTheme).toBe('dark_colorblind')
    expect(state.sectionHeadings).toEqual(['Account'])
    expect(state.text).not.toContain('Archive all chats')
    expect(state.text).not.toContain('Delete all chats')
    expect(state.mainCentered).toBe(true)
    expect(state.mainWidth).toBe(640)
    expect(state.headerGap).toBeGreaterThanOrEqual(16)
    expect(state.fieldLabelFontSize).toBe('14px')
    expect(state.profileRowCount).toBe(4)
    expect(state.profileRowsLargeEnough).toBe(true)
    expect(state.avatarSize).toBeGreaterThanOrEqual(32)
    expect(state.displayNameTextAlign).toBe('center')
    expect(state.displayNameInputWidth).toBe(208)
    expect(state.displayNameInputHeight).toBeGreaterThanOrEqual(32)
    expect(state.displayNameSaveInitiallyHidden).toBe(true)
    expect(state.displayNameSaveVisibleWhenDirty).toBe(true)
    expect(state.profileCommand).toEqual({ action: 'save', displayName: 'Jacob N.' })
    expect(state.profileControlsRightAligned).toBe(true)
    expect(state.emailIsReadOnlyText).toBe(true)
    expect(state.avatarMenuItems).toEqual(['Change avatar', 'Remove avatar'])
    expect(state.avatarMenuOpen).toBe('true')
    expect(state.avatarMenuClosed).toBe(true)
    expect(state.avatarTriggerFocused).toBe(true)
    expect(state.avatarMenuClosedOnOutsidePointer).toBe(true)
    expect(state.hiddenFileInputLabel).toBe('Upload profile picture')
  } finally {
    await page.close()
  }
})

test('security settings use a unified session list, focused password dialog, and confirmed revocation', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1200, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-personal-settings'))
    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin', title: 'Security & sessions', active: 'security', headerTitle: 'Security & sessions', headerDetail: 'Manage your password and active sessions.',
      }, personalSettings: {
        active: 'security',
        profile: { id: 'principal-1', email: 'jacob@example.com', displayName: 'Jacob Nielsen', theme: 'system', identitySource: 'local', canEditDisplayName: true, hasLocalPassword: true },
        security: {
          localPasswordEnabled: true,
          sessions: [
            { id: 'session-current', kind: 'web', clientLabel: 'Chrome on Linux', current: true, createdAt: '2026-09-17T08:00:00Z', lastSeenAt: '2026-09-17T09:00:00Z', expiresAt: '2026-09-18T08:00:00Z', absoluteExpiresAt: '2026-10-17T08:00:00Z', revokedAt: '' },
            { id: 'session-other', kind: 'desktop', clientLabel: 'LeapView Desktop', current: false, createdAt: '2026-09-16T08:00:00Z', lastSeenAt: '2026-09-17T07:00:00Z', expiresAt: '2026-09-18T08:00:00Z', absoluteExpiresAt: '2026-10-17T08:00:00Z', revokedAt: '' },
          ],
          authoringSessions: [
            { id: 'authoring-1', kind: 'cli', clientId: 'LeapView CLI', targetId: 'target-1', projectId: 'sales', capabilities: ['RESOURCE_READ'], createdAt: '2026-09-15T08:00:00Z', lastUsedAt: '2026-09-17T06:00:00Z', expiresAt: '2026-09-24T08:00:00Z', revokedAt: '' },
          ],
        },
        tokens: { items: [], capabilities: [] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      let sessionCommand: unknown = null
      let passwordCommand: unknown = null
      personal.addEventListener('lv-personal-session-command', (event: CustomEvent) => { sessionCommand = event.detail })
      personal.addEventListener('lv-personal-password-command', (event: CustomEvent) => { passwordCommand = event.detail })
      const sections = Array.from(root.querySelectorAll<HTMLElement>('.security-section'))
      const passwordSection = sections.find((section) => section.getAttribute('aria-label') === 'Password')!
      const sessionsSection = sections.find((section) => section.getAttribute('aria-label') === 'Active sessions')!
      const passwordInputsBeforeOpen = root.querySelectorAll('[data-password-dialog] input').length
      passwordSection.querySelector<HTMLButtonElement>('button')!.click()
      await personal.updateComplete
      const passwordDialog = root.querySelector<HTMLDialogElement>('[data-password-dialog]')!
      const currentPassword = passwordDialog.querySelector<HTMLInputElement>('input[name="currentPassword"]')!
      const newPassword = passwordDialog.querySelector<HTMLInputElement>('input[name="newPassword"]')!
      currentPassword.value = 'old-password-value'
      currentPassword.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      newPassword.value = 'new-password-value'
      newPassword.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await personal.updateComplete
      const passwordDialogOpened = passwordDialog.open
      passwordDialog.querySelector<HTMLFormElement>('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      await personal.updateComplete
      const otherGroup = root.querySelector<HTMLElement>('.security-session-group[aria-label="Other devices"]')!
      const otherRow = otherGroup.querySelector<HTMLElement>('.security-session')!
      otherRow.querySelector<HTMLButtonElement>('.security-session-main')!.click()
      await personal.updateComplete
      const drawer = root.querySelector('lv-drawer') as any
      const drawerText = drawer.textContent?.replace(/\s+/g, ' ').trim()
      const drawerNonModal = drawer.modal === false
      ;(drawer.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.close')!.click()
      await personal.updateComplete
      otherRow.querySelector<HTMLButtonElement>('.session-action')!.click()
      await personal.updateComplete
      const revokeDialog = root.querySelector<HTMLDialogElement>('[data-session-revoke-dialog]')!
      const commandBeforeConfirmation = sessionCommand
      const dialogOpenBeforeConfirmation = revokeDialog.open
      const dialogTitle = revokeDialog.querySelector('h2')?.textContent?.trim()
      revokeDialog.querySelector<HTMLButtonElement>('.token-delete-actions button')!.click()
      await personal.updateComplete
      return {
        headings: sections.map((section) => section.querySelector('h2')?.textContent?.trim()),
        mainClass: (admin.shadowRoot as ShadowRoot).querySelector('.main')?.className,
        sessionListCount: sessionsSection.querySelectorAll('.security-session-list').length,
        groupLabels: Array.from(sessionsSection.querySelectorAll('.security-session-group-label')).map((label) => label.textContent?.trim()),
        activeCount: sessionsSection.querySelector('.security-session-count')?.textContent?.trim(),
        currentBadge: root.querySelector('.security-badge')?.textContent?.trim(),
        currentAction: root.querySelector('.security-session-group[aria-label="Current session"] .session-action')?.textContent?.trim(),
        sessionActions: [root.querySelector('[data-logout-all]')?.textContent?.trim(), otherRow.querySelector('.session-action')?.textContent?.trim()],
        authoringText: root.querySelector('.security-session-group[aria-label="CLI and authoring"]')?.textContent?.replace(/\s+/g, ' ').trim(),
        passwordInputsBeforeOpen,
        passwordDialogOpened,
        passwordDialogClosed: !root.querySelector('[data-password-dialog]'),
        passwordCommand,
        drawerText,
        drawerNonModal,
        dialogOpen: dialogOpenBeforeConfirmation,
        dialogTitle,
        commandBeforeConfirmation,
        sessionCommand,
      }
    })
    expect(state.headings).toEqual(['Password', 'Active sessions'])
    expect(state.mainClass).toContain('main-security')
    expect(state.sessionListCount).toBe(1)
    expect(state.groupLabels).toEqual(['Current', 'Other devices', 'CLI & authoring'])
    expect(state.activeCount).toBe('3 active sessions')
    expect(state.currentBadge).toBe('This device')
    expect(state.currentAction).toBe('Sign out')
    expect(state.sessionActions).toEqual(['Log out all browser and desktop sessions', 'Revoke'])
    expect(state.authoringText).toContain('LeapView CLI')
    expect(state.authoringText).toContain('sales')
    expect(state.authoringText).toContain('Resource read')
    expect(state.passwordInputsBeforeOpen).toBe(0)
    expect(state.passwordDialogOpened).toBe(true)
    expect(state.passwordDialogClosed).toBe(true)
    expect(state.passwordCommand).toEqual({ currentPassword: 'old-password-value', newPassword: 'new-password-value' })
    expect(state.drawerText).toContain('LeapView Desktop')
    expect(state.drawerText).toContain('Session ID session-other')
    expect(state.drawerText).toContain('Absolute expiration')
    expect(state.drawerNonModal).toBe(true)
    expect(state.dialogOpen).toBe(true)
    expect(state.dialogTitle).toBe('Revoke this session?')
    expect(state.commandBeforeConfirmation).toBeNull()
    expect(state.sessionCommand).toEqual({ action: 'revoke', sessionId: 'session-other' })
  } finally {
    await page.close()
  }
})

test('personal API tokens use capability selectors', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1440, height: 700 } })
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
        tokens: { items: [], capabilities: [
          { value: 'PROJECT_ADMIN', label: 'Administer project', description: 'Manage project-level access and settings.', category: 'Administration' },
          { value: 'RESOURCE_USE', label: 'Use resource', description: 'Open and use the project resource.', category: 'Resource' },
          { value: 'RESOURCE_READ', label: 'Read resource', description: 'View the resource and its governed data.', category: 'Resource' },
          { value: 'RESOURCE_EDIT', label: 'Edit resource', description: 'Create and update the resource.', category: 'Resource' },
          { value: 'RESOURCE_MANAGE', label: 'Manage resource', description: 'Delete and administer the resource.', category: 'Resource' },
          { value: 'RESOURCE_SHARE', label: 'Share resource', description: 'Share the resource with other principals.', category: 'Resource' },
          { value: 'RESOURCE_PUBLISH', label: 'Publish resource', description: 'Publish the resource to serving.', category: 'Resource' },
        ] },
      } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      const admin = document.querySelector('lv-admin-page') as any
      await admin.updateComplete
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      await personal.updateComplete
      const root = personal.shadowRoot as ShadowRoot
      const listHeading = root.querySelector('.token-page-header h2')?.textContent?.trim()
      const pageHeaderVisible = Boolean((admin.shadowRoot as ShadowRoot).querySelector('.page-header'))
      const generateNew = Array.from(root.querySelectorAll<HTMLAnchorElement>('a')).find((link) => link.textContent?.trim() === 'Generate new token') as HTMLAnchorElement
      const generateHref = generateNew.getAttribute('href')
      mergePatch({ page: { active: 'api-token-new', title: 'New personal access token', headerTitle: 'New personal access token', headerDetail: 'Create a scoped credential for API, CLI, and automation access.' } })
      await admin.updateComplete
      await personal.updateComplete
      const name = root.querySelector('#token-name') as HTMLInputElement
      const create = root.querySelector('button[type="submit"]') as HTMLButtonElement
      const expirationSelect = root.querySelector('lv-select-menu') as any
      await expirationSelect.updateComplete
      const expirationRoot = expirationSelect.shadowRoot as ShadowRoot
      const expirationTrigger = expirationRoot.querySelector('.trigger') as HTMLButtonElement
      expirationTrigger.click()
      await expirationSelect.updateComplete
      const expirationOptions = Array.from(expirationRoot.querySelectorAll<HTMLButtonElement>('.option'))
      const initial = {
        listHeading,
        pageHeaderVisible,
        generateHref,
        routeMode: personal.getAttribute('token-view'),
        createHeading: root.querySelector('.token-create-header h2')?.textContent?.trim(),
        fieldOrder: Array.from(root.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLElement>('.token-details input, .token-details textarea, .token-details lv-select-menu')).map((field) => field.id),
        permissionsHeading: root.querySelector('.permissions-heading h3')?.textContent?.trim(),
        emptyPermissions: root.querySelector('.selected-permissions-empty .settings-label')?.textContent?.trim(),
        createDisabled: create.disabled,
        expiration: {
          label: expirationRoot.querySelector('.value')?.textContent?.trim(),
          options: expirationOptions.map((option) => option.textContent?.trim()),
          selected: expirationOptions.find((option) => option.getAttribute('aria-selected') === 'true')?.dataset.value,
          triggerHeight: Math.round(expirationTrigger.getBoundingClientRect().height),
          triggerWidth: Math.round(expirationTrigger.getBoundingClientRect().width),
          optionRowsLargeEnough: expirationOptions.every((option) => option.getBoundingClientRect().height >= 32),
        },
        rawProjectField: Boolean(root.querySelector('input[placeholder*="Project ID"]')),
        rawPrivilegeField: Boolean(root.querySelector('input[placeholder*="Privileges"]')),
      }

      name.value = 'Sales automation'
      name.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      const description = root.querySelector('#token-description') as HTMLTextAreaElement
      description.value = 'Used by the weekly sales reporting job.'
      description.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await personal.updateComplete
      const add = root.querySelector('.permission-trigger') as HTMLButtonElement
      add.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true, composed: true }))
      await personal.updateComplete
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const menu = root.querySelector('.permission-menu') as HTMLElement
      const permissionList = root.querySelector('.permission-list') as HTMLElement
      const menuTitle = root.querySelector('.permission-menu-title .settings-label') as HTMLElement
      const search = root.querySelector('.permission-search input') as HTMLInputElement
      const menuRect = menu.getBoundingClientRect()
      const menuLayout = {
        bottom: Math.round(menuRect.bottom),
        viewportHeight: innerHeight,
        listScrollable: permissionList.scrollHeight > permissionList.clientHeight,
        listOverflowY: getComputedStyle(permissionList).overflowY,
        width: Math.round(menuRect.width),
        searchHeight: Math.round(search.getBoundingClientRect().height),
        optionRowsLargeEnough: Array.from(root.querySelectorAll<HTMLElement>('.permission-option')).every((option) => option.getBoundingClientRect().height >= 48),
      }
      const searchFocused = root.activeElement === search
      const menuHeader = {
        title: menuTitle.textContent?.trim(),
        labelledByTitle: menu.getAttribute('aria-labelledby') === menuTitle.id,
        selectedCount: root.querySelector('.permission-menu-count')?.textContent?.replace(/\s+/g, ' ').trim(),
        categorySummaries: Array.from(root.querySelectorAll('.permission-category')).map((category) => category.textContent?.replace(/\s+/g, ' ').trim()),
      }
      search.value = 'read resource'
      search.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await personal.updateComplete
      const filteredPermissions = Array.from(root.querySelectorAll('.permission-option .settings-label')).map((label) => label.textContent?.trim())
      const readPermission = root.querySelector('input[type="checkbox"][value="resource-content"]') as HTMLInputElement
      const readDescription = root.querySelector('.permission-option .settings-description') as HTMLElement
      const readDescribedBy = readPermission.getAttribute('aria-describedby') === readDescription.id
      readPermission.click()
      await personal.updateComplete
      const selectedMenuState = {
        count: root.querySelector('.permission-menu-count')?.textContent?.replace(/\s+/g, ' ').trim(),
        selected: root.querySelector('.permission-option')?.getAttribute('data-selected'),
        categorySummary: root.querySelector('.permission-category')?.textContent?.replace(/\s+/g, ' ').trim(),
      }
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      await personal.updateComplete
      await Promise.resolve()
      const selectedPermissions = Array.from(root.querySelectorAll('.selected-permission .settings-label')).map((label) => label.textContent?.trim())
      const triggerFocused = root.activeElement === add

      let command: any = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      const form = root.querySelector('.token-form') as HTMLFormElement
      form.dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      const confirmation = root.querySelector('[data-token-confirm-dialog]') as HTMLDialogElement
      const confirmationText = confirmation.textContent?.replace(/\s+/g, ' ').trim()
      ;(confirmation.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      const pending = {
        name: (root.querySelector('#token-name') as HTMLInputElement).value,
        selectedPermissions: root.querySelectorAll('.selected-permission').length,
        buttonText: (root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).textContent?.trim(),
      }
      document.dispatchEvent(new CustomEvent('datastar-fetch', { detail: { type: 'error', argsRaw: { status: '403' } } }))
      await personal.updateComplete
      const failed = {
        name: (root.querySelector('#token-name') as HTMLInputElement).value,
        selectedPermissions: root.querySelectorAll('.selected-permission').length,
        error: root.querySelector('[role="alert"]')?.textContent?.trim(),
        createDisabled: (root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).disabled,
      }
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      window.history.replaceState(window.history.state, '', '/admin/api-tokens/new')
      mergePatch({ personalSettings: { tokens: { items: [
        { id: 'token-1', name: 'Sales automation', description: 'Used by the weekly sales reporting job.', capabilities: ['RESOURCE_READ'], createdAt: '2026-08-12T06:40:00Z', lastUsedAt: '', expiresAt: '2026-10-17T06:40:00Z', revokedAt: '' },
      ], newToken: 'lv_created_secret' } } })
      await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
      await personal.updateComplete
      const succeeded = {
        createViewClosed: !root.querySelector('#token-name'),
        selectedPermissions: root.querySelectorAll('.selected-permission').length,
        tokenNames: Array.from(root.querySelectorAll('.token-name')).map((element) => element.textContent?.trim()),
        description: root.querySelector('.token-description')?.textContent?.trim(),
        notice: root.querySelector('lv-one-time-secret')?.shadowRoot?.querySelector('[role="status"]')?.textContent?.trim(),
        pathname: window.location.pathname,
      }
      let deleteCommand: any = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { deleteCommand = event.detail }, { once: true })
      ;(Array.from(root.querySelectorAll<HTMLButtonElement>('button')).find((button) => button.textContent?.trim() === 'Delete') as HTMLButtonElement).click()
      await personal.updateComplete
      const deleteDialog = root.querySelector('[data-token-delete-dialog]') as HTMLDialogElement
      const deletion = {
        open: deleteDialog.open,
        title: deleteDialog.querySelector('h2')?.textContent?.trim(),
        warning: deleteDialog.querySelector('.token-delete-warning')?.textContent?.replace(/\s+/g, ' ').trim(),
        commandBeforeConfirmation: deleteCommand,
        confirmLabel: deleteDialog.querySelector('.token-delete-actions button')?.textContent?.trim(),
        commandAfterConfirmation: null as any,
        closedAfterConfirmation: false,
      }
      ;(deleteDialog.querySelector('.token-delete-actions button') as HTMLButtonElement).click()
      await personal.updateComplete
      deletion.commandAfterConfirmation = deleteCommand
      deletion.closedAfterConfirmation = !root.querySelector('[data-token-delete-dialog]')
      return {
        initial,
        menuLayout,
        menuHeader,
        filteredPermissions,
        readDescribedBy,
        selectedMenuState,
        selectedPermissions,
        menuClosed: !root.querySelector('.permission-menu'),
        searchFocused,
        triggerFocused,
        confirmationText,
        command,
        pending,
        failed,
        succeeded,
        deletion,
      }
    })

    await page.setViewportSize({ width: 390, height: 700 })
    const mobile = await page.evaluate(async () => {
      const admin = document.querySelector('lv-admin-page') as any
      const personal = (admin.shadowRoot as ShadowRoot).querySelector('lv-personal-settings') as any
      const root = personal.shadowRoot as ShadowRoot
      await personal.updateComplete
      personal.tokenView = 'create'
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
      listHeading: 'Personal access tokens',
      pageHeaderVisible: false,
      generateHref: '/admin/api-tokens/new',
      routeMode: 'create',
      createHeading: 'New personal access token',
      fieldOrder: ['token-name', 'token-description', 'token-expiration-preset'],
      permissionsHeading: 'Permissions',
      emptyPermissions: 'No permissions added yet',
      createDisabled: true,
      expiration: {
        label: expect.stringMatching(/^30 days \(.+\)$/),
        options: expect.arrayContaining(['Custom']),
        selected: '30',
        triggerHeight: 32,
        triggerWidth: expect.any(Number),
        optionRowsLargeEnough: true,
      },
      rawProjectField: false,
      rawPrivilegeField: false,
    })
    expect(state.initial.expiration.triggerWidth).toBeGreaterThanOrEqual(190)
    expect(state.initial.expiration.triggerWidth).toBeLessThanOrEqual(260)
    expect(state.menuLayout.bottom).toBeLessThanOrEqual(state.menuLayout.viewportHeight - 16)
    expect(state.menuLayout.listScrollable).toBe(true)
    expect(state.menuLayout.listOverflowY).toBe('auto')
    expect(state.menuLayout.width).toBeGreaterThanOrEqual(400)
    expect(state.menuLayout.searchHeight).toBeGreaterThanOrEqual(40)
    expect(state.menuLayout.optionRowsLargeEnough).toBe(true)
    expect(state.menuHeader).toEqual({
      title: 'Select token permissions',
      labelledByTitle: true,
      selectedCount: '0 selected',
      categorySummaries: ['Administration 0 / 1', 'Resource 0 / 4'],
    })
    expect(state.filteredPermissions).toEqual(['Resource access'])
    expect(state.readDescribedBy).toBe(true)
    expect(state.selectedMenuState).toEqual({ count: '1 selected', selected: 'true', categorySummary: 'Resource 1 / 4' })
    expect(state.selectedPermissions).toEqual(['Resource access'])
    expect(state.menuClosed).toBe(true)
    expect(state.searchFocused).toBe(true)
    expect(state.triggerFocused).toBe(true)
    expect(state.command).toMatchObject({
      action: 'create', name: 'Sales automation', description: 'Used by the weekly sales reporting job.', capabilities: ['RESOURCE_USE', 'RESOURCE_READ'],
    })
    expect(state.command.expiresAt).toMatch(/^\d{4}-\d{2}-\d{2}T/)
    expect(state.confirmationText).toContain('will be ready for use immediately')
    expect(state.pending).toEqual({ name: 'Sales automation', selectedPermissions: 1, buttonText: 'Generating…' })
    expect(state.failed).toEqual({
      name: 'Sales automation', selectedPermissions: 1,
      error: 'Token creation is not permitted for your account.',
      createDisabled: false,
    })
    expect(state.succeeded.createViewClosed).toBe(true)
    expect(state.succeeded.selectedPermissions).toBe(0)
    expect(state.succeeded.tokenNames).toContain('Sales automation')
    expect(state.succeeded.description).toBe('Used by the weekly sales reporting job.')
    expect(state.succeeded.notice).toContain('Copy your personal access token now')
    expect(state.succeeded.pathname).toBe('/admin/api-tokens')
    expect(state.deletion).toEqual({
      open: true,
      title: 'Are you sure you want to delete this token?',
      warning: 'Any applications or scripts using Sales automation will no longer be able to access the LeapView API. You cannot undo this action.',
      commandBeforeConfirmation: null,
      confirmLabel: 'I understand, delete this token',
      commandAfterConfirmation: { action: 'revoke', tokenId: 'token-1' },
      closedAfterConfirmation: true,
    })
    expect(mobile.position).toBe('fixed')
    expect(mobile.left).toBeGreaterThanOrEqual(16)
    expect(mobile.right).toBeLessThanOrEqual(mobile.viewportWidth - 16)
    expect(mobile.bottom).toBeLessThanOrEqual(mobile.viewportHeight - 16)
    expect(mobile.closed).toBe(true)
  } finally {
    await page.close()
  }
})

test('personal API token permissions expose enforceable access levels', async () => {
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
        tokens: { items: [], capabilities: [
          { value: 'PROJECT_ADMIN', label: 'Project administration', description: 'Manage project-level access and settings.', category: 'Administration' },
          { value: 'RESOURCE_USE', label: 'Use resource', description: 'Open and use the project resource.', category: 'Resource' },
          { value: 'RESOURCE_READ', label: 'Read resource', description: 'View the resource and its governed data.', category: 'Resource' },
          { value: 'RESOURCE_EDIT', label: 'Edit resource', description: 'Create and update the resource.', category: 'Resource' },
          { value: 'RESOURCE_MANAGE', label: 'Manage resource', description: 'Delete and administer the resource.', category: 'Resource' },
          { value: 'RESOURCE_SHARE', label: 'Share resource', description: 'Share the resource with other principals.', category: 'Resource' },
          { value: 'RESOURCE_PUBLISH', label: 'Publish resource', description: 'Publish the resource to serving.', category: 'Resource' },
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
      ;(root.querySelector('.permission-trigger') as HTMLButtonElement).click()
      await personal.updateComplete
      ;(root.querySelector('input[type="checkbox"][value="project-administration"]') as HTMLInputElement).click()
      await personal.updateComplete
      ;(root.querySelector('input[type="checkbox"][value="resource-content"]') as HTMLInputElement).click()
      await personal.updateComplete
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      await personal.updateComplete

      const administrationAccess = root.querySelector('.permission-access-trigger[data-permission="project-administration"]') as HTMLButtonElement
      const initialAdministrationAccess = administrationAccess.textContent?.replace(/\s+/g, ' ').trim()
      administrationAccess.click()
      await personal.updateComplete
      const administrationAccessOptions = Array.from(root.querySelectorAll('.permission-access-option')).map((option) => option.textContent?.trim())
      ;(Array.from(root.querySelectorAll<HTMLButtonElement>('.permission-access-option')).find((option) => option.textContent?.trim() === 'Read and write'))?.click()
      await personal.updateComplete

      const resourceAccess = root.querySelector('.permission-access-trigger[data-permission="resource-content"]') as HTMLButtonElement
      const initialResourceAccess = resourceAccess.textContent?.replace(/\s+/g, ' ').trim()
      resourceAccess.click()
      await personal.updateComplete
      const resourceAccessOptions = Array.from(root.querySelectorAll('.permission-access-option')).map((option) => option.textContent?.trim())
      ;(Array.from(root.querySelectorAll<HTMLButtonElement>('.permission-access-option')).find((option) => option.textContent?.trim() === 'Read and write'))?.click()
      await personal.updateComplete

      ;(root.querySelector('#token-name') as HTMLInputElement).value = 'Content automation'
      ;(root.querySelector('#token-name') as HTMLInputElement).dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      let command: any = null
      personal.addEventListener('lv-personal-token-command', (event: CustomEvent) => { command = event.detail }, { once: true })
      ;(root.querySelector('.token-form') as HTMLFormElement).dispatchEvent(new SubmitEvent('submit', { bubbles: true, composed: true, cancelable: true }))
      await personal.updateComplete
      ;(root.querySelector('.token-confirm-actions .primary') as HTMLButtonElement).click()
      await personal.updateComplete
      return {
        initialAdministrationAccess,
        administrationAccessOptions,
        selectedAdministrationAccess: administrationAccess.textContent?.replace(/\s+/g, ' ').trim(),
        initialResourceAccess,
        resourceAccessOptions,
        selectedResourceAccess: resourceAccess.textContent?.replace(/\s+/g, ' ').trim(),
        selectedPermissions: Array.from(root.querySelectorAll('.selected-permission .settings-label')).map((label) => label.textContent?.trim()),
        fixedAccessControls: root.querySelectorAll('.permission-access-fixed').length,
        command,
      }
    })

    expect(state.initialAdministrationAccess).toBe('Access: Read-only')
    expect(state.administrationAccessOptions).toEqual(['Read-only', 'Read and write'])
    expect(state.selectedAdministrationAccess).toBe('Access: Read and write')
    expect(state.initialResourceAccess).toBe('Access: Read-only')
    expect(state.resourceAccessOptions).toEqual(['Read-only', 'Read and write'])
    expect(state.selectedResourceAccess).toBe('Access: Read and write')
    expect(state.selectedPermissions).toEqual(['Project administration', 'Resource access'])
    expect(state.fixedAccessControls).toBe(0)
    expect(state.command).toMatchObject({
      action: 'create', name: 'Content automation', description: '', capabilities: ['PROJECT_ADMIN', 'RESOURCE_USE', 'RESOURCE_READ', 'RESOURCE_EDIT'],
    })
    expect(state.command.expiresAt).toMatch(/^\d{4}-\d{2}-\d{2}T/)
  } finally {
    await page.close()
  }
})

test('users directory list delegates search and filtering to the page stream', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
      await page.goto(fixture.baseURL)
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

    expect(state.initial.title).toBe('Users')
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

test('mobile entity lists advertise horizontal table scrolling while desktop stays quiet', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 390, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-entity-list'))
    const mobile = await page.evaluate(async () => {
      const list = document.querySelector('lv-admin-page')?.shadowRoot?.querySelector('lv-entity-list') as any
      await list?.updateComplete
      const region = list?.querySelector('.entity-list-table-wrap') as HTMLElement | null
      const hint = list?.querySelector('.entity-list-scroll-hint') as HTMLElement | null
      return {
        role: region?.getAttribute('role'),
        label: region?.getAttribute('aria-label'),
        tabIndex: region?.getAttribute('tabindex'),
        hint: hint?.textContent?.replace(/\s+/g, ' ').trim(),
        hintDisplay: hint ? getComputedStyle(hint).display : '',
      }
    })
    expect(mobile).toEqual({
      role: 'region', label: 'Scrollable Users table', tabIndex: '0',
      hint: 'Swipe horizontally to see more columns', hintDisplay: 'block',
    })
  } finally {
    await page.close()
  }

  const desktop = await fixture.browser.newPage({ viewport: { width: 1280, height: 760 } })
  try {
    await desktop.goto(fixture.baseURL)
    await desktop.waitForFunction(() => customElements.get('lv-entity-list'))
    expect(await desktop.locator('lv-admin-page').locator('.entity-list-scroll-hint').evaluate((element) => getComputedStyle(element).display)).toBe('none')
  } finally {
    await desktop.close()
  }
})

test('groups admin uses the reusable entity list and delegates search to the page stream', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
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
        iconsArePlain: Array.from(root.querySelectorAll('.entity-list-icon')).every((icon) => icon.classList.contains('is-plain')),
      }
    })

    expect(state.title).toBe('Groups')
    expect(state.before).toHaveLength(2)
    expect(state.after).toEqual(state.before)
    expect(state.filterOptions).toEqual(['All', 'Local', 'Scim'])
    expect(state.href).toBe('/admin/groups/operations')
    expect(state.toolbarActions).toEqual(['Export CSV', 'Create group'])
    expect(state.iconsArePlain).toBe(true)
  } finally {
    await page.close()
  }
})

test('group detail lets the shared detail shell own the page heading and summary', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1100, height: 760 } })
  try {
    await page.goto(fixture.baseURL)
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
    const page = await fixture.browser.newPage({ viewport })
    try {
      await page.goto(fixture.baseURL)
      await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
      await page.locator('lv-admin-page').evaluate((element: any) => element.updateComplete)

      const state = await page.locator('lv-admin-page').evaluate(async (element: any) => {
        const root = (element.shadowRoot as ShadowRoot)
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

      expect(state.title).toBe('Users')
      expect(state.headerText).toBe('Users')
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
      projectId: 'sales',
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
      queryJson: '{"projectId":"sales","target":"orders"}',
      createdAt: '2026-07-02T10:00:00Z',
    },
    {
      id: 'queryevent_2',
      projectId: 'operations',
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
      queryJson: '{"projectId":"operations","target":"customers"}',
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
      projectId: 'sales',
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
      queryJson: '{"projectId":"sales","target":"orders"}',
      createdAt: '2026-07-02T10:00:00Z',
    },
  }
}

function queryAuditFilterMenusFixture() {
  return [
    {
      id: 'project',
      label: 'Project',
      summaryLabel: 'Project',
      mode: 'multi',
      search: '',
      selected: [],
      loading: false,
      error: '',
      placeholder: 'Search projects',
      emptyLabel: 'No projects found.',
      options: [
        { value: 'sales', label: 'sales', icon: 'project', countLabel: '1', selected: false, disabled: false },
        { value: 'operations', label: 'operations', icon: 'project', countLabel: '1', selected: false, disabled: false },
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
      runtime: event.projectId || '-',
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

test('query audit exposes supported time and status shortcuts with clearable filters', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
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
      const root = element.shadowRoot as ShadowRoot
      const shortcutLabels = Array.from(root.querySelectorAll<HTMLButtonElement>('.query-filter-shortcut')).map((button) => button.textContent?.trim())
      const timeLabels = Array.from(root.querySelectorAll<HTMLButtonElement>('.query-time-preset')).map((button) => button.textContent?.trim())
      const slowShortcut = Array.from(root.querySelectorAll<HTMLButtonElement>('button')).find((button) => button.textContent?.trim() === 'Slow')
      root.querySelector<HTMLButtonElement>('.query-time-preset')?.click()
      await element.updateComplete
      const timeCommand = (window as any).queryHistoryCommands.at(-1)
      root.querySelector<HTMLButtonElement>('.query-filter-shortcut')?.click()
      await element.updateComplete
      const failedCommand = (window as any).queryHistoryCommands.at(-1)
      const activeSummary = root.querySelector('.query-filter-summary')?.textContent?.replace(/\s+/g, ' ').trim()
      root.querySelector<HTMLButtonElement>('.query-filter-clear')?.click()
      await element.updateComplete
      const clearCommand = (window as any).queryHistoryCommands.at(-1)
      const customFrom = root.querySelector<HTMLInputElement>('#query-filter-from')!
      customFrom.value = '2026-07-01'
      customFrom.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await element.updateComplete
      const customCommand = (window as any).queryHistoryCommands.at(-1)
      return {
        shortcutLabels,
        timeLabels,
        slowShortcut: Boolean(slowShortcut),
        timeCommand,
        failedCommand,
        activeSummary,
        clearCommand,
        customCommand,
        customFromValue: customFrom.value,
      }
    }, queryAuditFixturePage())

    expect(state.shortcutLabels).toEqual(['Failed'])
    expect(state.timeLabels).toEqual(['Last hour', 'Last 24 hours', 'Last 7 days', 'Current date'])
    expect(state.slowShortcut).toBe(false)
    expect(state.timeCommand).toMatchObject({ action: 'reset', filters: { from: expect.stringMatching(/Z$/), to: expect.stringMatching(/Z$/) } })
    expect(state.timeCommand.filters.from).not.toBe(state.timeCommand.filters.to)
    expect(state.failedCommand).toMatchObject({ action: 'reset', filters: { statuses: ['error'], from: expect.any(String), to: expect.any(String) } })
    expect(state.activeSummary).toContain('Active filters')
    expect(state.activeSummary).toContain('error')
    expect(state.clearCommand).toEqual(expect.objectContaining({ action: 'reset', filters: {} }))
    expect(state.customCommand).toMatchObject({ action: 'reset', filters: { from: '2026-07-01T00:00:00.000Z' } })
    expect(state.customFromValue).toBe('2026-07-01')
  } finally {
    await page.close()
  }
})

test('query audit page filters table rows and exposes optional metadata columns', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
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
      const root = (element.shadowRoot as ShadowRoot)
      const search = root.querySelector<HTMLInputElement>('#query-filter-search')!
      search.value = 'select status'
      search.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await new Promise((resolve) => setTimeout(resolve, 250))
      await element.updateComplete
      const commandAfterSearch = (window as any).queryHistoryCommands.at(-1)
      const menus = Array.from(root.querySelectorAll('lv-filter-menu')) as TestDomElement[]
      menus[0]?.shadowRoot?.querySelector<HTMLButtonElement>('.trigger')?.click()
      await menus[0]?.updateComplete
      const projectMenuSearch = menus[0]?.shadowRoot?.querySelector<HTMLInputElement>('.search input')
      if (projectMenuSearch) {
        projectMenuSearch.value = 'oper'
        projectMenuSearch.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      }
      await new Promise((resolve) => setTimeout(resolve, 250))
      await element.updateComplete
      const filterSearchCommand = (window as any).queryHistoryCommands.at(-1)
      menus[2]?.shadowRoot?.querySelector<HTMLButtonElement>('.trigger')?.click()
      await menus[2]?.updateComplete
      menus[2]?.shadowRoot?.querySelector<HTMLInputElement>('.option input')?.click()
      await element.updateComplete
      const filterToggleCommand = (window as any).queryHistoryCommands.at(-1)
      const table = root.querySelector('lv-record-table') as TestDomElement
      const rowText = table?.textContent ?? ''
      ;(table.querySelector('.record-table-column-selector summary') as HTMLElement | null)?.click()
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
      const drawer = root.querySelector('lv-drawer') as TestDomElement
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
      const drawerIsModal = (drawer as (TestDomElement & { modal?: boolean }) | null)?.modal
      const drawerModal = drawerPanel?.getAttribute('aria-modal') ?? null
      const drawerClose = (drawer?.shadowRoot as ShadowRoot | null)?.querySelector<HTMLButtonElement>('.close')
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
      const recreatedTable = (recreated.shadowRoot as ShadowRoot).querySelector('lv-record-table') as any
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
    expect(state.firstMenuText).toMatch(/Project/)
    expect(state.hasMetrics).toBe(false)
    expect(state.rowText).toMatch(/Query/)
    expect(state.rowText).not.toMatch(/Status/)
    expect(state.rowText).toMatch(/Started/)
    expect(state.rowText).toMatch(/Jul 2/)
    expect(state.rowText).not.toMatch(/2026-07-02T/)
    expect(state.rowText).toMatch(/Source type/)
    expect(state.rowText).toMatch(/Runtime/)
    expect(state.rowText).toMatch(/User/)
    expect(state.rowText).toMatch(/analyst/)
    expect(state.rowText).toMatch(/select status from orders/)
    expect(state.rowText).toMatch(/orders/)
    expect(state.rowText).toMatch(/customers/)
    expect(state.rowText).not.toMatch(/stale_page_event/)
    expect(state.commandAfterSearch).toMatchObject({ action: 'reset', limit: 50, filters: { search: 'select status' } })
    expect(state.filterSearchCommand).toMatchObject({ action: 'filter_search', filterMenu: { menuId: 'project', action: 'search', search: 'oper' } })
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
    expect(state.hasSubtitle).toBe(true)
    expect(state.statusTextColor).toBe(state.statusColor)
    expect(state.statusIconColor).not.toBe(state.statusColor)
    expect(state.drawerText).toMatch(/queryevent_1/)
    expect(state.drawerText).toMatch(/req_1/)
    expect(state.drawerText).toMatch(/corr_1/)
    expect(state.drawerHasCodeBlock).toBe(true)
    expect(state.drawerCode).toMatch(/select\s+status\s+from\s+orders/i)
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
    expect(state.refreshedHeaders).not.toContain('Runtime')
    expect(state.refreshedHeaders).toContain('Operation')
    expect(state.refreshedHeaders).not.toContain('Status')
  } finally {
    await page.close()
  }
})

test('query audit emits load more commands from backend-driven history state', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
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
      const root = (element.shadowRoot as ShadowRoot)
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
        filterMenus: fixture.queryHistory.filterMenus.map((menu: any) => menu.id === 'project' ? {
          ...menu,
          summaryLabel: 'operations',
          selected: ['operations'],
          options: menu.options.map((option: any) => ({ ...option, selected: option.value === 'operations' })),
        } : menu),
        filters: { projects: ['operations'] },
        nextCursor: '',
        hasMore: false,
        loadedCountLabel: '1 query loaded',
      } })
      await element.updateComplete
      const updatedText = root.textContent ?? ''
      const projectMenu = root.querySelector('lv-filter-menu') as HTMLElement | null
      return {
        footerText,
        command,
        updatedText,
        hasLoadMoreAfterPatch: Boolean(root.querySelector('.query-history-load-more')),
        projectFilterText: projectMenu?.shadowRoot?.textContent ?? '',
      }
    }, queryAuditFixturePage())

    expect(state.footerText).toMatch(/2 queries loaded/)
    expect(state.command).toMatchObject({ action: 'load_more', pageToken: 'cursor_next', limit: 50 })
    expect(state.updatedText).toMatch(/customers/)
    expect(state.updatedText).not.toMatch(/orders/)
    expect(state.hasLoadMoreAfterPatch).toBe(false)
    expect(state.projectFilterText).toMatch(/operations/)
  } finally {
    await page.close()
  }
})

test('query audit detail drawer behaves as a mobile overlay', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 390, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
      const state = await page.evaluate(async (fixture) => {
        const element = document.createElement('lv-admin-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: fixture, adminQueryHistory: fixture.queryHistory, adminQueryDetail: { eventId: '', loading: false, error: '' } })
      document.body.replaceChildren(element)
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)
      const table = root.querySelector('lv-record-table') as TestDomElement
      table.querySelector<HTMLElement>('tbody tr.record-row')?.click()
      await element.updateComplete
      mergePatch({ adminQueryDetail: fixture.queryDetail })
      await element.updateComplete
      const drawer = root.querySelector('lv-drawer') as TestDomElement
      const overlay = (drawer.shadowRoot as ShadowRoot).querySelector('.overlay') as HTMLElement
      const drawerPanel = (drawer.shadowRoot as ShadowRoot).querySelector('.drawer') as HTMLElement
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))
      const state = await page.evaluate(async (fixture) => {
        const element = document.createElement('lv-admin-page') as any
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: fixture, adminQueryHistory: fixture.queryHistory, adminQueryDetail: { eventId: '', loading: false, error: '' } })
      document.body.replaceChildren(element)
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)
      const table = root.querySelector('lv-record-table') as TestDomElement
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
        projectId: 'operations',
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
        queryJson: '{"projectId":"operations","target":"customers"}',
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

test('storage renders a simple shared table with a schema column', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  await page.route('**/profile/avatars/**', (route) => route.fulfill({ status: 204 }))
  const consoleErrors: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-entity-list'))

    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      const table = (schema: string, name: string, rows: number) => ({
        key: `${schema}\u0000${name}`,
        schema,
        name,
        type: 'table',
        beginSnapshot: 7,
        rowCount: rows,
        rowCountLabel: rows.toLocaleString('en-US'),
        columnCount: 3,
        fileCount: 1,
        sizeBytes: 12288,
        sizeLabel: '12 KiB',
      })
      mergePatch({ page: {
        kind: 'admin',
        title: 'Storage',
        active: 'storage',
        headerTitle: 'Storage',
        headerDetail: 'Browse tables and views across schemas.',
        metrics: [{ label: 'Total data size', value: '36 KiB', detail: '3 active files' }],
        storage: {
          summary: { totalDataSizeLabel: '36 KiB', tableCount: 3, dataFileCount: 3 },
          status: '',
          tables: [table('model', 'orders', 32000204), table('model', 'customers', 1200), { ...table('staging', 'events', 800), type: 'view' }],
        },
      } })
      const element = document.querySelector('lv-admin-page') as any
      await element.updateComplete
      const list = (element.shadowRoot as ShadowRoot).querySelector('lv-entity-list') as any
      await list.updateComplete
      const root = list as HTMLElement
      root.style.width = '672px'
      const rowState = () => ({
        rows: Array.from(root.querySelectorAll('.entity-list-table-row .entity-list-title')).map((title) => title.textContent?.trim()),
        schemas: Array.from(root.querySelectorAll('.entity-list-table-row')).map((row) => row.querySelectorAll('.entity-list-cell')[0]?.textContent?.trim()),
        types: Array.from(root.querySelectorAll('.entity-list-table-row')).map((row) => row.querySelectorAll('.entity-list-cell')[1]?.textContent?.trim()),
      })
      const initial = rowState()
      const columnLabels = Array.from(root.querySelectorAll('.entity-list-sort-button > span:first-child')).map((label) => label.textContent?.trim())
      const listText = root.textContent ?? ''
      const initialIconState = {
        hasRowIcons: Boolean(root.querySelector('.entity-list-table-row .entity-list-icon svg')),
        spacerCount: root.querySelectorAll('.entity-list-table-row .entity-list-icon-spacer').length,
        classes: Array.from(root.querySelectorAll('.entity-list-table-row .entity-list-icon')).map((icon) => icon.className),
      }
      const columns = Array.from(root.querySelectorAll('col'))
      const headerButtons = Array.from(root.querySelectorAll<HTMLElement>('.entity-list-sort-button'))
      const firstTitle = root.querySelector<HTMLElement>('.entity-list-table-row .entity-list-title')!
      const firstSchemaCell = root.querySelector<HTMLElement>('.entity-list-table-row td')!
      const tableWrap = root.querySelector<HTMLElement>('.entity-list-table-wrap')!
      const layout = {
        nameColumnWidth: columns[0].getBoundingClientRect().width,
        nameHeaderRight: headerButtons[0].getBoundingClientRect().right,
        schemaHeaderLeft: headerButtons[1].getBoundingClientRect().left,
        firstTitleRight: firstTitle.getBoundingClientRect().right,
        firstTitleLeft: firstTitle.getBoundingClientRect().left,
        firstSchemaLeft: firstSchemaCell.getBoundingClientRect().left,
        scrollWidth: tableWrap.scrollWidth,
        clientWidth: tableWrap.clientWidth,
      }
      const hrefs = Array.from(root.querySelectorAll<HTMLAnchorElement>('.entity-list-table-row a.entity-list-identity')).map((link) => link.getAttribute('href'))
      const schemaFilter = root.querySelector<HTMLSelectElement>('.entity-filter')!
      schemaFilter.value = 'staging'
      schemaFilter.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await list.updateComplete
      const schemaFiltered = rowState()
      schemaFilter.value = 'all'
      schemaFilter.dispatchEvent(new Event('change', { bubbles: true, composed: true }))
      await list.updateComplete
      const input = root.querySelector<HTMLInputElement>('.entity-search input')!
      input.value = 'events'
      input.dispatchEvent(new Event('input', { bubbles: true, composed: true }))
      await list.updateComplete
      const filtered = rowState()
      return {
        initial,
        filtered,
        columnLabels,
        listText,
        metricsText: Array.from((element.shadowRoot as ShadowRoot).querySelectorAll('.metrics .metric')).map((metric: Element) => metric.textContent?.replace(/\s+/g, ' ').trim()),
        hrefs,
        schemaFiltered,
        initialIconState,
        listLabel: root.querySelector('table')?.getAttribute('aria-label'),
        clientFilter: list.clientFilter,
        groupBy: list.groupBy,
        groupHeaderCount: root.querySelectorAll('.entity-list-group-row').length,
        filterOptions: Array.from(root.querySelectorAll('.entity-filter option')).map((option) => option.textContent?.trim()),
        columnWidths: list.columns.map((column: { width?: string }) => column.width ?? ''),
        layout,
      }
    })

    expect(state.listLabel).toBe('Storage tables')
    expect(state.clientFilter).toBe(true)
    expect(state.groupBy).toBe('')
    expect(state.groupHeaderCount).toBe(0)
    expect(state.filterOptions).toEqual(['All schemas', 'model', 'staging'])
    expect(state.columnLabels).toEqual(['Name', 'Schema', 'Type', 'Rows', 'Columns', 'Files', 'Data size', 'Snapshot'])
    expect(state.columnWidths).toEqual(['155px', '85px', '60px', '85px', '70px', '55px', '85px', '75px'])
    expect(state.layout.nameColumnWidth).toBeGreaterThanOrEqual(155)
    expect(state.layout.nameHeaderRight).toBeLessThan(state.layout.schemaHeaderLeft)
    expect(state.layout.firstTitleRight).toBeLessThan(state.layout.firstSchemaLeft)
    expect(state.layout.scrollWidth).toBeLessThanOrEqual(state.layout.clientWidth + 1)
    expect(state.listText).not.toContain('DuckLake catalog')
    expect(state.metricsText).toEqual(['Total data size 36 KiB 3 active files'])
    expect(state.hrefs).toEqual([
      '/admin/storage/tables/model/orders',
      '/admin/storage/tables/model/customers',
      '/admin/storage/tables/staging/events',
    ])
    expect(state.initialIconState.hasRowIcons).toBe(true)
    expect(state.initialIconState.spacerCount).toBe(0)
    expect(state.initialIconState.classes).toEqual([
      'entity-list-icon is-plain entity-list-icon-table',
      'entity-list-icon is-plain entity-list-icon-table',
      'entity-list-icon is-plain entity-list-icon-view',
    ])
    expect(state.initial.rows).toEqual(['orders', 'customers', 'events'])
    expect(state.initial.schemas).toEqual(['model', 'model', 'staging'])
    expect(state.initial.types).toEqual(['Table', 'Table', 'View'])
    expect(state.schemaFiltered.rows).toEqual(['events'])
    expect(state.schemaFiltered.schemas).toEqual(['staging'])
    expect(state.filtered.rows).toEqual(['events'])
    expect(state.filtered.schemas).toEqual(['staging'])
    expect(state.filtered.types).toEqual(['View'])
    expect(consoleErrors).toEqual([])
    if (process.env.LEAPVIEW_CAPTURE_STORAGE_V2) {
      const list = page.locator('lv-admin-page lv-entity-list')
      await list.locator('.entity-search input').fill('')
      await list.evaluate((element: any) => element.updateComplete)
      await list.screenshot({ path: process.env.LEAPVIEW_CAPTURE_STORAGE_V2 })
    }
  } finally {
    await page.close()
  }
})

test('storage catalog errors use a user-facing callout and hide list controls', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-entity-list'))

    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin',
        title: 'Storage',
        active: 'storage',
        headerTitle: 'Storage',
        headerDetail: 'Browse tables and views across schemas.',
        storage: {
          summary: { totalDataSizeLabel: '', tableCount: 0, dataFileCount: 0 },
          status: 'DuckLake catalog contains more than 10000 tables',
          tables: [],
        },
      } })
      const element = document.querySelector('lv-admin-page') as any
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const list = root.querySelector('lv-entity-list') as any
      await list.updateComplete
      const error = root.querySelector('[role="alert"]') as HTMLElement
      const details = error.querySelector('details') as HTMLDetailsElement
      return {
        heading: error.querySelector('strong')?.textContent?.trim(),
        message: error.querySelector('p')?.textContent?.trim(),
        technicalDetails: error.querySelector('code')?.textContent?.trim(),
        technicalDetailsCollapsed: !details.open,
        listEmpty: list.querySelector('.entity-list-empty')?.textContent?.trim(),
        toolbar: Boolean(list.querySelector('.entity-toolbar')),
        search: Boolean(list.querySelector('.entity-search')),
        clientFilter: list.clientFilter,
        showToolbar: list.showToolbar,
        retry: error.querySelector('.storage-retry')?.textContent?.trim(),
      }
    })

    expect(state.heading).toBe('Storage metadata is temporarily unavailable.')
    expect(state.message).toContain('could not load the table catalog')
    expect(state.technicalDetails).toBe('DuckLake catalog contains more than 10000 tables')
    expect(state.technicalDetailsCollapsed).toBe(true)
    expect(state.listEmpty).toBe('No storage tables are available.')
    expect(state.toolbar).toBe(false)
    expect(state.search).toBe(false)
    expect(state.clientFilter).toBe(false)
    expect(state.showToolbar).toBe(false)
    expect(state.retry).toBe('Retry')
  } finally {
    await page.close()
  }
})

test('storage table detail emphasizes physical storage and active files', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  await page.route('**/profile/avatars/**', (route) => route.fulfill({ status: 204 }))
  const consoleErrors: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-record-table'))

    const state = await page.evaluate(async () => {
      const { mergePatch } = await import('/static/vendor/datastar-1.0.2.js?v=dev') as any
      mergePatch({ page: {
        kind: 'admin',
        title: 'Storage table',
        active: 'storage-detail',
        headerTitle: 'orders',
        headerDetail: 'Physical storage and active data files.',
        metrics: [
          { label: 'Data size', value: '12 MiB' },
          { label: 'Active files', value: '1' },
          { label: 'Stored rows', value: '1,000' },
          { label: 'Begin snapshot', value: '7' },
        ],
        sections: [
          { title: 'Storage', facts: [
            { label: 'Schema', value: 'model' },
            { label: 'Object type', value: 'table' },
            { label: 'DuckLake path', value: 'model/orders/' },
            { label: 'Table UUID', value: 'table-uuid' },
          ] },
          { title: 'Active files', table: {
            columns: [
              { id: 'path', header: 'File path', kind: 'code', width: '320px' },
              { id: 'format', header: 'Format', width: '90px' },
              { id: 'rows', header: 'Rows', kind: 'number', align: 'right', width: '110px' },
              { id: 'size', header: 'Data size', kind: 'number', align: 'right', width: '110px' },
              { id: 'snapshot', header: 'Begin snapshot', kind: 'number', align: 'right', width: '130px' },
            ],
            rows: [{
              path: 'model/orders/file.parquet',
              format: 'PARQUET',
              rows: { label: '1,000', value: 1000 },
              size: { label: '12 MiB', value: 12582912 },
              snapshot: 7,
            }],
            empty: 'No active data files were found for this table.',
            minWidth: '760px',
          } },
        ],
      } })
      const element = document.querySelector('lv-admin-page') as any
      await element.updateComplete
      const root = element.shadowRoot as ShadowRoot
      const files = root.querySelector('lv-record-table') as TestDomElement
      await files.updateComplete
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        backHref: root.querySelector<HTMLAnchorElement>('.back-link')?.getAttribute('href'),
        sharedLayout: Boolean(root.querySelector('.detail-surface .detail-sections')),
        avatarClass: root.querySelector('.avatar')?.className,
        badges: Array.from(root.querySelectorAll('.identity-badges .badge')).map((badge) => badge.textContent?.trim()),
        metricText: Array.from(root.querySelectorAll('.detail-section[aria-label="Overview"] .fact')).map((metric) => metric.textContent?.replace(/\s+/g, ' ').trim()),
        factText: Array.from(root.querySelectorAll('.detail-section[aria-label="Storage"] .fact')).map((fact) => fact.textContent?.replace(/\s+/g, ' ').trim()),
        cardCount: root.querySelectorAll('.detail-surface .metric').length,
        sectionTitles: Array.from(root.querySelectorAll('.section > h2')).map((heading) => heading.textContent?.trim()),
        fileHeaders: Array.from(files.querySelectorAll('th')).map((header: Element) => header.textContent?.replace(/\s+/g, ' ').trim()),
        fileText: files.textContent?.replace(/\s+/g, ' ').trim(),
      }
    })

    expect(state.title).toBe('orders')
    expect(state.backHref).toBe('/admin/storage')
    expect(state.sharedLayout).toBe(true)
    expect(state.avatarClass).toContain('avatar-plain')
    expect(state.badges).toEqual([])
    expect(state.metricText).toEqual(['Data size 12 MiB', 'Active files 1', 'Stored rows 1,000', 'Begin snapshot 7'])
    expect(state.factText).toEqual(['Schema model', 'Object type table', 'DuckLake path model/orders/', 'Table UUID table-uuid'])
    expect(state.cardCount).toBe(0)
    expect(state.sectionTitles).toEqual(['Overview', 'Storage', 'Active files'])
    expect(state.fileHeaders).toEqual(['File path', 'Format', 'Rows', 'Data size', 'Begin snapshot'])
    expect(state.fileText).toContain('model/orders/file.parquet')
    expect(state.fileText).toContain('PARQUET')
    expect(state.fileText).toContain('12 MiB')
    expect(state.fileText).not.toContain('DuckLake catalog')
    expect(consoleErrors).toEqual([])
  } finally {
    await page.close()
  }
})

test('admin agent route renders prompt editor, tools catalog, and emits save command', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-agent-settings') && customElements.get('lv-agent-prompt-editor') && customElements.get('lv-agent-tools'))

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
            effect: 'read',
            tags: ['analytics', 'visualization'],
            defaults: {},
            inputSchema: {
              type: 'object',
              required: ['dashboardId'],
              properties: {
                dashboardId: { type: 'string', description: 'Dashboard identifier.' },
                mode: { enum: ['summary', 'detail'], description: 'Result detail level.' },
              },
              additionalProperties: false,
            },
            outputSchema: {},
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
      const root = (element.shadowRoot as ShadowRoot)
      const settings = root.querySelector('lv-agent-settings') as any
      await settings.updateComplete
      const settingsRoot = settings.shadowRoot as ShadowRoot
      const editor = settingsRoot.querySelector('lv-agent-prompt-editor') as any
      await editor.updateComplete
      const editorRoot = (editor.shadowRoot as ShadowRoot)
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
      await waitFor(() => Boolean((codeEditor.shadowRoot as ShadowRoot).querySelector('.view-line')))
      const editorFontSize = getComputedStyle((codeEditor.shadowRoot as ShadowRoot).querySelector('.view-line') as any).fontSize
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
      const toolsTab = Array.from(settingsRoot.querySelectorAll<HTMLButtonElement>('[role="tab"]')).find((button) => button.textContent?.trim() === 'Tools')!
      toolsTab.click()
      await settings.updateComplete
      const toolsCatalog = settingsRoot.querySelector('lv-agent-tools') as any
      await toolsCatalog.updateComplete
      return {
        title: root.querySelector('h1')?.textContent?.trim(),
        overview: settingsRoot.querySelector('[aria-label="Agent overview"]')?.textContent?.replace(/\s+/g, ' ').trim(),
        genericMetricCount: root.querySelectorAll('.metrics .metric').length,
        hasEditor: Boolean(editor),
        hasToolsCatalog: Boolean(toolsCatalog),
        hasGenericToolsRecordTable: Boolean(root.querySelector('section[aria-label="Tools"] lv-record-table')),
        toolsCatalogText: (toolsCatalog.shadowRoot as ShadowRoot).textContent,
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
    expect(state.overview).toContain('fake-model')
    expect(state.overview).toContain('Editable')
    expect(state.genericMetricCount).toBe(0)
    expect(state.hasEditor).toBe(true)
    expect(state.hasToolsCatalog).toBe(true)
    expect(state.hasGenericToolsRecordTable).toBe(false)
    expect(state.toolsCatalogText ?? '').toMatch(/query_visual/)
    expect(state.toolsCatalogText ?? '').toMatch(/Data & queries/)
    expect(state.toolsCatalogText ?? '').toMatch(/2 fields · 1 required/)
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-agent-settings') && customElements.get('lv-agent-prompt-editor'))

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
      const settings = (element.shadowRoot as ShadowRoot).querySelector('lv-agent-settings') as any
      await settings.updateComplete
      const settingsRoot = settings.shadowRoot as ShadowRoot
      const editor = settingsRoot.querySelector('lv-agent-prompt-editor') as any
      await editor.updateComplete
      const editorRoot = (editor.shadowRoot as ShadowRoot)
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
        notice: settingsRoot.querySelector('.notice')?.textContent?.replace(/\s+/g, ' ').trim(),
        command,
      }
    })

    expect(state.codeEditorDisabled).toBe(true)
    expect(state.notice).toContain('Deployment managed.')
    expect(state.hasSaveButton).toBe(false)
    expect(state.status).toBe('Read-only')
    expect(state.command).toBeNull()
  } finally {
    await page.close()
  }
})

test('admin agent tools use the shared list and a detail drawer for schemas', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-agent-tools'))

    const state = await page.evaluate(async () => {
      const element = document.createElement('lv-agent-tools') as any
      element.tools = [{
        name: 'query_visual',
        description: 'Query visual data.',
        effect: 'read',
        tags: ['analytics', 'visualization'],
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
        tags: ['documentation'],
        inputSchema: { type: 'object', additionalProperties: false },
      }, {
        name: 'unsupported_input',
        description: 'Composition schema.',
        tags: ['catalog'],
        inputSchema: { oneOf: [{ type: 'string' }, { type: 'number' }] },
      }]
      document.body.append(element)
      await element.updateComplete
      const root = (element.shadowRoot as ShadowRoot)
      const list = root.querySelector('lv-entity-list') as HTMLElement & { updateComplete: Promise<unknown> }
      await list.updateComplete
      const firstText = root.textContent ?? ''
      const listRows = Array.from(list.querySelectorAll<HTMLElement>('.entity-list-table-row'))
      const toolNames = listRows.map((row) => row.querySelector('.entity-list-title')?.textContent?.trim())
      const listText = list.textContent ?? ''
      const groupLabels = Array.from(list.querySelectorAll('.entity-list-group-label')).map((label) => label.textContent?.trim())
      const queryRow = listRows.find((row) => row.textContent?.includes('query_visual'))!
      const queryCells = Array.from(queryRow.querySelectorAll('th, td')).map((cell) => cell.textContent?.replace(/\s+/g, ' ').trim())
      queryRow.click()
      await element.updateComplete
      const drawer = root.querySelector('lv-drawer') as any
      const firstRows = Array.from(root.querySelectorAll('.fields tbody tr')).map((row) => Array.from(row.querySelectorAll('td')).map((cell) => cell.textContent?.trim()))
      const detailFacts = Array.from(root.querySelectorAll('.fact')).map((item) => [item.querySelector('dt')?.textContent?.trim(), item.querySelector('dd')?.textContent?.trim()])

      const jsonButton = root.querySelector<HTMLButtonElement>('.tabs button:nth-child(2)')!
      jsonButton.click()
      await element.updateComplete
      const jsonText = root.querySelector('.json')?.textContent ?? ''

      const outputButton = root.querySelector<HTMLButtonElement>('.tabs button:nth-child(3)')!
      outputButton.click()
      await element.updateComplete
      const outputText = root.querySelector('.json')?.textContent ?? ''

      const noInputRow = Array.from(list.querySelectorAll<HTMLElement>('.entity-list-table-row')).find((row) => row.textContent?.includes('no_input'))!
      noInputRow.click()
      await element.updateComplete
      const noInputText = root.textContent ?? ''

      const unsupportedRow = Array.from(list.querySelectorAll<HTMLElement>('.entity-list-table-row')).find((row) => row.textContent?.includes('unsupported_input'))!
      unsupportedRow.click()
      await element.updateComplete
      const unsupportedText = root.textContent ?? ''

      const search = list.querySelector<HTMLInputElement>('input[type="search"]')!
      search.value = 'filters.dateRange.start'
      search.dispatchEvent(new InputEvent('input', { bubbles: true, composed: true }))
      await list.updateComplete
      const searchRows = Array.from(list.querySelectorAll('.entity-list-table-row')).map((row) => row.querySelector('.entity-list-title')?.textContent?.trim())
      ;(drawer.shadowRoot as ShadowRoot).querySelector<HTMLButtonElement>('.close')?.click()
      await element.updateComplete
      return {
        firstText,
        hasSharedList: Boolean(list),
        toolNames,
        groupLabels,
        queryCells,
        hasRowIcons: Boolean(list.querySelector('.entity-list-icon')),
        listText,
        drawerOpen: Boolean(drawer),
        drawerModal: drawer.modal,
        firstRows,
        detailFacts,
        jsonText,
        outputText,
        noInputText,
        unsupportedText,
        searchRows,
        drawerClosed: !root.querySelector('lv-drawer'),
      }
    })

    expect(state.firstText).toMatch(/query_visual/)
    expect(state.hasSharedList).toBe(true)
    expect(state.toolNames).toEqual(['unsupported_input', 'query_visual', 'no_input'])
    expect(state.groupLabels).toEqual(['Catalog', 'Data & queries', 'Documentation'])
    expect(state.queryCells).toEqual(['query_visual', 'Query visual data.', 'Read-only', '13 fields · 6 required'])
    expect(state.hasRowIcons).toBe(false)
    expect(state.listText).toMatch(/Query visual data/)
    expect(state.drawerOpen).toBe(true)
    expect(state.drawerModal).toBe(false)
    expect(state.detailFacts).toEqual([
      ['Impact', 'Read-only'],
      ['Category', 'Data & queries'],
      ['Required inputs', '6'],
      ['Input', 'dashboardId, filters.dateRange.start, filters.dateRange.end +10'],
      ['Defaults', 'mode=summary'],
    ])
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
    expect(state.drawerClosed).toBe(true)
  } finally {
    await page.close()
  }
})

test('agent prompt editor seeds edit mode from value attribute', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
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
      const root = (element.shadowRoot as ShadowRoot)
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
  const page = await fixture.browser.newPage({ viewport: { width: 1280, height: 820 } })
  try {
    await page.goto(fixture.baseURL)
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
      const root = (element.shadowRoot as ShadowRoot)
      const markdownView = root.querySelector('lv-markdown-view') as any
      await markdownView.updateComplete
      const h1 = (markdownView.shadowRoot as ShadowRoot).querySelector('h1') as any
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
