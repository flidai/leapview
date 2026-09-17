import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Check, Copy, KeyRound } from 'lucide'
import { lucideIcon } from './lucide-icons'

export class OneTimeSecret extends LitElement {
  @property() secret = ''
  @property() message = 'Copy this secret now. You will not be able to see it again.'
  @property({ attribute: 'copy-label' }) copyLabel = 'Copy secret'
  @state() private copied = false
  @state() private copyError = ''

  static styles = css`
    :host { display: block; min-width: 0; }
    .secret {
      display: grid;
      min-width: 0;
      grid-template-columns: auto minmax(0, 1fr) auto;
      align-items: center;
      gap: var(--base-size-12);
      border: 1px solid var(--lv-fg-success);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-12);
      color: var(--lv-fg-default);
      background: var(--lv-bg-success-muted);
    }
    .icon { display: inline-flex; color: var(--lv-fg-success); }
    .copy { display: grid; min-width: 0; gap: var(--base-size-6); }
    strong { font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); }
    code {
      overflow: hidden;
      min-width: 0;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small);
      padding: var(--base-size-8) var(--base-size-12);
      background: var(--lv-bg-input);
      text-overflow: ellipsis;
      white-space: nowrap;
      user-select: all;
    }
    button {
      display: inline-flex;
      min-height: var(--control-medium-size, var(--lv-control-medium));
      align-items: center;
      justify-content: center;
      gap: var(--base-size-6);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small);
      padding: 0 var(--lv-space-control, var(--base-size-10));
      color: var(--lv-fg-default);
      background: var(--lv-button-bg-rest);
      cursor: pointer;
      font: var(--lv-type-body-compact);
    }
    .error { grid-column: 2 / -1; margin: 0; color: var(--lv-fg-danger); font: var(--lv-type-caption); }
    @media (max-width: 36rem) {
      .secret { grid-template-columns: auto minmax(0, 1fr); }
      button { grid-column: 2; justify-self: start; }
      .error { grid-column: 2; }
    }
  `

  protected willUpdate(changed: Map<string, unknown>): void {
    if (changed.has('secret')) {
      this.copied = false
      this.copyError = ''
    }
  }

  render() {
    return html`<div class="secret" role="status">
      <span class="icon" aria-hidden="true">${lucideIcon(KeyRound, { size: 20, strokeWidth: 2 })}</span>
      <div class="copy"><strong>${this.message}</strong><code title=${this.secret}>${this.secret}</code></div>
      <button type="button" aria-label=${this.copyLabel} @click=${this.copy}>${lucideIcon(this.copied ? Check : Copy, { size: 15, strokeWidth: 2 })}<span>${this.copied ? 'Copied' : 'Copy'}</span></button>
      ${this.copyError ? html`<p class="error" role="alert">${this.copyError}</p>` : nothing}
    </div>`
  }

  private copy = async (): Promise<void> => {
    if (!this.secret) return
    try {
      await navigator.clipboard.writeText(this.secret)
      this.copied = true
      this.copyError = ''
    } catch {
      this.copyError = 'Could not copy this value. Select it and copy it manually.'
    }
  }
}

if (!customElements.get('lv-one-time-secret')) customElements.define('lv-one-time-secret', OneTimeSecret)

declare global {
  interface HTMLElementTagNameMap {
    'lv-one-time-secret': OneTimeSecret
  }
}
