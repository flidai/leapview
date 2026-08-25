import { LitElement, css, html } from 'lit'
import { Monitor, Moon, Sun } from 'lucide'
import type { LoginPageSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { leapViewBrandName } from '../shared/brand-mark'
import { checkSignalContract } from '../shared/signal-contract'
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
  private themeMode: ThemePreference = currentThemeMode()
  private readonly handleThemeApplied = (event: Event) => {
    const detail = (event as CustomEvent<{ mode?: string }>).detail
    this.themeMode = normalizeThemeMode(detail?.mode)
    this.requestUpdate()
  }

  static styles = css`
    :host {
      position: relative;
      display: grid;
      width: 100%;
      height: 100svh;
      min-height: 100svh;
      place-items: center;
      place-content: center;
      overflow: hidden;
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      padding: var(--base-size-24);
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
      top: var(--base-size-16);
      right: var(--base-size-16);
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
      outline: 0;
    }

    .theme [hidden] {
      display: none;
    }

    .panel {
      position: relative;
      z-index: var(--zIndex-modal, 30);
      display: grid;
      width: min(100%, var(--lv-login-panel-width));
      justify-items: center;
      gap: var(--base-size-20);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-24);
      text-align: center;
      box-shadow: var(--shadow-resting-medium, var(--shadow-resting-small));
      box-sizing: border-box;
    }

    h1 {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-page-title);
    }

    .brand-lockup {
      display: flex;
      align-items: center;
      gap: var(--base-size-12);
    }

    .brand-lockup lv-brand-mark {
      --lv-brand-mark-size: var(--base-size-32);
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

    @media (max-width: 520px) {
      :host {
        padding: var(--base-size-16);
      }

      .theme {
        top: var(--base-size-12);
        right: var(--base-size-12);
      }
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

  render() {
    const page = this.page
    const nextMode = nextThemeMode[this.themeMode]
    const themeLabel = `${themeLabels[this.themeMode]}. Switch to ${themeLabels[nextMode]}.`
    const localAuth = page?.localAuth ?? false
    const ssoAuth = page?.ssoAuth ?? true
    const mustChangePassword = page?.mustChangePassword ?? false
    return html`
      <lv-topology-background
        data-login-background
        data-module-src=${page?.backgroundModuleSrc ?? this.backgroundModuleSrc}
      ></lv-topology-background>
      <div class="scrim" aria-hidden="true"></div>
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
      <section class="panel" aria-label="${leapViewBrandName} login">
        <div class="brand-lockup">
          <lv-brand-mark aria-hidden="true"></lv-brand-mark>
          <h1>${page?.title ?? leapViewBrandName}</h1>
        </div>
        ${mustChangePassword ? html`
          <form method="post" action="/auth/local/password">
            <input type="hidden" name="gorilla.csrf.Token" value=${csrfToken()}>
            <label>
              Temporary password
              <input name="currentPassword" type="password" autocomplete="current-password" maxlength="1024" required>
            </label>
            <label>
              New password
              <input name="newPassword" type="password" autocomplete="new-password" minlength="12" maxlength="1024" aria-describedby="new-password-requirements" required>
              <span id="new-password-requirements" class="field-hint">Use at least 12 characters.</span>
            </label>
            <button class="submit" type="submit">Change password</button>
          </form>
        ` : localAuth ? html`
          <form method="post" action="/auth/local/login">
            <input type="hidden" name="gorilla.csrf.Token" value=${csrfToken()}>
            <label>
              Email
              <input name="email" type="email" autocomplete="username" required>
            </label>
            <label>
              Password
              <input name="password" type="password" autocomplete="current-password" required>
            </label>
            <button class="submit" type="submit">Sign in</button>
          </form>
        ` : ''}
        ${!mustChangePassword && localAuth && ssoAuth ? html`<div class="divider" aria-hidden="true"></div>` : ''}
        ${!mustChangePassword && ssoAuth ? html`
          <a class="provider" href="/auth/azureadv2">
            <span class="provider-mark" aria-hidden="true"><span></span><span></span><span></span><span></span></span>
            <span>${page?.providerLabel ?? 'Sign in with Azure Active Directory'}</span>
          </a>
        ` : ''}
      </section>
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
