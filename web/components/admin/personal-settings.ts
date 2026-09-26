import { LitElement, html, nothing } from 'lit'
import { property, query, state } from 'lit/decorators.js'
import { ArrowLeft, CalendarDays, Camera, Check, ChevronDown, KeyRound, Trash2, X } from 'lucide'
import type {
  PersonalCapabilityOptionSignal,
  PersonalPermissionPairSignal,
  PersonalSettingsSignal,
  PersonalTokenSignal,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure } from '../shared/command-failure'
import { emptyStateStyles, renderEmptyState } from '../shared/empty-state'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/one-time-secret'
import '../shared/select-menu'
import '../shared/entity-list'
import type { EntityListColumn, EntityListItem } from '../shared/entity-list'
import type { SelectMenu } from '../shared/select-menu'
import { renderSettingsActions, renderSettingsRow, renderSettingsSection, settingsLayoutStyles } from '../shared/settings-layout'
import { submitAuthForm } from '../shared/auth-form'
import { settingsFieldStyles } from '../shared/settings-field-styles'
import { avatarResponseError } from './avatar-response'
import { formatDate, humanizeSessionKind, sessionFact } from './personal-settings-format'
import { permissionPairKey, tokenPermissionPolicies, uniquePermissionPairs } from './personal-settings-permissions'
import type { TokenPermissionPolicy } from './personal-settings-permissions'
import { renderAuthoringSessionRow, renderBrowserSessionRow, type PendingSessionRevocation, type SelectedSession } from './personal-settings-session-rows'
import { personalSettingsStyles } from './personal-settings.styles'
import './personal-settings-token-permission-picker'
import '../shared/drawer'
import '../shared/user-avatar'
const emptySettings: PersonalSettingsSignal = {
  active: 'profile',
  profile: { id: '', email: '', displayName: '', theme: 'system', identitySource: '', canEditDisplayName: false, hasLocalPassword: false },
  security: { localPasswordEnabled: false, sessions: [], authoringSessions: [] },
  tokens: { items: [], capabilities: [], permissionOptionsReady: false },
}

type ThemeOption = {
  value: string
  label: string
  group: 'Automatic' | 'Standard' | 'Accessibility'
  tone: 'system' | 'light' | 'dark'
}

type TokenExpirationPreset = '7' | '30' | '60' | '90' | 'custom'


const systemThemeOption: ThemeOption = { value: 'system', label: 'System', group: 'Automatic', tone: 'system' }

const themeOptions: readonly ThemeOption[] = [
  systemThemeOption,
  { value: 'light', label: 'Light default', group: 'Standard', tone: 'light' },
  { value: 'dark', label: 'Dark default', group: 'Standard', tone: 'dark' },
  { value: 'dark_dimmed', label: 'Soft dark', group: 'Standard', tone: 'dark' },
  { value: 'light_colorblind', label: 'Light protanopia and deuteranopia', group: 'Accessibility', tone: 'light' },
  { value: 'dark_colorblind', label: 'Dark protanopia and deuteranopia', group: 'Accessibility', tone: 'dark' },
  { value: 'light_tritanopia', label: 'Light tritanopia', group: 'Accessibility', tone: 'light' },
  { value: 'dark_tritanopia', label: 'Dark tritanopia', group: 'Accessibility', tone: 'dark' },
]

const themeGroups: readonly ThemeOption['group'][] = ['Automatic', 'Standard', 'Accessibility']

const tokenListColumns: EntityListColumn[] = [
  { id: 'name', label: 'Token', width: '16%' },
  { id: 'description', label: 'Description', width: '15%' },
  { id: 'permissions', label: 'Permissions', width: '12%' },
  { id: 'lastUsed', label: 'Last used', width: '13%', render: 'datetime' },
  { id: 'created', label: 'Created', width: '13%', render: 'datetime' },
  { id: 'modified', label: 'Last modified', width: '13%', render: 'datetime' },
  { id: 'status', label: 'Status', width: '12%', render: 'status' },
  { id: 'actions', label: 'Actions', width: '6%', render: 'actions', align: 'center' },
]

function tokenStatus(token: PersonalTokenSignal): string {
  return token.revokedAt ? 'Revoked' : token.expiresAt && Date.parse(token.expiresAt) <= Date.now() ? 'Expired' : 'Active'
}

function tokenListItem(token: PersonalTokenSignal): EntityListItem {
  const count = token.permissionProfile ? token.permissions.length : token.capabilities.length
  const modifiedAt = token.revokedAt || token.modifiedAt || token.createdAt
  return {
    id: token.id,
    title: token.name,
    href: `/admin/api-tokens/${encodeURIComponent(token.id)}/edit`,
    icon: 'key',
    actions: token.revokedAt ? [] : [{ label: `Delete ${token.name}`, action: 'delete', icon: 'trash' }],
    columns: {
      description: token.description || '—',
      permissions: count ? `${count} ${count === 1 ? 'permission' : 'permissions'}` : 'No permissions',
      lastUsed: token.lastUsedAt ? formatDateOnly(token.lastUsedAt) : 'Never',
      created: formatDateOnly(token.createdAt),
      modified: formatDateOnly(modifiedAt),
      status: tokenStatus(token),
    },
    columnTitles: {
      lastUsed: token.lastUsedAt ? formatDate(token.lastUsedAt) : '',
      created: formatDate(token.createdAt),
      modified: formatDate(modifiedAt),
    },
    sortValues: {
      lastUsed: token.lastUsedAt || '',
      created: token.createdAt,
      modified: modifiedAt,
    },
  }
}

class LeapViewPersonalSettings extends DatastarLit(LitElement) {
  @state() private profileName = ''
  @state() private profileTitle = ''
  @state() private profileUsername = ''
  @state() private currentPassword = ''
  @state() private newPassword = ''
  @state() private tokenName = ''
  @state() private tokenDescription = ''
  @state() private tokenSelectedPermissions: PersonalPermissionPairSignal[] = []
  @state() private tokenExpirationPreset: TokenExpirationPreset = '30'
  @state() private tokenCustomExpiration = ''
  @state() private tokenPendingDeletion: PersonalTokenSignal | null = null
  @state() private tokenPendingRotation: PersonalTokenSignal | null = null
  @state() private pendingSessionRevocation: PendingSessionRevocation | null = null
  @state() private selectedSession: SelectedSession | null = null
  @state() private passwordDialogOpen = false
  @state() private logoutAllDialogOpen = false
  @property({ attribute: 'token-view' }) tokenView: 'list' | 'create' | 'edit' = 'list'
  @state() private tokenConfirmationOpen = false
  @state() private tokenCreatePending = false
  @state() private tokenEditPending = false
  @state() private tokenRotatePending = false
  @state() private tokenEditUnavailable = false
  private tokenEditLoadedId = ''
  private tokenEditExpectedModifiedAt = ''
  private tokenEditOriginalExpiresAt = ''
  private tokenEditExpirationTouched = false
  @state() private tokenPermissionMenuOpen = false
  @state() private tokenPermissionsIncomplete = false
  @state() private message = ''
  @state() private error = ''
  @state() private avatarMenuOpen = false
  @state() private themeMenuOpen = false
  @state() private selectedTheme = ''
  @state() private avatarBusy = false
  @query('.avatar-trigger') private avatarTrigger?: HTMLButtonElement
  @query('.avatar-input') private avatarInput?: HTMLInputElement
  @query('.permission-trigger') private permissionTrigger?: HTMLButtonElement
  @query('#token-expiration-preset') private expirationSelect?: SelectMenu
  @query('.theme-trigger') private themeTrigger?: HTMLButtonElement
  @query('[data-token-confirm-dialog]') private tokenConfirmationDialog?: HTMLDialogElement
  @query('[data-token-delete-dialog]') private tokenDeleteDialog?: HTMLDialogElement
  @query('[data-token-rotate-dialog]') private tokenRotateDialog?: HTMLDialogElement
  @query('[data-session-revoke-dialog]') private sessionRevokeDialog?: HTMLDialogElement
  @query('[data-password-dialog]') private passwordDialog?: HTMLDialogElement
  @query('[data-logout-all-dialog]') private logoutAllDialog?: HTMLDialogElement
  private handledNewToken = ''
  private observedDisplayName = ''
  private observedProfileID = ''
  private observedTheme = ''

