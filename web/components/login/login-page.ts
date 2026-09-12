import { LitElement, css, html } from 'lit'
import { Eye, EyeOff, Monitor, Moon, Sun } from 'lucide'
import { ifDefined } from 'lit/directives/if-defined.js'
import type { DashboardStatus, LoginPageSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { leapViewBrandName } from '../shared/brand-mark'
import { checkSignalContract } from '../shared/signal-contract'
import { emptyDashboardStatus } from '../shared/signal-defaults'
import { lucideIcon } from '../shared/lucide-icons'

type ThemeMode = 'system' | 'light' | 'dark'
type ThemePreference = ThemeMode | 'dark_dimmed' | 'light_colorblind' | 'dark_colorblind' | 'light_tritanopia' | 'dark_tritanopia'

const nextThemeMode: Record<ThemePreference, ThemeMode> = {
  system: 'light',
  light: 'dark',
  dark: 'system',
  dark_dimmed: 'system',
  light_colorblind: 'system',
  dark_colorblind: 'system',
  light_tritanopia: 'system',
  dark_tritanopia: 'system',
}

const themeLabels: Record<ThemePreference, string> = {
  system: 'System theme',
  light: 'Light default',
  dark: 'Dark default',
  dark_dimmed: 'Soft dark',
  light_colorblind: 'Light protanopia and deuteranopia',
  dark_colorblind: 'Dark protanopia and deuteranopia',
  light_tritanopia: 'Light tritanopia',
  dark_tritanopia: 'Dark tritanopia',
}

class LeapViewLoginPage extends DatastarLit(LitElement) {
  private readonly revealedPasswords = new Set<string>()
  private themeMode: ThemePreference = currentThemeMode()
  private readonly handleThemeApplied = (event: Event) => {
    const detail = (event as CustomEvent<{ mode?: string }>).detail
    this.themeMode = normalizeThemeMode(detail?.mode)
    this.requestUpdate()
  }

  static styles = css`
    :host { display: block; min-width: 0; }

    .layout {
      position: relative;
      display: grid;
      width: 100%;
      min-height: 100svh;
      place-items: center;
      overflow: clip;
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      padding: var(--base-size-64) var(--base-size-24);
      box-sizing: border-box;
    }

    lv-topology-background,
    .scrim {
      position: absolute;
      inset: 0;
    }

    lv-topology-background {
      display: block;
      background: var(--lv-bg-app);
    }

    .scrim {
      pointer-events: none;
      z-index: var(--zIndex-overlay, 20);
      background: var(--overlay-backdrop-bgColor);
    }

    .theme {
      position: absolute;
      top: var(--base-size-24);
      right: var(--base-size-24);
      z-index: var(--zIndex-modal, 30);
      display: inline-grid;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
      cursor: pointer;
      box-shadow: var(--shadow-resting-small);
    }

    .theme:hover,
    .theme:focus-visible {
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
      outline: var(--focus-outline);
      outline-offset: var(--base-size-4);
    }

    .theme [hidden] {
      display: none;
    }

    .panel {
      position: relative;
      z-index: var(--zIndex-modal, 30);
      display: grid;
      width: min(100%, calc(var(--lv-login-panel-width) + var(--base-size-32)));
      align-self: center;
      justify-items: stretch;
      gap: var(--base-size-20);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--shadow-resting-medium);
      padding: var(--base-size-32);
      text-align: left;
      box-sizing: border-box;
    }

    h1 {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-page-title);
    }

    .brand-lockup {
      position: absolute;
      top: var(--base-size-24);
      left: var(--base-size-24);
      z-index: var(--zIndex-modal, 30);
      display: flex;
      align-items: center;
      gap: var(--base-size-12);
    }

    .brand-lockup lv-brand-mark {
      --lv-brand-mark-size: calc(var(--base-size-32) + var(--base-size-4));
    }

    .provider {
      display: inline-grid;
      min-height: var(--control-xlarge-size);
      width: 100%;
      grid-template-columns: auto minmax(0, 1fr);
      align-items: center;
      gap: var(--base-size-12);
      border: var(--borderWidth-default) solid var(--lv-button-border-rest);
      border-radius: var(--lv-button-radius);
      background: var(--lv-button-bg-rest);
      color: var(--lv-button-fg-rest);
      cursor: pointer;
      padding: 0 var(--lv-button-padding-inline-spacious);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      box-shadow: var(--lv-button-shadow-resting);
      text-decoration: none;
      box-sizing: border-box;
    }

    .provider:hover,
    .provider:focus-visible {
      border-color: var(--lv-button-border-hover);
      background: var(--lv-button-bg-hover);
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
      outline-offset: var(--focus-outline-offset, var(--base-size-2));
    }

    .provider-mark {
      display: grid;
      width: var(--base-size-20);
      height: var(--base-size-20);
      grid-template-columns: 1fr 1fr;
      grid-template-rows: 1fr 1fr;
      gap: 1px;
    }

    .provider-mark span:nth-child(1) { background: var(--lv-fg-danger); }
    .provider-mark span:nth-child(2) { background: var(--lv-fg-success); }
    .provider-mark span:nth-child(3) { background: var(--lv-accent); }
    .provider-mark span:nth-child(4) { background: var(--lv-fg-warning); }

    form {
      display: grid;
      width: 100%;
      gap: var(--base-size-12);
    }

    label {
      display: grid;
      gap: var(--base-size-6);
      text-align: left;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .field-hint {
      color: var(--lv-fg-subtle);
      font: var(--lv-type-caption);
    }

    input {
      width: 100%;
      min-height: var(--control-large-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-12);
      font: var(--lv-type-body);
      box-sizing: border-box;
    }

    input:focus {
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
      outline-offset: var(--focus-outline-offset, var(--base-size-2));
    }

    .password-field { display: grid; gap: var(--base-size-6); }
    .password-control { position: relative; }
    .password-control input { padding-right: var(--base-size-48); }
    .password-toggle {
      position: absolute;
      inset-block: 0;
      right: var(--base-size-2);
      display: grid;
      place-items: center;
      width: var(--control-large-size);
      padding: 0;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
    }
    .password-toggle svg { width: var(--base-size-20); height: var(--base-size-20); }
    .password-toggle:hover { color: var(--lv-fg-default); }
    .password-toggle:focus-visible { outline: var(--focus-outline); outline-offset: var(--base-size-2); }

    .submit {
      display: inline-grid;
      min-height: var(--control-xlarge-size);
      width: 100%;
      place-items: center;
      border: var(--borderWidth-default) solid var(--lv-button-accent-border-rest);
      border-radius: var(--lv-button-radius);
      background: var(--lv-button-accent-bg-rest);
      color: var(--lv-button-accent-fg-rest);
      cursor: pointer;
      padding: 0 var(--lv-button-padding-inline-spacious);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      box-shadow: var(--lv-button-shadow-resting);
    }

    .divider {
      width: 100%;
      border-top: var(--lv-border-muted);
    }

    .error {
      width: 100%;
      box-sizing: border-box;
      border: var(--borderWidth-default) solid var(--display-red-borderColor-muted);
      border-radius: var(--lv-radius-default);
      background: var(--display-red-bgColor-muted);
      color: var(--display-red-fgColor);
      padding: var(--base-size-8) var(--base-size-12);
      text-align: left;
      font: var(--lv-type-caption);
    }

    .brand-name { font: var(--lv-type-page-title); font-size: calc(var(--text-title-size-medium) * 1.1); }
    .panel-heading { text-align: center; }
    .panel-heading p { margin: var(--base-size-8) 0 0; color: var(--lv-fg-muted); font: var(--lv-type-body); }
    .access-help { margin: 0; padding-top: var(--base-size-16); border-top: var(--lv-border-muted); color: var(--lv-fg-muted); font: var(--lv-type-caption); text-align: center; }
    .submit:hover { background: var(--lv-button-accent-bg-hover); }
    .submit:focus-visible { outline: var(--focus-outline); outline-offset: var(--base-size-4); }
    .divider { display: flex; align-items: center; gap: var(--base-size-12); border: 0; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .divider::before, .divider::after { content: ''; flex: 1; border-top: var(--lv-border-muted); }
    @media (max-height: 700px), (max-width: 520px) {
      .layout { padding: var(--base-size-64) var(--base-size-16); }
      .brand-lockup { top: var(--base-size-16); left: var(--base-size-16); }
      .theme { top: var(--base-size-16); right: var(--base-size-16); }
      .panel { padding: var(--base-size-24); gap: var(--base-size-16); }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    this.themeMode = currentThemeMode()
    document.addEventListener('leapview-theme-applied', this.handleThemeApplied)
  }

  disconnectedCallback(): void {
    document.removeEventListener('leapview-theme-applied', this.handleThemeApplied)
    super.disconnectedCallback()
  }

  updated(): void {
    checkSignalContract('login page', this.page, { kind: 'required', title: 'required', providerLabel: 'required' })
  }

  get page(): LoginPageSignal | null {
    return this.signal<LoginPageSignal | null>('page', null)
  }

  get status(): DashboardStatus {
    return this.signal<DashboardStatus>('status', emptyDashboardStatus())
  }

  render() {
    const page = this.page
    const nextMode = nextThemeMode[this.themeMode]
    const themeLabel = `${themeLabels[this.themeMode]}. Switch to ${themeLabels[nextMode]}.`
    const localAuth = page?.localAuth ?? false
    const ssoAuth = page?.ssoAuth ?? true
    const mustChangePassword = page?.mustChangePassword ?? false
    return html`
      <div class="layout">
      <lv-topology-background
        data-login-background
        data-module-src=${page?.backgroundModuleSrc ?? this.backgroundModuleSrc}
      ></lv-topology-background>
      <div class="scrim" aria-hidden="true"></div>
        <header class="brand-lockup">
          <lv-brand-mark aria-hidden="true"></lv-brand-mark>
          <span class="brand-name">${page?.title ?? leapViewBrandName}</span>
        </header>
      <button
        class="theme"
        type="button"
        data-theme-toggle
        data-theme-mode=${this.themeMode}
        aria-label=${themeLabel}
        title=${themeLabel}
        @click=${this.toggleTheme}
      >
        <span data-theme-icon="system" ?hidden=${this.themeMode !== 'system'}>${lucideIcon(Monitor)}</span>
        <span data-theme-icon="light" ?hidden=${!isLightTheme(this.themeMode)}>${lucideIcon(Sun)}</span>
        <span data-theme-icon="dark" ?hidden=${!isDarkTheme(this.themeMode)}>${lucideIcon(Moon)}</span>
      </button>
      <section class="panel" aria-labelledby="login-heading">


        <div class="panel-heading">
          <h1 id="login-heading">${mustChangePassword ? 'Set a new password' : 'Welcome back'}</h1>
          <p>${mustChangePassword ? 'Choose a new password to continue.' : 'Sign in to LeapView.'}</p>
        </div>
        ${this.status.error ? html`<div class="error" role="alert" aria-live="assertive">${this.status.error}</div>` : ''}
        ${mustChangePassword ? html`
          <form method="post" action="/auth/local/password">
            <input type="hidden" name="gorilla.csrf.Token" value=${csrfToken()}>
            ${this.passwordField('currentPassword', 'Temporary password')}
            ${this.passwordField('newPassword', 'New password')}
            <button class="submit" type="submit">Change password</button>
          </form>
        ` : localAuth ? html`
          <form method="post" action="/auth/local/login">
            <input type="hidden" name="gorilla.csrf.Token" value=${csrfToken()}>
            <label>
              Email
              <input name="email" type="email" autocomplete="username" placeholder="you@company.com" required>
            </label>
            ${this.passwordField('password', 'Password')}
            <button class="submit" type="submit">Sign in</button>
          </form>
        ` : ''}
        ${!mustChangePassword && localAuth && ssoAuth ? html`<div class="divider" aria-hidden="true">or</div>` : ''}
        ${!mustChangePassword && ssoAuth ? html`
          <a class="provider" href="/auth/azureadv2">
            <span class="provider-mark" aria-hidden="true"><span></span><span></span><span></span><span></span></span>
            <span>${page?.providerLabel ?? 'Sign in with Azure Active Directory'}</span>
          </a>
        ` : ''}
        <p class="access-help">Need access? Contact your administrator.</p>
        </section>
      </div>
    `
  }

  private passwordField(name: 'password' | 'currentPassword' | 'newPassword', label: string) {
    const visible = this.revealedPasswords.has(name)
    const id = `login-${name}`
    const action = `${visible ? 'Hide' : 'Show'} ${label.toLowerCase()}`
    return html`
      <div class="password-field">
        <label for=${id}>${label}</label>
        <div class="password-control">
          <input id=${id} name=${name} type=${visible ? 'text' : 'password'}
            autocomplete=${name === 'newPassword' ? 'new-password' : 'current-password'}
            minlength=${ifDefined(name === 'newPassword' ? 12 : undefined)}
            maxlength=${ifDefined(name === 'password' ? undefined : 1024)}
            aria-describedby=${ifDefined(name === 'newPassword' ? 'new-password-requirements' : undefined)} required>
          <button class="password-toggle" type="button" aria-label=${action} title=${action} aria-controls=${id}
            @click=${() => {
              if (visible) this.revealedPasswords.delete(name)
              else this.revealedPasswords.add(name)
              this.requestUpdate()
            }}>${lucideIcon(visible ? EyeOff : Eye)}</button>
        </div>
        ${name === 'newPassword' ? html`<span id="new-password-requirements" class="field-hint">Use at least 12 characters.</span>` : ''}
      </div>
    `
  }

  private toggleTheme(): void {
    const mode = nextThemeMode[this.themeMode]
    this.themeMode = mode
    this.requestUpdate()
    document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode } }))
  }

  private get backgroundModuleSrc(): string {
    return this.getAttribute('background-module-src')?.trim() || '/static/topology-background.js'
  }
}

if (!customElements.get('lv-login-page')) customElements.define('lv-login-page', LeapViewLoginPage)

function currentThemeMode(): ThemePreference {
  try {
    return normalizeThemeMode(localStorage.getItem('leapview-color-mode'))
  } catch {
    const colorMode = document.documentElement.dataset.colorMode
    return colorMode === 'light' || colorMode === 'dark' ? colorMode : 'system'
  }
}

function csrfToken(): string {
  return document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')?.content.trim() ?? ''
}

function normalizeThemeMode(mode: string | null | undefined): ThemePreference {
  switch (mode) {
    case 'system':
    case 'light':
    case 'dark':
    case 'dark_dimmed':
    case 'light_colorblind':
    case 'dark_colorblind':
    case 'light_tritanopia':
    case 'dark_tritanopia':
      return mode
    default:
      return 'system'
  }
}

function isLightTheme(mode: ThemePreference): boolean {
  return mode === 'light' || mode === 'light_colorblind' || mode === 'light_tritanopia'
}

function isDarkTheme(mode: ThemePreference): boolean {
  return mode === 'dark' || mode === 'dark_dimmed' || mode === 'dark_colorblind' || mode === 'dark_tritanopia'
}
