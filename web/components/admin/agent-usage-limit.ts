import { LitElement, css, html, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import { browserCommandFailure, ownsBrowserCommandFetch } from '../shared/command-failure'

class AgentUsageLimit extends LitElement {
  @property({ type: Number }) limit = 100
  @property({ type: Number }) used = 0
  @property({ type: String }) resetsAt = ''
  @property({ type: Boolean }) disabled = false
  @state() private draft = '100'
  @state() private busy = false
  @state() private error = ''

  static styles = css`
    :host { display: block; min-width: 0; }
    .controls { display: flex; flex-wrap: wrap; align-items: end; gap: var(--base-size-12); }
    label { display: grid; gap: var(--base-size-8); font: var(--lv-type-body-compact); }
    input, button { box-sizing: border-box; padding: var(--base-size-8) var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); font: var(--lv-type-body-compact); }
    input { width: 12rem; max-width: 100%; color: var(--lv-fg-default); background: var(--lv-bg-panel); }
    button { background: var(--lv-bg-accent); color: var(--lv-fg-on-accent); cursor: pointer; }
    button:disabled { opacity: .5; cursor: default; }
    input:focus-visible, button:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
    p { font: var(--lv-type-body-compact); color: var(--lv-fg-muted); }
    [role=alert] { color: var(--lv-fg-danger); }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.onFetch)
  }

  disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.onFetch)
    super.disconnectedCallback()
  }

  protected willUpdate(changed: PropertyValues): void {
    if (changed.has('limit')) this.draft = String(this.limit)
  }

  private onFetch = (event: Event): void => {
    if (!this.busy || !ownsBrowserCommandFetch(this, event)) return
    const failure = browserCommandFailure(event, 'Agent limit update')
    if (!failure) return
    this.busy = false
    this.error = failure.message
  }

  private save = (): void => {
    if (this.disabled || this.busy) return
    const limit = Number(this.draft)
    if (!Number.isInteger(limit) || limit < 1 || limit > 1_000_000) {
      this.error = 'Enter a whole number from 1 to 1,000,000.'
      return
    }
    this.error = ''
    this.busy = true
    // The server reloads the saved settings after success. Keep the draft on failure.
    this.dispatchEvent(new CustomEvent('lv-agent-usage-limit-save', {
      bubbles: true, composed: true, detail: { dailyRequestLimit: limit },
    }))
  }

  render() {
    return html`
      ${this.disabled ? html`<p>Read-only</p>` : ''}
      <div class="controls">
        <label for="daily-limit">Requests per day for this instance
          <input id="daily-limit" type="number" min="1" max="1000000" step="1" .value=${this.draft}
            ?disabled=${this.disabled || this.busy} @input=${(event: Event) => { this.draft = (event.target as HTMLInputElement).value; this.error = '' }}>
        </label>
        <button type="button" ?disabled=${this.disabled || this.busy || this.draft === String(this.limit)} @click=${this.save}>${this.busy ? 'Saving…' : 'Save'}</button>
      </div>
      <p>${this.used.toLocaleString()} of ${this.limit.toLocaleString()} requests used today · ${Math.max(0, this.limit - this.used).toLocaleString()} remaining.</p>
      <p>Shared by all users. Resets daily at midnight UTC${this.resetsAt ? html` (<time datetime=${this.resetsAt}>${this.resetsAt.slice(0, 10)} 00:00 UTC</time>)` : ''}.</p>
      <p>Each model call counts, including tool follow-ups, conversation titles, context compaction, and failed attempts. Changing the limit keeps today’s usage.</p>
      ${this.error ? html`<p role="alert">${this.error}</p>` : ''}
    `
  }
}

if (!customElements.get('lv-agent-usage-limit')) customElements.define('lv-agent-usage-limit', AgentUsageLimit)