  static styles = [settingsFieldStyles, settingsLayoutStyles, emptyStateStyles, personalSettingsStyles]

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('pointerdown', this.handleDocumentPointerDown)
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
    window.addEventListener('keydown', this.handleWindowKeydown)
    window.addEventListener('resize', this.fitPermissionMenu)
    window.addEventListener('scroll', this.fitPermissionMenu, true)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('pointerdown', this.handleDocumentPointerDown)
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    window.removeEventListener('keydown', this.handleWindowKeydown)
    window.removeEventListener('resize', this.fitPermissionMenu)
    window.removeEventListener('scroll', this.fitPermissionMenu, true)
    super.disconnectedCallback()
  }

  get settings(): PersonalSettingsSignal {
    return this.signal<PersonalSettingsSignal>('personalSettings', emptySettings)
  }

  override updated(): void {
    const settings = this.settings
    if (this.tokenView === 'edit') {
      const tokenID = window.location.pathname.match(/^\/admin\/api-tokens\/([^/]+)\/edit$/)?.[1] ?? ''
      const token = settings.tokens.items.find((item) => item.id === tokenID)
      if (token && settings.tokens.permissionOptionsReady && token.id !== this.tokenEditLoadedId) {
        this.tokenEditLoadedId = token.id
        this.tokenEditExpectedModifiedAt = token.modifiedAt || token.createdAt
        this.tokenEditOriginalExpiresAt = token.expiresAt
        this.tokenEditExpirationTouched = false
        this.tokenName = token.name
        this.tokenDescription = token.description
        this.tokenExpirationPreset = 'custom'
        this.tokenCustomExpiration = token.expiresAt.slice(0, 10)
        this.tokenSelectedPermissions = this.authorizedSelectedPermissions(token.permissions, settings.tokens.capabilities)
        this.tokenEditUnavailable = this.tokenSelectedPermissions.length !== uniquePermissionPairs(token.permissions).length
      } else if (token && this.tokenEditPending && token.modifiedAt && token.modifiedAt !== this.tokenEditExpectedModifiedAt) {
        window.location.assign('/admin/api-tokens')
        return
      }
    } else {
      this.reconcileTokenPermissionState(settings.tokens.capabilities)
    }
    if (settings.profile.id && settings.profile.id !== this.observedProfileID) {
      this.observedProfileID = settings.profile.id
      this.profileTitle = ''
      this.profileUsername = usernameFromEmail(settings.profile.email)
    }
    const displayName = settings.profile.displayName
    if (displayName !== this.observedDisplayName) {
      this.observedDisplayName = displayName
      this.profileName = displayName
    }
    const theme = settings.profile.theme || 'system'
    if (theme !== this.observedTheme) {
      this.observedTheme = theme
      this.selectedTheme = theme
    }
    const newToken = settings.tokens.newToken ?? ''
    if (newToken && newToken !== this.handledNewToken) {
      const navigateToTokenList = this.tokenView === 'create' || this.tokenView === 'edit'
      this.handledNewToken = newToken
      this.tokenCreatePending = false
      this.tokenRotatePending = false
      this.tokenView = 'list'
      this.tokenConfirmationOpen = false
      this.tokenName = ''
      this.tokenDescription = ''
      this.tokenSelectedPermissions = []
      this.tokenPermissionsIncomplete = false
      this.tokenExpirationPreset = '30'
      this.tokenCustomExpiration = ''
      this.closeExpirationMenu()
      this.closePermissionMenu()
      if (navigateToTokenList) window.history.replaceState(window.history.state, '', '/admin/api-tokens')
    }
    const confirmation = this.tokenConfirmationDialog
    if (this.tokenConfirmationOpen && confirmation && !confirmation.open) confirmation.showModal()
    const deletion = this.tokenDeleteDialog
    if (this.tokenPendingDeletion && deletion && !deletion.open) deletion.showModal()
    const rotation = this.tokenRotateDialog
    if (this.tokenPendingRotation && rotation && !rotation.open) rotation.showModal()
    const sessionRevocation = this.sessionRevokeDialog
    if (this.pendingSessionRevocation && sessionRevocation && !sessionRevocation.open) sessionRevocation.showModal()
    const password = this.passwordDialog
    if (this.passwordDialogOpen && password && !password.open) password.showModal()
    const logoutAll = this.logoutAllDialog
    if (this.logoutAllDialogOpen && logoutAll && !logoutAll.open) logoutAll.showModal()
    if (settings.active !== 'security' && this.selectedSession) this.selectedSession = null
  }

  render() {
    const settings = this.settings
    if (!settings.profile.id) return html`<slot></slot>`
    const profileNameDraft = this.observedDisplayName === settings.profile.displayName ? this.profileName : settings.profile.displayName
    const profileNameDirty = profileNameDraft.trim() !== settings.profile.displayName
    const profileNameValid = profileNameDraft.trim().length > 0
    return html`
      <div class="settings-stack" aria-label="Personal settings">
        ${this.renderNotice(settings)}
        ${settings.active === 'profile' ? renderSettingsSection({ label: 'Profile', appearance: 'plain', content: html`
          ${renderSettingsSection({ label: 'Profile details', appearance: 'card', className: 'card profile-card', content: html`
            ${renderSettingsRow({ label: 'Profile picture', layout: 'action', className: 'row profile-row', description: 'Shown across LeapView.', control: html`<div class="avatar-control">
                <button
                  class="avatar-trigger"
                  type="button"
                  aria-label="Manage profile picture"
                  aria-haspopup="menu"
                  aria-expanded=${String(this.avatarMenuOpen)}
                  ?disabled=${this.avatarBusy}
                  @click=${this.toggleAvatarMenu}
                  @keydown=${this.handleAvatarTriggerKeydown}
                >
                  <lv-user-avatar
                    size="small"
                    .name=${settings.profile.displayName || settings.profile.email}
                    .imageUrl=${settings.profile.avatarUrl ?? ''}
                    aria-hidden="true"
                  ></lv-user-avatar>
                </button>
                <input class="avatar-input" type="file" accept="image/png,image/jpeg,image/webp" aria-label="Upload profile picture" tabindex="-1" @change=${this.uploadAvatar}>
                ${this.avatarMenuOpen ? html`
                  <div class="avatar-menu" role="menu" aria-label="Profile picture actions" @keydown=${this.handleAvatarMenuKeydown}>
                    <button class="avatar-menu-item" type="button" role="menuitem" @click=${this.chooseAvatar}>
                      ${lucideIcon(Camera, { size: 16, strokeWidth: 2 })}
                      <span>${settings.profile.avatarUrl ? 'Change avatar' : 'Upload avatar'}</span>
                    </button>
                    ${settings.profile.avatarUrl ? html`
                      <button class="avatar-menu-item danger" type="button" role="menuitem" @click=${this.deleteAvatar}>
                        ${lucideIcon(Trash2, { size: 16, strokeWidth: 2 })}
                        <span>Remove avatar</span>
                      </button>
                    ` : nothing}
                  </div>
                ` : nothing}
              </div>` })}
            ${renderSettingsRow({ label: 'Email', layout: 'action', className: 'row profile-row', description: 'Managed by your identity provider.', control: html`<span class="settings-value profile-email">${settings.profile.email || 'Not set'}</span>` })}
            ${renderSettingsRow({ label: 'Display name', layout: 'action', className: 'row profile-row', controlId: 'personal-display-name', description: 'How your name appears to collaborators.', control: html`<form class="profile-name-form" @submit=${this.saveProfile}>
                ${renderSettingsActions(html`
                  <input class="settings-input" id="personal-display-name" .value=${profileNameDraft} ?disabled=${!settings.profile.canEditDisplayName} @input=${this.onProfileNameInput}>
                  ${profileNameDirty ? html`<button class="settings-button primary" data-profile-save type="submit" ?disabled=${!settings.profile.canEditDisplayName || !profileNameValid}>Save</button>` : nothing}
                `, { className: 'profile-name-control' })}
              </form>` })}
            ${renderSettingsRow({ label: 'Title', layout: 'action', className: 'row profile-row', controlId: 'personal-title', description: 'Your job title or role.', control: html`<input id="personal-title" class="settings-input profile-local-input profile-title-input" maxlength="120" placeholder="Software engineer" .value=${this.profileTitle} @input=${this.onProfileTitleInput}>` })}
            ${renderSettingsRow({ label: 'Username', layout: 'action', className: 'row profile-row', controlId: 'personal-username', description: 'One word, like a nickname or first name.', control: html`<input id="personal-username" class="settings-input profile-local-input profile-username-input" maxlength="64" autocomplete="off" .value=${this.profileUsername} @input=${this.onProfileUsernameInput}>` })}
            ${renderSettingsRow({ label: 'Theme', layout: 'action', className: 'row profile-row', labelId: 'personal-theme-label', description: 'Choose how LeapView appears on your devices.', control: html`${this.renderThemePicker(this.selectedTheme || settings.profile.theme || 'system')}` })}
          ` })}
          ${renderSettingsSection({ label: 'Account', heading: 'Account', description: 'Use this ID when support or administration needs to identify your account.', appearance: 'plain', className: 'account-section', content: html`
            ${renderSettingsSection({ label: 'Account actions', appearance: 'card', className: 'card account-card', content: html`
              ${renderSettingsRow({ label: 'Account ID', layout: 'action', className: 'row account-row', description: 'Your LeapView principal identifier.', control: html`<code class="account-id">${settings.profile.id}</code>` })}
              ${renderSettingsRow({ label: 'Sign out', layout: 'action', className: 'row account-row', description: 'End this browser session and return to sign-in.', control: html`<button type="button" class="settings-button danger" data-sign-out @click=${this.signOutCurrentSession}>Sign out</button>` })}
            ` })}
          ` })}
        ` }) : nothing}
        ${settings.active === 'security' ? this.renderSecurity(settings) : nothing}
        ${settings.active === 'api-tokens' ? this.renderTokens(settings.tokens) : nothing}
      </div>
    `
  }

  private renderThemePicker(theme: string) {
    const selected = themeOption(theme)
    return html`
      <div class="theme-picker">
        <button
          class="theme-trigger"
          type="button"
          aria-haspopup="listbox"
          aria-expanded=${String(this.themeMenuOpen)}
          aria-labelledby="personal-theme-label personal-theme-value"
          aria-controls="personal-theme-listbox"
          @click=${this.toggleThemeMenu}
          @keydown=${this.handleThemeTriggerKeydown}
        >
          ${this.renderThemePreview(selected)}
          <span class="theme-trigger-label" id="personal-theme-value">${selected.label}</span>
          ${lucideIcon(ChevronDown, { size: 16, strokeWidth: 2 })}
        </button>
        ${this.themeMenuOpen ? html`
          <div id="personal-theme-listbox" class="theme-menu" role="listbox" aria-labelledby="personal-theme-label" @keydown=${this.handleThemeOptionKeydown}>
            ${themeGroups.map((group) => html`
              <div class="theme-group" role="group" aria-label=${group}>
                <div class="theme-group-label" aria-hidden="true">${group}</div>
                ${themeOptions.filter((option) => option.group === group).map((option) => html`
                  <button
                    class="theme-option"
                    type="button"
                    role="option"
                    aria-selected=${String(option.value === selected.value)}
                    data-theme=${option.value}
                    tabindex="-1"
                    @click=${() => this.chooseTheme(option.value)}
                  >
                    ${this.renderThemePreview(option)}
                    <span class="theme-option-label">${option.label}</span>
                    <span class="theme-check" aria-hidden="true">${option.value === selected.value ? lucideIcon(Check, { size: 16, strokeWidth: 2 }) : nothing}</span>
                  </button>
                `)}
              </div>
            `)}
          </div>
        ` : nothing}
      </div>
    `
  }

  private renderThemePreview(option: ThemeOption) {
    return html`
      <span class="theme-preview" data-tone=${option.tone} aria-hidden="true">
        <span class="theme-preview-dot"></span>
        <span>Aa</span>
      </span>
    `
  }

  private renderNotice(settings: PersonalSettingsSignal) {
    if (this.error && !this.tokenConfirmationOpen) return html`<p class="error" role="alert">${this.error}</p>`
    if (this.message) return html`<p class="notice" role="status">${this.message}</p>`
    return nothing
  }

  private renderSecurity(settings: PersonalSettingsSignal) {
    const browserSessions = settings.security.sessions.filter((session) => !session.revokedAt)
    const authoringSessions = settings.security.authoringSessions.filter((session) => !session.revokedAt)
    const canChangePassword = settings.security.localPasswordEnabled && settings.profile.hasLocalPassword
    return html`
      <section class="security-page" aria-label="Security and sessions">
        <section class="security-section" aria-label="Password">
          <div class="security-section-heading-copy"><h2>Password</h2></div>
          <div class="security-password-row">
            <div class="security-password-copy">
              <span class="security-password-state">${canChangePassword ? 'Local password · Configured' : 'Managed by identity provider'}</span>
              <span class="settings-description">${canChangePassword ? 'Changing your password signs out browser, desktop, CLI, and authoring sessions.' : 'Your identity provider controls password changes for this account.'}</span>
            </div>
            ${canChangePassword ? html`<button type="button" @click=${this.openPasswordDialog}>Change password</button>` : nothing}
          </div>
        </section>
        <section class="security-section" aria-label="Active sessions">
          <div class="security-section-heading">
            <div class="security-section-heading-copy"><h2>Active sessions</h2><p class="settings-description">Review devices and scoped clients that can access your account.</p></div>
            ${browserSessions.length ? html`<button type="button" data-logout-all @click=${this.openLogoutAllDialog}>Log out all</button>` : nothing}
          </div>
          <div class="security-session-table-wrap">
            <table class="security-session-table">
              <thead><tr><th>Device</th><th>Access</th><th>Created</th><th>Updated</th><th aria-label="Actions"></th></tr></thead>
              <tbody>
                ${browserSessions.map((session) => renderBrowserSessionRow(session, (selected) => { this.selectedSession = selected }, (pending) => this.requestSessionRevocation(pending)))}
                ${authoringSessions.map((session) => renderAuthoringSessionRow(session, (selected) => { this.selectedSession = selected }, (pending) => this.requestSessionRevocation(pending)))}
                ${browserSessions.length === 0 && authoringSessions.length === 0 ? html`<tr><td class="security-empty" colspan="5">No active sessions.</td></tr>` : nothing}
              </tbody>
            </table>
          </div>
        </section>
        ${this.passwordDialogOpen ? this.renderPasswordDialog() : nothing}
        ${this.logoutAllDialogOpen ? this.renderLogoutAllDialog() : nothing}
        ${this.renderSessionDrawer(settings)}
        ${this.pendingSessionRevocation ? this.renderSessionRevokeConfirmation(this.pendingSessionRevocation) : nothing}
      </section>
    `
  }

  private renderPasswordDialog() {
    return html`<dialog data-password-dialog aria-labelledby="change-password-title" @cancel=${this.closePasswordDialog} @click=${this.closePasswordDialogOnBackdrop}>
      <section class="token-confirm">
        <header class="token-confirm-header"><h2 id="change-password-title">Change password</h2><button class="token-confirm-close" type="button" aria-label="Close" @click=${this.closePasswordDialog}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button></header>
        <form @submit=${this.changePassword}>
          <div class="token-confirm-body">
            <div class="password-fields">
              <label class="password-field"><span class="settings-label">Current password</span><input name="currentPassword" type="password" autocomplete="current-password" maxlength="1024" .value=${this.currentPassword} @input=${this.onCurrentPasswordInput} required></label>
              <label class="password-field"><span class="settings-label">New password</span><input name="newPassword" type="password" autocomplete="new-password" minlength="12" maxlength="1024" .value=${this.newPassword} @input=${this.onNewPasswordInput} required><span class="settings-description">Use at least 12 characters and do not reuse this password elsewhere.</span></label>
            </div>
            <p class="settings-description">Saving signs out all browser, desktop, CLI, and authoring sessions.</p>
          </div>
          <div class="token-confirm-actions"><button type="button" @click=${this.closePasswordDialog}>Cancel</button><button class="primary" type="submit">Change password</button></div>
        </form>
      </section>
    </dialog>`
  }

  private renderLogoutAllDialog() {
    return html`<dialog data-logout-all-dialog aria-labelledby="logout-all-title" @cancel=${this.closeLogoutAllDialog} @click=${this.closeLogoutAllOnBackdrop}>
      <section class="token-confirm">
        <header class="token-confirm-header"><h2 id="logout-all-title">Log out all browser and desktop sessions?</h2><button class="token-confirm-close" type="button" aria-label="Close" @click=${this.closeLogoutAllDialog}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button></header>
        <div class="token-delete-warning"><p>This ends every active browser and desktop session, including this one. CLI and authoring sessions stay available until you revoke them separately.</p></div>
        <div class="token-delete-actions"><button class="danger" type="button" @click=${this.confirmLogoutAll}>Log out all browser and desktop sessions</button></div>
      </section>
    </dialog>`
  }

  private renderSessionDrawer(settings: PersonalSettingsSignal) {
    const selected = this.selectedSession
    if (!selected) return nothing
    if (selected.kind === 'browser') {
      const session = settings.security.sessions.find((candidate) => candidate.id === selected.id && !candidate.revokedAt)
      if (!session) return nothing
      const label = session.clientLabel || humanizeSessionKind(session.kind)
      return html`<lv-drawer open label="Session details" .modal=${false} @lv-drawer-close=${this.closeSessionDrawer}>
        <div slot="title" class="session-drawer-title"><h2>${label}</h2><p>${session.current ? 'Current browser or desktop session' : 'Browser or desktop session'}</p></div>
        <div class="session-drawer-body">
          <section class="session-drawer-section"><h3>Details</h3><dl class="session-drawer-facts">
            ${sessionFact('Status', session.current ? 'This device' : 'Active')}
            ${sessionFact('Client type', humanizeSessionKind(session.kind))}
            ${sessionFact('Last active', formatDate(session.lastSeenAt))}
            ${sessionFact('Created', formatDate(session.createdAt))}
            ${sessionFact('Expires', formatDate(session.expiresAt))}
            ${sessionFact('Absolute expiration', formatDate(session.absoluteExpiresAt))}
            ${sessionFact('Session ID', session.id, true)}
          </dl></section>
          <button type="button" @click=${() => this.requestSessionRevocation({ id: session.id, label, kind: 'browser', current: session.current })}>${session.current ? 'Sign out this session' : 'Revoke this session'}</button>
        </div>
      </lv-drawer>`
    }
    const session = settings.security.authoringSessions.find((candidate) => candidate.id === selected.id && !candidate.revokedAt)
    if (!session) return nothing
    const label = session.clientId || humanizeSessionKind(session.kind)
    return html`<lv-drawer open label="Session details" .modal=${false} @lv-drawer-close=${this.closeSessionDrawer}>
      <div slot="title" class="session-drawer-title"><h2>${label}</h2><p>CLI or authoring session</p></div>
      <div class="session-drawer-body">
        <section class="session-drawer-section"><h3>Details</h3><dl class="session-drawer-facts">
          ${sessionFact('Client type', humanizeSessionKind(session.kind))}
          ${sessionFact('Project', session.projectId || 'All available projects')}
          ${sessionFact('Permissions', session.permissions.map((permission) => permission.action.replaceAll('.', ' ')).join(', ') || 'Scoped access')}
          ${sessionFact('Created', formatDate(session.createdAt))}
          ${sessionFact('Last active', session.lastUsedAt ? formatDate(session.lastUsedAt) : 'Never')}
          ${sessionFact('Expires', formatDate(session.expiresAt))}
          ${sessionFact('Target ID', session.targetId, true)}
          ${sessionFact('Session ID', session.id, true)}
        </dl></section>
        <button type="button" @click=${() => this.requestSessionRevocation({ id: session.id, label, kind: 'authoring' })}>Revoke this session</button>
      </div>
    </lv-drawer>`
  }

  private renderSessionRevokeConfirmation(session: PendingSessionRevocation) {
    return html`<dialog data-session-revoke-dialog aria-labelledby="session-revoke-title" @cancel=${this.cancelSessionRevocation} @click=${this.closeSessionRevocationOnBackdrop}>
      <section class="token-confirm">
        <header class="token-confirm-header"><h2 id="session-revoke-title">${session.current ? 'Sign out this session?' : 'Revoke this session?'}</h2><button class="token-confirm-close" type="button" aria-label="Close" @click=${this.cancelSessionRevocation}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button></header>
        <div class="token-delete-warning"><p>${session.current ? 'You will be signed out on this device and will need to authenticate again.' : `${session.label} will immediately lose access to LeapView.`} You cannot undo this action.</p></div>
        <div class="token-delete-actions"><button class="danger" type="button" @click=${this.confirmSessionRevocation}>${session.current ? 'I understand, sign me out' : 'I understand, revoke this session'}</button></div>
      </section>
    </dialog>`
  }

  private renderTokens(tokens: PersonalSettingsSignal['tokens']) {
    if (this.tokenView === 'list') return this.renderTokenList(tokens)
    const editing = this.tokenView === 'edit'
    const tokenID = editing ? window.location.pathname.match(/^\/admin\/api-tokens\/([^/]+)\/edit$/)?.[1] ?? '' : ''
    const editedToken = editing ? tokens.items.find((item) => item.id === tokenID) : undefined
    if (editing && tokens.permissionOptionsReady && (!editedToken || tokenStatus(editedToken) !== 'Active')) return renderEmptyState({
      icon: lucideIcon(KeyRound, { size: 28, strokeWidth: 1.8 }),
      title: 'Token not available',
      description: 'This token was deleted or cannot be edited.',
    })
    const policies = tokenPermissionPolicies(tokens.capabilities)
    const expirationOptions = tokenExpirationOptions()
    const canCreate = Boolean(this.tokenName.trim() && this.tokenExpirationIsValid() && !this.tokenCreatePending && !this.tokenEditPending && !this.tokenPermissionsIncomplete && !this.tokenEditUnavailable && (!editing || this.tokenEditLoadedId === tokenID))
    return html`
      <section class="token-page" aria-label=${editing ? 'Edit personal access token' : 'Create personal access token'}>
        <div class="token-create-header">
          <a class="token-back" href="/admin/api-tokens" aria-label="Back to personal access tokens">${lucideIcon(ArrowLeft, { size: 16, strokeWidth: 2 })}</a>
          <div class="token-page-heading"><h2>${editing ? 'Edit personal access token' : 'New personal access token'}</h2></div>
        </div>
        <form class="token-form" @submit=${this.requestTokenCreation}>
          <p class="token-page-intro">${editing ? 'Update the token without changing its secret. Changes to access take effect immediately.' : 'Create a scoped token suitable for personal API, CLI, and automation access.'}</p>
          ${this.tokenEditUnavailable ? html`<p class="error" role="alert">This token contains permissions no longer available in your current authority. It cannot be edited here without losing them. Ask an administrator to review access, or create a replacement token.</p>` : nothing}
          <div class="token-details">
            <label class="token-field" for="token-name">
              <span class="settings-label">Token name *</span>
              <input id="token-name" required autocomplete="off" .value=${this.tokenName} @input=${this.onTokenNameInput}>
              <span class="settings-description">A unique name for this token. It may be visible to administrators.</span>
            </label>
            <label class="token-field" for="token-description">
              <span class="settings-label">Description <span class="muted">(optional)</span></span>
              <textarea id="token-description" maxlength="1024" .value=${this.tokenDescription} @input=${this.onTokenDescriptionInput}></textarea>
              <span class="settings-description">Explain what this token will be used for so you can identify it later.</span>
            </label>
            <div class="token-field token-expiration-field">
              <span class="settings-label" id="token-expiration-label">Expiration</span>
              <lv-select-menu
                id="token-expiration-preset"
                label="Expiration"
                .options=${expirationOptions}
                .value=${this.tokenExpirationPreset}
                @lv-select-change=${this.chooseExpirationPreset}
                @lv-select-toggle=${this.handleExpirationMenuToggle}
              >
                <span slot="leading">${lucideIcon(CalendarDays, { size: 16, strokeWidth: 2 })}</span>
              </lv-select-menu>
              ${this.tokenExpirationPreset === 'custom' ? html`<input class="custom-expiration" id="token-custom-expiration" type="date" min=${dateInputValueInDays(editing ? 0 : 1)} max=${editing && this.tokenEditOriginalExpiresAt.slice(0, 10) > dateInputValueInDays(90) ? this.tokenEditOriginalExpiresAt.slice(0, 10) : dateInputValueInDays(90)} .value=${this.tokenCustomExpiration} @input=${this.onTokenCustomExpirationInput} required>` : nothing}
              <span class="settings-description">The token will expire on the selected date. Maximum lifetime is 90 days.</span>
            </div>
          </div>
          <div class="permissions">
            <div class="permissions-heading">
              <h3>Permissions</h3>
              <p class="token-page-intro">Choose only the permissions and resource scopes this token needs.</p>
            </div>
            <div class="permissions-card">
              <lv-personal-token-permission-picker
                .policies=${policies}
                .selectedPermissions=${this.tokenSelectedPermissions}
                .permissionOptionsReady=${tokens.permissionOptionsReady}
                .open=${this.tokenPermissionMenuOpen}
                @lv-personal-token-permission-picker-toggle=${this.togglePermissionMenu}
                @lv-personal-token-permission-picker-close=${this.handlePermissionPickerClose}
                @lv-personal-token-permissions-change=${this.handleTokenPermissionsChange}
                @lv-personal-token-permission-incomplete-change=${this.handleTokenPermissionsIncompleteChange}
              ></lv-personal-token-permission-picker>
            </div>
          </div>
          <div class="actions token-actions"><button class="primary" type="submit" ?disabled=${!canCreate}>${editing ? this.tokenEditPending ? 'Saving…' : 'Save changes' : 'Generate token'}</button>${editing ? html`<button type="button" ?disabled=${this.tokenEditUnavailable || this.tokenEditPending || this.tokenRotatePending} @click=${() => { if (editedToken) this.tokenPendingRotation = editedToken }}>Rotate secret</button>` : nothing}<a class="button-link" href="/admin/api-tokens">Cancel</a></div>
        </form>
        ${this.tokenConfirmationOpen ? this.renderTokenConfirmation() : nothing}
        ${this.tokenPendingRotation ? this.renderTokenRotateConfirmation(this.tokenPendingRotation) : nothing}
      </section>
    `
  }

  private renderTokenList(tokens: PersonalSettingsSignal['tokens']) {
    return html`
      <section class="token-page" aria-label="Personal access tokens">
        <div class="token-page-header">
          <h2>Personal access tokens</h2>
          <a class="button-link primary" href="/admin/api-tokens/new">Generate new token</a>
        </div>
        <p class="token-page-intro">Tokens are scoped credentials for API, CLI, and automation access. Keep them secret and delete any you no longer use.</p>
        ${tokens.newToken && tokens.items[0] ? html`<div class="token-new-secret"><strong>New token: ${tokens.items[0].name}</strong><lv-one-time-secret secret=${tokens.newToken} message="Copy your personal access token now. You won’t be able to see it again." copy-label="Copy personal access token"></lv-one-time-secret></div>` : nothing}
        ${tokens.items.length ? html`<lv-entity-list
          .items=${tokens.items.map(tokenListItem)}
          .columns=${tokenListColumns}
          row-action="open"
          client-filter
          min-width="64rem"
          list-label="Personal access tokens"
          search-placeholder="Search tokens"
          @lv-entity-list-row-action=${(event: CustomEvent<{ action: string, item: EntityListItem }>) => this.handleTokenListAction(event, tokens.items)}
        ></lv-entity-list>` : renderEmptyState({
          icon: lucideIcon(KeyRound, { size: 28, strokeWidth: 1.8 }),
          title: 'No personal access tokens yet',
          description: 'Generate a token when a tool or script needs to access LeapView.',
        })}
        ${this.tokenPendingDeletion ? this.renderTokenDeleteConfirmation(this.tokenPendingDeletion) : nothing}
      </section>
    `
  }

  private renderTokenConfirmation() {
    const policies = tokenPermissionPolicies(this.settings.tokens.capabilities)
    const selectedPolicies = policies.filter((policy) => this.tokenPermissionPolicyOptions(policy).length > 0)
    return html`
      <dialog data-token-confirm-dialog aria-labelledby="token-confirm-title" @cancel=${this.cancelTokenConfirmation} @click=${this.closeTokenConfirmationOnBackdrop}>
        <section class="token-confirm">
          <header class="token-confirm-header">
            <h2 id="token-confirm-title">New personal access token</h2>
            <button class="token-confirm-close" type="button" aria-label="Close token confirmation" @click=${this.cancelTokenConfirmation}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button>
          </header>
          <div class="token-confirm-body">
            <p>Your new personal access token <strong>${this.tokenName.trim()}</strong> will be ready for use immediately.</p>
            <p>It will expire on <strong>${formatLongDate(this.tokenExpirationDate())}</strong>.</p>
            <div class="token-confirm-permissions">
              <span class="settings-label">Permission summary</span>
              ${selectedPolicies.length ? html`<ul>${selectedPolicies.map((policy) => html`<li><strong>${policy.label}</strong><span>${this.tokenPermissionPolicySummary(policy)}</span></li>`)}</ul>` : html`<p class="settings-description">Authentication only — no project or resource authority.</p>`}
            </div>
            ${this.error ? html`<p class="error" role="alert">${this.error}</p>` : nothing}
          </div>
          <div class="token-confirm-actions"><button type="button" @click=${this.cancelTokenConfirmation}>Cancel</button><button class="primary" type="button" ?disabled=${this.tokenCreatePending} @click=${this.createToken}>${this.tokenCreatePending ? 'Generating…' : 'Generate token'}</button></div>
        </section>
      </dialog>
    `
  }

  private renderTokenDeleteConfirmation(token: PersonalTokenSignal) {
    return html`
      <dialog data-token-delete-dialog aria-labelledby="token-delete-title" @cancel=${this.cancelTokenDeletion} @click=${this.closeTokenDeletionOnBackdrop}>
        <section class="token-confirm">
          <header class="token-confirm-header">
            <h2 id="token-delete-title">Are you sure you want to delete this token?</h2>
            <button class="token-confirm-close" type="button" aria-label="Close token deletion confirmation" @click=${this.cancelTokenDeletion}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button>
          </header>
          <div class="token-delete-warning">
            <p>Any applications or scripts using <strong>${token.name}</strong> will no longer be able to access the LeapView API. You cannot undo this action.</p>
          </div>
          <div class="token-delete-actions"><button class="danger" type="button" @click=${this.confirmTokenDeletion}>I understand, delete this token</button></div>
        </section>
      </dialog>
    `
  }

  private renderTokenRotateConfirmation(token: PersonalTokenSignal) {
    return html`<dialog data-token-rotate-dialog aria-labelledby="token-rotate-title" @cancel=${this.cancelTokenRotation} @click=${this.closeTokenRotationOnBackdrop}>
      <section class="token-confirm">
        <header class="token-confirm-header"><h2 id="token-rotate-title">Rotate this token?</h2><button class="token-confirm-close" type="button" aria-label="Close token rotation confirmation" @click=${this.cancelTokenRotation}>${lucideIcon(X, { size: 18, strokeWidth: 2 })}</button></header>
        <div class="token-delete-warning"><p>A new secret will replace <strong>${token.name}</strong>. The old secret stops working immediately; update every application that uses it. The new secret is shown only once.</p></div>
        <div class="token-delete-actions"><button class="primary" type="button" ?disabled=${this.tokenRotatePending} @click=${this.confirmTokenRotation}>Rotate token</button></div>
      </section>
    </dialog>`
  }

  private handleTokenListAction(event: CustomEvent<{ action: string, item: EntityListItem }>, tokens: PersonalTokenSignal[]): void {
    const { action, item } = event.detail
    if (action === 'delete') {
      const token = tokens.find((candidate) => candidate.id === item.id)
      if (token && !token.revokedAt) this.requestTokenDeletion(token)
      return
    }
    if (action === 'open' && item.href) window.location.assign(item.href)
  }

  private saveProfile = (event: Event): void => { event.preventDefault(); this.send('lv-personal-profile-command', { action: 'save', displayName: this.profileName.trim() }) }
  private chooseTheme(theme: string): void {
    this.selectedTheme = theme
    document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: theme } }))
    this.send('lv-personal-theme-command', { action: 'save', theme })
    this.closeThemeMenu(true)
  }
  private toggleThemeMenu = (): void => {
    this.themeMenuOpen = !this.themeMenuOpen
    if (!this.themeMenuOpen) return
    this.closeAvatarMenu()
    this.closePermissionMenu()
    this.closeExpirationMenu()
    void this.focusSelectedThemeOption()
  }
  private handleThemeTriggerKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    this.themeMenuOpen = true
    this.closeAvatarMenu()
    this.closePermissionMenu()
    this.closeExpirationMenu()
    void this.focusSelectedThemeOption()
  }
  private focusSelectedThemeOption = async (): Promise<void> => {
    await this.updateComplete
    const options = Array.from(this.renderRoot.querySelectorAll<HTMLButtonElement>('.theme-option'))
    const selected = options.find((option) => option.getAttribute('aria-selected') === 'true')
    const focusTarget = selected ?? options[0]
    focusTarget?.focus()
  }
  private handleThemeOptionKeydown = (event: KeyboardEvent): void => {
    const options = Array.from(this.renderRoot.querySelectorAll<HTMLButtonElement>('.theme-option'))
    const index = options.indexOf(this.shadowRoot?.activeElement as HTMLButtonElement)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      const offset = event.key === 'ArrowDown' ? 1 : -1
      options[(index + offset + options.length) % options.length]?.focus()
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault()
      options[event.key === 'Home' ? 0 : options.length - 1]?.focus()
    } else if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      this.closeThemeMenu(true)
    } else if (event.key === 'Tab') {
      this.closeThemeMenu()
    }
  }
  private openPasswordDialog = (): void => {
    this.passwordDialogOpen = true
  }
  private closePasswordDialog = (event?: Event): void => {
    event?.preventDefault()
    this.passwordDialogOpen = false
    this.currentPassword = ''
    this.newPassword = ''
  }
  private closePasswordDialogOnBackdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.closePasswordDialog(event)
  }
  private changePassword = (event: Event): void => {
    event.preventDefault()
    this.send('lv-personal-password-command', { currentPassword: this.currentPassword, newPassword: this.newPassword })
    this.passwordDialogOpen = false
    this.currentPassword = ''
    this.newPassword = ''
  }
  private requestTokenCreation = (event: Event): void => {
    event.preventDefault()
    if (!this.tokenName.trim() || !this.tokenExpirationIsValid() || this.tokenPermissionsIncomplete || this.tokenEditUnavailable) return
    if (this.tokenView === 'edit') {
      this.saveTokenEdits()
      return
    }
    this.tokenConfirmationOpen = true
  }
  private saveTokenEdits(): void {
    if (!this.tokenEditLoadedId || !this.tokenEditExpectedModifiedAt || this.tokenEditPending) return
    this.tokenEditPending = true
    this.send('lv-personal-token-command', {
      action: 'update', tokenId: this.tokenEditLoadedId,
      expectedModifiedAt: this.tokenEditExpectedModifiedAt,
      name: this.tokenName.trim(), description: this.tokenDescription.trim(),
      expiresAt: this.tokenEditExpirationTouched ? this.tokenExpirationDate().toISOString() : this.tokenEditOriginalExpiresAt,
      permissions: this.selectedTokenPermissions(),
    })
  }
  private cancelTokenConfirmation = (event?: Event): void => {
    event?.preventDefault()
    if (this.tokenCreatePending) return
    this.tokenConfirmationOpen = false
  }
  private closeTokenConfirmationOnBackdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.cancelTokenConfirmation(event)
  }
  private createToken = (): void => {
    if (!this.tokenName.trim() || !this.tokenExpirationIsValid() || this.tokenCreatePending || this.tokenPermissionsIncomplete) return
    const command: Record<string, unknown> = {
      action: 'create',
      name: this.tokenName.trim(),
      description: this.tokenDescription.trim(),
      expiresAt: this.tokenExpirationDate().toISOString(),
      // An explicit empty array creates an authentication-only credential.
      permissions: this.selectedTokenPermissions(),
    }
    this.send('lv-personal-token-command', command)
    this.tokenCreatePending = true
  }
  private tokenExpirationDate(): Date {
    if (this.tokenExpirationPreset === 'custom') return dateInputToEndOfDay(this.tokenCustomExpiration)
    const date = new Date()
    date.setDate(date.getDate() + Number(this.tokenExpirationPreset))
    return date
  }
  private tokenExpirationIsValid(): boolean {
    const expiration = this.tokenExpirationDate()
    if (this.tokenView === 'edit' && !this.tokenEditExpirationTouched) return Date.parse(this.tokenEditOriginalExpiresAt) > Date.now()
    return !Number.isNaN(expiration.valueOf()) && expiration.valueOf() > Date.now() && expiration.valueOf() <= endOfDayInDays(90).valueOf()
  }
  private requestTokenDeletion(token: PersonalTokenSignal): void {
    this.tokenPendingDeletion = token
  }
  private cancelTokenDeletion = (event?: Event): void => {
    event?.preventDefault()
    this.tokenPendingDeletion = null
  }
  private closeTokenDeletionOnBackdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.cancelTokenDeletion(event)
  }
  private confirmTokenDeletion = (): void => {
    const token = this.tokenPendingDeletion
    if (!token) return
    this.tokenPendingDeletion = null
    this.send('lv-personal-token-command', { action: 'revoke', tokenId: token.id })
  }
  private cancelTokenRotation = (event?: Event): void => { event?.preventDefault(); this.tokenPendingRotation = null }
  private closeTokenRotationOnBackdrop = (event: MouseEvent): void => { if (event.target === event.currentTarget) this.cancelTokenRotation(event) }
  private confirmTokenRotation = (): void => {
    const token = this.tokenPendingRotation
    if (!token || this.tokenRotatePending) return
    this.tokenPendingRotation = null
    this.tokenRotatePending = true
    this.send('lv-personal-token-command', { action: 'rotate', tokenId: token.id, expectedModifiedAt: token.modifiedAt || token.createdAt })
  }
  private requestSessionRevocation(session: PendingSessionRevocation): void {
    this.pendingSessionRevocation = session
  }
  private closeSessionDrawer = (): void => {
    this.selectedSession = null
  }
  private cancelSessionRevocation = (event?: Event): void => {
    event?.preventDefault()
    this.pendingSessionRevocation = null
  }
  private closeSessionRevocationOnBackdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.cancelSessionRevocation(event)
  }
  private confirmSessionRevocation = (): void => {
    const session = this.pendingSessionRevocation
    if (!session) return
    this.pendingSessionRevocation = null
    this.selectedSession = null
    if (session.current) submitAuthForm('/auth/logout')
    else if (session.kind === 'authoring') this.revokeAuthoringSession(session.id)
    else this.revokeSession(session.id)
  }
  private signOutCurrentSession = (): void => { submitAuthForm('/auth/logout') }
  private openLogoutAllDialog = (): void => { this.logoutAllDialogOpen = true }
  private closeLogoutAllDialog = (event?: Event): void => { event?.preventDefault(); this.logoutAllDialogOpen = false }
  private closeLogoutAllOnBackdrop = (event: MouseEvent): void => { if (event.target === event.currentTarget) this.closeLogoutAllDialog(event) }
  private confirmLogoutAll = (): void => { this.logoutAllDialogOpen = false; submitAuthForm('/auth/logout-all') }
  private revokeSession = (sessionId: string): void => { this.send('lv-personal-session-command', { action: 'revoke', sessionId }) }
  private revokeAuthoringSession = (sessionId: string): void => { this.send('lv-personal-authoring-session-command', { action: 'revoke', sessionId }) }
  private toggleAvatarMenu = (): void => {
    this.avatarMenuOpen = !this.avatarMenuOpen
    if (this.avatarMenuOpen) {
      this.closeThemeMenu()
      this.closePermissionMenu()
      this.closeExpirationMenu()
      void this.focusFirstAvatarMenuItem()
    }
  }
  private handleAvatarTriggerKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'ArrowDown') return
    event.preventDefault()
    this.avatarMenuOpen = true
    void this.focusFirstAvatarMenuItem()
  }
  private focusFirstAvatarMenuItem = async (): Promise<void> => {
    await this.updateComplete
    this.renderRoot.querySelector<HTMLButtonElement>('[role="menuitem"]')?.focus()
  }
  private handleAvatarMenuKeydown = (event: KeyboardEvent): void => {
    const items = Array.from(this.renderRoot.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)'))
    const index = items.indexOf(this.shadowRoot?.activeElement as HTMLButtonElement)
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      const offset = event.key === 'ArrowDown' ? 1 : -1
      items[(index + offset + items.length) % items.length]?.focus()
    } else if (event.key === 'Home' || event.key === 'End') {
      event.preventDefault()
      items[event.key === 'Home' ? 0 : items.length - 1]?.focus()
    }
  }
  private chooseAvatar = (): void => {
    this.avatarMenuOpen = false
    this.avatarInput?.click()
  }
  private closeAvatarMenu(returnFocus = false): void {
    if (!this.avatarMenuOpen) return
    this.avatarMenuOpen = false
    if (returnFocus) void this.updateComplete.then(() => this.avatarTrigger?.focus())
  }
  private handleDocumentPointerDown = (event: PointerEvent): void => {
    const path = event.composedPath()
    const avatarControl = this.renderRoot.querySelector('.avatar-control')
    if (avatarControl && !path.includes(avatarControl)) this.closeAvatarMenu()
    const permissionPicker = this.renderRoot.querySelector('.permission-picker')
    if (permissionPicker && !path.includes(permissionPicker)) this.closePermissionMenu()
    const themePicker = this.renderRoot.querySelector('.theme-picker')
    if (themePicker && !path.includes(themePicker)) this.closeThemeMenu()
  }
  private handleWindowKeydown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape' && this.avatarMenuOpen) {
      event.preventDefault()
      this.closeAvatarMenu(true)
    }
    if (event.key === 'Escape' && this.tokenPermissionMenuOpen) {
      event.preventDefault()
      this.closePermissionMenu(true)
    }
    if (event.key === 'Escape' && this.themeMenuOpen) {
      event.preventDefault()
      this.closeThemeMenu(true)
    }
  }
  private handleDatastarFetch = (event: Event): void => {
    if (!this.tokenCreatePending && !this.tokenEditPending && !this.tokenRotatePending) return
    const failure = browserCommandFailure(event, this.tokenEditPending ? 'Token update' : this.tokenRotatePending ? 'Token rotation' : 'Token creation')
    if (!failure) return
    this.tokenCreatePending = false
    this.tokenEditPending = false
    this.tokenRotatePending = false
    this.error = failure.message
  }
  private uploadAvatar = async (event: Event): Promise<void> => {
    const input = event.currentTarget as HTMLInputElement
    const file = input.files?.[0]
    if (!file) return
    this.avatarBusy = true
    this.error = ''
    try {
      const response = await fetch('/profile/avatar', { method: 'PUT', headers: { ...window.LeapViewCommand.headers('uploadCurrentAvatar'), 'Content-Type': file.type }, body: file })
      if (!response.ok) throw await avatarResponseError(response, 'Avatar upload failed')
      const uploaded = await response.json() as { url?: string }
      if (!uploaded.url?.trim()) throw new Error('Avatar upload returned no image URL')
      document.dispatchEvent(new CustomEvent('leapview-avatar-change', { detail: { url: uploaded.url } }))
      this.send('lv-personal-profile-command', { action: 'refresh', displayName: this.profileName })
      this.message = 'Profile picture updated.'
    } catch (error) { this.error = error instanceof Error ? error.message : 'Avatar upload failed' } finally { this.avatarBusy = false; input.value = '' }
  }
  private deleteAvatar = async (): Promise<void> => {
    this.avatarMenuOpen = false
    if (!window.confirm('Remove your profile picture?')) return
    this.avatarBusy = true
    this.error = ''
    try {
      const response = await fetch('/profile/avatar', { method: 'DELETE', headers: window.LeapViewCommand.headers('deleteCurrentAvatar') })
      if (!response.ok) throw await avatarResponseError(response, 'Avatar removal failed')
      document.dispatchEvent(new CustomEvent('leapview-avatar-change', { detail: { url: '' } }))
      this.send('lv-personal-profile-command', { action: 'refresh', displayName: this.profileName })
      this.message = 'Profile picture removed.'
    } catch (error) { this.error = error instanceof Error ? error.message : 'Avatar removal failed' } finally { this.avatarBusy = false }
  }
  private send(name: string, detail: Record<string, unknown>): void { this.error = ''; this.message = ''; this.dispatchEvent(new CustomEvent(name, { bubbles: true, composed: true, detail })) }
  private onProfileNameInput = (event: Event): void => { this.profileName = (event.currentTarget as HTMLInputElement).value }
  private onProfileTitleInput = (event: Event): void => { this.profileTitle = (event.currentTarget as HTMLInputElement).value }
  private onProfileUsernameInput = (event: Event): void => { this.profileUsername = (event.currentTarget as HTMLInputElement).value }
  private onCurrentPasswordInput = (event: Event): void => { this.currentPassword = (event.currentTarget as HTMLInputElement).value }
  private onNewPasswordInput = (event: Event): void => { this.newPassword = (event.currentTarget as HTMLInputElement).value }
  private onTokenNameInput = (event: Event): void => { this.tokenName = (event.currentTarget as HTMLInputElement).value }
  private onTokenDescriptionInput = (event: Event): void => { this.tokenDescription = (event.currentTarget as HTMLTextAreaElement).value }
  private onTokenCustomExpirationInput = (event: Event): void => { this.tokenCustomExpiration = (event.currentTarget as HTMLInputElement).value; this.tokenEditExpirationTouched = true }
  private chooseExpirationPreset = (event: CustomEvent<{ value: TokenExpirationPreset }>): void => {
    this.tokenExpirationPreset = event.detail.value
    this.tokenEditExpirationTouched = true
    if (event.detail.value !== 'custom') this.tokenCustomExpiration = ''
  }
  private handleExpirationMenuToggle = (event: CustomEvent<{ open: boolean }>): void => {
    if (!event.detail.open) return
    this.closeAvatarMenu()
    this.closeThemeMenu()
    this.closePermissionMenu()
  }
  private closeExpirationMenu(returnFocus = false): void {
    this.expirationSelect?.close(returnFocus)
  }
  private togglePermissionMenu = (): void => {
    if (this.tokenPermissionMenuOpen) this.closePermissionMenu()
    else this.openPermissionMenu()
  }
  private handlePermissionPickerClose = (event: CustomEvent<{ returnFocus: boolean }>): void => {
    this.closePermissionMenu(event.detail.returnFocus)
  }
  private handleTokenPermissionsChange = (event: CustomEvent<{ permissions: PersonalPermissionPairSignal[] }>): void => {
    this.tokenSelectedPermissions = this.authorizedSelectedPermissions(event.detail.permissions, this.settings.tokens.capabilities)
  }
  private handleTokenPermissionsIncompleteChange = (event: CustomEvent<{ hasIncompletePolicy: boolean }>): void => {
    this.tokenPermissionsIncomplete = event.detail.hasIncompletePolicy
  }
  private openPermissionMenu(): void {
    if (this.tokenPermissionMenuOpen) return
    this.tokenPermissionMenuOpen = true
    this.closeAvatarMenu()
    this.closeThemeMenu()
    this.closeExpirationMenu()
    void this.updateComplete.then(() => {
      this.permissionTrigger?.scrollIntoView({ block: 'nearest' })
      this.fitPermissionMenu()
      this.renderRoot.querySelector<HTMLInputElement>('.permission-search input')?.focus()
    })
  }
  private fitPermissionMenu = (): void => {
    if (!this.tokenPermissionMenuOpen || window.matchMedia('(max-width: 40rem)').matches) return
    const menu = this.renderRoot.querySelector<HTMLElement>('.permission-menu')
    if (!menu) return
    menu.style.removeProperty('--permission-menu-max-height')
    menu.dataset.placement = 'below'
    const trigger = this.permissionTrigger?.getBoundingClientRect()
    if (!trigger) return
    const viewportPadding = 16
    const menuGap = 6
    const availableBelow = Math.max(0, window.innerHeight - trigger.bottom - menuGap - viewportPadding)
    const availableAbove = Math.max(0, trigger.top - menuGap - viewportPadding)
    const minimumUsefulHeight = 224
    const placeAbove = availableBelow < minimumUsefulHeight && availableAbove > availableBelow
    menu.dataset.placement = placeAbove ? 'above' : 'below'
    const availableHeight = placeAbove ? availableAbove : availableBelow
    menu.style.setProperty('--permission-menu-max-height', `${availableHeight}px`)
  }
  private closePermissionMenu(returnFocus = false): void {
    if (!this.tokenPermissionMenuOpen) return
    this.tokenPermissionMenuOpen = false
    if (returnFocus) void this.updateComplete.then(() => this.permissionTrigger?.focus())
  }
  private closeThemeMenu(returnFocus = false): void {
    if (!this.themeMenuOpen) return
    this.themeMenuOpen = false
    if (returnFocus) void this.updateComplete.then(() => this.themeTrigger?.focus())
  }
  private tokenPermissionPolicyOptions(policy: TokenPermissionPolicy) {
    const selected = new Set(this.tokenSelectedPermissions.map(permissionPairKey))
    return [...policy.options, ...(policy.future ? [policy.future] : [])].filter((option) => option.permissions.every((pair) => selected.has(permissionPairKey(pair))))
  }
  private tokenPermissionPolicySummary(policy: TokenPermissionPolicy): string {
    if (policy.targetScope !== 'resource') return policy.description
    const selected = new Set(this.tokenSelectedPermissions.map(permissionPairKey))
    if (policy.future && policy.future.permissions.every((pair) => selected.has(permissionPairKey(pair)))) return `All current and future ${policy.category.toLocaleLowerCase()}`
    const selectedOptions = policy.options.filter((option) => option.permissions.every((pair) => selected.has(permissionPairKey(pair))))
    if (selectedOptions.length === policy.options.length && selectedOptions.length > 0) return `All ${selectedOptions.length} current ${policy.category.toLocaleLowerCase()}`
    const labels = selectedOptions.map((option) => option.label)
    if (labels.length <= 3) return labels.join(', ')
    return `${labels.slice(0, 2).join(', ')} and ${labels.length - 2} more`
  }
  private selectedTokenPermissions(): PersonalPermissionPairSignal[] {
    return this.tokenSelectedPermissions
  }
  private reconcileTokenPermissionState(capabilities: PersonalCapabilityOptionSignal[]): void {
    const reconciled = this.authorizedSelectedPermissions(this.tokenSelectedPermissions, capabilities)
    const currentKeys = this.tokenSelectedPermissions.map(permissionPairKey)
    const nextKeys = reconciled.map(permissionPairKey)
    if (currentKeys.length !== nextKeys.length || currentKeys.some((key, index) => key !== nextKeys[index])) this.tokenSelectedPermissions = reconciled
  }
  private authorizedSelectedPermissions(permissions: PersonalPermissionPairSignal[], capabilities: PersonalCapabilityOptionSignal[]): PersonalPermissionPairSignal[] {
    const policies = tokenPermissionPolicies(capabilities)
    const authorized = new Set(policies.flatMap((policy) => [...policy.options, ...(policy.future ? [policy.future] : [])].flatMap((option) => option.permissions.map(permissionPairKey))))
    let selected = uniquePermissionPairs(permissions).filter((permission) => authorized.has(permissionPairKey(permission)))
    const selectedKeys = new Set(selected.map(permissionPairKey))
    const partialBundlePairs = new Set(policies.flatMap((policy) => policy.options.filter((option) => option.permissions.length > 1).flatMap((option) => {
      const chosen = option.permissions.filter((pair) => selectedKeys.has(permissionPairKey(pair))).length
      return chosen > 0 && chosen < option.permissions.length ? option.permissions.map(permissionPairKey) : []
    })))
    if (partialBundlePairs.size) selected = selected.filter((permission) => !partialBundlePairs.has(permissionPairKey(permission)))
    return selected
  }
}


