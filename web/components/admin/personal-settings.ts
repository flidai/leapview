import { LitElement, css, html, nothing } from 'lit'
import { query, state } from 'lit/decorators.js'
import { Camera, Plus, Search, Trash2, X } from 'lucide'
import type {
  PersonalAuthoringSessionSignal,
  PersonalSessionSignal,
  PersonalSettingsSignal,
  PersonalTokenPrivilegeSignal,
  PersonalTokenScopeSignal,
  PersonalTokenSignal,
} from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { lucideIcon } from '../shared/lucide-icons'
import { settingsFieldStyles } from '../shared/settings-field-styles'
import '../shared/user-avatar'

const emptySettings: PersonalSettingsSignal = {
  active: 'profile',
  profile: { id: '', email: '', displayName: '', theme: 'system', identitySource: '', canEditDisplayName: false, hasLocalPassword: false },
  security: { localPasswordEnabled: false, sessions: [], authoringSessions: [] },
  tokens: { items: [], scopes: [] },
}

class LeapViewPersonalSettings extends DatastarLit(LitElement) {
  @state() private profileName = ''
  @state() private currentPassword = ''
  @state() private newPassword = ''
  @state() private tokenName = ''
  @state() private tokenScopeKey = ''
  @state() private tokenPrivileges: string[] = []
  @state() private tokenExpires = ''
  @state() private tokenCreatePending = false
  @state() private tokenPermissionMenuOpen = false
  @state() private tokenPermissionSearch = ''
  @state() private message = ''
  @state() private error = ''
  @state() private avatarMenuOpen = false
  @state() private avatarBusy = false
  @query('.avatar-trigger') private avatarTrigger?: HTMLButtonElement
  @query('.avatar-input') private avatarInput?: HTMLInputElement
  @query('.permission-trigger') private permissionTrigger?: HTMLButtonElement
  private handledNewToken = ''

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
    .permission-trigger svg, .permission-remove svg, .permission-search svg { width: var(--base-size-16); height: var(--base-size-16); }
    .permission-backdrop { display: none; }
    .permission-menu { position: absolute; z-index: var(--z-index-dropdown); top: calc(100% + var(--base-size-6)); right: 0; display: grid; width: min(var(--overlay-width-medium), calc(100vw - var(--base-size-32))); max-height: min(30rem, var(--permission-menu-max-height, calc(100svh - var(--base-size-64)))); box-sizing: border-box; grid-template-rows: auto minmax(0, 1fr); overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); }
    .permission-menu-header { display: grid; gap: var(--base-size-8); padding: var(--base-size-12); border-bottom: var(--lv-border-muted); }
    .permission-menu-title { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-8); }
    .permission-menu-close { display: none; width: var(--control-small-size); min-height: var(--control-small-size); place-items: center; padding: 0; color: var(--lv-fg-muted); background: transparent; }
    .permission-search { position: relative; display: grid; align-items: center; }
    .permission-search svg { position: absolute; left: var(--base-size-12); z-index: 1; color: var(--lv-fg-muted); pointer-events: none; }
    .permission-search input { width: 100%; padding-left: var(--base-size-40); }
    .permission-list { display: grid; min-height: 0; align-content: start; overflow-y: auto; overscroll-behavior: contain; padding: var(--base-size-4); scrollbar-color: var(--lv-scrollbar-thumb) transparent; scrollbar-width: thin; }
    .permission-category { padding: var(--base-size-8) var(--base-size-8) var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-semibold); }
    .permission-option { display: grid; min-width: 0; grid-template-columns: var(--base-size-20) minmax(0, 1fr); align-items: start; gap: var(--base-size-8); border-radius: var(--lv-radius-small); padding: var(--base-size-8); cursor: pointer; }
    .permission-option:hover { background: var(--lv-bg-control-hover); }
    .permission-option input[type="checkbox"] { width: var(--base-size-16); height: var(--base-size-16); min-height: 0; margin: var(--base-size-2) 0 0; padding: 0; accent-color: var(--lv-bg-accent); }
    .selected-permissions { display: grid; overflow: hidden; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); }
    .selected-permission { display: grid; min-height: var(--base-size-48); grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-12); padding: var(--base-size-8) var(--base-size-12); border-bottom: var(--lv-border-muted); }
    .selected-permission:last-child { border-bottom: 0; }
    .permission-remove { display: grid; width: var(--control-small-size); min-height: var(--control-small-size); place-items: center; padding: 0; color: var(--lv-fg-muted); background: transparent; }
    .permission-empty { display: grid; min-height: var(--base-size-48); place-items: center start; padding: var(--base-size-8) var(--base-size-12); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .avatar-control { position: relative; display: inline-grid; justify-items: end; }
    .avatar-trigger { display: grid; width: var(--control-large-size); height: var(--control-large-size); min-height: var(--control-large-size); place-items: center; border: 0; border-radius: var(--lv-radius-full); padding: 0; background: transparent; }
    .avatar-trigger:hover { box-shadow: var(--lv-shadow-resting-sm); }
    .avatar-trigger:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .avatar-trigger lv-user-avatar { pointer-events: none; }
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
    const displayName = settings.profile.displayName
    if (!this.profileName && displayName) this.profileName = displayName
    const newToken = settings.tokens.newToken ?? ''
    if (newToken && newToken !== this.handledNewToken) {
      this.handledNewToken = newToken
      this.tokenCreatePending = false
      this.tokenName = ''
      this.tokenScopeKey = ''
      this.tokenPrivileges = []
      this.tokenExpires = ''
      this.closePermissionMenu()
    }
  }

  render() {
    const settings = this.settings
    if (!settings.profile.id) return html`<slot></slot>`
    return html`
      <div class="settings" aria-label="Personal settings">
        ${this.renderNotice(settings)}
        ${settings.active === 'profile' ? html`<section aria-label="Profile">
          <div class="card">
            <div class="row">
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
                    size="medium"
                    .name=${settings.profile.displayName || settings.profile.email}
                    .imageUrl=${settings.profile.avatarUrl ?? ''}
                    aria-hidden="true"
                  ></lv-user-avatar>
                </button>
                <input class="avatar-input" type="file" accept="image/png,image/jpeg,image/webp" tabindex="-1" @change=${this.uploadAvatar}>
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
            <div class="row"><div class="settings-field"><span class="settings-label">Email</span><span class="settings-description">Managed by your identity provider.</span></div><span class="settings-value">${settings.profile.email || 'Not set'}</span></div>
            <div class="row">
              <div class="settings-field"><label class="settings-label" for="personal-display-name">Display name</label><span class="settings-description">How your name appears to collaborators.</span></div>
              <form @submit=${this.saveProfile}><div class="actions"><input id="personal-display-name" .value=${this.profileName || settings.profile.displayName} ?disabled=${!settings.profile.canEditDisplayName} @input=${this.onProfileNameInput}><button class="primary" type="submit" ?disabled=${!settings.profile.canEditDisplayName}>Save</button></div></form>
            </div>
            <div class="row">
              <div class="settings-field"><label class="settings-label" for="personal-theme">Theme</label><span class="settings-description">Choose how LeapView appears on your devices.</span></div>
              <select id="personal-theme" name="theme" .value=${settings.profile.theme || 'system'} @change=${this.changeTheme}>
                <optgroup label="Automatic">
                  <option value="system">System</option>
                </optgroup>
                <optgroup label="Standard">
                  <option value="light">Light default</option>
                  <option value="dark">Dark default</option>
                  <option value="dark_dimmed">Soft dark</option>
                </optgroup>
                <optgroup label="Accessibility">
                  <option value="light_colorblind">Light protanopia and deuteranopia</option>
                  <option value="dark_colorblind">Dark protanopia and deuteranopia</option>
                  <option value="light_tritanopia">Light tritanopia</option>
                  <option value="dark_tritanopia">Dark tritanopia</option>
                </optgroup>
              </select>
            </div>
          </div>
        </section>` : nothing}
        ${settings.active === 'security' ? this.renderSecurity(settings) : nothing}
        ${settings.active === 'api-tokens' ? this.renderTokens(settings.tokens) : nothing}
      </div>
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
          <div class="card"><div class="row"><div class="settings-field"><h3>Change password</h3><span class="settings-description">Use a strong password you do not reuse elsewhere.</span></div></div><div class="row"><form @submit=${this.changePassword}><div class="form-grid"><input type="password" autocomplete="current-password" placeholder="Current password" .value=${this.currentPassword} @input=${this.onCurrentPasswordInput}><input type="password" autocomplete="new-password" placeholder="New password" .value=${this.newPassword} @input=${this.onNewPasswordInput}></div><div class="actions"><button class="primary" type="submit">Change password</button></div></form></div></div>
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
    return html`<div class="row"><div class="settings-field"><span class="settings-label">${session.clientId || session.kind}</span><span class="settings-description">${session.projectId || 'No project'} · ${session.privileges.join(', ') || 'Scoped access'} · Created ${formatDate(session.createdAt)}</span></div>${session.revokedAt ? html`<span class="muted">Revoked</span>` : html`<button class="danger" type="button" @click=${() => this.revokeAuthoringSession(session.id)}>Revoke</button>`}</div>`
  }

  private renderTokens(tokens: PersonalSettingsSignal['tokens']) {
    const scope = this.selectedTokenScope(tokens)
    const selected = scope?.privileges.filter((privilege) => this.tokenPrivileges.includes(privilege.value)) ?? []
    const filtered = this.filteredTokenPrivileges(scope)
    const categories = groupTokenPrivileges(filtered)
    const platformScopes = tokens.scopes.filter((option) => option.kind === 'platform')
    const workspaceScopes = tokens.scopes.filter((option) => option.kind !== 'platform')
    const canCreate = Boolean(this.tokenName.trim() && scope && selected.length && !this.tokenCreatePending)
    return html`
      <section aria-label="API tokens">
        <div class="card token-card">
          <div class="row"><div class="settings-field"><h3>Create a personal API token</h3><span class="settings-description">Choose only the workspace and permissions your script needs.</span></div></div>
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
              <label class="token-field" for="token-scope">
                <span class="settings-label">Resource access</span>
                <select id="token-scope" required .value=${this.tokenScopeKey} @change=${this.onTokenScopeChange}>
                  <option value="">Choose a scope</option>
                  ${platformScopes.length ? html`<optgroup label="Broad access">${platformScopes.map((option) => html`<option value=${tokenScopeKey(option)}>${option.label}</option>`)}</optgroup>` : nothing}
                  ${workspaceScopes.length ? html`<optgroup label="Workspace access">${workspaceScopes.map((option) => html`<option value=${tokenScopeKey(option)}>${option.label}</option>`)}</optgroup>` : nothing}
                </select>
                <span class="settings-description scope-description">${scope?.description ?? 'The token will be limited to one workspace or product-administration scope.'}</span>
              </label>
              <div class="permissions">
                <div class="permissions-header">
                  <div class="settings-field">
                    <div class="permissions-title"><span class="settings-label">Permissions</span><span class="count" aria-label="${selected.length} selected permissions">${selected.length}</span></div>
                    <span class="settings-description">Choose the minimal permissions necessary for your needs.</span>
                  </div>
                  <div class="permission-picker">
                    <button class="permission-trigger" type="button" aria-haspopup="dialog" aria-controls="token-permission-menu" aria-expanded=${String(this.tokenPermissionMenuOpen)} ?disabled=${!scope} @click=${this.togglePermissionMenu}>
                      ${lucideIcon(Plus, { size: 16, strokeWidth: 2 })}<span>Add permissions</span>
                    </button>
                    ${this.tokenPermissionMenuOpen && scope ? html`
                      <div class="permission-backdrop" aria-hidden="true" @click=${() => this.closePermissionMenu(true)}></div>
                      <div id="token-permission-menu" class="permission-menu" role="dialog" aria-label="Add token permissions">
                        <div class="permission-menu-header">
                          <div class="permission-menu-title"><span class="settings-label">Select permissions</span><button class="permission-menu-close" type="button" aria-label="Close permission picker" @click=${() => this.closePermissionMenu(true)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button></div>
                          <label class="permission-search">
                            ${lucideIcon(Search, { size: 16, strokeWidth: 2 })}
                            <input type="search" aria-label="Search permissions" placeholder="Search permissions" .value=${this.tokenPermissionSearch} @input=${this.onTokenPermissionSearch}>
                          </label>
                        </div>
                        <div class="permission-list">
                          ${categories.length ? categories.map(([category, privileges]) => html`
                            <div class="permission-category">${category}</div>
                            ${privileges.map((privilege) => html`
                              <label class="permission-option">
                                <input type="checkbox" value=${privilege.value} .checked=${this.tokenPrivileges.includes(privilege.value)} @change=${() => this.toggleTokenPrivilege(privilege.value)}>
                                <span class="settings-field"><span class="settings-label">${privilege.label}</span><span class="settings-description">${privilege.description}</span></span>
                              </label>
                            `)}
                          `) : html`<div class="permission-empty">No permissions match your search.</div>`}
                        </div>
                      </div>
                    ` : nothing}
                  </div>
                </div>
                <div class="selected-permissions" aria-live="polite">
                  ${selected.length ? selected.map((privilege) => html`
                    <div class="selected-permission">
                      <div class="settings-field"><span class="settings-label">${privilege.label}</span><span class="settings-description">${privilege.description}</span></div>
                      <button class="permission-remove" type="button" aria-label="Remove ${privilege.label}" @click=${() => this.removeTokenPrivilege(privilege.value)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button>
                    </div>
                  `) : html`<div class="permission-empty">${scope ? 'No permissions selected.' : 'Choose resource access before adding permissions.'}</div>`}
                </div>
              </div>
              <div class="actions"><button class="primary" type="submit" ?disabled=${!canCreate}>${this.tokenCreatePending ? 'Creating…' : 'Create token'}</button></div>
            </form>
          </div>
        </div>
        <div class="card"><div class="row"><div class="settings-field"><h3>Personal API tokens</h3><span class="settings-description">Revoke credentials you no longer use.</span></div></div>${tokens.items.length ? tokens.items.map((token) => this.renderToken(token, tokens.scopes)) : html`<div class="row"><span class="muted">No personal API tokens.</span></div>`}</div>
      </section>
    `
  }

  private renderToken(token: PersonalTokenSignal, scopes: PersonalTokenScopeSignal[]) {
    const scope = scopes.find((option) => option.workspaceId === token.workspaceId)
    const options = new Map(scope?.privileges.map((privilege) => [privilege.value, privilege.label]) ?? [])
    const privileges = token.privileges.map((privilege) => options.get(privilege) ?? humanizePrivilege(privilege))
    return html`<div class="row"><div class="settings-field"><span class="settings-label">${token.name}</span><span class="settings-description">${scope?.label || token.workspaceId || 'Legacy unrestricted scope'} · ${privileges.join(', ') || 'Inherited effective privileges'} · Created ${formatDate(token.createdAt)}${token.expiresAt ? ` · Expires ${formatDate(token.expiresAt)}` : ''}</span></div>${token.revokedAt ? html`<span class="muted">Revoked</span>` : html`<button class="danger" type="button" @click=${() => this.revokeToken(token.id)}>Revoke</button>`}</div>`
  }

  private saveProfile = (event: Event): void => { event.preventDefault(); this.send('lv-personal-profile-command', { action: 'save', displayName: this.profileName.trim() }) }
  private changeTheme = (event: Event): void => {
    const theme = (event.currentTarget as HTMLSelectElement).value
    document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode: theme } }))
    this.send('lv-personal-theme-command', { action: 'save', theme })
  }
  private changePassword = (event: Event): void => { event.preventDefault(); this.send('lv-personal-password-command', { currentPassword: this.currentPassword, newPassword: this.newPassword }); this.currentPassword = ''; this.newPassword = '' }
  private createToken = (event: Event): void => {
    event.preventDefault()
    const scope = this.selectedTokenScope(this.settings.tokens)
    if (!this.tokenName.trim() || !scope || !this.tokenPrivileges.length) return
    this.send('lv-personal-token-command', {
      action: 'create', name: this.tokenName.trim(), workspaceId: scope.workspaceId,
      privileges: [...this.tokenPrivileges], expiresAt: localDateTimeToRFC3339(this.tokenExpires),
    })
    this.tokenCreatePending = true
  }
  private revokeToken = (tokenId: string): void => { this.send('lv-personal-token-command', { action: 'revoke', tokenId }) }
  private revokeSession = (sessionId: string): void => { this.send('lv-personal-session-command', { action: 'revoke', sessionId }) }
  private revokeAuthoringSession = (sessionId: string): void => { this.send('lv-personal-authoring-session-command', { action: 'revoke', sessionId }) }
  private toggleAvatarMenu = (): void => {
    this.avatarMenuOpen = !this.avatarMenuOpen
    if (this.avatarMenuOpen) void this.focusFirstAvatarMenuItem()
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
  }
  private handleDatastarFetch = (event: Event): void => {
    if (!this.tokenCreatePending) return
    const detail = (event as CustomEvent<{ type?: string, argsRaw?: { status?: string } }>).detail
    if (detail?.type !== 'error' && detail?.type !== 'retries-failed') return
    this.tokenCreatePending = false
    this.error = detail.argsRaw?.status === '403'
      ? 'Token creation failed because this page expired. Reload the page and try again.'
      : 'Token creation failed. Your selections were kept; please try again.'
  }
  private uploadAvatar = async (event: Event): Promise<void> => {
    const input = event.currentTarget as HTMLInputElement
    const file = input.files?.[0]
    if (!file) return
    this.avatarBusy = true
    this.error = ''
    try {
      const response = await fetch('/profile/avatar', { method: 'PUT', headers: { ...window.LeapViewCommand.headers(), 'Content-Type': file.type }, body: file })
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
      const response = await fetch('/profile/avatar', { method: 'DELETE', headers: window.LeapViewCommand.headers() })
      if (!response.ok) throw new Error('Avatar removal failed')
      document.dispatchEvent(new CustomEvent('leapview-avatar-change', { detail: { url: '' } }))
      this.send('lv-personal-profile-command', { action: 'refresh', displayName: this.profileName })
      this.message = 'Profile picture removed.'
    } catch (error) { this.error = error instanceof Error ? error.message : 'Avatar removal failed' } finally { this.avatarBusy = false }
  }
  private send(name: string, detail: Record<string, unknown>): void { this.error = ''; this.message = ''; this.dispatchEvent(new CustomEvent(name, { bubbles: true, composed: true, detail })) }
  private onProfileNameInput = (event: Event): void => { this.profileName = (event.currentTarget as HTMLInputElement).value }
  private onCurrentPasswordInput = (event: Event): void => { this.currentPassword = (event.currentTarget as HTMLInputElement).value }
  private onNewPasswordInput = (event: Event): void => { this.newPassword = (event.currentTarget as HTMLInputElement).value }
  private onTokenNameInput = (event: Event): void => { this.tokenName = (event.currentTarget as HTMLInputElement).value }
  private onTokenExpiresInput = (event: Event): void => { this.tokenExpires = (event.currentTarget as HTMLInputElement).value }
  private onTokenScopeChange = (event: Event): void => {
    this.tokenScopeKey = (event.currentTarget as HTMLSelectElement).value
    this.tokenPrivileges = []
    this.closePermissionMenu()
  }
  private selectedTokenScope(tokens: PersonalSettingsSignal['tokens']): PersonalTokenScopeSignal | undefined {
    return tokens.scopes.find((scope) => tokenScopeKey(scope) === this.tokenScopeKey)
  }
  private filteredTokenPrivileges(scope?: PersonalTokenScopeSignal): PersonalTokenPrivilegeSignal[] {
    if (!scope) return []
    const query = this.tokenPermissionSearch.trim().toLocaleLowerCase()
    if (!query) return scope.privileges
    return scope.privileges.filter((privilege) => `${privilege.label} ${privilege.description} ${privilege.category} ${privilege.value}`.toLocaleLowerCase().includes(query))
  }
  private togglePermissionMenu = (): void => {
    this.tokenPermissionMenuOpen = !this.tokenPermissionMenuOpen
    if (this.tokenPermissionMenuOpen) {
      void this.updateComplete.then(() => {
        this.fitPermissionMenu()
        this.renderRoot.querySelector<HTMLInputElement>('.permission-search input')?.focus()
      })
    }
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
  private onTokenPermissionSearch = (event: Event): void => { this.tokenPermissionSearch = (event.currentTarget as HTMLInputElement).value }
  private toggleTokenPrivilege(value: string): void {
    this.tokenPrivileges = this.tokenPrivileges.includes(value)
      ? this.tokenPrivileges.filter((privilege) => privilege !== value)
      : [...this.tokenPrivileges, value]
  }
  private removeTokenPrivilege(value: string): void { this.tokenPrivileges = this.tokenPrivileges.filter((privilege) => privilege !== value) }
}

function tokenScopeKey(scope: PersonalTokenScopeSignal): string {
  return scope.kind === 'platform' ? 'platform' : `workspace:${scope.workspaceId}`
}

function groupTokenPrivileges(privileges: PersonalTokenPrivilegeSignal[]): Array<[string, PersonalTokenPrivilegeSignal[]]> {
  const groups = new Map<string, PersonalTokenPrivilegeSignal[]>()
  for (const privilege of privileges) {
    const values = groups.get(privilege.category) ?? []
    values.push(privilege)
    groups.set(privilege.category, values)
  }
  return [...groups.entries()]
}

function humanizePrivilege(value: string): string {
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
