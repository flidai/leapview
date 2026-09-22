import { LitElement, html, nothing } from 'lit'
import { property, query, state } from 'lit/decorators.js'
import { ArrowLeft, CalendarDays, Camera, Check, ChevronDown, KeyRound, Monitor, Plus, Search, Terminal, Trash2, X } from 'lucide'
import type {
  PersonalAuthoringSessionSignal,
  PersonalCapabilityOptionSignal,
  PersonalSessionSignal,
  PersonalSettingsSignal,
  PersonalTokenSignal,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure } from '../shared/command-failure'
import { emptyStateStyles, renderEmptyState } from '../shared/empty-state'
import { lucideIcon } from '../shared/lucide-icons'
import '../shared/one-time-secret'
import '../shared/select-menu'
import type { SelectMenu } from '../shared/select-menu'
import { renderSettingsActions, renderSettingsRow, renderSettingsSection, settingsLayoutStyles } from '../shared/settings-layout'
import { submitAuthForm } from '../shared/auth-form'
import { settingsFieldStyles } from '../shared/settings-field-styles'
import { avatarResponseError } from './avatar-response'
import { formatDate, formatRelativeActivity, humanizeCapability, humanizeSessionKind, sessionFact } from './personal-settings-format'
import { personalSettingsStyles } from './personal-settings.styles'
import '../shared/drawer'
import '../shared/user-avatar'
const emptySettings: PersonalSettingsSignal = {
  active: 'profile',
  profile: { id: '', email: '', displayName: '', theme: 'system', identitySource: '', canEditDisplayName: false, hasLocalPassword: false },
  security: { localPasswordEnabled: false, sessions: [], authoringSessions: [] },
  tokens: { items: [], capabilities: [] },
}

type ThemeOption = {
  value: string
  label: string
  group: 'Automatic' | 'Standard' | 'Accessibility'
  tone: 'system' | 'light' | 'dark'
}

type TokenPermissionAccess = 'read' | 'write'

type TokenExpirationPreset = '7' | '30' | '60' | '90' | 'custom'

type TokenPermissionAccessOption = {
  value: TokenPermissionAccess
  label: 'Read-only' | 'Read and write'
  capabilities: string[]
}

type PendingSessionRevocation = {
  id: string
  label: string
  kind: 'browser' | 'authoring'
  current?: boolean
}

type SelectedSession = {
  id: string
  kind: 'browser' | 'authoring'
}

type TokenPermissionDefinition = {
  id: string
  label: string
  description: string
  category: string
  searchText: string
  access: TokenPermissionAccessOption[]
}

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