function themeOption(value: string): ThemeOption {
  return themeOptions.find((option) => option.value === value) ?? systemThemeOption
}

function usernameFromEmail(email: string): string {
  const localPart = email.split('@', 1)[0]?.trim().toLocaleLowerCase() ?? ''
  return localPart.replace(/[^a-z0-9._-]+/g, '.').replace(/^[._-]+|[._-]+$/g, '') || 'user'
}

function formatDateOnly(value: string): string {
  if (!value) return 'unknown'
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

function formatLongDate(date: Date): string {
  return Number.isNaN(date.valueOf()) ? 'the selected date' : date.toLocaleDateString(undefined, { weekday: 'long', month: 'long', day: 'numeric', year: 'numeric' })
}

function tokenExpirationOptions(): Array<{ value: TokenExpirationPreset, label: string }> {
  return ([7, 30, 60, 90] as const).map((days) => ({
    value: String(days) as TokenExpirationPreset,
    label: `${days} days (${dateInDays(days).toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })})`,
  })).concat([{ value: 'custom', label: 'Custom' }])
}

function dateInDays(days: number): Date {
  const date = new Date()
  date.setDate(date.getDate() + days)
  return date
}

function endOfDayInDays(days: number): Date {
  const date = dateInDays(days)
  date.setHours(23, 59, 59, 999)
  return date
}

function dateInputValueInDays(days: number): string {
  const date = dateInDays(days)
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function dateInputToEndOfDay(value: string): Date {
  if (!value) return new Date(Number.NaN)
  const date = new Date(`${value}T23:59:59.999`)
  return date
}

customElements.define('lv-personal-settings', LeapViewPersonalSettings)

export { LeapViewPersonalSettings }
