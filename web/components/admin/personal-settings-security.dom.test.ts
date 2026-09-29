import { afterAll, beforeAll, expect, test } from 'bun:test'
import { startAdminPageTestFixture, stopAdminPageTestFixture, type AdminPageTestFixture } from './admin-page.fixture.test'

let fixture: AdminPageTestFixture

beforeAll(async () => {
  fixture = await startAdminPageTestFixture()
})

afterAll(async () => {
  if (fixture) await stopAdminPageTestFixture(fixture)
}, 15_000)

test('security settings use a unified session list, focused password dialog, and confirmed revocation', async () => {
  const page = await fixture.browser.newPage({ viewport: { width: 1200, height: 820 } })
  try {
    await page.addInitScript(() => { Object.defineProperty(navigator, 'brave', { value: { isBrave: async () => true } }) })
    await page.goto(fixture.baseURL)
    await page.waitForFunction(() => customElements.get('lv-admin-page') && customElements.get('lv-personal-settings'))
    await page.keyboard.press('Tab')
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
            { id: 'session-other', kind: 'desktop', clientLabel: 'LeapView Desktop', current: false, createdAt: '2026-09-16T08:00:00Z', lastSeenAt: '2026-09-17T07:00:00Z', expiresAt: '2026-09-18T08:00:00Z', absoluteExpiresAt: '2026-10-17T08:00:00Z', revokedAt: '' },
            { id: 'session-firefox', kind: 'browser', clientLabel: 'Firefox on Windows', current: false, createdAt: '2026-09-15T08:00:00Z', lastSeenAt: '2026-09-16T07:00:00Z', expiresAt: '2026-09-17T08:00:00Z', absoluteExpiresAt: '2026-10-15T08:00:00Z', revokedAt: '' },
            { id: 'session-safari', kind: 'browser', clientLabel: 'Safari on macOS', current: false, createdAt: '2026-09-14T08:00:00Z', lastSeenAt: '2026-09-15T07:00:00Z', expiresAt: '2026-09-16T08:00:00Z', absoluteExpiresAt: '2026-10-14T08:00:00Z', revokedAt: '' },
            { id: 'session-edge', kind: 'browser', clientLabel: 'Edge on Windows', current: false, createdAt: '2026-09-13T08:00:00Z', lastSeenAt: '2026-09-14T07:00:00Z', expiresAt: '2026-09-15T08:00:00Z', absoluteExpiresAt: '2026-10-13T08:00:00Z', revokedAt: '' },
            { id: 'session-current', kind: 'browser', clientLabel: 'Chrome on Linux', current: true, createdAt: '2026-09-17T08:00:00Z', lastSeenAt: '2026-09-17T09:00:00Z', expiresAt: '2026-09-18T08:00:00Z', absoluteExpiresAt: '2026-10-17T08:00:00Z', revokedAt: '' },
          ],
          authoringSessions: [
            { id: 'authoring-1', kind: 'cli', clientId: 'LeapView CLI', targetId: 'target-1', projectId: 'sales', permissionProfile: 'leapview.permissions/v1', permissions: [{ action: 'dashboard.read', profile: 'leapview.permissions/v1', target: { scope: 'resource', projectId: 'sales', resourceKind: 'dashboard', resourceId: 'target-1' } }], createdAt: '2026-09-15T08:00:00Z', lastUsedAt: '2026-09-17T06:00:00Z', expiresAt: '2026-09-24T08:00:00Z', revokedAt: '' },
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
      const otherDevice = Array.from(root.querySelectorAll<HTMLButtonElement>('.security-session-device')).find((button) => button.textContent?.includes('LeapView Desktop'))!
      const otherRow = otherDevice.closest('tr')!
      const deviceColumnGap = getComputedStyle(otherDevice).columnGap
      otherDevice.click()
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
      const currentDevice = root.querySelector<HTMLButtonElement>('.security-session-table tr.is-current .security-session-device')!
      currentDevice.focus()
      return {
        headings: sections.map((section) => section.querySelector('h2')?.textContent?.trim()),
        mainClass: (admin.shadowRoot as ShadowRoot).querySelector('.main')?.className,
        sessionTableCount: sessionsSection.querySelectorAll('.security-session-table').length,
        sessionHeaders: Array.from(sessionsSection.querySelectorAll('thead th')).map((header) => header.textContent?.trim()),
        sessionRows: sessionsSection.querySelectorAll('tbody tr').length,
        firstSessionIsCurrent: sessionsSection.querySelector('tbody tr:first-child')?.classList.contains('is-current'),
        secondSessionIsDesktop: sessionsSection.querySelector('tbody tr:nth-child(2)')?.textContent?.includes('LeapView Desktop'),
        currentBadge: root.querySelector('.security-badge')?.textContent?.trim(),
        currentLabel: root.querySelector('.security-session-table tr.is-current .security-session-device')?.getAttribute('aria-label'),
        currentRowBackground: getComputedStyle(root.querySelector('.security-session-table tr.is-current')!).backgroundColor,
        deviceFocusVisible: currentDevice.matches(':focus-visible'),
        deviceFocusOutline: getComputedStyle(currentDevice).outlineStyle,
        deviceFocusOutlineWidth: getComputedStyle(currentDevice).outlineWidth,
        deviceFocusBackground: getComputedStyle(currentDevice).backgroundColor,
        currentDeviceIconColor: getComputedStyle(currentDevice.querySelector('.security-session-icon')!).color,
        currentAction: root.querySelector('.security-session-table tr.is-current .session-action')?.textContent?.trim(),
        sessionActions: [root.querySelector('[data-logout-all]')?.textContent?.trim(), otherRow.querySelector('.session-action')?.textContent?.trim()],
        deviceColumnGap,
        authoringText: Array.from(root.querySelectorAll('.security-session-table tbody tr')).find((row) => row.textContent?.includes('LeapView CLI'))?.textContent?.replace(/\s+/g, ' ').trim(),
        authoringKind: Array.from(root.querySelectorAll('.security-session-table tbody tr')).find((row) => row.textContent?.includes('LeapView CLI'))?.querySelector('.security-session-kind')?.textContent?.trim(),
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
    await page.locator('.security-session-table tr.is-current .security-session-device').hover()
    const hover = await page.evaluate(() => {
      const personal = (document.querySelector('lv-admin-page')!.shadowRoot as ShadowRoot).querySelector('lv-personal-settings')!
      const device = (personal.shadowRoot as ShadowRoot).querySelector<HTMLElement>('.security-session-table tr.is-current .security-session-device')!
      const icon = device.querySelector<HTMLElement>('.security-session-icon')!
      return {
        deviceBackground: getComputedStyle(device).backgroundColor,
        iconColor: getComputedStyle(icon).color,
      }
    })
    expect(state.headings).toEqual(['Password', 'Active sessions'])
    expect(state.mainClass).toContain('main-security')
    expect(state.sessionTableCount).toBe(1)
    expect(state.sessionHeaders).toEqual(['Device', 'Access', 'Created', 'Updated', ''])
    expect(state.sessionRows).toBe(6)
    expect(state.firstSessionIsCurrent).toBe(true)
    expect(state.secondSessionIsDesktop).toBe(true)
    expect(state.currentBadge).toBe('This device')
    expect(state.currentLabel).toBe('View details for Brave on Linux, this device, current session')
    expect(state.currentRowBackground).toBe('rgba(0, 0, 0, 0)')
    expect(state.deviceFocusVisible).toBe(true)
    expect(state.deviceFocusOutline).toBe('solid')
    expect(state.deviceFocusOutlineWidth).toBe('2px')
    expect(state.deviceFocusBackground).toBe('rgba(0, 0, 0, 0)')
    expect(hover.deviceBackground).toBe('rgba(0, 0, 0, 0)')
    expect(hover.iconColor).not.toBe(state.currentDeviceIconColor)
    expect(state.currentAction).toBe('Sign out')
    expect(state.sessionActions).toEqual(['Log out all', 'Revoke'])
    expect(state.deviceColumnGap).toBe('16px')
    expect(state.authoringText).toContain('LeapView CLI')
    expect(state.authoringKind).toBe('CLI')
    expect(state.authoringText).toContain('sales')
    expect(state.authoringText).toContain('dashboard read')
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

    await page.setViewportSize({ width: 390, height: 844 })
    const mobile = await page.evaluate(() => {
      const personal = (document.querySelector('lv-admin-page')!.shadowRoot as ShadowRoot).querySelector('lv-personal-settings')!
      const root = personal.shadowRoot as ShadowRoot
      const wrap = root.querySelector<HTMLElement>('.security-session-table-wrap')!
      const table = wrap.querySelector<HTMLTableElement>('.security-session-table')!
      const device = root.querySelector<HTMLElement>('.security-session-table tr.is-current .security-session-device')!
      const action = root.querySelector<HTMLElement>('.security-session-table tr.is-current .session-action')!
      const lastAction = table.querySelector<HTMLElement>('tbody tr:last-child .session-action')!
      const launcher = document.createElement('button')
      launcher.dataset.testFixedLauncher = ''
      launcher.style.cssText = 'position:fixed;right:16px;bottom:16px;width:38px;height:38px;z-index:99999'
      document.body.append(launcher)
      window.scrollTo(0, document.documentElement.scrollHeight)
      const launcherTop = launcher.getBoundingClientRect().top
      const lastActionBottom = lastAction.getBoundingClientRect().bottom
      return {
        visibleHeaders: Array.from(table.querySelectorAll('thead th')).filter((cell) => getComputedStyle(cell).display !== 'none').map((cell) => cell.textContent?.trim()),
        wrapWidth: wrap.clientWidth,
        wrapScrollWidth: wrap.scrollWidth,
        deviceRight: Math.round(device.getBoundingClientRect().right),
        actionRight: Math.round(action.getBoundingClientRect().right),
        currentRowBackground: getComputedStyle(root.querySelector('.security-session-table tr.is-current')!).backgroundColor,
        actionHeight: Math.round(action.getBoundingClientRect().height),
        scrollable: document.documentElement.scrollHeight > window.innerHeight,
        lastActionBottom,
        launcherTop,
      }
    })
    expect(mobile.visibleHeaders).toEqual(['Device', ''])
    expect(mobile.wrapScrollWidth).toBeLessThanOrEqual(mobile.wrapWidth)
    expect(mobile.deviceRight).toBeLessThanOrEqual(mobile.actionRight)
    expect(mobile.currentRowBackground).toBe('rgba(0, 0, 0, 0)')
    expect(mobile.actionHeight).toBeGreaterThanOrEqual(32)
    expect(mobile.scrollable).toBe(true)
    expect(mobile.lastActionBottom).toBeLessThanOrEqual(mobile.launcherTop)
  } finally {
    await page.close()
  }
})
