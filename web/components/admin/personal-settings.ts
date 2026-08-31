import { LitElement, css, html, nothing } from 'lit'
import { query, state } from 'lit/decorators.js'
import { Camera, Check, ChevronDown, Plus, Search, Trash2, X } from 'lucide'
import type {
  PersonalAuthoringSessionSignal,
  PersonalCapabilityOptionSignal,
  PersonalSessionSignal,
  PersonalSettingsSignal,
  PersonalTokenSignal,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure } from '../shared/command-failure'
import { lucideIcon } from '../shared/lucide-icons'
import { settingsFieldStyles } from '../shared/settings-field-styles'
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
  @state() private tokenCapabilities: string[] = []
  @state() private tokenExpires = ''
  @state() private tokenCreatePending = false
  @state() private tokenPermissionMenuOpen = false
  @state() private tokenPermissionSearch = ''
  @state() private message = ''
  @state() private error = ''
  @state() private avatarMenuOpen = false
  @state() private themeMenuOpen = false
  @state() private selectedTheme = ''
  @state() private avatarBusy = false
  @query('.avatar-trigger') private avatarTrigger?: HTMLButtonElement
  @query('.avatar-input') private avatarInput?: HTMLInputElement
  @query('.permission-trigger') private permissionTrigger?: HTMLButtonElement
  @query('.theme-trigger') private themeTrigger?: HTMLButtonElement
  private handledNewToken = ''
  private observedDisplayName = ''
  private observedProfileID = ''
  private observedTheme = ''

  static styles = [settingsFieldStyles, css`
    :host { display: block; color: var(--lv-fg-default); font: var(--lv-type-body); }
    .settings { display: grid; gap: var(--base-size-20); width: 100%; min-width: 0; }
    section { display: grid; gap: var(--base-size-20); }
    h2, h3, p { margin: 0; }
    h2 { font: var(--lv-type-section-title); }
    h3 { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .card { display: grid; gap: 0; overflow: visible; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); }
    .row { display: grid; min-height: var(--base-size-48); box-sizing: border-box; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-16); padding: var(--base-size-8) var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .row:first-child { border-radius: var(--lv-radius-large) var(--lv-radius-large) 0 0; }
    .row:last-child { border-bottom: 0; }
    .profile-row { min-height: var(--base-size-64); padding: var(--base-size-12) var(--base-size-20); }
    .profile-email { max-width: 22rem; justify-self: end; text-align: right; }
    .profile-name-form { min-width: 0; justify-self: end; }
    .profile-name-control { display: flex; min-width: 0; align-items: center; justify-content: flex-end; gap: var(--base-size-8); }
    .profile-name-control input { width: min(13rem, 40vw); min-height: var(--control-medium-size, var(--base-size-32)); text-align: center; font: var(--lv-type-body); }
    .profile-local-input { width: min(13rem, 40vw); min-height: var(--control-medium-size, var(--base-size-32)); justify-self: end; text-align: center; font: var(--lv-type-body); }
    .muted { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    input, select { min-width: 0; min-height: var(--control-small-size); box-sizing: border-box; border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: 0 var(--control-small-paddingInline-normal); color: var(--lv-fg-default); background: var(--lv-bg-input); font: var(--lv-type-body-compact); }
    button { min-height: var(--control-small-size); border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: 0 var(--control-small-paddingInline-normal); color: var(--lv-fg-default); background: var(--lv-button-bg-rest); cursor: pointer; font: var(--lv-type-body-compact); }
    button.primary { color: var(--lv-fg-on-accent); border-color: var(--lv-bg-accent); background: var(--lv-bg-accent); }
    button.danger { color: var(--lv-fg-danger); }
    button:disabled { cursor: not-allowed; opacity: .55; }
    form { display: grid; gap: var(--base-size-8); }
    .form-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr)); gap: var(--base-size-8); }
    .actions { display: flex; flex-wrap: wrap; gap: var(--base-size-8); align-items: center; }
    .token-form { width: 100%; gap: var(--base-size-20); }
    .token-form-grid { display: grid; grid-template-columns: minmax(0, 1fr) minmax(12rem, .7fr); gap: var(--base-size-16); }
    .token-field { display: grid; min-width: 0; align-content: start; gap: var(--base-size-6); }
    .token-field input, .token-field select { width: 100%; }
    .scope-description { min-height: var(--base-size-16); }
    .permissions { display: grid; gap: var(--base-size-12); }
    .permissions-header { display: flex; min-width: 0; align-items: center; justify-content: space-between; gap: var(--base-size-12); }
    .permissions-title { display: flex; min-width: 0; align-items: center; gap: var(--base-size-8); }
    .count { display: inline-grid; min-width: var(--base-size-24); height: var(--base-size-24); box-sizing: border-box; place-items: center; border-radius: var(--lv-radius-full); padding: 0 var(--base-size-8); color: var(--lv-fg-muted); background: var(--lv-bg-control); font: var(--lv-type-caption); }
    .permission-picker { position: relative; flex: none; }
    .permission-trigger { display: inline-flex; align-items: center; gap: var(--base-size-8); }
    .permission-trigger:hover, .permission-trigger:focus-visible, .permission-trigger[aria-expanded="true"] { border-color: var(--lv-border-accent); outline: 0; }
    .permission-trigger svg, .permission-remove svg, .permission-search svg { width: var(--base-size-16); height: var(--base-size-16); }
    .permission-backdrop { display: none; }
    .permission-menu { position: absolute; z-index: var(--z-index-dropdown); top: calc(100% + var(--base-size-6)); right: 0; display: grid; width: min(28rem, calc(100vw - var(--base-size-32))); max-height: min(32rem, var(--permission-menu-max-height, calc(100svh - var(--base-size-64)))); box-sizing: border-box; grid-template-rows: auto minmax(0, 1fr); overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); }
    .permission-menu-header { display: grid; gap: var(--base-size-12); padding: var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .permission-menu-title { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-8); }
    .permission-menu-heading { display: flex; min-width: 0; flex-wrap: wrap; align-items: center; gap: var(--base-size-8); }
    .permission-menu-count { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .permission-menu-close { display: none; width: var(--control-small-size); min-height: var(--control-small-size); place-items: center; padding: 0; color: var(--lv-fg-muted); background: transparent; }
    .permission-search { position: relative; display: grid; align-items: center; }
    .permission-search svg { position: absolute; left: var(--base-size-12); z-index: 1; color: var(--lv-fg-muted); pointer-events: none; }
    .permission-search input { width: 100%; min-height: var(--control-xlarge-size, var(--base-size-40)); padding-left: var(--base-size-40); font: var(--lv-type-body); }
    .permission-search input:focus-visible { border-color: var(--lv-border-accent); outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .permission-list { display: grid; min-height: 0; align-content: start; overflow-y: auto; overscroll-behavior: contain; scrollbar-color: var(--lv-scrollbar-thumb) transparent; scrollbar-width: thin; }
    .permission-group + .permission-group { border-top: var(--lv-border-muted); }
    .permission-category { display: flex; min-height: var(--base-size-32); box-sizing: border-box; align-items: center; justify-content: space-between; gap: var(--base-size-8); padding: var(--base-size-8) var(--base-size-12) var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-semibold); }
    .permission-category-count { font-weight: var(--base-text-weight-normal); font-variant-numeric: tabular-nums; }
    .permission-option { display: grid; min-width: 0; min-height: var(--base-size-48); box-sizing: border-box; grid-template-columns: var(--base-size-20) minmax(0, 1fr); align-items: start; gap: var(--base-size-8); padding: var(--base-size-8) var(--base-size-12); cursor: pointer; }
    .permission-option:hover { background: var(--lv-bg-control-hover); }
    .permission-option[data-selected="true"] { background: var(--lv-bg-accent-muted, var(--lv-bg-control-hover)); }
    .permission-option:focus-within { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .permission-option input[type="checkbox"] { width: var(--base-size-16); height: var(--base-size-16); min-height: 0; margin: var(--base-size-2) 0 0; padding: 0; accent-color: var(--lv-bg-accent); }
    .selected-permissions { display: grid; overflow: hidden; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); }
    .selected-permission { display: grid; min-height: var(--base-size-64); grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-12); padding: var(--base-size-12) var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .selected-permission:last-child { border-bottom: 0; }
    .permission-remove { display: grid; width: var(--control-small-size); min-height: var(--control-small-size); place-items: center; padding: 0; color: var(--lv-fg-muted); background: transparent; }
    .permission-remove:hover, .permission-remove:focus-visible { color: var(--lv-fg-danger); border-color: var(--lv-fg-danger); outline: 0; }
    .permission-empty { display: grid; min-height: var(--base-size-48); place-items: center start; padding: var(--base-size-8) var(--base-size-12); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .theme-picker { position: relative; display: inline-block; max-width: 100%; justify-self: end; }
    .theme-trigger { display: inline-grid; width: auto; max-width: 100%; min-height: var(--control-medium-size); grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-8); padding-inline: var(--base-size-8); text-align: left; }
    .theme-trigger:hover, .theme-trigger:focus-visible, .theme-trigger[aria-expanded="true"] { border-color: var(--lv-border-accent); outline: 0; }
    .theme-trigger-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .theme-trigger > svg { width: var(--base-size-16); height: var(--base-size-16); color: var(--lv-fg-muted); }
    .theme-preview { display: inline-grid; width: var(--base-size-40); height: var(--base-size-24); box-sizing: border-box; grid-template-columns: auto auto; place-content: center; align-items: center; gap: var(--base-size-2); overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: 0 var(--base-size-4); font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); line-height: 1; }
    .theme-preview[data-tone="light"] { color: var(--fgColor-black); background: var(--bgColor-white); }
    .theme-preview[data-tone="dark"] { color: var(--fgColor-white); background: var(--bgColor-black); }
    .theme-preview[data-tone="system"] { color: var(--fgColor-white); background: linear-gradient(135deg, var(--bgColor-white) 0 48%, var(--bgColor-black) 52% 100%); }
    .theme-preview-dot { width: var(--base-size-6); height: var(--base-size-6); border-radius: var(--lv-radius-full); background: var(--lv-bg-accent); }
    .theme-menu { position: absolute; z-index: var(--z-index-dropdown); top: calc(100% + var(--base-size-6)); right: 0; display: grid; width: min(22rem, calc(100vw - var(--base-size-32))); max-height: min(32rem, calc(100svh - var(--base-size-64))); overflow-y: auto; overscroll-behavior: contain; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); padding: var(--base-size-6); scrollbar-color: var(--lv-scrollbar-thumb) transparent; scrollbar-width: thin; }
    .theme-group { display: grid; gap: var(--base-size-2); padding: var(--base-size-4) 0; border-bottom: var(--lv-border-muted); }
    .theme-group:last-child { border-bottom: 0; }
    .theme-group-label { padding: var(--base-size-4) var(--base-size-8); color: var(--lv-fg-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-semibold); }
    button.theme-option { display: grid; width: 100%; min-height: var(--control-medium-size); grid-template-columns: auto minmax(0, 1fr) var(--base-size-16); align-items: center; gap: var(--base-size-8); border-color: transparent; background: transparent; padding-inline: var(--base-size-8); text-align: left; }
    button.theme-option:hover, button.theme-option:focus-visible, button.theme-option[aria-selected="true"] { background: var(--lv-bg-control-hover); outline: 0; }
    .theme-option-label { overflow-wrap: anywhere; }
    .theme-check { display: inline-grid; width: var(--base-size-16); height: var(--base-size-16); place-items: center; color: var(--lv-fg-accent); }
    .avatar-control { position: relative; display: inline-grid; justify-items: end; }
    .avatar-trigger { display: grid; width: var(--base-size-32); height: var(--base-size-32); min-height: var(--base-size-32); place-items: center; border: 0; border-radius: var(--lv-radius-full); padding: 0; background: transparent; }
    .avatar-trigger:hover { box-shadow: var(--lv-shadow-resting-sm); }
    .avatar-trigger:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .avatar-trigger lv-user-avatar { --lv-user-avatar-size: var(--base-size-32); pointer-events: none; }
    .avatar-input { display: none; }
    .avatar-menu { position: absolute; z-index: var(--z-index-dropdown); top: calc(100% + var(--base-size-6)); right: 0; display: grid; width: var(--overlay-width-xsmall); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); padding: var(--base-size-4); }
    .avatar-menu-item { display: grid; min-height: var(--control-medium-size); grid-template-columns: var(--base-size-16) minmax(0, 1fr); align-items: center; gap: var(--base-size-8); border: var(--lv-border-transparent); border-radius: var(--lv-radius-small); background: transparent; padding: 0 var(--base-size-8); text-align: left; }
    .avatar-menu-item:hover, .avatar-menu-item:focus-visible { background: var(--lv-bg-control-hover); outline: 0; }
    .avatar-menu-item svg { width: var(--base-size-16); height: var(--base-size-16); color: var(--lv-fg-muted); }
    .avatar-menu-item.danger, .avatar-menu-item.danger svg { color: var(--lv-fg-danger); }
    .notice { padding: var(--base-size-8) var(--base-size-12); border-radius: var(--lv-radius-small); background: var(--lv-bg-success-muted); color: var(--lv-fg-success); }
    .error { color: var(--lv-fg-danger); }
    @media (max-width: 40rem) {
      .row { grid-template-columns: 1fr; gap: var(--base-size-12); padding: var(--base-size-16); }
      .profile-email { max-width: none; justify-self: stretch; text-align: left; }
      .profile-name-form { width: 100%; justify-self: stretch; }
      .profile-name-control { justify-content: stretch; }
      .profile-name-control input { width: auto; flex: 1 1 auto; }
      .profile-local-input { width: 100%; justify-self: stretch; }
      .theme-picker { width: 100%; min-width: 0; justify-self: stretch; }
      .theme-trigger { width: 100%; }
      .theme-menu { right: auto; left: 0; }
      .avatar-control { justify-self: start; }
      .token-form-grid { grid-template-columns: 1fr; }
      .permissions-header { align-items: start; }
      .permission-backdrop { position: fixed; z-index: var(--z-index-dropdown); inset: 0; display: block; background: var(--lv-modal-backdrop); }
      .permission-menu { position: fixed; z-index: var(--z-index-modal); top: auto; right: var(--base-size-16); bottom: var(--base-size-16); left: var(--base-size-16); width: auto; max-height: calc(100svh - var(--base-size-32)); }
      .permission-menu-close { display: grid; }
    }
  `]

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
      this.handledNewToken = newToken
      this.tokenCreatePending = false
      this.tokenName = ''
      this.tokenCapabilities = []
      this.tokenExpires = ''
      this.closePermissionMenu()
    }
  }

  render() {
    const settings = this.settings
    if (!settings.profile.id) return html`<slot></slot>`
    const profileNameDraft = this.observedDisplayName === settings.profile.displayName ? this.profileName : settings.profile.displayName
    const profileNameDirty = profileNameDraft.trim() !== settings.profile.displayName
    const profileNameValid = profileNameDraft.trim().length > 0
    return html`
      <div class="settings" aria-label="Personal settings">
        ${this.renderNotice(settings)}
        ${settings.active === 'profile' ? html`<section aria-label="Profile">
          <div class="card profile-card">
            <div class="row profile-row">
              <div class="settings-field"><span class="settings-label">Profile picture</span><span class="settings-description">Shown across LeapView.</span></div>
              <div class="avatar-control">
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
              </div>
            </div>
            <div class="row profile-row"><div class="settings-field"><span class="settings-label">Email</span><span class="settings-description">Managed by your identity provider.</span></div><span class="settings-value profile-email">${settings.profile.email || 'Not set'}</span></div>
            <div class="row profile-row">
              <div class="settings-field"><label class="settings-label" for="personal-display-name">Display name</label><span class="settings-description">How your name appears to collaborators.</span></div>
              <form class="profile-name-form" @submit=${this.saveProfile}>
                <div class="profile-name-control">
                  <input id="personal-display-name" .value=${profileNameDraft} ?disabled=${!settings.profile.canEditDisplayName} @input=${this.onProfileNameInput}>
                  ${profileNameDirty ? html`<button class="primary" data-profile-save type="submit" ?disabled=${!settings.profile.canEditDisplayName || !profileNameValid}>Save</button>` : nothing}
                </div>
              </form>
            </div>
            <div class="row profile-row">
              <div class="settings-field"><label class="settings-label" for="personal-title">Title</label><span class="settings-description">Your job title or role.</span></div>
              <input id="personal-title" class="profile-local-input profile-title-input" maxlength="120" placeholder="Software engineer" .value=${this.profileTitle} @input=${this.onProfileTitleInput}>
            </div>
            <div class="row profile-row">
              <div class="settings-field"><label class="settings-label" for="personal-username">Username</label><span class="settings-description">One word, like a nickname or first name.</span></div>
              <input id="personal-username" class="profile-local-input profile-username-input" maxlength="64" autocomplete="off" .value=${this.profileUsername} @input=${this.onProfileUsernameInput}>
            </div>
            <div class="row profile-row">
              <div class="settings-field"><span class="settings-label" id="personal-theme-label">Theme</span><span class="settings-description">Choose how LeapView appears on your devices.</span></div>
              ${this.renderThemePicker(this.selectedTheme || settings.profile.theme || 'system')}
            </div>
          </div>
        </section>` : nothing}
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
    if (settings.tokens.newToken) return html`<p class="notice" role="status">Copy this token now; it will not be shown again: <code>${settings.tokens.newToken}</code></p>`
    if (this.error) return html`<p class="error" role="alert">${this.error}</p>`
    if (this.message) return html`<p class="notice" role="status">${this.message}</p>`
    return nothing
  }

  private renderSecurity(settings: PersonalSettingsSignal) {
    return html`
      <section aria-label="Security and sessions">
        ${settings.security.localPasswordEnabled && settings.profile.hasLocalPassword ? html`
          <div class="card"><div class="row"><div class="settings-field"><h3>Change password</h3><span class="settings-description">Use at least 12 characters and do not reuse the password elsewhere. Changing it signs out browser, desktop, CLI, and MCP sessions.</span></div></div><div class="row"><form @submit=${this.changePassword}><div class="form-grid"><input aria-label="Current password" type="password" autocomplete="current-password" maxlength="1024" placeholder="Current password" .value=${this.currentPassword} @input=${this.onCurrentPasswordInput} required><input aria-label="New password" type="password" autocomplete="new-password" minlength="12" maxlength="1024" placeholder="New password" .value=${this.newPassword} @input=${this.onNewPasswordInput} required></div><div class="actions"><button class="primary" type="submit">Change password</button></div></form></div></div>
        ` : html`<div class="card"><div class="row"><div class="settings-field"><h3>Local password</h3><span class="settings-description">Password changes are managed by your identity provider.</span></div></div></div>`}
        <div class="card"><div class="row"><div class="settings-field"><h3>Browser &amp; desktop sessions</h3><span class="settings-description">Revoke sessions you no longer recognize.</span></div></div>${settings.security.sessions.length ? settings.security.sessions.map((session) => this.renderSession(session)) : html`<div class="row"><span class="muted">No active sessions.</span></div>`}</div>
        ${settings.security.authoringSessions.length ? html`<div class="card"><div class="row"><div class="settings-field"><h3>CLI &amp; authoring sessions</h3><span class="settings-description">These sessions grant scoped access to authoring tools.</span></div></div>${settings.security.authoringSessions.map((session) => this.renderAuthoringSession(session))}</div>` : nothing}
      </section>
    `
  }

  private renderSession(session: PersonalSessionSignal) {
    return html`<div class="row"><div class="settings-field"><span class="settings-label">${session.clientLabel || session.kind}${session.current ? ' · This device' : ''}</span><span class="settings-description">Created ${formatDate(session.createdAt)} · Last seen ${formatDate(session.lastSeenAt)}${session.expiresAt ? ` · Expires ${formatDate(session.expiresAt)}` : ''}</span></div>${session.revokedAt ? html`<span class="muted">Revoked</span>` : html`<button class="danger" type="button" @click=${() => this.revokeSession(session.id)}>Revoke</button>`}</div>`
  }

  private renderAuthoringSession(session: PersonalAuthoringSessionSignal) {
    return html`<div class="row"><div class="settings-field"><span class="settings-label">${session.clientId || session.kind}</span><span class="settings-description">${session.projectId || 'No project'} · ${session.capabilities.join(', ') || 'Scoped access'} · Created ${formatDate(session.createdAt)}</span></div>${session.revokedAt ? html`<span class="muted">Revoked</span>` : html`<button class="danger" type="button" @click=${() => this.revokeAuthoringSession(session.id)}>Revoke</button>`}</div>`
  }

  private renderTokens(tokens: PersonalSettingsSignal['tokens']) {
    const selected = tokens.capabilities.filter((capability) => this.tokenCapabilities.includes(capability.value))
    const categories = groupTokenCapabilities(this.filteredTokenCapabilities(tokens.capabilities))
    const categoryStats = new Map(groupTokenCapabilities(tokens.capabilities).map(([category, capabilities]) => [category, {
      selected: capabilities.filter((capability) => this.tokenCapabilities.includes(capability.value)).length,
      total: capabilities.length,
    }]))
    const canCreate = Boolean(this.tokenName.trim() && !this.tokenCreatePending)
    return html`
      <section aria-label="API tokens">
        <div class="card token-card">
          <div class="row"><div class="settings-field"><h3>Create a personal API token</h3><span class="settings-description">Create a token for this project with only the permissions your script needs.</span></div></div>
          <div class="row">
            <form class="token-form" @submit=${this.createToken}>
              <div class="token-form-grid">
                <label class="token-field" for="token-name">
                  <span class="settings-label">Token name</span>
                  <input id="token-name" required autocomplete="off" placeholder="For example, Sales reporting" .value=${this.tokenName} @input=${this.onTokenNameInput}>
                  <span class="settings-description">A recognizable name for this credential.</span>
                </label>
                <label class="token-field" for="token-expiry">
                  <span class="settings-label">Expiration</span>
                  <input id="token-expiry" type="datetime-local" .value=${this.tokenExpires} @input=${this.onTokenExpiresInput}>
                  <span class="settings-description">Defaults to the product token lifetime.</span>
                </label>
              </div>
              <div class="permissions">
                <div class="permissions-header">
                  <div class="settings-field">
                    <div class="permissions-title"><span class="settings-label">Permissions</span><span class="count" aria-label="${selected.length} selected permissions">${selected.length}</span></div>
                    <span class="settings-description">Choose the minimal permissions necessary for your needs.</span>
                  </div>
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
                              <span class="permission-menu-count" aria-live="polite">${this.tokenCapabilities.length} selected</span>
                            </div>
                            <button class="permission-menu-close" type="button" aria-label="Close permission picker" @click=${() => this.closePermissionMenu(true)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button>
                          </div>
                          <label class="permission-search">
                            ${lucideIcon(Search, { size: 16, strokeWidth: 2 })}
                            <input type="search" aria-label="Search permissions" placeholder="Search permissions" .value=${this.tokenPermissionSearch} @input=${this.onTokenPermissionSearch}>
                          </label>
                        </div>
                        <div class="permission-list">
                          ${categories.length ? categories.map(([category, capabilities], categoryIndex) => html`
                            <div class="permission-group" role="group" aria-labelledby=${`token-permission-category-${categoryIndex}`}>
                              <div class="permission-category" id=${`token-permission-category-${categoryIndex}`}>
                                <span>${category}</span>
                                <span class="permission-category-count">${categoryStats.get(category)?.selected ?? 0} / ${categoryStats.get(category)?.total ?? capabilities.length}</span>
                              </div>
                              ${capabilities.map((capability) => {
                                const selected = this.tokenCapabilities.includes(capability.value)
                                const descriptionID = `token-permission-description-${capability.value}`
                                return html`
                                  <label class="permission-option" data-selected=${String(selected)}>
                                    <input aria-label=${capability.label} aria-describedby=${descriptionID} type="checkbox" value=${capability.value} .checked=${selected} @change=${() => this.toggleTokenCapability(capability.value)}>
                                    <span class="settings-field"><span class="settings-label">${capability.label}</span><span class="settings-description" id=${descriptionID}>${capability.description}</span></span>
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
                  ${selected.length ? selected.map((capability) => html`
                    <div class="selected-permission">
                      <div class="settings-field"><span class="settings-label">${capability.label}</span><span class="settings-description">${capability.description}</span></div>
                      <button class="permission-remove" type="button" aria-label="Remove ${capability.label}" @click=${() => this.removeTokenCapability(capability.value)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button>
                    </div>
                  `) : html`<div class="permission-empty">No explicit permissions selected. The token will dynamically follow your current access.</div>`}
                </div>
              </div>
              <div class="actions"><button class="primary" type="submit" ?disabled=${!canCreate}>${this.tokenCreatePending ? 'Creating…' : 'Create token'}</button></div>
            </form>
          </div>
        </div>
        <div class="card"><div class="row"><div class="settings-field"><h3>Personal API tokens</h3><span class="settings-description">Revoke credentials you no longer use.</span></div></div>${tokens.items.length ? tokens.items.map((token) => this.renderToken(token, tokens.capabilities)) : html`<div class="row"><span class="muted">No personal API tokens.</span></div>`}</div>
      </section>
    `
  }

  private renderToken(token: PersonalTokenSignal, capabilities: PersonalCapabilityOptionSignal[]) {
    const options = new Map(capabilities.map((capability) => [capability.value, capability.label]))
    const labels = token.capabilities.map((capability) => options.get(capability) ?? humanizeCapability(capability))
    return html`<div class="row"><div class="settings-field"><span class="settings-label">${token.name}</span><span class="settings-description">${labels.join(', ') || 'Dynamically follows current access'} · Created ${formatDate(token.createdAt)}${token.expiresAt ? ` · Expires ${formatDate(token.expiresAt)}` : ''}</span></div>${token.revokedAt ? html`<span class="muted">Revoked</span>` : html`<button class="danger" type="button" @click=${() => this.revokeToken(token.id)}>Revoke</button>`}</div>`
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
    void this.focusSelectedThemeOption()
  }
  private handleThemeTriggerKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    this.themeMenuOpen = true
    this.closeAvatarMenu()
    this.closePermissionMenu()
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
  private changePassword = (event: Event): void => { event.preventDefault(); this.send('lv-personal-password-command', { currentPassword: this.currentPassword, newPassword: this.newPassword }); this.currentPassword = ''; this.newPassword = '' }
  private createToken = (event: Event): void => {
    event.preventDefault()
    if (!this.tokenName.trim()) return
    const command: Record<string, unknown> = { action: 'create', name: this.tokenName.trim(), expiresAt: localDateTimeToRFC3339(this.tokenExpires) }
    if (this.tokenCapabilities.length) command.capabilities = [...this.tokenCapabilities]
    this.send('lv-personal-token-command', command)
    this.tokenCreatePending = true
  }
  private revokeToken = (tokenId: string): void => { this.send('lv-personal-token-command', { action: 'revoke', tokenId }) }
  private revokeSession = (sessionId: string): void => { this.send('lv-personal-session-command', { action: 'revoke', sessionId }) }
  private revokeAuthoringSession = (sessionId: string): void => { this.send('lv-personal-authoring-session-command', { action: 'revoke', sessionId }) }
  private toggleAvatarMenu = (): void => {
    this.avatarMenuOpen = !this.avatarMenuOpen
    if (this.avatarMenuOpen) {
      this.closeThemeMenu()
      this.closePermissionMenu()
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
      if (!response.ok) throw new Error('Avatar upload failed')
      const uploaded = await response.json() as { url?: string }
      document.dispatchEvent(new CustomEvent('leapview-avatar-change', { detail: { url: uploaded.url ?? '' } }))
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
      if (!response.ok) throw new Error('Avatar removal failed')
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
  private onTokenExpiresInput = (event: Event): void => { this.tokenExpires = (event.currentTarget as HTMLInputElement).value }
  private filteredTokenCapabilities(capabilities: PersonalCapabilityOptionSignal[]): PersonalCapabilityOptionSignal[] {
    const query = this.tokenPermissionSearch.trim().toLocaleLowerCase()
    if (!query) return capabilities
    return capabilities.filter((capability) => `${capability.label} ${capability.description} ${capability.category} ${capability.value}`.toLocaleLowerCase().includes(query))
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
  private toggleTokenCapability(value: string): void {
    this.tokenCapabilities = this.tokenCapabilities.includes(value)
      ? this.tokenCapabilities.filter((capability) => capability !== value)
      : [...this.tokenCapabilities, value]
  }
  private removeTokenCapability(value: string): void { this.tokenCapabilities = this.tokenCapabilities.filter((capability) => capability !== value) }
}

function groupTokenCapabilities(capabilities: PersonalCapabilityOptionSignal[]): Array<[string, PersonalCapabilityOptionSignal[]]> {
  const groups = new Map<string, PersonalCapabilityOptionSignal[]>()
  for (const capability of capabilities) {
    const values = groups.get(capability.category) ?? []
    values.push(capability)
    groups.set(capability.category, values)
  }
  return [...groups.entries()]
}

function themeOption(value: string): ThemeOption {
  return themeOptions.find((option) => option.value === value) ?? systemThemeOption
}

function usernameFromEmail(email: string): string {
  const localPart = email.split('@', 1)[0]?.trim().toLocaleLowerCase() ?? ''
  return localPart.replace(/[^a-z0-9._-]+/g, '.').replace(/^[._-]+|[._-]+$/g, '') || 'user'
}

function humanizeCapability(value: string): string {
  return value.toLocaleLowerCase().split('_').filter(Boolean).map((part, index) => index === 0 ? `${part.charAt(0).toLocaleUpperCase()}${part.slice(1)}` : part).join(' ')
}

function formatDate(value: string): string {
  if (!value) return 'unknown'
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString()
}

function localDateTimeToRFC3339(value: string): string {
  if (!value) return ''
  const parsed = new Date(value)
  return Number.isNaN(parsed.valueOf()) ? '' : parsed.toISOString()
}

customElements.define('lv-personal-settings', LeapViewPersonalSettings)

export { LeapViewPersonalSettings }
