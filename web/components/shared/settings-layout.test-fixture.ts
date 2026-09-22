import { LitElement, css, html, unsafeCSS } from 'lit'
import { typographyTestTokens } from '../test-typography-tokens'
import { settingsFieldStyles } from './settings-field-styles'
import { renderSettingsActions, renderSettingsRow, renderSettingsSection, settingsLayoutStyles } from './settings-layout'

class SettingsFixture extends LitElement {
  static styles = [settingsFieldStyles, settingsLayoutStyles, css`
    :host { display: block; width: 720px; ${unsafeCSS(typographyTestTokens)}
      --base-size-4: 4px; --base-size-6: 6px; --base-size-8: 8px; --base-size-12: 12px;
      --base-size-16: 16px; --base-size-20: 20px; --base-size-24: 24px; --base-size-64: 64px;
      --control-medium-size: 32px; --borderWidth-thick: 2px;
      --lv-border-muted: 1px solid currentColor; --lv-border-default: 1px solid currentColor;
    }
  `]
  private submitted = ''
  private clicks = 0

  render() {
    return html`<div class="settings-stack">
      ${renderSettingsSection({ label: 'Identity', heading: 'Identity', description: 'Shared form controls.', content: html`
        <form @submit=${(event: SubmitEvent) => {
          event.preventDefault()
          this.submitted = String(new FormData(event.currentTarget as HTMLFormElement).get('name'))
          this.requestUpdate()
        }}>
          ${renderSettingsRow({ label: 'Display name', description: 'Visible to colleagues.', controlId: 'name', control: renderSettingsActions(html`
            <input class="settings-input" id="name" name="name" required>
            <button class="settings-button primary" type="submit">Save</button>
          `, { stack: true }) })}
        </form>
      ` })}
      ${renderSettingsSection({ label: 'Account', appearance: 'card', content: html`
        ${renderSettingsRow({ label: 'Account identifier', layout: 'action', control: html`<span class="settings-value">${'identifier'.repeat(12)}</span>` })}
        ${renderSettingsRow({ label: 'Pending action', layout: 'action', control: html`<button class="settings-button" disabled @click=${() => { this.clicks++; this.requestUpdate() }}>Unavailable</button>` })}
      ` })}
      ${renderSettingsSection({ label: 'Status', appearance: 'plain', content: html`
        ${renderSettingsRow({ label: 'Connection', layout: 'stacked', control: html`<span class="settings-value">Connected</span>` })}
      ` })}
      <output aria-label="Submitted name">${this.submitted}</output><output aria-label="Action count">${this.clicks}</output>
    </div>`
  }
}
customElements.define('lv-settings-fixture', SettingsFixture)