class LeapViewPersonalSettings extends DatastarLit(LitElement) {
  @state() private profileName = ''
  @state() private profileTitle = ''
  @state() private profileUsername = ''
  @state() private currentPassword = ''
  @state() private newPassword = ''
  @state() private tokenName = ''
  @state() private tokenDescription = ''
  @state() private tokenPermissionSelections: Record<string, TokenPermissionAccess> = {}
  @state() private tokenExpirationPreset: TokenExpirationPreset = '30'
  @state() private tokenCustomExpiration = ''
  @state() private tokenPendingDeletion: PersonalTokenSignal | null = null
  @state() private pendingSessionRevocation: PendingSessionRevocation | null = null
  @state() private selectedSession: SelectedSession | null = null
  @state() private passwordDialogOpen = false
  @state() private logoutAllDialogOpen = false
  @property({ attribute: 'token-view' }) tokenView: 'list' | 'create' = 'list'
  @state() private tokenConfirmationOpen = false
  @state() private tokenCreatePending = false
  @state() private tokenPermissionMenuOpen = false
  @state() private tokenPermissionSearch = ''
  @state() private tokenPermissionAccessMenu = ''
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
      const navigateToTokenList = this.tokenView === 'create' && window.location.pathname === '/admin/api-tokens/new'
      this.handledNewToken = newToken
      this.tokenCreatePending = false
      this.tokenView = 'list'
      this.tokenConfirmationOpen = false
      this.tokenName = ''
      this.tokenDescription = ''
      this.tokenPermissionSelections = {}
      this.tokenExpirationPreset = '30'
      this.tokenCustomExpiration = ''
      this.closeExpirationMenu()
      this.closePermissionMenu()
      this.closeTokenPermissionAccessMenu()
      if (navigateToTokenList) window.history.replaceState(window.history.state, '', '/admin/api-tokens')
    }
    const confirmation = this.tokenConfirmationDialog
    if (this.tokenConfirmationOpen && confirmation && !confirmation.open) confirmation.showModal()
    const deletion = this.tokenDeleteDialog
    if (this.tokenPendingDeletion && deletion && !deletion.open) deletion.showModal()
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
    const currentSessions = browserSessions.filter((session) => session.current)
    const otherSessions = browserSessions.filter((session) => !session.current)
    const authoringSessions = settings.security.authoringSessions.filter((session) => !session.revokedAt)
    const activeCount = browserSessions.length + authoringSessions.length
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
            <div class="security-session-actions"><span class="security-session-count">${activeCount} active ${activeCount === 1 ? 'session' : 'sessions'}</span><button type="button" class="danger" data-logout-all @click=${this.openLogoutAllDialog}>Log out all browser and desktop sessions</button></div>
          </div>
          <div class="security-session-list">
            <div class="security-session-group" aria-label="Current session">
              <div class="security-session-group-label">Current</div>
              ${currentSessions.length ? currentSessions.map((session) => this.renderSession(session)) : html`<div class="security-session security-empty">Current session information is unavailable.</div>`}
            </div>
            ${otherSessions.length ? html`<div class="security-session-group" aria-label="Other devices"><div class="security-session-group-label">Other devices</div>${otherSessions.map((session) => this.renderSession(session))}</div>` : nothing}
            ${authoringSessions.length ? html`<div class="security-session-group" aria-label="CLI and authoring"><div class="security-session-group-label">CLI &amp; authoring</div>${authoringSessions.map((session) => this.renderAuthoringSession(session))}</div>` : nothing}
          </div>
        </section>
        ${this.passwordDialogOpen ? this.renderPasswordDialog() : nothing}
        ${this.logoutAllDialogOpen ? this.renderLogoutAllDialog() : nothing}
        ${this.renderSessionDrawer(settings)}
        ${this.pendingSessionRevocation ? this.renderSessionRevokeConfirmation(this.pendingSessionRevocation) : nothing}
      </section>
    `
  }

  private renderSession(session: PersonalSessionSignal) {
    const label = session.clientLabel || humanizeSessionKind(session.kind)
    return html`<div class="security-session">
      <span class="security-session-icon" aria-hidden="true">${lucideIcon(Monitor, { size: 16, strokeWidth: 1.75 })}</span>
      <button class="security-session-main" type="button" aria-label=${`View details for ${label}`} @click=${() => { this.selectedSession = { id: session.id, kind: 'browser' } }}>
        <div class="security-session-title"><strong>${label}</strong>${session.current ? html`<span class="security-badge">This device</span>` : nothing}</div>
        <span class="security-session-meta">${session.current ? 'Active now' : `Last active ${formatRelativeActivity(session.lastSeenAt)}`}</span>
      </button>
      <button class="session-action" type="button" @click=${() => this.requestSessionRevocation({ id: session.id, label, kind: 'browser', current: session.current })}>${session.current ? 'Sign out' : 'Revoke'}</button>
    </div>`
  }

  private renderAuthoringSession(session: PersonalAuthoringSessionSignal) {
    const label = session.clientId || humanizeSessionKind(session.kind)
    return html`<div class="security-session">
      <span class="security-session-icon" aria-hidden="true">${lucideIcon(Terminal, { size: 16, strokeWidth: 1.75 })}</span>
      <button class="security-session-main" type="button" aria-label=${`View details for ${label}`} @click=${() => { this.selectedSession = { id: session.id, kind: 'authoring' } }}>
        <div class="security-session-title"><strong>${label}</strong></div>
        <span class="security-session-meta">${session.projectId || 'All available projects'} · ${session.capabilities.map(humanizeCapability).join(', ') || 'Scoped access'} · ${session.lastUsedAt ? `Last active ${formatRelativeActivity(session.lastUsedAt)}` : 'Never used'}</span>
      </button>
      <button class="session-action" type="button" @click=${() => this.requestSessionRevocation({ id: session.id, label, kind: 'authoring' })}>Revoke</button>
    </div>`
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
          ${sessionFact('Capabilities', session.capabilities.map(humanizeCapability).join(', ') || 'Scoped access')}
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
    const permissions = tokenPermissionDefinitions(tokens.capabilities)
    const selected = permissions.filter((permission) => this.tokenPermissionAccess(permission))
    const categories = groupTokenPermissions(this.filteredTokenPermissions(permissions))
    const categoryStats = new Map(groupTokenPermissions(permissions).map(([category, categoryPermissions]) => [category, {
      selected: categoryPermissions.filter((permission) => this.tokenPermissionAccess(permission)).length,
      total: categoryPermissions.length,
    }]))
    const expirationOptions = tokenExpirationOptions()
    const canCreate = Boolean(this.tokenName.trim() && this.tokenExpirationIsValid() && !this.tokenCreatePending)
    return html`
      <section class="token-page" aria-label="Create personal access token">
        <div class="token-create-header">
          <a class="token-back" href="/admin/api-tokens" aria-label="Back to personal access tokens">${lucideIcon(ArrowLeft, { size: 16, strokeWidth: 2 })}</a>
          <div class="token-page-heading"><h2>New personal access token</h2></div>
        </div>
        <form class="token-form" @submit=${this.requestTokenCreation}>
          <p class="token-page-intro">Create a scoped token suitable for personal API, CLI, and automation access.</p>
          <div class="token-details">
            <label class="token-field" for="token-name">
              <span class="settings-label">Token name *</span>
              <input id="token-name" required autocomplete="off" placeholder="For example, Sales reporting" .value=${this.tokenName} @input=${this.onTokenNameInput}>
              <span class="settings-description">A unique name for this token. It may be visible to administrators.</span>
            </label>
            <label class="token-field" for="token-description">
              <span class="settings-label">Description <span class="muted">(optional)</span></span>
              <textarea id="token-description" maxlength="1024" placeholder="What will this token be used for?" .value=${this.tokenDescription} @input=${this.onTokenDescriptionInput}></textarea>
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
              ${this.tokenExpirationPreset === 'custom' ? html`<input class="custom-expiration" id="token-custom-expiration" type="date" min=${dateInputValueInDays(1)} max=${dateInputValueInDays(90)} .value=${this.tokenCustomExpiration} @input=${this.onTokenCustomExpirationInput} required>` : nothing}
              <span class="settings-description">The token will expire on the selected date. Maximum lifetime is 90 days.</span>
            </div>
          </div>
          <div class="permissions">
            <div class="permissions-heading">
              <h3>Permissions</h3>
              <p class="token-page-intro">Choose the minimal permissions necessary for your needs.</p>
            </div>
            <div class="permissions-card">
              <div class="permissions-header">
                <div class="permissions-title"><span class="settings-label">Token permissions</span><span class="count" aria-label="${selected.length} selected permissions">${selected.length}</span></div>
                <div class="permission-picker">
                  <button class="permission-trigger" type="button" aria-haspopup="dialog" aria-controls="token-permission-menu" aria-expanded=${String(this.tokenPermissionMenuOpen)} @click=${this.togglePermissionMenu} @keydown=${this.handlePermissionTriggerKeydown}>
                    ${lucideIcon(Plus, { size: 16, strokeWidth: 2 })}<span>Add permissions</span>
                  </button>
                  ${this.tokenPermissionMenuOpen ? html`
                      <div class="permission-backdrop" aria-hidden="true" @click=${() => this.closePermissionMenu(true)}></div>
                      <div id="token-permission-menu" class="permission-menu" role="dialog" aria-labelledby="token-permission-menu-title">
                        <div class="permission-menu-header">
                          <div class="permission-menu-title">
                            <div class="permission-menu-heading">
                              <span class="settings-label" id="token-permission-menu-title">Select token permissions</span>
                              <span class="permission-menu-count" aria-live="polite">${selected.length} selected</span>
                            </div>
                            <button class="permission-menu-close" type="button" aria-label="Close permission picker" @click=${() => this.closePermissionMenu(true)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button>
                          </div>
                          <label class="permission-search">
                            ${lucideIcon(Search, { size: 16, strokeWidth: 2 })}
                            <input type="search" aria-label="Search permissions" placeholder="Search permissions" .value=${this.tokenPermissionSearch} @input=${this.onTokenPermissionSearch}>
                          </label>
                        </div>
                        <div class="permission-list">
                          ${categories.length ? categories.map(([category, categoryPermissions], categoryIndex) => html`
                            <div class="permission-group" role="group" aria-labelledby=${`token-permission-category-${categoryIndex}`}>
                              <div class="permission-category" id=${`token-permission-category-${categoryIndex}`}>
                                <span>${category}</span>
                                <span class="permission-category-count">${categoryStats.get(category)?.selected ?? 0} / ${categoryStats.get(category)?.total ?? categoryPermissions.length}</span>
                              </div>
                              ${categoryPermissions.map((permission) => {
                                const permissionSelected = Boolean(this.tokenPermissionAccess(permission))
                                const descriptionID = `token-permission-description-${permission.id}`
                                return html`
                                  <label class="permission-option" data-selected=${String(permissionSelected)}>
                                    <input aria-label=${permission.label} aria-describedby=${descriptionID} type="checkbox" value=${permission.id} .checked=${permissionSelected} @change=${() => this.toggleTokenPermission(permission)}>
                                    <span class="settings-field"><span class="settings-label">${permission.label}</span><span class="settings-description" id=${descriptionID}>${permission.description}</span></span>
                                  </label>
                                `
                              })}
                            </div>
                          `) : html`<div class="permission-empty">No permissions match your search.</div>`}
                        </div>
                      </div>
                  ` : nothing}
                </div>
              </div>
              <div class="selected-permissions" aria-live="polite">
                ${selected.length ? selected.map((permission) => this.renderSelectedTokenPermission(permission)) : html`
                  <div class="selected-permissions-empty">
                    ${lucideIcon(KeyRound, { size: 28, strokeWidth: 1.8 })}
                    <div class="settings-field"><span class="settings-label">No permissions added yet</span><span class="settings-description">Without explicit permissions, this token follows your current access.</span></div>
                  </div>
                `}
              </div>
            </div>
          </div>
          <div class="actions token-actions"><button class="primary" type="submit" ?disabled=${!canCreate}>Generate token</button><a class="button-link" href="/admin/api-tokens">Cancel</a></div>
        </form>
        ${this.tokenConfirmationOpen ? this.renderTokenConfirmation() : nothing}
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
        ${tokens.items.length ? html`<div class="card token-list">${tokens.items.map((token, index) => this.renderToken(token, tokens.capabilities, index === 0 ? tokens.newToken : undefined))}</div>` : renderEmptyState({
          icon: lucideIcon(KeyRound, { size: 28, strokeWidth: 1.8 }),
          title: 'No personal access tokens yet',
          description: 'Generate a token when a tool or script needs to access LeapView.',
        })}
        ${this.tokenPendingDeletion ? this.renderTokenDeleteConfirmation(this.tokenPendingDeletion) : nothing}
      </section>
    `
  }

  private renderTokenConfirmation() {
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

  private renderSelectedTokenPermission(permission: TokenPermissionDefinition) {
    const access = this.tokenPermissionAccess(permission)
    if (!access) return nothing
    const accessMenuOpen = this.tokenPermissionAccessMenu === permission.id
    const accessControlID = `token-permission-access-${permission.id}`
    return html`
      <div class="selected-permission" data-permission=${permission.id}>
        <div class="settings-field"><span class="settings-label">${permission.label}</span><span class="settings-description">${permission.description}</span></div>
        <div class="permission-row-actions">
          <div class="permission-access-picker">
            ${permission.access.length > 1 ? html`
              <button
                class="permission-access-trigger"
                type="button"
                aria-label=${`Access for ${permission.label}: ${access.label}`}
                aria-haspopup="listbox"
                aria-controls=${accessControlID}
                aria-expanded=${String(accessMenuOpen)}
                data-permission=${permission.id}
                @click=${() => this.toggleTokenPermissionAccessMenu(permission.id)}
                @keydown=${(event: KeyboardEvent) => this.handleTokenPermissionAccessTriggerKeydown(event, permission.id)}
              >
                <span><span class="permission-access-prefix">Access: </span><span class="permission-access-value">${access.label}</span></span>
                ${lucideIcon(ChevronDown, { size: 16, strokeWidth: 2 })}
              </button>
            ` : html`
              <span class="permission-access-fixed" aria-label=${`Access for ${permission.label}: ${access.label}`}>
                <span class="permission-access-prefix">Access: </span><span class="permission-access-value">${access.label}</span>
              </span>
            `}
            ${accessMenuOpen ? html`
              <div id=${accessControlID} class="permission-access-menu" role="listbox" aria-label=${`Access for ${permission.label}`} @keydown=${this.handleTokenPermissionAccessOptionKeydown}>
                ${permission.access.map((option) => html`
                  <button
                    class="permission-access-option"
                    type="button"
                    role="option"
                    aria-selected=${String(option.value === access.value)}
                    data-access=${option.value}
                    @click=${() => this.chooseTokenPermissionAccess(permission, option)}
                  >
                    <span>${option.label}</span>
                    <span class="permission-access-check" aria-hidden="true">${option.value === access.value ? lucideIcon(Check, { size: 16, strokeWidth: 2 }) : nothing}</span>
                  </button>
                `)}
              </div>
            ` : nothing}
          </div>
          <button class="permission-remove" type="button" aria-label=${`Remove ${permission.label}`} @click=${() => this.removeTokenPermission(permission)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button>
        </div>
      </div>
    `
  }

  private renderToken(token: PersonalTokenSignal, capabilities: PersonalCapabilityOptionSignal[], newToken?: string) {
    const options = new Map(capabilities.map((capability) => [capability.value, capability.label]))
    const labels = token.capabilities.map((capability) => options.get(capability) ?? humanizeCapability(capability))
    const usage = token.lastUsedAt ? `Last used ${formatDateOnly(token.lastUsedAt)}` : 'Never used'
    return html`
      <div class="token-row">
        <span class="token-row-icon" aria-hidden="true">${lucideIcon(KeyRound, { size: 16, strokeWidth: 2 })}</span>
        <div class="token-row-content">
          <span class="token-name">${token.name}</span>
          ${token.description ? html`<span class="token-description">${token.description}</span>` : nothing}
          <span class="token-meta">${usage}${token.expiresAt ? ` · Expires ${formatDateOnly(token.expiresAt)}` : ''} · ${labels.join(', ') || 'Follows current access'}</span>
          ${newToken ? html`<lv-one-time-secret secret=${newToken} message="Copy your personal access token now. You won’t be able to see it again." copy-label="Copy personal access token"></lv-one-time-secret>` : nothing}
        </div>
        ${token.revokedAt ? html`<span class="muted">Revoked</span>` : html`<button class="danger" type="button" @click=${() => this.requestTokenDeletion(token)}>Delete</button>`}
      </div>
    `
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
    if (!this.tokenName.trim() || !this.tokenExpirationIsValid()) return
    this.tokenConfirmationOpen = true
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
    if (!this.tokenName.trim() || !this.tokenExpirationIsValid() || this.tokenCreatePending) return
    const command: Record<string, unknown> = {
      action: 'create',
      name: this.tokenName.trim(),
      description: this.tokenDescription.trim(),
      expiresAt: this.tokenExpirationDate().toISOString(),
    }
    const capabilities = this.selectedTokenCapabilities()
    if (capabilities.length) command.capabilities = capabilities
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
    const insidePermissionAccessPicker = path.some((node) => node instanceof Element && node.classList.contains('permission-access-picker'))
    if (!insidePermissionAccessPicker) this.closeTokenPermissionAccessMenu()
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
    if (event.key === 'Escape' && this.tokenPermissionAccessMenu) {
      event.preventDefault()
      this.closeTokenPermissionAccessMenu(true)
    }
  }
  private handleDatastarFetch = (event: Event): void => {
    if (!this.tokenCreatePending) return
    const failure = browserCommandFailure(event, 'Token creation')
    if (!failure) return
    this.tokenCreatePending = false
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
  private onTokenCustomExpirationInput = (event: Event): void => { this.tokenCustomExpiration = (event.currentTarget as HTMLInputElement).value }
  private chooseExpirationPreset = (event: CustomEvent<{ value: TokenExpirationPreset }>): void => {
    this.tokenExpirationPreset = event.detail.value
    if (event.detail.value !== 'custom') this.tokenCustomExpiration = ''
  }
  private handleExpirationMenuToggle = (event: CustomEvent<{ open: boolean }>): void => {
    if (!event.detail.open) return
    this.closeAvatarMenu()
    this.closeThemeMenu()
    this.closePermissionMenu()
    this.closeTokenPermissionAccessMenu()
  }
  private closeExpirationMenu(returnFocus = false): void {
    this.expirationSelect?.close(returnFocus)
  }
  private filteredTokenPermissions(permissions: TokenPermissionDefinition[]): TokenPermissionDefinition[] {
    const query = this.tokenPermissionSearch.trim().toLocaleLowerCase()
    if (!query) return permissions
    return permissions.filter((permission) => `${permission.label} ${permission.description} ${permission.category} ${permission.searchText}`.toLocaleLowerCase().includes(query))
  }
  private togglePermissionMenu = (): void => {
    if (this.tokenPermissionMenuOpen) this.closePermissionMenu()
    else this.openPermissionMenu()
  }
  private handlePermissionTriggerKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    this.openPermissionMenu()
  }
  private openPermissionMenu(): void {
    if (this.tokenPermissionMenuOpen) return
    this.tokenPermissionMenuOpen = true
    this.closeAvatarMenu()
    this.closeThemeMenu()
    this.closeExpirationMenu()
    this.closeTokenPermissionAccessMenu()
    void this.updateComplete.then(() => {
      this.fitPermissionMenu()
      this.renderRoot.querySelector<HTMLInputElement>('.permission-search input')?.focus()
    })
  }
  private fitPermissionMenu = (): void => {
    if (!this.tokenPermissionMenuOpen || window.matchMedia('(max-width: 40rem)').matches) return
    const menu = this.renderRoot.querySelector<HTMLElement>('.permission-menu')
    if (!menu) return
    menu.style.removeProperty('--permission-menu-max-height')
    const availableHeight = Math.max(0, window.innerHeight - menu.getBoundingClientRect().top - 16)
    menu.style.setProperty('--permission-menu-max-height', `${availableHeight}px`)
  }
  private closePermissionMenu(returnFocus = false): void {
    if (!this.tokenPermissionMenuOpen) return
    this.tokenPermissionMenuOpen = false
    this.tokenPermissionSearch = ''
    if (returnFocus) void this.updateComplete.then(() => this.permissionTrigger?.focus())
  }
  private closeThemeMenu(returnFocus = false): void {
    if (!this.themeMenuOpen) return
    this.themeMenuOpen = false
    if (returnFocus) void this.updateComplete.then(() => this.themeTrigger?.focus())
  }
  private onTokenPermissionSearch = (event: Event): void => { this.tokenPermissionSearch = (event.currentTarget as HTMLInputElement).value }
  private tokenPermissionAccess(permission: TokenPermissionDefinition): TokenPermissionAccessOption | undefined {
    const selectedAccess = this.tokenPermissionSelections[permission.id]
    return permission.access.find((option) => option.value === selectedAccess)
  }
  private toggleTokenPermission(permission: TokenPermissionDefinition): void {
    if (this.tokenPermissionAccess(permission)) {
      this.removeTokenPermission(permission)
      return
    }
    const defaultAccess = permission.access[0]
    if (!defaultAccess) return
    this.tokenPermissionSelections = { ...this.tokenPermissionSelections, [permission.id]: defaultAccess.value }
  }
  private removeTokenPermission(permission: TokenPermissionDefinition): void {
    const selections = { ...this.tokenPermissionSelections }
    delete selections[permission.id]
    this.tokenPermissionSelections = selections
    if (this.tokenPermissionAccessMenu === permission.id) this.closeTokenPermissionAccessMenu()
  }
  private chooseTokenPermissionAccess(permission: TokenPermissionDefinition, access: TokenPermissionAccessOption): void {
    this.tokenPermissionSelections = { ...this.tokenPermissionSelections, [permission.id]: access.value }
    this.closeTokenPermissionAccessMenu(true)
  }
  private selectedTokenCapabilities(): string[] {
    const selected = new Set(tokenPermissionDefinitions(this.settings.tokens.capabilities).flatMap((permission) => {
      const access = this.tokenPermissionAccess(permission)
      return access?.capabilities ?? []
    }))
    return this.settings.tokens.capabilities.map((capability) => capability.value).filter((capability) => selected.has(capability))
  }
  private toggleTokenPermissionAccessMenu(permissionID: string): void {
    if (this.tokenPermissionAccessMenu === permissionID) this.closeTokenPermissionAccessMenu()
    else this.openTokenPermissionAccessMenu(permissionID)
  }
  private openTokenPermissionAccessMenu(permissionID: string): void {
    this.tokenPermissionAccessMenu = permissionID
    this.closeAvatarMenu()
    this.closeThemeMenu()
    this.closePermissionMenu()
    this.closeExpirationMenu()
    void this.focusSelectedTokenPermissionAccess()
  }
  private handleTokenPermissionAccessTriggerKeydown(event: KeyboardEvent, permissionID: string): void {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    this.openTokenPermissionAccessMenu(permissionID)
  }
  private focusSelectedTokenPermissionAccess = async (): Promise<void> => {
    await this.updateComplete
    const options = Array.from(this.renderRoot.querySelectorAll<HTMLButtonElement>('.permission-access-option'))
    ;(options.find((option) => option.getAttribute('aria-selected') === 'true') ?? options[0])?.focus()
  }
  private handleTokenPermissionAccessOptionKeydown = (event: KeyboardEvent): void => {
    const options = Array.from(this.renderRoot.querySelectorAll<HTMLButtonElement>('.permission-access-option'))
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
      this.closeTokenPermissionAccessMenu(true)
    } else if (event.key === 'Tab') {
      this.closeTokenPermissionAccessMenu()
    }
  }
  private closeTokenPermissionAccessMenu(returnFocus = false): void {
    if (!this.tokenPermissionAccessMenu) return
    const permissionID = this.tokenPermissionAccessMenu
    this.tokenPermissionAccessMenu = ''
    if (returnFocus) void this.updateComplete.then(() => this.renderRoot.querySelector<HTMLButtonElement>(`.permission-access-trigger[data-permission="${permissionID}"]`)?.focus())
  }
}

function tokenPermissionDefinitions(capabilities: PersonalCapabilityOptionSignal[]): TokenPermissionDefinition[] {
  const options = new Map(capabilities.map((capability) => [capability.value, capability]))
  const permissions: TokenPermissionDefinition[] = []
  const baseReadCapabilities = ['RESOURCE_USE', 'RESOURCE_READ'].filter((value) => options.has(value))
  const addPermission = (
    id: string,
    writeCapabilityValue: string,
    label: string,
  ): void => {
    const writeCapability = options.get(writeCapabilityValue)
    if (!writeCapability) return
    const access: TokenPermissionAccessOption[] = []
    if (baseReadCapabilities.length) {
      access.push({ value: 'read', label: 'Read-only', capabilities: [...baseReadCapabilities] })
    }
    access.push({
      value: 'write',
      label: 'Read and write',
      capabilities: uniqueCapabilities([...baseReadCapabilities, writeCapability.value]),
    })
    permissions.push({
      id,
      label,
      description: writeCapability.description,
      category: writeCapability.category,
      searchText: `${writeCapability.value} ${writeCapability.label}`,
      access,
    })
  }

  addPermission('project-administration', 'PROJECT_ADMIN', 'Project administration')

  const use = options.get('RESOURCE_USE')
  const read = options.get('RESOURCE_READ')
  const edit = options.get('RESOURCE_EDIT')
  if (use || read || edit) {
    const access: TokenPermissionAccessOption[] = []
    const readCapabilities = [use?.value, read?.value].filter((value): value is string => Boolean(value))
    if (readCapabilities.length) access.push({ value: 'read', label: 'Read-only', capabilities: uniqueCapabilities(readCapabilities) })
    if (edit) access.push({ value: 'write', label: 'Read and write', capabilities: uniqueCapabilities([...readCapabilities, edit.value]) })
    permissions.push({
      id: 'resource-content',
      label: 'Resource access',
      description: 'Open and view project resources, or add access to create and update them.',
      category: use?.category ?? read?.category ?? edit?.category ?? 'Resource',
      searchText: [use, read, edit].filter(Boolean).map((capability) => `${capability?.value} ${capability?.label} ${capability?.description}`).join(' '),
      access,
    })
  }

  addPermission('resource-management', 'RESOURCE_MANAGE', 'Resource management')
  addPermission('resource-sharing', 'RESOURCE_SHARE', 'Resource sharing')
  addPermission('resource-publishing', 'RESOURCE_PUBLISH', 'Resource publishing')
  return permissions
}

function groupTokenPermissions(permissions: TokenPermissionDefinition[]): Array<[string, TokenPermissionDefinition[]]> {
  const groups = new Map<string, TokenPermissionDefinition[]>()
  for (const permission of permissions) {
    const values = groups.get(permission.category) ?? []
    values.push(permission)
    groups.set(permission.category, values)
  }
  return [...groups.entries()]
}

function uniqueCapabilities(capabilities: string[]): string[] {
  return [...new Set(capabilities)]
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
